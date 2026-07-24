package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/backup"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

const (
	controllerBackupEnabledSetting          = "controller_backup_enabled"
	controllerBackupScheduleSetting         = "controller_backup_schedule"
	controllerBackupTimeSetting             = "controller_backup_time"
	controllerBackupWeekdaySetting          = "controller_backup_weekday"
	controllerBackupLocalRetentionSetting   = "controller_backup_local_retention"
	controllerBackupRemoteRetentionSetting  = "controller_backup_remote_retention"
	controllerBackupDestinationSetting      = "controller_backup_destination"
	controllerBackupSecretsSetting          = "controller_backup_secret_config"
	controllerBackupLastPeriodSetting       = "controller_backup_last_period"
	controllerBackupLastSuccessSetting      = "controller_backup_last_success_at"
	controllerBackupLastErrorSetting        = "controller_backup_last_error"
	controllerBackupRestoreReconcileSetting = "controller_backup_restore_reconcile"
)

type controllerBackupSecrets struct {
	RecoveryPassword string               `json:"recovery_password"`
	Remote           backup.RemoteSecrets `json:"remote"`
}

type controllerBackupSettingsState struct {
	Enabled         bool               `json:"enabled"`
	Schedule        string             `json:"schedule"`
	Time            string             `json:"time"`
	Weekday         int                `json:"weekday"`
	LocalRetention  int                `json:"local_retention"`
	RemoteRetention int                `json:"remote_retention"`
	Destination     backup.Destination `json:"destination"`
	Secrets         controllerBackupSecrets
	Timezone        string `json:"-"`
	LastSuccessAt   string `json:"last_success_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

type controllerBackupSettingsRequest struct {
	Enabled          *bool              `json:"enabled"`
	Schedule         string             `json:"schedule"`
	Time             string             `json:"time"`
	Weekday          int                `json:"weekday"`
	LocalRetention   int                `json:"local_retention"`
	RemoteRetention  int                `json:"remote_retention"`
	Destination      backup.Destination `json:"destination"`
	RecoveryPassword string             `json:"recovery_password"`
	S3AccessKey      string             `json:"s3_access_key"`
	S3SecretKey      string             `json:"s3_secret_key"`
	WebDAVUsername   string             `json:"webdav_username"`
	WebDAVPassword   string             `json:"webdav_password"`
}

func (s *Server) ConfigureControllerBackups(dbPath string) {
	directory := strings.TrimSpace(os.Getenv("OBOARD_BACKUP_DIR"))
	if directory == "" {
		directory = filepath.Join(filepath.Dir(dbPath), "backups")
	}
	acmeHome := strings.TrimSpace(os.Getenv("OBOARD_ACME_HOME"))
	if acmeHome == "" {
		acmeHome = filepath.Join(filepath.Dir(dbPath), "acme")
	}
	s.acmeHome = acmeHome
	manager, err := backup.New(backup.Config{Root: directory, DatabasePath: dbPath, ACMEHome: acmeHome, MasterSecret: s.sessionSecret, SourceVersion: version.Version, Snapshot: s.store.Backup})
	if err != nil {
		log.Printf("configure controller backups: %v", err)
		return
	}
	s.backupManager = manager
	s.backupConfigured = true
}

func (s *Server) SetControllerBackupRestart(restart func()) {
	s.backupRestart = restart
}

func (s *Server) StartControllerBackups(ctx context.Context) {
	if !s.backupConfigured || s.backupManager == nil {
		return
	}
	startupCtx := context.WithoutCancel(ctx)
	s.reconcileControllerBackupFiles(startupCtx)
	go s.reconcileRestoredDeployment(startupCtx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runScheduledControllerBackup(context.WithoutCancel(ctx))
		}
	}
}

func (s *Server) runScheduledControllerBackup(ctx context.Context) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	settings, err := s.loadControllerBackupSettings(ctx)
	if err != nil || !settings.Enabled || strings.TrimSpace(settings.Secrets.RecoveryPassword) == "" {
		return
	}
	period, due := scheduledBackupPeriod(settings, time.Now())
	if !due {
		return
	}
	items, err := s.store.ListSettings(ctx)
	if err != nil || items[controllerBackupLastPeriodSetting] == period {
		return
	}
	item, err := s.createControllerDataBackup(ctx, settings, "automatic", true, false)
	if err != nil {
		values := map[string]string{controllerBackupLastErrorSetting: "自动备份失败：" + err.Error()}
		if item != nil {
			values[controllerBackupLastPeriodSetting] = period
			values[controllerBackupLastSuccessSetting] = time.Now().UTC().Format(time.RFC3339Nano)
		}
		_ = s.store.SetSettings(ctx, values)
		return
	}
	lastError := ""
	if item.RemoteStatus == "failed" {
		lastError = "本地自动备份已完成，但第三方上传失败：" + item.RemoteError
	}
	_ = s.store.SetSettings(ctx, map[string]string{controllerBackupLastPeriodSetting: period, controllerBackupLastSuccessSetting: time.Now().UTC().Format(time.RFC3339Nano), controllerBackupLastErrorSetting: lastError})
}

func scheduledBackupPeriod(settings controllerBackupSettingsState, current time.Time) (string, bool) {
	location, err := time.LoadLocation(settings.Timezone)
	if err == nil {
		current = current.In(location)
	}
	scheduledClock, err := time.Parse("15:04", settings.Time)
	if err != nil {
		return "", false
	}
	if settings.Schedule == "weekly" {
		daysSinceScheduled := (int(current.Weekday()) - settings.Weekday + 7) % 7
		scheduledDate := current.AddDate(0, 0, -daysSinceScheduled)
		scheduledAt := time.Date(scheduledDate.Year(), scheduledDate.Month(), scheduledDate.Day(), scheduledClock.Hour(), scheduledClock.Minute(), 0, 0, current.Location())
		if current.Before(scheduledAt) {
			return "", false
		}
		year, week := scheduledAt.ISOWeek()
		return fmt.Sprintf("weekly:%d-%02d", year, week), true
	}
	scheduledAt := time.Date(current.Year(), current.Month(), current.Day(), scheduledClock.Hour(), scheduledClock.Minute(), 0, 0, current.Location())
	if current.Before(scheduledAt) {
		return "", false
	}
	return "daily:" + current.Format("2006-01-02"), true
}

func (s *Server) loadControllerBackupSettings(ctx context.Context) (controllerBackupSettingsState, error) {
	items, err := s.store.ListSettings(ctx)
	if err != nil {
		return controllerBackupSettingsState{}, err
	}
	state := controllerBackupSettingsState{
		Enabled:         settingBool(items, controllerBackupEnabledSetting, false),
		Schedule:        strings.TrimSpace(items[controllerBackupScheduleSetting]),
		Time:            strings.TrimSpace(items[controllerBackupTimeSetting]),
		Weekday:         settingInt(items, controllerBackupWeekdaySetting, 0, 0, 6),
		LocalRetention:  settingInt(items, controllerBackupLocalRetentionSetting, 7, 1, 100),
		RemoteRetention: settingInt(items, controllerBackupRemoteRetentionSetting, 30, 1, 365),
		Timezone:        strings.TrimSpace(items["traffic_timezone"]),
		LastSuccessAt:   items[controllerBackupLastSuccessSetting],
		LastError:       items[controllerBackupLastErrorSetting],
	}
	if state.Schedule != "daily" && state.Schedule != "weekly" {
		state.Schedule = "daily"
	}
	if _, err := time.Parse("15:04", state.Time); err != nil {
		state.Time = "03:00"
	}
	if state.Timezone == "" {
		state.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(state.Timezone); err != nil {
		state.Timezone = "Asia/Shanghai"
	}
	if raw := strings.TrimSpace(items[controllerBackupDestinationSetting]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &state.Destination); err != nil {
			return controllerBackupSettingsState{}, errors.New("备份目标设置已损坏，请重新保存")
		}
	}
	if raw := strings.TrimSpace(items[controllerBackupSecretsSetting]); raw != "" {
		plain, err := security.DecryptSecret(s.sessionSecret, controllerBackupSecretsSetting, raw)
		if err != nil {
			return controllerBackupSettingsState{}, errors.New("备份加密设置无法读取，请重新设置恢复密码")
		}
		if err := json.Unmarshal([]byte(plain), &state.Secrets); err != nil {
			return controllerBackupSettingsState{}, errors.New("备份加密设置已损坏，请重新设置恢复密码")
		}
	}
	return state, nil
}

func publicControllerBackupSettings(settings controllerBackupSettingsState) map[string]any {
	return map[string]any{
		"enabled":                settings.Enabled,
		"schedule":               settings.Schedule,
		"time":                   settings.Time,
		"weekday":                settings.Weekday,
		"local_retention":        settings.LocalRetention,
		"remote_retention":       settings.RemoteRetention,
		"destination":            settings.Destination,
		"password_configured":    settings.Secrets.RecoveryPassword != "",
		"destination_configured": !settings.Destination.Enabled || destinationSecretsConfigured(settings.Destination, settings.Secrets.Remote),
		"last_success_at":        settings.LastSuccessAt,
		"last_error":             settings.LastError,
	}
}

func destinationSecretsConfigured(destination backup.Destination, secrets backup.RemoteSecrets) bool {
	if !destination.Enabled {
		return true
	}
	if destination.Provider == "s3" {
		return strings.TrimSpace(secrets.AccessKey) != "" && strings.TrimSpace(secrets.SecretKey) != ""
	}
	return strings.TrimSpace(secrets.Username) != "" && strings.TrimSpace(secrets.Password) != ""
}

func controllerBackupRemoteReady(item model.ControllerBackup, settings controllerBackupSettingsState) bool {
	return settings.Destination.Enabled && item.RemoteStatus == "available" && item.RemoteKey != "" && item.RemoteTarget == backup.DestinationID(settings.Destination)
}

func (s *Server) controllerBackups(w http.ResponseWriter, r *http.Request) {
	if !s.backupConfigured || s.backupManager == nil {
		fail(w, errors.New("主控备份目录不可用，请检查 OBOARD_BACKUP_DIR"), http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := s.loadControllerBackupSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		items, err := s.store.ListControllerBackups(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		for i := range items {
			items[i].RemoteReady = controllerBackupRemoteReady(items[i], settings)
		}
		write(w, 200, map[string]any{"settings": publicControllerBackupSettings(settings), "backups": items})
	case http.MethodPost:
		var req struct {
			UploadRemote *bool `json:"upload_remote"`
		}
		if !decode(w, r, &req) {
			return
		}
		s.backupMu.Lock()
		defer s.backupMu.Unlock()
		settings, err := s.loadControllerBackupSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		if strings.TrimSpace(settings.Secrets.RecoveryPassword) == "" {
			fail(w, errors.New("请先设置备份恢复密码"), http.StatusConflict)
			return
		}
		uploadRemote := true
		if req.UploadRemote != nil {
			uploadRemote = *req.UploadRemote
		}
		item, err := s.createControllerDataBackup(r.Context(), settings, "manual", uploadRemote, false)
		if err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "controller-backup", item.ID)
		write(w, http.StatusCreated, map[string]any{"backup": item})
	default:
		method(w)
	}
}

func (s *Server) controllerBackupSettings(w http.ResponseWriter, r *http.Request) {
	if !s.backupConfigured || s.backupManager == nil {
		fail(w, errors.New("主控备份目录不可用，请检查 OBOARD_BACKUP_DIR"), http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet {
		settings, err := s.loadControllerBackupSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"settings": publicControllerBackupSettings(settings)})
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		method(w)
		return
	}
	var req controllerBackupSettingsRequest
	if !decode(w, r, &req) {
		return
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	state, err := s.loadControllerBackupSettings(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	if req.Enabled != nil {
		state.Enabled = *req.Enabled
	}
	if req.Schedule != "" {
		state.Schedule = strings.ToLower(strings.TrimSpace(req.Schedule))
	}
	if state.Schedule != "daily" && state.Schedule != "weekly" {
		fail(w, errors.New("备份计划只能选择每日或每周"), 400)
		return
	}
	if req.Time != "" {
		state.Time = strings.TrimSpace(req.Time)
	}
	if _, err := time.Parse("15:04", state.Time); err != nil {
		fail(w, errors.New("备份时间格式应为 HH:MM"), 400)
		return
	}
	if req.Weekday >= 0 && req.Weekday <= 6 {
		state.Weekday = req.Weekday
	}
	if req.LocalRetention != 0 {
		state.LocalRetention = req.LocalRetention
	}
	if req.RemoteRetention != 0 {
		state.RemoteRetention = req.RemoteRetention
	}
	if state.LocalRetention < 1 || state.LocalRetention > 100 || state.RemoteRetention < 1 || state.RemoteRetention > 365 {
		fail(w, errors.New("本地保留数量应为 1 至 100，远端保留数量应为 1 至 365"), 400)
		return
	}
	state.Destination = req.Destination
	state.Destination.Provider = strings.ToLower(strings.TrimSpace(state.Destination.Provider))
	if err := backup.ValidateDestination(state.Destination); err != nil {
		fail(w, fmt.Errorf("备份目标无效：%w", err), 400)
		return
	}
	if strings.TrimSpace(req.RecoveryPassword) != "" {
		if len([]rune(req.RecoveryPassword)) < 12 {
			fail(w, errors.New("恢复密码至少需要 12 个字符"), 400)
			return
		}
		state.Secrets.RecoveryPassword = req.RecoveryPassword
	}
	if req.S3AccessKey != "" {
		state.Secrets.Remote.AccessKey = req.S3AccessKey
	}
	if req.S3SecretKey != "" {
		state.Secrets.Remote.SecretKey = req.S3SecretKey
	}
	if req.WebDAVUsername != "" {
		state.Secrets.Remote.Username = req.WebDAVUsername
	}
	if req.WebDAVPassword != "" {
		state.Secrets.Remote.Password = req.WebDAVPassword
	}
	if strings.TrimSpace(state.Secrets.RecoveryPassword) == "" {
		fail(w, errors.New("请设置备份恢复密码后再保存"), http.StatusConflict)
		return
	}
	if state.Destination.Enabled && !destinationSecretsConfigured(state.Destination, state.Secrets.Remote) {
		fail(w, errors.New("请填写已启用备份目标的访问凭据"), http.StatusConflict)
		return
	}
	destinationData, err := json.Marshal(state.Destination)
	if err != nil {
		fail(w, err, 500)
		return
	}
	secretData, err := json.Marshal(state.Secrets)
	if err != nil {
		fail(w, err, 500)
		return
	}
	wrapped, err := security.EncryptSecret(s.sessionSecret, controllerBackupSecretsSetting, string(secretData))
	if err != nil {
		fail(w, err, 500)
		return
	}
	values := map[string]string{
		controllerBackupEnabledSetting:         strconv.FormatBool(state.Enabled),
		controllerBackupScheduleSetting:        state.Schedule,
		controllerBackupTimeSetting:            state.Time,
		controllerBackupWeekdaySetting:         strconv.Itoa(state.Weekday),
		controllerBackupLocalRetentionSetting:  strconv.Itoa(state.LocalRetention),
		controllerBackupRemoteRetentionSetting: strconv.Itoa(state.RemoteRetention),
		controllerBackupDestinationSetting:     string(destinationData),
		controllerBackupSecretsSetting:         wrapped,
	}
	if err := s.store.SetSettings(r.Context(), values); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "update", "controller-backup", "settings")
	write(w, 200, map[string]any{"settings": publicControllerBackupSettings(state)})
}

func (s *Server) controllerBackupTestDestination(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	state, err := s.loadControllerBackupSettings(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	var req controllerBackupSettingsRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Destination.Provider) != "" || strings.TrimSpace(req.Destination.Endpoint) != "" {
		state.Destination = req.Destination
		state.Destination.Provider = strings.ToLower(strings.TrimSpace(state.Destination.Provider))
	}
	if req.S3AccessKey != "" {
		state.Secrets.Remote.AccessKey = req.S3AccessKey
	}
	if req.S3SecretKey != "" {
		state.Secrets.Remote.SecretKey = req.S3SecretKey
	}
	if req.WebDAVUsername != "" {
		state.Secrets.Remote.Username = req.WebDAVUsername
	}
	if req.WebDAVPassword != "" {
		state.Secrets.Remote.Password = req.WebDAVPassword
	}
	if !state.Destination.Enabled {
		fail(w, errors.New("请先启用第三方备份目标"), http.StatusConflict)
		return
	}
	if !destinationSecretsConfigured(state.Destination, state.Secrets.Remote) {
		fail(w, errors.New("请填写第三方备份目标的访问凭据"), http.StatusConflict)
		return
	}
	if err := backup.TestDestination(r.Context(), nil, state.Destination, state.Secrets.Remote); err != nil {
		fail(w, fmt.Errorf("第三方备份目标无法连接：%w", err), http.StatusBadGateway)
		return
	}
	auditReq(s, r, "test", "controller-backup", state.Destination.Provider)
	write(w, 200, map[string]any{"message": "第三方备份目标连接成功"})
}

func (s *Server) controllerBackupUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.backupConfigured || s.backupManager == nil {
		fail(w, errors.New("主控备份目录不可用，请检查 OBOARD_BACKUP_DIR"), http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(4<<30)+1)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, errors.New("备份文件无法读取或超过允许大小"), 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("backup")
	if err != nil {
		fail(w, errors.New("请选择备份文件"), 400)
		return
	}
	defer file.Close()
	password := r.FormValue("recovery_password")
	if strings.TrimSpace(password) == "" {
		fail(w, errors.New("请输入该备份的恢复密码"), 400)
		return
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	path, size, err := s.backupManager.SaveUpload(io.Reader(file))
	if err != nil {
		fail(w, err, 400)
		return
	}
	manifest, err := s.backupManager.Validate(path, password)
	if err != nil {
		_ = os.Remove(path)
		fail(w, err, 400)
		return
	}
	if err := backup.CheckCompatibility(manifest.SourceVersion, version.Version); err != nil {
		_ = os.Remove(path)
		fail(w, fmt.Errorf("该备份与当前主控版本不兼容：%w", err), http.StatusConflict)
		return
	}
	item := &model.ControllerBackup{ID: manifest.ID, Name: filepath.Base(path), Origin: "uploaded", LocalPath: path, LocalStatus: "available", RemoteStatus: "disabled", SizeBytes: size, SourceVersion: manifest.SourceVersion, FormatVersion: manifest.FormatVersion}
	if err := s.store.CreateControllerBackup(r.Context(), item); err != nil {
		_ = os.Remove(path)
		fail(w, err, 500)
		return
	}
	state, _ := s.loadControllerBackupSettings(r.Context())
	_ = s.enforceControllerBackupRetention(r.Context(), state)
	auditReq(s, r, "upload", "controller-backup", item.ID)
	write(w, http.StatusCreated, map[string]any{"backup": item, "inspection": backup.Inspection{Manifest: manifest}})
}

func (s *Server) controllerBackupSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/backups/")
	if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
		notFound(w, r)
		return
	}
	item, err := s.store.GetControllerBackup(r.Context(), parts[0])
	if err != nil {
		fail(w, errors.New("备份不存在"), http.StatusNotFound)
		return
	}
	if len(parts) == 2 && parts[1] == "download" {
		s.controllerBackupDownload(w, r, item)
		return
	}
	if len(parts) == 2 && parts[1] == "restore" {
		s.controllerBackupRestore(w, r, item)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.controllerBackupDelete(w, r, item)
		return
	}
	method(w)
}

func (s *Server) controllerBackupDownload(w http.ResponseWriter, r *http.Request, item *model.ControllerBackup) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if s.backupManager == nil {
		fail(w, errors.New("主控备份目录不可用"), http.StatusServiceUnavailable)
		return
	}
	s.backupMu.Lock()
	settings, err := s.loadControllerBackupSettings(r.Context())
	if err == nil {
		err = s.ensureControllerBackupLocal(r.Context(), settings, item)
	}
	if err != nil {
		s.backupMu.Unlock()
		fail(w, err, http.StatusBadGateway)
		return
	}
	file, err := os.Open(item.LocalPath)
	if err != nil {
		s.backupMu.Unlock()
		fail(w, errors.New("本地备份文件不可用"), http.StatusNotFound)
		return
	}
	info, err := file.Stat()
	s.backupMu.Unlock()
	if err != nil {
		_ = file.Close()
		fail(w, err, 500)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", item.Name))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, item.Name, info.ModTime(), file)
}

func (s *Server) controllerBackupDelete(w http.ResponseWriter, r *http.Request, item *model.ControllerBackup) {
	if s.backupManager == nil {
		fail(w, errors.New("主控备份目录不可用"), http.StatusServiceUnavailable)
		return
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	state, err := s.loadControllerBackupSettings(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	remoteDeleted := false
	if controllerBackupRemoteReady(*item, state) {
		if err := backup.Delete(r.Context(), nil, state.Destination, state.Secrets.Remote, item.RemoteKey); err != nil {
			fail(w, fmt.Errorf("删除远端备份失败：%w", err), http.StatusBadGateway)
			return
		}
		remoteDeleted = true
	}
	if item.LocalPath != "" && pathContained(s.backupManager.Root(), item.LocalPath) {
		if err := os.Remove(item.LocalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			fail(w, err, 500)
			return
		}
	}
	if err := s.store.DeleteControllerBackup(r.Context(), item.ID); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "delete", "controller-backup", item.ID)
	message := "本地记录已删除"
	if remoteDeleted {
		message = "本地和第三方备份已删除"
	} else if item.RemoteStatus == "available" {
		message = "本地记录已删除；第三方目标已变更，请在旧存储中手动删除远端文件"
	}
	write(w, 200, map[string]any{"message": message})
}

func (s *Server) controllerBackupRestore(w http.ResponseWriter, r *http.Request, item *model.ControllerBackup) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if s.backupManager == nil {
		fail(w, errors.New("主控备份目录不可用"), http.StatusServiceUnavailable)
		return
	}
	var req struct {
		RecoveryPassword string `json:"recovery_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RecoveryPassword) == "" {
		fail(w, errors.New("请输入备份恢复密码"), 400)
		return
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	state, err := s.loadControllerBackupSettings(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	if err := s.ensureControllerBackupLocal(r.Context(), state, item); err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	protectionPassword := state.Secrets.RecoveryPassword
	if protectionPassword == "" {
		protectionPassword = req.RecoveryPassword
	}
	if _, err := s.createControllerDataBackupWithPassword(r.Context(), state, protectionPassword, "pre_restore", false, true); err != nil {
		fail(w, fmt.Errorf("恢复前保护备份创建失败：%w", err), 500)
		return
	}
	staged, err := s.backupManager.StageRestore(r.Context(), item.LocalPath, req.RecoveryPassword, version.Version)
	if err != nil {
		fail(w, fmt.Errorf("备份无法恢复：%w", err), http.StatusConflict)
		return
	}
	auditReq(s, r, "restore", "controller-backup", staged.Manifest.ID)
	write(w, http.StatusAccepted, map[string]any{"message": "备份已验证，主控即将重启并恢复数据", "backup": item})
	if s.backupRestart != nil {
		go func() {
			time.Sleep(250 * time.Millisecond)
			s.backupRestart()
		}()
	}
}

func (s *Server) createControllerDataBackup(ctx context.Context, settings controllerBackupSettingsState, origin string, uploadRemote bool, protected bool) (*model.ControllerBackup, error) {
	return s.createControllerDataBackupWithPassword(ctx, settings, settings.Secrets.RecoveryPassword, origin, uploadRemote, protected)
}

func (s *Server) createControllerDataBackupWithPassword(ctx context.Context, settings controllerBackupSettingsState, password, origin string, uploadRemote bool, protected bool) (*model.ControllerBackup, error) {
	if s.backupManager == nil {
		return nil, errors.New("主控备份目录不可用")
	}
	created, err := s.backupManager.Create(ctx, password)
	if err != nil {
		return nil, err
	}
	item := &model.ControllerBackup{ID: created.Manifest.ID, Name: filepath.Base(created.Path), Origin: origin, LocalPath: created.Path, LocalStatus: "available", RemoteStatus: "disabled", SizeBytes: created.Size, SourceVersion: created.Manifest.SourceVersion, FormatVersion: created.Manifest.FormatVersion, Protected: protected}
	if err := s.store.CreateControllerBackup(ctx, item); err != nil {
		_ = os.Remove(created.Path)
		return nil, err
	}
	if uploadRemote && settings.Destination.Enabled {
		key := backup.ObjectKey(settings.Destination, item.Name)
		target := backup.DestinationID(settings.Destination)
		item.RemoteKey, item.RemoteTarget = key, target
		if err := backup.Upload(ctx, nil, settings.Destination, settings.Secrets.Remote, key, item.LocalPath); err != nil {
			item.RemoteStatus, item.RemoteError = "failed", err.Error()
			_ = s.store.UpdateControllerBackupRemote(ctx, item.ID, key, target, item.RemoteStatus, item.RemoteError)
		} else {
			item.RemoteStatus, item.RemoteReady = "available", true
			_ = s.store.UpdateControllerBackupRemote(ctx, item.ID, key, target, item.RemoteStatus, "")
		}
	}
	if err := s.enforceControllerBackupRetention(ctx, settings); err != nil {
		return item, err
	}
	return item, nil
}

func (s *Server) enforceControllerBackupRetention(ctx context.Context, settings controllerBackupSettingsState) error {
	items, err := s.store.ListControllerBackups(ctx)
	if err != nil {
		return err
	}
	localKept := 0
	for _, item := range items {
		if item.LocalPath == "" || item.Protected {
			continue
		}
		localKept++
		if localKept <= settings.LocalRetention {
			continue
		}
		if s.backupManager != nil && pathContained(s.backupManager.Root(), item.LocalPath) {
			if err := os.Remove(item.LocalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := s.store.ExpireControllerBackupLocal(ctx, item.ID); err != nil {
			return err
		}
	}
	remoteKept := 0
	currentTarget := backup.DestinationID(settings.Destination)
	for _, item := range items {
		if item.RemoteStatus != "available" || item.RemoteKey == "" || item.RemoteTarget != currentTarget || item.Protected || !settings.Destination.Enabled {
			continue
		}
		remoteKept++
		if remoteKept <= settings.RemoteRetention {
			continue
		}
		if settings.Destination.Enabled {
			if err := backup.Delete(ctx, nil, settings.Destination, settings.Secrets.Remote, item.RemoteKey); err != nil {
				return err
			}
		}
		if err := s.store.ExpireControllerBackupRemote(ctx, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) ensureControllerBackupLocal(ctx context.Context, settings controllerBackupSettingsState, item *model.ControllerBackup) error {
	if s.backupManager == nil {
		return errors.New("主控备份目录不可用")
	}
	if item.LocalPath != "" && pathContained(s.backupManager.Root(), item.LocalPath) {
		if info, err := os.Stat(item.LocalPath); err == nil && info.Mode().IsRegular() {
			return nil
		}
	}
	if !controllerBackupRemoteReady(*item, settings) {
		return errors.New("本地备份已不可用，且当前第三方目标无法取回该副本")
	}
	destination := filepath.Join(s.backupManager.Root(), "oboard-remote-"+item.ID+".obk")
	_ = os.Remove(destination)
	size, err := backup.Download(ctx, nil, settings.Destination, settings.Secrets.Remote, item.RemoteKey, destination)
	if err != nil {
		return fmt.Errorf("从第三方目标取回备份失败：%w", err)
	}
	manifest, err := s.backupManager.Verify(destination)
	if err != nil || manifest.ID != item.ID {
		_ = os.Remove(destination)
		if err != nil {
			return fmt.Errorf("第三方备份完整性校验失败：%w", err)
		}
		return errors.New("第三方备份与本地记录不匹配")
	}
	if err := s.store.UpdateControllerBackupLocal(ctx, item.ID, destination, "available", size); err != nil {
		_ = os.Remove(destination)
		return err
	}
	item.LocalPath, item.LocalStatus, item.SizeBytes = destination, "available", size
	return nil
}

func (s *Server) reconcileControllerBackupFiles(ctx context.Context) {
	if s.backupManager == nil {
		return
	}
	items, err := s.store.ListControllerBackups(ctx)
	if err != nil {
		return
	}
	for _, item := range items {
		if item.LocalPath == "" {
			continue
		}
		info, statErr := os.Stat(item.LocalPath)
		if !pathContained(s.backupManager.Root(), item.LocalPath) || statErr != nil || !info.Mode().IsRegular() {
			_ = s.store.ExpireControllerBackupLocal(ctx, item.ID)
		}
	}
}

func (s *Server) reconcileRestoredDeployment(ctx context.Context) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil || !settingBool(settings, controllerBackupRestoreReconcileSetting, false) {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/deployments/apply", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userKey, &model.User{Role: model.RoleAdmin, Username: "restore"}))
	result := httptest.NewRecorder()
	s.applyDeployment(result, req)
	if result.Code != http.StatusAccepted {
		_ = s.store.SetSetting(ctx, controllerBackupLastErrorSetting, "恢复后的节点配置下发失败")
		return
	}
	_ = s.store.SetSettings(ctx, map[string]string{controllerBackupRestoreReconcileSetting: "false", controllerBackupLastErrorSetting: ""})
}
