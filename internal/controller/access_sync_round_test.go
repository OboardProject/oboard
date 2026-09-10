package controller

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/store"
)

func alwaysSettled(int64) bool { return true }
func neverSettled(int64) bool  { return false }

// TestAccessSyncPlanSkipsSettledServers proves a fleet that is confirmed for
// the current routing revision and has crossed no boundary produces no work.
func TestAccessSyncPlanSkipsSettledServers(t *testing.T) {
	candidates := []store.AccessSyncCandidate{
		{ServerID: 1, DesiredRevision: 4, ConfirmedRevision: 4, EvaluatedRoutingRevision: 9},
		{ServerID: 2, DesiredRevision: 4, ConfirmedRevision: 4, EvaluatedRoutingRevision: 9},
		{ServerID: 3, DesiredRevision: 4, ConfirmedRevision: 3, EvaluatedRoutingRevision: 9}, // unconfirmed
		{ServerID: 4, DesiredRevision: 4, ConfirmedRevision: 4, EvaluatedRoutingRevision: 8}, // stale revision
		{ServerID: 5}, // no ledger row yet
	}
	plan := planAccessSyncRound(candidates, 9, nil, alwaysSettled)
	if plan.skipped != 2 {
		t.Fatalf("skipped %d settled servers, want 2", plan.skipped)
	}
	got := map[int64]bool{}
	for _, item := range plan.work {
		got[item.serverID] = true
		if item.force {
			t.Fatalf("server %d was forced without a reason to force it", item.serverID)
		}
	}
	for _, id := range []int64{3, 4, 5} {
		if !got[id] {
			t.Fatalf("server %d needs work but was skipped", id)
		}
	}
}

// TestAccessSyncPlanForcesWhenABoundaryPassed proves "confirmed" alone never
// skips: a business boundary that changed no row still forces the evaluation
// through the per-server ledger check that would otherwise short-circuit.
func TestAccessSyncPlanForcesWhenABoundaryPassed(t *testing.T) {
	candidates := []store.AccessSyncCandidate{{ServerID: 7, DesiredRevision: 2, ConfirmedRevision: 2, EvaluatedRoutingRevision: 5}}
	plan := planAccessSyncRound(candidates, 5, nil, neverSettled)
	if len(plan.work) != 1 || plan.skipped != 0 {
		t.Fatalf("boundary-driven refresh was dropped: %+v", plan)
	}
	if !plan.work[0].force {
		t.Fatal("a boundary refresh must be forced past the ledger's confirmed answer")
	}
}

// TestAccessSyncPlanHonoursForcedHints proves a reconnect is evaluated even
// when the ledger says the Agent already confirmed everything.
func TestAccessSyncPlanHonoursForcedHints(t *testing.T) {
	candidates := []store.AccessSyncCandidate{
		{ServerID: 1, DesiredRevision: 3, ConfirmedRevision: 3, EvaluatedRoutingRevision: 2},
		{ServerID: 2, DesiredRevision: 3, ConfirmedRevision: 3, EvaluatedRoutingRevision: 2},
	}
	hints := []accessSyncHint{{ServerID: 2, Reason: accessSyncReasonReconnect, Priority: accessSyncPriority(accessSyncReasonReconnect), Force: true}}
	plan := planAccessSyncRound(candidates, 2, hints, alwaysSettled)
	if plan.skipped != 1 {
		t.Fatalf("skipped %d, want the one server without a hint", plan.skipped)
	}
	if len(plan.work) != 1 || plan.work[0].serverID != 2 || !plan.work[0].force {
		t.Fatalf("reconnect hint did not force evaluation: %+v", plan.work)
	}
}

