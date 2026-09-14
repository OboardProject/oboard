package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func resourceHistoryWindow(at time.Time) model.ServerTrafficWindow {
	return model.ServerTrafficWindow{Key: at.Format("2006-01-02"), Start: at.Truncate(24 * time.Hour), End: at.Truncate(24 * time.Hour).Add(24 * time.Hour)}
}

func TestServerResourceHistoryCanBeDisabledWithoutStoppingNetworkSamples(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "resource-node", AgentID: "resource-agent", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if !server.ResourceHistoryEnabled {
		t.Fatal("new servers must record resource history by default")
	}
	at := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	report := model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, CPUUsagePercent: 42, MemoryUsedBytes: 400, MemoryTotalBytes: 1000, DiskBytes: 600, DiskTotalBytes: 2000, TCPConnectionCount: 12, UDPConnectionCount: 3, ProcessCount: 48, NetworkUploadBPS: 12, NetworkDownloadBPS: 34, NetworkTotalUploadBytes: 100, NetworkTotalDownloadBytes: 200, Timestamp: at}
	if _, _, err := db.UpsertHealthTransition(ctx, report, resourceHistoryWindow(at)); err != nil {
		t.Fatal(err)
	}
	points, err := db.ListServerResourceMetricPoints(ctx, server.ID, at.Add(-time.Hour), time.Minute)
	if err != nil || len(points) != 1 || points[0].CPUUsagePercent != 42 || points[0].DiskUsedBytes != 600 || points[0].DiskTotalBytes != 2000 || points[0].TCPConnectionCount != 12 || points[0].UDPConnectionCount != 3 || points[0].ProcessCount != 48 || points[0].NetworkUploadBPS != 12 || points[0].NetworkDownloadBPS != 34 {
		t.Fatalf("initial resource points = %#v, err=%v", points, err)
	}

	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.ResourceHistoryEnabled = false
	if err := db.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	points, err = db.ListServerResourceMetricPoints(ctx, server.ID, at.Add(-time.Hour), time.Minute)
	if err != nil || len(points) != 0 {
		t.Fatalf("disabled history retained resource points = %#v, err=%v", points, err)
	}

	report.Timestamp = at.Add(time.Minute)
	report.CPUUsagePercent = 75
	report.MemoryUsedBytes = 750
	report.NetworkTotalUploadBytes = 200
	report.NetworkTotalDownloadBytes = 400
	if _, _, err := db.UpsertHealthTransition(ctx, report, resourceHistoryWindow(report.Timestamp)); err != nil {
		t.Fatal(err)
	}
	samples, err := db.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil || len(samples) != 1 {
		t.Fatalf("disabling resource history bypassed sampling interval: len=%d err=%v", len(samples), err)
	}
	report.Timestamp = at.Add(defaultMetricSampleMinInterval)
	if _, _, err := db.UpsertHealthTransition(ctx, report, resourceHistoryWindow(report.Timestamp)); err != nil {
		t.Fatal(err)
	}
	samples, err = db.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil || len(samples) != 2 {
		t.Fatalf("network samples stopped after disabling resource history: len=%d err=%v", len(samples), err)
	}
	for _, sample := range samples {
		if sample.ResourceRecorded || sample.CPUUsagePercent != 0 || sample.MemoryUsedBytes != 0 || sample.MemoryTotalBytes != 0 || sample.DiskUsedBytes != 0 || sample.DiskTotalBytes != 0 || sample.TCPConnectionCount != 0 || sample.UDPConnectionCount != 0 || sample.ProcessCount != 0 {
			t.Fatalf("resource history survived disabled state: %#v", sample)
		}
	}
	live, err := db.GetServer(ctx, server.ID)
	if err != nil || live.CPUUsagePercent != 75 || live.MemoryUsedBytes != 750 || live.MemoryTotalBytes != 1000 {
		t.Fatalf("live resource state unavailable while history disabled: %#v err=%v", live, err)
	}
}
