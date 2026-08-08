package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const (
	orderCopyRulesPreserveManual = "copy_rules_preserve_manual"
	orderCopyRulesRebuild        = "copy_rules_rebuild"
	orderCopyEffective           = "copy_effective_order"
)

type planOrderingCopyRequest struct {
	SourcePlanID        int64  `json:"source_plan_id"`
	SourceRevisionID    int64  `json:"source_revision_id"`
	BaseRevisionID      int64  `json:"base_revision_id"`
	ExpectedLockVersion int64  `json:"expected_lock_version"`
	Mode                string `json:"mode"`
	ChangeSummary       string `json:"change_summary"`
}

type planOrderingCopyResult struct {
	SourceRevision *model.SubscriptionPlanRevision
	Policy         model.SubscriptionNodeOrderPolicy
	ManualOrder    []string
	TargetNodes    []model.SubscriptionPlanNode
	Views          []orderingNodeView
	Warnings       []string
}

func validatePlanOrderingCopyMode(mode string) error {
	switch mode {
	case orderCopyRulesPreserveManual, orderCopyRulesRebuild, orderCopyEffective:
		return nil
	default:
		return errors.New("mode must be copy_rules_preserve_manual, copy_rules_rebuild, or copy_effective_order")
	}
}

func (s *Server) computePlanOrderingCopy(r *http.Request, targetPlanID int64, req planOrderingCopyRequest) (*planOrderingCopyResult, error) {
	if req.SourcePlanID <= 0 || req.SourcePlanID == targetPlanID {
		return nil, errors.New("source_plan_id must identify another plan")
	}
	if err := validatePlanOrderingCopyMode(req.Mode); err != nil {
		return nil, err
	}
	targetPlan, err := s.store.GetSubscriptionPlan(r.Context(), targetPlanID)
	if err != nil {
		return nil, err
	}
	baseID := req.BaseRevisionID
	if baseID == 0 {
		baseID = targetPlan.LatestRevisionID
	}
	if baseID != targetPlan.LatestRevisionID {
		return nil, store.ErrPlanRevisionConflict
	}
	sourcePlan, err := s.store.GetSubscriptionPlan(r.Context(), req.SourcePlanID)
	if err != nil {
		return nil, err
	}
	sourceRevisionID := req.SourceRevisionID
	if sourceRevisionID == 0 {
		sourceRevisionID = sourcePlan.LatestRevisionID
	}
	sourceRevision, sourceNodes, err := s.store.GetPlanRevisionOrdering(r.Context(), req.SourcePlanID, sourceRevisionID)
	if err != nil {
		return nil, err
	}
	_, targetNodes, err := s.store.GetPlanRevisionOrdering(r.Context(), targetPlanID, baseID)
	if err != nil {
		return nil, err
	}
	policy, err := core.ValidateSubscriptionNodeOrderPolicy(sourceRevision.NodeOrderPolicy)
	if err != nil {
		return nil, err
	}
	if req.Mode != orderCopyEffective && policy.Mode == model.SubscriptionNodeOrderManual {
		policy.Mode = policy.ManualSeed
		if policy.Mode != model.SubscriptionNodeOrderExitRegion && policy.Mode != model.SubscriptionNodeOrderEntry {
			policy.Mode = model.SubscriptionNodeOrderExitRegion
		}
	}
	config, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		return nil, err
	}
	nameOptions, err := s.orderingNameOptions(r.Context())
	if err != nil {
		return nil, err
	}
	sourceOrder, err := core.BuildOrderingNodes(sourceNodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, sourceRevision.NodeOrderPolicy, nameOptions)
	if err != nil {
		return nil, err
	}
	targetCurrentRevision, targetCurrentNodes, err := s.store.GetPlanRevisionOrdering(r.Context(), targetPlanID, baseID)
	if err != nil {
		return nil, err
	}
	targetCurrentOrder, err := core.BuildOrderingNodes(targetCurrentNodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, targetCurrentRevision.NodeOrderPolicy, nameOptions)
	if err != nil {
		return nil, err
	}
	manualOrder := []string{}
	warnings := []string{}
	switch req.Mode {
	case orderCopyRulesPreserveManual:
		policy.Mode = model.SubscriptionNodeOrderManual
		for _, node := range targetCurrentOrder {
			manualOrder = append(manualOrder, node.Key)
		}
	case orderCopyRulesRebuild:
		// The source's automatic comparator becomes the target snapshot.
	case orderCopyEffective:
		policy.Mode = model.SubscriptionNodeOrderManual
		targetByKey := map[string]core.SubscriptionNode{}
		targetUnderSource, buildErr := core.BuildOrderingNodes(targetNodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, policy, nameOptions)
		if buildErr != nil {
			return nil, buildErr
		}
		for _, node := range targetUnderSource {
			targetByKey[node.Key] = node
		}
		shared := []core.SubscriptionNode{}
		sharedKeys := map[string]bool{}
		for _, sourceNode := range sourceOrder {
			if targetNode, ok := targetByKey[sourceNode.Key]; ok {
				shared = append(shared, targetNode)
				sharedKeys[sourceNode.Key] = true
			}
		}
		added := []core.SubscriptionNode{}
		for _, node := range targetUnderSource {
			if !sharedKeys[node.Key] {
				added = append(added, node)
			}
		}
		placement := core.AutoPlaceNewManualNodes(shared, added, policy)
		manualOrder = placement.OrderedNodeKeys
		for _, warning := range placement.Warnings {
			warnings = append(warnings, warning.Message)
		}
	}
	positions := map[string]int{}
	for index, key := range manualOrder {
		positions[key] = index
	}
	previewNodes := append([]model.SubscriptionPlanNode(nil), targetNodes...)
	for index := range previewNodes {
		key := core.NodeKeyOf(previewNodes[index].NodeType, previewNodes[index].NodeID)
		if position, ok := positions[key]; ok {
			value := position
			previewNodes[index].SortPosition = &value
		} else {
			previewNodes[index].SortPosition = nil
		}
	}
	previewOrder, err := core.BuildOrderingNodes(previewNodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, policy, nameOptions)
	if err != nil {
		return nil, err
	}
	views, _, viewWarnings := s.orderingNodeViews(r.Context(), config, previewOrder, policy.Mode == model.SubscriptionNodeOrderManual)
	warnings = append(warnings, viewWarnings...)
	return &planOrderingCopyResult{SourceRevision: sourceRevision, Policy: policy, ManualOrder: manualOrder, TargetNodes: previewNodes, Views: views, Warnings: warnings}, nil
}

