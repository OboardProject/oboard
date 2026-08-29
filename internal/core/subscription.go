package core

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type SubscriptionOptions struct {
	Format                 model.SubscriptionFormat
	ProxyPaths             []model.ProxyPath
	ProxyPathSteps         []model.ProxyPathStep
	RoutingRules           []model.RoutingRule
	ProxyPathEgressResults []model.ProxyPathEgressResult
	ExternalOutbounds      []model.ExternalOutbound
	SSHServerHostKeys      map[int64]string
	EffectiveNodes         map[string]bool
	// EffectiveNodeGroups maps node key to the display group from the granting
	// plan node. Nodes without an explicit group keep the default group.
	EffectiveNodeGroups map[string]string
	// NodeOrderPolicy is the active revision's ordering policy. An empty
	// policy falls back to the legacy group/name ordering so users without a
	// plan binding never see an ordering change after an upgrade.
	NodeOrderPolicy model.SubscriptionNodeOrderPolicy
	// NodeOrderPositions maps node key to the revision's manual sort position
	// (0-based, present only in manual mode).
	NodeOrderPositions map[string]int
	GlobalNodeNames    map[string]*string
	PlanNodeNames      map[string]*string
	Render             SubscriptionRenderOptions
	// AlwaysUseDomainHost forces the subscription server/host field to the
	// managed DNS domain even for static single-stack inbounds.
	AlwaysUseDomainHost bool
}

type SubscriptionNode struct {
	Key      string                   `json:"key"`
	NodeType model.AssignableNodeType `json:"node_type"`
	NodeID   int64                    `json:"node_id"`

	Name                string  `json:"name"`
	Group               string  `json:"group"`
	SourceName          string  `json:"source_name,omitempty"`
	GlobalName          string  `json:"global_name,omitempty"`
	PlanNameOverride    *string `json:"plan_name_override,omitempty"`
	HasPlanNameOverride bool    `json:"has_plan_name_override"`

	EntryKey       string `json:"entry_key"`
	EntryInboundID int64  `json:"entry_inbound_id"`
	EntryServerID  int64  `json:"entry_server_id"`
	EntryRegion    string `json:"entry_region"`

	ExitServerID           int64  `json:"exit_server_id"`
	ExitExternalOutboundID int64  `json:"exit_external_outbound_id"`
	ExitRegion             string `json:"exit_region"`

	ManualPosition *int           `json:"manual_position,omitempty"`
	ServerID       int64          `json:"server_id"`
	Inbound        model.Inbound  `json:"-"`
	Server         model.Server   `json:"-"`
	Raw            map[string]any `json:"raw"`
}

type subscriptionNodeNameKind uint8

const (
	subscriptionNodeNameProxyPath subscriptionNodeNameKind = iota
	subscriptionNodeNameExternal
	subscriptionNodeNameStandalone
)

type subscriptionNodeNameRef struct {
	index      int
	kind       subscriptionNodeNameKind
	resourceID int64
	serverID   int64
	regionCode string
}

func GenerateSubscriptionWithOptions(user model.User, servers []model.Server, inbounds []model.Inbound, opts SubscriptionOptions) (string, error) {
	format := normalizeSubscriptionFormat(opts.Format)
	nodes, err := BuildSubscriptionNodes(user, servers, inbounds, opts)
	if err != nil {
		return "", err
	}
	return renderSubscriptionTargetWithOptions(nodes, format, opts.Render)
}

