package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	settingRemoteTerminalEnabled                     = "remote_terminal_enabled"
	settingRemoteTerminalPasswordConfirmationEnabled = "remote_terminal_password_confirmation_enabled"
	settingMCPRemoteOperationsEnabled                = "mcp_remote_operations_enabled"
	settingMCPStructuredExecEnabled                  = "mcp_structured_exec_enabled"
	settingMCPRawShellEnabled                        = "mcp_raw_shell_enabled"
	settingMCPInteractiveTerminalEnabled             = "mcp_interactive_terminal_enabled"
)

type remoteAccessView struct {
	Global    RemoteAccessGlobalPolicy       `json:"global"`
	Server    model.ServerRemoteAccessPolicy `json:"server"`
	Effective struct {
		RemoteTerminal       bool `json:"remote_terminal"`
		MCPRemoteOperations  bool `json:"mcp_remote_operations"`
		MCPStructuredExec    bool `json:"mcp_structured_exec"`
		MCPRawShell          bool `json:"mcp_raw_shell"`
		MCPInteractive       bool `json:"mcp_interactive_terminal"`
		MCPInteractiveLegacy bool `json:"mcp_interactive_terminal_legacy,omitempty"`
	} `json:"effective"`
	Agent           model.ServerRemoteAccessStatus `json:"agent"`
	Reasons         []string                       `json:"unavailable_reasons,omitempty"`
	ActiveTerminals int                            `json:"active_terminals"`
}

type RemoteAccessGlobalPolicy struct {
	RemoteTerminalEnabled       bool `json:"remote_terminal_enabled"`
	MCPRemoteOperationsEnabled  bool `json:"mcp_remote_operations_enabled"`
	MCPStructuredExecEnabled    bool `json:"mcp_structured_exec_enabled"`
	MCPRawShellEnabled          bool `json:"mcp_raw_shell_enabled"`
	MCPInteractiveEnabled       bool `json:"mcp_interactive_terminal_enabled"`
}

type RemoteAccessPolicyPatch struct {
	RemoteTerminalEnabled      *bool `json:"remote_terminal_enabled"`
	MCPRemoteOperationsEnabled *bool `json:"mcp_remote_operations_enabled"`
	MCPStructuredExecEnabled   *bool `json:"mcp_structured_exec_enabled"`
	MCPRawShellEnabled         *bool `json:"mcp_raw_shell_enabled"`
	MCPInteractiveEnabled      *bool `json:"mcp_interactive_terminal_enabled"`
}

func (s *Server) globalRemoteAccessPolicy(r *http.Request) RemoteAccessGlobalPolicy {
	settings := s.runtimeSettings(r.Context())
	return globalRemoteAccessPolicyFromSettings(settings)
}

func globalRemoteAccessPolicyFromSettings(settings map[string]string) RemoteAccessGlobalPolicy {
	return RemoteAccessGlobalPolicy{
		RemoteTerminalEnabled:      settingBool(settings, settingRemoteTerminalEnabled, true),
		MCPRemoteOperationsEnabled: settingBool(settings, settingMCPRemoteOperationsEnabled, false),
		MCPStructuredExecEnabled:   settingBool(settings, settingMCPStructuredExecEnabled, false),
		MCPRawShellEnabled:         settingBool(settings, settingMCPRawShellEnabled, false),
		MCPInteractiveEnabled:      settingBool(settings, settingMCPInteractiveTerminalEnabled, false),
	}
}

func (s *Server) globalRemoteAccessPolicyFromContext(ctx context.Context) (RemoteAccessGlobalPolicy, error) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return RemoteAccessGlobalPolicy{}, err
	}
	return globalRemoteAccessPolicyFromSettings(settings), nil
}

func (s *Server) serverRemoteAccess(w http.ResponseWriter, r *http.Request, serverID int64) {
	server, err := s.store.GetServer(r.Context(), serverID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		view, err := s.remoteAccessView(r, server)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"remote_access": view})
	case http.MethodPatch, http.MethodPost:
		if !roleAllows(currentRole(r), model.RoleAdmin) {
			fail(w, codedError("forbidden", "admin role required"), http.StatusForbidden)
			return
		}
		var req RemoteAccessPolicyPatch
		if !decode(w, r, &req) {
			return
		}
		policy, err := s.store.GetServerRemoteAccessPolicy(r.Context(), serverID)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		if req.RemoteTerminalEnabled != nil {
			policy.RemoteTerminalEnabled = *req.RemoteTerminalEnabled
		}
		if req.MCPRemoteOperationsEnabled != nil {
			policy.MCPRemoteOperationsEnabled = *req.MCPRemoteOperationsEnabled
		}
		if req.MCPStructuredExecEnabled != nil {
			policy.MCPStructuredExecEnabled = *req.MCPStructuredExecEnabled
		}
		if req.MCPRawShellEnabled != nil {
			policy.MCPRawShellEnabled = *req.MCPRawShellEnabled
		}
		if req.MCPInteractiveEnabled != nil {
			policy.MCPInteractiveEnabled = *req.MCPInteractiveEnabled
		}
		policy.ServerID = serverID
		saved, err := s.store.UpsertServerRemoteAccessPolicy(r.Context(), policy)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		_ = saved
		view, err := s.remoteAccessView(r, server)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"remote_access": view})
	default:
		method(w)
	}
}

