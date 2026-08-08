package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) CreateAutomationWorkflow(ctx context.Context, item *model.AutomationWorkflow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	var grantID, changesetID any
	if item.GrantID != "" {
		grantID = item.GrantID
	}
	if item.ChangesetID != "" {
		changesetID = item.ChangesetID
	}
	if _, err := tx.ExecContext(ctx, `insert into automation_workflows(id,principal_id,grant_id,kind,status,reason,idempotency_key,changeset_id,current_step,correlation_id,affected_resources_json,next_action_json,error_code,error_message,created_at,updated_at,completed_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.PrincipalID, grantID, item.Kind, item.Status, item.Reason, item.IdempotencyKey, changesetID, item.CurrentStep, item.CorrelationID, normalizedJSONArray(item.AffectedResources), normalizedJSONObject(item.NextAction), item.ErrorCode, item.ErrorMessage, ts, ts, timePtrString(item.CompletedAt)); err != nil {
		return err
	}
	for index := range item.Steps {
		step := &item.Steps[index]
		if _, err := tx.ExecContext(ctx, `insert into automation_workflow_steps(id,workflow_id,position,name,status,attempt,idempotency_key,input_digest,output_digest,retryable,next_action_json,error_code,correlation_id,started_at,finished_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, step.ID, item.ID, step.Position, step.Name, step.Status, step.Attempt, step.IdempotencyKey, step.InputDigest, step.OutputDigest, boolInt(step.Retryable), normalizedJSONObject(step.NextAction), step.ErrorCode, step.CorrelationID, timePtrString(step.StartedAt), timePtrString(step.FinishedAt), ts, ts); err != nil {
			return err
		}
		step.WorkflowID, step.CreatedAt, step.UpdatedAt = item.ID, parseTime(ts), parseTime(ts)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) GetAutomationWorkflow(ctx context.Context, id string) (*model.AutomationWorkflow, error) {
	item, err := scanAutomationWorkflow(s.db.QueryRowContext(ctx, `select id,principal_id,coalesce(grant_id,''),kind,status,reason,idempotency_key,coalesce(changeset_id,''),current_step,correlation_id,affected_resources_json,next_action_json,error_code,error_message,created_at,updated_at,completed_at from automation_workflows where id=?`, id))
	if err != nil {
		return nil, err
	}
	item.Steps, err = s.ListAutomationWorkflowSteps(ctx, item.ID)
	return item, err
}

func (s *Store) FindAutomationWorkflowByIdempotency(ctx context.Context, principalID, key string) (*model.AutomationWorkflow, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `select id from automation_workflows where principal_id=? and idempotency_key=?`, principalID, key).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetAutomationWorkflow(ctx, id)
}

func (s *Store) FindAutomationWorkflowByChangeset(ctx context.Context, changesetID string) (*model.AutomationWorkflow, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `select id from automation_workflows where changeset_id=? order by created_at desc limit 1`, changesetID).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetAutomationWorkflow(ctx, id)
}

