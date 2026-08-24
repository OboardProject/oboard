package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// SaveMetricReport records an Agent-captured historical sample without changing
// live server state, traffic baselines, last-seen timestamps, or connectivity.
// A report is considered accepted when its fixed UTC-minute slot is inserted or
// already contains an equivalent live or replayed sample.
func (s *Store) SaveMetricReport(ctx context.Context, serverID int64, report model.MetricReport) (bool, error) {
	if serverID <= 0 {
		return false, errors.New("metric report requires a server id")
	}
	slotStart := report.SampledAt.UTC().Truncate(time.Minute)
	slotEnd := slotStart.Add(time.Minute)
	result, err := s.db.ExecContext(ctx, `insert into server_metric_samples(server_id,cpu_usage_percent,memory_used_bytes,memory_total_bytes,disk_used_bytes,disk_total_bytes,tcp_connection_count,udp_connection_count,process_count,resource_recorded,network_upload_bps,network_download_bps,traffic_upload_bytes,traffic_download_bytes,connectivity_available,connectivity_latency_ms,sampled_at)
select ?,
 case when t.resource_history_enabled=1 then ? else 0 end,
 case when t.resource_history_enabled=1 then ? else 0 end,
 case when t.resource_history_enabled=1 then ? else 0 end,
 case when t.resource_history_enabled=1 then ? else 0 end,
 case when t.resource_history_enabled=1 then ? else 0 end,
 case when t.resource_history_enabled=1 then ? else 0 end,
 case when t.resource_history_enabled=1 then ? else 0 end,
 case when t.resource_history_enabled=1 then ? else 0 end,
 t.resource_history_enabled,?,?,0,0,-1,0,?
from server_telemetry t
where t.server_id=? and not exists(select 1 from server_metric_samples where server_id=? and sampled_at>=? and sampled_at<?)`,
		serverID, report.CPUUsagePercent, report.MemoryUsedBytes, report.MemoryTotalBytes, report.DiskUsedBytes, report.DiskTotalBytes, report.TCPConnectionCount, report.UDPConnectionCount, report.ProcessCount, report.NetworkUploadBPS, report.NetworkDownloadBPS, report.SampledAt.UTC().Format(time.RFC3339Nano), serverID, serverID, slotStart.Format(time.RFC3339Nano), slotEnd.Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 1 {
		return true, nil
	}
	var equivalent int
	if err := s.db.QueryRowContext(ctx, `select exists(select 1 from server_metric_samples where server_id=? and sampled_at>=? and sampled_at<?)`, serverID, slotStart.Format(time.RFC3339Nano), slotEnd.Format(time.RFC3339Nano)).Scan(&equivalent); err != nil {
		return false, err
	}
	if equivalent != 1 {
		return false, sql.ErrNoRows
	}
	return false, nil
}
