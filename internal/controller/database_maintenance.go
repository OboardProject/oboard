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
	for {
		select {
		case <-ctx.Done():
			return
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
