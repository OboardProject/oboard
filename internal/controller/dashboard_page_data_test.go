package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=dashboard", token, nil, http.StatusOK)
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

	tasksPage := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=tasks", token, nil, http.StatusOK)
	assertLightTaskProjection(t, tasksPage["agent_tasks"], "tasks page-data")
	listed := request(t, h, http.MethodGet, "/api/v1/ui/agent-tasks?limit=300", token, nil, http.StatusOK)
	assertLightTaskProjection(t, listed["tasks"], "GET /agent-tasks")
	detail := request(t, h, http.MethodGet, "/api/v1/ui/agent-tasks/"+itoa(int64(task["id"].(float64))), token, nil, http.StatusOK)
	got := detail["task"].(map[string]any)
	if !strings.Contains(fmt.Sprint(got["payload_json"]), "kernel") || !strings.Contains(fmt.Sprint(got["result_json"]), "steps") {
		t.Fatalf("task detail omitted payload/result: %#v", got)
	}

	auditPage := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=audit", token, nil, http.StatusOK)
	for _, key := range []string{"connection_audit", "subscription_audit", "audit_risk"} {
		if value, exists := auditPage[key]; exists && value != nil {
			t.Fatalf("audit page-data should not embed the heavy risk overview (%q present: %#v); the console refetches /audit/risk-overview", key, value)
		}
	}
}

func assertLightTaskProjection(t *testing.T, raw any, label string) {
	t.Helper()
	tasks, ok := raw.([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("%s tasks = %#v", label, raw)
	}
	task := tasks[0].(map[string]any)
	for _, field := range []string{"payload_json", "result_json", "nonce"} {
		if value, exists := task[field]; exists && value != "" {
			t.Fatalf("%s leaked %q = %#v", label, field, value)
		}
	}
	if task["config_version"] != float64(3) || task["status"] != "succeeded" || task["type"] != model.AgentTaskTypeApplyDeployment {
		t.Fatalf("%s lost summary columns: %#v", label, task)
	}
}

// TestServersPageDataReusesLoadedSnapshots guards the per-request reuse of
// server and settings data across the visible page payload and shared status.
func TestServersPageDataReusesLoadedSnapshots(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, token := loginTestAdmin(t, db)

	before := db.SQLStatementCount()
	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=servers", token, nil, http.StatusOK)
	used := db.SQLStatementCount() - before
	// Authentication, servers + telemetry, settings/defaults, DNS data,
	// deployment status and configuration sync all fit in this budget. Loading
	// servers again just to decorate configuration-sync rows adds three more
	// statements and fails this regression guard.
	if used > 17 {
		t.Fatalf("servers page-data SQL statements = %d, want <= 17", used)
	}
	settings := page["settings"].(map[string]any)
	if settings["storage_diagnostics"] != nil || settings["database_maintenance_hint"] != nil {
		t.Fatal("server page loaded settings-only storage diagnostics")
	}
	if settings["traffic_timezone"] != "Asia/Shanghai" || settings[settingRemoteTerminalEnabled] != true {
		t.Fatal("server page lost normalized settings")
	}
	full := request(t, h, http.MethodGet, "/api/v1/ui/settings", token, nil, http.StatusOK)
	if full["settings"].(map[string]any)["storage_diagnostics"] == nil {
		t.Fatal("settings endpoint lost storage diagnostics")
	}
}

// TestDashboardPageDataSendsServerTiming proves the instrumentation header is
// present and shaped like "stage;dur=..., total;dur=..." without payload data.
func TestDashboardPageDataSendsServerTiming(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/page-data?page=dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("page-data status = %d", rr.Code)
	}
	header := rr.Header().Get("Server-Timing")
	if !strings.HasPrefix(header, "summary;dur=") || !strings.Contains(header, "total;dur=") {
		t.Fatalf("Server-Timing header = %q", header)
	}
	for _, chunk := range strings.Split(header, ",") {
		if !strings.Contains(chunk, ";dur=") {
			t.Fatalf("malformed timing chunk %q", chunk)
		}
	}
}

func TestReturnLatencyPageData(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, token := loginTestAdmin(t, db)
	server := &model.Server{Name: "return-probe", ListenIP: "0.0.0.0", Status: model.ServerOnline, LatencyProbeEnabled: true}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=return-latency", token, nil, http.StatusOK)
	rows, ok := page["servers"].([]any)
	if !ok || len(rows) != 1 || rows[0].(map[string]any)["name"] != "return-probe" {
		t.Fatalf("missing return latency server: %#v", page)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "latency-viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "latency-viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=return-latency", login["token"].(string), nil, http.StatusForbidden)
}

func TestReturnLatencySettingsPatchPreservesServerConfiguration(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, token := loginTestAdmin(t, db)
	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{
		"name": "latency-patch", "listen_ip": "0.0.0.0", "listen_mode": "ipv4_only", "ip_stack": "prefer_ipv4", "connection_audit_enabled": true,
		"port_range_start": 12000, "port_range_end": 14000, "monitoring_mode": "standard", "region_mode": "manual", "region_code": "JP",
	}, http.StatusCreated)["server"].(map[string]any)
	updated := request(t, h, http.MethodPatch, "/api/v1/ui/servers/"+itoa(int64(created["id"].(float64))), token, map[string]any{
		"latency_probe_enabled": true, "latency_probe_mode": "icmp", "latency_probe_public_target": "cloudflare", "latency_probe_interval_seconds": 120,
		"latency_probe_sample_count": 5, "latency_probe_max_targets": 64,
	}, http.StatusOK)["server"].(map[string]any)
	for _, key := range []string{"name", "listen_ip", "listen_mode", "ip_stack", "connection_audit_enabled", "port_range_start", "port_range_end", "monitoring_mode", "region_mode", "region_code"} {
		if updated[key] != created[key] {
			t.Errorf("unrelated field %s changed: %v -> %v", key, created[key], updated[key])
		}
	}
	if updated["latency_probe_mode"] != "icmp" || updated["latency_probe_interval_seconds"] != float64(120) {
		t.Fatalf("probe patch not saved: %#v", updated)
	}
}
