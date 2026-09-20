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

func (s *Store) CreatePlugin(ctx context.Context, item *model.Plugin) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugins(name,description,owner_user_id,status,created_at,updated_at) values(?,?,?,?,?,?)`,
		strings.TrimSpace(item.Name), strings.TrimSpace(item.Description), item.OwnerUserID, item.Status, ts, ts)
	if err != nil {
		if isUniqueConstraint(err) {
			return errors.New("plugin name already exists")
		}
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	item.UpdatedAt = item.CreatedAt
	return nil
}

func (s *Store) UpdatePlugin(ctx context.Context, item *model.Plugin, expectedUpdatedAt time.Time) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update plugins set name=?,description=?,status=?,updated_at=? where id=? and updated_at=?`,
		strings.TrimSpace(item.Name), strings.TrimSpace(item.Description), item.Status, ts, item.ID, expectedUpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return errors.New("plugin name already exists")
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("plugin revision conflict")
	}
	item.UpdatedAt = parseTime(ts)
	return nil
}

func (s *Store) GetPlugin(ctx context.Context, id int64) (model.Plugin, error) {
	return scanPlugin(s.db.QueryRowContext(ctx, `select id,name,description,owner_user_id,status,created_at,updated_at from plugins where id=?`, id))
}

func (s *Store) ListPlugins(ctx context.Context, status string, limit int) ([]model.Plugin, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select id,name,description,owner_user_id,status,created_at,updated_at from plugins`
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
	out := []model.Plugin{}
	for rows.Next() {
		item, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SavePluginDraft(ctx context.Context, rev *model.PluginRevision) error {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingID sql.NullInt64
	var existingNumber sql.NullInt64
	err = tx.QueryRowContext(ctx, `select id,revision_number from plugin_revisions where plugin_id=? and status='draft'`, rev.PluginID).Scan(&existingID, &existingNumber)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existingID.Valid {
		if rev.ID != 0 && rev.ID != existingID.Int64 {
			return errors.New("plugin revision conflict")
		}
		_, err = tx.ExecContext(ctx, `update plugin_revisions set schema_version=?,runtime=?,sdk_version=?,source=?,source_digest=?,manifest_json=?,author_user_id=? where id=? and status='draft'`,
			rev.SchemaVersion, rev.Runtime, rev.SDKVersion, rev.Source, rev.SourceDigest, string(rev.ManifestJSON), rev.AuthorUserID, existingID.Int64)
		if err != nil {
			return err
		}
		rev.ID = existingID.Int64
		rev.RevisionNumber = existingNumber.Int64
	} else {
		var maxNumber int64
		_ = tx.QueryRowContext(ctx, `select coalesce(max(revision_number),0) from plugin_revisions where plugin_id=?`, rev.PluginID).Scan(&maxNumber)
		rev.RevisionNumber = maxNumber + 1
		res, err := tx.ExecContext(ctx, `insert into plugin_revisions(plugin_id,revision_number,status,schema_version,runtime,sdk_version,source,source_digest,manifest_json,author_user_id,created_at) values(?,?,'draft',?,?,?,?,?,?,?,?)`,
			rev.PluginID, rev.RevisionNumber, rev.SchemaVersion, rev.Runtime, rev.SDKVersion, rev.Source, rev.SourceDigest, string(rev.ManifestJSON), rev.AuthorUserID, ts)
		if err != nil {
			return err
		}
		rev.ID, _ = res.LastInsertId()
	}
	rev.Status = model.PluginRevisionDraft
	rev.CreatedAt = parseTime(ts)
	_, _ = tx.ExecContext(ctx, `update plugins set updated_at=? where id=?`, ts, rev.PluginID)
	return tx.Commit()
}

func (s *Store) PublishPluginRevision(ctx context.Context, pluginID, revisionID, publisherID int64) (model.PluginRevision, error) {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.PluginRevision{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `update plugin_revisions set status='superseded' where plugin_id=? and status='published'`, pluginID); err != nil {
		return model.PluginRevision{}, err
	}
	res, err := tx.ExecContext(ctx, `update plugin_revisions set status='published',published_at=?,published_by_user_id=? where id=? and plugin_id=? and status='draft'`, ts, publisherID, revisionID, pluginID)
	if err != nil {
		return model.PluginRevision{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.PluginRevision{}, errors.New("only a draft can be published")
	}
	if _, err := tx.ExecContext(ctx, `update plugins set status=case when status='draft' then 'enabled' else status end, updated_at=? where id=?`, ts, pluginID); err != nil {
		return model.PluginRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.PluginRevision{}, err
	}
	return s.GetPluginRevision(ctx, revisionID)
}

func (s *Store) GetPluginRevision(ctx context.Context, id int64) (model.PluginRevision, error) {
	return scanRevision(s.db.QueryRowContext(ctx, revisionSelect+` where id=?`, id))
}

func (s *Store) GetPluginDraft(ctx context.Context, pluginID int64) (model.PluginRevision, error) {
	return scanRevision(s.db.QueryRowContext(ctx, revisionSelect+` where plugin_id=? and status='draft'`, pluginID))
}

func (s *Store) ListPluginRevisions(ctx context.Context, pluginID int64) ([]model.PluginRevision, error) {
	rows, err := s.db.QueryContext(ctx, revisionSelect+` where plugin_id=? order by revision_number desc`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginRevision{}
	for rows.Next() {
		item, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreatePluginTrigger(ctx context.Context, item *model.PluginTriggerBinding) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugin_trigger_bindings(plugin_id,revision_id,name,enabled,kind,spec_json,params_json,env_json,binding_revision,created_by_user_id,created_at,updated_at) values(?,?,?,?,?,?,?,?,1,?,?,?)`,
		item.PluginID, item.RevisionID, strings.TrimSpace(item.Name), boolInt(item.Enabled), item.Kind, string(item.SpecJSON), string(orJSON(item.ParamsJSON)), string(orJSON(item.EnvJSON)), item.CreatedByUserID, ts, ts)
	if err != nil {
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.BindingRevision = 1
	item.CreatedAt = parseTime(ts)
	item.UpdatedAt = item.CreatedAt
	_, _ = s.db.ExecContext(ctx, `insert into plugin_trigger_states(binding_id,armed,updated_at) values(?,1,?)`, item.ID, ts)
	return nil
}

