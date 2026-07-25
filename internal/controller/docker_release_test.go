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
		"scripts/install-docker.sh":           {"VERSION_VALUE", "OBOARD_TAG", "OBOARD_INSTALL_METHOD=docker", "docker compose", "docker-cli", "docker-compose-v2", "docker_compose_ready", "install_compose_plugin_binary", "OBOARD_COMPOSE_VERSION", "checksums.txt", "wait_for_health", "dev|development|nightly", "generate_admin_password", "configure_bootstrap_admin", "设置超级管理员", "自动加入“管理员组”", "不能在面板中删除", "Controller 和 Agent 相互独立，也可以安装在同一台服务器上", "OBOARD_PORT 必须是 1 到 65535", "OBOARD_BASE_PATH", "OBOARD_LAN_IP", "OBOARD_PUBLIC_IP", "内网访问", "公网访问", "更新当前渠道", "2787", "wait_for_controller_updater", "curl --unix-socket /run/oboard/controller-updater.sock", "harden_controller_updater_unit", "prepare_controller_updater_runtime", "/var/lib/oboard/controller-update", "make_install_tmp", "write_controller_entrypoint", "./oboard-entrypoint:/usr/local/bin/oboard-entrypoint:ro"},
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
	for _, name := range []string{"install.sh", "update.sh"} {
		content, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "BASH_SOURCE[0]") || strings.Contains(string(content), `dirname -- "$0"`) {
			t.Errorf("%s does not safely locate its companion scripts", name)
		}
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		path := filepath.Join(root, "scripts", "build-docker.sh")
		if output, err := exec.Command(bash, "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("build-docker.sh syntax error: %v\n%s", err, output)
		}
	}
}

