package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	maintenanceBatchSize  = 2000
	maintenanceMaxBatches = 50
)

const (
	subscriptionBucketRetention = 24 * time.Hour
	// maintenanceReclaimPages bounds one incremental vacuum pass. At the 4 KiB
	// default page size this returns up to 64 MiB per pass, which is far
	// above the steady-state churn of the retention deletes above.
	maintenanceReclaimPages = 16384
	// maintenanceWALTruncateFrames is the WAL size, in frames, past which the
	// passive checkpoint is assumed to be losing to readers.
	maintenanceWALTruncateFrames = 4096
	// maintenanceCheckpointEveryBatches asks SQLite to flush WAL frames after
	// this many delete batches so a catch-up pass cannot rebuild a multi-GB
	// WAL while it is the only thing shrinking the reporting tables.
	maintenanceCheckpointEveryBatches = 5

	// Low-priority audit rollup budget: accounting retention always runs
	// first; dirty drain and hourly backfill are capped so they cannot starve
	// writer availability under backlog.
	maintenanceHourlyDirtyDrainLimit   = 64
	maintenanceHourlyBackfillPageLimit = 200
	maintenanceRollupMaxWall           = 2 * time.Second

	ServerMonitoringRetentionDaysSetting = "server_monitoring_retention_days"
	DefaultServerMonitoringRetentionDays = 7
	MinServerMonitoringRetentionDays     = 1
	MaxServerMonitoringRetentionDays     = 30
)

const historicalTaskRetention = 30 * 24 * time.Hour

type MaintenanceResult struct {
	ConnectionAuditsDeleted       int64
	SubscriptionAuditsDeleted     int64
	ProbeEpisodesDeleted          int64
	RateBucketsDeleted            int64
	ServerMetricSamplesDeleted    int64
	LatencyProbeResultsDeleted    int64
	ConnectivityProbesDeleted     int64
	AgentTasksDeleted             int64
	FreePagesReclaimed            int64
	DNSBenchmarkRunsDeleted       int64
	DNSBenchmarkResultsDeleted    int64
	MTUDetectionsDeleted          int64
	InboundProbesDeleted          int64
	PortForwardProbesDeleted      int64
	NotificationDeliveriesDeleted int64
	HourlyDirtyDrained            int
	HourlyBackfillAdvanced        int
	RollupBudgetExhausted         bool
	WALBusyFrames                 int
	WALLogFrames                  int
	WALCheckpointedFrames         int
	NeedsCatchUp                  bool
}

