package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestReenrollmentEvictsPreviousAgentSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	h := srv.Handler()

	node := &model.Server{Name: "reinstall-evict", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 60000, Status: model.ServerOffline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}

	enroll := func(token string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"enrollment_token": token,
			"health":           map[string]any{"status": "online", "os": "linux", "arch": "amd64", "agent_version": "0.1.0", "agent_build": "1"},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/enroll", strings.NewReader(string(body)))
		req.Header.Set("content-type", "application/json")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("enroll status=%d body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	firstToken := "first-enroll-token"
	if err := db.SetServerEnrollmentHash(ctx, node.ID, security.HashSecret(firstToken), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	first := enroll(firstToken)
	firstAgent := &model.Server{ID: node.ID, AgentID: first["agent_id"].(string), AgentTokenHash: security.HashSecret(first["agent_token"].(string))}
	oldSock := connectAgentWithToken(t, httpServer.URL, firstAgent, first["agent_token"].(string))
	defer oldSock.close()
	for {
		msg := oldSock.readMessageMaybe(300 * time.Millisecond)
		if msg == nil {
			break
		}
	}

	secondToken := "second-enroll-token"
	if err := db.SetServerEnrollmentHash(ctx, node.ID, security.HashSecret(secondToken), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	second := enroll(secondToken)
	if second["agent_id"] == first["agent_id"] || second["agent_token"] == first["agent_token"] {
		t.Fatalf("re-enrollment reused agent identity: first=%#v second=%#v", first, second)
	}

	closed := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := oldSock.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := oldSock.conn.ReadJSON(&raw); err != nil {
			closed = true
			break
		}
		if raw["type"] == "task_request" {
			t.Fatalf("revoked agent received a post-enroll task: %#v", raw)
		}
	}
	if !closed {
		t.Fatal("previous agent websocket stayed open after re-enrollment")
	}

	fresh, err := db.ActiveTaskByServerType(ctx, node.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatalf("re-enrollment did not queue a deployment: %v", err)
	}
	if fresh.Status != "pending" && fresh.Status != "running" {
		t.Fatalf("unexpected deployment status %#v", fresh)
	}

	newAgent := &model.Server{ID: node.ID, AgentID: second["agent_id"].(string), AgentTokenHash: security.HashSecret(second["agent_token"].(string))}
	newSock := connectAgentWithToken(t, httpServer.URL, newAgent, second["agent_token"].(string))
	defer newSock.close()
	task := newSock.expectTaskRequest(3 * time.Second)
	if task["type"] != model.AgentTaskTypeApplyDeployment {
		t.Fatalf("new agent task type=%v", task["type"])
	}
}

func connectAgentWithToken(t *testing.T, baseURL string, server *model.Server, token string) *testAgentSocket {
	t.Helper()
	header := http.Header{}
	header.Set("X-Agent-ID", server.AgentID)
	header.Set("Authorization", "Bearer "+token)
	wsURL := "ws" + baseURL[4:] + "/api/v1/agent/connect"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial agent websocket: %v", err)
	}
	return &testAgentSocket{t: t, conn: conn}
}

func TestAgentRejectsCrossServerTaskResult(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	serverA := &model.Server{Name: "node-a", AgentID: "agent-a", AgentTokenHash: security.HashSecret("token-a"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	serverB := &model.Server{Name: "node-b", AgentID: "agent-b", AgentTokenHash: security.HashSecret("token-b"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010}
	for _, server := range []*model.Server{serverA, serverB} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	task := &model.AgentTask{ServerID: serverB.ID, Type: model.AgentTaskTypeCollectLogs, PayloadJSON: `{"reason":"test"}`, Status: "running", ResultJSON: "{}", ConfigVersion: 1, Nonce: "nonce-b"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	body, _ := json.Marshal(model.AgentTaskResultReport{TaskID: task.ID, Status: "succeeded", ResultJSON: `{"ok":true}`})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/task-results", strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", serverA.AgentID)
	req.Header.Set("Authorization", "Bearer token-a")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-server task result status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestOperatorCannotPromoteViaRegistrationDefaultGroup(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	opToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)["token"].(string)
	adminGroup := request(t, h, http.MethodPost, "/api/v1/ui/user-groups", adminToken, map[string]any{"name": "自定义管理员", "role": "admin", "enabled": true}, http.StatusCreated)
	groupID := int64(adminGroup["user_group"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/settings", opToken, map[string]any{"registration_enabled": true, "registration_default_group_id": groupID}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"registration_enabled": true, "registration_default_group_id": groupID}, http.StatusBadRequest)
}

func TestViewerSessionCannotListFleetViaMachineAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodGet, "/api/v1/users", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/servers", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/topology", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/ui/users", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/ui/servers", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/ui/traffic-ledger", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/ui/traffic-ledger?user_id=1", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/ui/users/1/traffic-ledger", viewerToken, nil, http.StatusForbidden)
}

func TestBootstrapConcurrentRequestsCreateSingleAdmin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	var wg sync.WaitGroup
	statuses := make([]int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{"username": "admin-" + itoa(int64(index)), "password": "very-secure-password"})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/auth/bootstrap", strings.NewReader(string(body)))
			req.Header.Set("content-type", "application/json")
			req.RemoteAddr = "127.0.0." + itoa(int64(index+1)) + ":9"
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			statuses[index] = rr.Code
		}(i)
	}
	wg.Wait()
	created := 0
	conflicted := 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicted++
		default:
			t.Fatalf("unexpected bootstrap status %d from %#v", status, statuses)
		}
	}
	if created != 1 || conflicted != 3 {
		t.Fatalf("bootstrap statuses=%v want one 201 and three 409", statuses)
	}
	users, err := db.ListUsers(ctxOrBackground())
	if err != nil {
		t.Fatal(err)
	}
	admins := 0
	for _, user := range users {
		if user.Role == model.RoleAdmin {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("admin count=%d users=%#v", admins, users)
	}
}

func ctxOrBackground() context.Context {
	return context.Background()
}

func TestSanitizeTaskForRoleRedactsDiagnoseConfigContent(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"inbounds": []map[string]any{{"users": []map[string]any{{"uuid": "11111111-1111-4111-8111-111111111111"}}}},
		"_oboard":  map[string]any{"trusted_forward": map[string]any{"receivers": []map[string]any{{"key": "super-secret-tf-key"}}}},
	})
	result, _ := json.Marshal(map[string]any{
		"files": map[string]any{
			"sing_box_config": map[string]any{"content": string(content)},
		},
	})
	// Operators share administrator task visibility. MCP and viewer surfaces
	// must still redact diagnose config material.
	mcpView := scrubSensitiveJSON(string(result))
	if strings.Contains(mcpView, "11111111-1111-4111-8111-111111111111") || strings.Contains(mcpView, "super-secret-tf-key") {
		t.Fatalf("MCP task sanitizer leaked diagnose secrets: %s", mcpView)
	}
	viewer := sanitizeTaskForRole(model.AgentTask{ResultJSON: string(result)}, model.RoleViewer)
	if viewer.ResultJSON != "<redacted>" {
		t.Fatalf("viewer diagnose result was not fully redacted: %s", viewer.ResultJSON)
	}
}

