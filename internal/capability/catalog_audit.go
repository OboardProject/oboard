package capability

import (
	"context"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// auditDescriptors builds the audit-console capability set: connection and
// subscription audit overviews, per-user details, the combined risk overview,
// audit logs, and AI reviews. It complements the existing structured incident
// capabilities (audit.incidents.*).

func auditDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	auditUserRow := closedObject(map[string]any{
		"user_id": positiveID, "username": stringValue, "risk_level": stringValue,
		"risk_score": map[string]any{"type": "integer"}, "confidence": map[string]any{"type": "number"},
		"recommended_action": stringValue, "suspended": boolValue, "last_seen_at": nullableString(),
		"upload_bytes": map[string]any{"type": "integer"}, "download_bytes": map[string]any{"type": "integer"},
	})
	window := map[string]any{"type": "integer", "minimum": 1, "maximum": 720}
	reads := []Descriptor{
		{Name: "audit.connection.overview", Description: "读取连接审计总览（来源、地域、风险与设备维度）", InputSchema: schemaObject(map[string]any{"window_hours": window}), OutputSchema: schemaObject(map[string]any{"window_hours": window, "generated_at": stringValue, "users": arrayOf(auditUserRow), "enabled_server_count": map[string]any{"type": "integer"}, "reporting_user_count": map[string]any{"type": "integer"}, "elevated_risk_count": map[string]any{"type": "integer"}, "total_connections": map[string]any{"type": "integer"}, "unique_source_ips": map[string]any{"type": "integer"}}), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"source_ip", "user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "audit.connection.user", Description: "读取单个用户的连接审计详情", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "window_hours": window}, "user_id"), OutputSchema: rawSchema(auditUserRow), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"source_ip", "user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: userRefFromID},
		{Name: "audit.subscription.overview", Description: "读取订阅审计总览（拉取、路由与风险维度）", InputSchema: schemaObject(map[string]any{"window_hours": window}), OutputSchema: schemaObject(map[string]any{"window_hours": window, "generated_at": stringValue, "users": arrayOf(auditUserRow), "reporting_user_count": map[string]any{"type": "integer"}, "elevated_risk_count": map[string]any{"type": "integer"}, "suspended_count": map[string]any{"type": "integer"}, "total_pulls": map[string]any{"type": "integer"}}), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"source_ip", "user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "audit.subscription.user", Description: "读取单个用户的订阅审计详情", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "window_hours": window}, "user_id"), OutputSchema: rawSchema(auditUserRow), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"source_ip", "user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: userRefFromID},
		{Name: "audit.risk_overview", Description: "读取连接与订阅审计合并后的风险总览", InputSchema: schemaObject(map[string]any{"window_hours": window}), OutputSchema: schemaObject(map[string]any{"window_hours": window, "users": arrayOf(auditUserRow), "elevated_risk_count": map[string]any{"type": "integer"}, "suspended_count": map[string]any{"type": "integer"}}), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "audit.logs.list", Description: "列出审计操作日志，支持 limit、offset 与按 action 过滤", InputSchema: schemaObject(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 100000}, "action": stringValue}), OutputSchema: schemaObject(map[string]any{"logs": arrayOf(closedObject(map[string]any{"id": map[string]any{"type": "integer"}, "actor": stringValue, "action": stringValue, "target": stringValue, "detail": stringValue, "ip": stringValue, "created_at": stringValue})), "count": map[string]any{"type": "integer"}, "offset": map[string]any{"type": "integer"}, "next_offset": map[string]any{"type": "integer"}}, "logs"), RequiredScopes: []string{"audit:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "audit.ai_reviews.list", Description: "列出 AI 审计审查及其状态", InputSchema: schemaObject(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}), OutputSchema: schemaObject(map[string]any{"reviews": arrayOf(closedObject(map[string]any{"id": stringValue, "status": stringValue, "requested_by": positiveID, "window_started_at": stringValue, "window_ended_at": stringValue, "job_count": map[string]any{"type": "integer"}, "completed_job_count": map[string]any{"type": "integer"}, "created_at": stringValue, "completed_at": nullableString()})), "count": map[string]any{"type": "integer"}}, "reviews"), RequiredScopes: []string{"audit:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
	}
	scopeSelector := closedObject(map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"all", "selected"}}, "ids": map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"type": "integer"}}})
	accessChange := closedObject(map[string]any{
		"id": positiveID, "change_type": stringValue, "source_plan_id": nullableInteger(),
		"candidate_revision_id": nullableInteger(), "status": stringValue,
		"affected_user_count": map[string]any{"type": "integer"}, "activate_at": nullableString(),
		"error": stringValue, "created_by": nullableInteger(), "created_at": stringValue,
		"activated_at": nullableString(), "finalized_at": nullableString(), "failed_at": nullableString(),
	})
	reads = append(reads, Descriptor{
		Name: "access_changes.list", Description: "列出套餐发布（access change）及其状态与失败原因，用于排查发布失败", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(accessChange)), RequiredScopes: []string{"access_changes:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
	}, Descriptor{
		Name: "access_changes.get", Description: "读取单个套餐发布的状态、失败原因与时间线", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: rawSchema(accessChange), RequiredScopes: []string{"access_changes:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
	})
	writes := []Descriptor{
		{Name: "audit.ai_reviews.create", Description: "创建一次 AI 审计审查作业", InputSchema: schemaObject(map[string]any{"provider_id": stringValue, "scope": closedObject(map[string]any{"users": scopeSelector, "servers": scopeSelector}, "users"), "evidence_types": stringArray(0, 32), "time_range": closedObject(map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"preset", "custom"}}, "preset": map[string]any{"type": "string", "enum": []string{"1h", "24h", "7d", "30d"}}})}, "scope"), OutputSchema: schemaObject(map[string]any{"review_id": stringValue, "status": stringValue, "job_count": map[string]any{"type": "integer"}}), RequiredScopes: []string{"audit:write"}, ResourceTypes: []string{"user", "server"}, ResourceEvaluator: "user_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: auditReviewCreateRefs},
		{Name: "audit.ai_reviews.cancel", Description: "取消一次尚未完成的 AI 审计审查", InputSchema: schemaObject(map[string]any{"review_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "review_id"), OutputSchema: schemaObject(map[string]any{"cancelled": boolValue, "review_id": stringValue}), RequiredScopes: []string{"audit:write"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "audit.ai_reviews.delete", Description: "永久删除一条终态 AI 审计记录及其证据、任务和原始日志", InputSchema: schemaObject(map[string]any{"review_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "confirm": map[string]any{"type": "boolean", "const": true}}, "review_id", "confirm"), OutputSchema: schemaObject(map[string]any{"deleted": boolValue, "review_id": stringValue}, "deleted"), RequiredScopes: []string{"audit:write"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, Destructive: true, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
	}
	reads = append(reads, writes...)
	return reads
}

func auditReviewCreateRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs := []mcpauth.ResourceRef{}
	scope, _ := object["scope"].(map[string]any)
	users, _ := scope["users"].(map[string]any)
	if ids, ok := users["ids"].([]any); ok {
		for _, raw := range ids {
			if id, ok := int64Value(raw); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "user", ID: strconv.FormatInt(id, 10)})
			}
		}
	}
	servers, _ := scope["servers"].(map[string]any)
	if ids, ok := servers["ids"].([]any); ok {
		for _, raw := range ids {
			if id, ok := int64Value(raw); ok && id > 0 {
				refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
			}
		}
	}
	return refs, nil
}
