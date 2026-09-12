package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/OboardProject/oboard/internal/model"
)

// deployedSSHPlanKey identifies the newest succeeded task of one type on one
// server, which is the only task any SSH readiness check ever reads.
type deployedSSHPlanKey struct {
	serverID int64
	taskType string
}

// deployedSSHPlanProjection is everything an SSH readiness check needs from a
// succeeded task: the deployed plan, whether that task carried one at all, and
// whether the Agent confirmed authentication for it.
type deployedSSHPlanProjection struct {
	taskID        int64
	configVersion int64
	plan          model.SSHInboundPlan
	hasPlan       bool
	verified      bool
}

// deployedSSHPlanCache memoizes that projection per server and task type.
//
// An apply_deployment payload embeds the whole generated kernel configuration,
// so decoding it to read the SSH section is expensive out of all proportion to
// the few fields that are wanted. Subscription rendering asks for the deployed
// SSH plan of every server the pulling account holds an SSH credential on, so
// an account with SSH nodes used to pay that decode on every pull while an
// account without them paid nothing.
//
// The payload and result of a succeeded task are immutable, so the task id is a
// complete validity key: a newer deployment replaces the entry, and because the
// key is the server and type rather than the task, the map stays bounded by the
// fleet size instead of growing with deployment history.
type deployedSSHPlanCache struct {
	mu          sync.Mutex
	projections map[deployedSSHPlanKey]deployedSSHPlanProjection
	decodes     atomic.Uint64
}

func (c *deployedSSHPlanCache) lookup(key deployedSSHPlanKey, taskID int64) (deployedSSHPlanProjection, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	projection, ok := c.projections[key]
	return projection, ok && projection.taskID == taskID
}

func (c *deployedSSHPlanCache) store(key deployedSSHPlanKey, projection deployedSSHPlanProjection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.projections == nil {
		c.projections = map[deployedSSHPlanKey]deployedSSHPlanProjection{}
	}
	c.projections[key] = projection
}

// deployedSSHPlanProjectionFor resolves the newest succeeded task of one type
// and returns its SSH projection, decoding the payload only when the memoized
// entry belongs to an older task.
func (s *Server) deployedSSHPlanProjectionFor(ctx context.Context, serverID int64, taskType string) (deployedSSHPlanProjection, bool, error) {
	taskID, configVersion, err := s.store.LastSuccessfulTaskRefByServerType(ctx, serverID, taskType)
	if errors.Is(err, sql.ErrNoRows) {
		return deployedSSHPlanProjection{}, false, nil
	}
	if err != nil {
		return deployedSSHPlanProjection{}, false, err
	}
	key := deployedSSHPlanKey{serverID: serverID, taskType: taskType}
	if projection, ok := s.deployedSSHPlans.lookup(key, taskID); ok {
		return projection, true, nil
	}
	payloadJSON, resultJSON, err := s.store.TaskDocuments(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return deployedSSHPlanProjection{}, false, nil
	}
	if err != nil {
		return deployedSSHPlanProjection{}, false, err
	}
	s.deployedSSHPlans.decodes.Add(1)
	projection := deployedSSHPlanProjection{taskID: taskID, configVersion: configVersion}
	if taskType == model.AgentTaskTypeApplyDeployment {
		var payload model.DeploymentTaskPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return deployedSSHPlanProjection{}, false, err
		}
		projection.plan, projection.hasPlan = payload.SSHInbounds, true
	} else {
		var payload model.ApplyCoreConfigTaskPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return deployedSSHPlanProjection{}, false, err
		}
		if payload.SSHInbounds != nil {
			projection.plan, projection.hasPlan = *payload.SSHInbounds, true
		}
	}
	projection.verified = sshTaskAuthenticationVerified(model.AgentTask{ID: taskID, ServerID: serverID, Type: taskType, ResultJSON: resultJSON}, projection.plan)
	s.deployedSSHPlans.store(key, projection)
	return projection, true, nil
}

func (s *Server) lastAppliedSSHPlan(ctx context.Context, serverID int64) (model.SSHInboundPlan, int64, error) {
	var plan model.SSHInboundPlan
	var version int64
	selected := false
	verified := false
	for _, kind := range []string{model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig} {
		projection, ok, err := s.deployedSSHPlanProjectionFor(ctx, serverID, kind)
		if err != nil {
			return model.SSHInboundPlan{}, 0, err
		}
		if !ok || projection.configVersion < version || !projection.hasPlan {
			continue
		}
		plan = projection.plan
		version = projection.configVersion
		verified = projection.verified
		selected = true
	}
	if selected && !verified {
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
