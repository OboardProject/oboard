package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) deliverAccountAuditNotifications(ctx context.Context, now time.Time) error {
	if !s.auditSettingsState(ctx).Enabled {
		return nil
	}
	items, err := s.store.ClaimAccountAuditNotifications(ctx, now, 32)
	if err != nil {
		return err
	}
	var failures error
	for _, item := range items {
		if !s.auditSettingsState(ctx).Enabled {
			return failures
		}
		err = s.queueAccountAuditNotification(ctx, item, now)
		completeErr := s.store.CompleteAccountAuditNotification(ctx, item.ID, item.LeaseToken, now, err == nil)
		failures = errors.Join(failures, err, completeErr)
	}
	return failures
}

// The event outbox is acknowledged only after all eligible channel deliveries
// are durable. Channel+outbox-ID keys make a crash between these commits safe.
func (s *Server) queueAccountAuditNotification(ctx context.Context, item store.AccountAuditNotification, now time.Time) error {
	var snapshot auditrisk.Snapshot
	if err := json.Unmarshal(item.Snapshot, &snapshot); err != nil {
		return err
	}
	if snapshot.AccountID != item.UserID {
		return errors.New("audit notification account mismatch")
	}
	gates := s.auditSettingsState(ctx)
	if !gates.Enabled || (item.RiskType == "activity" && !gates.Connection) || (item.RiskType == "exposure" && !gates.Subscription) {
		return nil
	}
	if snapshot.Status == "stale" || snapshot.Features.EvidenceCutoff.Before(now.Add(-5*time.Minute)) {
		return nil
	}
	score := snapshot.Activity
	problem := "异常并发活动"
	if item.RiskType == "exposure" {
		score = snapshot.Exposure
		problem = "订阅扩散线索"
	}
	if score == nil {
		return errors.New("audit notification score missing")
	}
	user, err := s.store.GetUser(ctx, item.UserID)
	if err != nil {
		return err
	}
	if user.Status != "active" {
		return nil
	}
	signals := problem + "；请核实，不代表已确认共享或泄露。"
	if score.LowerContribution != nil {
		c := score.LowerContribution
		signals += fmt.Sprintf("至少 %d 个来源组的共同活跃时间片累计出现在 %d 分钟。", c.Sources, c.Minutes)
	}
	level := map[string]string{"low": "低风险", "medium": "中风险", "high": "高风险", "very_high": "极高风险"}[score.Level]
	event := notificationEvent{Name: notificationUserRisk, Key: fmt.Sprintf("account-audit-outbox:%d", item.ID), TargetUserID: item.UserID, Data: map[string]string{"UserName": user.Username, "UserID": fmt.Sprint(item.UserID), "RiskLevel": level, "RiskScore": fmt.Sprint(score.Lower), "Signals": signals, "SourceIPCount": "不代表设备数", "ActivePeak": "不适用", "Time": snapshot.AsOf.UTC().Format(time.RFC3339)}}
	channels, err := s.store.ListEnabledNotificationChannels(ctx, event.Name)
	if err != nil {
		return err
	}
	queued := false
	for _, channel := range channels {
		owner, err := s.store.GetUser(ctx, channel.OwnerUserID)
		if err != nil {
			return err
		}
		if owner.Status != "active" {
			continue
		}
		role, err := s.store.EffectiveUserRole(ctx, *owner)
		if err != nil {
			return err
		}
		if !notificationChannelEligible(channel, role, event) {
			continue
		}
		title, body, err := renderNotificationEvent(channel, event)
		if err != nil {
			return err
		}
		delivery := model.NotificationDelivery{ChannelID: channel.ID, Event: event.Name, EventKey: event.Key, Title: title, Body: body, NextAttemptAt: now}
		inserted, err := s.store.QueueNotificationDelivery(ctx, &delivery)
		if err != nil {
			return err
		}
		queued = queued || inserted
	}
	if queued {
		s.wakeNotificationDelivery(ctx)
	}
	return nil
}
