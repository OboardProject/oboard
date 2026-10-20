package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestAgentSelfUpdateRepairsEmptyCoreIdentity(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is unavailable")
	}
	script := testAgentSelfUpdateScript(t)
	start := strings.Index(script, `if [ -f "$CONFIG_PATH" ]; then`)
	end := strings.Index(script, "\nrestart_agent_delayed()")
	if start < 0 || end <= start {
		t.Fatal("self-update config repair block is missing")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"core_binary":"","core_service":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	shell := testPOSIXShell(t)
	cmd := exec.Command(shell, "-c", script[start:end])
	cmd.Env = append(os.Environ(), "CONFIG_PATH="+configPath, "INSTALL_DIR=/opt/oboard", "TARGET_DEV=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("self-update config repair failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["core_binary"] != "/opt/oboard/oboard-sb" || config["core_service"] != "oboard-sb" {
		t.Fatalf("empty core identity was not repaired: %#v", config)
	}
}

// realm ships with the Agent rather than being installed by the operator, so
// both Controller-hosted scripts must download it, cover it with the signed
// manifest check, install it atomically, and remove it on uninstall.
func TestAgentScriptsInstallBundledRealm(t *testing.T) {
	realmAsset := `realm_name="oboard-realm-${OS_VALUE}-${ARCH_VALUE}"`
	for name, script := range map[string]string{
		"installer":   testAgentInstallScript(t),
		"self-update": testAgentSelfUpdateScript(t),
	} {
		for _, want := range []string{
			realmAsset,
			`"$agent_name" "$core_name" "$realm_name"`,
			`install -m 0755 "$tmp/$realm_name" "$INSTALL_DIR/oboard-realm.new"`,
			`mv -f "$INSTALL_DIR/oboard-realm.new" "$INSTALL_DIR/oboard-realm"`,
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s script is missing %q", name, want)
			}
		}
		// A downloaded binary that never reaches the signed manifest check would
		// be installed unverified.
		if strings.Contains(script, `chmod 0755 "$tmp/$agent_name" "$tmp/$core_name"`+"\n") {
			t.Fatalf("%s script still prepares only the agent and kernel binaries", name)
		}
	}
	installer := testAgentInstallScript(t)
	if !strings.Contains(installer, `rm -f "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/oboard-sb" "$INSTALL_DIR/oboard-realm" "$INSTALL_DIR/obag"`) {
		t.Fatal("uninstall branch leaves the bundled realm binary behind")
	}
	if !strings.Contains(installer, `--setenv=REALM_NAME="$realm_name"`) && !strings.Contains(testAgentSelfUpdateScript(t), `--setenv=REALM_NAME="$realm_name"`) {
		t.Fatal("the systemd fallback install path does not receive the realm asset name")
	}
}

func TestAgentInstallScriptBBRIsInstallOnly(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, want := range []string{
		"OBOARD_INSTALL_BBR",
		"INSTALL_BBR=${OBOARD_INSTALL_BBR:-off}",
		"OBOARD_INSTALL_BBR_CAKE_BANDWIDTH",
		"enable_bbr_qdisc",
		"try_enable_bbr_qdisc",
		"net.core.default_qdisc = $1",
		"net.ipv4.tcp_congestion_control = bbr",
		"安装程序不会自动更换内核",
		"[1/4] 检查运行环境",
		"[2/4] 下载 Agent 组件",
		"[3/4] 校验并安装组件",
		"[4/4] 注册并启动 Agent 服务",
		`-core-binary "$INSTALL_DIR/oboard-sb"`,
		"-core-service oboard-sb",
		"详细日志：$INSTALL_LOG",
		"管理 Agent：$management_command",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Agent installer is missing %q", want)
		}
	}
	installCase := shellCaseBranch(t, script, "install)", "update)")
	if strings.Count(installCase, "try_enable_bbr_qdisc") != 2 {
		t.Fatal("both Agent install branches must apply the requested BBR qdisc")
	}
	if strings.Index(installCase, "try_enable_bbr_qdisc") > strings.Index(installCase, "-enroll-only") {
		t.Fatal("Agent installer consumes the enrollment token before attempting BBR")
	}
	if strings.Index(installCase, "try_enable_bbr_qdisc") > strings.Index(installCase, "try_enable_tcp_tuning") {
		t.Fatal("TCP tuning must run after BBR so it can follow the applied qdisc")
	}
	updateCase := shellCaseBranch(t, script, "update)", "uninstall)")
	if strings.Contains(updateCase, "try_enable_bbr_qdisc") || strings.Contains(updateCase, "configure_cake_shaper") {
		t.Fatal("Agent update branch repeats BBR configuration")
	}
	for _, shellName := range []string{"bash", "dash"} {
		shell, err := exec.LookPath(shellName)
		if err != nil {
			continue
		}
		cmd := exec.Command(shell, "-n")
		cmd.Stdin = strings.NewReader(script)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Agent installer %s syntax error: %v\n%s", shellName, err, output)
		}
	}
}

