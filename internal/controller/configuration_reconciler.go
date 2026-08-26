package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const defaultConfigurationReconcileDelay = 150 * time.Millisecond
const certificateConfigurationRetryDelay = time.Second

type automaticConfigurationSyncContextKey struct{}

func withAutomaticConfigurationSync(ctx context.Context) context.Context {
	return context.WithValue(ctx, automaticConfigurationSyncContextKey{}, true)
}

func automaticConfigurationSync(ctx context.Context) bool {
	value, _ := ctx.Value(automaticConfigurationSyncContextKey{}).(bool)
	return value
}

// StartConfigurationReconciler runs the durable desired-state coordinator. The
// wake channel is only a latency hint; pending rows are always reread from
// SQLite so a lost wake or Controller restart cannot lose a configuration.
func (s *Server) StartConfigurationReconciler(ctx context.Context) {
	if err := s.store.RecoverConfigurationSyncStates(ctx); err != nil {
		logConfigurationError("recover", err)
	}
	// The watermark advances in the same SQLite transaction as the domain
	// mutation. Re-seeding existing servers closes the crash window between
	// that commit and the asynchronous sync-state write.
	if revision, err := s.store.ConfigurationRevision(ctx); err != nil {
		logConfigurationError("read configuration revision", err)
	} else if revision > 0 {
		if servers, listErr := s.store.ListServers(ctx); listErr != nil {
			logConfigurationError("list servers for recovery", listErr)
		} else {
			for _, server := range servers {
				relevant, relevantErr := s.store.ServerEverDeployedOrHasState(ctx, server.ID)
				if relevantErr != nil {
					logConfigurationError("check server recovery scope", relevantErr)
					continue
				}
				if relevant {
					if _, markErr := s.store.EnsureConfigurationSyncRevision(context.WithoutCancel(ctx), server.ID, revision); markErr != nil {
						logConfigurationError("repair configuration sync state", markErr)
					}
				}
			}
		}
	}
	timer := time.NewTimer(s.configurationReconcileDelay())
	recovery := time.NewTicker(time.Second)
	defer timer.Stop()
	defer recovery.Stop()
	pending := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.configurationWake:
			pending = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.configurationReconcileDelay())
		case <-timer.C:
			if pending {
				s.reconcileConfiguration(ctx)
				pending = false
			}
		case <-recovery.C:
			s.reconcileConfiguration(ctx)
		}
	}
}

func (s *Server) configurationReconcileDelay() time.Duration {
	if s.configurationDelay > 0 {
		return s.configurationDelay
	}
	return defaultConfigurationReconcileDelay
}

func (s *Server) signalConfigurationReconcile() {
	if s.configurationWake == nil {
		return
	}
	select {
	case s.configurationWake <- struct{}{}:
	default:
	}
}

func (s *Server) configurationChangesetApplied(ctx context.Context, item *model.AutomationChangeset, beforeRevision, afterRevision uint64) {
	if item == nil || afterRevision <= beforeRevision {
		return
	}
	for _, operation := range item.Operations {
		if configurationCapability(operation.Capability) {
			s.markConfigurationRevision(ctx, afterRevision, nil)
			return
		}
	}
}

