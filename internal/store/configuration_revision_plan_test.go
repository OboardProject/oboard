package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConfigurationRevisionIncludesActivePlanAndBindingTransitions(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "revision-plan-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "33333333-3333-4333-8333-333333333333", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "revision-plan", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	baseline, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	draftID, err := s.UpdatePlanDraftLimits(ctx, plan.ID, plan.Revision, 100, 1024, "monthly", 1)
	if err != nil || draftID <= 0 {
		t.Fatalf("update plan draft id=%d err=%v", draftID, err)
	}
	draftRevision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if draftRevision != baseline {
		t.Fatalf("plan draft advanced runtime revision: %d -> %d", baseline, draftRevision)
	}
	if _, err := s.PublishPlanRevisionGuarded(ctx, plan.ID, plan.ActiveRevisionID, draftID); err != nil {
		t.Fatal(err)
	}
	publishedRevision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if publishedRevision <= draftRevision {
		t.Fatalf("active plan publish did not advance runtime revision: %d -> %d", draftRevision, publishedRevision)
	}
	baseline = publishedRevision
	if err := s.SetUserPlanBindingsPending(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: plan.ID}}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != baseline {
		t.Fatalf("pending binding advanced configuration revision: %d -> %d", baseline, pending)
	}
	if err := s.SetUserPlanBindingsActiveForUsers(ctx, []int64{user.ID}); err != nil {
		t.Fatal(err)
	}
	active, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active <= pending {
		t.Fatalf("active binding did not advance configuration revision: %d -> %d", pending, active)
	}
}
