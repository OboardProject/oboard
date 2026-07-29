package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const proxyPathNameSeparator = "｜"

type proxyPathNameState struct {
	path         model.ProxyPath
	route        []string
	middleDepth  int
	features     []string
	featureDepth int
	base         string
	active       bool
}

func ResolveProxyPathNames(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, externals []model.ExternalOutbound) []model.ProxyPath {
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
	stepsByPath := proxyPathNameStepsByPath(steps)
	pathOrder := make(map[int64]int, len(paths))
	for index, path := range paths {
		pathOrder[path.ID] = index
	}

	activePathByInbound := map[int64]bool{}
	for _, path := range paths {
		if proxyPathIsActive(path, stepsByPath[path.ID]) {
			activePathByInbound[path.InboundID] = true
		}
	}
	reserved := map[string]bool{}
	for _, inbound := range inbounds {
		if inbound.Enabled && !activePathByInbound[inbound.ID] {
			if name := strings.TrimSpace(inbound.Name); name != "" {
				reserved[name] = true
			}
		}
	}
	for _, external := range externals {
		if external.Enabled && external.ExposeToUsers {
			if name := strings.TrimSpace(external.Name); name != "" {
				reserved[name] = true
			}
		}
	}

	states := make([]proxyPathNameState, 0, len(paths))
	for _, path := range paths {
		pathSteps := stepsByPath[path.ID]
		route := proxyPathRouteLabels(path, pathSteps, serverByID, inboundByID, externalByID)
		middleDepth := 0
		if path.Kind == model.ProxyPathKindDirect {
			middleDepth = max(0, len(route)-2)
		}
		base := automaticProxyPathName(route, middleDepth)
		if path.NameMode == model.ProxyPathNameCustom {
			if rendered, err := renderProxyPathNameTemplate(path.NameTemplate, serverByID, externalByID); err == nil && strings.TrimSpace(rendered) != "" {
				base = strings.TrimSpace(rendered)
			}
		}
		states = append(states, proxyPathNameState{
			path:        path,
			route:       route,
			middleDepth: middleDepth,
			features:    proxyPathNameFeatures(path, pathSteps, inboundByID, externalByID),
			base:        base,
			active:      proxyPathIsActive(path, pathSteps),
		})
	}

	recomputeProxyPathNames(states)
	for {
		conflicts := proxyPathNameConflicts(states, reserved)
		changed := false
		for index := range states {
			state := &states[index]
			if !conflicts[state.path.ID] || state.path.NameMode == model.ProxyPathNameCustom {
				continue
			}
			middleCount := max(0, len(state.route)-2)
			if state.middleDepth < middleCount {
				state.middleDepth++
				changed = true
			}
		}
		if !changed {
			break
		}
		recomputeProxyPathNames(states)
	}
	for {
		conflicts := proxyPathNameConflicts(states, reserved)
		changed := false
		for index := range states {
			state := &states[index]
			if conflicts[state.path.ID] && state.featureDepth < len(state.features) {
				state.featureDepth++
				changed = true
			}
		}
		if !changed {
			break
		}
		recomputeProxyPathNames(states)
	}

	used := make(map[string]bool, len(reserved)+len(states))
	for name := range reserved {
		used[name] = true
	}
	unresolved := proxyPathNameConflicts(states, reserved)
	for _, state := range states {
		if state.active && !unresolved[state.path.ID] {
			used[state.path.Name] = true
		}
	}
	sort.SliceStable(states, func(i, j int) bool { return states[i].path.ID < states[j].path.ID })
	for index := range states {
		state := &states[index]
		if !state.active || !unresolved[state.path.ID] {
			continue
		}
		for ordinal := 1; ; ordinal++ {
			candidate := fmt.Sprintf("%s%s%02d", state.path.Name, proxyPathNameSeparator, ordinal)
			if !used[candidate] {
				state.path.Name = candidate
				used[candidate] = true
				break
			}
		}
	}
	sort.SliceStable(states, func(i, j int) bool { return pathOrder[states[i].path.ID] < pathOrder[states[j].path.ID] })
	out := make([]model.ProxyPath, len(states))
	for index := range states {
		out[index] = states[index].path
	}
	return out
}

