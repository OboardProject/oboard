package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func accountAuditStatusDescriptors() []Descriptor {
	fields := map[string]any{
		"status":             map[string]any{"type": "string", "enum": []string{"pending", "available", "degraded"}},
		"audit_enabled":      map[string]any{"type": "boolean"},
		"collection_mode":    map[string]any{"type": "string", "enum": []string{"light", "standard"}},
		"action_mode":        map[string]any{"type": "string", "enum": []string{"alert_only"}},
		"last_snapshot_time": map[string]any{"type": []string{"string", "null"}},
		"unavailable":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"degradation":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"limits": closedObject(map[string]any{
			"status_read_rows": map[string]any{"type": "integer"}, "inbox_reports": map[string]any{"type": "integer"}, "inbox_bytes": map[string]any{"type": "integer"}, "inbox_reports_per_node": map[string]any{"type": "integer"}, "snapshot_freshness_seconds": map[string]any{"type": "integer"},
		}, "status_read_rows", "inbox_reports", "inbox_bytes", "inbox_reports_per_node", "snapshot_freshness_seconds"),
	}
	required := []string{"status", "audit_enabled", "collection_mode", "action_mode", "last_snapshot_time", "unavailable", "degradation", "limits"}
	for _, name := range []string{"pending_event_count", "pending_inbox_reports", "pending_inbox_bytes", "pending_evaluations", "pending_notifications", "oldest_backlog_age_seconds"} {
		fields[name] = map[string]any{"type": []string{"integer", "null"}, "minimum": 0}
		required = append(required, name)
	}
	return []Descriptor{{Name: "audit.status.read", Description: "读取有界审计队列与快照状态；不触发评估，超限或权限范围外指标标记不可用", InputSchema: schemaObject(nil), OutputSchema: schemaObject(fields, required...), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, DataClassification: DataSensitive, ResolveResourceRefs: noRefs}}
}
