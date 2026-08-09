package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

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
		out = append(out, subscriptionPlanDTO(item, nil, nil))
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
