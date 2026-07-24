package controller

import (
	"context"
	"errors"
	"fmt"
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

func (s *Server) ConfigureControllerUpdates(dbPath string) {
	s.controllerUpdatesConfigured = true
	if configured := strings.TrimSpace(os.Getenv("OBOARD_BACKUP_DIR")); configured != "" {
		s.controllerBackupDir = configured
	} else {
		s.controllerBackupDir = filepath.Join(filepath.Dir(dbPath), "backups")
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
	status, err := s.controllerUpdater.Check(ctx)
	if err != nil || !status.UpdateAvailable || status.Channel == "pinned" {
		return
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil || !settingBool(settings, controllerAutoUpdateSetting, false) {
		return
	}
	backup, err := s.createControllerBackup(ctx)
	if err != nil {
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "创建自动更新备份失败: "+err.Error())
		return
	}
	if err := s.store.SetSetting(ctx, controllerBackupSetting, backup); err != nil {
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "记录自动更新备份失败: "+err.Error())
		return
	}
	if installStatus, err := s.controllerUpdater.Install(ctx); err != nil {
		_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, controllerUpdateOperationError("启动自动更新失败", installStatus, err).Error())
		return
	}
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
	method := strings.ToLower(strings.TrimSpace(os.Getenv("OBOARD_INSTALL_METHOD")))
	if method != "docker" {
		method = "binary"
	}
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
		InstallMethod: method,
		Channel:       channel,
		State:         "unavailable",
		LastError:     "主控更新器不可用，请检查 oboard-controller-updater 服务。",
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
	status.BackupPath = backup
	_ = s.store.SetSetting(r.Context(), controllerUpdateErrorSetting, "")
	auditReq(s, r, "install", "controller_update", status.Channel+":"+status.Available.Version)
	s.writeControllerUpdateStatus(w, r, status)
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
