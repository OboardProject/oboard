package controller

import (
	"context"
	"errors"
	"log"
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
	// database lookup. Distinct unknown identities still cost one lookup
	// each; a repeated agent_id is answered from unknownAgents instead.
	agentAuthFailureLimit  = 8
	agentAuthFailureWindow = time.Minute
	// agentAuthBanTTL keeps an address that spent its failure budget from
	// starting a fresh lookup window every minute. Successful authentication
	// clears it immediately so a replacement Agent on the same address is
	// not stuck behind a decommissioned process.
	agentAuthBanTTL = 15 * time.Minute
	// agentUnknownTTL is how long a missing agent_id is answered without
	// touching SQLite. Decommissioned nodes retry on a fixed timer forever;
	// one lookup per quarter-hour is enough to notice a real re-enrollment
	// that reused the identifier, which enrollment does not do.
	agentUnknownTTL = 15 * time.Minute
	// agentAuthLookupTimeout bounds the credential query so a saturated
	// pool cannot hold the handler until the Agent's 20s HTTP client dies.
	agentAuthLookupTimeout = 3 * time.Second
)

var requestAbortLog = newMemoryRateLimiter()

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
	if s.agentCallbackRate.allow(key, limit, window, s.authNow()) {
		return true
	}
	s.logAgentRestriction("callback_rate", key, http.StatusTooManyRequests)
	fail(w, errors.New("rate limit exceeded"), http.StatusTooManyRequests)
	return false
}

func (s *Server) authNow() time.Time {
	if s != nil && s.authClock != nil {
		return s.authClock()
	}
	return time.Now()
}

// agentAuthBlocked reports whether this source address has already spent its
// Agent authentication-failure budget, or is still inside the longer ban that
// follows that budget. It is checked before the credential lookup so a
// decommissioned node retrying a revoked token cannot keep issuing database
// reads.
func (s *Server) agentAuthBlocked(ip string) bool {
	if ip == "" {
		return false
	}
	at := s.authNow()
	if s.agentAuthBans.has(ip, at) {
		return true
	}
	return s.agentAuthFailures.count(ip, agentAuthFailureWindow, at) >= agentAuthFailureLimit
}

func (s *Server) unknownAgentIdentity(agentID string) bool {
	return s.unknownAgents.has(agentID, s.authNow())
}

func (s *Server) rememberUnknownAgent(agentID string) {
	s.unknownAgents.put(agentID, agentUnknownTTL, s.authNow())
}

func (s *Server) forgetUnknownAgent(agentID string) {
	s.unknownAgents.delete(agentID)
}

// noteAgentAuthFailure records one failed Agent authentication for a source
// address. Spending the per-window budget starts the longer ban.
func (s *Server) noteAgentAuthFailure(ip string) {
	if ip == "" {
		return
	}
	at := s.authNow()
	s.logAgentRestriction("invalid_credentials", ip, http.StatusUnauthorized)
	s.agentAuthFailures.allow(ip, agentAuthFailureLimit, agentAuthFailureWindow, at)
	if s.agentAuthFailures.count(ip, agentAuthFailureWindow, at) >= agentAuthFailureLimit {
		s.agentAuthBans.put(ip, agentAuthBanTTL, at)
	}
}

// noteAgentAuthSuccess clears the failure window and the longer ban for a
// source address.
func (s *Server) noteAgentAuthSuccess(ip string) {
	s.agentAuthFailures.clear(ip)
	s.agentAuthBans.delete(ip)
}

func (s *Server) logAgentRestriction(reason, subject string, status int) {
	if s.agentDiagnosticRate != nil && !s.agentDiagnosticRate.allow(reason+":"+subject, 1, time.Minute, s.authNow()) {
		return
	}
	log.Printf("agent connection restricted reason=%s subject=%q http_status=%d", reason, subject, status)
}

func isRequestAbort(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

// ttlCache is a process-local expiry map with the same key ceiling as the
// Agent rate limiter.
type ttlCache struct {
	mu        sync.Mutex
	entries   map[string]time.Time
	lastSweep time.Time
}

func newTTLCache() *ttlCache {
	return &ttlCache{entries: map[string]time.Time{}}
}

func (c *ttlCache) has(key string, at time.Time) bool {
	if c == nil || key == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	expiry, ok := c.entries[key]
	if !ok {
		return false
	}
	if !expiry.After(at) {
		delete(c.entries, key)
		return false
	}
	return true
}

func (c *ttlCache) put(key string, ttl time.Duration, at time.Time) {
	if c == nil || key == "" || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(at)
	c.entries[key] = at.Add(ttl)
}

func (c *ttlCache) delete(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *ttlCache) sweepLocked(at time.Time) {
	if len(c.entries) < agentRateLimiterMaxKeys && at.Sub(c.lastSweep) < time.Minute {
		return
	}
	c.lastSweep = at
	for key, expiry := range c.entries {
		if !expiry.After(at) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) >= agentRateLimiterMaxKeys {
		c.entries = map[string]time.Time{}
	}
}
