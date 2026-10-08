package controller

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	telegramActionPrefix       = "ta:"
	telegramActionTokenBytes   = 8
	telegramActionOfferTTL     = 48 * time.Hour
	telegramActionConfirmTTL   = 5 * time.Minute
	telegramActionTextLimit    = 3900
	telegramActionRestoreLimit = 6
)

type notificationContext struct {
	ServerID      int64  `json:"server_id,omitempty"`
	TaskID        int64  `json:"task_id,omitempty"`
	TaskType      string `json:"task_type,omitempty"`
	CertificateID int64  `json:"certificate_id,omitempty"`
	InboundID     int64  `json:"inbound_id,omitempty"`
	UserID        int64  `json:"user_id,omitempty"`
}

func notificationContextJSON(value notificationContext) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func parseNotificationContext(raw string) notificationContext {
	var value notificationContext
	_ = json.Unmarshal([]byte(raw), &value)
	return value
}

type telegramActionPayload struct {
	Action          string  `json:"action"`
	Stage           string  `json:"stage,omitempty"`
	ServerID        int64   `json:"server_id,omitempty"`
	TaskID          int64   `json:"task_id,omitempty"`
	TaskType        string  `json:"task_type,omitempty"`
	CertificateID   int64   `json:"certificate_id,omitempty"`
	InboundID       int64   `json:"inbound_id,omitempty"`
	UserID          int64   `json:"user_id,omitempty"`
	Days            int     `json:"days,omitempty"`
	IncidentID      int64   `json:"incident_id,omitempty"`
	EventVersion    int64   `json:"event_version,omitempty"`
	InboundIDs      []int64 `json:"inbound_ids,omitempty"`
	RecoveryPolicy  string  `json:"recovery_policy,omitempty"`
	DurationMinutes int     `json:"duration_minutes,omitempty"`
	IsolationID     int64   `json:"isolation_id,omitempty"`
	Event           string  `json:"event,omitempty"`
	ContextJSON     string  `json:"context_json,omitempty"`
	Notice          string  `json:"notice,omitempty"`
}

type telegramActionSpec struct {
	Label   string
	Action  string
	Row     int
	Payload telegramActionPayload
}

type telegramIncidentButtonInput struct {
	ID         int64
	Version    int64
	ServerID   int64
	Status     string
	Published  []int64
	Isolations []struct {
		ID   int64
		Name string
	}
	Notice string
}

type telegramActionInput struct {
	Event                   string
	Context                 notificationContext
	CertificateIssueAllowed bool
	Notice                  string
}

func telegramNotificationActionSpecs(in telegramActionInput) []telegramActionSpec {
	notice := telegramLimitText(in.Notice)
	base := telegramActionPayload{Event: in.Event, ContextJSON: notificationContextJSON(in.Context), Notice: notice, ServerID: in.Context.ServerID, TaskID: in.Context.TaskID, TaskType: in.Context.TaskType, CertificateID: in.Context.CertificateID, InboundID: in.Context.InboundID, UserID: in.Context.UserID}
	var specs []telegramActionSpec
	add := func(row int, action, label string, extra func(*telegramActionPayload)) {
		payload := base
		payload.Action = action
		if extra != nil {
			extra(&payload)
		}
		specs = append(specs, telegramActionSpec{Label: label, Action: action, Row: row, Payload: payload})
	}
	view := in.Context.ServerID > 0 && in.Event != notificationBackupFailed
	if view && in.Event != notificationUserRisk && in.Event != notificationSubscriptionRisk && in.Event != notificationSubscriptionAbnormal {
		add(0, "view_server", "查看状态", nil)
	}
	switch in.Event {
	case notificationTaskFailed, notificationTaskTimeout:
		if taskTelegramRetryAllowed(in.Context.TaskType) && in.Context.ServerID > 0 {
			add(1, "retry_delivery", "重试下发", nil)
		}
	case notificationCertificateFailed, notificationCertificateExpiry:
		if in.CertificateIssueAllowed && in.Context.CertificateID > 0 {
			add(1, "issue_certificate", "重新签发", nil)
		}
	case notificationDNSSyncFailed:
		if in.Context.InboundID > 0 {
			add(1, "sync_dns", "重试同步", nil)
		}
	case notificationServerClockSkew:
		if in.Context.ServerID > 0 {
			add(1, "recheck_time", "再测一次", nil)
		}
	case notificationBackupFailed:
		add(0, "create_backup", "立即备份", nil)
	case notificationServerExpiry:
		if in.Context.ServerID > 0 {
			for _, choice := range []struct {
				days  int
				label string
				row   int
			}{{7, "+7 天", 1}, {30, "+30 天", 1}, {90, "+90 天", 2}, {365, "+1 年", 2}} {
				days := choice.days
				add(choice.row, "extend_expiry", choice.label, func(payload *telegramActionPayload) {
					payload.Days = days
				})
			}
		}
	case notificationUserRisk, notificationSubscriptionRisk, notificationSubscriptionAbnormal:
		if in.Context.UserID > 0 {
			add(0, "refresh_audit", "刷新快照", nil)
		}
	}
	return specs
}