func TestDockerInstallerBuildsDetectedPanelURLs(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "scripts", "install-docker.sh"))
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte("\nneed_root\nensure_base_tools")
	index := strings.Index(string(content), string(marker))
	if index < 0 {
		t.Fatal("unable to isolate Docker installer functions")
	}
	harness := append([]byte(nil), content[:index]...)
	harness = append(harness, []byte("\nprint_access_urls\n")...)
	path := filepath.Join(t.TempDir(), "installer-network-test.sh")
	if err := os.WriteFile(path, harness, 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		lanIP    string
		publicIP string
		wants    []string
	}{
		{name: "IPv4", lanIP: "192.168.50.10", publicIP: "203.0.113.10", wants: []string{"内网访问：http://192.168.50.10:8443/panel", "公网访问：http://203.0.113.10:8443/panel"}},
		{name: "IPv6", lanIP: "fd00::10", publicIP: "2001:db8::10", wants: []string{"内网访问：http://[fd00::10]:8443/panel", "公网访问：http://[2001:db8::10]:8443/panel"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command("sh", path)
			cmd.Env = testCommandEnv(
				"OBOARD_ACTION=install",
				"OBOARD_DOCKER_DIR="+filepath.Join(t.TempDir(), "install"),
				"OBOARD_PORT=8443",
				"OBOARD_BASE_PATH=/panel",
				"OBOARD_LAN_IP="+test.lanIP,
				"OBOARD_PUBLIC_IP="+test.publicIP,
				"VERSION=latest",
			)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("URL detection harness failed: %v\n%s", err, output)
			}
			for _, want := range test.wants {
				if !strings.Contains(string(output), want) {
					t.Errorf("installer output missing %q:\n%s", want, output)
				}
			}
		})
	}

	binDir := t.TempDir()
	fakeIP := "#!/bin/sh\ncase \"$*\" in\n  '-4 route get 1.1.1.1') echo '1.1.1.1 via 10.20.30.1 dev eth0 src 10.20.30.40 uid 0' ;;\n  '-o -4 addr show scope global') echo '2: eth0 inet 10.20.30.40/24 brd 10.20.30.255 scope global eth0' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "ip"), []byte(fakeIP), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte("#!/bin/sh\necho 198.51.100.20\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", path)
	cmd.Env = testCommandEnv(
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"OBOARD_ACTION=install",
		"OBOARD_DOCKER_DIR="+filepath.Join(t.TempDir(), "install"),
		"OBOARD_PORT=2787",
		"OBOARD_BASE_PATH=",
		"OBOARD_LAN_IP=",
		"OBOARD_PUBLIC_IP=",
		"VERSION=latest",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("automatic URL detection failed: %v\n%s", err, output)
	}
	for _, want := range []string{"内网访问：http://10.20.30.40:2787", "公网访问：http://198.51.100.20:2787"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("automatic installer output missing %q:\n%s", want, output)
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

	installedRoot := filepath.Join(temp, "installed")
	if err := os.MkdirAll(installedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installedRoot, ".env"), []byte("OBOARD_TAG=dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(script, dockerRoot string, env ...string) string {
		t.Helper()
		cmd := exec.Command("bash", filepath.Join(temp, script))
		cmd.Env = testCommandEnv(append([]string{"OBOARD_DOCKER_DIR=" + dockerRoot, "OBOARD_INSTALL_METHOD=", "OBOARD_ACTION=", "VERSION="}, env...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", script, err, output)
		}
		return strings.TrimSpace(string(output))
	}

	if got := run("update.sh", installedRoot); got != "action=update version=preserve" {
		t.Fatalf("generic update changed the installed channel: %q", got)
	}
	if got := run("update.sh", installedRoot, "VERSION=dev"); got != "action=update version=dev" {
		t.Fatalf("explicit development channel not forwarded: %q", got)
	}
	if got := run("install.sh", installedRoot); got != "action=update version=preserve" {
		t.Fatalf("generic installer did not detect the existing Docker installation: %q", got)
	}
	if got := run("install.sh", installedRoot, "OBOARD_ACTION=install"); got != "action=update version=preserve" {
		t.Fatalf("existing Docker install did not switch to update: %q", got)
	}
	freshRoot := filepath.Join(temp, "fresh")
	if got := run("install.sh", freshRoot, "OBOARD_INSTALL_METHOD=docker"); got != "action=install version=latest" {
		t.Fatalf("fresh Docker install should default to latest: %q", got)
	}
	if got := run("install.sh", installedRoot, "OBOARD_ACTION=update"); got != "action=update version=preserve" {
		t.Fatalf("generic installer update changed the installed channel: %q", got)
	}
}

func TestGenericInstallDetectsExistingInstallationMethod(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	temp := t.TempDir()
	installer := filepath.Join(temp, "install.sh")
	content, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installer, content, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(temp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "oboard-controller"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(dockerRoot string) string {
		t.Helper()
		cmd := exec.Command("bash", installer)
		cmd.Env = testCommandEnv([]string{"INSTALL_DIR=" + binDir, "OBOARD_DOCKER_DIR=" + dockerRoot, "OBOARD_INSTALL_METHOD=", "OBOARD_ACTION=uninstall", "VERSION="}...)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("installer unexpectedly succeeded:\n%s", output)
		}
		return string(output)
	}

	if output := run(filepath.Join(temp, "no-docker")); !strings.Contains(output, "二进制主控不能通过此脚本卸载") {
		t.Fatalf("binary installation was not detected:\n%s", output)
	}
	dockerRoot := filepath.Join(temp, "docker")
	if err := os.MkdirAll(dockerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerRoot, ".env"), []byte("OBOARD_TAG=dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", installer)
	cmd.Env = testCommandEnv("INSTALL_DIR="+binDir, "OBOARD_DOCKER_DIR="+dockerRoot, "OBOARD_INSTALL_METHOD=", "OBOARD_ACTION=", "VERSION=")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "检测到二进制和 Docker 两种主控安装") {
		t.Fatalf("conflicting installations were not rejected: %v\n%s", err, output)
	}
}

func testCommandEnv(overrides ...string) []string {
	keys := map[string]bool{}
	for _, value := range overrides {
		key, _, _ := strings.Cut(value, "=")
		keys[key] = true
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !keys[key] {
			env = append(env, value)
		}
	}
	return append(env, overrides...)
}
