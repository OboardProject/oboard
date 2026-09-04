package controller

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	terminalCookieName       = "oboard_terminal_ticket"
	terminalAbsoluteLifetime = time.Hour
	terminalMaxMessage       = 64 << 10
	terminalMaxPerServer     = 2
	terminalMaxPerUser       = 16
	terminalMaxCols          = 400
	terminalMaxRows          = 150

	terminalMCPBufferMax      = 256 << 10
	terminalMCPReadMax        = 64 << 10
	terminalMCPMaxPerGrant    = 4
	terminalMCPMaxPerServer   = 1
	terminalMCPMaxInput       = 64 << 10
	terminalMCPMaxWaitMS      = 3000
	terminalIdleTimeout       = 15 * time.Minute
	terminalAbsTimeout        = time.Hour
)

var terminalPrepareTimeout = 20 * time.Second

type InteractiveOwnerType string

const (
	InteractiveOwnerHuman InteractiveOwnerType = "human"
	InteractiveOwnerMCP   InteractiveOwnerType = "mcp"
)

type TerminalOutputBuffer struct {
	mu        sync.Mutex
	chunks    []bufferChunk
	total     int
	maxBytes  int
	nextSeq   int64
	oldestSeq int64
}

type bufferChunk struct {
	seq  int64
	data []byte
}

func newTerminalOutputBuffer(maxBytes int) *TerminalOutputBuffer {
	if maxBytes <= 0 {
		maxBytes = terminalMCPBufferMax
	}
	return &TerminalOutputBuffer{maxBytes: maxBytes, nextSeq: 1, oldestSeq: 1}
}

func (b *TerminalOutputBuffer) Write(p []byte) int64 {
	if len(p) == 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	seq := b.nextSeq
	b.nextSeq++
	cp := make([]byte, len(p))
	copy(cp, p)
	b.chunks = append(b.chunks, bufferChunk{seq: seq, data: cp})
	b.total += len(cp)
	// evict oldest until within limit
	for b.total > b.maxBytes && len(b.chunks) > 0 {
		b.total -= len(b.chunks[0].data)
		b.chunks = b.chunks[1:]
		if len(b.chunks) > 0 {
			b.oldestSeq = b.chunks[0].seq
		} else {
			b.oldestSeq = b.nextSeq
		}
	}
	if len(b.chunks) == 1 && seq == 1 {
		b.oldestSeq = 1
	}
	return seq
}

func (b *TerminalOutputBuffer) ReadAfter(after int64, maxBytes int) (out []byte, nextCursor int64, lost bool, oldest int64, truncated bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if maxBytes <= 0 {
		maxBytes = terminalMCPReadMax
	}
	if maxBytes > 64<<10 {
		maxBytes = 64 << 10
	}
	oldest = b.oldestSeq
	if after < oldest-1 && len(b.chunks) > 0 {
		lost = true
	}
	// collect
	var buf []byte
	var lastSeq int64 = after
	for _, ch := range b.chunks {
		if ch.seq <= after {
			continue
		}
		if len(buf)+len(ch.data) > maxBytes {
			need := maxBytes - len(buf)
			if need > 0 {
				buf = append(buf, ch.data[:need]...)
				lastSeq = ch.seq
				truncated = true
			}
			break
		}
		buf = append(buf, ch.data...)
		lastSeq = ch.seq
	}
	if len(buf) == 0 {
		nextCursor = after
		if b.nextSeq > 1 {
			// if no data, next cursor stays at after, but we may indicate current tip
			// keep after as per spec, caller will poll
		}
		return buf, nextCursor, lost, oldest, truncated
	}
	nextCursor = lastSeq
	return buf, nextCursor, lost, oldest, truncated
}

func (b *TerminalOutputBuffer) TotalBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

func (b *TerminalOutputBuffer) OldestSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.oldestSeq
}

func (b *TerminalOutputBuffer) NextSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq
}

