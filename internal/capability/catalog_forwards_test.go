package capability

import (
	"encoding/json"
	"testing"
)

func TestPortForwardCapabilityAllowsNullableManagedTarget(t *testing.T) {
	descriptor, ok := NewCatalog().Get("port_forwards.create")
	if !ok {
		t.Fatal("port_forwards.create capability missing")
	}
	var input map[string]any
	if err := json.Unmarshal(descriptor.InputSchema, &input); err != nil {
		t.Fatal(err)
	}
	properties := input["properties"].(map[string]any)
	forward := properties["port_forward"].(map[string]any)
	fields := forward["properties"].(map[string]any)
	target := fields["target_server_id"].(map[string]any)
	types, ok := target["type"].([]any)
	if !ok || len(types) != 2 || types[0] != "integer" || types[1] != "null" {
		t.Fatalf("target_server_id schema = %#v, want integer or null", target)
	}
}
