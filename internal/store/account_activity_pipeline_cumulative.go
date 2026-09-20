package store

import (
	"context"
	"database/sql"
)

func comparableActivityCounters(newer, older AccountActivityBatchItem) bool {
	return newer.UploadBytes >= older.UploadBytes && newer.DownloadBytes >= older.DownloadBytes && newer.ActivityBits&older.ActivityBits == older.ActivityBits
}

// Numeric checkpoints outlive application, but not the admission lateness window.
// Checking both sides of the sequence prevents delayed snapshots from erasing a
// predecessor constraint after a newer snapshot has already been applied.
func validateActivityCumulative(ctx context.Context, tx *sql.Tx, b AccountActivityBatch) error {
	for _, r := range b.Items {
		rows, err := tx.QueryContext(ctx, `SELECT seq,up,down,bits FROM account_activity_v1_checkpoint WHERE server=? AND stream=? AND boot=? AND minute=? AND account=? AND inbound=? AND path=? AND source=? AND version=?`, b.ServerID, b.StreamType, b.CollectorBootID, b.MinuteUnix, r.AccountID, r.InboundID, r.PathID, r.counterSource(), b.SourceVersion)
		if err != nil {
			return err
		}
		for rows.Next() {
			var seq int64
			var old AccountActivityBatchItem
			if err = rows.Scan(&seq, &old.UploadBytes, &old.DownloadBytes, &old.ActivityBits); err != nil {
				rows.Close()
				return err
			}
			newer, older := r, old
			if seq > b.Sequence {
				newer, older = old, r
			}
			if !comparableActivityCounters(newer, older) {
				rows.Close()
				return ErrAccountActivityConflict
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func reclaimActivityDirty(ctx context.Context, tx *sql.Tx, clock int64) error {
	// Never release a reservation belonging to an acknowledged, pending report.
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_activity_v1_counter WHERE rowid IN (SELECT c.rowid FROM account_activity_v1_counter c WHERE minute<? AND NOT EXISTS(SELECT 1 FROM account_activity_v1_inbox i WHERE i.server=c.server AND i.stream=c.stream AND i.boot=c.boot AND i.minute=c.minute AND i.payload IS NOT NULL) LIMIT 500)`, clock-clock%60-1920); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM account_activity_v1_dirty WHERE account IN (SELECT d.account FROM account_activity_v1_dirty d WHERE revision=evaluated AND NOT EXISTS(SELECT 1 FROM account_activity_v1_counter c WHERE c.account=d.account) AND NOT EXISTS(SELECT 1 FROM account_activity_v1_expected e WHERE e.account=d.account) AND NOT EXISTS(SELECT 1 FROM account_activity_v1_source s WHERE s.account=d.account AND s.minute>=?) LIMIT 500)`, clock-clock%60-1920)
	return err
}

// Reserve counter and dirty slots in the receipt transaction, never at apply.
func reserveActivityCapacity(ctx context.Context, tx *sql.Tx, b AccountActivityBatch, clock int64) error {
	if err := reclaimActivityDirty(ctx, tx, clock); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_activity_v1_checkpoint WHERE rowid IN (SELECT rowid FROM account_activity_v1_checkpoint WHERE minute<? LIMIT 500)`, clock-180); err != nil {
		return err
	}
	for _, r := range b.Items {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO account_activity_v1_counter VALUES(?,?,?,?,?,?,?,?,?,0,0,0,0)`, b.ServerID, b.StreamType, b.CollectorBootID, b.MinuteUnix, r.AccountID, r.InboundID, r.PathID, r.counterSource(), b.SourceVersion); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO account_activity_v1_dirty(account,revision) VALUES(?,0)`, r.AccountID); err != nil {
			return err
		}
	}
	for _, table := range []string{"counter", "checkpoint", "dirty"} {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_activity_v1_`+table).Scan(&n); err != nil {
			return err
		}
		bound := 1048576
		if table == "dirty" {
			bound = 65536
		}
		if table == "checkpoint" {
			n += len(b.Items)
		}
		if n > bound {
			return ErrAccountActivityCapacity
		}
	}
	if err := validateActivityCumulative(ctx, tx, b); err != nil {
		return err
	}
	for _, r := range b.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_activity_v1_checkpoint VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.ServerID, b.StreamType, b.CollectorBootID, b.MinuteUnix, r.AccountID, r.InboundID, r.PathID, r.counterSource(), b.SourceVersion, b.Sequence, r.UploadBytes, r.DownloadBytes, r.ActivityBits); err != nil {
			return err
		}
	}
	return nil
}
