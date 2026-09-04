package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	maintenanceBatchSize        = 2000
	maintenanceMaxBatches       = 50
	subscriptionBucketRetention = 24 * time.Hour
	// maintenanceReclaimPages bounds one incremental vacuum pass. At the 4 KiB
	// default page size this returns up to 64 MiB per hourly run, which is far
	// above the steady-state churn of the retention deletes above.
	maintenanceReclaimPages = 16384
	// maintenanceWALTruncateFrames is the WAL size, in frames, past which the
	// passive checkpoint is assumed to be losing to readers.
	maintenanceWALTruncateFrames = 4096

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
	WALBusyFrames                 int
	WALLogFrames                  int
	WALCheckpointedFrames         int
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
			name: "connectivity probe event retention",
			query: `delete from server_connectivity_events where rowid in (
				select event.rowid from server_connectivity_events event
				where event.kind='probe_result' and event.effective_at < ?1
				and event.id <> (
					select baseline.id from server_connectivity_events baseline
					where baseline.server_id=event.server_id and baseline.kind='probe_result' and baseline.effective_at < ?1
					order by baseline.effective_at desc,baseline.id desc limit 1
				)
				order by event.effective_at limit ?2
			)`,
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
		deleted, err := s.deleteMaintenanceBatches(ctx, job.query, job.cutoff)
		*job.count = deleted
		if err != nil {
			return result, fmt.Errorf("%s: %w", job.name, err)
		}
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
	if err := s.db.QueryRowContext(ctx, `pragma wal_checkpoint(passive)`).Scan(
		&result.WALBusyFrames,
		&result.WALLogFrames,
		&result.WALCheckpointedFrames,
	); err != nil {
		return result, fmt.Errorf("passive WAL checkpoint: %w", err)
	}
	// A passive checkpoint yields to any active reader, so on a busy
	// installation the WAL can keep growing while every hourly pass reports
	// success. Once it is past the threshold, ask for a truncating checkpoint
	// so the file returns to zero. It blocks new writers only for as long as
	// the current readers take to drain, and a failure here is not an error:
	// the next pass tries again.
	if result.WALLogFrames > maintenanceWALTruncateFrames {
		var busy, log, checkpointed int
		if err := s.db.QueryRowContext(ctx, `pragma wal_checkpoint(truncate)`).Scan(&busy, &log, &checkpointed); err == nil && busy == 0 {
			result.WALBusyFrames, result.WALLogFrames, result.WALCheckpointedFrames = busy, log, checkpointed
		}
	}
	return result, nil
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

func (s *Store) deleteMaintenanceBatches(ctx context.Context, query string, cutoff time.Time) (int64, error) {
	var deleted int64
	cutoffText := cutoff.UTC().Format(time.RFC3339Nano)
	for batch := 0; batch < maintenanceMaxBatches; batch++ {
		res, err := s.db.ExecContext(ctx, query, cutoffText, maintenanceBatchSize)
		if err != nil {
			return deleted, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += rows
		if rows < maintenanceBatchSize {
			break
		}
	}
	return deleted, nil
}
