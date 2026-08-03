package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) CreateAuditReview(ctx context.Context, review *model.AuditReview, evidence []model.AuditReviewEvidence, jobs []model.AuditReviewJob) error {
	if review == nil || review.ID == "" || review.RequestID == "" || review.ProviderID == "" || review.RequestedBy <= 0 || len(jobs) == 0 {
		return errors.New("audit review is incomplete")
	}
	scopeJSON, _ := json.Marshal(review.Scope)
	typesJSON, _ := json.Marshal(review.EvidenceTypes)
	usersJSON, _ := json.Marshal(review.ResolvedUserIDs)
	serversJSON, _ := json.Marshal(review.ResolvedServerIDs)
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into ai_audit_reviews(id,request_id,provider_id,requested_by,status,scope_json,evidence_types_json,window_started_at,window_ended_at,snapshot_at,privacy_mode,resolved_user_ids_json,resolved_server_ids_json,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		review.ID, review.RequestID, review.ProviderID, review.RequestedBy, "queued", string(scopeJSON), string(typesJSON), review.WindowStartedAt.UTC().Format(time.RFC3339Nano), review.WindowEndedAt.UTC().Format(time.RFC3339Nano), review.SnapshotAt.UTC().Format(time.RFC3339Nano), review.PrivacyMode, string(usersJSON), string(serversJSON), ts, ts); err != nil {
		return err
	}
	for _, item := range evidence {
		if _, err := tx.ExecContext(ctx, `insert into ai_audit_review_evidence(ref,review_id,kind,user_id,server_id,payload_json,created_at) values(?,?,?,?,?,?,?)`, item.Ref, review.ID, item.Kind, item.UserID, item.ServerID, normalizedJSONObject(item.Payload), ts); err != nil {
			return err
		}
	}
	for _, job := range jobs {
		if _, err := tx.ExecContext(ctx, `insert into ai_audit_review_jobs(id,review_id,provider_id,stage,position,kind,status,input_json,created_at,updated_at) values(?,?,?,?,?,?,'pending',?,?,?)`, job.ID, review.ID, review.ProviderID, job.Stage, job.Position, job.Kind, normalizedJSONObject(job.Input), ts, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListAuditReviews(ctx context.Context, limit int) ([]model.AuditReview, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, auditReviewSelect+` order by r.created_at desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.AuditReview{}
	for rows.Next() {
		item, err := scanAuditReview(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) GetAuditReview(ctx context.Context, id string) (*model.AuditReview, error) {
	return scanAuditReview(s.db.QueryRowContext(ctx, auditReviewSelect+` where r.id=?`, id))
}

func (s *Store) GetAuditReviewByRequestID(ctx context.Context, requestID string) (*model.AuditReview, error) {
	return scanAuditReview(s.db.QueryRowContext(ctx, auditReviewSelect+` where r.request_id=?`, requestID))
}

const auditReviewSelect = `select r.id,r.request_id,r.provider_id,r.requested_by,r.status,r.scope_json,r.evidence_types_json,r.window_started_at,r.window_ended_at,r.snapshot_at,r.privacy_mode,r.resolved_user_ids_json,r.resolved_server_ids_json,
	(select count(*) from ai_audit_review_jobs j where j.review_id=r.id),(select count(*) from ai_audit_review_jobs j where j.review_id=r.id and j.status='succeeded'),r.input_tokens,r.output_tokens,r.final_output_json,r.error,r.created_at,r.updated_at,r.completed_at from ai_audit_reviews r`

func scanAuditReview(scanner interface{ Scan(...any) error }) (*model.AuditReview, error) {
	var item model.AuditReview
	var scopeJSON, typesJSON, usersJSON, serversJSON, outputJSON string
	var windowStart, windowEnd, snapshot, created, updated string
	var completed sql.NullString
	if err := scanner.Scan(&item.ID, &item.RequestID, &item.ProviderID, &item.RequestedBy, &item.Status, &scopeJSON, &typesJSON, &windowStart, &windowEnd, &snapshot, &item.PrivacyMode, &usersJSON, &serversJSON, &item.JobCount, &item.CompletedJobCount, &item.InputTokens, &item.OutputTokens, &outputJSON, &item.Error, &created, &updated, &completed); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopeJSON), &item.Scope)
	_ = json.Unmarshal([]byte(typesJSON), &item.EvidenceTypes)
	_ = json.Unmarshal([]byte(usersJSON), &item.ResolvedUserIDs)
	_ = json.Unmarshal([]byte(serversJSON), &item.ResolvedServerIDs)
	if strings.TrimSpace(outputJSON) != "" && strings.TrimSpace(outputJSON) != "{}" {
		item.FinalOutput = json.RawMessage(outputJSON)
	}
	item.WindowStartedAt, item.WindowEndedAt, item.SnapshotAt = parseTime(windowStart), parseTime(windowEnd), parseTime(snapshot)
	item.CreatedAt, item.UpdatedAt, item.CompletedAt = parseTime(created), parseTime(updated), nullableTime(completed)
	return &item, nil
}

func (s *Store) ListAuditReviewEvidence(ctx context.Context, reviewID string, offset, limit int) ([]model.AuditReviewEvidence, int, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `select count(*) from ai_audit_review_evidence where review_id=?`, reviewID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `select ref,review_id,kind,user_id,server_id,payload_json,created_at from ai_audit_review_evidence where review_id=? order by kind,ref limit ? offset ?`, reviewID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []model.AuditReviewEvidence{}
	for rows.Next() {
		var item model.AuditReviewEvidence
		var userID, serverID sql.NullInt64
		var payload, created string
		if err := rows.Scan(&item.Ref, &item.ReviewID, &item.Kind, &userID, &serverID, &payload, &created); err != nil {
			return nil, 0, err
		}
		if userID.Valid {
			item.UserID = &userID.Int64
		}
		if serverID.Valid {
			item.ServerID = &serverID.Int64
		}
		item.Payload, item.CreatedAt = json.RawMessage(payload), parseTime(created)
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Store) ListAuditReviewJobs(ctx context.Context, reviewID string, includePayload bool) ([]model.AuditReviewJob, error) {
	rows, err := s.db.QueryContext(ctx, `select id,review_id,provider_id,stage,position,kind,status,input_json,output_json,error,error_detail,attempts,input_tokens,output_tokens,lease_owner,lease_until,created_at,updated_at,completed_at from ai_audit_review_jobs where review_id=? order by stage,position`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.AuditReviewJob{}
	for rows.Next() {
		item, err := scanAuditReviewJob(rows)
		if err != nil {
			return nil, err
		}
		if !includePayload {
			item.Input, item.Output = nil, nil
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) GetAuditReviewJob(ctx context.Context, reviewID, jobID string) (*model.AuditReviewJob, error) {
	return scanAuditReviewJob(s.db.QueryRowContext(ctx, `select id,review_id,provider_id,stage,position,kind,status,input_json,output_json,error,error_detail,attempts,input_tokens,output_tokens,lease_owner,lease_until,created_at,updated_at,completed_at from ai_audit_review_jobs where review_id=? and id=?`, reviewID, jobID))
}

func (s *Store) GetAuditReviewJobByID(ctx context.Context, jobID string) (*model.AuditReviewJob, error) {
	return scanAuditReviewJob(s.db.QueryRowContext(ctx, `select id,review_id,provider_id,stage,position,kind,status,input_json,output_json,error,error_detail,attempts,input_tokens,output_tokens,lease_owner,lease_until,created_at,updated_at,completed_at from ai_audit_review_jobs where id=?`, jobID))
}

func scanAuditReviewJob(scanner interface{ Scan(...any) error }) (*model.AuditReviewJob, error) {
	var item model.AuditReviewJob
	var input, output, errorDetail, created, updated string
	var lease, completed sql.NullString
	if err := scanner.Scan(&item.ID, &item.ReviewID, &item.ProviderID, &item.Stage, &item.Position, &item.Kind, &item.Status, &input, &output, &item.Error, &errorDetail, &item.Attempts, &item.InputTokens, &item.OutputTokens, &item.LeaseOwner, &lease, &created, &updated, &completed); err != nil {
		return nil, err
	}
	item.Input, item.Output = json.RawMessage(input), json.RawMessage(output)
	if errorDetail != "" {
		item.ErrorDetail = json.RawMessage(errorDetail)
	}
	item.LeaseUntil, item.CompletedAt = nullableTime(lease), nullableTime(completed)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

func (s *Store) LeaseAuditReviewJob(ctx context.Context, owner string, at time.Time, lease time.Duration) (*model.AuditReviewJob, *model.AIProvider, error) {
	cutoff := at.UTC().Format(time.RFC3339Nano)
	dayStart := at.UTC().Truncate(24 * time.Hour).Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `select j.id from ai_audit_review_jobs j join ai_audit_reviews r on r.id=j.review_id join ai_providers p on p.id=j.provider_id
		where r.status in ('queued','running') and p.enabled=1 and p.credential_encrypted<>'' and (j.status='pending' or (j.status='running' and j.lease_until<?))
		and (p.daily_token_limit<=0 or coalesce((select sum(x.input_tokens+x.output_tokens) from ai_audit_review_jobs x where x.provider_id=p.id and x.completed_at>=?),0)<p.daily_token_limit)
		order by j.stage,j.created_at,j.position limit 1`, cutoff, dayStart).Scan(&id)
	if err != nil {
		return nil, nil, err
	}
	leaseUntil := at.UTC().Add(lease).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `update ai_audit_review_jobs set status='running',attempts=attempts+1,lease_owner=?,lease_until=?,updated_at=? where id=? and (status='pending' or (status='running' and lease_until<?))`, owner, leaseUntil, cutoff, id, cutoff)
	if err != nil {
		return nil, nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, nil, sql.ErrNoRows
	}
	job, err := scanAuditReviewJob(tx.QueryRowContext(ctx, `select id,review_id,provider_id,stage,position,kind,status,input_json,output_json,error,error_detail,attempts,input_tokens,output_tokens,lease_owner,lease_until,created_at,updated_at,completed_at from ai_audit_review_jobs where id=?`, id))
	if err != nil {
		return nil, nil, err
	}
	provider, err := scanAIProvider(tx.QueryRowContext(ctx, `select id,name,base_url,model,api_format,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,last_used_at,created_at,updated_at from ai_providers where id=?`, job.ProviderID))
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `update ai_audit_reviews set status='running',updated_at=? where id=? and status='queued'`, cutoff, job.ReviewID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return job, provider, nil
}

func (s *Store) CompleteAuditReviewJob(ctx context.Context, owner, jobID string, output json.RawMessage, inputTokens, outputTokens int64) (*model.AuditReviewJob, error) {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update ai_audit_review_jobs set status='succeeded',output_json=?,error='',input_tokens=?,output_tokens=?,lease_owner='',lease_until=null,updated_at=?,completed_at=? where id=? and status='running' and lease_owner=?`, normalizedJSONObject(output), inputTokens, outputTokens, ts, ts, jobID, owner)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, sql.ErrNoRows
	}
	var reviewID, providerID string
	if err := tx.QueryRowContext(ctx, `select review_id,provider_id from ai_audit_review_jobs where id=?`, jobID).Scan(&reviewID, &providerID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update ai_audit_reviews set input_tokens=input_tokens+?,output_tokens=output_tokens+?,updated_at=? where id=?`, inputTokens, outputTokens, ts, reviewID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update ai_providers set last_used_at=?,updated_at=? where id=?`, ts, ts, providerID); err != nil {
		return nil, err
	}
	job, err := scanAuditReviewJob(tx.QueryRowContext(ctx, `select id,review_id,provider_id,stage,position,kind,status,input_json,output_json,error,error_detail,attempts,input_tokens,output_tokens,lease_owner,lease_until,created_at,updated_at,completed_at from ai_audit_review_jobs where id=?`, jobID))
	if err != nil {
		return nil, err
	}
	return job, tx.Commit()
}

func (s *Store) FailAuditReviewJob(ctx context.Context, owner, jobID, message string, detail json.RawMessage) error {
	var attempts int
	var reviewID string
	if err := s.db.QueryRowContext(ctx, `select attempts,review_id from ai_audit_review_jobs where id=? and status='running' and lease_owner=?`, jobID, owner).Scan(&attempts, &reviewID); err != nil {
		return err
	}
	status := "pending"
	if attempts >= 3 {
		status = "failed"
	}
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `update ai_audit_review_jobs set status=?,error=?,error_detail=?,lease_owner='',lease_until=null,updated_at=?,completed_at=case when ?='failed' then ? else null end where id=? and status='running' and lease_owner=?`, status, message, string(detail), ts, status, ts, jobID, owner); err != nil {
		return err
	}
	if status == "failed" {
		if _, err := tx.ExecContext(ctx, `update ai_audit_reviews set status='failed',error=?,updated_at=?,completed_at=? where id=? and status not in ('cancelled','succeeded')`, message, ts, ts, reviewID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CreateAuditReviewStage(ctx context.Context, reviewID, providerID string, stage int, inputs []json.RawMessage, ids []string) error {
	if len(inputs) == 0 || len(inputs) != len(ids) {
		return errors.New("audit review stage is empty")
	}
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for index := range inputs {
		if _, err := tx.ExecContext(ctx, `insert into ai_audit_review_jobs(id,review_id,provider_id,stage,position,kind,status,input_json,created_at,updated_at) values(?,?,?,?,?,'synthesis','pending',?,?,?)`, ids[index], reviewID, providerID, stage, index, normalizedJSONObject(inputs[index]), ts, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) FinalizeAuditReview(ctx context.Context, reviewID string, output json.RawMessage) error {
	ts := now()
	result, err := s.db.ExecContext(ctx, `update ai_audit_reviews set status='succeeded',final_output_json=?,error='',updated_at=?,completed_at=? where id=? and status in ('queued','running')`, normalizedJSONObject(output), ts, ts, reviewID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CancelAuditReview(ctx context.Context, reviewID string) error {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update ai_audit_reviews set status='cancelled',error='',updated_at=?,completed_at=? where id=? and status in ('queued','running')`, ts, ts, reviewID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update ai_audit_review_jobs set status='cancelled',lease_owner='',lease_until=null,updated_at=?,completed_at=? where review_id=? and status in ('pending','running')`, ts, ts, reviewID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuditReviewEvidenceRefs(ctx context.Context, reviewID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `select ref from ai_audit_review_evidence where review_id=?`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]bool{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs[ref] = true
	}
	return refs, rows.Err()
}

func (s *Store) AuditReviewHistoricalPairs(ctx context.Context, start, end time.Time) (map[int64]map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `select distinct user_id,server_id from connection_audit_reports where ended_at>=? and ended_at<=?`, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pairs := map[int64]map[int64]bool{}
	for rows.Next() {
		var userID, serverID int64
		if err := rows.Scan(&userID, &serverID); err != nil {
			return nil, err
		}
		if pairs[userID] == nil {
			pairs[userID] = map[int64]bool{}
		}
		pairs[userID][serverID] = true
	}
	return pairs, rows.Err()
}

func (s *Store) AuditReviewData(ctx context.Context, userIDs, serverIDs []int64, start, end time.Time, types map[string]bool) (model.AuditReviewData, error) {
	data := model.AuditReviewData{Users: []model.AuditReviewUserData{}}
	byUser := map[int64]*model.AuditReviewUserData{}
	for _, id := range userIDs {
		item := &model.AuditReviewUserData{UserID: id, RecentSubscriptions: []model.SubscriptionPullAudit{}, RecentConnections: []model.ConnectionAuditReport{}, ServerBreakdown: []model.AuditReviewServerBreakdown{}, Destinations: []model.AuditReviewDestination{}}
		byUser[id] = item
		data.Users = append(data.Users, *item)
	}
	userClause := auditReviewIDClause("user_id", userIDs)
	serverClause := auditReviewIDClause("server_id", serverIDs)
	startText, endText := start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
	if types[model.AuditReviewEvidenceSubscription] {
		rows, err := s.db.QueryContext(ctx, `select user_id,count(*),sum(case when outcome='served' then 1 else 0 end),sum(case when outcome like 'denied_%' then 1 else 0 end),count(distinct source_ip),count(distinct case when source_province<>'' then source_province when source_country_code<>'' then source_country_code end),count(distinct client_name),count(distinct format),max(requested_at) from subscription_pull_audits where requested_at>=? and requested_at<=? and `+userClause+` group by user_id`, startText, endText)
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var userID int64
			var last sql.NullString
			var pulls, successful, denied int64
			var ips, regions, clients, formats int
			if err := rows.Scan(&userID, &pulls, &successful, &denied, &ips, &regions, &clients, &formats, &last); err != nil {
				rows.Close()
				return data, err
			}
			if item := byUser[userID]; item != nil {
				item.SubscriptionPulls, item.SubscriptionSuccessful, item.SubscriptionDenied = pulls, successful, denied
				item.SubscriptionSourceIPs, item.SubscriptionRegions, item.SubscriptionClients, item.SubscriptionFormats = ips, regions, clients, formats
				item.SubscriptionLastSeenAt = nullableTime(last)
			}
		}
		if err := rows.Close(); err != nil {
			return data, err
		}
		recent, err := s.db.QueryContext(ctx, `select id,user_id,source_ip,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,user_agent,client_name,format,profile_id,age_encrypted,token_kind,outcome,reason,requested_at,created_at from (select a.*,row_number() over(partition by user_id order by requested_at desc) rn from subscription_pull_audits a where requested_at>=? and requested_at<=? and `+userClause+`) where rn<=10 order by user_id,requested_at desc`, startText, endText)
		if err != nil {
			return data, err
		}
		for recent.Next() {
			var item model.SubscriptionPullAudit
			var profileID sql.NullInt64
			var age int
			var requested, created string
			if err := recent.Scan(&item.ID, &item.UserID, &item.SourceIP, &item.SourceCountryCode, &item.SourceCountry, &item.SourceProvince, &item.SourceCity, &item.SourceISP, &item.GeoDatabaseRevision, &item.UserAgent, &item.ClientName, &item.Format, &profileID, &age, &item.TokenKind, &item.Outcome, &item.Reason, &requested, &created); err != nil {
				recent.Close()
				return data, err
			}
			if profileID.Valid {
				item.ProfileID = &profileID.Int64
			}
			item.AgeEncrypted, item.RequestedAt, item.CreatedAt = age != 0, parseTime(requested), parseTime(created)
			if target := byUser[item.UserID]; target != nil {
				target.RecentSubscriptions = append(target.RecentSubscriptions, item)
			}
		}
		if err := recent.Close(); err != nil {
			return data, err
		}
	}
	if types[model.AuditReviewEvidenceConnection] || types[model.AuditReviewEvidenceDestination] {
		where := `ended_at>=? and ended_at<=? and ` + userClause + ` and ` + serverClause
		rows, err := s.db.QueryContext(ctx, `select user_id,coalesce(sum(connection_count),0),coalesce(sum(closed_count),0),coalesce(max(active_peak),0),coalesce(max(active_at_end),0),count(distinct source_ip),count(distinct server_id),count(distinct case when destination<>'' then destination||':'||destination_port end),coalesce(sum(dropped_bucket_count),0),max(ended_at) from connection_audit_reports where `+where+` group by user_id`, startText, endText)
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var userID int64
			var last sql.NullString
			var count, closed, peak, active, dropped int64
			var ips, servers, destinations int
			if err := rows.Scan(&userID, &count, &closed, &peak, &active, &ips, &servers, &destinations, &dropped, &last); err != nil {
				rows.Close()
				return data, err
			}
			if item := byUser[userID]; item != nil {
				item.ConnectionCount, item.ConnectionClosed, item.ConnectionActivePeak, item.ConnectionActiveAtEnd = count, closed, peak, active
				item.ConnectionSourceIPs, item.ConnectionServers, item.ConnectionDestinations, item.ConnectionDropped = ips, servers, destinations, dropped
				item.ConnectionLastSeenAt = nullableTime(last)
			}
		}
		if err := rows.Close(); err != nil {
			return data, err
		}
		breakdown, err := s.db.QueryContext(ctx, `select user_id,server_id,sum(connection_count),max(active_peak),max(ended_at) from connection_audit_reports where `+where+` group by user_id,server_id order by user_id,sum(connection_count) desc`, startText, endText)
		if err != nil {
			return data, err
		}
		for breakdown.Next() {
			var userID int64
			var item model.AuditReviewServerBreakdown
			var last string
			if err := breakdown.Scan(&userID, &item.ServerID, &item.ConnectionCount, &item.ActivePeak, &last); err != nil {
				breakdown.Close()
				return data, err
			}
			item.LastSeenAt = parseTime(last)
			if target := byUser[userID]; target != nil {
				target.ServerBreakdown = append(target.ServerBreakdown, item)
			}
		}
		if err := breakdown.Close(); err != nil {
			return data, err
		}
		if types[model.AuditReviewEvidenceConnection] {
			recent, err := s.db.QueryContext(ctx, `select report_id,server_id,user_id,inbound_id,path_id,source_ip,source_geo_code,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,network,destination,destination_port,outbound_tag,outbound_type,connection_count,closed_count,duration_total_ms,duration_max_ms,active_peak,active_at_end,collection_generation,bucket_capacity,dropped_bucket_count,collection_started_at,collection_ended_at,started_at,ended_at,created_at from (select a.*,row_number() over(partition by user_id order by ended_at desc,connection_count desc) rn from connection_audit_reports a where `+where+`) where rn<=10 order by user_id,ended_at desc`, startText, endText)
			if err != nil {
				return data, err
			}
			for recent.Next() {
				item, err := scanAuditReviewConnection(recent)
				if err != nil {
					recent.Close()
					return data, err
				}
				if target := byUser[item.UserID]; target != nil {
					target.RecentConnections = append(target.RecentConnections, item)
				}
			}
			if err := recent.Close(); err != nil {
				return data, err
			}
		}
		if types[model.AuditReviewEvidenceDestination] {
			destinations, err := s.db.QueryContext(ctx, `select user_id,destination,destination_port,network,total,server_count,last_seen from (select user_id,destination,destination_port,network,sum(connection_count) total,count(distinct server_id) server_count,max(ended_at) last_seen,row_number() over(partition by user_id order by sum(connection_count) desc,max(ended_at) desc) rn from connection_audit_reports where `+where+` and destination<>'' group by user_id,destination,destination_port,network) where rn<=20 order by user_id,total desc`, startText, endText)
			if err != nil {
				return data, err
			}
			for destinations.Next() {
				var userID int64
				var item model.AuditReviewDestination
				var last string
				if err := destinations.Scan(&userID, &item.Destination, &item.Port, &item.Network, &item.ConnectionCount, &item.ServerCount, &last); err != nil {
					destinations.Close()
					return data, err
				}
				item.LastSeenAt = parseTime(last)
				if target := byUser[userID]; target != nil {
					target.Destinations = append(target.Destinations, item)
				}
			}
			if err := destinations.Close(); err != nil {
				return data, err
			}
		}
	}
	data.Users = data.Users[:0]
	for _, id := range userIDs {
		if item := byUser[id]; item != nil {
			data.Users = append(data.Users, *item)
		}
	}
	return data, nil
}

func scanAuditReviewConnection(scanner interface{ Scan(...any) error }) (model.ConnectionAuditReport, error) {
	var item model.ConnectionAuditReport
	var inboundID, pathID sql.NullInt64
	var collectionStarted, collectionEnded, started, ended, created string
	err := scanner.Scan(&item.ReportID, &item.ServerID, &item.UserID, &inboundID, &pathID, &item.SourceIP, &item.SourceGeoCode, &item.SourceCountryCode, &item.SourceCountry, &item.SourceProvince, &item.SourceCity, &item.SourceISP, &item.GeoDatabaseRevision, &item.Network, &item.Destination, &item.DestinationPort, &item.OutboundTag, &item.OutboundType, &item.ConnectionCount, &item.ClosedCount, &item.DurationTotalMS, &item.DurationMaxMS, &item.ActivePeak, &item.ActiveAtEnd, &item.CollectionGeneration, &item.BucketCapacity, &item.DroppedBucketCount, &collectionStarted, &collectionEnded, &started, &ended, &created)
	if err != nil {
		return item, err
	}
	if inboundID.Valid {
		item.InboundID = &inboundID.Int64
	}
	if pathID.Valid {
		item.PathID = &pathID.Int64
	}
	item.CollectionStartedAt, item.CollectionEndedAt = parseTime(collectionStarted), parseTime(collectionEnded)
	item.StartedAt, item.EndedAt, item.CreatedAt = parseTime(started), parseTime(ended), parseTime(created)
	return item, nil
}

func auditReviewIDClause(column string, ids []int64) string {
	if len(ids) == 0 {
		return "0"
	}
	values := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			values = append(values, strconv.FormatInt(id, 10))
		}
	}
	if len(values) == 0 {
		return "0"
	}
	return column + " in (" + strings.Join(values, ",") + ")"
}

func SortAuditReviewIDs(values []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value > 0 && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func AuditReviewJobID(reviewID string, stage, position int) string {
	return fmt.Sprintf("%s-%d-%d", reviewID, stage, position)
}
