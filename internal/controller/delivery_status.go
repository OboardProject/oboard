package controller

import (
	"context"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) attachAccessChangeDelivery(ctx context.Context, result map[string]any, change *model.AccessChange) map[string]any {
	if result == nil {
		result = map[string]any{}
	}
	view := s.accessChangeDeliveryView(ctx, change)
	for key, value := range view {
		result[key] = value
	}
	return result
}

func (s *Server) attachDeliveryCompletion(ctx context.Context, result map[string]any, userID, changeID int64) map[string]any {
	return s.attachDeliveryCompletionOn(ctx, result, userID, changeID, nil)
}

func (s *Server) attachDeliveryCompletionOn(ctx context.Context, result map[string]any, userID, changeID int64, serverIDs []int64) map[string]any {
	if result == nil {
		result = map[string]any{}
	}
	result["change_id"] = changeID
	status := s.deliveryCompletionForUser(ctx, userID)
	if serverIDs != nil {
		status = s.deliveryCompletionForServers(ctx, serverIDs)
	}
	result["pending_servers"] = status.pendingServers
	result["completion"] = status.completion
	return result
}

type deliveryCompletion struct {
	pendingServers []int64
	completion     string
}

func (s *Server) deliveryCompletionForUser(ctx context.Context, userID int64) deliveryCompletion {
	if userID <= 0 {
		return deliveryCompletion{pendingServers: []int64{}, completion: "confirmed"}
	}
	serverIDs, err := s.userAccountingServerIDs(ctx, userID)
	if err != nil {
		return deliveryCompletion{pendingServers: []int64{}, completion: "pending"}
	}
	return s.deliveryCompletionForServers(ctx, serverIDs)
}

func (s *Server) deliveryCompletionForServers(ctx context.Context, serverIDs []int64) deliveryCompletion {
	pending := make([]int64, 0)
	completion := "confirmed"
	for _, serverID := range serverIDs {
		if serverID <= 0 {
			continue
		}
		auth, _ := s.store.AuthorizationState(ctx, serverID)
		users, _ := s.store.RuntimeUserState(ctx, serverID)
		if lanePending(auth, users) {
			pending = append(pending, serverID)
			next := laneCompletion(auth, users)
			if completionRank(next) > completionRank(completion) {
				completion = next
			}
		}
	}
	if len(pending) == 0 {
		return deliveryCompletion{pendingServers: []int64{}, completion: "confirmed"}
	}
	return deliveryCompletion{pendingServers: pending, completion: completion}
}

func lanePending(auth store.AuthorizationState, users store.RuntimeUserState) bool {
	authPending := auth.DesiredRevision > 0 && !auth.Confirmed()
	usersPending := users.DesiredRevision > 0 && !users.Confirmed() &&
		users.PendingReason != store.RuntimeUsersPendingAgentUpgrade &&
		users.PendingReason != store.RuntimeUsersPendingCoreConfigFallback
	return authPending || usersPending
}

func laneCompletion(auth store.AuthorizationState, users store.RuntimeUserState) string {
	if auth.PendingReason == store.AuthorizationPendingAgentUpgrade || users.PendingReason == store.RuntimeUsersPendingAgentUpgrade {
		return "upgrade_required"
	}
	if auth.LastError != "" && !auth.Retryable || users.LastError != "" && !users.Retryable {
		return "failed"
	}
	if auth.PendingReason == store.AuthorizationPendingAgentOffline || users.PendingReason == store.RuntimeUsersPendingAgentOffline {
		return "waiting_offline"
	}
	if !auth.Retryable && auth.LastError != "" || !users.Retryable && users.LastError != "" {
		return "failed"
	}
	return "pending"
}

func completionRank(value string) int {
	switch value {
	case "failed":
		return 4
	case "upgrade_required":
		return 3
	case "waiting_offline":
		return 2
	case "pending":
		return 1
	default:
		return 0
	}
}

func (s *Server) attachLaneFields(ctx context.Context, item map[string]any, serverID int64) {
	if item == nil || serverID <= 0 {
		return
	}
	auth, _ := s.store.AuthorizationState(ctx, serverID)
	users, _ := s.store.RuntimeUserState(ctx, serverID)
	attachLaneFieldsFromStates(item, auth, users)
}

