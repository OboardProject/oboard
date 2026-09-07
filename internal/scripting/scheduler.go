package scripting

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Service) Tick(ctx context.Context) {
	settings := s.Settings(ctx)
	if !settings.Enabled || settings.SchedulerPaused {
		return
	}
	s.tickTimed(ctx)
	s.tickEvents(ctx)
	s.tickMetrics(ctx)
}

func (s *Service) tickTimed(ctx context.Context) {
	bindings, states, err := s.store.ListDueScriptTriggers(ctx, s.now(), 32)
	if err != nil {
		return
	}
	for i, binding := range bindings {
		state := states[i]
		spec, err := ParseTriggerSpec(binding.SpecJSON)
		if err != nil {
			continue
		}
		if state.NextDueAt == nil {
			continue
		}
		slot := *state.NextDueAt
		if DSTGap(spec, slot) {
			s.skipTrigger(ctx, binding, state, spec, model.ScriptSkipDSTGap)
			continue
		}
		if spec.MaxDelaySeconds > 0 && s.now().Sub(slot) > time.Duration(spec.MaxDelaySeconds)*time.Second {
			s.skipTrigger(ctx, binding, state, spec, model.ScriptSkipMissed)
			continue
		}
		if s.now().Sub(slot) > time.Minute && !spec.CatchupOnce {
			s.skipTrigger(ctx, binding, state, spec, model.ScriptSkipMissed)
			continue
		}
		if hasPowerCapability(ctx, s.store, binding) && s.now().After(slot.Add(2*time.Second)) {
			s.skipTrigger(ctx, binding, state, spec, model.ScriptSkipMissed)
			continue
		}
		key := SlotKey(binding.ID, slot)
		if _, created, err := s.EnqueueTriggerRun(ctx, binding, key, binding.Kind, map[string]any{
			"scheduled_at": slot.UTC().Format(time.RFC3339),
			"subject_server_id": "",
		}); err != nil {
			if CodeOf(err) == codeLimitExceeded {
				s.recordSkip(ctx, state, model.ScriptSkipOverlap)
			}
			continue
		} else if !created {
			continue
		}
		fired := s.now()
		state.LastFiredAt = &fired
		state.LastSkipReason = ""
		s.advanceTimedState(ctx, binding, state, spec, slot)
	}
}

func (s *Service) tickEvents(ctx context.Context) {
	items, err := s.store.ClaimScriptEvents(ctx, "script-scheduler", s.now().Add(30*time.Second), 32)
	if err != nil {
		return
	}
	for _, item := range items {
		s.handleOutbox(ctx, item)
		_ = s.store.CompleteScriptEvent(ctx, item.ID)
	}
}

func (s *Service) handleOutbox(ctx context.Context, item store.EventOutboxItem) {
	var payload map[string]any
	_ = json.Unmarshal(item.Payload, &payload)
	eventType, _ := payload["event"].(string)
	if eventType == "" {
		eventType = strings.TrimPrefix(item.Topic, "script.")
	}
	bindings, err := s.store.ListEnabledEventTriggers(ctx, eventType)
	if err != nil {
		return
	}
	subjectID, _ := intFromAny(payload["server_id"])
	incidentID, _ := intFromAny(payload["incident_id"])
	for _, binding := range bindings {
		spec, err := ParseTriggerSpec(binding.SpecJSON)
		if err != nil {
			continue
		}
		if len(spec.SubjectServerIDs) > 0 && !containsInt64(spec.SubjectServerIDs, subjectID) {
			continue
		}
		state, _ := s.store.GetScriptTriggerState(ctx, binding.ID)
		key := EventKey(binding.ID, subjectID, incidentID, eventType)
		if state.CurrentCycleKey == key && !spec.RepeatWhileOffline {
			s.recordSkip(ctx, state, model.ScriptSkipDuplicate)
			continue
		}
		if spec.SustainSeconds > 0 && eventType == model.ScriptEventServerOffline {
			hold := s.now().Add(time.Duration(spec.SustainSeconds) * time.Second)
			state.HoldUntil = &hold
			state.CurrentCycleKey = key
			_ = s.store.UpsertScriptTriggerState(ctx, state)
			continue
		}
		snapshot := map[string]any{
			"event": eventType,
			"subject_server_id": formatID(subjectID),
			"incident_id": formatID(incidentID),
			"cause_run_id": payload["cause_run_id"],
		}
		if _, created, err := s.EnqueueTriggerRun(ctx, binding, key, eventType, snapshot); err != nil {
			if CodeOf(err) == codeLimitExceeded {
				s.recordSkip(ctx, state, model.ScriptSkipOverlap)
			}
			continue
		} else if created {
			fired := s.now()
			state.LastFiredAt = &fired
			state.CurrentCycleKey = key
			state.LastSkipReason = ""
			_ = s.store.UpsertScriptTriggerState(ctx, state)
		}
	}
}

