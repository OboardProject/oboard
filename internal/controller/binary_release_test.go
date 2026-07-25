package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBinaryOnlyControllerReleaseAssets(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	required := map[string][]string{
		"scripts/build-release.sh":         {"create_tar_archive \"$stage\" \"$archive\" bin web downloads", "${arch}_install.tar.gz", "deploy/systemd", "deploy/openrc"},
		"scripts/install.sh":               {"OBOARD_UPDATE_CHANNEL", "oboard-controller-updater", "install_component controller", "prepare_controller_updater_runtime"},
		"scripts/verify-release.sh":        {"Testing Controller", "Building Web UI", "Building current-platform binaries", "cmd/controller-updater"},
		"scripts/fetch-agent-release.sh":   {"OBOARD_RELEASE_PUBLIC_KEY", "release-manifest.json.sig", "OBOARD_AGENT_CHANNEL"},
		".github/workflows/dev-build.yml":  {"contents: write", "OBOARD_AGENT_CHANNEL: dev", "controller-release-manifest.json", "gh release create dev"},
		".github/workflows/prerelease.yml": {"contents: write", "OBOARD_AGENT_CHANNEL: release", "gh release create"},
		".github/workflows/release.yml":    {"contents: write", "OBOARD_AGENT_CHANNEL: release", "gh release create"},
	}
	for name, fragments := range required {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(content), fragment) {
				t.Errorf("%s missing %q", name, fragment)
			}
		}
	}

	for _, name := range []string{
		".dockerignore",
		"deploy/docker",
		"deploy/docker-compose.yml",
		"deploy/docker/Dockerfile.controller",
		"deploy/docker/entrypoint.sh",
		"scripts/build-docker.sh",
		"scripts/install-docker.sh",
		"scripts/prepare-docker-downloads.sh",
		"scripts/update-docker.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Errorf("removed Controller Docker asset still exists: %s", name)
		}
	}

	for name, fragments := range map[string][]string{
		"scripts/build-release.sh":         {"deploy/docker", "install-docker", "update-docker"},
		"scripts/install.sh":               {"OBOARD_DOCKER", "OBOARD_INSTALL_METHOD", "install-docker", "docker compose"},
		".github/workflows/dev-build.yml":  {"docker/", "ghcr.io", "packages: write", "Docker"},
		".github/workflows/prerelease.yml": {"docker/", "ghcr.io", "packages: write", "Docker"},
		".github/workflows/release.yml":    {"docker/", "ghcr.io", "packages: write", "Docker"},
	} {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if strings.Contains(string(content), fragment) {
				t.Errorf("%s still contains removed Controller Docker fragment %q", name, fragment)
			}
		}
	}
	installer, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(installer), "controller_public_url") || !strings.Contains(string(installer), "controller_agent_url") {
		t.Fatal("binary installer contains a stale Controller URL helper reference")
	}

	if bash, err := exec.LookPath("bash"); err == nil {
		for _, name := range []string{"scripts/build-release.sh", "scripts/install.sh", "scripts/update.sh", "scripts/deploy-test-controller.sh"} {
			path := filepath.Join(root, filepath.FromSlash(name))
			if output, err := exec.Command(bash, "-n", path).CombinedOutput(); err != nil {
				t.Fatalf("%s syntax error: %v\n%s", name, err, output)
			}
		}
	}
}

func TestBinaryInstallerDetectsExistingController(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	installer := filepath.Join(t.TempDir(), "install.sh")
	content, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installer, content, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "oboard-controller"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", installer)
	command.Env = controllerTestEnv("INSTALL_DIR="+binDir, "OBOARD_ACTION=uninstall", "VERSION=")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "二进制主控不能通过此脚本卸载") {
		t.Fatalf("existing binary installation was not detected: %v\n%s", err, output)
	}
}

func TestBinaryInstallerBuildsDetectedPanelURLs(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var harness strings.Builder
	for _, name := range []string{"valid_ipv4", "valid_ipv6", "is_private_ipv4", "detect_lan_ip", "detect_public_ip", "controller_base_path", "controller_port", "controller_url", "configured_public_url", "print_controller_urls"} {
		harness.WriteString(extractShellFunction(t, string(content), name))
		harness.WriteByte('\n')
	}
	harness.WriteString("print_controller_urls\n")
	path := filepath.Join(t.TempDir(), "installer-network-test.sh")
	if err := os.WriteFile(path, []byte(harness.String()), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, lanIP, publicIP string
		wants                 []string
	}{
		{name: "IPv4", lanIP: "192.168.50.10", publicIP: "203.0.113.10", wants: []string{"内网访问：http://192.168.50.10:8443/panel", "公网访问：http://203.0.113.10:8443/panel"}},
		{name: "IPv6", lanIP: "fd00::10", publicIP: "2001:db8::10", wants: []string{"内网访问：http://[fd00::10]:8443/panel", "公网访问：http://[2001:db8::10]:8443/panel"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("bash", path)
			command.Env = controllerTestEnv("OBOARD_ADDR=:8443", "OBOARD_BASE_PATH=/panel", "OBOARD_LAN_IP="+test.lanIP, "OBOARD_PUBLIC_IP="+test.publicIP, "OBOARD_PUBLIC_URL=")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("URL detection failed: %v\n%s", err, output)
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
	command := exec.Command("bash", path)
	command.Env = controllerTestEnv("PATH="+binDir+":"+os.Getenv("PATH"), "OBOARD_ADDR=:2787", "OBOARD_BASE_PATH=", "OBOARD_LAN_IP=", "OBOARD_PUBLIC_IP=", "OBOARD_PUBLIC_URL=")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("automatic URL detection failed: %v\n%s", err, output)
	}
	for _, want := range []string{"内网访问：http://10.20.30.40:2787", "公网访问：http://198.51.100.20:2787"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("automatic installer output missing %q:\n%s", want, output)
		}
	}
}

func controllerTestEnv(overrides ...string) []string {
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
