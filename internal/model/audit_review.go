package model

import (
	"encoding/json"
	"time"
)

const (
	AuditReviewEvidenceSubscription = "subscription"
	AuditReviewEvidenceConnection   = "connection"
	AuditReviewEvidenceDestination  = "destination"

	AuditEvidenceSchemaVersion        = "audit-evidence-v2"
	AuditUserFindingSchemaVersion     = "audit-user-finding-v1"
	AuditReportSchemaVersion          = "audit-report-v2"
	AuditProviderProfileVersion       = "provider-profile-v2"
	AuditScoringVersion               = "deterministic-v2"
	AuditBaselineVersion              = "feature-snapshot-v1"
	AuditPromptFindingVersion         = "audit-finding-v1"
	AuditPromptReportVersion          = "audit-report-v2"
	AuditProviderGradeA               = "A"
	AuditProviderGradeB               = "B"
	AuditProviderGradeC               = "C"
	AuditProviderGradeUnusable        = "unusable"
	AuditProviderStructuredJSONSchema = "json_schema"
	AuditProviderStructuredJSONObject = "json_object"
	AuditProviderStructuredNone       = "none"
	AuditOutputModeStrictSchema       = "strict_schema"
	AuditOutputModeJSONObject         = "json_object"
	AuditOutputModeText               = "text"
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
	Route        json.RawMessage `json:"route,omitempty"`
	LeaseOwner   string          `json:"-"`
	LeaseUntil   *time.Time      `json:"-"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

// AIProviderCapability records the outcome of the audit readiness test for a
// provider/model pair. It is the single source for deciding which output mode a
// formal audit job may use; grades are A (strict schema), B (JSON object with
// local validation and one repair), C (text only, excluded from formal audits)
// and unusable (could not return a stable usable result).
type AIProviderCapability struct {
	ProviderProfileVersion     string    `json:"provider_profile_version"`
	ProviderID                 string    `json:"provider_id"`
	EndpointID                 string    `json:"endpoint_id"`
	APIStyle                   string    `json:"api_style"`
	Model                      string    `json:"model"`
	ConfigDigest               string    `json:"config_digest"`
	TestedAt                   time.Time `json:"tested_at"`
	ConnectivityOK             bool      `json:"connectivity_ok"`
	AuthenticationOK           bool      `json:"authentication_ok"`
	ModelsSupported            bool      `json:"models_supported"`
	AuditGrade                 string    `json:"audit_grade"`
	StructuredOutput           string    `json:"structured_output"`
	OutputMode                 string    `json:"output_mode"`
	SchemaSuccessRate          float64   `json:"schema_success_rate"`
	UsageSupported             bool      `json:"usage_supported"`
	FinishReasonSupported      bool      `json:"finish_reason_supported"`
	StreamingSupported         bool      `json:"streaming_supported"`
	ProviderRequestIDSupported bool      `json:"provider_request_id_supported"`
	MaxVerifiedOutputTokens    int       `json:"max_verified_output_tokens"`
	LatencyMS                  int64     `json:"latency_ms"`
	Notes                      []string  `json:"notes,omitempty"`
	Note                       string    `json:"note,omitempty"`
}

// AuditEvidencePack is the immutable, versioned deterministic input handed to
// the AI. Scores, confidence, signals, counter-evidence, data quality and every
// feature carry field-level evidence IDs so the AI can cite the exact fact that
// supports a conclusion and a validator can verify every reference.
type AuditEvidencePack struct {
	SchemaVersion   string                      `json:"schema_version"`
	Mode            string                      `json:"mode"`
	Subject         AuditEvidenceSubject        `json:"subject"`
	Window          AuditEvidenceWindow         `json:"window"`
	DataQuality     AuditEvidenceQuality        `json:"data_quality"`
	Scores          AuditEvidenceScores         `json:"scores"`
	Features        []AuditEvidenceFeature      `json:"features"`
	Signals         []AuditEvidenceSignal       `json:"signals"`
	CounterEvidence []AuditEvidenceCounter      `json:"counter_evidence"`
	Timeline        []AuditEvidenceTimelineItem `json:"timeline"`
	DataGaps        []string                    `json:"data_gaps"`
	Methodology     AuditEvidenceMethodology    `json:"methodology"`
}

type AuditEvidenceSubject struct {
	Ref           string `json:"ref"`
	IdentityMode  string `json:"identity_mode"`
	PolicyProfile string `json:"policy_profile"`
	Status        string `json:"status"`
	Role          string `json:"role"`
}

type AuditEvidenceWindow struct {
	Current     string   `json:"current"`
	Comparisons []string `json:"comparisons"`
}

type AuditEvidenceQuality struct {
	Coverage         float64 `json:"coverage"`
	BaselineDays     int     `json:"baseline_days"`
	DroppedBuckets   int64   `json:"dropped_buckets"`
	IdentityQuality  float64 `json:"identity_quality"`
	DataCompleteness float64 `json:"data_completeness"`
}

type AuditEvidenceScores struct {
	ConnectionRisk     int               `json:"connection_risk"`
	SubscriptionRisk   int               `json:"subscription_risk"`
	OverallRisk        int               `json:"overall_risk"`
	Health             int               `json:"health"`
	EvidenceConfidence float64           `json:"evidence_confidence"`
	Caps               AuditEvidenceCaps `json:"caps"`
}

type AuditEvidenceCaps struct {
	Anomaly     float64 `json:"anomaly"`
	DeviceClone float64 `json:"device_clone"`
	Normal      float64 `json:"normal"`
	HighRisk    float64 `json:"high_risk"`
}

type AuditEvidenceFeature struct {
	EvidenceID     string   `json:"evidence_id"`
	Metric         string   `json:"metric"`
	Value          float64  `json:"value"`
	Unit           string   `json:"unit"`
	Window         string   `json:"window"`
	BaselineMedian *float64 `json:"baseline_median,omitempty"`
	BaselineP95    *float64 `json:"baseline_p95,omitempty"`
	BaselineMAD    *float64 `json:"baseline_mad,omitempty"`
	DeltaPercent   *float64 `json:"delta_percent,omitempty"`
	Threshold      *float64 `json:"threshold,omitempty"`
	SampleCount    int      `json:"sample_count"`
	Quality        float64  `json:"quality"`
	Severity       string   `json:"severity"`
	Source         string   `json:"source"`
	Category       string   `json:"category"`
}

type AuditEvidenceSignal struct {
	SignalID            string   `json:"signal_id"`
	Kind                string   `json:"kind"`
	Severity            string   `json:"severity"`
	ObservedAt          string   `json:"observed_at,omitempty"`
	DurationSeconds     int      `json:"duration_seconds,omitempty"`
	EvidenceRefs        []string `json:"evidence_refs"`
	CounterEvidenceRefs []string `json:"counter_evidence_refs"`
	Confidence          float64  `json:"confidence"`
	Text                string   `json:"text"`
}

type AuditEvidenceCounter struct {
	Ref   string `json:"ref"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Scope string `json:"scope"`
}

