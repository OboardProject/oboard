package controller

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/store"
)

// Distinct unknown identities still cost one lookup each; the same
// decommissioned agent_id is answered from the negative cache and must not
// fill the per-address budget by itself.
func TestAgentAuthFailureBudgetStopsReachingTheCredentialLookup(t *testing.T) {
	db, _, _, _, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	body := map[string]any{"reports": []map[string]any{}}
	for i := 0; i < agentAuthFailureLimit; i++ {
		postAgentTraffic(t, h, fmt.Sprintf("ghost-agent-%d", i), "revoked", body, http.StatusUnauthorized)
	}
	postAgentTraffic(t, h, "ghost-agent-last", "revoked", body, http.StatusTooManyRequests)
}

func TestUnknownAgentIdentityIsRejectedWithoutAnotherLookup(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	body := map[string]any{"reports": []map[string]any{}}
	postAgentTraffic(t, h, "ghost-agent", "revoked", body, http.StatusUnauthorized)
	if got := srv.agentAuthLookups.Load(); got != 1 {
		t.Fatalf("lookups = %d, want 1", got)
	}
	postAgentTraffic(t, h, "ghost-agent", "revoked", body, http.StatusUnauthorized)
	if got := srv.agentAuthLookups.Load(); got != 1 {
		t.Fatalf("cached unknown identity issued another lookup: %d", got)
	}
}

func TestAgentAuthBanOutlivesTheFailureWindow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	srv := newTestServer(db, "test-secret", "")
	srv.authClock = func() time.Time { return now }
	h := srv.Handler()
	body := map[string]any{"reports": []map[string]any{}}
	for i := 0; i < agentAuthFailureLimit; i++ {
		postAgentTraffic(t, h, fmt.Sprintf("ghost-agent-%d", i), "revoked", body, http.StatusUnauthorized)
	}
	postAgentTraffic(t, h, "ghost-agent-last", "revoked", body, http.StatusTooManyRequests)
	now = now.Add(2 * time.Minute)
	postAgentTraffic(t, h, "ghost-agent-after-window", "revoked", body, http.StatusTooManyRequests)
	now = now.Add(agentAuthBanTTL)
	postAgentTraffic(t, h, "ghost-agent-after-ban", "revoked", body, http.StatusUnauthorized)
}

func TestFailMapsCanceledContextToServiceUnavailable(t *testing.T) {
	rr := httptest.NewRecorder()
	fail(rr, context.Canceled, http.StatusInternalServerError)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "internal server error") || !strings.Contains(rr.Body.String(), "request canceled") {
		t.Fatalf("body = %s", rr.Body.String())
	}
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

func TestAgentRateRestrictionLogsOnceWithoutChangingBudget(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	s := &Server{agentCallbackRate: newMemoryRateLimiter(), agentDiagnosticRate: newMemoryRateLimiter()}
	if !s.allowAgentRate(httptest.NewRecorder(), "agent-traffic:node-a", 1, time.Minute) {
		t.Fatal("first request rejected")
	}
	for range 3 {
		if s.allowAgentRate(httptest.NewRecorder(), "agent-traffic:node-a", 1, time.Minute) {
			t.Fatal("logging changed callback limit")
		}
	}
	got := output.String()
	if strings.Count(got, "agent connection restricted") != 1 || !strings.Contains(got, "reason=callback_rate") || !strings.Contains(got, "http_status=429") {
		t.Fatalf("incorrect restriction log: %s", got)
	}
}
