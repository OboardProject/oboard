package core

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func testFilterNode(key, name, protocol, exitRegion string) SubscriptionNode {
	raw := map[string]any{}
	if protocol != "" {
		raw["type"] = protocol
	}
	return SubscriptionNode{Key: key, Name: name, ExitRegion: exitRegion, Raw: raw}
}

func TestStripSubscriptionNodeRegionFlag(t *testing.T) {
	flag := func(code string) string { return RegionFlagEmoji(code) }
	cases := map[string]string{
		"":                          "",
		"香港 01":                     "香港 01",
		flag("HK") + " 香港 01":       "香港 01",
		flag("HK") + "香港 01":        "香港 01",
		flag("AQ") + " 未解析":         "未解析",
		flag("US"):                   "",
		flag("US") + " 东京":          "东京",
		flag("US") + flag("JP") + " 双旗": flag("JP") + " 双旗",
	}
	for input, want := range cases {
		if got := StripSubscriptionNodeRegionFlag(input); got != want {
			t.Fatalf("StripSubscriptionNodeRegionFlag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeSubscriptionOutputFilters(t *testing.T) {
	known := map[int64]bool{1: true, 2: true}
	valid := []model.SubscriptionOutputFilter{
		{Type: model.SubscriptionOutputFilterKeepName, Value: "  ^东京|香港$ "},
		{Type: model.SubscriptionOutputFilterDropProtocol, Value: "SOCKS5"},
		{Type: model.SubscriptionOutputFilterKeepRegion, Value: " jp "},
		{Type: model.SubscriptionOutputFilterDropGroup, Value: "2"},
	}
	normalized, err := NormalizeSubscriptionOutputFilters(valid, known)
	if err != nil {
		t.Fatal(err)
	}
	if normalized[1].Value != "socks5" || normalized[2].Value != "JP" {
		t.Fatalf("normalization failed: %#v", normalized)
	}
	badRegex := []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepName, Value: "(unclosed"}}
	if _, err := NormalizeSubscriptionOutputFilters(badRegex, known); err == nil || !strings.Contains(err.Error(), "regular expression") {
		t.Fatalf("invalid regex accepted: %v", err)
	}
	badType := []model.SubscriptionOutputFilter{{Type: "keep_all", Value: "x"}}
	if _, err := NormalizeSubscriptionOutputFilters(badType, known); err == nil {
		t.Fatal("unknown filter type accepted")
	}
	badProtocol := []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropProtocol, Value: "wireguard"}}
	if _, err := NormalizeSubscriptionOutputFilters(badProtocol, known); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	badRegion := []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepRegion, Value: "JPN"}}
	if _, err := NormalizeSubscriptionOutputFilters(badRegion, known); err == nil {
		t.Fatal("invalid region accepted")
	}
	badGroup := []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepGroup, Value: "99"}}
	if _, err := NormalizeSubscriptionOutputFilters(badGroup, known); err == nil || !strings.Contains(err.Error(), "node group") {
		t.Fatalf("unknown group accepted: %v", err)
	}
	emptyValue := []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropName, Value: "  "}}
	if _, err := NormalizeSubscriptionOutputFilters(emptyValue, known); err == nil {
		t.Fatal("empty value accepted")
	}
	tooLong := []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropName, Value: strings.Repeat("a", 257)}}
	if _, err := NormalizeSubscriptionOutputFilters(tooLong, known); err == nil {
		t.Fatal("overlong value accepted")
	}
	tooMany := make([]model.SubscriptionOutputFilter, subscriptionOutputMaxFilters+1)
	for i := range tooMany {
		tooMany[i] = model.SubscriptionOutputFilter{Type: model.SubscriptionOutputFilterDropProtocol, Value: "ssh"}
	}
	if _, err := NormalizeSubscriptionOutputFilters(tooMany, known); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("overlong rule list accepted: %v", err)
	}
}

