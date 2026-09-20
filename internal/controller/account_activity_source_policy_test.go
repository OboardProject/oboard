package controller

import (
	"context"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditactivity"
	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/store"
)

func TestConfiguredAccountSourcePolicy(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "source-secret", "")
	ctx := context.Background()
	c, err := db.GetAccountAuditPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, before, err := s.accountAuditSourcePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cached := s.accountSourceCache.Load()
	if _, _, err = s.accountAuditSourcePolicy(ctx); err != nil || s.accountSourceCache.Load() != cached {
		t.Fatal("cache not reused", err)
	}
	for _, config := range []store.AccountAuditSourceConfig{{IPv4Bits: 25, IPv6Bits: 56, Epoch: 1}, {IPv4Bits: 24, IPv6Bits: 57, Epoch: 1}, {IPv4Bits: 24, IPv6Bits: 56, Epoch: 0}} {
		invalid := c
		invalid.SourceGrouping = config
		if store.ValidateAccountAuditPolicy(invalid) == nil {
			t.Fatal("invalid source policy accepted")
		}
	}
	c.SourceGrouping = store.AccountAuditSourceConfig{IPv4Bits: 16, IPv6Bits: 48, Epoch: 2}
	c, err = db.SetAccountAuditPolicy(ctx, c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, p, err := s.accountAuditSourcePolicy(ctx)
	if err != nil || p.ID() == before.ID() || s.accountSourceCache.Load() == cached {
		t.Fatal("policy cache not invalidated", err)
	}
	for _, addresses := range [][2]string{{"8.8.9.0/24", "8.8.10.42"}, {"2606:4700:1234:ab00::/56", "2606:4700:1234:cd00::42"}} {
		a, err := accountActivitySourceGroup(s.sessionSecret, 1, addresses[0], p)
		if err != nil {
			t.Fatal(err)
		}
		b, err := auditactivity.SourceGroup(key, 1, addresses[1], true, p)
		if err != nil || a != b {
			t.Fatal("connection and subscription groups differ", err)
		}
		other, _ := accountActivitySourceGroup(s.sessionSecret, 2, addresses[0], p)
		if a == other {
			t.Fatal("cross-account group")
		}
	}
	if _, err := accountActivitySourceGroup(s.sessionSecret, 1, "8.8.0.0/16", p); err == nil {
		t.Fatal("noncanonical collector precision accepted")
	}
	now := time.Now().UTC().Truncate(time.Minute)
	coarse, _ := accountActivitySourceGroup(s.sessionSecret, 1, "8.8.9.0/24", p)
	fineA, _ := accountActivitySourceGroup(s.sessionSecret, 1, "8.8.9.0/24")
	fineB, _ := accountActivitySourceGroup(s.sessionSecret, 1, "8.8.10.0/24")
	batch := store.AccountActivityBatch{ServerID: 1, CollectorBootID: "0123456789abcdef0123456789abcdef", CollectorStartedAt: now.Add(-time.Hour).UnixNano(), StreamType: "kernel", Sequence: 1, MinuteUnix: now.Add(-time.Minute).Unix(), SourceVersion: p.ID(), ClockState: "aligned", Complete: true, Items: []store.AccountActivityBatchItem{
		{AccountID: 1, InboundID: 1, SourceGroup: coarse, CounterSource: fineA, ActivityBits: 7, UploadBytes: 60},
		{AccountID: 1, InboundID: 1, SourceGroup: coarse, CounterSource: fineB, ActivityBits: 7, UploadBytes: 60},
	}}
	if _, err := db.ReceiveAccountActivityBatch(ctx, batch, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApplyPendingAccountActivity(ctx, 10, now); err != nil {
		t.Fatal(err)
	}
	batch.Sequence++
	batch.Items = batch.Items[:1]
	batch.Items[0].UploadBytes = 70
	if _, err := db.ReceiveAccountActivityBatch(ctx, batch, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApplyPendingAccountActivity(ctx, 10, now); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveAccountActivityExpected(ctx, []store.AccountActivityExpected{{AccountID: 1, ServerID: 1, StreamType: "kernel", MinuteUnix: batch.MinuteUnix, CapabilitySupported: true}}, now); err != nil {
		t.Fatal(err)
	}
	threshold := auditrisk.DefaultPolicy()
	threshold.MinimumBytes = 130
	features, _, err := db.LoadAccountAuditFeatures(ctx, 1, now, threshold, p.ID())
	if err != nil || features.Activity[len(features.Activity)-1].Sources.Lower != 1 {
		t.Fatal("coarsened cumulative sources lost bytes", features.Activity, err)
	}
	c.SourceGrouping.Epoch = 1
	if _, err := db.SetAccountAuditPolicy(ctx, c, time.Now()); err == nil {
		t.Fatal("epoch rollback accepted")
	}
	c.SourceGrouping.Epoch = 3
	if _, err := db.SetAccountAuditPolicy(ctx, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, rotated, err := s.accountAuditSourcePolicy(ctx)
	if err != nil || rotated.ID() == p.ID() {
		t.Fatal("rotation reused source version", err)
	}
}
