package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	telegramBotRateLimit     = 20
	telegramBotMaxBodyBytes  = 1 << 20
	telegramBotConfigSetting = "telegram_bot_config"
	telegramBotConfigPurpose = "telegram-bot-config"
)

var errTelegramBotNotConfigured = errors.New("管理员尚未配置并启用 Telegram Bot")

type telegramBotConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
}

type telegramBotChannel struct {
	botToken string
}

func (s *Server) telegramBotConfig(ctx context.Context) (telegramBotConfig, error) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return telegramBotConfig{}, err
	}
	wrapped := strings.TrimSpace(settings[telegramBotConfigSetting])
	if wrapped == "" {
		return telegramBotConfig{}, nil
	}
	plain, err := security.DecryptSecret(s.sessionSecret, telegramBotConfigPurpose, wrapped)
	if err != nil {
		return telegramBotConfig{}, errors.New("Telegram Bot 配置无法解密")
	}
	var config telegramBotConfig
	if err := json.Unmarshal([]byte(plain), &config); err != nil {
		return telegramBotConfig{}, errors.New("Telegram Bot 配置无效")
	}
	config.BotToken = strings.TrimSpace(config.BotToken)
	return config, nil
}

func (s *Server) saveTelegramBotConfig(ctx context.Context, config telegramBotConfig) error {
	config.BotToken = strings.TrimSpace(config.BotToken)
	if config.Enabled && config.BotToken == "" {
		return errors.New("启用 Telegram Bot 前请填写 Bot Token")
	}
	plain, err := json.Marshal(config)
	if err != nil {
		return err
	}
	wrapped, err := security.EncryptSecret(s.sessionSecret, telegramBotConfigPurpose, string(plain))
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, telegramBotConfigSetting, wrapped)
}

func (s *Server) globalTelegramBot(ctx context.Context) (*telegramBotChannel, error) {
	config, err := s.telegramBotConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !config.Enabled || config.BotToken == "" {
		return nil, errTelegramBotNotConfigured
	}
	return &telegramBotChannel{botToken: config.BotToken}, nil
}

func (s *Server) telegramBotPublicStatus(ctx context.Context) map[string]any {
	config, err := s.telegramBotConfig(ctx)
	if err != nil {
		return map[string]any{"configured": false, "enabled": false, "token_configured": false, "error": err.Error()}
	}
	tokenConfigured := config.BotToken != ""
	return map[string]any{
		"configured":       config.Enabled && tokenConfigured,
		"enabled":          config.Enabled,
		"token_configured": tokenConfigured,
	}
}

func (s *Server) telegramBotSettings(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/test") {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		var request struct {
			BotToken string `json:"bot_token"`
		}
		if !decode(w, r, &request) {
			return
		}
		token := strings.TrimSpace(request.BotToken)
		if token == "" {
			config, err := s.telegramBotConfig(r.Context())
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			token = config.BotToken
		}
		if token == "" {
			fail(w, errors.New("请先填写 Bot Token"), http.StatusBadRequest)
			return
		}
		profile, err := s.telegramBotGetMe(r.Context(), token)
		if err != nil {
			fail(w, fmt.Errorf("Telegram Bot 验证失败: %w", err), http.StatusBadGateway)
			return
		}
		write(w, http.StatusOK, map[string]any{"ok": true, "username": profile.Username, "name": profile.Name})
		return
	}

	switch r.Method {
	case http.MethodGet:
		write(w, http.StatusOK, map[string]any{"telegram_bot": s.telegramBotPublicStatus(r.Context())})
	case http.MethodPut:
		var request struct {
			Enabled  bool   `json:"enabled"`
			BotToken string `json:"bot_token"`
		}
		if !decode(w, r, &request) {
			return
		}
		current, err := s.telegramBotConfig(r.Context())
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		if strings.TrimSpace(request.BotToken) != "" {
			current.BotToken = request.BotToken
		}
		current.Enabled = request.Enabled
		if err := s.saveTelegramBotConfig(r.Context(), current); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		auditReq(s, r, "update", "telegram_bot", "global")
		write(w, http.StatusOK, map[string]any{"telegram_bot": s.telegramBotPublicStatus(r.Context())})
	default:
		method(w)
	}
}

type telegramBotProfile struct {
	Username string
	Name     string
}

