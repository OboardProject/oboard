package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) registerMCPInteractiveTerminalTools(server *mcp.Server) {
	server.AddTool(&mcp.Tool{
		Name:  "server_terminal_command",
		Title: "Run Login PTY Command",
		Description: "Run one command in a real OBoard Agent PTY and return its output and exit code. Use this when a login shell environment or TTY is required. " +
			"This tool is always discoverable to MCP principals with operate access, but execution requires an active remote_interactive Privileged MCP Grant, both resource boundaries, and MCP Interactive Terminal enabled globally and on the target server. Missing authorization returns a structured denial and does not open a PTY.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"server_id":  map[string]any{"type": "integer", "minimum": 1},
			"command":    map[string]any{"type": "string", "minLength": 1, "maxLength": 32768},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 100, "maximum": 300000},
			"mode":       map[string]any{"type": "string", "enum": []string{"login", "minimal"}},
		}, "server_id", "command")),
		Annotations: mcpAnnotations(false, false),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerTerminalCommand(ctx, req)
	})
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_open",
		Title:       "Open Interactive Terminal",
		Description: "Open a persistent login PTY through the OBoard Agent. This tool is always discoverable to MCP principals with operate access, but execution requires MCP operate access, an active remote_interactive Privileged MCP Grant, the target server inside both resource boundaries, and MCP Interactive Terminal enabled globally and on the target server. If authorization is missing, it returns a structured denial and does not open a PTY.",
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
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_io",
		Title:       "Terminal IO",
		Description: "Write stdin to and read output from an owned OBoard MCP PTY. The active remote_interactive grant and all live access gates are rechecked on every call; revoked access closes the PTY.",
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
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_resize",
		Title:       "Resize Terminal",
		Description: "Resize an owned OBoard MCP PTY after rechecking the active remote_interactive grant and all live access gates.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"session_id": map[string]any{"type": "string", "minLength": 1},
			"cols":       map[string]any{"type": "integer", "minimum": 1, "maximum": 400},
			"rows":       map[string]any{"type": "integer", "minimum": 1, "maximum": 150},
		}, "session_id", "cols", "rows")),
		Annotations: mcpAnnotations(false, true),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerTerminalResize(ctx, req)
	})
	server.AddTool(&mcp.Tool{
		Name:        "server_terminal_close",
		Title:       "Close Terminal",
		Description: "Close an owned OBoard MCP PTY. Ownership and live remote_interactive authorization are rechecked; revoked access is closed and reported as denied.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"session_id": map[string]any{"type": "string", "minLength": 1},
		}, "session_id")),
		Annotations: mcpAnnotations(false, true),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerTerminalClose(ctx, req)
	})
}

type mcpTerminalAuthorization struct {
	principal application.Principal
	grant     mcpauth.GrantPrincipal
	server    *model.Server
}

