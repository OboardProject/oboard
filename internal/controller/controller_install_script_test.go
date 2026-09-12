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

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/scripting"
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
		`echo "OBoard 主控$result_title"`,
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
		"OBOARD_INSTALL_DIR",
		"OBOARD_GEOIP_DIR",
		"CONTROLLER_CONFIG_DIR",
		"CONTROLLER_DATA_DIR",
		"configure_controller_paths",
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
		"oboard-ai-worker",
		"oboard-script-worker",
		"enable-scripts",
		"OBOARD_INSTALL_SCRIPTS",
		"want_script_runtime",
		"install_script_runtime",
		"script_runtime_installed",
		"script-runtime.wanted",
		"未安装脚本运行环境（默认关闭）。",
		"正在安装脚本运行环境",
		"请回到面板启用脚本执行",
		"prepare_script_worker_user",
		"ensure_script_isolation_deps",
		"script_uidmap_package",
		"pkg_install bubblewrap",
		"pkg_install iproute2",
		"uidmap",
		"shadow-utils",
		"/sys/fs/cgroup/cgroup.controllers",
		"oboard-scripts",
		"prepare_controller_updater_runtime",
		"wait_for_controller_updater",
		"curl --unix-socket /run/oboard/controller-updater.sock",
		`"$CONTROLLER_DATA_DIR/controller-update"`,
		"uninstall_controller",
		"OBoard 主控已卸载",
		"配置和数据已保留",
		"OBOARD_PURGE_DATA",
		"resolve_purge_data",
		"是否同时删除主控的配置和数据",
		"清除请输入 y，保留请直接回车 [y/N]",
		"当前无法交互确认，已保留",
		"当前暂无可用的稳定版，将安装最新开发版",
		"安装包下载失败",
		"[1/4] 检查运行环境",
		"[2/4] 下载主控安装包",
		"[3/4] 校验安装包",
		"[4/4] 配置并启动主控服务",
		"详细日志：$INSTALL_LOG",
		"format_download_value",
		"download_component",
		"download_quiet",
		"--progress-bar",
		"--continue-at -",
		"完成：",
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
	if strings.Contains(text, "download_file()") {
		t.Fatal("controller installer still uses silent download_file")
	}
	for _, obsolete := range []string{"/etc/oboard/controller.env", "/var/lib/oboard/oboard.sqlite", "/var/lib/oboard/controller-update", "/opt/oboard/web /opt/oboard/downloads"} {
		if strings.Contains(text, obsolete) {
			t.Fatalf("controller installer still contains obsolete split path %q", obsolete)
		}
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

func TestControllerDownloadProgressOutput(t *testing.T) {
	script := controllerInstallScript(t)
	if !strings.Contains(script, "--progress-bar") {
		t.Fatal("interactive download progress bar is missing")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	curlLog := filepath.Join(root, "curl.log")
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
printf '%s\n' "$*" >> "$CURL_LOG"
case "$*" in
  *'--range 0-0'*)
    printf '200 https://mirror.example/package'
    exit 0
    ;;
esac
destination=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    shift
    destination=$1
  fi
  shift
done
printf payload > "$destination"
printf '1048576 524288'
`)
	harness := strings.Join([]string{
		"set -eu",
		extractShellFunction(t, script, "format_download_value"),
		extractShellFunction(t, script, "resolve_download_url"),
		extractShellFunction(t, script, "download_component"),
		"download_component 主控安装包 https://github.com/OboardProject/oboard/releases/download/dev/package " + shellQuote(filepath.Join(root, "package")),
	}, "\n")
	cmd := exec.Command(testPOSIXShell(t), "-c", harness)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CURL_LOG="+curlLog)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("download helper failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "完成：1.0 MB · 512.0 KB/s") {
		t.Fatalf("download summary missing size or speed:\n%s", output)
	}
	log, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--silent", "--write-out", "%{size_download} %{speed_download}", "--continue-at -"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("curl invocation missing %q: %s", want, log)
		}
	}
	// The redirect chain is resolved first so the metered transfer draws a
	// single progress bar instead of one bar per hop.
	if !strings.Contains(string(log), "https://mirror.example/package") {
		t.Errorf("metered download did not use the resolved URL: %s", log)
	}
	if count := strings.Count(string(log), "--continue-at -"); count != 1 {
		t.Errorf("metered transfer count = %d, want 1: %s", count, log)
	}
}

func TestControllerDownloadResumesInterruptedTransferAndStopsAfterThreeAttempts(t *testing.T) {
	script := controllerInstallScript(t)
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	attempts := filepath.Join(root, "attempts")
	curlLog := filepath.Join(root, "curl.log")
	writeExecutable(t, filepath.Join(bin, "sleep"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
case "$*" in
  *'--range 0-0'*)
    printf '200 https://github.com/OboardProject/oboard/releases/download/dev/package'
    exit 0
    ;;
esac
count=0
[ ! -f "$CURL_ATTEMPTS" ] || count=$(cat "$CURL_ATTEMPTS")
count=$((count + 1))
printf '%s\n' "$count" > "$CURL_ATTEMPTS"
printf '%s\n' "$*" >> "$CURL_LOG"
destination=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then shift; destination=$1; fi
  shift
done
if [ "$count" -eq 1 ]; then
  printf partial > "$destination"
  exit 18
fi
printf '%s' '-rest' >> "$destination"
printf '5 1024'
`)
	destination := filepath.Join(root, "package")
	harness := strings.Join([]string{
		"set -eu",
		extractShellFunction(t, script, "format_download_value"),
		extractShellFunction(t, script, "resolve_download_url"),
		extractShellFunction(t, script, "download_component"),
		"download_component 主控安装包 https://github.com/OboardProject/oboard/releases/download/dev/package " + shellQuote(destination),
	}, "\n")
	cmd := exec.Command(testPOSIXShell(t), "-c", harness)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CURL_ATTEMPTS="+attempts, "CURL_LOG="+curlLog)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resumed download failed: %v\n%s", err, output)
	}
	if raw, err := os.ReadFile(attempts); err != nil || strings.TrimSpace(string(raw)) != "2" {
		t.Fatalf("attempt count = %q, err=%v", raw, err)
	}
	if log, err := os.ReadFile(curlLog); err != nil || strings.Count(string(log), "--continue-at -") != 2 {
		t.Fatalf("curl did not resume both attempts: %q, err=%v", log, err)
	}
	if raw, err := os.ReadFile(destination); err != nil || string(raw) != "partial-rest" {
		t.Fatalf("resumed content = %q, err=%v", raw, err)
	}
	for _, path := range []string{attempts, curlLog, destination} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	quietHarness := strings.Join([]string{
		"set -eu",
		extractShellFunction(t, script, "download_quiet"),
		"download_quiet https://github.com/OboardProject/oboard/releases/download/dev/sha256sums.txt " + shellQuote(destination),
	}, "\n")
	cmd = exec.Command(testPOSIXShell(t), "-c", quietHarness)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CURL_ATTEMPTS="+attempts, "CURL_LOG="+curlLog)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("quiet resumed download failed: %v\n%s", err, output)
	}
	if raw, err := os.ReadFile(attempts); err != nil || strings.TrimSpace(string(raw)) != "2" {
		t.Fatalf("quiet attempt count = %q, err=%v", raw, err)
	}
	if raw, err := os.ReadFile(destination); err != nil || string(raw) != "partial-rest" {
		t.Fatalf("quiet resumed content = %q, err=%v", raw, err)
	}

	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
