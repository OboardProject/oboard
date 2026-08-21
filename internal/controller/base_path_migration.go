package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

const (
	settingControllerBasePath                    = "controller_base_path"
	settingControllerBasePathPrevious            = "controller_base_path_previous"
	settingControllerBasePathRetired             = "controller_base_path_retired"
	settingControllerBasePathMigrationVersion    = "controller_base_path_migration_version"
	settingControllerBasePathMigrationTotal      = "controller_base_path_migration_total"
	settingControllerBasePathMigrationStartedAt  = "controller_base_path_migration_started_at"
	settingControllerBasePathMigrationTargets    = "controller_base_path_migration_targets"
	settingControllerBasePathMigrationController = "controller_base_path_migration_controller_url"
)

type basePathMigrationTarget struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
}

type basePathState struct {
	Current                string
	Previous               string
	Retired                string
	MigrationVersion       int64
	MigrationTotal         int
	MigrationStartedAt     string
	MigrationTargets       []basePathMigrationTarget
	MigrationControllerURL string
}

type requestBasePathContextKey struct{}

type basePathMigrationRequestError struct {
	status int
	err    error
}

func (e *basePathMigrationRequestError) Error() string { return e.err.Error() }
func (e *basePathMigrationRequestError) Unwrap() error { return e.err }

