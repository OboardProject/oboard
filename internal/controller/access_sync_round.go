package controller

import (
	"context"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/store"
)

// accessSyncConcurrency bounds one reconciliation round. It is small on
// purpose: the point is that one slow node does not hold up the queue behind
// it, not to buy throughput with worker count. Each server still serializes
// against itself through the per-server in-flight guard.
const accessSyncConcurrency = 4

// accessSyncWork is one server the round decided actually needs evaluating.
type accessSyncWork struct {
	serverID int64
	// force skips the ledger's "already settled" answer, for reasons such as a
	// reconnect where the Agent's local state cannot be inferred from the ledger.
	force bool
}

// accessSyncPlan is what one round will do, in the order it will do it.
type accessSyncPlan struct {
	work    []accessSyncWork
	skipped int
}

// planAccessSyncRound turns the round's candidates into the work it must
// actually perform.
//
// candidates carry only ledger summary columns, and settled() answers from
// them plus an in-memory boundary check, so a fleet whose delivery is already
// confirmed costs one query and one comparison per server - never a server
// load, a package build, or a lease evaluation.
func planAccessSyncRound(candidates []store.AccessSyncCandidate, routingRevision uint64, hints []accessSyncHint, settled func(serverID int64) bool) accessSyncPlan {
	forced := make(map[int64]bool, len(hints))
	order := make([]int64, 0, len(hints)+len(candidates))
	seen := make(map[int64]bool, len(hints)+len(candidates))
	for _, hint := range hints {
		if hint.Force {
			forced[hint.ServerID] = true
		}
		if !seen[hint.ServerID] {
			seen[hint.ServerID] = true
			order = append(order, hint.ServerID)
		}
	}
	byID := make(map[int64]store.AccessSyncCandidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ServerID] = candidate
		if !seen[candidate.ServerID] {
			seen[candidate.ServerID] = true
			order = append(order, candidate.ServerID)
		}
	}

	plan := accessSyncPlan{}
	for _, serverID := range order {
		force := forced[serverID]
		if !force {
			candidate, known := byID[serverID]
			// "Confirmed" is not "nothing to do": a business boundary changes
			// what is owed without touching a row, so both must hold.
			ledgerSettled := known && candidate.Settled(routingRevision)
			if ledgerSettled && settled(serverID) {
				plan.skipped++
				continue
			}
			// The ledger says settled but a boundary passed. The per-server
			// evaluation would draw the same "already confirmed" conclusion from
			// the same row, so this refresh has to be forced through it.
			force = ledgerSettled
		}
		plan.work = append(plan.work, accessSyncWork{serverID: serverID, force: force})
	}
	return plan
}

// runAccessSyncPlan executes the plan with bounded concurrency, preserving the
// plan's priority order as the order work is handed out.
func runAccessSyncPlan(ctx context.Context, plan accessSyncPlan, reconcile func(ctx context.Context, serverID int64, force bool)) {
	if len(plan.work) == 0 {
		return
	}
	queue := make(chan accessSyncWork)
	var wg sync.WaitGroup
	workers := min(accessSyncConcurrency, len(plan.work))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				if ctx.Err() != nil {
					return
				}
				reconcile(ctx, item.serverID, item.force)
			}
		}()
	}
	for _, item := range plan.work {
		if ctx.Err() != nil {
			break
		}
		select {
		case queue <- item:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
}

// authorizationTimeBoundarySettled reports whether no time-driven authorization
// transition can have occurred for this server since its last issued lease.
//
// It answers from the lease this Controller actually issued: the lease records
// the projection it came from and the first boundary after it that could change
// this server's grant set. A server with no issued lease - a fresh start, a
// restart, an invalidated projection - is never settled, so the recovery scan
// still covers everything it used to.
func (s *Server) authorizationTimeBoundarySettled(projection *authorizationProjection, serverID int64, now time.Time) bool {
	if projection == nil {
		return false
	}
	s.authorizationLeases.mu.Lock()
	state := s.authorizationLeases.servers[serverID]
	s.authorizationLeases.mu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.lease == nil || state.projection != projection {
		return false
	}
	if state.nextBoundary.IsZero() {
		return true
	}
	return now.Before(state.nextBoundary)
}

// runtimeUsersSettledCache records which runtime-user package generation each
// server's last evaluation belonged to.
//
// Every business deadline that fires bumps the package generation, so a
// mismatch here means a time boundary passed since the evaluation even though
// the routing revision may be unchanged. A server with no record - a fresh
// start or a restart - is never settled.
type runtimeUsersSettledCache struct {
	mu      sync.Mutex
	servers map[int64]uint64
}

func (s *Server) markRuntimeUsersEvaluated(serverID int64, generation uint64) {
	s.runtimeUsersSettled.mu.Lock()
	if s.runtimeUsersSettled.servers == nil {
		s.runtimeUsersSettled.servers = map[int64]uint64{}
	}
	s.runtimeUsersSettled.servers[serverID] = generation
	s.runtimeUsersSettled.mu.Unlock()
}

func (s *Server) runtimeUsersTimeBoundarySettled(serverID int64, generation uint64) bool {
	s.runtimeUsersSettled.mu.Lock()
	defer s.runtimeUsersSettled.mu.Unlock()
	recorded, ok := s.runtimeUsersSettled.servers[serverID]
	return ok && recorded == generation
}

// forgetRuntimeUsersSettled removes a deleted server's record.
func (s *Server) forgetRuntimeUsersSettled(serverID int64) {
	s.runtimeUsersSettled.mu.Lock()
	delete(s.runtimeUsersSettled.servers, serverID)
	s.runtimeUsersSettled.mu.Unlock()
}
