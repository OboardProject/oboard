package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) lastAppliedSSHPlan(ctx context.Context, serverID int64) (model.SSHInboundPlan, int64, error) {
	var plan model.SSHInboundPlan
	var version int64
	var selected *model.AgentTask
	for _, kind := range []string{model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig} {
		task, err := s.store.LastSuccessfulTaskByServerType(ctx, serverID, kind)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return plan, 0, err
		}
		if task == nil || task.ConfigVersion < version {
			continue
		}
		if kind == model.AgentTaskTypeApplyDeployment {
			var payload model.DeploymentTaskPayload
			if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
				return plan, 0, err
			}
			plan = payload.SSHInbounds
			version = task.ConfigVersion
			selected = task
		} else {
			var payload model.ApplyCoreConfigTaskPayload
			if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
				return plan, 0, err
			}
			if payload.SSHInbounds != nil {
				plan = *payload.SSHInbounds
				version = task.ConfigVersion
				selected = task
			}
		}
	}
	if selected != nil && !sshTaskAuthenticationVerified(*selected, plan) {
		return model.SSHInboundPlan{}, 0, nil
	}
	return plan, version, nil
}

func (s *Server) sshConfigUnchanged(ctx context.Context, serverID int64, plan model.SSHInboundPlan) (bool, error) {
	previous, version, err := s.lastAppliedSSHPlan(ctx, serverID)
	if err != nil {
		return false, err
	}
	if version == 0 {
		return len(plan.Inbounds) == 0, nil
	}
	return sshInboundPlanDigest(previous) == sshInboundPlanDigest(plan), nil
}

func (s *Server) applyFocusedSSHState(ctx context.Context, serverID int64, task model.AgentTask, resultJSON string) error {
	var payload model.ApplyCoreConfigTaskPayload
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
		return err
	}
	if payload.SSHInbounds == nil {
		return nil
	}
	var result struct {
		SSH json.RawMessage `json:"ssh_inbounds"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return err
	}
	if len(result.SSH) == 0 || string(result.SSH) == "null" {
		return errors.New("focused apply did not confirm SSH runtime")
	}
	var host struct {
		PublicKey string `json:"host_public_key"`
	}
	if err := json.Unmarshal(result.SSH, &host); err != nil {
		return err
	}
	if host.PublicKey == "" {
		return errors.New("focused SSH apply did not confirm host identity")
	}
	signedPayload, err := json.Marshal(model.DeploymentTaskPayload{Version: task.ConfigVersion, SSHInbounds: *payload.SSHInbounds})
	if err != nil {
		return err
	}
	task.PayloadJSON = string(signedPayload)
	envelope, err := json.Marshal(map[string]any{"steps": []any{map[string]any{"key": "ssh_inbounds", "status": "succeeded", "result": result.SSH}}})
	if err != nil {
		return err
	}
	return s.applyDeploymentSSHState(ctx, serverID, task, string(envelope))
}
