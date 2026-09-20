package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func accountAuditEvidenceDescriptor() Descriptor {
	id := map[string]any{"type": "integer", "minimum": 1}
	integer := map[string]any{"type": "integer", "minimum": 0}
	text := map[string]any{"type": "string"}
	item := closedObject(map[string]any{"account_id": id, "server_id": id, "event_time": text, "upload_bytes": integer, "download_bytes": integer, "connection_count": integer}, "account_id", "server_id", "event_time", "upload_bytes", "download_bytes", "connection_count")
	return Descriptor{
		Name: "audit.evidence.read", Description: "按事件保存的时间窗分页读取已留存连接诊断证据；仅管理员，先校验账号范围。无订阅原始记录，不补采、不评分；空结果不代表无活动，覆盖情况未知。",
		InputSchema:    schemaObject(map[string]any{"event_id": id, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 100000}}, "event_id"),
		OutputSchema:   schemaObject(map[string]any{"items": arrayOf(item), "limit": integer, "offset": integer, "next_offset": map[string]any{"type": []string{"integer", "null"}}, "status": map[string]any{"type": "string", "enum": []string{"partial", "unknown"}}, "reason": text}, "items", "limit", "offset", "next_offset", "status", "reason"),
		RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, AdminOnly: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
	}
}
