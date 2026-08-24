package capability

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// forwardsDescriptors builds the port-forward and tunnel capability set
// mirroring the panel's 端口转发 / 隧道 tabs. Topology changes carry risk 3.

func forwardsDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	portForward := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue,
		"source_server_id": positiveID, "target_server_id": nullableInteger(),
		"listen_ip": stringValue, "listen_port": map[string]any{"type": "integer"},
		"target_address": stringValue, "target_port": map[string]any{"type": "integer"},
		"protocol": stringValue, "backend": stringValue, "probe_mode": stringValue,
		"probe_interval_seconds": map[string]any{"type": "integer"}, "sample_rate": map[string]any{"type": "number"},
		"priority": map[string]any{"type": "integer"}, "enabled": boolValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	portForwardFields := closedObject(map[string]any{
		"name":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"source_server_id":       positiveID,
		"target_server_id":       nullableInteger(),
		"listen_ip":              map[string]any{"type": "string", "maxLength": 255},
		"listen_port":            map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"target_address":         map[string]any{"type": "string", "maxLength": 255},
		"target_port":            map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"protocol":               map[string]any{"type": "string", "enum": []string{"tcp", "udp", "tcp_udp"}},
		"backend":                map[string]any{"type": "string", "enum": []string{"auto", "realm", "nft", "builtin"}},
		"probe_mode":             map[string]any{"type": "string", "enum": []string{"never", "apply", "periodic", "sampled", "periodic_sampled"}},
		"probe_interval_seconds": map[string]any{"type": "integer", "minimum": 300},
		"sample_rate":            map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		"priority":               map[string]any{"type": "integer", "minimum": 0},
		"config_json":            map[string]any{"type": "string", "maxLength": 65536},
		"enabled":                boolValue,
	})
	tunnel := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue,
		"source_server_id": positiveID, "target_server_id": positiveID, "type": stringValue,
		"local_address": stringValue, "peer_address": stringValue,
		"listen_port": map[string]any{"type": "integer"}, "target_endpoint": stringValue,
		"target_port": map[string]any{"type": "integer"}, "priority": map[string]any{"type": "integer"},
		"enabled": boolValue, "created_at": stringValue, "updated_at": stringValue,
	})
	tunnelFields := closedObject(map[string]any{
		"name":             map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"source_server_id": positiveID,
		"target_server_id": positiveID,
		"type":             map[string]any{"type": "string", "enum": []string{"wireguard", "ssh"}},
		"local_address":    map[string]any{"type": "string", "maxLength": 255},
		"peer_address":     map[string]any{"type": "string", "maxLength": 255},
		"listen_port":      map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"target_endpoint":  map[string]any{"type": "string", "maxLength": 255},
		"target_port":      map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"priority":         map[string]any{"type": "integer", "minimum": 0},
		"config_json":      map[string]any{"type": "string", "maxLength": 65536},
		"enabled":          boolValue,
	})
	reads := []Descriptor{
		{Name: "port_forwards.list", Description: "列出全部端口转发规则", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(portForward)), RequiredScopes: []string{"port_forwards:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "tunnels.list", Description: "列出全部 WireGuard / SSH 服务器间隧道", InputSchema: schemaObject(nil), OutputSchema: rawSchema(arrayOf(tunnel)), RequiredScopes: []string{"tunnels:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
	}
	writes := []struct {
		name, description string
		input, output     json.RawMessage
		risk              int
		destructive       bool
	}{
		{"port_forwards.create", "创建端口转发规则", schemaObject(map[string]any{"port_forward": portForwardFields}, "port_forward"), schemaObject(map[string]any{"port_forward": portForward}, "port_forward"), 3, false},
		{"port_forwards.update", "修改端口转发规则", schemaObject(map[string]any{"port_forward_id": positiveID, "changes": portForwardFields}, "port_forward_id", "changes"), schemaObject(map[string]any{"port_forward": portForward, "changed_fields": stringArray(1, 32)}, "port_forward"), 3, false},
		{"port_forwards.delete", "删除端口转发规则", schemaObject(map[string]any{"port_forward_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "port_forward_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "port_forward_id": positiveID}, "deleted"), 3, true},
		{"tunnels.create", "创建 WireGuard / SSH 服务器间隧道", schemaObject(map[string]any{"tunnel": tunnelFields}, "tunnel"), schemaObject(map[string]any{"tunnel": tunnel}, "tunnel"), 3, false},
		{"tunnels.update", "修改服务器间隧道", schemaObject(map[string]any{"tunnel_id": positiveID, "changes": tunnelFields}, "tunnel_id", "changes"), schemaObject(map[string]any{"tunnel": tunnel, "changed_fields": stringArray(1, 32)}, "tunnel"), 3, false},
		{"tunnels.delete", "删除服务器间隧道", schemaObject(map[string]any{"tunnel_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "tunnel_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "tunnel_id": positiveID}, "deleted"), 3, true},
	}
	for _, write := range writes {
		reads = append(reads, Descriptor{
			Name: write.name, Description: write.description, InputSchema: write.input, OutputSchema: write.output,
			RequiredScopes: []string{forwardScopeFor(write.name)}, ResourceTypes: []string{"server"},
			ResourceEvaluator: "server_ids", RiskClass: write.risk, ApprovalPolicy: "required", Idempotent: true,
			DataClassification: DataInternal, Destructive: write.destructive, MCPEnabled: true, Executable: true,
			MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: forwardWriteResolver(write.name),
		})
	}
	return reads
}

func forwardScopeFor(name string) string {
	if name == "tunnels.create" || name == "tunnels.update" || name == "tunnels.delete" {
		return "tunnels:write"
	}
	return "port_forwards:write"
}

func forwardWriteResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
	return func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		refs := []mcpauth.ResourceRef{}
		addServers := func(nested map[string]any) {
			for _, field := range []string{"source_server_id", "target_server_id"} {
				if id, ok := int64Value(nested[field]); ok && id > 0 {
					refs = append(refs, mcpauth.ResourceRef{Type: "server", ID: strconv.FormatInt(id, 10)})
				}
			}
		}
		switch name {
		case "port_forwards.create":
			nested, _ := object["port_forward"].(map[string]any)
			addServers(nested)
		case "port_forwards.update":
			changes, _ := object["changes"].(map[string]any)
			addServers(changes)
		case "tunnels.create":
			nested, _ := object["tunnel"].(map[string]any)
			addServers(nested)
		case "tunnels.update":
			changes, _ := object["changes"].(map[string]any)
			addServers(changes)
		}
		return refs, nil
	}
}
