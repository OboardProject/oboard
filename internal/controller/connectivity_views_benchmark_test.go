package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func BenchmarkConnectivityViews(b *testing.B) {
	count := 1440
	if value := os.Getenv("OBOARD_CONNECTIVITY_BENCH_REPORTS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 43200 {
			b.Fatal("OBOARD_CONNECTIVITY_BENCH_REPORTS must be 1..43200")
		}
		count = n
	}
	ctx := context.Background()
	db, err := store.Open(filepath.Join(b.TempDir(), "views.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "views-benchmark", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, node); err != nil {
		b.Fatal(err)
	}
	if err := db.SetSetting(ctx, "server_monitoring_retention_days", "30"); err != nil {
		b.Fatal(err)
	}
	now := time.Now().UTC().Truncate(30 * time.Second)
	// Current connection is outside the historical window; avoid benchmarking
	// unrelated latest-connection lookup work while generating the fixture.
	if err := db.RecordControllerConnectionEvent(ctx, node.ID, true, now); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < count; i++ {
		report := model.LatencyProbeResultReport{ReportID: fmt.Sprint(i), ResourceVersion: "test", CheckedAt: now.Add(-time.Duration(count-i) * time.Minute), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Available: i%60 != 0, LatencyMS: int64(i%100 + 1), SampleCount: 3, SuccessCount: 3}}}
		if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
			b.Fatal(err)
		}
	}
	app := newTestServer(db, "benchmark", "")
	defer app.Close()
	app.latencyHistoryNow = func() time.Time { return now }
	principal := application.HumanPrincipal(model.User{ID: 1}, model.RoleAdmin, netip.Addr{})
	input := connectivityViewInput{ServerID: node.ID, Window: "30d", Limit: 100}
	window, _ := parseConnectivityWindow("30d", now)
	b.Logf("1 server, 1 public target, %d minute reports; no successful result retention in any measured path", count)
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			result, err := app.buildFullConnectivity(ctx, node.ID, window)
			if err != nil {
				b.Fatal(err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(raw)), "response_bytes")
		}
	})
	for _, view := range []string{"sla", "events"} {
		b.Run(view, func(b *testing.B) {
			request := input
			if view == "sla" {
				request.Limit = 0
			}
			b.ReportAllocs()
			for b.Loop() {
				raw, err := app.readConnectivityDetails(ctx, principal, view, request)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(raw)), "response_bytes")
			}
		})
	}
}
