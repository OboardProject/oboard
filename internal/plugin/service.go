package plugin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/authorization"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// Management permissions. Admin-only ones are also refused to non-interactive
// principals: plugins can never authorize themselves and machine clients
// cannot install code or grant capabilities.
const (
	PermRead      = "plugins.read"
	PermConfigure = "plugins.configure"
	PermExecute   = "plugins.execute"
	PermDevelop   = "plugins.develop"
	PermInstall   = "plugins.install"
	PermAuthorize = "plugins.authorize"
	PermSettings  = "plugins.settings"
)

var adminOnlyPermissions = map[string]bool{PermInstall: true, PermAuthorize: true, PermSettings: true}

func permissionScope(permission string) string {
	return "plugins:" + strings.TrimPrefix(permission, "plugins.")
}

// ServerInfo is the plugin-facing server view. It never contains tokens,
// configuration or credentials.
type ServerInfo struct {
	ID           int64
	Name         string
	Status       string
	RegionCode   string
	PublicIPv4   string
	PublicIPv6   string
	Enrolled     bool
	Online       bool
	AgentVersion string
	LastSeenAt   *time.Time
	Capabilities []string
	PluginsGate  bool
}

// DiagnosticRequest is one validated network capability call bound to a run.
type DiagnosticRequest struct {
	Capability string
	ServerID   int64
	RunUUID    string
	PluginKey  string
	InstanceID int64
	Payload    json.RawMessage
	Deadline   time.Time
}

// Host is implemented by the Controller. Every method is an existing,
// validated Controller service; none exposes raw Agent tasks or REST.
type Host interface {
	Server(ctx context.Context, id int64) (ServerInfo, bool)
	ListServers(ctx context.Context) []ServerInfo
	ServerHealth(ctx context.Context, id int64) (map[string]any, error)
	ServerMetrics(ctx context.Context, id int64) (map[string]any, error)
	NotificationChannelExists(ctx context.Context, id int64) bool
	SendNotification(ctx context.Context, channelID int64, title, body string) error
	RunNetworkDiagnostic(ctx context.Context, request DiagnosticRequest) (json.RawMessage, error)
	HTTPDenied(ctx context.Context, ip netip.Addr) bool
	EncryptSecret(plain string) (string, error)
	DecryptSecret(encrypted string) (string, error)
	ResolveCaller(ctx context.Context, ref CallerRef) (application.Principal, error)
	RuntimeStatus() RuntimeHostStatus
}

// CallerRef identifies whoever started a manual run so every later SDK call
// can re-check that the caller is still allowed.
type CallerRef struct {
	PrincipalID string `json:"principal_id"`
	Type        string `json:"type"`
	GrantID     string `json:"grant_id,omitempty"`
	SourceIP    string `json:"source_ip,omitempty"`
}

type RuntimeHostStatus struct {
	Installed          bool
	InstallCommand     string
	WorkerConnected    bool
	IsolationAvailable bool
	IsolationMode      string
	IsolationReason    string
}

type Settings struct {
	Enabled          bool
	SchedulerPaused  bool
	RecoveryGen      int64
	MaxConcurrency   int
	MaxTimeout       time.Duration
	LogRetentionDays int
	RunRetentionDays int
}

type Service struct {
	store   *store.Store
	rbac    *authorization.RBAC
	host    Host
	now     func() time.Time
	limiter *rateLimiter
	wake    chan struct{}
	mu      sync.Mutex
}

func NewService(db *store.Store, rbac *authorization.RBAC, host Host) *Service {
	return &Service{store: db, rbac: rbac, host: host, now: func() time.Time { return time.Now().UTC() }, limiter: newRateLimiter(), wake: make(chan struct{}, 1)}
}

func (s *Service) Settings(ctx context.Context) Settings {
	values, _ := s.store.ListSettings(ctx)
	atoi := func(key string, fallback, min, max int) int {
		n, err := strconv.Atoi(strings.TrimSpace(values[key]))
		if err != nil || n < min || n > max {
			return fallback
		}
		return n
	}
	gen, err := strconv.ParseInt(strings.TrimSpace(values[model.PluginSettingRecoveryGeneration]), 10, 64)
	if err != nil || gen <= 0 {
		gen = 1
	}
	return Settings{
		Enabled:          values[model.PluginSettingEnabled] == "true",
		SchedulerPaused:  values[model.PluginSettingSchedulerPaused] == "true",
		RecoveryGen:      gen,
		MaxConcurrency:   atoi(model.PluginSettingMaxConcurrency, DefaultMaxConcurrentRuns, 1, MaxConcurrentRunsCeiling),
		MaxTimeout:       time.Duration(atoi(model.PluginSettingMaxTimeoutSeconds, 60, 5, int(MaxRunTimeout/time.Second))) * time.Second,
		LogRetentionDays: atoi(model.PluginSettingLogRetentionDays, 14, 1, 365),
		RunRetentionDays: atoi(model.PluginSettingRunRetentionDays, 30, 1, 365),
	}
}

// SettingsUpdate is an administrator change to the global runtime policy.
type SettingsUpdate struct {
	Enabled           *bool `json:"enabled,omitempty"`
	SchedulerPaused   *bool `json:"scheduler_paused,omitempty"`
	MaxConcurrency    *int  `json:"max_concurrency,omitempty"`
	MaxTimeoutSeconds *int  `json:"max_timeout_seconds,omitempty"`
	LogRetentionDays  *int  `json:"log_retention_days,omitempty"`
	RunRetentionDays  *int  `json:"run_retention_days,omitempty"`
}

