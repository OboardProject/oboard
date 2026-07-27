package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestControllerInstallScriptUserGuidanceAndSyntax(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "scripts", "install.sh"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"#!/bin/sh",
		`COMPONENT=${COMPONENT:-${1:-controller}}`,
		"OBoard 主控安装 / 更新完成",
		"面板地址",
		"设置超级管理员",
		"超级管理员账号：",
		"自动加入“管理员组”",
		"不能在面板中删除",
		"configure_bootstrap_admin",
		"OBOARD_ADMIN_USERNAME",
		"OBOARD_ADMIN_PASSWORD",
		"generate_admin_password",
		"超级管理员密码：$BOOTSTRAP_ADMIN_PASSWORD_VALUE",
		"该密码只显示这一次",
		"登录后请立即修改密码",
		"clear_bootstrap_admin_password",
		"unset_controller_env_value",
		"wait_for_controller_ready",
		"prepare_controller_env",
		"OBOARD_BASE_PATH",
		"install_agent_from_controller",
		"不会互相覆盖",
		"COMPONENT=agent",
		"INSTALL_DIR_INPUT",
		"normalize_install_dir",
		"install_dir_from_input",
		"请输入安装目录（留空为/opt/oboard）：",
		"/opt/oboard",
		"/usr/local/sbin",
		"OBOARD_INSTALL_DIR",
		"resolve_controller_install_dir",
		"render_service_file",
		"make_install_tmp",
		"OBOARD_TMPDIR",
		"pkg_install",
		"ensure_base_tools",
		"command -v install",
		"packages=\"$packages coreutils\"",
		"ACME_SH_VERSION=3.1.4",
		"ACME_SH_SHA256=fcabf274d4f96966ec933879ae0257266e8ef2f7d16161f14b84dd896c0cac32",
		"install_pinned_acme_sh",
		"sha256_file",
		"create_system_user",
		"detect_virt_hint",
		"centos",
		"rhel",
		"rocky",
		"almalinux",
		"OBOARD_UPDATE_CHANNEL",
		"oboard-controller-updater",
		"prepare_controller_updater_runtime",
		"wait_for_controller_updater",
		"curl --unix-socket /run/oboard/controller-updater.sock",
		"/var/lib/oboard/controller-update",
		"uninstall_controller",
		"OBoard 主控已卸载",
		"配置和数据已保留",
		"OBOARD_PURGE_DATA",
		"resolve_purge_data",
		"是否同时删除主控的配置和数据",
		"删除请直接回车，保留请输入 n [Y/n]",
		"当前无法交互确认，已保留",
		"当前暂无可用的稳定版，将安装最新开发版",
		"安装包下载失败",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("controller installer missing %q", want)
		}
	}
	if strings.Contains(text, "首次登录密码：admin") {
		t.Fatal("controller installer still advertises a well-known default password")
	}
	if strings.Contains(text, "grep -A2 'first administrator'") {
		t.Fatal("controller installer still sends operators to the service log for the bootstrap password")
	}
	if strings.Contains(text, `install_component agent`) || strings.Contains(text, `install_component sb`) {
		t.Fatal("controller installer still installs Agent artifacts from the controller release")
	}
	for _, shellName := range []string{"bash", "dash"} {
		if shell, err := exec.LookPath(shellName); err == nil {
			cmd := exec.Command(shell, "-n", path)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("controller installer %s syntax error: %v\n%s", shellName, err, output)
			}
		}
	}
}

func TestControllerInstallScriptACMEFallback(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "scripts", "install.sh"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertACMEInstallerBehavior(t, string(content))
	assertPackageManagerDispatch(t, string(content))
	assertInstallToolBootstrap(t, string(content))
}

