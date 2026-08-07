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

func TestSubscriptionPlanCRUDAndNodeVersions(t *testing.T) {
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

	// A node change creates an immutable version and sets the pending pointer
	// without touching the current snapshot.
	created, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes: &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{
			{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "HK2"},
			{NodeType: model.AssignableNodeExternalOutbound, NodeID: 7},
		}},
		ChangeKind:    model.PlanChangeKindNodes,
		ChangeSummary: "加入节点",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.NoChange || created.RequiresDeployment != true || created.EffectiveNow {
		t.Fatalf("node version result = %#v", created)
	}
	versionNodes, err := s.ListPlanRevisionNodes(ctx, created.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionNodes) != 3 {
		t.Fatalf("version nodes = %d, want 3: %#v", len(versionNodes), versionNodes)
	}
	foundUpsert := false
	for _, pn := range versionNodes {
		if pn.NodeType == model.AssignableNodeProxyPath && pn.NodeID == 1 {
			foundUpsert = true
			if pn.DisplayGroup != "HK2" {
				t.Fatalf("add must adopt the new display group: %#v", pn)
			}
		}
	}
	if !foundUpsert {
		t.Fatalf("added node missing: %#v", versionNodes)
	}
	activeNodes, _ = s.ListActivePlanNodes(ctx, plan.ID)
	if len(activeNodes) != 2 {
		t.Fatalf("current nodes changed while a version is pending: %#v", activeNodes)
	}
	plan, err = s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.LockVersion != 2 || plan.PendingRevisionID != created.Revision.ID || plan.CurrentRevisionID != created.Revision.BasedOnRevisionID {
		t.Fatalf("plan after node version = %#v", plan)
	}

	// A stale lock version conflicts instead of overwriting.
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: 1,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 9}}},
	}); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale lock err = %v, want ErrPlanRevisionConflict", err)
	}

	// A stale base revision conflicts.
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		BaseRevisionID:      plan.CurrentRevisionID,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 9}}},
	}); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale base err = %v, want ErrPlanRevisionConflict", err)
	}

	// A second save while a version is pending is rejected.
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 9}}},
	}); !errors.Is(err, ErrPlanVersionApplying) {
		t.Fatalf("concurrent save err = %v, want ErrPlanVersionApplying", err)
	}

	// Activation advances the current pointer and clears pending.
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, created.Revision.ID, 7); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.CurrentRevisionID != created.Revision.ID || plan.PendingRevisionID != 0 || plan.LockVersion != 3 {
		t.Fatalf("plan after activation = %#v", plan)
	}
	activeNodes, _ = s.ListActivePlanNodes(ctx, plan.ID)
	if len(activeNodes) != 3 {
		t.Fatalf("current nodes after activation = %d, want 3", len(activeNodes))
	}
	revisions, err := s.ListPlanRevisions(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].VersionNo != 2 || revisions[0].ActivationChangeID == nil || *revisions[0].ActivationChangeID != 7 {
		t.Fatalf("revisions = %#v", revisions)
	}

	// Remove and replace create further versions.
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "remove", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 2}}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	removedID := plan.PendingRevisionID
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, removedID, 8); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "replace", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: 42}}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	latest, err := s.GetPlanRevision(ctx, plan.ID, plan.LatestRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	latestNodes, _ := s.ListPlanRevisionNodes(ctx, latest.ID)
	if len(latestNodes) != 1 || latestNodes[0].NodeType != model.AssignableNodeInbound || latestNodes[0].NodeID != 42 {
		t.Fatalf("latest nodes after replace = %#v", latestNodes)
	}
	// The current snapshot still has the activated set (3 nodes from v2).
	activeNodes, _ = s.ListActivePlanNodes(ctx, plan.ID)
	if len(activeNodes) != 2 {
		t.Fatalf("current nodes after replace = %#v", activeNodes)
	}
	plansForNode, err := s.ListPlansForNode(ctx, string(model.AssignableNodeProxyPath), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plansForNode) != 1 || plansForNode[0].PlanID != plan.ID {
		t.Fatalf("plans for node = %#v", plansForNode)
	}

	// Restore creates a new version based on a historical revision.
	historical := revisions[0]
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, plan.PendingRevisionID, 8); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	restored, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "replace", Nodes: historicalNodesForTest(t, s, historical.ID)},
		ChangeKind:          model.PlanChangeKindRestore,
		ChangeSummary:       "基于历史版本恢复",
	})
	if err != nil {
		t.Fatal(err)
	}
	restoredNodes, err := s.ListPlanRevisionNodes(ctx, restored.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredNodes) != 3 {
		t.Fatalf("restored nodes = %#v", restoredNodes)
	}
	if restored.Revision.BasedOnRevisionID != plan.LatestRevisionID {
		t.Fatalf("restored based_on = %d, want latest %d", restored.Revision.BasedOnRevisionID, plan.LatestRevisionID)
	}

	// Re-adding an existing node in the same mutation is idempotent.
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, plan.PendingRevisionID, 8); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	same, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = same
	if err := s.DeleteSubscriptionPlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.ListActivePlanNodes(ctx, plan.ID)
	if len(nodes) != 0 {
		t.Fatalf("plan nodes must cascade-delete, got %#v", nodes)
	}
}