func BuildSubscriptionNodes(user model.User, servers []model.Server, inbounds []model.Inbound, opts SubscriptionOptions) ([]SubscriptionNode, error) {
	topologies, resolvedPaths, resolvedExternals, err := ResolveAssignableNodeTopologies(AssignableNodeCatalogInput{
		Servers:           servers,
		Inbounds:          inbounds,
		ProxyPaths:        opts.ProxyPaths,
		ProxyPathSteps:    opts.ProxyPathSteps,
		EgressResults:     opts.ProxyPathEgressResults,
		ExternalOutbounds: opts.ExternalOutbounds,
	})
	if err != nil {
		return nil, err
	}
	opts.ProxyPaths = resolvedPaths
	opts.ExternalOutbounds = resolvedExternals
	serverByID := map[int64]model.Server{}
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	defaultGroup := "default"
	hiddenFamilyTargets := subscriptionHiddenFamilyTargets(opts)
	nodes := []SubscriptionNode{}
	nameRefs := []subscriptionNodeNameRef{}
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		configuredBranches := subscriptionBranchesForInbound(inbound, opts.ProxyPaths, opts.ProxyPathSteps)
		authorizedBranches := configuredBranches[:0]
		for _, path := range configuredBranches {
			if opts.EffectiveNodes[NodeKeyOf(model.AssignableNodeProxyPath, path.ID)] && !hiddenFamilyTargets[path.ID] {
				authorizedBranches = append(authorizedBranches, path)
			}
		}
		inboundAllowed := opts.EffectiveNodes[NodeKeyOf(model.AssignableNodeInbound, inbound.ID)]
		if !inboundAllowed && len(authorizedBranches) == 0 {
			continue
		}
		server, ok := serverByID[inbound.ServerID]
		if !ok {
			continue
		}
		server.EntryAddress = ResolveEntryAddressHost(inbound, server, opts.AlwaysUseDomainHost)
		if strings.TrimSpace(server.EntryAddress) == "" {
			continue
		}
		standaloneName := proxyPathServerLabel(server, server.ID)
		if inbound.Protocol == model.ProtocolSSH {
			hostKey := strings.TrimSpace(opts.SSHServerHostKeys[server.ID])
			if strings.TrimSpace(user.ProxyPassword) == "" || strings.TrimSpace(user.SSHRandomID) == "" || hostKey == "" {
				continue
			}
			if len(authorizedBranches) == 0 {
				// A branchless SSH inbound renders one implicit direct-exit
				// route authorized by the inbound grant itself.
				if len(configuredBranches) > 0 || !inboundAllowed {
					continue
				}
				pathID := SSHDirectBranchPathID(inbound.ID)
				credentialUser := UserCredentialForRoute(user, inbound.ID, pathID, model.ProtocolSSH)
				group := nodeGroupFor(opts.EffectiveNodeGroups, NodeKeyOf(model.AssignableNodeInbound, inbound.ID), defaultGroup)
				raw := map[string]any{
					"type":         "ssh",
					"server":       server.EntryAddress,
					"server_port":  InboundSubscriptionPort(inbound),
					"username":     fmt.Sprintf("u%s-p%d", user.SSHRandomID, pathID),
					"password":     credentialUser.ProxyPassword,
					"host_key":     []string{hostKey},
					"oboard_group": group,
				}
				topo := topologies[NodeKeyOf(model.AssignableNodeInbound, inbound.ID)]
				nodes = append(nodes, newSubscriptionNode(topo, model.AssignableNodeInbound, inbound.ID, standaloneName, group, opts.NodeOrderPositions, inbound, server, raw))
				regionCode, _ := EffectiveServerRegion(server)
				nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameStandalone, resourceID: inbound.ID, serverID: server.ID, regionCode: regionCode})
				continue
			}
			for _, path := range authorizedBranches {
				credentialUser := UserCredentialForRoute(user, inbound.ID, path.ID, model.ProtocolSSH)
				branchName := strings.TrimSpace(path.Name)
				if branchName == "" {
					branchName = fmt.Sprintf("%s 分支 %d", standaloneName, path.ID)
				}
				group := nodeGroupFor(opts.EffectiveNodeGroups, NodeKeyOf(model.AssignableNodeProxyPath, path.ID), defaultGroup)
				raw := map[string]any{
					"type":         "ssh",
					"server":       server.EntryAddress,
					"server_port":  InboundSubscriptionPort(inbound),
					"username":     fmt.Sprintf("u%s-p%d", user.SSHRandomID, path.ID),
					"password":     credentialUser.ProxyPassword,
					"host_key":     []string{hostKey},
					"oboard_group": group,
				}
				topo := topologies[NodeKeyOf(model.AssignableNodeProxyPath, path.ID)]
				nodes = append(nodes, newSubscriptionNode(topo, model.AssignableNodeProxyPath, path.ID, branchName, group, opts.NodeOrderPositions, inbound, server, raw))
				nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameProxyPath, resourceID: path.ID, serverID: server.ID, regionCode: path.EffectiveExitRegionCode})
			}
			continue
		}
		adapter, err := AdapterFor(inbound.Protocol)
		if err != nil {
			return nil, err
		}
		branches := authorizedBranches
		if len(branches) == 0 {
			if len(configuredBranches) > 0 {
				continue
			}
			group := nodeGroupFor(opts.EffectiveNodeGroups, NodeKeyOf(model.AssignableNodeInbound, inbound.ID), defaultGroup)
			raw, err := adapter.SubscriptionNode(user, inbound, server)
			if err != nil {
				return nil, err
			}
			raw["oboard_group"] = group
			topo := topologies[NodeKeyOf(model.AssignableNodeInbound, inbound.ID)]
			nodes = append(nodes, newSubscriptionNode(topo, model.AssignableNodeInbound, inbound.ID, standaloneName, group, opts.NodeOrderPositions, inbound, server, raw))
			regionCode, _ := EffectiveServerRegion(server)
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameStandalone, resourceID: inbound.ID, serverID: server.ID, regionCode: regionCode})
			continue
		}
		for _, path := range branches {
			branchUser := proxyPathBranchUser(path, inbound, user)
			raw, err := adapter.SubscriptionNode(branchUser, inbound, server)
			if err != nil {
				return nil, err
			}
			branchName := strings.TrimSpace(path.Name)
			if branchName == "" {
				branchName = fmt.Sprintf("%s 分支 %d", standaloneName, path.ID)
			}
			group := nodeGroupFor(opts.EffectiveNodeGroups, NodeKeyOf(model.AssignableNodeProxyPath, path.ID), defaultGroup)
			raw["oboard_group"] = group
			topo := topologies[NodeKeyOf(model.AssignableNodeProxyPath, path.ID)]
			nodes = append(nodes, newSubscriptionNode(topo, model.AssignableNodeProxyPath, path.ID, branchName, group, opts.NodeOrderPositions, inbound, server, raw))
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameProxyPath, resourceID: path.ID, serverID: server.ID, regionCode: path.EffectiveExitRegionCode})
		}
	}
	for _, external := range opts.ExternalOutbounds {
		if !external.Enabled || !external.ExposeToUsers {
			continue
		}
		if !opts.EffectiveNodes[NodeKeyOf(model.AssignableNodeExternalOutbound, external.ID)] {
			continue
		}
		raw, err := externalOutboundSubscriptionRaw(external)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(external.Name)
		if name == "" {
			name = fmt.Sprintf("%s-%d", external.Protocol, external.ID)
		}
		group := nodeGroupFor(opts.EffectiveNodeGroups, NodeKeyOf(model.AssignableNodeExternalOutbound, external.ID), defaultGroup)
		raw["oboard_group"] = group
		topo := topologies[NodeKeyOf(model.AssignableNodeExternalOutbound, external.ID)]
		nodes = append(nodes, newSubscriptionNode(topo, model.AssignableNodeExternalOutbound, external.ID, name, group, opts.NodeOrderPositions, model.Inbound{}, model.Server{}, raw))
		nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameExternal, resourceID: external.ID, regionCode: external.EffectiveRegionCode})
	}
	resolveSubscriptionNodeNames(nodes, nameRefs)
	for i := range nodes {
		nodes[i].SourceName = nodes[i].Name
		nodes[i].GlobalName = ResolveEffectiveNodeName(nodes[i].SourceName, opts.GlobalNodeNames[nodes[i].Key], nil)
		if value, ok := opts.PlanNodeNames[nodes[i].Key]; ok && value != nil {
			nodes[i].PlanNameOverride = value
			nodes[i].HasPlanNameOverride = true
		}
		nodes[i].Name = ResolveEffectiveNodeName(nodes[i].SourceName, opts.GlobalNodeNames[nodes[i].Key], opts.PlanNodeNames[nodes[i].Key])
	}
	disambiguateEffectiveSubscriptionNodeNames(nodes)
	for _, ref := range nameRefs {
		nodes[ref.index].Name = RegionFlagEmoji(ref.regionCode) + " " + nodes[ref.index].Name
		nodes[ref.index].Raw["tag"] = nodes[ref.index].Name
	}
	policy := opts.NodeOrderPolicy
	if strings.TrimSpace(string(policy.Mode)) == "" {
		policy = model.DefaultSubscriptionNodeOrderPolicy()
	}
	return OrderSubscriptionNodes(nodes, policy), nil
}

