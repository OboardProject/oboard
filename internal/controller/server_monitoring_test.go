package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestServerMonitoringRESTAndMCPRoundTrip(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	ctx := context.Background()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "monitored"}, http.StatusCreated)
	id := int64(created["server"].(map[string]any)["id"].(float64))
	task := model.LatencyProbeTask{Name: "HTTP check", Method: model.LatencyProbeModeHTTP, Address: "https://example.com", Enabled: true, IntervalSeconds: 120, ServerIDs: []int64{id}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/ui/servers/%d", id)
	request(t, h, http.MethodPatch, path, token, map[string]any{"monitoring_target_task_id": task.ID}, http.StatusOK)
	request(t, h, http.MethodPatch, path, token, map[string]any{"name": "monitored-renamed"}, http.StatusOK)
	for _, route := range []string{path, "/api/v1/ui/servers", "/api/v1/ui/page-data?page=servers"} {
		response := request(t, h, http.MethodGet, route, token, nil, http.StatusOK)
		var server map[string]any
		if route == path {
			server = response["server"].(map[string]any)
		} else {
			server = firstNamedServer(t, response["servers"], "monitored-renamed")
		}
		if server["monitoring_target_task_id"] != float64(task.ID) {
			t.Fatalf("selection missing from %s: %#v", route, server)
		}
		display := server["monitoring_display"].(map[string]any)
		if display["name"] != task.Name || display["enabled"] != true {
			t.Fatalf("display missing from %s: %#v", route, display)
		}
	}
	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, users[0].ID)
	for i, target := range []int64{0, task.ID} {
		input, _ := json.Marshal(map[string]any{"server_id": id, "changes": map[string]any{"monitoring_target_task_id": target}})
		applyAutomationChangeset(t, srv, principal, fmt.Sprintf("monitor-%d", i), automation.OperationRequest{Capability: "servers.update", Input: input})
		dto, err := srv.application.GetServer(ctx, principal, id)
		if err != nil {
			t.Fatal(err)
		}
		if dto.MonitoringTargetTaskID != target || dto.MonitoringDisplay == nil {
			t.Fatalf("MCP selection = %#v", dto)
		}
		assertCapabilityOutputSchema(t, srv, "servers.get", dto)
	}
	for _, target := range []int64{-1, task.ID + 100} {
		request(t, h, http.MethodPatch, path, token, map[string]any{"monitoring_target_task_id": target}, http.StatusBadRequest)
	}
	other := &model.Server{Name: "other"}
	if err := db.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/servers/%d", other.ID), token, map[string]any{"monitoring_target_task_id": task.ID}, http.StatusBadRequest)
	if !hasServerManageParams(map[string]any{"monitoring_target_task_id": task.ID}) {
		t.Fatal("fast path does not recognize monitor selection")
	}
	denied := principal
	denied.ResourceFilter = json.RawMessage(fmt.Sprintf(`{"servers":{"mode":"selected","ids":[%d]}}`, other.ID))
	if _, _, err := srv.validateServerUpdateOperation(ctx, denied, serverUpdateOperation{ServerID: id, Changes: serverUpdateChanges{MonitoringTargetTaskID: &task.ID}}); err == nil {
		t.Fatal("server boundary bypassed")
	}
	task.Enabled = false
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.validateServerUpdateOperation(ctx, principal, serverUpdateOperation{ServerID: id, Changes: serverUpdateChanges{MonitoringTargetTaskID: &task.ID}}); err == nil {
		t.Fatal("disabled task accepted")
	}
	request(t, h, http.MethodPatch, path, token, map[string]any{"monitoring_target_task_id": 0}, http.StatusOK)
	request(t, h, http.MethodPatch, path, token, map[string]any{"monitoring_target_task_id": task.ID}, http.StatusBadRequest)
}
