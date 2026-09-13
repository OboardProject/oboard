package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerHistoryPurgeIsBoundedAndDrains(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "purge.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "history", EntryAddress: "198.51.100.21", Status: "online"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if _, err := db.db.ExecContext(ctx, `insert into server_metric_samples(server_id,sampled_at) values(?,?)`, server.ID, now()); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := db.PurgeServerHistoryBatch(ctx, server.ID, 100)
	if err != nil || removed != 100 {
		t.Fatalf("first batch removed=%d err=%v", removed, err)
	}
	total := removed
	for {
		removed, err = db.PurgeServerHistoryBatch(ctx, server.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		if removed == 0 {
			break
		}
		if removed > 100 {
			t.Fatalf("a batch exceeded its limit: %d", removed)
		}
		total += removed
	}
	if total < 250 {
		t.Fatalf("purged %d rows, fewer than the 250 samples seeded", total)
	}
	if _, err := db.PurgeServerHistoryBatch(ctx, server.ID, 0); err == nil {
		t.Fatal("a non-positive batch limit would loop forever")
	}
	// The server itself is untouched by the purge; only its history is gone.
	if _, err := db.GetServer(ctx, server.ID); err != nil {
		t.Fatalf("purge removed the server: %v", err)
	}
}

func TestServerDeletionClaimIsIdempotentAndOutlivesTheServer(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "deletion.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "claimed", EntryAddress: "198.51.100.22", Status: "online"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	first, claimed, err := db.BeginServerDeletion(ctx, server.ID, server.Name, `{"dns_records":[]}`)
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	// A second delete of the same server joins the first instead of starting a
	// competing one that would release the same external state twice.
	second, claimed, err := db.BeginServerDeletion(ctx, server.ID, "other name", `{"dns_records":[{"record_id":"x"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if claimed || second.Payload != first.Payload || second.Name != first.Name {
		t.Fatalf("second claim overwrote the first: claimed=%v deletion=%+v", claimed, second)
	}

	if err := db.SetServerDeletionStage(ctx, server.ID, ServerDeletionExternal); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	// The tombstone has to survive the row it removed, otherwise the external
	// cleanup that still has to run would be forgotten.
	pending, err := db.ListServerDeletions(ctx)
	if err != nil || len(pending) != 1 || pending[0].Stage != ServerDeletionExternal {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := db.RecordServerDeletionFailure(ctx, server.ID, "provider unreachable"); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetServerDeletion(ctx, server.ID)
	if err != nil || after.Attempts != 1 || after.LastError != "provider unreachable" {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	if err := db.CompleteServerDeletion(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetServerDeletion(ctx, server.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed deletion still present: %v", err)
	}
}

// TestDeletedServerLeavesNoOpenIncident covers the per-server tables that carry
// a server_id without a foreign key, so nothing removes them with the server.
// An incident left open for a deleted node keeps showing in the console, keeps
// its publication isolations applied, and keeps producing notifications about a
// server that no longer exists.
func TestDeletedServerLeavesNoOpenIncident(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "incidents.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "flapping", EntryAddress: "198.51.100.31", Status: "offline"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	incident, _, err := db.OpenOrReopenNodeIncident(ctx, *server, at.Add(-10*time.Minute), at, 5*time.Minute, 2*time.Minute, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `insert into node_publication_isolations(incident_id,inbound_name,server_id,recovery_policy,status,actor_user_id,created_at,updated_at) values(?,?,?,'manual','hidden',1,?,?)`, incident.ID, "edge-ss", server.ID, now(), now()); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	open, err := db.ListNodeIncidents(ctx, "active", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("deleted server still has open incidents: %+v", open)
	}
	var isolations int
	if err := db.db.QueryRowContext(ctx, `select count(*) from node_publication_isolations where server_id=?`, server.ID).Scan(&isolations); err != nil || isolations != 0 {
		t.Fatalf("publication isolations survived the server: %d err=%v", isolations, err)
	}
}

