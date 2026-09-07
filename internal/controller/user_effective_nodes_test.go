package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestUserEffectiveNodesUsesCurrentCatalog(t *testing.T) {
	h, srv, token, ids := setupOrderingTestTopology(t)
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{
		"username": "node-catalog-user", "password": "long-user-password", "role": "viewer", "status": "active",
	}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	plan := model.SubscriptionPlan{Name: "existing-membership", Enabled: true}
	// Persist membership that predates a branch being attached to its inbound.
	if err := srv.store.CreateSubscriptionPlan(t.Context(), &plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeInbound, NodeID: ids["i1"], Enabled: true},
		{NodeType: model.AssignableNodeInbound, NodeID: ids["i2"], Enabled: true},
		{NodeType: model.AssignableNodeProxyPath, NodeID: ids["p1"], Enabled: true},
		{NodeType: model.AssignableNodeProxyPath, NodeID: ids["p2"], Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetUserPlanBindings(t.Context(), []model.UserPlanBinding{{UserID: userID, PlanID: plan.ID}}); err != nil {
		t.Fatal(err)
	}
	name := "专用新加坡"
	if _, err := srv.store.UpsertNodeMetadata(t.Context(), model.AssignableNodeProxyPath, ids["p2"], &name, 0, nil); err != nil {
		t.Fatal(err)
	}
	data, err := srv.loadPlanAssignmentData(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	staleKey := core.NodeKeyOf(model.AssignableNodeInbound, ids["i1"])
	if _, ok := data.snapshot.UserNodes[userID][staleKey]; !ok {
		t.Fatal("fixture must retain the stale inbound grant")
	}
	response := request(t, h, http.MethodGet, "/api/v1/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	nodes := response["nodes"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("visible nodes = %#v, want standalone inbound and two paths", nodes)
	}
	want := map[string]bool{
		core.NodeKeyOf(model.AssignableNodeInbound, ids["i2"]):   false,
		core.NodeKeyOf(model.AssignableNodeProxyPath, ids["p1"]): false,
		core.NodeKeyOf(model.AssignableNodeProxyPath, ids["p2"]): false,
	}
	for _, raw := range nodes {
		node := raw.(map[string]any)
		key := node["key"].(string)
		if _, ok := want[key]; !ok || want[key] {
			t.Fatalf("unexpected or duplicate visible node: %#v", node)
		}
		want[key] = true
		if strings.TrimSpace(node["name"].(string)) == "" {
			t.Fatalf("missing display name: %#v", node)
		}
		if key == core.NodeKeyOf(model.AssignableNodeProxyPath, ids["p2"]) && !strings.Contains(node["name"].(string), name) {
			t.Fatalf("global node name override missing: %#v", node)
		}
	}
	path, err := srv.store.GetProxyPath(t.Context(), ids["p1"])
	if err != nil {
		t.Fatal(err)
	}
	path.Enabled = false
	if err := srv.store.UpdateProxyPath(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	response = request(t, h, http.MethodGet, "/api/v1/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	if len(response["nodes"].([]any)) != 2 {
		t.Fatalf("disabled path remains visible: %#v", response)
	}
	exception := model.UserNodeException{UserID: userID, NodeType: model.AssignableNodeInbound, NodeID: ids["i2"], Effect: model.UserNodeExceptionAllow, Status: model.UserNodeExceptionActive, Reason: "单独授权"}
	if err := srv.store.CreateUserNodeException(t.Context(), &exception); err != nil {
		t.Fatal(err)
	}
	removed := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": 0}, http.StatusOK)
	driveAccessChange(t, srv, token, int64(removed["access_change_id"].(float64)))
	response = request(t, h, http.MethodGet, "/api/v1/ui/users/"+itoa(userID)+"/nodes", token, nil, http.StatusOK)
	nodes = response["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["source"] != "exception_allow" {
		t.Fatalf("unassignment must retain only the individual authorization: %#v", nodes)
	}
	if _, err := srv.store.GetSubscriptionPlan(t.Context(), plan.ID); err != nil {
		t.Fatalf("unassignment removed the shared plan: %v", err)
	}
}
