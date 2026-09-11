package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func startRollupTest(t *testing.T, db *Store) {
	t.Helper()
	if _, err := db.RunLatencyRollupBatch(context.Background(), time.Now(), 500); err != nil {
		t.Fatal(err)
	}
}

func saveRollupReport(t *testing.T, db *Store, serverID int64, id string, at time.Time, latency int64, available bool, samples, success int) {
	t.Helper()
	report := model.LatencyProbeResultReport{ReportID: id, ResourceVersion: "test", CheckedAt: at, Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Mode: "tcp", Host: "example.net", Port: 443, Available: available, LatencyMS: latency, SampleCount: samples, SuccessCount: success}}}
	if err := db.SaveLatencyProbeResults(context.Background(), serverID, report); err != nil {
		t.Fatal(err)
	}
}

func TestLatencyRollupCountsIdempotencyAndLateReports(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	startRollupTest(t, db)
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for i, item := range []struct {
		minute           int
		latency          int64
		available        bool
		samples, success int
	}{{1, 10, true, 3, 3}, {2, 20, true, 2, 2}, {3, 30, true, 0, 0}, {4, 0, false, 4, 0}, {7, 50, true, 2, 1}} {
		saveRollupReport(t, db, node.ID, fmt.Sprint(i), base.Add(time.Duration(item.minute)*time.Minute), item.latency, item.available, item.samples, item.success)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 2); err != nil {
			t.Fatal(err)
		}
	}
	var sum, count, curve, curveCount, attempts, success, reports, available int64
	read := func() {
		t.Helper()
		if err := db.db.QueryRow(`select latency_sum,latency_value_count,curve_sum,curve_value_count,attempt_count,success_count,report_count,available_report_count from latency_rollup_buckets where server_id=? and resolution_seconds=3600`, node.ID).Scan(&sum, &count, &curve, &curveCount, &attempts, &success, &reports, &available); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if sum != 80 || count != 3 || curve != 110 || curveCount != 4 || attempts != 11 || success != 6 || reports != 5 || available != 4 {
		t.Fatalf("totals: %d %d %d %d %d %d %d %d", sum, count, curve, curveCount, attempts, success, reports, available)
	}
	var fiveSum, fiveCount, fiveReports int64
	if err := db.db.QueryRow(`select sum(latency_sum),sum(latency_value_count),sum(report_count) from latency_rollup_buckets where resolution_seconds=300`).Scan(&fiveSum, &fiveCount, &fiveReports); err != nil {
		t.Fatal(err)
	}
	if fiveSum != sum || fiveCount != count || fiveReports != reports {
		t.Fatal("hourly total re-added full five-minute buckets")
	}
	saveRollupReport(t, db, node.ID, "0", base.Add(time.Minute), 10, true, 3, 3)
	result, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500)
	if err != nil || result.Processed != 0 {
		t.Fatalf("retry: %+v %v", result, err)
	}
	saveRollupReport(t, db, node.ID, "late", base.Add(30*time.Second), 1000, true, 1, 1)
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	read()
	if sum != 1080 || curve != 1110 || count != 4 || reports != 6 {
		t.Fatal("late report not folded into original hour")
	}
	var peakAt int64
	if err := db.db.QueryRow(`select latency_max_at from latency_rollup_buckets where resolution_seconds=3600`).Scan(&peakAt); err != nil {
		t.Fatal(err)
	}
	if peakAt != base.Add(30*time.Second).UnixNano() {
		t.Fatal("peak time was synthesized")
	}
}

