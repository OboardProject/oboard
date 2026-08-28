package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerCPUCoresMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cpu-cores.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{
		Name:           "cpu-node",
		AgentID:        "cpu-agent",
		AgentTokenHash: "cpu-token",
		ChainSecret:    "chain-secret",
		ListenIP:       "0.0.0.0",
		Status:         model.ServerOnline,
		CPU:            "Intel Xeon E3-12xx v2 (Ivy Bridge, IBRS)",
		CPUCores:       4,
	}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`alter table servers drop column cpu_cores`); err != nil {
		t.Fatalf("drop cpu_cores: %v", err)
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

	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CPUCores != 0 {
		t.Fatalf("migrated cpu_cores should default 0, got %d", stored.CPUCores)
	}
	if stored.CPU != "Intel Xeon E3-12xx v2 (Ivy Bridge, IBRS)" {
		t.Fatalf("cpu model was rewritten: %q", stored.CPU)
	}
	stored.CPUCores = 8
	if err := s.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.GetServer(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CPUCores != 8 {
		t.Fatalf("reloaded cpu_cores = %d, want 8", reloaded.CPUCores)
	}
	if reloaded.Name != "cpu-node" {
		t.Fatalf("server row was rewritten: %#v", reloaded)
	}
}
