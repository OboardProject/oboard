package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
)

// registerMCPCapabilityTools exposes every MCPEnabled capability as a stable
// per-capability tool. Read-only capabilities execute through the existing
// management query path; state-changing capabilities always create a Changeset
// and start the canonical Workflow.
func (s *Server) registerMCPCapabilityTools(server *mcp.Server, principal application.Principal) {
	for _, descriptor := range s.capabilities.ListMCP(principal) {
		if !descriptor.MCPEnabled {
			continue
		}
		if descriptor.ReadOnly {
			s.addMCPCapabilityReadTool(server, descriptor)
			continue
		}
		if descriptor.Executable {
			s.addMCPCapabilityWriteTool(server, descriptor)
		}
	}
}

func (s *Server) addMCPCapabilityReadTool(server *mcp.Server, descriptor capability.Descriptor) {
	name := mcpCapabilityToolName(descriptor.Name)
	tool := &mcp.Tool{
		Name:        name,
		Title:       descriptor.Name,
		Description: "Execute the OBoard read-only capability " + descriptor.Name + ". " + descriptor.Description,
		InputSchema: descriptor.InputSchema,
		Annotations: mcpAnnotations(true, descriptor.Idempotent),
	}
	server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		arguments, err := mcpToolArguments(request)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		decision := s.authorizeCapability(ctx, descriptor, arguments)
		if !decision.Allowed {
			return mcpFailureResult(decision, ""), nil
		}
		raw, err := json.Marshal(arguments)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		payload, err := s.queryManagementCapability(ctx, principal, descriptor.Name, raw)
		if err != nil {
			s.recordToolCall(ctx, principal, descriptor.Name, arguments, "failed", descriptor.DataClassification)
			return mcpPlainFailureResult("", err.Error()), nil
		}
		s.recordToolCall(ctx, principal, descriptor.Name, arguments, "succeeded", descriptor.DataClassification)
		return mcpEnvelopeResult(newToolEnvelope("succeeded", "", payload))
	})
}

type mcpCapabilityWriteInput struct {
	CapabilityInput    map[string]any    `json:"capability_input"`
	Reason             string            `json:"reason"`
	IdempotencyKey     string            `json:"idempotency_key"`
	ApprovalPreference string            `json:"approval_preference,omitempty"`
	ExpectedRevisions  map[string]string `json:"expected_revisions,omitempty"`
	Assumptions        []string          `json:"assumptions,omitempty"`
}

func (s *Server) addMCPCapabilityWriteTool(server *mcp.Server, descriptor capability.Descriptor) {
	name := mcpCapabilityToolName(descriptor.Name)
	inputSchema := mustRawSchema(closedMCPSchema(map[string]any{
		"capability_input":    descriptor.InputSchema,
		"reason":              map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
		"idempotency_key":     map[string]any{"type": "string", "minLength": 8, "maxLength": 200},
		"approval_preference": map[string]any{"type": "string", "enum": []string{"request_approval", "use_preapproval_if_available"}},
		"expected_revisions":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"assumptions":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, "capability_input", "reason", "idempotency_key"))
	tool := &mcp.Tool{
		Name:        name,
		Title:       descriptor.Name,
		Description: "Submit the OBoard state-changing capability " + descriptor.Name + " through the validated Changeset and Workflow. " + descriptor.Description,
		InputSchema: inputSchema,
		Annotations: mcpAnnotationsWrite(descriptor.Idempotent),
	}
	server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		arguments, err := mcpToolArguments(request)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		var input mcpCapabilityWriteInput
		if err := json.Unmarshal(encoded, &input); err != nil {
			return mcpPlainFailureResult("", err.Error()), nil
		}
		if len(input.CapabilityInput) == 0 || strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
			return mcpPlainFailureResult("", "capability_input, reason, and idempotency_key are required"), nil
		}
		decision := s.authorizeCapability(ctx, descriptor, input.CapabilityInput)
		if !decision.Allowed {
			return mcpFailureResult(decision, ""), nil
		}
		plan, err := s.buildDesiredStatePlan(ctx, principal, mcpPlanDesiredStateInput{
			CapabilityID:      descriptor.Name,
			Goal:              strings.TrimSpace(input.Reason),
			DesiredState:      input.CapabilityInput,
			ExpectedRevisions: input.ExpectedRevisions,
			Assumptions:       input.Assumptions,
		})
		if err != nil {
			s.recordToolCall(ctx, principal, descriptor.Name, input, "failed", descriptor.DataClassification)
			return mcpPlainFailureResult("", err.Error()), nil
		}
		result, err := s.submitPreparedOperations(ctx, principal, plan.Operations, plan.ExpectedRevisions, strings.TrimSpace(input.Reason), strings.TrimSpace(input.IdempotencyKey), input.ApprovalPreference)
		if err != nil {
			s.recordToolCall(ctx, principal, descriptor.Name, input, "failed", descriptor.DataClassification)
			return mcpPlainFailureResult("", err.Error()), nil
		}
		s.recordToolCall(ctx, principal, descriptor.Name, input, result.Status, descriptor.DataClassification)
		return mcpEnvelopeResult(result)
	})
}

