package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestSLACheckpointRoundTripPreservesEntireState(t *testing.T) {
	at := time.Now().UTC()
	original := connectivityState{probeEnabled: true, probeEnabledKnown: true, controllerConnected: false, controllerConnectionKnown: true, controllerUpdate: true, availability: connectivityUnavailable, cause: "probe_failed", lastProbeAt: &at, lastProbeLatency: 23, lastProbeError: "failure"}
	restored, err := decodeSLACheckpoint(encodeSLACheckpoint(original))
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("roundtrip=%+v %v", restored, err)
	}
	if _, err := decodeSLACheckpoint(json.RawMessage(`{"version":1,"availability":1}`)); err == nil {
		t.Fatal("incomplete checkpoint accepted")
	}
	if _, err := decodeSLACheckpoint(json.RawMessage(`{"version":2}`)); err == nil {
		t.Fatal("unknown checkpoint version accepted")
	}
}

func TestSLAProjectionMergeMatchesExistingStateMachine(t *testing.T) {
	base := time.Date(2026, 9, 10, 23, 0, 0, 0, time.UTC)
	initial := connectivityState{availability: connectivityUnavailable, probeEnabled: true, probeEnabledKnown: true}
	// Include zero-duration recovery exactly on a bucket boundary, maintenance
	// unknown time, probe disable/target change and a fault crossing midnight.
	events := []model.ServerConnectivityEvent{
		{Kind: model.ConnectivityEventControllerConnected, EffectiveAt: base.Add(5 * time.Minute)},
		{Kind: model.ConnectivityEventControllerDisconnected, EffectiveAt: base.Add(5 * time.Minute)},
		{Kind: model.ConnectivityEventControllerDisconnected, Source: model.ConnectivityEventSourceControllerUpdate, EffectiveAt: base.Add(10 * time.Minute)},
		{Kind: model.ConnectivityEventProbeDisabled, EffectiveAt: base.Add(12 * time.Minute)},
		{Kind: model.ConnectivityEventProbeTargetChanged, EffectiveAt: base.Add(13 * time.Minute)},
		{Kind: model.ConnectivityEventControllerConnected, EffectiveAt: base.Add(20 * time.Minute)},
		{Kind: model.ConnectivityEventControllerDisconnected, EffectiveAt: base.Add(30 * time.Minute)},
		{Kind: model.ConnectivityEventControllerConnected, EffectiveAt: base.Add(100 * time.Minute)},
	}
	work := store.SLAProjectionWork{From: base.Unix(), To: base.Add(2 * time.Hour).Unix(), Checkpoint: encodeSLACheckpoint(initial), Events: events}
	output, err := buildSLAProjection(work)
	if err != nil {
		t.Fatal(err)
	}
	merged := model.ConnectivitySLAStats{}
	for _, bucket := range output.Buckets {
		merged = model.MergeConnectivitySLAStats(merged, bucket.Stats)
	}
	segments, _ := buildConnectivitySegmentsFromState(base, base.Add(2*time.Hour), initial, events)
	online, offline, unknown := connectivityDurations(segments, base, base.Add(2*time.Hour))
	outages := buildConnectivityOutagesFromState(base, base.Add(2*time.Hour), initial, events)
	if merged.OnlineNS != int64(online) || merged.OfflineNS != int64(offline) || merged.UnknownNS != int64(unknown) || merged.OutageCount != len(outages) || merged.LongestNS != int64(70*time.Minute) {
		t.Fatalf("merged=%+v raw=%v %v %v %+v", merged, online, offline, unknown, outages)
	}
	left := model.MergeConnectivitySLAStats(model.MergeConnectivitySLAStats(output.Buckets[0].Stats, output.Buckets[1].Stats), output.Buckets[2].Stats)
	right := model.MergeConnectivitySLAStats(output.Buckets[0].Stats, model.MergeConnectivitySLAStats(output.Buckets[1].Stats, output.Buckets[2].Stats))
	if !reflect.DeepEqual(left, right) {
		t.Fatal("incident merge is not associative")
	}
}

