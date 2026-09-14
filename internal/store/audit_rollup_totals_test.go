package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// The window totals the console shows must not change when they start coming
// from the rollup instead of the raw reports. This builds a window with varied
// addresses, servers and regions across several hours, rolls it up, and
// compares every total against the same aggregate computed directly from the
// raw rows.
func TestRollupWindowTotalsEqualTheRawAggregate(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rollup.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	servers := []*model.Server{}
	for i := 0; i < 3; i++ {
		server := &model.Server{Name: fmt.Sprintf("rollup-node-%d", i), PublicIPv4: fmt.Sprintf("203.0.113.%d", 100+i), Status: model.ServerOnline}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server)
	}
	user := &model.User{Username: "rollup-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Truncate(time.Hour).Add(-6 * time.Hour)
	countries := []string{"SG", "JP", "SG", "US"}
	ips := []string{"198.51.100.1", "198.51.100.2", "198.51.100.1", "203.0.113.9"}
	reports := []model.ConnectionAuditReport{}
	for hour := 0; hour < 5; hour++ {
		for slot := 0; slot < 4; slot++ {
			started := base.Add(time.Duration(hour)*time.Hour + time.Duration(slot*7)*time.Minute)
			ended := started.Add(time.Minute)
			reports = append(reports, model.ConnectionAuditReport{
				ReportID: fmt.Sprintf("rollup-%d-%d", hour, slot),
				ServerID: servers[slot%len(servers)].ID, UserID: user.ID,
				SourceIP: ips[slot], SourceCountryCode: countries[slot], Network: "tcp",
				ConnectionCount: int64(hour + slot + 1),
				UploadBytes:     int64(100 * (slot + 1)), DownloadBytes: int64(200 * (slot + 1)),
				PayloadFirstAt: started, PayloadLastAt: ended,
				ActivePeak: int64(slot + 1), BucketCapacity: 4, DroppedBucketCount: 0,
				CollectionGeneration: uint64(hour + 1),
				CollectionStartedAt:  started, CollectionEndedAt: ended,
				StartedAt: started, EndedAt: ended, CreatedAt: ended,
			})
		}
	}
	if _, err := db.AddConnectionAuditReportsResult(ctx, reports); err != nil {
		t.Fatal(err)
	}
	dirty, err := db.ListConnectionAuditHourlyDirty(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range dirty {
		if err := db.RecomputeConnectionAuditHour(ctx, item.UserID, item.UTCHour, item.DirtyAt); err != nil {
			t.Fatal(err)
		}
	}

	since := base
	until := base.Add(5 * time.Hour)
	totals, err := db.ConnectionAuditWindowTotalsFromRollup(ctx, []int64{user.ID}, since, until)
	if err != nil {
		t.Fatal(err)
	}
	got := totals[user.ID]
	if got == nil {
		t.Fatal("the rollup produced no totals for a user with reports")
	}

	// The same numbers, straight from the raw rows.
	var wantConnections, wantReports, wantPeak, wantUpload, wantDownload int64
	var wantIPs, wantServers, wantRegions int
	var wantLastEnded string
	if err := db.db.QueryRowContext(ctx, `select
			coalesce(sum(connection_count),0), count(*), coalesce(max(active_peak),0),
			coalesce(sum(upload_bytes),0), coalesce(sum(download_bytes),0),
			count(distinct source_ip), count(distinct server_id), count(distinct source_country_code),
			coalesce(max(ended_at),'')
		from connection_audit_reports
		where user_id=? and started_at>=? and started_at<?`,
		user.ID, since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano)).
		Scan(&wantConnections, &wantReports, &wantPeak, &wantUpload, &wantDownload, &wantIPs, &wantServers, &wantRegions, &wantLastEnded); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct {
		name      string
		got, want int64
	}{
		{"ConnectionCount", got.ConnectionCount, wantConnections},
		{"ReportCount", got.ReportCount, wantReports},
		{"ActivePeak", got.ActivePeak, wantPeak},
		{"UploadBytes", got.UploadBytes, wantUpload},
		{"DownloadBytes", got.DownloadBytes, wantDownload},
	} {
		if check.got != check.want {
			t.Fatalf("%s from rollup = %d, raw = %d", check.name, check.got, check.want)
		}
	}
	if got.SourceIPCount != wantIPs {
		t.Fatalf("SourceIPCount from rollup = %d, raw = %d", got.SourceIPCount, wantIPs)
	}
	if got.ServerCount != wantServers {
		t.Fatalf("ServerCount from rollup = %d, raw = %d", got.ServerCount, wantServers)
	}
	if got.RegionCount != wantRegions {
		t.Fatalf("RegionCount from rollup = %d, raw = %d", got.RegionCount, wantRegions)
	}
	if !got.LastEndedAt.Equal(parseTime(wantLastEnded)) {
		t.Fatalf("LastEndedAt from rollup = %s, raw = %s", got.LastEndedAt, wantLastEnded)
	}
	if !got.DimensionsComplete {
		t.Fatal("a small window was reported as having incomplete dimensions")
	}
	// The addresses themselves survive, so subnet-derived counts stay possible.
	if len(got.SourceIPs) != wantIPs {
		t.Fatalf("kept %d source addresses, want %d", len(got.SourceIPs), wantIPs)
	}
}

