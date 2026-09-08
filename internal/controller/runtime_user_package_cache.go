package controller

import (
	"context"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	runtimeUserPackageTTL         = 30 * time.Second
	runtimeUserPackageMaxEntries  = 512
	runtimeUserPackageMaxBuilds   = 4
	runtimeUserPackageBuildBudget = coalesceBuildTimeout
)

type runtimeUserPackageValue struct {
	pkg             core.RuntimeUserPackage
	routingRevision uint64
}

type runtimeUserPackageCache struct {
	once   sync.Once
	cache  *coalesceCache[int64, runtimeUserPackageValue]
	genMu  sync.Mutex
	routingRevision uint64
	policyRevision  uint64
	generation      uint64
}

func (c *runtimeUserPackageCache) init() {
	c.once.Do(func() {
		c.cache = newCoalesceCache[int64, runtimeUserPackageValue](runtimeUserPackageTTL, runtimeUserPackageMaxEntries, runtimeUserPackageMaxBuilds)
		c.cache.maxStale = 0 // enforcement path: never serve expired packages
	})
}

func cloneRuntimeUserPackage(pkg core.RuntimeUserPackage) core.RuntimeUserPackage {
	out := pkg
	if pkg.Scope != nil {
		out.Scope = append([]string(nil), pkg.Scope...)
	}
	if pkg.Entries != nil {
		out.Entries = append([]model.UsersInstallEntry(nil), pkg.Entries...)
	}
	if pkg.Chunk != nil {
		chunk := *pkg.Chunk
		out.Chunk = &chunk
	}
	return out
}

// Building a package regenerates the whole server configuration, which a
// snapshot pull, the periodic recovery scan, and every wake of the sync worker
// each used to pay for separately. Concurrent misses for one server coalesce
// into a single build that runs outside the cache management lock, so a slow
// build on one server cannot block a cache hit for another.
//
// Routing and quota-policy edits invalidate entries immediately, so only
// time-driven access transitions can be up to the TTL stale here; the
// authorization lease still evaluates those deadlines per renewal and remains
// the enforcement boundary.
func (s *Server) currentRuntimeUserPackage(ctx context.Context, server model.Server, revision int64) (core.RuntimeUserPackage, uint64, error) {
	c := &s.runtimeUserPackages
	c.init()

	routingRevision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return core.RuntimeUserPackage{}, 0, err
	}
	policyRevision, err := s.store.TrafficPolicyRevision(ctx)
	if err != nil {
		return core.RuntimeUserPackage{}, 0, err
	}

	generation := c.observeRevisions(routingRevision, policyRevision)
	entry, err := c.cache.getOrBuild(ctx, server.ID, generation, func(buildCtx context.Context) (runtimeUserPackageValue, time.Time, error) {
		dataAsOf := time.Now().UTC()
		pkg, buildErr := s.buildRuntimeUserPackage(buildCtx, server, revision)
		if buildErr != nil {
			return runtimeUserPackageValue{}, time.Time{}, buildErr
		}
		// Re-check revisions after the expensive build so a superseded result
		// is not published as current.
		currentRouting, revErr := s.store.RoutingCacheRevision(buildCtx)
		if revErr != nil {
			return runtimeUserPackageValue{}, time.Time{}, revErr
		}
		currentPolicy, revErr := s.store.TrafficPolicyRevision(buildCtx)
		if revErr != nil {
			return runtimeUserPackageValue{}, time.Time{}, revErr
		}
		if currentRouting != routingRevision || currentPolicy != policyRevision {
			// Still return the value to waiters that started under the old
			// revisions; finish() will refuse to overwrite a newer generation.
			return runtimeUserPackageValue{pkg: cloneRuntimeUserPackage(pkg), routingRevision: currentRouting}, dataAsOf, nil
		}
		return runtimeUserPackageValue{pkg: cloneRuntimeUserPackage(pkg), routingRevision: routingRevision}, dataAsOf, nil
	})
	if err != nil {
		return core.RuntimeUserPackage{}, 0, err
	}
	pkg := cloneRuntimeUserPackage(entry.value.pkg)
	pkg.UsersRevision = revision
	pkg.UsersDigest, err = core.UsersDigest(revision, pkg.Scope, pkg.Entries)
	return pkg, entry.value.routingRevision, err
}

func (c *runtimeUserPackageCache) observeRevisions(routingRevision, policyRevision uint64) uint64 {
	c.genMu.Lock()
	defer c.genMu.Unlock()
	if c.routingRevision != routingRevision || c.policyRevision != policyRevision {
		c.routingRevision = routingRevision
		c.policyRevision = policyRevision
		c.generation++
		if c.cache != nil {
			c.cache.clear()
		}
	}
	if c.generation == 0 {
		c.generation = 1
	}
	return c.generation
}

// runtimeUserPackageBuildCount exposes coalesced build attempts for tests.
func (s *Server) runtimeUserPackageBuildCount() int64 {
	s.runtimeUserPackages.init()
	return s.runtimeUserPackages.cache.builds.Load()
}