case "$*" in
  *'--range 0-0'*) exit 18 ;;
esac
count=0
[ ! -f "$CURL_ATTEMPTS" ] || count=$(cat "$CURL_ATTEMPTS")
count=$((count + 1))
printf '%s\n' "$count" > "$CURL_ATTEMPTS"
exit 18
`)
	for _, path := range []string{attempts, destination} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	cmd = exec.Command(testPOSIXShell(t), "-c", harness)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CURL_ATTEMPTS="+attempts, "CURL_LOG="+curlLog)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("three interrupted attempts unexpectedly succeeded:\n%s", output)
	}
	if raw, err := os.ReadFile(attempts); err != nil || strings.TrimSpace(string(raw)) != "3" {
		t.Fatalf("exhausted attempt count = %q, err=%v", raw, err)
	}
}

func TestControllerInstallScriptInstallsScriptIsolationDeps(t *testing.T) {
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
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
	}
	if err != nil {
		t.Skip("a POSIX shell is unavailable")
	}
	functions := strings.Join([]string{
		extractShellFunction(t, script, "script_isolation_unavailable"),
		extractShellFunction(t, script, "script_uidmap_package"),
		extractShellFunction(t, script, "ensure_script_isolation_deps"),
	}, "\n")

	t.Run("installs bubblewrap and uidmap when missing", func(t *testing.T) {
		root := t.TempDir()
		fakeBin := filepath.Join(root, "bin")
		if err := os.MkdirAll(fakeBin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fakeBin, "apt-get"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		packageLog := filepath.Join(root, "packages.log")
		installLog := filepath.Join(root, "install.log")
		harness := strings.Join([]string{
			"set -eu",
			"FAKE_BIN=" + shellQuote(fakeBin),
			"PATH=" + shellQuote(fakeBin) + ":$PATH",
			"PACKAGE_LOG=" + shellQuote(packageLog),
			"INSTALL_LOG=" + shellQuote(installLog),
			"export FAKE_BIN PATH PACKAGE_LOG INSTALL_LOG",
			functions,
			`pkg_install() {
  printf '%s\n' "$*" >> "$PACKAGE_LOG"
  for pkg in "$@"; do
    case "$pkg" in
      bubblewrap)
        printf '#!/bin/sh\n' > "$FAKE_BIN/bwrap"
        chmod 0755 "$FAKE_BIN/bwrap"
        ;;
      uidmap)
        printf '#!/bin/sh\n' > "$FAKE_BIN/newuidmap"
        chmod 0755 "$FAKE_BIN/newuidmap"
        ;;
    esac
  done
}`,
			": > \"$PACKAGE_LOG\"",
			"ensure_script_isolation_deps",
		}, "\n")
		cmd := exec.Command(shell, "-c", harness)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script isolation bootstrap failed: %v\n%s", err, output)
		}
		log, err := os.ReadFile(packageLog)
		if err != nil {
			t.Fatal(err)
		}
		got := string(log)
		if !containsShellWord(got, "bubblewrap") || !containsShellWord(got, "uidmap") {
			t.Fatalf("missing script isolation packages: %q", got)
		}
	})

	t.Run("skips packages when isolation tools exist", func(t *testing.T) {
		root := t.TempDir()
		fakeBin := filepath.Join(root, "bin")
		if err := os.MkdirAll(fakeBin, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"bwrap", "newuidmap", "apt-get"} {
			if err := os.WriteFile(filepath.Join(fakeBin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		packageLog := filepath.Join(root, "packages.log")
		installLog := filepath.Join(root, "install.log")
		harness := strings.Join([]string{
			"set -eu",
			"PATH=" + shellQuote(fakeBin) + ":$PATH",
			"PACKAGE_LOG=" + shellQuote(packageLog),
			"INSTALL_LOG=" + shellQuote(installLog),
			"export PATH PACKAGE_LOG INSTALL_LOG",
			functions,
			`pkg_install() { printf '%s\n' "$*" >> "$PACKAGE_LOG"; }`,
			": > \"$PACKAGE_LOG\"",
			"ensure_script_isolation_deps",
			"test ! -s \"$PACKAGE_LOG\"",
		}, "\n")
		cmd := exec.Command(shell, "-c", harness)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("existing isolation tools still requested packages: %v\n%s", err, output)
		}
	})
}

func TestControllerInstallScriptRuntimeIsOptional(t *testing.T) {
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
	if strings.Count(script, `install_file_atomic "$work/bin/oboard-script-worker"`) != 1 {
		t.Fatal("script-worker binary must be installed only from install_script_runtime")
	}
	if !strings.Contains(script, "if want_script_runtime; then") {
		t.Fatal("controller install must gate script runtime on want_script_runtime")
	}
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
	}
	if err != nil {
		t.Skip("a POSIX shell is unavailable")
	}
	root := t.TempDir()
	unit := filepath.Join(root, "oboard-script-worker.service")
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatal(err)
	}
	functions := strings.Join([]string{
		extractShellFunction(t, script, "script_runtime_opted_in"),
		extractShellFunction(t, script, "want_script_runtime"),
	}, "\n")
	run := func(t *testing.T, env string) string {
		t.Helper()
		harness := strings.Join([]string{
			"set -eu",
			functions,
			"CONTROLLER_CONFIG_DIR=" + shellQuote(configDir),
			env,
			`if want_script_runtime; then printf yes; else printf no; fi`,
		}, "\n")
		output, err := exec.Command(shell, "-c", harness).CombinedOutput()
		if err != nil {
			t.Fatalf("want_script_runtime failed: %v\n%s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if got := run(t, "ACTION=install\nOBOARD_INSTALL_SCRIPTS="); got != "no" {
		t.Fatalf("default install selected script runtime: %s", got)
	}
	if got := run(t, "ACTION=update\nOBOARD_INSTALL_SCRIPTS="); got != "no" {
		t.Fatalf("default update selected script runtime: %s", got)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "ACTION=install\nOBOARD_INSTALL_SCRIPTS="); got != "no" {
		t.Fatalf("leftover unit selected script runtime on install: %s", got)
	}
	if got := run(t, "ACTION=update\nOBOARD_INSTALL_SCRIPTS="); got != "no" {
		t.Fatalf("leftover unit selected script runtime on update: %s", got)
	}
	if got := run(t, "ACTION=install\nOBOARD_INSTALL_SCRIPTS=1"); got != "yes" {
		t.Fatalf("OBOARD_INSTALL_SCRIPTS=1 did not select script runtime: %s", got)
	}
	if got := run(t, "ACTION=enable-scripts\nOBOARD_INSTALL_SCRIPTS="); got != "yes" {
		t.Fatalf("enable-scripts did not select script runtime: %s", got)
	}
	if err := os.WriteFile(filepath.Join(configDir, "script-runtime.wanted"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "ACTION=update\nOBOARD_INSTALL_SCRIPTS="); got != "yes" {
		t.Fatalf("wanted marker did not keep script runtime: %s", got)
	}
	if got := run(t, "ACTION=update\nOBOARD_INSTALL_SCRIPTS=0"); got != "no" {
		t.Fatalf("OBOARD_INSTALL_SCRIPTS=0 did not skip script runtime: %s", got)
	}
}

func TestScriptRuntimeInstallCommandUsesUpdateChannel(t *testing.T) {
	t.Setenv("OBOARD_UPDATE_CHANNEL", "dev")
	command := (&Server{}).scriptRuntimeInstallCommand()
	if !strings.Contains(command, "OBOARD_ACTION=enable-scripts") || !strings.Contains(command, "VERSION=dev") {
		t.Fatalf("unexpected install command: %s", command)
	}
}

func TestEnsureScriptRuntimeForEnableRejectsMissingRuntime(t *testing.T) {
	s := &Server{}
	if s.scriptRuntimeInstalled() {
		t.Skip("host already has a script runtime unit or connected worker")
	}
	if err := s.ensureScriptRuntimeForEnable(false); err != nil {
		t.Fatal(err)
	}
	err := s.ensureScriptRuntimeForEnable(true)
	if err == nil || scripting.CodeOf(err) != model.ScriptErrorRuntimeUnavailable {
		t.Fatalf("got %v", err)
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
	configuredFor := func(t *testing.T, root string) string {
		t.Helper()
		servicePath := filepath.Join(t.TempDir(), "oboard-controller.service")
		if err := os.WriteFile(servicePath, []byte("ExecStart="+root+"/oboard-controller\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		configured := strings.ReplaceAll(extractShellFunction(t, script, "configured_controller_install_dir"), "/etc/systemd/system/oboard-controller.service", shellQuote(servicePath))
		return strings.ReplaceAll(configured, "/etc/init.d/oboard-controller", shellQuote(filepath.Join(t.TempDir(), "missing-openrc")))
	}

	t.Run("restore directory from service", func(t *testing.T) {
		harness := strings.Join([]string{
			extractShellFunction(t, script, "normalize_install_dir"),
			extractShellFunction(t, script, "install_dir_from_input"),
			configuredFor(t, "/data/oboard"),
			extractShellFunction(t, script, "choose_install_dir"),
			extractShellFunction(t, script, "resolve_controller_install_dir"),
			"INSTALL_DIR_INPUT=",
			"INSTALL_DIR=",
			"CONTROLLER_DATA_EXISTED=0",
			"resolve_controller_install_dir",
			"printf 'resolved=%s env=%s data=%s web=%s downloads=%s acme=%s\\n' \"$INSTALL_DIR\" \"$CONTROLLER_ENV\" \"$CONTROLLER_DATA_DIR\" \"$CONTROLLER_WEB_DIR\" \"$CONTROLLER_DOWNLOADS_DIR\" \"$ACME_SH_INSTALL_PATH\"",
		}, "\n")
		output, err := exec.Command(shell, "-c", harness).CombinedOutput()
		want := "resolved=/data/oboard env=/data/oboard/config/controller.env data=/data/oboard/data web=/data/oboard/web downloads=/data/oboard/downloads acme=/data/oboard/tools/acme.sh"
		if err != nil || !strings.Contains(string(output), want) {
			t.Fatalf("service install directory was not restored: %v\n%s", err, output)
		}
	})

	t.Run("reject directory change during update", func(t *testing.T) {
		harness := strings.Join([]string{
			extractShellFunction(t, script, "normalize_install_dir"),
			configuredFor(t, "/data/oboard"),
			extractShellFunction(t, script, "resolve_controller_install_dir"),
			"INSTALL_DIR_INPUT=/srv/oboard",
			"INSTALL_DIR=",
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
		fixture := "ExecStart=/opt/oboard/oboard-controller\nEnvironmentFile=-/opt/oboard/config/controller.env\nReadWritePaths=/run/oboard /opt/oboard\n"
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
		if strings.Contains(string(rendered), "/opt/oboard") || !strings.Contains(string(rendered), "ExecStart=/data/oboard/oboard-controller") || !strings.Contains(string(rendered), "EnvironmentFile=-/data/oboard/config/controller.env") || !strings.Contains(string(rendered), "ReadWritePaths=/run/oboard /data/oboard") {
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
	if !strings.Contains(text, "EnvironmentFile=-/opt/oboard/config/controller.env") {
		t.Fatal("updater unit does not load the persisted install directory")
	}
	want := "ReadWritePaths=/run/oboard /opt/oboard"
	if !strings.Contains(text, want) {
		t.Fatalf("updater unit missing binary installation write paths %q", want)
	}
	for _, removed := range []string{"docker", "/var/lib/oboard", "/etc/oboard", "/usr/local/bin", "/etc/systemd/system"} {
		if strings.Contains(strings.ToLower(text), removed) {
			t.Fatalf("updater unit still contains removed path or dependency %q", removed)
		}
	}
}

func TestControllerDeploymentFilesUseSingleInstallRoot(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	files := []string{
		"deploy/systemd/oboard-controller.service",
		"deploy/systemd/oboard-controller-updater.service",
		"deploy/systemd/oboard-ai-worker.service",
		"deploy/systemd/oboard-script-worker.service",
		"deploy/openrc/oboard-controller",
		"deploy/openrc/oboard-controller-updater",
		"deploy/openrc/oboard-ai-worker",
		"deploy/openrc/oboard-script-worker",
		"deploy/controller.env.example",
	}
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, obsolete := range []string{"/etc/oboard", "/var/lib/oboard", "/usr/local/bin/oboard-controller", "/var/log/oboard-controller"} {
			if strings.Contains(text, obsolete) {
				t.Errorf("%s contains obsolete split path %q", name, obsolete)
			}
		}
		if !strings.Contains(text, "/opt/oboard") {
			t.Errorf("%s does not use the default installation root", name)
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
			rewrite := func(runtimeRoot string) string {
				function := strings.ReplaceAll(original, "/run/oboard", shellQuote(runtimeRoot))
				function = strings.ReplaceAll(function, "-o root -g oboard ", "")
				function = strings.ReplaceAll(function, "-o root -g root ", "")
				return strings.ReplaceAll(function, "-o oboard -g oboard ", "")
			}
			run := func(t *testing.T, function, dataRoot string) error {
				t.Helper()
				cmd := exec.Command(bash, "-c", function+"\nCONTROLLER_DATA_DIR="+shellQuote(dataRoot)+"\nprepare_controller_updater_runtime")
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
				if err := run(t, rewrite(runtimeRoot), dataRoot); err != nil {
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
				if err := run(t, rewrite(runtimeRoot), dataRoot); err != nil {
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
				if err := run(t, rewrite(filepath.Join(temp, "run")), dataRoot); err == nil {
					t.Fatal("runtime preparation accepted a symlink data root")
				}
			})
		})
	}
}

func controllerInstallScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "scripts", "install.sh"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func extractShellFunction(t *testing.T, script, name string) string {
	t.Helper()
	needle := name + "() {"
	start := -1
	if strings.HasPrefix(script, needle) {
		start = 0
	} else {
		start = strings.Index(script, "\n"+needle)
		if start >= 0 {
			start++
		}
	}
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
	coLocated := strings.Contains(script, "CONTROLLER_CONFIG_DIR=")
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
		wantPackages := "openssl socat\nacme.sh\n"
		if coLocated {
			wantPackages = "openssl socat\n"
		}
		if result.packageLog != wantPackages {
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
		_, err := os.Stat(result.target)
		if coLocated && err != nil {
			t.Fatalf("co-located acme.sh was not installed: %v", err)
		}
		if !coLocated && !os.IsNotExist(err) {
			t.Fatalf("existing acme.sh was replaced by fallback target: %v", err)
		}
	})

	if !coLocated {
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
	}

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
	singleRoot := strings.Contains(script, "CONTROLLER_CONFIG_DIR=")
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
	valid := []struct{ name, input, want string }{
		{name: "default", input: "", want: "/opt/oboard"},
		{name: "opt", input: "/opt/oboard", want: "/opt/oboard"},
		{name: "local", input: "/usr/local/oboard", want: "/usr/local/oboard"},
		{name: "custom", input: "/data/oboard", want: "/data/oboard"},
		{name: "trim trailing slash", input: "/data/oboard/", want: "/data/oboard"},
	}
	if !singleRoot {
		valid = append(valid,
			struct{ name, input, want string }{name: "local bin", input: "/usr/local/bin", want: "/usr/local/bin"},
			struct{ name, input, want string }{name: "local sbin", input: "/usr/local/sbin", want: "/usr/local/sbin"},
		)
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.Command(shell, "-c", source+"\ninstall_dir_from_input "+shellQuote(test.input)).CombinedOutput()
			if err != nil || strings.TrimSpace(string(output)) != test.want {
				t.Fatalf("input %q = %q, err=%v; want %q", test.input, output, err, test.want)
			}
		})
	}
	invalid := []string{"data/oboard", "/", "/data//oboard", "/data/../etc", "/data/oboard path", "/data/oboard;rm"}
	if singleRoot {
		invalid = append(invalid, "/usr/local/bin", "/usr/local/sbin", "/usr/local/bin/oboard", "/var/lib", "/opt", "/data", "/home/user/oboard", "/proc/oboard")
	}
	for _, input := range invalid {
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
  printf '%s\n' "$*" >> "$PACKAGE_LOG"
}`,
		": > \"$PACKAGE_LOG\"",
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

