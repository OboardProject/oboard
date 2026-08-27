package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// registerOpsAutomationOperations wires the task-center and host-operation
// capability operations of the MCP automation layer: diagnostics, agent
// updates, log collection/management, deployment failure dismissal, and
// inbound / egress probes.

func (s *Server) registerOpsAutomationOperations() {
	s.registerTaskTriggerOperations()
	s.registerLogOperations()
	s.registerProbeOperations()
}

// ---- task triggers (diagnose / update agent / agents update all) ----

type opsTaskInput struct {
	ServerID   int64  `json:"server_id"`
	Source     string `json:"source"`
	GitHubRepo string `json:"github_repo"`
}

func (s *Server) registerTaskTriggerOperations() {
	s.automation.RegisterValidator("servers.diagnose", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinDiagnosticsTask) {
			return nil, errors.New("服务器 Agent 版本过旧，请先更新 Agent 再执行诊断")
		}
		return map[string]any{"server_id": server.ID}, nil
	})
	s.automation.RegisterRevisionResolver("servers.diagnose", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.diagnose", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		inbounds, err := s.store.ListInbounds(ctx)
		if err != nil {
			return nil, err
		}
		alwaysDomain := s.subscriptionAlwaysUseDomainHost(ctx)
		targets := []model.DiagnosticTarget{}
		for _, inbound := range inbounds {
			if inbound.ServerID != server.ID || !inbound.Enabled {
				continue
			}
			host := core.ResolveEntryAddressHost(inbound, *server, alwaysDomain)
			if strings.TrimSpace(host) == "" {
				continue
			}
			ports, err := core.MieruInboundPorts(inbound)
			if err != nil {
				continue
			}
			for _, port := range ports {
				targets = append(targets, model.DiagnosticTarget{Name: inbound.Name, Protocol: inbound.Protocol, Host: host, Port: port})
			}
		}
		configVersion := time.Now().Unix()
		task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeDiagnoseNetwork, model.DiagnoseNetworkTaskPayload{Version: configVersion, ServerID: server.ID, EntryTargets: targets}, configVersion)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": task.ID, "task_status": task.Status, "entry_target_count": len(targets)}, nil
	})

	s.automation.RegisterValidator("servers.list_network_interfaces", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if reason := agentTaskImmediateFailure(server); reason != "" {
			return nil, errors.New(reason)
		}
		if strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinNetworkInterfaces) {
			return nil, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再读取网卡")
		}
		return map[string]any{"server_id": server.ID}, nil
	})
	s.automation.RegisterRevisionResolver("servers.list_network_interfaces", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.list_network_interfaces", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if reason := agentTaskImmediateFailure(server); reason != "" {
			return nil, errors.New(reason)
		}
		if strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinNetworkInterfaces) {
			return nil, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再读取网卡")
		}
		task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeListNetworkInterfaces, map[string]any{}, time.Now().Unix())
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": task.ID, "task_status": task.Status}, nil
	})

	s.automation.RegisterValidator("servers.update_agent", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		if _, err := s.serverTaskBoundary(ctx, principal, input); err != nil {
			return nil, err
		}
		return map[string]any{"server_id": taskServerID(input)}, nil
	})
	s.automation.RegisterRevisionResolver("servers.update_agent", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.update_agent", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if _, err := s.publicBaseURL(ctx); err != nil {
			return nil, err
		}
		var request opsTaskInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		task, existing, err := s.enqueueAgentUpdate(ctx, server, model.AgentUpdateRequest{Source: request.Source, GitHubRepo: request.GitHubRepo})
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": task.ID, "task_status": task.Status, "existing": existing}, nil
	})

	s.automation.RegisterValidator("servers.uninstall_agent", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(server.AgentID) == "" {
			return nil, errors.New("agent is not enrolled")
		}
		if server.Status == model.ServerOffline {
			return nil, errors.New("服务器离线，无法远程卸载")
		}
		if !agentUninstallSupported(server) {
			return nil, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再远程卸载")
		}
		return map[string]any{"server_id": server.ID}, nil
	})
	s.automation.RegisterRevisionResolver("servers.uninstall_agent", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.uninstall_agent", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		task, existing, err := s.enqueueAgentUninstall(ctx, server, principal.UserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": task.ID, "task_status": task.Status, "existing": existing}, nil
	})

	s.automation.RegisterValidator("agents.update_all", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	s.automation.RegisterRevisionResolver("agents.update_all", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("agents.update_all", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		if _, err := s.publicBaseURL(ctx); err != nil {
			return nil, err
		}
		servers, err := s.store.ListServers(ctx)
		if err != nil {
			return nil, err
		}
		versionStamp := time.Now().Unix()
		summary := map[string]int{"total": 0, "created": 0, "existing": 0, "skipped": 0, "failed": 0}
		for _, server := range servers {
			if strings.TrimSpace(server.AgentID) == "" {
				summary["skipped"]++
				summary["total"]++
				continue
			}
			summary["total"]++
			task, existing, err := s.enqueueAgentUpdateWithVersion(ctx, &server, model.AgentUpdateRequest{}, versionStamp)
			if err != nil {
				summary["failed"]++
				continue
			}
			_ = task
			if existing {
				summary["existing"]++
			} else if task.Status == "failed" {
				summary["failed"]++
			} else {
				summary["created"]++
			}
		}
		return map[string]any{"summary": summary, "created_count": summary["created"]}, nil
	})

	s.automation.RegisterValidator("configuration_sync.retry", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		serverIDs, err := configurationSyncRetryServerIDs(input)
		if err != nil {
			return nil, err
		}
		for _, serverID := range serverIDs {
			if !principal.AllowsInt64("server_ids", serverID) {
				return nil, errors.New("configuration sync server is outside the authorized boundary")
			}
		}
		return map[string]any{"server_ids": serverIDs}, nil
	})
	s.automation.RegisterRevisionResolver("configuration_sync.retry", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		serverIDs, err := configurationSyncRetryServerIDs(input)
		if err != nil {
			return nil, err
		}
		revisions := map[string]string{}
		for _, serverID := range serverIDs {
			if !principal.AllowsInt64("server_ids", serverID) {
				return nil, errors.New("configuration sync server is outside the authorized boundary")
			}
			state, err := s.store.ConfigurationSyncState(ctx, serverID)
			if err != nil || state.State != "failed" {
				return nil, errors.New("configuration sync is not failed")
			}
			revisions["configuration_sync:"+strconv.FormatInt(serverID, 10)] = state.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		return revisions, nil
	})
	s.automation.Register("configuration_sync.retry", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		serverIDs, err := configurationSyncRetryServerIDs(input)
		if err != nil {
			return nil, err
		}
		for _, serverID := range serverIDs {
			if !principal.AllowsInt64("server_ids", serverID) {
				return nil, errors.New("configuration sync server is outside the authorized boundary")
			}
		}
		count, err := s.store.RetryFailedConfigurationSync(ctx, serverIDs)
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, errors.New("no failed configuration sync is available to retry")
		}
		s.signalConfigurationReconcile()
		s.publishRealtime("configuration", "deployments", "tasks")
		return map[string]any{"retried": count, "server_ids": serverIDs}, nil
	})

	s.automation.RegisterValidator("deployments.dismiss_failure", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	s.automation.RegisterRevisionResolver("deployments.dismiss_failure", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("deployments.dismiss_failure", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		s.expireTimedOutTasks(ctx)
		latest, err := s.store.LatestDeploymentTasks(ctx)
		if err != nil {
			return nil, err
		}
		if len(latest) == 0 {
			return nil, errors.New("deployment has no failure")
		}
		version := latest[0].ConfigVersion
		summary := taskSummary(latest)
		if summary["pending"] > 0 || summary["running"] > 0 || summary["failed"] == 0 {
			status, err := s.deploymentStatus(ctx, latest)
			if err != nil {
				return nil, err
			}
			return map[string]any{"dismissed": false, "deployment_status": status}, nil
		}
		if principal.UserID == nil {
			return nil, errors.New("authentication required")
		}
		if err := s.store.DismissDeploymentFailure(ctx, version, *principal.UserID); err != nil {
			return nil, err
		}
		status, err := s.deploymentStatus(ctx, latest)
		if err != nil {
			return nil, err
		}
		return map[string]any{"dismissed": true, "deployment_status": status}, nil
	})
}

