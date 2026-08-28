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
	// RoleNone marks an account with no panel permissions. It is the default
	// role for self-registered users and grants access only to self-service
	// account endpoints until an administrator assigns a user group.
	RoleNone Role = "none"
)

// HasManagementAccess reports whether a human role may use the complete
// Controller management surface. Operators intentionally share the system
// management authority of administrators; only administrator-account
// lifecycle changes remain reserved for RoleAdmin.
func HasManagementAccess(role Role) bool {
	return role == RoleAdmin || role == RoleOperator
}

func CanManageAdministratorAccounts(role Role) bool {
	return role == RoleAdmin
}

type Protocol string

const (
	ProtocolVLESS  Protocol = "vless"
	ProtocolHY2    Protocol = "hy2"
	ProtocolAnyTLS Protocol = "anytls"
	ProtocolSS     Protocol = "shadowsocks"
	ProtocolMieru  Protocol = "mieru"
	ProtocolSocks  Protocol = "socks"
	// ProtocolSnell is the Surge-owned Snell protocol. sing-box upstream
	// implements Snell v4/v6 outbounds and v5/v6 inbounds (the v5 inbound
	// accepts v4 clients, so a panel-level v4 server entry maps to the v5
	// inbound and advertises v4 to clients). UDP relay rides on the
	// established TCP stream rather than a native UDP listener.
	ProtocolSnell Protocol = "snell"
	// ProtocolSSH is a managed, password-authenticated SSH proxy entry. It is run by
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

type ServerRenewalCycle string

const (
	ServerRenewalCycleMonthly   ServerRenewalCycle = "monthly"
	ServerRenewalCycleQuarterly ServerRenewalCycle = "quarterly"
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

type ListenMode string

const (
	ListenModeAuto     ListenMode = "auto"
	ListenModeDual     ListenMode = "dual"
	ListenModeIPv4Only ListenMode = "ipv4_only"
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

type TimeCorrectionMode string

const (
	TimeCorrectionOff  TimeCorrectionMode = "off"
	TimeCorrectionAuto TimeCorrectionMode = "auto"
	TimeCorrectionNTP  TimeCorrectionMode = "ntp"
)

type ConnectivityTarget string

type LatencyProbeMode string

const (
	ConnectivityProbeTargetAuto       ConnectivityTarget = "auto"
	ConnectivityProbeTargetCloudflare ConnectivityTarget = "cloudflare"
	ConnectivityProbeTarget12306      ConnectivityTarget = "12306"
	ConnectivityProbeTargetGoogle     ConnectivityTarget = "google"
	LatencyProbeModeTCP               LatencyProbeMode   = "tcp"
	LatencyProbeModeICMP              LatencyProbeMode   = "icmp"
)

type RouteAction string

const (
	RouteActionDirect       RouteAction = "direct"
	RouteActionBlock        RouteAction = "block"
	RouteActionOutbound     RouteAction = "outbound"
	RouteActionExternal     RouteAction = "external"
	RouteActionProxyPath    RouteAction = "proxy_path"
	RouteActionFamilySplit  RouteAction = "family_split"
	RouteActionInterface    RouteAction = "interface"
	RouteActionSourcePrefix RouteAction = "source_prefix"
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
	ID                            int64                        `json:"id"`
	Username                      string                       `json:"username"`
	Nickname                      string                       `json:"nickname"`
	PasswordHash                  string                       `json:"-"`
	SessionVersion                int64                        `json:"-"`
	Role                          Role                         `json:"role"`
	Status                        string                       `json:"status"`
	ProxyUUID                     string                       `json:"proxy_uuid"`
	ProxyPassword                 string                       `json:"proxy_password"`
	SSHRandomID                   string                       `json:"-"`
	SpeedLimitMbps                int                          `json:"speed_limit_mbps"`
	TrafficLimitBytes             int64                        `json:"traffic_limit_bytes"`
	TrafficUsedBytes              int64                        `json:"traffic_used_bytes"`
	TrafficResetMode              string                       `json:"traffic_reset_mode"`
	TrafficResetDay               int                          `json:"traffic_reset_day"`
	TrafficPeriodKey              string                       `json:"traffic_period_key,omitempty"`
	TrafficPeriodEnd              string                       `json:"traffic_period_end,omitempty"`
	TrafficQuotaState             string                       `json:"traffic_quota_state,omitempty"`
	SubscriptionToken             string                       `json:"subscription_token,omitempty"`
	SubscriptionBurnAfterRead     bool                         `json:"subscription_burn_after_read"`
	SubscriptionBurnedAt          *time.Time                   `json:"subscription_burned_at,omitempty"`
	SubscriptionAgeEnabled        bool                         `json:"subscription_age_enabled"`
	SubscriptionAgePublicKey      string                       `json:"subscription_age_public_key,omitempty"`
	SubscriptionSuspended         bool                         `json:"subscription_suspended"`
	SubscriptionSuspendedAt       *time.Time                   `json:"subscription_suspended_at,omitempty"`
	SubscriptionSuspendReason     string                       `json:"subscription_suspend_reason,omitempty"`
	SubscriptionCustomPath        string                       `json:"subscription_custom_path,omitempty"`
	SubscriptionCustomPathPolicy  SubscriptionCustomPathPolicy `json:"subscription_custom_path_policy"`
	SubscriptionCustomPathEnabled bool                         `json:"subscription_custom_path_enabled"`
	SubscriptionCustomPathSource  string                       `json:"subscription_custom_path_source,omitempty"`
	DeviceLimit                   int                          `json:"device_limit"`
	LegacyProxyEnabled            bool                         `json:"legacy_proxy_enabled"`
	LegacyProxyEnabledSet         bool                         `json:"-"`
	DeviceIDHash                  string                       `json:"device_id_hash,omitempty"`
	CredentialEpoch               int64                        `json:"credential_epoch,omitempty"`
	CredentialSeed                string                       `json:"-"`
	CredentialStatus              string                       `json:"credential_status,omitempty"`
	Protected                     bool                         `json:"protected,omitempty"`
	CreatedAt                     time.Time                    `json:"created_at"`
	UpdatedAt                     time.Time                    `json:"updated_at"`
}

type UserDevice struct {
	ID                      string     `json:"id"`
	DeviceIDHash            string     `json:"device_id_hash"`
	UserID                  int64      `json:"user_id"`
	Name                    string     `json:"name"`
	TokenHash               string     `json:"-"`
	TokenPrefix             string     `json:"token_prefix"`
	CredentialEpoch         int64      `json:"credential_epoch"`
	Status                  string     `json:"status"`
	SubscriptionSuspended   bool       `json:"subscription_suspended"`
	ProxyAccessState        string     `json:"proxy_access_state"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	LastSubscriptionAt      *time.Time `json:"last_subscription_at,omitempty"`
	LastProxyActivityAt     *time.Time `json:"last_proxy_activity_at,omitempty"`
	RevokedAt               *time.Time `json:"revoked_at,omitempty"`
	SubscriptionSuspendedAt *time.Time `json:"subscription_suspended_at,omitempty"`
}

type UserDeviceCredential struct {
	Device UserDevice `json:"device"`
	Token  string     `json:"device_token,omitempty"`
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
	SubscriptionFormatAuto         SubscriptionFormat = "auto"
	SubscriptionFormatStash        SubscriptionFormat = "stash"
	SubscriptionFormatMihomo       SubscriptionFormat = "mihomo"
	SubscriptionFormatSurfboard    SubscriptionFormat = "surfboard"
	SubscriptionFormatSurge        SubscriptionFormat = "surge"
	SubscriptionFormatSurgeMac     SubscriptionFormat = "surge-mac"
	SubscriptionFormatLoon         SubscriptionFormat = "loon"
	SubscriptionFormatEgern        SubscriptionFormat = "egern"
	SubscriptionFormatShadowrocket SubscriptionFormat = "shadowrocket"
	SubscriptionFormatQX           SubscriptionFormat = "qx"
	SubscriptionFormatSingBox      SubscriptionFormat = "sing-box"
	SubscriptionFormatSingBoxMieru SubscriptionFormat = "sing-box-mieru"
	SubscriptionFormatV2Ray        SubscriptionFormat = "v2ray"
	SubscriptionFormatV2RayURI     SubscriptionFormat = "v2ray-uri"
)

// SubscriptionClientTemplate is a client configuration shell. Protocol field
// conversion stays in Controller renderers and must not be stored here.
type SubscriptionClientTemplate struct {
	Format            SubscriptionFormat `json:"format"`
	Label             string             `json:"label"`
	Content           string             `json:"content"`
	Source            string             `json:"source"`
	Revision          int64              `json:"revision"`
	BuiltinDigest     string             `json:"builtin_digest"`
	BaseBuiltinDigest string             `json:"base_builtin_digest,omitempty"`
	BuiltinUpdated    bool               `json:"builtin_updated,omitempty"`
	Markers           []string           `json:"markers"`
	UpdatedBy         *int64             `json:"updated_by,omitempty"`
	UpdatedAt         *time.Time         `json:"updated_at,omitempty"`
}

// PrivateSubscriptionProtocol is intentionally separate from Protocol. These
// nodes are rendered by Controller for a user's subscription and never enter
// Agent or kernel desired state.
type PrivateSubscriptionProtocol string

const (
	PrivateProtocolVLESS       PrivateSubscriptionProtocol = "vless"
	PrivateProtocolVMess       PrivateSubscriptionProtocol = "vmess"
	PrivateProtocolTrojan      PrivateSubscriptionProtocol = "trojan"
	PrivateProtocolTUIC        PrivateSubscriptionProtocol = "tuic"
	PrivateProtocolHysteria2   PrivateSubscriptionProtocol = "hysteria2"
	PrivateProtocolAnyTLS      PrivateSubscriptionProtocol = "anytls"
	PrivateProtocolShadowsocks PrivateSubscriptionProtocol = "shadowsocks"
	PrivateProtocolSOCKS5      PrivateSubscriptionProtocol = "socks5"
	PrivateProtocolMieru       PrivateSubscriptionProtocol = "mieru"
)

type NodeGroupKind string

const (
	NodeGroupOBoard NodeGroupKind = "oboard"
	NodeGroupRemote NodeGroupKind = "remote"
	NodeGroupManual NodeGroupKind = "manual"
)

type NodeGroup struct {
	ID        int64         `json:"id"`
	UserID    int64         `json:"user_id"`
	Kind      NodeGroupKind `json:"kind"`
	SystemKey string        `json:"system_key,omitempty"`
	Name      string        `json:"name"`
	Position  int           `json:"position"`
	NodeCount int           `json:"node_count"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type NodeSource struct {
	ID             int64      `json:"id"`
	UserID         int64      `json:"user_id"`
	GroupID        int64      `json:"group_id"`
	URLFingerprint string     `json:"-"`
	URLEncrypted   string     `json:"-"`
	URLDisplay     string     `json:"url_display"`
	ETag           string     `json:"-"`
	LastModified   string     `json:"-"`
	Status         string     `json:"status"`
	LastError      string     `json:"last_error,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type ImportedNode struct {
	ID              int64                       `json:"id"`
	UserID          int64                       `json:"user_id"`
	GroupID         int64                       `json:"group_id"`
	SourceID        *int64                      `json:"source_id,omitempty"`
	Protocol        PrivateSubscriptionProtocol `json:"protocol"`
	Name            string                      `json:"name"`
	Fingerprint     string                      `json:"fingerprint"`
	ConfigEncrypted string                      `json:"-"`
	Position        int                         `json:"position"`
	Enabled         bool                        `json:"enabled"`
	CreatedAt       time.Time                   `json:"created_at"`
	UpdatedAt       time.Time                   `json:"updated_at"`
}

// SubscriptionOutputFilterType is one rule kind of a subscription output's
// ordered filter pipeline (Sub-Store style). Rules run in order against the
// final merged node list; a node dropped by any rule stays removed.
const (
	// SubscriptionOutputFilterKeepName keeps nodes whose effective name
	// (region-flag prefix stripped) matches the Go regexp in Value.
	SubscriptionOutputFilterKeepName = "keep_name"
	// SubscriptionOutputFilterDropName removes nodes whose effective name
	// (region-flag prefix stripped) matches the Go regexp in Value.
	SubscriptionOutputFilterDropName = "drop_name"
	// SubscriptionOutputFilterKeepProtocol keeps nodes whose protocol type
	// equals Value (lowercase protocol key).
	SubscriptionOutputFilterKeepProtocol = "keep_protocol"
	// SubscriptionOutputFilterDropProtocol removes nodes whose protocol type
	// equals Value.
	SubscriptionOutputFilterDropProtocol = "drop_protocol"
	// SubscriptionOutputFilterKeepRegion keeps nodes whose exit region (or
	// entry region fallback) equals Value (ISO 3166-1 alpha-2). Nodes
	// without a region never match.
	SubscriptionOutputFilterKeepRegion = "keep_region"
	// SubscriptionOutputFilterDropRegion removes nodes whose exit region (or
	// entry region fallback) equals Value. Nodes without a region never
	// match and therefore survive.
	SubscriptionOutputFilterDropRegion = "drop_region"
	// SubscriptionOutputFilterKeepGroup keeps nodes that belong to the node
	// group whose ID equals Value.
	SubscriptionOutputFilterKeepGroup = "keep_group"
	// SubscriptionOutputFilterDropGroup removes nodes that belong to the
	// node group whose ID equals Value.
	SubscriptionOutputFilterDropGroup = "drop_group"
)

// SubscriptionOutputFilter is one ordered filter rule of a subscription
// output. The ordered rule list is a pipeline applied at render time to the
// final merged node list of the profile.
type SubscriptionOutputFilter struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type SubscriptionOutput struct {
	ID        int64                      `json:"id"`
	UserID    int64                      `json:"user_id"`
	Name      string                     `json:"name"`
	IsDefault bool                       `json:"is_default"`
	Enabled   bool                       `json:"enabled"`
	GroupIDs  []int64                    `json:"group_ids"`
	Filters   []SubscriptionOutputFilter `json:"filters"`
	CreatedAt time.Time                  `json:"created_at"`
	UpdatedAt time.Time                  `json:"updated_at"`
}

// AssignableNodeType identifies a client-visible node in the assignable node
// catalog. proxy_path and external_outbound are the final assignable units;
// inbound is transitional and only covers standalone inbounds that have no
// proxy-path branches yet (they become zero-step direct proxy_paths during the
// legacy data migration).
type AssignableNodeType string

const (
	AssignableNodeProxyPath        AssignableNodeType = "proxy_path"
	AssignableNodeExternalOutbound AssignableNodeType = "external_outbound"
	AssignableNodeInbound          AssignableNodeType = "inbound"
)

// PlanNodeSourceType describes how a plan node was added. Only explicit is
// supported today; rule-backed nodes are a later milestone and stay rejected by
// validation until then.
type PlanNodeSourceType string

const (
	PlanNodeSourceExplicit PlanNodeSourceType = "explicit"
	PlanNodeSourceRule     PlanNodeSourceType = "rule"
)

// SubscriptionPlan is the single axis of node authorization: a plan owns a node
// set plus the service limits of that subscription product. User groups no
// longer grant nodes; a user's effective nodes come from the one current plan
// revision combined with temporary per-user exceptions.
//
// Limits and the node set belong to revisions, not to the plan row itself.
// SpeedLimitMbps/TrafficLimitBytes/TrafficResetMode/TrafficResetDay mirror the
// current revision for read APIs. Every save creates an immutable new version:
// LockVersion guards concurrent writes, CurrentRevisionID is what bound users
// actually get, LatestRevisionID is the newest saved snapshot the editor works
// from, and PendingRevisionID (when set) is a version being applied to agents.
type SubscriptionPlan struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Enabled           bool   `json:"enabled"`
	SpeedLimitMbps    int    `json:"speed_limit_mbps"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes"`
	TrafficResetMode  string `json:"traffic_reset_mode"`
	TrafficResetDay   int    `json:"traffic_reset_day"`
	LockVersion       int64  `json:"lock_version"`
	CurrentRevisionID int64  `json:"current_revision_id"`
	LatestRevisionID  int64  `json:"latest_revision_id"`
	PendingRevisionID int64  `json:"pending_revision_id,omitempty"`
	// Revision, ActiveRevisionID and DraftRevisionID are legacy aliases kept
	// for read compatibility during the phased migration. They mirror
	// LockVersion, CurrentRevisionID and (when a frozen legacy draft exists)
	// the old draft pointer. New writes never touch the legacy columns.
	Revision         int64     `json:"revision"`
	ActiveRevisionID int64     `json:"active_revision_id"`
	DraftRevisionID  int64     `json:"draft_revision_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const (
	TrafficResetMonthly          = "monthly"
	TrafficResetMonthDay         = "month_day"
	TrafficResetAnniversaryMonth = "anniversary_month"
	TrafficResetNever            = "never"
)

// PlanRevisionStatus is the legacy lifecycle of one plan revision row. It is
// kept only for read compatibility: the draft/active/archived wording belongs
// to the old model and new versions derive their state from the plan pointers
// (current/latest/pending) instead.
type PlanRevisionStatus string

const (
	PlanRevisionDraft    PlanRevisionStatus = "draft"
	PlanRevisionActive   PlanRevisionStatus = "active"
	PlanRevisionArchived PlanRevisionStatus = "archived"
)

// PlanChangeKind describes why an immutable plan version was created. It is
// shown in the version history and used to decide whether a version affects
// authorization (nodes or limits) or is presentation-only (ordering).
const (
	PlanChangeKindCreate               = "create"
	PlanChangeKindSettings             = "settings"
	PlanChangeKindNodes                = "nodes"
	PlanChangeKindOrdering             = "ordering"
	PlanChangeKindPresentation         = "presentation"
	PlanChangeKindMixed                = "mixed"
	PlanChangeKindRestore              = "restore"
	PlanChangeKindClone                = "clone"
	PlanChangeKindLegacyDraftMigration = "legacy_draft_migration"
)

// SubscriptionPlanRevision is one immutable snapshot of a plan: its limits plus
// the isolated node set in subscription_plan_revision_nodes. Versions are
// never updated after creation; editing always creates the next version.
type SubscriptionPlanRevision struct {
	ID                    int64                       `json:"id"`
	PlanID                int64                       `json:"plan_id"`
	VersionNo             int64                       `json:"version_no"`
	BasedOnRevisionID     int64                       `json:"based_on_revision_id,omitempty"`
	ChangeKind            string                      `json:"change_kind,omitempty"`
	ChangeSummary         string                      `json:"change_summary,omitempty"`
	ActivationChangeID    *int64                      `json:"activation_change_id,omitempty"`
	Revision              int64                       `json:"revision"`
	Status                PlanRevisionStatus          `json:"status"`
	SpeedLimitMbps        int                         `json:"speed_limit_mbps"`
	TrafficLimitBytes     int64                       `json:"traffic_limit_bytes"`
	TrafficResetMode      string                      `json:"traffic_reset_mode"`
	TrafficResetDay       int                         `json:"traffic_reset_day"`
	NodeOrderPolicy       SubscriptionNodeOrderPolicy `json:"node_order_policy,omitempty"`
	OrderTemplateID       *int64                      `json:"order_template_id,omitempty"`
	OrderTemplateRevision int64                       `json:"order_template_revision"`
	OrderSourcePlanID     *int64                      `json:"order_source_plan_id,omitempty"`
	OrderSourceRevisionID *int64                      `json:"order_source_revision_id,omitempty"`
	OrderSourceMode       string                      `json:"order_source_mode,omitempty"`
	CreatedBy             *int64                      `json:"created_by,omitempty"`
	CreatedAt             time.Time                   `json:"created_at"`
	ActivatedAt           *time.Time                  `json:"activated_at,omitempty"`
}

// SubscriptionPlanRevisionNode is one node of a frozen revision. Rows are
// immutable after the revision is activated; only the draft revision's node set
// can change.
type SubscriptionPlanRevisionNode struct {
	ID                  int64              `json:"id"`
	RevisionID          int64              `json:"revision_id"`
	NodeType            AssignableNodeType `json:"node_type"`
	NodeID              int64              `json:"node_id"`
	DisplayGroup        string             `json:"display_group"`
	SourceType          PlanNodeSourceType `json:"source_type"`
	SourceRuleID        int64              `json:"source_rule_id,omitempty"`
	SortPosition        *int               `json:"sort_position,omitempty"`
	DisplayNameOverride *string            `json:"display_name_override,omitempty"`
	CreatedAt           time.Time          `json:"created_at"`
}

type PlanMembershipRule struct {
	ID         int64     `json:"id"`
	RevisionID int64     `json:"revision_id"`
	RuleID     int64     `json:"rule_id"`
	Kind       string    `json:"kind"`
	ScopeKey   string    `json:"scope_key"`
	CreatedAt  time.Time `json:"created_at"`
}

type PlanNodeExclusion struct {
	RevisionID int64              `json:"revision_id"`
	NodeType   AssignableNodeType `json:"node_type"`
	NodeID     int64              `json:"node_id"`
	CreatedAt  time.Time          `json:"created_at"`
}

type PlanRuleReconcileState struct {
	PlanID           int64      `json:"plan_id"`
	CatalogDigest    string     `json:"catalog_digest"`
	DesiredDigest    string     `json:"desired_digest"`
	Status           string     `json:"status"`
	LastError        string     `json:"last_error,omitempty"`
	LastReconciledAt *time.Time `json:"last_reconciled_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// SubscriptionNodeOrderMode controls how a plan revision's nodes are ordered in
// every rendered subscription format. The order is presentation metadata owned
// by the revision snapshot: it is copied into drafts, cloned, restored and
// published together with the node set.
type SubscriptionNodeOrderMode string

const (
	// SubscriptionNodeOrderLegacyGroupName preserves the pre-ordering behavior
	// (DisplayGroup then node name with stable input order). It is the
	// migration default for existing revisions so upgrades never change an
	// already-issued subscription body or ETag.
	SubscriptionNodeOrderLegacyGroupName SubscriptionNodeOrderMode = "legacy_group_name"
	// SubscriptionNodeOrderExitRegion orders by exit region first.
	SubscriptionNodeOrderExitRegion SubscriptionNodeOrderMode = "exit_region"
	// SubscriptionNodeOrderEntry orders by entry region then exact inbound.
	SubscriptionNodeOrderEntry SubscriptionNodeOrderMode = "entry"
	// SubscriptionNodeOrderManual uses explicit sort_position values.
	SubscriptionNodeOrderManual SubscriptionNodeOrderMode = "manual"
)

const (
	// SubscriptionNodeOrderVersion is the current policy snapshot version.
	SubscriptionNodeOrderVersion = 2
	// SubscriptionNodeEntryRegionOrderInheritExit makes the entry region order
	// follow the exit region order.
	SubscriptionNodeEntryRegionOrderInheritExit = "inherit_exit"
	// SubscriptionNodeEntryRegionOrderCustom uses the explicit entry region
	// order list.
	SubscriptionNodeEntryRegionOrderCustom = "custom"
)

type SubscriptionNodePlacement string

const (
	SubscriptionNodePlacementByTemplate SubscriptionNodePlacement = "by_template"
	SubscriptionNodePlacementAppend     SubscriptionNodePlacement = "append"
	SubscriptionNodePlacementPending    SubscriptionNodePlacement = "pending"
)

// SubscriptionNodeOrderPolicy is a versioned JSON snapshot stored on each plan
// revision. Region lists are ISO 3166-1 alpha-2 codes normalized to upper
// case; EntryOrder holds stable "inbound:<id>" keys.
type SubscriptionNodeOrderPolicy struct {
	Version              int                       `json:"version"`
	Mode                 SubscriptionNodeOrderMode `json:"mode"`
	ManualSeed           SubscriptionNodeOrderMode `json:"manual_seed,omitempty"`
	ExitRegionOrder      []string                  `json:"exit_region_order"`
	EntryRegionOrderMode string                    `json:"entry_region_order_mode"`
	EntryRegionOrder     []string                  `json:"entry_region_order"`
	EntryOrder           []string                  `json:"entry_order"`
	NewNodePlacement     SubscriptionNodePlacement `json:"new_node_placement,omitempty"`
	UnmatchedPlacement   SubscriptionNodePlacement `json:"unmatched_placement,omitempty"`
}

// DefaultSubscriptionNodeOrderPolicy returns the migration default used for
// existing revisions: legacy ordering with an exit-region manual seed.
func DefaultSubscriptionNodeOrderPolicy() SubscriptionNodeOrderPolicy {
	return SubscriptionNodeOrderPolicy{
		Version:              1,
		Mode:                 SubscriptionNodeOrderLegacyGroupName,
		ManualSeed:           SubscriptionNodeOrderExitRegion,
		ExitRegionOrder:      []string{},
		EntryRegionOrderMode: SubscriptionNodeEntryRegionOrderInheritExit,
		EntryRegionOrder:     []string{},
		EntryOrder:           []string{},
		NewNodePlacement:     SubscriptionNodePlacementPending,
		UnmatchedPlacement:   SubscriptionNodePlacementPending,
	}
}

// DefaultSubscriptionNodeOrderPolicyJSON is the exact legacy snapshot used as
// the column default so upgraded revisions keep their existing subscription
// order without a rewrite.
func DefaultSubscriptionNodeOrderPolicyJSON() string {
	return `{"version":1,"mode":"legacy_group_name","manual_seed":"exit_region","exit_region_order":[],"entry_region_order_mode":"inherit_exit","entry_region_order":[],"entry_order":[]}`
}

// NewSubscriptionNodeOrderPolicy is the default for newly created plans:
// exit-region ordering with the entry region order following the exit order.
func NewSubscriptionNodeOrderPolicy() SubscriptionNodeOrderPolicy {
	return SubscriptionNodeOrderPolicy{
		Version:              SubscriptionNodeOrderVersion,
		Mode:                 SubscriptionNodeOrderExitRegion,
		ManualSeed:           SubscriptionNodeOrderExitRegion,
		ExitRegionOrder:      []string{},
		EntryRegionOrderMode: SubscriptionNodeEntryRegionOrderInheritExit,
		EntryRegionOrder:     []string{},
		EntryOrder:           []string{},
		NewNodePlacement:     SubscriptionNodePlacementByTemplate,
		UnmatchedPlacement:   SubscriptionNodePlacementAppend,
	}
}

type SubscriptionPlanNode struct {
	ID                  int64              `json:"id"`
	PlanID              int64              `json:"plan_id"`
	RevisionID          int64              `json:"revision_id,omitempty"`
	NodeType            AssignableNodeType `json:"node_type"`
	NodeID              int64              `json:"node_id"`
	DisplayGroup        string             `json:"display_group"`
	SourceType          PlanNodeSourceType `json:"source_type"`
	SourceRuleID        int64              `json:"source_rule_id,omitempty"`
	SortPosition        *int               `json:"sort_position,omitempty"`
	DisplayNameOverride *string            `json:"display_name_override,omitempty"`
	Enabled             bool               `json:"enabled"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

type AssignableNodeMetadata struct {
	NodeType            AssignableNodeType `json:"node_type"`
	NodeID              int64              `json:"node_id"`
	DisplayNameOverride *string            `json:"display_name_override"`
	LockVersion         int64              `json:"lock_version"`
	CreatedBy           *int64             `json:"created_by,omitempty"`
	UpdatedBy           *int64             `json:"updated_by,omitempty"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

type NodeOrderTemplatePolicy struct {
	Version              int                       `json:"version"`
	BaseMode             SubscriptionNodeOrderMode `json:"base_mode"`
	ExitRegionOrder      []string                  `json:"exit_region_order"`
	EntryRegionOrderMode string                    `json:"entry_region_order_mode"`
	EntryRegionOrder     []string                  `json:"entry_region_order"`
	EntryOrder           []string                  `json:"entry_order"`
	NewNodePlacement     SubscriptionNodePlacement `json:"new_node_placement"`
	UnmatchedPlacement   SubscriptionNodePlacement `json:"unmatched_placement"`
}

type NodeOrderTemplate struct {
	ID          int64                   `json:"id"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Enabled     bool                    `json:"enabled"`
	Revision    int64                   `json:"revision"`
	Policy      NodeOrderTemplatePolicy `json:"policy"`
	CreatedBy   *int64                  `json:"created_by,omitempty"`
	UpdatedBy   *int64                  `json:"updated_by,omitempty"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
	UsageCount  int                     `json:"usage_count"`
}

// UserPlanBinding attaches one plan to a user. At most one enabled binding may
// exist per user (enforced by a partial unique index), so switching plans is an
// atomic replace rather than a union of plans.
type UserPlanBinding struct {
	ID                   int64      `json:"id"`
	UserID               int64      `json:"user_id"`
	PlanID               int64      `json:"plan_id"`
	Enabled              bool       `json:"enabled"`
	StartsAt             *time.Time `json:"starts_at,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	TrafficResetAnchorAt *time.Time `json:"traffic_reset_anchor_at,omitempty"`
	AssignedBy           *int64     `json:"assigned_by,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type UserNodeExceptionEffect string

const (
	UserNodeExceptionAllow UserNodeExceptionEffect = "allow"
	UserNodeExceptionDeny  UserNodeExceptionEffect = "deny"
)

// UserNodeException is an audited per-user node override. It never carries
// speed, traffic, or display grouping settings. Empty reason means no remark;
// nil ExpiresAt means permanent authorization.
type UserNodeException struct {
	ID        int64                   `json:"id"`
	UserID    int64                   `json:"user_id"`
	NodeType  AssignableNodeType      `json:"node_type"`
	NodeID    int64                   `json:"node_id"`
	Effect    UserNodeExceptionEffect `json:"effect"`
	Reason    string                  `json:"reason"`
	Status    UserNodeExceptionStatus `json:"status"`
	StartsAt  *time.Time              `json:"starts_at,omitempty"`
	ExpiresAt *time.Time              `json:"expires_at,omitempty"`
	CreatedBy *int64                  `json:"created_by,omitempty"`
	CreatedAt time.Time               `json:"created_at"`
}

// UserNodeExceptionStatus is the audited lifecycle of one exception. Allow
// exceptions are created pending and become active only when their change is
// activated (so the node never appears in a subscription before the servers
// hold the credential); deny exceptions are active immediately so the node
// disappears from subscriptions right away. Expired and revoked rows are kept
// for the audit trail.
type UserNodeExceptionStatus string

const (
	UserNodeExceptionPending UserNodeExceptionStatus = "pending"
	UserNodeExceptionActive  UserNodeExceptionStatus = "active"
	UserNodeExceptionExpired UserNodeExceptionStatus = "expired"
	UserNodeExceptionRevoked UserNodeExceptionStatus = "revoked"
)

// AccessChangeType is the kind of authorization mutation one access change
// carries through prepare/activate/finalize.
type AccessChangeType string

const (
	AccessChangePlanPublish  AccessChangeType = "plan_publish"
	AccessChangePlanRestore  AccessChangeType = "plan_restore"
	AccessChangePlanDisable  AccessChangeType = "plan_disable"
	AccessChangeUserBindings AccessChangeType = "user_bindings"
	AccessChangeExceptions   AccessChangeType = "exceptions"
)

// AccessChangeStatus is the orchestration state machine. Prepare deploys the
// old-union-new permission set, activation atomically switches the durable
// authorization state (publish revision, enable plan, activate exceptions),
// and finalize deploys the exact new set so removed credentials are pruned.
type AccessChangeStatus string

const (
	AccessChangePreparing  AccessChangeStatus = "preparing"
	AccessChangeActivating AccessChangeStatus = "activating"
	AccessChangeFinalizing AccessChangeStatus = "finalizing"
	AccessChangeFinalized  AccessChangeStatus = "finalized"
	AccessChangeFailed     AccessChangeStatus = "failed"
	AccessChangeCancelled  AccessChangeStatus = "cancelled"
)

// AccessChangeTargetStatus is the per-server phase state inside one change.
type AccessChangeTargetStatus string

const (
	AccessChangeTargetPending    AccessChangeTargetStatus = "pending"
	AccessChangeTargetPreparing  AccessChangeTargetStatus = "preparing"
	AccessChangeTargetPrepared   AccessChangeTargetStatus = "prepared"
	AccessChangeTargetFinalizing AccessChangeTargetStatus = "finalizing"
	AccessChangeTargetFinalized  AccessChangeTargetStatus = "finalized"
	AccessChangeTargetFailed     AccessChangeTargetStatus = "failed"
)

// AccessChange is one authorization mutation with a two-phase deployment. The
// prepare/finalize projections are materialized at creation time so the
// orchestration survives restarts and later plan edits without changing what
// the change deploys.
type AccessChange struct {
	ID                       int64                `json:"id"`
	ChangeType               AccessChangeType     `json:"change_type"`
	SourcePlanID             int64                `json:"source_plan_id,omitempty"`
	CandidateRevisionID      int64                `json:"candidate_revision_id,omitempty"`
	ExpectedActiveRevisionID int64                `json:"expected_active_revision_id,omitempty"`
	Status                   AccessChangeStatus   `json:"status"`
	PreviewHash              string               `json:"preview_hash,omitempty"`
	AffectedUserCount        int                  `json:"affected_user_count"`
	ActivateAt               *time.Time           `json:"activate_at,omitempty"`
	PayloadJSON              string               `json:"-"`
	PrepareProjectionJSON    string               `json:"-"`
	FinalizeProjectionJSON   string               `json:"-"`
	Error                    string               `json:"error,omitempty"`
	CreatedBy                *int64               `json:"created_by,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
	ActivatedAt              *time.Time           `json:"activated_at,omitempty"`
	FinalizedAt              *time.Time           `json:"finalized_at,omitempty"`
	FailedAt                 *time.Time           `json:"failed_at,omitempty"`
	Targets                  []AccessChangeTarget `json:"targets,omitempty"`
}

// AccessChangeTarget tracks the per-server Agent tasks of one change phase.
type AccessChangeTarget struct {
	AccessChangeID int64                    `json:"access_change_id"`
	ServerID       int64                    `json:"server_id"`
	PrepareTaskID  int64                    `json:"prepare_task_id,omitempty"`
	FinalizeTaskID int64                    `json:"finalize_task_id,omitempty"`
	Status         AccessChangeTargetStatus `json:"status"`
	Error          string                   `json:"error,omitempty"`
	UpdatedAt      time.Time                `json:"updated_at"`
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

type SubscriptionRelay struct {
	ID                     int64      `json:"id"`
	Name                   string     `json:"name"`
	PublicURL              string     `json:"public_url"`
	Status                 string     `json:"status"`
	TokenHash              string     `json:"-"`
	SigningSecretEncrypted string     `json:"-"`
	EnrollmentHash         string     `json:"-"`
	EnrollmentExpiresAt    *time.Time `json:"enrollment_expires_at,omitempty"`
	Version                string     `json:"version"`
	Build                  string     `json:"build"`
	Commit                 string     `json:"commit"`
	OS                     string     `json:"os"`
	Arch                   string     `json:"arch"`
	ServiceManager         string     `json:"service_manager"`
	UpdateTargetVersion    string     `json:"update_target_version"`
	UpdateTargetBuild      string     `json:"update_target_build"`
	UpdateRequestedAt      *time.Time `json:"update_requested_at,omitempty"`
	LastUpdateError        string     `json:"last_update_error,omitempty"`
	LastSeenAt             *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type Server struct {
	ID                          int64                `json:"id"`
	Name                        string               `json:"name"`
	AgentID                     string               `json:"agent_id"`
	AgentTokenHash              string               `json:"-"`
	ChainSecret                 string               `json:"-"`
	EnrollmentHash              string               `json:"-"`
	EnrollmentExpiresAt         *time.Time           `json:"-"`
	EntryAddress                string               `json:"entry_address"`
	PublicIPv4                  string               `json:"public_ipv4"`
	PublicIPv6                  string               `json:"public_ipv6"`
	InterfaceIPv6               string               `json:"interface_ipv6"`
	RegionCode                  string               `json:"region_code"`
	DetectedRegionCode          string               `json:"detected_region_code"`
	RegionMode                  string               `json:"region_mode"`
	EntryIPMode                 EntryIPMode          `json:"entry_ip_mode"`
	ListenIP                    string               `json:"listen_ip"`
	ListenMode                  ListenMode           `json:"listen_mode"`
	IPStack                     IPStack              `json:"ip_stack"`
	UDPInboundMode              UDPInboundMode       `json:"udp_inbound_mode"`
	MTUMode                     MTUMode              `json:"mtu_mode"`
	MTUValue                    int                  `json:"mtu_value"`
	MTUProbeHost                string               `json:"mtu_probe_host"`
	MTUProbePort                int                  `json:"mtu_probe_port"`
	MTUOverheadBytes            int                  `json:"mtu_overhead_bytes"`
	BBREnabled                  bool                 `json:"bbr_enabled"`
	PortRangeStart              int                  `json:"port_range_start"`
	PortRangeEnd                int                  `json:"port_range_end"`
	InternalPortRangeStart      int                  `json:"internal_port_range_start"`
	InternalPortRangeEnd        int                  `json:"internal_port_range_end"`
	PortPolicyRevision          int64                `json:"port_policy_revision"`
	Status                      ServerStatus         `json:"status"`
	OS                          string               `json:"os"`
	DistroID                    string               `json:"distro_id"`
	DistroVersion               string               `json:"distro_version"`
	DistroName                  string               `json:"distro_name"`
	Libc                        string               `json:"libc"`
	ServiceManager              string               `json:"service_manager"`
	PackageManager              string               `json:"package_manager"`
	Arch                        string               `json:"arch"`
	Kernel                      string               `json:"kernel"`
	CPU                         string               `json:"cpu"`
	MemoryBytes                 uint64               `json:"memory_bytes"`
	CPUUsagePercent             float64              `json:"cpu_usage_percent"`
	MemoryUsedBytes             uint64               `json:"memory_used_bytes"`
	MemoryTotalBytes            uint64               `json:"memory_total_bytes"`
	AgentMemoryBytes            uint64               `json:"agent_memory_bytes"`
	DiskBytes                   uint64               `json:"disk_bytes"`
	DiskTotalBytes              uint64               `json:"disk_total_bytes"`
	TCPConnectionCount          uint64               `json:"tcp_connection_count"`
	UDPConnectionCount          uint64               `json:"udp_connection_count"`
	ProcessCount                uint64               `json:"process_count"`
	AgentVersion                string               `json:"agent_version"`
	AgentBuild                  string               `json:"agent_build"`
	SingBoxVersion              string               `json:"sing_box_version"`
	KernelCapabilities          []string             `json:"kernel_capabilities,omitempty"`
	TCPFastOpenState            string               `json:"tcp_fastopen_state"`
	TCPFastOpenValue            int                  `json:"tcp_fastopen_value"`
	MonitoringMode              string               `json:"monitoring_mode"`
	ResourceHistoryEnabled      bool                 `json:"resource_history_enabled"`
	ResourceHistoryConfigured   bool                 `json:"-"`
	TrafficResetMode            string               `json:"traffic_reset_mode"`
	TrafficResetDay             int                  `json:"traffic_reset_day"`
	TrafficLimitBytes           int64                `json:"traffic_limit_bytes"`
	NetworkUploadBPS            uint64               `json:"network_upload_bps"`
	NetworkDownloadBPS          uint64               `json:"network_download_bps"`
	TrafficUploadBytes          uint64               `json:"traffic_upload_bytes"`
	TrafficDownloadBytes        uint64               `json:"traffic_download_bytes"`
	TrafficPeriodStart          string               `json:"traffic_period_start"`
	TrafficPeriodEnd            string               `json:"traffic_period_end"`
	ConnectivityProbeEnabled    bool                 `json:"-"`
	ConnectivityProbeTarget     ConnectivityTarget   `json:"-"`
	LatencyProbeEnabled         bool                 `json:"latency_probe_enabled"`
	LatencyProbeMode            LatencyProbeMode     `json:"latency_probe_mode"`
	LatencyProbePublicTarget    ConnectivityTarget   `json:"latency_probe_public_target"`
	LatencyProbeIntervalSeconds int                  `json:"latency_probe_interval_seconds"`
	LatencyProbeSampleCount     int                  `json:"latency_probe_sample_count"`
	LatencyProbeRegions         []LatencyProbeRegion `json:"latency_probe_regions,omitempty"`
	LatencyProbeMaxTargets      int                  `json:"latency_probe_max_targets"`
	LatencyProbeResourceVersion string               `json:"latency_probe_resource_version,omitempty"`
	ConnectionAuditEnabled      bool                 `json:"connection_audit_enabled"`
	OfflineNotifyEnabled        bool                 `json:"offline_notify_enabled"`
	OfflineAfterSeconds         int                  `json:"offline_after_seconds"`
	ServiceStartAt              *time.Time           `json:"service_start_at,omitempty"`
	ExpiresAt                   *time.Time           `json:"expires_at,omitempty"`
	RenewalCycle                ServerRenewalCycle   `json:"renewal_cycle"`
	AutoRenewEnabled            bool                 `json:"auto_renew_enabled"`
	ExpiryNotifyEnabled         bool                 `json:"expiry_notify_enabled"`
	LastAutoRenewedAt           *time.Time           `json:"last_auto_renewed_at,omitempty"`
	TimeCorrectionMode          TimeCorrectionMode   `json:"time_correction_mode"`
	TimeCheckStatus             string               `json:"time_check_status"`
	TimeOffsetMS                int64                `json:"time_offset_ms"`
	TimeEffectiveOffsetMS       int64                `json:"time_effective_offset_ms"`
	TimeCheckSource             string               `json:"time_check_source"`
	TimeCheckError              string               `json:"time_check_error"`
	TimeLogicalActive           bool                 `json:"time_logical_active"`
	TimeUnsupportedPaths        []string             `json:"time_unsupported_paths,omitempty"`
	TimeCheckedAt               *time.Time           `json:"time_checked_at,omitempty"`
	ConnectivityStatus          string               `json:"connectivity_status"`
	ConnectivityLatencyMS       int64                `json:"connectivity_latency_ms"`
	ConnectivityCheckedAt       *time.Time           `json:"connectivity_checked_at,omitempty"`
	ConnectivityError           string               `json:"connectivity_error"`
	TelemetryUpdatedAt          *time.Time           `json:"telemetry_updated_at,omitempty"`
	LastSeenAt                  *time.Time           `json:"last_seen_at,omitempty"`
	CreatedAt                   time.Time            `json:"created_at"`
	UpdatedAt                   time.Time            `json:"updated_at"`
}

type Inbound struct {
	ID                int64                `json:"id"`
	ServerID          int64                `json:"server_id"`
	Name              string               `json:"name"`
	Protocol          Protocol             `json:"protocol"`
	ListenIP          string               `json:"listen_ip"`
	Port              int                  `json:"port"`
	AdvertisePort     int                  `json:"advertise_port"`
	EntryIPMode       EntryIPMode          `json:"entry_ip_mode"`
	ExternalIP        string               `json:"external_ip"`
	DNSSyncEnabled    bool                 `json:"dns_sync_enabled"`
	DNSCredentialID   *int64               `json:"dns_credential_id,omitempty"`
	DNSDomain         string               `json:"dns_domain"`
	DNSProxyEnabled   bool                 `json:"dns_proxy_enabled"`
	DNSRecordTypes    string               `json:"dns_record_types"`
	DDNSEnabled       bool                 `json:"ddns_enabled"`
	DDNSInterval      int                  `json:"ddns_interval_seconds"`
	DNSSyncStatus     string               `json:"dns_sync_status"`
	DNSSyncError      string               `json:"dns_sync_error"`
	DNSLastSyncedAt   *time.Time           `json:"dns_last_synced_at,omitempty"`
	TLS               bool                 `json:"tls"`
	CertificateMode   string               `json:"certificate_mode,omitempty"`
	CertificateID     *int64               `json:"certificate_id,omitempty"`
	CertificateDomain string               `json:"certificate_domain,omitempty"`
	ConfigJSON        string               `json:"config_json"`
	Kind              string               `json:"kind,omitempty"`
	Reality           *InboundRealityInput `json:"reality,omitempty"`
	RotateRealityKey  bool                 `json:"rotate_reality_key,omitempty"`
	AnyTLSPadding     *AnyTLSPaddingInput  `json:"anytls_padding,omitempty"`
	Enabled           bool                 `json:"enabled"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

// AnyTLSPaddingInput is a create-only choice. Controller resolves it into the
// immutable padding_scheme snapshot and _oboard_padding metadata stored in
// ConfigJSON; it is never sent to Agent or sing-box.
type AnyTLSPaddingInput struct {
	PresetID string `json:"preset_id"`
	AutoTune *bool  `json:"auto_tune,omitempty"`
}

// InboundRealityInput is the public, non-secret Reality configuration. The
// Controller owns the X25519 private key and derived public key; callers may
// choose only the fallback target and the client-visible short ID.
type InboundRealityInput struct {
	HandshakeServer string `json:"handshake_server,omitempty"`
	HandshakePort   int    `json:"handshake_port,omitempty"`
	ShortID         string `json:"short_id,omitempty"`
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

type SSHServerHostKey struct {
	ServerID      int64     `json:"server_id"`
	PublicKey     string    `json:"public_key"`
	Fingerprint   string    `json:"fingerprint"`
	PlanDigest    string    `json:"-"`
	ConfigVersion int64     `json:"config_version"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type SSHPasswordDeployment struct {
	ServerID         int64     `json:"server_id"`
	UserID           int64     `json:"user_id"`
	DeviceIDHash     string    `json:"device_id_hash,omitempty"`
	CredentialEpoch  int64     `json:"credential_epoch,omitempty"`
	CredentialStatus string    `json:"credential_status,omitempty"`
	PasswordDigest   string    `json:"-"`
	ConfigVersion    int64     `json:"config_version"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type UserGroup struct {
	ID                           int64                        `json:"id"`
	Name                         string                       `json:"name"`
	Description                  string                       `json:"description"`
	Role                         Role                         `json:"role"`
	SystemKey                    string                       `json:"system_key,omitempty"`
	Enabled                      bool                         `json:"enabled"`
	SubscriptionCustomPathPolicy SubscriptionCustomPathPolicy `json:"subscription_custom_path_policy"`
	CreatedAt                    time.Time                    `json:"created_at"`
	UpdatedAt                    time.Time                    `json:"updated_at"`
}

type SubscriptionCustomPathMode string

const (
	SubscriptionCustomPathDisabled  SubscriptionCustomPathMode = "disabled"
	SubscriptionCustomPathSelective SubscriptionCustomPathMode = "selective"
	SubscriptionCustomPathEnabled   SubscriptionCustomPathMode = "enabled"
)

type SubscriptionCustomPathPolicy string

const (
	SubscriptionCustomPathInherit SubscriptionCustomPathPolicy = "inherit"
	SubscriptionCustomPathAllow   SubscriptionCustomPathPolicy = "allow"
	SubscriptionCustomPathDeny    SubscriptionCustomPathPolicy = "deny"
)

type SubscriptionCustomPath struct {
	UserID    int64     `json:"user_id"`
	Alias     string    `json:"alias"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserGroupMember struct {
	ID        int64     `json:"id"`
	GroupID   int64     `json:"group_id"`
	UserID    int64     `json:"user_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProxyPathUser struct {
	ProxyPathID int64 `json:"proxy_path_id"`
	InboundID   int64 `json:"inbound_id"`
	UserID      int64 `json:"user_id"`
	Enabled     bool  `json:"enabled"`
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

type FamilyDNSStrategy string

const (
	FamilyDNSStrategyAuto       FamilyDNSStrategy = "auto"
	FamilyDNSStrategyPreferIPv4 FamilyDNSStrategy = "prefer_ipv4"
	FamilyDNSStrategyPreferIPv6 FamilyDNSStrategy = "prefer_ipv6"
)

type RoutingRule struct {
	ID                    int64             `json:"id"`
	ServerID              int64             `json:"server_id"`
	Scope                 string            `json:"scope"`
	ProxyPathID           *int64            `json:"proxy_path_id,omitempty"`
	StageStepID           *int64            `json:"stage_step_id,omitempty"`
	SortPosition          int               `json:"sort_position"`
	MatchSource           string            `json:"match_source"`
	RuleSetID             *int64            `json:"rule_set_id,omitempty"`
	DNSResolver           string            `json:"dns_resolver,omitempty"`
	Name                  string            `json:"name"`
	Priority              int               `json:"priority"`
	MatchJSON             string            `json:"match_json"`
	Action                RouteAction       `json:"action"`
	OutboundID            *int64            `json:"outbound_id,omitempty"`
	ExternalOutboundID    *int64            `json:"external_outbound_id,omitempty"`
	TargetProxyPathID     *int64            `json:"target_proxy_path_id,omitempty"`
	IPv4TargetProxyPathID *int64            `json:"ipv4_target_proxy_path_id,omitempty"`
	IPv6TargetProxyPathID *int64            `json:"ipv6_target_proxy_path_id,omitempty"`
	FamilyDNSStrategy     FamilyDNSStrategy `json:"family_dns_strategy,omitempty"`
	TargetServerID        *int64            `json:"target_server_id,omitempty"`
	OutboundTag           string            `json:"outbound_tag"`
	InterfaceName         string            `json:"interface_name,omitempty"`
	InterfaceIPStack      IPStack           `json:"-"`
	SourcePrefix          string            `json:"source_prefix,omitempty"`
	SyncGroupID           string            `json:"sync_group_id,omitempty"`
	SyncSourceRuleID      *int64            `json:"sync_source_rule_id,omitempty"`
	SyncEnabled           bool              `json:"sync_enabled,omitempty"`
	Enabled               bool              `json:"enabled"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

const (
	RoutingRuleScopeServer    = "server"
	RoutingRuleScopePathStage = "path_stage"
	RoutingMatchSourceInline  = "inline"
	RoutingMatchSourceRuleSet = "rule_set"
)

type RoutingRuleSet struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	URL            string     `json:"url"`
	Format         string     `json:"format"`
	MihomoBehavior string     `json:"mihomo_behavior,omitempty"`
	ETag           string     `json:"etag,omitempty"`
	LastModified   string     `json:"last_modified,omitempty"`
	Content        []byte     `json:"-"`
	Revision       string     `json:"revision,omitempty"`
	Status         string     `json:"status"`
	LastError      string     `json:"last_error,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type RoutingRulePlacement struct {
	RuleID       int64  `json:"rule_id"`
	StageStepID  *int64 `json:"stage_step_id,omitempty"`
	SortPosition int    `json:"sort_position"`
}

const (
	RoutingRuleSetFormatSingBoxSource        = "singbox_source"
	RoutingRuleSetFormatSingBoxBinary        = "singbox_binary"
	RoutingRuleSetFormatMihomoDomain         = "mihomo_domain"
	RoutingRuleSetFormatMihomoIPCIDR         = "mihomo_ipcidr"
	RoutingRuleSetFormatMihomoClassical      = "mihomo_classical"
	RoutingRuleSetFormatBlackmatrixClassical = "blackmatrix_classical"
	RoutingRuleSetStatusPending              = "pending"
	RoutingRuleSetStatusReady                = "ready"
	RoutingRuleSetStatusRefreshing           = "refreshing"
	RoutingRuleSetStatusError                = "error"
)

type ExternalOutbound struct {
	ID                  int64                 `json:"id"`
	ServerID            *int64                `json:"server_id,omitempty"`
	Name                string                `json:"name"`
	Protocol            Protocol              `json:"protocol"`
	Scope               ExternalOutboundScope `json:"scope"`
	TargetAddress       string                `json:"target_address"`
	TargetPort          int                   `json:"target_port"`
	ConfigJSON          string                `json:"config_json"`
	RegionMode          string                `json:"region_mode"`
	RegionCode          string                `json:"region_code"`
	DetectedRegionCode  string                `json:"detected_region_code,omitempty"`
	EffectiveRegionCode string                `json:"effective_region_code,omitempty"`
	RegionSource        string                `json:"region_source,omitempty"`
	RegionStatus        string                `json:"region_status,omitempty"`
	RegionError         string                `json:"region_error,omitempty"`
	RegionProbedAt      *time.Time            `json:"region_probed_at,omitempty"`
	ExposeToUsers       bool                  `json:"expose_to_users"`
	Enabled             bool                  `json:"enabled"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

type ProxyPath struct {
	ID                      int64               `json:"id"`
	Kind                    ProxyPathKind       `json:"kind"`
	BranchSourceStepID      *int64              `json:"branch_source_step_id,omitempty"`
	Name                    string              `json:"name"`
	NameMode                ProxyPathNameMode   `json:"name_mode"`
	NameTemplate            []ProxyPathNamePart `json:"name_template"`
	NameTemplateJSON        string              `json:"-"`
	InboundID               int64               `json:"inbound_id"`
	ExitRegionMode          string              `json:"exit_region_mode"`
	ExitRegionCode          string              `json:"exit_region_code"`
	DetectedExitRegionCode  string              `json:"detected_exit_region_code,omitempty"`
	EffectiveExitRegionCode string              `json:"effective_exit_region_code,omitempty"`
	ExitRegionSource        string              `json:"exit_region_source,omitempty"`
	ExitRegionStatus        string              `json:"exit_region_status,omitempty"`
	ExitRegionError         string              `json:"exit_region_error,omitempty"`
	ExitRegionProbedAt      *time.Time          `json:"exit_region_probed_at,omitempty"`
	Secret                  string              `json:"-"`
	Enabled                 bool                `json:"enabled"`
	CreatedAt               time.Time           `json:"created_at"`
	UpdatedAt               time.Time           `json:"updated_at"`
}

type ProxyPathEgressResult struct {
	PathID              int64      `json:"path_id"`
	ExternalOutboundID  int64      `json:"external_outbound_id"`
	OwnerServerID       int64      `json:"owner_server_id"`
	TopologyFingerprint string     `json:"-"`
	ConfigVersion       int64      `json:"config_version"`
	TaskID              *int64     `json:"task_id,omitempty"`
	Status              string     `json:"status"`
	LastExitIP          string     `json:"-"`
	LastRegionCode      string     `json:"last_region_code,omitempty"`
	GeoDatabaseRevision string     `json:"geo_database_revision,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
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
	ProxyPathStepWARP          ProxyPathStepNodeType = "warp"
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

type ProxyPathRuntimeNode struct {
	ResourceKey    string          `json:"resource_key"`
	StepID         int64           `json:"step_id"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	ServerID       int64           `json:"server_id"`
	Protocol       Protocol        `json:"protocol"`
	Profile        string          `json:"profile,omitempty"`
	ListenIP       string          `json:"listen_ip"`
	Port           int             `json:"port"`
	Network        ForwardProtocol `json:"network"`
	ListenScope    string          `json:"listen_scope"`
	Shared         bool            `json:"shared"`
	ReferenceCount int             `json:"reference_count"`
}

type ProxyPathPlan struct {
	PathID       int64                  `json:"path_id"`
	Name         string                 `json:"name"`
	InboundID    int64                  `json:"inbound_id"`
	Enabled      bool                   `json:"enabled"`
	Steps        []ProxyPathPlanStep    `json:"steps"`
	Warnings     []string               `json:"warnings,omitempty"`
	RuntimeNodes []ProxyPathRuntimeNode `json:"runtime_nodes"`
	PortForwards []PortForward          `json:"port_forwards,omitempty"`
	Tunnels      []Tunnel               `json:"tunnels,omitempty"`
}

// ProxyPathPortAllocation records the port one generated proxy-path listener
// owns. Allocation is persisted rather than re-derived on every projection so an
// unrelated topology change cannot move a listener that is already deployed, and
// so the port an operator inspects is the port that gets applied.
type ProxyPathPortAllocation struct {
	ID             int64     `json:"id"`
	Kind           string    `json:"kind"`
	ScopeKey       string    `json:"scope_key"`
	ServerID       int64     `json:"server_id"`
	Pool           string    `json:"pool"`
	ListenIP       string    `json:"listen_ip"`
	Network        string    `json:"network"`
	Generation     int       `json:"generation"`
	Ordinal        int       `json:"ordinal"`
	Port           int       `json:"port"`
	State          string    `json:"state"`
	PolicyRevision int64     `json:"policy_revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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

// Port pools. Public listeners are reachable from other devices or servers and
// must stay inside the server's auto public port range; internal listeners bind
// loopback only and live in the separate internal loopback pool.
const (
	PortPoolPublic   = "public"
	PortPoolInternal = "internal"
)

// Allocation lifecycle states. A single active generation is the steady state;
// a migration adds a preparing generation before the previous one retires.
const (
	PortAllocationStateActive    = "active"
	PortAllocationStatePreparing = "preparing"
	PortAllocationStateRetiring  = "retiring"
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

// SnellProfile is a reusable Snell parameter set. Multiple inbounds on
// different servers can reference one profile so they share the same
// version/psk/obfs/mode parameters; modifications take effect on the next
// deployment. Builtin profiles are seeded and protected from deletion.
type SnellProfile struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Version  int    `json:"version"`
	PSK      string `json:"psk"`
	ObfsMode string `json:"obfs_mode"`
	ObfsHost string `json:"obfs_host"`
	Mode     string `json:"mode"`
	Reuse    bool   `json:"reuse"`
	// TCPFastOpen enables the TCP Fast Open socket option on the Snell
	// listener and on generated Snell client parameters. Snell always runs
	// over TCP, including its UDP relay.
	TCPFastOpen bool      `json:"tcp_fast_open"`
	Remark      string    `json:"remark"`
	Builtin     bool      `json:"builtin"`
	Enabled     bool      `json:"enabled"`
	UsageCount  int64     `json:"usage_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NodePreset is a reusable inbound configuration template for protocols
// that need more than a port. Multiple inbounds can reference one preset
// via config_json.node_preset_id; Snell continues to use snell_profiles.
type NodePreset struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Protocol    string    `json:"protocol"`
	Kind        string    `json:"kind"`
	ConfigJSON  string    `json:"config_json"`
	DefaultPort int       `json:"default_port"`
	Remark      string    `json:"remark"`
	Builtin     bool      `json:"builtin"`
	Enabled     bool      `json:"enabled"`
	UsageCount  int64     `json:"usage_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type DNSCandidate struct {
	Tag       string       `json:"tag"`
	Transport DNSTransport `json:"transport"`
	Server    string       `json:"server"`
	Port      int          `json:"port"`
	Path      string       `json:"path,omitempty"`
	TLSName   string       `json:"tls_name,omitempty"`
}

const DNSBenchmarkNoUsableCandidatesError = "both encrypted and bootstrap dns groups require at least one usable candidate"

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
	OutboundTag string  `json:"outbound_tag"`
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
	TargetServerID       int64                 `json:"target_server_id,omitempty"`
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
	Port      int                             `json:"port"`
	Enabled   bool                            `json:"enabled"`
	Users     []SSHInboundUser                `json:"users"`
	Policies  map[string]TrafficRuntimePolicy `json:"policies,omitempty"`
}

type SSHInboundUser struct {
	UserID           int64  `json:"user_id"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	DeviceIDHash     string `json:"device_id_hash,omitempty"`
	CredentialEpoch  int64  `json:"credential_epoch,omitempty"`
	CredentialStatus string `json:"credential_status,omitempty"`
	PathID           int64  `json:"path_id"`
	RouteKind        string `json:"route_kind"`
	OutboundTag      string `json:"outbound_tag,omitempty"`
	RouteInboundTag  string `json:"route_inbound_tag,omitempty"`
	RouteAuthUser    string `json:"route_auth_user,omitempty"`
	Enabled          bool   `json:"enabled"`
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
	AgentTaskTypeApplyTrafficPolicy    = "apply_traffic_policy"
	AgentTaskTypeUpdateAgent           = "update_agent"
	AgentTaskTypeUninstallAgent        = "uninstall_agent"
	AgentTaskTypeUpdateAgentConfig     = "update_agent_config"
	AgentTaskTypeDiagnoseNetwork       = "diagnose_network"
	AgentTaskTypeListNetworkInterfaces = "list_network_interfaces"
	AgentTaskTypeProbeInbounds         = "probe_inbounds"
	AgentTaskTypeProbeInboundsExternal = "probe_inbounds_external"
	AgentTaskTypeProbePortForwards     = "probe_port_forwards"
	AgentTaskTypeProbeExternalEgress   = "probe_external_egress"
	AgentTaskTypeProbeLatencyTargets   = "probe_latency_targets"
	AgentTaskTypeDetectMTU             = "detect_mtu"
	AgentTaskTypeBenchmarkDNS          = "benchmark_dns"
	AgentTaskTypeCollectLogs           = "collect_logs"
	AgentTaskTypeManageLogs            = "manage_logs"
	AgentTaskTypeCheckTime             = "check_time"
	AgentTaskTypeIssueCertificateHTTP  = "issue_certificate_http01"
	AgentTaskTypeRemoteExec            = "remote_exec"
	AgentTaskTypeRemoteOperation       = "remote_operation"
)

const AgentCapabilityTrafficPolicy = "traffic_policy_v1"

type NetworkInterfaceInfo struct {
	Name      string   `json:"name"`
	Up        bool     `json:"up"`
	Running   bool     `json:"running"`
	Loopback  bool     `json:"loopback"`
	Addresses []string `json:"addresses"`
}

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
	Type                     string          `json:"type"`
	ServerID                 int64           `json:"server_id,omitempty"`
	Timestamp                time.Time       `json:"ts,omitempty"`
	Task                     *AgentTask      `json:"task,omitempty"`
	Signature                string          `json:"signature,omitempty"`
	HealthReport             *HealthReport   `json:"health_report,omitempty"`
	MetricReport             *MetricReport   `json:"metric_report,omitempty"`
	ReportID                 string          `json:"report_id,omitempty"`
	DesiredConfigRevision    uint64          `json:"desired_config_revision,omitempty"`
	ConfigurationSyncState   string          `json:"configuration_sync_state,omitempty"`
	ConfigurationSyncVersion int64           `json:"configuration_sync_version,omitempty"`
	Raw                      json.RawMessage `json:"-"`
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

type ApplyTrafficPolicyTaskPayload struct {
	PolicyRevision int64                           `json:"policy_revision"`
	Reason         string                          `json:"reason,omitempty"`
	Policies       map[string]TrafficRuntimePolicy `json:"policies"`
}

// DeploymentTaskPayload keeps one user deployment as one Agent task while
// preserving the individual execution plans needed by the Agent.
type DeploymentTaskPayload struct {
	Version              int64                      `json:"version"`
	Config               ApplyCoreConfigTaskPayload `json:"config"`
	ConfigChanged        bool                       `json:"config_changed"`
	TriggerReason        string                     `json:"trigger_reason,omitempty"`
	WARPRequests         []WARPRequestPlan          `json:"warp_requests,omitempty"`
	TimeCheck            *TimeCheckPlan             `json:"time_check,omitempty"`
	PortForwards         PortForwardPlan            `json:"port_forwards"`
	InboundProbe         *InboundProbePlan          `json:"inbound_probe,omitempty"`
	ExternalInboundProbe *InboundProbePlan          `json:"external_inbound_probe,omitempty"`
	PortForwardProbe     *PortForwardPlan           `json:"port_forward_probe,omitempty"`
	ExternalEgressProbe  *ExternalEgressProbePlan   `json:"external_egress_probe,omitempty"`
	Tunnels              TunnelPlan                 `json:"tunnels"`
	SSHInbounds          SSHInboundPlan             `json:"ssh_inbounds"`
	DNSBenchmark         *DNSBenchmarkPlan          `json:"dns_benchmark,omitempty"`
	MTUDetection         *MTUDetectionPlan          `json:"mtu_detection,omitempty"`
}

type ExternalEgressProbePlan struct {
	Version               int64                       `json:"version"`
	ExpectedConfigVersion int64                       `json:"expected_config_version,omitempty"`
	TimeoutMS             int                         `json:"timeout_ms"`
	Targets               []ExternalEgressProbeTarget `json:"targets"`
}

type LatencyProbeTarget struct {
	ProbeID  string `json:"probe_id"`
	Kind     string `json:"kind"`
	Province string `json:"province,omitempty"`
	Carrier  string `json:"carrier,omitempty"`
	Host     string `json:"host"`
	IP       string `json:"ip,omitempty"`
	Port     int    `json:"port"`
}

type LatencyProbeRegion struct {
	Province string `json:"province"`
	Carrier  string `json:"carrier"`
}

type LatencyProbeTargetsPlan struct {
	Version         int64                `json:"version"`
	ResourceVersion string               `json:"resource_version"`
	Mode            LatencyProbeMode     `json:"mode"`
	Enabled         bool                 `json:"enabled"`
	IntervalSeconds int                  `json:"interval_seconds"`
	SampleCount     int                  `json:"sample_count"`
	IntervalMS      int                  `json:"interval_ms"`
	TimeoutMS       int                  `json:"timeout_ms"`
	Targets         []LatencyProbeTarget `json:"targets"`
}

type LatencyProbeResult struct {
	ProbeID      string    `json:"probe_id"`
	Kind         string    `json:"kind"`
	Mode         string    `json:"mode"`
	Province     string    `json:"province"`
	Carrier      string    `json:"carrier"`
	Host         string    `json:"host"`
	IP           string    `json:"ip"`
	Port         int       `json:"port"`
	Available    bool      `json:"available"`
	LatencyMS    int64     `json:"latency_ms"`
	MinLatencyMS int64     `json:"min_latency_ms"`
	P95LatencyMS int64     `json:"p95_latency_ms"`
	JitterMS     int64     `json:"jitter_ms"`
	SampleCount  int       `json:"sample_count"`
	SuccessCount int       `json:"success_count"`
	Error        string    `json:"error,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
}

type ServerRegionalLatencyPoint struct {
	Kind         string    `json:"kind"`
	Province     string    `json:"province"`
	Carrier      string    `json:"carrier"`
	Available    bool      `json:"available"`
	LatencyMS    float64   `json:"latency_ms"`
	MinLatencyMS int64     `json:"min_latency_ms"`
	MaxLatencyMS int64     `json:"max_latency_ms"`
	Count        int64     `json:"count"`
	CheckedAt    time.Time `json:"checked_at"`
}

type LatencyProbeResultReport struct {
	ReportID        string               `json:"report_id"`
	ResourceVersion string               `json:"resource_version"`
	CheckedAt       time.Time            `json:"checked_at"`
	Items           []LatencyProbeResult `json:"items"`
}

type ExternalEgressProbeTarget struct {
	ProbeID             string `json:"probe_id"`
	PathID              int64  `json:"path_id"`
	ExternalOutboundID  int64  `json:"external_outbound_id"`
	OwnerServerID       int64  `json:"owner_server_id"`
	OutboundTag         string `json:"outbound_tag"`
	TopologyFingerprint string `json:"topology_fingerprint"`
}

type ExternalEgressProbeItem struct {
	ProbeID string `json:"probe_id"`
	Status  string `json:"status"`
	ExitIP  string `json:"exit_ip,omitempty"`
	Error   string `json:"error,omitempty"`
}

type ExternalEgressProbeResult struct {
	Items []ExternalEgressProbeItem `json:"items"`
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

type UninstallAgentTaskPayload struct {
	Purge   bool  `json:"purge"`
	ActorID int64 `json:"actor_id,omitempty"`
}

type AgentUpdateRequest struct {
	Source     string `json:"source"`
	GitHubRepo string `json:"github_repo"`
}

type AgentConfigPatch struct {
	ControllerURL         string             `json:"controller_url,omitempty"`
	StateDir              string             `json:"state_dir,omitempty"`
	CoreBinary            string             `json:"core_binary,omitempty"`
	CoreService           string             `json:"core_service,omitempty"`
	CommandTimeoutSeconds int                `json:"command_timeout_seconds,omitempty"`
	ReloadCommand         string             `json:"reload_command,omitempty"`
	RestartCommand        string             `json:"restart_command,omitempty"`
	TimeSyncCommand       string             `json:"time_sync_command,omitempty"`
	TimeCorrectionMode    TimeCorrectionMode `json:"time_correction_mode,omitempty"`
	LogMaxMB              int                `json:"log_max_mb,omitempty"`
	LogBackups            int                `json:"log_backups,omitempty"`
	CoreLogMaxMB          int                `json:"core_log_max_mb,omitempty"`
	CoreLogBackups        int                `json:"core_log_backups,omitempty"`
	UpdateSource          string             `json:"update_source,omitempty"`
	AllowPanelUpdate      bool               `json:"allow_panel_update,omitempty"`
	UpdateRepo            string             `json:"update_repo,omitempty"`
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
	InboundID   int64    `json:"inbound_id"`
	Name        string   `json:"name"`
	Protocol    Protocol `json:"protocol"`
	Host        string   `json:"host"`
	ListenIP    string   `json:"listen_ip"`
	Port        int      `json:"port"`
	Transport   string   `json:"transport"`
	SampleCount int      `json:"sample_count,omitempty"`
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

type TimeCheckPlan struct {
	Version          int64              `json:"version"`
	CorrectionMode   TimeCorrectionMode `json:"correction_mode"`
	ThresholdSeconds int                `json:"threshold_seconds"`
	NTPServers       []string           `json:"ntp_servers"`
	Force            bool               `json:"force,omitempty"`
}

type TimeCheckResult struct {
	Status               string             `json:"status"`
	CorrectionMode       TimeCorrectionMode `json:"correction_mode"`
	RawOffsetMS          int64              `json:"raw_offset_ms"`
	EffectiveOffsetMS    int64              `json:"effective_offset_ms"`
	Source               string             `json:"source"`
	CheckedAt            time.Time          `json:"checked_at"`
	SystemSyncAttempted  bool               `json:"system_sync_attempted"`
	SystemSyncSucceeded  bool               `json:"system_sync_succeeded"`
	SystemSyncError      string             `json:"system_sync_error,omitempty"`
	LogicalTimeActive    bool               `json:"logical_time_active"`
	UnsupportedTimePaths []string           `json:"unsupported_time_paths,omitempty"`
	Error                string             `json:"error,omitempty"`
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

type ServerOfflineNotice struct {
	ServerID   int64      `json:"server_id"`
	Status     string     `json:"status"`
	SinceAt    time.Time  `json:"since_at"`
	NotifyAt   time.Time  `json:"notify_at"`
	GroupKey   string     `json:"group_key"`
	ServerName string     `json:"server_name"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type NodeIncidentStatus string

const (
	NodeIncidentActive     NodeIncidentStatus = "active"
	NodeIncidentRecovering NodeIncidentStatus = "recovering"
	NodeIncidentResolved   NodeIncidentStatus = "resolved"
)

// NodeIncident is the durable, debounced lifecycle of one server outage.
// SnapshotJSON contains only non-secret topology and publication metadata.
type NodeIncident struct {
	ID                       int64              `json:"id"`
	ServerID                 int64              `json:"server_id"`
	ServerName               string             `json:"server_name"`
	Kind                     string             `json:"kind"`
	Status                   NodeIncidentStatus `json:"status"`
	Version                  int64              `json:"version"`
	FirstOfflineAt           time.Time          `json:"first_offline_at"`
	DetectedAt               time.Time          `json:"detected_at"`
	RecoveryCandidateAt      *time.Time         `json:"recovery_candidate_at,omitempty"`
	RecoveryDeadlineAt       *time.Time         `json:"recovery_deadline_at,omitempty"`
	RecoveredAt              *time.Time         `json:"recovered_at,omitempty"`
	ResolvedAt               *time.Time         `json:"resolved_at,omitempty"`
	OutageDurationSeconds    int64              `json:"outage_duration_seconds"`
	OfflineThresholdSeconds  int                `json:"offline_threshold_seconds"`
	RecoveryThresholdSeconds int                `json:"recovery_threshold_seconds"`
	FlapCount                int                `json:"flap_count"`
	SnapshotJSON             string             `json:"snapshot_json"`
	CreatedAt                time.Time          `json:"created_at"`
	UpdatedAt                time.Time          `json:"updated_at"`
}

type NodeIncidentTelegramMessage struct {
	ID                int64      `json:"id"`
	IncidentID        int64      `json:"incident_id"`
	ChannelID         *int64     `json:"channel_id,omitempty"`
	ChatID            int64      `json:"chat_id"`
	MessageID         int64      `json:"message_id,omitempty"`
	FallbackMessageID int64      `json:"fallback_message_id,omitempty"`
	LastEventVersion  int64      `json:"last_event_version"`
	LastEditedAt      *time.Time `json:"last_edited_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type NodePublicationIsolation struct {
	ID             int64      `json:"id"`
	IncidentID     int64      `json:"incident_id"`
	InboundID      *int64     `json:"inbound_id,omitempty"`
	InboundName    string     `json:"inbound_name"`
	ServerID       int64      `json:"server_id"`
	RecoveryPolicy string     `json:"recovery_policy"`
	Status         string     `json:"status"`
	ActorUserID    int64      `json:"actor_user_id"`
	RestoredBy     *int64     `json:"restored_by,omitempty"`
	RestoredAt     *time.Time `json:"restored_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type NodeIncidentAction struct {
	ID             int64      `json:"id"`
	IncidentID     int64      `json:"incident_id"`
	ActorUserID    int64      `json:"actor_user_id"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	InboundIDsJSON string     `json:"inbound_ids_json"`
	ChangesetID    string     `json:"changeset_id"`
	ConfigVersion  int64      `json:"config_version"`
	TaskCount      int        `json:"task_count"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type TelegramBinding struct {
	ID             int64     `json:"id"`
	ChannelID      int64     `json:"channel_id"`
	UserID         int64     `json:"user_id"`
	ChatID         int64     `json:"chat_id"`
	TelegramUserID int64     `json:"telegram_user_id"`
	ChatType       string    `json:"chat_type"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type TelegramBindingCode struct {
	Code      string    `json:"code,omitempty"`
	ChannelID int64     `json:"channel_id"`
	UserID    int64     `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type NotificationBroadcast struct {
	ID             int64      `json:"id"`
	ActorUserID    int64      `json:"actor_user_id"`
	ActorName      string     `json:"actor_name"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	FilterJSON     string     `json:"filter_json"`
	IdempotencyKey string     `json:"idempotency_key"`
	Status         string     `json:"status"`
	RecipientCount int        `json:"recipient_count"`
	SuccessCount   int        `json:"success_count"`
	FailureCount   int        `json:"failure_count"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type NotificationBroadcastTarget struct {
	ID            int64                 `json:"id"`
	BroadcastID   int64                 `json:"broadcast_id"`
	UserID        int64                 `json:"user_id"`
	BindingID     *int64                `json:"binding_id,omitempty"`
	ChannelID     *int64                `json:"channel_id,omitempty"`
	ChatID        *int64                `json:"chat_id,omitempty"`
	Status        string                `json:"status"`
	Attempts      int                   `json:"attempts"`
	Error         string                `json:"error,omitempty"`
	NextAttemptAt time.Time             `json:"next_attempt_at"`
	SentAt        *time.Time            `json:"sent_at,omitempty"`
	Broadcast     NotificationBroadcast `json:"broadcast"`
	Channel       NotificationChannel   `json:"channel"`
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
	ReportID         string    `json:"report_id"`
	ServerID         int64     `json:"server_id"`
	UserID           int64     `json:"user_id"`
	InboundID        *int64    `json:"inbound_id,omitempty"`
	PathID           *int64    `json:"path_id,omitempty"`
	PeriodKey        string    `json:"period_key"`
	Upload           int64     `json:"upload_bytes"`
	Download         int64     `json:"download_bytes"`
	StartedAt        time.Time `json:"started_at"`
	EndedAt          time.Time `json:"ended_at"`
	ProtocolVersion  int       `json:"protocol_version,omitempty"`
	CounterSource    string    `json:"counter_source,omitempty"`
	StreamID         string    `json:"stream_id,omitempty"`
	CounterEpoch     string    `json:"counter_epoch,omitempty"`
	FromUploadBytes  int64     `json:"from_upload_bytes,omitempty"`
	ToUploadBytes    int64     `json:"to_upload_bytes,omitempty"`
	FromDownloadBytes int64    `json:"from_download_bytes,omitempty"`
	ToDownloadBytes  int64     `json:"to_download_bytes,omitempty"`
	AcceptStatus     string    `json:"accept_status,omitempty"`
}

type TrafficCounterStream struct {
	ID                    int64     `json:"id"`
	ServerID              int64     `json:"server_id"`
	UserID                int64     `json:"user_id"`
	CounterSource         string    `json:"counter_source"`
	StreamID              string    `json:"stream_id"`
	CounterEpoch          string    `json:"counter_epoch"`
	PeriodKey             string    `json:"period_key"`
	InboundID             int64     `json:"inbound_id,omitempty"`
	PathID                int64     `json:"path_id,omitempty"`
	AcceptedUploadBytes   int64     `json:"accepted_upload_bytes"`
	AcceptedDownloadBytes int64     `json:"accepted_download_bytes"`
	Status                string    `json:"status"`
	LastError             string    `json:"last_error,omitempty"`
	AgentInstanceID       string    `json:"agent_instance_id,omitempty"`
	FirstSeenAt           time.Time `json:"first_seen_at"`
	LastSeenAt            time.Time `json:"last_seen_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type TrafficLease struct {
	ID             int64      `json:"id"`
	ServerID       int64      `json:"server_id"`
	UserID         int64      `json:"user_id"`
	PeriodKey      string     `json:"period_key"`
	LeaseBytes     int64      `json:"lease_bytes"`
	ConsumedBytes  int64      `json:"consumed_bytes"`
	LeaseRevision  int64      `json:"lease_revision"`
	State          string     `json:"state"`
	IssuedAt       time.Time  `json:"issued_at"`
	LastSyncedAt   time.Time  `json:"last_synced_at"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type TrafficReconciliationEvent struct {
	ID            int64      `json:"id"`
	ServerID      int64      `json:"server_id"`
	UserID        int64      `json:"user_id"`
	Source        string     `json:"source"`
	StreamID      string     `json:"stream_id"`
	CounterEpoch  string     `json:"counter_epoch"`
	PeriodKey     string     `json:"period_key"`
	Kind          string     `json:"kind"`
	Detail        string     `json:"detail"`
	CreatedAt     time.Time  `json:"created_at"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
}

type TrafficStreamObservation struct {
	Source            string `json:"source"`
	StreamID          string `json:"stream_id"`
	CounterEpoch      string `json:"counter_epoch"`
	PeriodKey         string `json:"period_key"`
	UserID            int64  `json:"user_id"`
	InboundID         int64  `json:"inbound_id,omitempty"`
	PathID            int64  `json:"path_id,omitempty"`
	CurrentUpload     int64  `json:"current_upload_bytes"`
	CurrentDownload   int64  `json:"current_download_bytes"`
	Status            string `json:"status,omitempty"`
}

type TrafficStreamCheckpoint struct {
	Source           string `json:"source"`
	StreamID         string `json:"stream_id"`
	CounterEpoch     string `json:"counter_epoch"`
	PeriodKey        string `json:"period_key"`
	AcceptedUpload   int64  `json:"accepted_upload_bytes"`
	AcceptedDownload int64  `json:"accepted_download_bytes"`
	Status           string `json:"status"`
	LastError        string `json:"last_error,omitempty"`
}

type TrafficAcceptedReport struct {
	ReportID         string `json:"report_id"`
	Status           string `json:"status"`
	StreamID         string `json:"stream_id,omitempty"`
	CounterEpoch     string `json:"counter_epoch,omitempty"`
	PeriodKey        string `json:"period_key,omitempty"`
	AcceptedUpload   int64  `json:"accepted_upload_bytes"`
	AcceptedDownload int64  `json:"accepted_download_bytes"`
}

type TrafficLedgerView struct {
	UserID int64                  `json:"user_id"`
	Period TrafficLedgerPeriod    `json:"period"`
	Servers []TrafficLedgerServer `json:"servers"`
	Issues []TrafficReconciliationEvent `json:"issues,omitempty"`
}

type TrafficLedgerPeriod struct {
	Key           string `json:"key"`
	UploadBytes   int64  `json:"upload_bytes"`
	DownloadBytes int64  `json:"download_bytes"`
	UsedBytes     int64  `json:"used_bytes"`
	LimitBytes    int64  `json:"limit_bytes"`
	State         string `json:"state"`
}

type TrafficLedgerServer struct {
	ServerID   int64                   `json:"server_id"`
	ServerName string                  `json:"server_name,omitempty"`
	Lease      TrafficLedgerLease      `json:"lease"`
	Sync       TrafficLedgerSync       `json:"sync"`
	Streams    []TrafficCounterStream  `json:"streams"`
}

type TrafficLedgerLease struct {
	LeaseID        int64  `json:"lease_id,omitempty"`
	Revision       int64  `json:"revision"`
	GrantedBytes   int64  `json:"granted_bytes"`
	ConsumedBytes  int64  `json:"consumed_bytes"`
	RemainingBytes int64  `json:"remaining_bytes"`
	State          string `json:"state"`
}

type TrafficLedgerSync struct {
	Status     string    `json:"status"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

type ConnectionAuditReport struct {
	ReportID             string    `json:"report_id"`
	ServerID             int64     `json:"server_id"`
	UserID               int64     `json:"user_id"`
	InboundID            *int64    `json:"inbound_id,omitempty"`
	PathID               *int64    `json:"path_id,omitempty"`
	DeviceIDHash         string    `json:"device_id_hash,omitempty"`
	CredentialEpoch      int64     `json:"credential_epoch,omitempty"`
	ClientInstanceIDHash string    `json:"client_instance_id_hash,omitempty"`
	SourceIP             string    `json:"source_ip"`
	RouteID              string    `json:"route_id,omitempty"`
	SourceGeoCode        string    `json:"source_geo_code,omitempty"`
	SourceCountryCode    string    `json:"source_country_code,omitempty"`
	SourceCountry        string    `json:"source_country,omitempty"`
	SourceProvince       string    `json:"source_province,omitempty"`
	SourceCity           string    `json:"source_city,omitempty"`
	SourceISP            string    `json:"source_isp,omitempty"`
	GeoDatabaseRevision  string    `json:"geo_database_revision,omitempty"`
	Network              string    `json:"network"`
	Destination          string    `json:"destination,omitempty"`
	DestinationPort      int       `json:"destination_port,omitempty"`
	OutboundTag          string    `json:"outbound_tag,omitempty"`
	OutboundType         string    `json:"outbound_type,omitempty"`
	ConnectionCount      int64     `json:"connection_count"`
	ClosedCount          int64     `json:"closed_count"`
	DurationTotalMS      int64     `json:"duration_total_ms"`
	DurationMaxMS        int64     `json:"duration_max_ms"`
	UploadBytes          int64     `json:"upload_bytes"`
	DownloadBytes        int64     `json:"download_bytes"`
	PayloadFirstAt       time.Time `json:"payload_first_at,omitempty"`
	PayloadLastAt        time.Time `json:"payload_last_at,omitempty"`
	DurationLE1SCount    int64     `json:"duration_le_1s_count"`
	DurationLE5SCount    int64     `json:"duration_le_5s_count"`
	DurationLE20SCount   int64     `json:"duration_le_20s_count"`
	DurationGT20SCount   int64     `json:"duration_gt_20s_count"`
	ProbeState           string    `json:"probe_state,omitempty"`
	InternalProbe        bool      `json:"internal_probe"`
	PresenceSequence     uint64    `json:"presence_sequence,omitempty"`
	ActivePeak           int64     `json:"active_peak"`
	ActiveAtEnd          int64     `json:"active_at_end"`
	CollectionGeneration uint64    `json:"collection_generation"`
	BucketCapacity       int       `json:"bucket_capacity"`
	DroppedBucketCount   int64     `json:"dropped_bucket_count"`
	CollectionStartedAt  time.Time `json:"collection_started_at"`
	CollectionEndedAt    time.Time `json:"collection_ended_at"`
	StartedAt            time.Time `json:"started_at"`
	EndedAt              time.Time `json:"ended_at"`
	CreatedAt            time.Time `json:"created_at"`
}

type ConnectionPresenceEvent struct {
	Sequence          uint64    `json:"seq"`
	AgentID           string    `json:"-"`
	ServerID          int64     `json:"server_id"`
	UserID            int64     `json:"user_id"`
	InboundID         int64     `json:"inbound_id,omitempty"`
	PathID            int64     `json:"path_id,omitempty"`
	DeviceIDHash      string    `json:"device_id_hash,omitempty"`
	CredentialEpoch   int64     `json:"credential_epoch,omitempty"`
	SourceIP          string    `json:"source_ip"`
	RouteID           string    `json:"route_id,omitempty"`
	Network           string    `json:"network"`
	Event             string    `json:"event"`
	State             string    `json:"state"`
	ActiveConnections int64     `json:"active_connections"`
	Meaningful        bool      `json:"meaningful"`
	PayloadLastAt     time.Time `json:"payload_last_at,omitempty"`
	At                time.Time `json:"at"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
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
	Kind            string    `json:"kind"`
	Level           string    `json:"level"`
	Score           int       `json:"score"`
	SourceIPCount   int       `json:"source_ip_count"`
	RegionCount     int       `json:"region_count"`
	Regions         []string  `json:"regions"`
	DeviceIDHash    string    `json:"device_id_hash,omitempty"`
	RouteCount      int       `json:"route_count"`
	OverlapSecs     int       `json:"overlap_seconds"`
	CloneConfidence float64   `json:"clone_confidence"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
}

type ConnectionProbeEpisode struct {
	ID              string    `json:"id"`
	UserID          int64     `json:"user_id"`
	DeviceIDHash    string    `json:"device_id_hash,omitempty"`
	State           string    `json:"state"`
	Score           int       `json:"score"`
	NodeCount       int       `json:"node_count"`
	ConnectionCount int64     `json:"connection_count"`
	UploadBytes     int64     `json:"upload_bytes"`
	DownloadBytes   int64     `json:"download_bytes"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ConnectionAuditUserSummary struct {
	UserID                int64      `json:"user_id"`
	Username              string     `json:"username"`
	Nickname              string     `json:"nickname"`
	RiskLevel             string     `json:"risk_level"`
	RiskScore             int        `json:"risk_score"`
	RiskSignals           []string   `json:"risk_signals"`
	Confidence            float64    `json:"confidence"`
	EvidenceCategories    []string   `json:"evidence_categories"`
	CounterEvidence       []string   `json:"counter_evidence"`
	RecommendedAction     string     `json:"recommended_action"`
	IdentityMode          string     `json:"identity_mode"`
	DeviceLimit           int        `json:"device_limit"`
	RegisteredDeviceCount int        `json:"registered_device_count"`
	OnlineDeviceCount     int        `json:"online_device_count"`
	OnlineDeviceLower     int        `json:"online_device_lower"`
	OnlineDeviceEstimate  float64    `json:"online_device_estimate"`
	OnlineDeviceUpper     int        `json:"online_device_upper"`
	CoverageQuality       float64    `json:"coverage_quality"`
	CoverageComplete      bool       `json:"coverage_complete"`
	CloneConfidence       float64    `json:"clone_confidence"`
	RiskDeviceIDHash      string     `json:"risk_device_id_hash,omitempty"`
	ConcurrentRouteCount  int        `json:"concurrent_route_count"`
	NodeFanout            int        `json:"node_fanout"`
	RobustZ               float64    `json:"robust_z"`
	ResourcePressure      float64    `json:"resource_pressure"`
	AutoActionEligible    bool       `json:"auto_action_eligible"`
	ProbeEpisodeCount     int        `json:"probe_episode_count"`
	SourceIPCount         int        `json:"source_ip_count"`
	SourceSubnetCount     int        `json:"source_subnet_count"`
	SharedSourceIPCount   int        `json:"shared_source_ip_count"`
	SourceRegionCount     int        `json:"source_region_count"`
	RiskSourceIPCount     int        `json:"risk_source_ip_count"`
	RiskRegionCount       int        `json:"risk_region_count"`
	RiskRegions           []string   `json:"risk_regions"`
	RiskWindowStartedAt   *time.Time `json:"risk_window_started_at,omitempty"`
	RiskWindowEndedAt     *time.Time `json:"risk_window_ended_at,omitempty"`
	ServerCount           int        `json:"server_count"`
	ConnectionCount       int64      `json:"connection_count"`
	ActivePeak            int64      `json:"active_peak"`
	ActiveConnectionCount int64      `json:"active_connection_count"`
	ReportCount           int64      `json:"report_count"`
	UploadBytes           int64      `json:"upload_bytes"`
	DownloadBytes         int64      `json:"download_bytes"`
	LastSeenAt            time.Time  `json:"last_seen_at"`
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
	Policy             AuditPolicy                  `json:"policy"`
	EnabledServerCount int                          `json:"enabled_server_count"`
	ReportingUserCount int                          `json:"reporting_user_count"`
	ElevatedRiskCount  int                          `json:"elevated_risk_count"`
	TotalConnections   int64                        `json:"total_connections"`
	UniqueSourceIPs    int                          `json:"unique_source_ips"`
	Users              []ConnectionAuditUserSummary `json:"users"`
}

type ConnectionAuditUserDetail struct {
	Summary       ConnectionAuditUserSummary `json:"summary"`
	Sources       []ConnectionAuditDimension `json:"sources"`
	Destinations  []ConnectionAuditDimension `json:"destinations"`
	Outbounds     []ConnectionAuditDimension `json:"outbounds"`
	Servers       []ConnectionAuditDimension `json:"servers"`
	Recent        []ConnectionAuditReport    `json:"recent"`
	RiskEvents    []ConnectionAuditRiskEvent `json:"risk_events"`
	ProbeEpisodes []ConnectionProbeEpisode   `json:"probe_episodes"`
	Presence      []ConnectionPresenceEvent  `json:"presence"`
}

type AuditThreshold struct {
	Soft int `json:"soft"`
	Hard int `json:"hard"`
}

type AuditPolicy struct {
	Mode                     string         `json:"mode"`
	RawRequestsPer60Seconds  AuditThreshold `json:"raw_requests_per_60_seconds"`
	LogicalPullsPer10Minutes AuditThreshold `json:"logical_pulls_per_10_minutes"`
	LogicalPullsPer24Hours   AuditThreshold `json:"logical_pulls_per_24_hours"`
	RoutesPer15Minutes       AuditThreshold `json:"routes_per_15_minutes"`
	ClientFamiliesPer24Hours AuditThreshold `json:"client_families_per_24_hours"`
	ConcurrentRoutes90Secs   AuditThreshold `json:"concurrent_routes_90_seconds"`
	NodeFanout10Seconds      AuditThreshold `json:"node_fanout_10_seconds"`
	ProbeEpisodes10Minutes   AuditThreshold `json:"probe_episodes_10_minutes"`
	ActiveConnections        AuditThreshold `json:"active_connections"`
	LegacyDeviceExcess       AuditThreshold `json:"legacy_device_excess"`
	CloneOverlapSeconds      int            `json:"clone_overlap_seconds"`
	AutoActionConfidence     float64        `json:"auto_action_confidence"`
}

type AuditAction string

const (
	AuditActionRestrict AuditAction = "restrict"
	AuditActionWarn     AuditAction = "warn"
)

type SubscriptionAuditWindowSnapshot struct {
	WindowMinutes     int      `json:"window_minutes"`
	PullCount         int      `json:"pull_count"`
	RawRequestCount   int      `json:"raw_request_count"`
	LogicalPullWeight float64  `json:"logical_pull_weight"`
	RouteCount        int      `json:"route_count"`
	RouteNovelty      float64  `json:"route_novelty_weight"`
	ClientFamilyCount int      `json:"client_family_count"`
	FormatFamilyCount int      `json:"format_family_count"`
	SourceIPCount     int      `json:"source_ip_count"`
	RegionCount       int      `json:"region_count"`
	ClientFormatCount int      `json:"client_format_count"`
	Regions           []string `json:"regions"`
}

type SubscriptionAuditRisk struct {
	Level              string                          `json:"level"`
	Score              int                             `json:"score"`
	Signals            []string                        `json:"signals"`
	Confidence         float64                         `json:"confidence"`
	EvidenceCategories []string                        `json:"evidence_categories"`
	CounterEvidence    []string                        `json:"counter_evidence"`
	RecommendedAction  string                          `json:"recommended_action"`
	IdentityMode       string                          `json:"identity_mode"`
	HardBlock          bool                            `json:"hard_block"`
	Reason             string                          `json:"reason,omitempty"`
	Short              SubscriptionAuditWindowSnapshot `json:"short"`
	Long               SubscriptionAuditWindowSnapshot `json:"long"`
}

type SubscriptionPullAudit struct {
	ID                   int64     `json:"id"`
	UserID               int64     `json:"user_id"`
	DeviceIDHash         string    `json:"device_id_hash,omitempty"`
	RepresentationID     string    `json:"representation_id,omitempty"`
	SubscriptionRevision string    `json:"subscription_revision,omitempty"`
	RawRequestWeight     float64   `json:"raw_request_weight"`
	LogicalPullWeight    float64   `json:"logical_pull_weight"`
	LogicalFetchID       string    `json:"logical_fetch_id,omitempty"`
	RouteID              string    `json:"route_id,omitempty"`
	RouteNoveltyWeight   float64   `json:"route_novelty_weight"`
	DedupeReason         string    `json:"dedupe_reason,omitempty"`
	ConditionalRequest   bool      `json:"conditional_request"`
	SourceIP             string    `json:"source_ip"`
	SourceCountryCode    string    `json:"source_country_code,omitempty"`
	SourceCountry        string    `json:"source_country,omitempty"`
	SourceProvince       string    `json:"source_province,omitempty"`
	SourceCity           string    `json:"source_city,omitempty"`
	SourceISP            string    `json:"source_isp,omitempty"`
	GeoDatabaseRevision  string    `json:"geo_database_revision,omitempty"`
	UserAgent            string    `json:"user_agent,omitempty"`
	ClientName           string    `json:"client_name"`
	Format               string    `json:"format"`
	RequestedFormat      string    `json:"requested_format,omitempty"`
	AutoDetected         bool      `json:"auto_detected"`
	ProfileID            *int64    `json:"profile_id,omitempty"`
	AgeEncrypted         bool      `json:"age_encrypted"`
	TokenKind            string    `json:"token_kind"`
	Outcome              string    `json:"outcome"`
	Reason               string    `json:"reason,omitempty"`
	RiskEligible         bool      `json:"-"`
	RequestedAt          time.Time `json:"requested_at"`
	CreatedAt            time.Time `json:"created_at"`
}

type SubscriptionAccessState struct {
	UserID              int64                  `json:"user_id"`
	Suspended           bool                   `json:"suspended"`
	SuspendedAt         *time.Time             `json:"suspended_at,omitempty"`
	Reason              string                 `json:"reason,omitempty"`
	TriggerAuditID      *int64                 `json:"trigger_audit_id,omitempty"`
	TriggerRisk         *SubscriptionAuditRisk `json:"trigger_risk,omitempty"`
	EvaluationStartedAt time.Time              `json:"evaluation_started_at"`
	ResumedAt           *time.Time             `json:"resumed_at,omitempty"`
	ResumedBy           *int64                 `json:"resumed_by,omitempty"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

type SubscriptionAuditDimension struct {
	Key        string    `json:"key"`
	Label      string    `json:"label"`
	Secondary  string    `json:"secondary,omitempty"`
	PullCount  int64     `json:"pull_count"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type SubscriptionAuditUserSummary struct {
	UserID             int64                 `json:"user_id"`
	Username           string                `json:"username"`
	Nickname           string                `json:"nickname"`
	RiskLevel          string                `json:"risk_level"`
	RiskScore          int                   `json:"risk_score"`
	RiskSignals        []string              `json:"risk_signals"`
	Confidence         float64               `json:"confidence"`
	EvidenceCategories []string              `json:"evidence_categories"`
	CounterEvidence    []string              `json:"counter_evidence"`
	RecommendedAction  string                `json:"recommended_action"`
	IdentityMode       string                `json:"identity_mode"`
	DeviceCount        int                   `json:"device_count"`
	RawRequestCount    int64                 `json:"raw_request_count"`
	LogicalPullWeight  float64               `json:"logical_pull_weight"`
	RouteCount         int                   `json:"route_count"`
	Suspended          bool                  `json:"suspended"`
	SuspendedAt        *time.Time            `json:"suspended_at,omitempty"`
	SuspensionReason   string                `json:"suspension_reason,omitempty"`
	PullCount          int64                 `json:"pull_count"`
	SuccessfulCount    int64                 `json:"successful_count"`
	DeniedCount        int64                 `json:"denied_count"`
	SourceIPCount      int                   `json:"source_ip_count"`
	RegionCount        int                   `json:"region_count"`
	ClientFormatCount  int                   `json:"client_format_count"`
	LastSeenAt         time.Time             `json:"last_seen_at"`
	CurrentRisk        SubscriptionAuditRisk `json:"current_risk"`
}

type SubscriptionAuditOverview struct {
	WindowHours       int                            `json:"window_hours"`
	GeneratedAt       time.Time                      `json:"generated_at"`
	GeoDatabase       GeoDatabaseStatus              `json:"geo_database"`
	Policy            AuditPolicy                    `json:"policy"`
	ReportingUsers    int                            `json:"reporting_user_count"`
	ElevatedRiskCount int                            `json:"elevated_risk_count"`
	SuspendedCount    int                            `json:"suspended_count"`
	TotalPulls        int64                          `json:"total_pulls"`
	UniqueSourceIPs   int                            `json:"unique_source_ips"`
	Users             []SubscriptionAuditUserSummary `json:"users"`
}

type SubscriptionAuditUserDetail struct {
	Summary SubscriptionAuditUserSummary `json:"summary"`
	Sources []SubscriptionAuditDimension `json:"sources"`
	Regions []SubscriptionAuditDimension `json:"regions"`
	Clients []SubscriptionAuditDimension `json:"clients"`
	Formats []SubscriptionAuditDimension `json:"formats"`
	Recent  []SubscriptionPullAudit      `json:"recent"`
	Access  SubscriptionAccessState      `json:"access"`
}

type CombinedAuditUserSummary struct {
	UserID                int64     `json:"user_id"`
	Username              string    `json:"username"`
	Nickname              string    `json:"nickname"`
	RiskLevel             string    `json:"risk_level"`
	RiskScore             int       `json:"risk_score"`
	RiskSignals           []string  `json:"risk_signals"`
	Confidence            float64   `json:"confidence"`
	EvidenceCategories    []string  `json:"evidence_categories"`
	CounterEvidence       []string  `json:"counter_evidence"`
	RecommendedAction     string    `json:"recommended_action"`
	ConnectionRiskLevel   string    `json:"connection_risk_level"`
	ConnectionRiskScore   int       `json:"connection_risk_score"`
	ConnectionObserved    bool      `json:"connection_observed"`
	SubscriptionRiskLevel string    `json:"subscription_risk_level"`
	SubscriptionRiskScore int       `json:"subscription_risk_score"`
	SubscriptionObserved  bool      `json:"subscription_observed"`
	SubscriptionSuspended bool      `json:"subscription_suspended"`
	LastSeenAt            time.Time `json:"last_seen_at"`
}

type CombinedAuditOverview struct {
	WindowHours       int                        `json:"window_hours"`
	GeneratedAt       time.Time                  `json:"generated_at"`
	ElevatedRiskCount int                        `json:"elevated_risk_count"`
	SuspendedCount    int                        `json:"suspended_count"`
	Users             []CombinedAuditUserSummary `json:"users"`
}

type TrafficRuntimePolicy struct {
	UserID            int64  `json:"user_id"`
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
	ResetAnchor       string `json:"reset_anchor,omitempty"`
	PreviousPeriodKey string `json:"previous_period_key,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	QuotaState        string `json:"quota_state,omitempty"`
	EnforcementMode   string `json:"enforcement_mode,omitempty"`
	PolicyRevision    int64  `json:"policy_revision,omitempty"`
}

// Linux exposes TCP Fast Open through the net.ipv4.tcp_fastopen bitmask, where
// the client and server halves are enabled independently. The Agent reports the
// raw value plus the derived state so Controller never has to guess whether a
// generated tcp_fast_open listen option can actually take effect; most hosts
// ship with the client bit only.
const (
	TCPFastOpenClientBit = 0x1
	TCPFastOpenServerBit = 0x2
)

// TCPFastOpen* are the reported host states. An empty state means the Agent did
// not report one, which is different from a host that answered "disabled".
const (
	TCPFastOpenStateUnknown      = ""
	TCPFastOpenStateUnavailable  = "unavailable"
	TCPFastOpenStateDisabled     = "disabled"
	TCPFastOpenStateClient       = "client"
	TCPFastOpenStateServer       = "server"
	TCPFastOpenStateClientServer = "client_server"
)

// TCPFastOpenStateFromMask maps a net.ipv4.tcp_fastopen value to a report state.
func TCPFastOpenStateFromMask(mask int) string {
	client := mask&TCPFastOpenClientBit != 0
	server := mask&TCPFastOpenServerBit != 0
	switch {
	case client && server:
		return TCPFastOpenStateClientServer
	case server:
		return TCPFastOpenStateServer
	case client:
		return TCPFastOpenStateClient
	default:
		return TCPFastOpenStateDisabled
	}
}

// NormalizeTCPFastOpenState keeps an unknown or unsupported reported value out
// of persisted server state.
func NormalizeTCPFastOpenState(state string) string {
	switch state {
	case TCPFastOpenStateUnavailable, TCPFastOpenStateDisabled, TCPFastOpenStateClient, TCPFastOpenStateServer, TCPFastOpenStateClientServer:
		return state
	default:
		return TCPFastOpenStateUnknown
	}
}

// TCPFastOpenServerReady reports whether the host accepts TFO on listeners, the
// only bit that matters for a generated inbound.
func TCPFastOpenServerReady(state string) bool {
	return state == TCPFastOpenStateServer || state == TCPFastOpenStateClientServer
}

type HealthReport struct {
	AgentID                   string       `json:"agent_id"`
	Status                    ServerStatus `json:"status"`
	PublicIPv4                string       `json:"public_ipv4"`
	PublicIPv6                string       `json:"public_ipv6"`
	InterfaceIPv6             string       `json:"interface_ipv6"`
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
	DiskTotalBytes            uint64       `json:"disk_total_bytes"`
	TCPConnectionCount        uint64       `json:"tcp_connection_count"`
	UDPConnectionCount        uint64       `json:"udp_connection_count"`
	ProcessCount              uint64       `json:"process_count"`
	AgentVersion              string       `json:"agent_version"`
	AgentBuild                string       `json:"agent_build"`
	SingBoxVersion            string       `json:"sing_box_version"`
	KernelCapabilities        []string     `json:"kernel_capabilities,omitempty"`
	TCPFastOpenState          string       `json:"tcp_fastopen_state,omitempty"`
	TCPFastOpenValue          int          `json:"tcp_fastopen_value,omitempty"`
	NetworkUploadBPS          uint64       `json:"network_upload_bps"`
	NetworkDownloadBPS        uint64       `json:"network_download_bps"`
	NetworkTotalUploadBytes   uint64       `json:"network_total_upload_bytes"`
	NetworkTotalDownloadBytes uint64       `json:"network_total_download_bytes"`
	ConnectivityProbeEnabled  bool         `json:"-"`
	ConnectivityProbeTarget   string       `json:"-"`
	ConnectivityAvailable     bool         `json:"-"`
	ConnectivityLatencyMS     int64        `json:"-"`
	ConnectivityCheckedAt     time.Time    `json:"-"`
	ConnectivityError         string       `json:"-"`
	Timestamp                 time.Time    `json:"timestamp"`
	AppliedConfigVersion      int64              `json:"applied_config_version,omitempty"`
	AppliedConfigDigest       string             `json:"applied_config_digest,omitempty"`
	RemoteAccess              RemoteAccessReport `json:"remote_access,omitempty"`
}

type MetricReport struct {
	ReportID           string    `json:"report_id"`
	SampledAt          time.Time `json:"sampled_at"`
	CPUUsagePercent    float64   `json:"cpu_usage_percent"`
	MemoryUsedBytes    uint64    `json:"memory_used_bytes"`
	MemoryTotalBytes   uint64    `json:"memory_total_bytes"`
	DiskUsedBytes      uint64    `json:"disk_used_bytes"`
	DiskTotalBytes     uint64    `json:"disk_total_bytes"`
	TCPConnectionCount uint64    `json:"tcp_connection_count"`
	UDPConnectionCount uint64    `json:"udp_connection_count"`
	ProcessCount       uint64    `json:"process_count"`
	NetworkUploadBPS   uint64    `json:"network_upload_bps"`
	NetworkDownloadBPS uint64    `json:"network_download_bps"`
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
	DiskUsedBytes         uint64    `json:"disk_used_bytes"`
	DiskTotalBytes        uint64    `json:"disk_total_bytes"`
	TCPConnectionCount    uint64    `json:"tcp_connection_count"`
	UDPConnectionCount    uint64    `json:"udp_connection_count"`
	ProcessCount          uint64    `json:"process_count"`
	NetworkUploadBPS      uint64    `json:"network_upload_bps"`
	NetworkDownloadBPS    uint64    `json:"network_download_bps"`
	TrafficUploadBytes    uint64    `json:"traffic_upload_bytes"`
	TrafficDownloadBytes  uint64    `json:"traffic_download_bytes"`
	ConnectivityAvailable *bool     `json:"connectivity_available,omitempty"`
	ConnectivityLatencyMS int64     `json:"connectivity_latency_ms"`
	ResourceRecorded      bool      `json:"resource_recorded"`
	SampledAt             time.Time `json:"sampled_at"`
}

type ServerResourceMetricPoint struct {
	SampledAt          time.Time `json:"sampled_at"`
	CPUUsagePercent    float64   `json:"cpu_usage_percent"`
	MemoryUsedBytes    uint64    `json:"memory_used_bytes"`
	MemoryTotalBytes   uint64    `json:"memory_total_bytes"`
	DiskUsedBytes      uint64    `json:"disk_used_bytes"`
	DiskTotalBytes     uint64    `json:"disk_total_bytes"`
	TCPConnectionCount uint64    `json:"tcp_connection_count"`
	UDPConnectionCount uint64    `json:"udp_connection_count"`
	ProcessCount       uint64    `json:"process_count"`
	NetworkUploadBPS   uint64    `json:"network_upload_bps"`
	NetworkDownloadBPS uint64    `json:"network_download_bps"`
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
