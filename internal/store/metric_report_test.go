package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
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
	if samples[0].ConnectivityAvailable != nil || samples[1].ConnectivityAvailable != nil {
		t.Fatalf("metric reports must stay connectivity-empty without public probes: %#v", samples)
	}
}

func TestListServerMetricSamplesOverlaysPublicLatency(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "metric-latency-overlay.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "metric-latency", AgentID: "agent-metric-latency", Status: model.ServerUnknown, LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 26, 9, 0, 12, 0, time.UTC)
	for index := 0; index < 5; index++ {
		at := base.Add(time.Duration(index) * time.Minute)
		report := model.MetricReport{ReportID: "metric-" + at.Format("150405"), SampledAt: at, CPUUsagePercent: float64(10 + index), MemoryUsedBytes: 200, MemoryTotalBytes: 1000, NetworkDownloadBPS: 1000}
		if inserted, err := db.SaveMetricReport(ctx, server.ID, report); err != nil || !inserted {
			t.Fatalf("save metric %d inserted=%v err=%v", index, inserted, err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `insert into server_metric_samples(server_id,cpu_usage_percent,memory_used_bytes,memory_total_bytes,resource_recorded,network_upload_bps,network_download_bps,connectivity_available,connectivity_latency_ms,sampled_at) values(?,?,?,?,1,0,0,1,10,?)`, server.ID, 99, 200, 1000, base.Add(5*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 6; index++ {
		at := base.Add(time.Duration(index)*time.Minute + 40*time.Second)
		latency := int64(20 + index*5)
		available := index != 3
		if index == 3 {
			latency = 0
		}
		if index == 1 {
			if err := db.SaveLatencyProbeResults(ctx, server.ID, model.LatencyProbeResultReport{ReportID: "early-1", ResourceVersion: "resource-v1", CheckedAt: at.Add(-20 * time.Second), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Mode: "tcp", Available: true, LatencyMS: 1}}}); err != nil {
				t.Fatal(err)
			}
		}
		report := model.LatencyProbeResultReport{ReportID: "probe-" + at.Format("150405"), ResourceVersion: "resource-v1", CheckedAt: at, Items: []model.LatencyProbeResult{
			{ProbeID: "public", Kind: "public", Mode: "tcp", Available: available, LatencyMS: latency},
			{ProbeID: "广东-电信", Kind: "regional", Mode: "tcp", Province: "广东", Carrier: "电信", Available: true, LatencyMS: 80},
		}}
		if err := db.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
			t.Fatal(err)
		}
	}
	if inserted, err := db.SaveMetricReport(ctx, server.ID, model.MetricReport{ReportID: "no-probe", SampledAt: base.Add(20 * time.Minute), CPUUsagePercent: 8, MemoryUsedBytes: 200, MemoryTotalBytes: 1000}); err != nil || !inserted {
		t.Fatalf("save unmatched metric inserted=%v err=%v", inserted, err)
	}

	samples, err := db.ListServerMetricSamples(ctx, server.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 7 {
		t.Fatalf("samples=%d want 7", len(samples))
	}
	want := []struct {
		available *bool
		latency   int64
	}{
		{boolPointer(true), 20},
		{boolPointer(true), 25},
		{boolPointer(true), 30},
		{boolPointer(false), 0},
		{boolPointer(true), 40},
		{boolPointer(true), 10},
		{nil, 0},
	}
	for index, sample := range samples {
		if want[index].available == nil {
			if sample.ConnectivityAvailable != nil || sample.ConnectivityLatencyMS != 0 {
				t.Fatalf("sample %d should stay empty: %#v", index, sample)
			}
			continue
		}
		if sample.ConnectivityAvailable == nil || *sample.ConnectivityAvailable != *want[index].available || sample.ConnectivityLatencyMS != want[index].latency {
			t.Fatalf("sample %d overlay = available=%v latency=%d, want available=%v latency=%d", index, sample.ConnectivityAvailable, sample.ConnectivityLatencyMS, *want[index].available, want[index].latency)
		}
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestListServerMetricSamplesFleetKeepsLatestPerServer(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "fleet-metrics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	var servers []int64
	for index, count := range []int{0, 3, 75, 140} {
		server := &model.Server{Name: fmt.Sprintf("fleet-%d", index)}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server.ID)
		for sample := count - 1; sample >= 0; sample-- {
			at := base.Add(time.Duration(sample-index*1000) * time.Minute)
			if _, err := db.SaveMetricReport(ctx, server.ID, model.MetricReport{ReportID: fmt.Sprintf("%d-%d", index, sample), SampledAt: at, CPUUsagePercent: float64(sample)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, limit := range []int{1, 60, 120, 0, 2881} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			want := []model.ServerMetricSample{}
			for _, id := range servers {
				samples, err := db.ListServerMetricSamples(ctx, id, limit)
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, samples...)
			}
			got, err := db.ListServerMetricSamples(ctx, 0, limit)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fleet samples differ from individual histories: got %d, want %d", len(got), len(want))
			}
		})
	}
}
