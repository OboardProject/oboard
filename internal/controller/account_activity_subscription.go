package controller

import (
	"context"
	"time"

	"github.com/OboardProject/oboard/internal/auditactivity"
	"github.com/OboardProject/oboard/internal/auditrisk"
)

func (s *Server) attachAccountSubscriptionFeatures(ctx context.Context, f *auditrisk.Features, q *auditrisk.Quality, policy auditrisk.Policy, sourceVersion string, now time.Time) error {
	if !s.subscriptionAuditEnabled(ctx) {
		q.HistoryComplete = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "subscription_collection_disabled"}
		return nil
	}
	subscription, err := s.store.LoadAccountSubscriptionFeatures(ctx, f.AccountID, now)
	if err != nil {
		return err
	}
	if subscription.State.SourceVersion != sourceVersion {
		q.HistoryComplete = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "subscription_source_epoch_unavailable"}
		return nil
	}
	f.NovelRepeatedSources = auditrisk.CountRange{Lower: subscription.CandidateLower, Upper: subscription.CandidateUpper}
	q.HistoryComplete = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "subscription_history_incomplete"}
	if subscription.HistoryComplete {
		q.HistoryComplete = auditrisk.Dimension{State: auditrisk.Satisfied, ReasonCode: "subscription_history_complete"}
	}
	for i, minute := range f.Activity {
		sources, err := s.store.LoadAccountActivitySources(ctx, f.AccountID, minute.Start, sourceVersion)
		if err != nil {
			return err
		}
		confirmed := make([]auditrisk.SourceActivity, 0, len(sources))
		uncertain := !subscription.HistoryComplete || subscription.CandidateLower != subscription.CandidateUpper
		for _, source := range sources {
			seen, exists := subscription.State.Seen[source.SourceGroup]
			if !exists || seen.SecondUpdate.IsZero() || now.Sub(seen.FirstSeen) >= auditactivity.DefaultSubscriptionPolicy().CandidateLifetime {
				continue
			}
			if seen.NoveltyAtFirstSeen != auditactivity.Novel {
				continue
			}
			if minute.Start.Before(seen.FirstSeen) {
				// Byte counters are minute-wide. The first partial minute cannot prove
				// that its qualification bytes occurred after the configuration update.
				if minute.Start.Add(time.Minute).After(seen.FirstSeen) {
					uncertain = true
				}
				continue
			}
			confirmed = append(confirmed, source)
		}
		complete := minute.Sources.Lower == minute.Sources.Upper && q.CoverageComplete.State == auditrisk.Satisfied && q.SourceSetComplete.State == auditrisk.Satisfied && q.TimeAligned.State == auditrisk.Satisfied && !uncertain
		aggregate, err := auditrisk.AggregateMinute(confirmed, complete, policy)
		if err != nil {
			return err
		}
		f.ExposureActivity[i] = auditrisk.Minute{Start: minute.Start, Sources: aggregate.Sources}
	}
	return nil
}
