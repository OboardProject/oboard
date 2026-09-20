package auditrisk

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
	"time"
)

func fixture() (Features, Quality, Policy, Versions, time.Time) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	f := Features{AccountID: 1, EvidenceCutoff: now}
	for i := 0; i < WindowMinutes; i++ {
		m := Minute{now.Add(time.Duration(i-WindowMinutes) * time.Minute), CountRange{1, 1}}
		f.Activity[i] = m
		f.ExposureActivity[i] = m
	}
	d := Dimension{Satisfied, "verified"}
	q := Quality{d, d, d, d, d, d, d, d, d, d, d}
	return f, q, DefaultPolicy(), Versions{"v1", "b1", "source-v1"}, now
}
func TestExamples(t *testing.T) {
	for _, tc := range []struct{ sources, minutes, want int }{{3, 30, 0}, {8, 3, 0}, {8, 5, 0}, {6, 15, 40}, {8, 15, 67}, {8, 20, 100}} {
		f, q, p, v, now := fixture()
		for i := 0; i < tc.minutes; i++ {
			f.Activity[i].Sources = CountRange{tc.sources, tc.sources}
		}
		s, err := Evaluate(f, q, p, v, now)
		if err != nil || s.Activity.Lower != tc.want || s.Activity.Upper != tc.want {
			t.Fatalf("%+v: %+v %v", tc, s.Activity, err)
		}
		if s.AutomaticActionEligible {
			t.Fatal("behavior action enabled")
		}
	}
	f, q, p, v, now := fixture()
	for i := 0; i < 12; i++ {
		f.Activity[i].Sources = CountRange{8, 8}
	}
	for i := 12; i < 20; i++ {
		f.Activity[i].Sources = CountRange{0, 8}
	}
	q.CoverageComplete = Dimension{Unsatisfied, "missing_nodes"}
	s, err := Evaluate(f, q, p, v, now)
	if err != nil || s.Activity.Lower != 47 || s.Activity.Upper != 100 {
		t.Fatalf("missing range: %+v %v", s.Activity, err)
	}
}
func TestExposureAndIndependentResource(t *testing.T) {
	f, q, p, v, now := fixture()
	f.NovelRepeatedSources = CountRange{8, 8}
	s, err := Evaluate(f, q, p, v, now)
	if err != nil || s.Exposure.Lower != 50 || s.Exposure.Upper != 50 {
		t.Fatalf("%+v %v", s.Exposure, err)
	}
	for i := range f.ExposureActivity {
		f.ExposureActivity[i].Sources = CountRange{8, 8}
	}
	s, _ = Evaluate(f, q, p, v, now)
	if s.Exposure.Lower != 100 {
		t.Fatal(s.Exposure)
	}
	q.CapabilitySupported = Dimension{Unsatisfied, "legacy_collector"}
	s, _ = Evaluate(f, q, p, v, now)
	if s.Exposure.Lower != 50 || s.Exposure.Upper != 100 || s.Activity.Lower != 0 || s.Activity.Upper != 100 {
		t.Fatalf("missing capability: %+v %+v", s.Exposure, s.Activity)
	}
	q.HistoryComplete = Dimension{Unknown, "epoch_changed"}
	s, _ = Evaluate(f, q, p, v, now)
	if s.Exposure.Lower != 0 || s.Exposure.Upper != 100 {
		t.Fatal(s.Exposure)
	}
	f, q, p, v, now = fixture()
	zero, full := uint64(0), uint64(100)
	for _, name := range []string{"request_rate", "connections", "traffic_rate"} {
		f.Resources = append(f.Resources, ResourceDimension{name, "per_second", &full, &zero, &full})
	}
	s, err = Evaluate(f, q, p, v, now)
	if err != nil || s.Resource.Lower != 100 || s.Attention.Lower != 0 {
		t.Fatalf("resource contaminated behavior: %+v %v", s, err)
	}
	q.IdentityTrusted = Dimension{Unknown, "untrusted_identity"}
	s, _ = Evaluate(f, q, p, v, now)
	if s.Activity != nil || s.Exposure != nil || s.Status != "not_evaluable" {
		t.Fatal("untrusted lower bound")
	}
}
func reference(m [WindowMinutes]Minute, p Policy, upper bool) int {
	best := 0.0
	for k := p.ActivitySources.Start + 1; k <= p.ActivitySources.Full; k++ {
		d := 0
		for _, v := range m {
			n := v.Sources.Lower
			if upper {
				n = v.Sources.Upper
			}
			if n >= k {
				d++
			}
		}
		duration := math.Max(0, math.Min(1, float64(d-p.ActivityMinutes.Start)/float64(p.ActivityMinutes.Full-p.ActivityMinutes.Start)))
		value := float64(k-p.ActivitySources.Start) / float64(p.ActivitySources.Full-p.ActivitySources.Start) * duration
		if value > best {
			best = value
		}
	}
	return int(math.Floor(best*100 + 0.5 + 1e-10))
}
func TestFixedSeedProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(7821))
	f, _, p, _, _ := fixture()
	for trial := 0; trial < 2000; trial++ {
		for i := range f.Activity {
			lo := rng.Intn(9)
			f.Activity[i].Sources = CountRange{lo, lo + rng.Intn(9-lo)}
		}
		_, _, s := activityBounds(f.Activity, p)
		if s.Lower < 0 || s.Upper > 100 || s.Lower > s.Upper || s.Lower != reference(f.Activity, p, false) || s.Upper != reference(f.Activity, p, true) {
			t.Fatal(s)
		}
		complete := f.Activity
		for i := range complete {
			r := complete[i].Sources
			n := r.Lower + rng.Intn(r.Upper-r.Lower+1)
			complete[i].Sources = CountRange{n, n}
		}
		_, _, c := activityBounds(complete, p)
		if c.Lower < s.Lower || c.Upper > s.Upper {
			t.Fatal("range does not enclose completion")
		}
		tighter := f.Activity
		for i := range tighter {
			if tighter[i].Sources.Lower < tighter[i].Sources.Upper {
				tighter[i].Sources.Lower++
			}
		}
		_, _, ts := activityBounds(tighter, p)
		if ts.Lower < s.Lower || ts.Upper > s.Upper {
			t.Fatal("bounds did not tighten")
		}
		higher := complete
		for i := range higher {
			higher[i].Sources.Lower++
			higher[i].Sources.Upper++
		}
		_, _, h := activityBounds(higher, p)
		if h.Lower < c.Lower {
			t.Fatal("not monotone")
		}
	}
}
func TestValidationAndRounding(t *testing.T) {
	if halfUp(big.NewRat(1, 200)) != 1 || halfUp(big.NewRat(1, 201)) != 0 {
		t.Fatal("half-up")
	}
	f, q, p, v, now := fixture()
	bad := p
	bad.ActivityMinutes = Scale{20, 20}
	if _, err := Evaluate(f, q, bad, v, now); err == nil {
		t.Fatal("invalid policy accepted")
	}
	f.Activity[29].Start = now
	if _, err := Evaluate(f, q, p, v, now); err == nil {
		t.Fatal("unfinished minute accepted")
	}
	f, _, _, _, _ = fixture()
	f.Activity[0].Start = f.Activity[1].Start
	if _, err := Evaluate(f, q, p, v, now); err == nil {
		t.Fatal("gap replaced with older minute")
	}
	maxValue := ^uint64(0)
	zero := uint64(0)
	s, err := resource([]ResourceDimension{{"request_rate", "requests/s", &maxValue, &zero, &maxValue}})
	if err != nil || s.Lower != 100 {
		t.Fatal("uint overflow", s, err)
	}
}
func TestNoSplicedPeakAndDuration(t *testing.T) {
	f, _, p, _, _ := fixture()
	for i := 0; i < 20; i++ {
		f.Activity[i].Sources = CountRange{4, 4}
	}
	f.Activity[0].Sources = CountRange{8, 8}
	_, _, s := activityBounds(f.Activity, p)
	if s.Lower != 20 || s.LowerContribution.Sources != 4 || s.LowerContribution.Minutes != 20 {
		t.Fatal(s)
	}
}
func TestAggregateMinute(t *testing.T) {
	p := DefaultPolicy()
	rows := []SourceActivity{{"a", 1, 8192}, {"a", 6, 8192}, {"b", 7, 16384}, {"c", 7, 16383}}
	a, err := AggregateMinute(rows, true, p)
	if err != nil || a.Sources != (CountRange{2, 2}) {
		t.Fatal(a, err)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	b, _ := AggregateMinute(rows, true, p)
	if a != b {
		t.Fatal("merge order changed result")
	}
	a, _ = AggregateMinute(rows, false, p)
	if a.Sources != (CountRange{2, 8}) {
		t.Fatal(a)
	}
	rows = nil
	for i := 0; i < 33; i++ {
		rows = append(rows, SourceActivity{string(rune('a' + i)), 7, 16384})
	}
	a, _ = AggregateMinute(rows, true, p)
	if !a.Overflow || a.Sources.Lower != 32 {
		t.Fatal(a)
	}
	_, err = AggregateMinute([]SourceActivity{{"a", 0x1000, 1}}, true, p)
	if err == nil {
		t.Fatal("invalid bitmap accepted")
	}
	a, err = AggregateMinute([]SourceActivity{{"a", 7, ^uint64(0)}, {"a", 7, 1}}, true, p)
	if err != nil || !a.CounterOverflow {
		t.Fatal(a, err)
	}
}
func TestBaseline(t *testing.T) {
	days := make([]BaselineDay, 7)
	for i := range days {
		days[i].Histogram[4] = 30
	}
	b, err := SummarizeBaseline(days)
	if err != nil || !b.Ready || b.Q95 != 4 {
		t.Fatal(b, err)
	}
	p, limited, err := AdaptPolicy(DefaultPolicy(), b)
	if err != nil || limited || p.ActivitySources != (Scale{5, 10}) {
		t.Fatal(p, limited, err)
	}
	days[0].Overflow = true
	b, _ = SummarizeBaseline(days)
	if b.Ready || !b.Limited {
		t.Fatal(b)
	}
}
func BenchmarkEvaluate(b *testing.B) {
	f, q, p, v, now := fixture()
	f.NovelRepeatedSources = CountRange{4, 8}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Evaluate(f, q, p, v, now); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkAggregateMinute(b *testing.B) {
	p := DefaultPolicy()
	rows := make([]SourceActivity, 32)
	for i := range rows {
		rows[i] = SourceActivity{string(rune('a' + i)), 0xfff, 16384}
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := AggregateMinute(rows, true, p); err != nil {
			b.Fatal(err)
		}
	}
}
