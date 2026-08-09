package core

import (
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
	originalVersion := out.Version
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
	if out.NewNodePlacement == "" {
		if originalVersion <= 1 {
			out.NewNodePlacement = model.SubscriptionNodePlacementPending
		} else {
			out.NewNodePlacement = model.SubscriptionNodePlacementByTemplate
		}
	}
	if out.UnmatchedPlacement == "" {
		if originalVersion <= 1 {
			out.UnmatchedPlacement = model.SubscriptionNodePlacementPending
		} else {
			out.UnmatchedPlacement = model.SubscriptionNodePlacementAppend
		}
	}
	if err := validateNodePlacement(out.NewNodePlacement, true); err != nil {
		return out, err
	}
	if err := validateNodePlacement(out.UnmatchedPlacement, false); err != nil {
		return out, err
	}
	return out, nil
}

func validateNodePlacement(value model.SubscriptionNodePlacement, allowTemplate bool) error {
	switch value {
	case model.SubscriptionNodePlacementAppend, model.SubscriptionNodePlacementPending:
		return nil
	case model.SubscriptionNodePlacementByTemplate:
		if allowTemplate {
			return nil
		}
	}
	return fmt.Errorf("invalid node placement %q", value)
}

func ValidateNodeOrderTemplatePolicy(policy model.NodeOrderTemplatePolicy) (model.NodeOrderTemplatePolicy, error) {
	out := policy
	if out.Version == 0 {
		out.Version = 1
	}
	if out.Version != 1 {
		return out, fmt.Errorf("invalid template policy version %d", out.Version)
	}
	if out.BaseMode != model.SubscriptionNodeOrderExitRegion && out.BaseMode != model.SubscriptionNodeOrderEntry {
		return out, fmt.Errorf("invalid template base_mode %q", out.BaseMode)
	}
	planPolicy, err := ValidateSubscriptionNodeOrderPolicy(SubscriptionPolicyFromTemplate(out, out.BaseMode))
	if err != nil {
		return out, err
	}
	out.ExitRegionOrder = planPolicy.ExitRegionOrder
	out.EntryRegionOrderMode = planPolicy.EntryRegionOrderMode
	out.EntryRegionOrder = planPolicy.EntryRegionOrder
	out.EntryOrder = planPolicy.EntryOrder
	out.NewNodePlacement = planPolicy.NewNodePlacement
	out.UnmatchedPlacement = planPolicy.UnmatchedPlacement
	return out, nil
}

func SubscriptionPolicyFromTemplate(template model.NodeOrderTemplatePolicy, mode model.SubscriptionNodeOrderMode) model.SubscriptionNodeOrderPolicy {
	if mode == "" {
		mode = template.BaseMode
	}
	return model.SubscriptionNodeOrderPolicy{
		Version:              model.SubscriptionNodeOrderVersion,
		Mode:                 mode,
		ManualSeed:           template.BaseMode,
		ExitRegionOrder:      append([]string(nil), template.ExitRegionOrder...),
		EntryRegionOrderMode: template.EntryRegionOrderMode,
		EntryRegionOrder:     append([]string(nil), template.EntryRegionOrder...),
		EntryOrder:           append([]string(nil), template.EntryOrder...),
		NewNodePlacement:     template.NewNodePlacement,
		UnmatchedPlacement:   template.UnmatchedPlacement,
	}
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

type OrderingWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count"`
}

type AutoPlacementDetail struct {
	NodeKey string `json:"node_key"`
	Bucket  string `json:"bucket"`
	Reason  string `json:"reason"`
}

type AutoPlacementResult struct {
	OrderedNodeKeys []string              `json:"ordered_node_keys"`
	PendingNodeKeys []string              `json:"pending_node_keys"`
	Warnings        []OrderingWarning     `json:"warnings"`
	Details         []AutoPlacementDetail `json:"details"`
}

type placementBucket struct {
	key     string
	rank    int
	matched bool
}