func TestControllerInstallHistorySummaryDefaults(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	scriptBytes, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, tc := range []struct {
		name, action, existing string
		data                   bool
		want                   string
	}{
		{"fresh", "install", "", false, "1"},
		{"upgrade-missing-config", "update", "", false, "0"},
		{"reinstall-with-data", "install", "", true, "0"},
		{"old-config", "update", "OBOARD_ADDR=:2787\n", true, ""},
		{"explicit-disabled", "install", "OBOARD_LATENCY_ROLLUP_READ=0\n", false, ""},
		{"enabled-config", "update", "OBOARD_LATENCY_ROLLUP_READ=1\n", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			env := filepath.Join(dir, "controller.env")
			if tc.existing != "" {
				if err := os.WriteFile(env, []byte(tc.existing), 0600); err != nil {
					t.Fatal(err)
				}
			}
			data := "0"
			if tc.data {
				data = "1"
			}
			harness := strings.Join([]string{"set -eu", "CONTROLLER_CONFIG_DIR=" + shellQuote(dir), "CONTROLLER_ENV=" + shellQuote(env), "ACTION=" + tc.action, "CONTROLLER_DATA_EXISTED=" + data, extractShellFunction(t, script, "set_controller_env_value"), extractShellFunction(t, script, "initialize_controller_env"), "initialize_controller_env " + shellQuote(filepath.Join(root, "deploy", "controller.env.example"))}, "\n")
			if out, err := exec.Command(testPOSIXShell(t), "-c", harness).CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			got, err := os.ReadFile(env)
			if err != nil {
				t.Fatal(err)
			}
			if tc.existing != "" {
				if string(got) != tc.existing {
					t.Fatal("existing configuration changed")
				}
				return
			}
			for _, key := range []string{"OBOARD_LATENCY_ROLLUP_WRITE", "OBOARD_LATENCY_ROLLUP_READ", "OBOARD_SLA_PROJECTION_WRITE", "OBOARD_SLA_PROJECTION_READ"} {
				if !strings.Contains(string(got), key+"="+tc.want+"\n") && !strings.Contains(string(got), key+"=\""+tc.want+"\"\n") {
					t.Errorf("wrong default for %s", key)
				}
			}
		})
	}
}
