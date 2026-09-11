package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const latencyResponseMaxBytes = 2 << 20

type latencyChartInput struct {
	TargetIDs []int64 `json:"target_ids,omitempty"`
	ServerID  int64   `json:"server_id"`
	Window    string  `json:"window,omitempty"`
	MaxPoints int     `json:"max_points,omitempty"`
}

type latencyChartMetadata struct {
	RequestedFrom     time.Time            `json:"requested_from"`
	RequestedTo       time.Time            `json:"requested_to"`
	EffectiveFrom     time.Time            `json:"effective_from"`
	EffectiveTo       time.Time            `json:"effective_to"`
	ResolutionSeconds int64                `json:"resolution_seconds"`
	GeneratedAt       time.Time            `json:"generated_at"`
	ObservedThrough   *time.Time           `json:"observed_through"`
	AggregationState  string               `json:"aggregation_state"`
	Coverage          latencyChartCoverage `json:"coverage"`
	StatisticsBasis   string               `json:"statistics_basis"`
	Stale             bool                 `json:"stale"`
}
type latencyChartCoverage struct {
	TargetIDs                 []int64 `json:"target_ids,omitempty"`
	LegacyMeasurementRevision bool    `json:"legacy_measurement_revision"`
	MeasurementRevisionCount  int     `json:"measurement_revision_count"`
	Source                    string  `json:"source"`
	RetentionClipped          bool    `json:"retention_clipped"`
	HasSamples                bool    `json:"has_samples"`
	LegacyCurveReports        bool    `json:"legacy_curve_reports"`
}
type latencyChartResponse struct {
	ServerID              int64                              `json:"server_id"`
	RetentionDays         int                                `json:"retention_days"`
	Window                connectivityWindow                 `json:"window"`
	LatencyPoints         []connectivityLatencyPoint         `json:"latency_points"`
	FailedProbePoints     []store.LatencyFailurePoint        `json:"failed_probe_points"`
	RegionalLatencyPoints []model.ServerRegionalLatencyPoint `json:"regional_latency_points"`
	ProbeTargetStats      []model.LatencyProbeTargetStat     `json:"probe_target_stats"`
	RegionalDataStartAt   *time.Time                         `json:"regional_data_start_at"`
	Metadata              latencyChartMetadata               `json:"metadata"`
}

func (s *Server) historyReads() *coalesceCache[string, json.RawMessage] {
	s.latencyHistoryOnce.Do(func() {
		c := newCoalesceCache[string, json.RawMessage](30*time.Second, 128, 1)
		c.maxStale = 150 * time.Second
		c.budget = &coalesceBudget[json.RawMessage]{maxBytes: 16 << 20, maxEntryBytes: latencyResponseMaxBytes, maxInflight: 9, timeout: 5 * time.Second, retryDelay: 5 * time.Second, size: func(v json.RawMessage) int { return len(v) }}
		c.retain = func(key string) bool { return !strings.HasPrefix(key, "full:") }
		if os.Getenv("OBOARD_LATENCY_HISTORY_CACHE") == "0" {
			c.ttl = 0
			c.maxStale = 0
		}
		s.latencyHistoryCache = c
	})
	return s.latencyHistoryCache
}

