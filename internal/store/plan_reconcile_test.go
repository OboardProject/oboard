package store

import (
	"github.com/OboardProject/oboard/internal/model"
	"path/filepath"
	"testing"
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