func (s *Server) telegramBotGetMe(ctx context.Context, token string) (telegramBotProfile, error) {
	data, err := s.telegramAPI(ctx, http.MethodGet, "https://api.telegram.org/bot"+strings.TrimSpace(token)+"/getMe", nil)
	if err != nil {
		return telegramBotProfile{}, err
	}
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &response) != nil || !response.OK {
		return telegramBotProfile{}, errors.New("Telegram API 返回无效结果")
	}
	return telegramBotProfile{Username: strings.TrimSpace(response.Result.Username), Name: strings.TrimSpace(response.Result.FirstName)}, nil
}

func (s *Server) StartTelegramBots(ctx context.Context) {
	go s.telegramBotPollLoop(ctx)
}

func (s *Server) telegramBotPollLoop(ctx context.Context) {
	rate := &telegramBotRateLimiter{counts: map[string][]time.Time{}}
	for {
		if ctx.Err() != nil {
			return
		}
		bot, err := s.globalTelegramBot(ctx)
		if err != nil {
			if !errors.Is(err, errTelegramBotNotConfigured) {
				s.logPeriodicError("telegram-global-bot", "global Telegram bot unavailable: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}
		token := bot.botToken
		tokenHash := security.HashSecret("telegram-bot:" + token)
		offset, claimed, err := s.store.ClaimTelegramBotPoll(ctx, tokenHash, s.telegramPollerID, time.Now().UTC(), 40*time.Second)
		if err != nil {
			log.Printf("telegram bot poll lease failed: %v", err)
		} else if claimed {
			updates, pollErr := s.telegramBotGetUpdates(ctx, token, offset)
			if pollErr != nil {
				log.Printf("telegram bot getUpdates failed: %v", pollErr)
			} else {
				for _, update := range updates {
					nextOffset := update.UpdateID + 1
					if nextOffset > offset {
						offset = nextOffset
						if err := s.store.SaveTelegramBotOffset(ctx, tokenHash, s.telegramPollerID, offset); err != nil {
							log.Printf("telegram bot save offset failed: %v", err)
							break
						}
					}
					s.handleTelegramUpdate(ctx, *bot, update, rate)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

type telegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *telegramMessage       `json:"message"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query"`
}

type telegramCallbackQuery struct {
	ID   string `json:"id"`
	From *struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message *telegramMessage `json:"message"`
	Data    string           `json:"data"`
}

type telegramMessage struct {
	MessageID int64 `json:"message_id"`
	From      *struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat *struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text string `json:"text"`
}

func (s *Server) telegramBotGetUpdates(ctx context.Context, token string, offset int64) ([]telegramUpdate, error) {
	query := url.Values{}
	query.Set("timeout", "25")
	query.Set("allowed_updates", `["message","callback_query"]`)
	if offset > 0 {
		query.Set("offset", strconv.FormatInt(offset, 10))
	}
	target := "https://api.telegram.org/bot" + token + "/getUpdates?" + query.Encode()
	data, err := s.telegramAPI(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if !payload.OK {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}
	return payload.Result, nil
}

func (s *Server) handleTelegramUpdate(ctx context.Context, channel telegramBotChannel, update telegramUpdate, rate *telegramBotRateLimiter) {
	token := channel.botToken
	if update.CallbackQuery != nil {
		s.handleTelegramCallback(ctx, channel, *update.CallbackQuery, rate)
		return
	}
	message := update.Message
	if message == nil || message.Chat == nil || message.From == nil || strings.TrimSpace(message.Text) == "" {
		return
	}
	chatKey := strconv.FormatInt(message.Chat.ID, 10)
	if !rate.allow(chatKey + ":" + strconv.FormatInt(message.From.ID, 10)) {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "请求频繁，请于一分钟后重试。")
		return
	}
	command, arg := parseTelegramCommand(message.Text)
	if command == "bind" || command == "绑定" {
		channelID, code, ok := parseTelegramBindArgument(arg)
		if !ok {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "格式：/bind <通知渠道ID> <绑定码>\n请在 OBoard 通知页生成绑定码。")
			return
		}
		binding, err := s.store.ConsumeTelegramBindingCode(ctx, security.HashSecret(code), channelID, message.Chat.ID, message.From.ID, message.Chat.Type, time.Now().UTC())
		if err != nil {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "绑定码无效或已过期。")
			return
		}
		user, _ := s.store.GetUser(ctx, binding.UserID)
		name := user.Username
		if strings.TrimSpace(user.Nickname) != "" {
			name = user.Nickname
		}
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "绑定成功\n账户："+name+"\n发送 /help 查看可用指令。")
		return
	}
	binding, err := s.store.GetTelegramBindingForChat(ctx, message.Chat.ID, message.From.ID)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "当前会话未绑定或绑定已失效。\n请在 OBoard 通知页重新生成绑定码。")
		return
	}
	if command == "unbind" || command == "解绑" {
		if err := s.store.DeleteTelegramBindingsForChat(ctx, message.Chat.ID, message.From.ID); err != nil {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "解绑失败，请稍后重试。")
			return
		}
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "当前会话已解绑。")
		return
	}
	user, err := s.store.GetUser(ctx, binding.UserID)
	if err != nil || user.Status != "active" {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "账户不存在或已停用。")
		return
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "权限读取失败，请稍后重试。")
		return
	}
	if command == "incident" || command == "事件" {
		if !roleAllows(role, model.RoleOperator) {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "权限不足，无法处置节点事件。")
			return
		}
		s.telegramBotIncidentPreview(ctx, token, message.Chat.ID, message.From.ID, *user, role, arg)
		return
	}
	reply := s.telegramBotReplyForUser(ctx, *user, role, strings.TrimSpace(message.Text))
	s.telegramBotSendMessage(ctx, token, message.Chat.ID, reply)
}

