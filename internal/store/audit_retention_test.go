package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// Raw reports are the heaviest thing this database stores, and the fixed 30-day
// retention bought very little: a busy user's evaluation reaches its report
// ceiling inside a day, so older raw rows were stored, indexed and backed up
// without any consumer able to read them. Retention is now an operator setting.
func TestConnectionAuditRetentionIsAnOperatorSetting(t *testing.T) {
	if got := ConnectionAuditRetentionDays(nil); got != DefaultConnectionAuditRetentionDays {
		t.Fatalf("default = %d, want %d", got, DefaultConnectionAuditRetentionDays)
	}
	for _, bad := range []string{"", "0", "-3", "31", "abc"} {
		if got := ConnectionAuditRetentionDays(map[string]string{ConnectionAuditRetentionDaysSetting: bad}); got != DefaultConnectionAuditRetentionDays {
			t.Fatalf("%q gave %d, want the default", bad, got)
		}
	}
	if got := ConnectionAuditRetentionDays(map[string]string{ConnectionAuditRetentionDaysSetting: "30"}); got != 30 {
		t.Fatalf("configured 30 gave %d", got)
	}
}

// The hourly rollup is what the 28-day robust-Z baseline reads. Shortening raw
// retention must not take that long-term signal with it.
func TestShortRawRetentionKeepsTheHourlyBaseline(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "retention.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, ConnectionAuditRetentionDaysSetting, "1"); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "ret-node", PublicIPv4: "203.0.113.30", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "ret-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// One report inside the 1-day window and one ten days back.
	for index, ended := range []time.Time{now.Add(-2 * time.Hour), now.Add(-10 * 24 * time.Hour)} {
		if _, err := db.AddConnectionAuditReportsResult(ctx, []model.ConnectionAuditReport{{
			ReportID: "ret-" + ended.Format("20060102150405"), ServerID: server.ID, UserID: user.ID,
			SourceIP: "198.51.100.40", Network: "tcp", ConnectionCount: int64(index + 1), BucketCapacity: 1,
			CollectionStartedAt: ended.Add(-time.Minute), CollectionEndedAt: ended,
			StartedAt: ended.Add(-time.Minute), EndedAt: ended, CreatedAt: ended,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	dirty, err := db.ListConnectionAuditHourlyDirty(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range dirty {
		if err := db.RecomputeConnectionAuditHour(ctx, item.UserID, item.UTCHour, item.DirtyAt); err != nil {
			t.Fatal(err)
		}
	}
	var hourlyBefore int
	if err := db.db.QueryRowContext(ctx, `select count(*) from connection_audit_hourly`).Scan(&hourlyBefore); err != nil {
		t.Fatal(err)
	}
	if hourlyBefore != 2 {
		t.Fatalf("hourly rows before maintenance = %d, want 2", hourlyBefore)
	}

	if _, err := db.RunMaintenance(ctx, now); err != nil {
		t.Fatal(err)
	}

	var rawRemaining int
	if err := db.db.QueryRowContext(ctx, `select count(*) from connection_audit_reports`).Scan(&rawRemaining); err != nil {
		t.Fatal(err)
	}
	if rawRemaining != 1 {
		t.Fatalf("raw reports after a 1-day retention pass = %d, want only the recent one", rawRemaining)
	}
	var hourlyAfter int
	if err := db.db.QueryRowContext(ctx, `select count(*) from connection_audit_hourly`).Scan(&hourlyAfter); err != nil {
		t.Fatal(err)
	}
	if hourlyAfter != 2 {
		t.Fatalf("hourly rows after maintenance = %d, want the 10-day-old baseline kept", hourlyAfter)
	}
}

// device_id_hash is carried on every report but is never a search key, so its
// index was maintained on every insert for nothing.
func TestNoIndexIsKeptForDeviceHash(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "idx.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The two window indexes are built after the server is already serving.
	if err := db.MigrateDeferredIndexes(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRowContext(context.Background(), `select count(*) from sqlite_master where type='index' and name='idx_connection_audit_device_time'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("the unused device_id_hash index is still created")
	}
	// The indexes that are used must still be there.
	for _, name := range []string{"idx_connection_audit_user_window", "idx_connection_audit_source_window", "idx_connection_audit_time", "idx_connection_audit_route_time", "idx_connection_audit_server_time"} {
		present, err := db.indexExists(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			t.Fatalf("%s was dropped but queries still need it", name)
		}
	}
}
