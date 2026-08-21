package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

// registerTrafficAutomationOperations wires the outbound, routing-rule,
// external-outbound, and WARP capability operations of the MCP automation
// layer. They mirror the panel's 出口 / 分流规则 / 导入节点 behavior.

func (s *Server) registerTrafficAutomationOperations() {
	s.registerOutboundOperations()
	s.registerRoutingRuleOperations()
	s.registerRoutingRuleSetOperations()
	s.registerExternalOutboundOperations()
}

// ---- outbounds ----

var outboundAutomationFields = map[string]bool{
	"server_id": true, "next_server_id": true, "name": true, "protocol": true,
	"target_address": true, "target_port": true, "config_json": true, "enabled": true,
}

func (s *Server) registerOutboundOperations() {
	register := func(name string) {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			outbound, changed, err := s.outboundAutomationCandidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			return automationOutboundResult(outbound, changed)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.outboundAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyOutboundOperation(ctx, principal, input, name)
		})
	}
	register("outbounds.create")
	register("outbounds.update")
	register("outbounds.delete")
}

type outboundOperationInput struct {
	Outbound   json.RawMessage `json:"outbound,omitempty"`
	OutboundID int64           `json:"outbound_id,omitempty"`
	Changes    json.RawMessage `json:"changes,omitempty"`
	Confirm    bool            `json:"confirm,omitempty"`
}