func mcpCapabilityToolName(capabilityName string) string {
	sanitized := strings.NewReplacer(".", "_", "-", "_").Replace(capabilityName)
	if len(sanitized) <= 46 {
		return "oboard_capability_" + sanitized
	}
	sum := sha256.Sum256([]byte(capabilityName))
	return "oboard_capability_" + sanitized[:38] + "_" + hex.EncodeToString(sum[:3])
}

func mcpToolArguments(request *mcp.CallToolRequest) (map[string]any, error) {
	arguments := map[string]any{}
	if request.Params.Arguments == nil {
		return arguments, nil
	}
	raw, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return nil, errors.New("arguments must be JSON")
	}
	if string(raw) == "null" {
		return arguments, nil
	}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	return arguments, nil
}

func mcpEnvelopeResult(envelope *ToolEnvelope) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return mcpPlainFailureResult("", err.Error()), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
}

// registerMCPCapabilityResources adds a stable resource for every MCPEnabled
// read-only capability. Existing semantic URIs remain preferred; capabilities
// without one get a generic capability resource and, where the input schema
// has a single scalar argument, a matching {id} template.
func (s *Server) registerMCPCapabilityResources(server *mcp.Server, principal application.Principal) {
	covered := map[string]bool{}
	for _, def := range s.mcpResourceDefs() {
		if def.capability != "" {
			covered[def.capability] = true
		}
	}
	for _, def := range s.mcpResourceTemplateDefs() {
		if def.capability != "" {
			covered[def.capability] = true
		}
	}
	for _, descriptor := range s.capabilities.ListMCP(principal) {
		if !descriptor.MCPEnabled || !descriptor.ReadOnly || covered[descriptor.Name] {
			continue
		}
		def := mcpResourceDef{
			uri:         "oboard://capability/" + descriptor.Name,
			title:       "Capability: " + descriptor.Name,
			name:        descriptor.Name,
			description: "Return the current authorization-filtered view for the read-only capability " + descriptor.Name + ". " + descriptor.Description,
			capability:  descriptor.Name,
			kind:        "query_capability",
		}
		if !s.resourceAuthorized(principal, def) {
			continue
		}
		server.AddResource(&mcp.Resource{URI: def.uri, Title: def.title, Name: def.name, Description: def.description, MIMEType: "application/json"}, s.mcpResourceReadHandler(def))
		if property := mcpCapabilitySingleScalarID(descriptor); property != "" {
			template := def
			template.uri = def.uri + "/{id}"
			template.title = "Capability by ID: " + descriptor.Name
			template.description = "Return one authorization-filtered item for the read-only capability " + descriptor.Name + " using its required " + property + " argument."
			template.kind = "query_capability_template"
			server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: template.uri, Title: template.title, Name: template.name, Description: template.description, MIMEType: "application/json"}, s.mcpResourceReadHandler(template))
		}
	}
}

func mcpCapabilitySingleScalarID(descriptor capability.Descriptor) string {
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(descriptor.InputSchema, &schema) != nil {
		return ""
	}
	if len(schema.Required) != 1 {
		return ""
	}
	property := schema.Required[0]
	raw, ok := schema.Properties[property]
	if !ok {
		return ""
	}
	var field struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &field) != nil || field.Type != "integer" && field.Type != "string" {
		return ""
	}
	return property
}

