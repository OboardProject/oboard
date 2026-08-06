package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type fakeConnectionAuditGeoResolver struct{ geo model.IPGeography }

func (f *fakeConnectionAuditGeoResolver) Lookup(string) (model.IPGeography, error) { return f.geo, nil }
func (f *fakeConnectionAuditGeoResolver) Status() model.GeoDatabaseStatus {
	return model.GeoDatabaseStatus{Available: true, Provider: "test", Revision: f.geo.Revision}
}
func (f *fakeConnectionAuditGeoResolver) Close() {}

func TestConnectionAuditReportIsEnrichedByControllerGeoDatabase(t *testing.T) {
	srv := &Server{geoIP: &fakeConnectionAuditGeoResolver{geo: model.IPGeography{CountryCode: "CN", Country: "中国", Province: "广东", City: "广州", ISP: "测试运营商", Revision: "test-revision"}}}
	report := model.ConnectionAuditReport{SourceIP: "1.1.1.1"}
	srv.enrichConnectionAuditReport(&report)
	if report.SourceCountryCode != "CN" || report.SourceProvince != "广东" || report.SourceCity != "广州" || report.GeoDatabaseRevision != "test-revision" {
		t.Fatalf("enriched report = %#v", report)
	}
}

func TestValidateConnectionAuditItem(t *testing.T) {
	nowTime := time.Now().UTC()
	item := connectionAuditReportItem{
		ReportID: "report-1", UserID: 7, SourceIP: "::ffff:198.51.100.7", SourceGeoCode: "us", Network: "TCP",
		Destination: "example.com", DestinationPort: 443, OutboundTag: "direct", OutboundType: "direct",
		ConnectionCount: 2, ClosedCount: 2, DurationTotalMS: 1200, DurationMaxMS: 700, DurationLE1SCount: 2, ActivePeak: 1, PresenceSequence: 1,
		BucketCapacity: 4096, CollectionStartedAt: nowTime.Add(-time.Minute).Format(time.RFC3339Nano), CollectionEndedAt: nowTime.Format(time.RFC3339Nano),
		StartedAt: nowTime.Add(-time.Second).Format(time.RFC3339Nano), EndedAt: nowTime.Format(time.RFC3339Nano),
	}
	report, err := validateConnectionAuditItem(item, 3)
	if err != nil {
		t.Fatal(err)
	}
	if report.ServerID != 3 || report.SourceIP != "198.51.100.7" || report.SourceGeoCode != "US" || report.Network != "tcp" {
		t.Fatalf("unexpected normalized report: %#v", report)
	}
	item.SourceIP = "not-an-ip"
	if _, err := validateConnectionAuditItem(item, 3); err == nil {
		t.Fatal("invalid source IP was accepted")
	}
	item.SourceIP = "198.51.100.7"
	item.ActiveAtEnd = 2
	if _, err := validateConnectionAuditItem(item, 3); err == nil {
		t.Fatal("active_at_end greater than active_peak was accepted")
	}
	item.ActiveAtEnd = 0
	item.CollectionGeneration = math.MaxInt64 + 1
	if _, err := validateConnectionAuditItem(item, 3); err == nil {
		t.Fatal("collection generation outside SQLite integer range was accepted")
	}
	item.CollectionGeneration = 1
	item.StartedAt = nowTime.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	if _, err := validateConnectionAuditItem(item, 3); err == nil {
		t.Fatal("event outside collection window was accepted")
	}
	item.StartedAt = nowTime.Add(-time.Second).Format(time.RFC3339Nano)
	item.ConnectionCount, item.ClosedCount, item.ActivePeak, item.ActiveAtEnd = 1, 2, 1, 1
	if _, err := validateConnectionAuditItem(item, 3); err == nil {
		t.Fatal("inconsistent connection counters were accepted")
	}
}