func TestAgentDownloadProgressOutput(t *testing.T) {
	for _, installer := range []struct {
		name   string
		script string
	}{{name: "install", script: testAgentInstallScript(t)}, {name: "self-update", script: testAgentSelfUpdateScript(t)}} {
		t.Run(installer.name, func(t *testing.T) {
			if !strings.Contains(installer.script, "--progress-bar") {
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
    printf '200 https://mirror.example/agent'
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
				extractShellFunction(t, installer.script, "format_download_value"),
				extractShellFunction(t, installer.script, "resolve_download_url"),
				extractShellFunction(t, installer.script, "download_component"),
				"download_component Agent https://panel.example/downloads/agent " + shellQuote(filepath.Join(root, "agent")),
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
			for _, want := range []string{"--silent", "--write-out", "%{size_download} %{speed_download}"} {
				if !strings.Contains(string(log), want) {
					t.Errorf("curl invocation missing %q: %s", want, log)
				}
			}
			// The redirect chain is resolved first so the metered transfer
			// draws a single progress bar instead of one bar per hop.
			if !strings.Contains(string(log), "https://mirror.example/agent") {
				t.Errorf("metered download did not use the resolved URL: %s", log)
			}
			if count := strings.Count(string(log), "--continue-at -"); count != 1 {
				t.Errorf("metered transfer count = %d, want 1: %s", count, log)
			}
		})
	}
}

func TestAgentDownloadResumesInterruptedTransferAndStopsAfterThreeAttempts(t *testing.T) {
	for _, installer := range []struct {
		name   string
		script string
	}{{name: "install", script: testAgentInstallScript(t)}, {name: "self-update", script: testAgentSelfUpdateScript(t)}} {
		t.Run(installer.name, func(t *testing.T) {
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
    printf '200 https://panel.example/downloads/agent'
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
			destination := filepath.Join(root, "agent")
			harness := strings.Join([]string{
				"set -eu",
				extractShellFunction(t, installer.script, "format_download_value"),
				extractShellFunction(t, installer.script, "resolve_download_url"),
				extractShellFunction(t, installer.script, "download_component"),
				"download_component Agent https://panel.example/downloads/agent " + shellQuote(destination),
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
				extractShellFunction(t, installer.script, "download_quiet"),
				"download_quiet https://panel.example/downloads/release-manifest.json " + shellQuote(destination),
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
		})
	}
}

func TestAgentInstallScriptRegistersObagPath(t *testing.T) {
	for _, installer := range []struct {
		name   string
		script string
	}{{name: "install", script: testAgentInstallScript(t)}, {name: "self-update", script: testAgentSelfUpdateScript(t)}} {
		t.Run(installer.name, func(t *testing.T) {
			shell := testPOSIXShell(t)
			root := t.TempDir()
			profileDir := filepath.Join(root, "profile.d")
			harness := strings.Join([]string{
				"set -eu",
				"INSTALL_DIR=/opt/oboard",
				"OBOARD_PROFILE_DIR=" + shellQuote(profileDir),
				"PATH=/usr/bin:/bin",
				extractShellFunction(t, installer.script, "register_obag_path"),
				"register_obag_path",
				`grep -Fq '/opt/oboard' "$OBOARD_PROFILE_DIR/oboard-agent.sh"`,
				"echo registered",
			}, "\n")
			output, err := exec.Command(shell, "-c", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("register_obag_path failed: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "registered") {
				t.Fatalf("register_obag_path did not register PATH:\n%s", output)
			}
			if _, err := os.Stat(filepath.Join(profileDir, "oboard-agent.sh")); err != nil {
				t.Fatalf("profile snippet was not written: %v", err)
			}
			rerun := strings.Join([]string{
				"set -eu",
				"INSTALL_DIR=/opt/oboard",
				"OBOARD_PROFILE_DIR=" + shellQuote(profileDir),
				"PATH=/opt/oboard:/usr/bin:/bin",
				extractShellFunction(t, installer.script, "register_obag_path"),
				"register_obag_path",
				"echo second-run-ok",
			}, "\n")
			if output, err := exec.Command(shell, "-c", rerun).CombinedOutput(); err != nil || !strings.Contains(string(output), "second-run-ok") {
				t.Fatalf("register_obag_path is not idempotent: %v\n%s", err, output)
			}
		})
	}
}

func TestAgentInstallScriptSkipsStandardPathRegistration(t *testing.T) {
	script := testAgentInstallScript(t)
	shell := testPOSIXShell(t)
	root := t.TempDir()
	profileDir := filepath.Join(root, "profile.d")
	harness := strings.Join([]string{
		"set -eu",
		"INSTALL_DIR=/usr/local/bin",
		"OBOARD_PROFILE_DIR=" + shellQuote(profileDir),
		extractShellFunction(t, script, "register_obag_path"),
		"register_obag_path",
		`[ -e "$OBOARD_PROFILE_DIR/oboard-agent.sh" ] && exit 9 || echo standard-path-skipped`,
	}, "\n")
	output, err := exec.Command(shell, "-c", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("standard path was registered anyway: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "standard-path-skipped") {
		t.Fatalf("unexpected output:\n%s", output)
	}
}

// bbrHarness renders the BBR helpers against fake sysctl files, a fake PATH
// (ip, tc, modprobe, systemctl) and a fake kernel release.
type bbrHarness struct {
	root, available, congestion, qdisc, config, unitDir, bin, tcLog string
}

func newBBRHarness(t *testing.T) bbrHarness {
	t.Helper()
	root := t.TempDir()
	h := bbrHarness{
		root:       root,
		available:  filepath.Join(root, "available"),
		congestion: filepath.Join(root, "congestion"),
		qdisc:      filepath.Join(root, "qdisc"),
		config:     filepath.Join(root, "sysctl.d", "99-oboard-bbr.conf"),
		unitDir:    filepath.Join(root, "systemd"),
		bin:        filepath.Join(root, "bin"),
		tcLog:      filepath.Join(root, "tc.log"),
	}
	writeTestFile(t, h.available, "reno cubic bbr\n")
	writeTestFile(t, h.congestion, "cubic\n")
	writeTestFile(t, h.qdisc, "fq_codel\n")
	for _, dir := range []string{h.bin, h.unitDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(h.bin, "ip"), `#!/bin/sh
case "$*" in
  *-4*) echo "default via 203.0.113.1 dev eth0 proto static" ;;
  *-6*) echo "default via fe80::1 dev eth0 proto ra metric 1024" ;;
esac
`)
	writeExecutable(t, filepath.Join(h.bin, "tc"), `#!/bin/sh
echo "$*" >> "$TEST_TC_LOG"
case "$*" in
  "qdisc show"*) echo "qdisc mq 0: root" ;;
  *"root mq"*) exit "${TEST_TC_MQ_STATUS:-0}" ;;
esac
`)
	writeExecutable(t, filepath.Join(h.bin, "modprobe"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(h.bin, "systemctl"), "#!/bin/sh\necho \"systemctl $*\" >> \"$TEST_TC_LOG\"\n")
	return h
}

func (h bbrHarness) run(t *testing.T, script, qdisc, bandwidth, kernel string, extra ...string) (string, error) {
	t.Helper()
	lines := []string{
		"set -eu",
		"INSTALL_BBR=" + shellQuote(qdisc),
		"INSTALL_BBR_CAKE_BANDWIDTH=" + shellQuote(bandwidth),
		"BBR_QDISC_REQUESTED=",
		"BBR_QDISC_APPLIED=",
		"SERVICE_MANAGER=systemd",
		"CAKE_SHAPER_SERVICE=oboard-cake-shaper",
		"CAKE_SHAPER_SYSTEMD_DIR=" + shellQuote(h.unitDir),
		"CAKE_SHAPER_OPENRC_DIR=" + shellQuote(filepath.Join(h.root, "init.d")),
		"BBR_AVAILABLE_PATH=" + shellQuote(h.available),
		"BBR_CONGESTION_PATH=" + shellQuote(h.congestion),
		"BBR_QDISC_PATH=" + shellQuote(h.qdisc),
		"BBR_CONFIG_PATH=" + shellQuote(h.config),
	}
	for _, name := range []string{"bbr_requested", "bbr_qdisc_label", "bbr_kernel_release", "kernel_version_at_least", "bbr_qdisc_min_kernel", "bbr_available", "persist_bbr_qdisc", "bbr_restore_previous", "bbr_default_interfaces", "bbr_refresh_interface_qdisc", "cake_shaper_command", "remove_cake_shaper", "configure_cake_shaper", "enable_bbr_qdisc", "try_enable_bbr_qdisc"} {
		lines = append(lines, extractShellFunction(t, script, name))
	}
	cmd := exec.Command(testPOSIXShell(t), "-c", strings.Join(append(lines, extra...), "\n"))
	cmd.Env = append(os.Environ(), "PATH="+h.bin+":"+os.Getenv("PATH"), "OBOARD_KERNEL_RELEASE="+kernel, "TEST_TC_LOG="+h.tcLog)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (h bbrHarness) tcCalls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.tcLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func TestAgentInstallScriptContinuesWhenBBRIsUnavailable(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newBBRHarness(t)
	h.congestion = filepath.Join(h.root, "missing-congestion")
	output, err := h.run(t, script, "fq", "0", "6.1.0-18-amd64", "try_enable_bbr_qdisc", "echo install-continued")
	if err != nil {
		t.Fatalf("unavailable BBR stopped installation: %v\n%s", err, output)
	}
	if !strings.Contains(output, "常见于受限容器") || !strings.Contains(output, "Agent 安装将继续") || !strings.Contains(output, "install-continued") {
		t.Fatalf("unexpected unavailable BBR output:\n%s", output)
	}
}

func TestAgentInstallScriptEnablesAndPersistsBBRFQ(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newBBRHarness(t)
	if output, err := h.run(t, script, "fq", "0", "6.1.0-18-amd64", "enable_bbr_qdisc", `echo "applied=$BBR_QDISC_APPLIED"`); err != nil || !strings.Contains(output, "applied=fq") {
		t.Fatalf("enable BBR + FQ failed: %v\n%s", err, output)
	}
	assertTestFile(t, h.congestion, "bbr\n")
	assertTestFile(t, h.qdisc, "fq\n")
	assertTestFile(t, h.config, "net.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n")
	assertPathMode(t, h.config, 0o600)
	if calls := h.tcCalls(t); !strings.Contains(calls, "qdisc replace dev eth0 root mq") || strings.Contains(calls, "cake") {
		t.Fatalf("egress interface was not refreshed with the new default qdisc:\n%s", calls)
	}
}

func TestAgentInstallScriptAppliesCakeWithBandwidth(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newBBRHarness(t)
	output, err := h.run(t, script, "cake", "950", "6.1.0-18-amd64", "enable_bbr_qdisc")
	if err != nil {
		t.Fatalf("enable BBR + CAKE failed: %v\n%s", err, output)
	}
	assertTestFile(t, h.qdisc, "cake\n")
	assertTestFile(t, h.config, "net.core.default_qdisc = cake\nnet.ipv4.tcp_congestion_control = bbr\n")
	calls := h.tcCalls(t)
	if !strings.Contains(calls, "qdisc replace dev eth0 root cake bandwidth 950mbit") || strings.Contains(calls, "root mq") {
		t.Fatalf("cake shaping was not applied to the egress interface:\n%s", calls)
	}
	if !strings.Contains(calls, "systemctl enable oboard-cake-shaper") {
		t.Fatalf("cake shaper unit was not enabled:\n%s", calls)
	}
	unit, err := os.ReadFile(filepath.Join(h.unitDir, "oboard-cake-shaper.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "qdisc replace dev eth0 root cake bandwidth 950mbit") || !strings.Contains(string(unit), "Type=oneshot") {
		t.Fatalf("unexpected cake shaper unit:\n%s", unit)
	}
	if !strings.Contains(output, "950 Mbit/s") {
		t.Fatalf("cake bandwidth is not reported:\n%s", output)
	}

	// Reinstalling with plain fq removes the boot-time shaper.
	output, err = h.run(t, script, "fq", "0", "6.1.0-18-amd64", "enable_bbr_qdisc")
	if err != nil {
		t.Fatalf("switching back to fq failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(h.unitDir, "oboard-cake-shaper.service")); !os.IsNotExist(err) {
		t.Fatalf("cake shaper unit survived a switch to fq: %v", err)
	}
	assertTestFile(t, h.qdisc, "fq\n")
}

func TestAgentInstallScriptFallsBackToFQOnOldKernel(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, tc := range []struct{ qdisc, kernel, want string }{
		{"cake", "4.14.0-xen", "需要 4.19 及以上"},
		{"fq_pie", "5.4.0-150-generic", "需要 5.6 及以上"},
	} {
		h := newBBRHarness(t)
		output, err := h.run(t, script, tc.qdisc, "500", tc.kernel, "enable_bbr_qdisc")
		if err != nil {
			t.Fatalf("%s on %s failed: %v\n%s", tc.qdisc, tc.kernel, err, output)
		}
		if !strings.Contains(output, tc.want) || !strings.Contains(output, "改用 BBR + FQ") {
			t.Fatalf("%s on %s did not explain the fallback:\n%s", tc.qdisc, tc.kernel, output)
		}
		assertTestFile(t, h.qdisc, "fq\n")
		if strings.Contains(h.tcCalls(t), "cake") {
			t.Fatalf("%s fallback still shaped with cake:\n%s", tc.qdisc, h.tcCalls(t))
		}
	}

	h := newBBRHarness(t)
	output, err := h.run(t, script, "fq", "0", "4.4.0-210-generic", "enable_bbr_qdisc")
	if err == nil || !strings.Contains(output, "低于 4.9") {
		t.Fatalf("BBR was attempted on a kernel without it: %v\n%s", err, output)
	}
	assertTestFile(t, h.congestion, "cubic\n")
}

func TestAgentInstallScriptRejectsInvalidBBRValues(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, tc := range []struct{ qdisc, bandwidth string }{{"1", "0"}, {"pfifo", "0"}, {"cake", "-5"}, {"cake", "0100"}, {"cake", "100001"}} {
		h := newBBRHarness(t)
		output, err := h.run(t, script, tc.qdisc, tc.bandwidth, "6.1.0", "enable_bbr_qdisc")
		if err == nil || !strings.Contains(output, "请回到面板重新复制安装命令") {
			t.Fatalf("invalid BBR option %+v was accepted: %v\n%s", tc, err, output)
		}
		assertTestFile(t, h.congestion, "cubic\n")
	}
}

func TestAgentInstallScriptRollsBackBBRWhenPersistenceFails(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newBBRHarness(t)
	h.config = filepath.Join(h.root, "invalid-config")
	if err := os.Mkdir(h.config, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := h.run(t, script, "cake", "0", "6.1.0", "enable_bbr_qdisc"); err == nil {
		t.Fatalf("invalid BBR persistence path was accepted\n%s", output)
	}
	assertTestFile(t, h.congestion, "cubic\n")
	assertTestFile(t, h.qdisc, "fq_codel\n")
}

func TestAgentInstallScriptTCPTuningFollowsBBRQdisc(t *testing.T) {
	script := testAgentInstallScript(t)
	root := t.TempDir()
	proc := filepath.Join(root, "proc-sys")
	config := filepath.Join(root, "sysctl.d", "99-oboard-tcp.conf")
	qdisc := writeTestProcKey(t, proc, "net.core.default_qdisc", "fq_codel\n")
	harness := tcpTuningHarness(t, script, proc, config, "BBR_QDISC_APPLIED=cake", "enable_tcp_tuning")
	if output, err := exec.Command(testPOSIXShell(t), "-c", harness).CombinedOutput(); err != nil {
		t.Fatalf("enable_tcp_tuning failed: %v\n%s", err, output)
	}
	assertTestFile(t, qdisc, "cake\n")
	assertTestFile(t, config, "net.core.default_qdisc = cake\n")
}

func TestAgentScriptsSelectUpdateTempWithSystemdReserve(t *testing.T) {
	scripts := map[string]string{
		"installer":   testAgentInstallScript(t),
		"self-update": testAgentSelfUpdateScript(t),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			testAgentUpdateTempSelection(t, script)
		})
	}
}

func testAgentUpdateTempSelection(t *testing.T, script string) {
	t.Helper()
	shell := testPOSIXShell(t)
	function := extractShellFunction(t, script, "make_update_tmp")
	originalCleanup := `for cleanup_root in "${OBOARD_TMPDIR:-}" /var/tmp "$STATE_DIR" /tmp /run; do`
	originalCandidates := `for base in "${OBOARD_TMPDIR:-}" /var/tmp "$STATE_DIR" /tmp /run; do`
	if !strings.Contains(function, originalCleanup) || !strings.Contains(function, originalCandidates) {
		t.Fatal("make_update_tmp does not contain the expected cleanup and candidate order")
	}
	function = strings.Replace(function, originalCleanup, `for cleanup_root in "${OBOARD_TMPDIR:-}" "$TEST_VAR_TMP" "$STATE_DIR" "$TEST_TMP" "$TEST_RUN"; do`, 1)
	function = strings.Replace(function, originalCandidates, `for base in "${OBOARD_TMPDIR:-}" "$TEST_VAR_TMP" "$STATE_DIR" "$TEST_TMP" "$TEST_RUN"; do`, 1)

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "df"), `#!/bin/sh
target=${2:-}
available=0
case "$target" in
  */custom/*) available=${CUSTOM_AVAILABLE_KB:-0} ;;
  */var-tmp/*) available=${VAR_TMP_AVAILABLE_KB:-0} ;;
  */state/*) available=${STATE_AVAILABLE_KB:-0} ;;
  */tmp/*) available=${TMP_AVAILABLE_KB:-0} ;;
  */run/*) available=${RUN_AVAILABLE_KB:-0} ;;
esac
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf 'fake 999999 0 %s 0%% /\n' "$available"
`)

	custom := filepath.Join(root, "custom")
	varTmp := filepath.Join(root, "var-tmp")
	state := filepath.Join(root, "state")
	tmp := filepath.Join(root, "tmp")
	run := filepath.Join(root, "run")
	for _, dir := range []string{custom, varTmp, state, tmp, run} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	runSelection := func(t *testing.T, customDir string, availability ...string) ([]byte, error) {
		t.Helper()
		harness := strings.Join([]string{
			"set -eu",
			"OBOARD_TMPDIR=" + shellQuote(customDir),
			"TEST_VAR_TMP=" + shellQuote(varTmp),
			"STATE_DIR=" + shellQuote(state),
			"TEST_TMP=" + shellQuote(tmp),
			"TEST_RUN=" + shellQuote(run),
			function,
			"make_update_tmp",
		}, "\n")
		cmd := exec.Command(shell, "-c", harness)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		cmd.Env = append(cmd.Env, availability...)
		return cmd.CombinedOutput()
	}

	t.Run("prefers disk-backed directory over small run", func(t *testing.T) {
		output, err := runSelection(t, "",
			"VAR_TMP_AVAILABLE_KB=8388608",
			"STATE_AVAILABLE_KB=8388608",
			"TMP_AVAILABLE_KB=8388608",
			"RUN_AVAILABLE_KB=47104",
		)
		if err != nil {
			t.Fatalf("make_update_tmp failed: %v\n%s", err, output)
		}
		if selected := strings.TrimSpace(string(output)); !strings.HasPrefix(selected, varTmp+string(os.PathSeparator)) {
			t.Fatalf("selected %q, want a directory under %s", selected, varTmp)
		}
	})

	t.Run("honors explicit directory first", func(t *testing.T) {
		output, err := runSelection(t, custom,
			"CUSTOM_AVAILABLE_KB=65536",
			"VAR_TMP_AVAILABLE_KB=8388608",
			"RUN_AVAILABLE_KB=47104",
		)
		if err != nil {
			t.Fatalf("make_update_tmp failed: %v\n%s", err, output)
		}
		if selected := strings.TrimSpace(string(output)); !strings.HasPrefix(selected, custom+string(os.PathSeparator)) {
			t.Fatalf("selected %q, want a directory under %s", selected, custom)
		}
	})

	t.Run("rejects run below reserve threshold", func(t *testing.T) {
		output, err := runSelection(t, "", "RUN_AVAILABLE_KB=47104")
		if err == nil {
			t.Fatalf("make_update_tmp accepted a 47 MB /run candidate: %s", output)
		}
		if !strings.Contains(string(output), "至少 64 MB 可用空间") {
			t.Fatalf("unexpected low-space error:\n%s", output)
		}
	})
}

func TestAgentReleaseVerifierSupportsOldAndNewOpenSSL(t *testing.T) {
	scripts := map[string]string{
		"installer":   testAgentInstallScript(t),
		"self-update": testAgentSelfUpdateScript(t),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{"py3-cryptography", "python3-cryptography", "python-cryptography"} {
				if !strings.Contains(script, want) {
					t.Fatalf("release verifier is missing %q", want)
				}
			}
			assertAgentReleaseVerifierBehavior(t, script)
		})
	}
}

func assertAgentReleaseVerifierBehavior(t *testing.T, script string) {
	t.Helper()
	shell := testPOSIXShell(t)
	source := strings.Join([]string{
		extractShellFunction(t, script, "openssl_supports_ed25519"),
		extractShellFunction(t, script, "verify_ed25519_signature"),
	}, "\n")

	t.Run("cryptography fallback", func(t *testing.T) {
		root := t.TempDir()
		bin := filepath.Join(root, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		opensslLog := filepath.Join(root, "openssl.log")
		pythonLog := filepath.Join(root, "python.log")
		writeExecutable(t, filepath.Join(bin, "openssl"), `#!/bin/sh
if [ "$1" = pkeyutl ] && [ "$2" = -help ]; then
  exit 0
fi
printf '%s\n' "$*" > "$OPENSSL_LOG"
exit 91
`)
		writeExecutable(t, filepath.Join(bin, "python3"), `#!/bin/sh
printf '%s\n' "$*" > "$PYTHON_LOG"
payload=$(cat)
case "$payload" in
  *Ed25519PublicKey.from_public_bytes*) ;;
  *) exit 92 ;;
esac
exit "${PYTHON_VERIFY_EXIT:-0}"
`)
		harness := strings.Join([]string{
			source,
			"verify_ed25519_signature public.raw public.der manifest.json release.sig",
		}, "\n")
		cmd := exec.Command(shell, "-c", harness)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OPENSSL_LOG="+opensslLog, "PYTHON_LOG="+pythonLog)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cryptography fallback failed: %v\n%s", err, output)
		}
		assertTestFile(t, pythonLog, "- public.raw manifest.json release.sig\n")
		if _, err := os.Stat(opensslLog); !os.IsNotExist(err) {
			t.Fatalf("old OpenSSL was used for Ed25519 verification: %v", err)
		}

		cmd = exec.Command(shell, "-c", harness)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OPENSSL_LOG="+opensslLog, "PYTHON_LOG="+pythonLog, "PYTHON_VERIFY_EXIT=1")
		if output, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("invalid signature was accepted\n%s", output)
		}
	})

	t.Run("OpenSSL 3", func(t *testing.T) {
		root := t.TempDir()
		bin := filepath.Join(root, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		opensslLog := filepath.Join(root, "openssl.log")
		writeExecutable(t, filepath.Join(bin, "openssl"), `#!/bin/sh
if [ "$1" = pkeyutl ] && [ "$2" = -help ]; then
  echo '-rawin'
  exit 0
fi
printf '%s\n' "$*" > "$OPENSSL_LOG"
`)
		writeExecutable(t, filepath.Join(bin, "python3"), "#!/bin/sh\nexit 93\n")
		cmd := exec.Command(shell, "-c", source+"\nverify_ed25519_signature public.raw public.der manifest.json release.sig")
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OPENSSL_LOG="+opensslLog)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("OpenSSL 3 verifier failed: %v\n%s", err, output)
		}
		assertTestFile(t, opensslLog, "pkeyutl -verify -pubin -inkey public.der -rawin -in manifest.json -sigfile release.sig\n")
	})
}

func testAgentInstallScript(t *testing.T) string {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	New(db, "test-secret", "", "", nil).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/install/agent.sh", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Agent installer status=%d body=%s", response.Code, response.Body.String())
	}
	return response.Body.String()
}

