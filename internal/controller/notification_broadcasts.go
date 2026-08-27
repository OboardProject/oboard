package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type notificationBroadcastFilter struct {
	UserIDs            []int64 `json:"user_ids,omitempty"`
	GroupIDs           []int64 `json:"group_ids,omitempty"`
	PlanIDs            []int64 `json:"plan_ids,omitempty"`
	UserStatus         string  `json:"user_status,omitempty"`
	SubscriptionStatus string  `json:"subscription_status,omitempty"`
	TelegramBound      *bool   `json:"telegram_bound,omitempty"`
}

type notificationBroadcastOperation struct {
	Title          string                      `json:"title"`
	Body           string                      `json:"body"`
	Filter         notificationBroadcastFilter `json:"filter"`
	IdempotencyKey string                      `json:"idempotency_key"`
}

type notificationBroadcastPreview struct {
	Recipients       []store.BroadcastRecipient
	UserIDs          []int64
	RecipientCount   int
	BoundTargetCount int
	UnboundCount     int
}

func (s *Server) registerNotificationBroadcastAutomationOperations() {
	s.automation.RegisterValidator("notification_broadcasts.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, preview, _, err := s.validateNotificationBroadcastOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"title": request.Title, "recipient_count": preview.RecipientCount, "bound_target_count": preview.BoundTargetCount, "unbound_count": preview.UnboundCount, "filter": request.Filter}, nil
	})
	s.automation.RegisterRevisionResolver("notification_broadcasts.create", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("notification_broadcasts.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, preview, filterJSON, err := s.validateNotificationBroadcastOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if principal.UserID == nil {
			return nil, errors.New("notification broadcast requires a user actor")
		}
		actor, err := s.store.GetUser(ctx, *principal.UserID)
		if err != nil {
			return nil, err
		}
		actorName := strings.TrimSpace(actor.Nickname)
		if actorName == "" {
			actorName = actor.Username
		}
		broadcast := model.NotificationBroadcast{ActorUserID: actor.ID, ActorName: actorName, Title: request.Title, Body: request.Body, FilterJSON: filterJSON, IdempotencyKey: request.IdempotencyKey}
		created, err := s.store.CreateNotificationBroadcast(ctx, &broadcast, preview.Recipients)
		if err != nil {
			return nil, err
		}
		if created {
			announcement := model.NotificationAnnouncement{ActorUserID: actor.ID, ActorName: actorName, Title: request.Title, Body: request.Body, UserIDs: preview.UserIDs, QueuedCount: preview.BoundTargetCount}
			if err := s.store.CreateNotificationAnnouncement(ctx, &announcement); err != nil {
				return nil, err
			}
			_ = s.store.AddAudit(ctx, model.AuditLog{ActorID: principal.UserID, Action: "notification_broadcast", Target: "notification_broadcast", Detail: fmt.Sprintf("%d:%d", broadcast.ID, broadcast.RecipientCount), IP: "automation"})
			s.wakeNotificationDelivery(ctx)
			s.publishRealtime("notifications", "user_overview")
		}
		return map[string]any{"broadcast": broadcast}, nil
	})
}

func (s *Server) validateNotificationBroadcastOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (notificationBroadcastOperation, notificationBroadcastPreview, string, error) {
	var request notificationBroadcastOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, notificationBroadcastPreview{}, "", err
	}
	if !model.HasManagementAccess(principal.Role) || principal.UserID == nil {
		return request, notificationBroadcastPreview{}, "", errors.New("notification broadcast requires an administrator")
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Body = strings.TrimSpace(request.Body)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.Title == "" || request.Body == "" || request.IdempotencyKey == "" {
		return request, notificationBroadcastPreview{}, "", errors.New("title, body and idempotency_key are required")
	}
	if len([]rune(request.Title)) > 120 || len([]rune(request.Body)) > 3000 || len(request.IdempotencyKey) > 128 {
		return request, notificationBroadcastPreview{}, "", errors.New("notification broadcast content is too long")
	}
	preview, err := s.resolveNotificationBroadcastRecipients(ctx, *principal.UserID, request.Filter)
	if err != nil {
		return request, notificationBroadcastPreview{}, "", err
	}
	if preview.RecipientCount == 0 {
		return request, preview, "", errors.New("broadcast filter has no recipients")
	}
	filterJSONBytes, _ := json.Marshal(request.Filter)
	return request, preview, string(filterJSONBytes), nil
}

