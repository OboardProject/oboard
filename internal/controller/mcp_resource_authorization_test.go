package controller

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func mcpGrantContext(ctx context.Context, role model.Role, level mcpauth.AccessLevel) (context.Context, application.Principal) {
	boundary := mcpauth.ResourceBoundary{Version: mcpauth.ResourceBoundaryVersion}
	grant := mcpauth.GrantPolicy{GrantID: "grant-" + string(role), AccessLevel: level, ResourceBoundary: boundary, IssuedAt: time.Now().UTC()}
	principal := application.Principal{
		ID:             grant.GrantID,
		Role:           role,
		AccessLevel:    level,
		GrantPolicy:    &grant,
		ResourceFilter: application.ResourceFilterFromBoundary(boundary),
		SourceIP:       netip.MustParseAddr("127.0.0.1"),
	}
	ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grant, Role: role})
	return ctx, principal
}

// TestMCPCapabilityResourceReadRequiresGrantAuthorization covers a viewer
// escalation through resources/read. The singleton MCP server registers every
// read-only capability resource under a synthetic admin principal and the
// per-principal filter middleware only trims list results, so reading
// oboard://capability/<name> was authorized by nothing but the existence of a
// grant. node_presets.list carries RBACPermission admin.settings, which
// ManagementOnly denies to viewers.
func TestMCPCapabilityResourceReadRequiresGrantAuthorization(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")

	for _, capabilityName := range []string{"node_presets.list", "node_incidents.list"} {
		def := mcpResourceDef{uri: "oboard://capability/" + capabilityName, capability: capabilityName, kind: "query_capability"}
		viewerCtx, viewer := mcpGrantContext(context.Background(), model.RoleViewer, mcpauth.AccessRead)
		if _, err := app.readMCPResource(viewerCtx, viewer, def, def.uri); err == nil {
			t.Fatalf("viewer read management capability resource %s", capabilityName)
		}
		adminCtx, admin := mcpGrantContext(context.Background(), model.RoleAdmin, mcpauth.AccessRead)
		if _, err := app.readMCPResource(adminCtx, admin, def, def.uri); err != nil {
			t.Fatalf("admin denied capability resource %s: %v", capabilityName, err)
		}
	}
}

// TestMCPCapabilityResourceReadKeepsViewerReadableCapabilities guards the fix
// against over-blocking: a read-only capability that is not management-only
// stays readable by a viewer grant.
func TestMCPCapabilityResourceReadKeepsViewerReadableCapabilities(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	descriptor, known := app.capabilities.Get("servers.list")
	if !known || descriptor.ManagementOnly() {
		t.Fatal("servers.list must be a viewer-readable capability for this test")
	}
	def := mcpResourceDef{uri: "oboard://capability/servers.list", capability: "servers.list", kind: "query_capability"}
	ctx, viewer := mcpGrantContext(context.Background(), model.RoleViewer, mcpauth.AccessRead)
	if _, err := app.readMCPResource(ctx, viewer, def, def.uri); err != nil {
		t.Fatalf("viewer denied a read-only capability resource: %v", err)
	}
}

// TestMCPCapabilityResourceReadRejectsRevokedGrant keeps the read path fail
// closed for a grant that is no longer valid.
func TestMCPCapabilityResourceReadRejectsRevokedGrant(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	def := mcpResourceDef{uri: "oboard://capability/servers.list", capability: "servers.list", kind: "query_capability"}

	revokedAt := time.Now().UTC().Add(-time.Minute)
	boundary := mcpauth.ResourceBoundary{Version: mcpauth.ResourceBoundaryVersion}
	grant := mcpauth.GrantPolicy{GrantID: "grant-revoked", AccessLevel: mcpauth.AccessRead, ResourceBoundary: boundary, IssuedAt: time.Now().UTC(), RevokedAt: &revokedAt}
	principal := application.Principal{ID: grant.GrantID, Role: model.RoleAdmin, AccessLevel: mcpauth.AccessRead, GrantPolicy: &grant, ResourceFilter: application.ResourceFilterFromBoundary(boundary), SourceIP: netip.MustParseAddr("127.0.0.1")}
	ctx := context.WithValue(context.Background(), mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grant, Role: model.RoleAdmin})
	if _, err := app.readMCPResource(ctx, principal, def, def.uri); err == nil {
		t.Fatal("revoked grant read a capability resource")
	}
}