func (s *Server) remoteAccessView(r *http.Request, server *model.Server) (remoteAccessView, error) {
	policy, err := s.store.GetServerRemoteAccessPolicy(r.Context(), server.ID)
	if err != nil {
		return remoteAccessView{}, err
	}
	status, err := s.store.GetServerRemoteAccessStatus(r.Context(), server.ID)
	if err != nil {
		return remoteAccessView{}, err
	}
	global := s.globalRemoteAccessPolicy(r)
	view := remoteAccessView{Global: global, Server: policy, Agent: status, ActiveTerminals: s.terminalHub.countForServer(server.ID)}
	view.Effective.RemoteTerminal = global.RemoteTerminalEnabled && policy.RemoteTerminalEnabled
	view.Effective.MCPRemoteOperations = global.MCPRemoteOperationsEnabled && policy.MCPRemoteOperationsEnabled
	view.Effective.MCPStructuredExec = global.MCPStructuredExecEnabled && policy.MCPStructuredExecEnabled
	view.Effective.MCPRawShell = global.MCPRawShellEnabled && policy.MCPRawShellEnabled
	view.Effective.MCPInteractive = global.MCPInteractiveEnabled && policy.MCPInteractiveEnabled
	view.Reasons = s.remoteAccessUnavailableReasons(server, view, "remote_terminal")
	return view, nil
}

func (s *Server) remoteAccessUnavailableReasons(server *model.Server, view remoteAccessView, feature string) []string {
	reasons := []string{}
	switch feature {
	case "remote_terminal":
		if !view.Global.RemoteTerminalEnabled {
			reasons = append(reasons, "remote_access_global_disabled")
		}
		if !view.Server.RemoteTerminalEnabled {
			reasons = append(reasons, "remote_access_server_disabled")
		}
		if server.Status != model.ServerOnline {
			reasons = append(reasons, "agent_offline")
		}
		if !slices.Contains(view.Agent.Capabilities, model.RemoteAccessCapabilityTerminal) {
			reasons = append(reasons, "agent_upgrade_required")
		}
		if view.Agent.LocalMode == model.RemoteAccessModeHardened && !view.Agent.LocalAllow.RemoteTerminal {
			reasons = append(reasons, "agent_local_gate_denied")
		}
	case "mcp_remote_operations", "remote_operations":
		if !view.Global.MCPRemoteOperationsEnabled {
			reasons = append(reasons, "remote_access_global_disabled")
		}
		if !view.Server.MCPRemoteOperationsEnabled {
			reasons = append(reasons, "remote_access_server_disabled")
		}
		if server.Status != model.ServerOnline {
			reasons = append(reasons, "agent_offline")
		}
		if !slices.Contains(view.Agent.Capabilities, model.RemoteAccessCapabilityExec) {
			reasons = append(reasons, "agent_upgrade_required")
		}
		if view.Agent.LocalMode == model.RemoteAccessModeHardened && !view.Agent.LocalAllow.MCPRemoteOperations {
			reasons = append(reasons, "agent_local_gate_denied")
		}
	case "mcp_structured_exec", "remote_exec", "structured_exec":
		if !view.Global.MCPStructuredExecEnabled {
			reasons = append(reasons, "remote_access_global_disabled")
		}
		if !view.Server.MCPStructuredExecEnabled {
			reasons = append(reasons, "remote_access_server_disabled")
		}
		if server.Status != model.ServerOnline {
			reasons = append(reasons, "agent_offline")
		}
		if !slices.Contains(view.Agent.Capabilities, model.RemoteAccessCapabilityExec) {
			reasons = append(reasons, "agent_upgrade_required")
		}
		if view.Agent.LocalMode == model.RemoteAccessModeHardened && !view.Agent.LocalAllow.MCPStructuredExec {
			reasons = append(reasons, "agent_local_gate_denied")
		}
	case "mcp_raw_shell", "remote_shell", "raw_shell":
		if !view.Global.MCPRawShellEnabled {
			reasons = append(reasons, "remote_access_global_disabled")
		}
		if !view.Server.MCPRawShellEnabled {
			reasons = append(reasons, "remote_access_server_disabled")
		}
		if server.Status != model.ServerOnline {
			reasons = append(reasons, "agent_offline")
		}
		if !slices.Contains(view.Agent.Capabilities, model.RemoteAccessCapabilityExec) {
			reasons = append(reasons, "agent_upgrade_required")
		}
		if view.Agent.LocalMode == model.RemoteAccessModeHardened && !view.Agent.LocalAllow.MCPRawShell {
			reasons = append(reasons, "agent_local_gate_denied")
		}
	case "mcp_interactive_terminal", "remote_interactive", "interactive_terminal":
		if !view.Global.MCPInteractiveEnabled {
			reasons = append(reasons, "remote_access_global_disabled")
		}
		if !view.Server.MCPInteractiveEnabled {
			reasons = append(reasons, "remote_access_server_disabled")
		}
		if server.Status != model.ServerOnline {
			reasons = append(reasons, "agent_offline")
		}
		if !slices.Contains(view.Agent.Capabilities, model.RemoteAccessCapabilityInteractiveMCP) {
			reasons = append(reasons, "agent_upgrade_required")
		}
		if view.Agent.LocalMode == model.RemoteAccessModeHardened && !view.Agent.LocalAllow.MCPInteractive {
			reasons = append(reasons, "agent_local_gate_denied")
		}
	default:
		if server.Status != model.ServerOnline {
			reasons = append(reasons, "agent_offline")
		}
	}
	return reasons
}