func NormalizeProxyPathName(path *model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, externals []model.ExternalOutbound) error {
	if path.NameMode == "" {
		path.NameMode = model.ProxyPathNameAuto
	}
	switch path.NameMode {
	case model.ProxyPathNameAuto:
		path.NameTemplate = []model.ProxyPathNamePart{}
		return nil
	case model.ProxyPathNameCustom:
	default:
		return fmt.Errorf("unsupported name_mode %q", path.NameMode)
	}
	if len(path.NameTemplate) == 0 {
		return errors.New("name_template is required for custom naming")
	}
	if len(path.NameTemplate) > 32 {
		return errors.New("name_template must not contain more than 32 parts")
	}

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
	allowedServers, allowedExternals := proxyPathNameReferences(*path, steps, inboundByID)
	normalized := make([]model.ProxyPathNamePart, 0, len(path.NameTemplate))
	for _, part := range path.NameTemplate {
		switch part.Kind {
		case model.ProxyPathNameLiteral:
			if part.ServerID != 0 || part.ExternalOutboundID != 0 {
				return errors.New("literal name part cannot reference a resource")
			}
			if part.Value == "" {
				continue
			}
			if len(normalized) > 0 && normalized[len(normalized)-1].Kind == model.ProxyPathNameLiteral {
				normalized[len(normalized)-1].Value += part.Value
				continue
			}
			normalized = append(normalized, model.ProxyPathNamePart{Kind: model.ProxyPathNameLiteral, Value: part.Value})
		case model.ProxyPathNameServer:
			if part.ServerID == 0 || !allowedServers[part.ServerID] {
				return fmt.Errorf("server %d is not part of this proxy path", part.ServerID)
			}
			if _, ok := serverByID[part.ServerID]; !ok {
				return fmt.Errorf("server %d does not exist", part.ServerID)
			}
			normalized = append(normalized, model.ProxyPathNamePart{Kind: model.ProxyPathNameServer, ServerID: part.ServerID})
		case model.ProxyPathNameExternalOutbound:
			if part.ExternalOutboundID == 0 || !allowedExternals[part.ExternalOutboundID] {
				return fmt.Errorf("external outbound %d is not part of this proxy path", part.ExternalOutboundID)
			}
			if _, ok := externalByID[part.ExternalOutboundID]; !ok {
				return fmt.Errorf("external outbound %d does not exist", part.ExternalOutboundID)
			}
			normalized = append(normalized, model.ProxyPathNamePart{Kind: model.ProxyPathNameExternalOutbound, ExternalOutboundID: part.ExternalOutboundID})
		default:
			return fmt.Errorf("unsupported name_template kind %q", part.Kind)
		}
	}
	if len(normalized) == 0 {
		return errors.New("name_template must resolve to a non-empty name")
	}
	name, err := renderProxyPathNameTemplate(normalized, serverByID, externalByID)
	if err != nil || strings.TrimSpace(name) == "" {
		return errors.New("name_template must resolve to a non-empty name")
	}
	path.NameTemplate = normalized
	return nil
}

func ProxyPathNameTemplateIsValid(path model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, externals []model.ExternalOutbound) bool {
	return NormalizeProxyPathName(&path, steps, servers, inbounds, externals) == nil
}

func proxyPathNameStepsByPath(steps []model.ProxyPathStep) map[int64][]model.ProxyPathStep {
	out := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		out[step.PathID] = append(out[step.PathID], step)
	}
	for pathID := range out {
		sort.SliceStable(out[pathID], func(i, j int) bool {
			if out[pathID][i].Position == out[pathID][j].Position {
				return out[pathID][i].ID < out[pathID][j].ID
			}
			return out[pathID][i].Position < out[pathID][j].Position
		})
	}
	return out
}

