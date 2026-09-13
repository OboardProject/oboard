package controller

import (
	"context"
	"time"
)

// auditRiskQueueConfig paces the per-user risk pipeline: one worker, because
// an evaluation is a database read of that user's whole window and running
// several at once only moves the contention.
var auditRiskQueueConfig = coalescedQueueConfig{
	name:        "evaluate audit risks user",
	workers:     1,
	size:        256,
	debounce:    2 * time.Second,
	minInterval: 15 * time.Second,
	maxRetry:    5 * time.Minute,
}

func newAuditRiskQueue(evaluate func(context.Context, int64) error) *coalescedQueue {
	return newCoalescedQueue(auditRiskQueueConfig, evaluate)
}

func (s *Server) evaluateConnectionAuditRisks(ctx context.Context, userID int64) error {
	if err := s.store.RefreshConnectionProbeEpisodes(ctx, userID, time.Now().UTC()); err != nil {
		return err
	}
	// One shared evidence load per evaluation: the device-action gate, the
	// notification scan, and the incident detail below all consumed the same
	// shared-route scan and risk-window reports, each as its own full query.
	evidence, err := s.store.LoadConnectionAuditSharedEvidence(ctx, []int64{userID}, time.Now().UTC())
	if err != nil {
		return err
	}
	s.applyConnectionAuditDeviceActions(ctx, []int64{userID}, &evidence)
	s.notifyConnectionAuditRisks(ctx, []int64{userID}, &evidence)
	_, err = s.auditIntel.EvaluateUserWithEvidence(ctx, userID, &evidence)
	return err
}

// StartAuditRiskWorker runs the coalesced risk pipeline until ctx ends.
func (s *Server) StartAuditRiskWorker(ctx context.Context) {
	s.auditRisk.run(ctx)
}
