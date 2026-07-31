package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	RegionStatusResolved   = "resolved"
	RegionStatusPending    = "pending"
	RegionStatusFailed     = "failed"
	RegionStatusStale      = "stale"
	RegionStatusUnlinked   = "unlinked"
	RegionStatusIncomplete = "incomplete"
	RegionStatusConflict   = "conflict"

	RegionSourcePathManual     = "path_manual"
	RegionSourceExternalManual = "external_manual"
	RegionSourceProbe          = "probe"
	RegionSourceServerManual   = "server_manual"
	RegionSourceServerDetected = "server_detected"
	RegionSourceUnresolved     = "unresolved"
)

func NormalizeRegionCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return code
}

func EffectiveServerRegion(server model.Server) (string, string) {
	if strings.EqualFold(strings.TrimSpace(server.RegionMode), "manual") {
		if code := NormalizeRegionCode(server.RegionCode); code != "" {
			return code, RegionSourceServerManual
		}
	}
	if code := NormalizeRegionCode(server.DetectedRegionCode); code != "" {
		return code, RegionSourceServerDetected
	}
	return "", RegionSourceUnresolved
}

func RegionFlagEmoji(code string) string {
	code = NormalizeRegionCode(code)
	if code == "" {
		code = "AQ"
	}
	return string([]rune{rune(0x1F1E6) + rune(code[0]-'A'), rune(0x1F1E6) + rune(code[1]-'A')})
}

