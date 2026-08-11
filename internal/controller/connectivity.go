package controller

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type connectivityWindow struct {
	Key            string        `json:"key"`
	Duration       time.Duration `json:"-"`
	BucketDuration time.Duration `json:"-"`
	From           time.Time     `json:"from"`
	To             time.Time     `json:"to"`
	BucketSeconds  int64         `json:"bucket_seconds"`
}

type connectivitySummary struct {
	SLAPercent          *float64 `json:"sla_percent"`
	AvailableSeconds    float64  `json:"available_seconds"`
	UnavailableSeconds  float64  `json:"unavailable_seconds"`
	UnknownSeconds      float64  `json:"unknown_seconds"`
	ObservedSeconds     float64  `json:"observed_seconds"`
	CoveragePercent     float64  `json:"coverage_percent"`
	OutageCount         int      `json:"outage_count"`
	LongestOutageSecond float64  `json:"longest_outage_seconds"`
}

type connectivityProbeCounts struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Failed    int `json:"failed"`
}

type connectivityLatency struct {
	AverageMS            *float64 `json:"avg_ms"`
	MinimumMS            *int     `json:"min_ms"`
	MaximumMS            *int     `json:"max_ms"`
	P95MS                *int     `json:"p95_ms"`
	SuccessfulProbeCount int      `json:"successful_probe_count"`
}

type connectivityCurrent struct {
	Status    string     `json:"status"`
	LatencyMS int        `json:"latency_ms"`
	CheckedAt *time.Time `json:"checked_at"`
	Error     string     `json:"error"`
}

type connectivityBucket struct {
	StartAt            time.Time `json:"start_at"`
	EndAt              time.Time `json:"end_at"`
	SLAPercent         *float64  `json:"sla_percent"`
	AvailableSeconds   float64   `json:"available_seconds"`
	UnavailableSeconds float64   `json:"unavailable_seconds"`
	UnknownSeconds     float64   `json:"unknown_seconds"`
	AverageLatencyMS   *float64  `json:"avg_latency_ms"`
}

type connectivityLatencyPoint struct {
	At        time.Time `json:"at"`
	AverageMS float64   `json:"avg_ms"`
	MinimumMS int       `json:"min_ms"`
	MaximumMS int       `json:"max_ms"`
	Count     int       `json:"count"`
}

type connectivityOutage struct {
	StartedAt           time.Time  `json:"started_at"`
	EndedAt             *time.Time `json:"ended_at"`
	DurationSeconds     float64    `json:"duration_seconds"`
	Cause               string     `json:"cause"`
	StartedBeforeWindow bool       `json:"started_before_window"`
}

type connectivityResponse struct {
	ServerID      int64                      `json:"server_id"`
	Window        connectivityWindow         `json:"window"`
	Summary       connectivitySummary        `json:"summary"`
	Probes        connectivityProbeCounts    `json:"probes"`
	Latency       connectivityLatency        `json:"latency"`
	Current       connectivityCurrent        `json:"current"`
	Buckets       []connectivityBucket       `json:"buckets"`
	LatencyPoints []connectivityLatencyPoint `json:"latency_points"`
	Outages       []connectivityOutage       `json:"outages"`
	DataStartAt   *time.Time                 `json:"data_start_at"`
}

type connectivityAvailability uint8

const (
	connectivityUnknown connectivityAvailability = iota
	connectivityAvailable
	connectivityUnavailable
)

type connectivityState struct {
	probeEnabled      bool
	probeEnabledKnown bool
	availability      connectivityAvailability
	cause             string
	lastProbeAt       *time.Time
	lastProbeLatency  int
	lastProbeError    string
}

type connectivitySegment struct {
	start, end   time.Time
	availability connectivityAvailability
}

func parseConnectivityWindow(key string, now time.Time) (connectivityWindow, error) {
	if key == "" {
		key = "24h"
	}
	window := connectivityWindow{Key: key, To: now.UTC()}
	switch key {
	case "24h":
		window.Duration = 24 * time.Hour
		window.BucketDuration = time.Hour
	case "7d":
		window.Duration = 7 * 24 * time.Hour
		window.BucketDuration = 6 * time.Hour
	case "30d":
		window.Duration = 30 * 24 * time.Hour
		window.BucketDuration = 24 * time.Hour
	default:
		return connectivityWindow{}, errors.New("window must be one of 24h, 7d, or 30d")
	}
	window.From = window.To.Add(-window.Duration)
	window.BucketSeconds = int64(window.BucketDuration / time.Second)
	return window, nil
}

