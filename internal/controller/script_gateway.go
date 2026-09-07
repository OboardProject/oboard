package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/scripting"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) GetServer(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error) {
	server, err := s.authorizedScriptServer(ctx, principal, serverID)
	if err != nil {
		return nil, err
	}
	return scriptServerView(*server), nil
}

func (s *Server) ListServers(ctx context.Context, principal application.Principal, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, limit)
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.ID) {
			continue
		}
		out = append(out, scriptServerView(item))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Server) ServerStatus(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error) {
	server, err := s.authorizedScriptServer(ctx, principal, serverID)
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
		"server_id":                      formatScriptID(server.ID),
		"control_connected":              agentConnected,
		"last_trusted_communication_at":  lastSeen,
		"observed_at":                    now.Format(time.RFC3339Nano),
		"status_version":                 server.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"stale":                          stale,
		"maintenance":                    serverExpiresSoon(server, now),
		"capabilities":                   append([]string(nil), server.KernelCapabilities...),
		"agent_connected":                agentConnected,
		"core_running":                   coreRunning,
		"inbound_available":              inboundAvailable,
		"config_applied":                 configApplied,
		"reason":                         reason,
	}, nil
}

func (s *Server) LatestMetrics(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error) {
	if _, err := s.authorizedScriptServer(ctx, principal, serverID); err != nil {
		return nil, err
	}
	samples, err := s.store.ListServerMetricSamples(ctx, serverID, 1)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return map[string]any{"server_id": formatScriptID(serverID), "exists": false, "stale": true, "observed_at": ""}, nil
	}
	sample := samples[0]
	stale := time.Now().UTC().Sub(sample.SampledAt.UTC()) > 3*time.Minute
	return map[string]any{
		"server_id":          formatScriptID(serverID),
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
		return nil, scripting.ErrNotFound
	}
	if !principal.AllowsInt64("server_ids", item.ServerID) {
		return nil, scripting.ErrResourceOutOfScope
	}
	return map[string]any{
		"incident_id":      formatScriptID(item.ID),
		"subject_server_id": formatScriptID(item.ServerID),
		"status":           item.Status,
		"kind":             item.Kind,
		"first_offline_at": formatOptionalTime(item.FirstOfflineAt),
		"detected_at":      formatOptionalTime(item.DetectedAt),
		"resolved_at":      formatOptionalTimePtr(item.ResolvedAt),
		"flap_count":       item.FlapCount,
	}, nil
}

