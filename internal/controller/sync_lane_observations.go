package controller

import (
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// syncLaneObservations holds what nodes report about delivery lanes the
// Controller cannot otherwise observe.
//
// A delivery lane is a version gate: the Controller binds a version to a
// content identity, the node refuses a version it already holds with different
// content, and the Controller only ever re-derives the same binding. When the
// two sides disagree, neither can leave that state on its own - the version
// moves only when the content changes, and the content is already what the node
// should be running. The disagreement has to be visible somewhere before it can
// be repaired, and for the probe lane the node's own report is the only place it
// exists: a refused plan is a local log line and nothing else.
//
// This is deliberately in-process. The reports arrive every heartbeat, so a
// restart rebuilds it within one cycle, and persisting it would put a write on
// the health-report path to store something that is re-derived for free.
type syncLaneObservations struct {
	mu sync.Mutex
	// probe is the probe plan identity each node reports it currently runs.
	probe map[int64]model.LatencyProbeAppliedSnapshot
	// usersAutoResyncAt bounds automatic users-lane repair. A divergence at an
	// equal revision is repaired by allocating a new one, which is safe and
	// lossless; the interval is what keeps a repair that does not take from
	// becoming a revision allocated per heartbeat.
	usersAutoResyncAt map[int64]time.Time
}

// usersAutoResyncInterval is the minimum spacing between two automatic
// users-lane repairs for one server. One repair resolves a real conflict,
// because a higher revision is the one thing the node's gate always accepts.
const usersAutoResyncInterval = 10 * time.Minute

func (o *syncLaneObservations) recordProbe(serverID int64, applied *model.LatencyProbeAppliedSnapshot) {
	if serverID <= 0 || applied == nil || applied.PlanVersion <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.probe == nil {
		o.probe = map[int64]model.LatencyProbeAppliedSnapshot{}
	}
	o.probe[serverID] = *applied
}

func (o *syncLaneObservations) probeSnapshot() map[int64]model.LatencyProbeAppliedSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[int64]model.LatencyProbeAppliedSnapshot, len(o.probe))
	for serverID, applied := range o.probe {
		out[serverID] = applied
	}
	return out
}

// allowUsersAutoResync reports whether an automatic users-lane repair may run
// for this server now, and records the attempt when it may.
func (o *syncLaneObservations) allowUsersAutoResync(serverID int64, now time.Time) bool {
	if serverID <= 0 {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if last, ok := o.usersAutoResyncAt[serverID]; ok && now.Sub(last) < usersAutoResyncInterval {
		return false
	}
	if o.usersAutoResyncAt == nil {
		o.usersAutoResyncAt = map[int64]time.Time{}
	}
	o.usersAutoResyncAt[serverID] = now
	return true
}

func (o *syncLaneObservations) forget(serverID int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.probe, serverID)
	delete(o.usersAutoResyncAt, serverID)
}
