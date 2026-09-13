package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
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

// The shared-address probe decides whether anyone else reports from an address
// in the window. user_id is carried by the index so that answer never seeks the
// table: on a production Controller the busiest address held 40,507 rows, and
// concluding it belonged to a single user meant one seek per row.
func TestSharedSourceProbeIsIndexOnly(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.db.QueryContext(context.Background(), `explain query plan select 1 from connection_audit_reports shared
		where shared.source_ip=? and shared.user_id<>? and shared.ended_at>=?`, "198.51.100.1", int64(1), "2026-09-13T00:00:00Z")
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
	if !strings.Contains(plan, "COVERING INDEX idx_connection_audit_source_window") {
		t.Fatalf("shared-address probe seeks the report table:\n%s", plan)
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

// SharedSourceIPCount is derived by deduplicating (user_id, source_ip) before
// the correlated EXISTS rather than after. This fixture repeats each address
// many times and shares only some of them with another user, so a rewrite that
// changed the meaning - counting rows, or losing the "different user" condition
// - produces a different number.
func TestSharedSourceIPCountIgnoresRepeatedRows(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "shared.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "shared-node", PublicIPv4: "203.0.113.7", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	subject := &model.User{Username: "subject", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, subject); err != nil {
		t.Fatal(err)
	}
	other := &model.User{Username: "other", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC().Add(-time.Minute)
	reports := []model.ConnectionAuditReport{}
	add := func(userID int64, ip string, copies int) {
		for i := 0; i < copies; i++ {
			started := at.Add(-time.Duration(i) * time.Second)
			reports = append(reports, model.ConnectionAuditReport{
				ReportID: fmt.Sprintf("r-%d-%s-%d", userID, ip, i),
				ServerID: server.ID, UserID: userID, SourceIP: ip, Network: "tcp",
				ConnectionCount: 1, BucketCapacity: 1,
				CollectionStartedAt: started, CollectionEndedAt: at,
				StartedAt: started, EndedAt: at, CreatedAt: started,
			})
		}
	}
	// Two addresses the other user also uses, each repeated; one the subject
	// keeps to itself however many times it appears.
	add(subject.ID, "198.51.100.1", 40)
	add(subject.ID, "198.51.100.2", 25)
	add(subject.ID, "198.51.100.3", 30)
	add(other.ID, "198.51.100.1", 5)
	add(other.ID, "198.51.100.2", 5)
	if _, err := db.AddConnectionAuditReportsResult(ctx, reports); err != nil {
		t.Fatal(err)
	}

	overview, err := db.ConnectionAuditOverviewForUsers(ctx, 24, true, DefaultAuditPolicy(), []int64{subject.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range overview.Users {
		if item.UserID != subject.ID {
			continue
		}
		found = true
		if item.SharedSourceIPCount != 2 {
			t.Fatalf("SharedSourceIPCount = %d, want 2 distinct shared addresses out of 95 rows", item.SharedSourceIPCount)
		}
	}
	if !found {
		t.Fatal("subject missing from overview")
	}
}
