package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func openPlansTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func createPlanTestUser(t *testing.T, s *Store, id int64, username string) {
	t.Helper()
	if err := s.CreateUser(context.Background(), &model.User{ID: id, Username: username, Status: "active", Role: model.RoleViewer}); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionPlanCRUDAndNodeSync(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)

	plan := &model.SubscriptionPlan{Name: "premium", Description: "高级版", Enabled: true, SpeedLimitMbps: 100, TrafficResetMode: "monthly"}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "HK"},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if plan.ID == 0 || plan.ActiveRevisionID == 0 {
		t.Fatalf("plan id/revision not assigned: %#v", plan)
	}
	got, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "premium" || got.SpeedLimitMbps != 100 {
		t.Fatalf("plan = %#v", got)
	}
	activeNodes, err := s.ListActivePlanNodes(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(activeNodes) != 2 {
		t.Fatalf("active nodes = %d, want 2: %#v", len(activeNodes), activeNodes)
	}

	// Draft edits must not touch the active snapshot: adding a node creates a
	// draft revision copied from active, leaving the active set frozen.
	if err := s.SyncPlanDraftNodes(ctx, plan.ID, plan.Revision, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "HK2"},
		{NodeType: model.AssignableNodeExternalOutbound, NodeID: 7},
	}, "add"); err != nil {
		t.Fatal(err)
	}
	draftNodes, err := s.ListDraftPlanNodes(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(draftNodes) != 3 {
		t.Fatalf("draft nodes = %d, want 3: %#v", len(draftNodes), draftNodes)
	}
	foundUpsert := false
	for _, pn := range draftNodes {
		if pn.NodeType == model.AssignableNodeProxyPath && pn.NodeID == 1 {
			foundUpsert = true
			if pn.DisplayGroup != "HK2" {
				t.Fatalf("upsert must adopt the new display group: %#v", pn)
			}
		}
	}
	if !foundUpsert {
		t.Fatalf("upserted node missing: %#v", draftNodes)
	}
	activeNodes, _ = s.ListActivePlanNodes(ctx, plan.ID)
	if len(activeNodes) != 2 {
		t.Fatalf("active nodes changed while editing draft: %#v", activeNodes)
	}
	plan, err = s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 2 || plan.DraftRevisionID == 0 {
		t.Fatalf("revision = %d draft_id = %d, want 2/draft", plan.Revision, plan.DraftRevisionID)
	}

	// A stale expected revision must conflict instead of overwriting.
	if err := s.SyncPlanDraftNodes(ctx, plan.ID, plan.Revision-1, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 9}}, "add"); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale sync err = %v, want ErrPlanRevisionConflict", err)
	}

	// Publish activates the draft and clears it.
	if _, err := s.PublishPlanRevision(ctx, plan.ID, plan.Revision); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.ActiveRevisionID == 0 || plan.DraftRevisionID != 0 {
		t.Fatalf("plan after publish = %#v", plan)
	}
	activeNodes, _ = s.ListActivePlanNodes(ctx, plan.ID)
	if len(activeNodes) != 3 {
		t.Fatalf("active nodes after publish = %d, want 3", len(activeNodes))
	}
	revisions, err := s.ListPlanRevisions(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Status != model.PlanRevisionActive || revisions[1].Status != model.PlanRevisionArchived {
		t.Fatalf("revisions = %#v", revisions)
	}

	// Remove and replace operate on the draft.
	if err := s.SyncPlanDraftNodes(ctx, plan.ID, plan.Revision, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 2}}, "remove"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncPlanDraftNodes(ctx, plan.ID, 0, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: 42, Enabled: true}}, "replace"); err != nil {
		t.Fatal(err)
	}
	draftNodes, _ = s.ListDraftPlanNodes(ctx, plan.ID)
	if len(draftNodes) != 1 || draftNodes[0].NodeType != model.AssignableNodeInbound || draftNodes[0].NodeID != 42 {
		t.Fatalf("draft nodes after replace = %#v", draftNodes)
	}
	// The active snapshot still has the published set.
	activeNodes, _ = s.ListActivePlanNodes(ctx, plan.ID)
	if len(activeNodes) != 3 {
		t.Fatalf("active nodes after draft replace = %#v", activeNodes)
	}
	plansForNode, err := s.ListPlansForNode(ctx, string(model.AssignableNodeProxyPath), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plansForNode) != 1 || plansForNode[0].PlanID != plan.ID {
		t.Fatalf("plans for node = %#v", plansForNode)
	}

	// Restore a historical revision into the draft.
	active := revisions[0]
	restored, err := s.RestorePlanRevision(ctx, plan.ID, active.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	restoredNodes, err := s.ListPlanRevisionNodes(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredNodes) != 3 {
		t.Fatalf("restored nodes = %#v", restoredNodes)
	}

	// Re-adding an existing draft node is an idempotent upsert, never a duplicate.
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if err := s.SyncPlanDraftNodes(ctx, plan.ID, plan.Revision, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 1}}, "add"); err != nil {
		t.Fatal(err)
	}
	draftNodes, _ = s.ListDraftPlanNodes(ctx, plan.ID)
	if len(draftNodes) != 3 {
		t.Fatalf("upsert created duplicate rows: %#v", draftNodes)
	}
	if err := s.DeleteSubscriptionPlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.ListActivePlanNodes(ctx, plan.ID)
	if len(nodes) != 0 {
		t.Fatalf("plan nodes must cascade-delete, got %#v", nodes)
	}
}

