package controller

import (
	"context"
	"github.com/OboardProject/oboard/internal/application"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestSLAReadStitchesOutagesAndRawEdgesInSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "read.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "read"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test", "")
	defer app.Close()
	base := time.Now().UTC().Truncate(5 * time.Minute).Add(5 * time.Minute)
	for _, x := range []struct {
		minute    int
		connected bool
	}{{-1, true}, {10, false}, {80, true}} {
		if err := db.RecordControllerConnectionEvent(ctx, node.ID, x.connected, base.Add(time.Duration(x.minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	from, to := base.Add(31*time.Second), base.Add(2*time.Hour+17*time.Second)
	window := connectivityWindow{From: from, To: to, Duration: to.Sub(from), BucketDuration: time.Hour, BucketSeconds: 3600}
	raw, err := app.readSummarizedSLA(ctx, node.ID, 7, window)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunSLAProjectionBatch(ctx, base, 500, buildSLAProjection); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := db.RunSLAProjectionBatch(ctx, base.Add(2*time.Hour), 500, buildSLAProjection); err != nil {
			t.Fatal(err)
		}
	}
	projected, err := app.readSummarizedSLA(ctx, node.ID, 7, window)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Metadata.Source != "mixed" || !reflect.DeepEqual(raw.Summary, projected.Summary) || !reflect.DeepEqual(raw.Outages, projected.Outages) {
		t.Fatalf("raw=%+v\nprojected=%+v", raw, projected)
	}
	if projected.Summary.OutageCount != 1 || projected.Summary.LongestOutageSecond != 4200 {
		t.Fatalf("cross-boundary incident=%+v", projected.Summary)
	}
	if err := db.RecordControllerConnectionEvent(ctx, node.ID, true, base.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	late, err := app.readSummarizedSLA(ctx, node.ID, 7, window)
	if err != nil {
		t.Fatal(err)
	}
	if late.Metadata.Source != "raw_bounded" || late.Summary.LongestOutageSecond != 600 {
		t.Fatalf("unprocessed late state used stale summary: %+v", late)
	}
}

func TestMeasurementRevisionIgnoresRenameAndTracksURLAndFamily(t *testing.T) {
	server := model.Server{LatencyProbeMode: model.LatencyProbeModeTCP, LatencyProbeMaxTargets: 8, IPStack: "auto", PublicIPv4: "192.0.2.1"}
	task := model.LatencyProbeTask{ID: 1, Name: "before", Enabled: true, Method: model.LatencyProbeModeHTTP, Address: "https://example.net/a", Port: 443}
	before := latencyProbeTargets(latencyProbeResource{}, server, []model.LatencyProbeTask{task})
	task.Name = "after"
	after := latencyProbeTargets(latencyProbeResource{}, server, []model.LatencyProbeTask{task})
	if before[1].MeasurementRevision == "" || before[1].MeasurementRevision != after[1].MeasurementRevision {
		t.Fatal("rename changed measurement identity")
	}
	task.Address = "https://example.net/b"
	changed := latencyProbeTargets(latencyProbeResource{}, server, []model.LatencyProbeTask{task})
	if changed[1].MeasurementRevision == after[1].MeasurementRevision {
		t.Fatal("HTTP path change did not revise measurement")
	}
	server.IPStack = "ipv4_only"
	family := latencyProbeTargets(latencyProbeResource{}, server, []model.LatencyProbeTask{task})
	if family[1].MeasurementRevision == changed[1].MeasurementRevision {
		t.Fatal("family policy change did not revise measurement")
	}
}

func TestLatencyChartTargetFilterContract(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "filter.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "filter"}
	if err = db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		task := &model.LatencyProbeTask{Name: name, Method: model.LatencyProbeModeTCP, Address: "example.com", Port: 443, Enabled: true, IntervalSeconds: 120, ServerIDs: []int64{node.ID}}
		if err := db.SaveLatencyProbeTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC().Truncate(time.Minute)
	report := model.LatencyProbeResultReport{ReportID: "filter", ResourceVersion: "test", CheckedAt: at.Add(-time.Minute), Items: []model.LatencyProbeResult{
		{ProbeID: "public", Kind: "public", Available: true, LatencyMS: 10, SampleCount: 1, SuccessCount: 1},
		{ProbeID: "one", Kind: "custom", TaskID: 1, Available: true, LatencyMS: 20, SampleCount: 1, SuccessCount: 1},
		{ProbeID: "two", Kind: "custom", TaskID: 2, Available: true, LatencyMS: 30, SampleCount: 1, SuccessCount: 1},
	}}
	if err = db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "secret", "")
	defer app.Close()
	app.latencyHistoryNow = func() time.Time { return at }
	principal := application.HumanPrincipal(model.User{ID: 1}, model.RoleAdmin, netip.Addr{})
	input, err := latencyChartRequest(httptest.NewRequest("GET", "/?window=24h&target_ids=2", nil), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"0", "1"} {
		t.Setenv("OBOARD_LATENCY_ROLLUP_READ", enabled)
		result, err := app.readLatencyChart(ctx, principal, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.ProbeTargetStats) != 2 {
			t.Fatalf("filter result: %+v", result.ProbeTargetStats)
		}
		for _, stat := range result.ProbeTargetStats {
			if stat.Kind != "public" && stat.TaskID != 2 {
				t.Fatal("unselected target returned")
			}
		}
	}
	input.TargetIDs = []int64{2, 2}
	if _, err := app.readLatencyChart(ctx, principal, input); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
}
