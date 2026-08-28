package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, created.Revision.ID, 7, nil); err != nil {
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
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, removedID, 8, nil); err != nil {
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
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, plan.PendingRevisionID, 8, nil); err != nil {
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
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, plan.PendingRevisionID, 8, nil); err != nil {
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

func TestFailedPendingPlanVersionIsSupersededByNextSave(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "replace-failed", Enabled: true, TrafficResetMode: "monthly"}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		BaseRevisionID:      plan.LatestRevisionID,
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "remove", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 1}}},
		ChangeKind:          model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	change := &model.AccessChange{
		ChangeType:               model.AccessChangePlanPublish,
		SourcePlanID:             plan.ID,
		CandidateRevisionID:      first.Revision.ID,
		ExpectedActiveRevisionID: plan.CurrentRevisionID,
		Status:                   model.AccessChangePreparing,
		PayloadJSON:              `{}`,
		PrepareProjectionJSON:    `{}`,
		FinalizeProjectionJSON:   `{}`,
	}
	if _, err := s.CreateAccessChange(ctx, change, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccessChangeStatus(ctx, change.ID, []model.AccessChangeStatus{model.AccessChangePreparing}, model.AccessChangeFailed, "server unavailable"); err != nil {
		t.Fatal(err)
	}
	failedPlan, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}

	second, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		BaseRevisionID:      failedPlan.LatestRevisionID,
		ExpectedLockVersion: failedPlan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "remove", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 2}}},
		ChangeKind:          model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatalf("new save after failed deployment: %v", err)
	}
	if second.NoChange || second.Revision.ID == first.Revision.ID {
		t.Fatalf("new save did not create replacement version: first=%#v second=%#v", first, second)
	}
	oldChange, err := s.GetAccessChange(ctx, change.ID)
	if err != nil || oldChange.Status != model.AccessChangeCancelled || oldChange.Error != "server unavailable" {
		t.Fatalf("superseded access change = %#v, err=%v", oldChange, err)
	}
	updated, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil || updated.PendingRevisionID != second.Revision.ID || updated.LatestRevisionID != second.Revision.ID {
		t.Fatalf("replacement pending version = %#v, err=%v", updated, err)
	}
}

func TestSavingSameFailedPlanVersionQueuesFreshDeployment(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "retry-failed", Enabled: true, TrafficResetMode: "monthly"}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 1}}); err != nil {
		t.Fatal(err)
	}
	first, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		BaseRevisionID:      plan.LatestRevisionID,
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 2}}},
		ChangeKind:          model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	change := &model.AccessChange{
		ChangeType: model.AccessChangePlanPublish, SourcePlanID: plan.ID, CandidateRevisionID: first.Revision.ID,
		ExpectedActiveRevisionID: plan.CurrentRevisionID, Status: model.AccessChangePreparing,
		PayloadJSON: `{}`, PrepareProjectionJSON: `{}`, FinalizeProjectionJSON: `{}`,
	}
	if _, err := s.CreateAccessChange(ctx, change, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccessChangeStatus(ctx, change.ID, []model.AccessChangeStatus{model.AccessChangePreparing}, model.AccessChangeFailed, "link failed"); err != nil {
		t.Fatal(err)
	}
	failedPlan, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListPlanRevisionNodes(ctx, first.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}

	retry, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		BaseRevisionID:      failedPlan.LatestRevisionID,
		ExpectedLockVersion: failedPlan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "replace", Nodes: nodes},
		ChangeKind:          model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.NoChange || !retry.RequiresDeployment || retry.Revision.ID != first.Revision.ID || retry.PendingRevisionID != first.Revision.ID {
		t.Fatalf("same desired state was not prepared for a fresh deployment: %#v", retry)
	}
	oldChange, err := s.GetAccessChange(ctx, change.ID)
	if err != nil || oldChange.Status != model.AccessChangeCancelled {
		t.Fatalf("old failure was not superseded: change=%#v err=%v", oldChange, err)
	}
}

