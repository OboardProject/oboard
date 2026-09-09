package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testBoundedCache() *coalesceCache[string, string] {
	c := newCoalesceCache[string, string](30*time.Second, 128, 1)
	c.maxStale = 150 * time.Second
	c.budget = &coalesceBudget[string]{maxBytes: 1024, maxEntryBytes: 128, maxInflight: 9, timeout: time.Second, retryDelay: time.Second, size: func(v string) int { return len(v) }}
	return c
}
func waitHistoryCondition(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestBoundedHistoryTenReadersShareOneBuild(t *testing.T) {
	c := testBoundedCache()
	release := make(chan struct{})
	var builds atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := c.getOrBuild(context.Background(), "same", 1, func(context.Context) (string, time.Time, error) {
				builds.Add(1)
				<-release
				return "result", time.Now(), nil
			})
			if err != nil || e.value != "result" {
				t.Errorf("entry=%+v err=%v", e, err)
			}
		}()
	}
	waitHistoryCondition(t, func() bool { return c.waiters.Load() == 10 })
	close(release)
	wg.Wait()
	if builds.Load() != 1 || c.waiters.Load() != 0 {
		t.Fatalf("builds=%d waiters=%d", builds.Load(), c.waiters.Load())
	}
	_, err := c.getOrBuild(context.Background(), "same", 1, func(context.Context) (string, time.Time, error) { t.Error("hit rebuilt"); return "", time.Time{}, nil })
	if err != nil {
		t.Fatal(err)
	}
}
func TestBoundedHistoryCancelsOnlyAfterLastWaiter(t *testing.T) {
	c := testBoundedCache()
	a, cancelA := context.WithCancel(context.Background())
	b, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()
	started := make(chan context.Context, 1)
	finished := make(chan struct{})
	build := func(ctx context.Context) (string, time.Time, error) {
		started <- ctx
		<-ctx.Done()
		close(finished)
		return "", time.Time{}, ctx.Err()
	}
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := c.getOrBuild(a, "same", 1, build); first <- err }()
	shared := <-started
	go func() { _, err := c.getOrBuild(b, "same", 1, build); second <- err }()
	waitHistoryCondition(t, func() bool { return c.waiters.Load() == 2 })
	cancelA()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if shared.Err() != nil {
		t.Fatal("cancelled another reader")
	}
	cancelB()
	<-second
	<-finished
	waitHistoryCondition(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.inflight) == 0 })
	if len(c.entries) != 0 {
		t.Fatal("cancelled work was cached")
	}
}
func TestBoundedHistoryQueueAndTimeout(t *testing.T) {
	c := testBoundedCache()
	c.budget.maxInflight = 2
	c.budget.timeout = 50 * time.Millisecond
	var active, maximum atomic.Int64
	build := func(ctx context.Context) (string, time.Time, error) {
		n := active.Add(1)
		if n > maximum.Load() {
			maximum.Store(n)
		}
		defer active.Add(-1)
		<-ctx.Done()
		return "", time.Time{}, ctx.Err()
	}
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := c.getOrBuild(context.Background(), key, 1, build)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err=%v", err)
			}
		}(key)
	}
	waitHistoryCondition(t, func() bool { return c.waiters.Load() == 2 })
	if _, err := c.getOrBuild(context.Background(), "overflow", 1, build); !errors.Is(err, errHistoryBusy) {
		t.Fatalf("overflow=%v", err)
	}
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("active max=%d", maximum.Load())
	}
}
func TestBoundedHistoryLRUSizeStaleAndBackoff(t *testing.T) {
	c := testBoundedCache()
	c.budget.maxBytes = 6
	c.budget.maxEntryBytes = 4
	read := func(key, value string) {
		t.Helper()
		if _, err := c.getOrBuild(context.Background(), key, 1, func(context.Context) (string, time.Time, error) { return value, time.Now(), nil }); err != nil {
			t.Fatal(err)
		}
	}
	read("a", "aa")
	read("b", "bb")
	read("c", "cc")
	read("a", "unused")
	read("d", "ddd")
	if c.retainedBytes != 5 || len(c.entries) != 2 {
		t.Fatalf("bytes=%d entries=%d", c.retainedBytes, len(c.entries))
	}
	if _, ok := c.entries["a"]; !ok {
		t.Fatal("evicted recently read entry")
	}
	_, err := c.getOrBuild(context.Background(), "large", 1, func(context.Context) (string, time.Time, error) { return "oversized", time.Now(), nil })
	if !errors.Is(err, errHistoryTooLarge) {
		t.Fatal(err)
	}
	entry := c.entries["a"]
	entry.expiresAt = time.Now().Add(-time.Second)
	entry.buildCompletedAt = time.Now().Add(-31 * time.Second)
	c.entries["a"] = entry
	failures := 0
	failure := func(context.Context) (string, time.Time, error) {
		failures++
		return "", time.Time{}, errors.New("temporary")
	}
	for i := 0; i < 2; i++ {
		entry, err = c.getOrBuild(context.Background(), "a", 1, failure)
		if err != nil || entry.value != "aa" {
			t.Fatalf("stale=%+v %v", entry, err)
		}
	}
	if failures != 1 {
		t.Fatalf("failure retries=%d", failures)
	}
	entry = c.entries["a"]
	entry.buildCompletedAt = time.Now().Add(-151 * time.Second)
	c.entries["a"] = entry
	if _, err := c.getOrBuild(context.Background(), "a", 1, failure); err == nil {
		t.Fatal("served stale beyond retention")
	}
	c.clear()
	if c.retainedBytes != 0 || len(c.entries) != 0 {
		t.Fatal("clear leaked entries")
	}
}
