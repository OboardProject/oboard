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

func TestServerMonitoringRetentionDays(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings map[string]string
		want     int
	}{
		{name: "missing", settings: nil, want: 7},
		{name: "invalid", settings: map[string]string{ServerMonitoringRetentionDaysSetting: "not-a-number"}, want: DefaultServerMonitoringRetentionDays},
		{name: "zero", settings: map[string]string{ServerMonitoringRetentionDaysSetting: "0"}, want: DefaultServerMonitoringRetentionDays},
		{name: "above maximum", settings: map[string]string{ServerMonitoringRetentionDaysSetting: "31"}, want: DefaultServerMonitoringRetentionDays},
		{name: "minimum", settings: map[string]string{ServerMonitoringRetentionDaysSetting: "1"}, want: MinServerMonitoringRetentionDays},
		{name: "maximum", settings: map[string]string{ServerMonitoringRetentionDaysSetting: "30"}, want: MaxServerMonitoringRetentionDays},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ServerMonitoringRetentionDays(test.settings); got != test.want {
				t.Fatalf("retention days = %d, want %d", got, test.want)
			}
		})
	}
}

func TestServerMonitoringMaintenanceIndexesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "monitoring-index-migration.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"idx_server_latency_probe_results_checked", "idx_server_connectivity_events_kind_time"} {
		if _, err := s.db.ExecContext(ctx, `drop index `+index); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for reopen := 0; reopen < 2; reopen++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, index := range []string{"idx_server_latency_probe_results_checked", "idx_server_connectivity_events_kind_time"} {
			var found string
			if err := s.db.QueryRowContext(ctx, `select name from sqlite_master where type='index' and name=?`, index).Scan(&found); err != nil {
				t.Fatalf("monitoring maintenance index %s missing after reopen %d: %v", index, reopen+1, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMaintenancePrunesUnifiedServerMonitoringHistory(t *testing.T) {
	s, server, _ := newMaintenanceTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	if err := s.SetSetting(ctx, ServerMonitoringRetentionDaysSetting, "7"); err != nil {
		t.Fatal(err)
	}
	cutoff := at.Add(-7 * 24 * time.Hour)
	for _, item := range []struct {
		name string
		at   time.Time
	}{
		{name: "old", at: cutoff.Add(-time.Second)},
		{name: "boundary", at: cutoff},
		{name: "new", at: cutoff.Add(time.Second)},
	} {
		insertMaintenanceMonitoringRows(t, s, server.ID, item.name, item.at)
	}
	available := true
	if _, err := insertConnectivityEvent(ctx, s.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, Available: &available, EffectiveAt: cutoff.Add(-2 * time.Second), EventKey: "monitoring-oldest-probe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := insertConnectivityEvent(ctx, s.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventServerOffline, EffectiveAt: cutoff.Add(-time.Hour), EventKey: "monitoring-old-offline"}); err != nil {
		t.Fatal(err)
	}

	result, err := s.RunMaintenance(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServerMetricSamplesDeleted != 1 || result.LatencyProbeResultsDeleted != 1 || result.ConnectivityProbesDeleted != 1 {
		t.Fatalf("monitoring deletions = metrics:%d latency:%d probes:%d, want 1 each", result.ServerMetricSamplesDeleted, result.LatencyProbeResultsDeleted, result.ConnectivityProbesDeleted)
	}
	for _, tableAndTime := range []struct {
		table, column string
	}{
		{table: "server_metric_samples", column: "sampled_at"},
		{table: "server_latency_probe_results", column: "checked_at"},
		{table: "server_connectivity_events", column: "effective_at"},
	} {
		var old, boundary, newer int
		_ = s.db.QueryRow(fmt.Sprintf(`select count(*) from %s where %s=?`, tableAndTime.table, tableAndTime.column), cutoff.Add(-time.Second).Format(time.RFC3339Nano)).Scan(&old)
		_ = s.db.QueryRow(fmt.Sprintf(`select count(*) from %s where %s=?`, tableAndTime.table, tableAndTime.column), cutoff.Format(time.RFC3339Nano)).Scan(&boundary)
		_ = s.db.QueryRow(fmt.Sprintf(`select count(*) from %s where %s=?`, tableAndTime.table, tableAndTime.column), cutoff.Add(time.Second).Format(time.RFC3339Nano)).Scan(&newer)
		wantOld := 0
		if tableAndTime.table == "server_connectivity_events" {
			wantOld = 1
		}
		if old != wantOld || boundary != 1 || newer != 1 {
			t.Fatalf("%s retention counts old=%d boundary=%d new=%d", tableAndTime.table, old, boundary, newer)
		}
	}
	if got := connectivityEventCount(t, s, server.ID, model.ConnectivityEventServerOffline); got != 1 {
		t.Fatalf("non-probe connectivity events retained = %d, want 1", got)
	}
	history, err := s.ListConnectivityHistory(ctx, server.ID, cutoff, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Baseline) == 0 || history.Baseline[len(history.Baseline)-1].Kind != model.ConnectivityEventProbeResult {
		t.Fatalf("connectivity baseline after retention = %#v", history.Baseline)
	}
}

func TestMaintenancePrunesLatencyForStoppedServer(t *testing.T) {
	s, server, _ := newMaintenanceTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	if err := s.SetSetting(ctx, ServerMonitoringRetentionDaysSetting, "7"); err != nil {
		t.Fatal(err)
	}
	server.LatencyProbeEnabled = false
	server.Status = model.ServerOffline
	if err := s.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	insertMaintenanceLatencyResult(t, s, server.ID, "stopped-old", at.Add(-8*24*time.Hour))

	result, err := s.RunMaintenance(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if result.LatencyProbeResultsDeleted != 1 {
		t.Fatalf("stopped server latency deletions = %d, want 1", result.LatencyProbeResultsDeleted)
	}
	var remaining int
	if err := s.db.QueryRow(`select count(*) from server_latency_probe_results where server_id=?`, server.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("stopped server latency results = %d, err=%v", remaining, err)
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

func insertMaintenanceMonitoringRows(t *testing.T, s *Store, serverID int64, name string, at time.Time) {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`insert into server_metric_samples(server_id,sampled_at) values(?,?)`, serverID, ts); err != nil {
		t.Fatal(err)
	}
	insertMaintenanceLatencyResult(t, s, serverID, name, at)
	available := true
	if _, err := insertConnectivityEvent(context.Background(), s.db, model.ServerConnectivityEvent{ServerID: serverID, Kind: model.ConnectivityEventProbeResult, Available: &available, EffectiveAt: at, EventKey: "monitoring-" + name}); err != nil {
		t.Fatal(err)
	}
}

func insertMaintenanceLatencyResult(t *testing.T, s *Store, serverID int64, probeID string, at time.Time) {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`insert into server_latency_probe_results(server_id,resource_version,probe_id,kind,mode,province,carrier,host,ip,port,available,latency_ms,sample_count,success_count,checked_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, serverID, "maintenance-v1", probeID, "regional", "tcp", "广东", "中国电信", "192.0.2.1", "192.0.2.1", 443, 1, 20, 3, 3, ts, ts); err != nil {
		t.Fatal(err)
	}
}
