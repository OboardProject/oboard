package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) GetServer(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error) {
	server, err := s.authorizedPluginServer(ctx, principal, serverID)
	if err != nil {
		return nil, err
	}
	return pluginServerView(*server), nil
}

func (s *Server) ListServers(ctx context.Context, principal application.Principal, limit int) ([]map[string]any, error) {
	maxServers := 50
	if limit <= 0 || limit > maxServers {
		limit = maxServers
	}
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, maxServers)
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.ID) {
			continue
		}
		out = append(out, pluginServerView(item))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Server) ServerStatus(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error) {
	server, err := s.authorizedPluginServer(ctx, principal, serverID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	lastSeen := ""
	stale := true
	if server.LastSeenAt != nil {
		lastSeen = server.LastSeenAt.UTC().Format(time.RFC3339Nano)
		stale = now.Sub(server.LastSeenAt.UTC()) > 3*time.Minute
	}
	agentConnected := unknownBool(s.serverAgentReachable(server))
	coreRunning := "unknown"
	if strings.TrimSpace(server.SingBoxVersion) == "" {
		coreRunning = "unknown"
	} else if server.Status == model.ServerOffline {
		coreRunning = "unknown"
	}
	inboundAvailable := "unknown"
	if strings.TrimSpace(server.ConnectivityStatus) != "" {
		inboundAvailable = server.ConnectivityStatus
	}
	configApplied := "unknown"
	if syncState, err := s.store.ConfigurationSyncState(ctx, server.ID); err == nil {
		if syncState.State == "synced" {
			configApplied = "true"
		} else if syncState.State != "" {
			configApplied = "false"
		}
	}
	reason := "observed from control-plane state"
	if stale {
		reason = "last trusted communication is stale"
	}
	return map[string]any{
		"server_id":                     formatPluginID(server.ID),
		"control_connected":             agentConnected,
		"last_trusted_communication_at": lastSeen,
		"observed_at":                   now.Format(time.RFC3339Nano),
		"status_version":                server.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"stale":                         stale,
		"maintenance":                   serverExpiresSoon(server, now),
		"capabilities":                  append([]string(nil), server.KernelCapabilities...),
		"agent_connected":               agentConnected,
		"core_running":                  coreRunning,
		"inbound_available":             inboundAvailable,
		"config_applied":                configApplied,
		"reason":                        reason,
	}, nil
}

func (s *Server) LatestMetrics(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error) {
	if _, err := s.authorizedPluginServer(ctx, principal, serverID); err != nil {
		return nil, err
	}
	samples, err := s.store.ListServerMetricSamples(ctx, serverID, 1)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return map[string]any{"server_id": formatPluginID(serverID), "exists": false, "stale": true, "observed_at": ""}, nil
	}
	sample := samples[0]
	stale := time.Now().UTC().Sub(sample.SampledAt.UTC()) > 3*time.Minute
	return map[string]any{
		"server_id":          formatPluginID(serverID),
		"exists":             true,
		"stale":              stale,
		"observed_at":        sample.SampledAt.UTC().Format(time.RFC3339Nano),
		"cpu_usage_percent":  sample.CPUUsagePercent,
		"memory_used_bytes":  sample.MemoryUsedBytes,
		"memory_total_bytes": sample.MemoryTotalBytes,
	}, nil
}

func (s *Server) GetIncident(ctx context.Context, principal application.Principal, incidentID int64) (map[string]any, error) {
	item, err := s.store.GetNodeIncident(ctx, incidentID)
	if err != nil || item == nil {
		return nil, plugin.ErrNotFound
	}
	if !principal.AllowsInt64("server_ids", item.ServerID) {
		return nil, plugin.ErrResourceOutOfScope
	}
	return map[string]any{
		"incident_id":       formatPluginID(item.ID),
		"subject_server_id": formatPluginID(item.ServerID),
		"status":            item.Status,
		"kind":              item.Kind,
		"first_offline_at":  formatOptionalTime(item.FirstOfflineAt),
		"detected_at":       formatOptionalTime(item.DetectedAt),
		"resolved_at":       formatOptionalTimePtr(item.ResolvedAt),
		"flap_count":        item.FlapCount,
	}, nil
}

