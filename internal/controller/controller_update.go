package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

const (
	controllerAutoUpdateSetting          = "controller_auto_update_enabled"
	controllerAutoUpdateIntervalSetting  = "controller_auto_update_interval_hours"
	agentAutoUpdateSetting               = "agent_auto_update_enabled"
	subscriptionRelayAutoUpdateSetting   = "subscription_relay_auto_update_enabled"
	updateWindowEnabledSetting           = "update_window_enabled"
	updateWindowStartHourSetting         = "update_window_start_hour"
	updateWindowEndHourSetting           = "update_window_end_hour"
	controllerBackupSetting              = "controller_update_backup_path"
	controllerBackupTargetBuildSetting   = "controller_update_backup_target_build"
	controllerUpdateErrorSetting         = "controller_update_controller_error"
	controllerUpdateMaintenanceSetting   = "controller_update_maintenance"
	controllerUpdateForceFinishPhrase    = "强制结束更新任务"
	controllerUpdateForceFinishedReason  = "管理员强制结束更新任务"
	controllerUpdateSchedulerPeriod      = time.Minute
	controllerUpdatePanelIdlePeriod      = 5 * time.Minute
	controllerUpdateInstallTimeout       = 20 * time.Minute
	controllerUpdateDefaultIntervalHours = 24
	updateWindowDefaultStartHour         = 3
	updateWindowDefaultEndHour           = 7
	controllerUpdateBackupRetentionSetting = "controller_update_backup_retention"
)

type controllerUpdateMaintenanceMarker struct {
	StartedAt   time.Time `json:"started_at"`
	TargetBuild string    `json:"target_build"`
}

func (s *Server) beginControllerUpdateMaintenance(ctx context.Context, targetBuild string) error {
	targetBuild = strings.TrimSpace(targetBuild)
	if targetBuild == "" {
		return errors.New("Controller update target build is required")
	}
	marker := controllerUpdateMaintenanceMarker{StartedAt: time.Now().UTC(), TargetBuild: targetBuild}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, controllerUpdateMaintenanceSetting, string(encoded)); err != nil {
		return err
	}
	s.controllerUpdateMaintenance.Store(true)
	return nil
}

func (s *Server) clearControllerUpdateMaintenance(ctx context.Context) {
	if err := s.store.SetSetting(ctx, controllerUpdateMaintenanceSetting, ""); err != nil {
		log.Printf("clear Controller update maintenance marker: %v", err)
	}
	s.controllerUpdateMaintenance.Store(false)
}

func (s *Server) installControllerUpdate(ctx context.Context, targetBuild string) (controllerupdate.Status, error) {
	if err := s.beginControllerUpdateMaintenance(ctx, targetBuild); err != nil {
		return controllerupdate.Status{}, fmt.Errorf("record Controller update maintenance: %w", err)
	}
	status, err := s.controllerUpdater.Install(ctx)
	if err != nil && status.State != "installing" {
		s.clearControllerUpdateMaintenance(ctx)
	}
	return status, err
}

func (s *Server) restoreControllerUpdateMaintenance(ctx context.Context) {
	raw, err := s.store.GetSetting(ctx, controllerUpdateMaintenanceSetting)
	if err != nil {
		log.Printf("read Controller update maintenance marker: %v", err)
		_ = s.store.CloseOpenControllerConnections(ctx, time.Now().UTC())
		return
	}
	now := time.Now().UTC()
	var marker controllerUpdateMaintenanceMarker
	decoded := json.Unmarshal([]byte(raw), &marker) == nil
	targetBuild := strings.TrimSpace(marker.TargetBuild)
	valid := strings.TrimSpace(raw) != "" && decoded && !marker.StartedAt.IsZero() && !marker.StartedAt.After(now.Add(2*time.Minute)) && marker.StartedAt.After(now.Add(-controllerUpdateInstallTimeout-10*time.Minute)) && targetBuild != "" && targetBuild == strings.TrimSpace(version.Build)
	if !valid {
		if strings.TrimSpace(raw) != "" {
			_ = s.store.SetSetting(ctx, controllerUpdateMaintenanceSetting, "")
		}
		_ = s.store.CloseOpenControllerConnections(ctx, now)
		return
	}
	s.controllerUpdateMaintenance.Store(true)
	if err := s.store.CloseOpenControllerConnectionsWithSource(ctx, now, model.ConnectivityEventSourceControllerUpdate); err != nil {
		log.Printf("close Controller update connections: %v", err)
		s.controllerUpdateMaintenance.Store(false)
		return
	}
	s.clearControllerUpdateMaintenance(ctx)
}