func configurationSyncRetryServerIDs(input json.RawMessage) ([]int64, error) {
	var request struct {
		ServerIDs []int64 `json:"server_ids"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	request.ServerIDs = uniquePositiveIDs(request.ServerIDs)
	if len(request.ServerIDs) == 0 || len(request.ServerIDs) > 100 {
		return nil, errors.New("server_ids must contain between 1 and 100 servers")
	}
	return request.ServerIDs, nil
}

func (s *Server) serverTaskBoundary(ctx context.Context, principal application.Principal, input json.RawMessage) (*model.Server, error) {
	serverID := taskServerID(input)
	if serverID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	if !principal.AllowsInt64("server_ids", serverID) {
		return nil, errors.New("server is outside the authorized server boundary")
	}
	return s.store.GetServer(ctx, serverID)
}

func taskServerID(input json.RawMessage) int64 {
	var request struct {
		ServerID int64 `json:"server_id"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return 0
	}
	return request.ServerID
}

// ---- log operations ----

type logsTaskInput struct {
	ServerID int64  `json:"server_id"`
	Services string `json:"services"`
	Lines    int    `json:"lines"`
	Action   string `json:"action"`
}

func (s *Server) registerLogOperations() {
	for _, name := range []string{"servers.collect_logs", "servers.manage_logs"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			server, err := s.serverTaskBoundary(ctx, principal, input)
			if err != nil {
				return nil, err
			}
			var request logsTaskInput
			if err := strictAutomationInput(input, &request); err != nil {
				return nil, err
			}
			if name == "servers.collect_logs" {
				if strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinDiagnosticsTask) {
					return nil, errors.New("服务器 Agent 版本过旧，请先更新 Agent 再拉取日志")
				}
				if request.Services != "" && request.Services != "all" && request.Services != "agent" && request.Services != "core" {
					return nil, errors.New("services must be one of all, agent, core")
				}
				if request.Lines < 0 || request.Lines > 2000 {
					return nil, errors.New("lines must be between 1 and 2000")
				}
			} else {
				if request.Action != "rotate" && request.Action != "clear" {
					return nil, errors.New("action must be rotate or clear")
				}
				if request.Services != "" && request.Services != "all" && request.Services != "agent" && request.Services != "core" {
					return nil, errors.New("services must be one of all, agent, core")
				}
				if strings.TrimSpace(server.AgentID) == "" {
					return nil, errors.New("agent is not enrolled")
				}
			}
			return map[string]any{"server_id": server.ID}, nil
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			server, err := s.serverTaskBoundary(ctx, principal, input)
			if err != nil {
				return nil, err
			}
			return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			server, err := s.serverTaskBoundary(ctx, principal, input)
			if err != nil {
				return nil, err
			}
			var request logsTaskInput
			if err := strictAutomationInput(input, &request); err != nil {
				return nil, err
			}
			var task model.AgentTask
			if name == "servers.collect_logs" {
				payload := model.CollectLogsTaskPayload{Services: request.Services, Lines: request.Lines}
				if payload.Lines <= 0 {
					payload.Lines = 120
				}
				if payload.Lines > 2000 {
					payload.Lines = 2000
				}
				if payload.Services == "" {
					payload.Services = "all"
				}
				task, err = s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeCollectLogs, payload, time.Now().Unix())
			} else {
				payload := model.ManageLogsTaskPayload{Action: request.Action, Services: request.Services}
				if payload.Services == "" {
					payload.Services = "all"
				}
				task, err = s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeManageLogs, payload, time.Now().Unix())
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"task_id": task.ID, "task_status": task.Status}, nil
		})
	}
}

