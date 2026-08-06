package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func createSubscriptionAuditUser(t *testing.T, s *Store, username, token string, role model.Role) *model.User {
	t.Helper()
	user := &model.User{Username: username, PasswordHash: "hash", Role: role, Status: "active", ProxyUUID: username + "-uuid", ProxyPassword: username + "-password", SubscriptionToken: token}
	if err := s.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return user
}

func subscriptionAuditEvent(userID int64, ip, province string, at time.Time) model.SubscriptionPullAudit {
	return model.SubscriptionPullAudit{UserID: userID, SourceIP: ip, SourceCountryCode: "CN", SourceCountry: "中国", SourceProvince: province, ClientName: "Mihomo", Format: "mihomo", RequestedAt: at}
}

func TestSubscriptionAuditGeographyNeverSuspendsLegacyToken(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "audit-user", "persistent-token", model.RoleViewer)
	policy := DefaultSubscriptionAuditPolicy()
	base := time.Now().UTC().Add(-time.Minute)
	for index, item := range []struct{ ip, province string }{{"1.1.1.1", "广东"}, {"8.8.8.8", "北京"}, {"9.9.9.9", "上海"}} {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, item.ip, item.province, base.Add(time.Duration(index)*time.Second)), policy, SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionRestrict})
		if err != nil {
			t.Fatal(err)
		}
		if !decision.Allowed || decision.JustSuspended || decision.Risk.HardBlock {
			t.Fatalf("request %d was incorrectly restricted: %#v", index, decision)
		}
	}
	stored, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionSuspended || stored.SubscriptionToken != user.SubscriptionToken {
		t.Fatalf("geography changed subscription state: %#v", stored)
	}
	detail, err := s.SubscriptionAuditUserDetail(ctx, user.ID, 24, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Recent) != 3 || detail.Summary.RegionCount != 3 || detail.Summary.IdentityMode != "legacy_unbound" {
		t.Fatalf("unexpected geographic evidence: %#v", detail)
	}
}

func TestSubscriptionAuditLogicalPullDedupeAndRouteNovelty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	policy := DefaultSubscriptionAuditPolicy()
	user := createSubscriptionAuditUser(t, s, "long-window", "long-token", model.RoleViewer)
	base := time.Now().UTC().Add(-time.Minute)
	for index, item := range []struct{ ip, route string }{{"1.1.1.1", "AS1-CN-1.1.1.0/24"}, {"8.8.8.8", "AS2-CN-8.8.8.0/24"}} {
		event := subscriptionAuditEvent(user.ID, item.ip, "广东", base.Add(time.Duration(index)*time.Second))
		event.RouteID = item.route
		event.RepresentationID = "same-representation"
		event.SubscriptionRevision = "revision-1"
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, event, policy, SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionRestrict})
		if err != nil {
			t.Fatal(err)
		}
		if !decision.Allowed || decision.Risk.HardBlock {
			t.Fatalf("route change was restricted: %#v", decision)
		}
	}
	detail, err := s.SubscriptionAuditUserDetail(ctx, user.ID, 24, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Recent) != 2 || detail.Recent[0].LogicalPullWeight != 0.25 || detail.Summary.RawRequestCount != 2 || detail.Summary.LogicalPullWeight != 1.25 || detail.Summary.RouteCount != 2 {
		t.Fatalf("logical pull accounting mismatch: %#v", detail)
	}
}

func TestSubscriptionAuditRiskBlockDoesNotConsumeOneTimeToken(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "one-time-risk", "persistent-risk-token", model.RoleViewer)
	admin := createSubscriptionAuditUser(t, s, "one-time-admin", "one-time-admin-token", model.RoleAdmin)
	policy := DefaultSubscriptionAuditPolicy()
	policy.Mode = "custom"
	policy.RawRequestsPer60Seconds = model.AuditThreshold{Soft: 1, Hard: 2}
	base := time.Now().UTC().Add(-time.Minute)
	for index, item := range []struct{ ip, province string }{{"1.0.0.1", "广东"}, {"8.8.4.4", "北京"}} {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, item.ip, item.province, base.Add(time.Duration(index)*time.Second)), policy, SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionRestrict})
		if err != nil || !decision.Allowed {
			t.Fatalf("seed request failed: %#v %v", decision, err)
		}
	}
	const oneTimeToken = "risk-one-time-token"
	if err := s.CreateOneTimeSubscriptionToken(ctx, user.ID, oneTimeToken); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.AuthorizeSubscriptionPull(ctx, user.ID, oneTimeToken, subscriptionAuditEvent(user.ID, "9.9.9.10", "上海", base.Add(2*time.Second)), policy, SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionRestrict})
	if err != nil || blocked.Allowed || blocked.Burned || !blocked.RateLimited {
		t.Fatalf("rate-limited request consumed token: %#v, err=%v", blocked, err)
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, oneTimeToken); err != nil {
		t.Fatalf("blocked one-time token is unavailable: %v", err)
	}
	_ = admin
	allowed, err := s.AuthorizeSubscriptionPull(ctx, user.ID, oneTimeToken, subscriptionAuditEvent(user.ID, "9.9.9.10", "上海", base.Add(62*time.Second)), policy, SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionRestrict})
	if err != nil || !allowed.Allowed || !allowed.Burned {
		t.Fatalf("resumed one-time token was not consumed: %#v, err=%v", allowed, err)
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, oneTimeToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("consumed one-time token still resolves: %v", err)
	}
}

