package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DatabaseLastMaintenanceAtSetting      = "database_last_maintenance_at"
	DatabaseLastMaintenanceSummarySetting = "database_last_maintenance_summary"
	// ConnectionAuditRetentionHours is the raw connection-audit retention
	// window in hours. Do not shorten this while hourly rollups are unverified.
	ConnectionAuditRetentionHours = 30 * 24
)

// StorageDiagnostics is a lightweight snapshot for the settings UI. It only
// stats database files and reads small control rows — never a heavy ANALYZE or
// full-table scan on every refresh.
type StorageDiagnostics struct {
	DBBytes                         int64           `json:"db_bytes"`
	WALBytes                        int64           `json:"wal_bytes"`
	SHMBytes                        int64           `json:"shm_bytes"`
	PageCount                       int64           `json:"page_count,omitempty"`
	PageSize                        int64           `json:"page_size,omitempty"`
	FreelistCount                   int64           `json:"freelist_count,omitempty"`
	MaintenanceHint                 string          `json:"maintenance_hint,omitempty"`
	AuditRollupState                map[string]any  `json:"audit_rollup_state"`
	DirtyHourlyCount                int64           `json:"dirty_hourly_count"`
	ConnectionAuditRetentionHours   int             `json:"connection_audit_retention_hours"`
	SubscriptionAuditRetentionHours int             `json:"subscription_audit_retention_hours"`
	ServerMonitoringRetentionDays   int             `json:"server_monitoring_retention_days"`
	LastMaintenanceAt               string          `json:"last_maintenance_at,omitempty"`
	LastMaintenanceSummary          map[string]any  `json:"last_maintenance_summary,omitempty"`
	AnalyzedAt                      time.Time       `json:"analyzed_at"`
}

// GetStorageDiagnostics returns recent storage analysis without heavy scans.
func (s *Store) GetStorageDiagnostics(ctx context.Context) (StorageDiagnostics, error) {
	out := StorageDiagnostics{
		ConnectionAuditRetentionHours:   ConnectionAuditRetentionHours,
		SubscriptionAuditRetentionHours: ConnectionAuditRetentionHours,
		AuditRollupState: map[string]any{
			"backfill_cursor":   "",
			"backfill_complete": false,
			"read_path":         "raw",
			"algorithm_version": 1,
			"updated_at":        "",
		},
		AnalyzedAt: time.Now().UTC(),
	}
	settings, err := s.ListSettings(ctx)
	if err != nil {
		return out, err
	}
	out.ServerMonitoringRetentionDays = ServerMonitoringRetentionDays(settings)
	out.LastMaintenanceAt = strings.TrimSpace(settings[DatabaseLastMaintenanceAtSetting])
	if raw := strings.TrimSpace(settings[DatabaseLastMaintenanceSummarySetting]); raw != "" {
		var summary map[string]any
		if json.Unmarshal([]byte(raw), &summary) == nil {
			out.LastMaintenanceSummary = summary
		}
	}

	dbPath := sqliteFilesystemPath(s.path)
	if dbPath != "" && !sqliteMemoryDatabase(s.path) {
		out.DBBytes = fileSizeOrZero(dbPath)
		out.WALBytes = fileSizeOrZero(dbPath + "-wal")
		out.SHMBytes = fileSizeOrZero(dbPath + "-shm")
	}

	if pageCount, pageSize, freelist, err := s.DatabasePageStats(ctx); err == nil {
		out.PageCount = pageCount
		out.PageSize = pageSize
		out.FreelistCount = freelist
		dbBytes := pageCount * pageSize
		if pageCount > 0 && dbBytes > 512<<20 && float64(freelist)/float64(pageCount) > 0.25 {
			out.MaintenanceHint = "建议进行数据库维护"
		}
	}

	if state, ok, err := s.readAuditRollupStateForDiagnostics(ctx); err != nil {
		return out, err
	} else if ok {
		out.AuditRollupState = state
	}

	if count, err := s.countConnectionAuditHourlyDirty(ctx); err != nil {
		return out, err
	} else {
		out.DirtyHourlyCount = count
	}
	return out, nil
}

func (s *Store) readAuditRollupStateForDiagnostics(ctx context.Context) (map[string]any, bool, error) {
	var cursor, readPath, updated string
	var complete, algorithm int
	err := s.db.QueryRowContext(ctx, `select backfill_cursor,backfill_complete,read_path,algorithm_version,updated_at from audit_rollup_state where id=1`).
		Scan(&cursor, &complete, &readPath, &algorithm, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		// Table may not exist yet on older installations mid-upgrade.
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil, false, nil
		}
		return nil, false, err
	}
	return map[string]any{
		"backfill_cursor":   cursor,
		"backfill_complete": complete != 0,
		"read_path":         readPath,
		"algorithm_version": algorithm,
		"updated_at":        updated,
	}, true, nil
}

func (s *Store) countConnectionAuditHourlyDirty(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `select count(*) from connection_audit_hourly_dirty`).Scan(&count)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return count, nil
}

func (s *Store) recordMaintenanceResult(ctx context.Context, at time.Time, result MaintenanceResult) error {
	summary, err := json.Marshal(map[string]any{
		"connection_audits_deleted":   result.ConnectionAuditsDeleted,
		"subscription_audits_deleted": result.SubscriptionAuditsDeleted,
		"probe_episodes_deleted":      result.ProbeEpisodesDeleted,
		"rate_buckets_deleted":        result.RateBucketsDeleted,
		"server_metrics_deleted":      result.ServerMetricSamplesDeleted,
		"pages_reclaimed":             result.FreePagesReclaimed,
		"wal_busy":                    result.WALBusyFrames,
		"wal_log":                     result.WALLogFrames,
		"wal_checkpointed":            result.WALCheckpointedFrames,
		"needs_catch_up":              result.NeedsCatchUp,
	})
	if err != nil {
		return err
	}
	return s.SetSettings(ctx, map[string]string{
		DatabaseLastMaintenanceAtSetting:      at.UTC().Format(time.RFC3339Nano),
		DatabaseLastMaintenanceSummarySetting: string(summary),
	})
}

func sqliteFilesystemPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || sqliteMemoryDatabase(path) {
		return ""
	}
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
	}
	if queryAt := strings.IndexByte(path, '?'); queryAt >= 0 {
		path = path[:queryAt]
	}
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" {
		return ""
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
	}
	return path
}

func fileSizeOrZero(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}
