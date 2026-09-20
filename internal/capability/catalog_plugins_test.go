package capability

import (
	"slices"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func TestPluginGrantCapabilitiesAreAdminOnlyAndNotMCP(t *testing.T) {
	catalog := NewCatalog()
	grant, ok := catalog.Get("plugin_grants.create")
	if !ok || !grant.AdminOnly || grant.MCPEnabled {
		t.Fatalf("plugin grants must be admin-only and MCP-disabled: %#v", grant)
	}
	settings, ok := catalog.Get("plugin_runtime.settings.update")
	if !ok || !settings.AdminOnly || settings.MCPEnabled {
		t.Fatalf("runtime settings must be admin-only and MCP-disabled: %#v", settings)
	}
	operator := application.Principal{Role: model.RoleOperator, Scopes: []string{"*"}}
	if _, allowed := catalog.Authorize(operator, "plugin_grants.create"); allowed {
		t.Fatal("operator must not authorize plugin grants")
	}
	for _, name := range []string{"plugin_webhooks.list", "plugin_webhooks.create", "plugin_webhooks.update"} {
		descriptor, ok := catalog.Get(name)
		if !ok || !descriptor.AdminOnly || descriptor.MCPEnabled {
			t.Fatalf("%s must be admin-only and MCP-disabled: %#v", name, descriptor)
		}
		if _, allowed := catalog.Authorize(operator, name); allowed {
			t.Fatalf("operator must not manage %s", name)
		}
		if name != "plugin_webhooks.list" && !slices.Contains(descriptor.SensitiveOutput, "secret") {
			t.Fatalf("%s must classify its one-time secret", name)
		}
	}
	list, ok := catalog.Get("plugins.list")
	if !ok || !list.MCPEnabled || list.AdminOnly {
		t.Fatalf("plugin list must stay MCP-enabled for operators: %#v", list)
	}
	status, ok := catalog.Get("plugin_runtime.status")
	if !ok || !status.MCPEnabled || !strings.Contains(string(status.OutputSchema), "runtime_installed") || !strings.Contains(status.Description, "install_command") {
		t.Fatalf("plugin runtime status must advertise install state: %#v", status)
	}
}
