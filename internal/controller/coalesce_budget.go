package controller

import (
	"context"
	"errors"
	"time"
)

var errHistoryBusy = errors.New("history_busy: historical reads are busy; retry later")
var errHistoryTooLarge = errors.New("history_too_large: reduce the window or target selection")

type coalesceBudget[V any] struct {
	maxBytes, maxEntryBytes, maxInflight int
	timeout, retryDelay                  time.Duration
	size                                 func(V) int
}

func (c *coalesceCache[K, V]) touchBounded(key K, entry coalesceEntry[V]) {
	c.accessSequence++
	entry.access = c.accessSequence
	c.entries[key] = entry
}

func (c *coalesceCache[K, V]) putBounded(key K, entry coalesceEntry[V]) {
	if old, ok := c.entries[key]; ok {
		c.retainedBytes -= old.size
		delete(c.entries, key)
	}
	for len(c.entries) > 0 && (len(c.entries) >= c.maxEntries || c.retainedBytes+entry.size > c.budget.maxBytes) {
		var oldestKey K
		oldest := ^uint64(0)
		for k, v := range c.entries {
			if v.access < oldest {
				oldestKey, oldest = k, v.access
			}
		}
		c.retainedBytes -= c.entries[oldestKey].size
		delete(c.entries, oldestKey)
	}
	c.retainedBytes += entry.size
	c.touchBounded(key, entry)
}

func (c *coalesceCache[K, V]) getBounded(ctx context.Context, key K, generation uint64, build func(context.Context) (V, time.Time, error)) (coalesceEntry[V], error) {
	if err := ctx.Err(); err != nil {
		return coalesceEntry[V]{}, err
	}
	now := time.Now()
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && entry.generation == generation {
		if now.Before(entry.expiresAt) || now.Before(entry.retryAt) {
			c.touchBounded(key, entry)
			if entry.failure != nil && (entry.dataAsOf.IsZero() || now.Sub(entry.buildCompletedAt) > c.maxStale) {
				c.mu.Unlock()
				return coalesceEntry[V]{}, entry.failure
			}
			c.hits.Add(1)
			c.mu.Unlock()
			return entry, nil
		}
	}
	call := c.inflight[key]
	if call != nil && (call.abandoned || call.epoch != c.epoch || call.generation != generation) {
		c.mu.Unlock()
		return coalesceEntry[V]{}, errHistoryBusy
	}
	if call == nil {
		if len(c.inflight) >= c.budget.maxInflight {
			c.mu.Unlock()
			return coalesceEntry[V]{}, errHistoryBusy
		}
		buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.budget.timeout)
		call = &coalesceCall[V]{done: make(chan struct{}), epoch: c.epoch, cancel: cancel, generation: generation}
		c.inflight[key] = call
		c.builds.Add(1)
		go c.buildBounded(buildCtx, key, generation, call, build)
	}
	call.waiters++
	c.waiters.Add(1)
	c.misses.Add(1)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		call.waiters--
		c.waiters.Add(-1)
		if call.waiters == 0 && c.inflight[key] == call {
			call.abandoned = true
			call.cancel()
		}
	}()
	return c.wait(ctx, call)
}

func (c *coalesceCache[K, V]) buildBounded(ctx context.Context, key K, generation uint64, call *coalesceCall[V], build func(context.Context) (V, time.Time, error)) {
	defer call.cancel()
	var value V
	var observed time.Time
	err := c.acquireBuild(ctx)
	if err == nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else {
			value, observed, err = build(ctx)
		}
		c.releaseBuild()
	}
	if err == nil {
		err = ctx.Err()
	}
	size := 0
	if err == nil {
		size = c.budget.size(value)
		if size > c.budget.maxEntryBytes || size > c.budget.maxBytes {
			err = errHistoryTooLarge
		}
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	defer close(call.done)
	delete(c.inflight, key)
	if call.abandoned || call.epoch != c.epoch {
		call.err = context.Canceled
		return
	}
	entry := coalesceEntry[V]{value: value, generation: generation, epoch: call.epoch, dataAsOf: observed, buildCompletedAt: now, expiresAt: now.Add(c.ttl), size: size}
	if err != nil {
		c.buildFailed.Add(1)
		entry = coalesceEntry[V]{generation: generation, epoch: call.epoch, failure: err, retryAt: now.Add(c.budget.retryDelay)}
		if old, ok := c.entries[key]; ok && old.generation == generation && !old.dataAsOf.IsZero() && c.maxStale > 0 && now.Sub(old.buildCompletedAt) <= c.maxStale {
			entry = old
			entry.failure = err
			entry.retryAt = now.Add(c.budget.retryDelay)
		}
	}
	if (c.ttl > 0 || err != nil) && (c.retain == nil || c.retain(key)) {
		c.putBounded(key, entry)
	}
	call.entry, call.err = entry, err
	if err != nil && !entry.dataAsOf.IsZero() {
		call.err = nil
	}
}