func TestControllerInstallDirectorySelection(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "scripts", "install.sh"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(content)
	assertInstallDirectoryInputs(t, script)

	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
	}
	if err != nil {
		t.Skip("a POSIX shell is unavailable")
	}
	t.Run("restore persisted directory", func(t *testing.T) {
		envPath := filepath.Join(t.TempDir(), "controller.env")
		if err := os.WriteFile(envPath, []byte("OBOARD_INSTALL_DIR=/data/oboard/\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		configured := strings.ReplaceAll(extractShellFunction(t, script, "configured_controller_install_dir"), "/etc/oboard/controller.env", shellQuote(envPath))
		harness := strings.Join([]string{
			extractShellFunction(t, script, "normalize_install_dir"),
			extractShellFunction(t, script, "install_dir_from_input"),
			configured,
			extractShellFunction(t, script, "choose_install_dir"),
			extractShellFunction(t, script, "resolve_controller_install_dir"),
			"INSTALL_DIR_INPUT=",
			"INSTALL_DIR=",
			"INSTALLATION_EXISTS=1",
			"ACTION=update",
			"resolve_controller_install_dir",
			"printf 'resolved=%s\\n' \"$INSTALL_DIR\"",
		}, "\n")
		output, err := exec.Command(shell, "-c", harness).CombinedOutput()
		if err != nil || !strings.Contains(string(output), "resolved=/data/oboard") {
			t.Fatalf("persisted install directory was not restored: %v\n%s", err, output)
		}
	})

	t.Run("reject directory change during update", func(t *testing.T) {
		envPath := filepath.Join(t.TempDir(), "controller.env")
		if err := os.WriteFile(envPath, []byte("OBOARD_INSTALL_DIR=/data/oboard\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		configured := strings.ReplaceAll(extractShellFunction(t, script, "configured_controller_install_dir"), "/etc/oboard/controller.env", shellQuote(envPath))
		harness := strings.Join([]string{
			extractShellFunction(t, script, "normalize_install_dir"),
			configured,
			extractShellFunction(t, script, "resolve_controller_install_dir"),
			"INSTALL_DIR_INPUT=/srv/oboard",
			"INSTALL_DIR=",
			"INSTALLATION_EXISTS=1",
			"ACTION=update",
			"resolve_controller_install_dir",
		}, "\n")
		output, err := exec.Command(shell, "-c", harness).CombinedOutput()
		if err == nil || !strings.Contains(string(output), "更新或卸载时不能改为") {
			t.Fatalf("install directory change was not rejected: %v\n%s", err, output)
		}
	})

	t.Run("render service paths", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source.service")
		destination := filepath.Join(root, "rendered.service")
		fixture := "ExecStart=/usr/local/bin/oboard-controller\nReadWritePaths=/var/lib/oboard /usr/local/bin\n"
		if err := os.WriteFile(source, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
		harness := strings.Join([]string{
			extractShellFunction(t, script, "render_service_file"),
			"INSTALL_DIR=/data/oboard",
			"render_service_file " + shellQuote(source) + " " + shellQuote(destination),
		}, "\n")
		if output, err := exec.Command(shell, "-c", harness).CombinedOutput(); err != nil {
			t.Fatalf("service rendering failed: %v\n%s", err, output)
		}
		rendered, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rendered), "/usr/local/bin") || !strings.Contains(string(rendered), "ExecStart=/data/oboard/oboard-controller") || !strings.Contains(string(rendered), "ReadWritePaths=/var/lib/oboard /data/oboard") {
			t.Fatalf("unexpected rendered service:\n%s", rendered)
		}
		assertPathMode(t, destination, 0o644)
	})
}

func TestControllerUpdaterUnitBinaryWritePaths(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "systemd", "oboard-controller-updater.service"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "EnvironmentFile=-/etc/oboard/controller.env") {
		t.Fatal("updater unit does not load the persisted install directory")
	}
	want := "ReadWritePaths=/run/oboard /var/lib/oboard /opt/oboard /usr/local/bin"
	if !strings.Contains(text, want) {
		t.Fatalf("updater unit missing binary installation write paths %q", want)
	}
	for _, removed := range []string{"docker", "-/var/lib/oboard", "-/opt/oboard", "/etc/systemd/system"} {
		if strings.Contains(strings.ToLower(text), removed) {
			t.Fatalf("updater unit still contains removed path or dependency %q", removed)
		}
	}
}

