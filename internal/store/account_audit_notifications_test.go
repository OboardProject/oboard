package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestAccountAuditNotificationAtomicFailure(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 1, 80), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER audit_notify_fail BEFORE INSERT ON account_audit_notifications BEGIN SELECT RAISE(ABORT,'simulated notification disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 2, 80), 2); err == nil {
		t.Fatal("failure not propagated")
	}
	var rev, events int
	if err := s.db.QueryRow(`SELECT revision FROM account_audit_snapshots WHERE user_id=?`, id).Scan(&rev); err != nil || rev != 1 {
		t.Fatal("partial snapshot", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM account_audit_events`).Scan(&events); err != nil || events != 0 {
		t.Fatal("partial event", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER audit_notify_fail`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 2, 80), 2); err != nil {
		t.Fatal(err)
	}
}

func TestAccountAuditNotificationSnapshotLeaseAndReview(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	for m := 1; m <= 2; m++ {
		if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, m, 80), int64(m)); err != nil {
			t.Fatal(err)
		}
	}
	now := accountAuditSnapshot(id, 2, 80).AsOf
	claims, err := s.ClaimAccountAuditNotifications(ctx, now, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %+v %v", claims, err)
	}
	n := claims[0]
	page, err := s.ListAccountAuditEvents(ctx, AccountAuditQuery{EventID: n.EventID})
	if err != nil {
		t.Fatal(err)
	}
	e := page.Items.([]AccountAuditEvent)[0]
	accounts, err := s.ListAccountAuditSnapshots(ctx, AccountAuditQuery{UserID: id})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(n.Snapshot, e.Snapshot) || !bytes.Equal(n.Snapshot, accounts.Items.([]AccountAuditRow)[0].Snapshot) {
		t.Fatal("snapshot disagreement")
	}
	if next, err := s.ClaimAccountAuditNotifications(ctx, now, 10); err != nil || len(next) != 0 {
		t.Fatal("live lease redelivered", err)
	}
	later := now.Add(3 * time.Minute)
	claims, err = s.ClaimAccountAuditNotifications(ctx, later, 10)
	if err != nil || len(claims) != 1 {
		t.Fatal("crash recovery", err)
	}
	if err := s.CompleteAccountAuditNotification(ctx, n.ID, n.LeaseToken, later, true); err == nil {
		t.Fatal("stale worker accepted")
	}
	if err := s.SetAccountAuditEventStatus(ctx, e.ID, e.Revision, "false_positive", "admin:1", "verified shared egress", later.Add(time.Hour), later); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAccountAuditNotification(ctx, n.ID, claims[0].LeaseToken, later, true); err == nil {
		t.Fatal("cancelled delivery resurrected")
	}
	if err := s.SetAccountAuditEventStatus(ctx, e.ID, e.Revision, "pending", "admin:1", "stale", time.Time{}, later); err == nil {
		t.Fatal("stale review accepted")
	}
	actions, err := s.ListAccountAuditActions(ctx, AccountAuditQuery{EventID: e.ID, Limit: 1})
	if err != nil || len(actions.Items.([]AccountAuditAction)) != 1 {
		t.Fatal("missing action", err)
	}
	blocked, err := s.ListAccountAuditActions(ctx, AccountAuditQuery{AllowedUserIDs: []int64{}})
	if err != nil || len(blocked.Items.([]AccountAuditAction)) != 0 {
		t.Fatal("scope leak", err)
	}
}

func TestAccountAuditNotificationSeverityAndMuteExpiry(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	for m := 1; m <= 3; m++ {
		if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, m, 80), int64(m)); err != nil {
			t.Fatal(err)
		}
	}
	now := accountAuditSnapshot(id, 3, 80).AsOf
	messages, err := s.ListAccountAuditNotifications(ctx, now, 100)
	if err != nil || len(messages) != 1 {
		t.Fatal("duplicate notification", err)
	}
	if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 4, 95), 4); err != nil {
		t.Fatal(err)
	}
	messages, err = s.ListAccountAuditNotifications(ctx, now.Add(time.Minute), 100)
	if err != nil || len(messages) != 2 {
		t.Fatal("severity escalation", err)
	}
	p, _ := s.ListAccountAuditEvents(ctx, AccountAuditQuery{})
	e := p.Items.([]AccountAuditEvent)[0]
	if err := s.SetAccountAuditEventStatus(ctx, e.ID, e.Revision, "observing", "admin:1", "check tomorrow", now.Add(3*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	for m := 5; m <= 6; m++ {
		if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, m, 95), int64(m)); err != nil {
			t.Fatal(err)
		}
	}
	messages, err = s.ClaimAccountAuditNotifications(ctx, now.Add(3*time.Minute), 100)
	if err != nil || len(messages) != 1 {
		t.Fatal("mute expiry not notified", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.CompleteAccountAuditNotification(ctx, messages[0].ID, messages[0].LeaseToken, now.Add(3*time.Minute), true); err != nil {
			t.Fatal("completion replay", err)
		}
	}
}
