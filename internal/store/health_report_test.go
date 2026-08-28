package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// healthReportStatementCount is the exact number of SQL statements one
// ApplyHealthReport executes on the happy path: BEGIN, point read of the
// servers row, conditional runtime-state UPDATE, telemetry accounting SELECT,
// telemetry UPDATE, and the atomic
// conditional metric-sample INSERT.
const healthReportStatementCount = 6

func healthReportWindow(at time.Time) model.ServerTrafficWindow {
	return model.ServerTrafficWindow{Key: at.UTC().Format("2006-01-02"), Start: at.UTC().Truncate(time.Hour), End: at.UTC().Truncate(time.Hour).Add(24 * time.Hour)}
}

func healthReportFixture(agentID string, at time.Time) model.HealthReport {
	return model.HealthReport{
		AgentID: agentID, Status: model.ServerOnline, Timestamp: at,
		CPUUsagePercent: 23, MemoryUsedBytes: 1 << 30, MemoryTotalBytes: 4 << 30,
		TCPConnectionCount: 120, NetworkUploadBPS: 1000, NetworkDownloadBPS: 2000,
	}
}

// TestHealthReportSQLCountConstant asserts the hot path performs a constant
// number of SQL statements in exactly one write transaction regardless of how
// many servers the fleet manages, and that none of the statements touch rows
// of other servers (the statement count itself is the proxy: unconditional
// telemetry/latency/connectivity scans would scale with fleet size).
func TestHealthReportSQLCountConstant(t *testing.T) {
	ctx := context.Background()
	for _, count := range []int{1, 10, 100, 500} {
		t.Run(fmt.Sprintf("servers_%d", count), func(t *testing.T) {
			s, err := OpenWithOptions(filepath.Join(t.TempDir(), "oboard.sqlite"), DefaultSQLiteOptions())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			servers := make([]*model.Server, count)
			for index := range servers {
				server := &model.Server{Name: fmt.Sprintf("node-%d", index), AgentID: fmt.Sprintf("agent-%d", index), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerUnknown}
				if err := s.CreateServer(ctx, server); err != nil {
					t.Fatal(err)
				}
				servers[index] = server
			}
			at := time.Now().UTC()
			for _, server := range servers {
				at = at.Add(time.Second)
				before := s.SQLStatementCount()
				beforeTx := s.SQLWriteTransactionCount()
				result, err := s.ApplyHealthReport(ctx, server.ID, healthReportFixture(server.AgentID, at), healthReportWindow(at))
				if err != nil {
					t.Fatal(err)
				}
				if delta := s.SQLStatementCount() - before; delta != healthReportStatementCount {
					t.Fatalf("statement delta = %d, want %d", delta, healthReportStatementCount)
				}
				if delta := s.SQLWriteTransactionCount() - beforeTx; delta != 1 {
					t.Fatalf("write transaction delta = %d, want 1", delta)
				}
				if !result.StatusChanged {
					t.Fatalf("expected first report to claim the status transition")
				}
			}
		})
	}
}

