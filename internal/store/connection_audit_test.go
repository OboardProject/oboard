package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConnectionAuditDefaultsToDisabledForNewServer(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "audit-default-node", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectionAuditEnabled {
		t.Fatal("new server unexpectedly enabled connection audit")
	}
}

func TestConnectionAuditOverviewEnabledServerCountRespectsGlobalGate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "audit-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if enabled.EnabledServerCount != 1 {
		t.Fatalf("enabled server count = %d, want 1", enabled.EnabledServerCount)
	}
	gated, err := s.ConnectionAuditOverview(ctx, 24, false, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if gated.EnabledServerCount != 0 {
		t.Fatalf("gated enabled server count = %d, want 0", gated.EnabledServerCount)
	}
}

func TestConnectionPresenceIsIdempotentAndDrivesRealtimeOnlineDevices(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "presence-node", AgentID: "presence-agent", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "presence-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "presence-uuid", ProxyPassword: "presence-secret"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	active := model.ConnectionPresenceEvent{Sequence: 1, ServerID: server.ID, UserID: user.ID, InboundID: 11, DeviceIDHash: "device-a", CredentialEpoch: 1, SourceIP: "198.51.100.10", RouteID: "route-a", Network: "tcp", Event: "first_meaningful_payload", State: "active", ActiveConnections: 2, Meaningful: true, PayloadLastAt: nowTime, At: nowTime}
	for attempt := 0; attempt < 2; attempt++ {
		accepted, err := s.ApplyConnectionPresenceEvents(ctx, server.AgentID, server.ID, 0, []model.ConnectionPresenceEvent{active})
		if err != nil || len(accepted) != 1 || accepted[0] != 1 {
			t.Fatalf("presence attempt %d = %#v, %v", attempt, accepted, err)
		}
	}
	var eventCount int
	if err := s.db.QueryRowContext(ctx, `select count(*) from connection_presence_events where agent_id=? and sequence=?`, server.AgentID, active.Sequence).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("presence event count = %d, %v", eventCount, err)
	}
	overview, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Users) != 1 || overview.Users[0].OnlineDeviceCount != 1 || overview.Users[0].ActiveConnectionCount != 2 || overview.Users[0].ReportCount != 0 {
		t.Fatalf("realtime overview = %#v", overview)
	}

	closed := active
	closed.Sequence = 2
	closed.Event = "last_connection_closed"
	closed.State = "inactive"
	closed.ActiveConnections = 0
	closed.At = nowTime.Add(time.Second)
	if _, err := s.ApplyConnectionPresenceEvents(ctx, server.AgentID, server.ID, 0, []model.ConnectionPresenceEvent{closed}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Presence) != 1 || detail.Presence[0].State != "inactive" || detail.Summary.OnlineDeviceCount != 1 || detail.Summary.ActiveConnectionCount != 0 {
		t.Fatalf("recent closed presence = %#v", detail)
	}
}

func TestConnectionAuditReportsAreIdempotentAndRiskIsAggregated(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "audit-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "shared-user", Nickname: "Shared", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-audit", ProxyPassword: "secret"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	reports := make([]model.ConnectionAuditReport, 0, 15)
	for index := 0; index < 15; index++ {
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: fmt.Sprintf("audit-%d", index), ServerID: server.ID, UserID: user.ID,
			SourceIP: fmt.Sprintf("203.%d.0.1", index), Network: "tcp", Destination: "example.com", DestinationPort: 443,
			OutboundTag: "direct", OutboundType: "direct", ConnectionCount: 100, ActivePeak: 20,
			StartedAt: nowTime.Add(-time.Minute), EndedAt: nowTime,
		})
	}
	accepted, err := s.AddConnectionAuditReports(ctx, reports)
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != len(reports) {
		t.Fatalf("accepted = %d, want %d", len(accepted), len(reports))
	}
	if accepted, err = s.AddConnectionAuditReports(ctx, reports[:1]); err != nil || len(accepted) != 1 {
		t.Fatalf("idempotent retry = %v, %v", accepted, err)
	}
	retry, err := s.AddConnectionAuditReportsResult(ctx, reports[:1])
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.AcceptedReportIDs) != 1 || len(retry.InsertedReportIDs) != 0 || len(retry.InsertedUserIDs) != 0 {
		t.Fatalf("retry result = %#v", retry)
	}
	overview, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if overview.EnabledServerCount != 1 || overview.ReportingUserCount != 1 || overview.TotalConnections != 1500 || overview.UniqueSourceIPs != 15 {
		t.Fatalf("unexpected overview: %#v", overview)
	}
	got := overview.Users[0]
	if got.RiskLevel != "normal" || got.RiskScore != 0 || got.SourceSubnetCount != 15 {
		t.Fatalf("unexpected risk summary: %#v", got)
	}
	if len(got.RiskSignals) != 0 {
		t.Fatalf("IP, server, and ordinary connection counts added risk: %#v", got.RiskSignals)
	}
	detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Sources) != 15 || len(detail.Recent) != 15 || len(detail.Destinations) != 1 || len(detail.Outbounds) != 1 {
		t.Fatalf("unexpected detail dimensions: sources=%d recent=%d destinations=%d outbounds=%d", len(detail.Sources), len(detail.Recent), len(detail.Destinations), len(detail.Outbounds))
	}
	if detail.Outbounds[0].Label != "direct" || detail.Outbounds[0].ConnectionCount != 1500 {
		t.Fatalf("unexpected outbound aggregate: %#v", detail.Outbounds[0])
	}
	newReport := reports[0]
	newReport.ReportID = "audit-new"
	inserted, err := s.AddConnectionAuditReportsResult(ctx, []model.ConnectionAuditReport{newReport})
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted.AcceptedReportIDs) != 1 || len(inserted.InsertedReportIDs) != 1 || len(inserted.InsertedUserIDs) != 1 || inserted.InsertedUserIDs[0] != user.ID {
		t.Fatalf("insert result = %#v", inserted)
	}
}