func (s *Server) telegramBotIncidentPreview(ctx context.Context, token string, chatID, telegramUserID int64, user model.User, role model.Role, arg string) {
	fields := strings.Fields(strings.TrimSpace(arg))
	usage := "格式：\n/incident <事件ID> isolate <manual|auto> <入口ID列表>\n/incident <事件ID> remove <入口ID列表>"
	if len(fields) < 3 {
		s.telegramBotSendMessage(ctx, token, chatID, usage)
		return
	}
	eventID, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || eventID <= 0 {
		s.telegramBotSendMessage(ctx, token, chatID, usage)
		return
	}
	action := strings.ToLower(fields[1])
	recoveryPolicy := ""
	idsField := ""
	capabilityName := "node_incidents.isolate"
	if action == "isolate" && len(fields) == 4 {
		recoveryPolicy = strings.ToLower(fields[2])
		idsField = fields[3]
	} else if action == "remove" && len(fields) == 3 {
		action = "permanent_remove"
		capabilityName = "inbounds.delete"
		idsField = fields[2]
	} else {
		s.telegramBotSendMessage(ctx, token, chatID, usage)
		return
	}
	inboundIDs := []int64{}
	for _, raw := range strings.Split(idsField, ",") {
		id, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if parseErr != nil || id <= 0 {
			s.telegramBotSendMessage(ctx, token, chatID, "入口 ID 列表无效。\n"+usage)
			return
		}
		inboundIDs = append(inboundIDs, id)
	}
	principal := application.HumanPrincipal(user, role, netip.Addr{})
	if _, allowed := s.capabilities.Authorize(principal, capabilityName); !allowed {
		s.telegramBotSendMessage(ctx, token, chatID, "权限不足，无法执行此项处置。")
		return
	}
	event, err := s.store.GetNodeIncident(ctx, eventID)
	if err != nil || event.Status == model.NodeIncidentResolved || !principal.AllowsInt64("server_ids", event.ServerID) {
		s.telegramBotSendMessage(ctx, token, chatID, "事件不存在、已关闭或超出授权范围。")
		return
	}
	preview, err := s.nodeIncidentImpactPreview(ctx, *event, inboundIDs, action, recoveryPolicy)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, chatID, "影响评估失败："+err.Error())
		return
	}
	payload := nodeIncidentConfirmationPayload{EventID: event.ID, EventVersion: event.Version, Action: action, InboundIDs: preview["inbound_ids"].([]int64), RecoveryPolicy: recoveryPolicy, ChatID: chatID, TelegramUserID: telegramUserID}
	payloadJSON, _ := json.Marshal(payload)
	confirmation, err := security.RandomToken(18)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, chatID, "确认请求创建失败，请稍后重试。")
		return
	}
	if err := s.store.CreateOperationConfirmation(ctx, security.HashSecret(confirmation), capabilityName, event.ID, event.Version, user.ID, string(payloadJSON), time.Now().UTC().Add(5*time.Minute)); err != nil {
		s.telegramBotSendMessage(ctx, token, chatID, "确认请求保存失败，请稍后重试。")
		return
	}
	nodes, _ := preview["nodes"].([]nodeIncidentSnapshotInbound)
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, fmt.Sprintf("%s (#%d)", node.Name, node.ID))
	}
	text := fmt.Sprintf("处置确认\n事件：#%d · %s\n入口：%s\n影响套餐：%d\n影响用户：%d\n现有连接：%s\n有效期：5 分钟", event.ID, event.ServerName, strings.Join(names, "、"), preview["affected_plan_count"], preview["affected_user_count"], map[bool]string{true: "受影响", false: "不受影响"}[action == "permanent_remove"])
	markup := fmt.Sprintf(`{"inline_keyboard":[[{"text":"确认处置","callback_data":"confirm:%s"}]]}`, confirmation)
	s.telegramBotSendMessageMarkup(ctx, token, chatID, text, markup)
}

