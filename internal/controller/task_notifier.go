package controller

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// taskNotifier wakes the agent WebSocket loop of a specific server when a new
// task becomes claimable. SQLite remains the task source of truth: the
// notifier is only a hint, and every wake still goes through the atomic
// NextTask claim. Channels have capacity one so concurrent wakes merge; a
// merged wake is harmless because the agent re-claims after every task_ack and
// the periodic recovery scan re-wakes lost hints.
type taskNotifier struct {
	mu    sync.Mutex
	chans map[int64]chan struct{}
}

func newTaskNotifier() *taskNotifier {
	return &taskNotifier{chans: map[int64]chan struct{}{}}
}

// channel returns the buffered wake channel for one server, creating it on
// first use. Consumers select on it; sends never block.
func (n *taskNotifier) channel(serverID int64) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch := n.chans[serverID]
	if ch == nil {
		ch = make(chan struct{}, 1)
		n.chans[serverID] = ch
	}
	return ch
}

// wake signals one server non-blockingly. When the channel is already full
// the wake is dropped: the agent either has an in-flight task (and re-claims
// on ack) or already woke (and claims), so no task is lost.
func (n *taskNotifier) wake(serverID int64) {
	if serverID <= 0 {
		return
	}
	n.mu.Lock()
	ch := n.chans[serverID]
	n.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// createTaskAndWake persists a task and wakes the owning server when the task
// was created pending. The wake is best-effort: the recovery scan re-delivers
// the hint for any agent that misses it.
func (s *Server) createTaskAndWake(ctx context.Context, task *model.AgentTask) error {
	if err := s.store.CreateTask(ctx, task); err != nil {
		return err
	}
	if !isPendingTaskStatus(task.Status) {
		return nil
	}
	s.tasks.wake(task.ServerID)
	return nil
}

func isPendingTaskStatus(status string) bool {
	return status == "pending" || status == "" || status == "running"
}

// taskRecoveryScan bounds how long a lost wake can delay delivery of a pending
// task. The scan itself only wakes servers; the claim still happens inside the
// agent loop against the database.
const (
	defaultTaskRecoveryScanMin = 30 * time.Second
	defaultTaskRecoveryScanMax = 60 * time.Second
)

// StartTaskRecoveryScan re-wakes every server that still has pending tasks on
// a jittered interval. It covers tasks created by paths that bypass the
// notifier and wakes lost through races, while the database remains the only
// task source of truth.
func (s *Server) StartTaskRecoveryScan(ctx context.Context) {
	minDelay, maxDelay := s.taskRecoveryScanMin, s.taskRecoveryScanMax
	if minDelay <= 0 {
		minDelay = defaultTaskRecoveryScanMin
	}
	if maxDelay <= minDelay {
		maxDelay = minDelay + defaultTaskRecoveryScanMin
	}
	// Run shortly after start so a restart never relies on the first full
	// interval to resume queued work.
	sleep := minDelay / 4
	if sleep < 100*time.Millisecond {
		sleep = 100 * time.Millisecond
	}
	timer := time.NewTimer(sleep)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.recoverPendingTaskWakes(ctx)
			jitter := minDelay + time.Duration(rand.Int63n(int64(maxDelay-minDelay)))
			timer.Reset(jitter)
		}
	}
}

func (s *Server) recoverPendingTaskWakes(ctx context.Context) {
	serverIDs, err := s.store.PendingTaskServerIDs(ctx)
	if err != nil {
		log.Printf("task recovery scan: %v", err)
		return
	}
	for _, serverID := range serverIDs {
		s.tasks.wake(serverID)
	}
}
