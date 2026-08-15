package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuditRiskQueueDebouncesBurst(t *testing.T) {
	var calls atomic.Int64
	evaluated := make(chan int64, 4)
	queue := newAuditRiskQueue(func(_ context.Context, userID int64) error {
		calls.Add(1)
		evaluated <- userID
		return nil
	})
	queue.debounce = 10 * time.Millisecond
	queue.minInterval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	queue.start(ctx)
	defer func() {
		cancel()
		queue.stop()
		queue.wg.Wait()
	}()

	for index := 0; index < 100; index++ {
		queue.enqueue(7)
	}
	select {
	case userID := <-evaluated:
		if userID != 7 {
			t.Fatalf("evaluated user = %d", userID)
		}
	case <-time.After(time.Second):
		t.Fatal("debounced evaluation did not run")
	}
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("burst evaluations = %d, want 1", got)
	}
}

func TestAuditRiskQueueDefersOneTrailingEvaluation(t *testing.T) {
	var calls atomic.Int64
	started := make(chan time.Time, 4)
	releaseFirst := make(chan struct{})
	queue := newAuditRiskQueue(func(_ context.Context, _ int64) error {
		call := calls.Add(1)
		started <- time.Now()
		if call == 1 {
			<-releaseFirst
		}
		return nil
	})
	queue.debounce = 5 * time.Millisecond
	queue.minInterval = 80 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	queue.start(ctx)
	defer func() {
		cancel()
		queue.stop()
		queue.wg.Wait()
	}()

	queue.enqueue(9)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first evaluation did not start")
	}
	for index := 0; index < 50; index++ {
		queue.enqueue(9)
	}
	releasedAt := time.Now()
	close(releaseFirst)
	select {
	case trailingAt := <-started:
		if trailingAt.Sub(releasedAt) < 65*time.Millisecond {
			t.Fatalf("trailing evaluation ran after %s, before cooldown", trailingAt.Sub(releasedAt))
		}
	case <-time.After(time.Second):
		t.Fatal("trailing evaluation did not run")
	}
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("trailing evaluations = %d, want 2", got)
	}
}

func TestAuditRiskQueueKeepsCooldownAfterIdleEvaluation(t *testing.T) {
	finished := make(chan time.Time, 4)
	queue := newAuditRiskQueue(func(_ context.Context, _ int64) error {
		finished <- time.Now()
		return nil
	})
	queue.debounce = 5 * time.Millisecond
	queue.minInterval = 80 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	queue.start(ctx)
	defer func() {
		cancel()
		queue.stop()
		queue.wg.Wait()
	}()

	queue.enqueue(11)
	var first time.Time
	select {
	case first = <-finished:
	case <-time.After(time.Second):
		t.Fatal("first evaluation did not run")
	}
	queue.enqueue(11)
	select {
	case second := <-finished:
		if second.Sub(first) < 65*time.Millisecond {
			t.Fatalf("idle evaluation cooldown = %s", second.Sub(first))
		}
	case <-time.After(time.Second):
		t.Fatal("second evaluation did not run")
	}
}

func TestAuditRiskQueueRetriesEvaluationError(t *testing.T) {
	var calls atomic.Int64
	finished := make(chan error, 2)
	queue := newAuditRiskQueue(func(_ context.Context, _ int64) error {
		if calls.Add(1) == 1 {
			err := errors.New("transient failure")
			finished <- err
			return err
		}
		finished <- nil
		return nil
	})
	queue.debounce = 5 * time.Millisecond
	queue.minInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	queue.start(ctx)
	defer func() {
		cancel()
		queue.stop()
		queue.wg.Wait()
	}()

	queue.enqueue(13)
	for index := 0; index < 2; index++ {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("evaluation retry did not finish")
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("evaluation attempts = %d, want 2", got)
	}
}