func (s *Server) handleTelegramCallback(ctx context.Context, channel telegramBotChannel, callback telegramCallbackQuery, rate *telegramBotRateLimiter) {
	token := channel.botToken
	if callback.From == nil || callback.Message == nil || callback.Message.Chat == nil || !strings.HasPrefix(callback.Data, "confirm:") {
		return
	}
	chatID := callback.Message.Chat.ID
	if !rate.allow(strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(callback.From.ID, 10)) {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "请求频繁，请稍后重试")
		return
	}
	binding, err := s.store.GetTelegramBindingForChat(ctx, chatID, callback.From.ID)
	if err != nil {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "绑定已失效")
		return
	}
	user, err := s.store.GetUser(ctx, binding.UserID)
	if err != nil || user.Status != "active" {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "账户不可用")
		return
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "权限读取失败")
		return
	}
	confirmation, err := s.store.ConsumeOperationConfirmationToken(ctx, security.HashSecret(strings.TrimPrefix(callback.Data, "confirm:")), user.ID, time.Now().UTC())
	if err != nil {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "确认已失效或无权执行")
		return
	}
	var payload nodeIncidentConfirmationPayload
	if json.Unmarshal([]byte(confirmation.PayloadJSON), &payload) != nil || payload.EventID != confirmation.EventID || payload.EventVersion != confirmation.EventVersion || payload.ChatID != chatID || payload.TelegramUserID != callback.From.ID {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "确认数据无效")
		return
	}
	event, err := s.store.GetNodeIncident(ctx, payload.EventID)
	if err != nil || event.Status == model.NodeIncidentResolved || event.Version != payload.EventVersion {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "事件状态已变更")
		return
	}
	principal := application.HumanPrincipal(*user, role, netip.Addr{})
	if _, allowed := s.capabilities.Authorize(principal, confirmation.Capability); !allowed || !principal.AllowsInt64("server_ids", event.ServerID) {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "权限不足")
		return
	}
	operations := []automation.OperationRequest{}
	if payload.Action == "isolate" {
		input, _ := json.Marshal(nodeIncidentIsolationOperation{EventID: event.ID, EventVersion: event.Version, InboundIDs: payload.InboundIDs, RecoveryPolicy: payload.RecoveryPolicy})
		operations = append(operations, automation.OperationRequest{Capability: "node_incidents.isolate", Input: input, ResourceRefs: json.RawMessage(`{}`)})
	} else if payload.Action == "permanent_remove" {
		for _, inboundID := range payload.InboundIDs {
			input, _ := json.Marshal(map[string]any{"inbound_id": inboundID, "confirm": true})
			operations = append(operations, automation.OperationRequest{Capability: "inbounds.delete", Input: input, ResourceRefs: json.RawMessage(`{}`)})
		}
	} else {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "处置类型无效")
		return
	}
	changeset, err := s.applyConfirmedNodeChangeset(ctx, principal, operations, security.HashSecret(callback.Data))
	if err != nil {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "处置失败")
		s.telegramBotSendMessage(ctx, token, chatID, "处置失败："+err.Error())
		return
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
			s.telegramBotAnswerCallback(ctx, token, callback.ID, "部署任务创建失败")
			s.telegramBotSendMessage(ctx, token, chatID, "入口已移除，部署任务创建失败："+deployErr.Error())
			return
		}
		if err := s.store.CreateNodeIncidentAction(ctx, &action); err != nil {
			s.telegramBotAnswerCallback(ctx, token, callback.ID, "处置状态保存失败")
			return
		}
		s.telegramBotSendMessage(ctx, token, chatID, fmt.Sprintf("永久移除已提交\n变更集：%s\n处置记录：#%d\n配置版本：%d\n部署任务：%d 个", changeset.ID, action.ID, version, len(tasks)))
	} else {
		s.telegramBotSendMessage(ctx, token, chatID, fmt.Sprintf("临时剔除已生效\n变更集：%s\n无需下发配置。", changeset.ID))
	}
	s.telegramBotAnswerCallback(ctx, token, callback.ID, "处置已确认")
}

func (s *Server) telegramBotSendMessageMarkup(ctx context.Context, token string, chatID int64, text, markup string) {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	form.Set("reply_markup", markup)
	if _, err := s.telegramAPI(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", form); err != nil {
		log.Printf("telegram bot sendMessage with markup failed: %v", err)
	}
}