func (s *Service) TickSustain(ctx context.Context) {
	bindings, err := s.store.ListScriptTriggers(ctx, 0)
	if err != nil {
		return
	}
	for _, binding := range bindings {
		if !binding.Enabled || binding.Kind != model.ScriptTriggerEvent {
			continue
		}
		spec, err := ParseTriggerSpec(binding.SpecJSON)
		if err != nil || spec.SustainSeconds <= 0 {
			continue
		}
		state, err := s.store.GetScriptTriggerState(ctx, binding.ID)
		if err != nil || state.HoldUntil == nil || state.HoldUntil.After(s.now()) {
			continue
		}
		incidentID, serverID := parseCycle(state.CurrentCycleKey)
		incident, err := s.store.GetNodeIncident(ctx, incidentID)
		if err != nil || incident == nil || incident.Status != model.NodeIncidentActive {
			s.recordSkip(ctx, state, model.ScriptSkipConditionChanged)
			state.HoldUntil = nil
			_ = s.store.UpsertScriptTriggerState(ctx, state)
			continue
		}
		snapshot := map[string]any{"event": spec.Event, "subject_server_id": formatID(serverID), "incident_id": formatID(incidentID)}
		if _, created, err := s.EnqueueTriggerRun(ctx, binding, state.CurrentCycleKey, spec.Event, snapshot); err == nil && created {
			fired := s.now()
			state.LastFiredAt = &fired
			state.HoldUntil = nil
			_ = s.store.UpsertScriptTriggerState(ctx, state)
		}
	}
}

func (s *Service) tickMetrics(ctx context.Context) {
	bindings, err := s.store.ListEnabledEventTriggers(ctx, model.ScriptEventMetricConditionEntered)
	if err != nil {
		return
	}
	cleared, _ := s.store.ListEnabledEventTriggers(ctx, model.ScriptEventMetricConditionCleared)
	bindings = append(bindings, cleared...)
	for _, binding := range bindings {
		if !binding.Enabled {
			continue
		}
		spec, err := ParseTriggerSpec(binding.SpecJSON)
		if err != nil || spec.Metric == nil {
			continue
		}
		state, _ := s.store.GetScriptTriggerState(ctx, binding.ID)
		var cond metricRuntime
		_ = json.Unmarshal(state.ConditionJSON, &cond)
		for _, serverID := range spec.SubjectServerIDs {
			samples, err := s.store.ListServerMetricSamples(ctx, serverID, 1)
			if err != nil || len(samples) == 0 {
				cond.Unknown = true
				cond.EnteredSince = nil
				continue
			}
			sample := samples[0]
			if sample.SampledAt.IsZero() || s.now().Sub(sample.SampledAt) > 3*time.Minute {
				cond.Unknown = true
				cond.EnteredSince = nil
				continue
			}
			if cond.LastSampleAt != nil && !sample.SampledAt.After(*cond.LastSampleAt) {
				continue
			}
			value := metricValue(spec.Metric.Metric, sample)
			cond.LastSampleAt = &sample.SampledAt
			cond.Unknown = false
			if compareMetric(value, spec.Metric.Operator, spec.Metric.Threshold) {
				if cond.EnteredSince == nil {
					t := sample.SampledAt
					cond.EnteredSince = &t
				}
				if spec.Metric.DurationSeconds <= 0 || sample.SampledAt.Sub(*cond.EnteredSince) >= time.Duration(spec.Metric.DurationSeconds)*time.Second {
					if !cond.Fired {
						key := EventKey(binding.ID, serverID, sample.SampledAt.Unix(), model.ScriptEventMetricConditionEntered)
						_, _, _ = s.EnqueueTriggerRun(ctx, binding, key, model.ScriptEventMetricConditionEntered, map[string]any{
							"subject_server_id": formatID(serverID), "metric": spec.Metric.Metric, "value": value,
						})
						cond.Fired = true
						cond.ClearedSince = nil
					}
				}
			} else if cond.Fired && compareMetric(value, clearOperator(spec.Metric.Operator), spec.Metric.ClearThreshold) {
				if cond.ClearedSince == nil {
					t := sample.SampledAt
					cond.ClearedSince = &t
				}
				if spec.Metric.ClearDurationSec <= 0 || sample.SampledAt.Sub(*cond.ClearedSince) >= time.Duration(spec.Metric.ClearDurationSec)*time.Second {
					key := EventKey(binding.ID, serverID, sample.SampledAt.Unix(), model.ScriptEventMetricConditionCleared)
					_, _, _ = s.EnqueueTriggerRun(ctx, binding, key, model.ScriptEventMetricConditionCleared, map[string]any{
						"subject_server_id": formatID(serverID), "metric": spec.Metric.Metric, "value": value,
					})
					cond.Fired = false
					cond.EnteredSince = nil
					cond.ClearedSince = nil
				}
			} else {
				cond.EnteredSince = nil
			}
		}
		state.ConditionJSON = MustJSON(cond)
		_ = s.store.UpsertScriptTriggerState(ctx, state)
	}
}

