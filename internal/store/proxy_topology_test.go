package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestProxyTopologyDataAndNameResetStayNarrow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "entry-server"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{
		InboundID:    inbound.ID,
		NameMode:     model.ProxyPathNameCustom,
		NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameLiteral, Value: "custom"}},
		Secret:       "keep-secret",
		Enabled:      true,
	}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}

	topology, err := db.ProxyTopologyData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Servers) != 1 || len(topology.Inbounds) != 1 || len(topology.ProxyPaths) != 1 {
		t.Fatalf("topology snapshot = %#v", topology)
	}
	if err := db.ResetProxyPathNameTemplate(ctx, path.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetProxyPath(ctx, path.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NameMode != model.ProxyPathNameAuto || len(stored.NameTemplate) != 0 {
		t.Fatalf("name reset = mode %q template %#v", stored.NameMode, stored.NameTemplate)
	}
	if stored.InboundID != inbound.ID || stored.Secret != "keep-secret" || !stored.Enabled {
		t.Fatalf("name reset changed unrelated fields: %#v", stored)
	}
}

func BenchmarkProxyTopologyData(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "entry-server"}
	if err := db.CreateServer(ctx, server); err != nil {
		b.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		b.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Secret: "benchmark-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		b.Fatal(err)
	}

	b.Run("proxy_topology", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := db.ProxyTopologyData(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("full_routing", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := db.FullRoutingConfigData(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
}