func (s *Server) telegramBotAnswerCallback(ctx context.Context, token, callbackID, text string) {
	form := url.Values{}
	form.Set("callback_query_id", callbackID)
	form.Set("text", text)
	if _, err := s.telegramAPI(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/answerCallbackQuery", form); err != nil {
		log.Printf("telegram bot answerCallbackQuery failed: %v", err)
	}
}

func (s *Server) telegramBotReplyForUser(ctx context.Context, user model.User, role model.Role, text string) string {
	command, arg := parseTelegramCommand(text)
	adminAccess := roleAllows(role, model.RoleOperator)
	principal := application.HumanPrincipal(user, role, netip.Addr{})
	if _, allowed := s.capabilities.Authorize(principal, "inventory.read"); !allowed && adminAccess {
		adminAccess = false
	}
	switch command {
	case "help", "start", "菜单", "帮助":
		if adminAccess {
			return telegramBotHelpText() + "\n/unbind 解绑当前会话"
		}
		return "OBoard 账户指令\n/account 账户摘要\n/status 可用节点状态\n/announcements 管理员公告\n/unbind 解绑当前会话"
	case "account", "me", "账户", "我的":
		return s.telegramBotAccount(ctx, user)
	case "announcements", "公告":
		return s.telegramBotAnnouncements(ctx, user.ID)
	case "status", "状态":
		if adminAccess {
			return s.telegramBotServersStatus(ctx)
		}
		return s.telegramBotOwnNodes(ctx, user)
	case "servers", "服务器":
		if !adminAccess {
			return "权限不足，无法查看全局服务器状态。"
		}
		return s.telegramBotServersStatus(ctx)
	case "server", "服务器详情":
		if !adminAccess {
			return "权限不足，无法查看服务器详情。"
		}
		return s.telegramBotServerDetail(ctx, arg)
	case "traffic", "流量":
		if !adminAccess {
			return s.telegramBotAccount(ctx, user)
		}
		return s.telegramBotTraffic(ctx)
	case "users", "用户", "使用情况":
		if !adminAccess {
			return "权限不足，无法查看其他用户。"
		}
		return s.telegramBotUsers(ctx)
	case "audit", "审计":
		if !adminAccess {
			return "权限不足，无法查看审计信息。"
		}
		if _, allowed := s.capabilities.Authorize(principal, "audit.risk_overview"); !allowed {
			return "权限不足，无法查看审计信息。"
		}
		return s.telegramBotAudit(ctx)
	default:
		return "指令无效。发送 /help 查看可用指令。"
	}
}

func (s *Server) telegramBotAccount(ctx context.Context, user model.User) string {
	name := strings.TrimSpace(user.Nickname)
	if name == "" {
		name = user.Username
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "账户：%s\n状态：%s\n", name, map[bool]string{true: "正常", false: "已停用"}[user.Status == "active"])
	if binding, err := s.store.GetActiveUserPlanBinding(ctx, user.ID); err == nil {
		planName := fmt.Sprintf("套餐 #%d", binding.PlanID)
		if plan, planErr := s.store.GetSubscriptionPlan(ctx, binding.PlanID); planErr == nil {
			planName = plan.Name
		}
		fmt.Fprintf(&builder, "套餐：%s\n", planName)
		if binding.ExpiresAt != nil {
			fmt.Fprintf(&builder, "有效期至：%s\n", binding.ExpiresAt.Local().Format("2006-01-02 15:04"))
		} else {
			builder.WriteString("有效期：长期\n")
		}
	} else {
		builder.WriteString("套餐：未分配\n")
	}
	if user.TrafficLimitBytes > 0 {
		fmt.Fprintf(&builder, "流量：%s / %s\n", formatNotificationBytes(user.TrafficUsedBytes), formatNotificationBytes(user.TrafficLimitBytes))
	} else {
		fmt.Fprintf(&builder, "流量：已用 %s（不限量）\n", formatNotificationBytes(user.TrafficUsedBytes))
	}
	devices, err := s.store.ListUserDevices(ctx, user.ID)
	if err == nil {
		active := 0
		for _, device := range devices {
			if device.Status == "active" {
				active++
			}
		}
		fmt.Fprintf(&builder, "有效设备：%d", active)
		if user.DeviceLimit > 0 {
			fmt.Fprintf(&builder, " / 上限 %d", user.DeviceLimit)
		}
	}
	return builder.String()
}

