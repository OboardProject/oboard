package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const agentTaskSelectSQL = `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks`

// AgentUpdateCandidate is the lightweight row used by the fleet coordinator.
type AgentUpdateCandidate struct {
	ServerID   int64
	AgentID    string
	AgentBuild string
	Status     model.ServerStatus
}

// AgentFleetCounts is the bounded summary for Agent fleet update status.
type AgentFleetCounts struct {
	Total    int
	Current  int
	Pending  int
	Running  int
	Offline  int
	Enrolled int
	Outdated int
}

// AgentFleetState is the persisted coordinator pause/circuit-breaker snapshot.
type AgentFleetState struct {
	Paused          bool
	Rolling         bool
	TargetBuild     string
	Attempted       int
	Succeeded       int
	Failed          int
	LastPauseReason string
	UpdatedAt       time.Time
}

// AgentUpdateRetry is the per-server retry gate for one target build.
type AgentUpdateRetry struct {
	ServerID    int64
	TargetBuild string
	Attempts    int
	NextRetryAt *time.Time
	LastError   string
	UpdatedAt   time.Time
}

func (s *Store) migrateAgentUpdateIndexes(ctx context.Context) error {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `update agent_tasks set status='failed', result_json=?, updated_at=?, completed_at=? where type='update_agent' and status in ('pending','running') and exists (select 1 from agent_tasks newer where newer.server_id=agent_tasks.server_id and newer.type='update_agent' and newer.status in ('pending','running') and newer.id>agent_tasks.id)`, `{"message":"superseded duplicate update_agent"}`, ts, ts); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create unique index if not exists idx_agent_tasks_one_active_update on agent_tasks(server_id) where type='update_agent' and status in ('pending','running')`); err != nil {
		return err
	}
	return s.ensureColumn(ctx, "agent_fleet_update_state", "rolling", `alter table agent_fleet_update_state add column rolling integer not null default 0`)
}

// ListAgentUpdateCandidates returns at most limit online enrolled servers whose
// stored agent_build is not the target. Callers still apply buildNeedsUpdate.
func (s *Store) ListAgentUpdateCandidates(ctx context.Context, targetBuild string, limit int) ([]AgentUpdateCandidate, error) {
	targetBuild = strings.TrimSpace(targetBuild)
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `select id, coalesce(agent_id,''), coalesce(agent_build,''), status from servers where status=? and agent_id is not null and agent_id<>'' and coalesce(agent_build,'')<>? and not exists (select 1 from agent_tasks t where t.server_id=servers.id and t.type=? and t.status in ('pending','running')) order by id limit ?`, model.ServerOnline, targetBuild, model.AgentTaskTypeUpdateAgent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AgentUpdateCandidate, 0, limit)
	for rows.Next() {
		var item AgentUpdateCandidate
		if err := rows.Scan(&item.ServerID, &item.AgentID, &item.AgentBuild, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CountAgentUpdateFleet returns fleet-wide counters without loading telemetry.
func (s *Store) CountAgentUpdateFleet(ctx context.Context, targetBuild string) (AgentFleetCounts, error) {
	targetBuild = strings.TrimSpace(targetBuild)
	var counts AgentFleetCounts
	if err := s.db.QueryRowContext(ctx, `select
		count(*),
		coalesce(sum(case when agent_id is not null and agent_id<>'' then 1 else 0 end),0),
		coalesce(sum(case when status=? and agent_id is not null and agent_id<>'' and coalesce(agent_build,'')=? then 1 else 0 end),0),
		coalesce(sum(case when status=? and agent_id is not null and agent_id<>'' and coalesce(agent_build,'')<>? then 1 else 0 end),0),
		coalesce(sum(case when status<>? and agent_id is not null and agent_id<>'' then 1 else 0 end),0),
		coalesce(sum(case when agent_id is not null and agent_id<>'' and coalesce(agent_build,'')<>? then 1 else 0 end),0)
		from servers`, model.ServerOnline, targetBuild, model.ServerOnline, targetBuild, model.ServerOnline, targetBuild).Scan(
		&counts.Total, &counts.Enrolled, &counts.Current, &counts.Pending, &counts.Offline, &counts.Outdated,
	); err != nil {
		return AgentFleetCounts{}, err
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from agent_tasks where type=? and status in ('pending','running')`, model.AgentTaskTypeUpdateAgent).Scan(&counts.Running); err != nil {
		return AgentFleetCounts{}, err
	}
	if counts.Pending > counts.Running {
		counts.Pending -= counts.Running
	} else {
		counts.Pending = 0
	}
	return counts, nil
}

