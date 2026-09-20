package auditactivity

import (
	"errors"
	"math"
	"time"
)

const MaxSources = 32
const MaxUpdateKeys = 64

type Novelty string

const (
	Novel          Novelty = "new"
	Known          Novelty = "known"
	NoveltyUnknown Novelty = "unknown"
)

type SubscriptionPolicy struct {
	History           time.Duration
	CandidateLifetime time.Duration
	MergeWindow       time.Duration
	RepeatInterval    time.Duration
}

func DefaultSubscriptionPolicy() SubscriptionPolicy {
	return SubscriptionPolicy{7 * 24 * time.Hour, 60 * time.Minute, 60 * time.Second, 5 * time.Minute}
}
func (p SubscriptionPolicy) Validate() error {
	if p.History < time.Hour || p.History > 30*24*time.Hour || p.CandidateLifetime < time.Minute || p.CandidateLifetime > 24*time.Hour || p.MergeWindow <= 0 || p.RepeatInterval < p.MergeWindow || p.RepeatInterval >= p.CandidateLifetime {
		return errors.New("invalid subscription activity policy")
	}
	return nil
}

type SeenSource struct {
	LastSeen           time.Time `json:"last_seen"`
	FirstSeen          time.Time `json:"first_seen"`
	NoveltyAtFirstSeen Novelty   `json:"novelty_at_first_seen"`
	SecondUpdate       time.Time `json:"second_update"`
}
type UpdateKey struct {
	Source                string `json:"source"`
	Representation        string `json:"representation"`
	ConfigurationRevision string `json:"configuration_revision"`
}
type LogicalUpdate struct {
	Key UpdateKey `json:"key"`
	At  time.Time `json:"at"`
}

// SubscriptionState belongs to one authenticated account, token revision, source
// algorithm and key epoch. Persist it together with the applied inbox position.
// It deliberately has no LRU: capacity loss invalidates novelty until a complete
// new history horizon has elapsed. Eviction is not a first observation.
type SubscriptionState struct {
	TokenVersion        string                `json:"token_version"`
	SourceVersion       string                `json:"source_version"`
	HistoryStarted      time.Time             `json:"history_started"`
	HistoryUnknownUntil time.Time             `json:"history_unknown_until"`
	AsOf                time.Time             `json:"as_of"`
	Seen                map[string]SeenSource `json:"seen"`
	Updates             []LogicalUpdate       `json:"updates"`
	RawRequests         uint64                `json:"raw_requests"`
	LogicalUpdates      uint64                `json:"logical_updates"`
	CapacityLimited     bool                  `json:"capacity_limited"`
}

type SubscriptionObservation struct {
	At                    time.Time
	Source                string
	Representation        string
	ConfigurationRevision string
	TokenVersion          string
	SourceVersion         string
	Success               bool
	IdentityTrusted       bool
	SourceUsable          bool
	HistoryComplete       bool
}

type UpdateResult struct {
	Logical bool
	Novelty Novelty
	Reason  string
}

