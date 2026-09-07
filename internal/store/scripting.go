package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) CreateScript(ctx context.Context, item *model.Script) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into scripts(name,description,owner_user_id,status,created_at,updated_at) values(?,?,?,?,?,?)`,
		strings.TrimSpace(item.Name), strings.TrimSpace(item.Description), item.OwnerUserID, item.Status, ts, ts)
	if err != nil {
		if isUniqueConstraint(err) {
			return errors.New("script name already exists")
		}
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	item.UpdatedAt = item.CreatedAt
	return nil
}

func (s *Store) UpdateScript(ctx context.Context, item *model.Script, expectedUpdatedAt time.Time) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update scripts set name=?,description=?,status=?,updated_at=? where id=? and updated_at=?`,
		strings.TrimSpace(item.Name), strings.TrimSpace(item.Description), item.Status, ts, item.ID, expectedUpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return errors.New("script name already exists")
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("script revision conflict")
	}
	item.UpdatedAt = parseTime(ts)
	return nil
}

func (s *Store) GetScript(ctx context.Context, id int64) (model.Script, error) {
	return scanScript(s.db.QueryRowContext(ctx, `select id,name,description,owner_user_id,status,created_at,updated_at from scripts where id=?`, id))
}

func (s *Store) ListScripts(ctx context.Context, status string, limit int) ([]model.Script, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select id,name,description,owner_user_id,status,created_at,updated_at from scripts`
	args := []any{}
	if status != "" {
		query += ` where status=?`
		args = append(args, status)
	}
	query += ` order by updated_at desc, id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Script{}
	for rows.Next() {
		item, err := scanScript(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveScriptDraft(ctx context.Context, rev *model.ScriptRevision) error {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingID sql.NullInt64
	var existingNumber sql.NullInt64
	err = tx.QueryRowContext(ctx, `select id,revision_number from script_revisions where script_id=? and status='draft'`, rev.ScriptID).Scan(&existingID, &existingNumber)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existingID.Valid {
		if rev.ID != 0 && rev.ID != existingID.Int64 {
			return errors.New("script revision conflict")
		}
		_, err = tx.ExecContext(ctx, `update script_revisions set schema_version=?,runtime=?,sdk_version=?,source=?,source_digest=?,manifest_json=?,author_user_id=? where id=? and status='draft'`,
			rev.SchemaVersion, rev.Runtime, rev.SDKVersion, rev.Source, rev.SourceDigest, string(rev.ManifestJSON), rev.AuthorUserID, existingID.Int64)
		if err != nil {
			return err
		}
		rev.ID = existingID.Int64
		rev.RevisionNumber = existingNumber.Int64
	} else {
		var maxNumber int64
		_ = tx.QueryRowContext(ctx, `select coalesce(max(revision_number),0) from script_revisions where script_id=?`, rev.ScriptID).Scan(&maxNumber)
		rev.RevisionNumber = maxNumber + 1
		res, err := tx.ExecContext(ctx, `insert into script_revisions(script_id,revision_number,status,schema_version,runtime,sdk_version,source,source_digest,manifest_json,author_user_id,created_at) values(?,?,'draft',?,?,?,?,?,?,?,?)`,
			rev.ScriptID, rev.RevisionNumber, rev.SchemaVersion, rev.Runtime, rev.SDKVersion, rev.Source, rev.SourceDigest, string(rev.ManifestJSON), rev.AuthorUserID, ts)
		if err != nil {
			return err
		}
		rev.ID, _ = res.LastInsertId()
	}
	rev.Status = model.ScriptRevisionDraft
	rev.CreatedAt = parseTime(ts)
	_, _ = tx.ExecContext(ctx, `update scripts set updated_at=? where id=?`, ts, rev.ScriptID)
	return tx.Commit()
}

func (s *Store) PublishScriptRevision(ctx context.Context, scriptID, revisionID, publisherID int64) (model.ScriptRevision, error) {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ScriptRevision{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `update script_revisions set status='superseded' where script_id=? and status='published'`, scriptID); err != nil {
		return model.ScriptRevision{}, err
	}
	res, err := tx.ExecContext(ctx, `update script_revisions set status='published',published_at=?,published_by_user_id=? where id=? and script_id=? and status='draft'`, ts, publisherID, revisionID, scriptID)
	if err != nil {
		return model.ScriptRevision{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.ScriptRevision{}, errors.New("only a draft can be published")
	}
	if _, err := tx.ExecContext(ctx, `update scripts set status=case when status='draft' then 'enabled' else status end, updated_at=? where id=?`, ts, scriptID); err != nil {
		return model.ScriptRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ScriptRevision{}, err
	}
	return s.GetScriptRevision(ctx, revisionID)
}

func (s *Store) GetScriptRevision(ctx context.Context, id int64) (model.ScriptRevision, error) {
	return scanRevision(s.db.QueryRowContext(ctx, revisionSelect+` where id=?`, id))
}

func (s *Store) GetScriptDraft(ctx context.Context, scriptID int64) (model.ScriptRevision, error) {
	return scanRevision(s.db.QueryRowContext(ctx, revisionSelect+` where script_id=? and status='draft'`, scriptID))
}

func (s *Store) ListScriptRevisions(ctx context.Context, scriptID int64) ([]model.ScriptRevision, error) {
	rows, err := s.db.QueryContext(ctx, revisionSelect+` where script_id=? order by revision_number desc`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptRevision{}
	for rows.Next() {
		item, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreateScriptTrigger(ctx context.Context, item *model.ScriptTriggerBinding) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into script_trigger_bindings(script_id,revision_id,name,enabled,kind,spec_json,params_json,env_json,binding_revision,created_by_user_id,created_at,updated_at) values(?,?,?,?,?,?,?,?,1,?,?,?)`,
		item.ScriptID, item.RevisionID, strings.TrimSpace(item.Name), boolInt(item.Enabled), item.Kind, string(item.SpecJSON), string(orJSON(item.ParamsJSON)), string(orJSON(item.EnvJSON)), item.CreatedByUserID, ts, ts)
	if err != nil {
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.BindingRevision = 1
	item.CreatedAt = parseTime(ts)
	item.UpdatedAt = item.CreatedAt
	_, _ = s.db.ExecContext(ctx, `insert into script_trigger_states(binding_id,armed,updated_at) values(?,1,?)`, item.ID, ts)
	return nil
}

func (s *Store) UpdateScriptTrigger(ctx context.Context, item *model.ScriptTriggerBinding, expectedRevision int64) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update script_trigger_bindings set revision_id=?,name=?,enabled=?,kind=?,spec_json=?,params_json=?,env_json=?,binding_revision=binding_revision+1,updated_at=? where id=? and binding_revision=?`,
		item.RevisionID, strings.TrimSpace(item.Name), boolInt(item.Enabled), item.Kind, string(item.SpecJSON), string(orJSON(item.ParamsJSON)), string(orJSON(item.EnvJSON)), ts, item.ID, expectedRevision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("trigger revision conflict")
	}
	item.BindingRevision = expectedRevision + 1
	item.UpdatedAt = parseTime(ts)
	return nil
}

func (s *Store) GetScriptTrigger(ctx context.Context, id int64) (model.ScriptTriggerBinding, error) {
	return scanTrigger(s.db.QueryRowContext(ctx, triggerSelect+` where id=?`, id))
}

func (s *Store) ListScriptTriggers(ctx context.Context, scriptID int64) ([]model.ScriptTriggerBinding, error) {
	query := triggerSelect
	args := []any{}
	if scriptID > 0 {
		query += ` where script_id=?`
		args = append(args, scriptID)
	}
	query += ` order by updated_at desc, id desc`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptTriggerBinding{}
	for rows.Next() {
		item, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListEnabledEventTriggers(ctx context.Context, event string) ([]model.ScriptTriggerBinding, error) {
	rows, err := s.db.QueryContext(ctx, triggerSelect+` where enabled=1 and kind='event' and json_extract(spec_json,'$.event')=?`, event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptTriggerBinding{}
	for rows.Next() {
		item, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListDueScriptTriggers(ctx context.Context, until time.Time, limit int) ([]model.ScriptTriggerBinding, []model.ScriptTriggerState, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `select b.id,b.script_id,b.revision_id,b.name,b.enabled,b.kind,b.spec_json,b.params_json,b.env_json,b.binding_revision,b.created_by_user_id,b.created_at,b.updated_at,
		s.armed,s.condition_json,s.current_cycle_key,s.last_fired_at,s.last_skipped_at,s.last_skip_reason,s.next_due_at,s.hold_until,s.updated_at
		from script_trigger_states s join script_trigger_bindings b on b.id=s.binding_id
		where b.enabled=1 and b.kind in ('once','interval','cron') and s.next_due_at is not null and s.next_due_at<=?
		order by s.next_due_at limit ?`, until.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var bindings []model.ScriptTriggerBinding
	var states []model.ScriptTriggerState
	for rows.Next() {
		var b model.ScriptTriggerBinding
		var st model.ScriptTriggerState
		var enabled int
		var created, updated, stateUpdated string
		var lastFired, lastSkipped, nextDue, hold sql.NullString
		if err := rows.Scan(&b.ID, &b.ScriptID, &b.RevisionID, &b.Name, &enabled, &b.Kind, &b.SpecJSON, &b.ParamsJSON, &b.EnvJSON, &b.BindingRevision, &b.CreatedByUserID, &created, &updated,
			&st.Armed, &st.ConditionJSON, &st.CurrentCycleKey, &lastFired, &lastSkipped, &st.LastSkipReason, &nextDue, &hold, &stateUpdated); err != nil {
			return nil, nil, err
		}
		b.Enabled = enabled == 1
		b.CreatedAt = parseTime(created)
		b.UpdatedAt = parseTime(updated)
		st.BindingID = b.ID
		st.LastFiredAt = parseNullTime(lastFired)
		st.LastSkippedAt = parseNullTime(lastSkipped)
		st.NextDueAt = parseNullTime(nextDue)
		st.HoldUntil = parseNullTime(hold)
		st.UpdatedAt = parseTime(stateUpdated)
		bindings = append(bindings, b)
		states = append(states, st)
	}
	return bindings, states, rows.Err()
}

func (s *Store) UpsertScriptTriggerState(ctx context.Context, state model.ScriptTriggerState) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into script_trigger_states(binding_id,armed,condition_json,current_cycle_key,last_fired_at,last_skipped_at,last_skip_reason,next_due_at,hold_until,updated_at)
		values(?,?,?,?,?,?,?,?,?,?) on conflict(binding_id) do update set armed=excluded.armed,condition_json=excluded.condition_json,current_cycle_key=excluded.current_cycle_key,
		last_fired_at=excluded.last_fired_at,last_skipped_at=excluded.last_skipped_at,last_skip_reason=excluded.last_skip_reason,next_due_at=excluded.next_due_at,hold_until=excluded.hold_until,updated_at=excluded.updated_at`,
		state.BindingID, boolInt(state.Armed), string(orJSON(state.ConditionJSON)), state.CurrentCycleKey, nullTime(state.LastFiredAt), nullTime(state.LastSkippedAt), state.LastSkipReason, nullTime(state.NextDueAt), nullTime(state.HoldUntil), ts)
	return err
}

func (s *Store) GetScriptTriggerState(ctx context.Context, bindingID int64) (model.ScriptTriggerState, error) {
	var state model.ScriptTriggerState
	var lastFired, lastSkipped, nextDue, hold, updated sql.NullString
	err := s.db.QueryRowContext(ctx, `select binding_id,armed,condition_json,current_cycle_key,last_fired_at,last_skipped_at,last_skip_reason,next_due_at,hold_until,updated_at from script_trigger_states where binding_id=?`, bindingID).
		Scan(&state.BindingID, &state.Armed, &state.ConditionJSON, &state.CurrentCycleKey, &lastFired, &lastSkipped, &state.LastSkipReason, &nextDue, &hold, &updated)
	if err != nil {
		return model.ScriptTriggerState{}, err
	}
	state.LastFiredAt = parseNullTime(lastFired)
	state.LastSkippedAt = parseNullTime(lastSkipped)
	state.NextDueAt = parseNullTime(nextDue)
	state.HoldUntil = parseNullTime(hold)
	state.UpdatedAt = parseTime(updated.String)
	return state, nil
}

func (s *Store) CreateScriptGrant(ctx context.Context, item *model.ScriptGrant) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into script_grants(script_id,revision_id,binding_id,grant_revision,capabilities_json,resource_scope_json,constraints_json,source_digest,binding_digest,expires_at,approved_by_user_id,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.ScriptID, item.RevisionID, item.BindingID, item.GrantRevision, string(item.CapabilitiesJSON), string(item.ResourceScopeJSON), string(orJSON(item.ConstraintsJSON)), item.SourceDigest, item.BindingDigest, nullTime(item.ExpiresAt), item.ApprovedByUserID, ts)
	if err != nil {
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	return nil
}

func (s *Store) RevokeScriptGrant(ctx context.Context, id int64) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update script_grants set revoked_at=? where id=? and revoked_at is null`, ts, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("grant is not active")
	}
	return nil
}

func (s *Store) GetScriptGrant(ctx context.Context, id int64) (model.ScriptGrant, error) {
	return scanGrant(s.db.QueryRowContext(ctx, grantSelect+` where id=?`, id))
}

func (s *Store) ListScriptGrants(ctx context.Context, scriptID int64) ([]model.ScriptGrant, error) {
	query := grantSelect
	args := []any{}
	if scriptID > 0 {
		query += ` where script_id=?`
		args = append(args, scriptID)
	}
	query += ` order by id desc`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptGrant{}
	for rows.Next() {
		item, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListActiveScriptGrants(ctx context.Context, scriptID, revisionID int64, bindingID *int64) ([]model.ScriptGrant, error) {
	query := grantSelect + ` where script_id=? and revision_id=? and revoked_at is null and (expires_at is null or expires_at>?)`
	args := []any{scriptID, revisionID, now()}
	if bindingID != nil {
		query += ` and (binding_id is null or binding_id=?)`
		args = append(args, *bindingID)
	}
	query += ` order by id desc`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptGrant{}
	for rows.Next() {
		item, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreateScriptRun(ctx context.Context, item *model.ScriptRun) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into script_runs(uuid,script_id,revision_id,binding_id,grant_id,caller_principal,trigger_kind,idempotency_key,status,mode,snapshot_json,result_json,error_code,skip_reason,lease_generation,recovery_generation,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,
		item.UUID, item.ScriptID, item.RevisionID, item.BindingID, item.GrantID, item.CallerPrincipal, item.TriggerKind, item.IdempotencyKey, item.Status, item.Mode, string(orJSON(item.SnapshotJSON)), string(orJSON(item.ResultJSON)), item.ErrorCode, item.SkipReason, item.RecoveryGen, ts)
	if err != nil {
		if isUniqueConstraint(err) {
			existing, getErr := s.GetScriptRunByIdempotency(ctx, item.IdempotencyKey)
			if getErr == nil {
				*item = existing
				return errScriptRunIdempotent
			}
			return errors.New("idempotency conflict")
		}
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	return nil
}

var errScriptRunIdempotent = errors.New("script run already exists")

func IsScriptRunIdempotent(err error) bool {
	return errors.Is(err, errScriptRunIdempotent)
}

func (s *Store) GetScriptRun(ctx context.Context, id int64) (model.ScriptRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` where id=?`, id))
}

func (s *Store) GetScriptRunByUUID(ctx context.Context, uuid string) (model.ScriptRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` where uuid=?`, uuid))
}

func (s *Store) GetScriptRunByIdempotency(ctx context.Context, key string) (model.ScriptRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` where idempotency_key=?`, key))
}

func (s *Store) ListScriptRuns(ctx context.Context, scriptID int64, limit int) ([]model.ScriptRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := runSelect
	args := []any{}
	if scriptID > 0 {
		query += ` where script_id=?`
		args = append(args, scriptID)
	}
	query += ` order by id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptRun{}
	for rows.Next() {
		item, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CountScriptRuns(ctx context.Context, statuses ...string) (int, error) {
	query := `select count(*) from script_runs`
	args := []any{}
	if len(statuses) > 0 {
		query += ` where status in (` + placeholders(len(statuses)) + `)`
		for _, status := range statuses {
			args = append(args, status)
		}
	}
	var n int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

func (s *Store) CountActiveScriptRuns(ctx context.Context, scriptID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from script_runs where script_id=? and status in ('queued','running')`, scriptID).Scan(&n)
	return n, err
}

func (s *Store) LeaseScriptRun(ctx context.Context, workerID string, until time.Time, recoveryGen int64) (model.ScriptRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ScriptRun{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, runSelect+` where status='queued' and mode in ('live','simulate') and recovery_generation=? and (lease_until is null or lease_until<?) order by id limit 1`, recoveryGen, now())
	item, err := scanRun(row)
	if err != nil {
		return model.ScriptRun{}, err
	}
	item.LeaseGeneration++
	ts := now()
	res, err := tx.ExecContext(ctx, `update script_runs set status='running',lease_owner=?,lease_generation=?,lease_until=?,started_at=coalesce(started_at,?) where id=? and status='queued' and lease_generation=?`,
		workerID, item.LeaseGeneration, until.UTC().Format(time.RFC3339Nano), ts, item.ID, item.LeaseGeneration-1)
	if err != nil {
		return model.ScriptRun{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.ScriptRun{}, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `insert into script_run_attempts(run_id,generation,worker_id,status,started_at) values(?,?,?,'running',?)`, item.ID, item.LeaseGeneration, workerID, ts); err != nil {
		return model.ScriptRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ScriptRun{}, err
	}
	item.Status = model.ScriptRunRunning
	item.LeaseOwner = workerID
	started := parseTime(ts)
	item.StartedAt = &started
	untilCopy := until.UTC()
	item.LeaseUntil = &untilCopy
	return item, nil
}

func (s *Store) FinishScriptRun(ctx context.Context, id int64, generation int64, status, errorCode string, result json.RawMessage) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update script_runs set status=?,error_code=?,result_json=?,finished_at=?,lease_until=null where id=? and lease_generation=? and status='running'`,
		status, errorCode, string(orJSON(result)), ts, id, generation)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("run lease is stale")
	}
	_, err = s.db.ExecContext(ctx, `update script_run_attempts set status=?,error_code=?,finished_at=? where run_id=? and generation=?`, status, errorCode, ts, id, generation)
	return err
}

func (s *Store) CancelScriptRun(ctx context.Context, id int64) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `update script_runs set status='cancelled',error_code='cancelled',finished_at=? where id=? and status in ('queued','running')`, ts, id)
	return err
}

func (s *Store) CreateScriptRunAction(ctx context.Context, item *model.ScriptRunAction) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into script_run_actions(run_id,action_key,capability,target_json,payload_digest,status,operation_id,changeset_id,task_id,result_json,error_code,lease_generation,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.RunID, item.ActionKey, item.Capability, string(orJSON(item.TargetJSON)), item.PayloadDigest, item.Status, item.OperationID, item.ChangesetID, item.TaskID, string(orJSON(item.ResultJSON)), item.ErrorCode, item.LeaseGeneration, ts, ts)
	if err != nil {
		if isUniqueConstraint(err) {
			existing, getErr := s.GetScriptRunAction(ctx, item.RunID, item.ActionKey)
			if getErr == nil {
				if existing.PayloadDigest != item.PayloadDigest {
					return errors.New("idempotency_conflict")
				}
				*item = existing
				return nil
			}
		}
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	item.UpdatedAt = item.CreatedAt
	return nil
}

func (s *Store) GetScriptRunAction(ctx context.Context, runID int64, actionKey string) (model.ScriptRunAction, error) {
	return scanAction(s.db.QueryRowContext(ctx, actionSelect+` where run_id=? and action_key=?`, runID, actionKey))
}

func (s *Store) GetScriptRunActionByOperationID(ctx context.Context, operationID string) (model.ScriptRunAction, error) {
	return scanAction(s.db.QueryRowContext(ctx, actionSelect+` where operation_id=? order by id desc limit 1`, operationID))
}

func (s *Store) ListScriptRunActions(ctx context.Context, runID int64) ([]model.ScriptRunAction, error) {
	rows, err := s.db.QueryContext(ctx, actionSelect+` where run_id=? order by id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptRunAction{}
	for rows.Next() {
		item, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpdateScriptRunAction(ctx context.Context, item model.ScriptRunAction) error {
	_, err := s.db.ExecContext(ctx, `update script_run_actions set status=?,operation_id=?,changeset_id=?,task_id=?,result_json=?,error_code=?,updated_at=? where id=?`,
		item.Status, item.OperationID, item.ChangesetID, item.TaskID, string(orJSON(item.ResultJSON)), item.ErrorCode, now(), item.ID)
	return err
}

func (s *Store) AppendScriptRunLogs(ctx context.Context, runID int64, logs []model.ScriptRunLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `insert or ignore into script_run_logs(run_id,seq,level,message,fields_json,created_at) values(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range logs {
		if _, err := stmt.ExecContext(ctx, runID, item.Seq, item.Level, item.Message, string(orJSON(item.FieldsJSON)), now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListScriptRunLogs(ctx context.Context, runID int64, afterSeq int64, limit int) ([]model.ScriptRunLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `select id,run_id,seq,level,message,fields_json,created_at from script_run_logs where run_id=? and seq>? order by seq limit ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ScriptRunLog{}
	for rows.Next() {
		var item model.ScriptRunLog
		var created, fields string
		if err := rows.Scan(&item.ID, &item.RunID, &item.Seq, &item.Level, &item.Message, &fields, &created); err != nil {
			return nil, err
		}
		item.FieldsJSON = jsonBytes(fields)
		item.CreatedAt = parseTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetScriptState(ctx context.Context, scriptID int64, key string) (model.ScriptStateEntry, error) {
	var item model.ScriptStateEntry
	var updated, value string
	err := s.db.QueryRowContext(ctx, `select script_id,key,value_json,version,updated_at from script_state where script_id=? and key=?`, scriptID, key).
		Scan(&item.ScriptID, &item.Key, &value, &item.Version, &updated)
	item.ValueJSON = jsonBytes(value)
	if err != nil {
		return model.ScriptStateEntry{}, err
	}
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) CompareAndSetScriptState(ctx context.Context, scriptID int64, key string, expectedVersion int64, value json.RawMessage) (model.ScriptStateEntry, error) {
	ts := now()
	if expectedVersion == 0 {
		_, err := s.db.ExecContext(ctx, `insert into script_state(script_id,key,value_json,version,updated_at) values(?,?,?,1,?)`, scriptID, key, string(orJSON(value)), ts)
		if err != nil {
			return model.ScriptStateEntry{}, err
		}
		return s.GetScriptState(ctx, scriptID, key)
	}
	res, err := s.db.ExecContext(ctx, `update script_state set value_json=?,version=version+1,updated_at=? where script_id=? and key=? and version=?`, string(orJSON(value)), ts, scriptID, key, expectedVersion)
	if err != nil {
		return model.ScriptStateEntry{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.ScriptStateEntry{}, errors.New("condition_changed")
	}
	return s.GetScriptState(ctx, scriptID, key)
}

func (s *Store) CountScriptStateKeys(ctx context.Context, scriptID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from script_state where script_id=?`, scriptID).Scan(&n)
	return n, err
}

func (s *Store) EnqueueScriptEvent(ctx context.Context, topic, aggregateID string, payload json.RawMessage) error {
	id := fmt.Sprintf("evt_%d_%s", time.Now().UTC().UnixNano(), aggregateID)
	_, err := s.db.ExecContext(ctx, `insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values(?,?,?,?,'pending',?,?)`,
		id, topic, aggregateID, string(orJSON(payload)), now(), now())
	return err
}

func (s *Store) EnqueueScriptEventTx(ctx context.Context, tx *sql.Tx, topic, aggregateID string, payload json.RawMessage) error {
	id := fmt.Sprintf("evt_%d_%s", time.Now().UTC().UnixNano(), aggregateID)
	_, err := tx.ExecContext(ctx, `insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values(?,?,?,?,'pending',?,?)`,
		id, topic, aggregateID, string(orJSON(payload)), now(), now())
	return err
}

func (s *Store) ClaimScriptEvents(ctx context.Context, owner string, until time.Time, limit int) ([]EventOutboxItem, error) {
	if limit <= 0 {
		limit = 32
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id,topic,aggregate_id,payload_json,attempts,created_at from event_outbox where status='pending' and available_at<=? and topic like 'script.%' order by created_at limit ?`, now(), limit)
	if err != nil {
		return nil, err
	}
	var items []EventOutboxItem
	for rows.Next() {
		var item EventOutboxItem
		var created, payload string
		if err := rows.Scan(&item.ID, &item.Topic, &item.AggregateID, &payload, &item.Attempts, &created); err != nil {
			rows.Close()
			return nil, err
		}
		item.Payload = jsonBytes(payload)
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `update event_outbox set status='leased',lease_owner=?,lease_until=?,attempts=attempts+1 where id=? and status='pending'`, owner, until.UTC().Format(time.RFC3339Nano), item.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) CompleteScriptEvent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `update event_outbox set status='completed',completed_at=? where id=?`, now(), id)
	return err
}

type EventOutboxItem struct {
	ID          string
	Topic       string
	AggregateID string
	Payload     json.RawMessage
	Attempts    int
	CreatedAt   time.Time
}

func (s *Store) GetServerScriptPolicy(ctx context.Context, serverID int64) (model.ServerScriptPolicy, error) {
	var item model.ServerScriptPolicy
	var created, updated string
	var scriptsEnabled, powerEnabled int
	err := s.db.QueryRowContext(ctx, `select server_id,scripts_enabled,scripts_power_enabled,created_at,updated_at from server_script_policies where server_id=?`, serverID).
		Scan(&item.ServerID, &scriptsEnabled, &powerEnabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ServerScriptPolicy{ServerID: serverID}, nil
	}
	if err != nil {
		return model.ServerScriptPolicy{}, err
	}
	item.ScriptsEnabled = scriptsEnabled == 1
	item.ScriptsPowerEnabled = powerEnabled == 1
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) UpsertServerScriptPolicy(ctx context.Context, item model.ServerScriptPolicy) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into server_script_policies(server_id,scripts_enabled,scripts_power_enabled,created_at,updated_at) values(?,?,?,?,?)
		on conflict(server_id) do update set scripts_enabled=excluded.scripts_enabled,scripts_power_enabled=excluded.scripts_power_enabled,updated_at=excluded.updated_at`,
		item.ServerID, boolInt(item.ScriptsEnabled), boolInt(item.ScriptsPowerEnabled), ts, ts)
	return err
}

func (s *Store) PauseScriptSchedulerAfterRestore(ctx context.Context) error {
	var current string
	_ = s.db.QueryRowContext(ctx, `select value from app_settings where key='scripts.recovery_generation'`).Scan(&current)
	next := 1
	fmt.Sscanf(current, "%d", &next)
	next++
	return s.SetSettings(ctx, map[string]string{
		"scripts.scheduler_paused":    "true",
		"scripts.recovery_generation": fmt.Sprintf("%d", next),
	})
}

const revisionSelect = `select id,script_id,revision_number,status,schema_version,runtime,sdk_version,source,source_digest,manifest_json,author_user_id,published_at,published_by_user_id,created_at from script_revisions`
const triggerSelect = `select id,script_id,revision_id,name,enabled,kind,spec_json,params_json,env_json,binding_revision,created_by_user_id,created_at,updated_at from script_trigger_bindings`
const grantSelect = `select id,script_id,revision_id,binding_id,grant_revision,capabilities_json,resource_scope_json,constraints_json,source_digest,binding_digest,expires_at,revoked_at,approved_by_user_id,created_at from script_grants`
const runSelect = `select id,uuid,script_id,revision_id,binding_id,grant_id,caller_principal,trigger_kind,idempotency_key,status,mode,snapshot_json,result_json,error_code,skip_reason,lease_owner,lease_generation,lease_until,recovery_generation,created_at,started_at,finished_at from script_runs`
const actionSelect = `select id,run_id,action_key,capability,target_json,payload_digest,status,operation_id,changeset_id,task_id,result_json,error_code,lease_generation,created_at,updated_at from script_run_actions`

func scanScript(row incidentRowScanner) (model.Script, error) {
	var item model.Script
	var created, updated string
	if err := row.Scan(&item.ID, &item.Name, &item.Description, &item.OwnerUserID, &item.Status, &created, &updated); err != nil {
		return model.Script{}, err
	}
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func scanRevision(row incidentRowScanner) (model.ScriptRevision, error) {
	var item model.ScriptRevision
	var published, created sql.NullString
	var publishedBy sql.NullInt64
	var manifest string
	if err := row.Scan(&item.ID, &item.ScriptID, &item.RevisionNumber, &item.Status, &item.SchemaVersion, &item.Runtime, &item.SDKVersion, &item.Source, &item.SourceDigest, &manifest, &item.AuthorUserID, &published, &publishedBy, &created); err != nil {
		return model.ScriptRevision{}, err
	}
	item.ManifestJSON = jsonBytes(manifest)
	item.PublishedAt = parseNullTime(published)
	if publishedBy.Valid {
		item.PublishedByUserID = &publishedBy.Int64
	}
	item.CreatedAt = parseTime(created.String)
	return item, nil
}

func scanTrigger(row incidentRowScanner) (model.ScriptTriggerBinding, error) {
	var item model.ScriptTriggerBinding
	var enabled int
	var created, updated string
	var spec, params, env string
	if err := row.Scan(&item.ID, &item.ScriptID, &item.RevisionID, &item.Name, &enabled, &item.Kind, &spec, &params, &env, &item.BindingRevision, &item.CreatedByUserID, &created, &updated); err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	item.SpecJSON = jsonBytes(spec)
	item.ParamsJSON = jsonBytes(params)
	item.EnvJSON = jsonBytes(env)
	item.Enabled = enabled == 1
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func scanGrant(row incidentRowScanner) (model.ScriptGrant, error) {
	var item model.ScriptGrant
	var binding sql.NullInt64
	var expires, revoked, created sql.NullString
	var capabilities, scope, constraints string
	if err := row.Scan(&item.ID, &item.ScriptID, &item.RevisionID, &binding, &item.GrantRevision, &capabilities, &scope, &constraints, &item.SourceDigest, &item.BindingDigest, &expires, &revoked, &item.ApprovedByUserID, &created); err != nil {
		return model.ScriptGrant{}, err
	}
	item.CapabilitiesJSON = jsonBytes(capabilities)
	item.ResourceScopeJSON = jsonBytes(scope)
	item.ConstraintsJSON = jsonBytes(constraints)
	if binding.Valid {
		item.BindingID = &binding.Int64
	}
	item.ExpiresAt = parseNullTime(expires)
	item.RevokedAt = parseNullTime(revoked)
	item.CreatedAt = parseTime(created.String)
	return item, nil
}

func scanRun(row incidentRowScanner) (model.ScriptRun, error) {
	var item model.ScriptRun
	var binding, grant sql.NullInt64
	var leaseUntil, created, started, finished sql.NullString
	var snapshot, result string
	if err := row.Scan(&item.ID, &item.UUID, &item.ScriptID, &item.RevisionID, &binding, &grant, &item.CallerPrincipal, &item.TriggerKind, &item.IdempotencyKey, &item.Status, &item.Mode, &snapshot, &result, &item.ErrorCode, &item.SkipReason, &item.LeaseOwner, &item.LeaseGeneration, &leaseUntil, &item.RecoveryGen, &created, &started, &finished); err != nil {
		return model.ScriptRun{}, err
	}
	item.SnapshotJSON = jsonBytes(snapshot)
	item.ResultJSON = jsonBytes(result)
	if binding.Valid {
		item.BindingID = &binding.Int64
	}
	if grant.Valid {
		item.GrantID = &grant.Int64
	}
	item.LeaseUntil = parseNullTime(leaseUntil)
	item.CreatedAt = parseTime(created.String)
	item.StartedAt = parseNullTime(started)
	item.FinishedAt = parseNullTime(finished)
	return item, nil
}

func scanAction(row incidentRowScanner) (model.ScriptRunAction, error) {
	var item model.ScriptRunAction
	var task sql.NullInt64
	var created, updated string
	var target, result string
	if err := row.Scan(&item.ID, &item.RunID, &item.ActionKey, &item.Capability, &target, &item.PayloadDigest, &item.Status, &item.OperationID, &item.ChangesetID, &task, &result, &item.ErrorCode, &item.LeaseGeneration, &created, &updated); err != nil {
		return model.ScriptRunAction{}, err
	}
	item.TargetJSON = jsonBytes(target)
	item.ResultJSON = jsonBytes(result)
	if task.Valid {
		item.TaskID = &task.Int64
	}
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func orJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func jsonBytes(v string) json.RawMessage {
	if strings.TrimSpace(v) == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(v)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
