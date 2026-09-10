package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// TestProbePlanCacheServesSteadyHeartbeatsWithoutQueries proves a stable server
// stops querying its probe tasks and re-rendering its target list on every
// heartbeat.
func TestProbePlanCacheServesSteadyHeartbeatsWithoutQueries(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	server := createHotPathDedupServer(t, db, "plan-node", "plan-agent")

	first, err := srv.cachedLatencyProbePlanForServer(ctx, *server)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version <= 0 {
		t.Fatalf("plan version must be positive, got %d", first.Version)
	}
	baseline := db.SQLStatementCount()
	for range 200 {
		plan, err := srv.cachedLatencyProbePlanForServer(ctx, *server)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Version != first.Version {
			t.Fatalf("steady plan version changed: %d -> %d", first.Version, plan.Version)
		}
	}
	if statements := db.SQLStatementCount() - baseline; statements != 0 {
		t.Fatalf("steady heartbeats issued %d SQL statements for an unchanged plan", statements)
	}
	if hits := srv.hotPath.probePlanHit.Load(); hits != 200 {
		t.Fatalf("expected 200 cache hits, got %d", hits)
	}
	if rebuilt := srv.hotPath.probePlanRebuilt.Load(); rebuilt != 1 {
		t.Fatalf("expected exactly one rebuild, got %d", rebuilt)
	}
}

// TestProbePlanCacheIgnoresIrrelevantServerChanges proves health samples,
// traffic counters and unrelated administrative metadata do not rebuild a plan.
func TestProbePlanCacheIgnoresIrrelevantServerChanges(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	server := createHotPathDedupServer(t, db, "irrelevant-node", "irrelevant-agent")
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, *server); err != nil {
		t.Fatal(err)
	}

	noisy := *server
	noisy.CPUUsagePercent = 91
	noisy.MemoryUsedBytes = 7 << 30
	noisy.TCPConnectionCount = 4096
	noisy.UpdatedAt = noisy.UpdatedAt.Add(time.Hour)
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, noisy); err != nil {
		t.Fatal(err)
	}
	if rebuilt := srv.hotPath.probePlanRebuilt.Load(); rebuilt != 1 {
		t.Fatalf("irrelevant server changes rebuilt the plan, rebuilds=%d", rebuilt)
	}

	// A probe-relevant setting still rebuilds.
	changed := *server
	changed.LatencyProbeSampleCount = 5
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if rebuilt := srv.hotPath.probePlanRebuilt.Load(); rebuilt != 2 {
		t.Fatalf("probe setting change did not rebuild, rebuilds=%d", rebuilt)
	}
}

// TestProbePlanInvalidationIsScopedToAssignedServers proves editing one node's
// probe task leaves every unrelated node's cached plan in place.
func TestProbePlanInvalidationIsScopedToAssignedServers(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	owner := createHotPathDedupServer(t, db, "owner-node", "owner-agent")
	bystander := createHotPathDedupServer(t, db, "bystander-node", "bystander-agent")
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, *owner); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, *bystander); err != nil {
		t.Fatal(err)
	}
	rebuilds := srv.hotPath.probePlanRebuilt.Load()

	srv.invalidateLatencyProbePlans(owner.ID)
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, *bystander); err != nil {
		t.Fatal(err)
	}
	if got := srv.hotPath.probePlanRebuilt.Load(); got != rebuilds {
		t.Fatalf("an unrelated node's plan was rebuilt, rebuilds %d -> %d", rebuilds, got)
	}
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, *owner); err != nil {
		t.Fatal(err)
	}
	if got := srv.hotPath.probePlanRebuilt.Load(); got != rebuilds+1 {
		t.Fatalf("the assigned node's plan was not rebuilt, rebuilds %d -> %d", rebuilds, got)
	}
}

