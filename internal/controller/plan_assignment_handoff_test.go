package controller

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
)

// TestReassignmentPreparesOldAndNewTogether pins the two-phase promise of an
// assignment: until the change activates, prepare must keep the plan the user
// is on alive beside the one they are moving to. The previous binding is
// disabled the moment the new one is staged, so reading "the old bindings"
// after staging returns the new one and prepare silently drops the access it
// exists to preserve.
func TestReassignmentPreparesOldAndNewTogether(t *testing.T) {
	h, srv, token := setupPlansAPITestServer(t)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	newPath := func(name string, port int) int64 {
		inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": name, "protocol": "vless", "listen_ip": "0.0.0.0", "port": port, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
		path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": int64(inbound["id"].(float64)), "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
		return int64(path["id"].(float64))
	}
	newPlan := func(name string, pathID int64) int64 {
		created := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{
			"name": name, "enabled": true, "speed_limit_mbps": 100,
			"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathID}},
		}, http.StatusCreated)["subscription_plan"].(map[string]any)
		return int64(created["id"].(float64))
	}
	firstPath, secondPath := newPath("first", 443), newPath("second", 444)
	firstPlan, secondPlan := newPlan("basic", firstPath), newPlan("premium", secondPath)
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))

	applied := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": firstPlan, "deploy": false}, http.StatusOK)
	driveAccessChange(t, srv, token, int64(applied["access_change_id"].(float64)))

	// Move the user to the other plan; the change is left unfinished so the
	// prepare projection can be inspected as the runtime would receive it.
	moved := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": secondPlan, "deploy": false}, http.StatusOK)
	change, err := srv.store.GetAccessChange(t.Context(), int64(moved["access_change_id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	var prepare core.AccessProjection
	if err := json.Unmarshal([]byte(change.PrepareProjectionJSON), &prepare); err != nil {
		t.Fatal(err)
	}
	if !hasProjectedUser(prepare.ProxyPathUsers[firstPath], userID) {
		t.Fatalf("prepare dropped the plan the user is still on: %s", change.PrepareProjectionJSON)
	}
	if !hasProjectedUser(prepare.ProxyPathUsers[secondPath], userID) {
		t.Fatalf("prepare is missing the plan the user moves to: %s", change.PrepareProjectionJSON)
	}
	var scope core.AccessScope
	if err := json.Unmarshal([]byte(change.OldScopeJSON), &scope); err != nil {
		t.Fatal(err)
	}
	if !hasProjectedUser(scope.ProxyPathUsers[firstPath], userID) || hasProjectedUser(scope.ProxyPathUsers[secondPath], userID) {
		t.Fatalf("the recorded pre-change scope is not the state the user was in: %s", change.OldScopeJSON)
	}
}

func hasProjectedUser(users []int64, userID int64) bool {
	for _, id := range users {
		if id == userID {
			return true
		}
	}
	return false
}
