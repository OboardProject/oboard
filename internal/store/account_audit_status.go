package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const accountAuditStatusReadLimit = 4096

type AccountAuditStatus struct {
	Status                  string           `json:"status"`
	PendingEventCount       *int64           `json:"pending_event_count"`
	PendingInboxReports     *int64           `json:"pending_inbox_reports"`
	PendingInboxBytes       *int64           `json:"pending_inbox_bytes"`
	PendingEvaluations      *int64           `json:"pending_evaluations"`
	PendingNotifications    *int64           `json:"pending_notifications"`
	OldestBacklogAgeSeconds *int64           `json:"oldest_backlog_age_seconds"`
	LastSnapshotTime        *string          `json:"last_snapshot_time"`
	Unavailable             []string         `json:"unavailable"`
	Degradation             []string         `json:"degradation"`
	Limits                  map[string]int64 `json:"limits"`
}

// AccountAuditStatus reads bounded indexed windows only; an exhausted window is
// unavailable rather than an approximate count. Nil IDs mean unrestricted access.
func (s *Store) AccountAuditStatus(ctx context.Context, ids []int64, global bool, now time.Time) (AccountAuditStatus, error) {
	out := AccountAuditStatus{Status: "pending", Unavailable: []string{}, Degradation: []string{}, Limits: map[string]int64{"status_read_rows": accountAuditStatusReadLimit, "inbox_reports": 4096, "inbox_bytes": 64 << 20, "inbox_reports_per_node": 256, "snapshot_freshness_seconds": 180}}
	if len(ids) > 4096 {
		return out, fmt.Errorf("audit resource filter too large")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	scope := func(column string) (string, []any) {
		if ids == nil {
			return "", nil
		}
		if len(ids) == 0 {
			return " WHERE 0", nil
		}
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		return " WHERE " + column + " IN (" + strings.TrimRight(strings.Repeat("?,", len(ids)), ",") + ")", args
	}
	var oldest int64
	ageKnown := true
	count := func(field, query string, args []any, target **int64) error {
		var scanned, n, stamp int64
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&scanned, &n, &stamp); err != nil {
			return err
		}
		if scanned > accountAuditStatusReadLimit {
			out.Unavailable = append(out.Unavailable, field)
			out.Degradation = append(out.Degradation, field+"_read_budget_exceeded")
			ageKnown = false
			return nil
		}
		*target = &n
		if stamp > 0 && (oldest == 0 || stamp < oldest) {
			oldest = stamp
		}
		return nil
	}
	where, args := scope("e.user_id")
	err = count("pending_event_count", `SELECT COUNT(*),COALESCE(SUM(review_status='pending' AND status<>'recovered'),0),COALESCE(MIN(CASE WHEN review_status='pending' AND status<>'recovered' THEN unixepoch(first_seen_at) END),0) FROM (SELECT e.status,e.first_seen_at,COALESCE(w.status,'pending') review_status FROM account_audit_events e LEFT JOIN account_audit_workflow w ON w.event_id=e.id`+where+` LIMIT 4097)`, args, &out.PendingEventCount)
	if err != nil {
		return out, err
	}
	where, args = scope("user_id")
	activityWhere, activityArgs := scope("account")
	err = count("pending_evaluations", `SELECT COUNT(*),COUNT(*),0 FROM (SELECT user_id FROM (SELECT user_id FROM account_audit_dirty`+where+` LIMIT 4097) UNION SELECT account FROM (SELECT account,revision,evaluated FROM account_activity_v1_dirty`+activityWhere+` LIMIT 4097) WHERE revision>evaluated)`, append(args, activityArgs...), &out.PendingEvaluations)
	if err != nil {
		return out, err
	}
	var activityScanned int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT account FROM account_activity_v1_dirty`+activityWhere+` LIMIT 4097)`, activityArgs...).Scan(&activityScanned); err != nil {
		return out, err
	}
	if activityScanned > 4096 {
		out.PendingEvaluations = nil
		out.Unavailable = append(out.Unavailable, "pending_evaluations")
		out.Degradation = append(out.Degradation, "evaluation_read_budget_exceeded")
	}
	if out.PendingEvaluations == nil || *out.PendingEvaluations > 0 {
		ageKnown = false
	}
	where, args = scope("e.user_id")
	err = count("pending_notifications", `SELECT COUNT(*),COALESCE(SUM(status IN ('pending','leased')),0),COALESCE(MIN(CASE WHEN status IN ('pending','leased') THEN created_at END),0) FROM (SELECT n.status,n.created_at FROM account_audit_events e LEFT JOIN account_audit_notifications n ON n.event_id=e.id`+where+` LIMIT 4097)`, args, &out.PendingNotifications)
	if err != nil {
		return out, err
	}
	if global {
		var n, bytes, stamp, perNode int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(n),0),COALESCE(SUM(size),0),COALESCE(MIN(minute),0),COALESCE(MAX(n),0) FROM (SELECT server,COUNT(*) n,SUM(size) size,MIN(minute) minute FROM (SELECT server,length(payload) size,minute FROM account_activity_v1_inbox WHERE payload IS NOT NULL ORDER BY minute LIMIT 4097) GROUP BY server)`).Scan(&n, &bytes, &stamp, &perNode)
		if err != nil {
			return out, err
		}
		if n > 4096 {
			out.Unavailable = append(out.Unavailable, "pending_inbox_reports", "pending_inbox_bytes")
			out.Degradation = append(out.Degradation, "inbox_read_budget_exceeded")
			ageKnown = false
		} else {
			out.PendingInboxReports = &n
			out.PendingInboxBytes = &bytes
			if stamp > 0 && (oldest == 0 || stamp < oldest) {
				oldest = stamp
			}
			if n >= 4096 || bytes >= 64<<20 || perNode >= 256 {
				out.Degradation = append(out.Degradation, "inbox_capacity_reached")
			}
		}
	} else {
		out.Unavailable = append(out.Unavailable, "pending_inbox_reports", "pending_inbox_bytes")
		ageKnown = false
	}
	where, args = scope("u.id")
	var scanned, missing, stale int64
	var last sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(as_of IS NULL),0),COALESCE(SUM(as_of IS NOT NULL AND (unixepoch(as_of) IS NULL OR unixepoch(as_of)<? OR snapshot_status<>'evaluated')),0),MAX(as_of) FROM (SELECT a.as_of,COALESCE(json_extract(a.snapshot,'$.status'),'pending') snapshot_status FROM users u LEFT JOIN account_audit_snapshots a ON a.user_id=u.id`+where+` LIMIT 4097)`, append([]any{now.Add(-180 * time.Second).Unix()}, args...)...).Scan(&scanned, &missing, &stale, &last)
	if err != nil {
		return out, err
	}
	if scanned > 4096 {
		out.Unavailable = append(out.Unavailable, "last_snapshot_time")
		out.Degradation = append(out.Degradation, "snapshot_read_budget_exceeded")
	} else {
		if last.Valid {
			out.LastSnapshotTime = &last.String
			out.Status = "available"
		}
		if missing > 0 {
			out.Status = "pending"
			out.Degradation = append(out.Degradation, "snapshots_missing")
		}
		if stale > 0 {
			out.Degradation = append(out.Degradation, "snapshots_stale_or_incomplete")
		}
	}
	if ageKnown {
		age := int64(0)
		if oldest > 0 && now.Unix() > oldest {
			age = now.Unix() - oldest
		}
		out.OldestBacklogAgeSeconds = &age
	} else {
		out.Unavailable = append(out.Unavailable, "oldest_backlog_age_seconds")
	}
	if len(out.Degradation) > 0 && out.Status != "pending" {
		out.Status = "degraded"
	}
	return out, tx.Commit()
}
