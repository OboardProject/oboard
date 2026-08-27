package model

import "time"

const (
	RemoteAccessModeStandard = "standard"
	RemoteAccessModeHardened = "hardened"

	PrivilegeRemoteOperations = "remote_operations"
	PrivilegeRemoteExec       = "remote_exec"
	PrivilegeRemoteShell      = "remote_shell"

	ApprovalPolicyPrivilegedGrant = "privileged_grant"

	RemoteAccessCapabilityTerminal         = "remote_terminal_v1"
	RemoteAccessCapabilityTerminalLoginEnv = "terminal_login_env_v1"
	RemoteAccessCapabilityExec             = "remote_exec_v1"
	RemoteAccessCapabilityLocalGate        = "remote_access_local_gate_v1"

	RemoteExecOriginMCP   = "mcp"
	RemoteExecOriginPanel = "panel"
	RemoteExecModeArgv    = "argv"
	RemoteExecModeShell   = "shell"

	RemoteOperationSystemInfo     = "system_info"
	RemoteOperationNetworkInfo    = "network_info"
	RemoteOperationDiskUsage      = "disk_usage"
	RemoteOperationListeners      = "listeners"
	RemoteOperationServiceStatus  = "service_status"
	RemoteOperationServiceRestart = "service_restart"
	RemoteOperationLogs           = "logs"
	RemoteOperationDiagnostics    = "diagnostics"

	StepUpPurposeRemoteTerminal     = "remote_terminal"
	StepUpPurposeGrantMCPExec       = "grant_mcp_exec"
	StepUpPurposeGrantMCPRawShell   = "grant_mcp_raw_shell"
	StepUpPurposeGrantMCPOperations = "grant_mcp_operations"
	StepUpPurposePrivilegedGrant    = "privileged_grant"

	RemoteAccessAuditTerminalOpen           = "terminal_open"
	RemoteAccessAuditTerminalClose          = "terminal_close"
	RemoteAccessAuditTerminalDenied         = "terminal_denied"
	RemoteAccessAuditMCPRemoteOperation     = "mcp_remote_operation"
	RemoteAccessAuditMCPExec                = "mcp_exec"
	RemoteAccessAuditMCPShell               = "mcp_shell"
	RemoteAccessAuditMCPExecDenied          = "mcp_exec_denied"
	RemoteAccessAuditPrivilegedGrantCreated = "privileged_grant_created"
	RemoteAccessAuditPrivilegedGrantUpdated = "privileged_grant_updated"
	RemoteAccessAuditPrivilegedGrantRevoked = "privileged_grant_revoked"
	RemoteAccessAuditAgentLocalGateDenied   = "agent_local_gate_denied"
)

type RemoteAccessReport struct {
	Capabilities []string               `json:"capabilities,omitempty"`
	LocalMode    string                 `json:"local_mode,omitempty"`
	LocalAllow   RemoteAccessLocalAllow `json:"local_allow,omitempty"`
}

type RemoteAccessLocalAllow struct {
	RemoteTerminal      bool `json:"remote_terminal"`
	MCPRemoteOperations bool `json:"mcp_remote_operations"`
	MCPStructuredExec   bool `json:"mcp_structured_exec"`
	MCPRawShell         bool `json:"mcp_raw_shell"`
}

type ServerRemoteAccessPolicy struct {
	ServerID                   int64     `json:"server_id"`
	RemoteTerminalEnabled      bool      `json:"remote_terminal_enabled"`
	MCPRemoteOperationsEnabled bool      `json:"mcp_remote_operations_enabled"`
	MCPStructuredExecEnabled   bool      `json:"mcp_structured_exec_enabled"`
	MCPRawShellEnabled         bool      `json:"mcp_raw_shell_enabled"`
	CreatedAt                  time.Time `json:"created_at"`
	UpdatedAt                  time.Time `json:"updated_at"`
}