func TestPlanDraftLimits(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "limits", Enabled: true, SpeedLimitMbps: 50, TrafficLimitBytes: 1 << 30, TrafficResetMode: "monthly", TrafficResetDay: 1}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSubscriptionPlan(ctx, plan.ID)
	if got.SpeedLimitMbps != 50 || got.TrafficLimitBytes != 1<<30 {
		t.Fatalf("initial limits = %#v", got)
	}
	// Draft limits must not affect the active plan limits.
	draftID, err := s.UpdatePlanDraftLimits(ctx, plan.ID, got.Revision, 200, 2<<30, "month_day", 15)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if got.SpeedLimitMbps != 50 || got.DraftRevisionID == 0 {
		t.Fatalf("active limits changed by draft edit: %#v", got)
	}
	draftRev, err := s.GetPlanRevision(ctx, plan.ID, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draftRev.SpeedLimitMbps != 200 || draftRev.TrafficResetDay != 15 {
		t.Fatalf("draft revision = %#v", draftRev)
	}
	if _, err := s.PublishPlanRevision(ctx, plan.ID, got.Revision); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if got.SpeedLimitMbps != 200 || got.TrafficResetMode != "month_day" {
		t.Fatalf("plan limits after publish = %#v", got)
	}
}

func TestUserPlanBindingOneActivePerUser(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	createPlanTestUser(t, s, 1, "alice")
	createPlanTestUser(t, s, 2, "bob")
	p1 := &model.SubscriptionPlan{Name: "p1", Enabled: true}
	p2 := &model.SubscriptionPlan{Name: "p2", Enabled: true}
	p3 := &model.SubscriptionPlan{Name: "p3", Enabled: true}
	for _, p := range []*model.SubscriptionPlan{p1, p2, p3} {
		if err := s.CreateSubscriptionPlan(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{
		{UserID: 1, PlanID: p1.ID},
		{UserID: 2, PlanID: p1.ID},
	}); err != nil {
		t.Fatal(err)
	}
	// Switching alice to p2 must disable the p1 binding, not create a second
	// active one.
	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: 1, PlanID: p2.ID}}); err != nil {
		t.Fatal(err)
	}
	binding, err := s.GetActiveUserPlanBinding(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if binding.PlanID != p2.ID {
		t.Fatalf("alice plan = %d, want %d", binding.PlanID, p2.ID)
	}
	all, err := s.ListActiveUserPlanBindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("active bindings = %d, want 2: %#v", len(all), all)
	}
	members, err := s.ListUserPlanBindingsForPlan(ctx, p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != 2 {
		t.Fatalf("p1 members = %#v", members)
	}

	// Time-effective bindings: a future start and an expired binding are not
	// effective at now.
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)
	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: 1, PlanID: p3.ID, StartsAt: &future}}); err != nil {
		t.Fatal(err)
	}
	effective, err := s.ListEffectiveUserPlanBindings(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 1 || effective[0].UserID != 2 {
		t.Fatalf("effective bindings = %#v", effective)
	}
	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: 1, PlanID: p1.ID, ExpiresAt: &past}}); err != nil {
		t.Fatal(err)
	}
	effective, _ = s.ListEffectiveUserPlanBindings(ctx, time.Now())
	if len(effective) != 1 {
		t.Fatalf("effective bindings after expired = %#v", effective)
	}

	// Removing the plan binding (PlanID 0) must leave no active binding.
	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: 2, PlanID: 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetActiveUserPlanBinding(ctx, 2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected no active binding, got err=%v", err)
	}

	// Deleting a plan cascades its bindings.
	if err := s.DeleteSubscriptionPlan(ctx, p1.ID); err != nil {
		t.Fatal(err)
	}
	members, _ = s.ListUserPlanBindingsForPlan(ctx, p1.ID)
	if len(members) != 0 {
		t.Fatalf("bindings must cascade-delete with the plan")
	}
}

func TestUserNodeExceptionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	createPlanTestUser(t, s, 1, "alice")

	now := time.Now()
	ex := &model.UserNodeException{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 5, Effect: model.UserNodeExceptionAllow, Reason: "临时试用", ExpiresAt: now.Add(24 * time.Hour)}
	if err := s.CreateUserNodeException(ctx, ex); err != nil {
		t.Fatal(err)
	}
	if ex.ID == 0 {
		t.Fatal("exception id not assigned")
	}
	items, err := s.ListUserNodeExceptionsForUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("exceptions = %d, want 1", len(items))
	}
	items, err = s.ListUserNodeExceptionsForNode(ctx, string(model.AssignableNodeProxyPath), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("node exceptions = %d, want 1", len(items))
	}
	ex.Effect = model.UserNodeExceptionDeny
	ex.Reason = "改用拒绝"
	if err := s.UpdateUserNodeException(ctx, ex); err != nil {
		t.Fatal(err)
	}
	items, _ = s.ListUserNodeExceptionsForUser(ctx, 1)
	if items[0].Effect != model.UserNodeExceptionDeny {
		t.Fatalf("updated exception = %#v", items[0])
	}

	expired := &model.UserNodeException{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 6, Effect: model.UserNodeExceptionAllow, Reason: "过期", ExpiresAt: now.Add(-time.Hour)}
	if err := s.CreateUserNodeException(ctx, expired); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteExpiredUserNodeExceptions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expired deleted = %d, want 1", deleted)
	}
	if err := s.DeleteUserNodeException(ctx, ex.ID); err != nil {
		t.Fatal(err)
	}
	items, _ = s.ListUserNodeExceptionsForUser(ctx, 1)
	if len(items) != 0 {
		t.Fatalf("exceptions after delete = %d, want 0", len(items))
	}
}
