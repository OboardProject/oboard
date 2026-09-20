package auditactivity

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestSourceGroupTrustScopeAndNormalization(t *testing.T) {
	p := SourcePolicy{"prefix-v1", "epoch-1", 24, 56}
	key := bytes.Repeat([]byte{7}, 32)
	group := func(account int64, ip string) string {
		t.Helper()
		g, e := SourceGroup(key, account, ip, true, p)
		if e != nil {
			t.Fatal(e)
		}
		return g
	}
	if group(1, "8.8.8.1") != group(1, "::ffff:8.8.8.2") {
		t.Fatal("mapped IPv6 not normalized")
	}
	if group(1, "8.8.8.1") == group(2, "8.8.8.1") {
		t.Fatal("cross-account tracking identifier")
	}
	if group(1, "2001:4860:1234:5600::1") != group(1, "2001:4860:1234:56ff::2") {
		t.Fatal("IPv6 /56 mismatch")
	}
	for _, ip := range []string{"bad", "127.0.0.1", "10.0.0.1", "::1", "fe80::1%en0"} {
		if _, e := SourceGroup(key, 1, ip, true, p); e == nil {
			t.Fatalf("accepted %s", ip)
		}
	}
	if _, e := SourceGroup(key, 1, "8.8.8.8", false, p); e == nil {
		t.Fatal("untrusted ingress accepted")
	}
	a := group(1, "8.8.8.8")
	p.Epoch = "epoch-2"
	if a == group(1, "8.8.8.8") {
		t.Fatal("epoch not bound")
	}
}
func fixture() (SubscriptionState, SubscriptionObservation, SubscriptionPolicy) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	p := DefaultSubscriptionPolicy()
	s := SubscriptionState{TokenVersion: "t1", SourceVersion: "p1/e1", HistoryStarted: now.Add(-p.History)}
	o := SubscriptionObservation{At: now, Source: "source1", Representation: "mihomo", ConfigurationRevision: "stable-1", TokenVersion: "t1", SourceVersion: "p1/e1", Success: true, IdentityTrusted: true, SourceUsable: true, HistoryComplete: true}
	return s, o, p
}
func TestLogicalRetryAndNoveltyAtFirstSeen(t *testing.T) {
	s, o, p := fixture()
	r, e := s.Observe(o, p)
	if e != nil || r.Novelty != Novel {
		t.Fatalf("%+v %v", r, e)
	}
	o.At = o.At.Add(20 * time.Second)
	r, e = s.Observe(o, p)
	if e != nil || r.Logical {
		t.Fatal("retry was not merged")
	}
	o.At = o.At.Add(5 * time.Minute)
	r, e = s.Observe(o, p)
	if e != nil || !r.Logical || r.Novelty != Novel {
		t.Fatalf("second update %+v %v", r, e)
	}
	lo, hi, e := s.CandidateBounds(o.At, p)
	if e != nil || lo != 1 || hi != 1 {
		t.Fatalf("%d..%d %v", lo, hi, e)
	}
	if s.RawRequests != 3 || s.LogicalUpdates != 2 {
		t.Fatalf("raw=%d logical=%d", s.RawRequests, s.LogicalUpdates)
	}
	lo, hi, _ = s.CandidateBounds(o.At.Add(time.Hour), p)
	if lo != 0 || hi != 0 {
		t.Fatal("candidate did not expire")
	}
}
func TestMissingHistoryFailureAndEpochAreNotNovel(t *testing.T) {
	for _, mode := range []string{"history", "epoch", "token", "failure", "untrusted"} {
		t.Run(mode, func(t *testing.T) {
			s, o, p := fixture()
			switch mode {
			case "history":
				o.HistoryComplete = false
			case "epoch":
				o.SourceVersion = "p2/e2"
			case "token":
				o.TokenVersion = "t2"
			case "failure":
				o.Success = false
			case "untrusted":
				o.IdentityTrusted = false
			}
			r, e := s.Observe(o, p)
			if mode == "untrusted" {
				if e == nil || s.RawRequests != 0 {
					t.Fatal("untrusted allocated activity")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if r.Novelty == Novel {
				t.Fatal("unknown/failed observation became new")
			}
			o.At = o.At.Add(5 * time.Minute)
			_, _ = s.Observe(o, p)
			lo, _, _ := s.CandidateBounds(o.At, p)
			if lo != 0 {
				t.Fatal("unproven novelty counted")
			}
		})
	}
}
func TestCapacityAndTimeRegression(t *testing.T) {
	s, o, p := fixture()
	for i := 0; i < MaxSources+5; i++ {
		o.Source = fmt.Sprintf("s%d", i)
		if _, e := s.Observe(o, p); e != nil {
			t.Fatal(e)
		}
	}
	if len(s.Seen) != MaxSources || !s.CapacityLimited {
		t.Fatal("source budget not enforced")
	}
	lo, hi, e := s.CandidateBounds(o.At, p)
	if e != nil || lo != 0 || hi != MaxSources {
		t.Fatalf("unknown capacity %d..%d %v", lo, hi, e)
	}
	before := s.RawRequests
	o.At = o.At.Add(-time.Second)
	if _, e = s.Observe(o, p); e == nil || s.RawRequests != before {
		t.Fatal("time regression mutated state")
	}
}
func BenchmarkSubscriptionActivity(b *testing.B) {
	s, o, p := fixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o.At = o.At.Add(time.Second)
		_, err := s.Observe(o, p)
		if err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkSourceGroup(b *testing.B) {
	p := SourcePolicy{"prefix-v1", "epoch-1", 24, 56}
	key := bytes.Repeat([]byte{7}, 32)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, e := SourceGroup(key, 1, "8.8.8.8", true, p)
		if e != nil {
			b.Fatal(e)
		}
	}
}