func TestRemoteExecArgvRejectsShellBinary(t *testing.T) {
	_, err := newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"/bin/sh", "-c", "id"}, "", "/", 5)
	if err == nil {
		t.Fatal("structured exec accepted /bin/sh")
	}
	_, err = newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"bash", "-c", "id"}, "", "/", 5)
	if err == nil {
		t.Fatal("structured exec accepted bash")
	}
	_, err = newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"/usr/bin/env", "sh", "-c", "id"}, "", "/", 5)
	if err == nil {
		t.Fatal("structured exec accepted env sh")
	}
	_, err = newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"timeout", "1", "bash", "-c", "id"}, "", "/", 5)
	if err == nil {
		t.Fatal("structured exec accepted timeout bash")
	}
	_, err = newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"env", "PATH=/bin", "/bin/sh", "-c", "id"}, "", "/", 5)
	if err == nil {
		t.Fatal("structured exec accepted env PATH=/bin /bin/sh")
	}
	if _, err := newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"ls", "/bin/sh"}, "", "/", 5); err != nil {
		t.Fatalf("listing a shell path must remain allowed: %v", err)
	}
	payload, err := newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"true"}, "", "/", 5)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Command.Argv[0] != "true" {
		t.Fatalf("unexpected payload %#v", payload)
	}
	for _, argv := range [][]string{
		{"find", "/", "-name", "x", "-exec", "sh", "-c", "id", ";"},
		{"find", "/", "-execdir", "/bin/bash", "-c", "id", ";"},
		{"find", ".", "-ok", "env", "sh", "-c", "id", ";"},
		{"nice", "find", "/", "-exec", "sh", "-c", "id", ";"},
		{"script", "-c", "id"},
		{"su"},
		{"runuser", "root"},
		{"setsid", "sh", "-c", "id"},
		{"env", "env", "env", "env", "sh", "-c", "id"},
	} {
		if _, err := newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, argv, "", "/", 5); err == nil {
			t.Fatalf("structured exec accepted shell launcher %v", argv)
		}
	}
	if _, err := newRemoteExecPayload(1, 1, model.PrivilegeRemoteExec, model.RemoteExecModeArgv, []string{"find", "/", "-name", "*.log"}, "", "/", 5); err != nil {
		t.Fatalf("plain find must remain allowed: %v", err)
	}
}

