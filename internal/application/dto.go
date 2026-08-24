package application

import (
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type ServerDTO struct {
	ID                          int64                      `json:"id"`
	Revision                    string                     `json:"revision"`
	Name                        string                     `json:"name"`
	Status                      model.ServerStatus         `json:"status"`
	EntryAddress                string                     `json:"entry_address"`
	EntryIPMode                 model.EntryIPMode          `json:"entry_ip_mode"`
	RegionMode                  string                     `json:"region_mode"`
	RegionCode                  string                     `json:"region_code"`
	DetectedRegionCode          string                     `json:"detected_region_code"`
	PublicIPv4                  string                     `json:"public_ipv4"`
	PublicIPv6                  string                     `json:"public_ipv6"`
	InterfaceIPv6               string                     `json:"interface_ipv6"`
	IPStack                     model.IPStack              `json:"ip_stack"`
	ListenIP                    string                     `json:"listen_ip"`
	ListenMode                  model.ListenMode           `json:"listen_mode"`
	UDPInboundMode              model.UDPInboundMode       `json:"udp_inbound_mode"`
	MTUMode                     model.MTUMode              `json:"mtu_mode"`
	MTUValue                    int                        `json:"mtu_value"`
	MTUProbeHost                string                     `json:"mtu_probe_host"`
	MTUProbePort                int                        `json:"mtu_probe_port"`
	MTUOverheadBytes            int                        `json:"mtu_overhead_bytes"`
	BBREnabled                  bool                       `json:"bbr_enabled"`
	PortRangeStart              int                        `json:"port_range_start"`
	PortRangeEnd                int                        `json:"port_range_end"`
	InternalPortRangeStart      int                        `json:"internal_port_range_start"`
	InternalPortRangeEnd        int                        `json:"internal_port_range_end"`
	PortPolicyRevision          int64                      `json:"port_policy_revision"`
	AgentConnected              bool                       `json:"agent_connected"`
	AgentVersion                string                     `json:"agent_version"`
	AgentBuild                  string                     `json:"agent_build"`
	KernelVersion               string                     `json:"kernel_version"`
	KernelCapabilities          []string                   `json:"kernel_capabilities"`
	ConnectionAuditEnabled      bool                       `json:"connection_audit_enabled"`
	ResourceHistoryEnabled      bool                       `json:"resource_history_enabled"`
	MonitoringMode              string                     `json:"monitoring_mode"`
	TrafficResetMode            string                     `json:"traffic_reset_mode"`
	TrafficResetDay             int                        `json:"traffic_reset_day"`
	OfflineNotifyEnabled        bool                       `json:"offline_notify_enabled"`
	OfflineAfterSeconds         int                        `json:"offline_after_seconds"`
	ExpiresAt                   *time.Time                 `json:"expires_at,omitempty"`
	RenewalCycle                model.ServerRenewalCycle   `json:"renewal_cycle"`
	AutoRenewEnabled            bool                       `json:"auto_renew_enabled"`
	ExpiryNotifyEnabled         bool                       `json:"expiry_notify_enabled"`
	LastAutoRenewedAt           *time.Time                 `json:"last_auto_renewed_at,omitempty"`
	LatencyProbeEnabled         bool                       `json:"latency_probe_enabled"`
	LatencyProbeMode            model.LatencyProbeMode     `json:"latency_probe_mode"`
	LatencyProbePublicTarget    model.ConnectivityTarget   `json:"latency_probe_public_target"`
	LatencyProbeIntervalSeconds int                        `json:"latency_probe_interval_seconds"`
	LatencyProbeSampleCount     int                        `json:"latency_probe_sample_count"`
	LatencyProbeRegions         []model.LatencyProbeRegion `json:"latency_probe_regions,omitempty"`
	LatencyProbeMaxTargets      int                        `json:"latency_probe_max_targets"`
	LatencyProbeResourceVersion string                     `json:"latency_probe_resource_version"`
	TimeCorrectionMode          model.TimeCorrectionMode   `json:"time_correction_mode"`
	TimeCheckStatus             string                     `json:"time_check_status"`
	LastSeenAt                  *time.Time                 `json:"last_seen_at,omitempty"`
	CreatedAt                   time.Time                  `json:"created_at"`
	UpdatedAt                   time.Time                  `json:"updated_at"`
}

type UserDTO struct {
	ID                        int64      `json:"id"`
	Revision                  string     `json:"revision"`
	Username                  string     `json:"username"`
	Nickname                  string     `json:"nickname"`
	Role                      model.Role `json:"role"`
	Status                    string     `json:"status"`
	SpeedLimitMbps            int        `json:"speed_limit_mbps"`
	TrafficLimitBytes         string     `json:"traffic_limit_bytes"`
	TrafficUsedBytes          string     `json:"traffic_used_bytes"`
	SubscriptionConfigured    bool       `json:"subscription_configured"`
	SubscriptionAgeEnabled    bool       `json:"subscription_age_enabled"`
	SubscriptionSuspended     bool       `json:"subscription_suspended"`
	SubscriptionSuspendedAt   *time.Time `json:"subscription_suspended_at,omitempty"`
	SubscriptionSuspendReason string     `json:"subscription_suspend_reason,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

type InboundDTO struct {
	ID              int64          `json:"id"`
	Revision        string         `json:"revision"`
	ServerID        int64          `json:"server_id"`
	Name            string         `json:"name"`
	Protocol        model.Protocol `json:"protocol"`
	ListenIP        string         `json:"listen_ip"`
	Port            int            `json:"port"`
	DNSDomain       string         `json:"dns_domain"`
	DNSSyncEnabled  bool           `json:"dns_sync_enabled"`
	TLS             bool           `json:"tls"`
	Enabled         bool           `json:"enabled"`
	AdvancedEnabled bool           `json:"advanced_configured"`
}

type ProxyPathDTO struct {
	ID                      int64               `json:"id"`
	Revision                string              `json:"revision"`
	Kind                    model.ProxyPathKind `json:"kind"`
	Name                    string              `json:"name"`
	InboundID               int64               `json:"inbound_id"`
	EffectiveExitRegionCode string              `json:"effective_exit_region_code"`
	ExitRegionStatus        string              `json:"exit_region_status"`
	Enabled                 bool                `json:"enabled"`
}

type ProxyPathStepDTO struct {
	ID                 int64                            `json:"id"`
	Revision           string                           `json:"revision"`
	PathID             int64                            `json:"path_id"`
	Position           int                              `json:"position"`
	NodeType           model.ProxyPathStepNodeType      `json:"node_type"`
	TransportMode      model.ProxyPathStepTransportMode `json:"transport_mode"`
	ProcessingRole     bool                             `json:"processing_role"`
	ServerID           *int64                           `json:"server_id,omitempty"`
	InboundID          *int64                           `json:"inbound_id,omitempty"`
	ExternalOutboundID *int64                           `json:"external_outbound_id,omitempty"`
	AdvancedConfigured bool                             `json:"advanced_configured"`
}

type RoutingRuleDTO struct {
	ID                    int64                   `json:"id"`
	Revision              string                  `json:"revision"`
	ServerID              int64                   `json:"server_id"`
	Name                  string                  `json:"name"`
	Scope                 string                  `json:"scope"`
	ProxyPathID           *int64                  `json:"proxy_path_id"`
	StageStepID           *int64                  `json:"stage_step_id"`
	SortPosition          int                     `json:"sort_position"`
	MatchSource           string                  `json:"match_source"`
	RuleSetID             *int64                  `json:"rule_set_id"`
	DNSResolver           string                  `json:"dns_resolver"`
	Priority              int                     `json:"priority"`
	Action                model.RouteAction       `json:"action"`
	OutboundID            *int64                  `json:"outbound_id"`
	ExternalOutboundID    *int64                  `json:"external_outbound_id"`
	TargetProxyPathID     *int64                  `json:"target_proxy_path_id"`
	IPv4TargetProxyPathID *int64                  `json:"ipv4_target_proxy_path_id"`
	IPv6TargetProxyPathID *int64                  `json:"ipv6_target_proxy_path_id"`
	FamilyDNSStrategy     model.FamilyDNSStrategy `json:"family_dns_strategy"`
	OutboundTag           string                  `json:"outbound_tag"`
	InterfaceName         string                  `json:"interface_name"`
	SourcePrefix          string                  `json:"source_prefix"`
	SyncGroupID           string                  `json:"sync_group_id"`
	MatchConfigured       bool                    `json:"match_configured"`
	Enabled               bool                    `json:"enabled"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
}

type RoutingRuleSetDTO struct {
	ID             int64      `json:"id"`
	Revision       string     `json:"revision"`
	Name           string     `json:"name"`
	URL            string     `json:"url"`
	Format         string     `json:"format"`
	MihomoBehavior string     `json:"mihomo_behavior"`
	Status         string     `json:"status"`
	LastError      string     `json:"last_error"`
	LastAttemptAt  *time.Time `json:"last_attempt_at"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type InventoryDTO struct {
	Servers     []ServerDTO `json:"servers"`
	Users       []UserDTO   `json:"users"`
	ServerCount int         `json:"server_count"`
	OnlineCount int         `json:"online_count"`
	UserCount   int         `json:"user_count"`
}

type TopologyDTO struct {
	Servers  []ServerDTO        `json:"servers"`
	Inbounds []InboundDTO       `json:"inbounds"`
	Paths    []ProxyPathDTO     `json:"proxy_paths"`
	Steps    []ProxyPathStepDTO `json:"proxy_path_steps"`
}

type SubscriptionPlanNodeDTO struct {
	NodeType     model.AssignableNodeType `json:"node_type"`
	NodeID       int64                    `json:"node_id"`
	DisplayGroup string                   `json:"display_group"`
	SourceType   model.PlanNodeSourceType `json:"source_type"`
	SourceRuleID int64                    `json:"source_rule_id,omitempty"`
	SortPosition *int                     `json:"sort_position,omitempty"`
}

type SubscriptionPlanDTO struct {
	ID                int64                     `json:"id"`
	Name              string                    `json:"name"`
	Description       string                    `json:"description"`
	Enabled           bool                      `json:"enabled"`
	LockVersion       int64                     `json:"lock_version"`
	CurrentRevisionID int64                     `json:"current_revision_id"`
	LatestRevisionID  int64                     `json:"latest_revision_id"`
	PendingRevisionID int64                     `json:"pending_revision_id,omitempty"`
	SpeedLimitMbps    int                       `json:"speed_limit_mbps"`
	TrafficLimitBytes string                    `json:"traffic_limit_bytes"`
	TrafficResetMode  string                    `json:"traffic_reset_mode"`
	TrafficResetDay   int                       `json:"traffic_reset_day"`
	Nodes             []SubscriptionPlanNodeDTO `json:"nodes"`
	CurrentNodes      []SubscriptionPlanNodeDTO `json:"current_nodes"`
}

func subscriptionPlanDTO(item model.SubscriptionPlan, latest, current []model.SubscriptionPlanNode) SubscriptionPlanDTO {
	return SubscriptionPlanDTO{
		ID: item.ID, Name: item.Name, Description: item.Description, Enabled: item.Enabled,
		LockVersion: item.LockVersion, CurrentRevisionID: item.CurrentRevisionID, LatestRevisionID: item.LatestRevisionID,
		PendingRevisionID: item.PendingRevisionID, SpeedLimitMbps: item.SpeedLimitMbps,
		TrafficLimitBytes: formatInt64(item.TrafficLimitBytes), TrafficResetMode: item.TrafficResetMode,
		TrafficResetDay: item.TrafficResetDay, Nodes: subscriptionPlanNodeDTOs(latest), CurrentNodes: subscriptionPlanNodeDTOs(current),
	}
}

func subscriptionPlanNodeDTOs(items []model.SubscriptionPlanNode) []SubscriptionPlanNodeDTO {
	out := make([]SubscriptionPlanNodeDTO, 0, len(items))
	for _, item := range items {
		out = append(out, SubscriptionPlanNodeDTO{
			NodeType: item.NodeType, NodeID: item.NodeID, DisplayGroup: item.DisplayGroup,
			SourceType: item.SourceType, SourceRuleID: item.SourceRuleID, SortPosition: item.SortPosition,
		})
	}
	return out
}

func serverDTO(item model.Server) ServerDTO {
	return ServerDTO{
		ID: item.ID, Revision: revision(item.UpdatedAt), Name: item.Name, Status: item.Status,
		EntryAddress: item.EntryAddress, EntryIPMode: item.EntryIPMode, RegionMode: item.RegionMode,
		RegionCode: item.RegionCode, DetectedRegionCode: item.DetectedRegionCode, PublicIPv4: item.PublicIPv4,
		PublicIPv6: item.PublicIPv6, InterfaceIPv6: item.InterfaceIPv6, IPStack: item.IPStack,
		ListenIP: item.ListenIP, ListenMode: item.ListenMode, UDPInboundMode: item.UDPInboundMode,
		MTUMode: item.MTUMode, MTUValue: item.MTUValue, MTUProbeHost: item.MTUProbeHost,
		MTUProbePort: item.MTUProbePort, MTUOverheadBytes: item.MTUOverheadBytes, BBREnabled: item.BBREnabled,
		PortRangeStart: item.PortRangeStart, PortRangeEnd: item.PortRangeEnd,
		InternalPortRangeStart: item.InternalPortRangeStart, InternalPortRangeEnd: item.InternalPortRangeEnd,
		PortPolicyRevision: item.PortPolicyRevision, AgentConnected: item.AgentID != "",
		AgentVersion: item.AgentVersion, AgentBuild: item.AgentBuild, KernelVersion: item.SingBoxVersion,
		KernelCapabilities:     append([]string{}, item.KernelCapabilities...),
		ConnectionAuditEnabled: item.ConnectionAuditEnabled, ResourceHistoryEnabled: item.ResourceHistoryEnabled,
		MonitoringMode: item.MonitoringMode, TrafficResetMode: item.TrafficResetMode, TrafficResetDay: item.TrafficResetDay,
		OfflineNotifyEnabled: item.OfflineNotifyEnabled, OfflineAfterSeconds: item.OfflineAfterSeconds,
		ExpiresAt: item.ExpiresAt, RenewalCycle: item.RenewalCycle, AutoRenewEnabled: item.AutoRenewEnabled,
		ExpiryNotifyEnabled: item.ExpiryNotifyEnabled, LastAutoRenewedAt: item.LastAutoRenewedAt,
		LatencyProbeEnabled: item.LatencyProbeEnabled, LatencyProbeMode: item.LatencyProbeMode,
		LatencyProbePublicTarget: item.LatencyProbePublicTarget, LatencyProbeIntervalSeconds: item.LatencyProbeIntervalSeconds,
		LatencyProbeSampleCount: item.LatencyProbeSampleCount, LatencyProbeRegions: item.LatencyProbeRegions,
		LatencyProbeMaxTargets:      item.LatencyProbeMaxTargets,
		LatencyProbeResourceVersion: item.LatencyProbeResourceVersion,
		TimeCorrectionMode:          item.TimeCorrectionMode, TimeCheckStatus: item.TimeCheckStatus,
		LastSeenAt: item.LastSeenAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func userDTO(item model.User) UserDTO {
	return UserDTO{
		ID: item.ID, Revision: revision(item.UpdatedAt), Username: item.Username, Nickname: item.Nickname,
		Role: item.Role, Status: item.Status, SpeedLimitMbps: item.SpeedLimitMbps,
		TrafficLimitBytes: formatInt64(item.TrafficLimitBytes), TrafficUsedBytes: formatInt64(item.TrafficUsedBytes),
		SubscriptionConfigured: item.SubscriptionToken != "", SubscriptionAgeEnabled: item.SubscriptionAgeEnabled,
		SubscriptionSuspended: item.SubscriptionSuspended, SubscriptionSuspendedAt: item.SubscriptionSuspendedAt,
		SubscriptionSuspendReason: item.SubscriptionSuspendReason, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func routingRuleDTO(item model.RoutingRule) RoutingRuleDTO {
	return RoutingRuleDTO{
		ID: item.ID, Revision: revision(item.UpdatedAt), ServerID: item.ServerID, Name: item.Name,
		Scope: item.Scope, ProxyPathID: item.ProxyPathID, StageStepID: item.StageStepID,
		SortPosition: item.SortPosition, MatchSource: item.MatchSource, RuleSetID: item.RuleSetID,
		DNSResolver: item.DNSResolver, Priority: item.Priority, Action: item.Action, OutboundID: item.OutboundID,
		ExternalOutboundID: item.ExternalOutboundID, TargetProxyPathID: item.TargetProxyPathID,
		IPv4TargetProxyPathID: item.IPv4TargetProxyPathID, IPv6TargetProxyPathID: item.IPv6TargetProxyPathID,
		FamilyDNSStrategy: item.FamilyDNSStrategy, OutboundTag: item.OutboundTag,
		InterfaceName: item.InterfaceName, SourcePrefix: item.SourcePrefix, SyncGroupID: item.SyncGroupID,
		MatchConfigured: strings.TrimSpace(item.MatchJSON) != "" && strings.TrimSpace(item.MatchJSON) != "{}",
		Enabled:         item.Enabled, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func routingRuleSetDTO(item model.RoutingRuleSet) RoutingRuleSetDTO {
	return RoutingRuleSetDTO{
		ID: item.ID, Revision: item.Revision, Name: item.Name, URL: item.URL, Format: item.Format,
		MihomoBehavior: item.MihomoBehavior, Status: item.Status, LastError: item.LastError,
		LastAttemptAt: item.LastAttemptAt, LastSuccessAt: item.LastSuccessAt,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func revision(updatedAt time.Time) string {
	return updatedAt.UTC().Format(time.RFC3339Nano)
}