func mcpTerminalCapabilityDescriptor() capability.Descriptor {
	return capability.Descriptor{
		Name: "node.terminal", Description: "MCP interactive terminal", InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`),
		MinimumAccess: mcpauth.AccessOperate, RBACPermission: capability.PermissionServersRemoteAccess, MCPEnabled: true,
		PrivilegeClass: model.PrivilegeRemoteInteractive, ResourceTypes: []string{"server"}, ResolveResourceRefs: serverRefFromServerID,
	}
}

func (s *Server) authorizeMCPTerminal(ctx context.Context, req *mcp.CallToolRequest, serverID int64) (*mcpTerminalAuthorization, *mcp.CallToolResult, string) {
	principal, err := s.mcpPrincipalFromRequest(ctx, req)
	if err != nil {
		return nil, mcpPlainFailureResult("oauth_token_invalid", err.Error()), "oauth_token_invalid"
	}
	grant, err := s.mcpGrantPrincipalFromRequest(ctx, req)
	if err != nil {
		return nil, mcpPrivilegedDeniedResult(mcpauth.CodePrivilegedGrantRequired, "Interactive terminal requires an active Privileged MCP Grant.", model.PrivilegeRemoteInteractive, serverID), mcpauth.CodePrivilegedGrantRequired
	}
	args := map[string]any{"server_id": serverID}
	decision := s.authorizeCapability(ctx, mcpTerminalCapabilityDescriptor(), args)
	if !decision.Allowed {
		code := decision.Code
		if code == mcpauth.CodeResourceDenied {
			code = "privileged_resource_denied"
		}
		return nil, mcpPrivilegedDecisionResult(decision, model.PrivilegeRemoteInteractive, serverID), code
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, mcpPlainFailureResult("not_found", err.Error()), "not_found"
	}
	if err := s.assertRemotePrivilegeAllowed(ctx, server, model.PrivilegeRemoteInteractive); err != nil {
		code := "remote_access_denied"
		if coded, ok := err.(interface{ Code() string }); ok {
			code = coded.Code()
		}
		return nil, mcpPrivilegedDeniedResult(code, err.Error(), model.PrivilegeRemoteInteractive, serverID), code
	}
	return &mcpTerminalAuthorization{principal: principal, grant: grant, server: server}, nil, ""
}

func quoteInteractiveShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func terminalCommandResult(buffer []byte, marker string) (string, int, bool) {
	text := strings.ReplaceAll(toValidUTF8(buffer), "\r\n", "\n")
	needle := "\n" + marker + ":"
	index := strings.LastIndex(text, needle)
	if index < 0 {
		return "", 0, false
	}
	lineStart := index + len(needle)
	lineEnd := strings.IndexByte(text[lineStart:], '\n')
	if lineEnd < 0 {
		return "", 0, false
	}
	codeText := strings.TrimSpace(text[lineStart : lineStart+lineEnd])
	exitCode, err := strconv.Atoi(codeText)
	if err != nil {
		return "", 0, false
	}
	return stripANSI(text[:index]), exitCode, true
}

func (s *Server) callMCPServerTerminalCommand(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	serverID := int64FromAny(args["server_id"])
	command, _ := args["command"].(string)
	if serverID <= 0 || strings.TrimSpace(command) == "" {
		return mcpPlainFailureResult("invalid_input", "server_id and command are required"), nil
	}
	timeoutMS := int(int64FromAny(args["timeout_ms"]))
	if timeoutMS == 0 {
		timeoutMS = 5000
	}
	if timeoutMS < 100 || timeoutMS > 300000 {
		return mcpPlainFailureResult("invalid_input", "timeout_ms must be between 100 and 300000"), nil
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "login"
	}
	authz, denied, _ := s.authorizeMCPTerminal(ctx, req, serverID)
	if denied != nil {
		return denied, nil
	}
	session, err := s.prepareInteractiveSession(ctx, InteractiveOwnerMCP, authz.server, authz.grant.UserID, authz.grant.Grant.GrantID, authz.grant.ClientID, authz.grant.PrivilegedGrant.ID, 120, 32, mode)
	if err != nil {
		code := "terminal_prepare_failed"
		if coded, ok := err.(interface{ Code() string }); ok {
			code = coded.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	defer s.closeMCPTerminalSession(session.ID, "command_complete")
	if err := s.waitForTerminalReady(session); err != nil {
		code := "terminal_prepare_timeout"
		if coded, ok := err.(interface{ Code() string }); ok {
			code = coded.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	// Give the login shell a short prompt/profile grace period, then disable echo
	// in this disposable PTY so the command and completion marker are not
	// mistaken for command output.
	time.Sleep(150 * time.Millisecond)
	if err := s.mcpTerminalWrite(session, "stty -echo\n"); err != nil {
		return mcpPlainFailureResult("terminal_write_failed", err.Error()), nil
	}
	time.Sleep(75 * time.Millisecond)
	cursor := session.outputBuffer.NextSeq() - 1
	markerToken, err := security.RandomToken(12)
	if err != nil {
		return mcpPlainFailureResult("terminal_command_failed", err.Error()), nil
	}
	marker := "__OBOARD_MCP_DONE_" + markerToken + "__"
	payload := "eval " + quoteInteractiveShell(command) + "\n__oboard_status=$?\nprintf '\\n" + marker + ":%d\\n' \"$__oboard_status\"\n"
	if err := s.mcpTerminalWrite(session, payload); err != nil {
		return mcpPlainFailureResult("terminal_write_failed", err.Error()), nil
	}

	deadline := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer deadline.Stop()
	collected := make([]byte, 0, 32768)
	outputTruncated := false
	for {
		chunk, nextCursor, lost, _, _ := session.outputBuffer.ReadAfter(cursor, 65536)
		if len(chunk) > 0 {
			cursor = nextCursor
			collected = append(collected, chunk...)
			if lost || len(collected) > terminalMCPBufferMax {
				outputTruncated = true
				if len(collected) > terminalMCPBufferMax {
					collected = append([]byte(nil), collected[len(collected)-terminalMCPBufferMax:]...)
				}
			}
			if output, exitCode, complete := terminalCommandResult(collected, marker); complete {
				s.recordToolCall(ctx, authz.principal, "server_terminal_command", map[string]any{
					"server_id": serverID, "mode": mode, "timeout_ms": timeoutMS, "output_bytes": len(output), "exit_code": exitCode,
				}, "succeeded", capability.DataSensitive)
				return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{
					"status": "succeeded", "server_id": serverID, "output": output,
					"exit_code": exitCode, "output_truncated": outputTruncated,
				}))
			}
		}
		session.mu.Lock()
		closed := session.Closed || session.closed
		reason := session.CloseReason
		session.mu.Unlock()
		if closed {
			return mcpPlainFailureResult("terminal_closed", "terminal closed before command completion: "+reason), nil
		}
		select {
		case <-ctx.Done():
			return mcpPlainFailureResult("terminal_command_cancelled", ctx.Err().Error()), nil
		case <-deadline.C:
			return mcpPlainFailureResult("terminal_command_timeout", "PTY command timed out"), nil
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (s *Server) callMCPServerTerminalOpen(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	serverID := int64FromAny(args["server_id"])
	if serverID <= 0 {
		return mcpPlainFailureResult("invalid_input", "server_id is required"), nil
	}
	authz, denied, _ := s.authorizeMCPTerminal(ctx, req, serverID)
	if denied != nil {
		return denied, nil
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "login"
	}
	cols := int(int64FromAny(args["cols"]))
	rows := int(int64FromAny(args["rows"]))
	privilegedID := authz.grant.PrivilegedGrant.ID
	session, err := s.prepareInteractiveSession(ctx, InteractiveOwnerMCP, authz.server, authz.grant.UserID, authz.grant.Grant.GrantID, authz.grant.ClientID, privilegedID, cols, rows, mode)
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
	s.recordToolCall(ctx, authz.principal, "server_terminal_open", map[string]any{
		"server_id": serverID, "session_id": session.ID, "mode": mode, "cols": cols, "rows": rows,
	}, "succeeded", capability.DataSensitive)
	// also audit event
	serverIDCopy := serverID
	s.recordRemoteAccessAuditWithContext(ctx, "", model.RemoteAccessAuditEvent{
		EventType: model.RemoteAccessAuditMCPInteractiveOpen, ActorType: "mcp", OAuthGrantID: authz.grant.Grant.GrantID, OAuthClientID: authz.grant.ClientID,
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
	grant, err := s.mcpGrantPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPrivilegedDeniedResult(mcpauth.CodePrivilegedGrantRequired, "Interactive terminal requires an active Privileged MCP Grant.", model.PrivilegeRemoteInteractive, 0), nil
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
	if session.OwnerType != InteractiveOwnerMCP || session.OAuthGrantID != grant.Grant.GrantID || session.OAuthClientID != grant.ClientID || (session.UserID != 0 && grant.UserID != 0 && session.UserID != grant.UserID) {
		return mcpPlainFailureResult("terminal_not_owner", "session does not belong to this grant"), nil
	}
	if grant.PrivilegedGrant == nil || session.PrivilegedGrantID == 0 || session.PrivilegedGrantID != grant.PrivilegedGrant.ID {
		s.closeMCPTerminalSession(sessionID, "privileged_grant_revoked")
		return mcpPrivilegedDeniedResult("privileged_grant_revoked", "The Privileged MCP Grant for this terminal is no longer active.", model.PrivilegeRemoteInteractive, session.ServerID), nil
	}
	authz, denied, denialCode := s.authorizeMCPTerminal(ctx, req, session.ServerID)
	if denied != nil {
		s.closeMCPTerminalSession(sessionID, denialCode)
		return denied, nil
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
		if strings.HasPrefix(closeReason, "privileged_grant") {
			return mcpPrivilegedDeniedResult("privileged_grant_revoked", "The Privileged MCP Grant for this terminal is no longer active.", model.PrivilegeRemoteInteractive, session.ServerID), nil
		}
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
	s.recordToolCall(ctx, authz.principal, "server_terminal_io", map[string]any{
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
		if strings.HasPrefix(closeReason, "privileged_grant") {
			return mcpPrivilegedDeniedResult("privileged_grant_revoked", "The Privileged MCP Grant for this terminal is no longer active.", model.PrivilegeRemoteInteractive, session.ServerID), nil
		}
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
		return mcpPrivilegedDeniedResult(mcpauth.CodePrivilegedGrantRequired, "Interactive terminal requires an active Privileged MCP Grant.", model.PrivilegeRemoteInteractive, 0), nil
	}
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	sessionID := strings.TrimSpace(fmt.Sprint(args["session_id"]))
	if sessionID == "" {
		return mcpPlainFailureResult("invalid_input", "session_id is required"), nil
	}
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	s.terminalHub.mu.Unlock()
	if session == nil {
		return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{"closed": true, "session_id": sessionID}))
	}
	if session.OwnerType != InteractiveOwnerMCP || session.OAuthGrantID != grant.Grant.GrantID || session.OAuthClientID != grant.ClientID || (session.UserID != 0 && grant.UserID != 0 && session.UserID != grant.UserID) {
		return mcpPlainFailureResult("terminal_not_owner", "session does not belong to this grant"), nil
	}
	if grant.PrivilegedGrant == nil || session.PrivilegedGrantID == 0 || session.PrivilegedGrantID != grant.PrivilegedGrant.ID {
		s.closeMCPTerminalSession(sessionID, "privileged_grant_revoked")
		return mcpPrivilegedDeniedResult("privileged_grant_revoked", "The Privileged MCP Grant for this terminal is no longer active.", model.PrivilegeRemoteInteractive, session.ServerID), nil
	}
	authz, denied, denialCode := s.authorizeMCPTerminal(ctx, req, session.ServerID)
	if denied != nil {
		s.closeMCPTerminalSession(sessionID, denialCode)
		return denied, nil
	}
	s.closeMCPTerminalSession(sessionID, "user_close")
	serverID := session.ServerID
	s.recordRemoteAccessAuditWithContext(ctx, "", model.RemoteAccessAuditEvent{
		EventType: model.RemoteAccessAuditMCPInteractiveClose, ServerID: &serverID, SessionID: sessionID,
		OAuthGrantID: authz.grant.Grant.GrantID, OAuthClientID: authz.grant.ClientID,
		Capability: model.PrivilegeRemoteInteractive, Result: "closed",
	})
	s.recordToolCall(ctx, authz.principal, "server_terminal_close", map[string]any{"session_id": sessionID, "server_id": serverID}, "succeeded", capability.DataSensitive)
	return mcpEnvelopeResult(newToolEnvelope("succeeded", "", map[string]any{"closed": true, "session_id": sessionID}))
}

func (s *Server) callMCPServerTerminalResize(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	grant, err := s.mcpGrantPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPrivilegedDeniedResult(mcpauth.CodePrivilegedGrantRequired, "Interactive terminal requires an active Privileged MCP Grant.", model.PrivilegeRemoteInteractive, 0), nil
	}
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	sessionID := strings.TrimSpace(fmt.Sprint(args["session_id"]))
	cols := int(int64FromAny(args["cols"]))
	rows := int(int64FromAny(args["rows"]))
	s.terminalHub.mu.Lock()
	session := s.terminalHub.sessions[sessionID]
	s.terminalHub.mu.Unlock()
	if session == nil {
		return mcpPlainFailureResult("terminal_not_found", "terminal session not found"), nil
	}
	if session.OwnerType != InteractiveOwnerMCP || session.OAuthGrantID != grant.Grant.GrantID || session.OAuthClientID != grant.ClientID || (session.UserID != 0 && grant.UserID != 0 && session.UserID != grant.UserID) {
		return mcpPlainFailureResult("terminal_not_owner", "session does not belong to this grant"), nil
	}
	if grant.PrivilegedGrant == nil || session.PrivilegedGrantID == 0 || session.PrivilegedGrantID != grant.PrivilegedGrant.ID {
		s.closeMCPTerminalSession(sessionID, "privileged_grant_revoked")
		return mcpPrivilegedDeniedResult("privileged_grant_revoked", "The Privileged MCP Grant for this terminal is no longer active.", model.PrivilegeRemoteInteractive, session.ServerID), nil
	}
	authz, denied, denialCode := s.authorizeMCPTerminal(ctx, req, session.ServerID)
	if denied != nil {
		s.closeMCPTerminalSession(sessionID, denialCode)
		return denied, nil
	}
	if err := s.mcpTerminalResize(session, cols, rows); err != nil {
		code := "terminal_resize_failed"
		if coded, ok := err.(interface{ Code() string }); ok {
			code = coded.Code()
		}
		return mcpPlainFailureResult(code, err.Error()), nil
	}
	s.recordToolCall(ctx, authz.principal, "server_terminal_resize", map[string]any{"session_id": sessionID, "cols": cols, "rows": rows}, "succeeded", capability.DataSensitive)
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
