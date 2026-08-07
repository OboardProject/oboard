package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// ValidateSubscriptionNodeOrderPolicy normalizes and validates an ordering
// policy for storage. Region codes are uppercased and deduplicated; entry keys
// must be "inbound:<id>". The returned policy is the canonical snapshot.
func ValidateSubscriptionNodeOrderPolicy(policy model.SubscriptionNodeOrderPolicy) (model.SubscriptionNodeOrderPolicy, error) {
	out := policy
	out.Version = model.SubscriptionNodeOrderVersion
	switch out.Mode {
	case "", model.SubscriptionNodeOrderLegacyGroupName, model.SubscriptionNodeOrderExitRegion, model.SubscriptionNodeOrderEntry, model.SubscriptionNodeOrderManual:
		if out.Mode == "" {
			out.Mode = model.SubscriptionNodeOrderLegacyGroupName
		}
	default:
		return out, fmt.Errorf("invalid node order mode %q", out.Mode)
	}
	switch out.ManualSeed {
	case "", model.SubscriptionNodeOrderExitRegion, model.SubscriptionNodeOrderEntry:
		if out.ManualSeed == "" {
			out.ManualSeed = model.SubscriptionNodeOrderExitRegion
		}
	default:
		return out, fmt.Errorf("invalid manual_seed %q", out.ManualSeed)
	}
	switch out.EntryRegionOrderMode {
	case "", model.SubscriptionNodeEntryRegionOrderInheritExit, model.SubscriptionNodeEntryRegionOrderCustom:
		if out.EntryRegionOrderMode == "" {
			out.EntryRegionOrderMode = model.SubscriptionNodeEntryRegionOrderInheritExit
		}
	default:
		return out, fmt.Errorf("invalid entry_region_order_mode %q", out.EntryRegionOrderMode)
	}
	regions, err := normalizeRegionOrderList(out.ExitRegionOrder)
	if err != nil {
		return out, err
	}
	out.ExitRegionOrder = regions
	entryRegions, err := normalizeRegionOrderList(out.EntryRegionOrder)
	if err != nil {
		return out, err
	}
	out.EntryRegionOrder = entryRegions
	entries := []string{}
	seen := map[string]bool{}
	for _, key := range out.EntryOrder {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		nodeType, id, ok := ParseNodeKey(key)
		if !ok || nodeType != model.AssignableNodeInbound || id <= 0 {
			return out, fmt.Errorf("invalid entry key %q", key)
		}
		if seen[key] {
			return out, fmt.Errorf("duplicate entry key %q", key)
		}
		seen[key] = true
		entries = append(entries, key)
	}
	out.EntryOrder = entries
	return out, nil
}

func normalizeRegionOrderList(list []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range list {
		code := NormalizeRegionCode(raw)
		if code == "" {
			return nil, fmt.Errorf("invalid region code %q", raw)
		}
		if seen[code] {
			return nil, fmt.Errorf("duplicate region code %q", code)
		}
		seen[code] = true
		out = append(out, code)
	}
	return out, nil
}

type subscriptionOrderRank struct {
	exitRegionRank  int
	exitRegionCode  string
	entryRegionRank int
	entryListed     bool
	entryIndex      int
	entryServerID   int64
	entryInboundID  int64
	entryKey        string
	manualPosition  int
	hasManual       bool
	name            string
	key             string
}

