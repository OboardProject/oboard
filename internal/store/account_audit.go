package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

// InitAccountAuditSchema only adds current-model tables. Legacy evidence is not imported.
func (s *Store) InitAccountAuditSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS account_audit_snapshots (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, as_of TEXT NOT NULL, snapshot TEXT NOT NULL CHECK(length(snapshot)<=65536));
 CREATE TABLE IF NOT EXISTS account_audit_event_state (
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, risk_type TEXT NOT NULL,
 last_minute INTEGER NOT NULL, high_count INTEGER NOT NULL, low_count INTEGER NOT NULL,
 cycle INTEGER NOT NULL, active INTEGER NOT NULL, version TEXT NOT NULL,
 PRIMARY KEY(user_id,risk_type));
 CREATE TABLE IF NOT EXISTS account_audit_events (
 id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 risk_type TEXT NOT NULL, cycle INTEGER NOT NULL, status TEXT NOT NULL,
 score INTEGER NOT NULL, first_seen_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
 snapshot TEXT NOT NULL CHECK(length(snapshot)<=65536), UNIQUE(user_id,risk_type,cycle));
 CREATE INDEX IF NOT EXISTS account_audit_events_page ON account_audit_events(last_seen_at DESC,id DESC);
 CREATE TABLE IF NOT EXISTS account_audit_dirty (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, revision INTEGER NOT NULL);
 `)
	if err != nil {
		return err
	}
	return initAccountAuditWorkflow(ctx, s.db)
}

type AccountAuditQuery struct {
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
	UserID  int64  `json:"user_id,omitempty"`
	EventID int64  `json:"event_id,omitempty"`
	Status  string `json:"status,omitempty"`
	// Nil is unrestricted; an empty non-nil slice authorizes no accounts.
	AllowedUserIDs []int64 `json:"-"`
}

func (q *AccountAuditQuery) Validate() error {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 100000 || q.UserID < 0 || q.EventID < 0 {
		return errors.New("invalid audit pagination")
	}
	if q.Status != "" && !validAuditStatus(q.Status) && q.Status != "recovered" {
		return errors.New("invalid audit event status")
	}
	return nil
}

type AccountAuditPage struct {
	Items      any  `json:"items"`
	Limit      int  `json:"limit"`
	Offset     int  `json:"offset"`
	NextOffset *int `json:"next_offset"`
}
type AccountAuditRow struct {
	UserID           int64           `json:"user_id"`
	Username         string          `json:"username"`
	EvaluationStatus string          `json:"evaluation_status"`
	Snapshot         json.RawMessage `json:"snapshot"`
}
type AccountAuditEvent struct {
	ID               int64           `json:"id"`
	UserID           int64           `json:"user_id"`
	RiskType         string          `json:"risk_type"`
	Cycle            int64           `json:"cycle"`
	Status           string          `json:"status"`
	Score            int             `json:"score"`
	FirstSeenAt      string          `json:"first_seen_at"`
	LastSeenAt       string          `json:"last_seen_at"`
	Snapshot         json.RawMessage `json:"snapshot"`
	EvaluationStatus string          `json:"evaluation_status"`
	Revision         int64           `json:"revision"`
	ReviewStatus     string          `json:"review_status"`
}

func accountAuditWhere(q AccountAuditQuery, column string) (string, []any) {
	where := "1=1"
	args := []any{}
	if q.UserID > 0 {
		where += " AND " + column + "=?"
		args = append(args, q.UserID)
	}
	if q.AllowedUserIDs != nil {
		if len(q.AllowedUserIDs) == 0 {
			return "0=1", nil
		}
		slots := make([]string, len(q.AllowedUserIDs))
		for i, id := range q.AllowedUserIDs {
			slots[i] = "?"
			args = append(args, id)
		}
		where += " AND " + column + " IN (" + strings.Join(slots, ",") + ")"
	}
	return where, args
}
func auditPage(q AccountAuditQuery, items any, more bool) AccountAuditPage {
	p := AccountAuditPage{Items: items, Limit: q.Limit, Offset: q.Offset}
	if more {
		n := q.Offset + q.Limit
		p.NextOffset = &n
	}
	return p
}
func (s *Store) ListAccountAuditSnapshots(ctx context.Context, q AccountAuditQuery) (AccountAuditPage, error) {
	if err := q.Validate(); err != nil {
		return AccountAuditPage{}, err
	}
	where, args := accountAuditWhere(q, "u.id")
	args = append(args, q.Limit+1, q.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT u.id,u.username,a.snapshot FROM users u LEFT JOIN account_audit_snapshots a ON a.user_id=u.id WHERE `+where+` ORDER BY u.id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return AccountAuditPage{}, err
	}
	defer rows.Close()
	items := []AccountAuditRow{}
	for rows.Next() {
		var row AccountAuditRow
		var raw sql.NullString
		if err := rows.Scan(&row.UserID, &row.Username, &raw); err != nil {
			return AccountAuditPage{}, err
		}
		row.EvaluationStatus = "pending"
		if raw.Valid {
			var snapshot struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(raw.String), &snapshot); err != nil {
				return AccountAuditPage{}, err
			}
			row.EvaluationStatus = snapshot.Status
			row.Snapshot = json.RawMessage(raw.String)
		}
		items = append(items, row)
	}
	more := len(items) > q.Limit
	if more {
		items = items[:q.Limit]
	}
	return auditPage(q, items, more), rows.Err()
}
func (s *Store) ListAccountAuditEvents(ctx context.Context, q AccountAuditQuery) (AccountAuditPage, error) {
	if err := q.Validate(); err != nil {
		return AccountAuditPage{}, err
	}
	where, args := accountAuditWhere(q, "user_id")
	if q.Status != "" {
		where += " AND CASE WHEN status='recovered' THEN status ELSE COALESCE((SELECT w.status FROM account_audit_workflow w WHERE w.event_id=account_audit_events.id),'pending') END=?"
		args = append(args, q.Status)
	}
	snapshotColumn := "NULL"
	if q.EventID > 0 {
		where += " AND id=?"
		args = append(args, q.EventID)
		snapshotColumn = "snapshot"
	}
	args = append(args, q.Limit+1, q.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,risk_type,cycle,status,score,first_seen_at,last_seen_at,COALESCE(json_extract(snapshot,'$.status'),'pending'),`+snapshotColumn+`,COALESCE((SELECT revision FROM account_audit_workflow w WHERE w.event_id=account_audit_events.id),1),COALESCE((SELECT status FROM account_audit_workflow w WHERE w.event_id=account_audit_events.id),'pending') FROM account_audit_events WHERE `+where+` ORDER BY last_seen_at DESC,id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return AccountAuditPage{}, err
	}
	defer rows.Close()
	items := []AccountAuditEvent{}
	for rows.Next() {
		var row AccountAuditEvent
		var raw sql.NullString
		if err := rows.Scan(&row.ID, &row.UserID, &row.RiskType, &row.Cycle, &row.Status, &row.Score, &row.FirstSeenAt, &row.LastSeenAt, &row.EvaluationStatus, &raw, &row.Revision, &row.ReviewStatus); err != nil {
			return AccountAuditPage{}, err
		}
		if raw.Valid {
			row.Snapshot = json.RawMessage(raw.String)
		}
		if row.Status != "recovered" {
			row.Status = row.ReviewStatus
		}
		items = append(items, row)
	}
	more := len(items) > q.Limit
	if more {
		items = items[:q.Limit]
	}
	return auditPage(q, items, more), rows.Err()
}

// MarkAccountAuditDirty coalesces work durably; a concurrent newer revision is
// not cleared by a worker committing an older evaluation.
func (s *Store) MarkAccountAuditDirty(ctx context.Context, userID int64) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO account_audit_dirty(user_id,revision) VALUES (?,COALESCE((SELECT revision FROM account_audit_snapshots WHERE user_id=?),0)+1) ON CONFLICT(user_id) DO UPDATE SET revision=revision+1 RETURNING revision`, userID, userID).Scan(&revision)
	return revision, err
}

