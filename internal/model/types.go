package model

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

type Protocol string

const (
	ProtocolVLESS  Protocol = "vless"
	ProtocolHY2    Protocol = "hy2"
	ProtocolAnyTLS Protocol = "anytls"
	ProtocolSS     Protocol = "shadowsocks"
	ProtocolSocks  Protocol = "socks"
	// ProtocolSSH is a managed, public-key-only SSH proxy entry. It is run by
	// the Agent rather than sing-box, so it is intentionally kept out of the
	// generic proxy protocol adapters.
	ProtocolSSH Protocol = "ssh"
)

type ServerStatus string

const (
	ServerUnknown  ServerStatus = "unknown"
	ServerOnline   ServerStatus = "online"
	ServerOffline  ServerStatus = "offline"
	ServerDegraded ServerStatus = "degraded"
)

type EntryIPMode string

const (
	EntryIPModeAuto   EntryIPMode = "auto"
	EntryIPModeIPv4   EntryIPMode = "ipv4"
	EntryIPModeIPv6   EntryIPMode = "ipv6"
	EntryIPModeCustom EntryIPMode = "custom"
)

type IPStack string

const (
	IPStackAuto       IPStack = "auto"
	IPStackIPv4Only   IPStack = "ipv4_only"
	IPStackIPv6Only   IPStack = "ipv6_only"
	IPStackDualStack  IPStack = "dual_stack"
	IPStackPreferIPv4 IPStack = "prefer_ipv4"
	IPStackPreferIPv6 IPStack = "prefer_ipv6"
)

type UDPInboundMode string

const (
	UDPInboundAllow UDPInboundMode = "allow"
	UDPInboundBlock UDPInboundMode = "block"
	UDPInboundUoT   UDPInboundMode = "uot"
)

type DNSTransport string

const (
	DNSTransportUDP DNSTransport = "udp"
	DNSTransportTCP DNSTransport = "tcp"
	DNSTransportDoT DNSTransport = "dot"
	DNSTransportDoH DNSTransport = "doh"
	DNSTransportDoQ DNSTransport = "doq"
)

type DNSListKind string

const (
	DNSListEncrypted DNSListKind = "encrypted"
	DNSListBootstrap DNSListKind = "bootstrap"
)

type DNSAutoTestMode string

const (
	DNSAutoTestNever      DNSAutoTestMode = "never"
	DNSAutoTestFirstApply DNSAutoTestMode = "first_apply"
	DNSAutoTestPeriodic   DNSAutoTestMode = "periodic"
	DNSAutoTestAlways     DNSAutoTestMode = "always"
)

type MTUMode string

const (
	MTUModeDisabled MTUMode = "disabled"
	MTUModeDetect   MTUMode = "detect"
	MTUModeApply    MTUMode = "apply"
)

type RouteAction string

const (
	RouteActionDirect    RouteAction = "direct"
	RouteActionBlock     RouteAction = "block"
	RouteActionOutbound  RouteAction = "outbound"
	RouteActionExternal  RouteAction = "external"
	RouteActionWARP      RouteAction = "warp"
	RouteActionInterface RouteAction = "interface"
)

type ExternalOutboundScope string

const (
	ExternalOutboundScopeGlobal ExternalOutboundScope = "global"
	ExternalOutboundScopeServer ExternalOutboundScope = "server"
)

type WARPStatus string

const (
	WARPStatusNeeded    WARPStatus = "needed"
	WARPStatusRequested WARPStatus = "requested"
	WARPStatusReady     WARPStatus = "ready"
	WARPStatusFailed    WARPStatus = "failed"
)

