package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// benchmarkDashboardStore seeds a realistic data volume: 50 servers, 1,000
// users, 100,000 tasks, and several traffic periods per user.
func benchmarkDashboardStore2(t testing.TB) *Store {
	return benchmarkDashboardStoreImpl(t)
}

func benchmarkDashboardStore(b *testing.B) *Store {
	return benchmarkDashboardStoreImpl(b)
}

func benchmarkDashboardStoreImpl(t testing.TB) *Store {

	store, err := Open(t.TempDir() + "/oboard.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	servers := make([]model.Server, 0, 50)
	for index := 0; index < 50; index++ {
		server := model.Server{Name: fmt.Sprintf("bench-server-%d", index), ListenIP: "0.0.0.0", Status: model.ServerOnline}
		if err := store.CreateServer(ctx, &server); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server)
	}
	users := make([]model.User, 0, 1000)
	for index := 0; index < 1000; index++ {
		user := model.User{Username: fmt.Sprintf("bench-user-%d", index), PasswordHash: "x", Status: "active"}
		if err := store.CreateUser(ctx, &user); err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
	}
	// 100,000 tasks spread across servers and statuses. A deployment version
	// spans the 50 servers (50 tasks); group recency correlates with id order
	// like real history. Terminal statuses dominate.
	statuses := []string{"succeeded", "succeeded", "succeeded", "succeeded", "failed", "rollback_failed", "pending", "running"}
	now := time.Now().UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100000; index++ {
		server := servers[index%len(servers)]
		status := statuses[index%len(statuses)]
		version := int64(index / len(servers))
		ts := now.Add(-time.Duration(version) * time.Minute).UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `insert into agent_tasks(server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at) values(?,?,?,?,?,?,?,?,?,?)`, server.ID, model.AgentTaskTypeApplyDeployment, `{}`, status, `{}`, version, fmt.Sprintf("bench-%d", index), ts, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A few traffic periods per user.
	periodKeys := []string{"2026-06", "2026-07", "2026-08"}
	tsTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		for periodIndex, key := range periodKeys {
			start := now.Add(-time.Duration(len(periodKeys)-periodIndex) * 30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
			end := now.Add(-time.Duration(len(periodKeys)-periodIndex-1) * 30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
			if _, err := tsTx.ExecContext(ctx, `insert into traffic_periods(user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at) values(?,?,?,?,?,?,0,'active',?)`, user.ID, key, start, end, 1024, 2048, now.UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tsTx.Commit(); err != nil {
		t.Fatal(err)
	}
	return store
}

func BenchmarkDashboardSummary(b *testing.B) {
	store := benchmarkDashboardStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Dashboard(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDashboardTaskTimeline(b *testing.B) {
	store := benchmarkDashboardStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ListDashboardTaskTimeline(ctx, 6); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDashboardTaskTimelineLegacy300(b *testing.B) {
	store := benchmarkDashboardStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ListTaskTimeline(ctx, 300); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFailTimedOutTasks(b *testing.B) {
	store := benchmarkDashboardStore(b)
	ctx := context.Background()
	now := time.Now().UTC()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.FailTimedOutTasks(ctx, now.Add(-5*time.Minute), now.Add(-5*time.Minute), `{}`, `{}`); err != nil {
			b.Fatal(err)
		}
	}
}
