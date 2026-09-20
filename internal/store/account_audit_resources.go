package store

import (
	"context"
	"database/sql"
	"hash/maphash"
	"math"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

// AccountSubscriptionRequestRate describes the last completed UTC minute. Rate
// uses integer requests/second, rounded down; Requests preserves the exact count.
type AccountSubscriptionRequestRate struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Requests uint64    `json:"requests"`
	Rate     *uint64   `json:"rate"`
	Complete bool      `json:"complete"`
}

func (s *Store) AccountSubscriptionRequestRate(userID int64, asOf time.Time) AccountSubscriptionRequestRate {
	end := asOf.UTC().Truncate(time.Minute)
	out := AccountSubscriptionRequestRate{Start: end.Add(-time.Minute), End: end}
	l := &s.subscriptionLimiter
	l.once.Do(func() { l.seed = maphash.MakeSeed(); l.started = asOf })
	key := "account:" + strconv.FormatInt(userID, 10)
	shard := &l.shards[maphash.String(l.seed, key)%subscriptionLimitShards]
	shard.Lock()
	defer shard.Unlock()
	b, ok := shard.buckets[key]
	if !ok || b.observedSince.IsZero() || b.observedSince.After(out.Start) || b.at.After(asOf) {
		return out
	}
	slot := b.requests[uint64(out.Start.Unix()/60)&1]
	if slot.minute == out.Start.Unix() {
		if slot.overflow {
			return out
		}
		out.Requests = slot.count
	} else if b.at.Before(end) && !b.at.Before(out.Start) {
		return out
	}
	rate := out.Requests / 60
	out.Rate, out.Complete = &rate, true
	return out
}

// LoadAccountAuditResourceDimensions attaches only measured observations. No
// connection-count collector exists; no fleet size or quota is a substitute.
func (s *Store) LoadAccountAuditResourceDimensions(ctx context.Context, userID int64, asOf time.Time, config AccountAuditPolicy, quality auditrisk.Quality) ([]auditrisk.ResourceDimension, error) {
	if err := ValidateAccountAuditPolicy(config); err != nil {
		return nil, err
	}
	dimensions := []auditrisk.ResourceDimension{
		{Name: "request_rate", Unit: "requests/second"},
		{Name: "connections", Unit: "connections"},
		{Name: "traffic_rate", Unit: "bytes/second"},
	}
	for i, threshold := range []*AuditResourceThreshold{config.Resources.RequestRate, config.Resources.Connections, config.Resources.TrafficRate} {
		if threshold != nil {
			dimensions[i].Start, dimensions[i].Full = &threshold.Start, &threshold.Full
		}
	}
	if config.Resources.RequestRate != nil {
		observed := s.AccountSubscriptionRequestRate(userID, asOf)
		if observed.Complete {
			// Normalize exact minute counts against scaled per-second limits;
			// do not truncate the observation before the scorer's sole rounding.
			start, full := config.Resources.RequestRate.Start*60, config.Resources.RequestRate.Full*60
			dimensions[0].Unit = "requests/minute"
			dimensions[0].Start, dimensions[0].Full = &start, &full
			dimensions[0].Value = &observed.Requests
		}
	}
	if config.Resources.TrafficRate == nil {
		return dimensions, nil
	}
	for _, dimension := range []auditrisk.Dimension{quality.IdentityTrusted, quality.Deduplicated, quality.MeasurementValid, quality.CoverageComplete, quality.TimeAligned, quality.SourceSetComplete, quality.Freshness, quality.CapabilitySupported} {
		if dimension.State != auditrisk.Satisfied {
			return dimensions, nil
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	minute := asOf.UTC().Truncate(time.Minute).Add(-time.Minute).Unix()
	var expected, covered, versions int
	var sourceVersion string
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(e.capable=1 AND c.complete=1 AND c.aligned=1),0),COUNT(DISTINCT c.version),COALESCE(MIN(c.version),'') FROM account_activity_v1_expected e LEFT JOIN account_activity_v1_coverage c ON c.server=e.server AND c.stream=e.stream AND c.minute=e.minute WHERE e.account=? AND e.minute=?`, userID, minute).Scan(&expected, &covered, &versions, &sourceVersion)
	if err != nil {
		return nil, err
	}
	if expected == 0 || expected != covered || versions != 1 {
		return dimensions, nil
	}
	var overflow int
	err = tx.QueryRowContext(ctx, `SELECT overflow FROM account_activity_v1_quality WHERE account=? AND minute=?`, userID, minute).Scan(&overflow)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if overflow != 0 {
		return dimensions, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT bytes FROM account_activity_v1_source WHERE account=? AND minute=? AND version=? ORDER BY source LIMIT 33`, userID, minute, sourceVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var total uint64
	count := 0
	for rows.Next() {
		var bytes uint64
		if err := rows.Scan(&bytes); err != nil {
			return nil, err
		}
		count++
		if count > 32 || bytes > math.MaxUint64-total {
			return dimensions, nil
		}
		total += bytes
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	start, full := config.Resources.TrafficRate.Start*60, config.Resources.TrafficRate.Full*60
	dimensions[2].Unit = "bytes/minute"
	dimensions[2].Start, dimensions[2].Full = &start, &full
	dimensions[2].Value = &total
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return dimensions, tx.Commit()
}
