package model

import (
	"encoding/json"
	"time"
)

const (
	PluginRuntimeOBoardJSv1 = "oboard-js-v1"
	PluginSDKVersionV1      = "oboard-sdk-v1"
	PluginSchemaVersion     = 1

	PluginStatusDraft    = "draft"
	PluginStatusEnabled  = "enabled"
	PluginStatusDisabled = "disabled"
	PluginStatusArchived = "archived"

	PluginRevisionDraft      = "draft"
	PluginRevisionPublished  = "published"
	PluginRevisionSuperseded = "superseded"

	PluginTriggerOnce     = "once"
	PluginTriggerInterval = "interval"
	PluginTriggerCron     = "cron"
	PluginTriggerEvent    = "event"

	PluginEventServerOffline          = "server.offline"
	PluginEventServerRecovered        = "server.recovered"
	PluginEventTaskFailed             = "task.failed"
	PluginEventTaskTimedOut           = "task.timed_out"
	PluginEventMetricConditionEntered = "metric.condition_entered"
	PluginEventMetricConditionCleared = "metric.condition_cleared"

	PluginRunQueued    = "queued"
	PluginRunRunning   = "running"
	PluginRunSucceeded = "succeeded"
	PluginRunFailed    = "failed"
	PluginRunTimedOut  = "timed_out"
	PluginRunCancelled = "cancelled"
	PluginRunSkipped   = "skipped"

	PluginRunModeLive     = "live"
	PluginRunModeSimulate = "simulate"
	PluginRunModeValidate = "validate"

	PluginActionPending     = "pending"
	PluginActionAccepted    = "accepted"
	PluginActionDispatching = "dispatching"
	PluginActionExecuting   = "executing"
	PluginActionSucceeded   = "succeeded"
	PluginActionFailed      = "failed"
	PluginActionPartial     = "partial"
	PluginActionUnknown     = "unknown"
	PluginActionCancelled   = "cancelled"

	PluginSkipOverlap          = "overlap"
	PluginSkipMissed           = "missed"
	PluginSkipDSTGap           = "dst_gap"
	PluginSkipConditionChanged = "condition_changed"
	PluginSkipSuppressed       = "suppressed"
	PluginSkipDuplicate        = "duplicate"
	PluginSkipDisabled         = "disabled"
	PluginSkipPaused           = "scheduler_paused"

	PluginSDKServersGet        = "servers.get"
	PluginSDKServersList       = "servers.list"
	PluginSDKServersStatus     = "servers.status"
	PluginSDKMetricsLatest     = "metrics.latest"
	PluginSDKIncidentsGet      = "incidents.get"
	PluginSDKServicesStatus    = "services.status"
	PluginSDKServicesRestart   = "services.restart"
	PluginSDKHostPoweroff      = "host.poweroff"
	PluginSDKHostReboot        = "host.reboot"
	PluginSDKNotificationsSend = "notifications.send"
	PluginSDKOperationsGet     = "operations.get"
	PluginSDKOperationsWait    = "operations.wait"
	PluginSDKStateGet          = "state.get"
	PluginSDKStateCAS          = "state.compareAndSet"

	PluginErrorPermissionDenied      = "permission_denied"
	PluginErrorApprovalRequired      = "approval_required"
	PluginErrorResourceOutOfScope    = "resource_out_of_scope"
	PluginErrorConditionChanged      = "condition_changed"
	PluginErrorTargetOffline         = "target_offline"
	PluginErrorCapabilityUnsupported = "capability_unsupported"
	PluginErrorOperationExpired      = "operation_expired"
	PluginErrorIdempotencyConflict   = "idempotency_conflict"
	PluginErrorResultUnknown         = "result_unknown"
	PluginErrorRuntimeUnavailable    = "runtime_unavailable"
	PluginErrorLimitExceeded         = "limit_exceeded"
	PluginErrorInvalidInput          = "invalid_input"
	PluginErrorCancelled             = "cancelled"

	PluginSettingEnabled              = "plugins.enabled"
	PluginSettingHostActionsEnabled   = "plugins.host_actions_enabled"
	PluginSettingSchedulerPaused      = "plugins.scheduler_paused"
	PluginSettingRecoveryGeneration   = "plugins.recovery_generation"
	PluginSettingMaxConcurrency       = "plugins.max_concurrency"
	PluginSettingMaxTimeoutSeconds    = "plugins.max_timeout_seconds"
	PluginSettingLogRetentionDays     = "plugins.log_retention_days"
	PluginSettingSummaryRetentionDays = "plugins.summary_retention_days"

	RemoteExecOriginPlugin = "plugin"

	AgentTaskTypeHostPowerAction = "host_power_action"
	AgentCapabilityHostPower     = "host_power_v1"

	HostPowerActionPoweroff = "poweroff"
	HostPowerActionReboot   = "reboot"

	HostPowerReceiptAccepted  = "accepted"
	HostPowerReceiptPreparing = "preparing"
	HostPowerReceiptInitiated = "initiated"
	HostPowerReceiptObserved  = "observed"

	HostPowerProtocolVersion = 1
)

