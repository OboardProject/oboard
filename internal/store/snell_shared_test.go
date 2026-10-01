package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestSnellSharedUpgradeFromPreviousListenerModes(t *testing.T) {
	for _, mode := range []string{"", "per_identity_port", "shared_port"} {
		t.Run(mode, func(t *testing.T) {
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
			cfg := map[string]any{"version": 4, "psk": "previous-server-seed", "obfs_mode": "http", "snell_profile_id": 1}
			if mode != "" {
				cfg["listener_mode"] = mode
			}
			raw, _ := json.Marshal(cfg)
			in := model.Inbound{ServerID: server.ID, Name: "snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, AdvertisePort: 443, ConfigJSON: string(raw), Enabled: true}
			if err = s.CreateInbound(ctx, &in); err != nil {
				t.Fatal(err)
			}
			old := model.ProxyPathPortAllocation{Kind: model.ProxyPathPortKindSnellUser, ScopeKey: fmt.Sprintf("inbound:%d:user:7:path:0", in.ID), ServerID: server.ID, Port: 40001, State: model.PortAllocationStateActive, Generation: 1}
			if err = s.SaveProxyPathPortAllocations(ctx, []model.ProxyPathPortAllocation{old}, nil); err != nil {
				t.Fatal(err)
			}
			// These are the actual pre-upgrade configuration and confirmed endpoint shapes.
			if _, err = s.db.ExecContext(ctx, `insert into snell_listener_runtime(inbound_id,server_id,listener_mode,port,protocol_version,config_version,updated_at) values(?,?,'per_identity_port',0,4,9,?)`, in.ID, server.ID, now()); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.ExecContext(ctx, `insert into snell_runtime_confirmations(server_id,config_version,digest) values(?,9,'previous')`, server.ID); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			converted, err := s.GetInbound(ctx, in.ID)
			if err != nil {
				t.Fatal(err)
			}
			var next map[string]any
			if err = json.Unmarshal([]byte(converted.ConfigJSON), &next); err != nil {
				t.Fatal(err)
			}
			if next["listener_mode"] != "shared_port" || next["psk"] != cfg["psk"] || next["obfs_mode"] != cfg["obfs_mode"] || next["snell_profile_id"] != float64(1) || converted.Port != 6160 || converted.AdvertisePort != 443 || converted.SnellActiveMode != "per_identity_port" {
				t.Fatalf("upgrade lost parameters or invented runtime confirmation: %+v", converted)
			}
			allocations, err := s.ListProxyPathPortAllocations(ctx)
			if err != nil || len(allocations) != 1 || allocations[0].Port != old.Port {
				t.Fatalf("upgrade released an active old port: %+v %v", allocations, err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			again, err := s.GetInbound(ctx, in.ID)
			if err != nil || again.ConfigJSON != converted.ConfigJSON || !again.UpdatedAt.Equal(converted.UpdatedAt) {
				t.Fatal("shared-listener conversion is not idempotent")
			}
			listener := map[string]map[string]any{fmt.Sprintf("in-%d", in.ID): {"auth_mode": "multi_psk", "listen_port": 6160, "version": 5}}
			if err = s.ConfirmSnellRuntime(ctx, server.ID, 10, listener); err != nil {
				t.Fatal(err)
			}
			active, _ := s.GetInbound(ctx, in.ID)
			allocations, err = s.ListProxyPathPortAllocations(ctx)
			if err != nil || len(allocations) != 0 || active.SnellActiveMode != "shared_port" || active.SnellActivePort != 6160 {
				t.Fatal("verified shared endpoint did not retire old ports atomically")
			}
			if err = s.ConfirmSnellRuntime(ctx, server.ID, 9, nil); err != nil {
				t.Fatal(err)
			}
			active, _ = s.GetInbound(ctx, in.ID)
			if active.SnellActivePort != 6160 {
				t.Fatal("old runtime result replaced the new endpoint")
			}
		})
	}
}

func TestSnellSharedConversionRollsBackTheWholeBatch(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "batch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := model.Server{Name: "conversion-batch", Status: model.ServerOnline}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	var inbounds []model.Inbound
	for i := 0; i < 2; i++ {
		in := model.Inbound{ServerID: server.ID, Name: fmt.Sprintf("old-%d", i), Protocol: model.ProtocolSnell, Port: 6160 + i, ConfigJSON: `{"version":4,"psk":"previous-server-seed","listener_mode":"per_identity_port"}`}
		if err = s.CreateInbound(ctx, &in); err != nil {
			t.Fatal(err)
		}
		inbounds = append(inbounds, in)
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(`create trigger reject_shared_conversion before update of config_json on inbounds when new.id=%d begin select raise(abort, 'test conversion failure'); end`, inbounds[1].ID)); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateSnellSharedListeners(ctx); err == nil {
		t.Fatal("failed batch was accepted")
	}
	for _, before := range inbounds {
		after, err := s.GetInbound(ctx, before.ID)
		if err != nil || after.ConfigJSON != before.ConfigJSON {
			t.Fatal("failed batch partially converted an inbound")
		}
	}
	if _, err = s.db.ExecContext(ctx, `drop trigger reject_shared_conversion`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateSnellSharedListeners(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSnellSharedConfirmationRetiresOldOwnershipAtTheSamePort(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "reuse.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := model.Server{Name: "shared-port-reuse", Status: model.ServerOnline}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{ServerID: server.ID, Name: "snell", Protocol: model.ProtocolSnell, Port: 6160, ConfigJSON: `{"version":4,"listener_mode":"shared_port"}`}
	if err = s.CreateInbound(ctx, &in); err != nil {
		t.Fatal(err)
	}
	old := model.ProxyPathPortAllocation{Kind: model.ProxyPathPortKindSnellUser, ScopeKey: fmt.Sprintf("inbound:%d:user:7:path:0", in.ID), ServerID: server.ID, Port: in.Port, State: model.PortAllocationStateActive, Generation: 1}
	if err = s.SaveProxyPathPortAllocations(ctx, []model.ProxyPathPortAllocation{old}, nil); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]map[string]any{fmt.Sprintf("in-%d-u7", in.ID): {"listen_port": in.Port, "version": 5}}
	if err = s.ConfirmSnellRuntime(ctx, server.ID, 9, legacy); err != nil {
		t.Fatal(err)
	}
	allocations, err := s.ListProxyPathPortAllocations(ctx)
	if err != nil || len(allocations) != 1 {
		t.Fatal("old running listener lost its port ownership")
	}
	shared := map[string]map[string]any{fmt.Sprintf("in-%d", in.ID): {"auth_mode": "multi_psk", "listen_port": in.Port, "version": 5}}
	if err = s.ConfirmSnellRuntime(ctx, server.ID, 10, shared); err != nil {
		t.Fatal(err)
	}
	allocations, err = s.ListProxyPathPortAllocations(ctx)
	if err != nil || len(allocations) != 0 {
		t.Fatal("shared listener retained obsolete per-identity port ownership")
	}
}