func TestConnectionAuditCloneRequiresBoundDeviceAndOverlappingPayload(t *testing.T) {
	base := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	reports := []model.ConnectionAuditReport{
		meaningfulConnectionReport("clone-a", 1, 7, "device-a", "1.1.1.1", "CN", "ISP-A", base, base.Add(90*time.Second)),
		meaningfulConnectionReport("clone-b", 2, 7, "device-a", "8.8.8.8", "US", "ISP-B", base.Add(10*time.Second), base.Add(90*time.Second)),
	}
	events := buildConnectionAuditRiskEvents(reports, DefaultAuditPolicy(), nil)
	if len(events) != 1 || events[0].Kind != "device_clone" || events[0].CloneConfidence != 0.8 || events[0].OverlapSecs != 80 {
		t.Fatalf("clone event = %#v", events)
	}
	reports[1].DeviceIDHash = ""
	if events := buildConnectionAuditRiskEvents(reports, DefaultAuditPolicy(), nil); len(events) != 0 {
		t.Fatalf("legacy identity created clone event: %#v", events)
	}
	reports[1].DeviceIDHash = "device-a"
	reports[1].PayloadFirstAt = base.Add(2 * time.Minute)
	reports[1].PayloadLastAt = base.Add(3 * time.Minute)
	if events := buildConnectionAuditRiskEvents(reports, DefaultAuditPolicy(), nil); len(events) != 0 {
		t.Fatalf("non-overlapping routes created clone event: %#v", events)
	}
}

func TestConnectionAuditProvinceAndIPChangesAreAdvisoryOnly(t *testing.T) {
	base := time.Now().UTC()
	reports := []model.ConnectionAuditReport{
		meaningfulConnectionReport("province-a", 1, 7, "device-a", "1.1.1.1", "CN", "same-isp", base, base.Add(90*time.Second)),
		meaningfulConnectionReport("province-b", 2, 7, "device-a", "1.0.0.1", "CN", "same-isp", base, base.Add(90*time.Second)),
	}
	reports[0].SourceProvince = "广东"
	reports[1].SourceProvince = "北京"
	if events := buildConnectionAuditRiskEvents(reports, DefaultAuditPolicy(), nil); len(events) != 0 {
		t.Fatalf("same-network province change created risk: %#v", events)
	}
}

func TestConnectionAuditRiskIgnoresNonPublicSources(t *testing.T) {
	for _, raw := range []string{"10.0.0.1", "100.64.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1"} {
		if connectionAuditPublicSourceIP(raw) {
			t.Fatalf("%s was accepted as a public risk source", raw)
		}
	}
}