type basePathMigrationAgentProgress struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	TaskID     int64  `json:"task_id,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type basePathMigrationProgress struct {
	Active        bool                             `json:"active"`
	CurrentPath   string                           `json:"current_path"`
	PreviousPath  string                           `json:"previous_path,omitempty"`
	ConfigVersion int64                            `json:"config_version,omitempty"`
	StartedAt     string                           `json:"started_at,omitempty"`
	Total         int                              `json:"total"`
	Succeeded     int                              `json:"succeeded"`
	Pending       int                              `json:"pending"`
	Running       int                              `json:"running"`
	Failed        int                              `json:"failed"`
	Removed       int                              `json:"removed"`
	Percentage    int                              `json:"percentage"`
	Agents        []basePathMigrationAgentProgress `json:"agents"`
}

func (s *Server) restoreBasePathState(ctx context.Context, startupPath string) {
	state := &basePathState{Current: startupPath}
	if s.store == nil {
		s.basePaths.Store(state)
		return
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		log.Printf("load Controller base path settings: %v", err)
		s.basePaths.Store(state)
		return
	}
	if raw, exists := settings[settingControllerBasePath]; exists {
		if normalized, normalizeErr := NormalizeBasePath(raw); normalizeErr == nil {
			state.Current = normalized
		} else {
			log.Printf("ignore invalid persisted Controller base path %q: %v", raw, normalizeErr)
		}
	} else if err := s.store.SetSetting(ctx, settingControllerBasePath, startupPath); err != nil {
		log.Printf("persist initial Controller base path: %v", err)
	}
	if retired, retiredErr := NormalizeBasePath(settings[settingControllerBasePathRetired]); retiredErr == nil && retired != state.Current {
		state.Retired = retired
	}

	versionStamp, _ := strconv.ParseInt(strings.TrimSpace(settings[settingControllerBasePathMigrationVersion]), 10, 64)
	migrationTotal, _ := strconv.Atoi(strings.TrimSpace(settings[settingControllerBasePathMigrationTotal]))
	previous, previousErr := NormalizeBasePath(settings[settingControllerBasePathPrevious])
	if versionStamp > 0 && previousErr == nil && previous != state.Current {
		var targets []basePathMigrationTarget
		if err := json.Unmarshal([]byte(settings[settingControllerBasePathMigrationTargets]), &targets); err != nil {
			log.Printf("load Controller base path migration targets: %v", err)
			targets = []basePathMigrationTarget{}
		}
		state.Previous = previous
		state.MigrationVersion = versionStamp
		state.MigrationTotal = max(migrationTotal, len(targets))
		state.MigrationStartedAt = settings[settingControllerBasePathMigrationStartedAt]
		state.MigrationTargets = targets
		state.MigrationControllerURL = strings.TrimSpace(settings[settingControllerBasePathMigrationController])
		if err := s.migrateOAuthResourceForPreviousPath(ctx, state.Previous, state.MigrationControllerURL); err != nil {
			log.Printf("migrate OAuth resource during Controller base path restore: %v", err)
		}
	}
	s.basePaths.Store(state)
}

func (s *Server) basePathState() *basePathState {
	if state := s.basePaths.Load(); state != nil {
		return state
	}
	return &basePathState{Current: s.basePath}
}

func (s *Server) currentBasePath() string {
	return s.basePathState().Current
}

func (s *Server) BasePath() string {
	return s.currentBasePath()
}

func requestBasePath(r *http.Request, fallback string) string {
	if r != nil {
		if value, ok := r.Context().Value(requestBasePathContextKey{}).(string); ok {
			return value
		}
	}
	return fallback
}

func basePathMatches(path, prefix string) bool {
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

func (s *Server) matchBasePath(path string) (string, bool) {
	state := s.basePathState()
	if state.MigrationVersion == 0 && state.Current == "" && state.Retired != "" && basePathMatches(path, state.Retired) {
		return "", false
	}
	paths := []string{state.Current}
	if state.MigrationVersion > 0 && state.Previous != state.Current {
		paths = append(paths, state.Previous)
	}
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, prefix := range paths {
		if basePathMatches(path, prefix) {
			return prefix, true
		}
	}
	return "", false
}

func pathUnderBasePath(basePath, path string) string {
	if path == "" || path == "/" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return basePath + path
}

func (s *Server) normalizeControllerURLForBasePath(raw, basePath string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("controller_url must be a valid http(s) URL")
	}
	if u.Scheme == "ws" {
		u.Scheme = "http"
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	}
	u.Path = basePath
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	target := strings.TrimRight(u.String(), "/")
	if _, err := security.ValidateControllerURL(target, version.IsDev(), false); err != nil {
		return "", err
	}
	return target, nil
}

func (s *Server) controllerURLForBasePath(settings map[string]string, basePath string) (string, error) {
	raw := strings.TrimSpace(settings["controller_url"])
	if raw == "" {
		return "", errors.New("请先在系统设置中配置主控公开地址（controller_url）")
	}
	target, err := s.normalizeControllerURLForBasePath(raw, basePath)
	if err != nil {
		return "", err
	}
	return target, nil
}

func (s *Server) migrateOAuthResourceForPreviousPath(ctx context.Context, previousPath, targetURL string) error {
	if strings.TrimSpace(targetURL) == "" {
		return nil
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return err
	}
	parsed.Path = previousPath
	parsed.RawPath = ""
	oldURL := strings.TrimRight(parsed.String(), "/")
	return s.store.MigrateOAuthResource(ctx, oldURL+"/api/v1/mcp", strings.TrimRight(targetURL, "/")+"/api/v1/mcp")
}

func (s *Server) startBasePathMigration(ctx context.Context, r *http.Request, raw string) (string, bool, error) {
	s.basePathMigrationMu.Lock()
	defer s.basePathMigrationMu.Unlock()

	nextPath, err := NormalizeBasePath(raw)
	if err != nil {
		return "", false, &basePathMigrationRequestError{status: http.StatusBadRequest, err: err}
	}
	current := s.basePathState()
	if current.MigrationVersion > 0 {
		return "", false, &basePathMigrationRequestError{status: http.StatusConflict, err: errors.New("a Controller base path migration is already in progress")}
	}
	if nextPath == current.Current {
		return pathUnderBasePath(nextPath, "/settings"), false, nil
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(settings[settingSubscriptionRelayURL]) != "" {
		return "", false, &basePathMigrationRequestError{status: http.StatusConflict, err: errors.New("请先清空订阅中继地址，再修改面板路径")}
	}
	targetURL, err := s.controllerURLForBasePath(settings, nextPath)
	if err != nil {
		return "", false, &basePathMigrationRequestError{status: http.StatusBadRequest, err: err}
	}
	if err := s.migrateOAuthResourceForPreviousPath(ctx, current.Current, targetURL); err != nil {
		return "", false, err
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "", false, err
	}
	targets := make([]basePathMigrationTarget, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server.AgentID) != "" {
			targets = append(targets, basePathMigrationTarget{ServerID: server.ID, ServerName: server.Name})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ServerID < targets[j].ServerID })
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		return "", false, err
	}
	// Milliseconds remain exact in JavaScript and cannot collide with the
	// second-based config versions used by ordinary task batches.
	versionStamp := time.Now().UnixMilli()
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	values := map[string]string{
		settingControllerBasePath:                    nextPath,
		settingControllerBasePathPrevious:            current.Current,
		settingControllerBasePathRetired:             "",
		settingControllerBasePathMigrationVersion:    strconv.FormatInt(versionStamp, 10),
		settingControllerBasePathMigrationTotal:      strconv.Itoa(len(targets)),
		settingControllerBasePathMigrationStartedAt:  startedAt,
		settingControllerBasePathMigrationTargets:    string(targetsJSON),
		settingControllerBasePathMigrationController: targetURL,
	}
	if strings.TrimSpace(settings["controller_url"]) != "" {
		values["controller_url"] = targetURL
	}
	if err := s.store.SetSettings(ctx, values); err != nil {
		return "", false, err
	}
	next := &basePathState{
		Current:                nextPath,
		Previous:               current.Current,
		MigrationVersion:       versionStamp,
		MigrationTotal:         len(targets),
		MigrationStartedAt:     startedAt,
		MigrationTargets:       targets,
		MigrationControllerURL: targetURL,
	}
	s.basePaths.Store(next)
	s.syncControllerRuntimeState()
	for _, target := range targets {
		if _, err := s.queueAgentTask(ctx, target.ServerID, model.AgentTaskTypeUpdateAgentConfig, map[string]string{"controller_url": targetURL}, versionStamp); err != nil {
			log.Printf("queue Controller base path migration task for server %d: %v", target.ServerID, err)
		}
	}
	if len(targets) == 0 {
		if err := s.finalizeBasePathMigrationLocked(ctx, next); err != nil {
			return "", false, err
		}
	}
	return pathUnderBasePath(nextPath, "/settings"), true, nil
}

func latestMigrationTasks(tasks []model.AgentTask) map[int64]model.AgentTask {
	latest := make(map[int64]model.AgentTask)
	for _, task := range tasks {
		if task.Type != model.AgentTaskTypeUpdateAgentConfig {
			continue
		}
		if previous, ok := latest[task.ServerID]; !ok || task.ID > previous.ID {
			latest[task.ServerID] = task
		}
	}
	return latest
}

func (s *Server) basePathMigrationProgress(ctx context.Context) (basePathMigrationProgress, error) {
	state := s.basePathState()
	progress := basePathMigrationProgress{
		Active:       state.MigrationVersion > 0,
		CurrentPath:  state.Current,
		PreviousPath: state.Previous,
		Agents:       []basePathMigrationAgentProgress{},
	}
	if !progress.Active {
		return progress, nil
	}
	progress.ConfigVersion = state.MigrationVersion
	progress.StartedAt = state.MigrationStartedAt
	progress.Total = max(state.MigrationTotal, len(state.MigrationTargets))
	tasks, err := s.store.ListTasksByConfigVersion(ctx, state.MigrationVersion)
	if err != nil {
		return progress, err
	}
	latest := latestMigrationTasks(tasks)
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return progress, err
	}
	exists := make(map[int64]bool, len(servers))
	for _, server := range servers {
		exists[server.ID] = true
	}
	for _, target := range state.MigrationTargets {
		item := basePathMigrationAgentProgress{ServerID: target.ServerID, ServerName: target.ServerName}
		if !exists[target.ServerID] {
			item.Status = "removed"
			progress.Removed++
			progress.Agents = append(progress.Agents, item)
			continue
		}
		task, ok := latest[target.ServerID]
		if !ok {
			item.Status = "failed"
			item.Error = "迁移任务尚未创建，请重试"
			progress.Failed++
			progress.Agents = append(progress.Agents, item)
			continue
		}
		item.TaskID = task.ID
		item.Status = task.Status
		switch task.Status {
		case "succeeded":
			progress.Succeeded++
		case "pending":
			progress.Pending++
		case "running":
			progress.Running++
		default:
			item.Status = "failed"
			item.Error = taskResultMessage(task)
			progress.Failed++
		}
		progress.Agents = append(progress.Agents, item)
	}
	// A persisted total larger than the target snapshot indicates incomplete or
	// corrupt migration metadata. Keep the old path active until an operator can
	// repair it instead of treating the missing targets as successful.
	progress.Failed += progress.Total - len(state.MigrationTargets)
	if progress.Total == 0 {
		progress.Percentage = 100
	} else {
		progress.Percentage = (progress.Succeeded + progress.Removed) * 100 / progress.Total
	}
	return progress, nil
}

func (s *Server) retryBasePathMigration(ctx context.Context) (basePathMigrationProgress, error) {
	s.basePathMigrationMu.Lock()
	defer s.basePathMigrationMu.Unlock()
	state := s.basePathState()
	if state.MigrationVersion == 0 {
		return basePathMigrationProgress{}, errors.New("no Controller base path migration is in progress")
	}
	if strings.TrimSpace(state.MigrationControllerURL) == "" {
		return basePathMigrationProgress{}, errors.New("Controller base path migration target URL is missing")
	}
	tasks, err := s.store.ListTasksByConfigVersion(ctx, state.MigrationVersion)
	if err != nil {
		return basePathMigrationProgress{}, err
	}
	latest := latestMigrationTasks(tasks)
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return basePathMigrationProgress{}, err
	}
	exists := make(map[int64]bool, len(servers))
	for _, server := range servers {
		exists[server.ID] = true
	}
	for _, target := range state.MigrationTargets {
		if !exists[target.ServerID] {
			continue
		}
		if task, ok := latest[target.ServerID]; ok && (task.Status == "succeeded" || task.Status == "pending" || task.Status == "running") {
			continue
		}
		if _, err := s.queueAgentTask(ctx, target.ServerID, model.AgentTaskTypeUpdateAgentConfig, map[string]string{"controller_url": state.MigrationControllerURL}, state.MigrationVersion); err != nil {
			log.Printf("retry Controller base path migration task for server %d: %v", target.ServerID, err)
		}
	}
	progress, err := s.basePathMigrationProgress(ctx)
	if err != nil {
		return progress, err
	}
	if progress.Succeeded+progress.Removed == progress.Total {
		if err := s.finalizeBasePathMigrationLocked(ctx, state); err != nil {
			return progress, err
		}
		progress, err = s.basePathMigrationProgress(ctx)
	}
	return progress, err
}

func (s *Server) maybeFinalizeBasePathMigration(ctx context.Context) {
	s.basePathMigrationMu.Lock()
	defer s.basePathMigrationMu.Unlock()
	state := s.basePathState()
	if state.MigrationVersion == 0 {
		return
	}
	progress, err := s.basePathMigrationProgress(ctx)
	if err != nil {
		log.Printf("check Controller base path migration: %v", err)
		return
	}
	if progress.Succeeded+progress.Removed != progress.Total {
		return
	}
	if err := s.finalizeBasePathMigrationLocked(ctx, state); err != nil {
		log.Printf("finalize Controller base path migration: %v", err)
	}
}

func (s *Server) finalizeBasePathMigrationLocked(ctx context.Context, state *basePathState) error {
	if state == nil || state.MigrationVersion == 0 {
		return nil
	}
	retired := ""
	if state.Current == "" {
		retired = state.Previous
	}
	if err := s.store.SetSettings(ctx, map[string]string{
		settingControllerBasePath:                    state.Current,
		settingControllerBasePathPrevious:            "",
		settingControllerBasePathRetired:             retired,
		settingControllerBasePathMigrationVersion:    "",
		settingControllerBasePathMigrationTotal:      "0",
		settingControllerBasePathMigrationStartedAt:  "",
		settingControllerBasePathMigrationTargets:    "[]",
		settingControllerBasePathMigrationController: "",
	}); err != nil {
		return err
	}
	s.basePaths.Store(&basePathState{Current: state.Current, Retired: retired})
	s.syncControllerRuntimeState()
	return nil
}

func (s *Server) settingsBasePathRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if _, err := s.retryBasePathMigration(r.Context()); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "no Controller base path migration") {
			status = http.StatusConflict
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "retry", "settings", "controller_base_path")
	items, err := s.store.ListSettings(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"settings": s.publicSettings(r.Context(), items)})
}

func migrationConflictStatus(err error) int {
	var requestErr *basePathMigrationRequestError
	if errors.As(err, &requestErr) {
		return requestErr.status
	}
	return http.StatusInternalServerError
}
