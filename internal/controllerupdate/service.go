package controllerupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/version"
)

type ServiceConfig struct {
	SocketPath         string
	BinaryEnvPath      string
	StatePath          string
	RuntimeStatePath   string
	ControllerBinary   string
	UpdaterBinary      string
	WebRoot            string
	DownloadsRoot      string
	WorkRoot           string
	HTTPClient         *http.Client
	HealthClient       *http.Client
	HealthTimeout      time.Duration
	HealthPollInterval time.Duration
	ReadyWindow        time.Duration
	InstallGracePeriod time.Duration
	Wait               func(context.Context, time.Duration) error
	RunCommand         func(context.Context, string, ...string) error
}

type Service struct {
	config        ServiceConfig
	mu            sync.Mutex
	status        Status
	installCancel context.CancelFunc
	installRun    uint64
}

func DefaultServiceConfig() ServiceConfig {
	installDir := defaultInstallDir()
	dataDir := filepath.Join(installDir, "data")
	return ServiceConfig{
		SocketPath:         DefaultSocketPath,
		BinaryEnvPath:      filepath.Join(installDir, "config/controller.env"),
		StatePath:          filepath.Join(dataDir, "controller-update/status.json"),
		RuntimeStatePath:   filepath.Join(dataDir, RuntimeStateName),
		ControllerBinary:   filepath.Join(installDir, "oboard-controller"),
		UpdaterBinary:      filepath.Join(installDir, "oboard-controller-updater"),
		WebRoot:            filepath.Join(installDir, "web/dist"),
		DownloadsRoot:      filepath.Join(installDir, "downloads"),
		WorkRoot:           filepath.Join(dataDir, "controller-update"),
		HTTPClient:         &http.Client{Timeout: 2 * time.Minute},
		HealthClient:       &http.Client{Timeout: 3 * time.Second},
		HealthTimeout:      90 * time.Second,
		HealthPollInterval: time.Second,
		ReadyWindow:        4 * time.Second,
		InstallGracePeriod: 4 * time.Second,
		Wait:               waitForContext,
		RunCommand: func(ctx context.Context, name string, args ...string) error {
			switch name {
			case "systemctl", "rc-service":
			default:
				return errors.New("command is not allowlisted")
			}
			// #nosec G204 -- name is allowlisted above and arguments come from fixed updater operations.
			command := exec.CommandContext(ctx, name, args...)
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			return command.Run()
		},
	}
}

func defaultInstallDir() string {
	if value, ok := normalizeInstallDir(os.Getenv("OBOARD_INSTALL_DIR")); ok {
		return value
	}
	return "/opt/oboard"
}

func normalizeInstallDir(value string) (string, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, "/")
	if value == "" || value == "." || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", false
	}
	switch value {
	case "/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/tmp", "/usr", "/usr/bin", "/usr/lib", "/usr/lib64", "/usr/sbin", "/usr/local", "/usr/local/bin", "/usr/local/sbin", "/var", "/var/lib", "/opt", "/data", "/srv":
		return "", false
	}
	for _, prefix := range []string{"/bin/", "/boot/", "/dev/", "/etc/", "/home/", "/lib/", "/lib64/", "/proc/", "/root/", "/run/", "/sbin/", "/sys/", "/tmp/", "/usr/bin/", "/usr/lib/", "/usr/lib64/", "/usr/sbin/", "/usr/local/bin/", "/usr/local/sbin/"} {
		if strings.HasPrefix(value, prefix) {
			return "", false
		}
	}
	for _, char := range value {
		if char == '/' || char == '.' || char == '_' || char == '-' ||
			(char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		return "", false
	}
	return value, true
}

