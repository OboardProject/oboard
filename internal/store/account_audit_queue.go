package store

import (
	"context"
	"errors"
)

type AccountAuditWork struct {
	UserID   int64 `json:"user_id"`
	Revision int64 `json:"revision"`
}

// ListAccountAuditWork is a bounded durable queue scan. A shared worker advances
// afterUserID, wrapping to zero only at the end; reads never consume the work.
func (s *Store) ListAccountAuditWork(ctx context.Context, afterUserID int64, limit int) ([]AccountAuditWork, error) {
	if afterUserID < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid audit work page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,revision FROM account_audit_dirty WHERE user_id>? ORDER BY user_id LIMIT ?`, afterUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	work := []AccountAuditWork{}
	for rows.Next() {
		var item AccountAuditWork
		if err := rows.Scan(&item.UserID, &item.Revision); err != nil {
			return nil, err
		}
		work = append(work, item)
	}
	return work, rows.Err()
}
