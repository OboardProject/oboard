package capability

import (
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func TestScriptGrantCapabilitiesAreAdminOnlyAndNotMCP(t *testing.T) {
	catalog := NewCatalog()
	grant, ok := catalog.Get("script_grants.create")
	if !ok || !grant.AdminOnly || grant.MCPEnabled {
		t.Fatalf("script grants must be admin-only and MCP-disabled: %#v", grant)
	}
	settings, ok := catalog.Get("script_runtime.settings.update")
	if !ok || !settings.AdminOnly || settings.MCPEnabled {
		t.Fatalf("runtime settings must be admin-only and MCP-disabled: %#v", settings)
	}
	operator := application.Principal{Role: model.RoleOperator, Scopes: []string{"*"}}
	if _, allowed := catalog.Authorize(operator, "script_grants.create"); allowed {
		t.Fatal("operator must not authorize script grants")
	}
	list, ok := catalog.Get("scripts.list")
	if !ok || !list.MCPEnabled || list.AdminOnly {
		t.Fatalf("script list must stay MCP-enabled for operators: %#v", list)
	}
}