func (s *Server) telegramBotOwnNodes(ctx context.Context, user model.User) string {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return "节点状态查询失败，请稍后重试。"
	}
	snapshot, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return "节点状态查询失败，请稍后重试。"
	}
	effective := snapshot.EffectiveNodeKeys(user.ID)
	hidden, _ := s.store.ListHiddenInboundIDs(ctx)
	servers := map[int64]model.Server{}
	for _, server := range data.Servers {
		servers[server.ID] = server
	}
	var lines []string
	for _, inbound := range data.Inbounds {
		if !effective[core.NodeKeyOf(model.AssignableNodeInbound, inbound.ID)] || hidden[inbound.ID] {
			continue
		}
		server := servers[inbound.ServerID]
		lines = append(lines, fmt.Sprintf("%s · %s", inbound.Name, telegramServerStatusLabel(server.Status)))
	}
	for _, path := range data.ProxyPaths {
		if !effective[core.NodeKeyOf(model.AssignableNodeProxyPath, path.ID)] || hidden[path.InboundID] {
			continue
		}
		root := model.Inbound{}
		for _, inbound := range data.Inbounds {
			if inbound.ID == path.InboundID {
				root = inbound
				break
			}
		}
		server := servers[root.ServerID]
		lines = append(lines, fmt.Sprintf("%s · %s", path.Name, telegramServerStatusLabel(server.Status)))
	}
	if len(lines) == 0 {
		return "当前无可用节点。"
	}
	sort.Strings(lines)
	total := len(lines)
	if len(lines) > 50 {
		lines = append(lines[:50], "仅显示前 50 个节点。")
	}
	return fmt.Sprintf("可用节点：%d\n%s", total, strings.Join(lines, "\n"))
}

func (s *Server) telegramBotAnnouncements(ctx context.Context, userID int64) string {
	items, err := s.store.ListNotificationAnnouncementsForUser(ctx, userID, 10)
	if err != nil {
		return "公告查询失败，请稍后重试。"
	}
	if len(items) == 0 {
		return "当前无管理员公告。"
	}
	var builder strings.Builder
	builder.WriteString("管理员公告\n")
	for _, item := range items {
		fmt.Fprintf(&builder, "\n%s\n%s\n%s\n", item.Title, item.Body, item.CreatedAt.Local().Format("01-02 15:04"))
	}
	return strings.TrimSpace(builder.String())
}

func (s *Server) telegramBotReply(ctx context.Context, text string) string {
	command, arg := parseTelegramCommand(text)
	switch command {
	case "help", "start", "菜单", "帮助":
		return telegramBotHelpText()
	case "status", "servers", "状态", "服务器":
		return s.telegramBotServersStatus(ctx)
	case "server", "服务器详情":
		return s.telegramBotServerDetail(ctx, arg)
	case "traffic", "流量":
		return s.telegramBotTraffic(ctx)
	case "users", "用户", "使用情况":
		return s.telegramBotUsers(ctx)
	case "audit", "审计":
		return s.telegramBotAudit(ctx)
	default:
		return "指令无效。发送 /help 查看可用指令。"
	}
}

func parseTelegramBindArgument(value string) (int64, string, bool) {
	parts := strings.Fields(strings.TrimSpace(value))
	if len(parts) != 2 {
		return 0, "", false
	}
	channelID, err := strconv.ParseInt(parts[0], 10, 64)
	code := strings.TrimSpace(parts[1])
	return channelID, code, err == nil && channelID > 0 && code != ""
}

func parseTelegramCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "/")
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}
	command := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	return command, rest
}

func telegramBotHelpText() string {
	return "OBoard 运维指令\n" +
		"/status 服务器状态\n" +
		"/server <名称或ID> 服务器详情\n" +
		"/traffic 周期流量\n" +
		"/users 用户流量\n" +
		"/audit 审计概览\n" +
		"/incident <事件ID> isolate <manual|auto> <入口ID列表> 临时剔除预览\n" +
		"/incident <事件ID> remove <入口ID列表> 永久移除预览\n" +
		"/help 指令说明"
}

func telegramServerStatusLabel(status model.ServerStatus) string {
	switch status {
	case model.ServerOnline:
		return "在线"
	case model.ServerOffline:
		return "离线"
	case model.ServerDegraded:
		return "降级"
	default:
		return "未知"
	}
}

func telegramFormatTime(value *time.Time) string {
	if value == nil {
		return "从未连接"
	}
	return value.Local().Format("01-02 15:04")
}

