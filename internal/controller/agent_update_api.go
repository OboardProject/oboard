package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/version"
)

func (s *Server) agentUpdatesStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	status, err := s.agentFleetStatus(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, status)
}

func (s *Server) agentUpdatesPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	status, err := s.setAgentFleetPaused(r.Context(), true, "管理员已暂停 Agent 滚动更新")
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "pause", "agent_updates", version.AgentBuild)
	write(w, http.StatusOK, status)
}

func (s *Server) agentUpdatesResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	status, err := s.setAgentFleetPaused(r.Context(), false, "")
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if s.agentUpdates != nil {
		s.agentUpdates.Wake()
	}
	auditReq(s, r, "resume", "agent_updates", version.AgentBuild)
	write(w, http.StatusOK, status)
}

func (s *Server) agentUpdatesRetryFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	target := strings.TrimSpace(version.AgentBuild)
	if err := s.store.ClearAgentUpdateRetriesForBuild(r.Context(), target); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	state, err := s.store.GetAgentFleetState(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	state.Paused = false
	state.LastPauseReason = ""
	state.TargetBuild = target
	if err := s.store.SaveAgentFleetState(r.Context(), state); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if s.agentUpdates != nil {
		s.agentUpdates.Fill(r.Context(), true)
		s.agentUpdates.Wake()
	}
	status, err := s.agentFleetStatus(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "retry_failed", "agent_updates", target)
	write(w, http.StatusOK, status)
}

func (s *Server) setAgentFleetPaused(ctx context.Context, paused bool, reason string) (map[string]any, error) {
	state, err := s.store.GetAgentFleetState(ctx)
	if err != nil {
		return nil, err
	}
	state.Paused = paused
	state.LastPauseReason = strings.TrimSpace(reason)
	if strings.TrimSpace(state.TargetBuild) == "" {
		state.TargetBuild = strings.TrimSpace(version.AgentBuild)
	}
	if err := s.store.SaveAgentFleetState(ctx, state); err != nil {
		return nil, err
	}
	s.publishRealtime("agent-updates")
	return s.agentFleetStatus(ctx)
}

func (s *Server) registerAgentUpdateOperations() {
	s.automation.RegisterValidator("agent_updates.pause", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return map[string]any{"paused": true}, nil
	})
	s.automation.RegisterRevisionResolver("agent_updates.pause", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{"agent_updates:singleton": "fleet"}, nil
	})
	s.automation.Register("agent_updates.pause", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return s.setAgentFleetPaused(ctx, true, "管理员已暂停 Agent 滚动更新")
	})
	s.automation.RegisterValidator("agent_updates.resume", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return map[string]any{"paused": false}, nil
	})
	s.automation.RegisterRevisionResolver("agent_updates.resume", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{"agent_updates:singleton": "fleet"}, nil
	})
	s.automation.Register("agent_updates.resume", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		status, err := s.setAgentFleetPaused(ctx, false, "")
		if err != nil {
			return nil, err
		}
		if s.agentUpdates != nil {
			s.agentUpdates.Wake()
		}
		return status, nil
	})
	s.automation.RegisterValidator("agent_updates.retry_failed", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return map[string]any{"retry": true}, nil
	})
	s.automation.RegisterRevisionResolver("agent_updates.retry_failed", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{"agent_updates:singleton": "fleet"}, nil
	})
	s.automation.Register("agent_updates.retry_failed", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		target := strings.TrimSpace(version.AgentBuild)
		if err := s.store.ClearAgentUpdateRetriesForBuild(ctx, target); err != nil {
			return nil, err
		}
		state, err := s.store.GetAgentFleetState(ctx)
		if err != nil {
			return nil, err
		}
		state.Paused = false
		state.LastPauseReason = ""
		state.TargetBuild = target
		if err := s.store.SaveAgentFleetState(ctx, state); err != nil {
			return nil, err
		}
		if s.agentUpdates != nil {
			s.agentUpdates.Fill(ctx, true)
			s.agentUpdates.Wake()
		}
		return s.agentFleetStatus(ctx)
	})
}