func TestApplySubscriptionOutputFiltersPipeline(t *testing.T) {
	nodes := []SubscriptionNode{
		testFilterNode("private:1", "🇭🇰 香港 01", "shadowsocks", ""),
		testFilterNode("private:2", "🇯🇵 东京 GIA", "trojan", ""),
		testFilterNode("private:3", "🇸🇬 新加坡 01", "vless", ""),
		testFilterNode("proxy_path:4", "🇺🇸 洛杉矶", "vless", "US"),
		testFilterNode("external:5", "🇩🇪 法兰克福", "hysteria2", "DE"),
	}
	groupByNodeKey := map[string]int64{"private:1": 1, "private:2": 2, "private:3": 1, "proxy_path:4": 3, "external:5": 3}

	// keep_name matches the base name without the region flag; the flag prefix
	// must not break "香港" or anchored patterns.
	kept, stats := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepName, Value: "^香港|^东京"}}, groupByNodeKey)
	if len(kept) != 2 || kept[0].Key != "private:1" || kept[1].Key != "private:2" {
		t.Fatalf("keep_name kept %#v", keys(kept))
	}
	if stats.TotalDropped != 3 || stats.Remaining != 2 || len(stats.Rules) != 1 || stats.Rules[0].Matched != 2 || stats.Rules[0].Dropped != 3 {
		t.Fatalf("keep_name stats = %#v", stats)
	}

	// drop_name removes matching nodes.
	dropped, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropName, Value: "GIA"}}, groupByNodeKey)
	if len(dropped) != 4 || dropped[0].Key != "private:1" {
		t.Fatalf("drop_name dropped %#v", keys(dropped))
	}

	// keep_protocol / drop_protocol.
	onlyVless, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepProtocol, Value: "vless"}}, groupByNodeKey)
	if len(onlyVless) != 2 {
		t.Fatalf("keep_protocol kept %#v", keys(onlyVless))
	}
	noTrojan, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropProtocol, Value: "trojan"}}, groupByNodeKey)
	if len(noTrojan) != 4 {
		t.Fatalf("drop_protocol kept %#v", keys(noTrojan))
	}

	// keep_region keeps only region-capable nodes matching; nodes without a
	// region never match and are dropped.
	usOnly, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepRegion, Value: "US"}}, groupByNodeKey)
	if len(usOnly) != 1 || usOnly[0].Key != "proxy_path:4" {
		t.Fatalf("keep_region kept %#v", keys(usOnly))
	}
	// drop_region drops matching region nodes; region-less nodes survive.
	noUS, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropRegion, Value: "US"}}, groupByNodeKey)
	if len(noUS) != 4 {
		t.Fatalf("drop_region kept %#v", keys(noUS))
	}

	// keep_group / drop_group by node group id.
	groupOne, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepGroup, Value: "1"}}, groupByNodeKey)
	if len(groupOne) != 2 || groupOne[0].Key != "private:1" || groupOne[1].Key != "private:3" {
		t.Fatalf("keep_group kept %#v", keys(groupOne))
	}
	noGroupThree, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropGroup, Value: "3"}}, groupByNodeKey)
	if len(noGroupThree) != 3 {
		t.Fatalf("drop_group kept %#v", keys(noGroupThree))
	}

	// Pipeline order: a node dropped by an earlier rule is never resurrected
	// by a later keep rule (private:2 dropped by rule 1 stays gone even though
	// rule 2's keep_name would match it).
	pipeline, stats := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{
		{Type: model.SubscriptionOutputFilterDropProtocol, Value: "trojan"},
		{Type: model.SubscriptionOutputFilterKeepName, Value: "香港|东京"},
		{Type: model.SubscriptionOutputFilterDropRegion, Value: "US"},
	}, groupByNodeKey)
	if len(pipeline) != 1 || pipeline[0].Key != "private:1" {
		t.Fatalf("pipeline kept %#v", keys(pipeline))
	}
	if stats.Rules[0].Matched != 1 || stats.Rules[0].Dropped != 1 || stats.Rules[1].Matched != 1 || stats.Rules[1].Dropped != 3 || stats.Rules[2].Matched != 0 {
		t.Fatalf("pipeline stats = %#v", stats.Rules)
	}

	// Empty filters pass everything through.
	all, stats := ApplySubscriptionOutputFilters(nodes, nil, groupByNodeKey)
	if len(all) != len(nodes) || stats.TotalDropped != 0 || stats.Remaining != len(nodes) {
		t.Fatalf("empty filters changed the list")
	}

	// Empty node list never executes rules.
	empty, stats := ApplySubscriptionOutputFilters(nil, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepName, Value: "x"}}, nil)
	if empty != nil || stats.Remaining != 0 || len(stats.Rules) != 1 || stats.Rules[0].SkipReason == "" {
		t.Fatalf("empty list stats = %#v", stats)
	}

	// A missing group mapping makes group rules match nothing.
	noMapping, _ := ApplySubscriptionOutputFilters(nodes, []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterDropGroup, Value: "1"}}, nil)
	if len(noMapping) != len(nodes) {
		t.Fatalf("group rule without mapping dropped nodes: %#v", keys(noMapping))
	}
}

func keys(nodes []SubscriptionNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.Key)
	}
	return out
}

func TestSortSubscriptionOutputFiltersForDigest(t *testing.T) {
	filters := []model.SubscriptionOutputFilter{
		{Type: model.SubscriptionOutputFilterDropName, Value: "b"},
		{Type: model.SubscriptionOutputFilterKeepName, Value: "a"},
		{Type: model.SubscriptionOutputFilterDropName, Value: "a"},
	}
	sorted := SortSubscriptionOutputFiltersForDigest(filters)
	if sorted[0].Value != "a" || sorted[1].Value != "b" || sorted[2].Type != model.SubscriptionOutputFilterKeepName {
		t.Fatalf("digest sort = %#v", sorted)
	}
	if filters[0].Value != "b" {
		t.Fatal("digest sort mutated the input")
	}
}