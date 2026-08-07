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

func TestPlanOrderingDraftCloneRestoreCopy(t *testing.T) {
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

	// Save a manual ordering into a draft: policy + positions are persisted.
	policy := orderingPolicyForStore(model.SubscriptionNodeOrderManual)
	draftID, err := s.UpdatePlanDraftOrdering(ctx, plan.ID, plan.Revision, policy, []string{"proxy_path:2"})
	if err != nil {
		t.Fatal(err)
	}
	draft, draftNodes, err := s.GetPlanRevisionOrdering(ctx, plan.ID, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual {
		t.Fatalf("draft policy = %#v", draft.NodeOrderPolicy)
	}
	positions := map[int64]*int{}
	for _, node := range draftNodes {
		positions[node.NodeID] = node.SortPosition
	}
	if positions[2] == nil || *positions[2] != 0 {
		t.Fatalf("draft positions = %#v", positions)
	}
	if positions[1] != nil {
		t.Fatalf("unlisted node must keep NULL position: %#v", positions)
	}

	// The active revision stays untouched.
	active, err := s.GetPlanRevision(ctx, plan.ID, plan.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if active.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderExitRegion {
		t.Fatalf("active policy changed: %#v", active.NodeOrderPolicy)
	}
	if active.NodeOrderPolicy.ExitRegionOrder != nil && len(active.NodeOrderPolicy.ExitRegionOrder) != 0 {
		t.Fatalf("active policy region order changed: %#v", active.NodeOrderPolicy)
	}

	// Publish the draft, then clone: the clone copies policy and positions
	// from the active snapshot.
	if _, err := s.PublishPlanRevisionGuarded(ctx, plan.ID, plan.ActiveRevisionID, draftID); err != nil {
		t.Fatal(err)
	}
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

	// Restore copies the draft back as a new draft with policy + positions.
	freshPlan, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredDraftID, err := s.RestorePlanRevision(ctx, plan.ID, draftID, freshPlan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	restored, restoredNodes, err := s.GetPlanRevisionOrdering(ctx, plan.ID, restoredDraftID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual {
		t.Fatalf("restored policy = %#v", restored.NodeOrderPolicy)
	}
	restoredPositions := map[int64]*int{}
	for _, node := range restoredNodes {
		restoredPositions[node.NodeID] = node.SortPosition
	}
	if restoredPositions[2] == nil || *restoredPositions[2] != 0 {
		t.Fatalf("restore did not copy positions: %#v", restoredPositions)
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
	if _, err := s.UpdatePlanDraftOrdering(ctx, plan.ID, plan.Revision, policy, []string{"proxy_path:1", "proxy_path:2"}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Replace keeps the surviving node's position and NULLs the new one.
	if err := s.SyncPlanDraftNodes(ctx, plan.ID, detail.Revision, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2, DisplayGroup: "new-group"},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 3},
	}, "replace"); err != nil {
		t.Fatal(err)
	}
	draft, draftNodes, err := s.GetPlanRevisionOrdering(ctx, plan.ID, detail.DraftRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual {
		t.Fatalf("replace dropped policy: %#v", draft.NodeOrderPolicy)
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

	// A fresh manual save after the replace rewrites positions cleanly.
	fresh, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePlanDraftOrdering(ctx, plan.ID, fresh.Revision, policy, []string{"proxy_path:2", "proxy_path:3"}); err != nil {
		t.Fatalf("post-replace manual save: %v", err)
	}
	// The partial unique index still rejects a direct duplicate write.
	if _, err := s.db.ExecContext(ctx, `update subscription_plan_revision_nodes set sort_position=0 where revision_id=? and node_type='proxy_path' and node_id=3`, fresh.DraftRevisionID); err == nil {
		t.Fatalf("duplicate sort_position must be rejected by the unique index")
	}
}

func TestPlanOrderingStaleRevisionAndValidation(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "ordering-conflict", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	policy := orderingPolicyForStore(model.SubscriptionNodeOrderManual)
	if _, err := s.UpdatePlanDraftOrdering(ctx, plan.ID, 99999, policy, nil); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale revision err = %v", err)
	}
	if _, err := s.UpdatePlanDraftOrdering(ctx, plan.ID, plan.Revision, policy, []string{"proxy_path:1", "proxy_path:1"}); !errors.Is(err, ErrPlanOrderingInvalid) {
		t.Fatalf("duplicate key err = %v", err)
	}
	if _, err := s.UpdatePlanDraftOrdering(ctx, plan.ID, plan.Revision, policy, []string{"proxy_path:999"}); !errors.Is(err, ErrPlanOrderingInvalid) {
		t.Fatalf("unknown key err = %v", err)
	}
}