func historicalNodesForTest(t *testing.T, s *Store, revisionID int64) []model.SubscriptionPlanNode {
	t.Helper()
	nodes, err := s.ListPlanRevisionNodes(context.Background(), revisionID)
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func TestPlanVersionNoopSaveDoesNotCreateVersion(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "noop", Enabled: true}
	nodes := []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "A"}}
	if err := s.CreateSubscriptionPlan(ctx, plan, nodes); err != nil {
		t.Fatal(err)
	}
	result, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering: &PlanOrderingMutation{
			Policy:      model.NewSubscriptionNodeOrderPolicy(),
			ManualOrder: nil,
		},
		ChangeKind: model.PlanChangeKindOrdering,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoChange {
		t.Fatalf("identical ordering save must be NoChange: %#v", result)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.LatestRevisionID != plan.CurrentRevisionID || plan.LockVersion != 1 {
		t.Fatalf("noop save changed the plan: %#v", plan)
	}
	revisions, _ := s.ListPlanRevisions(ctx, plan.ID)
	if len(revisions) != 1 {
		t.Fatalf("noop save created a version: %#v", revisions)
	}
}

func TestPlanVersionOrderingAdvancesCurrentImmediately(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "ordering-immediate", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	policy := orderingPolicyForStore(model.SubscriptionNodeOrderManual)
	result, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: policy, ManualOrder: []string{"proxy_path:2"}},
		ChangeKind:          model.PlanChangeKindOrdering,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.EffectiveNow || result.RequiresDeployment || result.ChangeClass != "presentation_only" {
		t.Fatalf("ordering version result = %#v", result)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.CurrentRevisionID != result.Revision.ID || plan.LatestRevisionID != result.Revision.ID || plan.PendingRevisionID != 0 {
		t.Fatalf("plan after ordering save = %#v", plan)
	}
	_, versionNodes, err := s.GetPlanRevisionOrdering(ctx, plan.ID, result.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[int64]*int{}
	for _, node := range versionNodes {
		positions[node.NodeID] = node.SortPosition
	}
	if positions[2] == nil || *positions[2] != 0 {
		t.Fatalf("manual positions = %#v", positions)
	}
	if positions[1] != nil {
		t.Fatalf("unlisted node must keep NULL position: %#v", positions)
	}
	// Auto-mode save after manual keeps the manual positions (spec 4.4).
	auto, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: orderingPolicyForStore(model.SubscriptionNodeOrderExitRegion)},
		ChangeKind:          model.PlanChangeKindOrdering,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, autoNodes, _ := s.GetPlanRevisionOrdering(ctx, plan.ID, auto.Revision.ID)
	kept := 0
	for _, node := range autoNodes {
		if node.SortPosition != nil {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("auto-mode save kept positions = %d, want 1", kept)
	}
}

func TestPlanVersionCloneCopiesLatestSnapshot(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "clone-source", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "A"},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2, DisplayGroup: "B"},
	}); err != nil {
		t.Fatal(err)
	}
	policy := orderingPolicyForStore(model.SubscriptionNodeOrderManual)
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: policy, ManualOrder: []string{"proxy_path:2"}},
		ChangeKind:          model.PlanChangeKindOrdering,
	}); err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneSubscriptionPlan(ctx, plan.ID, "clone-copy")
	if err != nil {
		t.Fatal(err)
	}
	cloneNodes, err := s.ListActivePlanNodes(ctx, clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cloneNodes) != 2 {
		t.Fatalf("clone nodes = %d", len(cloneNodes))
	}
	for _, node := range cloneNodes {
		if node.NodeID == 2 && (node.SortPosition == nil || *node.SortPosition != 0) {
			t.Fatalf("clone did not copy sort_position: %#v", node)
		}
	}
	cloneRevision, err := s.GetPlanRevision(ctx, clone.ID, clone.CurrentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if cloneRevision.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual {
		t.Fatalf("clone policy = %#v", cloneRevision.NodeOrderPolicy)
	}
}

func TestPlanVersionActivationConflictKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "activation-conflict", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 1}}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	currentBefore := plan.CurrentRevisionID
	// Wrong current or wrong pending both conflict and keep the plan intact.
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID+1, created.Revision.ID, 1); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale current err = %v", err)
	}
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, 99999, 1); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale pending err = %v", err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.CurrentRevisionID != currentBefore || plan.PendingRevisionID == 0 {
		t.Fatalf("conflicting activation mutated plan: %#v", plan)
	}
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, created.Revision.ID, 9); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.CurrentRevisionID != created.Revision.ID || plan.PendingRevisionID != 0 {
		t.Fatalf("plan after successful activation = %#v", plan)
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
	// Limit changes create a pending version; the current limits stay until
	// activation.
	speed := 200
	traffic := int64(2 << 30)
	mode := "month_day"
	day := 15
	created, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: got.LockVersion,
		Settings:            &PlanSettingsMutation{SpeedLimitMbps: &speed, TrafficLimitBytes: &traffic, TrafficResetMode: &mode, TrafficResetDay: &day},
		ChangeKind:          model.PlanChangeKindSettings,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ChangeClass != "authorization" || !created.RequiresDeployment || created.EffectiveNow {
		t.Fatalf("limits version = %#v", created)
	}
	got, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if got.SpeedLimitMbps != 50 || got.PendingRevisionID != created.Revision.ID {
		t.Fatalf("current limits changed while pending: %#v", got)
	}
	versionRev, err := s.GetPlanRevision(ctx, plan.ID, created.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if versionRev.SpeedLimitMbps != 200 || versionRev.TrafficResetDay != 15 {
		t.Fatalf("version revision = %#v", versionRev)
	}
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, got.CurrentRevisionID, created.Revision.ID, 3); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if got.SpeedLimitMbps != 200 || got.TrafficResetMode != "month_day" {
		t.Fatalf("plan limits after activation = %#v", got)
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
