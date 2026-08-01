package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) CreateAuditFeatureSnapshot(ctx context.Context, item *model.AuditFeatureSnapshot) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into audit_feature_snapshots(id,user_id,window,window_started_at,window_ended_at,feature_version,rule_score,anomaly_score,features_json,fingerprint,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.UserID, item.Window, item.WindowStartedAt.UTC().Format(time.RFC3339Nano), item.WindowEndedAt.UTC().Format(time.RFC3339Nano), item.FeatureVersion, item.RuleScore, item.AnomalyScore, normalizedJSONObject(item.Features), item.Fingerprint, ts)
	if err == nil {
		item.CreatedAt = parseTime(ts)
	}
	return err
}

func (s *Store) ListAuditFeatureSnapshots(ctx context.Context, userID int64, window string, limit int) ([]model.AuditFeatureSnapshot, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `select id,user_id,window,window_started_at,window_ended_at,feature_version,rule_score,anomaly_score,features_json,fingerprint,created_at from audit_feature_snapshots where user_id=? and window=? order by created_at desc limit ?`, userID, window, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditFeatureSnapshot{}
	for rows.Next() {
		var item model.AuditFeatureSnapshot
		var started, ended, features, created string
		var anomaly sql.NullInt64
		if err := rows.Scan(&item.ID, &item.UserID, &item.Window, &started, &ended, &item.FeatureVersion, &item.RuleScore, &anomaly, &features, &item.Fingerprint, &created); err != nil {
			return nil, err
		}
		if anomaly.Valid {
			value := int(anomaly.Int64)
			item.AnomalyScore = &value
		}
		item.WindowStartedAt, item.WindowEndedAt, item.CreatedAt = parseTime(started), parseTime(ended), parseTime(created)
		item.Features = json.RawMessage(features)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpsertAuditIncident(ctx context.Context, item *model.AuditIncident) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into audit_incidents(id,user_id,status,classification,severity,rule_score,anomaly_score,fingerprint,latest_snapshot_id,opened_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?) on conflict(fingerprint) do update set status='open',severity=excluded.severity,rule_score=excluded.rule_score,anomaly_score=excluded.anomaly_score,latest_snapshot_id=excluded.latest_snapshot_id,updated_at=excluded.updated_at,resolved_at=null`, item.ID, item.UserID, item.Status, item.Classification, item.Severity, item.RuleScore, item.AnomalyScore, item.Fingerprint, item.LatestSnapshotID, ts, ts)
	if err != nil {
		return err
	}
	stored, err := s.GetAuditIncidentByFingerprint(ctx, item.Fingerprint)
	if err == nil {
		*item = *stored
	}
	return err
}

func (s *Store) GetAuditIncident(ctx context.Context, id string) (*model.AuditIncident, error) {
	return scanAuditIncident(s.db.QueryRowContext(ctx, `select id,user_id,status,classification,severity,rule_score,anomaly_score,fingerprint,coalesce(latest_snapshot_id,''),opened_at,updated_at,resolved_at from audit_incidents where id=?`, id))
}

func (s *Store) GetAuditIncidentByFingerprint(ctx context.Context, fingerprint string) (*model.AuditIncident, error) {
	return scanAuditIncident(s.db.QueryRowContext(ctx, `select id,user_id,status,classification,severity,rule_score,anomaly_score,fingerprint,coalesce(latest_snapshot_id,''),opened_at,updated_at,resolved_at from audit_incidents where fingerprint=?`, fingerprint))
}

func (s *Store) ListAuditIncidents(ctx context.Context, limit int) ([]model.AuditIncident, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `select id,user_id,status,classification,severity,rule_score,anomaly_score,fingerprint,coalesce(latest_snapshot_id,''),opened_at,updated_at,resolved_at from audit_incidents order by updated_at desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditIncident{}
	for rows.Next() {
		item, err := scanAuditIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func scanAuditIncident(scanner interface{ Scan(...any) error }) (*model.AuditIncident, error) {
	var item model.AuditIncident
	var anomaly sql.NullInt64
	var opened, updated string
	var resolved sql.NullString
	if err := scanner.Scan(&item.ID, &item.UserID, &item.Status, &item.Classification, &item.Severity, &item.RuleScore, &anomaly, &item.Fingerprint, &item.LatestSnapshotID, &opened, &updated, &resolved); err != nil {
		return nil, err
	}
	if anomaly.Valid {
		value := int(anomaly.Int64)
		item.AnomalyScore = &value
	}
	item.OpenedAt, item.UpdatedAt, item.ResolvedAt = parseTime(opened), parseTime(updated), nullableTime(resolved)
	return &item, nil
}

func (s *Store) CreateAIAnalysisJobIfAbsent(ctx context.Context, item *model.AIAnalysisJob, cooldown time.Duration) (bool, error) {
	since := time.Now().UTC().Add(-cooldown).Format(time.RFC3339Nano)
	var id string
	if err := s.db.QueryRowContext(ctx, `select id from ai_analysis_jobs where fingerprint=? and created_at>=? order by created_at desc limit 1`, item.Fingerprint, since).Scan(&id); err == nil {
		return false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into ai_analysis_jobs(id,kind,incident_id,provider_id,fingerprint,status,input_json,created_at,updated_at) values(?,?,?,?,?,'pending',?,?,?)`, item.ID, item.Kind, nullEmpty(item.IncidentID), item.ProviderID, item.Fingerprint, normalizedJSONObject(item.Input), ts, ts)
	if err == nil {
		item.Status, item.CreatedAt, item.UpdatedAt = "pending", parseTime(ts), parseTime(ts)
	}
	return err == nil, err
}

func (s *Store) CreateOperatorFeedback(ctx context.Context, item *model.OperatorFeedback) error {
	if item == nil {
		return errors.New("feedback is required")
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into operator_feedback(id,incident_id,actor_user_id,label,comment,created_at) values(?,?,?,?,?,?)`, item.ID, item.IncidentID, item.ActorUserID, item.Label, item.Comment, ts)
	if err == nil {
		item.CreatedAt = parseTime(ts)
	}
	return err
}

func (s *Store) FirstEnabledAIProviderID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `select id from ai_providers where enabled=1 order by created_at limit 1`).Scan(&id)
	return id, err
}
