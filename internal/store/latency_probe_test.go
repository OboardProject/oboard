package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestLatencyProbeSettingsAndResultsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "latency-probe.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{
		Name:                        "latency-edge",
		LatencyProbeEnabled:         true,
		LatencyProbeMode:            model.LatencyProbeModeICMP,
		LatencyProbePublicTarget:    model.ConnectivityProbeTarget12306,
		LatencyProbeIntervalSeconds: 600,
		LatencyProbeSampleCount:     5,
		LatencyProbeMaxTargets:      32,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LatencyProbeEnabled || stored.LatencyProbeIntervalSeconds != 600 || stored.LatencyProbeSampleCount != 5 || stored.LatencyProbeMaxTargets != 32 {
		t.Fatalf("settings = %#v", stored)
	}
	if stored.LatencyProbeMode != model.LatencyProbeModeICMP || stored.LatencyProbePublicTarget != model.ConnectivityProbeTarget12306 {
		t.Fatalf("normalized settings = %#v", stored)
	}
	stored.LatencyProbeEnabled = false
	stored.LatencyProbeIntervalSeconds = 900
	stored.LatencyProbeSampleCount = 4
	stored.LatencyProbeMode = model.LatencyProbeModeTCP
	stored.LatencyProbePublicTarget = model.ConnectivityProbeTargetCloudflare
	stored.LatencyProbeMaxTargets = 16
	if err := db.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	updated, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LatencyProbeEnabled || updated.LatencyProbeMode != model.LatencyProbeModeTCP || updated.LatencyProbePublicTarget != model.ConnectivityProbeTargetCloudflare || updated.LatencyProbeIntervalSeconds != 900 || updated.LatencyProbeSampleCount != 4 || updated.LatencyProbeMaxTargets != 16 {
		t.Fatalf("updated settings = %#v", updated)
	}

	checkedAt := time.Now().UTC().Truncate(time.Millisecond)
	report := model.LatencyProbeResultReport{ReportID: "report-1", ResourceVersion: "resource-v1", CheckedAt: checkedAt, Items: []model.LatencyProbeResult{{
		ProbeID: "广东-中国电信-0", Kind: "regional", Mode: "icmp", Province: "广东", Carrier: "中国电信", Host: "192.0.2.10", IP: "192.0.2.10", Available: true,
		LatencyMS: 23, MinLatencyMS: 18, P95LatencyMS: 29, JitterMS: 4, SampleCount: 5, SuccessCount: 5,
	}}}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListLatencyProbeResults(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ProbeID != report.Items[0].ProbeID || items[0].P95LatencyMS != 29 || !items[0].CheckedAt.Equal(checkedAt) {
		t.Fatalf("results = %#v", items)
	}
}

