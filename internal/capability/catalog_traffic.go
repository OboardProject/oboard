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
		"sort_position": map[string]any{"type": "integer"}, "match_source": stringValue, "rule_set_id": nullableInteger(), "dns_resolver": stringValue,
		"priority": map[string]any{"type": "integer"}, "action": stringValue,
		"outbound_id": nullableInteger(), "external_outbound_id": nullableInteger(),
		"target_proxy_path_id": nullableInteger(), "family_split_template_id": nullableInteger(),
		"family_dns_strategy": stringValue,
		"outbound_tag": stringValue, "interface_name": stringValue,
		"source_prefix":    stringValue,
		"sync_group_id":    stringValue,
		"match_configured": boolValue, "enabled": boolValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	nullablePositiveID := func(description string) map[string]any {
		return map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "description": description}
	}
	routingRuleFieldProperties := map[string]any{
		"server_id":                 map[string]any{"type": "integer", "minimum": 1, "description": "server scope 的目标服务器；path_stage scope 会根据分支节点自动推导"},
		"scope":                     map[string]any{"type": "string", "enum": []string{"server", "path_stage"}, "default": "server", "description": "server 表示服务器级规则；path_stage 表示代理分支根节点或受控节点规则"},
		"proxy_path_id":             nullablePositiveID("path_stage 规则所属代理分支；scope=path_stage 时必填"),
		"stage_step_id":             nullablePositiveID("分支内受控 server_inbound 步骤；null 表示分支根入口"),
		"sort_position":             map[string]any{"type": "integer", "minimum": 0, "maximum": 100000, "default": 0, "description": "path_stage 节点内的稳定排序位置"},
		"match_source":              map[string]any{"type": "string", "enum": []string{"inline", "rule_set"}, "default": "inline", "description": "inline 使用 match_json；rule_set 使用已成功抓取的远程规则集"},
		"rule_set_id":               nullablePositiveID("match_source=rule_set 时必填；远程规则集仅适用于 path_stage"),
		"dns_resolver":              map[string]any{"type": "string", "enum": []string{"", "remote-primary", "remote-secondary", "bootstrap-primary", "bootstrap-secondary", "local"}, "default": "", "description": "可选：仅命中规则时使用指定 DNS 服务器；留空使用服务器默认 DNS"},
		"name":                      map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "规则名称；从 sync_source_rule_id 复用匹配条件时可省略"},
		"priority":                  map[string]any{"type": "integer", "minimum": 0, "maximum": 100000, "default": 100, "description": "server scope 的匹配优先级；path_stage 优先使用 sort_position"},
		"match_json":                map[string]any{"type": "string", "maxLength": 8192, "default": "{}", "description": "inline 匹配对象的 JSON 字符串；rule_set 模式会规范化为 {}"},
		"action":                    map[string]any{"type": "string", "enum": []string{"direct", "block", "outbound", "external", "proxy_path", "family_split", "interface", "source_prefix"}, "description": "direct/block 无目标；family_split 选择可复用双栈模板，按目标地址家族分流后合并"},
		"outbound_id":               nullablePositiveID("action=outbound 时必填，且出口必须属于规则所在服务器"),
		"external_outbound_id":      nullablePositiveID("action=external 时必填，且服务器级导入节点必须属于规则所在服务器"),
		"target_proxy_path_id":      nullablePositiveID("action=proxy_path 时必填；该动作仅适用于 path_stage"),
		"family_split_template_id":  nullablePositiveID("action=family_split 时必填；可复用的 IPv4/IPv6 双栈模板"),
		"family_dns_strategy":       map[string]any{"type": "string", "enum": []string{"auto", "prefer_ipv4", "prefer_ipv6"}, "default": "auto", "description": "域名双栈记录的首选家族；首选失败只允许一次跨家族降级"},
		"interface_name":            map[string]any{"type": "string", "minLength": 1, "maxLength": 15, "pattern": "^[A-Za-z0-9._:-]+$", "description": "action=interface 时必填；proxy_path 可选绑定接口，与 source_prefix 互斥"},
		"source_prefix":             map[string]any{"type": "string", "minLength": 3, "maxLength": 64, "description": "action=source_prefix 时必填的 IPv4/IPv6 CIDR；proxy_path 可选绑定源前缀，与 interface_name 互斥"},
		"enabled":                   map[string]any{"type": "boolean", "description": "是否启用该规则"},
	}
	routingRuleFields := closedObject(routingRuleFieldProperties)
	routingRuleFields["minProperties"] = 1
	routingRuleCreateProperties := make(map[string]any, len(routingRuleFieldProperties)+2)
	for key, value := range routingRuleFieldProperties {
		routingRuleCreateProperties[key] = value
	}
	routingRuleCreateProperties["sync_source_rule_id"] = nullablePositiveID("仅创建时可用：复制另一条 path_stage 规则的名称与匹配条件")
	routingRuleCreateProperties["sync_enabled"] = map[string]any{"type": "boolean", "default": false, "description": "仅创建时可用：持续同步源规则的名称与匹配条件；为 true 时必须提供 sync_source_rule_id"}
	routingRuleCreateFields := closedObject(routingRuleCreateProperties, "action")
	routingRuleCreateFields["anyOf"] = []any{
		map[string]any{"required": []string{"name"}},
		map[string]any{"required": []string{"sync_source_rule_id"}},
	}
	routingRuleCreateFields["allOf"] = routingRuleCreateConstraints()
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
	routingRuleSetProperties := map[string]any{
		"name":            map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "远程规则集名称"},
		"url":             map[string]any{"type": "string", "format": "uri", "pattern": "^https://", "maxLength": 2048, "description": "不含凭据或 fragment 的 HTTPS URL；创建和来源变更会立即抓取校验"},
		"format":          map[string]any{"type": "string", "enum": []string{"singbox_source", "singbox_binary", "mihomo_domain", "mihomo_ipcidr", "mihomo_classical", "blackmatrix_classical"}, "description": "sing-box source/binary 或受支持的 Mihomo/Blackmatrix7 文本格式；.mrs 不受支持"},
		"mihomo_behavior": map[string]any{"type": "string", "enum": []string{"", "domain", "ipcidr", "classical", "blackmatrix_classical"}, "description": "由 format 规范化，通常无需显式提供"},
	}
	routingRuleSetFields := closedObject(routingRuleSetProperties)
	routingRuleSetFields["minProperties"] = 1
	routingRuleSetCreateFields := closedObject(routingRuleSetProperties, "name", "url", "format")
	reads := []Descriptor{
		{Name: "outbounds.list", Description: "列出服务器出口（下一跳）及其认证配置状态", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(outbound)), RequiredScopes: []string{"outbounds:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "routing_rules.list", Description: "列出全部分流规则", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(routingRule)), RequiredScopes: []string{"routing_rules:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "routing_rule_sets.list", Description: "列出可复用远程分流规则集及刷新状态", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(routingRuleSet)), RequiredScopes: []string{"routing_rule_sets:read"}, ResourceTypes: []string{"routing_rule_set"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "warp_profiles.list", Description: "列出各服务器的 WARP 配置档状态（不含私钥）", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(warpProfile)), RequiredScopes: []string{"warp_profiles:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
	}
	familySplitTemplate := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue,
		"ipv4_path_id": positiveID, "ipv6_path_id": positiveID,
		"created_at": stringValue, "updated_at": stringValue,
	})
	familySplitTemplateFields := closedObject(map[string]any{
		"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "双栈模板名称；去空白后大小写不敏感唯一"},
	}, "name")
	familySplitTemplateFields["minProperties"] = 1
	reads = append(reads, Descriptor{
		Name: "family_split_templates.list", Description: "列出可复用 IPv4/IPv6 双栈模板及其分支路径 ID",
		InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(familySplitTemplate)),
		RequiredScopes: []string{"family_split_templates:read"}, ResourceTypes: []string{"family_split_template"},
		ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true,
		MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs,
	})
	reads = append(reads, trafficWriteDescriptors(outbound, routingRule, outboundFields, routingRuleCreateFields, routingRuleFields, positiveID, stringValue, boolValue, nullableInteger)...)
	for _, descriptor := range []Descriptor{
		{Name: "family_split_templates.create", Description: "创建可复用 IPv4/IPv6 双栈模板及其空分支", InputSchema: schemaObject(map[string]any{"family_split_template": familySplitTemplateFields}, "family_split_template"), OutputSchema: schemaObject(map[string]any{"family_split_template": familySplitTemplate}, "family_split_template"), RequiredScopes: []string{"family_split_templates:write"}, ResourceTypes: []string{"family_split_template"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: noRefs},
		{Name: "family_split_templates.update", Description: "重命名双栈模板", InputSchema: schemaObject(map[string]any{"family_split_template_id": positiveID, "changes": familySplitTemplateFields}, "family_split_template_id", "changes"), OutputSchema: schemaObject(map[string]any{"family_split_template": familySplitTemplate, "changed_fields": stringArray(1, 8)}, "family_split_template"), RequiredScopes: []string{"family_split_templates:write"}, ResourceTypes: []string{"family_split_template"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("family_split_templates.update")},
		{Name: "family_split_templates.delete", Description: "删除未被分流规则引用的双栈模板", InputSchema: schemaObject(map[string]any{"family_split_template_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "family_split_template_id", "confirm"), OutputSchema: schemaObject(map[string]any{"deleted": boolValue, "family_split_template_id": positiveID}, "deleted"), RequiredScopes: []string{"family_split_templates:write"}, ResourceTypes: []string{"family_split_template"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, Destructive: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("family_split_templates.delete")},
	} {
		reads = append(reads, descriptor)
	}
	placements := map[string]any{"type": "array", "minItems": 1, "maxItems": 512, "items": closedObject(map[string]any{"rule_id": positiveID, "stage_step_id": nullablePositiveID("目标受控节点；null 表示分支根入口"), "sort_position": map[string]any{"type": "integer", "minimum": 0}}, "rule_id", "sort_position")}
	for _, descriptor := range []Descriptor{
		{Name: "routing_rules.place", Description: "原子移动并重排代理分支节点规则", InputSchema: schemaObject(map[string]any{"proxy_path_id": positiveID, "placements": placements}, "proxy_path_id", "placements"), OutputSchema: schemaObject(map[string]any{"proxy_path_id": positiveID, "placements": placements}, "proxy_path_id", "placements"), RequiredScopes: []string{"routing_rules:write"}, ResourceTypes: []string{"proxy_path", "routing_rule"}, ResourceEvaluator: "server_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rules.place")},
		{Name: "routing_rule_sets.create", Description: "创建并首次校验远程分流规则集", InputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSetCreateFields}, "routing_rule_set"), OutputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSet}, "routing_rule_set"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: noRefs},
		{Name: "routing_rule_sets.update", Description: "修改并校验远程分流规则集", InputSchema: schemaObject(map[string]any{"routing_rule_set_id": positiveID, "changes": routingRuleSetFields}, "routing_rule_set_id", "changes"), OutputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSet, "changed_fields": stringArray(1, 8)}, "routing_rule_set"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rule_sets.update")},
		{Name: "routing_rule_sets.delete", Description: "删除未被引用的远程分流规则集", InputSchema: schemaObject(map[string]any{"routing_rule_set_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "routing_rule_set_id", "confirm"), OutputSchema: schemaObject(map[string]any{"deleted": boolValue, "routing_rule_set_id": positiveID}, "deleted"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, Destructive: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rule_sets.delete")},
		{Name: "routing_rule_sets.refresh", Description: "立即刷新远程分流规则集并在内容变化时下发", InputSchema: schemaObject(map[string]any{"routing_rule_set_id": positiveID}, "routing_rule_set_id"), OutputSchema: schemaObject(map[string]any{"routing_rule_set": routingRuleSet, "changed": boolValue}, "routing_rule_set", "changed"), RequiredScopes: []string{"routing_rule_sets:write"}, ResourceTypes: []string{"routing_rule_set"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: trafficWriteResolver("routing_rule_sets.refresh")},
	} {
		reads = append(reads, descriptor)
	}
	return reads
}

func routingRuleCreateConstraints() []any {
	return []any{
		map[string]any{
			"if":   map[string]any{"properties": map[string]any{"scope": map[string]any{"const": "path_stage"}}, "required": []string{"scope"}},
			"then": map[string]any{"required": []string{"proxy_path_id"}},
		},
		map[string]any{
			"if":   map[string]any{"not": map[string]any{"properties": map[string]any{"scope": map[string]any{"const": "path_stage"}}, "required": []string{"scope"}}},
			"then": map[string]any{"required": []string{"server_id"}},
		},
		map[string]any{
			"if":   map[string]any{"properties": map[string]any{"match_source": map[string]any{"const": "rule_set"}}, "required": []string{"match_source"}},
			"then": map[string]any{"properties": map[string]any{"scope": map[string]any{"const": "path_stage"}}, "required": []string{"scope", "rule_set_id"}},
		},
		map[string]any{
			"if":   map[string]any{"properties": map[string]any{"sync_enabled": map[string]any{"const": true}}, "required": []string{"sync_enabled"}},
			"then": map[string]any{"required": []string{"sync_source_rule_id"}},
		},
		map[string]any{
			"if":   map[string]any{"properties": map[string]any{"action": map[string]any{"const": "outbound"}}, "required": []string{"action"}},
			"then": map[string]any{"required": []string{"outbound_id"}},
		},
		map[string]any{
			"if":   map[string]any{"properties": map[string]any{"action": map[string]any{"const": "external"}}, "required": []string{"action"}},
			"then": map[string]any{"required": []string{"external_outbound_id"}},
		},
		map[string]any{
			"if": map[string]any{"properties": map[string]any{"action": map[string]any{"const": "proxy_path"}}, "required": []string{"action"}},
			"then": map[string]any{
				"properties": map[string]any{"scope": map[string]any{"const": "path_stage"}},
				"required":   []string{"scope", "proxy_path_id", "target_proxy_path_id"},
				"not":        map[string]any{"required": []string{"interface_name", "source_prefix"}},
			},
		},
		map[string]any{
			"if": map[string]any{"properties": map[string]any{"action": map[string]any{"const": "family_split"}}, "required": []string{"action"}},
			"then": map[string]any{
				"properties": map[string]any{"scope": map[string]any{"const": "path_stage"}},
				"required":   []string{"scope", "proxy_path_id", "family_split_template_id"},
			},
		},
		map[string]any{
			"if":   map[string]any{"properties": map[string]any{"action": map[string]any{"const": "interface"}}, "required": []string{"action"}},
			"then": map[string]any{"required": []string{"interface_name"}},
		},
		map[string]any{
			"if":   map[string]any{"properties": map[string]any{"action": map[string]any{"const": "source_prefix"}}, "required": []string{"action"}},
			"then": map[string]any{"required": []string{"source_prefix"}},
		},
	}
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
		{"routing_rules.batch_delete", "原子删除一组分流规则", schemaObject(map[string]any{"routing_rule_ids": idArray(1, 256), "confirm": map[string]any{"type": "boolean", "const": true}}, "routing_rule_ids", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "routing_rule_ids": idArray(1, 256)}, "deleted", "routing_rule_ids"), 3, true},
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
	case strings.HasPrefix(name, "family_split_templates."):
		return "family_split_templates:write"
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
			for field, resourceType := range map[string]string{"server_id": "server", "proxy_path_id": "proxy_path", "target_proxy_path_id": "proxy_path", "family_split_template_id": "family_split_template", "sync_source_rule_id": "routing_rule", "rule_set_id": "routing_rule_set", "outbound_id": "outbound", "external_outbound_id": "external_outbound"} {
				if id, ok := int64Value(nested[field]); ok && id > 0 {
					refs = append(refs, mcpauth.ResourceRef{Type: resourceType, ID: strconv.FormatInt(id, 10)})
				}
			}
		case "routing_rules.update":
			if err := addRef("routing_rule_id", "routing_rule"); err != nil {
				return nil, err
			}
			changes, _ := object["changes"].(map[string]any)
			for field, resourceType := range map[string]string{"server_id": "server", "proxy_path_id": "proxy_path", "target_proxy_path_id": "proxy_path", "family_split_template_id": "family_split_template", "rule_set_id": "routing_rule_set", "outbound_id": "outbound", "external_outbound_id": "external_outbound"} {
				if id, ok := int64Value(changes[field]); ok && id > 0 {
					refs = append(refs, mcpauth.ResourceRef{Type: resourceType, ID: strconv.FormatInt(id, 10)})
				}
			}
		case "routing_rules.delete":
			if err := addRef("routing_rule_id", "routing_rule"); err != nil {
				return nil, err
			}
		case "routing_rules.batch_delete":
			if values, ok := object["routing_rule_ids"].([]any); ok {
				for _, value := range values {
					if id, ok := int64Value(value); ok && id > 0 {
						refs = append(refs, mcpauth.ResourceRef{Type: "routing_rule", ID: strconv.FormatInt(id, 10)})
					}
				}
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
		case "family_split_templates.update", "family_split_templates.delete":
			if err := addRef("family_split_template_id", "family_split_template"); err != nil {
				return nil, err
			}
		}
		return refs, nil
	}
}
