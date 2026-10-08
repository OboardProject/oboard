package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func runtimeSecurityDescriptors(id, text, boolean map[string]any) []Descriptor {
	check := closedObject(map[string]any{"id": text, "category": text, "severity": text, "status": map[string]any{"type": "string", "enum": []string{"passed", "warning", "failed", "unsupported", "unknown"}}, "supported": boolean, "checked_at": text, "message": text, "remedy": text, "auto_fix": boolean})
	report := closedObject(map[string]any{"revision": map[string]any{"type": "integer"}, "desired_mode": text, "actual_mode": text, "state": text, "platform": text, "supported": boolean, "applied_at": text, "checked_at": text, "error_code": text, "phase": text, "local_policy": text, "capabilities": stringArray(0, 32), "checks": map[string]any{"type": "array", "maxItems": 64, "items": check}})
	output := schemaObject(map[string]any{"server_id": id, "desired": closedObject(map[string]any{"mode": text, "revision": map[string]any{"type": "integer"}}), "report": map[string]any{"anyOf": []any{report, map[string]any{"type": "null"}}}, "task_id": map[string]any{"type": "integer"}, "task_status": text, "last_task_id": map[string]any{"type": "integer"}, "last_task_status": text, "queued": boolean, "requires_restart": boolean, "online": boolean})
	descriptors := []Descriptor{}
	for _, action := range []string{"read", "update", "check"} {
		fields := map[string]any{"server_id": id}
		required := []string{"server_id"}
		if action == "update" {
			fields["mode"] = map[string]any{"type": "string", "enum": []string{"standard", "enhanced"}}
			required = append(required, "mode")
		}
		d := Descriptor{Name: "servers.runtime_security." + action, Description: map[string]string{"read": "读取服务器期望与实际运行安全状态和脱敏检查结果", "update": "设置服务器运行安全模式；可能受控重启内核；不能降低本地 Hardened 策略", "check": "排队执行固定范围的只读运行安全检查，不接受路径或命令"}[action], InputSchema: schemaObject(fields, required...), OutputSchema: output, RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: action == "read", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: action != "read", MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromServerID}
		if action != "read" {
			d.RequiredScopes = []string{"servers:write"}
			d.MinimumAccess = mcpauth.AccessOperate
			d.RiskClass = 2
			d.ApprovalPolicy = "required"
			d.RBACPermission = "servers.update"
		}
		descriptors = append(descriptors, d)
	}
	return descriptors
}
