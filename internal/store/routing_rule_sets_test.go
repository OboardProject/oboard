package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestFamilySplitTemplatesMigrateFromSiblingBranchSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routing-family-split-templates.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := &model.Server{Name: "entry", Status: model.ServerOnline}
	v4 := &model.Server{Name: "v4", Status: model.ServerOnline}
	v6 := &model.Server{Name: "v6", Status: model.ServerOnline}
	for _, server := range []*model.Server{entry, v4, v6} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	inbound := &model.Inbound{ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "::", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	createPath := func(name string, hopServerID int64) model.ProxyPath {
		t.Helper()
		item := model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: name, Enabled: true}
		if err := db.CreateProxyPath(ctx, &item); err != nil {
			t.Fatal(err)
		}
		step := model.ProxyPathStep{PathID: item.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &hopServerID, ConfigJSON: `{}`}
		if err := db.CreateProxyPathStep(ctx, &step); err != nil {
			t.Fatal(err)
		}
		return item
	}
	source := createPath("source", v4.ID)
	ipv4Path := createPath("legacy-v4", v4.ID)
	ipv6Path := createPath("legacy-v6", v6.ID)
	sourceID := source.ID
	rule := &model.RoutingRule{
		ServerID: entry.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &sourceID,
		MatchSource: model.RoutingMatchSourceInline, Name: "StarHub", MatchJSON: `{}`,
		Action: model.RouteActionDirect, Enabled: true,
	}
	if err := db.CreateRoutingRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`alter table routing_rules add column ipv4_target_proxy_path_id integer references proxy_paths(id)`,
		`alter table routing_rules add column ipv6_target_proxy_path_id integer references proxy_paths(id)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare sibling family-split schema with %q: %v", statement, err)
		}
	}
	if _, err := raw.Exec(`update routing_rules set action='family_split', family_split_template_id=null, ipv4_target_proxy_path_id=?, ipv6_target_proxy_path_id=? where id=?`, ipv4Path.ID, ipv6Path.ID, rule.ID); err != nil {
		t.Fatalf("seed sibling family-split rule: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, column := range []string{"ipv4_target_proxy_path_id", "ipv6_target_proxy_path_id"} {
		var count int
		if err := db.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('routing_rules') where name=?`, column).Scan(&count); err != nil || count != 0 {
			t.Fatalf("routing_rules.%s should be dropped, count=%d err=%v", column, count, err)
		}
	}
	migrated, err := db.GetRoutingRule(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.FamilySplitTemplateID == nil || *migrated.FamilySplitTemplateID <= 0 {
		t.Fatalf("legacy family_split was not bound to a template: %#v", migrated)
	}
	templates, err := db.ListFamilySplitTemplates(ctx)
	if err != nil || len(templates) != 1 || templates[0].Name != "StarHub" {
		t.Fatalf("backfilled templates=%#v err=%v", templates, err)
	}
	template := templates[0]
	if template.IPv4PathID == 0 || template.IPv6PathID == 0 || template.IPv4PathID == ipv4Path.ID || template.IPv6PathID == ipv6Path.ID {
		t.Fatalf("template branch paths were not created independently: %#v", template)
	}
	ipv4Steps, err := db.ListProxyPathStepsForPath(ctx, template.IPv4PathID)
	if err != nil || len(ipv4Steps) != 1 || ipv4Steps[0].ServerID == nil || *ipv4Steps[0].ServerID != v4.ID {
		t.Fatalf("copied IPv4 suffix steps=%#v err=%v", ipv4Steps, err)
	}
	ipv6Steps, err := db.ListProxyPathStepsForPath(ctx, template.IPv6PathID)
	if err != nil || len(ipv6Steps) != 1 || ipv6Steps[0].ServerID == nil || *ipv6Steps[0].ServerID != v6.ID {
		t.Fatalf("copied IPv6 suffix steps=%#v err=%v", ipv6Steps, err)
	}
	if _, err := db.GetProxyPath(ctx, ipv4Path.ID); err != nil {
		t.Fatalf("legacy IPv4 sibling path was deleted: %v", err)
	}
	if _, err := db.GetProxyPath(ctx, ipv6Path.ID); err != nil {
		t.Fatalf("legacy IPv6 sibling path was deleted: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeated family-split template migration is not idempotent: %v", err)
	}
}