func ExternalEgressProbeTargets(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, externals []model.ExternalOutbound) []model.ExternalEgressProbeTarget {
	serverByID := make(map[int64]model.Server, len(servers))
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	inboundByID := make(map[int64]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	externalByID := make(map[int64]model.ExternalOutbound, len(externals))
	for _, external := range externals {
		externalByID[external.ID] = external
	}
	stepsByPath := make(map[int64][]model.ProxyPathStep)
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	targets := []model.ExternalEgressProbeTarget{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		root, ok := inboundByID[path.InboundID]
		if !ok || !root.Enabled {
			continue
		}
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		if len(ordered) == 0 || ordered[len(ordered)-1].NodeType != model.ProxyPathStepImported {
			continue
		}
		currentServerID := root.ServerID
		for _, step := range ordered[:len(ordered)-1] {
			if step.NodeType != model.ProxyPathStepServerInbound {
				continue
			}
			if serverID, _, found := proxyPathStepTargetServer(step, inboundByID); found {
				currentServerID = serverID
			}
		}
		terminal := ordered[len(ordered)-1]
		if terminal.ExternalOutboundID == nil || currentServerID == 0 {
			continue
		}
		external, ok := externalByID[*terminal.ExternalOutboundID]
		if !ok || !external.Enabled || !externalUsableOnServer(external, currentServerID) {
			continue
		}
		fingerprint := externalEgressTopologyFingerprint(path, ordered, serverByID, inboundByID, externalByID, currentServerID)
		if fingerprint == "" {
			continue
		}
		targets = append(targets, model.ExternalEgressProbeTarget{
			ProbeID:             fmt.Sprintf("egress-%d-%s", path.ID, fingerprint[:16]),
			PathID:              path.ID,
			ExternalOutboundID:  external.ID,
			OwnerServerID:       currentServerID,
			OutboundTag:         proxyPathStepTag(path.ID, terminal.Position),
			TopologyFingerprint: fingerprint,
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].PathID < targets[j].PathID })
	return targets
}

func externalEgressTopologyFingerprint(path model.ProxyPath, steps []model.ProxyPathStep, servers map[int64]model.Server, inbounds map[int64]model.Inbound, externals map[int64]model.ExternalOutbound, ownerServerID int64) string {
	type serverState struct {
		ID           int64             `json:"id"`
		EntryAddress string            `json:"entry_address"`
		PublicIPv4   string            `json:"public_ipv4"`
		PublicIPv6   string            `json:"public_ipv6"`
		EntryIPMode  model.EntryIPMode `json:"entry_ip_mode"`
		ListenIP     string            `json:"listen_ip"`
		IPStack      model.IPStack     `json:"ip_stack"`
	}
	type inboundState struct {
		ID          int64             `json:"id"`
		ServerID    int64             `json:"server_id"`
		Protocol    model.Protocol    `json:"protocol"`
		ListenIP    string            `json:"listen_ip"`
		Port        int               `json:"port"`
		EntryIPMode model.EntryIPMode `json:"entry_ip_mode"`
		ExternalIP  string            `json:"external_ip"`
		ConfigJSON  string            `json:"config_json"`
	}
	type externalState struct {
		ID            int64                       `json:"id"`
		ServerID      *int64                      `json:"server_id,omitempty"`
		Protocol      model.Protocol              `json:"protocol"`
		Scope         model.ExternalOutboundScope `json:"scope"`
		TargetAddress string                      `json:"target_address"`
		TargetPort    int                         `json:"target_port"`
		ConfigJSON    string                      `json:"config_json"`
		Enabled       bool                        `json:"enabled"`
	}
	type stepState struct {
		ID                 int64                            `json:"id"`
		Position           int                              `json:"position"`
		NodeType           model.ProxyPathStepNodeType      `json:"node_type"`
		TransportMode      model.ProxyPathStepTransportMode `json:"transport_mode"`
		ProcessingRole     bool                             `json:"processing_role"`
		ServerID           *int64                           `json:"server_id,omitempty"`
		InboundID          *int64                           `json:"inbound_id,omitempty"`
		ExternalOutboundID *int64                           `json:"external_outbound_id,omitempty"`
		ConfigJSON         string                           `json:"config_json"`
	}
	serverIDs := map[int64]bool{}
	inboundIDs := map[int64]bool{path.InboundID: true}
	externalIDs := map[int64]bool{}
	normalizedSteps := make([]stepState, 0, len(steps))
	for _, step := range steps {
		transport := step.TransportMode
		if transport == "" {
			transport = model.ProxyPathTransportSingBox
		}
		normalizedSteps = append(normalizedSteps, stepState{
			ID: step.ID, Position: step.Position, NodeType: step.NodeType,
			TransportMode: transport, ProcessingRole: step.ProcessingRole,
			ServerID: step.ServerID, InboundID: step.InboundID,
			ExternalOutboundID: step.ExternalOutboundID,
			ConfigJSON:         canonicalJSONObject(step.ConfigJSON),
		})
	}
	for i := range normalizedSteps {
		if normalizedSteps[i].ServerID != nil {
			serverIDs[*normalizedSteps[i].ServerID] = true
		}
		if normalizedSteps[i].InboundID != nil {
			inboundIDs[*normalizedSteps[i].InboundID] = true
		}
		if normalizedSteps[i].ExternalOutboundID != nil {
			externalIDs[*normalizedSteps[i].ExternalOutboundID] = true
		}
	}
	for inboundID := range inboundIDs {
		if inbound, ok := inbounds[inboundID]; ok {
			serverIDs[inbound.ServerID] = true
		}
	}
	serverStates := []serverState{}
	for serverID := range serverIDs {
		server, ok := servers[serverID]
		if !ok {
			continue
		}
		serverStates = append(serverStates, serverState{server.ID, server.EntryAddress, server.PublicIPv4, server.PublicIPv6, server.EntryIPMode, server.ListenIP, EffectiveIPStack(server)})
	}
	sort.Slice(serverStates, func(i, j int) bool { return serverStates[i].ID < serverStates[j].ID })
	inboundStates := []inboundState{}
	for inboundID := range inboundIDs {
		inbound, ok := inbounds[inboundID]
		if !ok {
			continue
		}
		inboundStates = append(inboundStates, inboundState{inbound.ID, inbound.ServerID, inbound.Protocol, inbound.ListenIP, inbound.Port, inbound.EntryIPMode, inbound.ExternalIP, canonicalJSONObject(inbound.ConfigJSON)})
	}
	sort.Slice(inboundStates, func(i, j int) bool { return inboundStates[i].ID < inboundStates[j].ID })
	externalStates := []externalState{}
	for externalID := range externalIDs {
		external, ok := externals[externalID]
		if !ok {
			continue
		}
		externalStates = append(externalStates, externalState{external.ID, external.ServerID, external.Protocol, external.Scope, external.TargetAddress, external.TargetPort, canonicalJSONObject(external.ConfigJSON), external.Enabled})
	}
	sort.Slice(externalStates, func(i, j int) bool { return externalStates[i].ID < externalStates[j].ID })
	descriptor := struct {
		PathID        int64               `json:"path_id"`
		Kind          model.ProxyPathKind `json:"kind"`
		InboundID     int64               `json:"inbound_id"`
		OwnerServerID int64               `json:"owner_server_id"`
		Steps         []stepState         `json:"steps"`
		Servers       []serverState       `json:"servers"`
		Inbounds      []inboundState      `json:"inbounds"`
		Externals     []externalState     `json:"externals"`
	}{path.ID, path.Kind, path.InboundID, ownerServerID, normalizedSteps, serverStates, inboundStates, externalStates}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func canonicalJSONObject(raw string) string {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return strings.TrimSpace(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return string(encoded)
}

func ResolveProxyPathExitRegions(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, externals []model.ExternalOutbound, results []model.ProxyPathEgressResult) ([]model.ProxyPath, []model.ExternalOutbound) {
	resolvedPaths := append([]model.ProxyPath(nil), paths...)
	resolvedExternals := append([]model.ExternalOutbound(nil), externals...)
	serverByID := make(map[int64]model.Server, len(servers))
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	inboundByID := make(map[int64]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	externalByID := make(map[int64]model.ExternalOutbound, len(externals))
	for _, external := range externals {
		externalByID[external.ID] = external
	}
	stepsByPath := make(map[int64][]model.ProxyPathStep)
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	resultByPath := make(map[int64]model.ProxyPathEgressResult, len(results))
	for _, result := range results {
		resultByPath[result.PathID] = result
	}
	targetByPath := map[int64]model.ExternalEgressProbeTarget{}
	for _, target := range ExternalEgressProbeTargets(paths, steps, servers, inbounds, externals) {
		targetByPath[target.PathID] = target
	}

	for i := range resolvedPaths {
		path := &resolvedPaths[i]
		path.ExitRegionMode = strings.ToLower(strings.TrimSpace(path.ExitRegionMode))
		if path.ExitRegionMode != "manual" {
			path.ExitRegionMode = "auto"
		}
		if path.ExitRegionMode == "manual" {
			if code := NormalizeRegionCode(path.ExitRegionCode); code != "" {
				path.ExitRegionCode = code
				path.EffectiveExitRegionCode = code
				path.ExitRegionSource = RegionSourcePathManual
				path.ExitRegionStatus = RegionStatusResolved
				continue
			}
		}
		path.ExitRegionCode = ""
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		if len(ordered) > 0 && ordered[len(ordered)-1].NodeType == model.ProxyPathStepImported {
			terminal := ordered[len(ordered)-1]
			if terminal.ExternalOutboundID != nil {
				if external, ok := externalByID[*terminal.ExternalOutboundID]; ok && strings.EqualFold(strings.TrimSpace(external.RegionMode), "manual") {
					if code := NormalizeRegionCode(external.RegionCode); code != "" {
						path.EffectiveExitRegionCode = code
						path.ExitRegionSource = RegionSourceExternalManual
						path.ExitRegionStatus = RegionStatusResolved
						continue
					}
				}
			}
			target, targetOK := targetByPath[path.ID]
			result, resultOK := resultByPath[path.ID]
			if !targetOK || !resultOK {
				path.ExitRegionSource = RegionSourceUnresolved
				path.ExitRegionStatus = RegionStatusPending
				continue
			}
			if result.TopologyFingerprint != target.TopologyFingerprint {
				path.ExitRegionSource = RegionSourceUnresolved
				path.ExitRegionStatus = RegionStatusStale
				continue
			}
			path.ExitRegionError = result.LastError
			path.ExitRegionProbedAt = result.LastAttemptAt
			path.ExitRegionStatus = result.Status
			code := detectedRegionCode(result.LastRegionCode)
			path.DetectedExitRegionCode = code
			if code != "" {
				path.EffectiveExitRegionCode = code
				path.ExitRegionSource = RegionSourceProbe
				if path.ExitRegionStatus == "" {
					path.ExitRegionStatus = RegionStatusResolved
				}
			} else {
				path.ExitRegionSource = RegionSourceUnresolved
				if path.ExitRegionStatus == "" || path.ExitRegionStatus == "succeeded" {
					path.ExitRegionStatus = RegionStatusFailed
				}
			}
			continue
		}
		serverID, ok := finalControlledServerID(*path, ordered, inboundByID)
		if ok {
			if server, found := serverByID[serverID]; found {
				path.EffectiveExitRegionCode, path.ExitRegionSource = EffectiveServerRegion(server)
			}
		}
		if path.EffectiveExitRegionCode != "" {
			path.ExitRegionStatus = RegionStatusResolved
		} else {
			path.ExitRegionSource = RegionSourceUnresolved
			path.ExitRegionStatus = RegionStatusPending
		}
	}

	pathsByExternal := map[int64][]int64{}
	for _, target := range targetByPath {
		pathsByExternal[target.ExternalOutboundID] = append(pathsByExternal[target.ExternalOutboundID], target.PathID)
	}
	pathByID := make(map[int64]model.ProxyPath, len(resolvedPaths))
	for _, path := range resolvedPaths {
		pathByID[path.ID] = path
	}
	for i := range resolvedExternals {
		external := &resolvedExternals[i]
		external.RegionMode = strings.ToLower(strings.TrimSpace(external.RegionMode))
		if external.RegionMode == "manual" {
			if code := NormalizeRegionCode(external.RegionCode); code != "" {
				external.RegionCode = code
				external.EffectiveRegionCode = code
				external.RegionSource = RegionSourceExternalManual
				external.RegionStatus = RegionStatusResolved
				continue
			}
		}
		external.RegionMode = "auto"
		external.RegionCode = ""
		linked := pathsByExternal[external.ID]
		if len(linked) == 0 {
			external.RegionSource = RegionSourceUnresolved
			external.RegionStatus = RegionStatusUnlinked
			continue
		}
		codes := map[string]bool{}
		missing, pending, failed := 0, false, false
		for _, pathID := range linked {
			path := pathByID[pathID]
			code := detectedRegionCode(path.DetectedExitRegionCode)
			if code == "" {
				missing++
			} else {
				codes[code] = true
			}
			if path.ExitRegionProbedAt != nil && (external.RegionProbedAt == nil || path.ExitRegionProbedAt.After(*external.RegionProbedAt)) {
				external.RegionProbedAt = path.ExitRegionProbedAt
			}
			if path.ExitRegionStatus == RegionStatusPending {
				pending = true
			}
			if path.ExitRegionStatus == RegionStatusFailed {
				failed = true
				if external.RegionError == "" {
					external.RegionError = path.ExitRegionError
				}
			}
		}
		if missing == 0 && len(codes) == 1 {
			for code := range codes {
				external.DetectedRegionCode = code
				external.EffectiveRegionCode = code
			}
			external.RegionSource = RegionSourceProbe
			switch {
			case pending:
				external.RegionStatus = RegionStatusPending
			case failed:
				external.RegionStatus = RegionStatusFailed
			default:
				external.RegionStatus = RegionStatusResolved
			}
			continue
		}
		external.RegionSource = RegionSourceUnresolved
		switch {
		case len(codes) > 1:
			external.RegionStatus = RegionStatusConflict
		case len(codes) > 0:
			external.RegionStatus = RegionStatusIncomplete
		case failed:
			external.RegionStatus = RegionStatusFailed
		case pending:
			external.RegionStatus = RegionStatusPending
		default:
			external.RegionStatus = RegionStatusIncomplete
		}
	}
	return resolvedPaths, resolvedExternals
}

func finalControlledServerID(path model.ProxyPath, steps []model.ProxyPathStep, inbounds map[int64]model.Inbound) (int64, bool) {
	root, ok := inbounds[path.InboundID]
	if !ok {
		return 0, false
	}
	serverID := root.ServerID
	for _, step := range steps {
		if step.NodeType != model.ProxyPathStepServerInbound {
			continue
		}
		if next, _, found := proxyPathStepTargetServer(step, inbounds); found {
			serverID = next
		}
	}
	return serverID, serverID > 0
}

func detectedRegionCode(code string) string {
	code = NormalizeRegionCode(code)
	if code == "AQ" {
		return ""
	}
	return code
}
