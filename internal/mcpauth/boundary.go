package mcpauth

import (
	"slices"
	"strings"
)

// ResourceBoundaryVersion is the current versioned boundary format. Any change
// to the shape requires a version bump and a re-consent or a documented
// migration.
const ResourceBoundaryVersion = 1

// SelectionMode enumerates how a resource type is selected.
const (
	SelectionAll      = "all"
	SelectionSelected = "selected"
	SelectionNone     = "none"
)

// ResourceSelection describes access to one resource type. Selection never
// derives from OAuth scope prefixes.
type ResourceSelection struct {
	// Selection is all, selected, or none.
	Selection string `json:"selection"`
	// IDs are the explicitly selected object IDs. Empty for all/none.
	IDs []string `json:"ids,omitempty"`
	// IncludeFuture allows objects created after Consent for all mode.
	IncludeFuture bool `json:"include_future,omitempty"`
	// AllowCreate independently controls creation. It is never implied by
	// Selection == all.
	AllowCreate bool `json:"allow_create,omitempty"`
}

// ResourceBoundary is the explicit, versioned resource authorization structure
// persisted on each grant. Absent resource types default to denied.
type ResourceBoundary struct {
	Version int `json:"version"`
	// Resources maps resource type names (server, user, proxy_path, inbound,
	// deployment, subscription, audit_incident, changeset, workflow, ...) to
	// selections.
	Resources map[string]ResourceSelection `json:"resources,omitempty"`
	// GlobalCapabilities lists capability names that are global (no resource
	// refs) and therefore gated only by access level and RBAC.
	GlobalCapabilities []string `json:"global_capabilities,omitempty"`
	// DestructiveOperations is fixed to false in the current MCP surface.
	DestructiveOperations bool `json:"destructive_operations"`
}

// Normalized returns the boundary with stable key order and sorted IDs.
func (b ResourceBoundary) Normalized() ResourceBoundary {
	if b.Version == 0 {
		b.Version = ResourceBoundaryVersion
	}
	keys := make([]string, 0, len(b.Resources))
	for key := range b.Resources {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	resources := make(map[string]ResourceSelection, len(b.Resources))
	for _, key := range keys {
		sel := b.Resources[key]
		sel.Selection = strings.ToLower(strings.TrimSpace(sel.Selection))
		ids := slices.Clone(sel.IDs)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		sel.IDs = ids
		resources[key] = sel
	}
	b.Resources = resources
	slices.Sort(b.GlobalCapabilities)
	return b
}

// Valid reports whether the boundary shape is well formed.
func (b ResourceBoundary) Valid() bool {
	if b.Version == 0 {
		return false
	}
	for name, sel := range b.Resources {
		switch sel.Selection {
		case SelectionAll, SelectionNone:
		case SelectionSelected:
			if len(sel.IDs) == 0 {
				return false
			}
		default:
			return false
		}
		_ = name
	}
	return true
}

// Denied returns the resource references the boundary rejects. Unknown
// resource types default to denied.
func (b ResourceBoundary) Denied(refs []ResourceRef) []ResourceRef {
	denied := make([]ResourceRef, 0, len(refs))
	for _, ref := range refs {
		sel, ok := b.Resources[ref.Type]
		if !ok {
			denied = append(denied, ref)
			continue
		}
		switch sel.Selection {
		case SelectionAll:
			// all allows current and (when include_future) future objects.
		case SelectionSelected:
			if !slices.Contains(sel.IDs, ref.ID) {
				denied = append(denied, ref)
			}
		default:
			denied = append(denied, ref)
		}
	}
	return denied
}

// AllowsCreate reports whether the boundary permits creating objects of the
// given resource type.
func (b ResourceBoundary) AllowsCreate(resourceType string) bool {
	sel, ok := b.Resources[resourceType]
	return ok && sel.AllowCreate
}

// AllowsResource reports whether the boundary allows a single object.
func (b ResourceBoundary) AllowsResource(ref ResourceRef) bool {
	return len(b.Denied([]ResourceRef{ref})) == 0
}

// Selection returns the selection for a resource type, defaulting to none.
func (b ResourceBoundary) Selection(resourceType string) ResourceSelection {
	sel, ok := b.Resources[resourceType]
	if !ok {
		return ResourceSelection{Selection: SelectionNone}
	}
	return sel
}
