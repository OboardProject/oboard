package store

import (
	"context"
	"path/filepath"
	"testing"
)

// The previous state is an installation carrying the narrow indexes: that is
// what every Controller upgraded from a build before 51240e0 has on disk. The
// migration must reach the new pair from there, and must leave the old ones
// only once their replacements exist.
func TestDeferredIndexMigrationFromTheNarrowIndexes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Recreate the pre-migration schema.
	for _, stmt := range []string{
		`drop index if exists idx_connection_audit_user_window`,
		`drop index if exists idx_connection_audit_source_window`,
		`create index if not exists idx_connection_audit_user_time on connection_audit_reports(user_id, ended_at desc)`,
		`create index if not exists idx_connection_audit_source_time on connection_audit_reports(source_ip, ended_at desc)`,
	} {
		if _, err := db.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := db.PendingDeferredIndexes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want both deferred indexes", pending)
	}

	if err := db.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_connection_audit_user_window", "idx_connection_audit_source_window"} {
		present, err := db.indexExists(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			t.Fatalf("%s was not created", name)
		}
	}
	for _, name := range []string{"idx_connection_audit_user_time", "idx_connection_audit_source_time"} {
		present, err := db.indexExists(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if present {
			t.Fatalf("%s was not dropped", name)
		}
	}

	pending, err = db.PendingDeferredIndexes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after migration = %v, want none", pending)
	}
	// Calling it again is a lookup, not a rebuild.
	if err := db.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatal(err)
	}
}

// An interruption after the new index is built but before the old one is
// dropped must be completed by the next call, not undone.
func TestDeferredIndexMigrationResumesAfterAPartialRun(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	// Both indexes present at once is the state an interrupted run leaves, and
	// the state a rolled-back Controller recreates.
	if _, err := db.db.ExecContext(ctx, `create index if not exists idx_connection_audit_user_time on connection_audit_reports(user_id, ended_at desc)`); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingDeferredIndexes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != "idx_connection_audit_user_window" {
		t.Fatalf("pending = %v, want the pair whose predecessor came back", pending)
	}
	if err := db.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	present, err := db.indexExists(ctx, "idx_connection_audit_user_time")
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("the superseded index survived a second migration")
	}
}

// Nothing may depend on a deferred index for correctness: the Controller serves
// before it exists.
func TestAuditOverviewAnswersBeforeTheDeferredIndexExists(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`drop index if exists idx_connection_audit_user_window`,
		`drop index if exists idx_connection_audit_source_window`,
	} {
		if _, err := db.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ConnectionAuditOverviewForUsers(ctx, 24, true, DefaultAuditPolicy(), []int64{1}); err != nil {
		t.Fatalf("audit overview requires the deferred index: %v", err)
	}
}
