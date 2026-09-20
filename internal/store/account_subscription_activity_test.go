package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditactivity"
	"github.com/OboardProject/oboard/internal/model"
)

func BenchmarkAccountSubscriptionMemoryLimit(b *testing.B) {
	var limiter subscriptionMemoryLimiter
	at := time.Unix(1800000000, 0).UTC()
	limiter.consume("account:1", 60, at.Add(-time.Minute))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		limiter.consume("account:1", 60, at)
	}
}

func TestAccountSubscriptionMemoryLimitsRestartAndBounds(t *testing.T) {
	at := time.Unix(1800000000, 0).UTC()
	var l subscriptionMemoryLimiter
	if hit, _ := l.consume("account:1", 60, at); !hit {
		t.Fatal("cold restart grants burst")
	}
	if hit, _ := l.consume("account:1", 60, at.Add(time.Second)); hit {
		t.Fatal("credit did not refill")
	}
	if hit, _ := l.consume("account:1", 60, at); !hit {
		t.Fatal("backward time minted credit")
	}
	for i := 0; i < subscriptionLimitShards*subscriptionLimitEntriesPerShard*2; i++ {
		l.consume(time.Unix(int64(i), 0).String(), 60, at.Add(time.Minute))
	}
	n := 0
	for i := range l.shards {
		n += len(l.shards[i].buckets)
	}
	if n > subscriptionLimitShards*subscriptionLimitEntriesPerShard {
		t.Fatal(n)
	}
	var restarted subscriptionMemoryLimiter
	if hit, _ := restarted.consume("account:1", 60, at.Add(time.Hour)); !hit {
		t.Fatal("restart reset allowance")
	}
}
func TestAccountSubscriptionDurableSuccessDedupeCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "activity", "activity-token", model.RoleViewer)
	at := time.Unix(1800000000, 0).UTC()
	if err = s.SetAccountSubscriptionCoverage(ctx, true, at); err != nil {
		t.Fatal(err)
	}
	// Fixed uninterrupted observation history, independent of connection data.
	if _, err = s.db.ExecContext(ctx, `UPDATE account_subscription_coverage SET started_at=?`, at.Add(-8*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	observation := auditactivity.SubscriptionObservation{At: at, Source: "source", Representation: "mihomo", ConfigurationRevision: "etag", TokenVersion: "token-rev", SourceVersion: "source-v1", Success: true, IdentityTrusted: true, SourceUsable: true}
	for _, delta := range []time.Duration{0, 30 * time.Second, 5 * time.Minute} {
		observation.At = at.Add(delta)
		if delta > time.Minute {
			if _, err = s.db.ExecContext(ctx, `UPDATE account_subscription_coverage SET last_at=?`, observation.At.Unix()); err != nil {
				t.Fatal(err)
			}
		}
		if err = s.RecordAccountSubscriptionActivity(ctx, user.ID, observation); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LoadAccountSubscriptionFeatures(ctx, user.ID, observation.At)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.RawRequests != 3 || got.State.LogicalUpdates != 2 || got.CandidateLower != 1 || got.CandidateUpper != 1 || !got.HistoryComplete {
		t.Fatalf("features: %+v", got)
	}
	observation.Success = false
	if err = s.RecordAccountSubscriptionActivity(ctx, user.ID, observation); err == nil {
		t.Fatal("failed request treated as successful")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.LoadAccountSubscriptionFeatures(ctx, user.ID, observation.At)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State.LogicalUpdates != 2 {
		t.Fatal("lost committed state")
	}
	if err = s.SetAccountSubscriptionCoverage(ctx, true, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadAccountSubscriptionFeatures(ctx, user.ID, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.HistoryComplete || got.CandidateUpper != auditactivity.MaxSources {
		t.Fatalf("gap hidden: %+v", got)
	}
	observation.Success = true
	observation.Source = "after-gap"
	observation.At = at.Add(time.Hour)
	if err = s.RecordAccountSubscriptionActivity(ctx, user.ID, observation); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadAccountSubscriptionFeatures(ctx, user.ID, observation.At)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Seen["after-gap"].NoveltyAtFirstSeen != auditactivity.NoveltyUnknown {
		t.Fatal("coverage reset fabricated novelty")
	}
	observation.SourceVersion = "rotated-epoch"
	observation.At = observation.At.Add(time.Second)
	if err = s.RecordAccountSubscriptionActivity(ctx, user.ID, observation); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadAccountSubscriptionFeatures(ctx, user.ID, observation.At)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Seen["after-gap"].NoveltyAtFirstSeen != auditactivity.NoveltyUnknown {
		t.Fatal("epoch change fabricated novelty")
	}
}
func TestAccountSubscriptionAuditDisabledStillLimits(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "audit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := createSubscriptionAuditUser(t, s, "limited", "limited-token", model.RoleViewer)
	at := time.Unix(1800000000, 0).UTC()
	policy := DefaultSubscriptionAuditPolicy()
	s.AllowSubscriptionIngress("1.1.1.1", at.Add(-time.Minute))
	limited := false
	for i := 0; i <= policy.RawRequestsPer60Seconds.Hard; i++ {
		d, err := s.AuthorizeSubscriptionPull(ctx, user.ID, user.SubscriptionToken, subscriptionAuditEvent(user.ID, "1.1.1.1", "", at), policy, SubscriptionAuditOptions{})
		if err != nil {
			t.Fatal(err)
		}
		limited = limited || d.RateLimited
		if d.Risk.Level != "pending" {
			t.Fatal("old online score returned")
		}
	}
	if !limited {
		t.Fatal("disabled auditing disabled protection")
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM subscription_pull_audits`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("per-request audit writes: %d %v", count, err)
	}
}