func (s *Server) ServiceStatus(ctx context.Context, principal application.Principal, serverID int64, service string) (map[string]any, error) {
	if err := s.assertScriptHostAction(ctx, principal, serverID, false); err != nil {
		return nil, err
	}
	payload, err := newScriptRemoteOperation(serverID, model.RemoteOperationServiceStatus, service)
	if err != nil {
		return nil, err
	}
	task, err := s.queueAgentTask(ctx, serverID, model.AgentTaskTypeRemoteOperation, payload, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": payload.RequestID, "task_id": formatScriptID(task.ID), "accepted": true}, nil
}

func (s *Server) RestartService(ctx context.Context, principal application.Principal, run model.ScriptRun, action model.ScriptRunAction, serverID int64, service string) (map[string]any, error) {
	if err := s.assertScriptHostAction(ctx, principal, serverID, false); err != nil {
		return nil, err
	}
	if err := s.assertNoHostConflict(ctx, serverID); err != nil {
		return nil, err
	}
	payload, err := newScriptRemoteOperation(serverID, model.RemoteOperationServiceRestart, service)
	if err != nil {
		return nil, err
	}
	task, err := s.queueAgentTask(ctx, serverID, model.AgentTaskTypeRemoteOperation, payload, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": payload.RequestID, "task_id": formatScriptID(task.ID), "accepted": true, "run_id": run.UUID, "action_key": action.ActionKey}, nil
}

func (s *Server) HostPower(ctx context.Context, principal application.Principal, run model.ScriptRun, action model.ScriptRunAction, serverID int64, actionName, reason string) (map[string]any, error) {
	if err := s.assertScriptHostAction(ctx, principal, serverID, true); err != nil {
		return nil, err
	}
	if err := s.assertNoHostConflict(ctx, serverID); err != nil {
		return nil, err
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil || server == nil {
		return nil, scripting.ErrNotFound
	}
	if !s.serverAgentReachable(server) {
		return nil, scripting.ErrTargetOffline
	}
	if !containsString(server.KernelCapabilities, model.AgentCapabilityHostPower) {
		return nil, scripting.ErrCapabilityUnsupported
	}
	if s.protectsControllerHost(ctx, serverID) {
		return nil, scripting.Coded(model.ScriptErrorApprovalRequired, "controller host power requires an explicit extra grant")
	}
	now := time.Now().UTC()
	operationID := "op_" + randomScriptToken()
	payload := model.HostPowerTaskPayload{
		ProtocolVersion:  model.HostPowerProtocolVersion,
		OperationID:      operationID,
		Action:           actionName,
		ServerID:         serverID,
		Source:           model.RemoteExecOriginScript,
		RunID:            run.UUID,
		ScriptRevisionID: run.RevisionID,
		IssuedAt:         now,
		ExpiresAt:        now.Add(scripting.HostPowerTTLSeconds * time.Second),
		Reason:           strings.TrimSpace(reason),
	}
	if run.BindingID != nil {
		payload.TriggerBindingID = *run.BindingID
	}
	if run.GrantID != nil {
		payload.GrantID = *run.GrantID
	}
	digest, err := scripting.DigestJSON(payload)
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
		"task_id":      formatScriptID(task.ID),
		"accepted":     true,
		"expires_at":   payload.ExpiresAt.Format(time.RFC3339Nano),
		"stage":        model.HostPowerReceiptAccepted,
	}, nil
}

func (s *Server) SendNotification(ctx context.Context, principal application.Principal, run model.ScriptRun, action model.ScriptRunAction, channelID int64, title, body string) (map[string]any, error) {
	if channelID <= 0 {
		return nil, scripting.Coded(model.ScriptErrorInvalidInput, "channel_id is required")
	}
	channel, err := s.store.GetNotificationChannel(ctx, channelID)
	if err != nil || channel == nil {
		return nil, scripting.ErrNotFound
	}
	sender := s.notificationSender
	if sender == nil {
		sender = sendNotification
	}
	if err := sender(ctx, *channel, strings.TrimSpace(title), strings.TrimSpace(body)); err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": "notify_" + run.UUID + "_" + action.ActionKey, "accepted": true, "channel_id": formatScriptID(channelID)}, nil
}

func (s *Server) GetOperation(ctx context.Context, principal application.Principal, operationID string) (map[string]any, error) {
	action, err := s.lookupScriptAction(ctx, principal, operationID)
	if err != nil {
		return nil, err
	}
	view := map[string]any{"operation_id": action.OperationID, "status": action.Status, "capability": action.Capability, "error_code": action.ErrorCode}
	if action.TaskID != nil {
		if task, taskErr := s.store.GetTask(ctx, *action.TaskID); taskErr == nil && task != nil {
			view["task_status"] = task.Status
			view["task_id"] = formatScriptID(task.ID)
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
		if isTerminalScriptAction(status) || isTerminalAgentTask(taskStatus) {
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

func (s *Server) authorizedScriptServer(ctx context.Context, principal application.Principal, serverID int64) (*model.Server, error) {
	if serverID <= 0 {
		return nil, scripting.Coded(model.ScriptErrorInvalidInput, "server_id is required")
	}
	if !principal.AllowsInt64("server_ids", serverID) {
		return nil, scripting.ErrResourceOutOfScope
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil || server == nil {
		return nil, scripting.ErrNotFound
	}
	return server, nil
}

func (s *Server) assertScriptHostAction(ctx context.Context, principal application.Principal, serverID int64, power bool) error {
	if _, err := s.authorizedScriptServer(ctx, principal, serverID); err != nil {
		return err
	}
	settings := s.scripts.Settings(ctx)
	if !settings.Enabled {
		return scripting.ErrRuntimeUnavailable
	}
	if power && !settings.HostActionsEnabled {
		return scripting.ErrApprovalRequired
	}
	policy, err := s.store.GetServerScriptPolicy(ctx, serverID)
	if err != nil {
		return err
	}
	if !policy.ScriptsEnabled {
		return scripting.ErrApprovalRequired
	}
	if power && !policy.ScriptsPowerEnabled {
		return scripting.ErrApprovalRequired
	}
	status, err := s.store.GetServerRemoteAccessStatus(ctx, serverID)
	if err == nil {
		if power && !status.LocalAllow.HostPowerEnabled {
			return scripting.Coded(model.ScriptErrorPermissionDenied, "agent local host power policy denied this operation")
		}
		if !status.LocalAllow.ScriptsEnabled {
			return scripting.Coded(model.ScriptErrorPermissionDenied, "agent local script policy denied this operation")
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
			return scripting.Coded(model.ScriptErrorConditionChanged, "a conflicting host operation is already in progress")
		}
	}
	return nil
}

func (s *Server) protectsControllerHost(ctx context.Context, serverID int64) bool {
	values, _ := s.store.ListSettings(ctx)
	raw := strings.TrimSpace(values["scripts.controller_server_id"])
	if raw == "" || raw == "0" {
		return false
	}
	id, err := strconvParseInt(raw)
	return err == nil && id == serverID
}

func (s *Server) lookupScriptAction(ctx context.Context, principal application.Principal, operationID string) (model.ScriptRunAction, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return model.ScriptRunAction{}, scripting.Coded(model.ScriptErrorInvalidInput, "operation_id is required")
	}
	action, err := s.store.GetScriptRunActionByOperationID(ctx, operationID)
	if err != nil {
		return model.ScriptRunAction{}, scripting.ErrNotFound
	}
	run, err := s.store.GetScriptRun(ctx, action.RunID)
	if err != nil {
		return model.ScriptRunAction{}, err
	}
	effective, err := s.scripts.EffectivePrincipal(ctx, run)
	if err != nil {
		return model.ScriptRunAction{}, err
	}
	if run.CallerPrincipal != "" && run.CallerPrincipal != principal.ID && principal.Type != model.APIPrincipalScript {
		if principal.Role != model.RoleAdmin && !principal.HasScope("scripts:read") {
			return model.ScriptRunAction{}, scripting.ErrPermissionDenied
		}
	}
	_ = effective
	return action, nil
}

func newScriptRemoteOperation(serverID int64, kind, service string) (model.RemoteOperationTaskPayload, error) {
	if service != "" && service != "oboard-agent" && service != "oboard-sb" && service != "all" {
		return model.RemoteOperationTaskPayload{}, scripting.Coded(model.ScriptErrorInvalidInput, "service is not an OBoard managed unit")
	}
	requestID, err := security.RandomToken(16)
	if err != nil {
		return model.RemoteOperationTaskPayload{}, err
	}
	now := time.Now().UTC()
	return model.RemoteOperationTaskPayload{
		RequestID: "sop_" + requestID, Origin: model.RemoteExecOriginScript, Kind: kind,
		ServerID: serverID, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Service: service,
	}, nil
}

func scriptServerView(server model.Server) map[string]any {
	return map[string]any{
		"server_id":        formatScriptID(server.ID),
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

func formatScriptID(id int64) string {
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

func isTerminalScriptAction(status string) bool {
	switch status {
	case model.ScriptActionSucceeded, model.ScriptActionFailed, model.ScriptActionCancelled, model.ScriptActionUnknown, model.ScriptActionPartial:
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

func randomScriptToken() string {
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
