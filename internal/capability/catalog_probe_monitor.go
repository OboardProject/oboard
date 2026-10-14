package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func probeMonitorDescriptors(id, text, boolean map[string]any) []Descriptor {
	integer := map[string]any{"type": "integer"}
	fields := map[string]any{"id": integer, "server_id": id, "revision": integer, "name": map[string]any{"type": "string", "maxLength": 60}, "kind": map[string]any{"type": "string", "enum": []string{"route", "ip_quality"}}, "target": map[string]any{"type": "string", "maxLength": 253}, "mode": map[string]any{"type": "string", "enum": []string{"", "icmp", "tcp"}}, "port": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}, "interval_seconds": map[string]any{"type": "integer", "minimum": 300, "maximum": 604800}, "enabled": boolean}
	probe := closedObject(map[string]any{"address": text, "rtt_ms": map[string]any{"type": "number"}, "timeout": boolean})
	geo := closedObject(map[string]any{"address": text, "asn": text, "owner": text, "isp": text, "country": text, "province": text, "city": text, "prefix": text, "router": map[string]any{"type": "object", "maxProperties": 16, "additionalProperties": stringArray(0, 16)}})
	hop := closedObject(map[string]any{"hop": integer, "addresses": stringArray(0, 3), "probes": arrayOf(probe), "geography": arrayOf(geo)})
	trace := closedObject(map[string]any{"operation_id": text, "target": text, "resolved_ip": text, "ip_family": text, "mode": text, "port": integer, "started_at": text, "duration_ms": integer, "reached": boolean, "truncated": boolean, "hops": arrayOf(hop), "provider": text, "geography_error": text})
	provider := closedObject(map[string]any{"provider": text, "status": text, "error": text, "checked_at": text, "asn": text, "organization": text, "country": text, "proxy": boolean, "vpn": boolean, "tor": boolean, "hosting": boolean, "risk": integer, "confidence": integer, "reports": integer})
	result := map[string]any{"anyOf": []any{map[string]any{"type": "null"}, trace, closedObject(map[string]any{"asn_path": stringArray(0, 30), "trace": trace}), closedObject(map[string]any{"ip": text, "providers": arrayOf(provider)})}}
	outputFields := map[string]any{}
	for k, v := range fields {
		outputFields[k] = v
	}
	outputFields["checked_at"] = text
	outputFields["status"] = text
	outputFields["error"] = text
	outputFields["result"] = result
	version := closedObject(map[string]any{"id": id, "checked_at": text, "last_seen_at": text, "asn_path": stringArray(0, 30), "trace": trace})
	out := []Descriptor{}
	for _, action := range []string{"list", "history", "save", "delete", "run"} {
		input := schemaObject(map[string]any{})
		output := schemaObject(map[string]any{"id": id, "revision": integer}, "id", "revision")
		refs := serverRefFromServerID
		switch action {
		case "list":
			refs = noRefs
			output = schemaObject(map[string]any{"monitors": arrayOf(closedObject(outputFields)), "providers": closedObject(map[string]any{"ipinfo": boolean, "proxycheck": boolean, "abuseipdb": boolean})}, "monitors", "providers")
		case "history":
			input = schemaObject(map[string]any{"id": id, "server_id": id, "before": integer}, "id", "server_id")
			output = schemaObject(map[string]any{"versions": arrayOf(version)}, "versions")
		case "save":
			input = schemaObject(fields, "server_id", "name", "kind", "target", "mode", "port", "interval_seconds", "enabled")
		default:
			input = schemaObject(map[string]any{"id": id, "server_id": id, "revision": id}, "id", "server_id", "revision")
		}
		read := action == "list" || action == "history"
		d := Descriptor{Name: "probe_monitors." + action, Description: map[string]string{"list": "列出授权服务器的路由及 IP 质量监控任务和最近结果；路由使用 NextTrace ASN 数据", "history": "分页读取 ASN 路径变化历史；相同路径只更新 last_seen_at，未知结果不生成变化", "save": "创建或更新拨测监控任务；既有任务只能改名称、周期和启停，更换目标请新建", "delete": "删除拨测监控任务及其路由历史", "run": "将已启用监控任务排入下一轮执行；结果随后读取"}[action], InputSchema: input, OutputSchema: output, RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: read, Idempotent: action != "save", DataClassification: DataInternal, MCPEnabled: true, Executable: !read, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: refs}
		if !read {
			d.RequiredScopes = []string{"servers:write"}
			d.MinimumAccess = mcpauth.AccessOperate
			d.RiskClass = 2
			d.ApprovalPolicy = "required"
			d.RBACPermission = "servers.update"
		}
		if action == "delete" {
			d.Destructive = true
		}
		out = append(out, d)
	}
	return out
}
