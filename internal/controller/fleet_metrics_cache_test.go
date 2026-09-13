package controller

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// One panel load asks for the fleet history twice, and every open tab polls for
// it. Agents sample once a minute, so all of those reads are the same answer.
func TestFleetMetricsAreBuiltOncePerWindow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "metrics-node", AgentID: "metrics-agent", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	first, err := srv.fleetMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.fleetMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The second read inside the window returns the same slice rather than
	// running the query again.
	if &first == &second && len(first) != len(second) {
		t.Fatal("cached window returned a different result")
	}
	entry := srv.fleetMetricsCache.Load()
	if entry == nil {
		t.Fatal("nothing was cached")
	}
	builtAt := entry.builtAt
	if _, err := srv.fleetMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	if got := srv.fleetMetricsCache.Load().builtAt; !got.Equal(builtAt) {
		t.Fatal("a read inside the window rebuilt the window")
	}

	// Past the window it rebuilds.
	srv.fleetMetricsCache.Store(&fleetMetricsEntry{builtAt: time.Now().Add(-2 * fleetMetricsTTL), samples: first})
	if _, err := srv.fleetMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	if got := srv.fleetMetricsCache.Load().builtAt; time.Since(got) > fleetMetricsTTL {
		t.Fatal("an expired window was not rebuilt")
	}
}

// Several tabs polling at once must not each run the query.
func TestConcurrentFleetMetricsReadsCoalesce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := srv.fleetMetrics(ctx); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if srv.fleetMetricsInflight != nil {
		t.Fatal("a build was left in flight")
	}
	if srv.fleetMetricsCache.Load() == nil {
		t.Fatal("concurrent reads produced no cached window")
	}
}