func TestControllerUpdateScriptDoesNotFetchMutableMain(t *testing.T) {
	bodyBytes, err := os.ReadFile(filepath.Join("..", "..", "scripts", "update.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if strings.Contains(body, "/main/scripts/install.sh") {
		t.Fatal("update.sh still fetches mutable main")
	}
	if !strings.Contains(body, "releases/latest") || !strings.Contains(body, "raw.githubusercontent.com") {
		t.Fatal("update.sh must resolve an immutable release tag")
	}
}

func TestServiceAccountPartialFilterDeniesUnmentionedTypes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "bob", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", adminToken, map[string]any{"name": "node-a", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", adminToken, map[string]any{"name": "node-b", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20010}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))

	created := request(t, h, http.MethodPost, "/api/v1/api-principals", adminToken, map[string]any{
		"name": "limited-sa", "scopes": []string{"*"}, "resource_filter": map[string]any{"server_ids": []int64{serverAID}},
	}, http.StatusCreated)
	principalID := created["data"].(map[string]any)["id"].(string)
	issued := request(t, h, http.MethodPost, "/api/v1/api-principals/"+principalID+"/tokens", adminToken, map[string]any{}, http.StatusCreated)
	saToken := issued["data"].(map[string]any)["token"].(string)

	users := request(t, h, http.MethodGet, "/api/v1/users", saToken, nil, http.StatusOK)
	if count := users["meta"].(map[string]any)["count"].(float64); count != 0 {
		t.Fatalf("partial server filter listed users: %#v", users)
	}
	servers := request(t, h, http.MethodGet, "/api/v1/servers", saToken, nil, http.StatusOK)
	items, _ := servers["data"].([]any)
	if len(items) != 1 || int64(items[0].(map[string]any)["id"].(float64)) != serverAID {
		t.Fatalf("partial server filter listed %#v", servers)
	}
	request(t, h, http.MethodGet, "/api/v1/servers/"+itoa(serverBID), saToken, nil, http.StatusNotFound)

	open := request(t, h, http.MethodPost, "/api/v1/api-principals", adminToken, map[string]any{
		"name": "open-sa", "scopes": []string{"users:read", "servers:read"}, "resource_filter": map[string]any{},
	}, http.StatusCreated)
	openToken := request(t, h, http.MethodPost, "/api/v1/api-principals/"+open["data"].(map[string]any)["id"].(string)+"/tokens", adminToken, map[string]any{}, http.StatusCreated)["data"].(map[string]any)["token"].(string)
	allUsers := request(t, h, http.MethodGet, "/api/v1/users", openToken, nil, http.StatusOK)
	if count := allUsers["meta"].(map[string]any)["count"].(float64); count < 2 {
		t.Fatalf("unrestricted SA should still list users: %#v", allUsers)
	}
}

