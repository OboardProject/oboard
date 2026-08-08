package store

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConnectionAuditRobustZSQLParity(t *testing.T) {
	at := time.Date(2026, time.August, 8, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		history []int64
		current int64
		filters bool
	}{
		{name: "ordinary traffic", history: []int64{90, 100, 100, 110}, current: 105},
		{name: "anomaly spike", history: []int64{90, 100, 100, 110}, current: 1000},
		{name: "insufficient history", history: []int64{90, 110}, current: 1000},
		{name: "filtered reports and unrelated buckets", history: []int64{90, 100, 100, 110}, current: 105, filters: true},
	}
	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, server, user := newMaintenanceTestStore(t)
			reports := make([]model.ConnectionAuditReport, 0, len(test.history)+8)
			for index, value := range test.history {
				reports = append(reports, robustZReport(fmt.Sprintf("history-%d-%d", testIndex, index), server.ID, user.ID, at.Add(-time.Duration(index+1)*7*24*time.Hour), value))
			}
			reports = append(reports, robustZReport(fmt.Sprintf("current-%d", testIndex), server.ID, user.ID, at, test.current))
			if test.filters {
				internal := robustZReport("internal", server.ID, user.ID, at, 10000)
				internal.InternalProbe = true
				confirmed := robustZReport("confirmed", server.ID, user.ID, at, 10000)
				confirmed.ProbeState = "confirmed"
				candidate := robustZReport("candidate", server.ID, user.ID, at, 10000)
				candidate.ProbeState = "candidate"
				dropped := robustZReport("dropped", server.ID, user.ID, at, 10000)
				dropped.DroppedBucketCount = 1
				differentWeekday := robustZReport("different-weekday", server.ID, user.ID, at.Add(-24*time.Hour), 10000)
				differentHour := robustZReport("different-hour", server.ID, user.ID, at.Add(-7*24*time.Hour+time.Hour), 10000)
				reports = append(reports, internal, confirmed, candidate, dropped, differentWeekday, differentHour)
			}
			for _, report := range reports {
				insertRobustZReport(t, s, report)
			}
			legacy := legacyConnectionAuditRobustZ(reports, at)
			got, err := s.connectionAuditRobustZ(context.Background(), user.ID, at)
			if err != nil {
				t.Fatal(err)
			}
			if got != legacy {
				t.Fatalf("SQL robust Z = %v, legacy = %v", got, legacy)
			}
		})
	}
}

func robustZReport(id string, serverID, userID int64, startedAt time.Time, connections int64) model.ConnectionAuditReport {
	return model.ConnectionAuditReport{
		ReportID: id, ServerID: serverID, UserID: userID, SourceIP: "203.0.113.1", Network: "tcp",
		ConnectionCount: connections, CollectionStartedAt: startedAt, CollectionEndedAt: startedAt,
		StartedAt: startedAt, EndedAt: startedAt,
	}
}

