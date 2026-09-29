package model

import "time"

// Plugin network diagnostics are structured, signed Agent tasks. Each task
// type is one fixed operation; none of them carries a command, binary path,
// flag list or environment. The Agent executes them with native Go sockets.
const (
	AgentTaskTypeNetworkPing  = "probe_network_ping"
	AgentTaskTypeNetworkTrace = "probe_network_trace"
	AgentTaskTypeNetworkTCP   = "probe_network_tcp"
	AgentTaskTypeNetworkDNS   = "probe_network_dns"
	AgentTaskTypeNetworkHTTP  = "probe_network_http"

	// AgentCapabilityNetworkDiagnostics is advertised by Agents that accept the
	// probe_network_* tasks. Per-operation capabilities say which ones this
	// platform actually implements.
	AgentCapabilityNetworkDiagnostics = "network_diagnostics_v1"
	AgentCapabilityNetworkPing        = "network_ping_v1"
	AgentCapabilityNetworkTraceICMP   = "network_trace_icmp_v1"
	AgentCapabilityNetworkTraceUDP    = "network_trace_udp_v1"
	AgentCapabilityNetworkTraceTCP    = "network_trace_tcp_v1"
	AgentCapabilityNetworkTCPProbe    = "network_tcp_probe_v1"
	AgentCapabilityNetworkDNSLookup   = "network_dns_lookup_v1"
	AgentCapabilityNetworkHTTPProbe   = "network_http_probe_v1"

	NetworkDiagnosticProtocolVersion = 1
	NetworkDiagnosticOriginPlugin    = "plugin"

	NetworkFamilyAuto = "auto"
	NetworkFamilyIPv4 = "ipv4"
	NetworkFamilyIPv6 = "ipv6"

	TraceModeICMP = "icmp"
	TraceModeUDP  = "udp"
	TraceModeTCP  = "tcp"
)

// Bounds shared by Controller validation and Agent validation.
const (
	PingMaxCount          = 10
	PingMinIntervalMS     = 200
	PingMaxIntervalMS     = 2000
	PingMinTimeoutMS      = 100
	PingMaxTimeoutMS      = 5000
	PingMaxPacketSize     = 1400
	TraceMaxHops          = 30
	TraceMaxQueriesPerHop = 3
	TraceMinHopTimeoutMS  = 100
	TraceMaxHopTimeoutMS  = 3000
	TraceMaxDurationMS    = 30000
	TCPProbeMaxTimeoutMS  = 10000
	DNSLookupMaxTimeoutMS = 10000
	HTTPProbeMaxTimeoutMS = 15000
	HTTPProbeMaxRedirects = 3
	NetworkTargetMaxBytes = 253
	NetworkURLMaxBytes    = 2048
	// NetworkDiagnosticMaxDurationMS bounds how long any one diagnostic task
	// can occupy an Agent.
	NetworkDiagnosticMaxDurationMS = 35000
)

// NetworkDiagnosticEnvelope is embedded in every probe_network_* payload.
type NetworkDiagnosticEnvelope struct {
	ProtocolVersion int       `json:"protocol_version"`
	OperationID     string    `json:"operation_id"`
	Origin          string    `json:"origin"`
	RunID           string    `json:"run_id"`
	PluginID        string    `json:"plugin_id"`
	InstanceID      int64     `json:"instance_id"`
	IssuedAt        time.Time `json:"issued_at"`
	// ExpiresAt is the latest time the Agent may start the operation. An
	// operation that could not start in time is refused, never queued.
	ExpiresAt time.Time `json:"expires_at"`
}

type NetworkPingTaskPayload struct {
	NetworkDiagnosticEnvelope
	Target     string `json:"target"`
	IPFamily   string `json:"ip_family"`
	Count      int    `json:"count"`
	IntervalMS int    `json:"interval_ms"`
	TimeoutMS  int    `json:"timeout_ms"`
	PacketSize int    `json:"packet_size"`
}

type NetworkTraceTaskPayload struct {
	NetworkDiagnosticEnvelope
	Target          string `json:"target"`
	IPFamily        string `json:"ip_family"`
	Mode            string `json:"mode"`
	Port            int    `json:"port,omitempty"`
	MaxHops         int    `json:"max_hops"`
	QueriesPerHop   int    `json:"queries_per_hop"`
	PerHopTimeoutMS int    `json:"per_hop_timeout_ms"`
}

type NetworkTCPProbeTaskPayload struct {
	NetworkDiagnosticEnvelope
	Host      string `json:"host"`
	Port      int    `json:"port"`
	IPFamily  string `json:"ip_family"`
	TimeoutMS int    `json:"timeout_ms"`
}

type NetworkDNSLookupTaskPayload struct {
	NetworkDiagnosticEnvelope
	Name        string   `json:"name"`
	RecordTypes []string `json:"record_types"`
	TimeoutMS   int      `json:"timeout_ms"`
}

