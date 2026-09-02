package capability

import (
	"context"
	"errors"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

func userNodeAuthorizationDescriptors(positiveID, stringValue, boolValue map[string]any, nullableString func() map[string]any) []Descriptor {
	nodeType := map[string]any{"type": "string", "enum": []string{"inbound", "proxy_path", "external_outbound"}}
	node := closedObject(map[string]any{
		"node_type": nodeType,
		"node_id":   positiveID,
	}, "node_type", "node_id")
	authorization := closedObject(map[string]any{
		"id": positiveID, "user_id": positiveID, "username": stringValue, "nickname": stringValue,
		"effect": map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
		"status": map[string]any{"type": "string", "enum": []string{"pending", "active"}},
		"reason": stringValue, "starts_at": nullableString(), "expires_at": nullableString(),
		"effective": boolValue, "plan_includes": boolValue, "plan_id": positiveID, "plan_name": stringValue,
	})
	writeOutput := schemaObject(map[string]any{
		"created":              map[string]any{"type": "integer", "minimum": 0},
		"updated":              map[string]any{"type": "integer", "minimum": 0},
		"skipped":              map[string]any{"type": "integer", "minimum": 0},
		"affected_users":       map[string]any{"type": "integer", "minimum": 0},
		"access_change_id":     map[string]any{"type": "integer", "minimum": 0},
		"access_change_status": stringValue,
		"queued_tasks":         map[string]any{"type": "integer", "minimum": 0},
	})
	return []Descriptor{
		{
			Name: "user_node_authorizations.list", Description: "列出一个节点的单独用户授权；plan_includes 标识该用户套餐也包含此节点，最终节点集合按并集计算且不会重复",
			InputSchema:    schemaObject(map[string]any{"node_type": nodeType, "node_id": positiveID}, "node_type", "node_id"),
			OutputSchema:   schemaObject(map[string]any{"node_type": stringValue, "node_id": positiveID, "authorizations": arrayOf(authorization), "count": map[string]any{"type": "integer", "minimum": 0}, "runtime_authorization_mode": stringValue}, "authorizations"),
			RequiredScopes: []string{"user_node_authorizations:read"}, ResourceTypes: []string{"user", "inbound", "proxy_path", "external_outbound"}, ResourceEvaluator: "user_ids",
			ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings",
			ResolveResourceRefs: userNodeAuthorizationListRefs,
		},
		{
			Name: "user_node_authorizations.set", Description: "为用户新增或更新节点允许/拒绝授权；允许授权与套餐节点取并集，拒绝授权优先，并通过访问变更流程应用",
			InputSchema: schemaObject(map[string]any{
				"user_ids": idArray(1, 256), "nodes": map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": node},
				"effect": map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
				"reason": map[string]any{"type": "string", "maxLength": 300}, "starts_at": nullableString(), "expires_at": nullableString(),
			}, "user_ids", "nodes", "effect"),
			OutputSchema: writeOutput, RequiredScopes: []string{"user_node_authorizations:write"}, ResourceTypes: []string{"user", "inbound", "proxy_path", "external_outbound"}, ResourceEvaluator: "user_ids",
			RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings",
			ResolveResourceRefs: userNodeAuthorizationSetRefs,
		},
		{
			Name: "user_node_authorizations.revoke", Description: "撤销现有单独节点授权并通过访问变更流程收回；必须同时声明授权记录、用户和节点边界",
			InputSchema: schemaObject(map[string]any{
				"authorization_ids": idArray(1, 256), "user_ids": idArray(1, 256),
				"nodes":   map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": node},
				"confirm": map[string]any{"type": "boolean", "const": true},
			}, "authorization_ids", "user_ids", "nodes", "confirm"),
			OutputSchema: schemaObject(map[string]any{
				"revoking": boolValue, "authorization_ids": idArray(0, 256), "affected_users": map[string]any{"type": "integer", "minimum": 0},
				"access_change_id": map[string]any{"type": "integer", "minimum": 0}, "access_change_status": stringValue, "queued_tasks": map[string]any{"type": "integer", "minimum": 0},
			}),
			RequiredScopes: []string{"user_node_authorizations:write"}, ResourceTypes: []string{"user", "inbound", "proxy_path", "external_outbound"}, ResourceEvaluator: "user_ids",
			RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings",
			ResolveResourceRefs: userNodeAuthorizationRevokeRefs,
		},
	}
}

func userNodeAuthorizationListRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	return authorizationNodeRefs(object)
}

func userNodeAuthorizationSetRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs, err := refsFromUserIDs(object)
	if err != nil {
		return nil, err
	}
	nodes, err := authorizationNodeRefs(object)
	if err != nil {
		return nil, err
	}
	return append(refs, nodes...), nil
}

func userNodeAuthorizationRevokeRefs(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
	object, err := canonicalMap(input)
	if err != nil {
		return nil, err
	}
	refs, err := refsFromUserIDs(object)
	if err != nil {
		return nil, err
	}
	nodes, err := authorizationNodeRefs(object)
	if err != nil {
		return nil, err
	}
	return append(refs, nodes...), nil
}

func refsFromUserIDs(object map[string]any) ([]mcpauth.ResourceRef, error) {
	items, ok := object["user_ids"].([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("user_ids must be a non-empty array")
	}
	refs := make([]mcpauth.ResourceRef, 0, len(items))
	for _, raw := range items {
		id, ok := int64Value(raw)
		if !ok || id <= 0 {
			return nil, errors.New("user_ids must contain positive integer IDs")
		}
		refs = append(refs, mcpauth.ResourceRef{Type: "user", ID: strconv.FormatInt(id, 10)})
	}
	return refs, nil
}

func authorizationNodeRefs(object map[string]any) ([]mcpauth.ResourceRef, error) {
	if nodeID, ok := int64Value(object["node_id"]); ok && nodeID > 0 {
		nodeType, _ := object["node_type"].(string)
		if !validAuthorizationNodeType(nodeType) {
			return nil, errors.New("invalid node_type")
		}
		return []mcpauth.ResourceRef{{Type: nodeType, ID: strconv.FormatInt(nodeID, 10)}}, nil
	}
	items, ok := object["nodes"].([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("nodes must be a non-empty array")
	}
	refs := make([]mcpauth.ResourceRef, 0, len(items))
	for _, raw := range items {
		node, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("each node must be an object")
		}
		nodeType, _ := node["node_type"].(string)
		nodeID, ok := int64Value(node["node_id"])
		if !validAuthorizationNodeType(nodeType) || !ok || nodeID <= 0 {
			return nil, errors.New("each node requires a valid node_type and positive node_id")
		}
		refs = append(refs, mcpauth.ResourceRef{Type: nodeType, ID: strconv.FormatInt(nodeID, 10)})
	}
	return refs, nil
}

func validAuthorizationNodeType(nodeType string) bool {
	return nodeType == "inbound" || nodeType == "proxy_path" || nodeType == "external_outbound"
}
