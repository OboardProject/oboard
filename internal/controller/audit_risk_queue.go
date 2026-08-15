package controller

import (
	"context"
	"log"
	"sync"
)

// auditRiskWorkers bounds concurrent audit risk evaluations. The former
// per-report synchronous EvaluateUsers call (several heavy queries plus
// snapshot writes per user) blocked the agent report HTTP handler; work is now
// queued and coalesced by userID: a user is never queued twice, and when
// activity arrives while its evaluation runs, one trailing re-evaluation
// happens.
const auditRiskWorkers = 2

// auditRiskQueue is a bounded, coalescing worker queue keyed by user ID.
type auditRiskQueue struct {
	evaluate func(context.Context, int64) error
	pending  map[int64]bool
	again    map[int64]bool
	ch       chan int64
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func newAuditRiskQueue(evaluate func(context.Context, int64) error) *auditRiskQueue {
	return &auditRiskQueue{
		evaluate: evaluate,
		pending:  map[int64]bool{},
		again:    map[int64]bool{},
		ch:       make(chan int64, 256),
	}
}

// enqueue schedules a user evaluation unless one is already queued or running.
func (q *auditRiskQueue) enqueue(userID int64) {
	if userID <= 0 {
		return
	}
	q.mu.Lock()
	if q.pending[userID] {
		q.again[userID] = true
		q.mu.Unlock()
		return
	}
	q.pending[userID] = true
	select {
	case q.ch <- userID:
	default:
		// The bounded queue is full; drop the wake and let a later report
		// batch cover the user. Never block the caller.
		q.pending[userID] = false
	}
	q.mu.Unlock()
}

func (q *auditRiskQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case userID := <-q.ch:
			for {
				if err := q.evaluate(ctx, userID); err != nil {
					log.Printf("evaluate audit incidents user=%d: %v", userID, err)
				}
				q.mu.Lock()
				rerun := q.again[userID]
				q.again[userID] = false
				q.pending[userID] = false
				q.mu.Unlock()
				if !rerun {
					break
				}
			}
		}
	}
}

func (q *auditRiskQueue) start(ctx context.Context) {
	for i := 0; i < auditRiskWorkers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
}

// StartAuditRiskWorker runs the bounded, coalescing audit risk evaluation
// queue against the built-in audit intelligence service until ctx ends.
func (s *Server) StartAuditRiskWorker(ctx context.Context) {
	if s.auditRisk == nil {
		return
	}
	s.auditRisk.start(ctx)
	<-ctx.Done()
	s.auditRisk.wg.Wait()
}
