package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func probeMonitorDescriptors(id, text, boolean map[string]any) []Descriptor {
	integer := map[string]any{"type": "integer"}
	fields := map[string]any{"id": integer, "server_id": id, "revision": integer, "name": map[string]any{"type": "string", "maxLength": 60}, "kind": map[string]any{"type": "string", "enum": []string{"route", "ip_quality"}}, "target": map[string]any{"type": "string", "maxLength": 253}, "mode": map[string]any{"type": "string", "enum": []string{"", "icmp", "tcp"}}, "port": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}, "interval_seconds": map[string]any{"type": "integer", "minimum": 300, "maximum": 604800}, "enabled": boolean}
	probe := closedObject(map[string]any{"address": text, "rtt_ms": map[string]any{"type": "number"}, "timeout": boolean})
	geo := closedObject(map[string]any{"address": text, "asn": text, "owner": text, "isp": text, "country": text, "province": text, "city": text, "prefix": text, "router": map[string]any{"type": "object", "maxProperties": 16, "additionalProperties": stringArray(0, 16)}, "source": text})
	hop := closedObject(map[string]any{"hop": integer, "addresses": stringArray(0, 3), "probes": arrayOf(probe), "geography": arrayOf(geo)})
	trace := closedObject(map[string]any{"operation_id": text, "target": text, "resolved_ip": text, "ip_family": text, "mode": text, "port": integer, "started_at": text, "duration_ms": integer, "reached": boolean, "truncated": boolean, "hops": arrayOf(hop), "provider": text, "geography_error": text})
	attribution, quality := ipDataSchemas(text, boolean)
	result := map[string]any{"anyOf": []any{map[string]any{"type": "null"}, trace, closedObject(map[string]any{"asn_path": stringArray(0, 30), "trace": trace}), closedObject(map[string]any{"ip": text, "attribution": attribution, "quality": quality})}}
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
			output = schemaObject(map[string]any{"monitors": arrayOf(closedObject(outputFields)), "providers": map[string]any{"type": "object", "additionalProperties": boolean}}, "monitors", "providers")
		case "history":
			input = schemaObject(map[string]any{"id": id, "server_id": id, "before": integer}, "id", "server_id")
			output = schemaObject(map[string]any{"versions": arrayOf(version)}, "versions")
		case "save":
			input = schemaObject(fields, "server_id", "name", "kind", "target", "mode", "port", "interval_seconds", "enabled")
		default:
			input = schemaObject(map[string]any{"id": id, "server_id": id, "revision": id}, "id", "server_id", "revision")
		}
		read := action == "list" || action == "history"
		d := Descriptor{Name: "probe_monitors." + action, Description: map[string]string{"list": "列出授权服务器的路由及 IP 质量监控任务和最近结果；路由以 NextTrace ASN 数据为主，缺失跳由主控 NextTrace 与 IP 归属数据源补全；IP 质量结果分为 attribution 与 quality 两部分；providers 只表示各数据源是否已配置密钥", "history": "分页读取 ASN 路径变化历史；相同路径只更新 last_seen_at，未知结果不生成变化", "save": "创建或更新拨测监控任务；既有任务只能改名称、周期和启停，更换目标请新建", "delete": "删除拨测监控任务及其路由历史", "run": "将已启用监控任务排入下一轮执行；结果随后读取"}[action], InputSchema: input, OutputSchema: output, RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: read, Idempotent: action != "save", DataClassification: DataInternal, MCPEnabled: true, Executable: !read, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: refs}
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

func probeDDNSDescriptors(id, text, boolean map[string]any) []Descriptor {
	integer := map[string]any{"type": "integer"}
	fields := map[string]any{
		"id": integer, "server_id": id, "revision": integer,
		"name":              map[string]any{"type": "string", "maxLength": 60},
		"dns_credential_id": id,
		"domain":            map[string]any{"type": "string", "maxLength": 253},
		"record_types":      map[string]any{"type": "string", "enum": []string{"a", "aaaa", "both"}},
		"proxy_enabled":     boolean,
		"interval_seconds":  map[string]any{"type": "integer", "minimum": 300, "maximum": 86400},
		"enabled":           boolean,
		"interface_name":    map[string]any{"type": "string", "maxLength": 64},
		"interface_prefix":  map[string]any{"type": "string", "maxLength": 80},
		"interface_address": map[string]any{"type": "string", "maxLength": 80},
		"address_family":    map[string]any{"type": "string", "enum": []string{"", "ipv4", "ipv6"}},
		"exclude_private":   boolean,
	}
	outputFields := map[string]any{}
	for k, v := range fields {
		outputFields[k] = v
	}
	outputFields["status"] = text
	outputFields["error"] = text
	outputFields["current_address"] = text
	outputFields["last_synced_at"] = text
	record := closedObject(outputFields)
	out := []Descriptor{}
	for _, action := range []string{"list", "save", "delete", "run"} {
		input := schemaObject(map[string]any{})
		output := schemaObject(map[string]any{"id": id, "revision": integer}, "id", "revision")
		refs := serverRefFromServerID
		switch action {
		case "list":
			refs = noRefs
			output = schemaObject(map[string]any{"records": arrayOf(record)}, "records")
		case "save":
			input = schemaObject(fields, "server_id", "name", "dns_credential_id", "domain", "record_types", "interval_seconds", "enabled")
		default:
			input = schemaObject(map[string]any{"id": id, "server_id": id, "revision": id}, "id", "server_id", "revision")
		}
		read := action == "list"
		d := Descriptor{Name: "probe_ddns." + action, Description: map[string]string{
			"list":   "列出授权服务器的拨测 DDNS 记录。记录按服务器归属，入口只引用记录作为连接域名",
			"save":   "创建或更新拨测 DDNS 记录。网卡绑定可选固定地址或 interface_prefix CIDR 前缀（两者互斥），前缀必须唯一匹配最近读取的地址；同域名任务分别拥有解析记录；排除内网时不会发布内网地址",
			"delete": "删除拨测 DDNS 记录及其在 DNS 服务商处的 A/AAAA 记录，并解除入口引用",
			"run":    "立即检查一条已启用的 DDNS 记录并更新解析",
		}[action], InputSchema: input, OutputSchema: output, RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: read, Idempotent: action != "save", DataClassification: DataInternal, MCPEnabled: true, Executable: !read, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: refs}
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