func (s *Store) UpdatePluginTrigger(ctx context.Context, item *model.PluginTriggerBinding, expectedRevision int64) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update plugin_trigger_bindings set revision_id=?,name=?,enabled=?,kind=?,spec_json=?,params_json=?,env_json=?,binding_revision=binding_revision+1,updated_at=? where id=? and binding_revision=?`,
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

func (s *Store) GetPluginTrigger(ctx context.Context, id int64) (model.PluginTriggerBinding, error) {
	return scanTrigger(s.db.QueryRowContext(ctx, triggerSelect+` where id=?`, id))
}

func (s *Store) ListPluginTriggers(ctx context.Context, pluginID int64) ([]model.PluginTriggerBinding, error) {
	query := triggerSelect
	args := []any{}
	if pluginID > 0 {
		query += ` where plugin_id=?`
		args = append(args, pluginID)
	}
	query += ` order by updated_at desc, id desc`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginTriggerBinding{}
	for rows.Next() {
		item, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListEnabledEventTriggers(ctx context.Context, event string) ([]model.PluginTriggerBinding, error) {
	rows, err := s.db.QueryContext(ctx, triggerSelect+` where enabled=1 and kind='event' and json_extract(spec_json,'$.event')=?`, event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginTriggerBinding{}
	for rows.Next() {
		item, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListDuePluginTriggers(ctx context.Context, until time.Time, limit int) ([]model.PluginTriggerBinding, []model.PluginTriggerState, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `select b.id,b.plugin_id,b.revision_id,b.name,b.enabled,b.kind,b.spec_json,b.params_json,b.env_json,b.binding_revision,b.created_by_user_id,b.created_at,b.updated_at,
		s.armed,s.condition_json,s.current_cycle_key,s.last_fired_at,s.last_skipped_at,s.last_skip_reason,s.next_due_at,s.hold_until,s.updated_at
		from plugin_trigger_states s join plugin_trigger_bindings b on b.id=s.binding_id
		where b.enabled=1 and b.kind in ('once','interval','cron') and s.next_due_at is not null and s.next_due_at<=?
		order by s.next_due_at limit ?`, until.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var bindings []model.PluginTriggerBinding
	var states []model.PluginTriggerState
	for rows.Next() {
		var b model.PluginTriggerBinding
		var st model.PluginTriggerState
		var enabled int
		var created, updated, stateUpdated string
		var lastFired, lastSkipped, nextDue, hold sql.NullString
		if err := rows.Scan(&b.ID, &b.PluginID, &b.RevisionID, &b.Name, &enabled, &b.Kind, &b.SpecJSON, &b.ParamsJSON, &b.EnvJSON, &b.BindingRevision, &b.CreatedByUserID, &created, &updated,
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

func (s *Store) UpsertPluginTriggerState(ctx context.Context, state model.PluginTriggerState) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into plugin_trigger_states(binding_id,armed,condition_json,current_cycle_key,last_fired_at,last_skipped_at,last_skip_reason,next_due_at,hold_until,updated_at)
		values(?,?,?,?,?,?,?,?,?,?) on conflict(binding_id) do update set armed=excluded.armed,condition_json=excluded.condition_json,current_cycle_key=excluded.current_cycle_key,
		last_fired_at=excluded.last_fired_at,last_skipped_at=excluded.last_skipped_at,last_skip_reason=excluded.last_skip_reason,next_due_at=excluded.next_due_at,hold_until=excluded.hold_until,updated_at=excluded.updated_at`,
		state.BindingID, boolInt(state.Armed), string(orJSON(state.ConditionJSON)), state.CurrentCycleKey, nullTime(state.LastFiredAt), nullTime(state.LastSkippedAt), state.LastSkipReason, nullTime(state.NextDueAt), nullTime(state.HoldUntil), ts)
	return err
}