func TestRoutingRuleSetReuseDeleteProtectionAndAtomicPlacement(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	serverA := &model.Server{Name: "A", Status: model.ServerOnline}
	serverB := &model.Server{Name: "B", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, serverB); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	serverBID := serverB.ID
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &serverBID, ConfigJSON: `{}`}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	set := &model.RoutingRuleSet{Name: "shared", URL: "https://rules.example/list.json", Format: model.RoutingRuleSetFormatSingBoxSource, Content: []byte(`{"version":1,"rules":[{"domain":["example.com"]}]}`), Revision: "revision", Status: model.RoutingRuleSetStatusReady}
	if err := db.CreateRoutingRuleSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	pathID, setID, stepID := path.ID, set.ID, step.ID
	ruleA := &model.RoutingRule{ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID, SortPosition: 0, MatchSource: model.RoutingMatchSourceRuleSet, RuleSetID: &setID, Name: "at-a", MatchJSON: `{}`, Action: model.RouteActionDirect, Enabled: true}
	ruleB := &model.RoutingRule{ServerID: serverB.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID, StageStepID: &stepID, SortPosition: 0, MatchSource: model.RoutingMatchSourceRuleSet, RuleSetID: &setID, Name: "at-b", MatchJSON: `{}`, Action: model.RouteActionBlock, Enabled: true}
	if err := db.CreateRoutingRule(ctx, ruleA); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoutingRule(ctx, ruleB); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRoutingRuleSet(ctx, set.ID); err == nil {
		t.Fatal("referenced routing rule set was deleted")
	}
	placements := []model.RoutingRulePlacement{{RuleID: ruleB.ID, SortPosition: 0}, {RuleID: ruleA.ID, StageStepID: &stepID, SortPosition: 0}}
	if err := db.PlaceRoutingRules(ctx, path.ID, placements); err != nil {
		t.Fatal(err)
	}
	movedA, _ := db.GetRoutingRule(ctx, ruleA.ID)
	movedB, _ := db.GetRoutingRule(ctx, ruleB.ID)
	if movedA.ServerID != serverB.ID || movedA.StageStepID == nil || movedB.ServerID != serverA.ID || movedB.StageStepID != nil {
		t.Fatalf("cross-stage move did not update derived servers atomically: A=%#v B=%#v", movedA, movedB)
	}
	bad := []model.RoutingRulePlacement{{RuleID: ruleA.ID, SortPosition: 2}, {RuleID: ruleB.ID, SortPosition: 0}}
	if err := db.PlaceRoutingRules(ctx, path.ID, bad); err == nil {
		t.Fatal("non-contiguous placement was accepted")
	}
	afterA, _ := db.GetRoutingRule(ctx, ruleA.ID)
	if afterA.ServerID != movedA.ServerID || afterA.StageStepID == nil || *afterA.StageStepID != *movedA.StageStepID || afterA.SortPosition != movedA.SortPosition {
		t.Fatalf("failed placement was not rolled back: before=%#v after=%#v", movedA, afterA)
	}
}

func TestSyncedRoutingRulesShareMatchesButKeepIndependentActions(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "sync", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	source := &model.RoutingRule{ServerID: server.ID, Scope: model.RoutingRuleScopeServer, MatchSource: model.RoutingMatchSourceInline, Name: "streaming", MatchJSON: `{"domain_suffix":["example.com"]}`, Action: model.RouteActionDirect, Enabled: true}
	if err := db.CreateRoutingRule(ctx, source); err != nil {
		t.Fatal(err)
	}
	synced := &model.RoutingRule{ServerID: server.ID, Scope: model.RoutingRuleScopeServer, MatchSource: source.MatchSource, Name: source.Name, MatchJSON: source.MatchJSON, Action: model.RouteActionBlock, Enabled: true}
	if err := db.CreateSyncedRoutingRule(ctx, synced, source.ID, "sync-test-group"); err != nil {
		t.Fatal(err)
	}
	independent := &model.RoutingRule{ServerID: server.ID, Scope: model.RoutingRuleScopeServer, MatchSource: source.MatchSource, Name: source.Name, MatchJSON: source.MatchJSON, Action: model.RouteActionDirect, Enabled: true}
	if err := db.CreateRoutingRule(ctx, independent); err != nil {
		t.Fatal(err)
	}
	synced.Name = "streaming-updated"
	synced.MatchJSON = `{"domain_suffix":["updated.example"]}`
	synced.Action = model.RouteActionBlock
	if err := db.UpdateRoutingRule(ctx, synced); err != nil {
		t.Fatal(err)
	}
	updatedSource, _ := db.GetRoutingRule(ctx, source.ID)
	updatedIndependent, _ := db.GetRoutingRule(ctx, independent.ID)
	if updatedSource.Name != synced.Name || updatedSource.MatchJSON != synced.MatchJSON || updatedSource.Action != model.RouteActionDirect || updatedSource.SyncGroupID == "" {
		t.Fatalf("shared match update did not preserve source action: %#v", updatedSource)
	}
	if updatedIndependent.Name != source.Name || updatedIndependent.MatchJSON != source.MatchJSON || updatedIndependent.SyncGroupID != "" {
		t.Fatalf("one-time copy was changed by sync update: %#v", updatedIndependent)
	}
}