func telegramIncidentActionSpecs(in telegramIncidentButtonInput) []telegramActionSpec {
	if in.ID <= 0 || in.ServerID <= 0 {
		return nil
	}
	notice := telegramLimitText(in.Notice)
	base := telegramActionPayload{Event: "node_incident", IncidentID: in.ID, EventVersion: in.Version, ServerID: in.ServerID, Notice: notice}
	specs := []telegramActionSpec{{Label: "查看状态", Action: "view_server", Row: 0, Payload: func() telegramActionPayload {
		payload := base
		payload.Action = "view_server"
		return payload
	}()}}
	if in.Status == string(model.NodeIncidentActive) && len(in.Published) > 0 {
		ids := append([]int64(nil), in.Published...)
		until := base
		until.Action = "incident_isolate"
		until.InboundIDs = ids
		until.RecoveryPolicy = "auto"
		specs = append(specs, telegramActionSpec{Label: "直至恢复在线", Action: "incident_isolate", Row: 1, Payload: until})
		for _, choice := range []struct {
			label   string
			minutes int
		}{{"剔除 1 小时", 60}, {"剔除 6 小时", 360}, {"剔除 24 小时", 1440}} {
			payload := base
			payload.Action = "incident_isolate"
			payload.InboundIDs = ids
			payload.RecoveryPolicy = "manual"
			payload.DurationMinutes = choice.minutes
			specs = append(specs, telegramActionSpec{Label: choice.label, Action: "incident_isolate", Row: 2, Payload: payload})
		}
		remove := base
		remove.Action = "incident_remove"
		remove.InboundIDs = ids
		specs = append(specs, telegramActionSpec{Label: "永久移除", Action: "incident_remove", Row: 3, Payload: remove})
	}
	shown := 0
	for _, isolation := range in.Isolations {
		if shown >= telegramActionRestoreLimit || isolation.ID <= 0 {
			break
		}
		name := strings.TrimSpace(isolation.Name)
		if name == "" {
			name = fmt.Sprintf("#%d", isolation.ID)
		}
		payload := base
		payload.Action = "incident_restore"
		payload.IsolationID = isolation.ID
		specs = append(specs, telegramActionSpec{Label: "撤销剔除·" + name, Action: "incident_restore", Row: len(specs), Payload: payload})
		shown++
	}
	return specs
}

func taskTelegramRetryAllowed(taskType string) bool {
	return taskType == model.AgentTaskTypeApplyDeployment || taskType == model.AgentTaskTypeApplyCoreConfig
}

func certificateTelegramReissueAllowed(certificate model.Certificate, now time.Time) bool {
	if certificate.ChallengeType == "imported" || certificate.ID <= 0 {
		return false
	}
	if certificate.Status == "issued" && certificate.NotAfter != nil && certificate.NotAfter.Sub(now) > 7*24*time.Hour {
		return false
	}
	return true
}

func telegramActionNeedsConfirm(action string) bool {
	switch action {
	case "view_server", "refresh_audit", "cancel":
		return false
	default:
		return action != ""
	}
}

func telegramExtendDaysAllowed(days int) bool {
	switch days {
	case 7, 30, 90, 365:
		return true
	default:
		return false
	}
}

func telegramLimitText(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= telegramActionTextLimit {
		return text
	}
	return string(runes[:telegramActionTextLimit]) + "…"
}

type telegramInlineButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type telegramInlineKeyboard struct {
	Rows [][]telegramInlineButton `json:"inline_keyboard"`
}

func (s *Server) issueTelegramKeyboard(ctx context.Context, chatID, messageID int64, specs []telegramActionSpec, ttl time.Duration) (string, []string, error) {
	if len(specs) == 0 {
		return "", nil, nil
	}
	_ = s.store.DeleteExpiredTelegramActionTokens(ctx, time.Now().UTC())
	if messageID > 0 {
		_ = s.store.DeleteTelegramActionTokensForMessage(ctx, chatID, messageID)
	}
	rows := map[int][]telegramInlineButton{}
	order := []int{}
	seen := map[int]bool{}
	hashes := []string{}
	expires := time.Now().UTC().Add(ttl)
	for _, spec := range specs {
		plain, hash, err := newTelegramActionToken()
		if err != nil {
			_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
			return "", nil, err
		}
		raw, err := json.Marshal(spec.Payload)
		if err != nil {
			_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
			return "", nil, err
		}
		if err := s.store.CreateTelegramActionToken(ctx, hash, chatID, messageID, spec.Action, string(raw), expires); err != nil {
			_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
			return "", nil, err
		}
		hashes = append(hashes, hash)
		if !seen[spec.Row] {
			seen[spec.Row] = true
			order = append(order, spec.Row)
		}
		rows[spec.Row] = append(rows[spec.Row], telegramInlineButton{Text: spec.Label, Data: telegramActionPrefix + plain})
	}
	keyboard := telegramInlineKeyboard{}
	for _, row := range order {
		keyboard.Rows = append(keyboard.Rows, rows[row])
	}
	encoded, err := json.Marshal(keyboard)
	if err != nil {
		_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
		return "", nil, err
	}
	return string(encoded), hashes, nil
}

