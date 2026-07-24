package controller

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/store"
)

func TestControllerUpdateAPIAndBackupRetention(t *testing.T) {
	root := t.TempDir()
	dockerRoot := filepath.Join(root, "docker")
	if err := os.MkdirAll(dockerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerRoot, ".env"), []byte("OBOARD_IMAGE=ghcr.io/oboardproject/oboard\nOBOARD_TAG=1.2.3\n"), 0o600); err != nil {
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
		SocketPath:    socketPath,
		DockerRoot:    dockerRoot,
		BinaryEnvPath: filepath.Join(root, "controller.env"),
		StatePath:     statePath,
		WorkRoot:      filepath.Join(root, "work"),
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
	request(t, handler, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	status := request(t, handler, http.MethodGet, "/api/v1/controller-update", adminToken, nil, http.StatusOK)
	if status["install_method"] != "docker" || status["channel"] != "pinned" || status["status"] != "pinned" {
		t.Fatalf("unexpected update status: %#v", status)
	}
	request(t, handler, http.MethodPost, "/api/v1/controller-update/check", adminToken, nil, http.StatusOK)
	request(t, handler, http.MethodPost, "/api/v1/settings", adminToken, map[string]any{"controller_auto_update_enabled": true}, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v1/controller-update/install", adminToken, nil, http.StatusConflict)

	request(t, handler, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, handler, http.MethodGet, "/api/v1/controller-update", viewerLogin["token"].(string), nil, http.StatusForbidden)

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
	t.Setenv("OBOARD_INSTALL_METHOD", "docker")
	t.Setenv("OBOARD_UPDATE_CHANNEL", "latest")
	status := (&Server{}).fallbackControllerUpdateStatus()
	if status.InstallMethod != "docker" || status.Channel != "stable" || status.State != "unavailable" {
		t.Fatalf("unexpected fallback status: %#v", status)
	}
	t.Setenv("OBOARD_UPDATE_CHANNEL", "1.2.3")
	if status := (&Server{}).fallbackControllerUpdateStatus(); status.Channel != "pinned" {
		t.Fatalf("exact Docker tag should be pinned: %#v", status)
	}
}
