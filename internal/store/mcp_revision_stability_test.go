package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestHealthReportsDoNotChurnRoutingTopologyRevision verifies the fix for the
// MCP revision-thrash: periodic Agent health reports (which run every ~20s)
// must not change servers.updated_at, so the routing-topology revision stays
// stable and plan/validate/submit do not fail with revision conflicts.
func TestHealthReportsDoNotChurnRoutingTopologyRevision(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	server := &model.Server{Name: "tokyo", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "agent_1"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	before, err := db.RoutingTopologyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	report := model.HealthReport{AgentID: "agent_1", Status: model.ServerOnline, CPUUsagePercent: 42.5, MemoryUsedBytes: 100, MemoryTotalBytes: 200, AgentVersion: "1.2.3"}
	window := model.ServerTrafficWindow{Key: "2026-08-10", Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)}
	if _, _, err := db.UpsertHealthTransition(ctx, report, window); err != nil {
		t.Fatal(err)
	}
	after, err := db.RoutingTopologyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("health report churned the routing topology revision: %s -> %s", before, after)
	}

	// Administrative edits must still advance the revision.
	current, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Name = "tokyo-renamed"
	current.UpdatedAt = time.Now().UTC()
	if err := db.UpdateServer(ctx, current); err != nil {
		t.Fatal(err)
	}
	afterEdit, err := db.RoutingTopologyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before == afterEdit {
		t.Fatal("administrative server edit did not advance the routing topology revision")
	}
}
