package controller

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A Controller restart makes the whole fleet reconnect within seconds. Running
// the deployment build inline on each health report meant every one of those
// started at once: a production Controller was observed with nine concurrent
// builds, which saturated the single SQLite writer and turned every Agent
// endpoint into a 503.
func TestFleetWideReconnectDoesNotDeployEverythingAtOnce(t *testing.T) {
	var live, peak int64
	var mu sync.Mutex
	done := make(chan int64, 64)
	config := recoveryDeploymentQueueConfig
	config.debounce = 5 * time.Millisecond
	config.minInterval = 10 * time.Millisecond
	queue := newCoalescedQueue(config, func(ctx context.Context, serverID int64) error {
		current := atomic.AddInt64(&live, 1)
		mu.Lock()
		if current > peak {
			peak = current
		}
		mu.Unlock()
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt64(&live, -1)
		done <- serverID
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue.start(ctx)
	defer queue.stop()

	const fleet = 25
	for id := int64(1); id <= fleet; id++ {
		queue.enqueue(id)
	}
	seen := map[int64]bool{}
	deadline := time.After(10 * time.Second)
	for len(seen) < fleet {
		select {
		case id := <-done:
			seen[id] = true
		case <-deadline:
			t.Fatalf("only %d of %d servers were deployed", len(seen), fleet)
		}
	}
	mu.Lock()
	observed := peak
	mu.Unlock()
	if observed > int64(recoveryDeploymentQueueConfig.workers) {
		t.Fatalf("peak concurrent deployments = %d, want at most %d", observed, recoveryDeploymentQueueConfig.workers)
	}
}

// A flapping Agent reconnects several times in a row; only the last state
// matters, so the burst must collapse into one push.
func TestRepeatedReconnectsOfOneServerCollapse(t *testing.T) {
	var runs int64
	config := recoveryDeploymentQueueConfig
	config.debounce = 40 * time.Millisecond
	config.minInterval = 40 * time.Millisecond
	queue := newCoalescedQueue(config, func(context.Context, int64) error {
		atomic.AddInt64(&runs, 1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue.start(ctx)
	defer queue.stop()

	for i := 0; i < 10; i++ {
		queue.enqueue(7)
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt64(&runs); got != 1 {
		t.Fatalf("runs = %d, want the burst collapsed into 1", got)
	}
}

// A reconnect that arrives while that server's push is running must still be
// served afterwards: the desired state may have changed under it.
func TestReconnectDuringADeploymentSchedulesATrailingPush(t *testing.T) {
	var runs int64
	started := make(chan struct{}, 4)
	config := recoveryDeploymentQueueConfig
	config.debounce = 5 * time.Millisecond
	config.minInterval = 10 * time.Millisecond
	queue := newCoalescedQueue(config, func(context.Context, int64) error {
		atomic.AddInt64(&runs, 1)
		started <- struct{}{}
		time.Sleep(30 * time.Millisecond)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue.start(ctx)
	defer queue.stop()

	queue.enqueue(3)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first push never started")
	}
	queue.enqueue(3)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("trailing push never ran, runs=%d", atomic.LoadInt64(&runs))
	}
}
