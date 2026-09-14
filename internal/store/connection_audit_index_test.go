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
	if err := db.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatal(err)
	}

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
	if err := db.MigrateDeferredIndexes(context.Background()); err != nil {
		t.Fatal(err)
	}
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
	if err := db.MigrateDeferredIndexes(context.Background()); err != nil {
		t.Fatal(err)
	}
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

// The risk queue evaluates one user at a time. For one user the window-function
// form and the ordered-limit form must return the same rows in the same order;
// only the work differs, because row_number() numbers the whole window before
// the outer filter can drop anything.
func TestSingleUserRiskReportsMatchTheBatchForm(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "risk.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "risk-node", PublicIPv4: "203.0.113.9", Status: model.ServerOnline}
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
	base := time.Now().UTC().Add(-2 * time.Hour)
	reports := []model.ConnectionAuditReport{}
	for i := 0; i < 60; i++ {
		ended := base.Add(time.Duration(i) * time.Minute)
		for _, userID := range []int64{subject.ID, other.ID} {
			reports = append(reports, model.ConnectionAuditReport{
				ReportID: fmt.Sprintf("r-%d-%d", userID, i), ServerID: server.ID, UserID: userID,
				SourceIP: "198.51.100.5", Network: "tcp", ConnectionCount: int64(i + 1), BucketCapacity: 1,
				CollectionStartedAt: ended.Add(-time.Minute), CollectionEndedAt: ended,
				StartedAt: ended.Add(-time.Minute), EndedAt: ended, CreatedAt: ended,
			})
		}
	}
	if _, err := db.AddConnectionAuditReportsResult(ctx, reports); err != nil {
		t.Fatal(err)
	}
	since := base.Add(-time.Hour).Format(time.RFC3339Nano)

	for _, limit := range []int{5, 25, 1000} {
		single, err := db.connectionAuditReportsForRisk(ctx, subject.ID, since, limit)
		if err != nil {
			t.Fatal(err)
		}
		// The batch form with two users exercises the window function; the
		// subject's slice must match what the single-user form returned.
		batch, err := db.batchConnectionAuditReportsForRisk(ctx, []int64{subject.ID, other.ID}, since, limit)
		if err != nil {
			t.Fatal(err)
		}
		got, want := single[subject.ID], batch[subject.ID]
		if len(got) != len(want) {
			t.Fatalf("limit %d: single returned %d reports, batch %d", limit, len(got), len(want))
		}
		for i := range got {
			if got[i].ReportID != want[i].ReportID {
				t.Fatalf("limit %d: position %d single=%s batch=%s", limit, i, got[i].ReportID, want[i].ReportID)
			}
		}
	}
}

// ConnectionAuditUserRisk computes the same window aggregate as the overview,
// written as a left join from users. It asked for count(report_id), the one
// column idx_connection_audit_user_window does not carry, which cost a seek
// into the report table per row - 57,641 of them for the busiest user on a
// production Controller, on every evaluation of that user.
func TestUserRiskAggregateIsIndexOnlyAndCountsMatchedRows(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "risk-index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "risk-node", PublicIPv4: "203.0.113.8", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	reported := &model.User{Username: "reported", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, reported); err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC()
	reports := []model.ConnectionAuditReport{}
	for i := 0; i < 9; i++ {
		ended := at.Add(-time.Duration(i) * time.Minute)
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: "risk-" + ended.Format("150405.000"), ServerID: server.ID, UserID: reported.ID,
			SourceIP: "198.51.100.20", Network: "tcp", ConnectionCount: 2, BucketCapacity: 1,
			CollectionStartedAt: ended.Add(-time.Minute), CollectionEndedAt: ended,
			StartedAt: ended.Add(-time.Minute), EndedAt: ended, CreatedAt: ended,
		})
	}
	// One outside the window, which must not be counted.
	old := at.Add(-48 * time.Hour)
	reports = append(reports, model.ConnectionAuditReport{
		ReportID: "risk-old", ServerID: server.ID, UserID: reported.ID,
		SourceIP: "198.51.100.21", Network: "tcp", ConnectionCount: 99, BucketCapacity: 1,
		CollectionStartedAt: old.Add(-time.Minute), CollectionEndedAt: old,
		StartedAt: old.Add(-time.Minute), EndedAt: old, CreatedAt: old,
	})
	if _, err := db.AddConnectionAuditReportsResult(ctx, reports); err != nil {
		t.Fatal(err)
	}

	item, err := db.ConnectionAuditUserRisk(ctx, reported.ID, 24, DefaultAuditPolicy(), at)
	if err != nil {
		t.Fatal(err)
	}
	if item.ReportCount != 9 {
		t.Fatalf("ReportCount = %d, want 9 matched rows in the window", item.ReportCount)
	}
	if item.ConnectionCount != 18 {
		t.Fatalf("ConnectionCount = %d, want 18", item.ConnectionCount)
	}
	if item.SourceIPCount != 1 {
		t.Fatalf("SourceIPCount = %d, want 1", item.SourceIPCount)
	}

	plan := explainPlan(t, db, `select u.id,
		coalesce(count(distinct case when r.source_ip<>'' then r.source_ip end),0),
		coalesce(count(distinct r.server_id),0),
		coalesce(sum(r.connection_count),0),
		coalesce(max(r.active_peak),0),
		coalesce(count(r.ended_at),0),
		max(r.ended_at)
		from users u
		left join connection_audit_reports r on r.user_id=u.id and r.ended_at>=?
		where u.id=? group by u.id`, "2026-09-13T00:00:00Z", reported.ID)
	if !strings.Contains(plan, "COVERING INDEX idx_connection_audit_user_window") {
		t.Fatalf("user risk aggregate seeks the report table:\n%s", plan)
	}
}
