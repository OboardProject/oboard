package controller

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
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