func newTelegramActionToken() (string, string, error) {
	buf := make([]byte, telegramActionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain := hex.EncodeToString(buf)
	return plain, security.HashSecret(plain), nil
}

func (s *Server) sendTelegramChannelNotification(ctx context.Context, delivery model.NotificationDelivery) error {
	channel := delivery.Channel
	bot, err := s.globalTelegramBot(ctx)
	if err != nil {
		return err
	}
	bindings, err := s.store.ListTelegramBindingsByChannel(ctx, channel.ID)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return errors.New("Telegram 通知渠道尚未绑定账号")
	}
	title := strings.TrimSpace(delivery.Title)
	body := strings.TrimSpace(delivery.Body)
	specs := s.telegramActionsForDelivery(ctx, delivery)
	seen := map[int64]bool{}
	sent := 0
	failures := []string{}
	for _, binding := range bindings {
		if binding.ChatID == 0 || seen[binding.ChatID] {
			continue
		}
		seen[binding.ChatID] = true
		if len(specs) == 0 {
			config, _ := json.Marshal(map[string]string{"bot_token": bot.botToken, "chat_id": strconv.FormatInt(binding.ChatID, 10)})
			plain := channel
			plain.ConfigJSON = string(config)
			if err := s.notificationSender(ctx, plain, title, body); err != nil {
				failures = append(failures, err.Error())
				continue
			}
			sent++
			continue
		}
		markup, hashes, issueErr := s.issueTelegramKeyboard(ctx, binding.ChatID, 0, specs, telegramActionOfferTTL)
		if issueErr != nil {
			failures = append(failures, issueErr.Error())
			continue
		}
		messageID, sendErr := s.deliverTelegramMarkup(ctx, bot.botToken, channel, binding.ChatID, title, body, markup)
		if sendErr != nil {
			_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
			failures = append(failures, sendErr.Error())
			continue
		}
		if err := s.store.SetTelegramActionTokenMessages(ctx, hashes, messageID); err != nil {
			log.Printf("telegram action attach message: %v", err)
		}
		sent++
	}
	if sent == 0 {
		if len(failures) > 0 {
			return fmt.Errorf("Telegram 通知发送失败: %s", strings.Join(failures, "; "))
		}
		return errors.New("Telegram 通知渠道没有有效绑定")
	}
	return nil
}

func (s *Server) deliverTelegramMarkup(ctx context.Context, token string, channel model.NotificationChannel, chatID int64, title, body, markup string) (int64, error) {
	if s.telegramMarkupSend != nil {
		return s.telegramMarkupSend(ctx, channel, chatID, title, body, markup)
	}
	return s.telegramIncidentSend(ctx, token, chatID, telegramLimitText(title+"\n"+body), markup)
}

func (s *Server) editTelegramMarkup(ctx context.Context, token string, chatID, messageID int64, text, markup string) error {
	if s.telegramMarkupEdit != nil {
		return s.telegramMarkupEdit(ctx, chatID, messageID, text, markup)
	}
	return s.telegramIncidentEdit(ctx, token, chatID, messageID, telegramLimitText(text), markup)
}

func (s *Server) telegramActionsForDelivery(ctx context.Context, delivery model.NotificationDelivery) []telegramActionSpec {
	parsed := parseNotificationContext(delivery.ContextJSON)
	in := telegramActionInput{Event: delivery.Event, Context: parsed, Notice: strings.TrimSpace(delivery.Title + "\n" + delivery.Body)}
	if (delivery.Event == notificationCertificateFailed || delivery.Event == notificationCertificateExpiry) && parsed.CertificateID > 0 {
		if certificate, err := s.store.GetCertificate(ctx, parsed.CertificateID); err == nil && certificate != nil {
			in.CertificateIssueAllowed = certificateTelegramReissueAllowed(*certificate, time.Now())
		}
	}
	return telegramNotificationActionSpecs(in)
}

