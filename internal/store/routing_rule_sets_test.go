package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestRoutingRuleScopeMigrationFromPreviousTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routing-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "legacy", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	rule := &model.RoutingRule{ServerID: server.ID, Scope: model.RoutingRuleScopeServer, MatchSource: model.RoutingMatchSourceInline, Name: "legacy-rule", Priority: 20, MatchJSON: `{"domain":["example.com"]}`, Action: model.RouteActionDirect, Enabled: true}
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
		`drop index idx_routing_rules_sync_group`,
		`drop index idx_routing_rules_target_path`,
		`drop index idx_routing_rules_stage_order`,
		`drop index idx_routing_rules_rule_set`,
		`alter table routing_rules drop column sync_group_id`,
		`alter table routing_rules drop column target_proxy_path_id`,
		`alter table routing_rules drop column rule_set_id`,
		`alter table routing_rules drop column match_source`,
		`alter table routing_rules drop column sort_position`,
		`alter table routing_rules drop column stage_step_id`,
		`alter table routing_rules drop column proxy_path_id`,
		`alter table routing_rules drop column scope`,
		`drop table routing_rule_sets`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous routing schema with %q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrated, err := db.GetRoutingRule(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Scope != model.RoutingRuleScopeServer || migrated.MatchSource != model.RoutingMatchSourceInline || migrated.ProxyPathID != nil || migrated.StageStepID != nil || migrated.RuleSetID != nil {
		t.Fatalf("legacy routing rule was not migrated to server scope: %#v", migrated)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration is not idempotent: %v", err)
	}
}

func TestRoutingRuleChainAndSyncColumnsMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routing-chain-sync-migration.sqlite")
	db, err := Open(path)
	if err != nil {
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
		`drop index idx_routing_rules_sync_group`,
		`drop index idx_routing_rules_target_path`,
		`alter table routing_rules drop column sync_group_id`,
		`alter table routing_rules drop column target_proxy_path_id`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous routing schema with %q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, column := range []string{"target_proxy_path_id", "sync_group_id"} {
		var count int
		if err := db.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('routing_rules') where name=?`, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("routing_rules.%s migration count=%d err=%v", column, count, err)
		}
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration is not idempotent: %v", err)
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