func NewService(config ServiceConfig) *Service {
	defaults := DefaultServiceConfig()
	if config.SocketPath == "" {
		config.SocketPath = defaults.SocketPath
	}
	if config.BinaryEnvPath == "" {
		config.BinaryEnvPath = defaults.BinaryEnvPath
	}
	if config.StatePath == "" {
		config.StatePath = defaults.StatePath
	}
	if config.RuntimeStatePath == "" {
		config.RuntimeStatePath = defaults.RuntimeStatePath
	}
	if config.ControllerBinary == "" {
		config.ControllerBinary = defaults.ControllerBinary
	}
	if config.UpdaterBinary == "" {
		config.UpdaterBinary = defaults.UpdaterBinary
	}
	if config.WebRoot == "" {
		config.WebRoot = defaults.WebRoot
	}
	if config.DownloadsRoot == "" {
		config.DownloadsRoot = defaults.DownloadsRoot
	}
	if config.WorkRoot == "" {
		config.WorkRoot = defaults.WorkRoot
	}
	if config.HTTPClient == nil {
		config.HTTPClient = defaults.HTTPClient
	}
	if config.HealthClient == nil {
		config.HealthClient = defaults.HealthClient
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = defaults.HealthTimeout
	}
	if config.HealthPollInterval <= 0 {
		config.HealthPollInterval = defaults.HealthPollInterval
	}
	if config.ReadyWindow <= 0 {
		config.ReadyWindow = defaults.ReadyWindow
	}
	if config.InstallGracePeriod <= 0 {
		config.InstallGracePeriod = defaults.InstallGracePeriod
	}
	if config.Wait == nil {
		config.Wait = defaults.Wait
	}
	if config.RunCommand == nil {
		config.RunCommand = defaults.RunCommand
	}
	s := &Service{config: config, status: Status{State: "idle"}}
	s.loadStatus()
	if isTransientUpdateState(s.status.State) {
		s.status.State = "idle"
		s.status.UpdateAvailable = false
		s.status.CanCancel = false
		s.status.LastError = ""
		_ = s.saveStatus(s.status)
	}
	return s
}

func (s *Service) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.config.SocketPath), 0o750); err != nil {
		return err
	}
	// #nosec G302 -- the dedicated directory must be traversable by the oboard IPC group.
	if err := os.Chmod(filepath.Dir(s.config.SocketPath), 0o750); err != nil {
		return err
	}
	if group, err := user.LookupGroup("oboard"); err == nil {
		if gid, parseErr := strconv.Atoi(group.Gid); parseErr == nil {
			_ = os.Chown(filepath.Dir(s.config.SocketPath), 0, gid)
		}
	}
	if err := os.Remove(s.config.SocketPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", s.config.SocketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	// #nosec G302 -- 0660 root:oboard is the updater's intended IPC boundary.
	if err := os.Chmod(s.config.SocketPath, 0o660); err != nil {
		return err
	}
	if group, err := user.LookupGroup("oboard"); err == nil {
		if gid, parseErr := strconv.Atoi(group.Gid); parseErr == nil {
			_ = os.Chown(s.config.SocketPath, 0, gid)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/check", s.handleCheck)
	mux.HandleFunc("/v1/install", s.handleInstall)
	mux.HandleFunc("/v1/cancel", s.handleCancel)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = server.Close() }()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.decorateStatus(s.status)
	if status != s.status {
		if isActiveUpdateState(s.status.State) && !isActiveUpdateState(status.State) && s.installCancel != nil {
			s.installCancel()
		}
		_ = s.saveStatus(status)
	}
	writeStatus(w, http.StatusOK, status)
}

func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || requestHasBody(r) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	status, err := s.check(r.Context())
	if err != nil {
		writeStatus(w, http.StatusBadGateway, status)
		return
	}
	writeStatus(w, http.StatusOK, status)
}