func (s *Server) handleTelegramActionCallback(ctx context.Context, channel telegramBotChannel, callback telegramCallbackQuery, rate *telegramBotRateLimiter) {
	token := strings.TrimPrefix(callback.Data, telegramActionPrefix)
	if _, err := hex.DecodeString(token); err != nil || len(token) != telegramActionTokenBytes*2 {
		return
	}
	if callback.From == nil || callback.Message == nil || callback.Message.Chat == nil {
		return
	}
	chatID := callback.Message.Chat.ID
	botToken := channel.botToken
	if !rate.allow(strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(callback.From.ID, 10)) {
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "请求频繁，请稍后重试")
		return
	}
	row, err := s.store.TelegramActionToken(ctx, security.HashSecret(token))
	if err != nil || row.ConsumedAt != nil || !row.ExpiresAt.After(time.Now()) || row.ChatID != chatID || (row.MessageID != 0 && callback.Message.MessageID != row.MessageID) {
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "按钮已失效")
		return
	}
	var payload telegramActionPayload
	if json.Unmarshal([]byte(row.PayloadJSON), &payload) != nil || payload.Action == "" {
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "按钮已失效")
		return
	}
	user, role, principal, err := s.telegramActionActor(ctx, chatID, callback.From.ID)
	if err != nil {
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, err.Error())
		return
	}
	if err := s.authorizeTelegramAction(principal, role, user, payload); err != nil {
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "权限不足")
		return
	}
	messageID := callback.Message.MessageID
	switch payload.Stage {
	case "cancel":
		if err := s.presentTelegramActionMessage(ctx, botToken, chatID, messageID, payload, ""); err != nil {
			s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "取消失败")
			return
		}
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "已取消")
	case "confirm":
		consumed, consumeErr := s.store.ConsumeTelegramActionToken(ctx, row.TokenHash, chatID, time.Now().UTC())
		if consumeErr != nil || consumed == nil {
			s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "确认已失效")
			return
		}
		text, execErr := s.executeTelegramAction(ctx, user, role, principal, payload, row.TokenHash)
		if execErr != nil {
			_ = s.presentTelegramActionMessage(ctx, botToken, chatID, messageID, payload, payload.Notice+"\n\n未执行："+execErr.Error())
			s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "执行失败")
			return
		}
		_ = s.editTelegramMarkup(ctx, botToken, chatID, messageID, payload.Notice+"\n\n"+text, `{"inline_keyboard":[]}`)
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "已执行")
	default:
		if telegramActionNeedsConfirm(payload.Action) {
			if err := s.showTelegramActionConfirm(ctx, botToken, chatID, messageID, user, role, payload); err != nil {
				s.telegramBotAnswerCallback(ctx, botToken, callback.ID, telegramCallbackToast(err.Error()))
				return
			}
			s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "请确认")
			return
		}
		text, execErr := s.executeTelegramAction(ctx, user, role, principal, payload, "")
		if execErr != nil {
			s.telegramBotAnswerCallback(ctx, botToken, callback.ID, telegramCallbackToast(execErr.Error()))
			return
		}
		if err := s.presentTelegramActionMessage(ctx, botToken, chatID, messageID, payload, payload.Notice+"\n\n"+text); err != nil {
			_ = s.editTelegramMarkup(ctx, botToken, chatID, messageID, payload.Notice+"\n\n"+text, "")
		}
		s.telegramBotAnswerCallback(ctx, botToken, callback.ID, "已更新")
	}
}

func (s *Server) showTelegramActionConfirm(ctx context.Context, token string, chatID, messageID int64, user model.User, role model.Role, payload telegramActionPayload) error {
	preview, err := s.telegramActionPreview(ctx, user, role, payload)
	if err != nil {
		return err
	}
	confirm := payload
	confirm.Stage = "confirm"
	cancel := payload
	cancel.Stage = "cancel"
	cancel.Action = "cancel"
	specs := []telegramActionSpec{
		{Label: "确认", Action: "confirm", Row: 0, Payload: confirm},
		{Label: "取消", Action: "cancel", Row: 0, Payload: cancel},
	}
	markup, hashes, err := s.issueTelegramKeyboard(ctx, chatID, messageID, specs, telegramActionConfirmTTL)
	if err != nil {
		return err
	}
	if err := s.editTelegramMarkup(ctx, token, chatID, messageID, payload.Notice+"\n\n"+preview, markup); err != nil {
		_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
		return err
	}
	return nil
}

