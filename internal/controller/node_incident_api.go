package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type nodeIncidentActionRequest struct {
	Action         string  `json:"action"`
	InboundIDs     []int64 `json:"inbound_ids"`
	RecoveryPolicy string  `json:"recovery_policy"`
}

type nodeIncidentConfirmationPayload struct {
	EventID        int64   `json:"event_id"`
	EventVersion   int64   `json:"event_version"`
	Action         string  `json:"action"`
	InboundIDs     []int64 `json:"inbound_ids"`
	RecoveryPolicy string  `json:"recovery_policy,omitempty"`
	ChatID         int64   `json:"chat_id,omitempty"`
	TelegramUserID int64   `json:"telegram_user_id,omitempty"`
}

func (s *Server) apiV1NodeIncidents(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if !roleAllows(principal.Role, model.RoleOperator) {
		v2Error(w, r, http.StatusForbidden, "capability_denied", "当前角色不能查看或处置节点运维事件")
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/node-incidents/")
	if r.URL.Path == "/api/v1/node-incidents" {
		parts = nil
	}
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
			return
		}
		if _, allowed := s.capabilities.Authorize(principal, "node_incidents.list"); !allowed {
			v2Error(w, r, http.StatusForbidden, "capability_denied", "缺少节点事件查看能力")
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if status != "" && status != string(model.NodeIncidentActive) && status != string(model.NodeIncidentRecovering) && status != string(model.NodeIncidentResolved) {
			v2Error(w, r, http.StatusBadRequest, "invalid_status", "事件状态无效")
			return
		}
		items, err := s.store.ListNodeIncidents(r.Context(), status, queryLimit(r, 50), intQuery(r, "offset", 0))
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		filtered := items[:0]
		for _, item := range items {
			if principal.AllowsInt64("server_ids", item.ServerID) {
				filtered = append(filtered, item)
			}
		}
		v2Write(w, r, http.StatusOK, map[string]any{"events": filtered}, map[string]any{"count": len(filtered)})
		return
	}
	eventID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || eventID <= 0 {
		v2Error(w, r, http.StatusBadRequest, "invalid_event", "事件 ID 无效")
		return
	}
	event, err := s.store.GetNodeIncident(r.Context(), eventID)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if !principal.AllowsInt64("server_ids", event.ServerID) {
		v2Error(w, r, http.StatusForbidden, "resource_denied", "事件不在授权服务器范围内")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		isolations, listErr := s.store.ListNodePublicationIsolations(r.Context(), event.ID)
		if listErr != nil {
			v2HandleError(w, r, listErr)
			return
		}
		actions, listErr := s.store.ListNodeIncidentActions(r.Context(), event.ID)
		if listErr != nil {
			v2HandleError(w, r, listErr)
			return
		}
		v2Write(w, r, http.StatusOK, map[string]any{"event": event, "isolations": isolations, "actions": actions}, nil)
		return
	}
	if len(parts) == 2 && parts[1] == "preview" && r.Method == http.MethodPost {
		s.apiV1NodeIncidentPreview(w, r, principal, *event)
		return
	}
	if len(parts) == 2 && parts[1] == "confirm" && r.Method == http.MethodPost {
		s.apiV1NodeIncidentConfirm(w, r, principal, *event)
		return
	}
	v2Error(w, r, http.StatusNotFound, "not_found", "节点事件操作不存在")
}

