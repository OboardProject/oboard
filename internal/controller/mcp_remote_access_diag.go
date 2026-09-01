package controller

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) registerRemoteAccessDiagnosticTools(server *mcp.Server) {
	server.AddTool(&mcp.Tool{
		Name:        "server_remote_access_get",
		Title:       "Get Server Remote Access Status",
		Description: "返回服务器全局与逐台远程访问策略、有效状态、Privileged Grant、Agent 状态与阻断原因。用于诊断 remote_access_global_disabled / remote_access_server_disabled 等失败，不需要额外 Privileged Grant 即可查询自身边界内的服务器。",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"server_id": map[string]any{"type": "integer", "minimum": 1},
		}, "server_id")),
		Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callMCPServerRemoteAccessGet(ctx, req)
	})
}

func (s *Server) callMCPServerRemoteAccessGet(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, err := s.mcpPrincipalFromRequest(ctx, req)
	if err != nil {
		return mcpPlainFailureResult("oauth_token_invalid", err.Error()), nil
	}
	grant, _ := s.mcpGrantPrincipalFromRequest(ctx, req)
	args, err := mcpToolArguments(req)
	if err != nil {
		return mcpPlainFailureResult("invalid_input", err.Error()), nil
	}
	serverID := int64FromAny(args["server_id"])
	if serverID <= 0 {
		return mcpPlainFailureResult("invalid_input", "server_id is required"), nil
	}
	// Authorization: require servers:read equivalent (via capability descriptor for servers.get)
	desc, ok := s.capabilities.Get("servers.get")
	if !ok {
		return mcpPlainFailureResult("capability_unavailable", "servers.get capability not found"), nil
	}
	decision := s.authorizeCapability(ctx, desc, map[string]any{"id": serverID})
	if !decision.Allowed {
		return mcpFailureResult(decision, ""), nil
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return mcpPlainFailureResult("not_found", err.Error()), nil
	}
	// Also check resource boundary via the same decision's resource refs
	// Already done via authorizeCapability, but for completeness ensure machine principal boundary
	// Use diagnostic view builder which also checks privileged grant boundary
	var grantPtr *mcpauth.GrantPrincipal
	if grant.Grant.GrantID != "" {
		grantPtr = &grant
	}
	view, err := s.remoteAccessDiagnosticView(ctx, server, grantPtr)
	if err != nil {
		return mcpPlainFailureResult("internal_error", err.Error()), nil
	}
	s.recordToolCall(ctx, principal, "server_remote_access_get", args, "succeeded", capability.DataInternal)
	// Envelope with remediation per spec §21
	return mcpEnvelopeResult(newToolEnvelope("succeeded", "", view))
}

func (s *Server) mcpRemoteAccessBlockerDetails(ctx context.Context, server *model.Server, privilege string) (code string, message string, details map[string]any) {
	// Helper to produce unified blocker details as per spec §20
	global, _ := s.globalRemoteAccessPolicyFromContext(ctx)
	policy, _ := s.store.GetServerRemoteAccessPolicy(ctx, server.ID)
	details = map[string]any{
		"server_id":            server.ID,
		"global_mcp_enabled":   global.MCPEnabled,
		"server_mcp_enabled":   policy.MCPEnabled,
		"effective_mcp_enabled": global.MCPEnabled && policy.MCPEnabled,
		"global_remote_terminal_enabled":   global.RemoteTerminalEnabled,
		"server_remote_terminal_enabled":   policy.RemoteTerminalEnabled,
		"effective_remote_terminal_enabled": global.RemoteTerminalEnabled && policy.RemoteTerminalEnabled,
	}
	if !global.MCPEnabled {
		return "remote_access_global_disabled", "MCP remote control is globally disabled", details
	}
	if !policy.MCPEnabled {
		return "remote_access_server_disabled", "MCP remote control is disabled for this server", details
	}
	return "", "", details
}

// enhanceMCPDeniedResult adds remediation and details for remote access denials per spec §21
func enhanceMCPDeniedResult(base *mcp.CallToolResult, blocker string, serverID int64) *mcp.CallToolResult {
	if base == nil {
		return base
	}
	// Currently mcpPrivilegedDeniedResult already encodes blocker; we keep it but add remediation via extra field
	// For future: inject remediation into envelope. To keep simple, return base; client can call server_remote_access_get for remediation.
	return base
}

