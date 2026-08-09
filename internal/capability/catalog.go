package capability

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/authorization"
	"github.com/OboardProject/oboard/internal/mcpauth"
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
	// MinimumAccess is the coarse MCP access level required. It replaces
	// RequiredScopes for MCP authorization.
	MinimumAccess mcpauth.AccessLevel `json:"minimum_access"`
	// RBACPermission is the shared role-based permission string checked by the
	// RBAC service. MCP no longer maintains its own role scope table.
	RBACPermission string `json:"rbac_permission,omitempty"`
	// ResolveResourceRefs extracts resource references from operation input for
	// the unified MCP evaluator's resource-boundary check.
	ResolveResourceRefs func(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) `json:"-"`
}

type Catalog struct {
	items map[string]Descriptor
	rbac  *authorization.RBAC
}

func NewCatalog() *Catalog {
	c := &Catalog{items: map[string]Descriptor{}, rbac: authorization.NewRBAC()}
	for _, item := range defaultDescriptors() {
		if item.RBACPermission == "" {
			item.RBACPermission = item.Name
		}
		c.items[item.Name] = item
		c.rbac.Register(item.Name, authorization.PermissionSpec{ReadOnly: item.ReadOnly, AdminOnly: item.AdminOnly()})
	}
	return c
}

// AdminOnly reports whether the capability is reserved for administrators.
// Destructive topology changes remain available to operators, but always use
// their explicit confirmation schema and the normal Changeset approval flow.
func (d Descriptor) AdminOnly() bool { return d.RBACPermission == "admin.settings" }

// RBAC returns the shared role-based permission service. The Controller wires
// the same instance into the unified MCP evaluator.
func (c *Catalog) RBAC() *authorization.RBAC { return c.rbac }

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
		if item.MCPEnabled && c.authorizePrincipal(principal, item) {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Catalog) Authorize(principal application.Principal, name string) (Descriptor, bool) {
	item, ok := c.Get(name)
	return item, ok && c.authorizePrincipal(principal, item)
}

// authorizePrincipal gates a capability for a principal. OAuth MCP grants use
// the coarse access level plus the shared RBAC service; every other principal
// (Service Account, Session, internal AI) keeps the legacy scope mapping.
func (c *Catalog) authorizePrincipal(principal application.Principal, item Descriptor) bool {
	if principal.AccessLevel != "" {
		if !item.MCPEnabled {
			return false
		}
		if !principal.AccessLevel.Allows(item.MinimumAccess) {
			return false
		}
		return c.rbac.Allows(principal.Role, item.RBACPermission)
	}
	return scopesAllow(principal, item.RequiredScopes)
}

