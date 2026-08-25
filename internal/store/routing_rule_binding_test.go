package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestProxyPathRoutingRuleBindingRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	server := &model.Server{Name: "edge", ChainSecret: "secret", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		interfaceName string
		sourcePrefix  string
		wantInterface string
		wantPrefix    string
	}{
		{name: "interface", interfaceName: "he-ipv6", wantInterface: "he-ipv6"},
		{name: "source prefix", sourcePrefix: "2001:db8:100::/64", wantPrefix: "2001:db8:100::/64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rule := &model.RoutingRule{
				ServerID: server.ID, Scope: model.RoutingRuleScopePathStage, MatchSource: model.RoutingMatchSourceInline,
				Name: test.name, MatchJSON: `{}`, Action: model.RouteActionProxyPath, InterfaceName: test.interfaceName, SourcePrefix: test.sourcePrefix, Enabled: true,
			}
			if err := db.CreateRoutingRule(ctx, rule); err != nil {
				t.Fatal(err)
			}
			stored, err := db.GetRoutingRule(ctx, rule.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.InterfaceName != test.wantInterface || stored.SourcePrefix != test.wantPrefix {
				t.Fatalf("stored binding = interface %q prefix %q, want interface %q prefix %q", stored.InterfaceName, stored.SourcePrefix, test.wantInterface, test.wantPrefix)
			}
		})
	}
}