func (s *Service) UpdateSettings(ctx context.Context, actor application.Principal, update SettingsUpdate) (model.PluginRuntimeStatus, error) {
	if err := s.require(actor, PermSettings); err != nil {
		return model.PluginRuntimeStatus{}, err
	}
	values := map[string]string{}
	if update.Enabled != nil {
		if *update.Enabled && !s.host.RuntimeStatus().Installed {
			return model.PluginRuntimeStatus{}, Fail(CodeRuntimeUnavailable, "尚未安装插件运行环境，请先在主控主机上执行安装命令")
		}
		values[model.PluginSettingEnabled] = strconv.FormatBool(*update.Enabled)
	}
	if update.SchedulerPaused != nil {
		values[model.PluginSettingSchedulerPaused] = strconv.FormatBool(*update.SchedulerPaused)
	}
	bounded := func(field string, value *int, min, max int, key string) error {
		if value == nil {
			return nil
		}
		if *value < min || *value > max {
			return FailField(CodeInvalidArgument, field, "value is out of range")
		}
		values[key] = strconv.Itoa(*value)
		return nil
	}
	for _, err := range []error{
		bounded("max_concurrency", update.MaxConcurrency, 1, MaxConcurrentRunsCeiling, model.PluginSettingMaxConcurrency),
		bounded("max_timeout_seconds", update.MaxTimeoutSeconds, 5, int(MaxRunTimeout/time.Second), model.PluginSettingMaxTimeoutSeconds),
		bounded("log_retention_days", update.LogRetentionDays, 1, 365, model.PluginSettingLogRetentionDays),
		bounded("run_retention_days", update.RunRetentionDays, 1, 365, model.PluginSettingRunRetentionDays),
	} {
		if err != nil {
			return model.PluginRuntimeStatus{}, err
		}
	}
	if len(values) > 0 {
		if err := s.store.SetSettings(ctx, values); err != nil {
			return model.PluginRuntimeStatus{}, err
		}
	}
	return s.RuntimeStatus(ctx, actor)
}

func (s *Service) RuntimeStatus(ctx context.Context, actor application.Principal) (model.PluginRuntimeStatus, error) {
	if err := s.require(actor, PermRead); err != nil {
		return model.PluginRuntimeStatus{}, err
	}
	settings := s.Settings(ctx)
	hostStatus := s.host.RuntimeStatus()
	queued, _ := s.store.CountPluginRunsByStatus(ctx, model.PluginRunQueued)
	running, _ := s.store.CountPluginRunsByStatus(ctx, model.PluginRunRunning)
	out := model.PluginRuntimeStatus{
		Enabled: settings.Enabled, SchedulerPaused: settings.SchedulerPaused,
		RuntimeInstalled: hostStatus.Installed, WorkerConnected: hostStatus.WorkerConnected,
		IsolationAvailable: hostStatus.IsolationAvailable, IsolationMode: hostStatus.IsolationMode, IsolationReason: hostStatus.IsolationReason,
		ActiveRuns: running, QueuedRuns: queued, MaxConcurrency: settings.MaxConcurrency,
		MaxTimeoutSeconds: int(settings.MaxTimeout / time.Second), LogRetentionDays: settings.LogRetentionDays, RunRetentionDays: settings.RunRetentionDays,
	}
	if !hostStatus.Installed {
		out.InstallCommand = hostStatus.InstallCommand
	}
	return out, nil
}

// require authorizes a management action for a human or machine principal.
func (s *Service) require(actor application.Principal, permission string) error {
	if adminOnlyPermissions[permission] {
		if actor.Role != model.RoleAdmin || !actor.Interactive {
			return Fail(CodePermissionDenied, "仅交互式管理员可执行该插件安全操作")
		}
	}
	if actor.Role != "" && actor.Role != model.RoleNone {
		if s.rbac == nil || !s.rbac.Allows(actor.Role, permission) {
			return ErrPermissionDenied
		}
		if actor.AccessLevel != "" || actor.Interactive {
			return nil
		}
	}
	if actor.HasScope(permissionScope(permission)) {
		return nil
	}
	return ErrPermissionDenied
}

// Can reports whether the actor holds a permission; views use it to decide
// what to reveal (for example drafts and grant editing).
func (s *Service) Can(actor application.Principal, permission string) bool {
	return s.require(actor, permission) == nil
}

func actorUserID(actor application.Principal) int64 {
	if actor.UserID != nil {
		return *actor.UserID
	}
	return 0
}

func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func notFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, store.ErrPluginConflict):
		return ErrConflict
	case errors.Is(err, store.ErrPluginVersionExists):
		return Fail(CodeConflict, "该版本号已存在；发布新代码请提升 version")
	case errors.Is(err, store.ErrPluginStateQuota):
		return Fail(CodeStateQuotaExceeded, "plugin state quota exceeded")
	case errors.Is(err, store.ErrPluginStateConflict):
		return Fail(CodeStateConflict, "state version changed")
	}
	var coded *Error
	if errors.As(err, &coded) {
		return coded
	}
	return Fail(CodeInternal, "internal error")
}

// rateLimiter bounds capability calls per instance per minute across runs.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

func newRateLimiter() *rateLimiter { return &rateLimiter{windows: map[string]*rateWindow{}} }

func (r *rateLimiter) allow(key string, perMinute int, now time.Time) bool {
	if perMinute <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	window := r.windows[key]
	if window == nil || now.Sub(window.start) >= time.Minute {
		if len(r.windows) > 4096 {
			for k, w := range r.windows {
				if now.Sub(w.start) >= time.Minute {
					delete(r.windows, k)
				}
			}
		}
		r.windows[key] = &rateWindow{start: now, count: 1}
		return true
	}
	if window.count >= perMinute {
		return false
	}
	window.count++
	return true
}
