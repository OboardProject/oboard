package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) sshTaskSuperseded(ctx context.Context, task model.AgentTask) (bool, error) {
	latest, err := s.store.LatestSSHDeploymentTask(ctx, task.ServerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return latest.ConfigVersion > task.ConfigVersion || latest.ConfigVersion == task.ConfigVersion && task.ID > 0 && latest.ID > task.ID, nil
}

func (s *Server) validateSSHAuthenticationTaskResult(ctx context.Context, task model.AgentTask, status, result string) (string, string, error) {
	var plan *model.SSHInboundPlan
	switch task.Type {
	case model.AgentTaskTypeApplyDeployment:
		var payload model.DeploymentTaskPayload
		if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
			return status, result, err
		}
		plan = &payload.SSHInbounds
	case model.AgentTaskTypeApplyCoreConfig:
		var payload model.ApplyCoreConfigTaskPayload
		if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
			return status, result, err
		}
		plan = payload.SSHInbounds
	}
	if plan == nil || len(plan.Inbounds) == 0 {
		return status, result, nil
	}
	if host, err := s.store.GetSSHServerHostKey(ctx, task.ServerID); err == nil && task.ConfigVersion > 0 && host.ConfigVersion > task.ConfigVersion {
		return status, result, nil
	}
	if stale, err := s.sshTaskSuperseded(ctx, task); stale || err != nil {
		return status, result, err
	}
	task.ResultJSON = result
	if status == "succeeded" && !sshTaskAuthenticationVerified(task, *plan) {
		status = "failed"
		result = `{"error":"SSH authentication verification missing or incomplete; update Agent and redeploy"}`
	}
	if status != "succeeded" {
		if err := s.store.ClearSSHDeploymentState(ctx, task); err != nil {
			return status, result, err
		}
	}
	return status, result, nil
}

func sshTaskAuthenticationVerified(task model.AgentTask, plan model.SSHInboundPlan) bool {
	var raw json.RawMessage
	if task.Type == model.AgentTaskTypeApplyDeployment {
		var report struct {
			Steps []struct {
				Key    string          `json:"key"`
				Status string          `json:"status"`
				Result json.RawMessage `json:"result"`
			} `json:"steps"`
		}
		if json.Unmarshal([]byte(task.ResultJSON), &report) != nil {
			return false
		}
		for _, step := range report.Steps {
			if step.Key != "ssh_inbounds" {
				continue
			}
			if raw != nil || (step.Status != "succeeded" && step.Status != "skipped") {
				return false
			}
			raw = step.Result
		}
	} else if task.Type == model.AgentTaskTypeApplyCoreConfig {
		var report struct {
			SSH json.RawMessage `json:"ssh_inbounds"`
		}
		if json.Unmarshal([]byte(task.ResultJSON), &report) != nil {
			return false
		}
		raw = report.SSH
	} else {
		return false
	}
	var verification model.SSHAuthenticationVerification
	if json.Unmarshal(raw, &verification) != nil || !verification.AuthenticationVerified || verification.Version != plan.Version || verification.AuthenticationPlanDigest != model.SSHAuthenticationPlanDigest(plan) {
		return false
	}
	wanted := 0
	for _, inbound := range plan.Inbounds {
		if !inbound.Enabled {
			continue
		}
		for _, user := range inbound.Users {
			if user.Enabled {
				wanted++
			}
		}
	}
	return verification.AuthenticatedUsers >= 0 && verification.AuthenticatedUsers <= wanted && verification.RejectedUsers >= 0 && verification.RejectedUsers == wanted-verification.AuthenticatedUsers
}

func (s *Server) annotateSSHUserDelivery(ctx context.Context, server *model.Server) {
	items := []model.Server{*server}
	s.annotateSSHUserDeliveryStatuses(ctx, items)
	*server = items[0]
}

func (s *Server) annotateSSHUserDeliveryStatuses(ctx context.Context, servers []model.Server) {
	if len(servers) == 0 {
		return
	}
	snapshot, err := s.routingSnapshot(ctx)
	if err != nil {
		for i := range servers {
			servers[i].UsersConfirmed = false
			servers[i].UsersPendingReason = "delivery_state_unavailable"
		}
		return
	}
	sshServers := map[int64]bool{}
	for _, inbound := range snapshot.data.Inbounds {
		if inbound.Protocol == model.ProtocolSSH && inbound.Enabled {
			sshServers[inbound.ServerID] = true
		}
	}
	if len(sshServers) == 0 {
		return
	}
	data, loadErr := s.loadProxyCredentialData(ctx, snapshot.data)
	for i := range servers {
		server := &servers[i]
		if !sshServers[server.ID] {
			continue
		}
		ready := false
		if loadErr == nil {
			plan, planErr := buildSSHInboundPlan(0, *server, data, snapshot.snapshot.InboundUserBindings(), snapshot.snapshot.ProxyPathUserBindings(), nil)
			if planErr == nil {
				_, deployed, matched, lookupErr := s.matchingDeployedSSHPlan(ctx, server.ID, plan)
				if lookupErr == nil && matched && sshInboundPlanDigest(plan) == sshInboundPlanDigest(deployed) {
					wanted, wantedErr := s.sshPasswordDeploymentsFromPlan(server.ID, plan)
					applied, appliedErr := s.sshPasswordDeploymentsFromPlan(server.ID, deployed)
					if wantedErr == nil && appliedErr == nil && len(wanted) == len(applied) {
						ready = true
						for j := range wanted {
							ready = ready && matchingSSHPasswordDeployment(applied[j], wanted[j])
						}
					}
				}
			}
		}
		if !ready {
			server.UsersConfirmed = false
			server.UsersPendingReason = "ssh_authentication_unverified"
			server.UsersFallback = "apply_core_config"
		}
	}
}
