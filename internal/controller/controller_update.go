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
	"github.com/OboardProject/oboard/internal/version"
)

const (
	controllerAutoUpdateSetting  = "controller_auto_update_enabled"
	controllerBackupSetting      = "controller_update_backup_path"
	controllerUpdateErrorSetting = "controller_update_controller_error"
	controllerUpdateCheckPeriod  = 24 * time.Hour
	controllerLoginCheckDedup    = 15 * time.Minute
	controllerBackupRetention    = 7
)

func (s *Server) ConfigureControllerUpdates(dbPath, listenAddress string) {
	s.controllerUpdatesConfigured = true
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
	}
	ticker := time.NewTicker(controllerUpdateCheckPeriod)
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

func (s *Server) TriggerControllerUpdateCheck(ctx context.Context) {
	if !s.controllerUpdatesConfigured {
		return
	}
	s.controllerUpdateMu.Lock()
	if time.Since(s.controllerLastLoginCheck) < controllerLoginCheckDedup {
		s.controllerUpdateMu.Unlock()
		return
	}
	s.controllerLastLoginCheck = time.Now()
	s.controllerUpdateMu.Unlock()
	go s.runScheduledControllerUpdate(context.WithoutCancel(ctx))
}

func (s *Server) runScheduledControllerUpdate(ctx context.Context) {
	s.controllerUpdateRunMu.Lock()
	defer s.controllerUpdateRunMu.Unlock()
	settings, settingsErr := s.store.ListSettings(ctx)
	autoUpdateEnabled := settingsErr == nil && settingBool(settings, controllerAutoUpdateSetting, false)
	status, err := s.controllerUpdater.Check(ctx)
	if err != nil {
		if autoUpdateEnabled {
			publicErr := controllerUpdateOperationError("检查可用更新失败", status, err)
			s.notifyControllerUpdateFailure(ctx, "检查可用更新", status.Available.Version, publicErr.Error())
		}
		return
	}
	s.publishRealtime("controller_update")
	if !status.UpdateAvailable || status.Channel == "pinned" || !autoUpdateEnabled {
		return
	}
	backup, err := s.createControllerBackup(ctx)
	if err != nil {
		message := "创建自动更新备份失败: " + err.Error()
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, message)
		s.notifyControllerUpdateFailure(ctx, "更新前备份", status.Available.Version, message)
		return
	}
	if err := s.store.SetSetting(ctx, controllerBackupSetting, backup); err != nil {
		message := "记录自动更新备份失败: " + err.Error()
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, message)
		s.notifyControllerUpdateFailure(ctx, "更新前备份", status.Available.Version, message)
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
	backup, err := s.createControllerBackup(r.Context())
	if err != nil {
		_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, err.Error())
		fail(w, fmt.Errorf("创建数据库备份失败，已取消更新: %w", err), http.StatusInternalServerError)
		return
	}
	if err := s.store.SetSetting(r.Context(), controllerBackupSetting, backup); err != nil {
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
	entries, err := filepath.Glob(filepath.Join(dir, "oboard-before-update-*.sqlite"))
	if err != nil {
		return path, nil
	}
	sort.Strings(entries)
	for len(entries) > controllerBackupRetention {
		_ = os.Remove(entries[0])
		entries = entries[1:]
	}
	return path, nil
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
