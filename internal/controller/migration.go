package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// migrations serves the PR 7 migration tooling:
//
//	POST /api/v1/migrations/standalone-inbounds/preview
//	POST /api/v1/migrations/standalone-inbounds/apply
//	POST /api/v1/migrations/materialize-plans/preview
//	POST /api/v1/migrations/materialize-plans/apply
//
// All operations are idempotent and never touch the legacy authorization
// tables. They prepare plan-mode data; the runtime switch stays explicit via
// the authorization.mode setting.
func (s *Server) migrations(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/migrations/")
	if len(parts) != 2 || r.Method != http.MethodPost {
		fail(w, errors.New("expected /api/v1/migrations/:tool/:action"), 404)
		return
	}
	switch parts[0] {
	case "standalone-inbounds":
		switch parts[1] {
		case "preview":
			s.migrationInboundPathsPreview(w, r)
		case "apply":
			s.migrationInboundPathsApply(w, r)
		default:
			fail(w, errors.New("expected standalone-inbounds preview or apply"), 404)
		}
	case "materialize-plans":
		switch parts[1] {
		case "preview":
			s.migrationMaterializePreview(w, r)
		case "apply":
			s.migrationMaterializeApply(w, r)
		default:
			fail(w, errors.New("expected materialize-plans preview or apply"), 404)
		}
	default:
		fail(w, errors.New("unknown migration tool"), 404)
	}
}

type standaloneInboundView struct {
	InboundID      int64  `json:"inbound_id"`
	InboundName    string `json:"inbound_name"`
	Protocol       string `json:"protocol"`
	Port           int    `json:"port"`
	ServerID       int64  `json:"server_id"`
	ServerName     string `json:"server_name"`
	EffectiveUsers int    `json:"effective_users"`
	ExitRegion     string `json:"exit_region"`
}

func (s *Server) migrationStandaloneInbounds(ctx context.Context) ([]standaloneInboundView, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	pathOnInbound := map[int64]bool{}
	for _, path := range data.ProxyPaths {
		if path.Enabled {
			pathOnInbound[path.InboundID] = true
		}
	}
	serverByName := map[int64]string{}
	serverRegion := map[int64]string{}
	for _, server := range data.Servers {
		serverByName[server.ID] = server.Name
		region, _ := core.EffectiveServerRegion(server)
		serverRegion[server.ID] = region
	}
	usersByInbound := map[int64]int{}
	for _, binding := range effectiveInboundUsersForRouting(data) {
		if binding.Enabled {
			usersByInbound[binding.InboundID]++
		}
	}
	out := []standaloneInboundView{}
	for _, inbound := range data.Inbounds {
		if !inbound.Enabled {
			continue
		}
		if pathOnInbound[inbound.ID] {
			continue
		}
		out = append(out, standaloneInboundView{
			InboundID:      inbound.ID,
			InboundName:    inbound.Name,
			Protocol:       string(inbound.Protocol),
			Port:           inbound.Port,
			ServerID:       inbound.ServerID,
			ServerName:     serverByName[inbound.ServerID],
			EffectiveUsers: usersByInbound[inbound.ID],
			ExitRegion:     serverRegion[inbound.ServerID],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InboundID < out[j].InboundID })
	return out, nil
}

func (s *Server) migrationInboundPathsPreview(w http.ResponseWriter, r *http.Request) {
	inbounds, err := s.migrationStandaloneInbounds(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"standalone_inbounds": inbounds, "count": len(inbounds), "runtime_authorization_mode": s.authorizationMode(r.Context())})
}

