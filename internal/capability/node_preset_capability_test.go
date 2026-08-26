package capability

import (
	"encoding/json"
	"testing"
)

func TestNodePresetCapabilitiesAreAdminOnlyAndExecutable(t *testing.T) {
	catalog := NewCatalog()
	for _, name := range []string{"node_presets.list", "node_presets.create", "node_presets.update", "node_presets.delete"} {
		descriptor, ok := catalog.Get(name)
		if !ok || !descriptor.MCPEnabled {
			t.Fatalf("%s is not exposed through MCP: %#v", name, descriptor)
		}
		if descriptor.RBACPermission != "admin.settings" {
			t.Fatalf("%s is not admin-only: %#v", name, descriptor)
		}
		if name != "node_presets.list" && (!descriptor.Executable || descriptor.ReadOnly) {
			t.Fatalf("%s must be an executable write capability: %#v", name, descriptor)
		}
	}
	create, ok := catalog.Get("node_presets.create")
	if !ok {
		t.Fatal("node_presets.create missing")
	}
	raw := string(create.InputSchema)
	if !jsonStringContains(raw, `"kind"`) || !jsonStringContains(raw, `"vless-reality"`) || !jsonStringContains(raw, `"hy2-salamander"`) || !jsonStringContains(raw, `"anytls-large-padding"`) || !jsonStringContains(raw, `"config_json"`) {
		t.Fatalf("node_presets.create schema lacks template fields: %s", raw)
	}
	var input map[string]any
	if err := json.Unmarshal(create.InputSchema, &input); err != nil {
		t.Fatal(err)
	}
}