type User struct {
	ID                        int64      `json:"id"`
	Username                  string     `json:"username"`
	Nickname                  string     `json:"nickname"`
	PasswordHash              string     `json:"-"`
	SessionVersion            int64      `json:"-"`
	Role                      Role       `json:"role"`
	Status                    string     `json:"status"`
	ProxyUUID                 string     `json:"proxy_uuid"`
	ProxyPassword             string     `json:"proxy_password"`
	SpeedLimitMbps            int        `json:"speed_limit_mbps"`
	TrafficLimitBytes         int64      `json:"traffic_limit_bytes"`
	TrafficUsedBytes          int64      `json:"traffic_used_bytes"`
	TrafficResetMode          string     `json:"traffic_reset_mode"`
	TrafficResetDay           int        `json:"traffic_reset_day"`
	TrafficPeriodKey          string     `json:"traffic_period_key,omitempty"`
	TrafficPeriodEnd          string     `json:"traffic_period_end,omitempty"`
	TrafficQuotaState         string     `json:"traffic_quota_state,omitempty"`
	SubscriptionToken         string     `json:"subscription_token,omitempty"`
	SubscriptionBurnAfterRead bool       `json:"subscription_burn_after_read"`
	SubscriptionBurnedAt      *time.Time `json:"subscription_burned_at,omitempty"`
	SubscriptionAgeEnabled    bool       `json:"subscription_age_enabled"`
	SubscriptionAgePublicKey  string     `json:"subscription_age_public_key,omitempty"`
	Protected                 bool       `json:"protected,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

type UserAuthentication struct {
	UserID                 int64
	TOTPEnabled            bool
	TOTPSecretEncrypted    string
	RecoveryCodeHashesJSON string
	TOTPLastUsedStep       int64
	WebAuthnUserHandle     string
	UpdatedAt              time.Time
}

type PasskeyCredential struct {
	ID             int64      `json:"id"`
	UserID         int64      `json:"-"`
	Name           string     `json:"name"`
	CredentialID   string     `json:"-"`
	CredentialJSON string     `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

type AuthChallenge struct {
	TokenHash     string
	Kind          string
	UserID        int64
	DataEncrypted string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type SubscriptionFormat string

const (
	SubscriptionFormatPlainJSON    SubscriptionFormat = "plain-json"
	SubscriptionFormatStash        SubscriptionFormat = "stash"
	SubscriptionFormatClashMeta    SubscriptionFormat = "clash-meta"
	SubscriptionFormatMihomo       SubscriptionFormat = "mihomo"
	SubscriptionFormatSurfboard    SubscriptionFormat = "surfboard"
	SubscriptionFormatSurge        SubscriptionFormat = "surge"
	SubscriptionFormatSurgeMac     SubscriptionFormat = "surge-mac"
	SubscriptionFormatLoon         SubscriptionFormat = "loon"
	SubscriptionFormatEgern        SubscriptionFormat = "egern"
	SubscriptionFormatShadowrocket SubscriptionFormat = "shadowrocket"
	SubscriptionFormatQX           SubscriptionFormat = "qx"
	SubscriptionFormatSingBox      SubscriptionFormat = "sing-box"
	SubscriptionFormatV2Ray        SubscriptionFormat = "v2ray"
	SubscriptionFormatV2RayURI     SubscriptionFormat = "v2ray-uri"
	SubscriptionFormatClash        SubscriptionFormat = "clash"
)

type SubscriptionProfile struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	GroupName   string    `json:"group_name"`
	Description string    `json:"description"`
	ConfigJSON  string    `json:"config_json"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SubscriptionAssignment struct {
	ID        int64     `json:"id"`
	ProfileID int64     `json:"profile_id"`
	UserID    int64     `json:"user_id"`
	ServerID  *int64    `json:"server_id,omitempty"`
	InboundID *int64    `json:"inbound_id,omitempty"`
	GroupName string    `json:"group_name"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ControllerBackup struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Origin        string    `json:"origin"`
	LocalPath     string    `json:"-"`
	LocalStatus   string    `json:"local_status"`
	RemoteKey     string    `json:"-"`
	RemoteTarget  string    `json:"-"`
	RemoteStatus  string    `json:"remote_status"`
	RemoteError   string    `json:"remote_error,omitempty"`
	RemoteReady   bool      `json:"remote_retrievable"`
	SizeBytes     int64     `json:"size_bytes"`
	SourceVersion string    `json:"source_version"`
	FormatVersion int       `json:"format_version"`
	Protected     bool      `json:"protected"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Server struct {
	ID                       int64          `json:"id"`
	Name                     string         `json:"name"`
	AgentID                  string         `json:"agent_id"`
	AgentTokenHash           string         `json:"-"`
	ChainSecret              string         `json:"-"`
	EnrollmentHash           string         `json:"-"`
	EnrollmentExpiresAt      *time.Time     `json:"-"`
	EntryAddress             string         `json:"entry_address"`
	PublicIPv4               string         `json:"public_ipv4"`
	PublicIPv6               string         `json:"public_ipv6"`
	RegionCode               string         `json:"region_code"`
	DetectedRegionCode       string         `json:"detected_region_code"`
	RegionMode               string         `json:"region_mode"`
	EntryIPMode              EntryIPMode    `json:"entry_ip_mode"`
	ListenIP                 string         `json:"listen_ip"`
	IPStack                  IPStack        `json:"ip_stack"`
	UDPInboundMode           UDPInboundMode `json:"udp_inbound_mode"`
	MTUMode                  MTUMode        `json:"mtu_mode"`
	MTUValue                 int            `json:"mtu_value"`
	MTUProbeHost             string         `json:"mtu_probe_host"`
	MTUProbePort             int            `json:"mtu_probe_port"`
	MTUOverheadBytes         int            `json:"mtu_overhead_bytes"`
	BBREnabled               bool           `json:"bbr_enabled"`
	PortRangeStart           int            `json:"port_range_start"`
	PortRangeEnd             int            `json:"port_range_end"`
	SSHPort                  int            `json:"ssh_port"`
	Status                   ServerStatus   `json:"status"`
	OS                       string         `json:"os"`
	DistroID                 string         `json:"distro_id"`
	DistroVersion            string         `json:"distro_version"`
	DistroName               string         `json:"distro_name"`
	Libc                     string         `json:"libc"`
	ServiceManager           string         `json:"service_manager"`
	PackageManager           string         `json:"package_manager"`
	Arch                     string         `json:"arch"`
	Kernel                   string         `json:"kernel"`
	CPU                      string         `json:"cpu"`
	MemoryBytes              uint64         `json:"memory_bytes"`
	CPUUsagePercent          float64        `json:"cpu_usage_percent"`
	MemoryUsedBytes          uint64         `json:"memory_used_bytes"`
	MemoryTotalBytes         uint64         `json:"memory_total_bytes"`
	AgentMemoryBytes         uint64         `json:"agent_memory_bytes"`
	DiskBytes                uint64         `json:"disk_bytes"`
	AgentVersion             string         `json:"agent_version"`
	AgentBuild               string         `json:"agent_build"`
	SingBoxVersion           string         `json:"sing_box_version"`
	MonitoringMode           string         `json:"monitoring_mode"`
	TrafficResetMode         string         `json:"traffic_reset_mode"`
	TrafficResetDay          int            `json:"traffic_reset_day"`
	NetworkUploadBPS         uint64         `json:"network_upload_bps"`
	NetworkDownloadBPS       uint64         `json:"network_download_bps"`
	TrafficUploadBytes       uint64         `json:"traffic_upload_bytes"`
	TrafficDownloadBytes     uint64         `json:"traffic_download_bytes"`
	TrafficPeriodStart       string         `json:"traffic_period_start"`
	TrafficPeriodEnd         string         `json:"traffic_period_end"`
	ConnectivityProbeEnabled bool           `json:"connectivity_probe_enabled"`
	ConnectionAuditEnabled   bool           `json:"connection_audit_enabled"`
	ConnectivityStatus       string         `json:"connectivity_status"`
	ConnectivityLatencyMS    int64          `json:"connectivity_latency_ms"`
	ConnectivityCheckedAt    *time.Time     `json:"connectivity_checked_at,omitempty"`
	ConnectivityError        string         `json:"connectivity_error"`
	TelemetryUpdatedAt       *time.Time     `json:"telemetry_updated_at,omitempty"`
	LastSeenAt               *time.Time     `json:"last_seen_at,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

type Inbound struct {
	ID                int64       `json:"id"`
	ServerID          int64       `json:"server_id"`
	Name              string      `json:"name"`
	Protocol          Protocol    `json:"protocol"`
	ListenIP          string      `json:"listen_ip"`
	Port              int         `json:"port"`
	EntryIPMode       EntryIPMode `json:"entry_ip_mode"`
	ExternalIP        string      `json:"external_ip"`
	DNSSyncEnabled    bool        `json:"dns_sync_enabled"`
	DNSCredentialID   *int64      `json:"dns_credential_id,omitempty"`
	DNSDomain         string      `json:"dns_domain"`
	DNSProxyEnabled   bool        `json:"dns_proxy_enabled"`
	DNSRecordTypes    string      `json:"dns_record_types"`
	DDNSEnabled       bool        `json:"ddns_enabled"`
	DDNSInterval      int         `json:"ddns_interval_seconds"`
	DNSSyncStatus     string      `json:"dns_sync_status"`
	DNSSyncError      string      `json:"dns_sync_error"`
	DNSLastSyncedAt   *time.Time  `json:"dns_last_synced_at,omitempty"`
	TLS               bool        `json:"tls"`
	CertificateMode   string      `json:"certificate_mode,omitempty"`
	CertificateID     *int64      `json:"certificate_id,omitempty"`
	CertificateDomain string      `json:"certificate_domain,omitempty"`
	ConfigJSON        string      `json:"config_json"`
	Enabled           bool        `json:"enabled"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

const (
	CertificateModeExternal = "external"
	CertificateModeAuto     = "auto"
	CertificateModeExact    = "exact"
	CertificateModeWildcard = "wildcard"
	CertificateModeExplicit = "explicit"
)

const (
	CertificateChallengeHTTP      = "http01"
	CertificateChallengeDNS       = "dns01"
	CertificateChallengeDNSManual = "dns01_manual"
)

const (
	CertificateStatusPending     = "pending"
	CertificateStatusIssuing     = "issuing"
	CertificateStatusAwaitingDNS = "awaiting_dns"
	CertificateStatusReady       = "ready"
	CertificateStatusFailed      = "failed"
)

type DNSProvider string

const (
	DNSProviderCloudflare  DNSProvider = "cloudflare"
	DNSProviderAliDNS      DNSProvider = "alidns"
	DNSProviderTencentDNS  DNSProvider = "tencent_dns"
	DNSProviderTencentESA  DNSProvider = "tencent_esa"
	DNSProviderHuaweiCloud DNSProvider = "huawei_cloud"
)

type DNSCredential struct {
	ID              int64               `json:"id"`
	Name            string              `json:"name"`
	Provider        DNSProvider         `json:"provider"`
	ZoneName        string              `json:"zone_name"`
	ZoneID          string              `json:"zone_id,omitempty"`
	Zones           []DNSCredentialZone `json:"zones"`
	ConfigEncrypted string              `json:"-"`
	Configured      bool                `json:"configured"`
	Enabled         bool                `json:"enabled"`
	VerifiedAt      *time.Time          `json:"verified_at,omitempty"`
	LastError       string              `json:"last_error,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

type DNSCredentialZone struct {
	ID             int64     `json:"id"`
	CredentialID   int64     `json:"credential_id"`
	ZoneName       string    `json:"zone_name"`
	ProviderZoneID string    `json:"provider_zone_id,omitempty"`
	ServerID       *int64    `json:"server_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type DNSRecord struct {
	ID               string `json:"id"`
	CredentialID     int64  `json:"credential_id"`
	CredentialZoneID int64  `json:"dns_zone_id,omitempty"`
	ZoneID           string `json:"zone_id,omitempty"`
	ZoneName         string `json:"zone_name"`
	Type             string `json:"type"`
	Name             string `json:"name"`
	Content          string `json:"content"`
	Comment          string `json:"comment,omitempty"`
	ServerID         *int64 `json:"server_id,omitempty"`
	InboundID        *int64 `json:"inbound_id,omitempty"`
	TTL              int    `json:"ttl"`
	Proxied          bool   `json:"proxied,omitempty"`
	Enabled          bool   `json:"enabled"`
}

type GoogleEABCredential struct {
	ID               int64     `json:"id"`
	KeyID            string    `json:"key_id"`
	Remark           string    `json:"remark"`
	HMACKeyEncrypted string    `json:"-"`
	UsageCount       int       `json:"usage_count"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"-"`
}

type Certificate struct {
	ID                    int64       `json:"id"`
	Name                  string      `json:"name"`
	PrimaryDomain         string      `json:"primary_domain"`
	Domains               []string    `json:"domains"`
	Wildcard              bool        `json:"wildcard"`
	ChallengeType         string      `json:"challenge_type"`
	DNSCredentialID       *int64      `json:"dns_credential_id,omitempty"`
	IssuanceServerID      *int64      `json:"issuance_server_id,omitempty"`
	ACMECA                string      `json:"acme_ca"`
	AccountEmail          string      `json:"account_email"`
	GoogleEABCredentialID *int64      `json:"google_eab_credential_id,omitempty"`
	EABKeyID              string      `json:"eab_key_id,omitempty"`
	EABHMACKeyEncrypted   string      `json:"-"`
	EABConfigured         bool        `json:"eab_configured,omitempty"`
	Status                string      `json:"status"`
	CertificatePEM        string      `json:"-"`
	FullchainPEM          string      `json:"-"`
	PrivateKeyEncrypted   string      `json:"-"`
	Revision              string      `json:"revision,omitempty"`
	NotBefore             *time.Time  `json:"not_before,omitempty"`
	NotAfter              *time.Time  `json:"not_after,omitempty"`
	AutoRenew             bool        `json:"auto_renew"`
	ValidationRecords     []DNSRecord `json:"validation_records,omitempty"`
	LastError             string      `json:"last_error,omitempty"`
	LastIssuedAt          *time.Time  `json:"last_issued_at,omitempty"`
	LastRenewalAttemptAt  *time.Time  `json:"last_renewal_attempt_at,omitempty"`
	CreatedAt             time.Time   `json:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at"`
}

type InboundCertificateBinding struct {
	InboundID     int64     `json:"inbound_id"`
	CertificateID *int64    `json:"certificate_id,omitempty"`
	Mode          string    `json:"mode"`
	ServerName    string    `json:"server_name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type InboundUser struct {
	ID        int64     `json:"id"`
	InboundID int64     `json:"inbound_id"`
	UserID    int64     `json:"user_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SSHUserKey belongs to one panel user and can be enabled on one or more SSH
// inbounds through the normal inbound-user grant. Private keys are never
// stored by OBoard.
type SSHUserKey struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AccessSubjectType string

const (
	AccessSubjectUser  AccessSubjectType = "user"
	AccessSubjectGroup AccessSubjectType = "group"
)

type AccessScopeType string

const (
	AccessScopeGlobal  AccessScopeType = "global"
	AccessScopeServer  AccessScopeType = "server"
	AccessScopeInbound AccessScopeType = "inbound"
)

type UserGroup struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	Role              Role      `json:"role"`
	SystemKey         string    `json:"system_key,omitempty"`
	Enabled           bool      `json:"enabled"`
	SpeedLimitMbps    int       `json:"speed_limit_mbps"`
	TrafficLimitBytes int64     `json:"traffic_limit_bytes"`
	TrafficResetMode  string    `json:"traffic_reset_mode"`
	TrafficResetDay   int       `json:"traffic_reset_day"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type UserGroupMember struct {
	ID        int64     `json:"id"`
	GroupID   int64     `json:"group_id"`
	UserID    int64     `json:"user_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type InboundAccessGrant struct {
	ID          int64             `json:"id"`
	SubjectType AccessSubjectType `json:"subject_type"`
	SubjectID   int64             `json:"subject_id"`
	ScopeType   AccessScopeType   `json:"scope_type"`
	ServerID    *int64            `json:"server_id,omitempty"`
	InboundID   *int64            `json:"inbound_id,omitempty"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type Outbound struct {
	ID            int64     `json:"id"`
	ServerID      int64     `json:"server_id"`
	NextServerID  *int64    `json:"next_server_id,omitempty"`
	Name          string    `json:"name"`
	Protocol      Protocol  `json:"protocol"`
	TargetAddress string    `json:"target_address"`
	TargetPort    int       `json:"target_port"`
	ConfigJSON    string    `json:"config_json"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RoutingRule struct {
	ID                 int64       `json:"id"`
	ServerID           int64       `json:"server_id"`
	Name               string      `json:"name"`
	Priority           int         `json:"priority"`
	MatchJSON          string      `json:"match_json"`
	Action             RouteAction `json:"action"`
	OutboundID         *int64      `json:"outbound_id,omitempty"`
	ExternalOutboundID *int64      `json:"external_outbound_id,omitempty"`
	TargetServerID     *int64      `json:"target_server_id,omitempty"`
	WARPProfileID      *int64      `json:"warp_profile_id,omitempty"`
	OutboundTag        string      `json:"outbound_tag"`
	InterfaceName      string      `json:"interface_name,omitempty"`
	Enabled            bool        `json:"enabled"`
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`
}

type ExternalOutbound struct {
	ID            int64                 `json:"id"`
	ServerID      *int64                `json:"server_id,omitempty"`
	Name          string                `json:"name"`
	Protocol      Protocol              `json:"protocol"`
	Scope         ExternalOutboundScope `json:"scope"`
	TargetAddress string                `json:"target_address"`
	TargetPort    int                   `json:"target_port"`
	ConfigJSON    string                `json:"config_json"`
	ExposeToUsers bool                  `json:"expose_to_users"`
	Enabled       bool                  `json:"enabled"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

type ExternalOutboundAccessGrant struct {
	ID                 int64             `json:"id"`
	ExternalOutboundID int64             `json:"external_outbound_id"`
	SubjectType        AccessSubjectType `json:"subject_type"`
	SubjectID          int64             `json:"subject_id"`
	Enabled            bool              `json:"enabled"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type ProxyPath struct {
	ID                 int64               `json:"id"`
	Kind               ProxyPathKind       `json:"kind"`
	BranchSourceStepID *int64              `json:"branch_source_step_id,omitempty"`
	Name               string              `json:"name"`
	NameMode           ProxyPathNameMode   `json:"name_mode"`
	NameTemplate       []ProxyPathNamePart `json:"name_template"`
	NameTemplateJSON   string              `json:"-"`
	InboundID          int64               `json:"inbound_id"`
	Secret             string              `json:"-"`
	Enabled            bool                `json:"enabled"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

type ProxyPathKind string

const (
	ProxyPathKindChain  ProxyPathKind = "chain"
	ProxyPathKindDirect ProxyPathKind = "direct"
)

type ProxyPathNameMode string

const (
	ProxyPathNameAuto   ProxyPathNameMode = "auto"
	ProxyPathNameCustom ProxyPathNameMode = "custom"
)

type ProxyPathNamePartKind string

const (
	ProxyPathNameLiteral          ProxyPathNamePartKind = "literal"
	ProxyPathNameServer           ProxyPathNamePartKind = "server"
	ProxyPathNameExternalOutbound ProxyPathNamePartKind = "external_outbound"
)

type ProxyPathNamePart struct {
	Kind               ProxyPathNamePartKind `json:"kind"`
	Value              string                `json:"value,omitempty"`
	ServerID           int64                 `json:"server_id,omitempty"`
	ExternalOutboundID int64                 `json:"external_outbound_id,omitempty"`
}

type ProxyPathStepNodeType string

const (
	ProxyPathStepServerInbound ProxyPathStepNodeType = "server_inbound"
	ProxyPathStepImported      ProxyPathStepNodeType = "imported"
)

type ProxyPathStepTransportMode string

const (
	ProxyPathTransportSingBox     ProxyPathStepTransportMode = "singbox"
	ProxyPathTransportPortForward ProxyPathStepTransportMode = "port_forward"
	ProxyPathTransportTunnel      ProxyPathStepTransportMode = "tunnel"
)

type ProxyPathPlanStep struct {
	ID                 int64                      `json:"id"`
	Position           int                        `json:"position"`
	NodeType           ProxyPathStepNodeType      `json:"node_type"`
	TransportMode      ProxyPathStepTransportMode `json:"transport_mode"`
	ProcessingRole     bool                       `json:"processing_role"`
	ServerID           *int64                     `json:"server_id,omitempty"`
	InboundID          *int64                     `json:"inbound_id,omitempty"`
	ExternalOutboundID *int64                     `json:"external_outbound_id,omitempty"`
	TunnelID           int64                      `json:"tunnel_id,omitempty"`
}

type ProxyPathPlan struct {
	PathID       int64               `json:"path_id"`
	Name         string              `json:"name"`
	InboundID    int64               `json:"inbound_id"`
	Enabled      bool                `json:"enabled"`
	Steps        []ProxyPathPlanStep `json:"steps"`
	Warnings     []string            `json:"warnings,omitempty"`
	PortForwards []PortForward       `json:"port_forwards,omitempty"`
	Tunnels      []Tunnel            `json:"tunnels,omitempty"`
}

// ProxyPathPortAllocation records the port one generated proxy-path listener
// owns. Allocation is persisted rather than re-derived on every projection so an
// unrelated topology change cannot move a listener that is already deployed, and
// so the port an operator inspects is the port that gets applied.
type ProxyPathPortAllocation struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	ScopeKey  string    `json:"scope_key"`
	ServerID  int64     `json:"server_id"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Allocation kinds. The scope key identifies what owns the port within a kind.
const (
	// ScopeKey is the normalized SS2022 method.
	ProxyPathPortKindChainService = "chain_service"
	// ScopeKey is "<pathID>:<position>".
	ProxyPathPortKindInternal = "internal_inbound"
	// ScopeKey is "<pathID>:<position>" for the loopback-only decrypted protocol listener.
	ProxyPathPortKindTrustedInner = "trusted_forward_inner"
	// ScopeKey is the derived tunnel ID; the loopback listener lives on the source.
	ProxyPathPortKindTunnelSSH = "tunnel_ssh_loopback"
	// ScopeKey is the derived tunnel ID; the UDP listener lives on the target.
	ProxyPathPortKindTunnelWG = "tunnel_wireguard"
)

type ProxyPathStep struct {
	ID                 int64                      `json:"id"`
	PathID             int64                      `json:"path_id"`
	Position           int                        `json:"position"`
	NodeType           ProxyPathStepNodeType      `json:"node_type"`
	TransportMode      ProxyPathStepTransportMode `json:"transport_mode"`
	ProcessingRole     bool                       `json:"processing_role"`
	ServerID           *int64                     `json:"server_id,omitempty"`
	InboundID          *int64                     `json:"inbound_id,omitempty"`
	ExternalOutboundID *int64                     `json:"external_outbound_id,omitempty"`
	ConfigJSON         string                     `json:"config_json"`
	CreatedAt          time.Time                  `json:"created_at"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}

type WARPProfile struct {
	ID              int64      `json:"id"`
	ServerID        int64      `json:"server_id"`
	Name            string     `json:"name"`
	Status          WARPStatus `json:"status"`
	ConfigJSON      string     `json:"config_json"`
	MTU             int        `json:"mtu"`
	DNSStrategy     string     `json:"dns_strategy"`
	LastRequestedAt *time.Time `json:"last_requested_at,omitempty"`
	Error           string     `json:"error"`
	Enabled         bool       `json:"enabled"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type DNSCandidate struct {
	Tag       string       `json:"tag"`
	Transport DNSTransport `json:"transport"`
	Server    string       `json:"server"`
	Port      int          `json:"port"`
	Path      string       `json:"path,omitempty"`
	TLSName   string       `json:"tls_name,omitempty"`
}

type DNSList struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Kind       DNSListKind    `json:"kind"`
	Revision   int64          `json:"revision"`
	Candidates []DNSCandidate `json:"candidates"`
	Enabled    bool           `json:"enabled"`
	Protected  bool           `json:"protected"`
	UsageCount int            `json:"usage_count"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type ServerDNSPolicy struct {
	ServerID                   int64           `json:"server_id"`
	EncryptedListID            int64           `json:"encrypted_list_id"`
	BootstrapListID            int64           `json:"bootstrap_list_id"`
	Revision                   int64           `json:"revision"`
	Strategy                   string          `json:"strategy"`
	AutoTest                   DNSAutoTestMode `json:"auto_test"`
	TestIntervalSeconds        int             `json:"test_interval_seconds"`
	EncryptedSelected          []DNSCandidate  `json:"encrypted_selected"`
	BootstrapSelected          []DNSCandidate  `json:"bootstrap_selected"`
	EncryptedSelectionRevision int64           `json:"encrypted_selection_revision"`
	BootstrapSelectionRevision int64           `json:"bootstrap_selection_revision"`
	LastAttemptAt              *time.Time      `json:"last_attempt_at,omitempty"`
	LastSuccessAt              *time.Time      `json:"last_success_at,omitempty"`
	LastError                  string          `json:"last_error"`
	NeedsBenchmark             bool            `json:"needs_benchmark"`
	CreatedAt                  time.Time       `json:"created_at"`
	UpdatedAt                  time.Time       `json:"updated_at"`
}

type DNSBenchmarkPlan struct {
	Version               int64           `json:"version"`
	ServerID              int64           `json:"server_id"`
	PolicyRevision        int64           `json:"policy_revision"`
	EncryptedListID       int64           `json:"encrypted_list_id"`
	EncryptedListRevision int64           `json:"encrypted_list_revision"`
	BootstrapListID       int64           `json:"bootstrap_list_id"`
	BootstrapListRevision int64           `json:"bootstrap_list_revision"`
	Mode                  DNSAutoTestMode `json:"mode"`
	IntervalSeconds       int             `json:"interval_seconds"`
	RequestID             string          `json:"request_id,omitempty"`
	EncryptedCandidates   []DNSCandidate  `json:"encrypted_candidates"`
	BootstrapCandidates   []DNSCandidate  `json:"bootstrap_candidates"`
}

type WARPRequestPlan struct {
	Version     int64   `json:"version"`
	ServerID    int64   `json:"server_id"`
	ProfileID   int64   `json:"profile_id"`
	Name        string  `json:"name"`
	IPStack     IPStack `json:"ip_stack"`
	MTU         int     `json:"mtu"`
	DNSStrategy string  `json:"dns_strategy"`
}

type WARPConfigReport struct {
	ServerID   int64      `json:"server_id"`
	ProfileID  int64      `json:"profile_id"`
	Status     WARPStatus `json:"status"`
	ConfigJSON string     `json:"config_json"`
	MTU        int        `json:"mtu"`
	Error      string     `json:"error"`
	ResultJSON string     `json:"result_json"`
}

type DNSBenchmarkResult struct {
	ID                    int64             `json:"id"`
	ReportID              string            `json:"report_id"`
	RequestID             string            `json:"request_id,omitempty"`
	ServerID              int64             `json:"server_id"`
	PolicyRevision        int64             `json:"policy_revision"`
	EncryptedListID       int64             `json:"encrypted_list_id"`
	EncryptedListRevision int64             `json:"encrypted_list_revision"`
	BootstrapListID       int64             `json:"bootstrap_list_id"`
	BootstrapListRevision int64             `json:"bootstrap_list_revision"`
	Encrypted             DNSBenchmarkGroup `json:"encrypted"`
	Bootstrap             DNSBenchmarkGroup `json:"bootstrap"`
	Status                string            `json:"status"`
	Error                 string            `json:"error"`
	CreatedAt             time.Time         `json:"created_at"`
}

type DNSBenchmarkItem struct {
	Tag       string `json:"tag"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

type DNSBenchmarkGroup struct {
	Items    []DNSBenchmarkItem `json:"items"`
	BestTags []string           `json:"best_tags"`
}

type DNSBenchmarkRun struct {
	ID                    int64      `json:"id"`
	RequestID             string     `json:"request_id"`
	ServerID              int64      `json:"server_id"`
	PolicyRevision        int64      `json:"policy_revision"`
	EncryptedListID       int64      `json:"encrypted_list_id"`
	EncryptedListRevision int64      `json:"encrypted_list_revision"`
	BootstrapListID       int64      `json:"bootstrap_list_id"`
	BootstrapListRevision int64      `json:"bootstrap_list_revision"`
	Trigger               string     `json:"trigger"`
	ApplyOnSuccess        bool       `json:"apply_on_success"`
	RequestedBy           *int64     `json:"requested_by,omitempty"`
	TaskID                *int64     `json:"task_id,omitempty"`
	ApplyTaskID           *int64     `json:"apply_task_id,omitempty"`
	Status                string     `json:"status"`
	Error                 string     `json:"error"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	CompletedAt           *time.Time `json:"completed_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type MTUDetectionMethod struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	MTU       int    `json:"mtu"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
	Error     string `json:"error,omitempty"`
}

type MTUDetectionPlan struct {
	Version       int64   `json:"version"`
	ServerID      int64   `json:"server_id"`
	Mode          MTUMode `json:"mode"`
	TargetHost    string  `json:"target_host"`
	TargetPort    int     `json:"target_port"`
	InterfaceName string  `json:"interface_name,omitempty"`
	OverheadBytes int     `json:"overhead_bytes"`
	DesiredMTU    int     `json:"desired_mtu"`
	SampleCount   int     `json:"sample_count"`
	TimeoutMS     int     `json:"timeout_ms"`
	MinMTU        int     `json:"min_mtu"`
	MaxMTU        int     `json:"max_mtu"`
}

type MTUDetectionResult struct {
	ID             int64                `json:"id"`
	ServerID       int64                `json:"server_id"`
	Mode           MTUMode              `json:"mode"`
	TargetHost     string               `json:"target_host"`
	TargetPort     int                  `json:"target_port"`
	InterfaceName  string               `json:"interface_name"`
	CurrentMTU     int                  `json:"current_mtu"`
	PathMTU        int                  `json:"path_mtu"`
	RecommendedMTU int                  `json:"recommended_mtu"`
	AppliedMTU     int                  `json:"applied_mtu"`
	Confidence     string               `json:"confidence"`
	Methods        []MTUDetectionMethod `json:"methods,omitempty"`
	Error          string               `json:"error"`
	ResultJSON     string               `json:"result_json"`
	CreatedAt      time.Time            `json:"created_at"`
}

type ForwardBackend string

const (
	ForwardBackendAuto    ForwardBackend = "auto"
	ForwardBackendRealm   ForwardBackend = "realm"
	ForwardBackendNFT     ForwardBackend = "nft"
	ForwardBackendBuiltin ForwardBackend = "builtin"
)

type ForwardProtocol string

const (
	ForwardProtocolTCP    ForwardProtocol = "tcp"
	ForwardProtocolUDP    ForwardProtocol = "udp"
	ForwardProtocolTCPUDP ForwardProtocol = "tcp_udp"
)

type PortForward struct {
	ID                   int64                 `json:"id"`
	Name                 string                `json:"name"`
	SourceServerID       int64                 `json:"source_server_id"`
	TargetServerID       int64                 `json:"target_server_id"`
	ListenIP             string                `json:"listen_ip"`
	ListenPort           int                   `json:"listen_port"`
	TargetAddress        string                `json:"target_address"`
	TargetPort           int                   `json:"target_port"`
	Protocol             ForwardProtocol       `json:"protocol"`
	Backend              ForwardBackend        `json:"backend"`
	ProbeMode            string                `json:"probe_mode"`
	ProbeIntervalSeconds int                   `json:"probe_interval_seconds"`
	SampleRate           float64               `json:"sample_rate"`
	Priority             int                   `json:"priority"`
	ConfigJSON           string                `json:"config_json"`
	TrustedForward       *TrustedForwardSender `json:"trusted_forward,omitempty"`
	Enabled              bool                  `json:"enabled"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

type TrustedForwardSender struct {
	Version             int    `json:"version"`
	ReceiverID          string `json:"receiver_id"`
	Key                 string `json:"key"`
	MaxClockSkewSeconds int    `json:"max_clock_skew_seconds"`
}

type TunnelType string

const (
	TunnelTypeWireGuard TunnelType = "wireguard"
	TunnelTypeSSH       TunnelType = "ssh"
)

type Tunnel struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	SourceServerID int64      `json:"source_server_id"`
	TargetServerID int64      `json:"target_server_id"`
	Type           TunnelType `json:"type"`
	LocalAddress   string     `json:"local_address"`
	PeerAddress    string     `json:"peer_address"`
	ListenPort     int        `json:"listen_port"`
	TargetEndpoint string     `json:"target_endpoint"`
	TargetPort     int        `json:"target_port"`
	Priority       int        `json:"priority"`
	ConfigJSON     string     `json:"config_json"`
	Enabled        bool       `json:"enabled"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type TunnelPlan struct {
	Version int64    `json:"version"`
	Tunnels []Tunnel `json:"tunnels"`
}

// SSHInboundPlan is separate from TunnelPlan: tunnels are private Agent
// plumbing, while SSH inbounds are user-facing public services.
type SSHInboundPlan struct {
	Version  int64        `json:"version"`
	Inbounds []SSHInbound `json:"inbounds"`
}

type SSHInbound struct {
	InboundID int64                           `json:"inbound_id"`
	ServerID  int64                           `json:"server_id"`
	Name      string                          `json:"name"`
	ListenIP  string                          `json:"listen_ip"`
	Address   string                          `json:"address"`
	Username  string                          `json:"username"`
	Port      int                             `json:"port"`
	Enabled   bool                            `json:"enabled"`
	Users     []SSHInboundUser                `json:"users"`
	Policies  map[string]TrafficRuntimePolicy `json:"policies,omitempty"`
}

type SSHInboundUser struct {
	UserID     int64    `json:"user_id"`
	Username   string   `json:"username"`
	PublicKeys []string `json:"public_keys"`
	Enabled    bool     `json:"enabled"`
}

type PortForwardProbeResult struct {
	ID            int64     `json:"id"`
	PortForwardID int64     `json:"port_forward_id"`
	ServerID      int64     `json:"server_id"`
	Mode          string    `json:"mode"`
	Available     bool      `json:"available"`
	LatencyMS     int64     `json:"latency_ms"`
	SampleCount   int       `json:"sample_count"`
	Error         string    `json:"error"`
	ResultJSON    string    `json:"result_json"`
	CreatedAt     time.Time `json:"created_at"`
}

type PortForwardPlan struct {
	Version int64         `json:"version"`
	Rules   []PortForward `json:"rules"`
}

type AgentTask struct {
	ID            int64      `json:"id"`
	ServerID      int64      `json:"server_id"`
	Type          string     `json:"type"`
	PayloadJSON   string     `json:"payload_json"`
	Status        string     `json:"status"`
	ResultJSON    string     `json:"result_json"`
	ConfigVersion int64      `json:"config_version"`
	Nonce         string     `json:"nonce"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

const (
	AgentTaskTypeApplyDeployment       = "apply_deployment"
	AgentTaskTypeApplyCoreConfig       = "apply_core_config"
	AgentTaskTypeUpdateAgent           = "update_agent"
	AgentTaskTypeUpdateAgentConfig     = "update_agent_config"
	AgentTaskTypeDiagnoseNetwork       = "diagnose_network"
	AgentTaskTypeProbeInbounds         = "probe_inbounds"
	AgentTaskTypeProbeInboundsExternal = "probe_inbounds_external"
	AgentTaskTypeProbePortForwards     = "probe_port_forwards"
	AgentTaskTypeDetectMTU             = "detect_mtu"
	AgentTaskTypeBenchmarkDNS          = "benchmark_dns"
	AgentTaskTypeCollectLogs           = "collect_logs"
	AgentTaskTypeManageLogs            = "manage_logs"
	AgentTaskTypeIssueCertificateHTTP  = "issue_certificate_http01"
)

type DeploymentFailureDismissal struct {
	ConfigVersion int64     `json:"config_version"`
	ActorID       int64     `json:"actor_id"`
	DismissedAt   time.Time `json:"dismissed_at"`
}

type AgentEnrollRequest struct {
	EnrollmentToken string       `json:"enrollment_token"`
	Health          HealthReport `json:"health"`
}

type AgentEnrollResponse struct {
	ServerID               int64  `json:"server_id"`
	AgentID                string `json:"agent_id"`
	AgentToken             string `json:"agent_token"`
	ConnectionAuditEnabled bool   `json:"connection_audit_enabled"`
}

type AgentSocketMessage struct {
	Type         string          `json:"type"`
	ServerID     int64           `json:"server_id,omitempty"`
	Timestamp    time.Time       `json:"ts,omitempty"`
	Task         *AgentTask      `json:"task,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	HealthReport *HealthReport   `json:"health_report,omitempty"`
	Raw          json.RawMessage `json:"-"`
}

type AgentTaskResultReport struct {
	TaskID       int64         `json:"task_id"`
	Status       string        `json:"status"`
	ResultJSON   string        `json:"result_json"`
	HealthReport *HealthReport `json:"health_report,omitempty"`
}

type ApplyCoreConfigTaskPayload struct {
	Config       string                  `json:"config"`
	Reason       string                  `json:"reason,omitempty"`
	PrunedUserID int64                   `json:"pruned_user_id,omitempty"`
	Assets       []ManagedAssetReference `json:"assets,omitempty"`
}

// DeploymentTaskPayload keeps one user deployment as one Agent task while
// preserving the individual execution plans needed by the Agent.
type DeploymentTaskPayload struct {
	Version              int64                      `json:"version"`
	Config               ApplyCoreConfigTaskPayload `json:"config"`
	ConfigChanged        bool                       `json:"config_changed"`
	WARPRequests         []WARPRequestPlan          `json:"warp_requests,omitempty"`
	TimeSync             *TimeSyncPlan              `json:"time_sync,omitempty"`
	PortForwards         PortForwardPlan            `json:"port_forwards"`
	InboundProbe         *InboundProbePlan          `json:"inbound_probe,omitempty"`
	ExternalInboundProbe *InboundProbePlan          `json:"external_inbound_probe,omitempty"`
	PortForwardProbe     *PortForwardPlan           `json:"port_forward_probe,omitempty"`
	Tunnels              TunnelPlan                 `json:"tunnels"`
	SSHInbounds          SSHInboundPlan             `json:"ssh_inbounds"`
	DNSBenchmark         *DNSBenchmarkPlan          `json:"dns_benchmark,omitempty"`
	MTUDetection         *MTUDetectionPlan          `json:"mtu_detection,omitempty"`
}

type IssueCertificateHTTPTaskPayload struct {
	CertificateID int64    `json:"certificate_id"`
	Domains       []string `json:"domains"`
	AccountEmail  string   `json:"account_email"`
	ACMECA        string   `json:"acme_ca"`
	Renew         bool     `json:"renew"`
}

type ManagedAssetReference struct {
	Kind     string `json:"kind"`
	ID       int64  `json:"id"`
	Revision string `json:"revision"`
}

type ManagedAssetRequest struct {
	Assets []ManagedAssetReference `json:"assets"`
}

type ManagedAssetFile struct {
	Name       string `json:"name"`
	ContentB64 string `json:"content_b64"`
	Mode       uint32 `json:"mode"`
}

type ManagedAsset struct {
	ManagedAssetReference
	Files []ManagedAssetFile `json:"files"`
}

type ManagedAssetResponse struct {
	Assets []ManagedAsset `json:"assets"`
}

type CertificateIssueReport struct {
	TaskID         int64    `json:"task_id"`
	CertificateID  int64    `json:"certificate_id"`
	Domains        []string `json:"domains"`
	CertificatePEM string   `json:"certificate_pem"`
	FullchainPEM   string   `json:"fullchain_pem"`
	PrivateKeyPEM  string   `json:"private_key_pem"`
}

type UpdateAgentTaskPayload struct {
	ControllerURL string `json:"controller_url"`
	ExpectedBuild string `json:"expected_build"`
	Source        string `json:"source"`
	GitHubRepo    string `json:"github_repo"`
}

type AgentUpdateRequest struct {
	Source     string `json:"source"`
	GitHubRepo string `json:"github_repo"`
}

type AgentConfigPatch struct {
	ControllerURL           string `json:"controller_url,omitempty"`
	StateDir                string `json:"state_dir,omitempty"`
	CoreBinary              string `json:"core_binary,omitempty"`
	CoreService             string `json:"core_service,omitempty"`
	CommandTimeoutSeconds   int    `json:"command_timeout_seconds,omitempty"`
	ReloadCommand           string `json:"reload_command,omitempty"`
	RestartCommand          string `json:"restart_command,omitempty"`
	TimeSyncCommand         string `json:"time_sync_command,omitempty"`
	TimeSyncIntervalSeconds int    `json:"time_sync_interval_seconds,omitempty"`
	LogMaxMB                int    `json:"log_max_mb,omitempty"`
	LogBackups              int    `json:"log_backups,omitempty"`
	CoreLogMaxMB            int    `json:"core_log_max_mb,omitempty"`
	CoreLogBackups          int    `json:"core_log_backups,omitempty"`
	UpdateSource            string `json:"update_source,omitempty"`
	AllowPanelUpdate        bool   `json:"allow_panel_update,omitempty"`
	UpdateRepo              string `json:"update_repo,omitempty"`
}

type DiagnosticTarget struct {
	Name     string   `json:"name"`
	Protocol Protocol `json:"protocol"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
}

type DiagnoseNetworkTaskPayload struct {
	Version      int64              `json:"version"`
	ServerID     int64              `json:"server_id"`
	EntryTargets []DiagnosticTarget `json:"entry_targets"`
}

type InboundProbeTarget struct {
	InboundID int64    `json:"inbound_id"`
	Name      string   `json:"name"`
	Protocol  Protocol `json:"protocol"`
	Host      string   `json:"host"`
	ListenIP  string   `json:"listen_ip"`
	Port      int      `json:"port"`
	Transport string   `json:"transport"`
}

type InboundProbePlan struct {
	Version      int64                `json:"version"`
	ServerID     int64                `json:"server_id"`
	SampleCount  int                  `json:"sample_count"`
	IntervalMS   int                  `json:"interval_ms"`
	TimeoutMS    int                  `json:"timeout_ms"`
	EntryTargets []InboundProbeTarget `json:"entry_targets"`
}

type InboundProbeResult struct {
	ID            int64     `json:"id"`
	InboundID     int64     `json:"inbound_id"`
	ServerID      int64     `json:"server_id"`
	ConfigVersion int64     `json:"config_version"`
	Mode          string    `json:"mode"`
	Transport     string    `json:"transport"`
	Endpoint      string    `json:"endpoint"`
	Available     bool      `json:"available"`
	Confirmed     bool      `json:"confirmed"`
	LatencyMS     int64     `json:"latency_ms"`
	MinLatencyMS  int64     `json:"min_latency_ms"`
	P95LatencyMS  int64     `json:"p95_latency_ms"`
	JitterMS      int64     `json:"jitter_ms"`
	SampleCount   int       `json:"sample_count"`
	SuccessCount  int       `json:"success_count"`
	Error         string    `json:"error"`
	ResultJSON    string    `json:"result_json"`
	CreatedAt     time.Time `json:"created_at"`
}

type CollectLogsTaskPayload struct {
	Lines    int    `json:"lines"`
	Services string `json:"services"`
}

type ManageLogsTaskPayload struct {
	Action   string `json:"action"`
	Services string `json:"services"`
}

type TimeSyncPlan struct {
	Version         int64    `json:"version"`
	Mode            string   `json:"mode"`
	IntervalSeconds int      `json:"interval_seconds"`
	Servers         []string `json:"servers"`
}

type NotificationChannel struct {
	ID            int64     `json:"id"`
	OwnerUserID   int64     `json:"owner_user_id"`
	OwnerUsername string    `json:"owner_username,omitempty"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Enabled       bool      `json:"enabled"`
	Events        string    `json:"events"`
	ConfigJSON    string    `json:"config_json"`
	TemplatesJSON string    `json:"templates_json"`
	UserIDs       []int64   `json:"user_ids"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type NotificationTemplate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type NotificationAnnouncement struct {
	ID          int64     `json:"id"`
	ActorUserID int64     `json:"actor_user_id"`
	ActorName   string    `json:"actor_name"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	UserIDs     []int64   `json:"user_ids"`
	QueuedCount int       `json:"queued_count"`
	CreatedAt   time.Time `json:"created_at"`
}

type NotificationDelivery struct {
	ID            int64               `json:"id"`
	ChannelID     int64               `json:"channel_id"`
	Event         string              `json:"event"`
	EventKey      string              `json:"event_key"`
	Title         string              `json:"title"`
	Body          string              `json:"body"`
	Status        string              `json:"status"`
	Attempts      int                 `json:"attempts"`
	Error         string              `json:"error"`
	NextAttemptAt time.Time           `json:"next_attempt_at"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	SentAt        *time.Time          `json:"sent_at,omitempty"`
	Channel       NotificationChannel `json:"channel"`
}

type AuditLog struct {
	ID        int64     `json:"id"`
	ActorID   *int64    `json:"actor_id,omitempty"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

type TrafficStat struct {
	ID        int64     `json:"id"`
	ServerID  int64     `json:"server_id"`
	UserID    *int64    `json:"user_id,omitempty"`
	InboundID *int64    `json:"inbound_id,omitempty"`
	Upload    int64     `json:"upload_bytes"`
	Download  int64     `json:"download_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type TrafficPeriod struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	PeriodKey string    `json:"period_key"`
	StartedAt time.Time `json:"started_at"`
	EndsAt    time.Time `json:"ends_at"`
	Upload    int64     `json:"upload_bytes"`
	Download  int64     `json:"download_bytes"`
	Limit     int64     `json:"traffic_limit_bytes"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TrafficReport struct {
	ReportID  string    `json:"report_id"`
	ServerID  int64     `json:"server_id"`
	UserID    int64     `json:"user_id"`
	InboundID *int64    `json:"inbound_id,omitempty"`
	PathID    *int64    `json:"path_id,omitempty"`
	PeriodKey string    `json:"period_key"`
	Upload    int64     `json:"upload_bytes"`
	Download  int64     `json:"download_bytes"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

type ConnectionAuditReport struct {
	ReportID            string    `json:"report_id"`
	ServerID            int64     `json:"server_id"`
	UserID              int64     `json:"user_id"`
	InboundID           *int64    `json:"inbound_id,omitempty"`
	PathID              *int64    `json:"path_id,omitempty"`
	SourceIP            string    `json:"source_ip"`
	SourceGeoCode       string    `json:"source_geo_code,omitempty"`
	SourceCountryCode   string    `json:"source_country_code,omitempty"`
	SourceCountry       string    `json:"source_country,omitempty"`
	SourceProvince      string    `json:"source_province,omitempty"`
	SourceCity          string    `json:"source_city,omitempty"`
	SourceISP           string    `json:"source_isp,omitempty"`
	GeoDatabaseRevision string    `json:"geo_database_revision,omitempty"`
	Network             string    `json:"network"`
	Destination         string    `json:"destination,omitempty"`
	DestinationPort     int       `json:"destination_port,omitempty"`
	OutboundTag         string    `json:"outbound_tag,omitempty"`
	OutboundType        string    `json:"outbound_type,omitempty"`
	ConnectionCount     int64     `json:"connection_count"`
	ActivePeak          int64     `json:"active_peak"`
	ActiveAtEnd         int64     `json:"active_at_end"`
	StartedAt           time.Time `json:"started_at"`
	EndedAt             time.Time `json:"ended_at"`
	CreatedAt           time.Time `json:"created_at"`
}

type IPGeography struct {
	CountryCode string `json:"country_code,omitempty"`
	Country     string `json:"country,omitempty"`
	Province    string `json:"province,omitempty"`
	City        string `json:"city,omitempty"`
	ISP         string `json:"isp,omitempty"`
	Revision    string `json:"revision,omitempty"`
}

type GeoDatabaseStatus struct {
	Available bool   `json:"available"`
	Provider  string `json:"provider"`
	Version   string `json:"version,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ConnectionAuditRiskEvent struct {
	Level         string    `json:"level"`
	Score         int       `json:"score"`
	SourceIPCount int       `json:"source_ip_count"`
	RegionCount   int       `json:"region_count"`
	Regions       []string  `json:"regions"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
}

type ConnectionAuditUserSummary struct {
	UserID              int64      `json:"user_id"`
	Username            string     `json:"username"`
	Nickname            string     `json:"nickname"`
	RiskLevel           string     `json:"risk_level"`
	RiskScore           int        `json:"risk_score"`
	RiskSignals         []string   `json:"risk_signals"`
	SourceIPCount       int        `json:"source_ip_count"`
	SourceSubnetCount   int        `json:"source_subnet_count"`
	SharedSourceIPCount int        `json:"shared_source_ip_count"`
	SourceRegionCount   int        `json:"source_region_count"`
	RiskSourceIPCount   int        `json:"risk_source_ip_count"`
	RiskRegionCount     int        `json:"risk_region_count"`
	RiskRegions         []string   `json:"risk_regions"`
	RiskWindowStartedAt *time.Time `json:"risk_window_started_at,omitempty"`
	RiskWindowEndedAt   *time.Time `json:"risk_window_ended_at,omitempty"`
	ServerCount         int        `json:"server_count"`
	ConnectionCount     int64      `json:"connection_count"`
	ActivePeak          int64      `json:"active_peak"`
	ReportCount         int64      `json:"report_count"`
	LastSeenAt          time.Time  `json:"last_seen_at"`
}

type ConnectionAuditDimension struct {
	Key             string    `json:"key"`
	Label           string    `json:"label"`
	Secondary       string    `json:"secondary,omitempty"`
	ConnectionCount int64     `json:"connection_count"`
	ActivePeak      int64     `json:"active_peak"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

type ConnectionAuditOverview struct {
	WindowHours        int                          `json:"window_hours"`
	RiskWindowMinutes  int                          `json:"risk_window_minutes"`
	GeneratedAt        time.Time                    `json:"generated_at"`
	GeoDatabase        GeoDatabaseStatus            `json:"geo_database"`
	EnabledServerCount int                          `json:"enabled_server_count"`
	ReportingUserCount int                          `json:"reporting_user_count"`
	ElevatedRiskCount  int                          `json:"elevated_risk_count"`
	TotalConnections   int64                        `json:"total_connections"`
	UniqueSourceIPs    int                          `json:"unique_source_ips"`
	Users              []ConnectionAuditUserSummary `json:"users"`
}

type ConnectionAuditUserDetail struct {
	Summary      ConnectionAuditUserSummary `json:"summary"`
	Sources      []ConnectionAuditDimension `json:"sources"`
	Destinations []ConnectionAuditDimension `json:"destinations"`
	Outbounds    []ConnectionAuditDimension `json:"outbounds"`
	Servers      []ConnectionAuditDimension `json:"servers"`
	Recent       []ConnectionAuditReport    `json:"recent"`
	RiskEvents   []ConnectionAuditRiskEvent `json:"risk_events"`
}

type TrafficRuntimePolicy struct {
	UserID            int64  `json:"user_id"`
	InboundID         int64  `json:"inbound_id,omitempty"`
	PathID            int64  `json:"path_id,omitempty"`
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
	EnforcementMode   string `json:"enforcement_mode,omitempty"`
}

type HealthReport struct {
	AgentID                   string       `json:"agent_id"`
	Status                    ServerStatus `json:"status"`
	PublicIPv4                string       `json:"public_ipv4"`
	PublicIPv6                string       `json:"public_ipv6"`
	RegionCode                string       `json:"region_code"`
	OS                        string       `json:"os"`
	DistroID                  string       `json:"distro_id"`
	DistroVersion             string       `json:"distro_version"`
	DistroName                string       `json:"distro_name"`
	Libc                      string       `json:"libc"`
	ServiceManager            string       `json:"service_manager"`
	PackageManager            string       `json:"package_manager"`
	Arch                      string       `json:"arch"`
	Kernel                    string       `json:"kernel"`
	CPU                       string       `json:"cpu"`
	MemoryBytes               uint64       `json:"memory_bytes"`
	CPUUsagePercent           float64      `json:"cpu_usage_percent"`
	MemoryUsedBytes           uint64       `json:"memory_used_bytes"`
	MemoryTotalBytes          uint64       `json:"memory_total_bytes"`
	AgentMemoryBytes          uint64       `json:"agent_memory_bytes"`
	DiskBytes                 uint64       `json:"disk_bytes"`
	AgentVersion              string       `json:"agent_version"`
	AgentBuild                string       `json:"agent_build"`
	SingBoxVersion            string       `json:"sing_box_version"`
	NetworkUploadBPS          uint64       `json:"network_upload_bps"`
	NetworkDownloadBPS        uint64       `json:"network_download_bps"`
	NetworkTotalUploadBytes   uint64       `json:"network_total_upload_bytes"`
	NetworkTotalDownloadBytes uint64       `json:"network_total_download_bytes"`
	ConnectivityProbeEnabled  bool         `json:"connectivity_probe_enabled"`
	ConnectivityAvailable     bool         `json:"connectivity_available"`
	ConnectivityLatencyMS     int64        `json:"connectivity_latency_ms"`
	ConnectivityCheckedAt     time.Time    `json:"connectivity_checked_at"`
	ConnectivityError         string       `json:"connectivity_error"`
	Timestamp                 time.Time    `json:"timestamp"`
}

type ServerTrafficWindow struct {
	Key   string
	Start time.Time
	End   time.Time
}

type ServerMetricSample struct {
	ID                    int64     `json:"id"`
	ServerID              int64     `json:"server_id"`
	CPUUsagePercent       float64   `json:"cpu_usage_percent"`
	MemoryUsedBytes       uint64    `json:"memory_used_bytes"`
	MemoryTotalBytes      uint64    `json:"memory_total_bytes"`
	NetworkUploadBPS      uint64    `json:"network_upload_bps"`
	NetworkDownloadBPS    uint64    `json:"network_download_bps"`
	TrafficUploadBytes    uint64    `json:"traffic_upload_bytes"`
	TrafficDownloadBytes  uint64    `json:"traffic_download_bytes"`
	ConnectivityAvailable *bool     `json:"connectivity_available,omitempty"`
	ConnectivityLatencyMS int64     `json:"connectivity_latency_ms"`
	SampledAt             time.Time `json:"sampled_at"`
}

type DashboardSummary struct {
	ServersTotal      int64 `json:"servers_total"`
	ServersOnline     int64 `json:"servers_online"`
	ServersOffline    int64 `json:"servers_offline"`
	ServersDegraded   int64 `json:"servers_degraded"`
	UsersTotal        int64 `json:"users_total"`
	UsersActive       int64 `json:"users_active"`
	TrafficUpload     int64 `json:"traffic_upload_bytes"`
	TrafficDownload   int64 `json:"traffic_download_bytes"`
	PendingTasks      int64 `json:"pending_tasks"`
	RunningTasks      int64 `json:"running_tasks"`
	FailedTasks       int64 `json:"failed_tasks"`
	LastConfigVersion int64 `json:"last_config_version"`
}

type VersionInfo struct {
	Name                 string   `json:"name"`
	Version              string   `json:"version"`
	Build                string   `json:"build"`
	Commit               string   `json:"commit"`
	BuiltAt              string   `json:"built_at"`
	Dev                  bool     `json:"dev"`
	AgentExpectedVersion string   `json:"agent_expected_version"`
	AgentExpectedBuild   string   `json:"agent_expected_build"`
	AgentUpdateRepo      string   `json:"agent_update_repo"`
	KernelVersion        string   `json:"kernel_version"`
	KernelBuild          string   `json:"kernel_build"`
	Protocols            []string `json:"protocols"`
	Kernel               string   `json:"kernel"`
	APIPrefix            string   `json:"api_prefix"`
}
