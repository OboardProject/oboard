package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
)

const (
	maxNotifyUsers     = 64
	maxPlanNotifyUsers = 256
)

func (s *Service) callerAllowsResource(ctx context.Context, run model.PluginRun, resource string, id int64) bool {
	if !operatorTriggered(run.Trigger) {
		return true
	}
	var detail TriggerDetail
	if json.Unmarshal(run.TriggerJSON, &detail) != nil || detail.Caller == nil {
		return false
	}
	caller, err := s.host.ResolveCaller(ctx, *detail.Caller)
	return err == nil && caller.AllowsInt64(resource, id)
}

func (s *Service) callerAllowsServer(ctx context.Context, run model.PluginRun, serverID int64) bool {
	return s.callerAllowsResource(ctx, run, "server_ids", serverID)
}

func userView(user UserInfo) map[string]any {
	return map[string]any{"user_id": strconv.FormatInt(user.ID, 10), "username": user.Username, "nickname": user.Nickname, "status": user.Status}
}

func planView(plan PlanInfo) map[string]any {
	return map[string]any{"plan_id": strconv.FormatInt(plan.ID, 10), "name": plan.Name, "enabled": plan.Enabled}
}

func (s *Service) handleUsers(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	if call.method == "users.list" {
		if err := decodeArgs(raw, &struct{}{}); err != nil {
			return nil, "", nil, err
		}
		out := []map[string]any{}
		for _, id := range call.grant.Capabilities[CapUsersRead].Users {
			if !s.callerAllowsResource(ctx, call.run, "user_ids", id) {
				continue
			}
			if user, ok := s.host.User(ctx, id); ok {
				out = append(out, userView(user))
			}
		}
		return map[string]any{"users": out}, "", nil, nil
	}
	var args struct {
		UserID string `json:"user_id"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	id, err := parseServerArg(args.UserID)
	if err != nil {
		return nil, "", nil, Fail(CodeInvalidArgument, "user_id must be a stable user ID string")
	}
	if !call.grant.AllowsUser(CapUsersRead, id) || !s.callerAllowsResource(ctx, call.run, "user_ids", id) {
		return nil, "user:" + args.UserID, nil, Fail(CodeResourceDenied, "user "+args.UserID+" is outside the granted scope")
	}
	user, ok := s.host.User(ctx, id)
	if !ok {
		return nil, "user:" + args.UserID, nil, Fail(CodeNotFound, "user "+args.UserID+" no longer exists")
	}
	return userView(user), "user:" + args.UserID, nil, nil
}

func (s *Service) handlePlans(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	if call.method == "plans.list" {
		if err := decodeArgs(raw, &struct{}{}); err != nil {
			return nil, "", nil, err
		}
		out := []map[string]any{}
		for _, id := range call.grant.Capabilities[CapPlansRead].Plans {
			if !s.callerAllowsResource(ctx, call.run, "subscription_plan_ids", id) {
				continue
			}
			if plan, ok := s.host.Plan(ctx, id); ok {
				out = append(out, planView(plan))
			}
		}
		return map[string]any{"plans": out}, "", nil, nil
	}
	var args struct {
		PlanID string `json:"plan_id"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	id, err := parseServerArg(args.PlanID)
	if err != nil {
		return nil, "", nil, Fail(CodeInvalidArgument, "plan_id must be a stable plan ID string")
	}
	if !call.grant.AllowsPlan(CapPlansRead, id) || !s.callerAllowsResource(ctx, call.run, "subscription_plan_ids", id) {
		return nil, "plan:" + args.PlanID, nil, Fail(CodeResourceDenied, "plan "+args.PlanID+" is outside the granted scope")
	}
	if _, ok := s.host.Plan(ctx, id); !ok {
		return nil, "plan:" + args.PlanID, nil, Fail(CodeNotFound, "plan "+args.PlanID+" no longer exists")
	}
	users, err := s.planAudience(ctx, call, id)
	if err != nil {
		return nil, "plan:" + args.PlanID, nil, err
	}
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		out = append(out, userView(user))
	}
	return map[string]any{"users": out}, "plan:" + args.PlanID, nil, nil
}

