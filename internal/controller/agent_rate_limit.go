package controller

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	// agentRateLimiterMaxKeys bounds the in-process limiter so an attacker
	// cannot grow it by inventing keys. The keys are one per enrolled Agent for
	// the authenticated limiter and one per source address for the
	// authentication-failure limiter, so the ceiling is far above normal use.
	agentRateLimiterMaxKeys = 8192

	// agentAuthFailureLimit is how many failed Agent authentications a single
	// source address may spend per window before it is refused without a
	// database lookup. A correctly enrolled Agent never reaches it; a
	// decommissioned node that still holds a stale token does so immediately.
	agentAuthFailureLimit  = 20
	agentAuthFailureWindow = time.Minute
)

// rateWindow is one fixed counting window.
type rateWindow struct {
	start time.Time
	count int
}

// memoryRateLimiter is a process-local fixed-window limiter.
//
// The database-backed limiter takes an exclusive SQLite write transaction per
// request, which put every Agent callback in line behind the single writer for
// work that is not durable state. Agent callback budgets are per-minute and
// only protect the Controller's own capacity, so losing the counters on
// restart is harmless - a restart already resets the fleet's reporting phase.
// Durable budgets (enrollment, certificate issuance) stay on the store.
type memoryRateLimiter struct {
	mu        sync.Mutex
	entries   map[string]*rateWindow
	lastSweep time.Time
}

func newMemoryRateLimiter() *memoryRateLimiter {
	return &memoryRateLimiter{entries: map[string]*rateWindow{}}
}

// allow consumes one unit for key and reports whether it stayed within limit.
func (l *memoryRateLimiter) allow(key string, limit int, window time.Duration, at time.Time) bool {
	if key == "" || limit <= 0 || window <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(at, window)
	entry := l.entries[key]
	if entry == nil || at.Sub(entry.start) >= window {
		l.entries[key] = &rateWindow{start: at, count: 1}
		return true
	}
	if entry.count >= limit {
		return false
	}
	entry.count++
	return true
}

// count reports the current window count for key without consuming a unit.
func (l *memoryRateLimiter) count(key string, window time.Duration, at time.Time) int {
	if key == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry == nil || at.Sub(entry.start) >= window {
		return 0
	}
	return entry.count
}

// clear drops the window for key. Used when an Agent authenticates
// successfully so a transient failure never accumulates against its address.
func (l *memoryRateLimiter) clear(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweepLocked drops expired windows. It runs at most once per window, and
// unconditionally once the map reaches its ceiling.
func (l *memoryRateLimiter) sweepLocked(at time.Time, window time.Duration) {
	if len(l.entries) < agentRateLimiterMaxKeys && at.Sub(l.lastSweep) < window {
		return
	}
	l.lastSweep = at
	for key, entry := range l.entries {
		if at.Sub(entry.start) >= window {
			delete(l.entries, key)
		}
	}
	if len(l.entries) >= agentRateLimiterMaxKeys {
		// Every live window is still inside its period. Resetting is
		// fail-open for one window rather than growing without bound.
		l.entries = map[string]*rateWindow{}
	}
}

// allowAgentRate applies a process-local budget to an authenticated Agent
// callback and writes 429 when it is exceeded.
func (s *Server) allowAgentRate(w http.ResponseWriter, key string, limit int, window time.Duration) bool {
	if s.agentCallbackRate.allow(key, limit, window, time.Now()) {
		return true
	}
	fail(w, errors.New("rate limit exceeded"), http.StatusTooManyRequests)
	return false
}

// agentAuthBlocked reports whether this source address has already spent its
// Agent authentication-failure budget. It is checked before the credential
// lookup so a decommissioned node retrying a revoked token cannot keep issuing
// database reads.
func (s *Server) agentAuthBlocked(ip string) bool {
	if ip == "" {
		return false
	}
	return s.agentAuthFailures.count(ip, agentAuthFailureWindow, time.Now()) >= agentAuthFailureLimit
}

// noteAgentAuthFailure records one failed Agent authentication for a source
// address.
func (s *Server) noteAgentAuthFailure(ip string) {
	if ip == "" {
		return
	}
	s.agentAuthFailures.allow(ip, agentAuthFailureLimit, agentAuthFailureWindow, time.Now())
}

// noteAgentAuthSuccess clears the failure window for a source address.
func (s *Server) noteAgentAuthSuccess(ip string) {
	s.agentAuthFailures.clear(ip)
}
