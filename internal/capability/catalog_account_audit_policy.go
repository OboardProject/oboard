package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func accountAuditPolicyDescriptors() []Descriptor {
	integer := func(min, max int64) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	scale := func(max int64) map[string]any {
		return closedObject(map[string]any{"start": integer(0, max-1), "full": integer(1, max)}, "start", "full")
	}
	threshold := func(unit string) map[string]any {
		return map[string]any{"anyOf": []any{map[string]any{"type": "null"}, closedObject(map[string]any{"start": map[string]any{"type": "integer", "minimum": 0, "maximum": uint64(18446744073709551614)}, "full": map[string]any{"type": "integer", "minimum": 1, "maximum": ^uint64(0)}, "unit": map[string]any{"type": "string", "enum": []string{unit}}}, "start", "full", "unit")}}
	}
	config := map[string]any{
		"revision":        integer(0, 9223372036854775806),
		"source_grouping": closedObject(map[string]any{"ipv4_prefix_bits": integer(0, 24), "ipv6_prefix_bits": integer(0, 56), "epoch": integer(1, 9007199254740991)}, "ipv4_prefix_bits", "ipv6_prefix_bits", "epoch"),
		"policy": closedObject(map[string]any{
			"version":          map[string]any{"type": "string", "pattern": "^account-risk-policy-v1:[0-9]+$"},
			"activity_sources": scale(32), "activity_minutes": scale(30), "exposure_sources": scale(32),
			"source_capacity": map[string]any{"type": "integer", "enum": []int{32}}, "minimum_bytes": integer(1, 1<<40), "minimum_slices": integer(1, 12),
		}, "version", "activity_sources", "activity_minutes", "exposure_sources", "source_capacity", "minimum_bytes", "minimum_slices"),
		"resources": closedObject(map[string]any{"request_rate": threshold("requests/second"), "connections": threshold("connections"), "traffic_rate": threshold("bytes/second")}, "request_rate", "connections", "traffic_rate"),
	}
	output := map[string]any{}
	for k, v := range config {
		output[k] = v
	}
	output["source"] = closedObject(map[string]any{
		"version":          map[string]any{"type": "string"},
		"epoch":            integer(1, 9007199254740991),
		"ipv4_prefix_bits": integer(0, 24), "ipv6_prefix_bits": integer(0, 56),
		"rotation_supported": map[string]any{"type": "boolean", "enum": []bool{true}}, "rotation_unknown_days": map[string]any{"type": "integer", "enum": []int{7}},
	}, "version", "epoch", "ipv4_prefix_bits", "ipv6_prefix_bits", "rotation_supported", "rotation_unknown_days")
	output["resource_measurement"] = map[string]any{"type": "string", "enum": []string{"coverage_required"}}
	return []Descriptor{
		{Name: "audit.policy.get", Description: "读取账号风险阈值、策略版本与版本化来源分组；资源观测不可用不等于零", InputSchema: schemaObject(nil), OutputSchema: schemaObject(output, "revision", "policy", "resources", "source_grouping", "source", "resource_measurement"), RequiredScopes: []string{"audit:read"}, ReadOnly: true, Idempotent: true, MCPEnabled: true, AdminOnly: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", DataClassification: DataInternal, ResolveResourceRefs: noRefs},
		{Name: "audit.policy.update", Description: "通过审批替换账号风险与资源观测阈值；可合并来源网段或递增来源代次，历史比较重新积累；不改变采集、授权或配额，不自动封禁", InputSchema: schemaObject(config, "revision", "policy", "resources", "source_grouping"), OutputSchema: schemaObject(config, "revision", "policy", "resources", "source_grouping"), RequiredScopes: []string{"audit:write"}, Executable: true, Idempotent: true, MCPEnabled: true, AdminOnly: true, RiskClass: 2, ApprovalPolicy: "required", MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", DataClassification: DataInternal, ResolveResourceRefs: noRefs},
	}
}
