package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerDisplayTagsMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "display-tags.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{
		Name:           "tag-node",
		AgentID:        "tag-agent",
		AgentTokenHash: "tag-token",
		ChainSecret:    "chain-secret",
		ListenIP:       "0.0.0.0",
		Status:         model.ServerOnline,
		DisplayTags: []model.ServerDisplayTag{
			{Text: "IncuShlii", Tone: "blue"},
			{Text: "原生IP/住宅IP", Tone: "orange"},
		},
	}
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
	if _, err := raw.Exec(`alter table servers drop column display_tags_json`); err != nil {
		t.Fatalf("drop display_tags_json: %v", err)
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
	if len(stored.DisplayTags) != 0 {
		t.Fatalf("migrated tags should default empty, got %#v", stored.DisplayTags)
	}
	stored.DisplayTags = []model.ServerDisplayTag{{Text: "峰值 500Mbps", Tone: "purple"}}
	if err := s.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.GetServer(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.DisplayTags) != 1 || reloaded.DisplayTags[0].Text != "峰值 500Mbps" || reloaded.DisplayTags[0].Tone != "purple" {
		t.Fatalf("reloaded tags = %#v", reloaded.DisplayTags)
	}
	if reloaded.Name != "tag-node" {
		t.Fatalf("server row was rewritten: %#v", reloaded)
	}
}
