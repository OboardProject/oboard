package controller

import (
	"net/http"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// setupOrderingTestTopology builds two servers (JP, SG), one inbound per
// server, a direct path on s1 and a two-hop path s1 -> s2, plus one plan
// containing both paths.
func setupOrderingTestTopology(t *testing.T) (http.Handler, *Server, string, map[string]int64) {
	t.Helper()
	h, srv, token := setupPlansAPITestServer(t)
	s1 := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "tokyo", "region_mode": "manual", "region_code": "JP", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	s2 := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "singapore", "region_mode": "manual", "region_code": "SG", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 10020, "port_range_end": 10030}, http.StatusCreated)["server"].(map[string]any)
	i1 := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": s1["id"], "name": "tokyo-vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	i2 := request(t, h, http.MethodPost, "/api/v2/ui/inbounds", token, map[string]any{"server_id": s2["id"], "name": "sg-vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 8443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	p1 := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{"inbound_id": i1["id"], "name": "东京直连", "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	p2 := request(t, h, http.MethodPost, "/api/v2/ui/proxy-paths", token, map[string]any{"inbound_id": i1["id"], "name": "东京-新加坡", "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	request(t, h, http.MethodPost, "/api/v2/ui/proxy-path-steps", token, map[string]any{"path_id": p2["id"], "position": 1, "node_type": "server_inbound", "server_id": s2["id"], "transport_mode": "singbox"}, http.StatusCreated)
	plan := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans", token, map[string]any{
		"name": "ordering-plan", "enabled": true,
		"nodes": []map[string]any{
			{"node_type": "proxy_path", "node_id": p1["id"]},
			{"node_type": "proxy_path", "node_id": p2["id"]},
		},
	}, http.StatusCreated)["subscription_plan"].(map[string]any)
	ids := map[string]int64{
		"s1": int64(s1["id"].(float64)), "s2": int64(s2["id"].(float64)),
		"i1": int64(i1["id"].(float64)), "i2": int64(i2["id"].(float64)),
		"p1": int64(p1["id"].(float64)), "p2": int64(p2["id"].(float64)),
		"plan": int64(plan["id"].(float64)),
	}
	return h, srv, token, ids
}

func orderingPolicy(mode, seed string, exitOrder []string) map[string]any {
	return map[string]any{
		"version": 1, "mode": mode, "manual_seed": seed,
		"exit_region_order": exitOrder, "entry_region_order_mode": "inherit_exit",
		"entry_region_order": []string{}, "entry_order": []string{},
	}
}

func TestPlanOrderingAPI(t *testing.T) {
	h, _, token, ids := setupOrderingTestTopology(t)
	planID := ids["plan"]
	keyP1 := "proxy_path:" + itoa(ids["p1"])

	// New plans default to exit_region; the active revision is read-only.
	active := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision=active", token, nil, http.StatusOK)
	if active["editable"] != false {
		t.Fatalf("active ordering editable = %#v", active["editable"])
	}
	policy := active["policy"].(map[string]any)
	if policy["mode"] != "exit_region" {
		t.Fatalf("new plan default mode = %#v", policy["mode"])
	}
	nodes := active["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("active ordering nodes = %#v", nodes)
	}

	// Invalid revision selector.
	request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision=bogus", token, nil, http.StatusBadRequest)

	// Invalid mode.
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 1, "policy": orderingPolicy("bogus", "exit_region", nil), "manual_node_order": []string{},
	}, http.StatusBadRequest)

	// Missing expected_revision.
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 0, "policy": orderingPolicy("manual", "exit_region", nil), "manual_node_order": []string{},
	}, http.StatusBadRequest)

	// Manual mode with a node outside the draft node set.
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 1, "policy": orderingPolicy("manual", "exit_region", nil),
		"manual_node_order": []string{"proxy_path:999999"},
	}, http.StatusBadRequest)

	// Manual mode with duplicate keys.
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 1, "policy": orderingPolicy("manual", "exit_region", nil),
		"manual_node_order": []string{keyP1, keyP1},
	}, http.StatusBadRequest)

	// Valid manual save creates a draft and persists positions.
	saved := request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 1, "policy": orderingPolicy("manual", "entry", nil),
		"manual_node_order": []string{keyP1},
	}, http.StatusOK)
	if saved["revision_id"].(float64) == 0 {
		t.Fatalf("manual save = %#v", saved)
	}
	draft := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision=draft", token, nil, http.StatusOK)
	if draft["editable"] != true {
		t.Fatalf("draft editable = %#v", draft["editable"])
	}
	draftNodes := draft["nodes"].([]any)
	if draft["unplaced_count"].(float64) != 1 {
		t.Fatalf("draft unplaced = %#v", draft["unplaced_count"])
	}
	placed := 0
	for _, raw := range draftNodes {
		node := raw.(map[string]any)
		if node["manual_position"] != nil {
			placed++
			if node["key"] != keyP1 {
				t.Fatalf("manual position on wrong node: %#v", node)
			}
		}
	}
	if placed != 1 {
		t.Fatalf("placed nodes = %d, want 1", placed)
	}

	// Stale expected_revision conflicts.
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 1, "policy": orderingPolicy("exit_region", "exit_region", nil), "manual_node_order": []string{},
	}, http.StatusConflict)

	// Switching back to an auto mode keeps the manual positions (spec 4.4).
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 2, "policy": orderingPolicy("exit_region", "exit_region", []string{"JP"}), "manual_node_order": []string{},
	}, http.StatusOK)
	draft = request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision=draft", token, nil, http.StatusOK)
	kept := 0
	for _, raw := range draft["nodes"].([]any) {
		if raw.(map[string]any)["manual_position"] != nil {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("auto-mode save kept positions = %d, want 1", kept)
	}
}

