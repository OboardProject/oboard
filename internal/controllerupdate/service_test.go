package controllerupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/version"
)

func TestPinnedCheckPersistsStatus(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "status.json")
	config := ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: statePath}
	service := NewService(config)
	status, err := service.check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pinned" || status.UpdateAvailable {
		t.Fatalf("unexpected pinned status: %#v", status)
	}
	restored := NewService(config)
	if restored.status.State != "pinned" || restored.status.LastCheckedAt == "" {
		t.Fatalf("status was not restored: %#v", restored.status)
	}
}

func TestDefaultServiceConfigUsesSelectedInstallDirectory(t *testing.T) {
	t.Setenv("OBOARD_INSTALL_DIR", "/data/oboard/")
	config := DefaultServiceConfig()
	if config.ControllerBinary != "/data/oboard/oboard-controller" ||
		config.UpdaterBinary != "/data/oboard/oboard-controller-updater" ||
		config.AIWorkerBinary != "/data/oboard/oboard-ai-worker" ||
		config.BinaryEnvPath != "/data/oboard/config/controller.env" ||
		config.StatePath != "/data/oboard/data/controller-update/status.json" ||
		config.RuntimeStatePath != "/data/oboard/data/controller-runtime.json" ||
		config.WebRoot != "/data/oboard/web/dist" ||
		config.DownloadsRoot != "/data/oboard/downloads" ||
		config.WorkRoot != "/data/oboard/data/controller-update" {
		t.Fatalf("unexpected custom binary paths: %#v", config)
	}

	t.Setenv("OBOARD_INSTALL_DIR", "../tmp/unsafe")
	config = DefaultServiceConfig()
	if config.ControllerBinary != "/opt/oboard/oboard-controller" || config.UpdaterBinary != "/opt/oboard/oboard-controller-updater" || config.AIWorkerBinary != "/opt/oboard/oboard-ai-worker" {
		t.Fatalf("unsafe install directory was accepted: %#v", config)
	}

	t.Setenv("OBOARD_INSTALL_DIR", "/usr/local/bin")
	if config = DefaultServiceConfig(); config.ControllerBinary != "/opt/oboard/oboard-controller" {
		t.Fatalf("shared system directory was accepted: %#v", config)
	}
}

func TestNormalizeInstallDir(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "/data/oboard", want: "/data/oboard"},
		{input: "/data/oboard/", want: "/data/oboard"},
		{input: "/usr/local/oboard", want: "/usr/local/oboard"},
	} {
		got, ok := normalizeInstallDir(test.input)
		if !ok || got != test.want {
			t.Errorf("normalizeInstallDir(%q) = %q, %v; want %q, true", test.input, got, ok, test.want)
		}
	}
	for _, input := range []string{"", "/", "/usr/local/bin", "/usr/local/sbin", "/usr/local/bin/oboard", "/var/lib", "/opt", "/data", "/home/user/oboard", "/proc/oboard", "data/oboard", "/data//oboard", "/data/../etc", "/data/oboard path", "/data/oboard;rm"} {
		if got, ok := normalizeInstallDir(input); ok {
			t.Errorf("normalizeInstallDir(%q) = %q, true; want rejection", input, got)
		}
	}
}

func TestValidateStagedBuild(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "controller")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"1.2.0\",\"build\":\"22\",\"commit\":\"abc\",\"date\":\"2026-07-24T00:00:00Z\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: "1.2.0", Build: "22", Commit: "abc", Date: "2026-07-24T00:00:00Z"}
	if err := validateStagedBuild(context.Background(), binary, manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Commit = "other"
	if err := validateStagedBuild(context.Background(), binary, manifest); err == nil {
		t.Fatal("mismatched staged Controller metadata was accepted")
	}
}

func TestTransientUpdateStateExpiresOnServiceRestart(t *testing.T) {
	for _, state := range []string{"checking", "downloading", "ready", "installing", "cancelling"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "oboard-controller")
			if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
				t.Fatal(err)
			}
			binaryEnv := filepath.Join(root, "controller.env")
			if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=dev\nOBOARD_ADDR=:1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(root, "status.json")
			stale := Status{State: state, UpdateAvailable: true, CanCancel: true, LastError: "stale"}
			data, _ := json.Marshal(stale)
			if err := os.WriteFile(statePath, data, 0o600); err != nil {
				t.Fatal(err)
			}

			service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: statePath})
			if service.status.State != "idle" || service.status.UpdateAvailable || service.status.CanCancel || service.status.LastError != "" {
				t.Fatalf("stale update state survived restart: %#v", service.status)
			}
			var persisted Status
			data, err := os.ReadFile(statePath)
			if err != nil || json.Unmarshal(data, &persisted) != nil {
				t.Fatalf("read reset update state: %v", err)
			}
			if persisted.State != "idle" || persisted.CanCancel {
				t.Fatalf("reset update state was not persisted: %#v", persisted)
			}
		})
	}
}

