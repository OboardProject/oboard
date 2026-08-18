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
	telegramBotRateLimit    = 20
	telegramBotMaxBodyBytes = 1 << 20
)

var (
	errTelegramBotNotConfigured = errors.New("管理员尚未配置并启用 Telegram Bot")
	errTelegramBotAmbiguous     = errors.New("存在多个已启用的管理员 Telegram Bot，请只保留一个")
)

type telegramBotChannel struct {
	channelID int64
	botToken  string
}

func (s *Server) globalTelegramBot(ctx context.Context) (*telegramBotChannel, error) {
	channels, err := s.store.ListEnabledNotificationChannelsUnfiltered(ctx)
	if err != nil {
		return nil, err
	}
	var selected *telegramBotChannel
	for _, channel := range channels {
		if channel.Type != "telegram" || !channel.Enabled {
			continue
		}
		owner, err := s.store.GetUser(ctx, channel.OwnerUserID)
		if err != nil || owner.Status != "active" {
			continue
		}
		role, err := s.store.EffectiveUserRole(ctx, *owner)
		if err != nil || !roleAllows(role, model.RoleAdmin) {
			continue
		}
		var cfg struct {
			BotToken string `json:"bot_token"`
		}
		if json.Unmarshal([]byte(channel.ConfigJSON), &cfg) != nil || strings.TrimSpace(cfg.BotToken) == "" {
			continue
		}
		if selected != nil {
			return nil, errTelegramBotAmbiguous
		}
		selected = &telegramBotChannel{channelID: channel.ID, botToken: strings.TrimSpace(cfg.BotToken)}
	}
	if selected == nil {
		return nil, errTelegramBotNotConfigured
	}
	return selected, nil
}

func (s *Server) telegramBotPublicStatus(ctx context.Context) map[string]any {
	bot, err := s.globalTelegramBot(ctx)
	if err != nil {
		return map[string]any{"configured": false, "error": err.Error()}
	}
	return map[string]any{"configured": true, "channel_id": bot.channelID}
}

