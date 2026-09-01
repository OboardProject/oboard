package controller

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

type serverRemoteAccessUpdateOperation struct {
	ServerID               int64 `json:"server_id"`
	RemoteTerminalEnabled *bool `json:"remote_terminal_enabled"`
	MCPEnabled            *bool `json:"mcp_enabled"`
}

func decodeServerRemoteAccessUpdateOperation(input json.RawMessage) (serverRemoteAccessUpdateOperation, error) {
	var request serverRemoteAccessUpdateOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	if request.ServerID <= 0 {
		return request, errors.New("positive server_id is required")
	}
	if request.RemoteTerminalEnabled == nil && request.MCPEnabled == nil {
		return request, errors.New("at least one of remote_terminal_enabled or mcp_enabled is required")
	}
	return request, nil
}

func (s *Server) registerRemoteAccessPolicyOperation() {
	s.automation.RegisterValidator("server.remote_access.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerRemoteAccessUpdateOperation(input)
		if err != nil {
			return nil, err
		}
		// Enforce server_remote_access_manage for non-interactive MCP principles (spec §14-16)
		if principal.AccessLevel != "" {
			if !slices.Contains(principal.PrivilegedClasses, model.PrivilegeServerRemoteAccessManage) {
				return nil, errors.New("privileged_capability_denied: server_remote_access_manage is required")
			}
			// Resource boundary for privileged grant is enforced via evaluator at call time, but we also check legacy filter here
		}
		if !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		// Additional privileged resource boundary check for MCP grants (when present)
		if principal.AccessLevel != "" && principal.GrantID != "" {
			if pg, err := s.store.GetMCPPrivilegedGrantByOAuthGrant(ctx, principal.GrantID); err == nil {
				if denied := s.privilegedGrantDeniesServer(pg, request.ServerID); denied {
					return nil, errors.New("privileged_resource_denied")
				}
			}
			if _, err := s.store.GetOAuthGrant(ctx, principal.GrantID); err == nil {
				// OAuth grant boundary also checked via AllowsInt64 already, but ensure mcpauth boundary as well
				// The legacy filter already covers it, but we keep for completeness
			}
		}
		server, err := s.store.GetServer(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		before, err := s.store.GetServerRemoteAccessPolicy(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		changes := map[string]any{}
		if request.RemoteTerminalEnabled != nil && *request.RemoteTerminalEnabled != before.RemoteTerminalEnabled {
			changes["remote_terminal_enabled"] = map[string]any{"old": before.RemoteTerminalEnabled, "new": *request.RemoteTerminalEnabled}
		}
		if request.MCPEnabled != nil && *request.MCPEnabled != before.MCPEnabled {
			changes["mcp_enabled"] = map[string]any{"old": before.MCPEnabled, "new": *request.MCPEnabled}
		}
		if len(changes) == 0 {
			return map[string]any{"server_id": server.ID, "server_name": server.Name, "no_change": true, "effective_mcp": before.MCPEnabled}, nil
		}
		// Preview includes global effective after change
		global, _ := s.globalRemoteAccessPolicyFromContext(ctx)
		effectiveMCP := global.MCPEnabled
		if request.MCPEnabled != nil {
			effectiveMCP = effectiveMCP && *request.MCPEnabled
		} else {
			effectiveMCP = effectiveMCP && before.MCPEnabled
		}
		effectiveRemote := global.RemoteTerminalEnabled
		if request.RemoteTerminalEnabled != nil {
			effectiveRemote = effectiveRemote && *request.RemoteTerminalEnabled
		} else {
			effectiveRemote = effectiveRemote && before.RemoteTerminalEnabled
		}
		return map[string]any{
			"server_id": server.ID, "server_name": server.Name,
			"changes": changes,
			"preview": map[string]any{
				"effective_remote_terminal_enabled": effectiveRemote,
				"effective_mcp_enabled":             effectiveMCP,
				"global": global,
			},
		}, nil
	})
	s.automation.RegisterRevisionResolver("server.remote_access.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := decodeServerRemoteAccessUpdateOperation(input)
		if err != nil || !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		server, err := s.store.GetServer(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		policy, err := s.store.GetServerRemoteAccessPolicy(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		// Use server updated_at plus policy updated_at as combined revision
		rev := server.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		if !policy.UpdatedAt.IsZero() {
			rev = rev + "|" + policy.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): rev, "server_remote_access:" + strconv.FormatInt(server.ID, 10): rev}, nil
	})
	s.automation.Register("server.remote_access.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerRemoteAccessUpdateOperation(input)
		if err != nil {
			return nil, err
		}
		if principal.AccessLevel != "" && !slices.Contains(principal.PrivilegedClasses, model.PrivilegeServerRemoteAccessManage) {
			return nil, errors.New("privileged_capability_denied: server_remote_access_manage is required")
		}
		if principal.AccessLevel != "" && principal.GrantID != "" {
			if pg, err := s.store.GetMCPPrivilegedGrantByOAuthGrant(ctx, principal.GrantID); err == nil {
				if denied := s.privilegedGrantDeniesServer(pg, request.ServerID); denied {
					return nil, errors.New("privileged_resource_denied")
				}
			}
		}
		server, err := s.store.GetServer(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		patch := RemoteAccessPolicyPatch{
			RemoteTerminalEnabled: request.RemoteTerminalEnabled,
			MCPEnabled:            request.MCPEnabled,
		}
		// Determine actor type for audit
		actorType := "machine"
		if principal.Interactive {
			actorType = "user"
		} else if strings.HasPrefix(principal.ID, "prn_") {
			actorType = "machine"
		}
		// Use non-nil context source IP if available (not required for machine)
		sourceIP := ""
		if principal.SourceIP.IsValid() {
			sourceIP = principal.SourceIP.String()
		}
		view, err := s.updateServerRemoteAccessPolicy(ctx, server, patch, actorType, sourceIP, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"server_id": server.ID, "server_name": server.Name,
			"remote_access": view,
			"requires_deployment": false,
			"requires_restart":    false,
		}, nil
	})
}

func (s *Server) privilegedGrantDeniesServer(grant *model.MCPPrivilegedGrant, serverID int64) bool {
	if grant == nil {
		return true
	}
	var boundary mcpauth.ResourceBoundary
	if err := json.Unmarshal(grant.ResourceBoundaryJSON, &boundary); err != nil {
		return true
	}
	ref := mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(serverID, 10)}
	return len(boundary.Denied([]mcpauth.ResourceRef{ref})) > 0
}
