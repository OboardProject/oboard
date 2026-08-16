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

	ServerMonitoringRetentionDaysSetting = "server_monitoring_retention_days"
	DefaultServerMonitoringRetentionDays = 7
	MinServerMonitoringRetentionDays     = 1
	MaxServerMonitoringRetentionDays     = 30
)

type MaintenanceResult struct {
	ConnectionAuditsDeleted    int64
	SubscriptionAuditsDeleted  int64
	ProbeEpisodesDeleted       int64
	RateBucketsDeleted         int64
	ServerMetricSamplesDeleted int64
	LatencyProbeResultsDeleted int64
	ConnectivityProbesDeleted  int64
	WALBusyFrames              int
	WALLogFrames               int
	WALCheckpointedFrames      int
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
	if err := s.db.QueryRowContext(ctx, `pragma wal_checkpoint(passive)`).Scan(
		&result.WALBusyFrames,
		&result.WALLogFrames,
		&result.WALCheckpointedFrames,
	); err != nil {
		return result, fmt.Errorf("passive WAL checkpoint: %w", err)
	}
	return result, nil
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
