package controller

import (
	"context"
	"sort"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) readSummarizedSLA(ctx context.Context, serverID int64, days int, window connectivityWindow) (connectivitySLAResponse, error) {
	read, err := s.store.QuerySLAHistory(ctx, serverID, window.From, window.To, window.BucketDuration, buildSLAProjection)
	if err != nil {
		return connectivitySLAResponse{}, err
	}
	total := model.ConnectivitySLAStats{}
	bins := map[int64]model.ConnectivitySLAStats{}
	outages := []connectivityOutage{}
	for _, part := range read.Parts {
		total = model.MergeConnectivitySLAStats(total, part.Stats)
		start := time.Unix(part.Start, 0).UTC().Truncate(window.BucketDuration)
		if start.Before(window.From) {
			start = window.From
		}
		bins[start.Unix()] = model.MergeConnectivitySLAStats(bins[start.Unix()], part.Stats)
		if part.Stats.OutageCount > len(part.Outages) {
			outages = outages[:0]
		}
		for i, item := range part.Outages {
			if i == 0 && item.StartedBeforeWindow && len(outages) > 0 && outages[len(outages)-1].EndedAt == nil {
				previous := &outages[len(outages)-1]
				previous.EndedAt = item.EndedAt
				previous.Cause = mergeOutageCause(previous.Cause, item.Cause)
				until := time.Unix(part.Start, 0).Add(time.Duration(part.Stats.DurationNS))
				if item.EndedAt != nil {
					until = *item.EndedAt
				}
				previous.DurationSeconds = until.Sub(previous.StartedAt).Seconds()
			} else {
				outages = append(outages, item)
			}
		}
		if len(outages) > 10 {
			outages = outages[len(outages)-10:]
		}
	}
	buckets := make([]connectivityBucket, 0, len(bins))
	for start, stats := range bins {
		at := time.Unix(start, 0).UTC()
		end := at.Truncate(window.BucketDuration).Add(window.BucketDuration)
		if end.After(window.To) {
			end = window.To
		}
		buckets = append(buckets, connectivityBucket{StartAt: at, EndAt: end, SLAPercent: connectivityPercent(time.Duration(stats.OnlineNS), time.Duration(stats.OfflineNS)), AvailableSeconds: time.Duration(stats.OnlineNS).Seconds(), UnavailableSeconds: time.Duration(stats.OfflineNS).Seconds(), UnknownSeconds: time.Duration(stats.UnknownNS).Seconds()})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].StartAt.Before(buckets[j].StartAt) })
	for l, r := 0, len(outages)-1; l < r; l, r = l+1, r-1 {
		outages[l], outages[r] = outages[r], outages[l]
	}
	observed := total.OnlineNS + total.OfflineNS
	coverage := 0.0
	if total.DurationNS > 0 {
		coverage = 100 * float64(observed) / float64(total.DurationNS)
	}
	return connectivitySLAResponse{ServerID: serverID, RetentionDays: days, Window: window, Summary: connectivitySummary{SLAPercent: connectivityPercent(time.Duration(total.OnlineNS), time.Duration(total.OfflineNS)), AvailableSeconds: time.Duration(total.OnlineNS).Seconds(), UnavailableSeconds: time.Duration(total.OfflineNS).Seconds(), UnknownSeconds: time.Duration(total.UnknownNS).Seconds(), ObservedSeconds: time.Duration(observed).Seconds(), CoveragePercent: coverage, OutageCount: total.OutageCount, LongestOutageSecond: time.Duration(total.LongestNS).Seconds()}, Buckets: buckets, Outages: outages, Metadata: connectivityViewMetadata{Source: read.Source, GeneratedAt: time.Now().UTC(), ObservedThrough: read.ObservedThrough, StatisticsBasis: "online/(online+offline); unknown separate; UTC buckets; summary and bounded raw edges in one snapshot"}}, nil
}
