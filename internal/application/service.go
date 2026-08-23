package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type Service struct {
	store *store.Store
}

func NewService(store *store.Store) *Service {
	return &Service{store: store}
}

func (s *Service) Inventory(ctx context.Context, principal Principal) (InventoryDTO, error) {
	servers, err := s.ListServers(ctx, principal)
	if err != nil {
		return InventoryDTO{}, err
	}
	users, err := s.ListUsers(ctx, principal)
	if err != nil {
		return InventoryDTO{}, err
	}
	online := 0
	for _, server := range servers {
		if server.Status == model.ServerOnline {
			online++
		}
	}
	return InventoryDTO{Servers: servers, Users: users, ServerCount: len(servers), OnlineCount: online, UserCount: len(users)}, nil
}

func (s *Service) ListServers(ctx context.Context, principal Principal) ([]ServerDTO, error) {
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ServerDTO, 0, len(items))
	for _, item := range items {
		if principal.AllowsInt64("server_ids", item.ID) {
			out = append(out, serverDTO(item))
		}
	}
	return out, nil
}

func (s *Service) GetServer(ctx context.Context, principal Principal, id int64) (ServerDTO, error) {
	if !principal.AllowsInt64("server_ids", id) {
		return ServerDTO{}, sql.ErrNoRows
	}
	item, err := s.store.GetServer(ctx, id)
	if err != nil {
		return ServerDTO{}, err
	}
	return serverDTO(*item), nil
}

func (s *Service) ListUsers(ctx context.Context, principal Principal) ([]UserDTO, error) {
	items, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserDTO, 0, len(items))
	for _, item := range items {
		if principal.AllowsInt64("user_ids", item.ID) {
			out = append(out, userDTO(item))
		}
	}
	return out, nil
}

func (s *Service) Topology(ctx context.Context, principal Principal) (TopologyDTO, error) {
	servers, err := s.ListServers(ctx, principal)
	if err != nil {
		return TopologyDTO{}, err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return TopologyDTO{}, err
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return TopologyDTO{}, err
	}
	steps, err := s.store.ListProxyPathSteps(ctx)
	if err != nil {
		return TopologyDTO{}, err
	}
	out := TopologyDTO{Servers: servers, Inbounds: []InboundDTO{}, Paths: []ProxyPathDTO{}, Steps: []ProxyPathStepDTO{}}
	allowedInbounds := map[int64]bool{}
	for _, item := range inbounds {
		if !principal.AllowsInt64("server_ids", item.ServerID) {
			continue
		}
		allowedInbounds[item.ID] = true
		out.Inbounds = append(out.Inbounds, InboundDTO{ID: item.ID, Revision: revision(item.UpdatedAt), ServerID: item.ServerID, Name: item.Name, Protocol: item.Protocol, ListenIP: item.ListenIP, Port: item.Port, DNSDomain: item.DNSDomain, DNSSyncEnabled: item.DNSSyncEnabled, TLS: item.TLS, Enabled: item.Enabled, AdvancedEnabled: strings.TrimSpace(item.ConfigJSON) != "" && strings.TrimSpace(item.ConfigJSON) != "{}"})
	}
	allowedPaths := map[int64]bool{}
	for _, item := range paths {
		if !allowedInbounds[item.InboundID] || !principal.AllowsInt64("proxy_path_ids", item.ID) {
			continue
		}
		allowedPaths[item.ID] = true
		out.Paths = append(out.Paths, ProxyPathDTO{ID: item.ID, Revision: revision(item.UpdatedAt), Kind: item.Kind, Name: item.Name, InboundID: item.InboundID, EffectiveExitRegionCode: item.EffectiveExitRegionCode, ExitRegionStatus: item.ExitRegionStatus, Enabled: item.Enabled})
	}
	for _, item := range steps {
		if !allowedPaths[item.PathID] || item.ServerID != nil && !principal.AllowsInt64("server_ids", *item.ServerID) {
			continue
		}
		out.Steps = append(out.Steps, ProxyPathStepDTO{ID: item.ID, Revision: revision(item.UpdatedAt), PathID: item.PathID, Position: item.Position, NodeType: item.NodeType, TransportMode: item.TransportMode, ProcessingRole: item.ProcessingRole, ServerID: item.ServerID, InboundID: item.InboundID, ExternalOutboundID: item.ExternalOutboundID, AdvancedConfigured: strings.TrimSpace(item.ConfigJSON) != "" && strings.TrimSpace(item.ConfigJSON) != "{}"})
	}
	return out, nil
}

