package capability

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
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
	// PrivilegeClass is empty for ordinary capabilities. Privileged host
	// operations (remote_operations / remote_exec / remote_shell) are never
	// included in default OAuth consent and require a dedicated Privileged Grant.
	PrivilegeClass string `json:"privilege_class,omitempty"`
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
		c.rbac.Register(item.RBACPermission, authorization.PermissionSpec{ReadOnly: item.ReadOnly, ManagementOnly: item.ManagementOnly()})
	}
	return c
}

// ManagementOnly reports whether the capability is hidden from viewers.
func (d Descriptor) ManagementOnly() bool { return d.RBACPermission == "admin.settings" }

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
		if item.MCPEnabled && c.authorizePrincipal(principal, item) && principalAllowsPrivilege(principal, item) {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Catalog) AllMCPDescriptors() []Descriptor {
	out := make([]Descriptor, 0, len(c.items))
	for _, item := range c.items {
		if item.MCPEnabled {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Catalog) AllDescriptors() []Descriptor {
	out := make([]Descriptor, 0, len(c.items))
	for _, item := range c.items {
		out = append(out, item)
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

func principalAllowsPrivilege(principal application.Principal, item Descriptor) bool {
	if item.PrivilegeClass == "" {
		return true
	}
	for _, class := range principal.PrivilegedClasses {
		if class == item.PrivilegeClass {
			return true
		}
	}
	return false
}

// ScopesForGrant derives the legacy fine-grained scope set implied by a coarse
// MCP grant. It is the union of every MCP-enabled capability that the grant's
// access level and the human role allow. This keeps application handlers that
// still call principal.HasScope consistent with the grant while MCP
// authorization itself uses the unified evaluator.
func (c *Catalog) ScopesForGrant(principal application.Principal) []string {
	seen := map[string]bool{}
	for _, item := range c.items {
		if !item.MCPEnabled || !c.authorizePrincipal(principal, item) || item.PrivilegeClass != "" {
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
		"info":    map[string]any{"title": "OBoard Capability API", "version": "v1"},
		"servers": []map[string]string{{"url": basePath}},
		"paths": map[string]any{
			"/api/v1/capabilities":             map[string]any{"get": map[string]any{"operationId": "listCapabilities", "security": security, "responses": okResponse("Authorized capability descriptors")}},
			"/api/v1/query":                    map[string]any{"post": map[string]any{"operationId": "queryCapability", "security": security, "requestBody": jsonBody(map[string]any{"type": "object", "required": []string{"capability", "arguments"}, "properties": map[string]any{"capability": map[string]string{"type": "string"}, "arguments": map[string]string{"type": "object"}}}), "responses": okResponse("Capability result")}},
			"/api/v1/changesets":               map[string]any{"get": map[string]any{"operationId": "listChangesets", "security": security, "responses": okResponse("Changesets")}, "post": map[string]any{"operationId": "createChangeset", "security": security, "responses": okResponse("Created Changeset")}},
			"/api/v1/changesets/{id}/{action}": map[string]any{"post": map[string]any{"operationId": "actOnChangeset", "security": security, "parameters": []map[string]any{{"name": "id", "in": "path", "required": true, "schema": map[string]string{"type": "string"}}, {"name": "action", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"validate", "approve", "apply"}}}}, "responses": okResponse("Changeset state")}},
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
	publicPortStart := map[string]any{"type": "integer", "description": "公网自动托管端口池起点（默认 10000）"}
	publicPortEnd := map[string]any{"type": "integer", "description": "公网自动托管端口池终点（默认 20000）"}
	internalPortStart := map[string]any{"type": "integer", "description": "回环内部端口池起点（默认 30000）"}
	internalPortEnd := map[string]any{"type": "integer", "description": "回环内部端口池终点（默认 59999）"}
	server := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "status": stringValue,
		"entry_address": stringValue, "entry_ip_mode": stringValue, "region_mode": stringValue,
		"region_code": stringValue, "detected_region_code": stringValue, "public_ipv4": stringValue,
		"public_ipv6": stringValue, "interface_ipv6": stringValue, "ip_stack": stringValue,
		"listen_ip": stringValue, "listen_mode": stringValue, "udp_inbound_mode": stringValue,
		"mtu_mode": stringValue, "mtu_value": map[string]any{"type": "integer"},
		"mtu_probe_host": stringValue, "mtu_probe_port": map[string]any{"type": "integer"},
		"mtu_overhead_bytes": map[string]any{"type": "integer"}, "bbr_enabled": boolValue,
		"port_range_start": publicPortStart, "port_range_end": publicPortEnd,
		"internal_port_range_start": internalPortStart, "internal_port_range_end": internalPortEnd,
		"port_policy_revision": map[string]any{"type": "integer"},
		"agent_connected":      boolValue, "agent_version": stringValue, "agent_build": stringValue,
		"kernel_version": stringValue, "kernel_capabilities": map[string]any{"type": "array", "maxItems": 64, "items": stringValue}, "connection_audit_enabled": boolValue,
		"tcp_fastopen_state":       map[string]any{"type": "string", "enum": []string{"", "unavailable", "disabled", "client", "server", "client_server"}, "description": "Agent 上报的 net.ipv4.tcp_fastopen 状态；只有 server/client_server 才能让入站 tcp_fast_open 生效"},
		"tcp_fastopen_value":       map[string]any{"type": "integer", "description": "net.ipv4.tcp_fastopen 原始位掩码"},
		"resource_history_enabled": boolValue, "monitoring_mode": stringValue,
		"traffic_reset_mode": stringValue, "traffic_reset_day": map[string]any{"type": "integer"}, "traffic_limit_bytes": map[string]any{"type": "integer"},
		"offline_notify_enabled": boolValue, "offline_after_seconds": map[string]any{"type": "integer"},
		"expires_at": nullableString(), "renewal_cycle": map[string]any{"type": "string", "enum": []string{"monthly", "quarterly"}},
		"auto_renew_enabled": boolValue, "expiry_notify_enabled": boolValue, "last_auto_renewed_at": nullableString(),
		"latency_probe_enabled": boolValue, "latency_probe_mode": stringValue, "latency_probe_public_target": stringValue,
		"latency_probe_interval_seconds": map[string]any{"type": "integer"}, "latency_probe_sample_count": map[string]any{"type": "integer"},
		"latency_probe_regions":          map[string]any{"type": "array", "items": closedObject(map[string]any{"province": stringValue, "carrier": stringValue}, "province", "carrier")},
		"latency_probe_max_targets":      map[string]any{"type": "integer"},
		"latency_probe_resource_version": stringValue,
		"time_correction_mode":           stringValue, "time_check_status": stringValue,
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
	planNode := closedObject(map[string]any{"node_type": stringValue, "node_id": positiveID, "display_group": stringValue, "source_type": stringValue, "source_rule_id": map[string]any{"type": "integer"}, "sort_position": nullableInteger()})
	plan := closedObject(map[string]any{
		"id": positiveID, "name": stringValue, "description": stringValue, "enabled": boolValue,
		"lock_version": positiveID, "current_revision_id": positiveID, "latest_revision_id": positiveID,
		"pending_revision_id": map[string]any{"type": "integer", "minimum": 0},
		"speed_limit_mbps":    map[string]any{"type": "integer", "minimum": 0}, "traffic_limit_bytes": stringValue,
		"traffic_reset_mode": stringValue, "traffic_reset_day": map[string]any{"type": "integer"},
		"nodes": arrayOf(planNode), "current_nodes": arrayOf(planNode),
	})
	path := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "kind": stringValue, "name": stringValue, "inbound_id": positiveID, "effective_exit_region_code": stringValue, "exit_region_status": stringValue, "enabled": boolValue})
	step := closedObject(map[string]any{"id": positiveID, "revision": stringValue, "path_id": positiveID, "position": map[string]any{"type": "integer", "minimum": 1}, "node_type": stringValue, "transport_mode": stringValue, "processing_role": boolValue, "server_id": nullableInteger(), "inbound_id": nullableInteger(), "external_outbound_id": nullableInteger(), "advanced_configured": boolValue})
	userGroup := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "name": stringValue, "description": stringValue,
		"role": stringValue, "system_key": stringValue, "enabled": boolValue,
		"subscription_custom_path_policy": stringValue, "created_at": stringValue, "updated_at": stringValue,
	})
	userDevice := closedObject(map[string]any{
		"id": stringValue, "device_id_hash": stringValue, "user_id": positiveID, "name": stringValue,
		"token_prefix": stringValue, "credential_epoch": map[string]any{"type": "integer"},
		"status": stringValue, "subscription_suspended": boolValue, "proxy_access_state": stringValue,
		"created_at": stringValue, "updated_at": stringValue, "last_subscription_at": nullableString(),
		"last_proxy_activity_at": nullableString(), "revoked_at": nullableString(),
	})
	userGroupMember := closedObject(map[string]any{
		"id": positiveID, "group_id": positiveID, "user_id": positiveID, "enabled": boolValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	incident := closedObject(map[string]any{"id": stringValue, "user_id": positiveID, "status": stringValue, "classification": stringValue, "severity": stringValue, "rule_score": map[string]any{"type": "integer"}, "anomaly_score": nullableInteger(), "fingerprint": stringValue, "latest_snapshot_id": stringValue, "opened_at": stringValue, "updated_at": stringValue, "resolved_at": nullableString()})
	planOutput := schemaObject(map[string]any{
		"kind":     stringValue,
		"valid":    boolValue,
		"warnings": stringArray(0, 100),
		"candidates": map[string]any{"type": "array", "maxItems": 100, "items": closedObject(map[string]any{
			"server": closedObject(map[string]any{
				"name": stringValue, "region_code": stringValue, "ip_stack": stringValue, "listen_ip": stringValue,
				"port_range_start": publicPortStart, "port_range_end": publicPortEnd,
				"internal_port_range_start": internalPortStart, "internal_port_range_end": internalPortEnd,
				"latency_probe_enabled": boolValue, "latency_probe_mode": stringValue, "latency_probe_public_target": stringValue,
				"latency_probe_interval_seconds": map[string]any{"type": "integer"}, "latency_probe_sample_count": map[string]any{"type": "integer"},
				"latency_probe_regions": map[string]any{"type": "array", "items": closedObject(map[string]any{"province": stringValue, "carrier": stringValue}, "province", "carrier")}, "latency_probe_max_targets": map[string]any{"type": "integer"},
				"expires_at": stringValue,
			}),
			"action": stringValue, "name": stringValue, "label": stringValue, "agent_connected": boolValue,
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
	probeTarget := map[string]any{"type": "string", "enum": []string{"auto", "cloudflare", "12306", "google"}}
	probeRegion := closedObject(map[string]any{"province": map[string]any{"type": "string", "minLength": 1}, "carrier": map[string]any{"type": "string", "minLength": 1}}, "province", "carrier")
	serverOnboardingInput := schemaObject(map[string]any{
		"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64, "description": "必填。同名已存在时规划结果改为重签发 enrollment，不会建议再创建一条服务器"}, "region_code": map[string]any{"type": "string", "pattern": "^[A-Za-z]{2}$"},
		"ip_stack":         map[string]any{"type": "string", "enum": []string{"auto", "ipv4_only", "ipv6_only", "dual_stack", "prefer_ipv4", "prefer_ipv6"}},
		"entry_address":    map[string]any{"type": "string", "maxLength": 253, "description": "自定义入口地址，仅当 entry_ip_mode=custom 时生效"},
		"entry_ip_mode":    map[string]any{"type": "string", "enum": []string{"auto", "ipv4", "ipv6", "custom"}, "description": "入口地址策略，默认 auto；填了 entry_address 必须为 custom，否则自动模式会忽略该地址"},
		"port_range_start": publicPortStart, "port_range_end": publicPortEnd,
		"internal_port_range_start": internalPortStart, "internal_port_range_end": internalPortEnd,
		"expires_at":               nullableString(),
		"resource_history_enabled": boolValue, "latency_probe_enabled": boolValue, "latency_probe_mode": map[string]any{"type": "string", "enum": []string{"tcp", "icmp"}}, "latency_probe_public_target": probeTarget,
		"latency_probe_interval_seconds": map[string]any{"type": "integer", "minimum": 30, "maximum": 86400}, "latency_probe_sample_count": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
		"latency_probe_regions": map[string]any{"type": "array", "maxItems": 200, "items": probeRegion}, "latency_probe_max_targets": map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
	})
	proxyPlanInput := schemaObject(map[string]any{"entry_server_id": positiveID, "exit_region": map[string]any{"type": "string", "maxLength": 2}, "preferred_relay_regions": stringArray(0, 32), "max_hops": map[string]any{"type": "integer", "minimum": 1, "maximum": 5}, "avoid_server_ids": idArray(0, 100), "objective": map[string]any{"type": "string", "maxLength": 500}}, "entry_server_id")
	deploymentInput := schemaObject(map[string]any{"server_ids": idArray(1, 100), "reason": map[string]any{"type": "string", "maxLength": 500}}, "server_ids")
	incidentPlanInput := schemaObject(map[string]any{"incident_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "user_id": positiveID, "rule_score": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "anomaly_score": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "evidence_refs": stringArray(0, 128)}, "incident_id", "user_id")
	descriptors := []Descriptor{
		{Name: "inventory.read", Description: "读取受授权范围内的库存摘要", InputSchema: emptyInput, OutputSchema: schemaObject(map[string]any{"servers": arrayOf(server), "users": arrayOf(user), "server_count": map[string]any{"type": "integer"}, "online_count": map[string]any{"type": "integer"}, "user_count": map[string]any{"type": "integer"}}, "servers", "users", "server_count", "online_count", "user_count"), RequiredScopes: []string{"inventory:read"}, ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "servers.list", Description: "列出受授权服务器。port_range_* 是公网托管池，internal_port_range_* 是回环内部池，与 servers.get 字段相同", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(server)), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "servers.get", Description: "读取服务器状态与能力。port_range_* 是公网托管池，internal_port_range_* 是回环内部池，与 servers.list 字段相同", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: rawSchema(server), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromID},
		{Name: "servers.metrics.read", Description: "读取服务器当前资源、连接数、系统负载与最近窗口内的流量指标", InputSchema: schemaObject(map[string]any{"server_id": positiveID, "window_hours": map[string]any{"type": "integer", "minimum": 1, "maximum": 72}}, "server_id"), OutputSchema: rawSchema(map[string]any{"type": "object"}), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromServerID},
		{Name: "servers.latency_probes.read", Description: "读取服务器已采集的延迟探测结果，不触发新的探测任务", InputSchema: schemaObject(map[string]any{"server_id": positiveID, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 512}}, "server_id"), OutputSchema: rawSchema(map[string]any{"type": "object"}), RequiredScopes: []string{"servers:read"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: serverRefFromServerID},
		{Name: "users.list", Description: "列出不包含凭据的用户摘要", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(user)), RequiredScopes: []string{"users:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, SensitiveOutput: []string{"username", "nickname"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "subscription_plans.list", Description: "列出订阅套餐及其当前版本状态", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(plan)), RequiredScopes: []string{"subscription_plans:read"}, ResourceTypes: []string{"subscription_plan"}, ResourceEvaluator: "subscription_plan_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: noRefs},
		{Name: "subscription_plans.get", Description: "读取订阅套餐的最新与当前节点快照", InputSchema: schemaObject(map[string]any{"id": positiveID}, "id"), OutputSchema: rawSchema(plan), RequiredScopes: []string{"subscription_plans:read"}, ResourceTypes: []string{"subscription_plan"}, ResourceEvaluator: "subscription_plan_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings", ResolveResourceRefs: subscriptionPlanRefFromID},
		{Name: "topology.read", Description: "读取脱敏后的当前代理拓扑", InputSchema: emptyInput, OutputSchema: schemaObject(map[string]any{"servers": arrayOf(server), "inbounds": arrayOf(inbound), "proxy_paths": arrayOf(path), "proxy_path_steps": arrayOf(step)}, "servers", "inbounds", "proxy_paths", "proxy_path_steps"), RequiredScopes: []string{"topology:read"}, ResourceTypes: []string{"server", "inbound", "proxy_path"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "audit.incidents.list", Description: "列出结构化审计事件，不返回秘密或连接载荷", InputSchema: emptyInput, OutputSchema: rawSchema(arrayOf(incident)), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "audit.incidents.get", Description: "读取一个结构化审计事件", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "id"), OutputSchema: rawSchema(incident), RequiredScopes: []string{"audit:read"}, ResourceTypes: []string{"audit_incident", "user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: auditIncidentRefFromID},
		{Name: "servers.onboarding.plan", Description: "根据当前库存规划节点接入。必须提供 name；同名已存在时 candidates 为 reissue_enrollment 而不是新建", InputSchema: withSchemaDescription(serverOnboardingInput, "name is required, for example {\"name\":\"SJC\"}. Existing names return reissue_enrollment candidates instead of a new server."), OutputSchema: planOutput, RequiredScopes: []string{"servers:plan"}, ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: noRefs},
		{Name: "proxy_paths.plan", Description: "根据在线节点、地域和约束规划代理拓扑候选", InputSchema: proxyPlanInput, OutputSchema: planOutput, RequiredScopes: []string{"proxy_paths:plan"}, ResourceTypes: []string{"server", "proxy_path"}, ResourceEvaluator: "server_ids", ReadOnly: true, Idempotent: true, DataClassification: DataInternal, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: proxyPathPlanRefs},
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
		{"servers.extend_expiry", "servers:write", 1, true, DataInternal, nil},
		{"servers.reset_traffic", "servers:write", 1, true, DataInternal, nil},
		{"subscriptions.resume", "subscriptions:resume", 2, true, DataInternal, nil},
		{"subscriptions.custom_paths.set_alias", "subscriptions:manage", 2, true, DataSensitive, []string{"alias"}},
		{"subscriptions.custom_paths.set_policy", "subscriptions:manage", 2, true, DataInternal, nil},
		{"inbounds.create", "topology:write", 3, true, DataSensitive, []string{"inbound.config_json"}},
		{"inbounds.update", "topology:write", 3, true, DataSensitive, []string{"changes.config_json"}},
		{"inbounds.padding.update", "topology:write", 3, true, DataInternal, nil},
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
		description := "创建受验证和审批保护的管理变更"
		if domain.name == "servers.onboard" {
			description = "创建服务器记录并可选签发一次性接入令牌；名称必须唯一，同名已存在时返回 conflict，应改用 servers.enrollment.issue"
		} else if domain.name == "inbounds.create" {
			description = "创建入口。TLS 入口提交服务器、协议/kind、端口和 dns_domain 即可；certificate_mode=auto 时主控在部署阶段匹配或申请证书，创建不等待证书就绪，不要改用 external 占位或让操作员先去面板申请"
		} else if domain.name == "servers.reset_traffic" {
			description = "将指定服务器当前周期已用流量清零；不影响限额、重置日、用户流量账本，也不触发部署。后续 Agent 上报会重新累计"
		} else if domain.name == "inbounds.padding.update" {
			description = "显式更换、重新生成或自定义 AnyTLS PaddingScheme；会改变流量形态并需要重新部署"
		}
		descriptors = append(descriptors, Descriptor{Name: domain.name, Description: description, InputSchema: input, OutputSchema: output, RequiredScopes: []string{domain.scope}, ResourceEvaluator: evaluator, RiskClass: domain.risk, ApprovalPolicy: "required", Idempotent: true, DataClassification: domain.classification, SensitiveFields: domain.sensitive, SensitiveInput: domain.sensitive, MCPEnabled: true, Executable: domain.executable, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: writeResolver(domain.name)})
		if domain.name == "servers.onboard" {
			descriptors[len(descriptors)-1].SensitiveOutput = []string{"enrollment_token"}
		} else if domain.name == "inbounds.padding.update" {
			descriptors[len(descriptors)-1].RBACPermission = "admin.settings"
		}
	}
	enrollmentInput, enrollmentOutput, _ := executableSchemas("servers.enrollment.issue")
	descriptors = append(descriptors, Descriptor{
		Name: "servers.enrollment.issue", Description: "为已存在服务器重新签发一次性 Agent 接入令牌，不创建新服务器记录",
		InputSchema: enrollmentInput, OutputSchema: enrollmentOutput, RequiredScopes: []string{"servers:onboard"},
		ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", RiskClass: 2, ApprovalPolicy: "required",
		Idempotent: true, DataClassification: DataSensitive, SensitiveOutput: []string{"enrollment_token"},
		MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: serverRefFromServerID,
	})
	deleteInput, deleteOutput, _ := executableSchemas("servers.delete")
	descriptors = append(descriptors, Descriptor{
		Name: "servers.delete", Description: "删除服务器记录及其关联入口、路径与遥测；未接入 Agent 的重复或僵尸记录可直接清理",
		InputSchema: deleteInput, OutputSchema: deleteOutput, RequiredScopes: []string{"servers:write"},
		ResourceTypes: []string{"server"}, ResourceEvaluator: "server_ids", RiskClass: 3, ApprovalPolicy: "required",
		Idempotent: true, DataClassification: DataInternal, Destructive: true, MCPEnabled: true, Executable: true,
		MinimumAccess: mcpauth.AccessOperate, ResolveResourceRefs: serverRefFromServerID,
	})
	input, output, evaluator := executableSchemas("subscription_plans.nodes.update")
	descriptors = append(descriptors, Descriptor{
		Name: "subscription_plans.nodes.update", Description: "新增、移除或替换订阅套餐节点，并通过访问变更流程应用",
		InputSchema: input, OutputSchema: output, RequiredScopes: []string{"subscription_plans:write"},
		ResourceTypes: []string{"subscription_plan", "inbound", "proxy_path", "external_outbound"}, ResourceEvaluator: evaluator,
		RiskClass: 3, ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal,
		MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings",
		ResolveResourceRefs: subscriptionPlanNodesUpdateRefs,
	})
	for _, name := range []string{"inbounds.delete", "proxy_paths.delete", "proxy_path_steps.truncate"} {
		input, output, evaluator := executableSchemas(name)
		descriptors = append(descriptors, Descriptor{
			Name: name, Description: "删除代理拓扑资源，并自动从所有订阅套餐移除已删除节点", InputSchema: input, OutputSchema: output,
			RequiredScopes: []string{"topology:write"}, ResourceEvaluator: evaluator, RiskClass: 3,
			ApprovalPolicy: "required", Idempotent: true, DataClassification: DataInternal,
			Destructive: true, MCPEnabled: true, Executable: true, MinimumAccess: mcpauth.AccessOperate,
			ResolveResourceRefs: writeResolver(name),
		})
	}
	descriptors = append(descriptors, usersAccessDescriptors(user, userGroup, userDevice, userGroupMember, positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, trafficLedgerDescriptors(positiveID, stringValue)...)
	descriptors = append(descriptors, trafficDescriptors(positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, externalOutboundDescriptors(positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, networkDescriptors(positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, forwardsDescriptors(positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, opsDescriptors(positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, auditDescriptors(positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, systemDescriptors(positiveID, stringValue, boolValue, nullableString, nullableInteger)...)
	descriptors = append(descriptors, nodeOperationsDescriptors(positiveID, stringValue, boolValue, nullableString)...)
	descriptors = append(descriptors, nodeWorkspaceDescriptors(positiveID, stringValue, boolValue)...)
	descriptors = append(descriptors, remoteAccessDescriptors(positiveID, stringValue, boolValue)...)
	for index := range descriptors {
		descriptors[index].Version = "1"
		descriptors[index].Documentation = "oboard://schemas/" + descriptors[index].Name
	}
	return descriptors
}

// usersAccessDescriptors builds the users and access-control capability set.
// User, group, member, device, and session capabilities are available to both
// management roles and follow the same administrator-account boundary as the
// panel's 用户与分组 tab.
func usersAccessDescriptors(user, userGroup, userDevice, userGroupMember, positiveID map[string]any, stringValue, boolValue map[string]any, nullableString, nullableInteger func() map[string]any) []Descriptor {
	adminWrite := func(name, description string, input, output json.RawMessage, risk int, destructive bool) Descriptor {
		return Descriptor{
			Name: name, Description: description, InputSchema: input, OutputSchema: output,
			RequiredScopes: []string{name + ":write"}, ResourceEvaluator: "user_ids",
			RiskClass: risk, ApprovalPolicy: "required", Idempotent: true,
			DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"}, SensitiveInput: []string{"user_identity", "password"},
			Destructive: destructive, MCPEnabled: true, Executable: true,
			MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings",
			ResolveResourceRefs: usersWriteResolver(name),
		}
	}
	adminRead := func(name, description string, output json.RawMessage, input json.RawMessage) Descriptor {
		return Descriptor{
			Name: name, Description: description, InputSchema: input, OutputSchema: output,
			RequiredScopes: []string{name + ":read"}, ReadOnly: true, Idempotent: true,
			DataClassification: DataSensitive, SensitiveFields: []string{"user_identity"},
			MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, RBACPermission: "admin.settings",
			ResolveResourceRefs: noRefs,
		}
	}
	userFull := closedObject(map[string]any{
		"id": positiveID, "revision": stringValue, "username": stringValue, "nickname": stringValue,
		"role": stringValue, "status": stringValue, "speed_limit_mbps": map[string]any{"type": "integer"},
		"traffic_limit_bytes": stringValue, "traffic_used_bytes": stringValue,
		"subscription_configured": boolValue, "subscription_age_enabled": boolValue,
		"subscription_suspended": boolValue, "subscription_suspended_at": nullableString(),
		"subscription_suspend_reason": stringValue, "device_limit": map[string]any{"type": "integer"},
		"legacy_proxy_enabled": boolValue, "protected": boolValue,
		"created_at": stringValue, "updated_at": stringValue,
	})
	userUpdateChanges := closedObject(map[string]any{
		"nickname":                     map[string]any{"type": "string", "maxLength": 40},
		"role":                         map[string]any{"type": "string", "enum": []string{"admin", "operator", "viewer", "none"}},
		"status":                       map[string]any{"type": "string", "enum": []string{"active", "disabled"}},
		"password":                     map[string]any{"type": "string", "minLength": 8, "maxLength": 128, "writeOnly": true},
		"speed_limit_mbps":             map[string]any{"type": "integer", "minimum": -1},
		"traffic_limit_bytes":          map[string]any{"type": "integer", "minimum": -1},
		"traffic_reset_mode":           map[string]any{"type": "string", "enum": []string{"monthly", "month_day", "anniversary_month", "never"}},
		"traffic_reset_day":            map[string]any{"type": "integer", "minimum": 0, "maximum": 31},
		"device_limit":                 map[string]any{"type": "integer", "minimum": 0},
		"legacy_proxy_enabled":         boolValue,
		"subscription_burn_after_read": boolValue,
		"subscription_age_enabled":     boolValue,
		"subscription_age_public_key":  map[string]any{"type": "string", "maxLength": 4096},
	})
	userCreate := closedObject(map[string]any{
		"username":                     map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"nickname":                     map[string]any{"type": "string", "maxLength": 40},
		"password":                     map[string]any{"type": "string", "minLength": 8, "maxLength": 128, "writeOnly": true},
		"role":                         map[string]any{"type": "string", "enum": []string{"admin", "operator", "viewer", "none"}},
		"status":                       map[string]any{"type": "string", "enum": []string{"active", "disabled"}},
		"speed_limit_mbps":             map[string]any{"type": "integer", "minimum": -1},
		"traffic_limit_bytes":          map[string]any{"type": "integer", "minimum": -1},
		"traffic_reset_mode":           map[string]any{"type": "string", "enum": []string{"monthly", "month_day", "anniversary_month", "never"}},
		"traffic_reset_day":            map[string]any{"type": "integer", "minimum": 0, "maximum": 31},
		"device_limit":                 map[string]any{"type": "integer", "minimum": 0},
		"legacy_proxy_enabled":         boolValue,
		"subscription_burn_after_read": boolValue,
	}, "username")
	groupChanges := closedObject(map[string]any{
		"name":                            map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"description":                     map[string]any{"type": "string", "maxLength": 200},
		"role":                            map[string]any{"type": "string", "enum": []string{"admin", "operator", "viewer", "none"}},
		"enabled":                         boolValue,
		"subscription_custom_path_policy": map[string]any{"type": "string", "enum": []string{"inherit", "allow", "deny"}},
	})
	return []Descriptor{
		adminRead("users.get", "读取单个用户的管理摘要，不含任何凭据", rawSchema(userFull), schemaObject(map[string]any{"id": positiveID}, "id")),
		adminRead("user_groups.list", "列出全部用户分组", rawSchema(arrayOf(userGroup)), schemaObject(nil)),
		adminRead("user_group_members.list", "列出全部用户分组与用户的成员关系", rawSchema(arrayOf(userGroupMember)), schemaObject(nil)),
		adminRead("user_devices.list", "列出指定用户的已登记设备", schemaObject(map[string]any{"devices": arrayOf(userDevice)}, "devices"), schemaObject(map[string]any{"user_id": positiveID}, "user_id")),
		adminRead("user_devices.list_all", "列出全部用户的已登记设备", schemaObject(map[string]any{"devices": arrayOf(userDevice), "count": map[string]any{"type": "integer"}}, "devices"), schemaObject(nil)),
		adminWrite("users.create", "创建面板用户并分配角色与额度", schemaObject(map[string]any{"user": userCreate}, "user"), schemaObject(map[string]any{"user": userFull}, "user"), 2, false),
		adminWrite("users.update", "修改用户角色、状态、额度与订阅设置", schemaObject(map[string]any{"user_id": positiveID, "changes": userUpdateChanges}, "user_id", "changes"), schemaObject(map[string]any{"user": userFull, "changed_fields": stringArray(1, 32)}, "user"), 2, false),
		adminWrite("users.delete", "删除用户及其所有关联数据", schemaObject(map[string]any{"user_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "user_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "user_id": positiveID}, "deleted"), 3, true),
		adminWrite("users.session_revoke", "吊销用户全部登录会话与访问令牌", schemaObject(map[string]any{"user_id": positiveID}, "user_id"), schemaObject(map[string]any{"session_revoked": boolValue, "user_id": positiveID}, "session_revoked"), 2, false),
		adminWrite("user_groups.create", "创建用户分组并设置角色与策略", schemaObject(map[string]any{"user_group": closedObject(map[string]any{
			"name":                            map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
			"description":                     map[string]any{"type": "string", "maxLength": 200},
			"role":                            map[string]any{"type": "string", "enum": []string{"admin", "operator", "viewer", "none"}},
			"enabled":                         boolValue,
			"subscription_custom_path_policy": map[string]any{"type": "string", "enum": []string{"inherit", "allow", "deny"}},
		}, "name")}, "user_group"), schemaObject(map[string]any{"user_group": userGroup}, "user_group"), 2, false),
		adminWrite("user_groups.update", "修改用户分组的角色、启用状态与订阅策略", schemaObject(map[string]any{"group_id": positiveID, "changes": groupChanges}, "group_id", "changes"), schemaObject(map[string]any{"user_group": userGroup, "changed_fields": stringArray(1, 32)}, "user_group"), 2, false),
		adminWrite("user_groups.delete", "删除用户分组及其全部成员关系", schemaObject(map[string]any{"group_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "group_id", "confirm"), schemaObject(map[string]any{"deleted": boolValue, "group_id": positiveID}, "deleted"), 3, true),
		adminWrite("user_group_members.set", "新增或更新用户与分组的成员关系", schemaObject(map[string]any{"group_id": positiveID, "user_id": positiveID, "enabled": boolValue}, "group_id", "user_id"), schemaObject(map[string]any{"user_group_member": userGroupMember}, "user_group_member"), 2, false),
		adminWrite("user_devices.update", "重命名用户已登记设备", schemaObject(map[string]any{"user_id": positiveID, "device_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 64}, "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64}}, "user_id", "device_id", "name"), schemaObject(map[string]any{"device": userDevice}, "device"), 2, false),
		adminWrite("user_devices.revoke", "吊销用户设备凭据并撤销其代理与订阅访问", schemaObject(map[string]any{"user_id": positiveID, "device_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 64}, "revoked": map[string]any{"type": "boolean", "enum": []any{true}}}, "user_id", "device_id", "revoked"), schemaObject(map[string]any{"device": userDevice, "revoked": boolValue}, "device"), 2, false),
	}
}

func usersWriteResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
	return func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		object, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		if name == "users.create" || name == "user_groups.create" || name == "user_groups.update" || name == "user_groups.delete" {
			return nil, nil
		}
		id, ok := int64Value(object["user_id"])
		if !ok || id <= 0 {
			return nil, errors.New("user_id must be a positive integer ID")
		}
		return []mcpauth.ResourceRef{{Type: "user", ID: strconv.FormatInt(id, 10)}}, nil
	}
}

func writeResolver(name string) func(context.Context, any) ([]mcpauth.ResourceRef, error) {
	switch name {
	case "subscriptions.resume", "subscriptions.custom_paths.set_alias", "subscriptions.custom_paths.set_policy":
		return userRefFromID
	case "topology.write":
		return topologyWriteRefs
	case "inbounds.create":
		return inboundCreateRefs
	case "inbounds.update", "inbounds.padding.update":
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
	case "servers.update", "servers.extend_expiry", "servers.reset_traffic", "servers.enrollment.issue", "servers.delete":
		return serverUpdateRefs
	case "subscription_plans.nodes.update":
		return subscriptionPlanNodesUpdateRefs
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
	case "subscription_plans.nodes.update":
		node := closedObject(map[string]any{
			"node_type": map[string]any{"type": "string", "enum": []string{"inbound", "proxy_path", "external_outbound"}},
			"node_id":   positiveID, "display_group": map[string]any{"type": "string", "maxLength": 100},
		}, "node_type", "node_id")
		return schemaObject(map[string]any{
				"plan_id": positiveID, "op": map[string]any{"type": "string", "enum": []string{"add", "remove", "replace"}},
				"nodes":                 map[string]any{"type": "array", "maxItems": 256, "items": node},
				"display_group":         map[string]any{"type": "string", "maxLength": 100},
				"expected_lock_version": positiveID, "base_revision_id": positiveID,
				"change_summary": map[string]any{"type": "string", "maxLength": 500},
			}, "plan_id", "op", "nodes"), simpleOutput(map[string]any{
				"plan_id": positiveID, "no_change": boolValue, "lock_version": positiveID,
				"latest_revision_id": positiveID, "pending_revision_id": map[string]any{"type": "integer", "minimum": 0},
				"access_change_id": map[string]any{"type": "integer", "minimum": 0}, "access_change_status": stringValue,
				"queued_tasks": map[string]any{"type": "integer", "minimum": 0},
			}), "subscription_plan_ids"
	case "servers.onboard":
		probeTarget := map[string]any{"type": "string", "enum": []string{"auto", "cloudflare", "12306", "google"}}
		probeRegion := closedObject(map[string]any{"province": map[string]any{"type": "string", "minLength": 1}, "carrier": map[string]any{"type": "string", "minLength": 1}}, "province", "carrier")
		serverInput := closedObject(map[string]any{
			"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64}, "region_code": stringValue, "region_mode": stringValue, "ip_stack": stringValue,
			"listen_ip": stringValue, "listen_mode": stringValue, "entry_address": stringValue, "entry_ip_mode": stringValue,
			"port_range_start": map[string]any{"type": "integer", "description": "公网自动托管端口池起点（默认 10000）"}, "port_range_end": map[string]any{"type": "integer", "description": "公网自动托管端口池终点（默认 20000）"},
			"internal_port_range_start": map[string]any{"type": "integer", "description": "回环内部端口池起点（默认 30000）"}, "internal_port_range_end": map[string]any{"type": "integer", "description": "回环内部端口池终点（默认 59999）"},
			"udp_inbound_mode": stringValue, "mtu_mode": stringValue, "mtu_value": map[string]any{"type": "integer"},
			"mtu_probe_host": stringValue, "mtu_probe_port": map[string]any{"type": "integer"}, "mtu_overhead_bytes": map[string]any{"type": "integer"}, "bbr_enabled": boolValue,
			"connection_audit_enabled": boolValue, "time_correction_mode": stringValue,
			"offline_notify_enabled": boolValue, "offline_after_seconds": map[string]any{"type": "integer"},
			"resource_history_enabled": boolValue, "latency_probe_enabled": boolValue, "latency_probe_mode": map[string]any{"type": "string", "enum": []string{"tcp", "icmp"}}, "latency_probe_public_target": probeTarget,
			"latency_probe_interval_seconds": map[string]any{"type": "integer", "minimum": 30, "maximum": 86400}, "latency_probe_sample_count": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
			"latency_probe_regions": map[string]any{"type": "array", "maxItems": 200, "items": probeRegion}, "latency_probe_max_targets": map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
			"service_start_at": stringValue, "expires_at": stringValue, "auto_renew_enabled": boolValue, "renewal_cycle": map[string]any{"type": "string", "enum": []string{"monthly", "quarterly"}}, "expiry_notify_enabled": boolValue,
			"traffic_reset_mode": map[string]any{"type": "string", "enum": []string{"monthly", "month_day"}, "description": "为空时自动按 service_start_at(优先)或 expires_at 的日推导(仅日精度),例如 2025-07-05 起租即每月5日重置"}, "traffic_reset_day": map[string]any{"type": "integer", "minimum": 1, "maximum": 31, "description": "为空时同上自动推导"}, "traffic_limit_bytes": map[string]any{"type": "integer", "minimum": 0}, "traffic_used_bytes": map[string]any{"type": "integer", "minimum": 0},
		})
		return schemaObject(map[string]any{"server": serverInput, "issue_enrollment_token": boolValue}, "server"), simpleOutput(map[string]any{"server": serverInput, "enrollment_expires_at": stringValue, "enrollment_token": stringValue}), "servers.allow_create"
	case "servers.update":
		probeTarget := map[string]any{"type": "string", "enum": []string{"auto", "cloudflare", "12306", "google"}}
		probeRegion := closedObject(map[string]any{"province": map[string]any{"type": "string", "minLength": 1}, "carrier": map[string]any{"type": "string", "minLength": 1}}, "province", "carrier")
		changes := closedObject(map[string]any{
			"name": stringValue, "entry_address": stringValue, "entry_ip_mode": stringValue,
			"region_mode": stringValue, "region_code": stringValue, "listen_ip": stringValue,
			"listen_mode": stringValue, "ip_stack": stringValue, "udp_inbound_mode": stringValue,
			"mtu_mode": stringValue, "mtu_value": map[string]any{"type": "integer"},
			"mtu_probe_host": stringValue, "mtu_probe_port": map[string]any{"type": "integer"},
			"mtu_overhead_bytes": map[string]any{"type": "integer"}, "bbr_enabled": boolValue,
			"port_range_start": map[string]any{"type": "integer", "description": "公网自动托管端口池起点"}, "port_range_end": map[string]any{"type": "integer", "description": "公网自动托管端口池终点"},
			"internal_port_range_start": map[string]any{"type": "integer", "description": "回环内部端口池起点"}, "internal_port_range_end": map[string]any{"type": "integer", "description": "回环内部端口池终点"},
			"connection_audit_enabled": boolValue, "resource_history_enabled": boolValue, "time_correction_mode": stringValue,
			"latency_probe_enabled": boolValue, "latency_probe_mode": map[string]any{"type": "string", "enum": []string{"tcp", "icmp"}},
			"latency_probe_public_target": probeTarget, "latency_probe_interval_seconds": map[string]any{"type": "integer", "minimum": 30, "maximum": 86400},
			"latency_probe_sample_count": map[string]any{"type": "integer", "minimum": 1, "maximum": 10}, "latency_probe_regions": map[string]any{"type": "array", "maxItems": 200, "items": probeRegion},
			"latency_probe_max_targets": map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
			"offline_notify_enabled":    boolValue, "offline_after_seconds": map[string]any{"type": "integer"},
			"service_start_at": stringValue, "clear_service_start_at": boolValue, "expires_at": stringValue, "clear_expires_at": boolValue, "auto_renew_enabled": boolValue, "renewal_cycle": map[string]any{"type": "string", "enum": []string{"monthly", "quarterly"}}, "expiry_notify_enabled": boolValue,
			"traffic_reset_mode": map[string]any{"type": "string", "enum": []string{"monthly", "month_day"}, "description": "为空且账期日期变更时自动按当前 service_start_at(优先)或 expires_at 的日推导；仅设置 traffic_reset_day 时自动使用 month_day"}, "traffic_reset_day": map[string]any{"type": "integer", "minimum": 1, "maximum": 31, "description": "单独设置时自动将 traffic_reset_mode 切换为 month_day；为空时可按账期日期推导"}, "traffic_limit_bytes": map[string]any{"type": "integer", "minimum": 0}, "traffic_used_bytes": map[string]any{"type": "integer", "minimum": 0},
		})
		return schemaObject(map[string]any{"server_id": positiveID, "changes": changes}, "server_id", "changes"), simpleOutput(map[string]any{"server_id": positiveID, "revision": stringValue, "changed_fields": stringArray(1, 32)}), "server_ids"
	case "servers.enrollment.issue":
		return schemaObject(map[string]any{"server_id": positiveID}, "server_id"), simpleOutput(map[string]any{
			"server":                closedObject(map[string]any{"id": positiveID, "name": stringValue, "bbr_enabled": boolValue, "agent_connected": boolValue, "status": stringValue}),
			"enrollment_expires_at": stringValue, "enrollment_token": stringValue,
		}), "server_ids"
	case "servers.delete":
		return schemaObject(map[string]any{"server_id": positiveID, "confirm": map[string]any{"type": "boolean", "const": true}}, "server_id", "confirm"), simpleOutput(map[string]any{"deleted": boolValue, "server_id": positiveID}), "server_ids"
	case "servers.extend_expiry":
		return schemaObject(map[string]any{"server_id": positiveID, "days": map[string]any{"type": "integer", "minimum": 1, "maximum": 3650}}, "server_id", "days"), simpleOutput(map[string]any{"server_id": positiveID, "expires_at": stringValue, "days": map[string]any{"type": "integer"}}), "server_ids"
	case "servers.reset_traffic":
		return schemaObject(map[string]any{"server_id": positiveID}, "server_id"), simpleOutput(map[string]any{
			"server_id":              positiveID,
			"traffic_used_bytes":     map[string]any{"type": "integer", "minimum": 0},
			"traffic_upload_bytes":   map[string]any{"type": "integer", "minimum": 0},
			"traffic_download_bytes": map[string]any{"type": "integer", "minimum": 0},
			"traffic_period_start":   stringValue,
			"traffic_period_end":     stringValue,
		}), "server_ids"
	case "deployments.apply":
		return schemaObject(map[string]any{"server_ids": idArray(1, 100), "reason": map[string]any{"type": "string", "maxLength": 500}}, "server_ids"), simpleOutput(map[string]any{"deployment": closedObject(map[string]any{"config_version": map[string]any{"type": "integer"}, "server_ids": idArray(0, 100), "status": stringValue})}), "server_ids"
	case "inbounds.create", "inbounds.update":
		inboundKinds := []string{"vless-reality", "vless-ws", "vless-tcp", "hy2-tls", "hy2-salamander", "anytls-basic", "anytls-large-padding", "ss-aes-128-gcm", "ss-aes-256-gcm", "ss-2022-128", "ss-2022-256", "mieru-basic", "snell-v4", "snell-v6", "socks5-auth", "ssh-restricted"}
		realityInput := closedObject(map[string]any{
			"handshake_server": map[string]any{"type": "string", "minLength": 1, "maxLength": 253},
			"handshake_port":   map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			"short_id":         map[string]any{"type": "string", "pattern": "^(?:[0-9a-fA-F]{2}){1,8}$"},
		})
		inboundProperties := map[string]any{
			"server_id":             positiveID,
			"name":                  map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"kind":                  map[string]any{"type": "string", "enum": inboundKinds},
			"protocol":              map[string]any{"type": "string", "enum": []string{"vless", "hy2", "anytls", "shadowsocks", "mieru", "snell", "socks", "ssh"}},
			"listen_ip":             map[string]any{"type": "string", "maxLength": 255},
			"port":                  map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			"advertise_port":        map[string]any{"type": "integer", "minimum": 0, "maximum": 65535, "description": "对外端口，0 表示与监听端口一致；启用 NAT 映射时需与监听端口不同"},
			"entry_ip_mode":         map[string]any{"type": "string", "enum": []string{"auto", "ipv4", "ipv6", "custom"}},
			"external_ip":           map[string]any{"type": "string", "maxLength": 255},
			"dns_sync_enabled":      map[string]any{"type": "boolean", "description": "true 时由主控写入解析。创建入口时连同域名一起提交即可，不必先有 DNS 记录或现成证书"},
			"dns_credential_id":     map[string]any{"type": []string{"integer", "null"}, "description": "DNS 凭据。dns_sync 或托管证书自动签发时需要；覆盖该域名的唯一凭据可由 Fast Path 自动选择"},
			"dns_domain":            map[string]any{"type": "string", "maxLength": 253, "description": "入口解析域名。托管证书若未填 certificate_domain，主控用此域名作为 SNI"},
			"dns_proxy_enabled":     boolValue,
			"dns_record_types":      map[string]any{"type": "string", "enum": []string{"auto", "a", "aaaa", "both"}},
			"ddns_enabled":          boolValue,
			"ddns_interval_seconds": map[string]any{"type": "integer", "minimum": 300, "maximum": 86400},
			"tls":                   boolValue,
			"certificate_mode":      map[string]any{"type": "string", "enum": []string{"external", "auto", "exact", "wildcard", "explicit"}, "description": "TLS 入口默认 auto。auto/exact/wildcard 由主控在部署时匹配或申请证书，创建不要求证书已就绪；explicit 才需要 certificate_id；Reality 必须 external"},
			"certificate_id":        map[string]any{"type": []string{"integer", "null"}, "description": "仅 certificate_mode=explicit 时需要；auto 不要填，也不要为了绕过签发去改 external"},
			"certificate_domain":    map[string]any{"type": "string", "maxLength": 253, "description": "SNI 域名，可省略并跟随 dns_domain。托管模式必须最终是有效域名，但不需要证书已经签发完成"},
			"config_json":           map[string]any{"type": "string", "maxLength": 65536},
			"reality":               realityInput,
			"rotate_reality_key":    boolValue,
			"enabled":               boolValue,
		}
		inboundProperties["anytls_padding"] = closedObject(map[string]any{
			"preset_id": map[string]any{"type": "string", "enum": []string{"balanced_v1", "light_v1"}},
			"auto_tune": boolValue,
		}, "preset_id")
		// Guidance for the most common protocol shapes so clients do not need
		// to reverse-engineer the stored config_json contracts:
		//   vless Reality: kind plus the non-secret reality object is the public
		//     input. Controller owns TLS/flow normalization and the keypair.
		//   hysteria2: kind hy2-tls is standard TLS; hy2-salamander adds
		//     per-inbound Salamander obfs. Bandwidth is per-inbound
		//     (default 1000/500) and is not stored in node presets.
		//   hysteria2/anytls: pass dns_domain; certificate_mode=auto lets
		//     Controller issue or match the certificate during deployment.
		//     Create does not wait for a ready certificate.
		//   shadowsocks 2022: method + password (generated when omitted).
		//   mieru: transport/multiplexing defaults are filled automatically.
		//   socks: authenticated SOCKS5 using each authorized user's credentials.
		//   snell: single-PSK protocol; version 4/6 with optional
		//     obfs_mode/obfs_host (v4) or mode (v6), reusable via
		//     config_json.snell_profile_id.
		inboundGuidance := "select an explicit kind; kind=vless-reality accepts only the non-secret reality.handshake_server, reality.handshake_port, and optional reality.short_id fields, while the Controller generates and retains the Reality keypair; set rotate_reality_key=true only when an update must rotate it; config_json.tls.reality.dest and caller-supplied Reality private/public keys are rejected with their exact JSON path before save; TLS kinds anytls-*, hy2-tls, hy2-salamander, and vless-ws default to certificate_mode=auto: pass dns_domain (and dns_sync_enabled plus a covering dns_credential_id when DNS records should be written); omit certificate_domain to follow dns_domain; Controller matches or issues the managed certificate during deployment, so create must not wait for a ready certificate, must not switch to external as a placeholder, and must not send the operator to the panel to pre-issue the certificate; kind=hy2-salamander generates a per-inbound Salamander obfs password; HY2 bandwidth is per-inbound (default up 1000 / down 500) and is not stored in node presets; config_json remains available only for protocol-specific advanced options"
		inboundOutput := closedObject(map[string]any{
			"id": positiveID, "revision": stringValue, "server_id": positiveID, "name": stringValue,
			"protocol": stringValue, "listen_ip": stringValue, "port": map[string]any{"type": "integer"}, "advertise_port": map[string]any{"type": "integer"},
			"entry_ip_mode": stringValue, "external_ip": stringValue, "dns_sync_enabled": boolValue,
			"dns_domain": stringValue, "tls": boolValue, "certificate_mode": stringValue,
			"certificate_domain": stringValue, "kind": stringValue, "enabled": boolValue, "advanced_configured": boolValue,
		})
		if name == "inbounds.create" {
			inboundFields := closedObject(inboundProperties, "server_id", "name", "kind", "port")
			input := schemaObject(map[string]any{"inbound": inboundFields}, "inbound")
			input = withSchemaDescription(input, inboundGuidance)
			input = withSchemaExamples(input, []any{
				map[string]any{"inbound": map[string]any{
					"server_id": 1, "name": "VLESS Reality", "kind": "vless-reality", "port": 443,
					"reality": map[string]any{"handshake_server": "gateway.icloud.com", "handshake_port": 443}, "enabled": true,
				}},
				map[string]any{"inbound": map[string]any{
					"server_id": 1, "name": "OC AnyTLS", "kind": "anytls-basic", "port": 443,
					"dns_sync_enabled": true, "dns_credential_id": 1, "dns_domain": "oc.example.com",
					"certificate_mode": "auto", "enabled": true,
				}},
			})
			return input, simpleOutput(map[string]any{"inbound": inboundOutput, "requires_deployment": boolValue}), "server_ids"
		}
		delete(inboundProperties, "anytls_padding")
		inboundFields := closedObject(inboundProperties)
		input := schemaObject(map[string]any{"inbound_id": positiveID, "changes": inboundFields}, "inbound_id", "changes")
		return withSchemaDescription(input, inboundGuidance), simpleOutput(map[string]any{"inbound": inboundOutput, "requires_deployment": boolValue}), "server_ids"
	case "inbounds.padding.update":
		return schemaObject(map[string]any{
			"inbound_id":     positiveID,
			"operation":      map[string]any{"type": "string", "enum": []string{"replace_preset", "regenerate", "set_custom"}},
			"preset_id":      map[string]any{"type": "string", "enum": []string{"balanced_v1", "light_v1"}},
			"auto_tune":      boolValue,
			"padding_scheme": map[string]any{"type": "array", "minItems": 2, "maxItems": 65, "items": stringValue},
		}, "inbound_id", "operation"), simpleOutput(map[string]any{"inbound": map[string]any{"type": "object"}, "requires_deployment": boolValue}), "server_ids"
	case "topology.reuse_inbound":
		source := closedObject(map[string]any{"inbound_id": positiveID, "step_id": positiveID})
		return schemaObject(map[string]any{"sources": map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": source}, "target_server_id": positiveID, "target_kind": stringValue, "target_inbound_id": positiveID, "chain_protocol": map[string]any{"type": "string", "enum": []string{"shadowsocks", "vless", "mieru", "socks"}}, "chain_method": stringValue, "reality_handshake_server": stringValue, "reality_handshake_port": map[string]any{"type": "integer"}, "transport_mode": stringValue, "tunnel_type": stringValue, "ssh_port": map[string]any{"type": "integer"}, "persistent_keepalive": map[string]any{"type": "integer"}, "copy_mode": stringValue, "branch_path_id": positiveID}, "sources", "target_server_id", "target_kind"), simpleOutput(map[string]any{"result_path_count": map[string]any{"type": "integer"}, "affected_server_ids": idArray(0, 100), "requires_deployment": boolValue}), "server_ids"
	case "topology.write":
		namePart := closedObject(map[string]any{"kind": stringValue, "value": stringValue}, "kind")
		path := closedObject(map[string]any{"kind": stringValue, "name": stringValue, "name_mode": stringValue, "name_template": map[string]any{"type": "array", "maxItems": 16, "items": namePart}, "inbound_id": positiveID, "exit_region_mode": stringValue, "exit_region_code": stringValue, "enabled": boolValue})
		step := closedObject(map[string]any{"node_type": stringValue, "transport_mode": stringValue, "processing_role": boolValue, "server_id": positiveID, "inbound_id": positiveID, "external_outbound_id": positiveID, "tunnel_type": stringValue, "ssh_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "persistent_keepalive": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}})
		routingRule := closedObject(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "priority": map[string]any{"type": "integer"}, "sort_position": map[string]any{"type": "integer", "minimum": 0}, "match_source": map[string]any{"type": "string", "enum": []string{"inline", "rule_set"}}, "rule_set_id": positiveID, "match_json": map[string]any{"type": "string", "maxLength": 8192}, "action": map[string]any{"type": "string", "enum": []string{"direct", "block", "outbound", "external", "interface", "source_prefix"}}, "outbound_id": positiveID, "external_outbound_id": positiveID, "interface_name": stringValue, "source_prefix": stringValue, "sync_source_rule_id": positiveID, "sync_enabled": boolValue, "enabled": boolValue}, "name", "action")
		pathOutput := closedObject(map[string]any{"id": positiveID, "kind": stringValue, "name": stringValue, "name_mode": stringValue, "name_template": map[string]any{"type": "array", "items": namePart}, "inbound_id": positiveID, "exit_region_mode": stringValue, "exit_region_code": stringValue, "exit_region_status": stringValue, "exit_region_source": stringValue, "enabled": boolValue, "created_at": stringValue, "updated_at": stringValue})
		stepOutput := closedObject(map[string]any{"id": positiveID, "path_id": positiveID, "position": positiveID, "node_type": stringValue, "transport_mode": stringValue, "processing_role": boolValue, "server_id": nullableInteger(), "inbound_id": nullableInteger(), "external_outbound_id": nullableInteger(), "config_json": stringValue, "created_at": stringValue, "updated_at": stringValue})
		routingRuleOutput := closedObject(map[string]any{"id": positiveID, "server_id": positiveID, "scope": stringValue, "proxy_path_id": nullableInteger(), "stage_step_id": nullableInteger(), "sort_position": map[string]any{"type": "integer"}, "match_source": stringValue, "rule_set_id": nullableInteger(), "name": stringValue, "priority": map[string]any{"type": "integer"}, "match_json": stringValue, "action": stringValue, "outbound_id": nullableInteger(), "external_outbound_id": nullableInteger(), "target_proxy_path_id": nullableInteger(), "target_server_id": nullableInteger(), "outbound_tag": stringValue, "sync_group_id": stringValue, "enabled": boolValue, "created_at": stringValue, "updated_at": stringValue})
		return schemaObject(map[string]any{"path": path, "steps": map[string]any{"type": "array", "minItems": 0, "maxItems": 5, "items": step}, "routing_rule": routingRule}, "path", "steps"), simpleOutput(map[string]any{"proxy_path": pathOutput, "proxy_path_steps": map[string]any{"type": "array", "items": stepOutput}, "routing_rule": routingRuleOutput, "requires_deployment": boolValue}), "server_ids"
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
			"chain_protocol": map[string]any{"type": "string", "enum": []string{"shadowsocks", "vless", "mieru", "socks"}},
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

// withSchemaDescription attaches a JSON Schema description without turning it
// into an input property.
func withSchemaDescription(schema json.RawMessage, description string) json.RawMessage {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return schema
	}
	root["description"] = description
	encoded, err := json.Marshal(root)
	if err != nil {
		return schema
	}
	return encoded
}

func withSchemaExamples(schema json.RawMessage, examples []any) json.RawMessage {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return schema
	}
	root["examples"] = examples
	encoded, err := json.Marshal(root)
	if err != nil {
		return schema
	}
	return encoded
}

func suggestedChangesetSchema() map[string]any {
	return closedObject(map[string]any{
		"base_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"operation": closedObject(map[string]any{
			"capability": map[string]any{"type": "string"},
			"input":      map[string]any{"type": "object", "additionalProperties": map[string]any{"type": []string{"string", "number", "integer", "boolean", "array", "object", "null"}}},
		}, "capability", "input"),
	}, "base_revisions", "operation")
}