func (s *Store) GetPluginTriggerState(ctx context.Context, bindingID int64) (model.PluginTriggerState, error) {
	var state model.PluginTriggerState
	var lastFired, lastSkipped, nextDue, hold, updated sql.NullString
	err := s.db.QueryRowContext(ctx, `select binding_id,armed,condition_json,current_cycle_key,last_fired_at,last_skipped_at,last_skip_reason,next_due_at,hold_until,updated_at from plugin_trigger_states where binding_id=?`, bindingID).
		Scan(&state.BindingID, &state.Armed, &state.ConditionJSON, &state.CurrentCycleKey, &lastFired, &lastSkipped, &state.LastSkipReason, &nextDue, &hold, &updated)
	if err != nil {
		return model.PluginTriggerState{}, err
	}
	state.LastFiredAt = parseNullTime(lastFired)
	state.LastSkippedAt = parseNullTime(lastSkipped)
	state.NextDueAt = parseNullTime(nextDue)
	state.HoldUntil = parseNullTime(hold)
	state.UpdatedAt = parseTime(updated.String)
	return state, nil
}

func (s *Store) CreatePluginGrant(ctx context.Context, item *model.PluginGrant) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugin_grants(plugin_id,revision_id,binding_id,grant_revision,capabilities_json,resource_scope_json,constraints_json,source_digest,binding_digest,expires_at,approved_by_user_id,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.PluginID, item.RevisionID, item.BindingID, item.GrantRevision, string(item.CapabilitiesJSON), string(item.ResourceScopeJSON), string(orJSON(item.ConstraintsJSON)), item.SourceDigest, item.BindingDigest, nullTime(item.ExpiresAt), item.ApprovedByUserID, ts)
	if err != nil {
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	return nil
}

