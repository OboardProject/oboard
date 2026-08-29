package controller

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

// Additional Fast Path recipes for the domains added to the MCP capability
// catalog (traffic, network, ops, and system). Each recipe fills defaults and
// produces one immutable prepared operation so clients can call without
// reconstructing capability plans.

func (s *Server) prepareUserTrafficLedgerRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	userID := int64(taskIntParam(input.Params, "user_id"))
	if userID <= 0 {
		userID = taskResourceRefID(input, "user")
	}
	if userID <= 0 {
		return recipeNeedInput("user.traffic.ledger", "user_id", "需要指定要查看流量账本的用户 ID"), nil
	}
	if !principal.AllowsInt64("user_ids", userID) {
		return nil, errors.New("authorized user not found")
	}
	serverID := int64(taskIntParam(input.Params, "server_id"))
	periodKey := taskStringParam(input.Params, "period_key")
	view, err := s.store.GetTrafficLedger(ctx, userID, serverID, periodKey)
	if err != nil {
		return nil, err
	}
	return &mcpPreparedRecipe{Status: "query_ready", Intent: "user.traffic.ledger", DirectResult: view, Summary: map[string]any{"user_id": userID, "action": "read_traffic_ledger"}, Verification: map[string]any{}, Fallback: []string{"oboard_capability_traffic_get_user_ledger", "oboard_capability_traffic_get_server_sync_state", "oboard_capability_traffic_list_reconciliation_issues"}}, nil
}

func recipeTargetServer(ctx context.Context, s *Server, principal application.Principal, input mcpTaskInput) (int64, *mcpPreparedRecipe, error) {
	if value := taskIntParam(input.Params, "server_id"); value > 0 {
		return int64(value), nil, nil
	}
	if target := firstTaskRef(input, "server", "target_server", "server"); target != "" {
		resolved, err := s.resolveServerRef(ctx, principal, target)
		if err != nil {
			return 0, nil, err
		}
		if len(resolved.Candidates) > 0 {
			return 0, &mcpPreparedRecipe{Status: "choose_candidate", Intent: "", Field: "server", Candidates: resolved.Candidates}, nil
		}
		return resolved.Value.ID, nil, nil
	}
	matches := s.inferServerCandidatesFromGoal(ctx, principal, input.Goal, 0)
	if len(matches) == 1 {
		return matches[0].ID, nil, nil
	}
	if len(matches) > 1 {
		return 0, &mcpPreparedRecipe{Status: "choose_candidate", Intent: "", Field: "server", Candidates: matches}, nil
	}
	return 0, &mcpPreparedRecipe{Status: "needs_input", Intent: "", Questions: []map[string]any{{"field": "server_id", "type": "integer", "reason": "需要指定目标服务器 ID"}}}, nil
}

func recipeNeedInput(intent, field, reason string) *mcpPreparedRecipe {
	return &mcpPreparedRecipe{Status: "needs_input", Intent: intent, Questions: []map[string]any{{"field": field, "type": "string", "reason": reason}}}
}

// prepareServerMetricsQueryRecipe is a read-only Fast Path that returns
// existing server metric data directly instead of producing a Changeset.
func (s *Server) prepareServerMetricsQueryRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	serverID := int64(taskIntParam(input.Params, "server_id"))
	if serverID <= 0 {
		serverID = taskResourceRefID(input, "server")
	}
	if serverID <= 0 {
		return recipeNeedInput("server.metrics.query", "server_id", "需要指定要查询指标的服务器 ID"), nil
	}
	windowHours := int64(taskIntParam(input.Params, "window_hours"))
	lowerGoal := strings.ToLower(input.Goal)
	latencyQuery := strings.Contains(lowerGoal, "latency") || strings.Contains(input.Goal, "延迟")
	var data any
	var err error
	if latencyQuery {
		limit := int64(taskIntParam(input.Params, "limit"))
		data, err = s.serverLatencyProbesRead(ctx, principal, serverID, limit)
	} else {
		data, err = s.serverMetricsRead(ctx, principal, serverID, windowHours)
	}
	if err != nil {
		return nil, err
	}
	action := "read_metrics"
	if latencyQuery {
		action = "read_latency_probes"
	}
	return &mcpPreparedRecipe{Status: "query_ready", Intent: "server.metrics.query", DirectResult: data, Summary: map[string]any{"server_id": serverID, "action": action}, Verification: map[string]any{}, Fallback: []string{"oboard_capability_servers_metrics_read", "oboard_capability_servers_latency_probes_read"}}, nil
}