func TestLatencyRollupAtomicRetryRestartAndDeletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rollup.sqlite")
	db, node := newConnectivityTestStoreAtPath(t, path)
	startRollupTest(t, db)
	at := time.Now().UTC().Add(-time.Hour)
	saveRollupReport(t, db, node.ID, "first", at, 20, true, 3, 3)
	before, err := db.LatencyRollupState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`create trigger rollup_fail before insert on latency_rollup_buckets when new.resolution_seconds=3600 begin select raise(abort,'simulated interruption'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err == nil {
		t.Fatal("failure did not roll back")
	}
	after, _ := db.LatencyRollupState(ctx)
	var rows int
	if err := db.db.QueryRow(`select count(*) from latency_rollup_buckets`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || after.LiveCursor != before.LiveCursor {
		t.Fatal("partial buckets or advanced cursor after rollback")
	}
	if _, err := db.db.Exec(`drop trigger rollup_fail`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if result, err := reopened.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil || result.Processed != 0 {
		t.Fatalf("committed batch replayed after restart: %+v %v", result, err)
	}
	saveRollupReport(t, reopened, node.ID, "second", at.Add(time.Minute), 30, true, 3, 3)
	state, _ := reopened.LatencyRollupState(ctx)
	batch, err := reopened.readLatencyRollupBatch(ctx, state, time.Now().Add(-7*24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteLatencyProbeResults(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.commitLatencyRollupBatch(ctx, batch); !errors.Is(err, ErrLatencyRollupChanged) {
		t.Fatalf("stale generation accepted: %v", err)
	}
	if err := reopened.db.QueryRow(`select count(*) from latency_rollup_buckets`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("deleted history revived")
	}
	saveRollupReport(t, reopened, node.ID, "third", at.Add(2*time.Minute), 40, true, 3, 3)
	state, _ = reopened.LatencyRollupState(ctx)
	batch, err = reopened.readLatencyRollupBatch(ctx, state, time.Now().Add(-7*24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteServer(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.commitLatencyRollupBatch(ctx, batch); !errors.Is(err, ErrLatencyRollupChanged) {
		t.Fatalf("deleted server accepted: %v", err)
	}
}

func TestLatencyRollupUpgradeOnlyRegistersStructureAndBoundary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, node := newConnectivityTestStoreAtPath(t, path)
	saveRollupReport(t, db, node.ID, "historical", time.Now().Add(-time.Hour), 10, true, 1, 1)
	for _, query := range []string{`drop table latency_rollup_buckets`, `drop table latency_rollup_state`} {
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
	state, err := upgraded.LatencyRollupState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.LiveCursor != -1 || state.BackfillCursor != -1 {
		t.Fatal("startup aggregated history")
	}
	startRollupTest(t, upgraded)
	state, _ = upgraded.LatencyRollupState(ctx)
	if state.LiveCursor != 1 || state.HistoricalBoundary != 1 || state.BackfillCursor != 1 {
		t.Fatalf("boundary=%+v", state)
	}
	var summaries, raw int
	if err := upgraded.db.QueryRow(`select count(*) from latency_rollup_buckets`).Scan(&summaries); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`select count(*) from server_latency_probe_results`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if summaries != 0 || raw != 1 {
		t.Fatal("upgrade rewrote raw data or eagerly backfilled")
	}
}

func TestLatencyRollupWriteBudgetAndMeasurementIdentity(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	startRollupTest(t, db)
	at := time.Now().UTC().Add(-time.Hour)
	report := model.LatencyProbeResultReport{ReportID: "many", ResourceVersion: "test", CheckedAt: at}
	for i := 0; i < 500; i++ {
		report.Items = append(report.Items, model.LatencyProbeResult{ProbeID: fmt.Sprint(i), Kind: "custom", TaskName: "same-name", Mode: "tcp", Host: "one.example", Available: true, LatencyMS: 10, SampleCount: 1, SuccessCount: 1})
	}
	if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	first, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500)
	if err != nil {
		t.Fatal(err)
	}
	if first.Processed != 500 || first.Buckets != 1000 || first.PendingIDSpan != 0 {
		t.Fatalf("budget=%+v", first)
	}
	for {
		result, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500)
		if err != nil {
			t.Fatal(err)
		}
		if result.Processed == 0 {
			break
		}
	}
	report.Items = report.Items[:1]
	report.Items[0].TaskName = "renamed"
	report.ReportID = "rename"
	report.CheckedAt = at.Add(time.Second)
	if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRow(`select count(*) from latency_rollup_buckets where probe_id='0' and resolution_seconds=3600`).Scan(&n); err != nil || n != 1 {
		t.Fatal("rename changed identity")
	}
	report.ReportID = "changed"
	report.CheckedAt = at.Add(2 * time.Second)
	report.Items[0].Host = "two.example"
	if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`select count(*) from latency_rollup_buckets where probe_id='0' and resolution_seconds=3600`).Scan(&n); err != nil || n != 2 {
		t.Fatal("endpoint change mixed revisions")
	}
}

func TestLatencyRollupRetentionPolicyAndConcurrentBatchGuards(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	startRollupTest(t, db)
	at := time.Now().UTC().Add(-2 * time.Hour)
	saveRollupReport(t, db, node.ID, "current", at, 10, true, 1, 1)
	state, _ := db.LatencyRollupState(ctx)
	batch, err := db.readLatencyRollupBatch(ctx, state, time.Now().Add(-7*24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	batch.retentionDays = 7
	if err := db.SetSetting(ctx, ServerMonitoringRetentionDaysSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if err := db.commitLatencyRollupBatch(ctx, batch); !errors.Is(err, ErrLatencyRollupChanged) {
		t.Fatalf("changed retention committed: %v", err)
	}
	batch.retentionDays = 1
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.commitLatencyRollupBatch(cancelled, batch); err == nil {
		t.Fatal("cancelled batch committed")
	}
	if err := db.commitLatencyRollupBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := db.commitLatencyRollupBatch(ctx, batch); !errors.Is(err, ErrLatencyRollupChanged) {
		t.Fatalf("duplicate concurrent batch committed: %v", err)
	}
	saveRollupReport(t, db, node.ID, "expired", time.Now().Add(-48*time.Hour), 50, true, 1, 1)
	state, _ = db.LatencyRollupState(ctx)
	oldBatch, err := db.readLatencyRollupBatch(ctx, state, time.Now().Add(-7*24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	deleted, _, err := db.deleteLatencyRetentionBatches(ctx, `delete from server_latency_probe_results where rowid in (select rowid from server_latency_probe_results where checked_at<? limit ?)`, cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("retention delete=%d %v", deleted, err)
	}
	state, _ = db.LatencyRollupState(ctx)
	if state.ExpiredUnprocessed != 1 {
		t.Fatalf("lost coverage not recorded: %+v", state)
	}
	if err := db.commitLatencyRollupBatch(ctx, oldBatch); !errors.Is(err, ErrLatencyRollupChanged) {
		t.Fatalf("retention resurrected old batch: %v", err)
	}
	if _, _, err := db.latencyRollupRetention(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	saveRollupReport(t, db, node.ID, "late-expired", time.Now().Add(-48*time.Hour), 90, true, 1, 1)
	result, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500)
	if err != nil || result.Expired != 1 || result.Buckets != 0 {
		t.Fatalf("expired replay=%+v %v", result, err)
	}
	db.db.db.SetMaxOpenConns(1)
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
}

func TestLatencyRollupTaskDeletionInvalidatesPreparedBatch(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	startRollupTest(t, db)
	task := &model.LatencyProbeTask{Name: "task", Method: "tcp", Address: "example.net", Port: 443, Enabled: true, IntervalSeconds: 60, ServerIDs: []int64{node.ID}}
	if err := db.SaveLatencyProbeTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	report := model.LatencyProbeResultReport{ReportID: "task-1", ResourceVersion: "test", CheckedAt: time.Now().Add(-time.Hour), Items: []model.LatencyProbeResult{{ProbeID: "task", Kind: "custom", TaskID: task.ID, Mode: "tcp", Host: task.Address, Available: true, LatencyMS: 10, SampleCount: 1, SuccessCount: 1}}}
	if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	state, _ := db.LatencyRollupState(ctx)
	batch, err := db.readLatencyRollupBatch(ctx, state, time.Now().Add(-7*24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteLatencyProbeTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.commitLatencyRollupBatch(ctx, batch); !errors.Is(err, ErrLatencyRollupChanged) {
		t.Fatalf("task resurrected: %v", err)
	}
}

func TestLatencyRollupSummaryRetentionPreservesExactBoundary(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	startRollupTest(t, db)
	at := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	saveRollupReport(t, db, node.ID, "boundary", at, 10, true, 1, 1)
	if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	if n, _, err := db.latencyRollupRetention(ctx, at); err != nil || n != 0 {
		t.Fatalf("exact boundary pruned: %d %v", n, err)
	}
	state, _ := db.LatencyRollupState(ctx)
	saveRollupReport(t, db, node.ID, "pending", at.Add(time.Second), 20, true, 1, 1)
	batch, err := db.readLatencyRollupBatch(ctx, state, time.Now().Add(-7*24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	if n, _, err := db.latencyRollupRetention(ctx, at.Add(time.Nanosecond)); err != nil || n != 2 {
		t.Fatalf("partial boundary not pruned: %d %v", n, err)
	}
	if err := db.commitLatencyRollupBatch(ctx, batch); !errors.Is(err, ErrLatencyRollupChanged) {
		t.Fatalf("pruned summary recreated: %v", err)
	}
}
