package controller

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type slaCheckpoint struct {
	Version                   int                      `json:"version"`
	ProbeEnabled              bool                     `json:"probe_enabled"`
	ProbeEnabledKnown         bool                     `json:"probe_enabled_known"`
	ControllerConnected       bool                     `json:"controller_connected"`
	ControllerConnectionKnown bool                     `json:"controller_connection_known"`
	ControllerUpdate          bool                     `json:"controller_update"`
	Availability              connectivityAvailability `json:"availability"`
	Cause                     string                   `json:"cause"`
	LastProbeAt               *time.Time               `json:"last_probe_at"`
	LastProbeLatency          int                      `json:"last_probe_latency"`
	LastProbeError            string                   `json:"last_probe_error"`
}

func encodeSLACheckpoint(state connectivityState) json.RawMessage {
	data, _ := json.Marshal(slaCheckpoint{Version: 1, ProbeEnabled: state.probeEnabled, ProbeEnabledKnown: state.probeEnabledKnown, ControllerConnected: state.controllerConnected, ControllerConnectionKnown: state.controllerConnectionKnown, ControllerUpdate: state.controllerUpdate, Availability: state.availability, Cause: state.cause, LastProbeAt: state.lastProbeAt, LastProbeLatency: state.lastProbeLatency, LastProbeError: state.lastProbeError})
	return data
}

func decodeSLACheckpoint(data json.RawMessage) (connectivityState, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return connectivityState{}, err
	}
	for _, key := range []string{"version", "probe_enabled", "probe_enabled_known", "controller_connected", "controller_connection_known", "controller_update", "availability", "cause", "last_probe_at", "last_probe_latency", "last_probe_error"} {
		if _, ok := fields[key]; !ok {
			return connectivityState{}, errors.New("incomplete SLA checkpoint")
		}
	}
	var value slaCheckpoint
	if err := json.Unmarshal(data, &value); err != nil {
		return connectivityState{}, err
	}
	if value.Version != 1 || value.Availability > connectivityUnavailable {
		return connectivityState{}, errors.New("unsupported SLA checkpoint")
	}
	return connectivityState{probeEnabled: value.ProbeEnabled, probeEnabledKnown: value.ProbeEnabledKnown, controllerConnected: value.ControllerConnected, controllerConnectionKnown: value.ControllerConnectionKnown, controllerUpdate: value.ControllerUpdate, availability: value.Availability, cause: value.Cause, lastProbeAt: value.LastProbeAt, lastProbeLatency: value.LastProbeLatency, lastProbeError: value.LastProbeError}, nil
}

func buildSLAProjection(work store.SLAProjectionWork) (store.SLAProjectionOutput, error) {
	state := connectivityState{availability: connectivityUnknown}
	if len(work.Checkpoint) > 0 {
		var err error
		state, err = decodeSLACheckpoint(work.Checkpoint)
		if err != nil {
			return store.SLAProjectionOutput{}, err
		}
	} else {
		for _, event := range work.Baseline {
			applyConnectivityEvent(&state, event)
		}
	}
	output := store.SLAProjectionOutput{Seed: encodeSLACheckpoint(state)}
	if work.CoverageChanged {
		coverage := connectivityState{availability: connectivityUnknown}
		if len(work.CoverageCheckpoint) > 0 {
			var err error
			coverage, err = decodeSLACheckpoint(work.CoverageCheckpoint)
			if err != nil {
				return store.SLAProjectionOutput{}, err
			}
		} else {
			for _, event := range work.CoverageBaseline {
				applyConnectivityEvent(&coverage, event)
			}
		}
		output.CoverageSeed = encodeSLACheckpoint(coverage)
	}

	index := 0
	for start := work.From; start < work.To; start += 300 {
		from, to := time.Unix(start, 0).UTC(), time.Unix(start+300, 0).UTC()
		end := index
		for end < len(work.Events) && work.Events[end].EffectiveAt.Before(to) {
			end++
		}
		events := work.Events[index:end]
		index = end
		initial := state
		segments, next := buildConnectivitySegmentsFromState(from, to, state, events)
		online, offline, unknown := connectivityDurations(segments, from, to)
		outages := buildConnectivityOutagesFromState(from, to, initial, events)
		stats := model.ConnectivitySLAStats{DurationNS: int64(to.Sub(from)), OnlineNS: int64(online), OfflineNS: int64(offline), UnknownNS: int64(unknown), OutageCount: len(outages), StartsDown: initial.availability == connectivityUnavailable, EndsDown: next.availability == connectivityUnavailable}
		for i, outage := range outages {
			until := to
			if outage.EndedAt != nil {
				until = *outage.EndedAt
			}
			duration := int64(until.Sub(outage.StartedAt))
			stats.LongestNS = max(stats.LongestNS, duration)
			if i == 0 && outage.StartedBeforeWindow {
				stats.PrefixNS = duration
			}
			if outage.EndedAt == nil {
				stats.SuffixNS = duration
			}
		}
		stats.WholeDown = stats.StartsDown && stats.EndsDown && stats.OutageCount == 1 && stats.OfflineNS == stats.DurationNS
		output.Buckets = append(output.Buckets, store.SLAProjectionBucket{Start: start, Stats: stats, Checkpoint: encodeSLACheckpoint(next)})
		state = next
	}
	return output, nil
}
