package model

const (
	AgentCapabilityRuntimeUsersVLESS       = "runtime_users:vless"
	AgentCapabilityRuntimeUsersHysteria2   = "runtime_users:hysteria2"
	AgentCapabilityRuntimeUsersShadowsocks = "runtime_users:shadowsocks-multi"
	AgentCapabilityRuntimeUsersSnell       = "runtime_users:snell-multi"
)

type UsersInstallChunk struct {
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	SHA256 string `json:"sha256"`
}

type UsersInstallRequest struct {
	Scope         []string            `json:"scope"`
	UsersRevision int64               `json:"users_revision"`
	UsersDigest   string              `json:"users_digest"`
	Mode          string              `json:"mode"`
	BaseRevision  int64               `json:"base_revision,omitempty"`
	Chunk         *UsersInstallChunk  `json:"chunk,omitempty"`
	Entries       []UsersInstallEntry `json:"entries"`

	// ContentDigest is the identity the revision is allocated against: the
	// snapshot without the lease-accounting counters the traffic lane owns.
	//
	// UsersDigest covers those counters, because the kernel recomputes it over
	// the bytes it is handed. They move on every accepted traffic report, so one
	// revision legitimately describes several delivered payloads, and a gate
	// built on UsersDigest alone reads that refresh as a conflict and stops the
	// lane for good. This field is what makes the revision gate decidable: equal
	// revision plus equal content is the same desired state, whatever the
	// counters say.
	ContentDigest string `json:"content_digest,omitempty"`
}

type UsersInstallEntry struct {
	InboundTag       string             `json:"inbound_tag"`
	AuthUser         string             `json:"auth_user"`
	Credential       UsersCredential    `json:"credential"`
	AuthorizationKey string             `json:"authorization_key"`
	Identity         UsersIdentity      `json:"identity"`
	RouteOutbound    string             `json:"route_outbound"`
	Policy           UsersRuntimePolicy `json:"policy"`
}

type UsersCredential struct {
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
	UserKey  string `json:"userkey,omitempty"`
	PSK      string `json:"psk,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

type UsersIdentity struct {
	UserID           int64  `json:"user_id,omitempty"`
	InboundID        int64  `json:"inbound_id,omitempty"`
	PathID           int64  `json:"path_id,omitempty"`
	DeviceIDHash     string `json:"device_id_hash,omitempty"`
	CredentialEpoch  int64  `json:"credential_epoch,omitempty"`
	CredentialStatus string `json:"credential_status,omitempty"`
}

type UsersRuntimePolicy struct {
	AuthorizationKey  string `json:"authorization_key,omitempty"`
	UserID            int64  `json:"user_id,omitempty"`
	InboundID         int64  `json:"inbound_id,omitempty"`
	PathID            int64  `json:"path_id,omitempty"`
	DeviceIDHash      string `json:"device_id_hash,omitempty"`
	CredentialEpoch   int64  `json:"credential_epoch,omitempty"`
	CredentialStatus  string `json:"credential_status,omitempty"`
	Billable          bool   `json:"billable"`
	SpeedLimitMbps    int    `json:"speed_limit_mbps,omitempty"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes,omitempty"`
	UsedBaselineBytes int64  `json:"used_baseline_bytes,omitempty"`
	LeaseBytes        int64  `json:"lease_bytes,omitempty"`
	ResetLeaseBytes   int64  `json:"reset_lease_bytes,omitempty"`
	LeaseEnforced     bool   `json:"lease_enforced,omitempty"`
	PeriodKey         string `json:"period_key,omitempty"`
	PeriodStart       string `json:"period_start,omitempty"`
	PeriodEnd         string `json:"period_end,omitempty"`
	ResetMode         string `json:"reset_mode,omitempty"`
	ResetDay          int    `json:"reset_day,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	QuotaState        string `json:"quota_state,omitempty"`
}
