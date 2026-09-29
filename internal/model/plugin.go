package model

import (
	"encoding/json"
	"time"
)

// Plugin runtime settings. The runtime is opt-in and defaults off after
// install and restore.
const (
	PluginSettingEnabled            = "plugins.enabled"
	PluginSettingSchedulerPaused    = "plugins.scheduler_paused"
	PluginSettingRecoveryGeneration = "plugins.recovery_generation"
	PluginSettingMaxConcurrency     = "plugins.max_concurrency"
	PluginSettingMaxTimeoutSeconds  = "plugins.max_timeout_seconds"
	PluginSettingLogRetentionDays   = "plugins.log_retention_days"
	PluginSettingRunRetentionDays   = "plugins.run_retention_days"
	PluginSettingModel              = "plugins.model"

	// PluginModelCurrent marks the capability-based plugin model. Any other
	// value means the database still carries an earlier plugin runtime.
	PluginModelCurrent = "capability"
)

const (
	PluginRunQueued           = "queued"
	PluginRunRunning          = "running"
	PluginRunSucceeded        = "succeeded"
	PluginRunFailed           = "failed"
	PluginRunTimeout          = "timeout"
	PluginRunCancelled        = "cancelled"
	PluginRunPermissionDenied = "permission_denied"
	PluginRunResourceLimit    = "resource_limit"

	PluginTriggerManual   = "manual"
	PluginTriggerInterval = "interval"
	PluginTriggerCron     = "cron"
	PluginTriggerEvent    = "event"

	PluginSourceUpload = "upload"
	PluginSourceGitHub = "github"
	PluginSourceEditor = "editor"

	PluginSignatureVerified = "verified"
	PluginSignatureUnsigned = "unsigned"

	PluginPublisherLocal = "local"
)

// PluginRunTerminal reports whether a run status is final.
func PluginRunTerminal(status string) bool {
	switch status {
	case PluginRunSucceeded, PluginRunFailed, PluginRunTimeout, PluginRunCancelled, PluginRunPermissionDenied, PluginRunResourceLimit:
		return true
	default:
		return false
	}
}

