package controller

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestControllerUpdateAPIAndBackupCleanup(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "obu-")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "updater.sock")
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	statePath := filepath.Join(root, "updater-status.json")
	updater := controllerupdate.NewService(controllerupdate.ServiceConfig{
		SocketPath:       socketPath,
		BinaryEnvPath:    binaryEnv,
		ControllerBinary: binary,
		StatePath:        statePath,
		WorkRoot:         filepath.Join(root, "work"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- updater.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("start updater: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("updater socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("updater shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("updater did not stop")
		}
	})

	dbPath := filepath.Join(root, "oboard.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	app.controllerBackupDir = filepath.Join(root, "backups")
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	status := request(t, handler, http.MethodGet, "/api/v2/ui/controller-update", adminToken, nil, http.StatusOK)
	if _, exists := status["install_method"]; exists || status["channel"] != "pinned" || status["status"] != "pinned" {
		t.Fatalf("unexpected update status: %#v", status)
	}
	if status["auto_update_interval_hours"] != float64(controllerUpdateDefaultIntervalHours) {
		t.Fatalf("unexpected default update interval: %#v", status["auto_update_interval_hours"])
	}
	settingsResponse := request(t, handler, http.MethodGet, "/api/v2/ui/settings", adminToken, nil, http.StatusOK)
	defaults := settingsResponse["settings"].(map[string]any)
	if defaults[agentAutoUpdateSetting] != false || defaults[subscriptionRelayAutoUpdateSetting] != false || defaults[updateWindowEnabledSetting] != false || defaults[updateWindowStartHourSetting] != float64(3) || defaults[updateWindowEndHourSetting] != float64(7) {
		t.Fatalf("unexpected managed update defaults: %#v", defaults)
	}
	settingsResponse = request(t, handler, http.MethodPost, "/api/v2/ui/settings", adminToken, map[string]any{
		agentAutoUpdateSetting: true, subscriptionRelayAutoUpdateSetting: true,
		updateWindowEnabledSetting: true, updateWindowStartHourSetting: 22, updateWindowEndHourSetting: 4,
	}, http.StatusOK)
	saved := settingsResponse["settings"].(map[string]any)
	if saved[agentAutoUpdateSetting] != true || saved[subscriptionRelayAutoUpdateSetting] != true || saved[updateWindowEnabledSetting] != true || saved[updateWindowStartHourSetting] != float64(22) || saved[updateWindowEndHourSetting] != float64(4) {
		t.Fatalf("unexpected managed update settings: %#v", saved)
	}
	request(t, handler, http.MethodPost, "/api/v2/ui/settings", adminToken, map[string]any{updateWindowStartHourSetting: 24}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/check", adminToken, nil, http.StatusOK)
	for _, interval := range []int{1, 6, 24, 72, 168} {
		settings := request(t, handler, http.MethodPost, "/api/v2/ui/settings", adminToken, map[string]any{"controller_auto_update_interval_hours": interval}, http.StatusOK)
		if got := settings["settings"].(map[string]any)["controller_auto_update_interval_hours"]; got != float64(interval) {
			t.Fatalf("unexpected saved update interval: got %#v, want %d", got, interval)
		}
	}
	for _, interval := range []int{2, 12} {
		request(t, handler, http.MethodPost, "/api/v2/ui/settings", adminToken, map[string]any{"controller_auto_update_interval_hours": interval}, http.StatusBadRequest)
	}
	request(t, handler, http.MethodPost, "/api/v2/ui/settings", adminToken, map[string]any{"controller_auto_update_enabled": true}, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/install", adminToken, nil, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/cancel", adminToken, nil, http.StatusConflict)

	request(t, handler, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, handler, http.MethodGet, "/api/v2/ui/controller-update", viewerLogin["token"].(string), nil, http.StatusForbidden)

	firstBackup, err := app.createControllerBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(app.controllerBackupDir, "manual-backup.sqlite")
	if err := os.WriteFile(unrelated, []byte("manual"), 0o600); err != nil {
		t.Fatal(err)
	}
	latestBackup, err := app.createControllerBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(firstBackup); !os.IsNotExist(err) {
		t.Fatalf("previous update backup was not removed: %v", err)
	}
	if _, err := os.Stat(latestBackup); err != nil {
		t.Fatalf("latest update backup was removed: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated backup was removed: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(app.controllerBackupDir, "oboard-before-update-*.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0] != latestBackup {
		t.Fatalf("unexpected retained update backups: %#v", backups)
	}
}

func TestSuccessfulControllerUpdateRemovesBackup(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerBackupDir = filepath.Join(root, "backups")
	backupPath, err := app.createControllerBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.recordControllerUpdateBackup(context.Background(), backupPath, "20260810010101"); err != nil {
		t.Fatal(err)
	}
	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.removeSuccessfulControllerUpdateBackup(context.Background(), settings, controllerupdate.Status{Current: controllerupdate.BuildInfo{Build: "different-build"}})
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup was removed before the target build succeeded: %v", err)
	}
	app.removeSuccessfulControllerUpdateBackup(context.Background(), settings, controllerupdate.Status{Current: controllerupdate.BuildInfo{Build: "20260810010101"}})
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("successful update backup was not removed: %v", err)
	}
	settings, err = db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings[controllerBackupSetting] != "" || settings[controllerBackupTargetBuildSetting] != "" {
		t.Fatalf("successful update backup state was not cleared: %#v", settings)
	}
}

func TestControllerUpdatePanelActivityGate(t *testing.T) {
	app := &Server{}
	if !app.controllerPanelIdle(time.Now()) {
		t.Fatal("panel with no activity should be idle")
	}
	app.beginControllerPanelRequest()
	if app.controllerPanelIdle(time.Now().Add(controllerUpdatePanelIdlePeriod)) {
		t.Fatal("active panel request must block automatic update")
	}
	app.endControllerPanelRequest()
	if app.controllerPanelIdle(time.Now()) {
		t.Fatal("recent panel activity must block automatic update")
	}
	app.controllerActivityMu.Lock()
	app.controllerLastActivity = time.Now().Add(-controllerUpdatePanelIdlePeriod)
	app.controllerActivityMu.Unlock()
	if !app.controllerPanelIdle(time.Now()) {
		t.Fatal("panel should become idle after the inactivity window")
	}
}

func TestFallbackControllerUpdateStatus(t *testing.T) {
	t.Setenv("OBOARD_UPDATE_CHANNEL", "latest")
	status := (&Server{}).fallbackControllerUpdateStatus()
	if status.Channel != "stable" || status.State != "unavailable" {
		t.Fatalf("unexpected fallback status: %#v", status)
	}
	t.Setenv("OBOARD_UPDATE_CHANNEL", "1.2.3")
	if status := (&Server{}).fallbackControllerUpdateStatus(); status.Channel != "pinned" {
		t.Fatalf("exact binary version should be pinned: %#v", status)
	}
}

func TestControllerUpdaterPrepareUnsupported(t *testing.T) {
	if !controllerUpdaterPrepareUnsupported(&controllerupdate.UpdaterStatusError{Code: http.StatusNotFound, Message: "404 page not found"}) {
		t.Fatal("legacy updater 404 was not recognized")
	}
	if controllerUpdaterPrepareUnsupported(&controllerupdate.UpdaterStatusError{Code: http.StatusConflict}) {
		t.Fatal("updater conflict was treated as a legacy updater")
	}
	if controllerUpdaterPrepareUnsupported(context.Canceled) {
		t.Fatal("context cancellation was treated as a legacy updater")
	}
}

func TestControllerUpdateInstallContinuesAfterRequestDisconnect(t *testing.T) {
	root := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "obu-disconnect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	installStarted := make(chan struct{})
	releaseInstall := make(chan struct{})
	var releaseInstallOnce sync.Once
	unblockInstall := func() { releaseInstallOnce.Do(func() { close(releaseInstall) }) }
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := controllerupdate.Status{
			Channel:         "dev",
			State:           "available",
			UpdateAvailable: true,
			Available:       controllerupdate.BuildInfo{Version: "dev-next", Build: "20260813000000"},
		}
		switch r.URL.Path {
		case "/v1/prepare":
			status.State = "downloading"
			status.CanCancel = true
		case "/v1/install":
			close(installStarted)
			select {
			case <-releaseInstall:
			case <-r.Context().Done():
				t.Errorf("updater install request was cancelled with the panel request: %v", r.Context().Err())
				return
			}
			status.State = "downloading"
			status.CanCancel = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- updater.Serve(listener) }()
	t.Cleanup(func() {
		unblockInstall()
		_ = updater.Close()
		<-serveDone
	})

	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	app.controllerBackupDir = filepath.Join(root, "backups")

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/ui/controller-update/install", nil).WithContext(requestCtx)
	handlerDone := make(chan struct{})
	go func() {
		app.controllerUpdateInstall(recorder, request)
		close(handlerDone)
	}()

	select {
	case <-handlerDone:
		if recorder.Code != http.StatusOK {
			t.Fatalf("install response=%d body=%s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(time.Second):
		unblockInstall()
		t.Fatal("install API waited for the background updater operation")
	}
	cancelRequest()
	select {
	case <-installStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("background install approval did not start")
	}
	unblockInstall()
	app.controllerUpdateRunMu.Lock()
	app.controllerUpdateRunMu.Unlock()
	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(settings[controllerBackupSetting]) == "" {
		t.Fatal("background update did not record its database backup")
	}
}

func TestControllerUpdateCapabilitiesAndPublicView(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "obu-mcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	channel := "stable"
	checkCalls, channelCalls, cancelCalls := 0, 0, 0
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		status := controllerupdate.Status{
			Channel: channel, State: "available", UpdateAvailable: true, CanCancel: true,
			Available:     controllerupdate.BuildInfo{Version: "2.0.0", Build: "20260817000000"},
			BackupPath:    "/private/controller.sqlite",
			ManualCommand: "sudo updater install --secret",
		}
		switch r.URL.Path {
		case "/v1/check":
			checkCalls++
		case "/v1/channel":
			var request controllerupdate.ChannelRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode channel request: %v", err)
			}
			channel = request.Channel
			status.Channel = channel
			channelCalls++
		case "/v1/cancel":
			status.State = "cancelling"
			cancelCalls++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})}
	done := make(chan error, 1)
	go func() { done <- updater.Serve(listener) }()
	t.Cleanup(func() {
		_ = updater.Close()
		<-done
	})

	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	server.controllerUpdater = controllerupdate.NewClient(socketPath)
	admin := &model.User{Username: "update-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111117", ProxyPassword: "unused"}
	if err := db.CreateUser(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	view, err := server.queryManagementCapability(t.Context(), principal, "controller_update.status", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "backup_path") || strings.Contains(string(encoded), "manual_command") || strings.Contains(string(encoded), "/private/") || strings.Contains(string(encoded), "sudo updater") {
		t.Fatalf("controller update public view leaked local details: %s", encoded)
	}

	applyAutomationChangeset(t, server, principal, "controller-check", automation.OperationRequest{Capability: "controller_update.check", Input: json.RawMessage(`{}`)})
	applyAutomationChangeset(t, server, principal, "controller-channel", automation.OperationRequest{Capability: "controller_update.set_channel", Input: json.RawMessage(`{"channel":"dev"}`)})
	applyAutomationChangeset(t, server, principal, "controller-cancel", automation.OperationRequest{Capability: "controller_update.cancel", Input: json.RawMessage(`{"confirm":true}`)})
	mu.Lock()
	defer mu.Unlock()
	if checkCalls != 1 || channelCalls != 1 || cancelCalls != 1 || channel != "dev" {
		t.Fatalf("updater calls check=%d channel=%d cancel=%d current_channel=%q", checkCalls, channelCalls, cancelCalls, channel)
	}
}

func TestControllerUpdateChannelAPI(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "obu-channel-")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "updater.sock")
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	updater := controllerupdate.NewService(controllerupdate.ServiceConfig{
		SocketPath:       socketPath,
		BinaryEnvPath:    binaryEnv,
		ControllerBinary: binary,
		StatePath:        filepath.Join(root, "updater-status.json"),
		WorkRoot:         filepath.Join(root, "work"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- updater.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("start updater: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("updater socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("updater shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("updater did not stop")
		}
	})

	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	status := request(t, handler, http.MethodGet, "/api/v2/ui/controller-update", adminToken, nil, http.StatusOK)
	if status["channel"] != "pinned" || status["status"] != "pinned" {
		t.Fatalf("unexpected update status: %#v", status)
	}
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/channel", adminToken, map[string]any{"channel": "nightly"}, http.StatusBadRequest)
	switched := request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/channel", adminToken, map[string]any{"channel": "dev"}, http.StatusOK)
	if switched["channel"] != "dev" || switched["status"] != "idle" {
		t.Fatalf("unexpected switched status: %#v", switched)
	}
	data, err := os.ReadFile(binaryEnv)
	if err != nil || !strings.Contains(string(data), `OBOARD_UPDATE_CHANNEL="dev"`) {
		t.Fatalf("channel was not persisted: %q, %v", data, err)
	}

	request(t, handler, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/channel", viewerLogin["token"].(string), map[string]any{"channel": "stable"}, http.StatusForbidden)
}
