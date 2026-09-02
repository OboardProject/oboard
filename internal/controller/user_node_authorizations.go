package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type userNodeAuthorizationRevokeRequest struct {
	AuthorizationIDs []int64 `json:"authorization_ids"`
	UserIDs          []int64 `json:"user_ids"`
	Nodes            []struct {
		NodeType model.AssignableNodeType `json:"node_type"`
		NodeID   int64                    `json:"node_id"`
	} `json:"nodes"`
	Confirm bool `json:"confirm"`
}

func (s *Server) registerUserNodeAuthorizationAutomationOperations() {
	s.automation.RegisterValidator("user_node_authorizations.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		req, existing, outcome, err := s.prepareUserNodeAuthorizationSet(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"created": len(outcome.Created), "updated": len(outcome.Updated), "skipped": len(outcome.Skipped),
			"affected_users": len(uniquePositiveIDs(req.UserIDs)), "node_count": len(req.Nodes), "effect": req.Effect,
			"existing_authorization_count": len(existing),
		}, nil
	})
	s.automation.RegisterRevisionResolver("user_node_authorizations.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		req, existing, _, err := s.prepareUserNodeAuthorizationSet(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return s.userNodeAuthorizationSetRevisions(ctx, req, existing)
	})
	s.automation.Register("user_node_authorizations.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		req, existing, outcome, err := s.prepareUserNodeAuthorizationSet(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return s.applyUserNodeAuthorizationSet(ctx, principal.UserID, req, existing, outcome)
	})

	s.automation.RegisterValidator("user_node_authorizations.revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		items, err := s.prepareUserNodeAuthorizationRevoke(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"authorization_ids": authorizationIDs(items), "affected_users": countAuthorizationUsers(items)}, nil
	})
	s.automation.RegisterRevisionResolver("user_node_authorizations.revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		items, err := s.prepareUserNodeAuthorizationRevoke(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		revisions := map[string]string{}
		for _, item := range items {
			revisions["user_node_authorization:"+strconv.FormatInt(item.ID, 10)] = userNodeAuthorizationRevision(item)
		}
		return revisions, nil
	})
	s.automation.Register("user_node_authorizations.revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		items, err := s.prepareUserNodeAuthorizationRevoke(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return s.revokeUserNodeAuthorizations(ctx, principal.UserID, items)
	})
}