func testAgentSelfUpdateScript(t *testing.T) string {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	New(db, "test-secret", "", "", nil).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/install/agent-self-update.sh", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Agent self-update installer status=%d body=%s", response.Code, response.Body.String())
	}
	return response.Body.String()
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func shellCaseBranch(t *testing.T, script, start, end string) string {
	t.Helper()
	startIndex := strings.Index(script, "\n  "+start)
	if startIndex < 0 {
		t.Fatalf("script is missing %s branch", start)
	}
	rest := script[startIndex:]
	endIndex := strings.Index(rest, "\n  "+end)
	if endIndex < 0 {
		t.Fatalf("script is missing %s branch", end)
	}
	return rest[:endIndex]
}

func testPOSIXShell(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"dash", "bash", "sh"} {
		if shell, err := exec.LookPath(name); err == nil {
			return shell
		}
	}
	t.Skip("no POSIX shell is available")
	return ""
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != want {
		t.Fatalf("%s = %q, want %q", path, value, want)
	}
}

// Both Controller-hosted shell scripts must stay valid POSIX shell.
func TestAgentShellScriptsParse(t *testing.T) {
	shell := testPOSIXShell(t)
	for name, script := range map[string]string{
		"installer":   testAgentInstallScript(t),
		"self-update": testAgentSelfUpdateScript(t),
	} {
		path := filepath.Join(t.TempDir(), name+".sh")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(shell, "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
}

// oboard-sb handles SIGINT and SIGTERM only. A unit that advertises a reload is
// either a no-op that leaves the kernel on the old configuration, or - under
// OpenRC, which has no automatic restart - a plain kill.
func TestGeneratedUnitsDoNotAdvertiseKernelReload(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, forbidden := range []string{"ExecReload=", "extra_commands=\"reload\"", "--signal HUP"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("generated units still advertise a kernel reload: %q", forbidden)
		}
	}
}

// Replacing binaries is not an update. A restart that fails, or a kernel that
// comes back on the wrong build or configuration, must fail the script.
func TestAgentUpdateScriptsFailOnRestartAndVerificationErrors(t *testing.T) {
	for name, script := range map[string]string{
		"installer":   testAgentInstallScript(t),
		"self-update": testAgentSelfUpdateScript(t),
	} {
		for _, want := range []string{
			"acquire_core_lifecycle_lock",
			"release_core_lifecycle_lock",
			"preflight_staged_core",
			"verify_core_runtime",
			"-verify-core-runtime",
			"wait_service_stable",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s script is missing %q", name, want)
			}
		}
		// restart_agent_delayed is intentionally fire-and-forget: it runs a
		// minute later so the Agent can report its task result first, and the
		// Controller's own update state machine is what catches a restart that
		// never happens. Everything the script waits for must be fatal.
		checked := script
		if start := strings.Index(checked, "restart_agent_delayed() {"); start >= 0 {
			if end := strings.Index(checked[start:], "\n}\n"); end > 0 {
				checked = checked[:start] + checked[start+end:]
			}
		}
		for _, forbidden := range []string{
			"restart oboard-sb || true",
			"restart oboard-agent || true",
			"verify_installed_versions || true",
		} {
			if strings.Contains(checked, forbidden) {
				t.Fatalf("%s script still ignores a failure: %q", name, forbidden)
			}
		}
	}
}

