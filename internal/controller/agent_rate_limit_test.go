package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The per-Agent budget is keyed by an identity a failed request never
// establishes, so before this gate every retry from a decommissioned node still
// cost a credential lookup.
func TestAgentAuthFailureBudgetStopsReachingTheCredentialLookup(t *testing.T) {
	db, _, _, _, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	body := map[string]any{"reports": []map[string]any{}}
	for i := 0; i < agentAuthFailureLimit; i++ {
		postAgentTraffic(t, h, "ghost-agent", "revoked", body, http.StatusUnauthorized)
	}
	postAgentTraffic(t, h, "ghost-agent", "revoked", body, http.StatusTooManyRequests)
}

// A valid Agent is never blocked by another source address spending the budget,
// and its own successful authentication clears any earlier failure.
func TestAgentAuthFailureBudgetDoesNotBlockValidAgents(t *testing.T) {
	db, server, _, _, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	body := map[string]any{"reports": []map[string]any{}}
	postAgentTraffic(t, h, server.AgentID, "wrong-token", body, http.StatusUnauthorized)
	for i := 0; i < agentAuthFailureLimit+5; i++ {
		postAgentTraffic(t, h, server.AgentID, "token-a", body, http.StatusOK)
	}
}

func TestMemoryRateLimiterWindow(t *testing.T) {
	limiter := newMemoryRateLimiter()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if !limiter.allow("k", 3, time.Minute, start) {
			t.Fatalf("request %d refused inside the budget", i)
		}
	}
	if limiter.allow("k", 3, time.Minute, start) {
		t.Fatal("budget was not enforced")
	}
	if limiter.count("k", time.Minute, start) != 3 {
		t.Fatalf("count = %d", limiter.count("k", time.Minute, start))
	}
	// A separate key has its own budget, and the window expires.
	if !limiter.allow("other", 3, time.Minute, start) {
		t.Fatal("keys are not independent")
	}
	if !limiter.allow("k", 3, time.Minute, start.Add(time.Minute)) {
		t.Fatal("the window did not expire")
	}
	limiter.clear("k")
	if limiter.count("k", time.Minute, start.Add(time.Minute)) != 0 {
		t.Fatal("clear did not drop the window")
	}
}

// The budget must not depend on SQLite: it is checked on the request path of
// every Agent callback, and a write transaction there put the whole fleet in
// line behind the single writer.
func TestAllowAgentRateWritesTooManyRequests(t *testing.T) {
	s := &Server{agentCallbackRate: newMemoryRateLimiter()}
	rr := httptest.NewRecorder()
	if !s.allowAgentRate(rr, "k", 1, time.Minute) {
		t.Fatal("first request refused")
	}
	rr = httptest.NewRecorder()
	if s.allowAgentRate(rr, "k", 1, time.Minute) {
		t.Fatal("budget was not enforced")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rr.Code)
	}
}
