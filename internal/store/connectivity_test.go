package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func newConnectivityTestStore(t *testing.T) (*Store, *model.Server) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &model.Server{Name: "connectivity-node", AgentID: "connectivity-agent", Status: model.ServerOnline, ConnectivityProbeEnabled: true, OfflineNotifyEnabled: true}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	return db, server
}

func connectivityEventCount(t *testing.T, db *Store, serverID int64, kind model.ConnectivityEventKind) int {
	t.Helper()
	var count int
	if err := db.db.QueryRow(`select count(*) from server_connectivity_events where server_id=? and kind=?`, serverID, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func telemetryWindow(at time.Time) model.ServerTrafficWindow {
	return model.ServerTrafficWindow{Key: "2026-08", Start: at.Add(-time.Hour), End: at.Add(time.Hour)}
}

func TestConnectivityProbeEventsTrackRealProbesOnly(t *testing.T) {
	ctx := context.Background()
	db, server := newConnectivityTestStore(t)
	checkedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	report := model.HealthReport{ConnectivityProbeEnabled: true, ConnectivityAvailable: true, ConnectivityLatencyMS: 23, ConnectivityCheckedAt: checkedAt, Timestamp: checkedAt}
	for range 6 {
		if err := db.UpdateServerTelemetryReport(ctx, server.ID, report, telemetryWindow(checkedAt)); err != nil {
			t.Fatal(err)
		}
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeResult); got != 1 {
		t.Fatalf("probe events after repeated heartbeat = %d, want 1", got)
	}
	report.ConnectivityCheckedAt = checkedAt.Add(time.Minute)
	report.Timestamp = report.ConnectivityCheckedAt
	if err := db.UpdateServerTelemetryReport(ctx, server.ID, report, telemetryWindow(report.Timestamp)); err != nil {
		t.Fatal(err)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeResult); got != 2 {
		t.Fatalf("probe events after fresh probe = %d, want 2", got)
	}
	report.ConnectivityCheckedAt = checkedAt.Add(-time.Minute)
	report.Timestamp = checkedAt.Add(2 * time.Minute)
	if err := db.UpdateServerTelemetryReport(ctx, server.ID, report, telemetryWindow(report.Timestamp)); err != nil {
		t.Fatal(err)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeResult); got != 2 {
		t.Fatalf("probe events after stale probe = %d, want 2", got)
	}
}

func TestConnectivityProbeTargetPersists(t *testing.T) {
	ctx := context.Background()
	db, server := newConnectivityTestStore(t)
	checkedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	report := model.HealthReport{ConnectivityProbeEnabled: true, ConnectivityAvailable: true, ConnectivityLatencyMS: 23, ConnectivityCheckedAt: checkedAt, Timestamp: checkedAt}
	if err := db.UpdateServerTelemetryReport(ctx, server.ID, report, telemetryWindow(checkedAt)); err != nil {
		t.Fatal(err)
	}
	initialBoundaries := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeTargetChanged)
	server.ConnectivityProbeTarget = model.ConnectivityProbeTargetGoogle
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityProbeTarget != model.ConnectivityProbeTargetGoogle {
		t.Fatalf("connectivity probe target = %q, want google", stored.ConnectivityProbeTarget)
	}
	if stored.ConnectivityStatus != "pending" || stored.ConnectivityCheckedAt != nil {
		t.Fatalf("target change retained old connectivity state: status=%q checked_at=%v", stored.ConnectivityStatus, stored.ConnectivityCheckedAt)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeTargetChanged); got != initialBoundaries+1 {
		t.Fatalf("target change boundaries = %d, want %d", got, initialBoundaries+1)
	}
	var source string
	if err := db.db.QueryRowContext(ctx, `select source from server_connectivity_events where server_id=? and kind=? order by id desc limit 1`, server.ID, model.ConnectivityEventProbeTargetChanged).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "target_change" {
		t.Fatalf("target change boundary source = %q", source)
	}
}

func TestConnectivityProbeTargetMigratesFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "migration-node", AgentID: "migration-agent", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`alter table server_telemetry drop column connectivity_probe_target`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("open with previous telemetry schema: %v", err)
	}
	defer db.Close()
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatalf("list servers after migration: %v", err)
	}
	if len(servers) != 1 || servers[0].ConnectivityProbeTarget != model.ConnectivityProbeTargetAuto {
		t.Fatalf("migrated servers = %#v", servers)
	}
	servers[0].ConnectivityProbeTarget = model.ConnectivityProbeTargetGoogle
	if err := db.UpdateServer(ctx, &servers[0]); err != nil {
		t.Fatalf("update migrated probe target: %v", err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityProbeTarget != model.ConnectivityProbeTargetGoogle {
		t.Fatalf("connectivity probe target = %q, want google", stored.ConnectivityProbeTarget)
	}
}

func TestConnectivityProbeRetentionKeepsBoundaryEvents(t *testing.T) {
	ctx := context.Background()
	db, server := newConnectivityTestStore(t)
	now := time.Now().UTC()
	available := true
	for _, event := range []model.ServerConnectivityEvent{
		{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, Available: &available, EffectiveAt: now.Add(-36 * 24 * time.Hour), EventKey: "old-probe"},
		{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, Available: &available, EffectiveAt: now.Add(-34 * 24 * time.Hour), EventKey: "new-probe"},
		{ServerID: server.ID, Kind: model.ConnectivityEventServerOffline, EffectiveAt: now.Add(-60 * 24 * time.Hour), EventKey: "old-offline"},
		{ServerID: server.ID, Kind: model.ConnectivityEventProbeDisabled, EffectiveAt: now.Add(-60 * 24 * time.Hour), EventKey: "old-disabled"},
	} {
		if _, err := insertConnectivityEvent(ctx, db.db, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CleanupOldConnectivityProbeEvents(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeResult); got != 1 {
		t.Fatalf("retained probe events = %d, want 1", got)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventServerOffline); got != 1 {
		t.Fatalf("offline events = %d, want 1", got)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeDisabled); got != 1 {
		t.Fatalf("disabled events = %d, want 1", got)
	}
}

func TestConnectivityOfflineEventUsesEffectiveThreshold(t *testing.T) {
	for _, test := range []struct {
		name          string
		customSeconds int
		defaultAfter  time.Duration
		wantAfter     time.Duration
	}{
		{name: "default", defaultAfter: 2 * time.Minute, wantAfter: 2 * time.Minute},
		{name: "custom", customSeconds: 300, defaultAfter: 2 * time.Minute, wantAfter: 5 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, server := newConnectivityTestStore(t)
			server.OfflineAfterSeconds = test.customSeconds
			lastSeen := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
			if _, err := db.db.ExecContext(ctx, `update servers set status='online',last_seen_at=? where id=?`, lastSeen.Format(time.RFC3339Nano), server.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.ExecContext(ctx, `update server_telemetry set offline_after_seconds=? where server_id=?`, test.customSeconds, server.ID); err != nil {
				t.Fatal(err)
			}
			pollAt := lastSeen.Add(10 * time.Minute)
			marked, err := db.MarkStaleServersOfflineEffective(ctx, pollAt, test.defaultAfter)
			if err != nil {
				t.Fatal(err)
			}
			if len(marked) != 1 {
				t.Fatalf("marked servers = %d, want 1", len(marked))
			}
			var effective string
			if err := db.db.QueryRowContext(ctx, `select effective_at from server_connectivity_events where server_id=? and kind=?`, server.ID, model.ConnectivityEventServerOffline).Scan(&effective); err != nil {
				t.Fatal(err)
			}
			if got, want := parseTime(effective), lastSeen.Add(test.wantAfter); !got.Equal(want) {
				t.Fatalf("offline effective_at = %s, want %s (poll was %s)", got, want, pollAt)
			}
		})
	}
}

func TestConnectivitySettingEventsRecordTransitionsOnly(t *testing.T) {
	ctx := context.Background()
	db, server := newConnectivityTestStore(t)
	initial := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeEnabled)
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeEnabled); got != initial {
		t.Fatalf("unchanged update added enabled event: %d -> %d", initial, got)
	}
	server.ConnectivityProbeEnabled = false
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	server.ConnectivityProbeEnabled = true
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeDisabled); got != 1 {
		t.Fatalf("disabled transitions = %d, want 1", got)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeEnabled); got != initial+1 {
		t.Fatalf("enabled transitions = %d, want %d", got, initial+1)
	}
}

func TestConnectivityTelemetryDeleteAndBootstrapIdempotency(t *testing.T) {
	ctx := context.Background()
	db, server := newConnectivityTestStore(t)
	if _, err := db.db.ExecContext(ctx, `delete from server_connectivity_events where server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	migrationAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.SeedConnectivityHistory(ctx, migrationAt); err != nil {
		t.Fatal(err)
	}
	var first int
	if err := db.db.QueryRowContext(ctx, `select count(*) from server_connectivity_events where server_id=?`, server.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedConnectivityHistory(ctx, migrationAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var second int
	if err := db.db.QueryRowContext(ctx, `select count(*) from server_connectivity_events where server_id=?`, server.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first == 0 || second != first {
		t.Fatalf("bootstrap event counts = %d then %d", first, second)
	}
	if err := db.DeleteServerTelemetry(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.db.QueryRowContext(ctx, `select count(*) from server_connectivity_events where server_id=?`, server.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("connectivity events after telemetry delete = %d, want 0", remaining)
	}
}
