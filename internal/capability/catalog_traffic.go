package capability

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// trafficDescriptors builds the outbound, routing-rule, and WARP-profile
// capability set. These mirror the panel's 出口 / 分流规则 tabs and are
// available to operators and admins.

func trafficDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	outbound := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "server_id": positiveID,
		"next_server_id": nullableInteger(), "name": stringValue, "protocol": stringValue,
		"target_address": stringValue, "target_port": map[string]any{"type": "integer"},
		"advanced_configured": boolValue, "enabled": boolValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	outboundFields := closedObject(map[string]any{
		"server_id":      positiveID,
		"next_server_id": nullableInteger(),
		"name":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"protocol":       map[string]any{"type": "string", "enum": []string{"vless", "hysteria2", "anytls", "shadowsocks", "mieru", "socks"}},
		"target_address": map[string]any{"type": "string", "maxLength": 255},
		"target_port":    map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"config_json":    map[string]any{"type": "string", "maxLength": 65536},
		"enabled":        boolValue,
	})
	routingRule := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "server_id": positiveID, "name": stringValue,
		"scope": stringValue, "proxy_path_id": nullableInteger(), "stage_step_id": nullableInteger(),
		"sort_position": map[string]any{"type": "integer"}, "match_source": stringValue, "rule_set_id": nullableInteger(),
		"priority": map[string]any{"type": "integer"}, "action": stringValue,
		"outbound_id": nullableInteger(), "external_outbound_id": nullableInteger(),
		"target_proxy_path_id": nullableInteger(), "target_server_id": nullableInteger(), "outbound_tag": stringValue, "interface_name": stringValue,
		"source_prefix":    stringValue,
		"sync_group_id":    stringValue,
		"match_configured": boolValue, "enabled": boolValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	routingRuleFieldProperties := map[string]any{
		"server_id":            positiveID,
		"scope":                map[string]any{"type": "string", "enum": []string{"server", "path_stage"}},
		"proxy_path_id":        nullableInteger(),
		"stage_step_id":        nullableInteger(),
		"sort_position":        map[string]any{"type": "integer", "minimum": 0, "maximum": 100000},
		"match_source":         map[string]any{"type": "string", "enum": []string{"inline", "rule_set"}},
		"rule_set_id":          nullableInteger(),
		"name":                 map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"priority":             map[string]any{"type": "integer", "minimum": 0, "maximum": 100000},
		"match_json":           map[string]any{"type": "string", "maxLength": 8192},
		"action":               map[string]any{"type": "string", "enum": []string{"direct", "block", "outbound", "external", "proxy_path", "interface", "source_prefix"}},
		"outbound_id":          nullableInteger(),
		"external_outbound_id": nullableInteger(),
		"target_proxy_path_id": nullableInteger(),
		"target_server_id":     nullableInteger(),
		"interface_name":       map[string]any{"type": "string", "maxLength": 64},
		"source_prefix":        map[string]any{"type": "string", "maxLength": 64},
		"enabled":              boolValue,
	}
	routingRuleFields := closedObject(routingRuleFieldProperties)
	routingRuleCreateProperties := make(map[string]any, len(routingRuleFieldProperties)+2)
	for key, value := range routingRuleFieldProperties {
		routingRuleCreateProperties[key] = value
	}
	routingRuleCreateProperties["sync_source_rule_id"] = nullableInteger()
	routingRuleCreateProperties["sync_enabled"] = boolValue
	routingRuleCreateFields := closedObject(routingRuleCreateProperties)
	warpProfile := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "server_id": positiveID, "name": stringValue,
		"status": stringValue, "mtu": map[string]any{"type": "integer"}, "dns_strategy": stringValue,
		"error": stringValue, "enabled": boolValue, "configured": boolValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	routingRuleSet := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "url": stringValue,
		"format": stringValue, "mihomo_behavior": stringValue, "status": stringValue, "last_error": stringValue,
		"last_attempt_at": nullableString(), "last_success_at": nullableString(), "created_at": stringValue, "updated_at": stringValue,
	})
	routingRuleSetFields := closedObject(map[string]any{
		"name":            map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"url":             map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
		"format":          map[string]any{"type": "string", "enum": []string{"singbox_source", "singbox_binary", "mihomo_domain", "mihomo_ipcidr", "mihomo_classical"}},
		"mihomo_behavior": map[string]any{"type": "string", "enum": []string{"", "domain", "ipcidr", "classical"}},
	})
	reads := []Descriptor{
		{Name: "outbounds.list", Description: "列出服务器出口（下一跳）及其认证配置状态", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(outbound)), RequiredScopes: []string{"outbounds:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "routing_rules.list", Description: "列出全部分流规则", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(routingRule)), RequiredScopes: []string{"routing_rules:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "routing_rule_sets.list", Description: "列出可复用远程分流规则集及刷新状态", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(routingRuleSet)), RequiredScopes: []string{"routing_rule_sets:read"}, ResourceTypes: []string{"routing_rule_set"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "warp_profiles.list", Description: "列出各服务器的 WARP 配置档状态（不含私钥）", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(warpProfile)), RequiredScopes: []string{"warp_profiles:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
	}
	reads = append(reads, trafficWriteDescriptors(outbound, routingRule, outboundFields, routingRuleCreateFields, routingRuleFields, positiveID, stringValue, boolValue, nullableInteger)...)
	placements := map[string]any{"type": "array", "minItems": 1, "maxItems": 512, "items": closedObject(map[string]any{"rule_id": positiveID, "stage_step_id": nullableInteger(), "sort_position": map[string]any{"type": "integer", "minimum": 0}})}
	for _, descriptor := range []Descriptor{
		{Name: "routing_rules.place", Description: "原子移动并重排代理分支节点规则", InputSchema: schemaObject(map[string]any{"proxy_path_id": positiveID, "placements": placements}, "proxy_path_id", "placements"), OutputSchema: schemaObject(map[string]any{"proxy_path_id": positiveID, "placements": placements}, "proxy_path_id", "placements"), RequiredScopes: []string{"routing_rules:write"}, ResourceTypes: []string{"proxy_path", "routing_rule"}, ResourceEvaluator: "server_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rules.place")},
		{Name: "routing_rule_sets.create", Description: "创建并首次校验远程分流规则集", InputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSetFields}, "routing_rule_set"), OutputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSet}, "routing_rule_set"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: noRefs},
		{Name: "routing_rule_sets.update", Description: "修改并校验远程分流规则集", InputSchema: schemaObject(map[string]any{"routing_rule_set_id": positiveID, "changes": routingRuleSetFields}, "routing_rule_set_id", "changes"), OutputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSet, "changed_fields": stringArray(1, 8)}, "routing_rule_set"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rule_sets.update")},
		{Name: "routing_rule_sets.delete", Description: "删除未被引用的远程分流规则集", InputSchema: schemaObject(map[string]any{"routing_rule_set_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "routing_rule_set_id", "confirm"), OutputSchema: schemaObject(map[string]any{"deleted": boolValue, "routing_rule_set_id": positiveID}, "deleted"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, Destructive: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rule_sets.delete")},
		{Name: "routing_rule_sets.refresh", Description: "立即刷新远程分流规则集并在内容变化时下发", InputSchema: schemaObject(map[string]any{"routing_rule_set_id": positiveID}, "routing_rule_set_id"), OutputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSet, "changed": boolValue}, "routing_rule_set", "changed"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rule_sets.refresh")},
	} {
		reads = append(reads, descriptor)
	}
	return reads
}