func (s *Server) resolveNotificationBroadcastRecipients(ctx context.Context, actorUserID int64, filter notificationBroadcastFilter) (notificationBroadcastPreview, error) {
	if _, err := s.globalTelegramBot(ctx); err != nil {
		return notificationBroadcastPreview{}, err
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return notificationBroadcastPreview{}, err
	}
	members, err := s.store.ListUserGroupMembers(ctx)
	if err != nil {
		return notificationBroadcastPreview{}, err
	}
	bindings, err := s.store.ListActiveUserPlanBindings(ctx)
	if err != nil {
		return notificationBroadcastPreview{}, err
	}
	userFilter := int64Set(filter.UserIDs)
	groupFilter := int64Set(filter.GroupIDs)
	planFilter := int64Set(filter.PlanIDs)
	userGroups := map[int64]map[int64]bool{}
	for _, member := range members {
		if !member.Enabled {
			continue
		}
		if userGroups[member.UserID] == nil {
			userGroups[member.UserID] = map[int64]bool{}
		}
		userGroups[member.UserID][member.GroupID] = true
	}
	userPlans := map[int64]map[int64]bool{}
	activeSubscription := map[int64]bool{}
	nowTime := time.Now().UTC()
	for _, binding := range bindings {
		if binding.StartsAt != nil && binding.StartsAt.After(nowTime) || binding.ExpiresAt != nil && !binding.ExpiresAt.After(nowTime) {
			continue
		}
		activeSubscription[binding.UserID] = true
		if userPlans[binding.UserID] == nil {
			userPlans[binding.UserID] = map[int64]bool{}
		}
		userPlans[binding.UserID][binding.PlanID] = true
	}
	preview := notificationBroadcastPreview{}
	for _, user := range users {
		if user.ID == actorUserID {
			continue
		}
		if len(userFilter) > 0 && !userFilter[user.ID] || filter.UserStatus != "" && user.Status != filter.UserStatus {
			continue
		}
		if len(groupFilter) > 0 && !setsIntersect(groupFilter, userGroups[user.ID]) || len(planFilter) > 0 && !setsIntersect(planFilter, userPlans[user.ID]) {
			continue
		}
		if filter.SubscriptionStatus == "active" && !activeSubscription[user.ID] || filter.SubscriptionStatus == "inactive" && activeSubscription[user.ID] {
			continue
		}
		userBindings, err := s.store.ListTelegramBindingsForUser(ctx, user.ID)
		if err != nil {
			return notificationBroadcastPreview{}, err
		}
		eligible := userBindings
		bound := len(eligible) > 0
		if filter.TelegramBound != nil && bound != *filter.TelegramBound {
			continue
		}
		preview.Recipients = append(preview.Recipients, store.BroadcastRecipient{UserID: user.ID, Bindings: eligible})
		preview.UserIDs = append(preview.UserIDs, user.ID)
		preview.RecipientCount++
		preview.BoundTargetCount += len(eligible)
		if !bound {
			preview.UnboundCount++
		}
	}
	sort.Slice(preview.Recipients, func(i, j int) bool { return preview.Recipients[i].UserID < preview.Recipients[j].UserID })
	sort.Slice(preview.UserIDs, func(i, j int) bool { return preview.UserIDs[i] < preview.UserIDs[j] })
	return preview, nil
}

func int64Set(values []int64) map[int64]bool {
	out := map[int64]bool{}
	for _, value := range values {
		if value > 0 {
			out[value] = true
		}
	}
	return out
}

func setsIntersect(left, right map[int64]bool) bool {
	for value := range left {
		if right[value] {
			return true
		}
	}
	return false
}

func (s *Server) deliverPendingTelegramBroadcasts(ctx context.Context) {
	targets, err := s.store.ListPendingNotificationBroadcastTargets(ctx, time.Now().UTC(), 50)
	if err != nil {
		s.logPeriodicError("pending-telegram-broadcasts", "list pending Telegram broadcasts: %v", err)
		return
	}
	bot, botErr := s.globalTelegramBot(ctx)
	for _, target := range targets {
		var sendErr error
		user, userErr := s.store.GetUser(ctx, target.UserID)
		bindingActive := false
		if target.BindingID != nil {
			bindingActive, _ = s.store.TelegramBindingActive(ctx, *target.BindingID, target.UserID)
		}
		if userErr != nil || user.Status != "active" {
			sendErr = errors.New("broadcast_recipient_inactive")
		} else if !bindingActive {
			sendErr = errors.New("telegram_binding_revoked")
		} else if botErr != nil || target.ChannelID == nil || target.ChatID == nil {
			sendErr = errors.New("telegram_binding_or_bot_unavailable")
		} else {
			_, sendErr = s.telegramIncidentSend(ctx, bot.botToken, *target.ChatID, target.Broadcast.Title+"\n"+target.Broadcast.Body)
		}
		retry := time.Now().UTC().Add(time.Minute)
		if target.Attempts > 0 {
			retry = time.Now().UTC().Add(5 * time.Minute)
		}
		if err := s.store.CompleteNotificationBroadcastTarget(ctx, target.ID, sendErr, retry); err != nil {
			log.Printf("complete Telegram broadcast target %d: %v", target.ID, err)
		}
	}
}