func (s *Server) apiV1NodeIncidentPreview(w http.ResponseWriter, r *http.Request, principal application.Principal, event model.NodeIncident) {
	if principal.UserID == nil || !principal.Interactive {
		v2Error(w, r, http.StatusForbidden, "interactive_required", "节点处置需要人工登录")
		return
	}
	if event.Status == model.NodeIncidentResolved {
		v2Error(w, r, http.StatusConflict, "event_resolved", "事件已关闭，故障期操作已禁用")
		return
	}
	var request nodeIncidentActionRequest
	if !decodeV2(w, r, &request) {
		return
	}
	request.Action = strings.TrimSpace(request.Action)
	capabilityName := "node_incidents.isolate"
	if request.Action == "permanent_remove" {
		capabilityName = "inbounds.delete"
	} else if request.Action != "isolate" {
		v2Error(w, r, http.StatusBadRequest, "invalid_action", "处置类型无效")
		return
	}
	if _, allowed := s.capabilities.Authorize(principal, capabilityName); !allowed {
		v2Error(w, r, http.StatusForbidden, "capability_denied", "当前角色没有此节点处置能力")
		return
	}
	if request.Action == "isolate" && request.RecoveryPolicy != "manual" && request.RecoveryPolicy != "auto" {
		v2Error(w, r, http.StatusBadRequest, "invalid_recovery_policy", "恢复策略必须是 manual 或 auto")
		return
	}
	preview, err := s.nodeIncidentImpactPreview(r.Context(), event, request.InboundIDs, request.Action, request.RecoveryPolicy)
	if err != nil {
		v2Error(w, r, http.StatusBadRequest, "invalid_selection", err.Error())
		return
	}
	payload := nodeIncidentConfirmationPayload{EventID: event.ID, EventVersion: event.Version, Action: request.Action, InboundIDs: preview["inbound_ids"].([]int64), RecoveryPolicy: request.RecoveryPolicy}
	payloadJSON, _ := json.Marshal(payload)
	token, err := security.RandomToken(24)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	if err := s.store.CreateOperationConfirmation(r.Context(), security.HashSecret(token), capabilityName, event.ID, event.Version, *principal.UserID, string(payloadJSON), expiresAt); err != nil {
		v2HandleError(w, r, err)
		return
	}
	preview["confirmation_token"] = token
	preview["expires_at"] = expiresAt
	preview["event_version"] = event.Version
	v2Write(w, r, http.StatusOK, preview, nil)
}

func (s *Server) apiV1NodeIncidentConfirm(w http.ResponseWriter, r *http.Request, principal application.Principal, event model.NodeIncident) {
	if principal.UserID == nil || !principal.Interactive {
		v2Error(w, r, http.StatusForbidden, "interactive_required", "节点处置需要人工登录")
		return
	}
	var request struct {
		Token string `json:"confirmation_token"`
	}
	if !decodeV2(w, r, &request) {
		return
	}
	if event.Status == model.NodeIncidentResolved {
		v2Error(w, r, http.StatusConflict, "event_resolved", "事件已关闭，旧确认按钮不能执行")
		return
	}
	var payload nodeIncidentConfirmationPayload
	var raw string
	var err error
	for _, capabilityName := range []string{"node_incidents.isolate", "inbounds.delete"} {
		raw, err = s.store.ConsumeOperationConfirmation(r.Context(), security.HashSecret(strings.TrimSpace(request.Token)), capabilityName, event.ID, event.Version, *principal.UserID, time.Now().UTC())
		if err == nil {
			break
		}
	}
	if err != nil || json.Unmarshal([]byte(raw), &payload) != nil || payload.EventID != event.ID || payload.EventVersion != event.Version {
		v2Error(w, r, http.StatusConflict, "confirmation_invalid", "确认令牌无效、已使用、已过期或事件版本已变化")
		return
	}
	capabilityName := "node_incidents.isolate"
	operations := []automation.OperationRequest{}
	if payload.Action == "isolate" {
		input, _ := json.Marshal(nodeIncidentIsolationOperation{EventID: event.ID, EventVersion: event.Version, InboundIDs: payload.InboundIDs, RecoveryPolicy: payload.RecoveryPolicy})
		operations = append(operations, automation.OperationRequest{Capability: capabilityName, Input: input, ResourceRefs: json.RawMessage(`{}`)})
	} else if payload.Action == "permanent_remove" {
		capabilityName = "inbounds.delete"
		for _, inboundID := range payload.InboundIDs {
			input, _ := json.Marshal(map[string]any{"inbound_id": inboundID, "confirm": true})
			operations = append(operations, automation.OperationRequest{Capability: capabilityName, Input: input, ResourceRefs: json.RawMessage(`{}`)})
		}
	} else {
		v2Error(w, r, http.StatusBadRequest, "invalid_action", "确认内容无效")
		return
	}
	if _, allowed := s.capabilities.Authorize(principal, capabilityName); !allowed {
		v2Error(w, r, http.StatusForbidden, "capability_denied", "当前权限已变化，操作被拒绝")
		return
	}
	changeset, err := s.applyConfirmedNodeChangeset(r.Context(), principal, operations, security.HashSecret(request.Token))
	if err != nil {
		v2Error(w, r, http.StatusConflict, "changeset_failed", err.Error())
		return
	}
	response := map[string]any{"changeset": changeset, "deployment_required": false}
	if payload.Action == "permanent_remove" {
		if err := s.store.MarkNodePublicationIsolationsRemoved(r.Context(), payload.InboundIDs, *principal.UserID); err != nil {
			v2HandleError(w, r, err)
			return
		}
		tasks, version, deployErr := s.deployConfiguration(r.Context(), 0, true)
		idsJSON, _ := json.Marshal(payload.InboundIDs)
		action := model.NodeIncidentAction{IncidentID: event.ID, ActorUserID: *principal.UserID, Kind: "permanent_remove", Status: "deployment_pending", InboundIDsJSON: string(idsJSON), ChangesetID: changeset.ID, ConfigVersion: version, TaskCount: len(tasks)}
		if deployErr != nil {
			action.Status = "failed"
			action.Error = deployErr.Error()
			_ = s.store.CreateNodeIncidentAction(r.Context(), &action)
			v2Error(w, r, http.StatusConflict, "deployment_failed", deployErr.Error())
			return
		}
		if err := s.store.CreateNodeIncidentAction(r.Context(), &action); err != nil {
			v2HandleError(w, r, err)
			return
		}
		response["deployment_required"] = true
		response["action"] = action
		response["deployment"] = map[string]any{"config_version": version, "task_count": len(tasks), "status": "queued"}
	}
	s.publishRealtime("node_incidents", "subscriptions", "topology", "tasks")
	v2Write(w, r, http.StatusAccepted, response, nil)
}