func (s *Server) queryUserNodeAuthorizations(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	var request struct {
		NodeType model.AssignableNodeType `json:"node_type"`
		NodeID   int64                    `json:"node_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if err := s.authorizeSubscriptionPlanNode(ctx, principal, planNodeRequest{NodeType: request.NodeType, NodeID: request.NodeID}); err != nil {
		return nil, err
	}
	data, err := s.loadPlanAssignmentData(ctx)
	if err != nil {
		return nil, err
	}
	views := data.nodeAuthorizationViews(request.NodeType, request.NodeID)
	filtered := views[:0]
	for _, view := range views {
		if principal.AllowsInt64("user_ids", view.UserID) {
			filtered = append(filtered, view)
		}
	}
	return map[string]any{
		"node_type": request.NodeType, "node_id": request.NodeID,
		"authorizations": filtered, "count": len(filtered),
		"runtime_authorization_mode": s.authorizationMode(ctx),
	}, nil
}

func (s *Server) prepareUserNodeAuthorizationSet(ctx context.Context, principal application.Principal, input json.RawMessage) (batchUserExceptionRequest, []model.UserNodeException, batchExceptionOutcome, error) {
	var req batchUserExceptionRequest
	if err := strictAutomationInput(input, &req); err != nil {
		return req, nil, batchExceptionOutcome{}, err
	}
	if err := s.validateBatchUserExceptionRequest(ctx, &req); err != nil {
		return req, nil, batchExceptionOutcome{}, err
	}
	if err := s.requireUserMutationsAccess(ctx, principal.Role, req.UserIDs); err != nil {
		return req, nil, batchExceptionOutcome{}, err
	}
	for _, userID := range uniquePositiveIDs(req.UserIDs) {
		if !principal.AllowsInt64("user_ids", userID) {
			return req, nil, batchExceptionOutcome{}, errors.New("user is outside the authorized resource boundary")
		}
	}
	for _, node := range req.Nodes {
		if err := s.authorizeSubscriptionPlanNode(ctx, principal, planNodeRequest{NodeType: node.NodeType, NodeID: node.NodeID}); err != nil {
			return req, nil, batchExceptionOutcome{}, err
		}
	}
	existing, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return req, nil, batchExceptionOutcome{}, err
	}
	outcome, err := s.planBatchUserExceptions(ctx, req, existing)
	return req, existing, outcome, err
}

func (s *Server) applyUserNodeAuthorizationSet(ctx context.Context, actorID *int64, req batchUserExceptionRequest, existing []model.UserNodeException, outcome batchExceptionOutcome) (any, error) {
	changed := append(append([]model.UserNodeException{}, outcome.Created...), outcome.Updated...)
	if len(changed) == 0 {
		return map[string]any{
			"created": 0, "updated": 0, "skipped": len(outcome.Skipped),
			"affected_users": 0, "access_change_id": int64(0), "access_change_status": "none", "queued_tasks": 0,
		}, nil
	}
	writes := make([]store.UserNodeExceptionWrite, 0, len(changed))
	affectedUsers := map[int64]bool{}
	for _, item := range changed {
		writes = append(writes, store.UserNodeExceptionWrite{
			ID: item.ID, UserID: item.UserID, NodeType: item.NodeType, NodeID: item.NodeID,
			Effect: item.Effect, Reason: item.Reason, Status: item.Status,
			StartsAt: item.StartsAt, ExpiresAt: item.ExpiresAt, CreatedBy: actorID,
		})
		affectedUsers[item.UserID] = true
	}
	written, err := s.store.ApplyUserNodeExceptionBatch(ctx, writes)
	if err != nil {
		return nil, err
	}
	after := append([]model.UserNodeException{}, existing...)
	ids := make([]int64, 0, len(written))
	for _, item := range written {
		projectionItem := item
		projectionItem.Status = model.UserNodeExceptionActive
		after = exceptionsWith(after, projectionItem)
		ids = append(ids, item.ID)
	}
	config, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	at := effectiveWindow(time.Now(), req.StartsAt, req.ExpiresAt)
	targetStatus := model.UserNodeExceptionStatus("")
	if req.Effect == model.UserNodeExceptionAllow {
		targetStatus = model.UserNodeExceptionActive
	}
	change, err := s.createExceptionChangesForActor(ctx, nil, actorID, config, existing, after, ids, targetStatus, len(affectedUsers), at)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.store.SetUserNodeExceptionChange(ctx, id, change.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"created": len(outcome.Created), "updated": len(outcome.Updated), "skipped": len(outcome.Skipped),
		"affected_users": len(affectedUsers), "access_change_id": change.ID,
		"access_change_status": change.Status, "queued_tasks": len(change.Targets),
	}, nil
}

func (s *Server) prepareUserNodeAuthorizationRevoke(ctx context.Context, principal application.Principal, input json.RawMessage) ([]model.UserNodeException, error) {
	var request userNodeAuthorizationRevokeRequest
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if !request.Confirm || len(request.AuthorizationIDs) == 0 || len(request.AuthorizationIDs) > 256 {
		return nil, errors.New("authorization_ids and confirm=true are required")
	}
	declaredUsers := map[int64]bool{}
	for _, userID := range request.UserIDs {
		if userID <= 0 {
			return nil, errors.New("user_ids must contain positive integer IDs")
		}
		declaredUsers[userID] = true
	}
	declaredNodes := map[string]bool{}
	for _, node := range request.Nodes {
		if node.NodeID <= 0 {
			return nil, errors.New("nodes must contain valid node references")
		}
		declaredNodes[core.NodeKeyOf(node.NodeType, node.NodeID)] = true
	}
	if len(declaredUsers) == 0 || len(declaredNodes) == 0 {
		return nil, errors.New("user_ids and nodes are required for resource authorization")
	}
	seen := map[int64]bool{}
	matchedUsers := map[int64]bool{}
	matchedNodes := map[string]bool{}
	items := make([]model.UserNodeException, 0, len(request.AuthorizationIDs))
	for _, id := range request.AuthorizationIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		item, err := s.store.GetUserNodeException(ctx, id)
		if err != nil {
			return nil, err
		}
		if item.Status == model.UserNodeExceptionRevoked || item.Status == model.UserNodeExceptionExpired {
			continue
		}
		if !declaredUsers[item.UserID] || !declaredNodes[core.NodeKeyOf(item.NodeType, item.NodeID)] {
			return nil, errors.New("authorization_ids do not match the declared users and nodes")
		}
		if !principal.AllowsInt64("user_ids", item.UserID) {
			return nil, errors.New("user is outside the authorized resource boundary")
		}
		if err := s.requireUserMutationAccess(ctx, principal.Role, item.UserID); err != nil {
			return nil, err
		}
		if err := s.authorizeSubscriptionPlanNode(ctx, principal, planNodeRequest{NodeType: item.NodeType, NodeID: item.NodeID}); err != nil {
			return nil, err
		}
		matchedUsers[item.UserID] = true
		matchedNodes[core.NodeKeyOf(item.NodeType, item.NodeID)] = true
		items = append(items, *item)
	}
	if len(items) > 0 && (len(matchedUsers) != len(declaredUsers) || len(matchedNodes) != len(declaredNodes)) {
		return nil, errors.New("declared users and nodes must exactly match the authorizations being revoked")
	}
	return items, nil
}

func (s *Server) revokeUserNodeAuthorizations(ctx context.Context, actorID *int64, items []model.UserNodeException) (any, error) {
	if len(items) == 0 {
		return map[string]any{"revoking": false, "authorization_ids": []int64{}, "access_change_id": int64(0), "access_change_status": "none", "queued_tasks": 0}, nil
	}
	before, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return nil, err
	}
	after := append([]model.UserNodeException{}, before...)
	ids := authorizationIDs(items)
	users := map[int64]bool{}
	for _, item := range items {
		after = exceptionsWithout(after, item.ID)
		users[item.UserID] = true
	}
	config, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	change, err := s.createExceptionChangesForActor(ctx, nil, actorID, config, before, after, ids, model.UserNodeExceptionRevoked, len(users), time.Now())
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.store.SetUserNodeExceptionChange(ctx, id, change.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"revoking": true, "authorization_ids": ids, "affected_users": len(users),
		"access_change_id": change.ID, "access_change_status": change.Status, "queued_tasks": len(change.Targets),
	}, nil
}

func (s *Server) userNodeAuthorizationSetRevisions(ctx context.Context, req batchUserExceptionRequest, existing []model.UserNodeException) (map[string]string, error) {
	revisions := map[string]string{}
	for _, userID := range uniquePositiveIDs(req.UserIDs) {
		user, err := s.store.GetUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		revisions["user:"+strconv.FormatInt(user.ID, 10)] = user.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	for _, node := range req.Nodes {
		key := core.NodeKeyOf(node.NodeType, node.NodeID)
		revisions[key] = "present"
	}
	wanted := map[string]bool{}
	for _, userID := range uniquePositiveIDs(req.UserIDs) {
		for _, node := range req.Nodes {
			wanted[exceptionKey(userID, node.NodeType, node.NodeID)] = true
		}
	}
	for _, item := range existing {
		if wanted[exceptionKey(item.UserID, item.NodeType, item.NodeID)] {
			revisions["user_node_authorization:"+strconv.FormatInt(item.ID, 10)] = userNodeAuthorizationRevision(item)
		}
	}
	return revisions, nil
}

func userNodeAuthorizationRevision(item model.UserNodeException) string {
	raw, _ := json.Marshal(struct {
		ID        int64                         `json:"id"`
		UserID    int64                         `json:"user_id"`
		NodeType  model.AssignableNodeType      `json:"node_type"`
		NodeID    int64                         `json:"node_id"`
		Effect    model.UserNodeExceptionEffect `json:"effect"`
		Status    model.UserNodeExceptionStatus `json:"status"`
		Reason    string                        `json:"reason"`
		StartsAt  *time.Time                    `json:"starts_at"`
		ExpiresAt *time.Time                    `json:"expires_at"`
	}{item.ID, item.UserID, item.NodeType, item.NodeID, item.Effect, item.Status, item.Reason, item.StartsAt, item.ExpiresAt})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12])
}

func authorizationIDs(items []model.UserNodeException) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func countAuthorizationUsers(items []model.UserNodeException) int {
	users := map[int64]bool{}
	for _, item := range items {
		users[item.UserID] = true
	}
	return len(users)
}
