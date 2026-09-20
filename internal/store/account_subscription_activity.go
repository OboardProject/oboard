package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/auditactivity"
)

const AccountSubscriptionStateLimit = 4096
const accountSubscriptionStateBytes = 32768

// AccountSubscriptionFeatures contains no address, credential or URL. Collection
// continuity is independent of the connection baseline and requires a heartbeat.
type AccountSubscriptionFeatures struct {
	State           auditactivity.SubscriptionState `json:"state"`
	CandidateLower  int                             `json:"candidate_lower"`
	CandidateUpper  int                             `json:"candidate_upper"`
	HistoryComplete bool                            `json:"history_complete"`
}

func (s *Store) EnsureAccountSubscriptionActivitySchema(ctx context.Context) error {
	s.subscriptionActivitySchemaOnce.Do(func() {
		_, s.subscriptionActivitySchemaErr = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS account_subscription_activity (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 state_json TEXT NOT NULL CHECK(length(state_json)<=32768), updated_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS account_subscription_activity_expiry ON account_subscription_activity(updated_at);
 CREATE TABLE IF NOT EXISTS account_subscription_coverage (
 id INTEGER PRIMARY KEY CHECK(id=1), started_at INTEGER NOT NULL, last_at INTEGER NOT NULL, enabled INTEGER NOT NULL);
 INSERT OR IGNORE INTO account_subscription_coverage VALUES(1,0,0,0);
 CREATE TABLE IF NOT EXISTS account_activity_v1_dirty(account INTEGER PRIMARY KEY,revision INTEGER NOT NULL,evaluated INTEGER NOT NULL DEFAULT 0);`)
	})
	return s.subscriptionActivitySchemaErr
}

// Call once per minute from the shared runtime scheduler, also on gate changes.
// Missing heartbeats (including process downtime) restart the seven-day horizon.
func (s *Store) SetAccountSubscriptionCoverage(ctx context.Context, enabled bool, at time.Time) error {
	if err := s.EnsureAccountSubscriptionActivitySchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE account_subscription_coverage SET started_at=CASE WHEN enabled=0 OR ?=0 OR last_at<? OR last_at>? THEN ? ELSE started_at END,last_at=?,enabled=? WHERE id=1`, enabled, at.Add(-2*time.Minute).Unix(), at.Unix(), at.Unix(), at.Unix(), enabled)
	return err
}
func (s *Store) RecordAccountSubscriptionActivity(ctx context.Context, userID int64, o auditactivity.SubscriptionObservation) error {
	if userID <= 0 || !o.Success || !o.IdentityTrusted {
		return errors.New("invalid successful subscription activity")
	}
	if err := s.EnsureAccountSubscriptionActivitySchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state auditactivity.SubscriptionState
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT state_json FROM account_subscription_activity WHERE user_id=?`, userID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	exists := err == nil
	if exists {
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			return err
		}
	}
	var start, last int64
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT started_at,last_at,enabled FROM account_subscription_coverage WHERE id=1`).Scan(&start, &last, &enabled); err != nil {
		return err
	}
	o.HistoryComplete = enabled && last <= o.At.Unix() && last >= o.At.Add(-2*time.Minute).Unix()
	if !exists {
		// A bounded indexed batch reclaims only expired account aggregates, never
		// historical evidence. Capacity rejection makes the coverage horizon unknown.
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_subscription_activity WHERE user_id IN (SELECT user_id FROM account_subscription_activity WHERE updated_at<? ORDER BY updated_at LIMIT 64)`, o.At.Add(-8*24*time.Hour).Unix()); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM account_subscription_activity`).Scan(&count); err != nil {
			return err
		}
		if count >= AccountSubscriptionStateLimit {
			if _, err = tx.ExecContext(ctx, `UPDATE account_subscription_coverage SET started_at=? WHERE id=1`, o.At.Unix()); err != nil {
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			return ErrAccountActivityCapacity
		}
	}
	// Event time is assigned by Controller at successful delivery. Concurrent
	// completions can commit in reverse order; never silently shift that event.
	if !exists && o.HistoryComplete {
		state.TokenVersion = o.TokenVersion
		state.SourceVersion = o.SourceVersion
		state.HistoryStarted = time.Unix(start, 0).UTC()
	}
	if exists && time.Unix(start, 0).After(state.HistoryStarted) {
		state.HistoryStarted = time.Unix(start, 0).UTC()
	}
	if _, err = state.Observe(o, auditactivity.DefaultSubscriptionPolicy()); err != nil {
		return err
	}
	rawBytes, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(rawBytes) > accountSubscriptionStateBytes {
		return ErrAccountActivityCapacity
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_subscription_activity(user_id,state_json,updated_at) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET state_json=excluded.state_json,updated_at=excluded.updated_at`, userID, string(rawBytes), o.At.Unix())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_dirty(account,revision,evaluated) VALUES(?,1,0) ON CONFLICT(account) DO UPDATE SET revision=revision+1`, userID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LoadAccountSubscriptionFeatures(ctx context.Context, userID int64, asOf time.Time) (AccountSubscriptionFeatures, error) {
	var out AccountSubscriptionFeatures
	if err := s.EnsureAccountSubscriptionActivitySchema(ctx); err != nil {
		return out, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT state_json FROM account_subscription_activity WHERE user_id=?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		out.CandidateUpper = auditactivity.MaxSources
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(raw), &out.State); err != nil {
		return out, err
	}
	out.CandidateLower, out.CandidateUpper, err = out.State.CandidateBounds(asOf, auditactivity.DefaultSubscriptionPolicy())
	if err != nil {
		return out, err
	}
	var start, last int64
	var enabled bool
	if err = s.db.QueryRowContext(ctx, `SELECT started_at,last_at,enabled FROM account_subscription_coverage WHERE id=1`).Scan(&start, &last, &enabled); err != nil {
		return out, err
	}
	out.HistoryComplete = enabled && last >= asOf.Add(-2*time.Minute).Unix() && last <= asOf.Unix() && start <= asOf.Add(-7*24*time.Hour).Unix() && !asOf.Before(out.State.HistoryUnknownUntil)
	if !out.HistoryComplete {
		out.CandidateUpper = auditactivity.MaxSources
	}
	return out, nil
}
