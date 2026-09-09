package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConnectivityBaselineSeeksPreserveOrdering(t *testing.T) {
	db, server := newConnectivityTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	kinds := []model.ConnectivityEventKind{model.ConnectivityEventProbeEnabled, model.ConnectivityEventProbeDisabled, model.ConnectivityEventProbeTargetChanged}
	for i, kind := range kinds {
		if _, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: kind, EffectiveAt: at, EventKey: string(kind)}); err != nil {
			t.Fatal(err)
		}
		got, err := db.latestConnectivityEventBefore(ctx, server.ID, at.Add(time.Second), kinds)
		if err != nil || got.Kind != kinds[i] {
			t.Fatalf("latest tied event = %+v, %v", got, err)
		}
	}
	got, err := db.latestConnectivityEventBefore(ctx, server.ID, at, kinds)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if err == nil && !got.EffectiveAt.Before(at) {
		t.Fatalf("included end boundary: %+v", got)
	}
	kinds = append(kinds, model.ConnectivityEventServerOffline)
	want, err := scanConnectivityEvent(db.db.QueryRowContext(ctx, `select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and kind in (?,?,?,?) and effective_at<? order by effective_at desc,id desc limit 1`, server.ID, kinds[0], kinds[1], kinds[2], kinds[3], at.Add(time.Second).Format(time.RFC3339Nano)))
	if err != nil {
		t.Fatal(err)
	}
	got, err = db.latestConnectivityEventBefore(ctx, server.ID, at.Add(time.Second), kinds)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v (%v), want %+v", got, err, want)
	}
	if _, err := db.latestConnectivityEventBefore(ctx, server.ID, at, []model.ConnectivityEventKind{model.ConnectivityEventServerOffline}); err != sql.ErrNoRows {
		t.Fatalf("missing kind: %v", err)
	}
}

func BenchmarkConnectivityBaseline(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "baseline.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "benchmark"}
	if err := db.CreateServer(ctx, server); err != nil {
		b.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `with recursive n(x) as (select 1 union all select x+1 from n where x<200000) insert into server_connectivity_events(server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at) select ?,'probe_result',1,20,'','latency_probe',strftime('%Y-%m-%dT%H:%M:%SZ','2026-01-01','+'||x||' seconds'),'bench:'||x,'2026-01-01T00:00:00Z' from n`, server.ID); err != nil {
		b.Fatal(err)
	}
	before := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	kinds := []model.ConnectivityEventKind{model.ConnectivityEventControllerConnected, model.ConnectivityEventControllerDisconnected}
	b.Run("previous", func(b *testing.B) {
		for b.Loop() {
			_, err := scanConnectivityEvent(db.db.QueryRowContext(ctx, `select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and kind in (?,?) and effective_at<? order by effective_at desc,id desc limit 1`, server.ID, kinds[0], kinds[1], before.Format(time.RFC3339Nano)))
			if err != sql.ErrNoRows {
				b.Fatal(err)
			}
		}
	})
	b.Run("indexed_candidates", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.latestConnectivityEventBefore(ctx, server.ID, before, kinds); err != sql.ErrNoRows {
				b.Fatal(err)
			}
		}
	})
}
