package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// BenchmarkNextTaskClaim100Agents measures the atomic claim path for 100
// servers with queued tasks, the exact work the previous per-agent 1s polling
// loop issued every second.
func BenchmarkNextTaskClaim100Agents(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const agents = 100
	servers := make([]*model.Server, agents)
	for index := range servers {
		server := &model.Server{Name: fmt.Sprintf("bench-agent-%d", index), AgentID: fmt.Sprintf("bench-agent-%d", index), AgentTokenHash: "hash", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
		if err := s.CreateServer(ctx, server); err != nil {
			b.Fatal(err)
		}
		servers[index] = server
	}
	for _, server := range servers {
		for i := 0; i < 100; i++ {
			if err := s.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: "collect_logs", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: fmt.Sprintf("claim-%d-%d", server.ID, i)}); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		server := servers[index%agents]
		if _, err := s.NextTask(ctx, server.ID); err != nil && err != sql.ErrNoRows {
			b.Fatal(err)
		}
		if err != nil && err == sql.ErrNoRows {
			if err := s.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: "collect_logs", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: fmt.Sprintf("refill-%d", index)}); err != nil {
				b.Fatal(err)
			}
			if _, err := s.NextTask(ctx, server.ID); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkUpsertHealthTransition compares the health report persistence with
// the 60-second metric sample rate limit (current) against the legacy
// per-report sample insert (previous behavior).
func BenchmarkUpsertHealthTransition(b *testing.B) {
	window := model.ServerTrafficWindow{Key: "2026-08-01", Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	newStore := func(interval time.Duration) (*Store, *model.Server) {
		opts := DefaultSQLiteOptions()
		opts.MetricSampleMinInterval = interval
		s, err := OpenWithOptions(filepath.Join(b.TempDir(), "oboard.sqlite"), opts)
		if err != nil {
			b.Fatal(err)
		}
		server := &model.Server{Name: "bench-health", AgentID: "bench-health-agent", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
		if err := s.CreateServer(context.Background(), server); err != nil {
			b.Fatal(err)
		}
		return s, server
	}
	report := func(at time.Time) model.HealthReport {
		return model.HealthReport{AgentID: "bench-health-agent", Status: model.ServerOnline, Timestamp: at, CPUUsagePercent: 23, MemoryUsedBytes: 1 << 30, MemoryTotalBytes: 4 << 30, TCPConnectionCount: 120, NetworkUploadBPS: 1000, NetworkDownloadBPS: 2000}
	}
	b.Run("ratelimited_60s", func(b *testing.B) {
		s, _ := newStore(defaultMetricSampleMinInterval)
		defer s.Close()
		ctx := context.Background()
		b.ResetTimer()
		at := time.Now().UTC()
		for index := 0; index < b.N; index++ {
			at = at.Add(30 * time.Second)
			if _, _, err := s.UpsertHealthTransition(ctx, report(at), window); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unlimited", func(b *testing.B) {
		s, _ := newStore(0)
		defer s.Close()
		ctx := context.Background()
		b.ResetTimer()
		at := time.Now().UTC()
		for index := 0; index < b.N; index++ {
			at = at.Add(30 * time.Second)
			if _, _, err := s.UpsertHealthTransition(ctx, report(at), window); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("legacy_insert_and_retention_delete", func(b *testing.B) {
		// The pre-optimization path inserted one sample per report and ran the
		// 30-day retention DELETE inside the same transaction.
		s, server := newStore(0)
		_ = server
		defer s.Close()
		ctx := context.Background()
		b.ResetTimer()
		at := time.Now().UTC()
		for index := 0; index < b.N; index++ {
			at = at.Add(30 * time.Second)
			if _, _, err := s.UpsertHealthTransition(ctx, report(at), window); err != nil {
				b.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `insert into server_metric_samples(server_id,cpu_usage_percent,sampled_at) values(?,?,?)`, server.ID, report(at).CPUUsagePercent, at.Format(time.RFC3339Nano)); err != nil {
				b.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `delete from server_metric_samples where server_id=? and sampled_at<?`, server.ID, at.Add(-30*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkHealthReportScaling measures the health-report persistence hot path
// as the managed fleet grows from 1 to 500 servers. The report is applied to a
// rotating server so every iteration exercises the full path; per-report cost
// must stay constant because all SQL is point-filtered by server id.
func BenchmarkHealthReportScaling(b *testing.B) {
	window := model.ServerTrafficWindow{Key: "2026-08-01", Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	for _, count := range []int{1, 10, 100, 500} {
		b.Run(fmt.Sprintf("servers_%d", count), func(b *testing.B) {
			b.ReportAllocs()
			s, servers := newHealthReportBenchStore(b, count)
			defer s.Close()
			ctx := context.Background()
			report := func(agentID string, at time.Time) model.HealthReport {
				return model.HealthReport{AgentID: agentID, Status: model.ServerOnline, Timestamp: at, CPUUsagePercent: 23, MemoryUsedBytes: 1 << 30, MemoryTotalBytes: 4 << 30, TCPConnectionCount: 120, NetworkUploadBPS: 1000, NetworkDownloadBPS: 2000}
			}
			b.ResetTimer()
			at := time.Now().UTC()
			for index := 0; index < b.N; index++ {
				at = at.Add(30 * time.Second)
				server := servers[index%count]
				if _, err := s.ApplyHealthReport(ctx, server.ID, report(server.AgentID, at), window); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func newHealthReportBenchStore(b *testing.B, count int) (*Store, []*model.Server) {
	b.Helper()
	opts := DefaultSQLiteOptions()
	opts.MetricSampleMinInterval = 0
	s, err := OpenWithOptions(filepath.Join(b.TempDir(), "oboard.sqlite"), opts)
	if err != nil {
		b.Fatal(err)
	}
	servers := make([]*model.Server, count)
	for index := range servers {
		server := &model.Server{Name: fmt.Sprintf("bench-node-%d", index), AgentID: fmt.Sprintf("bench-node-agent-%d", index), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
		if err := s.CreateServer(context.Background(), server); err != nil {
			b.Fatal(err)
		}
		servers[index] = server
	}
	return s, servers
}

// BenchmarkHealthReportPoolScaling compares the health-report hot path under
// MaxOpenConns of 1, 2, and 4. Do not raise the default pool size: SQLite
// serializes writers, and larger pools mostly add mutex and idle connections.
func BenchmarkHealthReportPoolScaling(b *testing.B) {
	window := model.ServerTrafficWindow{Key: "2026-08-01", Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	for _, pool := range []int{1, 2, 4} {
		b.Run(fmt.Sprintf("max_open_%d", pool), func(b *testing.B) {
			b.ReportAllocs()
			opts := DefaultSQLiteOptions()
			opts.MaxOpenConns = pool
			opts.MaxIdleConns = pool
			opts.MetricSampleMinInterval = 0
			s, err := OpenWithOptions(filepath.Join(b.TempDir(), "oboard.sqlite"), opts)
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			server := &model.Server{Name: "bench-pool", AgentID: "bench-pool-agent", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
			if err := s.CreateServer(context.Background(), server); err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.ResetTimer()
			at := time.Now().UTC()
			for index := 0; index < b.N; index++ {
				at = at.Add(30 * time.Second)
				report := model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, Timestamp: at, CPUUsagePercent: 23, MemoryUsedBytes: 1 << 30, MemoryTotalBytes: 4 << 30, TCPConnectionCount: 120, NetworkUploadBPS: 1000, NetworkDownloadBPS: 2000}
				if _, err := s.ApplyHealthReport(ctx, server.ID, report, window); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkConnectionAuditBatchDeviceActivity compares the per-report device
// activity UPDATE loop against the single-statement bulk update for report
// batches of 100 and 500 items.
func BenchmarkConnectionAuditBatchDeviceActivity(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "bench-device-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		b.Fatal(err)
	}
	devices := []*model.UserDevice{}
	for index := 0; index < 500; index++ {
		device := &model.UserDevice{ID: fmt.Sprintf("bench-device-%d", index), DeviceIDHash: fmt.Sprintf("bench-device-hash-%d", index), UserID: user.ID, Name: "device", TokenHash: fmt.Sprintf("bench-token-%d", index), TokenPrefix: "bench", CredentialEpoch: 1, Status: "active"}
		if err := s.CreateUserDevice(ctx, device); err != nil {
			b.Fatal(err)
		}
		devices = append(devices, device)
	}
	at := time.Now().UTC()
	activity := func(count int) map[string]time.Time {
		out := make(map[string]time.Time, count)
		for index := 0; index < count; index++ {
			out[devices[index].DeviceIDHash] = at
		}
		return out
	}
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprintf("bulk_%d", count), func(b *testing.B) {
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if err := s.MarkUserDevicesProxyActivity(ctx, activity(count)); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("per_report_%d", count), func(b *testing.B) {
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				for _, hash := range devices[:count] {
					if err := s.MarkUserDeviceProxyActivity(ctx, hash.DeviceIDHash, at); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
