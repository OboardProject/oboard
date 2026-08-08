package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) nodeOrderTemplates(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/node-order-templates/")
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/node-order-templates") {
		parts = nil
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := s.store.ListNodeOrderTemplates(r.Context(), strings.EqualFold(r.URL.Query().Get("include_archived"), "true") || r.URL.Query().Get("include_archived") == "1")
			if err != nil {
				fail(w, err, 500)
				return
			}
			views, err := s.nodeOrderTemplateViews(r.Context(), items)
			if err != nil {
				fail(w, err, 500)
				return
			}
			write(w, 200, map[string]any{"templates": views})
		case http.MethodPost:
			s.createNodeOrderTemplate(w, r)
		default:
			method(w)
		}
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		fail(w, errors.New("invalid template id"), 400)
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, err := s.store.GetNodeOrderTemplate(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			views, err := s.nodeOrderTemplateViews(r.Context(), []model.NodeOrderTemplate{*item})
			if err != nil {
				fail(w, err, 500)
				return
			}
			write(w, 200, map[string]any{"template": views[0]})
		case http.MethodPatch:
			s.updateNodeOrderTemplate(w, r, id)
		default:
			method(w)
		}
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		method(w)
		return
	}
	switch parts[1] {
	case "clone":
		s.cloneNodeOrderTemplate(w, r, id)
	case "archive":
		s.archiveNodeOrderTemplate(w, r, id)
	case "preview":
		s.previewNodeOrderTemplate(w, r, id)
	default:
		fail(w, errors.New("unknown template action"), 404)
	}
}

type nodeOrderTemplateRequest struct {
	Name             string                        `json:"name"`
	Description      string                        `json:"description"`
	Policy           model.NodeOrderTemplatePolicy `json:"policy"`
	ExpectedRevision int64                         `json:"expected_revision"`
}

type nodeOrderTemplateView struct {
	model.NodeOrderTemplate
	Warnings []string `json:"warnings"`
}

func (s *Server) nodeOrderTemplateViews(ctx context.Context, items []model.NodeOrderTemplate) ([]nodeOrderTemplateView, error) {
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(inbounds))
	for _, inbound := range inbounds {
		existing[core.NodeKeyOf(model.AssignableNodeInbound, inbound.ID)] = true
	}
	views := make([]nodeOrderTemplateView, 0, len(items))
	for _, item := range items {
		view := nodeOrderTemplateView{NodeOrderTemplate: item, Warnings: []string{}}
		for _, key := range item.Policy.EntryOrder {
			if !existing[key] {
				view.Warnings = append(view.Warnings, fmt.Sprintf("入口 %s 已不存在", key))
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func validateNodeOrderTemplateRequest(req *nodeOrderTemplateRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	if req.Name == "" {
		return errors.New("template name is required")
	}
	if len([]rune(req.Name)) > 100 || len([]rune(req.Description)) > 500 {
		return errors.New("template name or description is too long")
	}
	policy, err := core.ValidateNodeOrderTemplatePolicy(req.Policy)
	if err != nil {
		return err
	}
	req.Policy = policy
	return nil
}

func (s *Server) validateNodeOrderTemplateEntries(ctx context.Context, policy model.NodeOrderTemplatePolicy) error {
	if len(policy.EntryOrder) == 0 {
		return nil
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(inbounds))
	for _, inbound := range inbounds {
		existing[core.NodeKeyOf(model.AssignableNodeInbound, inbound.ID)] = true
	}
	for _, key := range policy.EntryOrder {
		if !existing[key] {
			return fmt.Errorf("entry key %q does not exist", key)
		}
	}
	return nil
}

func requestActorID(r *http.Request) *int64 {
	if user, ok := r.Context().Value(userKey).(*model.User); ok && user != nil {
		return &user.ID
	}
	return nil
}

func (s *Server) createNodeOrderTemplate(w http.ResponseWriter, r *http.Request) {
	var req nodeOrderTemplateRequest
	if !decode(w, r, &req) {
		return
	}
	if err := validateNodeOrderTemplateRequest(&req); err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.validateNodeOrderTemplateEntries(r.Context(), req.Policy); err != nil {
		fail(w, err, 400)
		return
	}
	actorID := requestActorID(r)
	item := model.NodeOrderTemplate{Name: req.Name, Description: req.Description, Enabled: true, Policy: req.Policy, CreatedBy: actorID, UpdatedBy: actorID}
	if err := s.store.CreateNodeOrderTemplate(r.Context(), &item); err != nil {
		fail(w, err, 400)
		return
	}
	auditReq(s, r, "node_order_template.create", "node-order-template", strconv.FormatInt(item.ID, 10))
	write(w, http.StatusCreated, map[string]any{"template": item})
}

func (s *Server) updateNodeOrderTemplate(w http.ResponseWriter, r *http.Request, id int64) {
	var req nodeOrderTemplateRequest
	if !decode(w, r, &req) {
		return
	}
	if req.ExpectedRevision <= 0 {
		fail(w, errors.New("expected_revision is required"), 400)
		return
	}
	if err := validateNodeOrderTemplateRequest(&req); err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.validateNodeOrderTemplateEntries(r.Context(), req.Policy); err != nil {
		fail(w, err, 400)
		return
	}
	item, err := s.store.UpdateNodeOrderTemplate(r.Context(), id, req.ExpectedRevision, req.Name, req.Description, req.Policy, requestActorID(r))
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrNodeOrderTemplateConflict) {
			status = 409
		} else if errors.Is(err, sql.ErrNoRows) {
			status = 404
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "node_order_template.update", "node-order-template", strconv.FormatInt(id, 10))
	write(w, 200, map[string]any{"template": item})
}

func (s *Server) cloneNodeOrderTemplate(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	source, err := s.store.GetNodeOrderTemplate(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = source.Name + " 副本"
	}
	actorID := requestActorID(r)
	item := model.NodeOrderTemplate{Name: name, Description: source.Description, Enabled: true, Policy: source.Policy, CreatedBy: actorID, UpdatedBy: actorID}
	if err := s.store.CreateNodeOrderTemplate(r.Context(), &item); err != nil {
		fail(w, err, 400)
		return
	}
	auditReq(s, r, "node_order_template.clone", "node-order-template", fmt.Sprintf("%d->%d", id, item.ID))
	write(w, http.StatusCreated, map[string]any{"template": item})
}

func (s *Server) archiveNodeOrderTemplate(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &req) {
		return
	}
	item, err := s.store.ArchiveNodeOrderTemplate(r.Context(), id, req.ExpectedRevision, requestActorID(r))
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrNodeOrderTemplateConflict) {
			status = 409
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "node_order_template.archive", "node-order-template", strconv.FormatInt(id, 10))
	write(w, 200, map[string]any{"template": item})
}