func (s *Server) ServiceStatus(ctx context.Context, principal application.Principal, serverID int64, service string) (map[string]any, error) {
	if err := s.assertPluginHostAction(ctx, principal, serverID, false); err != nil {
		return nil, err
	}
	payload, err := newPluginRemoteOperation(serverID, model.RemoteOperationServiceStatus, service)
	if err != nil {
		return nil, err
	}
	task, err := s.queueAgentTask(ctx, serverID, model.AgentTaskTypeRemoteOperation, payload, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": payload.RequestID, "task_id": formatPluginID(task.ID), "accepted": true}, nil
}

func (s *Server) RestartService(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, serverID int64, service string) (map[string]any, error) {
	if err := s.assertPluginHostAction(ctx, principal, serverID, false); err != nil {
		return nil, err
	}
	if err := s.assertNoHostConflict(ctx, serverID); err != nil {
		return nil, err
	}
	payload, err := newPluginRemoteOperation(serverID, model.RemoteOperationServiceRestart, service)
	if err != nil {
		return nil, err
	}
	task, err := s.queueAgentTask(ctx, serverID, model.AgentTaskTypeRemoteOperation, payload, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": payload.RequestID, "task_id": formatPluginID(task.ID), "accepted": true, "run_id": run.UUID, "action_key": action.ActionKey}, nil
}

func (s *Server) HostPower(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, serverID int64, actionName, reason string) (map[string]any, error) {
	if err := s.assertPluginHostAction(ctx, principal, serverID, true); err != nil {
		return nil, err
	}
	if err := s.assertNoHostConflict(ctx, serverID); err != nil {
		return nil, err
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil || server == nil {
		return nil, plugin.ErrNotFound
	}
	if !s.serverAgentReachable(server) {
		return nil, plugin.ErrTargetOffline
	}
	if !containsString(server.KernelCapabilities, model.AgentCapabilityHostPower) {
		return nil, plugin.ErrCapabilityUnsupported
	}
	if s.protectsControllerHost(ctx, serverID) {
		return nil, plugin.Coded(model.PluginErrorApprovalRequired, "controller host power requires an explicit extra grant")
	}
	now := time.Now().UTC()
	operationID := "op_" + randomPluginToken()
	payload := model.HostPowerTaskPayload{
		ProtocolVersion:  model.HostPowerProtocolVersion,
		OperationID:      operationID,
		Action:           actionName,
		ServerID:         serverID,
		Source:           model.RemoteExecOriginPlugin,
		RunID:            run.UUID,
		PluginRevisionID: run.RevisionID,
		IssuedAt:         now,
		ExpiresAt:        now.Add(plugin.HostPowerTTLSeconds * time.Second),
		Reason:           strings.TrimSpace(reason),
	}
	if run.BindingID != nil {
		payload.TriggerBindingID = *run.BindingID
	}
	if run.GrantID != nil {
		payload.GrantID = *run.GrantID
	}
	digest, err := plugin.DigestJSON(payload)
	if err != nil {
		return nil, err
	}
	payload.PayloadDigest = digest
	task, err := s.queueAgentTask(ctx, serverID, model.AgentTaskTypeHostPowerAction, payload, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"operation_id": operationID,
		"task_id":      formatPluginID(task.ID),
		"accepted":     true,
		"expires_at":   payload.ExpiresAt.Format(time.RFC3339Nano),
		"stage":        model.HostPowerReceiptAccepted,
	}, nil
}

func (s *Server) SendNotification(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, channelID int64, title, body string) (map[string]any, error) {
	if channelID <= 0 {
		return nil, plugin.Coded(model.PluginErrorInvalidInput, "channel_id is required")
	}
	channel, err := s.store.GetNotificationChannel(ctx, channelID)
	if err != nil || channel == nil {
		return nil, plugin.ErrNotFound
	}
	sender := s.notificationSender
	if sender == nil {
		sender = sendNotification
	}
	if err := sender(ctx, *channel, strings.TrimSpace(title), strings.TrimSpace(body)); err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": "notify_" + run.UUID + "_" + action.ActionKey, "accepted": true, "channel_id": formatPluginID(channelID)}, nil
}

func (s *Server) GetOperation(ctx context.Context, principal application.Principal, operationID string) (map[string]any, error) {
	action, err := s.lookupPluginAction(ctx, principal, operationID)
	if err != nil {
		return nil, err
	}
	view := map[string]any{"operation_id": action.OperationID, "status": action.Status, "capability": action.Capability, "error_code": action.ErrorCode}
	if action.ChangesetID != "" {
		operation, err := s.store.GetAutomationOperation(ctx, operationID)
		if err != nil || operation.ChangesetID != action.ChangesetID || !principal.HasScope("management:"+operation.Capability) {
			return nil, plugin.ErrPermissionDenied
		}
		descriptor, ok := s.capabilities.Get(operation.Capability)
		if !ok || !plugin.ManagementCapabilityAllowed(operation.Capability) {
			return nil, plugin.ErrPermissionDenied
		}
		principal.Scopes = append([]string(nil), descriptor.RequiredScopes...)
		operation, err = s.automation.GetOperation(ctx, principal, operationID)
		if err != nil {
			return nil, err
		}
		view["status"] = operation.Status
		view["error_code"] = operation.ErrorCode
		view["changeset_id"] = action.ChangesetID
	}
	if action.TaskID != nil {
		if task, taskErr := s.store.GetTask(ctx, *action.TaskID); taskErr == nil && task != nil {
			view["task_status"] = task.Status
			view["task_id"] = formatPluginID(task.ID)
		}
	}
	return view, nil
}

func (s *Server) WaitOperation(ctx context.Context, principal application.Principal, operationID string, seconds int) (map[string]any, error) {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		view, err := s.GetOperation(ctx, principal, operationID)
		if err != nil {
			return nil, err
		}
		status, _ := view["status"].(string)
		taskStatus, _ := view["task_status"].(string)
		if isTerminalPluginAction(status) || isTerminalAgentTask(taskStatus) {
			return view, nil
		}
		if time.Now().After(deadline) {
			view["waited"] = true
			return view, nil
		}
		select {
		case <-ctx.Done():
			return view, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Server) authorizedPluginServer(ctx context.Context, principal application.Principal, serverID int64) (*model.Server, error) {
	if serverID <= 0 {
		return nil, plugin.Coded(model.PluginErrorInvalidInput, "server_id is required")
	}
	if !principal.AllowsInt64("server_ids", serverID) {
		return nil, plugin.ErrResourceOutOfScope
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil || server == nil {
		return nil, plugin.ErrNotFound
	}
	return server, nil
}

func (s *Server) assertPluginHostAction(ctx context.Context, principal application.Principal, serverID int64, power bool) error {
	if _, err := s.authorizedPluginServer(ctx, principal, serverID); err != nil {
		return err
	}
	settings := s.plugins.Settings(ctx)
	if !settings.Enabled {
		return plugin.ErrRuntimeUnavailable
	}
	if power && !settings.HostActionsEnabled {
		return plugin.ErrApprovalRequired
	}
	policy, err := s.store.GetServerPluginPolicy(ctx, serverID)
	if err != nil {
		return err
	}
	if !policy.PluginsEnabled {
		return plugin.ErrApprovalRequired
	}
	if power && !policy.PluginsPowerEnabled {
		return plugin.ErrApprovalRequired
	}
	status, err := s.store.GetServerRemoteAccessStatus(ctx, serverID)
	if err == nil {
		if power && !status.LocalAllow.HostPowerEnabled {
			return plugin.Coded(model.PluginErrorPermissionDenied, "agent local host power policy denied this operation")
		}
		if !status.LocalAllow.PluginsEnabled {
			return plugin.Coded(model.PluginErrorPermissionDenied, "agent local plugin policy denied this operation")
		}
	}
	return nil
}

func (s *Server) assertNoHostConflict(ctx context.Context, serverID int64) error {
	tasks, err := s.store.ListTasksByServer(ctx, serverID, 20)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Status != "pending" && task.Status != "running" && task.Status != "queued" {
			continue
		}
		switch task.Type {
		case model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeUpdateAgent, model.AgentTaskTypeHostPowerAction, model.AgentTaskTypeUninstallAgent:
			return plugin.Coded(model.PluginErrorConditionChanged, "a conflicting host operation is already in progress")
		}
	}
	return nil
}

func (s *Server) protectsControllerHost(ctx context.Context, serverID int64) bool {
	values, _ := s.store.ListSettings(ctx)
	raw := strings.TrimSpace(values["plugins.controller_server_id"])
	if raw == "" || raw == "0" {
		return false
	}
	id, err := strconvParseInt(raw)
	return err == nil && id == serverID
}

func (s *Server) lookupPluginAction(ctx context.Context, principal application.Principal, operationID string) (model.PluginRunAction, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return model.PluginRunAction{}, plugin.Coded(model.PluginErrorInvalidInput, "operation_id is required")
	}
	action, err := s.store.GetPluginRunActionByOperationID(ctx, operationID)
	if err != nil {
		return model.PluginRunAction{}, plugin.ErrNotFound
	}
	run, err := s.store.GetPluginRun(ctx, action.RunID)
	if err != nil {
		return model.PluginRunAction{}, err
	}
	effective, err := s.plugins.EffectivePrincipal(ctx, run)
	if err != nil {
		return model.PluginRunAction{}, err
	}
	if principal.Type == model.APIPrincipalPlugin && principal.ID != effective.ID {
		return model.PluginRunAction{}, plugin.ErrPermissionDenied
	}
	if run.CallerPrincipal != "" && run.CallerPrincipal != principal.ID && principal.Type != model.APIPrincipalPlugin {
		if principal.Role != model.RoleAdmin && !principal.HasScope("plugins:read") {
			return model.PluginRunAction{}, plugin.ErrPermissionDenied
		}
	}
	return action, nil
}

func newPluginRemoteOperation(serverID int64, kind, service string) (model.RemoteOperationTaskPayload, error) {
	if service != "" && service != "oboard-agent" && service != "oboard-sb" && service != "all" {
		return model.RemoteOperationTaskPayload{}, plugin.Coded(model.PluginErrorInvalidInput, "service is not an OBoard managed unit")
	}
	requestID, err := security.RandomToken(16)
	if err != nil {
		return model.RemoteOperationTaskPayload{}, err
	}
	now := time.Now().UTC()
	return model.RemoteOperationTaskPayload{
		RequestID: "sop_" + requestID, Origin: model.RemoteExecOriginPlugin, Kind: kind,
		ServerID: serverID, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Service: service,
	}, nil
}

func pluginServerView(server model.Server) map[string]any {
	return map[string]any{
		"server_id":        formatPluginID(server.ID),
		"name":             server.Name,
		"status":           server.Status,
		"region_code":      server.RegionCode,
		"public_ipv4":      server.PublicIPv4,
		"public_ipv6":      server.PublicIPv6,
		"agent_version":    server.AgentVersion,
		"agent_build":      server.AgentBuild,
		"sing_box_version": server.SingBoxVersion,
		"last_seen_at":     formatOptionalTimePtr(server.LastSeenAt),
	}
}

func unknownBool(value bool) any {
	if value {
		return true
	}
	return "unknown"
}

func serverExpiresSoon(server *model.Server, now time.Time) bool {
	return server.ExpiresAt != nil && !server.ExpiresAt.After(now)
}

func formatPluginID(id int64) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatOptionalTime(*value)
}

func isTerminalPluginAction(status string) bool {
	switch status {
	case model.PluginActionSucceeded, model.PluginActionFailed, model.PluginActionCancelled, model.PluginActionUnknown, model.PluginActionPartial:
		return true
	default:
		return false
	}
}

func isTerminalAgentTask(status string) bool {
	switch status {
	case "succeeded", "failed", "rollback_failed":
		return true
	default:
		return false
	}
}

func randomPluginToken() string {
	token, err := security.RandomToken(12)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return token
}

func strconvParseInt(raw string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(strings.TrimSpace(raw), &id)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}
