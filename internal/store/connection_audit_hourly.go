package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

const connectionAuditHourlyAlgorithm = 1

// ConnectionAuditHourKey is the canonical UTC hour bucket used by Robust-Z.
func ConnectionAuditHourKey(at time.Time) string {
	at = at.UTC()
	hour := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), 0, 0, 0, time.UTC)
	return hour.Format("2006-01-02T15:00:00Z")
}

func connectionAuditHourStart(key string) time.Time {
	return parseTime(key).UTC()
}

func (s *Store) ensureConnectionAuditHourlySchema(ctx context.Context) error {
	stmts := []string{
		`create table if not exists connection_audit_hourly (
			user_id integer not null references users(id) on delete cascade,
			utc_hour text not null,
			connection_count integer not null default 0,
			algorithm_version integer not null default 1,
			source_watermark text not null default '',
			updated_at text not null,
			primary key(user_id, utc_hour)
		)`,
		`create index if not exists idx_connection_audit_hourly_hour on connection_audit_hourly(utc_hour)`,
		`create table if not exists connection_audit_hourly_dirty (
			user_id integer not null references users(id) on delete cascade,
			utc_hour text not null,
			dirty_at text not null,
			primary key(user_id, utc_hour)
		)`,
		`create index if not exists idx_connection_audit_hourly_dirty_at on connection_audit_hourly_dirty(dirty_at)`,
		`create table if not exists audit_rollup_state (
			id integer primary key check(id=1),
			backfill_cursor text not null default '',
			backfill_complete integer not null default 0,
			read_path text not null default 'raw',
			algorithm_version integer not null default 1,
			updated_at text not null
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into audit_rollup_state(id,backfill_cursor,backfill_complete,read_path,algorithm_version,updated_at)
		values(1,'',0,'raw',?,?) on conflict(id) do nothing`, connectionAuditHourlyAlgorithm, ts)
	return err
}

