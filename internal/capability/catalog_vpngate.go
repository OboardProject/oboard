package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func vpngateDescriptors(id, text, boolean map[string]any) []Descriptor {
	integer := func(min, max int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	pool := map[string]any{
		"id":                id,
		"name":              map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"countries":         stringArray(0, 32),
		"operator_asns":     stringArray(0, 64),
		"network":           map[string]any{"type": "string", "enum": []string{"any", "tcp", "udp"}},
		"select_mode":       map[string]any{"type": "string", "enum": []string{"latency", "quality"}},
		"max_rtt_ms":        integer(0, 5000),
		"min_quality_score": integer(0, 100),
		"hot_standby":       integer(0, 2),
		"pool_size":         integer(1, 8),
		"switch_policy":     map[string]any{"type": "string", "enum": []string{"on_failure", "improve"}},
		"refresh_minutes":   integer(15, 1440),
		"enabled":           boolean,
	}
	object := rawSchema(map[string]any{"type": "object"})
	write := func(d Descriptor) Descriptor {
		d.RequiredScopes = []string{"servers:write"}
		d.MinimumAccess = mcpauth.AccessOperate
		d.RiskClass = 2
		d.ApprovalPolicy = "required"
		d.RBACPermission = "servers.update"
		d.Executable = true
		d.MCPEnabled = true
		d.DataClassification = DataInternal
		d.ResolveResourceRefs = noRefs
		return d
	}
	read := func(d Descriptor) Descriptor {
		d.RequiredScopes = []string{"servers:read"}
		d.MinimumAccess = mcpauth.AccessRead
		d.ReadOnly = true
		d.Idempotent = true
		d.MCPEnabled = true
		d.DataClassification = DataInternal
		d.ResolveResourceRefs = noRefs
		return d
	}
	return []Descriptor{
		read(Descriptor{Name: "vpngate.overview", Description: "读取 VPNGate 出口池、目录状态、每台出口服务器的当前成员、候选评分、切换记录与内核健康；成员为空时该出口拒绝出站", InputSchema: schemaObject(map[string]any{}), OutputSchema: object}),
		read(Descriptor{Name: "vpngate.catalog.list", Description: "列出已累积的 VPNGate 服务器目录（不含证书与配置），可按国家/地区过滤", InputSchema: schemaObject(map[string]any{"country": map[string]any{"type": "string", "maxLength": 2}, "limit": integer(1, 500)}), OutputSchema: object}),
		write(Descriptor{Name: "vpngate.pools.save", Description: "创建或更新 VPNGate 出口池：国家/地区、运营商 ASN、协议、延迟优先或质量优先、门槛、热备与切换策略。调整只经运行时通道下发，不触发部署", InputSchema: schemaObject(pool, "name"), OutputSchema: schemaObject(map[string]any{"id": id}, "id"), Idempotent: true}),
		write(Descriptor{Name: "vpngate.pools.delete", Description: "删除未被任何代理拓扑使用的 VPNGate 出口池", InputSchema: schemaObject(map[string]any{"id": id}, "id"), OutputSchema: schemaObject(map[string]any{"id": id}, "id"), Destructive: true, Idempotent: true}),
		write(Descriptor{Name: "vpngate.pools.reselect", Description: "立即重新获取目录（可选）并对出口池重新测试、评分与选择成员；结果随后通过 vpngate.overview 读取", InputSchema: schemaObject(map[string]any{"id": id, "refresh_catalog": boolean}, "id"), OutputSchema: schemaObject(map[string]any{"id": id}, "id")}),
	}
}
