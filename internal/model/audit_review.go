package model

import (
	"encoding/json"
	"time"
)

const (
	AuditReviewEvidenceSubscription = "subscription"
	AuditReviewEvidenceConnection   = "connection"
	AuditReviewEvidenceDestination  = "destination"
)

type AuditReviewSelector struct {
	Mode string  `json:"mode"`
	IDs  []int64 `json:"ids"`
}

type AuditReviewScope struct {
	Users   AuditReviewSelector `json:"users"`
	Servers AuditReviewSelector `json:"servers"`
}

type AuditReview struct {
	ID                string           `json:"id"`
	RequestID         string           `json:"request_id"`
	ProviderID        string           `json:"provider_id"`
	RequestedBy       int64            `json:"requested_by"`
	Status            string           `json:"status"`
	Scope             AuditReviewScope `json:"scope"`
	EvidenceTypes     []string         `json:"evidence_types"`
	WindowStartedAt   time.Time        `json:"window_started_at"`
	WindowEndedAt     time.Time        `json:"window_ended_at"`
	SnapshotAt        time.Time        `json:"snapshot_at"`
	PrivacyMode       string           `json:"privacy_mode"`
	ResolvedUserIDs   []int64          `json:"resolved_user_ids"`
	ResolvedServerIDs []int64          `json:"resolved_server_ids"`
	JobCount          int              `json:"job_count"`
	CompletedJobCount int              `json:"completed_job_count"`
	InputTokens       int64            `json:"input_tokens"`
	OutputTokens      int64            `json:"output_tokens"`
	FinalOutput       json.RawMessage  `json:"final_output,omitempty"`
	Error             string           `json:"error,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	CompletedAt       *time.Time       `json:"completed_at,omitempty"`
}

type AuditReviewEvidence struct {
	Ref       string          `json:"ref"`
	ReviewID  string          `json:"review_id"`
	Kind      string          `json:"kind"`
	UserID    *int64          `json:"user_id,omitempty"`
	ServerID  *int64          `json:"server_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type AuditReviewJob struct {
	ID           string          `json:"id"`
	ReviewID     string          `json:"review_id"`
	ProviderID   string          `json:"provider_id"`
	Stage        int             `json:"stage"`
	Position     int             `json:"position"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	Error        string          `json:"error,omitempty"`
	ErrorDetail  json.RawMessage `json:"error_detail,omitempty"`
	Attempts     int             `json:"attempts"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
	LeaseOwner   string          `json:"-"`
	LeaseUntil   *time.Time      `json:"-"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

type AuditReviewDimensionFinding struct {
	Kind            string   `json:"kind"`
	RiskLevel       string   `json:"risk_level"`
	Summary         string   `json:"summary"`
	EvidenceRefs    []string `json:"evidence_refs"`
	CounterEvidence []string `json:"counter_evidence"`
}

type AuditReviewSubjectFinding struct {
	SubjectRef   string   `json:"subject_ref"`
	RiskLevel    string   `json:"risk_level"`
	Summary      string   `json:"summary"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type AuditReviewReport struct {
	Verdict            string                        `json:"verdict"`
	RiskLevel          string                        `json:"risk_level"`
	Confidence         float64                       `json:"confidence"`
	Summary            string                        `json:"summary"`
	Dimensions         []AuditReviewDimensionFinding `json:"dimensions"`
	NotableSubjects    []AuditReviewSubjectFinding   `json:"notable_subjects"`
	RecommendedActions []string                      `json:"recommended_actions"`
	DataGaps           []string                      `json:"data_gaps"`
	CoverageSummary    string                        `json:"coverage_summary"`
}

type AuditReviewData struct {
	Users []AuditReviewUserData `json:"users"`
}

type AuditReviewUserData struct {
	UserID                 int64                        `json:"user_id"`
	SubscriptionPulls      int64                        `json:"subscription_pulls"`
	SubscriptionSuccessful int64                        `json:"subscription_successful"`
	SubscriptionDenied     int64                        `json:"subscription_denied"`
	SubscriptionSourceIPs  int                          `json:"subscription_source_ips"`
	SubscriptionRegions    int                          `json:"subscription_regions"`
	SubscriptionClients    int                          `json:"subscription_clients"`
	SubscriptionFormats    int                          `json:"subscription_formats"`
	SubscriptionLastSeenAt *time.Time                   `json:"subscription_last_seen_at,omitempty"`
	RecentSubscriptions    []SubscriptionPullAudit      `json:"recent_subscriptions"`
	ConnectionCount        int64                        `json:"connection_count"`
	ConnectionClosed       int64                        `json:"connection_closed"`
	ConnectionActivePeak   int64                        `json:"connection_active_peak"`
	ConnectionActiveAtEnd  int64                        `json:"connection_active_at_end"`
	ConnectionSourceIPs    int                          `json:"connection_source_ips"`
	ConnectionServers      int                          `json:"connection_servers"`
	ConnectionDestinations int                          `json:"connection_destinations"`
	ConnectionDropped      int64                        `json:"connection_dropped_buckets"`
	ConnectionLastSeenAt   *time.Time                   `json:"connection_last_seen_at,omitempty"`
	ServerBreakdown        []AuditReviewServerBreakdown `json:"server_breakdown"`
	RecentConnections      []ConnectionAuditReport      `json:"recent_connections"`
	Destinations           []AuditReviewDestination     `json:"destinations"`
}

type AuditReviewServerBreakdown struct {
	ServerID        int64     `json:"server_id"`
	ConnectionCount int64     `json:"connection_count"`
	ActivePeak      int64     `json:"active_peak"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

type AuditReviewDestination struct {
	Destination     string    `json:"destination"`
	Port            int       `json:"port"`
	Network         string    `json:"network"`
	ConnectionCount int64     `json:"connection_count"`
	ServerCount     int       `json:"server_count"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}
