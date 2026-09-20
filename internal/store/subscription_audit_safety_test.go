package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestSubscriptionAuditDisabledStillLimitsRawRequests(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "audit-off-limited", "audit-off-limited-token", model.RoleViewer)
	policy := DefaultSubscriptionAuditPolicy()
	policy.RawRequestsPer60Seconds = model.AuditThreshold{Soft: 1, Hard: 2}
	at := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	options := SubscriptionAuditOptions{AuditEnabled: false, Action: model.AuditActionRestrict}
	s.AllowSubscriptionIngress("1.1.1.1", at.Add(-time.Minute))
	event := subscriptionAuditEvent(user.ID, "1.1.1.1", "", at)
	for i := 0; i < 2; i++ {
		decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, event, policy, options)
		if err != nil || !decision.Allowed {
			t.Fatalf("request %d: %+v %v", i, decision, err)
		}
	}
	const token = "limited-one-time"
	if err := s.CreateOneTimeSubscriptionToken(ctx, user.ID, token); err != nil {
		t.Fatal(err)
	}
	decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, token, event, policy, options)
	if err != nil || decision.Allowed || !decision.RateLimited || decision.RetryAfter <= 0 || decision.Burned || decision.AuditID != 0 || decision.Risk.Score != 0 {
		t.Fatalf("disabled audit bypassed resource protection: %+v %v", decision, err)
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, token); err != nil {
		t.Fatalf("rate limiting consumed token: %v", err)
	}
	event.RequestedAt = at.Add(time.Minute)
	decision, err = s.AuthorizeSubscriptionPull(ctx, user.ID, token, event, policy, options)
	if err != nil || !decision.Allowed || !decision.Burned {
		t.Fatalf("refilled request: %+v %v", decision, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from subscription_pull_audits where user_id=?`, user.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("behavior records while disabled: %d %v", count, err)
	}
}
