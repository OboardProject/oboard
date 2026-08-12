package store

import (
	"context"
	"database/sql"
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
		LatencyProbeIntervalSeconds: 600,
		LatencyProbeSampleCount:     5,
		LatencyProbeProvinces:       []string{"广东", "广东", " 浙江 "},
		LatencyProbeCarriers:        []string{"中国电信"},
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
	if len(stored.LatencyProbeProvinces) != 2 || stored.LatencyProbeProvinces[0] != "广东" || stored.LatencyProbeProvinces[1] != "浙江" {
		t.Fatalf("normalized provinces = %#v", stored.LatencyProbeProvinces)
	}
	stored.LatencyProbeEnabled = false
	stored.LatencyProbeIntervalSeconds = 900
	stored.LatencyProbeSampleCount = 4
	stored.LatencyProbeProvinces = []string{"四川"}
	stored.LatencyProbeCarriers = []string{"中国联通"}
	stored.LatencyProbeMaxTargets = 16
	if err := db.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	updated, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LatencyProbeEnabled || updated.LatencyProbeIntervalSeconds != 900 || updated.LatencyProbeSampleCount != 4 || updated.LatencyProbeMaxTargets != 16 || len(updated.LatencyProbeProvinces) != 1 || updated.LatencyProbeProvinces[0] != "四川" || len(updated.LatencyProbeCarriers) != 1 || updated.LatencyProbeCarriers[0] != "中国联通" {
		t.Fatalf("updated settings = %#v", updated)
	}

	checkedAt := time.Now().UTC().Truncate(time.Millisecond)
	report := model.LatencyProbeResultReport{ResourceVersion: "resource-v1", CheckedAt: checkedAt, Items: []model.LatencyProbeResult{{
		ProbeID: "广东-中国电信-0", Province: "广东", Carrier: "中国电信", IP: "192.0.2.10", Available: true,
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

func TestLatencyProbeTablesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "latency-probe-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`drop table server_latency_probe_results; drop table server_latency_probe_settings`); err != nil {
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
}
