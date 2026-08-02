package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type proxyPathReuseSource struct {
	InboundID int64 `json:"inbound_id,omitempty"`
	StepID    int64 `json:"step_id,omitempty"`
}

type proxyPathReuseRequest struct {
	Sources                []proxyPathReuseSource           `json:"sources"`
	TargetServerID         int64                            `json:"target_server_id"`
	TargetKind             string                           `json:"target_kind"`
	TargetInboundID        int64                            `json:"target_inbound_id,omitempty"`
	ChainProtocol          model.Protocol                   `json:"chain_protocol,omitempty"`
	ChainMethod            string                           `json:"chain_method,omitempty"`
	RealityHandshakeServer string                           `json:"reality_handshake_server,omitempty"`
	RealityHandshakePort   int                              `json:"reality_handshake_port,omitempty"`
	TransportMode          model.ProxyPathStepTransportMode `json:"transport_mode,omitempty"`
	TunnelType             model.TunnelType                 `json:"tunnel_type,omitempty"`
	SSHPort                int                              `json:"ssh_port,omitempty"`
	PersistentKeepalive    int                              `json:"persistent_keepalive,omitempty"`
	CopyMode               string                           `json:"copy_mode,omitempty"`
	BranchPathID           int64                            `json:"branch_path_id,omitempty"`
}

type proxyPathReuseTargetOption struct {
	Kind             string         `json:"kind"`
	InboundID        int64          `json:"inbound_id,omitempty"`
	Protocol         model.Protocol `json:"protocol"`
	Label            string         `json:"label"`
	Port             int            `json:"port,omitempty"`
	Visibility       string         `json:"visibility"`
	ActiveReuseCount int            `json:"active_reuse_count"`
	Eligible         bool           `json:"eligible"`
	Reason           string         `json:"reason,omitempty"`
	ChainMethod      string         `json:"chain_method,omitempty"`
}

