package controller

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

const (
	agentUpdateMaxConcurrencySetting  = "agent_update_max_concurrency"
	managedUpdateStartupQuietSetting  = "managed_update_startup_quiet_seconds"
	agentUpdateDefaultQuietSeconds    = 30
	agentUpdateFallbackPeriod         = 12 * time.Minute
	agentUpdateRetryFirst             = 10 * time.Minute
	agentUpdateRetrySecond            = time.Hour
	agentUpdateCircuitMinAttempted    = 10
	agentUpdateCircuitFailureRatio    = 0.4
	agentUpdateAutoConcurrencyMin     = 4
	agentUpdateAutoConcurrencyMax     = 16
	agentUpdateAutoConcurrencyPercent = 0.02
	relayUpdateMaxConcurrency         = 4
)

type agentUpdateCoordinator struct {
	server *Server
	wake   chan struct{}
	mu     sync.Mutex
}

type agentFleetFillResult struct {
	store.AgentFleetCounts
	Created int
	Limit   int
	Rolling bool
}

func newAgentUpdateCoordinator(server *Server) *agentUpdateCoordinator {
	return &agentUpdateCoordinator{server: server, wake: make(chan struct{}, 1)}
}

func (c *agentUpdateCoordinator) Wake() {
	if c == nil {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *agentUpdateCoordinator) Start(ctx context.Context) {
	if c == nil {
		return
	}
	quiet := c.quietPeriod(ctx)
	if quiet > 0 {
		timer := time.NewTimer(quiet)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	c.Fill(ctx, false)
	c.fillRelayUpdates(ctx)
	ticker := time.NewTicker(agentUpdateFallbackPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			c.Fill(ctx, false)
			c.fillRelayUpdates(ctx)
		case <-ticker.C:
			c.Fill(ctx, false)
			c.fillRelayUpdates(ctx)
		}
	}
}

func (c *agentUpdateCoordinator) quietPeriod(ctx context.Context) time.Duration {
	settings, err := c.server.store.ListSettings(ctx)
	if err != nil {
		return time.Duration(agentUpdateDefaultQuietSeconds) * time.Second
	}
	seconds := settingInt(settings, managedUpdateStartupQuietSetting, agentUpdateDefaultQuietSeconds, 0, 300)
	return time.Duration(seconds) * time.Second
}

func (c *agentUpdateCoordinator) concurrency(ctx context.Context, settings map[string]string) int {
	configured := settingInt(settings, agentUpdateMaxConcurrencySetting, 0, 0, 32)
	if configured > 0 {
		return configured
	}
	online, err := c.server.store.CountOnlineEnrolledAgents(ctx)
	if err != nil || online < 1 {
		return agentUpdateAutoConcurrencyMin
	}
	auto := int(math.Ceil(float64(online) * agentUpdateAutoConcurrencyPercent))
	if auto < agentUpdateAutoConcurrencyMin {
		auto = agentUpdateAutoConcurrencyMin
	}
	if auto > agentUpdateAutoConcurrencyMax {
		auto = agentUpdateAutoConcurrencyMax
	}
	return auto
}

// Fill occupies free Agent update slots. manual starts an operator roll that
// continues after this call: later Wake/Fill(false) still bypasses the
// auto-update switch and maintenance window until the enrolled fleet is
// current. Pause and the circuit breaker still stop refill.
func (c *agentUpdateCoordinator) Fill(ctx context.Context, manual bool) agentFleetFillResult {
	empty := agentFleetFillResult{}
	if c == nil {
		return empty
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	settings, err := c.server.store.ListSettings(ctx)
	if err != nil {
		return empty
	}
	targetBuild := strings.TrimSpace(version.AgentBuild)
	if targetBuild == "" || strings.EqualFold(targetBuild, "dev") {
		return empty
	}
	state, err := c.server.store.GetAgentFleetState(ctx)
	if err != nil {
		return empty
	}
	if !manual && !state.Rolling {
		if !settingBool(settings, agentAutoUpdateSetting, false) {
			return empty
		}
		if !automaticUpdateAllowedAt(settings, time.Now()) {
			return empty
		}
	}
	dirty := false
	if state.TargetBuild != targetBuild {
		state = store.AgentFleetState{TargetBuild: targetBuild, Rolling: state.Rolling || manual}
		dirty = true
		_ = c.server.store.ClearAgentUpdateRetriesForBuild(ctx, targetBuild)
	}
	if manual && !state.Rolling {
		state.Rolling = true
		dirty = true
	}
	limit := c.concurrency(ctx, settings)
	persist := func() {
		if dirty {
			_ = c.server.store.SaveAgentFleetState(ctx, state)
			dirty = false
		}
	}
	finish := func(counts store.AgentFleetCounts, created int, counted bool) agentFleetFillResult {
		if counted && state.Rolling && counts.Running == 0 && counts.Outdated == 0 {
			state.Rolling = false
			dirty = true
		}
		persist()
		return agentFleetFillResult{AgentFleetCounts: counts, Created: created, Limit: limit, Rolling: state.Rolling}
	}
	if !manual && state.Paused {
		counts, countErr := c.server.store.CountAgentUpdateFleet(ctx, targetBuild)
		return finish(counts, 0, countErr == nil)
	}
	active, err := c.server.store.CountActiveAgentUpdates(ctx)
	if err != nil {
		persist()
		return empty
	}
	free := limit - active
	if free < 1 {
		counts, countErr := c.server.store.CountAgentUpdateFleet(ctx, targetBuild)
		return finish(counts, 0, countErr == nil)
	}
	candidates, err := c.server.store.ListAgentUpdateCandidates(ctx, targetBuild, free*4)
	if err != nil {
		persist()
		return empty
	}
	versionStamp := time.Now().Unix()
	enqueued := 0
	now := time.Now().UTC()
	for index := range candidates {
		if enqueued >= free {
			break
		}
		item := candidates[index]
		if !buildNeedsUpdate(item.AgentBuild, targetBuild) {
			continue
		}
		retry, retryErr := c.server.store.GetAgentUpdateRetry(ctx, item.ServerID, targetBuild)
		if retryErr != nil {
			continue
		}
		if retry.Attempts >= 3 {
			continue
		}
		if retry.NextRetryAt != nil && retry.NextRetryAt.After(now) {
			continue
		}
		server := &model.Server{ID: item.ServerID, AgentID: item.AgentID, AgentBuild: item.AgentBuild, Status: item.Status}
		task, existing, err := c.server.enqueueAgentUpdateWithVersion(ctx, server, model.AgentUpdateRequest{Source: "auto"}, versionStamp)
		if err != nil {
			continue
		}
		if existing || task.ID == 0 || task.Status == "failed" {
			continue
		}
		enqueued++
		state.Attempted++
		dirty = true
	}
	if enqueued > 0 {
		log.Printf("agent fleet update enqueued=%d active_limit=%d target_build=%s rolling=%t", enqueued, limit, targetBuild, state.Rolling)
		c.server.publishRealtime("agent-updates", "controller-update")
	}
	counts, countErr := c.server.store.CountAgentUpdateFleet(ctx, targetBuild)
	return finish(counts, enqueued, countErr == nil)
}

func (c *agentUpdateCoordinator) fillRelayUpdates(ctx context.Context) {
	if c == nil {
		return
	}
	settings, err := c.server.store.ListSettings(ctx)
	if err != nil || !settingBool(settings, subscriptionRelayAutoUpdateSetting, false) || !automaticUpdateAllowedAt(settings, time.Now()) {
		return
	}
	targetBuild := strings.TrimSpace(version.Build)
	if targetBuild == "" || strings.EqualFold(targetBuild, "dev") {
		return
	}
	active, err := c.server.store.CountActiveRelayUpdates(ctx)
	if err != nil {
		return
	}
	free := relayUpdateMaxConcurrency - active
	if free < 1 {
		return
	}
	candidates, err := c.server.store.ListRelayUpdateCandidates(ctx, targetBuild, free)
	if err != nil {
		return
	}
	for index := range candidates {
		item := candidates[index]
		if !buildNeedsUpdate(item.Build, targetBuild) {
			continue
		}
		_ = c.server.store.RequestSubscriptionRelayUpdate(ctx, item.ID, version.Version, targetBuild)
	}
}

func (s *Server) noteAgentUpdateOutcome(ctx context.Context, serverID int64, status, errorText, targetBuild string) {
	targetBuild = strings.TrimSpace(targetBuild)
	if targetBuild == "" {
		targetBuild = strings.TrimSpace(version.AgentBuild)
	}
	state, err := s.store.GetAgentFleetState(ctx)
	if err != nil {
		return
	}
	if state.TargetBuild != targetBuild {
		state = store.AgentFleetState{TargetBuild: targetBuild, Rolling: state.Rolling}
	}
	switch status {
	case "succeeded":
		state.Succeeded++
		_ = s.store.ClearAgentUpdateRetry(ctx, serverID)
	case "failed", "rollback_failed":
		state.Failed++
		retry, retryErr := s.store.GetAgentUpdateRetry(ctx, serverID, targetBuild)
		if retryErr != nil {
			retry = store.AgentUpdateRetry{ServerID: serverID, TargetBuild: targetBuild}
		}
		retry.Attempts++
		retry.LastError = strings.TrimSpace(errorText)
		next := time.Now().UTC()
		switch retry.Attempts {
		case 1:
			next = next.Add(agentUpdateRetryFirst)
			retry.NextRetryAt = &next
		case 2:
			next = next.Add(agentUpdateRetrySecond)
			retry.NextRetryAt = &next
		default:
			retry.NextRetryAt = nil
		}
		_ = s.store.SaveAgentUpdateRetry(ctx, retry)
		if state.Attempted >= agentUpdateCircuitMinAttempted && float64(state.Failed)/float64(state.Attempted) >= agentUpdateCircuitFailureRatio {
			state.Paused = true
			state.LastPauseReason = "连续更新失败率过高，已暂停自动滚动"
			log.Printf("agent fleet update paused attempted=%d failed=%d target_build=%s", state.Attempted, state.Failed, targetBuild)
		}
	}
	_ = s.store.SaveAgentFleetState(ctx, state)
	s.publishRealtime("agent-updates")
	if s.agentUpdates != nil {
		s.agentUpdates.Wake()
	}
}

func (s *Server) agentFleetStatus(ctx context.Context) (map[string]any, error) {
	targetBuild := strings.TrimSpace(version.AgentBuild)
	counts, err := s.store.CountAgentUpdateFleet(ctx, targetBuild)
	if err != nil {
		return nil, err
	}
	state, err := s.store.GetAgentFleetState(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	concurrency := 0
	if s.agentUpdates != nil {
		concurrency = s.agentUpdates.concurrency(ctx, settings)
	}
	return map[string]any{
		"target_build":          targetBuild,
		"target_version":        version.AgentVersion,
		"paused":                state.Paused,
		"rolling":               state.Rolling,
		"pause_reason":          state.LastPauseReason,
		"attempted":             state.Attempted,
		"succeeded":             state.Succeeded,
		"failed":                state.Failed,
		"total":                 counts.Total,
		"enrolled":              counts.Enrolled,
		"current":               counts.Current,
		"pending":               counts.Pending,
		"running":               counts.Running,
		"offline":               counts.Offline,
		"max_concurrency":       settingInt(settings, agentUpdateMaxConcurrencySetting, 0, 0, 32),
		"effective_concurrency": concurrency,
		"startup_quiet_seconds": settingInt(settings, managedUpdateStartupQuietSetting, agentUpdateDefaultQuietSeconds, 0, 300),
		"auto_update_enabled":   settingBool(settings, agentAutoUpdateSetting, false),
		"message":               "Controller 更新成功后，Agent 版本同步将在后台滚动进行。",
	}, nil
}

func agentUpdatePayloadBuild(task model.AgentTask) string {
	var payload model.UpdateAgentTaskPayload
	if json.Unmarshal([]byte(task.PayloadJSON), &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.ExpectedBuild)
}
