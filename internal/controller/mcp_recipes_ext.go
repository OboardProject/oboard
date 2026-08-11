package controller

import (
	"context"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
)

// Additional Fast Path recipes for the domains added to the MCP capability
// catalog (traffic, network, ops, and system). Each recipe fills defaults and
// produces one immutable prepared operation so clients can call without
// reconstructing capability plans.

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

// prepareRoutingRuleRecipe routes routing rule create / update / delete.
func (s *Server) prepareRoutingRuleRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	deleting := containsAnyFold(input.Goal, "删除", "delete", "remove")
	ruleID := int64(0)
	if value := taskIntParam(input.Params, "routing_rule_id"); value > 0 {
		ruleID = int64(value)
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
			changes = nested
		}
		for _, key := range []string{"name", "priority", "match_json", "action", "outbound_id", "external_outbound_id", "interface_name", "enabled", "server_id"} {
			if value, ok := input.Params[key]; ok {
				changes[key] = value
			}
		}
		if len(changes) == 0 {
			return recipeNeedInput("routing.manage", "changes", "未识别到要修改的分流规则设置"), nil
		}
		operation := mcpOperationRef{Capability: "routing_rules.update", Input: map[string]any{"routing_rule_id": ruleID, "changes": changes}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "routing.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_routing_rule", "routing_rule_id": ruleID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
	}
	serverID, candidate, err := recipeTargetServer(ctx, s, principal, input)
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		candidate.Intent = "routing.manage"
		return candidate, nil
	}
	rule := map[string]any{"server_id": serverID}
	for _, key := range []string{"name", "priority", "match_json", "action", "outbound_id", "external_outbound_id", "interface_name", "enabled"} {
		if value, ok := input.Params[key]; ok {
			rule[key] = value
		}
	}
	if nested, ok := input.Params["routing_rule"].(map[string]any); ok {
		for key, value := range nested {
			rule[key] = value
		}
	}
	if rule["name"] == nil {
		return recipeNeedInput("routing.manage", "name", "需要指定分流规则名称"), nil
	}
	operation := mcpOperationRef{Capability: "routing_rules.create", Input: map[string]any{"routing_rule": rule}}
	return &mcpPreparedRecipe{Status: "ready", Intent: "routing.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "create_routing_rule", "server_id": serverID, "name": rule["name"]}, Verification: map[string]any{"after_commit": []string{"workflow_terminal"}}}, nil
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

// prepareHostOpsRecipe routes diagnose / agent update / log / MTU operations.
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
	case containsAnyFold(goal, "诊断", "diagnose"):
		operation := mcpOperationRef{Capability: "servers.diagnose", Input: map[string]any{"server_id": serverID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "diagnose_server", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "diagnose_task_queued"}}}, nil
	case containsAnyFold(goal, "升级", "update agent", "更新 agent", "update_server_agent"):
		operation := mcpOperationRef{Capability: "servers.update_agent", Input: map[string]any{"server_id": serverID}}
		return &mcpPreparedRecipe{Status: "ready", Intent: "host_ops.manage", Operations: []mcpOperationRef{operation}, Summary: map[string]any{"action": "update_agent", "server_id": serverID}, Verification: map[string]any{"after_commit": []string{"workflow_terminal", "update_task_queued"}}}, nil
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
		return recipeNeedInput("host_ops.manage", "operation", "需要指定操作：诊断、升级 Agent、MTU 检测或日志"), nil
	}
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

// prepareSettingsRecipe updates global settings.
func (s *Server) prepareSettingsRecipe(ctx context.Context, principal application.Principal, input mcpTaskInput) (*mcpPreparedRecipe, error) {
	changes := map[string]any{}
	if nested, ok := input.Params["changes"].(map[string]any); ok {
		changes = nested
	}
	for _, key := range []string{"audit_enabled", "subscription_audit_enabled", "connection_audit_enabled", "audit_action", "traffic_timezone", "traffic_enforcement_mode", "subscription_age_policy", "subscription_custom_path_mode", "server_default_mtu_mode", "server_default_bbr_enabled", "server_default_time_correction_mode", "time_check_ntp_servers", "trusted_proxy_cidrs", "controller_log_max_mb", "controller_log_backups", "agent_auto_update_enabled", "subscription_relay_auto_update_enabled", "update_window_enabled", "update_window_start_hour", "update_window_end_hour", "registration_enabled"} {
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