func (s *Server) ConfigureControllerUpdates(dbPath, listenAddress string) {
	s.controllerDBPath = dbPath
	s.controllerListenAddress = listenAddress
	s.controllerRuntimeStatePath = filepath.Join(filepath.Dir(dbPath), controllerupdate.RuntimeStateName)
	if configured := strings.TrimSpace(os.Getenv("OBOARD_BACKUP_DIR")); configured != "" {
		s.controllerBackupDir = configured
	} else {
		s.controllerBackupDir = filepath.Join(filepath.Dir(dbPath), "backups")
	}
	s.syncControllerRuntimeState()
}

func (s *Server) syncControllerRuntimeState() {
	if strings.TrimSpace(s.controllerRuntimeStatePath) == "" || strings.TrimSpace(s.controllerListenAddress) == "" {
		return
	}
	state := s.basePathState()
	basePaths := []string{state.Current}
	if state.MigrationVersion > 0 && state.Previous != state.Current {
		basePaths = append(basePaths, state.Previous)
	}
	if err := controllerupdate.WriteRuntimeState(s.controllerRuntimeStatePath, controllerupdate.RuntimeState{
		ListenAddress: s.controllerListenAddress,
		BasePaths:     basePaths,
	}); err != nil {
		log.Printf("write Controller runtime state: %v", err)
	}
}

func (s *Server) StartControllerUpdates(ctx context.Context) {
	s.recoverControllerUpdateRun(ctx)
	if s.agentUpdates != nil {
		go s.agentUpdates.Start(ctx)
	}
	initial := time.NewTimer(10 * time.Second)
	select {
	case <-ctx.Done():
		initial.Stop()
		return
	case <-initial.C:
		go s.runScheduledControllerUpdate(ctx)
	}
	ticker := time.NewTicker(controllerUpdateSchedulerPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			go s.runScheduledControllerUpdate(ctx)
		}
	}
}

func (s *Server) runScheduledControllerUpdate(ctx context.Context) {
	if !s.controllerUpdateRunMu.TryLock() {
		return
	}
	defer s.controllerUpdateRunMu.Unlock()
	settings, settingsErr := s.store.ListSettings(ctx)
	autoUpdateEnabled := settingsErr == nil && settingBool(settings, controllerAutoUpdateSetting, false)
	if settingsErr == nil {
		s.retainControllerUpdateBackups()
	}
	status, err := s.controllerUpdater.Status(ctx)
	if err == nil {
		s.removeSuccessfulControllerUpdateBackup(ctx, settings, status)
	}
	if !autoUpdateEnabled {
		return
	}
	interval := time.Duration(controllerUpdateIntervalHours(settings)) * time.Hour
	now := time.Now()
	if err != nil {
		if !s.controllerScheduledCheckDue(now, time.Time{}, interval) {
			return
		}
		s.markControllerScheduledCheck(now)
		publicErr := controllerUpdateOperationError("检查可用更新失败", status, err)
		s.notifyControllerUpdateFailure(ctx, "检查可用更新", status.Available.Version, publicErr.Error())
		return
	}
	if status.Channel == "pinned" || isActiveControllerUpdateStatus(status.State) {
		return
	}
	if run, _ := s.store.GetActiveControllerUpdateRun(ctx); run != nil {
		return
	}
	if !status.UpdateAvailable {
		if !s.controllerScheduledCheckDue(now, status.CheckedAt(), interval) {
			return
		}
		s.markControllerScheduledCheck(now)
		status, err = s.controllerUpdater.Check(ctx)
		if err != nil {
			publicErr := controllerUpdateOperationError("检查可用更新失败", status, err)
			s.notifyControllerUpdateFailure(ctx, "检查可用更新", status.Available.Version, publicErr.Error())
			return
		}
		s.publishRealtime("controller_update")
		if !status.UpdateAvailable || status.Channel == "pinned" {
			return
		}
	}
	if !s.controllerPanelIdle(time.Now()) {
		return
	}
	if !automaticUpdateAllowedAt(settings, time.Now()) {
		return
	}
	s.installScheduledControllerUpdate(ctx, status)
}

