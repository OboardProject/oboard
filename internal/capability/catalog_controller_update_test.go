package capability

import (
	"encoding/json"
	"testing"
)

func TestControllerUpdateDownloadSchemas(t *testing.T) {
	catalog := NewCatalog()
	for _, name := range []string{"controller_update.status", "controller_update.check", "controller_update.install", "controller_update.cancel"} {
		item, ok := catalog.Get(name)
		if !ok || !item.MCPEnabled {
			t.Fatalf("missing capability: %s", name)
		}
		var schema map[string]any
		if err := json.Unmarshal(item.OutputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		props := schema["properties"].(map[string]any)
		if name != "controller_update.status" {
			props = props["controller_update"].(map[string]any)["properties"].(map[string]any)
		}
		download := props["download"].(map[string]any)["properties"].(map[string]any)
		for _, field := range []string{"target_build", "bytes", "total_bytes", "bytes_per_second", "attempt", "duration_ms", "last_progress_at", "complete"} {
			if download[field] == nil {
				t.Fatalf("%s missing %s", name, field)
			}
		}
		if download["url"] != nil || download["path"] != nil {
			t.Fatal("download schema exposes privileged location")
		}
	}
}
