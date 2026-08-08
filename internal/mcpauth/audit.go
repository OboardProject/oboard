package mcpauth

import "time"

// AuditEvent is a structured authorization audit record. It deliberately
// carries only identifiers, codes, and digests — never tokens, codes, PKCE
// verifiers, passwords, private keys, enrollment secrets, or full sensitive
// tool results.
type AuditEvent struct {
	CorrelationID string        `json:"correlation_id"`
	GrantID       string        `json:"grant_id"`
	ClientID      string        `json:"client_id"`
	UserID        string        `json:"user_id"`
	PrincipalID   string        `json:"principal_id"`
	AccessLevel   AccessLevel   `json:"access_level"`
	CapabilityID  string        `json:"capability_id"`
	ResourceRefs  []ResourceRef `json:"resource_refs"`
	DecisionCode  string        `json:"decision_code"`
	RiskClass     int           `json:"risk_class"`
	ApprovalMode  string        `json:"approval_mode"`
	RequestDigest string        `json:"request_digest"`
	ResultDigest  string        `json:"result_digest,omitempty"`
	Action        string        `json:"action"`
	SourceIP      string        `json:"source_ip"`
	UserAgent     string        `json:"user_agent"`
	Timestamp     time.Time     `json:"timestamp"`
}

// AuditEventBuilder accumulates fields and emits one AuditEvent. Request and
// result bodies are reduced to digests so secrets never reach the audit row.
type AuditEventBuilder struct {
	event AuditEvent
}

func NewAuditEventBuilder(action, correlationID string) *AuditEventBuilder {
	return &AuditEventBuilder{event: AuditEvent{Action: action, CorrelationID: correlationID, Timestamp: time.Now().UTC()}}
}

func (b *AuditEventBuilder) Grant(grant GrantPolicy) *AuditEventBuilder {
	b.event.GrantID = grant.GrantID
	b.event.ClientID = grant.ClientID
	b.event.PrincipalID = grant.PrincipalID
	b.event.AccessLevel = grant.AccessLevel
	return b
}

func (b *AuditEventBuilder) Identity(userID, principalID string) *AuditEventBuilder {
	b.event.UserID = userID
	if principalID != "" {
		b.event.PrincipalID = principalID
	}
	return b
}

func (b *AuditEventBuilder) Capability(id string, decision AuthorizationDecision) *AuditEventBuilder {
	b.event.CapabilityID = id
	b.event.DecisionCode = decision.Code
	b.event.RiskClass = decision.RiskClass
	b.event.ApprovalMode = decision.ApprovalMode
	b.event.ResourceRefs = decision.DeniedResources
	return b
}

func (b *AuditEventBuilder) Source(sourceIP, userAgent string) *AuditEventBuilder {
	b.event.SourceIP = sourceIP
	b.event.UserAgent = userAgent
	return b
}

func (b *AuditEventBuilder) RequestDigest(digest string) *AuditEventBuilder {
	b.event.RequestDigest = digest
	return b
}

func (b *AuditEventBuilder) ResultDigest(digest string) *AuditEventBuilder {
	b.event.ResultDigest = digest
	return b
}

func (b *AuditEventBuilder) Build() AuditEvent { return b.event }