func configurationCapability(name string) bool {
	switch name {
	case "servers.onboard", "servers.update", "servers.delete", "servers.dns_policy.set",
		"users.create", "users.update", "users.delete",
		"user_devices.update", "user_devices.revoke",
		"subscription_plans.create", "subscription_plans.update", "subscription_plans.delete",
		"subscription_plans.nodes.update", "user_node_exceptions.create", "user_node_exceptions.update", "user_node_exceptions.delete":
		return true
	case "inbounds.probe", "proxy_paths.probe_egress", "routing_rule_sets.refresh":
		return false
	}
	for _, prefix := range []string{"inbounds.", "outbounds.", "external_outbounds.", "routing_rules.", "routing_rule_sets.", "topology.", "proxy_paths.", "proxy_path_steps.", "port_forwards.", "tunnels.", "dns_lists.", "user_groups.", "user_group_members."} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (s *Server) markConfigurationChanged(ctx context.Context, path, method string) {
	if s.store == nil || !configurationMutationPath(path, method) {
		return
	}
	revision, err := s.store.ConfigurationRevision(ctx)
	if err != nil || revision == 0 {
		if err != nil {
			logConfigurationError("read revision", err)
		}
		return
	}
	s.markConfigurationRevision(ctx, revision, s.configurationMutationServerIDs(ctx, path, method))
}

func (s *Server) markConfigurationRevision(ctx context.Context, revision uint64, serverIDs []int64) {
	ids, err := s.store.MarkConfigurationSyncPending(context.WithoutCancel(ctx), revision, serverIDs)
	if err != nil {
		logConfigurationError("mark pending", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	s.publishRealtime("configuration", "deployments", "tasks")
	s.signalConfigurationReconcile()
}

func configurationMutationPath(path, method string) bool {
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch && method != http.MethodDelete {
		return false
	}
	path = normalizeConfigurationPath(path)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	switch parts[0] {
	case "deployments", "agent-tasks", "task-results", "dns-benchmarks", "mtu-detections", "port-forward-probes", "inbound-probes", "certificates", "dns-records", "dns-credentials", "google-eab-credentials", "notification-channels", "notification-announcements", "backups", "controller-update", "auth", "me", "access-changes", "assignable-nodes", "assignable-node-scopes":
		return false
	case "changesets":
		return false // automation.Apply invokes the canonical observer after commit.
	case "servers":
		if len(parts) <= 2 {
			return true
		}
		return len(parts) == 3 && parts[2] == "dns-policy"
	case "inbounds":
		return len(parts) <= 2 || len(parts) == 3 && parts[2] == "padding"
	case "proxy-paths":
		return len(parts) <= 2 && parts[len(parts)-1] != "reuse-preview" || len(parts) == 2 && (parts[1] == "reuse" || parts[1] == "direct-branches")
	case "port-forwards":
		return len(parts) <= 2
	case "routing-rule-sets":
		return len(parts) <= 2 // /refresh is a command that updates fetched content explicitly.
	case "subscription-plans":
		return len(parts) <= 2
	case "user-node-exceptions":
		return len(parts) <= 2
	case "routing-rules":
		return len(parts) <= 2 || len(parts) == 2 && (parts[1] == "place" || parts[1] == "reorder")
	case "external-outbounds":
		return len(parts) <= 2 || len(parts) == 2 && parts[1] == "import"
	case "dns-lists":
		return len(parts) <= 2 || len(parts) == 3 && parts[2] == "set-default"
	case "outbounds", "proxy-path-steps", "warp-profiles", "tunnels", "user-groups", "user-group-members", "users":
		return len(parts) <= 2
	default:
		return false
	}
}

func normalizeConfigurationPath(path string) string {
	for _, prefix := range []string{"/api/v1/ui/", "/api/v1/", "/api/v1/"} {
		if strings.HasPrefix(path, prefix) {
			return strings.TrimPrefix(path, prefix)
		}
	}
	return strings.TrimPrefix(path, "/")
}

func (s *Server) configurationMutationServerIDs(ctx context.Context, path, method string) []int64 {
	path = normalizeConfigurationPath(path)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return nil
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	switch parts[0] {
	case "servers":
		if method == http.MethodDelete {
			return nil
		}
		return []int64{id}
	case "inbounds":
		return s.configurationTopologyServerIDs(ctx, []int64{id}, nil)
	case "proxy-paths":
		return s.configurationTopologyServerIDs(ctx, nil, []int64{id})
	case "proxy-path-steps":
		if item, err := s.store.GetProxyPathStep(ctx, id); err == nil {
			return s.configurationTopologyServerIDs(ctx, nil, []int64{item.PathID})
		}
	case "outbounds":
		if item, err := s.store.GetOutbound(ctx, id); err == nil {
			return uniquePositiveIDs([]int64{item.ServerID, valueOrZero(item.NextServerID)})
		}
	case "port-forwards":
		if item, err := s.store.GetPortForward(ctx, id); err == nil {
			return uniquePositiveIDs([]int64{item.SourceServerID, item.TargetServerID})
		}
	case "tunnels":
		if item, err := s.store.GetTunnel(ctx, id); err == nil {
			return uniquePositiveIDs([]int64{item.SourceServerID, item.TargetServerID})
		}
	}
	// Cross-server topology, global DNS, and credential changes are
	// conservatively scoped to all servers. Trusted-forward expansion remains
	// inside deployConfiguration.
	return nil
}

func (s *Server) configurationTopologyServerIDs(ctx context.Context, inboundIDs, pathIDs []int64) []int64 {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil
	}
	inboundSet := make(map[int64]bool, len(inboundIDs))
	for _, inboundID := range inboundIDs {
		if inboundID > 0 {
			inboundSet[inboundID] = true
		}
	}
	pathSet := make(map[int64]bool, len(pathIDs))
	for _, pathID := range pathIDs {
		if pathID > 0 {
			pathSet[pathID] = true
		}
	}
	for _, path := range data.ProxyPaths {
		if inboundSet[path.InboundID] {
			pathSet[path.ID] = true
		}
	}
	inboundByID := make(map[int64]model.Inbound, len(data.Inbounds))
	for _, inbound := range data.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	serverIDs := make([]int64, 0)
	for inboundID := range inboundSet {
		if inbound, ok := inboundByID[inboundID]; ok {
			serverIDs = append(serverIDs, inbound.ServerID)
		}
	}
	for _, path := range data.ProxyPaths {
		if !pathSet[path.ID] {
			continue
		}
		if inbound, ok := inboundByID[path.InboundID]; ok {
			serverIDs = append(serverIDs, inbound.ServerID)
		}
	}
	for _, step := range data.ProxyPathSteps {
		if !pathSet[step.PathID] {
			continue
		}
		if step.ServerID != nil {
			serverIDs = append(serverIDs, *step.ServerID)
		}
		if step.InboundID != nil {
			if inbound, ok := inboundByID[*step.InboundID]; ok {
				serverIDs = append(serverIDs, inbound.ServerID)
			}
		}
	}
	return uniquePositiveIDs(serverIDs)
}

func (s *Server) configurationMutationResponseServerIDs(ctx context.Context, path string, body []byte) ([]int64, bool) {
	segment := strings.Split(strings.Trim(normalizeConfigurationPath(path), "/"), "/")[0]
	if segment != "inbounds" && segment != "proxy-paths" && segment != "proxy-path-steps" {
		return nil, false
	}
	var response map[string]any
	if len(body) == 0 || json.Unmarshal(body, &response) != nil {
		return nil, false
	}
	inboundIDs := make([]int64, 0)
	pathIDs := make([]int64, 0)
	appendEntity := func(value any, kind string) {
		item, ok := value.(map[string]any)
		if !ok {
			return
		}
		if kind == "inbound" {
			if id := int64FromAny(item["id"]); id > 0 {
				inboundIDs = append(inboundIDs, id)
			}
			return
		}
		if kind == "path" {
			if id := int64FromAny(item["id"]); id > 0 {
				pathIDs = append(pathIDs, id)
			}
			return
		}
		if id := int64FromAny(item["path_id"]); id > 0 {
			pathIDs = append(pathIDs, id)
		}
	}
	appendEntity(response["inbound"], "inbound")
	appendEntity(response["proxy_path"], "path")
	appendEntity(response["proxy_path_step"], "step")
	for key, kind := range map[string]string{"inbounds": "inbound", "proxy_paths": "path", "proxy_path_steps": "step"} {
		if items, ok := response[key].([]any); ok {
			for _, item := range items {
				appendEntity(item, kind)
			}
		}
	}
	if len(inboundIDs) == 0 && len(pathIDs) == 0 {
		return nil, false
	}
	return s.configurationTopologyServerIDs(ctx, inboundIDs, pathIDs), true
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		result, _ := typed.Int64()
		return result
	case string:
		result, _ := strconv.ParseInt(typed, 10, 64)
		return result
	default:
		return 0
	}
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Server) reconcileConfiguration(ctx context.Context) {
	states, err := s.store.ListConfigurationSyncStates(ctx, time.Now().UTC())
	if err != nil || len(states) == 0 {
		if err != nil {
			logConfigurationError("list pending", err)
		}
		return
	}
	maxRevision := uint64(0)
	for _, state := range states {
		if state.WantedRevision > maxRevision {
			maxRevision = state.WantedRevision
		}
	}
	latest := make([]store.ConfigurationSyncState, 0, len(states))
	for _, state := range states {
		if state.WantedRevision == maxRevision {
			latest = append(latest, state)
		}
	}
	selectedServerID := int64(0)
	if len(latest) == 1 {
		selectedServerID = latest[0].ServerID
	}
	claimed := []store.ConfigurationSyncState{}
	for _, state := range latest {
		ok, claimErr := s.store.ClaimConfigurationSync(ctx, state.ServerID, state.WantedRevision)
		if claimErr != nil {
			logConfigurationError("claim pending", claimErr)
			continue
		}
		if ok {
			claimed = append(claimed, state)
		}
	}
	if len(claimed) == 0 {
		return
	}
	for _, state := range claimed {
		if err := s.store.SupersedePendingTasksByServerType(ctx, state.ServerID, model.AgentTaskTypeApplyDeployment, "新的期望配置已保存，旧任务已抑制"); err != nil {
			logConfigurationError("supersede stale task", err)
		}
	}
	preparedTasks, version, deployErr := s.deployConfiguration(withAutomaticConfigurationSync(ctx), selectedServerID, true)
	if deployErr != nil {
		if s.reconcileConfigurationAroundDuplicateDirectPaths(ctx, claimed) {
			s.publishRealtime("configuration", "deployments", "tasks")
			return
		}
		for _, state := range claimed {
			_ = s.store.MarkConfigurationSyncPreparationFailure(ctx, state.ServerID, state.WantedRevision, deployErr.Error())
		}
		s.publishRealtime("configuration", "deployments", "tasks")
		return
	}
	tasksByServer := make(map[int64]model.AgentTask, len(preparedTasks))
	for _, task := range preparedTasks {
		tasksByServer[task.ServerID] = task
	}
	for _, state := range claimed {
		task, ok := tasksByServer[state.ServerID]
		if !ok {
			_ = s.store.MarkConfigurationSyncWaiting(ctx, state.ServerID, state.WantedRevision, time.Now().UTC().Add(certificateConfigurationRetryDelay), "等待证书签发完成")
			continue
		}
		if err := s.store.MarkConfigurationSyncQueued(ctx, state.ServerID, state.WantedRevision, version, task.ID, configurationTaskPayloadDigest(task)); err != nil {
			logConfigurationError("mark queued", err)
		}
	}
	s.publishRealtime("configuration", "deployments", "tasks")
}

func (s *Server) reconcileConfigurationAroundDuplicateDirectPaths(ctx context.Context, claimed []store.ConfigurationSyncState) bool {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return false
	}
	resolveRoutingProxyPathNames(&data)
	conflicts := core.DuplicateDirectProxyPathConflicts(data.ProxyPaths, data.ProxyPathSteps)
	if len(conflicts) == 0 {
		return false
	}
	ignoredPathIDs := make(map[int64]bool)
	affectedServerIDs := make(map[int64]bool)
	pathByID := make(map[int64]model.ProxyPath, len(data.ProxyPaths))
	for _, path := range data.ProxyPaths {
		pathByID[path.ID] = path
	}
	conflictMessages := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		for _, pathID := range conflict.PathIDs {
			ignoredPathIDs[pathID] = true
		}
		for _, serverID := range s.configurationTopologyServerIDs(ctx, nil, conflict.PathIDs) {
			affectedServerIDs[serverID] = true
		}
		pathNames := make([]string, 0, len(conflict.PathIDs))
		for _, pathID := range conflict.PathIDs {
			name := strings.TrimSpace(pathByID[pathID].Name)
			if name == "" {
				name = fmt.Sprintf("#%d", pathID)
			} else {
				name = fmt.Sprintf("%s (#%d)", name, pathID)
			}
			pathNames = append(pathNames, "「"+name+"」")
		}
		conflictMessages = append(conflictMessages, fmt.Sprintf("入口 %d 的直接出口分支 %s 位于同一位置；请只保留其中一条", conflict.InboundID, strings.Join(pathNames, "、")))
	}
	for _, rule := range data.RoutingRules {
		if optionalIDInSet(rule.ProxyPathID, ignoredPathIDs) || optionalIDInSet(rule.TargetProxyPathID, ignoredPathIDs) || optionalIDInSet(rule.IPv4TargetProxyPathID, ignoredPathIDs) || optionalIDInSet(rule.IPv6TargetProxyPathID, ignoredPathIDs) {
			affectedServerIDs[rule.ServerID] = true
		}
	}
	validServerIDs := make(map[int64]bool)
	claimedByServer := make(map[int64]store.ConfigurationSyncState, len(claimed))
	message := strings.Join(conflictMessages, "；")
	for _, state := range claimed {
		claimedByServer[state.ServerID] = state
		if affectedServerIDs[state.ServerID] {
			_ = s.store.MarkConfigurationSyncPreparationFailure(ctx, state.ServerID, state.WantedRevision, message)
			continue
		}
		validServerIDs[state.ServerID] = true
	}
	filteredData := routingConfigWithoutProxyPaths(data, ignoredPathIDs)
	trustedServers := core.TrustedForwardServerIDs(filteredData.ProxyPaths, filteredData.ProxyPathSteps, filteredData.Inbounds)
	trustedBlocked := false
	for serverID := range trustedServers {
		if affectedServerIDs[serverID] {
			trustedBlocked = true
			break
		}
	}
	if trustedBlocked {
		for serverID := range trustedServers {
			delete(validServerIDs, serverID)
			if state, ok := claimedByServer[serverID]; ok && !affectedServerIDs[serverID] {
				_ = s.store.MarkConfigurationSyncPreparationFailure(ctx, state.ServerID, state.WantedRevision, "关联的可信透明转发成员存在配置问题；修复该成员后会成组重试")
			}
		}
	}
	if len(validServerIDs) == 0 {
		return true
	}
	preparedTasks, version, deployErr := s.deployConfigurationScoped(withAutomaticConfigurationSync(ctx), 0, true, validServerIDs, ignoredPathIDs)
	if deployErr != nil {
		for serverID := range validServerIDs {
			if state, ok := claimedByServer[serverID]; ok {
				_ = s.store.MarkConfigurationSyncPreparationFailure(ctx, state.ServerID, state.WantedRevision, deployErr.Error())
			}
		}
		return true
	}
	tasksByServer := make(map[int64]model.AgentTask, len(preparedTasks))
	for _, task := range preparedTasks {
		tasksByServer[task.ServerID] = task
	}
	for serverID := range validServerIDs {
		state, ok := claimedByServer[serverID]
		if !ok {
			continue
		}
		task, ok := tasksByServer[serverID]
		if !ok {
			_ = s.store.MarkConfigurationSyncWaiting(ctx, state.ServerID, state.WantedRevision, time.Now().UTC().Add(certificateConfigurationRetryDelay), "等待证书签发完成")
			continue
		}
		_ = s.store.MarkConfigurationSyncQueued(ctx, state.ServerID, state.WantedRevision, version, task.ID, configurationTaskPayloadDigest(task))
	}
	return true
}

