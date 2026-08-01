package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const principalColumnsSQL = `id,owner_user_id,name,type,enabled,scopes_json,resource_filter_json,allowed_cidrs_json,rate_limit_per_minute,max_concurrency,expires_at,last_used_at,created_at,updated_at`
const principalSelectSQL = `select ` + principalColumnsSQL + ` from api_principals`

func (s *Store) CreateAPIPrincipal(ctx context.Context, principal *model.APIPrincipal) error {
	if principal == nil {
		return errors.New("principal is required")
	}
	scopes, err := json.Marshal(principal.Scopes)
	if err != nil {
		return err
	}
	cidrs, err := json.Marshal(principal.AllowedCIDRs)
	if err != nil {
		return err
	}
	filter := normalizedJSONObject(principal.ResourceFilter)
	ts := now()
	_, err = s.db.ExecContext(ctx, `insert into api_principals(id,owner_user_id,name,type,enabled,scopes_json,resource_filter_json,allowed_cidrs_json,rate_limit_per_minute,max_concurrency,expires_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, principal.ID, principal.OwnerUserID, principal.Name, principal.Type, boolInt(principal.Enabled), string(scopes), filter, string(cidrs), principal.RateLimitPerMinute, principal.MaxConcurrency, timePtrString(principal.ExpiresAt), ts, ts)
	if err != nil {
		return err
	}
	principal.CreatedAt, principal.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) UpdateAPIPrincipal(ctx context.Context, principal *model.APIPrincipal) error {
	if principal == nil {
		return errors.New("principal is required")
	}
	scopes, err := json.Marshal(principal.Scopes)
	if err != nil {
		return err
	}
	cidrs, err := json.Marshal(principal.AllowedCIDRs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `update api_principals set name=?,enabled=?,scopes_json=?,resource_filter_json=?,allowed_cidrs_json=?,rate_limit_per_minute=?,max_concurrency=?,expires_at=?,updated_at=? where id=?`, principal.Name, boolInt(principal.Enabled), string(scopes), normalizedJSONObject(principal.ResourceFilter), string(cidrs), principal.RateLimitPerMinute, principal.MaxConcurrency, timePtrString(principal.ExpiresAt), now(), principal.ID)
	return err
}

func (s *Store) ListAPIPrincipals(ctx context.Context) ([]model.APIPrincipal, error) {
	rows, err := s.db.QueryContext(ctx, principalSelectSQL+` order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.APIPrincipal{}
	for rows.Next() {
		item, err := scanAPIPrincipal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) GetAPIPrincipal(ctx context.Context, id string) (*model.APIPrincipal, error) {
	return scanAPIPrincipal(s.db.QueryRowContext(ctx, principalSelectSQL+` where id=?`, id))
}

func (s *Store) DeleteAPIPrincipal(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `delete from api_principals where id=? and type='service_account'`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func scanAPIPrincipal(scanner interface{ Scan(...any) error }) (*model.APIPrincipal, error) {
	var item model.APIPrincipal
	var owner sql.NullInt64
	var enabled int
	var scopesJSON, filterJSON, cidrsJSON string
	var expires, lastUsed sql.NullString
	var created, updated string
	if err := scanner.Scan(&item.ID, &owner, &item.Name, &item.Type, &enabled, &scopesJSON, &filterJSON, &cidrsJSON, &item.RateLimitPerMinute, &item.MaxConcurrency, &expires, &lastUsed, &created, &updated); err != nil {
		return nil, err
	}
	if owner.Valid {
		item.OwnerUserID = &owner.Int64
	}
	item.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(scopesJSON), &item.Scopes)
	_ = json.Unmarshal([]byte(cidrsJSON), &item.AllowedCIDRs)
	item.ResourceFilter = json.RawMessage(filterJSON)
	item.ExpiresAt = nullableTime(expires)
	item.LastUsedAt = nullableTime(lastUsed)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, token *model.APIToken) error {
	if token == nil {
		return errors.New("token is required")
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into api_tokens(id,principal_id,token_hash,prefix,expires_at,created_at) values(?,?,?,?,?,?)`, token.ID, token.PrincipalID, token.TokenHash, token.Prefix, token.ExpiresAt.UTC().Format(time.RFC3339Nano), ts)
	if err == nil {
		token.CreatedAt = parseTime(ts)
	}
	return err
}

func (s *Store) ListAPITokens(ctx context.Context, principalID string) ([]model.APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `select id,principal_id,prefix,expires_at,revoked_at,last_used_at,created_at from api_tokens where principal_id=? order by created_at desc`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.APIToken{}
	for rows.Next() {
		var item model.APIToken
		var expires, created string
		var revoked, lastUsed sql.NullString
		if err := rows.Scan(&item.ID, &item.PrincipalID, &item.Prefix, &expires, &revoked, &lastUsed, &created); err != nil {
			return nil, err
		}
		item.ExpiresAt, item.CreatedAt = parseTime(expires), parseTime(created)
		item.RevokedAt, item.LastUsedAt = nullableTime(revoked), nullableTime(lastUsed)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) AuthenticateAPIToken(ctx context.Context, tokenHash string, at time.Time) (*model.APIPrincipal, *model.APIToken, error) {
	row := s.db.QueryRowContext(ctx, `select t.id,t.principal_id,t.prefix,t.expires_at,t.revoked_at,t.last_used_at,t.created_at,p.id,p.owner_user_id,p.name,p.type,p.enabled,p.scopes_json,p.resource_filter_json,p.allowed_cidrs_json,p.rate_limit_per_minute,p.max_concurrency,p.expires_at,p.last_used_at,p.created_at,p.updated_at from api_tokens t join api_principals p on p.id=t.principal_id where t.token_hash=?`, tokenHash)
	var token model.APIToken
	var tokenExpires, tokenCreated string
	var tokenRevoked, tokenLastUsed sql.NullString
	var principal model.APIPrincipal
	var owner sql.NullInt64
	var enabled int
	var scopesJSON, filterJSON, cidrsJSON string
	var principalExpires, principalLastUsed sql.NullString
	var principalCreated, principalUpdated string
	if err := row.Scan(&token.ID, &token.PrincipalID, &token.Prefix, &tokenExpires, &tokenRevoked, &tokenLastUsed, &tokenCreated, &principal.ID, &owner, &principal.Name, &principal.Type, &enabled, &scopesJSON, &filterJSON, &cidrsJSON, &principal.RateLimitPerMinute, &principal.MaxConcurrency, &principalExpires, &principalLastUsed, &principalCreated, &principalUpdated); err != nil {
		return nil, nil, err
	}
	token.ExpiresAt, token.RevokedAt = parseTime(tokenExpires), nullableTime(tokenRevoked)
	token.LastUsedAt, token.CreatedAt = nullableTime(tokenLastUsed), parseTime(tokenCreated)
	principal.Enabled = enabled != 0
	principal.OwnerUserID = nil
	if owner.Valid {
		principal.OwnerUserID = &owner.Int64
	}
	_ = json.Unmarshal([]byte(scopesJSON), &principal.Scopes)
	_ = json.Unmarshal([]byte(cidrsJSON), &principal.AllowedCIDRs)
	principal.ResourceFilter = json.RawMessage(filterJSON)
	principal.ExpiresAt, principal.LastUsedAt = nullableTime(principalExpires), nullableTime(principalLastUsed)
	principal.CreatedAt, principal.UpdatedAt = parseTime(principalCreated), parseTime(principalUpdated)
	if !principal.Enabled || token.RevokedAt != nil || !token.ExpiresAt.After(at) || principal.ExpiresAt != nil && !principal.ExpiresAt.After(at) {
		return nil, nil, sql.ErrNoRows
	}
	ts := at.UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx, `update api_tokens set last_used_at=? where id=?`, ts, token.ID)
	_, _ = s.db.ExecContext(ctx, `update api_principals set last_used_at=? where id=?`, ts, principal.ID)
	return &principal, &token, nil
}

func (s *Store) RevokeAPIToken(ctx context.Context, principalID, tokenID string) error {
	result, err := s.db.ExecContext(ctx, `update api_tokens set revoked_at=? where id=? and principal_id=? and revoked_at is null`, now(), tokenID, principalID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateToolCallAudit(ctx context.Context, item *model.ToolCallAudit) error {
	if item == nil {
		return errors.New("tool call audit is required")
	}
	created := now()
	_, err := s.db.ExecContext(ctx, `insert into tool_call_audits(id,principal_id,client_name,model_provider,capability,scope,data_classification,affected_resources_json,approval_id,request_id,arguments_hash,result,source_ip,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.PrincipalID, item.ClientName, item.ModelProvider, item.Capability, item.Scope, item.DataClassification, normalizedJSONObject(item.AffectedResources), item.ApprovalID, item.RequestID, item.ArgumentsHash, item.Result, item.SourceIP, created)
	if err == nil {
		item.CreatedAt = parseTime(created)
	}
	return err
}

func (s *Store) ListToolCallAudits(ctx context.Context, principalID string, limit int) ([]model.ToolCallAudit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `select id,principal_id,client_name,model_provider,capability,scope,data_classification,affected_resources_json,approval_id,request_id,arguments_hash,result,source_ip,created_at from tool_call_audits`
	args := []any{}
	if principalID != "" {
		query += ` where principal_id=?`
		args = append(args, principalID)
	}
	query += ` order by created_at desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ToolCallAudit{}
	for rows.Next() {
		var item model.ToolCallAudit
		var resources, created string
		if err := rows.Scan(&item.ID, &item.PrincipalID, &item.ClientName, &item.ModelProvider, &item.Capability, &item.Scope, &item.DataClassification, &resources, &item.ApprovalID, &item.RequestID, &item.ArgumentsHash, &item.Result, &item.SourceIP, &created); err != nil {
			return nil, err
		}
		item.AffectedResources = json.RawMessage(resources)
		item.CreatedAt = parseTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpsertApprovalPolicy(ctx context.Context, policy *model.ApprovalPolicy) error {
	if policy == nil {
		return errors.New("policy is required")
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into approval_policies(id,principal_id,capability,resource_filter_json,mode,allow_risk4,expires_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?) on conflict(principal_id,capability) do update set resource_filter_json=excluded.resource_filter_json,mode=excluded.mode,allow_risk4=excluded.allow_risk4,expires_at=excluded.expires_at,updated_at=excluded.updated_at`, policy.ID, policy.PrincipalID, policy.Capability, normalizedJSONObject(policy.ResourceFilter), policy.Mode, boolInt(policy.AllowRisk4), timePtrString(policy.ExpiresAt), ts, ts)
	return err
}

func (s *Store) GetApprovalPolicy(ctx context.Context, principalID, capability string, at time.Time) (*model.ApprovalPolicy, error) {
	var item model.ApprovalPolicy
	var filterJSON, created, updated string
	var risk4 int
	var expires sql.NullString
	err := s.db.QueryRowContext(ctx, `select id,principal_id,capability,resource_filter_json,mode,allow_risk4,expires_at,created_at,updated_at from approval_policies where principal_id=? and capability=?`, principalID, capability).Scan(&item.ID, &item.PrincipalID, &item.Capability, &filterJSON, &item.Mode, &risk4, &expires, &created, &updated)
	if err != nil {
		return nil, err
	}
	item.ResourceFilter = json.RawMessage(filterJSON)
	item.AllowRisk4 = risk4 != 0
	item.ExpiresAt = nullableTime(expires)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	if item.ExpiresAt != nil && !item.ExpiresAt.After(at) {
		return nil, sql.ErrNoRows
	}
	return &item, nil
}

func (s *Store) ListApprovalPolicies(ctx context.Context, principalID string) ([]model.ApprovalPolicy, error) {
	query := `select id,principal_id,capability,resource_filter_json,mode,allow_risk4,expires_at,created_at,updated_at from approval_policies`
	args := []any{}
	if principalID != "" {
		query += ` where principal_id=?`
		args = append(args, principalID)
	}
	query += ` order by principal_id,capability`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.ApprovalPolicy{}
	for rows.Next() {
		var item model.ApprovalPolicy
		var filterJSON, created, updated string
		var risk4 int
		var expires sql.NullString
		if err := rows.Scan(&item.ID, &item.PrincipalID, &item.Capability, &filterJSON, &item.Mode, &risk4, &expires, &created, &updated); err != nil {
			return nil, err
		}
		item.ResourceFilter = json.RawMessage(filterJSON)
		item.AllowRisk4 = risk4 != 0
		item.ExpiresAt = nullableTime(expires)
		item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteApprovalPolicy(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `delete from approval_policies where id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateAutomationChangeset(ctx context.Context, changeset *model.AutomationChangeset) error {
	if changeset == nil || len(changeset.Operations) == 0 {
		return errors.New("changeset operations are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	_, err = tx.ExecContext(ctx, `insert into automation_changesets(id,principal_id,actor_user_id,status,reason,idempotency_key,base_revisions_json,plan_hash,risk_class,auto_apply,validation_json,blast_radius_json,result_json,expires_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, changeset.ID, changeset.PrincipalID, changeset.ActorUserID, changeset.Status, changeset.Reason, changeset.IdempotencyKey, normalizedJSONObject(changeset.BaseRevisions), changeset.PlanHash, changeset.RiskClass, boolInt(changeset.AutoApply), normalizedJSONObject(changeset.Validation), normalizedJSONObject(changeset.BlastRadius), normalizedJSONObject(changeset.Result), changeset.ExpiresAt.UTC().Format(time.RFC3339Nano), ts, ts)
	if err != nil {
		return err
	}
	for index := range changeset.Operations {
		op := &changeset.Operations[index]
		secretRefs, err := json.Marshal(op.SecretRefs)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into automation_operations(id,changeset_id,position,capability,input_json,secret_refs_json,resource_refs_json,risk_class,status,result_json,error_code,error_message,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, changeset.ID, op.Position, op.Capability, string(op.Input), string(secretRefs), normalizedJSONObject(op.ResourceRefs), op.RiskClass, op.Status, normalizedJSONObject(op.Result), op.ErrorCode, op.ErrorMessage, ts)
		if err != nil {
			return err
		}
		op.CreatedAt = parseTime(ts)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	changeset.CreatedAt, changeset.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) GetAutomationChangeset(ctx context.Context, id string) (*model.AutomationChangeset, error) {
	item, err := scanAutomationChangeset(s.db.QueryRowContext(ctx, `select id,principal_id,actor_user_id,status,reason,idempotency_key,base_revisions_json,plan_hash,risk_class,auto_apply,validation_json,blast_radius_json,result_json,expires_at,created_at,updated_at,validated_at,approved_at,applied_at,completed_at from automation_changesets where id=?`, id))
	if err != nil {
		return nil, err
	}
	items, err := s.listAutomationOperations(ctx, id)
	if err != nil {
		return nil, err
	}
	item.Operations = items
	return item, nil
}

func (s *Store) FindAutomationChangesetByIdempotency(ctx context.Context, principalID, key string) (*model.AutomationChangeset, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `select id from automation_changesets where principal_id=? and idempotency_key=?`, principalID, key).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetAutomationChangeset(ctx, id)
}

func (s *Store) UpdateAutomationChangeset(ctx context.Context, item *model.AutomationChangeset) error {
	if item == nil {
		return errors.New("changeset is required")
	}
	_, err := s.db.ExecContext(ctx, `update automation_changesets set status=?,plan_hash=?,risk_class=?,auto_apply=?,validation_json=?,blast_radius_json=?,result_json=?,updated_at=?,validated_at=?,approved_at=?,applied_at=?,completed_at=? where id=?`, item.Status, item.PlanHash, item.RiskClass, boolInt(item.AutoApply), normalizedJSONObject(item.Validation), normalizedJSONObject(item.BlastRadius), normalizedJSONObject(item.Result), now(), timePtrString(item.ValidatedAt), timePtrString(item.ApprovedAt), timePtrString(item.AppliedAt), timePtrString(item.CompletedAt), item.ID)
	return err
}

func (s *Store) ClaimAutomationChangesetApply(ctx context.Context, id string, appliedAt time.Time) (bool, error) {
	stamp := appliedAt.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `update automation_changesets set status=?,applied_at=?,updated_at=? where id=? and status=?`, model.ChangesetApplying, stamp, stamp, id, model.ChangesetApproved)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) CreateAutomationApproval(ctx context.Context, approval *model.AutomationApproval) error {
	if approval == nil {
		return errors.New("approval is required")
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into automation_approvals(id,changeset_id,approver_id,decision,plan_hash,comment,approved_risk,created_at) values(?,?,?,?,?,?,?,?)`, approval.ID, approval.ChangesetID, approval.ApproverID, approval.Decision, approval.PlanHash, approval.Comment, approval.ApprovedRisk, ts)
	if err == nil {
		approval.CreatedAt = parseTime(ts)
	}
	return err
}

func (s *Store) UpdateAutomationOperation(ctx context.Context, operation *model.AutomationOperation) error {
	if operation == nil {
		return errors.New("operation is required")
	}
	_, err := s.db.ExecContext(ctx, `update automation_operations set status=?,result_json=?,error_code=?,error_message=?,completed_at=? where id=?`, operation.Status, normalizedJSONObject(operation.Result), operation.ErrorCode, operation.ErrorMessage, timePtrString(operation.CompletedAt), operation.ID)
	return err
}

func (s *Store) ListAutomationChangesets(ctx context.Context, principalID string, limit int) ([]model.AutomationChangeset, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `select id,principal_id,actor_user_id,status,reason,idempotency_key,base_revisions_json,plan_hash,risk_class,auto_apply,validation_json,blast_radius_json,result_json,expires_at,created_at,updated_at,validated_at,approved_at,applied_at,completed_at from automation_changesets`
	args := []any{}
	if principalID != "" {
		query += ` where principal_id=?`
		args = append(args, principalID)
	}
	query += ` order by created_at desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AutomationChangeset{}
	for rows.Next() {
		item, err := scanAutomationChangeset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func scanAutomationChangeset(scanner interface{ Scan(...any) error }) (*model.AutomationChangeset, error) {
	var item model.AutomationChangeset
	var actor sql.NullInt64
	var autoApply int
	var base, validation, blast, result, expires, created, updated string
	var validated, approved, applied, completed sql.NullString
	if err := scanner.Scan(&item.ID, &item.PrincipalID, &actor, &item.Status, &item.Reason, &item.IdempotencyKey, &base, &item.PlanHash, &item.RiskClass, &autoApply, &validation, &blast, &result, &expires, &created, &updated, &validated, &approved, &applied, &completed); err != nil {
		return nil, err
	}
	if actor.Valid {
		item.ActorUserID = &actor.Int64
	}
	item.AutoApply = autoApply != 0
	item.BaseRevisions, item.Validation = json.RawMessage(base), json.RawMessage(validation)
	item.BlastRadius, item.Result = json.RawMessage(blast), json.RawMessage(result)
	item.ExpiresAt, item.CreatedAt, item.UpdatedAt = parseTime(expires), parseTime(created), parseTime(updated)
	item.ValidatedAt, item.ApprovedAt = nullableTime(validated), nullableTime(approved)
	item.AppliedAt, item.CompletedAt = nullableTime(applied), nullableTime(completed)
	return &item, nil
}

func (s *Store) listAutomationOperations(ctx context.Context, changesetID string) ([]model.AutomationOperation, error) {
	rows, err := s.db.QueryContext(ctx, `select id,changeset_id,position,capability,input_json,secret_refs_json,resource_refs_json,risk_class,status,result_json,error_code,error_message,created_at,completed_at from automation_operations where changeset_id=? order by position`, changesetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AutomationOperation{}
	for rows.Next() {
		var item model.AutomationOperation
		var input, secrets, resources, result, created string
		var completed sql.NullString
		if err := rows.Scan(&item.ID, &item.ChangesetID, &item.Position, &item.Capability, &input, &secrets, &resources, &item.RiskClass, &item.Status, &result, &item.ErrorCode, &item.ErrorMessage, &created, &completed); err != nil {
			return nil, err
		}
		item.Input, item.ResourceRefs, item.Result = json.RawMessage(input), json.RawMessage(resources), json.RawMessage(result)
		_ = json.Unmarshal([]byte(secrets), &item.SecretRefs)
		item.CreatedAt, item.CompletedAt = parseTime(created), nullableTime(completed)
		out = append(out, item)
	}
	return out, rows.Err()
}

func normalizedJSONObject(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	return string(raw)
}

func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	t := parseTime(value.String)
	return &t
}
