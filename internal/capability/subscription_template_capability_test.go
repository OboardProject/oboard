package capability

import (
	"encoding/json"
	"testing"
)

func TestSubscriptionTemplateCapabilitiesAreManagementOnly(t *testing.T) {
	catalog := NewCatalog()
	reads := []string{"subscription_templates.list", "subscription_templates.get", "subscription_templates.validate", "subscription_templates.preview"}
	writes := []string{"subscription_templates.update", "subscription_templates.reset"}
	for _, name := range append(append([]string{}, reads...), writes...) {
		descriptor, ok := catalog.Get(name)
		if !ok || !descriptor.MCPEnabled {
			t.Fatalf("%s is not exposed through MCP: %#v", name, descriptor)
		}
		if descriptor.RBACPermission != "admin.settings" {
			t.Fatalf("%s is not management-only: %#v", name, descriptor)
		}
	}
	for _, name := range writes {
		descriptor, _ := catalog.Get(name)
		if !descriptor.Executable || descriptor.ReadOnly {
			t.Fatalf("%s must be an executable write capability: %#v", name, descriptor)
		}
	}
	update, ok := catalog.Get("subscription_templates.update")
	if !ok {
		t.Fatal("subscription_templates.update missing")
	}
	raw := string(update.InputSchema)
	if !jsonStringContains(raw, `"expected_revision"`) || !jsonStringContains(raw, `"content"`) {
		t.Fatalf("update schema lacks optimistic locking: %s", raw)
	}
	var input map[string]any
	if err := json.Unmarshal(update.InputSchema, &input); err != nil {
		t.Fatal(err)
	}
}