func automaticUpdateAllowedAt(settings map[string]string, now time.Time) bool {
	if !settingBool(settings, updateWindowEnabledSetting, false) {
		return true
	}
	location, err := time.LoadLocation(strings.TrimSpace(settings["traffic_timezone"]))
	if err != nil {
		location, _ = time.LoadLocation("Asia/Shanghai")
	}
	start := updateWindowHour(settings, updateWindowStartHourSetting, updateWindowDefaultStartHour)
	end := updateWindowHour(settings, updateWindowEndHourSetting, updateWindowDefaultEndHour)
	hour := now.In(location).Hour()
	if start == end {
		return true
	}
	if start < end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
}

func updateWindowHour(settings map[string]string, key string, fallback int) int {
	hour, err := strconv.Atoi(strings.TrimSpace(settings[key]))
	if err != nil || hour < 0 || hour > 23 {
		return fallback
	}
	return hour
}

func (s *Server) controllerScheduledCheckDue(now, checkedAt time.Time, interval time.Duration) bool {
	s.controllerUpdateScheduleMu.Lock()
	defer s.controllerUpdateScheduleMu.Unlock()
	last := checkedAt
	if s.controllerLastScheduledCheck.After(last) {
		last = s.controllerLastScheduledCheck
	}
	return last.IsZero() || now.Sub(last) >= interval
}

func (s *Server) markControllerScheduledCheck(at time.Time) {
	s.controllerUpdateScheduleMu.Lock()
	s.controllerLastScheduledCheck = at
	s.controllerUpdateScheduleMu.Unlock()
}

func (s *Server) installScheduledControllerUpdate(ctx context.Context, status controllerupdate.Status) {
	if active, _ := s.store.GetActiveControllerUpdateRun(ctx); active != nil {
		return
	}
	run := &store.ControllerUpdateRun{
		Source:         "auto",
		CurrentVersion: version.Version,
		CurrentBuild:   version.Build,
		TargetVersion:  status.Available.Version,
		TargetBuild:    status.Available.Build,
		Phase:          store.ControllerUpdatePhaseDownloading,
	}
	if err := s.store.CreateControllerUpdateRun(ctx, run); err != nil {
		return
	}
	prepared := true
	prepareStatus, err := s.controllerUpdater.Prepare(ctx)
	if err != nil {
		if !controllerUpdaterPrepareUnsupported(err) {
			publicErr := controllerUpdateOperationError("启动自动更新下载失败", prepareStatus, err)
			_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, publicErr.Error())
			s.notifyControllerUpdateFailure(ctx, "下载更新", status.Available.Version, publicErr.Error())
			s.failControllerUpdateRun(ctx, run, publicErr.Error())
			return
		}
		prepared = false
		prepareStatus = status
	} else {
		s.startControllerUpdateWatch()
	}
	status = prepareStatus
	if strings.TrimSpace(run.TargetBuild) == "" {
		run.TargetBuild = status.Available.Build
	}
	s.applyPreparedControllerUpdate(ctx, status, prepared, run, false)
}

func (s *Server) cancelPreparedControllerUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.controllerUpdater.Cancel(ctx)
}

func controllerUpdaterPrepareUnsupported(err error) bool {
	var statusErr *controllerupdate.UpdaterStatusError
	return errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound
}

func validControllerUpdateInterval(hours int) bool {
	switch hours {
	case 1, 6, 24, 72, 168:
		return true
	default:
		return false
	}
}

func controllerUpdateIntervalHours(settings map[string]string) int {
	hours, err := strconv.Atoi(strings.TrimSpace(settings[controllerAutoUpdateIntervalSetting]))
	if err != nil || !validControllerUpdateInterval(hours) {
		return controllerUpdateDefaultIntervalHours
	}
	return hours
}

func (s *Server) beginControllerPanelRequest() {
	s.controllerActivityMu.Lock()
	s.controllerActiveRequests++
	s.controllerLastActivity = time.Now()
	s.controllerActivityMu.Unlock()
}

func (s *Server) noteControllerPanelActivity() {
	s.controllerActivityMu.Lock()
	s.controllerLastActivity = time.Now()
	s.controllerActivityMu.Unlock()
}

func (s *Server) endControllerPanelRequest() {
	s.controllerActivityMu.Lock()
	if s.controllerActiveRequests > 0 {
		s.controllerActiveRequests--
	}
	s.controllerLastActivity = time.Now()
	s.controllerActivityMu.Unlock()
}

