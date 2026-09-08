package controller

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// coalesceBuildTimeout bounds how long a shared cache build may run after the
// original request is cancelled. Waiters use their own contexts separately.
const coalesceBuildTimeout = 2 * time.Minute

// coalesceCache merges concurrent misses for the same key into one build that
// runs outside the management lock. Different keys do not serialize behind each
// other; a shared concurrency semaphore still caps total builds.
type coalesceCache[K comparable, V any] struct {
	mu          sync.Mutex
	entries     map[K]coalesceEntry[V]
	inflight    map[K]*coalesceCall[V]
	buildSem    chan struct{}
	maxEntries  int
	ttl         time.Duration
	maxStale    time.Duration
	epoch       uint64
	hits        atomic.Int64
	misses      atomic.Int64
	builds      atomic.Int64
	buildFailed atomic.Int64
	waiters     atomic.Int64
}

type coalesceEntry[V any] struct {
	value            V
	generation       uint64
	epoch            uint64
	dataAsOf         time.Time
	buildCompletedAt time.Time
	expiresAt        time.Time
}

type coalesceCall[V any] struct {
	done    chan struct{}
	epoch   uint64
	value   V
	err     error
	entry   coalesceEntry[V]
	waiters int
}

func newCoalesceCache[K comparable, V any](ttl time.Duration, maxEntries, maxBuilds int) *coalesceCache[K, V] {
	if maxBuilds < 1 {
		maxBuilds = 1
	}
	if maxEntries < 1 {
		maxEntries = 8
	}
	return &coalesceCache[K, V]{
		entries:    make(map[K]coalesceEntry[V]),
		inflight:   make(map[K]*coalesceCall[V]),
		buildSem:   make(chan struct{}, maxBuilds),
		maxEntries: maxEntries,
		ttl:        ttl,
		maxStale:   5 * time.Minute,
		epoch:      1,
	}
}

type coalesceLookup[V any] struct {
	hit              bool
	stale            bool
	value            V
	dataAsOf         time.Time
	buildCompletedAt time.Time
	expiresAt        time.Time
}

func (c *coalesceCache[K, V]) get(key K, now time.Time) (coalesceLookup[V], *coalesceCall[V], bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	epoch := c.epoch
	if entry, ok := c.entries[key]; ok {
		if now.Before(entry.expiresAt) {
			c.hits.Add(1)
			return coalesceLookup[V]{
				hit:              true,
				value:            entry.value,
				dataAsOf:         entry.dataAsOf,
				buildCompletedAt: entry.buildCompletedAt,
				expiresAt:        entry.expiresAt,
			}, nil, false, epoch
		}
		if c.maxStale > 0 && now.Sub(entry.buildCompletedAt) <= c.maxStale {
			if call, building := c.inflight[key]; building && call.epoch == epoch {
				call.waiters++
				c.waiters.Add(1)
				c.hits.Add(1)
				return coalesceLookup[V]{
					hit:              true,
					stale:            true,
					value:            entry.value,
					dataAsOf:         entry.dataAsOf,
					buildCompletedAt: entry.buildCompletedAt,
					expiresAt:        entry.expiresAt,
				}, nil, false, epoch
			}
			// Keep the expired entry for failure fallback; start a refresh.
		} else {
			delete(c.entries, key)
		}
	}
	if call, building := c.inflight[key]; building && call.epoch == epoch {
		call.waiters++
		c.waiters.Add(1)
		c.misses.Add(1)
		return coalesceLookup[V]{}, call, false, epoch
	}
	call := &coalesceCall[V]{done: make(chan struct{}), epoch: epoch}
	c.inflight[key] = call
	c.misses.Add(1)
	c.builds.Add(1)
	return coalesceLookup[V]{}, call, true, epoch
}

func (c *coalesceCache[K, V]) wait(ctx context.Context, call *coalesceCall[V]) (coalesceEntry[V], error) {
	select {
	case <-call.done:
		return call.entry, call.err
	case <-ctx.Done():
		// Cancelling one waiter must not cancel the shared build.
		return coalesceEntry[V]{}, ctx.Err()
	}
}

func (c *coalesceCache[K, V]) finish(call *coalesceCall[V], key K, generation, buildEpoch uint64, value V, dataAsOf time.Time, err error) (coalesceEntry[V], error) {
	completed := time.Now()
	entry := coalesceEntry[V]{
		value:            value,
		generation:       generation,
		epoch:            buildEpoch,
		dataAsOf:         dataAsOf,
		buildCompletedAt: completed,
		expiresAt:        completed.Add(c.ttl),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.inflight[key]; ok && current == call {
		delete(c.inflight, key)
	}
	if err != nil {
		c.buildFailed.Add(1)
		if call != nil {
			call.err = err
			if existing, ok := c.entries[key]; ok && c.maxStale > 0 && time.Since(existing.buildCompletedAt) <= c.maxStale {
				call.entry = existing
				call.err = nil
				call.value = existing.value
				close(call.done)
				return existing, nil
			}
			close(call.done)
		}
		return coalesceEntry[V]{}, err
	}
	publish := buildEpoch == c.epoch
	if existing, ok := c.entries[key]; ok && existing.generation > generation {
		publish = false
		entry = existing
	}
	if publish {
		if len(c.entries) >= c.maxEntries {
			c.evictExpiredLocked(completed)
			if len(c.entries) >= c.maxEntries {
				c.entries = make(map[K]coalesceEntry[V], c.maxEntries)
			}
		}
		c.entries[key] = entry
	} else if existing, ok := c.entries[key]; ok {
		entry = existing
	}
	if call != nil {
		call.entry = entry
		call.value = entry.value
		close(call.done)
	}
	return entry, nil
}

func (c *coalesceCache[K, V]) evictExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) && (c.maxStale <= 0 || now.Sub(entry.buildCompletedAt) > c.maxStale) {
			delete(c.entries, key)
		}
	}
}

func (c *coalesceCache[K, V]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	c.entries = make(map[K]coalesceEntry[V])
}

func (c *coalesceCache[K, V]) acquireBuild(ctx context.Context) error {
	select {
	case c.buildSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *coalesceCache[K, V]) releaseBuild() {
	select {
	case <-c.buildSem:
	default:
	}
}

// getOrBuild is the standard short-lock → build-outside → publish path.
// generation identifies the logical version observed before the build; an older
// generation or a cleared epoch never overwrites a newer published entry.
func (c *coalesceCache[K, V]) getOrBuild(
	ctx context.Context,
	key K,
	generation uint64,
	build func(context.Context) (V, time.Time, error),
) (coalesceEntry[V], error) {
	now := time.Now()
	lookup, call, leader, epoch := c.get(key, now)
	if lookup.hit {
		return coalesceEntry[V]{
			value:            lookup.value,
			generation:       generation,
			epoch:            epoch,
			dataAsOf:         lookup.dataAsOf,
			buildCompletedAt: lookup.buildCompletedAt,
			expiresAt:        lookup.expiresAt,
		}, nil
	}
	if !leader {
		return c.wait(ctx, call)
	}
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), coalesceBuildTimeout)
	defer cancel()
	if err := c.acquireBuild(buildCtx); err != nil {
		return c.finish(call, key, generation, epoch, *new(V), time.Time{}, err)
	}
	defer c.releaseBuild()
	value, dataAsOf, err := build(buildCtx)
	return c.finish(call, key, generation, epoch, value, dataAsOf, err)
}
