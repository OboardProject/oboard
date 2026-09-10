package controller

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// presenceClearRetryBackoff bounds how often a failed presence cleanup is
// retried. A failure must neither be retried on every heartbeat nor be skipped
// forever.
const presenceClearRetryBackoff = 2 * time.Minute

// presenceAuditCache tracks, per server, the effective connection-audit state
// this Controller last observed and whether the disabled-state presence cleanup
// already succeeded for it. Cleanup then runs on the enabled -> disabled
// transition, on the first observation after Controller start, after an Agent
// identity replacement, and on bounded retry after a failure - instead of on
// every hello and every heartbeat while audit stays off.
//
// The per-server mutex is also the serialization point between cleanup and
// presence ingestion, so a report that passed its gate just before an
// administrator disabled audit can never write state back after the delete.
type presenceAuditCache struct {
	mu      sync.Mutex
	servers map[int64]*presenceAuditServerState
}

type presenceAuditServerState struct {
	mu          sync.Mutex
	known       bool
	agentID     string
	enabled     bool
	cleared     bool
	nextRetryAt time.Time
}

func (s *Server) presenceAuditState(serverID int64) *presenceAuditServerState {
	s.presenceAudit.mu.Lock()
	defer s.presenceAudit.mu.Unlock()
	if s.presenceAudit.servers == nil {
		s.presenceAudit.servers = map[int64]*presenceAuditServerState{}
	}
	state := s.presenceAudit.servers[serverID]
	if state == nil {
		state = &presenceAuditServerState{}
		s.presenceAudit.servers[serverID] = state
	}
	return state
}

// forgetPresenceAuditState drops the per-server cleanup memory and its lock so
// a deleted server leaves nothing behind.
func (s *Server) forgetPresenceAuditState(serverID int64) {
	s.presenceAudit.mu.Lock()
	delete(s.presenceAudit.servers, serverID)
	s.presenceAudit.mu.Unlock()
}

// syncConnectionAuditPresence reconciles one server's presence state with its
// effective audit state. It is called from hello and from every heartbeat, and
// performs the delete only when the recorded state actually requires it.
func (s *Server) syncConnectionAuditPresence(ctx context.Context, server *model.Server, enabled bool) {
	if server == nil || server.ID <= 0 {
		return
	}
	agentID := strings.TrimSpace(server.AgentID)
	state := s.presenceAuditState(server.ID)
	state.mu.Lock()
	defer state.mu.Unlock()

	// A replaced Agent identity invalidates everything remembered for the
	// previous one, including a completed cleanup.
	if state.known && state.agentID != agentID {
		state.known = false
		state.cleared = false
		state.nextRetryAt = time.Time{}
	}
	state.agentID = agentID

	if enabled {
		state.known = true
		state.enabled = true
		state.cleared = false
		state.nextRetryAt = time.Time{}
		return
	}

	// Audit is off. Nothing to do once the cleanup for this disabled epoch has
	// already succeeded.
	if state.known && !state.enabled && state.cleared {
		s.hotPath.presenceClearSkipped.Add(1)
		return
	}
	now := time.Now()
	if !state.nextRetryAt.IsZero() && now.Before(state.nextRetryAt) {
		s.hotPath.presenceClearSkipped.Add(1)
		return
	}
	if err := s.store.ClearConnectionPresenceForServer(ctx, server.ID); err != nil {
		s.hotPath.presenceClearFailed.Add(1)
		state.known = true
		state.enabled = false
		state.cleared = false
		state.nextRetryAt = now.Add(presenceClearRetryBackoff)
		log.Printf("clear connection presence server=%d: %v", server.ID, err)
		return
	}
	s.hotPath.presenceClearPerformed.Add(1)
	state.known = true
	state.enabled = false
	state.cleared = true
	state.nextRetryAt = time.Time{}
}

// withPresenceIngestGuard runs one presence ingestion under the same per-server
// lock the cleanup uses, re-checking the effective audit state inside the lock.
// An administrator who disables audit between the caller's gate check and the
// write therefore cannot have the state written back behind the delete.
func (s *Server) withPresenceIngestGuard(ctx context.Context, server *model.Server, ingest func() error) error {
	if server == nil || server.ID <= 0 {
		return nil
	}
	state := s.presenceAuditState(server.ID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !s.effectiveConnectionAuditEnabled(ctx, server) {
		return nil
	}
	// Ingesting proves audit is on, so the next disable must clean up again.
	state.known = true
	state.agentID = strings.TrimSpace(server.AgentID)
	state.enabled = true
	state.cleared = false
	state.nextRetryAt = time.Time{}
	return ingest()
}
