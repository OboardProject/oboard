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

	// New plans default to exit_region; GET defaults to the latest saved
	// version which is editable (read_only=false).
	latest := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	if latest["read_only"] != false {
		t.Fatalf("latest ordering read_only = %#v", latest["read_only"])
	}
	policy := latest["policy"].(map[string]any)
	if policy["mode"] != "exit_region" {
		t.Fatalf("new plan default mode = %#v", policy["mode"])
	}
	nodes := latest["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("latest ordering nodes = %#v", nodes)
	}
	if latest["unplaced_count"].(float64) != 0 {
		t.Fatalf("auto mode must report zero unplaced: %#v", latest["unplaced_count"])
	}
	lockVersion := int64(latest["lock_version"].(float64))

	// Invalid revision selector.
	request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision=bogus", token, nil, http.StatusNotFound)
	request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision_id=bogus", token, nil, http.StatusNotFound)

	// Invalid mode.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": lockVersion, "policy": orderingPolicy("bogus", "exit_region", nil), "manual_node_order": []string{},
	}, http.StatusBadRequest)

	// Missing expected_lock_version.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": 0, "policy": orderingPolicy("manual", "exit_region", nil), "manual_node_order": []string{},
	}, http.StatusBadRequest)

	// Manual mode with a node outside the version node set.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": lockVersion, "policy": orderingPolicy("manual", "exit_region", nil),
		"manual_node_order": []string{"proxy_path:999999"},
	}, http.StatusBadRequest)

	// Manual mode with duplicate keys.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": lockVersion, "policy": orderingPolicy("manual", "exit_region", nil),
		"manual_node_order": []string{keyP1, keyP1},
	}, http.StatusBadRequest)

	// Valid manual save creates a version that becomes current immediately.
	saved := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": lockVersion, "policy": orderingPolicy("manual", "entry", nil),
		"manual_node_order": []string{keyP1},
	}, http.StatusOK)
	if saved["revision_id"].(float64) == 0 || saved["effective_immediately"] != true {
		t.Fatalf("manual save = %#v", saved)
	}
	versionID := int64(saved["revision_id"].(float64))
	manual := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering?revision_id="+itoa(versionID), token, nil, http.StatusOK)
	if manual["read_only"] != false || manual["is_current"] != true {
		t.Fatalf("manual version state = %#v", manual)
	}
	manualNodes := manual["nodes"].([]any)
	if manual["unplaced_count"].(float64) != 1 {
		t.Fatalf("manual unplaced = %#v", manual["unplaced_count"])
	}
	placed := 0
	for _, raw := range manualNodes {
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

	// Stale expected_lock_version conflicts with the current lock + latest
	// version in the body.
	conflict := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": 1, "policy": orderingPolicy("exit_region", "exit_region", nil), "manual_node_order": []string{},
	}, http.StatusConflict)
	if conflict["code"] != "plan_version_conflict" {
		t.Fatalf("conflict body = %#v", conflict)
	}
	nextLock := int64(conflict["current_lock_version"].(float64))

	// Switching back to an auto mode keeps the manual positions (spec 4.4).
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": nextLock, "policy": orderingPolicy("exit_region", "exit_region", []string{"JP"}), "manual_node_order": []string{},
	}, http.StatusOK)
	auto := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	kept := 0
	for _, raw := range auto["nodes"].([]any) {
		if raw.(map[string]any)["manual_position"] != nil {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("auto-mode save kept positions = %d, want 1", kept)
	}
	// Auto mode still reports zero unplaced.
	if auto["unplaced_count"].(float64) != 0 {
		t.Fatalf("auto-mode unplaced = %#v", auto["unplaced_count"])
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
	// Preview never writes: the latest version stays at v1.
	latest := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	if latest["version_no"].(float64) != 1 {
		t.Fatalf("preview wrote a version: %#v", latest["version_no"])
	}
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

	// Imported exits have no OBoard root inbound: "仅选择此节点" must still
	// work while entry-based scopes stay rejected.
	imported := request(t, h, http.MethodPost, "/api/v2/ui/external-outbounds/import", token, map[string]any{
		"scope": "global", "expose_to_users": true, "content": "socks5://user:pass@socks.example.com:1080#SOCKS-I",
	}, http.StatusCreated)
	externalID := int64(imported["external_outbounds"].([]any)[0].(map[string]any)["id"].(float64))
	externalKey := "external_outbound:" + itoa(externalID)
	res = requestScope(externalKey, "node", nil)
	if res["count"].(float64) != 1 {
		t.Fatalf("external node scope = %#v", res)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/assignable-node-scopes/preview", token, map[string]any{
		"anchor_node_key": externalKey, "scope": map[string]any{"kind": "entry_inbound"}, "include_disabled": false,
	}, http.StatusBadRequest)
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

func TestPlanVersionChangeClassification(t *testing.T) {
	h, srv, token, ids := setupOrderingTestTopology(t)
	planID := ids["plan"]

	// Ordering-only save is presentation-only: effective immediately, no
	// access change, and the current version advances.
	saved := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": 1, "policy": orderingPolicy("exit_region", "exit_region", []string{"JP"}), "manual_node_order": []string{},
	}, http.StatusOK)
	if saved["effective_immediately"] != true || saved["current_revision_id"].(float64) == 1 {
		t.Fatalf("ordering save = %#v", saved)
	}
	ordering := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	if ordering["read_only"] != false || ordering["version_no"].(float64) != 2 {
		t.Fatalf("ordering state after save = %#v", ordering)
	}
	if ordering["version_created_at"] == nil || ordering["version_created_at"] == "" {
		t.Fatalf("ordering version timestamp = %#v", ordering["version_created_at"])
	}

	// A node membership change is authorization: pending version + access
	// change; the current snapshot stays until activation.
	applied := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/nodes/apply", token, map[string]any{
		"op": "remove", "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": ids["p1"]}},
	}, http.StatusOK)
	changeID := int64(applied["access_change_id"].(float64))
	pendingID := int64(applied["pending_revision_id"].(float64))
	if pendingID == 0 || applied["access_change_status"] == "" {
		t.Fatalf("nodes/apply = %#v", applied)
	}
	detail := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID), token, nil, http.StatusOK)
	plan := detail["subscription_plan"].(map[string]any)
	if int64(plan["pending_revision_id"].(float64)) != pendingID {
		t.Fatalf("plan pending = %#v", plan)
	}
	// The pending version previews as authorization with agent tasks (the
	// affected server is reachable).
	preview := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/preview", token, map[string]any{}, http.StatusOK)
	if preview["change_class"] != "authorization" || preview["membership_changed"] != true {
		t.Fatalf("authorization preview = %#v", preview)
	}
	if preview["task_count"].(float64) == 0 {
		t.Fatalf("membership change must queue tasks: %#v", preview)
	}
	hash := preview["preview_hash"].(string)

	// An ordering save while a version is pending is rejected; after the
	// pending version finalizes, reusing the old preview hash conflicts.
	currentLock := int64(plan["lock_version"].(float64))
	conflict := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": currentLock, "policy": orderingPolicy("manual", "entry", nil), "manual_node_order": []string{},
	}, http.StatusConflict)
	if conflict["code"] != "plan_version_applying" {
		t.Fatalf("concurrent ordering save = %#v", conflict)
	}
	terminal := driveAccessChange(t, srv, token, changeID)
	if terminal["status"] != "finalized" {
		t.Fatalf("node change = %#v", terminal)
	}
	detail = request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID), token, nil, http.StatusOK)
	plan = detail["subscription_plan"].(map[string]any)
	pendingAfter := int64(0)
	if v, ok := plan["pending_revision_id"].(float64); ok {
		pendingAfter = int64(v)
	}
	if int64(plan["current_revision_id"].(float64)) != pendingID || pendingAfter != 0 {
		t.Fatalf("plan after activation = %#v", plan)
	}
	// Stale hash against the old pending version conflicts after finalize.
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/changes/apply", token, map[string]any{"preview_hash": hash}, http.StatusConflict)

	// Manual position changes alter the digest: an ordering save is a new
	// version and the manual version's policy is persisted.
	keyP2 := "proxy_path:" + itoa(ids["p2"])
	manualSave := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"expected_lock_version": int64(plan["lock_version"].(float64)), "policy": orderingPolicy("manual", "entry", nil), "manual_node_order": []string{keyP2},
	}, http.StatusOK)
	if manualSave["effective_immediately"] != true {
		t.Fatalf("manual save = %#v", manualSave)
	}
	_ = model.SubscriptionNodeOrderManual
}
