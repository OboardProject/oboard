package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const SLAProjectionVersion = 1
const slaBucketSeconds int64 = 300

var ErrSLAProjectionChanged = errors.New("SLA projection changed; retry batch")

type SLAProjectionBucket struct {
	Outages    []model.ConnectivityOutage
	Start      int64
	Stats      model.ConnectivitySLAStats
	Checkpoint json.RawMessage
}
type SLAProjectionWork struct {
	Partial                                                                  json.RawMessage
	PartialMode, PartialMore                                                 bool
	PartialCeiling                                                           int64
	ReadStepSeconds                                                          int64
	CoverageChanged                                                          bool
	CoverageCheckpoint                                                       json.RawMessage
	CoverageBaseline                                                         []model.ServerConnectivityEvent
	RetentionDays                                                            int
	ServerID, From, To, CoverageFrom, Frontier, Revision, Generation, Cursor int64
	Repair                                                                   bool
	DirtyUntil                                                               int64
	Checkpoint, OldEndCheckpoint                                             json.RawMessage
	Baseline, Events                                                         []model.ServerConnectivityEvent
}
type SLAProjectionOutput struct {
	Partial      json.RawMessage
	CoverageSeed json.RawMessage
	Seed         json.RawMessage
	Buckets      []SLAProjectionBucket
}
type SLAProjectionResult struct {
	Events, Buckets int
	ServerID        int64
	Phase           string
}
type slaProjectionState struct {
	cursor, start, enrolled, generation, floor int64
	version                                    int
}

func (s *Store) ensureSLAProjectionSchema(ctx context.Context) error {
	for _, query := range []string{
		`create table if not exists sla_projection_state(id integer primary key check(id=1),algorithm_version integer not null default 1,event_cursor integer not null default -1,start_at integer not null default 0,enroll_cursor integer not null default 0,generation integer not null default 1,retention_floor integer not null default 0)`,
		`insert into sla_projection_state(id) values(1) on conflict(id) do nothing`,
		`create table if not exists sla_projection_servers(server_id integer primary key references servers(id) on delete cascade,coverage_from integer not null,frontier integer not null,seed_json text not null default '',dirty_from integer,dirty_until integer,repair_next integer,revision integer not null default 1,phase text not null default 'catching_up',retry_after integer not null default 0,updated_at text not null)`,
		`create index if not exists idx_sla_projection_work on sla_projection_servers(retry_after,frontier,server_id)`,
		`create table if not exists sla_projection_buckets(server_id integer not null references servers(id) on delete cascade,bucket_start integer not null,stats_json text not null,end_checkpoint text not null,event_cursor integer not null,algorithm_version integer not null,primary key(server_id,bucket_start))`,
		`create table if not exists sla_projection_partial(server_id integer primary key references servers(id) on delete cascade,bucket_start integer not null,generation integer not null,algorithm_version integer not null,ceiling integer not null,last_time text not null,last_priority integer not null,last_id integer not null,body text not null)`,
		`create index if not exists idx_sla_projection_time on sla_projection_buckets(bucket_start)`,
	} {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "sla_projection_buckets", "outages_json", `alter table sla_projection_buckets add column outages_json text not null default '[]'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sla_projection_buckets", "details_complete", `alter table sla_projection_buckets add column details_complete integer not null default 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sla_projection_servers", "details_version", `alter table sla_projection_servers add column details_version integer not null default 0`); err != nil {
		return err
	}
	return nil
}

func (s *Store) readSLAProjectionState(ctx context.Context) (slaProjectionState, error) {
	var state slaProjectionState
	err := s.db.QueryRowContext(ctx, `select event_cursor,start_at,enroll_cursor,generation,retention_floor,algorithm_version from sla_projection_state where id=1`).Scan(&state.cursor, &state.start, &state.enrolled, &state.generation, &state.floor, &state.version)
	if err == nil && state.version != SLAProjectionVersion {
		err = errors.New("unsupported SLA projection version")
	}
	return state, err
}

