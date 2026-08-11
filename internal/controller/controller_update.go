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
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
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
	controllerUpdateSchedulerPeriod      = time.Minute
	controllerUpdatePanelIdlePeriod      = 5 * time.Minute
	controllerUpdateDefaultIntervalHours = 24
	updateWindowDefaultStartHour         = 3
	updateWindowDefaultEndHour           = 7
)

func (s *Server) ConfigureControllerUpdates(dbPath, listenAddress string) {
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
	initial := time.NewTimer(10 * time.Second)
	select {
	case <-ctx.Done():
		initial.Stop()
		return
	case <-initial.C:
		go s.runScheduledControllerUpdate(ctx)
		go s.runScheduledManagedUpdates(ctx)
	}
	ticker := time.NewTicker(controllerUpdateSchedulerPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			go s.runScheduledControllerUpdate(ctx)
			go s.runScheduledManagedUpdates(ctx)
		}
	}
}

func (s *Server) runScheduledControllerUpdate(ctx context.Context) {
	s.controllerUpdateRunMu.Lock()
	defer s.controllerUpdateRunMu.Unlock()
	settings, settingsErr := s.store.ListSettings(ctx)
	autoUpdateEnabled := settingsErr == nil && settingBool(settings, controllerAutoUpdateSetting, false)
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
	prepared := true
	prepareStatus, err := s.controllerUpdater.Prepare(ctx)
	if err != nil {
		if !controllerUpdaterPrepareUnsupported(err) {
			publicErr := controllerUpdateOperationError("启动自动更新下载失败", prepareStatus, err)
			_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, publicErr.Error())
			s.notifyControllerUpdateFailure(ctx, "下载更新", status.Available.Version, publicErr.Error())
			return
		}
		prepared = false
		prepareStatus = status
	} else {
		s.startControllerUpdateWatch()
	}
	backup, err := s.createControllerBackup(ctx)
	if err != nil {
		if prepared {
			s.cancelPreparedControllerUpdate()
		}
		message := "创建自动更新备份失败: " + err.Error()
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, message)
		s.notifyControllerUpdateFailure(ctx, "更新前备份", status.Available.Version, message)
		return
	}
	targetBuild := strings.TrimSpace(prepareStatus.Available.Build)
	if targetBuild == "" {
		targetBuild = status.Available.Build
	}
	if err := s.recordControllerUpdateBackup(ctx, backup, targetBuild); err != nil {
		if prepared {
			s.cancelPreparedControllerUpdate()
		}
		message := "记录自动更新备份失败: " + err.Error()
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, message)
		s.notifyControllerUpdateFailure(ctx, "更新前备份", status.Available.Version, message)
		return
	}
	if !s.controllerPanelIdle(time.Now()) {
		if prepared {
			s.cancelPreparedControllerUpdate()
		}
		return
	}
	if installStatus, err := s.controllerUpdater.Install(ctx); err != nil {
		publicErr := controllerUpdateOperationError("启动自动更新失败", installStatus, err)
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, publicErr.Error())
		s.notifyControllerUpdateFailure(ctx, "安装更新", status.Available.Version, publicErr.Error())
		return
	}
	s.startControllerUpdateWatch()
	_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "")
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
	return state == "checking" || state == "downloading" || state == "ready" || state == "installing" || state == "cancelling"
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
	s.controllerUpdateRunMu.Lock()
	defer s.controllerUpdateRunMu.Unlock()
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
	s.controllerUpdateRunMu.Lock()
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
	s.controllerUpdateRunMu.Lock()
	defer s.controllerUpdateRunMu.Unlock()
	status, err := s.controllerUpdater.Status(r.Context())
	if err != nil {
		fail(w, errors.New("主控更新器不可用，请检查 oboard-controller-updater 服务"), http.StatusServiceUnavailable)
		return
	}
	if status.Channel == "pinned" {
		fail(w, errors.New("固定版本不能在面板内更新，请先按提示切换更新通道"), http.StatusConflict)
		return
	}
	if !status.UpdateAvailable {
		status, err = s.controllerUpdater.Check(r.Context())
		if err != nil {
			fail(w, controllerUpdateOperationError("检查主控更新失败", status, err), http.StatusBadGateway)
			return
		}
	}
	if !status.UpdateAvailable {
		s.writeControllerUpdateStatus(w, r, status)
		return
	}
	checkedStatus := status
	prepared := true
	prepareStatus, err := s.controllerUpdater.Prepare(r.Context())
	status = prepareStatus
	if err != nil {
		if !controllerUpdaterPrepareUnsupported(err) {
			publicErr := controllerUpdateOperationError("启动主控更新下载失败", status, err)
			_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, publicErr.Error())
			fail(w, publicErr, http.StatusBadGateway)
			return
		}
		prepared = false
		status = checkedStatus
	} else {
		s.startControllerUpdateWatch()
	}
	backup, err := s.createControllerBackup(r.Context())
	if err != nil {
		if prepared {
			s.cancelPreparedControllerUpdate()
		}
		_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, err.Error())
		fail(w, fmt.Errorf("创建数据库备份失败，已取消更新: %w", err), http.StatusInternalServerError)
		return
	}
	if err := s.recordControllerUpdateBackup(r.Context(), backup, status.Available.Build); err != nil {
		if prepared {
			s.cancelPreparedControllerUpdate()
		}
		_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, err.Error())
		fail(w, fmt.Errorf("记录数据库备份失败，已取消更新: %w", err), http.StatusInternalServerError)
		return
	}
	status, err = s.controllerUpdater.Install(r.Context())
	if err != nil {
		publicErr := controllerUpdateOperationError("启动主控更新失败", status, err)
		_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, publicErr.Error())
		fail(w, publicErr, http.StatusBadGateway)
		return
	}
	s.startControllerUpdateWatch()
	status.BackupPath = backup
	_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, "")
	auditReq(s, r, "install", "controller_update", status.Channel+":"+status.Available.Version)
	s.writeControllerUpdateStatus(w, r, status)
}

func (s *Server) controllerUpdateCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
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
	auditReq(s, r, "cancel", "controller_update", status.Channel+":"+status.Available.Version)
	s.writeControllerUpdateStatus(w, r, status)
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
		return fmt.Errorf("%s：%s", prefix, status.LastError)
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
	write(w, http.StatusOK, status)
}

func (s *Server) createControllerBackup(ctx context.Context) (string, error) {
	dir := s.controllerBackupDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("data", "backups")
	}
	name := "oboard-before-update-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".sqlite"
	path := filepath.Join(dir, name)
	if err := s.store.Backup(ctx, path); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return path, nil
	}
	for _, entry := range entries {
		entryPath := filepath.Join(dir, entry.Name())
		if entryPath == path || !isControllerUpdateBackupName(entry.Name()) {
			continue
		}
		if err := os.Remove(entryPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove previous Controller update backup %s: %v", entryPath, err)
		}
	}
	return path, nil
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
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("remove successful Controller update backup %s: %v", path, err)
		return
	}
	if err := s.store.SetSettings(ctx, map[string]string{
		controllerBackupSetting:            "",
		controllerBackupTargetBuildSetting: "",
	}); err != nil {
		log.Printf("clear successful Controller update backup state: %v", err)
	}
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
