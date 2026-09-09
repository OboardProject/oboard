package controller

import (
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	oboardlog "github.com/OboardProject/oboard/internal/logging"
	"github.com/OboardProject/oboard/internal/store"
)

func TestControllerUpdateDiagnosticsReportCarriesFailureContext(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	finished := now.Add(-time.Minute)
	run := &store.ControllerUpdateRun{
		ID: 7, Source: "manual", TargetVersion: "v1.2.4", TargetBuild: "20260908120000",
		Phase: store.ControllerUpdatePhaseFailed, StartedAt: now.Add(-5 * time.Minute), UpdatedAt: finished,
		FinishedAt: &finished, DownloadDurationMS: 1500, InstallDurationMS: 4200,
		BackupPath: "/opt/oboard/data/backups/update.sqlite", BackupSizeBytes: 3 << 20,
		Error: "启动主控更新失败：磁盘空间不足",
	}
	status := controllerupdate.Status{
		Channel: "stable", State: "failed", LastError: "install: no space left on device",
		Available: controllerupdate.BuildInfo{Version: "v1.2.4", Build: "20260908120000"},
	}
	logs := controllerUpdateLogTailContent{
		Update: "2026/09/08 09:59:00 controller update start failed\n", UpdateLineCount: 1,
		Window: "2026/09/08 09:59:00 controller update start failed\n", WindowLineCount: 1,
		WindowFrom: now.Add(-7 * time.Minute), WindowTo: now,
	}
	report := controllerUpdateDiagnosticsReport(now, status, nil, run, logs, "", true)

	for _, want := range []string{
		"OBoard 主控更新诊断",
		"更新通道: stable",
		"install: no space left on device",
		"任务 ID: 7",
		"阶段: failed",
		"启动主控更新失败：磁盘空间不足",
		"/opt/oboard/data/backups/update.sqlite",
		"controller update start failed",
		"journalctl -u oboard-controller-updater",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report is missing %q:\n%s", want, report)
		}
	}
	if got := controllerUpdateDiagnosticsOutcome(status, run); got != "failed" {
		t.Fatalf("unexpected outcome: %s", got)
	}

	status.ManualCommand = "sed -i s/dev/stable/ /etc/oboard/controller.env"
	automation := controllerUpdateDiagnosticsReport(now, status, nil, run, controllerUpdateLogTailContent{}, "", false)
	if strings.Contains(automation, run.BackupPath) || strings.Contains(automation, status.ManualCommand) {
		t.Fatalf("automation report must not expose local paths or shell commands:\n%s", automation)
	}
	if !strings.Contains(automation, "数据库备份: 已创建") {
		t.Fatalf("automation report should still report that a backup exists:\n%s", automation)
	}
}

func TestControllerUpdateDiagnosticsReportWithoutRunAndLogs(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	status := controllerupdate.Status{Channel: "dev", State: "installing"}
	report := controllerUpdateDiagnosticsReport(now, status, nil, nil, controllerUpdateLogTailContent{}, "主控运行日志中没有本次更新的记录", true)
	if !strings.Contains(report, "没有更新任务记录") {
		t.Fatalf("report should state that no run exists:\n%s", report)
	}
	if !strings.Contains(report, "主控运行日志中没有本次更新的记录") {
		t.Fatalf("report should carry the log hint:\n%s", report)
	}
	if got := controllerUpdateDiagnosticsOutcome(status, nil); got != "running" {
		t.Fatalf("unexpected outcome: %s", got)
	}
}