func (s *Server) remoteAccessAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := s.store.ListRemoteAccessAudit(r.Context(), intQuery(r, "limit", 100))
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"events": items})
}

// assertRemotePrivilegeAllowed is the single entry for all remote privilege checks.
// It validates global + server + agent online + capability + local gate for the requested privilege.
// Privileges: remote_terminal, remote_operations, remote_exec, remote_shell, remote_interactive
func (s *Server) assertRemotePrivilegeAllowed(ctx context.Context, server *model.Server, privilege string) error {
	global, err := s.globalRemoteAccessPolicyFromContext(ctx)
	if err != nil {
		return err
	}
	policy, err := s.store.GetServerRemoteAccessPolicy(ctx, server.ID)
	if err != nil {
		return err
	}
	status, err := s.store.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		return err
	}
	switch privilege {
	case "remote_terminal", model.PrivilegeRemoteOperations, model.PrivilegeRemoteExec, model.PrivilegeRemoteShell, model.PrivilegeRemoteInteractive:
	default:
		// allow legacy alias
		switch strings.TrimSpace(privilege) {
		case "mcp_remote_operations", "mcp_structured_exec", "mcp_raw_shell", "mcp_interactive_terminal":
		default:
			return codedError("privileged_grant_required", "unknown remote privilege")
		}
	}
	switch privilege {
	case "remote_terminal":
		if !global.RemoteTerminalEnabled {
			return codedError("remote_access_global_disabled", "remote terminal is globally disabled")
		}
		if !policy.RemoteTerminalEnabled {
			return codedError("remote_access_server_disabled", "remote terminal is disabled on this server")
		}
		if server.Status != model.ServerOnline {
			return codedError("agent_offline", "agent is offline")
		}
		if !slices.Contains(status.Capabilities, model.RemoteAccessCapabilityTerminal) {
			return codedError("agent_upgrade_required", "agent does not advertise remote_terminal_v1")
		}
		if status.LocalMode == model.RemoteAccessModeHardened && !status.LocalAllow.RemoteTerminal {
			return codedError("agent_local_gate_denied", "agent local security policy denied this operation")
		}
		return nil
	case model.PrivilegeRemoteOperations, "mcp_remote_operations":
		if !global.MCPRemoteOperationsEnabled {
			return codedError("remote_access_global_disabled", "MCP remote operations are globally disabled")
		}
		if !policy.MCPRemoteOperationsEnabled {
			return codedError("remote_access_server_disabled", "MCP remote operations are disabled on this server")
		}
	case model.PrivilegeRemoteExec, "mcp_structured_exec":
		if !global.MCPStructuredExecEnabled {
			return codedError("remote_access_global_disabled", "structured exec is globally disabled")
		}
		if !policy.MCPStructuredExecEnabled {
			return codedError("remote_access_server_disabled", "structured exec is disabled on this server")
		}
	case model.PrivilegeRemoteShell, "mcp_raw_shell":
		if !global.MCPRawShellEnabled {
			return codedError("remote_access_global_disabled", "raw shell is globally disabled")
		}
		if !policy.MCPRawShellEnabled {
			return codedError("remote_access_server_disabled", "raw shell is disabled on this server")
		}
	case model.PrivilegeRemoteInteractive, "mcp_interactive_terminal":
		if !global.MCPInteractiveEnabled {
			return codedError("remote_access_global_disabled", "interactive terminal is globally disabled")
		}
		if !policy.MCPInteractiveEnabled {
			return codedError("remote_access_server_disabled", "interactive terminal is disabled on this server")
		}
	}
	// Common checks for MCP privileges
	if privilege != "remote_terminal" {
		if server.Status != model.ServerOnline {
			return codedError("agent_offline", "agent is offline")
		}
		switch privilege {
		case model.PrivilegeRemoteOperations, model.PrivilegeRemoteExec, model.PrivilegeRemoteShell, "mcp_remote_operations", "mcp_structured_exec", "mcp_raw_shell":
			if !slices.Contains(status.Capabilities, model.RemoteAccessCapabilityExec) {
				return codedError("agent_upgrade_required", "agent does not advertise remote_exec_v1")
			}
		case model.PrivilegeRemoteInteractive, "mcp_interactive_terminal":
			if !slices.Contains(status.Capabilities, model.RemoteAccessCapabilityInteractiveMCP) {
				return codedError("agent_upgrade_required", "agent does not advertise remote_interactive_mcp_v1")
			}
		}
		if status.LocalMode == model.RemoteAccessModeHardened {
			allowed := false
			switch privilege {
			case model.PrivilegeRemoteOperations, "mcp_remote_operations":
				allowed = status.LocalAllow.MCPRemoteOperations
			case model.PrivilegeRemoteExec, "mcp_structured_exec":
				allowed = status.LocalAllow.MCPStructuredExec
			case model.PrivilegeRemoteShell, "mcp_raw_shell":
				allowed = status.LocalAllow.MCPRawShell
			case model.PrivilegeRemoteInteractive, "mcp_interactive_terminal":
				allowed = status.LocalAllow.MCPInteractive
			}
			if !allowed {
				return codedError("agent_local_gate_denied", "agent local security policy denied this operation")
			}
		}
	}
	return nil
}