func TestEnrollmentTokenCannotBindAnotherServer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", adminToken, map[string]any{"name": "enroll-a", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", adminToken, map[string]any{"name": "enroll-b", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20010}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	enroll := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(serverAID)+"/enroll-token", adminToken, map[string]any{}, http.StatusOK)
	claimed := request(t, h, http.MethodPost, "/api/v1/agent/enroll", "", map[string]any{
		"enrollment_token": enroll["enrollment_token"],
		"health":           map[string]any{"status": "online", "os": "linux", "arch": "amd64", "agent_version": "0.1.0", "agent_build": "1"},
	}, http.StatusOK)
	if int64(claimed["server_id"].(float64)) != serverAID {
		t.Fatalf("token for A bound server %v", claimed["server_id"])
	}
	storedB, err := db.GetServer(context.Background(), serverBID)
	if err != nil {
		t.Fatal(err)
	}
	if storedB.AgentID != "" {
		t.Fatalf("server B was enrolled by A's token: %#v", storedB)
	}
}

func TestEnrollmentConcurrentRequestsConsumeTokenOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", adminToken, map[string]any{"name": "race-node", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(created["server"].(map[string]any)["id"].(float64))
	enroll := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(serverID)+"/enroll-token", adminToken, map[string]any{}, http.StatusOK)
	token := enroll["enrollment_token"].(string)
	statuses := make([]int, 8)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{
				"enrollment_token": token,
				"health":           map[string]any{"status": "online", "os": "linux", "arch": "amd64", "agent_version": "0.1.0", "agent_build": "1"},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/enroll", strings.NewReader(string(body)))
			req.Header.Set("content-type", "application/json")
			req.RemoteAddr = "198.51.100." + itoa(int64(index+1)) + ":9"
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			statuses[index] = rr.Code
		}(i)
	}
	wg.Wait()
	ok, unauthorized := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusUnauthorized:
			unauthorized++
		default:
			t.Fatalf("unexpected enroll status %d from %#v", status, statuses)
		}
	}
	if ok != 1 || unauthorized != 7 {
		t.Fatalf("concurrent enroll statuses=%v want one 200 and seven 401", statuses)
	}
}

func TestAgentAuthRejectsInvalidAgentIDAndBearer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "auth-node", AgentID: "agent-live", AgentTokenHash: security.HashSecret("agent-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	post := func(agentID, bearer string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic-reports", strings.NewReader(`{"streams":[],"reports":[]}`))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	if got := post("missing-agent", "agent-token"); got != http.StatusUnauthorized {
		t.Fatalf("invalid agent id status=%d", got)
	}
	if got := post(server.AgentID, "wrong-token"); got != http.StatusUnauthorized {
		t.Fatalf("invalid bearer status=%d", got)
	}
	if got := post(server.AgentID, "agent-token"); got != http.StatusOK {
		t.Fatalf("valid agent auth status=%d", got)
	}
}

func TestAgentCannotClaimCrossServerTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	serverA := &model.Server{Name: "claim-a", AgentID: "agent-claim-a", AgentTokenHash: security.HashSecret("token-a"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	serverB := &model.Server{Name: "claim-b", AgentID: "agent-claim-b", AgentTokenHash: security.HashSecret("token-b"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010}
	for _, server := range []*model.Server{serverA, serverB} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	task := &model.AgentTask{ServerID: serverB.ID, Type: model.AgentTaskTypeCollectLogs, PayloadJSON: `{"reason":"cross"}`, Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "nonce-b"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	sock := connectAgentWithToken(t, httpServer.URL, serverA, "token-a")
	defer sock.close()
	if msg := sock.readMessageMaybe(400 * time.Millisecond); msg != nil && msg["type"] == "task_request" {
		t.Fatalf("agent A received agent B task: %#v", msg)
	}
	stored, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" {
		t.Fatalf("cross-server claim changed task status to %s", stored.Status)
	}
}
