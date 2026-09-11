package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrLatencyRollupChanged = errors.New("latency projection changed; retry batch")

const (
	LatencyRollupBatchLimit = 500
	latencyRollupWriteLimit = 1000
)

type LatencyRollupState struct {
	SchemaVersion                                              int
	Generation, LiveCursor, HistoricalBoundary, BackfillCursor int64
	ExpiredUnprocessed, RetentionFloor                         int64
}

type LatencyRollupResult struct {
	Processed, Expired, Buckets   int
	Cursor                        int64
	PendingIDSpan                 int64
	Duration, TransactionDuration time.Duration
}

type latencyRollupKey struct {
	serverID          int64
	series, revision  string
	resolution, start int64
}

type latencyRollupDelta struct {
	basis                                                                      string
	mode, taskName, province, carrier                                          string
	key                                                                        latencyRollupKey
	kind, probeID                                                              string
	taskID                                                                     int64
	sum, count, curveSum, curveCount, attempts, successes, reports, available  int64
	minimum, maximum, minAt, maxAt, curveMin, curveMax, curveMinAt, curveMaxAt sql.NullInt64
	first, last                                                                int64
}

type latencyRollupBatch struct {
	historical         bool
	state              LatencyRollupState
	deltas             map[latencyRollupKey]*latencyRollupDelta
	through            int64
	processed, expired int
	cutoff             int64
	retentionDays      int
}

func (s *Store) LatencyRollupState(ctx context.Context) (LatencyRollupState, error) {
	var state LatencyRollupState
	err := s.db.QueryRowContext(ctx, `select schema_version,generation,live_cursor,historical_boundary,backfill_cursor,expired_unprocessed,retention_floor from latency_rollup_state where id=1`).Scan(&state.SchemaVersion, &state.Generation, &state.LiveCursor, &state.HistoricalBoundary, &state.BackfillCursor, &state.ExpiredUnprocessed, &state.RetentionFloor)
	return state, err
}

// Initialize only records the ID boundary. Historical rows are neither scanned
// nor replayed on startup; backfill has a separate, descending cursor.
func (s *Store) initializeLatencyRollup(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var boundary int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(id),0) from server_latency_probe_results`).Scan(&boundary); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `update latency_rollup_state set live_cursor=?,historical_boundary=?,backfill_cursor=?,initialized_at=?,updated_at=? where id=1 and live_cursor=-1 and schema_version=?`, boundary, boundary, boundary, now(), now(), latencyRollupSchemaVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RunLatencyRollupBatch(ctx context.Context, at time.Time, limit int) (LatencyRollupResult, error) {
	return s.runLatencyRollupLane(ctx, at, limit, false)
}
func (s *Store) RunLatencyBackfillBatch(ctx context.Context, at time.Time, limit int) (LatencyRollupResult, error) {
	return s.runLatencyRollupLane(ctx, at, limit, true)
}
func (s *Store) runLatencyRollupLane(ctx context.Context, at time.Time, limit int, historical bool) (result LatencyRollupResult, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	if !s.latencyRollupBusy.CompareAndSwap(false, true) {
		return result, ErrLatencyRollupChanged
	}
	defer s.latencyRollupBusy.Store(false)
	if limit < 1 || limit > LatencyRollupBatchLimit {
		return result, errors.New("invalid latency rollup batch limit")
	}
	state, err := s.LatencyRollupState(ctx)
	if err != nil {
		return result, err
	}
	if state.SchemaVersion != latencyRollupSchemaVersion {
		return result, errors.New("unsupported latency rollup schema")
	}
	if state.LiveCursor < 0 {
		err = s.initializeLatencyRollup(ctx)
		return result, err
	}
	settings, err := s.ListSettings(ctx)
	if err != nil {
		return result, err
	}
	cutoff := at.UTC().Add(-time.Duration(ServerMonitoringRetentionDays(settings)) * 24 * time.Hour)
	if floor := time.Unix(state.RetentionFloor, 0); floor.After(cutoff) {
		cutoff = floor
	}
	if historical && state.BackfillCursor <= 0 {
		return result, nil
	}
	batch, err := s.readLatencyRollupBatchLane(ctx, state, cutoff, limit, historical)
	if err != nil {
		return result, err
	}
	batch.retentionDays = ServerMonitoringRetentionDays(settings)
	if batch.processed > 0 || (historical && batch.through != state.BackfillCursor) {
		txStarted := time.Now()
		err = s.commitLatencyRollupBatch(ctx, batch)
		result.TransactionDuration = time.Since(txStarted)
		if err != nil {
			return result, err
		}
	}
	result.Processed, result.Expired, result.Buckets, result.Cursor = batch.processed, batch.expired, len(batch.deltas), batch.through
	var newest int64
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(id),0) from server_latency_probe_results`).Scan(&newest); err != nil {
		return result, err
	}
	result.PendingIDSpan = max(0, newest-result.Cursor)
	return result, nil
}

