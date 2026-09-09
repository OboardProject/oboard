package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/logging"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

const (
	controllerUpdateDiagnosticsLogLines = 300
	controllerUpdateDiagnosticsMaxBytes = 96 << 10
	// The window tail is what makes a failed activation readable: when a new
	// build starts and dies, its own startup output lands in the same Controller
	// log file and carries none of the update markers below. It is selected by
	// timestamp rather than by line count, because a Controller that logs every
	// request produces hundreds of lines per minute and a fixed tail never
	// reaches back to an update that happened even a few minutes ago.
	controllerUpdateDiagnosticsWindowLines    = 400
	controllerUpdateDiagnosticsWindowMaxBytes = 64 << 10
	controllerUpdateDiagnosticsWindowMargin   = 2 * time.Minute
	controllerUpdateDiagnosticsWindowFallback = 20 * time.Minute
	// Access logs never explain why a build failed to start and would otherwise
	// fill the entire window on a busy panel.
	controllerUpdateDiagnosticsRequestPrefix = "http method="
	controllerUpdateLogTimeLayout            = "2006/01/02 15:04:05.000000"
)

// controllerUpdateLogMarkers selects the Controller log lines that belong to
// the update path. Matching is case-insensitive and the log manager already
// redacts credentials when the line is written.
var controllerUpdateLogMarkers = []string{
	"controller update",
	"controller updater",
	"controller-update",
	"oboard-controller-updater",
	"update backup",
	"主控更新",
}

type controllerUpdateDiagnosticsView struct {
	GeneratedAt  string `json:"generated_at"`
	Outcome      string `json:"outcome"`
	Report       string `json:"report"`
	LogLines     int    `json:"log_lines"`
	LogAvailable bool   `json:"log_available"`
	LogHint      string `json:"log_hint,omitempty"`
}

func (s *Server) controllerUpdateDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	write(w, 200, s.buildControllerUpdateDiagnostics(r.Context(), true))
}

// buildControllerUpdateDiagnostics renders the report an administrator copies
// out of the panel. localDetails keeps the same boundary the automation status
// view uses: MCP never receives the local backup path or a manual shell command.
func (s *Server) buildControllerUpdateDiagnostics(ctx context.Context, localDetails bool) controllerUpdateDiagnosticsView {
	now := time.Now().UTC()
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	status, statusErr := s.controllerUpdater.Status(statusCtx)
	cancel()
	if statusErr != nil {
		status = s.fallbackControllerUpdateStatus()
	}
	if strings.TrimSpace(status.LastError) == "" {
		if settings, err := s.store.ListSettings(ctx); err == nil {
			status.LastError = settings[controllerUpdateErrorSetting]
		}
	}
	run, runErr := s.store.LatestControllerUpdateRun(ctx)
	if runErr != nil {
		run = nil
	}
	logs, logErr := s.controllerUpdateLogTail(controllerUpdateDiagnosticsWindow(run, now))
	view := controllerUpdateDiagnosticsView{
		GeneratedAt:  now.Format(time.RFC3339),
		Outcome:      controllerUpdateDiagnosticsOutcome(status, run),
		LogLines:     logs.UpdateLineCount + logs.WindowLineCount,
		LogAvailable: logErr == nil && (logs.UpdateLineCount > 0 || logs.WindowLineCount > 0),
	}
	if logErr != nil {
		view.LogHint = "主控运行日志不可读取：" + logErr.Error()
	} else if logs.UpdateLineCount == 0 && logs.WindowLineCount == 0 {
		view.LogHint = "主控运行日志为空或已轮转。若主控以 OBOARD_LOG_OUTPUT=stdout 运行，日志只在 journalctl 中。"
	} else if logs.WindowLineCount == 0 {
		view.LogHint = "更新时间段内没有留下主控日志，通常说明新版本还没开始写日志就退出了；请查看 journalctl -u oboard-controller。"
	}
	view.LogHint = logging.Redact(view.LogHint)
	view.Report = controllerUpdateDiagnosticsReport(now, status, statusErr, run, logs, view.LogHint, localDetails)
	return view
}