func TestControllerUpdaterRuntimePreparationPreservesDataRoot(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	if _, err := exec.LookPath("install"); err != nil {
		t.Skip("install is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	for _, script := range []string{"scripts/install.sh"} {
		t.Run(script, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(script)))
			if err != nil {
				t.Fatal(err)
			}
			original := extractShellFunction(t, string(content), "prepare_controller_updater_runtime")
			rewrite := func(dataRoot, runtimeRoot string) string {
				function := strings.ReplaceAll(original, "/var/lib/oboard", shellQuote(dataRoot))
				function = strings.ReplaceAll(function, "/run/oboard", shellQuote(runtimeRoot))
				function = strings.ReplaceAll(function, "-o root -g oboard ", "")
				return strings.ReplaceAll(function, "-o root -g root ", "")
			}
			run := func(t *testing.T, function string) error {
				t.Helper()
				cmd := exec.Command(bash, "-c", function+"\nprepare_controller_updater_runtime")
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Logf("runtime preparation output:\n%s", output)
				}
				return err
			}

			t.Run("existing data root", func(t *testing.T) {
				temp := t.TempDir()
				dataRoot := filepath.Join(temp, "data")
				runtimeRoot := filepath.Join(temp, "run")
				if err := os.Mkdir(dataRoot, 0o711); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dataRoot, 0o711); err != nil {
					t.Fatal(err)
				}
				if err := run(t, rewrite(dataRoot, runtimeRoot)); err != nil {
					t.Fatal(err)
				}
				assertPathMode(t, dataRoot, 0o711)
				assertPathMode(t, runtimeRoot, 0o750)
				assertPathMode(t, filepath.Join(dataRoot, "controller-update"), 0o700)
			})

			t.Run("missing data root", func(t *testing.T) {
				temp := t.TempDir()
				dataRoot := filepath.Join(temp, "data")
				runtimeRoot := filepath.Join(temp, "run")
				if err := run(t, rewrite(dataRoot, runtimeRoot)); err != nil {
					t.Fatal(err)
				}
				assertPathMode(t, dataRoot, 0o750)
				assertPathMode(t, filepath.Join(dataRoot, "controller-update"), 0o700)
			})

			t.Run("symlink data root", func(t *testing.T) {
				temp := t.TempDir()
				target := filepath.Join(temp, "target")
				dataRoot := filepath.Join(temp, "data")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dataRoot); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				if err := run(t, rewrite(dataRoot, filepath.Join(temp, "run"))); err == nil {
					t.Fatal("runtime preparation accepted a symlink data root")
				}
			})
		})
	}
}

func extractShellFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, name+"() {")
	if start < 0 {
		t.Fatalf("script is missing %s", name)
	}
	rest := script[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("script has an unterminated %s", name)
	}
	return rest[:end+2]
}

func extractShellAssignment(t *testing.T, script, name string) string {
	t.Helper()
	prefix := name + "="
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("script is missing %s assignment", name)
	return ""
}

func assertACMEInstallerBehavior(t *testing.T, script string) {
	t.Helper()
	found := false
	for _, shellName := range []string{"bash", "dash"} {
		shell, err := exec.LookPath(shellName)
		if err != nil {
			continue
		}
		found = true
		t.Run(shellName, func(t *testing.T) {
			assertACMEInstallerBehaviorWithShell(t, script, shell)
		})
	}
	if !found {
		t.Skip("bash and dash are unavailable")
	}
}

