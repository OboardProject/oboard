package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRefreshRuntimeRequiresConfirmAndRebuildsEnrolledServers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	enrolled := &model.Server{
		Name: "edge-online", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("token-edge"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, enrolled); err != nil {
		t.Fatal(err)
	}
	unenrolled := &model.Server{
		Name: "edge-new", ListenIP: "0.0.0.0", PortRangeStart: 20001, PortRangeEnd: 30000, Status: model.ServerOffline,
	}
	if err := db.CreateServer(ctx, unenrolled); err != nil {
		t.Fatal(err)
	}

	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{
		"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active",
	}, http.StatusCreated)
	viewerToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", viewerToken, map[string]any{"confirm": true}, http.StatusForbidden)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", adminToken, map[string]any{}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", adminToken, map[string]any{"confirm": false}, http.StatusBadRequest)

	beforeRevision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", adminToken, map[string]any{"confirm": true}, http.StatusAccepted)
	if result["queued_tasks"].(float64) != 2 || result["queued_servers"].(float64) != 1 || result["failed_immediate"].(float64) != 1 {
		t.Fatalf("unexpected refresh summary: %#v", result)
	}
	if result["delivery_retried"].(float64) != 1 || result["skipped_unenrolled"].(float64) != 1 {
		t.Fatalf("unexpected delivery summary: %#v", result)
	}
	afterRevision, err := db.ConfigurationRevision(ctx)
	if err != nil || afterRevision != beforeRevision {
		t.Fatalf("runtime refresh changed configuration revision: before=%d after=%d err=%v", beforeRevision, afterRevision, err)
	}

	tasks, err := db.ListTasksByServer(ctx, enrolled.ID, 10)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("enrolled tasks: %#v err=%v", tasks, err)
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(tasks[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if tasks[0].Type != model.AgentTaskTypeApplyDeployment || !payload.ForceRefresh || !payload.ConfigChanged || payload.TriggerReason != "runtime_refresh" {
		t.Fatalf("enrolled refresh payload: task=%#v payload=%#v", tasks[0], payload)
	}
	if payload.Version <= 0 || payload.Version != tasks[0].ConfigVersion {
		t.Fatalf("refresh did not allocate a config version: task=%#v payload=%#v", tasks[0], payload)
	}

	authState, err := db.AuthorizationState(ctx, enrolled.ID)
	if err != nil || authState.PendingReason != store.AuthorizationPendingDelivering {
		t.Fatalf("enrolled authorization was not marked pending: %#v err=%v", authState, err)
	}
	usersState, err := db.RuntimeUserState(ctx, enrolled.ID)
	if err != nil || usersState.PendingReason != store.RuntimeUsersPendingDelivering {
		t.Fatalf("enrolled runtime users were not marked pending: %#v err=%v", usersState, err)
	}
}
