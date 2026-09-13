package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The audit risk queue recomputes this aggregate for every dirty user every 15
// seconds. On a production Controller the busiest user had 135k rows in the
// window, and locating them through a (user_id, ended_at) index still cost one
// random seek into the largest table in the database per row: 3.0s of a 3.7s
// query. The index carries the aggregated columns so the table is never
// touched, and this test fails if a column is added to the aggregate that the
// index does not cover.
func TestConnectionAuditOverviewIsIndexOnly(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	rows, err := db.db.QueryContext(ctx, `explain query plan `+connectionAuditOverviewUsersQuery(1), int64(1), "2026-09-13T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notUsed int64
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "COVERING INDEX idx_connection_audit_user_window") {
		t.Fatalf("audit overview reads the report table instead of the covering index:\n%s", plan)
	}
}

// The widened index replaces the narrow one rather than joining it, because
// every insert into this table maintains each of its indexes.
func TestConnectionAuditUserIndexIsNotDuplicated(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.db.QueryRowContext(context.Background(), `select count(*) from sqlite_master where type='index' and name='idx_connection_audit_user_time'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("the superseded idx_connection_audit_user_time is still present")
	}
}