type terminalSession struct {
	ID                 string
	ServerID           int64
	OwnerType          InteractiveOwnerType
	UserID             int64
	OAuthGrantID       string
	OAuthClientID      string
	PrivilegedGrantID  int64
	Nonce              string
	ExpiresAt          time.Time
	CreatedAt          time.Time
	LastActivityAt     time.Time
	Ticket             string
	TicketUsed         bool
	Cols               int
	Rows               int
	Mode               string
	LoginEnv           bool
	PrepareExp         string
	Origin             string
	browser            *websocket.Conn
	agent              *websocket.Conn
	outputBuffer       *TerminalOutputBuffer
	Ready              bool
	Closed             bool
	CloseReason        string
	relaying           bool
	agentReady         bool
	closed             bool
	prepareTimer       *time.Timer
	idleTimer          *time.Timer
	absTimer           *time.Timer
	readyCh            chan struct{}
	readyErr           string
	mu                 sync.Mutex
	writeMu            sync.Mutex
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

func (h *terminalSessionHub) countForGrant(grantID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, session := range h.sessions {
		if session.OwnerType == InteractiveOwnerMCP && session.OAuthGrantID == grantID {
			n++
		}
	}
	return n
}

func (h *terminalSessionHub) countForGrantServer(grantID string, serverID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, session := range h.sessions {
		if session.OwnerType == InteractiveOwnerMCP && session.OAuthGrantID == grantID && session.ServerID == serverID {
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

// prepareInteractiveSession is the single chokepoint for creating an interactive PTY session.
// It allocates the session, signs the interactive_prepare envelope with the correct origin and version,
// sends it to the Agent and waits for ready when owner is MCP.
// Human caller is responsible for step-up and remote_terminal privilege;
// MCP caller is responsible for PrivilegedGrant / resource boundary checks.
func (s *Server) prepareInteractiveSession(ctx context.Context, owner InteractiveOwnerType, server *model.Server, userID int64, grantID, clientID string, privilegedGrantID int64, cols, rows int, mode string) (*terminalSession, error) {
	if cols <= 0 || cols > terminalMaxCols {
		cols = 120
	}
	if rows <= 0 || rows > terminalMaxRows {
		rows = 32
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "login"
	}
	if mode != "login" && mode != "minimal" {
		return nil, codedError("terminal_mode_invalid", "terminal mode must be login or minimal")
	}
	// limit checks
	if s.terminalHub.countForServer(server.ID) >= terminalMaxPerServer {
		return nil, codedError("terminal_limit_exceeded", "too many active terminals for this server")
	}
	if owner == InteractiveOwnerHuman {
		if s.terminalHub.countForUser(userID) >= terminalMaxPerUser {
			return nil, codedError("terminal_limit_exceeded", "too many active terminals for this user")
		}
	} else {
		if s.terminalHub.countForGrant(grantID) >= terminalMCPMaxPerGrant {
			return nil, codedError("terminal_limit_exceeded", "too many MCP terminals for this grant")
		}
		if s.terminalHub.countForGrantServer(grantID, server.ID) >= terminalMCPMaxPerServer {
			return nil, codedError("terminal_limit_exceeded", "MCP terminal already open for this server")
		}
	}
	sessionID, err := security.RandomToken(18)
	if err != nil {
		return nil, err
	}
	nonce, err := security.RandomToken(18)
	if err != nil {
		return nil, err
	}
	ticket := ""
	if owner == InteractiveOwnerHuman {
		ticket, err = security.RandomToken(24)
		if err != nil {
			return nil, err
		}
	}
	// Agent capability check: need to handle different origin
	status, err := s.store.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	loginEnv := slices.Contains(status.Capabilities, model.RemoteAccessCapabilityTerminalLoginEnv)
	now := time.Now().UTC()
	prepareNow := now
	if controllerNow, _, ok := s.controllerTimeNow(); ok {
		prepareNow = controllerNow
	}
	prepareExp := prepareNow.Add(security.InteractivePrepareTTL)
	absExp := now.Add(terminalAbsTimeout)
	session := &terminalSession{
		ID: sessionID, ServerID: server.ID, OwnerType: owner, UserID: userID,
		OAuthGrantID: grantID, OAuthClientID: clientID, PrivilegedGrantID: privilegedGrantID,
		Nonce: nonce, ExpiresAt: absExp, CreatedAt: now, LastActivityAt: now,
		Ticket: ticket, Cols: cols, Rows: rows, Mode: mode, LoginEnv: loginEnv, PrepareExp: prepareExp.Format(time.RFC3339Nano),
		Origin: string(owner), readyCh: make(chan struct{}), outputBuffer: nil,
	}
	if owner == InteractiveOwnerMCP {
		session.outputBuffer = newTerminalOutputBuffer(terminalMCPBufferMax)
		session.Origin = model.InteractiveOriginMCP
	} else {
		session.Origin = model.InteractiveOriginHuman
	}
	s.terminalHub.mu.Lock()
	s.terminalHub.sessions[sessionID] = session
	s.terminalHub.mu.Unlock()

	var envelope security.InteractiveEnvelope
	var sig string
	var sigVersion int
	if owner == InteractiveOwnerMCP {
		envelope = security.InteractiveEnvelope{
			Type: "interactive_prepare", ServerID: server.ID, SessionID: sessionID, Nonce: nonce,
			IssuedAt: prepareNow.Format(time.RFC3339Nano), ExpiresAt: session.PrepareExp,
			Kind: "terminal", Origin: model.InteractiveOriginMCP, Cols: cols, Rows: rows, Mode: mode,
		}
		sig = security.SignInteractiveEnvelopeV2(server.AgentTokenHash, envelope)
		sigVersion = security.InteractiveSignatureV2
	} else {
		envelope = security.InteractiveEnvelope{
			Type: "interactive_prepare", ServerID: server.ID, SessionID: sessionID, Nonce: nonce,
			IssuedAt: prepareNow.Format(time.RFC3339Nano), ExpiresAt: session.PrepareExp,
			Kind: "terminal", Cols: cols, Rows: rows,
		}
		sig = security.SignInteractiveEnvelope(server.AgentTokenHash, envelope)
		sigVersion = security.InteractiveSignatureV1
	}
	payload := map[string]any{
		"type": "interactive_prepare", "signature_version": sigVersion,
		"server_id": server.ID, "session_id": sessionID, "nonce": nonce,
		"issued_at": envelope.IssuedAt, "expires_at": envelope.ExpiresAt, "kind": "terminal",
		"cols": cols, "rows": rows, "signature": sig,
	}
	if owner == InteractiveOwnerMCP {
		payload["origin"] = model.InteractiveOriginMCP
		payload["mode"] = mode
	} else if loginEnv {
		payload["mode"] = mode
	}
	if owner == InteractiveOwnerMCP && sigVersion == security.InteractiveSignatureV2 {
		// Explicit origin for V2 already set
	}
	if !s.sendAgentControl(server.ID, payload) {
		s.terminalHub.mu.Lock()
		delete(s.terminalHub.sessions, sessionID)
		s.terminalHub.mu.Unlock()
		return nil, codedError("agent_offline", "agent control channel is unavailable")
	}
	session.mu.Lock()
	session.prepareTimer = time.AfterFunc(terminalPrepareTimeout, func() {
		s.failTerminalSession(sessionID, server.ID, "prepare_timeout", "")
	})
	// idle timeout for MCP sessions: will be reset on activity
	if owner == InteractiveOwnerMCP {
		session.idleTimer = time.AfterFunc(terminalIdleTimeout, func() {
			s.closeMCPTerminalSession(sessionID, "idle_timeout")
		})
		session.absTimer = time.AfterFunc(terminalAbsTimeout, func() {
			s.closeMCPTerminalSession(sessionID, "terminal_expired")
		})
	}
	session.mu.Unlock()
	return session, nil
}

func (s *Server) waitForTerminalReady(session *terminalSession) error {
	select {
	case <-session.readyCh:
		session.mu.Lock()
		errMsg := session.readyErr
		ready := session.Ready || session.agentReady
		session.mu.Unlock()
		if errMsg != "" {
			return codedError(errMsg, errMsg)
		}
		if !ready {
			return codedError("terminal_prepare_timeout", "terminal prepare timeout")
		}
		return nil
	case <-time.After(terminalPrepareTimeout):
		return codedError("terminal_prepare_timeout", "terminal prepare timeout")
	}
}

func (s *Server) createTerminalSession(w http.ResponseWriter, r *http.Request, serverID int64) {
	user := currentUser(r)
	if user == nil {
		failCode(w, "terminal_auth_expired", "invalid session", http.StatusUnauthorized)
		return
	}
	var req struct {
		StepUpToken string          `json:"step_up_token"`
		Cols        int             `json:"cols"`
		Rows        int             `json:"rows"`
		Mode        string          `json:"mode"`
		Shell       string          `json:"shell"`
		Command     string          `json:"command"`
		Env         json.RawMessage `json:"env"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Shell) != "" || strings.TrimSpace(req.Command) != "" || (len(req.Env) > 0 && string(req.Env) != "null") {
		failCode(w, "terminal_request_invalid", "terminal request cannot select shell, command, or environment", http.StatusBadRequest)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "login"
	}
	if mode != "login" && mode != "minimal" {
		failCode(w, "terminal_mode_invalid", "terminal mode must be login or minimal", http.StatusBadRequest)
		return
	}
	if settingBool(s.runtimeSettings(r.Context()), settingRemoteTerminalPasswordConfirmationEnabled, true) {
		if err := s.consumeStepUpForServer(r, req.StepUpToken, model.StepUpPurposeRemoteTerminal, serverID); err != nil {
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
	session, err := s.prepareInteractiveSession(r.Context(), InteractiveOwnerHuman, server, user.ID, "", "", 0, req.Cols, req.Rows, mode)
	if err != nil {
		if coded, ok := err.(interface{ Code() string }); ok {
			switch coded.Code() {
			case "terminal_limit_exceeded":
				failCode(w, coded.Code(), err.Error(), http.StatusConflict)
			case "agent_offline", "agent_upgrade_required", "remote_access_global_disabled", "remote_access_server_disabled", "agent_local_gate_denied":
				failCode(w, coded.Code(), err.Error(), http.StatusConflict)
			default:
				fail(w, err, http.StatusInternalServerError)
			}
		} else {
			fail(w, err, http.StatusInternalServerError)
		}
		return
	}
	http.SetCookie(w, s.terminalTicketCookie(r, serverID, session.Ticket, 120))
	s.recordRemoteAccessAudit(r, model.RemoteAccessAuditEvent{
		EventType: model.RemoteAccessAuditTerminalOpen, ActorType: "user", ActorUserID: &user.ID,
		ServerID: &serverID, SessionID: session.ID, Result: "opened", Capability: "remote_terminal",
	})
	write(w, http.StatusCreated, map[string]any{
		"session_id": session.ID, "expires_at": session.ExpiresAt, "mode": mode, "login_env": session.LoginEnv,
	})
}

func (s *Server) terminalCookiePath(r *http.Request, serverID int64) string {
	prefix := strings.TrimRight(s.currentBasePath(), "/")
	return prefix + "/api/v1/ui/servers/" + strconv.FormatInt(serverID, 10) + "/terminal/"
}

func (s *Server) terminalTicketCookie(r *http.Request, serverID int64, value string, maxAge int) *http.Cookie {
	cookie := &http.Cookie{
		Name: terminalCookieName, Value: value, Path: s.terminalCookiePath(r, serverID),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: requestUsesHTTPS(r), MaxAge: maxAge,
	}
	if maxAge < 0 {
		cookie.Expires = time.Unix(1, 0).UTC()
	}
	return cookie
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
		if session.OwnerType == InteractiveOwnerMCP {
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
	_ = s.sendAgentControl(serverID, map[string]any{"type": "interactive_close", "session_id": sessionID})
	write(w, http.StatusOK, map[string]any{"closed": true})
}

func (session *terminalSession) close(reason string) {
	session.notifyAndClose("closed", reason, "")
}

func (session *terminalSession) fail(reason, detail string) {
	session.notifyAndClose("error", reason, detail)
}

func (session *terminalSession) notifyAndClose(msgType, reason, detail string) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed && session.Closed {
		return
	}
	session.closed = true
	session.Closed = true
	session.CloseReason = reason
	if session.prepareTimer != nil {
		session.prepareTimer.Stop()
		session.prepareTimer = nil
	}
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if session.absTimer != nil {
		session.absTimer.Stop()
		session.absTimer = nil
	}
	if session.readyCh != nil {
		select {
		case <-session.readyCh:
		default:
			session.readyErr = reason
			close(session.readyCh)
		}
	}
	payload := map[string]any{"type": msgType, "reason": reason}
	if detail != "" {
		payload["detail"] = detail
	}
	if session.browser != nil {
		_ = session.browser.WriteJSON(payload)
		_ = session.browser.Close()
	}
	if session.agent != nil {
		_ = session.agent.WriteJSON(map[string]any{"type": "close"})
		_ = session.agent.Close()
	}
}

func (session *terminalSession) markAgentConnected() {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.prepareTimer != nil {
		session.prepareTimer.Stop()
		session.prepareTimer = nil
	}
	session.Ready = true
	session.agentReady = true
	if session.readyCh != nil {
		select {
		case <-session.readyCh:
		default:
			close(session.readyCh)
		}
	}
	session.LastActivityAt = time.Now().UTC()
	if session.OwnerType == InteractiveOwnerMCP && session.idleTimer != nil {
		session.idleTimer.Reset(terminalIdleTimeout)
	}
}

func (s *Server) failTerminalSession(sessionID string, serverID int64, reason, detail string) {
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	if session == nil || session.ServerID != serverID {
		s.terminalHub.mu.Unlock()
		return
	}
	delete(s.terminalHub.sessions, sessionID)
	s.terminalHub.mu.Unlock()
	// ensure readyCh is notified with error before fail
	session.mu.Lock()
	if session.readyCh != nil {
		select {
		case <-session.readyCh:
		default:
			session.readyErr = reason
			if detail != "" {
				session.readyErr = reason + ": " + detail
			}
			close(session.readyCh)
		}
	}
	session.mu.Unlock()
	session.fail(reason, detail)
	_ = s.sendAgentControl(serverID, map[string]any{"type": "interactive_close", "session_id": sessionID})
}

func (s *Server) markTerminalReady(sessionID string, serverID int64) {
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	s.terminalHub.mu.Unlock()
	if session == nil || session.ServerID != serverID {
		return
	}
	session.mu.Lock()
	session.agentReady = true
	session.Ready = true
	if session.prepareTimer != nil {
		session.prepareTimer.Stop()
		session.prepareTimer = nil
	}
	if session.readyCh != nil {
		select {
		case <-session.readyCh:
		default:
			close(session.readyCh)
		}
	}
	session.LastActivityAt = time.Now().UTC()
	if session.OwnerType == InteractiveOwnerMCP && session.idleTimer != nil {
		session.idleTimer.Reset(terminalIdleTimeout)
	}
	session.mu.Unlock()
}

func (s *Server) closeMCPTerminalSession(sessionID string, reason string) {
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	if session == nil || session.OwnerType != InteractiveOwnerMCP {
		s.terminalHub.mu.Unlock()
		return
	}
	delete(s.terminalHub.sessions, sessionID)
	s.terminalHub.mu.Unlock()
	session.mu.Lock()
	if session.readyCh != nil {
		select {
		case <-session.readyCh:
		default:
			session.readyErr = reason
			close(session.readyCh)
		}
	}
	session.mu.Unlock()
	session.close(reason)
	_ = s.sendAgentControl(session.ServerID, map[string]any{"type": "interactive_close", "session_id": sessionID})
}

func (s *Server) handleInteractiveAgentStatus(serverID int64, msg map[string]json.RawMessage) {
	var msgType, sessionID, reason, detail string
	if raw, ok := msg["type"]; ok {
		_ = json.Unmarshal(raw, &msgType)
	}
	if raw, ok := msg["session_id"]; ok {
		_ = json.Unmarshal(raw, &sessionID)
	}
	if raw, ok := msg["reason"]; ok {
		_ = json.Unmarshal(raw, &reason)
	}
	if raw, ok := msg["detail"]; ok {
		_ = json.Unmarshal(raw, &detail)
	}
	if sessionID == "" {
		return
	}
	switch msgType {
	case "interactive_ready":
		s.markTerminalReady(sessionID, serverID)
	case "interactive_failed":
		if reason == "" {
			reason = "interactive_failed"
		}
		s.failTerminalSession(sessionID, serverID, reason, detail)
	}
}

func (s *Server) browserTerminalWS(w http.ResponseWriter, r *http.Request, serverID int64, sessionID string) {
	if r.Header.Get("Origin") == "" || !s.originAllowed(r, r.Header.Get("Origin")) {
		log.Printf("terminal websocket denied server=%d session=%s reason=origin_denied origin=%s host=%s", serverID, sessionID, r.Header.Get("Origin"), r.Host)
		failCode(w, "origin_denied", "origin is not allowed", http.StatusForbidden)
		return
	}
	cookie, err := r.Cookie(terminalCookieName)
	if err != nil || cookie.Value == "" {
		log.Printf("terminal websocket denied server=%d session=%s reason=ticket_missing", serverID, sessionID)
		failCode(w, "terminal_auth_expired", "terminal ticket missing", http.StatusUnauthorized)
		return
	}
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	if session == nil || session.ServerID != serverID || session.TicketUsed || session.Ticket != cookie.Value {
		s.terminalHub.mu.Unlock()
		log.Printf("terminal websocket denied server=%d session=%s reason=ticket_invalid", serverID, sessionID)
		failCode(w, "terminal_auth_expired", "terminal ticket is invalid", http.StatusUnauthorized)
		return
	}
	if session.OwnerType == InteractiveOwnerMCP {
		s.terminalHub.mu.Unlock()
		log.Printf("terminal websocket denied server=%d session=%s reason=mcp_isolation", serverID, sessionID)
		failCode(w, "terminal_not_owner", "MCP terminal cannot be attached via browser", http.StatusForbidden)
		return
	}
	session.TicketUsed = true
	s.terminalHub.mu.Unlock()
	http.SetCookie(w, s.terminalTicketCookie(r, serverID, "", -1))
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
	session.markAgentConnected()
	s.relayTerminal(session)
}

func (s *Server) relayTerminal(session *terminalSession) {
	session.mu.Lock()
	if session.OwnerType == InteractiveOwnerMCP {
		agent := session.agent
		if agent == nil || session.relaying {
			session.mu.Unlock()
			return
		}
		session.relaying = true
		session.mu.Unlock()
		// MCP relay: agent binary -> outputBuffer, with activity tracking
		go func() {
			for {
				mt, data, err := agent.ReadMessage()
				if err != nil {
					// agent disconnected: mark session closed but keep entry for final io read
					session.mu.Lock()
					if !session.Closed && !session.closed {
						session.CloseReason = "agent_disconnected"
						session.Closed = true
						session.closed = true
						if session.readyCh != nil {
							select {
							case <-session.readyCh:
							default:
								session.readyErr = "agent_disconnected"
								close(session.readyCh)
							}
						}
					}
					session.mu.Unlock()
					// stop timers
					session.mu.Lock()
					if session.idleTimer != nil {
						session.idleTimer.Stop()
						session.idleTimer = nil
					}
					if session.absTimer != nil {
						session.absTimer.Stop()
						session.absTimer = nil
					}
					session.mu.Unlock()
					// close agent conn
					_ = agent.Close()
					return
				}
				if mt == websocket.BinaryMessage {
					if session.outputBuffer != nil && len(data) > 0 {
						session.outputBuffer.Write(data)
						session.mu.Lock()
						session.LastActivityAt = time.Now().UTC()
						if session.idleTimer != nil {
							session.idleTimer.Reset(terminalIdleTimeout)
						}
						session.mu.Unlock()
					}
				} else if mt == websocket.TextMessage {
					// control messages from agent (e.g., ready) already handled via control channel; ignore
					continue
				}
			}
		}()
		return
	}
	// Human relay
	browser := session.browser
	agent := session.agent
	if browser == nil || agent == nil || session.relaying {
		session.mu.Unlock()
		return
	}
	session.relaying = true
	session.mu.Unlock()
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

// MCP helpers

var ansiRegexp = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]|\x1b\\].*?(\x07|\x1b\\\\)|\x1b\\[[?0-9;]*[a-zA-Z]")

func stripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

func toValidUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

func (s *Server) mcpTerminalWrite(session *terminalSession, input string) error {
	session.mu.Lock()
	agent := session.agent
	session.mu.Unlock()
	if agent == nil {
		return codedError("terminal_closed", "terminal is closed")
	}
	session.mu.Lock()
	if session.Closed || session.closed {
		session.mu.Unlock()
		return codedError("terminal_closed", "terminal is closed")
	}
	session.LastActivityAt = time.Now().UTC()
	if session.idleTimer != nil {
		session.idleTimer.Reset(terminalIdleTimeout)
	}
	if time.Now().After(session.ExpiresAt) {
		session.mu.Unlock()
		session.close("terminal_expired")
		return codedError("terminal_expired", "terminal has expired")
	}
	session.mu.Unlock()
	if input == "" {
		return nil
	}
	data := []byte(input)
	if len(data) > terminalMCPMaxInput {
		return codedError("invalid_input", "input exceeds 64KiB limit")
	}
	session.writeMu.Lock()
	err := agent.WriteMessage(websocket.BinaryMessage, data)
	session.writeMu.Unlock()
	if err != nil {
		session.close("agent_disconnected")
		return codedError("agent_disconnected", "agent disconnected")
	}
	return nil
}

func (s *Server) mcpTerminalResize(session *terminalSession, cols, rows int) error {
	session.mu.Lock()
	agent := session.agent
	session.mu.Unlock()
	if agent == nil {
		return codedError("terminal_closed", "terminal is closed")
	}
	if cols <= 0 || cols > terminalMaxCols || rows <= 0 || rows > terminalMaxRows {
		return codedError("invalid_input", "invalid cols/rows")
	}
	session.mu.Lock()
	session.Cols = cols
	session.Rows = rows
	session.LastActivityAt = time.Now().UTC()
	if session.idleTimer != nil {
		session.idleTimer.Reset(terminalIdleTimeout)
	}
	session.mu.Unlock()
	payload, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	session.writeMu.Lock()
	err := agent.WriteMessage(websocket.TextMessage, payload)
	session.writeMu.Unlock()
	if err != nil {
		return codedError("agent_disconnected", "agent disconnected")
	}
	return nil
}
