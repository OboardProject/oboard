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
	SocketPath       string
	BinaryEnvPath    string
	StatePath        string
	ControllerBinary string
	UpdaterBinary    string
	WebRoot          string
	DownloadsRoot    string
	WorkRoot         string
	HTTPClient       *http.Client
	RunCommand       func(context.Context, string, ...string) error
}

type Service struct {
	config ServiceConfig
	mu     sync.Mutex
	status Status
}

func DefaultServiceConfig() ServiceConfig {
	installDir := defaultInstallDir()
	return ServiceConfig{
		SocketPath:       DefaultSocketPath,
		BinaryEnvPath:    "/etc/oboard/controller.env",
		StatePath:        "/var/lib/oboard/controller-update/status.json",
		ControllerBinary: filepath.Join(installDir, "oboard-controller"),
		UpdaterBinary:    filepath.Join(installDir, "oboard-controller-updater"),
		WebRoot:          "/opt/oboard/web/dist",
		DownloadsRoot:    "/opt/oboard/downloads",
		WorkRoot:         "/var/lib/oboard/controller-update",
		HTTPClient:       &http.Client{Timeout: 2 * time.Minute},
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
	switch value := strings.TrimSpace(os.Getenv("OBOARD_INSTALL_DIR")); value {
	case "/usr/local/bin", "/opt/oboard", "/usr/local/sbin":
		return value
	default:
		return "/usr/local/bin"
	}
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
	if config.RunCommand == nil {
		config.RunCommand = defaults.RunCommand
	}
	s := &Service{config: config, status: Status{State: "idle"}}
	s.loadStatus()
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
	if status.State == "installing" {
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
	status.State, status.LastError = "installing", ""
	if err := s.saveStatus(status); err != nil {
		s.mu.Unlock()
		status.LastError = err.Error()
		writeStatus(w, http.StatusInternalServerError, status)
		return
	}
	s.mu.Unlock()
	go func() {
		_, _ = s.install(context.Background())
	}()
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
	if status.State == "installing" {
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
	status.State, status.LastError = "installing", ""
	if err := s.saveStatus(status); err != nil {
		s.mu.Unlock()
		return status, err
	}
	s.mu.Unlock()
	release, err := fetchRelease(ctx, s.config.HTTPClient, status.Channel)
	if err == nil {
		status.Available = BuildInfo{Version: release.Manifest.Version, Build: release.Manifest.Build, Commit: release.Manifest.Commit, Date: release.Manifest.Date}
		status.UpdateAvailable = updateAvailable(status.Channel, status.Current, release.Manifest)
		if !status.UpdateAvailable {
			status.State = "current"
			s.mu.Lock()
			defer s.mu.Unlock()
			return status, s.saveStatus(status)
		}
		err = s.installBinary(ctx, release)
	}
	if err != nil {
		status.State, status.LastError = "failed", err.Error()
		s.mu.Lock()
		defer s.mu.Unlock()
		if persistErr := s.saveStatus(status); persistErr != nil {
			return status, errors.Join(err, persistErr)
		}
		return status, err
	}
	status.State, status.UpdateAvailable, status.LastError = "installed", false, ""
	status.Current = status.Available
	s.mu.Lock()
	defer s.mu.Unlock()
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
		return status
	}
	if channel == "pinned" && status.State != "installing" {
		status.State = "pinned"
		status.UpdateAvailable = false
	}
	if status.State == "" {
		status.State = "idle"
	}
	return status
}

func (s *Service) detectInstallation() (string, string, string) {
	info, err := os.Stat(s.config.ControllerBinary)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", "未检测到二进制主控安装。"
	}
	binaryValues, _ := readEnv(s.config.BinaryEnvPath)
	channel := strings.ToLower(strings.TrimSpace(binaryValues["OBOARD_UPDATE_CHANNEL"]))
	if channel == "pinned" {
		return "pinned", "sed -i 's/^OBOARD_UPDATE_CHANNEL=.*/OBOARD_UPDATE_CHANNEL=stable/' /etc/oboard/controller.env && (systemctl restart oboard-controller-updater || rc-service oboard-controller-updater restart)", ""
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
	values, _ := readEnv(s.config.BinaryEnvPath)
	basePath := strings.TrimRight(values["OBOARD_BASE_PATH"], "/")
	port := "2787"
	if addr := values["OBOARD_ADDR"]; strings.Contains(addr, ":") {
		port = addr[strings.LastIndex(addr, ":")+1:]
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + basePath + "/api/v1/version")
	if err != nil {
		return info
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info
	}
	var remote struct {
		Version string `json:"version"`
		Build   string `json:"build"`
		Commit  string `json:"commit"`
		BuiltAt string `json:"built_at"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&remote) == nil && remote.Version != "" {
		return BuildInfo{Version: remote.Version, Build: remote.Build, Commit: remote.Commit, Date: remote.BuiltAt}
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

func (s *Service) installBinary(ctx context.Context, release remoteRelease) error {
	stage, err := s.stageControllerRelease(ctx, release)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	return s.replaceBinaryProgram(ctx, stage)
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
			return fmt.Errorf("controller update failed (%v) and program rollback did not become healthy: %w", err, rollbackErr)
		}
		return fmt.Errorf("controller health check failed; program files rolled back: %w", err)
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
	values, _ := readEnv(s.config.BinaryEnvPath)
	port := "2787"
	if addr := values["OBOARD_ADDR"]; strings.Contains(addr, ":") {
		port = addr[strings.LastIndex(addr, ":")+1:]
	}
	url := "http://127.0.0.1:" + port + strings.TrimRight(values["OBOARD_BASE_PATH"], "/") + "/healthz"
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("controller did not become healthy within 90 seconds")
}