type proxyPathReuseBranchOption struct {
	PathID    int64  `json:"path_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Eligible  bool   `json:"eligible"`
	Reason    string `json:"reason,omitempty"`
	StepCount int    `json:"step_count"`
}

type proxyPathReusePlan struct {
	Revision          string
	Writes            []store.ProxyPathReuseWrite
	Paths             []model.ProxyPath
	Steps             []model.ProxyPathStep
	SourceCount       int
	ResultPathCount   int
	AffectedServerIDs []int64
}

type proxyPathReuseSourceState struct {
	RootInbound model.Inbound
	Path        *model.ProxyPath
	Prefix      []model.ProxyPathStep
}

func (s *Server) proxyPathReusePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request proxyPathReuseRequest
	if !decode(w, r, &request) {
		return
	}
	preview, err := s.buildProxyPathReusePreview(r.Context(), request)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	write(w, http.StatusOK, preview)
}

func (s *Server) proxyPathReuseApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request proxyPathReuseRequest
	if !decode(w, r, &request) {
		return
	}
	plan, err := s.applyProxyPathReuseOperation(r.Context(), request, nil)
	if err != nil {
		if errors.Is(err, store.ErrRoutingTopologyChanged) {
			fail(w, errors.New("代理拓扑已发生变化，请重新预览后再提交"), http.StatusConflict)
			return
		}
		fail(w, err, http.StatusBadRequest)
		return
	}
	items, steps, servers, inbounds, externals, err := s.proxyPathNameData(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	createdIDs := map[int64]bool{}
	for _, write := range plan.Writes {
		createdIDs[write.Path.ID] = true
	}
	resolved := core.ResolveProxyPathNames(items, steps, servers, inbounds, externals)
	created := make([]model.ProxyPath, 0, len(plan.Writes))
	for _, path := range resolved {
		if createdIDs[path.ID] {
			created = append(created, path)
		}
	}
	createdSteps := make([]model.ProxyPathStep, 0)
	for _, step := range steps {
		if createdIDs[step.PathID] {
			createdSteps = append(createdSteps, step)
		}
	}
	auditReq(s, r, "reuse", "proxy-path", fmt.Sprintf("%d paths", len(created)))
	write(w, http.StatusCreated, map[string]any{
		"proxy_paths":         created,
		"proxy_path_steps":    publicProxyPathSteps(createdSteps),
		"result_path_count":   plan.ResultPathCount,
		"affected_server_ids": plan.AffectedServerIDs,
		"requires_deployment": true,
	})
}

func (s *Server) applyProxyPathReuseOperation(ctx context.Context, request proxyPathReuseRequest, authorize func(*proxyPathReusePlan) error) (*proxyPathReusePlan, error) {
	plan, err := s.planProxyPathReuse(ctx, request, true)
	if err != nil {
		return nil, err
	}
	if authorize != nil {
		if err := authorize(plan); err != nil {
			return nil, err
		}
	}
	if err := s.store.ApplyProxyPathReuse(ctx, plan.Revision, plan.Writes); err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Server) buildProxyPathReusePreview(ctx context.Context, request proxyPathReuseRequest) (map[string]any, error) {
	if err := normalizeProxyPathReuseRequest(&request); err != nil {
		return nil, err
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := serverByIDFromSlice(data.Servers)[request.TargetServerID]; !ok {
		return nil, sql.ErrNoRows
	}
	targets := proxyPathReuseTargets(data, request)
	branches := []proxyPathReuseBranchOption{}
	if request.TargetKind == "existing" && request.TargetInboundID > 0 {
		paths := append([]model.ProxyPath(nil), data.ProxyPaths...)
		resolved := core.ResolveProxyPathNames(paths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds)
		sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].ID < resolved[j].ID })
		for _, path := range resolved {
			if !path.Enabled || path.InboundID != request.TargetInboundID {
				continue
			}
			candidate := request
			candidate.CopyMode = "single"
			candidate.BranchPathID = path.ID
			_, planErr := s.planProxyPathReuse(ctx, candidate, false)
			branches = append(branches, proxyPathReuseBranchOption{
				PathID: path.ID, Name: firstNonEmptyString(path.Name, fmt.Sprintf("路径 %d", path.ID)), Kind: string(path.Kind),
				Eligible: planErr == nil, Reason: errorText(planErr), StepCount: len(proxyPathStepsForPath(data.ProxyPathSteps, path.ID)),
			})
		}
	}
	var selectedPlan *proxyPathReusePlan
	var selectedError string
	if proxyPathReuseTargetSelected(request) {
		selectedPlan, err = s.planProxyPathReuse(ctx, request, false)
		if err != nil {
			selectedError = err.Error()
		}
	}
	result := map[string]any{
		"target_options": targets,
		"branch_options": branches,
		"valid":          selectedError == "",
		"error":          selectedError,
	}
	if selectedPlan != nil {
		result["source_count"] = selectedPlan.SourceCount
		result["result_path_count"] = selectedPlan.ResultPathCount
		result["affected_server_ids"] = selectedPlan.AffectedServerIDs
	}
	if request.CopyMode == "all" {
		for _, branch := range branches {
			if !branch.Eligible {
				result["valid"] = false
				result["error"] = "目标入口包含无法拼接的启用分支，请改选单条分支"
				break
			}
		}
	}
	return result, nil
}

func proxyPathReuseTargets(data store.FullRoutingConfig, request proxyPathReuseRequest) []proxyPathReuseTargetOption {
	countsByInbound := map[int64]int{}
	generatedCounts := map[string]int{}
	pathEnabled := map[int64]bool{}
	for _, path := range data.ProxyPaths {
		pathEnabled[path.ID] = path.Enabled
	}
	for _, step := range data.ProxyPathSteps {
		if !pathEnabled[step.PathID] || step.NodeType != model.ProxyPathStepServerInbound {
			continue
		}
		if step.InboundID != nil && *step.InboundID != 0 {
			countsByInbound[*step.InboundID]++
			continue
		}
		if step.ServerID == nil || *step.ServerID != request.TargetServerID {
			continue
		}
		chain, err := core.ParseProxyPathChainConfig(step.ConfigJSON)
		if err == nil {
			generatedCounts[proxyPathGeneratedCountKey(chain)]++
		}
	}
	generated := []proxyPathReuseTargetOption{
		{Kind: "generated", Protocol: model.ProtocolSS, ChainMethod: core.DefaultProxyPathChainMethod, Label: "SS 2022-128", Visibility: "system_hidden", Eligible: true},
		{Kind: "generated", Protocol: model.ProtocolSS, ChainMethod: "2022-blake3-aes-256-gcm", Label: "SS 2022-256", Visibility: "system_hidden", Eligible: true},
		{Kind: "generated", Protocol: model.ProtocolSS, ChainMethod: "2022-blake3-chacha20-poly1305", Label: "SS 2022-ChaCha20", Visibility: "system_hidden", Eligible: true},
		{Kind: "generated", Protocol: model.ProtocolVLESS, Label: "VLESS Reality", Visibility: "system_hidden", Eligible: true},
		{Kind: "generated", Protocol: model.ProtocolMieru, Label: "Mieru TCP", Visibility: "system_hidden", Eligible: true},
	}
	for index := range generated {
		chain := core.ProxyPathChainConfig{Protocol: generated[index].Protocol, Method: generated[index].ChainMethod, RealityHandshakeServer: request.RealityHandshakeServer, RealityHandshakePort: request.RealityHandshakePort}
		generated[index].ActiveReuseCount = generatedCounts[proxyPathGeneratedCountKey(chain)]
	}
	out := generated
	for _, inbound := range data.Inbounds {
		if inbound.ServerID != request.TargetServerID || !inbound.Enabled || inbound.Protocol == model.ProtocolSSH || !core.InboundSupportsMultipleUsers(inbound) {
			continue
		}
		if _, err := core.AdapterFor(inbound.Protocol); err != nil {
			continue
		}
		option := proxyPathReuseTargetOption{Kind: "existing", InboundID: inbound.ID, Protocol: inbound.Protocol, Label: inbound.Name, Port: inbound.Port, Visibility: "existing_visible", ActiveReuseCount: countsByInbound[inbound.ID], Eligible: true}
		out = append(out, option)
	}
	return out
}

func proxyPathGeneratedCountKey(chain core.ProxyPathChainConfig) string {
	if chain.Protocol == model.ProtocolSS {
		return string(chain.Protocol) + ":" + firstNonEmptyString(chain.Method, core.DefaultProxyPathChainMethod)
	}
	if chain.Protocol == model.ProtocolVLESS {
		return fmt.Sprintf("%s:%s:%d", chain.Protocol, firstNonEmptyString(chain.RealityHandshakeServer, core.DefaultProxyPathRealityHandshakeServer), chain.RealityHandshakePort)
	}
	return string(chain.Protocol)
}

func (s *Server) planProxyPathReuse(ctx context.Context, request proxyPathReuseRequest, generateSecrets bool) (*proxyPathReusePlan, error) {
	if err := normalizeProxyPathReuseRequest(&request); err != nil {
		return nil, err
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	revision, err := s.store.RoutingTopologyRevision(ctx)
	if err != nil {
		return nil, err
	}
	sources, err := proxyPathReuseSources(data, request.Sources)
	if err != nil {
		return nil, err
	}
	targetInbound, err := proxyPathReuseTargetInbound(data, request)
	if err != nil {
		return nil, err
	}
	branches, err := proxyPathReuseSelectedBranches(data, request)
	if err != nil {
		return nil, err
	}
	projectedPaths := append([]model.ProxyPath(nil), data.ProxyPaths...)
	projectedSteps := append([]model.ProxyPathStep(nil), data.ProxyPathSteps...)
	writes := make([]store.ProxyPathReuseWrite, 0, len(sources)*len(branches))
	nextPathID, nextStepID := int64(-1000000000), int64(-2000000000)
	for _, source := range sources {
		for branchIndex, branch := range branches {
			useExisting := source.Path != nil && branchIndex == 0
			path := model.ProxyPath{ID: nextPathID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, NameTemplate: []model.ProxyPathNamePart{}, InboundID: source.RootInbound.ID, ExitRegionMode: "auto", Enabled: true}
			if generateSecrets {
				path.Secret, err = security.RandomToken(24)
				if err != nil {
					return nil, err
				}
			} else {
				path.Secret = fmt.Sprintf("preview-secret-%d", -nextPathID)
			}
			prefix := []model.ProxyPathStep{}
			if source.Path != nil {
				path.InboundID = source.Path.InboundID
				prefix = cloneProxyPathSteps(source.Prefix)
				if useExisting {
					path.ID = source.Path.ID
					path.Secret = source.Path.Secret
				} else {
					for index := range prefix {
						prefix[index].ID = nextStepID
						prefix[index].PathID = path.ID
						prefix[index].CreatedAt = model.ProxyPathStep{}.CreatedAt
						prefix[index].UpdatedAt = model.ProxyPathStep{}.UpdatedAt
						nextStepID--
					}
				}
			}
			if !useExisting {
				nextPathID--
			}
			if branch != nil {
				path.Kind = branch.Kind
				path.ExitRegionMode = branch.ExitRegionMode
				path.ExitRegionCode = branch.ExitRegionCode
			}
			appendSteps := make([]model.ProxyPathStep, 0, 1)
			targetStep := proxyPathReuseTargetStep(request, targetInbound)
			targetStep.ID, targetStep.PathID, targetStep.Position = nextStepID, path.ID, len(prefix)+1
			nextStepID--
			if err := s.normalizeProxyPathStepCandidate(ctx, &targetStep); err != nil {
				return nil, err
			}
			appendSteps = append(appendSteps, targetStep)
			branchSourcePosition := 0
			if branch != nil {
				suffix := proxyPathStepsForPath(data.ProxyPathSteps, branch.ID)
				sort.SliceStable(suffix, func(i, j int) bool { return suffix[i].Position < suffix[j].Position })
				for _, original := range suffix {
					step := original
					step.ID, step.PathID, step.Position = nextStepID, path.ID, len(prefix)+len(appendSteps)+1
					nextStepID--
					step.CreatedAt, step.UpdatedAt = model.ProxyPathStep{}.CreatedAt, model.ProxyPathStep{}.UpdatedAt
					if err := s.normalizeProxyPathStepCandidate(ctx, &step); err != nil {
						return nil, err
					}
					appendSteps = append(appendSteps, step)
					if branch.BranchSourceStepID != nil && original.ID == *branch.BranchSourceStepID {
						branchSourcePosition = step.Position
					}
				}
				if path.Kind == model.ProxyPathKindDirect && branchSourcePosition == 0 {
					branchSourcePosition = targetStep.Position
				}
			}
			allPathSteps := append(cloneProxyPathSteps(prefix), appendSteps...)
			if err := normalizeProxyPathProcessingRolesInMemory(allPathSteps, path.ID); err != nil {
				return nil, err
			}
			if err := s.validateProxyPathServerLoop(ctx, path.InboundID, allPathSteps); err != nil {
				return nil, err
			}
			processingByPosition := map[int]bool{}
			for _, step := range allPathSteps {
				processingByPosition[step.Position] = step.ProcessingRole
			}
			for index := range appendSteps {
				appendSteps[index].ProcessingRole = processingByPosition[appendSteps[index].Position]
			}
			if branchSourcePosition > 0 {
				for _, step := range allPathSteps {
					if step.Position == branchSourcePosition {
						id := step.ID
						path.BranchSourceStepID = &id
						break
					}
				}
			}
			if useExisting {
				for index := range projectedPaths {
					if projectedPaths[index].ID == path.ID {
						projectedPaths[index] = path
						break
					}
				}
				projectedSteps = append(projectedSteps, appendSteps...)
			} else {
				projectedPaths = append(projectedPaths, path)
				projectedSteps = append(projectedSteps, allPathSteps...)
			}
			storedSteps := appendSteps
			if !useExisting {
				storedSteps = allPathSteps
			}
			existingPathID := int64(0)
			if useExisting {
				existingPathID = path.ID
			}
			writes = append(writes, store.ProxyPathReuseWrite{Path: path, Steps: storedSteps, ExistingPathID: existingPathID, BranchSourcePosition: branchSourcePosition})
		}
	}
	for _, inbound := range data.Inbounds {
		count := 0
		for _, path := range projectedPaths {
			if path.Enabled && path.InboundID == inbound.ID {
				count++
			}
		}
		if count > 1 && !core.InboundSupportsMultipleUsers(inbound) {
			return nil, errors.New("来源入口协议不支持多个代理分支")
		}
	}
	for index := range writes {
		pathSteps := proxyPathStepsForPath(projectedSteps, writes[index].Path.ID)
		if err := core.NormalizeProxyPathName(&writes[index].Path, pathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds); err != nil {
			return nil, err
		}
		for projectedIndex := range projectedPaths {
			if projectedPaths[projectedIndex].ID == writes[index].Path.ID {
				projectedPaths[projectedIndex] = writes[index].Path
			}
		}
	}
	plans, err := core.BuildProxyPathPlansWithLedger(projectedPaths, projectedSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	if err != nil {
		return nil, err
	}
	affected := map[int64]bool{}
	writePathIDs := map[int64]bool{}
	for _, write := range writes {
		writePathIDs[write.Path.ID] = true
	}
	for _, plan := range plans {
		if !writePathIDs[plan.PathID] {
			continue
		}
		if root := inboundByIDFromSlice(data.Inbounds)[plan.InboundID]; root.ID != 0 {
			affected[root.ServerID] = true
		}
		for _, step := range plan.Steps {
			if step.ServerID != nil {
				affected[*step.ServerID] = true
			}
		}
	}
	affectedIDs := make([]int64, 0, len(affected))
	for id := range affected {
		affectedIDs = append(affectedIDs, id)
	}
	sort.Slice(affectedIDs, func(i, j int) bool { return affectedIDs[i] < affectedIDs[j] })
	return &proxyPathReusePlan{Revision: revision, Writes: writes, Paths: projectedPaths, Steps: projectedSteps, SourceCount: len(sources), ResultPathCount: len(writes), AffectedServerIDs: affectedIDs}, nil
}

func normalizeProxyPathReuseRequest(request *proxyPathReuseRequest) error {
	if len(request.Sources) == 0 || len(request.Sources) > 128 {
		return errors.New("sources 必须包含 1 到 128 个来源")
	}
	if request.TargetServerID <= 0 {
		return errors.New("target_server_id required")
	}
	request.TargetKind = strings.ToLower(strings.TrimSpace(request.TargetKind))
	if request.TargetKind == "" {
		request.TargetKind = "generated"
	}
	if request.TargetKind != "generated" && request.TargetKind != "existing" {
		return errors.New("target_kind 必须是 generated 或 existing")
	}
	if request.TargetKind == "existing" && request.TargetInboundID <= 0 {
		return errors.New("target_inbound_id required")
	}
	if request.ChainProtocol == "" {
		request.ChainProtocol = model.ProtocolSS
	}
	if request.ChainMethod == "" {
		request.ChainMethod = core.DefaultProxyPathChainMethod
	}
	if request.RealityHandshakeServer == "" {
		request.RealityHandshakeServer = core.DefaultProxyPathRealityHandshakeServer
	}
	if request.RealityHandshakePort == 0 {
		request.RealityHandshakePort = core.DefaultProxyPathRealityHandshakePort
	}
	if request.TransportMode == "" {
		request.TransportMode = model.ProxyPathTransportSingBox
	}
	if request.TransportMode != model.ProxyPathTransportSingBox && request.TransportMode != model.ProxyPathTransportTunnel {
		return errors.New("复用入口只能使用 singbox 或 tunnel")
	}
	request.CopyMode = strings.ToLower(strings.TrimSpace(request.CopyMode))
	if request.CopyMode == "" {
		request.CopyMode = "none"
	}
	if request.CopyMode != "none" && request.CopyMode != "all" && request.CopyMode != "single" {
		return errors.New("copy_mode 必须是 none、all 或 single")
	}
	if request.TargetKind != "existing" && request.CopyMode != "none" {
		return errors.New("系统隐藏入口没有可复制分支")
	}
	if request.CopyMode == "single" && request.BranchPathID <= 0 {
		return errors.New("branch_path_id required")
	}
	return nil
}

func proxyPathReuseSources(data store.FullRoutingConfig, requests []proxyPathReuseSource) ([]proxyPathReuseSourceState, error) {
	inbounds := inboundByIDFromSlice(data.Inbounds)
	paths := map[int64]model.ProxyPath{}
	steps := map[int64]model.ProxyPathStep{}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, path := range data.ProxyPaths {
		paths[path.ID] = path
	}
	for _, step := range data.ProxyPathSteps {
		steps[step.ID] = step
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	seen := map[string]bool{}
	out := make([]proxyPathReuseSourceState, 0, len(requests))
	for _, request := range requests {
		if (request.InboundID > 0) == (request.StepID > 0) {
			return nil, errors.New("每个来源必须且只能设置 inbound_id 或 step_id")
		}
		if request.InboundID > 0 {
			inbound, ok := inbounds[request.InboundID]
			if !ok || !inbound.Enabled || inbound.Protocol == model.ProtocolSSH {
				return nil, errors.New("来源入口不存在、已禁用或不支持代理路径")
			}
			key := fmt.Sprintf("inbound:%d", inbound.ID)
			if seen[key] {
				return nil, errors.New("来源重复")
			}
			seen[key] = true
			out = append(out, proxyPathReuseSourceState{RootInbound: inbound})
			continue
		}
		step, ok := steps[request.StepID]
		path, pathOK := paths[step.PathID]
		root, rootOK := inbounds[path.InboundID]
		if !ok || !pathOK || !path.Enabled || !rootOK {
			return nil, errors.New("来源路径步骤不存在或未启用")
		}
		ordered := stepsByPath[path.ID]
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
		if len(ordered) == 0 || ordered[len(ordered)-1].ID != step.ID {
			return nil, errors.New("只能从路径的最后一个步骤继续连接")
		}
		key := fmt.Sprintf("path:%d", path.ID)
		if seen[key] {
			return nil, errors.New("来源重复")
		}
		seen[key] = true
		pathCopy := path
		out = append(out, proxyPathReuseSourceState{RootInbound: root, Path: &pathCopy, Prefix: cloneProxyPathSteps(ordered)})
	}
	return out, nil
}

func proxyPathReuseTargetInbound(data store.FullRoutingConfig, request proxyPathReuseRequest) (*model.Inbound, error) {
	if _, ok := serverByIDFromSlice(data.Servers)[request.TargetServerID]; !ok {
		return nil, sql.ErrNoRows
	}
	if request.TargetKind != "existing" {
		return nil, nil
	}
	inbound, ok := inboundByIDFromSlice(data.Inbounds)[request.TargetInboundID]
	if !ok || inbound.ServerID != request.TargetServerID || !inbound.Enabled {
		return nil, errors.New("目标入口不存在、已禁用或不属于目标服务器")
	}
	if inbound.Protocol == model.ProtocolSSH || !core.InboundSupportsMultipleUsers(inbound) {
		return nil, errors.New("目标入口不支持路径独立凭据")
	}
	if _, err := core.AdapterFor(inbound.Protocol); err != nil {
		return nil, err
	}
	return &inbound, nil
}

func proxyPathReuseSelectedBranches(data store.FullRoutingConfig, request proxyPathReuseRequest) ([]*model.ProxyPath, error) {
	if request.CopyMode == "none" {
		return []*model.ProxyPath{nil}, nil
	}
	items := []model.ProxyPath{}
	for _, path := range data.ProxyPaths {
		if path.Enabled && path.InboundID == request.TargetInboundID {
			items = append(items, path)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if request.CopyMode == "single" {
		for index := range items {
			if items[index].ID == request.BranchPathID {
				copy := items[index]
				return []*model.ProxyPath{&copy}, nil
			}
		}
		return nil, errors.New("选择的目标分支不存在或未启用")
	}
	if len(items) == 0 {
		return nil, errors.New("目标入口没有启用分支")
	}
	out := make([]*model.ProxyPath, 0, len(items))
	for index := range items {
		copy := items[index]
		out = append(out, &copy)
	}
	return out, nil
}

func proxyPathReuseTargetStep(request proxyPathReuseRequest, targetInbound *model.Inbound) model.ProxyPathStep {
	step := model.ProxyPathStep{NodeType: model.ProxyPathStepServerInbound, TransportMode: request.TransportMode}
	if targetInbound != nil {
		id, serverID := targetInbound.ID, targetInbound.ServerID
		step.InboundID, step.ServerID = &id, &serverID
	} else {
		serverID := request.TargetServerID
		step.ServerID = &serverID
	}
	cfg := map[string]any{}
	if targetInbound == nil {
		cfg["chain_protocol"] = request.ChainProtocol
		switch request.ChainProtocol {
		case model.ProtocolSS:
			cfg["chain_method"] = request.ChainMethod
		case model.ProtocolVLESS:
			cfg["reality_handshake_server"] = request.RealityHandshakeServer
			cfg["reality_handshake_port"] = request.RealityHandshakePort
		}
	}
	if request.TransportMode == model.ProxyPathTransportTunnel {
		tunnelType := request.TunnelType
		if tunnelType == "" {
			tunnelType = model.TunnelTypeSSH
		}
		cfg["type"] = tunnelType
		if tunnelType == model.TunnelTypeSSH {
			cfg["ssh_port"] = request.SSHPort
		} else {
			cfg["persistent_keepalive"] = request.PersistentKeepalive
		}
	}
	b, _ := json.Marshal(cfg)
	step.ConfigJSON = string(b)
	return step
}

func cloneProxyPathSteps(items []model.ProxyPathStep) []model.ProxyPathStep {
	return append([]model.ProxyPathStep(nil), items...)
}

func proxyPathReuseTargetSelected(request proxyPathReuseRequest) bool {
	return request.TargetKind == "generated" || request.TargetInboundID > 0
}

func serverByIDFromSlice(items []model.Server) map[int64]model.Server {
	out := make(map[int64]model.Server, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func inboundByIDFromSlice(items []model.Inbound) map[int64]model.Inbound {
	out := make(map[int64]model.Inbound, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