func (s *Service) ListAuditIncidents(ctx context.Context, principal Principal, limit int) ([]model.AuditIncident, error) {
	items, err := s.store.ListAuditIncidents(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.AuditIncident, 0, len(items))
	for _, item := range items {
		if principal.AllowsInt64("user_ids", item.UserID) {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Service) GetAuditIncident(ctx context.Context, principal Principal, id string) (*model.AuditIncident, error) {
	item, err := s.store.GetAuditIncident(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if !principal.AllowsInt64("user_ids", item.UserID) {
		return nil, sql.ErrNoRows
	}
	return item, nil
}

func (s *Service) ListSubscriptionPlans(ctx context.Context, principal Principal) ([]SubscriptionPlanDTO, error) {
	items, err := s.store.ListSubscriptionPlans(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SubscriptionPlanDTO, 0, len(items))
	for _, item := range items {
		if !principal.AllowsInt64("subscription_plan_ids", item.ID) {
			continue
		}
		// The list view carries the node sets so MCP clients can verify that
		// planned nodes actually entered the plan (and which set is live).
		latest, err := s.store.ListPlanRevisionNodes(ctx, item.LatestRevisionID)
		if err != nil {
			return nil, err
		}
		current, err := s.store.ListActivePlanNodes(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, subscriptionPlanDTO(item, latest, current))
	}
	return out, nil
}

// GetUser returns one user's management summary. User identities are
// sensitive; the caller must classify the output accordingly.
func (s *Service) GetUser(ctx context.Context, principal Principal, id int64) (UserDTO, error) {
	if !principal.AllowsInt64("user_ids", id) {
		return UserDTO{}, sql.ErrNoRows
	}
	item, err := s.store.GetUser(ctx, id)
	if err != nil {
		return UserDTO{}, err
	}
	return userDTO(*item), nil
}

func (s *Service) ListUserGroups(ctx context.Context, principal Principal) ([]model.UserGroup, error) {
	items, err := s.store.ListUserGroups(ctx)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) ListUserGroupMembers(ctx context.Context, principal Principal) ([]model.UserGroupMember, error) {
	items, err := s.store.ListUserGroupMembers(ctx)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) ListUserDevices(ctx context.Context, principal Principal, userID int64) ([]model.UserDevice, error) {
	if !principal.AllowsInt64("user_ids", userID) {
		return nil, sql.ErrNoRows
	}
	return s.store.ListUserDevices(ctx, userID)
}

// ListOutbounds returns redacted server outbound views. The auth config
// (config_json) is never included so credentials stay out of MCP output.
func (s *Service) ListOutbounds(ctx context.Context, principal Principal) ([]map[string]any, error) {
	items, err := s.store.ListOutbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.ServerID) {
			continue
		}
		out = append(out, map[string]any{
			"id": item.ID, "revision": revision(item.UpdatedAt), "server_id": item.ServerID,
			"next_server_id": item.NextServerID, "name": item.Name, "protocol": item.Protocol,
			"target_address": item.TargetAddress, "target_port": item.TargetPort,
			"advanced_configured": strings.TrimSpace(item.ConfigJSON) != "" && strings.TrimSpace(item.ConfigJSON) != "{}",
			"enabled":             item.Enabled, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) ListRoutingRules(ctx context.Context, principal Principal) ([]RoutingRuleDTO, error) {
	items, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoutingRuleDTO, 0, len(items))
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.ServerID) {
			continue
		}
		out = append(out, routingRuleDTO(item))
	}
	return out, nil
}

func (s *Service) ListRoutingRuleSets(ctx context.Context, principal Principal) ([]RoutingRuleSetDTO, error) {
	items, err := s.store.ListRoutingRuleSets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoutingRuleSetDTO, 0, len(items))
	for _, item := range items {
		out = append(out, routingRuleSetDTO(item))
	}
	return out, nil
}