func (s *Service) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || requestHasBody(r) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	status := s.decorateStatus(s.status)
	if status.Channel == "" {
		s.mu.Unlock()
		writeStatus(w, http.StatusConflict, status)
		return
	}
	if status.Channel == "pinned" {
		s.mu.Unlock()
		status.LastError = "当前主控使用固定版本，不能直接更新。"
		writeStatus(w, http.StatusConflict, status)
		return
	}
	if isActiveUpdateState(status.State) {
		s.mu.Unlock()
		writeStatus(w, http.StatusOK, status)
		return
	}
	if status.State == "checking" {
		s.mu.Unlock()
		status.LastError = "正在检查更新，请稍后再试。"
		writeStatus(w, http.StatusConflict, status)
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	status.State, status.LastError, status.CanCancel = "downloading", "", true
	if err := s.saveStatus(status); err != nil {
		cancel()
		s.mu.Unlock()
		status.LastError = err.Error()
		writeStatus(w, http.StatusInternalServerError, status)
		return
	}
	s.installCancel = cancel
	s.installRun++
	runID := s.installRun
	s.mu.Unlock()
	go func() {
		_, _ = s.install(runCtx)
		s.mu.Lock()
		if s.installRun == runID {
			s.installCancel = nil
		}
		s.mu.Unlock()
	}()
	writeStatus(w, http.StatusOK, status)
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || requestHasBody(r) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.decorateStatus(s.status)
	if (status.State != "downloading" && status.State != "ready") || !status.CanCancel || s.installCancel == nil {
		status.CanCancel = false
		if status.State == "installing" {
			status.LastError = "新版本已经开始安装，现在不能中断。"
		} else {
			status.LastError = "当前没有可以中断的更新。"
		}
		writeStatus(w, http.StatusConflict, status)
		return
	}
	s.installCancel()
	status.State = "cancelling"
	status.CanCancel = false
	status.LastError = ""
	if err := s.saveStatus(status); err != nil {
		status.LastError = err.Error()
		writeStatus(w, http.StatusInternalServerError, status)
		return
	}
	writeStatus(w, http.StatusOK, status)
}

func requestHasBody(r *http.Request) bool {
	return r.ContentLength != 0 || len(r.TransferEncoding) != 0
}

func (s *Service) check(ctx context.Context) (Status, error) {
	s.mu.Lock()
	status := s.decorateStatus(s.status)
	if status.Channel == "" {
		err := errors.New(status.LastError)
		s.mu.Unlock()
		return status, err
	}
	if isActiveUpdateState(status.State) {
		s.mu.Unlock()
		return status, nil
	}
	if status.Channel == "pinned" {
		status.State, status.UpdateAvailable, status.LastError = "pinned", false, ""
		status.LastCheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
		err := s.saveStatus(status)
		s.mu.Unlock()
		return status, err
	}
	status.State, status.LastError = "checking", ""
	if err := s.saveStatus(status); err != nil {
		s.mu.Unlock()
		return status, err
	}
	s.mu.Unlock()
	release, err := fetchRelease(ctx, s.config.HTTPClient, status.Channel)
	s.mu.Lock()
	defer s.mu.Unlock()
	status.LastCheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		status.State, status.LastError = "failed", err.Error()
		if persistErr := s.saveStatus(status); persistErr != nil {
			return status, errors.Join(err, persistErr)
		}
		return status, err
	}
	status.Available = BuildInfo{Version: release.Manifest.Version, Build: release.Manifest.Build, Commit: release.Manifest.Commit, Date: release.Manifest.Date}
	status.UpdateAvailable = updateAvailable(status.Channel, status.Current, release.Manifest)
	if status.UpdateAvailable {
		status.State = "available"
	} else {
		status.State = "current"
	}
	return status, s.saveStatus(status)
}