type Plugin struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OwnerUserID int64     `json:"owner_user_id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type PluginRevision struct {
	ID                int64           `json:"id"`
	PluginID          int64           `json:"plugin_id"`
	RevisionNumber    int64           `json:"revision_number"`
	Status            string          `json:"status"`
	SchemaVersion     int             `json:"schema_version"`
	Runtime           string          `json:"runtime"`
	SDKVersion        string          `json:"sdk_version"`
	Source            string          `json:"source,omitempty"`
	SourceDigest      string          `json:"source_digest"`
	ManifestJSON      json.RawMessage `json:"manifest"`
	AuthorUserID      int64           `json:"author_user_id"`
	PublishedAt       *time.Time      `json:"published_at,omitempty"`
	PublishedByUserID *int64          `json:"published_by_user_id,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

type PluginTriggerBinding struct {
	ID              int64           `json:"id"`
	PluginID        int64           `json:"plugin_id"`
	RevisionID      int64           `json:"revision_id"`
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	Kind            string          `json:"kind"`
	SpecJSON        json.RawMessage `json:"spec"`
	ParamsJSON      json.RawMessage `json:"params"`
	EnvJSON         json.RawMessage `json:"env"`
	BindingRevision int64           `json:"binding_revision"`
	CreatedByUserID int64           `json:"created_by_user_id"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type PluginGrant struct {
	ID                int64           `json:"id"`
	PluginID          int64           `json:"plugin_id"`
	RevisionID        int64           `json:"revision_id"`
	BindingID         *int64          `json:"binding_id,omitempty"`
	GrantRevision     int64           `json:"grant_revision"`
	CapabilitiesJSON  json.RawMessage `json:"capabilities"`
	ResourceScopeJSON json.RawMessage `json:"resource_scope"`
	ConstraintsJSON   json.RawMessage `json:"constraints"`
	SourceDigest      string          `json:"source_digest"`
	BindingDigest     string          `json:"binding_digest"`
	ExpiresAt         *time.Time      `json:"expires_at,omitempty"`
	RevokedAt         *time.Time      `json:"revoked_at,omitempty"`
	ApprovedByUserID  int64           `json:"approved_by_user_id"`
	CreatedAt         time.Time       `json:"created_at"`
}

type PluginTriggerState struct {
	BindingID       int64           `json:"binding_id"`
	Armed           bool            `json:"armed"`
	ConditionJSON   json.RawMessage `json:"condition"`
	CurrentCycleKey string          `json:"current_cycle_key"`
	LastFiredAt     *time.Time      `json:"last_fired_at,omitempty"`
	LastSkippedAt   *time.Time      `json:"last_skipped_at,omitempty"`
	LastSkipReason  string          `json:"last_skip_reason,omitempty"`
	NextDueAt       *time.Time      `json:"next_due_at,omitempty"`
	HoldUntil       *time.Time      `json:"hold_until,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type PluginRun struct {
	ID              int64           `json:"id"`
	UUID            string          `json:"uuid"`
	PluginID        int64           `json:"plugin_id"`
	RevisionID      int64           `json:"revision_id"`
	BindingID       *int64          `json:"binding_id,omitempty"`
	GrantID         *int64          `json:"grant_id,omitempty"`
	CallerPrincipal string          `json:"caller_principal,omitempty"`
	TriggerKind     string          `json:"trigger_kind"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Status          string          `json:"status"`
	Mode            string          `json:"mode"`
	SnapshotJSON    json.RawMessage `json:"snapshot"`
	ResultJSON      json.RawMessage `json:"result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	SkipReason      string          `json:"skip_reason,omitempty"`
	LeaseOwner      string          `json:"lease_owner,omitempty"`
	LeaseGeneration int64           `json:"lease_generation"`
	LeaseUntil      *time.Time      `json:"lease_until,omitempty"`
	RecoveryGen     int64           `json:"recovery_generation"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
}

type PluginRunAttempt struct {
	ID           int64           `json:"id"`
	RunID        int64           `json:"run_id"`
	Generation   int64           `json:"generation"`
	WorkerID     string          `json:"worker_id"`
	Status       string          `json:"status"`
	ResourceJSON json.RawMessage `json:"resource"`
	ErrorCode    string          `json:"error_code,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
}