func TestPlanOrderingPreviewUsesBackendSorter(t *testing.T) {
	h, _, token, ids := setupOrderingTestTopology(t)
	planID := ids["plan"]
	// The two-hop path exits in SG, the direct path exits in JP. Custom exit
	// order [SG, JP] must place the SG path first.
	res := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/preview", token, map[string]any{
		"policy": orderingPolicy("exit_region", "exit_region", []string{"SG", "JP"}), "manual_node_order": []string{},
	}, http.StatusOK)
	nodes := res["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("preview nodes = %#v", nodes)
	}
	first := nodes[0].(map[string]any)
	if first["exit_region"] != "SG" {
		t.Fatalf("first preview node = %#v, want SG exit first", first)
	}
	// Preview never writes: no draft revision appears afterwards.
	draft := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision=draft", token, nil, http.StatusBadRequest)
	_ = draft
}

func TestAssignableNodeScopePreview(t *testing.T) {
	h, _, token, ids := setupOrderingTestTopology(t)
	keyP1 := "proxy_path:" + itoa(ids["p1"])
	keyP2 := "proxy_path:" + itoa(ids["p2"])
	requestScope := func(anchor string, kind string, extra map[string]any) map[string]any {
		t.Helper()
		scope := map[string]any{"kind": kind}
		for k, v := range extra {
			scope[k] = v
		}
		return request(t, h, http.MethodPost, "/api/v2/ui/assignable-node-scopes/preview", token, map[string]any{
			"anchor_node_key": anchor, "scope": scope, "include_disabled": false,
		}, http.StatusOK)
	}
	// Single node.
	res := requestScope(keyP1, "node", nil)
	if res["count"].(float64) != 1 {
		t.Fatalf("node scope = %#v", res)
	}
	// Same entry inbound: both paths share i1.
	res = requestScope(keyP1, "entry_inbound", nil)
	if res["count"].(float64) != 2 {
		t.Fatalf("entry_inbound scope = %#v", res)
	}
	// Same entry server: s1 (paths only; standalone i1 has a branch so it is
	// not a standalone node).
	res = requestScope(keyP1, "entry_server", nil)
	if res["count"].(float64) != 2 {
		t.Fatalf("entry_server scope = %#v", res)
	}
	// Path server s2: the two-hop path and the standalone inbound on s2 both
	// contain s2 in their topology.
	res = requestScope(keyP2, "path_server", map[string]any{"server_id": ids["s2"]})
	if res["count"].(float64) != 2 {
		t.Fatalf("path_server scope = %#v", res)
	}
	// Exit server s2: the two-hop path and the standalone inbound on s2 both
	// exit there; the direct path exits s1.
	res = requestScope(keyP2, "exit_server", nil)
	if res["count"].(float64) != 2 || res["scope"].(map[string]any)["server_id"].(float64) != float64(ids["s2"]) {
		t.Fatalf("exit_server scope = %#v", res)
	}
	// Exit region SG: the two-hop path and the standalone inbound on s2.
	res = requestScope(keyP2, "exit_region", nil)
	if res["count"].(float64) != 2 {
		t.Fatalf("exit_region scope = %#v", res)
	}
	// Unknown anchor is rejected.
	request(t, h, http.MethodPost, "/api/v2/ui/assignable-node-scopes/preview", token, map[string]any{
		"anchor_node_key": "proxy_path:999999", "scope": map[string]any{"kind": "node"}, "include_disabled": false,
	}, http.StatusBadRequest)
	// Invalid kind is rejected.
	request(t, h, http.MethodPost, "/api/v2/ui/assignable-node-scopes/preview", token, map[string]any{
		"anchor_node_key": keyP1, "scope": map[string]any{"kind": "bogus"}, "include_disabled": false,
	}, http.StatusBadRequest)
	// selection_hash is deterministic for the same scope.
	a := requestScope(keyP1, "entry_inbound", nil)
	b := requestScope(keyP1, "entry_inbound", nil)
	if a["selection_hash"] != b["selection_hash"] {
		t.Fatalf("selection hash not deterministic")
	}
}

