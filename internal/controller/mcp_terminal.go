package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) registerMCPInteractiveTerminalTools(server *mcp.Server) {
	// server_terminal_open
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_open",
		Title:       "Open Interactive Terminal",
		Description: "Open a persistent login PTY via OBoard Agent. Requires remote_interactive privileged grant and server boundary. Single terminal per grant per server, max 4 per grant.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"server_id": map[string]any{"type": "integer", "minimum": 1},
			"mode":      map[string]any{"type": "string", "enum": []string{"login", "minimal"}},
			"cols":      map[string]any{"type": "integer", "minimum": 1, "maximum": 400},
			"rows":      map[string]any{"type": "integer", "minimum": 1, "maximum": 150},
		}, "server_id")),
		Annotations: mcpAnnotations(false, false),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerTerminalOpen(ctx, req)
	})
	// server_terminal_io
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_io",
		Title:       "Terminal IO",
		Description: "Write stdin to an MCP PTY and read output via cursor. Input is optional (read-only). Wait up to 3000ms and max 64KiB per read.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"session_id":   map[string]any{"type": "string", "minLength": 1},
			"input":        map[string]any{"type": "string", "maxLength": 65536},
			"after_cursor": map[string]any{"type": "integer", "minimum": 0},
			"wait_ms":      map[string]any{"type": "integer", "minimum": 0, "maximum": 3000},
			"max_bytes":    map[string]any{"type": "integer", "minimum": 1, "maximum": 65536},
			"strip_ansi":   map[string]any{"type": "boolean"},
		}, "session_id")),
		Annotations: mcpAnnotations(false, false),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerTerminalIO(ctx, req)
	})
	// server_terminal_close
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_close",
		Title:       "Close Terminal",
		Description: "Close an MCP PTY session. Idempotent.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"session_id": map[string]any{"type": "string", "minLength": 1},
		}, "session_id")),
		Annotations: mcpAnnotations(false, true),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerTerminalClose(ctx, req)
	})
	// server_terminal_resize
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_resize",
		Title:       "Resize Terminal",
		Description: "Resize an MCP PTY. Validates cols/rows and forwards resize to Agent PTY.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"session_id": map[string]any{"type": "string", "minLength": 1},
			"cols":       map[string]any{"type": "integer", "minimum": 1, "maximum": 400},
			"rows":       map[string]any{"type": "integer", "minimum": 1, "maximum": 150},
		}, "session_id", "cols", "rows")),
		Annotations: mcpAnnotations(false, true),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerTerminalResize(ctx, req)
	})
}

