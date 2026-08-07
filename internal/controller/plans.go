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
// are computed from. The effective access snapshot is the single resolution
// entry for user-node relations; the legacy authorization tables stay inputs to
// shadow comparison and to legacy runtime mode only.
type planAssignmentData struct {
	users        []model.User
	bindings     []model.UserPlanBinding
	plans        []model.SubscriptionPlan
	planNodes    []model.SubscriptionPlanNode
	exceptions   []model.UserNodeException
	config       store.FullRoutingConfig
	serverOnline map[int64]bool
	snapshot     *core.EffectiveAccessSnapshot
}

func (s *Server) loadPlanAssignmentData(ctx context.Context) (*planAssignmentData, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListEffectiveUserPlanBindings(ctx, time.Now())
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
	data := &planAssignmentData{
		users:        users,
		bindings:     bindings,
		plans:        plans,
		planNodes:    planNodes,
		exceptions:   exceptions,
		config:       config,
		serverOnline: serverOnline,
	}
	data.snapshot = core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{
		Users:             users,
		Bindings:          bindings,
		Plans:             plans,
		PlanNodes:         planNodes,
		Exceptions:        exceptions,
		Paths:             config.ProxyPaths,
		Steps:             config.ProxyPathSteps,
		Inbounds:          config.Inbounds,
		ExternalOutbounds: config.ExternalOutbounds,
		Now:               time.Now(),
	})
	return data, nil
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
		if pn.PlanID == planID {
			out[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
		}
	}
	return out
}

// planMembership maps a node key to the active plans that directly contain it.
func (d *planAssignmentData) planMembership() map[string][]model.SubscriptionPlanNode {
	out := map[string][]model.SubscriptionPlanNode{}
	for _, pn := range d.planNodes {
		key := core.NodeKeyOf(pn.NodeType, pn.NodeID)
		out[key] = append(out[key], pn)
	}
	return out
}

// effectiveUsersByNode returns, per node key, the active users that can
// actually use the node from the effective access snapshot, plus raw
// allow/deny exception counts.
func (d *planAssignmentData) effectiveUsersByNode() (map[string][]int64, map[string]int, map[string]int) {
	allowCount := map[string]int{}
	denyCount := map[string]int{}
	for _, ex := range d.exceptions {
		if !ex.ExpiresAt.After(d.now()) {
			continue
		}
		key := core.NodeKeyOf(ex.NodeType, ex.NodeID)
		if ex.Effect == model.UserNodeExceptionAllow {
			allowCount[key]++
		} else {
			denyCount[key]++
		}
	}
	return d.snapshot.NodeUsers, allowCount, denyCount
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
	DisplayGroup string `json:"display_group,omitempty"`
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
	byNode, allowCount, denyCount := data.effectiveUsersByNode()
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
	write(w, 200, map[string]any{"nodes": filtered[start:end], "total": total, "page": page, "page_size": pageSize, "runtime_authorization_mode": s.authorizationMode(r.Context())})
}

