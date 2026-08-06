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
	Format  model.SubscriptionFormat
	Profile *model.SubscriptionProfile
	// RequireAssignments forces deny-by-default assignment filtering even when
	// Assignments is empty. Used when a concrete profile_id is requested so an
	// empty profile cannot fall back to every accessible inbound.
	RequireAssignments           bool
	Assignments                  []model.SubscriptionAssignment
	InboundUsers                 []model.InboundUser
	ProxyPathUsers               []model.ProxyPathUser
	ProxyPaths                   []model.ProxyPath
	ProxyPathSteps               []model.ProxyPathStep
	ProxyPathEgressResults       []model.ProxyPathEgressResult
	ExternalOutbounds            []model.ExternalOutbound
	ExternalOutboundAccessGrants []model.ExternalOutboundAccessGrant
	UserGroups                   []model.UserGroup
	UserGroupMembers             []model.UserGroupMember
	SSHServerHostKeys            map[int64]string
}

type SubscriptionNode struct {
	Name     string         `json:"name"`
	Group    string         `json:"group"`
	ServerID int64          `json:"server_id"`
	Inbound  model.Inbound  `json:"-"`
	Server   model.Server   `json:"-"`
	Raw      map[string]any `json:"raw"`
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
	return renderSubscriptionTarget(nodes, format)
}

func BuildSubscriptionNodes(user model.User, servers []model.Server, inbounds []model.Inbound, opts SubscriptionOptions) ([]SubscriptionNode, error) {
	opts.ProxyPaths = ResolveProxyPathNames(opts.ProxyPaths, opts.ProxyPathSteps, servers, inbounds, opts.ExternalOutbounds)
	opts.ProxyPaths, opts.ExternalOutbounds = ResolveProxyPathExitRegions(opts.ProxyPaths, opts.ProxyPathSteps, servers, inbounds, opts.ExternalOutbounds, opts.ProxyPathEgressResults)
	serverByID := map[int64]model.Server{}
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	assignmentByInbound := map[int64]model.SubscriptionAssignment{}
	serverAssignment := map[int64]model.SubscriptionAssignment{}
	useAssignments := opts.RequireAssignments
	for _, a := range opts.Assignments {
		if !a.Enabled || a.UserID != user.ID || a.ProfileID == 0 {
			continue
		}
		useAssignments = true
		if a.InboundID != nil {
			assignmentByInbound[*a.InboundID] = a
			continue
		}
		if a.ServerID != nil {
			serverAssignment[*a.ServerID] = a
		}
	}
	defaultGroup := "default"
	if opts.Profile != nil && strings.TrimSpace(opts.Profile.GroupName) != "" {
		defaultGroup = strings.TrimSpace(opts.Profile.GroupName)
	}
	nodes := []SubscriptionNode{}
	nameRefs := []subscriptionNodeNameRef{}
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		configuredBranches := subscriptionBranchesForInbound(inbound, opts.ProxyPaths, opts.ProxyPathSteps, 0, nil)
		authorizedBranches := subscriptionBranchesForInbound(inbound, opts.ProxyPaths, opts.ProxyPathSteps, user.ID, opts.ProxyPathUsers)
		inboundAllowed := opts.InboundUsers == nil || subscriptionInboundAllowed(user.ID, inbound.ID, opts.InboundUsers)
		if opts.ProxyPathUsers == nil && !inboundAllowed {
			authorizedBranches = nil
		}
		if !inboundAllowed && len(authorizedBranches) == 0 {
			continue
		}
		assignment, ok := assignmentByInbound[inbound.ID]
		if !ok {
			assignment, ok = serverAssignment[inbound.ServerID]
		}
		if useAssignments && !ok {
			continue
		}
		server, ok := serverByID[inbound.ServerID]
		if !ok {
			continue
		}
		server.EntryAddress = ResolveEntryAddress(inbound, server)
		if strings.TrimSpace(server.EntryAddress) == "" {
			continue
		}
		standaloneName := proxyPathServerLabel(server, server.ID)
		group := defaultGroup
		if strings.TrimSpace(assignment.GroupName) != "" {
			group = strings.TrimSpace(assignment.GroupName)
		}
		if inbound.Protocol == model.ProtocolSSH {
			hostKey := strings.TrimSpace(opts.SSHServerHostKeys[server.ID])
			if strings.TrimSpace(user.ProxyPassword) == "" || strings.TrimSpace(user.SSHRandomID) == "" || hostKey == "" {
				continue
			}
			for _, path := range authorizedBranches {
				credentialUser := UserCredentialForRoute(user, inbound.ID, path.ID, model.ProtocolSSH)
				branchName := strings.TrimSpace(path.Name)
				if branchName == "" {
					branchName = fmt.Sprintf("%s 分支 %d", standaloneName, path.ID)
				}
				raw := map[string]any{
					"type":         "ssh",
					"server":       server.EntryAddress,
					"server_port":  inbound.Port,
					"username":     fmt.Sprintf("u%s-p%d", user.SSHRandomID, path.ID),
					"password":     credentialUser.ProxyPassword,
					"host_key":     []string{hostKey},
					"oboard_group": group,
				}
				nodes = append(nodes, SubscriptionNode{Name: branchName, Group: group, ServerID: server.ID, Inbound: inbound, Server: server, Raw: raw})
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
			raw, err := adapter.SubscriptionNode(user, inbound, server)
			if err != nil {
				return nil, err
			}
			raw["oboard_group"] = group
			nodes = append(nodes, SubscriptionNode{Name: standaloneName, Group: group, ServerID: server.ID, Inbound: inbound, Server: server, Raw: raw})
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
			raw["oboard_group"] = group
			nodes = append(nodes, SubscriptionNode{Name: branchName, Group: group, ServerID: server.ID, Inbound: inbound, Server: server, Raw: raw})
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameProxyPath, resourceID: path.ID, serverID: server.ID, regionCode: path.EffectiveExitRegionCode})
		}
	}
	for _, external := range opts.ExternalOutbounds {
		if !external.Enabled || !external.ExposeToUsers {
			continue
		}
		if !subscriptionExternalAllowed(user.ID, external.ID, opts.ExternalOutboundAccessGrants, opts.UserGroups, opts.UserGroupMembers) {
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
		group := defaultGroup
		raw["oboard_group"] = group
		nodes = append(nodes, SubscriptionNode{Name: name, Group: group, Raw: raw})
		nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameExternal, resourceID: external.ID, regionCode: external.EffectiveRegionCode})
	}
	resolveSubscriptionNodeNames(nodes, nameRefs)
	for _, ref := range nameRefs {
		nodes[ref.index].Name = RegionFlagEmoji(ref.regionCode) + " " + nodes[ref.index].Name
		nodes[ref.index].Raw["tag"] = nodes[ref.index].Name
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Group == nodes[j].Group {
			return nodes[i].Name < nodes[j].Name
		}
		return nodes[i].Group < nodes[j].Group
	})
	return nodes, nil
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