func (s *Server) validateGlobalTelegramBotCandidate(ctx context.Context, candidate model.NotificationChannel) error {
	if candidate.Type != "telegram" || !candidate.Enabled {
		return nil
	}
	current, err := s.globalTelegramBot(ctx)
	if err == nil && current.channelID != candidate.ID {
		return errTelegramBotAmbiguous
	}
	if err != nil && !errors.Is(err, errTelegramBotNotConfigured) {
		return err
	}
	return nil
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
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "操作过于频繁，请一分钟后再试。")
		return
	}
	command, arg := parseTelegramCommand(message.Text)
	if command == "bind" || command == "绑定" {
		if strings.TrimSpace(arg) == "" {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "请发送 /bind <绑定码>。绑定码可在 OBoard 面板通知页生成。")
			return
		}
		binding, err := s.store.ConsumeTelegramBindingCode(ctx, security.HashSecret(strings.TrimSpace(arg)), channel.channelID, message.Chat.ID, message.From.ID, message.Chat.Type, time.Now().UTC())
		if err != nil {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "绑定码无效、已使用或已过期。")
			return
		}
		user, _ := s.store.GetUser(ctx, binding.UserID)
		name := user.Username
		if strings.TrimSpace(user.Nickname) != "" {
			name = user.Nickname
		}
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "已绑定 OBoard 账户："+name+"。发送 /help 查看可用指令。")
		return
	}
	binding, err := s.store.GetTelegramBinding(ctx, channel.channelID, message.Chat.ID, message.From.ID)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "此会话尚未绑定 OBoard 账户，或绑定已被撤销。请在面板生成新绑定码后发送 /bind <绑定码>。")
		return
	}
	if command == "unbind" || command == "解绑" {
		if err := s.store.DeleteTelegramBinding(ctx, channel.channelID, message.Chat.ID, message.From.ID); err != nil {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "解绑失败，请稍后重试。")
			return
		}
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "当前 Telegram 会话已解绑。")
		return
	}
	user, err := s.store.GetUser(ctx, binding.UserID)
	if err != nil || user.Status != "active" {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "绑定账户已停用或不存在，当前操作被拒绝。")
		return
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "无法读取当前账户权限，请稍后重试。")
		return
	}
	if command == "incident" || command == "事件" {
		if !roleAllows(role, model.RoleOperator) {
			s.telegramBotSendMessage(ctx, token, message.Chat.ID, "权限不足：当前角色不能处置节点事件。")
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
	usage := "用法：\n/incident <事件ID> isolate <manual|auto> <入口ID,入口ID>\n/incident <事件ID> remove <入口ID,入口ID>"
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
		s.telegramBotSendMessage(ctx, token, chatID, "当前角色没有此节点处置能力。")
		return
	}
	event, err := s.store.GetNodeIncident(ctx, eventID)
	if err != nil || event.Status == model.NodeIncidentResolved || !principal.AllowsInt64("server_ids", event.ServerID) {
		s.telegramBotSendMessage(ctx, token, chatID, "事件不存在、已关闭或不在当前授权范围。")
		return
	}
	preview, err := s.nodeIncidentImpactPreview(ctx, *event, inboundIDs, action, recoveryPolicy)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, chatID, "影响预览失败："+err.Error())
		return
	}
	payload := nodeIncidentConfirmationPayload{EventID: event.ID, EventVersion: event.Version, Action: action, InboundIDs: preview["inbound_ids"].([]int64), RecoveryPolicy: recoveryPolicy, ChatID: chatID, TelegramUserID: telegramUserID}
	payloadJSON, _ := json.Marshal(payload)
	confirmation, err := security.RandomToken(18)
	if err != nil {
		s.telegramBotSendMessage(ctx, token, chatID, "无法生成确认按钮，请稍后重试。")
		return
	}
	if err := s.store.CreateOperationConfirmation(ctx, security.HashSecret(confirmation), capabilityName, event.ID, event.Version, user.ID, string(payloadJSON), time.Now().UTC().Add(5*time.Minute)); err != nil {
		s.telegramBotSendMessage(ctx, token, chatID, "无法保存确认按钮，请稍后重试。")
		return
	}
	nodes, _ := preview["nodes"].([]nodeIncidentSnapshotInbound)
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, fmt.Sprintf("%s (#%d)", node.Name, node.ID))
	}
	text := fmt.Sprintf("影响预览\n事件：#%d %s\n入口：%s\n影响套餐：%d\n预计用户：%d\n现有连接：%s\n确认按钮 5 分钟内有效。", event.ID, event.ServerName, strings.Join(names, "、"), preview["affected_plan_count"], preview["affected_user_count"], map[bool]string{true: "会受影响", false: "不受影响"}[action == "permanent_remove"])
	markup := fmt.Sprintf(`{"inline_keyboard":[[{"text":"确认执行","callback_data":"confirm:%s"}]]}`, confirmation)
	s.telegramBotSendMessageMarkup(ctx, token, chatID, text, markup)
}

