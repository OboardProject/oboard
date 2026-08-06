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

func TestControllerUpdateAPIAndBackupRetention(t *testing.T) {
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
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/check", adminToken, nil, http.StatusOK)
	request(t, handler, http.MethodPost, "/api/v2/ui/settings", adminToken, map[string]any{"controller_auto_update_enabled": true}, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/install", adminToken, nil, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v2/ui/controller-update/cancel", adminToken, nil, http.StatusConflict)

	request(t, handler, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, handler, http.MethodGet, "/api/v2/ui/controller-update", viewerLogin["token"].(string), nil, http.StatusForbidden)

	for i := 0; i < controllerBackupRetention+2; i++ {
		if _, err := app.createControllerBackup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(app.controllerBackupDir, "oboard-before-update-*.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != controllerBackupRetention {
		t.Fatalf("retained %d backups, want %d", len(backups), controllerBackupRetention)
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