func TestAgentConnectionReportsAcknowledgeStaleItemsWithoutBlockingValidReports(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{
		Name: "audit-node", AgentID: "audit-agent", AgentTokenHash: security.HashSecret("audit-token"),
		ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	activeUser := &model.User{Username: "active-audit-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "active-password"}
	staleUser := &model.User{Username: "stale-audit-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "inactive", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "stale-password"}
	for _, user := range []*model.User{activeUser, staleUser} {
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateInboundUser(ctx, &model.InboundUser{InboundID: inbound.ID, UserID: activeUser.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	item := func(reportID string, userID int64) map[string]any {
		return map[string]any{
			"report_id": reportID, "user_id": userID, "inbound_id": inbound.ID,
			"source_ip": "198.51.100.7", "network": "tcp", "destination": "example.com", "destination_port": 443,
			"connection_count": 1, "active_peak": 1, "active_at_end": 0,
			"closed_count": 1, "duration_total_ms": 250, "duration_max_ms": 250, "duration_le_1s_count": 1, "presence_sequence": 1,
			"bucket_capacity": 4096, "collection_started_at": nowTime.Add(-time.Minute).Format(time.RFC3339Nano), "collection_ended_at": nowTime.Format(time.RFC3339Nano),
			"started_at": nowTime.Add(-time.Second).Format(time.RFC3339Nano), "ended_at": nowTime.Format(time.RFC3339Nano),
		}
	}
	body, err := json.Marshal(map[string]any{"items": []map[string]any{item("valid-report", activeUser.ID), item("stale-report", staleUser.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/connection-reports", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer audit-token")
	rr := httptest.NewRecorder()
	newTestServer(db, "test-secret", "").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("connection report status = %d: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Accepted []string `json:"accepted_report_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	accepted := map[string]bool{}
	for _, reportID := range response.Accepted {
		accepted[reportID] = true
	}
	if !accepted["valid-report"] || !accepted["stale-report"] {
		t.Fatalf("accepted report IDs = %#v", response.Accepted)
	}
	overview, err := db.ConnectionAuditOverview(ctx, 24, true, store.DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if overview.ReportingUserCount != 1 || len(overview.Users) != 1 || overview.Users[0].UserID != activeUser.ID {
		t.Fatalf("stale report was stored or valid report was lost: %#v", overview)
	}
}

func TestControllerConnectionPresenceIsIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "presence-node", AgentID: "presence-agent", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "presence-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "55555555-5555-4555-8555-555555555555", ProxyPassword: "presence-password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInboundUser(ctx, &model.InboundUser{InboundID: inbound.ID, UserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	event := model.ConnectionPresenceEvent{Sequence: 1, ServerID: server.ID, UserID: user.ID, InboundID: inbound.ID, SourceIP: "198.51.100.10", Network: "tcp", Event: "first_meaningful_payload", State: "active", ActiveConnections: 1, Meaningful: true, PayloadLastAt: nowTime, At: nowTime}
	sut := newTestServer(db, "test-secret", "")
	for attempt := 0; attempt < 2; attempt++ {
		accepted, err := sut.acceptConnectionPresenceDelta(ctx, server, connectionPresenceDelta{Events: []model.ConnectionPresenceEvent{event}})
		if err != nil || len(accepted) != 1 {
			t.Fatalf("presence attempt %d = %#v, %v", attempt, accepted, err)
		}
	}
	detail, err := db.ConnectionAuditUserDetail(ctx, user.ID, 24, store.DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Presence) != 1 || detail.Summary.OnlineDeviceLower != 1 || detail.Summary.ActiveConnectionCount != 1 {
		t.Fatalf("controller presence detail = %#v", detail)
	}
}

func TestConnectionAuditAutomaticActionTargetsOnlyBoundDevice(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	user := &model.User{Username: "action-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "66666666-6666-4666-8666-666666666666", ProxyPassword: "action-password", DeviceLimit: 2}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	first := model.UserDevice{ID: "device-a", DeviceIDHash: "device-hash-a", UserID: user.ID, Name: "A", TokenHash: "token-hash-a", TokenPrefix: "token-a", CredentialEpoch: 1}
	second := model.UserDevice{ID: "device-b", DeviceIDHash: "device-hash-b", UserID: user.ID, Name: "B", TokenHash: "token-hash-b", TokenPrefix: "token-b", CredentialEpoch: 1}
	for _, device := range []*model.UserDevice{&first, &second} {
		if err := db.CreateUserDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
	}
	summary := model.ConnectionAuditUserSummary{UserID: user.ID, RiskScore: 95, Confidence: 0.95, EvidenceCategories: []string{"device_clone", "historical_anomaly"}, IdentityMode: "device_bound", CoverageComplete: true, CloneConfidence: 0.8, RiskDeviceIDHash: first.DeviceIDHash}
	sut := newTestServer(db, "test-secret", "")
	sut.applyConnectionAuditDeviceAction(ctx, summary, model.SubscriptionAuditRisk{})
	storedFirst, err := db.GetUserDevice(ctx, user.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedSecond, err := db.GetUserDevice(ctx, user.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !storedFirst.SubscriptionSuspended || storedFirst.ProxyAccessState != "reject_new" {
		t.Fatalf("target device was not restricted: %#v", storedFirst)
	}
	if storedSecond.SubscriptionSuspended || storedSecond.ProxyAccessState != "active" {
		t.Fatalf("unrelated device was changed: %#v", storedSecond)
	}

	if err := db.SetSetting(ctx, settingAuditAction, string(model.AuditActionWarn)); err != nil {
		t.Fatal(err)
	}
	summary.RiskDeviceIDHash = second.DeviceIDHash
	sut.applyConnectionAuditDeviceAction(ctx, summary, model.SubscriptionAuditRisk{})
	storedSecond, err = db.GetUserDevice(ctx, user.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedSecond.SubscriptionSuspended || storedSecond.ProxyAccessState != "active" {
		t.Fatalf("warn mode changed device: %#v", storedSecond)
	}
}

func TestAgentConnectionReportsRejectCrossServerInbound(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	serverA := &model.Server{Name: "audit-a", AgentID: "audit-a", AgentTokenHash: security.HashSecret("token-a"), ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	serverB := &model.Server{Name: "audit-b", AgentID: "audit-b", AgentTokenHash: security.HashSecret("token-b"), ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	for _, server := range []*model.Server{serverA, serverB} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	inboundB := &model.Inbound{ServerID: serverB.ID, Name: "entry-b", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inboundB); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "cross-server-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "33333333-3333-4333-8333-333333333333", ProxyPassword: "cross-password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInboundUser(ctx, &model.InboundUser{InboundID: inboundB.ID, UserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	body, err := json.Marshal(map[string]any{"items": []map[string]any{{
		"report_id": "cross-server-report", "user_id": user.ID, "inbound_id": inboundB.ID,
		"source_ip": "198.51.100.31", "network": "tcp", "destination": "example.com", "destination_port": 443,
		"connection_count": 1, "active_peak": 1, "active_at_end": 0,
		"closed_count": 1, "duration_total_ms": 250, "duration_max_ms": 250, "duration_le_1s_count": 1, "presence_sequence": 1,
		"bucket_capacity": 4096, "collection_started_at": nowTime.Add(-time.Minute).Format(time.RFC3339Nano), "collection_ended_at": nowTime.Format(time.RFC3339Nano),
		"started_at": nowTime.Add(-time.Second).Format(time.RFC3339Nano), "ended_at": nowTime.Format(time.RFC3339Nano),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/connection-reports", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", serverA.AgentID)
	req.Header.Set("Authorization", "Bearer token-a")
	rr := httptest.NewRecorder()
	newTestServer(db, "test-secret", "").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-server report status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

func TestAgentConnectionReportsRejectedWhenGlobalAuditDisabled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{
		Name: "audit-node", AgentID: "audit-agent", AgentTokenHash: security.HashSecret("audit-token"),
		ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingConnectionAuditEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "audit-off-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "44444444-4444-4444-8444-444444444444", ProxyPassword: "audit-off-password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInboundUser(ctx, &model.InboundUser{InboundID: inbound.ID, UserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	body, err := json.Marshal(map[string]any{"items": []map[string]any{{
		"report_id": "gated-report", "user_id": user.ID, "inbound_id": inbound.ID,
		"source_ip": "198.51.100.9", "network": "tcp", "destination": "example.com", "destination_port": 443,
		"connection_count": 1, "active_peak": 1, "active_at_end": 0,
		"closed_count": 1, "duration_total_ms": 250, "duration_max_ms": 250, "duration_le_1s_count": 1, "presence_sequence": 1,
		"bucket_capacity": 4096, "collection_started_at": nowTime.Add(-time.Minute).Format(time.RFC3339Nano), "collection_ended_at": nowTime.Format(time.RFC3339Nano),
		"started_at": nowTime.Add(-time.Second).Format(time.RFC3339Nano), "ended_at": nowTime.Format(time.RFC3339Nano),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/connection-reports", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer audit-token")
	rr := httptest.NewRecorder()
	newTestServer(db, "test-secret", "").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("connection report status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	overview, err := db.ConnectionAuditOverview(ctx, 24, false, store.DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if overview.EnabledServerCount != 0 || overview.ReportingUserCount != 0 {
		t.Fatalf("gated reports leaked into the overview: %#v", overview)
	}
}

func TestViewerCannotReadConnectionAudit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	password, err := security.HashPassword("long-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	viewer := &model.User{Username: "audit-viewer", PasswordHash: password, Role: model.RoleViewer, Status: "active", ProxyUUID: "44444444-4444-4444-8444-444444444444", ProxyPassword: "viewer-password"}
	if err := db.CreateUser(context.Background(), viewer); err != nil {
		t.Fatal(err)
	}
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: viewer.ID, Role: string(model.RoleViewer), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	request(t, newTestServer(db, "test-secret", "").Handler(), http.MethodGet, "/api/v2/ui/audit/overview", token, nil, http.StatusForbidden)
}