// TestAccessSyncPlanOrdersByPriority proves a revoke and a reconnect are handed
// out before a fleet-wide recovery sweep.
func TestAccessSyncPlanOrdersByPriority(t *testing.T) {
	set := &accessSyncHintSet{}
	set.note(accessSyncReasonChange, []int64{10})
	set.note(accessSyncReasonRevoke, []int64{20})
	set.note(accessSyncReasonReconnect, []int64{30})
	set.note(accessSyncReasonDeadline, []int64{40})
	hints := set.drain()
	want := []int64{20, 30, 40, 10}
	if len(hints) != len(want) {
		t.Fatalf("drained %d hints, want %d", len(hints), len(want))
	}
	for i, id := range want {
		if hints[i].ServerID != id {
			t.Fatalf("position %d = server %d, want %d (order %+v)", i, hints[i].ServerID, id, hints)
		}
	}
	candidates := []store.AccessSyncCandidate{{ServerID: 10}, {ServerID: 20}, {ServerID: 30}, {ServerID: 40}}
	plan := planAccessSyncRound(candidates, 1, hints, neverSettled)
	for i, id := range want {
		if plan.work[i].serverID != id {
			t.Fatalf("plan position %d = server %d, want %d", i, plan.work[i].serverID, id)
		}
	}
}

// TestAccessSyncHintsMergeAndPreserveStrongestReason proves a burst of changes
// for one server costs one reconciliation, and that a forcing reason survives
// the merge regardless of arrival order.
func TestAccessSyncHintsMergeAndPreserveStrongestReason(t *testing.T) {
	set := &accessSyncHintSet{}
	for range 50 {
		set.note(accessSyncReasonChange, []int64{5})
	}
	set.note(accessSyncReasonReconnect, []int64{5})
	for range 50 {
		set.note(accessSyncReasonChange, []int64{5})
	}
	hints := set.drain()
	if len(hints) != 1 {
		t.Fatalf("101 hints for one server merged into %d entries", len(hints))
	}
	if !hints[0].Force || hints[0].Reason != accessSyncReasonReconnect {
		t.Fatalf("merge lost the forcing reason: %+v", hints[0])
	}
	if set.size() != 0 {
		t.Fatal("drain left hints behind")
	}
	// A hint noted after the drain belongs to the next round.
	set.note(accessSyncReasonChange, []int64{6})
	if set.size() != 1 {
		t.Fatal("a hint noted while a round runs must be kept for the next round")
	}
	// Naming no server is a deliberate "scope unknown" signal.
	set.note(accessSyncReasonChange, nil)
	if set.size() != 1 {
		t.Fatal("an unscoped wake must not invent a hint")
	}
}

// TestRunAccessSyncPlanIsBounded proves the round never runs more work in
// parallel than the configured bound, and that every server is visited once.
func TestRunAccessSyncPlanIsBounded(t *testing.T) {
	plan := accessSyncPlan{}
	for i := range 40 {
		plan.work = append(plan.work, accessSyncWork{serverID: int64(i + 1)})
	}
	var inFlight, peak atomic.Int64
	var mu sync.Mutex
	seen := map[int64]int{}
	runAccessSyncPlan(context.Background(), plan, func(_ context.Context, serverID int64, _ bool) {
		current := inFlight.Add(1)
		for {
			high := peak.Load()
			if current <= high || peak.CompareAndSwap(high, current) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		mu.Lock()
		seen[serverID]++
		mu.Unlock()
		inFlight.Add(-1)
	})
	if peak.Load() > accessSyncConcurrency {
		t.Fatalf("round ran %d servers in parallel, bound is %d", peak.Load(), accessSyncConcurrency)
	}
	if len(seen) != len(plan.work) {
		t.Fatalf("visited %d of %d servers", len(seen), len(plan.work))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("server %d visited %d times", id, count)
		}
	}
}

// TestRunAccessSyncPlanStopsOnCancel proves a cancelled round does not keep
// handing out work.
func TestRunAccessSyncPlanStopsOnCancel(t *testing.T) {
	plan := accessSyncPlan{}
	for i := range 200 {
		plan.work = append(plan.work, accessSyncWork{serverID: int64(i + 1)})
	}
	ctx, cancel := context.WithCancel(context.Background())
	var handled atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAccessSyncPlan(ctx, plan, func(_ context.Context, _ int64, _ bool) {
			if handled.Add(1) == 4 {
				cancel()
			}
			time.Sleep(time.Millisecond)
		})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("cancelled round did not stop")
	}
	if handled.Load() >= int64(len(plan.work)) {
		t.Fatalf("cancelled round still handled %d of %d servers", handled.Load(), len(plan.work))
	}
}
