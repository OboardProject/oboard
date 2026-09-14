package store

import (
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestPlanReconcileCandidatesSkipIdlePlans(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	plan := &model.SubscriptionPlan{Name: "idle", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanReconcileIdle(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListSubscriptionPlansToReconcile(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("idle candidates: %#v, %v", items, err)
	}
	if err := s.SetPlanReconcileWaiting(ctx, plan.ID, "stale", "{}"); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListSubscriptionPlansToReconcile(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("stale status not cleaned: %#v, %v", items, err)
	}
	if err := s.SetPlanReconcileIdle(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	speed := 123
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{Settings: &PlanSettingsMutation{SpeedLimitMbps: &speed}}); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListSubscriptionPlansToReconcile(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("pending version missing: %#v, %v", items, err)
	}
}

func TestPlanReconcileClearsOnlyConvergedPending(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	plan := &model.SubscriptionPlan{Name: "pending", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	original := plan.CurrentRevisionID
	speed := 123
	saved, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{Settings: &PlanSettingsMutation{SpeedLimitMbps: &speed}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanReconcileIdleIfConverged(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetSubscriptionPlan(ctx, plan.ID)
	if current.PendingRevisionID != saved.Revision.ID {
		t.Fatal("cleared unsent revision")
	}
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, original, saved.Revision.ID, 0, nil); err != nil {
		t.Fatal(err)
	}
	// Reproduce a stale pointer left after a newer revision has activated.
	if _, err := s.db.ExecContext(ctx, `update subscription_plans set pending_revision_id=? where id=?`, original, plan.ID); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListSubscriptionPlansToReconcile(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("stale pending was not selected: %#v, %v", items, err)
	}
	change := &model.AccessChange{SourcePlanID: plan.ID, CandidateRevisionID: saved.Revision.ID, ChangeType: model.AccessChangePlanPublish, Status: model.AccessChangeFinalizing}
	id, err := s.CreateAccessChangeWithPendingBindings(ctx, change, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanReconcileApplying(ctx, plan.ID, saved.Revision.ID, id, "finalizing"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanReconcileIdleIfConverged(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	state, _ := s.GetPlanReconcileState(ctx, plan.ID)
	current, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if state.Status != "finalizing" || current.PendingRevisionID != original {
		t.Fatal("cleared in-flight change")
	}
	if err := s.UpdateAccessChangeStatus(ctx, id, []model.AccessChangeStatus{model.AccessChangeFinalizing}, model.AccessChangeFinalized, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanReconcileIdleIfConverged(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	state, _ = s.GetPlanReconcileState(ctx, plan.ID)
	if current.PendingRevisionID != 0 || state.Status != "idle" || state.ApplyingRevisionID != nil {
		t.Fatalf("stale state survives: %#v %#v", current, state)
	}
	if changed, err := s.SetPendingIfEmpty(ctx, plan.ID, saved.Revision.ID); err != nil || changed {
		t.Fatalf("reinstated completed revision: %v, %v", changed, err)
	}
	items, err = s.ListSubscriptionPlansToReconcile(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("converged plan still queued: %#v, %v", items, err)
	}
}
