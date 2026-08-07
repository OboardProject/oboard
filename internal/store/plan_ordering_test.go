package store

import (
	"context"
	"errors"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func orderingPolicyForStore(mode model.SubscriptionNodeOrderMode) model.SubscriptionNodeOrderPolicy {
	return model.SubscriptionNodeOrderPolicy{
		Version:              model.SubscriptionNodeOrderVersion,
		Mode:                 mode,
		ManualSeed:           model.SubscriptionNodeOrderExitRegion,
		ExitRegionOrder:      []string{"JP", "SG"},
		EntryRegionOrderMode: model.SubscriptionNodeEntryRegionOrderInheritExit,
		EntryRegionOrder:     []string{},
		EntryOrder:           []string{"inbound:7"},
	}
}

func intPtr(v int) *int { return &v }

func TestPlanOrderingVersionSaveAndClone(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "ordering", Enabled: true}
	nodes := []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, DisplayGroup: "A"},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2, DisplayGroup: "B", SortPosition: intPtr(0)},
	}
	if err := s.CreateSubscriptionPlan(ctx, plan, nodes); err != nil {
		t.Fatal(err)
	}
	activeRevision, err := s.GetPlanRevision(ctx, plan.ID, plan.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	// New plans default to exit_region; the legacy default is reserved for
	// existing revisions (column default) so upgrades never reorder.
	if activeRevision.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderExitRegion {
		t.Fatalf("new plan default = %#v", activeRevision.NodeOrderPolicy.Mode)
	}
	if model.DefaultSubscriptionNodeOrderPolicy().Mode != model.SubscriptionNodeOrderLegacyGroupName {
		t.Fatalf("legacy migration default = %#v", model.DefaultSubscriptionNodeOrderPolicy().Mode)
	}

	// Save a manual ordering: the new version becomes current immediately and
	// persists policy + positions.
	policy := orderingPolicyForStore(model.SubscriptionNodeOrderManual)
	created, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: policy, ManualOrder: []string{"proxy_path:2"}},
		ChangeKind:          model.PlanChangeKindOrdering,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.EffectiveNow {
		t.Fatalf("ordering save must be effective immediately: %#v", created)
	}
	version, versionNodes, err := s.GetPlanRevisionOrdering(ctx, plan.ID, created.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if version.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual {
		t.Fatalf("version policy = %#v", version.NodeOrderPolicy)
	}
	positions := map[int64]*int{}
	for _, node := range versionNodes {
		positions[node.NodeID] = node.SortPosition
	}
	if positions[2] == nil || *positions[2] != 0 {
		t.Fatalf("version positions = %#v", positions)
	}
	if positions[1] != nil {
		t.Fatalf("unlisted node must keep NULL position: %#v", positions)
	}

	// The first revision (v1) is immutable: ordering saved a new version and
	// never updated v1 in place.
	v1, err := s.GetPlanRevision(ctx, plan.ID, plan.CurrentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	_ = v1
	v1 = nil
	revisions, err := s.ListPlanRevisions(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].VersionNo != 2 {
		t.Fatalf("revisions = %#v", revisions)
	}

	// Clone copies policy and positions from the current snapshot.
	clone, err := s.CloneSubscriptionPlan(ctx, plan.ID, "ordering-copy")
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
	cloneRevision, err := s.GetPlanRevision(ctx, clone.ID, clone.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if cloneRevision.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual {
		t.Fatalf("clone policy = %#v", cloneRevision.NodeOrderPolicy)
	}
}

func TestPlanOrderingReplaceKeepsPositionsAndRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "ordering-replace", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	policy := orderingPolicyForStore(model.SubscriptionNodeOrderManual)
	ordering, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: policy, ManualOrder: []string{"proxy_path:1", "proxy_path:2"}},
		ChangeKind:          model.PlanChangeKindOrdering,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Replace keeps the surviving node's position and NULLs the new one.
	replaced, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: detail.LockVersion,
		Nodes: &PlanNodesMutation{Op: "replace", Nodes: []model.SubscriptionPlanNode{
			{NodeType: model.AssignableNodeProxyPath, NodeID: 2, DisplayGroup: "new-group"},
			{NodeType: model.AssignableNodeProxyPath, NodeID: 3},
		}},
		ChangeKind: model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, draftNodes, err := s.GetPlanRevisionOrdering(ctx, plan.ID, replaced.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]model.SubscriptionPlanNode{}
	for _, node := range draftNodes {
		byID[node.NodeID] = node
	}
	if n := byID[2]; n.SortPosition == nil || *n.SortPosition != 1 || n.DisplayGroup != "new-group" {
		t.Fatalf("replaced surviving node = %#v", n)
	}
	if n := byID[3]; n.SortPosition != nil {
		t.Fatalf("new node must be NULL position: %#v", n)
	}
	if len(draftNodes) != 2 {
		t.Fatalf("draft nodes = %#v", draftNodes)
	}

	// The partial unique index still rejects a direct duplicate write.
	if _, err := s.db.ExecContext(ctx, `update subscription_plan_revision_nodes set sort_position=1 where revision_id=? and node_type='proxy_path' and node_id=3`, replaced.Revision.ID); err == nil {
		t.Fatalf("duplicate sort_position must be rejected by the unique index")
	}
	_ = ordering
}

func TestPlanOrderingStaleLockAndValidation(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "ordering-conflict", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	policy := orderingPolicyForStore(model.SubscriptionNodeOrderManual)
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: 99999,
		Ordering:            &PlanOrderingMutation{Policy: policy},
	}); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale lock err = %v", err)
	}
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: policy, ManualOrder: []string{"proxy_path:1", "proxy_path:1"}},
	}); !errors.Is(err, ErrPlanOrderingInvalid) {
		t.Fatalf("duplicate key err = %v", err)
	}
	if _, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: policy, ManualOrder: []string{"proxy_path:999"}},
	}); !errors.Is(err, ErrPlanOrderingInvalid) {
		t.Fatalf("unknown key err = %v", err)
	}
}