// legacy wrapper for HTTP handlers that had separate global+server checks
func (s *Server) assertRemoteExecAllowed(r *http.Request, server *model.Server, privilege string) error {
	return s.assertRemotePrivilegeAllowed(r.Context(), server, privilege)
}

func (s *Server) assertRemoteExecAllowedHTTP(ctx context.Context, server *model.Server, privilege string) error {
	return s.assertRemotePrivilegeAllowed(ctx, server, privilege)
}

func normalizeRemoteAccessSwitches(remote, operations, exec, shell *bool) (*bool, *bool, error) {
	// Deprecated bundling logic removed. Keep for backward compat but make independent.
	// Return independent values without enforcing same-value constraint.
	return remote, operations, nil
}

// normalizeRemoteAccessPatch validates a patch with 5 independent switches.
// Each field is independent; no cross-field coupling.
func normalizeRemoteAccessPatch(patch RemoteAccessPolicyPatch) error {
	// No validation needed: all switches independent
	return nil
}

func (s *Server) recordRemoteAccessAudit(r *http.Request, event model.RemoteAccessAuditEvent) {
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now().UTC()
	}
	if event.SourceIP == "" {
		event.SourceIP = clientIP(r)
	}
	if event.MetadataJSON == nil {
		event.MetadataJSON = json.RawMessage(`{}`)
	}
	_ = s.store.InsertRemoteAccessAudit(r.Context(), event)
}

// recordRemoteAccessAuditWithContext allows MCP tools to audit without http.Request
func (s *Server) recordRemoteAccessAuditWithContext(ctx context.Context, sourceIP string, event model.RemoteAccessAuditEvent) {
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now().UTC()
	}
	if event.SourceIP == "" {
		event.SourceIP = sourceIP
	}
	if event.MetadataJSON == nil {
		event.MetadataJSON = json.RawMessage(`{}`)
	}
	_ = s.store.InsertRemoteAccessAudit(ctx, event)
}

func serverIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}