// SaveAccountAuditSnapshot atomically persists the result and event transitions.
// Revisions are monotonic and completed UTC minutes, not reads, drive debounce.
func (s *Store) SaveAccountAuditSnapshot(ctx context.Context, snapshot auditrisk.Snapshot, revision int64) error {
	if snapshot.AccountID <= 0 || revision <= 0 || snapshot.AsOf.IsZero() || snapshot.WindowEnd.IsZero() || snapshot.AutomaticActionEligible {
		return errors.New("invalid account audit snapshot")
	}
	if snapshot.WindowEnd.After(snapshot.AsOf) || !snapshot.WindowEnd.Equal(snapshot.WindowEnd.Truncate(time.Minute)) || !snapshot.WindowStart.Equal(snapshot.WindowEnd.Add(-30*time.Minute)) {
		return errors.New("invalid account audit window")
	}
	for _, score := range []*auditrisk.Score{snapshot.Activity, snapshot.Exposure, snapshot.Resource, snapshot.Attention} {
		if score != nil && (score.Lower < 0 || score.Upper > 100 || score.Upper < score.Lower) {
			return errors.New("invalid account audit score")
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return errors.New("audit snapshot capacity exceeded")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldRevision int64
	var oldTime string
	err = tx.QueryRowContext(ctx, `SELECT revision,as_of FROM account_audit_snapshots WHERE user_id=?`, snapshot.AccountID).Scan(&oldRevision, &oldTime)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_audit_snapshots`).Scan(&count); err != nil {
			return err
		}
		if count >= 4096 {
			return errors.New("audit snapshot global capacity exceeded")
		}
	}
	stamp := snapshot.AsOf.UTC().Format("2006-01-02T15:04:05.000000000Z")
	if err == nil {
		previous, parseErr := time.Parse(time.RFC3339Nano, oldTime)
		if parseErr != nil {
			return parseErr
		}
		if revision <= oldRevision || snapshot.AsOf.Before(previous) {
			return errors.New("stale account audit snapshot")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_audit_snapshots(user_id,revision,as_of,snapshot) VALUES(?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET revision=excluded.revision,as_of=excluded.as_of,snapshot=excluded.snapshot`, snapshot.AccountID, revision, stamp, string(raw))
	if err != nil {
		return err
	}
	version := snapshot.Policy.Version + "/" + snapshot.Versions.Model + "/" + snapshot.Versions.Baseline + "/" + snapshot.Versions.Source
	minute := snapshot.WindowEnd.Unix() / 60
	trusted := true
	for _, dimension := range []auditrisk.Dimension{snapshot.Quality.IdentityTrusted, snapshot.Quality.SourceUsable, snapshot.Quality.Deduplicated, snapshot.Quality.MeasurementValid, snapshot.Quality.TimeAligned, snapshot.Quality.Freshness, snapshot.Quality.CapabilitySupported} {
		trusted = trusted && dimension.State == auditrisk.Satisfied
	}
	complete := trusted && snapshot.Quality.CoverageComplete.State == auditrisk.Satisfied && snapshot.Quality.SourceSetComplete.State == auditrisk.Satisfied
	for _, risk := range []struct {
		name  string
		score *auditrisk.Score
	}{{"activity", snapshot.Activity}, {"exposure", snapshot.Exposure}} {
		var last, cycle int64
		var high, low, active int
		var oldVersion string
		err = tx.QueryRowContext(ctx, `SELECT last_minute,high_count,low_count,cycle,active,version FROM account_audit_event_state WHERE user_id=? AND risk_type=?`, snapshot.AccountID, risk.name).Scan(&last, &high, &low, &cycle, &active, &oldVersion)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if minute <= last {
			continue
		}
		if minute != last+1 || version != oldVersion {
			high = 0
			low = 0
		}
		highNow := trusted && risk.score != nil && risk.score.Lower >= 70
		lowNow := complete && risk.score != nil && risk.score.Upper < 40
		if risk.name == "exposure" && snapshot.Quality.HistoryComplete.State != auditrisk.Satisfied {
			lowNow = false
		}
		if highNow {
			high = min(high+1, 2)
		} else {
			high = 0
		}
		if lowNow {
			low = min(low+1, 10)
		} else {
			low = 0
		}
		occurrence := ""
		if highNow && !snapshot.Features.EvidenceCutoff.IsZero() && !snapshot.Features.EvidenceCutoff.After(snapshot.AsOf) {
			occurrence = snapshot.Features.EvidenceCutoff.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
		if active == 0 && high >= 2 {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_audit_events`).Scan(&count); err != nil {
				return err
			}
			if count >= 4096 {
				return errors.New("audit event global capacity exceeded")
			}
			cycle++
			active = 1
			firstOccurrence := occurrence
			if firstOccurrence == "" {
				firstOccurrence = stamp
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO account_audit_events(user_id,risk_type,cycle,status,score,first_seen_at,last_seen_at,snapshot) VALUES(?,?,?,'pending',?,?,?,?)`, snapshot.AccountID, risk.name, cycle, risk.score.Lower, firstOccurrence, firstOccurrence, string(raw))
			if err != nil {
				return err
			}
		}
		if active == 1 {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO account_audit_workflow(event_id) SELECT id FROM account_audit_events WHERE user_id=? AND risk_type=? AND cycle=?`, snapshot.AccountID, risk.name, cycle); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE account_audit_workflow SET revision=revision+1 WHERE event_id IN (SELECT id FROM account_audit_events WHERE user_id=? AND risk_type=? AND cycle=?)`, snapshot.AccountID, risk.name, cycle); err != nil {
				return err
			}
			status := "pending"
			if low >= 10 {
				status = "recovered"
				active = 0
				high = 0
				low = 0
			}
			var value any
			if risk.score != nil {
				value = risk.score.Lower
			}
			_, err = tx.ExecContext(ctx, `UPDATE account_audit_events SET status=?,score=COALESCE(?,score),last_seen_at=CASE WHEN ? >= last_seen_at THEN ? ELSE last_seen_at END,snapshot=? WHERE user_id=? AND risk_type=? AND cycle=?`, status, value, occurrence, occurrence, string(raw), snapshot.AccountID, risk.name, cycle)
			if err != nil {
				return err
			}
		}
		if active == 1 && highNow {
			var eventID int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM account_audit_events WHERE user_id=? AND risk_type=? AND cycle=?`, snapshot.AccountID, risk.name, cycle).Scan(&eventID); err != nil {
				return err
			}
			if err := queueAccountAuditNotification(ctx, tx, eventID, risk.score.Lower, raw, snapshot.AsOf); err != nil {
				return err
			}
		}
		if active == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE account_audit_notifications SET status='cancelled',lease_token=lease_token+1 WHERE event_id IN (SELECT id FROM account_audit_events WHERE user_id=? AND risk_type=? AND cycle=?) AND status IN ('pending','leased')`, snapshot.AccountID, risk.name, cycle); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_audit_event_state(user_id,risk_type,last_minute,high_count,low_count,cycle,active,version) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(user_id,risk_type) DO UPDATE SET last_minute=excluded.last_minute,high_count=excluded.high_count,low_count=excluded.low_count,cycle=excluded.cycle,active=excluded.active,version=excluded.version`, snapshot.AccountID, risk.name, minute, high, low, cycle, active, version)
		if err != nil {
			return fmt.Errorf("save audit event state: %w", err)
		}
	}
	// Keep at most 64 recent cycles per account; open events are never evicted.
	_, err = tx.ExecContext(ctx, `DELETE FROM account_audit_events WHERE user_id=? AND status='recovered' AND id NOT IN (SELECT id FROM account_audit_events WHERE user_id=? ORDER BY id DESC LIMIT 64)`, snapshot.AccountID, snapshot.AccountID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM account_audit_dirty WHERE user_id=? AND revision<=?`, snapshot.AccountID, revision)
	if err != nil {
		return err
	}
	return tx.Commit()
}
