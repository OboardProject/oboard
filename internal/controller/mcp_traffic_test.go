package controller

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
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
	ruleInput := json.RawMessage(`{"routing_rule":{"server_id":1,"name":"直连网飞","priority":100,"action":"direct","match_json":"{\"domain_suffix\":[\"netflix.com\"]}","enabled":true}}`)
	applyAutomationChangeset(t, server, principal, "routing-create", automation.OperationRequest{Capability: "routing_rules.create", Input: ruleInput})
	rules, err := db.ListRoutingRules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules=%#v err=%v", rules, err)
	}
	rule := rules[0]
	if rule.Action != model.RouteActionDirect || rule.Priority != 100 {
		t.Fatalf("unexpected rule: %#v", rule)
	}
	updateInput, _ := json.Marshal(map[string]any{"routing_rule_id": rule.ID, "changes": map[string]any{"priority": 200}})
	applyAutomationChangeset(t, server, principal, "routing-update", automation.OperationRequest{Capability: "routing_rules.update", Input: updateInput})
	updated, err := db.GetRoutingRule(ctx, rule.ID)
	if err != nil || updated.Priority != 200 {
		t.Fatalf("rule not updated: %#v err=%v", updated, err)
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
