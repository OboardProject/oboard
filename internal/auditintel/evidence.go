package auditintel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// BuildEvidencePack assembles the versioned deterministic evidence pack for one
// user. Every feature, signal, counter-evidence and timeline item carries a
// field-level evidence ID so AI findings can cite the exact fact that supports
// a conclusion. All scores and the confidence are computed by the deterministic
// engine and are authoritative for downstream validation.
func (s *Service) BuildEvidencePack(ctx context.Context, subjectRef string, user model.User, windowStart, windowEnd time.Time, evidenceTypes []string, droppedBuckets int64) (*model.AuditEvidencePack, error) {
	at := s.now().UTC()
	policy := store.DefaultAuditPolicy()
	wantConnection := containsString(evidenceTypes, model.AuditReviewEvidenceConnection) || containsString(evidenceTypes, model.AuditReviewEvidenceDestination)
	wantSubscription := containsString(evidenceTypes, model.AuditReviewEvidenceSubscription)

	var conn *model.ConnectionAuditUserSummary
	if wantConnection {
		summary, err := s.store.ConnectionAuditUserRisk(ctx, user.ID, 24, policy, at)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		conn = summary
	}
	var sub model.SubscriptionAuditRisk
	subscriptionData := false
	if wantSubscription {
		risk, _, err := s.store.SubscriptionAuditCurrentRisk(ctx, user.ID, at, policy)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		sub = risk
		subscriptionData = risk.Short.PullCount > 0 || risk.Long.PullCount > 0
	}
	prior, _ := s.store.ListAuditFeatureSnapshots(ctx, user.ID, "15m", 28*96)
	baselineDays := distinctBaselineDays(prior)
	coverage, coverageComplete := 1.0, true
	if conn != nil {
		coverage, coverageComplete = conn.CoverageQuality, conn.CoverageComplete
	}
	identity := "unknown"
	if conn != nil && conn.IdentityMode != "" {
		identity = conn.IdentityMode
	} else if sub.IdentityMode != "" {
		identity = sub.IdentityMode
	}
	identityQuality := evidenceIdentityQuality(identity)

	pack := &model.AuditEvidencePack{
		SchemaVersion: model.AuditEvidenceSchemaVersion,
		Mode:          "single_user",
		Subject: model.AuditEvidenceSubject{
			Ref: subjectRef, IdentityMode: identity, PolicyProfile: policy.Mode,
			Status: user.Status, Role: string(user.Role),
		},
		Window: model.AuditEvidenceWindow{
			Current:     fmt.Sprintf("%s/%s", windowStart.UTC().Format(time.RFC3339), windowEnd.UTC().Format(time.RFC3339)),
			Comparisons: []string{"previous_period", "same_time_slot_28d"},
		},
		DataQuality: model.AuditEvidenceQuality{
			Coverage: coverage, BaselineDays: baselineDays, DroppedBuckets: 0,
			IdentityQuality: identityQuality, DataCompleteness: 1.0,
		},
		Methodology: model.AuditEvidenceMethodology{
			FeatureVersion: FeatureVersion, ScoringVersion: model.AuditScoringVersion,
			BaselineVersion: model.AuditBaselineVersion, EvidenceSchemaVersion: model.AuditEvidenceSchemaVersion,
			PromptVersion: model.AuditPromptFindingVersion, ReportSchemaVersion: model.AuditReportSchemaVersion,
			ProviderProfileVersion: model.AuditProviderProfileVersion,
		},
	}

	hourly := baselineHourlyStats(prior)
	connectionRisk, subscriptionRisk := 0, 0
	if conn != nil {
		connectionRisk = conn.RiskScore
		pack.DataQuality.DroppedBuckets = droppedBuckets
		pack.DataQuality.DataCompleteness = math.Min(1, 0.5+0.5*coverage)
		buildConnectionFeatures(pack, conn, policy, hourly, coverage)
		buildConnectionSignals(pack, conn, at, connectionRisk)
		buildConnectionTimeline(pack, conn)
	}
	if wantSubscription {
		subscriptionRisk = sub.Score
		buildSubscriptionFeatures(pack, sub, policy)
		buildSubscriptionSignals(pack, sub, at, subscriptionRisk)
	}
	if !coverageComplete {
		pack.CounterEvidence = append(pack.CounterEvidence, model.AuditEvidenceCounter{Ref: nextCounterRef(pack), Kind: "collection_gap", Text: "Agent 报告存在丢弃桶，自动限制已禁用，相关结论置信度受限", Scope: "engine:connection"})
	}
	if identity != "device_bound" {
		pack.CounterEvidence = append(pack.CounterEvidence, model.AuditEvidenceCounter{Ref: nextCounterRef(pack), Kind: "legacy_identity", Text: "存在旧凭证流量，设备数仅提供区间估计且不会单独触发限制", Scope: "engine:identity"})
	}
	if baselineDays < 7 && conn != nil {
		pack.DataGaps = append(pack.DataGaps, fmt.Sprintf("历史基线不足 7 天（%d 天），异常类结论置信度受限", baselineDays))
	}
	if wantConnection && conn == nil {
		pack.DataGaps = append(pack.DataGaps, "窗口内没有连接审计数据")
	}
	if wantSubscription && !subscriptionData {
		pack.DataGaps = append(pack.DataGaps, "窗口内没有订阅拉取审计数据")
	}

	overall := connectionRisk
	if subscriptionRisk > overall {
		overall = subscriptionRisk
	}
	baseConfidence := 0.0
	if conn != nil && conn.Confidence > baseConfidence {
		baseConfidence = conn.Confidence
	}
	if sub.Confidence > baseConfidence {
		baseConfidence = sub.Confidence
	}
	pack.Scores = model.AuditEvidenceScores{
		ConnectionRisk: connectionRisk, SubscriptionRisk: subscriptionRisk,
		OverallRisk: overall, Health: 100 - overall,
		EvidenceConfidence: math.Round(baseConfidence*100) / 100,
		Caps: model.AuditEvidenceCaps{
			Anomaly: 0.60, DeviceClone: 0.65, Normal: 0.55, HighRisk: 0.70,
		},
	}
	if baselineDays >= 7 {
		pack.Scores.Caps.Anomaly = 1
	}
	if identity == "device_bound" {
		pack.Scores.Caps.DeviceClone = 1
	}
	if coverage >= 0.8 {
		pack.Scores.Caps.Normal = 1
	}
	if len(evidenceCategories(pack)) >= 2 {
		pack.Scores.Caps.HighRisk = 1
	}
	return pack, nil
}

