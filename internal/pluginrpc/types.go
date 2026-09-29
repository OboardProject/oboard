// Package pluginrpc is the wire contract between the Controller and the
// unprivileged oboard-plugin-worker over the Controller-owned Unix socket.
package pluginrpc

import "encoding/json"

const (
	ProtocolVersion = 1
	Runtime         = "oboard-js"
)

type HeartbeatRequest struct {
	WorkerID           string `json:"worker_id"`
	ProtocolVersion    int    `json:"protocol_version"`
	IsolationAvailable bool   `json:"isolation_available"`
	IsolationMode      string `json:"isolation_mode"`
	IsolationReason    string `json:"isolation_reason,omitempty"`
}

type HeartbeatResponse struct {
	Enabled bool `json:"enabled"`
}

type LeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type LeaseResponse struct {
	Run          *RunLease `json:"run,omitempty"`
	RetryAfterMS int       `json:"retry_after_ms,omitempty"`
}

// RunLease is everything a disposable runner needs. It contains no secret
// plaintext: secret variables are delivered only as instance-bound references.
type RunLease struct {
	RunUUID         string              `json:"run_uuid"`
	LeaseGeneration int64               `json:"lease_generation"`
	PluginKey       string              `json:"plugin_key"`
	PluginVersion   string              `json:"plugin_version"`
	InstanceID      int64               `json:"instance_id"`
	Source          string              `json:"source"`
	Environment     map[string]EnvValue `json:"environment"`
	Context         RunContext          `json:"context"`
	Limits          Limits              `json:"limits"`
}

type EnvValue struct {
	Type   string          `json:"type"`
	Value  json.RawMessage `json:"value"`
	Raw    string          `json:"raw"`
	Custom bool            `json:"custom,omitempty"`
}

// RunContext is passed to main(run).
type RunContext struct {
	RunID         string          `json:"run_id"`
	PluginID      string          `json:"plugin_id"`
	PluginVersion string          `json:"plugin_version"`
	InstanceID    string          `json:"instance_id"`
	Trigger       string          `json:"trigger"`
	ScheduledAt   string          `json:"scheduled_at,omitempty"`
	Event         json.RawMessage `json:"event,omitempty"`
}

type Limits struct {
	TimeoutMS       int64 `json:"timeout_ms"`
	MemoryMiB       int   `json:"memory_mib"`
	SDKCalls        int   `json:"sdk_calls"`
	HTTPRequests    int   `json:"http_requests"`
	AgentOperations int   `json:"agent_operations"`
	LogBytes        int   `json:"log_bytes"`
	LogLines        int   `json:"log_lines"`
	LogLineBytes    int   `json:"log_line_bytes"`
	ResultBytes     int   `json:"result_bytes"`
}

// CallRequest is one SDK call forwarded from the runner to the Capability
// Gateway. Method is an SDK method name such as "network.trace".
type CallRequest struct {
	WorkerID        string          `json:"worker_id"`
	RunUUID         string          `json:"run_uuid"`
	LeaseGeneration int64           `json:"lease_generation"`
	Method          string          `json:"method"`
	Arguments       json.RawMessage `json:"arguments"`
}

type CallResponse struct {
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
}

type LogLine struct {
	Seq     int64  `json:"seq"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type CompleteRequest struct {
	WorkerID        string          `json:"worker_id"`
	RunUUID         string          `json:"run_uuid"`
	LeaseGeneration int64           `json:"lease_generation"`
	Status          string          `json:"status"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Logs            []LogLine       `json:"logs,omitempty"`
	DroppedLogs     int             `json:"dropped_logs,omitempty"`
}

type CancelCheckRequest struct {
	RunUUID         string `json:"run_uuid"`
	LeaseGeneration int64  `json:"lease_generation"`
}

type CancelCheckResponse struct {
	Cancelled bool `json:"cancelled"`
}