func (s *Server) applyConfirmedNodeChangeset(ctx context.Context, principal application.Principal, operations []automation.OperationRequest, key string) (*model.AutomationChangeset, error) {
	item, err := s.automation.Create(ctx, principal, automation.CreateRequest{Reason: "节点失联事件确认处置", IdempotencyKey: "node-incident:" + key, BaseRevisions: json.RawMessage(`{}`), Operations: operations})
	if err != nil {
		return nil, err
	}
	item, err = s.automation.Validate(ctx, principal, item.ID)
	if err != nil {
		return nil, err
	}
	if item.Status == model.ChangesetAwaitingApproval {
		item, err = s.automation.Approve(ctx, principal, item.ID, "短时一次性确认令牌已验证")
		if err != nil {
			return nil, err
		}
	}
	if item.Status == model.ChangesetApproved {
		item, err = s.automation.Apply(ctx, principal, item.ID)
	}
	return item, err
}

func (s *Server) nodeIncidentImpactPreview(ctx context.Context, event model.NodeIncident, inboundIDs []int64, action, recoveryPolicy string) (map[string]any, error) {
	published := s.nodeIncidentPublishedInbounds(event)
	allowed := map[int64]nodeIncidentSnapshotInbound{}
	for _, inbound := range published {
		allowed[inbound.ID] = inbound
	}
	selected := []nodeIncidentSnapshotInbound{}
	seen := map[int64]bool{}
	for _, inboundID := range inboundIDs {
		if inboundID <= 0 || seen[inboundID] {
			continue
		}
		inbound, ok := allowed[inboundID]
		if !ok {
			return nil, fmt.Errorf("入口 #%d 不在事件影响快照的已发布节点中", inboundID)
		}
		current, err := s.store.GetInbound(ctx, inboundID)
		if err != nil || current.ServerID != event.ServerID {
			return nil, fmt.Errorf("入口 #%d 已不存在或不再属于该服务器", inboundID)
		}
		seen[inboundID] = true
		selected = append(selected, inbound)
	}
	if len(selected) == 0 {
		return nil, errors.New("至少选择一个实际发布的入口")
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	planSet := map[int64]bool{}
	planNames := map[int64]string{}
	exceptions := 0
	ids := make([]int64, 0, len(selected))
	for _, inbound := range selected {
		ids = append(ids, inbound.ID)
		for index, planID := range inbound.PlanIDs {
			planSet[planID] = true
			if index < len(inbound.PlanNames) {
				planNames[planID] = inbound.PlanNames[index]
			}
		}
		refs, err := s.store.PlanNodeReferences(ctx, model.AssignableNodeInbound, inbound.ID)
		if err != nil {
			return nil, err
		}
		exceptions += refs.Exceptions
		for _, ref := range refs.Active {
			planSet[ref.PlanID] = true
			planNames[ref.PlanID] = ref.Name
		}
	}
	bindings, err := s.store.ListActiveUserPlanBindings(ctx)
	if err != nil {
		return nil, err
	}
	users := map[int64]bool{}
	for _, binding := range bindings {
		if planSet[binding.PlanID] {
			users[binding.UserID] = true
		}
	}
	plans := make([]string, 0, len(planNames))
	for _, name := range planNames {
		plans = append(plans, name)
	}
	sort.Strings(plans)
	return map[string]any{
		"event_id": event.ID, "action": action, "recovery_policy": recoveryPolicy,
		"inbound_ids": ids, "nodes": selected, "affected_plan_count": len(planSet),
		"affected_plan_names": plans, "affected_user_count": len(users), "exception_count": exceptions,
		"deployment_required": action == "permanent_remove", "existing_connections_affected": action == "permanent_remove",
	}, nil
}

func (s *Server) apiV1NotificationBroadcasts(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if !model.HasManagementAccess(principal.Role) || principal.UserID == nil || !principal.Interactive {
		v2Error(w, r, http.StatusForbidden, "capability_denied", "管理员广播需要当前管理员账户")
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notification-broadcasts") {
		items, err := s.store.ListNotificationBroadcasts(r.Context(), queryLimit(r, 20))
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, map[string]any{"broadcasts": items}, nil)
		return
	}
	if r.Method != http.MethodPost {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	actionPath := strings.TrimPrefix(r.URL.Path, "/api/v1/notification-broadcasts/")
	if actionPath == r.URL.Path {
		actionPath = strings.TrimPrefix(r.URL.Path, "/api/v1/ui/notification-broadcasts/")
	}
	if actionPath == r.URL.Path {
		actionPath = strings.TrimPrefix(r.URL.Path, "/api/v1/notification-broadcasts/")
	}
	switch actionPath {
	case "preview":
		var request notificationBroadcastOperation
		if !decodeV2(w, r, &request) {
			return
		}
		if strings.TrimSpace(request.IdempotencyKey) == "" {
			random, _ := security.RandomToken(16)
			request.IdempotencyKey = "web:" + random
		}
		raw, _ := json.Marshal(request)
		_, preview, _, err := s.validateNotificationBroadcastOperation(r.Context(), principal, raw)
		if err != nil {
			v2Error(w, r, http.StatusBadRequest, "invalid_broadcast", err.Error())
			return
		}
		token, err := security.RandomToken(24)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		expiresAt := time.Now().UTC().Add(5 * time.Minute)
		if err := s.store.CreateOperationConfirmation(r.Context(), security.HashSecret(token), "notification_broadcasts.create", 0, 0, *principal.UserID, string(raw), expiresAt); err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, map[string]any{"recipient_count": preview.RecipientCount, "bound_target_count": preview.BoundTargetCount, "unbound_count": preview.UnboundCount, "confirmation_token": token, "expires_at": expiresAt}, nil)
	case "confirm":
		var request struct {
			Token string `json:"confirmation_token"`
		}
		if !decodeV2(w, r, &request) {
			return
		}
		raw, err := s.store.ConsumeOperationConfirmation(r.Context(), security.HashSecret(strings.TrimSpace(request.Token)), "notification_broadcasts.create", 0, 0, *principal.UserID, time.Now().UTC())
		if err != nil {
			v2Error(w, r, http.StatusConflict, "confirmation_invalid", "确认令牌无效、已使用或已过期")
			return
		}
		if _, allowed := s.capabilities.Authorize(principal, "notification_broadcasts.create"); !allowed {
			v2Error(w, r, http.StatusForbidden, "capability_denied", "当前权限已变化，广播被拒绝")
			return
		}
		operation := automation.OperationRequest{Capability: "notification_broadcasts.create", Input: json.RawMessage(raw), ResourceRefs: json.RawMessage(`{}`)}
		changeset, err := s.applyConfirmedNodeChangeset(r.Context(), principal, []automation.OperationRequest{operation}, security.HashSecret(request.Token))
		if err != nil {
			v2Error(w, r, http.StatusConflict, "changeset_failed", err.Error())
			return
		}
		v2Write(w, r, http.StatusAccepted, map[string]any{"changeset": changeset}, nil)
	default:
		v2Error(w, r, http.StatusNotFound, "not_found", "广播操作不存在")
	}
}

