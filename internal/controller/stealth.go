package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// queueApplyStealth enqueues the apply_stealth task for a server whose
// security-process switch changed. The task carries only the desired state:
// the Agent generates every name and key locally, because the hidden layout
// must never be described on the wire or stored in the Controller database.
//
// The Agent must advertise stealth_v1; an older Agent cannot apply the task
// and would fail it, so the caller is told instead of queueing a guaranteed
// failure.
func (s *Server) queueApplyStealth(ctx context.Context, server model.Server, enable bool) (model.AgentTask, error) {
	if !serverSupportsCapability(server, "stealth_v1") {
		return model.AgentTask{}, fmt.Errorf("Agent 未上报 stealth_v1 能力，请先更新 Agent 再切换安全进程")
	}
	active, err := s.store.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyStealth)
	if err == nil {
		// An identical switch already in flight is a dedup; an opposite
		// toggle queues behind it, because the Agent serializes tasks and
		// applies the newest desired state last.
		var pending model.ApplyStealthTaskPayload
		if json.Unmarshal([]byte(active.PayloadJSON), &pending) == nil && pending.Enable == enable {
			return *active, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.AgentTask{}, err
	}
	return s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeApplyStealth, model.ApplyStealthTaskPayload{Enable: enable}, 0)
}

// serverSupportsCapability reports whether the server's last reported agent
// capabilities include the given one.
func serverSupportsCapability(server model.Server, capability string) bool {
	for _, item := range server.KernelCapabilities {
		if strings.TrimSpace(item) == capability {
			return true
		}
	}
	return false
}

// maybeQueueStealthSwitch queues the apply_stealth task after a server update
// when the switch changed on an enrolled, online server. The switch itself is
// saved either way: for an unenrolled server it only shapes the install
// command the panel will generate.
func (s *Server) maybeQueueStealthSwitch(ctx context.Context, before, after model.Server) (model.AgentTask, bool, error) {
	if before.StealthEnabled == after.StealthEnabled {
		return model.AgentTask{}, false, nil
	}
	if strings.TrimSpace(after.AgentID) == "" || after.Status == model.ServerOffline {
		return model.AgentTask{}, false, nil
	}
	task, err := s.queueApplyStealth(ctx, after, after.StealthEnabled)
	if err != nil {
		return model.AgentTask{}, false, err
	}
	return task, true, nil
}