type ServerRemoteAccessStatus struct {
	ServerID     int64                  `json:"server_id"`
	Capabilities []string               `json:"capabilities"`
	LocalMode    string                 `json:"local_mode"`
	LocalAllow   RemoteAccessLocalAllow `json:"local_allow"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

type MCPPrivilegedGrant struct {
	ID                   int64      `json:"id"`
	OAuthGrantID         string     `json:"oauth_grant_id"`
	OAuthClientID        string     `json:"oauth_client_id"`
	AuthorizedUserID     int64      `json:"authorized_user_id"`
	Capabilities         []string   `json:"capabilities"`
	ResourceBoundaryJSON []byte     `json:"resource_boundary_json"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
	CreatedByUserID      int64      `json:"created_by_user_id"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	LastStepUpAt         *time.Time `json:"last_step_up_at,omitempty"`
	Revision             int64      `json:"revision"`
}

func (g MCPPrivilegedGrant) Active(now time.Time) bool {
	if g.RevokedAt != nil && !g.RevokedAt.IsZero() {
		return false
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.IsZero() && !g.ExpiresAt.After(now) {
		return false
	}
	return true
}

func (g MCPPrivilegedGrant) HasCapability(name string) bool {
	for _, item := range g.Capabilities {
		if item == name {
			return true
		}
	}
	return false
}

type RemoteAccessAuditEvent struct {
	ID            int64      `json:"id"`
	EventType     string     `json:"event_type"`
	ActorType     string     `json:"actor_type"`
	ActorUserID   *int64     `json:"actor_user_id,omitempty"`
	OAuthClientID string     `json:"oauth_client_id,omitempty"`
	OAuthGrantID  string     `json:"oauth_grant_id,omitempty"`
	ServerID      *int64     `json:"server_id,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	RequestID     string     `json:"request_id,omitempty"`
	Capability    string     `json:"capability,omitempty"`
	Result        string     `json:"result"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	DurationMS    int64      `json:"duration_ms,omitempty"`
	SourceIP      string     `json:"source_ip,omitempty"`
	MetadataJSON  []byte     `json:"metadata_json,omitempty"`
}

type RemoteExecCommand struct {
	Mode  string   `json:"mode"`
	Argv  []string `json:"argv,omitempty"`
	Shell string   `json:"shell,omitempty"`
	Cwd   string   `json:"cwd,omitempty"`
}

type RemoteExecLimits struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	StdoutBytes    int `json:"stdout_bytes,omitempty"`
	StderrBytes    int `json:"stderr_bytes,omitempty"`
}

type RemoteExecTaskPayload struct {
	RequestID string            `json:"request_id"`
	Origin    string            `json:"origin"`
	Privilege string            `json:"privilege"`
	ActorRef  string            `json:"actor_ref,omitempty"`
	GrantID   int64             `json:"grant_id,omitempty"`
	ServerID  int64             `json:"server_id"`
	IssuedAt  time.Time         `json:"issued_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	Command   RemoteExecCommand `json:"command"`
	Limits    RemoteExecLimits  `json:"limits"`
}

type RemoteOperationTaskPayload struct {
	RequestID string    `json:"request_id"`
	Origin    string    `json:"origin"`
	Kind      string    `json:"kind"`
	ActorRef  string    `json:"actor_ref,omitempty"`
	GrantID   int64     `json:"grant_id,omitempty"`
	ServerID  int64     `json:"server_id"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Service   string    `json:"service,omitempty"`
	Lines     int       `json:"lines,omitempty"`
}

type RemoteExecResultMeta struct {
	ExitCode        int    `json:"exit_code"`
	DurationMS      int64  `json:"duration_ms"`
	StdoutBytes     int    `json:"stdout_bytes"`
	StderrBytes     int    `json:"stderr_bytes"`
	StdoutSHA256    string `json:"stdout_sha256"`
	StderrSHA256    string `json:"stderr_sha256"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	Cancelled       bool   `json:"cancelled,omitempty"`
	Error           string `json:"error,omitempty"`
}

type RemoteExecWireResult struct {
	RemoteExecResultMeta
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

type RemoteExecTransientResult struct {
	RequestID string
	TaskID    int64
	Meta      RemoteExecResultMeta
	Stdout    string
	Stderr    string
	Finished  time.Time
}

type InteractivePrepareEnvelope struct {
	Type             string `json:"type"`
	SignatureVersion int    `json:"signature_version"`
	ServerID         int64  `json:"server_id"`
	SessionID        string `json:"session_id"`
	Nonce            string `json:"nonce"`
	IssuedAt         string `json:"issued_at"`
	ExpiresAt        string `json:"expires_at"`
	Kind             string `json:"kind"`
	Cols             int    `json:"cols"`
	Rows             int    `json:"rows"`
	Mode             string `json:"mode,omitempty"`
	Signature        string `json:"signature,omitempty"`
}

type StepUpChallenge struct {
	ID                  string     `json:"id"`
	UserID              int64      `json:"user_id"`
	SessionID           string     `json:"session_id"`
	SessionVersion      int64      `json:"session_version"`
	Purpose             string     `json:"purpose"`
	ResourceType        string     `json:"resource_type"`
	ResourceID          string     `json:"resource_id"`
	Nonce               string     `json:"nonce"`
	WebAuthnSessionJSON []byte     `json:"-"`
	ConsumedAt          *time.Time `json:"consumed_at,omitempty"`
	ExpiresAt           time.Time  `json:"expires_at"`
	CreatedAt           time.Time  `json:"created_at"`
}