func TestRemoveAssignableNodeFromPlansCreatesImmutableCleanupVersion(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plans := make([]*model.SubscriptionPlan, 0, 2)
	for _, name := range []string{"管理员", "premium"} {
		plan := &model.SubscriptionPlan{Name: name, Enabled: true}
		if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
			{NodeType: model.AssignableNodeProxyPath, NodeID: 41, DisplayGroup: "HK"},
			{NodeType: model.AssignableNodeProxyPath, NodeID: 42},
		}); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	oldRevision := plans[0].CurrentRevisionID
	refs, err := s.RemoveAssignableNodeFromPlans(ctx, model.AssignableNodeProxyPath, 41)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.Active) != 2 || len(refs.Draft) != 0 || len(refs.Pending) != 0 {
		t.Fatalf("references = %#v", refs)
	}
	oldNodes, err := s.ListPlanRevisionNodes(ctx, oldRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldNodes) != 2 {
		t.Fatalf("published history was modified: %#v", oldNodes)
	}
	for _, plan := range plans {
		updated, err := s.GetSubscriptionPlan(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.CurrentRevisionID == plan.CurrentRevisionID || updated.PendingRevisionID != 0 {
			t.Fatalf("cleanup did not activate a new current revision: %#v", updated)
		}
		nodes, err := s.ListActivePlanNodes(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 || nodes[0].NodeID != 42 {
			t.Fatalf("active cleanup nodes = %#v", nodes)
		}
	}
}

func TestRemoveAssignableNodeFromPlansRejectsPendingPlanWithoutMutation(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "pending-cleanup", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 51}}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 52}}},
		ChangeKind:          model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveAssignableNodeFromPlans(ctx, model.AssignableNodeProxyPath, 51); !errors.Is(err, ErrPlanVersionApplying) {
		t.Fatalf("pending cleanup err = %v, want ErrPlanVersionApplying", err)
	}
	after, err := s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentRevisionID != before.CurrentRevisionID || after.PendingRevisionID != created.Revision.ID {
		t.Fatalf("pending plan mutated after rejected cleanup: before=%#v after=%#v", before, after)
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

func TestPlanVersionCopiesMembershipPolicyAndOrderingSource(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "policy-source", Enabled: true}
	nodes := []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, SourceType: model.PlanNodeSourceRule, SourceRuleID: 1},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2, SourceType: model.PlanNodeSourceExplicit},
	}
	if err := s.CreateSubscriptionPlan(ctx, plan, nodes); err != nil {
		t.Fatal(err)
	}
	rules := []model.PlanMembershipRule{{RuleID: 1, Kind: "exit_region", ScopeKey: "JP"}}
	exclusions := []model.PlanNodeExclusion{{NodeType: model.AssignableNodeProxyPath, NodeID: 3}}
	policyResult, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		BaseRevisionID: plan.LatestRevisionID, ExpectedLockVersion: plan.LockVersion,
		MembershipPolicy: &PlanMembershipPolicyMutation{Rules: rules, Exclusions: exclusions, Nodes: nodes},
		ChangeKind:       model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policyResult.RequiresDeployment {
		t.Fatal("rule-only snapshot with unchanged membership must not deploy")
	}
	sourcePlanID, sourceRevisionID := plan.ID, policyResult.Revision.ID
	orderingResult, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		BaseRevisionID: policyResult.Revision.ID, ExpectedLockVersion: policyResult.LockVersion,
		Ordering:   &PlanOrderingMutation{Policy: model.NewSubscriptionNodeOrderPolicy(), SetSourceProvenance: true, OrderSourcePlanID: &sourcePlanID, OrderSourceRevisionID: &sourceRevisionID, OrderSourceMode: "copy_rules_rebuild"},
		ChangeKind: model.PlanChangeKindOrdering,
	})
	if err != nil {
		t.Fatal(err)
	}
	if orderingResult.Revision.OrderSourcePlanID == nil || *orderingResult.Revision.OrderSourcePlanID != plan.ID || orderingResult.Revision.OrderSourceMode != "copy_rules_rebuild" {
		t.Fatalf("ordering provenance = %#v", orderingResult.Revision)
	}
	clone, err := s.CloneSubscriptionPlan(ctx, plan.ID, "policy-clone")
	if err != nil {
		t.Fatal(err)
	}
	cloneRevision, err := s.GetPlanRevision(ctx, clone.ID, clone.CurrentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if cloneRevision.OrderSourceRevisionID == nil || *cloneRevision.OrderSourceRevisionID != sourceRevisionID {
		t.Fatalf("clone provenance = %#v", cloneRevision)
	}
	cloneRules, cloneExclusions, err := s.ListPlanRevisionMembershipPolicy(ctx, clone.CurrentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cloneRules) != 1 || cloneRules[0].ScopeKey != "JP" || len(cloneExclusions) != 1 || cloneExclusions[0].NodeID != 3 {
		t.Fatalf("clone membership snapshot rules=%#v exclusions=%#v", cloneRules, cloneExclusions)
	}
}