// MarkConnectionAuditHoursDirty records UTC hours that must be recomputed from
// raw reports. Duplicate marks in one batch collapse on the primary key.
func (s *Store) MarkConnectionAuditHoursDirty(ctx context.Context, marks map[int64][]time.Time) error {
	if len(marks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := markConnectionAuditHoursDirtyTx(tx, ctx, marks); err != nil {
		return err
	}
	return tx.Commit()
}

func markConnectionAuditHoursDirtyTx(tx *sql.Tx, ctx context.Context, marks map[int64][]time.Time) error {
	ts := now()
	for userID, hours := range marks {
		if userID <= 0 {
			continue
		}
		seen := map[string]struct{}{}
		for _, at := range hours {
			if at.IsZero() {
				continue
			}
			key := ConnectionAuditHourKey(at)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if _, err := tx.ExecContext(ctx, `insert into connection_audit_hourly_dirty(user_id,utc_hour,dirty_at) values(?,?,?)
				on conflict(user_id,utc_hour) do update set dirty_at=excluded.dirty_at`, userID, key, ts); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListConnectionAuditHourlyDirty returns up to limit dirty buckets oldest first.
func (s *Store) ListConnectionAuditHourlyDirty(ctx context.Context, limit int) ([]ConnectionAuditHourDirty, error) {
	if limit < 1 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `select user_id,utc_hour,dirty_at from connection_audit_hourly_dirty order by dirty_at,user_id,utc_hour limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectionAuditHourDirty{}
	for rows.Next() {
		var item ConnectionAuditHourDirty
		var dirtyAt string
		if err := rows.Scan(&item.UserID, &item.UTCHour, &dirtyAt); err != nil {
			return nil, err
		}
		item.DirtyAt = parseTime(dirtyAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

type ConnectionAuditHourDirty struct {
	UserID  int64
	UTCHour string
	DirtyAt time.Time
}

// RecomputeConnectionAuditHour replaces one hourly bucket from raw reports
// under the same filters as Robust-Z, then clears the matching dirty mark only
// when no newer dirty_at arrived during the recompute.
func (s *Store) RecomputeConnectionAuditHour(ctx context.Context, userID int64, utcHour string, dirtyAt time.Time) error {
	if userID <= 0 || strings.TrimSpace(utcHour) == "" {
		return nil
	}
	start := connectionAuditHourStart(utcHour)
	end := start.Add(time.Hour)
	var total int64
	err := s.db.QueryRowContext(ctx, `select coalesce(sum(connection_count),0)
		from connection_audit_reports
		where user_id=? and started_at>=? and started_at<? and internal_probe=0
			and probe_state not in ('confirmed','candidate') and dropped_bucket_count=0`,
		userID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)).Scan(&total)
	if err != nil {
		return err
	}
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into connection_audit_hourly(user_id,utc_hour,connection_count,algorithm_version,source_watermark,updated_at)
		values(?,?,?,?,?,?)
		on conflict(user_id,utc_hour) do update set
			connection_count=excluded.connection_count,
			algorithm_version=excluded.algorithm_version,
			source_watermark=excluded.source_watermark,
			updated_at=excluded.updated_at`,
		userID, utcHour, total, connectionAuditHourlyAlgorithm, end.Format(time.RFC3339Nano), ts); err != nil {
		return err
	}
	// Only clear the dirty mark if it was not refreshed while we recomputed.
	if dirtyAt.IsZero() {
		if _, err := tx.ExecContext(ctx, `delete from connection_audit_hourly_dirty where user_id=? and utc_hour=?`, userID, utcHour); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `delete from connection_audit_hourly_dirty where user_id=? and utc_hour=? and dirty_at<=?`,
			userID, utcHour, dirtyAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DrainConnectionAuditHourlyDirty recomputes up to limit dirty buckets.
func (s *Store) DrainConnectionAuditHourlyDirty(ctx context.Context, limit int) (int, error) {
	items, err := s.ListConnectionAuditHourlyDirty(ctx, limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		if err := s.RecomputeConnectionAuditHour(ctx, item.UserID, item.UTCHour, item.DirtyAt); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// BackfillConnectionAuditHourlyPages fills hourly buckets from raw reports in
// stable cursor pages (user_id, utc_hour), never OFFSET.
func (s *Store) BackfillConnectionAuditHourlyPages(ctx context.Context, pageSize int) (int, bool, error) {
	if pageSize < 1 {
		pageSize = 200
	}
	state, err := s.GetAuditRollupState(ctx)
	if err != nil {
		return 0, false, err
	}
	if state.BackfillComplete {
		return 0, true, nil
	}
	cutoff := time.Now().UTC().Add(-28 * 24 * time.Hour)
	cursorUser := int64(0)
	cursorHour := ""
	if parts := strings.SplitN(state.BackfillCursor, "\x00", 2); len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &cursorUser)
		cursorHour = parts[1]
	}
	rows, err := s.db.QueryContext(ctx, `select user_id,strftime('%Y-%m-%dT%H:00:00Z',started_at) as hour_bucket,coalesce(sum(connection_count),0)
		from connection_audit_reports
		where started_at>=? and internal_probe=0 and probe_state not in ('confirmed','candidate') and dropped_bucket_count=0
			and (user_id>? or (user_id=? and strftime('%Y-%m-%dT%H:00:00Z',started_at)>?))
		group by user_id,hour_bucket
		order by user_id,hour_bucket
		limit ?`,
		cutoff.Format(time.RFC3339Nano), cursorUser, cursorUser, cursorHour, pageSize)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	type row struct {
		userID int64
		hour   string
		total  int64
	}
	batch := []row{}
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.userID, &item.hour, &item.total); err != nil {
			return 0, false, err
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(batch) == 0 {
		if err := s.SetAuditRollupState(ctx, AuditRollupState{
			BackfillCursor:   state.BackfillCursor,
			BackfillComplete: true,
			ReadPath:         "hourly",
			AlgorithmVersion: connectionAuditHourlyAlgorithm,
		}); err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	for _, item := range batch {
		end := connectionAuditHourStart(item.hour).Add(time.Hour)
		if _, err := tx.ExecContext(ctx, `insert into connection_audit_hourly(user_id,utc_hour,connection_count,algorithm_version,source_watermark,updated_at)
			values(?,?,?,?,?,?)
			on conflict(user_id,utc_hour) do update set
				connection_count=excluded.connection_count,
				algorithm_version=excluded.algorithm_version,
				source_watermark=excluded.source_watermark,
				updated_at=excluded.updated_at`,
			item.userID, item.hour, item.total, connectionAuditHourlyAlgorithm, end.Format(time.RFC3339Nano), ts); err != nil {
			return 0, false, err
		}
	}
	last := batch[len(batch)-1]
	cursor := fmt.Sprintf("%d\x00%s", last.userID, last.hour)
	complete := len(batch) < pageSize
	readPath := "raw"
	if complete {
		readPath = "hourly"
	}
	if _, err := tx.ExecContext(ctx, `update audit_rollup_state set backfill_cursor=?,backfill_complete=?,read_path=?,algorithm_version=?,updated_at=? where id=1`,
		cursor, boolInt(complete), readPath, connectionAuditHourlyAlgorithm, ts); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return len(batch), complete, nil
}

type AuditRollupState struct {
	BackfillCursor   string
	BackfillComplete bool
	ReadPath         string
	AlgorithmVersion int
	UpdatedAt        time.Time
}

func (s *Store) GetAuditRollupState(ctx context.Context) (AuditRollupState, error) {
	var state AuditRollupState
	var updated string
	err := s.db.QueryRowContext(ctx, `select backfill_cursor,backfill_complete,read_path,algorithm_version,updated_at from audit_rollup_state where id=1`).
		Scan(&state.BackfillCursor, &state.BackfillComplete, &state.ReadPath, &state.AlgorithmVersion, &updated)
	if err != nil {
		return state, err
	}
	state.UpdatedAt = parseTime(updated)
	return state, nil
}

func (s *Store) SetAuditRollupState(ctx context.Context, state AuditRollupState) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `update audit_rollup_state set backfill_cursor=?,backfill_complete=?,read_path=?,algorithm_version=?,updated_at=? where id=1`,
		state.BackfillCursor, boolInt(state.BackfillComplete), state.ReadPath, state.AlgorithmVersion, ts)
	return err
}

func (s *Store) loadConnectionAuditHourlyBuckets(ctx context.Context, userIDs []int64, since, until time.Time) (map[int64][]auditHourBucket, error) {
	if len(userIDs) == 0 {
		return map[int64][]auditHourBucket{}, nil
	}
	args := make([]any, 0, 3+len(userIDs))
	args = append(args, since.UTC().Format("2006-01-02T15:00:00Z"), until.UTC().Format("2006-01-02T15:00:00Z"), connectionAuditHourlyAlgorithm)
	for _, id := range userIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `select user_id,utc_hour,connection_count
		from connection_audit_hourly
		where utc_hour>=? and utc_hour<? and algorithm_version=? and user_id in (`+inClause(len(userIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]auditHourBucket{}
	for rows.Next() {
		var userID, count int64
		var hour string
		if err := rows.Scan(&userID, &hour, &count); err != nil {
			return nil, err
		}
		out[userID] = append(out[userID], auditHourBucket{at: parseTime(hour), value: float64(count)})
	}
	return out, rows.Err()
}

func (s *Store) connectionAuditHourlyDirtyUsers(ctx context.Context, userIDs []int64, since, until time.Time) (map[int64]struct{}, error) {
	out := map[int64]struct{}{}
	if len(userIDs) == 0 {
		return out, nil
	}
	args := []any{since.UTC().Format("2006-01-02T15:00:00Z"), until.UTC().Format("2006-01-02T15:00:00Z")}
	for _, id := range userIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `select distinct user_id from connection_audit_hourly_dirty
		where utc_hour>=? and utc_hour<? and user_id in (`+inClause(len(userIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		out[userID] = struct{}{}
	}
	return out, rows.Err()
}

// PurgeConnectionAuditHourlyBefore removes hourly rows older than the raw
// retention window. It never shortens raw report retention.
func (s *Store) PurgeConnectionAuditHourlyBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	key := ConnectionAuditHourKey(cutoff)
	res, err := s.db.ExecContext(ctx, `delete from connection_audit_hourly where utc_hour<?`, key)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `delete from connection_audit_hourly_dirty where utc_hour<?`, key); err != nil {
		return n, err
	}
	return n, nil
}

func sortAuditHourBuckets(buckets []auditHourBucket) {
	sort.SliceStable(buckets, func(i, j int) bool { return buckets[i].at.Before(buckets[j].at) })
}
