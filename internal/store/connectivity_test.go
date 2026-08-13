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
	return newConnectivityTestStoreAtPath(t, filepath.Join(t.TempDir(), "oboard.sqlite"))
}

func newConnectivityTestStoreAtPath(t *testing.T, path string) (*Store, *model.Server) {
	t.Helper()
	db, err := Open(path)
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
	server.LatencyProbeEnabled = true
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	checkedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	report := model.LatencyProbeResultReport{ReportID: "target-before-change", ResourceVersion: "resource-v1", CheckedAt: checkedAt, Items: []model.LatencyProbeResult{{ProbeID: "public-auto", Kind: "public", Mode: "tcp", Host: "cp.cloudflare.com", Port: 443, Available: true, LatencyMS: 23, SampleCount: 3, SuccessCount: 3}}}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	initialBoundaries := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeTargetChanged)
	server.LatencyProbePublicTarget = model.ConnectivityProbeTargetGoogle
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LatencyProbePublicTarget != model.ConnectivityProbeTargetGoogle {
		t.Fatalf("latency probe public target = %q, want google", stored.LatencyProbePublicTarget)
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
	if _, err := raw.Exec(`delete from app_settings where key='migration.controller-db-20260813-unified-latency-probes'`); err != nil {
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
	if len(servers) != 1 {
		t.Fatalf("migrated servers = %#v", servers)
	}
	var legacyTarget string
	if err := db.db.QueryRowContext(ctx, `select connectivity_probe_target from server_telemetry where server_id=?`, server.ID).Scan(&legacyTarget); err != nil {
		t.Fatal(err)
	}
	if legacyTarget != string(model.ConnectivityProbeTargetAuto) {
		t.Fatalf("restored legacy probe target column = %q, want auto", legacyTarget)
	}
	servers[0].LatencyProbePublicTarget = model.ConnectivityProbeTargetGoogle
	if err := db.UpdateServer(ctx, &servers[0]); err != nil {
		t.Fatalf("update unified latency probe target: %v", err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LatencyProbePublicTarget != model.ConnectivityProbeTargetGoogle {
		t.Fatalf("latency probe public target = %q, want google", stored.LatencyProbePublicTarget)
	}
}

func TestConnectivityProbeTargetEventConstraintUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, server := newConnectivityTestStoreAtPath(t, path)
	server.LatencyProbeEnabled = true
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	initialEvents := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeEnabled)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`drop index idx_server_connectivity_events_server_time`,
		`drop index idx_server_connectivity_events_server_kind_time`,
		`create table server_connectivity_events_v1 (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, kind text not null check(kind in ('probe_result','server_offline','probe_enabled','probe_disabled')), available integer check(available is null or available in (0,1)), latency_ms integer not null default 0, error text not null default '', source text not null default '', effective_at text not null, event_key text not null, created_at text not null, unique(server_id,event_key))`,
		`insert into server_connectivity_events_v1 select * from server_connectivity_events`,
		`drop table server_connectivity_events`,
		`alter table server_connectivity_events_v1 rename to server_connectivity_events`,
		`create index idx_server_connectivity_events_server_time on server_connectivity_events(server_id,effective_at)`,
		`create index idx_server_connectivity_events_server_kind_time on server_connectivity_events(server_id,kind,effective_at desc)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare old connectivity event schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("open with previous connectivity event constraint: %v", err)
	}
	defer db.Close()
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.LatencyProbePublicTarget = model.ConnectivityProbeTargetGoogle
	if err := db.UpdateServer(ctx, stored); err != nil {
		t.Fatalf("update target after constraint migration: %v", err)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeEnabled); got != initialEvents {
		t.Fatalf("enabled events after migration = %d, want %d", got, initialEvents)
	}
	if got := connectivityEventCount(t, db, server.ID, model.ConnectivityEventProbeTargetChanged); got != 1 {
		t.Fatalf("target change events after migration = %d, want 1", got)
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
