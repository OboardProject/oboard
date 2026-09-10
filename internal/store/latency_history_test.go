package store

import (
	"context"
	"github.com/OboardProject/oboard/internal/model"
	"testing"
	"time"
)

func TestLatencyHistoryRevisionIsTargetedAndBounded(t *testing.T) {
	s := &Store{}
	at := time.Now()
	a := s.LatencyHistoryRevision(1, at)
	b := s.LatencyHistoryRevision(2, at)
	s.invalidateLateLatency(1, at.Add(time.Second))
	if s.LatencyHistoryRevision(1, time.Time{}) != a {
		t.Fatal("normal report invalidated cache")
	}
	s.invalidateLateLatency(1, at.Add(-time.Second))
	if s.LatencyHistoryRevision(1, time.Time{}) == a || s.LatencyHistoryRevision(2, time.Time{}) != b {
		t.Fatal("late report invalidation not targeted")
	}
	for i := int64(3); i < 600; i++ {
		s.LatencyHistoryRevision(i, at)
	}
	if len(s.latencyHistory) > 256 {
		t.Fatal("unbounded revision entries")
	}
}
func TestLatencyHistoryPublicAuthorityAndClear(t *testing.T) {
	db, server := newConnectivityTestStore(t)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	report := model.LatencyProbeResultReport{ReportID: "chart-authority", ResourceVersion: "test", CheckedAt: from.Add(time.Minute), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Available: true, LatencyMS: 25, SampleCount: 3, SuccessCount: 3}}}
	revision := db.LatencyHistoryRevision(server.ID, from.Add(time.Hour))
	if err := db.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	changed := db.LatencyHistoryRevision(server.ID, time.Time{})
	if changed == revision {
		t.Fatal("late inserted report did not invalidate")
	}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	if db.LatencyHistoryRevision(server.ID, time.Time{}) != changed {
		t.Fatal("retry invalidated despite no insert")
	}
	points, err := db.QueryLatencyChartBuckets(ctx, server.ID, from, from.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := db.QueryLegacyLatencyBuckets(ctx, server.ID, from, from.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points.PublicPoints) != 1 || points.PublicPoints[0].Count != 1 || len(legacy.PublicPoints) != 0 {
		t.Fatalf("public=%+v legacy=%+v", points.PublicPoints, legacy.PublicPoints)
	}
	if err := db.DeleteLatencyProbeResults(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err = db.QueryLegacyLatencyBuckets(ctx, server.ID, from, from.Add(time.Hour), time.Minute)
	if err != nil || len(legacy.PublicPoints) != 0 {
		t.Fatalf("revived cleared reports: %+v %v", legacy, err)
	}
}

func TestLatencyHistoryRetentionInvalidatesOnlyAffectedServers(t *testing.T) {
	db, server := newConnectivityTestStore(t)
	ctx := context.Background()
	cutoff := time.Now().UTC().Truncate(time.Minute)
	other := &model.Server{Name: "unaffected"}
	if err := db.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	insertLatencyProbeResult(t, db, server.ID, "old", "custom", 0, "old", "tcp", "P", "C", true, 10, 1, 1, cutoff.Add(-time.Hour))
	before := db.LatencyHistoryRevision(server.ID, cutoff)
	unchanged := db.LatencyHistoryRevision(other.ID, cutoff)
	db.db.db.SetMaxOpenConns(1)
	count, _, err := db.deleteMaintenanceBatchesReporting(ctx, `delete from server_latency_probe_results where rowid in (select rowid from server_latency_probe_results where checked_at<? limit ?)`, cutoff, true)
	if err != nil || count != 1 {
		t.Fatalf("deleted=%d %v", count, err)
	}
	if db.LatencyHistoryRevision(server.ID, time.Time{}) == before || db.LatencyHistoryRevision(other.ID, time.Time{}) != unchanged {
		t.Fatal("retention invalidation was not targeted")
	}
}

func TestLatencyChartReadsPreviousConnectivityOnlyState(t *testing.T) {
	db, server := newConnectivityTestStore(t)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	available := true
	// This is the persisted event shape produced by the pre-report telemetry seed.
	_, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, Available: &available, LatencyMS: 37, Source: "migration_seed", EffectiveAt: from.Add(time.Minute), EventKey: "probe:" + from.Add(time.Minute).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := db.QueryLegacyLatencyBuckets(ctx, server.ID, from, from.Add(time.Hour), time.Minute)
		if err != nil || len(result.PublicPoints) != 1 || result.PublicPoints[0].LatencyMS != 37 || result.PublicPoints[0].Count != 1 {
			t.Fatalf("previous-state result=%+v %v", result, err)
		}
	}
}

func TestLatencyChartFractionalReportsRespectNormalizedBounds(t *testing.T) {
	db, server := newConnectivityTestStore(t)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	to := from.Add(time.Minute)
	for i, at := range []time.Time{from, from.Add(100 * time.Millisecond), to.Add(-100 * time.Millisecond), to, to.Add(100 * time.Millisecond)} {
		insertLatencyProbeResult(t, db, server.ID, at.String(), "public", 0, "public", "tcp", "", "", true, 10, 1, 1, at)
		available := true
		_, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, Available: &available, LatencyMS: 10, Source: "migration_seed", EffectiveAt: at, EventKey: at.String()})
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	current, err := db.QueryLatencyChartBuckets(ctx, server.ID, from, to, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := db.QueryLegacyLatencyBuckets(ctx, server.ID, from, to, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.PublicPoints) != 1 || current.PublicPoints[0].Count != 3 || len(legacy.PublicPoints) != 1 || legacy.PublicPoints[0].Count != 3 {
		t.Fatalf("current=%+v legacy=%+v", current.PublicPoints, legacy.PublicPoints)
	}
}
