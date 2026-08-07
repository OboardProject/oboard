package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// ---------------------------------------------------------------------------
// Subscription plan ordering
// ---------------------------------------------------------------------------

// planOrdering serves GET/PUT /subscription-plans/:id/ordering.
func (s *Server) planOrdering(w http.ResponseWriter, r *http.Request, id int64) {
	switch r.Method {
	case http.MethodGet:
		s.planOrderingGet(w, r, id)
	case http.MethodPut:
		s.planOrderingUpdate(w, r, id)
	default:
		method(w)
	}
}

func (s *Server) planOrderingGet(w http.ResponseWriter, r *http.Request, id int64) {
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	revisionID, err := s.planOrderingRevisionID(r, plan)
	if err != nil {
		fail(w, err, 400)
		return
	}
	revision, nodes, err := s.store.GetPlanRevisionOrdering(r.Context(), id, revisionID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	config, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	ordered, err := core.BuildOrderingNodes(nodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, revision.NodeOrderPolicy)
	if err != nil {
		fail(w, err, 500)
		return
	}
	views, unplaced, warnings := s.orderingNodeViews(r.Context(), config, ordered)
	write(w, 200, map[string]any{
		"plan_id":                    plan.ID,
		"plan_revision":              plan.Revision,
		"revision_id":                revision.ID,
		"revision_status":            revision.Status,
		"editable":                   revision.Status == model.PlanRevisionDraft,
		"policy":                     revision.NodeOrderPolicy,
		"nodes":                      views,
		"unplaced_count":             unplaced,
		"warnings":                   warnings,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

// planOrderingRevisionID resolves the ?revision= query (draft | active |
// numeric revision id) to a revision id. Empty defaults to the active
// revision.
func (s *Server) planOrderingRevisionID(r *http.Request, plan *model.SubscriptionPlan) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("revision"))
	switch raw {
	case "", "active":
		if plan.ActiveRevisionID == 0 {
			return 0, errors.New("plan has no active revision")
		}
		return plan.ActiveRevisionID, nil
	case "draft":
		if plan.DraftRevisionID == 0 {
			return 0, errors.New("plan has no draft revision")
		}
		return plan.DraftRevisionID, nil
	}
	revisionID, err := parseRevisionID(raw)
	if err != nil {
		return 0, errors.New("revision must be draft, active or a revision id")
	}
	if _, err := s.store.GetPlanRevision(r.Context(), plan.ID, revisionID); err != nil {
		return 0, errors.New("revision not found")
	}
	return revisionID, nil
}

func parseRevisionID(raw string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(raw, "%d", &id); err != nil || id <= 0 {
		return 0, errors.New("invalid revision id")
	}
	return id, nil
}

type orderingNodeView struct {
	Key               string                   `json:"key"`
	NodeType          model.AssignableNodeType `json:"node_type"`
	NodeID            int64                    `json:"node_id"`
	Name              string                   `json:"name"`
	Group             string                   `json:"group"`
	EntryKey          string                   `json:"entry_key,omitempty"`
	EntryServerID     int64                    `json:"entry_server_id,omitempty"`
	EntryRegion       string                   `json:"entry_region,omitempty"`
	ExitServerID      int64                    `json:"exit_server_id,omitempty"`
	ExitRegion        string                   `json:"exit_region,omitempty"`
	ManualPosition    *int                     `json:"manual_position,omitempty"`
	EffectivePosition int                      `json:"effective_position"`
	Renderable        bool                     `json:"renderable"`
	Warning           string                   `json:"warning,omitempty"`
}

func (s *Server) orderingNodeViews(ctx context.Context, config store.FullRoutingConfig, ordered []core.SubscriptionNode) ([]orderingNodeView, int, []string) {
	renderable := renderableNodeKeys(config)
	serverByID := map[int64]string{}
	for _, server := range config.Servers {
		serverByID[server.ID] = server.Name
	}
	views := make([]orderingNodeView, 0, len(ordered))
	unplaced := 0
	warnings := []string{}
	for position, node := range ordered {
		view := orderingNodeView{
			Key:               node.Key,
			NodeType:          node.NodeType,
			NodeID:            node.NodeID,
			Name:              node.Name,
			Group:             node.Group,
			EntryKey:          node.EntryKey,
			EntryServerID:     node.EntryServerID,
			EntryRegion:       node.EntryRegion,
			ExitServerID:      node.ExitServerID,
			ExitRegion:        node.ExitRegion,
			EffectivePosition: position,
			Renderable:        renderable[node.Key],
		}
		if node.ManualPosition != nil {
			position := *node.ManualPosition
			view.ManualPosition = &position
		} else {
			unplaced++
		}
		if !view.Renderable {
			view.Warning = "当前不可渲染"
			warnings = append(warnings, fmt.Sprintf("%s 当前不可渲染", node.Name))
		} else if node.ExitRegion == "" {
			view.Warning = "出口地区未解析"
			warnings = append(warnings, fmt.Sprintf("%s 出口地区未解析", node.Name))
		} else if node.EntryKey == "" {
			view.Warning = "没有 OBoard 入口"
			warnings = append(warnings, fmt.Sprintf("%s 没有 OBoard 入口", node.Name))
		}
		views = append(views, view)
	}
	return views, unplaced, warnings
}

// renderableNodeKeys reports which node keys can actually render a
// subscription entry, mirroring the generator's own conditions without
// building raw payloads.
func renderableNodeKeys(config store.FullRoutingConfig) map[string]bool {
	out := map[string]bool{}
	serverByID := map[int64]model.Server{}
	for _, server := range config.Servers {
		serverByID[server.ID] = server
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range config.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	pathByID := map[int64]model.ProxyPath{}
	for _, path := range config.ProxyPaths {
		pathByID[path.ID] = path
	}
	externalByID := map[int64]model.ExternalOutbound{}
	for _, external := range config.ExternalOutbounds {
		externalByID[external.ID] = external
	}
	hasBranch := map[int64]bool{}
	for _, path := range config.ProxyPaths {
		hasBranch[path.InboundID] = true
	}
	for _, inbound := range config.Inbounds {
		if !inbound.Enabled || hasBranch[inbound.ID] {
			continue
		}
		server := serverByID[inbound.ServerID]
		server.EntryAddress = core.ResolveEntryAddress(inbound, server)
		if strings.TrimSpace(server.EntryAddress) != "" {
			out[core.NodeKeyOf(model.AssignableNodeInbound, inbound.ID)] = true
		}
	}
	for _, path := range config.ProxyPaths {
		if !path.Enabled {
			continue
		}
		inbound, ok := inboundByID[path.InboundID]
		if !ok || !inbound.Enabled {
			continue
		}
		server := serverByID[inbound.ServerID]
		server.EntryAddress = core.ResolveEntryAddress(inbound, server)
		if strings.TrimSpace(server.EntryAddress) != "" {
			out[core.NodeKeyOf(model.AssignableNodeProxyPath, path.ID)] = true
		}
	}
	for _, external := range config.ExternalOutbounds {
		if external.Enabled && external.ExposeToUsers {
			out[core.NodeKeyOf(model.AssignableNodeExternalOutbound, external.ID)] = true
		}
	}
	return out
}

// planOrderingUpdate serves PUT /subscription-plans/:id/ordering.
func (s *Server) planOrderingUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		ExpectedRevision int64                             `json:"expected_revision"`
		Policy           model.SubscriptionNodeOrderPolicy `json:"policy"`
		ManualNodeOrder  []string                          `json:"manual_node_order"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ExpectedRevision <= 0 {
		fail(w, errors.New("expected_revision is required"), 400)
		return
	}
	policy, err := core.ValidateSubscriptionNodeOrderPolicy(req.Policy)
	if err != nil {
		fail(w, err, 400)
		return
	}
	manualOrder := normalizeManualOrder(req.ManualNodeOrder)
	if policy.Mode == model.SubscriptionNodeOrderManual {
		seen := map[string]bool{}
		for _, key := range manualOrder {
			nodeType, nodeID, ok := core.ParseNodeKey(key)
			if !ok || nodeID <= 0 || nodeType == "" {
				fail(w, fmt.Errorf("invalid manual node key %q", key), 400)
				return
			}
			if seen[key] {
				fail(w, fmt.Errorf("duplicate manual node key %q", key), 400)
				return
			}
			seen[key] = true
		}
	}
	draftID, err := s.store.UpdatePlanDraftOrdering(r.Context(), id, req.ExpectedRevision, policy, manualOrder)
	if err != nil {
		if errors.Is(err, store.ErrPlanOrderingInvalid) {
			fail(w, err, 400)
			return
		}
		fail(w, err, planWriteStatus(err))
		return
	}
	auditReq(s, r, "ordering", "subscription-plan", fmt.Sprintf("plan=%d draft=%d mode=%s", id, draftID, policy.Mode))
	write(w, 200, map[string]any{
		"revision_id":                draftID,
		"policy":                     policy,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func normalizeManualOrder(list []string) []string {
	out := make([]string, 0, len(list))
	for _, raw := range list {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		out = append(out, key)
	}
	return out
}

// planOrderingPreview serves POST /subscription-plans/:id/ordering/preview.
// It never writes: the base node set is the draft revision when one exists,
// otherwise the active revision, and the requested policy is applied by the
// real ordering engine.
func (s *Server) planOrderingPreview(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		Policy          model.SubscriptionNodeOrderPolicy `json:"policy"`
		ManualNodeOrder []string                          `json:"manual_node_order"`
	}
	if !decode(w, r, &req) {
		return
	}
	policy, err := core.ValidateSubscriptionNodeOrderPolicy(req.Policy)
	if err != nil {
		fail(w, err, 400)
		return
	}
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	revisionID := plan.DraftRevisionID
	if revisionID == 0 {
		revisionID = plan.ActiveRevisionID
	}
	if revisionID == 0 {
		fail(w, errors.New("plan has no revision to preview"), 400)
		return
	}
	revision, nodes, err := s.store.GetPlanRevisionOrdering(r.Context(), id, revisionID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if policy.Mode == model.SubscriptionNodeOrderManual {
		positionByKey, err := manualPositionsForKeys(nodes, normalizeManualOrder(req.ManualNodeOrder))
		if err != nil {
			fail(w, err, 400)
			return
		}
		for i := range nodes {
			key := core.NodeKeyOf(nodes[i].NodeType, nodes[i].NodeID)
			if position, ok := positionByKey[key]; ok {
				position := position
				nodes[i].SortPosition = &position
			} else {
				nodes[i].SortPosition = nil
			}
		}
	}
	config, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	ordered, err := core.BuildOrderingNodes(nodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, policy)
	if err != nil {
		fail(w, err, 500)
		return
	}
	views, unplaced, warnings := s.orderingNodeViews(r.Context(), config, ordered)
	write(w, 200, map[string]any{
		"plan_id":                    plan.ID,
		"revision_id":                revision.ID,
		"revision_status":            revision.Status,
		"policy":                     policy,
		"nodes":                      views,
		"unplaced_count":             unplaced,
		"warnings":                   warnings,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func manualPositionsForKeys(nodes []model.SubscriptionPlanNode, manualOrder []string) (map[string]int, error) {
	byKey := map[string]bool{}
	for _, node := range nodes {
		byKey[core.NodeKeyOf(node.NodeType, node.NodeID)] = true
	}
	out := map[string]int{}
	seen := map[string]bool{}
	for position, key := range manualOrder {
		if seen[key] {
			return nil, fmt.Errorf("duplicate manual node key %q", key)
		}
		seen[key] = true
		if !byKey[key] {
			return nil, fmt.Errorf("manual node order contains %s which is not in the revision node set", key)
		}
		out[key] = position
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Node scope preview
// ---------------------------------------------------------------------------

type nodeScopeKind string

const (
	nodeScopeNode             nodeScopeKind = "node"
	nodeScopeEntryInbound     nodeScopeKind = "entry_inbound"
	nodeScopeEntryServer      nodeScopeKind = "entry_server"
	nodeScopePathServer       nodeScopeKind = "path_server"
	nodeScopeExitServer       nodeScopeKind = "exit_server"
	nodeScopeExitRegion       nodeScopeKind = "exit_region"
	nodeScopeExternalOutbound nodeScopeKind = "external_outbound"
)

type nodeScopeRequest struct {
	AnchorNodeKey string `json:"anchor_node_key"`
	Scope         struct {
		Kind               string `json:"kind"`
		ServerID           int64  `json:"server_id,omitempty"`
		Region             string `json:"region,omitempty"`
		ExternalOutboundID int64  `json:"external_outbound_id,omitempty"`
	} `json:"scope"`
	IncludeDisabled bool `json:"include_disabled"`
}

// assignableNodeScopePreview serves POST /api/v1/assignable-node-scopes/preview.
// Scope resolution is authoritative in the backend: the frontend only sends the
// anchor and the scope kind and receives the complete matching node set.
func (s *Server) assignableNodeScopePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req nodeScopeRequest
	if !decode(w, r, &req) {
		return
	}
	config, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	nodes, err := core.BuildAssignableNodeCatalog(core.AssignableNodeCatalogInput{
		Servers:           config.Servers,
		Inbounds:          config.Inbounds,
		ProxyPaths:        config.ProxyPaths,
		ProxyPathSteps:    config.ProxyPathSteps,
		EgressResults:     config.ProxyPathEgressResults,
		ExternalOutbounds: config.ExternalOutbounds,
		ServerOnline:      serverOnlineMap(config),
	})
	if err != nil {
		fail(w, err, 500)
		return
	}
	byKey := map[string]core.AssignableNode{}
	for _, node := range nodes {
		byKey[node.Key] = node
	}
	anchor, ok := byKey[req.AnchorNodeKey]
	if !ok {
		fail(w, fmt.Errorf("anchor node %q is not assignable", req.AnchorNodeKey), 400)
		return
	}
	kind := nodeScopeKind(strings.TrimSpace(req.Scope.Kind))
	var serverID int64
	switch kind {
	case nodeScopeNode:
		if anchor.EntryKey == "" {
			fail(w, errors.New("当前节点没有 OBoard 入口，无法按入口选择"), 400)
			return
		}
	case nodeScopeEntryInbound:
		if anchor.EntryKey == "" {
			fail(w, errors.New("当前节点没有 OBoard 入口，无法按入口选择"), 400)
			return
		}
	case nodeScopeEntryServer:
		if anchor.EntryServerID == 0 {
			fail(w, errors.New("当前节点没有受管入口服务器，无法按入口服务器选择"), 400)
			return
		}
		serverID = anchor.EntryServerID
	case nodeScopePathServer:
		serverID = req.Scope.ServerID
		if serverID == 0 {
			if len(anchor.PathServers) == 1 {
				serverID = anchor.PathServers[0].ServerID
			} else {
				fail(w, errors.New("当前节点涉及多个路径服务器，请指定 server_id"), 400)
				return
			}
		}
	case nodeScopeExitServer:
		if anchor.ExitServerID == 0 {
			fail(w, errors.New("当前节点无法确定受管出口服务器"), 400)
			return
		}
		serverID = anchor.ExitServerID
	case nodeScopeExitRegion:
		if core.NormalizeRegionCode(anchor.ExitRegion) == "" {
			fail(w, errors.New("当前节点出口地区未解析，无法按出口地区选择"), 400)
			return
		}
	case nodeScopeExternalOutbound:
		if anchor.ExitExternalOutboundID == 0 {
			fail(w, errors.New("当前节点不使用导入出口，无法按导入出口选择"), 400)
			return
		}
	default:
		fail(w, fmt.Errorf("invalid scope kind %q", req.Scope.Kind), 400)
		return
	}
	serverName := ""
	if serverID != 0 {
		for _, server := range config.Servers {
			if server.ID == serverID {
				serverName = server.Name
				break
			}
		}
	}
	matched := []core.AssignableNode{}
	for _, node := range nodes {
		if !req.IncludeDisabled && !node.Enabled {
			continue
		}
		switch kind {
		case nodeScopeNode:
			if node.Key == anchor.Key {
				matched = append(matched, node)
			}
		case nodeScopeEntryInbound:
			if node.EntryKey != "" && node.EntryKey == anchor.EntryKey {
				matched = append(matched, node)
			}
		case nodeScopeEntryServer:
			if node.EntryServerID != 0 && node.EntryServerID == serverID {
				matched = append(matched, node)
			}
		case nodeScopePathServer:
			for _, role := range node.PathServers {
				if role.ServerID == serverID {
					matched = append(matched, node)
					break
				}
			}
		case nodeScopeExitServer:
			if node.ExitServerID != 0 && node.ExitServerID == serverID {
				matched = append(matched, node)
			}
		case nodeScopeExitRegion:
			if core.NormalizeRegionCode(node.ExitRegion) == core.NormalizeRegionCode(anchor.ExitRegion) && core.NormalizeRegionCode(node.ExitRegion) != "" {
				matched = append(matched, node)
			}
		case nodeScopeExternalOutbound:
			if node.ExitExternalOutboundID != 0 && node.ExitExternalOutboundID == anchor.ExitExternalOutboundID {
				matched = append(matched, node)
			}
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Key < matched[j].Key })
	nodeRefs := make([]map[string]any, 0, len(matched))
	sampleNodes := make([]map[string]any, 0, 5)
	for _, node := range matched {
		nodeRefs = append(nodeRefs, map[string]any{"node_type": node.Type, "node_id": node.ID})
		if len(sampleNodes) < 5 {
			sampleNodes = append(sampleNodes, map[string]any{"key": node.Key, "name": node.Name})
		}
	}
	hash := nodeScopeSelectionHash(nodeRefs)
	warnings := []string{}
	if !anchor.Renderable {
		warnings = append(warnings, "当前节点不可渲染")
	}
	write(w, 200, map[string]any{
		"scope": map[string]any{
			"kind":        kind,
			"server_id":   serverID,
			"server_name": serverName,
			"region":      anchor.ExitRegion,
		},
		"count":                      len(nodeRefs),
		"node_refs":                  nodeRefs,
		"sample_nodes":               sampleNodes,
		"warnings":                   warnings,
		"selection_hash":             hash,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func nodeScopeSelectionHash(nodeRefs []map[string]any) string {
	raw, err := json.Marshal(nodeRefs)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func serverOnlineMap(config store.FullRoutingConfig) map[int64]bool {
	out := make(map[int64]bool, len(config.Servers))
	for _, server := range config.Servers {
		out[server.ID] = server.Status == model.ServerOnline
	}
	return out
}

// ---------------------------------------------------------------------------
// Batch user node exceptions
// ---------------------------------------------------------------------------

type batchUserExceptionRequest struct {
	UserIDs []int64 `json:"user_ids"`
	Nodes   []struct {
		NodeType model.AssignableNodeType `json:"node_type"`
		NodeID   int64                    `json:"node_id"`
	} `json:"nodes"`
	Effect    model.UserNodeExceptionEffect `json:"effect"`
	Reason    string                        `json:"reason"`
	StartsAt  *time.Time                    `json:"starts_at"`
	ExpiresAt time.Time                     `json:"expires_at"`
}

type batchExceptionOutcome struct {
	Created []model.UserNodeException `json:"created"`
	Updated []model.UserNodeException `json:"updated"`
	Skipped []string                  `json:"skipped"`
}

// planBatchUserExceptions classifies the requested (user, node) pairs against
// the existing rows. Same-effect rows are skipped; opposite-effect rows are
// updated in place (the unique index allows one exception per user+node).
func (s *Server) planBatchUserExceptions(ctx context.Context, req batchUserExceptionRequest, existing []model.UserNodeException) (batchExceptionOutcome, error) {
	out := batchExceptionOutcome{}
	byKey := map[string]model.UserNodeException{}
	for _, row := range existing {
		byKey[exceptionKey(row.UserID, row.NodeType, row.NodeID)] = row
	}
	seenPairs := map[string]bool{}
	for _, userID := range req.UserIDs {
		if userID <= 0 {
			return out, errors.New("invalid user_id")
		}
		for _, node := range req.Nodes {
			if node.NodeType == "" || node.NodeID <= 0 {
				return out, errors.New("invalid node")
			}
			key := exceptionKey(userID, node.NodeType, node.NodeID)
			if seenPairs[key] {
				continue
			}
			seenPairs[key] = true
			row, exists := byKey[key]
			if exists && row.Effect == req.Effect {
				out.Skipped = append(out.Skipped, key)
				continue
			}
			status := model.UserNodeExceptionActive
			if req.Effect == model.UserNodeExceptionAllow {
				status = model.UserNodeExceptionPending
			}
			item := model.UserNodeException{
				ID:        row.ID,
				UserID:    userID,
				NodeType:  node.NodeType,
				NodeID:    node.NodeID,
				Effect:    req.Effect,
				Reason:    req.Reason,
				Status:    status,
				StartsAt:  req.StartsAt,
				ExpiresAt: req.ExpiresAt,
			}
			if exists {
				out.Updated = append(out.Updated, item)
			} else {
				out.Created = append(out.Created, item)
			}
		}
	}
	return out, nil
}

func exceptionKey(userID int64, nodeType model.AssignableNodeType, nodeID int64) string {
	return fmt.Sprintf("%d:%s:%d", userID, nodeType, nodeID)
}

func (s *Server) validateBatchUserExceptionRequest(ctx context.Context, req *batchUserExceptionRequest) error {
	if len(req.UserIDs) == 0 {
		return errors.New("user_ids required")
	}
	if len(req.Nodes) == 0 {
		return errors.New("nodes required")
	}
	if req.Effect != model.UserNodeExceptionAllow && req.Effect != model.UserNodeExceptionDeny {
		return errors.New("effect must be allow or deny")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return errors.New("reason required")
	}
	if len(req.Reason) > 300 {
		return errors.New("reason too long")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.ExpiresAt.IsZero() {
		return errors.New("expires_at required")
	}
	if !req.ExpiresAt.After(time.Now()) {
		return errors.New("expires_at must be in the future")
	}
	if req.StartsAt != nil && !req.StartsAt.Before(req.ExpiresAt) {
		return errors.New("starts_at must be before expires_at")
	}
	seenUsers := map[int64]bool{}
	for _, userID := range req.UserIDs {
		if seenUsers[userID] {
			continue
		}
		seenUsers[userID] = true
		if _, err := s.store.GetUser(ctx, userID); err != nil {
			return fmt.Errorf("user_id %d: %w", userID, err)
		}
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	nodes, err := core.BuildAssignableNodeCatalog(core.AssignableNodeCatalogInput{
		Servers:           data.Servers,
		Inbounds:          data.Inbounds,
		ProxyPaths:        data.ProxyPaths,
		ProxyPathSteps:    data.ProxyPathSteps,
		EgressResults:     data.ProxyPathEgressResults,
		ExternalOutbounds: data.ExternalOutbounds,
		ServerOnline:      serverOnlineMap(data),
	})
	if err != nil {
		return err
	}
	assignable := map[string]bool{}
	for _, node := range nodes {
		assignable[node.Key] = true
	}
	for _, node := range req.Nodes {
		if !assignable[core.NodeKeyOf(node.NodeType, node.NodeID)] {
			return fmt.Errorf("node %s is not assignable", core.NodeKeyOf(node.NodeType, node.NodeID))
		}
	}
	return nil
}

// userNodeExceptionsBatch serves POST /api/v1/user-node-exceptions/batch/{preview,apply}.
func (s *Server) userNodeExceptionsBatch(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/user-node-exceptions/batch/")
	if len(parts) != 1 || r.Method != http.MethodPost {
		method(w)
		return
	}
	var req batchUserExceptionRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.validateBatchUserExceptionRequest(r.Context(), &req); err != nil {
		fail(w, err, 400)
		return
	}
	existing, err := s.store.ListUserNodeExceptions(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	outcome, err := s.planBatchUserExceptions(r.Context(), req, existing)
	if err != nil {
		fail(w, err, 400)
		return
	}
	affectedUsers := map[int64]bool{}
	for _, item := range append(append([]model.UserNodeException{}, outcome.Created...), outcome.Updated...) {
		affectedUsers[item.UserID] = true
	}
	switch parts[0] {
	case "preview":
		write(w, 200, map[string]any{
			"created":                    len(outcome.Created),
			"updated":                    len(outcome.Updated),
			"skipped":                    len(outcome.Skipped),
			"skipped_items":              outcome.Skipped,
			"affected_users":             len(affectedUsers),
			"effect":                     req.Effect,
			"runtime_authorization_mode": s.authorizationMode(r.Context()),
		})
	case "apply":
		if len(outcome.Created)+len(outcome.Updated) == 0 {
			write(w, 200, map[string]any{
				"created": 0, "updated": 0, "skipped": len(outcome.Skipped),
				"access_change_id": nil, "access_change_status": "none",
				"runtime_authorization_mode": s.authorizationMode(r.Context()),
			})
			return
		}
		writes := make([]store.UserNodeExceptionWrite, 0, len(outcome.Created)+len(outcome.Updated))
		for _, item := range append(append([]model.UserNodeException{}, outcome.Created...), outcome.Updated...) {
			writes = append(writes, store.UserNodeExceptionWrite{
				ID: item.ID, UserID: item.UserID, NodeType: item.NodeType, NodeID: item.NodeID,
				Effect: item.Effect, Reason: item.Reason, Status: item.Status,
				StartsAt: item.StartsAt, ExpiresAt: item.ExpiresAt,
			})
		}
		written, err := s.store.ApplyUserNodeExceptionBatch(r.Context(), writes)
		if err != nil {
			fail(w, err, 409)
			return
		}
		before := existing
		after := append([]model.UserNodeException{}, before...)
		ids := []int64{}
		for i := range written {
			item := written[i]
			item.Status = model.UserNodeExceptionActive
			after = append(after, item)
			ids = append(ids, item.ID)
		}
		config, err := s.store.FullRoutingConfigData(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		now := time.Now()
		at := effectiveWindow(now, req.StartsAt, &req.ExpiresAt)
		targetStatus := model.UserNodeExceptionStatus("")
		if req.Effect == model.UserNodeExceptionAllow {
			targetStatus = model.UserNodeExceptionActive
		}
		change, err := s.createExceptionChanges(r.Context(), r, config, before, after, ids, targetStatus, len(affectedUsers), at)
		if err != nil {
			fail(w, err, 500)
			return
		}
		for _, id := range ids {
			if err := s.store.SetUserNodeExceptionChange(r.Context(), id, change.ID); err != nil {
				fail(w, err, 500)
				return
			}
		}
		auditReq(s, r, "batch", "user-node-exception", fmt.Sprintf("effect=%s users=%d nodes=%d created=%d updated=%d skipped=%d change=%d", req.Effect, len(affectedUsers), len(req.Nodes), len(outcome.Created), len(outcome.Updated), len(outcome.Skipped), change.ID))
		write(w, 200, map[string]any{
			"created": len(outcome.Created), "updated": len(outcome.Updated), "skipped": len(outcome.Skipped),
			"affected_users":   len(affectedUsers),
			"access_change_id": change.ID, "access_change_status": change.Status, "queued_tasks": len(change.Targets),
			"runtime_authorization_mode": s.authorizationMode(r.Context()),
		})
	default:
		fail(w, errors.New("expected /api/v1/user-node-exceptions/batch/preview or /apply"), 404)
	}
}
