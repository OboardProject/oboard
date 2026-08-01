package application

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type Principal struct {
	ID             string
	UserID         *int64
	Name           string
	Type           model.APIPrincipalType
	Role           model.Role
	Scopes         []string
	ResourceFilter json.RawMessage
	SourceIP       netip.Addr
	ClientName     string
	Interactive    bool
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

func HumanPrincipal(user model.User, role model.Role, sourceIP netip.Addr) Principal {
	scopes := []string{"*"}
	if role == model.RoleViewer {
		scopes = []string{"inventory:read", "servers:read", "users:read", "topology:read", "deployments:read", "audit:read", "notifications:read"}
	} else if role == model.RoleOperator {
		scopes = []string{"inventory:read", "servers:*", "topology:*", "deployments:*", "audit:*", "dns:read", "certificates:read", "tasks:*", "logs:read", "notifications:*"}
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
