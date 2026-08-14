package capability

import (
	"context"
	"testing"
)

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
				"server_id": 1, "proxy_path_id": 2, "rule_set_id": 3,
			}},
			want: map[string]bool{"server:1": true, "proxy_path:2": true, "routing_rule_set:3": true},
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
