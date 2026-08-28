package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/version"
)

type controllerUpdateAutomationInput struct {
	Channel string `json:"channel,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

func (s *Server) queryManagementCapability(ctx context.Context, principal application.Principal, capabilityName string, input json.RawMessage) (any, error) {
	switch capabilityName {
	case "traffic.get_user_ledger", "traffic.get_server_sync_state", "traffic.list_reconciliation_issues":
		return s.queryTrafficLedgerCapability(ctx, principal, capabilityName, input)
	case "servers.metrics.read":
		var request struct {
			ServerID    int64 `json:"server_id"`
			WindowHours int64 `json:"window_hours"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return s.serverMetricsRead(ctx, principal, request.ServerID, request.WindowHours)
	case "servers.latency_probes.read":
		var request struct {
			ServerID int64 `json:"server_id"`
			Limit    int64 `json:"limit"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return s.serverLatencyProbesRead(ctx, principal, request.ServerID, request.Limit)
	case "audit.logs.list":
		var request struct {
			Limit  int    `json:"limit"`
			Offset int    `json:"offset"`
			Action string `json:"action"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return s.mcpAuditLogs(ctx, principal, request.Limit, request.Offset, strings.TrimSpace(request.Action))
	case "user_devices.list_all":
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := s.store.ListAllUserDevices(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"devices": items, "count": len(items)}, nil
	case "node_library.list", "node_groups.list", "node_sources.list", "subscription_outputs.list", "subscription_outputs.preview":
		return s.queryNodeWorkspaceCapability(ctx, principal, capabilityName, input)
	case "node_incidents.list":
		var request struct {
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := s.store.ListNodeIncidents(ctx, strings.TrimSpace(request.Status), request.Limit, 0)
		if err != nil {
			return nil, err
		}
		filtered := items[:0]
		for _, item := range items {
			if principal.AllowsInt64("server_ids", item.ServerID) {
				filtered = append(filtered, item)
			}
		}
		return map[string]any{"events": filtered}, nil
	case "node_incidents.get":
		var request struct {
			ID int64 `json:"id"`
		}
		if err := strictAutomationInput(input, &request); err != nil || request.ID <= 0 {
			return nil, errors.New("valid node incident id is required")
		}
		item, err := s.store.GetNodeIncident(ctx, request.ID)
		if err != nil || !principal.AllowsInt64("server_ids", item.ServerID) {
			return nil, errors.New("authorized node incident not found")
		}
		isolations, err := s.store.ListNodePublicationIsolations(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		actions, err := s.store.ListNodeIncidentActions(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"event": item, "isolations": isolations, "actions": actions}, nil
	case "notification_broadcasts.preview":
		var request struct {
			Filter notificationBroadcastFilter `json:"filter"`
		}
		if err := strictAutomationInput(input, &request); err != nil || principal.UserID == nil || !model.HasManagementAccess(principal.Role) {
			return nil, errors.New("administrator broadcast filter is required")
		}
		preview, err := s.resolveNotificationBroadcastRecipients(ctx, *principal.UserID, request.Filter)
		if err != nil {
			return nil, err
		}
		return map[string]any{"recipient_count": preview.RecipientCount, "bound_target_count": preview.BoundTargetCount, "unbound_count": preview.UnboundCount}, nil
	case "telegram_bot.get":
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return map[string]any{"telegram_bot": s.telegramBotPublicStatus(ctx)}, nil
	case "api_principals.list":
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := s.store.ListAPIPrincipals(ctx)
		if err != nil {
			return nil, err
		}
		views := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if item.Type == model.APIPrincipalServiceAccount {
				views = append(views, automationAPIPrincipalView(item))
			}
		}
		return views, nil
	case "api_tokens.list":
		var request struct {
			PrincipalID string `json:"principal_id"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		request.PrincipalID = strings.TrimSpace(request.PrincipalID)
		item, err := s.store.GetAPIPrincipal(ctx, request.PrincipalID)
		if err != nil || item.Type != model.APIPrincipalServiceAccount {
			return nil, errors.New("service account not found")
		}
		tokens, err := s.store.ListAPITokens(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		views := make([]map[string]any, 0, len(tokens))
		for _, token := range tokens {
			views = append(views, automationAPITokenView(token))
		}
		return views, nil
	case "controller_update.status":
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		status, err := s.controllerUpdater.Status(ctx)
		if err != nil {
			status = s.fallbackControllerUpdateStatus()
		}
		return s.controllerUpdateAutomationView(ctx, status), nil
	case "agent_updates.status":
		var request struct{}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return s.agentFleetStatus(ctx)
	default:
		result, err := s.application.Query(ctx, principal, capabilityName, input)
		if err == nil {
			return result, nil
		}
		if strings.Contains(err.Error(), "unsupported query capability") {
			if fallback, fallbackErr := s.queryMCPCapabilityFallback(ctx, principal, capabilityName, input); fallbackErr == nil {
				return fallback, nil
			}
		}
		return nil, err
	}
}

func (s *Server) registerControllerUpdateOperations() {
	for _, capabilityName := range []string{"controller_update.check", "controller_update.set_channel", "controller_update.install", "controller_update.cancel"} {
		name := capabilityName
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			request, status, err := s.controllerUpdateAutomationCandidate(ctx, name, input)
			if err != nil {
				return nil, err
			}
			return map[string]any{"controller_update": s.controllerUpdateAutomationView(ctx, status), "channel": request.Channel}, nil
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			_, status, err := s.controllerUpdateAutomationCandidate(ctx, name, input)
			if err != nil {
				return nil, err
			}
			return map[string]string{"controller_update:singleton": controllerUpdateAutomationRevision(status)}, nil
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			request, _, err := s.controllerUpdateAutomationCandidate(ctx, name, input)
			if err != nil {
				return nil, err
			}
			var status controllerupdate.Status
			accepted := true
			switch name {
			case "controller_update.check":
				status, err = s.controllerUpdater.Check(ctx)
				if err != nil {
					err = controllerUpdateOperationError("检查主控更新失败", status, err)
				}
			case "controller_update.set_channel":
				status, err = s.controllerUpdater.SetChannel(ctx, request.Channel)
				if err != nil {
					err = controllerUpdateOperationError("切换更新通道失败", status, err)
				}
			case "controller_update.install":
				status, accepted, err = s.beginManualControllerUpdate(ctx)
			case "controller_update.cancel":
				status, err = s.controllerUpdater.Cancel(ctx)
				if err != nil {
					if strings.TrimSpace(status.LastError) != "" {
						err = errors.New(status.LastError)
					} else {
						err = errors.New("当前没有可以中断的更新")
					}
				}
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"controller_update": s.controllerUpdateAutomationView(ctx, status), "accepted": accepted}, nil
		})
	}
}

func (s *Server) controllerUpdateAutomationCandidate(ctx context.Context, capabilityName string, input json.RawMessage) (controllerUpdateAutomationInput, controllerupdate.Status, error) {
	var request controllerUpdateAutomationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return request, controllerupdate.Status{}, err
	}
	request.Channel = strings.ToLower(strings.TrimSpace(request.Channel))
	switch capabilityName {
	case "controller_update.check":
		if request.Channel != "" || request.Confirm {
			return request, controllerupdate.Status{}, errors.New("controller update check does not accept parameters")
		}
	case "controller_update.set_channel":
		if request.Channel != "stable" && request.Channel != "dev" {
			return request, controllerupdate.Status{}, errors.New("channel must be stable or dev")
		}
	case "controller_update.install", "controller_update.cancel":
		if !request.Confirm || request.Channel != "" {
			return request, controllerupdate.Status{}, errors.New("confirm=true is required")
		}
	default:
		return request, controllerupdate.Status{}, errors.New("unsupported controller update operation")
	}
	status, err := s.controllerUpdater.Status(ctx)
	if err != nil {
		return request, status, controllerUpdateOperationError("读取主控更新状态失败", status, err)
	}
	if capabilityName == "controller_update.install" {
		if status.Channel == "pinned" {
			return request, status, errors.New("固定版本不能在线更新，请先切换更新通道")
		}
		if isActiveControllerUpdateStatus(status.State) {
			return request, status, errors.New("主控更新已经在进行中")
		}
	}
	if capabilityName == "controller_update.cancel" {
		run, _ := s.store.GetActiveControllerUpdateRun(ctx)
		if (run == nil || !controllerUpdatePhaseCancellable(run.Phase)) && !status.CanCancel {
			return request, status, errors.New("当前没有可以中断的更新")
		}
	}
	return request, status, nil
}

func (s *Server) controllerUpdateAutomationView(ctx context.Context, status controllerupdate.Status) map[string]any {
	status.Current = controllerupdate.BuildInfo{Version: version.Version, Build: version.Build, Commit: version.Commit, Date: version.Date}
	backupConfigured := strings.TrimSpace(status.BackupPath) != ""
	if settings, err := s.store.ListSettings(ctx); err == nil {
		status.AutoUpdateEnabled = settingBool(settings, controllerAutoUpdateSetting, false)
		status.AutoUpdateIntervalHours = controllerUpdateIntervalHours(settings)
		backupConfigured = backupConfigured || strings.TrimSpace(settings[controllerBackupSetting]) != ""
		if status.LastError == "" {
			status.LastError = settings[controllerUpdateErrorSetting]
		}
	}
	s.attachControllerUpdateOperation(ctx, &status)
	buildView := func(item controllerupdate.BuildInfo) map[string]any {
		return map[string]any{"version": item.Version, "build": item.Build, "commit": item.Commit, "date": item.Date}
	}
	view := map[string]any{
		"channel": status.Channel, "current": buildView(status.Current), "available": buildView(status.Available),
		"update_available": status.UpdateAvailable, "auto_update_enabled": status.AutoUpdateEnabled,
		"auto_update_interval_hours": status.AutoUpdateIntervalHours, "can_cancel": status.CanCancel,
		"status": status.State, "last_checked_at": status.LastCheckedAt, "last_error": status.LastError,
		"backup_configured": backupConfigured,
	}
	if status.Operation != nil {
		view["operation"] = status.Operation
	}
	return view
}

func controllerUpdateAutomationRevision(status controllerupdate.Status) string {
	return fmt.Sprintf("%s:%s:%s:%t:%t", status.Channel, status.State, status.Available.Build, status.UpdateAvailable, status.CanCancel)
}
