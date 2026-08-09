package capability

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// externalOutboundDescriptors builds the imported third-party node
// (external outbound) capability set mirroring the panel's 导入节点 tab.

func externalOutboundDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	externalOutbound := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "server_id": nullableInteger(), "name": stringValue,
		"protocol": stringValue, "scope": stringValue, "target_address": stringValue,
		"target_port": map[string]any{"type": "integer"},
		"region_mode": stringValue, "region_code": stringValue, "effective_region_code": stringValue,
		"region_status": stringValue, "expose_to_users": boolValue, "enabled": boolValue,
		"advanced_configured": boolValue, "created_at": stringValue, "updated_at": stringValue,
	})
	fields := closedObject(map[string]any{
		"name":            map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"protocol":        map[string]any{"type": "string", "enum": []string{"vless", "hysteria2", "anytls", "shadowsocks", "mieru", "socks"}},
		"scope":           map[string]any{"type": "string", "enum": []string{"global", "server"}},
		"server_id":       nullableInteger(),
		"target_address":  map[string]any{"type": "string", "maxLength": 255},
		"target_port":     map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"config_json":     map[string]any{"type": "string", "maxLength": 65536},
		"region_mode":     map[string]any{"type": "string", "enum": []string{"auto", "manual"}},
		"region_code":     map[string]any{"type": "string", "maxLength": 2},
		"expose_to_users": boolValue,
		"enabled":         boolValue,
	})
	reads := []Descriptor{
		{Name: "external_outbounds.list", Description: "列出导入节点（第三方出口）及其地域信息", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(externalOutbound)), RequiredScopes: []string{"external_outbounds:read"}, ResourceTypes: []string{"server", "external_outbound"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
	}
	writes := []struct {
		name, description string
		input, output     json.RawMessage
		risk              int
		destructive       bool
	}{
		{"external_outbounds.import", "从订阅文本或节点 URI 批量导入第三方节点", schemaObject(map[string]any{"content": map[string]any{"type": "string", "minLength": 1, "maxLength": 262144}, "scope": map[string]any{"type": "string", "enum": []string{"global", "server"}}, "server_id": nullableInteger(), "expose_to_users": boolValue}, "content"), schemaObject(map[string]any{"external_outbounds": arrayOf(externalOutbound), "created_count": map[string]any{"type": "integer"}}, "created_count"), 2, false},
		{"external_outbounds.create", "手动创建导入节点", schemaObject(map[string]any{"external_outbound": fields}, "external_outbound"), schemaObject(map[string]any{"external_outbound": externalOutbound}, "external_outbound"), 2, false},
		{"external_outbounds.update", "修改导入节点", schemaObject(map[string]any{"external_outbound_id": positiveID, "changes": fields}, "external_outbound_id", "changes"), schemaObject(map[string]any{"external_outbound": externalOutbound, "changed_fields": stringArray(1, 32)}, "external_outbound"), 2, false},
		{"external_outbounds.delete", "删除导入节点及其引用", schemaObject(map[string]any{"external_outbound_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "external_outbound_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "external_outbound_id": positiveID}, "deleted"), 3, true},
	}
	for _, write := range writes {
		reads = append(reads, Descriptor{
			Name: write.name, Description: write.description, InputSchema: write.input, OutputSchema: write.output,
			RequiredScopes: []string{trafficScopeFor(write.name)}, ResourceTypes: []string{"server", "external_outbound"},
			ResourceEvaluator: "server_ids", RiskClass: write.risk, ApprovalPolicy: "required", Idempotent: true,
			DataClassification: DataInternal, Destructive: write.destructive, MCPEnabled: true, Executable: true,
			MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: externalOutboundResolver(write.name),
		})
	}
	return reads
}

func externalOutboundResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
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
		case "external_outbounds.import", "external_outbounds.create":
			nested := object["external_outbound"].(map[string]any)
			if name == "external_outbounds.import" {
				nested = object
			}
			addServer(nested)
		case "external_outbounds.update":
			if id, ok := int64Value(object["external_outbound_id"]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "external_outbound", ID: strconv.FormatInt(id, 10)})
			}
			changes, _ := object["changes"].(map[string]any)
			addServer(changes)
		case "external_outbounds.delete":
			if id, ok := int64Value(object["external_outbound_id"]); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "external_outbound", ID: strconv.FormatInt(id, 10)})
			}
		}
		return refs, nil
	}
}
