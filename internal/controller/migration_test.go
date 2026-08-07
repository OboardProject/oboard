package controller

import (
	"net/http"
	"testing"
)

func TestShadowReportAndMigrationTools(t *testing.T) {
	h, token := setupPlansAPITest(t)
	server := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)

	// Standalone inbound conversion preview lists the inbound.
	preview := request(t, h, http.MethodPost, "/api/v2/ui/migrations/standalone-inbounds/preview", token, map[string]any{}, http.StatusOK)
	if int(preview["count"].(float64)) != 1 {
		t.Fatalf("standalone preview = %#v", preview)
	}

	// Apply creates a zero-step direct path for it.
	applied := request(t, h, http.MethodPost, "/api/v2/ui/migrations/standalone-inbounds/apply", token, map[string]any{}, http.StatusOK)
	created := applied["created"].([]any)
	if len(created) != 1 {
		t.Fatalf("migration apply = %#v", applied)
	}
	pathID := int64(created[0].(map[string]any)["proxy_path_id"].(float64))
	if int(applied["created_count"].(float64)) != 1 {
		t.Fatalf("migration apply = %#v", applied)
	}
	// The catalog no longer contains a standalone inbound node.
	catalog := request(t, h, http.MethodGet, "/api/v2/ui/assignable-nodes", token, nil, http.StatusOK)
	for _, n := range catalog["nodes"].([]any) {
		if n.(map[string]any)["type"] == "inbound" {
			t.Fatalf("standalone inbound still in catalog: %#v", n)
		}
	}

	// Bind the user to a migration plan via materialize.
	matPreview := request(t, h, http.MethodPost, "/api/v2/ui/migrations/materialize-plans/preview", token, map[string]any{}, http.StatusOK)
	groups := matPreview["groups"].([]any)
	if int(matPreview["group_count"].(float64)) == 0 {
		t.Fatalf("materialize preview = %#v", matPreview)
	}
	matApplied := request(t, h, http.MethodPost, "/api/v2/ui/migrations/materialize-plans/apply", token, map[string]any{}, http.StatusOK)
	if int(matApplied["created_count"].(float64)) == 0 {
		t.Fatalf("materialize apply = %#v", matApplied)
	}
	if int(matApplied["bound_users"].(float64)) == 0 {
		t.Fatalf("materialize apply = %#v", matApplied)
	}
	_ = groups

	// Shadow report is available and counts the user.
	report := request(t, h, http.MethodGet, "/api/v2/ui/shadow-report", token, nil, http.StatusOK)
	shadow := report["shadow"].(map[string]any)
	if int(shadow["users_compared"].(float64)) == 0 {
		t.Fatalf("shadow report = %#v", shadow)
	}

	// A path referenced by an active plan cannot be deleted. The materialized
	// plan that carries the path is the one whose detail lists it.
	plans := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans", token, nil, http.StatusOK)["subscription_plans"].([]any)
	planID := int64(0)
	for _, p := range plans {
		pid := int64(p.(map[string]any)["id"].(float64))
		detail := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(pid), token, nil, http.StatusOK)
		for _, n := range detail["nodes"].([]any) {
			if n.(map[string]any)["node_type"] == "proxy_path" && int64(n.(map[string]any)["node_id"].(float64)) == pathID {
				planID = pid
				break
			}
		}
		if planID != 0 {
			break
		}
	}
	if planID == 0 {
		t.Fatalf("no plan references path %d: %#v", pathID, plans)
	}
	request(t, h, http.MethodDelete, "/api/v2/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusConflict)
	// After removing it from the active revision, deletion is allowed again.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/sync", token, map[string]any{"op": "remove", "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathID}}, "expected_revision": 1}, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/publish", token, map[string]any{"expected_revision": 2}, http.StatusOK)
	request(t, h, http.MethodDelete, "/api/v2/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusOK)
}

func TestShadowReportReflectsLegacyUsers(t *testing.T) {
	h, token := setupPlansAPITest(t)
	server := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(inbound["id"].(float64))
	user := request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))

	// Legacy grant: alice is allowed on the standalone inbound.
	request(t, h, http.MethodPost, "/api/v2/ui/inbound-users", token, map[string]any{"inbound_id": inboundID, "user_id": userID}, http.StatusCreated)
	report := request(t, h, http.MethodGet, "/api/v2/ui/shadow-report", token, nil, http.StatusOK)
	shadow := report["shadow"].(map[string]any)
	if int(shadow["divergent_users"].(float64)) == 0 {
		t.Fatalf("expected legacy user to diverge from empty plan: %#v", shadow)
	}
}

func TestPageDataForPlanPages(t *testing.T) {
	h, token := setupPlansAPITest(t)
	server := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans", token, map[string]any{"name": "basic", "enabled": true}, http.StatusCreated)

	nodesData := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=nodes", token, nil, http.StatusOK)
	if _, ok := nodesData["servers"]; !ok {
		t.Fatalf("nodes page-data missing servers: %#v", nodesData)
	}
	if _, ok := nodesData["subscription_plans"]; !ok {
		t.Fatalf("nodes page-data missing subscription_plans: %#v", nodesData)
	}
	plansData := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=plans", token, nil, http.StatusOK)
	if _, ok := plansData["subscription_plans"]; !ok {
		t.Fatalf("plans page-data missing subscription_plans: %#v", plansData)
	}
	usersData := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=users", token, nil, http.StatusOK)
	if _, ok := usersData["user_plan_bindings"]; !ok {
		t.Fatalf("users page-data missing user_plan_bindings: %#v", usersData)
	}
	if _, ok := usersData["subscription_plans"]; !ok {
		t.Fatalf("users page-data missing subscription_plans: %#v", usersData)
	}
}
