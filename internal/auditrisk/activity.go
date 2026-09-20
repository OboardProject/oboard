package auditrisk

import (
	"errors"
	"math/bits"
)

// SourceActivity must contain deduplicated delta bytes at the authorized
// measurement position. SourceGroup is an account-scoped keyed digest, never IP.
// Bitmap marks actual business payload in twelve five-second event-time slices.
type SourceActivity struct {
	SourceGroup string
	Bitmap      uint16
	Bytes       uint64
}
type Aggregation struct {
	Sources         CountRange
	Qualified       int
	Overflow        bool
	CounterOverflow bool
}

// AggregateMinute ORs one source across nodes before counting it. Input is
// bounded; callers must propagate upstream truncation through complete=false.
func AggregateMinute(rows []SourceActivity, complete bool, p Policy) (Aggregation, error) {
	var out Aggregation
	if err := p.Validate(); err != nil {
		return out, err
	}
	if len(rows) > 65536 {
		return out, errors.New("activity input exceeds budget")
	}
	groups := make(map[string]SourceActivity, p.SourceCapacity)
	for _, r := range rows {
		if r.SourceGroup == "" || len(r.SourceGroup) > 128 || r.Bitmap & ^uint16(0xfff) != 0 || (r.Bytes > 0 && r.Bitmap == 0) || (r.Bytes == 0 && r.Bitmap != 0) {
			return out, errors.New("invalid source activity")
		}
		old, ok := groups[r.SourceGroup]
		if !ok && len(groups) >= p.SourceCapacity {
			out.Overflow = true
			continue
		}
		old.Bitmap |= r.Bitmap
		if ^uint64(0)-old.Bytes < r.Bytes {
			old.Bytes = ^uint64(0)
			out.CounterOverflow = true
		} else {
			old.Bytes += r.Bytes
		}
		groups[r.SourceGroup] = old
	}
	var counts [12]int
	for _, g := range groups {
		if g.Bytes < p.MinimumBytes || bits.OnesCount16(g.Bitmap) < p.MinimumSlices {
			continue
		}
		out.Qualified++
		for j := range counts {
			if g.Bitmap&(1<<j) != 0 {
				counts[j]++
			}
		}
	}
	// Third-largest slice count, not peak size paired with unrelated duration.
	var top [3]int
	for _, n := range counts {
		for j := range top {
			if n > top[j] {
				n, top[j] = top[j], n
			}
		}
	}
	out.Sources = CountRange{top[2], top[2]}
	if !complete || out.Overflow || out.CounterOverflow {
		out.Sources.Upper = p.ActivitySources.Full
		if out.Sources.Lower > out.Sources.Upper {
			out.Sources.Upper = out.Sources.Lower
		}
	}
	return out, nil
}

// BaselineDay contains account-merged valid active-minute histogram counts.
// Missing, anomalous, investigation and action periods must be excluded before
// insertion. The fixed representation bounds work independently of raw history.
type BaselineDay struct {
	Histogram [33]uint16
	Overflow  bool
}
type Baseline struct {
	Ready         bool
	Limited       bool
	ValidDays     int
	ActiveMinutes int
	Q95           int
}

func SummarizeBaseline(days []BaselineDay) (Baseline, error) {
	var b Baseline
	if len(days) > 14 {
		return b, errors.New("baseline exceeds 14 days")
	}
	var merged [33]int
	for _, d := range days {
		if d.Overflow {
			b.Limited = true
			continue
		}
		total := 0
		for i, n := range d.Histogram {
			if i == 0 && n != 0 {
				return b, errors.New("baseline contains inactive minutes")
			}
			total += int(n)
		}
		if total > 1440 {
			return b, errors.New("baseline day exceeds 1440 minutes")
		}
		if total == 0 {
			continue
		}
		b.ValidDays++
		b.ActiveMinutes += total
		for i, n := range d.Histogram {
			merged[i] += int(n)
		}
	}
	b.Ready = b.ValidDays >= 7 && b.ActiveMinutes >= 200
	target := (b.ActiveMinutes*95 + 99) / 100
	sum := 0
	for i, n := range merged {
		sum += n
		if sum >= target {
			b.Q95 = i
			break
		}
	}
	return b, nil
}

// AdaptPolicy retains the configured saturation gap and stops rather than
// inventing precision beyond the representable source capacity.
func AdaptPolicy(p Policy, b Baseline) (Policy, bool, error) {
	if err := p.Validate(); err != nil {
		return p, false, err
	}
	if !b.Ready || b.Limited {
		return p, false, nil
	}
	start := max(p.ActivitySources.Start, b.Q95+1)
	full := start + p.ActivitySources.Full - p.ActivitySources.Start
	if full > p.SourceCapacity {
		return p, true, nil
	}
	p.ActivitySources = Scale{start, full}
	return p, false, nil
}
