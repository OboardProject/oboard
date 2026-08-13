package capability

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// systemDescriptors builds the system-domain capability set: global settings,
// backups, certificates, approval policies, service accounts, AI providers,
// tool-call audits, and notification channels.

func systemDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	adminRead := func(name, description string, input, output json.RawMessage) Descriptor {
		return Descriptor{
			Name: name, Description: description, InputSchema: input, OutputSchema: output,
			RequiredScopes: []string{"settings:read"}, ReadOnly: true, Idempotent: true,
			DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead,
			RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
		}
	}
	adminWrite := func(name, description string, input, output json.RawMessage, risk int, destructive bool) Descriptor {
		return Descriptor{
			Name: name, Description: description, InputSchema: input, OutputSchema: output,
			RequiredScopes: []string{"settings:write"}, RiskClass: risk, ApprovalPolicy: "required",
			Idempotent: true, DataClassification: DataInternal, Destructive: destructive,
			MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate,
			RBACPermission: "admin.settings", ResolveResourceRefs: noRefs,
		}
	}
	certificate := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "primary_domain": stringValue,
		"wildcard": boolValue, "challenge_type": stringValue, "status": stringValue,
		"not_before": nullableString(), "not_after": nullableString(), "auto_renew": boolValue,
		"last_error": stringValue, "last_issued_at": nullableString(), "created_at": stringValue, "updated_at": stringValue,
	})
	approvalPolicy := closedObject(map[string]any{
		"id": stringValue, "principal_id": stringValue, "capability": stringValue,
		"mode": stringValue, "allow_risk4": boolValue, "expires_at": nullableString(),
		"resource_filter_configured": boolValue, "created_at": stringValue, "updated_at": stringValue,
	})
	apiPrincipal := closedObject(map[string]any{
		"id": stringValue, "name": stringValue, "enabled": boolValue, "type": stringValue,
		"token_prefix": stringValue, "rate_limit_per_minute": map[string]any{"type": "integer"},
		"max_concurrency": map[string]any{"type": "integer"}, "expires_at": nullableString(),
		"created_at": stringValue, "updated_at": stringValue,
	})
	aiProvider := closedObject(map[string]any{
		"id": stringValue, "name": stringValue, "enabled": boolValue,
		"api_key_configured": boolValue, "model_count": map[string]any{"type": "integer"},
		"last_error": stringValue, "created_at": stringValue, "updated_at": stringValue,
	})
	toolAudit := closedObject(map[string]any{
		"id": stringValue, "principal_id": stringValue, "capability": stringValue, "status": stringValue,
		"ip": stringValue, "created_at": stringValue,
	})
	notificationChannel := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "type": stringValue,
		"enabled": boolValue, "events_configured": boolValue, "user_count": map[string]any{"type": "integer"},
		"created_at": stringValue, "updated_at": stringValue,
	})
	notificationChannelFields := closedObject(map[string]any{
		"name":           map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"type":           map[string]any{"type": "string", "enum": []string{"telegram", "bark"}},
		"enabled":        boolValue,
		"events":         stringArray(1, 64),
		"config_json":    map[string]any{"type": "string", "maxLength": 16384},
		"templates_json": map[string]any{"type": "string", "maxLength": 65536},
		"user_ids":       map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"type": "integer"}},
	}, "name", "type")
	subscriptionRelay := closedObject(map[string]any{
		"id": positiveID, "name": stringValue, "public_url": stringValue, "status": stringValue,
		"enrolled": boolValue, "active": boolValue, "version": stringValue, "build": stringValue,
		"commit": stringValue, "os": stringValue, "arch": stringValue, "service_manager": stringValue,
		"update_target_version": stringValue, "update_target_build": stringValue,
		"update_requested_at": nullableString(), "last_seen_at": nullableString(), "last_update_error": stringValue,
		"enrollment_expires_at": nullableString(),
		"created_at":            stringValue, "updated_at": stringValue,
	})
	descriptors := []Descriptor{
		adminRead("settings.get", "读取主控全局设置（审计、订阅、通知、Agent 设置等，不含秘密）", schemaObject(nil), schemaObject(map[string]any{"settings": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}}, "settings")),
		adminRead("subscription_relays.list", "列出受管订阅中继及其版本和在线状态（不含身份凭据）", schemaObject(nil), schemaObject(map[string]any{"subscription_relays": arrayOf(subscriptionRelay)}, "subscription_relays")),
		adminRead("backups.list", "列出主控备份与备份设置（不返回恢复密码）", schemaObject(nil), schemaObject(map[string]any{"backups": arrayOf(closedObject(map[string]any{"id": stringValue, "name": stringValue, "origin": stringValue, "local_status": stringValue, "remote_status": stringValue, "size_bytes": map[string]any{"type": "integer"}, "created_at": stringValue})), "settings": closedObject(map[string]any{"enabled": boolValue, "schedule": stringValue, "time": stringValue, "local_retention": map[string]any{"type": "integer"}, "remote_retention": map[string]any{"type": "integer"}, "destination_configured": boolValue, "password_configured": boolValue, "last_success_at": nullableString(), "last_error": stringValue})}, "backups", "settings")),
		adminRead("approval_policies.list", "列出自动化审批策略", schemaObject(nil), rawSchema(arrayOf(approvalPolicy))),
		adminRead("api_principals.list", "列出服务账号（不含令牌）", schemaObject(nil), rawSchema(arrayOf(apiPrincipal))),
		adminRead("ai.providers.list", "列出 AI 供应商（不含 API 密钥）", schemaObject(nil), rawSchema(arrayOf(aiProvider))),
		adminRead("tool_audits.list", "列出自动化工具调用审计", schemaObject(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}), schemaObject(map[string]any{"audits": arrayOf(toolAudit), "count": map[string]any{"type": "integer"}}, "audits")),
		{Name: "certificates.list", Description: "列出全部 TLS 证书及其状态", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(certificate)), RequiredScopes: []string{"certificates:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "notification_channels.list", Description: "列出通知频道（不含频道密钥）", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(notificationChannel)), RequiredScopes: []string{"notifications:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		adminWrite("settings.update", "修改主控全局设置", schemaObject(map[string]any{"changes": closedObject(map[string]any{
			"audit_enabled": boolValue, "subscription_audit_enabled": boolValue, "connection_audit_enabled": boolValue,
			"audit_action":     map[string]any{"type": "string", "enum": []string{"restrict", "warn"}},
			"traffic_timezone": stringValue, "traffic_enforcement_mode": map[string]any{"type": "string", "enum": []string{"reject_new", "disconnect_and_reject"}},
			"subscription_age_policy":                map[string]any{"type": "string", "enum": []string{"optional", "required"}},
			"subscription_relay_url":                 map[string]any{"type": "string", "maxLength": 2048},
			"subscription_controller_direct_enabled": boolValue,
			"subscription_custom_path_mode":          map[string]any{"type": "string", "enum": []string{"disabled", "selective", "enabled"}},
			"server_default_mtu_mode":                stringValue, "server_default_bbr_enabled": boolValue,
			"server_default_time_correction_mode": stringValue, "time_check_ntp_servers": stringArray(0, 8),
			"trusted_proxy_cidrs": stringArray(0, 64), "controller_log_max_mb": map[string]any{"type": "integer"},
			"controller_log_backups": map[string]any{"type": "integer"}, "registration_enabled": boolValue,
			"agent_auto_update_enabled":              boolValue,
			"subscription_relay_auto_update_enabled": boolValue, "update_window_enabled": boolValue,
			"update_window_start_hour": map[string]any{"type": "integer", "minimum": 0, "maximum": 23},
			"update_window_end_hour":   map[string]any{"type": "integer", "minimum": 0, "maximum": 23},
		})}, "changes"), schemaObject(map[string]any{"changed_fields": stringArray(1, 32)}, "changed_fields"), 2, false),
		adminWrite("subscription_relays.create", "创建受管订阅中继并签发一次性接入令牌", schemaObject(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "public_url": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}}, "name", "public_url"), schemaObject(map[string]any{"subscription_relay": subscriptionRelay, "enrollment_expires_at": stringValue}, "subscription_relay", "enrollment_expires_at"), 2, false),
		adminWrite("subscription_relays.update", "修改受管订阅中继名称和公开地址", schemaObject(map[string]any{"relay_id": positiveID, "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "public_url": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}}, "relay_id", "name", "public_url"), schemaObject(map[string]any{"subscription_relay": subscriptionRelay}, "subscription_relay"), 2, false),
		adminWrite("subscription_relays.issue_enrollment", "为受管订阅中继重新签发一次性接入令牌", schemaObject(map[string]any{"relay_id": positiveID}, "relay_id"), schemaObject(map[string]any{"relay_id": positiveID, "enrollment_expires_at": stringValue}, "relay_id", "enrollment_expires_at"), 2, false),
		adminWrite("subscription_relays.activate", "将指定受管订阅中继设为订阅公开入口", schemaObject(map[string]any{"relay_id": positiveID}, "relay_id"), schemaObject(map[string]any{"relay_id": positiveID, "active": boolValue}, "relay_id", "active"), 2, false),
		adminWrite("subscription_relays.request_update", "请求指定受管订阅中继更新到当前主控版本", schemaObject(map[string]any{"relay_id": positiveID}, "relay_id"), schemaObject(map[string]any{"relay_id": positiveID, "status": stringValue, "target_version": stringValue, "target_build": stringValue}, "relay_id", "status"), 2, false),
		adminWrite("subscription_relays.delete", "删除受管订阅中继记录", schemaObject(map[string]any{"relay_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "relay_id", "confirm"), schemaObject(map[string]any{"relay_id": positiveID, "deleted": boolValue}, "relay_id", "deleted"), 3, true),
		adminWrite("backups.create", "创建一次手动主控备份", schemaObject(map[string]any{"upload_remote": boolValue}), schemaObject(map[string]any{"backup": closedObject(map[string]any{"id": stringValue, "name": stringValue, "origin": stringValue, "status": stringValue})}, "backup"), 2, false),
		adminWrite("approval_policies.set", "新增或更新一条自动化审批策略", schemaObject(map[string]any{"principal_id": stringValue, "capability": stringValue, "mode": map[string]any{"type": "string", "enum": []string{"denied", "required", "automatic"}}, "allow_risk4": boolValue, "resource_filter": map[string]any{"type": "object"}}, "principal_id", "capability", "mode"), schemaObject(map[string]any{"approval_policy": approvalPolicy}, "approval_policy"), 2, false),
		adminWrite("approval_policies.delete", "删除一条自动化审批策略", schemaObject(map[string]any{"policy_id": stringValue, "confirm": map[string]any{"type": "boolean", "const": true}}, "policy_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "policy_id": stringValue}, "deleted"), 2, false),
		adminWrite("api_principals.delete", "删除一个服务账号及其令牌", schemaObject(map[string]any{"principal_id": stringValue, "confirm": map[string]any{"type": "boolean", "const": true}}, "principal_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "principal_id": stringValue}, "deleted"), 3, true),
		{Name: "certificates.issue", Description: "为证书发起签发或续期", InputSchema: schemaObject(map[string]any{"certificate_id": positiveID}, "certificate_id"), OutputSchema: schemaObject(map[string]any{"certificate": certificate, "status": stringValue}, "certificate"), RequiredScopes: []string{"certificates:write"}, ResourceTypes: []string{"certificate"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: certificateRefFromID},
		{Name: "certificates.delete", Description: "删除一个证书", InputSchema: schemaObject(map[string]any{"certificate_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "certificate_id", "confirm"), OutputSchema: schemaObject(map[string]any{"deleted": boolValue, "certificate_id": positiveID}, "deleted"), RequiredScopes: []string{"certificates:write"}, ResourceTypes: []string{"certificate"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, Destructive: true, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: certificateRefFromID},
		{Name: "notification_channels.create", Description: "创建通知频道", InputSchema: schemaObject(map[string]any{"notification_channel": notificationChannelFields}, "notification_channel"), OutputSchema: schemaObject(map[string]any{"notification_channel": notificationChannel}, "notification_channel"), RequiredScopes: []string{"notifications:write"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: noRefs},
		{Name: "notification_channels.update", Description: "修改通知频道", InputSchema: schemaObject(map[string]any{"channel_id": positiveID, "changes": notificationChannelFields}, "channel_id", "changes"), OutputSchema: schemaObject(map[string]any{"notification_channel": notificationChannel, "changed_fields": stringArray(1, 32)}, "notification_channel"), RequiredScopes: []string{"notifications:write"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: noRefs},
		{Name: "notification_channels.delete", Description: "删除通知频道", InputSchema: schemaObject(map[string]any{"channel_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "channel_id", "confirm"), OutputSchema: schemaObject(map[string]any{"deleted": boolValue, "channel_id": positiveID}, "deleted"), RequiredScopes: []string{"notifications:write"}, RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, Destructive: true, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: noRefs},
		{Name: "notification_channels.test", Description: "发送一次测试通知", InputSchema: schemaObject(map[string]any{"channel_id": positiveID}, "channel_id"), OutputSchema: schemaObject(map[string]any{"sent": boolValue, "channel_id": positiveID}, "sent"), RequiredScopes: []string{"notifications:write"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: noRefs},
		adminWrite("notification_announcements.create", "向用户发布一条通知公告", schemaObject(map[string]any{"title": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "body": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}, "user_ids": map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"type": "integer"}}}, "title", "body"), schemaObject(map[string]any{"announcement_id": positiveID, "queued_count": map[string]any{"type": "integer"}}, "announcement_id"), 2, false),
	}
	for index := range descriptors {
		switch descriptors[index].Name {
		case "subscription_relays.create", "subscription_relays.issue_enrollment":
			descriptors[index].DataClassification = DataSensitive
			descriptors[index].SensitiveOutput = []string{"enrollment_token"}
		}
	}
	return descriptors
}

func certificateRefFromID(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	id, ok := int64Value(object["certificate_id"])
	if !ok || id <= 0 {
		return nil, errCertificateInput
	}
	return []mcpauth.ResourceRef{{Type: "certificate", ID: strconv.FormatInt(id, 10)}}, nil
}

var errCertificateInput = &systemInputError{message: "certificate_id must be a positive integer ID"}

type systemInputError struct{ message string }

func (e *systemInputError) Error() string { return e.message }