// OrderSubscriptionNodes applies the revision ordering policy to the final
// node list. Every renderer shares this order; the legacy comparator is the
// exact pre-ordering behavior (DisplayGroup, then name, then stable input
// order) so upgraded revisions keep their issued body and ETag.
func OrderSubscriptionNodes(nodes []SubscriptionNode, policy model.SubscriptionNodeOrderPolicy) []SubscriptionNode {
	if len(nodes) < 2 {
		return append([]SubscriptionNode(nil), nodes...)
	}
	mode := policy.Mode
	if mode == "" {
		mode = model.SubscriptionNodeOrderLegacyGroupName
	}
	if mode == model.SubscriptionNodeOrderLegacyGroupName {
		ordered := append([]SubscriptionNode(nil), nodes...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].Group == ordered[j].Group {
				return ordered[i].Name < ordered[j].Name
			}
			return ordered[i].Group < ordered[j].Group
		})
		return ordered
	}
	normalized, err := ValidateSubscriptionNodeOrderPolicy(policy)
	if err != nil {
		normalized = model.NewSubscriptionNodeOrderPolicy()
		normalized.Mode = mode
	}
	exitRank := buildRegionRank(normalized.ExitRegionOrder)
	entryRegionRank := exitRank
	if normalized.EntryRegionOrderMode == model.SubscriptionNodeEntryRegionOrderCustom {
		entryRegionRank = buildRegionRank(normalized.EntryRegionOrder)
	}
	entryRankByKey := map[string]int{}
	for index, key := range normalized.EntryOrder {
		entryRankByKey[key] = index
	}
	ranked := make([]rankedSubscriptionNode, len(nodes))
	for index, node := range nodes {
		exitCode := NormalizeRegionCode(node.ExitRegion)
		entryCode := NormalizeRegionCode(node.EntryRegion)
		rank := subscriptionOrderRank{
			exitRegionRank:  regionRankOf(exitRank, exitCode),
			exitRegionCode:  exitCode,
			entryRegionRank: regionRankOf(entryRegionRank, entryCode),
			entryKey:        node.EntryKey,
			entryServerID:   node.EntryServerID,
			entryInboundID:  node.EntryInboundID,
			manualPosition:  -1,
			name:            node.Name,
			key:             node.Key,
		}
		if node.ManualPosition != nil {
			rank.hasManual = true
			rank.manualPosition = *node.ManualPosition
		}
		if index, ok := entryRankByKey[node.EntryKey]; ok {
			rank.entryListed = true
			rank.entryIndex = index
		} else {
			rank.entryIndex = len(entryRankByKey)
		}
		ranked[index] = rankedSubscriptionNode{node: node, rank: rank}
	}
	entryLess := func(a, b subscriptionOrderRank) bool {
		if a.entryListed != b.entryListed {
			return a.entryListed
		}
		if a.entryIndex != b.entryIndex {
			return a.entryIndex < b.entryIndex
		}
		if a.entryServerID != b.entryServerID {
			return a.entryServerID < b.entryServerID
		}
		if a.entryInboundID != b.entryInboundID {
			return a.entryInboundID < b.entryInboundID
		}
		return a.entryKey < b.entryKey
	}
	switch mode {
	case model.SubscriptionNodeOrderExitRegion:
		sort.Slice(ranked, func(i, j int) bool {
			a, b := ranked[i].rank, ranked[j].rank
			if a.exitRegionRank != b.exitRegionRank {
				return a.exitRegionRank < b.exitRegionRank
			}
			if a.exitRegionCode != b.exitRegionCode {
				return a.exitRegionCode < b.exitRegionCode
			}
			if a.entryRegionRank != b.entryRegionRank {
				return a.entryRegionRank < b.entryRegionRank
			}
			if entryLess(a, b) {
				return true
			}
			if entryLess(b, a) {
				return false
			}
			if a.name != b.name {
				return a.name < b.name
			}
			return a.key < b.key
		})
	case model.SubscriptionNodeOrderEntry:
		sort.Slice(ranked, func(i, j int) bool {
			a, b := ranked[i].rank, ranked[j].rank
			if a.entryRegionRank != b.entryRegionRank {
				return a.entryRegionRank < b.entryRegionRank
			}
			if entryLess(a, b) {
				return true
			}
			if entryLess(b, a) {
				return false
			}
			if a.exitRegionRank != b.exitRegionRank {
				return a.exitRegionRank < b.exitRegionRank
			}
			if a.exitRegionCode != b.exitRegionCode {
				return a.exitRegionCode < b.exitRegionCode
			}
			if a.name != b.name {
				return a.name < b.name
			}
			return a.key < b.key
		})
	case model.SubscriptionNodeOrderManual:
		sort.SliceStable(ranked, func(i, j int) bool {
			a, b := ranked[i].rank, ranked[j].rank
			if a.hasManual != b.hasManual {
				return a.hasManual
			}
			if a.hasManual {
				return a.manualPosition < b.manualPosition
			}
			return false
		})
		seedMode := normalized.ManualSeed
		if seedMode == "" {
			seedMode = model.SubscriptionNodeOrderExitRegion
		}
		ranked = orderManualTail(ranked, seedMode)
	}
	out := make([]SubscriptionNode, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].node
	}
	return out
}