func subscriptionBranchesForInbound(inbound model.Inbound, paths []model.ProxyPath, steps []model.ProxyPathStep, userID int64, pathUsers []model.ProxyPathUser) []model.ProxyPath {
	if len(paths) == 0 {
		return nil
	}
	hasStep := map[int64]bool{}
	for _, step := range steps {
		hasStep[step.PathID] = true
	}
	out := []model.ProxyPath{}
	for _, path := range paths {
		allowed := userID == 0 || pathUsers == nil || ProxyPathUserAllowed(path.ID, userID, pathUsers)
		if allowed && path.Enabled && path.InboundID == inbound.ID && (path.Kind == model.ProxyPathKindDirect || hasStep[path.ID]) {
			out = append(out, path)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func subscriptionExternalAllowed(userID, externalID int64, grants []model.ExternalOutboundAccessGrant, groups []model.UserGroup, members []model.UserGroupMember) bool {
	activeGroups := map[int64]bool{}
	for _, group := range groups {
		if group.Enabled {
			activeGroups[group.ID] = true
		}
	}
	userGroups := map[int64]bool{}
	for _, member := range members {
		if member.Enabled && member.UserID == userID && activeGroups[member.GroupID] {
			userGroups[member.GroupID] = true
		}
	}
	for _, grant := range grants {
		if !grant.Enabled || grant.ExternalOutboundID != externalID {
			continue
		}
		if grant.SubjectType == model.AccessSubjectUser && grant.SubjectID == userID {
			return true
		}
		if grant.SubjectType == model.AccessSubjectGroup && userGroups[grant.SubjectID] {
			return true
		}
	}
	return false
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
		for _, key := range []string{"version", "username", "password", "network", "udp_over_tcp"} {
			if extra != nil && extra[key] != nil {
				raw[key] = extra[key]
			}
		}
		return raw, nil
	case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolMieru:
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

func subscriptionInboundAllowed(userID, inboundID int64, bindings []model.InboundUser) bool {
	for _, binding := range bindings {
		if binding.Enabled && binding.UserID == userID && binding.InboundID == inboundID {
			return true
		}
	}
	return false
}

func renderSingBoxSubscription(nodes []SubscriptionNode) (string, error) {
	return renderSubscriptionTarget(nodes, model.SubscriptionFormatSingBox)
}

func renderSingBoxMieruSubscription(nodes []SubscriptionNode) (string, error) {
	return renderSubscriptionTarget(nodes, model.SubscriptionFormatSingBoxMieru)
}

func renderMieruSubscription(nodes []SubscriptionNode) (string, error) {
	return renderSubscriptionTarget(nodes, model.SubscriptionFormatMieru)
}

func renderClashMetaSubscription(nodes []SubscriptionNode) (string, error) {
	return renderSubscriptionTarget(nodes, model.SubscriptionFormatClashMeta)
}

func renderV2RaySubscription(nodes []SubscriptionNode) (string, error) {
	return renderSubscriptionTarget(nodes, model.SubscriptionFormatV2Ray)
}

func renderURIListSubscription(nodes []SubscriptionNode) (string, error) {
	return renderSubscriptionTarget(nodes, model.SubscriptionFormatV2RayURI)
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
	case "", "singbox", "sing-box":
		return model.SubscriptionFormatSingBox
	case "sing-box-mieru", "singbox-mieru", "singboxmieru":
		return model.SubscriptionFormatSingBoxMieru
	case "mieru", "mierus":
		return model.SubscriptionFormatMieru
	case "stash":
		return model.SubscriptionFormatStash
	case "clash", "clash-meta", "mihomo":
		if strings.ToLower(strings.TrimSpace(string(format))) == "clash" {
			return model.SubscriptionFormatClash
		}
		if strings.ToLower(strings.TrimSpace(string(format))) == "mihomo" {
			return model.SubscriptionFormatMihomo
		}
		return model.SubscriptionFormatClashMeta
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
		return format
	}
}

func NormalizeSubscriptionFormatForAPI(format model.SubscriptionFormat) model.SubscriptionFormat {
	return normalizeSubscriptionFormat(format)
}

func SupportedSubscriptionFormats() []model.SubscriptionFormat {
	return []model.SubscriptionFormat{
		model.SubscriptionFormatStash,
		model.SubscriptionFormatClashMeta,
		model.SubscriptionFormatMihomo,
		model.SubscriptionFormatSurfboard,
		model.SubscriptionFormatSurge,
		model.SubscriptionFormatSurgeMac,
		model.SubscriptionFormatLoon,
		model.SubscriptionFormatEgern,
		model.SubscriptionFormatShadowrocket,
		model.SubscriptionFormatQX,
		model.SubscriptionFormatSingBox,
		model.SubscriptionFormatSingBoxMieru,
		model.SubscriptionFormatMieru,
		model.SubscriptionFormatV2Ray,
		model.SubscriptionFormatV2RayURI,
		model.SubscriptionFormatClash,
	}
}

func IsSupportedSubscriptionFormat(format model.SubscriptionFormat) bool {
	normalized := normalizeSubscriptionFormat(format)
	for _, supported := range SupportedSubscriptionFormats() {
		if normalized == supported {
			return true
		}
	}
	return false
}

func SubscriptionContentType(format model.SubscriptionFormat) string {
	switch normalizeSubscriptionFormat(format) {
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		return "application/json"
	case model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatStash, model.SubscriptionFormatEgern, model.SubscriptionFormatClash:
		return "text/yaml; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
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
