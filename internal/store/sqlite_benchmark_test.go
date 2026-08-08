package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const defaultSQLiteBenchmarkRows = 100000

type sqliteBenchmarkDataset struct {
	store  *Store
	server *model.Server
	users  []*model.User
	at     time.Time
}

func BenchmarkConnectionAuditOverview(b *testing.B) {
	benchmarkSQLitePools(b, func(b *testing.B, poolSize int) {
		dataset := newSQLiteBenchmarkDataset(b, poolSize, sqliteBenchmarkRows(defaultSQLiteBenchmarkRows), true, false)
		ctx := context.Background()
		b.ResetTimer()
		b.RunParallel(func(worker *testing.PB) {
			for worker.Next() {
				if _, err := dataset.store.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy()); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func BenchmarkConnectionAuditProbeRefresh(b *testing.B) {
	benchmarkSQLitePools(b, func(b *testing.B, poolSize int) {
		dataset := newSQLiteBenchmarkDataset(b, poolSize, sqliteBenchmarkRows(defaultSQLiteBenchmarkRows), true, false)
		ctx := context.Background()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			if err := dataset.store.refreshConnectionProbeEpisodes(ctx, dataset.users[0].ID, dataset.at); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkConnectionAuditInsertParallel(b *testing.B) {
	benchmarkSQLitePools(b, func(b *testing.B, poolSize int) {
		dataset := newSQLiteBenchmarkDataset(b, poolSize, sqliteBenchmarkRows(defaultSQLiteBenchmarkRows), true, false)
		ctx := context.Background()
		var sequence atomic.Int64
		b.ResetTimer()
		b.RunParallel(func(worker *testing.PB) {
			for worker.Next() {
				index := sequence.Add(1)
				user := dataset.users[index%int64(len(dataset.users))]
				at := dataset.at.Add(time.Duration(index) * time.Nanosecond)
				report := model.ConnectionAuditReport{
					ReportID: fmt.Sprintf("parallel-%d", index), ServerID: dataset.server.ID, UserID: user.ID,
					DeviceIDHash: fmt.Sprintf("device-%d", user.ID), SourceIP: "1.1.1.1", Network: "tcp", OutboundTag: "direct",
					ConnectionCount: 1, CollectionStartedAt: at, CollectionEndedAt: at, StartedAt: at, EndedAt: at,
				}
				if _, err := dataset.store.AddConnectionAuditReports(ctx, []model.ConnectionAuditReport{report}); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func BenchmarkSubscriptionAuthorizeParallel(b *testing.B) {
	benchmarkSQLitePools(b, func(b *testing.B, poolSize int) {
		dataset := newSQLiteBenchmarkDataset(b, poolSize, sqliteBenchmarkRows(defaultSQLiteBenchmarkRows), false, true)
		ctx := context.Background()
		policy := benchmarkAuditPolicy()
		var sequence atomic.Int64
		b.ResetTimer()
		b.RunParallel(func(worker *testing.PB) {
			for worker.Next() {
				index := sequence.Add(1)
				user := dataset.users[index%int64(len(dataset.users))]
				event := model.SubscriptionPullAudit{
					UserID: user.ID, DeviceIDHash: fmt.Sprintf("device-%d", user.ID), RepresentationID: fmt.Sprintf("representation-%d", index),
					SubscriptionRevision: "benchmark", RouteID: "benchmark-route", SourceIP: "1.1.1.1", ClientName: "benchmark", Format: "sing-box",
					RequestedAt: dataset.at.Add(time.Duration(index) * time.Nanosecond),
				}
				if _, err := dataset.store.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, event, policy, SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionWarn}); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func BenchmarkConnectionAuditOverviewLarge(b *testing.B) {
	if strings.TrimSpace(os.Getenv("OBOARD_SQLITE_LARGE_BENCHMARK")) != "1" {
		b.Skip("set OBOARD_SQLITE_LARGE_BENCHMARK=1 to run the 500,000-row benchmark")
	}
	dataset := newSQLiteBenchmarkDataset(b, 4, 500000, true, true)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := dataset.store.ConnectionAuditOverview(context.Background(), 24, true, DefaultAuditPolicy()); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkSQLitePools(b *testing.B, run func(*testing.B, int)) {
	b.Helper()
	for _, poolSize := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("max_open_%d", poolSize), func(b *testing.B) {
			run(b, poolSize)
		})
	}
}

func sqliteBenchmarkRows(fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("OBOARD_SQLITE_BENCH_ROWS")))
	if err != nil || value < 1000 {
		return fallback
	}
	return value
}

func newSQLiteBenchmarkDataset(b *testing.B, poolSize, rows int, connectionAudits, subscriptionAudits bool) sqliteBenchmarkDataset {
	b.Helper()
	options := DefaultSQLiteOptions()
	options.MaxOpenConns = poolSize
	options.MaxIdleConns = poolSize
	s, err := OpenWithOptions(filepath.Join(b.TempDir(), "benchmark.sqlite"), options)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	server := &model.Server{Name: "benchmark-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		b.Fatal(err)
	}
	users := make([]*model.User, 100)
	for index := range users {
		user := &model.User{
			Username: fmt.Sprintf("benchmark-user-%d", index), PasswordHash: "hash", Role: model.RoleViewer, Status: "active",
			ProxyUUID: fmt.Sprintf("benchmark-uuid-%d", index), ProxyPassword: "benchmark-password", SubscriptionToken: fmt.Sprintf("benchmark-token-%d", index),
		}
		if err := s.CreateUser(ctx, user); err != nil {
			b.Fatal(err)
		}
		users[index] = user
	}
	at := time.Now().UTC().Truncate(time.Second)
	if connectionAudits {
		seedConnectionAuditBenchmark(b, s, server, users, at, rows)
	}
	if subscriptionAudits {
		seedSubscriptionAuditBenchmark(b, s, users, at, rows)
	}
	if _, err := s.db.ExecContext(ctx, `pragma optimize`); err != nil {
		b.Fatal(err)
	}
	return sqliteBenchmarkDataset{store: s, server: server, users: users, at: at}
}

func seedConnectionAuditBenchmark(b *testing.B, s *Store, server *model.Server, users []*model.User, at time.Time, rows int) {
	b.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`insert into connection_audit_reports(report_id,server_id,user_id,device_id_hash,source_ip,route_id,network,outbound_tag,connection_count,closed_count,upload_bytes,download_bytes,active_peak,collection_started_at,collection_ended_at,started_at,ended_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	for index := 0; index < rows; index++ {
		user := users[index%len(users)]
		startedAt := at.Add(-time.Duration(index%(28*24)) * time.Hour)
		ts := startedAt.Format(time.RFC3339Nano)
		if _, err := statement.Exec(fmt.Sprintf("seed-connection-%d", index), server.ID, user.ID, fmt.Sprintf("device-%d", user.ID), fmt.Sprintf("1.1.%d.%d", index%250, index%249+1), fmt.Sprintf("route-%d", index%20), "tcp", "direct", 1, 1, 1024, 2048, 1, ts, ts, ts, ts, ts); err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			b.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func seedSubscriptionAuditBenchmark(b *testing.B, s *Store, users []*model.User, at time.Time, rows int) {
	b.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`insert into subscription_pull_audits(user_id,device_id_hash,representation_id,subscription_revision,route_id,source_ip,client_name,format,token_kind,outcome,risk_eligible,requested_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	for index := 0; index < rows; index++ {
		user := users[index%len(users)]
		requestedAt := at.Add(-time.Duration(index%(28*24)) * time.Hour).Format(time.RFC3339Nano)
		if _, err := statement.Exec(user.ID, fmt.Sprintf("device-%d", user.ID), fmt.Sprintf("seed-representation-%d", index), "benchmark", fmt.Sprintf("route-%d", index%20), fmt.Sprintf("1.1.%d.%d", index%250, index%249+1), "benchmark", "sing-box", "persistent", "served", 1, requestedAt, requestedAt); err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			b.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func benchmarkAuditPolicy() model.AuditPolicy {
	policy := DefaultAuditPolicy()
	policy.Mode = "custom"
	high := model.AuditThreshold{Soft: 999999, Hard: 1000000}
	policy.RawRequestsPer60Seconds = high
	policy.LogicalPullsPer10Minutes = high
	policy.LogicalPullsPer24Hours = high
	policy.RoutesPer15Minutes = high
	policy.ClientFamiliesPer24Hours = high
	policy.ConcurrentRoutes90Secs = high
	policy.NodeFanout10Seconds = high
	policy.ProbeEpisodes10Minutes = high
	policy.ActiveConnections = high
	policy.LegacyDeviceExcess = high
	return policy
}
