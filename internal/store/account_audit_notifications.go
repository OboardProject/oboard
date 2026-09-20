package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func initAccountAuditWorkflow(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS account_audit_workflow (
 event_id INTEGER PRIMARY KEY REFERENCES account_audit_events(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'pending', muted_until INTEGER NOT NULL DEFAULT 0,
 notified_severity INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS account_audit_notifications (
 id INTEGER PRIMARY KEY, event_id INTEGER NOT NULL REFERENCES account_audit_events(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, snapshot TEXT NOT NULL CHECK(length(snapshot)<=65536),
 status TEXT NOT NULL DEFAULT 'pending', lease_token INTEGER NOT NULL DEFAULT 0, lease_until INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, UNIQUE(event_id,revision));
 CREATE INDEX IF NOT EXISTS account_audit_notification_queue ON account_audit_notifications(status,lease_until,id);
 CREATE TABLE IF NOT EXISTS account_audit_actions (
 id INTEGER PRIMARY KEY, event_id INTEGER NOT NULL REFERENCES account_audit_events(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, actor TEXT NOT NULL, reason TEXT NOT NULL, status TEXT NOT NULL,
 expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL, execution_status TEXT NOT NULL DEFAULT 'applied', UNIQUE(event_id,revision));`)
	return err
}

type AccountAuditNotification struct {
	ID         int64           `json:"id"`
	EventID    int64           `json:"event_id"`
	UserID     int64           `json:"user_id"`
	RiskType   string          `json:"risk_type"`
	Revision   int64           `json:"revision"`
	LeaseToken int64           `json:"lease_token"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

// ClaimAccountAuditNotifications leases durable messages. Delivery is at least once;
// downstream senders should use ID as their idempotency key.
func (s *Store) ClaimAccountAuditNotifications(ctx context.Context, now time.Time, limit int) ([]AccountAuditNotification, error) {
	if now.IsZero() || limit < 1 || limit > 100 {
		return nil, errors.New("invalid notification claim")
	}
	rows, err := s.db.QueryContext(ctx, `UPDATE account_audit_notifications SET status='leased',lease_token=lease_token+1,lease_until=? WHERE id IN (
 SELECT n.id FROM account_audit_notifications n JOIN account_audit_workflow w ON w.event_id=n.event_id
 JOIN account_audit_events e ON e.id=n.event_id
 WHERE (n.status='pending' OR (n.status='leased' AND n.lease_until<=?)) AND w.muted_until<=? AND w.status NOT IN ('closed','false_positive','handled') AND e.status!='recovered'
 ORDER BY n.id LIMIT ?) RETURNING id,event_id,revision,lease_token,snapshot,
 (SELECT user_id FROM account_audit_events WHERE id=event_id),(SELECT risk_type FROM account_audit_events WHERE id=event_id)`, now.Add(2*time.Minute).Unix(), now.Unix(), now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AccountAuditNotification{}
	for rows.Next() {
		var n AccountAuditNotification
		var raw string
		if err := rows.Scan(&n.ID, &n.EventID, &n.Revision, &n.LeaseToken, &raw, &n.UserID, &n.RiskType); err != nil {
			return nil, err
		}
		n.Snapshot = json.RawMessage(raw)
		result = append(result, n)
	}
	return result, rows.Err()
}
func (s *Store) ListAccountAuditNotifications(ctx context.Context, now time.Time, limit int) ([]AccountAuditNotification, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid notification limit")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.id,n.event_id,e.user_id,e.risk_type,n.revision,n.lease_token,n.snapshot FROM account_audit_notifications n JOIN account_audit_events e ON e.id=n.event_id WHERE n.status IN ('pending','leased') AND n.created_at<=? ORDER BY n.id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountAuditNotification{}
	for rows.Next() {
		var n AccountAuditNotification
		var raw string
		if err := rows.Scan(&n.ID, &n.EventID, &n.UserID, &n.RiskType, &n.Revision, &n.LeaseToken, &raw); err != nil {
			return nil, err
		}
		n.Snapshot = json.RawMessage(raw)
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Store) CompleteAccountAuditNotification(ctx context.Context, id, leaseToken int64, now time.Time, delivered bool) error {
	if id <= 0 || leaseToken <= 0 || now.IsZero() {
		return errors.New("invalid audit notification completion")
	}
	status := "pending"
	if delivered {
		status = "delivered"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE account_audit_notifications SET status=?,lease_until=0 WHERE id=? AND lease_token=? AND status='leased' AND lease_until>?`, status, id, leaseToken, now.Unix())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		var currentStatus string
		var currentToken int64
		if scanErr := s.db.QueryRowContext(ctx, `SELECT status,lease_token FROM account_audit_notifications WHERE id=?`, id).Scan(&currentStatus, &currentToken); scanErr == nil && currentStatus == status && currentToken == leaseToken {
			return nil
		}
		return errors.New("stale audit notification lease")
	}
	return err
}

type AccountAuditAction struct {
	UserID          int64  `json:"user_id"`
	ID              int64  `json:"id"`
	EventID         int64  `json:"event_id"`
	Revision        int64  `json:"revision"`
	Actor           string `json:"actor"`
	Reason          string `json:"reason"`
	Status          string `json:"status"`
	ExpiresAt       int64  `json:"expires_at"`
	CreatedAt       int64  `json:"created_at"`
	ExecutionStatus string `json:"execution_status"`
}

func validAuditStatus(status string) bool {
	switch status {
	case "pending", "observing", "handled", "closed", "false_positive":
		return true
	}
	return false
}

// SetAccountAuditEventStatus records review only, never alters access or quota.
// A review applies to this event cycle, not future cycles or the risk model.
func (s *Store) SetAccountAuditEventStatus(ctx context.Context, eventID, expectedRevision int64, status, actor, reason string, expiry, now time.Time) error {
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if eventID <= 0 || expectedRevision <= 0 || !validAuditStatus(status) || actor == "" || len(actor) > 256 || reason == "" || len(reason) > 2048 || now.IsZero() || (!expiry.IsZero() && (!expiry.After(now) || expiry.After(now.Add(30*24*time.Hour)))) {
		return errors.New("invalid audit review")
	}
	if (status == "observing" || status == "false_positive") && expiry.IsZero() {
		return errors.New("audit review expiry required")
	}
	until := int64(0)
	if !expiry.IsZero() {
		until = expiry.Unix()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var replay int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_audit_actions a JOIN account_audit_workflow w ON w.event_id=a.event_id WHERE a.event_id=? AND a.revision=? AND w.revision=a.revision AND a.actor=? AND a.reason=? AND a.status=? AND a.expires_at=?`, eventID, expectedRevision+1, actor, reason, status, until).Scan(&replay); err != nil {
		return err
	}
	if replay == 1 {
		return tx.Commit()
	}
	var actionCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_audit_actions`).Scan(&actionCount); err != nil {
		return err
	}
	if actionCount >= 16384 {
		return errors.New("audit action global capacity exceeded")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_audit_actions WHERE event_id=?`, eventID).Scan(&actionCount); err != nil {
		return err
	}
	if actionCount >= 256 {
		return errors.New("audit event action capacity exceeded")
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_audit_workflow SET revision=revision+1,status=?,muted_until=? WHERE event_id=? AND revision=?`, status, until, eventID, expectedRevision)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("audit event revision conflict")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_audit_actions(event_id,revision,actor,reason,status,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`, eventID, expectedRevision+1, actor, reason, status, until, now.Unix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_audit_notifications SET status='cancelled',lease_token=lease_token+1 WHERE event_id=? AND status IN ('pending','leased')`, eventID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListAccountAuditActions(ctx context.Context, q AccountAuditQuery) (AccountAuditPage, error) {
	if err := q.Validate(); err != nil {
		return AccountAuditPage{}, err
	}
	where, args := accountAuditWhere(q, "e.user_id")
	if q.EventID > 0 {
		where += " AND a.event_id=?"
		args = append(args, q.EventID)
	}
	args = append(args, q.Limit+1, q.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT e.user_id,a.id,a.event_id,a.revision,a.actor,a.reason,a.status,a.expires_at,a.created_at,a.execution_status FROM account_audit_actions a JOIN account_audit_events e ON e.id=a.event_id WHERE `+where+` ORDER BY a.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return AccountAuditPage{}, err
	}
	defer rows.Close()
	items := []AccountAuditAction{}
	for rows.Next() {
		var a AccountAuditAction
		if err := rows.Scan(&a.UserID, &a.ID, &a.EventID, &a.Revision, &a.Actor, &a.Reason, &a.Status, &a.ExpiresAt, &a.CreatedAt, &a.ExecutionStatus); err != nil {
			return AccountAuditPage{}, err
		}
		items = append(items, a)
	}
	more := len(items) > q.Limit
	if more {
		items = items[:q.Limit]
	}
	return auditPage(q, items, more), rows.Err()
}

func queueAccountAuditNotification(ctx context.Context, tx *sql.Tx, eventID int64, score int, raw []byte, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO account_audit_workflow(event_id) VALUES(?)`, eventID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_audit_workflow SET status='pending',muted_until=0,notified_severity=0,revision=revision+1 WHERE event_id=? AND muted_until>0 AND muted_until<=?`, eventID, now.Unix())
	if err != nil {
		return err
	}
	var revision, muted int64
	var previous int
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT revision,muted_until,notified_severity,status FROM account_audit_workflow WHERE event_id=?`, eventID).Scan(&revision, &muted, &previous, &status); err != nil {
		return err
	}
	severity := 0
	if score >= 70 {
		severity = 1
	}
	if score >= 90 {
		severity = 2
	}
	if severity <= previous || muted > now.Unix() || status == "closed" || status == "handled" || status == "false_positive" {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_audit_notifications`).Scan(&count); err != nil {
		return err
	}
	if count >= 4096 {
		return fmt.Errorf("audit notification capacity exceeded")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_audit_notifications(event_id,revision,snapshot,created_at) VALUES(?,?,?,?)`, eventID, revision+1, string(raw), now.Unix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_audit_workflow SET notified_severity=?,revision=revision+1 WHERE event_id=?`, severity, eventID)
	return err
}

// CleanupAccountAuditHistory removes only inactive, expired evidence in bounded
// batches. Callers supply the configured retention cutoff, never a page read.
func (s *Store) CleanupAccountAuditHistory(ctx context.Context, before time.Time, limit int) error {
	if before.IsZero() || limit < 1 || limit > 500 {
		return errors.New("invalid audit retention")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := before.UTC().Format("2006-01-02T15:04:05.000000000Z")
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_audit_events WHERE id IN (SELECT id FROM account_audit_events WHERE status='recovered' AND last_seen_at<? ORDER BY id LIMIT ?)`, stamp, limit); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_audit_snapshots WHERE user_id IN (SELECT a.user_id FROM account_audit_snapshots a WHERE a.as_of<? AND NOT EXISTS(SELECT 1 FROM account_audit_dirty d WHERE d.user_id=a.user_id) AND NOT EXISTS(SELECT 1 FROM account_audit_event_state e WHERE e.user_id=a.user_id AND e.active=1) ORDER BY a.user_id LIMIT ?)`, stamp, limit); err != nil {
		return err
	}
	return tx.Commit()
}

// CleanupAccountAuditNotifications bounds each maintenance transaction.
func (s *Store) CleanupAccountAuditNotifications(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit < 1 || limit > 500 {
		return 0, errors.New("invalid cleanup limit")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM account_audit_notifications WHERE id IN (SELECT id FROM account_audit_notifications WHERE status IN ('delivered','cancelled') AND created_at<? ORDER BY id LIMIT ?)`, before.Unix(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