func buildConnectionFeatures(pack *model.AuditEvidencePack, conn *model.ConnectionAuditUserSummary, policy model.AuditPolicy, hourly hourlyBaseline, coverage float64) {
	addFeature := func(metric string, value float64, unit, window string, threshold *float64, category string, quality float64, baseline *baselineStatsValue) {
		feature := model.AuditEvidenceFeature{
			EvidenceID: nextEvidenceRef(pack), Metric: metric, Value: value, Unit: unit, Window: window,
			Threshold: threshold, SampleCount: int(conn.ReportCount), Quality: quality,
			Severity: featureSeverity(value, threshold, baseline), Source: "connection", Category: category,
		}
		if baseline != nil {
			feature.BaselineMedian, feature.BaselineP95, feature.BaselineMAD = baseline.median, baseline.p95, baseline.mad
			feature.SampleCount = baseline.samples
			if baseline.median != nil && *baseline.median > 0 {
				delta := (value - *baseline.median) / *baseline.median * 100
				feature.DeltaPercent = &delta
			}
		}
		pack.Features = append(pack.Features, feature)
	}
	threshold := func(value model.AuditThreshold) *float64 { v := float64(value.Hard); return &v }
	if conn.ReportCount > 0 {
		addFeature("active_peak", float64(conn.ActivePeak), "connections", "24h", threshold(policy.ActiveConnections), "resource_pressure", coverage, nil)
		addFeature("source_ip_count", float64(conn.SourceIPCount), "ips", "24h", nil, "source_usage", coverage, nil)
		addFeature("region_count", float64(conn.SourceRegionCount), "regions", "24h", nil, "source_usage", coverage, nil)
		addFeature("node_fanout", float64(conn.NodeFanout), "nodes", "10s", threshold(policy.NodeFanout10Seconds), "node_fanout", coverage, nil)
		if conn.ConcurrentRouteCount > 0 {
			addFeature("concurrent_route_count", float64(conn.ConcurrentRouteCount), "routes", "90s", threshold(policy.ConcurrentRoutes90Secs), "device_clone", coverage, nil)
		}
		if conn.RobustZ > 0 {
			z := 6.0
			addFeature("robust_z", conn.RobustZ, "z", "28d-same-slot", &z, "historical_anomaly", coverage, nil)
		}
		if hourly.samples >= 3 {
			addFeature("hourly_connection_volume", float64(conn.ConnectionCount)/24, "connections/h", "24h-avg", nil, "historical_anomaly", coverage, &hourly.connection)
		}
	}
}

