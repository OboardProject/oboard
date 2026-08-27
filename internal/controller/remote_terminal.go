package controller

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	terminalCookieName       = "oboard_terminal_ticket"
	terminalAbsoluteLifetime = time.Hour
	terminalMaxMessage       = 64 << 10
	terminalMaxPerServer     = 2
	terminalMaxPerUser       = 4
	terminalMaxCols          = 400
	terminalMaxRows          = 150
)

type terminalSession struct {
	ID         string
	ServerID   int64
	UserID     int64
	Nonce      string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	Ticket     string
	TicketUsed bool
	Cols       int
	Rows       int
	PrepareExp string
	browser    *websocket.Conn
	agent      *websocket.Conn
	mu         sync.Mutex
}

type terminalSessionHub struct {
	mu       sync.Mutex
	sessions map[string]*terminalSession
}

func newTerminalSessionHub() *terminalSessionHub {
	return &terminalSessionHub{sessions: map[string]*terminalSession{}}
}

func (h *terminalSessionHub) countForServer(serverID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, session := range h.sessions {
		if session.ServerID == serverID {
			n++
		}
	}
	return n
}

func (h *terminalSessionHub) countForUser(userID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, session := range h.sessions {
		if session.UserID == userID {
			n++
		}
	}
	return n
}

func (s *Server) serverTerminal(w http.ResponseWriter, r *http.Request, serverID int64, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			s.listTerminalSessions(w, r, serverID)
		case http.MethodPost:
			if rest == nil || (len(rest) == 1 && rest[0] == "sessions") || len(rest) == 0 {
				s.createTerminalSession(w, r, serverID)
				return
			}
			method(w)
		default:
			method(w)
		}
		return
	}
	if rest[0] == "sessions" {
		if len(rest) == 1 && r.Method == http.MethodPost {
			s.createTerminalSession(w, r, serverID)
			return
		}
		if len(rest) == 1 && r.Method == http.MethodGet {
			s.listTerminalSessions(w, r, serverID)
			return
		}
		if len(rest) == 2 && r.Method == http.MethodDelete {
			s.closeTerminalSession(w, r, serverID, rest[1])
			return
		}
	}
	if rest[0] == "ws" && len(rest) == 2 {
		s.browserTerminalWS(w, r, serverID, rest[1])
		return
	}
	fail(w, errNotFound(), http.StatusNotFound)
}

func errNotFound() error { return codedError("not_found", "not found") }