// The Agent serializes its kernel work with an in-process mutex a shell script
// cannot see, so both sides must take the same advisory flock file.
func TestAgentScriptsShareTheAgentCoreLifecycleLockFile(t *testing.T) {
	for name, script := range map[string]string{
		"installer":   testAgentInstallScript(t),
		"self-update": testAgentSelfUpdateScript(t),
	} {
		if !strings.Contains(script, `core_lock_path="$STATE_DIR/core-lifecycle.lock"`) {
			t.Fatalf("%s script does not take the Agent core lifecycle lock", name)
		}
		if !strings.Contains(script, "until flock -n 9; do") {
			t.Fatalf("%s script does not wait on the lock", name)
		}
		// BusyBox flock has no -w, and its usage error is indistinguishable from
		// a busy lock, so an Alpine host would report a phantom deployment.
		if strings.Contains(script, "flock -w") {
			t.Fatalf("%s script waits with an option BusyBox flock rejects", name)
		}
	}
}

// A BusyBox host has flock(1), but only [-sxun]. The bounded retry loop must
// still wait for a busy lock there instead of aborting the operator's update.
func TestInstallerLockHelperWaitsWithBusyBoxFlock(t *testing.T) {
	shell := testPOSIXShell(t)
	script := testAgentInstallScript(t)
	start := strings.Index(script, "\nCORE_LOCK_HELD=0")
	if start < 0 {
		t.Fatal("installer lock helper is missing")
	}
	end := strings.Index(script[start:], "\nservice_active() {")
	if end <= 0 {
		t.Fatal("installer lock helper block has no end")
	}
	helpers := script[start : start+end]

	stub := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	// BusyBox rejects -w outright; the first -n attempt reports the lock busy.
	writeExecutable(t, filepath.Join(stub, "flock"), `#!/bin/sh
case "$*" in
  *-w*) echo "flock: unrecognized option: w" >&2; exit 1 ;;
esac
if [ ! -f "$STUB_STATE/tried" ]; then
  : > "$STUB_STATE/tried"
  exit 1
fi
exit 0
`)

	state := t.TempDir()
	driver := "set -eu\nSTATE_DIR=" + t.TempDir() + "\n" + helpers + "\nacquire_core_lifecycle_lock\nrelease_core_lifecycle_lock\necho reached-end\n"
	cmd := exec.Command(shell, "-c", driver)
	cmd.Env = append(os.Environ(), "PATH="+stub+":"+os.Getenv("PATH"), "STUB_STATE="+state)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lock helper aborted on a BusyBox host: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "reached-end") {
		t.Fatalf("lock helper did not return: %s", out)
	}
	if strings.Contains(string(out), "另一个 OBoard 更新") {
		t.Fatalf("lock helper reported a phantom concurrent deployment: %s", out)
	}
	if _, err := os.Stat(filepath.Join(state, "tried")); err != nil {
		t.Fatalf("lock helper did not retry a busy lock: %v", err)
	}
}

