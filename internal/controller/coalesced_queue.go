package controller

import (
	"context"
	"log"
	"sync"
	"time"
)

// coalescedQueue is a bounded next-due scheduler keyed by an int64. A burst for
// one key runs once after debounce; work arriving during a run produces at most
// one trailing run after the minimum interval.
//
// It is shared by the audit risk pipeline and by the deployment push that
// follows a server coming back online. Both are "something changed for this id,
// catch up on it" work where a duplicate run is waste and a dropped run is a
// correctness problem, and both must stay bounded when a whole fleet becomes
// due at once.
type coalescedQueue struct {
	config   coalescedQueueConfig
	evaluate func(context.Context, int64) error
	states   map[int64]*coalescedState
	ch       chan int64
	closed   bool
	mu       sync.Mutex
	wg       sync.WaitGroup
}

// coalescedQueueConfig names the pacing of one queue. Every field is required:
// the pacing is the whole point of the type, so a zero value would be a silent
// misconfiguration rather than a useful default.
type coalescedQueueConfig struct {
	// name prefixes the failure log so two queues are distinguishable.
	name string
	// workers bounds how many keys run at once. The work behind both current
	// queues is database-bound on a single writer, so this stays small.
	workers     int
	size        int
	debounce    time.Duration
	minInterval time.Duration
	maxRetry    time.Duration
}

type coalescedState struct {
	dirty        bool
	queued       bool
	running      bool
	timer        *time.Timer
	lastFinished time.Time
	failures     uint
}

func newCoalescedQueue(config coalescedQueueConfig, evaluate func(context.Context, int64) error) *coalescedQueue {
	return &coalescedQueue{
		config:   config,
		evaluate: evaluate,
		states:   map[int64]*coalescedState{},
		ch:       make(chan int64, config.size),
	}
}

func (q *coalescedQueue) enqueue(key int64) {
	if q == nil || key <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	state := q.states[key]
	if state == nil {
		if len(q.states) >= q.config.size {
			return
		}
		state = &coalescedState{}
		q.states[key] = state
	}
	state.dirty = true
	if state.timer == nil && !state.queued && !state.running {
		q.scheduleLocked(key, state, q.nextDelayLocked(state))
	}
}

func (q *coalescedQueue) nextDelayLocked(state *coalescedState) time.Duration {
	delay := q.config.debounce
	if !state.lastFinished.IsZero() {
		until := time.Until(state.lastFinished.Add(q.config.minInterval))
		if until > delay {
			delay = until
		}
	}
	if state.failures > 0 {
		shift := min(state.failures-1, 4)
		retryDelay := q.config.minInterval * time.Duration(1<<shift)
		if retryDelay > q.config.maxRetry {
			retryDelay = q.config.maxRetry
		}
		if retryDelay > delay {
			delay = retryDelay
		}
	}
	if delay < 0 {
		return 0
	}
	return delay
}

func (q *coalescedQueue) scheduleLocked(key int64, state *coalescedState, delay time.Duration) {
	state.timer = time.AfterFunc(delay, func() { q.makeReady(key) })
}

func (q *coalescedQueue) makeReady(key int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	state := q.states[key]
	if state == nil {
		return
	}
	state.timer = nil
	if !state.dirty {
		delete(q.states, key)
		return
	}
	if state.running || state.queued {
		return
	}
	state.queued = true
	select {
	case q.ch <- key:
	default:
		state.queued = false
		q.scheduleLocked(key, state, q.config.debounce)
	}
}

func (q *coalescedQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-q.ch:
			q.mu.Lock()
			state := q.states[key]
			if state == nil || q.closed {
				q.mu.Unlock()
				continue
			}
			state.queued = false
			state.running = true
			state.dirty = false
			q.mu.Unlock()

			evaluateErr := q.evaluate(ctx, key)
			if evaluateErr != nil {
				log.Printf("%s id=%d: %v", q.config.name, key, evaluateErr)
			}

			q.mu.Lock()
			state = q.states[key]
			if state != nil {
				state.running = false
				state.lastFinished = time.Now()
				if evaluateErr != nil {
					state.failures++
					state.dirty = true
				} else {
					state.failures = 0
				}
				if state.dirty && !q.closed {
					q.scheduleLocked(key, state, q.nextDelayLocked(state))
				} else if !q.closed {
					// Keep the completion timestamp through the cooldown. The
					// timer either wakes newly dirtied work or evicts the idle
					// state.
					q.scheduleLocked(key, state, q.config.minInterval)
				} else {
					delete(q.states, key)
				}
			}
			q.mu.Unlock()
		}
	}
}

func (q *coalescedQueue) start(ctx context.Context) {
	for i := 0; i < q.config.workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
}

func (q *coalescedQueue) stop() {
	q.mu.Lock()
	q.closed = true
	for _, state := range q.states {
		if state.timer != nil {
			state.timer.Stop()
		}
	}
	q.states = map[int64]*coalescedState{}
	q.mu.Unlock()
}

// run starts the queue's workers and blocks until ctx ends, then drains them.
func (q *coalescedQueue) run(ctx context.Context) {
	if q == nil {
		return
	}
	q.start(ctx)
	<-ctx.Done()
	q.stop()
	q.wg.Wait()
}
