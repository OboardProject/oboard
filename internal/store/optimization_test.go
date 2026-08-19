package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestPointServerLoadersUseConstantQueries(t *testing.T) {
	ctx := context.Background()
	for _, count := range []int{1, 500} {
		t.Run(fmt.Sprintf("servers_%d", count), func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var target *model.Server
			for index := 0; index < count; index++ {
				server := &model.Server{Name: fmt.Sprintf("point-%d", index), AgentID: fmt.Sprintf("point-agent-%d", index), Status: model.ServerOnline, LatencyProbeEnabled: true}
				if err := s.CreateServer(ctx, server); err != nil {
					t.Fatal(err)
				}
				target = server
			}
			if _, err := s.db.ExecContext(ctx, `update server_telemetry set traffic_upload_bytes=1234,connectivity_available=1 where server_id=?`, target.ID); err != nil {
				t.Fatal(err)
			}
			for _, load := range []struct {
				name string
				fn   func() (*model.Server, error)
			}{
				{name: "id", fn: func() (*model.Server, error) { return s.GetServer(ctx, target.ID) }},
				{name: "agent", fn: func() (*model.Server, error) { return s.GetServerByAgent(ctx, target.AgentID) }},
			} {
				t.Run(load.name, func(t *testing.T) {
					before := s.SQLStatementCount()
					loaded, err := load.fn()
					if err != nil {
						t.Fatal(err)
					}
					if delta := s.SQLStatementCount() - before; delta != 3 {
						t.Fatalf("SQL statements = %d, want 3", delta)
					}
					if loaded.ID != target.ID || loaded.TrafficUploadBytes != 1234 || loaded.ConnectivityStatus != "available" || !loaded.LatencyProbeEnabled {
						t.Fatalf("point loader returned incomplete state: %#v", loaded)
					}
				})
			}
		})
	}
}

func TestNextTaskQueryPlanUsesCompositeIndex(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var id, parent, notused int
	var detail string
	err = s.db.QueryRowContext(ctx, `explain query plan select id from agent_tasks where server_id=? and status='pending' order by id limit 1`, 1).Scan(&id, &parent, &notused, &detail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "idx_tasks_server_status_id") {
		t.Fatalf("NextTask query plan = %q, want idx_tasks_server_status_id", detail)
	}
	if strings.Contains(detail, "SCAN") {
		t.Fatalf("NextTask query plan = %q, want a pure index SEARCH", detail)
	}
}

func TestPendingTaskServerIDsReturnsDistinctServers(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	servers := []*model.Server{}
	for i := 0; i < 3; i++ {
		server := &model.Server{Name: "pending-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
		if err := s.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server)
	}
	for _, server := range servers {
		for i := 0; i < 2; i++ {
			task := &model.AgentTask{ServerID: server.ID, Type: "collect_logs", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "pending"}
			if err := s.CreateTask(ctx, task); err != nil {
				t.Fatal(err)
			}
		}
	}
	ids, err := s.PendingTaskServerIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("pending server ids = %v, want 3 distinct servers", ids)
	}
	// Claim one server's tasks; it must drop out of the scan.
	if _, err := s.NextTask(ctx, servers[0].ID); err != nil {
		t.Fatal(err)
	}
	ids, err = s.PendingTaskServerIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("pending server ids after partial claim = %v, want 3", ids)
	}
	for _, server := range servers[:1] {
		if _, err := s.NextTask(ctx, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	ids, err = s.PendingTaskServerIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("pending server ids after full claim = %v, want 2", ids)
	}
}

