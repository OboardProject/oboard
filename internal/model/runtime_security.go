package model

import "time"

const AgentTaskTypeRuntimeSecurity = "runtime_security"
const RuntimeSecurityCapability = "runtime_security_v1"

type RuntimeSecurityRequest struct {
	Mode     string `json:"mode,omitempty"`
	Revision int64  `json:"revision,omitempty"`
}

type RuntimeSecurityCheck struct {
	ID        string    `json:"id"`
	Category  string    `json:"category"`
	Severity  string    `json:"severity"`
	Status    string    `json:"status"`
	Supported bool      `json:"supported"`
	CheckedAt time.Time `json:"checked_at"`
	Message   string    `json:"message"`
	Remedy    string    `json:"remedy"`
	AutoFix   bool      `json:"auto_fix"`
}

type RuntimeSecurityReport struct {
	Revision     int64                  `json:"revision"`
	DesiredMode  string                 `json:"desired_mode"`
	ActualMode   string                 `json:"actual_mode"`
	State        string                 `json:"state"`
	Platform     string                 `json:"platform"`
	Supported    bool                   `json:"supported"`
	AppliedAt    *time.Time             `json:"applied_at,omitempty"`
	CheckedAt    time.Time              `json:"checked_at"`
	ErrorCode    string                 `json:"error_code,omitempty"`
	Phase        string                 `json:"phase"`
	LocalPolicy  string                 `json:"local_policy"`
	Capabilities []string               `json:"capabilities"`
	Checks       []RuntimeSecurityCheck `json:"checks"`
}