// A syntax check does not catch `set -e` traps or an off-by-one in the
// stability window, so the service helpers are executed against stubs.
func TestInstallerServiceHelpersDetectAFlappingService(t *testing.T) {
	shell := testPOSIXShell(t)
	script := testAgentInstallScript(t)
	start := strings.Index(script, "\nservice_active() {")
	if start < 0 {
		t.Fatal("installer service helpers are missing")
	}
	end := strings.Index(script[start:], "\nresolve_agent_install_dir\n")
	if end <= 0 {
		t.Fatal("installer service helper block has no end")
	}
	helpers := script[start : start+end]

	dir := t.TempDir()
	stub := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	// systemctl reports the service active only while the marker file exists.
	writeExecutable(t, filepath.Join(stub, "systemctl"), `#!/bin/sh
case "$1 $2" in
  "is-active --quiet") [ -f "$STUB_STATE/active" ] ;;
  *) exit 0 ;;
esac
`)

	for _, tc := range []struct {
		name    string
		active  bool
		wantErr bool
	}{
		{name: "stable", active: true},
		{name: "down", active: false, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			if tc.active {
				writeTestFile(t, filepath.Join(state, "active"), "1")
			}
			driver := "set -eu\nSERVICE_MANAGER=systemd\n" + helpers + "\nwait_service_stable oboard-sb 4\n"
			cmd := exec.Command(shell, "-c", driver)
			cmd.Env = append(os.Environ(), "PATH="+stub+":"+os.Getenv("PATH"), "STUB_STATE="+state)
			out, err := cmd.CombinedOutput()
			if tc.wantErr && err == nil {
				t.Fatalf("a service that is not running must fail: %s", out)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("a running service must pass: %v\n%s", err, out)
			}
		})
	}
}

