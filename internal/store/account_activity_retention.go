package store

import (
	"context"
	"time"
)

// CleanupAccountActivityPipeline is independent of admission transactions so a
// full budget can recover even while every new report is being backpressured.
// Unapplied payloads and their reserved counters are never discarded here.
func (s *Store) CleanupAccountActivityPipeline(ctx context.Context, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := at.UTC().Truncate(time.Minute).Unix() - 1920
	for _, table := range []string{"source", "expected", "coverage", "quality"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_activity_v1_`+table+` WHERE rowid IN (SELECT rowid FROM account_activity_v1_`+table+` WHERE minute<? LIMIT 500)`, cutoff); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_activity_v1_checkpoint WHERE rowid IN (SELECT rowid FROM account_activity_v1_checkpoint WHERE minute<? LIMIT 500)`, at.Unix()-180); err != nil {
		return err
	}
	if err = reclaimActivityDirty(ctx, tx, at.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