type metricRuntime struct {
	Unknown      bool       `json:"unknown"`
	Fired        bool       `json:"fired"`
	EnteredSince *time.Time `json:"entered_since,omitempty"`
	ClearedSince *time.Time `json:"cleared_since,omitempty"`
	LastSampleAt *time.Time `json:"last_sample_at,omitempty"`
}

func (s *Service) skipTrigger(ctx context.Context, binding model.ScriptTriggerBinding, state model.ScriptTriggerState, spec model.ScriptTriggerSpec, reason string) {
	s.recordSkip(ctx, state, reason)
	if state.NextDueAt != nil {
		s.advanceTimedState(ctx, binding, state, spec, *state.NextDueAt)
	}
}

func (s *Service) recordSkip(ctx context.Context, state model.ScriptTriggerState, reason string) {
	skipped := s.now()
	state.LastSkippedAt = &skipped
	state.LastSkipReason = reason
	_ = s.store.UpsertScriptTriggerState(ctx, state)
}

func (s *Service) advanceTimedState(ctx context.Context, binding model.ScriptTriggerBinding, state model.ScriptTriggerState, spec model.ScriptTriggerSpec, current time.Time) {
	if binding.Kind == model.ScriptTriggerOnce {
		state.NextDueAt = nil
		state.Armed = false
		_ = s.store.UpsertScriptTriggerState(ctx, state)
		return
	}
	slots, err := NextSlots(spec, current.Add(time.Second), 1)
	if err == nil && len(slots) > 0 {
		state.NextDueAt = &slots[0]
	} else {
		state.NextDueAt = nil
	}
	_ = s.store.UpsertScriptTriggerState(ctx, state)
}

func hasPowerCapability(ctx context.Context, db *store.Store, binding model.ScriptTriggerBinding) bool {
	rev, err := db.GetScriptRevision(ctx, binding.RevisionID)
	if err != nil {
		return false
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return false
	}
	for _, name := range manifest.Capabilities {
		if powerCapabilities[name] {
			return true
		}
	}
	return false
}

func intFromAny(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		n, err := typed.Int64()
		return n, err == nil
	case string:
		id, err := parseID(typed)
		return id, err == nil && id > 0
	default:
		return 0, false
	}
}

func formatID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func containsInt64(items []int64, want int64) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func parseCycle(key string) (incidentID, serverID int64) {
	parts := strings.Split(key, ":")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "incident" {
			incidentID, _ = parseID(parts[i+1])
		}
		if parts[i] == "server" {
			serverID, _ = parseID(parts[i+1])
		}
	}
	return incidentID, serverID
}

func metricValue(name string, sample model.ServerMetricSample) float64 {
	switch name {
	case "cpu", "cpu_usage_percent":
		return sample.CPUUsagePercent
	case "memory", "memory_used_ratio":
		if sample.MemoryTotalBytes <= 0 {
			return 0
		}
		return float64(sample.MemoryUsedBytes) / float64(sample.MemoryTotalBytes) * 100
	default:
		return sample.CPUUsagePercent
	}
}

func compareMetric(value float64, op string, threshold float64) bool {
	switch op {
	case ">", "gt":
		return value > threshold
	case ">=", "gte":
		return value >= threshold
	case "<", "lt":
		return value < threshold
	case "<=", "lte":
		return value <= threshold
	default:
		return value > threshold
	}
}

func clearOperator(op string) string {
	switch op {
	case ">", "gt", ">=", "gte":
		return "<"
	default:
		return ">"
	}
}
