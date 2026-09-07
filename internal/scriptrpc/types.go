package scriptrpc

import (
	"encoding/json"
	"time"
)

const (
	ProtocolVersion = 1
	RuntimeOBoardJS = "oboard-js-v1"
)

type LeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type LeaseResponse struct {
	Run            *RunLease `json:"run,omitempty"`
	IsolationOK    bool      `json:"isolation_ok"`
	IsolationMode  string    `json:"isolation_mode"`
	IsolationReason string   `json:"isolation_reason,omitempty"`
	RetryAfterMS   int       `json:"retry_after_ms,omitempty"`
}

type RunLease struct {
	RunID           int64           `json:"run_id"`
	UUID            string          `json:"uuid"`
	ScriptID        int64           `json:"script_id"`
	RevisionID      int64           `json:"revision_id"`
	LeaseGeneration int64           `json:"lease_generation"`
	Mode            string          `json:"mode"`
	Source          string          `json:"source"`
	Params          json.RawMessage `json:"params"`
	Env             map[string]string `json:"env"`
	Limits          RunLimits       `json:"limits"`
	Timeout         time.Duration   `json:"-"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
}

type RunLimits struct {
	TimeoutSeconds int   `json:"timeout_seconds"`
	MemoryMiB      int   `json:"memory_mib"`
	SDKCalls       int   `json:"sdk_calls"`
	ManageActions  int   `json:"manage_actions"`
	LogBytes       int64 `json:"log_bytes"`
	ResultBytes    int64 `json:"result_bytes"`
}

type CompleteRequest struct {
	WorkerID        string          `json:"worker_id"`
	RunUUID         string          `json:"run_uuid"`
	LeaseGeneration int64           `json:"lease_generation"`
	Status          string          `json:"status"`
	ErrorCode       string          `json:"error_code,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Logs            []LogLine       `json:"logs,omitempty"`
}

type LogLine struct {
	Seq     int64           `json:"seq"`
	Level   string          `json:"level"`
	Message string          `json:"message"`
	Fields  json.RawMessage `json:"fields,omitempty"`
}

type SDKRequest struct {
	WorkerID        string          `json:"worker_id"`
	RunUUID         string          `json:"run_uuid"`
	LeaseGeneration int64           `json:"lease_generation"`
	Capability      string          `json:"capability"`
	ActionKey       string          `json:"action_key"`
	Arguments       json.RawMessage `json:"arguments"`
}

type SDKResponse struct {
	OK           bool            `json:"ok"`
	ErrorCode    string          `json:"error_code,omitempty"`
	Message      string          `json:"message,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	OperationID  string          `json:"operation_id,omitempty"`
	ChangesetID  string          `json:"changeset_id,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
}

type CancelCheckRequest struct {
	RunUUID         string `json:"run_uuid"`
	LeaseGeneration int64  `json:"lease_generation"`
}

type CancelCheckResponse struct {
	Cancelled bool `json:"cancelled"`
}

type HeartbeatRequest struct {
	WorkerID           string `json:"worker_id"`
	IsolationAvailable bool   `json:"isolation_available"`
	IsolationMode      string `json:"isolation_mode"`
	IsolationReason    string `json:"isolation_reason,omitempty"`
}

type HeartbeatResponse struct {
	Enabled bool `json:"enabled"`
}