// PluginInstallation is one installed plugin identity (manifest id). The
// publisher identity is fixed at first install; a different publisher can
// never update it.
type PluginInstallation struct {
	ID                int64           `json:"id"`
	PluginKey         string          `json:"plugin_key"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	ActivePackageID   int64           `json:"active_package_id"`
	PublisherIdentity string          `json:"publisher_identity"`
	Enabled           bool            `json:"enabled"`
	DraftManifestJSON json.RawMessage `json:"-"`
	DraftSource       string          `json:"-"`
	DraftUpdatedAt    *time.Time      `json:"draft_updated_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// PluginPackage is an immutable, validated package version.
type PluginPackage struct {
	ID                int64           `json:"id"`
	InstallationID    int64           `json:"installation_id"`
	PluginKey         string          `json:"plugin_key"`
	Version           string          `json:"version"`
	SHA256            string          `json:"sha256"`
	ManifestJSON      json.RawMessage `json:"manifest"`
	Source            string          `json:"-"`
	IconPNG           []byte          `json:"-"`
	Readme            string          `json:"readme,omitempty"`
	License           string          `json:"license,omitempty"`
	PublisherIdentity string          `json:"publisher_identity"`
	PublisherName     string          `json:"publisher_name"`
	SignatureState    string          `json:"signature_state"`
	SourceKind        string          `json:"source_kind"`
	SourceRepository  string          `json:"source_repository,omitempty"`
	SourceCommit      string          `json:"source_commit,omitempty"`
	CreatedByUserID   int64           `json:"created_by_user_id"`
	CreatedAt         time.Time       `json:"created_at"`
}

// PluginInstance is one configured copy of an installation: environment,
// grant, secrets, state, schedules and runs are all instance-scoped.
type PluginInstance struct {
	ID                       int64           `json:"id"`
	InstallationID           int64           `json:"installation_id"`
	Name                     string          `json:"name"`
	Enabled                  bool            `json:"enabled"`
	AutoPaused               bool            `json:"auto_paused"`
	PermissionReviewRequired bool            `json:"permission_review_required"`
	ValuesJSON               json.RawMessage `json:"values"`
	CustomJSON               json.RawMessage `json:"custom"`
	Revision                 int64           `json:"revision"`
	FailureStreak            int             `json:"failure_streak"`
	LastRunAt                *time.Time      `json:"last_run_at,omitempty"`
	LastSuccessAt            *time.Time      `json:"last_success_at,omitempty"`
	LastErrorCode            string          `json:"last_error_code,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
}

// PluginGrant binds an administrator decision to one instance and one exact
// package. A grant for another package id never applies.
type PluginGrant struct {
	InstanceID       int64           `json:"instance_id"`
	PackageID        int64           `json:"package_id"`
	GrantJSON        json.RawMessage `json:"grant"`
	Revision         int64           `json:"revision"`
	ApprovedByUserID int64           `json:"approved_by_user_id"`
	ApprovedAt       time.Time       `json:"approved_at"`
}

type PluginSchedule struct {
	ID             int64      `json:"id"`
	InstanceID     int64      `json:"instance_id"`
	Kind           string     `json:"kind"`
	Interval       string     `json:"interval,omitempty"`
	Cron           string     `json:"cron,omitempty"`
	Timezone       string     `json:"timezone,omitempty"`
	Event          string     `json:"event,omitempty"`
	Enabled        bool       `json:"enabled"`
	NextDueAt      *time.Time `json:"next_due_at,omitempty"`
	LastFiredAt    *time.Time `json:"last_fired_at,omitempty"`
	LastSkippedAt  *time.Time `json:"last_skipped_at,omitempty"`
	LastSkipReason string     `json:"last_skip_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type PluginRun struct {
	ID                  int64           `json:"id"`
	UUID                string          `json:"uuid"`
	InstallationID      int64           `json:"installation_id"`
	InstanceID          int64           `json:"instance_id"`
	PackageID           int64           `json:"package_id"`
	PluginKey           string          `json:"plugin_key"`
	PluginVersion       string          `json:"plugin_version"`
	Trigger             string          `json:"trigger"`
	TriggerJSON         json.RawMessage `json:"trigger_detail"`
	CallerPrincipal     string          `json:"caller_principal,omitempty"`
	IdempotencyKey      string          `json:"-"`
	Status              string          `json:"status"`
	ErrorCode           string          `json:"error_code,omitempty"`
	ErrorMessage        string          `json:"error_message,omitempty"`
	CancelRequested     bool            `json:"cancel_requested"`
	QueuedAt            time.Time       `json:"queued_at"`
	StartedAt           *time.Time      `json:"started_at,omitempty"`
	FinishedAt          *time.Time      `json:"finished_at,omitempty"`
	CapabilityCallCount int             `json:"capability_call_count"`
	HTTPCallCount       int             `json:"http_call_count"`
	AgentOperationCount int             `json:"agent_operation_count"`
	ResultJSON          json.RawMessage `json:"result,omitempty"`
	LeaseOwner          string          `json:"-"`
	LeaseGeneration     int64           `json:"-"`
	LeaseUntil          *time.Time      `json:"-"`
	RecoveryGeneration  int64           `json:"-"`
}

type PluginRunLog struct {
	RunID     int64     `json:"run_id"`
	Seq       int64     `json:"seq"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type PluginStateEntry struct {
	InstanceID int64           `json:"instance_id"`
	Key        string          `json:"key"`
	ValueJSON  json.RawMessage `json:"value"`
	Version    int64           `json:"version"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// PluginAuditEvent records one sensitive capability call. It never carries
// secrets, request bodies or response bodies.
type PluginAuditEvent struct {
	ID             int64           `json:"id"`
	CreatedAt      time.Time       `json:"created_at"`
	InstallationID int64           `json:"installation_id"`
	InstanceID     int64           `json:"instance_id"`
	PluginKey      string          `json:"plugin_key"`
	PluginVersion  string          `json:"plugin_version"`
	RunUUID        string          `json:"run_uuid"`
	Capability     string          `json:"capability"`
	Resource       string          `json:"resource"`
	Result         string          `json:"result"`
	ErrorCode      string          `json:"error_code,omitempty"`
	DurationMS     int64           `json:"duration_ms"`
	DetailJSON     json.RawMessage `json:"detail"`
}

type PluginRuntimeStatus struct {
	Enabled            bool   `json:"enabled"`
	SchedulerPaused    bool   `json:"scheduler_paused"`
	RuntimeInstalled   bool   `json:"runtime_installed"`
	InstallCommand     string `json:"install_command,omitempty"`
	WorkerConnected    bool   `json:"worker_connected"`
	IsolationAvailable bool   `json:"isolation_available"`
	IsolationMode      string `json:"isolation_mode"`
	IsolationReason    string `json:"isolation_reason,omitempty"`
	ActiveRuns         int    `json:"active_runs"`
	QueuedRuns         int    `json:"queued_runs"`
	MaxConcurrency     int    `json:"max_concurrency"`
	MaxTimeoutSeconds  int    `json:"max_timeout_seconds"`
	LogRetentionDays   int    `json:"log_retention_days"`
	RunRetentionDays   int    `json:"run_retention_days"`
}
