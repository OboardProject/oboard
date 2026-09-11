package store

import (
	"context"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"path/filepath"
	"testing"
)

func TestSnellRuntimeUpgradePreservesPortsAndConfirmsAtomically(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := model.Server{Name: "snell-upgrade", Status: model.ServerOnline}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{ServerID: server.ID, Name: "snell", Protocol: model.ProtocolSnell, Port: 6160, ListenIP: "0.0.0.0", ConfigJSON: `{"version":4,"psk":"old-server-seed"}`, Enabled: true}
	if err = s.CreateInbound(ctx, &in); err != nil {
		t.Fatal(err)
	}
	old := model.ProxyPathPortAllocation{Kind: model.ProxyPathPortKindSnellUser, ScopeKey: fmt.Sprintf("inbound:%d:user:7:path:0", in.ID), ServerID: server.ID, Port: 40001, State: model.PortAllocationStateActive, Generation: 1}
	if err = s.SaveProxyPathPortAllocations(ctx, []model.ProxyPathPortAllocation{old}, nil); err != nil {
		t.Fatal(err)
	}
	// Actual previous schema has no endpoint confirmation tables.
	for _, table := range []string{"snell_listener_runtime", "snell_runtime_confirmations"} {
		if _, err = s.db.ExecContext(ctx, "drop table "+table); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unchanged, err := s.GetInbound(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ConfigJSON != in.ConfigJSON || unchanged.Port != in.Port || unchanged.SnellActiveMode != "" {
		t.Fatal("upgrade changed existing desired/active endpoints")
	}
	allocations, err := s.ListProxyPathPortAllocations(ctx)
	if err != nil || len(allocations) != 1 || allocations[0].Port != 40001 {
		t.Fatalf("old port lost: %+v %v", allocations, err)
	}
	in.ConfigJSON = `{"version":4,"psk":"old-server-seed","listener_mode":"shared_port"}`
	if err = s.UpdateInbound(ctx, &in); err != nil {
		t.Fatal(err)
	}
	allocations, err = s.ListProxyPathPortAllocations(ctx)
	if err != nil || len(allocations) != 1 {
		t.Fatal("save released an unconfirmed old listener")
	}
	before, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listener := map[string]map[string]any{fmt.Sprintf("in-%d", in.ID): {"auth_mode": "multi_psk", "listen_port": 6160, "version": 5}}
	if err = s.ConfirmSnellRuntime(ctx, server.ID, 10, listener); err != nil {
		t.Fatal(err)
	}
	active, err := s.GetInbound(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.SnellActiveMode != "shared_port" || active.SnellActivePort != 6160 || active.SnellActiveVersion != 4 {
		t.Fatalf("wrong active endpoint: %+v", active)
	}
	allocations, err = s.ListProxyPathPortAllocations(ctx)
	if err != nil || len(allocations) != 0 {
		t.Fatal("confirmed stopped port not released")
	}
	after, err := s.ConfigurationRevision(ctx)
	if err != nil || after != before {
		t.Fatalf("runtime confirmation queued another deployment %d -> %d: %v", before, after, err)
	}
	if err = s.ConfirmSnellRuntime(ctx, server.ID, 9, nil); err != nil {
		t.Fatal(err)
	}
	active, _ = s.GetInbound(ctx, in.ID)
	if active.SnellActivePort != 6160 {
		t.Fatal("old result replaced endpoint")
	}
	if err = s.ConfirmSnellRuntime(ctx, server.ID, 10, nil); err == nil {
		t.Fatal("same-version different result accepted")
	}
	if err = s.ConfirmSnellRuntime(ctx, server.ID, 10, listener); err != nil {
		t.Fatal("idempotent confirmation rejected")
	}
}