func TestConnectionAuditOverviewKeepsHistoricalCloneEvent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "geo-risk-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "geo-risk-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "geo-risk-uuid", ProxyPassword: "secret"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-2 * time.Hour)
	reports := []model.ConnectionAuditReport{
		meaningfulConnectionReport("clone-cn", server.ID, user.ID, "device-cloned", "1.1.1.1", "CN", "ISP-A", base, base.Add(90*time.Second)),
		meaningfulConnectionReport("clone-us", server.ID, user.ID, "device-cloned", "8.8.8.8", "US", "ISP-B", base.Add(10*time.Second), base.Add(90*time.Second)),
	}
	if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	overview, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Users) != 1 || overview.Users[0].CloneConfidence != 0.8 || overview.Users[0].RiskRegionCount != 2 || overview.Users[0].RiskWindowEndedAt == nil {
		t.Fatalf("historical clone risk = %#v", overview.Users)
	}
	detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.RiskEvents) != 1 || detail.RiskEvents[0].Kind != "device_clone" {
		t.Fatalf("historical risk events = %#v", detail.RiskEvents)
	}
}

func TestConnectionAuditDetectsSharedSourceIPsAcrossUsers(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "shared-ip-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	users := []*model.User{
		{Username: "shared-ip-a", PasswordHash: "hash-a", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-a", ProxyPassword: "secret-a"},
		{Username: "shared-ip-b", PasswordHash: "hash-b", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-b", ProxyPassword: "secret-b"},
		{Username: "shared-ip-c", PasswordHash: "hash-c", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-c", ProxyPassword: "secret-c"},
	}
	for _, user := range users {
		if err := s.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	nowTime := time.Now().UTC()
	for userIndex, user := range users {
		reports := make([]model.ConnectionAuditReport, 0, 2)
		for ipIndex, sourceIP := range []string{"198.51.100.20", "198.51.100.21"} {
			reports = append(reports, model.ConnectionAuditReport{
				ReportID: fmt.Sprintf("shared-ip-%d-%d", userIndex, ipIndex), ServerID: server.ID, UserID: user.ID,
				SourceIP: sourceIP, Network: "tcp", Destination: "example.com", DestinationPort: 443,
				ConnectionCount: 1, ActivePeak: 1, StartedAt: nowTime.Add(-time.Minute), EndedAt: nowTime,
			})
		}
		if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
			t.Fatal(err)
		}
	}
	overview, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Users) != 3 {
		t.Fatalf("users = %#v", overview.Users)
	}
	for _, item := range overview.Users {
		if item.SharedSourceIPCount != 2 {
			t.Fatalf("shared source count for user %d = %d", item.UserID, item.SharedSourceIPCount)
		}
		if item.RiskScore != 0 || len(item.RiskSignals) != 0 {
			t.Fatalf("shared source added risk for user %d: %#v", item.UserID, item)
		}
		if len(item.CounterEvidence) == 0 {
			t.Fatalf("shared source was not retained as counter-evidence for user %d", item.UserID)
		}
	}
}

func TestConnectionAuditProbeEpisodeExcludesAllNodeFanout(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "probe-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "probe-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "probe-uuid", ProxyPassword: "secret"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-30 * time.Second)
	reports := make([]model.ConnectionAuditReport, 0, 50)
	for index := 0; index < 50; index++ {
		started := base.Add(time.Duration(index) * 100 * time.Millisecond)
		report := meaningfulConnectionReport(fmt.Sprintf("probe-%d", index), server.ID, user.ID, "device-probe", "1.1.1.1", "CN", "ISP-A", started, started.Add(2*time.Second))
		report.OutboundTag = fmt.Sprintf("node-%d", index)
		reports = append(reports, report)
	}
	if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.ProbeEpisodes) != 1 || detail.ProbeEpisodes[0].State != "confirmed" || detail.ProbeEpisodes[0].NodeCount != 50 {
		t.Fatalf("probe episodes = %#v", detail.ProbeEpisodes)
	}
	if detail.Summary.NodeFanout != 0 || detail.Summary.OnlineDeviceCount != 0 || detail.Summary.RiskScore != 0 {
		t.Fatalf("confirmed probe leaked into device/risk counts: %#v", detail.Summary)
	}
}

func TestConnectionAuditProbeBudgetOverflowBackfillsFanout(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "probe-abuse-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "probe-abuse-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "probe-abuse-uuid", ProxyPassword: "secret"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-30 * time.Second)
	reports := make([]model.ConnectionAuditReport, 0, 20)
	for index := 0; index < 20; index++ {
		started := base.Add(time.Duration(index) * 100 * time.Millisecond)
		report := meaningfulConnectionReport(fmt.Sprintf("probe-abuse-%d", index), server.ID, user.ID, "device-abuse", "8.8.8.8", "US", "ISP-B", started, started.Add(2*time.Second))
		report.OutboundTag = fmt.Sprintf("node-%d", index)
		report.DownloadBytes = 512 * 1024
		reports = append(reports, report)
	}
	if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.ProbeEpisodes) != 1 || detail.ProbeEpisodes[0].State != "normal_traffic" {
		t.Fatalf("overflow episode = %#v", detail.ProbeEpisodes)
	}
	if detail.Summary.NodeFanout != 20 || len(detail.Summary.RiskSignals) == 0 {
		t.Fatalf("overflow traffic was not backfilled: %#v", detail.Summary)
	}
}

