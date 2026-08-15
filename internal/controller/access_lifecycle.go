package controller

import (
	"context"
	"log"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// StartAccessLifecycleWorker turns time-driven authorization state changes
// into access changes: bindings whose window opens (fallback when no change
// claimed them), bindings and exceptions that expired, and pending allow
// exceptions whose start time arrived. It runs only in plan mode and is
// restart-safe: claims are persisted so nothing is processed twice. Instead of
// polling, it sleeps until the next due moment from the database (or the
// fallback interval when nothing is scheduled) and wakes early on
// authorization mutations.
func (s *Server) StartAccessLifecycleWorker(ctx context.Context) {
	immediateRuns := 0
	for {
		sleep := time.Minute
		now := time.Now()
		due, err := s.store.AccessLifecycleNextDue(ctx, now)
		if err == nil && due != nil {
			wait := due.Sub(now)
			switch {
			case wait <= 0:
				// Work is due right now (unclaimed fallback items). Retry
				// quickly at first, then back off so a persistently failing
				// claim cannot turn into a busy loop.
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
				if wait < time.Minute {
					sleep = wait
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
		case <-s.accessWorkersWake:
			timer.Stop()
		case <-timer.C:
		}
		s.reconcileAccessLifecycle(ctx)
	}
}

func (s *Server) reconcileAccessLifecycle(ctx context.Context) {
	now := time.Now()

	// Bindings whose window is open but that no access change deployed yet.
	bindings, err := s.store.ListBindingsDueForDeploy(ctx, now)
	if err != nil {
		log.Printf("access lifecycle bindings: %v", err)
		return
	}
	for _, binding := range bindings {
		if err := s.createBindingActivationChange(ctx, binding, now); err != nil {
			log.Printf("access lifecycle binding %d: %v", binding.ID, err)
		}
	}

	// Expired bindings whose removal was never finalized.
	expired, err := s.store.ListExpiredBindingsNeedingSync(ctx, now)
	if err != nil {
		log.Printf("access lifecycle expired bindings: %v", err)
		return
	}
	for _, binding := range expired {
		if err := s.createBindingExpiryChange(ctx, binding, now); err != nil {
			log.Printf("access lifecycle binding expiry %d: %v", binding.ID, err)
		}
	}

	// Pending allow exceptions with no owning change whose window is open.
	pending, err := s.store.ListPendingExceptionsWithoutChange(ctx, now)
	if err != nil {
		log.Printf("access lifecycle pending exceptions: %v", err)
		return
	}
	for _, ex := range pending {
		if err := s.createExceptionActivationChange(ctx, ex, now); err != nil {
			log.Printf("access lifecycle exception %d: %v", ex.ID, err)
		}
	}

	// Active exceptions whose expiry passed and whose removal was never synced.
	expiredExceptions, err := s.store.ListActiveExceptionsExpired(ctx, now)
	if err != nil {
		log.Printf("access lifecycle expired exceptions: %v", err)
		return
	}
	for _, ex := range expiredExceptions {
		if err := s.createExceptionExpiryChange(ctx, ex, now); err != nil {
			log.Printf("access lifecycle exception expiry %d: %v", ex.ID, err)
		}
	}
}

// createBindingActivationChange deploys a binding that became effective without
// an owning access change (for example a legacy-mode write before the switch,
// or a crash between saving and creating the change).
func (s *Server) createBindingActivationChange(ctx context.Context, binding model.UserPlanBinding, now time.Time) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return err
	}
	effective, err := s.store.ListEffectiveUserPlanBindings(ctx, now)
	if err != nil {
		return err
	}
	// The binding may still be pending (status='pending'): the effective list
	// excludes it, so force it in or the projections would deploy nothing and
	// activation would later publish an undeployed plan.
	snap := s.snapshotFromConfig(data, append(effective, binding), data.ActivePlanNodes, exceptions, now)
	projection := snap.Projection()
	servers := s.authServersForUserSnapshot(data, snap, binding.UserID)
	change, err := s.createAccessChange(ctx, nil, accessChangeDraft{
		changeType:         model.AccessChangeUserBindings,
		affectedUserCount:  1,
		payload:            accessChangePayload{UserIDs: []int64{binding.UserID}},
		prepareProjection:  projection,
		finalizeProjection: projection,
		serverIDs:          servers,
	})
	if err != nil {
		return err
	}
	if err := s.store.ClaimBindingsDeployedForUsers(ctx, []int64{binding.UserID}); err != nil {
		return err
	}
	log.Printf("access lifecycle: binding %d claimed by change %d", binding.ID, change.ID)
	return nil
}

// createBindingExpiryChange removes the credentials of a binding whose window
// ended. Prepare deploys the still-deployed old state, finalize the exact
// post-expiry state.
func (s *Server) createBindingExpiryChange(ctx context.Context, binding model.UserPlanBinding, now time.Time) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return err
	}
	effective, err := s.store.ListEffectiveUserPlanBindings(ctx, now)
	if err != nil {
		return err
	}
	oldAt := effectiveWindow(now, nil, binding.ExpiresAt)
	oldSnap := s.snapshotFromConfig(data, append(effective, binding), data.ActivePlanNodes, exceptions, oldAt)
	finalizeSnap := s.snapshotFromConfig(data, effective, data.ActivePlanNodes, exceptions, now)
	prepare := core.MergeProjections(oldSnap.Projection(), finalizeSnap.Projection())
	servers := s.authServersForUserSnapshot(data, oldSnap, binding.UserID)
	change, err := s.createAccessChange(ctx, nil, accessChangeDraft{
		changeType:         model.AccessChangeUserBindings,
		affectedUserCount:  1,
		payload:            accessChangePayload{UserIDs: []int64{binding.UserID}},
		prepareProjection:  prepare,
		finalizeProjection: finalizeSnap.Projection(),
		serverIDs:          servers,
	})
	if err != nil {
		return err
	}
	if err := s.store.MarkBindingsExpirySynced(ctx, []int64{binding.ID}); err != nil {
		return err
	}
	log.Printf("access lifecycle: binding %d expiry change %d", binding.ID, change.ID)
	return nil
}

