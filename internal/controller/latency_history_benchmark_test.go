package controller

import (
	"context"
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

func BenchmarkLatencyChartService(b *testing.B) {
	count := 1440
	if raw := os.Getenv("OBOARD_CHART_BENCH_REPORTS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 43200 {
			b.Fatal("OBOARD_CHART_BENCH_REPORTS must be 1..43200")
		}
		count = n
	}
	db, err := store.Open(filepath.Join(b.TempDir(), "chart.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	node := &model.Server{Name: "benchmark"}
	if err := db.CreateServer(ctx, node); err != nil {
		b.Fatal(err)
	}
	if err := db.SetSetting(ctx, "server_monitoring_retention_days", "30"); err != nil {
		b.Fatal(err)
	}
	at := time.Now().UTC().Truncate(30 * time.Second)
	items := make([]model.LatencyProbeResult, 8)
	for i := range items {
		items[i] = model.LatencyProbeResult{ProbeID: fmt.Sprintf("probe-%d", i), Kind: "custom", TaskName: fmt.Sprintf("target-%d", i), Province: fmt.Sprintf("P%d", i), Carrier: "C", Mode: string(model.LatencyProbeModeTCP), Available: true, LatencyMS: int64(10 + i), SampleCount: 3, SuccessCount: 3}
	}
	for i := 0; i < count; i++ {
		report := model.LatencyProbeResultReport{ReportID: fmt.Sprint(i), ResourceVersion: "benchmark", CheckedAt: at.Add(-time.Duration(count-i) * time.Minute), Items: items}
		if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
			b.Fatal(err)
		}
	}
	server := newTestServer(db, "benchmark", "")
	server.latencyHistoryNow = func() time.Time { return at }
	principal := application.HumanPrincipal(model.User{ID: 1}, model.RoleAdmin, netip.Addr{})
	input := latencyChartInput{ServerID: node.ID, Window: "30d", MaxPoints: 360}
	b.Logf("fixture: 1 server, 8 targets, %d minute reports, %d rows; cache hit includes authorization-scope and metadata reads", count, count*8)
	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		before := server.historyReads().builds.Load()
		for b.Loop() {
			server.historyReads().clear()
			if _, err := server.readLatencyChart(ctx, principal, input); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(server.historyReads().builds.Load()-before)/float64(b.N), "history_builds/op")
	})
	if _, err := server.readLatencyChart(ctx, principal, input); err != nil {
		b.Fatal(err)
	}
	b.Run("hit", func(b *testing.B) {
		b.ReportAllocs()
		before := server.historyReads().builds.Load()
		for b.Loop() {
			if _, err := server.readLatencyChart(ctx, principal, input); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(server.historyReads().builds.Load()-before)/float64(b.N), "history_builds/op")
	})
}