type assignableNodeUserView struct {
	UserID    int64      `json:"user_id"`
	Username  string     `json:"username"`
	Nickname  string     `json:"nickname,omitempty"`
	Effective bool       `json:"effective"`
	Source    string     `json:"source,omitempty"` // plan | exception_allow | exception_deny | excluded
	PlanID    int64      `json:"plan_id,omitempty"`
	PlanName  string     `json:"plan_name,omitempty"`
	Effect    string     `json:"effect,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (s *Server) assignableNodeDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/assignable-nodes/")
	if len(parts) < 2 {
		fail(w, errors.New("expected /api/v1/assignable-nodes/:node_type/:node_id"), 404)
		return
	}
	nodeType := model.AssignableNodeType(parts[0])
	nodeID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || nodeID <= 0 {
		fail(w, errors.New("invalid node id"), 400)
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
	key := core.NodeKeyOf(nodeType, nodeID)
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
	planViews := []assignableNodePlanView{}
	for _, pn := range data.planMembership()[key] {
		planViews = append(planViews, assignableNodePlanView{PlanID: pn.PlanID, Name: planByID[pn.PlanID].Name, DisplayGroup: pn.DisplayGroup})
	}
	sort.Slice(planViews, func(i, j int) bool { return planViews[i].PlanID < planViews[j].PlanID })

	userIDs := data.snapshot.NodeUsers[key]
	userByID := map[int64]model.User{}
	for _, user := range data.users {
		userByID[user.ID] = user
	}
	users := make([]assignableNodeUserView, 0, len(userIDs)+len(data.exceptions))
	seen := map[int64]bool{}
	for _, userID := range userIDs {
		user, ok := userByID[userID]
		if !ok {
			continue
		}
		seen[userID] = true
		view := assignableNodeUserView{UserID: userID, Username: user.Username, Nickname: user.Nickname, Effective: true, Source: "plan"}
		if grant, ok := data.snapshot.UserNodes[userID][key]; ok {
			if grant.Source == "exception_allow" {
				view.Source = "exception_allow"
				view.Effect = string(model.UserNodeExceptionAllow)
				view.Reason = grant.Exception.Reason
				expiry := grant.Exception.ExpiresAt
				view.ExpiresAt = &expiry
			} else {
				view.PlanID = grant.PlanID
				view.PlanName = grant.PlanName
			}
		}
		users = append(users, view)
	}
	for _, ex := range data.exceptions {
		if ex.NodeType != nodeType || ex.NodeID != nodeID || !ex.ExpiresAt.After(now) {
			continue
		}
		user, ok := userByID[ex.UserID]
		if !ok || user.Status != "active" {
			continue
		}
		if seen[ex.UserID] {
			continue
		}
		view := assignableNodeUserView{UserID: ex.UserID, Username: user.Username, Nickname: user.Nickname, Effective: false, Source: "exception_deny", Effect: string(ex.Effect), Reason: ex.Reason}
		expiry := ex.ExpiresAt
		view.ExpiresAt = &expiry
		users = append(users, view)
	}
	sort.SliceStable(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })

	activeExceptions := []model.UserNodeException{}
	for _, ex := range data.exceptions {
		if ex.NodeType == nodeType && ex.NodeID == nodeID && ex.ExpiresAt.After(now) {
			activeExceptions = append(activeExceptions, ex)
		}
	}
	sort.Slice(activeExceptions, func(i, j int) bool { return activeExceptions[i].ID < activeExceptions[j].ID })
	write(w, 200, map[string]any{"node": node, "plans": planViews, "users": users, "exceptions": activeExceptions, "runtime_authorization_mode": s.authorizationMode(r.Context())})
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
			s.subscriptionPlanDetail(w, r, id)
			return
		}
		s.subscriptionPlanList(w, r)
	case http.MethodPost:
		s.subscriptionPlanCreate(w, r)
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		s.subscriptionPlanPatch(w, r, id)
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		s.subscriptionPlanDelete(w, r, id)
	default:
		method(w)
	}
}

func (s *Server) subscriptionPlanList(w http.ResponseWriter, r *http.Request) {
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
			"active_revision_id":  plan.ActiveRevisionID,
			"draft_revision_id":   plan.DraftRevisionID,
			"has_draft":           plan.DraftRevisionID != 0,
			"node_count":          nodeCount[plan.ID],
			"member_count":        memberCount[plan.ID],
			"created_at":          plan.CreatedAt,
			"updated_at":          plan.UpdatedAt,
		})
	}
	write(w, 200, map[string]any{"subscription_plans": views, "runtime_authorization_mode": s.authorizationMode(r.Context())})
}

func (s *Server) subscriptionPlanDetail(w http.ResponseWriter, r *http.Request, id int64) {
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	activeNodes, err := s.store.ListActivePlanNodes(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	draftNodes, err := s.store.ListDraftPlanNodes(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	revisions, err := s.store.ListPlanRevisions(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	members, err := s.store.ListUserPlanBindingsForPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{
		"subscription_plan":          plan,
		"nodes":                      activeNodes,
		"draft_nodes":                draftNodes,
		"revisions":                  revisions,
		"member_count":               len(members),
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

type planNodeRequest struct {
	NodeType     model.AssignableNodeType `json:"node_type"`
	NodeID       int64                    `json:"node_id"`
	DisplayGroup string                   `json:"display_group"`
}

func (s *Server) subscriptionPlanCreate(w http.ResponseWriter, r *http.Request) {
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
	req.ActiveRevisionID = 0
	req.DraftRevisionID = 0
	nodes, err := s.validatePlanNodeRequests(r.Context(), req.Nodes, "", false)
	if err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.store.CreateSubscriptionPlan(r.Context(), &req.SubscriptionPlan, nodes); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "create", "subscription-plan", fmt.Sprint(req.ID))
	write(w, 201, map[string]any{"subscription_plan": req.SubscriptionPlan})
}

type planPatchRequest struct {
	ExpectedRevision  int64   `json:"expected_revision"`
	Name              *string `json:"name"`
	Description       *string `json:"description"`
	Enabled           *bool   `json:"enabled"`
	SpeedLimitMbps    *int    `json:"speed_limit_mbps"`
	TrafficLimitBytes *int64  `json:"traffic_limit_bytes"`
	TrafficResetMode  *string `json:"traffic_reset_mode"`
	TrafficResetDay   *int    `json:"traffic_reset_day"`
}

func (s *Server) subscriptionPlanPatch(w http.ResponseWriter, r *http.Request, id int64) {
	var req planPatchRequest
	if !decode(w, r, &req) {
		return
	}
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	expected := req.ExpectedRevision
	if expected == 0 {
		expected = plan.Revision
	}
	name, description := plan.Name, plan.Description
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	meta := model.SubscriptionPlan{Name: name, Description: description, Enabled: plan.Enabled}
	if req.Enabled != nil {
		meta.Enabled = *req.Enabled
	}
	if err := validateSubscriptionPlanFields(&meta); err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.store.UpdateSubscriptionPlanMeta(r.Context(), id, expected, meta.Name, meta.Description, req.Enabled); err != nil {
		fail(w, err, planWriteStatus(err))
		return
	}
	if req.SpeedLimitMbps != nil || req.TrafficLimitBytes != nil || req.TrafficResetMode != nil || req.TrafficResetDay != nil {
		speed, traffic, mode, day := plan.SpeedLimitMbps, plan.TrafficLimitBytes, plan.TrafficResetMode, plan.TrafficResetDay
		if req.SpeedLimitMbps != nil {
			speed = *req.SpeedLimitMbps
		}
		if req.TrafficLimitBytes != nil {
			traffic = *req.TrafficLimitBytes
		}
		if req.TrafficResetMode != nil {
			mode = normalizeControllerTrafficResetMode(*req.TrafficResetMode)
		}
		if req.TrafficResetDay != nil {
			day = normalizeControllerTrafficResetDay(*req.TrafficResetDay)
		}
		if speed < 0 || traffic < 0 {
			fail(w, errors.New("limits must be >= 0"), 400)
			return
		}
		if _, err := s.store.UpdatePlanDraftLimits(r.Context(), id, 0, speed, traffic, mode, day); err != nil {
			fail(w, err, planWriteStatus(err))
			return
		}
	}
	updated, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "patch", "subscription-plan", fmt.Sprint(id))
	write(w, 200, map[string]any{"subscription_plan": updated})
}

func (s *Server) subscriptionPlanDelete(w http.ResponseWriter, r *http.Request, id int64) {
	members, err := s.store.ListUserPlanBindingsForPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	if len(members) > 0 {
		fail(w, fmt.Errorf("subscription plan has %d bound users; disable and migrate them before deletion", len(members)), http.StatusConflict)
		return
	}
	if err := s.store.DeleteSubscriptionPlan(r.Context(), id); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "delete", "subscription-plan", fmt.Sprint(id))
	write(w, 200, map[string]any{"deleted": true})
}

func (s *Server) subscriptionPlanSubroutes(w http.ResponseWriter, r *http.Request, id int64, parts []string) {
	if len(parts) < 1 || len(parts) > 3 {
		fail(w, errors.New("unknown subscription plan subroute"), 404)
		return
	}
	switch parts[0] {
	case "changes":
		if len(parts) != 2 {
			fail(w, errors.New("unknown subscription plan subroute"), 404)
			return
		}
		switch parts[1] {
		case "preview":
			if r.Method != http.MethodPost {
				method(w)
				return
			}
			s.planChangePreviewHandler(w, r, id)
		case "apply":
			if r.Method != http.MethodPost {
				method(w)
				return
			}
			s.planChangeApplyHandler(w, r, id)
		default:
			fail(w, errors.New("unknown subscription plan subroute"), 404)
		}
	case "disable":
		if len(parts) != 1 || r.Method != http.MethodPost {
			method(w)
			return
		}
		s.planDisable(w, r, id)
	case "nodes":
		if len(parts) != 2 {
			fail(w, errors.New("unknown subscription plan subroute"), 404)
			return
		}
		switch parts[1] {
		case "preview":
			s.planNodesPreview(w, r, id)
		case "sync":
			s.planNodesSync(w, r, id)
		default:
			fail(w, errors.New("unknown subscription plan subroute"), 404)
		}
	case "publish":
		if len(parts) != 1 || r.Method != http.MethodPost {
			method(w)
			return
		}
		s.planPublish(w, r, id)
	case "clone":
		if len(parts) != 1 || r.Method != http.MethodPost {
			method(w)
			return
		}
		s.planClone(w, r, id)
	case "revisions":
		s.planRevisions(w, r, id, parts[1:])
	default:
		fail(w, errors.New("unknown subscription plan subroute"), 404)
	}
}

func planWriteStatus(err error) int {
	if errors.Is(err, store.ErrPlanRevisionConflict) {
		return http.StatusConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func (s *Server) planPublish(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	_ = decode(w, r, &req)
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	expected := req.ExpectedRevision
	if expected == 0 {
		expected = plan.Revision
	}
	if plan.Revision != expected {
		fail(w, store.ErrPlanRevisionConflict, http.StatusConflict)
		return
	}
	if plan.DraftRevisionID == 0 {
		fail(w, errors.New("subscription plan has no draft revision to publish"), 400)
		return
	}
	change, err := s.createPlanPublishChange(r.Context(), r, plan, plan.DraftRevisionID)
	if err != nil {
		fail(w, err, planWriteStatus(err))
		return
	}
	auditReq(s, r, "publish", "access-change", fmt.Sprintf("plan=%d change=%d", id, change.ID))
	write(w, 200, map[string]any{"published": false, "access_change_id": change.ID, "status": change.Status, "runtime_authorization_mode": s.authorizationMode(r.Context())})
}

func (s *Server) planClone(w http.ResponseWriter, r *http.Request, id int64) {
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	newName := strings.TrimSpace(plan.Name) + " 副本"
	clone, err := s.store.CloneSubscriptionPlan(r.Context(), id, newName)
	if err != nil {
		fail(w, err, planWriteStatus(err))
		return
	}
	auditReq(s, r, "clone", "subscription-plan", fmt.Sprintf("%d->%d", id, clone.ID))
	write(w, 201, map[string]any{"subscription_plan": clone})
}

func (s *Server) planRevisions(w http.ResponseWriter, r *http.Request, id int64, parts []string) {
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		revisions, err := s.store.ListPlanRevisions(r.Context(), id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"revisions": revisions})
		return
	}
	revisionID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || revisionID <= 0 {
		fail(w, errors.New("invalid revision id"), 400)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		revision, err := s.store.GetPlanRevision(r.Context(), id, revisionID)
		if err != nil {
			fail(w, err, 404)
			return
		}
		nodes, err := s.store.ListPlanRevisionNodes(r.Context(), revisionID)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"revision": revision, "nodes": nodes})
		return
	}
	if len(parts) == 2 && parts[1] == "restore" && r.Method == http.MethodPost {
		var req struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		_ = decode(w, r, &req)
		plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		expected := req.ExpectedRevision
		if expected == 0 {
			expected = plan.Revision
		}
		draftID, err := s.store.RestorePlanRevision(r.Context(), id, revisionID, expected)
		if err != nil {
			fail(w, err, planWriteStatus(err))
			return
		}
		change, err := s.createPlanPublishChange(r.Context(), r, plan, draftID)
		if err != nil {
			fail(w, err, planWriteStatus(err))
			return
		}
		auditReq(s, r, "restore", "access-change", fmt.Sprintf("plan=%d revision=%d change=%d", id, revisionID, change.ID))
		write(w, 200, map[string]any{"restored": true, "draft_revision_id": draftID, "access_change_id": change.ID, "access_change_status": change.Status, "runtime_authorization_mode": s.authorizationMode(r.Context())})
		return
	}
	fail(w, errors.New("unknown subscription plan subroute"), 404)
}

type planNodesSyncRequest struct {
	Op               string            `json:"op"` // add | remove | replace
	Nodes            []planNodeRequest `json:"nodes"`
	DisplayGroup     string            `json:"display_group,omitempty"`
	ExpectedRevision int64             `json:"expected_revision"`
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
	out, err := s.computePlanNodesChange(r.Context(), id, req)
	if err != nil {
		fail(w, err, planWriteStatus(err))
		return
	}
	write(w, 200, out)
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
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	expected := req.ExpectedRevision
	if expected == 0 {
		expected = plan.Revision
	}
	nodes, err := s.validatePlanNodeRequests(r.Context(), req.Nodes, req.DisplayGroup, req.Op == "remove")
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if err := s.store.SyncPlanDraftNodes(r.Context(), id, expected, nodes, req.Op); err != nil {
		fail(w, err, planWriteStatus(err))
		return
	}
	auditReq(s, r, req.Op, "subscription-plan-nodes", fmt.Sprintf("%d:%d", id, len(nodes)))
	updated, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"subscription_plan": updated, "revision": updated.Revision})
}

// computePlanNodesChange previews one draft node edit: the diff against the
// current draft (or active when no draft exists), the active-vs-draft diff,
// affected authentication servers, and the expected revision for optimistic
// concurrency.
func (s *Server) computePlanNodesChange(ctx context.Context, id int64, req planNodesSyncRequest) (map[string]any, error) {
	nodes, err := s.validatePlanNodeRequests(ctx, req.Nodes, req.DisplayGroup, req.Op == "remove")
	if err != nil {
		return nil, err
	}
	data, err := s.loadPlanAssignmentData(ctx)
	if err != nil {
		return nil, err
	}
	plan := data.planByID(id)
	if plan == nil {
		return nil, sql.ErrNoRows
	}
	activeNodes, err := s.store.ListActivePlanNodes(ctx, id)
	if err != nil {
		return nil, err
	}
	draftNodes, err := s.store.ListDraftPlanNodes(ctx, id)
	if err != nil {
		return nil, err
	}
	currentNodes := draftNodes
	if len(currentNodes) == 0 {
		currentNodes = activeNodes
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
		return nil, errors.New("op must be add, remove, or replace")
	}
	preview := core.PreviewPlanNodeChange(data.users, data.bindings, data.plans, data.planNodes, data.exceptions, id, targetNodes, data.config.ProxyPaths, data.config.ProxyPathSteps, data.config.Inbounds, data.serverOnline, data.now())
	out := map[string]any{
		"preview":           preview,
		"expected_revision": plan.Revision,
		"draft_node_count":  len(targetNodes),
	}
	if len(draftNodes) > 0 {
		added, removed, unchanged := nodeSetDiff(activeNodes, draftNodes)
		out["active_vs_draft"] = map[string]any{"nodes_added": added, "nodes_removed": removed, "nodes_unchanged": unchanged}
	}
	return out, nil
}

func nodeSetDiff(from, to []model.SubscriptionPlanNode) ([]string, []string, int) {
	fromKeys := map[string]bool{}
	toKeys := map[string]bool{}
	for _, pn := range from {
		fromKeys[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
	}
	for _, pn := range to {
		toKeys[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
	}
	added := []string{}
	removed := []string{}
	unchanged := 0
	for key := range toKeys {
		if fromKeys[key] {
			unchanged++
		} else {
			added = append(added, key)
		}
	}
	for key := range fromKeys {
		if !toKeys[key] {
			removed = append(removed, key)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, unchanged
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
	UserIDs   []int64 `json:"user_ids"`
	PlanID    int64   `json:"plan_id"`
	Deploy    bool    `json:"deploy"`
	StartsAt  *string `json:"starts_at"`
	ExpiresAt *string `json:"expires_at"`
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

func (s *Server) parseAssignmentTime(raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(*raw))
	if err != nil {
		return nil, errors.New("starts_at/expires_at must use RFC3339")
	}
	return &t, nil
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
	write(w, 200, map[string]any{"preview": preview, "runtime_authorization_mode": s.authorizationMode(r.Context())})
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
	startsAt, err := s.parseAssignmentTime(req.StartsAt)
	if err != nil {
		fail(w, err, 400)
		return
	}
	expiresAt, err := s.parseAssignmentTime(req.ExpiresAt)
	if err != nil {
		fail(w, err, 400)
		return
	}
	if expiresAt != nil && startsAt != nil && !expiresAt.After(*startsAt) {
		fail(w, errors.New("expires_at must be after starts_at"), 400)
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
		bindings = append(bindings, model.UserPlanBinding{UserID: userID, PlanID: req.PlanID, AssignedBy: assignedBy, StartsAt: startsAt, ExpiresAt: expiresAt})
	}
	// Two-phase assignment: the new bindings are stored pending so the plan
	// snapshot keeps ignoring them until the access change activation flips
	// them active. Prepare deploys old-union-new credentials first.
	if err := s.store.SetUserPlanBindingsPending(r.Context(), bindings); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "assign", "user-plan", fmt.Sprintf("users=%d plan=%d", len(req.UserIDs), req.PlanID))
	userIDs := make([]int64, 0, len(req.UserIDs))
	for _, userID := range req.UserIDs {
		userIDs = append(userIDs, userID)
	}
	change, err := s.createUserBindingChange(r.Context(), r, data.config, userIDs, bindings, startsAt, expiresAt)
	if err != nil {
		fail(w, err, 500)
		return
	}
	out := map[string]any{"applied": true, "affected_users": len(selected), "access_change_id": change.ID, "status": change.Status, "queued_tasks": len(change.Targets), "runtime_authorization_mode": s.authorizationMode(r.Context())}
	if startsAt != nil && startsAt.After(time.Now()) {
		out["status"] = "scheduled"
		out["activate_at"] = startsAt
	}
	write(w, 200, out)
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
	nodes, err := s.store.ListActivePlanNodes(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	return plan, nodes, nil
}

// queueAccessSyncForServers queues at most one apply_core_config per affected
// authentication server with a shared configuration version. Config generation
// reads the effective plan snapshot in plan mode, so this is the access-only
// sync described by the assignment plan; in legacy mode callers skip it.
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
			write(w, 200, map[string]any{"user_node_exception": item, "runtime_authorization_mode": s.authorizationMode(r.Context())})
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
		write(w, 200, map[string]any{"user_node_exceptions": items, "runtime_authorization_mode": s.authorizationMode(r.Context())})
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
		if v.Effect == model.UserNodeExceptionAllow {
			v.Status = model.UserNodeExceptionPending
		}
		if v.Effect == model.UserNodeExceptionDeny {
			v.Status = model.UserNodeExceptionActive
		}
		before, err := s.store.ListUserNodeExceptions(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.CreateUserNodeException(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "user-node-exception", fmt.Sprintf("%d:%s:%d", v.UserID, v.NodeType, v.NodeID))
		out := map[string]any{"user_node_exception": v, "runtime_authorization_mode": s.authorizationMode(r.Context())}
		change, err := s.exceptionChangeAfterWrite(r.Context(), r, before, v)
		if err != nil {
			fail(w, err, 500)
			return
		}
		out["access_change_id"] = change.ID
		out["access_change_status"] = change.Status
		write(w, 201, out)
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
		if v.Effect == model.UserNodeExceptionAllow {
			v.Status = model.UserNodeExceptionPending
		}
		if v.Effect == model.UserNodeExceptionDeny {
			v.Status = model.UserNodeExceptionActive
		}
		if err := s.store.UpdateUserNodeException(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "patch", "user-node-exception", fmt.Sprint(id))
		out := map[string]any{"user_node_exception": v, "runtime_authorization_mode": s.authorizationMode(r.Context())}
		change, err := s.exceptionChangeAfterWrite(r.Context(), r, items, v)
		if err != nil {
			fail(w, err, 500)
			return
		}
		out["access_change_id"] = change.ID
		out["access_change_status"] = change.Status
		write(w, 200, out)
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		// Two-phase revocation: the row is kept and flipped to revoked at
		// activation so the audit trail survives; finalize prunes the
		// credentials.
		items, err := s.store.ListUserNodeExceptions(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		var current *model.UserNodeException
		for i := range items {
			if items[i].ID == id {
				current = &items[i]
				break
			}
		}
		if current == nil {
			fail(w, sql.ErrNoRows, 404)
			return
		}
		data, err := s.store.FullRoutingConfigData(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		change, err := s.createExceptionChange(r.Context(), r, data, items, exceptionsWithout(items, id), *current, model.UserNodeExceptionRevoked)
		if err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "user-node-exception", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": false, "revoking": true, "access_change_id": change.ID, "access_change_status": change.Status, "runtime_authorization_mode": s.authorizationMode(r.Context())})
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
	views := []userEffectiveNodeView{}
	grants := data.snapshot.UserNodes[userID]
	keys := make([]string, 0, len(grants))
	for key := range grants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		grant := grants[key]
		view := userEffectiveNodeView{Key: key, NodeType: grant.NodeType, NodeID: grant.NodeID, Name: nameByKey[key], Source: grant.Source}
		if grant.Source == "plan" {
			view.PlanID = grant.PlanID
			view.PlanName = grant.PlanName
		} else if grant.Exception != nil {
			view.Effect = grant.Exception.Effect
			view.Reason = grant.Exception.Reason
			expiry := grant.Exception.ExpiresAt
			view.ExpiresAt = &expiry
		}
		views = append(views, view)
	}
	write(w, 200, map[string]any{"user_id": userID, "nodes": views, "runtime_authorization_mode": s.authorizationMode(r.Context())})
}
