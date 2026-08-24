package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestProcessMetricReportAcknowledgesOnlyAcceptedHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "metric-report.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "metric-report", AgentID: "agent-metric-report", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")
	report := model.MetricReport{ReportID: "metric-1", SampledAt: time.Now().UTC().Add(-time.Minute), CPUUsagePercent: 25, MemoryUsedBytes: 250, MemoryTotalBytes: 1000}
	raw, _ := json.Marshal(report)
	latencyID, metricID := app.processAgentSocketMessage(ctx, stored, map[string]json.RawMessage{"metric_report": raw}, "192.0.2.1")
	if latencyID != "" || metricID != report.ReportID {
		t.Fatalf("acks latency=%q metric=%q", latencyID, metricID)
	}
	_, repeatedID := app.processAgentSocketMessage(ctx, stored, map[string]json.RawMessage{"metric_report": raw}, "192.0.2.1")
	if repeatedID != report.ReportID {
		t.Fatalf("idempotent replay ack=%q", repeatedID)
	}
	samples, err := db.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil || len(samples) != 1 {
		t.Fatalf("samples=%#v err=%v", samples, err)
	}

	report.ReportID = ""
	invalidRaw, _ := json.Marshal(report)
	_, invalidID := app.processAgentSocketMessage(ctx, stored, map[string]json.RawMessage{"metric_report": invalidRaw}, "192.0.2.1")
	if invalidID != "" {
		t.Fatalf("invalid report was acknowledged: %q", invalidID)
	}
}
