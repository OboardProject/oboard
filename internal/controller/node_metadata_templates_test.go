package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func findOrderingNode(t *testing.T, response map[string]any, key string) map[string]any {
	t.Helper()
	for _, raw := range response["nodes"].([]any) {
		node := raw.(map[string]any)
		if node["key"] == key {
			return node
		}
	}
	t.Fatalf("ordering response does not contain %s: %#v", key, response["nodes"])
	return nil
}

func createOrderTemplate(t *testing.T, h http.Handler, token, name string, regions []string) map[string]any {
	t.Helper()
	return request(t, h, http.MethodPost, "/api/v2/ui/node-order-templates", token, map[string]any{
		"name": name,
		"policy": map[string]any{
			"version": 1, "base_mode": "exit_region", "exit_region_order": regions,
			"entry_region_order_mode": "inherit_exit", "entry_region_order": []string{}, "entry_order": []string{},
			"new_node_placement": "by_template", "unmatched_placement": "append",
		},
	}, http.StatusCreated)["template"].(map[string]any)
}

func TestGlobalRenameInheritanceAndClearPlanOverride(t *testing.T) {
	h, srv, token, ids := setupOrderingTestTopology(t)
	planAID := ids["plan"]
	pathID := ids["p2"]
	key := "proxy_path:" + itoa(pathID)

	createdB := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans", token, map[string]any{
		"name": "override-plan", "enabled": true,
		"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathID}},
	}, http.StatusCreated)["subscription_plan"].(map[string]any)
	planBID := int64(createdB["id"].(float64))

	rename := request(t, h, http.MethodPatch, "/api/v2/ui/assignable-nodes/proxy_path/"+itoa(pathID)+"/metadata", token, map[string]any{
		"display_name_override": "日本 IPLC 01", "expected_lock_version": 0,
	}, http.StatusOK)
	if rename["affected_current_plan_count"].(float64) != 2 || rename["overridden_current_plan_count"].(float64) != 0 {
		t.Fatalf("initial rename counts = %#v", rename)
	}

	orderingB := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/ordering", token, nil, http.StatusOK)
	overrideSaved := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/node-presentation/versions", token, map[string]any{
		"base_revision_id":      int64(orderingB["base_revision_id"].(float64)),
		"expected_lock_version": int64(orderingB["lock_version"].(float64)),
		"nodes":                 []map[string]any{{"node_key": key, "display_name_override": "VIP 日本 01"}},
	}, http.StatusOK)
	overrideRevisionID := int64(overrideSaved["revision"].(map[string]any)["id"].(float64))

	rename = request(t, h, http.MethodPatch, "/api/v2/ui/assignable-nodes/proxy_path/"+itoa(pathID)+"/metadata", token, map[string]any{
		"display_name_override": "日本 Premium 01", "expected_lock_version": int64(rename["lock_version"].(float64)),
	}, http.StatusOK)
	audits, err := srv.store.ListAuditPage(t.Context(), 10, 0, "node_display_name.update")
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) == 0 || !strings.Contains(audits[0].Detail, `"old_global_override":"日本 IPLC 01"`) || !strings.Contains(audits[0].Detail, `"new_global_override":"日本 Premium 01"`) {
		t.Fatalf("global rename audit detail = %#v", audits)
	}
	if rename["affected_current_plan_count"].(float64) != 1 || rename["overridden_current_plan_count"].(float64) != 1 {
		t.Fatalf("second rename counts = %#v", rename)
	}

	orderingA := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planAID)+"/ordering", token, nil, http.StatusOK)
	if node := findOrderingNode(t, orderingA, key); node["global_name"] != "日本 Premium 01" || node["has_plan_name_override"] != false || !strings.Contains(node["name"].(string), "日本 Premium 01") {
		t.Fatalf("plan A did not inherit latest global name: %#v", node)
	}
	orderingB = request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/ordering", token, nil, http.StatusOK)
	if node := findOrderingNode(t, orderingB, key); node["global_name"] != "日本 Premium 01" || node["plan_name_override"] != "VIP 日本 01" || !strings.Contains(node["name"].(string), "VIP 日本 01") {
		t.Fatalf("plan B override changed with global rename: %#v", node)
	}

	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/node-presentation/versions", token, map[string]any{
		"base_revision_id":      int64(orderingB["base_revision_id"].(float64)),
		"expected_lock_version": int64(orderingB["lock_version"].(float64)),
		"nodes":                 []map[string]any{{"node_key": key, "display_name_override": nil}},
	}, http.StatusOK)
	orderingB = request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/ordering", token, nil, http.StatusOK)
	if node := findOrderingNode(t, orderingB, key); node["has_plan_name_override"] != false || !strings.Contains(node["name"].(string), "日本 Premium 01") {
		t.Fatalf("cleared plan override did not inherit global name: %#v", node)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/revisions/"+itoa(overrideRevisionID)+"/restore", token, map[string]any{
		"expected_lock_version": int64(orderingB["lock_version"].(float64)),
	}, http.StatusOK)
	restoredB := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/ordering", token, nil, http.StatusOK)
	if node := findOrderingNode(t, restoredB, key); node["plan_name_override"] != "VIP 日本 01" {
		t.Fatalf("restore did not copy historical name override: %#v", node)
	}
}

