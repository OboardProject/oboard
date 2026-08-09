package application

import (
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type ServerDTO struct {
	ID                     int64                    `json:"id"`
	Revision               string                   `json:"revision"`
	Name                   string                   `json:"name"`
	Status                 model.ServerStatus       `json:"status"`
	EntryAddress           string                   `json:"entry_address"`
	PublicIPv4             string                   `json:"public_ipv4"`
	PublicIPv6             string                   `json:"public_ipv6"`
	InterfaceIPv6          string                   `json:"interface_ipv6"`
	RegionCode             string                   `json:"region_code"`
	DetectedRegionCode     string                   `json:"detected_region_code"`
	IPStack                model.IPStack            `json:"ip_stack"`
	ListenMode             model.ListenMode         `json:"listen_mode"`
	UDPInboundMode         model.UDPInboundMode     `json:"udp_inbound_mode"`
	MTUMode                model.MTUMode            `json:"mtu_mode"`
	MTUValue               int                      `json:"mtu_value"`
	BBREnabled             bool                     `json:"bbr_enabled"`
	AgentConnected         bool                     `json:"agent_connected"`
	AgentVersion           string                   `json:"agent_version"`
	AgentBuild             string                   `json:"agent_build"`
	KernelVersion          string                   `json:"kernel_version"`
	ConnectionAuditEnabled bool                     `json:"connection_audit_enabled"`
	TimeCorrectionMode     model.TimeCorrectionMode `json:"time_correction_mode"`
	TimeCheckStatus        string                   `json:"time_check_status"`
	LastSeenAt             *time.Time               `json:"last_seen_at,omitempty"`
	CreatedAt              time.Time                `json:"created_at"`
	UpdatedAt              time.Time                `json:"updated_at"`
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
		EntryAddress: item.EntryAddress, PublicIPv4: item.PublicIPv4, PublicIPv6: item.PublicIPv6,
		InterfaceIPv6: item.InterfaceIPv6, RegionCode: item.RegionCode, DetectedRegionCode: item.DetectedRegionCode,
		IPStack: item.IPStack, ListenMode: item.ListenMode,
		UDPInboundMode: item.UDPInboundMode, MTUMode: item.MTUMode, MTUValue: item.MTUValue, BBREnabled: item.BBREnabled,
		AgentConnected: item.AgentID != "", AgentVersion: item.AgentVersion, AgentBuild: item.AgentBuild,
		KernelVersion: item.SingBoxVersion, ConnectionAuditEnabled: item.ConnectionAuditEnabled,
		TimeCorrectionMode: item.TimeCorrectionMode, TimeCheckStatus: item.TimeCheckStatus,
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

func revision(updatedAt time.Time) string {
	return updatedAt.UTC().Format(time.RFC3339Nano)
}
