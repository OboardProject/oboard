package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestProxyPathInitialStepCommitsAsOneTopologyUnit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	createdServer := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "compose-node", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 11000}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	createdInbound := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "compose-entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 10443, "config_json": "{}", "enabled": true}, http.StatusCreated)
	inboundID := int64(createdInbound["inbound"].(map[string]any)["id"].(float64))

	createdTargetServer := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "compose-target", "listen_ip": "0.0.0.0", "public_ipv4": "198.51.100.2", "entry_address": "198.51.100.2", "port_range_start": 11001, "port_range_end": 12000}, http.StatusCreated)
	targetServerID := int64(createdTargetServer["server"].(map[string]any)["id"].(float64))
	createdTargetInbound := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": targetServerID, "name": "compose-target-entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 10444, "config_json": "{}", "enabled": true}, http.StatusCreated)
	targetInboundID := int64(createdTargetInbound["inbound"].(map[string]any)["id"].(float64))

	before := configurationRevisionForTest(t, db)
	created := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{
		"name_mode": "auto", "inbound_id": inboundID, "enabled": true,
		"initial_steps": []map[string]any{{"position": 1, "node_type": "server_inbound", "inbound_id": targetInboundID, "transport_mode": "singbox", "config_json": "{}"}},
	}, http.StatusCreated)
	pathID := int64(created["proxy_path"].(map[string]any)["id"].(float64))
	steps := created["proxy_path_steps"].([]any)
	if len(steps) != 1 || int64(steps[0].(map[string]any)["path_id"].(float64)) != pathID {
		t.Fatalf("composed topology response = %#v", created)
	}
	if after := configurationRevisionForTest(t, db); after <= before {
		t.Fatalf("composed topology did not advance desired revision: %d -> %d", before, after)
	}

	invalid := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{
		"name_mode": "auto", "inbound_id": inboundID, "enabled": true,
		"initial_steps": []map[string]any{{"position": 1, "node_type": "imported", "transport_mode": "singbox", "config_json": "{}"}},
	}, http.StatusBadRequest)
	if invalid["error"] == nil {
		t.Fatalf("invalid composed topology did not return an error: %#v", invalid)
	}
	paths, err := db.ListProxyPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].ID != pathID {
		t.Fatalf("invalid composed topology left partial path: %#v", paths)
	}

	rule := request(t, h, http.MethodPost, "/api/v2/ui/routing-rules", token, map[string]any{
		"scope": "path_stage", "proxy_path_id": pathID, "name": "composed-rule", "priority": 100,
		"match_json": `{"domain_suffix":["example.com"]}`, "action": "direct", "enabled": true,
	}, http.StatusCreated)["routing_rule"].(map[string]any)
	ruleID := int64(rule["id"].(float64))
	bound := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{
		"name_mode": "auto", "inbound_id": inboundID, "enabled": true, "routing_rule_id": ruleID,
		"initial_steps": []map[string]any{{"position": 1, "node_type": "server_inbound", "inbound_id": targetInboundID, "transport_mode": "singbox", "config_json": "{}"}},
	}, http.StatusCreated)
	boundPathID := int64(bound["proxy_path"].(map[string]any)["id"].(float64))
	updatedRule := bound["routing_rule"].(map[string]any)
	if updatedRule["action"] != "proxy_path" || int64(updatedRule["target_proxy_path_id"].(float64)) != boundPathID {
		t.Fatalf("atomic path/rule binding response = %#v", bound)
	}
	storedRule, err := db.GetRoutingRule(context.Background(), ruleID)
	if err != nil || storedRule.TargetProxyPathID == nil || *storedRule.TargetProxyPathID != boundPathID {
		t.Fatalf("atomic path/rule binding not stored: %#v err=%v", storedRule, err)
	}

	request(t, h, http.MethodPost, "/api/v2/ui/proxy-path-steps/batch", token, map[string]any{"steps": []map[string]any{
		{"path_id": boundPathID, "position": 2, "node_type": "warp", "transport_mode": "singbox", "config_json": "{}"},
		{"path_id": boundPathID, "position": 2, "node_type": "warp", "transport_mode": "singbox", "config_json": "{}"},
	}}, http.StatusBadRequest)
	storedSteps, err := db.ListProxyPathStepsForPath(context.Background(), boundPathID)
	if err != nil || len(storedSteps) != 1 {
		t.Fatalf("failed batch left partial steps: %#v err=%v", storedSteps, err)
	}

	directResult := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths/direct-branches", token, map[string]any{
		"inbound_id": inboundID,
		"routing_rule": map[string]any{
			"server_id": serverID, "scope": "path_stage", "name": "atomic-direct-rule", "priority": 100,
			"match_json": `{"domain_suffix":["direct.example"]}`, "action": "direct", "enabled": true,
		},
	}, http.StatusCreated)
	directPathID := int64(directResult["proxy_path"].(map[string]any)["id"].(float64))
	directRule := directResult["routing_rule"].(map[string]any)
	directRuleID := int64(directRule["id"].(float64))
	if int64(directRule["proxy_path_id"].(float64)) != directPathID {
		t.Fatalf("atomic direct branch response = %#v", directResult)
	}
	storedDirectRule, err := db.GetRoutingRule(context.Background(), directRuleID)
	if err != nil || storedDirectRule.ProxyPathID == nil || *storedDirectRule.ProxyPathID != directPathID {
		t.Fatalf("atomic direct branch/rule not stored: %#v err=%v", storedDirectRule, err)
	}

	request(t, h, http.MethodPost, "/api/v2/ui/routing-rules/batch-delete", token, map[string]any{"ids": []int64{ruleID, 999999}}, http.StatusConflict)
	if _, err := db.GetRoutingRule(context.Background(), ruleID); err != nil {
		t.Fatalf("failed batch delete removed an existing rule: %v", err)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/routing-rules/batch-delete", token, map[string]any{"ids": []int64{ruleID, directRuleID}}, http.StatusOK)
	if _, err := db.GetRoutingRule(context.Background(), ruleID); err == nil {
		t.Fatal("successful batch delete kept first rule")
	}
	if _, err := db.GetRoutingRule(context.Background(), directRuleID); err == nil {
		t.Fatal("successful batch delete kept second rule")
	}
}

func configurationRevisionForTest(t *testing.T, db *store.Store) uint64 {
	t.Helper()
	revision, err := db.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return revision
}
