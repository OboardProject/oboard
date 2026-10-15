package model

import "time"

// VPNGate selection modes.
const (
	VPNGateSelectLatency = "latency"
	VPNGateSelectQuality = "quality"

	VPNGateSwitchOnFailure = "on_failure"
	VPNGateSwitchImprove   = "improve"
)

// VPNGatePool is an operator-defined exit policy. Members are selected per
// exit server from the shared VPNGate catalog and delivered at runtime.
type VPNGatePool struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Countries       []string  `json:"countries"`
	OperatorASNs    []string  `json:"operator_asns"`
	Network         string    `json:"network"`
	SelectMode      string    `json:"select_mode"`
	MaxRTTMS        int       `json:"max_rtt_ms"`
	MinQualityScore int       `json:"min_quality_score"`
	HotStandby      int       `json:"hot_standby"`
	PoolSize        int       `json:"pool_size"`
	SwitchPolicy    string    `json:"switch_policy"`
	RefreshMinutes  int       `json:"refresh_minutes"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// VPNGateNode is one server from the VPNGate catalog. Spec holds the
// sanitized OpenVPN parameters; nothing from the downloaded .ovpn is kept
// verbatim.
type VPNGateNode struct {
	ID          int64       `json:"id"`
	NodeKey     string      `json:"node_key"`
	HostName    string      `json:"host_name"`
	IP          string      `json:"ip"`
	Port        int         `json:"port"`
	Network     string      `json:"network"`
	CountryCode string      `json:"country_code"`
	CountryName string      `json:"country_name"`
	Score       int64       `json:"score"`
	PingMS      int         `json:"ping_ms"`
	SpeedBPS    int64       `json:"speed_bps"`
	Sessions    int         `json:"sessions"`
	UptimeMS    int64       `json:"uptime_ms"`
	Operator    string      `json:"operator"`
	Spec        OpenVPNSpec `json:"-"`
	FirstSeenAt time.Time   `json:"first_seen_at"`
	LastSeenAt  time.Time   `json:"last_seen_at"`
}

// VPNGateIPQuality caches the reputation of one public IP.
type VPNGateIPQuality struct {
	IP           string    `json:"ip"`
	ASN          string    `json:"asn"`
	Organization string    `json:"organization"`
	Country      string    `json:"country"`
	Hosting      *bool     `json:"hosting,omitempty"`
	VPN          *bool     `json:"vpn,omitempty"`
	Tor          *bool     `json:"tor,omitempty"`
	Risk         *int      `json:"risk,omitempty"`
	Abuse        *int      `json:"abuse,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
}

// VPNGateCandidate is the scored outcome of one measured server for one
// (pool, exit server) pair.
type VPNGateCandidate struct {
	NodeID       int64   `json:"node_id"`
	MemberID     string  `json:"member_id"`
	IP           string  `json:"ip"`
	Port         int     `json:"port"`
	Network      string  `json:"network"`
	CountryCode  string  `json:"country_code"`
	Status       string  `json:"status"`
	Stage        string  `json:"stage,omitempty"`
	Error        string  `json:"error,omitempty"`
	EgressIP     string  `json:"egress_ip,omitempty"`
	ASN          string  `json:"asn,omitempty"`
	Organization string  `json:"organization,omitempty"`
	HandshakeMS  int64   `json:"handshake_ms,omitempty"`
	RTTMS        int64   `json:"rtt_ms,omitempty"`
	TTFBMS       int64   `json:"ttfb_ms,omitempty"`
	Quality      int     `json:"quality"`
	Eligible     bool    `json:"eligible"`
	Reason       string  `json:"reason,omitempty"`
	SuccessRate  float64 `json:"success_rate"`
	Selected     bool    `json:"selected"`
}

