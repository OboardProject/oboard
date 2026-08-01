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

func TestSubscriptionAuditShortRegionThresholdSuspendsAndResumeResetsWindow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "audit-user", "persistent-token", model.RoleViewer)
	admin := createSubscriptionAuditUser(t, s, "audit-admin", "admin-token", model.RoleAdmin)
	policy := DefaultSubscriptionAuditPolicy()
	base := time.Now().UTC().Add(-time.Minute)
	for index, item := range []struct{ ip, province string }{{"1.1.1.1", "广东"}, {"8.8.8.8", "北京"}, {"9.9.9.9", "上海"}} {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, item.ip, item.province, base.Add(time.Duration(index)*time.Second)), policy)
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 && !decision.Allowed {
			t.Fatalf("request %d was unexpectedly denied: %#v", index, decision)
		}
		if index == 2 && (decision.Allowed || !decision.JustSuspended || decision.Risk.Short.RegionCount != 3) {
			t.Fatalf("third region did not suspend: %#v", decision)
		}
	}
	stored, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SubscriptionSuspended || stored.SubscriptionToken != user.SubscriptionToken {
		t.Fatalf("unexpected suspended user state: %#v", stored)
	}
	denied, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, "4.4.4.4", "浙江", time.Now().UTC()), policy)
	if err != nil || denied.Allowed || denied.JustSuspended {
		t.Fatalf("suspended replay = %#v, err=%v", denied, err)
	}
	state, err := s.ResumeSubscriptionAccess(ctx, user.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Suspended || state.ResumedBy == nil || *state.ResumedBy != admin.ID {
		t.Fatalf("unexpected resumed state: %#v", state)
	}
	afterResume := time.Now().UTC().Add(time.Second)
	allowed, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, "208.67.222.222", "江苏", afterResume), policy)
	if err != nil || !allowed.Allowed || allowed.Risk.Short.RegionCount != 1 {
		t.Fatalf("old window was not reset: %#v, err=%v", allowed, err)
	}
	detail, err := s.SubscriptionAuditUserDetail(ctx, user.ID, 24, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Recent) != 5 {
		t.Fatalf("history was not retained after resume: %d", len(detail.Recent))
	}
}

func TestSubscriptionAuditLongRegionThresholdAndNonRegionSignals(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	policy := DefaultSubscriptionAuditPolicy()
	user := createSubscriptionAuditUser(t, s, "long-window", "long-token", model.RoleViewer)
	base := time.Now().UTC().Add(-4 * time.Hour)
	for index, item := range []struct{ ip, province string }{{"1.1.1.1", "广东"}, {"8.8.8.8", "北京"}, {"9.9.9.9", "上海"}, {"208.67.222.222", "江苏"}} {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, item.ip, item.province, base.Add(time.Duration(index)*time.Hour)), policy)
		if err != nil {
			t.Fatal(err)
		}
		if index < 3 && !decision.Allowed {
			t.Fatalf("long-window request %d was denied", index)
		}
		if index == 3 && (decision.Allowed || !decision.Risk.HardBlock || decision.Risk.Long.RegionCount != 4 || decision.Risk.Short.RegionCount != 1) {
			t.Fatalf("long threshold did not suspend: %#v", decision)
		}
	}

	nonRegion := createSubscriptionAuditUser(t, s, "ip-risk", "ip-token", model.RoleViewer)
	policy.Short.SourceIPLimit = 2
	policy.Long.SourceIPLimit = 2
	first, err := s.AuthorizeSubscriptionPull(ctx, nonRegion.ID, nonRegion.SubscriptionToken, subscriptionAuditEvent(nonRegion.ID, "4.2.2.1", "广东", time.Now().UTC()), policy)
	if err != nil || !first.Allowed {
		t.Fatal(err)
	}
	second, err := s.AuthorizeSubscriptionPull(ctx, nonRegion.ID, nonRegion.SubscriptionToken, subscriptionAuditEvent(nonRegion.ID, "4.2.2.2", "广东", time.Now().UTC().Add(time.Second)), policy)
	if err != nil || !second.Allowed || second.JustSuspended || second.Risk.Score < 25 || second.Risk.HardBlock {
		t.Fatalf("IP-only risk should alert without suspending: %#v, err=%v", second, err)
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
	base := time.Now().UTC().Add(-time.Minute)
	for index, item := range []struct{ ip, province string }{{"1.0.0.1", "广东"}, {"8.8.4.4", "北京"}} {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, item.ip, item.province, base.Add(time.Duration(index)*time.Second)), policy)
		if err != nil || !decision.Allowed {
			t.Fatalf("seed request failed: %#v %v", decision, err)
		}
	}
	const oneTimeToken = "risk-one-time-token"
	if err := s.CreateOneTimeSubscriptionToken(ctx, user.ID, oneTimeToken); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.AuthorizeSubscriptionPull(ctx, user.ID, oneTimeToken, subscriptionAuditEvent(user.ID, "9.9.9.10", "上海", base.Add(2*time.Second)), policy)
	if err != nil || blocked.Allowed || blocked.Burned {
		t.Fatalf("risk request consumed token: %#v, err=%v", blocked, err)
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, oneTimeToken); err != nil {
		t.Fatalf("blocked one-time token is unavailable: %v", err)
	}
	if _, err := s.ResumeSubscriptionAccess(ctx, user.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.AuthorizeSubscriptionPull(ctx, user.ID, oneTimeToken, subscriptionAuditEvent(user.ID, "9.9.9.10", "上海", time.Now().UTC().Add(time.Second)), policy)
	if err != nil || !allowed.Allowed || !allowed.Burned {
		t.Fatalf("resumed one-time token was not consumed: %#v, err=%v", allowed, err)
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
	policy.Long.RegionLimit = policy.Short.RegionLimit - 1
	if err := ValidateSubscriptionAuditPolicy(policy); err == nil {
		t.Fatal("decreasing long threshold was accepted")
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
