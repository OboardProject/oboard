package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) prepareUserNodeAuthorizationRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	users, err := s.userNodeAuthorizationRecipeUsers(ctx, principal, input)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_node_authorization.manage", Questions: []map[string]any{{"field": "users", "type": "resource_ref_array", "reason": "需要指定要授权或撤销授权的用户"}}}, nil
	}
	nodes, err := s.userNodeAuthorizationRecipeNodes(ctx, principal, input)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return &mcpPreparedRecipe{Status: "needs_input", Intent: "user_node_authorization.manage", Questions: []map[string]any{{"field": "nodes", "type": "resource_ref_array", "reason": "需要指定入口、代理路径或导入节点"}}}, nil
	}

	revoke := taskBoolParam(input.Params, false, "revoke", "delete", "remove") || containsAnyFold(input.Goal, "撤销", "删除授权", "移除授权", "revoke authorization", "remove authorization")
	userIDs := make([]int64, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	if revoke {
		data, err := s.loadPlanAssignmentData(ctx)
		if err != nil {
			return nil, err
		}
		wantedUsers := map[int64]bool{}
		for _, id := range userIDs {
			wantedUsers[id] = true
		}
		wantedNodes := map[string]bool{}
		for _, node := range nodes {
			wantedNodes[node.Type+":"+strconv.FormatInt(node.ID, 10)] = true
		}
		ids := []int64{}
		for _, item := range data.exceptions {
			if !wantedUsers[item.UserID] || !wantedNodes[string(item.NodeType)+":"+strconv.FormatInt(item.NodeID, 10)] {
				continue
			}
			if item.Status == model.UserNodeExceptionActive || item.Status == model.UserNodeExceptionPending || item.Status == "" {
				ids = append(ids, item.ID)
			}
		}
		if len(ids) == 0 {
			return nil, errors.New("指定用户和节点之间没有可撤销的单独授权")
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		operation := mcpOperationRef{Capability: "user_node_authorizations.revoke", Input: map[string]any{
			"authorization_ids": ids, "user_ids": userIDs, "nodes": nodeRequestsFromRefs(nodes), "confirm": true,
		}}
		return &mcpPreparedRecipe{
			Status: "ready", Intent: "user_node_authorization.manage", Operations: []mcpOperationRef{operation},
			Summary:      map[string]any{"action": "revoke_user_node_authorizations", "user_count": len(userIDs), "node_count": len(nodes), "authorization_count": len(ids)},
			Verification: map[string]any{"after_commit": []string{"workflow_terminal", "access_change_terminal", "node_authorizations_refreshed"}},
		}, nil
	}

	effect := strings.ToLower(taskStringParam(input.Params, "effect"))
	if effect == "" {
		if containsAnyFold(input.Goal, "拒绝", "禁止", "deny", "block") {
			effect = string(model.UserNodeExceptionDeny)
		} else {
			effect = string(model.UserNodeExceptionAllow)
		}
	}
	operationInput := map[string]any{
		"user_ids": userIDs, "nodes": nodeRequestsFromRefs(nodes), "effect": effect,
		"reason": taskStringParam(input.Params, "reason", "remark", "备注", "原因"),
	}
	for _, key := range []string{"starts_at", "expires_at"} {
		if value := taskStringParam(input.Params, key); value != "" {
			operationInput[key] = value
		}
	}
	operation := mcpOperationRef{Capability: "user_node_authorizations.set", Input: operationInput}
	return &mcpPreparedRecipe{
		Status: "ready", Intent: "user_node_authorization.manage", Operations: []mcpOperationRef{operation},
		Summary:      map[string]any{"action": effect + "_user_node_authorizations", "user_count": len(userIDs), "node_count": len(nodes), "union_with_plan_nodes": effect == string(model.UserNodeExceptionAllow)},
		Verification: map[string]any{"after_commit": []string{"workflow_terminal", "access_change_terminal", "node_authorizations_refreshed"}},
	}, nil
}

func (s *Server) userNodeAuthorizationRecipeUsers(ctx context.Context, principal application.Principal, input mcpTaskInput) ([]MCPResourceRef, error) {
	refs := taskRefsByType(input.TargetRefs, "user")
	if raw, ok := input.Params["user_ids"]; ok {
		ids, err := taskInt64Slice(raw)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			refs = append(refs, "user:"+strconv.FormatInt(id, 10))
		}
	}
	if value := taskIntParam(input.Params, "user_id"); value > 0 {
		refs = append(refs, "user:"+strconv.FormatInt(int64(value), 10))
	}
	if len(refs) == 0 {
		candidates := s.inferUserCandidatesFromGoal(ctx, principal, input.Goal)
		if len(candidates) == 1 {
			return candidates, nil
		}
		if len(candidates) > 1 {
			return nil, errors.New("用户名称匹配多个账号，请使用 user:<id> 明确指定")
		}
	}
	out := []MCPResourceRef{}
	seen := map[int64]bool{}
	for _, ref := range refs {
		resolved, err := s.resolveUserRef(ctx, principal, ref)
		if err != nil {
			return nil, err
		}
		if resolved.Value == nil {
			return nil, sql.ErrNoRows
		}
		if !seen[resolved.Value.ID] {
			seen[resolved.Value.ID] = true
			out = append(out, *resolved.Value)
		}
	}
	return out, nil
}

func (s *Server) userNodeAuthorizationRecipeNodes(ctx context.Context, principal application.Principal, input mcpTaskInput) ([]MCPResourceRef, error) {
	refs := append(taskRefsByType(input.TargetRefs, "inbound"), taskRefsByType(input.TargetRefs, "proxy_path")...)
	refs = append(refs, taskRefsByType(input.TargetRefs, "external_outbound")...)
	if raw, ok := input.Params["nodes"]; ok {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var requests []planNodeRequest
		if err := json.Unmarshal(encoded, &requests); err != nil {
			return nil, errors.New("nodes must be node_type/node_id objects")
		}
		for _, request := range requests {
			refs = append(refs, string(request.NodeType)+":"+strconv.FormatInt(request.NodeID, 10))
		}
	}
	if len(refs) == 0 {
		candidates, err := s.inferAssignableNodeCandidates(ctx, principal, input.Goal)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 1 {
			return candidates, nil
		}
		if len(candidates) > 1 {
			return nil, errors.New("节点名称匹配多个资源，请使用带类型的节点引用明确指定")
		}
	}
	out := []MCPResourceRef{}
	seen := map[string]bool{}
	for _, ref := range refs {
		resolved, err := s.resolveAssignableNodeRef(ctx, principal, ref)
		if err != nil {
			return nil, err
		}
		if !seen[resolved.Ref] {
			seen[resolved.Ref] = true
			out = append(out, *resolved)
		}
	}
	return out, nil
}

func nodeRequestsFromRefs(refs []MCPResourceRef) []map[string]any {
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		out = append(out, map[string]any{"node_type": ref.Type, "node_id": ref.ID})
	}
	return out
}

func taskInt64Slice(raw any) ([]int64, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err := json.Unmarshal(encoded, &ids); err != nil {
		return nil, errors.New("expected an array of integer IDs")
	}
	return ids, nil
}
