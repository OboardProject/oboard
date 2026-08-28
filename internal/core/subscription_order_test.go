package core

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func orderTestNode(key, name, group, entryKey string, entryServerID, entryInboundID int64, entryRegion, exitRegion string, manual *int) SubscriptionNode {
	return SubscriptionNode{
		Key:            key,
		NodeType:       model.AssignableNodeProxyPath,
		NodeID:         0,
		Name:           name,
		Group:          group,
		EntryKey:       entryKey,
		EntryInboundID: entryInboundID,
		EntryServerID:  entryServerID,
		EntryRegion:    entryRegion,
		ExitRegion:     exitRegion,
		ManualPosition: manual,
	}
}

func keysOf(nodes []SubscriptionNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.Key)
	}
	return out
}

func TestLegacyOrderMatchesExistingBehavior(t *testing.T) {
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "🇯🇵 B", "g2", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:2", "🇯🇵 A", "g2", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:3", "🇺🇸 C", "g1", "inbound:2", 2, 2, "US", "US", nil),
		orderTestNode("proxy_path:4", "🇸🇬 D", "g1", "inbound:3", 3, 3, "SG", "SG", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, model.DefaultSubscriptionNodeOrderPolicy())
	want := []string{"proxy_path:4", "proxy_path:3", "proxy_path:2", "proxy_path:1"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("legacy order = %v, want %v", keysOf(ordered), want)
	}
}

func TestExitRegionCustomOrder(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.ExitRegionOrder = []string{"jp", "SG"}
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "US", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:1", 1, 1, "JP", "SG", nil),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:4", "n4", "g", "inbound:1", 1, 1, "JP", "DE", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:3", "proxy_path:2", "proxy_path:4", "proxy_path:1"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("exit region order = %v, want %v", keysOf(ordered), want)
	}
}

func TestExitRegionUnknownLast(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:1", 1, 1, "JP", "ZZ", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:2", "proxy_path:3", "proxy_path:1"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("unknown last order = %v, want %v", keysOf(ordered), want)
	}
}

func TestEntryUsesExitRegionOrderByDefault(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderEntry
	policy.ExitRegionOrder = []string{"JP", "US"}
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:2", 2, 2, "US", "US", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:3", 3, 3, "DE", "DE", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:2", "proxy_path:1", "proxy_path:3"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("inherit exit order = %v, want %v", keysOf(ordered), want)
	}
}

func TestEntryCustomRegionOrder(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderEntry
	policy.EntryRegionOrderMode = model.SubscriptionNodeEntryRegionOrderCustom
	policy.EntryRegionOrder = []string{"SG", "JP"}
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:2", 2, 2, "SG", "SG", nil),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:3", 3, 3, "US", "US", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:2", "proxy_path:1", "proxy_path:3"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("custom entry region order = %v, want %v", keysOf(ordered), want)
	}
}

func TestEntryCustomInboundOrder(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderEntry
	policy.EntryRegionOrderMode = model.SubscriptionNodeEntryRegionOrderCustom
	policy.EntryOrder = []string{"inbound:3", "inbound:1"}
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:2", 1, 2, "JP", "JP", nil),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:3", 1, 3, "JP", "JP", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:3", "proxy_path:1", "proxy_path:2"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("custom inbound order = %v, want %v", keysOf(ordered), want)
	}
}

