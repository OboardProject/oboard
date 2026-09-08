package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAccessDeadlineNextDueIncludesBindingAndTrafficPeriod(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "deadlines.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if due, err := db.AccessDeadlineNextDue(ctx, now); err != nil || due != nil {
		t.Fatalf("empty due=%v err=%v", due, err)
	}

	user := &model.User{Username: "deadline-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "deadline-plan", Enabled: true}
	if err := db.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	starts := now.Add(5 * time.Minute)
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: plan.ID, Enabled: true, StartsAt: &starts}}); err != nil {
		t.Fatal(err)
	}
	due, err := db.AccessDeadlineNextDue(ctx, now)
	if err != nil || due == nil || !due.Equal(starts) {
		t.Fatalf("binding due=%v err=%v want %v", due, err, starts)
	}

	ends := now.Add(2 * time.Minute)
	if _, err := db.EnsureTrafficPeriod(ctx, user.ID, "2026-09", now.Add(-time.Hour), ends, 100); err != nil {
		t.Fatal(err)
	}
	due, err = db.AccessDeadlineNextDue(ctx, now)
	if err != nil || due == nil || !due.Equal(ends) {
		t.Fatalf("traffic due=%v err=%v want %v", due, err, ends)
	}
	near, err := db.HasTrafficPeriodEndingNear(ctx, ends, time.Second)
	if err != nil || !near {
		t.Fatalf("near=%v err=%v", near, err)
	}
}

func TestMarkEvaluatedStaleClearsRoutingWatermark(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "stale.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	server := &model.Server{Name: "n1", AgentID: "agent-1", AgentTokenHash: "hash", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 7, "digest", []string{"k1"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, 1, 1, "digest", "boot"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 7, "users", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserConfirmation(ctx, server.ID, 1, "users", "boot"); err != nil {
		t.Fatal(err)
	}

	auth, err := db.AuthorizationState(ctx, server.ID)
	if err != nil || auth.EvaluatedRoutingRevision != 7 || !auth.Confirmed() {
		t.Fatalf("auth before=%#v err=%v", auth, err)
	}
	users, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil || users.EvaluatedRoutingRevision != 7 || !users.Confirmed() {
		t.Fatalf("users before=%#v err=%v", users, err)
	}
	staleAuth, err := db.StaleAuthorizationServerIDs(ctx, 7)
	if err != nil || len(staleAuth) != 0 {
		t.Fatalf("auth before stale=%v err=%v", staleAuth, err)
	}
	staleUsers, err := db.StaleRuntimeUserServerIDs(ctx, 7)
	if err != nil || len(staleUsers) != 0 {
		t.Fatalf("users before stale=%v err=%v", staleUsers, err)
	}

	if err := db.MarkAuthorizationEvaluatedStale(ctx, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRuntimeUsersEvaluatedStale(ctx, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	auth, _ = db.AuthorizationState(ctx, server.ID)
	users, _ = db.RuntimeUserState(ctx, server.ID)
	if auth.EvaluatedRoutingRevision != 0 || users.EvaluatedRoutingRevision != 0 {
		t.Fatalf("evaluated auth=%d users=%d", auth.EvaluatedRoutingRevision, users.EvaluatedRoutingRevision)
	}
	staleAuth, err = db.StaleAuthorizationServerIDs(ctx, 7)
	if err != nil || len(staleAuth) != 1 || staleAuth[0] != server.ID {
		t.Fatalf("auth after stale=%v err=%v", staleAuth, err)
	}
	staleUsers, err = db.StaleRuntimeUserServerIDs(ctx, 7)
	if err != nil || len(staleUsers) != 1 || staleUsers[0] != server.ID {
		t.Fatalf("users after stale=%v err=%v", staleUsers, err)
	}
}
