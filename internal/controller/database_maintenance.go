package controller

import (
	"context"
	"log"
	"time"
)

const (
	databaseMaintenanceInterval       = time.Hour
	databaseMaintenanceTick           = 30 * time.Second
	databaseMaintenanceTimeout        = 2 * time.Minute
	databaseMaintenanceCatchUpTimeout = 5 * time.Minute

	// Connection presence expires on a much shorter horizon than the hourly
	// retention sweep, so it gets its own cadence inside the same loop rather
	// than a second maintenance goroutine or a per-server job.
	presencePruneInterval = 2 * time.Minute
	// A pass is bounded by batch size, batch count and wall time so it shares
	// the maintenance budget with retention deletes and checkpoints instead of
	// holding the write lock through a whole backlog. These are protective
	// limits, not a promised duration on any particular host.
	presencePruneBatch      = 500
	presencePruneMaxBatches = 8
	presencePruneTimeout    = 5 * time.Second
	// After a bounded pass that hit its limit, come back promptly to keep
	// working the backlog off instead of waiting a full interval.
	presencePruneCatchUpDelay = 5 * time.Second
)

func (s *Server) StartDatabaseMaintenance(ctx context.Context) {
	if !s.databaseMaintenanceStarted.CompareAndSwap(false, true) {
		return
	}
	defer s.databaseMaintenanceStarted.Store(false)
	catchUp := s.runDatabaseMaintenance(ctx, true)
	lastFull := time.Now()
	ticker := time.NewTicker(databaseMaintenanceTick)
	defer ticker.Stop()
	rollup := newLatencyRollupSchedule()
	var rollupTick <-chan time.Time
	var rollupTimer *time.Timer
	if rollup != nil {
		rollupTimer = time.NewTimer(2 * time.Second)
		rollupTick = rollupTimer.C
		defer rollupTimer.Stop()
	}
	presenceTimer := time.NewTimer(presencePruneInterval)
	defer presenceTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-rollupTick:
			rollupTimer.Reset(s.runLatencyRollup(ctx, rollup))
		case <-presenceTimer.C:
			presenceTimer.Reset(s.runConnectionPresencePrune(ctx))
		case <-ticker.C:
			if catchUp || time.Since(lastFull) >= databaseMaintenanceInterval {
				catchUp = s.runDatabaseMaintenance(ctx, catchUp)
				lastFull = time.Now()
				continue
			}
			s.runWALCheckpoint(ctx)
		}
	}
}

func (s *Server) runDatabaseMaintenance(ctx context.Context, catchUp bool) bool {
	startedAt := time.Now()
	timeout := databaseMaintenanceTimeout
	if catchUp {
		timeout = databaseMaintenanceCatchUpTimeout
	}
	maintenanceCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := s.store.RunMaintenance(maintenanceCtx, startedAt.UTC())
	stats := s.store.DBStats()
	duration := time.Since(startedAt)
	if err != nil {
		log.Printf("database maintenance failed: %v max_open=%d open=%d in_use=%d idle=%d wait_count=%d wait_duration=%s duration=%s", err, stats.MaxOpenConnections, stats.OpenConnections, stats.InUse, stats.Idle, stats.WaitCount, stats.WaitDuration, duration)
		return true
	}
	deleted := result.ConnectionAuditsDeleted + result.SubscriptionAuditsDeleted + result.ProbeEpisodesDeleted + result.RateBucketsDeleted + result.ServerMetricSamplesDeleted + result.LatencyProbeResultsDeleted + result.ConnectivityProbesDeleted + result.AgentTasksDeleted
	if deleted == 0 && result.FreePagesReclaimed == 0 && result.WALBusyFrames == 0 && result.WALLogFrames == 0 && result.WALCheckpointedFrames == 0 && !result.NeedsCatchUp {
		return false
	}
	log.Printf("database maintenance completed: connection_audits_deleted=%d subscription_audits_deleted=%d probe_episodes_deleted=%d rate_buckets_deleted=%d server_metrics_deleted=%d latency_results_deleted=%d connectivity_events_deleted=%d agent_tasks_deleted=%d pages_reclaimed=%d wal_busy=%d wal_log=%d wal_checkpointed=%d catch_up=%v max_open=%d open=%d in_use=%d idle=%d wait_count=%d wait_duration=%s duration=%s", result.ConnectionAuditsDeleted, result.SubscriptionAuditsDeleted, result.ProbeEpisodesDeleted, result.RateBucketsDeleted, result.ServerMetricSamplesDeleted, result.LatencyProbeResultsDeleted, result.ConnectivityProbesDeleted, result.AgentTasksDeleted, result.FreePagesReclaimed, result.WALBusyFrames, result.WALLogFrames, result.WALCheckpointedFrames, result.NeedsCatchUp, stats.MaxOpenConnections, stats.OpenConnections, stats.InUse, stats.Idle, stats.WaitCount, stats.WaitDuration, duration)
	return result.NeedsCatchUp
}

func (s *Server) runWALCheckpoint(ctx context.Context) {
	checkpointCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := s.store.CheckpointWAL(checkpointCtx)
	if err != nil {
		log.Printf("database WAL checkpoint failed: %v", err)
		return
	}
	if result.WALLogFrames == 0 && result.WALBusyFrames == 0 && result.WALCheckpointedFrames == 0 {
		return
	}
	log.Printf("database WAL checkpoint: wal_busy=%d wal_log=%d wal_checkpointed=%d", result.WALBusyFrames, result.WALLogFrames, result.WALCheckpointedFrames)
}

// runConnectionPresencePrune deletes expired connection presence in bounded
// batches and reports how long to wait before the next pass.
//
// This work used to run inside every Agent presence report transaction, which
// made one node's upload cadence the delete schedule for the whole fleet and
// put a growing global DELETE on the critical path of an ordinary report. No
// reader depends on it: presence is always read through its business validity
// window and through the server's effective audit state, so an expired row that
// is still on disk is already excluded.
func (s *Server) runConnectionPresencePrune(ctx context.Context) time.Duration {
	pruneCtx, cancel := context.WithTimeout(ctx, presencePruneTimeout)
	defer cancel()
	result, err := s.store.PruneConnectionPresence(pruneCtx, time.Now().UTC(), presencePruneBatch, presencePruneMaxBatches)
	if err != nil {
		if ctx.Err() != nil {
			return presencePruneInterval
		}
		log.Printf("connection presence prune failed: %v", err)
		return presencePruneInterval
	}
	if result.EventsDeleted > 0 || result.StatesDeleted > 0 {
		log.Printf("connection presence pruned: events=%d states=%d more=%v", result.EventsDeleted, result.StatesDeleted, result.More)
	}
	if result.More {
		return presencePruneCatchUpDelay
	}
	return presencePruneInterval
}
