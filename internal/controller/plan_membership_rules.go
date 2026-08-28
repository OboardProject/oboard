package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type planMembershipRulesRequest struct {
	BaseRevisionID      int64                      `json:"base_revision_id"`
	ExpectedLockVersion int64                      `json:"expected_lock_version"`
	Rules               []model.PlanMembershipRule `json:"rules"`
	Exclusions          []model.PlanNodeExclusion  `json:"exclusions"`
	ChangeSummary       string                     `json:"change_summary"`
}

func (s *Server) assignableNodeCatalog(ctx context.Context) ([]core.AssignableNode, error) {
	config, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	metadata, err := s.store.ListNodeMetadata(ctx)
	if err != nil {
		return nil, err
	}
	serverOnline := make(map[int64]bool, len(config.Servers))
	for _, server := range config.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	return core.BuildAssignableNodeCatalog(core.AssignableNodeCatalogInput{
		Servers: config.Servers, Inbounds: config.Inbounds, ProxyPaths: config.ProxyPaths,
		ProxyPathSteps: config.ProxyPathSteps, EgressResults: config.ProxyPathEgressResults,
		ExternalOutbounds: config.ExternalOutbounds, ServerOnline: serverOnline, NodeMetadata: metadata,
	})
}

func (s *Server) planMembershipRulesGet(w http.ResponseWriter, r *http.Request, planID int64) {
	plan, err := s.store.GetSubscriptionPlan(r.Context(), planID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	revisionID := plan.LatestRevisionID
	if raw := strings.TrimSpace(r.URL.Query().Get("revision_id")); raw != "" {
		revisionID, err = parseRevisionID(raw)
		if err != nil {
			fail(w, err, 400)
			return
		}
	}
	rules, exclusions, err := s.store.ListPlanRevisionMembershipPolicy(r.Context(), revisionID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	nodes, err := s.store.ListPlanRevisionNodes(r.Context(), revisionID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	state, err := s.store.GetPlanRuleReconcileState(r.Context(), planID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{
		"plan_id": planID, "revision_id": revisionID, "base_revision_id": plan.LatestRevisionID,
		"lock_version": plan.LockVersion, "read_only": revisionID != plan.LatestRevisionID,
		"rules": rules, "exclusions": exclusions, "nodes": nodes, "reconcile_state": state,
	})
}

func normalizeRuleExclusions(items []model.PlanNodeExclusion) ([]model.PlanNodeExclusion, error) {
	seen := map[string]bool{}
	out := make([]model.PlanNodeExclusion, 0, len(items))
	for _, item := range items {
		key := core.NodeKeyOf(item.NodeType, item.NodeID)
		nodeType, nodeID, ok := core.ParseNodeKey(key)
		if !ok || nodeID <= 0 || nodeType != item.NodeType {
			return nil, fmt.Errorf("invalid excluded node %s", key)
		}
		switch nodeType {
		case model.AssignableNodeInbound, model.AssignableNodeProxyPath, model.AssignableNodeExternalOutbound:
		default:
			return nil, fmt.Errorf("invalid excluded node %s", key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		item.RevisionID = 0
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return core.NodeKeyOf(out[i].NodeType, out[i].NodeID) < core.NodeKeyOf(out[j].NodeType, out[j].NodeID)
	})
	return out, nil
}

func (s *Server) computePlanMembershipRules(ctx context.Context, planID int64, req planMembershipRulesRequest) (*model.SubscriptionPlan, []model.PlanMembershipRule, []model.PlanNodeExclusion, core.PlanMembershipResolution, error) {
	plan, err := s.store.GetSubscriptionPlan(ctx, planID)
	if err != nil {
		return nil, nil, nil, core.PlanMembershipResolution{}, err
	}
	baseID := req.BaseRevisionID
	if baseID == 0 {
		baseID = plan.LatestRevisionID
	}
	if baseID != plan.LatestRevisionID {
		return nil, nil, nil, core.PlanMembershipResolution{}, store.ErrPlanRevisionConflict
	}
	baseNodes, err := s.store.ListPlanRevisionNodes(ctx, baseID)
	if err != nil {
		return nil, nil, nil, core.PlanMembershipResolution{}, err
	}
	rules, err := core.NormalizePlanMembershipRules(req.Rules)
	if err != nil {
		return nil, nil, nil, core.PlanMembershipResolution{}, err
	}
	catalog, err := s.assignableNodeCatalog(ctx)
	if err != nil {
		return nil, nil, nil, core.PlanMembershipResolution{}, err
	}
	exclusions, err := normalizeRuleExclusions(req.Exclusions)
	if err != nil {
		return nil, nil, nil, core.PlanMembershipResolution{}, err
	}
	resolution, err := core.ResolvePlanMembershipRules(baseNodes, rules, exclusions, catalog)
	return plan, rules, exclusions, resolution, err
}

func (s *Server) orderingForDesiredMembership(ctx context.Context, planID, baseRevisionID int64, desired []model.SubscriptionPlanNode) (*store.PlanOrderingMutation, error) {
	revision, baseNodes, err := s.store.GetPlanRevisionOrdering(ctx, planID, baseRevisionID)
	if err != nil || revision.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual {
		return nil, err
	}
	config, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	nameOptions, err := s.orderingNameOptions(ctx)
	if err != nil {
		return nil, err
	}
	baseOrdered, err := core.BuildOrderingNodes(baseNodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, revision.NodeOrderPolicy, nameOptions)
	if err != nil {
		return nil, err
	}
	desiredOrdered, err := core.BuildOrderingNodes(desired, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, revision.NodeOrderPolicy, nameOptions)
	if err != nil {
		return nil, err
	}
	desiredByKey := map[string]core.SubscriptionNode{}
	baseKeys := map[string]bool{}
	positioned := map[string]bool{}
	for _, node := range desiredOrdered {
		desiredByKey[node.Key] = node
	}
	for _, node := range baseNodes {
		if node.SortPosition != nil {
			positioned[core.NodeKeyOf(node.NodeType, node.NodeID)] = true
		}
	}
	for _, node := range baseOrdered {
		baseKeys[node.Key] = true
	}
	existing := []core.SubscriptionNode{}
	for _, node := range baseOrdered {
		if desiredNode, ok := desiredByKey[node.Key]; ok && positioned[node.Key] {
			existing = append(existing, desiredNode)
		}
	}
	added := []core.SubscriptionNode{}
	for _, node := range desiredOrdered {
		if !baseKeys[node.Key] {
			added = append(added, node)
		}
	}
	placement := core.AutoPlaceNewManualNodes(existing, added, revision.NodeOrderPolicy)
	return &store.PlanOrderingMutation{Policy: revision.NodeOrderPolicy, ManualOrder: placement.OrderedNodeKeys}, nil
}

func (s *Server) planMembershipRulesPreview(w http.ResponseWriter, r *http.Request, planID int64) {
	var req planMembershipRulesRequest
	if !decode(w, r, &req) {
		return
	}
	plan, rules, exclusions, resolution, err := s.computePlanMembershipRules(r.Context(), planID, req)
	if err != nil {
		status := 400
		if errors.Is(err, sql.ErrNoRows) {
			status = 404
		}
		if errors.Is(err, store.ErrPlanRevisionConflict) {
			status = 409
		}
		fail(w, err, status)
		return
	}
	write(w, 200, map[string]any{
		"plan_id": planID, "base_revision_id": plan.LatestRevisionID, "lock_version": plan.LockVersion,
		"rules": rules, "exclusions": exclusions, "nodes": resolution.Nodes,
		"added_node_keys": resolution.AddedKeys, "removed_node_keys": resolution.RemovedKeys,
		"matched_by": resolution.MatchedBy, "warnings": resolution.Warnings, "desired_digest": resolution.Digest,
	})
}

func (s *Server) planMembershipRulesVersionCreate(w http.ResponseWriter, r *http.Request, planID int64) {
	var req planMembershipRulesRequest
	if !decode(w, r, &req) {
		return
	}
	if req.BaseRevisionID <= 0 || req.ExpectedLockVersion <= 0 {
		fail(w, errors.New("base_revision_id and expected_lock_version are required"), 400)
		return
	}
	plan, rules, exclusions, resolution, err := s.computePlanMembershipRules(r.Context(), planID, req)
	if err != nil {
		s.planVersionConflict(w, err, planID)
		return
	}
	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		summary = "修改方案自动节点规则"
	}
	ordering, err := s.orderingForDesiredMembership(r.Context(), planID, req.BaseRevisionID, resolution.Nodes)
	if err != nil {
		fail(w, err, 500)
		return
	}
	result, err := s.store.CreatePlanVersion(r.Context(), planID, store.PlanVersionMutation{
		BaseRevisionID: req.BaseRevisionID, ExpectedLockVersion: req.ExpectedLockVersion,
		MembershipPolicy: &store.PlanMembershipPolicyMutation{Rules: rules, Exclusions: exclusions, Nodes: resolution.Nodes},
		Ordering:         ordering,
		ChangeKind:       model.PlanChangeKindNodes, ChangeSummary: summary, CreatedBy: requestActorID(r),
	})
	if err != nil {
		s.planVersionConflict(w, err, planID)
		return
	}
	response := map[string]any{"revision": result.Revision, "no_change": result.NoChange, "lock_version": result.LockVersion, "warnings": resolution.Warnings, "added_node_keys": resolution.AddedKeys, "removed_node_keys": resolution.RemovedKeys}
	if result.NoChange || !result.RequiresDeployment {
		response["effective_immediately"] = true
		write(w, 200, response)
		return
	}
	s.signalPlanReconcile(planID)
	var change *model.AccessChange
	if hasOpen, _ := s.store.HasOpenPlanAccessChange(r.Context(), planID); !hasOpen {
		fresh, _ := s.store.GetSubscriptionPlan(r.Context(), planID)
		if fresh != nil {
			plan.PendingRevisionID = result.PendingRevisionID
			if c, err := s.createPlanPublishChange(r.Context(), r, plan, result.Revision.ID); err == nil {
				change = c
			}
		}
	}
	if change != nil {
		response["access_change_id"] = change.ID
		response["access_change_status"] = change.Status
		response["queued_tasks"] = len(change.Targets)
	} else {
		response["reconcile_queued"] = true
	}
	auditReq(s, r, "membership_rules.update", "subscription-plan", fmt.Sprintf("plan=%d version=%d", planID, result.Revision.ID))
	write(w, 200, response)
}
