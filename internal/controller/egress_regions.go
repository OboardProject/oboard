package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	agentBuildMinExternalEgress = "20260801000000"
	externalEgressTimeoutMS     = 8000
	maxExternalEgressTargets    = 256
)

func (s *Server) probeProxyPathEgress(w http.ResponseWriter, r *http.Request, pathID int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	resolveRoutingProxyPathNames(&data)
	target, ok := externalEgressTargetByPath(core.ExternalEgressProbeTargets(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds), pathID)
	if !ok {
		fail(w, errors.New("只有已启用且以第三方节点结束的代理分支可以探测出口地区"), http.StatusBadRequest)
		return
	}
	server, ok := serverByID(data.Servers, target.OwnerServerID)
	if !ok {
		fail(w, errors.New("出口探测所属服务器不存在"), http.StatusConflict)
		return
	}
	if reason := agentTaskImmediateFailure(&server); reason != "" {
		fail(w, errors.New(reason), http.StatusConflict)
		return
	}
	if !agentBuildSupportsTask(server.AgentBuild, agentBuildMinExternalEgress) {
		fail(w, fmt.Errorf("服务器 %s 的 Agent 不支持第三方节点出口探测；请先更新 Agent", server.Name), http.StatusConflict)
		return
	}
	if err := requireReadyWARPForFocusedApply(data, server.ID); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	generated, err := s.generateServerCoreConfigWithLedger(r.Context(), server, data, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if !configContainsOutboundTag(generated.Config, target.OutboundTag) {
		fail(w, errors.New("当前已生成配置不包含该分支出口；请先执行完整部署"), http.StatusConflict)
		return
	}
	unchanged, err := s.serverConfigUnchanged(r.Context(), server.ID, generated.Config)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if !unchanged {
		fail(w, errors.New("代理链路配置尚未部署或已经变更；请先执行完整部署"), http.StatusConflict)
		return
	}
	baseline, err := s.store.LastSuccessfulConfigTaskByServer(r.Context(), server.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, errors.New("服务器没有可用的已部署配置；请先执行完整部署"), http.StatusConflict)
			return
		}
		fail(w, err, http.StatusInternalServerError)
		return
	}
	plan := model.ExternalEgressProbePlan{
		Version:               baseline.ConfigVersion,
		ExpectedConfigVersion: baseline.ConfigVersion,
		TimeoutMS:             externalEgressTimeoutMS,
		Targets:               []model.ExternalEgressProbeTarget{target},
	}
	if active, found, err := s.activeExternalEgressTask(r.Context(), server.ID, target); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	} else if found {
		path := s.resolvedProxyPath(r.Context(), model.ProxyPath{ID: pathID})
		write(w, http.StatusAccepted, map[string]any{"task": sanitizeTaskForRole(*active, currentRole(r)), "proxy_path": path, "reused": true})
		return
	}
	task, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeProbeExternalEgress, plan, baseline.ConfigVersion)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.MarkProxyPathEgressPending(r.Context(), target, baseline.ConfigVersion, task.ID); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	path := s.resolvedProxyPath(r.Context(), model.ProxyPath{ID: pathID})
	auditReq(s, r, "probe", "proxy-path-egress", fmt.Sprint(pathID))
	write(w, http.StatusAccepted, map[string]any{"task": sanitizeTaskForRole(task, currentRole(r)), "proxy_path": path, "reused": false})
}

func (s *Server) activeExternalEgressTask(ctx context.Context, serverID int64, target model.ExternalEgressProbeTarget) (*model.AgentTask, bool, error) {
	tasks, err := s.store.ListTasksByServer(ctx, serverID, 100)
	if err != nil {
		return nil, false, err
	}
	for i := range tasks {
		task := &tasks[i]
		if task.Type != model.AgentTaskTypeProbeExternalEgress || (task.Status != "pending" && task.Status != "running") {
			continue
		}
		var plan model.ExternalEgressProbePlan
		if json.Unmarshal([]byte(task.PayloadJSON), &plan) != nil {
			continue
		}
		for _, existing := range plan.Targets {
			if existing.PathID == target.PathID && existing.TopologyFingerprint == target.TopologyFingerprint {
				return task, true, nil
			}
		}
	}
	return nil, false, nil
}