func (s *Server) createTerminalSession(w http.ResponseWriter, r *http.Request, serverID int64) {
	user := currentUser(r)
	if user == nil {
		failCode(w, "terminal_auth_expired", "invalid session", http.StatusUnauthorized)
		return
	}
	var req struct {
		StepUpToken string `json:"step_up_token"`
		Cols        int    `json:"cols"`
		Rows        int    `json:"rows"`
	}
	if !decode(w, r, &req) {
		return
	}
	if settingBool(s.runtimeSettings(r.Context()), settingRemoteTerminalPasswordConfirmationEnabled, true) {
		if err := s.consumeStepUp(r, req.StepUpToken, model.StepUpPurposeRemoteTerminal, "server", serverIDString(serverID)); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
	}
	server, err := s.store.GetServer(r.Context(), serverID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	view, err := s.remoteAccessView(r, server)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if reasons := s.remoteAccessUnavailableReasons(server, view, "remote_terminal"); len(reasons) > 0 {
		failCode(w, reasons[0], "remote terminal is unavailable", http.StatusConflict)
		s.recordRemoteAccessAudit(r, model.RemoteAccessAuditEvent{
			EventType: model.RemoteAccessAuditTerminalDenied, ActorType: "user", ActorUserID: &user.ID,
			ServerID: &serverID, Result: reasons[0], Capability: "remote_terminal",
		})
		return
	}
	if s.terminalHub.countForServer(serverID) >= terminalMaxPerServer || s.terminalHub.countForUser(user.ID) >= terminalMaxPerUser {
		failCode(w, "terminal_limit_exceeded", "too many active terminals", http.StatusConflict)
		return
	}
	cols, rows := req.Cols, req.Rows
	if cols <= 0 || cols > terminalMaxCols {
		cols = 120
	}
	if rows <= 0 || rows > terminalMaxRows {
		rows = 32
	}
	sessionID, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	nonce, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	ticket, err := security.RandomToken(24)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	expires := now.Add(security.InteractivePrepareTTL)
	session := &terminalSession{
		ID: sessionID, ServerID: serverID, UserID: user.ID, Nonce: nonce, ExpiresAt: now.Add(terminalAbsoluteLifetime),
		CreatedAt: now, Ticket: ticket, Cols: cols, Rows: rows, PrepareExp: expires.Format(time.RFC3339Nano),
	}
	s.terminalHub.mu.Lock()
	s.terminalHub.sessions[sessionID] = session
	s.terminalHub.mu.Unlock()
	envelope := security.InteractiveEnvelope{
		Type: "interactive_prepare", ServerID: serverID, SessionID: sessionID, Nonce: nonce,
		IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: session.PrepareExp,
		Kind: "terminal", Cols: cols, Rows: rows,
	}
	payload := map[string]any{
		"type": "interactive_prepare", "signature_version": security.InteractiveSignatureV1,
		"server_id": serverID, "session_id": sessionID, "nonce": nonce,
		"issued_at": envelope.IssuedAt, "expires_at": envelope.ExpiresAt, "kind": "terminal",
		"cols": cols, "rows": rows, "signature": security.SignInteractiveEnvelope(server.AgentTokenHash, envelope),
		"ts": now,
	}
	if !s.sendAgentControl(serverID, payload) {
		s.terminalHub.mu.Lock()
		delete(s.terminalHub.sessions, sessionID)
		s.terminalHub.mu.Unlock()
		failCode(w, "agent_offline", "agent control channel is unavailable", http.StatusConflict)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: terminalCookieName, Value: ticket, Path: s.terminalCookiePath(r, serverID),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		MaxAge: 120,
	})
	s.recordRemoteAccessAudit(r, model.RemoteAccessAuditEvent{
		EventType: model.RemoteAccessAuditTerminalOpen, ActorType: "user", ActorUserID: &user.ID,
		ServerID: &serverID, SessionID: sessionID, Result: "opened", Capability: "remote_terminal",
	})
	write(w, http.StatusCreated, map[string]any{"session_id": sessionID, "expires_at": session.ExpiresAt})
}

func (s *Server) terminalCookiePath(r *http.Request, serverID int64) string {
	prefix := strings.TrimRight(s.currentBasePath(), "/")
	return prefix + "/api/v1/ui/servers/" + strconv.FormatInt(serverID, 10) + "/terminal/"
}

func (s *Server) listTerminalSessions(w http.ResponseWriter, r *http.Request, serverID int64) {
	user := currentUser(r)
	s.terminalHub.mu.Lock()
	defer s.terminalHub.mu.Unlock()
	items := []map[string]any{}
	for _, session := range s.terminalHub.sessions {
		if session.ServerID != serverID {
			continue
		}
		if user != nil && session.UserID != user.ID && !roleAllows(currentRole(r), model.RoleAdmin) {
			continue
		}
		items = append(items, map[string]any{"session_id": session.ID, "created_at": session.CreatedAt, "expires_at": session.ExpiresAt})
	}
	write(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) closeTerminalSession(w http.ResponseWriter, r *http.Request, serverID int64, sessionID string) {
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	if session != nil && session.ServerID == serverID {
		delete(s.terminalHub.sessions, sessionID)
	} else {
		session = nil
	}
	s.terminalHub.mu.Unlock()
	if session == nil {
		fail(w, errNotFound(), http.StatusNotFound)
		return
	}
	session.close("user_close")
	_ = s.sendAgentControl(serverID, map[string]any{"type": "interactive_close", "session_id": sessionID, "ts": time.Now().UTC()})
	write(w, http.StatusOK, map[string]any{"closed": true})
}

func (session *terminalSession) close(reason string) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.browser != nil {
		_ = session.browser.WriteJSON(map[string]any{"type": "closed", "reason": reason})
		_ = session.browser.Close()
	}
	if session.agent != nil {
		_ = session.agent.WriteJSON(map[string]any{"type": "close"})
		_ = session.agent.Close()
	}
}