// controllerUpdateLogTailContent carries both views of the Controller log. The
// marker-filtered part reaches back across rotations for the update itself; the
// window part is the only place a failed new build's own startup error appears,
// because it never mentions the update.
type controllerUpdateLogTailContent struct {
	Update          string
	UpdateLineCount int
	Window          string
	WindowLineCount int
	WindowFrom      time.Time
	WindowTo        time.Time
}

// controllerUpdateDiagnosticsWindow brackets the run so the log selection
// reaches the activation attempt instead of whatever happened most recently.
func controllerUpdateDiagnosticsWindow(run *store.ControllerUpdateRun, now time.Time) (time.Time, time.Time) {
	if run == nil || run.StartedAt.IsZero() {
		return now.Add(-controllerUpdateDiagnosticsWindowFallback), now
	}
	from := run.StartedAt.UTC().Add(-controllerUpdateDiagnosticsWindowMargin)
	to := now
	if run.FinishedAt != nil && !run.FinishedAt.IsZero() {
		to = run.FinishedAt.UTC()
	} else if !run.UpdatedAt.IsZero() && run.UpdatedAt.After(run.StartedAt) {
		to = run.UpdatedAt.UTC()
	}
	to = to.Add(controllerUpdateDiagnosticsWindowMargin)
	if to.After(now) {
		to = now
	}
	if !to.After(from) {
		to = from.Add(controllerUpdateDiagnosticsWindowMargin)
	}
	return from, to
}

func (s *Server) controllerUpdateLogTail(from, to time.Time) (controllerUpdateLogTailContent, error) {
	content := controllerUpdateLogTailContent{WindowFrom: from, WindowTo: to}
	if s.logs == nil {
		return content, errors.New("主控日志存储未启用")
	}
	updates, err := s.logs.SnapshotMatching(controllerUpdateDiagnosticsLogLines, controllerUpdateLogMarkers)
	if err != nil {
		return content, err
	}
	content.Update = controllerUpdateTrimLogHead(updates.Content, controllerUpdateDiagnosticsMaxBytes)
	content.UpdateLineCount = updates.LineCount
	window, err := s.logs.SnapshotSelect(controllerUpdateDiagnosticsWindowLines, func(line string) bool {
		return controllerUpdateKeepWindowLine(line, from, to)
	})
	if err != nil {
		return content, err
	}
	content.Window = controllerUpdateTrimLogHead(window.Content, controllerUpdateDiagnosticsWindowMaxBytes)
	content.WindowLineCount = window.LineCount
	return content, nil
}

// controllerUpdateKeepWindowLine keeps a line inside the update window that is
// not a routine access log. A line without a parsable timestamp is a
// continuation (a panic stack, for example) and is kept: dropping it would
// silently remove the most useful evidence a crashing build leaves behind.
func controllerUpdateKeepWindowLine(line string, from, to time.Time) bool {
	if strings.Contains(line, controllerUpdateDiagnosticsRequestPrefix) {
		return false
	}
	stamp, ok := controllerUpdateLogLineTime(line)
	if !ok {
		return true
	}
	return !stamp.Before(from) && !stamp.After(to)
}

func controllerUpdateLogLineTime(line string) (time.Time, bool) {
	if len(line) < len(controllerUpdateLogTimeLayout) {
		return time.Time{}, false
	}
	stamp, err := time.Parse(controllerUpdateLogTimeLayout, line[:len(controllerUpdateLogTimeLayout)])
	if err != nil {
		return time.Time{}, false
	}
	return stamp.UTC(), true
}

func controllerUpdateTrimLogHead(content string, maxBytes int) string {
	if len(content) <= maxBytes {
		return content
	}
	content = content[len(content)-maxBytes:]
	if index := strings.IndexByte(content, '\n'); index >= 0 {
		content = content[index+1:]
	}
	return "（较早的日志已省略）\n" + content
}

