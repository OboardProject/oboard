package auditintel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const FeatureVersion = 1

type Service struct {
	store            *store.Store
	anonymizationKey []byte
	now              func() time.Time
}

type Features struct {
	SourceIPCount        int      `json:"source_ip_count"`
	RegionCount          int      `json:"region_count"`
	Regions              []string `json:"regions"`
	ServerCount          int      `json:"server_count"`
	DestinationCount     int      `json:"destination_count"`
	DestinationPortCount int      `json:"destination_port_count"`
	ConnectionCount      int64    `json:"connection_count"`
	ClosedCount          int64    `json:"closed_count"`
	ShortConnectionCount int64    `json:"short_connection_count"`
	DurationTotalMS      int64    `json:"duration_total_ms"`
	DurationMaxMS        int64    `json:"duration_max_ms"`
	DroppedBucketCount   int64    `json:"dropped_bucket_count"`
	CoverageIncomplete   bool     `json:"coverage_incomplete"`
	ActivePeak           int64    `json:"active_peak"`
	ReportCount          int      `json:"report_count"`
}

func New(store *store.Store, anonymizationKey string) *Service {
	return &Service{store: store, anonymizationKey: []byte(anonymizationKey), now: time.Now}
}

func (s *Service) EvaluateUsers(ctx context.Context, userIDs []int64) error {
	seen := map[int64]bool{}
	for _, userID := range userIDs {
		if userID <= 0 || seen[userID] {
			continue
		}
		seen[userID] = true
		if _, err := s.EvaluateUser(ctx, userID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) EvaluateUser(ctx context.Context, userID int64) (*model.AuditIncident, error) {
	detail, err := s.store.ConnectionAuditUserDetail(ctx, userID, 1)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	windowStart := now.Add(-15 * time.Minute)
	features, _ := extractFeatures(detail.Recent, windowStart, true)
	featuresJSON, _ := json.Marshal(features)
	prior, err := s.store.ListAuditFeatureSnapshots(ctx, userID, "15m", 14)
	if err != nil {
		return nil, err
	}
	anomaly := anomalyScore(features, prior)
	ruleScore := deterministicScore(detail.Summary.RiskScore, features)
	fingerprint := incidentFingerprint(userID, now, features)
	snapshotID, err := randomID("afs")
	if err != nil {
		return nil, err
	}
	snapshot := &model.AuditFeatureSnapshot{ID: snapshotID, UserID: userID, Window: "15m", WindowStartedAt: windowStart, WindowEndedAt: now, FeatureVersion: FeatureVersion, RuleScore: ruleScore, AnomalyScore: anomaly, Features: featuresJSON, Fingerprint: fingerprint}
	if err := s.store.CreateAuditFeatureSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	if ruleScore < 50 && (anomaly == nil || *anomaly < 70) {
		return nil, nil
	}
	incidentID, err := randomID("inc")
	if err != nil {
		return nil, err
	}
	incident := &model.AuditIncident{ID: incidentID, UserID: userID, Status: "open", Severity: severity(ruleScore, anomaly), RuleScore: ruleScore, AnomalyScore: anomaly, Fingerprint: fingerprint, LatestSnapshotID: snapshot.ID}
	if err := s.store.UpsertAuditIncident(ctx, incident); err != nil {
		return nil, err
	}
	return incident, nil
}

func extractFeatures(reports []model.ConnectionAuditReport, since time.Time, maskSensitive bool) (Features, []any) {
	ips, regions, servers, destinations, ports := map[string]bool{}, map[string]bool{}, map[int64]bool{}, map[string]bool{}, map[int]bool{}
	features := Features{}
	representative := []any{}
	coverageWindows := map[string]bool{}
	for _, report := range reports {
		if report.EndedAt.Before(since) {
			continue
		}
		ips[report.SourceIP], servers[report.ServerID] = true, true
		region := strings.TrimSpace(report.SourceProvince)
		if region == "" {
			region = strings.TrimSpace(report.SourceCountryCode)
		}
		if region != "" {
			regions[region] = true
		}
		if report.Destination != "" {
			destinations[report.Destination] = true
		}
		if report.DestinationPort > 0 {
			ports[report.DestinationPort] = true
		}
		features.ConnectionCount += report.ConnectionCount
		features.ClosedCount += report.ClosedCount
		features.DurationTotalMS += report.DurationTotalMS
		if report.DurationMaxMS > features.DurationMaxMS {
			features.DurationMaxMS = report.DurationMaxMS
		}
		if report.ClosedCount > 0 && report.DurationMaxMS <= int64((5*time.Second)/time.Millisecond) {
			features.ShortConnectionCount += report.ClosedCount
		}
		coverageKey := fmt.Sprintf("%d/%s/%s", report.CollectionGeneration, report.CollectionStartedAt.UTC().Format(time.RFC3339Nano), report.CollectionEndedAt.UTC().Format(time.RFC3339Nano))
		if !coverageWindows[coverageKey] {
			coverageWindows[coverageKey] = true
			features.DroppedBucketCount += report.DroppedBucketCount
		}
		if report.ActivePeak > features.ActivePeak {
			features.ActivePeak = report.ActivePeak
		}
		features.ReportCount++
		if len(representative) < 12 {
			sourceIP, destination := report.SourceIP, report.Destination
			if maskSensitive {
				sourceIP, destination = maskedIP(sourceIP), reducedDestination(destination)
			}
			representative = append(representative, map[string]any{"source_ip": sourceIP, "region": region, "network": report.Network, "destination": destination, "destination_port": report.DestinationPort, "connection_count": report.ConnectionCount, "closed_count": report.ClosedCount, "duration_total_ms": report.DurationTotalMS, "duration_max_ms": report.DurationMaxMS, "active_peak": report.ActivePeak, "collection_dropped_buckets": report.DroppedBucketCount, "started_at": report.StartedAt, "ended_at": report.EndedAt})
		}
	}
	features.SourceIPCount, features.RegionCount, features.ServerCount = len(ips), len(regions), len(servers)
	features.DestinationCount, features.DestinationPortCount = len(destinations), len(ports)
	features.CoverageIncomplete = features.DroppedBucketCount > 0
	for region := range regions {
		features.Regions = append(features.Regions, region)
	}
	sort.Strings(features.Regions)
	return features, representative
}

func deterministicScore(existing int, features Features) int {
	score := existing
	if features.ActivePeak >= 50 {
		score += 15
	}
	if features.DestinationCount >= 100 {
		score += 20
	}
	if features.DestinationPortCount >= 30 {
		score += 20
	}
	if features.ClosedCount >= 500 && features.ShortConnectionCount*100/features.ClosedCount >= 80 {
		score += 20
	}
	if score > 100 {
		return 100
	}
	return score
}

func anomalyScore(current Features, prior []model.AuditFeatureSnapshot) *int {
	if len(prior) < 3 {
		return nil
	}
	var connectionTotal, peakTotal int64
	count := 0
	for _, snapshot := range prior {
		var feature Features
		if json.Unmarshal(snapshot.Features, &feature) != nil {
			continue
		}
		connectionTotal += feature.ConnectionCount
		peakTotal += feature.ActivePeak
		count++
	}
	if count < 3 {
		return nil
	}
	connectionMean, peakMean := connectionTotal/int64(count), peakTotal/int64(count)
	score := 0
	if current.ConnectionCount > max64(20, connectionMean*3) {
		score += 50
	}
	if current.ActivePeak > max64(5, peakMean*3) {
		score += 30
	}
	if current.RegionCount >= 3 {
		score += 20
	}
	if score > 100 {
		score = 100
	}
	return &score
}

func incidentFingerprint(userID int64, at time.Time, features Features) string {
	bucket := at.Truncate(15 * time.Minute).Format(time.RFC3339)
	payload, _ := json.Marshal([]any{FeatureVersion, userID, bucket, features.Regions, bucketInt(features.SourceIPCount, 2), bucketInt(int(features.ActivePeak), 10), bucketInt(features.DestinationCount, 20)})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func severity(rule int, anomaly *int) string {
	value := rule
	if anomaly != nil && *anomaly > value {
		value = *anomaly
	}
	switch {
	case value >= 85:
		return "critical"
	case value >= 70:
		return "high"
	default:
		return "medium"
	}
}

func maskedIP(raw string) string {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return "unknown"
	}
	bits := 24
	if addr.Is6() {
		bits = 48
	}
	return netip.PrefixFrom(addr, bits).Masked().String()
}

func reducedDestination(raw string) string {
	raw = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if addr, err := netip.ParseAddr(raw); err == nil {
		return maskedIP(addr.String())
	}
	if domain, err := publicsuffix.EffectiveTLDPlusOne(raw); err == nil {
		return domain
	}
	return "unknown"
}

func baselineJSON(prior []model.AuditFeatureSnapshot) json.RawMessage {
	if len(prior) > 7 {
		prior = prior[:7]
	}
	snapshots := make([]map[string]any, 0, len(prior))
	for _, snapshot := range prior {
		snapshots = append(snapshots, map[string]any{
			"window": snapshot.Window, "window_started_at": snapshot.WindowStartedAt, "window_ended_at": snapshot.WindowEndedAt,
			"feature_version": snapshot.FeatureVersion, "rule_score": snapshot.RuleScore, "anomaly_score": snapshot.AnomalyScore, "features": snapshot.Features,
		})
	}
	encoded, _ := json.Marshal(map[string]any{"sample_count": len(snapshots), "snapshots": snapshots})
	return encoded
}

func (s *Service) anonymizedUserID(userID int64) string {
	mac := hmac.New(sha256.New, s.anonymizationKey)
	_, _ = mac.Write([]byte("oboard-audit-user-v1\x00" + strconv.FormatInt(userID, 10)))
	return "usr_" + hex.EncodeToString(mac.Sum(nil)[:8])
}

func randomID(prefix string) (string, error) {
	random, err := security.RandomToken(18)
	return prefix + "_" + random, err
}

func bucketInt(value, size int) int {
	if size <= 0 {
		return value
	}
	return value / size
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
