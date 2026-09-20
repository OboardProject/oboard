package controller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/OboardProject/oboard/internal/store"
)

// A shared, bounded sweep advances windows even for subscription-only accounts.
// Reads do not evaluate; only durable dirty markers are handed to the worker.
func (s *Server) queueAccountActivityExpiry(ctx context.Context, now time.Time, offset int) (int, error) {
	page, err := s.store.ListAccountAuditSnapshots(ctx, store.AccountAuditQuery{Limit: 64, Offset: offset})
	if err != nil {
		return offset, err
	}
	for _, row := range page.Items.([]store.AccountAuditRow) {
		if len(row.Snapshot) == 0 {
			continue
		}
		var snapshot struct {
			AsOf time.Time `json:"as_of"`
		}
		if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
			return offset, err
		}
		if snapshot.AsOf.Before(now.Truncate(time.Minute)) {
			if _, err := s.store.MarkAccountAuditDirty(ctx, row.UserID); err != nil {
				return offset, err
			}
		}
	}
	if page.NextOffset == nil {
		return 0, nil
	}
	return *page.NextOffset, nil
}
