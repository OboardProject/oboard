package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
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
	ProxyPaths                   []model.ProxyPath
	ProxyPathSteps               []model.ProxyPathStep
	ExternalOutbounds            []model.ExternalOutbound
	ExternalOutboundAccessGrants []model.ExternalOutboundAccessGrant
	UserGroups                   []model.UserGroup
	UserGroupMembers             []model.UserGroupMember
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
}

func GenerateSubscriptionWithOptions(user model.User, servers []model.Server, inbounds []model.Inbound, opts SubscriptionOptions) (string, error) {
	format := normalizeSubscriptionFormat(opts.Format)
	nodes, err := BuildSubscriptionNodes(user, servers, inbounds, opts)
	if err != nil {
		return "", err
	}
	switch format {
	case model.SubscriptionFormatPlainJSON:
		return renderPlainJSONSubscription(nodes)
	case model.SubscriptionFormatSingBox:
		return renderSingBoxSubscription(nodes)
	case model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatStash, model.SubscriptionFormatClash:
		return renderClashMetaSubscription(nodes)
	case model.SubscriptionFormatV2Ray:
		return renderV2RaySubscription(nodes)
	case model.SubscriptionFormatV2RayURI, model.SubscriptionFormatShadowrocket, model.SubscriptionFormatLoon, model.SubscriptionFormatEgern, model.SubscriptionFormatQX, model.SubscriptionFormatSurfboard, model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac:
		return renderURIListSubscription(nodes)
	default:
		return "", fmt.Errorf("unsupported subscription format %q", format)
	}
}

func BuildSubscriptionNodes(user model.User, servers []model.Server, inbounds []model.Inbound, opts SubscriptionOptions) ([]SubscriptionNode, error) {
	opts.ProxyPaths = ResolveProxyPathNames(opts.ProxyPaths, opts.ProxyPathSteps, servers, inbounds, opts.ExternalOutbounds)
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
		// SSH uses a public key held by the user, not a credential that can be
		// safely embedded in a proxy subscription. The panel exposes it as a
		// dedicated SSH access card instead.
		if !inbound.Enabled || inbound.Protocol == model.ProtocolSSH {
			continue
		}
		if opts.InboundUsers != nil && !subscriptionInboundAllowed(user.ID, inbound.ID, opts.InboundUsers) {
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
		adapter, err := AdapterFor(inbound.Protocol)
		if err != nil {
			return nil, err
		}
		branches := subscriptionBranchesForInbound(inbound, opts.ProxyPaths, opts.ProxyPathSteps)
		if len(branches) == 0 {
			raw, err := adapter.SubscriptionNode(user, inbound, server)
			if err != nil {
				return nil, err
			}
			raw["oboard_group"] = group
			nodes = append(nodes, SubscriptionNode{Name: standaloneName, Group: group, ServerID: server.ID, Inbound: inbound, Server: server, Raw: raw})
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameStandalone, resourceID: inbound.ID, serverID: server.ID})
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
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameProxyPath, resourceID: path.ID, serverID: server.ID})
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
		nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameExternal, resourceID: external.ID})
	}
	resolveSubscriptionNodeNames(nodes, nameRefs)
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
	case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS:
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

