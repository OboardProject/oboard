package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const planRuleReconcileInterval = 15 * time.Second

// StartPlanRuleReconciler keeps rule-owned membership aligned with the stable
// assignable-node catalog. Every material membership change is still an
// immutable authorization version and enters the normal access-change worker.
func (s *Server) StartPlanRuleReconciler(ctx context.Context) {
	s.reconcilePlanRules(ctx)
	ticker := time.NewTicker(planRuleReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcilePlanRules(ctx)
		}
	}
}

func assignableCatalogDigest(catalog []core.AssignableNode) string {
	type digestNode struct {
		Key                    string                          `json:"key"`
		Enabled                bool                            `json:"enabled"`
		EntryKey               string                          `json:"entry_key"`
		EntryServerID          int64                           `json:"entry_server_id"`
		ExitServerID           int64                           `json:"exit_server_id"`
		ExitExternalOutboundID int64                           `json:"exit_external_outbound_id"`
		ExitRegion             string                          `json:"exit_region"`
		PathServers            []core.AssignableNodeServerRole `json:"path_servers"`
	}
	items := make([]digestNode, 0, len(catalog))
	for _, node := range catalog {
		items = append(items, digestNode{Key: node.Key, Enabled: node.Enabled, EntryKey: node.EntryKey, EntryServerID: node.EntryServerID, ExitServerID: node.ExitServerID, ExitExternalOutboundID: node.ExitExternalOutboundID, ExitRegion: node.ExitRegion, PathServers: node.PathServers})
	}
	raw, _ := json.Marshal(items)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Server) reconcilePlanRules(ctx context.Context) {
	catalog, err := s.assignableNodeCatalog(ctx)
	if err != nil {
		return
	}
	catalogDigest := assignableCatalogDigest(catalog)
	plans, err := s.store.ListSubscriptionPlans(ctx)
	if err != nil {
		return
	}
	for index := range plans {
		if ctx.Err() != nil {
			return
		}
		s.reconcileOnePlanRules(ctx, &plans[index], catalog, catalogDigest)
	}
}

func (s *Server) reconcileOnePlanRules(ctx context.Context, plan *model.SubscriptionPlan, catalog []core.AssignableNode, catalogDigest string) {
	if plan.LatestRevisionID == 0 || plan.PendingRevisionID != 0 {
		return
	}
	rules, exclusions, err := s.store.ListPlanRevisionMembershipPolicy(ctx, plan.LatestRevisionID)
	if err != nil || len(rules) == 0 {
		return
	}
	baseNodes, err := s.store.ListPlanRevisionNodes(ctx, plan.LatestRevisionID)
	if err != nil {
		return
	}
	resolution, err := core.ResolvePlanMembershipRules(baseNodes, rules, exclusions, catalog)
	now := time.Now().UTC()
	if err != nil {
		_ = s.store.SetPlanRuleReconcileState(ctx, model.PlanRuleReconcileState{PlanID: plan.ID, CatalogDigest: catalogDigest, Status: "failed", LastError: err.Error(), UpdatedAt: now})
		return
	}
	state, _ := s.store.GetPlanRuleReconcileState(ctx, plan.ID)
	sourceChanged := planNodeRuleSourceChanged(baseNodes, resolution.Nodes)
	if state != nil && state.CatalogDigest == catalogDigest && state.DesiredDigest == resolution.Digest && len(resolution.AddedKeys) == 0 && len(resolution.RemovedKeys) == 0 && !sourceChanged {
		return
	}
	if len(resolution.AddedKeys) == 0 && len(resolution.RemovedKeys) == 0 && !sourceChanged {
		_ = s.store.SetPlanRuleReconcileState(ctx, model.PlanRuleReconcileState{PlanID: plan.ID, CatalogDigest: catalogDigest, DesiredDigest: resolution.Digest, Status: "current", LastReconciledAt: &now, UpdatedAt: now})
		return
	}
	ordering, err := s.orderingForDesiredMembership(ctx, plan.ID, plan.LatestRevisionID, resolution.Nodes)
	if err != nil {
		return
	}
	result, err := s.store.CreatePlanVersion(ctx, plan.ID, store.PlanVersionMutation{
		BaseRevisionID: plan.LatestRevisionID, ExpectedLockVersion: plan.LockVersion,
		MembershipPolicy: &store.PlanMembershipPolicyMutation{Rules: rules, Exclusions: exclusions, Nodes: resolution.Nodes}, Ordering: ordering,
		ChangeKind: model.PlanChangeKindNodes, ChangeSummary: fmt.Sprintf("自动规则同步：新增 %d，移除 %d", len(resolution.AddedKeys), len(resolution.RemovedKeys)),
	})
	if err != nil {
		if errors.Is(err, store.ErrPlanRevisionConflict) || errors.Is(err, store.ErrPlanVersionApplying) {
			return
		}
		_ = s.store.SetPlanRuleReconcileState(ctx, model.PlanRuleReconcileState{PlanID: plan.ID, CatalogDigest: catalogDigest, DesiredDigest: resolution.Digest, Status: "failed", LastError: err.Error(), UpdatedAt: now})
		return
	}
	if result.NoChange {
		_ = s.store.SetPlanRuleReconcileState(ctx, model.PlanRuleReconcileState{PlanID: plan.ID, CatalogDigest: catalogDigest, DesiredDigest: resolution.Digest, Status: "current", LastReconciledAt: &now, UpdatedAt: now})
		return
	}
	if !result.RequiresDeployment {
		_ = s.store.SetPlanRuleReconcileState(ctx, model.PlanRuleReconcileState{PlanID: plan.ID, CatalogDigest: catalogDigest, DesiredDigest: resolution.Digest, Status: "current", LastReconciledAt: &now, UpdatedAt: now})
		return
	}
	plan.PendingRevisionID = result.PendingRevisionID
	change, err := s.createPlanPublishChange(ctx, nil, plan, result.Revision.ID)
	status, lastError := "applying", ""
	if err != nil {
		status, lastError = "failed", err.Error()
	}
	_ = change
	_ = s.store.SetPlanRuleReconcileState(ctx, model.PlanRuleReconcileState{PlanID: plan.ID, CatalogDigest: catalogDigest, DesiredDigest: resolution.Digest, Status: status, LastError: lastError, LastReconciledAt: &now, UpdatedAt: now})
}

func planNodeRuleSourceChanged(before, after []model.SubscriptionPlanNode) bool {
	type source struct {
		kind   model.PlanNodeSourceType
		ruleID int64
	}
	values := map[string]source{}
	for _, node := range before {
		values[core.NodeKeyOf(node.NodeType, node.NodeID)] = source{node.SourceType, node.SourceRuleID}
	}
	for _, node := range after {
		value, ok := values[core.NodeKeyOf(node.NodeType, node.NodeID)]
		if ok && (value.kind != node.SourceType || value.ruleID != node.SourceRuleID) {
			return true
		}
	}
	return false
}