func (s *Store) readLatencyRollupBatch(ctx context.Context, state LatencyRollupState, cutoff time.Time, limit int) (latencyRollupBatch, error) {
	return s.readLatencyRollupBatchLane(ctx, state, cutoff, limit, false)
}
func (s *Store) readLatencyRollupBatchLane(ctx context.Context, state LatencyRollupState, cutoff time.Time, limit int, historical bool) (latencyRollupBatch, error) {
	floor := cutoff.Unix()
	if cutoff.Nanosecond() > 0 {
		floor++
	}
	batch := latencyRollupBatch{state: state, through: state.LiveCursor, cutoff: floor, deltas: make(map[latencyRollupKey]*latencyRollupDelta)}
	batch.historical = historical
	query := `select id,server_id,kind,task_id,probe_id,mode,host,port,task_name,province,carrier,available,latency_ms,sample_count,success_count,checked_at,measurement_revision from server_latency_probe_results where id>? order by id limit ?`
	args := []any{state.LiveCursor, limit}
	if historical {
		batch.through = state.BackfillCursor
		query = `select id,server_id,kind,task_id,probe_id,mode,host,port,task_name,province,carrier,available,latency_ms,sample_count,success_count,checked_at,measurement_revision from server_latency_probe_results where id<=? and id<=? order by id desc limit ?`
		args = []any{state.BackfillCursor, state.HistoricalBoundary, limit}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return batch, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return batch, err
		}
		var id, serverID, taskID, port, available, latency, attempts, successes int64
		var kind, probeID, mode, host, checked, taskName, province, carrier, measurement string
		if err := rows.Scan(&id, &serverID, &kind, &taskID, &probeID, &mode, &host, &port, &taskName, &province, &carrier, &available, &latency, &attempts, &successes, &checked, &measurement); err != nil {
			return batch, err
		}
		at, err := time.Parse(time.RFC3339Nano, checked)
		if err != nil {
			return batch, fmt.Errorf("invalid persisted latency timestamp at id %d", id)
		}
		expired := at.Before(cutoff)
		if !expired && (kind == "public" || kind == "regional" || kind == "custom") {
			series, _ := json.Marshal([]any{kind, taskID, probeID})
			// Legacy reports retain an explicitly incomplete revision basis.
			semantics, _ := json.Marshal([]any{"reported_endpoint_v1", mode, strings.TrimSpace(host), port})
			revision := fmt.Sprintf("%x", sha256.Sum256(semantics))
			basis := "reported_endpoint_v1"
			if measurement != "" {
				revision = measurement
				basis = "measurement_v1"
			}
			keys := [2]latencyRollupKey{}
			additional := 0
			for i, resolution := range []int64{300, 3600} {
				keys[i] = latencyRollupKey{serverID: serverID, series: string(series), revision: revision, resolution: resolution, start: at.UTC().Truncate(time.Duration(resolution) * time.Second).Unix()}
				if keys[i].start >= batch.cutoff && batch.deltas[keys[i]] == nil {
					additional++
				}
			}
			if len(batch.deltas)+additional > latencyRollupWriteLimit {
				break
			}
			for _, key := range keys {
				if key.start < batch.cutoff {
					continue
				}
				delta := batch.deltas[key]
				if delta == nil {
					delta = &latencyRollupDelta{basis: basis, key: key, kind: kind, taskID: taskID, probeID: probeID, first: at.UnixNano(), last: at.UnixNano()}
					batch.deltas[key] = delta
				}
				delta.mode = max(delta.mode, mode)
				delta.taskName = max(delta.taskName, taskName)
				delta.province = max(delta.province, province)
				delta.carrier = max(delta.carrier, carrier)
				delta.reports++
				delta.attempts += attempts
				delta.successes += successes
				delta.available += available
				delta.first = min(delta.first, at.UnixNano())
				delta.last = max(delta.last, at.UnixNano())
				if available == 1 && latency > 0 {
					delta.curveSum += latency
					delta.curveCount++
					addLatencyExtrema(&delta.curveMin, &delta.curveMax, &delta.curveMinAt, &delta.curveMaxAt, latency, at.UnixNano())
					if successes > 0 {
						delta.sum += latency
						delta.count++
						addLatencyExtrema(&delta.minimum, &delta.maximum, &delta.minAt, &delta.maxAt, latency, at.UnixNano())
					}
				}
			}
		}
		batch.through = id
		if historical {
			batch.through = id - 1
		}
		batch.processed++
		if expired {
			batch.expired++
		}
	}
	if historical && batch.processed == 0 {
		batch.through = 0
	}
	return batch, rows.Err()
}