func configurationTaskPayloadDigest(task model.AgentTask) string {
	payload := append(append([]byte(task.Type), 0), []byte(task.PayloadJSON)...)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func (s *Server) recordConfigurationTaskResult(ctx context.Context, task model.AgentTask, status, resultJSON string) {
	if task.Type != model.AgentTaskTypeApplyDeployment && task.Type != model.AgentTaskTypeApplyCoreConfig {
		return
	}
	succeeded := status == "succeeded"
	message := ""
	if !succeeded {
		var result map[string]any
		if jsonErr := json.Unmarshal([]byte(resultJSON), &result); jsonErr == nil {
			if value, ok := result["error"].(string); ok {
				message = value
			}
			if message == "" {
				if value, ok := result["message"].(string); ok {
					message = value
				}
			}
		}
		if message == "" {
			message = "Agent 配置任务失败"
		}
	}
	if err := s.store.MarkConfigurationSyncResult(ctx, task.ServerID, task.ConfigVersion, succeeded, message); err != nil {
		logConfigurationError("record task result", err)
	}
	if !succeeded {
		s.signalConfigurationReconcile()
	}
	s.publishRealtime("configuration", "deployments", "tasks")
}

func (s *Server) configurationMutationResponse(ctx context.Context, body []byte, revision uint64, serverIDs []int64) []byte {
	if len(body) == 0 || revision == 0 {
		return body
	}
	var response map[string]any
	if json.Unmarshal(body, &response) != nil || response == nil {
		return body
	}
	states, err := s.store.ListAllConfigurationSyncStates(ctx)
	if err != nil {
		return body
	}
	allowed := make(map[int64]bool, len(serverIDs))
	for _, id := range serverIDs {
		allowed[id] = true
	}
	filtered := make([]store.ConfigurationSyncState, 0, len(states))
	for _, state := range states {
		if state.WantedRevision != revision || len(allowed) > 0 && !allowed[state.ServerID] {
			continue
		}
		filtered = append(filtered, state)
	}
	response["desired_revision"] = revision
	response["state_committed_at"] = time.Now().UTC()
	response["configuration_sync"] = configurationSyncViews(filtered)
	encoded, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return encoded
}

func (s *Server) configurationSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	states, err := s.store.ListAllConfigurationSyncStates(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"configuration_sync": configurationSyncViews(states)})
}