func renderPlainJSONSubscription(nodes []SubscriptionNode) (string, error) {
	b, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func renderSingBoxSubscription(nodes []SubscriptionNode) (string, error) {
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	for _, node := range nodes {
		outbounds = append(outbounds, cloneMap(node.Raw))
	}
	config := SingBoxConfig{Log: map[string]any{"level": "warn"}, DNS: defaultDNS("remote"), Inbounds: []map[string]any{}, Outbounds: outbounds, Route: map[string]any{"final": "direct"}}
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func renderClashMetaSubscription(nodes []SubscriptionNode) (string, error) {
	if len(nodes) == 0 {
		return "proxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n", nil
	}
	var b strings.Builder
	b.WriteString("proxies:\n")
	groups := map[string][]string{}
	for _, node := range nodes {
		proxy, err := clashProxyFromNode(node)
		if err != nil {
			if strings.Contains(err.Error(), "unsupported") {
				continue
			}
			return "", err
		}
		if strings.TrimSpace(proxy) == "" {
			continue
		}
		b.WriteString(proxy)
		groups[node.Group] = append(groups[node.Group], node.Name)
	}
	b.WriteString("proxy-groups:\n")
	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		b.WriteString("  - name: ")
		b.WriteString(yamlQuote(name))
		b.WriteString("\n    type: select\n    proxies:\n")
		for _, proxy := range groups[name] {
			b.WriteString("      - ")
			b.WriteString(yamlQuote(proxy))
			b.WriteByte('\n')
		}
		b.WriteString("      - DIRECT\n")
	}
	b.WriteString("rules:\n")
	if len(groupNames) > 0 {
		b.WriteString("  - MATCH,")
		b.WriteString(groupNames[0])
		b.WriteByte('\n')
	} else {
		b.WriteString("  - MATCH,DIRECT\n")
	}
	return b.String(), nil
}

func renderV2RaySubscription(nodes []SubscriptionNode) (string, error) {
	list, err := renderURIListSubscription(nodes)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString([]byte(list)), nil
}

func renderURIListSubscription(nodes []SubscriptionNode) (string, error) {
	lines := make([]string, 0, len(nodes))
	for _, node := range nodes {
		uri, err := shareURIFromNode(node)
		if err != nil {
			if strings.Contains(err.Error(), "unsupported") {
				continue
			}
			return "", err
		}
		lines = append(lines, uri)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func shareURIFromNode(node SubscriptionNode) (string, error) {
	raw := node.Raw
	typ := stringFromAny(raw["type"])
	server := stringFromAny(raw["server"])
	port := intFromAny(raw["server_port"])
	if server == "" || port == 0 {
		return "", fmt.Errorf("subscription node %s missing server/server_port", node.Name)
	}
	switch typ {
	case "vless":
		return vlessShareURI(node, server, port), nil
	case "hysteria2":
		return passwordShareURI("hysteria2", node, server, port, stringFromAny(raw["password"])), nil
	case "anytls":
		return passwordShareURI("anytls", node, server, port, stringFromAny(raw["password"])), nil
	case "shadowsocks":
		method := stringFromAny(raw["method"])
		password := stringFromAny(raw["password"])
		if method == "" || password == "" {
			return "", fmt.Errorf("subscription node %s missing shadowsocks method/password", node.Name)
		}
		userInfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
		return "ss://" + userInfo + "@" + server + ":" + strconv.Itoa(port) + "#" + url.QueryEscape(node.Name), nil
	case "socks":
		username := stringFromAny(raw["username"])
		password := stringFromAny(raw["password"])
		if username == "" && password == "" {
			return "socks5://" + server + ":" + strconv.Itoa(port) + "#" + url.QueryEscape(node.Name), nil
		}
		return "socks5://" + url.UserPassword(username, password).String() + "@" + server + ":" + strconv.Itoa(port) + "#" + url.QueryEscape(node.Name), nil
	default:
		return "", errors.New("unsupported URI proxy type " + typ)
	}
}

func vlessShareURI(node SubscriptionNode, server string, port int) string {
	raw := node.Raw
	q := url.Values{}
	q.Set("encryption", "none")
	applyVLESSTransportURIParams(q, raw)
	if flow := stringFromAny(raw["flow"]); flow != "" {
		q.Set("flow", flow)
	}
	if packetEncoding := stringFromAny(raw["packet_encoding"]); packetEncoding != "" {
		q.Set("packetEncoding", packetEncoding)
	}
	applyTLSURIParams(q, raw)
	return "vless://" + url.QueryEscape(stringFromAny(raw["uuid"])) + "@" + server + ":" + strconv.Itoa(port) + "?" + q.Encode() + "#" + url.QueryEscape(node.Name)
}

func applyVLESSTransportURIParams(q url.Values, raw map[string]any) {
	transport, ok := raw["transport"].(map[string]any)
	if !ok {
		q.Set("type", "tcp")
		return
	}
	transportType := stringFromAny(transport["type"])
	if transportType == "" {
		transportType = "tcp"
	}
	q.Set("type", transportType)
	switch transportType {
	case "ws":
		if path := stringFromAny(transport["path"]); path != "" {
			q.Set("path", path)
		}
		if headers, ok := transport["headers"].(map[string]any); ok {
			if host := stringFromAny(headers["Host"]); host != "" {
				q.Set("host", host)
			}
		}
	case "grpc":
		if serviceName := stringFromAny(transport["service_name"]); serviceName != "" {
			q.Set("serviceName", serviceName)
		}
	}
}

func passwordShareURI(scheme string, node SubscriptionNode, server string, port int, password string) string {
	q := url.Values{}
	applyTLSURIParams(q, node.Raw)
	return scheme + "://" + url.QueryEscape(password) + "@" + server + ":" + strconv.Itoa(port) + querySuffix(q) + "#" + url.QueryEscape(node.Name)
}

func applyTLSURIParams(q url.Values, raw map[string]any) {
	tlsRaw, ok := raw["tls"].(map[string]any)
	if !ok {
		return
	}
	if enabled, ok := tlsRaw["enabled"].(bool); ok && enabled {
		q.Set("security", "tls")
	}
	if serverName := stringFromAny(tlsRaw["server_name"]); serverName != "" {
		q.Set("sni", serverName)
	}
	if reality, ok := tlsRaw["reality"].(map[string]any); ok {
		if publicKey := stringFromAny(reality["public_key"]); publicKey != "" {
			q.Set("security", "reality")
			q.Set("pbk", publicKey)
			if fp := realityFingerprint(tlsRaw); fp != "" {
				q.Set("fp", fp)
			}
		}
		if shortID := stringFromAny(reality["short_id"]); shortID != "" {
			q.Set("sid", shortID)
		}
	}
}

func realityFingerprint(tlsRaw map[string]any) string {
	if utls, ok := tlsRaw["utls"].(map[string]any); ok {
		if fp := strings.TrimSpace(stringFromAny(utls["fingerprint"])); fp != "" {
			return fp
		}
	}
	if fp := strings.TrimSpace(stringFromAny(tlsRaw["fingerprint"])); fp != "" {
		return fp
	}
	return "chrome"
}

func querySuffix(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func clashProxyFromNode(node SubscriptionNode) (string, error) {
	raw := node.Raw
	typ, _ := raw["type"].(string)
	server, _ := raw["server"].(string)
	port := intFromAny(raw["server_port"])
	if server == "" || port == 0 {
		return "", fmt.Errorf("subscription node %s missing server/server_port", node.Name)
	}
	var b strings.Builder
	b.WriteString("  - name: ")
	b.WriteString(yamlQuote(node.Name))
	b.WriteString("\n")
	switch typ {
	case "vless":
		b.WriteString("    type: vless\n")
		b.WriteString("    server: ")
		b.WriteString(yamlQuote(server))
		b.WriteString("\n    port: ")
		b.WriteString(strconv.Itoa(port))
		b.WriteString("\n    uuid: ")
		b.WriteString(yamlQuote(stringFromAny(raw["uuid"])))
		b.WriteString("\n")
		if flow := stringFromAny(raw["flow"]); flow != "" {
			b.WriteString("    flow: ")
			b.WriteString(yamlQuote(flow))
			b.WriteString("\n")
		}
		if transport, ok := raw["transport"].(map[string]any); ok {
			if transportType := stringFromAny(transport["type"]); transportType != "" {
				b.WriteString("    network: ")
				b.WriteString(yamlQuote(transportType))
				b.WriteString("\n")
			}
		}
		applyClashTLS(&b, raw)
	case "hysteria2":
		b.WriteString("    type: hysteria2\n")
		b.WriteString("    server: ")
		b.WriteString(yamlQuote(server))
		b.WriteString("\n    port: ")
		b.WriteString(strconv.Itoa(port))
		b.WriteString("\n    password: ")
		b.WriteString(yamlQuote(stringFromAny(raw["password"])))
		b.WriteString("\n")
		applyClashTLS(&b, raw)
	case "anytls":
		b.WriteString("    type: anytls\n")
		b.WriteString("    server: ")
		b.WriteString(yamlQuote(server))
		b.WriteString("\n    port: ")
		b.WriteString(strconv.Itoa(port))
		b.WriteString("\n    password: ")
		b.WriteString(yamlQuote(stringFromAny(raw["password"])))
		b.WriteString("\n")
		applyClashTLS(&b, raw)
	case "shadowsocks":
		b.WriteString("    type: ss\n")
		b.WriteString("    server: ")
		b.WriteString(yamlQuote(server))
		b.WriteString("\n    port: ")
		b.WriteString(strconv.Itoa(port))
		b.WriteString("\n    cipher: ")
		b.WriteString(yamlQuote(stringFromAny(raw["method"])))
		b.WriteString("\n    password: ")
		b.WriteString(yamlQuote(stringFromAny(raw["password"])))
		b.WriteString("\n")
		if udpOverTCPEnabled(raw["udp_over_tcp"]) {
			b.WriteString("    udp: true\n    udp-over-tcp: true\n")
		}
	case "socks":
		b.WriteString("    type: socks5\n")
		b.WriteString("    server: ")
		b.WriteString(yamlQuote(server))
		b.WriteString("\n    port: ")
		b.WriteString(strconv.Itoa(port))
		b.WriteString("\n")
		if username := stringFromAny(raw["username"]); username != "" {
			b.WriteString("    username: ")
			b.WriteString(yamlQuote(username))
			b.WriteString("\n")
		}
		if password := stringFromAny(raw["password"]); password != "" {
			b.WriteString("    password: ")
			b.WriteString(yamlQuote(password))
			b.WriteString("\n")
		}
	default:
		return "", errors.New("unsupported clash proxy type " + typ)
	}
	return b.String(), nil
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

func applyClashTLS(b *strings.Builder, raw map[string]any) {
	tlsRaw, ok := raw["tls"].(map[string]any)
	if !ok {
		return
	}
	if enabled, ok := tlsRaw["enabled"].(bool); ok {
		b.WriteString("    tls: ")
		if enabled {
			b.WriteString("true\n")
		} else {
			b.WriteString("false\n")
		}
	}
	if serverName := stringFromAny(tlsRaw["server_name"]); serverName != "" {
		b.WriteString("    servername: ")
		b.WriteString(yamlQuote(serverName))
		b.WriteString("\n")
	}
	if reality, ok := tlsRaw["reality"].(map[string]any); ok {
		if publicKey := stringFromAny(reality["public_key"]); publicKey != "" {
			b.WriteString("    client-fingerprint: ")
			b.WriteString(yamlQuote(realityFingerprint(tlsRaw)))
			b.WriteString("\n")
			b.WriteString("    reality-opts:\n      public-key: ")
			b.WriteString(yamlQuote(publicKey))
			b.WriteString("\n")
			if shortID := stringFromAny(reality["short_id"]); shortID != "" {
				b.WriteString("      short-id: ")
				b.WriteString(yamlQuote(shortID))
				b.WriteString("\n")
			}
		}
	}
}

func normalizeSubscriptionFormat(format model.SubscriptionFormat) model.SubscriptionFormat {
	switch strings.ToLower(strings.TrimSpace(string(format))) {
	case "", "singbox", "sing-box":
		return model.SubscriptionFormatSingBox
	case "plain-json", "plainjson", "plain json", "json":
		return model.SubscriptionFormatPlainJSON
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
		model.SubscriptionFormatPlainJSON,
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
	case model.SubscriptionFormatPlainJSON, model.SubscriptionFormatSingBox:
		return "application/json"
	case model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatStash, model.SubscriptionFormatClash:
		return "text/yaml; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func yamlQuote(v string) string {
	if v == "" {
		return `""`
	}
	b, _ := json.Marshal(v)
	return string(b)
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
