package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type nodeIncidentSnapshotInbound struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Protocol       string   `json:"protocol"`
	Published      bool     `json:"published"`
	PlanIDs        []int64  `json:"plan_ids"`
	PlanNames      []string `json:"plan_names"`
	ExceptionCount int      `json:"exception_count"`
}

type nodeIncidentSnapshot struct {
	ServerID   int64                         `json:"server_id"`
	ServerName string                        `json:"server_name"`
	Status     model.ServerStatus            `json:"status"`
	LastSeenAt *time.Time                    `json:"last_seen_at,omitempty"`
	Inbounds   []nodeIncidentSnapshotInbound `json:"inbounds"`
}

func (s *Server) nodeIncidentSnapshot(ctx context.Context, server model.Server) string {
	snapshot := nodeIncidentSnapshot{ServerID: server.ID, ServerName: server.Name, Status: server.Status, LastSeenAt: server.LastSeenAt, Inbounds: []nodeIncidentSnapshotInbound{}}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		encoded, _ := json.Marshal(snapshot)
		return string(encoded)
	}
	paths, _ := s.store.ListProxyPaths(ctx)
	for _, inbound := range inbounds {
		if inbound.ServerID != server.ID || !inbound.Enabled {
			continue
		}
		item := nodeIncidentSnapshotInbound{ID: inbound.ID, Name: inbound.Name, Protocol: string(inbound.Protocol), Published: false}
		refs, refErr := s.store.PlanNodeReferences(ctx, model.AssignableNodeInbound, inbound.ID)
		if refErr == nil {
			item.ExceptionCount = refs.Exceptions
			for _, ref := range refs.Active {
				item.PlanIDs = append(item.PlanIDs, ref.PlanID)
				item.PlanNames = append(item.PlanNames, ref.Name)
			}
		}
		planSeen := map[int64]bool{}
		for _, planID := range item.PlanIDs {
			planSeen[planID] = true
		}
		for _, path := range paths {
			if path.InboundID != inbound.ID || !path.Enabled {
				continue
			}
			pathRefs, pathErr := s.store.PlanNodeReferences(ctx, model.AssignableNodeProxyPath, path.ID)
			if pathErr != nil {
				continue
			}
			item.ExceptionCount += pathRefs.Exceptions
			for _, ref := range pathRefs.Active {
				if !planSeen[ref.PlanID] {
					item.PlanIDs = append(item.PlanIDs, ref.PlanID)
					item.PlanNames = append(item.PlanNames, ref.Name)
					planSeen[ref.PlanID] = true
				}
			}
		}
		item.Published = len(item.PlanIDs) > 0
		snapshot.Inbounds = append(snapshot.Inbounds, item)
	}
	sort.Slice(snapshot.Inbounds, func(i, j int) bool { return snapshot.Inbounds[i].ID < snapshot.Inbounds[j].ID })
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func (s *Server) reconcileNodeIncidentActions(ctx context.Context) {
	items, err := s.store.ReconcileNodeIncidentActions(ctx)
	if err != nil {
		s.logPeriodicError("node-incident-actions", "reconcile node incident actions: %v", err)
		return
	}
	for _, item := range items {
		action := "node_incident_action_succeeded"
		if item.Status == "failed" {
			action = "node_incident_action_failed"
		}
		actorID := item.ActorUserID
		_ = s.store.AddAudit(ctx, model.AuditLog{ActorID: &actorID, Action: action, Target: "node_incident_action", Detail: fmt.Sprintf("%d:%s", item.ID, item.ChangesetID), IP: "controller"})
	}
	if len(items) > 0 {
		s.publishRealtime("node_incidents", "tasks", "subscriptions")
	}
}