func (s *Server) telegramBotServersStatus(ctx context.Context) string {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "服务器状态查询失败，请稍后重试。"
	}
	if len(servers) == 0 {
		return "当前无服务器。"
	}
	var online, offline, degraded, unknown int
	for _, server := range servers {
		switch server.Status {
		case model.ServerOnline:
			online++
		case model.ServerOffline:
			offline++
		case model.ServerDegraded:
			degraded++
		default:
			unknown++
		}
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "服务器状态\n总数：%d\n", len(servers))
	fmt.Fprintf(&builder, "在线：%d · 离线：%d · 降级：%d · 未知：%d\n", online, offline, degraded, unknown)
	for _, server := range servers {
		fmt.Fprintf(&builder, "#%d %s · %s · 最后在线：%s\n", server.ID, server.Name, telegramServerStatusLabel(server.Status), telegramFormatTime(server.LastSeenAt))
	}
	return builder.String()
}

func (s *Server) telegramBotServerDetail(ctx context.Context, arg string) string {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "服务器状态查询失败，请稍后重试。"
	}
	var server *model.Server
	if id, parseErr := strconv.ParseInt(strings.TrimSpace(arg), 10, 64); parseErr == nil {
		for i := range servers {
			if servers[i].ID == id {
				server = &servers[i]
				break
			}
		}
	} else {
		lower := strings.ToLower(strings.TrimSpace(arg))
		for i := range servers {
			if strings.EqualFold(servers[i].Name, strings.TrimSpace(arg)) || strings.HasPrefix(strings.ToLower(servers[i].Name), lower) {
				server = &servers[i]
				break
			}
		}
	}
	if server == nil {
		return "服务器不存在，请检查名称或 ID。"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "服务器详情\n名称：%s\n状态：%s\n", server.Name, telegramServerStatusLabel(server.Status))
	fmt.Fprintf(&builder, "ID：%d\n", server.ID)
	if server.PublicIPv4 != "" || server.PublicIPv6 != "" {
		fmt.Fprintf(&builder, "公网地址：%s %s\n", server.PublicIPv4, server.PublicIPv6)
	}
	fmt.Fprintf(&builder, "最后在线：%s\n", telegramFormatTime(server.LastSeenAt))
	if server.RegionCode != "" {
		fmt.Fprintf(&builder, "地区：%s\n", server.RegionCode)
	}
	if server.MemoryTotalBytes > 0 {
		fmt.Fprintf(&builder, "内存：%s / %s\n", formatNotificationBytesUnsigned(server.MemoryUsedBytes), formatNotificationBytesUnsigned(server.MemoryTotalBytes))
	}
	fmt.Fprintf(&builder, "周期流量：↑ %s / ↓ %s\n", formatNotificationBytesUnsigned(server.TrafficUploadBytes), formatNotificationBytesUnsigned(server.TrafficDownloadBytes))
	fmt.Fprintf(&builder, "离线提醒：%s", map[bool]string{true: "开启", false: "已关闭"}[server.OfflineNotifyEnabled])
	if server.OfflineAfterSeconds > 0 {
		fmt.Fprintf(&builder, " · 判断时间 %d 秒", server.OfflineAfterSeconds)
	}
	return builder.String()
}

func (s *Server) telegramBotTraffic(ctx context.Context) string {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "流量查询失败，请稍后重试。"
	}
	var upload, download uint64
	for _, server := range servers {
		upload += server.TrafficUploadBytes
		download += server.TrafficDownloadBytes
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "周期流量\n总计：↑ %s / ↓ %s\n", formatNotificationBytesUnsigned(upload), formatNotificationBytesUnsigned(download))
	if len(servers) == 0 {
		builder.WriteString("当前无服务器。")
		return builder.String()
	}
	builder.WriteString("服务器明细：\n")
	for _, server := range servers {
		fmt.Fprintf(&builder, "%s：↑ %s / ↓ %s\n", server.Name, formatNotificationBytesUnsigned(server.TrafficUploadBytes), formatNotificationBytesUnsigned(server.TrafficDownloadBytes))
	}
	return builder.String()
}

func (s *Server) telegramBotUsers(ctx context.Context) string {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return "用户流量查询失败，请稍后重试。"
	}
	active := make([]model.User, 0, len(users))
	for _, user := range users {
		if user.Status == "active" {
			active = append(active, user)
		}
	}
	if len(active) == 0 {
		return "当前无活跃用户。"
	}
	const maxShown = 30
	var builder strings.Builder
	fmt.Fprintf(&builder, "用户流量\n活跃用户：%d\n", len(active))
	shown := active
	truncated := false
	if len(shown) > maxShown {
		shown = shown[:maxShown]
		truncated = true
	}
	for index, user := range shown {
		name := strings.TrimSpace(user.Nickname)
		if name == "" {
			name = user.Username
		}
		used := user.TrafficUsedBytes
		if user.TrafficLimitBytes > 0 {
			percent := int(float64(used) * 100 / float64(user.TrafficLimitBytes))
			fmt.Fprintf(&builder, "%d. %s：%s / %s（%d%%）\n", index+1, name, formatNotificationBytes(used), formatNotificationBytes(user.TrafficLimitBytes), percent)
		} else {
			fmt.Fprintf(&builder, "%d. %s：已用 %s（不限）\n", index+1, name, formatNotificationBytes(used))
		}
	}
	if truncated {
		fmt.Fprintf(&builder, "…共 %d 位用户，仅显示前 %d 位\n", len(active), maxShown)
	}
	return builder.String()
}

