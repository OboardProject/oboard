package application

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

type Principal struct {
	ID             string
	GrantID        string
	UserID         *int64
	Name           string
	Type           model.APIPrincipalType
	Role           model.Role
	Scopes         []string
	ResourceFilter json.RawMessage
	SourceIP       netip.Addr
	ClientName     string
	Interactive    bool
	// AccessLevel is set only for OAuth MCP grant principals. When set, the
	// capability catalog authorizes through MinimumAccess + RBAC instead of the
	// legacy fine-grained scopes.
	AccessLevel mcpauth.AccessLevel
	// GrantPolicy is the live grant snapshot used by the unified evaluator. It
	// is never persisted on the principal row.
	GrantPolicy *mcpauth.GrantPolicy
}

type ResourceSelection struct {
	Mode        string  `json:"mode"`
	IDs         []int64 `json:"ids,omitempty"`
	AllowCreate bool    `json:"allow_create,omitempty"`
}

type ResourceFilter struct {
	Servers           *ResourceSelection `json:"servers,omitempty"`
	Users             *ResourceSelection `json:"users,omitempty"`
	ProxyPaths        *ResourceSelection `json:"proxy_paths,omitempty"`
	SubscriptionPlans *ResourceSelection `json:"subscription_plans,omitempty"`
	Settings          *struct {
		AllowedSections []string `json:"allowed_sections"`
	} `json:"settings,omitempty"`
	DestructiveOperations bool `json:"destructive_operations"`
}

func (p Principal) HasScope(required string) bool {
	required = strings.TrimSpace(required)
	if required == "" {
		return true
	}
	if slices.Contains(p.Scopes, "*") || slices.Contains(p.Scopes, required) {
		return true
	}
	domain, _, ok := strings.Cut(required, ":")
	return ok && slices.Contains(p.Scopes, domain+":*")
}

func (p Principal) AllowsInt64(resource string, id int64) bool {
	if len(p.ResourceFilter) == 0 || string(p.ResourceFilter) == "{}" || string(p.ResourceFilter) == "null" {
		return true
	}
	var canonical ResourceFilter
	if json.Unmarshal(p.ResourceFilter, &canonical) != nil {
		return false
	}
	var selection *ResourceSelection
	switch resource {
	case "server_ids":
		selection = canonical.Servers
	case "user_ids":
		selection = canonical.Users
	case "proxy_path_ids":
		selection = canonical.ProxyPaths
	case "subscription_plan_ids":
		selection = canonical.SubscriptionPlans
	}
	if selection != nil {
		switch strings.ToLower(strings.TrimSpace(selection.Mode)) {
		case "all":
			return true
		case "selected":
			return slices.Contains(selection.IDs, id)
		case "none":
			return false
		default:
			return false
		}
	}
	// Existing Service Accounts used flat *_ids arrays. They remain readable
	// while MCP grants are always persisted in the canonical nested format.
	var filters map[string]json.RawMessage
	if json.Unmarshal(p.ResourceFilter, &filters) != nil {
		return false
	}
	raw, exists := filters[resource]
	if !exists {
		return true
	}
	var ids []int64
	if json.Unmarshal(raw, &ids) != nil {
		return false
	}
	return slices.Contains(ids, id)
}

func (p Principal) AllowsCreate(resource string) bool {
	if len(p.ResourceFilter) == 0 || string(p.ResourceFilter) == "{}" || string(p.ResourceFilter) == "null" {
		return true
	}
	var filter ResourceFilter
	if json.Unmarshal(p.ResourceFilter, &filter) != nil {
		return false
	}
	switch resource {
	case "server":
		return filter.Servers != nil && filter.Servers.AllowCreate
	default:
		return false
	}
}

func (p Principal) AllowsSettingSection(section string) bool {
	if len(p.ResourceFilter) == 0 || string(p.ResourceFilter) == "{}" || string(p.ResourceFilter) == "null" {
		return true
	}
	var filter ResourceFilter
	if json.Unmarshal(p.ResourceFilter, &filter) != nil || filter.Settings == nil {
		return false
	}
	return slices.Contains(filter.Settings.AllowedSections, strings.TrimSpace(section))
}

func (p Principal) AllowsDestructiveOperations() bool {
	if len(p.ResourceFilter) == 0 || string(p.ResourceFilter) == "{}" || string(p.ResourceFilter) == "null" {
		return true
	}
	var filter ResourceFilter
	return json.Unmarshal(p.ResourceFilter, &filter) == nil && filter.DestructiveOperations
}

func (p Principal) AllowsGlobal() bool {
	if len(p.ResourceFilter) == 0 || string(p.ResourceFilter) == "{}" || string(p.ResourceFilter) == "null" {
		return true
	}
	var filters struct {
		Global *bool `json:"global"`
	}
	if json.Unmarshal(p.ResourceFilter, &filters) != nil {
		return false
	}
	return filters.Global != nil && *filters.Global
}

// ResourceFilterFromBoundary converts a versioned mcpauth.ResourceBoundary into
// the legacy application.ResourceFilter JSON shape consumed by application
// handlers, automation, and existing REST checks. Unknown resource types are
// omitted (the evaluator enforces them separately). MCP authorization itself
// never reads this legacy filter.
func ResourceFilterFromBoundary(boundary mcpauth.ResourceBoundary) json.RawMessage {
	return mcpauth.LegacyResourceFilterJSON(boundary)
}

func HumanPrincipal(user model.User, role model.Role, sourceIP netip.Addr) Principal {
	scopes := []string{"*"}
	if role == model.RoleViewer {
		scopes = []string{"inventory:read", "servers:read", "users:read", "topology:read", "deployments:read", "audit:read", "notifications:read"}
	} else if role == model.RoleOperator {
		scopes = []string{"inventory:read", "servers:*", "topology:*", "deployments:*", "audit:*", "dns:read", "certificates:read", "tasks:*", "logs:read", "notifications:*", "outbounds:*", "routing_rules:*", "external_outbounds:*", "warp_profiles:*", "dns_lists:read", "dns_records:*", "port_forwards:*", "tunnels:*", "agent_tasks:*", "mtu:*", "backups:read"}
	}
	return Principal{ID: "user:" + formatInt64(user.ID), UserID: &user.ID, Name: user.Username, Type: model.APIPrincipalOAuth, Role: role, Scopes: scopes, SourceIP: sourceIP, ClientName: "oboard-web", Interactive: true}
}

func formatInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
