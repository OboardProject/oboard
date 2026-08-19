package controller

import (
	"context"
	"database/sql"
	"log"

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
	matches := state.LastConfigVersion > 0 && state.LastConfigVersion == health.AppliedConfigVersion &&
		health.AppliedConfigDigest != "" && state.WantedDigest == health.AppliedConfigDigest
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
	case "synced", "failed":
		s.markAgentConfigurationDrift(ctx, serverID)
	default:
		s.signalConfigurationReconcile()
	}
}

func (s *Server) markAgentConfigurationDrift(ctx context.Context, serverID int64) {
	revision, err := s.store.ConfigurationRevision(ctx)
	if err != nil || revision == 0 {
		if err != nil {
			log.Printf("read routing revision after Agent drift server=%d: %v", serverID, err)
		}
		return
	}
	if err := s.store.MarkConfigurationSyncDrift(context.WithoutCancel(ctx), serverID, revision); err != nil {
		log.Printf("mark Agent drift server=%d: %v", serverID, err)
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
