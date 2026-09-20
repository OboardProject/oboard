package capability

import (
	"context"
	"errors"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

func pluginDescriptors(positiveID, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	pluginID := map[string]any{"type": "string", "minLength": 1, "maxLength": 32, "pattern": "^[0-9]+$"}
	plugin := closedObject(map[string]any{
		"id": positiveID, "name": stringValue, "description": stringValue,
		"owner_user_id": positiveID, "status": stringValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	revision := closedObject(map[string]any{
		"id": positiveID, "plugin_id": positiveID, "revision_number": positiveID,
		"status": stringValue, "runtime": stringValue, "sdk_version": stringValue,
		"source_digest": stringValue, "created_at": stringValue,
	})
	trigger := closedObject(map[string]any{
		"id": positiveID, "plugin_id": positiveID, "revision_id": positiveID,
		"name": stringValue, "enabled": boolValue, "kind": stringValue,
		"binding_revision": positiveID,
	})
	run := closedObject(map[string]any{
		"id": positiveID, "uuid": stringValue, "plugin_id": positiveID,
		"revision_id": positiveID, "status": stringValue, "mode": stringValue,
		"error_code": stringValue, "skip_reason": stringValue,
	})
	webhookID := map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}
	webhook := closedObject(map[string]any{
		"id": webhookID, "plugin_id": positiveID, "binding_id": positiveID,
		"binding_revision": positiveID, "revision_id": positiveID, "grant_id": positiveID,
		"enabled": boolValue, "generation": positiveID, "created_by_user_id": positiveID,
		"created_at": stringValue, "updated_at": stringValue,
	})
	integerValue := map[string]any{"type": "integer"}
	runtimeStatus := closedObject(map[string]any{
		"enabled": boolValue, "host_actions_enabled": boolValue, "scheduler_paused": boolValue,
		"recovery_generation": integerValue, "runtime_installed": boolValue, "install_command": stringValue,
		"worker_connected": boolValue, "isolation_available": boolValue, "isolation_mode": stringValue,
		"isolation_reason": stringValue, "active_runs": integerValue, "queued_runs": integerValue,
		"max_concurrency": integerValue,
	}, "enabled", "runtime_installed", "worker_connected", "isolation_available")
	pluginRef := func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		id, ok := int64Value(object["plugin_id"])
		if !ok {
			if raw, ok := object["id"].(string); ok {
				parsed, convErr := strconv.ParseInt(raw, 10, 64)
				if convErr != nil || parsed <= 0 {
					return nil, errors.New("plugin_id must be a positive integer string")
				}
				id = parsed
				ok = true
			} else {
				id, ok = int64Value(object["id"])
			}
		}
		if !ok || id <= 0 {
			return nil, errors.New("plugin_id must be a positive integer ID")
		}
		return []mcpauth.ResourceRef{{Type: "plugin", ID: strconv.FormatInt(id, 10)}}, nil
	}
	return []Descriptor{
		{Name: "plugin_webhooks.list", Description: "管理员查看插件 Webhook 端点，不返回密钥。MCP 不可调用", InputSchema: schemaObject(map[string]any{"plugin_id": positiveID}, "plugin_id"), OutputSchema: schemaObject(map[string]any{"webhooks": arrayOf(webhook)}, "webhooks"), RequiredScopes: []string{"plugins:authorize"}, ResourceTypes: []string{"plugin"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, MCPEnabled: false, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.authorize", AdminOnly: true, ResolveResourceRefs: pluginRef},
		{Name: "plugin_webhooks.create", Description: "管理员创建默认关闭、固定触发器与授权的 Webhook，一次性返回密钥。MCP 不可调用", InputSchema: schemaObject(map[string]any{"binding_id": positiveID, "grant_id": positiveID}, "binding_id", "grant_id"), OutputSchema: schemaObject(map[string]any{"webhook": webhook, "secret": stringValue}, "webhook", "secret"), SensitiveOutput: []string{"secret"}, RequiredScopes: []string{"plugins:authorize"}, ResourceTypes: []string{"plugin"}, RiskClass: 4, ApprovalPolicy: "required", DataClassification: DataSensitive, MCPEnabled: false, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.authorize", AdminOnly: true, ResolveResourceRefs: noRefs},
		{Name: "plugin_webhooks.update", Description: "管理员启停 Webhook 或轮换密钥，撤销旧执行租约。MCP 不可调用", InputSchema: schemaObject(map[string]any{"id": webhookID, "expected_generation": positiveID, "enabled": boolValue, "rotate_secret": boolValue}, "id", "expected_generation", "enabled"), OutputSchema: schemaObject(map[string]any{"webhook": webhook, "secret": stringValue}, "webhook"), SensitiveOutput: []string{"secret"}, RequiredScopes: []string{"plugins:authorize"}, ResourceTypes: []string{"plugin"}, RiskClass: 4, ApprovalPolicy: "required", DataClassification: DataSensitive, MCPEnabled: false, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.authorize", AdminOnly: true, ResolveResourceRefs: noRefs},
		{Name: "plugins.list", Description: "列出授权范围内的插件身份与状态", InputSchema: schemaObject(map[string]any{"status": stringValue, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}), OutputSchema: schemaObject(map[string]any{"plugins": arrayOf(plugin)}, "plugins"), RequiredScopes: []string{"plugins:read"}, ResourceTypes: []string{"plugin"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "plugins.read", ResolveResourceRefs: noRefs},
		{Name: "plugins.get", Description: "读取插件详情、当前草稿与已发布版本摘要", InputSchema: schemaObject(map[string]any{"id": pluginID}, "id"), OutputSchema: schemaObject(map[string]any{"plugin": plugin, "draft": revision, "published": revision}), RequiredScopes: []string{"plugins:read"}, ResourceTypes: []string{"plugin"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "plugins.read", ResolveResourceRefs: pluginRef},
		{Name: "plugins.create", Description: "创建插件草稿，不授予执行权限", InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "description": map[string]any{"type": "string", "maxLength": 2000}}, "name"), OutputSchema: schemaObject(map[string]any{"plugin": plugin}, "plugin"), RequiredScopes: []string{"plugins:write"}, ResourceTypes: []string{"plugin"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.draft", ResolveResourceRefs: noRefs},
		{Name: "plugins.update", Description: "更新插件名称、说明或启用状态；归档后不可再启用", InputSchema: schemaObject(map[string]any{"id": pluginID, "name": stringValue, "description": stringValue, "status": map[string]any{"type": "string", "enum": []string{"enabled", "disabled", "archived"}}, "expected_updated_at": stringValue}, "id"), OutputSchema: schemaObject(map[string]any{"plugin": plugin}, "plugin"), RequiredScopes: []string{"plugins:write"}, ResourceTypes: []string{"plugin"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.draft", ResolveResourceRefs: pluginRef},
		{Name: "plugins.revisions.save", Description: "保存不可发布的草稿源码与运行规范", InputSchema: schemaObject(map[string]any{"plugin_id": pluginID, "source": map[string]any{"type": "string", "minLength": 1, "maxLength": 262144}, "manifest": closedObject(map[string]any{}), "expected_revision_id": nullableInteger()}, "plugin_id", "source", "manifest"), OutputSchema: schemaObject(map[string]any{"revision": revision}, "revision"), RequiredScopes: []string{"plugins:write"}, ResourceTypes: []string{"plugin"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.draft", ResolveResourceRefs: pluginRef},
		{Name: "plugins.revisions.publish", Description: "发布指定草稿版本。发布不继承旧版本高风险授权", InputSchema: schemaObject(map[string]any{"plugin_id": pluginID, "revision_id": pluginID}, "plugin_id", "revision_id"), OutputSchema: schemaObject(map[string]any{"revision": revision}, "revision"), RequiredScopes: []string{"plugins:publish"}, ResourceTypes: []string{"plugin"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.publish", ResolveResourceRefs: pluginRef},
		{Name: "plugins.validate", Description: "校验清单、参数 Schema 与兼容性，不编译或执行用户源码", InputSchema: schemaObject(map[string]any{"plugin_id": pluginID, "revision_id": nullableInteger(), "source": stringValue, "manifest": closedObject(map[string]any{}), "params": closedObject(map[string]any{})}), OutputSchema: schemaObject(map[string]any{"valid": boolValue, "errors": arrayOf(stringValue)}, "valid"), RequiredScopes: []string{"plugins:read"}, ResourceTypes: []string{"plugin"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "plugins.read", ResolveResourceRefs: pluginRef},
		{Name: "plugins.simulate", Description: "在隔离 Runner 中用模拟 SDK 运行，不产生真实动作", InputSchema: schemaObject(map[string]any{"plugin_id": pluginID, "revision_id": nullableInteger(), "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{})}, "plugin_id"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"plugins:execute"}, ResourceTypes: []string{"plugin"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.execute", ResolveResourceRefs: pluginRef},
		{Name: "plugins.run", Description: "手动执行已获准版本，权限与调用者取交集", InputSchema: schemaObject(map[string]any{"plugin_id": pluginID, "revision_id": pluginID, "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{}), "idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "plugin_id", "revision_id", "idempotency_key"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"plugins:execute"}, ResourceTypes: []string{"plugin"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.execute", ResolveResourceRefs: pluginRef},
		{Name: "plugins.runs.get", Description: "读取一次插件执行的状态、输入快照和动作阶段", InputSchema: schemaObject(map[string]any{"id": pluginID}, "id"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"plugins:read"}, ResourceTypes: []string{"plugin"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "plugins.logs", ResolveResourceRefs: noRefs},
		{Name: "plugins.runs.cancel", Description: "停止 Runner 并阻止未派发的 SDK 动作", InputSchema: schemaObject(map[string]any{"id": pluginID}, "id"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"plugins:cancel"}, ResourceTypes: []string{"plugin"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.cancel", ResolveResourceRefs: noRefs},
		{Name: "plugin_triggers.list", Description: "列出插件触发器及其下次执行与跳过原因", InputSchema: schemaObject(map[string]any{"plugin_id": nullableInteger()}), OutputSchema: schemaObject(map[string]any{"triggers": arrayOf(trigger)}, "triggers"), RequiredScopes: []string{"plugins:read"}, ResourceTypes: []string{"plugin"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "plugins.triggers", ResolveResourceRefs: noRefs},
		{Name: "plugin_triggers.create", Description: "创建绑定到固定版本的触发器，默认关闭", InputSchema: schemaObject(map[string]any{"plugin_id": pluginID, "revision_id": pluginID, "name": stringValue, "kind": map[string]any{"type": "string", "enum": []string{"once", "interval", "cron", "event"}}, "spec": closedObject(map[string]any{}), "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{})}, "plugin_id", "revision_id", "name", "kind", "spec"), OutputSchema: schemaObject(map[string]any{"trigger": trigger}, "trigger"), RequiredScopes: []string{"plugins:triggers"}, ResourceTypes: []string{"plugin"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.triggers", ResolveResourceRefs: pluginRef},
		{Name: "plugin_triggers.update", Description: "修改触发器绑定、启停或参数。修改后不对同一故障周期重放高风险动作", InputSchema: schemaObject(map[string]any{"id": pluginID, "enabled": boolValue, "revision_id": nullableInteger(), "spec": closedObject(map[string]any{}), "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{}), "expected_binding_revision": positiveID}, "id"), OutputSchema: schemaObject(map[string]any{"trigger": trigger}, "trigger"), RequiredScopes: []string{"plugins:triggers"}, ResourceTypes: []string{"plugin"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.triggers", ResolveResourceRefs: noRefs},
		{Name: "plugin_grants.create", Description: "管理员批准插件版本、资源范围与自动运行约束。MCP 不可调用", InputSchema: schemaObject(map[string]any{"plugin_id": pluginID, "revision_id": pluginID, "binding_id": nullableInteger(), "capabilities": arrayOf(stringValue), "resource_scope": closedObject(map[string]any{}), "constraints": closedObject(map[string]any{})}, "plugin_id", "revision_id", "capabilities", "resource_scope"), OutputSchema: schemaObject(map[string]any{"grant_id": positiveID}, "grant_id"), RequiredScopes: []string{"plugins:authorize"}, ResourceTypes: []string{"plugin"}, RiskClass: 4, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.authorize", AdminOnly: true, ResolveResourceRefs: pluginRef},
		{Name: "plugin_grants.revoke", Description: "撤销插件授权，立即阻止后续 SDK 调用和未派发动作", InputSchema: schemaObject(map[string]any{"id": pluginID}, "id"), OutputSchema: schemaObject(map[string]any{"revoked": boolValue}, "revoked"), RequiredScopes: []string{"plugins:authorize"}, ResourceTypes: []string{"plugin"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.authorize", AdminOnly: true, ResolveResourceRefs: noRefs},
		{Name: "plugin_runtime.status", Description: "读取插件运行环境是否已安装、Worker、沙箱诊断和调度暂停状态。未安装时返回主机安装命令 install_command", InputSchema: schemaObject(nil), OutputSchema: schemaObject(map[string]any{"status": runtimeStatus}, "status"), RequiredScopes: []string{"plugins:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "plugins.read", ResolveResourceRefs: noRefs},
		{Name: "plugin_runtime.settings.update", Description: "管理员启停插件执行或调整系统限额。未安装运行环境时不能启用。MCP 不可调用", InputSchema: schemaObject(map[string]any{"enabled": boolValue, "host_actions_enabled": boolValue, "scheduler_paused": boolValue, "max_concurrency": map[string]any{"type": "integer", "minimum": 1, "maximum": 8}, "max_timeout_seconds": map[string]any{"type": "integer", "minimum": 5, "maximum": 300}}), OutputSchema: schemaObject(map[string]any{"status": runtimeStatus}, "status"), RequiredScopes: []string{"plugins:settings"}, RiskClass: 4, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.settings", AdminOnly: true, ResolveResourceRefs: noRefs},
		{Name: "servers.plugin_policy.update", Description: "管理员设置单服务器是否允许插件操作或自动电源动作。MCP 不可调用", InputSchema: schemaObject(map[string]any{"server_id": pluginID, "plugins_enabled": boolValue, "plugins_power_enabled": boolValue}, "server_id"), OutputSchema: schemaObject(map[string]any{"policy": closedObject(map[string]any{})}, "policy"), RequiredScopes: []string{"plugins:authorize"}, ResourceTypes: []string{"server"}, RiskClass: 4, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "plugins.host_power", AdminOnly: true, ResolveResourceRefs: noRefs},
	}
}
