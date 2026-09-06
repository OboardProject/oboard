package controller

import (
	"context"
	"encoding/json"

	"github.com/OboardProject/oboard/internal/model"
)

func serverSupportsAuthorizationLease(server model.Server) bool {
	for _, capability := range server.KernelCapabilities {
		if capability == model.AgentCapabilityAuthorizationLease {
			return true
		}
	}
	return false
}

// A revoke does not wait for certificate preparation, topology validation or a
// kernel restart. Offline nodes instead exhaust their absolute authorization lease.
func (s *Server) queueAuthorizationRefresh(ctx context.Context, serverIDs []int64, reason string) error {
	pending, err := s.store.ListPendingTasksByType(ctx, model.AgentTaskTypeApplyTrafficPolicy)
	if err != nil {
		return err
	}
	for _, id := range serverIDs {
		server, err := s.store.GetServer(ctx, id)
		if err != nil {
			return err
		}
		if server.AgentID == "" || server.Status != model.ServerOnline || !serverSupportsAuthorizationLease(*server) {
			continue
		}
		lease, err := s.currentAuthorizationLease(ctx, id)
		if err != nil {
			return err
		}
		payload := model.ApplyTrafficPolicyTaskPayload{Authorization: lease, Reason: reason, Policies: map[string]model.TrafficRuntimePolicy{}}
		if _, err := s.queueAgentTask(ctx, id, model.AgentTaskTypeApplyTrafficPolicy, payload, lease.Revision); err != nil {
			return err
		}
		for _, task := range pending {
			if task.ServerID != id {
				continue
			}
			var previous model.ApplyTrafficPolicyTaskPayload
			if json.Unmarshal([]byte(task.PayloadJSON), &previous) == nil && previous.Authorization != nil && previous.PolicyRevision == 0 && len(previous.Policies) == 0 {
				if err := s.store.SupersedePendingTask(ctx, task.ID, "授权状态已由新任务取代"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