// A window whose raw reports have been purged must still report its totals:
// that is the whole point of keeping the rollup longer than the raw rows.
func TestRollupTotalsSurviveRawPurge(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rollup-purge.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, ConnectionAuditRetentionDaysSetting, "1"); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "purge-node", PublicIPv4: "203.0.113.77", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "purge-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour).Truncate(time.Hour).Add(20 * time.Minute)
	if _, err := db.AddConnectionAuditReportsResult(ctx, []model.ConnectionAuditReport{{
		ReportID: "purge-1", ServerID: server.ID, UserID: user.ID,
		SourceIP: "198.51.100.55", SourceCountryCode: "DE", Network: "tcp",
		ConnectionCount: 12, UploadBytes: 500, DownloadBytes: 900,
		PayloadFirstAt: old, PayloadLastAt: old.Add(time.Minute),
		ActivePeak: 2, BucketCapacity: 4,
		CollectionStartedAt: old, CollectionEndedAt: old.Add(time.Minute),
		StartedAt: old, EndedAt: old.Add(time.Minute), CreatedAt: old,
	}}); err != nil {
		t.Fatal(err)
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
	if _, err := db.RunMaintenance(ctx, now); err != nil {
		t.Fatal(err)
	}
	var rawLeft int
	if err := db.db.QueryRowContext(ctx, `select count(*) from connection_audit_reports`).Scan(&rawLeft); err != nil {
		t.Fatal(err)
	}
	if rawLeft != 0 {
		t.Fatalf("the raw report survived a 1-day retention pass: %d rows", rawLeft)
	}

	totals, err := db.ConnectionAuditWindowTotalsFromRollup(ctx, []int64{user.ID}, now.Add(-30*24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	got := totals[user.ID]
	if got == nil {
		t.Fatal("the 30-day window lost its totals when the raw rows were purged")
	}
	if got.ConnectionCount != 12 || got.UploadBytes != 500 || got.DownloadBytes != 900 {
		t.Fatalf("totals after purge = %#v", got)
	}
	if got.SourceIPCount != 1 || got.ServerCount != 1 || got.RegionCount != 1 {
		t.Fatalf("distinct counts after purge = ips %d servers %d regions %d", got.SourceIPCount, got.ServerCount, got.RegionCount)
	}
}

// A window longer than raw retention must still answer, with exact totals from
// the rollup and an honest statement of how much of it the risk assessment
// actually saw reports for.
func TestLongWindowReportsRollupTotalsAndAShorterEvidenceWindow(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "long-window.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, ConnectionAuditRetentionDaysSetting, "2"); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "long-node", PublicIPv4: "203.0.113.90", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "long-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// One report inside the evidence window and one well outside it but inside
	// the requested window.
	reports := []model.ConnectionAuditReport{}
	for index, ended := range []time.Time{now.Add(-time.Hour), now.Add(-10 * 24 * time.Hour)} {
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: fmt.Sprintf("long-%d", index), ServerID: server.ID, UserID: user.ID,
			SourceIP: fmt.Sprintf("198.51.100.%d", 80+index), SourceCountryCode: []string{"SG", "DE"}[index],
			Network: "tcp", ConnectionCount: int64(10 * (index + 1)),
			UploadBytes: 100, DownloadBytes: 200, ActivePeak: 1, BucketCapacity: 2,
			PayloadFirstAt: ended.Add(-time.Minute), PayloadLastAt: ended,
			CollectionStartedAt: ended.Add(-time.Minute), CollectionEndedAt: ended,
			StartedAt: ended.Add(-time.Minute), EndedAt: ended, CreatedAt: ended,
		})
	}
	if _, err := db.AddConnectionAuditReportsResult(ctx, reports); err != nil {
		t.Fatal(err)
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

	overview, err := db.ConnectionAuditOverviewForUsers(ctx, 30*24, true, DefaultAuditPolicy(), []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if overview.WindowHours != 30*24 {
		t.Fatalf("WindowHours = %d, want the requested 720", overview.WindowHours)
	}
	if overview.EvidenceWindowHours != 48 {
		t.Fatalf("EvidenceWindowHours = %d, want the 48 hours reports are kept for", overview.EvidenceWindowHours)
	}
	if !overview.TotalsFromRollup {
		t.Fatal("a window longer than retention did not say its totals came from the rollup")
	}
	if len(overview.Users) != 1 {
		t.Fatalf("overview users = %d, want 1", len(overview.Users))
	}
	item := overview.Users[0]
	// Both reports count toward the totals even though only one is inside the
	// evidence window.
	if item.ConnectionCount != 30 {
		t.Fatalf("ConnectionCount = %d, want 30 across the whole requested window", item.ConnectionCount)
	}
	if item.ReportCount != 2 {
		t.Fatalf("ReportCount = %d, want both reports", item.ReportCount)
	}
	if item.SourceIPCount != 2 || item.SourceRegionCount != 2 {
		t.Fatalf("distinct counts = ips %d regions %d, want 2 and 2 across the window", item.SourceIPCount, item.SourceRegionCount)
	}

	// A window inside retention keeps the raw path and says so.
	short, err := db.ConnectionAuditOverviewForUsers(ctx, 24, true, DefaultAuditPolicy(), []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if short.TotalsFromRollup || short.EvidenceWindowHours != 24 {
		t.Fatalf("a 24h window used the rollup path: from_rollup=%v evidence=%d", short.TotalsFromRollup, short.EvidenceWindowHours)
	}
	if len(short.Users) != 1 || short.Users[0].ConnectionCount != 10 {
		t.Fatalf("24h totals = %#v, want only the recent report", short.Users)
	}
}
