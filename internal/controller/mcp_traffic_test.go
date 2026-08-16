package controller

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func trafficAutomationPrincipal(t *testing.T, server *Server, username string) application.Principal {
	t.Helper()
	ctx := context.Background()
	user, err := server.store.GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	return application.HumanPrincipal(*user, model.RoleOperator, netip.MustParseAddr("127.0.0.1"))
}

func TestOutboundAndRoutingRuleCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	operator := &model.User{Username: "operator", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, operator); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "entry", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := trafficAutomationPrincipal(t, server, "operator")

	createInput := json.RawMessage(`{"outbound":{"server_id":1,"name":"跳板出口","protocol":"shadowsocks","target_address":"203.0.113.20","target_port":8388,"config_json":"{\"method\":\"2022-blake3-aes-128-gcm\",\"password\":\"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\"}","enabled":true}}`)
	applyAutomationChangeset(t, server, principal, "outbound-create", automation.OperationRequest{Capability: "outbounds.create", Input: createInput})
	outbounds, err := db.ListOutbounds(ctx)
	if err != nil || len(outbounds) != 1 {
		t.Fatalf("outbounds=%#v err=%v", outbounds, err)
	}
	outbound := outbounds[0]
	if outbound.Name != "跳板出口" || outbound.TargetPort != 8388 || !outbound.Enabled {
		t.Fatalf("unexpected outbound: %#v", outbound)
	}
	ruleInput := json.RawMessage(`{"routing_rule":{"server_id":1,"name":"动态 IPv6 出口","priority":100,"action":"source_prefix","source_prefix":"2001:db8:66::9/64","match_json":"{\"domain_suffix\":[\"netflix.com\"]}","enabled":true}}`)
	applyAutomationChangeset(t, server, principal, "routing-create", automation.OperationRequest{Capability: "routing_rules.create", Input: ruleInput})
	rules, err := db.ListRoutingRules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules=%#v err=%v", rules, err)
	}
	rule := rules[0]
	if rule.Action != model.RouteActionSourcePrefix || rule.SourcePrefix != "2001:db8:66::/64" || rule.Priority != 100 {
		t.Fatalf("unexpected rule: %#v", rule)
	}
	updateInput, _ := json.Marshal(map[string]any{"routing_rule_id": rule.ID, "changes": map[string]any{"priority": 200}})
	applyAutomationChangeset(t, server, principal, "routing-update", automation.OperationRequest{Capability: "routing_rules.update", Input: updateInput})
	updated, err := db.GetRoutingRule(ctx, rule.ID)
	if err != nil || updated.Priority != 200 {
		t.Fatalf("rule not updated: %#v err=%v", updated, err)
	}
	listed, err := server.application.Query(ctx, principal, "routing_rules.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("routing_rules.list: %v", err)
	}
	assertCapabilityOutputSchema(t, server, "routing_rules.list", listed)
	encoded, _ := json.Marshal(listed)
	if contains(encoded, `"match_json"`) || contains(encoded, `"sync_source_rule_id"`) || !contains(encoded, `"match_configured":true`) || !contains(encoded, `"revision"`) {
		t.Fatalf("routing_rules.list returned a non-public view: %s", encoded)
	}
	deleteInput, _ := json.Marshal(map[string]any{"routing_rule_id": rule.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "routing-delete", automation.OperationRequest{Capability: "routing_rules.delete", Input: deleteInput})
	if _, err := db.GetRoutingRule(ctx, rule.ID); err == nil {
		t.Fatal("deleted rule still exists")
	}
	deleteOutbound, _ := json.Marshal(map[string]any{"outbound_id": outbound.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "outbound-delete", automation.OperationRequest{Capability: "outbounds.delete", Input: deleteOutbound})
	if _, err := db.GetOutbound(ctx, outbound.ID); err == nil {
		t.Fatal("deleted outbound still exists")
	}
}

func TestRoutingRuleSetCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	operator := &model.User{Username: "operator", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, operator); err != nil {
		t.Fatal(err)
	}
	revision := "revision-1"
	server.routingRuleSetFetcher = func(context.Context, model.RoutingRuleSet, bool) (*fetchedRoutingRuleSet, error) {
		return &fetchedRoutingRuleSet{content: []byte(`{"version":1,"rules":[{"domain":["example.com"]}]}`), revision: revision, etag: "private-etag", lastModified: "private-last-modified"}, nil
	}
	principal := trafficAutomationPrincipal(t, server, "operator")
	createInput := json.RawMessage(`{"routing_rule_set":{"name":"shared","url":"https://rules.example/shared.json","format":"singbox_source"}}`)
	applyAutomationChangeset(t, server, principal, "routing-rule-set-create", automation.OperationRequest{Capability: "routing_rule_sets.create", Input: createInput})
	items, err := db.ListRoutingRuleSets(ctx)
	if err != nil || len(items) != 1 || items[0].Revision != revision {
		t.Fatalf("created routing rule sets=%#v error=%v", items, err)
	}
	item := items[0]
	updateInput, _ := json.Marshal(map[string]any{"routing_rule_set_id": item.ID, "changes": map[string]any{"name": "shared-renamed"}})
	applyAutomationChangeset(t, server, principal, "routing-rule-set-update", automation.OperationRequest{Capability: "routing_rule_sets.update", Input: updateInput})
	updated, err := db.GetRoutingRuleSet(ctx, item.ID)
	if err != nil || updated.Name != "shared-renamed" {
		t.Fatalf("updated routing rule set=%#v error=%v", updated, err)
	}
	revision = "revision-2"
	refreshInput, _ := json.Marshal(map[string]any{"routing_rule_set_id": item.ID})
	applyAutomationChangeset(t, server, principal, "routing-rule-set-refresh", automation.OperationRequest{Capability: "routing_rule_sets.refresh", Input: refreshInput})
	refreshed, err := db.GetRoutingRuleSet(ctx, item.ID)
	if err != nil || refreshed.Revision != revision {
		t.Fatalf("refreshed routing rule set=%#v error=%v", refreshed, err)
	}
	listed, err := server.application.Query(ctx, principal, "routing_rule_sets.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("routing_rule_sets.list: %v", err)
	}
	encoded, _ := json.Marshal(listed)
	assertCapabilityOutputSchema(t, server, "routing_rule_sets.list", listed)
	if contains(encoded, `"content"`) || contains(encoded, `"etag"`) || contains(encoded, `"last_modified"`) {
		t.Fatalf("routing_rule_sets.list leaked internal fetch state: %s", encoded)
	}
	deleteInput, _ := json.Marshal(map[string]any{"routing_rule_set_id": item.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "routing-rule-set-delete", automation.OperationRequest{Capability: "routing_rule_sets.delete", Input: deleteInput})
	if _, err := db.GetRoutingRuleSet(ctx, item.ID); err == nil {
		t.Fatal("deleted routing rule set still exists")
	}
}

func TestRoutingRuleCapabilityCreatesSynchronizedStageReuse(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := t.Context()
	operator := &model.User{Username: "operator-sync", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, operator); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "entry", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindDirect, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "direct-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	principal := trafficAutomationPrincipal(t, server, operator.Username)
	createSource, _ := json.Marshal(map[string]any{"routing_rule": map[string]any{
		"scope": model.RoutingRuleScopePathStage, "proxy_path_id": path.ID, "sort_position": 0,
		"name": "shared-match", "match_json": `{"domain_suffix":["example.com"]}`, "action": model.RouteActionDirect, "enabled": true,
	}})
	applyAutomationChangeset(t, server, principal, "routing-sync-source", automation.OperationRequest{Capability: "routing_rules.create", Input: createSource})
	rules, err := db.ListRoutingRules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("source rules=%#v err=%v", rules, err)
	}
	source := rules[0]
	createReuse, _ := json.Marshal(map[string]any{"routing_rule": map[string]any{
		"scope": model.RoutingRuleScopePathStage, "proxy_path_id": path.ID, "sort_position": 1,
		"sync_source_rule_id": source.ID, "sync_enabled": true, "action": model.RouteActionBlock, "enabled": true,
	}})
	applyAutomationChangeset(t, server, principal, "routing-sync-copy", automation.OperationRequest{Capability: "routing_rules.create", Input: createReuse})
	rules, err = db.ListRoutingRules(ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("synchronized rules=%#v err=%v", rules, err)
	}
	var synced model.RoutingRule
	for _, rule := range rules {
		if rule.ID != source.ID {
			synced = rule
		}
	}
	if synced.Name != source.Name || synced.MatchJSON != source.MatchJSON || synced.Action != model.RouteActionBlock || synced.SyncGroupID == "" {
		t.Fatalf("unexpected synchronized copy: %#v", synced)
	}
	updateSource, _ := json.Marshal(map[string]any{"routing_rule_id": source.ID, "changes": map[string]any{"name": "shared-updated", "match_json": `{"domain_suffix":["updated.example"]}`}})
	applyAutomationChangeset(t, server, principal, "routing-sync-update", automation.OperationRequest{Capability: "routing_rules.update", Input: updateSource})
	synced, err = func() (model.RoutingRule, error) {
		item, err := db.GetRoutingRule(ctx, synced.ID)
		if err != nil {
			return model.RoutingRule{}, err
		}
		return *item, nil
	}()
	if err != nil || synced.Name != "shared-updated" || !strings.Contains(synced.MatchJSON, "updated.example") || synced.Action != model.RouteActionBlock {
		t.Fatalf("synchronized MCP update=%#v err=%v", synced, err)
	}
}

