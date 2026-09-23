package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestStealthSwitchToggleQueuesApplyStealthTask(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "stealth-secret", "")
	server := &model.Server{
		Name: "stealth-server", PublicIPv4: "203.0.113.31", AgentID: "stealth-agent",
		AgentTokenHash: security.HashSecret("stealth-token"), Status: model.ServerOnline,
		KernelCapabilities: []string{model.AgentCapabilityStealth},
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	updated := *server
	updated.StealthEnabled = true
	task, queued, err := srv.maybeQueueStealthSwitch(ctx, *server, updated)
	if err != nil {
		t.Fatalf("queue apply_stealth: %v", err)
	}
	if queued || task.ID != 0 {
		t.Fatal("enabling must require reinstall instead of an in-place task")
	}
	// A same-direction toggle while the task is pending dedups to it.
	sameAgain := updated
	again, queuedAgain, err := srv.maybeQueueStealthSwitch(ctx, updated, sameAgain)
	if err != nil {
		t.Fatalf("same-direction toggle must not error: %v", err)
	}
	if queuedAgain {
		t.Fatal("unchanged switch must not queue a task")
	}
	_ = again
	// Disabling still uses the signed task lane.
	reverted := updated
	reverted.StealthEnabled = false
	opposite, queuedOpposite, err := srv.maybeQueueStealthSwitch(ctx, updated, reverted)
	if err != nil {
		t.Fatalf("opposite toggle: %v", err)
	}
	if !queuedOpposite || opposite.ID == task.ID {
		t.Fatalf("opposite toggle = %+v queued=%v", opposite, queuedOpposite)
	}
	var oppositePayload model.ApplyStealthTaskPayload
	if err := json.Unmarshal([]byte(opposite.PayloadJSON), &oppositePayload); err != nil || oppositePayload.Enable {
		t.Fatalf("opposite payload = %+v %v", oppositePayload, err)
	}
	// No change means no task.
	noTask, queued, err := srv.maybeQueueStealthSwitch(ctx, *server, *server)
	if err != nil || queued || noTask.ID != 0 {
		t.Fatalf("unchanged switch queued a task: %+v %v %v", noTask, queued, err)
	}
	// Offline servers are skipped: the switch saves and shapes the install
	// command only.
	offline := *server
	offline.Status = model.ServerOffline
	offline.StealthEnabled = true
	if _, queued, err := srv.maybeQueueStealthSwitch(ctx, *server, offline); err != nil || queued {
		t.Fatalf("offline switch queued a task: %v %v", queued, err)
	}
}

func TestStealthSwitchRequiresAgentCapability(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "stealth-old-secret", "")
	server := &model.Server{
		Name: "stealth-old", PublicIPv4: "203.0.113.32", AgentID: "stealth-old-agent",
		AgentTokenHash: security.HashSecret("stealth-old-token"), Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	updated := *server
	server.StealthEnabled = true
	updated.StealthEnabled = false
	if _, _, err := srv.maybeQueueStealthSwitch(ctx, *server, updated); err == nil || !strings.Contains(err.Error(), "stealth_v1") {
		t.Fatalf("old agent must be rejected with an upgrade hint: %v", err)
	}
}

func TestServerPatchStealthRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "stealth-patch", PublicIPv4: "203.0.113.33"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	// Toggle through the store update path the PATCH handler uses.
	updated := *server
	updated.StealthEnabled = true
	if err := db.UpdateServerSettings(ctx, &updated, store.ServerUpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.StealthEnabled {
		t.Fatal("stealth_enabled did not persist")
	}
	updated.StealthEnabled = false
	if err := db.UpdateServerSettings(ctx, &updated, store.ServerUpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if reloaded, err = db.GetServer(ctx, server.ID); err != nil || reloaded.StealthEnabled {
		t.Fatalf("stealth_enabled disable round trip: %+v %v", reloaded, err)
	}
}

func TestAgentInstallCommandCarriesStealthSwitch(t *testing.T) {
	enabled := agentInstallCommand("https://panel.example.com", agentInstallBBRValue(true), agentInstallTCPTuningValue(false), agentInstallStealthValue(true))
	if !strings.Contains(enabled, "OBOARD_INSTALL_STEALTH='1'") {
		t.Fatalf("install command missing stealth switch: %s", enabled)
	}
	disabled := agentInstallCommand("https://panel.example.com/", agentInstallBBRValue(false), agentInstallTCPTuningValue(false), agentInstallStealthValue(false))
	if !strings.Contains(disabled, "OBOARD_INSTALL_STEALTH='0'") {
		t.Fatalf("install command missing explicit stealth off: %s", disabled)
	}
	if strings.Contains(disabled, "${OBOARD_INSTALL_STEALTH") {
		t.Fatal("install command must never carry the literal default expression")
	}
}

func TestAgentInstallScriptStealthBranch(t *testing.T) {
	script := testAgentInstallScript(t)
	for _, want := range []string{
		"STEALTH_MODE=${OBOARD_INSTALL_STEALTH:-0}",
		"-stealth-bootstrap",
		"STEALTH_AGENT_BIN",
		"STEALTH_CONFIG_PATH",
		"STEALTH_KEY_PATH",
		"STEALTH_AGENT_SERVICE",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("installer script missing %q", want)
		}
	}
	installBranch := shellCaseBranch(t, script, "install)", "update)")
	if !strings.Contains(installBranch, `if [ "$STEALTH_MODE" = 1 ]; then`) {
		t.Fatal("install branch must gate the stealth bootstrap on STEALTH_MODE")
	}
	stealthGate := strings.Index(installBranch, `if [ "$STEALTH_MODE" = 1 ]; then`)
	if stealthGate < 0 || strings.Contains(installBranch[:stealthGate], "download_binaries") {
		t.Fatal("stealth install must skip the initial standard download and prefer GitHub")
	}
	if !strings.Contains(installBranch, `eval "$stealth_env"`) {
		t.Fatal("stealth branch must consume the bootstrap variables")
	}
	updateBranch := shellCaseBranch(t, script, "update)", "uninstall)")
	if !strings.Contains(updateBranch, `if [ "$STEALTH_MODE" = 1 ]; then`) {
		t.Fatal("update branch must refuse stealth installs")
	}
	if !strings.Contains(script, "此服务器已启用安全进程布局，命令行脚本无法定位随机化的安装") {
		t.Fatal("uninstall must refuse stealth installs with guidance")
	}
}