func (s *Store) RevokePluginGrant(ctx context.Context, id int64) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update plugin_grants set revoked_at=? where id=? and revoked_at is null`, ts, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("grant is not active")
	}
	return nil
}

func (s *Store) GetPluginGrant(ctx context.Context, id int64) (model.PluginGrant, error) {
	return scanGrant(s.db.QueryRowContext(ctx, grantSelect+` where id=?`, id))
}

func (s *Store) ListPluginGrants(ctx context.Context, pluginID int64) ([]model.PluginGrant, error) {
	query := grantSelect
	args := []any{}
	if pluginID > 0 {
		query += ` where plugin_id=?`
		args = append(args, pluginID)
	}
	query += ` order by id desc`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginGrant{}
	for rows.Next() {
		item, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListActivePluginGrants(ctx context.Context, pluginID, revisionID int64, bindingID *int64) ([]model.PluginGrant, error) {
	query := grantSelect + ` where plugin_id=? and revision_id=? and revoked_at is null and (expires_at is null or expires_at>?)`
	args := []any{pluginID, revisionID, now()}
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
	out := []model.PluginGrant{}
	for rows.Next() {
		item, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreatePluginRun(ctx context.Context, item *model.PluginRun) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugin_runs(uuid,plugin_id,revision_id,binding_id,grant_id,caller_principal,trigger_kind,idempotency_key,status,mode,snapshot_json,result_json,error_code,skip_reason,lease_generation,recovery_generation,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,
		item.UUID, item.PluginID, item.RevisionID, item.BindingID, item.GrantID, item.CallerPrincipal, item.TriggerKind, item.IdempotencyKey, item.Status, item.Mode, string(orJSON(item.SnapshotJSON)), string(orJSON(item.ResultJSON)), item.ErrorCode, item.SkipReason, item.RecoveryGen, ts)
	if err != nil {
		if isUniqueConstraint(err) {
			existing, getErr := s.GetPluginRunByIdempotency(ctx, item.IdempotencyKey)
			if getErr == nil {
				*item = existing
				return errPluginRunIdempotent
			}
			return errors.New("idempotency conflict")
		}
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt = parseTime(ts)
	return nil
}

var errPluginRunIdempotent = errors.New("plugin run already exists")

func IsPluginRunIdempotent(err error) bool {
	return errors.Is(err, errPluginRunIdempotent)
}

func (s *Store) GetPluginRun(ctx context.Context, id int64) (model.PluginRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` where id=?`, id))
}

func (s *Store) GetPluginRunByUUID(ctx context.Context, uuid string) (model.PluginRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` where uuid=?`, uuid))
}

func (s *Store) GetPluginRunByIdempotency(ctx context.Context, key string) (model.PluginRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` where idempotency_key=?`, key))
}

func (s *Store) ListPluginRuns(ctx context.Context, pluginID int64, limit int) ([]model.PluginRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := runSelect
	args := []any{}
	if pluginID > 0 {
		query += ` where plugin_id=?`
		args = append(args, pluginID)
	}
	query += ` order by id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginRun{}
	for rows.Next() {
		item, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CountPluginRuns(ctx context.Context, statuses ...string) (int, error) {
	query := `select count(*) from plugin_runs`
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

func (s *Store) CountActivePluginRuns(ctx context.Context, pluginID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from plugin_runs where plugin_id=? and status in ('queued','running')`, pluginID).Scan(&n)
	return n, err
}

func (s *Store) LeasePluginRun(ctx context.Context, workerID string, until time.Time, recoveryGen int64) (model.PluginRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.PluginRun{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, runSelect+` where status='queued' and mode in ('live','simulate') and recovery_generation=? and (lease_until is null or lease_until<?) order by id limit 1`, recoveryGen, now())
	item, err := scanRun(row)
	if err != nil {
		return model.PluginRun{}, err
	}
	item.LeaseGeneration++
	ts := now()
	res, err := tx.ExecContext(ctx, `update plugin_runs set status='running',lease_owner=?,lease_generation=?,lease_until=?,started_at=coalesce(started_at,?) where id=? and status='queued' and lease_generation=?`,
		workerID, item.LeaseGeneration, until.UTC().Format(time.RFC3339Nano), ts, item.ID, item.LeaseGeneration-1)
	if err != nil {
		return model.PluginRun{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.PluginRun{}, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `insert into plugin_run_attempts(run_id,generation,worker_id,status,started_at) values(?,?,?,'running',?)`, item.ID, item.LeaseGeneration, workerID, ts); err != nil {
		return model.PluginRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.PluginRun{}, err
	}
	item.Status = model.PluginRunRunning
	item.LeaseOwner = workerID
	started := parseTime(ts)
	item.StartedAt = &started
	untilCopy := until.UTC()
	item.LeaseUntil = &untilCopy
	return item, nil
}

func (s *Store) FinishPluginRun(ctx context.Context, id int64, generation int64, status, errorCode string, result json.RawMessage) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update plugin_runs set status=?,error_code=?,result_json=?,finished_at=?,lease_until=null where id=? and lease_generation=? and status='running'`,
		status, errorCode, string(orJSON(result)), ts, id, generation)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("run lease is stale")
	}
	_, err = s.db.ExecContext(ctx, `update plugin_run_attempts set status=?,error_code=?,finished_at=? where run_id=? and generation=?`, status, errorCode, ts, id, generation)
	return err
}

