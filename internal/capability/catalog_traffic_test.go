package capability

import (
	"context"
	"strings"
	"testing"
)

func TestRoutingCapabilitySchemasSupportSourcePrefixTargetPathsAndSync(t *testing.T) {
	catalog := NewCatalog()
	for _, name := range []string{"routing_rules.create", "routing_rules.update"} {
		descriptor, ok := catalog.Get(name)
		if !ok {
			t.Fatalf("capability %s is missing", name)
		}
		raw := string(descriptor.InputSchema)
		if !strings.Contains(raw, `"source_prefix"`) || !strings.Contains(raw, `"source_prefix"]`) || !strings.Contains(raw, `"target_proxy_path_id"`) || !strings.Contains(raw, `"proxy_path"`) {
			t.Fatalf("%s input schema lacks routing target or synchronization fields: %s", name, raw)
		}
		if name == "routing_rules.create" && (!strings.Contains(raw, `"sync_source_rule_id"`) || !strings.Contains(raw, `"sync_enabled"`)) {
			t.Fatalf("%s input schema lacks synchronization fields: %s", name, raw)
		}
		if name == "routing_rules.update" && (strings.Contains(raw, `"sync_source_rule_id"`) || strings.Contains(raw, `"sync_enabled"`)) {
			t.Fatalf("%s input schema exposes create-only synchronization fields: %s", name, raw)
		}
	}
	descriptor, ok := catalog.Get("routing_rules.list")
	if !ok || !strings.Contains(string(descriptor.OutputSchema), `"source_prefix"`) || !strings.Contains(string(descriptor.OutputSchema), `"target_proxy_path_id"`) || !strings.Contains(string(descriptor.OutputSchema), `"sync_group_id"`) {
		t.Fatalf("routing_rules.list output schema lacks current routing fields")
	}
}

func TestRoutingCapabilityResourceResolvers(t *testing.T) {
	catalog := NewCatalog()
	tests := []struct {
		name  string
		input any
		want  map[string]bool
	}{
		{
			name: "routing_rules.create",
			input: map[string]any{"routing_rule": map[string]any{
				"server_id": 1, "proxy_path_id": 2, "rule_set_id": 3, "target_proxy_path_id": 4, "sync_source_rule_id": 9,
			}},
			want: map[string]bool{"server:1": true, "proxy_path:2": true, "proxy_path:4": true, "routing_rule_set:3": true, "routing_rule:9": true},
		},
		{
			name: "routing_rules.place",
			input: map[string]any{"proxy_path_id": 5, "placements": []any{
				map[string]any{"rule_id": 7, "sort_position": 0},
				map[string]any{"rule_id": 8, "sort_position": 1},
			}},
			want: map[string]bool{"proxy_path:5": true, "routing_rule:7": true, "routing_rule:8": true},
		},
		{
			name:  "routing_rule_sets.refresh",
			input: map[string]any{"routing_rule_set_id": 11},
			want:  map[string]bool{"routing_rule_set:11": true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor, ok := catalog.Get(test.name)
			if !ok || descriptor.ResolveResourceRefs == nil {
				t.Fatalf("capability %s has no resource resolver", test.name)
			}
			refs, err := descriptor.ResolveResourceRefs(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, ref := range refs {
				got[ref.Type+":"+ref.ID] = true
			}
			if len(got) != len(test.want) {
				t.Fatalf("resource refs = %#v, want %#v", got, test.want)
			}
			for ref := range test.want {
				if !got[ref] {
					t.Fatalf("resource refs = %#v, missing %s", got, ref)
				}
			}
		})
	}
}
