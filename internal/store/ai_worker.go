package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) CreateAIProvider(ctx context.Context, item *model.AIProvider) error {
	if item == nil {
		return errors.New("AI provider is required")
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into ai_providers(id,name,base_url,model,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Name, item.BaseURL, item.Model, item.CredentialEncrypted, boolInt(item.Enabled), boolInt(item.AllowRawAudit), item.DailyTokenLimit, ts, ts)
	if err == nil {
		item.HasCredential = item.CredentialEncrypted != ""
		item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	}
	return err
}

func (s *Store) ListAIProviders(ctx context.Context) ([]model.AIProvider, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,base_url,model,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,last_used_at,created_at,updated_at from ai_providers order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AIProvider{}
	for rows.Next() {
		item, err := scanAIProvider(rows)
		if err != nil {
			return nil, err
		}
		item.CredentialEncrypted = ""
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) GetAIProvider(ctx context.Context, id string) (*model.AIProvider, error) {
	return scanAIProvider(s.db.QueryRowContext(ctx, `select id,name,base_url,model,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,last_used_at,created_at,updated_at from ai_providers where id=?`, id))
}

func (s *Store) UpdateAIProvider(ctx context.Context, item *model.AIProvider) error {
	result, err := s.db.ExecContext(ctx, `update ai_providers set name=?,base_url=?,model=?,credential_encrypted=?,enabled=?,allow_raw_audit=?,daily_token_limit=?,updated_at=? where id=?`, item.Name, item.BaseURL, item.Model, item.CredentialEncrypted, boolInt(item.Enabled), boolInt(item.AllowRawAudit), item.DailyTokenLimit, now(), item.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteAIProvider(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `delete from ai_providers where id=? and not exists(select 1 from ai_analysis_jobs where provider_id=?)`, id, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListAIAnalysisJobs(ctx context.Context, limit int) ([]model.AIAnalysisJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `select id,kind,coalesce(incident_id,''),provider_id,fingerprint,status,input_json,output_json,error,attempts,input_tokens,output_tokens,lease_owner,lease_until,created_at,updated_at,completed_at from ai_analysis_jobs order by created_at desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.AIAnalysisJob{}
	for rows.Next() {
		item, err := scanAIJob(rows)
		if err != nil {
			return nil, err
		}
		item.Input = nil
		item.Output = nil
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) ListAIFindings(ctx context.Context, incidentID string, limit int) ([]model.AIFinding, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `select id,job_id,incident_id,classification,confidence,evidence_json,counter_evidence_json,recommended_actions_json,summary,provider_id,model,prompt_version,created_at from ai_findings`
	args := []any{}
	if incidentID != "" {
		query += ` where incident_id=?`
		args = append(args, incidentID)
	}
	query += ` order by created_at desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.AIFinding{}
	for rows.Next() {
		var item model.AIFinding
		var evidence, counter, actions, created string
		if err := rows.Scan(&item.ID, &item.JobID, &item.IncidentID, &item.Classification, &item.Confidence, &evidence, &counter, &actions, &item.Summary, &item.ProviderID, &item.Model, &item.PromptVersion, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &item.EvidenceRefs)
		_ = json.Unmarshal([]byte(counter), &item.CounterEvidence)
		_ = json.Unmarshal([]byte(actions), &item.RecommendedActions)
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanAIProvider(scanner interface{ Scan(...any) error }) (*model.AIProvider, error) {
	var item model.AIProvider
	var enabled, raw int
	var last sql.NullString
	var created, updated string
	if err := scanner.Scan(&item.ID, &item.Name, &item.BaseURL, &item.Model, &item.CredentialEncrypted, &enabled, &raw, &item.DailyTokenLimit, &last, &created, &updated); err != nil {
		return nil, err
	}
	item.Enabled, item.AllowRawAudit, item.HasCredential = enabled != 0, raw != 0, item.CredentialEncrypted != ""
	item.LastUsedAt, item.CreatedAt, item.UpdatedAt = nullableTime(last), parseTime(created), parseTime(updated)
	return &item, nil
}

func (s *Store) LeaseAIAnalysisJob(ctx context.Context, owner string, at time.Time, lease time.Duration) (*model.AIAnalysisJob, *model.AIProvider, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	cutoff := at.UTC().Format(time.RFC3339Nano)
	var id string
	err = tx.QueryRowContext(ctx, `select j.id from ai_analysis_jobs j join ai_providers p on p.id=j.provider_id where p.enabled=1 and p.credential_encrypted<>'' and (j.status='pending' or (j.status='running' and j.lease_until<?)) and (p.daily_token_limit<=0 or coalesce((select sum(input_tokens+output_tokens) from ai_analysis_jobs used where used.provider_id=p.id and used.completed_at>=?),0)<p.daily_token_limit) order by j.created_at limit 1`, cutoff, at.UTC().Truncate(24*time.Hour).Format(time.RFC3339Nano)).Scan(&id)
	if err != nil {
		return nil, nil, err
	}
	leaseUntil := at.UTC().Add(lease).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `update ai_analysis_jobs set status='running',attempts=attempts+1,lease_owner=?,lease_until=?,updated_at=? where id=? and (status='pending' or lease_until<?)`, owner, leaseUntil, cutoff, id, cutoff)
	if err != nil {
		return nil, nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, nil, sql.ErrNoRows
	}
	job, err := scanAIJob(tx.QueryRowContext(ctx, `select id,kind,coalesce(incident_id,''),provider_id,fingerprint,status,input_json,output_json,error,attempts,input_tokens,output_tokens,lease_owner,lease_until,created_at,updated_at,completed_at from ai_analysis_jobs where id=?`, id))
	if err != nil {
		return nil, nil, err
	}
	provider, err := scanAIProvider(tx.QueryRowContext(ctx, `select id,name,base_url,model,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,last_used_at,created_at,updated_at from ai_providers where id=?`, job.ProviderID))
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return job, provider, nil
}

func scanAIJob(scanner interface{ Scan(...any) error }) (*model.AIAnalysisJob, error) {
	var item model.AIAnalysisJob
	var input, output, created, updated string
	var leaseUntil, completed sql.NullString
	if err := scanner.Scan(&item.ID, &item.Kind, &item.IncidentID, &item.ProviderID, &item.Fingerprint, &item.Status, &input, &output, &item.Error, &item.Attempts, &item.InputTokens, &item.OutputTokens, &item.LeaseOwner, &leaseUntil, &created, &updated, &completed); err != nil {
		return nil, err
	}
	item.Input, item.Output = json.RawMessage(input), json.RawMessage(output)
	item.LeaseUntil, item.CompletedAt = nullableTime(leaseUntil), nullableTime(completed)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

func (s *Store) CompleteAIAnalysisJob(ctx context.Context, owner string, job *model.AIAnalysisJob, finding *model.AIFinding) error {
	if job == nil || finding == nil {
		return errors.New("job and finding are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	evidence, _ := json.Marshal(finding.EvidenceRefs)
	counter, _ := json.Marshal(finding.CounterEvidence)
	actions, _ := json.Marshal(finding.RecommendedActions)
	ts := now()
	var expectedIncidentID, expectedProviderID string
	if err := tx.QueryRowContext(ctx, `select coalesce(incident_id,''),provider_id from ai_analysis_jobs where id=? and status='running' and lease_owner=?`, job.ID, owner).Scan(&expectedIncidentID, &expectedProviderID); err != nil {
		return err
	}
	if finding.JobID != job.ID || finding.IncidentID != expectedIncidentID || finding.ProviderID != expectedProviderID {
		return errors.New("AI finding does not match the leased job")
	}
	if _, err := tx.ExecContext(ctx, `insert into ai_findings(id,job_id,incident_id,classification,confidence,evidence_json,counter_evidence_json,recommended_actions_json,summary,provider_id,model,prompt_version,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, finding.ID, job.ID, finding.IncidentID, finding.Classification, finding.Confidence, string(evidence), string(counter), string(actions), finding.Summary, finding.ProviderID, finding.Model, finding.PromptVersion, ts); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `update ai_analysis_jobs set status='succeeded',output_json=?,error='',input_tokens=?,output_tokens=?,lease_owner='',lease_until=null,updated_at=?,completed_at=? where id=? and status='running' and lease_owner=?`, normalizedJSONObject(job.Output), job.InputTokens, job.OutputTokens, ts, ts, job.ID, owner)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	_, _ = tx.ExecContext(ctx, `update audit_incidents set classification=?,updated_at=? where id=?`, finding.Classification, ts, finding.IncidentID)
	_, _ = tx.ExecContext(ctx, `update ai_providers set last_used_at=?,updated_at=? where id=?`, ts, ts, finding.ProviderID)
	return tx.Commit()
}

func (s *Store) FailAIAnalysisJob(ctx context.Context, owner, id, message string) error {
	var attempts int
	if err := s.db.QueryRowContext(ctx, `select attempts from ai_analysis_jobs where id=? and status='running' and lease_owner=?`, id, owner).Scan(&attempts); err != nil {
		return err
	}
	status := "pending"
	if attempts >= 3 {
		status = "failed"
	}
	_, err := s.db.ExecContext(ctx, `update ai_analysis_jobs set status=?,error=?,lease_owner='',lease_until=null,updated_at=?,completed_at=case when ?='failed' then ? else null end where id=? and lease_owner=?`, status, message, now(), status, now(), id, owner)
	return err
}