func TestPlanVersionAppliesOrderingToResolvedMembership(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "rule-ordering", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, SourceType: model.PlanNodeSourceExplicit},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2, SourceType: model.PlanNodeSourceRule, SourceRuleID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, SourceType: model.PlanNodeSourceExplicit},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2, SourceType: model.PlanNodeSourceRule, SourceRuleID: 1},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 3, SourceType: model.PlanNodeSourceRule, SourceRuleID: 1},
	}
	result, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		BaseRevisionID: plan.LatestRevisionID, ExpectedLockVersion: plan.LockVersion,
		MembershipPolicy: &PlanMembershipPolicyMutation{
			Rules: []model.PlanMembershipRule{{RuleID: 1, Kind: "exit_region", ScopeKey: "JP"}},
			Nodes: desired,
		},
		Ordering: &PlanOrderingMutation{
			Policy:      orderingPolicyForStore(model.SubscriptionNodeOrderManual),
			ManualOrder: []string{"proxy_path:2", "proxy_path:3", "proxy_path:1"},
		},
		ChangeKind: model.PlanChangeKindNodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListPlanRevisionNodes(ctx, result.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[int64]int{}
	for _, node := range nodes {
		if node.SortPosition == nil {
			t.Fatalf("node %d has no manual position: %#v", node.NodeID, nodes)
		}
		positions[node.NodeID] = *node.SortPosition
	}
	if positions[2] != 0 || positions[3] != 1 || positions[1] != 2 {
		t.Fatalf("manual positions = %#v", positions)
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
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID+1, created.Revision.ID, 1, nil); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale current err = %v", err)
	}
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, 99999, 1, nil); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale pending err = %v", err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.CurrentRevisionID != currentBefore || plan.PendingRevisionID == 0 {
		t.Fatalf("conflicting activation mutated plan: %#v", plan)
	}
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, created.Revision.ID, 9, nil); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if plan.CurrentRevisionID != created.Revision.ID || plan.PendingRevisionID != 0 {
		t.Fatalf("plan after successful activation = %#v", plan)
	}
}