func TestTemplateSnapshotPreserveManualAndRebuild(t *testing.T) {
	h, _, token, ids := setupOrderingTestTopology(t)
	planID := ids["plan"]
	jpKey := "proxy_path:" + itoa(ids["p1"])
	sgKey := "proxy_path:" + itoa(ids["p2"])
	request(t, h, http.MethodPost, "/api/v2/ui/node-order-templates", token, map[string]any{
		"name": "无效入口", "policy": map[string]any{
			"version": 1, "base_mode": "entry", "exit_region_order": []string{},
			"entry_region_order_mode": "custom", "entry_region_order": []string{"JP"}, "entry_order": []string{"inbound:999999"},
			"new_node_placement": "by_template", "unmatched_placement": "append",
		},
	}, http.StatusBadRequest)
	template := createOrderTemplate(t, h, token, "日本优先", []string{"JP", "SG"})
	templateID := int64(template["id"].(float64))

	ordering := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	initialApply := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/apply-template", token, map[string]any{
		"template_id": templateID, "template_revision": 1, "apply_mode": "rebuild",
		"base_revision_id": int64(ordering["base_revision_id"].(float64)), "expected_lock_version": int64(ordering["lock_version"].(float64)),
	}, http.StatusOK)
	r1RevisionID := int64(initialApply["revision"].(map[string]any)["id"].(float64))
	ordering = request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	if ordering["order_template_revision"].(float64) != 1 || ordering["template_update_available"] != false {
		t.Fatalf("initial template snapshot = %#v", ordering)
	}

	manualPolicy := orderingPolicy("manual", "exit_region", []string{"JP", "SG"})
	manualPolicy["version"] = 2
	manualPolicy["new_node_placement"] = "by_template"
	manualPolicy["unmatched_placement"] = "append"
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"base_revision_id": int64(ordering["base_revision_id"].(float64)), "expected_lock_version": int64(ordering["lock_version"].(float64)),
		"policy": manualPolicy, "manual_node_order": []string{sgKey, jpKey},
	}, http.StatusOK)

	request(t, h, http.MethodPatch, "/api/v2/ui/node-order-templates/"+itoa(templateID), token, map[string]any{
		"name": "日本优先", "description": "r2", "expected_revision": 1,
		"policy": map[string]any{
			"version": 1, "base_mode": "exit_region", "exit_region_order": []string{"SG", "JP"},
			"entry_region_order_mode": "inherit_exit", "entry_region_order": []string{}, "entry_order": []string{},
			"new_node_placement": "by_template", "unmatched_placement": "append",
		},
	}, http.StatusOK)
	before := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	if before["order_template_revision"].(float64) != 1 || before["template_update_available"] != true {
		t.Fatalf("template update mutated snapshot or was not reported: %#v", before)
	}
	beforeNodes := before["nodes"].([]any)
	if beforeNodes[0].(map[string]any)["key"] != sgKey || beforeNodes[1].(map[string]any)["key"] != jpKey {
		t.Fatalf("manual order changed after template update: %#v", beforeNodes)
	}

	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/apply-template", token, map[string]any{
		"template_id": templateID, "template_revision": 2, "apply_mode": "preserve_manual",
		"base_revision_id": int64(before["base_revision_id"].(float64)), "expected_lock_version": int64(before["lock_version"].(float64)),
	}, http.StatusOK)
	preserved := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	preservedNodes := preserved["nodes"].([]any)
	if preserved["policy"].(map[string]any)["mode"] != "manual" || preservedNodes[0].(map[string]any)["key"] != sgKey || preservedNodes[1].(map[string]any)["key"] != jpKey {
		t.Fatalf("preserve_manual changed existing order: %#v", preserved)
	}

	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/apply-template", token, map[string]any{
		"template_id": templateID, "template_revision": 2, "apply_mode": "rebuild",
		"base_revision_id": int64(preserved["base_revision_id"].(float64)), "expected_lock_version": int64(preserved["lock_version"].(float64)),
	}, http.StatusOK)
	rebuilt := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	rebuiltNodes := rebuilt["nodes"].([]any)
	if rebuilt["policy"].(map[string]any)["mode"] != "exit_region" || rebuiltNodes[0].(map[string]any)["key"] != sgKey || rebuiltNodes[1].(map[string]any)["key"] != jpKey {
		t.Fatalf("rebuild did not use template r2: %#v", rebuilt)
	}
	for _, raw := range rebuiltNodes {
		if raw.(map[string]any)["manual_position"] != nil {
			t.Fatalf("rebuild retained a manual position: %#v", raw)
		}
	}
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/revisions/"+itoa(r1RevisionID)+"/restore", token, map[string]any{
		"expected_lock_version": int64(rebuilt["lock_version"].(float64)),
	}, http.StatusOK)
	restored := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	restoredNodes := restored["nodes"].([]any)
	if restored["order_template_revision"].(float64) != 1 || restoredNodes[0].(map[string]any)["key"] != jpKey {
		t.Fatalf("restore did not copy template snapshot: %#v", restored)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/node-order-templates/"+itoa(templateID)+"/archive", token, map[string]any{"expected_revision": 2}, http.StatusOK)
	archivedPlan := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	if archivedPlan["template_archived"] != true || archivedPlan["order_template_revision"].(float64) != 1 {
		t.Fatalf("archived template broke plan snapshot: %#v", archivedPlan)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/apply-template", token, map[string]any{
		"template_id": templateID, "template_revision": 3, "apply_mode": "rebuild",
		"base_revision_id": int64(archivedPlan["base_revision_id"].(float64)), "expected_lock_version": int64(archivedPlan["lock_version"].(float64)),
	}, http.StatusBadRequest)
}