func (s *Server) outboundAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (model.Outbound, []string, error) {
	var request outboundOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Outbound{}, nil, err
	}
	switch name {
	case "outbounds.create":
		if len(request.Outbound) == 0 {
			return model.Outbound{}, nil, errors.New("outbound object is required")
		}
		fields, err := decodeClosedAutomationFields(request.Outbound, outboundAutomationFields, "outbound")
		if err != nil {
			return model.Outbound{}, nil, err
		}
		if _, ok := fields["server_id"]; !ok {
			return model.Outbound{}, nil, errors.New("outbound.server_id is required")
		}
		if _, ok := fields["name"]; !ok {
			return model.Outbound{}, nil, errors.New("outbound.name is required")
		}
		if _, ok := fields["protocol"]; !ok {
			return model.Outbound{}, nil, errors.New("outbound.protocol is required")
		}
		if _, ok := fields["target_address"]; !ok {
			return model.Outbound{}, nil, errors.New("outbound.target_address is required")
		}
		if _, ok := fields["target_port"]; !ok {
			return model.Outbound{}, nil, errors.New("outbound.target_port is required")
		}
		var outbound model.Outbound
		if err := json.Unmarshal(request.Outbound, &outbound); err != nil {
			return model.Outbound{}, nil, err
		}
		if err := s.normalizeOutboundAutomationCandidate(ctx, principal, &outbound); err != nil {
			return model.Outbound{}, nil, err
		}
		return outbound, []string{"server_id", "name", "protocol", "target_address", "target_port"}, nil
	case "outbounds.update":
		if request.OutboundID <= 0 || len(request.Changes) == 0 {
			return model.Outbound{}, nil, errors.New("outbound_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, outboundAutomationFields, "changes")
		if err != nil {
			return model.Outbound{}, nil, err
		}
		if len(fields) == 0 {
			return model.Outbound{}, nil, errors.New("changes must contain at least one outbound field")
		}
		current, err := s.store.GetOutbound(ctx, request.OutboundID)
		if err != nil {
			return model.Outbound{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.ServerID) {
			return model.Outbound{}, nil, errors.New("outbound is outside the authorized server boundary")
		}
		var patch model.Outbound
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.Outbound{}, nil, err
		}
		merged := mergeOutboundPatch(*current, patch, fields)
		merged.ID = current.ID
		changed := make([]string, 0, len(fields))
		for field := range fields {
			changed = append(changed, field)
		}
		if err := s.normalizeOutboundAutomationCandidate(ctx, principal, &merged); err != nil {
			return model.Outbound{}, nil, err
		}
		return merged, changed, nil
	case "outbounds.delete":
		var deleteRequest struct {
			OutboundID int64 `json:"outbound_id"`
			Confirm    bool  `json:"confirm"`
		}
		if err := strictAutomationInput(input, &deleteRequest); err != nil {
			return model.Outbound{}, nil, err
		}
		if deleteRequest.OutboundID <= 0 || !deleteRequest.Confirm {
			return model.Outbound{}, nil, errors.New("outbound_id and confirm=true are required")
		}
		current, err := s.store.GetOutbound(ctx, deleteRequest.OutboundID)
		if err != nil {
			return model.Outbound{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.ServerID) {
			return model.Outbound{}, nil, errors.New("outbound is outside the authorized server boundary")
		}
		if err := s.guardOutboundReferences(ctx, current.ID); err != nil {
			return model.Outbound{}, nil, err
		}
		return *current, nil, nil
	default:
		return model.Outbound{}, nil, errors.New("unsupported outbound operation")
	}
}

func (s *Server) normalizeOutboundAutomationCandidate(ctx context.Context, principal application.Principal, outbound *model.Outbound) error {
	if !principal.AllowsInt64("server_ids", outbound.ServerID) {
		return errors.New("outbound server is outside the authorized server boundary")
	}
	if outbound.ConfigJSON == "" {
		outbound.ConfigJSON = "{}"
	}
	normalized, err := applyProtocolAuthDefaults(outbound.Protocol, outbound.ConfigJSON)
	if err != nil {
		return err
	}
	outbound.ConfigJSON = normalized
	if err := normalizeMieruOutboundPorts(outbound.Protocol, &outbound.TargetPort, &outbound.ConfigJSON); err != nil {
		return err
	}
	if err := validateOutbound(*outbound); err != nil {
		return err
	}
	ids := []int64{outbound.ServerID}
	if outbound.NextServerID != nil {
		ids = append(ids, *outbound.NextServerID)
	}
	if err := s.store.ValidateServerExists(ctx, ids...); err != nil {
		return err
	}
	return s.validateOutboundAddress(ctx, *outbound)
}

func (s *Server) guardOutboundReferences(ctx context.Context, outboundID int64) error {
	rules, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.OutboundID != nil && *rule.OutboundID == outboundID {
			return fmt.Errorf("outbound %d is referenced by routing rule %d", outboundID, rule.ID)
		}
	}
	return nil
}

func mergeOutboundPatch(current model.Outbound, patch model.Outbound, fields map[string]json.RawMessage) model.Outbound {
	merged := current
	if _, ok := fields["server_id"]; ok {
		merged.ServerID = patch.ServerID
	}
	if _, ok := fields["next_server_id"]; ok {
		merged.NextServerID = patch.NextServerID
	}
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["protocol"]; ok {
		merged.Protocol = patch.Protocol
	}
	if _, ok := fields["target_address"]; ok {
		merged.TargetAddress = patch.TargetAddress
	}
	if _, ok := fields["target_port"]; ok {
		merged.TargetPort = patch.TargetPort
	}
	if _, ok := fields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	return merged
}

func (s *Server) outboundAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	outbound, _, err := s.outboundAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	if name == "outbounds.create" {
		server, err := s.application.GetServer(ctx, principal, outbound.ServerID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.Revision}, nil
	}
	return map[string]string{"outbound:" + strconv.FormatInt(outbound.ID, 10): outbound.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) applyOutboundOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	outbound, changed, err := s.outboundAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "outbounds.create":
		if err := s.store.CreateOutbound(ctx, &outbound); err != nil {
			return nil, err
		}
	case "outbounds.update":
		if err := s.store.UpdateOutbound(ctx, &outbound); err != nil {
			return nil, err
		}
	case "outbounds.delete":
		if err := s.store.Delete(ctx, "outbounds", outbound.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "outbound_id": outbound.ID}, nil
	}
	return automationOutboundResult(outbound, changed)
}

func automationOutboundResult(outbound model.Outbound, changed []string) (any, error) {
	view := map[string]any{
		"id": outbound.ID, "revision": outbound.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"server_id": outbound.ServerID, "next_server_id": outbound.NextServerID, "name": outbound.Name,
		"protocol": outbound.Protocol, "target_address": outbound.TargetAddress, "target_port": outbound.TargetPort,
		"advanced_configured": strings.TrimSpace(outbound.ConfigJSON) != "" && strings.TrimSpace(outbound.ConfigJSON) != "{}",
		"enabled":             outbound.Enabled, "created_at": outbound.CreatedAt, "updated_at": outbound.UpdatedAt,
	}
	if len(changed) == 0 {
		return map[string]any{"outbound": view}, nil
	}
	return map[string]any{"outbound": view, "changed_fields": changed}, nil
}

// ---- routing rules ----

