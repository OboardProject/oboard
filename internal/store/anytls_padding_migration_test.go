package store

import (
	"context"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestExistingAnyTLSPaddingMigrationMarksCustomWithoutChangingScheme(t *testing.T) {
	path := t.TempDir() + "/oboard.sqlite"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	server := &model.Server{Name: "edge", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	legacyScheme := []string{"stop=3", "0=16-80", "1=120-320", "2=220-620"}
	legacy := &model.Inbound{ServerID: server.ID, Name: "legacy", Protocol: model.ProtocolAnyTLS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{"tls":{"enabled":true},"padding_scheme":["stop=3","0=16-80","1=120-320","2=220-620"]}`, Enabled: true}
	missing := &model.Inbound{ServerID: server.ID, Name: "missing", Protocol: model.ProtocolAnyTLS, ListenIP: "0.0.0.0", Port: 444, ConfigJSON: `{"tls":{"enabled":true}}`, Enabled: true}
	if err := s.CreateInbound(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInbound(ctx, missing); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateExistingAnyTLSPaddingMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.GetInbound(ctx, legacy.ID)
	metadata, scheme, err := core.AnyTLSPaddingMetadataFromJSON(stored.ConfigJSON)
	if err != nil || metadata == nil || metadata.Mode != core.AnyTLSPaddingModeCustom || strings.Join(scheme, "\n") != strings.Join(legacyScheme, "\n") {
		t.Fatalf("legacy migration = %#v %#v err=%v", metadata, scheme, err)
	}
	untouched, _ := s.GetInbound(ctx, missing.ID)
	if strings.Contains(untouched.ConfigJSON, "_oboard_padding") || strings.Contains(untouched.ConfigJSON, "padding_scheme") {
		t.Fatalf("missing scheme was changed: %s", untouched.ConfigJSON)
	}
	before := stored.ConfigJSON
	if err := s.migrateExistingAnyTLSPaddingMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetInbound(ctx, legacy.ID)
	if again.ConfigJSON != before {
		t.Fatal("migration is not idempotent")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	afterRestart, _ := reopened.GetInbound(ctx, legacy.ID)
	if afterRestart.ConfigJSON != before {
		t.Fatal("Controller restart changed the stored padding snapshot")
	}
}