func TestMatchingBuildStopsStaleUpdateProgress(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=dev\nOBOARD_ADDR=:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldBuild, oldCommit, oldDate := version.Version, version.Build, version.Commit, version.Date
	version.Version, version.Build, version.Commit, version.Date = "dev", "build-42", "commit-42", "2026-07-28T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Build, version.Commit, version.Date = oldVersion, oldBuild, oldCommit, oldDate
	})
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})
	service.status = Status{
		Channel: "dev", State: "downloading", UpdateAvailable: true, CanCancel: true,
		Available: BuildInfo{Version: "dev", Build: "build-42", Commit: "commit-42", Date: "2026-07-28T00:00:00Z"},
	}
	status := service.decorateStatus(service.status)
	if status.State != "current" || status.UpdateAvailable || status.CanCancel {
		t.Fatalf("matching running build did not stop stale progress: %#v", status)
	}
}

func TestMatchingStableVersionStopsStaleUpdateProgress(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=stable\nOBOARD_ADDR=:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldBuild, oldCommit, oldDate := version.Version, version.Build, version.Commit, version.Date
	version.Version, version.Build, version.Commit, version.Date = "1.2.0", "current-build", "current-commit", "2026-07-28T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Build, version.Commit, version.Date = oldVersion, oldBuild, oldCommit, oldDate
	})
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})
	service.status = Status{
		Channel: "stable", State: "installing", UpdateAvailable: true,
		Available: BuildInfo{Version: "1.2.0", Build: "other-build", Commit: "other-commit"},
	}
	status := service.decorateStatus(service.status)
	if status.State != "current" || status.UpdateAvailable || status.CanCancel {
		t.Fatalf("matching stable version did not stop stale progress: %#v", status)
	}
}

func TestCancelBeforeInstallation(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=dev\nOBOARD_ADDR=:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})

	for _, state := range []string{"downloading", "ready"} {
		t.Run(state, func(t *testing.T) {
			installCtx, cancelInstall := context.WithCancel(context.Background())
			service.installCancel = cancelInstall
			service.status = Status{State: state, UpdateAvailable: true, CanCancel: true, Available: BuildInfo{Version: "dev", Build: "next", Commit: "next"}}
			recorder := httptest.NewRecorder()
			service.handleCancel(recorder, httptest.NewRequest(http.MethodPost, "/v1/cancel", nil))
			if recorder.Code != http.StatusOK || service.status.State != "cancelling" || service.status.CanCancel {
				t.Fatalf("%s cancellation response=%d status=%#v", state, recorder.Code, service.status)
			}
			select {
			case <-installCtx.Done():
			default:
				t.Fatalf("%s context was not cancelled", state)
			}
			finished, err := service.finishCancelledInstall()
			if err != nil || finished.State != "cancelled" || !finished.UpdateAvailable {
				t.Fatalf("cancelled %s final status=%#v err=%v", state, finished, err)
			}
		})
	}

	installCtx, cancelInstall := context.WithCancel(context.Background())
	defer cancelInstall()
	service.installCancel = cancelInstall
	service.status = Status{State: "installing", UpdateAvailable: true, CanCancel: false, Available: BuildInfo{Version: "dev", Build: "next", Commit: "next"}}
	recorder := httptest.NewRecorder()
	service.handleCancel(recorder, httptest.NewRequest(http.MethodPost, "/v1/cancel", nil))
	if recorder.Code != http.StatusConflict || service.status.State != "installing" {
		t.Fatalf("installation cancellation response=%d status=%#v", recorder.Code, service.status)
	}
	select {
	case <-installCtx.Done():
		t.Fatal("installation context was cancelled")
	default:
	}
}

func TestPrepareInstallationPublishesCancellableReadyWindowAndInstallingGrace(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=dev\nOBOARD_ADDR=:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{
		BinaryEnvPath:    binaryEnv,
		ControllerBinary: binary,
		StatePath:        filepath.Join(root, "status.json"),
		RuntimeStatePath: filepath.Join(root, "runtime.json"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.installCancel = cancel
	service.status = Status{State: "downloading", UpdateAvailable: true, CanCancel: true}

	var states []Status
	var delays []time.Duration
	service.config.Wait = func(_ context.Context, delay time.Duration) error {
		service.mu.Lock()
		states = append(states, service.status)
		service.mu.Unlock()
		delays = append(delays, delay)
		return nil
	}
	available := BuildInfo{Version: "dev", Build: "next-build", Commit: "next-commit"}
	status, err := service.prepareInstallation(ctx, available)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || len(delays) != 2 {
		t.Fatalf("transition waits = states %#v, delays %#v", states, delays)
	}
	if states[0].State != "ready" || !states[0].CanCancel || delays[0] < 3*time.Second {
		t.Fatalf("ready window was not cancellable and visible: state=%#v delay=%s", states[0], delays[0])
	}
	if states[1].State != "installing" || states[1].CanCancel || delays[1] < 3*time.Second || status.State != "installing" {
		t.Fatalf("installing grace was not published: states=%#v delays=%#v status=%#v", states, delays, status)
	}

	recorder := httptest.NewRecorder()
	service.handleCancel(recorder, httptest.NewRequest(http.MethodPost, "/v1/cancel", nil))
	if recorder.Code != http.StatusConflict || service.status.State != "installing" {
		t.Fatalf("installing cancellation response=%d status=%#v", recorder.Code, service.status)
	}
	select {
	case <-ctx.Done():
		t.Fatal("installing context was cancelled during the grace period")
	default:
	}
}
