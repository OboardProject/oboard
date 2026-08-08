package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func newMaintenanceTestStore(t *testing.T) (*Store, *model.Server, *model.User) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	server := &model.Server{Name: "maintenance-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "maintenance-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "maintenance-uuid", ProxyPassword: "maintenance-password", SubscriptionToken: "maintenance-token"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	return s, server, user
}

func insertMaintenanceConnectionAudit(t *testing.T, s *Store, serverID, userID int64, reportID string, endedAt time.Time) {
	t.Helper()
	ts := endedAt.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`insert into connection_audit_reports(report_id,server_id,user_id,source_ip,network,connection_count,collection_started_at,collection_ended_at,started_at,ended_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, reportID, serverID, userID, "203.0.113.1", "tcp", 1, ts, ts, ts, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func insertMaintenanceSubscriptionAudit(t *testing.T, s *Store, userID int64, requestedAt time.Time) int64 {
	t.Helper()
	ts := requestedAt.UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(`insert into subscription_pull_audits(user_id,source_ip,outcome,requested_at,created_at) values(?,?,?,?,?)`, userID, "203.0.113.1", "served", ts, ts)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMaintenancePrunesConnectionAudits(t *testing.T) {
	s, server, user := newMaintenanceTestStore(t)
	at := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	old := at.Add(-connectionAuditRetention - time.Second)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ts := old.Format(time.RFC3339Nano)
	for i := 0; i < maintenanceBatchSize+1; i++ {
		if _, err := tx.Exec(`insert into connection_audit_reports(report_id,server_id,user_id,source_ip,network,connection_count,collection_started_at,collection_ended_at,started_at,ended_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("old-%d", i), server.ID, user.ID, "203.0.113.1", "tcp", 1, ts, ts, ts, ts, ts); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	insertMaintenanceConnectionAudit(t, s, server.ID, user.ID, "boundary", at.Add(-connectionAuditRetention))
	insertMaintenanceConnectionAudit(t, s, server.ID, user.ID, "new", at.Add(-connectionAuditRetention+time.Second))
	result, err := s.RunMaintenance(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConnectionAuditsDeleted != maintenanceBatchSize+1 {
		t.Fatalf("deleted = %d, want %d", result.ConnectionAuditsDeleted, maintenanceBatchSize+1)
	}
	var remaining int
	if err := s.db.QueryRow(`select count(*) from connection_audit_reports`).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("remaining = %d, err=%v", remaining, err)
	}
}

func TestMaintenancePrunesSubscriptionAudits(t *testing.T) {
	s, _, user := newMaintenanceTestStore(t)
	at := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	oldID := insertMaintenanceSubscriptionAudit(t, s, user.ID, at.Add(-subscriptionAuditRetention-time.Second))
	boundaryID := insertMaintenanceSubscriptionAudit(t, s, user.ID, at.Add(-subscriptionAuditRetention))
	newID := insertMaintenanceSubscriptionAudit(t, s, user.ID, at.Add(-subscriptionAuditRetention+time.Second))
	result, err := s.RunMaintenance(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	if result.SubscriptionAuditsDeleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.SubscriptionAuditsDeleted)
	}
	assertMaintenanceIDs(t, s, "subscription_pull_audits", "id", []int64{boundaryID, newID}, oldID)
}

func TestMaintenancePrunesProbeEpisodes(t *testing.T) {
	s, _, user := newMaintenanceTestStore(t)
	at := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id string
		at time.Time
	}{{"old", at.Add(-connectionAuditRetention - time.Second)}, {"boundary", at.Add(-connectionAuditRetention)}, {"new", at.Add(-connectionAuditRetention + time.Second)}} {
		ts := item.at.Format(time.RFC3339Nano)
		if _, err := s.db.Exec(`insert into connection_probe_episodes(id,user_id,state,score,node_count,connection_count,started_at,ended_at,updated_at) values(?,?,?,?,?,?,?,?,?)`, item.id, user.ID, "candidate", 1, 4, 4, ts, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.RunMaintenance(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProbeEpisodesDeleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.ProbeEpisodesDeleted)
	}
	var old, kept int
	_ = s.db.QueryRow(`select count(*) from connection_probe_episodes where id='old'`).Scan(&old)
	_ = s.db.QueryRow(`select count(*) from connection_probe_episodes where id in ('boundary','new')`).Scan(&kept)
	if old != 0 || kept != 2 {
		t.Fatalf("old=%d kept=%d", old, kept)
	}
}