func AutoPlaceNewManualNodes(existing, added []SubscriptionNode, policy model.SubscriptionNodeOrderPolicy) AutoPlacementResult {
	normalized, err := ValidateSubscriptionNodeOrderPolicy(policy)
	if err != nil {
		normalized = model.NewSubscriptionNodeOrderPolicy()
		normalized.Mode = model.SubscriptionNodeOrderManual
	}
	result := AutoPlacementResult{OrderedNodeKeys: make([]string, 0, len(existing)+len(added))}
	for _, node := range existing {
		result.OrderedNodeKeys = append(result.OrderedNodeKeys, node.Key)
	}
	if len(added) == 0 {
		return result
	}
	seedPolicy := normalized
	seedPolicy.Mode = normalized.ManualSeed
	if seedPolicy.Mode != model.SubscriptionNodeOrderEntry {
		seedPolicy.Mode = model.SubscriptionNodeOrderExitRegion
	}
	orderedAdded := OrderSubscriptionNodes(added, seedPolicy)
	if normalized.NewNodePlacement == model.SubscriptionNodePlacementPending {
		for _, node := range orderedAdded {
			result.PendingNodeKeys = append(result.PendingNodeKeys, node.Key)
			result.Details = append(result.Details, AutoPlacementDetail{NodeKey: node.Key, Bucket: placementBucketFor(node, normalized).key, Reason: "pending_by_policy"})
		}
		return result
	}
	if normalized.NewNodePlacement == model.SubscriptionNodePlacementAppend {
		for _, node := range orderedAdded {
			result.OrderedNodeKeys = append(result.OrderedNodeKeys, node.Key)
			result.Details = append(result.Details, AutoPlacementDetail{NodeKey: node.Key, Bucket: placementBucketFor(node, normalized).key, Reason: "append_by_policy"})
		}
		return result
	}

	nodesByKey := map[string]SubscriptionNode{}
	for _, node := range existing {
		nodesByKey[node.Key] = node
	}
	groups := map[string][]SubscriptionNode{}
	buckets := map[string]placementBucket{}
	unmatched := []SubscriptionNode{}
	for _, node := range orderedAdded {
		bucket := placementBucketFor(node, normalized)
		if !bucket.matched {
			unmatched = append(unmatched, node)
			continue
		}
		groups[bucket.key] = append(groups[bucket.key], node)
		buckets[bucket.key] = bucket
	}
	groupKeys := make([]string, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		if buckets[groupKeys[i]].rank == buckets[groupKeys[j]].rank {
			return groupKeys[i] < groupKeys[j]
		}
		return buckets[groupKeys[i]].rank < buckets[groupKeys[j]].rank
	})
	for _, groupKey := range groupKeys {
		bucket := buckets[groupKey]
		insertAt, reason := manualBucketInsertionIndex(result.OrderedNodeKeys, nodesByKey, bucket, normalized)
		keys := make([]string, 0, len(groups[groupKey]))
		for _, node := range groups[groupKey] {
			keys = append(keys, node.Key)
			nodesByKey[node.Key] = node
			result.Details = append(result.Details, AutoPlacementDetail{NodeKey: node.Key, Bucket: bucket.key, Reason: reason})
		}
		result.OrderedNodeKeys = insertStrings(result.OrderedNodeKeys, insertAt, keys)
	}
	if len(unmatched) > 0 {
		if normalized.UnmatchedPlacement == model.SubscriptionNodePlacementPending {
			for _, node := range unmatched {
				result.PendingNodeKeys = append(result.PendingNodeKeys, node.Key)
				result.Details = append(result.Details, AutoPlacementDetail{NodeKey: node.Key, Bucket: placementBucketFor(node, normalized).key, Reason: "unmatched_pending"})
			}
		} else {
			for _, node := range unmatched {
				result.OrderedNodeKeys = append(result.OrderedNodeKeys, node.Key)
				result.Details = append(result.Details, AutoPlacementDetail{NodeKey: node.Key, Bucket: placementBucketFor(node, normalized).key, Reason: "unmatched_append"})
			}
		}
		result.Warnings = append(result.Warnings, OrderingWarning{Code: "unmatched_nodes", Count: len(unmatched), Message: fmt.Sprintf("%d 个节点无法匹配模板", len(unmatched))})
	}
	return result
}

