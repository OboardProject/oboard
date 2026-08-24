package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestSaveMetricReportIsOutOfOrderIdempotentAndHistoricalOnly(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "metric-report.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "metric-report", AgentID: "agent-metric-report", Status: model.ServerUnknown}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 23, 8, 0, 17, 123, time.UTC)
	later := model.MetricReport{ReportID: "later", SampledAt: base.Add(10 * time.Minute), CPUUsagePercent: 70, MemoryUsedBytes: 700, MemoryTotalBytes: 1000, NetworkDownloadBPS: 2000}
	earlier := model.MetricReport{ReportID: "earlier", SampledAt: base, CPUUsagePercent: 20, MemoryUsedBytes: 200, MemoryTotalBytes: 1000, NetworkDownloadBPS: 1000}
	if inserted, err := db.SaveMetricReport(ctx, server.ID, later); err != nil || !inserted {
		t.Fatalf("save later inserted=%v err=%v", inserted, err)
	}
	if inserted, err := db.SaveMetricReport(ctx, server.ID, earlier); err != nil || !inserted {
		t.Fatalf("save earlier inserted=%v err=%v", inserted, err)
	}
	if inserted, err := db.SaveMetricReport(ctx, server.ID, earlier); err != nil || inserted {
		t.Fatalf("duplicate inserted=%v err=%v", inserted, err)
	}

	samples, err := db.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil || len(samples) != 2 {
		t.Fatalf("samples=%#v err=%v", samples, err)
	}
	if samples[0].SampledAt != earlier.SampledAt || samples[1].SampledAt != later.SampledAt || samples[0].CPUUsagePercent != 20 {
		t.Fatalf("out-of-order samples = %#v", samples)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.ServerUnknown || stored.CPUUsagePercent != 0 || stored.LastSeenAt != nil || stored.TrafficUploadBytes != 0 || stored.TrafficDownloadBytes != 0 {
		t.Fatalf("historical report changed live state: %#v", stored)
	}
}
