package controller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const offlineAfter = 2 * time.Minute

const (
	notificationServerOffline     = "server_offline"
	notificationServerOnline      = "server_online"
	notificationTrafficQuota      = "traffic_quota_exceeded"
	notificationTaskFailed        = "task_failed"
	notificationTaskTimeout       = "task_timeout"
	notificationAdminAnnouncement = "admin_announcement"
)

type notificationEvent struct {
	Name         string
	Key          string
	TargetUserID int64
	Data         map[string]string
}

type notificationEventDefinition struct {
	Value       string   `json:"value"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Variables   []string `json:"variables"`
}

var notificationEventDefinitions = []notificationEventDefinition{
	{notificationServerOffline, "服务器离线", "Agent 心跳超时后通知", []string{"ServerName", "ServerID", "LastSeen", "Time"}},
	{notificationServerOnline, "服务器上线", "离线服务器恢复连接时通知", []string{"ServerName", "ServerID", "Time"}},
	{notificationTrafficQuota, "流量已用完", "所选用户本周期达到流量额度时通知", []string{"UserName", "UserID", "Used", "Limit", "ResetAt", "Time"}},
	{notificationTaskFailed, "任务失败", "Agent 更新、配置下发等任务失败时通知", []string{"TaskType", "TaskID", "ServerName", "Error", "Time"}},
	{notificationTaskTimeout, "任务超时", "任务等待或执行超过五分钟时通知", []string{"TaskType", "TaskID", "ServerName", "Error", "Time"}},
	{notificationAdminAnnouncement, "管理员通知", "管理员发送给你的消息", []string{"Title", "Message", "Sender", "Time"}},
}

var defaultNotificationTemplates = map[string]model.NotificationTemplate{
	notificationServerOffline: {
		Title: "服务器离线 · {{.ServerName}}",
		Body:  "{{.ServerName}} 已离线\n最后在线：{{.LastSeen}}\n时间：{{.Time}}",
	},
	notificationServerOnline: {
		Title: "服务器上线 · {{.ServerName}}",
		Body:  "{{.ServerName}} 已恢复在线\n时间：{{.Time}}",
	},
	notificationTrafficQuota: {
		Title: "流量已用完 · {{.UserName}}",
		Body:  "{{.UserName}} 本周期流量已用完\n已用：{{.Used}} / {{.Limit}}\n重置：{{.ResetAt}}",
	},
	notificationTaskFailed: {
		Title: "任务失败 · {{.TaskType}}",
		Body:  "服务器：{{.ServerName}}\n任务：#{{.TaskID}} {{.TaskType}}\n原因：{{.Error}}\n时间：{{.Time}}",
	},
	notificationTaskTimeout: {
		Title: "任务超时 · {{.TaskType}}",
		Body:  "服务器：{{.ServerName}}\n任务：#{{.TaskID}} {{.TaskType}}\n原因：{{.Error}}\n时间：{{.Time}}",
	},
	notificationAdminAnnouncement: {
		Title: "{{.Title}}",
		Body:  "{{.Message}}\n\n来自：{{.Sender}}",
	},
}

func (s *Server) StartMonitor(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	// Run once at start so long-lived controllers clear stale work without
	// waiting for the first panel poll.
	s.expireTimedOutTasks(ctx)
	s.checkOffline(ctx)
	s.maybeFinalizeBasePathMigration(ctx)
	s.schedulePeriodicInboundProbes(ctx)
	s.deliverPendingNotifications(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expireTimedOutTasks(ctx)
			s.checkOffline(ctx)
			s.maybeFinalizeBasePathMigration(ctx)
			s.schedulePeriodicInboundProbes(ctx)
			s.deliverPendingNotifications(ctx)
		}
	}
}

func (s *Server) checkOffline(ctx context.Context) {
	items, err := s.store.MarkStaleServersOffline(ctx, time.Now().UTC().Add(-offlineAfter))
	if err != nil {
		log.Printf("offline monitor failed: %v", err)
		return
	}
	for _, server := range items {
		s.enqueueNotificationEvent(ctx, notificationEvent{
			Name: notificationServerOffline,
			Key:  fmt.Sprintf("server:%d:offline:%s", server.ID, lastSeen(server.LastSeenAt)),
			Data: map[string]string{"ServerName": server.Name, "ServerID": fmt.Sprint(server.ID), "LastSeen": lastSeen(server.LastSeenAt), "Time": s.notificationNow(ctx)},
		})
	}
}

func lastSeen(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) notificationChannels(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	role := currentRole(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/notification-channels"), "/")
	parts := []string{}
	if rest != "" {
		parts = strings.Split(rest, "/")
	}

	// POST /notification-channels/test — test an unsaved channel body.
	if len(parts) == 1 && parts[0] == "test" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		s.testNotificationChannelBody(w, r, user.ID, role)
		return
	}

	id := int64(0)
	if len(parts) >= 1 && parts[0] != "" {
		parsed, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || parsed <= 0 {
			fail(w, errors.New("invalid notification channel id"), http.StatusBadRequest)
			return
		}
		id = parsed
	}

	// POST /notification-channels/{id}/test — test a saved channel.
	if len(parts) == 2 && parts[1] == "test" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		s.testNotificationChannelByID(w, r, id, user.ID, role)
		return
	}
	if len(parts) > 1 {
		fail(w, errors.New("not found"), http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if id > 0 {
			item, err := s.store.GetNotificationChannel(r.Context(), id)
			if err != nil || item.OwnerUserID != user.ID {
				fail(w, errors.New("notification channel not found"), http.StatusNotFound)
				return
			}
			write(w, 200, map[string]any{"notification_channel": publicNotificationChannel(*item)})
			return
		}
		items, err := s.store.ListNotificationChannelsByOwner(r.Context(), user.ID)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"notification_channels": publicNotificationChannels(items)})
	case http.MethodPost:
		if id > 0 {
			fail(w, errors.New("use PATCH to update a channel"), 405)
			return
		}
		var v model.NotificationChannel
		if !decode(w, r, &v) {
			return
		}
		v.OwnerUserID = user.ID
		if err := validateNotificationChannel(&v, role); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateNotificationTargets(r.Context(), &v, user.ID, role); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateNotificationChannel(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "notification_channel", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"notification_channel": publicNotificationChannel(v)})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		old, err := s.store.GetNotificationChannel(r.Context(), id)
		if err != nil || old.OwnerUserID != user.ID {
			fail(w, errors.New("notification channel not found"), 404)
			return
		}
		var v model.NotificationChannel
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		v.OwnerUserID = user.ID
		if v.Name == "" {
			v.Name = old.Name
		}
		if v.Type == "" {
			v.Type = old.Type
		}
		if v.Events == "" {
			v.Events = old.Events
		}
		if v.ConfigJSON == "" || notificationConfigLooksRedacted(v.ConfigJSON) {
			// Keep stored secrets when the client omits config or posts a redacted view.
			v.ConfigJSON = old.ConfigJSON
		}
		if strings.TrimSpace(v.TemplatesJSON) == "" {
			v.TemplatesJSON = old.TemplatesJSON
		}
		if v.UserIDs == nil {
			v.UserIDs = old.UserIDs
		}
		// Preserve Enabled when the client intentionally sends the field; decode
		// already populates bool zero-value, so use full replace from request body
		// after defaults for missing string fields.
		if err := validateNotificationChannel(&v, role); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateNotificationTargets(r.Context(), &v, user.ID, role); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateNotificationChannel(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "notification_channel", fmt.Sprint(v.ID))
		write(w, 200, map[string]any{"notification_channel": publicNotificationChannel(v)})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteNotificationChannel(r.Context(), id, user.ID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fail(w, err, 404)
			} else {
				fail(w, err, 500)
			}
			return
		}
		auditReq(s, r, "delete", "notification_channel", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) testNotificationChannelByID(w http.ResponseWriter, r *http.Request, id, ownerUserID int64, role model.Role) {
	item, err := s.store.GetNotificationChannel(r.Context(), id)
	if err != nil || item.OwnerUserID != ownerUserID {
		fail(w, errors.New("notification channel not found"), http.StatusNotFound)
		return
	}
	if err := validateNotificationChannel(item, role); err != nil {
		fail(w, err, 400)
		return
	}
	title, body := notificationTestMessage(item.Name, item.Type)
	if err := s.notificationSender(r.Context(), *item, title, body); err != nil {
		fail(w, fmt.Errorf("发送测试通知失败: %w", err), 502)
		return
	}
	auditReq(s, r, "notify_test", "notification_channel", fmt.Sprint(id))
	write(w, 200, map[string]any{"ok": true, "message": "测试通知已发送"})
}

func (s *Server) testNotificationChannelBody(w http.ResponseWriter, r *http.Request, ownerUserID int64, role model.Role) {
	var v model.NotificationChannel
	if !decode(w, r, &v) {
		return
	}
	v.OwnerUserID = ownerUserID
	if err := validateNotificationChannel(&v, role); err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.validateNotificationTargets(r.Context(), &v, ownerUserID, role); err != nil {
		fail(w, err, 400)
		return
	}
	title, body := notificationTestMessage(v.Name, v.Type)
	if err := s.notificationSender(r.Context(), v, title, body); err != nil {
		fail(w, fmt.Errorf("发送测试通知失败: %w", err), 502)
		return
	}
	auditReq(s, r, "notify_test", "notification_channel", "draft")
	write(w, 200, map[string]any{"ok": true, "message": "测试通知已发送"})
}

func notificationTestMessage(name, channelType string) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名通道"
	}
	return "OBoard 测试通知",
		fmt.Sprintf("这是一条来自 OBoard 控制面板的测试消息。\n通道：%s\n类型：%s\n时间：%s",
			name, channelType, time.Now().UTC().Format(time.RFC3339))
}

func validateNotificationChannel(v *model.NotificationChannel, role model.Role) error {
	v.Name = strings.TrimSpace(v.Name)
	v.Type = strings.TrimSpace(strings.ToLower(v.Type))
	if v.Name == "" {
		return errors.New("请填写通道名称")
	}
	if v.Type != "telegram" && v.Type != "bark" {
		return errors.New("通道类型仅支持 Telegram 或 Bark")
	}
	events, err := normalizeNotificationEvents(v.Events, role)
	if err != nil {
		return err
	}
	v.Events = strings.Join(events, ",")
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
	var tmp map[string]any
	if err := json.Unmarshal([]byte(v.ConfigJSON), &tmp); err != nil {
		return fmt.Errorf("配置 JSON 无效: %w", err)
	}
	switch v.Type {
	case "telegram":
		botToken, _ := tmp["bot_token"].(string)
		chatID := ""
		switch id := tmp["chat_id"].(type) {
		case string:
			chatID = id
		case float64:
			chatID = strconv.FormatInt(int64(id), 10)
		}
		if strings.TrimSpace(botToken) == "" || strings.TrimSpace(chatID) == "" {
			return errors.New("通知渠道 Telegram 需要填写 Bot Token 和 Chat ID")
		}
	case "bark":
		deviceKey, _ := tmp["device_key"].(string)
		if strings.TrimSpace(deviceKey) == "" {
			return errors.New("通知渠道 Bark 需要填写 Device Key")
		}
		serverURL, _ := tmp["server_url"].(string)
		if strings.TrimSpace(serverURL) == "" {
			tmp["server_url"] = "https://api.day.app"
		} else if err := validateBarkServerURL(serverURL); err != nil {
			return err
		}
		encoded, err := json.Marshal(tmp)
		if err != nil {
			return err
		}
		v.ConfigJSON = string(encoded)
	}
	templatesJSON, err := normalizeNotificationTemplates(v.TemplatesJSON, role)
	if err != nil {
		return err
	}
	v.TemplatesJSON = templatesJSON
	return nil
}

func normalizeNotificationEvents(raw string, role model.Role) ([]string, error) {
	allowed := allowedNotificationEventSet(role)
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		event := strings.TrimSpace(strings.ToLower(part))
		if event == "" {
			continue
		}
		if !allowed[event] {
			return nil, fmt.Errorf("当前账户不能订阅通知事件 %s", event)
		}
		if seen[event] {
			continue
		}
		seen[event] = true
		out = append(out, event)
	}
	if len(out) == 0 {
		return nil, errors.New("请至少选择一个通知事件")
	}
	return out, nil
}

func allowedNotificationEventSet(role model.Role) map[string]bool {
	allowed := map[string]bool{
		notificationTrafficQuota:      true,
		notificationAdminAnnouncement: true,
	}
	if roleAllows(role, model.RoleAdmin) {
		allowed[notificationServerOffline] = true
		allowed[notificationServerOnline] = true
		allowed[notificationTaskFailed] = true
		allowed[notificationTaskTimeout] = true
	}
	return allowed
}

func notificationPageConfig(role model.Role) map[string]any {
	allowed := allowedNotificationEventSet(role)
	events := []notificationEventDefinition{}
	templates := map[string]model.NotificationTemplate{}
	for _, definition := range notificationEventDefinitions {
		if !allowed[definition.Value] {
			continue
		}
		events = append(events, definition)
		templates[definition.Value] = defaultNotificationTemplates[definition.Value]
	}
	return map[string]any{"events": events, "templates": templates}
}

func normalizeNotificationTemplates(raw string, role model.Role) (string, error) {
	allowed := allowedNotificationEventSet(role)
	templates := map[string]model.NotificationTemplate{}
	for event := range allowed {
		templates[event] = defaultNotificationTemplates[event]
	}
	if strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != "{}" {
		var custom map[string]model.NotificationTemplate
		if err := json.Unmarshal([]byte(raw), &custom); err != nil {
			return "", fmt.Errorf("通知模板 JSON 无效: %w", err)
		}
		for event, value := range custom {
			if !allowed[event] {
				return "", fmt.Errorf("当前账户不能编辑通知模板 %s", event)
			}
			templates[event] = value
		}
	}
	variablesByEvent := map[string]map[string]string{}
	for _, definition := range notificationEventDefinitions {
		if !allowed[definition.Value] {
			continue
		}
		variables := map[string]string{}
		for _, variable := range definition.Variables {
			variables[variable] = "示例"
		}
		variablesByEvent[definition.Value] = variables
	}
	for event, value := range templates {
		value.Title = strings.TrimSpace(value.Title)
		value.Body = strings.TrimSpace(value.Body)
		if value.Title == "" || value.Body == "" {
			return "", fmt.Errorf("通知模板 %s 的标题和正文不能为空", event)
		}
		if len(value.Title) > 500 || len(value.Body) > 5000 {
			return "", fmt.Errorf("通知模板 %s 过长", event)
		}
		variables := variablesByEvent[event]
		if _, err := executeNotificationTemplate(event+"-title", value.Title, variables); err != nil {
			return "", fmt.Errorf("通知模板 %s 标题无效: %w", event, err)
		}
		if _, err := executeNotificationTemplate(event+"-body", value.Body, variables); err != nil {
			return "", fmt.Errorf("通知模板 %s 正文无效: %w", event, err)
		}
		templates[event] = value
	}
	encoded, err := json.Marshal(templates)
	return string(encoded), err
}

func executeNotificationTemplate(name, source string, data map[string]string) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *Server) validateNotificationTargets(ctx context.Context, channel *model.NotificationChannel, ownerUserID int64, role model.Role) error {
	if !notificationEventEnabled(channel.Events, notificationTrafficQuota) {
		channel.UserIDs = nil
		return nil
	}
	if !roleAllows(role, model.RoleAdmin) {
		for _, userID := range channel.UserIDs {
			if userID != ownerUserID {
				return errors.New("普通用户只能接收自己的流量通知")
			}
		}
		channel.UserIDs = []int64{ownerUserID}
		return nil
	}
	if len(channel.UserIDs) == 0 {
		channel.UserIDs = []int64{ownerUserID}
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	valid := map[int64]bool{}
	for _, user := range users {
		if user.Status == "active" {
			valid[user.ID] = true
		}
	}
	seen := map[int64]bool{}
	targets := make([]int64, 0, len(channel.UserIDs))
	for _, userID := range channel.UserIDs {
		if !valid[userID] {
			return fmt.Errorf("通知用户 #%d 不存在或已停用", userID)
		}
		if !seen[userID] {
			seen[userID] = true
			targets = append(targets, userID)
		}
	}
	channel.UserIDs = targets
	return nil
}

func notificationEventEnabled(events, event string) bool {
	for _, value := range strings.Split(events, ",") {
		if strings.TrimSpace(value) == event {
			return true
		}
	}
	return false
}

func publicNotificationChannels(items []model.NotificationChannel) []model.NotificationChannel {
	out := make([]model.NotificationChannel, len(items))
	for i := range items {
		out[i] = publicNotificationChannel(items[i])
	}
	return out
}

// publicNotificationChannel redacts secret fields from API responses while
// keeping non-secret identifiers (chat_id / server_url) for UI editing.
func publicNotificationChannel(item model.NotificationChannel) model.NotificationChannel {
	item.ConfigJSON = redactNotificationConfigJSON(item.Type, item.ConfigJSON)
	return item
}

func redactNotificationConfigJSON(channelType, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return `{"configured":true}`
	}
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "telegram":
		if token, _ := cfg["bot_token"].(string); strings.TrimSpace(token) != "" {
			cfg["bot_token"] = "********"
			cfg["bot_token_configured"] = true
		}
	case "bark":
		if key, _ := cfg["device_key"].(string); strings.TrimSpace(key) != "" {
			cfg["device_key"] = "********"
			cfg["device_key_configured"] = true
		}
	default:
		for _, secretKey := range []string{"bot_token", "device_key", "token", "secret", "password", "api_key"} {
			if value, ok := cfg[secretKey].(string); ok && strings.TrimSpace(value) != "" {
				cfg[secretKey] = "********"
				cfg[secretKey+"_configured"] = true
			}
		}
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return `{"configured":true}`
	}
	return string(encoded)
}

func notificationConfigLooksRedacted(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return false
	}
	for _, key := range []string{"bot_token", "device_key", "token", "secret", "password", "api_key"} {
		if value, ok := cfg[key].(string); ok && isRedactedSecret(value) {
			return true
		}
	}
	return false
}

func isRedactedSecret(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if value == "********" || value == "****" || value == "[redacted]" {
		return true
	}
	// Treat all-asterisk placeholders as redacted.
	for _, r := range value {
		if r != '*' {
			return false
		}
	}
	return len(value) >= 4
}

func (s *Server) notificationNow(ctx context.Context) string {
	settings, _ := s.store.ListSettings(ctx)
	return time.Now().In(trafficLocation(settings)).Format("2006-01-02 15:04:05 MST")
}

func (s *Server) enqueueNotificationEvent(ctx context.Context, event notificationEvent) int {
	if strings.TrimSpace(event.Name) == "" || strings.TrimSpace(event.Key) == "" {
		return 0
	}
	channels, err := s.store.ListEnabledNotificationChannels(ctx, event.Name)
	if err != nil {
		log.Printf("list notification channels for %s: %v", event.Name, err)
		return 0
	}
	queued := 0
	for _, channel := range channels {
		owner, err := s.store.GetUser(ctx, channel.OwnerUserID)
		if err != nil || owner.Status != "active" {
			continue
		}
		role, err := s.store.EffectiveUserRole(ctx, *owner)
		if err != nil || !notificationChannelEligible(channel, role, event) {
			continue
		}
		title, body, err := renderNotificationEvent(channel, event)
		if err != nil {
			log.Printf("render notification %s for channel %d: %v", event.Name, channel.ID, err)
			continue
		}
		delivery := model.NotificationDelivery{ChannelID: channel.ID, Event: event.Name, EventKey: event.Key, Title: title, Body: body, NextAttemptAt: time.Now().UTC()}
		inserted, err := s.store.QueueNotificationDelivery(ctx, &delivery)
		if err != nil {
			log.Printf("queue notification %s for channel %d: %v", event.Name, channel.ID, err)
			continue
		}
		if inserted {
			queued++
		}
	}
	if queued > 0 {
		go func(parent context.Context) {
			deliveryCtx, cancel := context.WithTimeout(parent, 30*time.Second)
			defer cancel()
			s.deliverPendingNotifications(deliveryCtx)
		}(context.WithoutCancel(ctx))
	}
	return queued
}

func notificationChannelEligible(channel model.NotificationChannel, ownerRole model.Role, event notificationEvent) bool {
	switch event.Name {
	case notificationServerOffline, notificationServerOnline, notificationTaskFailed, notificationTaskTimeout:
		return roleAllows(ownerRole, model.RoleAdmin)
	case notificationTrafficQuota:
		if channel.OwnerUserID == event.TargetUserID {
			return true
		}
		return roleAllows(ownerRole, model.RoleAdmin) && containsNotificationUserID(channel.UserIDs, event.TargetUserID)
	case notificationAdminAnnouncement:
		return channel.OwnerUserID == event.TargetUserID
	default:
		return false
	}
}

func containsNotificationUserID(items []int64, target int64) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func renderNotificationEvent(channel model.NotificationChannel, event notificationEvent) (string, string, error) {
	templates := map[string]model.NotificationTemplate{}
	if err := json.Unmarshal([]byte(channel.TemplatesJSON), &templates); err != nil {
		templates = map[string]model.NotificationTemplate{}
	}
	value, ok := templates[event.Name]
	if !ok || strings.TrimSpace(value.Title) == "" || strings.TrimSpace(value.Body) == "" {
		value = defaultNotificationTemplates[event.Name]
	}
	title, err := executeNotificationTemplate(event.Name+"-title", value.Title, event.Data)
	if err != nil {
		return "", "", err
	}
	body, err := executeNotificationTemplate(event.Name+"-body", value.Body, event.Data)
	if err != nil {
		return "", "", err
	}
	if len(title) > 500 {
		title = title[:500]
	}
	if len(body) > 5000 {
		body = body[:5000]
	}
	return title, body, nil
}

func (s *Server) deliverPendingNotifications(ctx context.Context) {
	s.notificationMu.Lock()
	defer s.notificationMu.Unlock()
	deliveries, err := s.store.ListPendingNotificationDeliveries(ctx, time.Now().UTC(), 50)
	if err != nil {
		log.Printf("list pending notifications: %v", err)
		return
	}
	for _, delivery := range deliveries {
		sendErr := s.notificationSender(ctx, delivery.Channel, delivery.Title, delivery.Body)
		retryDelay := time.Minute
		if delivery.Attempts >= 1 {
			retryDelay = 5 * time.Minute
		}
		if err := s.store.CompleteNotificationDelivery(ctx, delivery.ID, sendErr, time.Now().UTC().Add(retryDelay)); err != nil {
			log.Printf("complete notification delivery %d: %v", delivery.ID, err)
			continue
		}
		if sendErr != nil {
			log.Printf("notification %s via %s failed: %v", delivery.Event, delivery.Channel.Type, sendErr)
			_ = s.store.AddAudit(ctx, model.AuditLog{Action: "notify_failed", Target: "notification_channel", Detail: fmt.Sprintf("%d:%s", delivery.ChannelID, delivery.Event), IP: "controller"})
			continue
		}
		_ = s.store.AddAudit(ctx, model.AuditLog{Action: "notify", Target: "notification_channel", Detail: fmt.Sprintf("%d:%s", delivery.ChannelID, delivery.Event), IP: "controller"})
	}
}

func (s *Server) notificationAnnouncements(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListNotificationAnnouncements(r.Context(), intQuery(r, "limit", 20))
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"notification_announcements": items})
	case http.MethodPost:
		actor := currentUser(r)
		if actor == nil {
			fail(w, errors.New("invalid session"), 401)
			return
		}
		var req struct {
			Title    string  `json:"title"`
			Body     string  `json:"body"`
			AllUsers bool    `json:"all_users"`
			UserIDs  []int64 `json:"user_ids"`
		}
		if !decode(w, r, &req) {
			return
		}
		req.Title = strings.TrimSpace(req.Title)
		req.Body = strings.TrimSpace(req.Body)
		if req.Title == "" || req.Body == "" {
			fail(w, errors.New("通知标题和内容不能为空"), 400)
			return
		}
		if len(req.Title) > 120 || len(req.Body) > 3000 {
			fail(w, errors.New("管理员通知内容过长"), 400)
			return
		}
		users, err := s.store.ListUsers(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		active := map[int64]model.User{}
		for _, user := range users {
			if user.Status == "active" {
				active[user.ID] = user
			}
		}
		targets := []int64{}
		if req.AllUsers {
			for _, user := range users {
				if user.ID == actor.ID || user.Status != "active" {
					continue
				}
				role, err := s.store.EffectiveUserRole(r.Context(), user)
				if err == nil && !roleAllows(role, model.RoleAdmin) {
					targets = append(targets, user.ID)
				}
			}
		} else {
			seen := map[int64]bool{}
			for _, userID := range req.UserIDs {
				if _, ok := active[userID]; !ok {
					fail(w, fmt.Errorf("通知用户 #%d 不存在或已停用", userID), 400)
					return
				}
				if !seen[userID] {
					seen[userID] = true
					targets = append(targets, userID)
				}
			}
		}
		if len(targets) == 0 {
			fail(w, errors.New("请选择至少一个接收用户"), 400)
			return
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
		actorName := strings.TrimSpace(actor.Nickname)
		if actorName == "" {
			actorName = actor.Username
		}
		announcement := model.NotificationAnnouncement{ActorUserID: actor.ID, ActorName: actorName, Title: req.Title, Body: req.Body, UserIDs: targets}
		if err := s.store.CreateNotificationAnnouncement(r.Context(), &announcement); err != nil {
			fail(w, err, 500)
			return
		}
		queued := 0
		for _, userID := range targets {
			queued += s.enqueueNotificationEvent(r.Context(), notificationEvent{
				Name:         notificationAdminAnnouncement,
				Key:          fmt.Sprintf("announcement:%d:user:%d", announcement.ID, userID),
				TargetUserID: userID,
				Data:         map[string]string{"Title": req.Title, "Message": req.Body, "Sender": actorName, "Time": s.notificationNow(r.Context())},
			})
		}
		announcement.QueuedCount = queued
		_ = s.store.UpdateNotificationAnnouncementQueuedCount(r.Context(), announcement.ID, queued)
		auditReq(s, r, "notify", "notification_announcement", fmt.Sprintf("%d:%d", announcement.ID, len(targets)))
		write(w, http.StatusAccepted, map[string]any{"notification_announcement": announcement, "queued_count": queued})
	default:
		method(w)
	}
}

func (s *Server) completeTaskWithNotification(ctx context.Context, taskID int64, status, result string) error {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if err := s.store.CompleteTask(ctx, taskID, status, result); err != nil {
		return err
	}
	if status == "failed" || status == "rollback_failed" {
		task.Status = status
		task.ResultJSON = result
		s.notifyTaskFailure(ctx, *task)
	}
	return nil
}

func (s *Server) notifyTaskFailure(ctx context.Context, task model.AgentTask) {
	eventName := notificationTaskFailed
	var result struct {
		Timeout bool `json:"timeout"`
	}
	if json.Unmarshal([]byte(task.ResultJSON), &result) == nil && result.Timeout {
		eventName = notificationTaskTimeout
	}
	serverName := fmt.Sprintf("服务器 #%d", task.ServerID)
	if server, err := s.store.GetServer(ctx, task.ServerID); err == nil && strings.TrimSpace(server.Name) != "" {
		serverName = server.Name
	}
	safeTask := task
	safeTask.ResultJSON = scrubSensitiveJSON(task.ResultJSON)
	errorText := strings.TrimSpace(taskResultMessage(safeTask))
	if errorText == "" {
		errorText = "未返回具体原因"
	}
	if len(errorText) > 600 {
		errorText = errorText[:600]
	}
	s.enqueueNotificationEvent(ctx, notificationEvent{
		Name: eventName,
		Key:  fmt.Sprintf("task:%d:%s", task.ID, eventName),
		Data: map[string]string{
			"TaskType":   taskTypeNotificationLabel(task.Type),
			"TaskID":     fmt.Sprint(task.ID),
			"ServerName": serverName,
			"Error":      errorText,
			"Time":       s.notificationNow(ctx),
		},
	})
}

func taskTypeNotificationLabel(taskType string) string {
	labels := map[string]string{
		model.AgentTaskTypeApplyDeployment:       "配置部署",
		model.AgentTaskTypeApplyCoreConfig:       "核心配置下发",
		model.AgentTaskTypeUpdateAgent:           "Agent 更新",
		model.AgentTaskTypeUpdateAgentConfig:     "Agent 设置同步",
		model.AgentTaskTypeProbeInbounds:         "入口探测",
		model.AgentTaskTypeProbeInboundsExternal: "外部入口探测",
		model.AgentTaskTypeProbePortForwards:     "端口转发探测",
		model.AgentTaskTypeCollectLogs:           "日志拉取",
		model.AgentTaskTypeManageLogs:            "日志管理",
		model.AgentTaskTypeDiagnoseNetwork:       "网络诊断",
		model.AgentTaskTypeDetectMTU:             "MTU 检测",
	}
	if label := labels[strings.TrimSpace(taskType)]; label != "" {
		return label
	}
	if strings.TrimSpace(taskType) == "" {
		return "未知任务"
	}
	return taskType
}

func (s *Server) notifyTrafficQuotaExceeded(ctx context.Context, user model.User, period model.TrafficPeriod) {
	if period.State != "quota_exceeded" || period.Limit <= 0 {
		return
	}
	name := strings.TrimSpace(user.Nickname)
	if name == "" {
		name = user.Username
	}
	settings, _ := s.store.ListSettings(ctx)
	location := trafficLocation(settings)
	s.enqueueNotificationEvent(ctx, notificationEvent{
		Name:         notificationTrafficQuota,
		Key:          fmt.Sprintf("traffic:%d:%s", user.ID, period.PeriodKey),
		TargetUserID: user.ID,
		Data: map[string]string{
			"UserName": name,
			"UserID":   fmt.Sprint(user.ID),
			"Used":     formatNotificationBytes(period.Upload + period.Download),
			"Limit":    formatNotificationBytes(period.Limit),
			"ResetAt":  period.EndsAt.In(location).Format("2006-01-02 15:04 MST"),
			"Time":     s.notificationNow(ctx),
		},
	})
}

func formatNotificationBytes(value int64) string {
	if value < 0 {
		value = 0
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	number := float64(value)
	unit := 0
	for number >= 1024 && unit < len(units)-1 {
		number /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	return fmt.Sprintf("%.2f %s", number, units[unit])
}

func sendNotification(ctx context.Context, channel model.NotificationChannel, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch channel.Type {
	case "telegram":
		var cfg struct {
			BotToken string `json:"bot_token"`
			ChatID   string `json:"chat_id"`
		}
		if err := json.Unmarshal([]byte(channel.ConfigJSON), &cfg); err != nil {
			return err
		}
		if cfg.BotToken == "" || cfg.ChatID == "" {
			return errors.New("telegram bot_token and chat_id required")
		}
		form := url.Values{}
		form.Set("chat_id", cfg.ChatID)
		form.Set("text", title+"\n"+body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+cfg.BotToken+"/sendMessage", strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return doNotify(req)
	case "bark":
		var cfg struct {
			ServerURL string `json:"server_url"`
			DeviceKey string `json:"device_key"`
		}
		if err := json.Unmarshal([]byte(channel.ConfigJSON), &cfg); err != nil {
			return err
		}
		if cfg.ServerURL == "" {
			cfg.ServerURL = "https://api.day.app"
		}
		if err := validateBarkServerURL(cfg.ServerURL); err != nil {
			return err
		}
		if cfg.DeviceKey == "" {
			return errors.New("bark device_key required")
		}
		target := strings.TrimRight(cfg.ServerURL, "/") + "/" + url.PathEscape(cfg.DeviceKey) + "/" + url.PathEscape(title) + "/" + url.PathEscape(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		return doNotify(req)
	default:
		return errors.New("unsupported notification channel")
	}
}

func doNotify(req *http.Request) error {
	return doNotifyWithNetwork(req, net.DefaultResolver, &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second})
}

type notificationResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type notificationDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

func doNotifyWithNetwork(req *http.Request, resolver notificationResolver, dialer notificationDialer) error {
	if req == nil {
		return errors.New("notification request is required")
	}
	if err := validatePublicHTTPSURLWithResolver(req.Context(), req.URL, resolver); err != nil {
		return err
	}
	client := &http.Client{
		Transport:     newNotificationTransport(resolver, dialer),
		Timeout:       10 * time.Second,
		CheckRedirect: notificationRedirectPolicy(resolver),
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned %s", resp.Status)
	}
	return nil
}

func notificationRedirectPolicy(resolver notificationResolver) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if err := validatePublicHTTPSURLWithResolver(req.Context(), req.URL, resolver); err != nil {
			return err
		}
		return nil
	}
}

func newNotificationTransport(resolver notificationResolver, dialer notificationDialer) *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid notification address: %w", err)
			}
			ips, err := resolvePublicNotificationIPs(ctx, host, resolver)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errors.New("notification URL host did not resolve")
			}
			return nil, lastErr
		},
	}
}

func validateBarkServerURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("无效的 Bark server_url，必须是 HTTPS URL")
	}
	return validatePublicHTTPSURL(u)
}

func validatePublicHTTPSURL(u *url.URL) error {
	return validatePublicHTTPSURLWithResolver(context.Background(), u, net.DefaultResolver)
}

func validatePublicHTTPSURLWithResolver(ctx context.Context, u *url.URL, resolver notificationResolver) error {
	if u == nil {
		return errors.New("missing URL")
	}
	if strings.ToLower(u.Scheme) != "https" {
		return errors.New("notification URL must use https")
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host == "" {
		return errors.New("notification URL host is required")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("notification URL must not target localhost")
	}
	_, err := resolvePublicNotificationIPs(ctx, host, resolver)
	return err
}

func resolvePublicNotificationIPs(ctx context.Context, host string, resolver notificationResolver) ([]net.IP, error) {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, errors.New("notification URL must not target private or link-local addresses")
		}
		return []net.IP{ip}, nil
	}
	if resolver == nil {
		return nil, errors.New("notification DNS resolver is unavailable")
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("notification URL host did not resolve: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("notification URL host did not resolve")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if !isPublicIP(addr.IP) {
			return nil, errors.New("notification URL must not resolve to private or link-local addresses")
		}
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 169 && ip4[1] == 254 {
			return false
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return false
		}
	}
	return true
}
