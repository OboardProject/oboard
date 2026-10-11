package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func proxyDSCPDescriptors(id, text, boolean map[string]any) []Descriptor {
	policy := closedObject(map[string]any{"enabled": boolean, "revision": map[string]any{"type": "integer"}})
	report := closedObject(map[string]any{"enabled": boolean, "revision": map[string]any{"type": "integer"}, "state": text, "value": map[string]any{"type": "integer", "const": 46}, "marked_sockets": map[string]any{"type": "integer"}, "socket_errors": map[string]any{"type": "integer"}, "error": text, "excluded": stringArray(0, 16)})
	output := schemaObject(map[string]any{"server_id": id, "enabled": boolean, "global_enabled": boolean, "effective_enabled": boolean, "desired": policy, "report": map[string]any{"anyOf": []any{report, map[string]any{"type": "null"}}}, "state": text, "value": map[string]any{"type": "integer", "const": 46}, "task_id": id, "task_status": text, "requires_restart": boolean})
	var result []Descriptor
	for _, action := range []string{"read", "update"} {
		fields := map[string]any{"server_id": id}
		d := Descriptor{Name: "servers.proxy_dscp." + action, Description: "读取或设置代理 EF（46）标记；全局开启覆盖逐台设置；不重启；report 为节点实测范围，partial 的 excluded 路径未标记；ipv6-mptcp 表示内核不支持 IPv6 MPTCP 的 DSCP，双栈 IPv4 单独尝试，不等同于全部 Socket 失败", OutputSchema: output, RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: action == "read", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: action == "update", MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromServerID}
		if action == "update" {
			fields["enabled"] = boolean
			fields["retry"] = boolean
			d.RequiredScopes = []string{"servers:write"}
			d.MinimumAccess = mcpauth.AccessOperate
			d.RiskClass = 2
			d.ApprovalPolicy = "required"
			d.RBACPermission = "servers.update"
		}
		d.InputSchema = schemaObject(fields, "server_id")
		result = append(result, d)
	}
	return result
}
