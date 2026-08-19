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