func (s *Store) UpdateAutomationWorkflow(ctx context.Context, item *model.AutomationWorkflow) error {
	result, err := s.db.ExecContext(ctx, `update automation_workflows set status=?,changeset_id=?,current_step=?,affected_resources_json=?,next_action_json=?,error_code=?,error_message=?,updated_at=?,completed_at=? where id=? and principal_id=?`, item.Status, nullString(item.ChangesetID), item.CurrentStep, normalizedJSONArray(item.AffectedResources), normalizedJSONObject(item.NextAction), item.ErrorCode, item.ErrorMessage, now(), timePtrString(item.CompletedAt), item.ID, item.PrincipalID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateAutomationWorkflowStep(ctx context.Context, step *model.AutomationWorkflowStep) error {
	result, err := s.db.ExecContext(ctx, `update automation_workflow_steps set status=?,attempt=?,output_digest=?,retryable=?,next_action_json=?,error_code=?,started_at=?,finished_at=?,updated_at=? where id=? and workflow_id=?`, step.Status, step.Attempt, step.OutputDigest, boolInt(step.Retryable), normalizedJSONObject(step.NextAction), step.ErrorCode, timePtrString(step.StartedAt), timePtrString(step.FinishedAt), now(), step.ID, step.WorkflowID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateAutomationWorkflowAndStep(ctx context.Context, item *model.AutomationWorkflow, step *model.AutomationWorkflowStep) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	workflowResult, err := tx.ExecContext(ctx, `update automation_workflows set status=?,changeset_id=?,current_step=?,affected_resources_json=?,next_action_json=?,error_code=?,error_message=?,updated_at=?,completed_at=? where id=? and principal_id=?`, item.Status, nullString(item.ChangesetID), item.CurrentStep, normalizedJSONArray(item.AffectedResources), normalizedJSONObject(item.NextAction), item.ErrorCode, item.ErrorMessage, now(), timePtrString(item.CompletedAt), item.ID, item.PrincipalID)
	if err != nil {
		return err
	}
	if count, _ := workflowResult.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	stepResult, err := tx.ExecContext(ctx, `update automation_workflow_steps set status=?,attempt=?,output_digest=?,retryable=?,next_action_json=?,error_code=?,started_at=?,finished_at=?,updated_at=? where id=? and workflow_id=?`, step.Status, step.Attempt, step.OutputDigest, boolInt(step.Retryable), normalizedJSONObject(step.NextAction), step.ErrorCode, timePtrString(step.StartedAt), timePtrString(step.FinishedAt), now(), step.ID, step.WorkflowID)
	if err != nil {
		return err
	}
	if count, _ := stepResult.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) ListAutomationWorkflowSteps(ctx context.Context, workflowID string) ([]model.AutomationWorkflowStep, error) {
	rows, err := s.db.QueryContext(ctx, `select id,workflow_id,position,name,status,attempt,idempotency_key,input_digest,output_digest,retryable,next_action_json,error_code,correlation_id,started_at,finished_at,created_at,updated_at from automation_workflow_steps where workflow_id=? order by position`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.AutomationWorkflowStep{}
	for rows.Next() {
		var item model.AutomationWorkflowStep
		var retryable int
		var nextAction, created, updated string
		var startedAt, finishedAt sql.NullString
		if err := rows.Scan(&item.ID, &item.WorkflowID, &item.Position, &item.Name, &item.Status, &item.Attempt, &item.IdempotencyKey, &item.InputDigest, &item.OutputDigest, &retryable, &nextAction, &item.ErrorCode, &item.CorrelationID, &startedAt, &finishedAt, &created, &updated); err != nil {
			return nil, err
		}
		item.Retryable = retryable != 0
		item.NextAction = json.RawMessage(nextAction)
		item.StartedAt, item.FinishedAt = nullableTime(startedAt), nullableTime(finishedAt)
		item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanAutomationWorkflow(scanner interface{ Scan(...any) error }) (*model.AutomationWorkflow, error) {
	var item model.AutomationWorkflow
	var affected, nextAction, created, updated string
	var completed sql.NullString
	if err := scanner.Scan(&item.ID, &item.PrincipalID, &item.GrantID, &item.Kind, &item.Status, &item.Reason, &item.IdempotencyKey, &item.ChangesetID, &item.CurrentStep, &item.CorrelationID, &affected, &nextAction, &item.ErrorCode, &item.ErrorMessage, &created, &updated, &completed); err != nil {
		return nil, err
	}
	item.AffectedResources, item.NextAction = json.RawMessage(affected), json.RawMessage(nextAction)
	item.CreatedAt, item.UpdatedAt, item.CompletedAt = parseTime(created), parseTime(updated), nullableTime(completed)
	return &item, nil
}

func normalizedJSONArray(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "[]"
	}
	var value []any
	if json.Unmarshal(raw, &value) != nil {
		return "[]"
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) ListAutomationWorkflows(ctx context.Context, principalID string, limit int) ([]model.AutomationWorkflow, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `select id,principal_id,coalesce(grant_id,''),kind,status,reason,idempotency_key,coalesce(changeset_id,''),current_step,correlation_id,affected_resources_json,next_action_json,error_code,error_message,created_at,updated_at,completed_at from automation_workflows where principal_id=? order by created_at desc limit ?`, principalID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.AutomationWorkflow{}
	for rows.Next() {
		item, err := scanAutomationWorkflow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}
