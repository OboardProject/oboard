package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const connectionAuditHourlyAlgorithm = 1

// connectionAuditHourlyTotalsVersion is the shape of the extended totals
// columns. An hourly row carrying this value has every column the window totals
// read; a row below it predates them and is not counted.
const connectionAuditHourlyTotalsVersion = 1

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
		// The distinct values behind count(distinct ...) for a window. Counts
		// of distinct things cannot be merged from per-hour counts, so the
		// values themselves are kept - a handful per user-hour against the
		// thousands of raw rows they summarise.
		`create table if not exists connection_audit_hourly_dimensions (
			user_id integer not null references users(id) on delete cascade,
			utc_hour text not null,
			dimension text not null,
			value text not null,
			primary key(user_id, utc_hour, dimension, value)
		)`,
		`create index if not exists idx_connection_audit_hourly_dimensions_window on connection_audit_hourly_dimensions(user_id, dimension, utc_hour)`,
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
	// Columns the console's window totals are served from once the window
	// reaches past raw retention. Every one of these merges exactly across
	// hours, so a total read from here equals the same total computed from the
	// raw reports it was built from.
	//
	// dimensions_complete is 0 when an hour had more distinct values than
	// connectionAuditHourlyDimensionCap, so a count derived from that hour is a
	// lower bound rather than an exact number and is reported as one.
	for _, column := range []struct {
		name string
		sql  string
	}{
		{"report_count", `alter table connection_audit_hourly add column report_count integer not null default 0`},
		{"active_peak", `alter table connection_audit_hourly add column active_peak integer not null default 0`},
		{"upload_bytes", `alter table connection_audit_hourly add column upload_bytes integer not null default 0`},
		{"download_bytes", `alter table connection_audit_hourly add column download_bytes integer not null default 0`},
		{"last_ended_at", `alter table connection_audit_hourly add column last_ended_at text not null default ''`},
		{"collection_count", `alter table connection_audit_hourly add column collection_count integer not null default 0`},
		{"dropped_bucket_count", `alter table connection_audit_hourly add column dropped_bucket_count integer not null default 0`},
		{"capacity_total", `alter table connection_audit_hourly add column capacity_total integer not null default 0`},
		{"dimensions_complete", `alter table connection_audit_hourly add column dimensions_complete integer not null default 1`},
		// totals_version marks an hour whose extended columns were actually
		// computed. Rows written before those columns existed keep 0 and are
		// excluded from window totals, because their zeros are "not measured",
		// not "nothing happened" - reading them as totals would make a month
		// look empty.
		//
		// This is deliberately separate from algorithm_version, which the
		// 28-day robust-Z baseline filters on: bumping that would have hidden
		// every legacy hour from the baseline as well.
		{"totals_version", `alter table connection_audit_hourly add column totals_version integer not null default 0`},
	} {
		if err := s.ensureColumn(ctx, "connection_audit_hourly", column.name, column.sql); err != nil {
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
// connectionAuditHourlyDimensionCap bounds how many distinct values one hour
// keeps per dimension. A user-hour realistically has a handful; the cap exists
// so a pathological hour cannot turn the rollup into a second copy of the raw
// table. When it is hit the hour is marked incomplete and its counts are
// reported as lower bounds.
const connectionAuditHourlyDimensionCap = 256

// connectionAuditHourlyDimensions are the count(distinct ...) dimensions the
// console shows over a window. Counts of distinct things do not merge from
// per-hour counts, so the values are kept and re-deduplicated across the
// window.
const (
	auditDimensionSourceIP = "source_ip"
	auditDimensionServer   = "server"
	auditDimensionCountry  = "country"
)

// RecomputeConnectionAuditHour replaces one hourly bucket from raw reports
// under the same filters as Robust-Z, then clears the matching dirty mark only
// when no newer dirty_at arrived during the recompute.
//
// The bucket carries everything the console's window totals need. Each field is
// chosen so that merging hours reproduces the same number the raw reports would
// have given: sums add, peaks take a maximum, last-seen takes a maximum, and
// the distinct dimensions are re-deduplicated from their stored values rather
// than added up.
func (s *Store) RecomputeConnectionAuditHour(ctx context.Context, userID int64, utcHour string, dirtyAt time.Time) error {
	if userID <= 0 || strings.TrimSpace(utcHour) == "" {
		return nil
	}
	start := connectionAuditHourStart(utcHour)
	end := start.Add(time.Hour)
	var bucket connectionAuditHourAggregate
	// ended_at bounds the scan; started_at remains the exact filter. A report
	// always satisfies started_at <= ended_at, so this cannot drop a row the
	// bucket wants. Without it the plan had no time bound at all and summing
	// one hour scanned every report the user had ever filed.
	err := s.db.QueryRowContext(ctx, `select
			coalesce(sum(connection_count),0),
			count(*),
			coalesce(max(active_peak),0),
			coalesce(sum(case when upload_bytes+download_bytes>0 and payload_first_at is not null then upload_bytes else 0 end),0),
			coalesce(sum(case when upload_bytes+download_bytes>0 and payload_first_at is not null then download_bytes else 0 end),0),
			coalesce(max(ended_at),''),
			count(distinct server_id||char(0)||collection_generation||char(0)||collection_ended_at),
			coalesce(sum(dropped_bucket_count),0),
			coalesce(sum(max(bucket_capacity,1)),0)
		from connection_audit_reports
		where user_id=? and ended_at>=? and started_at>=? and started_at<? and internal_probe=0
			and probe_state not in ('confirmed','candidate') and dropped_bucket_count=0`,
		userID, start.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)).
		Scan(&bucket.ConnectionCount, &bucket.ReportCount, &bucket.ActivePeak, &bucket.UploadBytes, &bucket.DownloadBytes,
			&bucket.LastEndedAt, &bucket.CollectionCount, &bucket.DroppedBucketCount, &bucket.CapacityTotal)
	if err != nil {
		return err
	}
	dimensions, complete, err := s.connectionAuditHourDimensions(ctx, userID, start, end)
	if err != nil {
		return err
	}

	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into connection_audit_hourly(user_id,utc_hour,connection_count,report_count,active_peak,
			upload_bytes,download_bytes,last_ended_at,collection_count,dropped_bucket_count,capacity_total,dimensions_complete,
			totals_version,algorithm_version,source_watermark,updated_at)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		on conflict(user_id,utc_hour) do update set
			connection_count=excluded.connection_count,
			report_count=excluded.report_count,
			active_peak=excluded.active_peak,
			upload_bytes=excluded.upload_bytes,
			download_bytes=excluded.download_bytes,
			last_ended_at=excluded.last_ended_at,
			collection_count=excluded.collection_count,
			dropped_bucket_count=excluded.dropped_bucket_count,
			capacity_total=excluded.capacity_total,
			dimensions_complete=excluded.dimensions_complete,
			totals_version=excluded.totals_version,
			algorithm_version=excluded.algorithm_version,
			source_watermark=excluded.source_watermark,
			updated_at=excluded.updated_at`,
		userID, utcHour, bucket.ConnectionCount, bucket.ReportCount, bucket.ActivePeak,
		bucket.UploadBytes, bucket.DownloadBytes, bucket.LastEndedAt, bucket.CollectionCount,
		bucket.DroppedBucketCount, bucket.CapacityTotal, boolInt(complete), connectionAuditHourlyTotalsVersion,
		connectionAuditHourlyAlgorithm, end.Format(time.RFC3339Nano), ts); err != nil {
		return err
	}
	// The hour is replaced, not merged, so its previous dimension values go
	// with it.
	if _, err := tx.ExecContext(ctx, `delete from connection_audit_hourly_dimensions where user_id=? and utc_hour=?`, userID, utcHour); err != nil {
		return err
	}
	for dimension, values := range dimensions {
		for value := range values {
			if _, err := tx.ExecContext(ctx, `insert or ignore into connection_audit_hourly_dimensions(user_id,utc_hour,dimension,value) values(?,?,?,?)`,
				userID, utcHour, dimension, value); err != nil {
				return err
			}
		}
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

type connectionAuditHourAggregate struct {
	ConnectionCount    int64
	ReportCount        int64
	ActivePeak         int64
	UploadBytes        int64
	DownloadBytes      int64
	LastEndedAt        string
	CollectionCount    int64
	DroppedBucketCount int64
	CapacityTotal      int64
}

// connectionAuditHourDimensions reads the distinct values one hour contributes,
// capped. The second return is false when a dimension hit the cap, which makes
// every window count derived from that hour a lower bound.
func (s *Store) connectionAuditHourDimensions(ctx context.Context, userID int64, start, end time.Time) (map[string]map[string]struct{}, bool, error) {
	out := map[string]map[string]struct{}{
		auditDimensionSourceIP: {},
		auditDimensionServer:   {},
		auditDimensionCountry:  {},
	}
	rows, err := s.db.QueryContext(ctx, `select distinct source_ip,server_id,source_country_code
		from connection_audit_reports
		where user_id=? and ended_at>=? and started_at>=? and started_at<? and internal_probe=0
			and probe_state not in ('confirmed','candidate') and dropped_bucket_count=0`,
		userID, start.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	complete := true
	for rows.Next() {
		var sourceIP, countryCode string
		var serverID int64
		if err := rows.Scan(&sourceIP, &serverID, &countryCode); err != nil {
			return nil, false, err
		}
		add := func(dimension, value string) {
			if strings.TrimSpace(value) == "" {
				return
			}
			set := out[dimension]
			if _, ok := set[value]; ok {
				return
			}
			if len(set) >= connectionAuditHourlyDimensionCap {
				complete = false
				return
			}
			set[value] = struct{}{}
		}
		add(auditDimensionSourceIP, sourceIP)
		add(auditDimensionServer, strconv.FormatInt(serverID, 10))
		add(auditDimensionCountry, strings.ToUpper(strings.TrimSpace(countryCode)))
	}
	return out, complete, rows.Err()
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

// connectionAuditMaxRepairedHours bounds how many dirty hours are repaired from
// raw reports beside the rollup. Beyond it, one aggregate over the whole window
// is the cheaper shape.
const connectionAuditMaxRepairedHours = 48

// connectionAuditHourlyDirtyHours returns the dirty hour keys per user in the
// window, rather than only which users have any.
func (s *Store) connectionAuditHourlyDirtyHours(ctx context.Context, userIDs []int64, since, until time.Time) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(userIDs) == 0 {
		return out, nil
	}
	args := []any{since.UTC().Format("2006-01-02T15:00:00Z"), until.UTC().Format("2006-01-02T15:00:00Z")}
	for _, id := range userIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `select user_id,utc_hour from connection_audit_hourly_dirty
		where utc_hour>=? and utc_hour<? and user_id in (`+inClause(len(userIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var hour string
		if err := rows.Scan(&userID, &hour); err != nil {
			return nil, err
		}
		out[userID] = append(out[userID], hour)
	}
	return out, rows.Err()
}

// connectionAuditRawHourBuckets aggregates specific hours from raw reports
// under the same filters as the rollup, so a stale or missing bucket can be
// replaced without re-reading the user's whole window.
func (s *Store) connectionAuditRawHourBuckets(ctx context.Context, userID int64, hours []string) (map[string]float64, error) {
	out := make(map[string]float64, len(hours))
	for _, hour := range hours {
		start := connectionAuditHourStart(hour)
		if start.IsZero() {
			continue
		}
		end := start.Add(time.Hour)
		var total float64
		// ended_at bounds the scan; started_at remains the exact filter. See
		// RecomputeConnectionAuditHour for why that narrowing is lossless.
		if err := s.db.QueryRowContext(ctx, `select coalesce(sum(connection_count),0)
			from connection_audit_reports
			where user_id=? and ended_at>=? and started_at>=? and started_at<? and internal_probe=0
				and probe_state not in ('confirmed','candidate') and dropped_bucket_count=0`,
			userID, start.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)).Scan(&total); err != nil {
			return nil, err
		}
		out[hour] = total
	}
	return out, nil
}

// mergeAuditHourBuckets replaces the named hours in buckets with freshly
// aggregated values, dropping a stale rollup row for an hour that no longer has
// any qualifying report.
func mergeAuditHourBuckets(buckets []auditHourBucket, hours []string, repaired map[string]float64) []auditHourBucket {
	replaced := make(map[string]struct{}, len(hours))
	for _, hour := range hours {
		replaced[hour] = struct{}{}
	}
	out := make([]auditHourBucket, 0, len(buckets)+len(repaired))
	for _, bucket := range buckets {
		if _, ok := replaced[ConnectionAuditHourKey(bucket.at)]; ok {
			continue
		}
		out = append(out, bucket)
	}
	for hour, value := range repaired {
		if value == 0 {
			continue
		}
		out = append(out, auditHourBucket{at: connectionAuditHourStart(hour), value: value})
	}
	sortAuditHourBuckets(out)
	return out
}

// ConnectionAuditWindowTotals is one user's window totals read from the rollup.
//
// Every field here is what the same window would have produced from the raw
// reports: the sums and peaks merge arithmetically, and the distinct counts are
// re-deduplicated from the values each hour stored rather than added up.
// DimensionsComplete is false when some hour in the window hit its dimension
// cap, which makes the three distinct counts lower bounds.
type ConnectionAuditWindowTotals struct {
	ConnectionCount    int64
	ReportCount        int64
	ActivePeak         int64
	UploadBytes        int64
	DownloadBytes      int64
	LastEndedAt        time.Time
	CollectionCount    int64
	DroppedBucketCount int64
	CapacityTotal      int64
	SourceIPCount      int
	ServerCount        int
	RegionCount        int
	SourceIPs          []string
	DimensionsComplete bool
	// CoveredFromHour is the earliest hour in the requested window that
	// actually carries measured totals. Hours written before the extended
	// columns existed are excluded, so this can be later than the window start
	// and the console reports the difference rather than presenting a month of
	// mostly-unmeasured hours as a month of low activity.
	CoveredFromHour time.Time
}

// ConnectionAuditWindowTotalsFromRollup reads window totals for the given users
// from the hourly rollup. since is inclusive and until exclusive, both aligned
// to the hour the caller wants covered.
func (s *Store) ConnectionAuditWindowTotalsFromRollup(ctx context.Context, userIDs []int64, since, until time.Time) (map[int64]*ConnectionAuditWindowTotals, error) {
	out := map[int64]*ConnectionAuditWindowTotals{}
	if len(userIDs) == 0 {
		return out, nil
	}
	fromHour := ConnectionAuditHourKey(since)
	toHour := ConnectionAuditHourKey(until)
	args := []any{fromHour, toHour, connectionAuditHourlyAlgorithm, connectionAuditHourlyTotalsVersion}
	for _, id := range userIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `select user_id,
			coalesce(sum(connection_count),0),
			coalesce(sum(report_count),0),
			coalesce(max(active_peak),0),
			coalesce(sum(upload_bytes),0),
			coalesce(sum(download_bytes),0),
			coalesce(max(last_ended_at),''),
			coalesce(sum(collection_count),0),
			coalesce(sum(dropped_bucket_count),0),
			coalesce(sum(capacity_total),0),
			min(dimensions_complete),
			min(utc_hour)
		from connection_audit_hourly
		where utc_hour>=? and utc_hour<=? and algorithm_version=? and totals_version>=? and user_id in (`+inClause(len(userIDs))+`)
		group by user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var lastEndedAt, earliestHour string
		var complete sql.NullInt64
		item := &ConnectionAuditWindowTotals{DimensionsComplete: true}
		if err := rows.Scan(&userID, &item.ConnectionCount, &item.ReportCount, &item.ActivePeak,
			&item.UploadBytes, &item.DownloadBytes, &lastEndedAt, &item.CollectionCount,
			&item.DroppedBucketCount, &item.CapacityTotal, &complete, &earliestHour); err != nil {
			return nil, err
		}
		if earliestHour != "" {
			item.CoveredFromHour = parseTime(earliestHour)
		}
		if lastEndedAt != "" {
			item.LastEndedAt = parseTime(lastEndedAt)
		}
		if complete.Valid && complete.Int64 == 0 {
			item.DimensionsComplete = false
		}
		out[userID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	return out, s.attachConnectionAuditWindowDimensions(ctx, out, fromHour, toHour)
}

func (s *Store) attachConnectionAuditWindowDimensions(ctx context.Context, totals map[int64]*ConnectionAuditWindowTotals, fromHour, toHour string) error {
	userIDs := make([]any, 0, len(totals))
	for userID := range totals {
		userIDs = append(userIDs, userID)
	}
	args := append([]any{fromHour, toHour}, userIDs...)
	rows, err := s.db.QueryContext(ctx, `select user_id,dimension,value
		from connection_audit_hourly_dimensions
		where utc_hour>=? and utc_hour<=? and user_id in (`+inClause(len(userIDs))+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[int64]map[string]map[string]struct{}{}
	for rows.Next() {
		var userID int64
		var dimension, value string
		if err := rows.Scan(&userID, &dimension, &value); err != nil {
			return err
		}
		if seen[userID] == nil {
			seen[userID] = map[string]map[string]struct{}{}
		}
		if seen[userID][dimension] == nil {
			seen[userID][dimension] = map[string]struct{}{}
		}
		seen[userID][dimension][value] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for userID, item := range totals {
		dimensions := seen[userID]
		item.SourceIPCount = len(dimensions[auditDimensionSourceIP])
		item.ServerCount = len(dimensions[auditDimensionServer])
		item.RegionCount = len(dimensions[auditDimensionCountry])
		item.SourceIPs = make([]string, 0, len(dimensions[auditDimensionSourceIP]))
		for value := range dimensions[auditDimensionSourceIP] {
			item.SourceIPs = append(item.SourceIPs, value)
		}
		sort.Strings(item.SourceIPs)
	}
	return nil
}