func applyConnectivityEvent(state *connectivityState, event model.ServerConnectivityEvent) {
	switch event.Kind {
	case model.ConnectivityEventProbeEnabled:
		state.probeEnabledKnown = true
		state.probeEnabled = true
		state.availability = connectivityUnknown
		state.cause = ""
		state.lastProbeAt = nil
		state.lastProbeLatency = 0
		state.lastProbeError = ""
	case model.ConnectivityEventProbeDisabled:
		state.probeEnabledKnown = true
		state.probeEnabled = false
		state.availability = connectivityUnknown
		state.cause = ""
		state.lastProbeAt = nil
		state.lastProbeLatency = 0
		state.lastProbeError = ""
	case model.ConnectivityEventProbeTargetChanged:
		state.probeEnabledKnown = true
		state.probeEnabled = true
		if state.cause != "server_offline" {
			state.availability = connectivityUnknown
			state.cause = ""
		}
		state.lastProbeAt = nil
		state.lastProbeLatency = 0
		state.lastProbeError = ""
	case model.ConnectivityEventProbeResult:
		state.probeEnabledKnown = true
		state.probeEnabled = true
		checkedAt := event.EffectiveAt.UTC()
		state.lastProbeAt = &checkedAt
		state.lastProbeLatency = event.LatencyMS
		state.lastProbeError = event.Error
		if event.Available != nil && *event.Available {
			state.availability = connectivityAvailable
			state.cause = "probe_success"
		} else {
			state.availability = connectivityUnavailable
			state.cause = "probe_failed"
		}
	case model.ConnectivityEventServerOffline:
		if state.probeEnabledKnown && state.probeEnabled {
			state.availability = connectivityUnavailable
			state.cause = "server_offline"
		}
	}
}

func connectivityPercent(available, unavailable time.Duration) *float64 {
	observed := available + unavailable
	if observed <= 0 {
		return nil
	}
	value := float64(available) / float64(observed) * 100
	return &value
}

func addConnectivityDuration(availability connectivityAvailability, duration time.Duration, available, unavailable, unknown *time.Duration) {
	if duration <= 0 {
		return
	}
	switch availability {
	case connectivityAvailable:
		*available += duration
	case connectivityUnavailable:
		*unavailable += duration
	default:
		*unknown += duration
	}
}

func buildConnectivitySegments(from, to time.Time, baseline, events []model.ServerConnectivityEvent) ([]connectivitySegment, connectivityState) {
	state := connectivityState{availability: connectivityUnknown}
	for _, event := range baseline {
		applyConnectivityEvent(&state, event)
	}
	cursor := from
	segments := make([]connectivitySegment, 0, len(events)+1)
	for _, event := range events {
		at := event.EffectiveAt.UTC()
		if at.Before(from) {
			applyConnectivityEvent(&state, event)
			continue
		}
		if !at.Before(to) {
			break
		}
		if at.After(cursor) {
			segments = append(segments, connectivitySegment{start: cursor, end: at, availability: state.availability})
		}
		applyConnectivityEvent(&state, event)
		cursor = at
	}
	if cursor.Before(to) {
		segments = append(segments, connectivitySegment{start: cursor, end: to, availability: state.availability})
	}
	return segments, state
}

func connectivityDurations(segments []connectivitySegment, from, to time.Time) (time.Duration, time.Duration, time.Duration) {
	var available, unavailable, unknown time.Duration
	for _, segment := range segments {
		start, end := segment.start, segment.end
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		addConnectivityDuration(segment.availability, end.Sub(start), &available, &unavailable, &unknown)
	}
	return available, unavailable, unknown
}

func mergeOutageCause(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" || current == next {
		return current
	}
	return "mixed"
}

