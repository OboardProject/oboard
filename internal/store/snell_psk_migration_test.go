package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func openStoreOrDeadlock(t *testing.T, path string) *Store {
	t.Helper()
	type result struct {
		store *Store
		err   error
	}
	done := make(chan result, 1)
	go func() {
		s, err := Open(path)
		done <- result{s, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.store
	case <-time.After(8 * time.Second):
		t.Fatal("Open deadlocked; migrateSnellServerPSK must not QueryRow while the snell inbound cursor holds MaxOpenConns=1")
		return nil
	}
}

func snellInboundPSK(t *testing.T, s *Store, id int64) string {
	t.Helper()
	inbound, err := s.GetInbound(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(inbound.ConfigJSON), &cfg); err != nil {
		t.Fatalf("config_json=%q err=%v", inbound.ConfigJSON, err)
	}
	psk, _ := cfg["psk"].(string)
	return strings.TrimSpace(psk)
}

func TestSnellServerPSKMigratesWithoutNestedQueryDeadlock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snell-psk-deadlock.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "snell-node", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	first := model.Inbound{ServerID: server.ID, Name: "snell-empty", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, ConfigJSON: `{"version":4}`, Enabled: true}
	second := model.Inbound{ServerID: server.ID, Name: "snell-v6", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6161, ConfigJSON: `{"version":6}`, Enabled: true}
	if err := s.CreateInbound(ctx, &first); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInbound(ctx, &second); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openStoreOrDeadlock(t, path)
	defer s.Close()
	for _, item := range []struct {
		id      int64
		version int
	}{{first.ID, 4}, {second.ID, 6}} {
		psk := snellInboundPSK(t, s, item.id)
		if psk == "" {
			t.Fatalf("inbound %d psk was not frozen", item.id)
		}
		if err := core.ValidateSnellPSKForVersion(psk, item.version); err != nil {
			t.Fatalf("inbound %d psk %q: %v", item.id, psk, err)
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeated snell psk migration is not idempotent: %v", err)
	}
}

func TestSnellServerPSKFreezesLegacyInboundUserPassword(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snell-psk-inbound-users.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "snell-legacy", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "snell-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "44444444-4444-4444-8444-444444444444", ProxyPassword: "bound-user-psk"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: server.ID, Name: "snell-bound", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, ConfigJSON: `{"version":4}`, Enabled: true}
	if err := s.CreateInbound(ctx, &inbound); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`create table inbound_users (id integer primary key autoincrement, inbound_id integer not null, user_id integer not null, enabled integer not null default 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`insert into inbound_users(inbound_id,user_id,enabled) values(?,?,1)`, inbound.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s = openStoreOrDeadlock(t, path)
	defer s.Close()
	if got := snellInboundPSK(t, s, inbound.ID); got != "bound-user-psk" {
		t.Fatalf("frozen psk=%q, want bound user password", got)
	}
	var leftover int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='inbound_users'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("inbound_users should be dropped after psk freeze, leftover=%d", leftover)
	}
}