func TestUserNodeExceptionsBatchAPI(t *testing.T) {
	h, _, token, ids := setupOrderingTestTopology(t)
	keyP1 := "proxy_path:" + itoa(ids["p1"])
	user1 := request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	user2 := request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "bob", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userIDs := []int64{int64(user1["id"].(float64)), int64(user2["id"].(float64))}
	node := map[string]any{"node_type": "proxy_path", "node_id": ids["p1"]}
	body := func() map[string]any {
		return map[string]any{
			"user_ids": userIDs, "nodes": []any{node}, "effect": "allow",
			"reason": "批量测试", "starts_at": nil, "expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		}
	}
	// Preview reports 2 created.
	preview := request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions/batch/preview", token, body(), http.StatusOK)
	if preview["created"].(float64) != 2 || preview["updated"].(float64) != 0 || preview["skipped"].(float64) != 0 {
		t.Fatalf("batch preview = %#v", preview)
	}
	// Apply creates exactly one aggregate access change for both rows.
	applied := request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions/batch/apply", token, body(), http.StatusOK)
	if applied["created"].(float64) != 2 {
		t.Fatalf("batch apply = %#v", applied)
	}
	changeID := int64(applied["access_change_id"].(float64))
	if changeID == 0 {
		t.Fatalf("batch apply missing access change: %#v", applied)
	}
	// The same payload again is fully skipped and creates no new change.
	again := request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions/batch/apply", token, body(), http.StatusOK)
	if again["created"].(float64) != 0 || again["updated"].(float64) != 0 || again["skipped"].(float64) != 2 {
		t.Fatalf("idempotent batch apply = %#v", again)
	}
	if again["access_change_id"] != nil {
		t.Fatalf("idempotent apply must not create a change: %#v", again)
	}
	// The opposite effect updates the existing rows in place.
	denyBody := body()
	denyBody["effect"] = "deny"
	flipped := request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions/batch/apply", token, denyBody, http.StatusOK)
	if flipped["updated"].(float64) != 2 || flipped["created"].(float64) != 0 {
		t.Fatalf("opposite-effect apply = %#v", flipped)
	}
	// Invalid input is rejected before any write.
	bad := body()
	bad["reason"] = ""
	request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions/batch/apply", token, bad, http.StatusBadRequest)
	bad = body()
	bad["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	request(t, h, http.MethodPost, "/api/v2/ui/user-node-exceptions/batch/apply", token, bad, http.StatusBadRequest)
	_ = keyP1
}

func planRevisionOf(t *testing.T, h http.Handler, token string, planID int64) float64 {
	t.Helper()
	detail := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID), token, nil, http.StatusOK)
	return detail["subscription_plan"].(map[string]any)["revision"].(float64)
}

func TestPlanPublishPreviewClassification(t *testing.T) {
	h, srv, token, ids := setupOrderingTestTopology(t)
	planID := ids["plan"]

	// Ordering-only draft change is presentation_only with zero tasks.
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": 1, "policy": orderingPolicy("exit_region", "exit_region", []string{"JP"}), "manual_node_order": []string{},
	}, http.StatusOK)
	preview := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/preview", token, map[string]any{}, http.StatusOK)
	if preview["change_class"] != "presentation_only" || preview["task_count"].(float64) != 0 {
		t.Fatalf("presentation preview = %#v", preview)
	}
	if preview["ordering_changed"] != true {
		t.Fatalf("ordering_changed = %#v", preview["ordering_changed"])
	}
	hash := preview["preview_hash"].(string)

	// Apply finalizes through the normal plan_publish access change with zero
	// agent tasks and flips the active revision.
	applied := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/apply", token, map[string]any{"preview_hash": hash}, http.StatusOK)
	changeID := int64(applied["access_change_id"].(float64))
	terminal := driveAccessChange(t, srv, token, changeID)
	if terminal["status"] != "finalized" {
		t.Fatalf("presentation publish = %#v", terminal)
	}
	detail := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID), token, nil, http.StatusOK)
	plan := detail["subscription_plan"].(map[string]any)
	if plan["active_revision_id"].(float64) == 1 {
		t.Fatalf("active revision did not advance: %#v", plan)
	}

	// A node membership change is authorization with tasks (the affected
	// server is reachable so a task is queued).
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/sync", token, map[string]any{
		"op": "remove", "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": ids["p1"]}},
	}, http.StatusOK)
	preview = request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/preview", token, map[string]any{}, http.StatusOK)
	if preview["change_class"] != "authorization" || preview["membership_changed"] != true {
		t.Fatalf("authorization preview = %#v", preview)
	}

	// Reusing the old presentation hash must conflict.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/apply", token, map[string]any{"preview_hash": hash}, http.StatusConflict)

	// Manual position changes are covered by the hash: switching to manual
	// with a different list produces a different preview hash.
	keyP2 := "proxy_path:" + itoa(ids["p2"])
	request(t, h, http.MethodPut, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, map[string]any{
		"expected_revision": int64(planRevisionOf(t, h, token, planID)), "policy": orderingPolicy("manual", "entry", nil), "manual_node_order": []string{keyP2},
	}, http.StatusOK)
	manualPreview := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/preview", token, map[string]any{}, http.StatusOK)
	if manualPreview["preview_hash"] == preview["preview_hash"] {
		t.Fatalf("manual position change must alter the preview hash")
	}
	_ = model.SubscriptionNodeOrderManual
}
