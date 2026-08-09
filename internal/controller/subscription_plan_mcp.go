package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) prepareSubscriptionPlanNodesRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	planTarget := firstTaskRef(input, "subscription_plan", "subscription_plan", "target_plan", "plan", "plan_id")
	planResolution, err := s.resolveSubscriptionPlanRef(ctx, principal, planTarget)
	if err != nil && planTarget != "" {
		return nil, err
	}
	if planTarget == "" {
		planResolution, err = s.inferSubscriptionPlanFromGoal(ctx, principal, input.Goal)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
	}
	if planResolution.Value == nil {
		if len(planResolution.Candidates) > 0 {
			return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "subscription_plan.nodes.manage", Field: "subscription_plan", Candidates: planResolution.Candidates}, nil
		}
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "subscription_plan.nodes.manage", Questions: []map[string]any{{"field": "subscription_plan", "type": "resource_ref", "reason": "需要指定要修改的订阅套餐"}}}, nil
	}

	op := strings.ToLower(taskStringParam(input.Params, "op", "operation"))
	if op == "" {
		switch {
		case containsAnyFold(input.Goal, "移除", "删除", "remove"):
			op = "remove"
		case containsAnyFold(input.Goal, "替换", "replace"):
			op = "replace"
		default:
			op = "add"
		}
	}
	nodes, err := planNodesFromTaskParams(input.Params)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		refs := append(taskRefsByType(input.TargetRefs, "inbound"), taskRefsByType(input.TargetRefs, "proxy_path")...)
		refs = append(refs, taskRefsByType(input.TargetRefs, "external_outbound")...)
		if len(refs) == 0 {
			candidates, inferErr := s.inferAssignableNodeCandidates(ctx, principal, input.Goal)
			if inferErr != nil {
				return nil, inferErr
			}
			if len(candidates) == 1 {
				refs = []string{candidates[0].Ref}
			} else if len(candidates) > 1 {
				return &mcpPreparedRecipe{Status: "choose_candidate", Intent: "subscription_plan.nodes.manage", Field: "nodes", Candidates: candidates}, nil
			}
		}
		for _, ref := range refs {
			node, resolveErr := s.resolveAssignableNodeRef(ctx, principal, ref)
			if resolveErr != nil {
				return nil, resolveErr
			}
			nodes = append(nodes, planNodeRequest{NodeType: model.AssignableNodeType(node.Type), NodeID: node.ID})
		}
	}
	if len(nodes) == 0 && op != "replace" {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "subscription_plan.nodes.manage", Questions: []map[string]any{{"field": "nodes", "type": "resource_ref_array", "reason": "需要指定要分配的入口、代理路径或外部节点"}}}, nil
	}
	operationInput := map[string]any{
		"plan_id": planResolution.Value.ID, "op": op, "nodes": nodes,
		"change_summary": firstNonEmptyString(taskStringParam(input.Params, "change_summary"), strings.TrimSpace(input.Goal)),
	}
	if value := taskStringParam(input.Params, "display_group"); value != "" {
		operationInput["display_group"] = value
	}
	if value := taskIntParam(input.Params, "expected_lock_version"); value > 0 {
		operationInput["expected_lock_version"] = value
	}
	if value := taskIntParam(input.Params, "base_revision_id"); value > 0 {
		operationInput["base_revision_id"] = value
	}
	operation := mcpOperationRef{Capability: "subscription_plans.nodes.update", Input: operationInput}
	return &mcpPreparedRecipe{
		Status: "ready", Intent: "subscription_plan.nodes.manage", Operations: []mcpOperationRef{operation},
		Summary:      map[string]any{"action": op + "_subscription_plan_nodes", "subscription_plan": planResolution.Value.Label, "node_count": len(nodes)},
		Verification: map[string]any{"after_commit": []string{"workflow_terminal", "subscription_plan_revision_changed", "access_change_terminal"}},
	}, nil
}

func planNodesFromTaskParams(params map[string]any) ([]planNodeRequest, error) {
	raw, ok := params["nodes"]
	if !ok {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var nodes []planNodeRequest
	if err := json.Unmarshal(encoded, &nodes); err != nil {
		return nil, fmt.Errorf("nodes must be an array of node_type/node_id objects: %w", err)
	}
	return nodes, nil
}

func (s *Server) resolveSubscriptionPlanRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "subscription_plan")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	items, err := s.store.ListSubscriptionPlans(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	if id, parseErr := strconv.ParseInt(target, 10, 64); parseErr == nil && id > 0 {
		for _, item := range items {
			if item.ID == id && principal.AllowsInt64("subscription_plan_ids", item.ID) {
				ref := subscriptionPlanMCPResourceRef(item)
				return mcpResourceResolution{Value: &ref}, nil
			}
		}
		return mcpResourceResolution{}, sql.ErrNoRows
	}
	wanted := normalizeMCPResourceName(target)
	matches := []MCPResourceRef{}
	for _, item := range items {
		if principal.AllowsInt64("subscription_plan_ids", item.ID) && normalizeMCPResourceName(item.Name) == wanted {
			matches = append(matches, subscriptionPlanMCPResourceRef(item))
		}
	}
	return finishMCPResourceResolution(matches)
}

