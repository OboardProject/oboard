package capability

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// networkDescriptors builds the DNS list, DNS credential, DNS record, DNS
// policy/test, and MTU capability set mirroring the panel's DNS and MTU tabs.

func networkDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	dnsCandidate := closedObject(map[string]any{
		"tag": stringValue, "transport": stringValue, "server": stringValue,
		"port": map[string]any{"type": "integer"}, "path": stringValue, "tls_name": stringValue,
	})
	dnsList := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "kind": stringValue,
		"revision_number": map[string]any{"type": "integer"}, "candidates": arrayOf(dnsCandidate),
		"enabled": boolValue, "protected": boolValue, "usage_count": map[string]any{"type": "integer"},
		"created_at": stringValue, "updated_at": stringValue,
	})
	dnsListFields := closedObject(map[string]any{
		"name":       map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"kind":       map[string]any{"type": "string", "enum": []string{"encrypted", "bootstrap"}},
		"candidates": map[string]any{"type": "array", "maxItems": 64, "items": dnsCandidate},
		"enabled":    boolValue,
	}, "name", "kind", "candidates")
	dnsCredential := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "provider": stringValue,
		"configured": boolValue, "enabled": boolValue, "verified_at": nullableString(),
		"last_error": stringValue, "zones": arrayOf(closedObject(map[string]any{
			"id": positiveID, "credential_id": positiveID, "zone_name": stringValue, "provider_zone_id": stringValue, "server_id": nullableInteger(),
		})),
		"created_at": stringValue, "updated_at": stringValue,
	})
	dnsRecord := closedObject(map[string]any{
		"id": stringValue, "dns_zone_id": positiveID, "zone_name": stringValue,
		"type": stringValue, "name": stringValue, "content": stringValue,
		"proxied": boolValue, "ttl": map[string]any{"type": "integer"}, "enabled": boolValue,
		"comment": stringValue, "server_id": nullableInteger(), "inbound_id": nullableInteger(),
	})
	dnsRecordFields := closedObject(map[string]any{
		"dns_zone_id": positiveID,
		"type":        map[string]any{"type": "string", "enum": []string{"A", "AAAA", "CNAME", "TXT"}},
		"name":        map[string]any{"type": "string", "minLength": 1, "maxLength": 253},
		"content":     map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		"proxied":     boolValue,
		"ttl":         map[string]any{"type": "integer", "minimum": 1, "maximum": 2147483647},
		"comment":     map[string]any{"type": "string", "maxLength": 100},
		"server_id":   nullableInteger(),
		"inbound_id":  nullableInteger(),
	}, "dns_zone_id", "type", "name", "content")
	dnsPolicy := closedObject(map[string]any{
		"server_id": positiveID, "revision": map[string]any{"type": "integer"},
		"encrypted_list_id": positiveID, "bootstrap_list_id": positiveID,
		"strategy": stringValue, "auto_test": stringValue, "test_interval_seconds": map[string]any{"type": "integer"},
		"last_attempt_at": nullableString(), "last_success_at": nullableString(), "last_error": stringValue,
		"needs_benchmark": boolValue, "updated_at": stringValue,
	})
	dnsPolicyFields := closedObject(map[string]any{
		"encrypted_list_id":     positiveID,
		"bootstrap_list_id":     positiveID,
		"strategy":              map[string]any{"type": "string", "enum": []string{"auto", "ipv4_only", "ipv6_only", "prefer_ipv4", "prefer_ipv6"}},
		"auto_test":             map[string]any{"type": "string", "enum": []string{"never", "first_apply", "periodic"}},
		"test_interval_seconds": map[string]any{"type": "integer", "minimum": 60},
	}, "encrypted_list_id", "bootstrap_list_id")
	snellProfile := closedObject(map[string]any{
		"id": positiveID, "name": stringValue, "version": map[string]any{"type": "integer", "enum": []int{4, 6}},
		"psk": stringValue, "obfs_mode": map[string]any{"type": "string", "enum": []string{"none", "http"}},
		"obfs_host": stringValue, "mode": map[string]any{"type": "string", "enum": []string{"default", "unshaped", "unsafe-raw"}},
		"reuse": boolValue, "remark": stringValue, "builtin": boolValue, "enabled": boolValue,
		"usage_count": map[string]any{"type": "integer"},
	})
	snellProfileFields := closedObject(map[string]any{
		"name": stringValue, "version": map[string]any{"type": "integer", "enum": []int{4, 6}},
		"psk": stringValue, "obfs_mode": map[string]any{"type": "string", "enum": []string{"none", "http"}},
		"obfs_host": stringValue, "mode": map[string]any{"type": "string", "enum": []string{"default", "unshaped", "unsafe-raw"}},
		"reuse": boolValue, "remark": stringValue, "enabled": boolValue,
	}, "name", "version", "psk")
	nodePresetKinds := []string{"vless-reality", "vless-tls-vision", "vless-ws", "vless-tcp", "hy2-tls", "anytls-basic", "anytls-large-padding", "ss-aes-128-gcm", "ss-aes-256-gcm", "ss-2022-128", "ss-2022-256", "mieru-basic", "socks5-auth"}
	nodePreset := closedObject(map[string]any{
		"id": positiveID, "name": stringValue, "protocol": stringValue,
		"kind":        map[string]any{"type": "string", "enum": nodePresetKinds},
		"config_json": map[string]any{"type": "object"}, "default_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"remark": stringValue, "builtin": boolValue, "enabled": boolValue,
		"usage_count": map[string]any{"type": "integer"},
		"created_at":  stringValue, "updated_at": stringValue,
	})
	nodePresetFields := closedObject(map[string]any{
		"name": stringValue, "protocol": stringValue,
		"kind":        map[string]any{"type": "string", "enum": nodePresetKinds},
		"config_json": map[string]any{"type": "object"}, "default_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"remark": stringValue, "enabled": boolValue,
	}, "name", "kind")
	reads := []Descriptor{
		{Name: "node_presets.list", Description: "列出全部节点配置预设（含内置模板与引用计数，不含密钥）", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(nodePreset)), RequiredScopes: []string{"node_presets:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "snell_profiles.list", Description: "列出全部 Snell 参数预设（含内置预设与引用计数）", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(snellProfile)), RequiredScopes: []string{"snell_profiles:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"snell_profile"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "dns_lists.list", Description: "列出全部加密与引导 DNS 解析列表", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(dnsList)), RequiredScopes: []string{"dns_lists:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "dns_credentials.list", Description: "列出 DNS 服务商账号元数据与绑定区域，不含任何凭据", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(dnsCredential)), RequiredScopes: []string{"dns_credentials:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"dns_credential"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "dns_records.list", Description: "列出指定 DNS 区域的全部解析记录（实时读取服务商）", InputSchema: schemaObject(map[string]any{"dns_zone_id": positiveID}, "dns_zone_id"), OutputSchema: rawSchema(arrayOf(dnsRecord)), RequiredScopes: []string{"dns_records:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: false, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "servers.dns_policy.get", Description: "读取指定服务器的 DNS 策略与最近检查状态", InputSchema: schemaObject(map[string]any{"server_id": positiveID}, "server_id"), OutputSchema: rawSchema(dnsPolicy), RequiredScopes: []string{"dns:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromID},
	}
	writes := []struct {
		name, description string
		input, output     json.RawMessage
		risk              int
		destructive       bool
		admin             bool
	}{
		{"dns_lists.create", "创建加密或引导 DNS 解析列表", schemaObject(map[string]any{"dns_list": dnsListFields}, "dns_list"), schemaObject(map[string]any{"dns_list": dnsList}, "dns_list"), 2, false, true},
		{"node_presets.create", "创建节点配置预设（VLESS / HY2 / AnyTLS / SS / Mieru / SOCKS5）", schemaObject(map[string]any{"node_preset": nodePresetFields}, "node_preset"), schemaObject(map[string]any{"node_preset": nodePreset}, "node_preset"), 2, false, true},
		{"node_presets.update", "修改节点配置预设", schemaObject(map[string]any{"node_preset_id": positiveID, "changes": nodePresetFields}, "node_preset_id", "changes"), schemaObject(map[string]any{"node_preset": nodePreset, "changed_fields": stringArray(1, 32)}, "node_preset"), 2, false, true},
		{"node_presets.delete", "删除未被引用的节点配置预设（内置预设不可删除）", schemaObject(map[string]any{"node_preset_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "node_preset_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "node_preset_id": positiveID}, "deleted"), 3, true, true},
		{"node_presets.restore_system", "将全部内置节点预设恢复为系统模板（覆盖内置模板上的自定义修改，保留引用）", schemaObject(map[string]any{"confirm": map[string]any{"type": "boolean", "const": true}}, "confirm"), schemaObject(map[string]any{"restored": map[string]any{"type": "integer", "minimum": 0}, "node_presets": arrayOf(nodePreset)}, "restored"), 3, true, true},
		{"snell_profiles.create", "创建 Snell 参数预设（多入站可共享）", schemaObject(map[string]any{"snell_profile": snellProfileFields}, "snell_profile"), schemaObject(map[string]any{"snell_profile": snellProfile}, "snell_profile"), 2, false, true},
		{"snell_profiles.update", "修改 Snell 参数预设", schemaObject(map[string]any{"snell_profile_id": positiveID, "changes": snellProfileFields}, "snell_profile_id", "changes"), schemaObject(map[string]any{"snell_profile": snellProfile, "changed_fields": stringArray(1, 32)}, "snell_profile"), 2, false, true},
		{"snell_profiles.delete", "删除未被引用的 Snell 参数预设（内置预设不可删除）", schemaObject(map[string]any{"snell_profile_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "snell_profile_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "snell_profile_id": positiveID}, "deleted"), 3, true, true},
		{"dns_lists.update", "修改 DNS 解析列表并触发关联基准检查", schemaObject(map[string]any{"dns_list_id": positiveID, "changes": dnsListFields}, "dns_list_id", "changes"), schemaObject(map[string]any{"dns_list": dnsList, "changed_fields": stringArray(1, 32)}, "dns_list"), 2, false, true},
		{"dns_lists.delete", "删除未被引用且非默认的 DNS 解析列表", schemaObject(map[string]any{"dns_list_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "dns_list_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "dns_list_id": positiveID}, "deleted"), 3, true, true},
		{"dns_lists.set_default", "将指定 DNS 解析列表设为全局默认", schemaObject(map[string]any{"dns_list_id": positiveID}, "dns_list_id"), schemaObject(map[string]any{"dns_list": dnsList}, "dns_list"), 2, false, true},
		{"servers.dns_policy.set", "设置指定服务器的 DNS 解析策略", schemaObject(map[string]any{"server_id": positiveID, "changes": dnsPolicyFields}, "server_id", "changes"), schemaObject(map[string]any{"dns_policy": dnsPolicy, "changed_fields": stringArray(1, 32)}, "dns_policy"), 2, false, false},
		{"servers.dns_test", "对指定服务器发起一次 DNS 解析基准检查任务", schemaObject(map[string]any{"server_id": positiveID, "action": map[string]any{"type": "string", "enum": []string{"test", "test_and_apply"}}, "apply_on_success": boolValue}, "server_id"), schemaObject(map[string]any{"task": closedObject(map[string]any{"id": positiveID, "type": stringValue, "status": stringValue})}, "task"), 2, false, false},
		{"servers.mtu_detect", "对指定服务器发起一次路径 MTU 检测任务", schemaObject(map[string]any{"server_id": positiveID, "target_host": map[string]any{"type": "string", "maxLength": 255}, "target_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "interface_name": map[string]any{"type": "string", "maxLength": 64}, "overhead_bytes": map[string]any{"type": "integer", "minimum": 0}, "desired_mtu": map[string]any{"type": "integer", "minimum": 1280, "maximum": 9000}}, "server_id"), schemaObject(map[string]any{"task": closedObject(map[string]any{"id": positiveID, "type": stringValue, "status": stringValue})}, "task"), 2, false, false},
		{"dns_records.create", "在指定 DNS 区域创建解析记录（实时写入服务商）", schemaObject(map[string]any{"record": dnsRecordFields}, "record"), schemaObject(map[string]any{"dns_record": dnsRecord}, "dns_record"), 2, false, false},
		{"dns_records.update", "更新指定 DNS 区域中的解析记录", schemaObject(map[string]any{"dns_zone_id": positiveID, "record_id": stringValue, "changes": dnsRecordFields}, "dns_zone_id", "record_id", "changes"), schemaObject(map[string]any{"dns_record": dnsRecord, "changed_fields": stringArray(1, 32)}, "dns_record"), 2, false, false},
		{"dns_records.delete", "删除指定 DNS 区域中的解析记录", schemaObject(map[string]any{"dns_zone_id": positiveID, "record_id": stringValue, "confirm": map[string]any{"type": "boolean", "const": true}}, "dns_zone_id", "record_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "record_id": stringValue}, "deleted"), 3, true, false},
	}
	for _, write := range writes {
		descriptor := Descriptor{
			Name: write.name, Description: write.description, InputSchema: write.input, OutputSchema: write.output,
			RequiredScopes: []string{networkScopeFor(write.name)}, ResourceTypes: []string{"server", "dns_zone"},
			ResourceEvaluator: "server_ids", RiskClass: write.risk, ApprovalPolicy: "required", Idempotent: true,
			DataClassification: DataInternal, Destructive: write.destructive, MCPEnabled: true, Executable: true,
			MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: networkWriteResolver(write.name),
		}
		if write.admin {
			descriptor.RBACPermission = "admin.settings"
		}
		reads = append(reads, descriptor)
	}
	return reads
}

func networkScopeFor(name string) string {
	switch {
	case name == "dns_records.create" || name == "dns_records.update" || name == "dns_records.delete":
		return "dns_records:write"
	case name == "node_presets.create" || name == "node_presets.update" || name == "node_presets.delete" || name == "node_presets.restore_system":
		return "node_presets:write"
	case name == "snell_profiles.create" || name == "snell_profiles.update" || name == "snell_profiles.delete":
		return "snell_profiles:write"
	case name == "servers.dns_test":
		return "dns:write"
	default:
		return "dns:write"
	}
}

func networkWriteResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
	return func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		refs := []mcpauth.ResourceRef{}
		addServer := func(nested map[string]any) {
			if id, ok := int64Value(nested["server_id"]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
			}
		}
		switch name {
		case "servers.dns_policy.set", "servers.dns_test", "servers.mtu_detect":
			if id, ok := int64Value(object["server_id"]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
			}
		case "dns_records.create":
			record, _ := object["record"].(map[string]any)
			if id, ok := int64Value(record["server_id"]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
			}
		case "dns_records.update":
			changes, _ := object["changes"].(map[string]any)
			addServer(changes)
		case "dns_records.delete":
			// zone-scoped deletion has no server reference
		}
		return refs, nil
	}
}
