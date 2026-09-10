package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func fakeSLAProjection(work SLAProjectionWork) (SLAProjectionOutput, error) {
	output := SLAProjectionOutput{Seed: json.RawMessage(`{"version":1}`), CoverageSeed: json.RawMessage(`{"version":1}`)}
	for at := work.From; at < work.To; at += 300 {
		output.Buckets = append(output.Buckets, SLAProjectionBucket{Start: at, Stats: model.ConnectivitySLAStats{DurationNS: int64(5 * time.Minute), UnknownNS: int64(5 * time.Minute)}, Checkpoint: json.RawMessage(`{"version":1}`)})
	}
	return output, nil
}

func TestSLAProjectionAtomicRetryRestartAndDeletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sla.sqlite")
	db, node := newConnectivityTestStoreAtPath(t, path)
	base := time.Now().UTC().Truncate(5 * time.Minute)
	if _, err := db.RunSLAProjectionBatch(ctx, base, 500, fakeSLAProjection); err != nil {
		t.Fatal(err)
	}
	if _, err := db.scanSLAProjectionEvents(ctx, base.Add(time.Hour), 500); err != nil {
		t.Fatal(err)
	}
	work, err := db.prepareSLAProjectionWork(ctx, base.Add(time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	output, _ := fakeSLAProjection(work)
	if _, err := db.db.Exec(`update sla_projection_state set algorithm_version=2 where id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.commitSLAProjection(ctx, work, output, base.Add(time.Hour).Unix()); !errors.Is(err, ErrSLAProjectionChanged) {
		t.Fatalf("changed algorithm accepted: %v", err)
	}
	if _, err := db.db.Exec(`update sla_projection_state set algorithm_version=1 where id=1`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.db.Exec(`create trigger fail_sla before insert on sla_projection_buckets begin select raise(abort,'simulated failure'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.commitSLAProjection(ctx, work, output, base.Add(time.Hour).Unix()); err == nil {
		t.Fatal("expected rollback")
	}
	status, err := db.InspectSLAProjection(ctx, node.ID)
	if err != nil || status.Frontier != base.Unix() {
		t.Fatalf("frontier advanced: %+v %v", status, err)
	}
	if _, err := db.db.Exec(`drop trigger fail_sla`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.commitSLAProjection(ctx, work, output, base.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.commitSLAProjection(ctx, work, output, base.Add(time.Hour).Unix()); !errors.Is(err, ErrSLAProjectionChanged) {
		t.Fatalf("stale batch accepted: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err = reopened.InspectSLAProjection(ctx, node.ID)
	if err != nil || status.Frontier != base.Add(time.Hour).Unix() {
		t.Fatalf("restart=%+v %v", status, err)
	}
	work, err = reopened.prepareSLAProjectionWork(ctx, base.Add(2*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	output, _ = fakeSLAProjection(work)
	if err := reopened.DeleteServer(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.commitSLAProjection(ctx, work, output, base.Add(2*time.Hour).Unix()); !errors.Is(err, ErrSLAProjectionChanged) {
		t.Fatalf("deleted server revived: %v", err)
	}
	var count int
	if err := reopened.db.QueryRow(`select count(*) from sla_projection_buckets`).Scan(&count); err != nil || count != 0 {
		t.Fatal("server bucket cascade failed")
	}
}

func TestSLAProjectionUpgradeAndRetentionGuards(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, node := newConnectivityTestStoreAtPath(t, path)
	for _, query := range []string{`drop table sla_projection_buckets`, `drop table sla_projection_servers`, `drop table sla_projection_state`} {
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
	state, err := upgraded.readSLAProjectionState(ctx)
	if err != nil || state.cursor != -1 {
		t.Fatalf("startup replayed events: %+v %v", state, err)
	}
	base := time.Now().UTC().Truncate(5 * time.Minute)
	if _, err := upgraded.RunSLAProjectionBatch(ctx, base, 500, fakeSLAProjection); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.scanSLAProjectionEvents(ctx, base, 500); err != nil {
		t.Fatal(err)
	}
	work, err := upgraded.prepareSLAProjectionWork(ctx, base.Add(time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	output, _ := fakeSLAProjection(work)
	if err := upgraded.SetSetting(ctx, ServerMonitoringRetentionDaysSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.commitSLAProjection(ctx, work, output, base.Add(time.Hour).Unix()); !errors.Is(err, ErrSLAProjectionChanged) {
		t.Fatalf("changed policy accepted: %v", err)
	}
	if err := upgraded.SetSetting(ctx, ServerMonitoringRetentionDaysSetting, "7"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := upgraded.purgeSLAProjection(ctx, base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.commitSLAProjection(ctx, work, output, base.Add(time.Hour).Unix()); !errors.Is(err, ErrSLAProjectionChanged) {
		t.Fatalf("retention generation ignored: %v", err)
	}
	upgraded.db.db.SetMaxOpenConns(1)
	if _, err := upgraded.RunSLAProjectionBatch(ctx, base.Add(time.Hour), 500, fakeSLAProjection); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.InspectSLAProjection(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSLAProjectionDenseBucketStopsWithoutTruncating(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(5 * time.Minute)
	if _, err := db.RunSLAProjectionBatch(ctx, base, 500, fakeSLAProjection); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := db.RecordControllerConnectionEvent(ctx, node.ID, i%2 == 0, base.Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.scanSLAProjectionEvents(ctx, base.Add(time.Hour), 500); err != nil {
		t.Fatal(err)
	}
	if _, err := db.prepareSLAProjectionWork(ctx, base.Add(time.Hour), 2); !errors.Is(err, ErrSLAProjectionDensity) {
		t.Fatalf("dense bucket truncated: %v", err)
	}
	status, err := db.InspectSLAProjection(ctx, node.ID)
	if err != nil || status.Phase != "unavailable" || status.Frontier != base.Unix() {
		t.Fatalf("density=%+v %v", status, err)
	}
}

func TestConnectivityRetentionPreservesNewLateEventsDuringPrune(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	cutoff := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	previous := maintenanceBatchSize
	maintenanceBatchSize = 1
	defer func() { maintenanceBatchSize = previous }()
	for i, at := range []time.Time{cutoff.Add(-2 * time.Hour), cutoff.Add(-time.Hour)} {
		if _, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: node.ID, Kind: model.ConnectivityEventProbeResult, EffectiveAt: at, EventKey: fmt.Sprintf("old-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	var oldest int64
	if err := db.db.QueryRow(`select id from server_connectivity_events where event_key='old-0'`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf(`create trigger append_late before delete on server_connectivity_events when old.id=%d begin insert into server_connectivity_events(server_id,kind,effective_at,event_key,created_at) values(%d,'probe_result','%s','late-during-prune','%s'); end`, oldest, node.ID, cutoff.Add(-30*time.Minute).Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano))
	if _, err := db.db.Exec(query); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.pruneExpiredConnectivityEvents(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow(`select count(*) from server_connectivity_events where event_key='late-during-prune'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("new late baseline deleted by an older retention snapshot")
	}
}

func TestSLAProjectionDoesNotBackfillBeforeServerCreation(t *testing.T) {
	db, node := newConnectivityTestStore(t)
	ctx := context.Background()
	started := time.Now().UTC().Truncate(5 * time.Minute).Add(-24 * time.Hour)
	if _, err := db.RunSLAProjectionBatch(ctx, started, 500, fakeSLAProjection); err != nil {
		t.Fatal(err)
	}
	if _, err := db.scanSLAProjectionEvents(ctx, time.Now(), 500); err != nil {
		t.Fatal(err)
	}
	status, err := db.InspectSLAProjection(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := node.CreatedAt.UTC().Truncate(5 * time.Minute).Unix()
	if status.CoverageFrom != want || status.Frontier != want {
		t.Fatalf("enrolled before creation: %+v want %d", status, want)
	}
}