type baselineStatsValue struct {
	median  *float64
	p95     *float64
	mad     *float64
	samples int
}

type hourlyBaseline struct {
	connection baselineStatsValue
	activePeak baselineStatsValue
	samples    int
}

func buildConnectionSignals(pack *model.AuditEvidencePack, conn *model.ConnectionAuditUserSummary, at time.Time, riskScore int) {
	severity := signalSeverity(riskScore)
	for _, text := range conn.RiskSignals {
		kind := signalKind(text, conn)
		refs := evidenceRefsForCategory(pack, kind)
		signal := model.AuditEvidenceSignal{
			SignalID: nextSignalRef(pack), Kind: kind, Severity: severity, Text: text,
			ObservedAt: at.UTC().Format(time.RFC3339), EvidenceRefs: refs, Confidence: conn.Confidence,
		}
		if conn.RiskWindowStartedAt != nil && kind == "device_clone" {
			signal.ObservedAt = conn.RiskWindowStartedAt.UTC().Format(time.RFC3339)
			signal.DurationSeconds = int(conn.RiskWindowEndedAt.Sub(*conn.RiskWindowStartedAt) / time.Second)
		}
		pack.Signals = append(pack.Signals, signal)
	}
	for _, text := range conn.CounterEvidence {
		pack.CounterEvidence = append(pack.CounterEvidence, model.AuditEvidenceCounter{Ref: nextCounterRef(pack), Kind: "engine", Text: text, Scope: "engine:connection"})
	}
}

func buildSubscriptionFeatures(pack *model.AuditEvidencePack, risk model.SubscriptionAuditRisk, policy model.AuditPolicy) {
	coverage := pack.DataQuality.Coverage
	threshold := func(value model.AuditThreshold) *float64 { v := float64(value.Hard); return &v }
	add := func(metric string, value float64, unit, window string, thr *float64, category string) {
		feature := model.AuditEvidenceFeature{
			EvidenceID: nextEvidenceRef(pack), Metric: metric, Value: value, Unit: unit, Window: window,
			Threshold: thr, SampleCount: risk.Long.PullCount, Quality: coverage,
			Severity: featureSeverity(value, thr, nil), Source: "subscription", Category: category,
		}
		pack.Features = append(pack.Features, feature)
	}
	if risk.Long.PullCount > 0 {
		add("logical_pulls_10m", risk.Short.LogicalPullWeight, "pulls", "10m", threshold(policy.LogicalPullsPer10Minutes), "logical_pull")
		add("logical_pulls_24h", risk.Long.LogicalPullWeight, "pulls", "24h", threshold(policy.LogicalPullsPer24Hours), "logical_pull")
		add("raw_requests_10m", float64(risk.Short.RawRequestCount), "requests", "10m", threshold(model.AuditThreshold{Soft: 0, Hard: policy.RawRequestsPer60Seconds.Hard * 10}), "raw_request")
		add("routes_15m", float64(risk.Short.RouteCount), "routes", "15m", threshold(policy.RoutesPer15Minutes), "network_route")
		add("client_families_24h", float64(risk.Long.ClientFamilyCount), "families", "24h", threshold(policy.ClientFamiliesPer24Hours), "client_family")
		add("source_ips_24h", float64(risk.Long.SourceIPCount), "ips", "24h", nil, "source_usage")
	}
}