func trafficWriteDescriptors(outbound, routingRule, outboundFields, routingRuleCreateFields, routingRuleFields, positiveID map[string]any, stringValue, boolValue map[string]any, nullableInteger func() map[string]any) []Descriptor {
	writes := []struct {
		name, description string
		input, output     json.RawMessage
		risk              int
		destructive       bool
	}{
		{"outbounds.create", "创建服务器出口（下一跳）", schemaObject(map[string]any{"outbound": outboundFields}, "outbound"), schemaObject(map[string]any{"outbound": outbound}, "outbound"), 2, false},
		{"outbounds.update", "修改服务器出口", schemaObject(map[string]any{"outbound_id": positiveID, "changes": outboundFields}, "outbound_id", "changes"), schemaObject(map[string]any{"outbound": outbound, "changed_fields": stringArray(1, 32)}, "outbound"), 2, false},
		{"outbounds.delete", "删除服务器出口", schemaObject(map[string]any{"outbound_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "outbound_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "outbound_id": positiveID}, "deleted"), 3, true},
		{"routing_rules.create", "创建分流规则", schemaObject(map[string]any{"routing_rule": routingRuleCreateFields}, "routing_rule"), schemaObject(map[string]any{"routing_rule": routingRule}, "routing_rule"), 2, false},
		{"routing_rules.update", "修改分流规则", schemaObject(map[string]any{"routing_rule_id": positiveID, "changes": routingRuleFields}, "routing_rule_id", "changes"), schemaObject(map[string]any{"routing_rule": routingRule, "changed_fields": stringArray(1, 32)}, "routing_rule"), 2, false},
		{"routing_rules.delete", "删除分流规则", schemaObject(map[string]any{"routing_rule_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "routing_rule_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "routing_rule_id": positiveID}, "deleted"), 3, true},
	}
	descriptors := make([]Descriptor, 0, len(writes))
	for _, write := range writes {
		resourceTypes := []string{"server", "outbound", "external_outbound"}
		if strings.HasPrefix(write.name, "routing_rules.") {
			resourceTypes = append(resourceTypes, "proxy_path", "routing_rule", "routing_rule_set")
		}
		descriptors = append(descriptors, Descriptor{
			Name: write.name, Description: write.description, InputSchema: write.input, OutputSchema: write.output,
			RequiredScopes: []string{trafficScopeFor(write.name)}, ResourceTypes: resourceTypes,
			ResourceEvaluator: "server_ids", RiskClass: write.risk, ApprovalPolicy: "required", Idempotent: true,
			DataClassification: DataInternal, Destructive: write.destructive, MCPEnabled: true, Executable: true,
			MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver(write.name),
		})
	}
	return descriptors
}

