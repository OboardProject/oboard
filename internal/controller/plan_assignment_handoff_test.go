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

// TestConcurrentAssignmentsDoNotOverwriteEachOther is the two-administrator
// case: both read the same current plan, both submit. One assignment has to
// lose visibly. Silently applying the second on top means the first operator
// sees a success for a plan the user is no longer on.
func TestConcurrentAssignmentsDoNotOverwriteEachOther(t *testing.T) {
	h, srv, token := setupPlansAPITestServer(t)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	newPlan := func(name string, port int) int64 {
		inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": name, "protocol": "vless", "listen_ip": "0.0.0.0", "port": port, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
		path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": int64(inbound["id"].(float64)), "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
		created := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{
			"name": name, "enabled": true, "speed_limit_mbps": 100,
			"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": int64(path["id"].(float64))}},
		}, http.StatusCreated)["subscription_plan"].(map[string]any)
		return int64(created["id"].(float64))
	}
	basic, premium := newPlan("basic", 443), newPlan("premium", 444)
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))

	first := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": basic, "deploy": false}, http.StatusOK)
	driveAccessChange(t, srv, token, int64(first["access_change_id"].(float64)))

	// Both administrators loaded the user while it was on `basic`. The first
	// assignment goes through; the second still believes `basic` is current.
	before, err := srv.store.ListEnabledUserPlanBindings(t.Context(), []int64{userID})
	if err != nil || len(before) != 1 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	staleExpectation := map[string]any{itoa(userID): basic}
	winner := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": premium, "deploy": false, "expected_plan_ids": staleExpectation}, http.StatusOK)
	if winner["applied"] != true {
		t.Fatalf("first writer did not apply: %#v", winner)
	}
	loser := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": basic, "deploy": false, "expected_plan_ids": staleExpectation}, http.StatusConflict)
	if loser["conflict"] != "user_plan_assignment_changed" {
		t.Fatalf("second writer response = %#v", loser)
	}
	if ids, ok := loser["conflict_user_ids"].([]any); !ok || len(ids) != 1 || int64(ids[0].(float64)) != userID {
		t.Fatalf("conflict did not name the user: %#v", loser)
	}
	after, err := srv.store.ListEnabledUserPlanBindings(t.Context(), []int64{userID})
	if err != nil || len(after) != 1 || after[0].PlanID != premium {
		t.Fatalf("the losing assignment still changed the binding: %+v err=%v", after, err)
	}
}

// TestBatchAssignmentGradesEachUser is the batch case: one member of a large
// selection was moved by somebody else in between. Discarding the whole batch
// would make the operator redo 49 assignments because of one; applying it over
// the other administrator's change would lose theirs. Each user is graded on
// its own and the skipped ones are named.
func TestBatchAssignmentGradesEachUser(t *testing.T) {
	h, srv, token := setupPlansAPITestServer(t)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	newPlan := func(name string, port int) int64 {
		inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": name, "protocol": "vless", "listen_ip": "0.0.0.0", "port": port, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
		path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": int64(inbound["id"].(float64)), "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
		created := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{
			"name": name, "enabled": true, "speed_limit_mbps": 100,
			"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": int64(path["id"].(float64))}},
		}, http.StatusCreated)["subscription_plan"].(map[string]any)
		return int64(created["id"].(float64))
	}
	basic, premium := newPlan("basic", 443), newPlan("premium", 444)
	newUser := func(name string) int64 {
		created := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": name, "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
		return int64(created["id"].(float64))
	}
	stable, moved := newUser("stable"), newUser("moved")

	// Both users start unassigned; somebody else puts `moved` on basic.
	first := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{moved}, "plan_id": basic, "deploy": false}, http.StatusOK)
	driveAccessChange(t, srv, token, int64(first["access_change_id"].(float64)))

	// This request was built when both were unassigned.
	staleExpectation := map[string]any{itoa(stable): 0, itoa(moved): 0}
	applied := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{
		"user_ids": []int64{stable, moved}, "plan_id": premium, "deploy": false, "expected_plan_ids": staleExpectation,
	}, http.StatusOK)
	if applied["applied"] != true || int(applied["affected_users"].(float64)) != 1 {
		t.Fatalf("batch response = %#v", applied)
	}
	if int(applied["skipped_users"].(float64)) != 1 {
		t.Fatalf("skipped count = %#v", applied["skipped_users"])
	}
	conflicts, _ := applied["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %#v", applied["conflicts"])
	}
	conflict := conflicts[0].(map[string]any)
	if int64(conflict["user_id"].(float64)) != moved || int64(conflict["current_plan_id"].(float64)) != basic {
		t.Fatalf("conflict does not describe what happened: %#v", conflict)
	}

	// The user nobody touched moved to the new plan; the other kept the plan
	// the first writer put them on.
	bindings, err := srv.store.ListEnabledUserPlanBindings(t.Context(), []int64{stable, moved})
	if err != nil {
		t.Fatal(err)
	}
	byUser := map[int64]int64{}
	for _, binding := range bindings {
		byUser[binding.UserID] = binding.PlanID
	}
	if byUser[stable] != premium {
		t.Fatalf("the applicable user was not assigned: %#v", byUser)
	}
	if byUser[moved] != basic {
		t.Fatalf("the conflicting user was overwritten: %#v", byUser)
	}

	// When every user in the batch conflicts, nothing is applied at all.
	allStale := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{
		"user_ids": []int64{moved}, "plan_id": premium, "deploy": false, "expected_plan_ids": map[string]any{itoa(moved): 0},
	}, http.StatusConflict)
	if allStale["applied"] != false || allStale["conflict"] != "user_plan_assignment_changed" {
		t.Fatalf("all-conflicting batch = %#v", allStale)
	}
}
