package controller

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func userIdentityChanged(before, after model.User) bool {
	return before.Username != after.Username ||
		before.Role != after.Role ||
		before.Status != after.Status ||
		before.ProxyUUID != after.ProxyUUID ||
		before.ProxyPassword != after.ProxyPassword ||
		before.LegacyProxyEnabled != after.LegacyProxyEnabled ||
		before.DeviceLimit != after.DeviceLimit
}

func userTrafficPolicyChanged(before, after model.User) bool {
	return before.SpeedLimitMbps != after.SpeedLimitMbps ||
		before.TrafficLimitBytes != after.TrafficLimitBytes ||
		before.TrafficResetMode != after.TrafficResetMode ||
		before.TrafficResetDay != after.TrafficResetDay
}

func serverSupportsTrafficPolicy(server model.Server) bool {
	for _, capability := range server.KernelCapabilities {
		if capability == model.AgentCapabilityTrafficPolicy {
			return true
		}
	}
	return false
}

func (s *Server) syncUserChange(ctx context.Context, before, after model.User) {
	identity := userIdentityChanged(before, after)
	traffic := userTrafficPolicyChanged(before, after)
	if !identity && !traffic {
		return
	}
	serverIDs, err := s.userAccountingServerIDs(ctx, after.ID)
	if err != nil {
		logConfigurationError("user accounting servers", err)
		return
	}
	if identity {
		if err := s.queueCoreConfigRefreshForServers(ctx, serverIDs, "user_credentials_changed"); err != nil {
			logConfigurationError("queue core config for user identity", err)
		}
		return
	}
	if err := s.queueApplyTrafficPolicy(ctx, serverIDs, "user_policy_changed", map[int64]bool{after.ID: true}); err != nil {
		logConfigurationError("queue traffic policy", err)
	}
}

func (s *Server) userAccountingServerIDs(ctx context.Context, userID int64) ([]int64, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	// Identity changes such as disable/delete must still resolve the servers that
	// authenticated this user. The access snapshot otherwise drops inactive users.
	for i := range data.Users {
		if data.Users[i].ID == userID {
			data.Users[i].Status = "active"
			break
		}
	}
	snapshot, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return nil, err
	}
	return core.TrafficAccountingServerIDsForUser(userID, data.Servers, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, snapshot.InboundUserBindings(), snapshot.ProxyPathUserBindings()), nil
}

func (s *Server) queueApplyTrafficPolicy(ctx context.Context, serverIDs []int64, reason string, userFilter map[int64]bool) error {
	if len(serverIDs) == 0 {
		return nil
	}
	revision, err := s.store.BumpTrafficPolicyRevision(ctx)
	if err != nil {
		return err
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	legacy := make([]int64, 0)
	for _, serverID := range serverIDs {
		if serverID <= 0 {
			continue
		}
		server, ok := serverByID(data.Servers, serverID)
		if !ok || strings.TrimSpace(server.AgentID) == "" {
			continue
		}
		generated, err := s.generateServerCoreConfigWithLedger(ctx, server, data, ledger)
		if err != nil {
			return err
		}
		policies := filterTrafficPolicies(generated.TrafficPolicies, userFilter)
		if len(policies) == 0 {
			continue
		}
		if !serverSupportsTrafficPolicy(server) {
			legacy = append(legacy, server.ID)
			continue
		}
		payload := model.ApplyTrafficPolicyTaskPayload{PolicyRevision: int64(revision), Reason: reason, Policies: trafficPoliciesByString(policies)}
		if _, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeApplyTrafficPolicy, payload, int64(revision)); err != nil {
			return err
		}
		s.applyTrafficPolicyCreatedTotal.Add(1)
		s.trafficPolicyUpdatesTotal.Add(1)
	}
	if len(legacy) > 0 {
		return s.queueCoreConfigRefreshForServers(ctx, legacy, reason)
	}
	return nil
}

func (s *Server) queueApplyTrafficPolicyForAllAccounting(ctx context.Context, reason string) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(data.Servers))
	for _, server := range data.Servers {
		if strings.TrimSpace(server.AgentID) != "" {
			ids = append(ids, server.ID)
		}
	}
	return s.queueApplyTrafficPolicy(ctx, ids, reason, nil)
}

func filterTrafficPolicies(in map[int64]model.TrafficRuntimePolicy, userFilter map[int64]bool) map[int64]model.TrafficRuntimePolicy {
	if len(in) == 0 || len(userFilter) == 0 {
		return in
	}
	out := make(map[int64]model.TrafficRuntimePolicy, len(userFilter))
	for userID, policy := range in {
		if userFilter[userID] {
			out[userID] = policy
		}
	}
	return out
}

func trafficPoliciesByString(in map[int64]model.TrafficRuntimePolicy) map[string]model.TrafficRuntimePolicy {
	out := make(map[string]model.TrafficRuntimePolicy, len(in))
	for userID, policy := range in {
		out[strconv.FormatInt(userID, 10)] = policy
	}
	return out
}

func (s *Server) cleanupTrafficStormPendingDeployments(ctx context.Context) {
	tasks, err := s.store.ListPendingTasksByType(ctx, model.AgentTaskTypeApplyDeployment)
	if err != nil || len(tasks) == 0 {
		if err != nil {
			logConfigurationError("list pending deployments", err)
		}
		return
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return
	}
	forwards, err := s.store.ListPortForwards(ctx)
	if err != nil {
		return
	}
	tunnels, err := s.store.ListTunnels(ctx)
	if err != nil {
		return
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	if derived, err := core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger); err == nil {
		forwards = append(forwards, derived...)
	}
	if derived, err := core.DerivedTunnelsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger); err == nil {
		tunnels = append(tunnels, derived...)
	}
	for _, task := range tasks {
		var payload model.DeploymentTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) != nil {
			continue
		}
		server, ok := serverByID(data.Servers, task.ServerID)
		if !ok {
			continue
		}
		current, err := s.currentDeploymentProjection(ctx, server, data, forwards, tunnels, ledger)
		if err != nil {
			continue
		}
		last := lastDeploymentProjection(payload, payload.Config.Config, server)
		if current.DataPlane == last.DataPlane && current.PortForwards == last.PortForwards && current.Tunnels == last.Tunnels && current.SSH == last.SSH && current.Assets == last.Assets {
			_ = s.store.SupersedePendingTask(ctx, task.ID, "semantic_noop: runtime traffic policy only")
		}
	}
}
