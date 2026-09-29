package plugin

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// Skip reasons recorded on a schedule when a slot does not produce a run.
const (
	SkipOverlap      = "overlap"
	SkipMissed       = "missed"
	SkipNotRunnable  = "not_runnable"
	SkipAutoPaused   = "auto_paused"
	SkipQueueFull    = "queue_full"
	SkipOutOfScope   = "out_of_scope"
	maxScheduleDelay = 2 * time.Minute
)

// Tick fires due schedules and pending server events. The scheduler never
// stacks runs: a slot that finds the previous run still active is skipped
// (coalesced) and the schedule moves to its next slot.
func (s *Service) Tick(ctx context.Context) {
	settings := s.Settings(ctx)
	if !settings.Enabled || settings.SchedulerPaused {
		// Events that arrive while automation is off are dropped, never
		// replayed in a burst when it is switched back on.
		s.drainEvents(ctx)
		return
	}
	s.tickSchedules(ctx)
	s.tickEvents(ctx)
}

func (s *Service) drainEvents(ctx context.Context) {
	items, err := s.store.ClaimPluginEvents(ctx, "plugin-scheduler", s.now().Add(time.Minute), 64)
	if err != nil {
		return
	}
	for _, item := range items {
		_ = s.store.CompletePluginEvent(ctx, item.ID)
	}
}

func (s *Service) tickSchedules(ctx context.Context) {
	now := s.now()
	due, err := s.store.ListDuePluginSchedules(ctx, now, 64)
	if err != nil {
		return
	}
	for _, schedule := range due {
		slot := *schedule.NextDueAt
		next := nextDue(schedule, now)
		if now.Sub(slot) > maxScheduleDelay {
			// A Controller outage never replays every missed slot.
			_ = s.store.MarkPluginSchedule(ctx, schedule.ID, false, SkipMissed, next)
			continue
		}
		reason := s.fireSchedule(ctx, schedule, slot)
		_ = s.store.MarkPluginSchedule(ctx, schedule.ID, reason == "", reason, next)
	}
}

func (s *Service) fireSchedule(ctx context.Context, schedule model.PluginSchedule, slot time.Time) string {
	loaded, instance, err := s.loadInstance(ctx, schedule.InstanceID)
	if err != nil {
		return SkipNotRunnable
	}
	if instance.AutoPaused {
		return SkipAutoPaused
	}
	if err := s.runnable(ctx, loaded, instance); err != nil {
		return SkipNotRunnable
	}
	detail := TriggerDetail{ScheduleID: schedule.ID, ScheduledAt: slot.UTC().Format(time.RFC3339)}
	key := "schedule:" + strconv.FormatInt(schedule.ID, 10) + ":" + strconv.FormatInt(slot.Unix(), 10)
	_, created, err := s.enqueue(ctx, loaded, instance, schedule.Kind, detail, key, "")
	switch CodeOf(err) {
	case "":
		if created {
			s.Wake()
		}
		return ""
	case CodeRunInProgress:
		return SkipOverlap
	case CodeRateLimited:
		return SkipQueueFull
	default:
		return SkipNotRunnable
	}
}

// Event topics written by the node incident lifecycle.
var eventTopics = map[string]string{
	"plugin.server.offline":   EventServerOffline,
	"plugin.server.recovered": EventServerOnline,
}

func (s *Service) tickEvents(ctx context.Context) {
	items, err := s.store.ClaimPluginEvents(ctx, "plugin-scheduler", s.now().Add(time.Minute), 32)
	if err != nil {
		return
	}
	for _, item := range items {
		s.handleEvent(ctx, item)
		_ = s.store.CompletePluginEvent(ctx, item.ID)
	}
}

func (s *Service) handleEvent(ctx context.Context, item store.EventOutboxItem) {
	event, ok := eventTopics[item.Topic]
	if !ok {
		return
	}
	var payload struct {
		ServerID   int64  `json:"server_id"`
		IncidentID int64  `json:"incident_id"`
		OccurredAt string `json:"occurred_at"`
	}
	if json.Unmarshal(item.Payload, &payload) != nil || payload.ServerID <= 0 {
		return
	}
	occurred := payload.OccurredAt
	if occurred == "" {
		occurred = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	schedules, err := s.store.ListEnabledEventPluginSchedules(ctx, event)
	if err != nil {
		return
	}
	for _, schedule := range schedules {
		loaded, instance, err := s.loadInstance(ctx, schedule.InstanceID)
		if err != nil || instance.AutoPaused {
			continue
		}
		// Only events about servers inside the instance's granted scope are
		// delivered; configuration never widens it.
		record, err := s.store.GetPluginGrant(ctx, instance.ID)
		if err != nil || loaded.pkg == nil || record.PackageID != loaded.pkg.ID {
			_ = s.store.MarkPluginSchedule(ctx, schedule.ID, false, SkipNotRunnable, nil)
			continue
		}
		grant, _ := ParseGrant(record.GrantJSON)
		if !grant.AllowsServer(CapEventsServerStatus, payload.ServerID) {
			continue
		}
		if err := s.runnable(ctx, loaded, instance); err != nil {
			_ = s.store.MarkPluginSchedule(ctx, schedule.ID, false, SkipNotRunnable, nil)
			continue
		}
		detail := TriggerDetail{ScheduleID: schedule.ID, Event: &EventDetail{Type: event, ServerID: strconv.FormatInt(payload.ServerID, 10), OccurredAt: occurred}}
		key := "event:" + strconv.FormatInt(schedule.ID, 10) + ":" + item.ID
		_, created, err := s.enqueue(ctx, loaded, instance, model.PluginTriggerEvent, detail, key, "")
		reason := ""
		switch CodeOf(err) {
		case "":
			if created {
				s.Wake()
			}
		case CodeRunInProgress:
			reason = SkipOverlap
		case CodeRateLimited:
			reason = SkipQueueFull
		default:
			reason = SkipNotRunnable
		}
		_ = s.store.MarkPluginSchedule(ctx, schedule.ID, reason == "", reason, nil)
	}
}
