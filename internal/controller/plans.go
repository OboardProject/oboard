package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// planAssignmentData is the read-only snapshot the node catalog and plan APIs
// are computed from. It mirrors the legacy authorization inputs plus the new
// plan model, without changing how subscription generation reads them yet.
type planAssignmentData struct {
	users        []model.User
	bindings     []model.UserPlanBinding
	plans        []model.SubscriptionPlan
	planNodes    []model.SubscriptionPlanNode
	exceptions   []model.UserNodeException
	config       store.FullRoutingConfig
	serverOnline map[int64]bool
}

func (s *Server) loadPlanAssignmentData(ctx context.Context) (*planAssignmentData, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListActiveUserPlanBindings(ctx)
	if err != nil {
		return nil, err
	}
	plans, err := s.store.ListSubscriptionPlans(ctx)
	if err != nil {
		return nil, err
	}
	planNodes, err := s.store.ListAllPlanNodes(ctx)
	if err != nil {
		return nil, err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return nil, err
	}
	config, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	serverOnline := make(map[int64]bool, len(config.Servers))
	for _, server := range config.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	return &planAssignmentData{
		users:        users,
		bindings:     bindings,
		plans:        plans,
		planNodes:    planNodes,
		exceptions:   exceptions,
		config:       config,
		serverOnline: serverOnline,
	}, nil
}

func (d *planAssignmentData) now() time.Time { return time.Now() }

func (d *planAssignmentData) planByID(id int64) *model.SubscriptionPlan {
	for i := range d.plans {
		if d.plans[i].ID == id {
			return &d.plans[i]
		}
	}
	return nil
}

