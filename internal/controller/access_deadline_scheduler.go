package controller

import (
	"context"
	"log"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const accessDeadlineFallback = time.Minute

// StartAccessDeadlineScheduler restores upcoming authorization and runtime-user
// boundaries from authoritative SQLite state after Controller start or restart.
// When a boundary fires it marks affected delivery ledgers stale and wakes the
// existing authorization + runtime-users sync workers. Correctness does not
// depend on package/lease cache TTLs.
func (s *Server) StartAccessDeadlineScheduler(ctx context.Context) {
	immediateRuns := 0
	for {
		sleep := accessDeadlineFallback
		now := time.Now().UTC()
		due, err := s.store.AccessDeadlineNextDue(ctx, now)
		if err != nil {
			log.Printf("access deadline scheduler: next due: %v", err)
		} else if due != nil {
			wait := due.Sub(now)
			switch {
			case wait <= 0:
				immediateRuns++
				switch {
				case immediateRuns <= 3:
					sleep = 2 * time.Second
				case immediateRuns <= 10:
					sleep = 10 * time.Second
				default:
					sleep = 30 * time.Second
				}
			default:
				immediateRuns = 0
				sleep = wait + 50*time.Millisecond
				if sleep > time.Hour {
					sleep = time.Hour
				}
			}
		} else {
			immediateRuns = 0
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.accessDeadlineWake:
			timer.Stop()
			continue
		case <-timer.C:
		}
		s.fireAccessDeadlines(ctx, time.Now().UTC())
	}
}

func (s *Server) wakeAccessDeadlineScheduler() {
	if s.accessDeadlineWake == nil {
		return
	}
	select {
	case s.accessDeadlineWake <- struct{}{}:
	default:
	}
}

// fireAccessDeadlines invalidates authorization projections and runtime-user
// packages whose business windows crossed `at`, then wakes the sync workers.
// When no precise server set is available it marks every enrolled server stale
// so Stale*ServerIDs still drives a bounded wake pass.
func (s *Server) fireAccessDeadlines(ctx context.Context, at time.Time) {
	s.invalidateAuthorizationProjection()
	s.bumpRuntimeUserPackageGeneration()

	serverIDs, err := s.accessDeadlineAffectedServerIDs(ctx, at)
	if err != nil {
		log.Printf("access deadline scheduler: affected servers: %v", err)
		serverIDs = nil
	}
	if len(serverIDs) == 0 {
		serverIDs, err = s.store.EnrolledServerIDs(ctx)
		if err != nil {
			log.Printf("access deadline scheduler: enrolled servers: %v", err)
			s.wakeAuthorizationSync()
			s.wakeRuntimeUsersSync()
			return
		}
	}
	if err := s.store.MarkAuthorizationEvaluatedStale(ctx, serverIDs); err != nil {
		log.Printf("access deadline scheduler: mark authorization stale: %v", err)
	}
	if err := s.store.MarkRuntimeUsersEvaluatedStale(ctx, serverIDs); err != nil {
		log.Printf("access deadline scheduler: mark runtime users stale: %v", err)
	}
	if due, err := s.store.HasTrafficPeriodEndingNear(ctx, at, time.Second); err != nil {
		log.Printf("access deadline scheduler: traffic period check: %v", err)
	} else if due {
		if _, err := s.store.BumpTrafficPolicyRevision(ctx); err != nil {
			log.Printf("access deadline scheduler: bump traffic policy: %v", err)
		}
	}
	s.wakeAuthorizationSync()
	s.wakeRuntimeUsersSync()
}

func (s *Server) accessDeadlineAffectedServerIDs(ctx context.Context, at time.Time) ([]int64, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	keys := map[string]bool{}
	considerBinding := func(binding model.UserPlanBinding) {
		for _, pn := range data.ActivePlanNodes {
			if pn.PlanID == binding.PlanID && pn.Enabled {
				keys[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
			}
		}
	}
	considerException := func(ex model.UserNodeException) {
		keys[core.NodeKeyOf(ex.NodeType, ex.NodeID)] = true
	}

	if due, err := s.store.ListBindingsDueForDeploy(ctx, at); err == nil {
		for _, binding := range due {
			considerBinding(binding)
		}
	}
	if expired, err := s.store.ListExpiredBindingsNeedingSync(ctx, at); err == nil {
		for _, binding := range expired {
			considerBinding(binding)
		}
	}
	if pending, err := s.store.ListPendingExceptionsWithoutChange(ctx, at); err == nil {
		for _, ex := range pending {
			considerException(ex)
		}
	}
	if expired, err := s.store.ListActiveExceptionsExpired(ctx, at); err == nil {
		for _, ex := range expired {
			considerException(ex)
		}
	}
	// Bindings/exceptions whose window crosses `at` even when lifecycle already
	// claimed them still need lease/package refresh.
	for _, binding := range data.PlanBindings {
		if !binding.Enabled {
			continue
		}
		if crossesDeadline(binding.StartsAt, at) || crossesDeadline(binding.ExpiresAt, at) {
			considerBinding(binding)
		}
	}
	for _, ex := range data.UserNodeExceptions {
		if ex.Status != model.UserNodeExceptionActive && ex.Status != model.UserNodeExceptionPending {
			continue
		}
		if crossesDeadline(ex.StartsAt, at) || crossesDeadline(ex.ExpiresAt, at) {
			considerException(ex)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	servers, _, _ := core.AffectedAuthServers(keys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	return servers, nil
}

func crossesDeadline(boundary *time.Time, at time.Time) bool {
	if boundary == nil {
		return false
	}
	delta := boundary.UTC().Sub(at.UTC())
	if delta < 0 {
		delta = -delta
	}
	return delta <= time.Second
}

// invalidateAccessServers marks the precise server set stale and wakes the
// existing sync workers. serverIDs should already be the before∪after union for
// the mutation.
func (s *Server) invalidateAccessServers(ctx context.Context, serverIDs []int64) {
	ids := core.UnionInt64IDs(serverIDs)
	if len(ids) == 0 {
		s.wakeAuthorizationSync()
		s.wakeRuntimeUsersSync()
		s.wakeAccessDeadlineScheduler()
		return
	}
	s.invalidateAuthorizationProjection()
	s.invalidateRuntimeUserPackagesFor(ids)
	if err := s.store.MarkAuthorizationEvaluatedStale(ctx, ids); err != nil {
		log.Printf("invalidate access servers: authorization: %v", err)
	}
	if err := s.store.MarkRuntimeUsersEvaluatedStale(ctx, ids); err != nil {
		log.Printf("invalidate access servers: runtime users: %v", err)
	}
	s.wakeAuthorizationSync()
	s.wakeRuntimeUsersSync()
	s.wakeAccessDeadlineScheduler()
}

// accessServersFromProjections is the controller helper for access-change
// drafts: before∪after membership diff resolved against the current topology.
func accessServersFromProjections(before, after core.AccessProjection, data store.FullRoutingConfig) []int64 {
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	return core.AffectedServersFromAccessProjections(before, after, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
}