func assertACMEInstallerBehaviorWithShell(t *testing.T, script, shell string) {
	t.Helper()
	hashToolName := ""
	hashToolPath := ""
	for _, candidate := range []string{"sha256sum", "shasum"} {
		if path, lookupErr := exec.LookPath(candidate); lookupErr == nil {
			hashToolName = candidate
			hashToolPath = path
			break
		}
	}
	if hashToolPath == "" {
		t.Skip("sha256sum and shasum are unavailable")
	}

	fragments := []string{
		extractShellAssignment(t, script, "ACME_SH_VERSION"),
		extractShellAssignment(t, script, "ACME_SH_SHA256"),
		extractShellAssignment(t, script, "ACME_SH_URL"),
		extractShellAssignment(t, script, "ACME_SH_INSTALL_PATH"),
		extractShellFunction(t, script, "sha256_file"),
		extractShellFunction(t, script, "install_pinned_acme_sh"),
		extractShellFunction(t, script, "ensure_acme_sh"),
	}
	functionSource := strings.Join(fragments, "\n\n")
	fixture := []byte("#!/usr/bin/env sh\nprintf 'acme fixture\\n'\n")
	fixtureSum := sha256.Sum256(fixture)
	fixtureHash := hex.EncodeToString(fixtureSum[:])

	type runResult struct {
		root       string
		target     string
		packageLog string
		output     string
		err        error
	}
	run := func(t *testing.T, expectedHash string, existing, packageAvailable bool, failureMode string) runResult {
		t.Helper()
		root := t.TempDir()
		fakeBin := filepath.Join(root, "bin")
		if err := os.MkdirAll(fakeBin, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"awk", "chmod", "cp", "mkdir", "mktemp", "mv", "rm"} {
			path, lookupErr := exec.LookPath(command)
			if lookupErr != nil {
				t.Skipf("%s is unavailable", command)
			}
			if err := os.Symlink(path, filepath.Join(fakeBin, command)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(hashToolPath, filepath.Join(fakeBin, hashToolName)); err != nil {
			t.Fatal(err)
		}
		fixturePath := filepath.Join(root, "fixture-acme.sh")
		if err := os.WriteFile(fixturePath, fixture, 0o644); err != nil {
			t.Fatal(err)
		}
		if existing {
			if err := os.WriteFile(filepath.Join(fakeBin, "acme.sh"), []byte("existing\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		target := filepath.Join(root, "install", "acme.sh")
		packageLog := filepath.Join(root, "packages.log")
		curlLog := filepath.Join(root, "curl.log")
		realMV, err := exec.LookPath("mv")
		if err != nil {
			t.Fatal(err)
		}
		harness := functionSource + "\n" + strings.Join([]string{
			"ACME_SH_SHA256=" + shellQuote(expectedHash),
			"ACME_SH_INSTALL_PATH=" + shellQuote(target),
			"ACME_FIXTURE=" + shellQuote(fixturePath),
			"PACKAGE_LOG=" + shellQuote(packageLog),
			"CURL_LOG=" + shellQuote(curlLog),
			"FAKE_BIN=" + shellQuote(fakeBin),
			"REAL_MV=" + shellQuote(realMV),
			"ACME_PACKAGE_AVAILABLE=" + shellQuote(strconv.FormatBool(packageAvailable)),
			"ACME_FAILURE_MODE=" + shellQuote(failureMode),
			"OBOARD_TMPDIR=" + shellQuote(root),
			"PATH=\"$FAKE_BIN\"",
			"export PATH ACME_FIXTURE PACKAGE_LOG CURL_LOG FAKE_BIN REAL_MV ACME_PACKAGE_AVAILABLE ACME_FAILURE_MODE OBOARD_TMPDIR",
			`pkg_install() {
  printf '%s\n' "$*" >> "$PACKAGE_LOG"
  if [ "$1" = acme.sh ]; then
    if [ "$ACME_PACKAGE_AVAILABLE" = true ]; then
      printf '#!/bin/sh\nexit 0\n' > "$FAKE_BIN/acme.sh"
      chmod 0755 "$FAKE_BIN/acme.sh"
      return 0
    fi
    return 1
  fi
  for package in "$@"; do
    printf '#!/bin/sh\nexit 0\n' > "$FAKE_BIN/$package"
    chmod 0755 "$FAKE_BIN/$package"
  done
}`,
			`curl() {
  printf 'called\n' >> "$CURL_LOG"
  if [ "$ACME_FAILURE_MODE" = download ]; then
    return 1
  fi
  output=
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then
      shift
      output=$1
    fi
    shift
  done
  [ -n "$output" ] || return 1
  cp "$ACME_FIXTURE" "$output"
}`,
			`mv() {
  if [ "$ACME_FAILURE_MODE" = install ]; then
    return 1
  fi
  "$REAL_MV" "$@"
}`,
			"ensure_acme_sh",
		}, "\n")
		cmd := exec.Command(shell, "-c", harness)
		output, runErr := cmd.CombinedOutput()
		log, readErr := os.ReadFile(packageLog)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		return runResult{root: root, target: target, packageLog: string(log), output: string(output), err: runErr}
	}

	t.Run("verified fallback", func(t *testing.T) {
		result := run(t, fixtureHash, false, false, "")
		if result.err != nil {
			t.Fatalf("fallback failed: %v\n%s", result.err, result.output)
		}
		installed, err := os.ReadFile(result.target)
		if err != nil {
			t.Fatal(err)
		}
		if string(installed) != string(fixture) {
			t.Fatal("installed acme.sh does not match the verified download")
		}
		info, err := os.Stat(result.target)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o755 {
			t.Fatalf("installed acme.sh mode = %04o, want 0755", mode)
		}
		if result.packageLog != "openssl socat\nacme.sh\n" {
			t.Fatalf("package attempts = %q", result.packageLog)
		}
		if leftovers, err := filepath.Glob(filepath.Join(result.root, "oboard-acme.*")); err != nil || len(leftovers) != 0 {
			t.Fatalf("download temporary files remain: %v, err=%v", leftovers, err)
		}
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		result := run(t, strings.Repeat("0", 64), false, false, "")
		if result.err == nil {
			t.Fatal("checksum mismatch was accepted")
		}
		if !strings.Contains(result.output, "acme.sh 校验失败") {
			t.Fatalf("missing checksum failure message: %s", result.output)
		}
		if _, err := os.Stat(result.target); !os.IsNotExist(err) {
			t.Fatalf("checksum mismatch left an installed target: %v", err)
		}
		if leftovers, err := filepath.Glob(filepath.Join(result.root, "oboard-acme.*")); err != nil || len(leftovers) != 0 {
			t.Fatalf("download temporary files remain: %v, err=%v", leftovers, err)
		}
	})

	t.Run("existing command", func(t *testing.T) {
		result := run(t, fixtureHash, true, false, "")
		if result.err != nil {
			t.Fatalf("existing acme.sh was rejected: %v\n%s", result.err, result.output)
		}
		if strings.Contains(result.packageLog, "acme.sh") {
			t.Fatalf("existing acme.sh triggered a package attempt: %q", result.packageLog)
		}
		if _, err := os.Stat(result.target); !os.IsNotExist(err) {
			t.Fatalf("existing acme.sh was replaced by fallback target: %v", err)
		}
	})

	t.Run("distribution package", func(t *testing.T) {
		result := run(t, fixtureHash, false, true, "")
		if result.err != nil {
			t.Fatalf("distribution package was rejected: %v\n%s", result.err, result.output)
		}
		if result.packageLog != "openssl socat\nacme.sh\n" {
			t.Fatalf("package attempts = %q", result.packageLog)
		}
		if _, err := os.Stat(filepath.Join(result.root, "curl.log")); !os.IsNotExist(err) {
			t.Fatalf("distribution package triggered fallback download: %v", err)
		}
		if _, err := os.Stat(result.target); !os.IsNotExist(err) {
			t.Fatalf("distribution package triggered fallback install: %v", err)
		}
	})

	for _, failure := range []struct {
		name    string
		mode    string
		message string
	}{
		{name: "download failure", mode: "download", message: "无法下载固定版本的 acme.sh"},
		{name: "install failure", mode: "install", message: "无法安装 acme.sh"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			result := run(t, fixtureHash, false, false, failure.mode)
			if result.err == nil {
				t.Fatalf("%s was accepted", failure.name)
			}
			if !strings.Contains(result.output, failure.message) {
				t.Fatalf("missing failure message %q: %s", failure.message, result.output)
			}
			if _, err := os.Stat(result.target); !os.IsNotExist(err) {
				t.Fatalf("%s left an installed target: %v", failure.name, err)
			}
			if leftovers, err := filepath.Glob(filepath.Join(result.root, "oboard-acme.*")); err != nil || len(leftovers) != 0 {
				t.Fatalf("download temporary files remain: %v, err=%v", leftovers, err)
			}
			if leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(result.target), ".acme.sh.*")); err != nil || len(leftovers) != 0 {
				t.Fatalf("install staging files remain: %v, err=%v", leftovers, err)
			}
		})
	}
}

func assertPackageManagerDispatch(t *testing.T, script string) {
	t.Helper()
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
	}
	if err != nil {
		t.Skip("a POSIX shell is unavailable")
	}
	pkgInstall := extractShellFunction(t, script, "pkg_install")
	for _, test := range []struct {
		manager string
		want    string
	}{
		{manager: "apk", want: "apk add --no-cache curl ca-certificates\n"},
		{manager: "apt-get", want: "apt-get update -y\napt-get install -y --no-install-recommends curl ca-certificates\n"},
		{manager: "dnf", want: "dnf install -y curl ca-certificates\n"},
		{manager: "yum", want: "yum install -y curl ca-certificates\n"},
		{manager: "microdnf", want: "microdnf install -y curl ca-certificates\n"},
		{manager: "zypper", want: "zypper --non-interactive install -y curl ca-certificates\n"},
		{manager: "pacman", want: "pacman -Sy --noconfirm curl ca-certificates\n"},
	} {
		t.Run(test.manager, func(t *testing.T) {
			root := t.TempDir()
			fakeBin := filepath.Join(root, "bin")
			if err := os.MkdirAll(fakeBin, 0o755); err != nil {
				t.Fatal(err)
			}
			managerPath := filepath.Join(fakeBin, test.manager)
			stub := "#!/bin/sh\nprintf '" + test.manager + " %s\\n' \"$*\" >> \"$PACKAGE_LOG\"\n"
			if err := os.WriteFile(managerPath, []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			packageLog := filepath.Join(root, "packages.log")
			harness := strings.Join([]string{
				"set -eu",
				"PATH=" + shellQuote(fakeBin),
				"PACKAGE_LOG=" + shellQuote(packageLog),
				"export PATH PACKAGE_LOG",
				pkgInstall,
				"pkg_install curl ca-certificates",
			}, "\n")
			cmd := exec.Command(shell, "-c", harness)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("pkg_install failed: %v\n%s", err, output)
			}
			log, err := os.ReadFile(packageLog)
			if err != nil {
				t.Fatal(err)
			}
			if string(log) != test.want {
				t.Fatalf("pkg_install log = %q, want %q", log, test.want)
			}
		})
	}
}

func assertInstallDirectoryInputs(t *testing.T, script string) {
	t.Helper()
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
	}
	if err != nil {
		t.Skip("a POSIX shell is unavailable")
	}
	source := strings.Join([]string{
		extractShellFunction(t, script, "normalize_install_dir"),
		extractShellFunction(t, script, "install_dir_from_input"),
	}, "\n")
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "default", input: "", want: "/opt/oboard"},
		{name: "local bin", input: "/usr/local/bin", want: "/usr/local/bin"},
		{name: "opt", input: "/opt/oboard", want: "/opt/oboard"},
		{name: "local sbin", input: "/usr/local/sbin", want: "/usr/local/sbin"},
		{name: "custom", input: "/data/oboard", want: "/data/oboard"},
		{name: "trim trailing slash", input: "/data/oboard/", want: "/data/oboard"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.Command(shell, "-c", source+"\ninstall_dir_from_input "+shellQuote(test.input)).CombinedOutput()
			if err != nil || strings.TrimSpace(string(output)) != test.want {
				t.Fatalf("input %q = %q, err=%v; want %q", test.input, output, err, test.want)
			}
		})
	}
	for _, input := range []string{"data/oboard", "/", "/data//oboard", "/data/../etc", "/data/oboard path", "/data/oboard;rm"} {
		output, err := exec.Command(shell, "-c", source+"\ninstall_dir_from_input "+shellQuote(input)).CombinedOutput()
		if err == nil {
			t.Fatalf("invalid install directory %q was accepted: %s", input, output)
		}
	}
	for _, old := range []string{"install_dir_for_choice", "OBOARD_INSTALL_CHOICE", "请选择 [1]："} {
		if strings.Contains(script, old) {
			t.Fatalf("installer still contains obsolete directory selection %q", old)
		}
	}
}