func (d *planAssignmentData) nodeKeySet(planID int64) map[string]bool {
	out := map[string]bool{}
	for _, pn := range d.planNodes {
		if pn.PlanID == planID && pn.Enabled {
			out[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
		}
	}
	return out
}

// planMembership maps a node key to the plans that directly contain it.
func (d *planAssignmentData) planMembership() map[string][]model.SubscriptionPlanNode {
	out := map[string][]model.SubscriptionPlanNode{}
	for _, pn := range d.planNodes {
		if !pn.Enabled {
			continue
		}
		key := core.NodeKeyOf(pn.NodeType, pn.NodeID)
		out[key] = append(out[key], pn)
	}
	return out
}

// effectiveUsersByNode computes, per node key, the active users that can
// actually use the node (plan membership plus exceptions with fixed priority),
// plus raw exception counts.
func (d *planAssignmentData) effectiveUsersByNode(now time.Time) (map[string]map[int64]bool, map[string]int, map[string]int) {
	byNode := map[string]map[int64]bool{}
	allowCount := map[string]int{}
	denyCount := map[string]int{}
	active := map[int64]bool{}
	for _, user := range d.users {
		if user.Status == "active" {
			active[user.ID] = true
		}
	}
	bindingByUser := map[int64]*model.UserPlanBinding{}
	for i := range d.bindings {
		b := d.bindings[i]
		bindingByUser[b.UserID] = &b
	}
	exceptionsByUser := map[int64][]model.UserNodeException{}
	for _, ex := range d.exceptions {
		exceptionsByUser[ex.UserID] = append(exceptionsByUser[ex.UserID], ex)
	}
	planNodesByPlan := map[int64][]model.SubscriptionPlanNode{}
	for _, pn := range d.planNodes {
		if pn.Enabled {
			planNodesByPlan[pn.PlanID] = append(planNodesByPlan[pn.PlanID], pn)
		}
	}
	for _, user := range d.users {
		if !active[user.ID] {
			continue
		}
		binding := bindingByUser[user.ID]
		var plan *model.SubscriptionPlan
		if binding != nil {
			plan = d.planByID(binding.PlanID)
		}
		var planNodes []model.SubscriptionPlanNode
		if binding != nil {
			planNodes = planNodesByPlan[binding.PlanID]
		}
		for key := range core.UserEffectiveNodeSet(plan, planNodes, exceptionsByUser[user.ID], now) {
			if byNode[key] == nil {
				byNode[key] = map[int64]bool{}
			}
			byNode[key][user.ID] = true
		}
		for _, ex := range exceptionsByUser[user.ID] {
			if !ex.ExpiresAt.After(now) {
				continue
			}
			key := core.NodeKeyOf(ex.NodeType, ex.NodeID)
			if ex.Effect == model.UserNodeExceptionAllow {
				allowCount[key]++
			} else {
				denyCount[key]++
			}
		}
	}
	return byNode, allowCount, denyCount
}

// ---------------------------------------------------------------------------
// Assignable node catalog
// ---------------------------------------------------------------------------

type assignableNodeView struct {
	core.AssignableNode
	Group           string                   `json:"group,omitempty"`
	Plans           []assignableNodePlanView `json:"plans"`
	EffectiveUsers  int                      `json:"effective_users"`
	AllowExceptions int                      `json:"allow_exceptions"`
	DenyExceptions  int                      `json:"deny_exceptions"`
}

type assignableNodePlanView struct {
	PlanID       int64  `json:"plan_id"`
	Name         string `json:"name"`
	DisplayGroup string `json:"display_group"`
}

func (s *Server) assignableNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	data, err := s.loadPlanAssignmentData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
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
		fail(w, err, 500)
		return
	}
	now := data.now()
	byNode, allowCount, denyCount := data.effectiveUsersByNode(now)
	membership := data.planMembership()
	planByID := map[int64]*model.SubscriptionPlan{}
	for i := range data.plans {
		p := data.plans[i]
		planByID[p.ID] = &p
	}
	serverRegion := map[int64]string{}
	serverNameByID := map[int64]string{}
	for _, server := range data.config.Servers {
		region, _ := core.EffectiveServerRegion(server)
		serverRegion[server.ID] = region
		serverNameByID[server.ID] = server.Name
	}

	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	entryServerID := int64Query(r, "entry_server_id", 0)
	entryRegion := core.NormalizeRegionCode(r.URL.Query().Get("entry_region"))
	exitRegion := core.NormalizeRegionCode(r.URL.Query().Get("exit_region"))
	protocol := strings.TrimSpace(r.URL.Query().Get("protocol"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	planFilter := int64Query(r, "plan_id", 0)
	unassignedOnly := r.URL.Query().Get("unassigned") == "1" || strings.EqualFold(r.URL.Query().Get("unassigned"), "true")

	filtered := make([]assignableNodeView, 0, len(nodes))
	for _, node := range nodes {
		plans := membership[node.Key]
		if planFilter != 0 {
			found := false
			for _, pn := range plans {
				if pn.PlanID == planFilter {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if unassignedOnly && len(plans) > 0 {
			continue
		}
		if entryServerID != 0 && node.EntryServerID != entryServerID {
			continue
		}
		if entryRegion != "" && serverRegion[node.EntryServerID] != entryRegion {
			continue
		}
		if exitRegion != "" && node.ExitRegion != exitRegion {
			continue
		}
		if protocol != "" && string(node.EntryProtocol) != protocol {
			continue
		}
		if status != "" && string(node.Status) != status {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join([]string{
				node.Name,
				node.EntryServerName,
				string(node.EntryProtocol),
				strings.Join(node.PathSummary, " "),
				node.ExitRegion,
				serverNameByID[node.EntryServerID],
			}, " "))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		view := assignableNodeView{
			AssignableNode:  node,
			Plans:           make([]assignableNodePlanView, 0, len(plans)),
			EffectiveUsers:  len(byNode[node.Key]),
			AllowExceptions: allowCount[node.Key],
			DenyExceptions:  denyCount[node.Key],
		}
		for _, pn := range plans {
			view.Plans = append(view.Plans, assignableNodePlanView{PlanID: pn.PlanID, Name: planByID[pn.PlanID].Name, DisplayGroup: pn.DisplayGroup})
		}
		sort.Slice(view.Plans, func(i, j int) bool { return view.Plans[i].PlanID < view.Plans[j].PlanID })
		filtered = append(filtered, view)
	}

	groupBy := strings.TrimSpace(r.URL.Query().Get("group_by"))
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	sort.SliceStable(filtered, func(i, j int) bool {
		switch sortBy {
		case "entry_server":
			a, b := filtered[i].EntryServerName, filtered[j].EntryServerName
			if a == b {
				return filtered[i].Name < filtered[j].Name
			}
			return a < b
		case "exit_region":
			a, b := filtered[i].ExitRegion, filtered[j].ExitRegion
			if a == b {
				return filtered[i].Name < filtered[j].Name
			}
			return a < b
		case "users":
			if filtered[i].EffectiveUsers == filtered[j].EffectiveUsers {
				return filtered[i].Name < filtered[j].Name
			}
			return filtered[i].EffectiveUsers > filtered[j].EffectiveUsers
		case "status":
			a, b := string(filtered[i].Status), string(filtered[j].Status)
			if a == b {
				return filtered[i].Name < filtered[j].Name
			}
			return a < b
		default:
			return filtered[i].Name < filtered[j].Name
		}
	})
	for i := range filtered {
		switch groupBy {
		case "entry_server":
			filtered[i].Group = filtered[i].EntryServerName
		case "exit_region":
			filtered[i].Group = filtered[i].ExitRegion
		default:
			filtered[i].Group = ""
		}
	}

	page := intQuery(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := intQuery(r, "page_size", 50)
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	total := len(filtered)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	write(w, 200, map[string]any{"nodes": filtered[start:end], "total": total, "page": page, "page_size": pageSize})
}

type assignableNodeUserView struct {
	UserID    int64      `json:"user_id"`
	Username  string     `json:"username"`
	Nickname  string     `json:"nickname,omitempty"`
	Source    string     `json:"source"` // plan | exception_allow | exception_deny | excluded
	PlanID    int64      `json:"plan_id,omitempty"`
	PlanName  string     `json:"plan_name,omitempty"`
	Effect    string     `json:"effect,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Effective bool       `json:"effective"`
}

func (s *Server) assignableNodeDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/assignable-nodes/")
	if len(parts) != 2 {
		fail(w, errors.New("expected /api/v1/assignable-nodes/:type/:id"), 400)
		return
	}
	nodeType := model.AssignableNodeType(parts[0])
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		fail(w, errors.New("invalid node id"), 400)
		return
	}
	if id <= 0 {
		fail(w, errors.New("invalid node id"), 400)
		return
	}
	key := core.NodeKeyOf(nodeType, id)
	data, err := s.loadPlanAssignmentData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
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
		fail(w, err, 500)
		return
	}
	var node *core.AssignableNode
	for i := range nodes {
		if nodes[i].Key == key {
			node = &nodes[i]
			break
		}
	}
	if node == nil {
		fail(w, sql.ErrNoRows, 404)
		return
	}
	now := data.now()
	planByID := map[int64]*model.SubscriptionPlan{}
	for i := range data.plans {
		p := data.plans[i]
		planByID[p.ID] = &p
	}
	membership := data.planMembership()
	planViews := make([]assignableNodePlanView, 0, len(membership[key]))
	for _, pn := range membership[key] {
		planViews = append(planViews, assignableNodePlanView{PlanID: pn.PlanID, Name: planByID[pn.PlanID].Name, DisplayGroup: pn.DisplayGroup})
	}
	sort.Slice(planViews, func(i, j int) bool { return planViews[i].PlanID < planViews[j].PlanID })

	bindingByUser := map[int64]*model.UserPlanBinding{}
	for i := range data.bindings {
		b := data.bindings[i]
		bindingByUser[b.UserID] = &b
	}
	exceptionsByUser := map[int64][]model.UserNodeException{}
	nodeExceptions := []model.UserNodeException{}
	for _, ex := range data.exceptions {
		exceptionsByUser[ex.UserID] = append(exceptionsByUser[ex.UserID], ex)
		if core.NodeKeyOf(ex.NodeType, ex.NodeID) == key {
			nodeExceptions = append(nodeExceptions, ex)
		}
	}
	planNodesByPlan := map[int64][]model.SubscriptionPlanNode{}
	for _, pn := range data.planNodes {
		if pn.Enabled {
			planNodesByPlan[pn.PlanID] = append(planNodesByPlan[pn.PlanID], pn)
		}
	}
	users := []assignableNodeUserView{}
	for _, user := range data.users {
		if user.Status != "active" {
			continue
		}
		binding := bindingByUser[user.ID]
		var plan *model.SubscriptionPlan
		var planNodes []model.SubscriptionPlanNode
		if binding != nil {
			plan = planByID[binding.PlanID]
			planNodes = planNodesByPlan[binding.PlanID]
		}
		effective := core.UserEffectiveNodeSet(plan, planNodes, exceptionsByUser[user.ID], now)[key]
		view := assignableNodeUserView{UserID: user.ID, Username: user.Username, Nickname: user.Nickname, Effective: effective}
		if !effective {
			for _, ex := range exceptionsByUser[user.ID] {
				if ex.ExpiresAt.After(now) && ex.Effect == model.UserNodeExceptionDeny && core.NodeKeyOf(ex.NodeType, ex.NodeID) == key {
					view.Source = "exception_deny"
					view.Effect = string(ex.Effect)
					view.Reason = ex.Reason
					expiry := ex.ExpiresAt
					view.ExpiresAt = &expiry
					break
				}
			}
			if view.Source == "" {
				for _, pn := range membership[key] {
					if pn.PlanID == bindingPlanIDOf(binding) {
						view.Source = "excluded"
						view.PlanID = pn.PlanID
						view.PlanName = planByID[pn.PlanID].Name
						break
					}
				}
			}
			if view.Source == "" {
				continue
			}
			users = append(users, view)
			continue
		}
		for _, ex := range exceptionsByUser[user.ID] {
			if ex.ExpiresAt.After(now) && ex.Effect == model.UserNodeExceptionAllow && core.NodeKeyOf(ex.NodeType, ex.NodeID) == key {
				view.Source = "exception_allow"
				view.Effect = string(ex.Effect)
				view.Reason = ex.Reason
				expiry := ex.ExpiresAt
				view.ExpiresAt = &expiry
				break
			}
		}
		if view.Source == "" {
			view.Source = "plan"
			if binding != nil {
				view.PlanID = binding.PlanID
				view.PlanName = planByID[binding.PlanID].Name
			}
		}
		users = append(users, view)
	}
	sort.SliceStable(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })

	activeExceptions := []model.UserNodeException{}
	for _, ex := range nodeExceptions {
		if ex.ExpiresAt.After(now) {
			activeExceptions = append(activeExceptions, ex)
		}
	}
	sort.Slice(activeExceptions, func(i, j int) bool { return activeExceptions[i].ID < activeExceptions[j].ID })
	write(w, 200, map[string]any{"node": node, "plans": planViews, "users": users, "exceptions": activeExceptions})
}

func bindingPlanIDOf(binding *model.UserPlanBinding) int64 {
	if binding == nil {
		return 0
	}
	return binding.PlanID
}

// ---------------------------------------------------------------------------
// Subscription plans
// ---------------------------------------------------------------------------

func (s *Server) subscriptionPlans(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/subscription-plans/")
	parts := pathParts(r.URL.Path, "/api/v1/subscription-plans/")
	if id != 0 && len(parts) > 1 {
		s.subscriptionPlanSubroutes(w, r, id, parts[1:])
		return
	}
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			nodes, err := s.store.ListPlanNodes(r.Context(), id)
			if err != nil {
				fail(w, err, 500)
				return
			}
			members, err := s.store.ListUserPlanBindingsForPlan(r.Context(), id)
			if err != nil {
				fail(w, err, 500)
				return
			}
			write(w, 200, map[string]any{"subscription_plan": plan, "nodes": nodes, "member_count": len(members)})
			return
		}
		plans, err := s.store.ListSubscriptionPlans(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		allNodes, err := s.store.ListAllPlanNodes(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		allBindings, err := s.store.ListActiveUserPlanBindings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		nodeCount := map[int64]int{}
		for _, pn := range allNodes {
			nodeCount[pn.PlanID]++
		}
		memberCount := map[int64]int{}
		for _, binding := range allBindings {
			memberCount[binding.PlanID]++
		}
		views := make([]map[string]any, 0, len(plans))
		for _, plan := range plans {
			views = append(views, map[string]any{
				"id":                  plan.ID,
				"name":                plan.Name,
				"description":         plan.Description,
				"enabled":             plan.Enabled,
				"speed_limit_mbps":    plan.SpeedLimitMbps,
				"traffic_limit_bytes": plan.TrafficLimitBytes,
				"traffic_reset_mode":  plan.TrafficResetMode,
				"traffic_reset_day":   plan.TrafficResetDay,
				"revision":            plan.Revision,
				"active_revision":     plan.ActiveRevision,
				"draft_revision":      plan.DraftRevision,
				"node_count":          nodeCount[plan.ID],
				"member_count":        memberCount[plan.ID],
				"created_at":          plan.CreatedAt,
				"updated_at":          plan.UpdatedAt,
			})
		}
		write(w, 200, map[string]any{"subscription_plans": views})
	case http.MethodPost:
		var req struct {
			model.SubscriptionPlan
			Nodes []planNodeRequest `json:"nodes"`
		}
		if !decode(w, r, &req) {
			return
		}
		if err := validateSubscriptionPlanFields(&req.SubscriptionPlan); err != nil {
			fail(w, err, 400)
			return
		}
		req.Revision = 0
		req.ActiveRevision = 0
		req.DraftRevision = 0
		if err := s.store.CreateSubscriptionPlan(r.Context(), &req.SubscriptionPlan); err != nil {
			fail(w, err, 500)
			return
		}
		if len(req.Nodes) > 0 {
			nodes, err := s.validatePlanNodeRequests(r.Context(), req.Nodes, "", false)
			if err != nil {
				fail(w, err, 400)
				return
			}
			if err := s.store.AddPlanNodes(r.Context(), req.ID, nodes); err != nil {
				fail(w, err, 500)
				return
			}
		}
		auditReq(s, r, "create", "subscription-plan", fmt.Sprint(req.ID))
		write(w, 201, map[string]any{"subscription_plan": req.SubscriptionPlan})
	default:
		method(w)
	}
}

type planNodeRequest struct {
	NodeType     model.AssignableNodeType `json:"node_type"`
	NodeID       int64                    `json:"node_id"`
	DisplayGroup string                   `json:"display_group"`
}

func (s *Server) subscriptionPlanSubroutes(w http.ResponseWriter, r *http.Request, id int64, parts []string) {
	if len(parts) != 2 || parts[0] != "nodes" {
		fail(w, errors.New("unknown subscription plan subroute"), 404)
		return
	}
	switch parts[1] {
	case "preview":
		s.planNodesPreview(w, r, id)
	case "sync":
		s.planNodesSync(w, r, id)
	case "publish":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		if err := s.store.PublishPlanRevision(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "publish", "subscription-plan", fmt.Sprint(id))
		write(w, 200, map[string]any{"published": true})
	default:
		fail(w, errors.New("unknown subscription plan subroute"), 404)
	}
}

type planNodesSyncRequest struct {
	Op           string            `json:"op"` // add | remove | replace
	Nodes        []planNodeRequest `json:"nodes"`
	DisplayGroup string            `json:"display_group,omitempty"`
}

func (s *Server) planNodesPreview(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req planNodesSyncRequest
	if !decode(w, r, &req) {
		return
	}
	req.Op = strings.ToLower(strings.TrimSpace(req.Op))
	if req.Op == "" {
		req.Op = "add"
	}
	preview, err := s.computePlanNodesChange(r.Context(), id, req)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	write(w, 200, map[string]any{"preview": preview})
}

func (s *Server) planNodesSync(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req planNodesSyncRequest
	if !decode(w, r, &req) {
		return
	}
	req.Op = strings.ToLower(strings.TrimSpace(req.Op))
	if req.Op == "" {
		req.Op = "add"
	}
	nodes, err := s.validatePlanNodeRequests(r.Context(), req.Nodes, req.DisplayGroup, req.Op == "remove")
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	switch req.Op {
	case "add":
		err = s.store.AddPlanNodes(r.Context(), id, nodes)
	case "remove":
		err = s.store.RemovePlanNodes(r.Context(), id, nodes)
	case "replace":
		err = s.store.ReplacePlanNodes(r.Context(), id, nodes)
	default:
		fail(w, errors.New("op must be add, remove, or replace"), 400)
		return
	}
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, req.Op, "subscription-plan-nodes", fmt.Sprintf("%d:%d", id, len(nodes)))
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"subscription_plan": plan})
}

func (s *Server) computePlanNodesChange(ctx context.Context, id int64, req planNodesSyncRequest) (core.PlanChangePreview, error) {
	nodes, err := s.validatePlanNodeRequests(ctx, req.Nodes, req.DisplayGroup, req.Op == "remove")
	if err != nil {
		return core.PlanChangePreview{}, err
	}
	data, err := s.loadPlanAssignmentData(ctx)
	if err != nil {
		return core.PlanChangePreview{}, err
	}
	plan := data.planByID(id)
	if plan == nil {
		return core.PlanChangePreview{}, errors.New("subscription plan not found")
	}
	currentNodes, err := s.store.ListPlanNodes(ctx, id)
	if err != nil {
		return core.PlanChangePreview{}, err
	}
	targetNodes := append([]model.SubscriptionPlanNode(nil), currentNodes...)
	switch req.Op {
	case "add":
		existing := map[string]bool{}
		for _, pn := range targetNodes {
			existing[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
		}
		for _, pn := range nodes {
			if !existing[core.NodeKeyOf(pn.NodeType, pn.NodeID)] {
				targetNodes = append(targetNodes, pn)
			}
		}
	case "remove":
		remove := map[string]bool{}
		for _, pn := range nodes {
			remove[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
		}
		kept := targetNodes[:0]
		for _, pn := range targetNodes {
			if !remove[core.NodeKeyOf(pn.NodeType, pn.NodeID)] {
				kept = append(kept, pn)
			}
		}
		targetNodes = kept
	case "replace":
		targetNodes = nodes
	default:
		return core.PlanChangePreview{}, errors.New("op must be add, remove, or replace")
	}
	return core.PreviewPlanNodeChange(data.users, data.bindings, data.plans, data.planNodes, data.exceptions, id, targetNodes, data.config.ProxyPaths, data.config.ProxyPathSteps, data.config.Inbounds, data.serverOnline, data.now()), nil
}

func (s *Server) validatePlanNodeRequests(ctx context.Context, requests []planNodeRequest, defaultGroup string, forRemove bool) ([]model.SubscriptionPlanNode, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	if forRemove {
		out := make([]model.SubscriptionPlanNode, 0, len(requests))
		for _, req := range requests {
			if req.NodeType != model.AssignableNodeProxyPath && req.NodeType != model.AssignableNodeExternalOutbound && req.NodeType != model.AssignableNodeInbound {
				return nil, errors.New("invalid node_type")
			}
			if req.NodeID <= 0 {
				return nil, errors.New("invalid node_id")
			}
			out = append(out, model.SubscriptionPlanNode{NodeType: req.NodeType, NodeID: req.NodeID, SourceType: model.PlanNodeSourceExplicit, Enabled: true})
		}
		return out, nil
	}
	data, err := s.loadPlanAssignmentData(ctx)
	if err != nil {
		return nil, err
	}
	valid := map[string]bool{}
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
	for _, node := range nodes {
		valid[node.Key] = true
	}
	out := make([]model.SubscriptionPlanNode, 0, len(requests))
	for _, req := range requests {
		key := core.NodeKeyOf(req.NodeType, req.NodeID)
		if !valid[key] {
			return nil, fmt.Errorf("node %s is not assignable", key)
		}
		group := strings.TrimSpace(req.DisplayGroup)
		if group == "" {
			group = strings.TrimSpace(defaultGroup)
		}
		out = append(out, model.SubscriptionPlanNode{NodeType: req.NodeType, NodeID: req.NodeID, DisplayGroup: group, SourceType: model.PlanNodeSourceExplicit, Enabled: true})
	}
	return out, nil
}

func validateSubscriptionPlanFields(v *model.SubscriptionPlan) error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("name required")
	}
	if len(v.Name) > 100 {
		return errors.New("name too long")
	}
	if len(v.Description) > 500 {
		return errors.New("description too long")
	}
	if v.SpeedLimitMbps < 0 {
		return errors.New("speed_limit_mbps must be >= 0")
	}
	if v.TrafficLimitBytes < 0 {
		return errors.New("traffic_limit_bytes must be >= 0")
	}
	v.Name = strings.TrimSpace(v.Name)
	v.TrafficResetMode = normalizeControllerTrafficResetMode(v.TrafficResetMode)
	v.TrafficResetDay = normalizeControllerTrafficResetDay(v.TrafficResetDay)
	return nil
}

// ---------------------------------------------------------------------------
// User plan assignment (batch)
// ---------------------------------------------------------------------------

type userPlanAssignmentRequest struct {
	UserIDs []int64 `json:"user_ids"`
	PlanID  int64   `json:"plan_id"`
	Deploy  bool    `json:"deploy"`
}

func (s *Server) userPlanAssignment(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/users/plan-assignment/")
	action := ""
	if len(parts) == 1 && parts[0] != "" {
		action = parts[0]
	}
	switch action {
	case "preview":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		s.planAssignmentPreview(w, r)
	case "apply":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		s.planAssignmentApply(w, r)
	default:
		fail(w, errors.New("expected /api/v1/users/plan-assignment/preview or /apply"), 404)
	}
}

func (s *Server) planAssignmentPreview(w http.ResponseWriter, r *http.Request) {
	var req userPlanAssignmentRequest
	if !decode(w, r, &req) {
		return
	}
	req.UserIDs = uniquePositiveIDs(req.UserIDs)
	if len(req.UserIDs) == 0 {
		fail(w, errors.New("user_ids required"), 400)
		return
	}
	data, err := s.loadPlanAssignmentData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	targetPlan, targetNodes, err := s.resolveAssignmentTarget(r.Context(), data, req.PlanID)
	if err != nil {
		fail(w, err, 400)
		return
	}
	selected := make([]model.User, 0, len(req.UserIDs))
	byID := map[int64]model.User{}
	for _, user := range data.users {
		byID[user.ID] = user
	}
	for _, userID := range req.UserIDs {
		user, ok := byID[userID]
		if !ok {
			fail(w, fmt.Errorf("user %d not found", userID), 400)
			return
		}
		selected = append(selected, user)
	}
	preview := core.PreviewPlanAssignment(selected, data.bindings, data.plans, data.planNodes, data.exceptions, targetPlan, targetNodes, data.config.ProxyPaths, data.config.ProxyPathSteps, data.config.Inbounds, data.serverOnline, data.now())
	write(w, 200, map[string]any{"preview": preview})
}

func (s *Server) planAssignmentApply(w http.ResponseWriter, r *http.Request) {
	var req userPlanAssignmentRequest
	if !decode(w, r, &req) {
		return
	}
	req.UserIDs = uniquePositiveIDs(req.UserIDs)
	if len(req.UserIDs) == 0 {
		fail(w, errors.New("user_ids required"), 400)
		return
	}
	data, err := s.loadPlanAssignmentData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	targetPlan, targetNodes, err := s.resolveAssignmentTarget(r.Context(), data, req.PlanID)
	if err != nil {
		fail(w, err, 400)
		return
	}
	byID := map[int64]model.User{}
	for _, user := range data.users {
		byID[user.ID] = user
	}
	selected := make([]model.User, 0, len(req.UserIDs))
	for _, userID := range req.UserIDs {
		user, ok := byID[userID]
		if !ok {
			fail(w, fmt.Errorf("user %d not found", userID), 400)
			return
		}
		selected = append(selected, user)
	}
	preview := core.PreviewPlanAssignment(selected, data.bindings, data.plans, data.planNodes, data.exceptions, targetPlan, targetNodes, data.config.ProxyPaths, data.config.ProxyPathSteps, data.config.Inbounds, data.serverOnline, data.now())
	if len(preview.CapacityIssues) > 0 {
		fail(w, errors.New(strings.Join(preview.CapacityIssues, "; ")), 400)
		return
	}
	var assignedBy *int64
	if user := currentUser(r); user != nil {
		assignedBy = &user.ID
	}
	bindings := make([]model.UserPlanBinding, 0, len(req.UserIDs))
	for _, userID := range req.UserIDs {
		bindings = append(bindings, model.UserPlanBinding{UserID: userID, PlanID: req.PlanID, AssignedBy: assignedBy})
	}
	if err := s.store.SetUserPlanBindings(r.Context(), bindings); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "assign", "user-plan", fmt.Sprintf("users=%d plan=%d", len(req.UserIDs), req.PlanID))
	queued := 0
	if req.Deploy && len(preview.AffectedServers) > 0 {
		queued, err = s.queueAccessSyncForServers(r.Context(), preview.AffectedServers, "plan_assignment")
		if err != nil {
			fail(w, err, 500)
			return
		}
	}
	write(w, 200, map[string]any{"applied": true, "affected_users": len(selected), "queued_tasks": queued, "affected_servers": preview.AffectedServers})
}

func (s *Server) resolveAssignmentTarget(ctx context.Context, data *planAssignmentData, planID int64) (*model.SubscriptionPlan, []model.SubscriptionPlanNode, error) {
	if planID == 0 {
		return nil, nil, nil
	}
	plan := data.planByID(planID)
	if plan == nil {
		return nil, nil, errors.New("subscription plan not found")
	}
	if !plan.Enabled {
		return nil, nil, errors.New("subscription plan is disabled")
	}
	nodes, err := s.store.ListPlanNodes(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	return plan, nodes, nil
}

// queueAccessSyncForServers queues at most one apply_core_config per affected
// authentication server with a shared configuration version. Config generation
// still reads the legacy tables today, so unchanged servers are skipped; once
// subscription generation switches to plan data, the same call becomes the
// access-only sync described by the assignment plan.
func (s *Server) queueAccessSyncForServers(ctx context.Context, serverIDs []int64, reason string) (int, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return 0, err
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	derivedForwards, err := core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger)
	if err != nil {
		return 0, err
	}
	serverByID := map[int64]model.Server{}
	for _, server := range data.Servers {
		serverByID[server.ID] = server
	}
	type preparedCoreRefresh struct {
		serverID int64
		payload  model.ApplyCoreConfigTaskPayload
	}
	prepared := make([]preparedCoreRefresh, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		server, ok := serverByID[serverID]
		if !ok || strings.TrimSpace(server.AgentID) == "" {
			continue
		}
		if err := requireReadyWARPForFocusedApply(data, server.ID); err != nil {
			return 0, err
		}
		generated, err := s.generateServerCoreConfigWithLedger(ctx, server, data, ledger)
		if err != nil {
			return 0, err
		}
		unchanged, err := s.serverConfigUnchanged(ctx, server.ID, generated.Config)
		if err != nil {
			return 0, err
		}
		if unchanged {
			continue
		}
		forwardPlan, err := core.BuildPortForwardPlan(0, server, data.Servers, derivedForwards)
		if err != nil {
			return 0, err
		}
		if err := s.requireTrustedForwardDeploymentBaseline(ctx, server, generated.Config, forwardPlan); err != nil {
			return 0, err
		}
		payload := model.ApplyCoreConfigTaskPayload{Config: generated.Config, Reason: reason, Assets: generated.Assets}
		prepared = append(prepared, preparedCoreRefresh{serverID: server.ID, payload: payload})
	}
	if len(prepared) == 0 {
		return 0, nil
	}
	version, err := s.store.NextConfigVersion(ctx)
	if err != nil {
		return 0, err
	}
	for _, item := range prepared {
		if _, err := s.queueAgentTask(ctx, item.serverID, model.AgentTaskTypeApplyCoreConfig, item.payload, version); err != nil {
			return 0, err
		}
	}
	return len(prepared), nil
}

// ---------------------------------------------------------------------------
// User node exceptions
// ---------------------------------------------------------------------------

func (s *Server) userNodeExceptions(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/user-node-exceptions/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			items, err := s.store.ListUserNodeExceptions(r.Context())
			if err != nil {
				fail(w, err, 500)
				return
			}
			var item *model.UserNodeException
			for i := range items {
				if items[i].ID == id {
					item = &items[i]
					break
				}
			}
			if item == nil {
				fail(w, sql.ErrNoRows, 404)
				return
			}
			write(w, 200, map[string]any{"user_node_exception": item})
			return
		}
		userID := int64Query(r, "user_id", 0)
		nodeType := strings.TrimSpace(r.URL.Query().Get("node_type"))
		nodeID := int64Query(r, "node_id", 0)
		var (
			items []model.UserNodeException
			err   error
		)
		switch {
		case userID != 0:
			items, err = s.store.ListUserNodeExceptionsForUser(r.Context(), userID)
		case nodeType != "" && nodeID != 0:
			items, err = s.store.ListUserNodeExceptionsForNode(r.Context(), nodeType, nodeID)
		default:
			items, err = s.store.ListUserNodeExceptions(r.Context())
		}
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"user_node_exceptions": items})
	case http.MethodPost:
		var v model.UserNodeException
		if !decode(w, r, &v) {
			return
		}
		if err := s.validateUserNodeException(r.Context(), &v, 0); err != nil {
			fail(w, err, 400)
			return
		}
		if user := currentUser(r); user != nil {
			v.CreatedBy = &user.ID
		}
		if err := s.store.CreateUserNodeException(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "user-node-exception", fmt.Sprintf("%d:%s:%d", v.UserID, v.NodeType, v.NodeID))
		write(w, 201, map[string]any{"user_node_exception": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current := &model.UserNodeException{}
		items, err := s.store.ListUserNodeExceptions(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		found := false
		for i := range items {
			if items[i].ID == id {
				*current = items[i]
				found = true
				break
			}
		}
		if !found {
			fail(w, sql.ErrNoRows, 404)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if err := s.validateUserNodeException(r.Context(), &v, id); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateUserNodeException(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "user-node-exception", fmt.Sprint(id))
		write(w, 200, map[string]any{"user_node_exception": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteUserNodeException(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "user-node-exception", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) validateUserNodeException(ctx context.Context, v *model.UserNodeException, id int64) error {
	if _, err := s.store.GetUser(ctx, v.UserID); err != nil {
		return fmt.Errorf("user_id: %w", err)
	}
	if v.NodeType != model.AssignableNodeProxyPath && v.NodeType != model.AssignableNodeExternalOutbound && v.NodeType != model.AssignableNodeInbound {
		return errors.New("invalid node_type")
	}
	if v.NodeID <= 0 {
		return errors.New("invalid node_id")
	}
	if v.Effect != model.UserNodeExceptionAllow && v.Effect != model.UserNodeExceptionDeny {
		return errors.New("effect must be allow or deny")
	}
	if strings.TrimSpace(v.Reason) == "" {
		return errors.New("reason required")
	}
	if len(v.Reason) > 300 {
		return errors.New("reason too long")
	}
	v.Reason = strings.TrimSpace(v.Reason)
	if v.ExpiresAt.IsZero() {
		return errors.New("expires_at required")
	}
	if !v.ExpiresAt.After(time.Now()) {
		return errors.New("expires_at must be in the future")
	}
	// Validate the node exists in the catalog.
	data, err := s.loadPlanAssignmentData(ctx)
	if err != nil {
		return err
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
		return err
	}
	key := core.NodeKeyOf(v.NodeType, v.NodeID)
	for _, node := range nodes {
		if node.Key == key {
			return nil
		}
	}
	return fmt.Errorf("node %s is not assignable", key)
}

// ---------------------------------------------------------------------------
// User effective nodes
// ---------------------------------------------------------------------------

type userEffectiveNodeView struct {
	Key       string                        `json:"key"`
	NodeType  model.AssignableNodeType      `json:"node_type"`
	NodeID    int64                         `json:"node_id"`
	Name      string                        `json:"name"`
	Source    string                        `json:"source"`
	PlanID    int64                         `json:"plan_id,omitempty"`
	PlanName  string                        `json:"plan_name,omitempty"`
	Effect    model.UserNodeExceptionEffect `json:"effect,omitempty"`
	Reason    string                        `json:"reason,omitempty"`
	ExpiresAt *time.Time                    `json:"expires_at,omitempty"`
}

func (s *Server) userEffectiveNodes(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, err := s.store.GetUser(r.Context(), userID); err != nil {
		fail(w, err, 404)
		return
	}
	data, err := s.loadPlanAssignmentData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
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
		fail(w, err, 500)
		return
	}
	nameByKey := map[string]string{}
	for _, node := range nodes {
		nameByKey[node.Key] = node.Name
	}
	var binding *model.UserPlanBinding
	for i := range data.bindings {
		if data.bindings[i].UserID == userID {
			binding = &data.bindings[i]
			break
		}
	}
	var plan *model.SubscriptionPlan
	var planNodes []model.SubscriptionPlanNode
	if binding != nil {
		plan = data.planByID(binding.PlanID)
		for _, pn := range data.planNodes {
			if pn.PlanID == binding.PlanID && pn.Enabled {
				planNodes = append(planNodes, pn)
			}
		}
	}
	exceptions := []model.UserNodeException{}
	for _, ex := range data.exceptions {
		if ex.UserID == userID {
			exceptions = append(exceptions, ex)
		}
	}
	sources := core.UserEffectiveNodeSources(binding, plan, planNodes, exceptions, data.now())
	views := make([]userEffectiveNodeView, 0, len(sources))
	for _, source := range sources {
		view := userEffectiveNodeView{Key: source.Key, NodeType: source.NodeType, NodeID: source.NodeID, Name: nameByKey[source.Key], Source: source.Source}
		if source.Source == "plan" && binding != nil {
			view.PlanID = binding.PlanID
			view.PlanName = planNameOrEmpty(data.planByID(binding.PlanID))
		}
		view.Effect = source.Effect
		view.Reason = source.Reason
		view.ExpiresAt = source.ExpiresAt
		views = append(views, view)
	}
	write(w, 200, map[string]any{"user_id": userID, "nodes": views})
}

func planNameOrEmpty(plan *model.SubscriptionPlan) string {
	if plan == nil {
		return ""
	}
	return plan.Name
}