func TestPlanVersionActivationWaitsForConcurrentWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := openPlansTestStore(t)
	plan := &model.SubscriptionPlan{Name: "activation-writer", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Nodes:               &PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}

	writer, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `begin immediate`); err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = writer.ExecContext(context.Background(), `rollback`)
		}
	}()
	if _, err := writer.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values('activation-writer','held',?)`, now()); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, created.Revision.ID, 9, nil)
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("activation returned before the concurrent writer committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := writer.ExecContext(ctx, `commit`); err != nil {
		t.Fatal(err)
	}
	committed = true
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	plan, err = s.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CurrentRevisionID != created.Revision.ID || plan.PendingRevisionID != 0 {
		t.Fatalf("plan after queued activation = %#v", plan)
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
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, got.CurrentRevisionID, created.Revision.ID, 3, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSubscriptionPlan(ctx, plan.ID)
	if got.SpeedLimitMbps != 200 || got.TrafficResetMode != "month_day" {
		t.Fatalf("plan limits after activation = %#v", got)
	}
}

func TestPlanActivationMergesTrafficIntoExistingTargetPeriod(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	createPlanTestUser(t, s, 1, "alice")
	plan := &model.SubscriptionPlan{Name: "period-migration", Enabled: true, TrafficLimitBytes: 1000, TrafficResetMode: "monthly", TrafficResetDay: 1}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	mode := "anniversary_month"
	created, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Settings:            &PlanSettingsMutation{TrafficResetMode: &mode},
		ChangeKind:          model.PlanChangeKindSettings,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `insert into traffic_periods(user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at) values(1,'old',?,?,100,50,1000,'active',?),(1,'new',?,?,20,30,1000,'active',?)`, ts.Add(-time.Hour).Format(time.RFC3339Nano), ts.Add(time.Hour).Format(time.RFC3339Nano), now(), ts.Add(-time.Hour).Format(time.RFC3339Nano), ts.Add(time.Hour).Format(time.RFC3339Nano), now()); err != nil {
		t.Fatal(err)
	}
	migration := TrafficPeriodMigration{UserID: 1, SourcePeriodKey: "old", TargetPeriodKey: "new", TargetStart: ts.Add(-time.Hour), TargetEnd: ts.Add(time.Hour), TrafficLimit: 180}
	if err := s.ActivatePlanVersionGuarded(ctx, plan.ID, plan.CurrentRevisionID, created.Revision.ID, 3, []TrafficPeriodMigration{migration}); err != nil {
		t.Fatal(err)
	}
	period, err := s.GetTrafficPeriod(ctx, 1, "new")
	if err != nil {
		t.Fatal(err)
	}
	if period.Upload != 120 || period.Download != 80 || period.State != "quota_exceeded" {
		t.Fatalf("merged period = %#v", period)
	}
	user, err := s.GetUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if user.TrafficUsedBytes != 200 {
		t.Fatalf("traffic_used_bytes = %d, want 200", user.TrafficUsedBytes)
	}
	source, err := s.GetTrafficPeriod(ctx, 1, "old")
	if err != nil || source.Upload != 0 || source.Download != 0 {
		t.Fatalf("source period was not moved: %#v err=%v", source, err)
	}
	resolved, changed, err := s.ResolveTrafficPeriodKey(ctx, 1, "old")
	if err != nil || !changed || resolved != "new" {
		t.Fatalf("resolved period = %q changed=%v err=%v", resolved, changed, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTrafficPeriodTx(ctx, tx, TrafficPeriodMigration{UserID: 1, SourcePeriodKey: "new", TargetPeriodKey: "old", TargetStart: ts.Add(-time.Hour), TargetEnd: ts.Add(time.Hour), TrafficLimit: 180}, now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	resolved, changed, err = s.ResolveTrafficPeriodKey(ctx, 1, "old")
	if err != nil || !changed || !strings.Contains(resolved, "#migration-") {
		t.Fatalf("switch-back resolution = %q changed=%v err=%v", resolved, changed, err)
	}
	switchBack, err := s.GetTrafficPeriod(ctx, 1, resolved)
	if err != nil || switchBack.Upload != 120 || switchBack.Download != 80 {
		t.Fatalf("switch-back period = %#v err=%v", switchBack, err)
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

	// Deleting a plan unbinds remaining users and cascades binding rows.
	if err := s.DeleteSubscriptionPlan(ctx, p1.ID); err != nil {
		t.Fatal(err)
	}
	members, _ = s.ListUserPlanBindingsForPlan(ctx, p1.ID)
	if len(members) != 0 {
		t.Fatalf("bindings must cascade-delete with the plan")
	}
}

func TestDeleteSubscriptionPlanUnbindsUsersWithoutDeletingThem(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	createPlanTestUser(t, s, 1, "alice")
	createPlanTestUser(t, s, 2, "bob")
	plan := &model.SubscriptionPlan{Name: "to-delete", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{
		{UserID: 1, PlanID: plan.ID},
		{UserID: 2, PlanID: plan.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubscriptionPlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSubscriptionPlan(ctx, plan.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected plan to be gone, got err=%v", err)
	}
	for _, userID := range []int64{1, 2} {
		if _, err := s.GetActiveUserPlanBinding(ctx, userID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("user %d still has a plan binding: %v", userID, err)
		}
		if _, err := s.GetUser(ctx, userID); err != nil {
			t.Fatalf("user %d must remain after plan delete: %v", userID, err)
		}
	}
}

func TestUserNodeExceptionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	createPlanTestUser(t, s, 1, "alice")

	now := time.Now()
	expires := now.Add(24 * time.Hour)
	ex := &model.UserNodeException{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 5, Effect: model.UserNodeExceptionAllow, Reason: "授权试用", ExpiresAt: &expires}
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

	expiredTime := now.Add(-time.Hour)
	expired := &model.UserNodeException{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 6, Effect: model.UserNodeExceptionAllow, Reason: "过期", ExpiresAt: &expiredTime}
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
	// Permanent authorization (nil ExpiresAt) should not be deleted and should be listable.
	permanent := &model.UserNodeException{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 7, Effect: model.UserNodeExceptionAllow, Reason: "", ExpiresAt: nil}
	if err := s.CreateUserNodeException(ctx, permanent); err != nil {
		t.Fatal(err)
	}
	permanentItems, err := s.ListUserNodeExceptionsForUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(permanentItems) != 2 {
		t.Fatalf("permanent exceptions = %d, want 2", len(permanentItems))
	}
	foundPermanent := false
	for _, item := range permanentItems {
		if item.ID == permanent.ID {
			if item.ExpiresAt != nil {
				t.Fatalf("permanent ExpiresAt = %v, want nil", item.ExpiresAt)
			}
			foundPermanent = true
		}
	}
	if !foundPermanent {
		t.Fatalf("permanent exception not found in list")
	}
	deleted, err = s.DeleteExpiredUserNodeExceptions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("permanent should not be deleted, got %d", deleted)
	}
	if err := s.DeleteUserNodeException(ctx, ex.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUserNodeException(ctx, permanent.ID); err != nil {
		t.Fatal(err)
	}
	items, _ = s.ListUserNodeExceptionsForUser(ctx, 1)
	if len(items) != 0 {
		t.Fatalf("exceptions after delete = %d, want 0", len(items))
	}
}