// prepareOutboundRecipe routes outbound create / update / delete.
func (s *Server) prepareOutboundRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	outboundID := int64(0)
	if value := taskIntParam(input.Params, "outbound_id"); value > 0 {
		outboundID = int64(value)
	}
	if deleting && outboundID == 0 {
		return recipeNeedInput("outbound.manage", "outbound_id", "需要指定要删除的出口 ID"), nil
	}
	if deleting {
		operation := mcpOperationRef{Capability: "outbounds.delete", Input: map[string]any{"outbound_id": outboundID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "outbound.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_outbound", "outbound_id": outboundID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if outboundID > 0 {
		changes := map[string]any{}
		if nested, ok := input.Params["changes"].(map[string]any); ok {
			changes = nested
		}
		for _, key := range []string{"name", "protocol", "target_address", "target_port", "config_json", "enabled", "server_id", "next_server_id"} {
			if value, ok := input.Params[key]; ok {
				changes[key] = value
			}
		}
		if len(changes) == 0 {
			return recipeNeedInput("outbound.manage", "changes", "未识别到要修改的出口设置"), nil
		}
		operation := mcpOperationRef{Capability: "outbounds.update", Input: map[string]any{"outbound_id": outboundID, "changes": changes}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "outbound.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_outbound", "outbound_id": outboundID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	serverID, candidate, err := recipeTargetServer(ctx, s, principal, input)
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		candidate.Intent = "outbound.manage"
		return candidate, nil
	}
	outbound := map[string]any{"server_id": serverID}
	for _, key := range []string{"name", "protocol", "target_address", "target_port", "config_json", "enabled", "next_server_id"} {
		if value, ok := input.Params[key]; ok {
			outbound[key] = value
		}
	}
	if nested, ok := input.Params["outbound"].(map[string]any); ok {
		for key, value := range nested {
			outbound[key] = value
		}
	}
	if outbound["name"] == nil {
		outbound["name"] = strings.ToUpper(taskStringParam(input.Params, "protocol", "outbound.protocol"))
	}
	operation := mcpOperationRef{Capability: "outbounds.create", Input: map[string]any{"outbound": outbound}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "outbound.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_outbound", "server_id": serverID, "name": outbound["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

var routingRuleRecipeFields = []string{
	"server_id", "scope", "proxy_path_id", "stage_step_id", "sort_position",
	"match_source", "rule_set_id", "dns_resolver", "name", "priority", "match_json", "action",
	"outbound_id", "external_outbound_id", "target_proxy_path_id",
	"family_split_template_id", "family_dns_strategy",
	"interface_name", "source_prefix", "enabled",
}

func taskResourceRefID(input mcpTaskInput, resourceType string) int64 {
	ref := firstTaskRef(input, resourceType)
	_, target, err := splitMCPResourceRef(ref, resourceType)
	if err != nil {
		return 0
	}
	id, err := strconv.ParseInt(target, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func copyRecipeFields(dst, src map[string]any, fields []string) {
	for _, key := range fields {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
}

func (s *Server) routingRuleRecipeServerID(ctx context.Context, pathID, stageStepID int64) (int64, error) {
	path, err := s.store.GetProxyPath(ctx, pathID)
	if err != nil {
		return 0, err
	}
	if stageStepID > 0 {
		step, err := s.store.GetProxyPathStep(ctx, stageStepID)
		if err != nil {
			return 0, err
		}
		if step.PathID != path.ID || step.NodeType != model.ProxyPathStepServerInbound || step.ServerID == nil {
			return 0, errors.New("stage_step_id must identify a controlled server node in the selected proxy path")
		}
		return *step.ServerID, nil
	}
	inbound, err := s.store.GetInbound(ctx, path.InboundID)
	if err != nil {
		return 0, err
	}
	return inbound.ServerID, nil
}

// prepareRoutingRuleRecipe routes routing rule create, update, delete, and
// atomic path-stage placement.
func (s *Server) prepareRoutingRuleRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	placing := input.Params["placements"] != nil || containsAnyFold(input.Goal, "放置", "移动", "重排", "重新排序", "排序", "place", "move", "reorder")
	if placing {
		pathID := int64(taskIntParam(input.Params, "proxy_path_id"))
		if pathID == 0 {
			pathID = taskResourceRefID(input, "proxy_path")
		}
		if pathID == 0 {
			return recipeNeedInput("routing.manage", "proxy_path_id", "需要指定要重排分流规则的代理路径 ID"), nil
		}
		placements, ok := input.Params["placements"]
		if !ok || placements == nil {
			return recipeNeedInput("routing.manage", "placements", "需要提供代理路径中全部分流规则的位置"), nil
		}
		operation := mcpOperationRef{Capability: "routing_rules.place", Input: map[string]any{"proxy_path_id": pathID, "placements": placements}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "routing.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "place_routing_rules", "proxy_path_id": pathID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}

	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	ruleID := int64(taskIntParam(input.Params, "routing_rule_id"))
	if ruleID == 0 {
		ruleID = taskResourceRefID(input, "routing_rule")
	}
	if deleting && ruleID == 0 {
		return recipeNeedInput("routing.manage", "routing_rule_id", "需要指定要删除的分流规则 ID"), nil
	}
	if deleting {
		operation := mcpOperationRef{Capability: "routing_rules.delete", Input: map[string]any{"routing_rule_id": ruleID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "routing.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_routing_rule", "routing_rule_id": ruleID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if ruleID > 0 {
		changes := map[string]any{}
		if nested, ok := input.Params["changes"].(map[string]any); ok {
			copyRecipeFields(changes, nested, routingRuleRecipeFields)
		}
		copyRecipeFields(changes, input.Params, routingRuleRecipeFields)
		if len(changes) == 0 {
			return recipeNeedInput("routing.manage", "changes", "未识别到要修改的分流规则设置"), nil
		}
		operation := mcpOperationRef{Capability: "routing_rules.update", Input: map[string]any{"routing_rule_id": ruleID, "changes": changes}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "routing.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_routing_rule", "routing_rule_id": ruleID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	rule := map[string]any{}
	copyRecipeFields(rule, input.Params, append(routingRuleRecipeFields, "sync_source_rule_id", "sync_enabled"))
	if nested, ok := input.Params["routing_rule"].(map[string]any); ok {
		copyRecipeFields(rule, nested, append(routingRuleRecipeFields, "sync_source_rule_id", "sync_enabled"))
	}
	if _, hasScope := rule["scope"]; !hasScope && taskIntParam(rule, "proxy_path_id") > 0 {
		rule["scope"] = model.RoutingRuleScopePathStage
	}
	serverID := int64(taskIntParam(rule, "server_id"))
	if taskStringParam(rule, "scope") == model.RoutingRuleScopePathStage {
		pathID := int64(taskIntParam(rule, "proxy_path_id"))
		if pathID == 0 {
			pathID = taskResourceRefID(input, "proxy_path")
			if pathID > 0 {
				rule["proxy_path_id"] = pathID
			}
		}
		if pathID == 0 {
			return recipeNeedInput("routing.manage", "proxy_path_id", "path_stage 分流规则需要指定代理路径 ID"), nil
		}
		var err error
		serverID, err = s.routingRuleRecipeServerID(ctx, pathID, int64(taskIntParam(rule, "stage_step_id")))
		if err != nil {
			return nil, err
		}
		rule["server_id"] = serverID
	} else if serverID == 0 {
		resolvedID, candidate, err := recipeTargetServer(ctx, s, principal, input)
		if err != nil {
			return nil, err
		}
		if candidate != nil {
			candidate.Intent = "routing.manage"
			return candidate, nil
		}
		serverID = resolvedID
		rule["server_id"] = serverID
	}
	if rule["name"] == nil && taskIntParam(rule, "sync_source_rule_id") == 0 {
		return recipeNeedInput("routing.manage", "name", "需要指定分流规则名称"), nil
	}
	operation := mcpOperationRef{Capability: "routing_rules.create", Input: map[string]any{"routing_rule": rule}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "routing.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_routing_rule", "server_id": serverID, "name": rule["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// prepareRoutingRuleSetRecipe routes reusable remote rule-set CRUD and refresh.
func (s *Server) prepareRoutingRuleSetRecipe(_ context.Context, _ application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	setID := int64(taskIntParam(input.Params, "routing_rule_set_id", "rule_set_id"))
	if setID == 0 {
		setID = taskResourceRefID(input, "routing_rule_set")
	}
	refreshing := containsAnyFold(input.Goal, "刷新", "重新拉取", "refresh", "reload")
	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	if refreshing || deleting {
		if setID == 0 {
			return recipeNeedInput("routing_rule_set.manage", "routing_rule_set_id", "需要指定远程分流规则集 ID"), nil
		}
		capability := "routing_rule_sets.refresh"
		action := "refresh_routing_rule_set"
		operationInput := map[string]any{"routing_rule_set_id": setID}
		if deleting {
			capability = "routing_rule_sets.delete"
			action = "delete_routing_rule_set"
			operationInput["confirm"] = true
		}
		operation := mcpOperationRef{Capability: capability, Input: operationInput}
		return &mcpPreparedRecipe{Status: "ready", Intent: "routing_rule_set.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": action, "routing_rule_set_id": setID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}

	fields := map[string]any{}
	if setID > 0 {
		if nested, ok := input.Params["changes"].(map[string]any); ok {
			copyRecipeFields(fields, nested, []string{"name", "url", "format", "mihomo_behavior"})
		}
	} else if nested, ok := input.Params["routing_rule_set"].(map[string]any); ok {
		copyRecipeFields(fields, nested, []string{"name", "url", "format", "mihomo_behavior"})
	}
	copyRecipeFields(fields, input.Params, []string{"name", "url", "format", "mihomo_behavior"})
	if setID > 0 {
		if len(fields) == 0 {
			return recipeNeedInput("routing_rule_set.manage", "changes", "未识别到要修改的远程分流规则集设置"), nil
		}
		operation := mcpOperationRef{Capability: "routing_rule_sets.update", Input: map[string]any{"routing_rule_set_id": setID, "changes": fields}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "routing_rule_set.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_routing_rule_set", "routing_rule_set_id": setID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if fields["name"] == nil || fields["url"] == nil || fields["format"] == nil {
		return recipeNeedInput("routing_rule_set.manage", "routing_rule_set", "需要指定规则集名称、URL 和格式"), nil
	}
	operation := mcpOperationRef{Capability: "routing_rule_sets.create", Input: map[string]any{"routing_rule_set": fields}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "routing_rule_set.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_routing_rule_set", "name": fields["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// prepareExternalOutboundImportRecipe imports third-party nodes from text.
func (s *Server) prepareExternalOutboundImportRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	content := taskStringParam(input.Params, "content")
	if content == "" {
		if nested, ok := input.Params["external_outbound"].(map[string]any); ok {
			if value, ok := nested["content"].(string); ok {
				content = value
			}
		}
	}
	if content == "" {
		return recipeNeedInput("external_outbound.import", "content", "需要提供订阅文本或节点 URI"), nil
	}
	importInput := map[string]any{"content": content}
	for _, key := range []string{"scope", "server_id", "expose_to_users"} {
		if value, ok := input.Params[key]; ok {
			importInput[key] = value
		}
	}
	operation := mcpOperationRef{Capability: "external_outbounds.import", Input: importInput}
	return &mcpPreparedRecipe{Status: "ready", Intent: "external_outbound.import", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "import_external_outbounds", "content_length": len(content)}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "external_outbounds_present"}}}, nil
}

// prepareDNSPolicyRecipe sets or tests a server DNS policy.
func (s *Server) prepareDNSPolicyRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	testing := containsAnyFold(input.Goal, "测试", "检查", "test", "benchmark")
	serverID, candidate, err := recipeTargetServer(ctx, s, principal, input)
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		candidate.Intent = "dns_policy.manage"
		return candidate, nil
	}
	if testing {
		action := "test"
		if containsAnyFold(input.Goal, "应用", "apply") {
			action = "test_and_apply"
		}
		operation := mcpOperationRef{Capability: "servers.dns_test", Input: map[string]any{"server_id": serverID, "action": action}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "dns_policy.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "dns_test", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "dns_task_queued"}}}, nil
	}
	changes := map[string]any{}
	for _, key := range []string{"encrypted_list_id", "bootstrap_list_id", "strategy", "auto_test", "test_interval_seconds"} {
		if value, ok := input.Params[key]; ok {
			changes[key] = value
		}
	}
	if nested, ok := input.Params["changes"].(map[string]any); ok {
		for key, value := range nested {
			changes[key] = value
		}
	}
	if len(changes) == 0 {
		return recipeNeedInput("dns_policy.manage", "changes", "需要指定加密解析列表、引导解析列表或策略"), nil
	}
	operation := mcpOperationRef{Capability: "servers.dns_policy.set", Input: map[string]any{"server_id": serverID, "changes": changes}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "dns_policy.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "set_dns_policy", "server_id": serverID, "changes": changes}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// prepareDNSRecordRecipe routes DNS record create / update / delete.
func (s *Server) prepareDNSRecordRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	zoneID := int64(0)
	if value := taskIntParam(input.Params, "dns_zone_id"); value > 0 {
		zoneID = int64(value)
	} else if record, ok := input.Params["record"].(map[string]any); ok {
		if value, ok := record["dns_zone_id"].(float64); ok {
			zoneID = int64(value)
		}
	}
	if zoneID == 0 {
		return recipeNeedInput("dns_record.manage", "dns_zone_id", "需要指定 DNS 区域 ID"), nil
	}
	recordID := taskStringParam(input.Params, "record_id")
	if deleting {
		if recordID == "" {
			return recipeNeedInput("dns_record.manage", "record_id", "需要指定要删除的解析记录 ID"), nil
		}
		operation := mcpOperationRef{Capability: "dns_records.delete", Input: map[string]any{"dns_zone_id": zoneID, "record_id": recordID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "dns_record.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_dns_record", "dns_zone_id": zoneID, "record_id": recordID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if recordID != "" {
		changes := map[string]any{}
		if nested, ok := input.Params["changes"].(map[string]any); ok {
			changes = nested
		}
		for _, key := range []string{"type", "name", "content", "proxied", "ttl", "comment", "server_id", "inbound_id"} {
			if value, ok := input.Params[key]; ok {
				changes[key] = value
			}
		}
		if len(changes) == 0 {
			return recipeNeedInput("dns_record.manage", "changes", "未识别到要修改的解析记录字段"), nil
		}
		operation := mcpOperationRef{Capability: "dns_records.update", Input: map[string]any{"dns_zone_id": zoneID, "record_id": recordID, "changes": changes}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "dns_record.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_dns_record", "record_id": recordID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	record := map[string]any{"dns_zone_id": zoneID}
	if nested, ok := input.Params["record"].(map[string]any); ok {
		for key, value := range nested {
			record[key] = value
		}
	}
	for _, key := range []string{"type", "name", "content", "proxied", "ttl", "comment", "server_id", "inbound_id"} {
		if value, ok := input.Params[key]; ok {
			record[key] = value
		}
	}
	if record["name"] == nil || record["content"] == nil {
		return recipeNeedInput("dns_record.manage", "record", "需要指定记录名称和内容"), nil
	}
	operation := mcpOperationRef{Capability: "dns_records.create", Input: map[string]any{"record": record}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "dns_record.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_dns_record", "name": record["name"], "type": record["type"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// preparePortForwardRecipe routes port forward create / update / delete.
func (s *Server) preparePortForwardRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	forwardID := int64(0)
	if value := taskIntParam(input.Params, "port_forward_id"); value > 0 {
		forwardID = int64(value)
	}
	if deleting && forwardID == 0 {
		return recipeNeedInput("port_forward.manage", "port_forward_id", "需要指定要删除的端口转发 ID"), nil
	}
	if deleting {
		operation := mcpOperationRef{Capability: "port_forwards.delete", Input: map[string]any{"port_forward_id": forwardID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "port_forward.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_port_forward", "port_forward_id": forwardID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	fields := map[string]any{}
	if nested, ok := input.Params["port_forward"].(map[string]any); ok {
		fields = nested
	}
	for _, key := range []string{"name", "source_server_id", "target_server_id", "listen_ip", "listen_port", "target_address", "target_port", "protocol", "backend", "probe_mode", "enabled"} {
		if value, ok := input.Params[key]; ok {
			fields[key] = value
		}
	}
	if forwardID > 0 {
		changes := map[string]any{}
		if nested, ok := input.Params["changes"].(map[string]any); ok {
			changes = nested
		}
		for key, value := range fields {
			changes[key] = value
		}
		if len(changes) == 0 {
			return recipeNeedInput("port_forward.manage", "changes", "未识别到要修改的端口转发设置"), nil
		}
		operation := mcpOperationRef{Capability: "port_forwards.update", Input: map[string]any{"port_forward_id": forwardID, "changes": changes}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "port_forward.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_port_forward", "port_forward_id": forwardID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if fields["name"] == nil || fields["listen_port"] == nil || fields["target_port"] == nil {
		return recipeNeedInput("port_forward.manage", "port_forward", "需要指定名称、监听端口和目标端口"), nil
	}
	operation := mcpOperationRef{Capability: "port_forwards.create", Input: map[string]any{"port_forward": fields}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "port_forward.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_port_forward", "name": fields["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// prepareTunnelRecipe routes tunnel create / update / delete.
func (s *Server) prepareTunnelRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	tunnelID := int64(0)
	if value := taskIntParam(input.Params, "tunnel_id"); value > 0 {
		tunnelID = int64(value)
	}
	if deleting && tunnelID == 0 {
		return recipeNeedInput("tunnel.manage", "tunnel_id", "需要指定要删除的隧道 ID"), nil
	}
	if deleting {
		operation := mcpOperationRef{Capability: "tunnels.delete", Input: map[string]any{"tunnel_id": tunnelID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "tunnel.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_tunnel", "tunnel_id": tunnelID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	fields := map[string]any{}
	if nested, ok := input.Params["tunnel"].(map[string]any); ok {
		fields = nested
	}
	for _, key := range []string{"name", "source_server_id", "target_server_id", "type", "local_address", "peer_address", "listen_port", "target_endpoint", "target_port", "config_json", "enabled"} {
		if value, ok := input.Params[key]; ok {
			fields[key] = value
		}
	}
	if tunnelID > 0 {
		changes := map[string]any{}
		if nested, ok := input.Params["changes"].(map[string]any); ok {
			changes = nested
		}
		for key, value := range fields {
			changes[key] = value
		}
		if len(changes) == 0 {
			return recipeNeedInput("tunnel.manage", "changes", "未识别到要修改的隧道设置"), nil
		}
		operation := mcpOperationRef{Capability: "tunnels.update", Input: map[string]any{"tunnel_id": tunnelID, "changes": changes}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "tunnel.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_tunnel", "tunnel_id": tunnelID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if fields["name"] == nil {
		return recipeNeedInput("tunnel.manage", "tunnel", "需要指定隧道名称"), nil
	}
	operation := mcpOperationRef{Capability: "tunnels.create", Input: map[string]any{"tunnel": fields}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "tunnel.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_tunnel", "name": fields["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// prepareHostOpsRecipe routes diagnose / agent update / log / MTU / interface operations.
func (s *Server) prepareHostOpsRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	serverID, candidate, err := recipeTargetServer(ctx, s, principal, input)
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		candidate.Intent = "host_ops.manage"
		return candidate, nil
	}
	goal := strings.ToLower(input.Goal)
	switch {
	case containsAnyFold(goal, "网卡", "网络接口", "network interface", "interfaces"):
		operation := mcpOperationRef{Capability: "servers.list_network_interfaces", Input: map[string]any{"server_id": serverID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "list_network_interfaces", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "network_interface_task_queued"}}}, nil
	case containsAnyFold(goal, "诊断", "diagnose"):
		operation := mcpOperationRef{Capability: "servers.diagnose", Input: map[string]any{"server_id": serverID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "diagnose_server", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "diagnose_task_queued"}}}, nil
	case containsAnyFold(goal, "升级", "update agent", "更新 agent", "update_server_agent"):
		operation := mcpOperationRef{Capability: "servers.update_agent", Input: map[string]any{"server_id": serverID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_agent", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "update_task_queued"}}}, nil
	case containsAnyFold(goal, "卸载 agent", "uninstall agent", "remove agent"):
		operation := mcpOperationRef{Capability: "servers.uninstall_agent", Input: map[string]any{"server_id": serverID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "uninstall_agent", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "uninstall_task_queued"}}}, nil
	case containsAnyFold(goal, "mtu", "拉取日志", "collect logs", "日志", "轮转", "清空", "manage logs"):
		if containsAnyFold(goal, "mtu") {
			operation := mcpOperationRef{Capability: "servers.mtu_detect", Input: map[string]any{"server_id": serverID, "target_host": taskStringParam(input.Params, "target_host"), "target_port": taskIntParam(input.Params, "target_port")}}
			return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "detect_mtu", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "mtu_task_queued"}}}, nil
		}
		if containsAnyFold(goal, "轮转", "rotate", "清空", "clear") {
			action := "rotate"
			if containsAnyFold(goal, "清空", "clear") {
				action = "clear"
			}
			operation := mcpOperationRef{Capability: "servers.manage_logs", Input: map[string]any{"server_id": serverID, "action": action, "services": firstNonEmptyString(taskStringParam(input.Params, "services"), "all")}}
			return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "manage_logs", "server_id": serverID, "action_type": action}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "logs_task_queued"}}}, nil
		}
		operation := mcpOperationRef{Capability: "servers.collect_logs", Input: map[string]any{"server_id": serverID, "services": firstNonEmptyString(taskStringParam(input.Params, "services"), "all"), "lines": taskIntParam(input.Params, "lines")}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "collect_logs", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "logs_task_queued"}}}, nil
	default:
		return recipeNeedInput("host_ops.manage", "operation", "需要指定操作：诊断、升级/卸载 Agent、读取网卡、MTU 检测或日志"), nil
	}
}

func (s *Server) prepareControllerUpdateRecipe(_ context.Context, _ application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	goal := strings.ToLower(input.Goal)
	channel := strings.ToLower(taskStringParam(input.Params, "channel"))
	var operation mcpOperationRef
	action := ""
	switch {
	case containsAnyFold(goal, "强制结束", "force finish", "force stop"):
		operation = mcpOperationRef{Capability: "controller_update.force_finish", Input: map[string]any{"confirmation": controllerUpdateForceFinishPhrase}}
		action = "force_finish_controller_update"
	case containsAnyFold(goal, "取消", "cancel"):
		operation = mcpOperationRef{Capability: "controller_update.cancel", Input: map[string]any{"confirm": true}}
		action = "cancel_controller_update"
	case channel != "" || containsAnyFold(goal, "通道", "channel"):
		if channel != "stable" && channel != "dev" {
			return recipeNeedInput("controller_update.manage", "channel", "需要指定 stable 或 dev 更新通道"), nil
		}
		operation = mcpOperationRef{Capability: "controller_update.set_channel", Input: map[string]any{"channel": channel}}
		action = "set_controller_update_channel"
	case containsAnyFold(goal, "安装", "升级", "更新主控", "install", "update controller"):
		skipBackup := taskBoolParam(input.Params, true, "skip_backup")
		if containsAnyFold(goal, "备份并", "with backup", "先备份") && !containsAnyFold(goal, "跳过备份", "skip backup") {
			skipBackup = false
		}
		if containsAnyFold(goal, "跳过备份", "skip backup") {
			skipBackup = true
		}
		installInput := map[string]any{"confirm": true, "skip_backup": skipBackup}
		operation = mcpOperationRef{Capability: "controller_update.install", Input: installInput}
		action = "install_controller_update"
	case containsAnyFold(goal, "检查", "check"):
		operation = mcpOperationRef{Capability: "controller_update.check", Input: map[string]any{}}
		action = "check_controller_update"
	default:
		return recipeNeedInput("controller_update.manage", "operation", "需要指定操作：检查更新、切换通道、安装更新、取消更新或强制结束任务"), nil
	}
	return &mcpPreparedRecipe{Status: "ready", Intent: "controller_update.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": action}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "controller_update_status"}}}, nil
}

// prepareNotificationRecipe routes notification channel create / update /
// delete / test and announcements.
func (s *Server) prepareNotificationRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	goal := strings.ToLower(input.Goal)
	channelID := int64(0)
	if value := taskIntParam(input.Params, "channel_id"); value > 0 {
		channelID = int64(value)
	}
	if containsAnyFold(goal, "公告", "announce") {
		title, body := taskStringParam(input.Params, "title"), taskStringParam(input.Params, "body")
		if title == "" || body == "" {
			return recipeNeedInput("notification.manage", "announcement", "需要指定公告标题和内容"), nil
		}
		announcement := map[string]any{"title": title, "body": body}
		if ids, ok := input.Params["user_ids"].([]any); ok {
			announcement["user_ids"] = ids
		}
		operation := mcpOperationRef{Capability: "notification_announcements.create", Input: announcement}
		return &mcpPreparedRecipe{Status: "ready", Intent: "notification.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_announcement", "title": title}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if containsAnyFold(goal, "测试", "test") && channelID > 0 {
		operation := mcpOperationRef{Capability: "notification_channels.test", Input: map[string]any{"channel_id": channelID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "notification.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "test_channel", "channel_id": channelID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if containsAnyFold(goal, "删除", "delete", "remove") && channelID > 0 {
		operation := mcpOperationRef{Capability: "notification_channels.delete", Input: map[string]any{"channel_id": channelID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "notification.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_channel", "channel_id": channelID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	fields := map[string]any{}
	if nested, ok := input.Params["notification_channel"].(map[string]any); ok {
		fields = nested
	}
	for _, key := range []string{"name", "type", "enabled", "events", "config_json", "templates_json", "user_ids"} {
		if value, ok := input.Params[key]; ok {
			fields[key] = value
		}
	}
	if channelID > 0 {
		changes := map[string]any{}
		if nested, ok := input.Params["changes"].(map[string]any); ok {
			changes = nested
		}
		for key, value := range fields {
			changes[key] = value
		}
		if len(changes) == 0 {
			return recipeNeedInput("notification.manage", "changes", "未识别到要修改的通知设置"), nil
		}
		operation := mcpOperationRef{Capability: "notification_channels.update", Input: map[string]any{"channel_id": channelID, "changes": changes}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "notification.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_channel", "channel_id": channelID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if fields["name"] == nil || fields["type"] == nil {
		return recipeNeedInput("notification.manage", "notification_channel", "需要指定频道名称和类型"), nil
	}
	operation := mcpOperationRef{Capability: "notification_channels.create", Input: map[string]any{"notification_channel": fields}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "notification.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_channel", "name": fields["name"], "type": fields["type"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

// prepareCertificateRecipe routes certificate issue / delete.
func (s *Server) prepareCertificateRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	certificateID := int64(0)
	if value := taskIntParam(input.Params, "certificate_id"); value > 0 {
		certificateID = int64(value)
	}
	if certificateID == 0 {
		if ref := firstTaskRef(input, "certificate", "target_certificate", "certificate"); ref != "" {
			if id, err := strconv.ParseInt(strings.TrimPrefix(ref, "certificate:"), 10, 64); err == nil {
				certificateID = id
			}
		}
	}
	if certificateID == 0 {
		return recipeNeedInput("certificate.manage", "certificate_id", "需要指定证书 ID"), nil
	}
	if containsAnyFold(input.Goal, "删除", "delete", "remove") {
		operation := mcpOperationRef{Capability: "certificates.delete", Input: map[string]any{"certificate_id": certificateID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "certificate.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_certificate", "certificate_id": certificateID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	operation := mcpOperationRef{Capability: "certificates.issue", Input: map[string]any{"certificate_id": certificateID}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "certificate.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "issue_certificate", "certificate_id": certificateID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "certificate_issuance_accepted"}}}, nil
}

// prepareSettingsRecipe updates global settings or controls an active
// Controller base-path migration.
func (s *Server) prepareSettingsRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	goal := strings.ToLower(input.Goal)
	if containsAnyFold(goal, "强制完成迁移", "强制迁移", "force migrate", "force base path") || (containsAnyFold(goal, "强制完成", "force complete") && containsAnyFold(goal, "路径", "base path", "迁移")) {
		operation := mcpOperationRef{Capability: "settings.base_path.force", Input: map[string]any{"confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "settings.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "force_base_path_migration"}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "settings"}}}, nil
	}
	if containsAnyFold(goal, "撤销迁移", "撤销面板路径", "revoke base path", "rollback base path") || (containsAnyFold(goal, "撤销", "revoke") && containsAnyFold(goal, "路径", "base path", "迁移")) {
		operation := mcpOperationRef{Capability: "settings.base_path.revoke", Input: map[string]any{"confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "settings.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "revoke_base_path_migration"}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "settings"}}}, nil
	}
	if containsAnyFold(goal, "重试失败", "重试 agent", "retry base path") || (containsAnyFold(goal, "重试", "retry") && containsAnyFold(goal, "路径", "base path", "迁移")) {
		operation := mcpOperationRef{Capability: "settings.base_path.retry", Input: map[string]any{}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "settings.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "retry_base_path_migration"}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "settings"}}}, nil
	}
	changes := map[string]any{}
	if nested, ok := input.Params["changes"].(map[string]any); ok {
		changes = nested
	}
	for _, key := range []string{"audit_enabled", "subscription_audit_enabled", "connection_audit_enabled", "audit_action", "traffic_timezone", "traffic_enforcement_mode", "subscription_age_policy", "subscription_always_use_domain_host", "subscription_custom_path_mode", "server_default_mtu_mode", "server_default_bbr_enabled", "server_default_time_correction_mode", "server_monitoring_retention_days", "time_check_ntp_servers", "trusted_proxy_cidrs", "controller_log_max_mb", "controller_log_backups", "agent_auto_update_enabled", "subscription_relay_auto_update_enabled", "update_window_enabled", "update_window_start_hour", "update_window_end_hour", "agent_update_max_concurrency", "managed_update_startup_quiet_seconds", "registration_enabled"} {
		if value, ok := input.Params[key]; ok {
			changes[key] = value
		}
	}
	if len(changes) == 0 {
		return recipeNeedInput("settings.manage", "changes", "未识别到要修改的全局设置"), nil
	}
	operation := mcpOperationRef{Capability: "settings.update", Input: map[string]any{"changes": changes}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "settings.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_settings", "changed_fields": changes}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}

func (s *Server) prepareFamilySplitTemplateRecipe(_ context.Context, _ application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	templateID := int64(taskIntParam(input.Params, "family_split_template_id"))
	if templateID == 0 {
		templateID = taskResourceRefID(input, "family_split_template")
	}
	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	if deleting {
		if templateID == 0 {
			return recipeNeedInput("family_split_template.manage", "family_split_template_id", "需要指定要删除的双栈模板 ID"), nil
		}
		operation := mcpOperationRef{Capability: "family_split_templates.delete", Input: map[string]any{"family_split_template_id": templateID, "confirm": true}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "family_split_template.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "delete_family_split_template", "family_split_template_id": templateID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	fields := map[string]any{}
	if nested, ok := input.Params["family_split_template"].(map[string]any); ok {
		copyRecipeFields(fields, nested, []string{"name"})
	}
	if nested, ok := input.Params["changes"].(map[string]any); ok {
		copyRecipeFields(fields, nested, []string{"name"})
	}
	copyRecipeFields(fields, input.Params, []string{"name"})
	if templateID > 0 {
		if len(fields) == 0 {
			return recipeNeedInput("family_split_template.manage", "changes", "未识别到要修改的双栈模板名称"), nil
		}
		operation := mcpOperationRef{Capability: "family_split_templates.update", Input: map[string]any{"family_split_template_id": templateID, "changes": fields}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "family_split_template.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_family_split_template", "family_split_template_id": templateID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	if fields["name"] == nil {
		return recipeNeedInput("family_split_template.manage", "name", "需要指定双栈模板名称"), nil
	}
	operation := mcpOperationRef{Capability: "family_split_templates.create", Input: map[string]any{"family_split_template": fields}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "family_split_template.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_family_split_template", "name": fields["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
}
