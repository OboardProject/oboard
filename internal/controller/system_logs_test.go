package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	oboardlog "github.com/OboardProject/oboard/internal/logging"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestSystemLogsAdminAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logs, err := oboardlog.New(filepath.Join(t.TempDir(), "controller.log"), oboardlog.Config{MaxBytes: 1 << 20, Backups: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	if _, err := logs.Write([]byte("startup ready\nagent_token=must-not-leak\nunique failure marker\n")); err != nil {
		t.Fatal(err)
	}
	h := New(db, "test-secret", "", "", logs).Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	response := request(t, h, http.MethodGet, "/api/v1/system-logs?lines=20&q=unique", token, nil, http.StatusOK)
	snapshot := response["logs"].(map[string]any)
	content := snapshot["content"].(string)
	if !strings.Contains(content, "unique failure marker") || strings.Contains(content, "startup ready") {
		t.Fatalf("unexpected filtered logs: %q", content)
	}

	request(t, h, http.MethodPost, "/api/v1/settings", token, map[string]any{"controller_log_max_mb": 2, "controller_log_backups": 1}, http.StatusOK)
	config := logs.Config()
	if config.MaxBytes != 2<<20 || config.Backups != 1 {
		t.Fatalf("runtime log config = %#v", config)
	}

	request(t, h, http.MethodPost, "/api/v1/system-logs", token, map[string]any{"action": "rotate"}, http.StatusOK)
	download := httptest.NewRecorder()
	downloadReq := httptest.NewRequest(http.MethodGet, "/api/v1/system-logs/download", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(download, downloadReq)
	if download.Code != http.StatusOK || !strings.HasPrefix(download.Body.String(), "PK") {
		t.Fatalf("log download status=%d body=%q", download.Code, download.Body.String())
	}

	request(t, h, http.MethodDelete, "/api/v1/system-logs", token, nil, http.StatusOK)
	cleared, err := logs.Snapshot(100, "unique failure marker")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.LineCount != 0 {
		t.Fatalf("old logs remained after clear: %q", cleared.Content)
	}
}

func TestAgentLogSettingsAndControlTasks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "node-a", AgentID: "agent-a", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	configTask := request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(server.ID)+"/agent-config", token, map[string]any{
		"log_max_mb": 12, "log_backups": 0, "core_log_max_mb": 48, "core_log_backups": 4,
	}, http.StatusAccepted)
	if configTask["task"].(map[string]any)["type"] != "update_agent_config" {
		t.Fatalf("unexpected config task: %#v", configTask)
	}
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(server.ID)+"/agent-config", token, map[string]any{"log_max_mb": 0}, http.StatusBadRequest)
	controlTask := request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(server.ID)+"/logs/control", token, map[string]any{"action": "rotate", "services": "core"}, http.StatusAccepted)
	if controlTask["task"].(map[string]any)["type"] != "manage_logs" {
		t.Fatalf("unexpected log control task: %#v", controlTask)
	}
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(server.ID)+"/logs/control", token, map[string]any{"action": "remove", "services": "all"}, http.StatusBadRequest)
}

func TestSystemLogsRejectNonAdmin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logs, err := oboardlog.New(filepath.Join(t.TempDir(), "controller.log"), oboardlog.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	h := New(db, "test-secret", "", "", logs).Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/system-logs", viewerLogin["token"].(string), nil, http.StatusForbidden)
}
