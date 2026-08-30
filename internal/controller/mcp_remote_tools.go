package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
)

func remoteMCPToolName(capabilityName string) string {
	switch capabilityName {
	case "node.system_info":
		return "server_get_system_info"
	case "node.network_info":
		return "server_get_network_info"
	case "node.disk_usage":
		return "server_get_disk_usage"
	case "node.listeners":
		return "server_get_listeners"
	case "node.service_status":
		return "server_get_service_status"
	case "node.restart_service":
		return "server_restart_service"
	case "node.get_logs":
		return "server_get_logs"
	case "node.run_diagnostics":
		return "server_run_diagnostics"
	case "node.exec":
		return "server_exec"
	case "node.exec_shell":
		return "server_exec_shell"
	default:
		return ""
	}
}

func (s *Server) registerRemoteAccessMCPTools(server *mcp.Server) {
	for _, descriptor := range s.capabilities.AllMCPDescriptors() {
		if descriptor.PrivilegeClass == "" {
			continue
		}
		name := remoteMCPToolName(descriptor.Name)
		if name == "" {
			continue
		}
		desc := descriptor
		tool := &mcp.Tool{
			Name: name, Title: desc.Name, Description: remoteMCPToolDescription(desc),
			InputSchema: desc.InputSchema, Annotations: mcpAnnotations(desc.ReadOnly, desc.Idempotent),
		}
		server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return s.callRemoteAccessMCPTool(ctx, request, desc)
		})
	}
}

func (s *Server) callRemoteAccessMCPTool(ctx context.Context, request *mcp.CallToolRequest, descriptor capability.Descriptor) (*mcp.CallToolResult, error) {
	principal, err := s.mcpPrincipalFromRequest(ctx, request)
	if err != nil {
		return mcpPlainFailureResult("", err.Error()), nil
	}
	arguments, err := mcpToolArguments(request)
	if err != nil {
		return mcpPlainFailureResult("", err.Error()), nil
	}
	serverID := int64FromAny(arguments["server_id"])
	decision := s.authorizeCapability(ctx, descriptor, arguments)
	if !decision.Allowed {
		return mcpPrivilegedDecisionResult(decision, descriptor.PrivilegeClass, serverID), nil
	}
	if serverID <= 0 {
		return mcpPlainFailureResult("invalid_input", "server_id is required"), nil
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return mcpPlainFailureResult("not_found", err.Error()), nil
	}
	grant, _ := mcpGrantPrincipal(ctx)
	grantID := int64(0)
	if grant.PrivilegedGrant != nil {
		grantID = grant.PrivilegedGrant.ID
	}
	timeout := 40 * time.Second
	if seconds := intFromAny(arguments["timeout_seconds"]); seconds > 0 {
		timeout = time.Duration(seconds+10) * time.Second
	}
	var payload any
	privilege := descriptor.PrivilegeClass
	switch descriptor.Name {
	case "node.exec":
		argv := stringSlice(arguments["argv"])
		cwd, _ := arguments["cwd"].(string)
		body, err := newRemoteExecPayload(serverID, grantID, privilege, model.RemoteExecModeArgv, argv, "", cwd, intFromAny(arguments["timeout_seconds"]))
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		payload = body
		timeout = time.Duration(body.Limits.TimeoutSeconds+10) * time.Second
	case "node.exec_shell":
		command, _ := arguments["command"].(string)
		cwd, _ := arguments["cwd"].(string)
		body, err := newRemoteExecPayload(serverID, grantID, privilege, model.RemoteExecModeShell, nil, command, cwd, intFromAny(arguments["timeout_seconds"]))
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		payload = body
		timeout = time.Duration(body.Limits.TimeoutSeconds+10) * time.Second
	default:
		kind := remoteOperationKind(descriptor.Name)
		service, _ := arguments["service"].(string)
		body, err := newRemoteOperationPayload(serverID, grantID, kind, service, intFromAny(arguments["lines"]))
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		payload = body
	}
	result, execErr := s.waitRemoteExec(ctx, grant, server, privilege, payload, timeout)
	if execErr != nil {
		code := ""
		if coded, ok := execErr.(interface{ Code() string }); ok {
			code = coded.Code()
		}
		s.recordToolCall(ctx, principal, descriptor.Name, arguments, "failed", descriptor.DataClassification)
		if isRemoteAccessDenialCode(code) {
			return mcpPrivilegedDeniedResult(code, execErr.Error(), descriptor.PrivilegeClass, serverID), nil
		}
		if result != nil {
			raw, _ := json.Marshal(result)
			return mcpPlainFailureResult(code, execErr.Error()+": "+string(raw)), nil
		}
		return mcpPlainFailureResult(code, execErr.Error()), nil
	}
	s.recordToolCall(ctx, principal, descriptor.Name, arguments, "succeeded", descriptor.DataClassification)
	return mcpEnvelopeResult(newToolEnvelope("succeeded", "", result))
}

func remoteMCPToolDescription(descriptor capability.Descriptor) string {
	return fmt.Sprintf("%s\n\nThis tool is always discoverable to MCP principals with operate access, but execution requires an active %s Privileged MCP Grant and the target server inside both OAuth and privileged resource boundaries. Missing authorization returns a structured denial and does not schedule host work.", descriptor.Description, descriptor.PrivilegeClass)
}

func isRemoteAccessDenialCode(code string) bool {
	switch code {
	case "privileged_grant_required", "privileged_grant_revoked", "privileged_grant_expired", "privileged_resource_denied",
		"remote_access_global_disabled", "remote_access_server_disabled", "agent_offline", "agent_upgrade_required", "agent_local_gate_denied":
		return true
	default:
		return false
	}
}

func remoteOperationKind(name string) string {
	switch name {
	case "node.system_info":
		return model.RemoteOperationSystemInfo
	case "node.network_info":
		return model.RemoteOperationNetworkInfo
	case "node.disk_usage":
		return model.RemoteOperationDiskUsage
	case "node.listeners":
		return model.RemoteOperationListeners
	case "node.service_status":
		return model.RemoteOperationServiceStatus
	case "node.restart_service":
		return model.RemoteOperationServiceRestart
	case "node.get_logs":
		return model.RemoteOperationLogs
	case "node.run_diagnostics":
		return model.RemoteOperationDiagnostics
	default:
		return name
	}
}

func intFromAny(value any) int { return int(int64FromAny(value)) }

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}
