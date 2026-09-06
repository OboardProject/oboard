package model

// AuthorizationLease renews only the exact credentials installed by signed
// desired state. An absent key is denied; deadlines are absolute logical time.
type AuthorizationLease struct {
	Revision int64             `json:"revision"`
	IssuedAt string            `json:"issued_at"`
	Grants   map[string]string `json:"grants"`
}

const AgentCapabilityAuthorizationLease = "authorization_lease_v1"
