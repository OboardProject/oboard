package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func basePathTestStaticDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<!doctype html><html><head><base href="/" /></head><body>panel</body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func basePathRequest(t *testing.T, handler http.Handler, path string, wantStatus int, wantBody string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
	if recorder.Code != wantStatus {
		t.Fatalf("GET %s status = %d; want %d; body=%s", path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if wantBody != "" && !strings.Contains(recorder.Body.String(), wantBody) {
		t.Fatalf("GET %s body does not contain %q: %s", path, wantBody, recorder.Body.String())
	}
}

func TestBasePathMigrationWaitsForAgentCallbackOnNewPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "oboard.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{
		Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "controller_url", "http://localhost/old"); err != nil {
		t.Fatal(err)
	}

	app := New(db, "test-secret", basePathTestStaticDir(t), "/old", nil)
	app.ConfigureControllerUpdates(dbPath, "127.0.0.1:2787")
	handler := app.Handler()
	redirect, migrated, err := app.startBasePathMigration(ctx, httptest.NewRequest(http.MethodPost, "http://localhost/old/api/v1/ui/settings", nil), "/new")
	if err != nil || !migrated || redirect != "/new/settings" {
		t.Fatalf("start migration = redirect %q, migrated %v, err %v", redirect, migrated, err)
	}
	runtimeData, err := os.ReadFile(filepath.Join(root, controllerupdate.RuntimeStateName))
	if err != nil {
		t.Fatal(err)
	}
	var runtimeState controllerupdate.RuntimeState
	if err := json.Unmarshal(runtimeData, &runtimeState); err != nil {
		t.Fatal(err)
	}
	if runtimeState.ListenAddress != "127.0.0.1:2787" || len(runtimeState.BasePaths) != 2 || runtimeState.BasePaths[0] != "/new" || runtimeState.BasePaths[1] != "/old" {
		t.Fatalf("migration runtime state = %#v", runtimeState)
	}
	state := app.basePathState()
	tasks, err := db.ListTasksByConfigVersion(ctx, state.MigrationVersion)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("migration tasks = %#v, %v", tasks, err)
	}
	if !strings.Contains(tasks[0].PayloadJSON, `"controller_url":"http://localhost/new"`) {
		t.Fatalf("migration task has wrong Controller URL: %s", tasks[0].PayloadJSON)
	}

	basePathRequest(t, handler, "/old/settings", http.StatusOK, `<base href="/old/" />`)
	basePathRequest(t, handler, "/new/settings", http.StatusOK, `<base href="/new/" />`)
	basePathRequest(t, handler, "/old/healthz", http.StatusOK, `"ok":true`)
	basePathRequest(t, handler, "/new/healthz", http.StatusOK, `"ok":true`)
	basePathRequest(t, handler, "/healthz", http.StatusNotFound, "")

	report, _ := json.Marshal(model.AgentTaskResultReport{TaskID: tasks[0].ID, Status: "succeeded", ResultJSON: `{"message":"agent config updated"}`})
	request := httptest.NewRequest(http.MethodPost, "http://localhost/new/api/v1/agent/task-results", bytes.NewReader(report))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer agent-token")
	request.Header.Set("X-Agent-ID", "agent-edge")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Agent callback status = %d; body=%s", response.Code, response.Body.String())
	}
	basePathRequest(t, handler, "/old/healthz", http.StatusNotFound, "")
	basePathRequest(t, handler, "/new/healthz", http.StatusOK, `"ok":true`)
	if app.basePathState().MigrationVersion != 0 {
		t.Fatal("migration remained active after every Agent succeeded")
	}
	runtimeData, err = os.ReadFile(filepath.Join(root, controllerupdate.RuntimeStateName))
	if err != nil || json.Unmarshal(runtimeData, &runtimeState) != nil {
		t.Fatalf("read finalized runtime state: %v", err)
	}
	if len(runtimeState.BasePaths) != 1 || runtimeState.BasePaths[0] != "/new" {
		t.Fatalf("finalized runtime state = %#v", runtimeState)
	}

	restarted := New(db, "test-secret", app.staticDir, "/ignored", nil)
	basePathRequest(t, restarted.Handler(), "/old/healthz", http.StatusNotFound, "")
	basePathRequest(t, restarted.Handler(), "/new/healthz", http.StatusOK, `"ok":true`)
}