func (s *Service) install(ctx context.Context) (Status, error) {
	s.mu.Lock()
	status := s.decorateStatus(s.status)
	if status.Channel == "" {
		err := errors.New(status.LastError)
		s.mu.Unlock()
		return status, err
	}
	if status.Channel == "pinned" {
		s.mu.Unlock()
		return status, errors.New("当前主控使用固定版本，不能直接更新")
	}
	s.mu.Unlock()

	release, err := fetchRelease(ctx, s.config.HTTPClient, status.Channel)
	if err != nil {
		return s.finishInstallError(err)
	}
	available := BuildInfo{Version: release.Manifest.Version, Build: release.Manifest.Build, Commit: release.Manifest.Commit, Date: release.Manifest.Date}
	s.mu.Lock()
	status = s.decorateStatus(s.status)
	status.Available = available
	status.UpdateAvailable = buildInfoUpdateAvailable(status.Channel, status.Current, available)
	if !status.UpdateAvailable {
		status.State, status.CanCancel, status.LastError = "current", false, ""
		err := s.saveStatus(status)
		s.mu.Unlock()
		return status, err
	}
	if ctx.Err() != nil || status.State == "cancelling" {
		s.mu.Unlock()
		return s.finishCancelledInstall()
	}
	status.State, status.CanCancel, status.LastError = "downloading", true, ""
	if err := s.saveStatus(status); err != nil {
		s.mu.Unlock()
		return status, err
	}
	s.mu.Unlock()

	stage, err := s.stageControllerRelease(ctx, release)
	if err != nil {
		return s.finishInstallError(err)
	}
	defer os.RemoveAll(stage)

	status, err = s.prepareInstallation(ctx, available)
	if err != nil || status.State != "installing" {
		return status, err
	}
	if err := s.replaceBinaryProgram(ctx, stage); err != nil {
		return s.finishInstallError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status = s.decorateStatus(s.status)
	status.State, status.UpdateAvailable, status.CanCancel, status.LastError = "installed", false, false, ""
	status.Current = available
	return status, s.saveStatus(status)
}

func (s *Service) prepareInstallation(ctx context.Context, available BuildInfo) (Status, error) {
	s.mu.Lock()
	status := s.decorateStatus(s.status)
	status.Available = available
	status.UpdateAvailable = buildInfoUpdateAvailable(status.Channel, status.Current, available)
	if ctx.Err() != nil || status.State == "cancelling" {
		s.mu.Unlock()
		return s.finishCancelledInstall()
	}
	if !status.UpdateAvailable {
		status.State, status.CanCancel, status.LastError = "current", false, ""
		err := s.saveStatus(status)
		s.mu.Unlock()
		return status, err
	}
	status.State, status.CanCancel, status.LastError = "ready", true, ""
	if err := s.saveStatus(status); err != nil {
		s.mu.Unlock()
		return status, err
	}
	s.mu.Unlock()
	if err := s.config.Wait(ctx, s.config.ReadyWindow); err != nil {
		return s.finishInstallError(err)
	}

	s.mu.Lock()
	status = s.decorateStatus(s.status)
	status.Available = available
	status.UpdateAvailable = buildInfoUpdateAvailable(status.Channel, status.Current, available)
	if ctx.Err() != nil || status.State == "cancelling" {
		s.mu.Unlock()
		return s.finishCancelledInstall()
	}
	if !status.UpdateAvailable {
		status.State, status.CanCancel, status.LastError = "current", false, ""
		err := s.saveStatus(status)
		s.mu.Unlock()
		return status, err
	}
	status.State, status.CanCancel, status.LastError = "installing", false, ""
	if err := s.saveStatus(status); err != nil {
		s.mu.Unlock()
		return status, err
	}
	s.mu.Unlock()
	if err := s.config.Wait(ctx, s.config.InstallGracePeriod); err != nil {
		return s.finishInstallError(err)
	}
	return status, nil
}

func (s *Service) finishInstallError(installErr error) (Status, error) {
	if errors.Is(installErr, context.Canceled) {
		return s.finishCancelledInstall()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.decorateStatus(s.status)
	if status.State == "current" && !status.UpdateAvailable {
		status.CanCancel = false
		status.LastError = ""
		return status, s.saveStatus(status)
	}
	status.State, status.CanCancel, status.LastError = "failed", false, installErr.Error()
	if persistErr := s.saveStatus(status); persistErr != nil {
		return status, errors.Join(installErr, persistErr)
	}
	return status, installErr
}

func (s *Service) finishCancelledInstall() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.decorateStatus(s.status)
	status.CanCancel = false
	status.LastError = ""
	status.UpdateAvailable = buildInfoUpdateAvailable(status.Channel, status.Current, status.Available)
	if status.UpdateAvailable {
		status.State = "cancelled"
	} else if status.Available.Version != "" {
		status.State = "current"
	} else {
		status.State = "idle"
	}
	return status, s.saveStatus(status)
}

func (s *Service) decorateStatus(status Status) Status {
	channel, command, detectionError := s.detectInstallation()
	status.Channel, status.ManualCommand = channel, command
	status.Current = s.currentBuildInfo()
	if detectionError != "" {
		status.State = "failed"
		status.UpdateAvailable = false
		status.LastError = detectionError
		status.CanCancel = false
		return status
	}
	if channel == "pinned" && !isActiveUpdateState(status.State) {
		status.State = "pinned"
		status.UpdateAvailable = false
		status.CanCancel = false
	} else if status.Available.Version != "" && !buildInfoUpdateAvailable(channel, status.Current, status.Available) {
		status.State = "current"
		status.UpdateAvailable = false
		status.CanCancel = false
		status.LastError = ""
	}
	if status.State == "" {
		status.State = "idle"
	}
	if status.State != "downloading" && status.State != "ready" {
		status.CanCancel = false
	}
	return status
}

func buildInfoUpdateAvailable(channel string, current, available BuildInfo) bool {
	if strings.TrimSpace(available.Version) == "" {
		return false
	}
	return updateAvailable(channel, current, Manifest{Version: available.Version, Build: available.Build, Commit: available.Commit, Date: available.Date})
}

func isActiveUpdateState(state string) bool {
	return state == "downloading" || state == "ready" || state == "installing" || state == "cancelling"
}

func isTransientUpdateState(state string) bool {
	return state == "checking" || isActiveUpdateState(state)
}

func (s *Service) detectInstallation() (string, string, string) {
	info, err := os.Stat(s.config.ControllerBinary)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", "未检测到二进制主控安装。"
	}
	binaryValues, _ := readEnv(s.config.BinaryEnvPath)
	channel := strings.ToLower(strings.TrimSpace(binaryValues["OBOARD_UPDATE_CHANNEL"]))
	if channel == "pinned" {
		return "pinned", "sed -i 's/^OBOARD_UPDATE_CHANNEL=.*/OBOARD_UPDATE_CHANNEL=stable/' " + s.config.BinaryEnvPath + " && (systemctl restart oboard-controller-updater || rc-service oboard-controller-updater restart)", ""
	}
	if channel != "dev" && channel != "stable" {
		if version.IsDev() {
			channel = "dev"
		} else {
			channel = "stable"
		}
	}
	return channel, "", ""
}

