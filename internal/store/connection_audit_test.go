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
	overview, err := s.ConnectionAuditOverview(ctx, 24)
	if err != nil {
		t.Fatal(err)
	}
	if overview.EnabledServerCount != 1 || overview.ReportingUserCount != 1 || overview.TotalConnections != 1500 || overview.UniqueSourceIPs != 15 {
		t.Fatalf("unexpected overview: %#v", overview)
	}
	got := overview.Users[0]
	if got.RiskLevel != "critical" || got.RiskScore < 75 || got.SourceSubnetCount != 15 {
		t.Fatalf("unexpected risk summary: %#v", got)
	}
	if len(got.RiskSignals) == 0 || got.RiskSignals[0] != "当前窗口内来源 IP 达 15 个" {
		t.Fatalf("unexpected risk signals: %#v", got.RiskSignals)
	}
	detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Sources) != 15 || len(detail.Recent) != 15 || len(detail.Destinations) != 1 || len(detail.Outbounds) != 1 {
		t.Fatalf("unexpected detail dimensions: sources=%d recent=%d destinations=%d outbounds=%d", len(detail.Sources), len(detail.Recent), len(detail.Destinations), len(detail.Outbounds))
	}
	if detail.Outbounds[0].Label != "direct" || detail.Outbounds[0].ConnectionCount != 1500 {
		t.Fatalf("unexpected outbound aggregate: %#v", detail.Outbounds[0])
	}
}

func TestConnectionAuditRiskAdaptsToWindow(t *testing.T) {
	item := model.ConnectionAuditUserSummary{
		SourceIPCount:     15,
		SourceSubnetCount: 15,
		ServerCount:       3,
		ConnectionCount:   1500,
		ActivePeak:        20,
	}
	shortScore, shortLevel, shortSignals := connectionAuditRisk(item, 24)
	longScore, longLevel, longSignals := connectionAuditRisk(item, 30*24)
	if shortScore <= longScore || shortLevel != "critical" || longLevel != "high" {
		t.Fatalf("window-adjusted risk = short %d/%s, long %d/%s", shortScore, shortLevel, longScore, longLevel)
	}
	if len(shortSignals) <= len(longSignals) {
		t.Fatalf("long window did not reduce transient signals: short=%#v long=%#v", shortSignals, longSignals)
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
	overview, err := s.ConnectionAuditOverview(ctx, 24)
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
		if len(item.RiskSignals) == 0 || item.RiskSignals[0] != "有 2 个来源 IP 同时被多个用户使用" {
			t.Fatalf("shared source signal for user %d = %#v", item.UserID, item.RiskSignals)
		}
	}
}

func TestConnectionAuditPrunesReportsOutsideRetention(t *testing.T) {
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
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from connection_audit_reports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retained audit rows = %d, want 1", count)
	}
}
