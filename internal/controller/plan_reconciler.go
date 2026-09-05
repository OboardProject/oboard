package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const defaultPlanReconcileDelay = 150 * time.Millisecond

func (s *Server) StartSubscriptionPlanReconciler(ctx context.Context) {
	if err := s.store.MigratePlanReconcileStates(ctx); err != nil {
		log.Printf("plan reconciler migrate: %v", err)
	}
	// Ensure every plan with current != latest gets a reconcile signal
	s.reconcilePlans(ctx)
	timer := time.NewTimer(s.planReconcileDelay())
	recovery := time.NewTicker(time.Second)
	defer timer.Stop()
	defer recovery.Stop()
	pending := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.planReconcileWake:
			pending = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.planReconcileDelay())
		case <-timer.C:
			if pending {
				s.reconcilePlans(ctx)
				pending = false
			}
		case <-recovery.C:
			s.reconcilePlans(ctx)
		}
	}
}

func (s *Server) planReconcileDelay() time.Duration {
	return defaultPlanReconcileDelay
}

func (s *Server) signalPlanReconcile(planID int64) {
	if s.planReconcileWake == nil {
		return
	}
	select {
	case s.planReconcileWake <- struct{}{}:
	default:
	}
	_ = planID
}

func (s *Server) reconcilePlans(ctx context.Context) {
	plans, err := s.store.ListSubscriptionPlansToReconcile(ctx)
	if err != nil {
		log.Printf("plan reconcile list plans: %v", err)
		return
	}
	for _, plan := range plans {
		if err := s.reconcileOnePlan(ctx, plan); err != nil {
			if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, store.ErrPlanRevisionConflict) {
				log.Printf("plan reconcile %d: %v", plan.ID, err)
			}
		}
	}
}

func (s *Server) reconcileOnePlan(ctx context.Context, plan model.SubscriptionPlan) error {
	if plan.CurrentRevisionID == plan.LatestRevisionID && plan.CurrentRevisionID != 0 {
		// Already converged, ensure reconcile state idle
		_ = s.store.SetPlanReconcileIdle(ctx, plan.ID)
		return nil
	}
	if plan.LatestRevisionID == 0 {
		return nil
	}
	// If there is an open access change for this plan, wait for it
	hasOpen, err := s.store.HasOpenPlanAccessChange(ctx, plan.ID)
	if err != nil {
		return err
	}
	if hasOpen {
		open, err := s.store.GetOpenPlanAccessChange(ctx, plan.ID)
		if err == nil && open != nil {
			_ = s.store.SetPlanReconcileApplying(ctx, plan.ID, open.CandidateRevisionID, open.ID, string(open.Status))
		}
		return nil
	}
	// Check if current already equals latest (after open check, pending cleared)
	// Re-read plan to get latest values after possible concurrent save
	fresh, err := s.store.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		return err
	}
	if fresh.CurrentRevisionID == fresh.LatestRevisionID {
		_ = s.store.SetPlanReconcileIdle(ctx, fresh.ID)
		return nil
	}
	targetRevisionID := fresh.LatestRevisionID
	// Dependency check: for MVP we treat all nodes as ready; allow saving
	// any node even if its ingress not yet deployed. The prepare phase will
	// still succeed; the subscription will filter only after activation.
	// We optionally check configuration sync readiness here and set waiting.
	if blocked, reason, details := s.planReconcileBlockedReason(ctx, fresh, targetRevisionID); blocked {
		blockedJSON, _ := json.Marshal(details)
		_ = s.store.SetPlanReconcileWaiting(ctx, fresh.ID, reason, string(blockedJSON))
		return nil
	}
	// Create access change for target
	revision, err := s.store.GetPlanRevision(ctx, fresh.ID, targetRevisionID)
	if err != nil {
		return err
	}
	change, err := s.createPlanPublishChangeForActor(ctx, nil, revision.CreatedBy, fresh, targetRevisionID)
	if err != nil {
		_ = s.store.SetPlanReconcileFailed(ctx, fresh.ID, err.Error())
		return err
	}
	// Mark pending pointer for compatibility and reconcile state
	if _, err := s.store.GetPlanReconcileState(ctx, fresh.ID); err != nil {
		// ensure state exists
	}
	_ = s.store.SetPlanReconcileApplying(ctx, fresh.ID, targetRevisionID, change.ID, string(change.Status))
	// Also ensure pending_revision_id reflects the applying revision for legacy clients
	_, _ = s.store.SetPendingIfEmpty(ctx, fresh.ID, targetRevisionID)
	return nil
}

func (s *Server) planReconcileBlockedReason(ctx context.Context, plan *model.SubscriptionPlan, targetRevisionID int64) (bool, string, map[string]any) {
	// MVP: always ready. Future: check required servers' configuration sync.
	// We keep hook to demonstrate waiting_dependency status without blocking saves.
	return false, "", nil
}
