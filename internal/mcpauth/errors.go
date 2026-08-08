package mcpauth

// MCPError is the structured business error body carried inside the
// schema_version=2 error envelope for MCP tool and resource failures.
type MCPError struct {
	SchemaVersion string      `json:"schema_version"`
	Status        string      `json:"status"`
	Error         ErrorDetail `json:"error"`
	CorrelationID string      `json:"correlation_id"`
	Warnings      []string    `json:"warnings"`
}

type ErrorDetail struct {
	Code            string        `json:"code"`
	Message         string        `json:"message"`
	DeniedBy        string        `json:"denied_by"`
	RequiredScope   string        `json:"required_scope,omitempty"`
	RequiredRole    string        `json:"required_role,omitempty"`
	DeniedResources []ResourceRef `json:"denied_resources,omitempty"`
	Recoverable     bool          `json:"recoverable"`
	NextAction      *NextAction   `json:"next_action,omitempty"`
}

type NextAction struct {
	Type        string `json:"type"`
	ResourceURI string `json:"resource_uri"`
}

// NewMCPError builds the standard structured error body from an
// AuthorizationDecision or a plain code/message pair.
func NewMCPError(code, message, deniedBy string, decision AuthorizationDecision, correlationID string) MCPError {
	detail := ErrorDetail{Code: code, Message: message, DeniedBy: deniedBy, Recoverable: decision.Recoverable}
	if decision.RequiredScope != "" {
		detail.RequiredScope = decision.RequiredScope
	}
	if decision.RequiredRole != "" {
		detail.RequiredRole = decision.RequiredRole
	}
	if len(decision.DeniedResources) > 0 {
		detail.DeniedResources = decision.DeniedResources
	}
	if decision.Code == CodeResourceDenied || decision.Code == CodeInsufficientScope || decision.Code == CodeRoleDenied {
		detail.NextAction = &NextAction{Type: "request_new_consent", ResourceURI: "oboard://auth/grant"}
	}
	if code == "" {
		code = decision.Code
	}
	if deniedBy == "" {
		deniedBy = "evaluator"
	}
	return MCPError{SchemaVersion: "2", Status: "failed", Error: detail, CorrelationID: correlationID, Warnings: []string{}}
}

// NotAllowedError maps an authorization decision to a structured MCP error.
func NotAllowedError(decision AuthorizationDecision, correlationID string) MCPError {
	code := decision.Code
	if code == "" || code == "allowed" {
		code = CodeResourceDenied
	}
	return NewMCPError(code, decision.Reason, "evaluator", decision, correlationID)
}
