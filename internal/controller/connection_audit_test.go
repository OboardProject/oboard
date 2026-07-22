package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestValidateConnectionAuditItem(t *testing.T) {
	nowTime := time.Now().UTC()
	item := connectionAuditReportItem{
		ReportID: "report-1", UserID: 7, SourceIP: "::ffff:198.51.100.7", SourceGeoCode: "us", Network: "TCP",
		Destination: "example.com", DestinationPort: 443, OutboundTag: "direct", OutboundType: "direct",
		ConnectionCount: 2, ActivePeak: 1, StartedAt: nowTime.Add(-time.Second).Format(time.RFC3339Nano), EndedAt: nowTime.Format(time.RFC3339Nano),
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
	overview, err := db.ConnectionAuditOverview(ctx, 24)
	if err != nil {
		t.Fatal(err)
	}
	if overview.ReportingUserCount != 1 || len(overview.Users) != 1 || overview.Users[0].UserID != activeUser.ID {
		t.Fatalf("stale report was stored or valid report was lost: %#v", overview)
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
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: viewer.ID, Role: string(model.RoleViewer), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	request(t, newTestServer(db, "test-secret", "").Handler(), http.MethodGet, "/api/v1/audit/overview", token, nil, http.StatusForbidden)
}