func (s *Server) readLatencyChart(ctx context.Context, p application.Principal, input latencyChartInput) (latencyChartResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var response latencyChartResponse
	if input.ServerID <= 0 || !p.AllowsInt64("server_ids", input.ServerID) {
		return response, errors.New("resource_denied: server access denied")
	}
	if len(input.TargetIDs) > 64 {
		return response, errors.New("invalid_history_input: at most 64 target IDs")
	}
	input.TargetIDs = append([]int64(nil), input.TargetIDs...)
	sort.Slice(input.TargetIDs, func(i, j int) bool { return input.TargetIDs[i] < input.TargetIDs[j] })
	for i, id := range input.TargetIDs {
		if id <= 0 || (i > 0 && id == input.TargetIDs[i-1]) {
			return response, errors.New("invalid_history_input: invalid target IDs")
		}
	}
	now := time.Now().UTC()
	if s.latencyHistoryNow != nil {
		now = s.latencyHistoryNow()
	}
	requested, err := parseConnectivityWindow(input.Window, now)
	if err != nil {
		return response, err
	}
	if input.MaxPoints == 0 {
		input.MaxPoints = 360
	}
	if input.MaxPoints < 1 || input.MaxPoints > 360 {
		return response, errors.New("max_points must be between 1 and 360")
	}
	server, err := s.store.GetServer(ctx, input.ServerID)
	if err != nil {
		return response, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return response, err
	}
	days := store.ServerMonitoringRetentionDays(settings)
	tasks, err := s.store.ListLatencyProbeTasksForServer(ctx, input.ServerID)
	if err != nil {
		return response, err
	}
	effective, _ := parseConnectivityWindow(requested.Key, requested.To.Truncate(30*time.Second))
	if retained := effective.To.Add(-time.Duration(days) * 24 * time.Hour); retained.After(effective.From) {
		effective.From = retained
		effective.Duration = effective.To.Sub(retained)
	}
	maxLabelBytes := 0
	for _, task := range tasks {
		maxLabelBytes = max(maxLabelBytes, len(task.Name)+len(task.Province)+len(task.Carrier))
	}
	pointBudget := min(12000, (latencyResponseMaxBytes-128*1024)/(384+6*maxLabelBytes))
	points := min(input.MaxPoints, max(1, pointBudget/(len(tasks)+2)))
	summaryRead := os.Getenv("OBOARD_LATENCY_ROLLUP_READ") == "1"
	interval := time.Duration(math.Ceil(effective.Duration.Seconds()/float64(points))) * time.Second
	if summaryRead || len(input.TargetIDs) > 0 {
		base := time.Minute
		if effective.Duration > time.Hour {
			base = 5 * time.Minute
		}
		if effective.Duration > 24*time.Hour {
			base = time.Hour
		}
		interval = time.Duration(math.Ceil(float64(interval)/float64(base))) * base
		for int64(math.Ceil(float64(effective.To.UnixNano())/float64(interval)))-effective.From.UnixNano()/int64(interval) > int64(points) {
			interval += base
		}
	}
	if interval < time.Minute {
		interval = time.Minute
	}
	effective.BucketSeconds = int64(interval / time.Second)
	effective.BucketDuration = interval
	// Names are presentation metadata; the key includes only measurement semantics.
	semantics := make([]string, 0, len(tasks))
	for _, task := range tasks {
		semantics = append(semantics, fmt.Sprintf("%d/%s/%s/%d/%s/%s/%t/%d", task.ID, task.Method, task.Address, task.Port, task.Province, task.Carrier, task.Enabled, task.IntervalSeconds))
	}
	sort.Strings(semantics)
	revision := s.store.LatencyHistoryRevision(input.ServerID, effective.To)
	scopeJSON, _ := json.Marshal(p)
	keyJSON, _ := json.Marshal([]any{input.ServerID, days, effective.From, effective.To, effective.BucketSeconds, revision, server.LatencyProbeMode, server.LatencyProbePublicTarget, server.LatencyProbeEnabled, server.LatencyProbeSampleCount, server.LatencyProbeIntervalSeconds, server.LatencyProbeMaxTargets, server.IPStack, semantics, sha256.Sum256(scopeJSON), "report-v1", summaryRead, input.TargetIDs})
	key := fmt.Sprintf("chart:%x", sha256.Sum256(keyJSON))
	entry, err := s.historyReads().getOrBuild(ctx, key, revision, func(buildCtx context.Context) (json.RawMessage, time.Time, error) {
		var result, legacy store.LatencyBucketResult
		coverage := store.LatencyReadCoverage{Source: "raw"}
		var err error
		if summaryRead || len(input.TargetIDs) > 0 {
			result, coverage, err = s.store.QueryLatencySummaryChart(buildCtx, input.ServerID, effective.From, effective.To, interval, store.LatencySummaryReadOptions{TargetIDs: input.TargetIDs, ForceRaw: !summaryRead})
			legacy = coverage.Legacy
		} else {
			result, err = s.store.QueryLatencyChartBuckets(buildCtx, input.ServerID, effective.From, effective.To, interval)
			if err == nil {
				legacy, err = s.store.QueryLegacyLatencyBuckets(buildCtx, input.ServerID, effective.From, effective.To, interval)
			}
		}
		if err != nil {
			return nil, time.Time{}, err
		}
		built := latencyChartResponse{ServerID: input.ServerID, RetentionDays: days, Window: effective, LatencyPoints: make([]connectivityLatencyPoint, 0), FailedProbePoints: make([]store.LatencyFailurePoint, 0), RegionalLatencyPoints: result.Points, ProbeTargetStats: result.Stats, RegionalDataStartAt: result.DataStart}
		byTime := map[time.Time]connectivityLatencyPoint{}
		for _, point := range append(result.PublicPoints, legacy.PublicPoints...) {
			existing, ok := byTime[point.CheckedAt]
			if !ok {
				existing = connectivityLatencyPoint{At: point.CheckedAt, MinimumMS: int(point.MinLatencyMS), MaximumMS: int(point.MaxLatencyMS)}
			}
			existing.AverageMS = (existing.AverageMS*float64(existing.Count) + point.LatencyMS*float64(point.Count)) / float64(existing.Count+int(point.Count))
			existing.Count += int(point.Count)
			existing.MinimumMS = min(existing.MinimumMS, int(point.MinLatencyMS))
			existing.MaximumMS = max(existing.MaximumMS, int(point.MaxLatencyMS))
			byTime[point.CheckedAt] = existing
		}
		for _, point := range byTime {
			built.LatencyPoints = append(built.LatencyPoints, point)
		}
		sort.Slice(built.LatencyPoints, func(i, j int) bool { return built.LatencyPoints[i].At.Before(built.LatencyPoints[j].At) })
		failures := map[time.Time]int64{}
		for _, point := range append(result.Failures, legacy.Failures...) {
			failures[point.At] += point.Count
		}
		for at, count := range failures {
			built.FailedProbePoints = append(built.FailedProbePoints, store.LatencyFailurePoint{At: at, Count: count})
		}
		sort.Slice(built.FailedProbePoints, func(i, j int) bool { return built.FailedProbePoints[i].At.Before(built.FailedProbePoints[j].At) })
		if len(built.LatencyPoints)+len(built.FailedProbePoints)+len(built.RegionalLatencyPoints) > 12000 {
			return nil, time.Time{}, errHistoryTooLarge
		}
		observed := result.ObservedThrough
		if legacy.ObservedThrough.After(observed) {
			observed = legacy.ObservedThrough
		}
		generated := time.Now().UTC()
		built.Metadata = latencyChartMetadata{EffectiveFrom: effective.From, EffectiveTo: effective.To, ResolutionSeconds: effective.BucketSeconds, GeneratedAt: generated, AggregationState: "ready", Coverage: latencyChartCoverage{TargetIDs: input.TargetIDs, Source: coverage.Source, LegacyMeasurementRevision: coverage.LegacyRevision, MeasurementRevisionCount: coverage.RevisionCount, RetentionClipped: effective.Duration < requested.Duration, HasSamples: !observed.IsZero(), LegacyCurveReports: len(legacy.PublicPoints)+len(legacy.Failures) > 0}, StatisticsBasis: "report means weighted by report count; loss uses attempts/successes, falling back to report availability; target statistics use latency reports; legacy connectivity events supplement public curves only; P95 is a percentile of displayed bucket means"}
		if coverage.CatchingUp {
			built.Metadata.AggregationState = "catching_up"
		}
		if !observed.IsZero() {
			built.Metadata.ObservedThrough = &observed
		}
		if s.store.LatencyHistoryRevision(input.ServerID, time.Time{}) != revision {
			return nil, time.Time{}, errors.New("history_changed: retry query")
		}
		encoded, err := json.Marshal(built)
		return encoded, generated, err
	})
	if err != nil {
		return response, err
	}
	if s.store.LatencyHistoryRevision(input.ServerID, time.Time{}) != revision {
		return response, errors.New("history_changed: retry query")
	}
	if err = json.Unmarshal(entry.value, &response); err != nil {
		return response, err
	}
	response.Metadata.RequestedFrom = requested.From
	response.Metadata.RequestedTo = requested.To
	response.Metadata.Stale = time.Now().After(entry.expiresAt)
	names := make(map[int64]string, len(tasks))
	for _, task := range tasks {
		names[task.ID] = task.Name
	}
	for i := range response.ProbeTargetStats {
		if name, ok := names[response.ProbeTargetStats[i].TaskID]; ok {
			response.ProbeTargetStats[i].TaskName = name
		}
	}
	for i := range response.RegionalLatencyPoints {
		if name, ok := names[response.RegionalLatencyPoints[i].TaskID]; ok {
			response.RegionalLatencyPoints[i].TaskName = name
		}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return response, err
	}
	if len(encoded) > latencyResponseMaxBytes-4096 {
		return response, errHistoryTooLarge
	}
	return response, nil
}

