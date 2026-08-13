package controllerupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestReexecUpdaterUsesInstalledBinary(t *testing.T) {
	var path string
	var args []string
	service := NewService(ServiceConfig{
		UpdaterBinary: "/opt/test/oboard-controller-updater",
		ReexecUpdater: func(gotPath string, gotArgs, _ []string) error {
			path = gotPath
			args = append([]string(nil), gotArgs...)
			return nil
		},
	})
	service.reexecUpdater()
	if path != "/opt/test/oboard-controller-updater" || len(args) != 1 || args[0] != path {
		t.Fatalf("unexpected updater re-exec: path=%q args=%#v", path, args)
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
	version.Version, version.Build, version.Commit, version.Date = "dev", "20260701000000", "commit-42", "2026-07-28T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Build, version.Commit, version.Date = oldVersion, oldBuild, oldCommit, oldDate
	})
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})
	service.status = Status{
		Channel: "dev", State: "downloading", UpdateAvailable: true, CanCancel: true,
		Available: BuildInfo{Version: "dev", Build: "20260701000000", Commit: "commit-42", Date: "2026-07-28T00:00:00Z"},
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

func TestStatusPollingDoesNotCancelActiveInstallingUpdate(t *testing.T) {
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
	version.Version, version.Build, version.Commit, version.Date = "dev", "20260813182613", "target-commit", "2026-08-13T18:26:13Z"
	t.Cleanup(func() {
		version.Version, version.Build, version.Commit, version.Date = oldVersion, oldBuild, oldCommit, oldDate
	})
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})
	installCtx, cancelInstall := context.WithCancel(context.Background())
	defer cancelInstall()
	service.installCancel = cancelInstall
	service.status = Status{
		Channel: "dev", State: "installing", UpdateAvailable: true,
		Available: BuildInfo{Version: "dev", Build: "20260813182613", Commit: "target-commit", Date: "2026-08-13T18:26:13Z"},
	}

	recorder := httptest.NewRecorder()
	service.handleStatus(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var status Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "installing" || !status.UpdateAvailable {
		t.Fatalf("active installation was collapsed before finalization: %#v", status)
	}
	select {
	case <-installCtx.Done():
		t.Fatal("status polling cancelled the active installation")
	default:
	}
}

