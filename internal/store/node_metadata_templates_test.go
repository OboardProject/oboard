package store

import (
	"context"
	"errors"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestNodeMetadataOptimisticLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	name := "日本 IPLC 01"
	created, err := s.UpsertNodeMetadata(ctx, model.AssignableNodeInbound, 7, &name, 0, nil)
	if err != nil || created.LockVersion != 1 || created.DisplayNameOverride == nil || *created.DisplayNameOverride != name {
		t.Fatalf("create = %#v, %v", created, err)
	}
	if _, err := s.UpsertNodeMetadata(ctx, model.AssignableNodeInbound, 7, nil, 99, nil); !errors.Is(err, ErrNodeMetadataConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	cleared, err := s.UpsertNodeMetadata(ctx, model.AssignableNodeInbound, 7, nil, 1, nil)
	if err != nil || cleared.LockVersion != 2 || cleared.DisplayNameOverride != nil {
		t.Fatalf("clear = %#v, %v", cleared, err)
	}
}

func TestNodeOrderTemplateRevisionArchiveAndPlanSnapshot(t *testing.T) {
	ctx := context.Background()
	s := openPlansTestStore(t)
	policy := model.NodeOrderTemplatePolicy{
		Version: 1, BaseMode: model.SubscriptionNodeOrderExitRegion,
		ExitRegionOrder: []string{"JP", "HK"}, EntryRegionOrderMode: model.SubscriptionNodeEntryRegionOrderInheritExit,
		NewNodePlacement: model.SubscriptionNodePlacementByTemplate, UnmatchedPlacement: model.SubscriptionNodePlacementAppend,
	}
	template := &model.NodeOrderTemplate{Name: "日本优先", Enabled: true, Policy: policy}
	if err := s.CreateNodeOrderTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	updatedPolicy := policy
	updatedPolicy.ExitRegionOrder = []string{"HK", "JP"}
	updated, err := s.UpdateNodeOrderTemplate(ctx, template.ID, 1, template.Name, "r2", updatedPolicy, nil)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	if _, err := s.UpdateNodeOrderTemplate(ctx, template.ID, 1, template.Name, "stale", policy, nil); !errors.Is(err, ErrNodeOrderTemplateConflict) {
		t.Fatalf("stale template update err = %v", err)
	}

	plan := &model.SubscriptionPlan{Name: "snapshot", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: 1}}); err != nil {
		t.Fatal(err)
	}
	templateID := template.ID
	planPolicy := model.NewSubscriptionNodeOrderPolicy()
	planPolicy.ExitRegionOrder = []string{"JP", "HK"}
	name := "VIP 日本"
	version, err := s.CreatePlanVersion(ctx, plan.ID, PlanVersionMutation{
		ExpectedLockVersion: plan.LockVersion,
		Ordering:            &PlanOrderingMutation{Policy: planPolicy, SetTemplateProvenance: true, OrderTemplateID: &templateID, OrderTemplateRevision: 1},
		NodePresentation:    &PlanNodePresentationMutation{DisplayNameOverrides: map[string]*string{"inbound:1": &name}},
		ChangeKind:          model.PlanChangeKindPresentation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if version.Revision.OrderTemplateID == nil || *version.Revision.OrderTemplateID != template.ID || version.Revision.OrderTemplateRevision != 1 {
		t.Fatalf("snapshot provenance = %#v", version.Revision)
	}
	clone, err := s.CloneSubscriptionPlan(ctx, plan.ID, "snapshot-copy")
	if err != nil {
		t.Fatal(err)
	}
	cloneRevision, err := s.GetPlanRevision(ctx, clone.ID, clone.CurrentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	cloneNodes, err := s.ListActivePlanNodes(ctx, clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cloneRevision.OrderTemplateID == nil || *cloneRevision.OrderTemplateID != template.ID || cloneRevision.OrderTemplateRevision != 1 {
		t.Fatalf("clone provenance = %#v", cloneRevision)
	}
	if len(cloneNodes) != 1 || cloneNodes[0].DisplayNameOverride == nil || *cloneNodes[0].DisplayNameOverride != name {
		t.Fatalf("clone name override = %#v", cloneNodes)
	}
	archived, err := s.ArchiveNodeOrderTemplate(ctx, template.ID, 2, nil)
	if err != nil || archived.Enabled {
		t.Fatalf("archive = %#v, %v", archived, err)
	}
	unchanged, err := s.GetPlanRevision(ctx, plan.ID, version.Revision.ID)
	if err != nil || unchanged.OrderTemplateRevision != 1 || unchanged.NodeOrderPolicy.ExitRegionOrder[0] != "JP" {
		t.Fatalf("template update mutated plan snapshot = %#v, %v", unchanged, err)
	}
}
