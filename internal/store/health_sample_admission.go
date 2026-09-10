package store

import (
	"sync"
	"time"
)

// metricSampleAdmission remembers, per server, the earliest time a health
// report could produce a new resource sample.
//
// The conditional INSERT in the health transaction is already idempotent, but
// it still costs a statement inside the write transaction of every report, for
// every server, forever - and the overwhelming majority of those statements
// insert nothing. This cache answers the obvious cases in memory.
//
// It is deliberately one-directional: it only ever says "definitely too early",
// never "definitely due". A missing entry, a changed sampling interval, a
// restart, or any doubt sends the report to the database, where the INSERT
// remains the authority. Nothing else writes through it, so a sample written by
// another path can only make this cache pessimistic, never permissive.
type metricSampleAdmission struct {
	mu      sync.Mutex
	servers map[int64]metricSampleAdmissionEntry
}

type metricSampleAdmissionEntry struct {
	// nextDueAt is the sampled_at of the last inserted sample plus the interval
	// that was in force when it was inserted.
	nextDueAt time.Time
	// interval records which sampling policy produced nextDueAt, so a policy
	// change invalidates the entry instead of silently outliving it.
	interval time.Duration
}

// metricSampleDue reports whether a report at `at` may attempt a sample.
func (s *Store) metricSampleDue(serverID int64, at time.Time, interval time.Duration) bool {
	if serverID <= 0 || interval <= 0 {
		return true
	}
	s.metricSamples.mu.Lock()
	entry, ok := s.metricSamples.servers[serverID]
	s.metricSamples.mu.Unlock()
	if !ok || entry.interval != interval || entry.nextDueAt.IsZero() {
		return true
	}
	// A report timestamped before the entry was built (a late report, a
	// backwards clock) is never suppressed by it.
	if at.Before(entry.nextDueAt.Add(-interval)) {
		return true
	}
	return !at.Before(entry.nextDueAt)
}

// noteMetricSampleInserted records a committed sample.
func (s *Store) noteMetricSampleInserted(serverID int64, at time.Time) {
	interval := s.metricSampleMinInterval
	if interval <= 0 {
		interval = defaultMetricSampleMinInterval
	}
	s.metricSamples.mu.Lock()
	if s.metricSamples.servers == nil {
		s.metricSamples.servers = map[int64]metricSampleAdmissionEntry{}
	}
	s.metricSamples.servers[serverID] = metricSampleAdmissionEntry{nextDueAt: at.UTC().Add(interval), interval: interval}
	s.metricSamples.mu.Unlock()
}

// noteMetricSampleSuppressed is a no-op hook kept for symmetry: a suppressed
// report must not extend the window it was suppressed by, otherwise a busy
// server could starve its own sampling.
func (s *Store) noteMetricSampleSuppressed(int64) {}

// ForgetMetricSampleAdmission drops one server's admission entry. Deleting a
// server, or resetting its sampling policy, must not leave a window behind.
func (s *Store) ForgetMetricSampleAdmission(serverID int64) {
	s.metricSamples.mu.Lock()
	delete(s.metricSamples.servers, serverID)
	s.metricSamples.mu.Unlock()
}