func (s *Service) currentBuildInfo() BuildInfo {
	info := BuildInfo{Version: version.Version, Build: version.Build, Commit: version.Commit, Date: version.Date}
	for _, url := range s.healthURLs("/api/v1/version") {
		resp, err := s.config.HealthClient.Get(url)
		if err != nil {
			continue
		}
		var remote struct {
			Version string `json:"version"`
			Build   string `json:"build"`
			Commit  string `json:"commit"`
			BuiltAt string `json:"built_at"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&remote)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK && decodeErr == nil && remote.Version != "" {
			return BuildInfo{Version: remote.Version, Build: remote.Build, Commit: remote.Commit, Date: remote.BuiltAt}
		}
	}
	return info
}

func readEnv(path string) (map[string]string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			result[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return result, nil
}

func writeStatus(w http.ResponseWriter, code int, status Status) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Service) loadStatus() {
	data, err := os.ReadFile(s.config.StatePath)
	if err == nil {
		_ = json.Unmarshal(data, &s.status)
	}
}

func (s *Service) saveStatus(status Status) error {
	s.status = status
	if err := os.MkdirAll(filepath.Dir(s.config.StatePath), 0o700); err != nil {
		return fmt.Errorf("persist controller updater status: %w", err)
	}
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("persist controller updater status: %w", err)
	}
	temp := s.config.StatePath + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("persist controller updater status: %w", err)
	}
	if err := os.Rename(temp, s.config.StatePath); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("persist controller updater status: %w", err)
	}
	return nil
}

func (s *Service) stageControllerRelease(ctx context.Context, release remoteRelease) (string, error) {
	artifact, err := selectArtifact(release.Manifest, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.config.WorkRoot, 0o700); err != nil {
		return "", err
	}
	archivePath := filepath.Join(s.config.WorkRoot, "controller.tar.gz")
	workRoot, err := os.OpenRoot(s.config.WorkRoot)
	if err != nil {
		return "", err
	}
	defer workRoot.Close()
	file, err := workRoot.OpenFile("controller.tar.gz.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.Assets[artifact.Name], nil)
	if err != nil {
		_ = file.Close()
		return "", err
	}
	resp, err := s.config.HTTPClient.Do(req)
	if err != nil {
		_ = file.Close()
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		_ = file.Close()
		return "", fmt.Errorf("download controller package: HTTP %d", resp.StatusCode)
	}
	err = verifyDownload(resp.Body, file, artifact)
	_ = resp.Body.Close()
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := workRoot.Rename("controller.tar.gz.tmp", "controller.tar.gz"); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(s.config.WorkRoot, "stage-")
	if err != nil {
		return "", err
	}
	if err := extractControllerArchive(archivePath, stage); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	if err := validateStagedBuild(ctx, filepath.Join(stage, "bin/oboard-controller"), release.Manifest); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	return stage, nil
}

func validateStagedBuild(ctx context.Context, binary string, manifest Manifest) error {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, binary, "-version-json").Output()
	if err != nil {
		return fmt.Errorf("read staged Controller metadata: %w", err)
	}
	var build BuildInfo
	if err := json.Unmarshal(output, &build); err != nil {
		return fmt.Errorf("decode staged Controller metadata: %w", err)
	}
	if build.Version != manifest.Version || build.Build != manifest.Build || build.Commit != manifest.Commit || build.Date != manifest.Date {
		return errors.New("staged Controller metadata does not match the release manifest")
	}
	return nil
}

func extractControllerArchive(archivePath, stage string) error {
	archiveRoot, err := os.OpenRoot(filepath.Dir(archivePath))
	if err != nil {
		return err
	}
	defer archiveRoot.Close()
	file, err := archiveRoot.Open(filepath.Base(archivePath))
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	stageRoot, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer stageRoot.Close()
	reader := tar.NewReader(gz)
	var entries int
	var extracted int64
	allowed := func(path string) bool {
		return path == "bin/oboard-controller" || path == "bin/oboard-controller-updater" || strings.HasPrefix(path, "web/dist/") || strings.HasPrefix(path, "downloads/")
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > 10000 {
			return errors.New("controller archive contains too many entries")
		}
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(header.Name)), "./")
		if name == "." || header.Typeflag == tar.TypeDir {
			continue
		}
		if !allowed(name) || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 512<<20 {
			return fmt.Errorf("unsafe controller archive entry %q", header.Name)
		}
		extracted += header.Size
		if extracted > 2<<30 {
			return errors.New("controller archive expands beyond the allowed size")
		}
		target := filepath.FromSlash(name)
		if err := stageRoot.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		out, err := stageRoot.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(out, io.LimitReader(reader, header.Size+1))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != header.Size {
			return fmt.Errorf("truncated controller archive entry %q", name)
		}
	}
	for _, required := range []string{"bin/oboard-controller", "bin/oboard-controller-updater", "web/dist/index.html"} {
		if info, err := stageRoot.Stat(filepath.FromSlash(required)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("controller package is missing %s", required)
		}
	}
	return nil
}

func (s *Service) replaceBinaryProgram(ctx context.Context, stage string) error {
	type target struct{ source, destination string }
	targets := []target{{filepath.Join(stage, "bin/oboard-controller"), s.config.ControllerBinary}, {filepath.Join(stage, "bin/oboard-controller-updater"), s.config.UpdaterBinary}, {filepath.Join(stage, "web/dist"), s.config.WebRoot}, {filepath.Join(stage, "downloads"), s.config.DownloadsRoot}}
	rollback := []func(){}
	runRollback := func() {
		for i := len(rollback) - 1; i >= 0; i-- {
			rollback[i]()
		}
	}
	for index, item := range targets {
		if _, err := os.Stat(item.source); os.IsNotExist(err) && index == 3 {
			continue
		} else if err != nil {
			runRollback()
			return err
		}
		backup := item.destination + ".update-backup"
		pending := item.destination + ".update-new"
		_ = os.RemoveAll(backup)
		_ = os.RemoveAll(pending)
		if err := copyTree(item.source, pending); err != nil {
			_ = os.RemoveAll(pending)
			runRollback()
			return err
		}
		if _, err := os.Stat(item.destination); err == nil {
			if err := os.Rename(item.destination, backup); err != nil {
				_ = os.RemoveAll(pending)
				runRollback()
				return err
			}
			rollback = append(rollback, func() { _ = os.RemoveAll(item.destination); _ = os.Rename(backup, item.destination) })
		} else if os.IsNotExist(err) {
			rollback = append(rollback, func() { _ = os.RemoveAll(item.destination) })
		} else {
			_ = os.RemoveAll(pending)
			runRollback()
			return err
		}
		if err := os.Rename(pending, item.destination); err != nil {
			runRollback()
			_ = os.RemoveAll(pending)
			return err
		}
	}
	if err := s.restartAndWait(ctx); err != nil {
		runRollback()
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if rollbackErr := s.restartAndWait(rollbackCtx); rollbackErr != nil {
			return fmt.Errorf("安装新版本后主控未恢复可用（%v），恢复原版本后仍未恢复：%w", err, rollbackErr)
		}
		return fmt.Errorf("安装新版本后主控未恢复可用，已恢复原版本：%w", err)
	}
	for _, item := range targets {
		_ = os.RemoveAll(item.destination + ".update-backup")
	}
	return nil
}

func copyTree(source, destination string) error {
	sourceParent, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer sourceParent.Close()
	info, err := sourceParent.Lstat(filepath.Base(source))
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("staged program asset must not be a symlink")
	}
	if info.Mode().IsRegular() {
		destinationRoot, err := os.OpenRoot(filepath.Dir(destination))
		if err != nil {
			return err
		}
		defer destinationRoot.Close()
		return copyRootFile(sourceParent, filepath.Base(source), destinationRoot, filepath.Base(destination), info.Mode().Perm())
	}
	if !info.IsDir() {
		return errors.New("staged program asset must be a regular file or directory")
	}
	destinationParent, err := os.OpenRoot(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer destinationParent.Close()
	// #nosec G301 -- Web/download trees must be readable by the unprivileged Controller.
	if err := destinationParent.Mkdir(filepath.Base(destination), 0o755); err != nil {
		return err
	}
	sourceRoot, err := sourceParent.OpenRoot(filepath.Base(source))
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	destinationRoot, err := destinationParent.OpenRoot(filepath.Base(destination))
	if err != nil {
		return err
	}
	defer destinationRoot.Close()
	return fs.WalkDir(sourceRoot.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			// #nosec G301 -- Web/download trees must be readable by the Controller.
			return destinationRoot.MkdirAll(path, 0o755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported staged file %s", path)
		}
		return copyRootFile(sourceRoot, path, destinationRoot, path, info.Mode().Perm())
	})
}

func copyRootFile(sourceRoot *os.Root, source string, destinationRoot *os.Root, destination string, mode os.FileMode) error {
	input, err := sourceRoot.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := destinationRoot.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (s *Service) restartAndWait(ctx context.Context) error {
	if err := s.restartController(ctx); err != nil {
		return err
	}
	return s.waitHealth(ctx)
}

func (s *Service) restartController(ctx context.Context) error {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return s.config.RunCommand(ctx, "systemctl", "restart", "oboard-controller.service")
	}
	if _, err := exec.LookPath("rc-service"); err == nil {
		return s.config.RunCommand(ctx, "rc-service", "oboard-controller", "restart")
	}
	return errors.New("supported service manager not found")
}

func (s *Service) waitHealth(ctx context.Context) error {
	urls := s.healthURLs("/healthz")
	if len(urls) == 0 {
		return errors.New("无法确定主控的本机健康检查地址")
	}
	healthCtx, cancel := context.WithTimeout(ctx, s.config.HealthTimeout)
	defer cancel()
	for healthCtx.Err() == nil {
		for _, url := range urls {
			req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, url, nil)
			if err != nil {
				continue
			}
			resp, err := s.config.HealthClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		if err := s.config.Wait(healthCtx, s.config.HealthPollInterval); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			break
		}
	}
	return fmt.Errorf("主控在 %s 内未恢复可用", s.config.HealthTimeout)
}

func waitForContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