// The lock helper must survive an unwritable state directory and a host with no
// flock(1): the lock coordinates with the Agent, it does not authorize the work.
func TestInstallerLockHelperToleratesUnusableLockPath(t *testing.T) {
	shell := testPOSIXShell(t)
	script := testAgentInstallScript(t)
	start := strings.Index(script, "\nCORE_LOCK_HELD=0")
	if start < 0 {
		t.Fatal("installer lock helper is missing")
	}
	end := strings.Index(script[start:], "\nservice_active() {")
	if end <= 0 {
		t.Fatal("installer lock helper block has no end")
	}
	helpers := script[start : start+end]

	emptyPath := t.TempDir()
	for _, tc := range []struct {
		name     string
		stateDir string
	}{
		{name: "unwritable state dir", stateDir: "/proc/oboard-does-not-exist"},
		{name: "writable state dir", stateDir: t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			driver := "set -eu\nSTATE_DIR=" + tc.stateDir + "\n" + helpers + "\nacquire_core_lifecycle_lock\nrelease_core_lifecycle_lock\necho reached-end\n"
			cmd := exec.Command(shell, "-c", driver)
			// An empty PATH stands in for a host without flock(1).
			cmd.Env = append(os.Environ(), "PATH="+emptyPath)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("lock helper aborted the script: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "reached-end") {
				t.Fatalf("lock helper did not return: %s", out)
			}
		})
	}
}

func TestAgentComponentDownloadFallsBackWithoutPartialFile(t *testing.T) {
	for _, script := range []string{testAgentInstallScript(t), testAgentSelfUpdateScript(t)} {
		path := filepath.Join(t.TempDir(), "agent")
		harness := "set -eu\n" + extractShellFunction(t, script, "download_agent_component") + `
download_component() {
  case "$2" in
    *'?source=controller')
      [ ! -e "$3" ] || return 1
      printf verified > "$3"
      ;;
    *) printf partial > "$3"; return 1 ;;
  esac
}
` + "download_agent_component Agent https://panel.example/hidden/downloads/oboard-agent-linux-amd64 " + shellQuote(path)
		if output, err := exec.Command(testPOSIXShell(t), "-c", harness).CombinedOutput(); err != nil {
			t.Fatalf("fallback failed: %v\n%s", err, output)
		}
		if content, err := os.ReadFile(path); err != nil || string(content) != "verified" {
			t.Fatalf("fallback content = %q, err=%v", content, err)
		}
	}
}

// An OpenRC node must recover from a crash the same way a systemd node does.
// Without a supervisor OpenRC starts a service once and leaves it down, so an
// Agent killed by the OOM killer never reconnects and the Controller loses the
// only channel it could have recovered the node through.
func TestAgentInstallScriptOpenRCUnitsRestartOnCrash(t *testing.T) {
	script := testAgentInstallScript(t)
	start := strings.Index(script, "write_openrc_units() {")
	if start < 0 {
		t.Fatal("OpenRC unit rendering is missing")
	}
	end := strings.Index(script[start:], "\nwrite_units()")
	if end <= 0 {
		t.Fatal("OpenRC unit rendering is unbounded")
	}
	block := script[start : start+end]
	for _, want := range []string{
		"supervisor=supervise-daemon",
		"respawn_delay=5",
		"respawn_max=0",
	} {
		if strings.Count(block, want) != 2 {
			t.Fatalf("both OpenRC units must set %q, found %d", want, strings.Count(block, want))
		}
	}
	// supervise-daemon backgrounds the service itself; leaving command_background
	// set makes OpenRC double-fork and lose track of the supervised process.
	if strings.Contains(block, "command_background=true") {
		t.Fatal("command_background conflicts with supervise-daemon")
	}
}

// OpenRC starts a service by forking supervise-daemon from the calling shell,
// so the daemon inherits every descriptor the script left open. FD 9 carries
// the core lifecycle flock: an inherited copy keeps the lock held for as long
// as the kernel or Agent runs, long after the script exited, and every later
// panel deployment then fails with a concurrent-update error that no operator
// can find a cause for.
func TestInstallerDoesNotLeakTheCoreLockToAServiceDaemon(t *testing.T) {
	shell := testPOSIXShell(t)
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock(1) is unavailable")
	}
	script := testAgentInstallScript(t)
	lockStart := strings.Index(script, "\nCORE_LOCK_HELD=0")
	serviceStart := strings.Index(script, "\nservice_active() {")
	serviceEnd := strings.Index(script, "\nresolve_agent_install_dir\n")
	if lockStart < 0 || serviceStart <= lockStart || serviceEnd <= serviceStart {
		t.Fatal("installer lock or service helpers are missing")
	}
	helpers := script[lockStart:serviceStart] + script[serviceStart:serviceEnd]

	state := t.TempDir()
	stub := filepath.Join(state, "bin")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Stands in for OpenRC: a short-lived front end that leaves a supervised
	// daemon behind, inheriting whatever the calling shell held open.
	writeExecutable(t, filepath.Join(stub, "rc-service"), `#!/bin/sh
sleep 30 &
echo $! > "$STUB_STATE/daemon.pid"
`)

	driver := "set -eu\nSTATE_DIR=" + state + "\nINSTALL_LOG=" + filepath.Join(state, "install.log") +
		"\nSERVICE_MANAGER=openrc\n" + helpers +
		"\nacquire_core_lifecycle_lock\nrestart_managed_service oboard-sb\nrelease_core_lifecycle_lock\necho reached-end\n"
	cmd := exec.Command(shell, "-c", driver)
	cmd.Env = append(os.Environ(), "PATH="+stub+":"+os.Getenv("PATH"), "STUB_STATE="+state)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer restart helper failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "reached-end") {
		t.Fatalf("installer restart helper did not return: %s", out)
	}
	if raw, readErr := os.ReadFile(filepath.Join(state, "daemon.pid")); readErr == nil {
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		}
	} else {
		t.Fatalf("the stub service manager left no daemon behind: %v", readErr)
	}

	// The daemon is still running. The Agent must be able to take the lock
	// anyway, exactly as it does on its next deployment.
	lock, err := os.OpenFile(filepath.Join(state, "core-lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the service daemon inherited the core lifecycle lock: %v", err)
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
}

// The behavioural test above covers one call site. Every other place either
// script starts a managed service must close the same descriptor, so a new one
// cannot reintroduce the leak.
func TestAgentScriptsCloseTheCoreLockOnServiceRestarts(t *testing.T) {
	for name, script := range map[string]string{
		"installer":   testAgentInstallScript(t),
		"self-update": testAgentSelfUpdateScript(t),
	} {
		for _, line := range strings.Split(script, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			starts := strings.Contains(trimmed, "systemctl restart") ||
				strings.Contains(trimmed, "systemctl start") ||
				(strings.Contains(trimmed, "rc-service") && strings.Contains(trimmed, "restart")) ||
				(strings.Contains(trimmed, "rc-service") && strings.Contains(trimmed, " start"))
			if !starts {
				continue
			}
			if !strings.Contains(trimmed, "9>&-") {
				t.Fatalf("%s starts a service without closing the core lifecycle descriptor: %s", name, trimmed)
			}
		}
	}
}