type PluginRunAction struct {
	ID              int64           `json:"id"`
	RunID           int64           `json:"run_id"`
	ActionKey       string          `json:"action_key"`
	Capability      string          `json:"capability"`
	TargetJSON      json.RawMessage `json:"target"`
	PayloadDigest   string          `json:"payload_digest"`
	Status          string          `json:"status"`
	OperationID     string          `json:"operation_id,omitempty"`
	ChangesetID     string          `json:"changeset_id,omitempty"`
	TaskID          *int64          `json:"task_id,omitempty"`
	ResultJSON      json.RawMessage `json:"result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	LeaseGeneration int64           `json:"lease_generation"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type PluginStateEntry struct {
	PluginID  int64           `json:"plugin_id"`
	Key       string          `json:"key"`
	ValueJSON json.RawMessage `json:"value"`
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type PluginRunLog struct {
	ID         int64           `json:"id"`
	RunID      int64           `json:"run_id"`
	Seq        int64           `json:"seq"`
	Level      string          `json:"level"`
	Message    string          `json:"message"`
	FieldsJSON json.RawMessage `json:"fields,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type PluginSecret struct {
	ID                int64           `json:"id"`
	Name              string          `json:"name"`
	Purpose           string          `json:"purpose"`
	ResourceScopeJSON json.RawMessage `json:"resource_scope"`
	ValueEncrypted    string          `json:"-"`
	RevokedAt         *time.Time      `json:"revoked_at,omitempty"`
	CreatedByUserID   int64           `json:"created_by_user_id"`
	CreatedAt         time.Time       `json:"created_at"`
}

type ServerPluginPolicy struct {
	ServerID            int64     `json:"server_id"`
	PluginsEnabled      bool      `json:"plugins_enabled"`
	PluginsPowerEnabled bool      `json:"plugins_power_enabled"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type HostPowerTaskPayload struct {
	ProtocolVersion   int             `json:"protocol_version"`
	OperationID       string          `json:"operation_id"`
	Action            string          `json:"action"`
	ServerID          int64           `json:"server_id"`
	Source            string          `json:"source"`
	RunID             string          `json:"run_id"`
	PluginRevisionID  int64           `json:"plugin_revision_id,omitempty"`
	TriggerBindingID  int64           `json:"trigger_binding_id,omitempty"`
	GrantID           int64           `json:"grant_id,omitempty"`
	IssuedAt          time.Time       `json:"issued_at"`
	ExpiresAt         time.Time       `json:"expires_at"`
	ExpectedBootID    string          `json:"expected_boot_id,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	PreconditionsJSON json.RawMessage `json:"preconditions,omitempty"`
	PayloadDigest     string          `json:"payload_digest"`
}

type HostPowerReceipt struct {
	OperationID string    `json:"operation_id"`
	TaskID      int64     `json:"task_id,omitempty"`
	ServerID    int64     `json:"server_id"`
	Sequence    int64     `json:"sequence"`
	Stage       string    `json:"stage"`
	Observed    string    `json:"observed,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type PluginRuntimeStatus struct {
	Enabled            bool   `json:"enabled"`
	HostActionsEnabled bool   `json:"host_actions_enabled"`
	SchedulerPaused    bool   `json:"scheduler_paused"`
	RecoveryGeneration int64  `json:"recovery_generation"`
	RuntimeInstalled   bool   `json:"runtime_installed"`
	InstallCommand     string `json:"install_command,omitempty"`
	WorkerConnected    bool   `json:"worker_connected"`
	IsolationAvailable bool   `json:"isolation_available"`
	IsolationMode      string `json:"isolation_mode"`
	IsolationReason    string `json:"isolation_reason,omitempty"`
	ActiveRuns         int    `json:"active_runs"`
	QueuedRuns         int    `json:"queued_runs"`
	MaxConcurrency     int    `json:"max_concurrency"`
}

type PluginNetworkPolicy struct {
	AllowedOrigins   []string `json:"allowed_origins"`
	AllowedMethods   []string `json:"allowed_methods"`
	MaxRequestBytes  int64    `json:"max_request_bytes,omitempty"`
	MaxResponseBytes int64    `json:"max_response_bytes,omitempty"`
}

type PluginManifest struct {
	PluginID        string                 `json:"plugin_id,omitempty"`
	Name            string                 `json:"name,omitempty"`
	Version         string                 `json:"version,omitempty"`
	Description     string                 `json:"description,omitempty"`
	UIContentSHA256 string                 `json:"ui_content_sha256,omitempty"`
	ConfigSchema    json.RawMessage        `json:"config_schema,omitempty"`
	Network         *PluginNetworkPolicy   `json:"network,omitempty"`
	SchemaVersion   int                    `json:"schema_version"`
	Runtime         string                 `json:"runtime"`
	SDKVersion      string                 `json:"sdk_version"`
	Entry           string                 `json:"entry"`
	Params          json.RawMessage        `json:"params_schema"`
	Env             []PluginEnvDeclaration `json:"env"`
	Secrets         []PluginSecretRef      `json:"secrets"`
	Capabilities    []string               `json:"capabilities"`
	ResourceTypes   []string               `json:"resource_types"`
	Limits          PluginDeclaredLimits   `json:"limits"`
	Output          json.RawMessage        `json:"output_schema,omitempty"`
}

type PluginEnvDeclaration struct {
	Name        string `json:"name"`
	Default     string `json:"default,omitempty"`
	Overridable bool   `json:"overridable"`
	Sensitive   bool   `json:"sensitive"`
}

type PluginSecretRef struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
}

type PluginDeclaredLimits struct {
	TimeoutSeconds int   `json:"timeout_seconds,omitempty"`
	MemoryMiB      int   `json:"memory_mib,omitempty"`
	SDKCalls       int   `json:"sdk_calls,omitempty"`
	ManageActions  int   `json:"manage_actions,omitempty"`
	LogBytes       int64 `json:"log_bytes,omitempty"`
	ResultBytes    int64 `json:"result_bytes,omitempty"`
}

type PluginTriggerSpec struct {
	Timezone           string                 `json:"timezone"`
	OnceAt             string                 `json:"once_at,omitempty"`
	IntervalSeconds    int                    `json:"interval_seconds,omitempty"`
	Cron               string                 `json:"cron,omitempty"`
	Event              string                 `json:"event,omitempty"`
	SubjectServerIDs   []int64                `json:"subject_server_ids,omitempty"`
	TargetServerIDs    []int64                `json:"target_server_ids,omitempty"`
	SustainSeconds     int                    `json:"sustain_seconds,omitempty"`
	CatchupOnce        bool                   `json:"catchup_once,omitempty"`
	RepeatWhileOffline bool                   `json:"repeat_while_offline,omitempty"`
	Metric             *PluginMetricCondition `json:"metric,omitempty"`
	MaxDelaySeconds    int                    `json:"max_delay_seconds,omitempty"`
}

type PluginMetricCondition struct {
	Metric           string  `json:"metric"`
	Operator         string  `json:"operator"`
	Threshold        float64 `json:"threshold"`
	DurationSeconds  int     `json:"duration_seconds"`
	ClearThreshold   float64 `json:"clear_threshold"`
	ClearDurationSec int     `json:"clear_duration_seconds"`
}

type PluginGrantConstraints struct {
	Network *PluginNetworkPolicy `json:"network,omitempty"`
	Secrets []string             `json:"secrets,omitempty"`
}
