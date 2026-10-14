package controller

import (
	"net/http"
	"testing"
	"time"
)

// The topology page has no user rows, so Controller reports how many users each
// entry serves: plan grants and direct grants as one union, minus denies.
func TestProxyPathsPageCountsPlanAndDirectUsersOnce(t *testing.T) {
	h, srv, token := setupPlansAPITestServer(t)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010, "renewal_cycle": "monthly"}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 10443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(inbound["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	pathID := int64(path["id"].(float64))
	plan := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{
		"name": "basic", "enabled": true, "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathID}},
	}, http.StatusCreated)["subscription_plan"].(map[string]any)
	newUser := func(name string) int64 {
		user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": name, "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
		return int64(user["id"].(float64))
	}
	alice, bob, carol := newUser("alice"), newUser("bob"), newUser("carol")
	applied := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{alice, bob}, "plan_id": int64(plan["id"].(float64)), "deploy": false}, http.StatusOK)
	driveAccessChange(t, srv, token, int64(applied["access_change_id"].(float64)))
	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	for _, userID := range []int64{alice, carol} {
		created := request(t, h, http.MethodPost, "/api/v1/ui/user-node-exceptions", token, map[string]any{
			"user_id": userID, "node_type": "inbound", "node_id": inboundID, "effect": "allow", "reason": "直接分配", "expires_at": expires,
		}, http.StatusCreated)
		driveAccessChange(t, srv, token, int64(created["access_change_id"].(float64)))
	}

	count := func() float64 {
		page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=proxy-paths", token, nil, http.StatusOK)
		counts, ok := page["inbound_user_counts"].(map[string]any)
		if !ok {
			t.Fatalf("proxy-paths page has no inbound_user_counts: %#v", page["inbound_user_counts"])
		}
		value, _ := counts[itoa(inboundID)].(float64)
		return value
	}
	if got := count(); got != 3 {
		t.Fatalf("plan users {alice,bob} + direct {alice,carol} = %v, want 3", got)
	}
	denied := request(t, h, http.MethodPost, "/api/v1/ui/user-node-exceptions", token, map[string]any{
		"user_id": bob, "node_type": "proxy_path", "node_id": pathID, "effect": "deny", "reason": "停用", "expires_at": expires,
	}, http.StatusCreated)
	driveAccessChange(t, srv, token, int64(denied["access_change_id"].(float64)))
	if got := count(); got != 2 {
		t.Fatalf("after denying bob = %v, want 2", got)
	}
}