func controllerUpdateDiagnosticsOutcome(status controllerupdate.Status, run *store.ControllerUpdateRun) string {
	if run != nil {
		switch run.Phase {
		case store.ControllerUpdatePhaseFailed:
			return "failed"
		case store.ControllerUpdatePhaseCancelled:
			return "cancelled"
		case store.ControllerUpdatePhaseSucceeded:
			return "succeeded"
		case "":
		default:
			return "running"
		}
	}
	switch status.State {
	case "failed", "unavailable":
		return "failed"
	case "cancelled":
		return "cancelled"
	case "installed":
		return "succeeded"
	}
	if isActiveControllerUpdateStatus(status.State) {
		return "running"
	}
	return "unknown"
}

func controllerUpdateDiagnosticsReport(now time.Time, status controllerupdate.Status, statusErr error, run *store.ControllerUpdateRun, logs controllerUpdateLogTailContent, logHint string, localDetails bool) string {
	var b strings.Builder
	b.WriteString("OBoard 主控更新诊断\n")
	b.WriteString("生成时间: " + now.Format(time.RFC3339) + "\n")
	b.WriteString("当前版本: " + controllerUpdateBuildLabel(version.Version, version.Build, version.Commit) + "\n")
	b.WriteString("更新通道: " + controllerUpdateDiagnosticsValue(status.Channel) + "\n")

	b.WriteString("\n[更新器]\n")
	if statusErr != nil {
		b.WriteString("读取失败: " + statusErr.Error() + "\n")
	}
	b.WriteString("状态: " + controllerUpdateDiagnosticsValue(status.State) + "\n")
	b.WriteString("可用版本: " + controllerUpdateBuildLabel(status.Available.Version, status.Available.Build, status.Available.Commit) + "\n")
	if trimmed := strings.TrimSpace(status.LastCheckedAt); trimmed != "" {
		b.WriteString("最近检查: " + trimmed + "\n")
	}
	if trimmed := strings.TrimSpace(status.LastError); trimmed != "" {
		b.WriteString("最近错误: " + trimmed + "\n")
	}
	if trimmed := strings.TrimSpace(status.ManualCommand); trimmed != "" && localDetails {
		b.WriteString("手动命令: " + trimmed + "\n")
	}

	b.WriteString("\n[更新任务]\n")
	if run == nil {
		b.WriteString("没有更新任务记录。\n")
	} else {
		b.WriteString(fmt.Sprintf("任务 ID: %d\n", run.ID))
		b.WriteString("触发来源: " + controllerUpdateDiagnosticsValue(run.Source) + "\n")
		b.WriteString("当前构建: " + controllerUpdateBuildLabel(run.CurrentVersion, run.CurrentBuild, "") + "\n")
		b.WriteString("目标构建: " + controllerUpdateBuildLabel(run.TargetVersion, run.TargetBuild, "") + "\n")
		b.WriteString("阶段: " + controllerUpdateDiagnosticsValue(run.Phase) + "\n")
		b.WriteString("开始时间: " + controllerUpdateDiagnosticsTime(run.StartedAt) + "\n")
		b.WriteString("最后更新: " + controllerUpdateDiagnosticsTime(run.UpdatedAt) + "\n")
		if run.FinishedAt != nil {
			b.WriteString("结束时间: " + controllerUpdateDiagnosticsTime(*run.FinishedAt) + "\n")
		} else {
			b.WriteString("结束时间: 未结束\n")
		}
		if reference := controllerUpdateRunReferenceTime(run); !reference.IsZero() && !run.StartedAt.IsZero() {
			b.WriteString("已经历: " + controllerUpdateDiagnosticsDuration(reference.Sub(run.StartedAt).Milliseconds()) + "\n")
		}
		b.WriteString("阶段耗时: 下载 " + controllerUpdateDiagnosticsDuration(run.DownloadDurationMS) +
			" · 备份 " + controllerUpdateDiagnosticsDuration(run.BackupDurationMS) +
			" · 安装 " + controllerUpdateDiagnosticsDuration(run.InstallDurationMS) +
			" · 重启 " + controllerUpdateDiagnosticsDuration(run.RestartDurationMS) +
			" · 合计 " + controllerUpdateDiagnosticsDuration(run.TotalDurationMS) + "\n")
		if trimmed := strings.TrimSpace(run.BackupPath); trimmed != "" {
			label := trimmed
			if !localDetails {
				label = "已创建"
			}
			b.WriteString("数据库备份: " + label + controllerUpdateDiagnosticsSize(run.BackupSizeBytes) + "\n")
		} else {
			b.WriteString("数据库备份: 未创建\n")
		}
		if trimmed := strings.TrimSpace(run.Error); trimmed != "" {
			b.WriteString("错误: " + trimmed + "\n")
		}
	}

	b.WriteString("\n[主控更新日志]\n")
	if trimmed := strings.TrimSpace(logs.Update); trimmed != "" {
		b.WriteString(strings.TrimRight(logs.Update, "\n") + "\n")
	} else {
		b.WriteString(controllerUpdateDiagnosticsValue(logHint) + "\n")
	}

	// A rollback after "主控未恢复可用" means the new build started and died, or
	// never started. Neither writes an update marker, so this window is the only
	// place that evidence survives inside the panel.
	b.WriteString("\n[更新时间段内的主控日志（已排除请求访问日志）]\n")
	if !logs.WindowFrom.IsZero() && !logs.WindowTo.IsZero() {
		b.WriteString("时间范围: " + logs.WindowFrom.Format(time.RFC3339) + " ~ " + logs.WindowTo.Format(time.RFC3339) + "\n")
	}
	if trimmed := strings.TrimSpace(logs.Window); trimmed != "" {
		b.WriteString(strings.TrimRight(logs.Window, "\n") + "\n")
	} else {
		b.WriteString(controllerUpdateDiagnosticsValue(logHint) + "\n")
	}

	b.WriteString("\n[进一步排查]\n")
	b.WriteString("更新器自身的日志不在这里：journalctl -u oboard-controller-updater -n 200 --no-pager（OpenRC 主机查看 /var/log/oboard-controller-updater.log）\n")
	b.WriteString("新版本启动失败时看这里: journalctl -u oboard-controller -n 200 --no-pager\n")
	b.WriteString("完整面板日志: 设置 → 日志 → 下载\n")
	return logging.Redact(b.String())
}