func addLatencyExtrema(minimum, maximum, minAt, maxAt *sql.NullInt64, value, at int64) {
	if !minimum.Valid || value < minimum.Int64 || (value == minimum.Int64 && at < minAt.Int64) {
		*minimum = sql.NullInt64{Int64: value, Valid: true}
		*minAt = sql.NullInt64{Int64: at, Valid: true}
	}
	if !maximum.Valid || value > maximum.Int64 || (value == maximum.Int64 && at < maxAt.Int64) {
		*maximum = sql.NullInt64{Int64: value, Valid: true}
		*maxAt = sql.NullInt64{Int64: at, Valid: true}
	}
}

var latencyRollupUpsert = buildLatencyRollupUpsert()

func buildLatencyRollupUpsert() string {
	columns := []string{"server_id", "series_key", "measurement_revision", "resolution_seconds", "bucket_start", "kind", "task_id", "probe_id", "revision_basis", "mode", "task_name", "province", "carrier", "latency_sum", "latency_value_count", "latency_min", "latency_max", "latency_min_at", "latency_max_at", "curve_sum", "curve_value_count", "curve_min", "curve_max", "curve_min_at", "curve_max_at", "attempt_count", "success_count", "report_count", "available_report_count", "first_sample_at", "last_sample_at", "summary_schema_version", "updated_at"}
	updates := []string{"mode=max(mode,excluded.mode)", "task_name=max(task_name,excluded.task_name)", "province=max(province,excluded.province)", "carrier=max(carrier,excluded.carrier)", "updated_at=excluded.updated_at", "first_sample_at=min(first_sample_at,excluded.first_sample_at)", "last_sample_at=max(last_sample_at,excluded.last_sample_at)"}
	for _, column := range []string{"latency_sum", "latency_value_count", "curve_sum", "curve_value_count", "attempt_count", "success_count", "report_count", "available_report_count"} {
		updates = append(updates, column+"="+column+"+excluded."+column)
	}
	for _, prefix := range []string{"latency", "curve"} {
		for _, extreme := range []string{"min", "max"} {
			field := prefix + "_" + extreme
			comparison := "<"
			if extreme == "max" {
				comparison = ">"
			}
			replace := fmt.Sprintf("excluded.%s is not null and (%s is null or excluded.%s%s%s or (excluded.%s=%s and excluded.%s_at<%s_at))", field, field, field, comparison, field, field, field, field, field)
			updates = append(updates, fmt.Sprintf("%s_at=case when %s then excluded.%s_at else %s_at end", field, replace, field, field), fmt.Sprintf("%s=case when %s then excluded.%s else %s end", field, replace, field, field))
		}
	}
	return "insert into latency_rollup_buckets(" + strings.Join(columns, ",") + ") values(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") on conflict(server_id,series_key,measurement_revision,resolution_seconds,bucket_start) do update set " + strings.Join(updates, ",") + " where summary_schema_version=excluded.summary_schema_version"
}

func (s *Store) commitLatencyRollupBatch(ctx context.Context, batch latencyRollupBatch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if batch.retentionDays > 0 {
		var value string
		err := tx.QueryRowContext(ctx, `select value from app_settings where key=?`, ServerMonitoringRetentionDaysSetting).Scan(&value)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if ServerMonitoringRetentionDays(map[string]string{ServerMonitoringRetentionDaysSetting: value}) != batch.retentionDays {
			return ErrLatencyRollupChanged
		}
	}
	cursorColumn := "live_cursor"
	previous := batch.state.LiveCursor
	if batch.historical {
		cursorColumn = "backfill_cursor"
		previous = batch.state.BackfillCursor
	}
	result, err := tx.ExecContext(ctx, `update latency_rollup_state set `+cursorColumn+`=?,expired_unprocessed=expired_unprocessed+?,retention_floor=max(retention_floor,?),updated_at=? where id=1 and `+cursorColumn+`=? and generation=? and schema_version=?`, batch.through, batch.expired, batch.cutoff, now(), previous, batch.state.Generation, latencyRollupSchemaVersion)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrLatencyRollupChanged
	}
	stmt, err := tx.PrepareContext(ctx, latencyRollupUpsert)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range batch.deltas {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := stmt.ExecContext(ctx, d.key.serverID, d.key.series, d.key.revision, d.key.resolution, d.key.start, d.kind, d.taskID, d.probeID, d.basis, d.mode, d.taskName, d.province, d.carrier, d.sum, d.count, d.minimum, d.maximum, d.minAt, d.maxAt, d.curveSum, d.curveCount, d.curveMin, d.curveMax, d.curveMinAt, d.curveMaxAt, d.attempts, d.successes, d.reports, d.available, d.first, d.last, latencyRollupSchemaVersion, now())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("incompatible latency bucket schema")
		}
	}
	return tx.Commit()
}

func bumpLatencyRollupGeneration(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `update latency_rollup_state set generation=generation+1,updated_at=? where id=1`, now())
	return err
}
