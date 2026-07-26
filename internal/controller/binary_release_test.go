package controller

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBinaryOnlyControllerReleaseAssets(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	required := map[string][]string{
		"scripts/build-release.sh":         {"create_tar_archive \"$stage\" \"$archive\" bin web downloads", "${arch}_install.tar.gz", "deploy/systemd", "deploy/openrc"},
		"scripts/install.sh":               {"OBOARD_UPDATE_CHANNEL", "oboard-controller-updater", "install_component controller", "prepare_controller_updater_runtime", "uninstall_controller", "OBOARD_PURGE_DATA", "drain_piped_script"},
		"scripts/verify-release.sh":        {"Testing Controller", "Building Web UI", "Building current-platform binaries", "cmd/controller-updater"},
		"scripts/fetch-agent-release.sh":   {"OBOARD_RELEASE_PUBLIC_KEY", "release-manifest.json.sig", "OBOARD_AGENT_CHANNEL"},
		".github/workflows/ci.yml":         {"contents: read", "Test Controller and release build inputs"},
		".github/workflows/dev-build.yml":  {"contents: write", "client-id: ${{ vars.OBOARD_RELEASE_APP_ID }}", "OBOARD_AGENT_CHANNEL: dev", "controller-release-manifest.json", "gh release create dev"},
		".github/workflows/prerelease.yml": {"contents: write", "client-id: ${{ vars.OBOARD_RELEASE_APP_ID }}", "OBOARD_AGENT_CHANNEL: release", "gh release create"},
		".github/workflows/release.yml":    {"contents: write", "client-id: ${{ vars.OBOARD_RELEASE_APP_ID }}", "OBOARD_AGENT_CHANNEL: release", "gh release create"},
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
		".github/workflows/ci.yml":         {"docker/", "ghcr.io", "packages: write", "Docker", "app-id:", "container:", "services:"},
		".github/workflows/dev-build.yml":  {"docker/", "ghcr.io", "packages: write", "Docker", "app-id:", "container:", "services:"},
		".github/workflows/prerelease.yml": {"docker/", "ghcr.io", "packages: write", "Docker", "app-id:", "container:", "services:"},
		".github/workflows/release.yml":    {"docker/", "ghcr.io", "packages: write", "Docker", "app-id:", "container:", "services:"},
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

func TestBinaryInstallerUninstallConsumesPipedScript(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "id"), []byte("#!/bin/sh\n[ \"${1:-}\" = -u ] && { echo 0; exit 0; }\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash")
	command.Env = controllerTestEnv(
		"INSTALL_DIR="+t.TempDir(),
		"OBOARD_ACTION=uninstall",
		"VERSION=",
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	var writeErr error
	for offset := 0; offset < len(content); offset += 256 {
		end := min(offset+256, len(content))
		if _, writeErr = stdin.Write(content[offset:end]); writeErr != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	closeErr := stdin.Close()
	waitErr := command.Wait()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("installer stopped reading its pipe: write=%v close=%v\n%s", writeErr, closeErr, output.String())
	}
	if waitErr != nil || !strings.Contains(output.String(), "无需卸载") {
		t.Fatalf("empty uninstall failed: %v\n%s", waitErr, output.String())
	}
}

func TestBinaryInstallerUninstallDataPolicy(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	original := extractShellFunction(t, string(content), "uninstall_controller")

	for _, test := range []struct {
		name  string
		purge bool
	}{
		{name: "preserve data"},
		{name: "purge data", purge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			temp := t.TempDir()
			paths := controllerUninstallTestPaths{
				install: filepath.Join(temp, "bin"),
				etc:     filepath.Join(temp, "etc", "oboard"),
				data:    filepath.Join(temp, "var", "lib", "oboard"),
				opt:     filepath.Join(temp, "opt", "oboard"),
				run:     filepath.Join(temp, "run", "oboard"),
				logs:    filepath.Join(temp, "var", "log"),
				systemd: filepath.Join(temp, "etc", "systemd", "system"),
				init:    filepath.Join(temp, "etc", "init.d"),
			}
			for _, path := range []string{
				filepath.Join(paths.install, "oboard-controller"),
				filepath.Join(paths.install, "oboard-controller-updater"),
				filepath.Join(paths.etc, "controller.env"),
				filepath.Join(paths.data, "oboard.sqlite"),
				filepath.Join(paths.data, "controller-update", "status.json"),
				filepath.Join(paths.opt, "web", "dist", "index.html"),
				filepath.Join(paths.opt, "downloads", "release-manifest.json"),
				filepath.Join(paths.run, "controller-updater.sock"),
				filepath.Join(paths.logs, "oboard-controller.log"),
				filepath.Join(paths.logs, "oboard-controller-updater.log"),
				filepath.Join(paths.systemd, "oboard-controller.service"),
				filepath.Join(paths.systemd, "oboard-controller-updater.service"),
				filepath.Join(paths.init, "oboard-controller"),
				filepath.Join(paths.init, "oboard-controller-updater"),
			} {
				writeInstallerTestFile(t, path)
			}

			function := rewriteControllerUninstallPaths(original, paths)
			harness := function + "\nuserdel() { :; }\ngroupdel() { :; }\n" +
				"INSTALLATION_EXISTS=1\nINSTALL_DIR=" + shellQuote(paths.install) + "\n" +
				"OBOARD_PURGE_DATA=" + map[bool]string{false: "0", true: "1"}[test.purge] + "\n" +
				"uninstall_controller unknown\n"
			command := exec.Command("bash", "-c", harness)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("uninstall failed: %v\n%s", err, output)
			}
			for _, path := range []string{paths.install, paths.opt, paths.run, filepath.Join(paths.data, "controller-update"), paths.systemd, paths.init} {
				if entries, err := os.ReadDir(path); err == nil && len(entries) != 0 {
					t.Errorf("runtime path was not cleared: %s", path)
				}
			}
			for _, path := range []string{filepath.Join(paths.logs, "oboard-controller.log"), filepath.Join(paths.logs, "oboard-controller-updater.log")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("runtime file was not removed: %s", path)
				}
			}
			for _, path := range []string{paths.etc, paths.data} {
				_, err := os.Stat(path)
				if test.purge && !os.IsNotExist(err) {
					t.Errorf("purged path still exists: %s", path)
				}
				if !test.purge && err != nil {
					t.Errorf("preserved path is missing: %s: %v", path, err)
				}
			}
		})
	}
}

