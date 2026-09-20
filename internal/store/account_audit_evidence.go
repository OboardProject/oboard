package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

type AccountAuditEvidenceQuery struct {
	EventID int64 `json:"event_id"`
	Limit   int   `json:"limit,omitempty"`
	Offset  int   `json:"offset,omitempty"`
}

func (q AccountAuditEvidenceQuery) Validate() error {
	if q.EventID <= 0 || q.Limit < 0 || q.Limit > 100 || q.Offset < 0 || q.Offset > 100000 {
		return errors.New("invalid evidence query")
	}
	return nil
}

type AccountAuditEvidenceItem struct {
	AccountID       int64  `json:"account_id"`
	ServerID        int64  `json:"server_id"`
	EventTime       string `json:"event_time"`
	UploadBytes     int64  `json:"upload_bytes"`
	DownloadBytes   int64  `json:"download_bytes"`
	ConnectionCount int64  `json:"connection_count"`
}

type AccountAuditEvidencePage struct {
	Items      []AccountAuditEvidenceItem `json:"items"`
	Limit      int                        `json:"limit"`
	Offset     int                        `json:"offset"`
	NextOffset *int                       `json:"next_offset"`
	Status     string                     `json:"status"`
	Reason     string                     `json:"reason"`
}

// AccountAuditEvidence reads only retained detail, never reconstructing evidence
// from aggregates. Authorization precedes both snapshot and report reads.
func (s *Store) AccountAuditEvidence(ctx context.Context, q AccountAuditEvidenceQuery, authorize func(int64) bool) (AccountAuditEvidencePage, error) {
	out := AccountAuditEvidencePage{Items: []AccountAuditEvidenceItem{}, Limit: q.Limit, Offset: q.Offset, Status: "unknown", Reason: "not_collected_expired_or_capacity_unknown"}
	if err := q.Validate(); err != nil {
		return out, err
	}
	if out.Limit == 0 {
		out.Limit = 20
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var userID int64
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM account_audit_events WHERE id=?`, q.EventID).Scan(&userID); err != nil {
		return out, err
	}
	if authorize == nil || !authorize(userID) {
		return out, errors.New("forbidden audit account")
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT snapshot FROM account_audit_events WHERE id=? AND user_id=?`, q.EventID, userID).Scan(&raw); err != nil {
		return out, err
	}
	var snapshot auditrisk.Snapshot
	if json.Unmarshal([]byte(raw), &snapshot) != nil || snapshot.AccountID != userID || snapshot.AsOf.IsZero() || snapshot.WindowStart.IsZero() || !snapshot.WindowEnd.After(snapshot.WindowStart) {
		out.Reason = "snapshot_window_unavailable"
		return out, nil
	}
	// Require the whole bucket inside the saved window; overlapping buckets cannot
	// safely attribute their bytes to that window. The persisted as-of bounds late arrivals.
	rows, err := tx.QueryContext(ctx, `SELECT user_id,server_id,ended_at,upload_bytes,download_bytes,connection_count FROM connection_audit_reports WHERE user_id=? AND julianday(started_at)>=julianday(?) AND julianday(ended_at)<julianday(?) AND julianday(created_at)<=julianday(?) ORDER BY ended_at DESC,report_id DESC LIMIT ? OFFSET ?`, userID, snapshot.WindowStart.UTC().Format(time.RFC3339Nano), snapshot.WindowEnd.UTC().Format(time.RFC3339Nano), snapshot.AsOf.UTC().Format(time.RFC3339Nano), out.Limit+1, q.Offset)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AccountAuditEvidenceItem
		if err = rows.Scan(&item.AccountID, &item.ServerID, &item.EventTime, &item.UploadBytes, &item.DownloadBytes, &item.ConnectionCount); err != nil {
			return out, err
		}
		if len(out.Items) == out.Limit {
			next := q.Offset + out.Limit
			if next <= 100000 {
				out.NextOffset = &next
			}
			break
		}
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > 0 {
		out.Status = "partial"
		out.Reason = "retained_connection_details_only_coverage_unknown"
	}
	return out, nil
}