var routingRuleAutomationFields = map[string]bool{
	"server_id": true, "name": true, "priority": true, "match_json": true, "action": true,
	"outbound_id": true, "external_outbound_id": true,
	"target_proxy_path_id": true, "sync_source_rule_id": true, "sync_enabled": true,
	"interface_name": true, "source_prefix": true, "enabled": true, "scope": true, "proxy_path_id": true,
	"stage_step_id": true, "sort_position": true, "match_source": true, "rule_set_id": true, "dns_resolver": true,
}

func (s *Server) registerRoutingRuleOperations() {
	for _, name := range []string{"routing_rules.create", "routing_rules.update", "routing_rules.delete"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			rule, changed, err := s.routingRuleAutomationCandidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			return automationRoutingRuleResult(rule, changed)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.routingRuleAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyRoutingRuleOperation(ctx, principal, input, name)
		})
	}
	batchName := "routing_rules.batch_delete"
	s.automation.RegisterValidator(batchName, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		ids, err := s.validateRoutingRuleBatchDelete(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"routing_rule_ids": ids, "deleted": false}, nil
	})
	s.automation.RegisterRevisionResolver(batchName, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		ids, err := s.validateRoutingRuleBatchDelete(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		revisions := make(map[string]string, len(ids))
		for _, id := range ids {
			rule, err := s.store.GetRoutingRule(ctx, id)
			if err != nil {
				return nil, err
			}
			revisions["routing_rule:"+strconv.FormatInt(id, 10)] = rule.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		return revisions, nil
	})
	s.automation.Register(batchName, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		ids, err := s.validateRoutingRuleBatchDelete(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteRoutingRules(ctx, ids); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "routing_rule_ids": ids}, nil
	})
	name := "routing_rules.place"
	s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := s.validateRoutingRulePlacement(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return request, nil
	})
	s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := s.validateRoutingRulePlacement(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		path, err := s.store.GetProxyPath(ctx, request.ProxyPathID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"proxy_path:" + strconv.FormatInt(path.ID, 10): path.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := s.validateRoutingRulePlacement(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		before, err := s.routingRuleServerIDsForPath(ctx, request.ProxyPathID)
		if err != nil {
			return nil, err
		}
		if err := s.store.PlaceRoutingRules(ctx, request.ProxyPathID, request.Placements); err != nil {
			return nil, err
		}
		after, err := s.routingRuleServerIDsForPath(ctx, request.ProxyPathID)
		if err != nil {
			return nil, err
		}
		for id := range after {
			before[id] = true
		}
		serverIDs := make([]int64, 0, len(before))
		for id := range before {
			serverIDs = append(serverIDs, id)
		}
		if err := s.queueCoreConfigRefreshForServers(ctx, serverIDs, "routing_rules_placed"); err != nil {
			return nil, err
		}
		return map[string]any{"proxy_path_id": request.ProxyPathID, "placements": request.Placements}, nil
	})
}

