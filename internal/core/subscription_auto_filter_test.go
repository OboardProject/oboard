package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAutoResolutionFiltersByResolvedFormat(t *testing.T) {
	nodes := specialProtocolContractNodes()
	tests := []struct {
		ua     string
		want   []string
		deny   []string
	}{
		{ua: "mihomo/1.19.0", want: []string{"SSH Managed", "Snell v4", "Mieru Multiport"}, deny: []string{"Snell v6"}},
		{ua: "Surge iOS/5.8.0", want: []string{"SSH Managed", "Snell v4", "Snell v6"}, deny: []string{"Mieru Multiport"}},
		{ua: "Shadowrocket/2.2", want: []string{"SSH Managed", "Snell v4", "Snell v6", "Mieru Multiport"}},
	}
	for _, test := range tests {
		t.Run(test.ua, func(t *testing.T) {
			resolution := ResolveSubscriptionFormat(model.SubscriptionFormatAuto, test.ua)
			if !resolution.Auto || !IsConcreteSubscriptionFormat(resolution.Resolved) {
				t.Fatalf("resolution = %#v", resolution)
			}
			preview, err := PreviewSubscriptionNodes(nodes, resolution.Resolved)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, node := range preview.Nodes {
				got[node.Name] = true
			}
			for _, name := range test.want {
				if !got[name] {
					t.Fatalf("resolved %s missing %s: %#v", resolution.Resolved, name, got)
				}
			}
			for _, name := range test.deny {
				if got[name] {
					t.Fatalf("resolved %s unexpectedly kept %s", resolution.Resolved, name)
				}
			}
		})
	}
}
