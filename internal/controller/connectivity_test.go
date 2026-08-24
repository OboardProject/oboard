package controller

import (
	"context"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func connectivityTestWindow(from time.Time, duration, bucket time.Duration) connectivityWindow {
	return connectivityWindow{Key: "test", Duration: duration, BucketDuration: bucket, From: from.UTC(), To: from.UTC().Add(duration), BucketSeconds: int64(bucket / time.Second)}
}

func connectivityTestEvent(id int64, at time.Time, kind model.ConnectivityEventKind, available *bool, latency int) model.ServerConnectivityEvent {
	return model.ServerConnectivityEvent{ID: id, ServerID: 1, Kind: kind, Available: available, LatencyMS: latency, EffectiveAt: at.UTC()}
}

func boolPointer(value bool) *bool { return &value }

func assertNear(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("got %.6f, want %.6f (+/- %.6f)", got, want, tolerance)
	}
}

func TestConnectivitySLADurationIntegration(t *testing.T) {
	from := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	window := connectivityTestWindow(from, time.Hour, 15*time.Minute)
	t.Run("all available", func(t *testing.T) {
		history := model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{
			connectivityTestEvent(1, from, model.ConnectivityEventProbeEnabled, nil, 0),
			connectivityTestEvent(2, from, model.ConnectivityEventProbeResult, boolPointer(true), 20),
		}}
		response := BuildConnectivityResponse(1, window, history)
		if response.Summary.SLAPercent == nil || *response.Summary.SLAPercent != 100 {
			t.Fatalf("SLA = %v, want 100", response.Summary.SLAPercent)
		}
		assertNear(t, response.Summary.AvailableSeconds, 3600, 0.001)
	})

	t.Run("half available", func(t *testing.T) {
		history := model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{
			connectivityTestEvent(1, from, model.ConnectivityEventProbeResult, boolPointer(true), 20),
			connectivityTestEvent(2, from.Add(30*time.Minute), model.ConnectivityEventProbeResult, boolPointer(false), 8000),
		}}
		response := BuildConnectivityResponse(1, window, history)
		if response.Summary.SLAPercent == nil || *response.Summary.SLAPercent != 50 {
			t.Fatalf("SLA = %v, want 50", response.Summary.SLAPercent)
		}
		if response.Latency.SuccessfulProbeCount != 1 || response.Latency.AverageMS == nil || *response.Latency.AverageMS != 20 {
			t.Fatalf("failed probe entered latency stats: %#v", response.Latency)
		}
	})

	t.Run("latency does not affect SLA", func(t *testing.T) {
		build := func(latency int) connectivityResponse {
			return BuildConnectivityResponse(1, window, model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{connectivityTestEvent(1, from, model.ConnectivityEventProbeResult, boolPointer(true), latency)}})
		}
		fast, slow := build(20), build(5000)
		if fast.Summary.SLAPercent == nil || slow.Summary.SLAPercent == nil || *fast.Summary.SLAPercent != *slow.Summary.SLAPercent {
			t.Fatalf("SLA changed with latency: %v vs %v", fast.Summary.SLAPercent, slow.Summary.SLAPercent)
		}
	})

	t.Run("disabled excluded and re-enabled probe applied", func(t *testing.T) {
		history := model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{
			connectivityTestEvent(1, from, model.ConnectivityEventProbeResult, boolPointer(true), 20),
			connectivityTestEvent(2, from.Add(30*time.Minute), model.ConnectivityEventProbeDisabled, nil, 0),
			connectivityTestEvent(3, from.Add(45*time.Minute), model.ConnectivityEventProbeEnabled, nil, 0),
			connectivityTestEvent(4, from.Add(45*time.Minute), model.ConnectivityEventProbeResult, boolPointer(false), 0),
		}}
		response := BuildConnectivityResponse(1, window, history)
		assertNear(t, response.Summary.AvailableSeconds, 1800, 0.001)
		assertNear(t, response.Summary.UnavailableSeconds, 900, 0.001)
		assertNear(t, response.Summary.UnknownSeconds, 900, 0.001)
		assertNear(t, response.Summary.ObservedSeconds, 2700, 0.001)
		assertNear(t, *response.Summary.SLAPercent, 66.6666667, 0.0001)
		assertNear(t, response.Summary.CoveragePercent, 75, 0.0001)
		if response.Summary.OutageCount != 1 || len(response.Outages) != 1 || response.Outages[0].StartedAt != from.Add(45*time.Minute) {
			t.Fatalf("disabled interval was counted as an outage: %#v", response.Outages)
		}
	})

	t.Run("target change is an unobserved boundary", func(t *testing.T) {
		history := model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{
			connectivityTestEvent(1, from, model.ConnectivityEventProbeResult, boolPointer(true), 20),
			connectivityTestEvent(2, from.Add(30*time.Minute), model.ConnectivityEventProbeTargetChanged, nil, 0),
			connectivityTestEvent(3, from.Add(31*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 25),
		}}
		response := BuildConnectivityResponse(1, window, history)
		assertNear(t, response.Summary.AvailableSeconds, 59*60, 0.001)
		assertNear(t, response.Summary.UnknownSeconds, 60, 0.001)
		if response.Summary.SLAPercent == nil || *response.Summary.SLAPercent != 100 || response.Summary.OutageCount != 0 {
			t.Fatalf("target change affected SLA or outages: %#v", response.Summary)
		}
	})

	t.Run("target change preserves an existing offline outage", func(t *testing.T) {
		history := model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{
			connectivityTestEvent(1, from, model.ConnectivityEventProbeResult, boolPointer(true), 20),
			connectivityTestEvent(2, from.Add(20*time.Minute), model.ConnectivityEventServerOffline, boolPointer(false), 0),
			connectivityTestEvent(3, from.Add(30*time.Minute), model.ConnectivityEventProbeTargetChanged, nil, 0),
			connectivityTestEvent(4, from.Add(40*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 25),
		}}
		response := BuildConnectivityResponse(1, window, history)
		assertNear(t, response.Summary.AvailableSeconds, 40*60, 0.001)
		assertNear(t, response.Summary.UnavailableSeconds, 20*60, 0.001)
		assertNear(t, response.Summary.UnknownSeconds, 0, 0.001)
		if response.Summary.SLAPercent == nil || response.Summary.OutageCount != 1 || len(response.Outages) != 1 || response.Outages[0].DurationSeconds != 20*60 {
			t.Fatalf("target change masked offline outage: summary=%#v outages=%#v", response.Summary, response.Outages)
		}
	})
}

