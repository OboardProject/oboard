package controller

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// reconcileAgentAppliedState turns an authenticated health report into a
// durable convergence check. The report contains only the Agent's local
// version metadata; task payloads and secrets never travel back to Controller.
func (s *Server) reconcileAgentAppliedState(ctx context.Context, serverID int64, health model.HealthReport) {
	state, err := s.store.ConfigurationSyncState(ctx, serverID)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("read configuration sync state server=%d: %v", serverID, err)
			return
		}
		relevant, relevantErr := s.store.ServerEverDeployedOrHasState(ctx, serverID)
		if relevantErr != nil || !relevant {
			return
		}
		revision, revisionErr := s.store.ConfigurationRevision(ctx)
		if revisionErr != nil || revision == 0 {
			return
		}
		_, _ = s.store.MarkConfigurationSyncPending(context.WithoutCancel(ctx), revision, []int64{serverID})
		s.signalConfigurationReconcile()
		return
	}
	matches := false
	if state.State == "synced" && state.LastConfigVersion == 0 && health.AppliedConfigVersion == 0 && (state.WantedDigest == "" || isSemanticDigest(state.WantedDigest) || state.SyncStrategy == "semantic_noop") {
		matches = true
	} else if state.LastConfigVersion > 0 && state.LastConfigVersion == health.AppliedConfigVersion && health.AppliedConfigDigest != "" {
		if state.WantedDigest == health.AppliedConfigDigest {
			matches = true
		} else if isSemanticDigest(state.WantedDigest) || state.SyncStrategy == "semantic_noop" {
			expected := s.serverExpectedPayloadDigest(ctx, serverID, state.LastConfigVersion)
			if expected == "" || expected == health.AppliedConfigDigest {
				matches = true
				if expected == health.AppliedConfigDigest {
					_ = s.store.UpdateConfigurationSyncWantedDigest(ctx, serverID, health.AppliedConfigDigest)
				}
			}
		}
	}
	if matches {
		if state.State == "queued" || state.State == "running" {
			_ = s.store.MarkConfigurationSyncResult(ctx, serverID, health.AppliedConfigVersion, true, "")
		}
		return
	}
	switch state.State {
	case "queued", "running":
		if health.AppliedConfigVersion < state.LastConfigVersion {
			s.tasks.wake(serverID)
			return
		}
		s.markAgentConfigurationDrift(ctx, serverID)
	case "synced":
		// A convergence report that still names an older config version and
		// arrives right after the deployment succeeded was produced before that
		// deployment. Redeploying on it would restart a server that has just
		// converged, so a settled state is required before it counts as drift.
		if health.AppliedConfigVersion < state.LastConfigVersion && time.Since(state.UpdatedAt) < agentConfigurationDriftGrace {
			return
		}
		s.markAgentConfigurationDrift(ctx, serverID)
	case "failed":
		return
	default:
		s.signalConfigurationReconcile()
	}
}

// agentConfigurationDriftGrace is how long a freshly settled sync state ignores
// convergence reports that were still in flight while it settled.
const agentConfigurationDriftGrace = 45 * time.Second

func isSemanticDigest(digest string) bool {
	digest = strings.TrimSpace(digest)
	return strings.HasPrefix(digest, "semantic_noop:") || strings.HasPrefix(digest, "routing:")
}

func (s *Server) markAgentConfigurationDrift(ctx context.Context, serverID int64) {
	revision, err := s.store.ConfigurationRevision(ctx)
	if err != nil || revision == 0 {
		if err != nil {
			log.Printf("read routing revision after Agent drift server=%d: %v", serverID, err)
		}
		return
	}
	opened, err := s.store.MarkConfigurationSyncDrift(context.WithoutCancel(ctx), serverID, revision)
	if err != nil {
		log.Printf("mark Agent drift server=%d: %v", serverID, err)
		return
	}
	if !opened {
		// A recovery for this server is already preparing, queued, running, or
		// pending. Repeated drift reports must not open a second one.
		return
	}
	s.publishRealtime("configuration", "deployments", "tasks")
	s.signalConfigurationReconcile()
}

func (s *Server) configurationHeartbeatFields(ctx context.Context, serverID int64) map[string]any {
	state, err := s.store.ConfigurationSyncState(ctx, serverID)
	if err != nil {
		return nil
	}
	return map[string]any{
		"desired_config_revision":    state.WantedRevision,
		"configuration_sync_state":   state.State,
		"configuration_sync_version": state.LastConfigVersion,
	}
}