func (s *Server) validateRoutingRuleBatchDelete(ctx context.Context, principal application.Principal, input json.RawMessage) ([]int64, error) {
	var request struct {
		RoutingRuleIDs []int64 `json:"routing_rule_ids"`
		Confirm        bool    `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if len(request.RoutingRuleIDs) == 0 || len(request.RoutingRuleIDs) > 256 || !request.Confirm {
		return nil, errors.New("routing_rule_ids and confirm=true are required")
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(request.RoutingRuleIDs))
	for _, id := range request.RoutingRuleIDs {
		if id <= 0 || seen[id] {
			return nil, errors.New("routing_rule_ids must contain unique positive IDs")
		}
		seen[id] = true
		rule, err := s.store.GetRoutingRule(ctx, id)
		if err != nil {
			return nil, err
		}
		if !principal.AllowsInt64("server_ids", rule.ServerID) {
			return nil, errors.New("routing rule is outside the authorized server boundary")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type routingRulePlacementInput struct {
	ProxyPathID int64                        `json:"proxy_path_id"`
	Placements  []model.RoutingRulePlacement `json:"placements"`
}

func (s *Server) validateRoutingRulePlacement(ctx context.Context, principal application.Principal, input json.RawMessage) (routingRulePlacementInput, error) {
	var request routingRulePlacementInput
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	if request.ProxyPathID <= 0 || len(request.Placements) == 0 || len(request.Placements) > 512 {
		return request, errors.New("proxy_path_id and 1-512 placements are required")
	}
	if !principal.AllowsInt64("proxy_path_ids", request.ProxyPathID) {
		return request, errors.New("proxy path is outside the authorized boundary")
	}
	path, err := s.store.GetProxyPath(ctx, request.ProxyPathID)
	if err != nil {
		return request, err
	}
	root, err := s.store.GetInbound(ctx, path.InboundID)
	if err != nil {
		return request, err
	}
	if err := s.validateRoutingRulePlacements(ctx, request.ProxyPathID, request.Placements); err != nil {
		return request, err
	}
	for _, placement := range request.Placements {
		rule, err := s.store.GetRoutingRule(ctx, placement.RuleID)
		if err != nil {
			return request, err
		}
		if !principal.AllowsInt64("server_ids", rule.ServerID) {
			return request, errors.New("routing rule placement is outside the authorized server boundary")
		}
		targetServerID := root.ServerID
		if placement.StageStepID != nil {
			step, err := s.store.GetProxyPathStep(ctx, *placement.StageStepID)
			if err != nil {
				return request, err
			}
			if step.PathID != request.ProxyPathID || step.NodeType != model.ProxyPathStepServerInbound || step.ServerID == nil {
				return request, errors.New("routing rule placement targets an invalid controlled stage")
			}
			targetServerID = *step.ServerID
		}
		if !principal.AllowsInt64("server_ids", targetServerID) {
			return request, errors.New("routing rule target stage is outside the authorized server boundary")
		}
	}
	return request, nil
}

type routingRuleOperationInput struct {
	RoutingRule   json.RawMessage `json:"routing_rule,omitempty"`
	RoutingRuleID int64           `json:"routing_rule_id,omitempty"`
	Changes       json.RawMessage `json:"changes,omitempty"`
	Confirm       bool            `json:"confirm,omitempty"`
}

func (s *Server) routingRuleAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (model.RoutingRule, []string, error) {
	var request routingRuleOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.RoutingRule{}, nil, err
	}
	switch name {
	case "routing_rules.create":
		if len(request.RoutingRule) == 0 {
			return model.RoutingRule{}, nil, errors.New("routing_rule object is required")
		}
		fields, err := decodeClosedAutomationFields(request.RoutingRule, routingRuleAutomationFields, "routing_rule")
		if err != nil {
			return model.RoutingRule{}, nil, err
		}
		if _, serverOK := fields["server_id"]; !serverOK {
			if _, pathOK := fields["proxy_path_id"]; !pathOK {
				return model.RoutingRule{}, nil, errors.New("routing_rule.server_id or proxy_path_id is required")
			}
		}
		if _, ok := fields["name"]; !ok {
			if _, reuse := fields["sync_source_rule_id"]; !reuse {
				return model.RoutingRule{}, nil, errors.New("routing_rule.name is required")
			}
		}
		var rule model.RoutingRule
		if err := json.Unmarshal(request.RoutingRule, &rule); err != nil {
			return model.RoutingRule{}, nil, err
		}
		if _, err := s.prepareRoutingRuleReuse(ctx, &rule); err != nil {
			return model.RoutingRule{}, nil, err
		}
		if err := s.validateRoutingRuleAutomationCandidate(ctx, principal, &rule); err != nil {
			return model.RoutingRule{}, nil, err
		}
		return rule, nil, nil
	case "routing_rules.update":
		if request.RoutingRuleID <= 0 || len(request.Changes) == 0 {
			return model.RoutingRule{}, nil, errors.New("routing_rule_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, routingRuleAutomationFields, "changes")
		if err != nil {
			return model.RoutingRule{}, nil, err
		}
		if len(fields) == 0 {
			return model.RoutingRule{}, nil, errors.New("changes must contain at least one routing rule field")
		}
		if _, ok := fields["sync_source_rule_id"]; ok {
			return model.RoutingRule{}, nil, errors.New("sync_source_rule_id is create-only")
		}
		if _, ok := fields["sync_enabled"]; ok {
			return model.RoutingRule{}, nil, errors.New("sync_enabled is create-only")
		}
		current, err := s.store.GetRoutingRule(ctx, request.RoutingRuleID)
		if err != nil {
			return model.RoutingRule{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.ServerID) {
			return model.RoutingRule{}, nil, errors.New("routing rule is outside the authorized server boundary")
		}
		var patch model.RoutingRule
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.RoutingRule{}, nil, err
		}
		merged := mergeRoutingRulePatch(*current, patch, fields)
		merged.ID = current.ID
		changed := make([]string, 0, len(fields))
		for field := range fields {
			changed = append(changed, field)
		}
		if err := s.validateRoutingRuleAutomationCandidate(ctx, principal, &merged); err != nil {
			return model.RoutingRule{}, nil, err
		}
		return merged, changed, nil
	case "routing_rules.delete":
		var deleteRequest struct {
			RoutingRuleID int64 `json:"routing_rule_id"`
			Confirm       bool  `json:"confirm"`
		}
		if err := strictAutomationInput(input, &deleteRequest); err != nil {
			return model.RoutingRule{}, nil, err
		}
		if deleteRequest.RoutingRuleID <= 0 || !deleteRequest.Confirm {
			return model.RoutingRule{}, nil, errors.New("routing_rule_id and confirm=true are required")
		}
		current, err := s.store.GetRoutingRule(ctx, deleteRequest.RoutingRuleID)
		if err != nil {
			return model.RoutingRule{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.ServerID) {
			return model.RoutingRule{}, nil, errors.New("routing rule is outside the authorized server boundary")
		}
		return *current, nil, nil
	default:
		return model.RoutingRule{}, nil, errors.New("unsupported routing rule operation")
	}
}

func (s *Server) validateRoutingRuleAutomationCandidate(ctx context.Context, principal application.Principal, rule *model.RoutingRule) error {
	if err := s.validateRoutingRule(ctx, rule); err != nil {
		return err
	}
	if !principal.AllowsInt64("server_ids", rule.ServerID) {
		return errors.New("routing rule server is outside the authorized server boundary")
	}
	if rule.ProxyPathID != nil && !principal.AllowsInt64("proxy_path_ids", *rule.ProxyPathID) {
		return errors.New("routing rule proxy path is outside the authorized boundary")
	}
	if rule.TargetProxyPathID != nil && !principal.AllowsInt64("proxy_path_ids", *rule.TargetProxyPathID) {
		return errors.New("routing rule target proxy path is outside the authorized boundary")
	}
	if rule.OutboundID != nil {
		outbound, err := s.store.GetOutbound(ctx, *rule.OutboundID)
		if err != nil {
			return fmt.Errorf("outbound %d: %w", *rule.OutboundID, err)
		}
		if !principal.AllowsInt64("server_ids", outbound.ServerID) {
			return errors.New("routing rule references an unauthorized outbound")
		}
	}
	if rule.ExternalOutboundID != nil {
		external, err := s.store.GetExternalOutbound(ctx, *rule.ExternalOutboundID)
		if err != nil {
			return fmt.Errorf("external_outbound %d: %w", *rule.ExternalOutboundID, err)
		}
		if external.ServerID != nil && !principal.AllowsInt64("server_ids", *external.ServerID) {
			return errors.New("routing rule references an unauthorized external outbound")
		}
	}
	return nil
}

func mergeRoutingRulePatch(current model.RoutingRule, patch model.RoutingRule, fields map[string]json.RawMessage) model.RoutingRule {
	merged := current
	if _, ok := fields["server_id"]; ok {
		merged.ServerID = patch.ServerID
	}
	if _, ok := fields["scope"]; ok {
		merged.Scope = patch.Scope
	}
	if _, ok := fields["proxy_path_id"]; ok {
		merged.ProxyPathID = patch.ProxyPathID
	}
	if _, ok := fields["stage_step_id"]; ok {
		merged.StageStepID = patch.StageStepID
	}
	if _, ok := fields["sort_position"]; ok {
		merged.SortPosition = patch.SortPosition
	}
	if _, ok := fields["match_source"]; ok {
		merged.MatchSource = patch.MatchSource
	}
	if _, ok := fields["rule_set_id"]; ok {
		merged.RuleSetID = patch.RuleSetID
	}
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["priority"]; ok {
		merged.Priority = patch.Priority
	}
	if _, ok := fields["match_json"]; ok {
		merged.MatchJSON = patch.MatchJSON
	}
	if _, ok := fields["action"]; ok {
		merged.Action = patch.Action
	}
	if _, ok := fields["outbound_id"]; ok {
		merged.OutboundID = patch.OutboundID
	}
	if _, ok := fields["external_outbound_id"]; ok {
		merged.ExternalOutboundID = patch.ExternalOutboundID
	}
	if _, ok := fields["target_proxy_path_id"]; ok {
		merged.TargetProxyPathID = patch.TargetProxyPathID
	}
	if _, ok := fields["interface_name"]; ok {
		merged.InterfaceName = patch.InterfaceName
	}
	if _, ok := fields["source_prefix"]; ok {
		merged.SourcePrefix = patch.SourcePrefix
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	return merged
}

func (s *Server) routingRuleAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	rule, _, err := s.routingRuleAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	if name == "routing_rules.create" {
		server, err := s.application.GetServer(ctx, principal, rule.ServerID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.Revision}, nil
	}
	return map[string]string{"routing_rule:" + strconv.FormatInt(rule.ID, 10): rule.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) applyRoutingRuleOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	rule, changed, err := s.routingRuleAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "routing_rules.create":
		syncSourceID, err := s.prepareRoutingRuleReuse(ctx, &rule)
		if err != nil {
			return nil, err
		}
		if syncSourceID != 0 {
			groupID, err := security.RandomToken(18)
			if err == nil {
				err = s.store.CreateSyncedRoutingRule(ctx, &rule, syncSourceID, groupID)
			}
			if err != nil {
				return nil, err
			}
		} else if err := s.store.CreateRoutingRule(ctx, &rule); err != nil {
			return nil, err
		}
	case "routing_rules.update":
		if err := s.store.UpdateRoutingRule(ctx, &rule); err != nil {
			return nil, err
		}
	case "routing_rules.delete":
		if err := s.store.Delete(ctx, "routing_rules", rule.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "routing_rule_id": rule.ID}, nil
	}
	return automationRoutingRuleResult(rule, changed)
}

func automationRoutingRuleResult(rule model.RoutingRule, changed []string) (any, error) {
	view := map[string]any{
		"id": rule.ID, "revision": rule.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"server_id": rule.ServerID, "scope": rule.Scope, "proxy_path_id": rule.ProxyPathID, "stage_step_id": rule.StageStepID,
		"sort_position": rule.SortPosition, "match_source": rule.MatchSource, "rule_set_id": rule.RuleSetID, "name": rule.Name, "priority": rule.Priority,
		"action": rule.Action, "outbound_id": rule.OutboundID, "external_outbound_id": rule.ExternalOutboundID,
		"target_proxy_path_id": rule.TargetProxyPathID, "outbound_tag": rule.OutboundTag, "interface_name": rule.InterfaceName, "source_prefix": rule.SourcePrefix,
		"sync_group_id":    rule.SyncGroupID,
		"match_configured": strings.TrimSpace(rule.MatchJSON) != "" && strings.TrimSpace(rule.MatchJSON) != "{}",
		"enabled":          rule.Enabled, "created_at": rule.CreatedAt, "updated_at": rule.UpdatedAt,
	}
	if len(changed) == 0 {
		return map[string]any{"routing_rule": view}, nil
	}
	return map[string]any{"routing_rule": view, "changed_fields": changed}, nil
}

// ---- external outbounds ----

var externalOutboundAutomationFields = map[string]bool{
	"server_id": true, "name": true, "protocol": true, "scope": true, "target_address": true,
	"target_port": true, "config_json": true, "region_mode": true, "region_code": true,
	"expose_to_users": true, "enabled": true,
}

func (s *Server) registerExternalOutboundOperations() {
	for _, name := range []string{"external_outbounds.import", "external_outbounds.create", "external_outbounds.update", "external_outbounds.delete"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			result, err := s.externalOutboundAutomationValidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			return result, nil
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.externalOutboundAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyExternalOutboundOperation(ctx, principal, input, name)
		})
	}
}

type externalOutboundOperationInput struct {
	ExternalOutbound   json.RawMessage `json:"external_outbound,omitempty"`
	ExternalOutboundID int64           `json:"external_outbound_id,omitempty"`
	Changes            json.RawMessage `json:"changes,omitempty"`
	Content            string          `json:"content,omitempty"`
	Scope              string          `json:"scope,omitempty"`
	ServerID           *int64          `json:"server_id,omitempty"`
	ExposeToUsers      bool            `json:"expose_to_users,omitempty"`
	Confirm            bool            `json:"confirm,omitempty"`
}

func (s *Server) externalOutboundAutomationValidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	switch name {
	case "external_outbounds.import":
		var request externalOutboundOperationInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		if strings.TrimSpace(request.Content) == "" {
			return nil, errors.New("content is required")
		}
		if request.ServerID != nil && !principal.AllowsInt64("server_ids", *request.ServerID) {
			return nil, errors.New("server is outside the authorized server boundary")
		}
		items, err := parseExternalOutboundImport(request.Content)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, errors.New("content contained no importable nodes")
		}
		scope := model.ExternalOutboundScope(strings.TrimSpace(request.Scope))
		if scope == "" {
			scope = model.ExternalOutboundScopeGlobal
		}
		count := 0
		for index := range items {
			items[index].ServerID = request.ServerID
			items[index].Scope = scope
			items[index].ExposeToUsers = request.ExposeToUsers
			items[index].Enabled = true
			if err := s.validateExternalOutbound(ctx, &items[index]); err != nil {
				return nil, err
			}
			count++
		}
		views := make([]any, 0, len(items))
		for _, item := range items {
			views = append(views, automationExternalOutboundView(item))
		}
		return map[string]any{"external_outbounds": views, "created_count": count}, nil
	default:
		external, changed, err := s.externalOutboundAutomationCandidate(ctx, principal, input, name)
		if err != nil {
			return nil, err
		}
		return automationExternalOutboundResult(external, changed)
	}
}

func (s *Server) externalOutboundAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (model.ExternalOutbound, []string, error) {
	var request externalOutboundOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.ExternalOutbound{}, nil, err
	}
	switch name {
	case "external_outbounds.create":
		if len(request.ExternalOutbound) == 0 {
			return model.ExternalOutbound{}, nil, errors.New("external_outbound object is required")
		}
		fields, err := decodeClosedAutomationFields(request.ExternalOutbound, externalOutboundAutomationFields, "external_outbound")
		if err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		if _, ok := fields["name"]; !ok {
			return model.ExternalOutbound{}, nil, errors.New("external_outbound.name is required")
		}
		var external model.ExternalOutbound
		if err := json.Unmarshal(request.ExternalOutbound, &external); err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		if err := s.validateExternalOutboundAutomationCandidate(ctx, principal, &external); err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		return external, nil, nil
	case "external_outbounds.update":
		if request.ExternalOutboundID <= 0 || len(request.Changes) == 0 {
			return model.ExternalOutbound{}, nil, errors.New("external_outbound_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, externalOutboundAutomationFields, "changes")
		if err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		if len(fields) == 0 {
			return model.ExternalOutbound{}, nil, errors.New("changes must contain at least one external outbound field")
		}
		current, err := s.store.GetExternalOutbound(ctx, request.ExternalOutboundID)
		if err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		if current.ServerID != nil && !principal.AllowsInt64("server_ids", *current.ServerID) {
			return model.ExternalOutbound{}, nil, errors.New("external outbound is outside the authorized server boundary")
		}
		var patch model.ExternalOutbound
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		merged := mergeExternalOutboundPatch(*current, patch, fields)
		merged.ID = current.ID
		changed := make([]string, 0, len(fields))
		for field := range fields {
			changed = append(changed, field)
		}
		if err := s.validateExternalOutboundAutomationCandidate(ctx, principal, &merged); err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		return merged, changed, nil
	case "external_outbounds.delete":
		var deleteRequest struct {
			ExternalOutboundID int64 `json:"external_outbound_id"`
			Confirm            bool  `json:"confirm"`
		}
		if err := strictAutomationInput(input, &deleteRequest); err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		if deleteRequest.ExternalOutboundID <= 0 || !deleteRequest.Confirm {
			return model.ExternalOutbound{}, nil, errors.New("external_outbound_id and confirm=true are required")
		}
		current, err := s.store.GetExternalOutbound(ctx, deleteRequest.ExternalOutboundID)
		if err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		if current.ServerID != nil && !principal.AllowsInt64("server_ids", *current.ServerID) {
			return model.ExternalOutbound{}, nil, errors.New("external outbound is outside the authorized server boundary")
		}
		if _, err := s.guardAssignableNodeDelete(ctx, model.AssignableNodeExternalOutbound, current.ID); err != nil {
			return model.ExternalOutbound{}, nil, err
		}
		return *current, nil, nil
	default:
		return model.ExternalOutbound{}, nil, errors.New("unsupported external outbound operation")
	}
}

func (s *Server) validateExternalOutboundAutomationCandidate(ctx context.Context, principal application.Principal, external *model.ExternalOutbound) error {
	if external.ServerID != nil && *external.ServerID != 0 && !principal.AllowsInt64("server_ids", *external.ServerID) {
		return errors.New("external outbound server is outside the authorized server boundary")
	}
	return s.validateExternalOutbound(ctx, external)
}

func mergeExternalOutboundPatch(current model.ExternalOutbound, patch model.ExternalOutbound, fields map[string]json.RawMessage) model.ExternalOutbound {
	merged := current
	if _, ok := fields["server_id"]; ok {
		merged.ServerID = patch.ServerID
	}
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["protocol"]; ok {
		merged.Protocol = patch.Protocol
	}
	if _, ok := fields["scope"]; ok {
		merged.Scope = patch.Scope
	}
	if _, ok := fields["target_address"]; ok {
		merged.TargetAddress = patch.TargetAddress
	}
	if _, ok := fields["target_port"]; ok {
		merged.TargetPort = patch.TargetPort
	}
	if _, ok := fields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := fields["region_mode"]; ok {
		merged.RegionMode = patch.RegionMode
	}
	if _, ok := fields["region_code"]; ok {
		merged.RegionCode = patch.RegionCode
	}
	if _, ok := fields["expose_to_users"]; ok {
		merged.ExposeToUsers = patch.ExposeToUsers
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	return merged
}

func (s *Server) externalOutboundAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	if name == "external_outbounds.import" {
		var request externalOutboundOperationInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		if request.ServerID != nil {
			server, err := s.application.GetServer(ctx, principal, *request.ServerID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.Revision}, nil
		}
		return map[string]string{}, nil
	}
	external, _, err := s.externalOutboundAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	if name == "external_outbounds.create" {
		if external.ServerID != nil && *external.ServerID != 0 {
			server, err := s.application.GetServer(ctx, principal, *external.ServerID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.Revision}, nil
		}
		return map[string]string{}, nil
	}
	return map[string]string{"external_outbound:" + strconv.FormatInt(external.ID, 10): external.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) applyExternalOutboundOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	switch name {
	case "external_outbounds.import":
		var request externalOutboundOperationInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := parseExternalOutboundImport(request.Content)
		if err != nil {
			return nil, err
		}
		scope := model.ExternalOutboundScope(strings.TrimSpace(request.Scope))
		if scope == "" {
			scope = model.ExternalOutboundScopeGlobal
		}
		views := make([]any, 0, len(items))
		for index := range items {
			items[index].ServerID = request.ServerID
			items[index].Scope = scope
			items[index].ExposeToUsers = request.ExposeToUsers
			items[index].Enabled = true
			if err := s.validateExternalOutbound(ctx, &items[index]); err != nil {
				return nil, err
			}
			if err := s.store.CreateExternalOutbound(ctx, &items[index]); err != nil {
				return nil, err
			}
			views = append(views, automationExternalOutboundView(items[index]))
		}
		return map[string]any{"external_outbounds": views, "created_count": len(views)}, nil
	default:
		external, changed, err := s.externalOutboundAutomationCandidate(ctx, principal, input, name)
		if err != nil {
			return nil, err
		}
		switch name {
		case "external_outbounds.create":
			if err := s.store.CreateExternalOutbound(ctx, &external); err != nil {
				return nil, err
			}
		case "external_outbounds.update":
			if err := s.store.UpdateExternalOutbound(ctx, &external); err != nil {
				return nil, err
			}
		case "external_outbounds.delete":
			if _, err := s.store.RemoveAssignableNodeFromPlans(ctx, model.AssignableNodeExternalOutbound, external.ID); err != nil {
				return nil, err
			}
			if err := s.store.DeleteProxyPathStepsForExternal(ctx, external.ID); err != nil {
				return nil, err
			}
			if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
				return nil, err
			}
			if err := s.store.Delete(ctx, "external_outbounds", external.ID); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true, "external_outbound_id": external.ID}, nil
		}
		return automationExternalOutboundResult(external, changed)
	}
}

func automationExternalOutboundView(external model.ExternalOutbound) map[string]any {
	return map[string]any{
		"id": external.ID, "revision": external.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"server_id": external.ServerID, "name": external.Name, "protocol": external.Protocol,
		"scope": external.Scope, "target_address": external.TargetAddress, "target_port": external.TargetPort,
		"region_mode": external.RegionMode, "region_code": external.RegionCode,
		"effective_region_code": external.EffectiveRegionCode, "region_status": external.RegionStatus,
		"expose_to_users": external.ExposeToUsers, "enabled": external.Enabled,
		"advanced_configured": strings.TrimSpace(external.ConfigJSON) != "" && strings.TrimSpace(external.ConfigJSON) != "{}",
		"created_at":          external.CreatedAt, "updated_at": external.UpdatedAt,
	}
}

func automationExternalOutboundResult(external model.ExternalOutbound, changed []string) (any, error) {
	view := automationExternalOutboundView(external)
	if len(changed) == 0 {
		return map[string]any{"external_outbound": view}, nil
	}
	return map[string]any{"external_outbound": view, "changed_fields": changed}, nil
}