func (s *Server) planOrderingCopyPreview(w http.ResponseWriter, r *http.Request, targetPlanID int64) {
	var req planOrderingCopyRequest
	if !decode(w, r, &req) {
		return
	}
	result, err := s.computePlanOrderingCopy(r, targetPlanID, req)
	if err != nil {
		status := 400
		if errors.Is(err, store.ErrPlanRevisionConflict) {
			status = 409
		}
		fail(w, err, status)
		return
	}
	write(w, 200, map[string]any{"source_revision_id": result.SourceRevision.ID, "policy": result.Policy, "manual_node_order": result.ManualOrder, "nodes": result.Views, "warnings": result.Warnings})
}

func (s *Server) planOrderingCopyFromPlan(w http.ResponseWriter, r *http.Request, targetPlanID int64) {
	var req planOrderingCopyRequest
	if !decode(w, r, &req) {
		return
	}
	if req.BaseRevisionID <= 0 || req.ExpectedLockVersion <= 0 {
		fail(w, errors.New("base_revision_id and expected_lock_version are required"), 400)
		return
	}
	computed, err := s.computePlanOrderingCopy(r, targetPlanID, req)
	if err != nil {
		s.planVersionConflict(w, err, targetPlanID)
		return
	}
	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		summary = fmt.Sprintf("从方案 %d 复制排序", req.SourcePlanID)
	}
	sourcePlanID := req.SourcePlanID
	sourceRevisionID := computed.SourceRevision.ID
	result, err := s.store.CreatePlanVersion(r.Context(), targetPlanID, store.PlanVersionMutation{
		BaseRevisionID: req.BaseRevisionID, ExpectedLockVersion: req.ExpectedLockVersion,
		Ordering: &store.PlanOrderingMutation{Policy: computed.Policy, ManualOrder: computed.ManualOrder, ClearManualPositions: req.Mode == orderCopyRulesRebuild,
			SetSourceProvenance: true, OrderSourcePlanID: &sourcePlanID, OrderSourceRevisionID: &sourceRevisionID, OrderSourceMode: req.Mode,
			SetTemplateProvenance: true, OrderTemplateID: nil, OrderTemplateRevision: 0},
		ChangeKind: model.PlanChangeKindOrdering, ChangeSummary: summary, CreatedBy: requestActorID(r),
	})
	if err != nil {
		s.planVersionConflict(w, err, targetPlanID)
		return
	}
	auditReq(s, r, "ordering.copy_from_plan", "subscription-plan", fmt.Sprintf("target=%d source=%d revision=%d mode=%s", targetPlanID, req.SourcePlanID, sourceRevisionID, req.Mode))
	write(w, 200, map[string]any{"revision": result.Revision, "no_change": result.NoChange, "effective_immediately": true, "lock_version": result.LockVersion, "latest_revision_id": result.LatestRevisionID})
}
