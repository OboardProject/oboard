package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDockerInstallAndReleaseAssets(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	checks := map[string][]string{
		"deploy/docker/Dockerfile.controller": {"EXPOSE 2787", "OBOARD_ADDR=:2787", "OBOARD_DOWNLOADS=/app/downloads", "OBOARD_BASE_PATH", "HEALTHCHECK", "entrypoint.sh"},
		"deploy/docker/entrypoint.sh":         {"su-exec", "OBOARD_UPDATER_GID", "controller-updater.sock"},
		"deploy/docker-compose.yml":           {"ghcr.io/oboardproject/oboard", "${OBOARD_PORT:-2787}:2787", "OBOARD_BASE_PATH", "./data:/app/data", "controller-updater.sock", "OBOARD_UPDATER_GID", "read_only: true", "cap_drop:", "cap_add:", "CHOWN", "SETGID", "SETUID", "set OBOARD_ADMIN_PASSWORD"},
		"scripts/install-docker.sh":           {"VERSION_VALUE", "OBOARD_TAG", "docker compose", "docker-cli", "docker-compose-v2", "docker_compose_ready", "install_compose_plugin_binary", "OBOARD_COMPOSE_VERSION", "checksums.txt", "wait_for_health", "dev|development|nightly", "generate_admin_password", "configure_bootstrap_admin", "设置超级管理员", "自动加入“管理员组”", "不能在面板中删除", "Controller 和 Agent 相互独立，也可以安装在同一台服务器上", "OBOARD_PORT 必须是 1 到 65535", "OBOARD_BASE_PATH", "更新当前渠道", "2787", "wait_for_controller_updater", "curl --unix-socket /run/oboard/controller-updater.sock", "harden_controller_updater_unit", "prepare_controller_updater_runtime", "/var/lib/oboard/controller-update", "make_install_tmp", "write_controller_entrypoint", "./oboard-entrypoint:/usr/local/bin/oboard-entrypoint:ro"},
		"scripts/verify-release.sh":           {"Testing Controller", "Building Web UI", "Building current-platform binaries", "cmd/controller-updater"},
		"scripts/fetch-agent-release.sh":      {"OBOARD_RELEASE_PUBLIC_KEY", "gh release download", "release-manifest.json.sig", "OBOARD_AGENT_CHANNEL", "OBOARD_AGENT_RELEASE_WAIT_ATTEMPTS"},
		".github/workflows/ci.yml":            {"verify-release.sh", "go-version: '1.25.12'", "node-version: '22'"},
		".github/workflows/dev-build.yml":     {"packages: write", "linux/amd64,linux/arm64", ":dev", "create-github-app-token", "OBOARD_AGENT_CHANNEL: dev", "controller-release-manifest.json", "gh release create dev"},
		".github/workflows/prerelease.yml":    {":prerelease", "build-push-action", "create-github-app-token", "OBOARD_AGENT_CHANNEL: release"},
		".github/workflows/release.yml":       {":latest", "build-push-action", "create-github-app-token", "OBOARD_AGENT_CHANNEL: release"},
	}
	for name, wants := range checks {
		path := filepath.Join(root, filepath.FromSlash(name))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, want := range wants {
			if !strings.Contains(string(content), want) {
				t.Errorf("%s missing %q", name, want)
			}
		}
	}

	for _, script := range []string{"install-docker.sh", "update-docker.sh", "prepare-docker-downloads.sh"} {
		path := filepath.Join(root, "scripts", script)
		cmd := exec.Command("sh", "-n", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s syntax error: %v\n%s", script, err, output)
		}
	}
	installer, err := os.ReadFile(filepath.Join(root, "scripts", "install-docker.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"docker-compose version", "docker-compose-shim", "exec docker-compose"} {
		if strings.Contains(string(installer), removed) {
			t.Errorf("install-docker.sh still contains removed Compose v1 compatibility %q", removed)
		}
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		path := filepath.Join(root, "scripts", "build-docker.sh")
		if output, err := exec.Command(bash, "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("build-docker.sh syntax error: %v\n%s", err, output)
		}
	}
}

func TestGenericDockerUpdatePreservesInstalledChannel(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	temp := t.TempDir()

	for _, name := range []string{"install.sh", "update.sh"} {
		content, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(temp, name), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := []byte("#!/bin/sh\nprintf 'action=%s version=%s\\n' \"${OBOARD_ACTION:-unset}\" \"${VERSION:-preserve}\"\n")
	if err := os.WriteFile(filepath.Join(temp, "install-docker.sh"), fake, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(script string, env ...string) string {
		t.Helper()
		cmd := exec.Command("bash", filepath.Join(temp, script))
		cmd.Env = append(os.Environ(), append([]string{"OBOARD_INSTALL_METHOD=docker", "VERSION="}, env...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", script, err, output)
		}
		return strings.TrimSpace(string(output))
	}

	if got := run("update.sh"); got != "action=update version=preserve" {
		t.Fatalf("generic update changed the installed channel: %q", got)
	}
	if got := run("update.sh", "VERSION=dev"); got != "action=update version=dev" {
		t.Fatalf("explicit development channel not forwarded: %q", got)
	}
	if got := run("install.sh", "OBOARD_ACTION=update"); got != "action=update version=preserve" {
		t.Fatalf("generic installer update changed the installed channel: %q", got)
	}
	if got := run("install.sh", "OBOARD_ACTION=install"); got != "action=install version=latest" {
		t.Fatalf("fresh Docker install should default to latest: %q", got)
	}
}
