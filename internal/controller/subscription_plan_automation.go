package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type subscriptionPlanNodesUpdateOperation struct {
	PlanID              int64             `json:"plan_id"`
	Op                  string            `json:"op"`
	Nodes               []planNodeRequest `json:"nodes"`
	DisplayGroup        string            `json:"display_group,omitempty"`
	ExpectedLockVersion int64             `json:"expected_lock_version,omitempty"`
	BaseRevisionID      int64             `json:"base_revision_id,omitempty"`
	ChangeSummary       string            `json:"change_summary,omitempty"`
}

type preparedSubscriptionPlanNodesUpdate struct {
	request            subscriptionPlanNodesUpdateOperation
	plan               *model.SubscriptionPlan
	nodes              []model.SubscriptionPlanNode
	membershipMutation *store.PlanMembershipPolicyMutation
	orderingMutation   *store.PlanOrderingMutation
	targetNodeCount    int
}

func (s *Server) registerSubscriptionPlanAutomationOperations() {
	const capabilityName = "subscription_plans.nodes.update"
	s.automation.RegisterValidator(capabilityName, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionPlanNodesUpdateOperation(input)
		if err != nil {
			return nil, err
		}
		prepared, err := s.prepareSubscriptionPlanNodesUpdate(ctx, principal, request)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"plan_id": prepared.plan.ID, "plan_name": prepared.plan.Name, "operation": prepared.request.Op,
			"requested_node_count": len(prepared.request.Nodes), "target_node_count": prepared.targetNodeCount,
			"current_lock_version": prepared.plan.LockVersion, "base_revision_id": prepared.request.BaseRevisionID,
		}, nil
	})
	s.automation.RegisterRevisionResolver(capabilityName, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := decodeSubscriptionPlanNodesUpdateOperation(input)
		if err != nil {
			return nil, err
		}
		plan, err := s.store.GetSubscriptionPlan(ctx, request.PlanID)
		if err != nil {
			return nil, err
		}
		if !principal.AllowsInt64("subscription_plan_ids", plan.ID) {
			return nil, errors.New("subscription plan is outside the authorized resource boundary")
		}
		revisions := map[string]string{"subscription_plan:" + strconv.FormatInt(plan.ID, 10): strconv.FormatInt(plan.LockVersion, 10)}
		for _, node := range request.Nodes {
			if err := s.authorizeSubscriptionPlanNode(ctx, principal, node); err != nil {
				return nil, err
			}
			key := string(node.NodeType) + ":" + strconv.FormatInt(node.NodeID, 10)
			switch node.NodeType {
			case model.AssignableNodeInbound:
				item, loadErr := s.store.GetInbound(ctx, node.NodeID)
				if loadErr != nil {
					return nil, loadErr
				}
				revisions[key] = item.UpdatedAt.UTC().Format(revisionTimeFormat)
			case model.AssignableNodeProxyPath:
				item, loadErr := s.store.GetProxyPath(ctx, node.NodeID)
				if loadErr != nil {
					return nil, loadErr
				}
				revisions[key] = item.UpdatedAt.UTC().Format(revisionTimeFormat)
			case model.AssignableNodeExternalOutbound:
				item, loadErr := s.store.GetExternalOutbound(ctx, node.NodeID)
				if loadErr != nil {
					return nil, loadErr
				}
				revisions[key] = item.UpdatedAt.UTC().Format(revisionTimeFormat)
			}
		}
		return revisions, nil
	})
	s.automation.Register(capabilityName, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionPlanNodesUpdateOperation(input)
		if err != nil {
			return nil, err
		}
		return s.applySubscriptionPlanNodesUpdate(ctx, principal, request)
	})
	s.automation.RegisterValidator("subscription_plans.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		plan, unbound, err := s.subscriptionPlanDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"plan_id": plan.ID, "plan_name": plan.Name, "unbound_user_count": unbound}, nil
	})
	s.automation.RegisterRevisionResolver("subscription_plans.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		plan, _, err := s.subscriptionPlanDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"subscription_plan:" + strconv.FormatInt(plan.ID, 10): strconv.FormatInt(plan.LockVersion, 10)}, nil
	})
	s.automation.Register("subscription_plans.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		plan, _, err := s.subscriptionPlanDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return s.deleteSubscriptionPlan(ctx, plan.ID, principal.UserID, nil)
	})
}

const revisionTimeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func decodeSubscriptionPlanNodesUpdateOperation(input json.RawMessage) (subscriptionPlanNodesUpdateOperation, error) {
	var request subscriptionPlanNodesUpdateOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	request.Op = strings.ToLower(strings.TrimSpace(request.Op))
	request.DisplayGroup = strings.TrimSpace(request.DisplayGroup)
	request.ChangeSummary = strings.TrimSpace(request.ChangeSummary)
	if request.PlanID <= 0 {
		return request, errors.New("plan_id must be a positive integer")
	}
	if request.Op != "add" && request.Op != "remove" && request.Op != "replace" {
		return request, errors.New("op must be add, remove, or replace")
	}
	if len(request.Nodes) > 256 || request.Op != "replace" && len(request.Nodes) == 0 {
		return request, errors.New("nodes must contain between 1 and 256 entries for add/remove, or at most 256 entries for replace")
	}
	if len(request.DisplayGroup) > 100 || len(request.ChangeSummary) > 500 {
		return request, errors.New("display_group or change_summary is too long")
	}
	return request, nil
}

func (s *Server) prepareSubscriptionPlanNodesUpdate(ctx context.Context, principal application.Principal, request subscriptionPlanNodesUpdateOperation) (*preparedSubscriptionPlanNodesUpdate, error) {
	plan, err := s.store.GetSubscriptionPlan(ctx, request.PlanID)
	if err != nil {
		return nil, err
	}
	if !principal.AllowsInt64("subscription_plan_ids", plan.ID) {
		return nil, errors.New("subscription plan is outside the authorized resource boundary")
	}
	for _, node := range request.Nodes {
		if err := s.authorizeSubscriptionPlanNode(ctx, principal, node); err != nil {
			return nil, err
		}
	}
	if request.ExpectedLockVersion == 0 {
		request.ExpectedLockVersion = plan.LockVersion
	}
	if request.ExpectedLockVersion != plan.LockVersion {
		return nil, store.ErrPlanRevisionConflict
	}
	if request.BaseRevisionID == 0 {
		request.BaseRevisionID = plan.LatestRevisionID
	}
	if request.BaseRevisionID != plan.LatestRevisionID {
		return nil, store.ErrPlanRevisionConflict
	}
	if plan.PendingRevisionID != 0 {
		return nil, store.ErrPlanVersionApplying
	}
	nodes, err := s.validatePlanNodeRequests(ctx, request.Nodes, request.DisplayGroup, request.Op == "remove")
	if err != nil {
		return nil, err
	}
	baseNodes, err := s.store.ListPlanRevisionNodes(ctx, request.BaseRevisionID)
	if err != nil {
		return nil, err
	}
	candidate, err := store.PreviewPlanNodesMutation(baseNodes, store.PlanNodesMutation{Op: request.Op, Nodes: nodes})
	if err != nil {
		return nil, err
	}
	var membershipMutation *store.PlanMembershipPolicyMutation
	rules, exclusions, err := s.store.ListPlanRevisionMembershipPolicy(ctx, request.BaseRevisionID)
	if err != nil {
		return nil, err
	}
	if len(rules) > 0 {
		baseByKey := map[string]model.SubscriptionPlanNode{}
		candidateKeys := map[string]bool{}
		for _, item := range baseNodes {
			baseByKey[core.NodeKeyOf(item.NodeType, item.NodeID)] = item
		}
		for index := range candidate {
			key := core.NodeKeyOf(candidate[index].NodeType, candidate[index].NodeID)
			candidateKeys[key] = true
			if previous, ok := baseByKey[key]; ok && request.Op == "replace" {
				candidate[index].SourceType, candidate[index].SourceRuleID = previous.SourceType, previous.SourceRuleID
			} else if _, ok := baseByKey[key]; !ok || request.Op == "add" {
				candidate[index].SourceType, candidate[index].SourceRuleID = model.PlanNodeSourceExplicit, 0
			}
		}
		excluded := map[string]model.PlanNodeExclusion{}
		for _, item := range exclusions {
			excluded[core.NodeKeyOf(item.NodeType, item.NodeID)] = item
		}
		for _, item := range nodes {
			if request.Op == "add" {
				delete(excluded, core.NodeKeyOf(item.NodeType, item.NodeID))
			}
		}
		catalog, loadErr := s.assignableNodeCatalog(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		matched, matchErr := core.ResolvePlanMembershipRules(baseNodes, rules, nil, catalog)
		if matchErr != nil {
			return nil, matchErr
		}
		for key, previous := range baseByKey {
			if candidateKeys[key] || len(matched.MatchedBy[key]) == 0 {
				continue
			}
			excluded[key] = model.PlanNodeExclusion{NodeType: previous.NodeType, NodeID: previous.NodeID}
		}
		exclusions = exclusions[:0]
		for _, item := range excluded {
			exclusions = append(exclusions, item)
		}
		resolution, resolveErr := core.ResolvePlanMembershipRules(candidate, rules, exclusions, catalog)
		if resolveErr != nil {
			return nil, resolveErr
		}
		nodes = resolution.Nodes
		candidate = resolution.Nodes
		membershipMutation = &store.PlanMembershipPolicyMutation{Rules: rules, Exclusions: exclusions, Nodes: resolution.Nodes}
	}
	mutationOp := request.Op
	if membershipMutation != nil {
		mutationOp = "replace"
	}
	orderingMutation, _, err := s.planNodePlacement(ctx, request.PlanID, request.BaseRevisionID, mutationOp, nodes)
	if err != nil {
		return nil, err
	}
	return &preparedSubscriptionPlanNodesUpdate{
		request: request, plan: plan, nodes: nodes, membershipMutation: membershipMutation,
		orderingMutation: orderingMutation, targetNodeCount: len(candidate),
	}, nil
}

func (s *Server) applySubscriptionPlanNodesUpdate(ctx context.Context, principal application.Principal, request subscriptionPlanNodesUpdateOperation) (any, error) {
	prepared, err := s.prepareSubscriptionPlanNodesUpdate(ctx, principal, request)
	if err != nil {
		return nil, err
	}
	result, err := s.store.CreatePlanVersion(ctx, prepared.plan.ID, store.PlanVersionMutation{
		BaseRevisionID: prepared.request.BaseRevisionID, ExpectedLockVersion: prepared.request.ExpectedLockVersion,
		Nodes: func() *store.PlanNodesMutation {
			if prepared.membershipMutation != nil {
				return nil
			}
			return &store.PlanNodesMutation{Op: prepared.request.Op, Nodes: prepared.nodes}
		}(),
		MembershipPolicy: prepared.membershipMutation, Ordering: prepared.orderingMutation,
		ChangeKind: model.PlanChangeKindNodes, ChangeSummary: prepared.request.ChangeSummary, CreatedBy: principal.UserID,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"plan_id": prepared.plan.ID, "no_change": result.NoChange, "lock_version": result.LockVersion,
		"latest_revision_id": result.LatestRevisionID, "pending_revision_id": result.PendingRevisionID,
		"access_change_id": int64(0), "access_change_status": "", "queued_tasks": 0, "reconcile_queued": false,
	}
	if result.NoChange {
		return out, nil
	}
	if result.RequiresDeployment {
		s.signalPlanReconcile(prepared.plan.ID)
	}
	out["reconcile_queued"] = result.RequiresDeployment
	return out, nil
}

func (s *Server) authorizeSubscriptionPlanNode(ctx context.Context, principal application.Principal, node planNodeRequest) error {
	switch node.NodeType {
	case model.AssignableNodeInbound:
		item, err := s.store.GetInbound(ctx, node.NodeID)
		if err != nil {
			return err
		}
		if !principal.AllowsInt64("server_ids", item.ServerID) {
			return errors.New("inbound is outside the authorized resource boundary")
		}
	case model.AssignableNodeProxyPath:
		if _, err := s.store.GetProxyPath(ctx, node.NodeID); err != nil {
			return err
		}
		if !principal.AllowsInt64("proxy_path_ids", node.NodeID) {
			return errors.New("proxy path is outside the authorized resource boundary")
		}
	case model.AssignableNodeExternalOutbound:
		item, err := s.store.GetExternalOutbound(ctx, node.NodeID)
		if err != nil {
			return err
		}
		if item.ServerID != nil && !principal.AllowsInt64("server_ids", *item.ServerID) {
			return errors.New("external outbound is outside the authorized resource boundary")
		}
	default:
		return errors.New("invalid node_type")
	}
	return nil
}

func (s *Server) subscriptionPlanDeleteCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (*model.SubscriptionPlan, int, error) {
	var request struct {
		PlanID  int64 `json:"plan_id"`
		Confirm bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, 0, err
	}
	if request.PlanID <= 0 || !request.Confirm {
		return nil, 0, errors.New("plan_id and confirm=true are required")
	}
	if !principal.AllowsInt64("subscription_plan_ids", request.PlanID) {
		return nil, 0, errors.New("subscription plan is outside the authorized resource boundary")
	}
	plan, err := s.store.GetSubscriptionPlan(ctx, request.PlanID)
	if err != nil {
		return nil, 0, err
	}
	members, err := s.store.ListEnabledUserPlanBindingsForPlan(ctx, plan.ID)
	if err != nil {
		return nil, 0, err
	}
	return plan, len(members), nil
}