func TestEntryKeepsSameInboundContiguous(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderEntry
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "a", "g", "inbound:1", 1, 1, "JP", "US", nil),
		orderTestNode("proxy_path:2", "b", "g", "inbound:1", 1, 1, "JP", "DE", nil),
		orderTestNode("proxy_path:3", "c", "g", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("proxy_path:4", "d", "g", "inbound:2", 2, 2, "JP", "JP", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	got := keysOf(ordered)
	positions := map[string]int{}
	for index, key := range got {
		positions[key] = index
	}
	inboundOne := []int{positions["proxy_path:1"], positions["proxy_path:2"], positions["proxy_path:3"]}
	sort.Ints(inboundOne)
	if inboundOne[0] != 0 || inboundOne[1] != 1 || inboundOne[2] != 2 {
		t.Fatalf("inbound:1 not contiguous: %v", got)
	}
}

func TestManualSeedFromExitRegion(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderManual
	policy.ManualSeed = model.SubscriptionNodeOrderExitRegion
	zero := 0
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "US", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:1", 1, 1, "JP", "SG", &zero),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:2", "proxy_path:1"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("manual seed order = %v, want %v", keysOf(ordered), want)
	}
}

func TestManualSeedFromEntry(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderManual
	policy.ManualSeed = model.SubscriptionNodeOrderEntry
	zero := 0
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:2", 2, 2, "JP", "US", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:1", 1, 1, "JP", "SG", &zero),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:2", "proxy_path:1"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("manual seed from entry = %v, want %v", keysOf(ordered), want)
	}
}

func TestManualExplicitPositions(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderManual
	three, one, two := 3, 1, 2
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "JP", &three),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:1", 1, 1, "JP", "JP", &one),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:1", 1, 1, "JP", "JP", &two),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:2", "proxy_path:3", "proxy_path:1"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("manual positions = %v, want %v", keysOf(ordered), want)
	}
}

func TestBuildOrderingNodesManualOrderPlacesStandaloneSSH(t *testing.T) {
	first, second := 0, 1
	servers := []model.Server{
		{ID: 1, Name: "沪日", Status: model.ServerOnline, RegionCode: "JP", DetectedRegionCode: "JP"},
		{ID: 2, Name: "9929", Status: model.ServerOnline, RegionCode: "DE", DetectedRegionCode: "DE"},
	}
	inbounds := []model.Inbound{
		{ID: 31, ServerID: 1, Name: "ssh", Protocol: model.ProtocolSSH, Port: 2222, Enabled: true},
		{ID: 41, ServerID: 2, Name: "vless", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true},
	}
	paths := []model.ProxyPath{{ID: 100, Kind: model.ProxyPathKindDirect, Name: "de-direct", InboundID: 41, Enabled: true}}
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderManual
	planNodes := []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 100, SortPosition: &first},
		{NodeType: model.AssignableNodeInbound, NodeID: 31, SortPosition: &second},
	}
	nodes, err := BuildOrderingNodes(planNodes, servers, inbounds, paths, nil, nil, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(nodes); !equalStrings(got, []string{"proxy_path:100", "inbound:31"}) {
		t.Fatalf("SSH after path = %v", got)
	}
	planNodes[0].SortPosition, planNodes[1].SortPosition = &second, &first
	nodes, err = BuildOrderingNodes(planNodes, servers, inbounds, paths, nil, nil, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(nodes); !equalStrings(got, []string{"inbound:31", "proxy_path:100"}) {
		t.Fatalf("SSH before path = %v", got)
	}
	if nodes[0].NodeType != model.AssignableNodeInbound || nodes[0].Inbound.Protocol != model.ProtocolSSH {
		t.Fatalf("leading SSH node = %#v", nodes[0])
	}
}

func TestManualUnplacedNodesUseSeedTail(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderManual
	policy.ManualSeed = model.SubscriptionNodeOrderExitRegion
	zero := 0
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "US", nil),
		orderTestNode("proxy_path:2", "n2", "g", "inbound:1", 1, 1, "JP", "SG", &zero),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:1", 1, 1, "JP", "JP", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	want := []string{"proxy_path:2", "proxy_path:3", "proxy_path:1"}
	if !equalStrings(keysOf(ordered), want) {
		t.Fatalf("manual unplaced tail = %v, want %v", keysOf(ordered), want)
	}
}

