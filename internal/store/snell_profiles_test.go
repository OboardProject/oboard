package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestSnellProfilesSeededAndProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profiles, err := s.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) == 0 {
		t.Fatal("expected seeded snell profiles")
	}
	versions := map[int]bool{}
	for _, profile := range profiles {
		if !profile.Builtin {
			t.Fatalf("seed profile %q must be builtin", profile.Name)
		}
		if profile.Version != 4 && profile.Version != 6 {
			t.Fatalf("seed profile %q has invalid version %d", profile.Name, profile.Version)
		}
		versions[profile.Version] = true
	}
	if !versions[4] || !versions[6] {
		t.Fatalf("seed profiles must cover v4 and v6, got %#v", versions)
	}
	// Reopen must be idempotent (no duplicate seeds).
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(profiles) {
		t.Fatalf("seed count changed after reopen: %d -> %d", len(profiles), len(again))
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnellProfilesCRUDAndReferenceGuard(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	profile := model.SnellProfile{Name: "机柜 A v4", Version: 4, PSK: "secret-psk-1234", ObfsMode: "http", ObfsHost: "bing.com", Mode: "default", Enabled: true}
	if err := s.CreateSnellProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.ID <= 0 {
		t.Fatal("created profile has no id")
	}
	got, err := s.GetSnellProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "机柜 A v4" || got.Version != 4 || got.PSK != "secret-psk-1234" || got.ObfsMode != "http" || got.ObfsHost != "bing.com" {
		t.Fatalf("unexpected profile: %#v", got)
	}
	// Update with normalization.
	got.PSK = "new-psk-5678"
	got.ObfsMode = "http"
	got.Mode = "unshaped"
	changed, err := s.UpdateSnellProfile(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true after psk/obfs update")
	}
	after, err := s.GetSnellProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PSK != "new-psk-5678" || after.ObfsMode != "http" || after.Mode != "unshaped" {
		t.Fatalf("update not applied: %#v", after)
	}
	// Referenced profiles cannot be deleted.
	if err := s.CreateServer(ctx, &model.Server{Name: "node", AgentID: "agent-1", ChainSecret: "chain-secret", ListenIP: "0.0.0.0", Status: model.ServerOnline}); err != nil {
		t.Fatal(err)
	}
	servers, err := s.ListServers(ctx)
	if err != nil || len(servers) == 0 {
		t.Fatalf("list servers: %v (%d)", err, len(servers))
	}
	inbound := model.Inbound{ServerID: servers[0].ID, Name: "snell-in", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, ConfigJSON: `{"version":4,"snell_profile_id":` + strconv.FormatInt(profile.ID, 10) + `,"psk":"secret-psk-1234"}`, Enabled: true}
	if err := s.CreateInbound(ctx, &inbound); err != nil {
		t.Fatal(err)
	}
	usage, err := s.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var used bool
	for _, item := range usage {
		if item.ID == profile.ID && item.UsageCount >= 1 {
			used = true
		}
	}
	if !used {
		t.Fatalf("expected usage count >= 1, got %#v", usage)
	}
	if err := s.DeleteSnellProfile(ctx, profile.ID); err == nil {
		t.Fatal("expected delete to fail while inbound references the profile")
	} else if !strings.Contains(err.Error(), "引用") {
		t.Fatalf("unexpected guard error: %v", err)
	}
	// Builtin profiles are protected even without references.
	builtinID := int64(0)
	profiles, err := s.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range profiles {
		if item.Builtin {
			builtinID = item.ID
			break
		}
	}
	if builtinID == 0 {
		t.Fatal("no builtin profile found")
	}
	if err := s.DeleteSnellProfile(ctx, builtinID); err == nil {
		t.Fatal("expected builtin delete to fail")
	}
	// After unbinding, delete succeeds.
	if err := s.Delete(ctx, "inbounds", inbound.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSnellProfile(ctx, profile.ID); err != nil {
		t.Fatalf("delete after unbind: %v", err)
	}
	if _, err := s.GetSnellProfile(ctx, profile.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
}