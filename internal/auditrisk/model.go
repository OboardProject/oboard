// Package auditrisk evaluates bounded account-level evidence without I/O or side effects.
package auditrisk

import (
	"errors"
	"fmt"
	"math/big"
	"time"
)

const WindowMinutes = 30

type State string

const (
	Satisfied   State = "satisfied"
	Unsatisfied State = "unsatisfied"
	Unknown     State = "unknown"
)

type Dimension struct {
	State      State  `json:"state"`
	ReasonCode string `json:"reason_code"`
}
type Quality struct {
	IdentityTrusted     Dimension `json:"identity_trusted"`
	SourceUsable        Dimension `json:"source_usable"`
	Deduplicated        Dimension `json:"deduplicated"`
	MeasurementValid    Dimension `json:"measurement_valid"`
	CoverageComplete    Dimension `json:"coverage_complete"`
	TimeAligned         Dimension `json:"time_aligned"`
	SourceSetComplete   Dimension `json:"source_set_complete"`
	BaselineReady       Dimension `json:"baseline_ready"`
	HistoryComplete     Dimension `json:"history_complete"`
	Freshness           Dimension `json:"freshness"`
	CapabilitySupported Dimension `json:"capability_supported"`
}

func (q Quality) dimensions() []Dimension {
	return []Dimension{q.IdentityTrusted, q.SourceUsable, q.Deduplicated, q.MeasurementValid, q.CoverageComplete, q.TimeAligned, q.SourceSetComplete, q.BaselineReady, q.HistoryComplete, q.Freshness, q.CapabilitySupported}
}

type Scale struct {
	Start int `json:"start"`
	Full  int `json:"full"`
}
type Policy struct {
	Version         string `json:"version"`
	ActivitySources Scale  `json:"activity_sources"`
	ActivityMinutes Scale  `json:"activity_minutes"`
	ExposureSources Scale  `json:"exposure_sources"`
	SourceCapacity  int    `json:"source_capacity"`
	MinimumBytes    uint64 `json:"minimum_bytes"`
	MinimumSlices   int    `json:"minimum_slices"`
}

func DefaultPolicy() Policy {
	return Policy{"account-risk-v1", Scale{3, 8}, Scale{5, 20}, Scale{3, 8}, 32, 16 * 1024, 3}
}
func validScale(s Scale) bool { return s.Start >= 0 && s.Full > s.Start }
func (p Policy) Validate() error {
	if p.Version == "" || !validScale(p.ActivitySources) || !validScale(p.ActivityMinutes) || !validScale(p.ExposureSources) || p.SourceCapacity < 1 || p.SourceCapacity > 32 || p.ActivitySources.Full > p.SourceCapacity || p.ExposureSources.Full > p.SourceCapacity || p.ActivityMinutes.Full > WindowMinutes || p.MinimumBytes == 0 || p.MinimumSlices < 1 || p.MinimumSlices > 12 {
		return errors.New("invalid risk policy")
	}
	return nil
}

type CountRange struct {
	Lower int `json:"lower"`
	Upper int `json:"upper"`
}

// Minute is an ended UTC natural minute. Bounds must already reflect coverage,
// overflow and missing collectors; missing evidence must never be encoded as zero.
type Minute struct {
	Start   time.Time  `json:"start"`
	Sources CountRange `json:"sources"`
}
type Contribution struct {
	Sources            int   `json:"sources"`
	Minutes            int   `json:"minutes"`
	SourceThresholds   Scale `json:"source_thresholds"`
	DurationThresholds Scale `json:"duration_thresholds"`
}
type Score struct {
	Lower             int           `json:"lower"`
	Upper             int           `json:"upper"`
	Status            string        `json:"status"`
	Level             string        `json:"level"`
	LowerContribution *Contribution `json:"lower_contribution,omitempty"`
	UpperContribution *Contribution `json:"upper_contribution,omitempty"`
}
type ResourceDimension struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
	// Nil observation or thresholds is unavailable, not zero.
	Value *uint64 `json:"value"`
	Start *uint64 `json:"start"`
	Full  *uint64 `json:"full"`
}
type Features struct {
	AccountID int64                 `json:"account_id"`
	Activity  [WindowMinutes]Minute `json:"activity"`
	// ExposureActivity contains only activity after qualifying logical updates.
	ExposureActivity     [WindowMinutes]Minute `json:"exposure_activity"`
	NovelRepeatedSources CountRange            `json:"novel_repeated_sources"`
	Resources            []ResourceDimension   `json:"resources"`
	DataRevision         uint64                `json:"data_revision"`
	EvidenceCutoff       time.Time             `json:"evidence_cutoff"`
}
type Versions struct {
	Model    string `json:"model"`
	Baseline string `json:"baseline"`
	Source   string `json:"source"`
}
type Snapshot struct {
	AccountID               int64     `json:"account_id"`
	Activity                *Score    `json:"activity"`
	Exposure                *Score    `json:"exposure"`
	Resource                *Score    `json:"resource"`
	Attention               *Score    `json:"attention"`
	Quality                 Quality   `json:"quality"`
	Features                Features  `json:"features"`
	Policy                  Policy    `json:"policy"`
	Versions                Versions  `json:"versions"`
	AsOf                    time.Time `json:"as_of"`
	WindowStart             time.Time `json:"window_start"`
	WindowEnd               time.Time `json:"window_end"`
	Status                  string    `json:"status"`
	AutomaticActionEligible bool      `json:"automatic_action_eligible"`
	ActionBlockReasons      []string  `json:"action_block_reasons"`
}

