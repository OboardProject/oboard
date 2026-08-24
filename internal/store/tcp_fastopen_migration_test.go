package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// TestTCPFastOpenColumnsMigrateFromPreviousSchema starts from the real previous
// state (no snell_profiles.tcp_fast_open and no servers.tcp_fastopen_* columns)
// and verifies the migration adds them with off/unknown defaults while keeping
// existing rows intact.
func TestTCPFastOpenColumnsMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tcp-fastopen.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "tfo-node", AgentID: "tfo-agent", AgentTokenHash: "tfo-token", ChainSecret: "chain-secret", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	profile := model.SnellProfile{Name: "机柜 A v4", Version: 4, PSK: "secret-psk-1234", Mode: "default", Enabled: true}
	if err := s.CreateSnellProfile(ctx, &profile); err != nil {
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
		`alter table snell_profiles drop column tcp_fast_open`,
		`alter table servers drop column tcp_fastopen_state`,
		`alter table servers drop column tcp_fastopen_value`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("open with previous schema: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}

	profiles, err := s.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var migrated *model.SnellProfile
	for index := range profiles {
		if profiles[index].ID == profile.ID {
			migrated = &profiles[index]
		}
		if profiles[index].TCPFastOpen {
			t.Fatalf("migrated profile %q must default to TFO off", profiles[index].Name)
		}
	}
	if migrated == nil || migrated.PSK != "secret-psk-1234" {
		t.Fatalf("migrated profile lost its parameters: %#v", migrated)
	}
	migrated.TCPFastOpen = true
	changed, err := s.UpdateSnellProfile(ctx, migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("enabling TFO must report the profile as changed")
	}
	stored, err := s.GetSnellProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.TCPFastOpen {
		t.Fatalf("stored profile = %#v", stored)
	}

	servers, err := s.ListServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("list servers: %v (%d)", err, len(servers))
	}
	if servers[0].TCPFastOpenState != model.TCPFastOpenStateUnknown || servers[0].TCPFastOpenValue != 0 {
		t.Fatalf("migrated server TFO state = %q/%d, want unknown", servers[0].TCPFastOpenState, servers[0].TCPFastOpenValue)
	}
	current := servers[0]
	current.TCPFastOpenState = model.TCPFastOpenStateClientServer
	current.TCPFastOpenValue = 3
	if err := s.UpdateServerRuntimeState(ctx, &current); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.GetServer(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.TCPFastOpenState != model.TCPFastOpenStateClientServer || reloaded.TCPFastOpenValue != 3 {
		t.Fatalf("reloaded server TFO state = %q/%d", reloaded.TCPFastOpenState, reloaded.TCPFastOpenValue)
	}
}