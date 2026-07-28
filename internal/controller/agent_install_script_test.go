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