func (s *Store) RunMaintenance(ctx context.Context, at time.Time) (MaintenanceResult, error) {
	at = at.UTC()
	result := MaintenanceResult{}
	settings, err := s.ListSettings(ctx)
	if err != nil {
		return result, fmt.Errorf("load monitoring retention setting: %w", err)
	}
	monitoringRetention := time.Duration(ServerMonitoringRetentionDays(settings)) * 24 * time.Hour
	jobs := []struct {
		name   string
		query  string
		custom func(context.Context, time.Time) (int64, bool, error)
		cutoff time.Time
		count  *int64
	}{
		{
			name:   "connection audit retention",
			query:  `delete from connection_audit_reports where rowid in (select rowid from connection_audit_reports where ended_at < ? order by ended_at limit ?)`,
			cutoff: at.Add(-connectionAuditRetention),
			count:  &result.ConnectionAuditsDeleted,
		},
		{
			name:   "subscription audit retention",
			query:  `delete from subscription_pull_audits where id in (select id from subscription_pull_audits where requested_at < ? order by requested_at limit ?)`,
			cutoff: at.Add(-subscriptionAuditRetention),
			count:  &result.SubscriptionAuditsDeleted,
		},
		{
			name:   "connection probe episode retention",
			query:  `delete from connection_probe_episodes where rowid in (select rowid from connection_probe_episodes where ended_at < ? order by ended_at limit ?)`,
			cutoff: at.Add(-connectionAuditRetention),
			count:  &result.ProbeEpisodesDeleted,
		},
		{
			name:   "subscription rate bucket retention",
			query:  `delete from subscription_rate_buckets where rowid in (select rowid from subscription_rate_buckets where updated_at < ? order by updated_at limit ?)`,
			cutoff: at.Add(-subscriptionBucketRetention),
			count:  &result.RateBucketsDeleted,
		},
		{
			name:   "server metric sample retention",
			query:  `delete from server_metric_samples where rowid in (select rowid from server_metric_samples where sampled_at < ? order by sampled_at limit ?)`,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.ServerMetricSamplesDeleted,
		},
		{
			name:   "latency probe result retention",
			query:  `delete from server_latency_probe_results where rowid in (select rowid from server_latency_probe_results where checked_at < ? order by checked_at limit ?)`,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.LatencyProbeResultsDeleted,
		},
		{
			name:   "connectivity event retention",
			custom: s.pruneExpiredConnectivityEvents,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.ConnectivityProbesDeleted,
		},
		{
			name:   "agent task retention",
			query:  `delete from agent_tasks where id in (select id from agent_tasks where status in ('succeeded','failed','rollback_failed') and completed_at < ? order by completed_at limit ?)`,
			cutoff: at.Add(-historicalTaskRetention),
			count:  &result.AgentTasksDeleted,
		},
		{
			name:   "dns benchmark run retention",
			query:  `delete from dns_benchmark_runs where id in (select id from dns_benchmark_runs where created_at < ? order by created_at limit ?)`,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.DNSBenchmarkRunsDeleted,
		},
		{
			name:   "dns benchmark result retention",
			query:  `delete from dns_benchmark_results where id in (select id from dns_benchmark_results where created_at < ? order by created_at limit ?)`,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.DNSBenchmarkResultsDeleted,
		},
		{
			name:   "mtu detection retention",
			query:  `delete from mtu_detection_results where id in (select id from mtu_detection_results where created_at < ? order by created_at limit ?)`,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.MTUDetectionsDeleted,
		},
		{
			name:   "inbound probe retention",
			query:  `delete from inbound_probe_results where id in (select id from inbound_probe_results where created_at < ? order by created_at limit ?)`,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.InboundProbesDeleted,
		},
		{
			name:   "port forward probe retention",
			query:  `delete from port_forward_probe_results where id in (select id from port_forward_probe_results where created_at < ? order by created_at limit ?)`,
			cutoff: at.Add(-monitoringRetention),
			count:  &result.PortForwardProbesDeleted,
		},
		{
			name:   "notification delivery retention",
			query:  `delete from notification_deliveries where id in (select id from notification_deliveries where status in ('sent','failed') and created_at < ? order by created_at limit ?)`,
			cutoff: at.Add(-historicalTaskRetention),
			count:  &result.NotificationDeliveriesDeleted,
		},
	}
	for _, job := range jobs {
		var deleted int64
		var hitCap bool
		var jobErr error
		if job.custom != nil {
			deleted, hitCap, jobErr = job.custom(ctx, job.cutoff)
		} else {
			deleted, hitCap, jobErr = s.deleteMaintenanceBatches(ctx, job.query, job.cutoff)
		}
		*job.count = deleted
		if hitCap {
			result.NeedsCatchUp = true
		}
		if jobErr != nil {
			if ckErr := s.recordWALCheckpoint(ctx, &result); ckErr != nil && result.WALLogFrames == 0 {
				return result, fmt.Errorf("%s: %w", job.name, jobErr)
			}
			return result, fmt.Errorf("%s: %w", job.name, jobErr)
		}
	}
	// Hourly rollups follow the same 30-day raw retention; never purge raw
	// reports early just because hourly rows exist.
	if _, err := s.PurgeConnectionAuditHourlyBefore(ctx, at.Add(-connectionAuditRetention)); err != nil {
		return result, fmt.Errorf("connection audit hourly retention: %w", err)
	}
	// Low-priority rollup work after accounting deletes. Cap wall time so a
	// dirty backlog cannot monopolize the writer during maintenance.
	rollupStarted := time.Now()
	drained, err := s.DrainConnectionAuditHourlyDirty(ctx, maintenanceHourlyDirtyDrainLimit)
	if err != nil {
		return result, fmt.Errorf("connection audit hourly dirty drain: %w", err)
	}
	result.HourlyDirtyDrained = drained
	if time.Since(rollupStarted) < maintenanceRollupMaxWall {
		advanced, complete, backfillErr := s.BackfillConnectionAuditHourlyPages(ctx, maintenanceHourlyBackfillPageLimit)
		if backfillErr != nil {
			return result, fmt.Errorf("connection audit hourly backfill: %w", backfillErr)
		}
		result.HourlyBackfillAdvanced = advanced
		if !complete && advanced >= maintenanceHourlyBackfillPageLimit {
			result.NeedsCatchUp = true
		}
	} else {
		result.RollupBudgetExhausted = true
		result.NeedsCatchUp = true
	}
	if drained >= maintenanceHourlyDirtyDrainLimit {
		result.NeedsCatchUp = true
	}
	if _, err := s.db.ExecContext(ctx, `pragma optimize`); err != nil {
		return result, fmt.Errorf("optimize SQLite database: %w", err)
	}
	// Return a bounded slice of the pages the deletes above freed to the
	// filesystem. This is a no-op when the database is not in incremental
	// auto-vacuum mode, and bounded so one maintenance pass cannot hold the
	// writer while it rewrites a large backlog.
	reclaimed, err := s.reclaimFreePages(ctx, maintenanceReclaimPages)
	if err != nil {
		return result, fmt.Errorf("reclaim free SQLite pages: %w", err)
	}
	result.FreePagesReclaimed = reclaimed
	if err := s.recordWALCheckpoint(ctx, &result); err != nil {
		return result, err
	}
	if err := s.recordMaintenanceResult(ctx, at, result); err != nil {
		return result, fmt.Errorf("record maintenance result: %w", err)
	}
	return result, nil
}

