package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// connectionAuditParityFixture seeds several users whose histories exercise
// every risk input shape: ordinary traffic, anomaly spikes, insufficient
// robust-Z history, internal probes, confirmed and candidate probe episodes,
// dropped buckets, shared routes, multiple source IPs and countries, and a
// presence-only user with no reports.
func connectionAuditParityFixture(t *testing.T, s *Store, server *model.Server) ([]*model.User, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	users := []*model.User{}
	for index := 0; index < 4; index++ {
		user := &model.User{Username: "parity-user-" + string(rune('A'+index)), PasswordHash: "x", Role: model.RoleViewer, Status: "active"}
		if err := s.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
	}
	reports := []model.ConnectionAuditReport{}
	at := now.Add(-10 * time.Minute)
	// User A: ordinary traffic across several weeks for a healthy robust-Z
	// baseline, plus a mild current hour.
	for week := 0; week < 4; week++ {
		started := now.Add(-time.Duration(week+1) * 7 * 24 * time.Hour)
		reports = append(reports, robustZReport("parity-a-history-"+string(rune('0'+week)), server.ID, users[0].ID, started, 100))
	}
	reports = append(reports, robustZReport("parity-a-current", server.ID, users[0].ID, now, 105))
	// User B: anomaly spike in the current hour.
	for week := 0; week < 4; week++ {
		started := now.Add(-time.Duration(week+1) * 7 * 24 * time.Hour)
		reports = append(reports, robustZReport("parity-b-history-"+string(rune('0'+week)), server.ID, users[1].ID, started, 100))
	}
	reports = append(reports, robustZReport("parity-b-spike", server.ID, users[1].ID, now, 1000))
	// User C: clone signal on one device across two independent networks and
	// countries, plus candidate and confirmed probe episodes and a dropped
	// bucket report.
	left := meaningfulConnectionReport("parity-c-left", server.ID, users[2].ID, "parity-c-device", "1.1.1.1", "CN", "ISP-A", at, at.Add(90*time.Second))
	right := meaningfulConnectionReport("parity-c-right", server.ID, users[2].ID, "parity-c-device", "8.8.8.8", "US", "ISP-B", at.Add(5*time.Second), at.Add(90*time.Second))
	reports = append(reports, left, right)
	dropped := meaningfulConnectionReport("parity-c-dropped", server.ID, users[2].ID, "parity-c-device", "1.1.1.1", "CN", "ISP-A", at.Add(95*time.Second), at.Add(96*time.Second))
	dropped.DroppedBucketCount = 3
	dropped.ProbeState = "confirmed"
	reports = append(reports, dropped)
	reports = append(reports, probeParityReports(server.ID, users[2].ID, "parity-c-probe", 4, 4, at.Add(-30*time.Second), "confirmed")...)
	reports = append(reports, probeParityReports(server.ID, users[2].ID, "parity-c-candidate", 4, 4, at.Add(-5*time.Second), "candidate")...)
	// User D: shared route with user A, multiple source IPs, plus an internal
	// probe that must never count as a device.
	sharedLeft := meaningfulConnectionReport("parity-d-left", server.ID, users[3].ID, "parity-d-device", "203.0.113.7", "JP", "ISP-J", at, at.Add(30*time.Second))
	sharedLeft.RouteID = "route-shared"
	reports = append(reports, sharedLeft)
	internal := meaningfulConnectionReport("parity-d-internal", server.ID, users[3].ID, "", "127.0.0.1", "", "", at, at.Add(5*time.Second))
	internal.InternalProbe = true
	reports = append(reports, internal)
	// User A shares the route with user D.
	sharedRight := meaningfulConnectionReport("parity-a-shared", server.ID, users[0].ID, "parity-a-device", "203.0.113.7", "JP", "ISP-J", at.Add(2*time.Second), at.Add(30*time.Second))
	sharedRight.RouteID = "route-shared"
	reports = append(reports, sharedRight)
	if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	// Presence-only user E: no reports at all.
	presenceUser := &model.User{Username: "parity-user-presence", PasswordHash: "x", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, presenceUser); err != nil {
		t.Fatal(err)
	}
	users = append(users, presenceUser)
	presence := model.ConnectionPresenceEvent{
		Sequence: 1, ServerID: server.ID, UserID: presenceUser.ID, DeviceIDHash: "presence-only", CredentialEpoch: 1,
		SourceIP: "9.9.9.9", RouteID: "route-presence", Network: "tcp", Event: "first_meaningful_payload",
		State: "active", ActiveConnections: 1, Meaningful: true, PayloadLastAt: now, At: now,
	}
	if _, err := s.ApplyConnectionPresenceEvents(ctx, "parity-agent", server.ID, 0, []model.ConnectionPresenceEvent{presence}); err != nil {
		t.Fatal(err)
	}
	return users, now
}

