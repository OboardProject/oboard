package core

import (
	"reflect"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func membershipCatalogForTest() []AssignableNode {
	return []AssignableNode{
		{Type: model.AssignableNodeProxyPath, ID: 1, Key: "proxy_path:1", Enabled: true, EntryKey: "inbound:11", EntryServerID: 1, ExitServerID: 3, ExitRegion: "JP", PathServers: []AssignableNodeServerRole{{ServerID: 2}}},
		{Type: model.AssignableNodeProxyPath, ID: 2, Key: "proxy_path:2", Enabled: true, EntryKey: "inbound:12", EntryServerID: 1, ExitServerID: 4, ExitRegion: "HK"},
		{Type: model.AssignableNodeExternalOutbound, ID: 8, Key: "external_outbound:8", Enabled: true, ExitExternalOutboundID: 8, ExitRegion: "US"},
		{Type: model.AssignableNodeProxyPath, ID: 9, Key: "proxy_path:9", Enabled: false, EntryKey: "inbound:11", EntryServerID: 1, ExitRegion: "JP"},
	}
}

func TestNormalizePlanMembershipRulesStableScopes(t *testing.T) {
	rules := []model.PlanMembershipRule{
		{RuleID: 6, Kind: PlanRuleExternalOutbound, ScopeKey: "8"},
		{RuleID: 1, Kind: PlanRuleEntryInbound, ScopeKey: "inbound:11"},
		{RuleID: 2, Kind: PlanRuleEntryServer, ScopeKey: "1"},
		{RuleID: 3, Kind: PlanRulePathServer, ScopeKey: "2"},
		{RuleID: 4, Kind: PlanRuleExitServer, ScopeKey: "3"},
		{RuleID: 5, Kind: PlanRuleExitRegion, ScopeKey: " jp "},
	}
	got, err := NormalizePlanMembershipRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].RuleID != 1 || got[4].ScopeKey != "JP" {
		t.Fatalf("normalized rules = %#v", got)
	}
	bad := []model.PlanMembershipRule{{RuleID: 1, Kind: PlanRuleEntryInbound, ScopeKey: "Tokyo"}}
	if _, err := NormalizePlanMembershipRules(bad); err == nil {
		t.Fatal("display name must not be accepted as an inbound identity")
	}
}

func TestPlanMembershipRuleMatchesEveryStableScope(t *testing.T) {
	node := membershipCatalogForTest()[0]
	cases := []model.PlanMembershipRule{
		{Kind: PlanRuleEntryInbound, ScopeKey: "inbound:11"},
		{Kind: PlanRuleEntryServer, ScopeKey: "1"},
		{Kind: PlanRulePathServer, ScopeKey: "2"},
		{Kind: PlanRuleExitServer, ScopeKey: "3"},
		{Kind: PlanRuleExitRegion, ScopeKey: "JP"},
	}
	for _, rule := range cases {
		if !planMembershipRuleMatches(rule, node) {
			t.Errorf("rule did not match: %#v", rule)
		}
	}
	external := membershipCatalogForTest()[2]
	if !planMembershipRuleMatches(model.PlanMembershipRule{Kind: PlanRuleExternalOutbound, ScopeKey: "8"}, external) {
		t.Fatal("external outbound stable id did not match")
	}
}

func TestResolvePlanMembershipRulesUnionExplicitAndExclusion(t *testing.T) {
	base := []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeProxyPath, NodeID: 2, SourceType: model.PlanNodeSourceExplicit},
		{NodeType: model.AssignableNodeProxyPath, NodeID: 1, SourceType: model.PlanNodeSourceRule, SourceRuleID: 9},
	}
	rules := []model.PlanMembershipRule{
		{RuleID: 2, Kind: PlanRuleExitRegion, ScopeKey: "US"},
		{RuleID: 1, Kind: PlanRuleExitRegion, ScopeKey: "JP"},
		{RuleID: 3, Kind: PlanRuleEntryServer, ScopeKey: "1"},
	}
	exclusions := []model.PlanNodeExclusion{{NodeType: model.AssignableNodeProxyPath, NodeID: 2}}
	got, err := ResolvePlanMembershipRules(base, rules, exclusions, membershipCatalogForTest())
	if err != nil {
		t.Fatal(err)
	}
	keys := sortedNodeKeys(got.Nodes)
	if !reflect.DeepEqual(keys, []string{"external_outbound:8", "proxy_path:1"}) {
		t.Fatalf("resolved keys = %#v", keys)
	}
	for _, node := range got.Nodes {
		if node.NodeID == 1 && (node.SourceType != model.PlanNodeSourceRule || node.SourceRuleID != 1) {
			t.Fatalf("lowest matching rule must own node: %#v", node)
		}
	}
	if got.MatchedBy["proxy_path:1"][0] != 1 || len(got.MatchedBy["proxy_path:1"]) != 2 {
		t.Fatalf("union matches = %#v", got.MatchedBy)
	}
	if len(got.AddedKeys) != 1 || got.AddedKeys[0] != "external_outbound:8" {
		t.Fatalf("added = %#v", got.AddedKeys)
	}
	if len(got.RemovedKeys) != 1 || got.RemovedKeys[0] != "proxy_path:2" {
		t.Fatalf("removed = %#v", got.RemovedKeys)
	}
}

func TestResolvePlanMembershipRulesOfflineStableDisabledRemoved(t *testing.T) {
	catalog := membershipCatalogForTest()
	catalog[0].Status = AssignableNodeStatusOffline
	rules := []model.PlanMembershipRule{{RuleID: 1, Kind: PlanRuleExitRegion, ScopeKey: "JP"}, {RuleID: 2, Kind: PlanRuleExitRegion, ScopeKey: "ZZ"}}
	got, err := ResolvePlanMembershipRules(nil, rules, nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if keys := sortedNodeKeys(got.Nodes); !reflect.DeepEqual(keys, []string{"proxy_path:1"}) {
		t.Fatalf("offline/disabled resolution = %#v", keys)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("no-match warnings = %#v", got.Warnings)
	}
	catalog[0].Enabled = false
	got, err = ResolvePlanMembershipRules(got.Nodes, rules, nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 0 || len(got.RemovedKeys) != 1 {
		t.Fatalf("disabled resource must be removed: %#v", got)
	}
}
