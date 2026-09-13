package controller

import (
	"context"
	"time"
)

// recoveryDeploymentQueueConfig paces the deployment push that follows a server
// coming back online.
//
// Two workers, not one: a single slow node must not hold the rest of a
// recovering fleet behind it, and two is still far below what saturated the
// writer. The debounce is longer than the audit queue's because a flapping
// Agent reconnects several times in a row and only the last state matters, and
// the minimum interval keeps one node that reconnects repeatedly from
// monopolising a slot.
var recoveryDeploymentQueueConfig = coalescedQueueConfig{
	name:        "auto deployment after reconnect server",
	workers:     2,
	size:        512,
	debounce:    5 * time.Second,
	minInterval: 30 * time.Second,
	maxRetry:    10 * time.Minute,
}

func newRecoveryDeploymentQueue(s *Server) *coalescedQueue {
	return newCoalescedQueue(recoveryDeploymentQueueConfig, func(ctx context.Context, serverID int64) error {
		s.queueDeploymentAfterReconnect(ctx, serverID)
		// queueDeploymentAfterReconnect already records its own failures, as a
		// visible failed task and a log line. Reporting an error here as well
		// would put the server into the queue's retry backoff on top of that,
		// which is the caller's decision to make, not this wrapper's.
		return nil
	})
}

// StartRecoveryDeploymentWorker runs the bounded reconnect push until ctx ends.
func (s *Server) StartRecoveryDeploymentWorker(ctx context.Context) {
	s.recoveryDeployments.run(ctx)
}
