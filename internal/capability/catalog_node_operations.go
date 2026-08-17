package capability

import (
	"context"
	"errors"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

func nodeOperationsDescriptors(positiveID, stringValue, boolValue map[string]any, nullableString func() map[string]any) []Descriptor {
	incident := closedObject(map[string]any{
		"id": positiveID, "server_id": positiveID, "server_name": stringValue,
		"kind": stringValue, "status": stringValue, "version": positiveID,
		"first_offline_at": stringValue, "detected_at": stringValue,
		"recovery_candidate_at": nullableString(), "recovery_deadline_at": nullableString(),
		"recovered_at": nullableString(), "resolved_at": nullableString(),
		"outage_duration_seconds":    map[string]any{"type": "integer", "minimum": 0},
		"offline_threshold_seconds":  map[string]any{"type": "integer", "minimum": 0},
		"recovery_threshold_seconds": map[string]any{"type": "integer", "minimum": 0},
		"flap_count":                 map[string]any{"type": "integer", "minimum": 0},
		"snapshot_json":              stringValue, "created_at": stringValue, "updated_at": stringValue,
	})
	isolation := closedObject(map[string]any{
		"id": positiveID, "incident_id": positiveID, "inbound_id": positiveID,
		"inbound_name": stringValue, "server_id": positiveID, "recovery_policy": stringValue,
		"status": stringValue, "actor_user_id": positiveID,
	})
	incidentRef := func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		id, ok := int64Value(object["event_id"])
		if !ok {
			id, ok = int64Value(object["id"])
		}
		if !ok || id <= 0 {
			return nil, errors.New("event_id must be a positive integer ID")
		}
		return []mcpauth.ResourceRef{{Type: "node_incident", ID: strconv.FormatInt(id, 10)}}, nil
	}
	action := closedObject(map[string]any{
		"id": positiveID, "incident_id": positiveID, "actor_user_id": positiveID,
		"kind": stringValue, "status": stringValue, "inbound_ids_json": stringValue,
		"changeset_id": stringValue, "config_version": map[string]any{"type": "integer"},
		"task_count": map[string]any{"type": "integer", "minimum": 0}, "error": stringValue,
		"created_at": stringValue, "completed_at": nullableString(), "updated_at": stringValue,
	})
	filter := closedObject(map[string]any{
		"user_ids": idArray(0, 10000), "group_ids": idArray(0, 1000), "plan_ids": idArray(0, 1000),
		"user_status":         map[string]any{"type": "string", "enum": []string{"", "active", "disabled"}},
		"subscription_status": map[string]any{"type": "string", "enum": []string{"", "active", "inactive"}},
		"telegram_bound":      map[string]any{"type": "boolean"},
	})
	broadcast := closedObject(map[string]any{
		"id": positiveID, "actor_user_id": positiveID, "actor_name": stringValue,
		"title": stringValue, "body": stringValue, "filter_json": stringValue,
		"idempotency_key": stringValue, "status": stringValue,
		"recipient_count": map[string]any{"type": "integer", "minimum": 0},
		"success_count":   map[string]any{"type": "integer", "minimum": 0},
		"failure_count":   map[string]any{"type": "integer", "minimum": 0},
		"created_at":      stringValue, "completed_at": nullableString(),
	})
	return []Descriptor{
		{Name: "node_incidents.list", Description: "列出节点失联事件及防抖恢复状态", InputSchema: schemaObject(map[string]any{"status": stringValue, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}), OutputSchema: schemaObject(map[string]any{"events": arrayOf(incident)}, "events"), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server", "node_incident"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "node_incidents.get", Description: "读取一个节点失联事件、影响快照和隔离记录", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: schemaObject(map[string]any{"event": incident, "isolations": arrayOf(isolation), "actions": arrayOf(action)}, "event", "isolations", "actions"), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server", "node_incident"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: incidentRef},
		{Name: "node_incidents.isolate", Description: "仅从后续订阅渲染隐藏失联事件中的选定入口", InputSchema: schemaObject(map[string]any{"event_id": positiveID, "event_version": positiveID, "inbound_ids": idArray(1, 256), "recovery_policy": map[string]any{"type": "string", "enum": []string{"manual", "auto"}}}, "event_id", "event_version", "inbound_ids", "recovery_policy"), OutputSchema: schemaObject(map[string]any{"isolations": arrayOf(isolation), "deployment_required": boolValue}, "isolations"), RequiredScopes: []string{"topology:write"}, ResourceTypes: []string{"server", "node_incident", "inbound"}, ResourceEvaluator: "server_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "node_publication.write", ResolveResourceRefs: incidentRef},
		{Name: "node_incidents.restore", Description: "人工将隔离入口重新加入后续订阅渲染", InputSchema: schemaObject(map[string]any{"event_id": positiveID, "isolation_id": positiveID}, "event_id", "isolation_id"), OutputSchema: schemaObject(map[string]any{"restored": boolValue, "deployment_required": boolValue}, "restored"), RequiredScopes: []string{"topology:write"}, ResourceTypes: []string{"server", "node_incident", "inbound"}, ResourceEvaluator: "server_ids", RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "node_publication.write", ResolveResourceRefs: incidentRef},
		{Name: "notification_broadcasts.preview", Description: "按用户、分组、套餐、状态和 Telegram 绑定筛选预览管理员广播", InputSchema: schemaObject(map[string]any{"filter": filter}, "filter"), OutputSchema: schemaObject(map[string]any{"recipient_count": map[string]any{"type": "integer", "minimum": 0}, "bound_target_count": map[string]any{"type": "integer", "minimum": 0}}, "recipient_count"), RequiredScopes: []string{"notifications:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "notification_broadcasts.create", Description: "创建幂等管理员广播并逐 Telegram 绑定记录投递结果", InputSchema: schemaObject(map[string]any{"title": map[string]any{"type": "string", "minLength": 1, "maxLength": 120}, "body": map[string]any{"type": "string", "minLength": 1, "maxLength": 3000}, "filter": filter, "idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "title", "body", "filter", "idempotency_key"), OutputSchema: schemaObject(map[string]any{"broadcast": broadcast}, "broadcast"), RequiredScopes: []string{"notifications:write"}, RiskClass: 2, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
	}
}