func subscriptionHiddenFamilyTargets(opts SubscriptionOptions) map[int64]bool {
	familySources := map[int64]bool{}
	for _, rule := range opts.RoutingRules {
		if !rule.Enabled || rule.Action != model.RouteActionFamilySplit || rule.ProxyPathID == nil {
			continue
		}
		if opts.EffectiveNodes[NodeKeyOf(model.AssignableNodeProxyPath, *rule.ProxyPathID)] {
			familySources[*rule.ProxyPathID] = true
		}
	}
	hidden := map[int64]bool{}
	for _, rule := range opts.RoutingRules {
		if !rule.Enabled || rule.Action != model.RouteActionFamilySplit || rule.ProxyPathID == nil || !familySources[*rule.ProxyPathID] {
			continue
		}
		if rule.FamilySplitTemplateID == nil {
			continue
		}
		for _, path := range opts.ProxyPaths {
			if IsFamilyBranch(path) && path.TemplateID != nil && *path.TemplateID == *rule.FamilySplitTemplateID && !familySources[path.ID] {
				hidden[path.ID] = true
			}
		}
	}
	return hidden
}

func disambiguateEffectiveSubscriptionNodeNames(nodes []SubscriptionNode) {
	counts := map[string]int{}
	for _, node := range nodes {
		counts[node.Name]++
	}
	used := map[string]bool{}
	for _, node := range nodes {
		if counts[node.Name] == 1 {
			used[node.Name] = true
		}
	}
	indexes := make([]int, 0, len(nodes))
	for i := range nodes {
		if counts[nodes[i].Name] > 1 {
			indexes = append(indexes, i)
		}
	}
	sort.Slice(indexes, func(i, j int) bool { return nodes[indexes[i]].Key < nodes[indexes[j]].Key })
	next := map[string]int{}
	for _, index := range indexes {
		base := nodes[index].Name
		for {
			next[base]++
			candidate := fmt.Sprintf("%s%s%02d", base, proxyPathNameSeparator, next[base])
			if used[candidate] {
				continue
			}
			nodes[index].Name = candidate
			used[candidate] = true
			break
		}
	}
}