func controllerUpdateRunReferenceTime(run *store.ControllerUpdateRun) time.Time {
	if run.FinishedAt != nil {
		return *run.FinishedAt
	}
	if !run.UpdatedAt.IsZero() {
		return run.UpdatedAt
	}
	return time.Time{}
}

func controllerUpdateBuildLabel(versionValue, build, commit string) string {
	parts := make([]string, 0, 3)
	if trimmed := strings.TrimSpace(versionValue); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if trimmed := strings.TrimSpace(build); trimmed != "" {
		parts = append(parts, "构建 "+trimmed)
	}
	if trimmed := strings.TrimSpace(commit); trimmed != "" {
		parts = append(parts, "提交 "+trimmed)
	}
	if len(parts) == 0 {
		return "未知"
	}
	return strings.Join(parts, " · ")
}

func controllerUpdateDiagnosticsValue(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return "未知"
}

func controllerUpdateDiagnosticsTime(value time.Time) string {
	if value.IsZero() {
		return "未记录"
	}
	return value.UTC().Format(time.RFC3339)
}

func controllerUpdateDiagnosticsDuration(milliseconds int64) string {
	if milliseconds <= 0 {
		return "—"
	}
	return (time.Duration(milliseconds) * time.Millisecond).Round(time.Millisecond).String()
}

func controllerUpdateDiagnosticsSize(sizeBytes int64) string {
	if sizeBytes <= 0 {
		return ""
	}
	return fmt.Sprintf("（%.1f MB）", float64(sizeBytes)/(1024*1024))
}