func placementBucketFor(node SubscriptionNode, policy model.SubscriptionNodeOrderPolicy) placementBucket {
	mode := policy.ManualSeed
	if mode == model.SubscriptionNodeOrderEntry {
		region := NormalizeRegionCode(node.EntryRegion)
		regionOrder := policy.ExitRegionOrder
		if policy.EntryRegionOrderMode == model.SubscriptionNodeEntryRegionOrderCustom {
			regionOrder = policy.EntryRegionOrder
		}
		regionRanks := buildRegionRank(regionOrder)
		regionRank, regionMatched := regionRanks[region]
		exactRank, exactMatched := -1, false
		for index, key := range policy.EntryOrder {
			if key == node.EntryKey {
				exactRank, exactMatched = index, true
				break
			}
		}
		matched := regionMatched || exactMatched
		rank := regionRank*100000 + 50000
		if !regionMatched {
			rank = len(regionOrder)*100000 + 50000
		}
		if exactMatched {
			rank = regionRank*100000 + exactRank
			if !regionMatched {
				rank = len(regionOrder)*100000 + exactRank
			}
		}
		key := "entry:" + region + ":" + node.EntryKey
		if regionMatched && !exactMatched {
			// Unlisted entries still belong to their listed region. Treat them as
			// one deterministic tail bucket so future nodes stay inside that
			// region without inventing an order based on display names.
			rank = regionRank*100000 + 99999
			key = "entry-region:" + region + ":unlisted"
		}
		return placementBucket{key: key, rank: rank, matched: matched && node.EntryKey != ""}
	}
	region := NormalizeRegionCode(node.ExitRegion)
	ranks := buildRegionRank(policy.ExitRegionOrder)
	rank, ok := ranks[region]
	return placementBucket{key: "exit:" + region, rank: rank, matched: ok && region != ""}
}

func SubscriptionNodeTemplateBucket(node SubscriptionNode, policy model.SubscriptionNodeOrderPolicy) (string, bool) {
	bucket := placementBucketFor(node, policy)
	return bucket.key, bucket.matched
}

func manualBucketInsertionIndex(order []string, nodes map[string]SubscriptionNode, target placementBucket, policy model.SubscriptionNodeOrderPolicy) (int, string) {
	lastSame := -1
	predecessorIndex, predecessorRank := -1, -1
	successorIndex, successorRank := -1, int(^uint(0)>>1)
	for index, key := range order {
		bucket := placementBucketFor(nodes[key], policy)
		if !bucket.matched {
			continue
		}
		if bucket.key == target.key {
			lastSame = index
		}
		if bucket.rank < target.rank && bucket.rank >= predecessorRank {
			predecessorRank = bucket.rank
			predecessorIndex = index
		}
		if bucket.rank > target.rank && bucket.rank < successorRank {
			successorRank = bucket.rank
			successorIndex = index
		}
	}
	if lastSame >= 0 {
		return lastSame + 1, "insert_after_same_bucket"
	}
	if predecessorIndex >= 0 {
		return predecessorIndex + 1, "insert_after_predecessor_bucket"
	}
	if successorIndex >= 0 {
		return successorIndex, "insert_before_successor_bucket"
	}
	return len(order), "append_without_anchor"
}

func insertStrings(input []string, at int, values []string) []string {
	out := make([]string, 0, len(input)+len(values))
	out = append(out, input[:at]...)
	out = append(out, values...)
	out = append(out, input[at:]...)
	return out
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
	nameOptions ...OrderingNameOptions,
) ([]SubscriptionNode, error) {
	names := OrderingNameOptions{}
	if len(nameOptions) > 0 {
		names = nameOptions[0]
	}
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
	planNames := map[string]*string{}
	for _, planNode := range planNodes {
		if planNode.DisplayNameOverride != nil {
			planNames[NodeKeyOf(planNode.NodeType, planNode.NodeID)] = planNode.DisplayNameOverride
		}
	}
	for i := range nodes {
		nodes[i].SourceName = nodes[i].Name
		nodes[i].GlobalName = ResolveEffectiveNodeName(nodes[i].SourceName, names.GlobalNodeNames[nodes[i].Key], nil)
		if value := planNames[nodes[i].Key]; value != nil {
			nodes[i].PlanNameOverride = value
			nodes[i].HasPlanNameOverride = true
		}
		nodes[i].Name = ResolveEffectiveNodeName(nodes[i].SourceName, names.GlobalNodeNames[nodes[i].Key], planNames[nodes[i].Key])
	}
	disambiguateEffectiveSubscriptionNodeNames(nodes)
	for _, ref := range nameRefs {
		nodes[ref.index].Name = RegionFlagEmoji(ref.regionCode) + " " + nodes[ref.index].Name
		nodes[ref.index].Raw["tag"] = nodes[ref.index].Name
	}
	return OrderSubscriptionNodes(nodes, policy), nil
}

type OrderingNameOptions struct {
	GlobalNodeNames map[string]*string
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