func (s *Server) browserTerminalWS(w http.ResponseWriter, r *http.Request, serverID int64, sessionID string) {
	if r.Header.Get("Origin") == "" || !s.originAllowed(r, r.Header.Get("Origin")) {
		failCode(w, "origin_denied", "origin is not allowed", http.StatusForbidden)
		return
	}
	cookie, err := r.Cookie(terminalCookieName)
	if err != nil || cookie.Value == "" {
		failCode(w, "terminal_auth_expired", "terminal ticket missing", http.StatusUnauthorized)
		return
	}
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	if session == nil || session.ServerID != serverID || session.TicketUsed || session.Ticket != cookie.Value {
		s.terminalHub.mu.Unlock()
		failCode(w, "terminal_auth_expired", "terminal ticket is invalid", http.StatusUnauthorized)
		return
	}
	session.TicketUsed = true
	s.terminalHub.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: terminalCookieName, Value: "", Path: s.terminalCookiePath(r, serverID), MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	upgrader := websocket.Upgrader{
		CheckOrigin: func(req *http.Request) bool {
			origin := req.Header.Get("Origin")
			return origin != "" && s.originAllowed(req, origin)
		},
		EnableCompression: false, ReadBufferSize: 4096, WriteBufferSize: 4096,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(terminalMaxMessage)
	session.mu.Lock()
	session.browser = conn
	session.mu.Unlock()
	s.relayTerminal(session)
}

func (s *Server) agentInteractive(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	sessionID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/agent/interactive/"), "/")
	if sessionID == "" {
		fail(w, errNotFound(), http.StatusNotFound)
		return
	}
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	s.terminalHub.mu.Unlock()
	if session == nil || session.ServerID != server.ID {
		failCode(w, "not_found", "interactive session not found", http.StatusNotFound)
		return
	}
	proof := r.Header.Get("X-OBoard-Interactive-Proof")
	if !security.VerifyInteractiveProof(server.AgentTokenHash, session.ID, session.ServerID, session.Nonce, session.PrepareExp, proof) {
		failCode(w, "terminal_auth_expired", "interactive proof is invalid", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: s.checkOrigin, EnableCompression: false, ReadBufferSize: 4096, WriteBufferSize: 4096}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(terminalMaxMessage)
	session.mu.Lock()
	session.agent = conn
	session.mu.Unlock()
	s.relayTerminal(session)
}

func (s *Server) relayTerminal(session *terminalSession) {
	session.mu.Lock()
	browser := session.browser
	agent := session.agent
	session.mu.Unlock()
	if browser == nil || agent == nil {
		return
	}
	copy := func(src, dst *websocket.Conn, toAgent bool) {
		for {
			mt, data, err := src.ReadMessage()
			if err != nil {
				session.close("peer_closed")
				return
			}
			if len(data) > terminalMaxMessage {
				session.close("oversized_frame")
				return
			}
			if mt == websocket.TextMessage && toAgent {
				var msg struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(data, &msg) != nil || (msg.Type != "resize" && msg.Type != "close" && msg.Type != "ping") {
					continue
				}
			}
			if err := dst.WriteMessage(mt, data); err != nil {
				session.close("slow_consumer")
				return
			}
		}
	}
	go copy(browser, agent, true)
	go copy(agent, browser, false)
}