func TestConnectionAuditOnlineDeviceUsesStableIdentity(t *testing.T) {
	at := time.Now().UTC()
	reports := []model.ConnectionAuditReport{
		meaningfulConnectionReport("online-a", 1, 7, "device-one", "1.1.1.1", "CN", "ISP-A", at.Add(-40*time.Second), at.Add(-5*time.Second)),
		meaningfulConnectionReport("online-b", 2, 7, "device-one", "8.8.8.8", "US", "ISP-B", at.Add(-30*time.Second), at.Add(-4*time.Second)),
		meaningfulConnectionReport("online-c", 3, 7, "device-one", "9.9.9.9", "DE", "ISP-C", at.Add(-20*time.Second), at.Add(-3*time.Second)),
	}
	var exact model.ConnectionAuditUserSummary
	quality := connectionAuditOnlineDevices(&exact, reports, nil, at)
	if quality != 1 || exact.IdentityMode != "device_bound" || exact.OnlineDeviceCount != 1 || exact.OnlineDeviceLower != 1 || exact.OnlineDeviceUpper != 1 {
		t.Fatalf("exact online devices = %#v quality=%v", exact, quality)
	}
	for index := range reports {
		reports[index].DeviceIDHash = ""
	}
	var legacy model.ConnectionAuditUserSummary
	quality = connectionAuditOnlineDevices(&legacy, reports, nil, at)
	if quality != 0.4 || legacy.IdentityMode != "legacy_unbound" || legacy.OnlineDeviceLower != 1 || legacy.OnlineDeviceEstimate <= 1 || legacy.OnlineDeviceUpper != 3 {
		t.Fatalf("legacy online interval = %#v quality=%v", legacy, quality)
	}
}

func TestConnectionAuditMaintenancePrunesReportsOutsideRetention(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "retention-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "retention-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "retention-uuid", ProxyPassword: "retention-secret"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	reports := []model.ConnectionAuditReport{
		{ReportID: "expired-audit", ServerID: server.ID, UserID: user.ID, SourceIP: "198.51.100.40", Network: "tcp", ConnectionCount: 1, ActivePeak: 1, StartedAt: nowTime.Add(-31*24*time.Hour - time.Minute), EndedAt: nowTime.Add(-31 * 24 * time.Hour)},
		{ReportID: "current-audit", ServerID: server.ID, UserID: user.ID, SourceIP: "198.51.100.41", Network: "tcp", ConnectionCount: 1, ActivePeak: 1, StartedAt: nowTime.Add(-time.Minute), EndedAt: nowTime},
	}
	if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunMaintenance(ctx, nowTime); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from connection_audit_reports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retained audit rows = %d, want 1", count)
	}
}

func meaningfulConnectionReport(id string, serverID, userID int64, deviceID, sourceIP, country, isp string, payloadStart, payloadEnd time.Time) model.ConnectionAuditReport {
	duration := payloadEnd.Sub(payloadStart)
	report := model.ConnectionAuditReport{
		ReportID: id, ServerID: serverID, UserID: userID, DeviceIDHash: deviceID, CredentialEpoch: 1,
		SourceIP: sourceIP, RouteID: "route-" + sourceIP, SourceCountryCode: country, SourceCountry: country, SourceISP: isp, GeoDatabaseRevision: "test",
		Network: "tcp", Destination: "example.com", DestinationPort: 443, OutboundTag: "direct", OutboundType: "direct",
		ConnectionCount: 1, ClosedCount: 1, DurationTotalMS: duration.Milliseconds(), DurationMaxMS: duration.Milliseconds(),
		UploadBytes: 512, DownloadBytes: 512, PayloadFirstAt: payloadStart, PayloadLastAt: payloadEnd,
		PresenceSequence: 1, ActivePeak: 1, BucketCapacity: 4096,
		CollectionStartedAt: payloadStart, CollectionEndedAt: payloadEnd, StartedAt: payloadStart, EndedAt: payloadEnd,
	}
	switch {
	case duration <= time.Second:
		report.DurationLE1SCount = 1
	case duration <= 5*time.Second:
		report.DurationLE5SCount = 1
	case duration <= 20*time.Second:
		report.DurationLE20SCount = 1
	default:
		report.DurationGT20SCount = 1
	}
	return report
}