func TestAgentCommandLineUpdateRejectsMissingStandardInstall(t *testing.T) {
	script := testAgentInstallScript(t)
	branch := shellCaseBranch(t, script, "update)", "uninstall)")
	start := strings.Index(branch, "\n    if [ \"$STEALTH_MODE\" = 1 ]; then")
	end := strings.Index(branch, "\n    need_base_url")
	if start < 0 || end <= start {
		t.Fatal("update preflight is missing")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cmd := exec.Command(testPOSIXShell(t), "-c", "set -eu\nSTEALTH_MODE=0\nCONFIG_PATH=\"$TEST_CONFIG\"\nINSTALL_DIR=\"$TEST_INSTALL\"\n"+branch[start:end]+"\necho update-accepted")
	cmd.Env = append(os.Environ(), "TEST_CONFIG="+configPath, "TEST_INSTALL="+dir)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "未找到普通 Agent") || strings.Contains(string(output), "update-accepted") {
		t.Fatalf("missing standard install was accepted: %v\n%s", err, output)
	}
}

// GitHub's releases/latest endpoint excludes prereleases, so a dev-only
// repository answers 404 there. The stealth branch must derive the release
// tag from the panel-resolved target version first and keep a prerelease
// API fallback.
func TestStealthInstallResolvesReleaseTagFromTargetVersion(t *testing.T) {
	script := testAgentInstallScript(t)
	shell := testPOSIXShell(t)
	harness := strings.Join([]string{
		"set -eu",
		extractShellFunction(t, script, "stealth_target_release_tag"),
		`tag=$(stealth_target_release_tag "dev-82e772e0b212" "20260915045410")`,
		`[ "$tag" = "dev-82e772e0b212-20260915045410" ]`,
		`tag=$(stealth_target_release_tag "1.2.3" "20260915045410")`,
		`[ "$tag" = "v1.2.3" ]`,
		`tag=$(stealth_target_release_tag "v1.2.3" "20260915045410")`,
		`[ "$tag" = "v1.2.3" ]`,
		`if stealth_target_release_tag "dev-82e772e0b21" "20260915045410"; then exit 9; fi`,
		`if stealth_target_release_tag "dev-82e772e0b212" "202609150454"; then exit 9; fi`,
		`if stealth_target_release_tag "" ""; then exit 9; fi`,
		`if stealth_target_release_tag "garbage" ""; then exit 9; fi`,
		"echo tag-ok",
	}, "\n")
	if output, err := exec.Command(shell, "-c", harness).CombinedOutput(); err != nil {
		t.Fatalf("stealth tag derivation failed: %v\n%s", err, output)
	}

	installBranch := shellCaseBranch(t, script, "install)", "update)")
	stealthStart := strings.Index(installBranch, `if [ "$STEALTH_MODE" = 1 ]; then`)
	if stealthStart < 0 {
		t.Fatal("install branch must gate the stealth bootstrap on STEALTH_MODE")
	}
	stealthBranch := installBranch[stealthStart:]
	derived := strings.Index(stealthBranch, `stealth_target_release_tag "${TARGET_VERSION:-}" "${TARGET_BUILD:-}"`)
	apiFallback := strings.Index(stealthBranch, "api.github.com")
	if derived < 0 || apiFallback < 0 || derived > apiFallback {
		t.Fatal("stealth branch must derive the release tag from the panel-resolved target version before any GitHub API lookup")
	}
	if !strings.Contains(stealthBranch, "tags/dev") {
		t.Fatal("stealth branch must fall back to the mutable dev prerelease tag because releases/latest excludes prereleases")
	}
	ghDownload := strings.Index(stealthBranch, `download_component "Agent" "$gh_base/$agent_name"`)
	panelFallback := strings.Index(stealthBranch, `download_agent_component "Agent" "${BASE_URL}/downloads/$agent_name"`)
	if ghDownload < 0 || panelFallback < 0 || ghDownload > panelFallback {
		t.Fatal("stealth branch must download from GitHub first and fall back to the panel download only after GitHub transfers fail")
	}
	if !strings.Contains(stealthBranch, "GitHub 发布下载未完成，回落到主控下载") {
		t.Fatal("stealth branch must announce the panel fallback")
	}
	if !strings.Contains(stealthBranch, `rm -f "$tmp/$agent_name"`) {
		t.Fatal("stealth panel fallback must discard partial GitHub transfers before downloading from the panel")
	}
}