// Discover events by insertion ID. Only this bounded pass marks historical
// ranges dirty; no trigger adds work to the Agent's report transaction.
func (s *Store) scanSLAProjectionEvents(ctx context.Context, at time.Time, limit int) (int, error) {
	state, err := s.readSLAProjectionState(ctx)
	if err != nil {
		return 0, err
	}
	if state.cursor < 0 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		var boundary int64
		if err := tx.QueryRowContext(ctx, `select coalesce(max(id),0) from server_connectivity_events`).Scan(&boundary); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `update sla_projection_state set event_cursor=?,start_at=? where id=1 and event_cursor=-1 and algorithm_version=1`, boundary, at.UTC().Truncate(5*time.Minute).Unix()); err != nil {
			return 0, err
		}
		return 0, tx.Commit()
	}
	rows, err := s.db.QueryContext(ctx, `select id,server_id,effective_at from server_connectivity_events where id>? order by id limit ?`, state.cursor, limit)
	if err != nil {
		return 0, err
	}
	marks := make(map[int64][]int64)
	through := state.cursor
	count := 0
	for rows.Next() {
		var id, server int64
		var value string
		if err := rows.Scan(&id, &server, &value); err != nil {
			rows.Close()
			return 0, err
		}
		if marks[server] == nil && len(marks) >= 64 {
			break
		}
		checked, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			rows.Close()
			return 0, err
		}
		marks[server] = append(marks[server], checked.UTC().Truncate(5*time.Minute).Unix())
		through = id
		count++
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update sla_projection_state set event_cursor=? where id=1 and event_cursor=? and generation=? and enroll_cursor=? and algorithm_version=?`, through, state.cursor, state.generation, state.enrolled, SLAProjectionVersion)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrSLAProjectionChanged
	}
	enrolled, err := queryInt64sTx(ctx, tx, `select id from servers where id>? order by id limit 32`, state.enrolled)
	if err != nil {
		return 0, err
	}
	for _, server := range enrolled {
		if marks[server] == nil {
			marks[server] = []int64{}
		}
		state.enrolled = server
	}
	for server, times := range marks {
		if _, err := tx.ExecContext(ctx, `insert into sla_projection_servers(server_id,coverage_from,frontier,updated_at,details_version) select id,max(?,cast(unixepoch(created_at)/300 as integer)*300),max(?,cast(unixepoch(created_at)/300 as integer)*300),?,1 from servers where id=? on conflict(server_id) do nothing`, state.start, state.start, now(), server); err != nil {
			return 0, err
		}
		var coverage, frontier int64
		var dirty, until, next sql.NullInt64
		var seed, phase string
		err := tx.QueryRowContext(ctx, `select coverage_from,frontier,dirty_from,dirty_until,repair_next,seed_json,phase from sla_projection_servers where server_id=?`, server).Scan(&coverage, &frontier, &dirty, &until, &next, &seed, &phase)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return 0, err
		}
		for _, value := range times {
			if _, err := tx.ExecContext(ctx, `delete from sla_projection_partial where server_id=? and bucket_start>=?`, server, value); err != nil {
				return 0, err
			}
			if value < coverage {
				seed = ""
			}
			if value >= frontier {
				continue
			}
			start := max(coverage, value)
			end := max(coverage+slaBucketSeconds, value+slaBucketSeconds)
			if !dirty.Valid || start < dirty.Int64 {
				dirty = sql.NullInt64{Int64: start, Valid: true}
			}
			if !next.Valid || start < next.Int64 {
				next = sql.NullInt64{Int64: start, Valid: true}
			}
			if !until.Valid || end > until.Int64 {
				until = sql.NullInt64{Int64: end, Valid: true}
			}
			phase = "correcting"
		}
		if _, err := tx.ExecContext(ctx, `update sla_projection_servers set dirty_from=?,dirty_until=?,repair_next=?,seed_json=?,phase=?,revision=revision+1,updated_at=? where server_id=?`, dirty, until, next, seed, phase, now(), server); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update sla_projection_state set enroll_cursor=? where id=1`, state.enrolled); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func (s *Store) RunSLAProjectionBatch(ctx context.Context, at time.Time, limit int, build func(SLAProjectionWork) (SLAProjectionOutput, error)) (SLAProjectionResult, error) {
	result := SLAProjectionResult{}
	if limit < 1 || limit > 500 {
		return result, errors.New("invalid SLA batch limit")
	}
	count, err := s.scanSLAProjectionEvents(ctx, at, limit)
	result.Events = count
	if err != nil {
		return result, err
	}
	if count >= limit {
		return result, nil
	}
	if _, err := s.db.ExecContext(ctx, `update sla_projection_servers set seed_json='',dirty_from=coverage_from,repair_next=coverage_from,dirty_until=frontier,phase='correcting',details_version=1,revision=revision+1 where server_id in (select server_id from sla_projection_servers where details_version=0 limit 1)`); err != nil {
		return result, err
	}
	for jobs := 0; jobs < 16 && result.Events < limit; jobs++ {
		work, err := s.prepareSLAProjectionWork(ctx, at, limit-result.Events)
		if err == sql.ErrNoRows {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		result.ServerID = work.ServerID
		result.Events += len(work.Events)
		output, err := build(work)
		if err != nil {
			s.deferSLAProjection(ctx, work.ServerID, work.Revision, at)
			return result, err
		}
		phase, err := s.commitSLAProjection(ctx, work, output, at.UTC().Truncate(5*time.Minute).Unix())
		result.Phase = phase
		if err != nil {
			return result, err
		}
		result.Buckets += len(output.Buckets)
		// Yield after a repair chunk so other lanes retain predictable turns.
		if work.Repair || work.PartialMode {
			return result, nil
		}
	}
	return result, nil
}

func (s *Store) deferSLAProjection(ctx context.Context, id, revision int64, at time.Time) {
	_, _ = s.db.ExecContext(ctx, `update sla_projection_servers set phase='unavailable',retry_after=? where server_id=? and revision=?`, at.Add(30*time.Second).Unix(), id, revision)
}

func (s *Store) prepareSLAProjectionWork(ctx context.Context, at time.Time, limit int) (SLAProjectionWork, error) {
	work := SLAProjectionWork{}
	state, err := s.readSLAProjectionState(ctx)
	if err != nil {
		return work, err
	}
	var repair, until sql.NullInt64
	var seed string
	closed := at.UTC().Truncate(5 * time.Minute).Unix()
	err = s.db.QueryRowContext(ctx, `select server_id,coverage_from,frontier,repair_next,dirty_until,revision,seed_json from sla_projection_servers where retry_after<=? and (repair_next is not null or frontier<?) order by coalesce(repair_next,frontier),server_id limit 1`, at.Unix(), closed).Scan(&work.ServerID, &work.CoverageFrom, &work.Frontier, &repair, &until, &work.Revision, &seed)
	if err != nil {
		return work, err
	}
	settings, err := s.ListSettings(ctx)
	if err != nil {
		return work, err
	}
	work.RetentionDays = ServerMonitoringRetentionDays(settings)
	cutoff := at.Add(-time.Duration(work.RetentionDays) * 24 * time.Hour)
	floor := cutoff.UTC().Truncate(5 * time.Minute)
	if floor.Before(cutoff) {
		floor = floor.Add(5 * time.Minute)
	}
	retained := max(floor.Unix(), state.floor)
	work.From = work.Frontier
	if repair.Valid {
		work.From = repair.Int64
		work.Repair = true
		work.DirtyUntil = until.Int64
	}
	if work.CoverageFrom < retained {
		work.CoverageChanged = true
		work.CoverageFrom = retained
		seed = ""
		var checkpoint string
		err := s.db.QueryRowContext(ctx, `select end_checkpoint from sla_projection_buckets where server_id=? and bucket_start=? and algorithm_version=?`, work.ServerID, retained-300, SLAProjectionVersion).Scan(&checkpoint)
		if err != nil && err != sql.ErrNoRows {
			return work, err
		}
		if err == nil {
			work.CoverageCheckpoint = json.RawMessage(checkpoint)
		} else {
			work.CoverageBaseline, err = s.slaProjectionBaseline(ctx, work.ServerID, time.Unix(retained, 0), state.cursor)
			if err != nil {
				return work, err
			}
		}
	}
	work.From = max(work.From, work.CoverageFrom)
	work.To = min(closed, work.From+3600)
	work.Generation, work.Cursor = state.generation, state.cursor
	if work.To <= work.From {
		return work, sql.ErrNoRows
	}
	if work.From == work.CoverageFrom {
		work.Checkpoint = json.RawMessage(seed)
	} else {
		var checkpoint string
		err := s.db.QueryRowContext(ctx, `select end_checkpoint from sla_projection_buckets where server_id=? and bucket_start=? and algorithm_version=?`, work.ServerID, work.From-300, SLAProjectionVersion).Scan(&checkpoint)
		if err != nil {
			s.deferSLAProjection(ctx, work.ServerID, work.Revision, at)
			return work, fmt.Errorf("missing SLA checkpoint: %w", err)
		}
		work.Checkpoint = json.RawMessage(checkpoint)
	}
	if len(work.Checkpoint) == 0 {
		work.Baseline, err = s.slaProjectionBaseline(ctx, work.ServerID, time.Unix(work.From, 0), state.cursor)
		if err != nil {
			return work, err
		}
	}
	if resumed, err := s.prepareSLAPartial(ctx, &work, limit, false); err != nil {
		return work, err
	} else if resumed {
		err := s.loadSLAOldEnd(ctx, &work)
		return work, err
	}
	rows, err := s.db.QueryContext(ctx, `select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and effective_at>=? and effective_at<? and id<=? order by effective_at,id limit ?`, work.ServerID, connectivityTimeBound(time.Unix(work.From, 0)), connectivityTimeBound(time.Unix(work.To, 0)), state.cursor, limit+1)
	if err != nil {
		return work, err
	}
	work.Events, err = scanConnectivityEvents(rows)
	if err != nil {
		return work, err
	}
	if len(work.Events) > limit {
		work.To = work.Events[limit].EffectiveAt.UTC().Truncate(5 * time.Minute).Unix()
		if work.To <= work.From {
			if _, err := s.prepareSLAPartial(ctx, &work, limit, true); err != nil {
				return work, err
			}
			err := s.loadSLAOldEnd(ctx, &work)
			return work, err
		}
		kept := work.Events[:0]
		for _, event := range work.Events {
			if event.EffectiveAt.Before(time.Unix(work.To, 0)) {
				kept = append(kept, event)
			}
		}
		work.Events = kept
	}
	sort.Slice(work.Events, func(i, j int) bool { return slaEventLess(work.Events[i], work.Events[j]) })
	var old string
	err = s.db.QueryRowContext(ctx, `select end_checkpoint from sla_projection_buckets where server_id=? and bucket_start=? and algorithm_version=?`, work.ServerID, work.To-300, SLAProjectionVersion).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return work, err
	}
	work.OldEndCheckpoint = json.RawMessage(old)
	return work, nil
}