func proxyPathRouteLabels(path model.ProxyPath, steps []model.ProxyPathStep, servers map[int64]model.Server, inbounds map[int64]model.Inbound, externals map[int64]model.ExternalOutbound) []string {
	root := inbounds[path.InboundID]
	labels := []string{proxyPathServerLabel(servers[root.ServerID], root.ServerID)}
	for _, step := range steps {
		if step.NodeType == model.ProxyPathStepWARP {
			labels = append(labels, "WARP")
			continue
		}
		if step.NodeType == model.ProxyPathStepImported && step.ExternalOutboundID != nil {
			external := externals[*step.ExternalOutboundID]
			labels = append(labels, firstNonEmpty(strings.TrimSpace(external.Name), fmt.Sprintf("导入节点 #%d", *step.ExternalOutboundID)))
			continue
		}
		serverID := int64(0)
		if step.ServerID != nil {
			serverID = *step.ServerID
		} else if step.InboundID != nil {
			serverID = inbounds[*step.InboundID].ServerID
		}
		if serverID != 0 {
			labels = append(labels, proxyPathServerLabel(servers[serverID], serverID))
		}
	}
	if path.Kind == model.ProxyPathKindDirect {
		labels = append(labels, "直出")
	}
	return labels
}

func proxyPathIsActive(path model.ProxyPath, steps []model.ProxyPathStep) bool {
	return path.Enabled && (path.Kind == model.ProxyPathKindDirect || len(steps) > 0)
}

func proxyPathServerLabel(server model.Server, id int64) string {
	return firstNonEmpty(strings.TrimSpace(server.Name), fmt.Sprintf("服务器 #%d", id))
}

func automaticProxyPathName(route []string, middleDepth int) string {
	if len(route) == 0 {
		return "代理链路"
	}
	if len(route) == 1 {
		return route[0]
	}
	middle := route[1 : len(route)-1]
	if middleDepth > len(middle) {
		middleDepth = len(middle)
	}
	parts := []string{route[0]}
	parts = append(parts, middle[len(middle)-middleDepth:]...)
	parts = append(parts, route[len(route)-1])
	return strings.Join(parts, proxyPathNameSeparator)
}

func recomputeProxyPathNames(states []proxyPathNameState) {
	for index := range states {
		state := &states[index]
		name := state.base
		if state.path.NameMode != model.ProxyPathNameCustom {
			name = automaticProxyPathName(state.route, state.middleDepth)
		}
		if state.featureDepth > 0 {
			name += proxyPathNameSeparator + strings.Join(state.features[:state.featureDepth], proxyPathNameSeparator)
		}
		state.path.Name = strings.TrimSpace(name)
		if state.path.Name == "" {
			state.path.Name = "代理链路"
		}
	}
}

func proxyPathNameConflicts(states []proxyPathNameState, reserved map[string]bool) map[int64]bool {
	counts := map[string]int{}
	for _, state := range states {
		if state.active {
			counts[state.path.Name]++
		}
	}
	out := map[int64]bool{}
	for _, state := range states {
		if state.active && (counts[state.path.Name] > 1 || reserved[state.path.Name]) {
			out[state.path.ID] = true
		}
	}
	return out
}