// orderManualTail sorts the unplaced nodes (NULL manual position) after the
// placed ones using the manual seed rule; the placed prefix stays untouched.
type rankedSubscriptionNode struct {
	node SubscriptionNode
	rank subscriptionOrderRank
}

func orderManualTail(ranked []rankedSubscriptionNode, seedMode model.SubscriptionNodeOrderMode) []rankedSubscriptionNode {
	placedCount := 0
	for _, item := range ranked {
		if item.rank.hasManual {
			placedCount++
		}
	}
	if placedCount == len(ranked) {
		return ranked
	}
	firstUnplaced := placedCount
	head := ranked[:firstUnplaced]
	tail := append([]rankedSubscriptionNode{}, ranked[firstUnplaced:]...)
	seed := func(a, b subscriptionOrderRank) bool {
		if a.exitRegionRank != b.exitRegionRank {
			return a.exitRegionRank < b.exitRegionRank
		}
		if a.exitRegionCode != b.exitRegionCode {
			return a.exitRegionCode < b.exitRegionCode
		}
		if a.entryRegionRank != b.entryRegionRank {
			return a.entryRegionRank < b.entryRegionRank
		}
		if a.entryKey != b.entryKey {
			return a.entryKey < b.entryKey
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.key < b.key
	}
	if seedMode == model.SubscriptionNodeOrderEntry {
		seed = func(a, b subscriptionOrderRank) bool {
			if a.entryRegionRank != b.entryRegionRank {
				return a.entryRegionRank < b.entryRegionRank
			}
			if a.entryKey != b.entryKey {
				return a.entryKey < b.entryKey
			}
			if a.exitRegionRank != b.exitRegionRank {
				return a.exitRegionRank < b.exitRegionRank
			}
			if a.exitRegionCode != b.exitRegionCode {
				return a.exitRegionCode < b.exitRegionCode
			}
			if a.name != b.name {
				return a.name < b.name
			}
			return a.key < b.key
		}
	}
	sort.SliceStable(tail, func(i, j int) bool { return seed(tail[i].rank, tail[j].rank) })
	return append(head, tail...)
}

// buildRegionRank maps normalized region codes to their order index. Valid but
// unlisted codes share the len(list) rank and are then stable-sorted by code;
// the empty (unresolved) region always ranks last.
func buildRegionRank(list []string) map[string]int {
	rank := map[string]int{}
	for index, code := range list {
		rank[code] = index
	}
	return rank
}

func regionRankOf(rank map[string]int, code string) int {
	if index, ok := rank[code]; ok {
		return index
	}
	if code == "" {
		return 1 << 30
	}
	return len(rank)
}

var errSubscriptionOrderPolicy = errors.New("invalid subscription node order policy")

// BuildOrderingNodes renders the display metadata (names, display groups,
// topology, manual positions) for one revision's node set and applies the
// ordering policy. It shares the topology resolver and the name
// disambiguation with the subscription generator so the previewed order and
// names match every rendered subscription format. Raw subscription payloads
// are intentionally not built.
func BuildOrderingNodes(
	planNodes []model.SubscriptionPlanNode,
	servers []model.Server,
	inbounds []model.Inbound,
	paths []model.ProxyPath,
	steps []model.ProxyPathStep,
	egressResults []model.ProxyPathEgressResult,
	externals []model.ExternalOutbound,
	policy model.SubscriptionNodeOrderPolicy,
) ([]SubscriptionNode, error) {
	topologies, resolvedPaths, resolvedExternals, err := ResolveAssignableNodeTopologies(AssignableNodeCatalogInput{
		Servers:           servers,
		Inbounds:          inbounds,
		ProxyPaths:        paths,
		ProxyPathSteps:    steps,
		EgressResults:     egressResults,
		ExternalOutbounds: externals,
	})
	if err != nil {
		return nil, err
	}
	paths = resolvedPaths
	externals = resolvedExternals
	serverByID := map[int64]model.Server{}
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	pathByID := map[int64]model.ProxyPath{}
	for _, path := range paths {
		pathByID[path.ID] = path
	}
	externalByID := map[int64]model.ExternalOutbound{}
	for _, external := range externals {
		externalByID[external.ID] = external
	}
	positions := map[string]int{}
	for _, pn := range planNodes {
		if pn.SortPosition == nil {
			continue
		}
		positions[NodeKeyOf(pn.NodeType, pn.NodeID)] = *pn.SortPosition
	}
	nodes := make([]SubscriptionNode, 0, len(planNodes))
	nameRefs := make([]subscriptionNodeNameRef, 0, len(planNodes))
	for _, pn := range planNodes {
		key := NodeKeyOf(pn.NodeType, pn.NodeID)
		topo, topoOK := topologies[key]
		group := strings.TrimSpace(pn.DisplayGroup)
		if group == "" {
			group = "default"
		}
		switch pn.NodeType {
		case model.AssignableNodeProxyPath:
			path, ok := pathByID[pn.NodeID]
			if !ok {
				continue
			}
			var server model.Server
			if inbound, found := inboundByID[path.InboundID]; found {
				server = serverByID[inbound.ServerID]
			}
			name := strings.TrimSpace(path.Name)
			if name == "" {
				name = fmt.Sprintf("%s 分支 %d", proxyPathServerLabel(server, server.ID), path.ID)
			}
			node := SubscriptionNode{
				Key:      key,
				NodeType: pn.NodeType,
				NodeID:   pn.NodeID,
				Name:     name,
				Group:    group,
				ServerID: topo.EntryServerID,
				Raw:      map[string]any{},
			}
			applyTopologyToSubscriptionNode(&node, topo, topoOK)
			if position, ok := positions[key]; ok && position >= 0 {
				position := position
				node.ManualPosition = &position
			}
			nodes = append(nodes, node)
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameProxyPath, resourceID: path.ID, serverID: topo.EntryServerID, regionCode: path.EffectiveExitRegionCode})
		case model.AssignableNodeInbound:
			inbound, ok := inboundByID[pn.NodeID]
			if !ok {
				continue
			}
			server := serverByID[inbound.ServerID]
			regionCode, _ := EffectiveServerRegion(server)
			node := SubscriptionNode{
				Key:      key,
				NodeType: pn.NodeType,
				NodeID:   pn.NodeID,
				Name:     proxyPathServerLabel(server, server.ID),
				Group:    group,
				ServerID: server.ID,
				Inbound:  inbound,
				Server:   server,
				Raw:      map[string]any{},
			}
			applyTopologyToSubscriptionNode(&node, topo, topoOK)
			if position, ok := positions[key]; ok && position >= 0 {
				position := position
				node.ManualPosition = &position
			}
			nodes = append(nodes, node)
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameStandalone, resourceID: inbound.ID, serverID: server.ID, regionCode: regionCode})
		case model.AssignableNodeExternalOutbound:
			external, ok := externalByID[pn.NodeID]
			if !ok {
				continue
			}
			name := strings.TrimSpace(external.Name)
			if name == "" {
				name = fmt.Sprintf("%s-%d", external.Protocol, external.ID)
			}
			node := SubscriptionNode{
				Key:      key,
				NodeType: pn.NodeType,
				NodeID:   pn.NodeID,
				Name:     name,
				Group:    group,
				Raw:      map[string]any{},
			}
			applyTopologyToSubscriptionNode(&node, topo, topoOK)
			if position, ok := positions[key]; ok && position >= 0 {
				position := position
				node.ManualPosition = &position
			}
			nodes = append(nodes, node)
			nameRefs = append(nameRefs, subscriptionNodeNameRef{index: len(nodes) - 1, kind: subscriptionNodeNameExternal, resourceID: external.ID, regionCode: external.EffectiveRegionCode})
		}
	}
	resolveSubscriptionNodeNames(nodes, nameRefs)
	for _, ref := range nameRefs {
		nodes[ref.index].Name = RegionFlagEmoji(ref.regionCode) + " " + nodes[ref.index].Name
		nodes[ref.index].Raw["tag"] = nodes[ref.index].Name
	}
	return OrderSubscriptionNodes(nodes, policy), nil
}

func applyTopologyToSubscriptionNode(node *SubscriptionNode, topo AssignableNodeTopology, ok bool) {
	if !ok {
		return
	}
	node.EntryKey = topo.EntryKey
	node.EntryInboundID = topo.EntryInboundID
	node.EntryServerID = topo.EntryServerID
	node.EntryRegion = topo.EntryRegion
	node.ExitServerID = topo.ExitServerID
	node.ExitExternalOutboundID = topo.ExitExternalOutboundID
	node.ExitRegion = topo.ExitRegion
}
