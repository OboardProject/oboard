package controller

import (
	"context"
	"errors"
)

func (s *Server) queueAccountActivityDirty(ctx context.Context) error {
	work, err := s.store.ListAccountActivityDirty(ctx, 64)
	if err != nil {
		return err
	}
	var failures error
	for _, item := range work {
		if _, err = s.store.MarkAccountAuditDirty(ctx, item.AccountID); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		failures = errors.Join(failures, s.store.MarkAccountActivityPipelineEvaluated(ctx, item.AccountID, item.Revision))
	}
	return failures
}