func (s *Server) telegramBotAudit(ctx context.Context) string {
	connection, subscription, combined, err := s.auditOverviewData(ctx, 24)
	if err != nil {
		return "审计概览查询失败，请稍后重试。"
	}
	var builder strings.Builder
	builder.WriteString("审计概览（24 小时）\n")
	fmt.Fprintf(&builder, "连接审计：启用服务器 %d 台 · 上报用户 %d 人 · 连接 %d 次 · 来源 IP %d 个\n",
		connection.EnabledServerCount, connection.ReportingUserCount, connection.TotalConnections, connection.UniqueSourceIPs)
	fmt.Fprintf(&builder, "订阅审计：上报用户 %d 人 · 拉取 %d 次 · 来源 IP %d 个 · 已暂停 %d 人\n",
		subscription.ReportingUsers, subscription.TotalPulls, subscription.UniqueSourceIPs, subscription.SuspendedCount)
	fmt.Fprintf(&builder, "风险用户：连接 %d 人 · 订阅 %d 人\n", connection.ElevatedRiskCount, subscription.ElevatedRiskCount)
	risky := make([]model.CombinedAuditUserSummary, 0, len(combined.Users))
	for _, user := range combined.Users {
		if user.ConnectionRiskLevel != "low" || user.SubscriptionRiskLevel != "low" || user.SubscriptionSuspended {
			risky = append(risky, user)
		}
	}
	if len(risky) > 0 {
		builder.WriteString("风险明细：\n")
		for _, user := range risky {
			name := strings.TrimSpace(user.Nickname)
			if name == "" {
				name = user.Username
			}
			level := auditBotRiskLabel(user.ConnectionRiskLevel)
			if user.SubscriptionRiskLevel != "low" {
				level = auditBotRiskLabel(user.SubscriptionRiskLevel)
			}
			status := ""
			if user.SubscriptionSuspended {
				status = " · 订阅已暂停"
			}
			fmt.Fprintf(&builder, "· %s（%s%s）\n", name, level, status)
		}
	} else {
		builder.WriteString("无风险用户。")
	}
	return builder.String()
}

func auditBotRiskLabel(level string) string {
	switch level {
	case "critical":
		return "严重风险"
	case "high":
		return "高风险"
	case "medium":
		return "中风险"
	case "low":
		return "低风险"
	default:
		if strings.TrimSpace(level) == "" {
			return "低风险"
		}
		return level
	}
}

func (s *Server) telegramBotSendMessage(ctx context.Context, token string, chatID int64, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	runes := []rune(text)
	if len(runes) > 4000 {
		text = string(runes[:4000]) + "\n…（内容过长已截断）"
	}
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	if _, err := s.telegramAPI(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", form); err != nil {
		log.Printf("telegram bot sendMessage failed: %v", err)
	}
}

func telegramBotHTTP(ctx context.Context, method, target string, form url.Values) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if err := validatePublicHTTPSURLWithResolver(ctx, parsed, net.DefaultResolver); err != nil {
		return nil, err
	}
	var reader io.Reader
	if form != nil {
		reader = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := &http.Client{
		Transport:     newNotificationTransport(net.DefaultResolver, &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}),
		Timeout:       30 * time.Second,
		CheckRedirect: notificationRedirectPolicy(net.DefaultResolver),
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, telegramBotMaxBodyBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram API returned %s", resp.Status)
	}
	return data, nil
}

type telegramBotRateLimiter struct {
	mu     sync.Mutex
	counts map[string][]time.Time
}

func (l *telegramBotRateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.counts[key][:0]
	for _, stamp := range l.counts[key] {
		if now.Sub(stamp) < time.Minute {
			recent = append(recent, stamp)
		}
	}
	l.counts[key] = recent
	if len(recent) >= telegramBotRateLimit {
		return false
	}
	l.counts[key] = append(l.counts[key], now)
	return true
}