func latencyChartRequest(r *http.Request, serverID int64) (latencyChartInput, error) {
	input := latencyChartInput{ServerID: serverID, Window: r.URL.Query().Get("window")}
	for key := range r.URL.Query() {
		if key != "view" && key != "window" && key != "max_points" && key != "target_ids" {
			return input, fmt.Errorf("unsupported chart parameter: %s", key)
		}
	}
	if raw, ok := r.URL.Query()["target_ids"]; ok {
		if len(raw) != 1 {
			return input, errors.New("invalid target filter")
		}
		for _, value := range strings.Split(raw[0], ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || id <= 0 {
				return input, errors.New("invalid target filter")
			}
			input.TargetIDs = append(input.TargetIDs, id)
		}
	}
	if raw := r.URL.Query().Get("max_points"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return input, errors.New("invalid max_points")
		}
		input.MaxPoints = value
		if value < 1 || value > 360 {
			return input, errors.New("max_points must be between 1 and 360")
		}
	}
	return input, nil
}

func historyErrorStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrHistoryCoverage), errors.Is(err, context.DeadlineExceeded), errors.Is(err, errHistoryBusy), strings.HasPrefix(err.Error(), "history_changed:"):
		return http.StatusServiceUnavailable
	case errors.Is(err, errHistoryTooLarge), errors.Is(err, store.ErrLatencyPointBudget), errors.Is(err, store.ErrConnectivityEventBudget):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, sql.ErrNoRows):
		return http.StatusNotFound
	case strings.HasPrefix(err.Error(), "resource_denied:"):
		return http.StatusForbidden
	case strings.HasPrefix(err.Error(), "invalid_history_input:"), strings.Contains(err.Error(), "window must be"), strings.Contains(err.Error(), "max_points"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// History deadlines are service budgets, not evidence that a user cancelled.
func writeHistoryReadError(w http.ResponseWriter, r *http.Request, err error, machine bool) {
	code, message := "", ""
	switch {
	case errors.Is(err, store.ErrHistoryCoverage):
		code, message = "history_catching_up", "历史汇总尚未覆盖此范围，请缩短时间范围或稍后刷新"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "history_timeout", "历史查询超时，主控可能正忙；请稍后重试或缩短时间范围"
	case errors.Is(err, context.Canceled):
		code, message = "history_canceled", "历史读取已中断，请稍后重试"
	case errors.Is(err, errHistoryBusy):
		code, message = "history_busy", "历史查询繁忙，请稍后重试"
	case strings.HasPrefix(err.Error(), "history_changed:"):
		code, message = "history_changed", "历史数据正在更新，请稍后重试"
	}
	if code != "" {
		w.Header().Set("Retry-After", "5")
		if code == "history_catching_up" {
			w.Header().Set("Retry-After", "30")
		}
		w.Header().Set("Cache-Control", "no-store")
		if machine {
			v2Error(w, r, http.StatusServiceUnavailable, code, message)
		} else {
			write(w, http.StatusServiceUnavailable, map[string]any{"code": code, "error": message})
		}
		return
	}
	if machine {
		v2Error(w, r, historyErrorStatus(err), "history_read_failed", err.Error())
	} else {
		fail(w, err, historyErrorStatus(err))
	}
}