func (s *Server) controllerPanelIdle(now time.Time) bool {
	s.controllerActivityMu.Lock()
	defer s.controllerActivityMu.Unlock()
	return s.controllerActiveRequests == 0 && (s.controllerLastActivity.IsZero() || now.Sub(s.controllerLastActivity) >= controllerUpdatePanelIdlePeriod)
}

func (s *Server) controllerUpdateActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func isActiveControllerUpdateStatus(state string) bool {
	switch state {
	case "checking", "downloading", "ready", "installing", "cancelling",
		store.ControllerUpdatePhasePreflight, store.ControllerUpdatePhaseBackingUp,
		store.ControllerUpdatePhaseRestarting, store.ControllerUpdatePhaseVerifying:
		return true
	default:
		return false
	}
}

func (s *Server) controllerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	status, err := s.controllerUpdater.Status(r.Context())
	if err != nil {
		status = s.fallbackControllerUpdateStatus()
	}
	s.writeControllerUpdateStatus(w, r, status)
}

func (s *Server) fallbackControllerUpdateStatus() controllerupdate.Status {
	channel := strings.ToLower(strings.TrimSpace(os.Getenv("OBOARD_UPDATE_CHANNEL")))
	switch channel {
	case "dev":
	case "stable", "latest":
		channel = "stable"
	case "":
		if version.IsDev() {
			channel = "dev"
		} else {
			channel = "stable"
		}
	default:
		channel = "pinned"
	}
	return controllerupdate.Status{
		Channel:   channel,
		State:     "unavailable",
		LastError: "主控更新器不可用，请检查 oboard-controller-updater 服务。",
	}
}

func (s *Server) controllerUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	status, err := s.controllerUpdater.Check(r.Context())
	if err != nil {
		fail(w, controllerUpdateOperationError("检查主控更新失败", status, err), http.StatusBadGateway)
		return
	}
	auditReq(s, r, "check", "controller_update", status.Channel)
	s.writeControllerUpdateStatus(w, r, status)
}

func (s *Server) controllerUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request controllerupdate.ChannelRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		fail(w, errors.New("读取请求失败"), http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &request); err != nil {
		fail(w, errors.New("请求格式不正确"), http.StatusBadRequest)
		return
	}
	channel := strings.ToLower(strings.TrimSpace(request.Channel))
	if channel != "dev" && channel != "stable" {
		fail(w, errors.New("更新通道只能是 dev 或 stable"), http.StatusBadRequest)
		return
	}
	if !s.controllerUpdateRunMu.TryLock() {
		fail(w, errControllerUpdateBusy, http.StatusConflict)
		return
	}
	defer s.controllerUpdateRunMu.Unlock()
	status, err := s.controllerUpdater.SetChannel(r.Context(), channel)
	if err != nil {
		var statusErr *controllerupdate.UpdaterStatusError
		if errors.As(err, &statusErr) && statusErr.Code == http.StatusConflict {
			fail(w, errors.New(statusErr.Status.LastError), http.StatusConflict)
			return
		}
		fail(w, controllerUpdateOperationError("切换更新通道失败", status, err), http.StatusBadGateway)
		return
	}
	auditReq(s, r, "channel", "controller_update", channel)
	s.writeControllerUpdateStatus(w, r, status)
}

func (s *Server) controllerUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		fail(w, errors.New("读取请求失败"), http.StatusBadRequest)
		return
	}
	var request struct {
		SkipBackup *bool `json:"skip_backup"`
	}
	if strings.TrimSpace(string(body)) != "" {
		if err := json.Unmarshal(body, &request); err != nil {
			fail(w, errors.New("请求格式不正确"), http.StatusBadRequest)
			return
		}
	}
	status, _, err := s.beginManualControllerUpdate(r.Context(), controllerUpdateSkipBackup(request.SkipBackup))
	if err != nil {
		switch {
		case errors.Is(err, errControllerUpdaterUnavailable):
			fail(w, err, http.StatusServiceUnavailable)
		case errors.Is(err, errControllerUpdatePinned), errors.Is(err, errControllerUpdateBusy):
			fail(w, err, http.StatusConflict)
		default:
			fail(w, err, http.StatusBadGateway)
		}
		return
	}
	auditReq(s, r, "install", "controller_update", status.Channel+":"+status.Available.Version)
	s.writeControllerUpdateStatus(w, r, status)
}

