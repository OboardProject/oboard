package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// loginTestAdmin bootstraps and logs in the default admin, returning the bearer
// token and the test handler.
func loginTestAdmin(t *testing.T, db *store.Store) (http.Handler, string) {
	t.Helper()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	return h, login["token"].(string)
}

func seedStaleOnlineServer(t *testing.T, db *store.Store, now time.Time) *model.Server {
	t.Helper()
	lastSeen := now.Add(-2 * time.Hour)
	server := &model.Server{Name: "stale-node", ListenIP: "0.0.0.0", Status: model.ServerOnline, AgentID: "agent-stale", LastSeenAt: &lastSeen}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	return server
}

// TestDashboardPageDataDoesNotExpireTasksOrMarkServersOffline seeds a pending
// task older than the five-minute timeout and an online server whose last seen
// time is stale, then proves the dashboard GET leaves both untouched while the
// lifecycle maintenance transitions them.
func TestDashboardPageDataDoesNotExpireTasksOrMarkServersOffline(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	server := seedStaleOnlineServer(t, db, now)
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: `{"config":{"kernel":true}}`, Status: "pending", Nonce: "n"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskStateForTest(ctx, task.ID, "pending", now.Add(-6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	h, token := loginTestAdmin(t, db)

	request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=dashboard", token, nil, http.StatusOK)
	assertTaskStatus(t, db, task.ID, "pending")
	assertServerStatus(t, db, server.ID, model.ServerOnline)

	srv := newTestServer(db, "test-secret", "")
	srv.expireTimedOutTasks(ctx)
	srv.checkOfflineAt(ctx, now)
	assertTaskStatus(t, db, task.ID, "failed")
	assertServerStatus(t, db, server.ID, model.ServerOffline)
}

// TestServersGETDoesNotMarkServersOffline proves the read-only servers endpoint
// never transitions runtime status.
func TestServersGETDoesNotMarkServersOffline(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	server := seedStaleOnlineServer(t, db, now)
	h, token := loginTestAdmin(t, db)

	request(t, h, http.MethodGet, "/api/v1/ui/servers", token, nil, http.StatusOK)
	assertServerStatus(t, db, server.ID, model.ServerOnline)

	srv := newTestServer(db, "test-secret", "")
	srv.checkOfflineAt(context.Background(), now)
	assertServerStatus(t, db, server.ID, model.ServerOffline)
}

// TestTasksGETDoesNotExpireTasks proves the tasks list endpoints never mutate
// task state as a side effect of reading.
func TestTasksGETDoesNotExpireTasks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	server := seedStaleOnlineServer(t, db, now)
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: `{"config":{"kernel":true}}`, Status: "pending", Nonce: "n"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskStateForTest(ctx, task.ID, "pending", now.Add(-6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	h, token := loginTestAdmin(t, db)

	request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=tasks", token, nil, http.StatusOK)
	assertTaskStatus(t, db, task.ID, "pending")

	request(t, h, http.MethodGet, "/api/v1/ui/servers/"+itoa(server.ID)+"/tasks", token, nil, http.StatusOK)
	assertTaskStatus(t, db, task.ID, "pending")

	request(t, h, http.MethodGet, "/api/v1/ui/agent-tasks", token, nil, http.StatusOK)
	assertTaskStatus(t, db, task.ID, "pending")
}

// TestProxyPathsPageDataIsReadOnly seeds topology that every page load used to
// repair (orphaned step, invalid custom name template, wrong derived processing
// role), then proves the GET leaves the database untouched while the mutation
// path repairs it.
func TestProxyPathsPageDataIsReadOnly(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "pp-node", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "pp-inbound", Protocol: model.ProtocolVLESS, Port: 443, ListenIP: "0.0.0.0", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameCustom, NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameServer, ServerID: 999999}}, Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 0, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &server.ID, InboundID: &inbound.ID, ProcessingRole: false}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	orphan := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &server.ID, InboundID: &inbound.ID}
	if err := db.CreateProxyPathStep(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	// Seed the invariants the GET used to repair inline: an orphaned step
	// reference, a custom name template that resolves to nothing, and a
	// processing role that does not match the derived role for the path.
	if err := db.SetProxyPathStepServerForTest(ctx, orphan.ID, 999999); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProxyPathStepProcessingRoleForTest(ctx, step.ID, true); err != nil {
		t.Fatal(err)
	}
	before := proxyPathTableSnapshot(t, db)
	h, token := loginTestAdmin(t, db)

	request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=proxy-paths", token, nil, http.StatusOK)
	after := proxyPathTableSnapshot(t, db)
	for key := range before {
		if after[key] != before[key] {
			t.Fatalf("proxy-paths GET mutated %s: before=%v after=%v", key, before[key], after[key])
		}
	}

	srv := newTestServer(db, "test-secret", "")
	if err := srv.store.PruneOrphanedProxyPathSteps(ctx); err != nil {
		t.Fatal(err)
	}
	if err := srv.reconcileProxyPathNameTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if err := srv.normalizeEnabledProxyPathProcessingRoles(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := db.ListProxyPathSteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range steps {
		if item.ID == orphan.ID {
			t.Fatalf("orphaned step %d survived maintenance", orphan.ID)
		}
	}
	paths, err := db.ListProxyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].NameMode != model.ProxyPathNameAuto {
		t.Fatalf("invalid custom name mode not reconciled: %#v", paths)
	}
	for _, item := range steps {
		if item.ID == step.ID && item.ProcessingRole {
			t.Fatalf("processing role not normalized: %#v", item)
		}
	}
}

func assertTaskStatus(t *testing.T, db *store.Store, taskID int64, want string) {
	t.Helper()
	task, err := db.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != want {
		t.Fatalf("task %d status = %q, want %q", taskID, task.Status, want)
	}
}

func assertServerStatus(t *testing.T, db *store.Store, serverID int64, want model.ServerStatus) {
	t.Helper()
	server, err := db.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if server.Status != want {
		t.Fatalf("server %d status = %q, want %q", serverID, server.Status, want)
	}
}

func proxyPathTableSnapshot(t *testing.T, db *store.Store) map[string]any {
	t.Helper()
	ctx := context.Background()
	paths, err := db.ListProxyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := db.ListProxyPathSteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	serverIDs := map[int64]struct{}{}
	for _, server := range servers {
		serverIDs[server.ID] = struct{}{}
	}
	orphans := 0
	for _, step := range steps {
		if step.ServerID != nil {
			if _, ok := serverIDs[*step.ServerID]; !ok {
				orphans++
			}
		}
	}
	nameMode := model.ProxyPathNameMode("")
	if len(paths) > 0 {
		nameMode = paths[0].NameMode
	}
	role := false
	if len(steps) > 0 {
		role = steps[0].ProcessingRole
	}
	return map[string]any{
		"paths_count":      len(paths),
		"steps_count":      len(steps),
		"orphans":          orphans,
		"name_mode":        nameMode,
		"processing_role0": role,
	}
}