func TestServerMetricSampleRateLimit(t *testing.T) {
	opts := DefaultSQLiteOptions()
	opts.MetricSampleMinInterval = time.Hour
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "oboard.sqlite"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "metric-server", AgentID: "metric-agent", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	window := model.ServerTrafficWindow{Key: "2026-08-01", Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	report := func(timestamp time.Time) model.HealthReport {
		return model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, Timestamp: timestamp, CPUUsagePercent: 10, MemoryUsedBytes: 100, MemoryTotalBytes: 200}
	}
	first := report(time.Now().UTC())
	if _, _, err := s.UpsertHealthTransition(ctx, first, window); err != nil {
		t.Fatal(err)
	}
	samples, err := s.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples after first report = %d, want 1", len(samples))
	}
	// A second report inside the interval must not add a sample.
	second := first
	second.Timestamp = first.Timestamp.Add(time.Minute)
	second.CPUUsagePercent = 42
	if _, _, err := s.UpsertHealthTransition(ctx, second, window); err != nil {
		t.Fatal(err)
	}
	samples, err = s.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples after rate-limited report = %d, want 1", len(samples))
	}
	if samples[0].CPUUsagePercent != 10 {
		t.Fatalf("rate-limited sample changed stored cpu = %v", samples[0].CPUUsagePercent)
	}
	// Telemetry still advances for every report.
	var periodUp uint64
	if err := s.db.QueryRowContext(ctx, `select traffic_upload_bytes from server_telemetry where server_id=?`, server.ID).Scan(&periodUp); err != nil {
		t.Fatal(err)
	}
	// A report after the interval adds the next sample.
	third := first
	third.Timestamp = first.Timestamp.Add(2 * time.Hour)
	if _, _, err := s.UpsertHealthTransition(ctx, third, window); err != nil {
		t.Fatal(err)
	}
	samples, err = s.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples after interval = %d, want 2", len(samples))
	}
}