func nodeGroupFor(groups map[string]string, key, fallback string) string {
	if groups != nil {
		if g := strings.TrimSpace(groups[key]); g != "" {
			return g
		}
	}
	return fallback
}

func newSubscriptionNode(topo AssignableNodeTopology, nodeType model.AssignableNodeType, nodeID int64, name, group string, positions map[string]int, inbound model.Inbound, server model.Server, raw map[string]any) SubscriptionNode {
	node := SubscriptionNode{
		Key:                    NodeKeyOf(nodeType, nodeID),
		NodeType:               nodeType,
		NodeID:                 nodeID,
		Name:                   name,
		Group:                  group,
		EntryKey:               topo.EntryKey,
		EntryInboundID:         topo.EntryInboundID,
		EntryServerID:          topo.EntryServerID,
		EntryRegion:            topo.EntryRegion,
		ExitServerID:           topo.ExitServerID,
		ExitExternalOutboundID: topo.ExitExternalOutboundID,
		ExitRegion:             topo.ExitRegion,
		ServerID:               topo.EntryServerID,
		Inbound:                inbound,
		Server:                 server,
		Raw:                    raw,
	}
	if position, ok := positions[node.Key]; ok && position >= 0 {
		position := position
		node.ManualPosition = &position
	}
	return node
}