func (s *Server) finalizeRecoveredNodeIncidents(ctx context.Context, nowTime time.Time) {
	items, err := s.store.ListDueRecoveringNodeIncidents(ctx, nowTime)
	if err != nil {
		log.Printf("list recovering node incidents: %v", err)
		return
	}
	for _, item := range items {
		server, err := s.store.GetServer(ctx, item.ServerID)
		if err != nil || server.Status != model.ServerOnline || item.RecoveryCandidateAt == nil || server.LastSeenAt == nil || server.LastSeenAt.Before(*item.RecoveryCandidateAt) {
			continue
		}
		resolved, err := s.store.ResolveNodeIncident(ctx, item.ID, item.Version, nowTime)
		if err != nil {
			log.Printf("resolve node incident %d: %v", item.ID, err)
			continue
		}
		s.syncNodeIncidentTelegram(ctx, *resolved)
		s.publishRealtime("node_incidents", "subscriptions", "servers")
		_ = s.store.AddAudit(ctx, model.AuditLog{Action: "node_incident_resolved", Target: "node_incident", Detail: fmt.Sprintf("%d:%d", resolved.ID, resolved.OutageDurationSeconds), IP: "controller"})
	}
}

type telegramIncidentTarget struct {
	Channel model.NotificationChannel
	Token   string
	ChatID  int64
}

func (s *Server) telegramIncidentTargets(ctx context.Context) []telegramIncidentTarget {
	bot, err := s.globalTelegramBot(ctx)
	if err != nil {
		return nil
	}
	channels, err := s.store.ListEnabledNotificationChannels(ctx, notificationServerOffline)
	if err != nil {
		return nil
	}
	targets := []telegramIncidentTarget{}
	seen := map[int64]bool{}
	for _, channel := range channels {
		if channel.Type != "telegram" {
			continue
		}
		owner, ownerErr := s.store.GetUser(ctx, channel.OwnerUserID)
		if ownerErr != nil || owner.Status != "active" {
			continue
		}
		role, roleErr := s.store.EffectiveUserRole(ctx, *owner)
		if roleErr != nil || !roleAllows(role, model.RoleAdmin) {
			continue
		}
		bindings, bindingErr := s.store.ListTelegramBindingsByChannel(ctx, channel.ID)
		if bindingErr != nil {
			continue
		}
		for _, binding := range bindings {
			if binding.ChatID == 0 || seen[binding.ChatID] {
				continue
			}
			seen[binding.ChatID] = true
			targets = append(targets, telegramIncidentTarget{Channel: channel, Token: bot.botToken, ChatID: binding.ChatID})
		}
	}
	return targets
}

func nodeIncidentTelegramText(item model.NodeIncident) string {
	var snapshot nodeIncidentSnapshot
	_ = json.Unmarshal([]byte(item.SnapshotJSON), &snapshot)
	var builder strings.Builder
	switch item.Status {
	case model.NodeIncidentResolved:
		fmt.Fprintf(&builder, "服务器恢复\n服务器：%s\n事件：#%d\n", item.ServerName, item.ID)
		fmt.Fprintf(&builder, "中断开始：%s\n恢复连接：%s\n稳定确认：%s\n中断时长：%s\n", item.FirstOfflineAt.Local().Format("2006-01-02 15:04:05"), formatIncidentTime(item.RecoveredAt), formatIncidentTime(item.ResolvedAt), formatIncidentDuration(item.OutageDurationSeconds))
		if item.FlapCount > 0 {
			fmt.Fprintf(&builder, "抖动次数：%d\n", item.FlapCount)
		}
		builder.WriteString("事件已关闭，相关处置不可执行。")
	case model.NodeIncidentRecovering:
		fmt.Fprintf(&builder, "服务器恢复确认中\n服务器：%s\n事件：#%d\n", item.ServerName, item.ID)
		fmt.Fprintf(&builder, "恢复连接：%s\n稳定窗口截止：%s\n", formatIncidentTime(item.RecoveryCandidateAt), formatIncidentTime(item.RecoveryDeadlineAt))
		builder.WriteString("窗口内再次失联将归入当前事件。")
	default:
		fmt.Fprintf(&builder, "服务器失联\n服务器：%s\n事件：#%d\n", item.ServerName, item.ID)
		fmt.Fprintf(&builder, "首次失联：%s\n告警判定：%s\n", item.FirstOfflineAt.Local().Format("2006-01-02 15:04:05"), item.DetectedAt.Local().Format("2006-01-02 15:04:05"))
		published := 0
		for _, inbound := range snapshot.Inbounds {
			if inbound.Published {
				published++
			}
		}
		fmt.Fprintf(&builder, "已发布入口：%d", published)
		if published > 0 {
			builder.WriteString("\n入口：")
			parts := []string{}
			for _, inbound := range snapshot.Inbounds {
				if inbound.Published {
					parts = append(parts, fmt.Sprintf("%s (#%d)", inbound.Name, inbound.ID))
				}
			}
			builder.WriteString(strings.Join(parts, "、"))
			builder.WriteString("\n使用下方按钮可查看状态、临时剔除或永久移除。")
		}
		if item.FlapCount > 0 {
			fmt.Fprintf(&builder, "\n抖动次数：%d", item.FlapCount)
		}
	}
	return builder.String()
}

func formatIncidentTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

func formatIncidentDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	duration := time.Duration(seconds) * time.Second
	if duration < time.Minute {
		return fmt.Sprintf("%d 秒", seconds)
	}
	if duration < time.Hour {
		return fmt.Sprintf("%d 分 %d 秒", int(duration/time.Minute), int(duration%time.Minute/time.Second))
	}
	return fmt.Sprintf("%d 小时 %d 分", int(duration/time.Hour), int(duration%time.Hour/time.Minute))
}

func (s *Server) syncNodeIncidentTelegram(ctx context.Context, item model.NodeIncident) {
	for _, target := range s.telegramIncidentTargets(ctx) {
		message, err := s.store.GetNodeIncidentTelegramMessage(ctx, item.ID, target.ChatID)
		if err != nil && item.Status == model.NodeIncidentActive {
			message, err = s.store.EnsureNodeIncidentTelegramMessage(ctx, item.ID, target.Channel.ID, target.ChatID)
		}
		if err != nil || message.LastEventVersion >= item.Version {
			continue
		}
		text := nodeIncidentTelegramText(item)
		if message.MessageID == 0 {
			markup, hashes, markupErr := s.incidentTelegramMarkup(ctx, item, target.ChatID, 0, text)
			if markupErr != nil {
				log.Printf("telegram incident keyboard: %v", markupErr)
				markup, hashes = "", nil
			}
			messageID, sendErr := s.telegramIncidentSend(ctx, target.Token, target.ChatID, text, markup)
			if sendErr != nil {
				_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
			} else if err := s.store.SetTelegramActionTokenMessages(ctx, hashes, messageID); err != nil {
				log.Printf("telegram incident attach message: %v", err)
			}
			version := item.Version
			if sendErr != nil {
				version = message.LastEventVersion
			}
			_ = s.store.UpdateNodeIncidentTelegramMessage(ctx, message.ID, messageID, 0, version, sendErr)
			continue
		}
		markup, hashes, markupErr := s.incidentTelegramMarkup(ctx, item, target.ChatID, message.MessageID, text)
		if markupErr != nil {
			log.Printf("telegram incident keyboard: %v", markupErr)
			markup, hashes = "", nil
		}
		editErr := s.telegramIncidentEdit(ctx, target.Token, target.ChatID, message.MessageID, text, markup)
		if editErr != nil {
			_ = s.store.DeleteTelegramActionTokens(ctx, hashes)
		}
		if editErr == nil {
			_ = s.store.UpdateNodeIncidentTelegramMessage(ctx, message.ID, 0, 0, item.Version, nil)
			continue
		}
		if item.Status != model.NodeIncidentResolved {
			_ = s.store.UpdateNodeIncidentTelegramMessage(ctx, message.ID, 0, 0, message.LastEventVersion, editErr)
			continue
		}
		fallback := fmt.Sprintf("原告警消息：#%d\n%s", message.MessageID, text)
		fallbackMarkup, fallbackHashes, _ := s.incidentTelegramMarkup(ctx, item, target.ChatID, 0, text)
		fallbackID, fallbackErr := s.telegramIncidentSend(ctx, target.Token, target.ChatID, fallback, fallbackMarkup)
		if fallbackErr != nil {
			_ = s.store.DeleteTelegramActionTokens(ctx, fallbackHashes)
		} else if err := s.store.SetTelegramActionTokenMessages(ctx, fallbackHashes, fallbackID); err != nil {
			log.Printf("telegram incident attach fallback: %v", err)
		}
		if fallbackErr != nil {
			_ = s.store.UpdateNodeIncidentTelegramMessage(ctx, message.ID, 0, 0, message.LastEventVersion, fallbackErr)
			continue
		}
		_ = s.store.UpdateNodeIncidentTelegramMessage(ctx, message.ID, 0, fallbackID, item.Version, editErr)
	}
}

