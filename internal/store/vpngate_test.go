package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestVPNGatePoolLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vpngate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pool := model.VPNGatePool{Name: "日本家宽", Countries: []string{"jp"}, OperatorASNs: []string{"4713"}, Enabled: true}
	if err := s.CreateVPNGatePool(ctx, &pool); err != nil {
		t.Fatal(err)
	}
	duplicate := model.VPNGatePool{Name: "日本家宽"}
	if err := s.CreateVPNGatePool(ctx, &duplicate); err == nil {
		t.Fatal("duplicate pool name must be rejected")
	}
	got, err := s.GetVPNGatePool(ctx, pool.ID)
	if err != nil || got.Countries[0] != "JP" || got.OperatorASNs[0] != "AS4713" || got.PoolSize != core.VPNGateDefaultPoolSize {
		t.Fatalf("stored pool %+v %v", got, err)
	}
	got.SelectMode = model.VPNGateSelectQuality
	if err := s.UpdateVPNGatePool(ctx, got); err != nil {
		t.Fatal(err)
	}

	server := &model.Server{Name: "exit", AgentID: "agent", AgentTokenHash: "hash", ChainSecret: "secret", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	state := &model.VPNGatePoolServer{PoolID: pool.ID, ServerID: server.ID, Revision: 3, ContentDigest: "d"}
	for i := 0; i < 30; i++ {
		state.History = append(state.History, model.VPNGateSwitchEvent{At: time.Now(), Reason: "x"})
	}
	if err := s.SaveVPNGatePoolServer(ctx, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetVPNGatePoolServer(ctx, pool.ID, server.ID)
	if err != nil || loaded.Revision != 3 || len(loaded.History) != vpngateHistoryLimit {
		t.Fatalf("pool server state %+v %v", loaded, err)
	}

	if err := s.DeleteVPNGatePool(ctx, pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVPNGatePoolServer(ctx, pool.ID, server.ID); err == nil {
		t.Fatal("pool server state must cascade with the pool")
	}
}

func TestVPNGateCatalogAccumulatesAndExpires(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Now().Add(-48 * time.Hour)
	entry := func(ip string) core.VPNGateCatalogEntry {
		return core.VPNGateCatalogEntry{IP: ip, Port: 443, Network: "tcp", CountryCode: "JP", Spec: model.OpenVPNSpec{Server: ip, ServerPort: 443, Network: "tcp"}}
	}
	if _, err := s.UpsertVPNGateCatalog(ctx, []core.VPNGateCatalogEntry{entry("219.100.37.1"), entry("219.100.37.2")}, 0, "test", base); err != nil {
		t.Fatal(err)
	}
	total, err := s.UpsertVPNGateCatalog(ctx, []core.VPNGateCatalogEntry{entry("219.100.37.2"), entry("219.100.37.3")}, 1, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("stale entry must expire after a day, total=%d", total)
	}
	nodes, err := s.ListVPNGateNodes(ctx)
	if err != nil || len(nodes) != 2 || nodes[0].Spec.Server == "" {
		t.Fatalf("nodes %+v %v", nodes, err)
	}
	state, err := s.GetVPNGateCatalogState(ctx)
	if err != nil || state.Entries != 2 || state.Skipped != 1 || state.Total != 2 {
		t.Fatalf("catalog state %+v %v", state, err)
	}
	if err := s.DeleteVPNGatePool(ctx, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting a missing pool: %v", err)
	}
}