func buildConnectivityOutages(from, to time.Time, baseline, events []model.ServerConnectivityEvent) []connectivityOutage {
	state := connectivityState{availability: connectivityUnknown}
	for _, event := range baseline {
		applyConnectivityEvent(&state, event)
	}
	var outages []connectivityOutage
	var active *connectivityOutage
	if state.availability == connectivityUnavailable {
		active = &connectivityOutage{StartedAt: from, Cause: state.cause, StartedBeforeWindow: true}
	}
	for _, event := range events {
		at := event.EffectiveAt.UTC()
		if at.Before(from) || !at.Before(to) {
			continue
		}
		before := state.availability
		applyConnectivityEvent(&state, event)
		after := state.availability
		if before != connectivityUnavailable && after == connectivityUnavailable {
			active = &connectivityOutage{StartedAt: at, Cause: state.cause}
		} else if before == connectivityUnavailable && after == connectivityUnavailable && active != nil {
			active.Cause = mergeOutageCause(active.Cause, state.cause)
		} else if before == connectivityUnavailable && after != connectivityUnavailable && active != nil {
			endedAt := at
			active.EndedAt = &endedAt
			active.DurationSeconds = endedAt.Sub(active.StartedAt).Seconds()
			outages = append(outages, *active)
			active = nil
		}
	}
	if active != nil {
		active.DurationSeconds = to.Sub(active.StartedAt).Seconds()
		outages = append(outages, *active)
	}
	return outages
}

func successfulConnectivityProbes(events []model.ServerConnectivityEvent) []model.ServerConnectivityEvent {
	probes := make([]model.ServerConnectivityEvent, 0)
	for _, event := range events {
		if event.Kind == model.ConnectivityEventProbeResult && event.Available != nil && *event.Available && event.LatencyMS > 0 {
			probes = append(probes, event)
		}
	}
	return probes
}

