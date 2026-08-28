package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) GetPlanReconcileState(ctx context.Context, planID int64) (*model.PlanReconcileState, error) {
	row := s.db.QueryRowContext(ctx, `select plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at from subscription_plan_reconcile_states where plan_id=?`, planID)
	var state model.PlanReconcileState
	var applying, lastChange sql.NullInt64
	var createdAt, updatedAt string
	if err := row.Scan(&state.PlanID, &applying, &state.Status, &lastChange, &state.BlockedReason, &state.BlockedJSON, &state.AttemptCount, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	if applying.Valid {
		v := applying.Int64
		state.ApplyingRevisionID = &v
	}
	if lastChange.Valid {
		v := lastChange.Int64
		state.LastAccessChangeID = &v
	}
	state.CreatedAt = parseTime(createdAt)
	state.UpdatedAt = parseTime(updatedAt)
	if state.BlockedJSON == "" {
		state.BlockedJSON = "{}"
	}
	if state.Status == "" {
		state.Status = "idle"
	}
	return &state, nil
}

func (s *Store) ListPlanReconcileStates(ctx context.Context) ([]model.PlanReconcileState, error) {
	rows, err := s.db.QueryContext(ctx, `select plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at from subscription_plan_reconcile_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PlanReconcileState
	for rows.Next() {
		var state model.PlanReconcileState
		var applying, lastChange sql.NullInt64
		var createdAt, updatedAt string
		if err := rows.Scan(&state.PlanID, &applying, &state.Status, &lastChange, &state.BlockedReason, &state.BlockedJSON, &state.AttemptCount, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if applying.Valid {
			v := applying.Int64
			state.ApplyingRevisionID = &v
		}
		if lastChange.Valid {
			v := lastChange.Int64
			state.LastAccessChangeID = &v
		}
		state.CreatedAt = parseTime(createdAt)
		state.UpdatedAt = parseTime(updatedAt)
		out = append(out, state)
	}
	return out, rows.Err()
}

func (s *Store) UpsertPlanReconcileState(ctx context.Context, state model.PlanReconcileState) error {
	now := now()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = parseTime(now)
	}
	state.UpdatedAt = parseTime(now)
	_, err := s.db.ExecContext(ctx, `insert into subscription_plan_reconcile_states(plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at) values(?,?,?,?,?,?,?,?,?)
		on conflict(plan_id) do update set applying_revision_id=excluded.applying_revision_id, status=excluded.status, last_access_change_id=excluded.last_access_change_id, blocked_reason=excluded.blocked_reason, blocked_json=excluded.blocked_json, attempt_count=excluded.attempt_count, updated_at=excluded.updated_at`,
		state.PlanID, state.ApplyingRevisionID, state.Status, state.LastAccessChangeID, state.BlockedReason, state.BlockedJSON, state.AttemptCount, state.CreatedAt.UTC().Format(time.RFC3339Nano), state.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) SetPlanReconcileIdle(ctx context.Context, planID int64) error {
	now := now()
	_, err := s.db.ExecContext(ctx, `insert into subscription_plan_reconcile_states(plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at) values(?,?,?,?,?,?,?,?,?)
		on conflict(plan_id) do update set applying_revision_id=null, status='idle', last_access_change_id=null, blocked_reason='', blocked_json='{}', attempt_count=0, updated_at=excluded.updated_at`,
		planID, nil, "idle", nil, "", "{}", 0, now, now)
	return err
}

func (s *Store) SetPlanReconcileApplying(ctx context.Context, planID, revisionID, accessChangeID int64, status string) error {
	now := now()
	_, err := s.db.ExecContext(ctx, `insert into subscription_plan_reconcile_states(plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at) values(?,?,?,?,?,?,?,?,?)
		on conflict(plan_id) do update set applying_revision_id=excluded.applying_revision_id, status=excluded.status, last_access_change_id=excluded.last_access_change_id, blocked_reason='', blocked_json='{}', attempt_count=0, updated_at=excluded.updated_at`,
		planID, revisionID, status, accessChangeID, "", "{}", 0, now, now)
	return err
}

func (s *Store) SetPlanReconcileWaiting(ctx context.Context, planID int64, reason, blockedJSON string) error {
	now := now()
	if blockedJSON == "" {
		blockedJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx, `insert into subscription_plan_reconcile_states(plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at) values(?,null,'waiting_dependency',null,?,?,0,?,?)
		on conflict(plan_id) do update set status='waiting_dependency', blocked_reason=excluded.blocked_reason, blocked_json=excluded.blocked_json, updated_at=excluded.updated_at`,
		planID, reason, blockedJSON, now, now)
	return err
}

func (s *Store) SetPlanReconcileFailed(ctx context.Context, planID int64, reason string) error {
	now := now()
	_, err := s.db.ExecContext(ctx, `insert into subscription_plan_reconcile_states(plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at) values(?,null,'failed',null,?,'{}',1,?,?)
		on conflict(plan_id) do update set status='failed', blocked_reason=excluded.blocked_reason, attempt_count=attempt_count+1, updated_at=excluded.updated_at`,
		planID, reason, now, now)
	return err
}

func (s *Store) HasOpenPlanAccessChange(ctx context.Context, planID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from access_changes where source_plan_id=? and status in ('preparing','activating','finalizing')`, planID).Scan(&n)
	return n > 0, err
}

func (s *Store) GetOpenPlanAccessChange(ctx context.Context, planID int64) (*model.AccessChange, error) {
	row := s.db.QueryRowContext(ctx, `select id,change_type,source_plan_id,candidate_revision_id,expected_active_revision_id,status,preview_hash,affected_user_count,activate_at,payload_json,prepare_projection_json,finalize_projection_json,error,created_by,created_at,activated_at,finalized_at,failed_at,updated_at from access_changes where source_plan_id=? and status in ('preparing','activating','finalizing') order by id desc limit 1`, planID)
	return scanAccessChange(row)
}

func scanAccessChange(row *sql.Row) (*model.AccessChange, error) {
	var c model.AccessChange
	var previewHash, payloadJSON, prepareJSON, finalizeJSON, errMsg sql.NullString
	var activateAt, activatedAt, finalizedAt, failedAt, createdAt, updatedAt sql.NullString
	var createdBy sql.NullInt64
	var sourcePlanID sql.NullInt64
	if err := row.Scan(&c.ID, &c.ChangeType, &sourcePlanID, &c.CandidateRevisionID, &c.ExpectedActiveRevisionID, &c.Status, &previewHash, &c.AffectedUserCount, &activateAt, &payloadJSON, &prepareJSON, &finalizeJSON, &errMsg, &createdBy, &createdAt, &activatedAt, &finalizedAt, &failedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	if sourcePlanID.Valid {
		c.SourcePlanID = sourcePlanID.Int64
	}
	if previewHash.Valid {
		c.PreviewHash = previewHash.String
	}
	if payloadJSON.Valid {
		c.PayloadJSON = payloadJSON.String
	}
	if prepareJSON.Valid {
		c.PrepareProjectionJSON = prepareJSON.String
	}
	if finalizeJSON.Valid {
		c.FinalizeProjectionJSON = finalizeJSON.String
	}
	if errMsg.Valid {
		c.Error = errMsg.String
	}
	if createdBy.Valid {
		c.CreatedBy = &createdBy.Int64
	}
	if createdAt.Valid {
		c.CreatedAt = parseTime(createdAt.String)
	}
	if activateAt.Valid && activateAt.String != "" {
		t := parseTime(activateAt.String)
		c.ActivateAt = &t
	}
	if activatedAt.Valid && activatedAt.String != "" {
		t := parseTime(activatedAt.String)
		c.ActivatedAt = &t
	}
	if finalizedAt.Valid && finalizedAt.String != "" {
		t := parseTime(finalizedAt.String)
		c.FinalizedAt = &t
	}
	if failedAt.Valid && failedAt.String != "" {
		t := parseTime(failedAt.String)
		c.FailedAt = &t
	}
	return &c, nil
}

func (s *Store) SetPendingIfEmpty(ctx context.Context, planID, revisionID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `update subscription_plans set pending_revision_id=? where id=? and coalesce(pending_revision_id,0)=0`, revisionID, planID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) ClearPendingRevision(ctx context.Context, planID, expectedPendingID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `update subscription_plans set pending_revision_id=null where id=? and coalesce(pending_revision_id,0)=?`, planID, expectedPendingID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) MigratePlanReconcileStates(ctx context.Context) error {
	// For each plan with a non-null pending_revision_id, create a reconcile state
	// that reflects the current applying revision. This preserves the old
	// pending as the worker's applying pointer without blocking new saves.
	rows, err := s.db.QueryContext(ctx, `select id,coalesce(pending_revision_id,0),coalesce(current_revision_id,0),coalesce(latest_revision_id,0) from subscription_plans where pending_revision_id is not null`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct {
		planID, pendingID, currentID, latestID int64
	}
	var pendings []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.planID, &p.pendingID, &p.currentID, &p.latestID); err != nil {
			return err
		}
		pendings = append(pendings, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pendings {
		var status string = "queued"
		var lastChangeID sql.NullInt64
		// Try to find the access change for this pending revision
		err := s.db.QueryRowContext(ctx, `select id, status from access_changes where source_plan_id=? and candidate_revision_id=? order by id desc limit 1`, p.planID, p.pendingID).Scan(&lastChangeID, &status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			status = "queued"
			lastChangeID = sql.NullInt64{}
		} else {
			switch status {
			case "preparing":
				status = "preparing"
			case "activating":
				status = "activating"
			case "finalizing":
				status = "finalizing"
			case "failed":
				status = "failed"
			default:
				status = "queued"
			}
		}
		now := now()
		if lastChangeID.Valid {
			_, err = s.db.ExecContext(ctx, `insert into subscription_plan_reconcile_states(plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at) values(?,?,?,?,?,?,?,?,?) on conflict(plan_id) do nothing`, p.planID, p.pendingID, status, lastChangeID.Int64, "", "{}", 0, now, now)
		} else {
			_, err = s.db.ExecContext(ctx, `insert into subscription_plan_reconcile_states(plan_id,applying_revision_id,status,last_access_change_id,blocked_reason,blocked_json,attempt_count,created_at,updated_at) values(?,?,?,?,?,?,?,?,?) on conflict(plan_id) do nothing`, p.planID, p.pendingID, status, nil, "", "{}", 0, now, now)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