// TestProbePlanVersionNeverRegresses covers the cases a timestamp maximum gets
// wrong: deleting the most recently updated task, unassigning it, and a
// Controller restart that starts from an empty cache. It also proves one
// version never describes two different plans.
func TestProbePlanVersionNeverRegresses(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	server := createHotPathDedupServer(t, db, "version-node", "version-agent")

	base, err := srv.cachedLatencyProbePlanForServer(ctx, *server)
	if err != nil {
		t.Fatal(err)
	}

	task := model.LatencyProbeTask{Name: "自定义", Method: model.LatencyProbeModeTCP, Address: "example.com", Port: 8443, Enabled: true, ServerIDs: []int64{server.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	srv.invalidateLatencyProbePlans(task.ServerIDs...)
	withTask, err := srv.cachedLatencyProbePlanForServer(ctx, *server)
	if err != nil {
		t.Fatal(err)
	}
	if withTask.Version <= base.Version {
		t.Fatalf("adding a task did not advance the version: %d -> %d", base.Version, withTask.Version)
	}
	if len(withTask.Targets) != 2 {
		t.Fatalf("expected the custom target in the plan, got %d targets", len(withTask.Targets))
	}

	// Deleting the most recently updated task shrinks the timestamp set the old
	// version was the maximum of; the issued version must still advance.
	if err := db.DeleteLatencyProbeTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	srv.invalidateLatencyProbePlans(server.ID)
	afterDelete, err := srv.cachedLatencyProbePlanForServer(ctx, *server)
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.Version <= withTask.Version {
		t.Fatalf("deleting the newest task regressed the version: %d -> %d", withTask.Version, afterDelete.Version)
	}
	if len(afterDelete.Targets) != 1 {
		t.Fatalf("deleted task still in plan: %d targets", len(afterDelete.Targets))
	}

	// A Controller restart starts from an empty cache and must not hand out an
	// older version than the Agent already holds.
	restarted := newTestServer(db, "hotpath-secret", "")
	afterRestart, err := restarted.cachedLatencyProbePlanForServer(ctx, *server)
	if err != nil {
		t.Fatal(err)
	}
	if afterRestart.Version != afterDelete.Version {
		t.Fatalf("restart changed the version of unchanged content: %d -> %d", afterDelete.Version, afterRestart.Version)
	}

	// A detected region change alters the rendered public target without moving
	// any input timestamp; the version must still advance.
	regional := *server
	regional.DetectedRegionCode = "CN"
	regionPlan, err := restarted.cachedLatencyProbePlanForServer(ctx, regional)
	if err != nil {
		t.Fatal(err)
	}
	if regionPlan.Targets[0].Host != "www.12306.cn" {
		t.Fatalf("region change did not alter the public target: %s", regionPlan.Targets[0].Host)
	}
	if regionPlan.Version <= afterRestart.Version {
		t.Fatalf("region-driven content change did not advance the version: %d -> %d", afterRestart.Version, regionPlan.Version)
	}
}

// TestProbePlanResourceFailureKeepsLastValidPlan proves a probe resource outage
// neither clears the last valid plan nor fails the control channel.
func TestProbePlanResourceFailureKeepsLastValidPlan(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	server := createHotPathDedupServer(t, db, "resource-node", "resource-agent")
	task := model.LatencyProbeTask{Name: "广东 · 中国电信", Province: "广东", Carrier: "中国电信", Method: model.LatencyProbeModeTCP, Port: 80, Enabled: true, ServerIDs: []int64{server.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}

	resetLatencyProbeCacheForTest()
	latencyProbeResourceFetcher = func(context.Context) (latencyProbeResource, error) {
		return parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["203.0.113.10"]}}`), time.Now().UTC())
	}
	t.Cleanup(func() {
		latencyProbeResourceFetcher = fetchLatencyProbeResource
		resetLatencyProbeCacheForTest()
	})
	good, err := srv.cachedLatencyProbePlanForServer(ctx, *server)
	if err != nil {
		t.Fatal(err)
	}
	if len(good.Targets) != 2 {
		t.Fatalf("expected the regional target in the plan, got %d", len(good.Targets))
	}

	// The resource goes away entirely: the cached plan must survive.
	resetLatencyProbeCacheForTest()
	latencyProbeResourceFetcher = func(context.Context) (latencyProbeResource, error) {
		return latencyProbeResource{}, errors.New("probe resource offline")
	}
	srv.invalidateLatencyProbePlans(server.ID)
	degraded, err := srv.cachedLatencyProbePlanForServer(ctx, *server)
	if err == nil && len(degraded.Targets) != 2 {
		t.Fatalf("resource outage dropped the last valid plan: %+v", degraded)
	}
	if err != nil {
		t.Fatalf("resource outage must not fail plan delivery: %v", err)
	}
}

// TestProbePlanRebuildDoesNotMaskConcurrentInvalidation proves a task change
// that lands while a rebuild is in flight is never hidden by the older result.
func TestProbePlanRebuildDoesNotMaskConcurrentInvalidation(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	server := createHotPathDedupServer(t, db, "concurrent-node", "concurrent-agent")

	generation := srv.latencyProbePlans.generation(server.ID)
	plan, err := srv.latencyProbePlanForServer(ctx, *server)
	if err != nil {
		t.Fatal(err)
	}
	// The invalidation happens after the rebuild read its inputs but before it
	// publishes, which is exactly the window a naive cache loses.
	srv.invalidateLatencyProbePlans(server.ID)
	srv.latencyProbePlans.store(server.ID, latencyProbePlanCacheKey(*server, generation, "public", time.Time{}), generation, plan)

	rebuilds := srv.hotPath.probePlanRebuilt.Load()
	if _, err := srv.cachedLatencyProbePlanForServer(ctx, *server); err != nil {
		t.Fatal(err)
	}
	if got := srv.hotPath.probePlanRebuilt.Load(); got != rebuilds+1 {
		t.Fatalf("stale rebuild masked a concurrent invalidation, rebuilds %d -> %d", rebuilds, got)
	}
}

// TestProbePlanCacheConcurrentAccess exercises the cache under -race with
// simultaneous readers, invalidations and deletions.
func TestProbePlanCacheConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	servers := make([]*model.Server, 0, 4)
	for i := range 4 {
		servers = append(servers, createHotPathDedupServer(t, db, "race-node-"+string(rune('a'+i)), "race-agent-"+string(rune('a'+i))))
	}
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(2)
		go func(server model.Server) {
			defer wg.Done()
			for range 50 {
				if _, err := srv.cachedLatencyProbePlanForServer(ctx, server); err != nil {
					t.Error(err)
					return
				}
			}
		}(*server)
		go func(id int64) {
			defer wg.Done()
			for range 50 {
				srv.invalidateLatencyProbePlans(id)
			}
		}(server.ID)
	}
	wg.Wait()
	srv.forgetLatencyProbePlan(servers[0].ID)
	srv.latencyProbePlans.mu.Lock()
	_, entry := srv.latencyProbePlans.entries[servers[0].ID]
	_, generation := srv.latencyProbePlans.generations[servers[0].ID]
	srv.latencyProbePlans.mu.Unlock()
	if entry || generation {
		t.Fatal("a forgotten server left plan cache state behind")
	}
}
