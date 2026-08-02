package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	telegramBotMaxTokens    = 4
	telegramBotRateLimit    = 20
	telegramBotMaxBodyBytes = 1 << 20
)

type telegramBotChannel struct {
	channelID int64
	botToken  string
	chatIDs   map[string]bool
}

func (s *Server) telegramBotChannels(ctx context.Context) map[string][]telegramBotChannel {
	channels, err := s.store.ListEnabledNotificationChannelsUnfiltered(ctx)
	if err != nil {
		log.Printf("list telegram bot channels: %v", err)
		return nil
	}
	out := map[string][]telegramBotChannel{}
	for _, channel := range channels {
		if channel.Type != "telegram" || !channel.Enabled {
			continue
		}
		var cfg struct {
			BotToken       string `json:"bot_token"`
			Interactive    bool   `json:"interactive"`
			AllowedChatIDs string `json:"allowed_chat_ids"`
		}
		if err := json.Unmarshal([]byte(channel.ConfigJSON), &cfg); err != nil {
			continue
		}
		if strings.TrimSpace(cfg.BotToken) == "" || !cfg.Interactive {
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
		chatIDs := map[string]bool{}
		for _, part := range strings.Split(cfg.AllowedChatIDs, ",") {
			if value := strings.TrimSpace(part); value != "" {
				chatIDs[value] = true
			}
		}
		if len(chatIDs) == 0 {
			continue
		}
		out[cfg.BotToken] = append(out[cfg.BotToken], telegramBotChannel{channelID: channel.ID, botToken: cfg.BotToken, chatIDs: chatIDs})
	}
	return out
}

func (s *Server) StartTelegramBots(ctx context.Context) {
	go s.telegramBotPollLoop(ctx)
}

func (s *Server) telegramBotPollLoop(ctx context.Context) {
	offsets := map[string]int64{}
	rate := &telegramBotRateLimiter{counts: map[string][]time.Time{}}
	for {
		if ctx.Err() != nil {
			return
		}
		channelsByToken := s.telegramBotChannels(ctx)
		tokens := make([]string, 0, len(channelsByToken))
		for token := range channelsByToken {
			tokens = append(tokens, token)
		}
		sort.Strings(tokens)
		if len(tokens) > telegramBotMaxTokens {
			tokens = tokens[:telegramBotMaxTokens]
		}
		if len(tokens) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}
		for _, token := range tokens {
			if ctx.Err() != nil {
				return
			}
			updates, err := s.telegramBotGetUpdates(ctx, token, offsets[token])
			if err != nil {
				log.Printf("telegram bot getUpdates failed: %v", err)
				continue
			}
			for _, update := range updates {
				if update.UpdateID+1 > offsets[token] {
					offsets[token] = update.UpdateID + 1
				}
				s.handleTelegramUpdate(ctx, channelsByToken[token], token, update, rate)
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
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	MessageID int64 `json:"message_id"`
	Chat      *struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

func (s *Server) telegramBotGetUpdates(ctx context.Context, token string, offset int64) ([]telegramUpdate, error) {
	query := url.Values{}
	query.Set("timeout", "25")
	query.Set("allowed_updates", `["message"]`)
	if offset > 0 {
		query.Set("offset", strconv.FormatInt(offset, 10))
	}
	target := "https://api.telegram.org/bot" + token + "/getUpdates?" + query.Encode()
	data, err := telegramBotHTTP(ctx, http.MethodGet, target, nil)
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

func (s *Server) handleTelegramUpdate(ctx context.Context, channels []telegramBotChannel, token string, update telegramUpdate, rate *telegramBotRateLimiter) {
	message := update.Message
	if message == nil || message.Chat == nil || strings.TrimSpace(message.Text) == "" {
		return
	}
	chatKey := strconv.FormatInt(message.Chat.ID, 10)
	var channel *telegramBotChannel
	for i := range channels {
		if channels[i].chatIDs[chatKey] {
			channel = &channels[i]
			break
		}
	}
	if channel == nil {
		return
	}
	if !rate.allow(chatKey) {
		s.telegramBotSendMessage(ctx, token, message.Chat.ID, "操作过于频繁，请一分钟后再试。")
		return
	}
	reply := s.telegramBotReply(ctx, strings.TrimSpace(message.Text))
	s.telegramBotSendMessage(ctx, token, message.Chat.ID, reply)
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
		fmt.Fprintf(&builder, "内存：%s / %s\n", formatNotificationBytes(int64(server.MemoryUsedBytes)), formatNotificationBytes(int64(server.MemoryTotalBytes)))
	}
	fmt.Fprintf(&builder, "周期流量：↑ %s / ↓ %s\n", formatNotificationBytes(int64(server.TrafficUploadBytes)), formatNotificationBytes(int64(server.TrafficDownloadBytes)))
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
	var upload, download int64
	for _, server := range servers {
		upload += int64(server.TrafficUploadBytes)
		download += int64(server.TrafficDownloadBytes)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "📊 当前周期流量\n总计：↑ %s / ↓ %s\n", formatNotificationBytes(upload), formatNotificationBytes(download))
	if len(servers) == 0 {
		builder.WriteString("当前没有服务器。")
		return builder.String()
	}
	builder.WriteString("服务器明细：\n")
	for _, server := range servers {
		fmt.Fprintf(&builder, "%s：↑ %s / ↓ %s\n", server.Name, formatNotificationBytes(int64(server.TrafficUploadBytes)), formatNotificationBytes(int64(server.TrafficDownloadBytes)))
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
	if _, err := telegramBotHTTP(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", form); err != nil {
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
