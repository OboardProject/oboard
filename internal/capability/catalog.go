package capability

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
)

type DataClassification string

const (
	DataPublic    DataClassification = "public"
	DataInternal  DataClassification = "internal"
	DataSensitive DataClassification = "sensitive"
)

type Descriptor struct {
	Name               string             `json:"name"`
	Version            string             `json:"version"`
	Description        string             `json:"description"`
	InputSchema        json.RawMessage    `json:"input_schema"`
	OutputSchema       json.RawMessage    `json:"output_schema"`
	RequiredScopes     []string           `json:"required_scopes"`
	ResourceTypes      []string           `json:"resource_types"`
	ResourceEvaluator  string             `json:"resource_filter_evaluator"`
	RiskClass          int                `json:"risk_class"`
	ApprovalPolicy     string             `json:"approval_policy"`
	Idempotent         bool               `json:"idempotent"`
	ReadOnly           bool               `json:"read_only"`
	DataClassification DataClassification `json:"data_classification"`
	SensitiveFields    []string           `json:"sensitive_fields"`
	SensitiveInput     []string           `json:"sensitive_input_fields"`
	SensitiveOutput    []string           `json:"sensitive_output_fields"`
	Destructive        bool               `json:"destructive"`
	OpenWorld          bool               `json:"open_world"`
	Documentation      string             `json:"documentation"`
	DeprecatedSince    string             `json:"deprecated_since,omitempty"`
	Replacement        string             `json:"replacement,omitempty"`
	MCPEnabled         bool               `json:"mcp_enabled"`
	Executable         bool               `json:"executable"`
}

type Catalog struct {
	items map[string]Descriptor
}

func NewCatalog() *Catalog {
	c := &Catalog{items: map[string]Descriptor{}}
	for _, item := range defaultDescriptors() {
		c.items[item.Name] = item
	}
	return c
}

func (c *Catalog) Get(name string) (Descriptor, bool) {
	item, ok := c.items[strings.TrimSpace(name)]
	return item, ok
}

func (c *Catalog) List(principal application.Principal) []Descriptor {
	out := make([]Descriptor, 0, len(c.items))
	for _, item := range c.items {
		if scopesAllow(principal, item.RequiredScopes) {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Catalog) ListMCP(principal application.Principal) []Descriptor {
	out := make([]Descriptor, 0, len(c.items))
	for _, item := range c.items {
		if item.MCPEnabled && scopesAllow(principal, item.RequiredScopes) {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Catalog) Authorize(principal application.Principal, name string) (Descriptor, bool) {
	item, ok := c.Get(name)
	return item, ok && scopesAllow(principal, item.RequiredScopes)
}

func (c *Catalog) OpenAPI(basePath string) map[string]any {
	capabilities := make([]Descriptor, 0, len(c.items))
	for _, item := range c.items {
		capabilities = append(capabilities, item)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	security := []map[string][]string{{"bearerAuth": {}}}
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "OBoard Capability API", "version": "v2"},
		"servers": []map[string]string{{"url": basePath}},
		"paths": map[string]any{
			"/api/v2/capabilities":             map[string]any{"get": map[string]any{"operationId": "listCapabilities", "security": security, "responses": okResponse("Authorized capability descriptors")}},
			"/api/v2/query":                    map[string]any{"post": map[string]any{"operationId": "queryCapability", "security": security, "requestBody": jsonBody(map[string]any{"type": "object", "required": []string{"capability", "arguments"}, "properties": map[string]any{"capability": map[string]string{"type": "string"}, "arguments": map[string]string{"type": "object"}}}), "responses": okResponse("Capability result")}},
			"/api/v2/changesets":               map[string]any{"get": map[string]any{"operationId": "listChangesets", "security": security, "responses": okResponse("Changesets")}, "post": map[string]any{"operationId": "createChangeset", "security": security, "responses": okResponse("Created Changeset")}},
			"/api/v2/changesets/{id}/{action}": map[string]any{"post": map[string]any{"operationId": "actOnChangeset", "security": security, "parameters": []map[string]any{{"name": "id", "in": "path", "required": true, "schema": map[string]string{"type": "string"}}, {"name": "action", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"validate", "approve", "apply"}}}}, "responses": okResponse("Changeset state")}},
		},
		"components":            map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]string{"type": "http", "scheme": "bearer"}}},
		"x-oboard-capabilities": capabilities,
	}
}

