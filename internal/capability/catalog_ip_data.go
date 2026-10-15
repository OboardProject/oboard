package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

// ipDataSchemas describes the attribution / quality report shared by
// ip_data.lookup and the ip_quality probe monitor result.
func ipDataSchemas(text, boolean map[string]any) (attribution, quality map[string]any) {
	integer := map[string]any{"type": "integer"}
	score := map[string]any{"type": "integer", "minimum": 0, "maximum": 100}
	source := closedObject(map[string]any{"provider": text, "status": text, "error": text, "checked_at": text, "keyed": boolean, "asn": text, "as_name": text, "organization": text, "country_code": text, "country": text, "region": text, "city": text, "proxy": boolean, "vpn": boolean, "tor": boolean, "hosting": boolean, "abuser": boolean, "risk": score, "confidence": score, "abuse_score": score, "reports": integer})
	agreement := closedObject(map[string]any{"agree": integer, "total": integer})
	conflict := closedObject(map[string]any{"provider": text, "field": text, "value": text})
	attribution = closedObject(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"ok", "degraded", "offline", "unknown"}}, "primary": text, "asn": text, "as_name": text, "country_code": text, "country": text, "region": text, "city": text, "field_sources": map[string]any{"type": "object", "additionalProperties": text}, "asn_agreement": agreement, "country_agreement": agreement, "conflicts": arrayOf(conflict), "sources": arrayOf(source)})
	verdict := closedObject(map[string]any{"state": map[string]any{"type": "string", "enum": []string{"yes", "no", "conflict", "unknown"}}, "positive": integer, "negative": integer, "flagged_by": stringArray(0, 8)})
	quality = closedObject(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"ok", "partial", "unknown"}}, "proxy": verdict, "vpn": verdict, "tor": verdict, "hosting": verdict, "abuser": verdict, "risk": score, "risk_source": text, "abuse_score": score, "abuse_source": text, "reports": integer, "sources": arrayOf(source)})
	return attribution, quality
}

func ipDataDescriptors(text, boolean map[string]any) []Descriptor {
	integer := map[string]any{"type": "integer"}
	attribution, quality := ipDataSchemas(text, boolean)
	provider := closedObject(map[string]any{"name": text, "label": text, "roles": stringArray(0, 4), "key_required": boolean, "key_label": text, "keyless": text, "signup_url": text, "configured": boolean, "used_today": integer, "daily_budget": integer, "cooldown_until": text, "last_status": text, "last_error": text, "last_checked_at": text})
	offline := closedObject(map[string]any{"available": boolean, "provider": text, "error": text})
	return []Descriptor{
		{Name: "ip_data.providers.list", Description: "列出 IP 归属、IP 质量与路由数据源的配置状态、本地额度与冷却情况；只返回是否已配置，从不返回密钥。密钥只能由交互式管理员在面板填写", InputSchema: schemaObject(nil), OutputSchema: schemaObject(map[string]any{"providers": arrayOf(provider), "offline": offline}, "providers", "offline"), RequiredScopes: []string{"settings:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "ip_data.lookup", Description: "查询一个公网 IP 的归属信息（以 IPinfo 为准，ipapi.is / IPWhois / Proxycheck / 离线库交叉校验并自动降级）与质量信息（Proxycheck、ipapi.is、AbuseIPDB 各自判断后投票）；每个来源原始结果一并返回，未知不补零，不合成纯净度总分。会消耗数据源额度，结果有缓存", InputSchema: schemaObject(map[string]any{"ip": map[string]any{"type": "string", "minLength": 2, "maxLength": 45}, "attribution": boolean, "quality": boolean}, "ip"), OutputSchema: schemaObject(map[string]any{"ip": text, "checked_at": text, "attribution": attribution, "quality": quality}, "ip", "checked_at"), RequiredScopes: []string{"servers:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
	}
}
