package store

import (
	"context"
	"github.com/OboardProject/oboard/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeMonitorHistoryAndPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "probe.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := model.Server{Name: "monitor-node"}
	if err = db.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	// Previous releases had neither table; existing servers and probe tasks remain.
	for _, table := range []string{"probe_route_versions", "probe_monitors"} {
		if _, err = db.db.Exec("drop table " + table); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	task := model.ProbeMonitor{ServerID: server.ID, Name: "route", Kind: "route", Target: "1.1.1.1", Mode: "icmp", IntervalSeconds: 300, Enabled: true}
	if err = db.SaveProbeMonitor(ctx, server, &task); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimProbeMonitor(ctx, task, time.Now())
	if err != nil || !claimed {
		t.Fatalf("claim=%v %v", claimed, err)
	}
	if again, _ := db.ClaimProbeMonitor(ctx, task, time.Now()); again {
		t.Fatal("duplicate claim")
	}
	first := &model.ProbeRouteVersion{ASNPath: []string{"AS1", "AS2"}, Trace: model.NetworkTraceResult{Reached: true}}
	for i := 0; i < 2; i++ {
		if err = db.FinishProbeMonitor(ctx, task, "ok", "", first, first); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := db.ProbeRouteHistory(ctx, task.ID, 0)
	if err != nil || len(versions) != 1 {
		t.Fatalf("duplicates: %v %v", versions, err)
	}
	second := *first
	second.ASNPath = []string{"AS1", "AS3", "AS2"}
	if err = db.FinishProbeMonitor(ctx, task, "ok", "", second, &second); err != nil {
		t.Fatal(err)
	}
	if err = db.FinishProbeMonitor(ctx, task, "unknown", "timeout", nil, nil); err != nil {
		t.Fatal(err)
	}
	versions, _ = db.ProbeRouteHistory(ctx, task.ID, 0)
	if len(versions) != 2 {
		t.Fatalf("wanted old and new path, got %d", len(versions))
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	versions, err = db.ProbeRouteHistory(ctx, task.ID, 0)
	if err != nil || len(versions) != 2 {
		t.Fatalf("reopen lost history: %v %v", versions, err)
	}
	if current, err := db.GetServer(ctx, server.ID); err != nil || current.Name != server.Name {
		t.Fatalf("migration changed existing server: %v %v", current, err)
	}
	old := task
	task.Name = "renamed"
	if err = db.SaveProbeMonitor(ctx, server, &task); err != nil {
		t.Fatal(err)
	}
	if err = db.FinishProbeMonitor(ctx, old, "ok", "", first, first); err != nil {
		t.Fatal(err)
	}
	versions, _ = db.ProbeRouteHistory(ctx, task.ID, 0)
	if len(versions) != 2 {
		t.Fatal("stale result retained")
	}
	if err = db.MutateProbeMonitor(ctx, task.ID, old.Revision, "delete"); err == nil {
		t.Fatal("stale deletion accepted")
	}
	if err = db.MutateProbeMonitor(ctx, task.ID, task.Revision, "delete"); err != nil {
		t.Fatal(err)
	}
	versions, _ = db.ProbeRouteHistory(ctx, task.ID, 0)
	if len(versions) != 0 {
		t.Fatal("orphan history")
	}
	replacement := server
	replacement.AgentID = "replacement"
	task.ID = 0
	if err = db.SaveProbeMonitor(ctx, replacement, &task); err == nil {
		t.Fatal("stale server identity accepted")
	}
}
