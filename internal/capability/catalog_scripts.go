package capability

import (
	"context"
	"errors"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

func scriptDescriptors(positiveID, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	scriptID := map[string]any{"type": "string", "minLength": 1, "maxLength": 32, "pattern": "^[0-9]+$"}
	script := closedObject(map[string]any{
		"id": positiveID, "name": stringValue, "description": stringValue,
		"owner_user_id": positiveID, "status": stringValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	revision := closedObject(map[string]any{
		"id": positiveID, "script_id": positiveID, "revision_number": positiveID,
		"status": stringValue, "runtime": stringValue, "sdk_version": stringValue,
		"source_digest": stringValue, "created_at": stringValue,
	})
	trigger := closedObject(map[string]any{
		"id": positiveID, "script_id": positiveID, "revision_id": positiveID,
		"name": stringValue, "enabled": boolValue, "kind": stringValue,
		"binding_revision": positiveID,
	})
	run := closedObject(map[string]any{
		"id": positiveID, "uuid": stringValue, "script_id": positiveID,
		"revision_id": positiveID, "status": stringValue, "mode": stringValue,
		"error_code": stringValue, "skip_reason": stringValue,
	})
	integerValue := map[string]any{"type": "integer"}
	runtimeStatus := closedObject(map[string]any{
		"enabled": boolValue, "host_actions_enabled": boolValue, "scheduler_paused": boolValue,
		"recovery_generation": integerValue, "runtime_installed": boolValue, "install_command": stringValue,
		"worker_connected": boolValue, "isolation_available": boolValue, "isolation_mode": stringValue,
		"isolation_reason": stringValue, "active_runs": integerValue, "queued_runs": integerValue,
		"max_concurrency": integerValue,
	}, "enabled", "runtime_installed", "worker_connected", "isolation_available")
	scriptRef := func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		id, ok := int64Value(object["script_id"])
		if !ok {
			if raw, ok := object["id"].(string); ok {
				parsed, convErr := strconv.ParseInt(raw, 10, 64)
				if convErr != nil || parsed <= 0 {
					return nil, errors.New("script_id must be a positive integer string")
				}
				id = parsed
				ok = true
			} else {
				id, ok = int64Value(object["id"])
			}
		}
		if !ok || id <= 0 {
			return nil, errors.New("script_id must be a positive integer ID")
		}
		return []mcpauth.ResourceRef{{Type: "script", ID: strconv.FormatInt(id, 10)}}, nil
	}
	return []Descriptor{
		{Name: "scripts.list", Description: "列出授权范围内的脚本身份与状态", InputSchema: schemaObject(map[string]any{"status": stringValue, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}), OutputSchema: schemaObject(map[string]any{"scripts": arrayOf(script)}, "scripts"), RequiredScopes: []string{"scripts:read"}, ResourceTypes: []string{"script"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "scripts.read", ResolveResourceRefs: noRefs},
		{Name: "scripts.get", Description: "读取脚本详情、当前草稿与已发布版本摘要", InputSchema: schemaObject(map[string]any{"id": scriptID}, "id"), OutputSchema: schemaObject(map[string]any{"script": script, "draft": revision, "published": revision}), RequiredScopes: []string{"scripts:read"}, ResourceTypes: []string{"script"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "scripts.read", ResolveResourceRefs: scriptRef},
		{Name: "scripts.create", Description: "创建脚本草稿，不授予执行权限", InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "description": map[string]any{"type": "string", "maxLength": 2000}}, "name"), OutputSchema: schemaObject(map[string]any{"script": script}, "script"), RequiredScopes: []string{"scripts:write"}, ResourceTypes: []string{"script"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.draft", ResolveResourceRefs: noRefs},
		{Name: "scripts.update", Description: "更新脚本名称、说明或启用状态；归档后不可再启用", InputSchema: schemaObject(map[string]any{"id": scriptID, "name": stringValue, "description": stringValue, "status": map[string]any{"type": "string", "enum": []string{"enabled", "disabled", "archived"}}, "expected_updated_at": stringValue}, "id"), OutputSchema: schemaObject(map[string]any{"script": script}, "script"), RequiredScopes: []string{"scripts:write"}, ResourceTypes: []string{"script"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.draft", ResolveResourceRefs: scriptRef},
		{Name: "scripts.revisions.save", Description: "保存不可发布的草稿源码与运行规范", InputSchema: schemaObject(map[string]any{"script_id": scriptID, "source": map[string]any{"type": "string", "minLength": 1, "maxLength": 262144}, "manifest": closedObject(map[string]any{}), "expected_revision_id": nullableInteger()}, "script_id", "source", "manifest"), OutputSchema: schemaObject(map[string]any{"revision": revision}, "revision"), RequiredScopes: []string{"scripts:write"}, ResourceTypes: []string{"script"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.draft", ResolveResourceRefs: scriptRef},
		{Name: "scripts.revisions.publish", Description: "发布指定草稿版本。发布不继承旧版本高风险授权", InputSchema: schemaObject(map[string]any{"script_id": scriptID, "revision_id": scriptID}, "script_id", "revision_id"), OutputSchema: schemaObject(map[string]any{"revision": revision}, "revision"), RequiredScopes: []string{"scripts:publish"}, ResourceTypes: []string{"script"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.publish", ResolveResourceRefs: scriptRef},
		{Name: "scripts.validate", Description: "校验清单、参数 Schema 与兼容性，不编译或执行用户源码", InputSchema: schemaObject(map[string]any{"script_id": scriptID, "revision_id": nullableInteger(), "source": stringValue, "manifest": closedObject(map[string]any{}), "params": closedObject(map[string]any{})}), OutputSchema: schemaObject(map[string]any{"valid": boolValue, "errors": arrayOf(stringValue)}, "valid"), RequiredScopes: []string{"scripts:read"}, ResourceTypes: []string{"script"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "scripts.read", ResolveResourceRefs: scriptRef},
		{Name: "scripts.simulate", Description: "在隔离 Runner 中用模拟 SDK 运行，不产生真实动作", InputSchema: schemaObject(map[string]any{"script_id": scriptID, "revision_id": nullableInteger(), "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{})}, "script_id"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"scripts:execute"}, ResourceTypes: []string{"script"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.execute", ResolveResourceRefs: scriptRef},
		{Name: "scripts.run", Description: "手动执行已获准版本，权限与调用者取交集", InputSchema: schemaObject(map[string]any{"script_id": scriptID, "revision_id": scriptID, "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{}), "idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "script_id", "revision_id", "idempotency_key"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"scripts:execute"}, ResourceTypes: []string{"script"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.execute", ResolveResourceRefs: scriptRef},
		{Name: "scripts.runs.get", Description: "读取一次脚本执行的状态、输入快照和动作阶段", InputSchema: schemaObject(map[string]any{"id": scriptID}, "id"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"scripts:read"}, ResourceTypes: []string{"script"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "scripts.logs", ResolveResourceRefs: noRefs},
		{Name: "scripts.runs.cancel", Description: "停止 Runner 并阻止未派发的 SDK 动作", InputSchema: schemaObject(map[string]any{"id": scriptID}, "id"), OutputSchema: schemaObject(map[string]any{"run": run}, "run"), RequiredScopes: []string{"scripts:cancel"}, ResourceTypes: []string{"script"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.cancel", ResolveResourceRefs: noRefs},
		{Name: "script_triggers.list", Description: "列出脚本触发器及其下次执行与跳过原因", InputSchema: schemaObject(map[string]any{"script_id": nullableInteger()}), OutputSchema: schemaObject(map[string]any{"triggers": arrayOf(trigger)}, "triggers"), RequiredScopes: []string{"scripts:read"}, ResourceTypes: []string{"script"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "scripts.triggers", ResolveResourceRefs: noRefs},
		{Name: "script_triggers.create", Description: "创建绑定到固定版本的触发器，默认关闭", InputSchema: schemaObject(map[string]any{"script_id": scriptID, "revision_id": scriptID, "name": stringValue, "kind": map[string]any{"type": "string", "enum": []string{"once", "interval", "cron", "event"}}, "spec": closedObject(map[string]any{}), "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{})}, "script_id", "revision_id", "name", "kind", "spec"), OutputSchema: schemaObject(map[string]any{"trigger": trigger}, "trigger"), RequiredScopes: []string{"scripts:triggers"}, ResourceTypes: []string{"script"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.triggers", ResolveResourceRefs: scriptRef},
		{Name: "script_triggers.update", Description: "修改触发器绑定、启停或参数。修改后不对同一故障周期重放高风险动作", InputSchema: schemaObject(map[string]any{"id": scriptID, "enabled": boolValue, "revision_id": nullableInteger(), "spec": closedObject(map[string]any{}), "params": closedObject(map[string]any{}), "env": closedObject(map[string]any{}), "expected_binding_revision": positiveID}, "id"), OutputSchema: schemaObject(map[string]any{"trigger": trigger}, "trigger"), RequiredScopes: []string{"scripts:triggers"}, ResourceTypes: []string{"script"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.triggers", ResolveResourceRefs: noRefs},
		{Name: "script_grants.create", Description: "管理员批准脚本版本、资源范围与自动运行约束。MCP 不可调用", InputSchema: schemaObject(map[string]any{"script_id": scriptID, "revision_id": scriptID, "binding_id": nullableInteger(), "capabilities": arrayOf(stringValue), "resource_scope": closedObject(map[string]any{}), "constraints": closedObject(map[string]any{})}, "script_id", "revision_id", "capabilities", "resource_scope"), OutputSchema: schemaObject(map[string]any{"grant_id": positiveID}, "grant_id"), RequiredScopes: []string{"scripts:authorize"}, ResourceTypes: []string{"script"}, RiskClass: 4, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.authorize", AdminOnly: true, ResolveResourceRefs: scriptRef},
		{Name: "script_grants.revoke", Description: "撤销脚本授权，立即阻止后续 SDK 调用和未派发动作", InputSchema: schemaObject(map[string]any{"id": scriptID}, "id"), OutputSchema: schemaObject(map[string]any{"revoked": boolValue}, "revoked"), RequiredScopes: []string{"scripts:authorize"}, ResourceTypes: []string{"script"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.authorize", AdminOnly: true, ResolveResourceRefs: noRefs},
		{Name: "script_runtime.status", Description: "读取脚本运行环境是否已安装、Worker、沙箱诊断和调度暂停状态。未安装时返回主机安装命令 install_command", InputSchema: schemaObject(nil), OutputSchema: schemaObject(map[string]any{"status": runtimeStatus}, "status"), RequiredScopes: []string{"scripts:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "scripts.read", ResolveResourceRefs: noRefs},
		{Name: "script_runtime.settings.update", Description: "管理员启停脚本执行或调整系统限额。未安装运行环境时不能启用。MCP 不可调用", InputSchema: schemaObject(map[string]any{"enabled": boolValue, "host_actions_enabled": boolValue, "scheduler_paused": boolValue, "max_concurrency": map[string]any{"type": "integer", "minimum": 1, "maximum": 8}, "max_timeout_seconds": map[string]any{"type": "integer", "minimum": 5, "maximum": 300}}), OutputSchema: schemaObject(map[string]any{"status": runtimeStatus}, "status"), RequiredScopes: []string{"scripts:settings"}, RiskClass: 4, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.settings", AdminOnly: true, ResolveResourceRefs: noRefs},
		{Name: "servers.script_policy.update", Description: "管理员设置单服务器是否允许脚本操作或自动电源动作。MCP 不可调用", InputSchema: schemaObject(map[string]any{"server_id": scriptID, "scripts_enabled": boolValue, "scripts_power_enabled": boolValue}, "server_id"), OutputSchema: schemaObject(map[string]any{"policy": closedObject(map[string]any{})}, "policy"), RequiredScopes: []string{"scripts:authorize"}, ResourceTypes: []string{"server"}, RiskClass: 4, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: false, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "scripts.host_power", AdminOnly: true, ResolveResourceRefs: noRefs},
	}
}
