package controller

import (
	"sort"
	"sync"
)

// Reasons a server was queued for reconciliation. They are a delivery hint
// only: they decide ordering and whether the ledger's "settled" answer may be
// trusted, never what the reconciliation computes. Plan lifecycle - prepare,
// activate, finalize - approval, and credential allocation are untouched by
// this file; hints are merged, plan phases never are.
const (
	accessSyncReasonRecovery  = "recovery"
	accessSyncReasonChange    = "change"
	accessSyncReasonDeadline  = "deadline"
	accessSyncReasonRevoke    = "revoke"
	accessSyncReasonReconnect = "reconnect"
)

// accessSyncPriority orders the queue. A revoke and a reconnect must not wait
// behind a fleet-wide recovery sweep.
func accessSyncPriority(reason string) int {
	switch reason {
	case accessSyncReasonRevoke:
		return 40
	case accessSyncReasonReconnect:
		return 30
	case accessSyncReasonDeadline:
		return 20
	case accessSyncReasonChange:
		return 10
	default:
		return 0
	}
}

// accessSyncForces reports whether a reason invalidates the ledger's own
// "already confirmed" answer. A reconnecting Agent may have lost its local
// state, changed boot ID, or come back with different capabilities, so a
// confirmed ledger row is not evidence that it still holds what it confirmed.
func accessSyncForces(reason string) bool {
	return reason == accessSyncReasonReconnect || reason == accessSyncReasonRevoke
}

// accessSyncHint is the merged pending state for one server on one channel.
type accessSyncHint struct {
	ServerID int64
	Reason   string
	Priority int
	Force    bool
}

// accessSyncHintSet holds the servers one delivery channel still owes work for.
//
// Hints for the same server merge into one entry that keeps the strongest
// reason, so a burst of changes costs one reconciliation rather than one per
// change. A hint noted while a round is running lands in the set the next round
// drains, so an update that arrives mid-processing is never lost.
type accessSyncHintSet struct {
	mu      sync.Mutex
	pending map[int64]accessSyncHint
}

// note records that one server may owe work. Passing no servers is a
// deliberate "scope unknown" signal and records nothing: the caller's wake then
// falls back to the ledger's own stale query.
func (h *accessSyncHintSet) note(reason string, serverIDs []int64) {
	if len(serverIDs) == 0 {
		return
	}
	priority := accessSyncPriority(reason)
	force := accessSyncForces(reason)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil {
		h.pending = map[int64]accessSyncHint{}
	}
	for _, serverID := range serverIDs {
		if serverID <= 0 {
			continue
		}
		existing, ok := h.pending[serverID]
		if !ok {
			h.pending[serverID] = accessSyncHint{ServerID: serverID, Reason: reason, Priority: priority, Force: force}
			continue
		}
		if priority > existing.Priority {
			existing.Reason = reason
			existing.Priority = priority
		}
		existing.Force = existing.Force || force
		h.pending[serverID] = existing
	}
}

// drain removes and returns the pending hints, highest priority first and
// stable by server id within a priority.
func (h *accessSyncHintSet) drain() []accessSyncHint {
	h.mu.Lock()
	pending := h.pending
	h.pending = nil
	h.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	out := make([]accessSyncHint, 0, len(pending))
	for _, hint := range pending {
		out = append(out, hint)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ServerID < out[j].ServerID
	})
	return out
}

func (h *accessSyncHintSet) size() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}
