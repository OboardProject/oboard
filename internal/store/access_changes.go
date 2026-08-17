package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// ErrAccessChangeNotActive is returned when a transition targets a change whose
// current status no longer permits it (concurrent worker or cancelled change).
var ErrAccessChangeNotActive = errors.New("access change is not in the expected state")

const accessChangeSelectSQL = `select id,change_type,coalesce(source_plan_id,0),coalesce(candidate_revision_id,0),coalesce(expected_active_revision_id,0),status,coalesce(preview_hash,''),affected_user_count,activate_at,coalesce(payload_json,'{}'),coalesce(prepare_projection_json,'{}'),coalesce(finalize_projection_json,'{}'),coalesce(error,''),created_by,created_at,activated_at,finalized_at,failed_at from access_changes`

// CreateAccessChange inserts the change and its per-server targets in one
// transaction.
func (s *Store) CreateAccessChange(ctx context.Context, v *model.AccessChange, serverIDs []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.ExecContext(ctx, `insert into access_changes(change_type,source_plan_id,candidate_revision_id,expected_active_revision_id,status,preview_hash,affected_user_count,activate_at,payload_json,prepare_projection_json,finalize_projection_json,error,created_by,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(v.ChangeType), nullInt64(v.SourcePlanID), nonNullInt64(v.CandidateRevisionID), nonNullInt64(v.ExpectedActiveRevisionID), string(v.Status), v.PreviewHash, v.AffectedUserCount, nullTime(v.ActivateAt), v.PayloadJSON, v.PrepareProjectionJSON, v.FinalizeProjectionJSON, v.Error, v.CreatedBy, ts, ts)
	if err != nil {
		return 0, err
	}
	changeID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, serverID := range serverIDs {
		if _, err := tx.ExecContext(ctx, `insert into access_change_targets(access_change_id,server_id,status,updated_at) values(?,?,?,?)`, changeID, serverID, string(model.AccessChangeTargetPending), ts); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	v.ID = changeID
	v.CreatedAt = parseTime(ts)
	return changeID, nil
}

// GetAccessChange loads one change including its targets.
func (s *Store) GetAccessChange(ctx context.Context, id int64) (*model.AccessChange, error) {
	change, err := s.getAccessChange(ctx, id)
	if err != nil {
		return nil, err
	}
	targets, err := s.ListAccessChangeTargets(ctx, id)
	if err != nil {
		return nil, err
	}
	change.Targets = targets
	return change, nil
}

func (s *Store) getAccessChange(ctx context.Context, id int64) (*model.AccessChange, error) {
	var v model.AccessChange
	var createdBy sql.NullInt64
	var activateAt, activatedAt, finalizedAt, failedAt sql.NullString
	var ca string
	if err := s.db.QueryRowContext(ctx, accessChangeSelectSQL+` where id=?`, id).Scan(&v.ID, &v.ChangeType, &v.SourcePlanID, &v.CandidateRevisionID, &v.ExpectedActiveRevisionID, &v.Status, &v.PreviewHash, &v.AffectedUserCount, &activateAt, &v.PayloadJSON, &v.PrepareProjectionJSON, &v.FinalizeProjectionJSON, &v.Error, &createdBy, &ca, &activatedAt, &finalizedAt, &failedAt); err != nil {
		return nil, err
	}
	v.ActivateAt = parseNullTimePtr(activateAt)
	v.CreatedAt = parseTime(ca)
	v.ActivatedAt = parseNullTimePtr(activatedAt)
	v.FinalizedAt = parseNullTimePtr(finalizedAt)
	v.FailedAt = parseNullTimePtr(failedAt)
	if createdBy.Valid {
		v.CreatedBy = &createdBy.Int64
	}
	return &v, nil
}

// ListAccessChanges returns the most recent changes, newest first.
func (s *Store) ListAccessChanges(ctx context.Context, limit int) ([]model.AccessChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, accessChangeSelectSQL+` order by id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanAccessChanges(rows)
	if err != nil {
		return nil, err
	}
	for i := range out {
		targets, err := s.ListAccessChangeTargets(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Targets = targets
	}
	return out, nil
}

// ListAccessChangesByStatus returns open changes for the worker.
func (s *Store) ListAccessChangesByStatus(ctx context.Context, statuses ...model.AccessChangeStatus) ([]model.AccessChange, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(statuses))
	args := make([]any, 0, len(statuses))
	for _, status := range statuses {
		placeholders = append(placeholders, "?")
		args = append(args, string(status))
	}
	rows, err := s.db.QueryContext(ctx, accessChangeSelectSQL+` where status in (`+strings.Join(placeholders, ",")+`) order by id asc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAccessChanges(rows)
}

func scanAccessChanges(rows *sql.Rows) ([]model.AccessChange, error) {
	var out []model.AccessChange
	for rows.Next() {
		var v model.AccessChange
		var createdBy sql.NullInt64
		var activateAt, activatedAt, finalizedAt, failedAt sql.NullString
		var ca string
		if err := rows.Scan(&v.ID, &v.ChangeType, &v.SourcePlanID, &v.CandidateRevisionID, &v.ExpectedActiveRevisionID, &v.Status, &v.PreviewHash, &v.AffectedUserCount, &activateAt, &v.PayloadJSON, &v.PrepareProjectionJSON, &v.FinalizeProjectionJSON, &v.Error, &createdBy, &ca, &activatedAt, &finalizedAt, &failedAt); err != nil {
			return nil, err
		}
		v.ActivateAt = parseNullTimePtr(activateAt)
		v.CreatedAt = parseTime(ca)
		v.ActivatedAt = parseNullTimePtr(activatedAt)
		v.FinalizedAt = parseNullTimePtr(finalizedAt)
		v.FailedAt = parseNullTimePtr(failedAt)
		if createdBy.Valid {
			v.CreatedBy = &createdBy.Int64
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateAccessChangeStatus transitions a change conditionally. When the current
// status no longer matches, ErrAccessChangeNotActive is returned.
func (s *Store) UpdateAccessChangeStatus(ctx context.Context, id int64, from []model.AccessChangeStatus, to model.AccessChangeStatus, errorText string) error {
	if len(from) == 0 {
		return errors.New("at least one source status required")
	}
	placeholders := make([]string, 0, len(from))
	for range from {
		placeholders = append(placeholders, "?")
	}
	if strings.TrimSpace(errorText) == "" {
		errorText = ""
	}
	ts := now()
	args := []any{string(to), errorText}
	phaseSet := ""
	switch to {
	case model.AccessChangeActivating:
	case model.AccessChangeFinalizing:
		phaseSet = `activated_at=?,`
		args = append(args, ts)
	case model.AccessChangeFinalized:
		phaseSet = `finalized_at=?,`
		args = append(args, ts)
	case model.AccessChangeFailed:
		phaseSet = `failed_at=?,`
		args = append(args, ts)
	}
	args = append(args, ts, id)
	for _, status := range from {
		args = append(args, string(status))
	}
	sqlText := `update access_changes set status=?,error=?` + (`,` + phaseSet + `updated_at=?`) + ` where id=? and status in (` + strings.Join(placeholders, ",") + `)` // #nosec G202 -- fragments are allowlisted and placeholders are generated.
	res, err := s.db.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrAccessChangeNotActive
	}
	return nil
}

// SetAccessChangeTargetTask records the Agent task id for a phase and moves the
// target into the phase state.
func (s *Store) SetAccessChangeTargetTask(ctx context.Context, changeID, serverID, taskID int64, phase string, status model.AccessChangeTargetStatus) error {
	column := "prepare_task_id"
	if phase == "finalize" {
		column = "finalize_task_id"
	}
	_, err := s.db.ExecContext(ctx, `update access_change_targets set `+column+`=?,status=?,updated_at=? where access_change_id=? and server_id=?`, taskID, string(status), now(), changeID, serverID)
	return err
}

// SetAccessChangeTargetStatus updates a target's phase state and error.
func (s *Store) SetAccessChangeTargetStatus(ctx context.Context, changeID, serverID int64, status model.AccessChangeTargetStatus, errorText string) error {
	_, err := s.db.ExecContext(ctx, `update access_change_targets set status=?,error=?,updated_at=? where access_change_id=? and server_id=?`, string(status), errorText, now(), changeID, serverID)
	return err
}

// ListAccessChangeTargets returns the per-server targets of one change.
func (s *Store) ListAccessChangeTargets(ctx context.Context, changeID int64) ([]model.AccessChangeTarget, error) {
	rows, err := s.db.QueryContext(ctx, `select access_change_id,server_id,prepare_task_id,finalize_task_id,status,coalesce(error,''),updated_at from access_change_targets where access_change_id=? order by server_id`, changeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AccessChangeTarget
	for rows.Next() {
		var v model.AccessChangeTarget
		var updatedAt string
		if err := rows.Scan(&v.AccessChangeID, &v.ServerID, &v.PrepareTaskID, &v.FinalizeTaskID, &v.Status, &v.Error, &updatedAt); err != nil {
			return nil, err
		}
		v.UpdatedAt = parseTime(updatedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

// MarkAccessChangeCancelled cancels a change that has not been activated yet.
// Plan publishes also release their pending candidate so a failed prepare does
// not permanently block later edits.
func (s *Store) MarkAccessChangeCancelled(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var current model.AccessChangeStatus
	var changeType model.AccessChangeType
	var sourcePlanID, candidateRevisionID int64
	var currentError string
	var activatedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `select status,change_type,coalesce(source_plan_id,0),candidate_revision_id,coalesce(error,''),activated_at from access_changes where id=?`, id).Scan(&current, &changeType, &sourcePlanID, &candidateRevisionID, &currentError, &activatedAt); err != nil {
		return err
	}
	failedPlanChange := current == model.AccessChangeFailed && (changeType == model.AccessChangePlanPublish || changeType == model.AccessChangePlanRestore)
	canCancel := current == model.AccessChangePreparing || current == model.AccessChangeActivating || failedPlanChange
	if !canCancel || activatedAt.Valid {
		return fmt.Errorf("access change %d cannot be cancelled in status %s", id, current)
	}
	ts := now()
	if current != model.AccessChangeFailed {
		currentError = ""
	}
	res, err := tx.ExecContext(ctx, `update access_changes set status=?,error=?,updated_at=? where id=? and status=?`, string(model.AccessChangeCancelled), currentError, ts, id, string(current))
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrAccessChangeNotActive
	}
	if changeType == model.AccessChangePlanPublish || changeType == model.AccessChangePlanRestore {
		res, err = tx.ExecContext(ctx, `update subscription_plans set latest_revision_id=current_revision_id,pending_revision_id=null,lock_version=lock_version+1,updated_at=? where id=? and pending_revision_id=?`, ts, sourcePlanID, candidateRevisionID)
		if err != nil {
			return err
		}
		if affected, err := res.RowsAffected(); err != nil {
			return err
		} else if affected != 1 {
			return fmt.Errorf("access change %d plan candidate is no longer pending", id)
		}
	}
	return tx.Commit()
}

func nullInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nonNullInt64(v int64) any {
	if v <= 0 {
		return 0
	}
	return v
}

func nullTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UTC().Format(time.RFC3339Nano)
}

func parseNullTimePtr(v sql.NullString) *time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	t := parseTime(v.String)
	return &t
}
