package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	settingRemoteTerminalEnabled       = "remote_terminal_enabled"
	settingMCPRemoteOperationsEnabled  = "mcp_remote_operations_enabled"
	settingMCPStructuredExecEnabled    = "mcp_structured_exec_enabled"
	settingMCPRawShellEnabled          = "mcp_raw_shell_enabled"
)

type remoteAccessView struct {
	Global RemoteAccessGlobalPolicy     `json:"global"`
	Server model.ServerRemoteAccessPolicy `json:"server"`
	Effective struct {
		RemoteTerminal      bool `json:"remote_terminal"`
		MCPRemoteOperations bool `json:"mcp_remote_operations"`
		MCPStructuredExec   bool `json:"mcp_structured_exec"`
		MCPRawShell         bool `json:"mcp_raw_shell"`
	} `json:"effective"`
	Agent model.ServerRemoteAccessStatus `json:"agent"`
	Reasons []string `json:"unavailable_reasons,omitempty"`
	ActiveTerminals int `json:"active_terminals"`
}

type RemoteAccessGlobalPolicy struct {
	RemoteTerminalEnabled      bool `json:"remote_terminal_enabled"`
	MCPRemoteOperationsEnabled bool `json:"mcp_remote_operations_enabled"`
	MCPStructuredExecEnabled   bool `json:"mcp_structured_exec_enabled"`
	MCPRawShellEnabled         bool `json:"mcp_raw_shell_enabled"`
}

func (s *Server) globalRemoteAccessPolicy(r *http.Request) RemoteAccessGlobalPolicy {
	settings := s.runtimeSettings(r.Context())
	return RemoteAccessGlobalPolicy{
		RemoteTerminalEnabled:      settingBool(settings, settingRemoteTerminalEnabled, false),
		MCPRemoteOperationsEnabled: settingBool(settings, settingMCPRemoteOperationsEnabled, false),
		MCPStructuredExecEnabled:   settingBool(settings, settingMCPStructuredExecEnabled, false),
		MCPRawShellEnabled:         settingBool(settings, settingMCPRawShellEnabled, false),
	}
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
			fail(w, errors.New("admin role required"), http.StatusForbidden)
			return
		}
		var req struct {
			RemoteTerminalEnabled      *bool `json:"remote_terminal_enabled"`
			MCPRemoteOperationsEnabled *bool `json:"mcp_remote_operations_enabled"`
			MCPStructuredExecEnabled   *bool `json:"mcp_structured_exec_enabled"`
			MCPRawShellEnabled         *bool `json:"mcp_raw_shell_enabled"`
		}
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
	}
	if server.Status != model.ServerOnline {
		reasons = append(reasons, "agent_offline")
	}
	if !slices.Contains(view.Agent.Capabilities, model.RemoteAccessCapabilityTerminal) && feature == "remote_terminal" {
		reasons = append(reasons, "agent_upgrade_required")
	}
	if view.Agent.LocalMode == model.RemoteAccessModeHardened && feature == "remote_terminal" && !view.Agent.LocalAllow.RemoteTerminal {
		reasons = append(reasons, "agent_local_gate_denied")
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

func (s *Server) assertRemoteExecAllowed(r *http.Request, server *model.Server, privilege string) error {
	global := s.globalRemoteAccessPolicy(r)
	policy, err := s.store.GetServerRemoteAccessPolicy(r.Context(), server.ID)
	if err != nil {
		return err
	}
	status, err := s.store.GetServerRemoteAccessStatus(r.Context(), server.ID)
	if err != nil {
		return err
	}
	switch privilege {
	case model.PrivilegeRemoteOperations:
		if !global.MCPRemoteOperationsEnabled {
			return codedError("remote_access_global_disabled", "MCP remote operations are globally disabled")
		}
		if !policy.MCPRemoteOperationsEnabled {
			return codedError("remote_access_server_disabled", "MCP remote operations are disabled on this server")
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
	default:
		return codedError("privileged_grant_required", "unknown remote privilege")
	}
	if server.Status != model.ServerOnline {
		return codedError("agent_offline", "agent is offline")
	}
	if !slices.Contains(status.Capabilities, model.RemoteAccessCapabilityExec) {
		return codedError("agent_upgrade_required", "agent does not advertise remote_exec_v1")
	}
	if status.LocalMode == model.RemoteAccessModeHardened {
		allowed := false
		switch privilege {
		case model.PrivilegeRemoteOperations:
			allowed = status.LocalAllow.MCPRemoteOperations
		case model.PrivilegeRemoteExec:
			allowed = status.LocalAllow.MCPStructuredExec
		case model.PrivilegeRemoteShell:
			allowed = status.LocalAllow.MCPRawShell
		}
		if !allowed {
			return codedError("agent_local_gate_denied", "agent local security policy denied this operation")
		}
	}
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

func serverIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}
