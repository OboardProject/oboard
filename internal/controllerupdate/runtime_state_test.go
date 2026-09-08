package controllerupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeStateUsesCurrentBasePathForVersionAndHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/current/api/v1/ui/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"dev","build":"running-build","commit":"running-commit","built_at":"2026-07-28T00:00:00Z"}`))
		case "/current/healthz":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	runtimePath := filepath.Join(root, RuntimeStateName)
	listenAddress := strings.TrimPrefix(server.URL, "http://")
	if err := WriteRuntimeState(runtimePath, RuntimeState{ListenAddress: listenAddress, BasePaths: []string{"/current", "/previous"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime state mode = %v", info.Mode().Perm())
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_ADDR="+listenAddress+"\nOBOARD_BASE_PATH=/stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{
		BinaryEnvPath:      binaryEnv,
		RuntimeStatePath:   runtimePath,
		StatePath:          filepath.Join(root, "status.json"),
		HealthClient:       server.Client(),
		HealthTimeout:      time.Second,
		HealthPollInterval: time.Millisecond,
	})
	build := service.currentBuildInfo()
	if build.Build != "running-build" || build.Commit != "running-commit" {
		t.Fatalf("current build did not use runtime base path: %#v", build)
	}
	if err := service.waitHealth(t.Context()); err != nil {
		t.Fatalf("runtime health check failed: %v", err)
	}
}

func TestHealthURLsRejectInvalidOrNonLocalRuntimeState(t *testing.T) {
	root := t.TempDir()
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_ADDR=127.0.0.1:2787\nOBOARD_BASE_PATH=/fallback\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		state RuntimeState
	}{
		{name: "non-local address", state: RuntimeState{ListenAddress: "203.0.113.10:8080", BasePaths: []string{"/unsafe"}}},
		{name: "invalid base path", state: RuntimeState{ListenAddress: "127.0.0.1:8080", BasePaths: []string{"/../unsafe"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimePath := filepath.Join(t.TempDir(), RuntimeStateName)
			if err := WriteRuntimeState(runtimePath, test.state); err != nil {
				t.Fatal(err)
			}
			service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, RuntimeStatePath: runtimePath, StatePath: filepath.Join(root, fmt.Sprintf("%s.json", strings.ReplaceAll(test.name, " ", "-")))})
			urls := service.healthURLs("/healthz")
			if len(urls) != 1 || urls[0] != "http://127.0.0.1:2787/fallback/healthz" {
				t.Fatalf("unsafe runtime state was accepted: %#v", urls)
			}
		})
	}
}

func TestWaitHealthReloadsRuntimePathAfterRestart(t *testing.T) {
	root := t.TempDir()
	runtimePath := filepath.Join(root, RuntimeStateName)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/qzq/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	listenAddress := strings.TrimPrefix(server.URL, "http://")
	envPath := filepath.Join(root, "controller.env")
	if err := os.WriteFile(envPath, []byte("OBOARD_ADDR="+listenAddress+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	polls := 0
	service := NewService(ServiceConfig{
		BinaryEnvPath:    envPath,
		RuntimeStatePath: runtimePath,
		StatePath:        filepath.Join(root, "status.json"),
		HealthClient:     server.Client(),
		HealthTimeout:    time.Second,
		Wait: func(ctx context.Context, _ time.Duration) error {
			polls++
			return WriteRuntimeState(runtimePath, RuntimeState{ListenAddress: listenAddress, BasePaths: []string{"/qzq"}})
		},
	})
	if err := service.waitHealth(t.Context()); err != nil {
		t.Fatal(err)
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want one stale-path retry", polls)
	}
}

func TestWaitHealthFailurePreservesAllProbeOutcomes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/current/healthz" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	listenAddress := strings.TrimPrefix(server.URL, "http://")
	envPath := filepath.Join(root, "controller.env")
	if err := os.WriteFile(envPath, []byte("OBOARD_ADDR="+listenAddress+"\nOBOARD_BASE_PATH=/stale\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(root, RuntimeStateName)
	if err := WriteRuntimeState(runtimePath, RuntimeState{ListenAddress: listenAddress, BasePaths: []string{"/current"}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{
		BinaryEnvPath: envPath, RuntimeStatePath: runtimePath, StatePath: filepath.Join(root, "status.json"),
		HealthClient: server.Client(), HealthTimeout: time.Second,
		Wait: func(context.Context, time.Duration) error { return context.DeadlineExceeded },
	})
	err := service.waitHealth(t.Context())
	if err == nil || !strings.Contains(err.Error(), "/current/healthz HTTP 503") || !strings.Contains(err.Error(), "/stale/healthz HTTP 404") {
		t.Fatalf("health failure lost a probe outcome: %v", err)
	}
}