func (s *Store) CancelPluginRun(ctx context.Context, id int64) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='cancelled',finished_at=? where id=? and status in ('queued','running')`, ts, id)
	return err
}

func (s *Store) CreatePluginRunAction(ctx context.Context, item *model.PluginRunAction) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugin_run_actions(run_id,action_key,capability,target_json,payload_digest,status,operation_id,changeset_id,task_id,result_json,error_code,lease_generation,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.RunID, item.ActionKey, item.Capability, string(orJSON(item.TargetJSON)), item.PayloadDigest, item.Status, item.OperationID, item.ChangesetID, item.TaskID, string(orJSON(item.ResultJSON)), item.ErrorCode, item.LeaseGeneration, ts, ts)
	if err != nil {
		if isUniqueConstraint(err) {
			existing, getErr := s.GetPluginRunAction(ctx, item.RunID, item.ActionKey)
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

func (s *Store) GetPluginRunAction(ctx context.Context, runID int64, actionKey string) (model.PluginRunAction, error) {
	return scanAction(s.db.QueryRowContext(ctx, actionSelect+` where run_id=? and action_key=?`, runID, actionKey))
}

func (s *Store) GetPluginRunActionByOperationID(ctx context.Context, operationID string) (model.PluginRunAction, error) {
	return scanAction(s.db.QueryRowContext(ctx, actionSelect+` where operation_id=? order by id asc limit 1`, operationID))
}

func (s *Store) ListPluginRunActions(ctx context.Context, runID int64) ([]model.PluginRunAction, error) {
	rows, err := s.db.QueryContext(ctx, actionSelect+` where run_id=? order by id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginRunAction{}
	for rows.Next() {
		item, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpdatePluginRunAction(ctx context.Context, item model.PluginRunAction) error {
	_, err := s.db.ExecContext(ctx, `update plugin_run_actions set status=?,operation_id=?,changeset_id=?,task_id=?,result_json=?,error_code=?,updated_at=? where id=?`,
		item.Status, item.OperationID, item.ChangesetID, item.TaskID, string(orJSON(item.ResultJSON)), item.ErrorCode, now(), item.ID)
	return err
}

func (s *Store) AppendPluginRunLogs(ctx context.Context, runID int64, logs []model.PluginRunLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `insert or ignore into plugin_run_logs(run_id,seq,level,message,fields_json,created_at) values(?,?,?,?,?,?)`)
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

func (s *Store) ListPluginRunLogs(ctx context.Context, runID int64, afterSeq int64, limit int) ([]model.PluginRunLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `select id,run_id,seq,level,message,fields_json,created_at from plugin_run_logs where run_id=? and seq>? order by seq limit ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginRunLog{}
	for rows.Next() {
		var item model.PluginRunLog
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

func (s *Store) GetPluginState(ctx context.Context, pluginID int64, key string) (model.PluginStateEntry, error) {
	var item model.PluginStateEntry
	var updated, value string
	err := s.db.QueryRowContext(ctx, `select plugin_id,key,value_json,version,updated_at from plugin_state where plugin_id=? and key=?`, pluginID, key).
		Scan(&item.PluginID, &item.Key, &value, &item.Version, &updated)
	item.ValueJSON = jsonBytes(value)
	if err != nil {
		return model.PluginStateEntry{}, err
	}
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) CompareAndSetPluginState(ctx context.Context, pluginID int64, key string, expectedVersion int64, value json.RawMessage) (model.PluginStateEntry, error) {
	ts := now()
	if expectedVersion == 0 {
		_, err := s.db.ExecContext(ctx, `insert into plugin_state(plugin_id,key,value_json,version,updated_at) values(?,?,?,1,?)`, pluginID, key, string(orJSON(value)), ts)
		if err != nil {
			return model.PluginStateEntry{}, err
		}
		return s.GetPluginState(ctx, pluginID, key)
	}
	res, err := s.db.ExecContext(ctx, `update plugin_state set value_json=?,version=version+1,updated_at=? where plugin_id=? and key=? and version=?`, string(orJSON(value)), ts, pluginID, key, expectedVersion)
	if err != nil {
		return model.PluginStateEntry{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.PluginStateEntry{}, errors.New("condition_changed")
	}
	return s.GetPluginState(ctx, pluginID, key)
}

func (s *Store) CountPluginStateKeys(ctx context.Context, pluginID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from plugin_state where plugin_id=?`, pluginID).Scan(&n)
	return n, err
}