func connectivityLatencyStats(probes []model.ServerConnectivityEvent) connectivityLatency {
	stats := connectivityLatency{SuccessfulProbeCount: len(probes)}
	if len(probes) == 0 {
		return stats
	}
	values := make([]int, 0, len(probes))
	var total int64
	for _, probe := range probes {
		values = append(values, probe.LatencyMS)
		total += int64(probe.LatencyMS)
	}
	sort.Ints(values)
	average := float64(total) / float64(len(values))
	minimum, maximum := values[0], values[len(values)-1]
	index := int(math.Ceil(0.95*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	p95 := values[index]
	stats.AverageMS = &average
	stats.MinimumMS = &minimum
	stats.MaximumMS = &maximum
	stats.P95MS = &p95
	return stats
}

func buildConnectivityLatencyPoints(from time.Time, duration time.Duration, probes []model.ServerConnectivityEvent) []connectivityLatencyPoint {
	minutes := int64(math.Ceil(duration.Minutes() / 360))
	if minutes < 1 {
		minutes = 1
	}
	interval := time.Duration(minutes) * time.Minute
	type group struct {
		sum             int64
		min, max, count int
	}
	groups := map[int]*group{}
	for _, probe := range probes {
		index := int(probe.EffectiveAt.Sub(from) / interval)
		if index < 0 {
			continue
		}
		item := groups[index]
		if item == nil {
			item = &group{min: probe.LatencyMS, max: probe.LatencyMS}
			groups[index] = item
		}
		item.sum += int64(probe.LatencyMS)
		item.count++
		if probe.LatencyMS < item.min {
			item.min = probe.LatencyMS
		}
		if probe.LatencyMS > item.max {
			item.max = probe.LatencyMS
		}
	}
	indexes := make([]int, 0, len(groups))
	for index := range groups {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	points := make([]connectivityLatencyPoint, 0, len(indexes))
	for _, index := range indexes {
		item := groups[index]
		points = append(points, connectivityLatencyPoint{At: from.Add(time.Duration(index) * interval), AverageMS: float64(item.sum) / float64(item.count), MinimumMS: item.min, MaximumMS: item.max, Count: item.count})
	}
	return points
}

func buildConnectivityBuckets(window connectivityWindow, segments []connectivitySegment, probes []model.ServerConnectivityEvent) []connectivityBucket {
	count := int(window.Duration / window.BucketDuration)
	buckets := make([]connectivityBucket, count)
	type latencyTotal struct {
		sum   int64
		count int
	}
	latencies := make([]latencyTotal, count)
	for _, probe := range probes {
		index := int(probe.EffectiveAt.Sub(window.From) / window.BucketDuration)
		if index >= 0 && index < count {
			latencies[index].sum += int64(probe.LatencyMS)
			latencies[index].count++
		}
	}
	for index := 0; index < count; index++ {
		start := window.From.Add(time.Duration(index) * window.BucketDuration)
		end := start.Add(window.BucketDuration)
		available, unavailable, unknown := connectivityDurations(segments, start, end)
		bucket := connectivityBucket{StartAt: start, EndAt: end, SLAPercent: connectivityPercent(available, unavailable), AvailableSeconds: available.Seconds(), UnavailableSeconds: unavailable.Seconds(), UnknownSeconds: unknown.Seconds()}
		if latencies[index].count > 0 {
			average := float64(latencies[index].sum) / float64(latencies[index].count)
			bucket.AverageLatencyMS = &average
		}
		buckets[index] = bucket
	}
	return buckets
}

func BuildConnectivityResponse(serverID int64, window connectivityWindow, history model.ServerConnectivityHistory) connectivityResponse {
	segments, currentState := buildConnectivitySegments(window.From, window.To, history.Baseline, history.Events)
	available, unavailable, unknown := connectivityDurations(segments, window.From, window.To)
	observed := available + unavailable
	coverage := 0.0
	if window.Duration > 0 {
		coverage = math.Min(100, math.Max(0, float64(observed)/float64(window.Duration)*100))
	}
	outages := buildConnectivityOutages(window.From, window.To, history.Baseline, history.Events)
	if outages == nil {
		outages = make([]connectivityOutage, 0)
	}
	longest := 0.0
	for _, outage := range outages {
		longest = math.Max(longest, outage.DurationSeconds)
	}
	probes := connectivityProbeCounts{}
	for _, event := range history.Events {
		if event.Kind != model.ConnectivityEventProbeResult {
			continue
		}
		probes.Total++
		if event.Available != nil && *event.Available {
			probes.Available++
		} else {
			probes.Failed++
		}
	}
	successfulProbes := successfulConnectivityProbes(history.Events)
	current := connectivityCurrent{Status: "pending"}
	if currentState.probeEnabledKnown && !currentState.probeEnabled {
		current.Status = "disabled"
	} else {
		switch currentState.availability {
		case connectivityAvailable:
			current.Status = "available"
			current.LatencyMS = currentState.lastProbeLatency
		case connectivityUnavailable:
			if currentState.cause == "server_offline" {
				current.Status = "offline"
			} else {
				current.Status = "unavailable"
			}
		default:
			current.Status = "pending"
		}
	}
	current.CheckedAt = currentState.lastProbeAt
	current.Error = currentState.lastProbeError
	response := connectivityResponse{
		ServerID:      serverID,
		Window:        window,
		Summary:       connectivitySummary{SLAPercent: connectivityPercent(available, unavailable), AvailableSeconds: available.Seconds(), UnavailableSeconds: unavailable.Seconds(), UnknownSeconds: unknown.Seconds(), ObservedSeconds: observed.Seconds(), CoveragePercent: coverage, OutageCount: len(outages), LongestOutageSecond: longest},
		Probes:        probes,
		Latency:       connectivityLatencyStats(successfulProbes),
		Current:       current,
		Buckets:       buildConnectivityBuckets(window, segments, successfulProbes),
		LatencyPoints: buildConnectivityLatencyPoints(window.From, window.Duration, successfulProbes),
		Outages:       outages,
		DataStartAt:   history.DataStart,
	}
	if len(response.Outages) > 10 {
		response.Outages = response.Outages[len(response.Outages)-10:]
	}
	for left, right := 0, len(response.Outages)-1; left < right; left, right = left+1, right-1 {
		response.Outages[left], response.Outages[right] = response.Outages[right], response.Outages[left]
	}
	return response
}

func (s *Server) serverConnectivity(w http.ResponseWriter, r *http.Request, serverID int64) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	window, err := parseConnectivityWindow(r.URL.Query().Get("window"), time.Now().UTC())
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	s.checkOffline(r.Context())
	if _, err := s.store.GetServer(r.Context(), serverID); err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	history, err := s.store.ListConnectivityHistory(r.Context(), serverID, window.From, window.To)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, BuildConnectivityResponse(serverID, window, history))
}