func TestPlanOrderingIsolation(t *testing.T) {
	h, _, token, ids := setupOrderingTestTopology(t)
	planAID := ids["plan"]
	jpKey := "proxy_path:" + itoa(ids["p1"])
	sgKey := "proxy_path:" + itoa(ids["p2"])
	createdB := request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans", token, map[string]any{
		"name": "isolated-plan", "enabled": true,
		"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": ids["p1"]}, {"node_type": "proxy_path", "node_id": ids["p2"]}},
	}, http.StatusCreated)["subscription_plan"].(map[string]any)
	planBID := int64(createdB["id"].(float64))

	saveManual := func(planID int64, order []string) map[string]any {
		t.Helper()
		state := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
		policy := orderingPolicy("manual", "exit_region", []string{"JP", "SG"})
		policy["version"] = 2
		policy["new_node_placement"] = "by_template"
		policy["unmatched_placement"] = "append"
		request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
			"base_revision_id": int64(state["base_revision_id"].(float64)), "expected_lock_version": int64(state["lock_version"].(float64)),
			"policy": policy, "manual_node_order": order,
		}, http.StatusOK)
		return request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	}
	afterA := saveManual(planAID, []string{sgKey, jpKey})
	afterB := saveManual(planBID, []string{jpKey, sgKey})
	planBRevision := afterB["base_revision_id"]

	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planAID)+"/ordering/versions", token, map[string]any{
		"base_revision_id": int64(afterA["base_revision_id"].(float64)), "expected_lock_version": int64(afterA["lock_version"].(float64)),
		"policy": orderingPolicy("exit_region", "exit_region", []string{"JP", "SG"}), "manual_node_order": []string{},
	}, http.StatusOK)
	unchangedB := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planBID)+"/ordering", token, nil, http.StatusOK)
	if unchangedB["base_revision_id"] != planBRevision {
		t.Fatalf("plan B revision changed with plan A: before=%v after=%v", planBRevision, unchangedB["base_revision_id"])
	}
	nodesB := unchangedB["nodes"].([]any)
	if nodesB[0].(map[string]any)["key"] != jpKey || nodesB[1].(map[string]any)["key"] != sgKey {
		t.Fatalf("plan B order changed with plan A: %#v", nodesB)
	}
}

