package mcpauth

import (
	"slices"
)

// ParseAccessLevel normalizes a raw access level string. The server never
// invents new levels; unknown values return an empty level that denies all
// capabilities.
func ParseAccessLevel(raw string) AccessLevel {
	switch AccessLevel(raw) {
	case AccessRead, AccessOperate:
		return AccessLevel(raw)
	default:
		return ""
	}
}

// Allows reports whether the grant access level satisfies a capability's
// minimum access. operate includes read.
func (a AccessLevel) Allows(minimum AccessLevel) bool {
	if a == AccessOperate {
		return true
	}
	return a == AccessRead && minimum == AccessRead
}

// RequiredScope returns the OAuth scope a client must request for the level.
func (a AccessLevel) RequiredScope() string {
	if a == AccessOperate {
		return ScopeOperate
	}
	return ScopeRead
}

// NormalizedScopes returns the canonical OAuth scope list for the grant in the
// fixed order read, operate, offline. The client may request a superset but the
// grant is always normalized to these values.
func (a AccessLevel) NormalizedScopes(offline bool) []string {
	scopes := []string{ScopeRead}
	if a == AccessOperate {
		scopes = append(scopes, ScopeOperate)
	}
	if offline {
		scopes = append(scopes, ScopeOffline)
	}
	return scopes
}

// RequestsOperate reports whether the requested scope list asks for operate.
func RequestsOperate(scopes []string) bool {
	return slices.Contains(scopes, ScopeOperate)
}

// RequestsOffline reports whether the requested scope list asks for
// offline_access.
func RequestsOffline(scopes []string) bool {
	return slices.Contains(scopes, ScopeOffline)
}

// KnownScope reports whether a scope belongs to the coarse MCP scope set.
func KnownScope(scope string) bool {
	return scope == ScopeRead || scope == ScopeOperate || scope == ScopeOffline
}