// trafficScopeFor maps an executable capability to its single-colon scope
// family so the legacy scope mapping and MCP boundary stay consistent.
func trafficScopeFor(name string) string {
	switch {
	case strings.HasPrefix(name, "outbounds."):
		return "outbounds:write"
	case strings.HasPrefix(name, "routing_rules."):
		return "routing_rules:write"
	case strings.HasPrefix(name, "routing_rule_sets."):
		return "routing_rule_sets:write"
	case strings.HasPrefix(name, "external_outbounds."):
		return "external_outbounds:write"
	default:
		return name + ":write"
	}
}

func trafficWriteResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
	return func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		refs := []mcpauth.ResourceRef{}
		addRef := func(field, resourceType string) error {
			if id, ok := int64Value(object[field]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: resourceType, ID: strconv.FormatInt(id, 10)})
			}
			return nil
		}
		switch name {
		case "outbounds.create":
			nested, _ := object["outbound"].(map[string]any)
			if id, ok := int64Value(nested["server_id"]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
			}
		case "outbounds.update":
			if err := addRef("outbound_id", "outbound"); err != nil {
				return nil, err
			}
			changes, _ := object["changes"].(map[string]any)
			if id, ok := int64Value(changes["server_id"]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
			}
		case "outbounds.delete":
			if err := addRef("outbound_id", "outbound"); err != nil {
				return nil, err
			}
		case "routing_rules.create":
			nested, _ := object["routing_rule"].(map[string]any)
			for field, resourceType := range map[string]string{"server_id": "server", "proxy_path_id": "proxy_path", "target_proxy_path_id": "proxy_path", "sync_source_rule_id": "routing_rule", "rule_set_id": "routing_rule_set", "outbound_id": "outbound", "external_outbound_id": "external_outbound"} {
				if id, ok := int64Value(nested[field]); ok && id > 0 {
					refs = append(refs, mcpauth.ResourceRef{Type: resourceType, ID: strconv.FormatInt(id, 10)})
				}
			}
		case "routing_rules.update":
			if err := addRef("routing_rule_id", "routing_rule"); err != nil {
				return nil, err
			}
			changes, _ := object["changes"].(map[string]any)
			for field, resourceType := range map[string]string{"server_id": "server", "proxy_path_id": "proxy_path", "target_proxy_path_id": "proxy_path", "rule_set_id": "routing_rule_set", "outbound_id": "outbound", "external_outbound_id": "external_outbound"} {
				if id, ok := int64Value(changes[field]); ok && id > 0 {
					refs = append(refs, mcpauth.ResourceRef{Type: resourceType, ID: strconv.FormatInt(id, 10)})
				}
			}
		case "routing_rules.delete":
			if err := addRef("routing_rule_id", "routing_rule"); err != nil {
				return nil, err
			}
		case "routing_rules.place":
			if err := addRef("proxy_path_id", "proxy_path"); err != nil {
				return nil, err
			}
			if placements, ok := object["placements"].([]any); ok {
				for _, value := range placements {
					placement, _ := value.(map[string]any)
					if id, ok := int64Value(placement["rule_id"]); ok && id > 0 {
						refs = append(refs, mcpauth.ResourceRef{Type: "routing_rule", ID: strconv.FormatInt(id, 10)})
					}
				}
			}
		case "routing_rule_sets.update", "routing_rule_sets.delete", "routing_rule_sets.refresh":
			if err := addRef("routing_rule_set_id", "routing_rule_set"); err != nil {
				return nil, err
			}
		}
		return refs, nil
	}
}