type NetworkHTTPProbeTaskPayload struct {
	NetworkDiagnosticEnvelope
	URL             string `json:"url"`
	Method          string `json:"method"`
	IPFamily        string `json:"ip_family"`
	TimeoutMS       int    `json:"timeout_ms"`
	FollowRedirects bool   `json:"follow_redirects"`
}

// Results. Every result carries operation_id so the Controller can bind it to
// the task it issued. Failure results use {"code","error"}.
type NetworkPingSample struct {
	Seq     int     `json:"seq"`
	RTTMS   float64 `json:"rtt_ms,omitempty"`
	Timeout bool    `json:"timeout"`
}

type NetworkPingResult struct {
	OperationID string              `json:"operation_id"`
	Target      string              `json:"target"`
	ResolvedIP  string              `json:"resolved_ip"`
	IPFamily    string              `json:"ip_family"`
	Sent        int                 `json:"sent"`
	Received    int                 `json:"received"`
	LossPercent float64             `json:"loss_percent"`
	MinRTTMS    float64             `json:"min_rtt_ms"`
	AvgRTTMS    float64             `json:"avg_rtt_ms"`
	MaxRTTMS    float64             `json:"max_rtt_ms"`
	Samples     []NetworkPingSample `json:"samples"`
}

type NetworkTraceProbe struct {
	Address string  `json:"address,omitempty"`
	RTTMS   float64 `json:"rtt_ms,omitempty"`
	Timeout bool    `json:"timeout"`
}

type NetworkTraceHop struct {
	Hop       int                 `json:"hop"`
	Addresses []string            `json:"addresses"`
	Probes    []NetworkTraceProbe `json:"probes"`
}

type NetworkTraceResult struct {
	OperationID string            `json:"operation_id"`
	Target      string            `json:"target"`
	ResolvedIP  string            `json:"resolved_ip"`
	IPFamily    string            `json:"ip_family"`
	Mode        string            `json:"mode"`
	Port        int               `json:"port,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	DurationMS  int64             `json:"duration_ms"`
	Reached     bool              `json:"reached"`
	Truncated   bool              `json:"truncated"`
	Hops        []NetworkTraceHop `json:"hops"`
}

type NetworkTCPProbeResult struct {
	OperationID string  `json:"operation_id"`
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	ResolvedIP  string  `json:"resolved_ip,omitempty"`
	IPFamily    string  `json:"ip_family,omitempty"`
	Connected   bool    `json:"connected"`
	ConnectMS   float64 `json:"connect_ms,omitempty"`
	ErrorClass  string  `json:"error_class,omitempty"`
}

type NetworkDNSRecord struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

type NetworkDNSLookupResult struct {
	OperationID string             `json:"operation_id"`
	Name        string             `json:"name"`
	Records     []NetworkDNSRecord `json:"records"`
	DurationMS  float64            `json:"duration_ms"`
	ErrorClass  string             `json:"error_class,omitempty"`
}

type NetworkHTTPTimings struct {
	DNSMS     float64 `json:"dns_ms"`
	ConnectMS float64 `json:"connect_ms"`
	TLSMS     float64 `json:"tls_ms"`
	TTFBMS    float64 `json:"ttfb_ms"`
	TotalMS   float64 `json:"total_ms"`
}

type NetworkHTTPTLS struct {
	Version     string    `json:"version"`
	ServerName  string    `json:"server_name"`
	CertExpires time.Time `json:"cert_not_after"`
}

type NetworkHTTPProbeResult struct {
	OperationID string             `json:"operation_id"`
	URL         string             `json:"url"`
	FinalURL    string             `json:"final_url,omitempty"`
	ResolvedIP  string             `json:"resolved_ip,omitempty"`
	StatusCode  int                `json:"status_code,omitempty"`
	Redirects   int                `json:"redirects"`
	BodyBytes   int64              `json:"body_bytes"`
	Timings     NetworkHTTPTimings `json:"timings"`
	TLS         *NetworkHTTPTLS    `json:"tls,omitempty"`
	ErrorClass  string             `json:"error_class,omitempty"`
}

// Diagnostic failure codes reported by the Agent in {"code": ...}.
const (
	NetworkDiagnosticCodeInvalidInput     = "invalid_input"
	NetworkDiagnosticCodeExpired          = "expired"
	NetworkDiagnosticCodeLocalGateDenied  = "agent_local_gate_denied"
	NetworkDiagnosticCodeUnsupported      = "unsupported"
	NetworkDiagnosticCodeTargetNotAllowed = "target_not_allowed"
	NetworkDiagnosticCodeResolveFailed    = "resolve_failed"
	NetworkDiagnosticCodeFailed           = "failed"
)
