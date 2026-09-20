package capability

import (
	"context"
	"strconv"

	"github.com/OboardProject/oboard/internal/auditcontract"
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
	snapshotSchema := accountAuditSnapshotSchema()
	for _, view := range []string{"accounts", "events", "executions"} {
		itemSchema := closedObject(nil)
		switch view {
		case "accounts":
			itemSchema = closedObject(map[string]any{"user_id": positiveID, "username": stringValue, "evaluation_status": stringValue, "snapshot": snapshotSchema}, "user_id", "username", "evaluation_status", "snapshot")
		case "events":
			itemSchema = closedObject(map[string]any{"id": positiveID, "user_id": positiveID, "risk_type": stringValue, "cycle": positiveID, "revision": positiveID, "review_status": stringValue, "status": stringValue, "score": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "first_seen_at": stringValue, "last_seen_at": stringValue, "evaluation_status": stringValue, "snapshot": snapshotSchema, "assistance": accountAuditAssistanceSchema()}, "id", "user_id", "risk_type", "cycle", "status", "score", "first_seen_at", "last_seen_at", "evaluation_status", "snapshot")
		}
		if view == "executions" {
			itemSchema = closedObject(map[string]any{"id": positiveID, "user_id": positiveID, "event_id": positiveID, "revision": positiveID, "actor": stringValue, "reason": stringValue, "status": stringValue, "expires_at": map[string]any{"type": "integer"}, "created_at": map[string]any{"type": "integer"}, "execution_status": stringValue})
		}
		inputFields := map[string]any{
			"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"offset":  map[string]any{"type": "integer", "minimum": 0, "maximum": 100000},
			"user_id": positiveID, "status": map[string]any{"type": "string", "enum": []string{"pending", "observing", "handled", "closed", "false_positive", "recovered"}},
		}
		if view == "events" || view == "executions" {
			inputFields["event_id"] = positiveID
		}
		reads = append(reads, Descriptor{
			Name: "audit." + view + ".list", Description: "分页读取账号审计持久化结果；事件默认仅摘要，event_id 查询单条完整快照；无结果时待评估，不触发评分",
			InputSchema:    schemaObject(inputFields),
			OutputSchema:   schemaObject(map[string]any{"items": arrayOf(itemSchema), "limit": map[string]any{"type": "integer"}, "offset": map[string]any{"type": "integer"}, "next_offset": nullableInteger()}, "items", "limit", "offset", "next_offset"),
			RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids",
			ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true,
			MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
		})
	}
	scopeSelector := closedObject(map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"all", "selected"}}, "ids": map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"type": "integer"}}})
	accessChange := closedObject(map[string]any{
		"id": positiveID, "change_type": stringValue, "source_plan_id": nullableInteger(),
		"candidate_revision_id": nullableInteger(), "status": stringValue,
		"affected_user_count": map[string]any{"type": "integer"}, "activate_at": nullableString(),
		"error": stringValue, "created_by": nullableInteger(), "created_at": stringValue,
		"activated_at": nullableString(), "finalized_at": nullableString(), "failed_at": nullableString(),
		"retryable": boolValue, "abandonable": boolValue, "change_id": map[string]any{"type": "integer", "minimum": 0},
		"pending_servers": map[string]any{"type": "array", "items": positiveID}, "completion": stringValue,
		"desired_state": stringValue, "effective_state": stringValue, "pending_reason": stringValue,
		"applied_users_revision": map[string]any{"type": "integer"}, "applied_authorization_revision": map[string]any{"type": "integer"},
		"lease_valid_until": nullableString(), "last_error": stringValue,
		"expected_active_revision_id": nullableInteger(),
	})
	reads = append(reads, Descriptor{
		Name: "access_changes.list", Description: "列出套餐发布（access change）及其状态与失败原因，用于排查发布失败", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(accessChange)), RequiredScopes: []string{"access_changes:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
	}, Descriptor{
		Name: "access_changes.get", Description: "读取单个套餐发布的状态、失败原因、授权下发进度与时间线", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: rawSchema(accessChange), RequiredScopes: []string{"access_changes:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
	})
	writes := []Descriptor{
		{Name: "audit.events.analyze", Description: "显式请求当前事件快照的辅助分析；仅读取保存快照，不重评分或执行限制。每事件每天最多3个快照，全局最多16个待完成审查。同快照返回缓存；未配置可用 provider_id 返回 unavailable；结果从事件详情 assistance 读取", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "event_id": positiveID, "expected_revision": positiveID, "provider_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "user_id", "event_id", "expected_revision"), OutputSchema: rawSchema(accountAuditAssistanceSchema()), RequiredScopes: []string{"audit:write"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, Executable: true, AdminOnly: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: userRefFromID},
		{Name: "audit.events.review", Description: "记录当前事件的人工核实状态，不修改风险分、订阅权限、代理凭证或账号限制；观察和误报必须指定到期时间", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "event_id": positiveID, "expected_revision": positiveID, "status": map[string]any{"type": "string", "enum": []string{"pending", "observing", "handled", "closed", "false_positive"}}, "reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}, "expires_at": map[string]any{"type": "string", "format": "date-time"}}, "user_id", "event_id", "expected_revision", "status", "reason"), OutputSchema: schemaObject(map[string]any{"event_id": positiveID, "revision": positiveID, "status": stringValue, "execution_status": stringValue}, "event_id", "revision", "status", "execution_status"), RequiredScopes: []string{"audit:write"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: userRefFromID},
		{Name: "access_changes.retry", Description: "重试失败的访问变更，从持久化失败点恢复 prepare 或 finalize", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: schemaObject(map[string]any{"access_change_id": positiveID, "phase": stringValue, "queued_tasks": map[string]any{"type": "integer", "minimum": 0}, "status": stringValue, "change_id": map[string]any{"type": "integer", "minimum": 0}, "pending_servers": map[string]any{"type": "array", "items": positiveID}, "completion": stringValue}, "access_change_id"), RequiredScopes: []string{"access_changes:write"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "audit.ai_reviews.create", Description: "创建一次 AI 审计审查作业", InputSchema: schemaObject(map[string]any{"provider_id": stringValue, "scope": closedObject(map[string]any{"users": scopeSelector, "servers": scopeSelector}, "users"), "evidence_types": stringArray(0, 32), "time_range": closedObject(map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"preset", "custom"}}, "preset": map[string]any{"type": "string", "enum": []string{"1h", "24h", "7d", "30d"}}})}, "scope"), OutputSchema: schemaObject(map[string]any{"review_id": stringValue, "status": stringValue, "job_count": map[string]any{"type": "integer"}}), RequiredScopes: []string{"audit:write"}, ResourceTypes: []string{"user", "server"}, ResourceEvaluator: "user_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: auditReviewCreateRefs},
		{Name: "audit.ai_reviews.cancel", Description: "取消一次尚未完成的 AI 审计审查", InputSchema: schemaObject(map[string]any{"review_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "review_id"), OutputSchema: schemaObject(map[string]any{"cancelled": boolValue, "review_id": stringValue}), RequiredScopes: []string{"audit:write"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "audit.ai_reviews.delete", Description: "永久删除一条终态 AI 审计记录及其证据、任务和原始日志", InputSchema: schemaObject(map[string]any{"review_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "confirm": map[string]any{"type": "boolean", "const": true}}, "review_id", "confirm"), OutputSchema: schemaObject(map[string]any{"deleted": boolValue, "review_id": stringValue}, "deleted"), RequiredScopes: []string{"audit:write"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, Destructive: true, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
	}
	reads = append(reads, accountAuditEvidenceDescriptor())
	reads = append(reads, writes...)
	return reads
}

func accountAuditAssistanceSchema() map[string]any {
	finding := auditcontract.UserFindingSchema()
	finding["type"] = []string{"object", "null"}
	result := closedObject(map[string]any{"review_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "result": finding, "error_code": map[string]any{"type": "string"}}, "review_id", "status", "result")
	result["type"] = []string{"object", "null"}
	return result
}

func accountAuditSnapshotSchema() map[string]any {
	integer := map[string]any{"type": "integer", "minimum": 0}
	text := map[string]any{"type": "string"}
	nullableInteger := map[string]any{"type": []string{"integer", "null"}, "minimum": 0}
	scale := closedObject(map[string]any{"start": integer, "full": integer}, "start", "full")
	bounds := closedObject(map[string]any{"lower": integer, "upper": integer}, "lower", "upper")
	contribution := closedObject(map[string]any{"sources": integer, "minutes": integer, "source_thresholds": scale, "duration_thresholds": scale})
	score := closedObject(map[string]any{"lower": integer, "upper": integer, "status": text, "level": text, "lower_contribution": contribution, "upper_contribution": contribution}, "lower", "upper", "status", "level")
	score["type"] = []string{"object", "null"}
	quality := map[string]any{}
	for _, key := range []string{"identity_trusted", "source_usable", "deduplicated", "measurement_valid", "coverage_complete", "time_aligned", "source_set_complete", "baseline_ready", "history_complete", "freshness", "capability_supported"} {
		quality[key] = closedObject(map[string]any{"state": text, "reason_code": text}, "state", "reason_code")
	}
	minutes := map[string]any{"type": "array", "minItems": 30, "maxItems": 30, "items": closedObject(map[string]any{"start": text, "sources": bounds}, "start", "sources")}
	resources := map[string]any{"type": []string{"array", "null"}, "items": closedObject(map[string]any{"name": text, "unit": text, "value": nullableInteger, "start": nullableInteger, "full": nullableInteger})}
	features := closedObject(map[string]any{"account_id": integer, "activity": minutes, "exposure_activity": minutes, "novel_repeated_sources": bounds, "resources": resources, "data_revision": integer, "evidence_cutoff": text})
	policy := closedObject(map[string]any{"version": text, "activity_sources": scale, "activity_minutes": scale, "exposure_sources": scale, "source_capacity": integer, "minimum_bytes": integer, "minimum_slices": integer})
	snapshot := closedObject(map[string]any{
		"account_id": integer, "activity": score, "exposure": score, "resource": score, "attention": score,
		"quality": closedObject(quality), "features": features, "policy": policy,
		"versions": closedObject(map[string]any{"model": text, "baseline": text, "source": text}),
		"as_of":    text, "window_start": text, "window_end": text, "status": text,
		"automatic_action_eligible": map[string]any{"type": "boolean", "const": false},
		"action_block_reasons":      map[string]any{"type": []string{"array", "null"}, "items": text},
	})
	snapshot["type"] = []string{"object", "null"}
	return snapshot
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