func TestExternalNodesWithoutEntryAreLast(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderEntry
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "n1", "g", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("external_outbound:2", "e2", "g", "", 0, 0, "", "US", nil),
		orderTestNode("proxy_path:3", "n3", "g", "inbound:2", 2, 2, "JP", "JP", nil),
		orderTestNode("external_outbound:1", "e1", "g", "", 0, 0, "", "SG", nil),
	}
	ordered := OrderSubscriptionNodes(nodes, policy)
	got := keysOf(ordered)
	if got[0] != "proxy_path:1" || got[1] != "proxy_path:3" || got[2] != "external_outbound:1" || got[3] != "external_outbound:2" {
		t.Fatalf("external last order = %v", got)
	}
}

func TestOrderingIsDeterministicAfterInputShuffle(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderManual
	policy.ManualSeed = model.SubscriptionNodeOrderEntry
	policy.ExitRegionOrder = []string{"JP", "SG", "US"}
	policy.EntryRegionOrderMode = model.SubscriptionNodeEntryRegionOrderCustom
	policy.EntryRegionOrder = []string{"SG", "JP"}
	policy.EntryOrder = []string{"inbound:3", "inbound:1"}
	zero, one, two := 0, 1, 2
	nodes := []SubscriptionNode{
		orderTestNode("proxy_path:1", "a", "g", "inbound:1", 1, 1, "JP", "US", &two),
		orderTestNode("proxy_path:2", "b", "g", "inbound:1", 1, 1, "JP", "SG", nil),
		orderTestNode("proxy_path:3", "c", "g", "inbound:2", 2, 2, "SG", "JP", &zero),
		orderTestNode("proxy_path:4", "d", "g", "inbound:3", 3, 3, "DE", "DE", &one),
		orderTestNode("proxy_path:5", "e", "g", "inbound:4", 4, 4, "JP", "US", nil),
		orderTestNode("external_outbound:9", "f", "g", "", 0, 0, "", "HK", nil),
	}
	expected := keysOf(OrderSubscriptionNodes(nodes, policy))
	rng := rand.New(rand.NewSource(42))
	for round := 0; round < 100; round++ {
		shuffled := append([]SubscriptionNode(nil), nodes...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got := keysOf(OrderSubscriptionNodes(shuffled, policy))
		if !equalStrings(got, expected) {
			t.Fatalf("round %d order = %v, want %v", round, got, expected)
		}
	}
}

func TestValidateSubscriptionNodeOrderPolicy(t *testing.T) {
	policy, err := ValidateSubscriptionNodeOrderPolicy(model.SubscriptionNodeOrderPolicy{
		Mode:            model.SubscriptionNodeOrderManual,
		ManualSeed:      model.SubscriptionNodeOrderEntry,
		ExitRegionOrder: []string{"jp", "us"},
		EntryOrder:      []string{"inbound:2", "inbound:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.ExitRegionOrder) != 2 || policy.ExitRegionOrder[0] != "JP" || policy.ExitRegionOrder[1] != "US" {
		t.Fatalf("region normalization = %v", policy.ExitRegionOrder)
	}
	if policy.EntryRegionOrderMode != model.SubscriptionNodeEntryRegionOrderInheritExit {
		t.Fatalf("entry region mode = %q", policy.EntryRegionOrderMode)
	}
	if _, err := ValidateSubscriptionNodeOrderPolicy(model.SubscriptionNodeOrderPolicy{Mode: "bogus"}); err == nil {
		t.Fatal("bogus mode accepted")
	}
	if _, err := ValidateSubscriptionNodeOrderPolicy(model.SubscriptionNodeOrderPolicy{Mode: model.SubscriptionNodeOrderExitRegion, ExitRegionOrder: []string{"not-a-code"}}); err == nil {
		t.Fatal("invalid region accepted")
	}
	if _, err := ValidateSubscriptionNodeOrderPolicy(model.SubscriptionNodeOrderPolicy{Mode: model.SubscriptionNodeOrderExitRegion, ExitRegionOrder: []string{"JP", "jp"}}); err == nil {
		t.Fatal("duplicate region accepted")
	}
	if _, err := ValidateSubscriptionNodeOrderPolicy(model.SubscriptionNodeOrderPolicy{Mode: model.SubscriptionNodeOrderEntry, EntryOrder: []string{"proxy_path:1"}}); err == nil {
		t.Fatal("non-inbound entry key accepted")
	}
}

func TestResolveEffectiveNodeName(t *testing.T) {
	global := "日本 01"
	plan := "VIP 日本"
	if got := ResolveEffectiveNodeName("Tokyo-01", nil, nil); got != "Tokyo-01" {
		t.Fatalf("source only = %q", got)
	}
	if got := ResolveEffectiveNodeName("Tokyo-01", &global, nil); got != global {
		t.Fatalf("global = %q", got)
	}
	if got := ResolveEffectiveNodeName("Tokyo-01", &global, &plan); got != plan {
		t.Fatalf("plan = %q", got)
	}
	if got := ResolveEffectiveNodeName("Tokyo-01", &global, nil); got != "日本 01" {
		t.Fatalf("cleared plan must inherit latest global = %q", got)
	}
}

func TestAutoPlaceNewManualNodesPreservesExistingOrder(t *testing.T) {
	policy := model.NewSubscriptionNodeOrderPolicy()
	policy.Mode = model.SubscriptionNodeOrderManual
	policy.ManualSeed = model.SubscriptionNodeOrderExitRegion
	policy.ExitRegionOrder = []string{"JP", "KR", "HK", "SG", "US"}
	existing := []SubscriptionNode{
		orderTestNode("US-01", "US-01", "", "inbound:1", 1, 1, "US", "US", nil),
		orderTestNode("JP-01", "JP-01", "", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("JP-02", "JP-02", "", "inbound:1", 1, 1, "JP", "JP", nil),
		orderTestNode("HK-01", "HK-01", "", "inbound:1", 1, 1, "HK", "HK", nil),
		orderTestNode("SG-01", "SG-01", "", "inbound:1", 1, 1, "SG", "SG", nil),
	}
	added := []SubscriptionNode{
		orderTestNode("SG-02", "SG-02", "", "inbound:1", 1, 1, "SG", "SG", nil),
		orderTestNode("KR-01", "KR-01", "", "inbound:1", 1, 1, "KR", "KR", nil),
		orderTestNode("JP-03", "JP-03", "", "inbound:1", 1, 1, "JP", "JP", nil),
	}
	result := AutoPlaceNewManualNodes(existing, added, policy)
	want := []string{"US-01", "JP-01", "JP-02", "JP-03", "KR-01", "HK-01", "SG-01", "SG-02"}
	if !equalStrings(result.OrderedNodeKeys, want) {
		t.Fatalf("auto placement = %v, want %v", result.OrderedNodeKeys, want)
	}
	existingAfter := []string{}
	existingKeys := map[string]bool{}
	for _, node := range existing {
		existingKeys[node.Key] = true
	}
	for _, key := range result.OrderedNodeKeys {
		if existingKeys[key] {
			existingAfter = append(existingAfter, key)
		}
	}
	if !equalStrings(existingAfter, keysOf(existing)) {
		t.Fatalf("existing relative order changed: %v", existingAfter)
	}
}

func TestAutoPlaceNewManualNodesPlacementPolicies(t *testing.T) {
	base := model.NewSubscriptionNodeOrderPolicy()
	base.Mode = model.SubscriptionNodeOrderManual
	base.ExitRegionOrder = []string{"JP"}
	existing := []SubscriptionNode{orderTestNode("old", "old", "", "inbound:1", 1, 1, "JP", "JP", nil)}
	added := []SubscriptionNode{orderTestNode("new", "new", "", "", 0, 0, "", "", nil)}

	appendPolicy := base
	appendPolicy.NewNodePlacement = model.SubscriptionNodePlacementAppend
	if got := AutoPlaceNewManualNodes(existing, added, appendPolicy); !equalStrings(got.OrderedNodeKeys, []string{"old", "new"}) || len(got.PendingNodeKeys) != 0 {
		t.Fatalf("append result = %#v", got)
	}
	pendingPolicy := base
	pendingPolicy.NewNodePlacement = model.SubscriptionNodePlacementPending
	if got := AutoPlaceNewManualNodes(existing, added, pendingPolicy); !equalStrings(got.OrderedNodeKeys, []string{"old"}) || !equalStrings(got.PendingNodeKeys, []string{"new"}) {
		t.Fatalf("pending result = %#v", got)
	}
	unmatchedPolicy := base
	unmatchedPolicy.NewNodePlacement = model.SubscriptionNodePlacementByTemplate
	unmatchedPolicy.UnmatchedPlacement = model.SubscriptionNodePlacementPending
	if got := AutoPlaceNewManualNodes(existing, added, unmatchedPolicy); len(got.Warnings) != 1 || !equalStrings(got.PendingNodeKeys, []string{"new"}) {
		t.Fatalf("unmatched result = %#v", got)
	}
}

func TestAutoPlaceNewManualNodesUsesSuccessorAndEntryBuckets(t *testing.T) {
	exitPolicy := model.NewSubscriptionNodeOrderPolicy()
	exitPolicy.Mode = model.SubscriptionNodeOrderManual
	exitPolicy.ManualSeed = model.SubscriptionNodeOrderExitRegion
	exitPolicy.ExitRegionOrder = []string{"JP", "HK"}
	existing := []SubscriptionNode{orderTestNode("hk", "hk", "", "inbound:2", 2, 2, "HK", "HK", nil)}
	added := []SubscriptionNode{orderTestNode("jp", "jp", "", "inbound:1", 1, 1, "JP", "JP", nil)}
	if got := AutoPlaceNewManualNodes(existing, added, exitPolicy); !equalStrings(got.OrderedNodeKeys, []string{"jp", "hk"}) || got.Details[0].Reason != "insert_before_successor_bucket" {
		t.Fatalf("successor insertion = %#v", got)
	}

	entryPolicy := model.NewSubscriptionNodeOrderPolicy()
	entryPolicy.Mode = model.SubscriptionNodeOrderManual
	entryPolicy.ManualSeed = model.SubscriptionNodeOrderEntry
	entryPolicy.EntryRegionOrderMode = model.SubscriptionNodeEntryRegionOrderCustom
	entryPolicy.EntryRegionOrder = []string{"JP", "HK"}
	existing = []SubscriptionNode{
		orderTestNode("hk", "hk", "", "inbound:3", 3, 3, "HK", "HK", nil),
		orderTestNode("jp-a", "jp-a", "", "inbound:1", 1, 1, "JP", "JP", nil),
	}
	added = []SubscriptionNode{orderTestNode("jp-new", "jp-new", "", "inbound:2", 2, 2, "JP", "JP", nil)}
	if got := AutoPlaceNewManualNodes(existing, added, entryPolicy); !equalStrings(got.OrderedNodeKeys, []string{"hk", "jp-a", "jp-new"}) {
		t.Fatalf("entry-region fallback insertion = %#v", got)
	}
}

func TestValidateLegacyManualPlacementDefaultsPending(t *testing.T) {
	policy, err := ValidateSubscriptionNodeOrderPolicy(model.SubscriptionNodeOrderPolicy{Version: 1, Mode: model.SubscriptionNodeOrderManual})
	if err != nil {
		t.Fatal(err)
	}
	if policy.NewNodePlacement != model.SubscriptionNodePlacementPending || policy.UnmatchedPlacement != model.SubscriptionNodePlacementPending {
		t.Fatalf("legacy placement defaults = %#v", policy)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