func normalized(x int, s Scale) *big.Rat {
	if x <= s.Start {
		return new(big.Rat)
	}
	if x >= s.Full {
		return big.NewRat(1, 1)
	}
	return big.NewRat(int64(x-s.Start), int64(s.Full-s.Start))
}
func halfUp(r *big.Rat) int {
	n := new(big.Int).Mul(r.Num(), big.NewInt(200))
	n.Add(n, r.Denom())
	d := new(big.Int).Mul(r.Denom(), big.NewInt(2))
	return int(n.Quo(n, d).Int64())
}
func level(n int) string {
	if n >= 90 {
		return "very_high"
	}
	if n >= 70 {
		return "high"
	}
	if n >= 40 {
		return "medium"
	}
	return "low"
}
func score(lo, hi *big.Rat) *Score {
	l, h := halfUp(lo), halfUp(hi)
	s := "complete"
	v := level(l)
	if lo.Cmp(hi) != 0 {
		s = "range"
		v = "uncertain"
	}
	return &Score{Lower: l, Upper: h, Status: s, Level: v}
}
func activity(minutes [WindowMinutes]Minute, p Policy, upper bool) (*big.Rat, *Contribution) {
	best := new(big.Rat)
	var evidence *Contribution
	for k := p.ActivitySources.Start + 1; k <= p.ActivitySources.Full; k++ {
		d := 0
		for _, m := range minutes {
			n := m.Sources.Lower
			if upper {
				n = m.Sources.Upper
			}
			if n >= k {
				d++
			}
		}
		v := new(big.Rat).Mul(normalized(k, p.ActivitySources), normalized(d, p.ActivityMinutes))
		if v.Cmp(best) > 0 {
			best = v
			evidence = &Contribution{k, d, p.ActivitySources, p.ActivityMinutes}
		}
	}
	return best, evidence
}
func activityBounds(m [WindowMinutes]Minute, p Policy) (*big.Rat, *big.Rat, *Score) {
	lo, lc := activity(m, p, false)
	hi, hc := activity(m, p, true)
	s := score(lo, hi)
	s.LowerContribution = lc
	s.UpperContribution = hc
	return lo, hi, s
}
func validateMinutes(m [WindowMinutes]Minute, end time.Time, capacity int) error {
	for i, v := range m {
		if !v.Start.Equal(end.Add(time.Duration(i-WindowMinutes)*time.Minute)) || v.Sources.Lower < 0 || v.Sources.Upper < v.Sources.Lower || v.Sources.Upper > capacity {
			return fmt.Errorf("invalid minute %d", i)
		}
	}
	return nil
}
func maxRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

