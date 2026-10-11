package model

const ProxyDSCPCapability = "proxy_dscp_control_v1"
const ProxyDSCPKernelCapability = "proxy_dscp_socket_v1"
const AgentTaskTypeProxyDSCP = "apply_proxy_dscp"

type ProxyDSCPPolicy struct {
	Enabled  bool  `json:"enabled"`
	Revision int64 `json:"revision"`
}
type ProxyDSCPReport struct {
	ProxyDSCPPolicy
	State    string   `json:"state"`
	Value    int      `json:"value"`
	Marked   uint64   `json:"marked_sockets"`
	Errors   uint64   `json:"socket_errors"`
	Error    string   `json:"error,omitempty"`
	Excluded []string `json:"excluded"`
}
