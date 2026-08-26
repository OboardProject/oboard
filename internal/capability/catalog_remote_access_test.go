package capability

import (
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

func TestPrivilegedCapabilitiesAreNotDefaultGrantable(t *testing.T) {
	catalog := NewCatalog()
	principal := application.Principal{AccessLevel: mcpauth.AccessOperate, Role: model.RoleAdmin, Scopes: []string{"*"}}
	for _, name := range []string{"node.exec", "node.exec_shell", "node.system_info"} {
		item, ok := catalog.Get(name)
		if !ok || !item.MCPEnabled {
			t.Fatalf("%s missing from catalog", name)
		}
		if item.DefaultGrantable() {
			t.Fatalf("%s must not be default-grantable", name)
		}
		if item.PrivilegeClass == "" {
			t.Fatalf("%s must have a privilege class", name)
		}
	}
	for _, item := range catalog.ListMCP(principal) {
		if item.PrivilegeClass != "" {
			t.Fatalf("operate admin ListMCP leaked privileged capability %s", item.Name)
		}
	}
	principal.PrivilegedClasses = []string{model.PrivilegeRemoteExec}
	found := false
	for _, item := range catalog.ListMCP(principal) {
		if item.Name == "node.exec" {
			found = true
		}
		if item.Name == "node.exec_shell" {
			t.Fatal("structured exec grant must not expose raw shell")
		}
	}
	if !found {
		t.Fatal("privileged grant should expose node.exec")
	}
}

func TestScopesForGrantOmitPrivilegedCapabilities(t *testing.T) {
	catalog := NewCatalog()
	principal := application.Principal{AccessLevel: mcpauth.AccessOperate, Role: model.RoleAdmin, Scopes: []string{"*"}}
	scopes := catalog.ScopesForGrant(principal)
	for _, scope := range scopes {
		if scope == "node.exec" || scope == "node.exec_shell" {
			t.Fatalf("derived OAuth scopes leaked privileged capability %s", scope)
		}
	}
	item, _ := catalog.Get("node.exec")
	if item.DefaultGrantable() {
		t.Fatal("node.exec must not be default-grantable")
	}
}
