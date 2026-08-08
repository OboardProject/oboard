package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/security"
)

const mcpSchemaVersion = "2"

type mcpAffectedResource struct {
	Type string `json:"type"`
	ID   any    `json:"id"`
}

type mcpErrorBody struct {
	Code            string                `json:"code"`
	Message         string                `json:"message"`
	DeniedBy        string                `json:"denied_by,omitempty"`
	RequiredScope   string                `json:"required_scope,omitempty"`
	RequiredRole    string                `json:"required_role,omitempty"`
	DeniedResources []mcpauth.ResourceRef `json:"denied_resources,omitempty"`
	Recoverable     bool                  `json:"recoverable"`
	NextAction      *mcpNextActionBody    `json:"next_action,omitempty"`
}

type mcpNextActionBody struct {
	Type        string `json:"type"`
	ResourceURI string `json:"resource_uri"`
}

// ToolEnvelope is the unified schema_version=2 envelope for every tool result.
type ToolEnvelope struct {
	SchemaVersion     string                `json:"schema_version"`
	Status            string                `json:"status"`
	WorkflowID        string                `json:"workflow_id,omitempty"`
	ChangesetID       string                `json:"changeset_id,omitempty"`
	OperationID       string                `json:"operation_id,omitempty"`
	AffectedResources []mcpAffectedResource `json:"affected_resources,omitempty"`
	Warnings          []string              `json:"warnings"`
	NextAction        any                   `json:"next_action,omitempty"`
	CorrelationID     string                `json:"correlation_id"`
	Retryable         bool                  `json:"retryable,omitempty"`
	Error             *mcpErrorBody         `json:"error,omitempty"`
	Data              any                   `json:"data,omitempty"`
}

func newToolEnvelope(status, correlationID string, data any) *ToolEnvelope {
	if correlationID == "" {
		random, _ := security.RandomToken(18)
		correlationID = "corr_" + random
	}
	return &ToolEnvelope{SchemaVersion: mcpSchemaVersion, Status: status, Warnings: []string{}, CorrelationID: correlationID, Data: data}
}

// errorEnvelope builds the standard structured failure envelope from an
// authorization decision or a code/message pair.
func errorEnvelope(correlationID string, decision mcpauth.AuthorizationDecision, code, message string) *ToolEnvelope {
	if code == "" {
		code = decision.Code
	}
	if message == "" {
		message = decision.Reason
	}
	if message == "" {
		message = "the operation was denied"
	}
	body := &mcpErrorBody{Code: code, Message: message, Recoverable: decision.Recoverable}
	if decision.RequiredScope != "" {
		body.RequiredScope = decision.RequiredScope
	}
	if decision.RequiredRole != "" {
		body.RequiredRole = decision.RequiredRole
	}
	if len(decision.DeniedResources) > 0 {
		body.DeniedResources = decision.DeniedResources
	}
	if decision.Code == mcpauth.CodeResourceDenied || decision.Code == mcpauth.CodeInsufficientScope || decision.Code == mcpauth.CodeRoleDenied {
		body.NextAction = &mcpNextActionBody{Type: "request_new_consent", ResourceURI: "oboard://auth/grant"}
	}
	return &ToolEnvelope{SchemaVersion: mcpSchemaVersion, Status: "failed", Warnings: []string{}, CorrelationID: correlationID, Error: body}
}

// mcpFailureResult renders an authorization denial as an MCP error tool result.
func mcpFailureResult(decision mcpauth.AuthorizationDecision, correlationID string) *mcp.CallToolResult {
	envelope := errorEnvelope(correlationID, decision, "", "")
	encoded, _ := json.Marshal(envelope)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
}

// mcpPlainFailureResult renders a plain error without leaking internals.
func mcpPlainFailureResult(correlationID, message string) *mcp.CallToolResult {
	envelope := &ToolEnvelope{SchemaVersion: mcpSchemaVersion, Status: "failed", Warnings: []string{}, CorrelationID: correlationID, Error: &mcpErrorBody{Code: "invalid_input", Message: message}}
	encoded, _ := json.Marshal(envelope)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
}

// ResourceEnvelope is the unified schema_version=2 resource body.
type ResourceEnvelope struct {
	SchemaVersion string                `json:"schema_version"`
	ResourceURI   string                `json:"resource_uri"`
	Revision      string                `json:"revision"`
	GeneratedAt   string                `json:"generated_at"`
	Authorization *AuthorizationSummary `json:"authorization"`
	Data          json.RawMessage       `json:"data"`
	Warnings      []string              `json:"warnings"`
}

type AuthorizationSummary struct {
	GrantID         string `json:"grant_id"`
	AccessLevel     string `json:"access_level"`
	BoundaryApplied bool   `json:"boundary_applied"`
	PolicyVersion   int    `json:"policy_version"`
}

// resourceRevision computes a stable revision from normalized data. generated_at
// and the authorization summary never participate in the revision hash.
func resourceRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return "rev_" + hex.EncodeToString(sum[:12])
}

func newResourceEnvelope(uri string, data []byte, summary *AuthorizationSummary, warnings []string) ResourceEnvelope {
	if warnings == nil {
		warnings = []string{}
	}
	return ResourceEnvelope{
		SchemaVersion: mcpSchemaVersion, ResourceURI: uri, Revision: resourceRevision(data),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Authorization: summary,
		Data: json.RawMessage(data), Warnings: warnings,
	}
}