// ---- probe operations ----

func (s *Server) registerProbeOperations() {
	s.automation.RegisterValidator("servers.probe_latency", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if !server.LatencyProbeEnabled {
			return nil, errors.New("服务器未启用延迟测试")
		}
		if reason := agentTaskImmediateFailure(server); reason != "" {
			return nil, errors.New(reason)
		}
		if latencyProbeAgentUpgradeRequired(*server) {
			return nil, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再执行延迟测试")
		}
		return map[string]any{"server_id": server.ID}, nil
	})
	s.automation.RegisterRevisionResolver("servers.probe_latency", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		return s.serverTaskAutomationRevision(ctx, principal, input)
	})
	s.automation.Register("servers.probe_latency", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		server, err := s.serverTaskBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if !server.LatencyProbeEnabled {
			return nil, errors.New("服务器未启用延迟测试")
		}
		if reason := agentTaskImmediateFailure(server); reason != "" {
			return nil, errors.New(reason)
		}
		if latencyProbeAgentUpgradeRequired(*server) {
			return nil, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再执行延迟测试")
		}
		resource, err := loadLatencyProbeResource(ctx, false)
		if err != nil {
			return nil, err
		}
		targets := latencyProbeTargets(resource, *server)
		if len(targets) == 0 {
			return nil, errors.New("没有匹配的省份或运营商探针")
		}
		task, existing, err := s.queueLatencyProbeTask(ctx, server, resource, targets)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": task.ID, "task_status": task.Status, "target_count": latencyProbeTaskTargetCount(task, len(targets)), "existing": existing}, nil
	})

	s.automation.RegisterValidator("inbounds.probe", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		inbound, err := s.inboundProbeBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"inbound_id": inbound.ID}, nil
	})
	s.automation.RegisterRevisionResolver("inbounds.probe", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		inbound, err := s.inboundProbeBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"inbound:" + strconv.FormatInt(inbound.ID, 10): inbound.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("inbounds.probe", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		inbound, err := s.inboundProbeBoundary(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		server, err := s.store.GetServer(ctx, inbound.ServerID)
		if err != nil {
			return nil, err
		}
		version := time.Now().Unix()
		plan := buildInboundProbePlan(version, *server, []model.Inbound{*inbound})
		if len(plan.EntryTargets) == 0 {
			return nil, errors.New("inbound has no probeable endpoint")
		}
		localTask, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeProbeInbounds, plan, version)
		if err != nil {
			return nil, err
		}
		externalTask, err := s.createControllerInboundProbeTask(ctx, 0, 0, plan)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_ids": []int64{localTask.ID, externalTask.ID}, "entry_target_count": len(plan.EntryTargets)}, nil
	})

	s.automation.RegisterValidator("proxy_paths.probe_egress", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		pathID := proxyPathTaskID(input)
		if pathID <= 0 {
			return nil, errors.New("path_id must be a positive integer")
		}
		if !principal.AllowsInt64("proxy_path_ids", pathID) {
			return nil, errors.New("proxy path is outside the authorized resource boundary")
		}
		if _, err := s.store.GetProxyPath(ctx, pathID); err != nil {
			return nil, err
		}
		return map[string]any{"path_id": pathID}, nil
	})
	s.automation.RegisterRevisionResolver("proxy_paths.probe_egress", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		pathID := proxyPathTaskID(input)
		if pathID <= 0 {
			return nil, errors.New("path_id must be a positive integer")
		}
		path, err := s.store.GetProxyPath(ctx, pathID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"proxy_path:" + strconv.FormatInt(path.ID, 10): path.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("proxy_paths.probe_egress", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		pathID := proxyPathTaskID(input)
		if pathID <= 0 {
			return nil, errors.New("path_id must be a positive integer")
		}
		data, err := s.store.FullRoutingConfigData(ctx)
		if err != nil {
			return nil, err
		}
		resolveRoutingProxyPathNames(&data)
		target, ok := externalEgressTargetByPath(core.ExternalEgressProbeTargets(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds), pathID)
		if !ok {
			return nil, errors.New("只有已启用且以第三方节点结束的代理分支可以探测出口地区")
		}
		server, ok := serverByID(data.Servers, target.OwnerServerID)
		if !ok {
			return nil, errors.New("出口探测所属服务器不存在")
		}
		if reason := agentTaskImmediateFailure(&server); reason != "" {
			return nil, errors.New(reason)
		}
		if !agentBuildSupportsTask(server.AgentBuild, agentBuildMinExternalEgress) {
			return nil, errors.New("服务器 Agent 不支持第三方节点出口探测；请先更新 Agent")
		}
		if err := requireReadyWARPForFocusedApply(data, server.ID); err != nil {
			return nil, err
		}
		generated, err := s.generateServerCoreConfigWithLedger(ctx, server, data, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
		if err != nil {
			return nil, err
		}
		if !configContainsOutboundTag(generated.Config, target.OutboundTag) {
			return nil, errors.New("当前已生成配置不包含该分支出口；请先执行完整部署")
		}
		unchanged, err := s.serverConfigUnchanged(ctx, server.ID, generated.Config)
		if err != nil {
			return nil, err
		}
		if !unchanged {
			return nil, errors.New("代理拓扑配置尚未部署或已经变更；请先执行完整部署")
		}
		baseline, err := s.store.LastSuccessfulConfigTaskByServer(ctx, server.ID)
		if err != nil {
			return nil, errors.New("服务器没有可用的已部署配置；请先执行完整部署")
		}
		plan := model.ExternalEgressProbePlan{
			Version:               baseline.ConfigVersion,
			ExpectedConfigVersion: baseline.ConfigVersion,
			TimeoutMS:             externalEgressTimeoutMS,
			Targets:               []model.ExternalEgressProbeTarget{target},
		}
		task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeProbeExternalEgress, plan, plan.Version)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": task.ID, "path_id": pathID, "status": task.Status}, nil
	})
}

func (s *Server) inboundProbeBoundary(ctx context.Context, principal application.Principal, input json.RawMessage) (*model.Inbound, error) {
	var request struct {
		InboundID int64 `json:"inbound_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil || request.InboundID <= 0 {
		return nil, errors.New("inbound_id must be a positive integer")
	}
	inbound, err := s.store.GetInbound(ctx, request.InboundID)
	if err != nil {
		return nil, err
	}
	if !principal.AllowsInt64("server_ids", inbound.ServerID) {
		return nil, errors.New("inbound is outside the authorized server boundary")
	}
	return inbound, nil
}

func proxyPathTaskID(input json.RawMessage) int64 {
	var request struct {
		PathID int64 `json:"path_id"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return 0
	}
	return request.PathID
}
