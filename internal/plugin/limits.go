package plugin

import "time"

// Host ceilings. A manifest may only lower them; the global runtime setting
// may further lower the wall-time ceiling.
const (
	DefaultRunTimeout        = 30 * time.Second
	MaxRunTimeout            = 120 * time.Second
	DefaultRunnerMemoryMiB   = 64
	MaxRunnerMemoryMiB       = 128
	DefaultSDKCalls          = 200
	MaxSDKCalls              = 1000
	DefaultHTTPRequests      = 20
	MaxHTTPRequests          = 100
	DefaultAgentOperations   = 5
	MaxAgentOperations       = 20
	DefaultLogBytes          = 64 << 10
	MaxLogBytes              = 256 << 10
	MaxLogLines              = 1000
	MaxLogLineBytes          = 2048
	MaxResultBytes           = 64 << 10
	MaxStateKeys             = 128
	MaxStateValueBytes       = 16 << 10
	MaxStateTotalBytes       = 512 << 10
	MaxStateKeyBytes         = 128
	DefaultMaxConcurrentRuns = 2
	MaxConcurrentRunsCeiling = 8
	AutoPauseFailureStreak   = 5
	DegradedFailureStreak    = 2
	MinScheduleInterval      = time.Minute
	MaxScheduleInterval      = 30 * 24 * time.Hour
	LeaseDuration            = 3 * time.Minute
)

// Limits are the effective per-run budgets sent to the worker and enforced by
// the gateway.
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

func (l Limits) Timeout() time.Duration { return time.Duration(l.TimeoutMS) * time.Millisecond }

// EffectiveLimits validates declared limits and returns host-bounded budgets.
// systemTimeout <= 0 means the host ceiling.
func EffectiveLimits(declared DeclaredLimits, systemTimeout time.Duration) (Limits, error) {
	if systemTimeout <= 0 || systemTimeout > MaxRunTimeout {
		systemTimeout = MaxRunTimeout
	}
	out := Limits{
		TimeoutMS:       DefaultRunTimeout.Milliseconds(),
		MemoryMiB:       DefaultRunnerMemoryMiB,
		SDKCalls:        DefaultSDKCalls,
		HTTPRequests:    DefaultHTTPRequests,
		AgentOperations: DefaultAgentOperations,
		LogBytes:        DefaultLogBytes,
		LogLines:        MaxLogLines,
		LogLineBytes:    MaxLogLineBytes,
		ResultBytes:     MaxResultBytes,
	}
	if declared.Timeout != "" {
		d, _, err := ParseDuration(declared.Timeout)
		if err != nil || d < time.Second || d > MaxRunTimeout {
			return out, FailField(CodeInvalidManifest, "limits.timeout", "timeout must be between 1s and 2m")
		}
		out.TimeoutMS = d.Milliseconds()
	}
	if out.Timeout() > systemTimeout {
		out.TimeoutMS = systemTimeout.Milliseconds()
	}
	bound := func(field string, value, ceiling int, target *int) error {
		if value < 0 || value > ceiling {
			return FailField(CodeInvalidManifest, "limits."+field, "limit exceeds the host ceiling")
		}
		if value > 0 {
			*target = value
		}
		return nil
	}
	if err := bound("memory_mib", declared.MemoryMiB, MaxRunnerMemoryMiB, &out.MemoryMiB); err != nil {
		return out, err
	}
	if declared.MemoryMiB > 0 && declared.MemoryMiB < 16 {
		return out, FailField(CodeInvalidManifest, "limits.memory_mib", "memory_mib must be at least 16")
	}
	if err := bound("sdk_calls", declared.SDKCalls, MaxSDKCalls, &out.SDKCalls); err != nil {
		return out, err
	}
	if err := bound("http_requests", declared.HTTPRequests, MaxHTTPRequests, &out.HTTPRequests); err != nil {
		return out, err
	}
	if err := bound("agent_operations", declared.AgentOperations, MaxAgentOperations, &out.AgentOperations); err != nil {
		return out, err
	}
	if err := bound("log_bytes", declared.LogBytes, MaxLogBytes, &out.LogBytes); err != nil {
		return out, err
	}
	return out, nil
}
