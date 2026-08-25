package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestAgentInstallScriptBBRIsInstallOnly(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, want := range []string{
		"OBOARD_INSTALL_BBR",
		"enable_bbr_fq",
		"try_enable_bbr_fq",
		"net.core.default_qdisc = fq",
		"net.ipv4.tcp_congestion_control = bbr",
		"安装程序不会自动更换内核",
		"[1/4] 检查运行环境",
		"[2/4] 下载 Agent 组件",
		"[3/4] 校验并安装组件",
		"[4/4] 注册并启动 Agent 服务",
		"详细日志：$INSTALL_LOG",
		"管理 Agent：$management_command",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Agent installer is missing %q", want)
		}
	}
	installCase := shellCaseBranch(t, script, "install)", "update)")
	if !strings.Contains(installCase, "try_enable_bbr_fq") {
		t.Fatal("Agent install branch does not enable requested BBR + FQ")
	}
	if strings.Index(installCase, "try_enable_bbr_fq") > strings.Index(installCase, "-enroll-only") {
		t.Fatal("Agent installer consumes the enrollment token before attempting BBR + FQ")
	}
	updateCase := shellCaseBranch(t, script, "update)", "uninstall)")
	if strings.Contains(updateCase, "try_enable_bbr_fq") {
		t.Fatal("Agent update branch repeats BBR + FQ configuration")
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
printf '%s\n' "$*" > "$CURL_LOG"
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

func TestAgentInstallScriptContinuesWhenBBRFQIsUnavailable(t *testing.T) {
	script := testAgentInstallScript(t)
	shell := testPOSIXShell(t)
	root := t.TempDir()
	available := filepath.Join(root, "available")
	writeTestFile(t, available, "reno cubic bbr\n")

	harness := strings.Join([]string{
		"set -eu",
		"INSTALL_BBR=1",
		"BBR_AVAILABLE_PATH=" + shellQuote(available),
		"BBR_CONGESTION_PATH=" + shellQuote(filepath.Join(root, "missing-congestion")),
		"BBR_QDISC_PATH=" + shellQuote(filepath.Join(root, "missing-qdisc")),
		"BBR_CONFIG_PATH=" + shellQuote(filepath.Join(root, "99-oboard-bbr.conf")),
		extractShellFunction(t, script, "bbr_requested"),
		extractShellFunction(t, script, "bbr_available"),
		extractShellFunction(t, script, "persist_bbr_fq"),
		extractShellFunction(t, script, "enable_bbr_fq"),
		extractShellFunction(t, script, "try_enable_bbr_fq"),
		"try_enable_bbr_fq",
		"echo install-continued",
	}, "\n")
	output, err := exec.Command(shell, "-c", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("unavailable BBR stopped installation: %v\n%s", err, output)
	}
	text := string(output)
	if !strings.Contains(text, "常见于受限容器") || !strings.Contains(text, "Agent 安装将继续") || !strings.Contains(text, "install-continued") {
		t.Fatalf("unexpected unavailable BBR output:\n%s", output)
	}
}

func TestAgentInstallScriptEnablesAndPersistsBBRFQ(t *testing.T) {
	script := testAgentInstallScript(t)
	shell := testPOSIXShell(t)
	root := t.TempDir()
	available := filepath.Join(root, "available")
	congestion := filepath.Join(root, "congestion")
	qdisc := filepath.Join(root, "qdisc")
	config := filepath.Join(root, "sysctl.d", "99-oboard-bbr.conf")
	writeTestFile(t, available, "reno cubic bbr\n")
	writeTestFile(t, congestion, "cubic\n")
	writeTestFile(t, qdisc, "fq_codel\n")

	harness := strings.Join([]string{
		"set -eu",
		"INSTALL_BBR=1",
		"BBR_AVAILABLE_PATH=" + shellQuote(available),
		"BBR_CONGESTION_PATH=" + shellQuote(congestion),
		"BBR_QDISC_PATH=" + shellQuote(qdisc),
		"BBR_CONFIG_PATH=" + shellQuote(config),
		extractShellFunction(t, script, "bbr_requested"),
		extractShellFunction(t, script, "bbr_available"),
		extractShellFunction(t, script, "persist_bbr_fq"),
		extractShellFunction(t, script, "enable_bbr_fq"),
		"enable_bbr_fq",
	}, "\n")
	if output, err := exec.Command(shell, "-c", harness).CombinedOutput(); err != nil {
		t.Fatalf("enable BBR + FQ failed: %v\n%s", err, output)
	}
	assertTestFile(t, congestion, "bbr\n")
	assertTestFile(t, qdisc, "fq\n")
	assertTestFile(t, config, "net.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n")
	assertPathMode(t, config, 0o600)
}

func TestAgentInstallScriptRollsBackBBRWhenPersistenceFails(t *testing.T) {
	script := testAgentInstallScript(t)
	shell := testPOSIXShell(t)
	root := t.TempDir()
	available := filepath.Join(root, "available")
	congestion := filepath.Join(root, "congestion")
	qdisc := filepath.Join(root, "qdisc")
	config := filepath.Join(root, "invalid-config")
	writeTestFile(t, available, "reno cubic bbr\n")
	writeTestFile(t, congestion, "cubic\n")
	writeTestFile(t, qdisc, "fq_codel\n")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}

	harness := strings.Join([]string{
		"set -eu",
		"INSTALL_BBR=1",
		"BBR_AVAILABLE_PATH=" + shellQuote(available),
		"BBR_CONGESTION_PATH=" + shellQuote(congestion),
		"BBR_QDISC_PATH=" + shellQuote(qdisc),
		"BBR_CONFIG_PATH=" + shellQuote(config),
		extractShellFunction(t, script, "bbr_requested"),
		extractShellFunction(t, script, "bbr_available"),
		extractShellFunction(t, script, "persist_bbr_fq"),
		extractShellFunction(t, script, "enable_bbr_fq"),
		"enable_bbr_fq",
	}, "\n")
	if output, err := exec.Command(shell, "-c", harness).CombinedOutput(); err == nil {
		t.Fatalf("invalid BBR persistence path was accepted\n%s", output)
	}
	assertTestFile(t, congestion, "cubic\n")
	assertTestFile(t, qdisc, "fq_codel\n")
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
