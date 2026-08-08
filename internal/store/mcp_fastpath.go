package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

var (
	ErrMCPPreparedPlanExpired      = errors.New("prepared plan expired")
	ErrMCPPreparedPlanConsumed     = errors.New("prepared plan already consumed")
	ErrMCPPreparedPlanClaimed      = errors.New("prepared plan commit is in progress")
	ErrMCPPreparedPlanCommitKey    = errors.New("prepared plan idempotency key mismatch")
	ErrMCPPreparedPlanUnauthorized = errors.New("prepared plan is not available to this principal and grant")
)

func (s *Store) CreateMCPPreparedPlan(ctx context.Context, item *model.MCPPreparedPlan) error {
	if item == nil || item.ID == "" || item.PrincipalID == "" || item.GrantID == "" || item.RecipeID == "" || item.RecipeVersion == "" || item.PlanHash == "" {
		return errors.New("complete prepared plan identity is required")
	}
	created := now()
	_, err := s.db.ExecContext(ctx, `insert into mcp_prepared_plans(id,principal_id,grant_id,recipe_id,recipe_version,operations_json,expected_revisions_json,plan_hash,risk_class,approval_mode,summary_json,verification_json,created_at,expires_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.PrincipalID, item.GrantID, item.RecipeID, item.RecipeVersion, normalizedJSONArray(item.Operations), normalizedJSONObject(item.ExpectedRevisions), item.PlanHash, item.RiskClass, item.ApprovalMode, normalizedJSONObject(item.Summary), normalizedJSONObject(item.Verification), created, item.ExpiresAt.UTC().Format(time.RFC3339Nano))
	if err == nil {
		item.CreatedAt = parseTime(created)
	}
	return err
}

func (s *Store) GetMCPPreparedPlan(ctx context.Context, id string) (*model.MCPPreparedPlan, error) {
	return scanMCPPreparedPlan(s.db.QueryRowContext(ctx, `select id,principal_id,grant_id,recipe_id,recipe_version,operations_json,expected_revisions_json,plan_hash,risk_class,approval_mode,summary_json,verification_json,commit_key,claimed_at,consumed_at,changeset_id,workflow_id,created_at,expires_at from mcp_prepared_plans where id=?`, id))
}

// ClaimMCPPreparedPlan atomically binds the first commit key and acquires a
// short lease. A failed caller may release the lease, while retries after a
// process interruption must use the same key and converge through Changeset
// and Workflow idempotency.
func (s *Store) ClaimMCPPreparedPlan(ctx context.Context, id, principalID, grantID, commitKey string, at time.Time) (*model.MCPPreparedPlan, error) {
	if id == "" || principalID == "" || grantID == "" || commitKey == "" {
		return nil, errors.New("complete prepared plan claim identity is required")
	}
	item, err := s.GetMCPPreparedPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.PrincipalID != principalID || item.GrantID != grantID {
		return nil, ErrMCPPreparedPlanUnauthorized
	}
	if !item.ExpiresAt.After(at) {
		return nil, ErrMCPPreparedPlanExpired
	}
	if item.CommitKey != "" && item.CommitKey != commitKey {
		return nil, ErrMCPPreparedPlanCommitKey
	}
	if item.ConsumedAt != nil {
		return item, nil
	}
	leaseBefore := at.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	stamp := at.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `update mcp_prepared_plans set commit_key=case when commit_key='' then ? else commit_key end,claimed_at=? where id=? and principal_id=? and grant_id=? and expires_at>? and consumed_at is null and (commit_key='' or commit_key=?) and (claimed_at is null or claimed_at<?)`, commitKey, stamp, id, principalID, grantID, stamp, commitKey, leaseBefore)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		latest, getErr := s.GetMCPPreparedPlan(ctx, id)
		if getErr != nil {
			return nil, getErr
		}
		if latest.ConsumedAt != nil && latest.CommitKey == commitKey {
			return latest, nil
		}
		if latest.PrincipalID != principalID || latest.GrantID != grantID {
			return nil, ErrMCPPreparedPlanUnauthorized
		}
		if !latest.ExpiresAt.After(at) {
			return nil, ErrMCPPreparedPlanExpired
		}
		if latest.CommitKey != "" && latest.CommitKey != commitKey {
			return nil, ErrMCPPreparedPlanCommitKey
		}
		return nil, ErrMCPPreparedPlanClaimed
	}
	return s.GetMCPPreparedPlan(ctx, id)
}

func (s *Store) ReleaseMCPPreparedPlanClaim(ctx context.Context, id, commitKey string) error {
	_, err := s.db.ExecContext(ctx, `update mcp_prepared_plans set claimed_at=null where id=? and commit_key=? and consumed_at is null`, id, commitKey)
	return err
}

func (s *Store) ConsumeMCPPreparedPlan(ctx context.Context, id, commitKey, changesetID, workflowID string, at time.Time) error {
	stamp := at.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `update mcp_prepared_plans set consumed_at=?,claimed_at=null,changeset_id=?,workflow_id=? where id=? and commit_key=? and consumed_at is null`, stamp, changesetID, workflowID, id, commitKey)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrMCPPreparedPlanConsumed
	}
	return nil
}

func scanMCPPreparedPlan(scanner interface{ Scan(...any) error }) (*model.MCPPreparedPlan, error) {
	var item model.MCPPreparedPlan
	var operations, revisions, summary, verification, created, expires string
	var claimed, consumed sql.NullString
	if err := scanner.Scan(&item.ID, &item.PrincipalID, &item.GrantID, &item.RecipeID, &item.RecipeVersion, &operations, &revisions, &item.PlanHash, &item.RiskClass, &item.ApprovalMode, &summary, &verification, &item.CommitKey, &claimed, &consumed, &item.ChangesetID, &item.WorkflowID, &created, &expires); err != nil {
		return nil, err
	}
	item.Operations, item.ExpectedRevisions = []byte(operations), []byte(revisions)
	item.Summary, item.Verification = []byte(summary), []byte(verification)
	item.ClaimedAt, item.ConsumedAt = nullableTime(claimed), nullableTime(consumed)
	item.CreatedAt, item.ExpiresAt = parseTime(created), parseTime(expires)
	return &item, nil
}

func (s *Store) CreateMCPTaskContinuation(ctx context.Context, item *model.MCPTaskContinuation) error {
	if item == nil || item.ID == "" || item.PrincipalID == "" || item.GrantID == "" || item.RecipeID == "" || item.RecipeVersion == "" {
		return errors.New("complete continuation identity is required")
	}
	created := now()
	_, err := s.db.ExecContext(ctx, `insert into mcp_task_continuations(id,principal_id,grant_id,recipe_id,recipe_version,goal,params_json,target_refs_json,state_json,created_at,expires_at) values(?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.PrincipalID, item.GrantID, item.RecipeID, item.RecipeVersion, item.Goal, normalizedJSONObject(item.Params), normalizedJSONArray(item.TargetRefs), normalizedJSONObject(item.State), created, item.ExpiresAt.UTC().Format(time.RFC3339Nano))
	if err == nil {
		item.CreatedAt = parseTime(created)
	}
	return err
}

func (s *Store) GetMCPTaskContinuation(ctx context.Context, id, principalID, grantID string, at time.Time) (*model.MCPTaskContinuation, error) {
	var item model.MCPTaskContinuation
	var params, refs, state, created, expires string
	err := s.db.QueryRowContext(ctx, `select id,principal_id,grant_id,recipe_id,recipe_version,goal,params_json,target_refs_json,state_json,created_at,expires_at from mcp_task_continuations where id=? and principal_id=? and grant_id=? and expires_at>?`, id, principalID, grantID, at.UTC().Format(time.RFC3339Nano)).Scan(&item.ID, &item.PrincipalID, &item.GrantID, &item.RecipeID, &item.RecipeVersion, &item.Goal, &params, &refs, &state, &created, &expires)
	if err != nil {
		return nil, err
	}
	item.Params, item.TargetRefs, item.State = []byte(params), []byte(refs), []byte(state)
	item.CreatedAt, item.ExpiresAt = parseTime(created), parseTime(expires)
	return &item, nil
}

func (s *Store) DeleteMCPTaskContinuation(ctx context.Context, id, principalID, grantID string) error {
	_, err := s.db.ExecContext(ctx, `delete from mcp_task_continuations where id=? and principal_id=? and grant_id=?`, id, principalID, grantID)
	return err
}

func (s *Store) CreateMCPFastPathMetric(ctx context.Context, item *model.MCPFastPathMetric) error {
	if item == nil || item.ID == "" || item.PrincipalID == "" || item.GrantID == "" || item.Phase == "" || item.Status == "" {
		return errors.New("complete fast path metric identity is required")
	}
	created := now()
	_, err := s.db.ExecContext(ctx, `insert into mcp_fast_path_metrics(id,principal_id,grant_id,recipe_id,recipe_version,fast_path_used,phase,status,duration_ms,fallback_reason,needs_input_count,candidate_resolution_count,validation_failure,changeset_id,workflow_id,final_workflow_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.PrincipalID, item.GrantID, item.RecipeID, item.RecipeVersion, boolInt(item.FastPathUsed), item.Phase, item.Status, item.DurationMS, item.FallbackReason, item.NeedsInputCount, item.CandidateResolutionCount, boolInt(item.ValidationFailure), item.ChangesetID, item.WorkflowID, item.FinalWorkflowStatus, created)
	if err == nil {
		item.CreatedAt = parseTime(created)
	}
	return err
}

func (s *Store) ListMCPFastPathMetrics(ctx context.Context, limit int) ([]model.MCPFastPathMetric, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select id,principal_id,grant_id,recipe_id,recipe_version,fast_path_used,phase,status,duration_ms,fallback_reason,needs_input_count,candidate_resolution_count,validation_failure,changeset_id,workflow_id,final_workflow_status,created_at from mcp_fast_path_metrics order by created_at desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.MCPFastPathMetric{}
	for rows.Next() {
		var item model.MCPFastPathMetric
		var fastPathUsed, validationFailure int
		var created string
		if err := rows.Scan(&item.ID, &item.PrincipalID, &item.GrantID, &item.RecipeID, &item.RecipeVersion, &fastPathUsed, &item.Phase, &item.Status, &item.DurationMS, &item.FallbackReason, &item.NeedsInputCount, &item.CandidateResolutionCount, &validationFailure, &item.ChangesetID, &item.WorkflowID, &item.FinalWorkflowStatus, &created); err != nil {
			return nil, err
		}
		item.FastPathUsed = fastPathUsed != 0
		item.ValidationFailure = validationFailure != 0
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) FindMCPPreparedPlanByWorkflow(ctx context.Context, workflowID, principalID, grantID string) (*model.MCPPreparedPlan, error) {
	return scanMCPPreparedPlan(s.db.QueryRowContext(ctx, `select id,principal_id,grant_id,recipe_id,recipe_version,operations_json,expected_revisions_json,plan_hash,risk_class,approval_mode,summary_json,verification_json,commit_key,claimed_at,consumed_at,changeset_id,workflow_id,created_at,expires_at from mcp_prepared_plans where workflow_id=? and principal_id=? and grant_id=?`, workflowID, principalID, grantID))
}
