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
	settingControllerBasePathMigrationDirection  = "controller_base_path_migration_direction"
	settingControllerBasePathMigrationOrigin     = "controller_base_path_migration_origin_url"

	basePathMigrationDirectionForward  = "forward"
	basePathMigrationDirectionRollback = "rollback"
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
	MigrationDirection     string
	OriginControllerURL    string
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
	Direction     string                           `json:"direction,omitempty"`
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
	Skipped       int                              `json:"skipped"`
	Percentage    int                              `json:"percentage"`
	CanRevoke     bool                             `json:"can_revoke"`
	CanForce      bool                             `json:"can_force"`
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
		state.MigrationDirection = normalizeBasePathMigrationDirection(settings[settingControllerBasePathMigrationDirection])
		state.OriginControllerURL = strings.TrimSpace(settings[settingControllerBasePathMigrationOrigin])
		if state.OriginControllerURL == "" {
			state.OriginControllerURL = s.derivedOriginControllerURL(state)
		}
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

func (s *Server) derivedOriginControllerURL(state *basePathState) string {
	if state == nil || strings.TrimSpace(state.MigrationControllerURL) == "" {
		return ""
	}
	if normalizeBasePathMigrationDirection(state.MigrationDirection) == basePathMigrationDirectionRollback {
		return state.MigrationControllerURL
	}
	derived, err := s.normalizeControllerURLForBasePath(state.MigrationControllerURL, state.Previous)
	if err != nil {
		return ""
	}
	return derived
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

func normalizeBasePathMigrationDirection(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), basePathMigrationDirectionRollback) {
		return basePathMigrationDirectionRollback
	}
	return basePathMigrationDirectionForward
}

func enrolledBasePathTargets(servers []model.Server) []basePathMigrationTarget {
	targets := make([]basePathMigrationTarget, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server.AgentID) == "" {
			continue
		}
		targets = append(targets, basePathMigrationTarget{ServerID: server.ID, ServerName: server.Name})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ServerID < targets[j].ServerID })
	return targets
}

func (s *Server) persistActiveBasePathMigration(ctx context.Context, next *basePathState, controllerURL string) error {
	targetsJSON, err := json.Marshal(next.MigrationTargets)
	if err != nil {
		return err
	}
	values := map[string]string{
		settingControllerBasePath:                    next.Current,
		settingControllerBasePathPrevious:            next.Previous,
		settingControllerBasePathRetired:             "",
		settingControllerBasePathMigrationVersion:    strconv.FormatInt(next.MigrationVersion, 10),
		settingControllerBasePathMigrationTotal:      strconv.Itoa(len(next.MigrationTargets)),
		settingControllerBasePathMigrationStartedAt:  next.MigrationStartedAt,
		settingControllerBasePathMigrationTargets:    string(targetsJSON),
		settingControllerBasePathMigrationController: next.MigrationControllerURL,
		settingControllerBasePathMigrationDirection:  normalizeBasePathMigrationDirection(next.MigrationDirection),
		settingControllerBasePathMigrationOrigin:     next.OriginControllerURL,
	}
	if strings.TrimSpace(controllerURL) != "" {
		values["controller_url"] = controllerURL
	}
	return s.store.SetSettings(ctx, values)
}

