package authorization

import (
	"sync"

	"github.com/OboardProject/oboard/internal/model"
)

// PermissionSpec describes one RBAC permission. It is registered once per
// capability and shared by Web, REST, and MCP authorization so a capability is
// never accidentally dropped to read-only because a scope string was omitted.
type PermissionSpec struct {
	// ReadOnly permissions are available to Viewers (read, analyze, plan,
	// validate). Writes are never granted to Viewers.
	ReadOnly bool
	// ManagementOnly keeps the capability unavailable to viewers. Both
	// administrators and operators are management roles.
	ManagementOnly bool
}

// RBAC is the single role-based permission service. It replaces the static
// per-role scope arrays used by the legacy HumanPrincipal mapping, whose
// operator mapping missed subscription and other business domains.
type RBAC struct {
	mu          sync.RWMutex
	permissions map[string]PermissionSpec
}

func NewRBAC() *RBAC {
	return &RBAC{permissions: map[string]PermissionSpec{}}
}

func (r *RBAC) Register(name string, spec PermissionSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permissions[name] = spec
}

func (r *RBAC) RegisterAll(specs map[string]PermissionSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, spec := range specs {
		r.permissions[name] = spec
	}
}

// Allows reports whether the role grants the permission. Unknown permissions
// always deny, so a typo cannot silently escalate.
func (r *RBAC) Allows(role model.Role, permission string) bool {
	r.mu.RLock()
	spec, ok := r.permissions[permission]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	switch role {
	case model.RoleAdmin, model.RoleOperator:
		return true
	case model.RoleViewer:
		return spec.ReadOnly && !spec.ManagementOnly
	default:
		return false
	}
}

// RoleRank returns the rank of a role for comparisons. Higher is more
// privileged. Used by migration and summary code.
func RoleRank(role model.Role) int {
	switch role {
	case model.RoleNone:
		return 0
	case model.RoleViewer:
		return 1
	case model.RoleOperator:
		return 2
	case model.RoleAdmin:
		return 3
	default:
		return 0
	}
}
