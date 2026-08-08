package model

import (
	"encoding/json"
	"time"
)

type APIPrincipalType string

const (
	APIPrincipalServiceAccount APIPrincipalType = "service_account"
	APIPrincipalOAuth          APIPrincipalType = "oauth"
	APIPrincipalInternalAI     APIPrincipalType = "internal_ai"
)

type APIPrincipal struct {
	ID                 string           `json:"id"`
	OAuthGrantID       string           `json:"-"`
	OwnerUserID        *int64           `json:"owner_user_id,omitempty"`
	Name               string           `json:"name"`
	Type               APIPrincipalType `json:"type"`
	Enabled            bool             `json:"enabled"`
	Scopes             []string         `json:"scopes"`
	ResourceFilter     json.RawMessage  `json:"resource_filter"`
	AllowedCIDRs       []string         `json:"allowed_cidrs"`
	RateLimitPerMinute int              `json:"rate_limit_per_minute"`
	MaxConcurrency     int              `json:"max_concurrency"`
	ExpiresAt          *time.Time       `json:"expires_at,omitempty"`
	LastUsedAt         *time.Time       `json:"last_used_at,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

type APIToken struct {
	ID          string     `json:"id"`
	PrincipalID string     `json:"principal_id"`
	TokenHash   string     `json:"-"`
	Prefix      string     `json:"prefix"`
	ExpiresAt   time.Time  `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type ApprovalMode string

const (
	ApprovalDenied    ApprovalMode = "denied"
	ApprovalRequired  ApprovalMode = "required"
	ApprovalAutomatic ApprovalMode = "automatic"
)

type ApprovalPolicy struct {
	ID             string          `json:"id"`
	PrincipalID    string          `json:"principal_id"`
	Capability     string          `json:"capability"`
	ResourceFilter json.RawMessage `json:"resource_filter"`
	Mode           ApprovalMode    `json:"mode"`
	AllowRisk4     bool            `json:"allow_risk4"`
	ExpiresAt      *time.Time      `json:"expires_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type ChangesetStatus string

const (
	ChangesetDraft            ChangesetStatus = "draft"
	ChangesetValidated        ChangesetStatus = "validated"
	ChangesetAwaitingApproval ChangesetStatus = "awaiting_approval"
	ChangesetApproved         ChangesetStatus = "approved"
	ChangesetApplying         ChangesetStatus = "applying"
	ChangesetSucceeded        ChangesetStatus = "succeeded"
	ChangesetFailed           ChangesetStatus = "failed"
	ChangesetExpired          ChangesetStatus = "expired"
	ChangesetSuperseded       ChangesetStatus = "superseded"
	ChangesetRollbackFailed   ChangesetStatus = "rollback_failed"
)

type AutomationOperation struct {
	ID           string          `json:"id"`
	ChangesetID  string          `json:"changeset_id"`
	Position     int             `json:"position"`
	Capability   string          `json:"capability"`
	Input        json.RawMessage `json:"input"`
	SecretRefs   []string        `json:"secret_refs,omitempty"`
	ResourceRefs json.RawMessage `json:"resource_refs"`
	RiskClass    int             `json:"risk_class"`
	Status       string          `json:"status"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

type AutomationChangeset struct {
	ID             string                `json:"id"`
	PrincipalID    string                `json:"principal_id"`
	ActorUserID    *int64                `json:"actor_user_id,omitempty"`
	Status         ChangesetStatus       `json:"status"`
	Reason         string                `json:"reason"`
	IdempotencyKey string                `json:"idempotency_key"`
	BaseRevisions  json.RawMessage       `json:"base_revisions"`
	PlanHash       string                `json:"plan_hash"`
	RiskClass      int                   `json:"risk_class"`
	AutoApply      bool                  `json:"auto_apply"`
	Validation     json.RawMessage       `json:"validation"`
	BlastRadius    json.RawMessage       `json:"blast_radius"`
	Result         json.RawMessage       `json:"result,omitempty"`
	ExpiresAt      time.Time             `json:"expires_at"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	ValidatedAt    *time.Time            `json:"validated_at,omitempty"`
	ApprovedAt     *time.Time            `json:"approved_at,omitempty"`
	AppliedAt      *time.Time            `json:"applied_at,omitempty"`
	CompletedAt    *time.Time            `json:"completed_at,omitempty"`
	Operations     []AutomationOperation `json:"operations"`
}

type AutomationApproval struct {
	ID           string    `json:"id"`
	ChangesetID  string    `json:"changeset_id"`
	ApproverID   int64     `json:"approver_id"`
	Decision     string    `json:"decision"`
	PlanHash     string    `json:"plan_hash"`
	Comment      string    `json:"comment,omitempty"`
	ApprovedRisk int       `json:"approved_risk"`
	CreatedAt    time.Time `json:"created_at"`
}

type WorkflowStatus string

const (
	WorkflowPlanning               WorkflowStatus = "planning"
	WorkflowExternalActionRequired WorkflowStatus = "external_action_required"
	WorkflowWaitingForAgent        WorkflowStatus = "waiting_for_agent"
	WorkflowApprovalRequired       WorkflowStatus = "approval_required"
	WorkflowQueued                 WorkflowStatus = "queued"
	WorkflowRunning                WorkflowStatus = "running"
	WorkflowSucceeded              WorkflowStatus = "succeeded"
	WorkflowPartiallySucceeded     WorkflowStatus = "partially_succeeded"
	WorkflowFailed                 WorkflowStatus = "failed"
	WorkflowCancelled              WorkflowStatus = "cancelled"
)

type AutomationWorkflow struct {
	ID                string                   `json:"id"`
	PrincipalID       string                   `json:"principal_id"`
	GrantID           string                   `json:"grant_id,omitempty"`
	Kind              string                   `json:"kind"`
	Status            WorkflowStatus           `json:"status"`
	Reason            string                   `json:"reason"`
	IdempotencyKey    string                   `json:"idempotency_key"`
	ChangesetID       string                   `json:"changeset_id,omitempty"`
	CurrentStep       string                   `json:"current_step,omitempty"`
	CorrelationID     string                   `json:"correlation_id"`
	AffectedResources json.RawMessage          `json:"affected_resources"`
	NextAction        json.RawMessage          `json:"next_action,omitempty"`
	ErrorCode         string                   `json:"error_code,omitempty"`
	ErrorMessage      string                   `json:"error_message,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	CompletedAt       *time.Time               `json:"completed_at,omitempty"`
	Steps             []AutomationWorkflowStep `json:"steps"`
}

type AutomationWorkflowStep struct {
	ID             string          `json:"id"`
	WorkflowID     string          `json:"workflow_id"`
	Position       int             `json:"position"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Attempt        int             `json:"attempt"`
	IdempotencyKey string          `json:"idempotency_key"`
	InputDigest    string          `json:"input_digest"`
	OutputDigest   string          `json:"output_digest,omitempty"`
	Retryable      bool            `json:"retryable"`
	NextAction     json.RawMessage `json:"next_action,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	CorrelationID  string          `json:"correlation_id"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type ToolCallAudit struct {
	ID                 string          `json:"id"`
	PrincipalID        string          `json:"principal_id"`
	ClientName         string          `json:"client_name"`
	ModelProvider      string          `json:"model_provider,omitempty"`
	Capability         string          `json:"capability"`
	Scope              string          `json:"scope,omitempty"`
	DataClassification string          `json:"data_classification"`
	AffectedResources  json.RawMessage `json:"affected_resources"`
	ApprovalID         string          `json:"approval_id,omitempty"`
	RequestID          string          `json:"request_id"`
	ArgumentsHash      string          `json:"arguments_hash"`
	Result             string          `json:"result"`
	SourceIP           string          `json:"source_ip"`
	CreatedAt          time.Time       `json:"created_at"`
}

type OAuthClient struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	RedirectURIs      []string        `json:"redirect_uris"`
	IdentityType      string          `json:"identity_type"`
	MetadataURI       string          `json:"metadata_uri,omitempty"`
	MetadataHash      string          `json:"metadata_hash,omitempty"`
	MetadataETag      string          `json:"metadata_etag,omitempty"`
	MetadataFetchedAt *time.Time      `json:"metadata_fetched_at,omitempty"`
	ClientMetadata    json.RawMessage `json:"client_metadata"`
	Enabled           bool            `json:"enabled"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type OAuthApprovalProfile struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	AutoApproveRisk int       `json:"auto_approve_risk"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type OAuthGrantStatus string

const (
	OAuthGrantActive         OAuthGrantStatus = "active"
	OAuthGrantNeedsReconsent OAuthGrantStatus = "needs_reconsent"
	OAuthGrantRevoked        OAuthGrantStatus = "revoked"
)

type OAuthGrant struct {
	ID          string `json:"id"`
	ClientID    string `json:"client_id"`
	ClientName  string `json:"client_name,omitempty"`
	UserID      int64  `json:"user_id"`
	Username    string `json:"username,omitempty"`
	PrincipalID string `json:"principal_id"`
	// AccessLevel is the coarse MCP access level (read or operate). It is the
	// single authorization source for MCP.
	AccessLevel string `json:"access_level"`
	// ResourceBoundaryJSON is the versioned ResourceBoundary. json.RawMessage
	// keeps it an object on the wire so management UIs can render the boundary.
	ResourceBoundaryJSON json.RawMessage       `json:"resource_boundary"`
	ApprovalProfileID    string                `json:"approval_profile_id"`
	ApprovalProfile      *OAuthApprovalProfile `json:"approval_profile,omitempty"`
	OfflineAccess        bool                  `json:"offline_access"`
	PolicyVersion        int                   `json:"policy_version"`
	RoleVersion          int                   `json:"role_version"`
	ConsentVersion       int                   `json:"consent_version"`
	Status               OAuthGrantStatus      `json:"status"`
	CreatedAt            time.Time             `json:"created_at"`
	ExpiresAt            *time.Time            `json:"expires_at,omitempty"`
	LastUsedAt           *time.Time            `json:"last_used_at,omitempty"`
	RevokedAt            *time.Time            `json:"revoked_at,omitempty"`
	RevokeReason         string                `json:"revoke_reason,omitempty"`
}

type OAuthAuthorizationCode struct {
	CodeHash      string
	GrantID       string
	ClientID      string
	UserID        int64
	PrincipalID   string
	RedirectURI   string
	Resource      string
	CodeChallenge string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type OAuthToken struct {
	TokenHash       string
	FamilyID        string
	GrantID         string
	ParentTokenHash string
	PrincipalID     string
	ClientID        string
	UserID          int64
	Resource        string
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
	RevokedAt       *time.Time
	ReuseDetectedAt *time.Time
	CreatedAt       time.Time
}

type AIProvider struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	BaseURL             string                `json:"base_url"`
	Model               string                `json:"model"`
	APIFormat           string                `json:"api_format"`
	Enabled             bool                  `json:"enabled"`
	AllowRawAudit       bool                  `json:"allow_raw_audit"`
	DailyTokenLimit     int64                 `json:"daily_token_limit"`
	Capability          *AIProviderCapability `json:"capability,omitempty"`
	HasCredential       bool                  `json:"has_credential"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
	LastUsedAt          *time.Time            `json:"last_used_at,omitempty"`
	CredentialEncrypted string                `json:"-"`
}

type AuditFeatureSnapshot struct {
	ID              string          `json:"id"`
	UserID          int64           `json:"user_id"`
	Window          string          `json:"window"`
	WindowStartedAt time.Time       `json:"window_started_at"`
	WindowEndedAt   time.Time       `json:"window_ended_at"`
	FeatureVersion  int             `json:"feature_version"`
	RuleScore       int             `json:"rule_score"`
	AnomalyScore    *int            `json:"anomaly_score,omitempty"`
	Features        json.RawMessage `json:"features"`
	Fingerprint     string          `json:"fingerprint"`
	CreatedAt       time.Time       `json:"created_at"`
}

type AuditIncident struct {
	ID               string     `json:"id"`
	UserID           int64      `json:"user_id"`
	Status           string     `json:"status"`
	Classification   string     `json:"classification,omitempty"`
	Severity         string     `json:"severity"`
	RuleScore        int        `json:"rule_score"`
	AnomalyScore     *int       `json:"anomaly_score,omitempty"`
	Fingerprint      string     `json:"fingerprint"`
	LatestSnapshotID string     `json:"latest_snapshot_id"`
	OpenedAt         time.Time  `json:"opened_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
}

type OperatorFeedback struct {
	ID          string    `json:"id"`
	IncidentID  string    `json:"incident_id"`
	ActorUserID int64     `json:"actor_user_id"`
	Label       string    `json:"label"`
	Comment     string    `json:"comment,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
