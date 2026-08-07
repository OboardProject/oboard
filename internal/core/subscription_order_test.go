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
