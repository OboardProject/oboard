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
	if err := s.CreateSubscriptionPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if plan.ID == 0 {
		t.Fatal("plan id not assigned")
	}
	got, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "premium" || got.SpeedLimitMbps != 100 {
		t.Fatalf("plan = %#v", got)
	}

	if err := s.AddPlanNodes(ctx, plan.ID, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "HK"},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	// Upsert the same node with a new group and add a third.
	if err := s.AddPlanNodes(ctx, plan.ID, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "HK2"},
		{NodeType: model.AssignableNodeExternalOutbound, NodeID: 7},
	}); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListPlanNodes(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("nodes = %d, want 3: %#v", len(nodes), nodes)
	}
	foundUpsert := false
	for _, pn := range nodes {
		if pn.NodeType == model.AssignableNodeProxyPath && pn.NodeID == 1 {
			foundUpsert = true
			if pn.DisplayGroup != "HK2" {
				t.Fatalf("upsert must adopt the new display group: %#v", pn)
			}
		}
	}
	if !foundUpsert {
		t.Fatalf("upserted node missing: %#v", nodes)
	}
	plan, err = s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 2 || plan.DraftRevision != 2 {
		t.Fatalf("revision = %d draft = %d, want 2/2", plan.Revision, plan.DraftRevision)
	}
	if err := s.PublishPlanRevision(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.ActiveRevision != 2 {
		t.Fatalf("active revision = %d, want 2", plan.ActiveRevision)
	}

	if err := s.RemovePlanNodes(ctx, plan.ID, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 2}}); err != nil {
		t.Fatal(err)
	}
	nodes, _ = s.ListPlanNodes(ctx, plan.ID)
	if len(nodes) != 2 {
		t.Fatalf("nodes after remove = %d, want 2", len(nodes))
	}

	if err := s.ReplacePlanNodes(ctx, plan.ID, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: 42, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	nodes, _ = s.ListPlanNodes(ctx, plan.ID)
	if len(nodes) != 1 || nodes[0].NodeType != model.AssignableNodeInbound || nodes[0].NodeID != 42 {
		t.Fatalf("nodes after replace = %#v", nodes)
	}
	plansForNode, err := s.ListPlansForNode(ctx, string(model.AssignableNodeInbound), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(plansForNode) != 1 || plansForNode[0].PlanID != plan.ID {
		t.Fatalf("plans for node = %#v", plansForNode)
	}

	// Duplicate node rows are rejected by the unique index.
	if err := s.AddPlanNodes(ctx, plan.ID, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: 42}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubscriptionPlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	nodes, _ = s.ListPlanNodes(ctx, plan.ID)
	if len(nodes) != 0 {
		t.Fatalf("plan nodes must cascade-delete, got %#v", nodes)
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
		if err := s.CreateSubscriptionPlan(ctx, p); err != nil {
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

	// Removing the plan binding (PlanID 0) must leave no active binding.
	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: 1, PlanID: 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetActiveUserPlanBinding(ctx, 1); !errors.Is(err, sql.ErrNoRows) {
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