// createExceptionActivationChange activates a pending allow exception: prepare
// deploys its node early, activation (at starts_at or immediately) makes the
// node visible to subscriptions.
func (s *Server) createExceptionActivationChange(ctx context.Context, ex model.UserNodeException, now time.Time) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	effective, err := s.store.ListEffectiveUserPlanBindings(ctx, now)
	if err != nil {
		return err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return err
	}
	active := ex
	active.Status = model.UserNodeExceptionActive
	withActive := exceptionsWith(exceptions, active)
	at := effectiveWindow(now, ex.StartsAt, &ex.ExpiresAt)
	snap := s.snapshotFromConfig(data, effective, data.ActivePlanNodes, withActive, at)
	projection := snap.Projection()
	servers := s.authServersForNode(data, ex.NodeType, ex.NodeID)
	change, err := s.createAccessChange(ctx, nil, accessChangeDraft{
		changeType:         model.AccessChangeExceptions,
		affectedUserCount:  1,
		activateAt:         &at,
		payload:            accessChangePayload{ExceptionIDs: []int64{ex.ID}, TargetStatus: string(model.UserNodeExceptionActive)},
		prepareProjection:  projection,
		finalizeProjection: projection,
		serverIDs:          servers,
	})
	if err != nil {
		return err
	}
	if err := s.store.SetUserNodeExceptionChange(ctx, ex.ID, change.ID); err != nil {
		return err
	}
	log.Printf("access lifecycle: exception %d activation change %d", ex.ID, change.ID)
	return nil
}

// createExceptionExpiryChange removes an expired exception's credentials and
// marks the row expired for the audit trail. Deny expiry restores the plan
// node, so prepare deploys the post-expiry state while the runtime still has
// the old (denied) state.
func (s *Server) createExceptionExpiryChange(ctx context.Context, ex model.UserNodeException, now time.Time) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	effective, err := s.store.ListEffectiveUserPlanBindings(ctx, now)
	if err != nil {
		return err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return err
	}
	oldAt := effectiveWindow(now, nil, &ex.ExpiresAt)
	oldSnap := s.snapshotFromConfig(data, effective, data.ActivePlanNodes, exceptions, oldAt)
	finalizeSnap := s.snapshotFromConfig(data, effective, data.ActivePlanNodes, exceptionsWithout(exceptions, ex.ID), now)
	prepare := core.MergeProjections(oldSnap.Projection(), finalizeSnap.Projection())
	servers := s.authServersForNode(data, ex.NodeType, ex.NodeID)
	change, err := s.createAccessChange(ctx, nil, accessChangeDraft{
		changeType:         model.AccessChangeExceptions,
		affectedUserCount:  1,
		payload:            accessChangePayload{ExceptionIDs: []int64{ex.ID}, TargetStatus: string(model.UserNodeExceptionExpired)},
		prepareProjection:  prepare,
		finalizeProjection: finalizeSnap.Projection(),
		serverIDs:          servers,
	})
	if err != nil {
		return err
	}
	if err := s.store.MarkExceptionsExpirySynced(ctx, []int64{ex.ID}); err != nil {
		return err
	}
	log.Printf("access lifecycle: exception %d expiry change %d", ex.ID, change.ID)
	return nil
}

// authServersForUserSnapshot resolves the authentication servers of one user's
// granted nodes.
func (s *Server) authServersForUserSnapshot(data store.FullRoutingConfig, snap *core.EffectiveAccessSnapshot, userID int64) []int64 {
	keys := snap.EffectiveNodeKeys(userID)
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, _, _ := core.AffectedAuthServers(keys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	return servers
}

// authServersForNode resolves the authentication servers of one node.
func (s *Server) authServersForNode(data store.FullRoutingConfig, nodeType model.AssignableNodeType, nodeID int64) []int64 {
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, _, _ := core.AffectedAuthServers(map[string]bool{core.NodeKeyOf(nodeType, nodeID): true}, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	return servers
}