func jsonBody(schema map[string]any) map[string]any {
	return map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
}

func okResponse(description string) map[string]any {
	return map[string]any{"200": map[string]any{"description": description}, "400": map[string]any{"description": "Invalid request"}, "401": map[string]any{"description": "Authentication required"}, "403": map[string]any{"description": "Scope or resource denied"}}
}

func scopesAllow(principal application.Principal, scopes []string) bool {
	for _, scope := range scopes {
		if !principal.HasScope(scope) {
			return false
		}
	}
	return true
}

func defaultDescriptors() []Descriptor {
	emptyInput := schemaObject(nil)
	positiveID := map[string]any{"type": "integer", "minimum": 1}
	stringValue := map[string]any{"type": "string"}
	boolValue := map[string]any{"type": "boolean"}
	server := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "status": stringValue,
		"entry_address": stringValue, "public_ipv4": stringValue, "public_ipv6": stringValue,
		"interface_ipv6": stringValue, "region_code": stringValue, "detected_region_code": stringValue,
		"ip_stack": stringValue, "listen_mode": stringValue, "udp_inbound_mode": stringValue,
		"mtu_mode": stringValue, "mtu_value": map[string]any{"type": "integer"}, "bbr_enabled": boolValue,
		"agent_connected": boolValue, "agent_version": stringValue, "agent_build": stringValue,
		"kernel_version": stringValue, "connection_audit_enabled": boolValue,
		"time_correction_mode": stringValue, "time_check_status": stringValue,
		"last_seen_at": nullableString(), "created_at": stringValue, "updated_at": stringValue,
	})
	user := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "username": stringValue, "nickname": stringValue,
		"role": stringValue, "status": stringValue, "speed_limit_mbps": map[string]any{"type": "integer"},
		"traffic_limit_bytes": stringValue, "traffic_used_bytes": stringValue,
		"subscription_configured": boolValue, "subscription_age_enabled": boolValue,
		"subscription_suspended": boolValue, "subscription_suspended_at": nullableString(),
		"subscription_suspend_reason": stringValue, "created_at": stringValue, "updated_at": stringValue,
	})
	inbound := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "server_id": positiveID, "name": stringValue, "protocol": stringValue, "listen_ip": stringValue, "port": map[string]any{"type": "integer"}, "dns_domain": stringValue, "dns_sync_enabled": boolValue, "tls": boolValue, "enabled": boolValue, "advanced_configured": boolValue})
	path := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "kind": stringValue, "name": stringValue, "inbound_id": positiveID, "effective_exit_region_code": stringValue, "exit_region_status": stringValue, "enabled": boolValue})
	step := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "path_id": positiveID, "position": map[string]any{"type": "integer", "minimum": 1}, "node_type": stringValue, "transport_mode": stringValue, "processing_role": boolValue, "server_id": nullableInteger(), "inbound_id": nullableInteger(), "external_outbound_id": nullableInteger(), "advanced_configured": boolValue})
	incident := closedObject(map[string]any{"id": stringValue, "user_id": positiveID, "status": stringValue, "classification": stringValue, "severity": stringValue, "rule_score": map[string]any{"type": "integer"}, "anomaly_score": nullableInteger(), "fingerprint": stringValue, "latest_snapshot_id": stringValue, "opened_at": stringValue, "updated_at": stringValue, "resolved_at": nullableString()})
	planOutput := schemaObject(map[string]any{
		"kind":     stringValue,
		"valid":    boolValue,
		"warnings": stringArray(0, 100),
		"candidates": map[string]any{"type": "array", "maxItems": 100, "items": closedObject(map[string]any{
			"server":                    closedObject(map[string]any{"name": stringValue, "region_code": stringValue, "ip_stack": stringValue, "listen_ip": stringValue, "port_range_start": map[string]any{"type": "integer"}, "port_range_end": map[string]any{"type": "integer"}}),
			"requires_external_install": boolValue, "entry_server_id": positiveID, "entry_inbound_id": positiveID,
			"exit_server_id": positiveID, "exit_region": stringValue, "hops": map[string]any{"type": "integer"},
			"objective": stringValue, "requires_topology_changeset": boolValue, "server_id": positiveID,
			"revision": stringValue, "ready": boolValue, "requires_core_reload": boolValue,
			"incident_id": stringValue, "user_id": positiveID, "recommended_actions": stringArray(0, 16),
			"evidence_refs": stringArray(0, 128), "automatic_enforcement": boolValue,
			"suggested_changeset": suggestedChangesetSchema(),
		})},
		"suggested_changeset": suggestedChangesetSchema(),
	}, "kind", "valid", "warnings", "candidates")
	serverOnboardingInput := schemaObject(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64}, "region_code": map[string]any{"type": "string", "pattern": "^[A-Za-z]{2}$"}, "ip_stack": map[string]any{"type": "string", "enum": []string{"auto", "ipv4_only", "ipv6_only", "dual_stack", "prefer_ipv4", "prefer_ipv6"}}}, "name")
	proxyPlanInput := schemaObject(map[string]any{"entry_server_id": positiveID, "exit_region": map[string]any{"type": "string", "maxLength": 2}, "preferred_relay_regions": stringArray(0, 32), "max_hops": map[string]any{"type": "integer", "minimum": 1, "maximum": 5}, "avoid_server_ids": idArray(0, 100), "objective": map[string]any{"type": "string", "maxLength": 500}}, "entry_server_id")
	deploymentInput := schemaObject(map[string]any{"server_ids": idArray(1, 100), "reason": map[string]any{"type": "string", "maxLength": 500}}, "server_ids")
	incidentPlanInput := schemaObject(map[string]any{"incident_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "user_id": positiveID, "rule_score": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "anomaly_score": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "evidence_refs": stringArray(0, 128)}, "incident_id", "user_id")
	descriptors := []Descriptor{
		{Name: "inventory.read", Description: "读取受授权范围内的库存摘要", InputSchema: emptyInput, OutputSchema: schemaObject(map[string]any{"servers": arrayOf(server), "users": arrayOf(user), "server_count": map[string]any{"type": "integer"}, "online_count": map[string]any{"type": "integer"}, "user_count": map[string]any{"type": "integer"}}, "servers", "users", "server_count", "online_count", "user_count"), RequiredScopes: []string{"inventory:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "servers.list", Description: "列出受授权服务器", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(server)), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "servers.get", Description: "读取服务器状态与能力", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: rawSchema(server), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "users.list", Description: "列出不包含凭据的用户摘要", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(user)), RequiredScopes: []string{"users:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, SensitiveOutput: []string{"username", "nickname"}, MCPEnabled: true},
		{Name: "topology.read", Description: "读取脱敏后的当前代理拓扑", InputSchema: emptyInput, OutputSchema: schemaObject(map[string]any{"servers": arrayOf(server), "inbounds": arrayOf(inbound), "proxy_paths": arrayOf(path), "proxy_path_steps": arrayOf(step)}, "servers", "inbounds", "proxy_paths", "proxy_path_steps"), RequiredScopes: []string{"topology:read"}, ResourceTypes: []string{"server", "inbound", "proxy_path"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "audit.incidents.list", Description: "列出结构化审计事件，不返回秘密或连接载荷", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(incident)), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true},
		{Name: "audit.incidents.get", Description: "读取一个结构化审计事件", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "id"), OutputSchema: rawSchema(incident), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true},
		{Name: "servers.onboarding.plan", Description: "根据当前库存规划节点接入", InputSchema: serverOnboardingInput, OutputSchema: planOutput, RequiredScopes: []string{"servers:plan"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "proxy_paths.plan", Description: "根据在线节点、地域和约束规划代理链路候选", InputSchema: proxyPlanInput, OutputSchema: planOutput, RequiredScopes: []string{"proxy_paths:plan"}, ResourceTypes: []string{"server", "proxy_path"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "deployments.plan", Description: "计算部署影响范围和前置检查", InputSchema: deploymentInput, OutputSchema: planOutput, RequiredScopes: []string{"deployments:validate"}, ResourceTypes: []string{"server", "deployment"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "audit.incident_response.plan", Description: "根据结构化风险证据生成可逆处置建议", InputSchema: incidentPlanInput, OutputSchema: planOutput, RequiredScopes: []string{"audit:analyze"}, ResourceTypes: []string{"user", "audit_incident"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"source_ip", "destination", "user_identity"}, MCPEnabled: true},
	}
	writeDomains := []struct {
		name, scope    string
		risk           int
		executable     bool
		classification DataClassification
		sensitive      []string
	}{
		{"servers.onboard", "servers:onboard", 2, true, DataInternal, nil},
		{"subscriptions.resume", "subscriptions:resume", 2, true, DataInternal, nil},
		{"subscriptions.custom_paths.set_alias", "subscriptions:manage", 2, true, DataSensitive, []string{"alias"}},
		{"subscriptions.custom_paths.set_policy", "subscriptions:manage", 2, true, DataInternal, nil},
		{"topology.write", "topology:write", 3, true, DataInternal, nil},
		{"topology.reuse_inbound", "topology:write", 3, true, DataInternal, nil},
		{"deployments.apply", "deployments:apply", 3, true, DataInternal, nil},
	}
	for _, domain := range writeDomains {
		input, output, evaluator := executableSchemas(domain.name)
		descriptors = append(descriptors, Descriptor{Name: domain.name, Description: "创建受验证和审批保护的管理变更", InputSchema: input, OutputSchema: output, RequiredScopes: []string{domain.scope}, ResourceEvaluator: evaluator, RiskClass: domain.risk, ApprovalPolicy: "required", Idempotent: true, DataClassification: domain.classification, SensitiveFields: domain.sensitive, SensitiveInput: domain.sensitive, MCPEnabled: true, Executable: domain.executable})
	}
	for index := range descriptors {
		descriptors[index].Version = "1"
		descriptors[index].Documentation = "oboard://schemas/" + descriptors[index].Name
	}
	return descriptors
}

func executableSchemas(name string) (json.RawMessage, json.RawMessage, string) {
	positiveID := map[string]any{"type": "integer", "minimum": 1}
	stringValue := map[string]any{"type": "string"}
	boolValue := map[string]any{"type": "boolean"}
	simpleOutput := func(properties map[string]any) json.RawMessage { return schemaObject(properties) }
	switch name {
	case "subscriptions.resume":
		return schemaObject(map[string]any{"user_id": positiveID}, "user_id"), simpleOutput(map[string]any{"id": positiveID, "user_id": positiveID, "resumed": boolValue}), "user_ids"
	case "subscriptions.custom_paths.set_alias":
		return schemaObject(map[string]any{"user_id": positiveID, "alias": map[string]any{"type": "string", "maxLength": 64}, "delete": boolValue}, "user_id"), simpleOutput(map[string]any{"user_id": positiveID, "deleted": boolValue, "subscription_custom_path": closedObject(map[string]any{"user_id": positiveID, "alias": stringValue})}), "user_ids"
	case "subscriptions.custom_paths.set_policy":
		return schemaObject(map[string]any{"target_type": map[string]any{"type": "string", "enum": []string{"global", "user", "group"}}, "target_id": map[string]any{"type": "integer", "minimum": 0}, "mode": stringValue}, "target_type", "mode"), simpleOutput(map[string]any{"target_type": stringValue, "target_id": map[string]any{"type": "integer"}, "mode": stringValue}), "user_ids"
	case "servers.onboard":
		serverInput := closedObject(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64}, "region_code": stringValue, "ip_stack": stringValue, "listen_ip": stringValue, "listen_mode": stringValue, "entry_address": stringValue, "port_range_start": map[string]any{"type": "integer"}, "port_range_end": map[string]any{"type": "integer"}, "udp_inbound_mode": stringValue, "mtu_mode": stringValue, "mtu_value": map[string]any{"type": "integer"}, "bbr_enabled": boolValue})
		return schemaObject(map[string]any{"server": serverInput, "issue_enrollment_token": boolValue}, "server"), simpleOutput(map[string]any{"server": serverInput, "enrollment_expires_at": stringValue, "enrollment_token": stringValue}), "servers.allow_create"
	case "deployments.apply":
		return schemaObject(map[string]any{"server_ids": idArray(1, 100), "reason": map[string]any{"type": "string", "maxLength": 500}}, "server_ids"), simpleOutput(map[string]any{"deployment": closedObject(map[string]any{"config_version": map[string]any{"type": "integer"}, "server_ids": idArray(0, 100), "status": stringValue})}), "server_ids"
	case "topology.reuse_inbound":
		source := closedObject(map[string]any{"inbound_id": positiveID, "step_id": positiveID})
		return schemaObject(map[string]any{"sources": map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": source}, "target_server_id": positiveID, "target_kind": stringValue, "target_inbound_id": positiveID, "chain_protocol": stringValue, "chain_method": stringValue, "reality_handshake_server": stringValue, "reality_handshake_port": map[string]any{"type": "integer"}, "transport_mode": stringValue, "tunnel_type": stringValue, "ssh_port": map[string]any{"type": "integer"}, "persistent_keepalive": map[string]any{"type": "integer"}, "copy_mode": stringValue, "branch_path_id": positiveID}, "sources", "target_server_id", "target_kind"), simpleOutput(map[string]any{"result_path_count": map[string]any{"type": "integer"}, "affected_server_ids": idArray(0, 100), "requires_deployment": boolValue}), "server_ids"
	case "topology.write":
		path := closedObject(map[string]any{"kind": stringValue, "name": stringValue, "name_mode": stringValue, "name_template": stringArray(0, 16), "inbound_id": positiveID, "exit_region_mode": stringValue, "exit_region_code": stringValue, "enabled": boolValue})
		step := closedObject(map[string]any{"node_type": stringValue, "transport_mode": stringValue, "processing_role": boolValue, "server_id": positiveID, "inbound_id": positiveID, "external_outbound_id": positiveID, "tunnel_type": stringValue})
		return schemaObject(map[string]any{"path": path, "steps": map[string]any{"type": "array", "minItems": 1, "maxItems": 5, "items": step}}, "path", "steps"), simpleOutput(map[string]any{"proxy_path": path, "proxy_path_steps": map[string]any{"type": "array", "items": step}, "requires_deployment": boolValue}), "server_ids"
	default:
		return schemaObject(nil), schemaObject(nil), ""
	}
}

func schemaObject(properties map[string]any, required ...string) json.RawMessage {
	return rawSchema(closedObject(properties, required...))
}

func closedObject(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		value["required"] = required
	}
	return value
}

func rawSchema(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func arrayOf(item any) map[string]any { return map[string]any{"type": "array", "items": item} }

func stringArray(min, max int) map[string]any {
	return map[string]any{"type": "array", "minItems": min, "maxItems": max, "items": map[string]any{"type": "string"}}
}

func idArray(min, max int) map[string]any {
	return map[string]any{"type": "array", "minItems": min, "maxItems": max, "uniqueItems": true, "items": map[string]any{"type": "integer", "minimum": 1}}
}

func nullableString() map[string]any  { return map[string]any{"type": []string{"string", "null"}} }
func nullableInteger() map[string]any { return map[string]any{"type": []string{"integer", "null"}} }

func suggestedChangesetSchema() map[string]any {
	return closedObject(map[string]any{
		"base_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"operation": closedObject(map[string]any{
			"capability": map[string]any{"type": "string"},
			"input":      map[string]any{"type": "object", "additionalProperties": map[string]any{"type": []string{"string", "number", "integer", "boolean", "array", "object", "null"}}},
		}, "capability", "input"),
	}, "base_revisions", "operation")
}
