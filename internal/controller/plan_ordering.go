package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// ---------------------------------------------------------------------------
// Subscription plan ordering
// ---------------------------------------------------------------------------

// planOrdering serves the ordering editor GET route. Saving goes through
// POST /subscription-plans/:id/ordering/versions (see planOrderingVersionCreate).
func (s *Server) planOrdering(w http.ResponseWriter, r *http.Request, id int64) {
	switch r.Method {
	case http.MethodGet:
		s.planOrderingGet(w, r, id)
	default:
		method(w)
	}
}

func (s *Server) planOrderingGet(w http.ResponseWriter, r *http.Request, id int64) {
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	revisionID, err := s.planOrderingRevisionID(r, plan)
	if err != nil {
		fail(w, err, 404)
		return
	}
	revision, nodes, err := s.store.GetPlanRevisionOrdering(r.Context(), id, revisionID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	config, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	nameOptions, err := s.orderingNameOptions(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	ordered, err := core.BuildOrderingNodes(nodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, revision.NodeOrderPolicy, nameOptions)
	if err != nil {
		fail(w, err, 500)
		return
	}
	views, unplaced, warnings := s.orderingNodeViews(r.Context(), config, ordered, revision.NodeOrderPolicy.Mode == model.SubscriptionNodeOrderManual)
	response := map[string]any{
		"plan_id":                    plan.ID,
		"lock_version":               plan.LockVersion,
		"base_revision_id":           plan.LatestRevisionID,
		"revision_id":                revision.ID,
		"version_no":                 revision.VersionNo,
		"read_only":                  revisionID != plan.LatestRevisionID,
		"is_current":                 revisionID == plan.CurrentRevisionID,
		"is_latest":                  revisionID == plan.LatestRevisionID,
		"pending_revision_id":        plan.PendingRevisionID,
		"policy":                     revision.NodeOrderPolicy,
		"nodes":                      views,
		"unplaced_count":             unplaced,
		"warnings":                   warnings,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
		"order_template_id":          revision.OrderTemplateID,
		"order_template_revision":    revision.OrderTemplateRevision,
	}
	if revision.OrderTemplateID != nil {
		if template, templateErr := s.store.GetNodeOrderTemplate(r.Context(), *revision.OrderTemplateID); templateErr == nil {
			response["order_template"] = template
			response["template_update_available"] = revision.OrderTemplateRevision < template.Revision
			response["template_archived"] = !template.Enabled
		} else if errors.Is(templateErr, sql.ErrNoRows) {
			response["template_archived"] = true
		}
	}
	write(w, 200, response)
}

// planOrderingRevisionID resolves the editor's target revision. Empty or
// ?revision_id= defaults to the latest saved version (the working base); the
// legacy ?revision=draft|active selectors still map to the frozen legacy draft
// or the current version during the migration.
func (s *Server) planOrderingRevisionID(r *http.Request, plan *model.SubscriptionPlan) (int64, error) {
	if raw := strings.TrimSpace(r.URL.Query().Get("revision_id")); raw != "" {
		revisionID, err := parseRevisionID(raw)
		if err != nil {
			return 0, errors.New("revision_id must be a revision id")
		}
		if _, err := s.store.GetPlanRevision(r.Context(), plan.ID, revisionID); err != nil {
			return 0, errors.New("revision not found")
		}
		return revisionID, nil
	}
	if legacy := strings.TrimSpace(r.URL.Query().Get("revision")); legacy != "" {
		switch legacy {
		case "active", "current":
			if plan.CurrentRevisionID == 0 {
				return 0, errors.New("plan has no current revision")
			}
			return plan.CurrentRevisionID, nil
		case "draft":
			if plan.DraftRevisionID == 0 {
				return 0, errors.New("plan has no draft revision")
			}
			return plan.DraftRevisionID, nil
		}
		// Unknown legacy selectors are rejected instead of silently falling
		// back to the latest version.
		return 0, errors.New("revision must be draft, current or a revision id")
	}
	if plan.LatestRevisionID == 0 {
		return 0, errors.New("plan has no saved version")
	}
	return plan.LatestRevisionID, nil
}

func parseRevisionID(raw string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(raw, "%d", &id); err != nil || id <= 0 {
		return 0, errors.New("invalid revision id")
	}
	return id, nil
}

type orderingNodeView struct {
	Key                 string                   `json:"key"`
	NodeType            model.AssignableNodeType `json:"node_type"`
	NodeID              int64                    `json:"node_id"`
	Name                string                   `json:"name"`
	SourceName          string                   `json:"source_name"`
	GlobalName          string                   `json:"global_name"`
	PlanNameOverride    *string                  `json:"plan_name_override"`
	HasPlanNameOverride bool                     `json:"has_plan_name_override"`
	Group               string                   `json:"group"`
	EntryKey            string                   `json:"entry_key,omitempty"`
	EntryName           string                   `json:"entry_name,omitempty"`
	EntryProtocol       string                   `json:"entry_protocol,omitempty"`
	EntryPort           int                      `json:"entry_port,omitempty"`
	EntryServerID       int64                    `json:"entry_server_id,omitempty"`
	EntryRegion         string                   `json:"entry_region,omitempty"`
	ExitServerID        int64                    `json:"exit_server_id,omitempty"`
	ExitRegion          string                   `json:"exit_region,omitempty"`
	ManualPosition      *int                     `json:"manual_position,omitempty"`
	EffectivePosition   int                      `json:"effective_position"`
	Renderable          bool                     `json:"renderable"`
	Warning             string                   `json:"warning,omitempty"`
}

func (s *Server) orderingNodeViews(ctx context.Context, config store.FullRoutingConfig, ordered []core.SubscriptionNode, countUnplaced bool) ([]orderingNodeView, int, []string) {
	renderable := renderableNodeKeys(config)
	serverByID := map[int64]string{}
	for _, server := range config.Servers {
		serverByID[server.ID] = server.Name
	}
	views := make([]orderingNodeView, 0, len(ordered))
	unplaced := 0
	warnings := []string{}
	for position, node := range ordered {
		view := orderingNodeView{
			Key:                 node.Key,
			NodeType:            node.NodeType,
			NodeID:              node.NodeID,
			Name:                node.Name,
			SourceName:          node.SourceName,
			GlobalName:          node.GlobalName,
			PlanNameOverride:    node.PlanNameOverride,
			HasPlanNameOverride: node.HasPlanNameOverride,
			Group:               node.Group,
			EntryKey:            node.EntryKey,
			EntryServerID:       node.EntryServerID,
			EntryRegion:         node.EntryRegion,
			ExitServerID:        node.ExitServerID,
			ExitRegion:          node.ExitRegion,
			EffectivePosition:   position,
			Renderable:          renderable[node.Key],
		}
		if node.Inbound.ID != 0 {
			view.EntryName = node.Inbound.Name
			view.EntryProtocol = string(node.Inbound.Protocol)
			view.EntryPort = node.Inbound.Port
		}
		if node.ManualPosition != nil {
			position := *node.ManualPosition
			view.ManualPosition = &position
		} else if countUnplaced {
			unplaced++
		}
		if !view.Renderable {
			view.Warning = "当前不可渲染"
			warnings = append(warnings, fmt.Sprintf("%s 当前不可渲染", node.Name))
		} else if node.ExitRegion == "" {
			view.Warning = "出口地区未解析"
			warnings = append(warnings, fmt.Sprintf("%s 出口地区未解析", node.Name))
		} else if node.EntryKey == "" {
			view.Warning = "没有 OBoard 入口"
			warnings = append(warnings, fmt.Sprintf("%s 没有 OBoard 入口", node.Name))
		}
		views = append(views, view)
	}
	return views, unplaced, warnings
}

// renderableNodeKeys reports which node keys can actually render a
// subscription entry, mirroring the generator's own conditions without
// building raw payloads.
func renderableNodeKeys(config store.FullRoutingConfig) map[string]bool {
	out := map[string]bool{}
	serverByID := map[int64]model.Server{}
	for _, server := range config.Servers {
		serverByID[server.ID] = server
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range config.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	pathByID := map[int64]model.ProxyPath{}
	for _, path := range config.ProxyPaths {
		pathByID[path.ID] = path
	}
	externalByID := map[int64]model.ExternalOutbound{}
	for _, external := range config.ExternalOutbounds {
		externalByID[external.ID] = external
	}
	hasBranch := map[int64]bool{}
	for _, path := range config.ProxyPaths {
		hasBranch[path.InboundID] = true
	}
	for _, inbound := range config.Inbounds {
		if !inbound.Enabled || hasBranch[inbound.ID] {
			continue
		}
		server := serverByID[inbound.ServerID]
		server.EntryAddress = core.ResolveEntryAddress(inbound, server)
		if strings.TrimSpace(server.EntryAddress) != "" {
			out[core.NodeKeyOf(model.AssignableNodeInbound, inbound.ID)] = true
		}
	}
	for _, path := range config.ProxyPaths {
		if !path.Enabled {
			continue
		}
		inbound, ok := inboundByID[path.InboundID]
		if !ok || !inbound.Enabled {
			continue
		}
		server := serverByID[inbound.ServerID]
		server.EntryAddress = core.ResolveEntryAddress(inbound, server)
		if strings.TrimSpace(server.EntryAddress) != "" {
			out[core.NodeKeyOf(model.AssignableNodeProxyPath, path.ID)] = true
		}
	}
	for _, external := range config.ExternalOutbounds {
		if external.Enabled && external.ExposeToUsers {
			out[core.NodeKeyOf(model.AssignableNodeExternalOutbound, external.ID)] = true
		}
	}
	return out
}

// planOrderingUpdate serves PUT /subscription-plans/:id/ordering.
// planOrderingVersionCreate serves POST /subscription-plans/:id/ordering/versions.
// The request derives a new immutable version from the latest saved snapshot.
// Ordering is presentation-only: the version becomes current immediately and
// no agent task is created. Conflicts return 409 with the current lock and
// latest version so the UI can offer a reload.
func (s *Server) planOrderingVersionCreate(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		BaseRevisionID      int64                             `json:"base_revision_id"`
		ExpectedLockVersion int64                             `json:"expected_lock_version"`
		Policy              model.SubscriptionNodeOrderPolicy `json:"policy"`
		ManualNodeOrder     []string                          `json:"manual_node_order"`
		ChangeSummary       string                            `json:"change_summary"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ExpectedLockVersion <= 0 {
		fail(w, errors.New("expected_lock_version is required"), 400)
		return
	}
	policy, err := core.ValidateSubscriptionNodeOrderPolicy(req.Policy)
	if err != nil {
		fail(w, err, 400)
		return
	}
	manualOrder := normalizeManualOrder(req.ManualNodeOrder)
	if policy.Mode == model.SubscriptionNodeOrderManual {
		seen := map[string]bool{}
		for _, key := range manualOrder {
			nodeType, nodeID, ok := core.ParseNodeKey(key)
			if !ok || nodeID <= 0 || nodeType == "" {
				fail(w, fmt.Errorf("invalid manual node key %q", key), 400)
				return
			}
			if seen[key] {
				fail(w, fmt.Errorf("duplicate manual node key %q", key), 400)
				return
			}
			seen[key] = true
		}
	}
	result, err := s.store.CreatePlanVersion(r.Context(), id, store.PlanVersionMutation{
		BaseRevisionID:      req.BaseRevisionID,
		ExpectedLockVersion: req.ExpectedLockVersion,
		Ordering:            &store.PlanOrderingMutation{Policy: policy, ManualOrder: manualOrder},
		ChangeKind:          model.PlanChangeKindOrdering,
		ChangeSummary:       strings.TrimSpace(req.ChangeSummary),
	})
	if err != nil {
		if errors.Is(err, store.ErrPlanOrderingInvalid) {
			fail(w, err, 400)
			return
		}
		s.planVersionConflict(w, err, id)
		return
	}
	auditReq(s, r, "ordering", "subscription-plan", fmt.Sprintf("plan=%d version=%d mode=%s", id, result.Revision.ID, policy.Mode))
	if result.NoChange {
		write(w, 200, map[string]any{
			"no_change":                  true,
			"revision_id":                result.LatestRevisionID,
			"lock_version":               result.LockVersion,
			"effective_immediately":      true,
			"runtime_authorization_mode": s.authorizationMode(r.Context()),
		})
		return
	}
	write(w, 200, map[string]any{
		"revision":                   result.Revision,
		"version_no":                 result.Revision.VersionNo,
		"revision_id":                result.Revision.ID,
		"effective_immediately":      true,
		"current_revision_id":        result.CurrentRevisionID,
		"latest_revision_id":         result.LatestRevisionID,
		"lock_version":               result.LockVersion,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func normalizeManualOrder(list []string) []string {
	out := make([]string, 0, len(list))
	for _, raw := range list {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		out = append(out, key)
	}
	return out
}

func (s *Server) planOrderingApplyTemplate(w http.ResponseWriter, r *http.Request, planID int64) {
	var req struct {
		TemplateID          int64  `json:"template_id"`
		TemplateRevision    int64  `json:"template_revision"`
		BaseRevisionID      int64  `json:"base_revision_id"`
		ExpectedLockVersion int64  `json:"expected_lock_version"`
		ApplyMode           string `json:"apply_mode"`
		ChangeSummary       string `json:"change_summary"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.TemplateID <= 0 || req.TemplateRevision <= 0 || req.BaseRevisionID <= 0 || req.ExpectedLockVersion <= 0 {
		fail(w, errors.New("template_id, template_revision, base_revision_id and expected_lock_version are required"), 400)
		return
	}
	template, err := s.store.GetNodeOrderTemplate(r.Context(), req.TemplateID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if !template.Enabled {
		fail(w, errors.New("node order template is archived"), 400)
		return
	}
	if req.TemplateRevision != template.Revision {
		fail(w, store.ErrNodeOrderTemplateConflict, 409)
		return
	}
	if req.ApplyMode != "preserve_manual" && req.ApplyMode != "rebuild" {
		fail(w, errors.New("apply_mode must be preserve_manual or rebuild"), 400)
		return
	}
	plan, err := s.store.GetSubscriptionPlan(r.Context(), planID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	baseID := req.BaseRevisionID
	if baseID == 0 {
		baseID = plan.LatestRevisionID
	}
	revision, nodes, err := s.store.GetPlanRevisionOrdering(r.Context(), planID, baseID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	policy := core.SubscriptionPolicyFromTemplate(template.Policy, template.Policy.BaseMode)
	manualOrder := []string{}
	if req.ApplyMode == "preserve_manual" {
		config, loadErr := s.store.FullRoutingConfigData(r.Context())
		if loadErr != nil {
			fail(w, loadErr, 500)
			return
		}
		nameOptions, loadErr := s.orderingNameOptions(r.Context())
		if loadErr != nil {
			fail(w, loadErr, 500)
			return
		}
		currentOrder, loadErr := core.BuildOrderingNodes(nodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, revision.NodeOrderPolicy, nameOptions)
		if loadErr != nil {
			fail(w, loadErr, 500)
			return
		}
		positioned := map[string]bool{}
		if revision.NodeOrderPolicy.Mode == model.SubscriptionNodeOrderManual {
			for _, node := range nodes {
				if node.SortPosition != nil {
					positioned[core.NodeKeyOf(node.NodeType, node.NodeID)] = true
				}
			}
		}
		for _, node := range currentOrder {
			if revision.NodeOrderPolicy.Mode != model.SubscriptionNodeOrderManual || positioned[node.Key] {
				manualOrder = append(manualOrder, node.Key)
			}
		}
		policy.Mode = model.SubscriptionNodeOrderManual
	}
	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		summary = fmt.Sprintf("应用模板「%s」r%d", template.Name, template.Revision)
	}
	templateID := template.ID
	result, err := s.store.CreatePlanVersion(r.Context(), planID, store.PlanVersionMutation{
		BaseRevisionID: baseID, ExpectedLockVersion: req.ExpectedLockVersion,
		Ordering: &store.PlanOrderingMutation{
			Policy: policy, ManualOrder: manualOrder, ClearManualPositions: req.ApplyMode == "rebuild",
			SetTemplateProvenance: true, OrderTemplateID: &templateID, OrderTemplateRevision: template.Revision,
		},
		ChangeKind: model.PlanChangeKindOrdering, ChangeSummary: summary, CreatedBy: requestActorID(r),
	})
	if err != nil {
		s.planVersionConflict(w, err, planID)
		return
	}
	auditReq(s, r, "ordering.apply_template", "subscription-plan", fmt.Sprintf("plan=%d template=%d revision=%d mode=%s", planID, template.ID, template.Revision, req.ApplyMode))
	write(w, 200, map[string]any{"revision": result.Revision, "no_change": result.NoChange, "effective_immediately": true, "lock_version": result.LockVersion, "latest_revision_id": result.LatestRevisionID})
}

func (s *Server) planNodePresentationVersionCreate(w http.ResponseWriter, r *http.Request, planID int64) {
	var req struct {
		BaseRevisionID      int64 `json:"base_revision_id"`
		ExpectedLockVersion int64 `json:"expected_lock_version"`
		Nodes               []struct {
			NodeKey             string          `json:"node_key"`
			DisplayNameOverride json.RawMessage `json:"display_name_override"`
		} `json:"nodes"`
		ChangeSummary string `json:"change_summary"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.BaseRevisionID <= 0 || req.ExpectedLockVersion <= 0 || len(req.Nodes) == 0 {
		fail(w, errors.New("base_revision_id, expected_lock_version and nodes are required"), 400)
		return
	}
	_, baseNodes, err := s.store.GetPlanRevisionOrdering(r.Context(), planID, req.BaseRevisionID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	valid := map[string]bool{}
	for _, node := range baseNodes {
		valid[core.NodeKeyOf(node.NodeType, node.NodeID)] = true
	}
	overrides := map[string]*string{}
	for _, item := range req.Nodes {
		key := strings.TrimSpace(item.NodeKey)
		if !valid[key] {
			fail(w, fmt.Errorf("node %s is not in the base revision", key), 400)
			return
		}
		if item.DisplayNameOverride == nil {
			fail(w, errors.New("display_name_override is required"), 400)
			return
		}
		if string(item.DisplayNameOverride) == "null" {
			overrides[key] = nil
			continue
		}
		var value string
		if err := json.Unmarshal(item.DisplayNameOverride, &value); err != nil {
			fail(w, errors.New("display_name_override must be a string or null"), 400)
			return
		}
		value = strings.TrimSpace(value)
		if value == "" {
			overrides[key] = nil
			continue
		}
		if len([]rune(value)) > 100 {
			fail(w, errors.New("display_name_override is too long"), 400)
			return
		}
		overrides[key] = &value
	}
	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		summary = fmt.Sprintf("修改 %d 个方案内节点名称", len(overrides))
	}
	result, err := s.store.CreatePlanVersion(r.Context(), planID, store.PlanVersionMutation{
		BaseRevisionID: req.BaseRevisionID, ExpectedLockVersion: req.ExpectedLockVersion,
		NodePresentation: &store.PlanNodePresentationMutation{DisplayNameOverrides: overrides},
		ChangeKind:       model.PlanChangeKindPresentation, ChangeSummary: summary, CreatedBy: requestActorID(r),
	})
	if err != nil {
		s.planVersionConflict(w, err, planID)
		return
	}
	auditReq(s, r, "node_presentation.update", "subscription-plan", fmt.Sprintf("plan=%d nodes=%d", planID, len(overrides)))
	write(w, 200, map[string]any{"revision": result.Revision, "no_change": result.NoChange, "effective_immediately": true, "lock_version": result.LockVersion, "latest_revision_id": result.LatestRevisionID})
}

// planVersionConflict writes the 409 plan_version_conflict response with the
// current lock version and latest revision so the UI can prompt a reload.
func (s *Server) planVersionConflict(w http.ResponseWriter, err error, planID int64) {
	out := map[string]any{
		"code":    "plan_version_conflict",
		"message": "方案已由其他管理员保存新版本，请重新加载后再次调整",
	}
	if errors.Is(err, store.ErrPlanVersionApplying) {
		out["code"] = "plan_version_applying"
		out["message"] = "方案版本正在应用，完成后才能继续保存新的方案版本"
	}
	if plan, loadErr := s.store.GetSubscriptionPlan(context.Background(), planID); loadErr == nil {
		out["current_lock_version"] = plan.LockVersion
		out["latest_revision_id"] = plan.LatestRevisionID
		if revision, revErr := s.store.GetPlanRevision(context.Background(), planID, plan.LatestRevisionID); revErr == nil {
			out["latest_version_no"] = revision.VersionNo
		}
	}
	write(w, http.StatusConflict, out)
}

// planOrderingPreview serves POST /subscription-plans/:id/ordering/preview.
// It never writes: the base node set is the requested base revision (defaults
// to the latest saved version) and the requested policy is applied by the real
// ordering engine.
func (s *Server) planOrderingPreview(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		BaseRevisionID  int64                             `json:"base_revision_id"`
		Policy          model.SubscriptionNodeOrderPolicy `json:"policy"`
		ManualNodeOrder []string                          `json:"manual_node_order"`
	}
	if !decode(w, r, &req) {
		return
	}
	policy, err := core.ValidateSubscriptionNodeOrderPolicy(req.Policy)
	if err != nil {
		fail(w, err, 400)
		return
	}
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	revisionID := req.BaseRevisionID
	if revisionID == 0 {
		revisionID = plan.LatestRevisionID
	}
	if revisionID == 0 {
		fail(w, errors.New("plan has no revision to preview"), 400)
		return
	}
	revision, nodes, err := s.store.GetPlanRevisionOrdering(r.Context(), id, revisionID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if policy.Mode == model.SubscriptionNodeOrderManual {
		positionByKey, err := manualPositionsForKeys(nodes, normalizeManualOrder(req.ManualNodeOrder))
		if err != nil {
			fail(w, err, 400)
			return
		}
		for i := range nodes {
			key := core.NodeKeyOf(nodes[i].NodeType, nodes[i].NodeID)
			if position, ok := positionByKey[key]; ok {
				position := position
				nodes[i].SortPosition = &position
			} else {
				nodes[i].SortPosition = nil
			}
		}
	}
	config, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	nameOptions, err := s.orderingNameOptions(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	ordered, err := core.BuildOrderingNodes(nodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, policy, nameOptions)
	if err != nil {
		fail(w, err, 500)
		return
	}
	views, unplaced, warnings := s.orderingNodeViews(r.Context(), config, ordered, policy.Mode == model.SubscriptionNodeOrderManual)
	write(w, 200, map[string]any{
		"plan_id":                    plan.ID,
		"base_revision_id":           revision.ID,
		"version_no":                 revision.VersionNo,
		"policy":                     policy,
		"nodes":                      views,
		"unplaced_count":             unplaced,
		"warnings":                   warnings,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func (s *Server) orderingNameOptions(ctx context.Context) (core.OrderingNameOptions, error) {
	metadata, err := s.store.ListNodeMetadata(ctx)
	if err != nil {
		return core.OrderingNameOptions{}, err
	}
	names := map[string]*string{}
	for key, item := range metadata {
		if item.DisplayNameOverride != nil {
			names[key] = item.DisplayNameOverride
		}
	}
	return core.OrderingNameOptions{GlobalNodeNames: names}, nil
}

func manualPositionsForKeys(nodes []model.SubscriptionPlanNode, manualOrder []string) (map[string]int, error) {
	byKey := map[string]bool{}
	for _, node := range nodes {
		byKey[core.NodeKeyOf(node.NodeType, node.NodeID)] = true
	}
	out := map[string]int{}
	seen := map[string]bool{}
	for position, key := range manualOrder {
		if seen[key] {
			return nil, fmt.Errorf("duplicate manual node key %q", key)
		}
		seen[key] = true
		if !byKey[key] {
			return nil, fmt.Errorf("manual node order contains %s which is not in the revision node set", key)
		}
		out[key] = position
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Node scope preview
// ---------------------------------------------------------------------------

type nodeScopeKind string

const (
	nodeScopeNode             nodeScopeKind = "node"
	nodeScopeEntryInbound     nodeScopeKind = "entry_inbound"
	nodeScopeEntryServer      nodeScopeKind = "entry_server"
	nodeScopePathServer       nodeScopeKind = "path_server"
	nodeScopeExitServer       nodeScopeKind = "exit_server"
	nodeScopeExitRegion       nodeScopeKind = "exit_region"
	nodeScopeExternalOutbound nodeScopeKind = "external_outbound"
)

type nodeScopeRequest struct {
	AnchorNodeKey string `json:"anchor_node_key"`
	Scope         struct {
		Kind               string `json:"kind"`
		ServerID           int64  `json:"server_id,omitempty"`
		Region             string `json:"region,omitempty"`
		ExternalOutboundID int64  `json:"external_outbound_id,omitempty"`
	} `json:"scope"`
	IncludeDisabled bool `json:"include_disabled"`
}

// assignableNodeScopePreview serves POST /api/v1/assignable-node-scopes/preview.
// Scope resolution is authoritative in the backend: the frontend only sends the
// anchor and the scope kind and receives the complete matching node set.
func (s *Server) assignableNodeScopePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req nodeScopeRequest
	if !decode(w, r, &req) {
		return
	}
	config, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	nodes, err := core.BuildAssignableNodeCatalog(core.AssignableNodeCatalogInput{
		Servers:           config.Servers,
		Inbounds:          config.Inbounds,
		ProxyPaths:        config.ProxyPaths,
		ProxyPathSteps:    config.ProxyPathSteps,
		EgressResults:     config.ProxyPathEgressResults,
		ExternalOutbounds: config.ExternalOutbounds,
		ServerOnline:      serverOnlineMap(config),
	})
	if err != nil {
		fail(w, err, 500)
		return
	}
	byKey := map[string]core.AssignableNode{}
	for _, node := range nodes {
		byKey[node.Key] = node
	}
	anchor, ok := byKey[req.AnchorNodeKey]
	if !ok {
		fail(w, fmt.Errorf("anchor node %q is not assignable", req.AnchorNodeKey), 400)
		return
	}
	kind := nodeScopeKind(strings.TrimSpace(req.Scope.Kind))
	var serverID int64
	switch kind {
	case nodeScopeNode:
		// "仅选择此节点" is a single-node scope: it must never depend on the
		// anchor having an OBoard root inbound (imported exits have none).
	case nodeScopeEntryInbound:
		if anchor.EntryKey == "" {
			fail(w, errors.New("当前节点没有 OBoard 入口，无法按入口选择"), 400)
			return
		}
	case nodeScopeEntryServer:
		if anchor.EntryServerID == 0 {
			fail(w, errors.New("当前节点没有受管入口服务器，无法按入口服务器选择"), 400)
			return
		}
		serverID = anchor.EntryServerID
	case nodeScopePathServer:
		serverID = req.Scope.ServerID
		if serverID == 0 {
			if len(anchor.PathServers) == 1 {
				serverID = anchor.PathServers[0].ServerID
			} else {
				fail(w, errors.New("当前节点涉及多个路径服务器，请指定 server_id"), 400)
				return
			}
		}
	case nodeScopeExitServer:
		if anchor.ExitServerID == 0 {
			fail(w, errors.New("当前节点无法确定受管出口服务器"), 400)
			return
		}
		serverID = anchor.ExitServerID
	case nodeScopeExitRegion:
		if core.NormalizeRegionCode(anchor.ExitRegion) == "" {
			fail(w, errors.New("当前节点出口地区未解析，无法按出口地区选择"), 400)
			return
		}
	case nodeScopeExternalOutbound:
		if anchor.ExitExternalOutboundID == 0 {
			fail(w, errors.New("当前节点不使用导入出口，无法按导入出口选择"), 400)
			return
		}
	default:
		fail(w, fmt.Errorf("invalid scope kind %q", req.Scope.Kind), 400)
		return
	}
	serverName := ""
	if serverID != 0 {
		for _, server := range config.Servers {
			if server.ID == serverID {
				serverName = server.Name
				break
			}
		}
	}
	matched := []core.AssignableNode{}
	for _, node := range nodes {
		if !req.IncludeDisabled && !node.Enabled {
			continue
		}
		switch kind {
		case nodeScopeNode:
			if node.Key == anchor.Key {
				matched = append(matched, node)
			}
		case nodeScopeEntryInbound:
			if node.EntryKey != "" && node.EntryKey == anchor.EntryKey {
				matched = append(matched, node)
			}
		case nodeScopeEntryServer:
			if node.EntryServerID != 0 && node.EntryServerID == serverID {
				matched = append(matched, node)
			}
		case nodeScopePathServer:
			for _, role := range node.PathServers {
				if role.ServerID == serverID {
					matched = append(matched, node)
					break
				}
			}
		case nodeScopeExitServer:
			if node.ExitServerID != 0 && node.ExitServerID == serverID {
				matched = append(matched, node)
			}
		case nodeScopeExitRegion:
			if core.NormalizeRegionCode(node.ExitRegion) == core.NormalizeRegionCode(anchor.ExitRegion) && core.NormalizeRegionCode(node.ExitRegion) != "" {
				matched = append(matched, node)
			}
		case nodeScopeExternalOutbound:
			if node.ExitExternalOutboundID != 0 && node.ExitExternalOutboundID == anchor.ExitExternalOutboundID {
				matched = append(matched, node)
			}
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Key < matched[j].Key })
	nodeRefs := make([]map[string]any, 0, len(matched))
	sampleNodes := make([]map[string]any, 0, 5)
	for _, node := range matched {
		nodeRefs = append(nodeRefs, map[string]any{"node_type": node.Type, "node_id": node.ID})
		if len(sampleNodes) < 5 {
			sampleNodes = append(sampleNodes, map[string]any{"key": node.Key, "name": node.Name})
		}
	}
	hash := nodeScopeSelectionHash(nodeRefs)
	warnings := []string{}
	if !anchor.Renderable {
		warnings = append(warnings, "当前节点不可渲染")
	}
	write(w, 200, map[string]any{
		"scope": map[string]any{
			"kind":        kind,
			"server_id":   serverID,
			"server_name": serverName,
			"region":      anchor.ExitRegion,
		},
		"count":                      len(nodeRefs),
		"node_refs":                  nodeRefs,
		"sample_nodes":               sampleNodes,
		"warnings":                   warnings,
		"selection_hash":             hash,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}

func nodeScopeSelectionHash(nodeRefs []map[string]any) string {
	raw, err := json.Marshal(nodeRefs)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func serverOnlineMap(config store.FullRoutingConfig) map[int64]bool {
	out := make(map[int64]bool, len(config.Servers))
	for _, server := range config.Servers {
		out[server.ID] = server.Status == model.ServerOnline
	}
	return out
}

// ---------------------------------------------------------------------------
// Batch user node exceptions
// ---------------------------------------------------------------------------

type batchUserExceptionRequest struct {
	UserIDs []int64 `json:"user_ids"`
	Nodes   []struct {
		NodeType model.AssignableNodeType `json:"node_type"`
		NodeID   int64                    `json:"node_id"`
	} `json:"nodes"`
	Effect    model.UserNodeExceptionEffect `json:"effect"`
	Reason    string                        `json:"reason"`
	StartsAt  *time.Time                    `json:"starts_at"`
	ExpiresAt time.Time                     `json:"expires_at"`
}

type batchExceptionOutcome struct {
	Created []model.UserNodeException `json:"created"`
	Updated []model.UserNodeException `json:"updated"`
	Skipped []string                  `json:"skipped"`
}

// planBatchUserExceptions classifies the requested (user, node) pairs against
// the existing rows. Same-effect rows are skipped; opposite-effect rows are
// updated in place (the unique index allows one exception per user+node).
func (s *Server) planBatchUserExceptions(ctx context.Context, req batchUserExceptionRequest, existing []model.UserNodeException) (batchExceptionOutcome, error) {
	out := batchExceptionOutcome{}
	byKey := map[string]model.UserNodeException{}
	for _, row := range existing {
		byKey[exceptionKey(row.UserID, row.NodeType, row.NodeID)] = row
	}
	seenPairs := map[string]bool{}
	for _, userID := range req.UserIDs {
		if userID <= 0 {
			return out, errors.New("invalid user_id")
		}
		for _, node := range req.Nodes {
			if node.NodeType == "" || node.NodeID <= 0 {
				return out, errors.New("invalid node")
			}
			key := exceptionKey(userID, node.NodeType, node.NodeID)
			if seenPairs[key] {
				continue
			}
			seenPairs[key] = true
			row, exists := byKey[key]
			if exists && row.Effect == req.Effect {
				out.Skipped = append(out.Skipped, key)
				continue
			}
			status := model.UserNodeExceptionActive
			if req.Effect == model.UserNodeExceptionAllow {
				status = model.UserNodeExceptionPending
			}
			item := model.UserNodeException{
				ID:        row.ID,
				UserID:    userID,
				NodeType:  node.NodeType,
				NodeID:    node.NodeID,
				Effect:    req.Effect,
				Reason:    req.Reason,
				Status:    status,
				StartsAt:  req.StartsAt,
				ExpiresAt: req.ExpiresAt,
			}
			if exists {
				out.Updated = append(out.Updated, item)
			} else {
				out.Created = append(out.Created, item)
			}
		}
	}
	return out, nil
}

func exceptionKey(userID int64, nodeType model.AssignableNodeType, nodeID int64) string {
	return fmt.Sprintf("%d:%s:%d", userID, nodeType, nodeID)
}

func (s *Server) validateBatchUserExceptionRequest(ctx context.Context, req *batchUserExceptionRequest) error {
	if len(req.UserIDs) == 0 {
		return errors.New("user_ids required")
	}
	if len(req.Nodes) == 0 {
		return errors.New("nodes required")
	}
	if req.Effect != model.UserNodeExceptionAllow && req.Effect != model.UserNodeExceptionDeny {
		return errors.New("effect must be allow or deny")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return errors.New("reason required")
	}
	if len(req.Reason) > 300 {
		return errors.New("reason too long")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.ExpiresAt.IsZero() {
		return errors.New("expires_at required")
	}
	if !req.ExpiresAt.After(time.Now()) {
		return errors.New("expires_at must be in the future")
	}
	if req.StartsAt != nil && !req.StartsAt.Before(req.ExpiresAt) {
		return errors.New("starts_at must be before expires_at")
	}
	seenUsers := map[int64]bool{}
	for _, userID := range req.UserIDs {
		if seenUsers[userID] {
			continue
		}
		seenUsers[userID] = true
		if _, err := s.store.GetUser(ctx, userID); err != nil {
			return fmt.Errorf("user_id %d: %w", userID, err)
		}
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	nodes, err := core.BuildAssignableNodeCatalog(core.AssignableNodeCatalogInput{
		Servers:           data.Servers,
		Inbounds:          data.Inbounds,
		ProxyPaths:        data.ProxyPaths,
		ProxyPathSteps:    data.ProxyPathSteps,
		EgressResults:     data.ProxyPathEgressResults,
		ExternalOutbounds: data.ExternalOutbounds,
		ServerOnline:      serverOnlineMap(data),
	})
	if err != nil {
		return err
	}
	assignable := map[string]bool{}
	for _, node := range nodes {
		assignable[node.Key] = true
	}
	for _, node := range req.Nodes {
		if !assignable[core.NodeKeyOf(node.NodeType, node.NodeID)] {
			return fmt.Errorf("node %s is not assignable", core.NodeKeyOf(node.NodeType, node.NodeID))
		}
	}
	return nil
}

// userNodeExceptionsBatch serves POST /api/v1/user-node-exceptions/batch/{preview,apply}.
func (s *Server) userNodeExceptionsBatch(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/user-node-exceptions/batch/")
	if len(parts) != 1 || r.Method != http.MethodPost {
		method(w)
		return
	}
	var req batchUserExceptionRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.validateBatchUserExceptionRequest(r.Context(), &req); err != nil {
		fail(w, err, 400)
		return
	}
	existing, err := s.store.ListUserNodeExceptions(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	outcome, err := s.planBatchUserExceptions(r.Context(), req, existing)
	if err != nil {
		fail(w, err, 400)
		return
	}
	affectedUsers := map[int64]bool{}
	for _, item := range append(append([]model.UserNodeException{}, outcome.Created...), outcome.Updated...) {
		affectedUsers[item.UserID] = true
	}
	switch parts[0] {
	case "preview":
		write(w, 200, map[string]any{
			"created":                    len(outcome.Created),
			"updated":                    len(outcome.Updated),
			"skipped":                    len(outcome.Skipped),
			"skipped_items":              outcome.Skipped,
			"affected_users":             len(affectedUsers),
			"effect":                     req.Effect,
			"runtime_authorization_mode": s.authorizationMode(r.Context()),
		})
	case "apply":
		if len(outcome.Created)+len(outcome.Updated) == 0 {
			write(w, 200, map[string]any{
				"created": 0, "updated": 0, "skipped": len(outcome.Skipped),
				"access_change_id": nil, "access_change_status": "none",
				"runtime_authorization_mode": s.authorizationMode(r.Context()),
			})
			return
		}
		writes := make([]store.UserNodeExceptionWrite, 0, len(outcome.Created)+len(outcome.Updated))
		for _, item := range append(append([]model.UserNodeException{}, outcome.Created...), outcome.Updated...) {
			writes = append(writes, store.UserNodeExceptionWrite{
				ID: item.ID, UserID: item.UserID, NodeType: item.NodeType, NodeID: item.NodeID,
				Effect: item.Effect, Reason: item.Reason, Status: item.Status,
				StartsAt: item.StartsAt, ExpiresAt: item.ExpiresAt,
			})
		}
		written, err := s.store.ApplyUserNodeExceptionBatch(r.Context(), writes)
		if err != nil {
			fail(w, err, 409)
			return
		}
		before := existing
		after := append([]model.UserNodeException{}, before...)
		ids := []int64{}
		for i := range written {
			item := written[i]
			item.Status = model.UserNodeExceptionActive
			after = append(after, item)
			ids = append(ids, item.ID)
		}
		config, err := s.store.FullRoutingConfigData(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		now := time.Now()
		at := effectiveWindow(now, req.StartsAt, &req.ExpiresAt)
		targetStatus := model.UserNodeExceptionStatus("")
		if req.Effect == model.UserNodeExceptionAllow {
			targetStatus = model.UserNodeExceptionActive
		}
		change, err := s.createExceptionChanges(r.Context(), r, config, before, after, ids, targetStatus, len(affectedUsers), at)
		if err != nil {
			fail(w, err, 500)
			return
		}
		for _, id := range ids {
			if err := s.store.SetUserNodeExceptionChange(r.Context(), id, change.ID); err != nil {
				fail(w, err, 500)
				return
			}
		}
		auditReq(s, r, "batch", "user-node-exception", fmt.Sprintf("effect=%s users=%d nodes=%d created=%d updated=%d skipped=%d change=%d", req.Effect, len(affectedUsers), len(req.Nodes), len(outcome.Created), len(outcome.Updated), len(outcome.Skipped), change.ID))
		write(w, 200, map[string]any{
			"created": len(outcome.Created), "updated": len(outcome.Updated), "skipped": len(outcome.Skipped),
			"affected_users":   len(affectedUsers),
			"access_change_id": change.ID, "access_change_status": change.Status, "queued_tasks": len(change.Targets),
			"runtime_authorization_mode": s.authorizationMode(r.Context()),
		})
	default:
		fail(w, errors.New("expected /api/v1/user-node-exceptions/batch/preview or /apply"), 404)
	}
}