// ListExternalOutbounds returns redacted imported-node views. The node auth
// config (config_json) is never included.
func (s *Service) ListExternalOutbounds(ctx context.Context, principal Principal) ([]map[string]any, error) {
	items, err := s.store.ListExternalOutbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if item.ServerID != nil && !principal.AllowsInt64("server_ids", *item.ServerID) {
			continue
		}
		out = append(out, map[string]any{
			"id": item.ID, "revision": revision(item.UpdatedAt), "server_id": item.ServerID,
			"name": item.Name, "protocol": item.Protocol, "scope": item.Scope,
			"target_address": item.TargetAddress, "target_port": item.TargetPort,
			"region_mode": item.RegionMode, "region_code": item.RegionCode,
			"effective_region_code": item.EffectiveRegionCode, "region_status": item.RegionStatus,
			"expose_to_users": item.ExposeToUsers, "enabled": item.Enabled,
			"advanced_configured": strings.TrimSpace(item.ConfigJSON) != "" && strings.TrimSpace(item.ConfigJSON) != "{}",
			"created_at":          item.CreatedAt, "updated_at": item.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) ListWARPProfiles(ctx context.Context, principal Principal) ([]map[string]any, error) {
	items, err := s.store.ListWARPProfiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.ServerID) {
			continue
		}
		configured := strings.TrimSpace(item.ConfigJSON) != ""
		out = append(out, map[string]any{
			"id": item.ID, "revision": revision(item.UpdatedAt), "server_id": item.ServerID,
			"name": item.Name, "status": item.Status, "mtu": item.MTU, "dns_strategy": item.DNSStrategy,
			"error": item.Error, "enabled": item.Enabled, "configured": configured,
			"created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) ListNodePresets(ctx context.Context, principal Principal) ([]map[string]any, error) {
	items, err := s.store.ListNodePresets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var config any = map[string]any{}
		if err := json.Unmarshal([]byte(item.ConfigJSON), &config); err != nil {
			config = map[string]any{}
		}
		out = append(out, map[string]any{
			"id": item.ID, "name": item.Name, "protocol": item.Protocol, "kind": item.Kind,
			"config_json": config, "default_port": item.DefaultPort, "remark": item.Remark,
			"builtin": item.Builtin, "enabled": item.Enabled, "usage_count": item.UsageCount,
			"created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) ListSnellProfiles(ctx context.Context, principal Principal) ([]model.SnellProfile, error) {
	return s.store.ListSnellProfiles(ctx)
}

// ListDNSLists returns DNS lists (encrypted and bootstrap) with candidates.
func (s *Service) ListDNSLists(ctx context.Context, principal Principal) ([]model.DNSList, error) {
	items, err := s.store.ListDNSLists(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]model.DNSList, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out, nil
}

// ListDNSCredentials returns DNS provider credential metadata. Credential
// secrets and tokens are never included.
func (s *Service) ListDNSCredentials(ctx context.Context, principal Principal) ([]model.DNSCredential, error) {
	items, err := s.store.ListDNSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// GetServerDNSPolicy returns one server's DNS policy when it exists.
func (s *Service) GetServerDNSPolicy(ctx context.Context, principal Principal, serverID int64) (model.ServerDNSPolicy, error) {
	if !principal.AllowsInt64("server_ids", serverID) {
		return model.ServerDNSPolicy{}, sql.ErrNoRows
	}
	policy, err := s.store.GetServerDNSPolicy(ctx, serverID)
	if err != nil {
		return model.ServerDNSPolicy{}, err
	}
	return *policy, nil
}

// ListPortForwards returns port forwards the principal may manage.
func (s *Service) ListPortForwards(ctx context.Context, principal Principal) ([]model.PortForward, error) {
	items, err := s.store.ListPortForwards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.PortForward, 0, len(items))
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.SourceServerID) || !principal.AllowsInt64("server_ids", item.TargetServerID) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// ListTunnels returns tunnels the principal may manage.
func (s *Service) ListTunnels(ctx context.Context, principal Principal) ([]model.Tunnel, error) {
	items, err := s.store.ListTunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Tunnel, 0, len(items))
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.SourceServerID) || !principal.AllowsInt64("server_ids", item.TargetServerID) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) GetSubscriptionPlan(ctx context.Context, principal Principal, id int64) (SubscriptionPlanDTO, error) {
	if !principal.AllowsInt64("subscription_plan_ids", id) {
		return SubscriptionPlanDTO{}, sql.ErrNoRows
	}
	item, err := s.store.GetSubscriptionPlan(ctx, id)
	if err != nil {
		return SubscriptionPlanDTO{}, err
	}
	latest, err := s.store.ListPlanRevisionNodes(ctx, item.LatestRevisionID)
	if err != nil {
		return SubscriptionPlanDTO{}, err
	}
	current, err := s.store.ListActivePlanNodes(ctx, item.ID)
	if err != nil {
		return SubscriptionPlanDTO{}, err
	}
	return subscriptionPlanDTO(*item, latest, current), nil
}

func (s *Service) ListSubscriptionRelays(ctx context.Context) ([]map[string]any, error) {
	items, err := s.store.ListSubscriptionRelays(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	activeURL := strings.TrimRight(settings["subscription_relay_url"], "/")
	now := time.Now().UTC()
	out := make([]map[string]any, 0, len(items))
	for _, relay := range items {
		status := relay.Status
		if relay.TokenHash == "" && status != "uninstalled" {
			status = "pending"
		} else if status != "uninstalled" && relay.LastSeenAt != nil && now.Sub(*relay.LastSeenAt) > 2*time.Minute {
			status = "offline"
		}
		out = append(out, map[string]any{
			"id": relay.ID, "name": relay.Name, "public_url": relay.PublicURL, "status": status,
			"enrolled": relay.TokenHash != "", "active": activeURL != "" && strings.TrimRight(relay.PublicURL, "/") == activeURL,
			"version": relay.Version, "build": relay.Build, "commit": relay.Commit, "os": relay.OS, "arch": relay.Arch,
			"service_manager": relay.ServiceManager, "update_target_version": relay.UpdateTargetVersion,
			"update_target_build": relay.UpdateTargetBuild, "update_requested_at": relay.UpdateRequestedAt,
			"last_update_error": relay.LastUpdateError, "last_seen_at": relay.LastSeenAt,
			"enrollment_expires_at": relay.EnrollmentExpiresAt, "created_at": relay.CreatedAt, "updated_at": relay.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) Query(ctx context.Context, principal Principal, capability string, arguments json.RawMessage) (any, error) {
	switch capability {
	case "inventory.read":
		return s.Inventory(ctx, principal)
	case "servers.list":
		return s.ListServers(ctx, principal)
	case "servers.get":
		var input struct {
			ID int64 `json:"id"`
		}
		if err := strictUnmarshal(arguments, &input); err != nil || input.ID <= 0 {
			return nil, errors.New("valid server id is required")
		}
		return s.GetServer(ctx, principal, input.ID)
	case "users.list":
		return s.ListUsers(ctx, principal)
	case "users.get":
		var input struct {
			ID int64 `json:"id"`
		}
		if err := strictUnmarshal(arguments, &input); err != nil || input.ID <= 0 {
			return nil, errors.New("valid user id is required")
		}
		return s.GetUser(ctx, principal, input.ID)
	case "user_groups.list":
		return s.ListUserGroups(ctx, principal)
	case "user_group_members.list":
		return s.ListUserGroupMembers(ctx, principal)
	case "user_devices.list":
		var input struct {
			UserID int64 `json:"user_id"`
		}
		if err := strictUnmarshal(arguments, &input); err != nil || input.UserID <= 0 {
			return nil, errors.New("valid user_id is required")
		}
		return s.ListUserDevices(ctx, principal, input.UserID)
	case "outbounds.list":
		return s.ListOutbounds(ctx, principal)
	case "routing_rules.list":
		return s.ListRoutingRules(ctx, principal)
	case "routing_rule_sets.list":
		return s.ListRoutingRuleSets(ctx, principal)
	case "external_outbounds.list":
		return s.ListExternalOutbounds(ctx, principal)
	case "warp_profiles.list":
		return s.ListWARPProfiles(ctx, principal)
	case "node_presets.list":
		return s.ListNodePresets(ctx, principal)
	case "snell_profiles.list":
		return s.ListSnellProfiles(ctx, principal)
	case "dns_lists.list":
		return s.ListDNSLists(ctx, principal)
	case "dns_credentials.list":
		return s.ListDNSCredentials(ctx, principal)
	case "servers.dns_policy.get":
		var input struct {
			ServerID int64 `json:"server_id"`
		}
		if err := strictUnmarshal(arguments, &input); err != nil || input.ServerID <= 0 {
			return nil, errors.New("valid server_id is required")
		}
		return s.GetServerDNSPolicy(ctx, principal, input.ServerID)
	case "port_forwards.list":
		return s.ListPortForwards(ctx, principal)
	case "tunnels.list":
		return s.ListTunnels(ctx, principal)
	case "subscription_plans.list":
		return s.ListSubscriptionPlans(ctx, principal)
	case "subscription_plans.get":
		var input struct {
			ID int64 `json:"id"`
		}
		if err := strictUnmarshal(arguments, &input); err != nil || input.ID <= 0 {
			return nil, errors.New("valid subscription plan id is required")
		}
		return s.GetSubscriptionPlan(ctx, principal, input.ID)
	case "subscription_relays.list":
		return s.ListSubscriptionRelays(ctx)
	case "topology.read":
		return s.Topology(ctx, principal)
	case "servers.onboarding.plan":
		return s.PlanServerOnboarding(ctx, principal, arguments)
	case "proxy_paths.plan":
		return s.PlanProxyPath(ctx, principal, arguments)
	case "deployments.plan":
		return s.PlanDeployment(ctx, principal, arguments)
	case "audit.incident_response.plan":
		return s.PlanIncidentResponse(ctx, principal, arguments)
	case "audit.incidents.list":
		return s.ListAuditIncidents(ctx, principal, 50)
	case "audit.incidents.get":
		var input struct {
			ID string `json:"id"`
		}
		if err := strictUnmarshal(arguments, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return nil, errors.New("incident id is required")
		}
		return s.GetAuditIncident(ctx, principal, input.ID)
	default:
		return nil, errors.New("unsupported query capability")
	}
}

func strictUnmarshal(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func ParseID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid resource id")
	}
	return id, nil
}