func TestSubscriptionAuditLegacyModeNeverAutoSuspends(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "warn-mode", "warn-token", model.RoleViewer)
	policy := DefaultSubscriptionAuditPolicy()
	options := SubscriptionAuditOptions{AuditEnabled: true, Action: model.AuditActionWarn}
	base := time.Now().UTC().Add(-time.Minute)
	for index, item := range []struct{ ip, province string }{{"1.1.1.1", "广东"}, {"8.8.8.8", "北京"}, {"9.9.9.9", "上海"}} {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, item.ip, item.province, base.Add(time.Duration(index)*time.Second)), policy, options)
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 && !decision.Allowed {
			t.Fatalf("request %d was unexpectedly denied: %#v", index, decision)
		}
		if index == 2 {
			if !decision.Allowed || decision.Risk.HardBlock || decision.JustSuspended {
				t.Fatalf("legacy mode was not kept advisory: %#v", decision)
			}
			if decision.Access.Suspended {
				t.Fatalf("warn mode suspended the user: %#v", decision)
			}
		}
	}
	stored, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionSuspended {
		t.Fatalf("warn mode suspended the stored user: %#v", stored)
	}
	detail, err := s.SubscriptionAuditUserDetail(ctx, user.ID, 24, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Recent) != 3 || detail.Recent[0].Outcome != "served" {
		t.Fatalf("legacy outcomes were not recorded: %#v", detail.Recent)
	}
}

func TestSubscriptionAuditDisabledSkipsRecordingAndEvaluation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "audit-off", "audit-off-token", model.RoleViewer)
	policy := DefaultSubscriptionAuditPolicy()
	options := SubscriptionAuditOptions{AuditEnabled: false}
	base := time.Now().UTC().Add(-time.Minute)
	for index, item := range []struct{ ip, province string }{{"1.1.1.1", "广东"}, {"8.8.8.8", "北京"}, {"9.9.9.9", "上海"}} {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, item.ip, item.province, base.Add(time.Duration(index)*time.Second)), policy, options)
		if err != nil {
			t.Fatal(err)
		}
		if !decision.Allowed || decision.JustSuspended || decision.Risk.Score != 0 {
			t.Fatalf("disabled audit request %d = %#v", index, decision)
		}
	}
	stored, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionSuspended {
		t.Fatalf("disabled audit suspended the user: %#v", stored)
	}
	detail, err := s.SubscriptionAuditUserDetail(ctx, user.ID, 24, policy)
	if !errors.Is(err, sql.ErrNoRows) {
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Recent) != 0 {
			t.Fatalf("disabled audit recorded %d pulls", len(detail.Recent))
		}
	}
	const oneTimeToken = "audit-off-one-time"
	if err := s.CreateOneTimeSubscriptionToken(ctx, user.ID, oneTimeToken); err != nil {
		t.Fatal(err)
	}
	decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, oneTimeToken, subscriptionAuditEvent(user.ID, "208.67.222.222", "江苏", time.Now().UTC()), policy, options)
	if err != nil || !decision.Allowed || !decision.Burned {
		t.Fatalf("disabled audit did not consume one-time token: %#v, err=%v", decision, err)
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, oneTimeToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("consumed one-time token still resolves: %v", err)
	}
}

func TestValidateSubscriptionAuditPolicy(t *testing.T) {
	policy := DefaultSubscriptionAuditPolicy()
	if err := ValidateSubscriptionAuditPolicy(policy); err != nil {
		t.Fatal(err)
	}
	policy.Mode = "custom"
	policy.LogicalPullsPer24Hours = model.AuditThreshold{Soft: 10, Hard: 10}
	if err := ValidateSubscriptionAuditPolicy(policy); err == nil {
		t.Fatal("invalid threshold was accepted")
	}
}

func TestRejectedSubscriptionPullAuditPreservesUnknownProfileID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "invalid-profile", "invalid-profile-token", model.RoleViewer)
	profileID := int64(9999)
	event := subscriptionAuditEvent(user.ID, "1.1.1.1", "广东", time.Now().UTC())
	event.ProfileID = &profileID
	event.Outcome = "rejected_invalid_request"
	event.Reason = "subscription profile not found"
	if err := s.AddRejectedSubscriptionPullAudit(ctx, user.SubscriptionToken, event); err != nil {
		t.Fatal(err)
	}
	detail, err := s.SubscriptionAuditUserDetail(ctx, user.ID, 24, DefaultSubscriptionAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Recent) != 1 || detail.Recent[0].ProfileID == nil || *detail.Recent[0].ProfileID != profileID {
		t.Fatalf("requested profile ID was not retained: %#v", detail.Recent)
	}
}