func (s *Server) configurationSyncRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		ServerIDs []int64 `json:"server_ids"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decode(w, r, &request) {
		return
	}
	count, err := s.store.RetryFailedConfigurationSync(r.Context(), request.ServerIDs)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if count == 0 {
		fail(w, errors.New("没有可重试的配置同步失败"), http.StatusConflict)
		return
	}
	auditReq(s, r, "retry", "configuration-sync", fmt.Sprintf("servers=%d", count))
	s.signalConfigurationReconcile()
	s.publishRealtime("configuration", "deployments", "tasks")
	states, _ := s.store.ListAllConfigurationSyncStates(r.Context())
	write(w, http.StatusAccepted, map[string]any{"retried": count, "configuration_sync": configurationSyncViews(states)})
}

func configurationSyncViews(states []store.ConfigurationSyncState) []map[string]any {
	out := make([]map[string]any, 0, len(states))
	for _, state := range states {
		item := map[string]any{
			"server_id": state.ServerID, "desired_revision": state.WantedRevision,
			"state": state.State, "config_version": state.LastConfigVersion,
			"task_id": state.LastTaskID, "retry_count": state.RetryCount,
			"changed_at": state.ChangedAt, "updated_at": state.UpdatedAt,
		}
		if state.NextRetryAt != nil {
			item["next_retry_at"] = state.NextRetryAt
		}
		if state.LastError != "" {
			item["error"] = state.LastError
		}
		out = append(out, item)
	}
	return out
}

func logConfigurationError(operation string, err error) {
	if err != nil {
		log.Printf("configuration reconciler %s: %v", operation, err)
	}
}