func TestConfigurationRevisionTracksDesiredWritesAndIgnoresOperationalActivity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	read := func() uint64 {
		revision, err := s.ConfigurationRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return revision
	}
	baseline := read()
	server := &model.Server{Name: "config-rev-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("server insert did not bump configuration revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	user := &model.User{Username: "config-rev-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("user insert did not bump configuration revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	ruleSet := &model.RoutingRuleSet{Name: "config-revision-rules", URL: "https://example.invalid/rules", Format: model.RoutingRuleSetFormatSingBoxSource, Content: []byte{}, Status: model.RoutingRuleSetStatusPending}
	if err := s.CreateRoutingRuleSet(ctx, ruleSet); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("rule-set definition did not bump configuration revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	ruleSet.Content = []byte("payload")
	ruleSet.ETag = "etag-1"
	ruleSet.Status = model.RoutingRuleSetStatusReady
	if err := s.UpdateRoutingRuleSet(ctx, ruleSet); err != nil {
		t.Fatal(err)
	}
	if after := read(); after != baseline {
		t.Fatalf("rule-set refresh result bumped configuration revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	device := &model.UserDevice{ID: "config-device-1", DeviceIDHash: "config-hash-1", UserID: user.ID, Name: "phone", TokenHash: "config-token-1", TokenPrefix: "tok", CredentialEpoch: 1, Status: "active"}
	if err := s.CreateUserDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("device insert did not bump configuration revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	if err := s.MarkUserDeviceProxyActivity(ctx, device.DeviceIDHash, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if after := read(); after != baseline {
		t.Fatalf("device activity update bumped configuration revision (%d -> %d)", baseline, after)
	}
	window := model.ServerTrafficWindow{Key: "2026-08", Start: time.Now().UTC().Add(-time.Hour), End: time.Now().UTC().Add(time.Hour)}
	if _, err := s.ApplyHealthReport(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, AgentVersion: "test", Timestamp: time.Now().UTC()}, window); err != nil {
		t.Fatal(err)
	}
	if after := read(); after != baseline {
		t.Fatalf("health report bumped configuration revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	if _, err := s.RevokeUserDevice(ctx, device.UserID, device.ID); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("device revoke did not bump configuration revision (%d -> %d)", baseline, after)
	}
}

func TestRoutingCacheRevisionTracksMutations(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	read := func() uint64 {
		revision, err := s.RoutingCacheRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return revision
	}
	baseline := read()
	server := &model.Server{Name: "rev-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("server insert did not bump routing revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	// Device activity timestamp writes must NOT invalidate the routing cache.
	user := &model.User{Username: "rev-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("user insert did not bump routing revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	device := &model.UserDevice{ID: "device-1", DeviceIDHash: "hash-device-1", UserID: user.ID, Name: "phone", TokenHash: "token-hash", TokenPrefix: "tok", CredentialEpoch: 1, Status: "active"}
	if err := s.CreateUserDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("device insert did not bump routing revision (%d -> %d)", baseline, after)
	}
	baseline = read()
	if err := s.MarkUserDeviceProxyActivity(ctx, device.DeviceIDHash, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if after := read(); after != baseline {
		t.Fatalf("device activity update bumped routing revision (%d -> %d)", baseline, after)
	}
	// Identity-relevant device mutations must invalidate.
	baseline = read()
	if _, err := s.RevokeUserDevice(ctx, device.UserID, device.ID); err != nil {
		t.Fatal(err)
	}
	if after := read(); after <= baseline {
		t.Fatalf("device status change did not bump routing revision (%d -> %d)", baseline, after)
	}
}

func TestConnectionAuditOverviewForUsersMatchesFullOverview(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "audit-server", AgentID: "audit-agent", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := s.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	users := []*model.User{}
	for i := 0; i < 5; i++ {
		user := &model.User{Username: "audit-user-" + string(rune('a'+i)), PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-11111111111" + string(rune('a'+i)), ProxyPassword: "password"}
		if err := s.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
	}
	nowTime := time.Now().UTC()
	reports := []model.ConnectionAuditReport{}
	for index, user := range users {
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: "parity-" + string(rune('a'+index)), ServerID: server.ID, UserID: user.ID, InboundID: &inbound.ID,
			SourceIP: "198.51.100." + string(rune('1'+index)), Network: "tcp", Destination: "example.com", DestinationPort: 443,
			ConnectionCount: 5, ClosedCount: 5, DurationTotalMS: 3000, DurationMaxMS: 1000, DurationLE1SCount: 5, ActivePeak: 3,
			PresenceSequence: 1, BucketCapacity: 4096, CollectionStartedAt: nowTime.Add(-time.Minute), CollectionEndedAt: nowTime,
			StartedAt: nowTime.Add(-time.Second), EndedAt: nowTime,
		})
	}
	if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	full, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	subset := []int64{users[1].ID, users[3].ID}
	partial, err := s.ConnectionAuditOverviewForUsers(ctx, 24, true, DefaultAuditPolicy(), subset)
	if err != nil {
		t.Fatal(err)
	}
	if partial.ReportingUserCount != len(subset) || len(partial.Users) != len(subset) {
		t.Fatalf("partial overview users = %d/%d, want %d", len(partial.Users), partial.ReportingUserCount, len(subset))
	}
	byID := map[int64]model.ConnectionAuditUserSummary{}
	for _, item := range partial.Users {
		byID[item.UserID] = item
	}
	for _, userID := range subset {
		fullItem, partialItem := model.ConnectionAuditUserSummary{}, model.ConnectionAuditUserSummary{}
		for _, item := range full.Users {
			if item.UserID == userID {
				fullItem = item
			}
		}
		partialItem = byID[userID]
		if partialItem.UserID == 0 {
			t.Fatalf("partial overview missing user %d", userID)
		}
		if fullItem.RiskScore != partialItem.RiskScore || fullItem.RiskLevel != partialItem.RiskLevel || fullItem.Confidence != partialItem.Confidence || fullItem.ConnectionCount != partialItem.ConnectionCount || fullItem.SourceIPCount != partialItem.SourceIPCount {
			t.Fatalf("user %d risk mismatch: full=%+v partial=%+v", userID, fullItem, partialItem)
		}
	}
	if partial.TotalConnections != 10 {
		t.Fatalf("partial total connections = %d, want 10", partial.TotalConnections)
	}
}

func TestAccessLifecycleNextDue(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "lifecycle-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	if due, err := s.AccessLifecycleNextDue(ctx, nowTime); err != nil || due != nil {
		t.Fatalf("next due without schedules = %v, %v", due, err)
	}
	future := nowTime.Add(2 * time.Hour)
	plan := &model.SubscriptionPlan{Name: "lifecycle-plan", Enabled: true}
	if err := s.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	binding := model.UserPlanBinding{UserID: user.ID, PlanID: plan.ID, Enabled: true, StartsAt: &future}
	if err := s.SetUserPlanBindings(ctx, []model.UserPlanBinding{binding}); err != nil {
		t.Fatal(err)
	}
	due, err := s.AccessLifecycleNextDue(ctx, nowTime)
	if err != nil {
		t.Fatal(err)
	}
	if due == nil || !due.UTC().Equal(future) {
		t.Fatalf("next due = %v, want %v", due, future)
	}
}
