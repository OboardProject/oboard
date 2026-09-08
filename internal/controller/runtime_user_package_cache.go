package controller

import (
	"context"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

const runtimeUserPackageTTL = 30 * time.Second

type runtimeUserPackageEntry struct {
	pkg     core.RuntimeUserPackage
	builtAt time.Time
}

type runtimeUserPackageCache struct {
	mu              sync.Mutex
	routingRevision uint64
	policyRevision  uint64
	entries         map[int64]runtimeUserPackageEntry
}

// Building a package regenerates the whole server configuration, which a
// snapshot pull, the periodic recovery scan, and every wake of the sync worker
// each used to pay for separately. Pulls and scans now share one build, and the
// lock also collapses a fleet-wide burst into a single generation instead of
// one per concurrent request.
//
// Routing and quota-policy edits invalidate the entry immediately, so only
// time-driven access transitions can be up to the TTL stale here; the
// authorization lease still evaluates those deadlines per renewal and remains
// the enforcement boundary.
func (s *Server) currentRuntimeUserPackage(ctx context.Context, server model.Server, revision int64) (core.RuntimeUserPackage, uint64, error) {
	c := &s.runtimeUserPackages
	c.mu.Lock()
	defer c.mu.Unlock()
	routingRevision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return core.RuntimeUserPackage{}, 0, err
	}
	policyRevision, err := s.store.TrafficPolicyRevision(ctx)
	if err != nil {
		return core.RuntimeUserPackage{}, 0, err
	}
	if c.routingRevision != routingRevision || c.policyRevision != policyRevision {
		c.entries = nil
		c.routingRevision, c.policyRevision = routingRevision, policyRevision
	}
	now := time.Now()
	entry, found := c.entries[server.ID]
	if !found || now.Sub(entry.builtAt) < 0 || now.Sub(entry.builtAt) >= runtimeUserPackageTTL {
		pkg, err := s.buildRuntimeUserPackage(ctx, server, revision)
		if err != nil {
			return core.RuntimeUserPackage{}, 0, err
		}
		if c.entries == nil {
			c.entries = make(map[int64]runtimeUserPackageEntry)
		}
		for id, old := range c.entries {
			if now.Sub(old.builtAt) >= runtimeUserPackageTTL {
				delete(c.entries, id)
			}
		}
		entry = runtimeUserPackageEntry{pkg: pkg, builtAt: now}
		c.entries[server.ID] = entry
	}
	pkg := entry.pkg
	pkg.UsersRevision = revision
	pkg.UsersDigest, err = core.UsersDigest(revision, pkg.Scope, pkg.Entries)
	return pkg, routingRevision, err
}
