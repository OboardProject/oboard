package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// latencyProbePlanCache holds one immutable rendered probe plan per server.
//
// The cache key is the set of inputs the plan is actually derived from: the
// server's own probe-relevant settings and region resolution, a per-server
// generation counter bumped when that server's probe tasks or task assignment
// change, and the probe resource version. Unrelated server edits (an
// administrative note, a display tag), health samples and traffic updates do
// not appear in the key, so they never rebuild a plan; a task edit bumps only
// the generation of the servers that task was or is assigned to, so unrelated
// nodes keep their cached plan.
type latencyProbePlanCache struct {
	mu          sync.Mutex
	entries     map[int64]latencyProbePlanEntry
	generations map[int64]uint64
}

type latencyProbePlanEntry struct {
	key  string
	plan model.LatencyProbeTargetsPlan
}

func (c *latencyProbePlanCache) generation(serverID int64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generations[serverID]
}

func (c *latencyProbePlanCache) lookup(serverID int64, key string) (model.LatencyProbeTargetsPlan, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[serverID]
	if !ok || entry.key != key {
		return model.LatencyProbeTargetsPlan{}, false
	}
	return entry.plan, true
}

// last returns the most recent plan rendered for one server regardless of key.
// It backs the rule that a probe resource failure must not drop the last valid
// plan.
func (c *latencyProbePlanCache) last(serverID int64) (model.LatencyProbeTargetsPlan, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[serverID]
	return entry.plan, ok
}

// store publishes a rebuilt plan only when the generation captured at the start
// of the rebuild is still current, so a task change concurrent with a rebuild
// cannot be masked by the older result.
func (c *latencyProbePlanCache) store(serverID int64, key string, generation uint64, plan model.LatencyProbeTargetsPlan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generations[serverID] != generation {
		return
	}
	if c.entries == nil {
		c.entries = map[int64]latencyProbePlanEntry{}
	}
	c.entries[serverID] = latencyProbePlanEntry{key: key, plan: plan}
}

func (c *latencyProbePlanCache) invalidate(serverIDs ...int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generations == nil {
		c.generations = map[int64]uint64{}
	}
	bumped := 0
	for _, serverID := range serverIDs {
		if serverID <= 0 {
			continue
		}
		c.generations[serverID]++
		// The entry itself is kept: its key no longer matches, so the next
		// request rebuilds, and until that rebuild succeeds it remains the last
		// valid plan a probe resource outage must not discard.
		bumped++
	}
	return bumped
}

func (c *latencyProbePlanCache) forget(serverID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, serverID)
	delete(c.generations, serverID)
}

// invalidateLatencyProbePlans marks the given servers as needing a fresh probe
// plan. Callers pass the union of the servers a task was assigned to before and
// after the change, so an unassignment invalidates the node that lost the task
// as well as the node that gained it.
func (s *Server) invalidateLatencyProbePlans(serverIDs ...int64) {
	if bumped := s.latencyProbePlans.invalidate(serverIDs...); bumped > 0 {
		s.hotPath.probePlanInvalidated.Add(int64(bumped))
	}
}

// forgetLatencyProbePlan removes every trace of one server from the cache.
func (s *Server) forgetLatencyProbePlan(serverID int64) {
	s.latencyProbePlans.forget(serverID)
}

// latencyProbePlanServerKey renders the probe-relevant identity of one server.
// Only fields the rendered plan actually depends on are included.
func latencyProbePlanServerKey(server model.Server) string {
	region, _ := core.EffectiveServerRegion(server)
	parts := []string{
		strconv.FormatBool(server.LatencyProbeEnabled),
		string(server.LatencyProbeMode),
		string(server.LatencyProbePublicTarget),
		strconv.Itoa(server.LatencyProbeIntervalSeconds),
		strconv.Itoa(server.LatencyProbeSampleCount),
		strconv.Itoa(server.LatencyProbeMaxTargets),
		strings.TrimSpace(server.LatencyProbeResourceVersion),
		string(server.IPStack),
		strconv.FormatBool(server.PublicIPv4 != ""),
		strconv.FormatBool(server.PublicIPv6 != ""),
		region,
	}
	return strings.Join(parts, "\x1f")
}

func latencyProbePlanCacheKey(server model.Server, generation uint64, resourceVersion string, resourceUpdatedAt time.Time) string {
	return strings.Join([]string{
		latencyProbePlanServerKey(server),
		strconv.FormatUint(generation, 10),
		resourceVersion,
		strconv.FormatInt(resourceUpdatedAt.UnixNano(), 10),
	}, "\x1e")
}

// latencyProbePlanDigest is the content identity of a rendered plan. It covers
// everything an Agent compares except the version itself, so one version can
// never describe two different plans.
func latencyProbePlanDigest(plan model.LatencyProbeTargetsPlan) string {
	plan.Version = 0
	raw, err := json.Marshal(plan)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// latencyProbeResourceState reports the currently cached probe resource without
// triggering a fetch, plus whether that cache is due for a refresh. A cache hit
// must still notice that the shared resource went stale, and it must do so
// without a database query or an HTTP request per heartbeat.
func latencyProbeResourceState() (version string, updatedAt time.Time, refreshDue bool) {
	latencyProbeCache.Lock()
	defer latencyProbeCache.Unlock()
	version = latencyProbeCache.resource.Version
	updatedAt = latencyProbeCache.resource.UpdatedAt
	if len(latencyProbeCache.resource.Provinces) == 0 {
		return version, updatedAt, true
	}
	return version, updatedAt, time.Since(latencyProbeCache.fetched) >= latencyProbeCacheTTL
}

// cachedLatencyProbePlanForServer returns the plan for one server, rebuilding
// it only when one of its real inputs changed.
func (s *Server) cachedLatencyProbePlanForServer(ctx context.Context, server model.Server) (model.LatencyProbeTargetsPlan, error) {
	generation := s.latencyProbePlans.generation(server.ID)
	resourceVersion, resourceUpdatedAt, resourceRefreshDue := latencyProbeResourceState()
	key := latencyProbePlanCacheKey(server, generation, resourceVersion, resourceUpdatedAt)
	if plan, ok := s.latencyProbePlans.lookup(server.ID, key); ok {
		// A plan that does not use the shared regional resource is unaffected by
		// its refresh cadence; forcing a rebuild for it would defeat the cache.
		if !resourceRefreshDue || plan.ResourceVersion == "public" {
			s.hotPath.probePlanHit.Add(1)
			return plan, nil
		}
		s.hotPath.probePlanResourceRefresh.Add(1)
	}
	plan, err := s.latencyProbePlanForServer(ctx, server)
	if err != nil {
		s.hotPath.probePlanFailed.Add(1)
		// A probe resource that could not be refreshed must not drop the last
		// valid plan; the Agent keeps running the plan it already has.
		if last, ok := s.latencyProbePlans.last(server.ID); ok {
			s.hotPath.probePlanStaleServed.Add(1)
			return last, nil
		}
		return model.LatencyProbeTargetsPlan{}, err
	}
	s.hotPath.probePlanRebuilt.Add(1)
	// Re-read the resource identity: the rebuild may have refreshed it, and the
	// key must describe the inputs the plan was actually rendered from.
	freshVersion, freshUpdatedAt, _ := latencyProbeResourceState()
	if plan.ResourceVersion == "public" {
		freshVersion, freshUpdatedAt = resourceVersion, resourceUpdatedAt
	}
	s.latencyProbePlans.store(server.ID, latencyProbePlanCacheKey(server, generation, freshVersion, freshUpdatedAt), generation, plan)
	return plan, nil
}