func (s *Server) attachLaneFieldsToSyncViews(ctx context.Context, views []map[string]any, states []store.ConfigurationSyncState) {
	s.attachLaneFieldsFromLaneStates(ctx, views, states, s.loadServerDeliveryLaneStates(ctx))
}

func (s *Server) attachLaneFieldsFromLaneStates(ctx context.Context, views []map[string]any, states []store.ConfigurationSyncState, lanes serverDeliveryLaneStates) {
	if len(views) == 0 {
		return
	}
	if !lanes.ok {
		for i := range views {
			if i < len(states) {
				s.attachLaneFields(ctx, views[i], states[i].ServerID)
			}
		}
		return
	}
	for i := range views {
		if i >= len(states) {
			break
		}
		auth := lanes.auth[states[i].ServerID]
		if auth.ServerID == 0 {
			auth = store.AuthorizationState{ServerID: states[i].ServerID, Retryable: true}
		}
		userState := lanes.users[states[i].ServerID]
		if userState.ServerID == 0 {
			userState = store.RuntimeUserState{ServerID: states[i].ServerID, Retryable: true}
		}
		attachLaneFieldsFromStates(views[i], auth, userState)
	}
}

func attachLaneFieldsFromStates(item map[string]any, auth store.AuthorizationState, users store.RuntimeUserState) {
	if item == nil {
		return
	}
	item["desired_state"] = laneDesiredState(auth, users)
	item["effective_state"] = laneEffectiveState(auth, users)
	item["pending_reason"] = firstNonEmpty(auth.PendingReason, users.PendingReason)
	item["applied_authorization_revision"] = auth.ConfirmedRevision
	item["applied_users_revision"] = users.ConfirmedRevision
	item["last_error"] = firstNonEmpty(auth.LastError, users.LastError)
	item["retryable"] = auth.Retryable || users.Retryable
	if auth.LastIssuedExpiresAt != nil {
		item["lease_valid_until"] = auth.LastIssuedExpiresAt.UTC().Format(time.RFC3339Nano)
	}
}

func laneDesiredState(auth store.AuthorizationState, users store.RuntimeUserState) string {
	if auth.DesiredRevision == 0 && users.DesiredRevision == 0 {
		return "none"
	}
	if lanePending(auth, users) {
		return "pending"
	}
	return "applied"
}

func laneEffectiveState(auth store.AuthorizationState, users store.RuntimeUserState) string {
	if auth.Confirmed() && (users.DesiredRevision == 0 || users.Confirmed() || users.PendingReason == store.RuntimeUsersPendingAgentUpgrade || users.PendingReason == store.RuntimeUsersPendingCoreConfigFallback) {
		return "confirmed"
	}
	if auth.ConfirmedRevision > 0 || users.ConfirmedRevision > 0 {
		return "partial"
	}
	return "unconfirmed"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) accessChangeDeliveryView(ctx context.Context, change *model.AccessChange) map[string]any {
	if change == nil {
		return map[string]any{"change_id": 0, "retryable": false, "pending_servers": []int64{}, "completion": "confirmed"}
	}
	out := map[string]any{
		"change_id":        change.ID,
		"retryable":        change.Status == model.AccessChangeFailed,
		"pending_servers":  []int64{},
		"completion":       "confirmed",
	}
	targets, err := s.store.ListAccessChangeTargets(ctx, change.ID)
	if err != nil || len(targets) == 0 {
		return out
	}
	serverIDs := make([]int64, 0, len(targets))
	for _, target := range targets {
		serverIDs = append(serverIDs, target.ServerID)
	}
	status := s.deliveryCompletionForServers(ctx, serverIDs)
	out["pending_servers"] = status.pendingServers
	out["completion"] = status.completion
	if len(serverIDs) == 1 {
		s.attachLaneFields(ctx, out, serverIDs[0])
	} else if len(status.pendingServers) == 1 {
		s.attachLaneFields(ctx, out, status.pendingServers[0])
	} else if len(serverIDs) > 0 {
		s.attachLaneFields(ctx, out, serverIDs[0])
	}
	return out
}
