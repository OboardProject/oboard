package controller

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/capability"
)

type mcpSystemGetCapabilitiesInput struct{}
type mcpSystemBootstrapInput struct {
	ClientName    string `json:"client_name,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

func (s *Server) addMCPSystemCapabilitiesTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "system_get_capabilities",
		Title:       "Get Capabilities",
		Description: "STABLE CAPABILITY MANIFEST. Returns server_version, api_version, capability_revision, toolset_hash, min_mcp_protocol, features, and deprecated_tools. This tool name never changes; use its capability_revision and toolset_hash to detect new tools without polling server_version. Compare capability_revision to decide whether to re-call tools/list.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{})),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}),
		Annotations:  mcpAnnotations(true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpSystemGetCapabilitiesInput) (*mcp.CallToolResult, any, error) {
		manifest := s.mcpCurrentManifest()
		data := map[string]any{
			"server_version":      manifest.ServerVersion,
			"api_version":         manifest.APIVersion,
			"capability_revision": manifest.CapabilityRevision,
			"toolset_hash":        manifest.ToolsetHash,
			"min_mcp_protocol":    manifest.MinMCPProtocol,
			"features":            manifest.Features,
			"deprecated_tools":    manifest.DeprecatedTools,
			"tool_count":          manifest.ToolCount,
			"instructions_hash":   manifest.InstructionsHash,
		}
		if principal, err := mcpPrincipal(ctx); err == nil {
			s.recordToolCall(ctx, principal, "system.get_capabilities", map[string]any{}, "succeeded", capability.DataInternal)
		}
		return &mcp.CallToolResult{}, newToolEnvelope("succeeded", "", data), nil
	})
}

func (s *Server) addMCPSystemBootstrapTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "system_bootstrap",
		Title:       "Bootstrap",
		Description: "STABLE BOOTSTRAP. Returns the minimal authenticated context plus the capability manifest and recommended client behavior. Always available, never renamed. Use it to discover server identity, capability_revision, and whether dynamic_tool_discovery is recommended.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"client_name": map[string]any{"type": "string"}, "client_version": map[string]any{"type": "string"}, "protocol": map[string]any{"type": "string"}})),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}),
		Annotations:  mcpAnnotations(true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpSystemBootstrapInput) (*mcp.CallToolResult, any, error) {
		manifest := s.mcpCurrentManifest()
		bootstrap, _ := s.mcpBootstrapContext(ctx)
		data := map[string]any{
			"server": map[string]any{
				"version":             manifest.ServerVersion,
				"api_version":         manifest.APIVersion,
				"capability_revision": manifest.CapabilityRevision,
				"toolset_hash":        manifest.ToolsetHash,
				"min_mcp_protocol":    manifest.MinMCPProtocol,
			},
			"capability_revision": manifest.CapabilityRevision,
			"toolset_hash":        manifest.ToolsetHash,
			"manifest":            manifest,
			"recommended_client_behavior": map[string]any{
				"dynamic_tool_discovery": true,
				"poll_interval":          "on_list_changed",
			},
			"documentation": "Use notifications/tools/list_changed to refresh tools. Fall back to polling system_get_capabilities every 5m and comparing capability_revision/toolset_hash.",
			"modules":       mcpCapabilityModules(),
			"warnings":      []string{},
			"bootstrap":     bootstrap,
			"client": map[string]any{
				"name":     input.ClientName,
				"version":  input.ClientVersion,
				"protocol": input.Protocol,
			},
		}
		if principal, err := mcpPrincipal(ctx); err == nil {
			s.recordToolCall(ctx, principal, "system.bootstrap", input, "succeeded", capability.DataInternal)
		}
		return &mcp.CallToolResult{}, newToolEnvelope("succeeded", "", data), nil
	})
}

func mcpCapabilityModules() []string {
	return []string{"server", "node", "subscription", "audit", "traffic", "topology", "dns", "certificate", "deployment"}
}