func (s *SubscriptionState) Observe(o SubscriptionObservation, p SubscriptionPolicy) (UpdateResult, error) {
	if err := p.Validate(); err != nil {
		return UpdateResult{}, err
	}
	if !o.IdentityTrusted {
		return UpdateResult{}, errors.New("identity_untrusted")
	}
	if o.At.IsZero() || (!s.AsOf.IsZero() && o.At.Before(s.AsOf)) {
		return UpdateResult{}, errors.New("event_requires_ordered_replay")
	}
	if o.TokenVersion == "" || len(o.TokenVersion) > 128 || o.SourceVersion == "" || len(o.SourceVersion) > 128 || len(o.Source) > 128 || len(o.Representation) > 64 || len(o.ConfigurationRevision) > 128 {
		return UpdateResult{}, errors.New("invalid activity dimensions")
	}
	if s.RawRequests == math.MaxUint64 || s.LogicalUpdates == math.MaxUint64 {
		return UpdateResult{}, errors.New("counter_overflow")
	}
	if s.TokenVersion != o.TokenVersion || s.SourceVersion != o.SourceVersion {
		*s = SubscriptionState{TokenVersion: o.TokenVersion, SourceVersion: o.SourceVersion, HistoryStarted: o.At, Seen: make(map[string]SeenSource)}
	}
	if s.Seen == nil {
		s.Seen = make(map[string]SeenSource)
	}
	s.RawRequests++
	s.AsOf = o.At
	if !o.HistoryComplete {
		s.HistoryUnknownUntil = o.At.Add(p.History)
	}
	for source, seen := range s.Seen {
		if !seen.LastSeen.After(o.At.Add(-p.History)) {
			delete(s.Seen, source)
		}
	}
	keep := s.Updates[:0]
	for _, u := range s.Updates {
		if u.At.After(o.At.Add(-p.MergeWindow)) {
			keep = append(keep, u)
		}
	}
	s.Updates = keep
	if !o.Success {
		return UpdateResult{Reason: "request_not_successful"}, nil
	}
	if !o.SourceUsable || o.Source == "" {
		s.HistoryUnknownUntil = o.At.Add(p.History)
		return UpdateResult{Novelty: NoveltyUnknown, Reason: "source_unusable"}, nil
	}
	key := UpdateKey{o.Source, o.Representation, o.ConfigurationRevision}
	for _, u := range s.Updates {
		if u.Key == key {
			if seen, ok := s.Seen[o.Source]; ok {
				seen.LastSeen = o.At
				s.Seen[o.Source] = seen
			}
			return UpdateResult{Reason: "retry_merged"}, nil
		}
	}
	if len(s.Updates) >= MaxUpdateKeys {
		s.CapacityLimited = true
		s.HistoryUnknownUntil = o.At.Add(p.History)
		return UpdateResult{Novelty: NoveltyUnknown, Reason: "update_capacity"}, nil
	}
	s.Updates = append(s.Updates, LogicalUpdate{key, o.At})
	s.LogicalUpdates++
	seen, exists := s.Seen[o.Source]
	if !exists {
		if len(s.Seen) >= MaxSources {
			s.CapacityLimited = true
			s.HistoryUnknownUntil = o.At.Add(p.History)
			return UpdateResult{Logical: true, Novelty: NoveltyUnknown, Reason: "source_capacity"}, nil
		}
		novelty := NoveltyUnknown
		if o.HistoryComplete && !o.At.Before(s.HistoryStarted.Add(p.History)) && !o.At.Before(s.HistoryUnknownUntil) {
			novelty = Novel
		}
		seen = SeenSource{FirstSeen: o.At, NoveltyAtFirstSeen: novelty}
	}
	if exists && o.At.Sub(seen.FirstSeen) >= p.CandidateLifetime {
		seen.NoveltyAtFirstSeen = Known
		seen.SecondUpdate = time.Time{}
	}
	if exists && seen.SecondUpdate.IsZero() && o.At.Sub(seen.FirstSeen) >= p.RepeatInterval && o.At.Sub(seen.FirstSeen) < p.CandidateLifetime {
		seen.SecondUpdate = o.At
	}
	seen.LastSeen = o.At
	s.Seen[o.Source] = seen
	return UpdateResult{Logical: true, Novelty: seen.NoveltyAtFirstSeen}, nil
}

// CandidateBounds propagates unknown novelty rather than treating unknown
// history as either all new or all familiar. Saturation is at source capacity.
func (s *SubscriptionState) CandidateBounds(asOf time.Time, p SubscriptionPolicy) (int, int, error) {
	if err := p.Validate(); err != nil {
		return 0, 0, err
	}
	if asOf.Before(s.AsOf) {
		return 0, 0, errors.New("event_requires_ordered_replay")
	}
	lo, hi := 0, 0
	for _, seen := range s.Seen {
		if asOf.Sub(seen.FirstSeen) >= p.CandidateLifetime || seen.SecondUpdate.IsZero() {
			continue
		}
		if seen.NoveltyAtFirstSeen == Novel {
			lo++
			hi++
		} else if seen.NoveltyAtFirstSeen == NoveltyUnknown {
			hi++
		}
	}
	if s.CapacityLimited && asOf.Before(s.HistoryUnknownUntil) {
		hi = MaxSources
	}
	return lo, hi, nil
}
