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
	Description        string             `json:"description"`
	InputSchema        json.RawMessage    `json:"input_schema"`
	OutputSchema       json.RawMessage    `json:"output_schema"`
	RequiredScopes     []string           `json:"required_scopes"`
	ResourceTypes      []string           `json:"resource_types"`
	RiskClass          int                `json:"risk_class"`
	ApprovalPolicy     string             `json:"approval_policy"`
	Idempotent         bool               `json:"idempotent"`
	ReadOnly           bool               `json:"read_only"`
	DataClassification DataClassification `json:"data_classification"`
	SensitiveFields    []string           `json:"sensitive_fields"`
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
	object := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	withID := json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","minimum":1}},"required":["id"],"additionalProperties":false}`)
	descriptors := []Descriptor{
		{Name: "inventory.read", Description: "读取受授权范围内的库存摘要", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"inventory:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "servers.list", Description: "列出受授权服务器", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "servers.get", Description: "读取服务器状态与能力", InputSchema: withID, OutputSchema: object, RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "users.list", Description: "列出不包含凭据的用户摘要", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"users:read"}, ResourceTypes: []string{"user"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true},
		{Name: "topology.read", Description: "读取脱敏后的当前代理拓扑", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"topology:read"}, ResourceTypes: []string{"server", "inbound", "proxy_path"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "audit.incidents.list", Description: "列出结构化审计事件，不返回秘密或连接载荷", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true},
		{Name: "audit.incidents.get", Description: "读取一个结构化审计事件", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`), OutputSchema: object, RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true},
		{Name: "servers.onboarding.plan", Description: "根据当前库存规划节点接入", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"servers:plan"}, ResourceTypes: []string{"server"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "proxy_paths.plan", Description: "根据在线节点、地域和约束规划代理链路候选", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"proxy_paths:plan"}, ResourceTypes: []string{"server", "proxy_path"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "deployments.plan", Description: "计算部署影响范围和前置检查", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"deployments:validate"}, ResourceTypes: []string{"server", "deployment"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true},
		{Name: "audit.incident_response.plan", Description: "根据结构化风险证据生成可逆处置建议", InputSchema: object, OutputSchema: object, RequiredScopes: []string{"audit:analyze"}, ResourceTypes: []string{"user", "audit_incident"}, ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"source_ip", "destination", "user_identity"}, MCPEnabled: true},
	}
	writeDomains := []struct {
		name, scope string
		risk        int
		executable  bool
	}{
		{"servers.onboard", "servers:onboard", 2, true},
		{"subscriptions.resume", "subscriptions:resume", 2, true},
		{"topology.write", "topology:write", 3, true},
		{"deployments.apply", "deployments:apply", 3, true},
	}
	for _, domain := range writeDomains {
		descriptors = append(descriptors, Descriptor{Name: domain.name, Description: "创建受验证和审批保护的管理变更", InputSchema: object, OutputSchema: object, RequiredScopes: []string{domain.scope}, RiskClass: domain.risk, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, Executable: domain.executable})
	}
	return descriptors
}
