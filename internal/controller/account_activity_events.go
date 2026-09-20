package controller

import (
	"context"
	"errors"

	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) pendingAccountAuditEventCount(ctx context.Context) (int, error) {
	total := 0
	for offset := 0; offset < 10000; offset += 100 {
		page, err := s.store.ListAccountAuditEvents(ctx, store.AccountAuditQuery{Status: "pending", Limit: 100, Offset: offset})
		if err != nil {
			return 0, err
		}
		events, ok := page.Items.([]store.AccountAuditEvent)
		if !ok {
			return 0, errors.New("invalid account event page")
		}
		total += len(events)
		if page.NextOffset == nil {
			return total, nil
		}
	}
	return 0, errors.New("account audit summary capacity reached")
}