var (
	errControllerUpdaterUnavailable = errors.New("主控更新器不可用，请检查 oboard-controller-updater 服务")
	errControllerUpdatePinned       = errors.New("固定版本不能在面板内更新，请先按提示切换更新通道")
	errControllerUpdateBusy         = errors.New("已有主控更新操作正在进行，请稍后查看更新状态")
)

func controllerUpdateSkipBackup(value *bool) bool {
	return value != nil && *value
}

func (s *Server) beginManualControllerUpdate(ctx context.Context, skipBackup bool) (controllerupdate.Status, bool, error) {
	if !s.controllerUpdateRunMu.TryLock() {
		return controllerupdate.Status{}, false, errControllerUpdateBusy
	}
	backgroundStarted := false
	defer func() {
		if !backgroundStarted {
			s.controllerUpdateRunMu.Unlock()
		}
	}()
	if active, _ := s.store.GetActiveControllerUpdateRun(ctx); active != nil {
		return controllerupdate.Status{}, false, errControllerUpdateBusy
	}
	status, err := s.controllerUpdater.Status(ctx)
	if err != nil {
		return status, false, errControllerUpdaterUnavailable
	}
	if status.Channel == "pinned" {
		return status, false, errControllerUpdatePinned
	}
	run := &store.ControllerUpdateRun{
		Source:         "manual",
		CurrentVersion: version.Version,
		CurrentBuild:   version.Build,
		TargetVersion:  status.Available.Version,
		TargetBuild:    status.Available.Build,
		Phase:          store.ControllerUpdatePhaseChecking,
	}
	if err := s.store.CreateControllerUpdateRun(ctx, run); err != nil {
		return status, false, errControllerUpdateBusy
	}
	if !status.UpdateAvailable {
		run.Phase = store.ControllerUpdatePhaseChecking
		_ = s.store.UpdateControllerUpdateRun(ctx, run)
		status, err = s.controllerUpdater.Check(ctx)
		if err != nil {
			s.failControllerUpdateRun(ctx, run, controllerUpdateOperationError("检查主控更新失败", status, err).Error())
			return status, false, controllerUpdateOperationError("检查主控更新失败", status, err)
		}
	}
	if !status.UpdateAvailable {
		s.cancelControllerUpdateRun(ctx, run)
		return status, false, nil
	}
	run.TargetVersion = status.Available.Version
	run.TargetBuild = status.Available.Build
	run.Phase = store.ControllerUpdatePhaseDownloading
	_ = s.store.UpdateControllerUpdateRun(ctx, run)
	checkedStatus := status
	prepared := true
	prepareStatus, err := s.controllerUpdater.Prepare(ctx)
	status = prepareStatus
	if err != nil {
		if !controllerUpdaterPrepareUnsupported(err) {
			publicErr := controllerUpdateOperationError("启动主控更新下载失败", status, err)
			_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, publicErr.Error())
			s.failControllerUpdateRun(ctx, run, publicErr.Error())
			return status, false, publicErr
		}
		prepared = false
		status = checkedStatus
	} else {
		s.startControllerUpdateWatch()
	}
	_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "")
	backgroundStarted = true
	go s.finishManualControllerUpdate(status, prepared, run, skipBackup)
	return status, true, nil
}

func (s *Server) finishManualControllerUpdate(status controllerupdate.Status, prepared bool, run *store.ControllerUpdateRun, skipBackup bool) {
	defer s.controllerUpdateRunMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), controllerUpdateInstallTimeout)
	s.setControllerUpdateCancel(cancel)
	defer func() {
		s.cancelControllerUpdateContext()
	}()
	s.applyPreparedControllerUpdate(ctx, status, prepared, run, skipBackup)
}