type AuditEvidenceTimelineItem struct {
	EvidenceID string `json:"evidence_id"`
	Kind       string `json:"kind"`
	StartedAt  string `json:"started_at,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
	Score      int    `json:"score"`
	Detail     string `json:"detail,omitempty"`
}

type AuditEvidenceMethodology struct {
	FeatureVersion         int    `json:"feature_version"`
	ScoringVersion         string `json:"scoring_version"`
	BaselineVersion        string `json:"baseline_version"`
	EvidenceSchemaVersion  string `json:"evidence_schema_version"`
	PromptVersion          string `json:"prompt_version"`
	ReportSchemaVersion    string `json:"report_schema_version"`
	ProviderProfileVersion string `json:"provider_profile_version"`
}

// AuditUserFinding is the stage-0 output: structured per-user findings with
// field-level evidence references, produced from one evidence pack.
type AuditUserFinding struct {
	SchemaVersion   string               `json:"schema_version"`
	SubjectRef      string               `json:"subject_ref"`
	BehaviorProfile AuditBehaviorProfile `json:"behavior_profile"`
	Findings        []AuditReportFinding `json:"findings"`
	CounterEvidence []string             `json:"counter_evidence"`
	DataGaps        []string             `json:"data_gaps"`
}

// AuditReviewReport is the final stage-1 report. Scores, confidence, data
// quality and methodology are authoritative engine values; the validator
// rejects reports that modify or recompute them.
type AuditReviewReport struct {
	SchemaVersion      string                    `json:"schema_version"`
	Executive          AuditReportExecutive      `json:"executive"`
	BehaviorProfile    AuditBehaviorProfile      `json:"behavior_profile"`
	Findings           []AuditReportFinding      `json:"findings"`
	Timeline           []AuditReportTimelineItem `json:"timeline"`
	CounterEvidence    []AuditReportCounter      `json:"counter_evidence"`
	RecommendedActions []AuditReportAction       `json:"recommended_actions"`
	DataQuality        AuditReportDataQuality    `json:"data_quality"`
	DataGaps           []string                  `json:"data_gaps"`
	Methodology        AuditReportMethodology    `json:"methodology"`
}

type AuditReportExecutive struct {
	Verdict            string  `json:"verdict"`
	RiskScore          int     `json:"risk_score"`
	HealthScore        int     `json:"health_score"`
	EvidenceConfidence float64 `json:"evidence_confidence"`
	OneLineConclusion  string  `json:"one_line_conclusion"`
}

type AuditBehaviorProfile struct {
	UsualPattern   []string `json:"usual_pattern"`
	CurrentPattern []string `json:"current_pattern"`
	KeyChanges     []string `json:"key_changes"`
}

type AuditBaselineComparison struct {
	Current         float64  `json:"current,omitempty"`
	BaselineP95     *float64 `json:"baseline_p95,omitempty"`
	Threshold       *float64 `json:"threshold,omitempty"`
	DurationSeconds int      `json:"duration_seconds,omitempty"`
}

type AuditReportFinding struct {
	FindingID                   string                  `json:"finding_id"`
	Title                       string                  `json:"title"`
	Severity                    string                  `json:"severity"`
	Observation                 string                  `json:"observation"`
	BaselineComparison          AuditBaselineComparison `json:"baseline_comparison"`
	Interpretation              string                  `json:"interpretation"`
	EvidenceRefs                []string                `json:"evidence_refs"`
	CounterEvidenceRefs         []string                `json:"counter_evidence_refs"`
	PlausibleBenignExplanations []string                `json:"plausible_benign_explanations"`
	VerificationSteps           []string                `json:"verification_steps"`
	NeedsVerification           bool                    `json:"needs_verification,omitempty"`
}

type AuditReportTimelineItem struct {
	TimelineID   string   `json:"timeline_id"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Detail       string   `json:"detail"`
	StartedAt    string   `json:"started_at,omitempty"`
	EndedAt      string   `json:"ended_at,omitempty"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type AuditReportCounter struct {
	CounterID    string   `json:"counter_id"`
	Text         string   `json:"text"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

type AuditReportAction struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

type AuditReportDataQuality struct {
	Coverage        float64 `json:"coverage"`
	BaselineDays    int     `json:"baseline_days"`
	DroppedBuckets  int64   `json:"dropped_buckets"`
	IdentityQuality float64 `json:"identity_quality"`
}

type AuditReportMethodology struct {
	FeatureVersion         int    `json:"feature_version"`
	ScoringVersion         string `json:"scoring_version"`
	BaselineVersion        string `json:"baseline_version"`
	EvidenceSchemaVersion  string `json:"evidence_schema_version"`
	PromptVersion          string `json:"prompt_version"`
	ReportSchemaVersion    string `json:"report_schema_version"`
	ProviderProfileVersion string `json:"provider_profile_version"`
	ProviderGrade          string `json:"provider_grade"`
	StructuredOutput       string `json:"structured_output"`
	OutputMode             string `json:"output_mode"`
	Model                  string `json:"model"`
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