func insertRobustZReport(t *testing.T, s *Store, report model.ConnectionAuditReport) {
	t.Helper()
	ts := report.StartedAt.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`insert into connection_audit_reports(report_id,server_id,user_id,source_ip,network,connection_count,probe_state,internal_probe,dropped_bucket_count,collection_started_at,collection_ended_at,started_at,ended_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, report.ReportID, report.ServerID, report.UserID, report.SourceIP, report.Network, report.ConnectionCount, report.ProbeState, boolInt(report.InternalProbe), report.DroppedBucketCount, ts, ts, ts, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func legacyConnectionAuditRobustZ(reports []model.ConnectionAuditReport, at time.Time) float64 {
	currentStart := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), 0, 0, 0, at.Location())
	currentEnd := currentStart.Add(time.Hour)
	current := 0.0
	historyByHour := map[time.Time]float64{}
	for _, report := range reports {
		if report.InternalProbe || report.ProbeState == "confirmed" || report.ProbeState == "candidate" || report.DroppedBucketCount > 0 {
			continue
		}
		started := report.StartedAt.In(at.Location())
		value := float64(report.ConnectionCount)
		if !started.Before(currentStart) && started.Before(currentEnd) {
			current += value
			continue
		}
		if started.Before(currentStart.Add(-28*24*time.Hour)) || !started.Before(currentStart) || started.Weekday() != at.Weekday() || started.Hour() != at.Hour() {
			continue
		}
		bucket := time.Date(started.Year(), started.Month(), started.Day(), started.Hour(), 0, 0, 0, at.Location())
		historyByHour[bucket] += value
	}
	history := make([]float64, 0, len(historyByHour))
	for _, value := range historyByHour {
		history = append(history, value)
	}
	if len(history) < 3 {
		return 0
	}
	median := medianAuditValues(history)
	deviations := make([]float64, 0, len(history))
	for _, value := range history {
		deviations = append(deviations, math.Abs(value-median))
	}
	mad := medianAuditValues(deviations)
	robustZ := (current - median) / (1.4826*mad + math.Max(1, 0.1*median))
	if robustZ < 0 {
		return 0
	}
	return math.Round(robustZ*10) / 10
}

func TestConnectionAuditProbeRefreshNarrowQueryParity(t *testing.T) {
	s, server, user := newMaintenanceTestStore(t)
	at := time.Now().UTC().Truncate(time.Second)
	reports := []model.ConnectionAuditReport{}
	reports = append(reports, probeParityReports(server.ID, user.ID, "confirmed-device", 20, 5, at.Add(-30*time.Second), "confirmed")...)
	reports = append(reports, probeParityReports(server.ID, user.ID, "candidate-device", 16, 4, at.Add(-5*time.Second), "candidate")...)
	reports = append(reports, probeParityReports(server.ID, user.ID, "normal-device", 16, 4, at.Add(-30*time.Second), "normal_traffic")...)

	expected := legacyProbeEpisodes(reports, user.ID, at)
	if len(expected) != 3 {
		t.Fatalf("legacy episodes = %d, want 3", len(expected))
	}
	if _, err := s.AddConnectionAuditReports(context.Background(), reports); err != nil {
		t.Fatal(err)
	}
	actual, err := s.listConnectionProbeEpisodes(context.Background(), user.ID, at.Add(-time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("episodes = %d, want %d: %#v", len(actual), len(expected), actual)
	}
	for _, episode := range actual {
		want, ok := expected[episode.DeviceIDHash]
		if !ok {
			t.Fatalf("unexpected episode: %#v", episode)
		}
		if episode.State != want.State || episode.Score != want.Score || episode.NodeCount != want.NodeCount || episode.ConnectionCount != want.ConnectionCount || episode.ID != want.ID {
			t.Fatalf("episode parity for %s: got=%#v want=%#v", episode.DeviceIDHash, episode, want)
		}
		var reportStates int
		if err := s.db.QueryRow(`select count(*) from connection_audit_reports where device_id_hash=? and started_at>=? and probe_state=?`, episode.DeviceIDHash, at.Add(-10*time.Minute-connectionAuditProbeWindow).Format(time.RFC3339Nano), want.State).Scan(&reportStates); err != nil {
			t.Fatal(err)
		}
		if reportStates != want.NodeCount {
			t.Fatalf("classified reports for %s = %d, want %d", episode.DeviceIDHash, reportStates, want.NodeCount)
		}
	}
}

func probeParityReports(serverID, userID int64, deviceID string, assigned, recent int, recentStart time.Time, wantedState string) []model.ConnectionAuditReport {
	reports := make([]model.ConnectionAuditReport, 0, assigned+recent)
	for index := 0; index < assigned; index++ {
		startedAt := recentStart.Add(-2 * time.Hour)
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: fmt.Sprintf("%s-assigned-%d", deviceID, index), ServerID: serverID, UserID: userID,
			DeviceIDHash: deviceID, SourceIP: "203.0.113.1", Network: "tcp", OutboundTag: fmt.Sprintf("node-%d", index), ConnectionCount: 1,
			CollectionStartedAt: startedAt, CollectionEndedAt: startedAt.Add(time.Second), StartedAt: startedAt, EndedAt: startedAt.Add(time.Second),
		})
	}
	for index := 0; index < recent; index++ {
		startedAt := recentStart.Add(time.Duration(index) * time.Second)
		upload := int64(1024)
		closed, shortClosed := int64(1), int64(1)
		if wantedState == "candidate" {
			shortClosed = 0
		}
		if wantedState == "normal_traffic" {
			upload = 2 * 1024 * 1024
		}
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: fmt.Sprintf("%s-recent-%d", deviceID, index), ServerID: serverID, UserID: userID,
			DeviceIDHash: deviceID, SourceIP: "203.0.113.1", Network: "tcp", OutboundTag: fmt.Sprintf("node-%d", index), ConnectionCount: 1,
			ClosedCount: closed, DurationLE1SCount: shortClosed, UploadBytes: upload,
			CollectionStartedAt: startedAt, CollectionEndedAt: startedAt.Add(time.Second), StartedAt: startedAt, EndedAt: startedAt.Add(time.Second),
		})
	}
	return reports
}

func legacyProbeEpisodes(reports []model.ConnectionAuditReport, userID int64, at time.Time) map[string]model.ConnectionProbeEpisode {
	cutoff := at.Add(-10*time.Minute - connectionAuditProbeWindow)
	byIdentity := map[string][]model.ConnectionAuditReport{}
	assignedNodes := map[string]map[string]struct{}{}
	for _, report := range reports {
		if report.InternalProbe || report.EndedAt.Before(at.Add(-24*time.Hour)) {
			continue
		}
		key := connectionAuditReportIdentity(report).key()
		if assignedNodes[key] == nil {
			assignedNodes[key] = map[string]struct{}{}
		}
		assignedNodes[key][connectionAuditNode(report)] = struct{}{}
		if !report.StartedAt.Before(cutoff) {
			byIdentity[key] = append(byIdentity[key], report)
		}
	}
	out := map[string]model.ConnectionProbeEpisode{}
	for key, items := range byIdentity {
		sort.SliceStable(items, func(i, j int) bool { return items[i].StartedAt.Before(items[j].StartedAt) })
		for start := 0; start < len(items); {
			end := start + 1
			for end < len(items) && !items[end].StartedAt.After(items[start].StartedAt.Add(connectionAuditProbeWindow)) {
				end++
			}
			episode, state := classifyConnectionProbeEpisode(userID, items[start:end], connectionProbeCandidateThreshold(len(assignedNodes[key])), at)
			if state != "" {
				out[episode.DeviceIDHash] = episode
			}
			start = end
		}
	}
	return out
}

func TestConnectionAuditUserDetailUsesSingleUserRiskPath(t *testing.T) {
	s, server, user := newMaintenanceTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	reports := probeParityReports(server.ID, user.ID, "detail-probe", 4, 4, at, "confirmed")
	left := meaningfulConnectionReport("detail-risk-left", server.ID, user.ID, "detail-risk", "1.1.1.1", "CN", "ISP-A", at, at.Add(90*time.Second))
	right := meaningfulConnectionReport("detail-risk-right", server.ID, user.ID, "detail-risk", "8.8.8.8", "US", "ISP-B", at.Add(5*time.Second), at.Add(90*time.Second))
	reports = append(reports, left, right)
	if _, err := s.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	presenceAt := time.Now().UTC()
	presence := model.ConnectionPresenceEvent{
		Sequence: 1, ServerID: server.ID, UserID: user.ID, DeviceIDHash: "detail-risk", CredentialEpoch: 1,
		SourceIP: "1.1.1.1", RouteID: "route-1.1.1.1", Network: "tcp", Event: "first_meaningful_payload",
		State: "active", ActiveConnections: 1, Meaningful: true, PayloadLastAt: presenceAt, At: presenceAt,
	}
	if _, err := s.ApplyConnectionPresenceEvents(ctx, "detail-agent", server.ID, 0, []model.ConnectionPresenceEvent{presence}); err != nil {
		t.Fatal(err)
	}

	detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.ConnectionAuditUserRisk(ctx, user.ID, 24, DefaultAuditPolicy(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detail.Summary, *summary) {
		t.Fatalf("detail summary differs from single-user risk path:\n detail=%#v\n summary=%#v", detail.Summary, *summary)
	}
	overview, err := s.ConnectionAuditOverview(ctx, 24, true, DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Users) != 1 || !reflect.DeepEqual(detail.Summary, overview.Users[0]) {
		t.Fatalf("single-user detail changed overview semantics:\n detail=%#v\n overview=%#v", detail.Summary, overview.Users)
	}
	if len(detail.Sources) == 0 || len(detail.Destinations) == 0 || len(detail.Outbounds) == 0 || len(detail.Servers) == 0 || len(detail.Recent) == 0 || len(detail.RiskEvents) == 0 || len(detail.ProbeEpisodes) == 0 || len(detail.Presence) == 0 {
		t.Fatalf("detail lost data: sources=%d destinations=%d outbounds=%d servers=%d recent=%d risk_events=%d probe_episodes=%d presence=%d", len(detail.Sources), len(detail.Destinations), len(detail.Outbounds), len(detail.Servers), len(detail.Recent), len(detail.RiskEvents), len(detail.ProbeEpisodes), len(detail.Presence))
	}
}