func (s *Server) presentTelegramActionMessage(ctx context.Context, token string, chatID, messageID int64, payload telegramActionPayload, display string) error {
	if payload.IncidentID > 0 {
		event, err := s.store.GetNodeIncident(ctx, payload.IncidentID)
		if err != nil {
			return err
		}
		text := nodeIncidentTelegramText(*event)
		if strings.TrimSpace(display) != "" {
			text = display
		}
		markup, hashes, err := s.incidentTelegramMarkup(ctx, *event, chatID, messageID, nodeIncidentTelegramText(*event))
		if err != nil {
			return err
		}
		if err := s.editTelegramMarkup(ctx, token, chatID, messageID, text, markup); err != nil {
			_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
			return err
		}
		return nil
	}
	specs := s.telegramActionsForDelivery(ctx, model.NotificationDelivery{Event: payload.Event, Title: payload.Notice, ContextJSON: payload.ContextJSON})
	markup, hashes, err := s.issueTelegramKeyboard(ctx, chatID, messageID, specs, telegramActionOfferTTL)
	if err != nil {
		return err
	}
	text := payload.Notice
	if strings.TrimSpace(display) != "" {
		text = display
	}
	if err := s.editTelegramMarkup(ctx, token, chatID, messageID, text, markup); err != nil {
		_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
		return err
	}
	return nil
}

func (s *Server) telegramActionActor(ctx context.Context, chatID, telegramUserID int64) (model.User, model.Role, application.Principal, error) {
	binding, err := s.store.GetTelegramBindingForChat(ctx, chatID, telegramUserID)
	if err != nil {
		return model.User{}, "", application.Principal{}, errors.New("绑定已失效")
	}
	user, err := s.store.GetUser(ctx, binding.UserID)
	if err != nil || user.Status != "active" {
		return model.User{}, "", application.Principal{}, errors.New("账户不可用")
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		return model.User{}, "", application.Principal{}, errors.New("权限读取失败")
	}
	return *user, role, application.HumanPrincipal(*user, role, netip.Addr{}), nil
}

func (s *Server) authorizeTelegramAction(principal application.Principal, role model.Role, user model.User, payload telegramActionPayload) error {
	action := payload.Action
	if payload.Stage == "cancel" || payload.Stage == "confirm" {
		action = payload.Action
		if payload.Stage == "cancel" {
			return nil
		}
	}
	requireAdmin := func() error {
		if !roleAllows(role, model.RoleAdmin) {
			return errors.New("权限不足")
		}
		return nil
	}
	requireServer := func(serverID int64) error {
		if serverID <= 0 || !principal.AllowsInt64("server_ids", serverID) {
			return errors.New("超出授权范围")
		}
		return nil
	}
	requireCapability := func(name string) error {
		if _, allowed := s.capabilities.Authorize(principal, name); !allowed {
			return errors.New("权限不足")
		}
		return nil
	}
	switch action {
	case "view_server":
		if !roleAllows(role, model.RoleOperator) {
			return errors.New("权限不足")
		}
		if err := requireCapability("inventory.read"); err != nil {
			return err
		}
		return requireServer(payload.ServerID)
	case "refresh_audit":
		if payload.UserID <= 0 {
			return errors.New("权限不足")
		}
		if user.ID == payload.UserID {
			return nil
		}
		return requireAdmin()
	case "retry_delivery":
		if err := requireCapability("servers.delivery.retry"); err != nil {
			return err
		}
		return requireServer(payload.ServerID)
	case "issue_certificate":
		return requireCapability("certificates.issue")
	case "sync_dns", "recheck_time":
		if err := requireAdmin(); err != nil {
			return err
		}
		return requireServer(payload.ServerID)
	case "create_backup":
		return requireCapability("backups.create")
	case "extend_expiry":
		if !telegramExtendDaysAllowed(payload.Days) {
			return errors.New("延期天数无效")
		}
		if err := requireCapability("servers.extend_expiry"); err != nil {
			return err
		}
		return requireServer(payload.ServerID)
	case "incident_isolate":
		if err := requireCapability("node_incidents.isolate"); err != nil {
			return err
		}
		return requireServer(payload.ServerID)
	case "incident_remove":
		if err := requireCapability("inbounds.delete"); err != nil {
			return err
		}
		return requireServer(payload.ServerID)
	case "incident_restore":
		if err := requireCapability("node_incidents.restore"); err != nil {
			return err
		}
		return requireServer(payload.ServerID)
	case "cancel":
		return nil
	default:
		return errors.New("按钮已失效")
	}
}