func (s *Server) applyPreparedControllerUpdate(ctx context.Context, status controllerupdate.Status, prepared bool, run *store.ControllerUpdateRun, skipBackup bool) {
	if run == nil {
		return
	}
	run.Phase = store.ControllerUpdatePhasePreflight
	_ = s.store.UpdateControllerUpdateRun(ctx, run)
	s.publishRealtime("controller_update")
	dir := s.controllerBackupDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("data", "backups")
	}
	if err := s.preflightControllerUpdate(ctx, run, dir, skipBackup); err != nil {
		log.Printf("controller update preflight failed: %v", err)
		if prepared {
			s.cancelPreparedControllerUpdate()
		}
		message := "更新前检查失败: " + localizeBackupErrorMessage(err.Error())
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, message)
		s.failControllerUpdateRun(ctx, run, message)
		return
	}
	if !skipBackup {
		run.Phase = store.ControllerUpdatePhaseBackingUp
		_ = s.store.UpdateControllerUpdateRun(ctx, run)
		s.publishRealtime("controller_update")
		backupStarted := time.Now()
		backup, err := s.createControllerBackup(ctx)
		run.BackupDurationMS = time.Since(backupStarted).Milliseconds()
		if err != nil {
			log.Printf("Controller update backup failed: %v", err)
			if prepared {
				s.cancelPreparedControllerUpdate()
			}
			if errors.Is(err, context.Canceled) {
				s.cancelControllerUpdateRun(ctx, run)
				_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "")
				return
			}
			message := "创建数据库备份失败，已取消更新: " + localizeBackupErrorMessage(err.Error())
			_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, message)
			s.failControllerUpdateRun(ctx, run, message)
			return
		}
		run.BackupPath = backup
		if info, statErr := os.Stat(backup); statErr == nil {
			run.BackupSizeBytes = info.Size()
			run.BackupRemainingPages = 0
		}
		if err := s.recordControllerUpdateBackup(ctx, backup, status.Available.Build); err != nil {
			log.Printf("Controller update backup recording failed: %v", err)
			if prepared {
				s.cancelPreparedControllerUpdate()
			}
			message := "记录数据库备份失败，已取消更新: " + localizeBackupErrorMessage(err.Error())
			_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, message)
			s.failControllerUpdateRun(ctx, run, message)
			return
		}
	}
	if run.Source == "auto" && !s.controllerPanelIdle(time.Now()) {
		if prepared {
			s.cancelPreparedControllerUpdate()
		}
		s.cancelControllerUpdateRun(ctx, run)
		return
	}
	run.Phase = store.ControllerUpdatePhaseInstalling
	_ = s.store.UpdateControllerUpdateRun(ctx, run)
	s.publishRealtime("controller_update")
	installStarted := time.Now()
	targetBuild := strings.TrimSpace(run.TargetBuild)
	if targetBuild == "" {
		targetBuild = status.Available.Build
	}
	status, err := s.installControllerUpdate(ctx, targetBuild)
	run.InstallDurationMS = time.Since(installStarted).Milliseconds()
	if err != nil {
		if (status.State == "cancelled" || status.State == "cancelling") && strings.TrimSpace(status.LastError) == "" {
			s.cancelControllerUpdateRun(ctx, run)
			_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "")
			return
		}
		publicErr := controllerUpdateOperationError("启动主控更新失败", status, err)
		log.Printf("Controller update start failed: %v", publicErr)
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, publicErr.Error())
		s.failControllerUpdateRun(ctx, run, publicErr.Error())
		return
	}
	run.Phase = store.ControllerUpdatePhaseRestarting
	_ = s.store.UpdateControllerUpdateRun(ctx, run)
	s.startControllerUpdateWatch()
	_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "")
	s.publishRealtime("controller_update")
}

func (s *Server) controllerUpdateCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	run, _ := s.store.GetActiveControllerUpdateRun(r.Context())
	if run != nil && controllerUpdatePhaseCancellable(run.Phase) {
		s.cancelControllerUpdateContext()
		s.cancelPreparedControllerUpdate()
		s.cancelControllerUpdateRun(r.Context(), run)
		_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, "")
		auditReq(s, r, "cancel", "controller_update", run.TargetBuild)
		status, err := s.controllerUpdater.Status(r.Context())
		if err != nil {
			status = s.fallbackControllerUpdateStatus()
		}
		status.State = store.ControllerUpdatePhaseCancelled
		s.writeControllerUpdateStatus(w, r, status)
		return
	}
	status, err := s.controllerUpdater.Cancel(r.Context())
	if err != nil {
		if strings.TrimSpace(status.LastError) != "" {
			fail(w, errors.New(status.LastError), http.StatusConflict)
		} else {
			fail(w, errors.New("当前没有可以中断的更新"), http.StatusConflict)
		}
		return
	}
	if run != nil {
		s.cancelControllerUpdateRun(r.Context(), run)
	}
	auditReq(s, r, "cancel", "controller_update", status.Channel+":"+status.Available.Version)
	s.writeControllerUpdateStatus(w, r, status)
}