func (s *Server) telegramIncidentSend(ctx context.Context, token string, chatID int64, text, markup string) (int64, error) {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	form.Set("disable_web_page_preview", "true")
	if strings.TrimSpace(markup) != "" {
		form.Set("reply_markup", markup)
	}
	data, err := s.telegramAPI(ctx, "POST", "https://api.telegram.org/bot"+token+"/sendMessage", form)
	if err != nil {
		return 0, err
	}
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &response) != nil || !response.OK || response.Result.MessageID <= 0 {
		return 0, fmt.Errorf("telegram sendMessage returned an invalid response")
	}
	return response.Result.MessageID, nil
}

func (s *Server) telegramIncidentEdit(ctx context.Context, token string, chatID, messageID int64, text, markup string) error {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("message_id", strconv.FormatInt(messageID, 10))
	form.Set("text", text)
	if strings.TrimSpace(markup) == "" {
		markup = `{"inline_keyboard":[]}`
	}
	form.Set("reply_markup", markup)
	data, err := s.telegramAPI(ctx, "POST", "https://api.telegram.org/bot"+token+"/editMessageText", form)
	if err != nil {
		return err
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(data, &response) != nil || !response.OK {
		return fmt.Errorf("telegram editMessageText returned an invalid response")
	}
	return nil
}

func (s *Server) nodeIncidentPublishedInbounds(item model.NodeIncident) []nodeIncidentSnapshotInbound {
	var snapshot nodeIncidentSnapshot
	_ = json.Unmarshal([]byte(item.SnapshotJSON), &snapshot)
	out := make([]nodeIncidentSnapshotInbound, 0, len(snapshot.Inbounds))
	for _, inbound := range snapshot.Inbounds {
		if inbound.Published {
			out = append(out, inbound)
		}
	}
	return out
}

func nodeIncidentNotFound(err error) bool {
	return err == sql.ErrNoRows
}

func (s *Server) offlineIsolationPanel(ctx context.Context) (map[string]any, error) {
	events := []model.NodeIncident{}
	for _, status := range []model.NodeIncidentStatus{model.NodeIncidentActive, model.NodeIncidentRecovering} {
		items, err := s.store.ListNodeIncidents(ctx, string(status), 50, 0)
		if err != nil {
			return nil, err
		}
		events = append(events, items...)
	}
	prompts := []map[string]any{}
	active := []map[string]any{}
	for _, event := range events {
		server, err := s.store.GetServer(ctx, event.ServerID)
		if nodeIncidentNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if server.CreatedAt.After(event.DetectedAt) {
			continue
		}
		isolations, err := s.store.ListNodePublicationIsolations(ctx, event.ID)
		if err != nil {
			return nil, err
		}
		hidden := map[int64]bool{}
		for _, isolation := range isolations {
			if isolation.Status != "hidden" || isolation.InboundID == nil {
				continue
			}
			hidden[*isolation.InboundID] = true
			row := map[string]any{
				"id": isolation.ID, "incident_id": event.ID, "server_id": event.ServerID, "server_name": server.Name,
				"inbound_name": isolation.InboundName, "recovery_policy": isolation.RecoveryPolicy,
			}
			if isolation.RestoreAt != nil {
				row["restore_at"] = isolation.RestoreAt
			}
			active = append(active, row)
		}
		if event.Status != model.NodeIncidentActive || server.Status != model.ServerOffline {
			continue
		}
		inbounds := []map[string]any{}
		for _, inbound := range s.nodeIncidentPublishedInbounds(event) {
			if hidden[inbound.ID] {
				continue
			}
			current, err := s.store.GetInbound(ctx, inbound.ID)
			if nodeIncidentNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !current.Enabled || current.ServerID != server.ID {
				continue
			}
			inbounds = append(inbounds, map[string]any{"id": current.ID, "name": current.Name})
		}
		if len(inbounds) == 0 {
			continue
		}
		prompts = append(prompts, map[string]any{
			"id": event.ID, "version": event.Version, "server_id": event.ServerID, "server_name": server.Name, "inbounds": inbounds,
		})
	}
	return map[string]any{"incidents": prompts, "active": active}, nil
}