// TestConnectionAuditOverviewBatchParity proves the batched overview produces
// exactly the same per-user summaries as the per-user reference loop across
// every risk input shape.
func TestConnectionAuditOverviewBatchParity(t *testing.T) {
	s, server, _ := newMaintenanceTestStore(t)
	ctx := context.Background()
	connectionAuditParityFixture(t, s, server)

	got, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	want := legacyConnectionAuditOverviewPerUser(ctx, t, s, 24, true, DefaultAuditPolicy())
	// The two runs happen at slightly different wall-clock instants.
	got.GeneratedAt = time.Time{}
	want.GeneratedAt = time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batch overview differs from per-user reference:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestConnectionAuditOverviewBatchParityDisabledGate keeps the batch path on
// the disabled branch.
func TestConnectionAuditOverviewBatchParityDisabledGate(t *testing.T) {
	s, server, _ := newMaintenanceTestStore(t)
	ctx := context.Background()
	connectionAuditParityFixture(t, s, server)
	got, err := s.ConnectionAuditOverview(ctx, 24, false, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if got.EnabledServerCount != 0 || got.ElevatedRiskCount != 0 {
		t.Fatalf("disabled batch overview must be empty: %+v", got)
	}
}

// legacyConnectionAuditOverviewPerUser is the pre-batch per-user overview loop
// kept as the reference implementation for the parity test: every shared query
// plus one report, episode, and robust-Z query per user, evaluated through the
// same evaluateConnectionAuditUser helper as the batch driver.
func legacyConnectionAuditOverviewPerUser(ctx context.Context, t testing.TB, s *Store, windowHours int, connectionAuditEnabled bool, policy model.AuditPolicy) model.ConnectionAuditOverview {
	t.Helper()
	if windowHours < 1 {
		windowHours = 24
	}
	nowTime := time.Now().UTC()
	since := nowTime.Add(-time.Duration(windowHours) * time.Hour).Format(time.RFC3339Nano)
	if ValidateAuditPolicy(policy) != nil {
		policy = DefaultAuditPolicy()
	}
	overview := model.ConnectionAuditOverview{WindowHours: windowHours, RiskWindowMinutes: int(connectionAuditRiskWindow / time.Minute), GeneratedAt: nowTime, Policy: policy, Users: []model.ConnectionAuditUserSummary{}}
	if err := s.db.QueryRowContext(ctx, `select count(*) from servers where connection_audit_enabled=1`).Scan(&overview.EnabledServerCount); err != nil {
		t.Fatal(err)
	}
	if !connectionAuditEnabled {
		overview.EnabledServerCount = 0
	}
	rows, err := s.db.QueryContext(ctx, `select r.user_id,u.username,u.nickname,count(distinct r.source_ip),count(distinct r.server_id),coalesce(sum(r.connection_count),0),coalesce(max(r.active_peak),0),count(*),max(r.ended_at),coalesce(u.device_limit,0),(select count(*) from user_devices d where d.user_id=u.id and d.status='active')
		from connection_audit_reports r join users u on u.id=r.user_id where r.ended_at>=? group by r.user_id,u.username,u.nickname`, since)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var item model.ConnectionAuditUserSummary
		var lastSeen string
		if err := rows.Scan(&item.UserID, &item.Username, &item.Nickname, &item.SourceIPCount, &item.ServerCount, &item.ConnectionCount, &item.ActivePeak, &item.ReportCount, &lastSeen, &item.DeviceLimit, &item.RegisteredDeviceCount); err != nil {
			t.Fatal(errors.Join(err, rows.Close()))
		}
		item.LastSeenAt = parseTime(lastSeen)
		overview.Users = append(overview.Users, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	usersByID := make(map[int64]int, len(overview.Users))
	for index := range overview.Users {
		usersByID[overview.Users[index].UserID] = index
	}
	presenceByUser := map[int64][]model.ConnectionPresenceEvent{}
	presenceRows, err := s.db.QueryContext(ctx, `select p.server_id,p.user_id,p.inbound_id,p.path_id,p.device_id_hash,p.credential_epoch,p.source_ip,p.route_id,p.network,p.active_connections,p.meaningful,p.payload_last_at,p.last_event_at,p.last_sequence,p.updated_at,u.username,u.nickname,coalesce(u.device_limit,0),(select count(*) from user_devices d where d.user_id=u.id and d.status='active') from connection_presence_states p join users u on u.id=p.user_id where p.last_event_at>=? order by p.user_id,p.device_id_hash,p.source_ip,p.network`, nowTime.Add(-connectionAuditPresenceTCP).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	for presenceRows.Next() {
		var event model.ConnectionPresenceEvent
		var meaningful int
		var payloadLastAt *string
		var at, updatedAt string
		var username, nickname string
		var deviceLimit, registeredDevices int
		if err := presenceRows.Scan(&event.ServerID, &event.UserID, &event.InboundID, &event.PathID, &event.DeviceIDHash, &event.CredentialEpoch, &event.SourceIP, &event.RouteID, &event.Network, &event.ActiveConnections, &meaningful, &payloadLastAt, &at, &event.Sequence, &updatedAt, &username, &nickname, &deviceLimit, &registeredDevices); err != nil {
			t.Fatal(errors.Join(err, presenceRows.Close()))
		}
		event.Event = "current"
		event.Meaningful = meaningful == 1
		if event.ActiveConnections > 0 {
			event.State = "active"
		} else {
			event.State = "inactive"
		}
		if payloadLastAt != nil {
			event.PayloadLastAt = parseTime(*payloadLastAt)
		}
		event.At = parseTime(at)
		event.CreatedAt = parseTime(updatedAt)
		presenceByUser[event.UserID] = append(presenceByUser[event.UserID], event)
		if _, exists := usersByID[event.UserID]; !exists {
			usersByID[event.UserID] = len(overview.Users)
			overview.Users = append(overview.Users, model.ConnectionAuditUserSummary{UserID: event.UserID, Username: username, Nickname: nickname, DeviceLimit: deviceLimit, RegisteredDeviceCount: registeredDevices, LastSeenAt: event.At})
		}
	}
	if err := presenceRows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := presenceRows.Err(); err != nil {
		t.Fatal(err)
	}
	usersByIP := map[string]map[int64]struct{}{}
	sharedIPsByUser := map[int64]map[string]struct{}{}
	subnetsByUser := map[int64]map[string]struct{}{}
	ipRows, err := s.db.QueryContext(ctx, `select distinct user_id,source_ip from connection_audit_reports where ended_at>=?`, since)
	if err != nil {
		t.Fatal(err)
	}
	for ipRows.Next() {
		var userID int64
		var sourceIP string
		if err := ipRows.Scan(&userID, &sourceIP); err != nil {
			t.Fatal(errors.Join(err, ipRows.Close()))
		}
		if usersByIP[sourceIP] == nil {
			usersByIP[sourceIP] = map[int64]struct{}{}
		}
		usersByIP[sourceIP][userID] = struct{}{}
		subnet := auditSubnet(sourceIP)
		if subnet == "" {
			continue
		}
		if subnetsByUser[userID] == nil {
			subnetsByUser[userID] = map[string]struct{}{}
		}
		subnetsByUser[userID][subnet] = struct{}{}
	}
	if err := ipRows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ipRows.Err(); err != nil {
		t.Fatal(err)
	}
	for sourceIP, users := range usersByIP {
		if len(users) < 2 {
			continue
		}
		for userID := range users {
			if sharedIPsByUser[userID] == nil {
				sharedIPsByUser[userID] = map[string]struct{}{}
			}
			sharedIPsByUser[userID][sourceIP] = struct{}{}
		}
	}
	sharedRoutes, err := s.connectionAuditSharedRouteUsers(ctx, nowTime.Add(-connectionAuditRiskWindow))
	if err != nil {
		t.Fatal(err)
	}
	for index := range overview.Users {
		item := &overview.Users[index]
		presence := presenceByUser[item.UserID]
		for _, event := range presence {
			item.ActiveConnectionCount += event.ActiveConnections
			if event.At.After(item.LastSeenAt) {
				item.LastSeenAt = event.At
			}
		}
		item.SourceSubnetCount = len(subnetsByUser[item.UserID])
		item.SharedSourceIPCount = len(sharedIPsByUser[item.UserID])
		selectedReports, loadErr := s.listConnectionAuditReportsForRisk(ctx, item.UserID, nowTime.Add(-time.Duration(windowHours)*time.Hour), 50000)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		episodes, loadErr := s.listConnectionProbeEpisodes(ctx, item.UserID, nowTime.Add(-time.Duration(windowHours)*time.Hour), 200)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		robustZ, loadErr := s.connectionAuditRobustZ(ctx, item.UserID, nowTime)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		evaluateConnectionAuditUser(item, selectedReports, episodes, robustZ, presence, policy, sharedRoutes, nowTime)
		overview.TotalConnections += item.ConnectionCount
		if item.RiskScore >= 55 {
			overview.ElevatedRiskCount++
		}
	}
	overview.ReportingUserCount = len(overview.Users)
	overview.UniqueSourceIPs = len(usersByIP)
	sort.SliceStable(overview.Users, func(i, j int) bool {
		if overview.Users[i].RiskScore != overview.Users[j].RiskScore {
			return overview.Users[i].RiskScore > overview.Users[j].RiskScore
		}
		return overview.Users[i].LastSeenAt.After(overview.Users[j].LastSeenAt)
	})
	return overview
}
