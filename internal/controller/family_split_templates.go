package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) familySplitTemplates(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/family-split-templates/")
	switch r.Method {
	case http.MethodGet:
		if id > 0 {
			item, err := s.store.GetFamilySplitTemplate(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"family_split_template": item})
			return
		}
		items, err := s.store.ListFamilySplitTemplates(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"family_split_templates": items})
	case http.MethodPost:
		var v model.FamilySplitTemplate
		if !decode(w, r, &v) {
			return
		}
		ipv4Secret, err := randomFamilyBranchSecret()
		if err != nil {
			fail(w, err, 500)
			return
		}
		ipv6Secret, err := randomFamilyBranchSecret()
		if err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.CreateFamilySplitTemplate(r.Context(), &v, ipv4Secret, ipv6Secret); err != nil {
			status := 400
			if !strings.Contains(err.Error(), "名称") && !strings.Contains(err.Error(), "required") {
				status = 500
			}
			fail(w, err, status)
			return
		}
		auditReq(s, r, "create", "family_split_template", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"family_split_template": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetFamilySplitTemplate(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		var patch struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &patch) {
			return
		}
		if strings.TrimSpace(patch.Name) != "" {
			current.Name = patch.Name
		}
		if err := s.store.UpdateFamilySplitTemplate(r.Context(), current); err != nil {
			fail(w, err, 400)
			return
		}
		write(w, 200, map[string]any{"family_split_template": current})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteFamilySplitTemplate(r.Context(), id); err != nil {
			status := 500
			if errors.Is(err, sql.ErrNoRows) {
				status = 404
			} else if strings.Contains(err.Error(), "引用") {
				status = 409
			}
			fail(w, err, status)
			return
		}
		auditReq(s, r, "delete", "family_split_template", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func randomFamilyBranchSecret() (string, error) {
	return security.RandomToken(24)
}

func (s *Server) syncFamilySplitTemplateEnabled(ctx context.Context, templateID int64) error {
	if templateID <= 0 {
		return nil
	}
	count, err := s.store.CountEnabledFamilySplitTemplateReferences(ctx, templateID)
	if err != nil {
		return err
	}
	return s.store.SetFamilyBranchPathsEnabled(ctx, templateID, count > 0)
}

func (s *Server) syncFamilySplitTemplatesForRules(ctx context.Context, rules ...model.RoutingRule) {
	seen := map[int64]bool{}
	for _, rule := range rules {
		if rule.FamilySplitTemplateID == nil || *rule.FamilySplitTemplateID <= 0 || seen[*rule.FamilySplitTemplateID] {
			continue
		}
		seen[*rule.FamilySplitTemplateID] = true
		_ = s.syncFamilySplitTemplateEnabled(ctx, *rule.FamilySplitTemplateID)
	}
}

func (s *Server) validateFamilyBranchGraft(ctx context.Context, sourcePathID int64, sourceStageStepID *int64, templateID, ruleID int64) error {
	sourceServer, err := s.routingRuleStageServer(ctx, sourcePathID, sourceStageStepID)
	if err != nil {
		return err
	}
	if sourceServer.Status != model.ServerOnline {
		return fmt.Errorf("family split decision server %s is offline", sourceServer.Name)
	}
	if strings.TrimSpace(sourceServer.AgentID) != "" && !serverHasKernelCapability(*sourceServer, "family_selector_v1") {
		return fmt.Errorf("family split decision server %s 的内核缺少 family_selector_v1 能力；请先更新 Agent/内核", sourceServer.Name)
	}
	template, err := s.store.GetFamilySplitTemplate(ctx, templateID)
	if err != nil {
		return fmt.Errorf("family_split_template %d: %w", templateID, err)
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return err
	}
	ipv4Path, ipv6Path, err := core.FamilySplitTemplatePaths(paths, template.ID)
	if err != nil {
		return err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return err
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	for _, branch := range []struct {
		family string
		path   model.ProxyPath
	}{
		{family: "ipv4", path: ipv4Path},
		{family: "ipv6", path: ipv6Path},
	} {
		steps, err := s.store.ListProxyPathStepsForPath(ctx, branch.path.ID)
		if err != nil {
			return err
		}
		if err := core.ValidateFamilyBranchTransport(steps); err != nil {
			return fmt.Errorf("%s family branch: %w", branch.family, err)
		}
		remaining := core.CollapseFamilyBranchSteps(sourceServer.ID, steps, inboundByID)
		if len(remaining) == 0 {
			continue
		}
		first := remaining[0]
		mode := first.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if first.NodeType == model.ProxyPathStepWARP || first.NodeType == model.ProxyPathStepImported {
			continue
		}
		if mode == model.ProxyPathTransportTunnel {
			continue
		}
		if mode != model.ProxyPathTransportSingBox || first.NodeType != model.ProxyPathStepServerInbound {
			return fmt.Errorf("%s family branch must enter a controlled server through a sing-box hop", branch.family)
		}
		if err := s.validateFamilyBranchFirstHopReachability(ctx, first, branch.family, *sourceServer); err != nil {
			return fmt.Errorf("%s family branch: %w", branch.family, err)
		}
	}
	_ = ruleID
	return nil
}

func (s *Server) validateFamilyBranchFirstHopReachability(ctx context.Context, step model.ProxyPathStep, family string, sourceServer model.Server) error {
	var inbound model.Inbound
	var targetServerID int64
	if step.InboundID != nil && *step.InboundID > 0 {
		item, err := s.store.GetInbound(ctx, *step.InboundID)
		if err != nil {
			return fmt.Errorf("target inbound %d: %w", *step.InboundID, err)
		}
		if !item.Enabled {
			return errors.New("target inbound is disabled")
		}
		inbound = *item
		targetServerID = item.ServerID
	} else if step.ServerID != nil && *step.ServerID > 0 {
		targetServerID = *step.ServerID
	} else {
		return errors.New("target controlled server is missing")
	}
	targetServer, err := s.store.GetServer(ctx, targetServerID)
	if err != nil {
		return err
	}
	if targetServer.Status != model.ServerOnline {
		return fmt.Errorf("target server %s is offline", targetServer.Name)
	}
	if inbound.ID == 0 {
		inbound = model.Inbound{ServerID: targetServer.ID, ListenIP: targetServer.ListenIP, EntryIPMode: model.EntryIPModeAuto, Enabled: true}
	}
	_, err = core.ResolveReachableEntryAddressForFamily(sourceServer, inbound, *targetServer, family)
	return err
}