func TestMaintenancePrunesRateBuckets(t *testing.T) {
	s, _, _ := newMaintenanceTestStore(t)
	at := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		key string
		at  time.Time
	}{{"old", at.Add(-subscriptionBucketRetention - time.Second)}, {"boundary", at.Add(-subscriptionBucketRetention)}, {"new", at.Add(-subscriptionBucketRetention + time.Second)}} {
		if _, err := s.db.Exec(`insert into subscription_rate_buckets(bucket_key,level,updated_at) values(?,?,?)`, item.key, 1, item.at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.RunMaintenance(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	if result.RateBucketsDeleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.RateBucketsDeleted)
	}
	var old, kept int
	_ = s.db.QueryRow(`select count(*) from subscription_rate_buckets where bucket_key='old'`).Scan(&old)
	_ = s.db.QueryRow(`select count(*) from subscription_rate_buckets where bucket_key in ('boundary','new')`).Scan(&kept)
	if old != 0 || kept != 2 {
		t.Fatalf("old=%d kept=%d", old, kept)
	}
}

func TestAuditWritesDoNotRunRetentionCleanup(t *testing.T) {
	s, server, user := newMaintenanceTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	oldAt := at.Add(-connectionAuditRetention - time.Hour)
	insertMaintenanceConnectionAudit(t, s, server.ID, user.ID, "old-hot-path", oldAt)
	oldSubscriptionID := insertMaintenanceSubscriptionAudit(t, s, user.ID, oldAt)
	if _, err := s.db.Exec(`insert into subscription_rate_buckets(bucket_key,level,updated_at) values(?,?,?)`, "old-hot-path", 1, oldAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	report := model.ConnectionAuditReport{ReportID: "new-hot-path", ServerID: server.ID, UserID: user.ID, SourceIP: "203.0.113.2", Network: "tcp", ConnectionCount: 1, CollectionStartedAt: at, CollectionEndedAt: at, StartedAt: at, EndedAt: at}
	if _, err := s.AddConnectionAuditReports(ctx, []model.ConnectionAuditReport{report}); err != nil {
		t.Fatal(err)
	}
	policy := DefaultSubscriptionAuditPolicy()
	event := subscriptionAuditEvent(user.ID, "203.0.113.3", "广东", at)
	if _, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, event, policy, SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionRestrict}); err != nil {
		t.Fatal(err)
	}
	rejected := subscriptionAuditEvent(user.ID, "203.0.113.4", "广东", at.Add(time.Second))
	if err := s.AddRejectedSubscriptionPullAudit(ctx, user.SubscriptionToken, rejected); err != nil {
		t.Fatal(err)
	}
	var oldConnections, oldSubscriptions, oldBuckets int
	_ = s.db.QueryRow(`select count(*) from connection_audit_reports where report_id='old-hot-path'`).Scan(&oldConnections)
	_ = s.db.QueryRow(`select count(*) from subscription_pull_audits where id=?`, oldSubscriptionID).Scan(&oldSubscriptions)
	_ = s.db.QueryRow(`select count(*) from subscription_rate_buckets where bucket_key='old-hot-path'`).Scan(&oldBuckets)
	if oldConnections != 1 || oldSubscriptions != 1 || oldBuckets != 1 {
		t.Fatalf("hot path pruned retention data: connection=%d subscription=%d bucket=%d", oldConnections, oldSubscriptions, oldBuckets)
	}
	if _, err := s.RunMaintenance(ctx, at); err != nil {
		t.Fatal(err)
	}
	_ = s.db.QueryRow(`select count(*) from connection_audit_reports where report_id='old-hot-path'`).Scan(&oldConnections)
	_ = s.db.QueryRow(`select count(*) from subscription_pull_audits where id=?`, oldSubscriptionID).Scan(&oldSubscriptions)
	_ = s.db.QueryRow(`select count(*) from subscription_rate_buckets where bucket_key='old-hot-path'`).Scan(&oldBuckets)
	if oldConnections != 0 || oldSubscriptions != 0 || oldBuckets != 0 {
		t.Fatalf("maintenance retained expired data: connection=%d subscription=%d bucket=%d", oldConnections, oldSubscriptions, oldBuckets)
	}
}

func assertMaintenanceIDs(t *testing.T, s *Store, table, column string, kept []int64, removed int64) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(fmt.Sprintf(`select count(*) from %s where %s=?`, table, column), removed).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed id count = %d, err=%v", count, err)
	}
	for _, id := range kept {
		if err := s.db.QueryRow(fmt.Sprintf(`select count(*) from %s where %s=?`, table, column), id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("kept id %d count = %d, err=%v", id, count, err)
		}
	}
}