// tcpTuningHarness renders the tuning helpers against a fake /proc/sys tree so
// the best-effort behaviour can be exercised without touching the host kernel.
func tcpTuningHarness(t *testing.T, script, procDir, configPath string, extra ...string) string {
	t.Helper()
	lines := []string{
		"set -eu",
		"INSTALL_TCP_TUNING=1",
		"TCP_TUNING_PROC_DIR=" + shellQuote(procDir),
		"TCP_TUNING_CONFIG_PATH=" + shellQuote(configPath),
		extractShellFunction(t, script, "tcp_tuning_requested"),
		extractShellFunction(t, script, "tcp_tuning_parameters"),
		extractShellFunction(t, script, "tcp_tuning_normalize"),
		extractShellFunction(t, script, "tcp_tuning_apply_key"),
		extractShellFunction(t, script, "persist_tcp_tuning"),
		extractShellFunction(t, script, "enable_tcp_tuning"),
		extractShellFunction(t, script, "try_enable_tcp_tuning"),
	}
	return strings.Join(append(lines, extra...), "\n")
}

func writeTestProcKey(t *testing.T, procDir, key, value string) string {
	t.Helper()
	path := filepath.Join(procDir, filepath.FromSlash(strings.ReplaceAll(key, ".", "/")))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, value)
	return path
}

func TestAgentInstallScriptTCPTuningSkipsUnsupportedKeys(t *testing.T) {
	script := testAgentInstallScript(t)
	shell := testPOSIXShell(t)
	root := t.TempDir()
	proc := filepath.Join(root, "proc-sys")
	config := filepath.Join(root, "sysctl.d", "99-oboard-tcp.conf")

	rmem := writeTestProcKey(t, proc, "net.ipv4.tcp_rmem", "4096\t131072\t6291456\n")
	forward := writeTestProcKey(t, proc, "net.ipv4.ip_forward", "0\n")
	v6forward := writeTestProcKey(t, proc, "net.ipv6.conf.all.forwarding", "0\n")
	// A key this kernel does not expose at all (every other key in the set is
	// absent from the fake tree) and a read-only knob (a restricted container)
	// must both be skipped instead of failing the run.
	readonly := writeTestProcKey(t, proc, "net.ipv4.tcp_ecn", "1\n")
	if err := os.Chmod(readonly, 0o400); err != nil {
		t.Fatal(err)
	}

	output, err := exec.Command(shell, "-c", tcpTuningHarness(t, script, proc, config, "enable_tcp_tuning")).CombinedOutput()
	if err != nil {
		t.Fatalf("enable_tcp_tuning failed: %v\n%s", err, output)
	}
	assertTestFile(t, rmem, "8192 174760 67108864\n")
	assertTestFile(t, forward, "1\n")
	assertTestFile(t, v6forward, "1\n")
	assertTestFile(t, readonly, "1\n")
	assertTestFile(t, config, "net.ipv4.tcp_rmem = 8192 174760 67108864\nnet.ipv4.ip_forward = 1\nnet.ipv6.conf.all.forwarding = 1\n")
	assertPathMode(t, config, 0o600)
	text := string(output)
	if !strings.Contains(text, "net.ipv4.tcp_mtu_probing") || !strings.Contains(text, "net.ipv4.tcp_ecn") {
		t.Fatalf("skipped keys are not reported:\n%s", output)
	}
	if !strings.Contains(text, "已应用 3 项参数") {
		t.Fatalf("unexpected tuning summary:\n%s", output)
	}
}

func TestAgentInstallScriptContinuesWhenTCPTuningIsUnavailable(t *testing.T) {
	script := testAgentInstallScript(t)
	shell := testPOSIXShell(t)
	root := t.TempDir()
	proc := filepath.Join(root, "proc-sys")
	if err := os.MkdirAll(proc, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "sysctl.d", "99-oboard-tcp.conf")

	harness := tcpTuningHarness(t, script, proc, config, "try_enable_tcp_tuning", "echo install-continued")
	output, err := exec.Command(shell, "-c", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("unavailable TCP tuning stopped installation: %v\n%s", err, output)
	}
	text := string(output)
	if !strings.Contains(text, "常见于受限容器") || !strings.Contains(text, "Agent 安装将继续") || !strings.Contains(text, "install-continued") {
		t.Fatalf("unexpected unavailable tuning output:\n%s", output)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("a sysctl file was written even though nothing applied: %v", err)
	}
}

func TestAgentInstallScriptTCPTuningIsInstallOnly(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, want := range []string{
		"OBOARD_INSTALL_TCP_TUNING",
		"INSTALL_TCP_TUNING=${OBOARD_INSTALL_TCP_TUNING:-0}",
		"net.ipv6.conf.all.forwarding=1",
		"net.ipv6.conf.default.forwarding=1",
		"net.ipv4.tcp_congestion_control=bbr",
		"net.ipv4.tcp_rmem=8192 174760 67108864",
		"net.ipv4.tcp_ecn=2",
		"/etc/sysctl.d/99-oboard-tcp.conf",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Agent installer is missing %q", want)
		}
	}
	installCase := shellCaseBranch(t, script, "install)", "update)")
	if strings.Count(installCase, "try_enable_tcp_tuning") != 2 {
		t.Fatal("both install branches must attempt the requested TCP tuning")
	}
	if strings.Index(installCase, "try_enable_tcp_tuning") > strings.Index(installCase, "-enroll-only") {
		t.Fatal("Agent installer consumes the enrollment token before attempting TCP tuning")
	}
	updateCase := shellCaseBranch(t, script, "update)", "uninstall)")
	if strings.Contains(updateCase, "try_enable_tcp_tuning") {
		t.Fatal("Agent update branch repeats TCP tuning")
	}
}

type timeSyncHarness struct {
	root, bin, state, openrc, status string
}

func newTimeSyncHarness(t *testing.T, capEff string) timeSyncHarness {
	t.Helper()
	root := t.TempDir()
	h := timeSyncHarness{
		root:   root,
		bin:    filepath.Join(root, "bin"),
		state:  filepath.Join(root, "state"),
		openrc: filepath.Join(root, "init.d"),
		status: filepath.Join(root, "status"),
	}
	for _, dir := range []string{h.bin, h.state, h.openrc} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, h.status, "Name:\tsh\nCapEff:\t"+capEff+"\n")
	state := shellQuote(h.state)
	writeExecutable(t, filepath.Join(h.bin, "systemctl"), `#!/bin/sh
echo "systemctl $*" >> `+state+`/calls
case "$1" in
  show) grep -qx "$5" `+state+`/loaded 2>/dev/null && echo loaded || echo not-found ;;
  is-active) grep -qx "$3" `+state+`/active 2>/dev/null ;;
  start) echo "$2" >> `+state+`/active ;;
esac
`)
	writeExecutable(t, filepath.Join(h.bin, "rc-service"), `#!/bin/sh
echo "rc-service $*" >> `+state+`/calls
case "$2" in
  status) grep -qx "$1" `+state+`/active 2>/dev/null ;;
  start) echo "$1" >> `+state+`/active ;;
esac
`)
	for _, name := range []string{"rc-update", "chronyc", "timedatectl"} {
		writeExecutable(t, filepath.Join(h.bin, name), "#!/bin/sh\necho \""+name+" $*\" >> "+state+"/calls\n")
	}
	return h
}

