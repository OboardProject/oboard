package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func auditCollectionDescriptors() []Descriptor {
	text := map[string]any{"type": "string"}
	config := map[string]any{
		"mode":     map[string]any{"type": "string", "enum": []string{"light", "standard"}},
		"revision": map[string]any{"type": "integer", "minimum": 0},
		"diagnostics": map[string]any{"type": "array", "maxItems": 8, "items": closedObject(map[string]any{
			"scope": map[string]any{"type": "string", "enum": []string{"user", "node"}},
			"id":    map[string]any{"type": "integer", "minimum": 1}, "until": text,
		}, "scope", "id", "until")},
	}
	return []Descriptor{
		{Name: "audit.collection.get", Description: "读取独立采集档位及限时诊断；不触发风险扫描", InputSchema: schemaObject(nil), OutputSchema: schemaObject(config, "mode", "revision", "diagnostics"), RequiredScopes: []string{"audit:read"}, ReadOnly: true, Idempotent: true, MCPEnabled: true, AdminOnly: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", DataClassification: DataInternal, ResolveResourceRefs: noRefs},
		{Name: "audit.collection.update", Description: "设置轻量/标准采集与最多8个一小时内到期的诊断对象；不能补回未采集历史，不改变风险阈值、鉴权或配额", InputSchema: schemaObject(config, "mode", "revision", "diagnostics"), OutputSchema: schemaObject(config, "mode", "revision", "diagnostics"), RequiredScopes: []string{"audit:write"}, Executable: true, Idempotent: true, MCPEnabled: true, AdminOnly: true, RiskClass: 2, ApprovalPolicy: "required", MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", DataClassification: DataInternal, ResolveResourceRefs: noRefs},
	}
}
