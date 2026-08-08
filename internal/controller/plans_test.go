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
	h, srv, token := setupPlansAPITestServer(t)
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
	revisions := detail["revisions"].([]any)
	latestCreatedAt := revisions[0].(map[string]any)["created_at"]
	list := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans", token, nil, http.StatusOK)
	listedPlan := list["subscription_plans"].([]any)[0].(map[string]any)
	if listedPlan["latest_version_created_at"] != latestCreatedAt {
		t.Fatalf("latest version timestamp = %#v, want %#v", listedPlan["latest_version_created_at"], latestCreatedAt)
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
	driveAccessChange(t, srv, token, int64(applied["access_change_id"].(float64)))

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
	published := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/publish", token, map[string]any{}, http.StatusOK)
	driveAccessChange(t, srv, token, int64(published["access_change_id"].(float64)))

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

func setupPlansAPITestServer(t *testing.T) (http.Handler, *Server, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	return h, srv, token
}

// driveAccessChange runs the worker until the change reaches a terminal state.
func driveAccessChange(t *testing.T, srv *Server, token string, changeID int64) map[string]any {
	t.Helper()
	var change map[string]any
	for i := 0; i < 10; i++ {
		srv.reconcileAccessChanges(t.Context())
		change = request(t, srv.Handler(), http.MethodGet, "/api/v2/ui/access-changes/"+itoa(changeID), token, nil, http.StatusOK)["access_change"].(map[string]any)
		switch change["status"] {
		case "finalized", "failed", "cancelled":
			return change
		}
	}
	t.Fatalf("access change %d did not reach a terminal state: %#v", changeID, change)
	return nil
}

// TestPlanModeAccessChangeFlow drives the prepare -> activate -> finalize
// state machine through the HTTP API with the worker reconcile functions, and
// asserts the runtime invariants: credentials are prepared before activation,
// the durable state flips only at activation, and stale previews conflict.
func TestPlanModeAccessChangeFlow(t *testing.T) {
	h, srv, token := setupPlansAPITestServer(t)
	server := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(inbound["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	pathID := int64(path["id"].(float64))
	user := request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	created := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans", token, map[string]any{
		"name": "premium", "enabled": true, "speed_limit_mbps": 100,
		"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathID}},
	}, http.StatusCreated)
	planID := int64(created["subscription_plan"].(map[string]any)["id"].(float64))

	// Apply the assignment: the binding must stay pending until activation and
	// the runtime must not grant the node before the change finalizes.
	applied := request(t, h, http.MethodPost, "/api/v2/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": planID, "deploy": true}, http.StatusOK)
	changeID := int64(applied["access_change_id"].(float64))
	if applied["status"] != "preparing" {
		t.Fatalf("apply status = %#v", applied)
	}
	userNodes := request(t, h, http.MethodGet, "/api/v2/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	if len(userNodes["nodes"].([]any)) != 0 {
		t.Fatalf("user must not hold nodes before activation: %#v", userNodes["nodes"])
	}
	change := request(t, h, http.MethodGet, "/api/v2/ui/access-changes/"+itoa(changeID), token, nil, http.StatusOK)["access_change"].(map[string]any)
	if change["status"] != "preparing" {
		t.Fatalf("change status = %#v", change)
	}

	// Drive the worker: prepare -> activate -> finalize. The test server has
	// no agent, so phases complete without Agent tasks.
	change = driveAccessChange(t, srv, token, changeID)
	if change["status"] != "finalized" {
		t.Fatalf("change status after reconcile = %#v", change)
	}
	userNodes = request(t, h, http.MethodGet, "/api/v2/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	if len(userNodes["nodes"].([]any)) != 1 {
		t.Fatalf("user nodes after finalize = %#v", userNodes["nodes"])
	}

	// Node change creates a pending version + access change; previewing the
	// pending version reports the change, and a stale hash must conflict.
	path2Inbound := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless2", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 8443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	path2 := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{"inbound_id": int64(path2Inbound["id"].(float64)), "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	path2ID := int64(path2["id"].(float64))
	appliedNodes := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/apply", token, map[string]any{
		"op": "add", "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": path2ID, "display_group": "HK"}},
	}, http.StatusOK)
	publishChangeID := int64(appliedNodes["access_change_id"].(float64))
	preview := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/preview", token, map[string]any{}, http.StatusOK)
	hash := preview["preview_hash"].(string)
	expectedActive := int64(preview["expected_active_revision_id"].(float64))
	if hash == "" || expectedActive == 0 {
		t.Fatalf("preview = %#v", preview)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/apply", token, map[string]any{
		"preview_hash": "stale", "expected_active_revision_id": expectedActive,
	}, http.StatusConflict)
	change = driveAccessChange(t, srv, token, publishChangeID)
	if change["status"] != "finalized" {
		t.Fatalf("publish change status = %#v", change)
	}
	detail := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID), token, nil, http.StatusOK)
	planAfter := detail["subscription_plan"].(map[string]any)
	pendingAfter, _ := planAfter["pending_revision_id"].(float64)
	currentAfter, _ := planAfter["current_revision_id"].(float64)
	if pendingAfter != 0 || currentAfter == 0 {
		t.Fatalf("plan after publish = %#v", detail)
	}

	// Allow exception: stored pending, activated by its change.
	ex := request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions", token, map[string]any{
		"user_id": userID, "node_type": "proxy_path", "node_id": pathID, "effect": "allow", "reason": "临时", "expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}, http.StatusCreated)
	exChangeID := int64(ex["access_change_id"].(float64))
	exceptionID := int64(ex["user_node_exception"].(map[string]any)["id"].(float64))
	if ex["access_change_status"] != "preparing" {
		t.Fatalf("exception change status = %#v", ex)
	}
	change = driveAccessChange(t, srv, token, exChangeID)
	if change["status"] != "finalized" {
		t.Fatalf("exception change status = %#v", change)
	}
	userNodes = request(t, h, http.MethodGet, "/api/v2/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	if len(userNodes["nodes"].([]any)) != 2 {
		t.Fatalf("user nodes with allow exception = %#v", userNodes["nodes"])
	}

	// Deny replaces the same row (one exception per user+node): the effect is
	// active immediately so subscriptions hide the node, while the change
	// revokes the credential.
	deny := request(t, h, http.MethodPatch, "/api/v2/ui/user-node-exceptions/"+itoa(exceptionID), token, map[string]any{
		"effect": "deny", "reason": "违规",
	}, http.StatusOK)
	userNodes = request(t, h, http.MethodGet, "/api/v2/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	if len(userNodes["nodes"].([]any)) != 1 {
		t.Fatalf("deny must hide the node immediately: %#v", userNodes["nodes"])
	}
	denyChangeID := int64(deny["access_change_id"].(float64))
	change = driveAccessChange(t, srv, token, denyChangeID)
	if change["status"] != "finalized" {
		t.Fatalf("deny change status = %#v", change)
	}
}
