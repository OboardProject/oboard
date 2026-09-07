package model

import (
	"encoding/json"
	"time"
)

const (
	ScriptRuntimeOBoardJSv1 = "oboard-js-v1"
	ScriptSDKVersionV1      = "oboard-sdk-v1"
	ScriptSchemaVersion     = 1

	ScriptStatusDraft    = "draft"
	ScriptStatusEnabled  = "enabled"
	ScriptStatusDisabled = "disabled"
	ScriptStatusArchived = "archived"

	ScriptRevisionDraft      = "draft"
	ScriptRevisionPublished  = "published"
	ScriptRevisionSuperseded = "superseded"

	ScriptTriggerOnce     = "once"
	ScriptTriggerInterval = "interval"
	ScriptTriggerCron     = "cron"
	ScriptTriggerEvent    = "event"

	ScriptEventServerOffline         = "server.offline"
	ScriptEventServerRecovered       = "server.recovered"
	ScriptEventTaskFailed            = "task.failed"
	ScriptEventTaskTimedOut          = "task.timed_out"
	ScriptEventMetricConditionEntered = "metric.condition_entered"
	ScriptEventMetricConditionCleared = "metric.condition_cleared"

	ScriptRunQueued    = "queued"
	ScriptRunRunning   = "running"
	ScriptRunSucceeded = "succeeded"
	ScriptRunFailed    = "failed"
	ScriptRunTimedOut  = "timed_out"
	ScriptRunCancelled = "cancelled"
	ScriptRunSkipped   = "skipped"

	ScriptRunModeLive     = "live"
	ScriptRunModeSimulate = "simulate"
	ScriptRunModeValidate = "validate"

	ScriptActionPending     = "pending"
	ScriptActionAccepted    = "accepted"
	ScriptActionDispatching = "dispatching"
	ScriptActionExecuting   = "executing"
	ScriptActionSucceeded   = "succeeded"
	ScriptActionFailed      = "failed"
	ScriptActionPartial     = "partial"
	ScriptActionUnknown     = "unknown"
	ScriptActionCancelled   = "cancelled"

	ScriptSkipOverlap          = "overlap"
	ScriptSkipMissed           = "missed"
	ScriptSkipDSTGap           = "dst_gap"
	ScriptSkipConditionChanged = "condition_changed"
	ScriptSkipSuppressed       = "suppressed"
	ScriptSkipDuplicate        = "duplicate"
	ScriptSkipDisabled         = "disabled"
	ScriptSkipPaused           = "scheduler_paused"

	ScriptSDKServersGet        = "servers.get"
	ScriptSDKServersList       = "servers.list"
	ScriptSDKServersStatus     = "servers.status"
	ScriptSDKMetricsLatest     = "metrics.latest"
	ScriptSDKIncidentsGet      = "incidents.get"
	ScriptSDKServicesStatus    = "services.status"
	ScriptSDKServicesRestart   = "services.restart"
	ScriptSDKHostPoweroff      = "host.poweroff"
	ScriptSDKHostReboot        = "host.reboot"
	ScriptSDKNotificationsSend = "notifications.send"
	ScriptSDKOperationsGet     = "operations.get"
	ScriptSDKOperationsWait    = "operations.wait"
	ScriptSDKStateGet          = "state.get"
	ScriptSDKStateCAS          = "state.compareAndSet"

	ScriptErrorPermissionDenied      = "permission_denied"
	ScriptErrorApprovalRequired      = "approval_required"
	ScriptErrorResourceOutOfScope    = "resource_out_of_scope"
	ScriptErrorConditionChanged      = "condition_changed"
	ScriptErrorTargetOffline         = "target_offline"
	ScriptErrorCapabilityUnsupported = "capability_unsupported"
	ScriptErrorOperationExpired      = "operation_expired"
	ScriptErrorIdempotencyConflict   = "idempotency_conflict"
	ScriptErrorResultUnknown         = "result_unknown"
	ScriptErrorRuntimeUnavailable    = "runtime_unavailable"
	ScriptErrorLimitExceeded         = "limit_exceeded"
	ScriptErrorInvalidInput          = "invalid_input"
	ScriptErrorCancelled             = "cancelled"

	ScriptSettingEnabled              = "scripts.enabled"
	ScriptSettingHostActionsEnabled   = "scripts.host_actions_enabled"
	ScriptSettingSchedulerPaused      = "scripts.scheduler_paused"
	ScriptSettingRecoveryGeneration   = "scripts.recovery_generation"
	ScriptSettingMaxConcurrency       = "scripts.max_concurrency"
	ScriptSettingMaxTimeoutSeconds    = "scripts.max_timeout_seconds"
	ScriptSettingLogRetentionDays     = "scripts.log_retention_days"
	ScriptSettingSummaryRetentionDays = "scripts.summary_retention_days"

	RemoteExecOriginScript = "script"

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

type Script struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OwnerUserID int64     `json:"owner_user_id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ScriptRevision struct {
	ID                int64           `json:"id"`
	ScriptID          int64           `json:"script_id"`
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

type ScriptTriggerBinding struct {
	ID               int64           `json:"id"`
	ScriptID         int64           `json:"script_id"`
	RevisionID       int64           `json:"revision_id"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	Kind             string          `json:"kind"`
	SpecJSON         json.RawMessage `json:"spec"`
	ParamsJSON       json.RawMessage `json:"params"`
	EnvJSON          json.RawMessage `json:"env"`
	BindingRevision  int64           `json:"binding_revision"`
	CreatedByUserID  int64           `json:"created_by_user_id"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type ScriptGrant struct {
	ID               int64           `json:"id"`
	ScriptID         int64           `json:"script_id"`
	RevisionID       int64           `json:"revision_id"`
	BindingID        *int64          `json:"binding_id,omitempty"`
	GrantRevision    int64           `json:"grant_revision"`
	CapabilitiesJSON json.RawMessage `json:"capabilities"`
	ResourceScopeJSON json.RawMessage `json:"resource_scope"`
	ConstraintsJSON  json.RawMessage `json:"constraints"`
	SourceDigest     string          `json:"source_digest"`
	BindingDigest    string          `json:"binding_digest"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	RevokedAt        *time.Time      `json:"revoked_at,omitempty"`
	ApprovedByUserID int64           `json:"approved_by_user_id"`
	CreatedAt        time.Time       `json:"created_at"`
}