func buildSubscriptionSignals(pack *model.AuditEvidencePack, risk model.SubscriptionAuditRisk, at time.Time, riskScore int) {
	severity := signalSeverity(riskScore)
	for _, text := range risk.Signals {
		kind := subscriptionSignalKind(text)
		pack.Signals = append(pack.Signals, model.AuditEvidenceSignal{
			SignalID: nextSignalRef(pack), Kind: kind, Severity: severity, Text: text,
			ObservedAt: at.UTC().Format(time.RFC3339), EvidenceRefs: evidenceRefsForCategory(pack, kind), Confidence: risk.Confidence,
		})
	}
	for _, text := range risk.CounterEvidence {
		pack.CounterEvidence = append(pack.CounterEvidence, model.AuditEvidenceCounter{Ref: nextCounterRef(pack), Kind: "engine", Text: text, Scope: "engine:subscription"})
	}
}

func buildConnectionTimeline(pack *model.AuditEvidencePack, conn *model.ConnectionAuditUserSummary) {
	if conn.RiskWindowStartedAt != nil {
		pack.Timeline = append(pack.Timeline, model.AuditEvidenceTimelineItem{
			EvidenceID: nextTimelineRef(pack), Kind: "device_clone",
			StartedAt: conn.RiskWindowStartedAt.UTC().Format(time.RFC3339),
			EndedAt:   conn.RiskWindowEndedAt.UTC().Format(time.RFC3339),
			Score:     conn.RiskScore,
			Detail:    fmt.Sprintf("同一设备凭证在 %d 条独立网络上重叠", conn.ConcurrentRouteCount),
		})
	}
	if conn.ProbeEpisodeCount > 0 {
		pack.Timeline = append(pack.Timeline, model.AuditEvidenceTimelineItem{
			EvidenceID: nextTimelineRef(pack), Kind: "speedtest", Score: 0,
			Detail: fmt.Sprintf("已确认 %d 次全节点测速并排除", conn.ProbeEpisodeCount),
		})
	}
	if len(pack.Timeline) > 12 {
		pack.Timeline = pack.Timeline[:12]
	}
}

func signalKind(text string, conn *model.ConnectionAuditUserSummary) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "独立网络"), strings.Contains(lower, "重叠"):
		return "device_clone"
	case strings.Contains(lower, "扇出"):
		return "node_fanout"
	case strings.Contains(lower, "基线"), strings.Contains(lower, "robust"):
		return "historical_anomaly"
	case strings.Contains(lower, "并发压力"), strings.Contains(lower, "活跃连接"):
		return "resource_pressure"
	case strings.Contains(lower, "设备"):
		return "device_identity"
	default:
		return "connection"
	}
}

func subscriptionSignalKind(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "拉取"):
		return "logical_pull"
	case strings.Contains(lower, "原始请求"):
		return "raw_request"
	case strings.Contains(lower, "路由"):
		return "network_route"
	case strings.Contains(lower, "客户端"):
		return "client_family"
	case strings.Contains(lower, "设备"):
		return "device_identity"
	default:
		return "subscription"
	}
}

func evidenceCategories(pack *model.AuditEvidencePack) map[string]bool {
	out := map[string]bool{}
	for _, feature := range pack.Features {
		if feature.Category != "" {
			out[feature.Category] = true
		}
	}
	for _, signal := range pack.Signals {
		if signal.Kind != "" {
			out[signal.Kind] = true
		}
	}
	return out
}