func (s *Server) callMCPServerTerminalOpen(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, err := s.mcpPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPlainFailureResult("", err.Error()), nil
	}
	grant, err := s.mcpGrantPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPlainFailureResult("privileged_grant_required", err.Error()), nil
	}
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	serverID := int64FromAny(args["server_id"])
	if serverID <= 0 {
		return mcpPlainFailureResult("invalid_input", "server_id is required"), nil
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "login"
	}
	cols := int(int64FromAny(args["cols"]))
	rows := int(int64FromAny(args["rows"]))
	// Authorize via evaluator (RBAC + OAuth boundary + privileged)
	descriptor := capability.Descriptor{
		Name: "node.terminal", Description: "MCP interactive terminal", InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`),
		MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", MCPEnabled: true, PrivilegeClass: model.PrivilegeRemoteInteractive,
		ResourceTypes: []string{"server"}, ResolveResourceRefs: serverRefFromServerID,
	}
	decision := s.authorizeCapability(ctx, descriptor, args)
	if !decision.Allowed {
		return mcpFailureResult(decision, ""), nil
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return mcpPlainFailureResult("not_found", err.Error()), nil
	}
	// Additional global/server/agent checks for interactive
	if err := s.assertRemotePrivilegeAllowed(ctx, server, model.PrivilegeRemoteInteractive); err != nil {
		code := ""
		if c, ok := err.(interface{ Code() string }); ok {
			code = c.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	// Enforce limits via prepareInteractiveSession helper will also check, but pre-check for clearer error
	session, err := s.prepareInteractiveSession(ctx, InteractiveOwnerMCP, server, grant.UserID, grant.Grant.GrantID, grant.ClientID, 0, cols, rows, mode)
	if err != nil {
		code := ""
		if c, ok := err.(interface{ Code() string }); ok {
			code = c.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	// Bind ownership already set in prepareInteractiveSession
	// Wait for agent ready (with timeout)
	if err := s.waitForTerminalReady(session); err != nil {
		// clean up failed session
		s.terminalHub.mu.Lock()
		delete(s.terminalHub.sessions, session.ID)
		s.terminalHub.mu.Unlock()
		session.close("prepare_failed")
		_ = s.sendAgentControl(serverID, map[string]any{"type": "interactive_close", "session_id": session.ID})
		code := "terminal_prepare_timeout"
		if c, ok := err.(interface{ Code() string }); ok {
			code = c.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	// Read initial output (prompt) after ready
	var initial []byte
	var nextCursor int64
	var lost bool
	var oldest int64
	if session.outputBuffer != nil {
		// give a short grace for prompt to arrive
		time.Sleep(150 * time.Millisecond)
		initial, nextCursor, lost, oldest, _ = session.outputBuffer.ReadAfter(0, 32768)
	}
	// Audit: open (redacted)
	inputHash := ""
	if session.outputBuffer != nil {
		sum := sha256.Sum256(initial)
		_ = hex.EncodeToString(sum[:])
	}
	_ = inputHash
	s.recordToolCall(ctx, principal, "server_terminal_open", map[string]any{
		"server_id": serverID, "session_id": session.ID, "mode": mode, "cols": cols, "rows": rows,
	}, "succeeded", capability.DataSensitive)
	// also audit event
	serverIDCopy := serverID
	s.recordRemoteAccessAuditWithContext(ctx, "", model.RemoteAccessAuditEvent{
		EventType: model.RemoteAccessAuditMCPInteractiveOpen, ActorType: "mcp", OAuthGrantID: grant.Grant.GrantID, OAuthClientID: grant.ClientID,
		ServerID: &serverIDCopy, SessionID: session.ID, Capability: model.PrivilegeRemoteInteractive, Result: "opened",
		MetadataJSON: json.RawMessage(`{}`),
	})
	outputStr := toValidUTF8(initial)
	// strip ansi default true for LLM readability? spec says strip_ansi default true for io, but open also should maybe strip? We'll keep raw but strip for open as well if requested default true.
	// For open, we always strip for initial prompt to be readable.
	outputStr = stripANSI(outputStr)
	result := map[string]any{
		"status": "ready", "session_id": session.ID, "server_id": serverID, "cursor": nextCursor,
		"output": outputStr, "output_bytes": len(initial), "output_lost": lost, "oldest_cursor": oldest,
		"created_at": session.CreatedAt, "expires_at": session.ExpiresAt,
	}
	return mcpEnvelopeResult(newToolEnvelope("succeeded", "", result))
}

func (s *Server) callMCPServerTerminalIO(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, err := s.mcpPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPlainFailureResult("", err.Error()), nil
	}
	grant, err := s.mcpGrantPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPlainFailureResult("privileged_grant_required", err.Error()), nil
	}
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	sessionID, _ := args["session_id"].(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return mcpPlainFailureResult("invalid_input", "session_id is required"), nil
	}
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	s.terminalHub.mu.Unlock()
	if session == nil {
		return mcpPlainFailureResult("terminal_not_found", "terminal session not found"), nil
	}
	// ownership check
	if session.OwnerType != InteractiveOwnerMCP || session.OAuthGrantID != grant.Grant.GrantID || session.OAuthClientID != grant.ClientID {
		return mcpPlainFailureResult("terminal_not_owner", "session does not belong to this grant"), nil
	}
	if session.UserID != 0 && grant.UserID != 0 && session.UserID != grant.UserID {
		return mcpPlainFailureResult("terminal_not_owner", "session user mismatch"), nil
	}
	// re-authorize every call (global/server/agent/local gate + RBAC + boundaries)
	server, err := s.store.GetServer(ctx, session.ServerID)
	if err != nil {
		return mcpPlainFailureResult("not_found", err.Error()), nil
	}
	// re-check privilege boundaries via evaluator (use original args with server_id)
	authArgs := map[string]any{"server_id": session.ServerID}
	descriptor := capability.Descriptor{
		Name: "node.terminal", MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", MCPEnabled: true, PrivilegeClass: model.PrivilegeRemoteInteractive,
		ResourceTypes: []string{"server"}, ResolveResourceRefs: serverRefFromServerID,
	}
	decision := s.authorizeCapability(ctx, descriptor, authArgs)
	if !decision.Allowed {
		// close session on revocation
		if decision.Code == mcpauth.CodePrivilegedGrantRequired || decision.Code == mcpauth.CodeResourceDenied || decision.Code == mcpauth.CodeRoleDenied {
			s.closeMCPTerminalSession(sessionID, "privileged_grant_revoked")
		}
		return mcpFailureResult(decision, ""), nil
	}
	if err := s.assertRemotePrivilegeAllowed(ctx, server, model.PrivilegeRemoteInteractive); err != nil {
		code := ""
		if c, ok := err.(interface{ Code() string }); ok {
			code = c.Code()
		}
		// close on global/server disable
		if code == "remote_access_global_disabled" || code == "remote_access_server_disabled" || code == "agent_local_gate_denied" {
			s.closeMCPTerminalSession(sessionID, code)
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	// check session closed/expired
	session.mu.Lock()
	closed := session.Closed || session.closed
	closeReason := session.CloseReason
	expired := time.Now().After(session.ExpiresAt)
	session.mu.Unlock()
	if expired {
		s.closeMCPTerminalSession(sessionID, "terminal_expired")
		return mcpPlainFailureResult("terminal_expired", "terminal has expired"), nil
	}
	if closed {
		// return closed status instead of error? spec says io should return closed true
		return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{
			"status": "closed", "session_id": sessionID, "closed": true, "reason": closeReason,
		}))
	}
	input, _ := args["input"].(string)
	afterCursor := int64(int64FromAny(args["after_cursor"]))
	waitMS := int(int64FromAny(args["wait_ms"]))
	if waitMS < 0 {
		waitMS = 0
	}
	if waitMS > terminalMCPMaxWaitMS {
		waitMS = terminalMCPMaxWaitMS
	}
	maxBytes := int(int64FromAny(args["max_bytes"]))
	if maxBytes <= 0 {
		maxBytes = 32768
	}
	if maxBytes > 65536 {
		maxBytes = 65536
	}
	stripAnsi := true
	if v, ok := args["strip_ansi"]; ok {
		if b, ok2 := v.(bool); ok2 {
			stripAnsi = b
		}
	}
	if len(input) > terminalMCPMaxInput {
		return mcpPlainFailureResult("invalid_input", "input exceeds 64KiB"), nil
	}
	// write input if any
	if input != "" {
		if err := s.mcpTerminalWrite(session, input); err != nil {
			code := ""
			if c, ok := err.(interface{ Code() string }); ok {
				code = c.Code()
			}
			return mcpPlainFailureResult(code, err.Error()), nil
		}
	}
	// wait for output
	if waitMS > 0 {
		time.Sleep(time.Duration(waitMS) * time.Millisecond)
	} else {
		// small wait for echo
		time.Sleep(50 * time.Millisecond)
	}
	// read buffer
	var out []byte
	var nextCursor int64
	var lost bool
	var oldest int64
	var truncated bool
	if session.outputBuffer != nil {
		out, nextCursor, lost, oldest, truncated = session.outputBuffer.ReadAfter(afterCursor, maxBytes)
	}
	// activity touch
	session.mu.Lock()
	session.LastActivityAt = time.Now().UTC()
	if session.idleTimer != nil {
		session.idleTimer.Reset(terminalIdleTimeout)
	}
	session.mu.Unlock()
	outputStr := toValidUTF8(out)
	if stripAnsi {
		outputStr = stripANSI(outputStr)
	}
	// audit redacted (no input/output content, just metrics)
	inputHash := ""
	if input != "" {
		sum := sha256.Sum256([]byte(input))
		inputHash = hex.EncodeToString(sum[:])
	}
	_ = inputHash
	// avoid full content in generic audit; use specialized metadata
	s.recordToolCall(ctx, principal, "server_terminal_io", map[string]any{
		"session_id": sessionID, "server_id": session.ServerID,
		"input_bytes": len(input), "input_sha256": inputHash, "output_bytes": len(out), "after_cursor": afterCursor, "next_cursor": nextCursor,
	}, "succeeded", capability.DataSensitive)
	if !utf8.ValidString(outputStr) {
		outputStr = strings.ToValidUTF8(outputStr, "\uFFFD")
	}
	// detect if session closed during wait
	session.mu.Lock()
	closed = session.Closed || session.closed
	closeReason = session.CloseReason
	session.mu.Unlock()
	if closed {
		return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{
			"status": "closed", "session_id": sessionID, "closed": true, "reason": closeReason,
			"output": outputStr, "next_cursor": nextCursor, "output_bytes": len(out), "output_lost": lost, "oldest_cursor": oldest, "output_truncated": truncated,
		}))
	}
	closedFlag := false
	// also check if PTY exited: we could know via CloseReason shell_exited but still report closed
	if closeReason == "shell_exited" || closeReason == "agent_disconnected" {
		closedFlag = true
	}
	result := map[string]any{
		"status": "running", "session_id": sessionID, "output": outputStr, "next_cursor": nextCursor,
		"output_bytes": len(out), "output_truncated": truncated, "output_lost": lost, "oldest_cursor": oldest,
		"closed": closedFlag, "exit_code": nil,
	}
	if closedFlag {
		result["closed"] = true
		result["reason"] = closeReason
	}
	return mcpEnvelopeResult(newToolEnvelope("succeeded", "", result))
}

func (s *Server) callMCPServerTerminalClose(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	grant, err := s.mcpGrantPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPlainFailureResult("privileged_grant_required", err.Error()), nil
	}
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	sessionID, _ := args["session_id"].(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return mcpPlainFailureResult("invalid_input", "session_id is required"), nil
	}
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	if session != nil && session.OwnerType == InteractiveOwnerMCP {
		// ownership check before delete
		if session.OAuthGrantID != grant.Grant.GrantID || session.OAuthClientID != grant.ClientID {
			s.terminalHub.mu.Unlock()
			return mcpPlainFailureResult("terminal_not_owner", "session does not belong to this grant"), nil
		}
		delete(s.terminalHub.sessions, sessionID)
		s.terminalHub.mu.Unlock()
		session.close("user_close")
		_ = s.sendAgentControl(session.ServerID, map[string]any{"type": "interactive_close", "session_id": sessionID})
		serverID := session.ServerID
		s.recordRemoteAccessAuditWithContext(ctx, "", model.RemoteAccessAuditEvent{
			EventType: model.RemoteAccessAuditMCPInteractiveClose, ServerID: &serverID, SessionID: sessionID, OAuthGrantID: grant.Grant.GrantID, OAuthClientID: grant.ClientID, Capability: model.PrivilegeRemoteInteractive, Result: "closed",
		})
		principal, _ := s.mcpPrincipalFromRequest(ctx, req)
		s.recordToolCall(ctx, principal, "server_terminal_close", map[string]any{"session_id": sessionID, "server_id": serverID}, "succeeded", capability.DataSensitive)
		return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{"closed": true, "session_id": sessionID}))
	}
	// idempotent: if not found, treat as closed
	var sessOwner bool
	if session != nil {
		sessOwner = session.OAuthGrantID == grant.Grant.GrantID
	}
	s.terminalHub.mu.Unlock()
	if session != nil && !sessOwner {
		return mcpPlainFailureResult("terminal_not_owner", "session does not belong to this grant"), nil
	}
	return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{"closed": true, "session_id": sessionID}))
}

func (s *Server) callMCPServerTerminalResize(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	grant, err := s.mcpGrantPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPlainFailureResult("privileged_grant_required", err.Error()), nil
	}
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	sessionID, _ := args["session_id"].(string)
	sessionID = strings.TrimSpace(sessionID)
	cols := int(int64FromAny(args["cols"]))
	rows := int(int64FromAny(args["rows"]))
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	s.terminalHub.mu.Unlock()
	if session == nil {
		return mcpPlainFailureResult("terminal_not_found", "terminal session not found"), nil
	}
	if session.OwnerType != InteractiveOwnerMCP || session.OAuthGrantID != grant.Grant.GrantID || session.OAuthClientID != grant.ClientID {
		return mcpPlainFailureResult("terminal_not_owner", "session does not belong to this grant"), nil
	}
	// re-authorize
	server, err := s.store.GetServer(ctx, session.ServerID)
	if err != nil {
		return mcpPlainFailureResult("not_found", err.Error()), nil
	}
	if err := s.assertRemotePrivilegeAllowed(ctx, server, model.PrivilegeRemoteInteractive); err != nil {
		code := ""
		if c, ok := err.(interface{ Code() string }); ok {
			code = c.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	if err := s.mcpTerminalResize(session, cols, rows); err != nil {
		code := ""
		if c, ok := err.(interface{ Code() string }); ok {
			code = c.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	principal, _ := s.mcpPrincipalFromRequest(ctx, req)
	s.recordToolCall(ctx, principal, "server_terminal_resize", map[string]any{"session_id": sessionID, "cols": cols, "rows": rows}, "succeeded", capability.DataSensitive)
	return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{"resized": true, "session_id": sessionID, "cols": cols, "rows": rows}))
}

func serverRefFromServerID(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
	m, ok := input.(map[string]any)
	if !ok {
		return nil, errors.New("invalid input")
	}
	id := int64FromAny(m["server_id"])
	if id <= 0 {
		return nil, errors.New("server_id is required")
	}
	return []mcpauth.ResourceRef{{Type: "server", ID: fmt.Sprint(id)}}, nil
}