func TestExternalOutboundImportCapability(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	operator := &model.User{Username: "operator", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, operator); err != nil {
		t.Fatal(err)
	}
	principal := trafficAutomationPrincipal(t, server, "operator")
	// ss:// URI with base64 method:password + address
	content := "ss://MjAyMi1ibGFrZTMtYWVzLTEyOC1nY206QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQQ@203.0.113.99:8443#JP-Node"
	input, _ := json.Marshal(map[string]any{"content": content, "expose_to_users": true})
	applyAutomationChangeset(t, server, principal, "external-import", automation.OperationRequest{Capability: "external_outbounds.import", Input: input})
	items, err := db.ListExternalOutbounds(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("external outbounds=%#v err=%v", items, err)
	}
	if items[0].Protocol != model.ProtocolSS || !items[0].ExposeToUsers || !items[0].Enabled {
		t.Fatalf("unexpected imported outbound: %#v", items[0])
	}
	payload, err := server.application.Query(ctx, principal, "external_outbounds.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("external_outbounds.list: %v", err)
	}
	encoded, _ := json.Marshal(payload)
	if contains(encoded, "config_json") {
		t.Fatalf("external_outbounds.list leaked config_json: %s", encoded)
	}
}

func contains(raw []byte, needle string) bool {
	return len(raw) > 0 && string(raw) != "" && bytesContains(raw, needle)
}

func bytesContains(raw []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(raw); i++ {
		if string(raw[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}

func assertCapabilityOutputSchema(t *testing.T, server *Server, capabilityName string, value any) {
	t.Helper()
	descriptor, ok := server.capabilities.Get(capabilityName)
	if !ok {
		t.Fatalf("capability %s is not registered", capabilityName)
	}
	var schemaValue any
	if err := json.Unmarshal(descriptor.OutputSchema, &schemaValue); err != nil {
		t.Fatalf("decode %s output schema: %v", capabilityName, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("output-schema.json", schemaValue); err != nil {
		t.Fatalf("load %s output schema: %v", capabilityName, err)
	}
	compiled, err := compiler.Compile("output-schema.json")
	if err != nil {
		t.Fatalf("compile %s output schema: %v", capabilityName, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s output: %v", capabilityName, err)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatalf("normalize %s output: %v", capabilityName, err)
	}
	if err := compiled.Validate(normalized); err != nil {
		t.Fatalf("%s output does not match its catalog schema: %v\n%s", capabilityName, err, encoded)
	}
}