func externalEgressTargetByPath(targets []model.ExternalEgressProbeTarget, pathID int64) (model.ExternalEgressProbeTarget, bool) {
	for _, target := range targets {
		if target.PathID == pathID {
			return target, true
		}
	}
	return model.ExternalEgressProbeTarget{}, false
}

func serverByID(servers []model.Server, id int64) (model.Server, bool) {
	for _, server := range servers {
		if server.ID == id {
			return server, true
		}
	}
	return model.Server{}, false
}

func configContainsOutboundTag(config, tag string) bool {
	var root map[string]any
	if json.Unmarshal([]byte(config), &root) != nil {
		return false
	}
	for _, field := range []string{"outbounds", "endpoints"} {
		items, _ := root[field].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if strings.TrimSpace(fmt.Sprint(item["tag"])) == tag {
				return true
			}
		}
	}
	return false
}

func (s *Server) applyExternalEgressTaskResults(ctx context.Context, serverID int64, task model.AgentTask, taskStatus, resultJSON string) error {
	plan, result, executed, err := externalEgressTaskResult(task, resultJSON)
	if err != nil || plan == nil {
		return err
	}
	if len(plan.Targets) == 0 || len(plan.Targets) > maxExternalEgressTargets {
		return errors.New("invalid external egress probe target count")
	}
	if len(result.Items) > len(plan.Targets) || len(result.Items) > maxExternalEgressTargets {
		return errors.New("external egress probe returned too many results")
	}
	payloadTargets := make(map[string]model.ExternalEgressProbeTarget, len(plan.Targets))
	for _, target := range plan.Targets {
		if target.ProbeID == "" || target.OwnerServerID != serverID {
			return errors.New("external egress probe payload does not belong to this agent")
		}
		if _, duplicate := payloadTargets[target.ProbeID]; duplicate {
			return errors.New("external egress probe payload contains duplicate probe_id")
		}
		payloadTargets[target.ProbeID] = target
	}
	items := make(map[string]model.ExternalEgressProbeItem, len(result.Items))
	for _, item := range result.Items {
		if _, ok := payloadTargets[item.ProbeID]; !ok {
			return errors.New("external egress probe returned an unknown probe_id")
		}
		if _, duplicate := items[item.ProbeID]; duplicate {
			return errors.New("external egress probe returned duplicate probe_id")
		}
		items[item.ProbeID] = item
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	current := map[int64]model.ExternalEgressProbeTarget{}
	for _, target := range core.ExternalEgressProbeTargets(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds) {
		current[target.PathID] = target
	}
	attemptedAt := time.Now().UTC()
	for _, target := range plan.Targets {
		latest, ok := current[target.PathID]
		if !ok || latest.TopologyFingerprint != target.TopologyFingerprint || latest.OwnerServerID != serverID {
			continue
		}
		item, found := items[target.ProbeID]
		if !executed || !found {
			message := "出口探测未执行"
			if executed {
				message = "Agent 未返回该分支的出口探测结果"
			}
			if taskStatus != "succeeded" {
				message = "出口探测任务失败"
			}
			if err := s.store.SaveProxyPathEgressAttempt(ctx, target, plan.ExpectedConfigVersion, task.ID, core.RegionStatusFailed, "", "", "", message, attemptedAt); err != nil {
				return err
			}
			continue
		}
		if item.Status != "succeeded" {
			message := boundedExternalEgressError(item.Error)
			if message == "" {
				message = "未能通过该分支获取出口 IP"
			}
			if err := s.store.SaveProxyPathEgressAttempt(ctx, target, plan.ExpectedConfigVersion, task.ID, core.RegionStatusFailed, "", "", "", message, attemptedAt); err != nil {
				return err
			}
			continue
		}
		exitIP, err := parsePublicEgressIP(item.ExitIP)
		if err != nil {
			if saveErr := s.store.SaveProxyPathEgressAttempt(ctx, target, plan.ExpectedConfigVersion, task.ID, core.RegionStatusFailed, "", "", "", err.Error(), attemptedAt); saveErr != nil {
				return saveErr
			}
			continue
		}
		if s.geoIP == nil {
			if err := s.store.SaveProxyPathEgressAttempt(ctx, target, plan.ExpectedConfigVersion, task.ID, core.RegionStatusFailed, "", "", "", "Controller 的 IP 归属库不可用", attemptedAt); err != nil {
				return err
			}
			continue
		}
		geo, lookupErr := s.geoIP.Lookup(exitIP)
		code := core.NormalizeRegionCode(geo.CountryCode)
		if lookupErr != nil || code == "" || code == "AQ" {
			message := "出口 IP 地区未识别"
			if lookupErr != nil {
				message = boundedExternalEgressError(lookupErr.Error())
			}
			if err := s.store.SaveProxyPathEgressAttempt(ctx, target, plan.ExpectedConfigVersion, task.ID, core.RegionStatusFailed, "", "", "", message, attemptedAt); err != nil {
				return err
			}
			continue
		}
		revision := strings.TrimSpace(geo.Revision)
		if revision == "" {
			revision = strings.TrimSpace(s.geoIPStatus.Revision)
		}
		if err := s.store.SaveProxyPathEgressAttempt(ctx, target, plan.ExpectedConfigVersion, task.ID, "succeeded", exitIP, code, revision, "", attemptedAt); err != nil {
			return err
		}
	}
	return nil
}

func externalEgressTaskResult(task model.AgentTask, resultJSON string) (*model.ExternalEgressProbePlan, model.ExternalEgressProbeResult, bool, error) {
	switch task.Type {
	case model.AgentTaskTypeProbeExternalEgress:
		var plan model.ExternalEgressProbePlan
		if err := json.Unmarshal([]byte(task.PayloadJSON), &plan); err != nil {
			return nil, model.ExternalEgressProbeResult{}, false, err
		}
		var result model.ExternalEgressProbeResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, result, false, err
		}
		return &plan, result, true, nil
	case model.AgentTaskTypeApplyDeployment:
		var payload model.DeploymentTaskPayload
		if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
			return nil, model.ExternalEgressProbeResult{}, false, err
		}
		if payload.ExternalEgressProbe == nil {
			return nil, model.ExternalEgressProbeResult{}, false, nil
		}
		var deployment struct {
			Steps []struct {
				Key    string          `json:"key"`
				Result json.RawMessage `json:"result"`
			} `json:"steps"`
		}
		if err := json.Unmarshal([]byte(resultJSON), &deployment); err != nil {
			return nil, model.ExternalEgressProbeResult{}, false, err
		}
		for _, step := range deployment.Steps {
			if step.Key != "external_egress_probe" {
				continue
			}
			var result model.ExternalEgressProbeResult
			if len(step.Result) != 0 && string(step.Result) != "null" {
				if err := json.Unmarshal(step.Result, &result); err != nil {
					return nil, result, true, err
				}
			}
			return payload.ExternalEgressProbe, result, true, nil
		}
		return payload.ExternalEgressProbe, model.ExternalEgressProbeResult{}, false, nil
	default:
		return nil, model.ExternalEgressProbeResult{}, false, nil
	}
}