func (s *Store) EnqueuePluginEvent(ctx context.Context, topic, aggregateID string, payload json.RawMessage) error {
	id := fmt.Sprintf("evt_%d_%s", time.Now().UTC().UnixNano(), aggregateID)
	_, err := s.db.ExecContext(ctx, `insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values(?,?,?,?,'pending',?,?)`,
		id, topic, aggregateID, string(orJSON(payload)), now(), now())
	return err
}

func (s *Store) EnqueuePluginEventTx(ctx context.Context, tx *sql.Tx, topic, aggregateID string, payload json.RawMessage) error {
	id := fmt.Sprintf("evt_%d_%s", time.Now().UTC().UnixNano(), aggregateID)
	_, err := tx.ExecContext(ctx, `insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values(?,?,?,?,'pending',?,?)`,
		id, topic, aggregateID, string(orJSON(payload)), now(), now())
	return err
}

func (s *Store) ClaimPluginEvents(ctx context.Context, owner string, until time.Time, limit int) ([]EventOutboxItem, error) {
	if limit <= 0 {
		limit = 32
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id,topic,aggregate_id,payload_json,attempts,created_at from event_outbox where status='pending' and available_at<=? and topic like 'plugin.%' order by created_at limit ?`, now(), limit)
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

func (s *Store) CompletePluginEvent(ctx context.Context, id string) error {
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

func (s *Store) GetServerPluginPolicy(ctx context.Context, serverID int64) (model.ServerPluginPolicy, error) {
	var item model.ServerPluginPolicy
	var created, updated string
	var pluginsEnabled, powerEnabled int
	err := s.db.QueryRowContext(ctx, `select server_id,plugins_enabled,plugins_power_enabled,created_at,updated_at from server_plugin_policies where server_id=?`, serverID).
		Scan(&item.ServerID, &pluginsEnabled, &powerEnabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ServerPluginPolicy{ServerID: serverID}, nil
	}
	if err != nil {
		return model.ServerPluginPolicy{}, err
	}
	item.PluginsEnabled = pluginsEnabled == 1
	item.PluginsPowerEnabled = powerEnabled == 1
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) UpsertServerPluginPolicy(ctx context.Context, item model.ServerPluginPolicy) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into server_plugin_policies(server_id,plugins_enabled,plugins_power_enabled,created_at,updated_at) values(?,?,?,?,?)
		on conflict(server_id) do update set plugins_enabled=excluded.plugins_enabled,plugins_power_enabled=excluded.plugins_power_enabled,updated_at=excluded.updated_at`,
		item.ServerID, boolInt(item.PluginsEnabled), boolInt(item.PluginsPowerEnabled), ts, ts)
	return err
}

