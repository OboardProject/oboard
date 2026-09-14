package capability

import (
	"context"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// configHealthDescriptors expose the configuration health report and its
// cleanup operation.
//
// The cleanup input deliberately carries no mutation of its own: a caller names
// a finding (code + scope + resource) and the Controller derives what to change
// from a freshly evaluated report. An automation client therefore cannot
// describe an edit that the evaluator did not independently justify.
func configHealthDescriptors(positiveID, stringValue, boolValue map[string]any) []Descriptor {
	remedy := closedObject(map[string]any{
		"kind":        map[string]any{"type": "string", "enum": []string{"none", "normalize", "disable", "delete", "resync"}},
		"summary":     stringValue,
		"fields":      stringArray(0, 64),
		"destructive": boolValue,
	})
	finding := closedObject(map[string]any{
		"code": stringValue, "severity": map[string]any{"type": "string", "enum": []string{"blocking", "warning", "notice"}},
		"scope":       map[string]any{"type": "string", "enum": []string{"inbound", "proxy_path", "routing_rule", "dns_policy", "sync_lane"}},
		"resource_id": positiveID, "resource_name": stringValue,
		"server_id": map[string]any{"type": "integer", "minimum": 0}, "server_name": stringValue,
		"title": stringValue, "detail": stringValue, "path": stringValue, "remedy": remedy,
	})
	summary := closedObject(map[string]any{
		"blocking":  map[string]any{"type": "integer", "minimum": 0},
		"warning":   map[string]any{"type": "integer", "minimum": 0},
		"notice":    map[string]any{"type": "integer", "minimum": 0},
		"total":     map[string]any{"type": "integer", "minimum": 0},
		"truncated": boolValue,
	})
	action := closedObject(map[string]any{
		"code":        map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"scope":       map[string]any{"type": "string", "enum": []string{"inbound", "proxy_path", "routing_rule", "dns_policy", "sync_lane"}},
		"resource_id": positiveID,
	}, "code", "scope", "resource_id")
	result := closedObject(map[string]any{
		"code": stringValue, "scope": stringValue, "resource_id": positiveID, "resource_name": stringValue,
		"status": map[string]any{"type": "string", "enum": []string{"applied", "skipped", "failed"}},
		"reason": stringValue, "removed_fields": stringArray(0, 64),
	})

	return []Descriptor{
		{
			Name:        "config_health.report",
			Description: "读取入口、链路、分流、DNS 策略与节点下发通道的配置体检结果。报告按当前拓扑与下发状态派生，不修改任何配置。",
			InputSchema: schemaObject(map[string]any{
				"scope": map[string]any{"type": "string", "enum": []string{"inbound", "proxy_path", "routing_rule", "dns_policy", "sync_lane"}},
			}),
			OutputSchema: schemaObject(map[string]any{
				"revision": map[string]any{"type": "integer", "minimum": 0},
				"summary":  summary,
				"findings": arrayOf(finding),
			}, "revision", "summary", "findings"),
			RequiredScopes: []string{"config_health:read"}, ResourceTypes: []string{"server"},
			ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true,
			DataClassification: DataInternal, MCPEnabled: true,
			MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs,
		},
		{
			Name: "config_health.cleanup",
			Description: "清理体检报告中的不规范配置，或为版本冲突的下发通道重新分配版本号。只能按 code + scope + resource_id 选择一条既有结论；" +
				"具体改动由 Controller 依据最新报告推导，调用方无法描述任意修改。" +
				"confirm=false 返回逐项预览而不写入；revision 与当前不一致时整批拒绝。",
			InputSchema: schemaObject(map[string]any{
				"revision": map[string]any{"type": "integer", "minimum": 0},
				"confirm":  boolValue,
				"actions":  map[string]any{"type": "array", "minItems": 1, "maxItems": 200, "items": action},
			}, "actions"),
			OutputSchema: schemaObject(map[string]any{
				"revision":            map[string]any{"type": "integer", "minimum": 0},
				"dry_run":             boolValue,
				"applied":             map[string]any{"type": "integer", "minimum": 0},
				"skipped":             map[string]any{"type": "integer", "minimum": 0},
				"failed":              map[string]any{"type": "integer", "minimum": 0},
				"requires_deployment": boolValue,
				"results":             arrayOf(result),
			}, "revision", "results"),
			RequiredScopes: []string{"config_health:write"}, ResourceTypes: []string{"server"},
			ResourceEvaluator: "server_ids", RiskClass: 3, ApprovalPolicy: "required",
			// Re-running the same selection is safe: an action whose finding is
			// already resolved is skipped rather than re-applied.
			Idempotent: true, DataClassification: DataInternal, Destructive: true,
			MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate,
			RBACPermission: "admin.settings", ResolveResourceRefs: configHealthCleanupRefs,
		},
	}
}

// configHealthCleanupRefs resolves nothing from the request itself: the
// selection names findings, not servers, and the owning server of each finding
// is only known after evaluation. Authorization therefore rests on the
// admin.settings permission and the operate access level above.
func configHealthCleanupRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	if id, ok := int64Value(object["server_id"]); ok && id > 0 {
		refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
	}
	return refs, nil
}