func TestConnectivityFailedProbePointsAggregateActualFailures(t *testing.T) {
	from := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	window := connectivityTestWindow(from, time.Hour, 15*time.Minute)
	events := []model.ServerConnectivityEvent{
		connectivityTestEvent(1, from.Add(5*time.Minute+10*time.Second), model.ConnectivityEventProbeResult, boolPointer(false), 0),
		connectivityTestEvent(2, from.Add(5*time.Minute+50*time.Second), model.ConnectivityEventProbeResult, boolPointer(false), 0),
		connectivityTestEvent(3, from.Add(6*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 25),
		connectivityTestEvent(4, from.Add(7*time.Minute), model.ConnectivityEventControllerDisconnected, nil, 0),
	}
	response := BuildConnectivityResponse(1, window, model.ServerConnectivityHistory{Events: events})
	if len(response.FailedProbePoints) != 1 || response.FailedProbePoints[0].At != from.Add(5*time.Minute) || response.FailedProbePoints[0].Count != 2 {
		t.Fatalf("failed probe points = %#v", response.FailedProbePoints)
	}
	if response.Probes.Failed != 2 || response.Probes.Available != 1 {
		t.Fatalf("probe counts = %#v", response.Probes)
	}
}

func TestConnectivityConnectionAndOfflineProbeStateMachine(t *testing.T) {
	from := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	window := connectivityTestWindow(from, time.Hour, 15*time.Minute)
	history := model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{
		connectivityTestEvent(1, from, model.ConnectivityEventControllerConnected, nil, 0),
		connectivityTestEvent(2, from.Add(5*time.Minute), model.ConnectivityEventProbeTargetChanged, nil, 0),
		connectivityTestEvent(3, from.Add(10*time.Minute), model.ConnectivityEventControllerDisconnected, nil, 0),
		connectivityTestEvent(4, from.Add(15*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 30),
		connectivityTestEvent(5, from.Add(30*time.Minute), model.ConnectivityEventProbeResult, boolPointer(false), 0),
		connectivityTestEvent(6, from.Add(45*time.Minute), model.ConnectivityEventControllerConnected, nil, 0),
	}}
	response := BuildConnectivityResponse(1, window, history)
	assertNear(t, response.Summary.AvailableSeconds, 40*60, 0.001)
	assertNear(t, response.Summary.UnavailableSeconds, 20*60, 0.001)
	assertNear(t, response.Summary.UnknownSeconds, 0, 0.001)
	if response.Current.Status != "available" || response.Summary.SLAPercent == nil {
		t.Fatalf("state machine response = %#v", response)
	}
	assertNear(t, *response.Summary.SLAPercent, 66.6666667, 0.0001)
}

func TestControllerUpdateDisconnectIsUnknownUntilReplayedProbeEvidence(t *testing.T) {
	from := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	window := connectivityTestWindow(from, time.Hour, 15*time.Minute)
	connected := connectivityTestEvent(1, from, model.ConnectivityEventControllerConnected, nil, 0)
	maintenance := connectivityTestEvent(2, from.Add(10*time.Minute), model.ConnectivityEventControllerDisconnected, nil, 0)
	maintenance.Source = model.ConnectivityEventSourceControllerUpdate
	offline := connectivityTestEvent(3, from.Add(15*time.Minute), model.ConnectivityEventServerOffline, nil, 0)
	failed := connectivityTestEvent(4, from.Add(20*time.Minute), model.ConnectivityEventProbeResult, boolPointer(false), 0)
	succeeded := connectivityTestEvent(5, from.Add(30*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 25)
	reconnected := connectivityTestEvent(6, from.Add(40*time.Minute), model.ConnectivityEventControllerConnected, nil, 0)
	response := BuildConnectivityResponse(1, window, model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{connected, maintenance, offline, failed, succeeded, reconnected}})
	assertNear(t, response.Summary.AvailableSeconds, 40*60, 0.001)
	assertNear(t, response.Summary.UnavailableSeconds, 10*60, 0.001)
	assertNear(t, response.Summary.UnknownSeconds, 10*60, 0.001)
	assertNear(t, *response.Summary.SLAPercent, 80, 0.001)

	normal := maintenance
	normal.Source = model.ConnectivityEventSourceAgentSocket
	normalResponse := BuildConnectivityResponse(1, window, model.ServerConnectivityHistory{Events: []model.ServerConnectivityEvent{connected, normal, reconnected}})
	assertNear(t, normalResponse.Summary.UnavailableSeconds, 30*60, 0.001)
	assertNear(t, normalResponse.Summary.UnknownSeconds, 0, 0.001)
}

func TestConnectivityPendingUnknownAndBaseline(t *testing.T) {
	from := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	window := connectivityTestWindow(from, time.Hour, 15*time.Minute)
	t.Run("no observation", func(t *testing.T) {
		response := BuildConnectivityResponse(1, window, model.ServerConnectivityHistory{})
		if response.Summary.SLAPercent != nil || response.Current.Status != "pending" || response.Summary.UnknownSeconds != 3600 {
			t.Fatalf("unexpected unknown response: %#v", response)
		}
	})
	t.Run("re-enable resets old success", func(t *testing.T) {
		history := model.ServerConnectivityHistory{Baseline: []model.ServerConnectivityEvent{
			connectivityTestEvent(1, from.Add(-time.Hour), model.ConnectivityEventProbeResult, boolPointer(true), 20),
			connectivityTestEvent(2, from.Add(-time.Minute), model.ConnectivityEventProbeEnabled, nil, 0),
		}}
		response := BuildConnectivityResponse(1, window, history)
		if response.Summary.SLAPercent != nil || response.Summary.UnknownSeconds != 3600 || response.Current.Status != "pending" {
			t.Fatalf("enabled baseline did not reset status: %#v", response)
		}
	})
	t.Run("old probe continues as baseline", func(t *testing.T) {
		history := model.ServerConnectivityHistory{Baseline: []model.ServerConnectivityEvent{connectivityTestEvent(1, from.Add(-time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 20)}}
		response := BuildConnectivityResponse(1, window, history)
		if response.Summary.SLAPercent == nil || *response.Summary.SLAPercent != 100 || response.Summary.AvailableSeconds != 3600 {
			t.Fatalf("baseline did not continue: %#v", response.Summary)
		}
	})
}

func TestConnectivityLatencyP95OutagesAndBuckets(t *testing.T) {
	from := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	window := connectivityTestWindow(from, time.Hour, 15*time.Minute)
	latencies := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 100}
	events := []model.ServerConnectivityEvent{connectivityTestEvent(1, from, model.ConnectivityEventProbeResult, boolPointer(true), 1)}
	for index, latency := range latencies {
		events = append(events, connectivityTestEvent(int64(index+2), from.Add(time.Duration(index+1)*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), latency))
	}
	response := BuildConnectivityResponse(1, window, model.ServerConnectivityHistory{Events: events})
	if response.Latency.P95MS == nil || *response.Latency.P95MS != 100 {
		t.Fatalf("p95 = %v, want 100", response.Latency.P95MS)
	}
	var bucketAvailable, bucketUnavailable, bucketUnknown float64
	for _, bucket := range response.Buckets {
		bucketAvailable += bucket.AvailableSeconds
		bucketUnavailable += bucket.UnavailableSeconds
		bucketUnknown += bucket.UnknownSeconds
	}
	assertNear(t, bucketAvailable, response.Summary.AvailableSeconds, 0.001)
	assertNear(t, bucketUnavailable, response.Summary.UnavailableSeconds, 0.001)
	assertNear(t, bucketUnknown, response.Summary.UnknownSeconds, 0.001)

	crossWindow := model.ServerConnectivityHistory{
		Baseline: []model.ServerConnectivityEvent{connectivityTestEvent(20, from.Add(-10*time.Minute), model.ConnectivityEventProbeResult, boolPointer(false), 0)},
		Events:   []model.ServerConnectivityEvent{connectivityTestEvent(21, from.Add(20*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 20)},
	}
	outageResponse := BuildConnectivityResponse(1, window, crossWindow)
	if len(outageResponse.Outages) != 1 || !outageResponse.Outages[0].StartedBeforeWindow || outageResponse.Outages[0].DurationSeconds != 1200 {
		t.Fatalf("cross-window outage = %#v", outageResponse.Outages)
	}
}

func TestConnectivityLatencyPointsAreBounded(t *testing.T) {
	from := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	window := connectivityTestWindow(from, 30*24*time.Hour, 24*time.Hour)
	events := make([]model.ServerConnectivityEvent, 0, 43200)
	for index := 0; index < 43200; index++ {
		events = append(events, connectivityTestEvent(int64(index+1), from.Add(time.Duration(index)*time.Minute), model.ConnectivityEventProbeResult, boolPointer(true), 10+index%20))
	}
	response := BuildConnectivityResponse(1, window, model.ServerConnectivityHistory{Events: events})
	if len(response.LatencyPoints) > 360 {
		t.Fatalf("latency points = %d, want <= 360", len(response.LatencyPoints))
	}
}

func TestServerConnectivityAPIWindowsAndErrors(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "api-node", Status: model.ServerUnknown, ConnectivityProbeEnabled: true, OfflineNotifyEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	for _, key := range []string{"", "1h", "6h", "12h", "24h", "7d", "30d"} {
		path := "/api/v1/ui/servers/" + strconv.FormatInt(server.ID, 10) + "/connectivity"
		if key != "" {
			path += "?window=" + key
		}
		response := request(t, handler, http.MethodGet, path, token, nil, http.StatusOK)
		window := response["window"].(map[string]any)
		wantKey := key
		if wantKey == "" {
			wantKey = "24h"
		}
		if window["key"] != wantKey {
			t.Fatalf("window key = %v, want %s", window["key"], wantKey)
		}
		summary := response["summary"].(map[string]any)
		if summary["sla_percent"] != nil {
			t.Fatalf("new server SLA = %v, want null", summary["sla_percent"])
		}
		if _, exposed := response["events"]; exposed {
			t.Fatal("API exposed raw connectivity events")
		}
		if points := response["latency_points"].([]any); len(points) > 360 {
			t.Fatalf("latency_points = %d, want <= 360", len(points))
		}
		if outages, ok := response["outages"].([]any); !ok || outages == nil {
			t.Fatalf("outages must be a JSON array: %#v", response["outages"])
		}
	}
	request(t, handler, http.MethodGet, "/api/v1/ui/servers/"+strconv.FormatInt(server.ID, 10)+"/connectivity?window=1y", token, nil, http.StatusBadRequest)
	request(t, handler, http.MethodGet, "/api/v1/ui/servers/999999/connectivity", token, nil, http.StatusNotFound)
}