func (s *Server) handleTelegramCallback(ctx context.Context, channel telegramBotChannel, callback telegramCallbackQuery, rate *telegramBotRateLimiter) {
	token := channel.botToken
	if callback.From == nil || callback.Message == nil || callback.Message.Chat == nil || !strings.HasPrefix(callback.Data, "confirm:") {
		return
	}
	chatID := callback.Message.Chat.ID
	if !rate.allow(strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(callback.From.ID, 10)) {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "操作过于频繁")
		return
	}
	binding, err := s.store.GetTelegramBinding(ctx, channel.channelID, chatID, callback.From.ID)
	if err != nil {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "绑定已失效")
		return
	}
	user, err := s.store.GetUser(ctx, binding.UserID)
	if err != nil || user.Status != "active" {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "账户已停用")
		return
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "无法读取权限")
		return
	}
	confirmation, err := s.store.ConsumeOperationConfirmationToken(ctx, security.HashSecret(strings.TrimPrefix(callback.Data, "confirm:")), user.ID, time.Now().UTC())
	if err != nil {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "按钮已使用、已过期或无权执行")
		return
	}
	var payload nodeIncidentConfirmationPayload
	if json.Unmarshal([]byte(confirmation.PayloadJSON), &payload) != nil || payload.EventID != confirmation.EventID || payload.EventVersion != confirmation.EventVersion || payload.ChatID != chatID || payload.TelegramUserID != callback.From.ID {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "确认内容无效")
		return
	}
	event, err := s.store.GetNodeIncident(ctx, payload.EventID)
	if err != nil || event.Status == model.NodeIncidentResolved || event.Version != payload.EventVersion {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "事件已关闭或版本已变化")
		return
	}
	principal := application.HumanPrincipal(*user, role, netip.Addr{})
	if _, allowed := s.capabilities.Authorize(principal, confirmation.Capability); !allowed || !principal.AllowsInt64("server_ids", event.ServerID) {
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "当前权限不足")
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
		s.telegramBotAnswerCallback(ctx, token, callback.ID, "执行失败")
		s.telegramBotSendMessage(ctx, token, chatID, "节点处置失败："+err.Error())
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
			s.telegramBotAnswerCallback(ctx, token, callback.ID, "部署创建失败")
			s.telegramBotSendMessage(ctx, token, chatID, "入口已移除，但完整部署创建失败："+deployErr.Error())
			return
		}
		if err := s.store.CreateNodeIncidentAction(ctx, &action); err != nil {
			s.telegramBotAnswerCallback(ctx, token, callback.ID, "处置状态保存失败")
			return
		}
		s.telegramBotSendMessage(ctx, token, chatID, fmt.Sprintf("永久移除已确认，Changeset %s 已执行；处置记录 #%d 正在等待配置版本 %d 的 %d 个部署任务完成。", changeset.ID, action.ID, version, len(tasks)))
	} else {
		s.telegramBotSendMessage(ctx, token, chatID, fmt.Sprintf("临时剔除已生效，Changeset %s 已完成；未触发 Agent 部署。", changeset.ID))
	}
	s.telegramBotAnswerCallback(ctx, token, callback.ID, "操作已确认")
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
		return "OBoard 账户服务\n/account 查看账户、套餐、有效期、流量和设备摘要\n/status 查看自己可用节点的当前状态\n/announcements 查看管理员公告\n/unbind 解绑当前会话"
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
			return "权限不足：普通用户不能查看全局服务器状态。"
		}
		return s.telegramBotServersStatus(ctx)
	case "server", "服务器详情":
		if !adminAccess {
			return "权限不足：普通用户不能查看服务器详情。"
		}
		return s.telegramBotServerDetail(ctx, arg)
	case "traffic", "流量":
		if !adminAccess {
			return s.telegramBotAccount(ctx, user)
		}
		return s.telegramBotTraffic(ctx)
	case "users", "用户", "使用情况":
		if !adminAccess {
			return "权限不足：普通用户不能查看其他用户。"
		}
		return s.telegramBotUsers(ctx)
	case "audit", "审计":
		if !adminAccess {
			return "权限不足：普通用户不能查看审计信息。"
		}
		if _, allowed := s.capabilities.Authorize(principal, "audit.risk_overview"); !allowed {
			return "权限不足：当前角色没有审计查看能力。"
		}
		return s.telegramBotAudit(ctx)
	default:
		return "未识别的指令，发送 /help 查看当前账户可用指令。"
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
		fmt.Fprintf(&builder, "设备：%d 个有效", active)
		if user.DeviceLimit > 0 {
			fmt.Fprintf(&builder, " / 上限 %d", user.DeviceLimit)
		}
	}
	return builder.String()
}

