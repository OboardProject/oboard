package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// The release C backup/restore gate.
//
// A backup is a byte-level copy, so it carries whatever schema the source had.
// The case that matters for this release is a snapshot taken from an
// installation that still has the narrow audit indexes - every Controller
// upgraded from a build before the widened pair - restored onto an installation
// that has never carried them. The restored database must open, keep its data,
// and converge to the current schema through the deferred migration rather than
// staying on the old indexes or failing to open.
func TestRestoredPreMigrationSnapshotConvergesToTheCurrentSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source, err := Open(filepath.Join(dir, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	// Put the source back into the pre-migration shape.
	for _, stmt := range []string{
		`drop index if exists idx_connection_audit_user_window`,
		`drop index if exists idx_connection_audit_source_window`,
		`create index if not exists idx_connection_audit_user_time on connection_audit_reports(user_id, ended_at desc)`,
		`create index if not exists idx_connection_audit_source_time on connection_audit_reports(source_ip, ended_at desc)`,
	} {
		if _, err := source.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	server := &model.Server{Name: "restore-node", AgentID: "restore-agent", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := source.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "restore-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := source.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	reports := make([]model.ConnectionAuditReport, 0, 40)
	for i := 0; i < 40; i++ {
		ended := at.Add(time.Duration(i) * time.Minute)
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: "restore-" + ended.Format("150405"), ServerID: server.ID, UserID: user.ID,
			SourceIP: "198.51.100.7", Network: "tcp", ConnectionCount: int64(i + 1), BucketCapacity: 1,
			CollectionStartedAt: ended.Add(-time.Minute), CollectionEndedAt: ended,
			StartedAt: ended.Add(-time.Minute), EndedAt: ended, CreatedAt: ended,
		})
	}
	if _, err := source.AddConnectionAuditReportsResult(ctx, reports); err != nil {
		t.Fatal(err)
	}

	snapshot := filepath.Join(dir, "snapshot.sqlite")
	if err := source.Backup(ctx, snapshot, BackupOptions{}); err != nil {
		t.Fatalf("backup of a pre-migration installation failed: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	// Restoring is putting the snapshot in place and opening it.
	restored, err := Open(snapshot)
	if err != nil {
		t.Fatalf("restored snapshot does not open: %v", err)
	}
	defer restored.Close()

	var reportCount int
	if err := restored.db.QueryRowContext(ctx, `select count(*) from connection_audit_reports`).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != len(reports) {
		t.Fatalf("restored report count = %d, want %d", reportCount, len(reports))
	}
	servers, err := restored.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "restore-node" {
		t.Fatalf("restored servers = %#v", servers)
	}

	// Opening does not build the deferred indexes: that is the whole point of
	// deferring them, and a restore must not be blocked on it either.
	pending, err := restored.PendingDeferredIndexes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending after restore = %v, want both deferred indexes", pending)
	}
	// The audit surface must answer before the migration runs.
	if _, err := restored.ConnectionAuditOverviewForUsers(ctx, 24, true, DefaultAuditPolicy(), []int64{user.ID}); err != nil {
		t.Fatalf("restored installation cannot serve audit before the migration: %v", err)
	}

	if err := restored.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatalf("deferred migration on a restored snapshot failed: %v", err)
	}
	pending, err = restored.PendingDeferredIndexes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("restored installation did not converge: %v", pending)
	}
	for _, name := range []string{"idx_connection_audit_user_time", "idx_connection_audit_source_time"} {
		present, err := restored.indexExists(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if present {
			t.Fatalf("the restored snapshot kept the superseded %s", name)
		}
	}

	overview, err := restored.ConnectionAuditOverviewForUsers(ctx, 24, true, DefaultAuditPolicy(), []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Users) != 1 || overview.Users[0].UserID != user.ID {
		t.Fatalf("converged installation lost the restored user: %#v", overview.Users)
	}
}
