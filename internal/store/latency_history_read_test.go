package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSummaryReadMatchesRawAcrossBoundariesAndPendingLateData(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	for i := 0; i < 240; i++ {
		saveRollupReport(t, db, node.ID, base.Add(time.Duration(i)*time.Minute).String(), base.Add(time.Duration(i)*time.Minute), int64(i+10), i%7 != 0, 3, i%4)
	}
	from, to := base.Add(31*time.Second), base.Add(3*time.Hour+17*time.Second)
	raw, _, err := db.QueryLatencySummaryChart(ctx, node.ID, from, to, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	startRollupTest(t, db)
	for {
		state, _ := db.LatencyRollupState(ctx)
		if state.BackfillCursor == 0 {
			break
		}
		if _, err := db.RunLatencyBackfillBatch(ctx, time.Now(), 500); err != nil {
			t.Fatal(err)
		}
	}
	summarized, coverage, err := db.QueryLatencySummaryChart(ctx, node.ID, from, to, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Source != "mixed" || !reflect.DeepEqual(raw, summarized) {
		t.Fatalf("summary differs: coverage=%+v\nraw=%+v\nsummary=%+v", coverage, raw, summarized)
	}
	saveRollupReport(t, db, node.ID, "late", base.Add(10*time.Minute), 999, true, 3, 3)
	withLate, _, err := db.QueryLatencySummaryChart(ctx, node.ID, from, to, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	after, _, err := db.QueryLatencySummaryChart(ctx, node.ID, from, to, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withLate, after) {
		t.Fatalf("pending sample overlapped or was lost: %+v %+v", withLate, after)
	}
	db.db.db.SetMaxOpenConns(1)
	if _, _, err := db.QueryLatencySummaryChart(ctx, node.ID, from, to, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalAndLiveLanesCommitWithoutOverlap(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for i := 0; i < 20; i++ {
		saveRollupReport(t, db, node.ID, fmt.Sprint(i), base.Add(time.Duration(i)*time.Minute), 10, true, 1, 1)
	}
	startRollupTest(t, db)
	for i := 20; i < 30; i++ {
		saveRollupReport(t, db, node.ID, fmt.Sprint(i), base.Add(time.Duration(i)*time.Minute), 10, true, 1, 1)
	}
	if _, err := db.RunLatencyBackfillBatch(ctx, time.Now(), 7); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.RunLatencyBackfillBatch(ctx, time.Now(), 7); err != nil {
			t.Fatal(err)
		}
	}
	var reports int64
	if err := db.db.QueryRow(`select sum(report_count) from latency_rollup_buckets where resolution_seconds=3600`).Scan(&reports); err != nil || reports != 30 {
		t.Fatalf("overlap/loss reports=%d %v", reports, err)
	}
	state, err := db.LatencyRollupState(ctx)
	if err != nil || state.BackfillCursor != 0 || state.LiveCursor < 30 {
		t.Fatalf("state=%+v %v", state, err)
	}
}

func TestHistoryReadUpgradePreservesPreviousData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, node := newConnectivityTestStoreAtPath(t, path)
	saveRollupReport(t, db, node.ID, "previous", time.Now().Add(-time.Hour), 42, true, 3, 3)
	for _, query := range []string{
		`alter table server_latency_probe_results drop column measurement_revision`,
		`alter table sla_projection_buckets drop column outages_json`,
		`alter table sla_projection_buckets drop column details_complete`,
		`alter table sla_projection_servers drop column details_version`,
		`drop table latency_legacy_archive`, `drop table latency_legacy_progress`,
	} {
		if _, err := db.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var revision string
	var latency int
	if err := upgraded.db.QueryRow(`select measurement_revision,latency_ms from server_latency_probe_results where report_id='previous'`).Scan(&revision, &latency); err != nil {
		t.Fatal(err)
	}
	if revision != "" || latency != 42 {
		t.Fatal("upgrade changed historical measurement")
	}
	var cursor, archived int
	if err := upgraded.db.QueryRow(`select cursor from latency_legacy_progress`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`select count(*) from latency_legacy_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if cursor != -1 || archived != 0 {
		t.Fatal("startup backfilled historical events")
	}
	for i := 0; i < 2; i++ {
		if _, err := upgraded.RunLatencyLegacyBackfill(ctx, 500); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLegacyArchiveReadKeepsRowBudget(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour)
	_, err := db.db.Exec(`with recursive n(x) as (select 1 union all select x+1 from n where x<50001) insert into server_connectivity_events(server_id,kind,latency_ms,source,effective_at,event_key,created_at) select ?,'probe_result',10,'legacy',?,'budget-'||x,? from n`, node.ID, connectivityTimeBound(base), connectivityTimeBound(base))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.db.Exec(`insert into latency_legacy_archive select id,server_id,kind,source,available,latency_ms,effective_at from server_connectivity_events where source='legacy'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`update latency_legacy_progress set cursor=0`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.legacyLatencySource(ctx, db.db, node.ID, base, base.Add(time.Hour)); err != ErrHistoryCoverage {
		t.Fatalf("archive bypassed read budget: %v", err)
	}
}