func TestPanelEnrollmentCommandIncludesStealthTransport(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	if err := db.SetSetting(ctx, "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	node := &model.Server{Name: "enrollment-stealth", StealthEnabled: true, BBREnabled: true}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/ui/servers/%d/enroll-token", node.ID)
	request(t, h, http.MethodPost, path, token, map[string]any{}, http.StatusBadRequest)
	stored, err := db.GetServer(ctx, node.ID)
	if err != nil || stored.EnrollmentHash != "" {
		t.Fatalf("failed command issued token: %v", err)
	}
	srv.stealthTransport.Store(&stealthTransport{addr: "transport.example.com:443", pin: "test-pin"})
	result := request(t, h, http.MethodPost, path, token, map[string]any{}, http.StatusOK)
	command := result["install_command"].(string)
	for _, want := range []string{
		"https://panel.example.com/install/agent.sh",
		"OBOARD_ENROLL_TOKEN=" + shellSingleQuote(result["enrollment_token"].(string)),
		"OBOARD_INSTALL_BBR='1'", "OBOARD_INSTALL_STEALTH='1'",
		"OBOARD_STEALTH_ADDR='transport.example.com:443'", "OBOARD_STEALTH_PIN='test-pin'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %s", want)
		}
	}
	plain, env, err := srv.agentEnrollmentCommand(ctx, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "OBOARD_STEALTH_ADDR") || env["OBOARD_STEALTH_PIN"] != nil || !strings.Contains(plain, "OBOARD_INSTALL_STEALTH='0'") {
		t.Fatal("plain command includes stealth transport")
	}
}

func TestStealthInstallerSkipsDirectoryPromptAndChecksStartup(t *testing.T) {
	script := testAgentInstallScript(t)
	start := strings.Index(script, "resolve_agent_install_dir() {")
	end := strings.Index(script[start:], "\npersist_agent_install_dir()") + start
	body := script[start:end]
	gate := strings.Index(body, `if [ "$ACTION" = install ] && [ "$STEALTH_MODE" = 1 ]; then`)
	prompt := strings.Index(body, "choose_install_dir")
	if gate < 0 || prompt < gate || !strings.Contains(body[gate:prompt], "return 0") {
		t.Fatal("stealth installation must bypass persisted paths and directory prompt")
	}
	branch := shellCaseBranch(t, script, "install)", "update)")
	enrolled := strings.Index(branch, "unset OBOARD_ENROLL_TOKEN")
	stable := strings.Index(branch, `wait_service_stable "$STEALTH_AGENT_SERVICE" 15`)
	cleanup := strings.Index(branch, "-cleanup-existing")
	success := strings.Index(branch, "安装完成：Agent 已重新安装")
	if enrolled < 0 || stable < enrolled || cleanup < stable || success < cleanup {
		t.Fatal("cleanup and success must follow enrollment and verified startup")
	}
}

func TestStealthInstallerDirectoryResolutionIgnoresOldInstallation(t *testing.T) {
	script := testAgentInstallScript(t)
	start := strings.Index(script, "resolve_agent_install_dir() {")
	end := strings.Index(script[start:], "\npersist_agent_install_dir()") + start
	command := script[start:end] + `
mkdir() { :; }
mktemp() { printf '/opt/.install.random\n'; }
configured_agent_install_dir() { echo 'unexpected old install lookup' >&2; exit 1; }
choose_install_dir() { echo 'unexpected prompt' >&2; exit 1; }
ACTION=install
STEALTH_MODE=1
INSTALL_DIR_INPUT=/old/install
resolve_agent_install_dir
[ "$INSTALL_DIR" = /opt/.install.random ]
`
	if out, err := exec.Command(testPOSIXShell(t), "-eu", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("stealth directory resolution: %v\n%s", err, out)
	}
}
