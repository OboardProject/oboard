package model

import "time"

// ConnectivitySLAStats is a duration monoid over adjacent state-machine spans.
// Zero-length recoveries still split incidents, so WholeDown is not inferred
// solely from the sum of offline durations.
type ConnectivitySLAStats struct {
	DurationNS  int64 `json:"duration_ns"`
	OnlineNS    int64 `json:"online_ns"`
	OfflineNS   int64 `json:"offline_ns"`
	UnknownNS   int64 `json:"unknown_ns"`
	OutageCount int   `json:"outage_count"`
	LongestNS   int64 `json:"longest_ns"`
	PrefixNS    int64 `json:"prefix_ns"`
	SuffixNS    int64 `json:"suffix_ns"`
	StartsDown  bool  `json:"starts_down"`
	EndsDown    bool  `json:"ends_down"`
	WholeDown   bool  `json:"whole_down"`
}

func MergeConnectivitySLAStats(a, b ConnectivitySLAStats) ConnectivitySLAStats {
	if a.DurationNS == 0 {
		return b
	}
	if b.DurationNS == 0 {
		return a
	}
	joined := a.EndsDown && b.StartsDown
	result := ConnectivitySLAStats{DurationNS: a.DurationNS + b.DurationNS, OnlineNS: a.OnlineNS + b.OnlineNS, OfflineNS: a.OfflineNS + b.OfflineNS, UnknownNS: a.UnknownNS + b.UnknownNS, OutageCount: a.OutageCount + b.OutageCount, LongestNS: max(a.LongestNS, b.LongestNS), PrefixNS: a.PrefixNS, SuffixNS: b.SuffixNS, StartsDown: a.StartsDown, EndsDown: b.EndsDown, WholeDown: a.WholeDown && b.WholeDown && joined}
	if joined {
		result.OutageCount--
		result.LongestNS = max(result.LongestNS, a.SuffixNS+b.PrefixNS)
		if a.WholeDown {
			result.PrefixNS = a.DurationNS + b.PrefixNS
		}
		if b.WholeDown {
			result.SuffixNS = b.DurationNS + a.SuffixNS
		}
	}
	return result
}

type ConnectivityOutage struct {
	StartedAt           time.Time  `json:"started_at"`
	EndedAt             *time.Time `json:"ended_at"`
	DurationSeconds     float64    `json:"duration_seconds"`
	Cause               string     `json:"cause"`
	StartedBeforeWindow bool       `json:"started_before_window"`
}