func (s *Server) telegramActionPreview(ctx context.Context, user model.User, role model.Role, payload telegramActionPayload) (string, error) {
	switch payload.Action {
	case "retry_delivery":
		return "确认后将重新推送该服务器当前的授权和运行用户。这不会重新生成核心配置。", nil
	case "issue_certificate":
		certificate, err := s.store.GetCertificate(ctx, payload.CertificateID)
		if err != nil || certificate == nil || !certificateTelegramReissueAllowed(*certificate, time.Now()) {
			return "", errors.New("当前不能重新签发这张证书")
		}
		return "确认后将重新签发证书：" + certificate.Name, nil
	case "sync_dns":
		inbound, err := s.store.GetInbound(ctx, payload.InboundID)
		if err != nil || inbound == nil {
			return "", errors.New("入口不存在")
		}
		return "确认后将重试入口「" + inbound.Name + "」的域名记录同步。", nil
	case "recheck_time":
		return "确认后将立即再做一次时间检测。", nil
	case "create_backup":
		return "确认后将立即创建一次主控备份。", nil
	case "extend_expiry":
		if !telegramExtendDaysAllowed(payload.Days) {
			return "", errors.New("延期天数无效")
		}
		label := fmt.Sprintf("%d 天", payload.Days)
		if payload.Days == 365 {
			label = "1 年"
		}
		return "确认后将把服务器到期日延长 " + label + "。已有到期日从当前到期日顺延。", nil
	case "incident_isolate", "incident_remove":
		return s.telegramIncidentPreviewText(ctx, payload)
	case "incident_restore":
		items, err := s.store.ListNodePublicationIsolations(ctx, payload.IncidentID)
		if err != nil {
			return "", errors.New("剔除记录读取失败")
		}
		for _, item := range items {
			if item.ID == payload.IsolationID && item.Status == "hidden" {
				return "确认后将撤销对「" + item.InboundName + "」的临时剔除，订阅会重新包含该入口。", nil
			}
		}
		return "", errors.New("剔除记录已不存在")
	default:
		return "", errors.New("按钮已失效")
	}
}

func (s *Server) telegramIncidentPreviewText(ctx context.Context, payload telegramActionPayload) (string, error) {
	event, err := s.store.GetNodeIncident(ctx, payload.IncidentID)
	if err != nil || event.Status == model.NodeIncidentResolved || event.Version != payload.EventVersion {
		return "", errors.New("事件状态已变更")
	}
	action := "isolate"
	if payload.Action == "incident_remove" {
		action = "permanent_remove"
	}
	preview, err := s.nodeIncidentImpactPreview(ctx, *event, payload.InboundIDs, action, payload.RecoveryPolicy)
	if err != nil {
		return "", err
	}
	nodes, _ := preview["nodes"].([]nodeIncidentSnapshotInbound)
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, fmt.Sprintf("%s (#%d)", node.Name, node.ID))
	}
	verb := "临时剔除"
	if action == "permanent_remove" {
		verb = "永久移除"
	}
	return fmt.Sprintf("确认%s\n事件：#%d · %s\n入口：%s\n影响套餐：%v\n影响用户：%v", verb, event.ID, event.ServerName, strings.Join(names, "、"), preview["affected_plan_count"], preview["affected_user_count"]), nil
}

func (s *Server) executeTelegramAction(ctx context.Context, user model.User, role model.Role, principal application.Principal, payload telegramActionPayload, confirmToken string) (string, error) {
	switch payload.Action {
	case "view_server":
		if err := s.authorizeTelegramAction(principal, role, user, payload); err != nil {
			return "", err
		}
		return s.telegramBotServerDetail(ctx, strconv.FormatInt(payload.ServerID, 10)), nil
	case "refresh_audit":
		return s.telegramRefreshAudit(ctx, payload.UserID)
	case "retry_delivery":
		task, err := s.store.GetTask(ctx, payload.TaskID)
		if err != nil || task == nil || !taskTelegramRetryAllowed(task.Type) || task.ServerID != payload.ServerID {
			return "", errors.New("这个任务不能从通知里重试")
		}
		return s.applyTelegramChangeset(ctx, principal, "servers.delivery.retry", map[string]any{"server_id": payload.ServerID}, confirmToken, "Telegram 通知重试下发")
	case "issue_certificate":
		certificate, err := s.store.GetCertificate(ctx, payload.CertificateID)
		if err != nil || certificate == nil || !certificateTelegramReissueAllowed(*certificate, time.Now()) {
			return "", errors.New("当前不能重新签发这张证书")
		}
		return s.applyTelegramChangeset(ctx, principal, "certificates.issue", map[string]any{"certificate_id": payload.CertificateID}, confirmToken, "Telegram 通知重新签发证书")
	case "sync_dns":
		return s.telegramRetryDNS(ctx, principal, user, payload.InboundID)
	case "recheck_time":
		return s.telegramRecheckTime(ctx, principal, user, payload.ServerID)
	case "create_backup":
		return s.applyTelegramChangeset(ctx, principal, "backups.create", map[string]any{}, confirmToken, "Telegram 通知立即备份")
	case "extend_expiry":
		if !telegramExtendDaysAllowed(payload.Days) {
			return "", errors.New("延期天数无效")
		}
		return s.applyTelegramChangeset(ctx, principal, "servers.extend_expiry", map[string]any{"server_id": payload.ServerID, "days": payload.Days}, confirmToken, "Telegram 通知延长服务器到期")
	case "incident_isolate", "incident_remove":
		action := "isolate"
		if payload.Action == "incident_remove" {
			action = "permanent_remove"
		}
		return s.executeNodeIncidentAction(ctx, user, role, nodeIncidentConfirmationPayload{EventID: payload.IncidentID, EventVersion: payload.EventVersion, Action: action, InboundIDs: payload.InboundIDs, RecoveryPolicy: payload.RecoveryPolicy, DurationMinutes: payload.DurationMinutes, ChatID: 0, TelegramUserID: user.ID}, confirmToken)
	case "incident_restore":
		return s.applyTelegramChangeset(ctx, principal, "node_incidents.restore", map[string]any{"event_id": payload.IncidentID, "isolation_id": payload.IsolationID}, confirmToken, "Telegram 通知撤销节点剔除")
	default:
		return "", errors.New("按钮已失效")
	}
}