func slaEventLess(a, b model.ServerConnectivityEvent) bool {
	if !a.EffectiveAt.Equal(b.EffectiveAt) {
		return a.EffectiveAt.Before(b.EffectiveAt)
	}
	if connectivityEventPriority(a.Kind) != connectivityEventPriority(b.Kind) {
		return connectivityEventPriority(a.Kind) < connectivityEventPriority(b.Kind)
	}
	return a.ID < b.ID
}

func (s *Store) slaProjectionBaseline(ctx context.Context, serverID int64, at time.Time, cursor int64) ([]model.ServerConnectivityEvent, error) {
	var baseline []model.ServerConnectivityEvent
	for _, kind := range connectivityBaselineKinds {
		event, err := s.latestConnectivityEventBeforeCursor(ctx, serverID, at, []model.ConnectivityEventKind{kind}, cursor)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		baseline = append(baseline, event)
	}
	sort.Slice(baseline, func(i, j int) bool { return slaEventLess(baseline[i], baseline[j]) })
	return baseline, nil
}

func (s *Store) commitSLAProjection(ctx context.Context, work SLAProjectionWork, output SLAProjectionOutput, closed int64) (string, error) {
	if len(output.Partial) > 0 {
		return s.commitSLAPartial(ctx, work, output)
	}
	if work.From%300 != 0 || work.To%300 != 0 || len(output.Buckets) == 0 || len(output.Buckets) > 12 || int64(len(output.Buckets))*300 != work.To-work.From || len(output.Seed) > 4096 || !json.Valid(output.Seed) {
		return "", errors.New("invalid SLA projection output")
	}
	for i, bucket := range output.Buckets {
		stats := bucket.Stats
		if bucket.Start != work.From+int64(i)*300 || stats.DurationNS != int64(5*time.Minute) || stats.OnlineNS < 0 || stats.OfflineNS < 0 || stats.UnknownNS < 0 || stats.OnlineNS+stats.OfflineNS+stats.UnknownNS != stats.DurationNS || stats.OutageCount < 0 || len(bucket.Checkpoint) > 4096 || !json.Valid(bucket.Checkpoint) {
			return "", errors.New("invalid SLA bucket")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var setting string
	err = tx.QueryRowContext(ctx, `select value from app_settings where key=?`, ServerMonitoringRetentionDaysSetting).Scan(&setting)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if work.RetentionDays > 0 && ServerMonitoringRetentionDays(map[string]string{ServerMonitoringRetentionDaysSetting: setting}) != work.RetentionDays {
		return "", ErrSLAProjectionChanged
	}
	var generation, cursor int64
	var version int
	if err := tx.QueryRowContext(ctx, `select generation,event_cursor,algorithm_version from sla_projection_state where id=1`).Scan(&generation, &cursor, &version); err != nil {
		return "", err
	}
	if generation != work.Generation || cursor != work.Cursor || version != SLAProjectionVersion {
		return "", ErrSLAProjectionChanged
	}
	frontier := max(work.Frontier, work.To)
	var next any
	phase := "catching_up"
	if work.Repair && work.To < work.Frontier && (work.To < work.DirtyUntil || !bytes.Equal(output.Buckets[len(output.Buckets)-1].Checkpoint, work.OldEndCheckpoint)) {
		next = work.To
		phase = "correcting"
	} else if frontier >= closed {
		phase = "ready"
	}
	seed := string(output.Seed)
	if work.CoverageChanged {
		if len(output.CoverageSeed) > 4096 || !json.Valid(output.CoverageSeed) {
			return "", errors.New("invalid SLA coverage checkpoint")
		}
		seed = string(output.CoverageSeed)
	}
	result, err := tx.ExecContext(ctx, `update sla_projection_servers set coverage_from=?,frontier=?,seed_json=case when ?=coverage_from or ?>coverage_from then ? else seed_json end,repair_next=?,dirty_from=case when ? is null then null else dirty_from end,dirty_until=case when ? is null then null else dirty_until end,phase=?,retry_after=0,revision=revision+1,updated_at=? where server_id=? and revision=?`, work.CoverageFrom, frontier, work.From, work.CoverageFrom, seed, next, next, next, phase, now(), work.ServerID, work.Revision)
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrSLAProjectionChanged
	}
	for _, bucket := range output.Buckets {
		data, _ := json.Marshal(bucket.Stats)
		details, _ := json.Marshal(bucket.Outages)
		if _, err := tx.ExecContext(ctx, `insert into sla_projection_buckets(server_id,bucket_start,stats_json,end_checkpoint,event_cursor,algorithm_version,outages_json,details_complete) values(?,?,?,?,?,?,?,1) on conflict(server_id,bucket_start) do update set stats_json=excluded.stats_json,end_checkpoint=excluded.end_checkpoint,event_cursor=excluded.event_cursor,algorithm_version=excluded.algorithm_version,outages_json=excluded.outages_json,details_complete=1`, work.ServerID, bucket.Start, string(data), string(bucket.Checkpoint), work.Cursor, SLAProjectionVersion, string(details)); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from sla_projection_partial where server_id=?`, work.ServerID); err != nil {
		return "", err
	}
	return phase, tx.Commit()
}

// Inspect methods expose bounded internal diagnostics, not a summary read path
// for HTTP/MCP. A future reader must also account for events after EventCursor.
type SLAProjectionStatus struct {
	RetentionFloor                                            int64
	LatestEventID                                             int64
	Seed                                                      json.RawMessage
	CoverageFrom, Frontier, Revision, EventCursor, Generation int64
	DirtyFrom, DirtyUntil, RepairNext                         sql.NullInt64
	Phase                                                     string
}

func (s *Store) InspectSLAProjection(ctx context.Context, serverID int64) (SLAProjectionStatus, error) {
	var status SLAProjectionStatus
	var seed string
	err := s.db.QueryRowContext(ctx, `select q.coverage_from,q.frontier,q.revision,q.dirty_from,q.dirty_until,q.repair_next,q.phase,s.event_cursor,s.generation,q.seed_json,(select coalesce(max(id),0) from server_connectivity_events),s.retention_floor from sla_projection_servers q cross join sla_projection_state s where q.server_id=? and s.id=1`, serverID).Scan(&status.CoverageFrom, &status.Frontier, &status.Revision, &status.DirtyFrom, &status.DirtyUntil, &status.RepairNext, &status.Phase, &status.EventCursor, &status.Generation, &seed, &status.LatestEventID, &status.RetentionFloor)
	status.Seed = json.RawMessage(seed)
	if status.Phase == "ready" && (status.RetentionFloor > status.CoverageFrom || status.LatestEventID > status.EventCursor || status.Frontier < time.Now().UTC().Truncate(5*time.Minute).Unix()) {
		status.Phase = "catching_up"
	}
	return status, err
}
func (s *Store) InspectSLABuckets(ctx context.Context, serverID, from, to int64) ([]SLAProjectionBucket, error) {
	if serverID <= 0 || to <= from || to-from > 360*300 {
		return nil, errors.New("invalid SLA inspection range")
	}
	rows, err := s.db.QueryContext(ctx, `select bucket_start,stats_json,end_checkpoint from sla_projection_buckets where server_id=? and bucket_start>=? and bucket_start<? order by bucket_start limit 360`, serverID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SLAProjectionBucket{}
	for rows.Next() {
		var bucket SLAProjectionBucket
		var stats, checkpoint string
		if err := rows.Scan(&bucket.Start, &stats, &checkpoint); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(stats), &bucket.Stats); err != nil {
			return nil, err
		}
		bucket.Checkpoint = json.RawMessage(checkpoint)
		result = append(result, bucket)
	}
	return result, rows.Err()
}

// Historical enrollment changes only the repair queue; work remains in the
// ordinary bounded projector and is resumable through its existing checkpoints.
func (s *Store) QueueSLAHistoricalBackfill(ctx context.Context, at time.Time, limit int) (int, error) {
	settings, err := s.ListSettings(ctx)
	if err != nil {
		return 0, err
	}
	floor := slaRetentionFloor(at.Add(-time.Duration(ServerMonitoringRetentionDays(settings)) * 24 * time.Hour))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select q.server_id,max(?,cast(unixepoch(s.created_at)/300 as integer)*300) from sla_projection_servers q join servers s on s.id=q.server_id where q.coverage_from>max(?,cast(unixepoch(s.created_at)/300 as integer)*300) order by q.server_id limit ?`, floor, floor, min(32, max(1, limit)))
	if err != nil {
		return 0, err
	}
	type item struct{ id, start int64 }
	items := []item{}
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.id, &x.start); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, x)
	}
	readErr, closeErr := rows.Err(), rows.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	for _, x := range items {
		if _, err := tx.ExecContext(ctx, `update sla_projection_servers set coverage_from=?,seed_json='',dirty_from=?,repair_next=?,dirty_until=max(coalesce(dirty_until,0),frontier),phase='correcting',retry_after=0,revision=revision+1 where server_id=?`, x.start, x.start, x.start, x.id); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit()
}
