package controller

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	auditRiskWorkers     = 1
	auditRiskQueueSize   = 256
	auditRiskDebounce    = 2 * time.Second
	auditRiskMinInterval = 15 * time.Second
	auditRiskMaxRetry    = 5 * time.Minute
)

type auditRiskState struct {
	dirty        bool
	queued       bool
	running      bool
	timer        *time.Timer
	lastFinished time.Time
	failures     uint
}

// auditRiskQueue is a bounded next-due scheduler keyed by user ID. A burst is
// evaluated once after debounce; activity arriving during evaluation produces
// at most one trailing run after the minimum interval.
type auditRiskQueue struct {
	evaluate    func(context.Context, int64) error
	states      map[int64]*auditRiskState
	ch          chan int64
	debounce    time.Duration
	minInterval time.Duration
	closed      bool
	mu          sync.Mutex
	wg          sync.WaitGroup
}

func newAuditRiskQueue(evaluate func(context.Context, int64) error) *auditRiskQueue {
	return &auditRiskQueue{
		evaluate:    evaluate,
		states:      map[int64]*auditRiskState{},
		ch:          make(chan int64, auditRiskQueueSize),
		debounce:    auditRiskDebounce,
		minInterval: auditRiskMinInterval,
	}
}

func (q *auditRiskQueue) enqueue(userID int64) {
	if q == nil || userID <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	state := q.states[userID]
	if state == nil {
		if len(q.states) >= auditRiskQueueSize {
			return
		}
		state = &auditRiskState{}
		q.states[userID] = state
	}
	state.dirty = true
	if state.timer == nil && !state.queued && !state.running {
		q.scheduleLocked(userID, state, q.nextDelayLocked(state))
	}
}

func (q *auditRiskQueue) nextDelayLocked(state *auditRiskState) time.Duration {
	delay := q.debounce
	if !state.lastFinished.IsZero() {
		until := time.Until(state.lastFinished.Add(q.minInterval))
		if until > delay {
			delay = until
		}
	}
	if state.failures > 0 {
		shift := min(state.failures-1, 4)
		retryDelay := q.minInterval * time.Duration(1<<shift)
		if retryDelay > auditRiskMaxRetry {
			retryDelay = auditRiskMaxRetry
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

func (q *auditRiskQueue) scheduleLocked(userID int64, state *auditRiskState, delay time.Duration) {
	state.timer = time.AfterFunc(delay, func() { q.makeReady(userID) })
}

func (q *auditRiskQueue) makeReady(userID int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	state := q.states[userID]
	if state == nil {
		return
	}
	state.timer = nil
	if !state.dirty {
		delete(q.states, userID)
		return
	}
	if state.running || state.queued {
		return
	}
	state.queued = true
	select {
	case q.ch <- userID:
	default:
		state.queued = false
		q.scheduleLocked(userID, state, q.debounce)
	}
}

func (q *auditRiskQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case userID := <-q.ch:
			q.mu.Lock()
			state := q.states[userID]
			if state == nil || q.closed {
				q.mu.Unlock()
				continue
			}
			state.queued = false
			state.running = true
			state.dirty = false
			q.mu.Unlock()

			evaluateErr := q.evaluate(ctx, userID)
			if evaluateErr != nil {
				log.Printf("evaluate audit risks user=%d: %v", userID, evaluateErr)
			}

			q.mu.Lock()
			state = q.states[userID]
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
					q.scheduleLocked(userID, state, q.nextDelayLocked(state))
				} else if !q.closed {
					// Keep the completion timestamp through the cooldown. The timer
					// either wakes newly dirtied work or evicts the idle state.
					q.scheduleLocked(userID, state, q.minInterval)
				} else {
					delete(q.states, userID)
				}
			}
			q.mu.Unlock()
		}
	}
}

func (q *auditRiskQueue) start(ctx context.Context) {
	for i := 0; i < auditRiskWorkers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
}

func (s *Server) evaluateConnectionAuditRisks(ctx context.Context, userID int64) error {
	if err := s.store.RefreshConnectionProbeEpisodes(ctx, userID, time.Now().UTC()); err != nil {
		return err
	}
	s.applyConnectionAuditDeviceActions(ctx, []int64{userID})
	s.notifyConnectionAuditRisks(ctx, []int64{userID})
	_, err := s.auditIntel.EvaluateUser(ctx, userID)
	return err
}

func (q *auditRiskQueue) stop() {
	q.mu.Lock()
	q.closed = true
	for _, state := range q.states {
		if state.timer != nil {
			state.timer.Stop()
		}
	}
	q.states = map[int64]*auditRiskState{}
	q.mu.Unlock()
}

// StartAuditRiskWorker runs the coalesced risk pipeline until ctx ends.
func (s *Server) StartAuditRiskWorker(ctx context.Context) {
	if s.auditRisk == nil {
		return
	}
	s.auditRisk.start(ctx)
	<-ctx.Done()
	s.auditRisk.stop()
	s.auditRisk.wg.Wait()
}
