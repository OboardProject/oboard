package capability

import (
	"context"
	"fmt"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

func pluginWebhookDescriptors() []Descriptor {
	id := map[string]any{"type": "integer", "minimum": 1}
	endpoint := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	text := map[string]any{"type": "string"}
	flag := map[string]any{"type": "boolean"}
	item := closedObject(map[string]any{
		"id": endpoint, "plugin_id": id, "binding_id": id, "binding_revision": id, "revision_id": id, "grant_id": id,
		"enabled": flag, "generation": id, "created_by_user_id": id, "created_at": text, "updated_at": text,
	}, "id", "plugin_id", "binding_id", "binding_revision", "revision_id", "grant_id", "enabled", "generation", "created_by_user_id", "created_at", "updated_at")
	result := schemaObject(map[string]any{"webhook": item, "secret": text}, "webhook")
	refs := func(fields map[string]string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
		return func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
			value, err := canonicalMap(input)
			if err != nil {
				return nil, err
			}
			out := []mcpauth.ResourceRef{}
			for field, kind := range fields {
				if field == "id" {
					v, ok := value[field].(string)
					if !ok || v == "" {
						return nil, fmt.Errorf("%s is required", field)
					}
					out = append(out, mcpauth.ResourceRef{Type: kind, ID: v})
					continue
				}
				v, ok := int64Value(value[field])
				if !ok || v <= 0 {
					return nil, fmt.Errorf("%s must be a positive integer", field)
				}
				out = append(out, mcpauth.ResourceRef{Type: kind, ID: fmt.Sprint(v)})
			}
			return out, nil
		}
	}
	descriptors := []Descriptor{
		{Name: "plugin_webhooks.list", Description: "管理员查看插件 Webhook 绑定，不返回密钥；不向 MCP 开放", InputSchema: schemaObject(map[string]any{"plugin_id": id}, "plugin_id"), OutputSchema: schemaObject(map[string]any{"webhooks": arrayOf(item)}, "webhooks"), ReadOnly: true, Idempotent: true, ResourceTypes: []string{"plugin"}, ResolveResourceRefs: refs(map[string]string{"plugin_id": "plugin"})},
		{Name: "plugin_webhooks.create", Description: "管理员创建固定版本、触发器和授权的 Webhook，默认关闭；密钥仅返回一次", InputSchema: schemaObject(map[string]any{"binding_id": id, "grant_id": id}, "binding_id", "grant_id"), OutputSchema: result, Executable: true, RiskClass: 4, ApprovalPolicy: "required", ResourceTypes: []string{"plugin_trigger", "plugin_grant"}, ResolveResourceRefs: refs(map[string]string{"binding_id": "plugin_trigger", "grant_id": "plugin_grant"})},
		{Name: "plugin_webhooks.update", Description: "管理员启停或轮换 Webhook 密钥，立即取消旧代执行；不向 MCP 开放", InputSchema: schemaObject(map[string]any{"id": endpoint, "expected_generation": id, "enabled": flag, "rotate_secret": flag}, "id", "expected_generation", "enabled"), OutputSchema: result, Executable: true, RiskClass: 4, ApprovalPolicy: "required", ResourceTypes: []string{"plugin_webhook"}, ResolveResourceRefs: refs(map[string]string{"id": "plugin_webhook"})},
	}
	for i := range descriptors {
		descriptors[i].RequiredScopes = []string{"plugins:authorize"}
		descriptors[i].RBACPermission = "plugins.authorize"
		descriptors[i].AdminOnly = true
		descriptors[i].MCPEnabled = false
		descriptors[i].MinimumAccess = mcpauth.AccessOperate
		descriptors[i].DataClassification = DataSensitive
	}
	return descriptors
}