// MarkAgentUpdateAwaitingRestart records the intermediate phase of an
// update_agent task without completing it.
//
// The Agent replaces the binaries, reports the result, and only then arms its
// own restart. Treating that first report as terminal success declared the
// update done while the process was still the old build, and it emptied the
// active-task slot that the reconnect confirmation looks up. The task stays
// 'running' until the Agent comes back on the expected build, which also keeps
// the fleet coordinator's concurrency slot occupied for the whole transition.
func (s *Store) MarkAgentUpdateAwaitingRestart(ctx context.Context, id int64, result string) error {
	if result == "" {
		result = "{}"
	}
	res, err := s.db.ExecContext(ctx, `update agent_tasks set result_json=?, updated_at=? where id=? and type=? and status='running'`, result, now(), id, model.AgentTaskTypeUpdateAgent)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("update_agent task is not running")
	}
	return nil
}

// ListRunningTasksByType returns running tasks of one type that have not been
// touched since olderThan. The caller decides what to do with them.
func (s *Store) ListRunningTasksByType(ctx context.Context, taskType string, olderThan time.Time) ([]model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, agentTaskSelectSQL+` where type=? and status='running' and updated_at < ? order by id`, taskType, olderThan.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// CountActiveAgentUpdates returns pending+running update_agent tasks.
func (s *Store) CountActiveAgentUpdates(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from agent_tasks where type=? and status in ('pending','running')`, model.AgentTaskTypeUpdateAgent).Scan(&count)
	return count, err
}

// CountOnlineEnrolledAgents returns online servers with a non-empty agent_id.
func (s *Store) CountOnlineEnrolledAgents(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from servers where status=? and agent_id is not null and agent_id<>''`, model.ServerOnline).Scan(&count)
	return count, err
}