// CheckpointWAL flushes as many WAL frames as current readers allow. A
// truncating checkpoint is attempted only after the passive pass still leaves
// the log above the threshold; a busy reader is not an error.
func (s *Store) CheckpointWAL(ctx context.Context) (MaintenanceResult, error) {
	result := MaintenanceResult{}
	if err := s.recordWALCheckpoint(ctx, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) recordWALCheckpoint(ctx context.Context, result *MaintenanceResult) error {
	// wal_checkpoint returns (busy, log, checkpointed). busy=0 does not mean
	// every frame was backfilled into the main database; compare log vs
	// checkpointed. A database with no WAL reports (0, 0, 0).
	if err := s.db.QueryRowContext(ctx, `pragma wal_checkpoint(passive)`).Scan(
		&result.WALBusyFrames,
		&result.WALLogFrames,
		&result.WALCheckpointedFrames,
	); err != nil {
		return fmt.Errorf("passive WAL checkpoint: %w", err)
	}
	pending := result.WALLogFrames - result.WALCheckpointedFrames
	if pending < 0 {
		pending = 0
	}
	// Truncate only when passive already advanced through the log and the
	// remaining size still exceeds the threshold. A truncating checkpoint
	// against busy readers waits for them and can materialize a multi-GB WAL
	// into the main file on a nearly full disk.
	if result.WALBusyFrames == 0 && pending == 0 && result.WALLogFrames > maintenanceWALTruncateFrames {
		var busy, log, checkpointed int
		if err := s.db.QueryRowContext(ctx, `pragma wal_checkpoint(truncate)`).Scan(&busy, &log, &checkpointed); err == nil && busy == 0 {
			result.WALBusyFrames, result.WALLogFrames, result.WALCheckpointedFrames = busy, log, checkpointed
		}
	}
	return nil
}

// reclaimFreePages returns up to maxPages free pages to the filesystem.
func (s *Store) reclaimFreePages(ctx context.Context, maxPages int) (int64, error) {
	var before int64
	if err := s.db.QueryRowContext(ctx, `pragma freelist_count`).Scan(&before); err != nil {
		return 0, err
	}
	if before == 0 {
		return 0, nil
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`pragma incremental_vacuum(%d)`, maxPages)); err != nil {
		return 0, err
	}
	var after int64
	if err := s.db.QueryRowContext(ctx, `pragma freelist_count`).Scan(&after); err != nil {
		return 0, err
	}
	if after >= before {
		return 0, nil
	}
	return before - after, nil
}

func ServerMonitoringRetentionDays(settings map[string]string) int {
	days, err := strconv.Atoi(strings.TrimSpace(settings[ServerMonitoringRetentionDaysSetting]))
	if err != nil || days < MinServerMonitoringRetentionDays || days > MaxServerMonitoringRetentionDays {
		return DefaultServerMonitoringRetentionDays
	}
	return days
}

func (s *Store) deleteMaintenanceBatches(ctx context.Context, query string, cutoff time.Time) (int64, bool, error) {
	var deleted int64
	cutoffText := cutoff.UTC().Format(time.RFC3339Nano)
	for batch := 0; batch < maintenanceMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return deleted, true, err
		}
		res, err := s.db.ExecContext(ctx, query, cutoffText, maintenanceBatchSize)
		if err != nil {
			return deleted, deleted > 0, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return deleted, deleted > 0, err
		}
		deleted += rows
		if rows < int64(maintenanceBatchSize) {
			return deleted, false, nil
		}
		if (batch+1)%maintenanceCheckpointEveryBatches == 0 {
			if _, err := s.CheckpointWAL(ctx); err != nil {
				return deleted, true, err
			}
		}
	}
	return deleted, true, nil
}

// pruneExpiredConnectivityEvents deletes reporting events older than cutoff
// while keeping the newest row of each kind per server so SLA windows can
// still reconstruct a baseline. The previous correlated subquery scanned every
// old probe_result once per candidate row and could consume the whole
// maintenance budget on a large table.
func (s *Store) pruneExpiredConnectivityEvents(ctx context.Context, cutoff time.Time) (int64, bool, error) {
	cutoffText := cutoff.UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
		select id from (
			select id, row_number() over (partition by server_id, kind order by effective_at desc, id desc) as rn
			from server_connectivity_events
			where effective_at < ?
		) ranked
		where rn = 1`, cutoffText)
	if err != nil {
		return 0, false, err
	}
	keepIDs := make([]int64, 0, 64)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, false, err
		}
		keepIDs = append(keepIDs, id)
	}
	if err := rows.Close(); err != nil {
		return 0, false, err
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	notKeep := ""
	if len(keepIDs) > 0 {
		notKeep = "and id not in (" + formatInt64SQLList(keepIDs) + ")"
	}
	query := `delete from server_connectivity_events where rowid in (
		select rowid from server_connectivity_events
		where effective_at < ? ` + notKeep + `
		order by effective_at
		limit ?
	)`
	return s.deleteMaintenanceBatches(ctx, query, cutoff)
}

func formatInt64SQLList(ids []int64) string {
	if len(ids) == 0 {
		return "0"
	}
	var builder strings.Builder
	builder.Grow(len(ids) * 8)
	for index, id := range ids {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatInt(id, 10))
	}
	return builder.String()
}