func TestManualMembershipPreviewPreservesExistingPendingNodes(t *testing.T) {
	h, srv, token, ids := setupOrderingTestTopology(t)
	planID := ids["plan"]
	jpID := ids["p1"]
	jpKey := "proxy_path:" + itoa(jpID)
	sgKey := "proxy_path:" + itoa(ids["p2"])

	state := request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)
	policy := orderingPolicy("manual", "exit_region", []string{"JP", "SG"})
	policy["version"] = 2
	policy["new_node_placement"] = "pending"
	policy["unmatched_placement"] = "pending"
	request(t, h, http.MethodPost, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering/versions", token, map[string]any{
		"base_revision_id": int64(state["base_revision_id"].(float64)), "expected_lock_version": int64(state["lock_version"].(float64)),
		"policy": policy, "manual_node_order": []string{jpKey},
	}, http.StatusOK)
	state = request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans/"+itoa(planID)+"/ordering", token, nil, http.StatusOK)

	mutation, preview, err := srv.planNodePlacement(t.Context(), planID, int64(state["base_revision_id"].(float64)), "remove", []model.SubscriptionPlanNode{{
		NodeType: model.AssignableNodeProxyPath,
		NodeID:   jpID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if mutation == nil || len(mutation.ManualOrder) != 0 || preview.PendingCount != 1 {
		t.Fatalf("pending membership preview = mutation %#v preview %#v", mutation, preview)
	}
	var pendingNode *orderingNodeView
	for i := range preview.Nodes {
		if preview.Nodes[i].Key == sgKey {
			pendingNode = &preview.Nodes[i]
			break
		}
	}
	if pendingNode == nil || pendingNode.ManualPosition != nil {
		t.Fatalf("existing pending node received a manual position: %#v", pendingNode)
	}
	foundReason := false
	for _, detail := range preview.InsertionDetail {
		if detail.NodeKey == sgKey && detail.Reason == "existing_pending" {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatalf("existing pending insertion detail missing: %#v", preview.InsertionDetail)
	}
}

func TestGlobalRenameInvalidatesInheritedSubscriptionETag(t *testing.T) {
	h, srv, token, ids := setupOrderingTestTopology(t)
	planID := ids["plan"]
	pathID := ids["p2"]
	createdUser := request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "alias-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(createdUser["id"].(float64))
	subscriptionToken := createdUser["subscription_token"].(string)
	assignment := request(t, h, http.MethodPost, "/api/v2/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": planID, "deploy": false}, http.StatusOK)
	if changeID, ok := assignment["access_change_id"].(float64); ok && changeID > 0 {
		driveAccessChange(t, srv, token, int64(changeID))
	}

	rename := request(t, h, http.MethodPatch, "/api/v2/ui/assignable-nodes/proxy_path/"+itoa(pathID)+"/metadata", token, map[string]any{
		"display_name_override": "新加坡 01", "expected_lock_version": 0,
	}, http.StatusOK)
	fetch := func(ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+subscriptionToken+"?format=sing-box", nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	first := fetch("")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "新加坡 01") || first.Header().Get("ETag") == "" {
		t.Fatalf("first subscription status=%d etag=%q body=%s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	oldETag := first.Header().Get("ETag")
	request(t, h, http.MethodPatch, "/api/v2/ui/assignable-nodes/proxy_path/"+itoa(pathID)+"/metadata", token, map[string]any{
		"display_name_override": "新加坡 Premium", "expected_lock_version": int64(rename["lock_version"].(float64)),
	}, http.StatusOK)
	second := fetch(oldETag)
	if second.Code != http.StatusOK || second.Header().Get("ETag") == oldETag || !strings.Contains(second.Body.String(), "新加坡 Premium") {
		t.Fatalf("renamed subscription reused stale cache status=%d old=%q new=%q body=%s", second.Code, oldETag, second.Header().Get("ETag"), second.Body.String())
	}
}