func (s *Server) queueBasePathConfigTasks(ctx context.Context, targets []basePathMigrationTarget, controllerURL string, version int64) {
	for _, target := range targets {
		if _, err := s.queueAgentTask(ctx, target.ServerID, model.AgentTaskTypeUpdateAgentConfig, map[string]string{"controller_url": controllerURL}, version); err != nil {
			log.Printf("queue Controller base path migration task for server %d: %v", target.ServerID, err)
		}
	}
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
	originURL, err := s.controllerURLForBasePath(settings, current.Current)
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
	targets := enrolledBasePathTargets(servers)
	// Milliseconds remain exact in JavaScript and cannot collide with the
	// second-based config versions used by ordinary task batches.
	versionStamp := time.Now().UnixMilli()
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	next := &basePathState{
		Current:                nextPath,
		Previous:               current.Current,
		MigrationVersion:       versionStamp,
		MigrationTotal:         len(targets),
		MigrationStartedAt:     startedAt,
		MigrationTargets:       targets,
		MigrationControllerURL: targetURL,
		MigrationDirection:     basePathMigrationDirectionForward,
		OriginControllerURL:    originURL,
	}
	if err := s.persistActiveBasePathMigration(ctx, next, targetURL); err != nil {
		return "", false, err
	}
	s.basePaths.Store(next)
	s.syncControllerRuntimeState()
	s.queueBasePathConfigTasks(ctx, targets, targetURL, versionStamp)
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

func basePathTaskNeverReachedAgent(task model.AgentTask) bool {
	if task.Status != "failed" {
		return false
	}
	var payload struct {
		Offline bool   `json:"offline"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal([]byte(task.ResultJSON), &payload) != nil {
		return false
	}
	if payload.Offline {
		return true
	}
	text := payload.Error
	if text == "" {
		text = payload.Message
	}
	return strings.Contains(text, "服务器离线") || strings.Contains(text, "Agent 未接入") || strings.Contains(text, "服务器不存在")
}

func basePathAgentNeedsURLUpdate(task model.AgentTask, ok bool) bool {
	if !ok {
		return false
	}
	switch task.Status {
	case "succeeded", "pending", "running":
		return true
	case "failed":
		return !basePathTaskNeverReachedAgent(task)
	default:
		return true
	}
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
	progress.Direction = normalizeBasePathMigrationDirection(state.MigrationDirection)
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
	exists := make(map[int64]model.Server, len(servers))
	for _, server := range servers {
		exists[server.ID] = server
	}
	seen := map[int64]bool{}
	for _, target := range state.MigrationTargets {
		seen[target.ServerID] = true
		item := basePathMigrationAgentProgress{ServerID: target.ServerID, ServerName: target.ServerName}
		server, found := exists[target.ServerID]
		if !found {
			item.Status = "removed"
			progress.Removed++
			progress.Agents = append(progress.Agents, item)
			continue
		}
		item.ServerName = server.Name
		if strings.TrimSpace(server.AgentID) == "" {
			item.Status = "skipped"
			item.Error = "Agent 未接入，已跳过"
			progress.Skipped++
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
	for _, server := range servers {
		if seen[server.ID] {
			continue
		}
		item := basePathMigrationAgentProgress{ServerID: server.ID, ServerName: server.Name, Status: "skipped"}
		if strings.TrimSpace(server.AgentID) == "" {
			item.Error = "Agent 未接入，已跳过"
		} else if progress.Direction == basePathMigrationDirectionRollback {
			item.Error = "仍使用当前路径，无需回退"
		} else {
			item.Error = "迁移开始后接入，已使用新路径"
		}
		progress.Skipped++
		progress.Agents = append(progress.Agents, item)
	}
	sort.Slice(progress.Agents, func(i, j int) bool { return progress.Agents[i].ServerID < progress.Agents[j].ServerID })
	// A persisted total larger than the target snapshot indicates incomplete or
	// corrupt migration metadata. Keep the old path active until an operator can
	// repair it instead of treating the missing targets as successful.
	progress.Failed += progress.Total - len(state.MigrationTargets)
	resolved := progress.Succeeded + progress.Removed + skippedRequiredAgents(progress, state)
	if progress.Total == 0 {
		progress.Percentage = 100
	} else {
		progress.Percentage = resolved * 100 / progress.Total
	}
	progress.CanRevoke = progress.Direction == basePathMigrationDirectionForward
	progress.CanForce = resolved < progress.Total
	return progress, nil
}

func skippedRequiredAgents(progress basePathMigrationProgress, state *basePathState) int {
	required := map[int64]bool{}
	for _, target := range state.MigrationTargets {
		required[target.ServerID] = true
	}
	count := 0
	for _, agent := range progress.Agents {
		if required[agent.ServerID] && agent.Status == "skipped" {
			count++
		}
	}
	return count
}

func (s *Server) retryBasePathMigration(ctx context.Context) (basePathMigrationProgress, error) {
	s.basePathMigrationMu.Lock()
	defer s.basePathMigrationMu.Unlock()
	return s.retryBasePathMigrationLocked(ctx, 0)
}

func (s *Server) retryBasePathMigrationForServer(ctx context.Context, serverID int64) {
	s.basePathMigrationMu.Lock()
	defer s.basePathMigrationMu.Unlock()
	if _, err := s.retryBasePathMigrationLocked(ctx, serverID); err != nil && !strings.Contains(err.Error(), "no Controller base path migration") {
		log.Printf("retry Controller base path migration for recovered server %d: %v", serverID, err)
	}
}

func (s *Server) retryBasePathMigrationLocked(ctx context.Context, serverID int64) (basePathMigrationProgress, error) {
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
	exists := make(map[int64]model.Server, len(servers))
	for _, server := range servers {
		exists[server.ID] = server
	}
	for _, target := range state.MigrationTargets {
		if serverID > 0 && target.ServerID != serverID {
			continue
		}
		server, found := exists[target.ServerID]
		if !found || strings.TrimSpace(server.AgentID) == "" {
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
	if s.migrationReadyToFinalize(progress, state) {
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
	if !s.migrationReadyToFinalize(progress, state) {
		return
	}
	if err := s.finalizeBasePathMigrationLocked(ctx, state); err != nil {
		log.Printf("finalize Controller base path migration: %v", err)
	}
}

func (s *Server) migrationReadyToFinalize(progress basePathMigrationProgress, state *basePathState) bool {
	if !progress.Active {
		return false
	}
	return progress.Succeeded+progress.Removed+skippedRequiredAgents(progress, state) == progress.Total
}

func (s *Server) forceBasePathMigration(ctx context.Context) (basePathMigrationProgress, error) {
	s.basePathMigrationMu.Lock()
	defer s.basePathMigrationMu.Unlock()
	state := s.basePathState()
	if state.MigrationVersion == 0 {
		return basePathMigrationProgress{}, &basePathMigrationRequestError{status: http.StatusConflict, err: errors.New("no Controller base path migration is in progress")}
	}
	progress, err := s.basePathMigrationProgress(ctx)
	if err != nil {
		return progress, err
	}
	if !progress.CanForce {
		if err := s.finalizeBasePathMigrationLocked(ctx, state); err != nil {
			return progress, err
		}
		return s.basePathMigrationProgress(ctx)
	}
	for _, target := range state.MigrationTargets {
		if err := s.store.SupersedePendingTasksByServerType(ctx, target.ServerID, model.AgentTaskTypeUpdateAgentConfig, "面板路径迁移已强制完成，未完成的同步已取消"); err != nil {
			log.Printf("supersede Controller base path migration task for server %d: %v", target.ServerID, err)
		}
	}
	if err := s.finalizeBasePathMigrationLocked(ctx, state); err != nil {
		return progress, err
	}
	return s.basePathMigrationProgress(ctx)
}

func (s *Server) revokeBasePathMigration(ctx context.Context) (string, basePathMigrationProgress, error) {
	s.basePathMigrationMu.Lock()
	defer s.basePathMigrationMu.Unlock()
	state := s.basePathState()
	if state.MigrationVersion == 0 {
		return "", basePathMigrationProgress{}, &basePathMigrationRequestError{status: http.StatusConflict, err: errors.New("no Controller base path migration is in progress")}
	}
	if normalizeBasePathMigrationDirection(state.MigrationDirection) == basePathMigrationDirectionRollback {
		return "", basePathMigrationProgress{}, &basePathMigrationRequestError{status: http.StatusConflict, err: errors.New("面板路径迁移已在撤销中")}
	}
	originURL := strings.TrimSpace(state.OriginControllerURL)
	if originURL == "" {
		originURL = s.derivedOriginControllerURL(state)
	}
	if originURL == "" {
		return "", basePathMigrationProgress{}, errors.New("Controller base path migration origin URL is missing")
	}
	if err := s.migrateOAuthResourceForPreviousPath(ctx, state.Current, originURL); err != nil {
		return "", basePathMigrationProgress{}, err
	}
	tasks, err := s.store.ListTasksByConfigVersion(ctx, state.MigrationVersion)
	if err != nil {
		return "", basePathMigrationProgress{}, err
	}
	latest := latestMigrationTasks(tasks)
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "", basePathMigrationProgress{}, err
	}
	original := map[int64]basePathMigrationTarget{}
	for _, target := range state.MigrationTargets {
		original[target.ServerID] = target
	}
	rollback := make([]basePathMigrationTarget, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server.AgentID) == "" {
			continue
		}
		target, wasOriginal := original[server.ID]
		if !wasOriginal {
			rollback = append(rollback, basePathMigrationTarget{ServerID: server.ID, ServerName: server.Name})
			continue
		}
		if err := s.store.SupersedePendingTasksByServerType(ctx, server.ID, model.AgentTaskTypeUpdateAgentConfig, "面板路径迁移已撤销，未开始的同步已取消"); err != nil {
			log.Printf("supersede Controller base path migration task for server %d: %v", server.ID, err)
		}
		task, ok := latest[server.ID]
		if ok && task.Status == "pending" {
			continue
		}
		if !basePathAgentNeedsURLUpdate(task, ok) {
			continue
		}
		name := server.Name
		if strings.TrimSpace(target.ServerName) != "" {
			name = target.ServerName
		}
		rollback = append(rollback, basePathMigrationTarget{ServerID: server.ID, ServerName: name})
	}
	sort.Slice(rollback, func(i, j int) bool { return rollback[i].ServerID < rollback[j].ServerID })
	versionStamp := time.Now().UnixMilli()
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	next := &basePathState{
		Current:                state.Previous,
		Previous:               state.Current,
		MigrationVersion:       versionStamp,
		MigrationTotal:         len(rollback),
		MigrationStartedAt:     startedAt,
		MigrationTargets:       rollback,
		MigrationControllerURL: originURL,
		MigrationDirection:     basePathMigrationDirectionRollback,
		OriginControllerURL:    originURL,
	}
	if err := s.persistActiveBasePathMigration(ctx, next, originURL); err != nil {
		return "", basePathMigrationProgress{}, err
	}
	s.basePaths.Store(next)
	s.syncControllerRuntimeState()
	s.queueBasePathConfigTasks(ctx, rollback, originURL, versionStamp)
	if len(rollback) == 0 {
		if err := s.finalizeBasePathMigrationLocked(ctx, next); err != nil {
			return "", basePathMigrationProgress{}, err
		}
	}
	progress, err := s.basePathMigrationProgress(ctx)
	if err != nil {
		return pathUnderBasePath(next.Current, "/settings"), progress, err
	}
	return pathUnderBasePath(next.Current, "/settings"), progress, nil
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
		settingControllerBasePathMigrationDirection:  "",
		settingControllerBasePathMigrationOrigin:     "",
	}); err != nil {
		return err
	}
	s.basePaths.Store(&basePathState{Current: state.Current, Retired: retired})
	s.syncControllerRuntimeState()
	s.publishRealtime("settings")
	return nil
}

func (s *Server) settingsBasePathRetry(w http.ResponseWriter, r *http.Request) {
	s.writeBasePathMigrationAction(w, r, "retry", func(ctx context.Context) (string, error) {
		_, err := s.retryBasePathMigration(ctx)
		return "", err
	})
}

func (s *Server) settingsBasePathForce(w http.ResponseWriter, r *http.Request) {
	s.writeBasePathMigrationAction(w, r, "force", func(ctx context.Context) (string, error) {
		_, err := s.forceBasePathMigration(ctx)
		return "", err
	})
}

func (s *Server) settingsBasePathRevoke(w http.ResponseWriter, r *http.Request) {
	s.writeBasePathMigrationAction(w, r, "revoke", func(ctx context.Context) (string, error) {
		redirect, _, err := s.revokeBasePathMigration(ctx)
		return redirect, err
	})
}

func (s *Server) writeBasePathMigrationAction(w http.ResponseWriter, r *http.Request, action string, run func(context.Context) (string, error)) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	redirect, err := run(r.Context())
	if err != nil {
		fail(w, err, migrationConflictStatus(err))
		return
	}
	auditReq(s, r, action, "settings", "controller_base_path")
	items, err := s.store.ListSettings(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	response := map[string]any{"settings": s.publicSettings(r.Context(), items)}
	if strings.TrimSpace(redirect) != "" {
		response["redirect_path"] = redirect
	}
	write(w, http.StatusAccepted, response)
}

func migrationConflictStatus(err error) int {
	var requestErr *basePathMigrationRequestError
	if errors.As(err, &requestErr) {
		return requestErr.status
	}
	if err != nil && strings.Contains(err.Error(), "no Controller base path migration") {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