func resolveSubscriptionNodeNames(nodes []SubscriptionNode, refs []subscriptionNodeNameRef) {
	serverNames := map[int64]string{}
	serverProtocols := map[int64]map[model.Protocol]bool{}
	for _, ref := range refs {
		if ref.kind != subscriptionNodeNameStandalone {
			continue
		}
		node := nodes[ref.index]
		serverNames[ref.serverID] = proxyPathServerLabel(node.Server, ref.serverID)
		if serverProtocols[ref.serverID] == nil {
			serverProtocols[ref.serverID] = map[model.Protocol]bool{}
		}
		serverProtocols[ref.serverID][node.Inbound.Protocol] = true
	}

	serversByName := map[string][]int64{}
	for serverID, name := range serverNames {
		serversByName[name] = append(serversByName[name], serverID)
	}
	usedServerNames := map[string]bool{}
	duplicateServerNames := []string{}
	for name, serverIDs := range serversByName {
		if len(serverIDs) == 1 {
			usedServerNames[name] = true
			continue
		}
		duplicateServerNames = append(duplicateServerNames, name)
	}
	sort.Strings(duplicateServerNames)
	for _, name := range duplicateServerNames {
		serverIDs := serversByName[name]
		sort.Slice(serverIDs, func(i, j int) bool { return serverIDs[i] < serverIDs[j] })
		ordinal := 1
		for _, serverID := range serverIDs {
			for {
				candidate := fmt.Sprintf("%s%s%02d", name, proxyPathNameSeparator, ordinal)
				ordinal++
				if usedServerNames[candidate] {
					continue
				}
				serverNames[serverID] = candidate
				usedServerNames[candidate] = true
				break
			}
		}
	}

	for _, ref := range refs {
		if ref.kind != subscriptionNodeNameStandalone {
			continue
		}
		name := serverNames[ref.serverID]
		if len(serverProtocols[ref.serverID]) > 1 {
			name += proxyPathNameSeparator + proxyProtocolName(nodes[ref.index].Inbound.Protocol)
		}
		nodes[ref.index].Name = name
	}

	disambiguateSubscriptionNodeNames(nodes, refs, subscriptionNodeNameExternal)
	disambiguateSubscriptionNodeNames(nodes, refs, subscriptionNodeNameStandalone)
	for _, ref := range refs {
		nodes[ref.index].Raw["tag"] = nodes[ref.index].Name
	}
}

func disambiguateSubscriptionNodeNames(nodes []SubscriptionNode, refs []subscriptionNodeNameRef, target subscriptionNodeNameKind) {
	counts := map[string]int{}
	reserved := map[string]bool{}
	for _, ref := range refs {
		name := nodes[ref.index].Name
		if ref.kind < target {
			reserved[name] = true
		}
		if ref.kind == target {
			counts[name]++
		}
	}

	used := map[string]bool{}
	for name := range reserved {
		used[name] = true
	}
	conflicts := []subscriptionNodeNameRef{}
	bases := map[int]string{}
	for _, ref := range refs {
		if ref.kind != target {
			continue
		}
		name := nodes[ref.index].Name
		if counts[name] > 1 || reserved[name] {
			conflicts = append(conflicts, ref)
			bases[ref.index] = name
			continue
		}
		used[name] = true
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if target == subscriptionNodeNameStandalone && conflicts[i].serverID != conflicts[j].serverID {
			return conflicts[i].serverID < conflicts[j].serverID
		}
		return conflicts[i].resourceID < conflicts[j].resourceID
	})
	nextOrdinal := map[string]int{}
	for _, ref := range conflicts {
		base := bases[ref.index]
		for {
			nextOrdinal[base]++
			candidate := fmt.Sprintf("%s%s%02d", base, proxyPathNameSeparator, nextOrdinal[base])
			if used[candidate] {
				continue
			}
			nodes[ref.index].Name = candidate
			used[candidate] = true
			break
		}
	}
}

