package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// legacyProxyPathPortAllocationSchema is the table as released before the
// generation-aware ledger: no pool/listen/network/lifecycle metadata and a
// three-field unique key that cannot hold two generations of one owner.
const legacyProxyPathPortAllocationSchema = `create table proxy_path_port_allocations (id integer primary key autoincrement, kind text not null, scope_key text not null, server_id integer not null references servers(id) on delete cascade, port integer not null, created_at text not null, updated_at text not null, unique(kind,scope_key,server_id))`

func TestProxyPathPortAllocationsUpgradeRebuildsLegacyUniqueKey(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "upgrade-node", AgentID: "upgrade-agent", AgentTokenHash: "upgrade-token", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`drop index if exists idx_proxy_path_port_allocations_server`,
		`drop table proxy_path_port_allocations`,
		legacyProxyPathPortAllocationSchema,
		`insert into proxy_path_port_allocations(kind,scope_key,server_id,port,created_at,updated_at) values('chain_service','2022-blake3-aes-128-gcm',1,41001,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`insert into proxy_path_port_allocations(kind,scope_key,server_id,port,created_at,updated_at) values('internal_inbound','7:2',1,41002,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`insert into proxy_path_port_allocations(kind,scope_key,server_id,port,created_at,updated_at) values('trusted_forward_inner','7:3',1,42001,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`insert into proxy_path_port_allocations(kind,scope_key,server_id,port,created_at,updated_at) values('tunnel_ssh_loopback','555',1,42002,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`insert into proxy_path_port_allocations(kind,scope_key,server_id,port,created_at,updated_at) values('tunnel_wireguard','556',1,41003,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("%q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("open with legacy allocation schema: %v", err)
	}
	defer s.Close()

	legacy, err := s.proxyPathPortAllocationLegacyKeyPresent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("legacy three-field unique key still present after upgrade")
	}
	generationKey, err := s.proxyPathPortAllocationGenerationKeyPresent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !generationKey {
		t.Fatal("generation unique key missing after upgrade")
	}

	allocations, err := s.ListProxyPathPortAllocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 5 {
		t.Fatalf("upgrade changed allocation count: got %d want 5", len(allocations))
	}
	byKind := map[string]model.ProxyPathPortAllocation{}
	for _, item := range allocations {
		byKind[item.Kind] = item
	}
	if got := byKind["chain_service"]; got.Pool != model.PortPoolPublic || got.ListenIP != "" || got.Network != "tcp_udp" || got.Port != 41001 {
		t.Fatalf("chain_service backfill = %#v", got)
	}
	if got := byKind["internal_inbound"]; got.Pool != model.PortPoolPublic || got.Network != "tcp_udp" || got.Port != 41002 {
		t.Fatalf("internal_inbound backfill = %#v", got)
	}
	if got := byKind["trusted_forward_inner"]; got.Pool != model.PortPoolInternal || got.ListenIP != "127.0.0.1" || got.Network != "tcp_udp" || got.Port != 42001 {
		t.Fatalf("trusted_forward_inner backfill = %#v", got)
	}
	if got := byKind["tunnel_ssh_loopback"]; got.Pool != model.PortPoolInternal || got.ListenIP != "127.0.0.1" || got.Network != "tcp" || got.Port != 42002 {
		t.Fatalf("tunnel_ssh_loopback backfill = %#v", got)
	}
	if got := byKind["tunnel_wireguard"]; got.Pool != model.PortPoolPublic || got.Network != "udp" || got.Port != 41003 {
		t.Fatalf("tunnel_wireguard backfill = %#v", got)
	}
	for _, item := range allocations {
		if item.Generation != 1 || item.Ordinal != 0 || item.State != model.PortAllocationStateActive {
			t.Fatalf("lifecycle backfill = %#v", item)
		}
	}
}

func TestProxyPathPortAllocationsUpgradeHoldsTwoGenerations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "dual-gen-node", AgentID: "dual-gen-agent", AgentTokenHash: "dual-gen-token", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`drop index if exists idx_proxy_path_port_allocations_server`,
		`drop table proxy_path_port_allocations`,
		legacyProxyPathPortAllocationSchema,
		`insert into proxy_path_port_allocations(kind,scope_key,server_id,port,created_at,updated_at) values('chain_service','2022-blake3-aes-128-gcm',1,41001,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("%q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// The upgraded database must hold two generations of the same owner: the
	// active row and a preparing row. Under the old three-field key the second
	// insert would overwrite the first.
	added := []model.ProxyPathPortAllocation{
		{Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: server.ID, Pool: model.PortPoolPublic, Network: "tcp_udp", Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing, PolicyRevision: 2},
	}
	if err := s.SaveProxyPathPortAllocations(ctx, added, nil); err != nil {
		t.Fatalf("save preparing generation: %v", err)
	}
	allocations, err := s.ListProxyPathPortAllocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 2 {
		t.Fatalf("generation count = %d, want 2", len(allocations))
	}
	var active, preparing *model.ProxyPathPortAllocation
	for index := range allocations {
		if allocations[index].Generation == 1 {
			active = &allocations[index]
		}
		if allocations[index].Generation == 2 {
			preparing = &allocations[index]
		}
	}
	if active == nil || active.Port != 41001 || active.State != model.PortAllocationStateActive {
		t.Fatalf("generation 1 row = %#v", active)
	}
	if preparing == nil || preparing.Port != 43001 || preparing.State != model.PortAllocationStatePreparing || preparing.PolicyRevision != 2 {
		t.Fatalf("generation 2 row = %#v", preparing)
	}

	// The same save is idempotent: replaying the preparing row must not create a
	// third generation or move the active row.
	if err := s.SaveProxyPathPortAllocations(ctx, added, nil); err != nil {
		t.Fatalf("replay save: %v", err)
	}
	allocations, err = s.ListProxyPathPortAllocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 2 {
		t.Fatalf("replay changed generation count to %d", len(allocations))
	}

	// Controller restart must read both generations back intact.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	allocations, err = s.ListProxyPathPortAllocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 2 {
		t.Fatalf("restart changed generation count to %d", len(allocations))
	}

	// A repeat migration is a no-op and must not rebuild or truncate rows.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	legacy, err := s.proxyPathPortAllocationLegacyKeyPresent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("repeat migration restored the legacy unique key")
	}
	allocations, err = s.ListProxyPathPortAllocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 2 {
		t.Fatalf("repeat migration changed generation count to %d", len(allocations))
	}
}