func (s *Service) handleUserNotify(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var args struct {
		UserIDs []string `json:"user_ids"`
		Title   string   `json:"title"`
		Body    string   `json:"body"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	if len(args.UserIDs) == 0 || len(args.UserIDs) > maxNotifyUsers {
		return nil, "", nil, Fail(CodeInvalidArgument, "user_ids must contain 1 to 64 users")
	}
	title, body, err := notifyText(ctx, s, call, args.Title, args.Body)
	if err != nil {
		return nil, "", nil, err
	}
	ids := make([]int64, 0, len(args.UserIDs))
	seen := map[int64]bool{}
	for _, rawID := range args.UserIDs {
		id, err := parseServerArg(rawID)
		if err != nil {
			return nil, "", nil, Fail(CodeInvalidArgument, "user_ids must be stable user ID strings")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if !call.grant.AllowsUser(CapUsersNotify, id) || !s.callerAllowsResource(ctx, call.run, "user_ids", id) {
			return nil, "user:" + rawID, nil, Fail(CodeResourceDenied, "user "+rawID+" is outside the granted scope")
		}
		if _, ok := s.host.User(ctx, id); !ok {
			return nil, "user:" + rawID, nil, Fail(CodeNotFound, "user "+rawID+" no longer exists")
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result, err := s.queueUserNotify(ctx, call, "users.notify", title, body, ids)
	return result, "users", map[string]any{"users": len(ids)}, err
}

func (s *Service) handlePlanNotify(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var args struct {
		PlanID string `json:"plan_id"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	id, err := parseServerArg(args.PlanID)
	if err != nil {
		return nil, "", nil, Fail(CodeInvalidArgument, "plan_id must be a stable plan ID string")
	}
	if !call.grant.AllowsPlan(CapPlansNotify, id) || !s.callerAllowsResource(ctx, call.run, "subscription_plan_ids", id) {
		return nil, "plan:" + args.PlanID, nil, Fail(CodeResourceDenied, "plan "+args.PlanID+" is outside the granted scope")
	}
	if _, ok := s.host.Plan(ctx, id); !ok {
		return nil, "plan:" + args.PlanID, nil, Fail(CodeNotFound, "plan "+args.PlanID+" no longer exists")
	}
	title, body, err := notifyText(ctx, s, call, args.Title, args.Body)
	if err != nil {
		return nil, "plan:" + args.PlanID, nil, err
	}
	users, err := s.planAudience(ctx, call, id)
	if err != nil {
		return nil, "plan:" + args.PlanID, nil, err
	}
	if len(users) == 0 {
		return nil, "plan:" + args.PlanID, nil, Fail(CodeResourceDenied, "the plan has no users inside the granted scope")
	}
	if len(users) > maxPlanNotifyUsers {
		return nil, "plan:" + args.PlanID, nil, Fail(CodeLimitExceeded, "the plan has more users than one notification can address")
	}
	ids := make([]int64, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result, err := s.queueUserNotify(ctx, call, "plans.notify", title, body, ids)
	return result, "plan:" + args.PlanID, map[string]any{"users": len(ids)}, err
}

func (s *Service) planAudience(ctx context.Context, call callContext, planID int64) ([]UserInfo, error) {
	users, err := s.host.UsersOnPlan(ctx, planID)
	if err != nil {
		return nil, Fail(CodeOperationFailed, "failed to read plan users")
	}
	out := make([]UserInfo, 0, len(users))
	for _, user := range users {
		if !s.callerAllowsResource(ctx, call.run, "user_ids", user.ID) {
			continue
		}
		out = append(out, user)
	}
	return out, nil
}

func notifyText(ctx context.Context, s *Service, call callContext, title, body string) (string, string, error) {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if title == "" || utf8.RuneCountInString(title) > 120 || utf8.RuneCountInString(body) > 3000 || hasControl(title, false) || hasControl(body, true) {
		return "", "", Fail(CodeInvalidArgument, "title is required (at most 120 characters) and body is at most 3000 characters")
	}
	secrets := s.instanceSecretValues(ctx, call.instance.ID)
	return pluginhttp.Redact(title, secrets), pluginhttp.Redact(body, secrets), nil
}

func (s *Service) queueUserNotify(ctx context.Context, call callContext, method, title, body string, userIDs []int64) (map[string]any, error) {
	record, err := s.store.GetPluginGrant(ctx, call.instance.ID)
	if err != nil || record.ApprovedByUserID <= 0 {
		return nil, Fail(CodeOperationFailed, "user notification requires the administrator who approved this grant")
	}
	raw, _ := json.Marshal(struct {
		Method string  `json:"method"`
		Users  []int64 `json:"users"`
		Title  string  `json:"title"`
		Body   string  `json:"body"`
	}{Method: method, Users: userIDs, Title: title, Body: body})
	sum := sha256.Sum256(raw)
	key := "plugin:" + call.run.UUID + ":" + hex.EncodeToString(sum[:8])
	result, err := s.host.NotifyUsers(ctx, UserNotifyRequest{ActorUserID: record.ApprovedByUserID, Title: title, Body: body, UserIDs: userIDs, IdempotencyKey: key})
	if err != nil {
		if coded, ok := err.(*Error); ok {
			return nil, coded
		}
		return nil, Fail(CodeOperationFailed, "user notification could not be queued")
	}
	return map[string]any{"recipients": result.Recipients, "queued": result.Queued, "unbound": result.Unbound}, nil
}
