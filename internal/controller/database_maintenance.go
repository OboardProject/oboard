package controller

import (
	"context"
	"log"
	"time"
)

const (
	databaseMaintenanceInterval = time.Hour
	databaseMaintenanceTimeout  = 2 * time.Minute
)

func (s *Server) StartDatabaseMaintenance(ctx context.Context) {
	if !s.databaseMaintenanceStarted.CompareAndSwap(false, true) {
		return
	}
	defer s.databaseMaintenanceStarted.Store(false)
	s.runDatabaseMaintenance(ctx)
	ticker := time.NewTicker(databaseMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDatabaseMaintenance(ctx)
		}
	}
}

func (s *Server) runDatabaseMaintenance(ctx context.Context) {
	startedAt := time.Now()
	maintenanceCtx, cancel := context.WithTimeout(ctx, databaseMaintenanceTimeout)
	defer cancel()
	result, err := s.store.RunMaintenance(maintenanceCtx, startedAt.UTC())
	stats := s.store.DBStats()
	duration := time.Since(startedAt)
	if err != nil {
		log.Printf("database maintenance failed: %v max_open=%d open=%d in_use=%d idle=%d wait_count=%d wait_duration=%s duration=%s", err, stats.MaxOpenConnections, stats.OpenConnections, stats.InUse, stats.Idle, stats.WaitCount, stats.WaitDuration, duration)
		return
	}
	deleted := result.ConnectionAuditsDeleted + result.SubscriptionAuditsDeleted + result.ProbeEpisodesDeleted + result.RateBucketsDeleted
	if deleted == 0 && result.WALBusyFrames == 0 && result.WALLogFrames == 0 && result.WALCheckpointedFrames == 0 {
		return
	}
	log.Printf("database maintenance completed: connection_audits_deleted=%d subscription_audits_deleted=%d probe_episodes_deleted=%d rate_buckets_deleted=%d wal_busy=%d wal_log=%d wal_checkpointed=%d max_open=%d open=%d in_use=%d idle=%d wait_count=%d wait_duration=%s duration=%s", result.ConnectionAuditsDeleted, result.SubscriptionAuditsDeleted, result.ProbeEpisodesDeleted, result.RateBucketsDeleted, result.WALBusyFrames, result.WALLogFrames, result.WALCheckpointedFrames, stats.MaxOpenConnections, stats.OpenConnections, stats.InUse, stats.Idle, stats.WaitCount, stats.WaitDuration, duration)
}