func (s *Server) apiV1TelegramBindingCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	principal, _ := apiPrincipal(r)
	if principal.UserID == nil || !principal.Interactive {
		v2Error(w, r, http.StatusForbidden, "interactive_required", "绑定码只能由当前登录用户生成")
		return
	}
	user, err := s.store.GetUser(r.Context(), *principal.UserID)
	if err != nil || user.Status != "active" {
		v2Error(w, r, http.StatusForbidden, "user_inactive", "当前用户不可绑定 Telegram")
		return
	}
	var request struct {
		ChannelID int64 `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ChannelID <= 0 {
		v2Error(w, r, http.StatusBadRequest, "invalid_channel", "请选择要绑定的 Telegram 通知渠道")
		return
	}
	channel, err := s.store.GetNotificationChannel(r.Context(), request.ChannelID)
	if err != nil || channel.OwnerUserID != user.ID || channel.Type != "telegram" {
		v2Error(w, r, http.StatusNotFound, "channel_not_found", "Telegram 通知渠道不存在")
		return
	}
	if _, err := s.globalTelegramBot(r.Context()); err != nil {
		v2Error(w, r, http.StatusServiceUnavailable, "telegram_bot_not_configured", err.Error())
		return
	}
	secret, err := security.RandomToken(12)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	code := strings.ToUpper(strings.ReplaceAll(secret, "_", ""))
	if len(code) > 12 {
		code = code[:12]
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	if err := s.store.CreateTelegramBindingCode(r.Context(), security.HashSecret(code), user.ID, expiresAt); err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusCreated, model.TelegramBindingCode{Code: code, ChannelID: channel.ID, UserID: user.ID, ExpiresAt: expiresAt}, nil)
}

func (s *Server) apiV1TelegramBindings(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if principal.UserID == nil || !principal.Interactive {
		v2Error(w, r, http.StatusForbidden, "interactive_required", "Telegram 绑定需要当前登录用户")
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/telegram/bindings") {
		items, err := s.store.ListTelegramBindingsForUser(r.Context(), *principal.UserID)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, map[string]any{"bindings": items}, nil)
		return
	}
	if r.Method == http.MethodDelete {
		rawID := strings.TrimPrefix(r.URL.Path, "/api/v1/telegram/bindings/")
		if rawID == r.URL.Path {
			rawID = strings.TrimPrefix(r.URL.Path, "/api/v1/ui/telegram/bindings/")
		}
		if rawID == r.URL.Path {
			rawID = strings.TrimPrefix(r.URL.Path, "/api/v1/telegram/bindings/")
		}
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			v2Error(w, r, http.StatusBadRequest, "invalid_binding", "绑定 ID 无效")
			return
		}
		allowAny := model.HasManagementAccess(principal.Role)
		if err := s.store.DeleteTelegramBindingByID(r.Context(), id, *principal.UserID, allowAny); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				v2Error(w, r, http.StatusNotFound, "not_found", "Telegram 绑定不存在")
				return
			}
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, map[string]any{"deleted": true, "binding_id": id}, nil)
		return
	}
	v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
}