// EnqueueUniqueAgentTask inserts one pending task unless an active task of the
// same type already exists for the server. All update_agent sources must use it.
func (s *Store) EnqueueUniqueAgentTask(ctx context.Context, v *model.AgentTask, staleBefore time.Time) (*model.AgentTask, bool, error) {
	if v == nil {
		return nil, false, errors.New("task is required")
	}
	if strings.TrimSpace(v.Type) == "" {
		return nil, false, errors.New("task type is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	ts := now()
	if !staleBefore.IsZero() {
		if _, err := tx.ExecContext(ctx, `update agent_tasks set status='failed', result_json=?, updated_at=?, completed_at=? where server_id=? and type=? and status in ('pending','running') and updated_at < ?`, `{"message":"更新任务超时，已允许重新创建"}`, ts, ts, v.ServerID, v.Type, staleBefore.UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, false, err
		}
	}
	rows, err := tx.QueryContext(ctx, agentTaskSelectSQL+` where server_id=? and type=? and status in ('pending','running') order by id desc limit 1`, v.ServerID, v.Type)
	if err != nil {
		return nil, false, err
	}
	existing, err := scanTasks(rows)
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if len(existing) > 0 {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return &existing[0], false, nil
	}
	if v.ResultJSON == "" {
		v.ResultJSON = "{}"
	}
	if v.Status == "" {
		v.Status = "pending"
	}
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	var completed any
	if isTerminalTaskStatus(v.Status) {
		completed = ts
		t := parseTime(ts)
		v.CompletedAt = &t
	}
	res, err := tx.ExecContext(ctx, `insert into agent_tasks(server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at) values(?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.Type, v.PayloadJSON, v.Status, v.ResultJSON, v.ConfigVersion, v.Nonce, ts, ts, completed)
	if err != nil {
		if IsSQLiteConstraint(err) {
			rows, lookupErr := tx.QueryContext(ctx, agentTaskSelectSQL+` where server_id=? and type=? and status in ('pending','running') order by id desc limit 1`, v.ServerID, v.Type)
			if lookupErr != nil {
				return nil, false, lookupErr
			}
			items, scanErr := scanTasks(rows)
			rows.Close()
			if scanErr != nil {
				return nil, false, scanErr
			}
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			if len(items) > 0 {
				return &items[0], false, nil
			}
		}
		return nil, false, err
	}
	v.ID, _ = res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (s *Store) GetAgentFleetState(ctx context.Context) (AgentFleetState, error) {
	var state AgentFleetState
	var paused int
	var rolling int
	var updated string
	err := s.db.QueryRowContext(ctx, `select paused,rolling,target_build,attempted,succeeded,failed,last_pause_reason,updated_at from agent_fleet_update_state where id=1`).Scan(&paused, &rolling, &state.TargetBuild, &state.Attempted, &state.Succeeded, &state.Failed, &state.LastPauseReason, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentFleetState{}, nil
	}
	if err != nil {
		return AgentFleetState{}, err
	}
	state.Paused = paused == 1
	state.Rolling = rolling == 1
	state.UpdatedAt = parseTime(updated)
	return state, nil
}

func (s *Store) SaveAgentFleetState(ctx context.Context, state AgentFleetState) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into agent_fleet_update_state(id,paused,rolling,target_build,attempted,succeeded,failed,last_pause_reason,updated_at) values(1,?,?,?,?,?,?,?,?) on conflict(id) do update set paused=excluded.paused,rolling=excluded.rolling,target_build=excluded.target_build,attempted=excluded.attempted,succeeded=excluded.succeeded,failed=excluded.failed,last_pause_reason=excluded.last_pause_reason,updated_at=excluded.updated_at`, boolInt(state.Paused), boolInt(state.Rolling), strings.TrimSpace(state.TargetBuild), state.Attempted, state.Succeeded, state.Failed, state.LastPauseReason, ts)
	return err
}

func (s *Store) GetAgentUpdateRetry(ctx context.Context, serverID int64, targetBuild string) (AgentUpdateRetry, error) {
	var item AgentUpdateRetry
	var next sql.NullString
	var updated string
	err := s.db.QueryRowContext(ctx, `select server_id,target_build,attempts,next_retry_at,last_error,updated_at from agent_update_retries where server_id=?`, serverID).Scan(&item.ServerID, &item.TargetBuild, &item.Attempts, &next, &item.LastError, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentUpdateRetry{ServerID: serverID, TargetBuild: strings.TrimSpace(targetBuild)}, nil
	}
	if err != nil {
		return AgentUpdateRetry{}, err
	}
	if strings.TrimSpace(item.TargetBuild) != strings.TrimSpace(targetBuild) {
		return AgentUpdateRetry{ServerID: serverID, TargetBuild: strings.TrimSpace(targetBuild)}, nil
	}
	if next.Valid && next.String != "" {
		t := parseTime(next.String)
		item.NextRetryAt = &t
	}
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) SaveAgentUpdateRetry(ctx context.Context, item AgentUpdateRetry) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into agent_update_retries(server_id,target_build,attempts,next_retry_at,last_error,updated_at) values(?,?,?,?,?,?) on conflict(server_id) do update set target_build=excluded.target_build,attempts=excluded.attempts,next_retry_at=excluded.next_retry_at,last_error=excluded.last_error,updated_at=excluded.updated_at`, item.ServerID, strings.TrimSpace(item.TargetBuild), item.Attempts, timePtrString(item.NextRetryAt), item.LastError, ts)
	return err
}

func (s *Store) ClearAgentUpdateRetry(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from agent_update_retries where server_id=?`, serverID)
	return err
}

func (s *Store) ClearAgentUpdateRetriesForBuild(ctx context.Context, targetBuild string) error {
	_, err := s.db.ExecContext(ctx, `delete from agent_update_retries where target_build=?`, strings.TrimSpace(targetBuild))
	return err
}

// RelayUpdateCandidate is the lightweight row used by relay rolling updates.
type RelayUpdateCandidate struct {
	ID    int64
	Build string
}

func (s *Store) ListRelayUpdateCandidates(ctx context.Context, targetBuild string, limit int) ([]RelayUpdateCandidate, error) {
	targetBuild = strings.TrimSpace(targetBuild)
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `select id, coalesce(build,'') from subscription_relays where token_hash<>'' and update_requested_at is null and coalesce(build,'')<>? order by id limit ?`, targetBuild, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RelayUpdateCandidate, 0, limit)
	for rows.Next() {
		var item RelayUpdateCandidate
		if err := rows.Scan(&item.ID, &item.Build); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CountActiveRelayUpdates(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from subscription_relays where update_requested_at is not null`).Scan(&count)
	return count, err
}
