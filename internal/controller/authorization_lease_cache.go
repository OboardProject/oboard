package controller

import (
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// authorizationProjectionBuildTimeout bounds one shared projection build. The
// build is detached from the caller's cancellation, so it needs a stop of its
// own; it is far above any healthy build and only exists so a stuck read cannot
// hold the shared-build slot forever.
const authorizationProjectionBuildTimeout = 60 * time.Second

// authorizationLeaseCache holds the last issued lease per server.
//
// It lives on the Controller rather than on the projection for two reasons.
// Issuance writes to the database, and a single fleet-wide mutex made every
// server queue behind one another's SQL; each server now serializes only
// against itself. And a projection replacement must not silently produce a
// second, unrelated set of issuance locks for the same servers.
type authorizationLeaseCache struct {
	mu      sync.Mutex
	servers map[int64]*serverAuthorizationLease
}

// serverAuthorizationLease is one server's issuance lock plus the lease that
// lock last issued. Everything needed to decide reuse is recorded here, so a
// renewal that reuses the lease touches no grant list, no digest and no
// database.
type serverAuthorizationLease struct {
	mu sync.Mutex

	// projection identifies the exact authorization input the lease was derived
	// from. A new routing revision, a credential reconciliation, or any other
	// invalidation produces a different projection and therefore a miss.
	projection *authorizationProjection
	lease      *model.AuthorizationLease
	issuedAt   time.Time
	// nextBoundary is the first business time boundary after issuedAt that could
	// change this server's grant set, including the start of a grant it is not
	// authorized for yet. Zero means no boundary is scheduled.
	nextBoundary time.Time
}

func (s *Server) serverAuthorizationLeaseState(serverID int64) *serverAuthorizationLease {
	s.authorizationLeases.mu.Lock()
	defer s.authorizationLeases.mu.Unlock()
	if s.authorizationLeases.servers == nil {
		s.authorizationLeases.servers = map[int64]*serverAuthorizationLease{}
	}
	state := s.authorizationLeases.servers[serverID]
	if state == nil {
		state = &serverAuthorizationLease{}
		s.authorizationLeases.servers[serverID] = state
	}
	return state
}

// invalidateAuthorizationLease drops one server's reusable lease. It is called
// when the Agent identity behind the server may have changed - enrollment and
// control-channel connect - so a reconnecting or re-enrolled Agent is always
// answered with a freshly issued lease and sequence rather than one issued to
// its predecessor.
func (s *Server) invalidateAuthorizationLease(serverID int64) {
	s.authorizationLeases.mu.Lock()
	state := s.authorizationLeases.servers[serverID]
	s.authorizationLeases.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	state.projection = nil
	state.lease = nil
	state.mu.Unlock()
}

// forgetAuthorizationLease removes a deleted server's lease state and its lock.
func (s *Server) forgetAuthorizationLease(serverID int64) {
	s.authorizationLeases.mu.Lock()
	delete(s.authorizationLeases.servers, serverID)
	s.authorizationLeases.mu.Unlock()
}

// reusable reports whether the recorded lease still answers a renewal at `now`.
// It is deliberately conservative: every condition must hold, and a lease whose
// own validity is in doubt is never reused. It never extends an expiry and
// never rewrites issued_at - a reused lease is returned exactly as issued.
func (state *serverAuthorizationLease) reusable(projection *authorizationProjection, now time.Time) bool {
	if state.lease == nil || state.projection != projection {
		return false
	}
	elapsed := now.Sub(state.issuedAt)
	if elapsed < 0 || elapsed >= authorizationLeaseReissueAfter {
		return false
	}
	if !state.nextBoundary.IsZero() && !now.Before(state.nextBoundary) {
		return false
	}
	validUntil := authorizationLeaseValidUntil(state.lease)
	return !validUntil.IsZero() && validUntil.After(now)
}