func (s *Server) applyTelegramChangeset(ctx context.Context, principal application.Principal, capability string, input any, confirmToken, reason string) (string, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	if confirmToken == "" {
		confirmToken, _, err = newTelegramActionToken()
		if err != nil {
			return "", err
		}
	}
	changeset, err := s.applyConfirmedChangeset(ctx, principal, []automation.OperationRequest{{Capability: capability, Input: raw, ResourceRefs: json.RawMessage(`{}`)}}, "telegram-action:"+confirmToken, reason)
	if err != nil {
		return "", err
	}
	if changeset == nil {
		return "", errors.New("变更未完成")
	}
	return "已提交\n变更集：" + changeset.ID, nil
}

func (s *Server) telegramRetryDNS(ctx context.Context, principal application.Principal, user model.User, inboundID int64) (string, error) {
	inbound, err := s.store.GetInbound(ctx, inboundID)
	if err != nil || inbound == nil {
		return "", errors.New("入口不存在")
	}
	if !principal.AllowsInt64("server_ids", inbound.ServerID) {
		return "", errors.New("超出授权范围")
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "", err
	}
	results, err := s.syncDNSInbounds(ctx, servers, []model.Inbound{*inbound})
	if err != nil {
		return "", err
	}
	_ = s.store.AddAudit(ctx, model.AuditLog{ActorID: &user.ID, Action: "sync", Target: "dns", Detail: strconv.FormatInt(inbound.ID, 10), IP: "telegram"})
	if len(results) == 0 {
		return "域名同步已执行", nil
	}
	return "域名同步完成：" + results[0].Status, nil
}

func (s *Server) telegramRecheckTime(ctx context.Context, principal application.Principal, user model.User, serverID int64) (string, error) {
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil || server == nil {
		return "", errors.New("服务器不存在")
	}
	if !principal.AllowsInt64("server_ids", server.ID) {
		return "", errors.New("超出授权范围")
	}
	if strings.TrimSpace(server.AgentID) == "" {
		return "", errors.New("服务器尚未接入")
	}
	if server.Status != model.ServerOnline {
		return "", errors.New("服务器当前离线，恢复在线后会自动检测")
	}
	task, err := s.queueTimeCheck(ctx, *server, true)
	if err != nil {
		return "", err
	}
	_ = s.store.AddAudit(ctx, model.AuditLog{ActorID: &user.ID, Action: "check_time", Target: "server", Detail: strconv.FormatInt(server.ID, 10), IP: "telegram"})
	return fmt.Sprintf("已安排时间检测\n任务：#%d", task.ID), nil
}

func (s *Server) telegramRefreshAudit(ctx context.Context, userID int64) (string, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil || user == nil {
		return "", errors.New("账户不存在")
	}
	raw, err := s.store.AccountAuditSnapshotJSON(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "尚无已保存的风险快照。\n仅供核实，不会限制账号。", nil
		}
		return "", errors.New("风险快照读取失败")
	}
	var snapshot auditrisk.Snapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return "", errors.New("风险快照无法读取")
	}
	name := strings.TrimSpace(user.Nickname)
	if name == "" {
		name = user.Username
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "账户：%s\n快照：%s\n", name, snapshot.Status)
	writeScore := func(label string, score *auditrisk.Score) {
		if score == nil {
			fmt.Fprintf(&builder, "%s：暂无\n", label)
			return
		}
		fmt.Fprintf(&builder, "%s：%s（下界 %d）\n", label, telegramAuditLevel(score.Level), score.Lower)
	}
	writeScore("活动", snapshot.Activity)
	writeScore("订阅", snapshot.Exposure)
	if !snapshot.AsOf.IsZero() {
		fmt.Fprintf(&builder, "时间：%s\n", snapshot.AsOf.Local().Format("2006-01-02 15:04"))
	}
	builder.WriteString("仅供核实，不会限制账号。")
	return builder.String(), nil
}

