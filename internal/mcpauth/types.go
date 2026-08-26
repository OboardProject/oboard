package mcpauth

import (
	"context"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// AccessLevel is the only business access level granted to MCP clients. There
// are exactly two levels plus the OAuth offline_access token-lifetime scope.
type AccessLevel string

const (
	AccessRead    AccessLevel = "read"
	AccessOperate AccessLevel = "operate"
)

// Scope constants are the coarse OAuth scopes exposed to MCP clients.
const (
	ScopeRead      = "oboard:read"
	ScopeOperate   = "oboard:operate"
	ScopeOffline   = "offline_access"
	ScopeSeparator = ' '
)

// GrantPolicy is the effective authorization snapshot derived from the active
// OAuthGrant at request time. It is the single source of truth for MCP
// authorization; tokens, codes, and API principals never carry business scopes.
type GrantPolicy struct {
	GrantID          string
	ClientID         string
	UserID           string
	PrincipalID      string
	AccessLevel      AccessLevel
	ResourceBoundary ResourceBoundary
	ApprovalProfile  string
	ApprovalMaxRisk  int
	OfflineAccess    bool
	PolicyVersion    int
	RoleVersion      int
	ConsentVersion   int
	IssuedAt         time.Time
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
}

// ResourceRef identifies one business resource referenced by an operation or
// resource read. Types map into the grant's ResourceBoundary.
type ResourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// AuthorizationDecision is the unified outcome returned by the Evaluator for
// resource reads, tool calls, and Changeset operations.
type AuthorizationDecision struct {
	Allowed         bool          `json:"allowed"`
	Code            string        `json:"code"`
	Reason          string        `json:"reason"`
	RequiredScope   string        `json:"required_scope,omitempty"`
	RequiredRole    string        `json:"required_role,omitempty"`
	DeniedResources []ResourceRef `json:"denied_resources,omitempty"`
	ApprovalMode    string        `json:"approval_mode,omitempty"`
	RiskClass       int           `json:"risk_class,omitempty"`
	Recoverable     bool          `json:"recoverable"`
	CorrelationID   string        `json:"correlation_id"`
}

// CapabilitySpec is the MCP-facing slice of a capability descriptor used by
// the unified Evaluator. The controller builds it from the capability catalog.
type CapabilitySpec struct {
	ID                  string
	Title               string
	Description         string
	MinimumAccess       AccessLevel
	RBACPermission      string
	MCPEnabled          bool
	Executable          bool
	ReadOnly            bool
	Idempotent          bool
	RiskClass           int
	ApprovalRequired    bool
	ResourceTypes       []string
	DataClassification  string
	ResolveResourceRefs func(ctx context.Context, input any) ([]ResourceRef, error)
	PrivilegeClass      string
	ApprovalPolicy      string
}

// PrivilegedGrantPolicy is the extra host-execution authorization bound to an
// OAuth grant. It is never encoded as an OAuth scope.
type PrivilegedGrantPolicy struct {
	ID               int64
	OAuthGrantID     string
	Capabilities     []string
	ResourceBoundary ResourceBoundary
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
	Revision         int64
}

func (p *PrivilegedGrantPolicy) Active(now time.Time) bool {
	if p == nil {
		return false
	}
	if p.RevokedAt != nil && !p.RevokedAt.IsZero() {
		return false
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(now) {
		return false
	}
	return true
}

func (p *PrivilegedGrantPolicy) HasCapability(name string) bool {
	if p == nil {
		return false
	}
	for _, item := range p.Capabilities {
		if item == name {
			return true
		}
	}
	return false
}

// GrantPrincipal is the caller identity evaluated by the Evaluator.
type GrantPrincipal struct {
	Grant           GrantPolicy
	Role            model.Role
	UserID          int64
	ClientID        string
	PrivilegedGrant *PrivilegedGrantPolicy
}
