package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

// ExcludeAccountActivityBaseline is additive: shorter exclusions cannot undo a
// prior investigation/action hold. A controller must set this before evaluation.
func (s *Store) ExcludeAccountActivityBaseline(ctx context.Context, accountID int64, until time.Time) error {
	if accountID <= 0 {
		return ErrAccountActivityInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_activity_v1_baseline_exclusion VALUES(?,?) ON CONFLICT(account) DO UPDATE SET until_minute=MAX(until_minute,excluded.until_minute)`, accountID, until.UTC().Truncate(time.Minute).Unix())
	return err
}

// RecordAccountActivityBaseline admits only complete, finalized, quiet evidence.
// A day is a fixed histogram, never a scan of raw connection history. Advancing
// the checkpoint and its histogram is one transaction; rereads add no samples.
func (s *Store) RecordAccountActivityBaseline(ctx context.Context, snapshot auditrisk.Snapshot, now time.Time) error {
	if snapshot.AccountID <= 0 || snapshot.Versions.Source == "" || snapshot.Policy.Version == "" {
		return ErrAccountActivityInvalid
	}
	if err := snapshot.Policy.Validate(); err != nil {
		return err
	}
	if snapshot.Activity == nil || snapshot.Activity.Upper >= 40 || (snapshot.Exposure != nil && snapshot.Exposure.Lower >= 40) || snapshot.Quality.TimeAligned.State != auditrisk.Satisfied || snapshot.Quality.SourceSetComplete.State != auditrisk.Satisfied {
		return nil
	}
	tx, err := s.db.BeginCountedTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE account_activity_v1_clock SET now_seconds=MAX(now_seconds,?) WHERE id=1`, now.Unix()); err != nil {
		return err
	}
	var excluded int64
	err = tx.QueryRowContext(ctx, `SELECT until_minute FROM account_activity_v1_baseline_exclusion WHERE account=?`, snapshot.AccountID).Scan(&excluded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cutoff := now.UTC().Truncate(time.Minute).Unix() - 180
	for _, minute := range snapshot.Features.Activity {
		m := minute.Start.Unix()
		if m > cutoff || m <= excluded || m < now.Unix()-14*86400 || minute.Sources.Lower != minute.Sources.Upper || minute.Sources.Lower <= 0 || minute.Sources.Lower > snapshot.Policy.ActivitySources.Start || minute.Sources.Lower > 32 {
			continue
		}
		day := m / 86400 * 86400
		var saved []byte
		var last int64
		var histogram auditrisk.BaselineDay
		err = tx.QueryRowContext(ctx, `SELECT histogram,last_minute FROM account_activity_v1_baseline WHERE account=? AND day=? AND policy=? AND version=?`, snapshot.AccountID, day, snapshot.Policy.Version, snapshot.Versions.Source).Scan(&saved, &last)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(saved) > 0 {
			if err = json.Unmarshal(saved, &histogram); err != nil {
				return err
			}
		}
		if m <= last {
			continue
		}
		histogram.Histogram[minute.Sources.Lower]++
		saved, err = json.Marshal(histogram)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_baseline VALUES(?,?,?,?,?,?) ON CONFLICT(account,day,policy,version) DO UPDATE SET histogram=excluded.histogram,last_minute=excluded.last_minute`, snapshot.AccountID, day, snapshot.Policy.Version, snapshot.Versions.Source, saved, m); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_activity_v1_baseline WHERE day<? OR (account=? AND (policy<>? OR version<>?))`, now.UTC().Truncate(24*time.Hour).Unix()-13*86400, snapshot.AccountID, snapshot.Policy.Version, snapshot.Versions.Source); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LoadAccountActivityBaseline(ctx context.Context, accountID int64, asOf time.Time, policyVersion, sourceVersion string) (auditrisk.Baseline, error) {
	var empty auditrisk.Baseline
	rows, err := s.db.QueryContext(ctx, `SELECT histogram FROM account_activity_v1_baseline WHERE account=? AND day>=? AND day<=? AND policy=? AND version=? ORDER BY day LIMIT 14`, accountID, asOf.UTC().Truncate(24*time.Hour).Unix()-13*86400, asOf.Unix(), policyVersion, sourceVersion)
	if err != nil {
		return empty, err
	}
	defer rows.Close()
	var days []auditrisk.BaselineDay
	for rows.Next() {
		var data []byte
		var day auditrisk.BaselineDay
		if err = rows.Scan(&data); err != nil {
			return empty, err
		}
		if err = json.Unmarshal(data, &day); err != nil {
			return empty, err
		}
		days = append(days, day)
	}
	if err = rows.Err(); err != nil {
		return empty, err
	}
	return auditrisk.SummarizeBaseline(days)
}