func TestChannelSwitchRewritesEnvironmentFile(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_ADDR=:1\nOBOARD_UPDATE_CHANNEL=stable\nOBOARD_DB=/data/oboard.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})

	recorder := httptest.NewRecorder()
	service.handleChannel(recorder, httptest.NewRequest(http.MethodPost, "/v1/channel", strings.NewReader(`{"channel":"dev"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("channel switch response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var status Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Channel != "dev" || status.State != "idle" || status.UpdateAvailable || status.Available.Version != "" || status.LastError != "" {
		t.Fatalf("unexpected switched status: %#v", status)
	}
	data, err := os.ReadFile(binaryEnv)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `OBOARD_UPDATE_CHANNEL="dev"`) {
		t.Fatalf("channel not persisted in environment file: %s", text)
	}
	if strings.Count(text, "OBOARD_UPDATE_CHANNEL=") != 1 || !strings.Contains(text, "OBOARD_ADDR=:1") || !strings.Contains(text, "OBOARD_DB=/data/oboard.sqlite") {
		t.Fatalf("environment file lines were not preserved: %s", text)
	}
	info, err := os.Stat(binaryEnv)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("environment file mode = %v, err=%v", info.Mode(), err)
	}
}

func TestChannelSwitchValidationAndPinned(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})

	for _, body := range []string{`{"channel":"pinned"}`, `{"channel":"latest"}`, `{"channel":123}`, `{"channel":"dev","extra":1}`, `not json`} {
		recorder := httptest.NewRecorder()
		service.handleChannel(recorder, httptest.NewRequest(http.MethodPost, "/v1/channel", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid request %q response=%d body=%s", body, recorder.Code, recorder.Body.String())
		}
	}

	recorder := httptest.NewRecorder()
	service.handleChannel(recorder, httptest.NewRequest(http.MethodPost, "/v1/channel", strings.NewReader(`{"channel":"stable"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("pinned channel switch response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	data, err := os.ReadFile(binaryEnv)
	if err != nil || !strings.Contains(string(data), `OBOARD_UPDATE_CHANNEL="stable"`) {
		t.Fatalf("pinned channel was not switched: %q, %v", data, err)
	}
	var status Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Channel != "stable" || status.State != "idle" {
		t.Fatalf("unexpected switched status: %#v", status)
	}

	service.status = Status{State: "available", UpdateAvailable: true, Available: BuildInfo{Version: "9.9.9", Build: "20260801000000", Commit: "next"}}
	recorder = httptest.NewRecorder()
	service.handleChannel(recorder, httptest.NewRequest(http.MethodPost, "/v1/channel", strings.NewReader(`{"channel":"stable"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("same-channel switch response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.Available.Version != "9.9.9" || status.State != "available" {
		t.Fatalf("same-channel switch reset update state: %#v", status)
	}
}

func TestChannelSwitchRejectsTransientState(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=stable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})

	for _, state := range []string{"checking", "downloading", "ready", "installing", "cancelling"} {
		t.Run(state, func(t *testing.T) {
			service.status = Status{State: state, UpdateAvailable: true, CanCancel: state == "downloading" || state == "ready"}
			recorder := httptest.NewRecorder()
			service.handleChannel(recorder, httptest.NewRequest(http.MethodPost, "/v1/channel", strings.NewReader(`{"channel":"dev"}`)))
			if recorder.Code != http.StatusConflict {
				t.Fatalf("%s channel switch response=%d body=%s", state, recorder.Code, recorder.Body.String())
			}
			var status Status
			if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.LastError == "" {
				t.Fatalf("%s switch did not report an error", state)
			}
			data, err := os.ReadFile(binaryEnv)
			if err != nil || !strings.Contains(string(data), "OBOARD_UPDATE_CHANNEL=stable") {
				t.Fatalf("%s switch changed the environment file: %q, %v", state, data, err)
			}
		})
	}
}

func TestChannelSwitchCreatesMissingEnvironmentFile(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{
		BinaryEnvPath:    filepath.Join(root, "config", "controller.env"),
		ControllerBinary: binary,
		StatePath:        filepath.Join(root, "status.json"),
	})
	recorder := httptest.NewRecorder()
	service.handleChannel(recorder, httptest.NewRequest(http.MethodPost, "/v1/channel", strings.NewReader(`{"channel":"stable"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("channel switch response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "config", "controller.env"))
	if err != nil || !strings.Contains(string(data), `OBOARD_UPDATE_CHANNEL="stable"`) {
		t.Fatalf("environment file was not created: %q, %v", data, err)
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
	oldVersion, oldBuild, oldCommit, oldDate := version.Version, version.Build, version.Commit, version.Date
	version.Version, version.Build, version.Commit, version.Date = "dev", "20260701000000", "commit-old", "2026-07-28T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Build, version.Commit, version.Date = oldVersion, oldBuild, oldCommit, oldDate
	})
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})

	for _, state := range []string{"downloading", "ready"} {
		t.Run(state, func(t *testing.T) {
			installCtx, cancelInstall := context.WithCancel(context.Background())
			service.installCancel = cancelInstall
			service.status = Status{State: state, UpdateAvailable: true, CanCancel: true, Available: BuildInfo{Version: "dev", Build: "20260802000000", Commit: "next"}}
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
	service.status = Status{State: "installing", UpdateAvailable: true, CanCancel: false, Available: BuildInfo{Version: "dev", Build: "20260802000000", Commit: "next"}}
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
	oldVersion, oldBuild, oldCommit, oldDate := version.Version, version.Build, version.Commit, version.Date
	version.Version, version.Build, version.Commit, version.Date = "dev", "20260701000000", "commit-old", "2026-07-28T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Build, version.Commit, version.Date = oldVersion, oldBuild, oldCommit, oldDate
	})
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
	available := BuildInfo{Version: "dev", Build: "20260802000000", Commit: "next-commit"}
	approval := make(chan struct{})
	close(approval)
	status, err := service.prepareInstallation(ctx, available, approval)
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

func TestPreparedInstallationWaitsForExplicitApproval(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldBuild, oldCommit, oldDate := version.Version, version.Build, version.Commit, version.Date
	version.Version, version.Build, version.Commit, version.Date = "dev", "20260701000000", "commit-old", "2026-07-28T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Build, version.Commit, version.Date = oldVersion, oldBuild, oldCommit, oldDate
	})
	service := NewService(ServiceConfig{
		BinaryEnvPath:      binaryEnv,
		ControllerBinary:   binary,
		StatePath:          filepath.Join(root, "status.json"),
		RuntimeStatePath:   filepath.Join(root, "runtime.json"),
		ReadyWindow:        time.Nanosecond,
		InstallGracePeriod: time.Nanosecond,
		Wait: func(context.Context, time.Duration) error {
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	approval := make(chan struct{})
	service.mu.Lock()
	service.installCancel = cancel
	service.installApprove = approval
	service.status = Status{State: "downloading", UpdateAvailable: true, CanCancel: true}
	service.mu.Unlock()

	type result struct {
		status Status
		err    error
	}
	done := make(chan result, 1)
	available := BuildInfo{Version: "dev", Build: "20260802000000", Commit: "next-commit"}
	go func() {
		status, err := service.prepareInstallation(ctx, available, approval)
		done <- result{status: status, err: err}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		state := service.status.State
		service.mu.Unlock()
		if state == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prepared update did not reach ready state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case got := <-done:
		t.Fatalf("installation advanced without approval: %#v", got)
	default:
	}

	recorder := httptest.NewRecorder()
	service.handleInstall(recorder, httptest.NewRequest(http.MethodPost, "/v1/install", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("install approval response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case got := <-done:
		if got.err != nil || got.status.State != "installing" || got.status.CanCancel {
			t.Fatalf("approved installation result=%#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approved installation did not advance")
	}
}