func (s *Server) telegramBotOwnNodes(ctx context.Context, user model.User) string {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return "查询节点状态失败，请稍后再试。"
	}
	snapshot, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return "查询节点状态失败，请稍后再试。"
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
		return "当前没有可用节点。"
	}
	sort.Strings(lines)
	if len(lines) > 50 {
		lines = append(lines[:50], "…仅显示前 50 个节点")
	}
	return fmt.Sprintf("我的节点（%d）\n%s", len(lines), strings.Join(lines, "\n"))
}

func (s *Server) telegramBotAnnouncements(ctx context.Context, userID int64) string {
	items, err := s.store.ListNotificationAnnouncementsForUser(ctx, userID, 10)
	if err != nil {
		return "查询管理员公告失败，请稍后再试。"
	}
	if len(items) == 0 {
		return "当前没有管理员公告。"
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
		return "未识别的指令，发送 /help 查看可用指令。"
	}
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
	return "🤖 OBoard 机器人指令\n" +
		"/status 查看所有服务器状态\n" +
		"/server <名称或ID> 查看某台服务器详情\n" +
		"/traffic 查看当前周期流量\n" +
		"/users 查看用户使用情况\n" +
		"/audit 查看审计台概览\n" +
		"/incident <事件ID> isolate <manual|auto> <入口ID列表> 预览临时剔除\n" +
		"/incident <事件ID> remove <入口ID列表> 预览永久移除\n" +
		"/help 显示本帮助"
}

func telegramServerStatusLabel(status model.ServerStatus) string {
	switch status {
	case model.ServerOnline:
		return "🟢 在线"
	case model.ServerOffline:
		return "🔴 离线"
	case model.ServerDegraded:
		return "🟡 降级"
	default:
		return "⚪ 未知"
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
		return "查询服务器状态失败，请稍后再试。"
	}
	if len(servers) == 0 {
		return "当前没有服务器。"
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
	fmt.Fprintf(&builder, "📡 服务器状态（共 %d 台）\n", len(servers))
	fmt.Fprintf(&builder, "🟢 在线 %d · 🔴 离线 %d · 🟡 降级 %d · ⚪ 未知 %d\n", online, offline, degraded, unknown)
	for _, server := range servers {
		fmt.Fprintf(&builder, "%d. %s %s · 最后在线 %s\n", server.ID, server.Name, telegramServerStatusLabel(server.Status), telegramFormatTime(server.LastSeenAt))
	}
	return builder.String()
}

func (s *Server) telegramBotServerDetail(ctx context.Context, arg string) string {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "查询服务器状态失败，请稍后再试。"
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
		return "没有找到这台服务器，请检查名称或 ID。"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "🖥 %s %s\n", server.Name, telegramServerStatusLabel(server.Status))
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
		return "查询流量失败，请稍后再试。"
	}
	var upload, download uint64
	for _, server := range servers {
		upload += server.TrafficUploadBytes
		download += server.TrafficDownloadBytes
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "📊 当前周期流量\n总计：↑ %s / ↓ %s\n", formatNotificationBytesUnsigned(upload), formatNotificationBytesUnsigned(download))
	if len(servers) == 0 {
		builder.WriteString("当前没有服务器。")
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
		return "查询用户使用情况失败，请稍后再试。"
	}
	active := make([]model.User, 0, len(users))
	for _, user := range users {
		if user.Status == "active" {
			active = append(active, user)
		}
	}
	if len(active) == 0 {
		return "当前没有活跃用户。"
	}
	const maxShown = 30
	var builder strings.Builder
	fmt.Fprintf(&builder, "👥 用户使用情况（活跃 %d 位）\n", len(active))
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
		return "查询审计概览失败，请稍后再试。"
	}
	var builder strings.Builder
	builder.WriteString("🛡 审计台概览（最近 24 小时）\n")
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
		builder.WriteString("重点关注：\n")
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
		builder.WriteString("暂无风险用户。")
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
