package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// connectionAuditBenchmarkStore seeds a connection-audit corpus with the given
// user count and reports per user. The reports are spread across the last
// twenty-eight days so the robust-Z history has data, with the newest hour
// carrying the current bucket.
func connectionAuditBenchmarkStore(b testing.TB, users, reportsPerUser int) *Store {
	b.Helper()
	store, err := Open(b.TempDir() + "/oboard.sqlite")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	ctx := context.Background()
	server := &model.Server{Name: "audit-bench-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := store.CreateServer(ctx, server); err != nil {
		b.Fatal(err)
	}
	now := time.Now().UTC()
	reportIDs := make([]string, users*reportsPerUser)
	for index := range reportIDs {
		reportIDs[index] = fmt.Sprintf("bench-audit-%d", index)
	}
	usersModel := make([]*model.User, users)
	for index := range usersModel {
		user := &model.User{Username: fmt.Sprintf("audit-bench-user-%d", index), PasswordHash: "x", Role: model.RoleViewer, Status: "active"}
		if err := store.CreateUser(ctx, user); err != nil {
			b.Fatal(err)
		}
		usersModel[index] = user
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	for userIndex, user := range usersModel {
		for reportIndex := 0; reportIndex < reportsPerUser; reportIndex++ {
			offset := -time.Duration(reportIndex%(28*24)) * time.Hour
			started := now.Add(offset)
			ended := started.Add(time.Minute)
			ts := started.UTC().Format(time.RFC3339Nano)
			tsEnd := ended.UTC().Format(time.RFC3339Nano)
			connectionCount := int64(5 + reportIndex%20)
			if _, err := tx.ExecContext(ctx, `insert into connection_audit_reports(report_id,server_id,user_id,source_ip,network,connection_count,closed_count,active_peak,bucket_capacity,collection_started_at,collection_ended_at,started_at,ended_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				reportIDs[userIndex*reportsPerUser+reportIndex], server.ID, user.ID, fmt.Sprintf("203.0.113.%d", userIndex%200+1), "tcp", connectionCount, connectionCount, 1, 4096, ts, tsEnd, ts, tsEnd, ts); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return store
}

func BenchmarkConnectionAuditOverviewSmall(b *testing.B) {
	store := connectionAuditBenchmarkStore(b, 10, 1000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConnectionAuditOverviewMedium(b *testing.B) {
	store := connectionAuditBenchmarkStore(b, 50, 10000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConnectionAuditOverviewPerUser is the pre-batch N+1 implementation
// kept as the reference: run with -bench=ConnectionAuditOverviewPerUser on the
// same fixture to compare against the batch path.
func BenchmarkConnectionAuditOverviewPerUser(b *testing.B) {
	store := connectionAuditBenchmarkStore(b, 50, 10000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		legacyConnectionAuditOverviewPerUser(ctx, b, store, 24, true, DefaultAuditPolicy())
	}
}