func proxyPathNameFeatures(path model.ProxyPath, steps []model.ProxyPathStep, inbounds map[int64]model.Inbound, externals map[int64]model.ExternalOutbound) []string {
	features := []string{proxyProtocolName(inbounds[path.InboundID].Protocol)}
	for _, step := range steps {
		feature := ""
		if step.NodeType == model.ProxyPathStepWARP {
			feature = "WARP"
		} else if step.NodeType == model.ProxyPathStepImported && step.ExternalOutboundID != nil {
			feature = proxyProtocolName(externals[*step.ExternalOutboundID].Protocol)
		} else {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			switch mode {
			case model.ProxyPathTransportPortForward:
				switch transparentForwardProtocol(inbounds[path.InboundID]) {
				case model.ForwardProtocolUDP:
					feature = "UDP转发"
				case model.ForwardProtocolTCPUDP:
					feature = "TCP+UDP转发"
				default:
					feature = "TCP转发"
				}
			case model.ProxyPathTransportTunnel:
				tunnelType := strings.ToLower(stringValue(parseStepConfig(step.ConfigJSON), "type", "ssh"))
				if tunnelType == "wireguard" || tunnelType == "wg" {
					feature = "WireGuard"
				} else {
					feature = "SSH"
				}
			default:
				if step.InboundID != nil && *step.InboundID != 0 {
					feature = proxyProtocolName(inbounds[*step.InboundID].Protocol)
				} else {
					feature = proxyPathChainMethodName(proxyPathStepChainMethod(step))
				}
			}
		}
		if feature != "" && feature != features[len(features)-1] {
			features = append(features, feature)
		}
	}
	return features
}

func proxyProtocolName(protocol model.Protocol) string {
	switch protocol {
	case model.ProtocolVLESS:
		return "VLESS"
	case model.ProtocolHY2:
		return "HY2"
	case model.ProtocolAnyTLS:
		return "AnyTLS"
	case model.ProtocolSS:
		return "SS"
	case model.ProtocolSocks:
		return "SOCKS"
	case model.ProtocolSSH:
		return "SSH"
	default:
		return strings.ToUpper(string(protocol))
	}
}

func proxyPathChainMethodName(method string) string {
	switch normalizeProxyPathChainMethod(method) {
	case "2022-blake3-aes-256-gcm":
		return "SS2022-256"
	case "2022-blake3-chacha20-poly1305":
		return "SS2022-ChaCha20"
	default:
		return "SS2022-128"
	}
}

func proxyPathNameReferences(path model.ProxyPath, steps []model.ProxyPathStep, inbounds map[int64]model.Inbound) (map[int64]bool, map[int64]bool) {
	servers := map[int64]bool{}
	externals := map[int64]bool{}
	if root, ok := inbounds[path.InboundID]; ok {
		servers[root.ServerID] = true
	}
	for _, step := range steps {
		if step.PathID != path.ID {
			continue
		}
		if step.ServerID != nil && *step.ServerID != 0 {
			servers[*step.ServerID] = true
		}
		if step.InboundID != nil && *step.InboundID != 0 {
			servers[inbounds[*step.InboundID].ServerID] = true
		}
		if step.ExternalOutboundID != nil && *step.ExternalOutboundID != 0 {
			externals[*step.ExternalOutboundID] = true
		}
	}
	return servers, externals
}

func renderProxyPathNameTemplate(parts []model.ProxyPathNamePart, servers map[int64]model.Server, externals map[int64]model.ExternalOutbound) (string, error) {
	var builder strings.Builder
	for _, part := range parts {
		switch part.Kind {
		case model.ProxyPathNameLiteral:
			builder.WriteString(part.Value)
		case model.ProxyPathNameServer:
			server, ok := servers[part.ServerID]
			if !ok {
				return "", fmt.Errorf("server %d does not exist", part.ServerID)
			}
			builder.WriteString(proxyPathServerLabel(server, part.ServerID))
		case model.ProxyPathNameExternalOutbound:
			external, ok := externals[part.ExternalOutboundID]
			if !ok {
				return "", fmt.Errorf("external outbound %d does not exist", part.ExternalOutboundID)
			}
			builder.WriteString(firstNonEmpty(strings.TrimSpace(external.Name), fmt.Sprintf("导入节点 #%d", part.ExternalOutboundID)))
		default:
			return "", fmt.Errorf("unsupported name_template kind %q", part.Kind)
		}
	}
	return builder.String(), nil
}
