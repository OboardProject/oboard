package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

// mcpTerminalStream is GET /api/v1/mcp/terminal/:id/stream — authenticated via mcpAuth
// (OAuth Bearer), reusing the same privilege checks as the polling tools: double
// resource boundary, PrivilegedGrant capability, global/server mcp_enabled, agent
// online/capability and hardened local gate. After upgrade, Binary frames are
// forwarded to the agent via the existing session.outputBuffer path, Text frames
// are limited to {"type":"resize"|"close"|"ping"}, and agent output is streamed
// as Binary from TerminalOutputBuffer with 25ms polling, mirroring relayTerminal.
func (s *Server) mcpTerminalStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	sessionID := mcpTerminalStreamID(r.URL.Path)
	if sessionID == "" {
		failCode(w, "invalid_input", "terminal session id is required", http.StatusBadRequest)
		return
	}
	grant, err := mcpGrantPrincipal(r.Context())
	if err != nil {
		failCode(w, mcpauth.CodePrivilegedGrantRequired, "Interactive terminal requires an active Privileged MCP Grant.", http.StatusForbidden)
		return
	}
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	s.terminalHub.mu.Unlock()
	if session == nil || session.OwnerType != InteractiveOwnerMCP {
		failCode(w, "terminal_not_found", "terminal session not found", http.StatusNotFound)
		return
	}
	if session.OAuthGrantID != grant.Grant.GrantID || session.OAuthClientID != grant.ClientID {
		failCode(w, "terminal_not_owner", "session does not belong to this grant", http.StatusForbidden)
		return
	}
	if grant.UserID != 0 && session.UserID != 0 && session.UserID != grant.UserID {
		failCode(w, "terminal_not_owner", "session does not belong to this grant", http.StatusForbidden)
		return
	}
	if grant.PrivilegedGrant == nil || session.PrivilegedGrantID == 0 || session.PrivilegedGrantID != grant.PrivilegedGrant.ID {
		s.closeMCPTerminalSession(sessionID, "privileged_grant_revoked")
		failCode(w, "privileged_grant_revoked", "The Privileged MCP Grant for this terminal is no longer active.", http.StatusForbidden)
		return
	}
	if !grant.PrivilegedGrant.HasCapability(model.PrivilegeRemoteInteractive) {
		s.closeMCPTerminalSession(sessionID, mcpauth.CodePrivilegedGrantRequired)
		failCode(w, mcpauth.CodePrivilegedGrantRequired, "Interactive terminal requires remote_interactive capability.", http.StatusForbidden)
		return
	}
	server, err := s.store.GetServer(r.Context(), session.ServerID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	decision := s.authorizeCapability(r.Context(), mcpTerminalCapabilityDescriptor(), map[string]any{"server_id": server.ID})
	if !decision.Allowed {
		code := decision.Code
		if code == mcpauth.CodeResourceDenied {
			code = "privileged_resource_denied"
		}
		s.closeMCPTerminalSession(sessionID, code)
		failCode(w, code, decision.Reason, http.StatusForbidden)
		return
	}
	if err := s.assertRemotePrivilegeAllowed(r.Context(), server, model.PrivilegeRemoteInteractive); err != nil {
		code := "remote_access_denied"
		if coded, ok := err.(interface{ Code() string }); ok {
			code = coded.Code()
		}
		s.closeMCPTerminalSession(sessionID, code)
		failCode(w, code, err.Error(), http.StatusConflict)
		return
	}
	session.mu.Lock()
	closed := session.Closed || session.closed
	expired := time.Now().After(session.ExpiresAt)
	session.mu.Unlock()
	if expired {
		s.closeMCPTerminalSession(sessionID, "terminal_expired")
		failCode(w, "terminal_expired", "terminal has expired", http.StatusGone)
		return
	}
	if closed {
		failCode(w, "terminal_closed", "terminal is closed: "+session.CloseReason, http.StatusGone)
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin:       s.checkOrigin,
		ReadBufferSize:    4096,
		WriteBufferSize:   4096,
		EnableCompression: false,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(terminalMaxMessage)
	_ = capability.DataSensitive

	session.mu.Lock()
	session.LastActivityAt = time.Now().UTC()
	if session.idleTimer != nil {
		session.idleTimer.Reset(terminalIdleTimeout)
	}
	session.mu.Unlock()

	// Stream agent output (Binary) to the MCP client.
	// Start from current tip to avoid replaying the entire backlog on reconnect;
	// the initial prompt is already delivered via server_terminal_open.
	var cursor int64
	if session.outputBuffer != nil {
		cursor = session.outputBuffer.NextSeq() - 1
		if cursor < 0 {
			cursor = 0
		}
	}
	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if session.outputBuffer == nil {
					continue
				}
				out, next, _, _, _ := session.outputBuffer.ReadAfter(cursor, 32768)
				if len(out) > 0 {
					session.mu.Lock()
					session.LastActivityAt = time.Now().UTC()
					if session.idleTimer != nil {
						session.idleTimer.Reset(terminalIdleTimeout)
					}
					session.mu.Unlock()
					_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
					if err := conn.WriteMessage(websocket.BinaryMessage, out); err != nil {
						return
					}
					cursor = next
				}
				session.mu.Lock()
				closed := session.Closed || session.closed
				reason := session.CloseReason
				session.mu.Unlock()
				if closed {
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"closed","reason":`+jsonString(reason)+`}`))
					return
				}
			}
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		// Re-check liveness on every client frame (hot auth).
		if grantNow, err := mcpGrantPrincipal(r.Context()); err != nil || grantNow.PrivilegedGrant == nil || grantNow.PrivilegedGrant.ID != session.PrivilegedGrantID {
			s.closeMCPTerminalSession(sessionID, "privileged_grant_revoked")
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"closed","reason":"privileged_grant_revoked"}`))
			return
		}
		if err := s.assertRemotePrivilegeAllowed(r.Context(), server, model.PrivilegeRemoteInteractive); err != nil {
			code := "remote_access_denied"
			if coded, ok := err.(interface{ Code() string }); ok {
				code = coded.Code()
			}
			s.closeMCPTerminalSession(sessionID, code)
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"closed","reason":`+jsonString(code)+`}`))
			return
		}
		if mt == websocket.BinaryMessage {
			if len(data) == 0 {
				continue
			}
			if len(data) > terminalMCPMaxInput {
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","code":"invalid_input","message":"input exceeds 64KiB"}`))
				continue
			}
			if err := s.mcpTerminalWrite(session, string(data)); err != nil {
				code := "terminal_closed"
				if coded, ok := err.(interface{ Code() string }); ok {
					code = coded.Code()
				}
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","code":`+jsonString(code)+`}`))
				if code == "terminal_closed" || code == "terminal_expired" {
					return
				}
			}
		} else if mt == websocket.TextMessage {
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "resize":
				if err := s.mcpTerminalResize(session, msg.Cols, msg.Rows); err != nil {
					code := "terminal_resize_failed"
					if coded, ok := err.(interface{ Code() string }); ok {
						code = coded.Code()
					}
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","code":`+jsonString(code)+`}`))
				}
			case "close":
				s.closeMCPTerminalSession(sessionID, "user_close")
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"closed","reason":"user_close"}`))
				return
			case "ping":
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`))
			default:
				continue
			}
		}
		session.mu.Lock()
		session.LastActivityAt = time.Now().UTC()
		if session.idleTimer != nil {
			session.idleTimer.Reset(terminalIdleTimeout)
		}
		session.mu.Unlock()
	}
}

func mcpTerminalStreamID(path string) string {
	const prefix = "/api/v1/mcp/terminal/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.Trim(rest, "/")
	if strings.HasSuffix(rest, "/stream") {
		rest = strings.TrimSuffix(rest, "/stream")
		rest = strings.TrimSuffix(rest, "/")
	} else {
		// allow /api/v1/mcp/terminal/{id} without /stream for future compat? require /stream
		return ""
	}
	if rest == "" || strings.Contains(rest, "/") {
		return ""
	}
	return rest
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
