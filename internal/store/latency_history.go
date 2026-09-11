package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type LatencyFailurePoint struct {
	At    time.Time `json:"at"`
	Count int64     `json:"count"`
}
type latencyHistoryRevision struct {
	version uint64
	through time.Time
}

// Revisions cover only queried servers and never grow with the report stream.
func (s *Store) LatencyHistoryRevision(serverID int64, through time.Time) uint64 {
	s.latencyHistoryMu.Lock()
	defer s.latencyHistoryMu.Unlock()
	if s.latencyHistory == nil {
		s.latencyHistory = make(map[int64]latencyHistoryRevision)
	}
	entry, ok := s.latencyHistory[serverID]
	if !ok {
		if len(s.latencyHistory) >= 256 {
			for id := range s.latencyHistory {
				delete(s.latencyHistory, id)
				break
			}
		}
		s.latencyHistoryVersion++
		entry.version = s.latencyHistoryVersion
	}
	if through.After(entry.through) {
		entry.through = through
	}
	s.latencyHistory[serverID] = entry
	return entry.version
}
func (s *Store) invalidateLatencyHistory(serverIDs ...int64) {
	s.latencyHistoryMu.Lock()
	defer s.latencyHistoryMu.Unlock()
	invalidate := func(id int64) {
		if entry, ok := s.latencyHistory[id]; ok {
			s.latencyHistoryVersion++
			entry.version = s.latencyHistoryVersion
			s.latencyHistory[id] = entry
		}
	}
	if len(serverIDs) == 0 {
		for id := range s.latencyHistory {
			invalidate(id)
		}
	} else {
		for _, id := range serverIDs {
			invalidate(id)
		}
	}
}
func (s *Store) invalidateLateLatency(serverID int64, at time.Time) {
	s.latencyHistoryMu.Lock()
	defer s.latencyHistoryMu.Unlock()
	if entry, ok := s.latencyHistory[serverID]; ok && at.Before(entry.through) {
		s.latencyHistoryVersion++
		entry.version = s.latencyHistoryVersion
		s.latencyHistory[serverID] = entry
	}
}

// Legacy sources predate the latency report lane. Generated latency_probe events
// are never read here, including after their authoritative reports are cleared.
func (s *Store) QueryLegacyLatencyBuckets(ctx context.Context, serverID int64, from, to time.Time, bucket time.Duration) (LatencyBucketResult, error) {
	return s.queryLegacyLatencyBuckets(ctx, s.db, serverID, from, to, bucket, false)
}

type latencyHistoryQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) queryLegacyLatencyBuckets(ctx context.Context, db latencyHistoryQueryer, serverID int64, from, to time.Time, bucket time.Duration, utcGrid bool) (LatencyBucketResult, error) {
	result := LatencyBucketResult{}
	if serverID <= 0 || from.Nanosecond() != 0 || to.Nanosecond() != 0 || !to.After(from) || bucket < time.Second || (to.Sub(from)+bucket-1)/bucket > 360 {
		return result, errors.New("invalid legacy latency window")
	}
	table, err := s.legacyLatencySource(ctx, db, serverID, from, to)
	if err != nil {
		return result, err
	}
	origin := from
	if utcGrid {
		origin = from.UTC().Truncate(bucket)
	}
	rows, err := db.QueryContext(ctx, `select cast((unixepoch(effective_at)-unixepoch(?))/? as integer),
 avg(case when available=1 and latency_ms>0 then latency_ms end),min(case when available=1 and latency_ms>0 then latency_ms end),max(case when available=1 and latency_ms>0 then latency_ms end),count(case when available=1 and latency_ms>0 then 1 end),count(case when available=0 then 1 end),max(effective_at)
 from `+table+` where server_id=? and kind='probe_result' and source<>'latency_probe' and effective_at>=? and effective_at<? group by 1 order by 1`, origin.UTC().Format("2006-01-02T15:04:05.000000000Z"), int64(bucket/time.Second), serverID, from.UTC().Format("2006-01-02T15:04:05.000000000Z"), to.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var index, count, failed int64
		var average sql.NullFloat64
		var minimum, maximum sql.NullInt64
		var observed string
		if err := rows.Scan(&index, &average, &minimum, &maximum, &count, &failed, &observed); err != nil {
			return result, err
		}
		at := origin.Add(time.Duration(index) * bucket)
		if at.Before(from) {
			at = from
		}
		if count > 0 {
			result.PublicPoints = append(result.PublicPoints, model.ServerRegionalLatencyPoint{Kind: "public", Available: true, CheckedAt: at, LatencyMS: average.Float64, MinLatencyMS: minimum.Int64, MaxLatencyMS: maximum.Int64, Count: count})
		}
		if failed > 0 {
			result.Failures = append(result.Failures, LatencyFailurePoint{At: at, Count: failed})
		}
		if at := parseTime(observed); at.After(result.ObservedThrough) {
			result.ObservedThrough = at
		}
	}
	return result, rows.Err()
}