// closeMatchingMCPTerminalSessions closes MCP interactive sessions matching revocation criteria.
// Called when privileged grant revoked, boundary changed, global/server switches disabled.
func (s *Server) closeMatchingMCPTerminalSessions(predicate func(*terminalSession) bool, reason string) {
	if s.terminalHub == nil {
		return
	}
	s.terminalHub.mu.Lock()
	matching := []*terminalSession{}
	for _, sess := range s.terminalHub.sessions {
		if sess.OwnerType != InteractiveOwnerMCP {
			continue
		}
		if predicate(sess) {
			matching = append(matching, sess)
		}
	}
	for _, sess := range matching {
		delete(s.terminalHub.sessions, sess.ID)
	}
	s.terminalHub.mu.Unlock()
	for _, sess := range matching {
		sess.close(reason)
		_ = s.sendAgentControl(sess.ServerID, map[string]any{"type": "interactive_close", "session_id": sess.ID})
		if sess.ServerID != 0 {
			s.recordRemoteAccessAuditWithContext(context.Background(), "", model.RemoteAccessAuditEvent{
				EventType: model.RemoteAccessAuditMCPInteractiveClose, ServerID: &sess.ServerID, SessionID: sess.ID, OAuthGrantID: sess.OAuthGrantID, OAuthClientID: sess.OAuthClientID, Capability: model.PrivilegeRemoteInteractive, Result: reason,
			})
		}
	}
}

func (s *Server) closeMCPTerminalsForGrant(grantID string) {
	s.closeMatchingMCPTerminalSessions(func(sess *terminalSession) bool {
		return sess.OAuthGrantID == grantID
	}, "privileged_grant_revoked")
}

func (s *Server) closeMCPTerminalsForServer(serverID int64) {
	s.closeMatchingMCPTerminalSessions(func(sess *terminalSession) bool {
		return sess.ServerID == serverID
	}, "remote_access_disabled")
}

func (s *Server) enforceMCPTerminalsForGrant(grantID string, grant *model.MCPPrivilegedGrant) {
	if s.terminalHub == nil {
		return
	}
	policy := loadPrivilegedGrantPolicy(grant)
	// If grant is nil or inactive, all sessions for grant should be closed (already handled by revoke path).
	// Otherwise, close sessions where capability or resource boundary no longer allows server.
	if policy == nil {
		s.closeMCPTerminalsForGrant(grantID)
		return
	}
	hasInteractive := false
	for _, cap := range policy.Capabilities {
		if cap == model.PrivilegeRemoteInteractive {
			hasInteractive = true
			break
		}
	}
	s.closeMatchingMCPTerminalSessions(func(sess *terminalSession) bool {
		if sess.OAuthGrantID != grantID {
			return false
		}
		if !hasInteractive {
			return true
		}
		if sess.ServerID == 0 {
			return true
		}
		// Check resource boundary: if boundary denies server, close.
		if denied := policy.ResourceBoundary.Denied([]mcpauth.ResourceRef{{Type: "server", ID: strconv.FormatInt(sess.ServerID, 10)}}); len(denied) > 0 {
			return true
		}
		// Also check OAuth grant's own boundary? The outer evaluator already combined, but privileged boundary alone is authoritative for this check.
		// If server no longer in boundary, close.
		return false
	}, "privileged_grant_changed")
}

// ensure mcpauth import for enforce
var _ = mcpauth.ResourceBoundary{}

func assertRemotePrivilegeAllowedFromSettings(settings map[string]string, policy model.ServerRemoteAccessPolicy, status model.ServerRemoteAccessStatus, server *model.Server, privilege string) error {
	global := globalRemoteAccessPolicyFromSettings(settings)
	switch privilege {
	case model.PrivilegeRemoteInteractive:
		if !global.MCPInteractiveEnabled {
			return codedError("remote_access_global_disabled", "interactive terminal is globally disabled")
		}
		if !policy.MCPInteractiveEnabled {
			return codedError("remote_access_server_disabled", "interactive terminal is disabled on this server")
		}
	case model.PrivilegeRemoteExec:
		if !global.MCPStructuredExecEnabled {
			return codedError("remote_access_global_disabled", "structured exec is globally disabled")
		}
		if !policy.MCPStructuredExecEnabled {
			return codedError("remote_access_server_disabled", "structured exec is disabled on this server")
		}
	case model.PrivilegeRemoteShell:
		if !global.MCPRawShellEnabled {
			return codedError("remote_access_global_disabled", "raw shell is globally disabled")
		}
		if !policy.MCPRawShellEnabled {
			return codedError("remote_access_server_disabled", "raw shell is disabled on this server")
		}
	case model.PrivilegeRemoteOperations:
		if !global.MCPRemoteOperationsEnabled {
			return codedError("remote_access_global_disabled", "MCP remote operations are globally disabled")
		}
		if !policy.MCPRemoteOperationsEnabled {
			return codedError("remote_access_server_disabled", "MCP remote operations are disabled on this server")
		}
	}
	return nil
}

// wait helper not needed
var _ = context.Background