func TestSLAProjectionLateEventsPropagatePastTemporaryConvergence(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "sla.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "sla", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(5 * time.Minute)
	for _, event := range []struct {
		minute    int
		connected bool
	}{{-1, true}, {10, false}, {60, true}, {140, true}} {
		if err := db.RecordControllerConnectionEvent(ctx, node.ID, event.connected, base.Add(time.Duration(event.minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.RunSLAProjectionBatch(ctx, base, 500, buildSLAProjection); err != nil {
		t.Fatal(err)
	}
	end := base.Add(3 * time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := db.RunSLAProjectionBatch(ctx, end, 500, buildSLAProjection); err != nil {
			t.Fatal(err)
		}
	}
	status, err := db.InspectSLAProjection(ctx, node.ID)
	if err != nil || status.Phase != "ready" {
		t.Fatalf("initial=%+v %v", status, err)
	}
	for _, event := range []struct {
		minute    int
		connected bool
	}{{20, true}, {100, false}} {
		if err := db.RecordControllerConnectionEvent(ctx, node.ID, event.connected, base.Add(time.Duration(event.minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.RunSLAProjectionBatch(ctx, end, 500, buildSLAProjection); err != nil {
		t.Fatal(err)
	}
	status, err = db.InspectSLAProjection(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "correcting" || !status.RepairNext.Valid || !status.DirtyUntil.Valid || status.RepairNext.Int64 >= status.DirtyUntil.Int64 {
		t.Fatalf("stopped before second late event: %+v", status)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.RunSLAProjectionBatch(ctx, end, 500, buildSLAProjection); err != nil {
			t.Fatal(err)
		}
	}
	status, _ = db.InspectSLAProjection(ctx, node.ID)
	if status.Phase != "ready" || status.DirtyFrom.Valid {
		t.Fatalf("did not converge: %+v", status)
	}
	buckets, err := db.InspectSLABuckets(ctx, node.ID, base.Unix(), end.Unix())
	if err != nil {
		t.Fatal(err)
	}
	merged := model.ConnectivitySLAStats{}
	for _, bucket := range buckets {
		merged = model.MergeConnectivitySLAStats(merged, bucket.Stats)
	}
	history, err := db.ListConnectivityHistory(ctx, node.ID, base, end)
	if err != nil {
		t.Fatal(err)
	}
	window := connectivityWindow{From: base, To: end, Duration: end.Sub(base), BucketDuration: 5 * time.Minute}
	reference := BuildConnectivityResponse(node.ID, window, history)
	if merged.OnlineNS != int64(reference.Summary.AvailableSeconds*float64(time.Second)) || merged.OfflineNS != int64(reference.Summary.UnavailableSeconds*float64(time.Second)) || merged.OutageCount != reference.Summary.OutageCount || merged.LongestNS != int64(reference.Summary.LongestOutageSecond*float64(time.Second)) {
		t.Fatalf("projection=%+v reference=%+v", merged, reference.Summary)
	}
}

func TestSLAProjectionRetentionSeedMatchesCoverageTime(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "coverage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "coverage"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(5 * time.Minute)
	for _, event := range []struct {
		hour      int
		connected bool
	}{{-1, true}, {12, false}, {36, true}} {
		if err := db.RecordControllerConnectionEvent(ctx, node.ID, event.connected, base.Add(time.Duration(event.hour)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.RunSLAProjectionBatch(ctx, base, 500, buildSLAProjection); err != nil {
		t.Fatal(err)
	}
	end := base.Add(48 * time.Hour)
	for i := 0; i < 48; i++ {
		if _, err := db.RunSLAProjectionBatch(ctx, end, 500, buildSLAProjection); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSetting(ctx, store.ServerMonitoringRetentionDaysSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunSLAProjectionBatch(ctx, end.Add(5*time.Minute), 500, buildSLAProjection); err != nil {
		t.Fatal(err)
	}
	status, err := db.InspectSLAProjection(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := decodeSLACheckpoint(status.Seed)
	if err != nil {
		t.Fatal(err)
	}
	if status.CoverageFrom != base.Add(24*time.Hour+5*time.Minute).Unix() || seed.availability != connectivityUnavailable {
		t.Fatalf("seed came from frontier instead of coverage start: %+v %+v", status, seed)
	}
}

func TestSLAProjectionMixedStateMatrixMatchesRawIncidents(t *testing.T) {
	base := time.Date(2026, 9, 10, 23, 0, 0, 0, time.UTC)
	end := base.Add(3 * time.Hour)
	kinds := []model.ConnectivityEventKind{model.ConnectivityEventProbeResult, model.ConnectivityEventProbeEnabled, model.ConnectivityEventProbeDisabled, model.ConnectivityEventProbeTargetChanged, model.ConnectivityEventControllerConnected, model.ConnectivityEventControllerDisconnected, model.ConnectivityEventServerOffline}
	events := make([]model.ServerConnectivityEvent, 0, 360)
	for i := 0; i < 360; i++ {
		at := base.Add(time.Duration(i/2) * time.Minute)
		if (i/2)%7 == 0 {
			at = at.Add(100 * time.Millisecond)
		}
		available := i%2 == 0
		source := "agent_socket"
		if i%3 == 0 {
			source = model.ConnectivityEventSourceControllerUpdate
		}
		events = append(events, model.ServerConnectivityEvent{ID: int64(i + 1), Kind: kinds[(i/2)%len(kinds)], Available: &available, LatencyMS: i + 1, Source: source, EffectiveAt: at})
	}
	for _, initial := range []connectivityState{
		{availability: connectivityUnknown},
		{availability: connectivityAvailable, controllerConnected: true, controllerConnectionKnown: true},
		{availability: connectivityUnavailable, controllerConnectionKnown: true},
		{availability: connectivityUnknown, controllerUpdate: true, probeEnabled: true, probeEnabledKnown: true},
	} {
		result, err := buildSLAProjection(store.SLAProjectionWork{From: base.Unix(), To: end.Unix(), Checkpoint: encodeSLACheckpoint(initial), Events: events})
		if err != nil {
			t.Fatal(err)
		}
		merged := model.ConnectivitySLAStats{}
		for _, bucket := range result.Buckets {
			merged = model.MergeConnectivitySLAStats(merged, bucket.Stats)
		}
		segments, final := buildConnectivitySegmentsFromState(base, end, initial, events)
		online, offline, unknown := connectivityDurations(segments, base, end)
		outages := buildConnectivityOutagesFromState(base, end, initial, events)
		var longest int64
		for _, outage := range outages {
			until := end
			if outage.EndedAt != nil {
				until = *outage.EndedAt
			}
			longest = max(longest, int64(until.Sub(outage.StartedAt)))
		}
		if merged.OnlineNS != int64(online) || merged.OfflineNS != int64(offline) || merged.UnknownNS != int64(unknown) || merged.OutageCount != len(outages) || merged.LongestNS != longest {
			t.Fatalf("matrix=%+v raw=%v %v %v count=%d longest=%d", merged, online, offline, unknown, len(outages), longest)
		}
		decoded, err := decodeSLACheckpoint(result.Buckets[len(result.Buckets)-1].Checkpoint)
		if err != nil || !reflect.DeepEqual(decoded, final) {
			t.Fatal("final checkpoint differs from raw state")
		}
	}
}

func TestConnectivityBaselineKeepsProbeThatEndsControllerMaintenance(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "baseline.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "baseline", LatencyProbeEnabled: true, AgentID: "baseline-agent", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(5 * time.Minute).Add(time.Hour)
	if err := db.RecordControllerConnectionEventWithSource(ctx, node.ID, false, base, model.ConnectivityEventSourceControllerUpdate); err != nil {
		t.Fatal(err)
	}
	report := model.LatencyProbeResultReport{ReportID: "after-maintenance", ResourceVersion: "test", CheckedAt: base.Add(time.Minute), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Available: true, LatencyMS: 10, SampleCount: 1, SuccessCount: 1}}}
	if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	// The public store method emits the same server_offline event as a real timeout.
	node.LastSeenAt = &base
	if err := db.UpdateServerRuntimeState(ctx, node); err != nil {
		t.Fatal(err)
	}
	if marked, err := db.MarkStaleServersOfflineEffective(ctx, base.Add(2*time.Minute), 2*time.Minute); err != nil || len(marked) != 1 {
		t.Fatalf("offline transition: %v %v", marked, err)
	}
	from, to := base.Add(5*time.Minute), base.Add(10*time.Minute)
	history, err := db.ListConnectivityHistory(ctx, node.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	state := connectivityState{availability: connectivityUnknown}
	for _, event := range history.Baseline {
		applyConnectivityEvent(&state, event)
	}
	if state.controllerUpdate || state.availability != connectivityUnavailable || state.lastProbeAt == nil {
		t.Fatalf("maintenance completion lost from baseline: %+v", state)
	}
	whole, err := db.ListConnectivityHistory(ctx, node.ID, base.Add(-time.Hour), to)
	if err != nil {
		t.Fatal(err)
	}
	full := connectivityState{availability: connectivityUnknown}
	for _, event := range append(whole.Baseline, whole.Events...) {
		applyConnectivityEvent(&full, event)
	}
	if !reflect.DeepEqual(full, state) {
		t.Fatalf("baseline differs from full replay: %+v / %+v", state, full)
	}
}
