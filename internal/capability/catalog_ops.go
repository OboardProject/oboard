package capability

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// opsDescriptors builds the task-center and host-operations capability set:
// agent task reads, diagnostics, agent updates, log collection, deployment
// failure dismissal, and inbound / egress probes.

func opsDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	agentTask := closedObject(map[string]any{
		"id": positiveID, "server_id": positiveID, "type": stringValue, "status": stringValue,
		"config_version": map[string]any{"type": "integer"}, "created_at": stringValue,
		"completed_at": nullableString(), "result_summary": stringValue, "error": stringValue,
	})
	reads := []Descriptor{
		{Name: "agent_tasks.list", Description: "列出 Agent 任务（部署、检测、日志等），已脱敏不包含秘密", InputSchema: schemaObject(map[string]any{"server_id": nullableInteger(), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}), OutputSchema: schemaObject(map[string]any{"tasks": arrayOf(agentTask), "count": map[string]any{"type": "integer"}}, "tasks"), RequiredScopes: []string{"tasks:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "agent_tasks.get", Description: "读取单个 Agent 任务详情，已脱敏不包含秘密", InputSchema: schemaObject(map[string]any{"task_id": positiveID}, "task_id"), OutputSchema: rawSchema(agentTask), RequiredScopes: []string{"tasks:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
	}
	writes := []struct {
		name, description string
		input, output     json.RawMessage
		risk              int
		admin             bool
	}{
		{"servers.diagnose", "对指定服务器发起网络诊断任务", schemaObject(map[string]any{"server_id": positiveID}, "server_id"), schemaObject(map[string]any{"task_id": positiveID, "task_status": stringValue, "entry_target_count": map[string]any{"type": "integer"}}, "task_id"), 2, true},
		{"servers.update_agent", "升级指定服务器的 Agent 与内核", schemaObject(map[string]any{"server_id": positiveID, "source": stringValue, "github_repo": stringValue}, "server_id"), schemaObject(map[string]any{"task_id": positiveID, "task_status": stringValue, "existing": boolValue}, "task_id"), 2, true},
		{"agents.update_all", "升级全部已接入服务器的 Agent 与内核", schemaObject(nil), schemaObject(map[string]any{"summary": closedObject(map[string]any{"total": map[string]any{"type": "integer"}, "created": map[string]any{"type": "integer"}, "existing": map[string]any{"type": "integer"}, "skipped": map[string]any{"type": "integer"}, "failed": map[string]any{"type": "integer"}}), "created_count": map[string]any{"type": "integer"}}, "summary"), 2, true},
		{"servers.collect_logs", "拉取指定服务器的 Agent/内核日志", schemaObject(map[string]any{"server_id": positiveID, "services": map[string]any{"type": "string", "enum": []string{"all", "agent", "core"}}, "lines": map[string]any{"type": "integer", "minimum": 1, "maximum": 2000}}, "server_id"), schemaObject(map[string]any{"task_id": positiveID, "task_status": stringValue}, "task_id"), 2, true},
		{"servers.manage_logs", "轮转或清空指定服务器的日志", schemaObject(map[string]any{"server_id": positiveID, "action": map[string]any{"type": "string", "enum": []string{"rotate", "clear"}}, "services": map[string]any{"type": "string", "enum": []string{"all", "agent", "core"}}}, "server_id", "action"), schemaObject(map[string]any{"task_id": positiveID, "task_status": stringValue}, "task_id"), 2, true},
		{"servers.list_network_interfaces", "读取指定服务器的网卡及地址列表", schemaObject(map[string]any{"server_id": positiveID}, "server_id"), schemaObject(map[string]any{"task_id": positiveID, "task_status": stringValue}, "task_id"), 2, false},
		{"deployments.dismiss_failure", "忽略当前最新部署失败的提醒", schemaObject(nil), schemaObject(map[string]any{"dismissed": boolValue, "deployment_status": stringValue}, "dismissed"), 2, false},
		{"configuration_sync.retry", "重试已失败的配置自动同步", schemaObject(map[string]any{"server_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": positiveID}}, "server_ids"), schemaObject(map[string]any{"retried": map[string]any{"type": "integer", "minimum": 1}, "server_ids": map[string]any{"type": "array", "items": positiveID}}, "retried"), 2, false},
		{"inbounds.probe", "对指定入口发起本地与公网探测任务", schemaObject(map[string]any{"inbound_id": positiveID}, "inbound_id"), schemaObject(map[string]any{"task_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "entry_target_count": map[string]any{"type": "integer"}}, "task_ids"), 2, false},
		{"proxy_paths.probe_egress", "对已部署的代理分支手动重探测出口地区", schemaObject(map[string]any{"path_id": positiveID}, "path_id"), schemaObject(map[string]any{"task_id": positiveID, "region_code": stringValue, "status": stringValue}, "task_id"), 2, false},
		{"servers.probe_latency", "对指定服务器发起延迟测试", schemaObject(map[string]any{"server_id": positiveID}, "server_id"), schemaObject(map[string]any{"task_id": positiveID, "task_status": stringValue, "target_count": map[string]any{"type": "integer"}, "existing": boolValue}, "task_id"), 2, false},
	}
	for _, write := range writes {
		descriptor := Descriptor{
			Name: write.name, Description: write.description, InputSchema: write.input, OutputSchema: write.output,
			RequiredScopes: []string{opsScopeFor(write.name)}, ResourceTypes: []string{"server", "inbound", "proxy_path"},
			ResourceEvaluator: "server_ids", RiskClass: write.risk, ApprovalPolicy: "required", Idempotent: true,
			DataClassification: DataInternal, MCPEnabled: true, Executable: true,
			MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: opsWriteResolver(write.name),
		}
		if write.admin {
			descriptor.RBACPermission = "admin.settings"
		}
		reads = append(reads, descriptor)
	}
	return reads
}

func opsScopeFor(name string) string {
	switch {
	case name == "agents.update_all":
		return "tasks:write"
	case name == "deployments.dismiss_failure", name == "configuration_sync.retry":
		return "deployments:write"
	case name == "inbounds.probe":
		return "topology:write"
	case name == "proxy_paths.probe_egress":
		return "topology:write"
	case name == "servers.probe_latency":
		return "tasks:write"
	default:
		return "tasks:write"
	}
}

func opsWriteResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
	return func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		refs := []mcpauth.ResourceRef{}
		if id, ok := int64Value(object["server_id"]); ok && id > 0 {
			refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
		}
		if values, ok := object["server_ids"].([]any); ok {
			for _, value := range values {
				if id, ok := int64Value(value); ok && id > 0 {
					refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
				}
			}
		}
		if id, ok := int64Value(object["inbound_id"]); ok && id > 0 {
			refs = append(refs, mcpauth.ResourceRef{Type: "inbound", ID: strconv.FormatInt(id, 10)})
		}
		if id, ok := int64Value(object["path_id"]); ok && id > 0 {
			refs = append(refs, mcpauth.ResourceRef{Type: "proxy_path", ID: strconv.FormatInt(id, 10)})
		}
		return refs, nil
	}
}
