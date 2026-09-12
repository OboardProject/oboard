package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoalesceCacheSameKeyBuildsOnce(t *testing.T) {
	cache := newCoalesceCache[string, int](time.Minute, 8, 2)
	cache.maxStale = 0
	var builds atomic.Int64
	var started sync.WaitGroup
	started.Add(1)
	build := func(ctx context.Context) (int, time.Time, error) {
		builds.Add(1)
		started.Wait()
		return 42, time.Now().UTC(), nil
	}
	var wg sync.WaitGroup
	results := make([]int, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entry, err := cache.getOrBuild(context.Background(), "k", 1, build)
			if err != nil {
				t.Errorf("build: %v", err)
				return
			}
			results[i] = entry.value
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	started.Done()
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("builds = %d, want 1", builds.Load())
	}
	for i, value := range results {
		if value != 42 {
			t.Fatalf("result[%d] = %d, want 42", i, value)
		}
	}
}

func TestCoalesceCacheHitNotBlockedByOtherKeyBuild(t *testing.T) {
	cache := newCoalesceCache[string, string](time.Minute, 8, 1)
	cache.maxStale = 0
	_, err := cache.getOrBuild(context.Background(), "fast", 1, func(context.Context) (string, time.Time, error) {
		return "ready", time.Now().UTC(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	var slowStarted sync.WaitGroup
	slowStarted.Add(1)
	go func() {
		_, _ = cache.getOrBuild(context.Background(), "slow", 1, func(context.Context) (string, time.Time, error) {
			slowStarted.Done()
			<-block
			return "slow", time.Now().UTC(), nil
		})
	}()
	slowStarted.Wait()
	start := time.Now()
	entry, err := cache.getOrBuild(context.Background(), "fast", 1, func(context.Context) (string, time.Time, error) {
		t.Fatal("fast key should hit cache")
		return "", time.Time{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.value != "ready" {
		t.Fatalf("value = %q", entry.value)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("cache hit blocked by other key for %s", time.Since(start))
	}
	close(block)
}

func TestCoalesceCacheOldGenerationDoesNotOverwrite(t *testing.T) {
	cache := newCoalesceCache[string, int](time.Minute, 8, 2)
	cache.maxStale = 0
	releaseOld := make(chan struct{})
	var oldStarted sync.WaitGroup
	oldStarted.Add(1)
	var oldDone sync.WaitGroup
	oldDone.Add(1)
	go func() {
		defer oldDone.Done()
		_, _ = cache.getOrBuild(context.Background(), "k", 1, func(context.Context) (int, time.Time, error) {
			oldStarted.Done()
			<-releaseOld
			return 1, time.Now().UTC(), nil
		})
	}()
	oldStarted.Wait()
	cache.clear() // bumps epoch so the old build cannot publish
	entry, err := cache.getOrBuild(context.Background(), "k", 2, func(context.Context) (int, time.Time, error) {
		return 2, time.Now().UTC(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.value != 2 {
		t.Fatalf("value = %d, want 2", entry.value)
	}
	close(releaseOld)
	oldDone.Wait()
	entry, err = cache.getOrBuild(context.Background(), "k", 2, func(context.Context) (int, time.Time, error) {
		t.Fatal("should hit generation-2 entry")
		return 0, time.Time{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.value != 2 {
		t.Fatalf("old build overwrote newer result: %d", entry.value)
	}
}

func TestCoalesceCacheCancelledWaiterDoesNotCancelSharedBuild(t *testing.T) {
	cache := newCoalesceCache[string, int](time.Minute, 8, 1)
	cache.maxStale = 0
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(1)
	var wg sync.WaitGroup
	// A waiter must join the leader's build, never start one of its own. Record
	// any stray build and assert after the goroutines finish: t.Fatal from a
	// non-test goroutine only stops that goroutine and never fails the test.
	var strayBuilds atomic.Int64
	wg.Add(3)
	go func() {
		defer wg.Done()
		entry, err := cache.getOrBuild(context.Background(), "k", 1, func(context.Context) (int, time.Time, error) {
			started.Done()
			<-release
			return 7, time.Now().UTC(), nil
		})
		if err != nil {
			t.Errorf("leader: %v", err)
			return
		}
		if entry.value != 7 {
			t.Errorf("leader value = %d", entry.value)
		}
	}()
	started.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer wg.Done()
		_, err := cache.getOrBuild(ctx, "k", 1, func(context.Context) (int, time.Time, error) {
			strayBuilds.Add(1)
			return 0, time.Time{}, nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled waiter err = %v", err)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	go func() {
		defer wg.Done()
		entry, err := cache.getOrBuild(context.Background(), "k", 1, func(context.Context) (int, time.Time, error) {
			strayBuilds.Add(1)
			return 0, time.Time{}, nil
		})
		if err != nil {
			t.Errorf("shared waiter: %v", err)
			return
		}
		if entry.value != 7 {
			t.Errorf("value = %d", entry.value)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if stray := strayBuilds.Load(); stray != 0 {
		t.Fatalf("waiters ran %d builds instead of joining the shared build", stray)
	}
}

func TestCoalesceCacheTTLStartsAfterBuildCompletes(t *testing.T) {
	cache := newCoalesceCache[string, int](50*time.Millisecond, 8, 1)
	cache.maxStale = 0
	entry, err := cache.getOrBuild(context.Background(), "k", 1, func(context.Context) (int, time.Time, error) {
		time.Sleep(80 * time.Millisecond)
		return 1, time.Now().UTC(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(entry.expiresAt)
	if remaining < 30*time.Millisecond {
		t.Fatalf("TTL appears measured from build start; remaining=%s", remaining)
	}
	_, err = cache.getOrBuild(context.Background(), "k", 1, func(context.Context) (int, time.Time, error) {
		t.Fatal("should still be fresh immediately after a slow build")
		return 0, time.Time{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoalesceCacheFailedRefreshDoesNotForgeFreshness(t *testing.T) {
	cache := newCoalesceCache[string, int](20*time.Millisecond, 8, 1)
	cache.maxStale = time.Minute
	first, err := cache.getOrBuild(context.Background(), "k", 1, func(context.Context) (int, time.Time, error) {
		return 1, time.Now().UTC(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	originalExpiry := first.expiresAt
	time.Sleep(25 * time.Millisecond)
	second, err := cache.getOrBuild(context.Background(), "k", 1, func(context.Context) (int, time.Time, error) {
		return 0, time.Time{}, errors.New("refresh failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.value != 1 {
		t.Fatalf("failed refresh dropped display value: %d", second.value)
	}
	if !second.expiresAt.Equal(originalExpiry) {
		t.Fatalf("failed refresh forged freshness: old=%s new=%s", originalExpiry, second.expiresAt)
	}
}
