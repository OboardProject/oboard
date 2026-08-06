package controller

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/store"
)

func setupPlansAPITest(t *testing.T) (http.Handler, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	return h, token
}

func TestSubscriptionPlansAndAssignmentAPI(t *testing.T) {
	h, token := setupPlansAPITest(t)
	server := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(inbound["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	pathID := int64(path["id"].(float64))
	user := request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))

	// The catalog lists the standalone inbound and the new path.
	catalog := request(t, h, http.MethodGet, "/api/v2/ui/assignable-nodes", token, nil, http.StatusOK)
	nodes := catalog["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("catalog nodes = %d, want 2: %#v", len(nodes), nodes)
	}
	if int(catalog["total"].(float64)) != 2 {
		t.Fatalf("catalog total = %#v", catalog["total"])
	}

	// Create a plan and attach the path node.
	created := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans", token, map[string]any{
		"name": "premium", "enabled": true, "speed_limit_mbps": 100,
		"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathID}},
	}, http.StatusCreated)
	plan := created["subscription_plan"].(map[string]any)
	planID := int64(plan["id"].(float64))
	detail := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID), token, nil, http.StatusOK)
	if len(detail["nodes"].([]any)) != 1 {
		t.Fatalf("plan nodes = %#v", detail["nodes"])
	}

	// Preview the assignment, then apply it.
	preview := request(t, h, http.MethodPost, "/api/v2/ui/users/plan-assignment/preview", token, map[string]any{"user_ids": []int64{userID}, "plan_id": planID}, http.StatusOK)["preview"].(map[string]any)
	if len(preview["nodes_added"].([]any)) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	applied := request(t, h, http.MethodPost, "/api/v2/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": planID, "deploy": false}, http.StatusOK)
	if applied["applied"] != true {
		t.Fatalf("apply response = %#v", applied)
	}

	// The user's effective nodes now contain the path.
	userNodes := request(t, h, http.MethodGet, "/api/v2/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	nodeList := userNodes["nodes"].([]any)
	if len(nodeList) != 1 {
		t.Fatalf("user nodes = %#v", nodeList)
	}
	first := nodeList[0].(map[string]any)
	if first["source"] != "plan" || first["node_type"] != "proxy_path" {
		t.Fatalf("user node = %#v", first)
	}

	// Catalog node detail traces the user back to the plan.
	detail = request(t, h, http.MethodGet, "/api/v2/ui/assignable-nodes/proxy_path/"+itoa(pathID), token, nil, http.StatusOK)
	users := detail["users"].([]any)
	if len(users) != 1 || users[0].(map[string]any)["username"] != "alice" {
		t.Fatalf("node users = %#v", users)
	}

	// Replace plan nodes with nothing: the plan node change preview reports it.
	preview = request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/preview", token, map[string]any{"op": "replace", "nodes": []map[string]any{}}, http.StatusOK)["preview"].(map[string]any)
	if len(preview["nodes_removed"].([]any)) != 1 {
		t.Fatalf("replace preview = %#v", preview)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/sync", token, map[string]any{"op": "replace", "nodes": []map[string]any{}}, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/publish", token, map[string]any{}, http.StatusOK)

	// Removing a node that is no longer in the catalog (e.g. a deleted path)
	// must still work so stale plan membership can be cleaned up.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/sync", token, map[string]any{
		"op": "add", "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathID}},
	}, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/sync", token, map[string]any{
		"op": "remove", "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": 999999}},
	}, http.StatusOK)

	// Add a deny exception: the user loses the node but stays traceable.
	request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions", token, map[string]any{
		"user_id": userID, "node_type": "proxy_path", "node_id": pathID, "effect": "deny", "reason": "违规使用", "expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}, http.StatusCreated)
	userNodes = request(t, h, http.MethodGet, "/api/v2/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	if len(userNodes["nodes"].([]any)) != 0 {
		t.Fatalf("user nodes after deny = %#v", userNodes["nodes"])
	}
	detail = request(t, h, http.MethodGet, "/api/v2/ui/assignable-nodes/proxy_path/"+itoa(pathID), token, nil, http.StatusOK)
	users = detail["users"].([]any)
	if len(users) != 1 || users[0].(map[string]any)["source"] != "exception_deny" {
		t.Fatalf("node users after deny = %#v", users)
	}

	// Exceptions without a reason or with an expired date are rejected.
	request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions", token, map[string]any{
		"user_id": userID, "node_type": "proxy_path", "node_id": pathID, "effect": "allow", "reason": "", "expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions", token, map[string]any{
		"user_id": userID, "node_type": "proxy_path", "node_id": pathID, "effect": "allow", "reason": "过期", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
	}, http.StatusBadRequest)
	// Non-assignable nodes are rejected too.
	request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions", token, map[string]any{
		"user_id": userID, "node_type": "proxy_path", "node_id": 999999, "effect": "allow", "reason": "x", "expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}, http.StatusBadRequest)
}
