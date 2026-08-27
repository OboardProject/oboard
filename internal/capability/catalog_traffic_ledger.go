package capability

import (
	"github.com/OboardProject/oboard/internal/mcpauth"
)

func trafficLedgerDescriptors(positiveID map[string]any, stringValue map[string]any) []Descriptor {
	lease := closedObject(map[string]any{
		"lease_id": map[string]any{"type": "integer"}, "revision": map[string]any{"type": "integer"},
		"granted_bytes": map[string]any{"type": "integer"}, "consumed_bytes": map[string]any{"type": "integer"},
		"remaining_bytes": map[string]any{"type": "integer"}, "state": stringValue,
	})
	syncState := closedObject(map[string]any{
		"status": stringValue, "last_seen_at": stringValue, "last_error": stringValue,
	})
	stream := closedObject(map[string]any{
		"id": positiveID, "server_id": positiveID, "user_id": positiveID, "counter_source": stringValue,
		"stream_id": stringValue, "counter_epoch": stringValue, "period_key": stringValue,
		"inbound_id": map[string]any{"type": "integer"}, "path_id": map[string]any{"type": "integer"},
		"accepted_upload_bytes": map[string]any{"type": "integer"}, "accepted_download_bytes": map[string]any{"type": "integer"},
		"status": stringValue, "last_error": stringValue, "last_seen_at": stringValue,
	})
	period := closedObject(map[string]any{
		"key": stringValue, "upload_bytes": map[string]any{"type": "integer"}, "download_bytes": map[string]any{"type": "integer"},
		"used_bytes": map[string]any{"type": "integer"}, "limit_bytes": map[string]any{"type": "integer"}, "state": stringValue,
	})
	server := closedObject(map[string]any{
		"server_id": positiveID, "server_name": stringValue, "lease": lease, "sync": syncState, "streams": arrayOf(stream),
	})
	issue := closedObject(map[string]any{
		"id": positiveID, "server_id": positiveID, "user_id": positiveID, "source": stringValue, "stream_id": stringValue,
		"counter_epoch": stringValue, "period_key": stringValue, "kind": stringValue, "detail": stringValue, "created_at": stringValue,
	})
	ledger := closedObject(map[string]any{
		"user_id": positiveID, "period": period, "servers": arrayOf(server), "issues": arrayOf(issue),
	})
	return []Descriptor{
		{
			Name: "traffic.get_user_ledger", Description: "读取用户当期流量账本：已确认用量、各服务器 Lease 与对账状态。不含密码、UUID、内部用户名或 Agent 令牌",
			InputSchema: schemaObject(map[string]any{"user_id": positiveID, "server_id": positiveID, "period_key": stringValue}, "user_id"),
			OutputSchema: rawSchema(ledger), RequiredScopes: []string{"users:read"}, ResourceTypes: []string{"user"},
			ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal,
			MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: userRefFromID,
		},
		{
			Name: "traffic.get_server_sync_state", Description: "读取一台服务器上的流量同步与 Lease 状态，用于判断是否需要对账",
			InputSchema: schemaObject(map[string]any{"server_id": positiveID, "user_id": positiveID, "period_key": stringValue}, "server_id"),
			OutputSchema: rawSchema(map[string]any{"type": "object"}), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"},
			ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal,
			MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromServerID,
		},
		{
			Name: "traffic.list_reconciliation_issues", Description: "列出未解决的流量对账事件，例如 counter_regression、checkpoint_gap、checkpoint_overlap",
			InputSchema: schemaObject(map[string]any{"user_id": positiveID, "server_id": positiveID, "period_key": stringValue, "kind": stringValue}),
			OutputSchema: schemaObject(map[string]any{"issues": arrayOf(issue)}, "issues"), RequiredScopes: []string{"users:read"},
			ResourceTypes: []string{"user", "server"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true,
			DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs,
		},
	}
}