func (s *Server) controllerUpdateForceFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		Confirmation string `json:"confirmation"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Confirmation) != controllerUpdateForceFinishPhrase {
		fail(w, errors.New("请输入“强制结束更新任务”以确认操作"), http.StatusBadRequest)
		return
	}
	status, changed, err := s.forceFinishControllerUpdate(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "force_finish", "controller_update", status.Available.Build)
	s.writeControllerUpdateStatus(w, r, status)
	if changed {
		s.publishRealtime("controller_update")
	}
}

func (s *Server) forceFinishControllerUpdate(ctx context.Context) (controllerupdate.Status, bool, error) {
	_, changed, err := s.store.ForceFinishActiveControllerUpdateRun(ctx, controllerUpdateForceFinishedReason)
	if err != nil {
		return controllerupdate.Status{}, false, err
	}
	if changed {
		s.cancelControllerUpdateContext()
		s.cancelPreparedControllerUpdate()
		s.clearControllerUpdateMaintenance(ctx)
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "")
	}
	status, statusErr := s.controllerUpdater.Status(ctx)
	if statusErr != nil {
		status = s.fallbackControllerUpdateStatus()
	}
	if isActiveControllerUpdateStatus(status.State) {
		status.State = store.ControllerUpdatePhaseCancelled
		status.CanCancel = false
		status.LastError = ""
	}
	return status, changed, nil
}

func (s *Server) startControllerUpdateWatch() {
	s.controllerUpdateWatchMu.Lock()
	if s.controllerUpdateWatching {
		s.controllerUpdateWatchMu.Unlock()
		return
	}
	s.controllerUpdateWatching = true
	s.controllerUpdateWatchMu.Unlock()
	go func() {
		defer func() {
			s.controllerUpdateWatchMu.Lock()
			s.controllerUpdateWatching = false
			s.controllerUpdateWatchMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		previous := ""
		activeSeen := false
		idleSamples := 0
		failures := 0
		for {
			status, err := s.controllerUpdater.Status(ctx)
			if err != nil {
				failures++
				if failures >= 5 {
					s.publishRealtime("controller_update")
					return
				}
			} else {
				failures = 0
				digest := fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%t\x00%s", status.State, status.Current.Build, status.Available.Build, status.UpdateAvailable, status.CanCancel, status.LastError)
				if digest != previous {
					previous = digest
					s.publishRealtime("controller_update")
				}
				active := status.State == "checking" || status.State == "downloading" || status.State == "ready" || status.State == "installing" || status.State == "cancelling"
				activeSeen = activeSeen || active
				if active {
					idleSamples = 0
				} else {
					idleSamples++
				}
				if !active && (activeSeen || idleSamples >= 5) {
					if s.controllerUpdateMaintenance.Load() {
						s.clearControllerUpdateMaintenance(ctx)
					}
					settings, listErr := s.store.ListSettings(ctx)
					if listErr == nil {
						s.removeSuccessfulControllerUpdateBackup(ctx, settings, status)
						s.cleanupControllerUpdateBackupFiles(settings[controllerBackupSetting])
					}
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func controllerUpdateOperationError(prefix string, status controllerupdate.Status, err error) error {
	if strings.Contains(err.Error(), "controller updater unavailable") {
		return errors.New("主控更新器不可用，请检查 oboard-controller-updater 服务")
	}
	if strings.TrimSpace(status.LastError) != "" {
		return fmt.Errorf("%s：%s", prefix, localizeBackupErrorMessage(status.LastError))
	}
	return errors.New(prefix)
}

func (s *Server) writeControllerUpdateStatus(w http.ResponseWriter, r *http.Request, status controllerupdate.Status) {
	status.Current = controllerupdate.BuildInfo{Version: version.Version, Build: version.Build, Commit: version.Commit, Date: version.Date}
	settings, err := s.store.ListSettings(r.Context())
	if err == nil {
		status.AutoUpdateEnabled = settingBool(settings, controllerAutoUpdateSetting, false)
		status.AutoUpdateIntervalHours = controllerUpdateIntervalHours(settings)
		if status.BackupPath == "" {
			status.BackupPath = settings[controllerBackupSetting]
		}
		if status.LastError == "" {
			status.LastError = settings[controllerUpdateErrorSetting]
		}
	}
	status.LastError = localizeBackupErrorMessage(status.LastError)
	s.attachControllerUpdateOperation(r.Context(), &status)
	write(w, http.StatusOK, status)
}

func (s *Server) createControllerBackup(ctx context.Context) (string, error) {
	dir := s.controllerBackupDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("data", "backups")
	}
	s.cleanupZeroByteControllerUpdateBackupFiles()
	name := "oboard-before-update-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".sqlite"
	path := filepath.Join(dir, name)
	lastPublish := time.Time{}
	err := s.store.Backup(ctx, path, store.BackupOptions{
		Progress: func(progress store.BackupProgress) {
			s.controllerUpdateProgress.Store(progress)
			now := time.Now()
			if lastPublish.IsZero() || now.Sub(lastPublish) >= 250*time.Millisecond {
				s.publishRealtime("controller_update")
				lastPublish = now
			}
		},
	})
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (s *Server) cleanupZeroByteControllerUpdateBackupFiles() {
	dir := s.controllerBackupDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("data", "backups")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !isControllerUpdateBackupName(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() != 0 {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			log.Printf("remove zero-byte Controller update backup %s: %v", filepath.Join(dir, entry.Name()), err)
		}
	}
}

func (s *Server) retainControllerUpdateBackups() {
	retain := s.controllerUpdateRetention()
	s.retainControllerUpdateBackupsWithLimit(retain)
}

func (s *Server) retainControllerUpdateBackupsWithLimit(retain int) {
	dir := s.controllerBackupDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("data", "backups")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type backupFile struct {
		path    string
		modTime time.Time
		size    int64
	}
	var complete []backupFile
	for _, entry := range entries {
		if !isControllerUpdateBackupName(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if info.Size() <= 0 {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Printf("remove partial Controller update backup %s: %v", path, err)
			}
			continue
		}
		complete = append(complete, backupFile{path: path, modTime: info.ModTime(), size: info.Size()})
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].modTime.After(complete[j].modTime) })
	if retain < 0 {
		retain = 0
	}
	for index := retain; index < len(complete); index++ {
		if err := os.Remove(complete[index].path); err != nil && !os.IsNotExist(err) {
			log.Printf("remove excess Controller update backup %s: %v", complete[index].path, err)
		}
	}
}

func (s *Server) cleanupControllerUpdateBackupFiles(retainedPath string) {
	s.retainControllerUpdateBackups()
	_ = retainedPath
}

func (s *Server) recordControllerUpdateBackup(ctx context.Context, path, targetBuild string) error {
	return s.store.SetSettings(ctx, map[string]string{
		controllerBackupSetting:            path,
		controllerBackupTargetBuildSetting: strings.TrimSpace(targetBuild),
	})
}

func (s *Server) removeSuccessfulControllerUpdateBackup(ctx context.Context, settings map[string]string, status controllerupdate.Status) {
	path := strings.TrimSpace(settings[controllerBackupSetting])
	targetBuild := strings.TrimSpace(settings[controllerBackupTargetBuildSetting])
	if path == "" || targetBuild == "" || strings.TrimSpace(status.Current.Build) != targetBuild || !s.isControllerUpdateBackupPath(path) {
		return
	}
	retain := s.controllerUpdateRetention()
	if retain == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("remove successful Controller update backup %s: %v", path, err)
		}
		_ = s.store.SetSettings(ctx, map[string]string{controllerBackupSetting: "", controllerBackupTargetBuildSetting: ""})
		s.retainControllerUpdateBackupsWithLimit(0)
		return
	}
	s.retainControllerUpdateBackupsWithLimit(retain)
}

func (s *Server) isControllerUpdateBackupPath(path string) bool {
	dir := s.controllerBackupDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("data", "backups")
	}
	return filepath.Clean(filepath.Dir(path)) == filepath.Clean(dir) && isControllerUpdateBackupName(filepath.Base(path))
}

func isControllerUpdateBackupName(name string) bool {
	return strings.HasPrefix(name, "oboard-before-update-") && strings.HasSuffix(name, ".sqlite")
}

func settingBool(settings map[string]string, key string, fallback bool) bool {
	value := strings.TrimSpace(settings[key])
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