func assertInstallToolBootstrap(t *testing.T, script string) {
	t.Helper()
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
	}
	if err != nil {
		t.Skip("a POSIX shell is unavailable")
	}
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"curl", "tar"} {
		path, lookupErr := exec.LookPath(command)
		if lookupErr != nil {
			t.Skipf("%s is unavailable", command)
		}
		if err := os.Symlink(path, filepath.Join(fakeBin, command)); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"sha256sum", "shasum"} {
		if path, lookupErr := exec.LookPath(command); lookupErr == nil {
			if err := os.Symlink(path, filepath.Join(fakeBin, command)); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	packageLog := filepath.Join(root, "packages.log")
	harness := strings.Join([]string{
		"set -eu",
		"PATH=" + shellQuote(fakeBin),
		"PACKAGE_LOG=" + shellQuote(packageLog),
		"export PATH PACKAGE_LOG",
		extractShellFunction(t, script, "ensure_base_tools"),
		`pkg_install() {
  printf '%s\n' "$*" > "$PACKAGE_LOG"
}`,
		"ensure_base_tools",
	}, "\n")
	cmd := exec.Command(shell, "-c", harness)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("base tool bootstrap failed: %v\n%s", err, output)
	}
	log, err := os.ReadFile(packageLog)
	if err != nil {
		t.Fatal(err)
	}
	if !containsShellWord(string(log), "coreutils") {
		t.Fatalf("missing install command did not request coreutils: %q", log)
	}
}

func containsShellWord(value, want string) bool {
	for _, word := range strings.Fields(value) {
		if word == want {
			return true
		}
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func assertPathMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("%s mode = %04o, want %04o", path, actual, expected)
	}
}
