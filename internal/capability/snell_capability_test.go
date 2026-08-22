package capability

import (
	"encoding/json"
	"testing"
)

func TestSnellProfileCapabilitiesAreAdminOnlyAndExecutable(t *testing.T) {
	catalog := NewCatalog()
	for _, name := range []string{"snell_profiles.list", "snell_profiles.create", "snell_profiles.update", "snell_profiles.delete"} {
		descriptor, ok := catalog.Get(name)
		if !ok || !descriptor.MCPEnabled {
			t.Fatalf("%s is not exposed through MCP: %#v", name, descriptor)
		}
		if descriptor.RBACPermission != "admin.settings" {
			t.Fatalf("%s is not admin-only: %#v", name, descriptor)
		}
		if name != "snell_profiles.list" && (!descriptor.Executable || descriptor.ReadOnly) {
			t.Fatalf("%s must be an executable write capability: %#v", name, descriptor)
		}
	}
	create, ok := catalog.Get("snell_profiles.create")
	if !ok {
		t.Fatal("snell_profiles.create missing")
	}
	var input map[string]any
	if err := json.Unmarshal(create.InputSchema, &input); err != nil {
		t.Fatal(err)
	}
	raw := string(create.InputSchema)
	if !jsonStringContains(raw, `"version"`) || !jsonStringContains(raw, `"psk"`) || !jsonStringContains(raw, `"obfs_mode"`) || !jsonStringContains(raw, `"mode"`) {
		t.Fatalf("snell_profiles.create schema lacks Snell parameters: %s", raw)
	}
	_ = input
}

func jsonStringContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}