func (s *Server) previewNodeOrderTemplate(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		PlanID int64                          `json:"plan_id"`
		Policy *model.NodeOrderTemplatePolicy `json:"policy,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	template, err := s.store.GetNodeOrderTemplate(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if req.Policy != nil {
		policy, validateErr := core.ValidateNodeOrderTemplatePolicy(*req.Policy)
		if validateErr != nil {
			fail(w, validateErr, 400)
			return
		}
		if validateErr := s.validateNodeOrderTemplateEntries(r.Context(), policy); validateErr != nil {
			fail(w, validateErr, 400)
			return
		}
		template.Policy = policy
	}
	plan, err := s.store.GetSubscriptionPlan(r.Context(), req.PlanID)
	if err != nil || plan.LatestRevisionID == 0 {
		fail(w, sql.ErrNoRows, 404)
		return
	}
	revision, nodes, err := s.store.GetPlanRevisionOrdering(r.Context(), plan.ID, plan.LatestRevisionID)
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
	before, err := core.BuildOrderingNodes(nodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, revision.NodeOrderPolicy, nameOptions)
	if err != nil {
		fail(w, err, 500)
		return
	}
	policy := core.SubscriptionPolicyFromTemplate(template.Policy, template.Policy.BaseMode)
	after, err := core.BuildOrderingNodes(nodes, config.Servers, config.Inbounds, config.ProxyPaths, config.ProxyPathSteps, config.ProxyPathEgressResults, config.ExternalOutbounds, policy, nameOptions)
	if err != nil {
		fail(w, err, 500)
		return
	}
	oldPosition := map[string]int{}
	for index, node := range before {
		oldPosition[node.Key] = index
	}
	views := make([]map[string]any, 0, len(after))
	matched := 0
	for index, node := range after {
		bucket, ok := core.SubscriptionNodeTemplateBucket(node, policy)
		if ok {
			matched++
		}
		views = append(views, map[string]any{"node_key": node.Key, "name": node.Name, "old_position": oldPosition[node.Key], "new_position": index, "bucket": bucket, "matched": ok})
	}
	warnings := []string{}
	if unmatched := len(after) - matched; unmatched > 0 {
		warnings = append(warnings, fmt.Sprintf("%d 个节点无法匹配模板", unmatched))
	}
	write(w, 200, map[string]any{"nodes": views, "warnings": warnings, "summary": map[string]int{"total": len(after), "matched": matched, "unmatched": len(after) - matched}})
}