type ScriptTriggerState struct {
	BindingID          int64           `json:"binding_id"`
	Armed              bool            `json:"armed"`
	ConditionJSON      json.RawMessage `json:"condition"`
	CurrentCycleKey    string          `json:"current_cycle_key"`
	LastFiredAt        *time.Time      `json:"last_fired_at,omitempty"`
	LastSkippedAt      *time.Time      `json:"last_skipped_at,omitempty"`
	LastSkipReason     string          `json:"last_skip_reason,omitempty"`
	NextDueAt          *time.Time      `json:"next_due_at,omitempty"`
	HoldUntil          *time.Time      `json:"hold_until,omitempty"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type ScriptRun struct {
	ID              int64           `json:"id"`
	UUID            string          `json:"uuid"`
	ScriptID        int64           `json:"script_id"`
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

type ScriptRunAttempt struct {
	ID              int64           `json:"id"`
	RunID           int64           `json:"run_id"`
	Generation      int64           `json:"generation"`
	WorkerID        string          `json:"worker_id"`
	Status          string          `json:"status"`
	ResourceJSON    json.RawMessage `json:"resource"`
	ErrorCode       string          `json:"error_code,omitempty"`
	StartedAt       time.Time       `json:"started_at"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
}

type ScriptRunAction struct {
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

type ScriptStateEntry struct {
	ScriptID  int64           `json:"script_id"`
	Key       string          `json:"key"`
	ValueJSON json.RawMessage `json:"value"`
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type ScriptRunLog struct {
	ID        int64     `json:"id"`
	RunID     int64     `json:"run_id"`
	Seq       int64     `json:"seq"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	FieldsJSON json.RawMessage `json:"fields,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ScriptSecret struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Purpose          string     `json:"purpose"`
	ResourceScopeJSON json.RawMessage `json:"resource_scope"`
	ValueEncrypted   string     `json:"-"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	CreatedByUserID  int64      `json:"created_by_user_id"`
	CreatedAt        time.Time  `json:"created_at"`
}

type ServerScriptPolicy struct {
	ServerID             int64     `json:"server_id"`
	ScriptsEnabled       bool      `json:"scripts_enabled"`
	ScriptsPowerEnabled  bool      `json:"scripts_power_enabled"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type HostPowerTaskPayload struct {
	ProtocolVersion   int             `json:"protocol_version"`
	OperationID       string          `json:"operation_id"`
	Action            string          `json:"action"`
	ServerID          int64           `json:"server_id"`
	Source            string          `json:"source"`
	RunID             string          `json:"run_id"`
	ScriptRevisionID  int64           `json:"script_revision_id,omitempty"`
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

type ScriptRuntimeStatus struct {
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

type ScriptManifest struct {
	SchemaVersion int                      `json:"schema_version"`
	Runtime       string                   `json:"runtime"`
	SDKVersion    string                   `json:"sdk_version"`
	Entry         string                   `json:"entry"`
	Params        json.RawMessage          `json:"params_schema"`
	Env           []ScriptEnvDeclaration   `json:"env"`
	Secrets       []ScriptSecretRef        `json:"secrets"`
	Capabilities  []string                 `json:"capabilities"`
	ResourceTypes []string                 `json:"resource_types"`
	Limits        ScriptDeclaredLimits     `json:"limits"`
	Output        json.RawMessage          `json:"output_schema,omitempty"`
}

type ScriptEnvDeclaration struct {
	Name       string `json:"name"`
	Default    string `json:"default,omitempty"`
	Overridable bool  `json:"overridable"`
	Sensitive  bool   `json:"sensitive"`
}

type ScriptSecretRef struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
}

type ScriptDeclaredLimits struct {
	TimeoutSeconds int   `json:"timeout_seconds,omitempty"`
	MemoryMiB      int   `json:"memory_mib,omitempty"`
	SDKCalls       int   `json:"sdk_calls,omitempty"`
	ManageActions  int   `json:"manage_actions,omitempty"`
	LogBytes       int64 `json:"log_bytes,omitempty"`
	ResultBytes    int64 `json:"result_bytes,omitempty"`
}

type ScriptTriggerSpec struct {
	Timezone           string          `json:"timezone"`
	OnceAt             string          `json:"once_at,omitempty"`
	IntervalSeconds    int             `json:"interval_seconds,omitempty"`
	Cron               string          `json:"cron,omitempty"`
	Event              string          `json:"event,omitempty"`
	SubjectServerIDs   []int64         `json:"subject_server_ids,omitempty"`
	TargetServerIDs    []int64         `json:"target_server_ids,omitempty"`
	SustainSeconds     int             `json:"sustain_seconds,omitempty"`
	CatchupOnce        bool            `json:"catchup_once,omitempty"`
	RepeatWhileOffline bool            `json:"repeat_while_offline,omitempty"`
	Metric             *ScriptMetricCondition `json:"metric,omitempty"`
	MaxDelaySeconds    int             `json:"max_delay_seconds,omitempty"`
}

type ScriptMetricCondition struct {
	Metric           string  `json:"metric"`
	Operator         string  `json:"operator"`
	Threshold        float64 `json:"threshold"`
	DurationSeconds  int     `json:"duration_seconds"`
	ClearThreshold   float64 `json:"clear_threshold"`
	ClearDurationSec int     `json:"clear_duration_seconds"`
}

type ScriptGrantConstraints struct {
	Actions            []string `json:"actions"`
	ServerIDs          []int64  `json:"server_ids"`
	WindowStart        string   `json:"window_start,omitempty"`
	WindowEnd          string   `json:"window_end,omitempty"`
	MaxRunsPerHour     int      `json:"max_runs_per_hour,omitempty"`
	MaxTargetsPerRun   int      `json:"max_targets_per_run,omitempty"`
	ExpiresAt          string   `json:"expires_at,omitempty"`
}