func evidenceRefsForCategory(pack *model.AuditEvidencePack, category string) []string {
	out := []string{}
	for _, feature := range pack.Features {
		if feature.Category == category {
			out = append(out, feature.EvidenceID)
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func featureSeverity(value float64, threshold *float64, baseline *baselineStatsValue) string {
	if baseline != nil && baseline.median != nil && *baseline.median > 0 {
		delta := (value - *baseline.median) / *baseline.median
		if delta >= 1 {
			return "high"
		}
		if delta >= 0.5 {
			return "medium"
		}
		return "low"
	}
	if threshold != nil && *threshold > 0 {
		switch {
		case value >= *threshold*2:
			return "high"
		case value >= *threshold:
			return "medium"
		default:
			return "low"
		}
	}
	return "low"
}

func signalSeverity(score int) string {
	switch {
	case score >= 85:
		return "critical"
	case score >= 70:
		return "high"
	case score >= 55:
		return "medium"
	default:
		return "low"
	}
}

func evidenceIdentityQuality(mode string) float64 {
	switch mode {
	case "device_bound":
		return 0.90
	case "mixed":
		return 0.65
	case "legacy_unbound":
		return 0.40
	default:
		return 0.30
	}
}

func distinctBaselineDays(prior []model.AuditFeatureSnapshot) int {
	days := map[string]bool{}
	for _, snapshot := range prior {
		days[snapshot.WindowStartedAt.UTC().Format("2006-01-02")] = true
	}
	if len(days) > 28 {
		return 28
	}
	return len(days)
}

func baselineHourlyStats(prior []model.AuditFeatureSnapshot) hourlyBaseline {
	hourlyConn := map[int64]float64{}
	hourlyPeak := map[int64]float64{}
	for _, snapshot := range prior {
		var feature Features
		if json.Unmarshal(snapshot.Features, &feature) != nil {
			continue
		}
		hour := snapshot.WindowStartedAt.UTC().Truncate(time.Hour).Unix()
		hourlyConn[hour] += float64(feature.ConnectionCount)
		if float64(feature.ActivePeak) > hourlyPeak[hour] {
			hourlyPeak[hour] = float64(feature.ActivePeak)
		}
	}
	if len(hourlyConn) < 3 {
		return hourlyBaseline{}
	}
	return hourlyBaseline{
		connection: statsFromValues(hourlyConn), activePeak: statsFromValues(hourlyPeak),
		samples: len(hourlyConn),
	}
}

func statsFromValues(values map[int64]float64) baselineStatsValue {
	nums := make([]float64, 0, len(values))
	for _, value := range values {
		nums = append(nums, value)
	}
	sort.Float64s(nums)
	median := percentile(nums, 0.5)
	p95 := percentile(nums, 0.95)
	mads := make([]float64, 0, len(nums))
	for _, value := range nums {
		mads = append(mads, math.Abs(value-median))
	}
	sort.Float64s(mads)
	mad := percentile(mads, 0.5)
	return baselineStatsValue{median: &median, p95: &p95, mad: &mad, samples: len(nums)}
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(q*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func nextEvidenceRef(pack *model.AuditEvidencePack) string {
	ref := fmt.Sprintf("ev-%02d", len(pack.Features)+1)
	return ref
}

func nextSignalRef(pack *model.AuditEvidencePack) string {
	return fmt.Sprintf("sig-%02d", len(pack.Signals)+1)
}

func nextCounterRef(pack *model.AuditEvidencePack) string {
	return fmt.Sprintf("ce-%02d", len(pack.CounterEvidence)+1)
}

func nextTimelineRef(pack *model.AuditEvidencePack) string {
	return fmt.Sprintf("tl-%02d", len(pack.Timeline)+1)
}

// PackID returns the content-addressed ID of a canonicalized pack so identical
// evidence packs are stored and billed once and reports can be replayed.
func PackID(pack *model.AuditEvidencePack) (string, error) {
	encoded, err := json.Marshal(pack)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
