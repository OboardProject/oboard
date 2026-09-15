package controller

import (
	"context"
	"encoding/json"
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
	if !queued {
		t.Fatal("switch change must queue the task")
	}
	if task.Type != model.AgentTaskTypeApplyStealth {
		t.Fatalf("task type = %q", task.Type)
	}
	var payload model.ApplyStealthTaskPayload
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Enable {
		t.Fatalf("payload = %+v", payload)
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
	// An opposite toggle queues behind the pending task instead of being
	// swallowed: the Agent serializes and applies the newest state last.
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
	updated.StealthEnabled = true
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
	enabled := agentInstallCommand("https://panel.example.com", agentInstallBBRValue(true), agentInstallStealthValue(true))
	if !strings.Contains(enabled, "OBOARD_INSTALL_STEALTH='1'") {
		t.Fatalf("install command missing stealth switch: %s", enabled)
	}
	disabled := agentInstallCommand("https://panel.example.com/", agentInstallBBRValue(false), agentInstallStealthValue(false))
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
