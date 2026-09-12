package controller

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type slaPartialAccumulator struct {
	Version    int                        `json:"version"`
	At         time.Time                  `json:"at"`
	Checkpoint json.RawMessage            `json:"checkpoint"`
	Stats      model.ConnectivitySLAStats `json:"stats"`
	Active     *connectivityOutage        `json:"active"`
	Outages    []connectivityOutage       `json:"outages"`
}

func buildSLAPartial(work store.SLAProjectionWork, state connectivityState, output store.SLAProjectionOutput) (store.SLAProjectionOutput, error) {
	from, to := time.Unix(work.From, 0).UTC(), time.Unix(work.To, 0).UTC()
	part := slaPartialAccumulator{Version: 1, At: from, Stats: model.ConnectivitySLAStats{StartsDown: state.availability == connectivityUnavailable}}
	if part.Stats.StartsDown {
		part.Active = &connectivityOutage{StartedAt: from, Cause: state.cause, StartedBeforeWindow: true}
	}
	if len(work.Partial) > 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(work.Partial, &fields); err != nil {
			return output, err
		}
		for _, key := range []string{"version", "at", "checkpoint", "stats", "active", "outages"} {
			if _, ok := fields[key]; !ok {
				return output, errors.New("incomplete SLA partial accumulator")
			}
		}

		if err := json.Unmarshal(work.Partial, &part); err != nil {
			return output, err
		}
		stats := part.Stats
		if part.Version != 1 || part.At.Before(from) || !part.At.Before(to) || stats.DurationNS != int64(part.At.Sub(from)) || stats.OnlineNS < 0 || stats.OfflineNS < 0 || stats.UnknownNS < 0 || stats.OutageCount < len(part.Outages) || stats.LongestNS < 0 || stats.LongestNS > stats.DurationNS || stats.DurationNS != stats.OnlineNS+stats.OfflineNS+stats.UnknownNS || len(part.Outages) > 10 {
			return output, errors.New("invalid SLA partial accumulator")
		}
		var err error
		state, err = decodeSLACheckpoint(part.Checkpoint)
		if err != nil {
			return output, err
		}
		if (state.availability == connectivityUnavailable) != (part.Active != nil) {
			return output, errors.New("inconsistent SLA active outage")
		}
		if part.Active != nil && (part.Active.StartedAt.Before(from) || part.Active.StartedAt.After(part.At) || part.Active.EndedAt != nil) {
			return output, errors.New("invalid SLA active outage")
		}
	}
	integrate := func(at time.Time) {
		duration := int64(at.Sub(part.At))
		part.Stats.DurationNS += duration
		switch state.availability {
		case connectivityAvailable:
			part.Stats.OnlineNS += duration
		case connectivityUnavailable:
			part.Stats.OfflineNS += duration
		default:
			part.Stats.UnknownNS += duration
		}
		part.At = at
	}
	closeOutage := func(outage connectivityOutage, until time.Time) {
		duration := int64(until.Sub(outage.StartedAt))
		outage.DurationSeconds = time.Duration(duration).Seconds()
		part.Stats.OutageCount++
		part.Stats.LongestNS = max(part.Stats.LongestNS, duration)
		if outage.StartedBeforeWindow {
			part.Stats.PrefixNS = duration
		}
		part.Outages = append(part.Outages, outage)
		if len(part.Outages) > 10 {
			part.Outages = part.Outages[len(part.Outages)-10:]
		}
	}
	for _, event := range work.Events {
		at := event.EffectiveAt.UTC()
		if at.Before(part.At) || !at.Before(to) {
			return output, errors.New("SLA partial events out of order")
		}
		integrate(at)
		before := state.availability
		applyConnectivityEvent(&state, event)
		if closed := advanceConnectivityOutage(&part.Active, before, state, at); closed != nil {
			closeOutage(*closed, at)
		}
	}
	part.Checkpoint = encodeSLACheckpoint(state)
	if work.PartialMore {
		var err error
		output.Partial, err = json.Marshal(part)
		return output, err
	}
	integrate(to)
	if part.Active != nil {
		closeOutage(*part.Active, to)
		part.Stats.SuffixNS = int64(to.Sub(part.Active.StartedAt))
	}
	part.Stats.EndsDown = state.availability == connectivityUnavailable
	part.Stats.WholeDown = part.Stats.StartsDown && part.Stats.EndsDown && part.Stats.OutageCount == 1 && part.Stats.OfflineNS == part.Stats.DurationNS
	output.Buckets = []store.SLAProjectionBucket{{Start: work.From, Stats: part.Stats, Checkpoint: part.Checkpoint, Outages: part.Outages}}
	return output, nil
}
