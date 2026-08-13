package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type PlanResult struct {
	Kind               string           `json:"kind"`
	Valid              bool             `json:"valid"`
	Warnings           []string         `json:"warnings"`
	Candidates         []map[string]any `json:"candidates"`
	SuggestedChangeset map[string]any   `json:"suggested_changeset,omitempty"`
}

func (s *Service) PlanServerOnboarding(ctx context.Context, principal Principal, raw json.RawMessage) (PlanResult, error) {
	var input struct {
		Name                        string                     `json:"name"`
		RegionCode                  string                     `json:"region_code"`
		IPStack                     string                     `json:"ip_stack"`
		LatencyProbeEnabled         *bool                      `json:"latency_probe_enabled"`
		LatencyProbeMode            model.LatencyProbeMode     `json:"latency_probe_mode"`
		LatencyProbePublicTarget    model.ConnectivityTarget   `json:"latency_probe_public_target"`
		LatencyProbeIntervalSeconds int                        `json:"latency_probe_interval_seconds"`
		LatencyProbeSampleCount     int                        `json:"latency_probe_sample_count"`
		LatencyProbeRegions         []model.LatencyProbeRegion `json:"latency_probe_regions"`
		LatencyProbeMaxTargets      int                        `json:"latency_probe_max_targets"`
	}
	if err := strictUnmarshal(raw, &input); err != nil {
		return PlanResult{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return PlanResult{}, errors.New("server name is required")
	}
	servers, err := s.ListServers(ctx, principal)
	if err != nil {
		return PlanResult{}, err
	}
	warnings := []string{}
	for _, server := range servers {
		if strings.EqualFold(server.Name, input.Name) {
			warnings = append(warnings, "服务器名称已存在")
		}
	}
	if input.IPStack == "" {
		input.IPStack = string(model.IPStackAuto)
	}
	latencyEnabled := true
	if input.LatencyProbeEnabled != nil {
		latencyEnabled = *input.LatencyProbeEnabled
	}
	if input.LatencyProbeMode == "" {
		input.LatencyProbeMode = model.LatencyProbeModeTCP
	}
	if input.LatencyProbePublicTarget == "" {
		input.LatencyProbePublicTarget = model.ConnectivityProbeTargetAuto
	}
	if input.LatencyProbeIntervalSeconds == 0 {
		input.LatencyProbeIntervalSeconds = 60
	}
	if input.LatencyProbeSampleCount == 0 {
		input.LatencyProbeSampleCount = 3
	}
	if input.LatencyProbeMaxTargets == 0 {
		input.LatencyProbeMaxTargets = 64
	}
	server := map[string]any{
		"name": input.Name, "region_code": strings.ToUpper(strings.TrimSpace(input.RegionCode)), "ip_stack": input.IPStack,
		"latency_probe_enabled": latencyEnabled, "latency_probe_mode": input.LatencyProbeMode, "latency_probe_public_target": input.LatencyProbePublicTarget,
		"latency_probe_interval_seconds": input.LatencyProbeIntervalSeconds, "latency_probe_sample_count": input.LatencyProbeSampleCount,
		"latency_probe_regions": input.LatencyProbeRegions, "latency_probe_max_targets": input.LatencyProbeMaxTargets,
		"listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 60000,
	}
	suggested := map[string]any{
		"base_revisions": map[string]string{},
		"operation":      map[string]any{"capability": "servers.onboard", "input": map[string]any{"server": server, "issue_enrollment_token": true}},
	}
	return PlanResult{Kind: "server_onboarding", Valid: len(warnings) == 0, Warnings: warnings, Candidates: []map[string]any{{"server": server, "requires_external_install": true, "suggested_changeset": suggested}}, SuggestedChangeset: suggested}, nil
}

func (s *Service) PlanProxyPath(ctx context.Context, principal Principal, raw json.RawMessage) (PlanResult, error) {
	var input struct {
		EntryServerID         int64    `json:"entry_server_id"`
		ExitRegion            string   `json:"exit_region"`
		PreferredRelayRegions []string `json:"preferred_relay_regions"`
		MaxHops               int      `json:"max_hops"`
		AvoidServerIDs        []int64  `json:"avoid_server_ids"`
		Objective             string   `json:"objective"`
	}
	if err := strictUnmarshal(raw, &input); err != nil {
		return PlanResult{}, err
	}
	if input.EntryServerID <= 0 || !principal.AllowsInt64("server_ids", input.EntryServerID) {
		return PlanResult{}, errors.New("authorized entry_server_id is required")
	}
	if input.MaxHops == 0 {
		input.MaxHops = 3
	}
	if input.MaxHops < 1 || input.MaxHops > 5 {
		return PlanResult{}, errors.New("max_hops must be between 1 and 5")
	}
	servers, err := s.ListServers(ctx, principal)
	if err != nil {
		return PlanResult{}, err
	}
	entryFound := false
	exitRegion := strings.ToUpper(strings.TrimSpace(input.ExitRegion))
	eligible := []ServerDTO{}
	for _, server := range servers {
		if server.ID == input.EntryServerID {
			entryFound = true
		}
		if server.Status != model.ServerOnline || slices.Contains(input.AvoidServerIDs, server.ID) || server.ID == input.EntryServerID || strings.TrimSpace(server.EntryAddress) == "" && strings.TrimSpace(server.PublicIPv4) == "" && strings.TrimSpace(server.PublicIPv6) == "" {
			continue
		}
		region := server.RegionCode
		if region == "" {
			region = server.DetectedRegionCode
		}
		if exitRegion == "" || strings.EqualFold(region, exitRegion) {
			eligible = append(eligible, server)
		}
	}
	if !entryFound {
		return PlanResult{}, sql.ErrNoRows
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return PlanResult{}, err
	}
	entryInbounds := make([]model.Inbound, 0)
	for _, inbound := range inbounds {
		if inbound.ServerID == input.EntryServerID && inbound.Enabled && inbound.Protocol != model.ProtocolSSH {
			entryInbounds = append(entryInbounds, inbound)
		}
	}
	sort.Slice(entryInbounds, func(i, j int) bool { return entryInbounds[i].ID < entryInbounds[j].ID })
	sort.SliceStable(eligible, func(i, j int) bool {
		return relayPreference(eligible[i], input.PreferredRelayRegions) < relayPreference(eligible[j], input.PreferredRelayRegions)
	})
	candidates := []map[string]any{}
	for _, inbound := range entryInbounds {
		for _, exit := range eligible {
			baseRevisions := map[string]string{
				"inbound:" + formatInt64(inbound.ID): inbound.UpdatedAt.UTC().Format(time.RFC3339Nano),
				"server:" + formatInt64(exit.ID):     exit.Revision,
			}
			operation := map[string]any{
				"capability": "topology.write",
				"input": map[string]any{
					"path":  map[string]any{"kind": model.ProxyPathKindChain, "name_mode": model.ProxyPathNameAuto, "name_template": []any{}, "inbound_id": inbound.ID, "exit_region_mode": "auto", "enabled": true},
					"steps": []map[string]any{{"node_type": model.ProxyPathStepServerInbound, "transport_mode": model.ProxyPathTransportSingBox, "server_id": exit.ID}},
				},
			}
			candidates = append(candidates, map[string]any{
				"entry_server_id": input.EntryServerID, "entry_inbound_id": inbound.ID, "exit_server_id": exit.ID,
				"exit_region": effectiveRegion(exit), "hops": 1, "objective": strings.TrimSpace(input.Objective),
				"requires_topology_changeset": true, "suggested_changeset": map[string]any{"base_revisions": baseRevisions, "operation": operation},
			})
			if len(candidates) == 5 {
				break
			}
		}
		if len(candidates) == 5 {
			break
		}
	}
	warnings := []string{}
	if len(entryInbounds) == 0 {
		warnings = append(warnings, "入口服务器没有可用于代理链路的已启用入站")
	} else if len(candidates) == 0 {
		warnings = append(warnings, "没有满足地域和在线状态约束的出口节点")
	}
	result := PlanResult{Kind: "proxy_path", Valid: len(candidates) > 0, Warnings: warnings, Candidates: candidates}
	if len(candidates) > 0 {
		result.SuggestedChangeset = candidates[0]["suggested_changeset"].(map[string]any)
	}
	return result, nil
}

func (s *Service) PlanDeployment(ctx context.Context, principal Principal, raw json.RawMessage) (PlanResult, error) {
	var input struct {
		ServerIDs []int64 `json:"server_ids"`
		Reason    string  `json:"reason"`
	}
	if err := strictUnmarshal(raw, &input); err != nil {
		return PlanResult{}, err
	}
	if len(input.ServerIDs) == 0 || len(input.ServerIDs) > 100 {
		return PlanResult{}, errors.New("server_ids must contain between 1 and 100 items")
	}
	candidates := []map[string]any{}
	warnings := []string{}
	baseRevisions := map[string]string{}
	for _, id := range slices.Compact(input.ServerIDs) {
		server, err := s.GetServer(ctx, principal, id)
		if err != nil {
			return PlanResult{}, err
		}
		ready := server.AgentConnected && server.Status == model.ServerOnline
		if !ready {
			warnings = append(warnings, "服务器 "+formatInt64(id)+" 当前未在线连接")
		}
		baseRevisions["server:"+formatInt64(id)] = server.Revision
		candidates = append(candidates, map[string]any{"server_id": id, "revision": server.Revision, "ready": ready, "requires_core_reload": true})
	}
	return PlanResult{Kind: "deployment", Valid: len(warnings) == 0, Warnings: warnings, Candidates: candidates, SuggestedChangeset: map[string]any{
		"base_revisions": baseRevisions,
		"operation":      map[string]any{"capability": "deployments.apply", "input": map[string]any{"server_ids": input.ServerIDs, "reason": strings.TrimSpace(input.Reason)}},
	}}, nil
}

func (s *Service) PlanIncidentResponse(ctx context.Context, principal Principal, raw json.RawMessage) (PlanResult, error) {
	var input struct {
		IncidentID   string   `json:"incident_id"`
		UserID       int64    `json:"user_id"`
		RuleScore    float64  `json:"rule_score"`
		AnomalyScore float64  `json:"anomaly_score"`
		EvidenceRefs []string `json:"evidence_refs"`
	}
	if err := strictUnmarshal(raw, &input); err != nil {
		return PlanResult{}, err
	}
	if input.UserID <= 0 || !principal.AllowsInt64("user_ids", input.UserID) {
		return PlanResult{}, errors.New("authorized user_id is required")
	}
	if _, err := s.store.GetUser(ctx, input.UserID); err != nil {
		return PlanResult{}, err
	}
	actions := []string{"notify_admin", "request_manual_review"}
	if input.RuleScore >= 80 && input.AnomalyScore >= 80 {
		actions = append(actions, "propose_temporary_subscription_suspension")
	}
	candidate := map[string]any{"incident_id": strings.TrimSpace(input.IncidentID), "user_id": input.UserID, "recommended_actions": actions, "evidence_refs": input.EvidenceRefs, "automatic_enforcement": false}
	return PlanResult{Kind: "incident_response", Valid: true, Warnings: []string{"处置建议不替代 Controller 规则与人工审批"}, Candidates: []map[string]any{candidate}}, nil
}

func relayPreference(server ServerDTO, preferred []string) int {
	region := effectiveRegion(server)
	for index, item := range preferred {
		if strings.EqualFold(region, strings.TrimSpace(item)) {
			return index
		}
	}
	return len(preferred) + 1
}

func effectiveRegion(server ServerDTO) string {
	if server.RegionCode != "" {
		return server.RegionCode
	}
	return server.DetectedRegionCode
}