func boundedExternalEgressError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func parsePublicEgressIP(raw string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("Agent 返回的出口 IP 无效")
	}
	addr = addr.Unmap()
	if !isPublicEgressAddr(addr) {
		return "", errors.New("Agent 返回的出口 IP 不是公网地址")
	}
	return addr.String(), nil
}

func isPublicEgressAddr(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return false
	}
	for _, raw := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"2001:2::/48", "2001:10::/28", "2001:db8::/32",
	} {
		prefix := netip.MustParsePrefix(raw)
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func (s *Server) refreshProxyPathEgressGeography(ctx context.Context) error {
	if s.geoIP == nil {
		return nil
	}
	items, err := s.store.ListProxyPathEgressResults(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if strings.TrimSpace(item.LastExitIP) == "" {
			continue
		}
		geo, lookupErr := s.geoIP.Lookup(item.LastExitIP)
		code := core.NormalizeRegionCode(geo.CountryCode)
		if lookupErr != nil || code == "" || code == "AQ" {
			continue
		}
		revision := strings.TrimSpace(geo.Revision)
		if revision == "" {
			revision = strings.TrimSpace(s.geoIPStatus.Revision)
		}
		if err := s.store.UpdateProxyPathEgressGeography(ctx, item.PathID, code, revision); err != nil {
			return err
		}
	}
	return nil
}