func (s *Server) inferSubscriptionPlanFromGoal(ctx context.Context, principal application.Principal, goal string) (mcpResourceResolution, error) {
	items, err := s.store.ListSubscriptionPlans(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	matches := []MCPResourceRef{}
	for _, item := range items {
		if principal.AllowsInt64("subscription_plan_ids", item.ID) && strings.TrimSpace(item.Name) != "" && containsAnyFold(goal, item.Name) {
			matches = append(matches, subscriptionPlanMCPResourceRef(item))
		}
	}
	if len(matches) == 0 {
		return mcpResourceResolution{}, sql.ErrNoRows
	}
	return finishMCPResourceResolution(matches)
}

func subscriptionPlanMCPResourceRef(item model.SubscriptionPlan) MCPResourceRef {
	return MCPResourceRef{Type: "subscription_plan", ID: item.ID, Name: item.Name, Ref: "subscription_plan:" + strconv.FormatInt(item.ID, 10), Label: item.Name}
}

func (s *Server) resolveProxyPathRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "proxy_path")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	allowed := map[int64]bool{}
	for _, inbound := range inbounds {
		allowed[inbound.ID] = principal.AllowsInt64("server_ids", inbound.ServerID)
	}
	wanted := normalizeMCPResourceName(target)
	matches := []MCPResourceRef{}
	for _, item := range paths {
		if !allowed[item.InboundID] || !principal.AllowsInt64("proxy_path_ids", item.ID) {
			continue
		}
		idMatch := strconv.FormatInt(item.ID, 10) == target
		if idMatch || normalizeMCPResourceName(item.Name) == wanted {
			matches = append(matches, MCPResourceRef{Type: "proxy_path", ID: item.ID, Name: item.Name, Ref: "proxy_path:" + strconv.FormatInt(item.ID, 10), Label: item.Name})
		}
	}
	return finishMCPResourceResolution(matches)
}

func (s *Server) inferAssignableNodeCandidates(ctx context.Context, principal application.Principal, goal string) ([]MCPResourceRef, error) {
	matches := []MCPResourceRef{}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	serverNames := map[int64]string{}
	for _, server := range servers {
		serverNames[server.ID] = server.Name
	}
	for _, inbound := range inbounds {
		if principal.AllowsInt64("server_ids", inbound.ServerID) && strings.TrimSpace(inbound.Name) != "" && containsAnyFold(goal, inbound.Name) {
			matches = append(matches, inboundMCPResourceRef(inbound, serverNames[inbound.ServerID]))
		}
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if principal.AllowsInt64("proxy_path_ids", path.ID) && strings.TrimSpace(path.Name) != "" && containsAnyFold(goal, path.Name) {
			matches = append(matches, MCPResourceRef{Type: "proxy_path", ID: path.ID, Name: path.Name, Ref: "proxy_path:" + strconv.FormatInt(path.ID, 10), Label: path.Name})
		}
	}
	externals, err := s.store.ListExternalOutbounds(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range externals {
		if item.ServerID != nil && !principal.AllowsInt64("server_ids", *item.ServerID) {
			continue
		}
		if strings.TrimSpace(item.Name) != "" && containsAnyFold(goal, item.Name) {
			matches = append(matches, MCPResourceRef{Type: "external_outbound", ID: item.ID, Name: item.Name, Ref: "external_outbound:" + strconv.FormatInt(item.ID, 10), Label: item.Name})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Type == matches[j].Type {
			return matches[i].ID < matches[j].ID
		}
		return matches[i].Type < matches[j].Type
	})
	return matches, nil
}

func (s *Server) resolveAssignableNodeRef(ctx context.Context, principal application.Principal, ref string) (*MCPResourceRef, error) {
	resourceType, _, found := strings.Cut(strings.ToLower(strings.TrimSpace(ref)), ":")
	if !found {
		return nil, errors.New("node resource reference requires a type prefix")
	}
	var resolution mcpResourceResolution
	var err error
	switch resourceType {
	case "inbound":
		resolution, err = s.resolveInboundRef(ctx, principal, ref)
	case "external_outbound":
		resolution, err = s.resolveExternalOutboundRef(ctx, principal, ref)
	case "proxy_path":
		resolution, err = s.resolveProxyPathRef(ctx, principal, ref)
	default:
		return nil, errors.New("node reference must be inbound, proxy_path, or external_outbound")
	}
	if err != nil {
		return nil, err
	}
	if resolution.Value == nil {
		return nil, sql.ErrNoRows
	}
	return resolution.Value, nil
}