func TestBinaryInstallerFallsBackToDevelopmentRelease(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	fakeCurl := `#!/bin/sh
case "$*" in
  *releases/latest*) exit 22 ;;
  *releases/download/dev/sha256sums.txt*) exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(fakeCurl), 0o700); err != nil {
		t.Fatal(err)
	}
	harness := extractShellFunction(t, string(content), "latest_version") + "\nREPO=OboardProject/oboard\nlatest_version\n"
	command := exec.Command("bash", "-c", harness)
	command.Env = controllerTestEnv("PATH=" + fakeBin + ":" + os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "dev" {
		t.Fatalf("development fallback failed: %v\n%s", err, output)
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

type controllerUninstallTestPaths struct {
	install string
	etc     string
	data    string
	opt     string
	run     string
	logs    string
	systemd string
	init    string
}

func rewriteControllerUninstallPaths(function string, paths controllerUninstallTestPaths) string {
	replacements := [][2]string{
		{"/etc/systemd/system", paths.systemd},
		{"/var/log", paths.logs},
		{"/var/lib/oboard", paths.data},
		{"/etc/oboard", paths.etc},
		{"/etc/init.d", paths.init},
		{"/opt/oboard", paths.opt},
		{"/run/oboard", paths.run},
	}
	for _, replacement := range replacements {
		function = strings.ReplaceAll(function, replacement[0], shellQuote(replacement[1]))
	}
	return function
}

func writeInstallerTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
}