// ScopesForGrant derives the legacy fine-grained scope set implied by a coarse
// MCP grant. It is the union of every MCP-enabled capability that the grant's
// access level and the human role allow. This keeps application handlers that
// still call principal.HasScope consistent with the grant while MCP
// authorization itself uses the unified evaluator.
func (c *Catalog) ScopesForGrant(principal application.Principal) []string {
	seen := map[string]bool{}
	for _, item := range c.items {
		if !item.MCPEnabled || !c.authorizePrincipal(principal, item) {
			continue
		}
		for _, scope := range item.RequiredScopes {
			seen[scope] = true
		}
	}
	out := make([]string, 0, len(seen))
	for scope := range seen {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
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
		{Name: "inventory.read", Description: "读取受授权范围内的库存摘要", InputSchema: emptyInput, OutputSchema: schemaObject(map[string]any{"servers": arrayOf(server), "users": arrayOf(user), "server_count": map[string]any{"type": "integer"}, "online_count": map[string]any{"type": "integer"}, "user_count": map[string]any{"type": "integer"}}, "servers", "users", "server_count", "online_count", "user_count"), RequiredScopes: []string{"inventory:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "servers.list", Description: "列出受授权服务器", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(server)), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "servers.get", Description: "读取服务器状态与能力", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: rawSchema(server), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromID},
		{Name: "users.list", Description: "列出不包含凭据的用户摘要", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(user)), RequiredScopes: []string{"users:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, SensitiveOutput: []string{"username", "nickname"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "topology.read", Description: "读取脱敏后的当前代理拓扑", InputSchema: emptyInput, OutputSchema: schemaObject(map[string]any{"servers": arrayOf(server), "inbounds": arrayOf(inbound), "proxy_paths": arrayOf(path), "proxy_path_steps": arrayOf(step)}, "servers", "inbounds", "proxy_paths", "proxy_path_steps"), RequiredScopes: []string{"topology:read"}, ResourceTypes: []string{"server", "inbound", "proxy_path"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "audit.incidents.list", Description: "列出结构化审计事件，不返回秘密或连接载荷", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(incident)), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "audit.incidents.get", Description: "读取一个结构化审计事件", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "id"), OutputSchema: rawSchema(incident), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: auditIncidentRefFromID},
		{Name: "servers.onboarding.plan", Description: "根据当前库存规划节点接入", InputSchema: serverOnboardingInput, OutputSchema: planOutput, RequiredScopes: []string{"servers:plan"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "proxy_paths.plan", Description: "根据在线节点、地域和约束规划代理链路候选", InputSchema: proxyPlanInput, OutputSchema: planOutput, RequiredScopes: []string{"proxy_paths:plan"}, ResourceTypes: []string{"server", "proxy_path"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: proxyPathPlanRefs},
		{Name: "deployments.plan", Description: "计算部署影响范围和前置检查", InputSchema: deploymentInput, OutputSchema: planOutput, RequiredScopes: []string{"deployments:validate"}, ResourceTypes: []string{"server", "deployment"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefsFromIDs},
		{Name: "audit.incident_response.plan", Description: "根据结构化风险证据生成可逆处置建议", InputSchema: incidentPlanInput, OutputSchema: planOutput, RequiredScopes: []string{"audit:analyze"}, ResourceTypes: []string{"user", "audit_incident"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"source_ip", "destination", "user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: incidentResponseRefs},
	}
	writeDomains := []struct {
		name, scope    string
		risk           int
		executable     bool
		classification DataClassification
		sensitive      []string
	}{
		{"servers.onboard", "servers:onboard", 2, true, DataInternal, nil},
		{"servers.update", "servers:write", 2, true, DataInternal, nil},
		{"subscriptions.resume", "subscriptions:resume", 2, true, DataInternal, nil},
		{"subscriptions.custom_paths.set_alias", "subscriptions:manage", 2, true, DataSensitive, []string{"alias"}},
		{"subscriptions.custom_paths.set_policy", "subscriptions:manage", 2, true, DataInternal, nil},
		{"inbounds.create", "topology:write", 3, true, DataSensitive, []string{"inbound.config_json"}},
		{"inbounds.update", "topology:write", 3, true, DataSensitive, []string{"changes.config_json"}},
		{"topology.write", "topology:write", 3, true, DataInternal, nil},
		{"proxy_paths.create_direct", "topology:write", 3, true, DataInternal, nil},
		{"proxy_paths.update", "topology:write", 3, true, DataInternal, nil},
		{"proxy_path_steps.create", "topology:write", 3, true, DataInternal, nil},
		{"proxy_path_steps.update", "topology:write", 3, true, DataInternal, nil},
		{"topology.reuse_inbound", "topology:write", 3, true, DataInternal, nil},
		{"deployments.apply", "deployments:apply", 3, true, DataInternal, nil},
	}
	for _, domain := range writeDomains {
		input, output, evaluator := executableSchemas(domain.name)
		descriptors = append(descriptors, Descriptor{Name: domain.name, Description: "创建受验证和审批保护的管理变更", InputSchema: input, OutputSchema: output, RequiredScopes: []string{domain.scope}, ResourceEvaluator: evaluator, RiskClass: domain.risk, ApprovalPolicy: "required", Idempotent: true, DataClassification: domain.classification, SensitiveFields: domain.sensitive, SensitiveInput: domain.sensitive, MCPEnabled: true, Executable: domain.executable, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: writeResolver(domain.name)})
	}
	for _, name := range []string{"inbounds.delete", "proxy_paths.delete", "proxy_path_steps.truncate"} {
		input, output, evaluator := executableSchemas(name)
		descriptors = append(descriptors, Descriptor{
			Name: name, Description: "删除受引用保护和审批保护的代理拓扑资源", InputSchema: input, OutputSchema: output,
			RequiredScopes: []string{"topology:write"}, ResourceEvaluator: evaluator, RiskClass: 3,
			ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal,
			Destructive: true, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate,
			ResolveResourceRefs: writeResolver(name),
		})
	}
	for index := range descriptors {
		descriptors[index].Version = "1"
		descriptors[index].Documentation = "oboard://schemas/" + descriptors[index].Name
	}
	return descriptors
}

func writeResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
	switch name {
	case "subscriptions.resume", "subscriptions.custom_paths.set_alias", "subscriptions.custom_paths.set_policy":
		return userRefFromID
	case "topology.write":
		return topologyWriteRefs
	case "inbounds.create":
		return inboundCreateRefs
	case "inbounds.update":
		return inboundUpdateRefs
	case "proxy_paths.update":
		return proxyPathUpdateRefs
	case "proxy_paths.create_direct":
		return proxyPathDirectRefs
	case "proxy_path_steps.create", "proxy_path_steps.update":
		return proxyPathStepWriteRefs
	case "inbounds.delete":
		return inboundDeleteRefs
	case "proxy_paths.delete":
		return proxyPathDeleteRefs
	case "proxy_path_steps.truncate":
		return proxyPathStepTruncateRefs
	case "topology.reuse_inbound":
		return topologyReuseInboundRefs
	case "deployments.apply":
		return serverRefsFromIDs
	case "servers.onboard":
		return serverOnboardRefs
	case "servers.update":
		return serverUpdateRefs
	default:
		return noRefs
	}
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
	case "servers.update":
		changes := closedObject(map[string]any{
			"name": stringValue, "entry_address": stringValue, "entry_ip_mode": stringValue,
			"region_mode": stringValue, "region_code": stringValue, "listen_ip": stringValue,
			"listen_mode": stringValue, "ip_stack": stringValue, "udp_inbound_mode": stringValue,
			"mtu_mode": stringValue, "mtu_value": map[string]any{"type": "integer"},
			"mtu_probe_host": stringValue, "mtu_probe_port": map[string]any{"type": "integer"},
			"mtu_overhead_bytes": map[string]any{"type": "integer"}, "bbr_enabled": boolValue,
			"port_range_start": map[string]any{"type": "integer"}, "port_range_end": map[string]any{"type": "integer"},
			"internal_port_range_start": map[string]any{"type": "integer"}, "internal_port_range_end": map[string]any{"type": "integer"},
			"connection_audit_enabled": boolValue, "time_correction_mode": stringValue,
			"offline_notify_enabled": boolValue, "offline_after_seconds": map[string]any{"type": "integer"},
		})
		return schemaObject(map[string]any{"server_id": positiveID, "changes": changes}, "server_id", "changes"), simpleOutput(map[string]any{"server_id": positiveID, "revision": stringValue, "changed_fields": stringArray(1, 32)}), "server_ids"
	case "deployments.apply":
		return schemaObject(map[string]any{"server_ids": idArray(1, 100), "reason": map[string]any{"type": "string", "maxLength": 500}}, "server_ids"), simpleOutput(map[string]any{"deployment": closedObject(map[string]any{"config_version": map[string]any{"type": "integer"}, "server_ids": idArray(0, 100), "status": stringValue})}), "server_ids"
	case "inbounds.create", "inbounds.update":
		inboundProperties := map[string]any{
			"server_id":             positiveID,
			"name":                  map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"protocol":              map[string]any{"type": "string", "enum": []string{"vless", "hysteria2", "anytls", "shadowsocks", "mieru", "ssh"}},
			"listen_ip":             map[string]any{"type": "string", "maxLength": 255},
			"port":                  map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			"entry_ip_mode":         map[string]any{"type": "string", "enum": []string{"auto", "ipv4", "ipv6", "custom"}},
			"external_ip":           map[string]any{"type": "string", "maxLength": 255},
			"dns_sync_enabled":      boolValue,
			"dns_credential_id":     nullableInteger(),
			"dns_domain":            map[string]any{"type": "string", "maxLength": 253},
			"dns_proxy_enabled":     boolValue,
			"dns_record_types":      map[string]any{"type": "string", "enum": []string{"auto", "a", "aaaa", "both"}},
			"ddns_enabled":          boolValue,
			"ddns_interval_seconds": map[string]any{"type": "integer", "minimum": 300, "maximum": 86400},
			"tls":                   boolValue,
			"certificate_mode":      map[string]any{"type": "string", "enum": []string{"external", "auto", "exact", "wildcard", "explicit"}},
			"certificate_id":        nullableInteger(),
			"certificate_domain":    map[string]any{"type": "string", "maxLength": 253},
			"config_json":           map[string]any{"type": "string", "maxLength": 65536},
			"enabled":               boolValue,
		}
		inboundOutput := closedObject(map[string]any{
			"id": positiveID, "revision": stringValue, "server_id": positiveID, "name": stringValue,
			"protocol": stringValue, "listen_ip": stringValue, "port": map[string]any{"type": "integer"},
			"entry_ip_mode": stringValue, "external_ip": stringValue, "dns_sync_enabled": boolValue,
			"dns_domain": stringValue, "tls": boolValue, "certificate_mode": stringValue,
			"certificate_domain": stringValue, "enabled": boolValue, "advanced_configured": boolValue,
		})
		if name == "inbounds.create" {
			inboundFields := closedObject(inboundProperties, "server_id", "name", "protocol", "port")
			return schemaObject(map[string]any{"inbound": inboundFields}, "inbound"), simpleOutput(map[string]any{"inbound": inboundOutput, "requires_deployment": boolValue}), "server_ids"
		}
		inboundFields := closedObject(inboundProperties)
		return schemaObject(map[string]any{"inbound_id": positiveID, "changes": inboundFields}, "inbound_id", "changes"), simpleOutput(map[string]any{"inbound": inboundOutput, "requires_deployment": boolValue}), "server_ids"
	case "topology.reuse_inbound":
		source := closedObject(map[string]any{"inbound_id": positiveID, "step_id": positiveID})
		return schemaObject(map[string]any{"sources": map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": source}, "target_server_id": positiveID, "target_kind": stringValue, "target_inbound_id": positiveID, "chain_protocol": stringValue, "chain_method": stringValue, "reality_handshake_server": stringValue, "reality_handshake_port": map[string]any{"type": "integer"}, "transport_mode": stringValue, "tunnel_type": stringValue, "ssh_port": map[string]any{"type": "integer"}, "persistent_keepalive": map[string]any{"type": "integer"}, "copy_mode": stringValue, "branch_path_id": positiveID}, "sources", "target_server_id", "target_kind"), simpleOutput(map[string]any{"result_path_count": map[string]any{"type": "integer"}, "affected_server_ids": idArray(0, 100), "requires_deployment": boolValue}), "server_ids"
	case "topology.write":
		namePart := closedObject(map[string]any{"kind": stringValue, "value": stringValue}, "kind")
		path := closedObject(map[string]any{"kind": stringValue, "name": stringValue, "name_mode": stringValue, "name_template": map[string]any{"type": "array", "maxItems": 16, "items": namePart}, "inbound_id": positiveID, "exit_region_mode": stringValue, "exit_region_code": stringValue, "enabled": boolValue})
		step := closedObject(map[string]any{"node_type": stringValue, "transport_mode": stringValue, "processing_role": boolValue, "server_id": positiveID, "inbound_id": positiveID, "external_outbound_id": positiveID, "tunnel_type": stringValue, "ssh_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "persistent_keepalive": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}})
		return schemaObject(map[string]any{"path": path, "steps": map[string]any{"type": "array", "minItems": 0, "maxItems": 5, "items": step}}, "path", "steps"), simpleOutput(map[string]any{"proxy_path": path, "proxy_path_steps": map[string]any{"type": "array", "items": step}, "requires_deployment": boolValue}), "server_ids"
	case "proxy_paths.update":
		namePart := closedObject(map[string]any{"kind": stringValue, "value": stringValue}, "kind")
		changes := closedObject(map[string]any{
			"name_mode":     map[string]any{"type": "string", "enum": []string{"auto", "custom"}},
			"name_template": map[string]any{"type": "array", "maxItems": 16, "items": namePart},
			"inbound_id":    positiveID, "exit_region_mode": map[string]any{"type": "string", "enum": []string{"auto", "manual"}},
			"exit_region_code": map[string]any{"type": "string", "maxLength": 2}, "enabled": boolValue,
		})
		pathOutput := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "kind": stringValue, "name": stringValue, "name_mode": stringValue, "inbound_id": positiveID, "exit_region_mode": stringValue, "exit_region_code": stringValue, "enabled": boolValue})
		return schemaObject(map[string]any{"path_id": positiveID, "changes": changes}, "path_id", "changes"), simpleOutput(map[string]any{"proxy_path": pathOutput, "requires_deployment": boolValue}), "proxy_path_ids"
	case "proxy_paths.create_direct":
		pathOutput := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "kind": stringValue, "name": stringValue, "name_mode": stringValue, "inbound_id": positiveID, "enabled": boolValue})
		stepOutput := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "path_id": positiveID, "position": map[string]any{"type": "integer"}, "node_type": stringValue, "transport_mode": stringValue, "server_id": nullableInteger(), "inbound_id": nullableInteger(), "external_outbound_id": nullableInteger()})
		return schemaObject(map[string]any{"inbound_id": positiveID, "source_path_id": positiveID, "source_step_id": positiveID}), simpleOutput(map[string]any{"proxy_path": pathOutput, "proxy_path_steps": map[string]any{"type": "array", "items": stepOutput}, "requires_deployment": boolValue}), "proxy_path_ids"
	case "proxy_path_steps.create", "proxy_path_steps.update":
		stepProperties := map[string]any{
			"path_id": positiveID, "position": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"node_type":      map[string]any{"type": "string", "enum": []string{"server_inbound", "imported", "warp"}},
			"transport_mode": map[string]any{"type": "string", "enum": []string{"singbox", "port_forward", "tunnel"}},
			"server_id":      positiveID, "inbound_id": positiveID, "external_outbound_id": positiveID,
			"chain_protocol": map[string]any{"type": "string", "enum": []string{"shadowsocks", "vless", "mieru"}},
			"chain_method":   stringValue, "reality_handshake_server": stringValue,
			"reality_handshake_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			"tunnel_type":            map[string]any{"type": "string", "enum": []string{"ssh", "wireguard"}},
			"ssh_port":               map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			"persistent_keepalive":   map[string]any{"type": "integer", "minimum": 0, "maximum": 65535},
			"backend":                map[string]any{"type": "string", "enum": []string{"auto", "realm", "nft", "builtin"}},
			"listen_ip":              map[string]any{"type": "string", "maxLength": 255},
		}
		stepOutput := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "path_id": positiveID, "position": map[string]any{"type": "integer"}, "node_type": stringValue, "transport_mode": stringValue, "server_id": nullableInteger(), "inbound_id": nullableInteger(), "external_outbound_id": nullableInteger()})
		if name == "proxy_path_steps.create" {
			stepInput := closedObject(stepProperties, "path_id", "node_type")
			return schemaObject(map[string]any{"step": stepInput}, "step"), simpleOutput(map[string]any{"proxy_path_step": stepOutput, "requires_deployment": boolValue}), "proxy_path_ids"
		}
		stepInput := closedObject(stepProperties, "path_id")
		return schemaObject(map[string]any{"step_id": positiveID, "changes": stepInput}, "step_id", "changes"), simpleOutput(map[string]any{"proxy_path_step": stepOutput, "requires_deployment": boolValue}), "proxy_path_ids"
	case "inbounds.delete":
		return schemaObject(map[string]any{"inbound_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "inbound_id", "confirm"), simpleOutput(map[string]any{"deleted": boolValue, "inbound_id": positiveID, "deleted_proxy_path_count": map[string]any{"type": "integer"}, "requires_deployment": boolValue}), "server_ids"
	case "proxy_paths.delete":
		return schemaObject(map[string]any{"path_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "path_id", "confirm"), simpleOutput(map[string]any{"deleted": boolValue, "path_id": positiveID, "requires_deployment": boolValue}), "proxy_path_ids"
	case "proxy_path_steps.truncate":
		return schemaObject(map[string]any{"path_id": positiveID, "step_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "path_id", "step_id", "confirm"), simpleOutput(map[string]any{"deleted": boolValue, "path_id": positiveID, "deleted_steps": map[string]any{"type": "integer"}, "path_deleted": boolValue, "requires_deployment": boolValue}), "proxy_path_ids"
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
