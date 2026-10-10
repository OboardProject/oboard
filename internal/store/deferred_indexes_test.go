package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
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
	if len(pending) != len(deferredIndexes) {
		t.Fatalf("pending = %v, want all deferred indexes", pending)
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

func TestDeletionIndexesMigrateHistoricalReferences(t *testing.T) {
	ctx := context.Background()
	old, server, user := newMaintenanceTestStore(t)
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := old.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Secret: "seed", Enabled: true}
	if err := old.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := old.db.ExecContext(ctx, "insert into traffic_stats(server_id,user_id,inbound_id,upload_bytes,download_bytes,created_at) values(?,?,?,123,456,?)", server.ID, user.ID, inbound.ID, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx, "insert into traffic_reports(report_id,server_id,user_id,inbound_id,path_id,period_key,upload_bytes,download_bytes,started_at,ended_at,created_at) values('historical',?,?,?,?,'period',123,456,?,?,?)", server.ID, user.ID, inbound.ID, path.ID, ts, ts, ts); err != nil {
		t.Fatal(err)
	}
	insertMaintenanceConnectionAudit(t, old, server.ID, user.ID, "historical", time.Now().UTC())
	if _, err := old.db.ExecContext(ctx, "update connection_audit_reports set inbound_id=?,path_id=?,upload_bytes=123,download_bytes=456", inbound.ID, path.ID); err != nil {
		t.Fatal(err)
	}
	checks := []struct{ table, column string }{
		{"traffic_stats", "inbound_id"}, {"traffic_reports", "inbound_id"}, {"traffic_reports", "path_id"},
		{"connection_audit_reports", "inbound_id"}, {"connection_audit_reports", "path_id"},
	}
	plan := func(db *Store, table, column string) string {
		t.Helper()
		rows, err := db.db.QueryContext(ctx, "explain query plan select rowid from "+table+" where "+column+"=?", inbound.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			out += detail
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, check := range checks {
		if !strings.Contains(plan(old, check.table, check.column), "SCAN ") {
			t.Fatalf("previous schema unexpectedly indexes %s.%s", check.table, check.column)
		}
	}
	dbPath := old.path
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		if err := db.MigrateDeferredIndexes(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range checks {
		if got := plan(db, check.table, check.column); !strings.Contains(got, "SEARCH ") {
			t.Fatalf("unindexed foreign key %s.%s: %s", check.table, check.column, got)
		}
	}
	if err := db.DeleteProxyPathsForInbound(ctx, inbound.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(ctx, "inbounds", inbound.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"traffic_stats", "traffic_reports", "connection_audit_reports"} {
		var count, up, down int
		query := "select count(*),sum(upload_bytes),sum(download_bytes) from " + table + " where inbound_id is null"
		if table != "traffic_stats" {
			query += " and path_id is null"
		}
		if err := db.db.QueryRowContext(ctx, query).Scan(&count, &up, &down); err != nil {
			t.Fatal(err)
		}
		if count != 1 || up != 123 || down != 456 {
			t.Fatalf("%s history changed: count=%d bytes=%d/%d", table, count, up, down)
		}
	}
}