// newDiagnosticsLogManager mirrors the Controller's own log flags so the window
// filter sees the same timestamp format it parses in production.
func newDiagnosticsLogManager(t *testing.T) *oboardlog.Manager {
	t.Helper()
	manager, err := oboardlog.New(filepath.Join(t.TempDir(), "controller.log"), oboardlog.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	previousOutput, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(manager)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	t.Cleanup(func() { log.SetOutput(previousOutput); log.SetFlags(previousFlags) })
	return manager
}

func TestControllerUpdateLogTailSeparatesUpdateLinesFromTheWindow(t *testing.T) {
	manager := newDiagnosticsLogManager(t)
	log.Printf("agent websocket connected server=3")
	log.Printf("controller update preflight failed: 磁盘空间不足")
	log.Printf("subscription rendered user=9")
	for range 350 {
		log.Printf("http method=GET path=/api/v1/ui/controller-update status=200")
	}
	log.Printf("Controller update start failed: 更新器不可用")

	now := time.Now().UTC()
	server := &Server{logs: manager}
	logs, err := server.controllerUpdateLogTail(now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if logs.UpdateLineCount != 2 {
		t.Fatalf("unexpected update line count %d: %s", logs.UpdateLineCount, logs.Update)
	}
	if strings.Contains(logs.Update, "agent websocket connected") || strings.Contains(logs.Update, "subscription rendered") {
		t.Fatalf("unrelated lines leaked into the update log: %s", logs.Update)
	}
	if logs.WindowLineCount != 4 || !strings.Contains(logs.Window, "agent websocket connected") {
		t.Fatalf("the window should keep every line it brackets: %d %s", logs.WindowLineCount, logs.Window)
	}
}

// A rollback after "主控未恢复可用" is the failure the marker filter cannot see:
// the new build's own startup error never mentions the update.
func TestControllerUpdateLogTailKeepsNewBuildStartupFailure(t *testing.T) {
	manager := newDiagnosticsLogManager(t)
	log.Printf("listen tcp :2787: bind: address already in use")

	now := time.Now().UTC()
	server := &Server{logs: manager}
	logs, err := server.controllerUpdateLogTail(now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if logs.UpdateLineCount != 0 {
		t.Fatalf("the startup failure carries no update marker: %s", logs.Update)
	}
	if !strings.Contains(logs.Window, "bind: address already in use") {
		t.Fatalf("the window must keep the startup failure: %s", logs.Window)
	}
}

// A busy Controller logs every request, so a fixed line count never reaches an
// update from minutes ago. The window must survive that noise and must not
// return lines from outside the update.
func TestControllerUpdateLogTailWindowSurvivesRequestNoise(t *testing.T) {
	manager := newDiagnosticsLogManager(t)
	log.Printf("controller update replacing program target_build=20260908120000")
	log.Printf("panic: migrate schema: database is locked")
	for i := 0; i < 600; i++ {
		log.Printf("http method=GET path=/api/v1/ui/servers status=200 bytes=735749 duration_ms=190 remote=203.0.113.7")
	}

	now := time.Now().UTC()
	server := &Server{logs: manager}
	logs, err := server.controllerUpdateLogTail(now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.Window, "panic: migrate schema") {
		t.Fatalf("600 access-log lines must not push out the startup failure: %s", logs.Window)
	}
	if strings.Contains(logs.Window, "http method=") {
		t.Fatalf("access logs must stay out of the window: %s", logs.Window)
	}

	outside, err := server.controllerUpdateLogTail(now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if outside.WindowLineCount != 0 {
		t.Fatalf("lines outside the window must not be returned: %s", outside.Window)
	}
}

func TestControllerUpdateDiagnosticsWindowBracketsTheRun(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	finished := now.Add(-3 * time.Minute)
	run := &store.ControllerUpdateRun{StartedAt: now.Add(-9 * time.Minute), UpdatedAt: finished, FinishedAt: &finished}
	from, to := controllerUpdateDiagnosticsWindow(run, now)
	if !from.Equal(run.StartedAt.Add(-controllerUpdateDiagnosticsWindowMargin)) {
		t.Fatalf("window should start before the run: %s", from)
	}
	if !to.Equal(finished.Add(controllerUpdateDiagnosticsWindowMargin)) {
		t.Fatalf("window should end after the run: %s", to)
	}

	// A run that is still going has no end yet, so the window runs to now.
	running := &store.ControllerUpdateRun{StartedAt: now.Add(-4 * time.Minute), UpdatedAt: now.Add(-time.Minute)}
	if _, openTo := controllerUpdateDiagnosticsWindow(running, now); openTo.After(now) {
		t.Fatalf("window must not extend past now: %s", openTo)
	}

	fallbackFrom, fallbackTo := controllerUpdateDiagnosticsWindow(nil, now)
	if !fallbackFrom.Equal(now.Add(-controllerUpdateDiagnosticsWindowFallback)) || !fallbackTo.Equal(now) {
		t.Fatalf("without a run the window falls back to a fixed span: %s ~ %s", fallbackFrom, fallbackTo)
	}
}

func TestControllerUpdateLogTailWithoutLogStorage(t *testing.T) {
	server := &Server{}
	now := time.Now().UTC()
	if _, err := server.controllerUpdateLogTail(now.Add(-time.Minute), now); err == nil {
		t.Fatal("expected an error when log storage is disabled")
	}
}

func TestControllerUpdateDiagnosticsRedactsPersistedErrors(t *testing.T) {
	secretURL := "https://api.telegram.org/bot123456:synthetic-diagnostic-token/getUpdates"
	status := controllerupdate.Status{State: "failed", LastError: secretURL}
	run := &store.ControllerUpdateRun{Phase: "failed", Error: secretURL}
	logs := controllerUpdateLogTailContent{Update: secretURL, Window: secretURL}
	for _, local := range []bool{true, false} {
		report := controllerUpdateDiagnosticsReport(time.Now(), status, nil, run, logs, "", local)
		if strings.Contains(report, "synthetic-diagnostic-token") {
			t.Fatalf("diagnostics leaked credential: %s", report)
		}
		if !strings.Contains(report, "getUpdates") {
			t.Fatal("missing request method")
		}
	}
}