func (s *Server) readMCPCapabilityResource(ctx context.Context, principal application.Principal, def mcpResourceDef, uri string) (any, error) {
	descriptor, known := s.capabilities.Get(def.capability)
	if !known || !descriptor.ReadOnly {
		return nil, errors.New("capability is not readable as a resource")
	}
	if strings.Contains(def.uri, "{id}") {
		prefix, _, _ := strings.Cut(def.uri, "{id}")
		id, err := mcpTemplateID(uri, prefix)
		if err != nil {
			return nil, err
		}
		property := mcpCapabilitySingleScalarID(descriptor)
		if property == "" {
			return nil, errors.New("capability has no scalar resource template")
		}
		field := descriptorSchemaProperty(descriptor, property)
		value := any(id)
		if field == "integer" {
			parsed, parseErr := strconv.ParseInt(id, 10, 64)
			if parseErr != nil || parsed <= 0 {
				return nil, errors.New("invalid " + property)
			}
			value = parsed
		}
		raw, err := json.Marshal(map[string]any{property: value})
		if err != nil {
			return nil, err
		}
		return s.queryManagementCapability(ctx, principal, def.capability, raw)
	}
	required, err := mcpCapabilityRequiredArguments(descriptor)
	if err != nil {
		return nil, err
	}
	if len(required) == 0 {
		return s.queryManagementCapability(ctx, principal, def.capability, json.RawMessage(`{}`))
	}
	return map[string]any{
		"capability":   descriptor.Name,
		"requires":     required,
		"input_schema": string(descriptor.InputSchema),
		"usage":        "Call the corresponding oboard_capability tool, or use the {id} template when the capability has a single scalar required argument.",
	}, nil
}

func mcpCapabilityRequiredArguments(descriptor capability.Descriptor) ([]string, error) {
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(descriptor.InputSchema, &schema); err != nil {
		return nil, err
	}
	return schema.Required, nil
}

func descriptorSchemaProperty(descriptor capability.Descriptor, property string) string {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(descriptor.InputSchema, &schema) != nil {
		return ""
	}
	var field struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(schema.Properties[property], &field) != nil {
		return ""
	}
	return field.Type
}

// queryMCPCapabilityFallback routes read-only capabilities that application.Query
// does not implement to the existing resource-read implementation. This keeps
// the dynamic MCP tools usable for every capability advertised in tools/list.
func (s *Server) queryMCPCapabilityFallback(ctx context.Context, principal application.Principal, capabilityName string, arguments json.RawMessage) (any, error) {
	descriptor, known := s.capabilities.Get(capabilityName)
	if !known || !descriptor.MCPEnabled || !descriptor.ReadOnly {
		return nil, errors.New("unsupported query capability")
	}
	if def, ok := s.mcpExistingResourceDef(capabilityName, false); ok {
		if !strings.Contains(def.uri, "{id}") {
			return s.readMCPResource(ctx, principal, def, def.uri)
		}
	}
	if def, ok := s.mcpExistingResourceDef(capabilityName, true); ok {
		uri, err := s.mcpResourceURIFromArguments(def, arguments)
		if err != nil {
			return nil, err
		}
		return s.readMCPResource(ctx, principal, def, uri)
	}
	return nil, errors.New("unsupported query capability")
}

func (s *Server) mcpExistingResourceDef(capabilityName string, template bool) (mcpResourceDef, bool) {
	defs := s.mcpResourceDefs()
	if template {
		defs = s.mcpResourceTemplateDefs()
	}
	for _, def := range defs {
		if def.capability == capabilityName {
			return def, true
		}
	}
	return mcpResourceDef{}, false
}

func (s *Server) mcpResourceURIFromArguments(def mcpResourceDef, arguments json.RawMessage) (string, error) {
	descriptor, known := s.capabilities.Get(def.capability)
	if !known {
		return "", errors.New("unknown capability")
	}
	property := mcpCapabilitySingleScalarID(descriptor)
	if property == "" {
		return "", errors.New("capability requires multiple arguments")
	}
	var args map[string]any
	if len(arguments) == 0 {
		return "", fmt.Errorf("missing required argument %s", property)
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return "", errors.New("arguments must be a JSON object")
	}
	raw, ok := args[property]
	if !ok {
		return "", fmt.Errorf("missing required argument %s", property)
	}
	return strings.Replace(def.uri, "{id}", url.PathEscape(fmt.Sprint(raw)), 1), nil
}