func TestBasePathMigrationRequiresSubscriptionRelayDisabled(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, settingSubscriptionRelayURL, "https://subscriptions.example.com/old"); err != nil {
		t.Fatal(err)
	}
	app := New(db, "test-secret", basePathTestStaticDir(t), "/old", nil)
	defer app.Close()
	if _, migrated, err := app.startBasePathMigration(ctx, httptest.NewRequest(http.MethodPost, "http://localhost/old/api/v1/ui/settings", nil), "/new"); err == nil || migrated || migrationConflictStatus(err) != http.StatusConflict {
		t.Fatalf("migration with active relay = migrated %v, err %v", migrated, err)
	}
}

func TestBasePathMigrationRestoresAndRetriesFailedAgents(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	online := &model.Server{Name: "online", AgentID: "agent-online", AgentTokenHash: security.HashSecret("online-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	offline := &model.Server{Name: "offline", AgentID: "agent-offline", AgentTokenHash: security.HashSecret("offline-token"), ListenIP: "0.0.0.0", PortRangeStart: 11000, PortRangeEnd: 11010, Status: model.ServerOffline}
	for _, server := range []*model.Server{online, offline} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSetting(ctx, "controller_url", "http://localhost"); err != nil {
		t.Fatal(err)
	}

	staticDir := basePathTestStaticDir(t)
	app := New(db, "test-secret", staticDir, "", nil)
	if _, migrated, err := app.startBasePathMigration(ctx, httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/ui/settings", nil), "/private"); err != nil || !migrated {
		t.Fatalf("start migration = %v, %v", migrated, err)
	}
	progress, err := app.basePathMigrationProgress(ctx)
	if err != nil || progress.Total != 2 || progress.Pending != 1 || progress.Failed != 1 {
		t.Fatalf("initial progress = %#v, %v", progress, err)
	}

	restarted := New(db, "test-secret", staticDir, "/ignored", nil)
	handler := restarted.Handler()
	basePathRequest(t, handler, "/healthz", http.StatusOK, `"ok":true`)
	basePathRequest(t, handler, "/private/healthz", http.StatusOK, `"ok":true`)
	if restarted.basePathState().MigrationVersion == 0 {
		t.Fatal("Controller restart did not restore active migration")
	}

	offline.Status = model.ServerOnline
	if err := db.UpdateServer(ctx, offline); err != nil {
		t.Fatal(err)
	}
	progress, err = restarted.retryBasePathMigration(ctx)
	if err != nil || progress.Pending != 2 || progress.Failed != 0 {
		t.Fatalf("retried progress = %#v, %v", progress, err)
	}
	tasks, err := db.ListTasksByConfigVersion(ctx, restarted.basePathState().MigrationVersion)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestMigrationTasks(tasks)
	if len(latest) != 2 {
		t.Fatalf("latest migration tasks = %#v", latest)
	}
	for _, task := range latest {
		if err := db.CompleteTask(ctx, task.ID, "succeeded", `{"message":"updated"}`); err != nil {
			t.Fatal(err)
		}
	}
	restarted.maybeFinalizeBasePathMigration(ctx)
	basePathRequest(t, handler, "/healthz", http.StatusNotFound, "")
	basePathRequest(t, handler, "/private/healthz", http.StatusOK, `"ok":true`)
}

func TestBasePathMigrationUsesLongestOverlappingPrefix(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := New(db, "test-secret", basePathTestStaticDir(t), "/abc", nil)
	app.basePaths.Store(&basePathState{Current: "/abc/new", Previous: "/abc", MigrationVersion: 1})
	handler := app.Handler()
	basePathRequest(t, handler, "/abc/settings", http.StatusOK, `<base href="/abc/" />`)
	basePathRequest(t, handler, "/abc/new/settings", http.StatusOK, `<base href="/abc/new/" />`)
}

func TestUnknownRoutesReturnStructuredJSON(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := New(db, "test-secret", basePathTestStaticDir(t), "/qzq", nil)
	defer app.Close()
	handler := app.Handler()
	for _, path := range []string{"/qzq/mcp", "/qzq/api/v2/oauth-clients", "/qzq/api/v1/does-not-exist"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `"request_id"`) || !strings.Contains(recorder.Body.String(), `"not_found"`) {
			t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestOutsideBasePathReturnsOpaqueNotFound(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := New(db, "test-secret", basePathTestStaticDir(t), "/qzq", nil)
	defer app.Close()
	handler := app.Handler()
	for _, path := range []string{"/", "/healthz", "/api/v1/version", "/api/v2/oauth-clients", "/login", "/dashboard", "/qzqx/healthz", "//qzq/healthz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status=%d; want 404", path, recorder.Code)
		}
		if body := recorder.Body.String(); body != "" {
			t.Fatalf("GET %s body=%q; want empty opaque 404", path, body)
		}
		if ct := recorder.Header().Get("Content-Type"); ct != "" {
			t.Fatalf("GET %s Content-Type=%q; want unset", path, ct)
		}
	}
}

func TestBasePathMigrationKeepsOAuthTokensUsableOnNewPath(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, "controller_url", "http://localhost/old"); err != nil {
		t.Fatal(err)
	}
	app := New(db, "test-secret", basePathTestStaticDir(t), "/old", nil)
	defer app.Close()

	user := &model.User{Username: "oauth-basepath-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "33333333-3333-4333-8333-333333333333", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_basepath", "Base path client", []string{"http://127.0.0.1/callback"})
	grant, principal := createTestGrant(t, app, *user, client, []string{"oboard:read", "offline_access"})
	issueTestMCPToken(t, db, grant, principal, client, user.ID, "http://localhost/old/api/v1/mcp", "oba_basepath_old")

	if _, migrated, err := app.startBasePathMigration(ctx, httptest.NewRequest(http.MethodPost, "http://localhost/old/api/v1/ui/settings", nil), "/new"); err != nil || !migrated {
		t.Fatalf("start migration = %v, %v", migrated, err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/new/api/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer oba_basepath_old")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code == http.StatusUnauthorized {
		t.Fatalf("migrated OAuth token was rejected: %s", response.Body.String())
	}
}

func TestBasePathMigrationToRootRetiresOldPrefix(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "http://localhost/old"); err != nil {
		t.Fatal(err)
	}
	app := New(db, "test-secret", basePathTestStaticDir(t), "/old", nil)
	if _, migrated, err := app.startBasePathMigration(context.Background(), httptest.NewRequest(http.MethodPost, "http://localhost/old/api/v1/ui/settings", nil), ""); err != nil || !migrated {
		t.Fatalf("migrate to root = %v, %v", migrated, err)
	}
	handler := app.Handler()
	basePathRequest(t, handler, "/healthz", http.StatusOK, `"ok":true`)
	basePathRequest(t, handler, "/settings", http.StatusOK, `<base href="/" />`)
	basePathRequest(t, handler, "/old/healthz", http.StatusNotFound, "")

	restarted := New(db, "test-secret", app.staticDir, "/ignored", nil)
	basePathRequest(t, restarted.Handler(), "/old/settings", http.StatusNotFound, "")
}

func TestBasePathMigrationSkipsUnenrolledAndForceCompletesOffline(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	unenrolled := &model.Server{Name: "plain", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	offline := &model.Server{Name: "offline", AgentID: "agent-offline", AgentTokenHash: security.HashSecret("offline-token"), ListenIP: "0.0.0.0", PortRangeStart: 11000, PortRangeEnd: 11010, Status: model.ServerOffline}
	for _, server := range []*model.Server{unenrolled, offline} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSetting(ctx, "controller_url", "http://localhost"); err != nil {
		t.Fatal(err)
	}
	app := New(db, "test-secret", basePathTestStaticDir(t), "", nil)
	if _, migrated, err := app.startBasePathMigration(ctx, httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/ui/settings", nil), "/private"); err != nil || !migrated {
		t.Fatalf("start migration = %v, %v", migrated, err)
	}
	progress, err := app.basePathMigrationProgress(ctx)
	if err != nil || progress.Total != 1 || progress.Failed != 1 || progress.Skipped < 1 || !progress.CanForce || !progress.CanRevoke {
		t.Fatalf("progress with unenrolled and offline = %#v, %v", progress, err)
	}
	handler := app.Handler()
	basePathRequest(t, handler, "/healthz", http.StatusOK, `"ok":true`)
	basePathRequest(t, handler, "/private/healthz", http.StatusOK, `"ok":true`)
	if _, err := app.forceBasePathMigration(ctx); err != nil {
		t.Fatal(err)
	}
	if app.basePathState().MigrationVersion != 0 {
		t.Fatal("force complete left the migration active")
	}
	basePathRequest(t, handler, "/healthz", http.StatusNotFound, "")
	basePathRequest(t, handler, "/private/healthz", http.StatusOK, `"ok":true`)
}

func TestBasePathMigrationRevokeSkipsUnreachableAndWaitsForUpdatedOffline(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	online := &model.Server{Name: "online", AgentID: "agent-online", AgentTokenHash: security.HashSecret("online-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	offline := &model.Server{Name: "offline", AgentID: "agent-offline", AgentTokenHash: security.HashSecret("offline-token"), ListenIP: "0.0.0.0", PortRangeStart: 11000, PortRangeEnd: 11010, Status: model.ServerOffline}
	for _, server := range []*model.Server{online, offline} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSetting(ctx, "controller_url", "http://localhost"); err != nil {
		t.Fatal(err)
	}
	app := New(db, "test-secret", basePathTestStaticDir(t), "", nil)
	handler := app.Handler()
	if _, migrated, err := app.startBasePathMigration(ctx, httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/ui/settings", nil), "/private"); err != nil || !migrated {
		t.Fatalf("start migration = %v, %v", migrated, err)
	}
	tasks, err := db.ListTasksByConfigVersion(ctx, app.basePathState().MigrationVersion)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestMigrationTasks(tasks)
	onlineTask, ok := latest[online.ID]
	if !ok {
		t.Fatal("missing online migration task")
	}
	if err := db.CompleteTask(ctx, onlineTask.ID, "succeeded", `{"message":"updated"}`); err != nil {
		t.Fatal(err)
	}
	online.Status = model.ServerOffline
	if err := db.UpdateServer(ctx, online); err != nil {
		t.Fatal(err)
	}

	redirect, progress, err := app.revokeBasePathMigration(ctx)
	if err != nil || redirect != "/settings" || progress.Direction != basePathMigrationDirectionRollback || progress.CanRevoke {
		t.Fatalf("revoke = redirect %q progress %#v err %v", redirect, progress, err)
	}
	if progress.Total != 1 || progress.Failed != 1 || !progress.CanForce {
		t.Fatalf("revoke should wait only for the previously updated offline agent: %#v", progress)
	}
	basePathRequest(t, handler, "/healthz", http.StatusOK, `"ok":true`)
	basePathRequest(t, handler, "/private/healthz", http.StatusOK, `"ok":true`)

	online.Status = model.ServerOnline
	if err := db.UpdateServer(ctx, online); err != nil {
		t.Fatal(err)
	}
	app.retryBasePathMigrationForServer(ctx, online.ID)
	tasks, err = db.ListTasksByConfigVersion(ctx, app.basePathState().MigrationVersion)
	if err != nil {
		t.Fatal(err)
	}
	rollback := latestMigrationTasks(tasks)[online.ID]
	if rollback.Status != "pending" {
		t.Fatalf("recovered agent was not retried: %#v", rollback)
	}
	if err := db.CompleteTask(ctx, rollback.ID, "succeeded", `{"message":"rolled back"}`); err != nil {
		t.Fatal(err)
	}
	app.maybeFinalizeBasePathMigration(ctx)
	basePathRequest(t, handler, "/private/healthz", http.StatusNotFound, "")
	basePathRequest(t, handler, "/healthz", http.StatusOK, `"ok":true`)
	if app.basePathState().Current != "" {
		t.Fatalf("revoked current path = %q", app.basePathState().Current)
	}
}
