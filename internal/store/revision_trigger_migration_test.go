package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestLegacyUnconditionalRevisionTriggersAreReplacedOnMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, name := range []string{"config_rev_users_update", "routing_rev_users_update"} {
		if _, err := s.db.ExecContext(ctx, `drop trigger if exists `+name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `create trigger config_rev_users_update after update on users begin update configuration_revision set revision=revision+1 where id=1; end`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `create trigger routing_rev_users_update after update on users begin update routing_cache_revision set revision=revision+1 where id=1; end`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	configSQL, err := reopened.revisionTriggerSQL(ctx, "config_rev_users_update")
	if err == nil && strings.TrimSpace(configSQL) != "" {
		t.Fatalf("config_rev_users_update should be removed so user identity uses apply_core_config, got %s", configSQL)
	}
	routingSQL, err := reopened.revisionTriggerSQL(ctx, "routing_rev_users_update")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(routingSQL)
	if !strings.Contains(lower, "when") {
		t.Fatalf("routing trigger was not replaced with a WHEN clause: %s", routingSQL)
	}
	if strings.Contains(lower, "traffic_used_bytes") {
		t.Fatalf("routing trigger still watches traffic_used_bytes: %s", routingSQL)
	}
}

func TestCommitTrafficLedgerDoesNotAdvanceConfigurationOrRoutingRevision(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	beforeConfig, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeRouting, err := s.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforePolicy, err := s.TrafficPolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{v2Report("tr-storm", server.ID, user.ID, 0, 100, 0, 200)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AcceptedReports) != 1 || result.AcceptedReports[0].Status != trafficAcceptAccepted {
		t.Fatalf("accepted = %#v", result.AcceptedReports)
	}
	afterConfig, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterRouting, err := s.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterPolicy, err := s.TrafficPolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterConfig != beforeConfig {
		t.Fatalf("configuration_revision %d -> %d", beforeConfig, afterConfig)
	}
	if afterRouting != beforeRouting {
		t.Fatalf("routing_cache_revision %d -> %d", beforeRouting, afterRouting)
	}
	if afterPolicy <= beforePolicy {
		t.Fatalf("traffic_policy_revision did not advance (%d -> %d)", beforePolicy, afterPolicy)
	}
	stored, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 300 {
		t.Fatalf("traffic_used_bytes = %d, want 300", stored.TrafficUsedBytes)
	}
}

func TestUserQuotaFieldsDoNotBumpConfigurationRevision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "quota-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111188", ProxyPassword: "pass"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	before, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeRouting, err := s.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	user.TrafficLimitBytes = 1 << 30
	user.SpeedLimitMbps = 50
	user.TrafficResetDay = 15
	if err := s.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	after, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterRouting, err := s.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("quota fields bumped configuration_revision %d -> %d", before, after)
	}
	if afterRouting != beforeRouting {
		t.Fatalf("quota fields bumped routing_cache_revision %d -> %d", beforeRouting, afterRouting)
	}
	user.ProxyPassword = "rotated-password"
	if err := s.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	afterIdentity, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterIdentity != after {
		t.Fatalf("proxy password rotation bumped configuration_revision %d -> %d", after, afterIdentity)
	}
	afterRoutingIdentity, err := s.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRoutingIdentity <= afterRouting {
		t.Fatal("proxy password rotation did not invalidate routing cache")
	}
}
