package controller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestServerAgentUninstallQueuesTaskAndReusesActive(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline, AgentBuild: "dev"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	first := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(server.ID)+"/agent-uninstall", token, map[string]any{}, http.StatusAccepted)
	task := first["task"].(map[string]any)
	if task["type"] != model.AgentTaskTypeUninstallAgent {
		t.Fatalf("unexpected uninstall response: %#v", first)
	}
	var payload model.UninstallAgentTaskPayload
	if err := json.Unmarshal([]byte(task["payload_json"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Purge {
		t.Fatal("uninstall payload purge should be true")
	}
	if payload.ActorID == 0 {
		t.Fatal("uninstall payload should carry actor_id")
	}

	second := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(server.ID)+"/agent-uninstall", token, map[string]any{}, http.StatusAccepted)
	if second["existing"] != true {
		t.Fatalf("active uninstall task was not reused: %#v", second)
	}
	secondTask := second["task"].(map[string]any)
	if secondTask["id"].(float64) != task["id"].(float64) {
		t.Fatalf("reused task id = %v, want %v", secondTask["id"], task["id"])
	}
}

func TestServerAgentUninstallRejectsUnenrolledAndOfflineButAllowsOperator(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorLogin := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)

	unenrolled := &model.Server{Name: "plain", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	offline := &model.Server{Name: "offline", AgentID: "agent-offline", AgentTokenHash: security.HashSecret("t"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOffline}
	online := &model.Server{Name: "online", AgentID: "agent-online", AgentTokenHash: security.HashSecret("t2"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	for _, server := range []*model.Server{unenrolled, offline, online} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}

	request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(unenrolled.ID)+"/agent-uninstall", adminToken, map[string]any{}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(offline.ID)+"/agent-uninstall", adminToken, map[string]any{}, http.StatusConflict)
	request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(online.ID)+"/agent-uninstall", operatorToken, map[string]any{}, http.StatusAccepted)
}

func TestAgentUninstallSuccessDeletesServerAndFailureKeepsIt(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()

	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task, err := srv.queueAgentTask(ctx, server.ID, model.AgentTaskTypeUninstallAgent, model.UninstallAgentTaskPayload{Purge: true, ActorID: 7}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}

	post := func(taskID int64, status string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"task_id": taskID, "status": status, "result_json": `{"message":"ok"}`})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/task-results", bytes.NewReader(body))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("X-Agent-ID", "agent-edge")
		req.Header.Set("Authorization", "Bearer agent-token")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	failed := post(task.ID, "failed")
	if failed.Code != http.StatusOK {
		t.Fatalf("failed uninstall callback status=%d body=%s", failed.Code, failed.Body.String())
	}
	if _, err := db.GetServer(ctx, server.ID); err != nil {
		t.Fatalf("failed uninstall deleted server: %v", err)
	}
	stored, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" {
		t.Fatalf("failed uninstall task status=%s", stored.Status)
	}

	successTask, err := srv.queueAgentTask(ctx, server.ID, model.AgentTaskTypeUninstallAgent, model.UninstallAgentTaskPayload{Purge: true, ActorID: 7}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	ok := post(successTask.ID, "succeeded")
	if ok.Code != http.StatusOK {
		t.Fatalf("successful uninstall callback status=%d body=%s", ok.Code, ok.Body.String())
	}
	if _, err := db.GetServer(ctx, server.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("server was not deleted after successful uninstall: %v", err)
	}
}