// Evaluate never reads the clock. AsOf fixes the 30 ended-minute time axis.
// First-release behavior is observation-only regardless of score or completeness.
func Evaluate(f Features, q Quality, p Policy, v Versions, asOf time.Time) (Snapshot, error) {
	s := Snapshot{AccountID: f.AccountID, Quality: q, Features: f, Policy: p, Versions: v, AsOf: asOf, WindowEnd: asOf.UTC().Truncate(time.Minute), Status: "not_evaluable", ActionBlockReasons: []string{"behavior_alert_only"}}
	s.WindowStart = s.WindowEnd.Add(-WindowMinutes * time.Minute)
	if err := p.Validate(); err != nil {
		return s, err
	}
	if f.AccountID <= 0 || asOf.IsZero() || v.Model == "" || v.Baseline == "" || v.Source == "" || f.EvidenceCutoff.IsZero() || f.EvidenceCutoff.After(asOf) {
		return s, errors.New("invalid evaluation identity, versions or time")
	}
	for _, d := range q.dimensions() {
		if (d.State != Satisfied && d.State != Unsatisfied && d.State != Unknown) || d.ReasonCode == "" {
			return s, errors.New("invalid quality dimension")
		}
	}
	if err := validateMinutes(f.Activity, s.WindowEnd, p.SourceCapacity); err != nil {
		return s, err
	}
	if err := validateMinutes(f.ExposureActivity, s.WindowEnd, p.SourceCapacity); err != nil {
		return s, err
	}
	n := f.NovelRepeatedSources
	if n.Lower < 0 || n.Upper < n.Lower || n.Upper > p.SourceCapacity {
		return s, errors.New("invalid novelty bounds")
	}
	var err error
	s.Resource, err = resource(f.Resources)
	if err != nil {
		return s, err
	}
	for _, d := range []Dimension{q.IdentityTrusted, q.SourceUsable, q.Deduplicated, q.MeasurementValid, q.TimeAligned} {
		if d.State != Satisfied {
			s.ActionBlockReasons = append(s.ActionBlockReasons, d.ReasonCode)
			return s, nil
		}
	}
	activityMinutes := f.Activity
	if q.CapabilitySupported.State == Unsatisfied {
		for i := range activityMinutes {
			activityMinutes[i].Sources = CountRange{0, p.ActivitySources.Full}
		}
	}
	al, ah, a := activityBounds(activityMinutes, p)
	s.Activity = a
	ml, mh, _ := activityBounds(f.ExposureActivity, p)
	// Entirely unsupported collection cannot establish post-update activity.
	// Partial capability uses the caller's per-minute conservative bounds.
	if q.CapabilitySupported.State == Unsatisfied {
		ml = new(big.Rat)
		mh = big.NewRat(1, 1)
	}
	if q.HistoryComplete.State != Satisfied {
		n.Lower = 0
		n.Upper = p.ExposureSources.Full
	}
	exposure := func(n int, m *big.Rat) *big.Rat {
		factor := new(big.Rat).Add(big.NewRat(1, 1), m)
		factor.Quo(factor, big.NewRat(2, 1))
		return factor.Mul(factor, normalized(n, p.ExposureSources))
	}
	el, eh := exposure(n.Lower, ml), exposure(n.Upper, mh)
	s.Exposure = score(el, eh)
	s.Attention = score(maxRat(al, el), maxRat(ah, eh))
	s.Status = "evaluated"
	if q.Freshness.State != Satisfied {
		s.Status = "stale"
		s.ActionBlockReasons = append(s.ActionBlockReasons, "evidence_not_fresh")
	}
	return s, nil
}
func resource(ds []ResourceDimension) (*Score, error) {
	if len(ds) > 3 {
		return nil, errors.New("at most three resource dimensions")
	}
	lo, hi := new(big.Rat), new(big.Rat)
	known := 0
	seen := map[string]bool{}
	for _, d := range ds {
		if (d.Name != "request_rate" && d.Name != "connections" && d.Name != "traffic_rate") || d.Unit == "" || seen[d.Name] {
			return nil, errors.New("invalid resource dimension")
		}
		seen[d.Name] = true
		if (d.Start == nil) != (d.Full == nil) {
			return nil, errors.New("incomplete resource thresholds")
		}
		if d.Start != nil && *d.Full <= *d.Start {
			return nil, errors.New("invalid resource thresholds")
		}
		if d.Value == nil || d.Start == nil {
			hi = big.NewRat(1, 1)
			continue
		}
		known++
		r := new(big.Rat)
		if *d.Value >= *d.Full {
			r.SetInt64(1)
		} else if *d.Value > *d.Start {
			r.SetFrac(new(big.Int).SetUint64(*d.Value-*d.Start), new(big.Int).SetUint64(*d.Full-*d.Start))
		}
		lo = maxRat(lo, r)
		hi = maxRat(hi, r)
	}
	if known == 0 {
		return nil, nil
	}
	if len(ds) < 3 {
		hi = big.NewRat(1, 1)
	}
	return score(lo, hi), nil
}