func (s *Store) PausePluginSchedulerAfterRestore(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for key, value := range map[string]string{
		"plugins.enabled":          "false",
		"plugins.scheduler_paused": "true",
	} {
		if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, key, value, ts); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values('plugins.recovery_generation','2',?) on conflict(key) do update set value=cast(cast(value as integer)+1 as text),updated_at=excluded.updated_at`, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_grants set revoked_at=? where revoked_at is null`, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='cancelled',finished_at=?,lease_generation=lease_generation+1,lease_owner='',lease_until=null where status in ('queued','running')`, ts); err != nil {
		return err
	}
	return tx.Commit()
}

const revisionSelect = `select id,plugin_id,revision_number,status,schema_version,runtime,sdk_version,source,source_digest,manifest_json,author_user_id,published_at,published_by_user_id,created_at from plugin_revisions`
const triggerSelect = `select id,plugin_id,revision_id,name,enabled,kind,spec_json,params_json,env_json,binding_revision,created_by_user_id,created_at,updated_at from plugin_trigger_bindings`
const grantSelect = `select id,plugin_id,revision_id,binding_id,grant_revision,capabilities_json,resource_scope_json,constraints_json,source_digest,binding_digest,expires_at,revoked_at,approved_by_user_id,created_at from plugin_grants`
const runSelect = `select id,uuid,plugin_id,revision_id,binding_id,grant_id,caller_principal,trigger_kind,idempotency_key,status,mode,snapshot_json,result_json,error_code,skip_reason,lease_owner,lease_generation,lease_until,recovery_generation,created_at,started_at,finished_at from plugin_runs`
const actionSelect = `select id,run_id,action_key,capability,target_json,payload_digest,status,operation_id,changeset_id,task_id,result_json,error_code,lease_generation,created_at,updated_at from plugin_run_actions`

func scanPlugin(row incidentRowScanner) (model.Plugin, error) {
	var item model.Plugin
	var created, updated string
	if err := row.Scan(&item.ID, &item.Name, &item.Description, &item.OwnerUserID, &item.Status, &created, &updated); err != nil {
		return model.Plugin{}, err
	}
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func scanRevision(row incidentRowScanner) (model.PluginRevision, error) {
	var item model.PluginRevision
	var published, created sql.NullString
	var publishedBy sql.NullInt64
	var manifest string
	if err := row.Scan(&item.ID, &item.PluginID, &item.RevisionNumber, &item.Status, &item.SchemaVersion, &item.Runtime, &item.SDKVersion, &item.Source, &item.SourceDigest, &manifest, &item.AuthorUserID, &published, &publishedBy, &created); err != nil {
		return model.PluginRevision{}, err
	}
	item.ManifestJSON = jsonBytes(manifest)
	item.PublishedAt = parseNullTime(published)
	if publishedBy.Valid {
		item.PublishedByUserID = &publishedBy.Int64
	}
	item.CreatedAt = parseTime(created.String)
	return item, nil
}

func scanTrigger(row incidentRowScanner) (model.PluginTriggerBinding, error) {
	var item model.PluginTriggerBinding
	var enabled int
	var created, updated string
	var spec, params, env string
	if err := row.Scan(&item.ID, &item.PluginID, &item.RevisionID, &item.Name, &enabled, &item.Kind, &spec, &params, &env, &item.BindingRevision, &item.CreatedByUserID, &created, &updated); err != nil {
		return model.PluginTriggerBinding{}, err
	}
	item.SpecJSON = jsonBytes(spec)
	item.ParamsJSON = jsonBytes(params)
	item.EnvJSON = jsonBytes(env)
	item.Enabled = enabled == 1
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func scanGrant(row incidentRowScanner) (model.PluginGrant, error) {
	var item model.PluginGrant
	var binding sql.NullInt64
	var expires, revoked, created sql.NullString
	var capabilities, scope, constraints string
	if err := row.Scan(&item.ID, &item.PluginID, &item.RevisionID, &binding, &item.GrantRevision, &capabilities, &scope, &constraints, &item.SourceDigest, &item.BindingDigest, &expires, &revoked, &item.ApprovedByUserID, &created); err != nil {
		return model.PluginGrant{}, err
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

func scanRun(row incidentRowScanner) (model.PluginRun, error) {
	var item model.PluginRun
	var binding, grant sql.NullInt64
	var leaseUntil, created, started, finished sql.NullString
	var snapshot, result string
	if err := row.Scan(&item.ID, &item.UUID, &item.PluginID, &item.RevisionID, &binding, &grant, &item.CallerPrincipal, &item.TriggerKind, &item.IdempotencyKey, &item.Status, &item.Mode, &snapshot, &result, &item.ErrorCode, &item.SkipReason, &item.LeaseOwner, &item.LeaseGeneration, &leaseUntil, &item.RecoveryGen, &created, &started, &finished); err != nil {
		return model.PluginRun{}, err
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

func scanAction(row incidentRowScanner) (model.PluginRunAction, error) {
	var item model.PluginRunAction
	var task sql.NullInt64
	var created, updated string
	var target, result string
	if err := row.Scan(&item.ID, &item.RunID, &item.ActionKey, &item.Capability, &target, &item.PayloadDigest, &item.Status, &item.OperationID, &item.ChangesetID, &task, &result, &item.ErrorCode, &item.LeaseGeneration, &created, &updated); err != nil {
		return model.PluginRunAction{}, err
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
