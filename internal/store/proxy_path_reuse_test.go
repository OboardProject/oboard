package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestApplyProxyPathReuseChecksRevisionAndRollsBackWholeExpansion(t *testing.T) {
	newFixture := func(t *testing.T) (*Store, model.Inbound, model.Server) {
		t.Helper()
		db, err := Open(filepath.Join(t.TempDir(), "proxy-path-reuse.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		ctx := context.Background()
		entry := model.Server{Name: "entry", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30999, Status: model.ServerOnline}
		target := model.Server{Name: "target", ListenIP: "0.0.0.0", PortRangeStart: 31000, PortRangeEnd: 31999, Status: model.ServerOnline}
		if err := db.CreateServer(ctx, &entry); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateServer(ctx, &target); err != nil {
			t.Fatal(err)
		}
		inbound := model.Inbound{ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
		if err := db.CreateInbound(ctx, &inbound); err != nil {
			t.Fatal(err)
		}
		return db, inbound, target
	}

	t.Run("revision conflict", func(t *testing.T) {
		db, inbound, _ := newFixture(t)
		write := ProxyPathReuseWrite{Path: model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "secret", Enabled: true}}
		err := db.ApplyProxyPathReuse(context.Background(), "stale-revision", []ProxyPathReuseWrite{write})
		if !errors.Is(err, ErrRoutingTopologyChanged) {
			t.Fatalf("revision error = %v", err)
		}
		paths, err := db.ListProxyPaths(context.Background())
		if err != nil || len(paths) != 0 {
			t.Fatalf("revision conflict persisted paths=%#v err=%v", paths, err)
		}
	})

	t.Run("write failure", func(t *testing.T) {
		db, inbound, target := newFixture(t)
		revision, err := db.RoutingTopologyRevision(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		targetID := target.ID
		write := ProxyPathReuseWrite{
			Path: model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "secret", Enabled: true},
			Steps: []model.ProxyPathStep{
				{Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &targetID, ConfigJSON: `{}`},
				{Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &targetID, ConfigJSON: `{}`},
			},
		}
		if err := db.ApplyProxyPathReuse(context.Background(), revision, []ProxyPathReuseWrite{write}); err == nil {
			t.Fatal("duplicate step positions unexpectedly committed")
		}
		paths, err := db.ListProxyPaths(context.Background())
		if err != nil || len(paths) != 0 {
			t.Fatalf("failed expansion persisted paths=%#v err=%v", paths, err)
		}
		steps, err := db.ListProxyPathSteps(context.Background())
		if err != nil || len(steps) != 0 {
			t.Fatalf("failed expansion persisted steps=%#v err=%v", steps, err)
		}
	})
}
