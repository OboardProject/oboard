package controller

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestSLADenseBucketResumesAcrossRestartAndLateEvents(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "dense.sqlite")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			node := &model.Server{Name: "dense"}
			if err := db.CreateServer(ctx, node); err != nil {
				t.Fatal(err)
			}
			base := time.Now().UTC().Truncate(5 * time.Minute).Add(5 * time.Minute)
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			tx, err := raw.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			statement, err := tx.Prepare(`insert into server_connectivity_events(server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at) values(?,?,1,10,'','test',?,?,?)`)
			if err != nil {
				t.Fatal(err)
			}
			kinds := []model.ConnectivityEventKind{model.ConnectivityEventControllerConnected, model.ConnectivityEventProbeEnabled, model.ConnectivityEventControllerDisconnected, model.ConnectivityEventProbeResult, model.ConnectivityEventProbeDisabled, model.ConnectivityEventProbeTargetChanged, model.ConnectivityEventServerOffline}
			offsets := []time.Duration{0, 100 * time.Millisecond, 100*time.Millisecond + time.Nanosecond, 110 * time.Millisecond}
			for i := 0; i < 2100; i++ {
				at := base.Add(time.Second)
				if i >= 1400 {
					at = at.Add(offsets[i%len(offsets)])
				}
				stamp := at.Format(time.RFC3339Nano)
				if _, err := statement.Exec(node.ID, kinds[i%len(kinds)], stamp, fmt.Sprintf("dense-%d", i), stamp); err != nil {
					t.Fatal(err)
				}
			}
			statement.Close()
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := db.RunSLAProjectionBatch(ctx, base, 500, buildSLAProjection); err != nil {
				t.Fatal(err)
			}
			result, err := db.RunSLAProjectionBatch(ctx, base.Add(5*time.Minute), 73, buildSLAProjection)
			if err != nil || result.Buckets != 0 || result.Events > 73 {
				t.Fatalf("first=%+v %v", result, err)
			}
			var count int
			if err := raw.QueryRow(`select count(*) from sla_projection_partial`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("partial not persisted %d %v", count, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if late {
				if err := db.RecordControllerConnectionEvent(ctx, node.ID, true, base.Add(time.Nanosecond)); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 80; i++ {
				result, err = db.RunSLAProjectionBatch(ctx, base.Add(5*time.Minute), 73, buildSLAProjection)
				if err != nil || result.Events > 73 {
					t.Fatalf("batch=%+v %v", result, err)
				}
				if result.Buckets == 1 {
					break
				}
				if i == 79 {
					t.Fatal("dense bucket did not finish")
				}
			}
			history, err := db.ListConnectivitySLAHistory(ctx, node.ID, base, base.Add(5*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := buildSLAProjection(store.SLAProjectionWork{From: base.Unix(), To: base.Add(5 * time.Minute).Unix(), Baseline: history.Baseline, Events: history.Events})
			if err != nil {
				t.Fatal(err)
			}
			read, err := db.QuerySLAHistory(ctx, node.ID, base, base.Add(5*time.Minute), 5*time.Minute, buildSLAProjection)
			if err != nil {
				t.Fatal(err)
			}
			if read.Source != "summary" || !reflect.DeepEqual(expected.Buckets, read.Parts) {
				t.Fatalf("replay differs:\nexpected=%+v\nactual=%+v", expected.Buckets, read.Parts)
			}
			if err := raw.QueryRow(`select count(*) from sla_projection_partial`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("completed partial remains %d %v", count, err)
			}
		})
	}
}

func BenchmarkSLADenseContinuation(b *testing.B) {
	if b.N != 1 {
		b.Skip("use -benchtime=1x for a complete dense bucket")
	}
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "dense.sqlite")
	db, err := store.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "dense-benchmark"}
	if err := db.CreateServer(ctx, node); err != nil {
		b.Fatal(err)
	}
	base := time.Now().UTC().Truncate(5 * time.Minute).Add(5 * time.Minute)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		b.Fatal(err)
	}
	defer raw.Close()
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`insert into server_connectivity_events(server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at) values(?,?,?,10,'','benchmark',?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		at := base.Add(time.Second + time.Duration(i)*time.Nanosecond).Format(time.RFC3339Nano)
		if _, err := stmt.Exec(node.ID, model.ConnectivityEventProbeResult, i%2, at, fmt.Sprintf("bench-%d", i), at); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := db.RunSLAProjectionBatch(ctx, base, 500, buildSLAProjection); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	batches := 0
	var slowest time.Duration
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		started := time.Now()
		result, err := db.RunSLAProjectionBatch(batchCtx, base.Add(5*time.Minute), 500, buildSLAProjection)
		elapsed := time.Since(started)
		cancel()
		if err != nil {
			b.Fatal(err)
		}
		if elapsed > slowest {
			slowest = elapsed
		}
		batches++
		if result.Events > 500 {
			b.Fatalf("unbounded batch: %+v", result)
		}
		if result.Buckets == 1 {
			break
		}
		if batches > 100 {
			b.Fatal("no progress")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(batches), "batches")
	b.ReportMetric(float64(slowest.Nanoseconds()), "max_batch_ns")
}

func TestSLADenseConcurrentReadWrite(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "concurrent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "concurrent"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(5 * time.Minute).Add(5 * time.Minute)
	end := base.Add(5 * time.Minute)
	if _, err := db.RunSLAProjectionBatch(ctx, base, 500, buildSLAProjection); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 700; i++ {
		if err := db.RecordControllerConnectionEvent(ctx, node.ID, i%2 == 0, base.Add(time.Duration(i+1)*time.Nanosecond)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := db.RecordControllerConnectionEvent(ctx, node.ID, i%2 == 0, base.Add(time.Second+time.Duration(i)*time.Nanosecond)); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			if _, err := db.QuerySLAHistory(ctx, node.ID, base, end, 5*time.Minute, buildSLAProjection); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			if _, err := db.RunSLAProjectionBatch(ctx, end, 73, buildSLAProjection); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := db.RunSLAProjectionBatch(ctx, end, 73, buildSLAProjection); err != nil {
			t.Fatal(err)
		}
		read, err := db.QuerySLAHistory(ctx, node.ID, base, end, 5*time.Minute, buildSLAProjection)
		if err != nil {
			t.Fatal(err)
		}
		if read.Source != "summary" {
			continue
		}
		history, err := db.ListConnectivitySLAHistory(ctx, node.ID, base, end)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := buildSLAProjection(store.SLAProjectionWork{From: base.Unix(), To: end.Unix(), Baseline: history.Baseline, Events: history.Events})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(expected.Buckets, read.Parts) {
			t.Fatal("concurrent projection differs from raw replay")
		}
		return
	}
	t.Fatal("concurrent projection did not converge")
}