func telegramAuditLevel(level string) string {
	if label := map[string]string{"low": "低风险", "medium": "中风险", "high": "高风险", "very_high": "极高风险"}[level]; label != "" {
		return label
	}
	if strings.TrimSpace(level) == "" {
		return "未分级"
	}
	return level
}

func (s *Server) incidentTelegramMarkup(ctx context.Context, item model.NodeIncident, chatID, messageID int64, notice string) (string, []string, error) {
	published := s.nodeIncidentPublishedInbounds(item)
	ids := make([]int64, 0, len(published))
	for _, inbound := range published {
		ids = append(ids, inbound.ID)
	}
	isolations, err := s.store.ListNodePublicationIsolations(ctx, item.ID)
	if err != nil {
		return "", nil, err
	}
	hidden := make([]struct {
		ID   int64
		Name string
	}, 0)
	for _, isolation := range isolations {
		if isolation.Status != "hidden" {
			continue
		}
		hidden = append(hidden, struct {
			ID   int64
			Name string
		}{ID: isolation.ID, Name: isolation.InboundName})
	}
	specs := telegramIncidentActionSpecs(telegramIncidentButtonInput{ID: item.ID, Version: item.Version, ServerID: item.ServerID, Status: string(item.Status), Published: ids, Isolations: hidden, Notice: notice})
	return s.issueTelegramKeyboard(ctx, chatID, messageID, specs, telegramActionOfferTTL)
}

func (s *Server) executeNodeIncidentAction(ctx context.Context, user model.User, role model.Role, payload nodeIncidentConfirmationPayload, idempotencyKey string) (string, error) {
	event, err := s.store.GetNodeIncident(ctx, payload.EventID)
	if err != nil || event.Status == model.NodeIncidentResolved || event.Version != payload.EventVersion {
		return "", errors.New("事件状态已变更")
	}
	principal := application.HumanPrincipal(user, role, netip.Addr{})
	capabilityName := "node_incidents.isolate"
	if payload.Action == "permanent_remove" {
		capabilityName = "inbounds.delete"
	} else if payload.Action != "isolate" {
		return "", errors.New("处置类型无效")
	}
	if _, allowed := s.capabilities.Authorize(principal, capabilityName); !allowed || !principal.AllowsInt64("server_ids", event.ServerID) {
		return "", errors.New("权限不足")
	}
	operations := []automation.OperationRequest{}
	if payload.Action == "isolate" {
		input, _ := json.Marshal(nodeIncidentIsolationOperation{EventID: event.ID, EventVersion: event.Version, InboundIDs: payload.InboundIDs, RecoveryPolicy: payload.RecoveryPolicy, DurationMinutes: payload.DurationMinutes})
		operations = append(operations, automation.OperationRequest{Capability: "node_incidents.isolate", Input: input, ResourceRefs: json.RawMessage(`{}`)})
	} else {
		for _, inboundID := range payload.InboundIDs {
			input, _ := json.Marshal(map[string]any{"inbound_id": inboundID, "confirm": true})
			operations = append(operations, automation.OperationRequest{Capability: "inbounds.delete", Input: input, ResourceRefs: json.RawMessage(`{}`)})
		}
	}
	changeset, err := s.applyConfirmedNodeChangeset(ctx, principal, operations, idempotencyKey)
	if err != nil {
		return "", err
	}
	if changeset == nil {
		return "", errors.New("变更未完成")
	}
	if payload.Action == "permanent_remove" {
		_ = s.store.MarkNodePublicationIsolationsRemoved(ctx, payload.InboundIDs, user.ID)
		tasks, version, deployErr := s.deployConfiguration(ctx, 0, true)
		idsJSON, _ := json.Marshal(payload.InboundIDs)
		action := model.NodeIncidentAction{IncidentID: event.ID, ActorUserID: user.ID, Kind: "permanent_remove", Status: "deployment_pending", InboundIDsJSON: string(idsJSON), ChangesetID: changeset.ID, ConfigVersion: version, TaskCount: len(tasks)}
		if deployErr != nil {
			action.Status = "failed"
			action.Error = deployErr.Error()
			_ = s.store.CreateNodeIncidentAction(ctx, &action)
			return fmt.Sprintf("入口已移除，部署任务创建失败：%s", deployErr.Error()), nil
		}
		if err := s.store.CreateNodeIncidentAction(ctx, &action); err != nil {
			return "", errors.New("处置状态保存失败")
		}
		return fmt.Sprintf("永久移除已提交\n变更集：%s\n处置记录：#%d\n配置版本：%d\n部署任务：%d 个", changeset.ID, action.ID, version, len(tasks)), nil
	}
	return fmt.Sprintf("临时剔除已生效\n变更集：%s\n无需下发配置。", changeset.ID), nil
}

func telegramCallbackToast(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= 180 {
		return text
	}
	return string(runes[:180])
}
