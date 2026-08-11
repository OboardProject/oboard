package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestDashboardPageDataUsesLightTaskProjection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "dash-node", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: `{"config":{"kernel":true}}`, ResultJSON: `{"steps":[]}`, Status: "succeeded", ConfigVersion: 3, Nonce: "secret-nonce"}); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	page := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=dashboard", token, nil, http.StatusOK)
	tasks, ok := page["agent_tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("dashboard agent_tasks = %#v", page["agent_tasks"])
	}
	task := tasks[0].(map[string]any)
	for _, field := range []string{"payload_json", "result_json", "nonce"} {
		if value, exists := task[field]; exists && value != "" {
			t.Fatalf("dashboard agent_task leaked %q = %#v", field, value)
		}
	}
	if task["config_version"] != float64(3) || task["status"] != "succeeded" || task["type"] != model.AgentTaskTypeApplyDeployment {
		t.Fatalf("dashboard agent_task lost summary columns: %#v", task)
	}
	audit, ok := page["connection_audit"].(map[string]any)
	if !ok {
		t.Fatalf("dashboard connection_audit missing: %#v", page["connection_audit"])
	}
	if audit["window_hours"] != float64(24) {
		t.Fatalf("dashboard connection_audit window_hours = %#v", audit["window_hours"])
	}
	if _, exists := audit["elevated_risk_count"]; !exists {
		t.Fatalf("dashboard connection_audit missing elevated_risk_count: %#v", audit)
	}

	auditPage := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=audit", token, nil, http.StatusOK)
	for _, key := range []string{"connection_audit", "subscription_audit", "audit_risk"} {
		if value, exists := auditPage[key]; exists && value != nil {
			t.Fatalf("audit page-data should not embed the heavy risk overview (%q present: %#v); the console refetches /audit/risk-overview", key, value)
		}
	}
}
