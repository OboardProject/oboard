package model

// AuthorizationLease renews only the exact credentials installed by signed
// desired state. An absent key is denied; deadlines are absolute logical time.
//
// Revision is semantic and per server: it advances only when the grant set or
// a business boundary changes. Sequence orders renewals within a revision.
// Digest identifies the grant set independent of renewal deadlines. ExpiresAt
// is the absolute end of the whole lease. Denied lists credential keys the
// data plane must refuse even if an older, still-unexpired lease granted them.
type AuthorizationLease struct {
	Revision  int64             `json:"revision"`
	Sequence  int64             `json:"sequence,omitempty"`
	Digest    string            `json:"digest,omitempty"`
	IssuedAt  string            `json:"issued_at"`
	ExpiresAt string            `json:"expires_at,omitempty"`
	Grants    map[string]string `json:"grants"`
	Denied    []string          `json:"denied,omitempty"`
}

const (
	// AgentCapabilityAuthorizationLease is advertised by kernels that enforce
	// the lease through RateLimitTracker.
	AgentCapabilityAuthorizationLease = "authorization_lease_v1"
	// AgentCapabilityAuthorizationControl is advertised by Agents that accept
	// the signed authorization_update control message, poll
	// GET /api/v1/agent/authorization, and answer with authorization_ack.
	AgentCapabilityAuthorizationControl = "authorization_control_v1"
)

// AuthorizationEnvelope is the signed carrier for a lease on every transport
// (WebSocket control message, HTTP pull, traffic response). The signature is
// HMAC-SHA256 over the canonical string
//
//	authz_v1\n<server_id>\n<message_id>\n<revision>\n<sequence>\n<issued_at>\n<expires_at>\n<sha256(lease_json)>
//
// keyed with the Agent token hash, so an Agent can verify it with the same
// material it already holds for task signatures.
type AuthorizationEnvelope struct {
	Type      string `json:"type,omitempty"`
	MessageID string `json:"message_id"`
	ServerID  int64  `json:"server_id"`
	// LeaseJSON is the exact encoded AuthorizationLease the signature covers.
	// Receivers verify the signature over these bytes before decoding.
	LeaseJSON string `json:"lease_json"`
	Signature string `json:"signature"`
}

const (
	AgentControlAuthorizationUpdate = "authorization_update"
	AgentControlAuthorizationAck    = "authorization_ack"
)

// AuthorizationAck is the Agent's report that an envelope was applied.
type AuthorizationAck struct {
	Type      string                        `json:"type,omitempty"`
	MessageID string                        `json:"message_id"`
	Revision  int64                         `json:"revision"`
	Sequence  int64                         `json:"sequence"`
	Digest    string                        `json:"digest,omitempty"`
	Confirmed bool                          `json:"confirmed"`
	BootID    string                        `json:"boot_id,omitempty"`
	Runtimes  map[string]string             `json:"runtimes,omitempty"`
	Error     string                        `json:"error,omitempty"`
	Applied   *AuthorizationAppliedSnapshot `json:"applied,omitempty"`
}

// AuthorizationAppliedSnapshot is the opaque confirmation metadata the Agent
// also reports in health reports and pull requests.
type AuthorizationAppliedSnapshot struct {
	Revision int64  `json:"revision"`
	Sequence int64  `json:"sequence"`
	Digest   string `json:"digest,omitempty"`
	BootID   string `json:"boot_id,omitempty"`
}