// VPNGatePoolServer is the selection state of a pool on one exit server.
type VPNGatePoolServer struct {
	PoolID         int64                `json:"pool_id"`
	ServerID       int64                `json:"server_id"`
	Revision       int64                `json:"revision"`
	ContentDigest  string               `json:"content_digest"`
	Members        []VPNGateCandidate   `json:"members"`
	Candidates     []VPNGateCandidate   `json:"candidates"`
	Status         *VPNGatePoolStatus   `json:"status,omitempty"`
	ProbeTaskID    int64                `json:"probe_task_id,omitempty"`
	ProbeRequestID string               `json:"probe_request_id,omitempty"`
	ApplyTaskID    int64                `json:"apply_task_id,omitempty"`
	AppliedAt      *time.Time           `json:"applied_at,omitempty"`
	LastProbeAt    *time.Time           `json:"last_probe_at,omitempty"`
	NextRefreshAt  *time.Time           `json:"next_refresh_at,omitempty"`
	StatusAt       *time.Time           `json:"status_at,omitempty"`
	LastError      string               `json:"last_error,omitempty"`
	Improvements   int                  `json:"improvements"`
	History        []VPNGateSwitchEvent `json:"history"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

// VPNGateSwitchEvent records one membership change for the operator.
type VPNGateSwitchEvent struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
	From   string    `json:"from,omitempty"`
	To     string    `json:"to,omitempty"`
}

// OpenVPNSpec is the wire description of one OpenVPN server for the
// vpngate-pool kernel outbound.
type OpenVPNSpec struct {
	Server     string   `json:"server"`
	ServerPort uint16   `json:"server_port"`
	Network    string   `json:"network"`
	Cipher     string   `json:"cipher"`
	Auth       string   `json:"auth"`
	CA         string   `json:"ca"`
	Cert       string   `json:"cert"`
	Key        string   `json:"key"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	MTU        int      `json:"mtu,omitempty"`
	Resolvers  []string `json:"resolvers,omitempty"`
}

type VPNGatePoolMember struct {
	MemberID string `json:"member_id"`
	OpenVPNSpec
}

type VPNGatePoolPlan struct {
	PoolID        int64               `json:"pool_id"`
	Tag           string              `json:"tag"`
	Revision      int64               `json:"revision"`
	ContentDigest string              `json:"content_digest"`
	HotStandby    int                 `json:"hot_standby"`
	Members       []VPNGatePoolMember `json:"members"`
}

type VPNGateProbeCandidate struct {
	ProbeID string `json:"probe_id"`
	OpenVPNSpec
}

type VPNGateProbePlan struct {
	PoolID     int64                   `json:"pool_id"`
	Tag        string                  `json:"tag"`
	RequestID  string                  `json:"request_id"`
	Candidates []VPNGateProbeCandidate `json:"candidates"`
}

type VPNGateProbeResult struct {
	ProbeID     string  `json:"probe_id"`
	Status      string  `json:"status"`
	Stage       string  `json:"stage,omitempty"`
	Error       string  `json:"error,omitempty"`
	HandshakeMS int64   `json:"handshake_ms,omitempty"`
	RTTMS       []int64 `json:"rtt_ms,omitempty"`
	TTFBMS      int64   `json:"ttfb_ms,omitempty"`
	EgressIP    string  `json:"egress_ip,omitempty"`
}

type VPNGateProbeReport struct {
	PoolID    int64                `json:"pool_id"`
	RequestID string               `json:"request_id"`
	Results   []VPNGateProbeResult `json:"results"`
}

type VPNGateMemberStatus struct {
	MemberID    string `json:"member_id"`
	State       string `json:"state"`
	Active      bool   `json:"active"`
	Failures    int    `json:"failures"`
	LastError   string `json:"last_error,omitempty"`
	HandshakeMS int64  `json:"handshake_ms,omitempty"`
	RTTMS       int64  `json:"rtt_ms,omitempty"`
	LastChange  string `json:"last_change,omitempty"`
}

type VPNGatePoolStatus struct {
	Tag           string                `json:"tag"`
	PoolID        int64                 `json:"pool_id"`
	Revision      int64                 `json:"revision"`
	ContentDigest string                `json:"content_digest"`
	ActiveMember  string                `json:"active_member,omitempty"`
	Healthy       int                   `json:"healthy"`
	Members       []VPNGateMemberStatus `json:"members"`
}
