package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (s *Store) loadSLAOldEnd(ctx context.Context, work *SLAProjectionWork) error {
	var old string
	err := s.db.QueryRowContext(ctx, `select end_checkpoint from sla_projection_buckets where server_id=? and bucket_start=? and algorithm_version=?`, work.ServerID, work.To-300, SLAProjectionVersion).Scan(&old)
	if err == sql.ErrNoRows {
		return nil
	}
	work.OldEndCheckpoint = json.RawMessage(old)
	return err
}

func (s *Store) prepareSLAPartial(ctx context.Context, work *SLAProjectionWork, limit int, force bool) (bool, error) {
	var start, generation, ceiling, lastID int64
	var version, priority int
	var lastTime, body string
	err := s.db.QueryRowContext(ctx, `select bucket_start,generation,algorithm_version,ceiling,last_time,last_priority,last_id,body from sla_projection_partial where server_id=?`, work.ServerID).Scan(&start, &generation, &version, &ceiling, &lastTime, &priority, &lastID, &body)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	valid := err == nil && start == work.From && generation == work.Generation && version == SLAProjectionVersion && ceiling <= work.Cursor && !work.CoverageChanged
	if !valid && !force {
		return false, nil
	}
	work.PartialMode = true
	work.To = work.From + 300
	work.PartialCeiling = work.Cursor
	if valid {
		work.Partial = json.RawMessage(body)
		work.PartialCeiling = ceiling
	} else {
		lastTime = ""
		priority = -1
		lastID = 0
	}
	// RFC3339Nano omits trailing zeros; normalize for exact replay ordering.
	const stamp = `substr(effective_at,1,19)||'.'||substr(replace(substr(effective_at,21),'Z','')||'000000000',1,9)`
	const columns = `id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at`
	var queries []string
	var args []any
	for _, kind := range connectivityBaselineKinds {
		rank := connectivityEventPriority(kind)
		queries = append(queries, `select * from (select `+columns+`,`+stamp+` as replay_time,? as priority from server_connectivity_events indexed by idx_server_connectivity_events_server_kind_time where server_id=? and kind=? and effective_at>=? and effective_at<? and id<=? and (`+stamp+`>? or (`+stamp+`=? and (? > ? or (?=? and id>?)))) order by replay_time,id limit ?)`)
		args = append(args, rank, work.ServerID, kind, connectivityTimeBound(time.Unix(work.From, 0)), connectivityTimeBound(time.Unix(work.To, 0)), work.PartialCeiling, lastTime, lastTime, rank, priority, rank, priority, lastID, limit+1)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `select `+columns+` from (`+strings.Join(queries, ` union all `)+`) order by replay_time,priority,id limit ?`, args...)
	if err != nil {
		return false, err
	}
	work.Events, err = scanConnectivityEvents(rows)
	if err != nil {
		return false, err
	}
	work.PartialMore = len(work.Events) > limit
	if work.PartialMore {
		work.Events = work.Events[:limit]
	}
	return true, nil
}

func (s *Store) commitSLAPartial(ctx context.Context, work SLAProjectionWork, output SLAProjectionOutput) (string, error) {
	if work.From%300 != 0 || !work.PartialMode || !work.PartialMore || work.To-work.From != 300 || len(work.Events) == 0 || len(output.Buckets) != 0 || len(output.Partial) > 16384 || !json.Valid(output.Partial) {
		return "", errors.New("invalid SLA partial output")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var generation, cursor int64
	var version int
	if err := tx.QueryRowContext(ctx, `select generation,event_cursor,algorithm_version from sla_projection_state where id=1`).Scan(&generation, &cursor, &version); err != nil {
		return "", err
	}
	if generation != work.Generation || cursor != work.Cursor || version != SLAProjectionVersion {
		return "", ErrSLAProjectionChanged
	}
	var setting string
	err = tx.QueryRowContext(ctx, `select value from app_settings where key=?`, ServerMonitoringRetentionDaysSetting).Scan(&setting)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if ServerMonitoringRetentionDays(map[string]string{ServerMonitoringRetentionDaysSetting: setting}) != work.RetentionDays {
		return "", ErrSLAProjectionChanged
	}
	seed := output.Seed
	if work.CoverageChanged {
		seed = output.CoverageSeed
	}
	if len(seed) > 4096 || !json.Valid(seed) {
		return "", errors.New("invalid SLA partial seed")
	}
	phase := "catching_up"
	if work.Repair {
		phase = "correcting"
	}
	result, err := tx.ExecContext(ctx, `update sla_projection_servers set coverage_from=?,seed_json=case when ?=coverage_from or ?>coverage_from then ? else seed_json end,revision=revision+1,retry_after=0,phase=?,updated_at=? where server_id=? and revision=?`, work.CoverageFrom, work.From, work.CoverageFrom, string(seed), phase, now(), work.ServerID, work.Revision)
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
	last := work.Events[len(work.Events)-1]
	stamp := last.EffectiveAt.UTC().Format("2006-01-02T15:04:05.000000000")
	_, err = tx.ExecContext(ctx, `insert into sla_projection_partial(server_id,bucket_start,generation,algorithm_version,ceiling,last_time,last_priority,last_id,body) values(?,?,?,?,?,?,?,?,?) on conflict(server_id) do update set bucket_start=excluded.bucket_start,generation=excluded.generation,algorithm_version=excluded.algorithm_version,ceiling=excluded.ceiling,last_time=excluded.last_time,last_priority=excluded.last_priority,last_id=excluded.last_id,body=excluded.body`, work.ServerID, work.From, work.Generation, SLAProjectionVersion, work.PartialCeiling, stamp, connectivityEventPriority(last.Kind), last.ID, string(output.Partial))
	if err != nil {
		return "", err
	}
	return phase, tx.Commit()
}