// SSHDirectBranchPathID is the virtual branch id for a branchless SSH
// inbound: the inbound renders one implicit direct-exit route whose login
// name and kernel auth identity encode this id instead of a proxy path id.
// It is only used while the inbound has no real proxy-path branches.
func SSHDirectBranchPathID(inboundID int64) int64 { return inboundID }

// SSHDirectBranchIdentity returns the kernel route identity for the implicit
// direct branch of a branchless SSH inbound. It mirrors the identity a real
// direct proxy path would receive so the Agent validates both identically.
func SSHDirectBranchIdentity(inboundID int64, username string) (routeInboundTag, routeAuthUser string, pathID int64) {
	pathID = SSHDirectBranchPathID(inboundID)
	return fmt.Sprintf("in-%d", inboundID), fmt.Sprintf("%s__oboard_path_%d", username, pathID), pathID
}

func subscriptionBranchesForInbound(inbound model.Inbound, paths []model.ProxyPath, steps []model.ProxyPathStep) []model.ProxyPath {
	if len(paths) == 0 {
		return nil
	}
	hasStep := map[int64]bool{}
	for _, step := range steps {
		hasStep[step.PathID] = true
	}
	out := []model.ProxyPath{}
	for _, path := range paths {
		if path.Enabled && path.InboundID == inbound.ID && (path.Kind == model.ProxyPathKindDirect || hasStep[path.ID]) {
			out = append(out, path)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func externalOutboundSubscriptionRaw(external model.ExternalOutbound) (map[string]any, error) {
	if strings.TrimSpace(external.ConfigJSON) != "" && strings.TrimSpace(external.ConfigJSON) != "{}" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(external.ConfigJSON), &raw); err == nil && raw["type"] != nil {
			delete(raw, "_oboard")
			if external.TargetAddress != "" {
				raw["server"] = external.TargetAddress
			}
			if external.TargetPort > 0 {
				raw["server_port"] = external.TargetPort
			}
			return raw, nil
		}
	}
	switch external.Protocol {
	case model.ProtocolSocks:
		raw := map[string]any{"type": "socks", "server": external.TargetAddress, "server_port": external.TargetPort}
		var extra map[string]any
		_ = json.Unmarshal([]byte(external.ConfigJSON), &extra)
		for _, key := range []string{"version", "username", "password", "network", "udp_over_tcp", "tcp_fast_open"} {
			if extra != nil && extra[key] != nil {
				raw[key] = extra[key]
			}
		}
		return raw, nil
	case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolMieru, model.ProtocolSnell:
		var raw map[string]any
		if err := json.Unmarshal([]byte(external.ConfigJSON), &raw); err != nil {
			raw = map[string]any{}
		}
		if external.Protocol == model.ProtocolHY2 {
			raw["type"] = "hysteria2"
		} else {
			raw["type"] = string(external.Protocol)
		}
		raw["server"] = external.TargetAddress
		raw["server_port"] = external.TargetPort
		delete(raw, "_oboard")
		return raw, nil
	default:
		return nil, fmt.Errorf("unsupported imported node protocol %q", external.Protocol)
	}
}

func renderSingBoxSubscription(nodes []SubscriptionNode) (string, error) {
	return renderSubscriptionTarget(nodes, model.SubscriptionFormatSingBox)
}

func querySuffix(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func udpOverTCPEnabled(value any) bool {
	if enabled, ok := value.(bool); ok {
		return enabled
	}
	if options, ok := value.(map[string]any); ok {
		return boolValue(options["enabled"])
	}
	return false
}

func normalizeSubscriptionFormat(format model.SubscriptionFormat) model.SubscriptionFormat {
	switch strings.ToLower(strings.TrimSpace(string(format))) {
	case "":
		return model.SubscriptionFormatMihomo
	case "singbox", "sing-box":
		return model.SubscriptionFormatSingBox
	case "auto":
		return model.SubscriptionFormatAuto
	case "stash":
		return model.SubscriptionFormatStash
	case "mihomo":
		return model.SubscriptionFormatMihomo
	case "surfboard":
		return model.SubscriptionFormatSurfboard
	case "surge":
		return model.SubscriptionFormatSurge
	case "surgemac", "surge-mac", "surge_mac":
		return model.SubscriptionFormatSurgeMac
	case "loon":
		return model.SubscriptionFormatLoon
	case "egern":
		return model.SubscriptionFormatEgern
	case "shadowrocket":
		return model.SubscriptionFormatShadowrocket
	case "qx", "quantumultx", "quantumult-x", "quantumult x":
		return model.SubscriptionFormatQX
	case "v2ray":
		return model.SubscriptionFormatV2Ray
	case "v2ray-uri", "v2rayuri", "v2ray uri":
		return model.SubscriptionFormatV2RayURI
	default:
		return model.SubscriptionFormat(strings.ToLower(strings.TrimSpace(string(format))))
	}
}

func NormalizeSubscriptionFormatForAPI(format model.SubscriptionFormat) model.SubscriptionFormat {
	return normalizeSubscriptionFormat(format)
}

func ConcreteSubscriptionFormats() []model.SubscriptionFormat {
	return []model.SubscriptionFormat{
		model.SubscriptionFormatMihomo,
		model.SubscriptionFormatStash,
		model.SubscriptionFormatSurfboard,
		model.SubscriptionFormatSurge,
		model.SubscriptionFormatSurgeMac,
		model.SubscriptionFormatLoon,
		model.SubscriptionFormatEgern,
		model.SubscriptionFormatShadowrocket,
		model.SubscriptionFormatQX,
		model.SubscriptionFormatSingBox,
		model.SubscriptionFormatV2Ray,
		model.SubscriptionFormatV2RayURI,
	}
}

func SupportedSubscriptionFormats() []model.SubscriptionFormat {
	return ConcreteSubscriptionFormats()
}

func IsConcreteSubscriptionFormat(format model.SubscriptionFormat) bool {
	normalized := normalizeSubscriptionFormat(format)
	if normalized == model.SubscriptionFormatAuto {
		return false
	}
	for _, supported := range ConcreteSubscriptionFormats() {
		if normalized == supported {
			return true
		}
	}
	return false
}

func IsSupportedSubscriptionFormat(format model.SubscriptionFormat) bool {
	normalized := normalizeSubscriptionFormat(format)
	if normalized == model.SubscriptionFormatAuto {
		return true
	}
	return IsConcreteSubscriptionFormat(normalized)
}

func SubscriptionContentType(format model.SubscriptionFormat) string {
	switch normalizeSubscriptionFormat(format) {
	case model.SubscriptionFormatSingBox:
		return "application/json"
	case model.SubscriptionFormatMihomo, model.SubscriptionFormatStash, model.SubscriptionFormatEgern:
		return "text/yaml; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

func SubscriptionFormatLabel(format model.SubscriptionFormat) string {
	switch normalizeSubscriptionFormat(format) {
	case model.SubscriptionFormatAuto:
		return "自动识别"
	case model.SubscriptionFormatMihomo:
		return "Mihomo"
	case model.SubscriptionFormatStash:
		return "Stash"
	case model.SubscriptionFormatSurfboard:
		return "Surfboard"
	case model.SubscriptionFormatSurge:
		return "Surge"
	case model.SubscriptionFormatSurgeMac:
		return "Surge Mac"
	case model.SubscriptionFormatLoon:
		return "Loon"
	case model.SubscriptionFormatEgern:
		return "Egern"
	case model.SubscriptionFormatShadowrocket:
		return "Shadowrocket"
	case model.SubscriptionFormatQX:
		return "Quantumult X"
	case model.SubscriptionFormatSingBox:
		return "sing-box"
	case model.SubscriptionFormatV2Ray:
		return "V2Ray"
	case model.SubscriptionFormatV2RayURI:
		return "V2Ray URI"
	default:
		return string(format)
	}
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