// migrationInboundPathsApply creates one zero-step direct proxy_path per
// standalone inbound so every client-visible node is a proxy_path node. The
// derived path credentials replace the standalone credential, so affected
// servers receive a full deployment before the change is acknowledged.
func (s *Server) migrationInboundPathsApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InboundIDs []int64 `json:"inbound_ids"`
	}
	_ = decode(w, r, &req)
	selected := map[int64]bool{}
	for _, id := range req.InboundIDs {
		if id > 0 {
			selected[id] = true
		}
	}
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	pathOnInbound := map[int64]bool{}
	for _, path := range data.ProxyPaths {
		if path.Enabled {
			pathOnInbound[path.InboundID] = true
		}
	}
	created := []map[string]any{}
	skipped := []map[string]any{}
	serverSet := map[int64]bool{}
	for _, inbound := range data.Inbounds {
		if !inbound.Enabled {
			continue
		}
		if len(selected) > 0 && !selected[inbound.ID] {
			continue
		}
		if pathOnInbound[inbound.ID] {
			skipped = append(skipped, map[string]any{"inbound_id": inbound.ID, "reason": "already has proxy path branches"})
			continue
		}
		secret, err := randomPathSecret()
		if err != nil {
			fail(w, err, 500)
			return
		}
		path := model.ProxyPath{
			Kind:           model.ProxyPathKindDirect,
			NameMode:       model.ProxyPathNameAuto,
			InboundID:      inbound.ID,
			ExitRegionMode: "auto",
			Secret:         secret,
			Enabled:        true,
		}
		if err := s.store.CreateProxyPath(r.Context(), &path); err != nil {
			fail(w, err, 500)
			return
		}
		serverSet[inbound.ServerID] = true
		created = append(created, map[string]any{"inbound_id": inbound.ID, "proxy_path_id": path.ID})
	}
	// Topology changed: deploy the affected root servers so their inbound
	// auth-user sets include the new derived path credentials.
	queued := map[int64]int{}
	for serverID := range serverSet {
		tasks, _, err := s.deployConfiguration(r.Context(), serverID, false)
		if err != nil {
			fail(w, err, 500)
			return
		}
		queued[serverID] = len(tasks)
	}
	auditReq(s, r, "apply", "migration-inbound-paths", fmt.Sprintf("created=%d skipped=%d", len(created), len(skipped)))
	write(w, 200, map[string]any{
		"created":                    created,
		"skipped":                    skipped,
		"created_count":              len(created),
		"skipped_count":              len(skipped),
		"deployments":                queued,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func randomPathSecret() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ---------------------------------------------------------------------------
// Materialize legacy authorization results into migration plans
// ---------------------------------------------------------------------------

type migrationPlanGroup struct {
	Signature         string   `json:"signature"`
	Users             []int64  `json:"users"`
	NodeKeys          []string `json:"node_keys"`
	SpeedLimitMbps    int      `json:"speed_limit_mbps"`
	TrafficLimitBytes int64    `json:"traffic_limit_bytes"`
	TrafficResetMode  string   `json:"traffic_reset_mode"`
	TrafficResetDay   int      `json:"traffic_reset_day"`
	ExistingPlanID    int64    `json:"existing_plan_id,omitempty"`
}

type materializeInput struct {
	data   store.FullRoutingConfig
	groups []migrationPlanGroup
}

func (s *Server) materializeGroups(ctx context.Context) (*materializeInput, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	legacy := core.LegacyAccessInput{
		Inbounds:                     data.Inbounds,
		InboundUsers:                 data.InboundUsers,
		UserGroups:                   data.UserGroups,
		UserGroupMembers:             data.UserGroupMembers,
		InboundAccessGrants:          data.InboundAccessGrants,
		ExternalOutbounds:            data.ExternalOutbounds,
		ExternalOutboundAccessGrants: data.ExternalOutboundAccessGrants,
		Paths:                        data.ProxyPaths,
		Steps:                        data.ProxyPathSteps,
	}
	legacyKeys := core.LegacyEffectiveNodeKeys(data.Users, legacy)
	type groupAcc struct {
		users    []int64
		policies []core.UserLimitPolicy
		keys     map[string]bool
	}
	acc := map[string]*groupAcc{}
	for _, user := range data.Users {
		if user.Status != "active" || strings.HasPrefix(user.Username, "__oboard_") {
			continue
		}
		keys := legacyKeys[user.ID]
		policy := core.EffectiveUserLimitPolicy(user, data.UserGroups, data.UserGroupMembers)
		sig := migrationSignature(keys, policy)
		g, ok := acc[sig]
		if !ok {
			g = &groupAcc{}
			acc[sig] = g
		}
		if g.keys == nil {
			g.keys = keys
		}
		g.users = append(g.users, user.ID)
		g.policies = append(g.policies, policy)
	}
	groups := []migrationPlanGroup{}
	for sig, g := range acc {
		policy := dominantPolicy(g.policies)
		groups = append(groups, migrationPlanGroup{
			Signature:         sig,
			Users:             g.users,
			NodeKeys:          sortedNodeKeys(g.keys),
			SpeedLimitMbps:    policy.SpeedLimitMbps,
			TrafficLimitBytes: policy.TrafficLimitBytes,
			TrafficResetMode:  policy.TrafficResetMode,
			TrafficResetDay:   policy.TrafficResetDay,
		})
	}
	sort.Slice(groups, func(i, j int) bool { return len(groups[i].Users) > len(groups[j].Users) })
	return &materializeInput{data: data, groups: groups}, nil
}

func dominantPolicy(policies []core.UserLimitPolicy) core.UserLimitPolicy {
	counts := map[core.UserLimitPolicy]int{}
	for _, p := range policies {
		counts[p]++
	}
	best := core.UserLimitPolicy{}
	bestCount := 0
	for p, n := range counts {
		if n > bestCount {
			best, bestCount = p, n
		}
	}
	return best
}

func migrationSignature(keys map[string]bool, policy core.UserLimitPolicy) string {
	raw, _ := json.Marshal(struct {
		Keys              []string `json:"keys"`
		SpeedLimitMbps    int      `json:"speed"`
		TrafficLimitBytes int64    `json:"traffic"`
		TrafficResetMode  string   `json:"mode"`
		TrafficResetDay   int      `json:"day"`
	}{Keys: sortedNodeKeys(keys), SpeedLimitMbps: policy.SpeedLimitMbps, TrafficLimitBytes: policy.TrafficLimitBytes, TrafficResetMode: policy.TrafficResetMode, TrafficResetDay: policy.TrafficResetDay})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Server) migrationMaterializePreview(w http.ResponseWriter, r *http.Request) {
	in, err := s.materializeGroups(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	existing, err := s.existingPlanKeySets(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	views := make([]migrationPlanGroup, 0, len(in.groups))
	wouldCreate := 0
	for _, g := range in.groups {
		g.ExistingPlanID = existing[groupKeySet(g.NodeKeys)]
		if g.ExistingPlanID == 0 {
			wouldCreate++
		}
		views = append(views, g)
	}
	write(w, 200, map[string]any{"groups": views, "group_count": len(views), "would_create": wouldCreate, "runtime_authorization_mode": s.authorizationMode(r.Context())})
}

func groupKeySet(keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	raw, _ := json.Marshal(sorted)
	return string(raw)
}

// existingPlanKeySets maps the JSON of a plan's sorted active node keys to the
// plan id for enabled plans.
func (s *Server) existingPlanKeySets(ctx context.Context) (map[string]int64, error) {
	plans, err := s.store.ListSubscriptionPlans(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListAllPlanNodes(ctx)
	if err != nil {
		return nil, err
	}
	byPlan := map[int64][]string{}
	for _, n := range nodes {
		byPlan[n.PlanID] = append(byPlan[n.PlanID], core.NodeKeyOf(n.NodeType, n.NodeID))
	}
	out := map[string]int64{}
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		out[groupKeySet(byPlan[plan.ID])] = plan.ID
	}
	return out, nil
}

func (s *Server) migrationMaterializeApply(w http.ResponseWriter, r *http.Request) {
	in, err := s.materializeGroups(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	existing, err := s.existingPlanKeySets(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	valid, err := s.assignableNodeKeySet(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	created := []map[string]any{}
	skippedNodes := 0
	planByGroup := make([]int64, len(in.groups))
	for i, g := range in.groups {
		keys := make([]string, 0, len(g.NodeKeys))
		for _, key := range g.NodeKeys {
			if !valid[key] {
				skippedNodes++
				continue
			}
			keys = append(keys, key)
		}
		planID := existing[groupKeySet(keys)]
		if planID == 0 {
			nodes := make([]model.SubscriptionPlanNode, 0, len(keys))
			for _, key := range keys {
				nodeType, nodeID, ok := core.ParseNodeKey(key)
				if !ok {
					skippedNodes++
					continue
				}
				nodes = append(nodes, model.SubscriptionPlanNode{NodeType: nodeType, NodeID: nodeID, SourceType: model.PlanNodeSourceExplicit, Enabled: true})
			}
			plan := &model.SubscriptionPlan{
				Name:              migrationPlanName(i + 1),
				Description:       "由旧授权结果自动生成；Shadow 对比无差异后可切换 authorization_mode=plan",
				Enabled:           true,
				SpeedLimitMbps:    g.SpeedLimitMbps,
				TrafficLimitBytes: g.TrafficLimitBytes,
				TrafficResetMode:  g.TrafficResetMode,
				TrafficResetDay:   g.TrafficResetDay,
			}
			if err := s.store.CreateSubscriptionPlan(r.Context(), plan, nodes); err != nil {
				fail(w, err, 500)
				return
			}
			planID = plan.ID
			created = append(created, map[string]any{"plan_id": planID, "name": plan.Name, "node_count": len(nodes)})
		}
		planByGroup[i] = planID
	}
	userPlan := map[int64]int64{}
	for i, g := range in.groups {
		for _, userID := range g.Users {
			userPlan[userID] = planByGroup[i]
		}
	}
	bindings := make([]model.UserPlanBinding, 0, len(userPlan))
	for userID, planID := range userPlan {
		bindings = append(bindings, model.UserPlanBinding{UserID: userID, PlanID: planID, Enabled: true})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].UserID < bindings[j].UserID })
	bound := 0
	if len(bindings) > 0 {
		if err := s.store.SetUserPlanBindings(r.Context(), bindings); err != nil {
			fail(w, err, 500)
			return
		}
		bound = len(bindings)
	}
	auditReq(s, r, "apply", "migration-materialize-plans", fmt.Sprintf("plans=%d users=%d", len(created), bound))
	write(w, 200, map[string]any{
		"created_plans":              created,
		"created_count":              len(created),
		"bound_users":                bound,
		"skipped_nodes":              skippedNodes,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func migrationPlanName(index int) string {
	return fmt.Sprintf("迁移方案 %d", index)
}

func (s *Server) assignableNodeKeySet(ctx context.Context) (map[string]bool, error) {
	data, err := s.loadPlanAssignmentData(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := core.BuildAssignableNodeCatalog(core.AssignableNodeCatalogInput{
		Servers:           data.config.Servers,
		Inbounds:          data.config.Inbounds,
		ProxyPaths:        data.config.ProxyPaths,
		ProxyPathSteps:    data.config.ProxyPathSteps,
		EgressResults:     data.config.ProxyPathEgressResults,
		ExternalOutbounds: data.config.ExternalOutbounds,
		ServerOnline:      data.serverOnline,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, node := range nodes {
		out[node.Key] = true
	}
	return out, nil
}

// guardAssignableNodeDelete blocks deletion of nodes referenced by active plan
// revisions and auto-removes draft-only references. It returns the references
// for the audit trail.
func (s *Server) guardAssignableNodeDelete(ctx context.Context, nodeType model.AssignableNodeType, nodeID int64) (store.PlanNodeReferences, error) {
	refs, err := s.store.PlanNodeReferences(ctx, nodeType, nodeID)
	if err != nil {
		return refs, err
	}
	if len(refs.Active) > 0 {
		names := make([]string, 0, len(refs.Active))
		for _, ref := range refs.Active {
			names = append(names, ref.Name)
		}
		return refs, fmt.Errorf("node is referenced by active subscription plan(s): %s; publish a plan change to remove it first", strings.Join(names, ", "))
	}
	if len(refs.Draft) > 0 {
		if err := s.store.RemovePlanNodeFromDraftRevisions(ctx, nodeType, nodeID); err != nil {
			return refs, err
		}
	}
	return refs, nil
}
