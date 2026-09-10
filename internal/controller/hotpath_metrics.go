package controller

import (
	"sync/atomic"

	"github.com/OboardProject/oboard/internal/store"
)

// hotPathCounters records how often the steady-state Agent paths performed real
// work versus how often they proved the work was unnecessary. Every counter is
// a process-wide scalar: no server, user, credential, or task identity is ever
// used as a label, so the diagnostic surface stays low cardinality no matter
// how large the fleet grows. Per-node detail belongs in a focused test or a
// controlled diagnostic, never here.
type hotPathCounters struct {
	// Remote access capability persistence.
	remoteAccessWritten atomic.Int64
	remoteAccessSkipped atomic.Int64
	remoteAccessFailed  atomic.Int64

	// Connection presence cleanup after audit is switched off.
	presenceClearPerformed atomic.Int64
	presenceClearSkipped   atomic.Int64
	presenceClearFailed    atomic.Int64

	// Latency probe plan generation.
	probePlanHit             atomic.Int64
	probePlanRebuilt         atomic.Int64
	probePlanInvalidated     atomic.Int64
	probePlanResourceRefresh atomic.Int64
	probePlanStaleServed     atomic.Int64
	probePlanFailed          atomic.Int64
	probePlanVersionAssigned atomic.Int64

	// Authorization projection and lease issuance.
	authorizationProjectionHit       atomic.Int64
	authorizationProjectionBuilt     atomic.Int64
	authorizationProjectionShared    atomic.Int64
	authorizationProjectionDiscarded atomic.Int64
	authorizationLeaseReused         atomic.Int64
	authorizationLeaseIssued         atomic.Int64
}

// hotPathSnapshot is the machine-readable form used by diagnostics and tests.
type hotPathSnapshot struct {
	RemoteAccessWritten int64 `json:"remote_access_status_written"`
	RemoteAccessSkipped int64 `json:"remote_access_status_skipped"`
	RemoteAccessFailed  int64 `json:"remote_access_status_failed"`

	PresenceClearPerformed int64 `json:"presence_clear_performed"`
	PresenceClearSkipped   int64 `json:"presence_clear_skipped"`
	PresenceClearFailed    int64 `json:"presence_clear_failed"`

	ProbePlanHit             int64 `json:"probe_plan_hit"`
	ProbePlanRebuilt         int64 `json:"probe_plan_rebuilt"`
	ProbePlanInvalidated     int64 `json:"probe_plan_invalidated"`
	ProbePlanResourceRefresh int64 `json:"probe_plan_resource_refresh"`
	ProbePlanStaleServed     int64 `json:"probe_plan_stale_served"`
	ProbePlanFailed          int64 `json:"probe_plan_failed"`
	ProbePlanVersionAssigned int64 `json:"probe_plan_version_assigned"`

	AuthorizationProjectionHit       int64 `json:"authorization_projection_hit"`
	AuthorizationProjectionBuilt     int64 `json:"authorization_projection_built"`
	AuthorizationProjectionShared    int64 `json:"authorization_projection_shared"`
	AuthorizationProjectionDiscarded int64 `json:"authorization_projection_discarded"`
	AuthorizationLeaseReused         int64 `json:"authorization_lease_reused"`
	AuthorizationLeaseIssued         int64 `json:"authorization_lease_issued"`

	SQLStatements       int64 `json:"sql_statements"`
	SQLWriteTransactons int64 `json:"sql_write_transactions"`
}

func (c *hotPathCounters) snapshot(db *store.Store) hotPathSnapshot {
	snapshot := hotPathSnapshot{
		RemoteAccessWritten: c.remoteAccessWritten.Load(),
		RemoteAccessSkipped: c.remoteAccessSkipped.Load(),
		RemoteAccessFailed:  c.remoteAccessFailed.Load(),

		PresenceClearPerformed: c.presenceClearPerformed.Load(),
		PresenceClearSkipped:   c.presenceClearSkipped.Load(),
		PresenceClearFailed:    c.presenceClearFailed.Load(),

		ProbePlanHit:             c.probePlanHit.Load(),
		ProbePlanRebuilt:         c.probePlanRebuilt.Load(),
		ProbePlanInvalidated:     c.probePlanInvalidated.Load(),
		ProbePlanResourceRefresh: c.probePlanResourceRefresh.Load(),
		ProbePlanStaleServed:     c.probePlanStaleServed.Load(),
		ProbePlanFailed:          c.probePlanFailed.Load(),
		ProbePlanVersionAssigned: c.probePlanVersionAssigned.Load(),

		AuthorizationProjectionHit:       c.authorizationProjectionHit.Load(),
		AuthorizationProjectionBuilt:     c.authorizationProjectionBuilt.Load(),
		AuthorizationProjectionShared:    c.authorizationProjectionShared.Load(),
		AuthorizationProjectionDiscarded: c.authorizationProjectionDiscarded.Load(),
		AuthorizationLeaseReused:         c.authorizationLeaseReused.Load(),
		AuthorizationLeaseIssued:         c.authorizationLeaseIssued.Load(),
	}
	if db != nil {
		snapshot.SQLStatements = db.SQLStatementCount()
		snapshot.SQLWriteTransactons = db.SQLWriteTransactionCount()
	}
	return snapshot
}

// hotPathMetrics returns the current counters together with the Store SQL
// counters so one sample can separate "did less computation" from "issued
// fewer statements" and "started fewer write transactions".
func (s *Server) hotPathMetrics() hotPathSnapshot {
	return s.hotPath.snapshot(s.store)
}