// TestApplyHealthReportTransitionIdempotent verifies concurrent reports for
// the same server yield exactly one offline->online transition claim, so
// recovery handling (deployment push, online notification) fires once.
func TestApplyHealthReportTransitionIdempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "recover", AgentID: "agent-recover", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOffline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	window := healthReportWindow(at)
	const reporters = 8
	results := make([]HealthApplyResult, reporters)
	var wg sync.WaitGroup
	for index := 0; index < reporters; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := s.ApplyHealthReport(ctx, server.ID, healthReportFixture(server.AgentID, at.Add(time.Duration(index)*time.Millisecond)), window)
			if err == nil {
				results[index] = result
			}
		}(index)
	}
	wg.Wait()
	claimed := 0
	for _, result := range results {
		if result.OldStatus == model.ServerOffline && result.NewStatus == model.ServerOnline && result.StatusChanged {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("transition claimed by %d reports, want exactly 1", claimed)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.ServerOnline {
		t.Fatalf("server status = %q, want online", stored.Status)
	}
}

// TestApplyHealthReportPreservesSemantics covers the report surface the hot
// path must not regress: runtime state, public IP updates, traffic windows,
// metric sample rate limiting, and telemetry management settings untouched.
func TestApplyHealthReportPreservesSemantics(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "metrics", AgentID: "agent-metrics", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline, MonitoringMode: "standard", TrafficResetMode: "month_day", TrafficResetDay: 15, OfflineNotifyEnabled: false}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	window := model.ServerTrafficWindow{Key: "2026-07-15", Start: start, End: start.AddDate(0, 1, 0)}
	first := healthReportFixture(server.AgentID, start)
	first.NetworkTotalUploadBytes = 1000
	first.NetworkTotalDownloadBytes = 2000
	first.PublicIPv4 = "203.0.113.7"
	first.RegionCode = "SG"
	if _, err := s.ApplyHealthReport(ctx, server.ID, first, window); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Timestamp = start.Add(10 * time.Second)
	second.NetworkTotalUploadBytes = 1600
	second.NetworkTotalDownloadBytes = 3200
	second.CPUUsagePercent = 30
	if _, err := s.ApplyHealthReport(ctx, server.ID, second, window); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.ServerOnline || stored.CPUUsagePercent != 30 || stored.PublicIPv4 != "203.0.113.7" || stored.DetectedRegionCode != "SG" {
		t.Fatalf("runtime state not applied: %#v", stored)
	}
	if stored.TrafficUploadBytes != 600 || stored.TrafficDownloadBytes != 1200 {
		t.Fatalf("period traffic upload=%d download=%d, want 600/1200", stored.TrafficUploadBytes, stored.TrafficDownloadBytes)
	}
	if stored.MonitoringMode != "standard" || stored.TrafficResetMode != "month_day" || stored.TrafficResetDay != 15 {
		t.Fatalf("health report must not touch telemetry settings: %#v", stored)
	}
	samples, err := s.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 10s apart with the default 60s sample interval: exactly one sample.
	if len(samples) != 1 {
		t.Fatalf("metric samples = %d, want 1 (rate limited)", len(samples))
	}
	// Period rollover resets the counters.
	nextStart := start.AddDate(0, 1, 0)
	nextWindow := model.ServerTrafficWindow{Key: "2026-08-15", Start: nextStart, End: nextStart.AddDate(0, 1, 0)}
	rollover := second
	rollover.Timestamp = nextStart
	rollover.NetworkTotalUploadBytes = 5000
	rollover.NetworkTotalDownloadBytes = 9000
	if _, err := s.ApplyHealthReport(ctx, server.ID, rollover, nextWindow); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUploadBytes != 0 || stored.TrafficDownloadBytes != 0 {
		t.Fatalf("new period should start at zero: upload=%d download=%d", stored.TrafficUploadBytes, stored.TrafficDownloadBytes)
	}
}

func TestHealthReportCoalescesUnchangedRuntime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "hot", AgentID: "agent-hot", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerUnknown}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	first, err := s.ApplyHealthReport(ctx, server.ID, healthReportFixture(server.AgentID, at), healthReportWindow(at))
	if err != nil || first.Coalesced {
		t.Fatalf("first report coalesced=%t err=%v", first.Coalesced, err)
	}
	before := s.SQLStatementCount()
	second, err := s.ApplyHealthReport(ctx, server.ID, healthReportFixture(server.AgentID, at.Add(time.Second)), healthReportWindow(at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Coalesced {
		t.Fatal("unchanged volatile report within 45s should coalesce")
	}
	if delta := s.SQLStatementCount() - before; delta >= healthReportStatementCount {
		t.Fatalf("coalesced statement delta = %d, want fewer than first-write %d", delta, healthReportStatementCount)
	}
	changed := healthReportFixture(server.AgentID, at.Add(2*time.Second))
	changed.AgentBuild = "20260828010101"
	third, err := s.ApplyHealthReport(ctx, server.ID, changed, healthReportWindow(at.Add(2*time.Second)))
	if err != nil || third.Coalesced {
		t.Fatalf("agent build change must persist: coalesced=%t err=%v", third.Coalesced, err)
	}
}

func TestApplyHealthReportPersistsCPUCoresWithoutParsingModel(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "cores", AgentID: "agent-cores", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerUnknown}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	first := healthReportFixture(server.AgentID, at)
	first.CPU = "Intel Xeon E3-12xx v2 (Ivy Bridge, IBRS)"
	first.CPUCores = 4
	applied, err := s.ApplyHealthReport(ctx, server.ID, first, healthReportWindow(at))
	if err != nil || applied.Coalesced {
		t.Fatalf("first cores report coalesced=%t err=%v", applied.Coalesced, err)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CPU != first.CPU || stored.CPUCores != 4 {
		t.Fatalf("cpu=%q cores=%d, want model plus 4 cores", stored.CPU, stored.CPUCores)
	}
	omitted := healthReportFixture(server.AgentID, at.Add(time.Minute))
	omitted.CPU = first.CPU
	omitted.CPUCores = 0
	kept, err := s.ApplyHealthReport(ctx, server.ID, omitted, healthReportWindow(at.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if kept.Curr.CPUCores != 4 {
		t.Fatalf("omitted cpu_cores cleared last value: %d", kept.Curr.CPUCores)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CPUCores != 4 {
		t.Fatalf("stored cpu_cores = %d after omitted report, want 4", stored.CPUCores)
	}
}