func TestRegionalLatencyPointsAggregateFullWindow(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "regional-latency.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "regional-history", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 600; index++ {
		checkedAt := from.Add(time.Duration(index) * time.Minute)
		ts := checkedAt.Format(time.RFC3339Nano)
		if _, err := tx.Exec(`insert into server_latency_probe_results(server_id,resource_version,probe_id,kind,mode,province,carrier,host,ip,port,available,latency_ms,sample_count,success_count,checked_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, server.ID, "regional-v1", "probe", "regional", "tcp", "广东", "中国电信", "192.0.2.1", "192.0.2.1", 443, 1, index+1, 3, 3, ts, ts); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	points, dataStart, err := db.ListRegionalLatencyPoints(ctx, server.ID, from, from.Add(600*time.Minute), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 300 {
		t.Fatalf("regional points = %d, want 300", len(points))
	}
	if !points[0].CheckedAt.Equal(from) || !points[len(points)-1].CheckedAt.Equal(from.Add(598*time.Minute)) {
		t.Fatalf("regional point bounds = %s..%s", points[0].CheckedAt, points[len(points)-1].CheckedAt)
	}
	if dataStart == nil || !dataStart.Equal(from) {
		t.Fatalf("data start = %v, want %s", dataStart, from)
	}
}

func TestRegionalLatencyPointsAverageSuccessfulTargets(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "regional-latency-average.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "regional-average", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	insertRegionalLatencyResult(t, db, server.ID, "earliest-failure", "浙江", "中国联通", false, 0, 0, from.Add(-time.Hour))
	insertRegionalLatencyResult(t, db, server.ID, "ip-1", "广东", "中国电信", true, 10, 3, from.Add(10*time.Second))
	insertRegionalLatencyResult(t, db, server.ID, "ip-2", "广东", "中国电信", true, 30, 3, from.Add(20*time.Second))
	insertRegionalLatencyResult(t, db, server.ID, "failed-ip", "广东", "中国电信", false, 0, 0, from.Add(30*time.Second))
	insertRegionalLatencyResult(t, db, server.ID, "all-failed", "四川", "中国移动", false, 0, 0, from.Add(40*time.Second))

	points, dataStart, err := db.ListRegionalLatencyPoints(ctx, server.ID, from, from.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("regional points = %#v, want one successful target", points)
	}
	point := points[0]
	if point.Kind != "regional" || !point.Available || point.Province != "广东" || point.Carrier != "中国电信" || point.LatencyMS != 20 || point.MinLatencyMS != 10 || point.MaxLatencyMS != 30 || point.Count != 2 {
		t.Fatalf("aggregated point = %#v", point)
	}
	if dataStart == nil || !dataStart.Equal(from.Add(-time.Hour)) {
		t.Fatalf("data start = %v, want %s", dataStart, from.Add(-time.Hour))
	}
}

func TestRegionalLatencyPointsRejectsUnboundedWindow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "regional-latency-bounds.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	from := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	if _, _, err := db.ListRegionalLatencyPoints(context.Background(), 1, from, from.Add(361*time.Minute), time.Minute); err == nil {
		t.Fatal("expected a bounded regional latency query error")
	}
}

func insertRegionalLatencyResult(t *testing.T, db *Store, serverID int64, probeID, province, carrier string, available bool, latencyMS int64, successCount int, checkedAt time.Time) {
	t.Helper()
	ts := checkedAt.UTC().Format(time.RFC3339Nano)
	if _, err := db.db.Exec(`insert into server_latency_probe_results(server_id,resource_version,probe_id,kind,mode,province,carrier,host,ip,port,available,latency_ms,sample_count,success_count,checked_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, serverID, "aggregate-v1", probeID, "regional", "tcp", province, carrier, "192.0.2.1", "192.0.2.1", 443, boolInt(available), latencyMS, 3, successCount, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func TestLatencyProbeTablesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "latency-probe-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "legacy-latency", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`update server_telemetry set connectivity_probe_enabled=1,connectivity_probe_target='12306' where server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	legacyCheckedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	for _, statement := range []string{
		`drop index idx_server_latency_probe_results_report_target`,
		`drop index idx_server_latency_probe_results_server_checked`,
		`alter table server_latency_probe_settings rename to server_latency_probe_settings_current`,
		`create table server_latency_probe_settings (server_id integer primary key references servers(id) on delete cascade, enabled integer not null default 1, interval_seconds integer not null default 300, sample_count integer not null default 3, provinces_json text not null default '[]', carriers_json text not null default '[]', max_targets integer not null default 64, resource_version text not null default '', updated_at text not null)`,
		`insert into server_latency_probe_settings(server_id,enabled,interval_seconds,sample_count,provinces_json,carriers_json,max_targets,resource_version,updated_at) values(` + fmt.Sprint(server.ID) + `,1,300,3,'["广东"]','["中国电信"]',64,'legacy-v1','` + legacyCheckedAt.Format(time.RFC3339Nano) + `')`,
		`drop table server_latency_probe_settings_current`,
		`alter table server_latency_probe_results rename to server_latency_probe_results_current`,
		`create table server_latency_probe_results (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, resource_version text not null, probe_id text not null, province text not null, carrier text not null, ip text not null, available integer not null default 0, latency_ms integer not null default 0, min_latency_ms integer not null default 0, p95_latency_ms integer not null default 0, jitter_ms integer not null default 0, sample_count integer not null default 0, success_count integer not null default 0, error text not null default '', checked_at text not null, created_at text not null, unique(server_id,resource_version,probe_id,checked_at))`,
		`insert into server_latency_probe_results(server_id,resource_version,probe_id,province,carrier,ip,available,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,checked_at,created_at) values(` + fmt.Sprint(server.ID) + `,'legacy-v1','legacy-probe','广东','中国电信','192.0.2.10',1,20,18,22,2,3,3,'','` + legacyCheckedAt.Format(time.RFC3339Nano) + `','` + legacyCheckedAt.Format(time.RFC3339Nano) + `')`,
		`drop table server_latency_probe_results_current`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatalf("prepare legacy latency schema: %v", err)
		}
	}
	if _, err := db.db.Exec(`delete from app_settings where key='migration.controller-db-20260813-unified-latency-probes'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"server_latency_probe_settings", "server_latency_probe_results"} {
		var name string
		if err := db.db.QueryRowContext(ctx, `select name from sqlite_master where type='table' and name=?`, table).Scan(&name); err != nil {
			if err == sql.ErrNoRows {
				t.Fatalf("table %s was not migrated", table)
			}
			t.Fatal(err)
		}
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LatencyProbeEnabled || stored.LatencyProbeMode != model.LatencyProbeModeTCP || stored.LatencyProbePublicTarget != model.ConnectivityProbeTarget12306 {
		t.Fatalf("migrated latency settings = %#v", stored)
	}
	results, err := db.ListLatencyProbeResults(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ProbeID != "legacy-probe" || results[0].LatencyMS != 20 {
		t.Fatalf("migrated latency results = %#v", results)
	}
}

func TestLatencyProbePublicResultDeduplicatesAndKeepsNewestCurrentState(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "latency-public.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "latency-public", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	connectedAt := time.Now().UTC().Add(-4 * time.Minute).Truncate(time.Millisecond)
	if err := db.RecordControllerConnectionEvent(ctx, server.ID, true, connectedAt); err != nil {
		t.Fatal(err)
	}
	newerAt := connectedAt.Add(time.Minute)
	newer := model.LatencyProbeResultReport{ReportID: "report-new", ResourceVersion: "resource-v2", CheckedAt: newerAt, Items: []model.LatencyProbeResult{{ProbeID: "public-cloudflare", Kind: "public", Mode: "tcp", Host: "cp.cloudflare.com", Port: 443, Available: false, SampleCount: 3, Error: "timeout"}}}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, newer); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, newer); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityStatus != "available" {
		t.Fatalf("connected server status = %q, want available", stored.ConnectivityStatus)
	}
	disconnectedAt := connectedAt.Add(2 * time.Minute)
	if err := db.RecordControllerConnectionEvent(ctx, server.ID, false, disconnectedAt); err != nil {
		t.Fatal(err)
	}
	older := model.LatencyProbeResultReport{ReportID: "report-old", ResourceVersion: "resource-v1", CheckedAt: connectedAt.Add(-time.Hour), Items: []model.LatencyProbeResult{{ProbeID: "public-12306", Kind: "public", Mode: "tcp", Host: "www.12306.cn", Port: 443, Available: true, LatencyMS: 25, MinLatencyMS: 20, P95LatencyMS: 30, SampleCount: 3, SuccessCount: 3}}}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, older); err != nil {
		t.Fatal(err)
	}
	offline := model.LatencyProbeResultReport{ReportID: "report-offline", ResourceVersion: "resource-v2", CheckedAt: disconnectedAt.Add(time.Minute), Items: []model.LatencyProbeResult{{ProbeID: "public-cloudflare", Kind: "public", Mode: "tcp", Host: "cp.cloudflare.com", Port: 443, Available: true, LatencyMS: 25, MinLatencyMS: 20, P95LatencyMS: 30, SampleCount: 3, SuccessCount: 3}}}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, offline); err != nil {
		t.Fatal(err)
	}
	var resultCount, eventCount int
	if err := db.db.QueryRowContext(ctx, `select count(*) from server_latency_probe_results where server_id=?`, server.ID).Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `select count(*) from server_connectivity_events where server_id=? and kind=?`, server.ID, model.ConnectivityEventProbeResult).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if resultCount != 3 || eventCount != 3 {
		t.Fatalf("dedupe counts results=%d events=%d", resultCount, eventCount)
	}
	stored, err = db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectivityStatus != "available" || stored.ConnectivityCheckedAt == nil || !stored.ConnectivityCheckedAt.Equal(offline.CheckedAt) || stored.ConnectivityLatencyMS != 25 || stored.ConnectivityError != "" {
		t.Fatalf("current latency state = %#v", stored)
	}
}