func (h timeSyncHarness) run(t *testing.T, script, manager, virt string, extra ...string) (string, string) {
	t.Helper()
	lines := []string{
		"set -eu",
		"PATH=" + shellQuote(h.bin) + ":$PATH",
		"INSTALL_TIME_SYNC=1",
		"SERVICE_MANAGER=" + manager,
		"VIRT_HINT=" + virt,
		"TIME_SYNC_STATUS_PATH=" + shellQuote(h.status),
		"TIME_SYNC_OPENRC_DIR=" + shellQuote(h.openrc),
	}
	for _, name := range []string{
		"time_sync_requested", "time_sync_in_container", "time_sync_has_clock_permission",
		"time_sync_can_install_packages", "time_sync_kind_label", "time_sync_service_name",
		"time_sync_service_active", "time_sync_service_enable", "time_sync_service_start",
		"time_sync_calibrate", "time_sync_start_installed", "enable_time_sync", "try_enable_time_sync",
	} {
		lines = append(lines, extractShellFunction(t, script, name))
	}
	lines = append(lines,
		"time_sync_can_install_packages() { return 0; }",
		"pkg_install() { echo \"pkg_install $*\" >> "+shellQuote(filepath.Join(h.state, "calls"))+"; }",
	)
	lines = append(lines, extra...)
	lines = append(lines, "try_enable_time_sync", "echo install-continued")
	output, err := exec.Command(testPOSIXShell(t), "-c", strings.Join(lines, "\n")).CombinedOutput()
	if err != nil {
		t.Fatalf("time sync stopped installation: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "install-continued") {
		t.Fatalf("installation did not continue:\n%s", output)
	}
	calls, _ := os.ReadFile(filepath.Join(h.state, "calls"))
	return string(output), string(calls)
}

const fullCapEff = "000001ffffffffff"

func TestAgentInstallScriptTimeSyncSkipsContainers(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, virt := range []string{"lxc", "lxc-libvirt", "openvz", "docker", "container"} {
		h := newTimeSyncHarness(t, fullCapEff)
		output, calls := h.run(t, script, "systemd", virt)
		if !strings.Contains(output, "容器环境") || calls != "" {
			t.Fatalf("%s: container was not skipped:\n%s\ncalls:\n%s", virt, output, calls)
		}
	}
}

func TestAgentInstallScriptTimeSyncRequiresClockPermission(t *testing.T) {
	script := testAgentInstallScript(t)
	// Every capability except CAP_SYS_TIME (bit 25).
	h := newTimeSyncHarness(t, "000001fffdffffff")
	output, calls := h.run(t, script, "systemd", "kvm")
	if !strings.Contains(output, "CAP_SYS_TIME") || !strings.Contains(output, "Agent 安装将继续") {
		t.Fatalf("missing permission was not reported:\n%s", output)
	}
	if calls != "" {
		t.Fatalf("time sync touched services without clock permission:\n%s", calls)
	}
}

func TestAgentInstallScriptTimeSyncKeepsRunningDaemon(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newTimeSyncHarness(t, fullCapEff)
	writeTestFile(t, filepath.Join(h.state, "loaded"), "systemd-timesyncd.service\nchrony.service\n")
	writeTestFile(t, filepath.Join(h.state, "active"), "systemd-timesyncd.service\n")
	output, calls := h.run(t, script, "systemd", "kvm")
	if !strings.Contains(output, "systemd-timesyncd 已在运行") {
		t.Fatalf("running daemon was not recognized:\n%s", output)
	}
	for _, want := range []string{"systemctl enable systemd-timesyncd.service", "timedatectl set-ntp true"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "pkg_install") || strings.Contains(calls, "systemctl start") {
		t.Fatalf("a running daemon must not be replaced or restarted:\n%s", calls)
	}
}

func TestAgentInstallScriptTimeSyncWarnsAboutCompetingDaemons(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newTimeSyncHarness(t, fullCapEff)
	writeTestFile(t, filepath.Join(h.state, "loaded"), "chrony.service\nchronyd.service\nntpsec.service\nntp.service\n")
	writeTestFile(t, filepath.Join(h.state, "active"), "chrony.service\nchronyd.service\nntpsec.service\nntp.service\n")
	output, calls := h.run(t, script, "systemd", "none")
	if !strings.Contains(output, "多个时间同步服务同时运行：chrony ntpd") {
		t.Fatalf("competing daemons were not reported once per kind:\n%s", output)
	}
	if strings.Contains(calls, "systemctl stop") || !strings.Contains(calls, "chronyc -a makestep") {
		t.Fatalf("unexpected service changes:\n%s", calls)
	}
}

func TestAgentInstallScriptTimeSyncStartsInstalledDaemon(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newTimeSyncHarness(t, fullCapEff)
	writeTestFile(t, filepath.Join(h.state, "loaded"), "chronyd.service\n")
	output, calls := h.run(t, script, "systemd", "kvm")
	for _, want := range []string{"systemctl enable chronyd.service", "systemctl start chronyd.service", "chronyc -a makestep"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "pkg_install") || !strings.Contains(output, "已启动 chrony") {
		t.Fatalf("installed daemon was not started:\n%s\n%s", output, calls)
	}
}

func TestAgentInstallScriptTimeSyncInstallsChronyWhenMissing(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newTimeSyncHarness(t, fullCapEff)
	installed := filepath.Join(h.openrc, "chronyd")
	output, calls := h.run(t, script, "openrc", "kvm",
		"pkg_install() { echo \"pkg_install $*\" >> "+shellQuote(filepath.Join(h.state, "calls"))+"; printf '#!/bin/sh\\n' > "+shellQuote(installed)+"; chmod 700 "+shellQuote(installed)+"; }")
	want := "pkg_install chrony\nrc-update add chronyd default\nrc-service chronyd start\nrc-service chronyd status\nchronyc -a makestep\n"
	if !strings.HasSuffix(calls, want) || !strings.Contains(output, "已启动 chrony") {
		t.Fatalf("chrony was not installed and started:\n%s\ncalls:\n%s", output, calls)
	}
}

func TestAgentInstallScriptTimeSyncRespectsReadOnlyHost(t *testing.T) {
	script := testAgentInstallScript(t)
	h := newTimeSyncHarness(t, fullCapEff)
	output, calls := h.run(t, script, "openrc", "kvm", "time_sync_can_install_packages() { return 1; }")
	if strings.Contains(calls, "pkg_install") || !strings.Contains(output, "不允许安装软件包") {
		t.Fatalf("packages were installed without permission:\n%s\n%s", output, calls)
	}
}

func TestAgentInstallScriptTimeSyncIsInstallOnly(t *testing.T) {
	script := testAgentInstallScript(t)
	if !strings.Contains(script, "INSTALL_TIME_SYNC=${OBOARD_INSTALL_TIME_SYNC:-1}") {
		t.Fatal("time sync must be on by default with an OBOARD_INSTALL_TIME_SYNC opt-out")
	}
	installCase := shellCaseBranch(t, script, "install)", "update)")
	if strings.Count(installCase, "try_enable_time_sync") != 2 {
		t.Fatal("both install branches must attempt time sync")
	}
	if strings.Index(installCase, "try_enable_time_sync") > strings.Index(installCase, "-enroll-only") {
		t.Fatal("Agent installer consumes the enrollment token before configuring time sync")
	}
	if strings.Contains(shellCaseBranch(t, script, "update)", "uninstall)"), "try_enable_time_sync") {
		t.Fatal("Agent update branch repeats time sync setup")
	}
}
