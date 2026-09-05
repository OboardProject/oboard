package controller

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	urlpath "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/auditintel"
	"github.com/OboardProject/oboard/internal/auditreview"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/backup"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/core"
	oboardgeoip "github.com/OboardProject/oboard/internal/geoip"
	oboardlog "github.com/OboardProject/oboard/internal/logging"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

const (
	settingServerDefaultMTUMode           = "server_default_mtu_mode"
	settingServerDefaultBBREnabled        = "server_default_bbr_enabled"
	settingServerDefaultTimeCorrection    = "server_default_time_correction_mode"
	settingServerMonitoringRetentionDays  = "server_monitoring_retention_days"
	settingTimeCheckNTPServers            = "time_check_ntp_servers"
	settingAuditEnabled                   = "audit_enabled"
	settingSubscriptionAuditEnabled       = "subscription_audit_enabled"
	settingConnectionAuditEnabled         = "connection_audit_enabled"
	settingAuditAction                    = "audit_action"
	settingAuditPolicy                    = "audit_policy"
	settingTrustedProxyCIDRs              = "trusted_proxy_cidrs"
	settingNotificationServerOfflineAfter = "notification_server_offline_after_seconds"
	settingNotificationServerOnlineAfter  = "notification_server_online_after_seconds"
	settingNotificationServerMergeOffline = "notification_server_merge_offline"
	settingRegistrationEnabled            = "registration_enabled"
	settingRegistrationDefaultGroupID     = "registration_default_group_id"
	timeCheckThresholdSeconds             = 30
)

var defaultTimeCheckNTPServers = []string{"time.cloudflare.com", "time.google.com", "ntp.aliyun.com"}

var automaticTrustedProxyCIDRs = []string{"127.0.0.0/8", "::1/128"}

type trustedProxyState struct {
	prefixes []netip.Prefix
}

type trustedProxyStateContextKey struct{}

type Server struct {
	store                      *store.Store
	sessionSecret              string
	staticDir                  string
	application                *application.Service
	capabilities               *capability.Catalog
	automation                 *automation.Service
	auditIntel                 *auditintel.Service
	auditReviews               *auditreview.Service
	aiModelDiscoveries         *aiModelDiscoveryQueue
	aiModelDiscoveryTimeout    time.Duration
	aiTests                    *aiTaskQueue[airpc.AITestRequest, aiTestResult]
	aiTestTimeout              time.Duration
	apiGateMu                  sync.Mutex
	apiInFlight                map[string]int
	databaseMaintenanceStarted atomic.Bool
	// basePath is the immutable startup fallback for direct test constructors.
	// Runtime request handling reads basePaths instead.
	basePath                      string
	basePaths                     atomic.Pointer[basePathState]
	basePathMigrationMu           sync.Mutex
	trustedProxies                atomic.Pointer[trustedProxyState]
	trustedProxyEnvironmentCIDRs  []string
	allowedOrigins                map[string]bool
	dnsEndpoints                  dnsProviderEndpoints
	acmeCommand                   string
	acmeHome                      string
	logs                          *oboardlog.Manager
	upgrader                      websocket.Upgrader
	realtime                      *realtimeBroker
	probeMu                       sync.Mutex
	activeProbes                  map[int64]bool
	latencyProbeMu                sync.Mutex
	agentConnectionMu             sync.Mutex
	agentConnectionCount          map[int64]int
	agentLiveMu                   sync.Mutex
	agentLive                     map[int64]chan any
	remoteExecHub                 *remoteExecResultHub
	terminalHub                   *terminalSessionHub
	notificationMu                sync.Mutex
	notificationWake              chan struct{}
	monitorStarted                atomic.Bool
	controllerUpdateMaintenance   atomic.Bool
	periodicLogMu                 sync.Mutex
	periodicLogNext               map[string]time.Time
	controllerNTPMu               sync.RWMutex
	controllerNTPState            controllerNTPState
	controllerNTPQuery            controllerNTPQueryFunc
	connectionAuditNotificationMu sync.Mutex
	connectionAuditActionMu       sync.Mutex
	connectionAuditCacheMu        sync.Mutex
	connectionAuditCacheAt        time.Time
	connectionAuditCacheCount     int
	connectionAuditCacheValid     bool
	connectionAuditComputing      bool
	notificationWG                sync.WaitGroup
	notificationSender            func(context.Context, model.NotificationChannel, string, string) error
	telegramAPI                   func(context.Context, string, string, url.Values) ([]byte, error)
	telegramPollerID              string
	certificateIssueMu            sync.Mutex
	certificateIssues             map[int64]bool
	controllerUpdater             *controllerupdate.Client
	controllerBackupDir           string
	controllerDBPath              string
	controllerRuntimeStatePath    string
	controllerListenAddress       string
	controllerUpdateRunMu         sync.Mutex
	controllerUpdateScheduleMu    sync.Mutex
	controllerLastScheduledCheck  time.Time
	controllerUpdateWatchMu       sync.Mutex
	controllerUpdateWatching      bool
	controllerUpdateCancelMu      sync.Mutex
	controllerUpdateAbort         context.CancelFunc
	controllerUpdateProgress      atomic.Value
	agentUpdates                  *agentUpdateCoordinator
	controllerActivityMu          sync.Mutex
	controllerActiveRequests      int
	controllerLastActivity        time.Time
	subscriptionRelayMu           sync.Mutex
	subscriptionRelayNonces       map[string]time.Time
	backupManager                 *backup.Manager
	backupConfigured              bool
	backupMu                      sync.Mutex
	backupJobs                    chan controllerBackupJob
	backupJobsStarted             bool
	backupRestart                 func()
	// deploymentMu serializes deployment preparation. Preparing a deployment
	// repairs stored topology, refreshes derived roles and allocates one
	// monotonic config version, so two concurrent applies would interleave those
	// writes and hand overlapping desired state to the same Agents.
	deploymentMu          sync.Mutex
	geoIP                 connectionAuditGeoResolver
	geoIPStatus           model.GeoDatabaseStatus
	routingRuleSetFetcher func(context.Context, model.RoutingRuleSet, bool) (*fetchedRoutingRuleSet, error)
	// tasks is the per-server task wake notifier. SQLite stays the task source
	// of truth; wakes are only hints to claim immediately.
	tasks *taskNotifier
	// taskRecoveryScanMin/Max bound the jittered recovery scan that re-wakes
	// servers with pending tasks after a lost wake. Tests shorten them.
	taskRecoveryScanMin time.Duration
	taskRecoveryScanMax time.Duration
	// configurationWake is a coalesced hint for the durable desired-state
	// reconciler. SQLite configuration_sync_states remains authoritative.
	configurationWake  chan struct{}
	configurationDelay time.Duration
	// routingSnapshotCache is the immutable FullRoutingConfigData + effective
	// access snapshot cache, keyed by the store routing revision.
	routingSnapshotCache atomic.Pointer[routingSnapshot]
	// settingsCache is the revision-keyed ListSettings snapshot used by hot
	// paths (health reports, audit gates).
	settingsCache atomic.Pointer[settingsSnapshot]
	// agentCallbackRate is the process-local budget for authenticated Agent
	// callbacks. It replaces a SQLite write transaction per callback; durable
	// budgets (enrollment, certificate issuance) stay on the store.
	agentCallbackRate *memoryRateLimiter
	// agentAuthFailures counts failed Agent authentications per source address
	// so a decommissioned node holding a revoked token stops reaching the
	// credential lookup.
	agentAuthFailures *memoryRateLimiter
	// auditRisk is the bounded, userID-coalescing audit risk evaluation queue.
	auditRisk *auditRiskQueue
	// accessWorkersWake coalesces wake events for the access change and
	// authorization lifecycle workers; the database remains the recovery
	// fallback for both.
	accessWorkersWake chan struct{}
	// planReconcileWake coalesces hints for the durable subscription-plan
	// reconciler. SQLite plan state remains authoritative.
	planReconcileWake chan struct{}
	nodeRefreshSem    chan struct{}
	nodeRefreshMu     sync.Mutex
	// oauthRefreshMu guards the per-token rotation gates and the replay cache.
	// Concurrent clients sharing one refresh token are serialized and the losers
	// receive the winning token pair, so a benign race no longer looks like
	// refresh token reuse and no longer revokes the token family.
	oauthRefreshMu      sync.Mutex
	oauthRefreshGates   map[string]*oauthRefreshGate
	oauthRefreshReplays map[string]oauthRefreshReplay
	oauthRefreshGrace   time.Duration

	trafficReportsReceivedTotal      atomic.Uint64
	trafficReportsAcceptedTotal      atomic.Uint64
	trafficReportsRejectedTotal      atomic.Uint64
	connectionAuditDiscardedTotal    atomic.Uint64
	trafficPolicyUpdatesTotal        atomic.Uint64
	trafficPolicyRuntimeAppliesTotal atomic.Uint64
	configurationReconcileTotal      atomic.Uint64
	configurationSemanticNoopTotal   atomic.Uint64
	applyDeploymentCreatedTotal      atomic.Uint64
	applyCoreConfigCreatedTotal      atomic.Uint64
	applyTrafficPolicyCreatedTotal   atomic.Uint64
	nodeRefreshUsers                 map[int64]bool
}

type connectionAuditGeoResolver interface {
	Lookup(string) (model.IPGeography, error)
	Status() model.GeoDatabaseStatus
	Close()
}

func New(store *store.Store, sessionSecret, staticDir, basePath string, logs *oboardlog.Manager) *Server {
	acmeCommand := strings.TrimSpace(os.Getenv("OBOARD_ACME_SH"))
	if acmeCommand == "" {
		acmeCommand = "acme.sh"
	}
	acmeHome := strings.TrimSpace(os.Getenv("OBOARD_ACME_HOME"))
	if acmeHome == "" {
		acmeHome = "./data/acme"
	}
	socketPath := strings.TrimSpace(os.Getenv("OBOARD_CONTROLLER_UPDATER_SOCKET"))
	catalog := capability.NewCatalog()
	auditIntel := auditintel.New(store, sessionSecret)
	pollerID, _ := security.RandomToken(12)
	if pollerID == "" {
		pollerID = fmt.Sprintf("controller-%d", time.Now().UnixNano())
	}
	s := &Server{store: store, sessionSecret: sessionSecret, staticDir: staticDir, basePath: basePath, application: application.NewService(store), capabilities: catalog, automation: automation.NewService(store, catalog), auditIntel: auditIntel, auditReviews: auditreview.New(store, auditIntel, sessionSecret), aiModelDiscoveries: newAIModelDiscoveryQueue(), aiModelDiscoveryTimeout: aiModelDiscoveryTimeout, aiTests: newAITaskQueue[airpc.AITestRequest, aiTestResult](), aiTestTimeout: aiTestTimeout, apiInFlight: map[string]int{}, allowedOrigins: parseAllowedOrigins(os.Getenv("OBOARD_CORS_ORIGINS")), dnsEndpoints: defaultDNSProviderEndpoints(), acmeCommand: acmeCommand, acmeHome: acmeHome, logs: logs, realtime: newRealtimeBroker(), activeProbes: map[int64]bool{}, agentConnectionCount: map[int64]int{}, notificationWake: make(chan struct{}, 1), periodicLogNext: map[string]time.Time{}, controllerNTPQuery: queryControllerNTP, notificationSender: sendNotification, telegramAPI: telegramBotHTTP, telegramPollerID: pollerID, certificateIssues: map[int64]bool{}, controllerUpdater: controllerupdate.NewClient(socketPath), geoIPStatus: model.GeoDatabaseStatus{Provider: "ip2region", Error: "IP 归属库不可用"}, subscriptionRelayNonces: map[string]time.Time{}, tasks: newTaskNotifier(), taskRecoveryScanMin: defaultTaskRecoveryScanMin, taskRecoveryScanMax: defaultTaskRecoveryScanMax, configurationWake: make(chan struct{}, 1), configurationDelay: defaultConfigurationReconcileDelay, agentCallbackRate: newMemoryRateLimiter(), agentAuthFailures: newMemoryRateLimiter(), accessWorkersWake: make(chan struct{}, 1), planReconcileWake: make(chan struct{}, 1), nodeRefreshSem: make(chan struct{}, 4), nodeRefreshUsers: map[int64]bool{}, backupJobs: make(chan controllerBackupJob, 4)}
	s.auditRisk = newAuditRiskQueue(s.evaluateConnectionAuditRisks)
	s.oauthRefreshGrace = oauthRefreshReplayGrace
	s.agentLive = map[int64]chan any{}
	s.remoteExecHub = newRemoteExecResultHub()
	s.terminalHub = newTerminalSessionHub()
	s.agentUpdates = newAgentUpdateCoordinator(s)
	s.automation.SetApplyObserver(s.configurationChangesetApplied)
	s.restoreControllerUpdateMaintenance(context.Background())
	s.recoverControllerUpdateRun(context.Background())
	s.initializeTrustedProxies()
	s.registerAutomationHandlers()
	s.restoreBasePathState(context.Background(), basePath)
	s.upgrader = websocket.Upgrader{CheckOrigin: s.checkOrigin, ReadBufferSize: 4096, WriteBufferSize: 4096}
	return s
}

func (s *Server) ConfigureGeoIP(dir string) error {
	database, err := oboardgeoip.Open(dir)
	if err != nil {
		s.geoIPStatus = model.GeoDatabaseStatus{Provider: "ip2region", Error: "IP 归属库缺失或校验失败"}
		return err
	}
	if s.geoIP != nil {
		s.geoIP.Close()
	}
	s.geoIP = database
	s.geoIPStatus = database.Status()
	if err := s.refreshConnectionAuditGeography(context.Background()); err != nil {
		return err
	}
	return s.refreshProxyPathEgressGeography(context.Background())
}

func (s *Server) Close() {
	if s.realtime != nil {
		s.realtime.close()
	}
	if s.geoIP != nil {
		s.geoIP.Close()
	}
	if s.terminalHub != nil {
		s.terminalHub.mu.Lock()
		sessions := make([]*terminalSession, 0, len(s.terminalHub.sessions))
		for _, sess := range s.terminalHub.sessions {
			sessions = append(sessions, sess)
		}
		s.terminalHub.mu.Unlock()
		for _, sess := range sessions {
			sess.mu.Lock()
			if sess.prepareTimer != nil {
				sess.prepareTimer.Stop()
				sess.prepareTimer = nil
			}
			if sess.idleTimer != nil {
				sess.idleTimer.Stop()
				sess.idleTimer = nil
			}
			if sess.absTimer != nil {
				sess.absTimer.Stop()
				sess.absTimer = nil
			}
			sess.mu.Unlock()
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/api/v1/version", s.version)
	mux.HandleFunc("/api/v1/auth/bootstrap", s.bootstrap)
	mux.HandleFunc("/api/v1/auth/registration", s.registrationStatus)
	mux.HandleFunc("/api/v1/auth/register", s.register)
	mux.HandleFunc("/api/v1/auth/login", s.login)
	mux.HandleFunc("/api/v1/auth/totp/verify", s.verifyTOTPLogin)
	mux.HandleFunc("/api/v1/auth/passkey/login/begin", s.passkeyLoginBegin)
	mux.HandleFunc("/api/v1/auth/passkey/login/finish", s.passkeyLoginFinish)
	mux.HandleFunc("/api/v1/auth/session", s.auth(s.restoreSession, model.RoleNone))
	mux.HandleFunc("/api/v1/auth/logout", s.auth(s.logout, model.RoleNone))
	mux.HandleFunc("/api/v1/auth/password", s.auth(s.changePassword, model.RoleNone))
	mux.HandleFunc("/api/v1/me", s.auth(s.me, model.RoleNone))
	mux.HandleFunc("/api/v1/me/authentication", s.auth(s.authenticationStatus, model.RoleNone))
	mux.HandleFunc("/api/v1/me/totp/setup/begin", s.auth(s.totpSetupBegin, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/totp/setup/confirm", s.auth(s.totpSetupConfirm, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/totp/disable", s.auth(s.totpDisable, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/totp/recovery-codes", s.auth(s.totpRecoveryCodes, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/passkeys/register/begin", s.auth(s.passkeyRegisterBegin, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/passkeys/register/finish", s.auth(s.passkeyRegisterFinish, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/passkeys/", s.auth(s.passkeys, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/subscription-age", s.auth(s.selfSubscriptionAge, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/subscription-custom-path", s.auth(s.selfSubscriptionCustomPath, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/devices", s.auth(s.selfUserDevices, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/devices/", s.auth(s.selfUserDevices, model.RoleViewer))
	mux.HandleFunc("/api/v1/page-data", s.auth(s.pageData, model.RoleNone))
	mux.HandleFunc("/api/v1/events", s.auth(s.uiEvents, model.RoleNone))
	mux.HandleFunc("/api/v1/poll-events", s.auth(s.uiPollEvents, model.RoleNone))
	mux.HandleFunc("/api/v1/dashboard/summary", s.auth(s.dashboard, model.RoleOperator))
	mux.HandleFunc("/api/v1/settings/base-path/retry", s.auth(s.settingsBasePathRetry, model.RoleAdmin))
	mux.HandleFunc("/api/v1/settings/base-path/force", s.auth(s.settingsBasePathForce, model.RoleAdmin))
	mux.HandleFunc("/api/v1/settings/base-path/revoke", s.auth(s.settingsBasePathRevoke, model.RoleAdmin))
	mux.HandleFunc("/api/v1/settings", s.auth(s.settings, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-relays", s.auth(s.subscriptionRelays, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-relays/", s.auth(s.subscriptionRelaySubroutes, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update", s.auth(s.controllerUpdate, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/check", s.auth(s.controllerUpdateCheck, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/channel", s.auth(s.controllerUpdateChannel, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/install", s.auth(s.controllerUpdateInstall, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/cancel", s.auth(s.controllerUpdateCancel, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/force-finish", s.auth(s.controllerUpdateForceFinish, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/backups", s.auth(s.controllerUpdateBackups, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/backups/", s.auth(s.controllerUpdateBackupSubroutes, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/activity", s.auth(s.controllerUpdateActivity, model.RoleNone))
	mux.HandleFunc("/api/v1/agent-updates/status", s.auth(s.agentUpdatesStatus, model.RoleAdmin))
	mux.HandleFunc("/api/v1/agent-updates/pause", s.auth(s.agentUpdatesPause, model.RoleAdmin))
	mux.HandleFunc("/api/v1/agent-updates/resume", s.auth(s.agentUpdatesResume, model.RoleAdmin))
	mux.HandleFunc("/api/v1/agent-updates/retry-failed", s.auth(s.agentUpdatesRetryFailed, model.RoleAdmin))
	mux.HandleFunc("/api/v1/backups", s.auth(s.controllerBackups, model.RoleAdmin))
	mux.HandleFunc("/api/v1/backups/settings", s.auth(s.controllerBackupSettings, model.RoleAdmin))
	mux.HandleFunc("/api/v1/backups/settings/test", s.auth(s.controllerBackupTestDestination, model.RoleAdmin))
	mux.HandleFunc("/api/v1/backups/upload", s.auth(s.controllerBackupUpload, model.RoleAdmin))
	mux.HandleFunc("/api/v1/backups/", s.auth(s.controllerBackupSubroutes, model.RoleAdmin))
	mux.HandleFunc("/api/v1/system-logs/download", s.auth(s.systemLogsDownload, model.RoleAdmin))
	mux.HandleFunc("/api/v1/system-logs", s.auth(s.systemLogs, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-credentials", s.auth(s.dnsCredentials, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-credentials/", s.auth(s.dnsCredentialSubroutes, model.RoleAdmin))
	mux.HandleFunc("/api/v1/google-eab-credentials", s.auth(s.googleEABCredentials, model.RoleAdmin))
	mux.HandleFunc("/api/v1/google-eab-credentials/", s.auth(s.googleEABCredentials, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-records", s.auth(s.dnsRecords, model.RoleOperator))
	mux.HandleFunc("/api/v1/dns-sync", s.auth(s.dnsSync, model.RoleOperator))
	mux.HandleFunc("/api/v1/certificates", s.auth(s.certificates, model.RoleOperator))
	mux.HandleFunc("/api/v1/certificates/", s.auth(s.certificateSubroutes, model.RoleOperator))
	mux.HandleFunc("/api/v1/servers", s.auth(s.servers, model.RoleOperator))
	mux.HandleFunc("/api/v1/servers/", s.auth(s.serverSubroutes, model.RoleOperator))
	mux.HandleFunc("/api/v1/agents/update-all", s.auth(s.agentsUpdateAll, model.RoleAdmin))
	mux.HandleFunc("/api/v1/inbounds", s.auth(s.inbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/inbounds/", s.auth(s.inbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/anytls-padding-presets", s.auth(s.anyTLSPaddingPresets, model.RoleOperator))
	mux.HandleFunc("/api/v1/user-groups", s.auth(s.userGroups, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-groups/", s.auth(s.userGroups, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-group-members", s.auth(s.userGroupMembers, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-group-members/", s.auth(s.userGroupMembers, model.RoleAdmin))
	mux.HandleFunc("/api/v1/outbounds", s.auth(s.outbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/outbounds/", s.auth(s.outbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/routing-rules", s.auth(s.routingRules, model.RoleOperator))
	mux.HandleFunc("/api/v1/routing-rules/", s.auth(s.routingRules, model.RoleOperator))
	mux.HandleFunc("/api/v1/family-split-templates", s.auth(s.familySplitTemplates, model.RoleOperator))
	mux.HandleFunc("/api/v1/family-split-templates/", s.auth(s.familySplitTemplates, model.RoleOperator))
	mux.HandleFunc("/api/v1/routing-rule-sets", s.auth(s.routingRuleSets, model.RoleOperator))
	mux.HandleFunc("/api/v1/routing-rule-sets/", s.auth(s.routingRuleSets, model.RoleOperator))
	mux.HandleFunc("/api/v1/routing-rule-catalog", s.auth(s.routingRuleCatalog, model.RoleOperator))
	mux.HandleFunc("/api/v1/external-outbounds", s.auth(s.externalOutbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/external-outbounds/", s.auth(s.externalOutbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/proxy-paths", s.auth(s.proxyPaths, model.RoleOperator))
	mux.HandleFunc("/api/v1/proxy-paths/", s.auth(s.proxyPaths, model.RoleOperator))
	mux.HandleFunc("/api/v1/proxy-path-steps", s.auth(s.proxyPathSteps, model.RoleOperator))
	mux.HandleFunc("/api/v1/proxy-path-steps/", s.auth(s.proxyPathSteps, model.RoleOperator))
	mux.HandleFunc("/api/v1/warp-profiles", s.auth(s.warpProfiles, model.RoleOperator))
	mux.HandleFunc("/api/v1/warp-profiles/", s.auth(s.warpProfiles, model.RoleOperator))
	mux.HandleFunc("/api/v1/users", s.auth(s.users, model.RoleAdmin))
	mux.HandleFunc("/api/v1/users/", s.auth(s.users, model.RoleAdmin))
	mux.HandleFunc("/api/v1/traffic-ledger", s.auth(s.trafficLedger, model.RoleViewer))
	mux.HandleFunc("/api/v1/traffic-ledger/reconcile", s.auth(s.trafficLedgerReconcile, model.RoleAdmin))
	mux.HandleFunc("/api/v1/assignable-nodes", s.auth(s.assignableNodes, model.RoleOperator))
	mux.HandleFunc("/api/v1/assignable-nodes/", s.auth(s.assignableNodeDetail, model.RoleOperator))
	mux.HandleFunc("/api/v1/assignable-node-scopes/preview", s.auth(s.assignableNodeScopePreview, model.RoleOperator))
	mux.HandleFunc("/api/v1/node-workspace", s.auth(s.nodeWorkspace, model.RoleNone))
	mux.HandleFunc("/api/v1/node-groups", s.auth(s.nodeGroups, model.RoleNone))
	mux.HandleFunc("/api/v1/node-groups/", s.auth(s.nodeGroup, model.RoleNone))
	mux.HandleFunc("/api/v1/node-sources/", s.auth(s.nodeSource, model.RoleNone))
	mux.HandleFunc("/api/v1/node-import-preview", s.auth(s.nodeImportPreview, model.RoleNone))
	mux.HandleFunc("/api/v1/node-library", s.auth(s.nodeLibrary, model.RoleNone))
	mux.HandleFunc("/api/v1/node-library/", s.auth(s.nodeLibraryItem, model.RoleNone))
	mux.HandleFunc("/api/v1/subscription-outputs", s.auth(s.subscriptionOutputs, model.RoleNone))
	mux.HandleFunc("/api/v1/subscription-outputs/", s.auth(s.subscriptionOutput, model.RoleNone))
	mux.HandleFunc("/api/v1/node-order-templates", s.auth(s.nodeOrderTemplates, model.RoleAdmin))
	mux.HandleFunc("/api/v1/node-order-templates/", s.auth(s.nodeOrderTemplates, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-plans", s.auth(s.subscriptionPlans, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-plans/", s.auth(s.subscriptionPlans, model.RoleAdmin))
	mux.HandleFunc("/api/v1/users/plan-assignment", s.auth(s.userPlanAssignment, model.RoleAdmin))
	mux.HandleFunc("/api/v1/users/plan-assignment/", s.auth(s.userPlanAssignment, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-node-exceptions", s.auth(s.userNodeExceptions, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-node-exceptions/", s.auth(s.userNodeExceptions, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-node-exceptions/batch", s.auth(s.userNodeExceptionsBatch, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-node-exceptions/batch/", s.auth(s.userNodeExceptionsBatch, model.RoleAdmin))
	mux.HandleFunc("/api/v1/access-changes", s.auth(s.accessChanges, model.RoleAdmin))
	mux.HandleFunc("/api/v1/access-changes/", s.auth(s.accessChanges, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-lists", s.auth(s.dnsLists, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-lists/", s.auth(s.dnsLists, model.RoleAdmin))
	mux.HandleFunc("/api/v1/snell-profiles", s.auth(s.snellProfiles, model.RoleAdmin))
	mux.HandleFunc("/api/v1/snell-profiles/", s.auth(s.snellProfiles, model.RoleAdmin))
	mux.HandleFunc("/api/v1/node-presets", s.auth(s.nodePresets, model.RoleAdmin))
	mux.HandleFunc("/api/v1/node-presets/", s.auth(s.nodePresets, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-templates", s.auth(s.subscriptionTemplates, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-templates/", s.auth(s.subscriptionTemplates, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-benchmarks", s.auth(s.dnsBenchmarks, model.RoleOperator))
	mux.HandleFunc("/api/v1/mtu-detections", s.auth(s.mtuDetections, model.RoleOperator))
	mux.HandleFunc("/api/v1/port-forwards", s.auth(s.portForwards, model.RoleOperator))
	mux.HandleFunc("/api/v1/port-forwards/", s.auth(s.portForwards, model.RoleOperator))
	mux.HandleFunc("/api/v1/tunnels", s.auth(s.tunnels, model.RoleOperator))
	mux.HandleFunc("/api/v1/tunnels/", s.auth(s.tunnels, model.RoleOperator))
	mux.HandleFunc("/api/v1/notification-channels", s.auth(s.notificationChannels, model.RoleViewer))
	mux.HandleFunc("/api/v1/notification-channels/", s.auth(s.notificationChannels, model.RoleViewer))
	mux.HandleFunc("/api/v1/telegram-bot", s.auth(s.telegramBotSettings, model.RoleAdmin))
	mux.HandleFunc("/api/v1/telegram-bot/", s.auth(s.telegramBotSettings, model.RoleAdmin))
	mux.HandleFunc("/api/v1/notification-announcements", s.auth(s.notificationAnnouncements, model.RoleAdmin))
	mux.HandleFunc("/api/v1/port-forward-probes", s.auth(s.portForwardProbes, model.RoleOperator))
	mux.HandleFunc("/api/v1/inbound-probes", s.auth(s.inboundProbes, model.RoleOperator))
	mux.HandleFunc("/api/v1/latency-probe-resource", s.auth(s.latencyProbeResource, model.RoleViewer))
	mux.HandleFunc("/api/v1/configuration-sync", s.auth(s.configurationSync, model.RoleOperator))
	mux.HandleFunc("/api/v1/configuration-sync/retry", s.auth(s.configurationSyncRetry, model.RoleOperator))
	mux.HandleFunc("/api/v1/deployments/apply", s.auth(s.applyDeployment, model.RoleOperator))
	mux.HandleFunc("/api/v1/deployments/", s.auth(s.deployment, model.RoleOperator))
	mux.HandleFunc("/api/v1/agent-tasks", s.auth(s.agentTasks, model.RoleOperator))
	mux.HandleFunc("/api/v1/agent-tasks/", s.auth(s.agentTask, model.RoleOperator))
	mux.HandleFunc("/api/v1/subscriptions", notFound)
	mux.HandleFunc("/api/v1/subscriptions/", s.subscription)
	mux.HandleFunc("/s", notFound)
	mux.HandleFunc("/s/", s.subscriptionCustomPath)
	mux.HandleFunc("/api/v1/audit-logs", s.auth(s.auditLogs, model.RoleAdmin))
	mux.HandleFunc("/api/v1/audit/overview", s.auth(s.connectionAuditOverview, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/users/", s.auth(s.connectionAuditUser, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/risk-overview", s.auth(s.combinedAuditOverview, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/subscriptions/overview", s.auth(s.subscriptionAuditOverview, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/subscriptions/users/", s.auth(s.subscriptionAuditUser, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/ai-reviews", s.auth(s.auditAIReviews, model.RoleAdmin))
	mux.HandleFunc("/api/v1/audit/ai-reviews/", s.auth(s.auditAIReview, model.RoleAdmin))
	mux.HandleFunc("/api/v1/telegram/binding-code", s.apiAuth(s.apiV1TelegramBindingCode, model.RoleNone))
	mux.HandleFunc("/api/v1/telegram/bindings", s.apiAuth(s.apiV1TelegramBindings, model.RoleNone))
	mux.HandleFunc("/api/v1/telegram/bindings/", s.apiAuth(s.apiV1TelegramBindings, model.RoleNone))
	mux.HandleFunc("/api/v1/notification-broadcasts", s.apiAuth(s.apiV1NotificationBroadcasts, model.RoleAdmin))
	mux.HandleFunc("/api/v1/notification-broadcasts/", s.apiAuth(s.apiV1NotificationBroadcasts, model.RoleAdmin))
	machineMux := http.NewServeMux()
	s.registerAPIV1Routes(machineMux)
	s.registerOAuthManagementRoutes(machineMux)
	machineMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeNotFoundJSON(w) })
	s.registerOAuthRoutes(mux)
	mcpHandler := s.newMCPHandler()
	mux.HandleFunc("/api/v1/auth/step-up/begin", s.auth(s.stepUpBegin, model.RoleAdmin))
	mux.HandleFunc("/api/v1/auth/step-up/password", s.auth(s.stepUpPassword, model.RoleAdmin))
	mux.HandleFunc("/api/v1/auth/step-up/passkey/finish", s.auth(s.stepUpPasskeyFinish, model.RoleAdmin))
	mux.HandleFunc("/api/v1/mcp/grants/", s.auth(s.mcpPrivilegedAccess, model.RoleAdmin))
	mux.HandleFunc("/api/v1/remote-access/audit", s.auth(s.remoteAccessAudit, model.RoleAdmin))
	mux.HandleFunc("/api/v1/agent/enroll", s.agentEnroll)
	mux.HandleFunc("/api/v1/agent/connect", s.agentConnect)
	mux.HandleFunc("/api/v1/agent/interactive/", s.agentInteractive)
	mux.HandleFunc("/api/v1/agent/task-results", s.agentTaskResults)
	mux.HandleFunc("/api/v1/agent/assets", s.agentManagedAssets)
	mux.HandleFunc("/api/v1/agent/certificate-issues", s.agentCertificateIssues)
	mux.HandleFunc("/api/v1/agent/traffic-reports", s.agentTrafficReports)
	mux.HandleFunc("/api/v1/agent/connection-reports", s.agentConnectionReports)
	mux.HandleFunc("/api/v1/agent/dns-benchmarks", s.agentDNSBenchmarks)
	mux.HandleFunc("/api/v1/agent/mtu-detections", s.agentMTUDetections)
	mux.HandleFunc("/api/v1/agent/port-forward-probes", s.agentPortForwardProbes)
	mux.HandleFunc("/api/v1/agent/inbound-probes", s.agentInboundProbes)
	mux.HandleFunc("/api/v1/subscription-relay/enroll", s.subscriptionRelayEnroll)
	mux.HandleFunc("/api/v1/subscription-relay/heartbeat", s.subscriptionRelayHeartbeat)
	mux.HandleFunc("/api/v1/subscription-relay/uninstall", s.subscriptionRelayUninstall)
	mux.HandleFunc("/install/agent.sh", s.agentInstallScript)
	mux.HandleFunc("/install/agent-self-update.sh", s.agentSelfUpdateScript)
	mux.HandleFunc("/install/subscription-relay.sh", s.subscriptionRelayInstallScript)
	mux.HandleFunc("/downloads", notFound)
	mux.HandleFunc("/downloads/", s.downloadArtifact)
	mux.HandleFunc("/", s.static)

	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/ui/", s.webAPIPrefix(mux))
	rootMux.HandleFunc("/api/v1/ui", notFound)
	rootMux.Handle("/api/v1/agent/", mux)
	rootMux.HandleFunc("/api/v1/subscriptions", mux.ServeHTTP)
	rootMux.Handle("/api/v1/subscriptions/", mux)
	rootMux.HandleFunc("/s", mux.ServeHTTP)
	rootMux.Handle("/s/", mux)
	rootMux.Handle("/api/v1/subscription-relay/", mux)
	rootMux.HandleFunc("/api/v1/version", mux.ServeHTTP)
	rootMux.Handle("/api/v1/mcp", s.mcpAuth(mcpHandler))
	rootMux.Handle("/api/v1/mcp/terminal/", s.mcpAuth(http.HandlerFunc(s.mcpTerminalStream)))
	rootMux.Handle("/api/v1/", machineMux)
	rootMux.Handle("/install/", mux)
	rootMux.Handle("/downloads/", mux)
	rootMux.HandleFunc("/downloads", notFound)
	rootMux.HandleFunc("/install", notFound)
	rootMux.HandleFunc("/healthz", mux.ServeHTTP)
	rootMux.Handle("/oauth/", mux)
	rootMux.HandleFunc("/.well-known/oauth-authorization-server", mux.ServeHTTP)
	rootMux.HandleFunc("/.well-known/oauth-protected-resource", mux.ServeHTTP)
	rootMux.Handle("/", mux)
	return s.withSubscriptionRelay(s.withTrustedProxyState(s.withBasePath(s.requestLogger(s.withSecurityHeaders(s.realtimeInvalidation(s.apiVersionGate(rootMux)))))))
}

// webAPIPrefix exposes the existing Web handler surface as /api/v1/ui while
// preserving the internal /api/v1 route contracts used by handler path parsers.
func (s *Server) webAPIPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
			writeNotFoundJSON(w)
			return
		}
		request := r.Clone(r.Context())
		request.URL = new(url.URL)
		*request.URL = *r.URL
		request.URL.Path = "/api/v1/" + strings.TrimPrefix(r.URL.Path, "/api/v1/ui/")
		request.URL.RawPath = ""
		next.ServeHTTP(w, request)
	})
}

func (s *Server) apiVersionGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2") || r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/") {
			writeNotFoundJSON(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func NormalizeBasePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || value == "/" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") {
		return "", errors.New("base path must start with /")
	}
	value = strings.TrimRight(value, "/")
	if len(value) > 128 {
		return "", errors.New("base path must not exceed 128 characters")
	}
	if strings.ContainsAny(value, "?#%\\") {
		return "", errors.New("base path must use unescaped URL path characters")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("base path contains an invalid segment")
		}
		for _, char := range segment {
			valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", char)
			if !valid {
				return "", errors.New("base path may contain only letters, numbers, -, ., _, and ~")
			}
		}
	}
	return value, nil
}

func (s *Server) withBasePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ambiguousRequestPath(r.URL.Path, r.URL.RawPath) {
			writeOpaqueNotFound(w)
			return
		}
		if matched, target, ok := s.matchOAuthWellKnownPath(r.URL.Path); ok {
			request := r.Clone(r.Context())
			request.URL = new(url.URL)
			*request.URL = *r.URL
			request.URL.Path = target
			request.URL.RawPath = ""
			request = request.WithContext(context.WithValue(request.Context(), requestBasePathContextKey{}, matched))
			next.ServeHTTP(w, request)
			return
		}
		matched, ok := s.matchBasePath(r.URL.Path)
		if !ok {
			writeOpaqueNotFound(w)
			return
		}
		request := r.Clone(r.Context())
		request.URL = new(url.URL)
		*request.URL = *r.URL
		request.URL.Path = strings.TrimPrefix(r.URL.Path, matched)
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		request.URL.RawPath = ""
		request = request.WithContext(context.WithValue(request.Context(), requestBasePathContextKey{}, matched))
		next.ServeHTTP(w, request)
	})
}

func ambiguousRequestPath(path, rawPath string) bool {
	if rawPath != "" || strings.Contains(path, "//") {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

type responseStatusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseStatusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseStatusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (w *responseStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseStatusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseStatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("websocket hijacking not supported")
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return hijacker.Hijack()
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/v1/ui/poll-events" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/v1/agent/connect" || r.URL.Path == "/api/v1/ui/events" {
			// #nosec G706 -- every request-derived field is stripped of control characters by safeLogField.
			log.Printf("http method=%s path=%s remote=%s status=websocket", safeLogField(r.Method), safeLogField(r.URL.Path), safeLogField(clientIP(r)))
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		recorder := &responseStatusWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		// #nosec G706 -- every request-derived field is stripped of control characters by safeLogField.
		log.Printf("http method=%s path=%s status=%d bytes=%d duration_ms=%d remote=%s", safeLogField(r.Method), safeLogField(requestLogPath(r.URL.Path)), recorder.status, recorder.bytes, time.Since(started).Milliseconds(), safeLogField(clientIP(r)))
	})
}

func safeLogField(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, value)
}

func requestLogPath(path string) string {
	if strings.HasPrefix(path, "/api/v1/subscriptions/") {
		return "/api/v1/subscriptions/[redacted]"
	}
	if strings.HasPrefix(path, "/s/") {
		return "/s/[redacted]"
	}
	return path
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; script-src 'self'; base-uri 'self'; frame-ancestors 'none'")
		if origin := r.Header.Get("Origin"); origin != "" && s.originAllowed(r, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "authorization,content-type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseAllowedOrigins(raw string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out[item] = true
		}
	}
	return out
}

func (s *Server) initializeTrustedProxies() {
	environmentCIDRs, err := normalizeTrustedProxyCIDRs(strings.Split(os.Getenv("OBOARD_TRUSTED_PROXY_CIDRS"), ","))
	if err != nil {
		log.Printf("configure trusted proxy environment: %v", err)
		environmentCIDRs = nil
	}
	s.trustedProxyEnvironmentCIDRs = environmentCIDRs
	settings, err := s.store.ListSettings(context.Background())
	if err != nil {
		log.Printf("load trusted proxy settings: %v", err)
		s.applyTrustedProxyCIDRs(nil)
		return
	}
	configuredCIDRs, err := trustedProxyCIDRsFromSettings(settings)
	if err != nil {
		log.Printf("load trusted proxy settings: %v", err)
		configuredCIDRs = nil
	}
	s.applyTrustedProxyCIDRs(configuredCIDRs)
}

func (s *Server) applyTrustedProxyCIDRs(configuredCIDRs []string) {
	values := make([]string, 0, len(automaticTrustedProxyCIDRs)+len(s.trustedProxyEnvironmentCIDRs)+len(configuredCIDRs))
	values = append(values, automaticTrustedProxyCIDRs...)
	values = append(values, s.trustedProxyEnvironmentCIDRs...)
	values = append(values, configuredCIDRs...)
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		prefix, err := parseTrustedProxyPrefix(value)
		if err != nil || seen[prefix.String()] {
			continue
		}
		seen[prefix.String()] = true
		prefixes = append(prefixes, prefix)
	}
	s.trustedProxies.Store(&trustedProxyState{prefixes: prefixes})
}

func normalizeTrustedProxyCIDRs(values []string) ([]string, error) {
	if len(values) > 64 {
		return nil, errors.New("trusted_proxy_cidrs must contain at most 64 entries")
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := parseTrustedProxyPrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy address %q", value)
		}
		if prefix.Bits() == 0 {
			return nil, errors.New("trusted proxy sources cannot include an all-addresses network")
		}
		if prefix.Addr().IsUnspecified() {
			return nil, errors.New("trusted proxy sources cannot use an unspecified address")
		}
		canonical := prefix.String()
		if !seen[canonical] {
			seen[canonical] = true
			out = append(out, canonical)
		}
	}
	if len(out) > 64 {
		return nil, errors.New("trusted_proxy_cidrs must contain at most 64 entries")
	}
	sort.Strings(out)
	return out, nil
}

func parseTrustedProxyPrefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if addr, err := netip.ParseAddr(value); err == nil {
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr := prefix.Addr()
	bits := prefix.Bits()
	if addr.Is4In6() {
		addr = addr.Unmap()
		bits -= 96
	}
	if bits < 0 || bits > addr.BitLen() {
		return netip.Prefix{}, errors.New("invalid mapped IPv4 prefix")
	}
	return netip.PrefixFrom(addr, bits).Masked(), nil
}

func trustedProxyCIDRsFromSettings(settings map[string]string) ([]string, error) {
	raw := strings.TrimSpace(settings[settingTrustedProxyCIDRs])
	if raw == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, errors.New("stored trusted proxy settings are invalid")
	}
	return normalizeTrustedProxyCIDRs(values)
}

func (s *Server) withTrustedProxyState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), trustedProxyStateContextKey{}, &s.trustedProxies)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func trustedProxyStateForRequest(r *http.Request) *trustedProxyState {
	if source, ok := r.Context().Value(trustedProxyStateContextKey{}).(*atomic.Pointer[trustedProxyState]); ok {
		if state := source.Load(); state != nil {
			return state
		}
	}
	environmentCIDRs, _ := normalizeTrustedProxyCIDRs(strings.Split(os.Getenv("OBOARD_TRUSTED_PROXY_CIDRS"), ","))
	values := append(append([]string{}, automaticTrustedProxyCIDRs...), environmentCIDRs...)
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		if prefix, err := parseTrustedProxyPrefix(value); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return &trustedProxyState{prefixes: prefixes}
}

func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || s.originAllowed(r, origin)
}

func (s *Server) originAllowed(r *http.Request, origin string) bool {
	if origin == "" {
		return true
	}
	if s.allowedOrigins[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"settings": s.publicSettings(r.Context(), items), "reverse_proxy_status": s.reverseProxyStatus(r)})
	case http.MethodPost, http.MethodPatch:
		var req struct {
			ControllerURL                             *string            `json:"controller_url"`
			SubscriptionRelayURL                      *string            `json:"subscription_relay_url"`
			SubscriptionControllerDirect              *bool              `json:"subscription_controller_direct_enabled"`
			BasePath                                  *string            `json:"base_path"`
			CertificateAutoMatch                      *bool              `json:"certificate_auto_match_enabled"`
			CertificatePreference                     *string            `json:"certificate_default_preference"`
			CertificateAutoIssueCA                    *string            `json:"certificate_auto_issue_acme_ca"`
			CertificateAutoIssueEAB                   *int64             `json:"certificate_auto_issue_google_eab_credential_id"`
			SubscriptionAgePolicy                     *string            `json:"subscription_age_policy"`
			SubscriptionAlwaysUseDomainHost           *bool              `json:"subscription_always_use_domain_host"`
			SubscriptionCustomPathMode                *string            `json:"subscription_custom_path_mode"`
			AuditPolicy                               *model.AuditPolicy `json:"audit_policy"`
			AuditEnabled                              *bool              `json:"audit_enabled"`
			SubscriptionAuditEnabled                  *bool              `json:"subscription_audit_enabled"`
			ConnectionAuditEnabled                    *bool              `json:"connection_audit_enabled"`
			AuditAction                               *string            `json:"audit_action"`
			TrafficTimezone                           *string            `json:"traffic_timezone"`
			TrafficEnforcementMode                    *string            `json:"traffic_enforcement_mode"`
			ControllerLogMaxMB                        *int               `json:"controller_log_max_mb"`
			ControllerLogBackups                      *int               `json:"controller_log_backups"`
			ControllerAutoUpdate                      *bool              `json:"controller_auto_update_enabled"`
			ControllerAutoUpdateInterval              *int               `json:"controller_auto_update_interval_hours"`
			AgentAutoUpdate                           *bool              `json:"agent_auto_update_enabled"`
			SubscriptionRelayAutoUpdate               *bool              `json:"subscription_relay_auto_update_enabled"`
			AgentUpdateMaxConcurrency                 *int               `json:"agent_update_max_concurrency"`
			ManagedUpdateStartupQuietSeconds          *int               `json:"managed_update_startup_quiet_seconds"`
			UpdateWindowEnabled                       *bool              `json:"update_window_enabled"`
			UpdateWindowStartHour                     *int               `json:"update_window_start_hour"`
			UpdateWindowEndHour                       *int               `json:"update_window_end_hour"`
			ServerDefaultMTUMode                      *string            `json:"server_default_mtu_mode"`
			ServerDefaultBBREnabled                   *bool              `json:"server_default_bbr_enabled"`
			ServerDefaultTimeCorrection               *string            `json:"server_default_time_correction_mode"`
			ServerMonitoringRetentionDays             *int               `json:"server_monitoring_retention_days"`
			TimeCheckNTPServers                       []string           `json:"time_check_ntp_servers"`
			TrustedProxyCIDRs                         *[]string          `json:"trusted_proxy_cidrs"`
			NotificationOfflineAfter                  *int               `json:"notification_server_offline_after_seconds"`
			NotificationOnlineAfter                   *int               `json:"notification_server_online_after_seconds"`
			NotificationMergeOffline                  *bool              `json:"notification_server_merge_offline"`
			ServerExpiryNotifyLeadDays                *[]int             `json:"server_expiry_notify_lead_days"`
			ServerExpiryNotifyTime                    *string            `json:"server_expiry_notify_time"`
			RegistrationEnabled                       *bool              `json:"registration_enabled"`
			RegistrationDefaultGroupID                *int64             `json:"registration_default_group_id"`
			RemoteTerminalEnabled                     *bool `json:"remote_terminal_enabled"`
			RemoteTerminalPasswordConfirmationEnabled *bool `json:"remote_terminal_password_confirmation_enabled"`
			MCPEnabled                                *bool `json:"mcp_enabled"`
		}
		if !decode(w, r, &req) {
			return
		}
		var normalizedTrustedProxyCIDRs []string
		if req.TrustedProxyCIDRs != nil {
			var err error
			normalizedTrustedProxyCIDRs, err = normalizeTrustedProxyCIDRs(*req.TrustedProxyCIDRs)
			if err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
		}
		if req.BasePath != nil && req.ControllerURL != nil {
			fail(w, errors.New("base_path and controller_url must be updated separately"), http.StatusBadRequest)
			return
		}
		autoIssueSettings := map[string]string{}
		if req.CertificateAutoIssueCA != nil || req.CertificateAutoIssueEAB != nil {
			currentSettings, err := s.store.ListSettings(r.Context())
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			for key, value := range currentSettings {
				autoIssueSettings[key] = value
			}
			if req.CertificateAutoIssueCA != nil {
				autoIssueSettings[settingCertificateAutoIssueACMECA] = strings.ToLower(strings.TrimSpace(*req.CertificateAutoIssueCA))
			}
			if req.CertificateAutoIssueEAB != nil {
				autoIssueSettings[settingCertificateAutoIssueGoogleEABCredential] = strconv.FormatInt(*req.CertificateAutoIssueEAB, 10)
			}
			acmeCA, eabCredentialID, err := automaticCertificateIssuerSettings(autoIssueSettings)
			if err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
			autoIssueSettings = map[string]string{settingCertificateAutoIssueACMECA: acmeCA, settingCertificateAutoIssueGoogleEABCredential: "0"}
			if eabCredentialID > 0 {
				if _, err := s.store.GetGoogleEABCredential(r.Context(), eabCredentialID); err != nil {
					fail(w, errors.New("默认 Google EAB 不存在，请重新选择"), http.StatusBadRequest)
					return
				}
				autoIssueSettings[settingCertificateAutoIssueGoogleEABCredential] = strconv.FormatInt(eabCredentialID, 10)
			}
		}
		changed := []string{}
		redirectPath := ""
		if req.BasePath != nil {
			path, migrated, err := s.startBasePathMigration(r.Context(), r, *req.BasePath)
			if err != nil {
				fail(w, err, migrationConflictStatus(err))
				return
			}
			redirectPath = path
			if migrated {
				changed = append(changed, "base_path")
			}
		}
		if req.ControllerURL != nil {
			if s.basePathState().MigrationVersion > 0 {
				fail(w, errors.New("controller_url cannot be changed during a base path migration"), http.StatusConflict)
				return
			}
			controllerURL, err := s.normalizeControllerURL(*req.ControllerURL)
			if err != nil {
				fail(w, err, 400)
				return
			}
			if err := s.store.SetSetting(r.Context(), "controller_url", controllerURL); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, "controller_url")
		}
		if req.SubscriptionRelayURL != nil {
			relayURL, err := s.normalizeSubscriptionRelayURL(*req.SubscriptionRelayURL)
			if err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
			if relayURL != "" {
				matches, matchErr := s.subscriptionRelayURLMatchesEnrolled(r.Context(), relayURL)
				if matchErr != nil {
					fail(w, matchErr, http.StatusInternalServerError)
					return
				}
				if !matches {
					fail(w, errors.New("订阅中继地址必须与某个已接入中继的公开地址一致；如需关闭请传空字符串"), http.StatusBadRequest)
					return
				}
			}
			if err := s.store.SetSetting(r.Context(), settingSubscriptionRelayURL, relayURL); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, "subscription_relay_url")
		}
		if req.SubscriptionControllerDirect != nil {
			if err := s.store.SetSetting(r.Context(), settingSubscriptionControllerDirectEnabled, strconv.FormatBool(*req.SubscriptionControllerDirect)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingSubscriptionControllerDirectEnabled)
		}
		if req.CertificateAutoMatch != nil {
			if err := s.store.SetSetting(r.Context(), "certificate_auto_match_enabled", strconv.FormatBool(*req.CertificateAutoMatch)); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, "certificate_auto_match_enabled")
		}
		if req.CertificatePreference != nil {
			preference := strings.ToLower(strings.TrimSpace(*req.CertificatePreference))
			if preference != "subdomain" && preference != "wildcard" {
				fail(w, errors.New("certificate_default_preference must be subdomain or wildcard"), 400)
				return
			}
			if err := s.store.SetSetting(r.Context(), "certificate_default_preference", preference); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, "certificate_default_preference")
		}
		if len(autoIssueSettings) > 0 {
			if err := s.store.SetSettings(r.Context(), autoIssueSettings); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingCertificateAutoIssueACMECA, settingCertificateAutoIssueGoogleEABCredential)
		}
		if req.SubscriptionAgePolicy != nil {
			policy := strings.TrimSpace(strings.ToLower(*req.SubscriptionAgePolicy))
			if policy != "optional" && policy != "required" {
				fail(w, errors.New("subscription_age_policy must be optional or required"), 400)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingSubscriptionAgePolicy, policy); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, settingSubscriptionAgePolicy)
		}
		if req.SubscriptionAlwaysUseDomainHost != nil {
			if err := s.store.SetSetting(r.Context(), settingSubscriptionAlwaysUseDomainHost, strconv.FormatBool(*req.SubscriptionAlwaysUseDomainHost)); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, settingSubscriptionAlwaysUseDomainHost)
		}
		if req.SubscriptionCustomPathMode != nil {
			mode := model.SubscriptionCustomPathMode(strings.ToLower(strings.TrimSpace(*req.SubscriptionCustomPathMode)))
			switch mode {
			case model.SubscriptionCustomPathDisabled, model.SubscriptionCustomPathSelective, model.SubscriptionCustomPathEnabled:
			default:
				fail(w, errors.New("subscription_custom_path_mode must be disabled, selective or enabled"), http.StatusBadRequest)
				return
			}
			if err := s.application.SetSubscriptionCustomPathMode(r.Context(), subscriptionCustomPathPrincipal(r), mode); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingSubscriptionCustomPathMode)
		}
		if req.AuditPolicy != nil {
			if err := store.ValidateAuditPolicy(*req.AuditPolicy); err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
			raw, err := json.Marshal(req.AuditPolicy)
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingAuditPolicy, string(raw)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingAuditPolicy)
		}
		if req.AuditEnabled != nil {
			if err := s.store.SetSetting(r.Context(), settingAuditEnabled, strconv.FormatBool(*req.AuditEnabled)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingAuditEnabled)
		}
		if req.SubscriptionAuditEnabled != nil {
			if err := s.store.SetSetting(r.Context(), settingSubscriptionAuditEnabled, strconv.FormatBool(*req.SubscriptionAuditEnabled)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingSubscriptionAuditEnabled)
		}
		if req.ConnectionAuditEnabled != nil {
			if err := s.store.SetSetting(r.Context(), settingConnectionAuditEnabled, strconv.FormatBool(*req.ConnectionAuditEnabled)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingConnectionAuditEnabled)
		}
		if req.AuditAction != nil {
			action := strings.ToLower(strings.TrimSpace(*req.AuditAction))
			if action != string(model.AuditActionRestrict) && action != string(model.AuditActionWarn) {
				fail(w, errors.New("audit_action must be restrict or warn"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingAuditAction, action); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingAuditAction)
		}
		if req.TrafficTimezone != nil {
			tz := strings.TrimSpace(*req.TrafficTimezone)
			if tz == "" {
				tz = "Asia/Shanghai"
			}
			if _, err := time.LoadLocation(tz); err != nil {
				fail(w, errors.New("traffic_timezone is invalid"), 400)
				return
			}
			if err := s.store.SetSetting(r.Context(), "traffic_timezone", tz); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, "traffic_timezone")
		}
		if req.TrafficEnforcementMode != nil {
			mode := strings.TrimSpace(*req.TrafficEnforcementMode)
			switch mode {
			case "", "disconnect_and_reject":
				mode = "disconnect_and_reject"
			case "reject_new":
			default:
				fail(w, errors.New("traffic_enforcement_mode is invalid"), 400)
				return
			}
			if err := s.store.SetSetting(r.Context(), "traffic_enforcement_mode", mode); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, "traffic_enforcement_mode")
		}
		if req.ControllerLogMaxMB != nil {
			if *req.ControllerLogMaxMB < 1 || *req.ControllerLogMaxMB > 1024 {
				fail(w, errors.New("controller_log_max_mb must be between 1 and 1024"), 400)
				return
			}
			if err := s.store.SetSetting(r.Context(), "controller_log_max_mb", strconv.Itoa(*req.ControllerLogMaxMB)); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, "controller_log_max_mb")
		}
		if req.ControllerLogBackups != nil {
			if *req.ControllerLogBackups < 0 || *req.ControllerLogBackups > 20 {
				fail(w, errors.New("controller_log_backups must be between 0 and 20"), 400)
				return
			}
			if err := s.store.SetSetting(r.Context(), "controller_log_backups", strconv.Itoa(*req.ControllerLogBackups)); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, "controller_log_backups")
		}
		if req.ControllerLogMaxMB != nil || req.ControllerLogBackups != nil {
			if err := s.ApplyRuntimeSettings(r.Context()); err != nil {
				fail(w, err, 500)
				return
			}
		}
		if req.ControllerAutoUpdate != nil {
			if *req.ControllerAutoUpdate {
				status, err := s.controllerUpdater.Status(r.Context())
				if err != nil {
					fail(w, errors.New("主控更新器当前不可用，无法开启自动更新"), http.StatusServiceUnavailable)
					return
				}
				if status.Channel == "pinned" {
					fail(w, errors.New("固定版本不能开启自动更新，请先切换更新通道"), http.StatusConflict)
					return
				}
			}
			if err := s.store.SetSetting(r.Context(), controllerAutoUpdateSetting, strconv.FormatBool(*req.ControllerAutoUpdate)); err != nil {
				fail(w, err, 500)
				return
			}
			changed = append(changed, controllerAutoUpdateSetting)
		}
		if req.ControllerAutoUpdateInterval != nil {
			if !validControllerUpdateInterval(*req.ControllerAutoUpdateInterval) {
				fail(w, errors.New("controller_auto_update_interval_hours must be 1, 6, 24, 72 or 168"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), controllerAutoUpdateIntervalSetting, strconv.Itoa(*req.ControllerAutoUpdateInterval)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, controllerAutoUpdateIntervalSetting)
		}
		for key, value := range map[string]*bool{
			agentAutoUpdateSetting:             req.AgentAutoUpdate,
			subscriptionRelayAutoUpdateSetting: req.SubscriptionRelayAutoUpdate,
			updateWindowEnabledSetting:         req.UpdateWindowEnabled,
		} {
			if value == nil {
				continue
			}
			if err := s.store.SetSetting(r.Context(), key, strconv.FormatBool(*value)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, key)
			if (key == agentAutoUpdateSetting || key == subscriptionRelayAutoUpdateSetting) && *value && s.agentUpdates != nil {
				s.agentUpdates.Wake()
			}
		}
		if req.AgentUpdateMaxConcurrency != nil {
			if *req.AgentUpdateMaxConcurrency < 0 || *req.AgentUpdateMaxConcurrency > 32 {
				fail(w, errors.New("agent_update_max_concurrency must be between 0 and 32"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), agentUpdateMaxConcurrencySetting, strconv.Itoa(*req.AgentUpdateMaxConcurrency)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, agentUpdateMaxConcurrencySetting)
		}
		if req.ManagedUpdateStartupQuietSeconds != nil {
			if *req.ManagedUpdateStartupQuietSeconds < 0 || *req.ManagedUpdateStartupQuietSeconds > 300 {
				fail(w, errors.New("managed_update_startup_quiet_seconds must be between 0 and 300"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), managedUpdateStartupQuietSetting, strconv.Itoa(*req.ManagedUpdateStartupQuietSeconds)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, managedUpdateStartupQuietSetting)
		}
		for key, value := range map[string]*int{
			updateWindowStartHourSetting: req.UpdateWindowStartHour,
			updateWindowEndHourSetting:   req.UpdateWindowEndHour,
		} {
			if value == nil {
				continue
			}
			if *value < 0 || *value > 23 {
				fail(w, errors.New(key+" must be between 0 and 23"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), key, strconv.Itoa(*value)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, key)
		}
		if req.ServerDefaultMTUMode != nil {
			mode := model.MTUMode(strings.ToLower(strings.TrimSpace(*req.ServerDefaultMTUMode)))
			switch mode {
			case model.MTUModeDisabled, model.MTUModeDetect, model.MTUModeApply:
			default:
				fail(w, errors.New("server_default_mtu_mode is invalid"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingServerDefaultMTUMode, string(mode)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingServerDefaultMTUMode)
		}
		if req.ServerDefaultBBREnabled != nil {
			if err := s.store.SetSetting(r.Context(), settingServerDefaultBBREnabled, strconv.FormatBool(*req.ServerDefaultBBREnabled)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingServerDefaultBBREnabled)
		}
		if req.ServerDefaultTimeCorrection != nil {
			mode := normalizeControllerTimeCorrectionMode(model.TimeCorrectionMode(*req.ServerDefaultTimeCorrection))
			if mode != model.TimeCorrectionMode(strings.ToLower(strings.TrimSpace(*req.ServerDefaultTimeCorrection))) {
				fail(w, errors.New("server_default_time_correction_mode is invalid"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingServerDefaultTimeCorrection, string(mode)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingServerDefaultTimeCorrection)
		}
		if req.ServerMonitoringRetentionDays != nil {
			if *req.ServerMonitoringRetentionDays < store.MinServerMonitoringRetentionDays || *req.ServerMonitoringRetentionDays > store.MaxServerMonitoringRetentionDays {
				fail(w, fmt.Errorf("server_monitoring_retention_days must be between %d and %d", store.MinServerMonitoringRetentionDays, store.MaxServerMonitoringRetentionDays), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingServerMonitoringRetentionDays, strconv.Itoa(*req.ServerMonitoringRetentionDays)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingServerMonitoringRetentionDays)
		}
		if req.TimeCheckNTPServers != nil {
			servers, err := normalizeTimeCheckNTPServers(req.TimeCheckNTPServers)
			if err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
			raw, _ := json.Marshal(servers)
			if err := s.store.SetSetting(r.Context(), settingTimeCheckNTPServers, string(raw)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingTimeCheckNTPServers)
		}
		if req.TrustedProxyCIDRs != nil {
			raw, _ := json.Marshal(normalizedTrustedProxyCIDRs)
			if err := s.store.SetSetting(r.Context(), settingTrustedProxyCIDRs, string(raw)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			s.applyTrustedProxyCIDRs(normalizedTrustedProxyCIDRs)
			changed = append(changed, settingTrustedProxyCIDRs)
		}
		if req.NotificationOfflineAfter != nil {
			if *req.NotificationOfflineAfter < 30 || *req.NotificationOfflineAfter > 86400 {
				fail(w, errors.New("notification_server_offline_after_seconds must be between 30 and 86400"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingNotificationServerOfflineAfter, strconv.Itoa(*req.NotificationOfflineAfter)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingNotificationServerOfflineAfter)
		}
		if req.NotificationOnlineAfter != nil {
			if *req.NotificationOnlineAfter < 0 || *req.NotificationOnlineAfter > 86400 {
				fail(w, errors.New("notification_server_online_after_seconds must be between 0 and 86400"), http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingNotificationServerOnlineAfter, strconv.Itoa(*req.NotificationOnlineAfter)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingNotificationServerOnlineAfter)
		}
		if req.NotificationMergeOffline != nil {
			if err := s.store.SetSetting(r.Context(), settingNotificationServerMergeOffline, strconv.FormatBool(*req.NotificationMergeOffline)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingNotificationServerMergeOffline)
		}
		if req.ServerExpiryNotifyLeadDays != nil {
			leadDays, err := normalizeExpiryNotifyLeadDays(*req.ServerExpiryNotifyLeadDays)
			if err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
			raw, _ := json.Marshal(leadDays)
			if err := s.store.SetSetting(r.Context(), settingServerExpiryNotifyLeadDays, string(raw)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingServerExpiryNotifyLeadDays)
		}
		if req.ServerExpiryNotifyTime != nil {
			notifyTime, err := normalizeExpiryNotifyTime(*req.ServerExpiryNotifyTime)
			if err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingServerExpiryNotifyTime, notifyTime); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingServerExpiryNotifyTime)
		}
		if req.RegistrationEnabled != nil {
			if err := s.store.SetSetting(r.Context(), settingRegistrationEnabled, strconv.FormatBool(*req.RegistrationEnabled)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingRegistrationEnabled)
		}
		if req.RegistrationDefaultGroupID != nil {
			groupID := *req.RegistrationDefaultGroupID
			if groupID < 0 {
				fail(w, errors.New("registration_default_group_id is invalid"), http.StatusBadRequest)
				return
			}
			if groupID > 0 {
				group, err := s.store.GetUserGroup(r.Context(), groupID)
				if err != nil {
					fail(w, errors.New("默认注册用户组不存在"), http.StatusBadRequest)
					return
				}
				if group.SystemKey == store.UserGroupSystemAdmins {
					fail(w, errors.New("默认注册用户组不能是系统管理员组"), http.StatusBadRequest)
					return
				}
			}
			if err := s.store.SetSetting(r.Context(), settingRegistrationDefaultGroupID, strconv.FormatInt(groupID, 10)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingRegistrationDefaultGroupID)
		}
		for _, item := range []struct {
			value *bool
			key   string
		}{
			{req.RemoteTerminalEnabled, settingRemoteTerminalEnabled},
			{req.RemoteTerminalPasswordConfirmationEnabled, settingRemoteTerminalPasswordConfirmationEnabled},
			{req.MCPEnabled, settingMCPEnabled},
		} {
			if item.value == nil {
				continue
			}
			if err := s.store.SetSetting(r.Context(), item.key, strconv.FormatBool(*item.value)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, item.key)
		}
		if len(changed) > 0 {
			s.invalidateConnectionAuditCache()
			auditReq(s, r, "update", "settings", strings.Join(changed, ","))
			for _, key := range changed {
				if key == "traffic_enforcement_mode" || key == "traffic_timezone" {
					if err := s.queueApplyTrafficPolicyForAllAccounting(r.Context(), "traffic_policy_changed"); err != nil {
						logConfigurationError("queue traffic policy after settings", err)
					}
					break
				}
			}
		}
		items, err := s.store.ListSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		if len(changed) > 0 {
			s.handleGlobalRemoteAccessChange(r.Context(), changed, items)
		}
		if req.TrustedProxyCIDRs != nil {
			s.refreshSecureSessionCookie(w, r)
		}
		response := map[string]any{"settings": s.publicSettings(r.Context(), items), "reverse_proxy_status": s.reverseProxyStatus(r)}
		if redirectPath != "" {
			response["redirect_path"] = redirectPath
		}
		write(w, 200, response)
	default:
		method(w)
	}
}

func (s *Server) publicSettings(ctx context.Context, items map[string]string) map[string]any {
	out := map[string]any{"certificate_auto_match_enabled": true, "certificate_default_preference": "subdomain", settingCertificateAutoIssueACMECA: "letsencrypt", settingCertificateAutoIssueGoogleEABCredential: 0, "subscription_age_policy": "optional", settingSubscriptionAlwaysUseDomainHost: false, settingSubscriptionCustomPathMode: string(model.SubscriptionCustomPathDisabled), settingSubscriptionControllerDirectEnabled: false, settingAuditPolicy: store.DefaultAuditPolicy(), settingAuditEnabled: true, settingSubscriptionAuditEnabled: true, settingConnectionAuditEnabled: true, settingAuditAction: string(model.AuditActionRestrict), "traffic_timezone": "Asia/Shanghai", "traffic_enforcement_mode": "disconnect_and_reject", "controller_log_max_mb": "32", "controller_log_backups": "5", controllerAutoUpdateSetting: false, controllerAutoUpdateIntervalSetting: controllerUpdateDefaultIntervalHours, settingServerDefaultMTUMode: string(model.MTUModeDetect), settingServerDefaultBBREnabled: true, settingServerDefaultTimeCorrection: string(model.TimeCorrectionAuto), settingServerMonitoringRetentionDays: store.DefaultServerMonitoringRetentionDays, settingTimeCheckNTPServers: append([]string(nil), defaultTimeCheckNTPServers...), settingTrustedProxyCIDRs: []string{}, settingNotificationServerOfflineAfter: defaultNotificationOfflineAfterSeconds, settingNotificationServerOnlineAfter: defaultNotificationOnlineAfterSeconds, settingNotificationServerMergeOffline: true, settingServerExpiryNotifyLeadDays: append([]int(nil), defaultServerExpiryNotifyLeadDays...), settingServerExpiryNotifyTime: defaultServerExpiryNotifyTime, settingRegistrationEnabled: false, settingRegistrationDefaultGroupID: int64(0), settingRemoteTerminalEnabled: true, settingRemoteTerminalPasswordConfirmationEnabled: true, settingMCPEnabled: false, "trusted_proxy_environment_cidrs": append([]string(nil), s.trustedProxyEnvironmentCIDRs...)}
	out[agentAutoUpdateSetting] = false
	out[subscriptionRelayAutoUpdateSetting] = false
	out[agentUpdateMaxConcurrencySetting] = 0
	out[managedUpdateStartupQuietSetting] = agentUpdateDefaultQuietSeconds
	out[updateWindowEnabledSetting] = false
	out[updateWindowStartHourSetting] = updateWindowDefaultStartHour
	out[updateWindowEndHourSetting] = updateWindowDefaultEndHour
	for key, value := range items {
		if strings.HasPrefix(key, "controller_base_path") || key == controllerBackupSetting || key == controllerBackupTargetBuildSetting || key == controllerUpdateErrorSetting || key == controllerAutoUpdateSetting || key == controllerAutoUpdateIntervalSetting || key == settingAuditPolicy || key == settingTrustedProxyCIDRs || key == settingRegistrationEnabled || key == settingRegistrationDefaultGroupID {
			continue
		}
		out[key] = value
	}
	out[controllerAutoUpdateSetting] = settingBool(items, controllerAutoUpdateSetting, false)
	out[controllerAutoUpdateIntervalSetting] = controllerUpdateIntervalHours(items)
	out[agentAutoUpdateSetting] = settingBool(items, agentAutoUpdateSetting, false)
	out[subscriptionRelayAutoUpdateSetting] = settingBool(items, subscriptionRelayAutoUpdateSetting, false)
	out[agentUpdateMaxConcurrencySetting] = settingInt(items, agentUpdateMaxConcurrencySetting, 0, 0, 32)
	out[managedUpdateStartupQuietSetting] = settingInt(items, managedUpdateStartupQuietSetting, agentUpdateDefaultQuietSeconds, 0, 300)
	out[settingServerMonitoringRetentionDays] = store.ServerMonitoringRetentionDays(items)
	out[updateWindowEnabledSetting] = settingBool(items, updateWindowEnabledSetting, false)
	out[updateWindowStartHourSetting] = updateWindowHour(items, updateWindowStartHourSetting, updateWindowDefaultStartHour)
	out[updateWindowEndHourSetting] = updateWindowHour(items, updateWindowEndHourSetting, updateWindowDefaultEndHour)
	out[settingSubscriptionControllerDirectEnabled] = settingBool(items, settingSubscriptionControllerDirectEnabled, false)
	out[settingSubscriptionAlwaysUseDomainHost] = settingBool(items, settingSubscriptionAlwaysUseDomainHost, false)
	if leadDays, err := parseExpiryNotifyLeadDays(items[settingServerExpiryNotifyLeadDays]); err == nil {
		out[settingServerExpiryNotifyLeadDays] = leadDays
	}
	if notifyTime, err := normalizeExpiryNotifyTime(items[settingServerExpiryNotifyTime]); err == nil {
		out[settingServerExpiryNotifyTime] = notifyTime
	}
	if raw := strings.TrimSpace(items[settingRegistrationEnabled]); raw != "" {
		out[settingRegistrationEnabled] = settingBool(items, settingRegistrationEnabled, false)
	}
	if raw := strings.TrimSpace(items[settingRegistrationDefaultGroupID]); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id >= 0 {
			out[settingRegistrationDefaultGroupID] = id
		}
	}
	if values, err := trustedProxyCIDRsFromSettings(items); err == nil {
		out[settingTrustedProxyCIDRs] = values
	}
	if raw := strings.TrimSpace(items[settingAuditPolicy]); raw != "" {
		var policy model.AuditPolicy
		if json.Unmarshal([]byte(raw), &policy) == nil && store.ValidateAuditPolicy(policy) == nil {
			out[settingAuditPolicy] = policy
		}
	}
	if raw := strings.TrimSpace(items[settingTimeCheckNTPServers]); raw != "" {
		var servers []string
		if json.Unmarshal([]byte(raw), &servers) == nil {
			if normalized, err := normalizeTimeCheckNTPServers(servers); err == nil {
				out[settingTimeCheckNTPServers] = normalized
			}
		}
	}
	if raw, ok := out["controller_url"].(string); ok && strings.TrimSpace(raw) != "" {
		if normalized, err := s.normalizeControllerURL(raw); err == nil {
			out["controller_url"] = normalized
		}
	}
	if raw, ok := out[settingSubscriptionRelayURL].(string); ok && strings.TrimSpace(raw) != "" {
		if normalized, err := s.normalizeSubscriptionRelayURL(raw); err == nil {
			out[settingSubscriptionRelayURL] = normalized
		} else if value, err := s.subscriptionPublicBaseURL(ctx); err == nil && value != "" {
			out[settingSubscriptionRelayURL] = value
		} else {
			out[settingSubscriptionRelayURL] = ""
		}
	}
	out["base_path"] = s.currentBasePath()
	out[settingRemoteTerminalEnabled] = settingBool(items, settingRemoteTerminalEnabled, true)
	out[settingRemoteTerminalPasswordConfirmationEnabled] = settingBool(items, settingRemoteTerminalPasswordConfirmationEnabled, true)
	out[settingMCPEnabled] = settingBool(items, settingMCPEnabled, false)
	if migration, err := s.basePathMigrationProgress(ctx); err == nil {
		out["base_path_migration"] = migration
	}
	if pageCount, pageSize, freelist, err := s.store.DatabasePageStats(ctx); err == nil {
		dbBytes := pageCount * pageSize
		if pageCount > 0 && dbBytes > 512<<20 && float64(freelist)/float64(pageCount) > 0.25 {
			out["database_maintenance_hint"] = "建议进行数据库维护"
		}
	}
	return out
}

func serverCreationDefaults(settings map[string]string) (model.MTUMode, bool, model.TimeCorrectionMode) {
	mode := model.MTUMode(strings.ToLower(strings.TrimSpace(settings[settingServerDefaultMTUMode])))
	switch mode {
	case model.MTUModeDisabled, model.MTUModeDetect, model.MTUModeApply:
	default:
		mode = model.MTUModeDetect
	}
	return mode, settingBool(settings, settingServerDefaultBBREnabled, true), normalizeControllerTimeCorrectionMode(model.TimeCorrectionMode(settings[settingServerDefaultTimeCorrection]))
}

func normalizeControllerTimeCorrectionMode(mode model.TimeCorrectionMode) model.TimeCorrectionMode {
	switch model.TimeCorrectionMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case model.TimeCorrectionOff:
		return model.TimeCorrectionOff
	case model.TimeCorrectionNTP:
		return model.TimeCorrectionNTP
	default:
		return model.TimeCorrectionAuto
	}
}

func normalizeTimeCheckNTPServers(values []string) ([]string, error) {
	if len(values) != 3 {
		return nil, errors.New("time_check_ntp_servers must contain exactly three servers")
	}
	out := make([]string, 0, 3)
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || strings.Contains(value, "://") || strings.Contains(value, "/") {
			return nil, errors.New("NTP 服务器必须是主机名或 IP")
		}
		if strings.HasPrefix(value, "[") {
			if !strings.HasSuffix(value, "]") {
				return nil, errors.New("NTP 服务器不能包含端口")
			}
			value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
			if net.ParseIP(value) == nil {
				return nil, errors.New("NTP 服务器包含无效 IPv6 地址")
			}
		}
		if err := core.ValidateSafeHost(value); err != nil {
			return nil, fmt.Errorf("invalid NTP server %q: %w", value, err)
		}
		if seen[value] {
			return nil, errors.New("NTP 服务器不能重复")
		}
		seen[value] = true
		out = append(out, value)
	}
	return out, nil
}

func timeCheckNTPServers(settings map[string]string) []string {
	var values []string
	if json.Unmarshal([]byte(settings[settingTimeCheckNTPServers]), &values) == nil {
		if normalized, err := normalizeTimeCheckNTPServers(values); err == nil {
			return normalized
		}
	}
	return append([]string(nil), defaultTimeCheckNTPServers...)
}

func (s *Server) ApplyRuntimeSettings(ctx context.Context) error {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return err
	}
	trustedProxyCIDRs, err := trustedProxyCIDRsFromSettings(settings)
	if err != nil {
		return err
	}
	s.applyTrustedProxyCIDRs(trustedProxyCIDRs)
	if s.logs == nil {
		return nil
	}
	maxMB := settingInt(settings, "controller_log_max_mb", 32, 1, 1024)
	backups := settingInt(settings, "controller_log_backups", 5, 0, 20)
	return s.logs.Configure(oboardlog.Config{MaxBytes: int64(maxMB) << 20, Backups: backups})
}

func settingInt(settings map[string]string, key string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(settings[key]))
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}

func (s *Server) normalizeControllerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("controller_url must be a valid http(s) URL")
	}
	if _, err := security.ValidateControllerURL(raw, version.IsDev(), false); err != nil {
		return "", err
	}
	if u.Scheme == "ws" {
		u.Scheme = "http"
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	}
	basePath := s.currentBasePath()
	u.Path = strings.TrimRight(u.Path, "/")
	if basePath != "" {
		switch u.Path {
		case "":
			u.Path = basePath
		case basePath:
		default:
			return "", fmt.Errorf("controller_url path must be %s", basePath)
		}
	} else if u.Path != "" {
		return "", errors.New("controller_url path must be /")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func validateAgentManagedCommand(field, value string) error {
	switch strings.TrimSpace(value) {
	case "", "auto", "none", "systemd-reload", "systemd-restart", "openrc-reload", "openrc-restart", "chrony", "systemd-timesyncd":
		return nil
	default:
		return fmt.Errorf("%s only allows auto, none, or a managed preset", field)
	}
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if err := s.store.CheckHealth(r.Context()); err != nil {
		fail(w, err, http.StatusServiceUnavailable)
		return
	}
	write(w, 200, map[string]any{"ok": true, "service": "oboard-controller"})
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	write(w, 200, s.currentVersionInfo())
}

func (s *Server) currentVersionInfo() model.VersionInfo {
	return model.VersionInfo{
		Name:                 "OBoard",
		Version:              version.Version,
		Build:                version.Build,
		Commit:               version.Commit,
		BuiltAt:              version.Date,
		Dev:                  version.IsDev(),
		AgentExpectedVersion: version.AgentVersion,
		AgentExpectedBuild:   version.AgentBuild,
		AgentUpdateRepo:      defaultAgentUpdateRepo,
		KernelVersion:        version.KernelVersion,
		KernelBuild:          version.KernelBuild,
		Protocols:            []string{"vless", "hy2", "anytls", "shadowsocks", "mieru", "socks"},
		Kernel:               "oboard-sb (sing-box compatible)",
		APIPrefix:            s.currentBasePath() + "/api/v1",
	}
}

func (s *Server) pageData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	page := strings.TrimSpace(r.URL.Query().Get("page"))
	if page == "" {
		page = "dashboard"
	}
	role := currentRole(r)
	require := func(min model.Role) error {
		if !roleAllows(role, min) {
			return errors.New("forbidden")
		}
		return nil
	}
	out := map[string]any{"version": s.currentVersionInfo(), "session": map[string]any{"role": role}}
	if user := currentUser(r); user != nil {
		out["current_user"] = sessionUserResponse(*user, role)
	}
	timing := newPageStageTimer(page)
	ctx := r.Context()
	var serverSnapshot []model.Server
	serverSnapshotLoaded := false
	var settingsSnapshot map[string]string
	settingsSnapshotLoaded := false
	loadSettingsSnapshot := func() (map[string]string, error) {
		if !settingsSnapshotLoaded {
			items, err := s.store.ListSettings(ctx)
			if err != nil {
				return nil, err
			}
			settingsSnapshot = items
			settingsSnapshotLoaded = true
		}
		return settingsSnapshot, nil
	}
	addServerSnapshot := func() error {
		if !serverSnapshotLoaded {
			items, err := s.store.ListServers(ctx)
			if err != nil {
				return err
			}
			serverSnapshot = items
			serverSnapshotLoaded = true
		}
		out["servers"] = serverSnapshot
		return nil
	}
	addServers := func() error {
		return addServerSnapshot()
	}
	addSettings := func() error {
		items, err := loadSettingsSnapshot()
		if err != nil {
			return err
		}
		out["settings"] = s.publicSettings(ctx, items)
		out["reverse_proxy_status"] = s.reverseProxyStatus(r)
		return nil
	}
	addSubscriptionPublicBaseURL := func() error {
		value, err := s.subscriptionPublicBaseURL(ctx)
		if err != nil {
			return err
		}
		out["subscription_public_base_url"] = value
		return nil
	}
	addServerCreationDefaults := func() error {
		settings, err := loadSettingsSnapshot()
		if err != nil {
			return err
		}
		mtuMode, bbrEnabled, timeMode := serverCreationDefaults(settings)
		out["server_creation_defaults"] = map[string]any{"mtu_mode": mtuMode, "bbr_enabled": bbrEnabled, "time_correction_mode": timeMode, "public_port_range_start": core.DefaultPublicPortRangeStart, "public_port_range_end": core.DefaultPublicPortRangeEnd, "internal_port_range_start": core.DefaultInternalPortRangeStart, "internal_port_range_end": core.DefaultInternalPortRangeEnd}
		return nil
	}
	addUsers := func() error {
		items, err := s.store.ListUsers(ctx)
		if err != nil {
			return err
		}
		out["users"] = s.withTrafficStatus(ctx, items)
		return nil
	}
	addGroups := func() error {
		groups, err := s.subscriptionCustomPathGroups(ctx)
		if err != nil {
			return err
		}
		members, err := s.store.ListUserGroupMembers(ctx)
		if err != nil {
			return err
		}
		out["user_groups"] = groups
		out["user_group_members"] = members
		return nil
	}
	var (
		inbounds      []model.Inbound
		outbounds     []model.Outbound
		rules         []model.RoutingRule
		ruleSets      []model.RoutingRuleSet
		templates     []model.FamilySplitTemplate
		externals     []model.ExternalOutbound
		paths         []model.ProxyPath
		steps         []model.ProxyPathStep
		egressResults []model.ProxyPathEgressResult
		forwards      []model.PortForward
		tunnels       []model.Tunnel
		warps         []model.WARPProfile
		dnsLists      []model.DNSList
		dnsPolicies   []model.ServerDNSPolicy
		snellProfiles []model.SnellProfile
		nodePresets   []model.NodePreset
		inboundProbes []model.InboundProbeResult
		forwardProbes []model.PortForwardProbeResult
	)
	addProxy := func() error {
		if err := timing.run("servers", addServers); err != nil {
			return err
		}
		if err := timing.run("inbounds", func() error {
			var listErr error
			inbounds, listErr = s.store.ListInbounds(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("outbounds", func() error {
			var listErr error
			outbounds, listErr = s.store.ListOutbounds(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("routing_rules", func() error {
			var listErr error
			rules, listErr = s.store.ListRoutingRules(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("routing_rule_sets", func() error {
			var listErr error
			ruleSets, listErr = s.store.ListRoutingRuleSets(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("family_split_templates", func() error {
			var listErr error
			templates, listErr = s.store.ListFamilySplitTemplates(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("external_outbounds", func() error {
			var listErr error
			externals, listErr = s.store.ListExternalOutbounds(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("proxy_paths", func() error {
			var listErr error
			paths, listErr = s.store.ListProxyPaths(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("proxy_steps", func() error {
			var listErr error
			steps, listErr = s.store.ListProxyPathSteps(ctx)
			return listErr
		}); err != nil {
			return err
		}
		paths = core.ResolveProxyPathNames(paths, steps, serverSnapshot, inbounds, externals)
		if err := timing.run("egress", func() error {
			var listErr error
			egressResults, listErr = s.store.ListProxyPathEgressResults(ctx)
			return listErr
		}); err != nil {
			return err
		}
		paths, externals = core.ResolveProxyPathExitRegions(paths, steps, serverSnapshot, inbounds, externals, egressResults)
		if err := timing.run("forwards", func() error {
			var listErr error
			forwards, listErr = s.store.ListPortForwards(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("tunnels", func() error {
			var listErr error
			tunnels, listErr = s.store.ListTunnels(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("warp", func() error {
			var listErr error
			warps, listErr = s.store.ListWARPProfiles(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("dns", func() error {
			var listErr error
			dnsLists, listErr = s.store.ListDNSLists(ctx, false)
			if listErr != nil {
				return listErr
			}
			dnsPolicies, listErr = s.store.ListServerDNSPolicies(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("snell", func() error {
			var listErr error
			snellProfiles, listErr = s.store.ListSnellProfiles(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("node_presets", func() error {
			var listErr error
			nodePresets, listErr = s.store.ListNodePresets(ctx)
			return listErr
		}); err != nil {
			return err
		}
		if err := timing.run("probes", func() error {
			var listErr error
			inboundProbes, listErr = s.store.ListInboundProbeResults(ctx, 0, 0, 200)
			if listErr != nil {
				return listErr
			}
			forwardProbes, listErr = s.store.ListPortForwardProbeResults(ctx, 0, 0, 200)
			return listErr
		}); err != nil {
			return err
		}
		out["inbounds"] = inbounds
		out["outbounds"] = outbounds
		out["routing_rules"] = rules
		out["routing_rule_sets"] = ruleSets
		out["family_split_templates"] = templates
		out["external_outbounds"] = externals
		out["proxy_paths"] = paths
		out["proxy_path_steps"] = publicProxyPathSteps(steps)
		out["port_forwards"] = forwards
		out["tunnels"] = publicTunnels(tunnels)
		out["warp_profiles"] = publicWARPProfiles(warps)
		out["dns_lists"] = dnsLists
		out["server_dns_policies"] = dnsPolicies
		out["snell_profiles"] = snellProfiles
		out["node_presets"] = nodePresets
		out["anytls_padding_presets"] = core.AnyTLSPaddingPresets()
		out["inbound_probes"] = inboundProbes
		out["port_forward_probes"] = forwardProbes
		if roleAllows(role, model.RoleAdmin) {
			if err := timing.run("admin_extras", func() error {
				dnsCredentials, listErr := s.store.ListDNSCredentials(ctx)
				if listErr != nil {
					return listErr
				}
				certificates, listErr := s.store.ListCertificates(ctx)
				if listErr != nil {
					return listErr
				}
				out["dns_credentials"] = dnsCredentials
				out["certificates"] = certificates
				users, listErr := s.store.ListUsers(ctx)
				if listErr != nil {
					return listErr
				}
				groups, listErr := s.store.ListUserGroups(ctx)
				if listErr != nil {
					return listErr
				}
				members, listErr := s.store.ListUserGroupMembers(ctx)
				if listErr != nil {
					return listErr
				}
				settings, listErr := loadSettingsSnapshot()
				if listErr != nil {
					return listErr
				}
				users = s.withTrafficStatus(ctx, users)
				if err := s.enrichSubscriptionCustomPaths(ctx, users, groups, members); err != nil {
					return err
				}
				out["users"] = users
				out["user_groups"] = groups
				out["user_group_members"] = members
				out["settings"] = s.publicSettings(ctx, settings)
				out["reverse_proxy_status"] = s.reverseProxyStatus(r)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}
	addInbounds := func() error {
		if err := addServers(); err != nil {
			return err
		}
		items, err := s.store.ListInbounds(ctx)
		if err != nil {
			return err
		}
		out["inbounds"] = items
		profiles, err := s.store.ListSnellProfiles(ctx)
		if err != nil {
			return err
		}
		out["snell_profiles"] = profiles
		nodePresets, err := s.store.ListNodePresets(ctx)
		if err != nil {
			return err
		}
		out["node_presets"] = nodePresets
		out["anytls_padding_presets"] = core.AnyTLSPaddingPresets()
		probes, err := s.store.ListInboundProbeResults(ctx, 0, 0, 200)
		if err != nil {
			return err
		}
		out["inbound_probes"] = probes
		if roleAllows(role, model.RoleAdmin) {
			dnsCredentials, err := s.store.ListDNSCredentials(ctx)
			if err != nil {
				return err
			}
			certificates, err := s.store.ListCertificates(ctx)
			if err != nil {
				return err
			}
			out["dns_credentials"] = dnsCredentials
			out["certificates"] = certificates
			users, err := s.store.ListUsers(ctx)
			if err != nil {
				return err
			}
			out["users"] = users
		}
		return nil
	}
	addOutbounds := func() error {
		if err := addServers(); err != nil {
			return err
		}
		items, err := s.store.ListOutbounds(ctx)
		if err != nil {
			return err
		}
		out["outbounds"] = items
		return nil
	}
	addRouting := func() error {
		if err := addServers(); err != nil {
			return err
		}
		rules, err := s.store.ListRoutingRules(ctx)
		if err != nil {
			return err
		}
		ruleSets, err := s.store.ListRoutingRuleSets(ctx)
		if err != nil {
			return err
		}
		templates, err := s.store.ListFamilySplitTemplates(ctx)
		if err != nil {
			return err
		}
		inbounds, err := s.store.ListInbounds(ctx)
		if err != nil {
			return err
		}
		paths, err := s.store.ListProxyPaths(ctx)
		if err != nil {
			return err
		}
		steps, err := s.store.ListProxyPathSteps(ctx)
		if err != nil {
			return err
		}
		outbounds, err := s.store.ListOutbounds(ctx)
		if err != nil {
			return err
		}
		externals, err := s.store.ListExternalOutbounds(ctx)
		if err != nil {
			return err
		}
		warps, err := s.store.ListWARPProfiles(ctx)
		if err != nil {
			return err
		}
		out["routing_rules"] = rules
		out["routing_rule_sets"] = ruleSets
		out["family_split_templates"] = templates
		out["inbounds"] = inbounds
		out["proxy_paths"] = paths
		out["proxy_path_steps"] = publicProxyPathSteps(steps)
		out["outbounds"] = outbounds
		out["external_outbounds"] = externals
		out["warp_profiles"] = publicWARPProfiles(warps)
		return nil
	}
	addExternal := func() error {
		if err := addServers(); err != nil {
			return err
		}
		items, err := s.store.ListExternalOutbounds(ctx)
		if err != nil {
			return err
		}
		out["external_outbounds"] = items
		if roleAllows(role, model.RoleAdmin) {
			users, err := s.store.ListUsers(ctx)
			if err != nil {
				return err
			}
			out["users"] = users
		}
		return nil
	}
	addWarp := func() error {
		if err := addServers(); err != nil {
			return err
		}
		items, err := s.store.ListWARPProfiles(ctx)
		if err != nil {
			return err
		}
		out["warp_profiles"] = publicWARPProfiles(items)
		return nil
	}
	addDNS := func() error {
		if err := addServers(); err != nil {
			return err
		}
		lists, err := s.store.ListDNSLists(ctx, false)
		if err != nil {
			return err
		}
		policies, err := s.store.ListServerDNSPolicies(ctx)
		if err != nil {
			return err
		}
		benchmarks, err := s.store.ListDNSBenchmarkResults(ctx, 0, intQuery(r, "limit", 50))
		if err != nil {
			return err
		}
		out["dns_lists"] = lists
		out["server_dns_policies"] = policies
		out["dns_benchmarks"] = benchmarks
		return nil
	}
	addMTU := func() error {
		if err := addServers(); err != nil {
			return err
		}
		items, err := s.store.ListMTUDetectionResults(ctx, 0, intQuery(r, "limit", 50))
		if err != nil {
			return err
		}
		out["mtu_detections"] = items
		return nil
	}
	addForwards := func() error {
		if err := addServers(); err != nil {
			return err
		}
		items, err := s.store.ListPortForwards(ctx)
		if err != nil {
			return err
		}
		probes, err := s.store.ListPortForwardProbeResults(ctx, 0, 0, intQuery(r, "limit", 50))
		if err != nil {
			return err
		}
		out["port_forwards"] = items
		out["port_forward_probes"] = probes
		return nil
	}
	addTunnels := func() error {
		if err := addServers(); err != nil {
			return err
		}
		items, err := s.store.ListTunnels(ctx)
		if err != nil {
			return err
		}
		out["tunnels"] = publicTunnels(items)
		return nil
	}

	var err error
	switch page {
	case "dashboard":
		if !roleAllows(role, model.RoleOperator) {
			user := currentUser(r)
			if user == nil {
				err = errors.New("invalid session")
				break
			}
			err = timing.run("user_overview", func() error {
				overview, overviewErr := s.userDashboardOverview(ctx, *user)
				if overviewErr == nil {
					out["user_overview"] = overview
				}
				return overviewErr
			})
			if err == nil {
				err = timing.run("user_announcements", func() error {
					announcements, announcementsErr := s.store.ListNotificationAnnouncementsForUser(ctx, user.ID, 20)
					if announcementsErr == nil {
						out["user_announcements"] = userDashboardAnnouncements(announcements)
					}
					return announcementsErr
				})
			}
			break
		}
		if err = require(model.RoleOperator); err == nil {
			err = timing.run("summary", func() error {
				var summary any
				summary, err = s.store.Dashboard(ctx)
				out["summary"] = summary
				return err
			})
		}
		if err == nil {
			err = timing.run("servers", addServerSnapshot)
		}
		if err == nil {
			err = timing.run("inbounds", func() error {
				var inbounds []model.Inbound
				inbounds, err = s.store.ListInbounds(ctx)
				out["inbounds"] = inbounds
				return err
			})
		}
		if err == nil {
			err = timing.run("task_timeline", func() error {
				var tasks []model.AgentTask
				tasks, err = s.store.ListDashboardTaskTimeline(ctx, 6)
				out["agent_tasks"] = sanitizeTasksForRole(tasks, role)
				return err
			})
		}
		if err == nil {
			err = timing.run("audit_badge", func() error {
				out["connection_audit"], err = s.dashboardConnectionAudit(ctx)
				return err
			})
		}
		if err == nil {
			err = timing.run("settings", addSettings)
		}
	case "return-latency":
		if err = require(model.RoleOperator); err == nil {
			err = addServers()
		}
	case "servers":
		if err = require(model.RoleOperator); err == nil {
			err = addServers()
		}
		if err == nil {
			err = addServerCreationDefaults()
		}
		if err == nil {
			var lists []model.DNSList
			lists, err = s.store.ListDNSLists(ctx, false)
			out["dns_lists"] = lists
		}
		if err == nil {
			var policies []model.ServerDNSPolicy
			policies, err = s.store.ListServerDNSPolicies(ctx)
			out["server_dns_policies"] = policies
		}
		if err == nil {
			var benchmarks []model.DNSBenchmarkResult
			benchmarks, err = s.store.ListDNSBenchmarkResults(ctx, 0, 50)
			out["dns_benchmarks"] = benchmarks
		}
		if err == nil {
			var samples []model.ServerMetricSample
			samples, err = s.store.ListServerMetricSamples(ctx, 0, 60)
			out["server_metrics"] = samples
		}
		if err == nil && roleAllows(role, model.RoleAdmin) {
			err = addSettings()
		}
	case "proxy-paths":
		if err = require(model.RoleOperator); err == nil {
			err = addProxy()
		}
		if err == nil {
			err = addServerCreationDefaults()
		}
	case "inbounds":
		if err = require(model.RoleOperator); err == nil {
			err = addInbounds()
		}
	case "outbounds":
		if err = require(model.RoleOperator); err == nil {
			err = addOutbounds()
		}
	case "routing":
		if err = require(model.RoleOperator); err == nil {
			err = addRouting()
		}
	case "external-outbounds":
		if err = require(model.RoleOperator); err == nil {
			err = addExternal()
		}
	case "warp":
		if err = require(model.RoleOperator); err == nil {
			err = addWarp()
		}
	case "dns":
		if err = require(model.RoleAdmin); err == nil {
			err = addDNS()
		}
	case "dns-records":
		if err = require(model.RoleAdmin); err == nil {
			err = addServers()
		}
		if err == nil {
			out["inbounds"], err = s.store.ListInbounds(ctx)
		}
		if err == nil {
			out["dns_credentials"], err = s.store.ListDNSCredentials(ctx)
		}
	case "mtu":
		if err = require(model.RoleOperator); err == nil {
			err = addMTU()
		}
	case "port-forwards":
		if err = require(model.RoleOperator); err == nil {
			err = addForwards()
		}
	case "tunnels":
		if err = require(model.RoleOperator); err == nil {
			err = addTunnels()
		}
	case "users":
		if err = require(model.RoleAdmin); err == nil {
			err = addUsers()
		}
		if err == nil {
			err = addGroups()
		}
		if err == nil {
			var plans []model.SubscriptionPlan
			plans, err = s.store.ListSubscriptionPlans(ctx)
			if err == nil {
				out["subscription_plans"] = plans
			}
		}
		if err == nil {
			var bindings []model.UserPlanBinding
			bindings, err = s.store.ListActiveUserPlanBindings(ctx)
			if err == nil {
				out["user_plan_bindings"] = bindings
			}
		}
	case "nodes":
		if err = addSubscriptionPublicBaseURL(); err != nil {
			break
		}
		if !roleAllows(role, model.RoleAdmin) {
			if user := currentUser(r); user != nil {
				out["account_user"] = selfUserResponse(ctx, s.store, *user, role)
			} else {
				err = errors.New("invalid session")
			}
			break
		}
		if err = addServers(); err == nil {
			var plans []model.SubscriptionPlan
			plans, err = s.store.ListSubscriptionPlans(ctx)
			if err == nil {
				out["subscription_plans"] = plans
			}
		}
		if err == nil {
			err = addUsers()
		}
		if err == nil {
			err = addSettings()
		}
		if err == nil {
			err = addGroups()
		}
	case "plans":
		if err = require(model.RoleAdmin); err == nil {
			var plans []model.SubscriptionPlan
			plans, err = s.store.ListSubscriptionPlans(ctx)
			if err == nil {
				out["subscription_plans"] = plans
			}
		}
	case "node-order-templates":
		if err = require(model.RoleAdmin); err == nil {
			err = addServers()
		}
		if err == nil {
			out["inbounds"], err = s.store.ListInbounds(ctx)
		}
		if err == nil {
			var plans []model.SubscriptionPlan
			plans, err = s.store.ListSubscriptionPlans(ctx)
			if err == nil {
				out["subscription_plans"] = plans
			}
		}
	case "subscriptions":
		if err = require(model.RoleViewer); err != nil {
			break
		}
		if err = addSubscriptionPublicBaseURL(); err != nil {
			break
		}
		if !roleAllows(role, model.RoleAdmin) {
			if user := currentUser(r); user != nil {
				out["account_user"] = selfUserResponse(ctx, s.store, *user, role)
			} else {
				err = errors.New("invalid session")
			}
			break
		}
		err = addUsers()
		if err == nil {
			err = addSettings()
		}
		if err == nil {
			err = addGroups()
		}
		if err == nil {
			// Entry servers + proxy paths power the plan node assignment UI.
			err = addProxy()
		}
		if err == nil {
			var plans []model.SubscriptionPlan
			plans, err = s.store.ListSubscriptionPlans(ctx)
			if err == nil {
				out["subscription_plans"] = plans
			}
		}
		if err == nil {
			var planNodes []model.SubscriptionPlanNode
			planNodes, err = s.store.ListAllPlanNodes(ctx)
			if err == nil {
				out["subscription_plan_nodes"] = planNodes
			}
		}
		if err == nil {
			var bindings []model.UserPlanBinding
			bindings, err = s.store.ListActiveUserPlanBindings(ctx)
			if err == nil {
				out["user_plan_bindings"] = bindings
			}
		}
		if err == nil {
			var exceptions []model.UserNodeException
			exceptions, err = s.store.ListUserNodeExceptions(ctx)
			if err == nil {
				out["user_node_exceptions"] = exceptions
			}
		}
	case "notifications":
		if err = require(model.RoleViewer); err != nil {
			break
		}
		if user := currentUser(r); user != nil {
			var channels []model.NotificationChannel
			channels, err = s.store.ListNotificationChannelsByOwner(ctx, user.ID)
			if err == nil {
				out["notification_channels"] = publicNotificationChannels(channels)
				out["notification_config"] = notificationPageConfig(role)
				out["telegram_bot"] = s.telegramBotPublicStatus(ctx)
			}
			if err == nil {
				var bindings []model.TelegramBinding
				bindings, err = s.store.ListTelegramBindingsForUser(ctx, user.ID)
				if err == nil {
					out["telegram_bindings"] = bindings
				}
			}
		} else {
			err = errors.New("invalid session")
		}
		if err == nil && roleAllows(role, model.RoleAdmin) {
			err = addUsers()
		}
		if err == nil && roleAllows(role, model.RoleAdmin) {
			var announcements []model.NotificationAnnouncement
			announcements, err = s.store.ListNotificationAnnouncements(ctx, 20)
			if err == nil {
				out["notification_announcements"] = announcements
			}
		}
	case "tasks":
		if err = require(model.RoleOperator); err == nil {
			err = addServers()
		}
		if err == nil {
			var tasks []model.AgentTask
			tasks, err = s.store.ListTasks(ctx, intQuery(r, "limit", 300))
			if err == nil {
				out["agent_tasks"] = sanitizeTasksForRole(tasks, role)
			}
		}
	case "audit":
		if err = require(model.RoleOperator); err == nil {
			var logs []model.AuditLog
			logs, err = s.store.ListAuditPage(ctx, intQuery(r, "limit", 100), intQuery(r, "offset", 0), r.URL.Query().Get("action"))
			if err == nil {
				out["audit_logs"] = logs
			}
		}
		if err == nil {
			err = addServers()
		}
		if err == nil && roleAllows(role, model.RoleAdmin) {
			err = addUsers()
		}

	case "account":
		if user := currentUser(r); user != nil {
			out["account_user"] = selfUserResponse(ctx, s.store, *user, role)
			var passkeys []model.PasskeyCredential
			passkeys, err = s.store.ListPasskeyCredentials(ctx, user.ID)
			if err == nil {
				out["passkeys"] = passkeys
			}
			if err == nil {
				config, configErr := s.store.FullRoutingConfigData(ctx)
				if configErr != nil {
					err = configErr
				} else {
					inboundBindings, pathBindings, _, bindingsErr := s.runtimeAccessBindings(ctx, config)
					if bindingsErr != nil {
						err = bindingsErr
						break
					}
					deployments, deploymentErr := s.store.ListSSHPasswordDeploymentsForUser(ctx, user.ID)
					if deploymentErr != nil {
						err = deploymentErr
						break
					}
					pathNames := make(map[int64]string, len(config.ProxyPaths))
					for _, path := range core.ResolveProxyPathNames(config.ProxyPaths, config.ProxyPathSteps, config.Servers, config.Inbounds, config.ExternalOutbounds) {
						pathNames[path.ID] = path.Name
					}
					accesses := make([]map[string]any, 0)
					for _, server := range config.Servers {
						plan, planErr := buildSSHInboundPlan(0, server, config, inboundBindings, pathBindings, nil)
						if planErr != nil {
							err = planErr
							break
						}
						if len(plan.Inbounds) == 0 {
							continue
						}
						_, deployedPlan, ready, readyErr := s.matchingDeployedSSHPlan(ctx, server.ID, plan)
						if readyErr != nil {
							err = readyErr
							break
						}
						if !ready {
							continue
						}
						expectedDeployments, deploymentErr := s.sshPasswordDeploymentsFromPlan(server.ID, plan)
						if deploymentErr != nil {
							err = deploymentErr
							break
						}
						for _, inbound := range plan.Inbounds {
							for _, access := range inbound.Users {
								if !access.Enabled || access.UserID != user.ID {
									continue
								}
								identity := sshPasswordDeploymentIdentityForPlanUser(access)
								expected, expectedOK := sshPasswordDeploymentForIdentity(expectedDeployments, server.ID, identity)
								persisted, persistedOK := sshPasswordDeploymentForIdentity(deployments, server.ID, identity)
								if expectedOK && persistedOK && matchingSSHPasswordDeployment(persisted, expected) && matchingSSHIdentityRoutePlan(plan, deployedPlan, identity) {
									accessName := pathNames[access.PathID]
									if strings.TrimSpace(accessName) == "" && access.PathID == core.SSHDirectBranchPathID(inbound.InboundID) {
										accessName = inbound.Name
									}
									accesses = append(accesses, map[string]any{"inbound_id": inbound.InboundID, "path_id": access.PathID, "name": accessName, "address": inbound.Address, "port": inbound.Port, "username": access.Username, "device_id_hash": access.DeviceIDHash, "credential_epoch": access.CredentialEpoch, "credential_status": access.CredentialStatus})
								}
							}
						}
					}
					if err == nil {
						out["ssh_accesses"] = accesses
					}
				}
			}
		}
	case "settings":
		if err = require(model.RoleAdmin); err == nil {
			err = addSettings()
		}
		if err == nil {
			var inbounds []model.Inbound
			inbounds, err = s.store.ListInbounds(ctx)
			out["inbounds"] = inbounds
		}
		if err == nil {
			out["snell_profiles"], err = s.store.ListSnellProfiles(ctx)
		}
		if err == nil {
			out["node_presets"], err = s.store.ListNodePresets(ctx)
		}
		if err == nil {
			out["dns_credentials"], err = s.store.ListDNSCredentials(ctx)
		}
		if err == nil {
			out["certificates"], err = s.store.ListCertificates(ctx)
		}
		if err == nil {
			out["google_eab_credentials"], err = s.store.ListGoogleEABCredentials(ctx)
		}
		if err == nil {
			out["user_groups"], err = s.store.ListUserGroups(ctx)
		}
		if err == nil {
			err = addServers()
		}
		if err == nil {
			out["subscription_relays"], err = s.publicSubscriptionRelays(ctx)
		}
	default:
		fail(w, fmt.Errorf("unknown page %q", page), http.StatusBadRequest)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if err.Error() == "forbidden" {
			status = http.StatusForbidden
		}
		fail(w, err, status)
		return
	}
	if roleAllows(role, model.RoleOperator) {
		latestDeployment, err := s.store.LatestDeploymentTasks(ctx)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		deploymentStatus, err := s.deploymentStatus(ctx, latestDeployment)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		out["deployment_status"] = deploymentStatus
		configurationStates, syncErr := s.store.ListAllConfigurationSyncStates(ctx)
		if syncErr != nil {
			fail(w, syncErr, http.StatusInternalServerError)
			return
		}
		if serverSnapshotLoaded {
			out["configuration_sync"] = configurationSyncViews(configurationStates, serverSnapshot)
		} else {
			out["configuration_sync"] = s.configurationSyncViews(ctx, configurationStates)
		}
	}
	w.Header().Set("Server-Timing", timing.serverTiming())
	timing.logSlowIfNeeded()
	write(w, 200, out)
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "bootstrap:"+clientIP(r), 5, time.Minute) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Username == "" || len(req.Password) < 8 {
		fail(w, errors.New("username and password >= 8 chars required"), 400)
		return
	}
	pass, err := security.HashPassword(req.Password)
	if err != nil {
		fail(w, err, 500)
		return
	}
	uid, err := security.RandomUUID()
	if err != nil {
		fail(w, err, 500)
		return
	}
	upass, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, 500)
		return
	}
	sub, err := security.RandomToken(24)
	if err != nil {
		fail(w, err, 500)
		return
	}
	u := &model.User{Username: req.Username, Nickname: req.Username, PasswordHash: pass, Role: model.RoleAdmin, Status: "active", ProxyUUID: uid, ProxyPassword: upass, SubscriptionToken: sub}
	created, err := s.store.BootstrapAdmin(r.Context(), u)
	if err != nil {
		fail(w, err, 500)
		return
	}
	if !created {
		fail(w, errors.New("admin already bootstrapped"), 409)
		return
	}
	_ = s.store.AddAudit(r.Context(), model.AuditLog{ActorID: &u.ID, Action: "bootstrap", Target: "user", Detail: u.Username, IP: clientIP(r)})
	write(w, 201, map[string]any{"user": u})
}

// loginDummyPasswordHash is a valid argon2id encoding used when the login
// username is missing or inactive so VerifyPassword still runs for roughly the
// same duration as a real check.
const loginDummyPasswordHash = "argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // #nosec G101 -- deliberately invalid dummy verifier input, never an account credential.

const (
	sessionCookieName = "oboard_session"
	sessionLifetime   = 24 * time.Hour
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "login-ip:"+clientIP(r), 20, time.Minute) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !s.allowRate(w, r, "login-user:"+strings.ToLower(strings.TrimSpace(req.Username)), 8, time.Minute) {
		return
	}
	u, err := s.store.GetUserByUsername(r.Context(), req.Username)
	passwordHash := loginDummyPasswordHash
	active := err == nil && u != nil && u.Status == "active"
	if active {
		passwordHash = u.PasswordHash
	}
	// Always run the password verifier so missing/inactive usernames do not
	// short-circuit before the argon2 work of a real credential check.
	if !active || !security.VerifyPassword(req.Password, passwordHash) {
		fail(w, errors.New("用户名或密码错误"), 401)
		return
	}
	if s.beginSecondFactorLogin(w, r, u) {
		return
	}
	s.finishUserLogin(w, r, u, "login")
}

func validateRegistrationUsername(username string) error {
	if len(username) < 3 || len(username) > 32 {
		return errors.New("用户名长度需为 3-32 个字符")
	}
	for _, ch := range username {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '_', ch == '-', ch == '.':
		default:
			return errors.New("用户名只能包含字母、数字、下划线、连字符和点")
		}
	}
	if strings.HasPrefix(username, "__oboard_") {
		return errors.New("该用户名不可用")
	}
	return nil
}

func (s *Server) registrationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	settings, err := s.store.ListSettings(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"registration_enabled": settingBool(settings, settingRegistrationEnabled, false)})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "register-ip:"+clientIP(r), 10, time.Minute) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Nickname string `json:"nickname"`
	}
	if !decode(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	if !s.allowRate(w, r, "register-user:"+strings.ToLower(username), 5, time.Hour) {
		return
	}
	settings, err := s.store.ListSettings(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if !settingBool(settings, settingRegistrationEnabled, false) {
		fail(w, errors.New("注册未开放，请联系管理员"), http.StatusForbidden)
		return
	}
	if err := validateRegistrationUsername(username); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		fail(w, errors.New("密码至少需要 8 个字符"), http.StatusBadRequest)
		return
	}
	nickname := strings.TrimSpace(req.Nickname)
	if len([]rune(nickname)) > 40 {
		fail(w, errors.New("昵称不能超过 40 个字符"), http.StatusBadRequest)
		return
	}
	exists, err := s.store.UsernameExists(r.Context(), username)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if exists {
		fail(w, errors.New("用户名已被占用"), http.StatusConflict)
		return
	}
	pass, err := security.HashPassword(req.Password)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	proxyUUID, err := security.RandomUUID()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	proxyPassword, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	subscriptionToken, err := security.RandomToken(24)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	u := &model.User{Username: username, Nickname: nickname, PasswordHash: pass, Role: model.RoleNone, Status: "active", ProxyUUID: proxyUUID, ProxyPassword: proxyPassword, SubscriptionToken: subscriptionToken}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			fail(w, errors.New("用户名已被占用"), http.StatusConflict)
			return
		}
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if groupID := registrationDefaultGroupID(settings); groupID > 0 {
		if group, err := s.store.GetUserGroup(r.Context(), groupID); err == nil && group.SystemKey != store.UserGroupSystemAdmins {
			_ = s.store.CreateUserGroupMember(r.Context(), &model.UserGroupMember{GroupID: group.ID, UserID: u.ID, Enabled: true})
		}
	}
	_ = s.store.AddAudit(r.Context(), model.AuditLog{ActorID: &u.ID, Action: "register", Target: "user", Detail: u.Username, IP: clientIP(r)})
	write(w, http.StatusCreated, map[string]any{"user": map[string]any{"id": u.ID, "username": u.Username, "nickname": u.Nickname, "role": u.Role}})
}

func registrationDefaultGroupID(settings map[string]string) int64 {
	raw := strings.TrimSpace(settings[settingRegistrationDefaultGroupID])
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	token := currentSessionToken(r)
	claims, ok := r.Context().Value(claimsKey).(security.TokenClaims)
	if !ok || token == "" {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if err := s.store.RevokeUserSession(r.Context(), user.ID, security.HashSecret(token), claims.Expiry); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	s.clearSessionCookies(w, r)
	_ = s.store.AddAudit(r.Context(), model.AuditLog{ActorID: &user.ID, Action: "logout", Target: "user", Detail: user.Username, IP: clientIP(r)})
	write(w, http.StatusOK, map[string]any{"ok": true, "session_revoked": true})
}

func (s *Server) restoreSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	user := currentUser(r)
	sessionToken := currentSessionToken(r)
	if user == nil || sessionToken == "" {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	write(w, http.StatusOK, map[string]any{
		"csrf_token": s.csrfTokenForSession(sessionToken),
		"user":       sessionUserResponse(*user, currentRole(r)),
	})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "password:"+clientIP(r), 10, time.Minute) {
		return
	}
	user, ok := r.Context().Value(userKey).(*model.User)
	if !ok || user == nil {
		fail(w, errors.New("invalid session"), 401)
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !security.VerifyPassword(req.CurrentPassword, user.PasswordHash) {
		fail(w, errors.New("current password is incorrect"), 403)
		return
	}
	if len(req.NewPassword) < 8 {
		fail(w, errors.New("new password must be at least 8 characters"), 400)
		return
	}
	hash, err := security.HashPassword(req.NewPassword)
	if err != nil {
		fail(w, err, 500)
		return
	}
	updated := *user
	updated.PasswordHash = hash
	if err := s.store.UpdateUser(r.Context(), &updated); err != nil {
		fail(w, err, 500)
		return
	}
	if _, err := s.store.BumpSessionVersion(r.Context(), user.ID); err != nil {
		fail(w, err, 500)
		return
	}
	_ = s.store.AddAudit(r.Context(), model.AuditLog{ActorID: &user.ID, Action: "change_password", Target: "user", Detail: user.Username, IP: clientIP(r)})
	write(w, 200, map[string]any{"ok": true, "session_revoked": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), 401)
		return
	}
	switch r.Method {
	case http.MethodGet:
		write(w, 200, map[string]any{"user": selfUserResponse(r.Context(), s.store, *user, currentRole(r))})
	case http.MethodPatch:
		var req struct {
			Nickname string `json:"nickname"`
		}
		if !decode(w, r, &req) {
			return
		}
		nickname := strings.TrimSpace(req.Nickname)
		if len([]rune(nickname)) > 40 {
			fail(w, errors.New("昵称不能超过 40 个字符"), 400)
			return
		}
		updated := *user
		updated.Nickname = nickname
		if err := s.store.UpdateUser(r.Context(), &updated); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "profile", fmt.Sprint(updated.ID))
		write(w, 200, map[string]any{"user": selfUserResponse(r.Context(), s.store, updated, currentRole(r))})
	default:
		method(w)
	}
}

func (s *Server) selfSubscriptionAge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), 401)
		return
	}
	var req struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
	}
	if !decode(w, r, &req) {
		return
	}
	publicKey := strings.TrimSpace(req.PublicKey)
	if publicKey != "" {
		_, canonical, err := parseSubscriptionAgeRecipient(publicKey)
		if err != nil {
			fail(w, err, 400)
			return
		}
		publicKey = canonical
	}
	settings, err := s.store.ListSettings(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	enabled := req.Enabled || normalizeSubscriptionAgePolicy(settings[settingSubscriptionAgePolicy]) == "required"
	if enabled && publicKey == "" {
		fail(w, errSubscriptionAgeKeyRequired, 400)
		return
	}
	if err := s.store.SetUserSubscriptionAge(r.Context(), user.ID, enabled, publicKey); err != nil {
		fail(w, err, 500)
		return
	}
	updated, err := s.store.GetUser(r.Context(), user.ID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "update", "subscription-age", fmt.Sprint(user.ID))
	write(w, 200, map[string]any{"user": selfUserResponse(r.Context(), s.store, *updated, currentRole(r))})
}

func selfUserResponse(ctx context.Context, st *store.Store, user model.User, role model.Role) map[string]any {
	protected, _ := st.IsBootstrapAdmin(ctx, user.ID)
	settings, _ := st.ListSettings(ctx)
	authentication, _ := st.GetUserAuthentication(ctx, user.ID)
	passkeys, _ := st.ListPasskeyCredentials(ctx, user.ID)
	customPath := user
	groups, groupErr := st.ListUserGroups(ctx)
	members, memberErr := st.ListUserGroupMembers(ctx)
	userPolicies, groupPolicies, policyErr := st.SubscriptionCustomPathPolicies(ctx)
	paths, pathErr := st.ListSubscriptionCustomPaths(ctx)
	if groupErr == nil && memberErr == nil && policyErr == nil && pathErr == nil {
		for index := range groups {
			groups[index].SubscriptionCustomPathPolicy = model.SubscriptionCustomPathInherit
			if policy, ok := groupPolicies[groups[index].ID]; ok {
				groups[index].SubscriptionCustomPathPolicy = policy
			}
		}
		customPath.SubscriptionCustomPathPolicy = model.SubscriptionCustomPathInherit
		if policy, ok := userPolicies[user.ID]; ok {
			customPath.SubscriptionCustomPathPolicy = policy
		}
		for _, item := range paths {
			if item.UserID == user.ID {
				customPath.SubscriptionCustomPath = item.Alias
				break
			}
		}
		mode := core.NormalizeSubscriptionCustomPathMode(settings[settingSubscriptionCustomPathMode])
		items := []model.User{customPath}
		core.ApplySubscriptionCustomPathPolicies(mode, items, groups, members)
		customPath = items[0]
	}
	return map[string]any{
		"id":                               user.ID,
		"username":                         user.Username,
		"nickname":                         user.Nickname,
		"role":                             role,
		"status":                           user.Status,
		"protected":                        protected,
		"subscription_token":               user.SubscriptionToken,
		"subscription_age_enabled":         user.SubscriptionAgeEnabled,
		"subscription_age_public_key":      user.SubscriptionAgePublicKey,
		"subscription_age_policy":          normalizeSubscriptionAgePolicy(settings[settingSubscriptionAgePolicy]),
		"subscription_suspended":           user.SubscriptionSuspended,
		"subscription_suspended_at":        user.SubscriptionSuspendedAt,
		"subscription_suspend_reason":      user.SubscriptionSuspendReason,
		"subscription_custom_path":         customPath.SubscriptionCustomPath,
		"subscription_custom_path_policy":  customPath.SubscriptionCustomPathPolicy,
		"subscription_custom_path_enabled": customPath.SubscriptionCustomPathEnabled,
		"subscription_custom_path_source":  customPath.SubscriptionCustomPathSource,
		"totp_enabled":                     authentication.TOTPEnabled,
		"passkey_count":                    len(passkeys),
	}
}

func sessionUserResponse(user model.User, role model.Role) map[string]any {
	return map[string]any{
		"id":       user.ID,
		"username": user.Username,
		"nickname": user.Nickname,
		"role":     role,
		"status":   user.Status,
	}
}

type ctxKey string

const claimsKey ctxKey = "claims"
const userKey ctxKey = "user"
const sessionTokenKey ctxKey = "session-token"

func (s *Server) auth(next http.HandlerFunc, min model.Role) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		token := ""
		cookieSession := false
		switch {
		case authorization == "":
			cookie, err := r.Cookie(sessionCookieName)
			if err == nil {
				token = cookie.Value
				cookieSession = token != ""
			}
		case strings.HasPrefix(authorization, "Bearer "):
			token = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		default:
			token = authorization
		}
		claims, err := security.VerifySession(s.sessionSecret, token)
		if err != nil {
			if cookieSession {
				s.clearSessionCookies(w, r)
			}
			fail(w, err, 401)
			return
		}
		revoked, err := s.store.UserSessionRevoked(r.Context(), security.HashSecret(token))
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		if revoked {
			if cookieSession {
				s.clearSessionCookies(w, r)
			}
			fail(w, errors.New("session revoked"), http.StatusUnauthorized)
			return
		}
		if !hmac.Equal([]byte(claims.ClientBinding), []byte(s.sessionBindingForRequest(r))) {
			if cookieSession {
				s.clearSessionCookies(w, r)
			}
			fail(w, errors.New("invalid session"), http.StatusUnauthorized)
			return
		}
		u, err := s.store.GetUser(r.Context(), claims.Subject)
		if err != nil || u.Status != "active" {
			if cookieSession {
				s.clearSessionCookies(w, r)
			}
			fail(w, errors.New("invalid session"), 401)
			return
		}
		if claims.SessionVersion != u.SessionVersion {
			if cookieSession {
				s.clearSessionCookies(w, r)
			}
			fail(w, errors.New("session revoked"), 401)
			return
		}
		if cookieSession && csrfRequired(r.Method) && !s.validCSRFToken(r, token) {
			fail(w, errors.New("invalid CSRF token"), http.StatusForbidden)
			return
		}
		effectiveRole, err := s.store.EffectiveUserRole(r.Context(), *u)
		if err != nil {
			fail(w, err, 500)
			return
		}
		claims.Role = string(effectiveRole)
		if !roleAllows(effectiveRole, min) {
			fail(w, errors.New("forbidden"), 403)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		ctx = context.WithValue(ctx, userKey, u)
		ctx = context.WithValue(ctx, sessionTokenKey, token)
		if strings.HasSuffix(r.URL.Path, "/events") {
			s.noteControllerPanelActivity()
			next(w, r.WithContext(ctx))
			return
		}
		s.beginControllerPanelRequest()
		defer s.endControllerPanelRequest()
		next(w, r.WithContext(ctx))
	}
}

func csrfRequired(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (s *Server) validCSRFToken(r *http.Request, sessionToken string) bool {
	token := strings.TrimSpace(r.Header.Get("X-OBoard-CSRF"))
	if token == "" && strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err == nil {
			token = strings.TrimSpace(r.Form.Get("_oboard_csrf"))
		}
	}
	return token != "" && hmac.Equal([]byte(token), []byte(s.csrfTokenForSession(sessionToken)))
}

func (s *Server) csrfTokenForSession(sessionToken string) string {
	mac := hmac.New(sha256.New, []byte(s.sessionSecret))
	_, _ = io.WriteString(mac, "oboard-csrf\x00")
	_, _ = io.WriteString(mac, sessionToken)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) sessionBindingForRequest(r *http.Request) string {
	return sessionClientBinding(s.sessionSecret, r.UserAgent())
}

func sessionClientBinding(secret, userAgent string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, "oboard-session-client-v1\x00")
	_, _ = io.WriteString(mac, userAgent)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, sessionToken string, expiresAt time.Time) {
	secure := requestUsesHTTPS(r)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: sessionToken, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expiresAt.UTC(), MaxAge: int(sessionLifetime / time.Second)}) // #nosec G124 -- Secure follows direct TLS or an explicitly trusted proxy; localhost HTTP remains supported for development.
}

func (s *Server) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := requestUsesHTTPS(r)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: time.Unix(1, 0).UTC(), MaxAge: -1}) // #nosec G124 -- deletion must match the dynamically secured development or production cookie.
}

func requestUsesHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if trustedProxyRequest(r) {
		return strings.EqualFold(lastHeaderValue(r.Header.Get("X-Forwarded-Proto")), "https")
	}
	return false
}

func trustedProxyRequest(r *http.Request) bool {
	peer, ok := requestPeerIP(r)
	return ok && trustedProxyIP(r, peer)
}

func (s *Server) allowRate(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	keyHash := security.HashSecret(key)
	allowed, err := s.store.AllowRate(r.Context(), keyHash, limit, window, 10_000)
	if err != nil {
		fail(w, err, http.StatusServiceUnavailable)
		return false
	}
	if allowed {
		return true
	}
	fail(w, errors.New("rate limit exceeded"), http.StatusTooManyRequests)
	return false
}

func clientIP(r *http.Request) string {
	peer, peerValid := requestPeerIP(r)
	if peerValid && trustedProxyIP(r, peer) {
		current := peer
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for index := len(forwarded) - 1; index >= 0 && trustedProxyIP(r, current); index-- {
			candidate, err := netip.ParseAddr(strings.TrimSpace(forwarded[index]))
			if err != nil {
				break
			}
			current = candidate.Unmap()
		}
		if current != peer {
			return current.String()
		}
		if forwarded := normalizedIP(r.Header.Get("X-Real-IP")); forwarded != "" {
			return forwarded
		}
	}
	if peerValid {
		return peer.String()
	}
	return r.RemoteAddr
}

func trustedProxyIP(r *http.Request, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	for _, prefix := range trustedProxyStateForRequest(r).prefixes {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func (s *Server) reverseProxyStatus(r *http.Request) map[string]any {
	peer, peerValid := requestPeerIP(r)
	peerIP := ""
	peerTrusted := false
	suggestedCIDR := ""
	if peerValid {
		peer = peer.Unmap()
		peerIP = peer.String()
		peerTrusted = trustedProxyIP(r, peer)
		if !peer.IsLoopback() {
			suggestedCIDR = netip.PrefixFrom(peer, peer.BitLen()).String()
		}
	}
	forwardedProto := strings.ToLower(lastHeaderValue(r.Header.Get("X-Forwarded-Proto")))
	if forwardedProto != "http" && forwardedProto != "https" {
		forwardedProto = ""
	}
	return map[string]any{
		"peer_ip":                   peerIP,
		"peer_trusted":              peerTrusted,
		"client_ip":                 clientIP(r),
		"https":                     requestUsesHTTPS(r),
		"direct_tls":                r.TLS != nil,
		"forwarded_for_present":     strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != "",
		"forwarded_real_ip_present": strings.TrimSpace(r.Header.Get("X-Real-IP")) != "",
		"forwarded_proto":           forwardedProto,
		"suggested_cidr":            suggestedCIDR,
	}
}

func (s *Server) refreshSecureSessionCookie(w http.ResponseWriter, r *http.Request) {
	if !requestUsesHTTPS(r) {
		return
	}
	token := currentSessionToken(r)
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || token == "" || !hmac.Equal([]byte(cookie.Value), []byte(token)) {
		return
	}
	claims, ok := r.Context().Value(claimsKey).(security.TokenClaims)
	if !ok || !claims.Expiry.After(time.Now()) {
		return
	}
	s.setSessionCookie(w, r, token, claims.Expiry)
}

func requestPeerIP(r *http.Request) (netip.Addr, bool) {
	value := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func normalizedIP(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return ""
	}
	return addr.Unmap().String()
}

func lastHeaderValue(value string) string {
	parts := strings.Split(value, ",")
	return strings.TrimSpace(parts[len(parts)-1])
}

func currentRole(r *http.Request) model.Role {
	if claims, ok := r.Context().Value(claimsKey).(security.TokenClaims); ok {
		return model.Role(claims.Role)
	}
	if u, ok := r.Context().Value(userKey).(*model.User); ok && u != nil {
		return u.Role
	}
	return ""
}

func currentUser(r *http.Request) *model.User {
	if u, ok := r.Context().Value(userKey).(*model.User); ok {
		return u
	}
	return nil
}

func currentSessionToken(r *http.Request) string {
	token, _ := r.Context().Value(sessionTokenKey).(string)
	return token
}

var errAdministratorAccountManagedByOperator = errors.New("操作员不能创建、编辑或删除管理员账号")

func (s *Server) requireUserMutationAccess(ctx context.Context, actorRole model.Role, userID int64) error {
	if model.CanManageAdministratorAccounts(actorRole) || userID <= 0 {
		return nil
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	effectiveRole, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		return err
	}
	if effectiveRole == model.RoleAdmin {
		return errAdministratorAccountManagedByOperator
	}
	return nil
}

func (s *Server) requireUserMutationsAccess(ctx context.Context, actorRole model.Role, userIDs []int64) error {
	for _, userID := range uniquePositiveIDs(userIDs) {
		if err := s.requireUserMutationAccess(ctx, actorRole, userID); err != nil {
			return err
		}
	}
	return nil
}

func requireAssignedRoleAccess(actorRole, assignedRole model.Role) error {
	if assignedRole == model.RoleAdmin && !model.CanManageAdministratorAccounts(actorRole) {
		return errAdministratorAccountManagedByOperator
	}
	return nil
}

func (s *Server) requireUserGroupMutationAccess(ctx context.Context, actorRole model.Role, groupID int64) error {
	if model.CanManageAdministratorAccounts(actorRole) || groupID <= 0 {
		return nil
	}
	group, err := s.store.GetUserGroup(ctx, groupID)
	if err != nil {
		return err
	}
	if group.Role == model.RoleAdmin || group.SystemKey == store.UserGroupSystemAdmins {
		return errAdministratorAccountManagedByOperator
	}
	return nil
}

func roleAllows(got, min model.Role) bool {
	if min == model.RoleAdmin {
		return model.HasManagementAccess(got)
	}
	rank := map[model.Role]int{model.RoleNone: 0, model.RoleViewer: 1, model.RoleOperator: 2, model.RoleAdmin: 3}
	return rank[got] >= rank[min]
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	d, err := s.store.Dashboard(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, d)
}

func (s *Server) servers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListServers(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		out := map[string]any{"servers": items}
		if r.URL.Query().Get("include_metrics") == "1" {
			samples, err := s.store.ListServerMetricSamples(r.Context(), 0, 60)
			if err != nil {
				fail(w, err, 500)
				return
			}
			out["server_metrics"] = samples
		}
		write(w, 200, out)
	case http.MethodPost:
		var input struct {
			model.Server
			MTUMode                *model.MTUMode            `json:"mtu_mode"`
			BBREnabled             *bool                     `json:"bbr_enabled"`
			TimeCorrectionMode     *model.TimeCorrectionMode `json:"time_correction_mode"`
			ResourceHistoryEnabled *bool                     `json:"resource_history_enabled"`
			LatencyProbeEnabled    *bool                     `json:"latency_probe_enabled"`
			ConnectionAuditEnabled *bool                     `json:"connection_audit_enabled"`
			OfflineNotifyEnabled   *bool                     `json:"offline_notify_enabled"`
			OfflineAfterSeconds    *int                      `json:"offline_after_seconds"`
			ServiceStartAt         *time.Time                `json:"service_start_at"`
			ExpiresAt              *time.Time                `json:"expires_at"`
			AutoRenewEnabled       *bool                     `json:"auto_renew_enabled"`
			RenewalCycle           *model.ServerRenewalCycle `json:"renewal_cycle"`
			ExpiryNotifyEnabled    *bool                     `json:"expiry_notify_enabled"`
			TrafficResetMode       *string                   `json:"traffic_reset_mode"`
			TrafficResetDay        *int                      `json:"traffic_reset_day"`
			TrafficLimitBytes      *int64                    `json:"traffic_limit_bytes"`
			TrafficUsedBytes       *int64                    `json:"traffic_used_bytes"`
		}
		if !decode(w, r, &input) {
			return
		}
		v := input.Server
		settings, err := s.store.ListSettings(r.Context())
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		defaultMTU, defaultBBR, defaultTimeMode := serverCreationDefaults(settings)
		if input.MTUMode == nil {
			v.MTUMode = defaultMTU
		} else {
			v.MTUMode = *input.MTUMode
		}
		if input.BBREnabled == nil {
			v.BBREnabled = defaultBBR
		} else {
			v.BBREnabled = *input.BBREnabled
		}
		if input.TimeCorrectionMode == nil {
			v.TimeCorrectionMode = defaultTimeMode
		} else {
			v.TimeCorrectionMode = *input.TimeCorrectionMode
		}
		if input.ResourceHistoryEnabled == nil {
			v.ResourceHistoryEnabled = true
		} else {
			v.ResourceHistoryEnabled = *input.ResourceHistoryEnabled
		}
		v.ResourceHistoryConfigured = true
		if input.LatencyProbeEnabled == nil {
			v.LatencyProbeEnabled = true
		} else {
			v.LatencyProbeEnabled = *input.LatencyProbeEnabled
		}
		if input.ConnectionAuditEnabled == nil {
			v.ConnectionAuditEnabled = settingBool(settings, settingConnectionAuditEnabled, true)
		} else {
			v.ConnectionAuditEnabled = *input.ConnectionAuditEnabled
		}
		if input.OfflineNotifyEnabled == nil {
			v.OfflineNotifyEnabled = true
		} else {
			v.OfflineNotifyEnabled = *input.OfflineNotifyEnabled
		}
		if input.OfflineAfterSeconds != nil {
			v.OfflineAfterSeconds = *input.OfflineAfterSeconds
		}
		if input.ServiceStartAt != nil {
			startAt := *input.ServiceStartAt
			v.ServiceStartAt = &startAt
		} else {
			v.ServiceStartAt = nil
		}
		if input.ExpiresAt != nil {
			expiresAt := *input.ExpiresAt
			v.ExpiresAt = &expiresAt
		} else {
			v.ExpiresAt = nil
		}
		if input.AutoRenewEnabled == nil {
			v.AutoRenewEnabled = false
		} else {
			v.AutoRenewEnabled = *input.AutoRenewEnabled
		}
		if input.ExpiryNotifyEnabled == nil {
			v.ExpiryNotifyEnabled = true
		} else {
			v.ExpiryNotifyEnabled = *input.ExpiryNotifyEnabled
		}
		if input.RenewalCycle == nil {
			v.RenewalCycle = model.ServerRenewalCycleMonthly
		} else {
			v.RenewalCycle = normalizeServerRenewalCycle(*input.RenewalCycle)
		}
		// Server traffic reset day is derived from billing dates when the caller
		// leaves traffic_reset_mode/day unspecified: service_start_at > expires_at.
		if input.TrafficResetMode == nil && input.TrafficResetDay == nil {
			if derivedMode, derivedDay, ok := deriveServerTrafficReset(input.TrafficResetMode, input.TrafficResetDay, v.ServiceStartAt, v.ExpiresAt, trafficLocation(settings)); ok {
				v.TrafficResetMode = derivedMode
				v.TrafficResetDay = derivedDay
			} else {
				v.TrafficResetMode = "monthly"
				v.TrafficResetDay = 1
			}
		} else {
			if input.TrafficResetMode == nil {
				v.TrafficResetMode = "monthly"
			} else {
				v.TrafficResetMode = normalizeControllerTrafficResetMode(*input.TrafficResetMode)
			}
			if input.TrafficResetDay == nil {
				v.TrafficResetDay = 1
			} else {
				v.TrafficResetDay = normalizeControllerTrafficResetDay(*input.TrafficResetDay)
			}
		}
		if input.TrafficLimitBytes != nil {
			if *input.TrafficLimitBytes < 0 {
				fail(w, errors.New("traffic_limit_bytes must be >= 0"), 400)
				return
			}
			v.TrafficLimitBytes = *input.TrafficLimitBytes
		}
		var trafficUsedBytes *int64
		if input.TrafficUsedBytes != nil {
			if *input.TrafficUsedBytes < 0 {
				fail(w, errors.New("traffic_used_bytes must be >= 0"), 400)
				return
			}
			trafficUsedBytes = input.TrafficUsedBytes
		}
		if err := validateServer(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.rejectDuplicateServerName(r.Context(), v.Name, 0); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if v.Status == "" {
			v.Status = model.ServerUnknown
		}
		if err := s.store.CreateServer(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if trafficUsedBytes != nil {
			loc := trafficLocation(settings)
			k, sTime, eTime := trafficWindow(time.Now(), v.TrafficResetMode, v.TrafficResetDay, time.Time{}, loc)
			window := model.ServerTrafficWindow{Key: k, Start: sTime, End: eTime}
			if err := s.store.SetServerTrafficUsed(r.Context(), v.ID, *trafficUsedBytes, window); err != nil {
				fail(w, err, 500)
				return
			}
		}
		auditReq(s, r, "create", "server", fmt.Sprint(v.ID))
		created, _ := s.store.GetServer(r.Context(), v.ID)
		write(w, 201, map[string]any{"server": created})
	default:
		method(w)
	}
}
func (s *Server) serverSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/servers/")
	if len(parts) < 1 {
		fail(w, errors.New("missing id"), 400)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		fail(w, err, 400)
		return
	}
	if len(parts) == 2 && parts[1] == "enroll-token" {
		s.enrollToken(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "remote-access" {
		s.serverRemoteAccess(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "terminal" {
		s.serverTerminal(w, r, id, parts[2:])
		return
	}
	if len(parts) == 2 && parts[1] == "tasks" {
		s.serverTasks(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "metrics" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		if _, err := s.store.GetServer(r.Context(), id); err != nil {
			fail(w, err, 404)
			return
		}
		items, err := s.store.ListServerMetricSamples(r.Context(), id, intQuery(r, "limit", 120))
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"server_metrics": items})
		return
	}
	if len(parts) == 2 && parts[1] == "resource-metrics" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		server, err := s.store.GetServer(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		hours := intQuery(r, "hours", 24)
		if hours != 1 && hours != 4 && hours != 24 && hours != 168 && hours != 720 {
			hours = 24
		}
		bucket := time.Minute
		if hours >= 720 {
			bucket = 4 * time.Hour
		} else if hours >= 168 {
			bucket = time.Hour
		} else if hours >= 24 {
			bucket = 10 * time.Minute
		} else if hours >= 4 {
			bucket = 2 * time.Minute
		}
		points := []model.ServerResourceMetricPoint{}
		settings, err := s.store.ListSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		retentionDays := store.ServerMonitoringRetentionDays(settings)
		if server.ResourceHistoryEnabled {
			now := time.Now().UTC()
			from := now.Add(-time.Duration(hours) * time.Hour)
			retainedFrom := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
			if from.Before(retainedFrom) {
				from = retainedFrom
			}
			points, err = s.store.ListServerResourceMetricPoints(r.Context(), id, from, bucket)
			if err != nil {
				fail(w, err, 500)
				return
			}
		}
		write(w, 200, map[string]any{
			"history_enabled": server.ResourceHistoryEnabled,
			"retention_days":  retentionDays,
			"window_hours":    hours,
			"bucket_seconds":  int(bucket.Seconds()),
			"points":          points,
			"current": map[string]any{
				"cpu_usage_percent":      server.CPUUsagePercent,
				"memory_used_bytes":      server.MemoryUsedBytes,
				"memory_total_bytes":     server.MemoryTotalBytes,
				"disk_used_bytes":        server.DiskBytes,
				"disk_total_bytes":       server.DiskTotalBytes,
				"tcp_connection_count":   server.TCPConnectionCount,
				"udp_connection_count":   server.UDPConnectionCount,
				"process_count":          server.ProcessCount,
				"network_upload_bps":     server.NetworkUploadBPS,
				"network_download_bps":   server.NetworkDownloadBPS,
				"traffic_upload_bytes":   server.TrafficUploadBytes,
				"traffic_download_bytes": server.TrafficDownloadBytes,
				"traffic_period_start":   server.TrafficPeriodStart,
				"traffic_period_end":     server.TrafficPeriodEnd,
				"traffic_reset_mode":     server.TrafficResetMode,
				"traffic_reset_day":      server.TrafficResetDay,
				"sampled_at":             server.TelemetryUpdatedAt,
			},
			"traffic": map[string]any{
				"reset_mode":     server.TrafficResetMode,
				"reset_day":      server.TrafficResetDay,
				"period_start":   server.TrafficPeriodStart,
				"period_end":     server.TrafficPeriodEnd,
				"upload_bytes":   server.TrafficUploadBytes,
				"download_bytes": server.TrafficDownloadBytes,
				"total_bytes":    server.TrafficUploadBytes + server.TrafficDownloadBytes,
			},
		})
		return
	}
	if len(parts) == 2 && parts[1] == "traffic" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		server, err := s.store.GetServer(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		settings, err := s.store.ListSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		loc := trafficLocation(settings)
		_, start, end := trafficWindow(time.Now(), server.TrafficResetMode, server.TrafficResetDay, time.Time{}, loc)
		write(w, 200, map[string]any{
			"server_id":              server.ID,
			"traffic_reset_mode":     server.TrafficResetMode,
			"traffic_reset_day":      server.TrafficResetDay,
			"period_key":             start.Format("2006-01-02"),
			"period_start":           start,
			"period_end":             end,
			"traffic_period_start":   server.TrafficPeriodStart,
			"traffic_period_end":     server.TrafficPeriodEnd,
			"traffic_upload_bytes":   server.TrafficUploadBytes,
			"traffic_download_bytes": server.TrafficDownloadBytes,
			"total_bytes":            server.TrafficUploadBytes + server.TrafficDownloadBytes,
			"expires_at":             server.ExpiresAt,
			"renewal_cycle":          server.RenewalCycle,
		})
		return
	}
	if len(parts) == 2 && parts[1] == "connectivity" {
		s.serverConnectivity(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "latency-probe" {
		s.serverLatencyProbe(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "mtu-detect" {
		s.serverMTUDetect(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "dns-test" {
		s.serverDNSTest(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "dns-policy" {
		s.serverDNSPolicy(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "agent-config" {
		s.serverAgentConfig(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "agent-update" {
		s.serverAgentUpdate(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "agent-uninstall" {
		s.serverAgentUninstall(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "diagnose" {
		s.serverDiagnose(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "network-interfaces" {
		s.serverNetworkInterfaces(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "logs" {
		s.serverLogs(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "logs" && parts[2] == "control" {
		s.serverLogsControl(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "extend-expiry" {
		s.extendServerExpiryHandler(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "reset-traffic" {
		s.resetServerTrafficHandler(w, r, id)
		return
	}
	if r.Method == http.MethodGet {
		srv, err := s.store.GetServer(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		write(w, 200, map[string]any{"server": srv})
		return
	}
	if r.Method == http.MethodPatch {
		var input struct {
			model.Server
			MTUMode                  *model.MTUMode              `json:"mtu_mode"`
			BBREnabled               *bool                       `json:"bbr_enabled"`
			TimeCorrectionMode       *model.TimeCorrectionMode   `json:"time_correction_mode"`
			ResourceHistoryEnabled   *bool                       `json:"resource_history_enabled"`
			LatencyProbeEnabled      *bool                       `json:"latency_probe_enabled"`
			LatencyProbeMode         *model.LatencyProbeMode     `json:"latency_probe_mode"`
			LatencyProbePublicTarget *model.ConnectivityTarget   `json:"latency_probe_public_target"`
			LatencyProbeInterval     *int                        `json:"latency_probe_interval_seconds"`
			LatencyProbeSamples      *int                        `json:"latency_probe_sample_count"`
			LatencyProbeRegions      *[]model.LatencyProbeRegion `json:"latency_probe_regions"`
			LatencyProbeMaxTargets   *int                        `json:"latency_probe_max_targets"`
			OfflineNotifyEnabled     *bool                       `json:"offline_notify_enabled"`
			OfflineAfterSeconds      *int                        `json:"offline_after_seconds"`
			ServiceStartAt           *time.Time                  `json:"service_start_at"`
			ClearServiceStartAt      *bool                       `json:"clear_service_start_at"`
			ExpiresAt                *time.Time                  `json:"expires_at"`
			ClearExpiresAt           *bool                       `json:"clear_expires_at"`
			AutoRenewEnabled         *bool                       `json:"auto_renew_enabled"`
			RenewalCycle             *model.ServerRenewalCycle   `json:"renewal_cycle"`
			ExpiryNotifyEnabled      *bool                       `json:"expiry_notify_enabled"`
			TrafficResetMode         *string                     `json:"traffic_reset_mode"`
			TrafficResetDay          *int                        `json:"traffic_reset_day"`
			TrafficLimitBytes        *int64                      `json:"traffic_limit_bytes"`
			TrafficUsedBytes         *int64                      `json:"traffic_used_bytes"`
		}
		var raw json.RawMessage
		if !decode(w, r, &raw) {
			return
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		current, err := s.store.GetServer(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v := *current
		if err := json.Unmarshal(raw, &v); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		v.ID = id
		if input.MTUMode == nil {
			v.MTUMode = current.MTUMode
		} else {
			v.MTUMode = *input.MTUMode
		}
		if input.BBREnabled == nil {
			v.BBREnabled = current.BBREnabled
		} else {
			v.BBREnabled = *input.BBREnabled
		}
		if input.TimeCorrectionMode == nil {
			v.TimeCorrectionMode = current.TimeCorrectionMode
		} else {
			v.TimeCorrectionMode = *input.TimeCorrectionMode
		}
		if input.ResourceHistoryEnabled == nil {
			v.ResourceHistoryEnabled = current.ResourceHistoryEnabled
		} else {
			v.ResourceHistoryEnabled = *input.ResourceHistoryEnabled
		}
		if input.OfflineNotifyEnabled == nil {
			v.OfflineNotifyEnabled = current.OfflineNotifyEnabled
		} else {
			v.OfflineNotifyEnabled = *input.OfflineNotifyEnabled
		}
		if input.OfflineAfterSeconds == nil {
			v.OfflineAfterSeconds = current.OfflineAfterSeconds
		} else {
			v.OfflineAfterSeconds = *input.OfflineAfterSeconds
		}
		if input.ClearServiceStartAt != nil && *input.ClearServiceStartAt {
			v.ServiceStartAt = nil
		} else if input.ServiceStartAt != nil {
			startAt := *input.ServiceStartAt
			v.ServiceStartAt = &startAt
		} else {
			v.ServiceStartAt = current.ServiceStartAt
		}
		if input.ClearExpiresAt != nil && *input.ClearExpiresAt {
			v.ExpiresAt = nil
		} else if input.ExpiresAt != nil {
			expiresAt := *input.ExpiresAt
			v.ExpiresAt = &expiresAt
		} else {
			v.ExpiresAt = current.ExpiresAt
		}
		if input.AutoRenewEnabled == nil {
			v.AutoRenewEnabled = current.AutoRenewEnabled
		} else {
			v.AutoRenewEnabled = *input.AutoRenewEnabled
		}
		if input.ExpiryNotifyEnabled == nil {
			v.ExpiryNotifyEnabled = current.ExpiryNotifyEnabled
		} else {
			v.ExpiryNotifyEnabled = *input.ExpiryNotifyEnabled
		}
		if input.RenewalCycle == nil {
			v.RenewalCycle = current.RenewalCycle
		} else {
			v.RenewalCycle = normalizeServerRenewalCycle(*input.RenewalCycle)
		}
		// Server traffic reset auto-derivation: start > expires, day precision only.
		if input.TrafficResetMode == nil && input.TrafficResetDay == nil {
			billingChanged := input.ServiceStartAt != nil || (input.ClearServiceStartAt != nil && *input.ClearServiceStartAt) || input.ExpiresAt != nil || (input.ClearExpiresAt != nil && *input.ClearExpiresAt)
			if billingChanged {
				settings, _ := s.store.ListSettings(r.Context())
				loc := trafficLocation(settings)
				if derivedMode, derivedDay, ok := deriveServerTrafficReset(nil, nil, v.ServiceStartAt, v.ExpiresAt, loc); ok {
					v.TrafficResetMode = derivedMode
					v.TrafficResetDay = derivedDay
				} else {
					v.TrafficResetMode = current.TrafficResetMode
					if strings.TrimSpace(v.TrafficResetMode) == "" {
						v.TrafficResetMode = "monthly"
					}
					v.TrafficResetDay = current.TrafficResetDay
					if v.TrafficResetDay == 0 {
						v.TrafficResetDay = 1
					}
				}
			} else {
				v.TrafficResetMode = current.TrafficResetMode
				if strings.TrimSpace(v.TrafficResetMode) == "" {
					v.TrafficResetMode = "monthly"
				}
				v.TrafficResetDay = current.TrafficResetDay
				if v.TrafficResetDay == 0 {
					v.TrafficResetDay = 1
				}
			}
		} else {
			if input.TrafficResetMode == nil {
				v.TrafficResetMode = current.TrafficResetMode
				if strings.TrimSpace(v.TrafficResetMode) == "" {
					v.TrafficResetMode = "monthly"
				}
			} else {
				v.TrafficResetMode = normalizeControllerTrafficResetMode(*input.TrafficResetMode)
			}
			if input.TrafficResetDay == nil {
				v.TrafficResetDay = current.TrafficResetDay
				if v.TrafficResetDay == 0 {
					v.TrafficResetDay = 1
				}
			} else {
				v.TrafficResetDay = normalizeControllerTrafficResetDay(*input.TrafficResetDay)
			}
		}
		if input.TrafficLimitBytes == nil {
			v.TrafficLimitBytes = current.TrafficLimitBytes
		} else {
			if *input.TrafficLimitBytes < 0 {
				fail(w, errors.New("traffic_limit_bytes must be >= 0"), 400)
				return
			}
			v.TrafficLimitBytes = *input.TrafficLimitBytes
		}
		if input.TrafficUsedBytes != nil && *input.TrafficUsedBytes < 0 {
			fail(w, errors.New("traffic_used_bytes must be >= 0"), 400)
			return
		}
		if v.DisplayTags == nil {
			v.DisplayTags = current.DisplayTags
		}
		v.LatencyProbeEnabled = current.LatencyProbeEnabled
		v.LatencyProbeMode = current.LatencyProbeMode
		v.LatencyProbePublicTarget = current.LatencyProbePublicTarget
		v.LatencyProbeIntervalSeconds = current.LatencyProbeIntervalSeconds
		v.LatencyProbeSampleCount = current.LatencyProbeSampleCount
		v.LatencyProbeRegions = current.LatencyProbeRegions
		v.LatencyProbeMaxTargets = current.LatencyProbeMaxTargets
		v.LatencyProbeResourceVersion = current.LatencyProbeResourceVersion
		if input.LatencyProbeEnabled != nil {
			v.LatencyProbeEnabled = *input.LatencyProbeEnabled
		}
		if input.LatencyProbeMode != nil {
			v.LatencyProbeMode = *input.LatencyProbeMode
		}
		if input.LatencyProbePublicTarget != nil {
			v.LatencyProbePublicTarget = *input.LatencyProbePublicTarget
		}
		if input.LatencyProbeInterval != nil {
			v.LatencyProbeIntervalSeconds = *input.LatencyProbeInterval
		}
		if input.LatencyProbeSamples != nil {
			v.LatencyProbeSampleCount = *input.LatencyProbeSamples
		}
		if input.LatencyProbeRegions != nil {
			v.LatencyProbeRegions = *input.LatencyProbeRegions
		}
		if input.LatencyProbeMaxTargets != nil {
			v.LatencyProbeMaxTargets = *input.LatencyProbeMaxTargets
		}
		// Automatic region is Agent telemetry. Panel edits may select auto or a
		// manual region, but cannot replace the last detected value.
		v.DetectedRegionCode = current.DetectedRegionCode
		if err := s.validateServerUpdateCandidate(r.Context(), *current, &v); err != nil {
			var migration *serverPortMigrationRequiredError
			if errors.As(err, &migration) {
				write(w, http.StatusConflict, map[string]any{
					"error":   "port_migration_required",
					"message": migration.Error(),
					"preview": migration.Preview,
				})
				return
			}
			var conflict *serverNameConflictError
			if errors.As(err, &conflict) {
				fail(w, err, http.StatusConflict)
				return
			}
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateServer(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if input.TrafficUsedBytes != nil {
			settings, _ := s.store.ListSettings(r.Context())
			loc := trafficLocation(settings)
			k, sTime, eTime := trafficWindow(time.Now(), v.TrafficResetMode, v.TrafficResetDay, time.Time{}, loc)
			window := model.ServerTrafficWindow{Key: k, Start: sTime, End: eTime}
			if err := s.store.SetServerTrafficUsed(r.Context(), v.ID, *input.TrafficUsedBytes, window); err != nil {
				fail(w, err, 500)
				return
			}
		}
		auditReq(s, r, "update", "server", fmt.Sprint(id))
		updated, _ := s.store.GetServer(r.Context(), v.ID)
		response := map[string]any{"server": updated}
		if current.TimeCorrectionMode != v.TimeCorrectionMode {
			if err := s.store.ResetServerTimeCheck(r.Context(), v.ID); err != nil {
				fail(w, err, 500)
				return
			}
			updated, _ = s.store.GetServer(r.Context(), v.ID)
			response["server"] = updated
		}
		if current.TimeCorrectionMode != v.TimeCorrectionMode && strings.TrimSpace(current.AgentID) != "" && current.Status != model.ServerOffline {
			if task, err := s.queueTimeCheck(r.Context(), *updated, true); err == nil {
				response["time_check_task"] = task
			} else {
				response["time_check_error"] = err.Error()
			}
		}
		write(w, 200, response)
		return
	}
	if r.Method == http.MethodDelete {
		if status, err := s.deleteServerRecord(r.Context(), id, requestActorID(r), clientIP(r)); err != nil {
			fail(w, err, status)
			return
		}
		write(w, 200, map[string]any{"deleted": true})
		return
	}
	method(w)
}

func (s *Server) deleteServerRecord(ctx context.Context, id int64, actorID *int64, ip string) (int, error) {
	if err := s.store.CleanupRoutingForServer(ctx, id); err != nil {
		return 500, err
	}
	if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
		return 500, err
	}
	if err := s.store.DeleteServerTelemetry(ctx, id); err != nil {
		return 500, err
	}
	if inbounds, err := s.store.ListInbounds(ctx); err != nil {
		return 500, err
	} else {
		for _, inbound := range inbounds {
			if inbound.ServerID == id {
				if _, err := s.store.RemoveAssignableNodeFromPlans(ctx, model.AssignableNodeInbound, inbound.ID); err != nil {
					return http.StatusConflict, err
				}
				if err := s.deleteDNSInboundRecords(ctx, inbound); err != nil {
					return http.StatusBadGateway, err
				}
				if err := s.store.DeleteProxyPathsForInbound(ctx, inbound.ID); err != nil {
					return 500, err
				}
				if err := s.store.DeleteInboundProbeResults(ctx, inbound.ID); err != nil {
					return 500, err
				}
				if err := s.store.Delete(ctx, "inbounds", inbound.ID); err != nil {
					return 500, err
				}
			}
		}
	}
	if err := s.store.Delete(ctx, "servers", id); err != nil {
		return 500, err
	}
	_ = s.store.AddAudit(ctx, model.AuditLog{ActorID: actorID, Action: "delete", Target: "server", Detail: fmt.Sprint(id), IP: ip})
	return 0, nil
}

func (s *Server) serverTasks(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, err := s.store.GetServer(r.Context(), id); err != nil {
		fail(w, err, 404)
		return
	}
	items, err := s.store.ListTasksByServer(r.Context(), id, intQuery(r, "limit", 100))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"tasks": sanitizeTasksForRole(items, currentRole(r))})
}

const (
	agentBuildMinDiagnosticsTask   = "20260706000116"
	agentBuildMinSSHPathRelay      = "20260804000000"
	agentBuildMinNetworkInterfaces = "20260804155957"
	agentBuildMinLatencyProbe      = "20260812000000"
	// Pending and running tasks both expire after 5 minutes so the panel does
	// not keep "waiting" forever for dead Agents or stuck executions.
	agentTaskPendingTimeout = 5 * time.Minute
	agentTaskRunningTimeout = 5 * time.Minute
)

func (s *Server) expireTimedOutTasks(ctx context.Context) {
	now := time.Now()
	// update_agent stays running through its restart phase, so it needs the
	// specific verdict before the generic running-task sweep can claim it.
	s.expireStuckAgentUpdateRestarts(ctx)
	pendingResult, _ := json.Marshal(map[string]any{
		"message":         "任务等待超时",
		"error":           "任务超过 5 分钟仍未被 Agent 领取执行，已标记为超时。请确认服务器在线后重新执行。",
		"timeout":         true,
		"timeout_seconds": int(agentTaskPendingTimeout.Seconds()),
	})
	runningResult, _ := json.Marshal(map[string]any{
		"message":         "任务执行超时",
		"error":           "Agent 超过 5 分钟未回传执行结果，任务已标记为超时。请查看 Agent 日志后重试。",
		"timeout":         true,
		"timeout_seconds": int(agentTaskRunningTimeout.Seconds()),
	})
	failed, err := s.store.FailTimedOutTasks(ctx, now.Add(-agentTaskPendingTimeout), now.Add(-agentTaskRunningTimeout), string(pendingResult), string(runningResult))
	if err != nil {
		log.Printf("expire timed out tasks: %v", err)
		return
	}
	for _, task := range failed {
		if task.Type == model.AgentTaskTypeBenchmarkDNS {
			_ = s.store.FailDNSBenchmarkRunForTask(ctx, task.ID, task.ResultJSON)
		}
		if task.Type == model.AgentTaskTypeApplyCoreConfig {
			_ = s.store.CompleteDNSBenchmarkApplyTask(ctx, task.ID, false, task.ResultJSON)
		}
		if task.Type == model.AgentTaskTypeProbeExternalEgress || task.Type == model.AgentTaskTypeApplyDeployment {
			_ = s.applyExternalEgressTaskResults(ctx, task.ServerID, task, "failed", task.ResultJSON)
		}
		if err := s.applyTimeCheckTaskResult(ctx, task, "failed", task.ResultJSON); err != nil {
			log.Printf("apply timed out time check task %d: %v", task.ID, err)
		}
		s.recordConfigurationTaskResult(ctx, task, "failed", task.ResultJSON)
		s.notifyTaskFailure(ctx, task)
	}
	if len(failed) > 0 {
		s.publishRealtime(realtimeResourcesForTasks(failed)...)
	}
}

// agentTaskImmediateFailure returns a user-facing failure message when the
// target server cannot receive work right now. Empty means the task may be queued.
func agentTaskImmediateFailure(server *model.Server) string {
	if server == nil {
		return "服务器不存在，任务无法下发"
	}
	if strings.TrimSpace(server.AgentID) == "" {
		return "Agent 未接入，任务无法下发。请先在服务器上安装并完成注册。"
	}
	if server.Status == model.ServerOffline {
		return "服务器离线，任务无法下发。请确认 Agent 在线后重新执行。"
	}
	return ""
}

func agentBuildSupportsTask(build string, minBuild string) bool {
	build = strings.TrimSpace(build)
	minBuild = strings.TrimSpace(minBuild)
	if build == "" || minBuild == "" {
		return false
	}
	if len(build) == len(minBuild) && isDigits(build) && isDigits(minBuild) {
		return build >= minBuild
	}
	return build == minBuild || build > minBuild
}

func isDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func (s *Server) queueAgentTask(ctx context.Context, serverID int64, taskType string, payload any, configVersion int64) (model.AgentTask, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return model.AgentTask{}, err
	}
	return s.createAgentTask(ctx, serverID, taskType, string(payloadJSON), configVersion)
}

// versionGuardedTaskType reports whether an Agent runs this task type behind
// its monotonic applied-version watermark. Both types share one watermark, so
// they form a single totally ordered stream per server.
func versionGuardedTaskType(taskType string) bool {
	return taskType == model.AgentTaskTypeApplyDeployment || taskType == model.AgentTaskTypeApplyCoreConfig
}

func (s *Server) createAgentTask(ctx context.Context, serverID int64, taskType, payloadJSON string, configVersion int64) (model.AgentTask, error) {
	if configVersion == 0 {
		configVersion = time.Now().Unix()
	}
	if versionGuardedTaskType(taskType) {
		// A guarded task that does not advance the watermark is skipped on
		// arrival, leaving the sync state pinned to a version it can never
		// reach. Refuse it here, where the caller can still allocate a fresh
		// version, instead of discovering it a round trip later.
		previous, err := s.store.MaxGuardedConfigVersion(ctx, serverID)
		if err != nil {
			return model.AgentTask{}, err
		}
		if configVersion <= previous {
			return model.AgentTask{}, fmt.Errorf("refusing to queue %s for server %d at config_version %d: not newer than the last queued version %d", taskType, serverID, configVersion, previous)
		}
	}
	nonce, err := security.RandomToken(12)
	if err != nil {
		return model.AgentTask{}, err
	}
	task := model.AgentTask{ServerID: serverID, Type: taskType, PayloadJSON: payloadJSON, Status: "pending", ResultJSON: "{}", ConfigVersion: configVersion, Nonce: nonce}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return model.AgentTask{}, err
		}
		server = nil
	}
	if reason := agentTaskImmediateFailure(server); reason != "" && !(automaticConfigurationSync(ctx) && (taskType == model.AgentTaskTypeApplyDeployment || taskType == model.AgentTaskTypeApplyCoreConfig)) && taskType != model.AgentTaskTypeApplyTrafficPolicy {
		result, _ := json.Marshal(map[string]any{
			"message": reason,
			"error":   reason,
			"offline": true,
		})
		task.Status = "failed"
		task.ResultJSON = string(result)
		if err := s.store.CreateTask(ctx, &task); err != nil {
			return model.AgentTask{}, err
		}
		s.notifyTaskFailure(ctx, task)
		s.publishRealtime(realtimeResourcesForTask(task.Type)...)
		return task, nil
	}
	if versionGuardedTaskType(taskType) {
		_ = s.store.SupersedePendingOperationalTasks(ctx, serverID, "配置下发优先，诊断类任务已取消")
		// Anything still queued below this version lost its meaning the moment
		// this task was allocated. Retiring both guarded types together matters
		// because they share one watermark: a stale core refresh behind a newer
		// deployment would otherwise sit in the queue only to be skipped.
		_ = s.store.SupersedeStaleGuardedTasks(ctx, serverID, configVersion, "已被更新的配置下发取代")
	}
	if err := s.createTaskAndWake(ctx, &task); err != nil {
		return model.AgentTask{}, err
	}
	s.publishRealtime(realtimeResourcesForTask(task.Type)...)
	return task, nil
}

func (s *Server) serverAgentUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for Agent updates"), 403)
		return
	}
	var req model.AgentUpdateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(w, err, 400)
			return
		}
	}
	if _, err := s.publicBaseURL(r.Context()); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	task, existing, err := s.enqueueAgentUpdate(r.Context(), server, req)
	if err != nil {
		status := 500
		if strings.Contains(err.Error(), "not enrolled") {
			status = 400
		} else if strings.Contains(err.Error(), "not allowed") {
			status = 400
		} else if errors.Is(err, errAgentUpdateOffline) {
			status = http.StatusConflict
		} else if strings.Contains(err.Error(), "已是最新") {
			status = http.StatusConflict
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "update", "agent", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task, "existing": existing})
}

func (s *Server) serverAgentUninstall(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for Agent uninstall"), 403)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if strings.TrimSpace(server.AgentID) == "" {
		fail(w, errors.New("agent is not enrolled"), 400)
		return
	}
	if server.Status == model.ServerOffline {
		fail(w, errors.New("服务器离线，无法远程卸载；可取消勾选直接删除"), http.StatusConflict)
		return
	}
	if !agentUninstallSupported(server) {
		fail(w, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再远程卸载；也可取消勾选直接删除"), http.StatusConflict)
		return
	}
	task, existing, err := s.enqueueAgentUninstall(r.Context(), server, requestActorID(r))
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "uninstall", "agent", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task, "existing": existing})
}

func agentUninstallSupported(server *model.Server) bool {
	if version.IsDev() {
		return true
	}
	minBuild := strings.TrimSpace(version.AgentBuild)
	if minBuild == "" || minBuild == "dev" {
		return true
	}
	if strings.TrimSpace(server.AgentBuild) == "" {
		return true
	}
	return agentBuildSupportsTask(server.AgentBuild, minBuild)
}

func (s *Server) enqueueAgentUninstall(ctx context.Context, server *model.Server, actorID *int64) (model.AgentTask, bool, error) {
	if server == nil {
		return model.AgentTask{}, false, errors.New("server not found")
	}
	if strings.TrimSpace(server.AgentID) == "" {
		return model.AgentTask{}, false, errors.New("agent is not enrolled")
	}
	if server.Status == model.ServerOffline {
		return model.AgentTask{}, false, errors.New("服务器离线，无法远程卸载")
	}
	if !agentUninstallSupported(server) {
		return model.AgentTask{}, false, errors.New("服务器 Agent 版本过旧，请先更新 Agent")
	}
	_ = s.store.FailStaleActiveTasksByServerType(ctx, server.ID, model.AgentTaskTypeUninstallAgent, time.Now().Add(-10*time.Minute), `{"message":"卸载任务超时，已允许重新创建"}`)
	if active, err := s.store.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeUninstallAgent); err == nil {
		return *active, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.AgentTask{}, false, err
	}
	actorIDValue := int64(0)
	if actorID != nil {
		actorIDValue = *actorID
	}
	task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeUninstallAgent, model.UninstallAgentTaskPayload{Purge: true, ActorID: actorIDValue}, time.Now().Unix())
	if err != nil {
		return model.AgentTask{}, false, err
	}
	return task, false, nil
}

// agentsUpdateAll fills the bounded Agent fleet update window. It does not
// enqueue one task per server and never creates update_agent tasks for offline hosts.
func (s *Server) agentsUpdateAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if _, err := s.publicBaseURL(r.Context()); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if s.agentUpdates == nil {
		fail(w, errors.New("Agent 更新协调器不可用"), http.StatusServiceUnavailable)
		return
	}
	result := s.agentUpdates.Fill(r.Context(), true)
	s.agentUpdates.Wake()
	created := result.Created
	if created < 0 {
		created = 0
	}
	existing := result.Running - created
	if existing < 0 {
		existing = 0
	}
	auditReq(s, r, "update", "agent", fmt.Sprintf("fleet:%d", created))
	write(w, 202, map[string]any{
		"summary": map[string]int{
			"total":    result.Enrolled,
			"created":  created,
			"existing": existing,
			"skipped":  result.Offline,
			"failed":   0,
		},
		"running":         result.Running,
		"pending":         result.Pending,
		"offline":         result.Offline,
		"current":         result.Current,
		"rolling":         result.Rolling,
		"max_concurrency": result.Limit,
		"config_version":  time.Now().Unix(),
	})
}

func taskResultMessage(task model.AgentTask) string {
	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(task.ResultJSON), &payload); err != nil {
		return strings.TrimSpace(task.ResultJSON)
	}
	if strings.TrimSpace(payload.Error) != "" {
		return payload.Error
	}
	return payload.Message
}

func (s *Server) enqueueAgentUpdate(ctx context.Context, server *model.Server, req model.AgentUpdateRequest) (model.AgentTask, bool, error) {
	return s.enqueueAgentUpdateWithVersion(ctx, server, req, time.Now().Unix())
}

func (s *Server) enqueueAgentUpdateWithVersion(ctx context.Context, server *model.Server, req model.AgentUpdateRequest, configVersion int64) (model.AgentTask, bool, error) {
	if server == nil {
		return model.AgentTask{}, false, errors.New("server not found")
	}
	if strings.TrimSpace(server.AgentID) == "" {
		return model.AgentTask{}, false, errors.New("agent is not enrolled")
	}
	if server.Status != model.ServerOnline {
		return model.AgentTask{}, false, errAgentUpdateOffline
	}
	targetBuild := strings.TrimSpace(version.AgentBuild)
	if targetBuild != "" && !strings.EqualFold(targetBuild, "dev") {
		if !buildNeedsUpdate(strings.TrimSpace(server.AgentBuild), targetBuild) {
			return model.AgentTask{}, false, fmt.Errorf("Agent 已是最新版本 (%s)，无需更新", emptyDash(strings.TrimSpace(server.AgentBuild)))
		}
	}
	source := strings.ToLower(strings.TrimSpace(req.Source))
	if source == "" {
		source = "auto"
	}
	repo := strings.TrimSpace(req.GitHubRepo)
	if repo == "" {
		repo = defaultAgentUpdateRepo
	}
	if !agentUpdateRepoAllowed(repo) {
		return model.AgentTask{}, false, fmt.Errorf("github repo %s is not allowed", repo)
	}
	if configVersion == 0 {
		configVersion = time.Now().Unix()
	}
	controllerURL, err := s.publicBaseURL(ctx)
	if err != nil {
		return model.AgentTask{}, false, err
	}
	nonce, err := security.RandomToken(12)
	if err != nil {
		return model.AgentTask{}, false, err
	}
	payload, err := json.Marshal(model.UpdateAgentTaskPayload{
		ControllerURL: controllerURL,
		ExpectedBuild: version.AgentBuild,
		Source:        source,
		GitHubRepo:    repo,
	})
	if err != nil {
		return model.AgentTask{}, false, err
	}
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeUpdateAgent, PayloadJSON: string(payload), Status: "pending", ResultJSON: "{}", ConfigVersion: configVersion, Nonce: nonce}
	got, created, err := s.store.EnqueueUniqueAgentTask(ctx, task, time.Now().Add(-10*time.Minute))
	if err != nil {
		return model.AgentTask{}, false, err
	}
	if created {
		s.tasks.wake(server.ID)
		s.publishRealtime(realtimeResourcesForTask(task.Type)...)
	}
	if got == nil {
		return model.AgentTask{}, false, errors.New("agent update was not queued")
	}
	return *got, !created, nil
}

var errAgentUpdateOffline = errors.New("服务器离线，将在重新连接后更新")

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

const defaultAgentUpdateRepo = "OboardProject/oboard-agent"

func agentUpdateRepoAllowed(repo string) bool {
	repo = strings.TrimSpace(repo)
	allowed := map[string]bool{defaultAgentUpdateRepo: true}
	for _, item := range strings.Split(os.Getenv("OBOARD_UPDATE_REPO_ALLOWLIST"), ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			allowed[item] = true
		}
	}
	return allowed[repo]
}

func (s *Server) serverAgentConfig(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for Agent command settings"), 403)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if strings.TrimSpace(server.AgentID) == "" {
		fail(w, errors.New("agent is not enrolled"), 400)
		return
	}
	var cfg map[string]any
	if !decode(w, r, &cfg) {
		return
	}
	allowed := map[string]bool{
		"controller_url": true, "state_dir": true, "core_binary": true, "core_service": true,
		"command_timeout_seconds": true,
		"reload_command":          true, "restart_command": true, "time_sync_command": true,
		"log_max_mb": true, "log_backups": true, "core_log_max_mb": true, "core_log_backups": true,
	}
	clean := map[string]any{}
	for key, value := range cfg {
		if !allowed[key] {
			fail(w, fmt.Errorf("unsupported Agent setting %q", key), 400)
			return
		}
		clean[key] = value
	}
	if raw, ok := clean["controller_url"].(string); ok && strings.TrimSpace(raw) != "" {
		normalized, err := s.normalizeControllerURL(raw)
		if err != nil {
			fail(w, err, 400)
			return
		}
		clean["controller_url"] = normalized
	}
	for _, key := range []string{"state_dir", "core_binary"} {
		if raw, ok := clean[key].(string); ok && strings.TrimSpace(raw) != "" {
			if err := validateAgentManagedPath(key, raw); err != nil {
				fail(w, err, 400)
				return
			}
		}
	}
	if raw, ok := clean["core_service"].(string); ok && strings.TrimSpace(raw) != "" {
		if err := validateAgentServiceName(raw); err != nil {
			fail(w, err, 400)
			return
		}
	}
	for _, key := range []string{"reload_command", "restart_command", "time_sync_command"} {
		if raw, ok := clean[key].(string); ok {
			if err := validateAgentManagedCommand(key, raw); err != nil {
				fail(w, err, 400)
				return
			}
		}
	}
	if err := validateAgentIntegerSetting(clean, "command_timeout_seconds", 5, 120); err != nil {
		fail(w, err, 400)
		return
	}
	for _, key := range []string{"log_max_mb", "core_log_max_mb"} {
		if err := validateAgentIntegerSetting(clean, key, 1, 1024); err != nil {
			fail(w, err, 400)
			return
		}
	}
	for _, key := range []string{"log_backups", "core_log_backups"} {
		if err := validateAgentIntegerSetting(clean, key, 0, 20); err != nil {
			fail(w, err, 400)
			return
		}
	}
	task, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeUpdateAgentConfig, clean, time.Now().Unix())
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "update", "agent-config", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task})
}

func validateAgentManagedPath(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s must be an absolute path", field)
	}
	cleaned := filepath.Clean(value)
	if cleaned != value {
		return fmt.Errorf("%s must be a cleaned absolute path without . or .. segments", field)
	}
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("%s must not contain dot-dot segments", field)
	}
	switch field {
	case "state_dir":
		allowedPrefixes := []string{"/var/lib/oboard-agent", "/var/lib/oboard", "/opt/oboard-agent", "/opt/oboard"}
		ok := false
		for _, prefix := range allowedPrefixes {
			if cleaned == prefix || strings.HasPrefix(cleaned, prefix+string(filepath.Separator)) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("state_dir must be under /var/lib/oboard-agent, /var/lib/oboard, /opt/oboard-agent, or /opt/oboard")
		}
	case "core_binary":
		base := filepath.Base(cleaned)
		if base != "oboard-sb" && base != "sing-box" {
			return fmt.Errorf("core_binary base name must be oboard-sb or sing-box")
		}
		allowedPrefixes := []string{"/usr/local/bin", "/usr/bin", "/opt/oboard", "/opt/oboard-agent"}
		ok := false
		for _, prefix := range allowedPrefixes {
			if cleaned == prefix || strings.HasPrefix(cleaned, prefix+string(filepath.Separator)) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("core_binary must be under /usr/local/bin, /usr/bin, /opt/oboard, or /opt/oboard-agent")
		}
	}
	return nil
}

func validateAgentServiceName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 64 {
		return errors.New("core_service name is too long")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return errors.New("core_service must match [A-Za-z0-9._-]+")
	}
	return nil
}

func validateAgentIntegerSetting(values map[string]any, key string, minimum, maximum int) error {
	raw, exists := values[key]
	if !exists {
		return nil
	}
	number, ok := raw.(float64)
	if !ok || number != float64(int(number)) || number < float64(minimum) || number > float64(maximum) {
		return fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	return nil
}

func (s *Server) serverDiagnose(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for host diagnostics"), 403)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinDiagnosticsTask) {
		fail(w, fmt.Errorf("服务器 Agent 版本过旧：当前构建 %s，主控需要支持诊断任务的 Agent。请先在服务器管理的 Agent 命令里执行“更新”命令", emptyDash(server.AgentBuild)), 409)
		return
	}
	inbounds, err := s.store.ListInbounds(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	alwaysDomain := s.subscriptionAlwaysUseDomainHost(r.Context())
	targets := []model.DiagnosticTarget{}
	for _, inbound := range inbounds {
		if inbound.ServerID != server.ID || !inbound.Enabled {
			continue
		}
		host := core.ResolveEntryAddressHost(inbound, *server, alwaysDomain)
		if strings.TrimSpace(host) == "" {
			continue
		}
		ports, err := core.MieruInboundPorts(inbound)
		if err != nil {
			continue
		}
		for _, port := range ports {
			targets = append(targets, model.DiagnosticTarget{Name: inbound.Name, Protocol: inbound.Protocol, Host: host, Port: port})
		}
	}
	configVersion := time.Now().Unix()
	task, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeDiagnoseNetwork, model.DiagnoseNetworkTaskPayload{Version: configVersion, ServerID: server.ID, EntryTargets: targets}, configVersion)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "diagnose", "server", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task, "entry_targets": targets})
}

func (s *Server) serverNetworkInterfaces(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	if strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinNetworkInterfaces) {
		fail(w, fmt.Errorf("服务器 Agent 版本过旧：当前构建 %s，请先更新 Agent 后再读取网卡", emptyDash(server.AgentBuild)), http.StatusConflict)
		return
	}
	task, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeListNetworkInterfaces, map[string]any{}, time.Now().Unix())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "inspect", "server-network-interfaces", fmt.Sprint(id))
	write(w, http.StatusAccepted, map[string]any{"task": sanitizeTaskForRole(task, currentRole(r))})
}

func (s *Server) serverLogs(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for Agent logs"), 403)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinDiagnosticsTask) {
		fail(w, fmt.Errorf("服务器 Agent 版本过旧：当前构建 %s，主控需要支持诊断任务的 Agent。请先在服务器管理的 Agent 命令里执行“更新”命令", emptyDash(server.AgentBuild)), 409)
		return
	}
	var req model.CollectLogsTaskPayload
	if !decode(w, r, &req) {
		return
	}
	if req.Lines <= 0 {
		req.Lines = 120
	}
	if req.Lines > 2000 {
		req.Lines = 2000
	}
	req.Services = strings.ToLower(strings.TrimSpace(req.Services))
	if req.Services == "" {
		req.Services = "all"
	}
	if req.Services != "all" && req.Services != "agent" && req.Services != "core" {
		fail(w, errors.New("services must be one of all, agent, core"), 400)
		return
	}
	task, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeCollectLogs, req, time.Now().Unix())
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "collect_logs", "server", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task})
}

func (s *Server) serverLogsControl(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for Agent log control"), 403)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if strings.TrimSpace(server.AgentID) == "" {
		fail(w, errors.New("agent is not enrolled"), 400)
		return
	}
	var req model.ManageLogsTaskPayload
	if !decode(w, r, &req) {
		return
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	req.Services = strings.ToLower(strings.TrimSpace(req.Services))
	if req.Services == "" {
		req.Services = "all"
	}
	if req.Action != "rotate" && req.Action != "clear" {
		fail(w, errors.New("action must be rotate or clear"), 400)
		return
	}
	if req.Services != "all" && req.Services != "agent" && req.Services != "core" {
		fail(w, errors.New("services must be one of all, agent, core"), 400)
		return
	}
	task, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeManageLogs, req, time.Now().Unix())
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, req.Action, "server-logs", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task})
}

func sanitizeTasksForRole(items []model.AgentTask, role model.Role) []model.AgentTask {
	out := make([]model.AgentTask, len(items))
	for i := range items {
		out[i] = sanitizeTaskForRole(items[i], role)
	}
	return out
}

func sanitizeTaskForRole(task model.AgentTask, role model.Role) model.AgentTask {
	if roleAllows(role, model.RoleAdmin) {
		task.PayloadJSON = scrubManagedTunnelSecretsJSON(task.PayloadJSON)
		task.ResultJSON = scrubManagedTunnelSecretsJSON(task.ResultJSON)
		return task
	}
	if roleAllows(role, model.RoleOperator) {
		task.PayloadJSON = scrubSensitiveJSON(task.PayloadJSON)
		task.ResultJSON = scrubSensitiveJSON(task.ResultJSON)
		if task.Nonce != "" {
			task.Nonce = "<redacted>"
		}
		return task
	}
	if task.PayloadJSON != "" {
		task.PayloadJSON = "<redacted>"
	}
	if task.ResultJSON != "" {
		task.ResultJSON = "<redacted>"
	}
	if task.Nonce != "" {
		task.Nonce = "<redacted>"
	}
	return task
}

func scrubManagedTunnelSecretsJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return raw
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	scrubManagedTunnelSecretsValue(value)
	b, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(b)
}

func scrubManagedTunnelSecretsValue(value any) {
	secretKeys := map[string]bool{
		"client_private_key": true,
		"client_public_key":  true,
		"authorized_key":     true,
		"source_private_key": true,
		"source_public_key":  true,
		"target_private_key": true,
		"target_public_key":  true,
	}
	switch node := value.(type) {
	case map[string]any:
		wireGuardConfig := strings.EqualFold(strings.TrimSpace(fmt.Sprint(node["type"])), string(model.TunnelTypeWireGuard))
		trustedForwardConfig := node["max_clock_skew_seconds"] != nil && (node["receiver_id"] != nil || node["target_port"] != nil)
		sshInboundUser := node["user_id"] != nil && node["username"] != nil && node["password"] != nil
		for key, child := range node {
			lowerKey := strings.ToLower(key)
			if secretKeys[lowerKey] || (wireGuardConfig && (lowerKey == "private_key" || lowerKey == "peer_public_key")) || (trustedForwardConfig && lowerKey == "key") || (sshInboundUser && lowerKey == "password") {
				node[key] = "<redacted>"
				continue
			}
			if text, ok := child.(string); ok && strings.HasPrefix(strings.TrimSpace(text), "{") {
				var nested any
				if json.Unmarshal([]byte(text), &nested) == nil {
					if wireGuardConfig {
						if nestedMap, ok := nested.(map[string]any); ok {
							for _, nestedKey := range []string{"private_key", "peer_public_key"} {
								if _, exists := nestedMap[nestedKey]; exists {
									nestedMap[nestedKey] = "<redacted>"
								}
							}
						}
					}
					scrubManagedTunnelSecretsValue(nested)
					if encoded, err := json.Marshal(nested); err == nil {
						node[key] = string(encoded)
					}
					continue
				}
			}
			scrubManagedTunnelSecretsValue(child)
		}
	case []any:
		for _, child := range node {
			scrubManagedTunnelSecretsValue(child)
		}
	}
}

func publicProxyPathStep(step model.ProxyPathStep) model.ProxyPathStep {
	var cfg map[string]any
	if json.Unmarshal([]byte(step.ConfigJSON), &cfg) == nil {
		for _, key := range []string{"client_private_key", "client_public_key", "authorized_key", "source_private_key", "source_public_key", "target_private_key", "target_public_key"} {
			delete(cfg, key)
		}
		if b, err := json.Marshal(cfg); err == nil {
			step.ConfigJSON = string(b)
		}
	}
	return step
}

func publicProxyPathSteps(steps []model.ProxyPathStep) []model.ProxyPathStep {
	out := make([]model.ProxyPathStep, len(steps))
	for i := range steps {
		out[i] = publicProxyPathStep(steps[i])
	}
	return out
}

func publicProxyPathPlan(plan model.ProxyPathPlan) model.ProxyPathPlan {
	for i := range plan.Tunnels {
		plan.Tunnels[i].ConfigJSON = scrubManagedTunnelSecretsJSON(plan.Tunnels[i].ConfigJSON)
	}
	return plan
}

func publicTunnels(items []model.Tunnel) []model.Tunnel {
	out := make([]model.Tunnel, len(items))
	copy(out, items)
	for i := range out {
		if out[i].Type == model.TunnelTypeWireGuard {
			var cfg map[string]any
			if json.Unmarshal([]byte(out[i].ConfigJSON), &cfg) == nil {
				for _, key := range []string{"private_key", "peer_public_key", "source_private_key", "source_public_key", "target_private_key", "target_public_key"} {
					if _, ok := cfg[key]; ok {
						cfg[key] = "<redacted>"
					}
				}
				if b, err := json.Marshal(cfg); err == nil {
					out[i].ConfigJSON = string(b)
				}
			}
		} else {
			out[i].ConfigJSON = scrubManagedTunnelSecretsJSON(out[i].ConfigJSON)
		}
	}
	return out
}

func scrubSensitiveJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return raw
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "<redacted>"
	}
	scrubSensitiveValue(obj)
	b, err := json.Marshal(obj)
	if err != nil {
		return "<redacted>"
	}
	return string(b)
}

func scrubSensitiveValue(v any) {
	switch node := v.(type) {
	case map[string]any:
		for key, child := range node {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "private") || lower == "config" || lower == "config_json" ||
				strings.Contains(lower, "authorization") || strings.Contains(lower, "key_path") || strings.Contains(lower, "uuid") {
				node[key] = "<redacted>"
				continue
			}
			scrubSensitiveValue(child)
		}
	case []any:
		for i := range node {
			scrubSensitiveValue(node[i])
		}
	}
}
func validateServer(v *model.Server) error {
	if v.Name == "" {
		return errors.New("name required")
	}
	v.RenewalCycle = normalizeServerRenewalCycle(v.RenewalCycle)
	if v.OfflineAfterSeconds < 0 || v.OfflineAfterSeconds > 86400 {
		return errors.New("offline_after_seconds must be between 0 and 86400")
	}
	if v.LatencyProbeIntervalSeconds != 0 && (v.LatencyProbeIntervalSeconds < 30 || v.LatencyProbeIntervalSeconds > 86400) {
		return errors.New("延迟测试间隔必须在 30 到 86400 秒之间")
	}
	if v.LatencyProbeIntervalSeconds == 0 {
		v.LatencyProbeIntervalSeconds = 60
	}
	if v.LatencyProbeSampleCount < 0 || v.LatencyProbeSampleCount > 10 {
		return errors.New("延迟测试的每个目标样本数必须在 1 到 10 之间")
	}
	if v.LatencyProbeSampleCount == 0 {
		v.LatencyProbeSampleCount = 3
	}
	if v.LatencyProbeMaxTargets < 0 || v.LatencyProbeMaxTargets > 256 {
		return errors.New("延迟测试的单次目标数必须在 1 到 256 之间")
	}
	if v.LatencyProbeMaxTargets == 0 {
		v.LatencyProbeMaxTargets = 64
	}
	var err error
	v.LatencyProbeRegions, err = normalizeLatencyProbeRegions(v.LatencyProbeRegions)
	if err != nil {
		return fmt.Errorf("latency_probe_regions: %w", err)
	}
	switch v.LatencyProbeMode {
	case "", model.LatencyProbeModeTCP:
		v.LatencyProbeMode = model.LatencyProbeModeTCP
	case model.LatencyProbeModeICMP:
	default:
		return errors.New("延迟测试方式必须是 TCP Ping 或 ICMP Ping")
	}
	switch v.LatencyProbePublicTarget {
	case "", model.ConnectivityProbeTargetAuto:
		v.LatencyProbePublicTarget = model.ConnectivityProbeTargetAuto
	case model.ConnectivityProbeTargetCloudflare, model.ConnectivityProbeTarget12306, model.ConnectivityProbeTargetGoogle:
	default:
		return errors.New("公网延迟目标必须是自动、Cloudflare、12306 或 Google")
	}
	if v.ListenIP == "" {
		v.ListenIP = "0.0.0.0"
	}
	if v.ListenMode == "" {
		v.ListenMode = model.ListenModeAuto
	}
	if v.IPStack == "" {
		v.IPStack = model.IPStackAuto
	}
	if v.EntryIPMode == "" {
		v.EntryIPMode = model.EntryIPModeAuto
	}
	if v.UDPInboundMode == "" {
		v.UDPInboundMode = model.UDPInboundAllow
	}
	if v.MTUMode == "" {
		v.MTUMode = model.MTUModeDetect
	}
	if v.MTUProbeHost == "" {
		v.MTUProbeHost = core.DefaultBootstrapForIPStack(v.IPStack)
	}
	if v.MTUProbePort == 0 {
		v.MTUProbePort = 443
	}
	if v.PortRangeStart == 0 {
		v.PortRangeStart = core.DefaultPublicPortRangeStart
	}
	if v.PortRangeEnd == 0 {
		v.PortRangeEnd = core.DefaultPublicPortRangeEnd
	}
	if v.InternalPortRangeStart == 0 {
		v.InternalPortRangeStart = core.DefaultInternalPortRangeStart
	}
	if v.InternalPortRangeEnd == 0 {
		v.InternalPortRangeEnd = core.DefaultInternalPortRangeEnd
	}
	switch strings.ToLower(strings.TrimSpace(v.MonitoringMode)) {
	case "", "lightweight":
		v.MonitoringMode = "lightweight"
	case "standard":
		v.MonitoringMode = "standard"
	default:
		return errors.New("monitoring_mode must be lightweight or standard")
	}
	if v.TrafficLimitBytes < 0 {
		return errors.New("traffic_limit_bytes must be >= 0")
	}
	v.TrafficResetMode = normalizeControllerTrafficResetMode(v.TrafficResetMode)
	v.TrafficResetDay = normalizeControllerTrafficResetDay(v.TrafficResetDay)
	normalizedTimeMode := normalizeControllerTimeCorrectionMode(v.TimeCorrectionMode)
	if v.TimeCorrectionMode != "" && normalizedTimeMode != model.TimeCorrectionMode(strings.ToLower(strings.TrimSpace(string(v.TimeCorrectionMode)))) {
		return errors.New("time_correction_mode must be off, auto, or ntp")
	}
	v.TimeCorrectionMode = normalizedTimeMode
	if err := validateServerRegion(v); err != nil {
		return err
	}
	if err := core.ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if err := core.ValidateListenMode(v.ListenMode); err != nil {
		return err
	}
	if err := core.ValidateIPStack(v.IPStack); err != nil {
		return err
	}
	if err := core.ValidateUDPInboundMode(v.UDPInboundMode); err != nil {
		return err
	}
	if err := validateEntryIPPolicy(v); err != nil {
		return err
	}
	if err := validateMTUPolicy(v); err != nil {
		return err
	}
	if err := core.ValidatePortRange(v.PortRangeStart, v.PortRangeEnd); err != nil {
		return err
	}
	v.DisplayTags, err = model.NormalizeServerDisplayTags(v.DisplayTags)
	if err != nil {
		return err
	}
	return core.ValidatePortRange(v.InternalPortRangeStart, v.InternalPortRangeEnd)
}

func normalizeLatencyProbeRegions(values []model.LatencyProbeRegion) ([]model.LatencyProbeRegion, error) {
	seen := map[string]bool{}
	out := make([]model.LatencyProbeRegion, 0, len(values))
	for _, value := range values {
		value.Province = strings.TrimSpace(value.Province)
		value.Carrier = strings.TrimSpace(value.Carrier)
		if value.Province == "" || value.Carrier == "" {
			return nil, errors.New("每个地区目标都必须同时选择省份和运营商")
		}
		key := value.Province + "\x00" + value.Carrier
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
		if len(out) > 200 {
			return nil, errors.New("地区延迟目标最多选择 200 组")
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Province == out[j].Province {
			return out[i].Carrier < out[j].Carrier
		}
		return out[i].Province < out[j].Province
	})
	return out, nil
}

func portPolicyChanged(current, next model.Server) bool {
	// validateServer fills default internal ranges for rows created before the
	// field existed; an unset stored value is the default policy, not a change.
	curInternalStart, curInternalEnd := current.InternalPortRangeStart, current.InternalPortRangeEnd
	if curInternalStart == 0 || curInternalEnd == 0 {
		curInternalStart, curInternalEnd = core.DefaultInternalPortRangeStart, core.DefaultInternalPortRangeEnd
	}
	return current.PortRangeStart != next.PortRangeStart ||
		current.PortRangeEnd != next.PortRangeEnd ||
		curInternalStart != next.InternalPortRangeStart ||
		curInternalEnd != next.InternalPortRangeEnd
}

// serverPortPolicyConflictMessage renders the 409 body for a server range PATCH
// that would exclude managed listeners. Migration is not implemented yet, so the
// message tells the operator exactly which listeners block the change instead of
// silently moving or clearing ports.
func serverPortPolicyConflictMessage(preview core.ServerPortPolicyChangePreview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "新端口范围会排除 %d 个由系统托管的监听端口，禁止直接保存。", len(preview.AffectedManaged))
	for _, item := range preview.AffectedManaged {
		label := item.Kind
		switch item.Kind {
		case model.ProxyPathPortKindChainService:
			label = "共享链路服务"
		case model.ProxyPathPortKindInternal:
			label = "链路内部入口"
		case model.ProxyPathPortKindTunnelSSH:
			label = "SSH 本地转发"
		case model.ProxyPathPortKindTunnelWG:
			label = "WireGuard 监听"
		}
		fmt.Fprintf(&b, "\n· %s %s: %d", label, item.ScopeKey, item.Port)
	}
	if len(preview.ManualOutsidePolicy) > 0 {
		fmt.Fprintf(&b, "\n另有 %d 个手工入口位于新范围之外（保持不动，仅提示）：", len(preview.ManualOutsidePolicy))
		for _, item := range preview.ManualOutsidePolicy {
			fmt.Fprintf(&b, "\n· 入口端口 %d", item.Port)
		}
	}
	fmt.Fprintf(&b, "\n请调整范围以包含以上端口，或先手动迁移这些服务。")
	return b.String()
}

func validateServerRegion(v *model.Server) error {
	return normalizeRegionSelection(&v.RegionMode, &v.RegionCode, "region")
}

func normalizeRegionSelection(mode, code *string, field string) error {
	*mode = strings.ToLower(strings.TrimSpace(*mode))
	*code = normalizeControllerRegionCode(*code)
	switch *mode {
	case "", "auto":
		*mode = "auto"
		*code = ""
	case "manual":
		if *code == "" {
			return fmt.Errorf("manual %s requires a two-letter region code", field)
		}
	default:
		return fmt.Errorf("%s_mode must be auto or manual", strings.ReplaceAll(field, " ", "_"))
	}
	return nil
}

func normalizeControllerRegionCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return code
}

func validateEntryIPPolicy(v *model.Server) error {
	switch v.EntryIPMode {
	case model.EntryIPModeAuto, model.EntryIPModeIPv4, model.EntryIPModeIPv6, model.EntryIPModeCustom:
	default:
		return fmt.Errorf("invalid entry_ip_mode %q", v.EntryIPMode)
	}
	if ip, family := cleanPublicEntryIP(v.PublicIPv4); strings.TrimSpace(v.PublicIPv4) != "" && family != "ipv4" {
		_ = ip
		return errors.New("public_ipv4 must be a public IPv4 address")
	}
	if ip, family := cleanPublicEntryIP(v.PublicIPv6); strings.TrimSpace(v.PublicIPv6) != "" && family != "ipv6" {
		_ = ip
		return errors.New("public_ipv6 must be a public IPv6 address")
	}
	trimmedEntryAddress := strings.TrimSpace(v.EntryAddress)
	if trimmedEntryAddress != "" && v.EntryIPMode != model.EntryIPModeCustom {
		return errors.New("自定义入口需将入口策略设为自定义")
	}
	if v.EntryIPMode == model.EntryIPModeCustom && trimmedEntryAddress == "" {
		return errors.New("custom entry address required")
	}
	v.EntryAddress = trimmedEntryAddress
	if v.EntryAddress != "" {
		if err := core.ValidateSafeHost(v.EntryAddress); err != nil {
			return fmt.Errorf("entry_address: %w", err)
		}
	}
	return nil
}

func validateMTUPolicy(v *model.Server) error {
	switch v.MTUMode {
	case model.MTUModeDisabled, model.MTUModeDetect, model.MTUModeApply:
	default:
		return fmt.Errorf("invalid mtu_mode %q", v.MTUMode)
	}
	if v.MTUProbeHost == "" {
		return errors.New("mtu_probe_host required")
	}
	if err := core.ValidatePort(v.MTUProbePort); err != nil {
		return fmt.Errorf("mtu_probe_port: %w", err)
	}
	if v.MTUValue < 0 || v.MTUValue > 9000 {
		return errors.New("mtu_value must be 0..9000")
	}
	if v.MTUOverheadBytes < 0 || v.MTUOverheadBytes > 512 {
		return errors.New("mtu_overhead_bytes must be 0..512")
	}
	return nil
}

func mtuPlanFromServer(version int64, srv model.Server, mode model.MTUMode) model.MTUDetectionPlan {
	if mode == "" {
		mode = srv.MTUMode
	}
	if mode == "" {
		mode = model.MTUModeDetect
	}
	effectiveStack := core.EffectiveIPStack(srv)
	host := srv.MTUProbeHost
	if strings.TrimSpace(host) == "" || core.ValidateAddressForIPStack(effectiveStack, host) != nil {
		host = core.DefaultBootstrapForIPStack(effectiveStack)
	}
	port := srv.MTUProbePort
	if port == 0 {
		port = 443
	}
	return model.MTUDetectionPlan{Version: version, ServerID: srv.ID, Mode: mode, TargetHost: host, TargetPort: port, OverheadBytes: srv.MTUOverheadBytes, DesiredMTU: srv.MTUValue, SampleCount: 3, TimeoutMS: 1200, MinMTU: 1280, MaxMTU: 9000}
}

func (s *Server) serverMTUDetect(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	srv, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	var req struct {
		Mode          model.MTUMode `json:"mode"`
		TargetHost    string        `json:"target_host"`
		TargetPort    int           `json:"target_port"`
		InterfaceName string        `json:"interface_name"`
		OverheadBytes int           `json:"overhead_bytes"`
		DesiredMTU    int           `json:"desired_mtu"`
		SampleCount   int           `json:"sample_count"`
		TimeoutMS     int           `json:"timeout_ms"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	version := time.Now().Unix()
	plan := mtuPlanFromServer(version, *srv, req.Mode)
	if req.TargetHost != "" {
		plan.TargetHost = req.TargetHost
	}
	if req.TargetPort != 0 {
		plan.TargetPort = req.TargetPort
	}
	if req.InterfaceName != "" {
		plan.InterfaceName = req.InterfaceName
	}
	if req.OverheadBytes >= 0 && r.ContentLength != 0 {
		plan.OverheadBytes = req.OverheadBytes
	}
	if req.DesiredMTU > 0 {
		plan.DesiredMTU = req.DesiredMTU
	}
	if req.SampleCount > 0 {
		plan.SampleCount = req.SampleCount
	}
	if req.TimeoutMS > 0 {
		plan.TimeoutMS = req.TimeoutMS
	}
	if plan.Mode == model.MTUModeDisabled {
		plan.Mode = model.MTUModeDetect
	}
	if plan.TargetHost == "" {
		fail(w, errors.New("target_host required"), 400)
		return
	}
	if err := core.ValidateSafeHost(plan.TargetHost); err != nil {
		fail(w, fmt.Errorf("target_host: %w", err), 400)
		return
	}
	if err := core.ValidateNetworkInterfaceName(plan.InterfaceName); err != nil {
		fail(w, fmt.Errorf("interface_name: %w", err), 400)
		return
	}
	if err := core.ValidatePort(plan.TargetPort); err != nil {
		fail(w, err, 400)
		return
	}
	task, err := s.queueAgentTask(r.Context(), srv.ID, model.AgentTaskTypeDetectMTU, plan, version)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "detect", "mtu", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task, "plan": plan})
}

func (s *Server) serverDNSTest(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Action != "test" && request.Action != "test_and_apply" {
		fail(w, errors.New("action must be test or test_and_apply"), 400)
		return
	}
	server, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	policy, err := s.store.EnsureServerDNSPolicy(r.Context(), server.ID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	encrypted, bootstrap, err := s.dnsPolicyLists(r.Context(), *policy)
	if err != nil {
		fail(w, err, 500)
		return
	}
	requestID, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, 500)
		return
	}
	version := time.Now().UnixNano()
	plan, err := core.DNSBenchmarkPlanForPolicy(version, *policy, encrypted, *bootstrap, core.EffectiveIPStack(*server), model.DNSAutoTestAlways, requestID)
	if err != nil {
		fail(w, err, 400)
		return
	}
	run := model.DNSBenchmarkRun{
		RequestID: requestID, ServerID: server.ID, PolicyRevision: policy.Revision,
		EncryptedListID: plan.EncryptedListID, EncryptedListRevision: plan.EncryptedListRevision,
		BootstrapListID: bootstrap.ID, BootstrapListRevision: bootstrap.Revision,
		Trigger: "manual", ApplyOnSuccess: request.Action == "test_and_apply", Status: "pending",
	}
	if actor := currentUser(r); actor != nil {
		run.RequestedBy = &actor.ID
	}
	if err := s.store.CreateDNSBenchmarkRun(r.Context(), &run); err != nil {
		fail(w, err, 500)
		return
	}
	task, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeBenchmarkDNS, plan, version)
	if err != nil {
		fail(w, err, 500)
		return
	}
	if err := s.store.AttachDNSBenchmarkTask(r.Context(), requestID, task.ID); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, request.Action, "dns", fmt.Sprint(server.ID))
	write(w, 202, map[string]any{"task": task, "run": run})
}

// dnsPolicyLists resolves the lists a DNS policy binds. A policy with
// EncryptedListID 0 resolves through the plain bootstrap resolvers only and
// returns a nil encrypted list.
func (s *Server) dnsPolicyLists(ctx context.Context, policy model.ServerDNSPolicy) (*model.DNSList, *model.DNSList, error) {
	var encrypted *model.DNSList
	if policy.EncryptedListID != 0 {
		item, err := s.store.GetDNSList(ctx, policy.EncryptedListID)
		if err != nil {
			return nil, nil, err
		}
		encrypted = item
	}
	bootstrap, err := s.store.GetDNSList(ctx, policy.BootstrapListID)
	if err != nil {
		return nil, nil, err
	}
	return encrypted, bootstrap, nil
}

func (s *Server) serverDNSPolicy(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := s.store.GetServer(r.Context(), id); err != nil {
		fail(w, err, 404)
		return
	}
	switch r.Method {
	case http.MethodGet:
		policy, err := s.store.EnsureServerDNSPolicy(r.Context(), id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"dns_policy": policy})
	case http.MethodPut:
		current, err := s.store.EnsureServerDNSPolicy(r.Context(), id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		var policy model.ServerDNSPolicy
		if !decode(w, r, &policy) {
			return
		}
		policy.ServerID = id
		// encrypted_list_id 0 means the server uses plain DNS only.
		if policy.EncryptedListID < 0 || policy.BootstrapListID == 0 {
			fail(w, errors.New("bootstrap_list_id is required and encrypted_list_id must not be negative"), 400)
			return
		}
		if policy.Strategy == "" {
			policy.Strategy = "auto"
		}
		if !validDNSStrategy(policy.Strategy) {
			fail(w, errors.New("unsupported dns strategy"), 400)
			return
		}
		if policy.AutoTest == "" {
			policy.AutoTest = model.DNSAutoTestFirstApply
		}
		if err := core.ValidateDNSAutoTest(policy.AutoTest); err != nil || policy.AutoTest == model.DNSAutoTestAlways {
			fail(w, errors.New("auto_test must be never, first_apply, or periodic"), 400)
			return
		}
		if err := s.store.UpdateServerDNSPolicy(r.Context(), &policy); err != nil {
			fail(w, err, 400)
			return
		}
		if current.AutoTest == model.DNSAutoTestPeriodic && policy.AutoTest != model.DNSAutoTestPeriodic {
			plan := model.DNSBenchmarkPlan{ServerID: id, PolicyRevision: policy.Revision, EncryptedListID: policy.EncryptedListID, BootstrapListID: policy.BootstrapListID, Mode: model.DNSAutoTestNever}
			_, _ = s.queueAgentTask(r.Context(), id, model.AgentTaskTypeBenchmarkDNS, plan, time.Now().UnixNano())
		}
		auditReq(s, r, "update", "server_dns_policy", fmt.Sprint(id))
		write(w, 200, map[string]any{"dns_policy": policy})
	default:
		method(w)
	}
}

func validDNSStrategy(strategy string) bool {
	switch strategy {
	case "auto", "ipv4_only", "ipv6_only", "prefer_ipv4", "prefer_ipv6":
		return true
	default:
		return false
	}
}

const enrollmentTokenTTL = 2 * time.Hour

func (s *Server) enrollToken(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for Agent enrollment"), 403)
		return
	}
	if _, err := s.store.GetServer(r.Context(), id); err != nil {
		fail(w, err, 404)
		return
	}
	token, expiresAt, _, err := s.issueServerEnrollmentToken(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "create", "enroll-token", fmt.Sprint(id))
	write(w, 200, map[string]any{"enrollment_token": token, "expires_at": expiresAt, "expires_in_seconds": int(enrollmentTokenTTL.Seconds())})
}

func (s *Server) inbounds(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/padding") {
		path := strings.TrimSuffix(strings.TrimRight(r.URL.Path, "/"), "/padding")
		s.anyTLSPaddingOperation(w, r, idFromPath(path, "/api/v1/inbounds/"))
		return
	}
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/probe") {
		path := strings.TrimSuffix(strings.TrimRight(r.URL.Path, "/"), "/probe")
		s.inboundProbeNow(w, r, idFromPath(path, "/api/v1/inbounds/"))
		return
	}
	id := idFromPath(r.URL.Path, "/api/v1/inbounds/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetInbound(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			item.Kind = inferredInboundKind(*item)
			write(w, 200, map[string]any{"inbound": item})
			return
		}
		items, err := s.store.ListInbounds(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		for i := range items {
			items[i].Kind = inferredInboundKind(items[i])
		}
		write(w, 200, map[string]any{"inbounds": items})
	case http.MethodPost:
		var v model.Inbound
		if !decode(w, r, &v) {
			return
		}
		if v.ConfigJSON == "" {
			v.ConfigJSON = "{}"
		}
		if err := applyInboundKindDefaults(&v, nil); err != nil {
			fail(w, err, 400)
			return
		}
		normalized, err := applyInboundConfigDefaults(v.Protocol, v.ConfigJSON)
		if err != nil {
			fail(w, err, 400)
			return
		}
		v.ConfigJSON = normalized
		v = normalizeInbound(v)
		if err := normalizeMieruInboundPorts(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.resolveInboundDNSCredential(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.application.PrepareInboundCreate(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.resolveInboundTemplates(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if v.ConfigJSON, err = applyInboundConfigDefaults(v.Protocol, v.ConfigJSON); err != nil {
			fail(w, err, 400)
			return
		}
		if err := validateInbound(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.ValidateServerExists(r.Context(), v.ServerID); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateInboundManagedReferences(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.ensureInboundListenAvailable(r.Context(), v); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if err := s.applyInboundDomainSideEffects(r.Context(), nil, &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateInbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.saveInboundCertificateBinding(r.Context(), v); err != nil {
			_ = s.store.Delete(r.Context(), "inbounds", v.ID)
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "inbound", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"inbound": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetInbound(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			fail(w, err, 400)
			return
		}
		defer r.Body.Close()
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			fail(w, err, 400)
			return
		}
		var v model.Inbound
		if err := json.Unmarshal(raw, &v); err != nil {
			fail(w, err, 400)
			return
		}
		v = mergeInboundPatch(*current, v, fields)
		v.ID = id
		if _, supplied := fields["anytls_padding"]; supplied {
			fail(w, errors.New("anytls_padding can only be changed through the explicit padding operation"), 400)
			return
		}
		if v.ConfigJSON == "" {
			v.ConfigJSON = "{}"
		}
		if err := applyInboundKindDefaults(&v, current); err != nil {
			fail(w, err, 400)
			return
		}
		normalized, err := applyInboundConfigDefaults(v.Protocol, v.ConfigJSON)
		if err != nil {
			fail(w, err, 400)
			return
		}
		v.ConfigJSON = normalized
		v = normalizeInbound(v)
		if current.Protocol == model.ProtocolAnyTLS && v.Protocol == model.ProtocolAnyTLS {
			v.ConfigJSON, err = core.PreserveAnyTLSPaddingSnapshot(current.ConfigJSON, v.ConfigJSON)
			if err != nil {
				fail(w, err, 400)
				return
			}
		} else if current.Protocol != model.ProtocolAnyTLS && v.Protocol == model.ProtocolAnyTLS {
			if err := s.application.PrepareInboundCreate(r.Context(), &v); err != nil {
				fail(w, err, 400)
				return
			}
		}
		if err := normalizeMieruInboundPorts(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.resolveInboundDNSCredential(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.resolveInboundTemplates(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if v.ConfigJSON, err = applyInboundConfigDefaults(v.Protocol, v.ConfigJSON); err != nil {
			fail(w, err, 400)
			return
		}
		if err := validateInbound(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.ValidateServerExists(r.Context(), v.ServerID); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateInboundManagedReferences(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.ensureInboundListenAvailable(r.Context(), v); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if err := s.applyInboundDomainSideEffects(r.Context(), current, &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.deleteStaleInboundDNS(r.Context(), current, v); err != nil {
			fail(w, err, 502)
			return
		}
		if err := s.store.UpdateInbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.saveInboundCertificateBinding(r.Context(), v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.syncInboundDNSIfChanged(r.Context(), current, v); err != nil {
			fail(w, err, 502)
			return
		}
		if stored, getErr := s.store.GetInbound(r.Context(), v.ID); getErr == nil {
			v = *stored
		}
		write(w, 200, map[string]any{"inbound": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		inbound, err := s.store.GetInbound(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		// Deleting an inbound also deletes every proxy path rooted at it, so
		// guard both the standalone inbound node and its paths against active
		// plan references.
		if _, err := s.guardAssignableNodeDelete(r.Context(), model.AssignableNodeInbound, id); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		paths, err := s.store.ListProxyPaths(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		for _, path := range paths {
			if path.InboundID == id {
				if _, err := s.guardAssignableNodeDelete(r.Context(), model.AssignableNodeProxyPath, path.ID); err != nil {
					fail(w, err, http.StatusConflict)
					return
				}
			}
		}
		if _, err := s.store.RemoveAssignableNodeFromPlans(r.Context(), model.AssignableNodeInbound, id); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if err := s.deleteDNSInboundRecords(r.Context(), *inbound); err != nil {
			fail(w, err, 502)
			return
		}
		if err := s.store.DeleteProxyPathsForInbound(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteInboundProbeResults(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.Delete(r.Context(), "inbounds", id); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) anyTLSPaddingPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	write(w, http.StatusOK, map[string]any{"presets": core.AnyTLSPaddingPresets()})
}

func (s *Server) anyTLSPaddingOperation(w http.ResponseWriter, r *http.Request, inboundID int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if inboundID <= 0 {
		fail(w, errors.New("missing id"), http.StatusBadRequest)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("administrator role required"), http.StatusForbidden)
		return
	}
	var operation core.AnyTLSPaddingOperation
	if !decode(w, r, &operation) {
		return
	}
	inbound, err := s.application.UpdateAnyTLSPadding(r.Context(), subscriptionCustomPathPrincipal(r), inboundID, operation)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "anytls_padding."+operation.Operation, "inbound", fmt.Sprint(inboundID))
	write(w, http.StatusOK, map[string]any{"inbound": inbound, "requires_deployment": true})
}
func (s *Server) ensureInboundListenAvailable(ctx context.Context, v model.Inbound) error {
	if !v.Enabled {
		return nil
	}
	listenIP := strings.TrimSpace(v.ListenIP)
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	items, err := s.store.ListInbounds(ctx)
	if err != nil {
		return err
	}
	ports, err := core.MieruInboundPorts(v)
	if err != nil {
		return err
	}
	for _, existing := range items {
		if existing.ID == v.ID || !existing.Enabled {
			continue
		}
		existingListenIP := strings.TrimSpace(existing.ListenIP)
		if existingListenIP == "" {
			existingListenIP = "0.0.0.0"
		}
		existingPorts, err := core.MieruInboundPorts(existing)
		if err != nil {
			return err
		}
		for _, port := range ports {
			for _, existingPort := range existingPorts {
				if existing.ServerID == v.ServerID && existingListenIP == listenIP && existingPort == port {
					return fmt.Errorf("inbound listen resource %s:%d on server %d already used by %s (id %d)", listenIP, port, v.ServerID, existing.Name, existing.ID)
				}
			}
		}
	}
	if v.AdvertisePort > 0 {
		advertisePort := v.AdvertisePort
		for _, existing := range items {
			if existing.ID == v.ID || !existing.Enabled {
				continue
			}
			if existing.ServerID != v.ServerID {
				continue
			}
			existingAdvertise := existing.AdvertisePort
			if existingAdvertise == 0 {
				existingAdvertise = existing.Port
			}
			// For Mieru, compare only primary ports; multi-port NAT is already rejected.
			if existingAdvertise == advertisePort {
				return fmt.Errorf("对外端口 %d 在服务器 %d 已被 %s (id %d) 占用", advertisePort, v.ServerID, existing.Name, existing.ID)
			}
			// Also prevent advertised port colliding with another inbound's listen port when NAT is off (same effective external port).
			if existingAdvertise == 0 {
				// handled above, but keep for clarity
			}
		}
	} else {
		// When NAT is off, the effective external port is the listen port; ensure no other inbound advertises this port.
		for _, existing := range items {
			if existing.ID == v.ID || !existing.Enabled || existing.ServerID != v.ServerID || existing.AdvertisePort == 0 {
				continue
			}
			if existing.AdvertisePort == v.Port {
				return fmt.Errorf("监听端口 %d 在服务器 %d 已被 %s (id %d) 的对外端口占用", v.Port, v.ServerID, existing.Name, existing.ID)
			}
		}
	}
	allocations, err := s.store.ListProxyPathPortAllocations(ctx)
	if err != nil {
		return err
	}
	return core.ValidateInboundManagedPortAvailability(v, allocations)
}

func (s *Server) userGroups(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/user-groups/")
	parts := pathParts(r.URL.Path, "/api/v1/user-groups/")
	if r.Method != http.MethodGet && id > 0 {
		if err := s.requireUserGroupMutationAccess(r.Context(), currentRole(r), id); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
	}
	if len(parts) == 2 && parts[1] == "subscription-custom-path-policy" {
		s.userGroupSubscriptionCustomPathPolicy(w, r, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetUserGroup(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"user_group": item})
			return
		}
		items, err := s.store.ListUserGroups(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"user_groups": items})
	case http.MethodPost:
		var v model.UserGroup
		if !decode(w, r, &v) {
			return
		}
		v.SystemKey = ""
		if err := requireAssignedRoleAccess(currentRole(r), v.Role); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if err := validateUserGroup(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateUserGroup(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "user-group", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"user_group": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetUserGroup(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if err := s.requireUserGroupMutationAccess(r.Context(), currentRole(r), id); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		mergeUserGroupPatch(&v, current)
		v.SystemKey = current.SystemKey
		if err := requireAssignedRoleAccess(currentRole(r), v.Role); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if current.SystemKey == store.UserGroupSystemAdmins {
			v.Role = model.RoleAdmin
			v.Enabled = true
		}
		if err := validateUserGroup(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateUserGroup(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "user-group", fmt.Sprint(id))
		write(w, 200, map[string]any{"user_group": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetUserGroup(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if err := s.requireUserGroupMutationAccess(r.Context(), currentRole(r), id); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if current.SystemKey != "" {
			fail(w, errors.New("系统用户组不允许删除"), 400)
			return
		}
		if err := s.store.DeleteUserGroupMembersForGroup(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.Delete(r.Context(), "user_groups", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "user-group", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) userGroupMembers(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/user-group-members/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetUserGroupMember(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"user_group_member": item})
			return
		}
		items, err := s.store.ListUserGroupMembers(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"user_group_members": items})
	case http.MethodPost:
		var v model.UserGroupMember
		if !decode(w, r, &v) {
			return
		}
		if err := s.requireUserGroupMutationAccess(r.Context(), currentRole(r), v.GroupID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if err := s.requireUserMutationAccess(r.Context(), currentRole(r), v.UserID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if err := s.validateUserGroupMember(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateUserGroupMember(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "grant", "user-group-member", fmt.Sprintf("%d:%d", v.GroupID, v.UserID))
		write(w, 201, map[string]any{"user_group_member": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetUserGroupMember(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if err := s.requireUserGroupMutationAccess(r.Context(), currentRole(r), current.GroupID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if err := s.requireUserMutationAccess(r.Context(), currentRole(r), current.UserID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if err := s.requireUserGroupMutationAccess(r.Context(), currentRole(r), v.GroupID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if err := s.requireUserMutationAccess(r.Context(), currentRole(r), v.UserID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if protected, err := s.protectedAdminMembership(r.Context(), *current); err != nil {
			fail(w, err, 500)
			return
		} else if protected && (!v.Enabled || v.GroupID != current.GroupID || v.UserID != current.UserID) {
			fail(w, errors.New("初始管理员必须保留在管理员组中"), 400)
			return
		}
		if err := s.validateUserGroupMember(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateUserGroupMember(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"user_group_member": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetUserGroupMember(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if err := s.requireUserGroupMutationAccess(r.Context(), currentRole(r), current.GroupID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if err := s.requireUserMutationAccess(r.Context(), currentRole(r), current.UserID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if protected, err := s.protectedAdminMembership(r.Context(), *current); err != nil {
			fail(w, err, 500)
			return
		} else if protected {
			fail(w, errors.New("初始管理员不能移出管理员组"), 400)
			return
		}
		if err := s.store.Delete(r.Context(), "user_group_members", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "revoke", "user-group-member", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) protectedAdminMembership(ctx context.Context, member model.UserGroupMember) (bool, error) {
	group, err := s.store.GetUserGroup(ctx, member.GroupID)
	if err != nil {
		return false, err
	}
	if group.SystemKey != store.UserGroupSystemAdmins {
		return false, nil
	}
	return s.store.IsBootstrapAdmin(ctx, member.UserID)
}

func uniquePositiveIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Server) trafficRuntimePolicies(ctx context.Context, serverID int64, users []model.User, accountingUsers map[int64]bool, userPolicies map[int64]core.UserLimitPolicy) (map[int64]model.TrafficRuntimePolicy, error) {
	settings := s.runtimeSettings(ctx)
	loc := trafficLocation(settings)
	enforcement := trafficEnforcementMode(settings)
	tz := strings.TrimSpace(settings["traffic_timezone"])
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	policies := map[int64]model.TrafficRuntimePolicy{}
	for _, user := range users {
		if user.ID <= 0 || user.Status != "active" || strings.HasPrefix(user.Username, "__oboard_") {
			continue
		}
		if accountingUsers != nil && !accountingUsers[user.ID] {
			continue
		}
		limit, okLimit := userPolicies[user.ID]
		if !okLimit {
			limit = defaultUserLimitPolicy(user)
		}
		periodKey, start, end, err := s.resolvedTrafficWindow(ctx, user.ID, time.Now(), limit, loc)
		if err != nil {
			return nil, err
		}
		period, err := s.store.EnsureTrafficPeriod(ctx, user.ID, periodKey, start, end, limit.TrafficLimitBytes)
		if err != nil {
			return nil, err
		}
		used := period.Upload + period.Download
		lease, err := s.store.EnsureTrafficLeaseAllocation(ctx, serverID, user.ID, periodKey, limit.TrafficLimitBytes, used)
		if err != nil {
			return nil, err
		}
		policy := model.TrafficRuntimePolicy{UserID: user.ID, Billable: true, SpeedLimitMbps: limit.SpeedLimitMbps, TrafficLimitBytes: limit.TrafficLimitBytes, UsedBaselineBytes: used, LeaseBytes: lease.RemainingBytes, ResetLeaseBytes: lease.ResetBytes, LeaseEnforced: limit.TrafficLimitBytes > 0, PeriodKey: periodKey, PeriodStart: start.UTC().Format(time.RFC3339Nano), PeriodEnd: end.UTC().Format(time.RFC3339Nano), ResetMode: limit.TrafficResetMode, ResetDay: limit.TrafficResetDay, Timezone: tz, QuotaState: period.State, EnforcementMode: enforcement}
		if !limit.TrafficResetAnchor.IsZero() {
			policy.ResetAnchor = limit.TrafficResetAnchor.UTC().Format(time.RFC3339Nano)
		}
		if previous, ok := s.store.PreviousTrafficPeriodKey(ctx, user.ID, periodKey); ok {
			policy.PreviousPeriodKey = previous
		}
		policies[user.ID] = policy
	}
	if revision, err := s.store.TrafficPolicyRevision(ctx); err == nil {
		for userID, policy := range policies {
			policy.PolicyRevision = int64(revision)
			policies[userID] = policy
		}
	}
	return policies, nil
}

func validateUserGroup(v *model.UserGroup) error {
	v.Name = strings.TrimSpace(v.Name)
	v.Description = strings.TrimSpace(v.Description)
	if v.Name == "" {
		return errors.New("name required")
	}
	if v.Role == "" {
		v.Role = model.RoleViewer
	}
	switch v.Role {
	case model.RoleAdmin, model.RoleOperator, model.RoleViewer:
	default:
		return fmt.Errorf("invalid user group role %q", v.Role)
	}
	return nil
}

func mergeUserGroupPatch(v *model.UserGroup, current *model.UserGroup) {
	if v.Name == "" {
		v.Name = current.Name
	}
	if v.Role == "" {
		v.Role = current.Role
	}
}

func normalizeControllerTrafficResetMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "month_day", "day", "custom_day":
		return model.TrafficResetMonthDay
	case model.TrafficResetAnniversaryMonth:
		return model.TrafficResetAnniversaryMonth
	case model.TrafficResetNever:
		return model.TrafficResetNever
	default:
		return model.TrafficResetMonthly
	}
}

func normalizeControllerTrafficResetDay(day int) int {
	if day < 1 {
		return 1
	}
	if day > 31 {
		return 31
	}
	return day
}

// deriveServerTrafficReset implements the server-traffic billing principle:
// when the caller leaves traffic_reset_mode/day unspecified, the reset day is
// derived from the billing anchor. The anchor priority is service_start_at
// (if present) > expires_at > none. Day precision only; renewal_cycle does
// not change the derivation (traffic is always monthly, quarterly still resets
// monthly on the same day). A derived result always uses month_day mode so
// the day is explicit; callers that explicitly set mode/day are never
// overridden.
func deriveServerTrafficReset(explicitMode *string, explicitDay *int, startAt, expiresAt *time.Time, loc *time.Location) (string, int, bool) {
	if explicitMode != nil || explicitDay != nil {
		return "", 0, false
	}
	var anchor *time.Time
	if startAt != nil && !startAt.IsZero() {
		anchor = startAt
	} else if expiresAt != nil && !expiresAt.IsZero() {
		anchor = expiresAt
	}
	if anchor == nil {
		return "", 0, false
	}
	if loc == nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	day := anchor.In(loc).Day()
	if day < 1 {
		day = 1
	}
	if day > 31 {
		day = 31
	}
	return model.TrafficResetMonthDay, normalizeControllerTrafficResetDay(day), true
}

func trafficLocation(settings map[string]string) *time.Location {
	name := strings.TrimSpace(settings["traffic_timezone"])
	if name == "" {
		name = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*3600)
	}
	return loc
}

func trafficEnforcementMode(settings map[string]string) string {
	switch strings.TrimSpace(settings["traffic_enforcement_mode"]) {
	case "reject_new":
		return "reject_new"
	default:
		return "disconnect_and_reject"
	}
}

func trafficWindow(now time.Time, mode string, day int, anchor time.Time, loc *time.Location) (string, time.Time, time.Time) {
	if loc == nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	n := now.In(loc)
	mode = normalizeControllerTrafficResetMode(mode)
	day = normalizeControllerTrafficResetDay(day)
	if mode == model.TrafficResetNever {
		if anchor.IsZero() {
			anchor = n
		}
		start := anchor.In(loc)
		end := time.Date(9999, time.December, 31, 23, 59, 59, 0, loc)
		return start.UTC().Format(time.RFC3339Nano), start, end
	}
	if mode == model.TrafficResetAnniversaryMonth {
		if anchor.IsZero() {
			anchor = n
		}
		start, end := anniversaryTrafficWindow(n, anchor.In(loc), loc)
		return start.UTC().Format(time.RFC3339Nano), start, end
	}
	if mode == model.TrafficResetMonthly {
		start := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
		end := start.AddDate(0, 1, 0)
		return start.Format("2006-01-02"), start, end
	}
	startDay := clampedMonthDay(n.Year(), n.Month(), day)
	start := time.Date(n.Year(), n.Month(), startDay, 0, 0, 0, 0, loc)
	if n.Before(start) {
		prev := start.AddDate(0, -1, 0)
		start = time.Date(prev.Year(), prev.Month(), clampedMonthDay(prev.Year(), prev.Month(), day), 0, 0, 0, 0, loc)
	}
	next := start.AddDate(0, 1, 0)
	end := time.Date(next.Year(), next.Month(), clampedMonthDay(next.Year(), next.Month(), day), 0, 0, 0, 0, loc)
	return start.Format("2006-01-02"), start, end
}

func (s *Server) resolvedTrafficWindow(ctx context.Context, userID int64, at time.Time, limit core.UserLimitPolicy, loc *time.Location) (string, time.Time, time.Time, error) {
	periodKey, start, end := trafficWindow(at, limit.TrafficResetMode, limit.TrafficResetDay, limit.TrafficResetAnchor, loc)
	resolved, changed, err := s.store.ResolveTrafficPeriodKey(ctx, userID, periodKey)
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	if !changed && !strings.Contains(resolved, "#migration-") {
		return periodKey, start, end, nil
	}
	period, err := s.store.GetTrafficPeriod(ctx, userID, resolved)
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	return resolved, period.StartedAt, period.EndsAt, nil
}

func trafficWindowForPeriodKey(now time.Time, periodKey, mode string, day int, anchor time.Time, loc *time.Location) (string, time.Time, time.Time, error) {
	periodKey = strings.TrimSpace(periodKey)
	if periodKey == "" {
		key, start, end := trafficWindow(now, mode, day, anchor, loc)
		return key, start, end, nil
	}
	if loc == nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	mode = normalizeControllerTrafficResetMode(mode)
	if mode == model.TrafficResetAnniversaryMonth || mode == model.TrafficResetNever {
		parsed, err := time.Parse(time.RFC3339Nano, periodKey)
		if err != nil {
			return "", time.Time{}, time.Time{}, errors.New("traffic period_key must use RFC3339Nano for anchored reset cycles")
		}
		if anchor.IsZero() {
			return "", time.Time{}, time.Time{}, errors.New("traffic reset anchor is required")
		}
		if mode == model.TrafficResetNever {
			key, start, end := trafficWindow(parsed, mode, day, anchor, loc)
			if key != periodKey {
				return "", time.Time{}, time.Time{}, errors.New("traffic period_key does not match the user reset anchor")
			}
			return key, start, end, nil
		}
		start, end := anniversaryTrafficWindow(parsed.In(loc).Add(time.Nanosecond), anchor.In(loc), loc)
		if !start.UTC().Equal(parsed.UTC()) {
			return "", time.Time{}, time.Time{}, errors.New("traffic period_key does not match the user reset cycle")
		}
		return periodKey, start, end, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", periodKey, loc)
	if err != nil {
		return "", time.Time{}, time.Time{}, errors.New("traffic period_key must use YYYY-MM-DD")
	}
	key, start, end := trafficWindow(parsed.Add(12*time.Hour), mode, day, time.Time{}, loc)
	if key != periodKey {
		return "", time.Time{}, time.Time{}, errors.New("traffic period_key does not match the user reset cycle")
	}
	return key, start, end, nil
}

func anniversaryTrafficWindow(now, anchor time.Time, loc *time.Location) (time.Time, time.Time) {
	if now.Before(anchor) {
		return anchor, anniversaryBoundary(anchor, 1, loc)
	}
	months := (now.Year()-anchor.Year())*12 + int(now.Month()-anchor.Month())
	start := anniversaryBoundary(anchor, months, loc)
	if now.Before(start) {
		months--
		start = anniversaryBoundary(anchor, months, loc)
	}
	return start, anniversaryBoundary(anchor, months+1, loc)
}

func anniversaryBoundary(anchor time.Time, months int, loc *time.Location) time.Time {
	monthStart := time.Date(anchor.Year(), anchor.Month()+time.Month(months), 1, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), loc)
	day := clampedMonthDay(monthStart.Year(), monthStart.Month(), anchor.Day())
	return time.Date(monthStart.Year(), monthStart.Month(), day, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), loc)
}

func clampedMonthDay(year int, month time.Month, day int) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	if day < 1 {
		return 1
	}
	return day
}

func (s *Server) validateUserGroupMember(ctx context.Context, v model.UserGroupMember) error {
	if v.GroupID == 0 || v.UserID == 0 {
		return errors.New("group_id and user_id required")
	}
	if _, err := s.store.GetUserGroup(ctx, v.GroupID); err != nil {
		return fmt.Errorf("group_id: %w", err)
	}
	if _, err := s.store.GetUser(ctx, v.UserID); err != nil {
		return fmt.Errorf("user_id: %w", err)
	}
	return nil
}

func mergeInboundPatch(current model.Inbound, patch model.Inbound, fields map[string]json.RawMessage) model.Inbound {
	merged := current
	if _, ok := fields["server_id"]; ok {
		merged.ServerID = patch.ServerID
	}
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["protocol"]; ok {
		merged.Protocol = patch.Protocol
	}
	if _, ok := fields["listen_ip"]; ok {
		merged.ListenIP = patch.ListenIP
	}
	if _, ok := fields["port"]; ok {
		merged.Port = patch.Port
	}
	if _, ok := fields["advertise_port"]; ok {
		merged.AdvertisePort = patch.AdvertisePort
	}
	if _, ok := fields["entry_ip_mode"]; ok {
		merged.EntryIPMode = patch.EntryIPMode
	}
	if _, ok := fields["external_ip"]; ok {
		merged.ExternalIP = patch.ExternalIP
	}
	if _, ok := fields["dns_sync_enabled"]; ok {
		merged.DNSSyncEnabled = patch.DNSSyncEnabled
	}
	if _, ok := fields["dns_credential_id"]; ok {
		merged.DNSCredentialID = patch.DNSCredentialID
	}
	if _, ok := fields["dns_domain"]; ok {
		merged.DNSDomain = patch.DNSDomain
	}
	if _, ok := fields["dns_proxy_enabled"]; ok {
		merged.DNSProxyEnabled = patch.DNSProxyEnabled
	}
	if _, ok := fields["dns_record_types"]; ok {
		merged.DNSRecordTypes = patch.DNSRecordTypes
	}
	if _, ok := fields["ddns_enabled"]; ok {
		merged.DDNSEnabled = patch.DDNSEnabled
	}
	if _, ok := fields["ddns_interval_seconds"]; ok {
		merged.DDNSInterval = patch.DDNSInterval
	}
	if _, ok := fields["dns_sync_status"]; ok {
		merged.DNSSyncStatus = patch.DNSSyncStatus
	}
	if _, ok := fields["dns_sync_error"]; ok {
		merged.DNSSyncError = patch.DNSSyncError
	}
	if _, ok := fields["dns_last_synced_at"]; ok {
		merged.DNSLastSyncedAt = patch.DNSLastSyncedAt
	}
	if _, ok := fields["tls"]; ok {
		merged.TLS = patch.TLS
	}
	if _, ok := fields["certificate_mode"]; ok {
		merged.CertificateMode = patch.CertificateMode
	}
	if _, ok := fields["certificate_id"]; ok {
		merged.CertificateID = patch.CertificateID
	}
	if _, ok := fields["certificate_domain"]; ok {
		merged.CertificateDomain = patch.CertificateDomain
	}
	if _, ok := fields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := fields["kind"]; ok {
		merged.Kind = patch.Kind
	}
	if _, ok := fields["reality"]; ok {
		merged.Reality = patch.Reality
	}
	if _, ok := fields["rotate_reality_key"]; ok {
		merged.RotateRealityKey = patch.RotateRealityKey
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	return merged
}

func normalizeInbound(v model.Inbound) model.Inbound {
	if strings.TrimSpace(string(v.EntryIPMode)) == "" {
		v.EntryIPMode = model.EntryIPModeAuto
	}
	v.DNSRecordTypes = strings.ToLower(strings.TrimSpace(v.DNSRecordTypes))
	if v.DNSRecordTypes == "" {
		v.DNSRecordTypes = "auto"
	}
	v.DNSDomain = normalizeDomainName(v.DNSDomain)
	v.CertificateMode = strings.ToLower(strings.TrimSpace(v.CertificateMode))
	if v.CertificateMode == "" {
		if v.TLS && !inboundConfigHasCertificatePaths(v.ConfigJSON) {
			v.CertificateMode = model.CertificateModeAuto
		} else {
			v.CertificateMode = model.CertificateModeExternal
		}
	}
	v.CertificateDomain = normalizeDomainName(v.CertificateDomain)
	if v.CertificateMode != model.CertificateModeExternal && v.CertificateDomain == "" {
		v.CertificateDomain = v.DNSDomain
	}
	if v.DDNSInterval == 0 {
		v.DDNSInterval = 300
	}
	if strings.TrimSpace(v.ListenIP) == "" {
		v.ListenIP = "0.0.0.0"
	}
	if v.AdvertisePort < 0 || v.AdvertisePort > 65535 {
		v.AdvertisePort = 0
	}
	return v
}

func normalizeMieruInboundPorts(v *model.Inbound) error {
	if v == nil || v.Protocol != model.ProtocolMieru {
		return nil
	}
	port, configJSON, err := core.NormalizeMieruPortConfig(v.Port, v.ConfigJSON, "listen_ports")
	if err != nil {
		return err
	}
	v.Port = port
	v.ConfigJSON = configJSON
	return nil
}

func inboundConfigHasCertificatePaths(configJSON string) bool {
	var config map[string]any
	if json.Unmarshal([]byte(configJSON), &config) != nil {
		return false
	}
	tls, _ := config["tls"].(map[string]any)
	certificatePath, _ := tls["certificate_path"].(string)
	keyPath, _ := tls["key_path"].(string)
	return strings.TrimSpace(certificatePath) != "" && strings.TrimSpace(keyPath) != ""
}

func (s *Server) resolveInboundTemplates(ctx context.Context, v *model.Inbound) error {
	if err := s.resolveSnellProfileIntoInbound(ctx, v); err != nil {
		return err
	}
	return s.resolveNodePresetIntoInbound(ctx, v)
}

// resolveNodePresetIntoInbound merges a referenced node preset's template
// into inbound config_json. Inbound values win over preset values. Secret
// fields are never copied from the preset. The node_preset_id reference is
// retained for usage counting.
func (s *Server) resolveNodePresetIntoInbound(ctx context.Context, v *model.Inbound) error {
	if v == nil || v.Protocol == model.ProtocolSnell || v.Protocol == model.ProtocolSSH {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(v.ConfigJSON), &cfg); err != nil {
		return err
	}
	presetID := configInt64(cfg, "node_preset_id")
	if presetID <= 0 {
		return nil
	}
	preset, err := s.store.GetNodePreset(ctx, presetID)
	if err != nil {
		return fmt.Errorf("node preset %d not found", presetID)
	}
	if !preset.Enabled {
		return fmt.Errorf("node preset %d is disabled", preset.ID)
	}
	if string(v.Protocol) != preset.Protocol {
		return fmt.Errorf("node preset %d belongs to protocol %s", preset.ID, preset.Protocol)
	}
	var template map[string]any
	if err := json.Unmarshal([]byte(preset.ConfigJSON), &template); err != nil || template == nil {
		return errors.New("node preset config_json must be a JSON object")
	}
	merged := mergeInboundPresetConfig(template, cfg)
	merged["node_preset_id"] = preset.ID
	encoded, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	v.ConfigJSON = string(encoded)
	return nil
}

var inboundPresetSecretKeys = map[string]bool{
	"password": true, "psk": true, "uuid": true,
	"private_key": true, "public_key": true, "short_id": true,
}

// inboundPresetMetadataKeys are preset-only template fields (such as the
// Reality domain template) that never merge into an inbound config_json.
var inboundPresetMetadataKeys = map[string]bool{
	"reality_domains": true,
}

// inboundPresetInboundOwnedKeys stay on the inbound. Node presets must not
// supply or overwrite them; HY2 bandwidth is per-entry, not a template field.
var inboundPresetInboundOwnedKeys = map[string]bool{
	"up_mbps": true, "down_mbps": true,
}

func mergeInboundPresetConfig(preset, inbound map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range preset {
		if inboundPresetSecretKeys[key] || inboundPresetMetadataKeys[key] || inboundPresetInboundOwnedKeys[key] {
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			out[key] = mergeInboundPresetConfig(nested, nil)
			continue
		}
		out[key] = cloneJSONAny(value)
	}
	for key, value := range inbound {
		if inboundPresetSecretKeys[key] || inboundPresetMetadataKeys[key] {
			if !isEmptyJSONValue(value) && !inboundPresetMetadataKeys[key] {
				out[key] = cloneJSONAny(value)
			}
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			if current, ok := out[key].(map[string]any); ok {
				out[key] = mergeInboundPresetConfig(current, nested)
				continue
			}
		}
		if !isEmptyJSONValue(value) {
			out[key] = cloneJSONAny(value)
		}
	}
	return out
}

func cloneJSONAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range typed {
			out[key] = cloneJSONAny(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneJSONAny(item)
		}
		return out
	default:
		return typed
	}
}

func isEmptyJSONValue(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func configInt64(cfg map[string]any, key string) int64 {
	switch value := cfg[key].(type) {
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	case int:
		return int64(value)
	case int64:
		return value
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n
	default:
		return 0
	}
}

// resolveSnellProfileIntoInbound merges a referenced Snell profile's
// parameters into the inbound config_json so core config generation sees the
// final parameter set. Inbound-level explicit fields win over profile fields.
// The `snell_profile_id` reference is retained for audit and usage counting.
// Snell server PSK is now a stable per-inbound credential: if the inbound
// (and the referenced profile when present) provide no PSK, a random PSK is
// generated and persisted so adding or removing users never rotates existing
// clients.
func (s *Server) resolveSnellProfileIntoInbound(ctx context.Context, v *model.Inbound) error {
	if v == nil || v.Protocol != model.ProtocolSnell {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(v.ConfigJSON), &cfg); err != nil {
		return err
	}
	profileID, _ := cfg["snell_profile_id"].(float64)
	var profile *model.SnellProfile
	if profileID > 0 {
		p, err := s.store.GetSnellProfile(ctx, int64(profileID))
		if err != nil {
			return fmt.Errorf("snell profile %d not found", int64(profileID))
		}
		if !p.Enabled {
			return fmt.Errorf("snell profile %d is disabled", p.ID)
		}
		profile = p
		if _, exists := cfg["version"]; !exists || cfg["version"] == nil {
			cfg["version"] = profile.Version
		}
		if psk, _ := cfg["psk"].(string); psk == "" {
			cfg["psk"] = profile.PSK
		}
		if obfs, _ := cfg["obfs_mode"].(string); obfs == "" {
			cfg["obfs_mode"] = profile.ObfsMode
		}
		if host, _ := cfg["obfs_host"].(string); host == "" {
			cfg["obfs_host"] = profile.ObfsHost
		}
		if mode, _ := cfg["mode"].(string); mode == "" {
			cfg["mode"] = profile.Mode
		}
		if _, exists := cfg["reuse"]; !exists || cfg["reuse"] == nil {
			cfg["reuse"] = profile.Reuse
		}
		if _, exists := cfg["tcp_fast_open"]; !exists || cfg["tcp_fast_open"] == nil {
			cfg["tcp_fast_open"] = profile.TCPFastOpen
		}
	}
	if psk, _ := cfg["psk"].(string); strings.TrimSpace(psk) == "" {
		secret, err := security.RandomToken(24)
		if err != nil {
			return err
		}
		cfg["psk"] = secret
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	v.ConfigJSON = string(encoded)
	return nil
}

func validateInbound(v model.Inbound) error {
	if v.ServerID == 0 {
		return errors.New("server_id required")
	}
	if v.Name == "" {
		return errors.New("name required")
	}
	if v.AdvertisePort != 0 {
		if err := core.ValidatePort(v.AdvertisePort); err != nil {
			return fmt.Errorf("advertise_port: %w", err)
		}
		if v.AdvertisePort == v.Port && v.Protocol != model.ProtocolSnell {
			return errors.New("advertise_port must differ from listen port; disable NAT mapping to use the same port")
		}
		if v.Protocol == model.ProtocolMieru {
			if ports, err := core.MieruInboundPorts(v); err == nil && len(ports) > 1 {
				return errors.New("NAT 端口映射暂不支持多端口 Mieru 入口，请先移除额外 listen_ports")
			}
		}
		// Snell may use one advertised NAT/forwarding port when the
		// deployment resolves to exactly one generated listener. The full
		// projection enforces that cardinality because it owns the effective
		// user, device and proxy-path identities.
	}
	if v.EntryIPMode == "" {
		v.EntryIPMode = model.EntryIPModeAuto
	}
	switch v.EntryIPMode {
	case model.EntryIPModeAuto, model.EntryIPModeIPv4, model.EntryIPModeIPv6, model.EntryIPModeCustom:
	default:
		return fmt.Errorf("invalid entry_ip_mode %q", v.EntryIPMode)
	}
	if v.EntryIPMode == model.EntryIPModeCustom && strings.TrimSpace(v.ExternalIP) == "" {
		return errors.New("custom entry address required")
	}
	recordTypes := strings.ToLower(strings.TrimSpace(v.DNSRecordTypes))
	if recordTypes == "" {
		recordTypes = "auto"
	}
	switch recordTypes {
	case "auto", "a", "aaaa", "both":
	default:
		return fmt.Errorf("invalid dns_record_types %q", v.DNSRecordTypes)
	}
	if v.DNSSyncEnabled {
		if !isDNSDomainName(v.DNSDomain) {
			return errors.New("启用 DNS 自动解析时需要填写有效的解析域名")
		}
		if v.DNSCredentialID == nil || *v.DNSCredentialID <= 0 {
			return errors.New("启用 DNS 自动解析时需要选择 DNS 凭据")
		}
		if v.DDNSEnabled && (v.DDNSInterval < 300 || v.DDNSInterval > 86400) {
			return errors.New("启用 DDNS 时更新间隔必须为 300 到 86400 秒")
		}
		if v.DDNSEnabled && v.EntryIPMode == model.EntryIPModeCustom {
			return errors.New("自定义入口地址使用固定解析目标，不需要开启 DDNS")
		}
	}
	if inferredInboundKind(v) == "vless-reality" {
		if v.TLS {
			return &core.ConfigFieldError{Path: "tls", Problem: "must be false for Reality because Reality provides its own TLS"}
		}
		if v.CertificateMode != model.CertificateModeExternal {
			return &core.ConfigFieldError{Path: "certificate_mode", Problem: "must be external for Reality"}
		}
	}
	switch v.CertificateMode {
	case model.CertificateModeExternal:
	case model.CertificateModeAuto, model.CertificateModeExact, model.CertificateModeWildcard:
		if !isDNSDomainName(v.CertificateDomain) {
			return errors.New("托管证书需要有效的 SNI 域名；该域名不必解析到本机，客户端可以直接连接 IP")
		}
	case model.CertificateModeExplicit:
		if !isDNSDomainName(v.CertificateDomain) {
			return errors.New("指定证书需要有效的 SNI 域名；该域名不必解析到本机，客户端可以直接连接 IP")
		}
		if v.CertificateID == nil || *v.CertificateID <= 0 {
			return errors.New("指定证书模式需要选择证书")
		}
	default:
		return fmt.Errorf("invalid certificate_mode %q", v.CertificateMode)
	}
	if err := validJSONObject(v.ConfigJSON); err != nil {
		return err
	}
	if v.Protocol == model.ProtocolSSH {
		return validateSSHInbound(v)
	}
	if err := core.ValidatePersistedInboundConfigJSON(v.Protocol, v.ConfigJSON); err != nil {
		return err
	}
	a, err := core.AdapterFor(v.Protocol)
	if err != nil {
		return err
	}
	return a.ValidateInbound(v)
}

const sshInboundConfirmationVersion = "ssh-inbound-v1"

// validateSSHInbound deliberately requires an explicit confirmation in the
// persisted configuration as well as the UI confirmation. This means a raw
// API request cannot accidentally expose an SSH listener by merely selecting
// the protocol.
func validateSSHInbound(v model.Inbound) error {
	if v.DNSProxyEnabled {
		return errors.New("SSH 入口不支持 DNS 代理，请使用 DNS only")
	}
	if err := core.ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if err := core.ValidatePort(v.Port); err != nil {
		return err
	}
	var cfg struct {
		ExposureConfirmed           bool   `json:"exposure_confirmed"`
		ExposureConfirmationVersion string `json:"exposure_confirmation_version"`
		AccessMode                  string `json:"access_mode"`
	}
	if err := json.Unmarshal([]byte(v.ConfigJSON), &cfg); err != nil {
		return err
	}
	if !cfg.ExposureConfirmed || cfg.ExposureConfirmationVersion != sshInboundConfirmationVersion {
		return errors.New("创建 SSH 入口前必须确认 SSH 暴露风险")
	}
	if cfg.AccessMode != "" && cfg.AccessMode != "restricted_proxy" {
		return errors.New("SSH 入口仅支持受限代理模式")
	}
	return nil
}

func applyInboundConfigDefaults(protocol model.Protocol, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if protocol == model.ProtocolSS {
		method := stringFromMap(cfg, "method")
		if method == "" {
			method = "2022-blake3-aes-128-gcm"
			cfg["method"] = method
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "2022-") && stringFromMap(cfg, "password") == "" {
			secret, err := randomSS2022Key(method)
			if err != nil {
				return "", err
			}
			cfg["password"] = secret
		}
	}
	if protocol == model.ProtocolHY2 {
		applyHY2BandwidthDefaults(cfg)
		if hy2ObfsType(cfg) == "salamander" {
			if err := applyHY2SalamanderObfs(cfg); err != nil {
				return "", err
			}
		}
	}
	if protocol == model.ProtocolVLESS {
		if err := applyVLESSRealityDefaults(cfg); err != nil {
			return "", err
		}
	}
	if protocol == model.ProtocolMieru {
		if stringFromMap(cfg, "transport") == "" {
			cfg["transport"] = "TCP"
		}
		if _, exists := cfg["user_hint_is_mandatory"]; !exists {
			cfg["user_hint_is_mandatory"] = true
		}
		if stringFromMap(cfg, "multiplexing") == "" {
			cfg["multiplexing"] = "MULTIPLEXING_DEFAULT"
		}
	}
	if protocol == model.ProtocolSnell {
		if _, exists := cfg["version"]; !exists {
			cfg["version"] = float64(core.SnellVersionV4)
		}
		if psk := strings.TrimSpace(fmt.Sprint(cfg["psk"])); psk == "" {
			secret, err := security.RandomToken(18)
			if err != nil {
				return "", err
			}
			cfg["psk"] = secret
		}
		if stringFromMap(cfg, "obfs_mode") == "" {
			cfg["obfs_mode"] = "none"
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func applyProtocolAuthDefaults(protocol model.Protocol, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	meta := oboardMetadata(cfg)
	if stringFromMap(meta, "username") == "" {
		meta["username"] = randomNodeUsername()
	}
	meta["auth_auto"] = true
	switch protocol {
	case model.ProtocolVLESS:
		if stringFromMap(cfg, "uuid") == "" {
			uuid, err := randomUUID()
			if err != nil {
				return "", err
			}
			cfg["uuid"] = uuid
		}
	case model.ProtocolHY2, model.ProtocolAnyTLS:
		if stringFromMap(cfg, "password") == "" {
			secret, err := security.RandomToken(18)
			if err != nil {
				return "", err
			}
			cfg["password"] = secret
		}
	case model.ProtocolSS:
		if stringFromMap(cfg, "method") == "" {
			cfg["method"] = "2022-blake3-aes-128-gcm"
		}
		if stringFromMap(cfg, "password") == "" {
			secret, err := randomSS2022Key(stringFromMap(cfg, "method"))
			if err != nil {
				return "", err
			}
			cfg["password"] = secret
		}
	case model.ProtocolMieru:
		if stringFromMap(cfg, "transport") == "" {
			cfg["transport"] = "TCP"
		}
		if stringFromMap(cfg, "multiplexing") == "" {
			cfg["multiplexing"] = "MULTIPLEXING_DEFAULT"
		}
		if stringFromMap(cfg, "username") == "" {
			cfg["username"] = randomNodeUsername()
		}
		if stringFromMap(cfg, "password") == "" {
			secret, err := security.RandomToken(18)
			if err != nil {
				return "", err
			}
			cfg["password"] = secret
		}
	case model.ProtocolSocks:
		if stringFromMap(cfg, "username") == "" {
			cfg["username"] = randomNodeUsername()
		}
		if stringFromMap(cfg, "password") == "" {
			secret, err := security.RandomToken(18)
			if err != nil {
				return "", err
			}
			cfg["password"] = secret
		}
		cfg["version"] = "5"
	case model.ProtocolSnell:
		if stringFromMap(cfg, "psk") == "" {
			secret, err := security.RandomToken(18)
			if err != nil {
				return "", err
			}
			cfg["psk"] = secret
		}
		if stringFromMap(cfg, "obfs_mode") == "" {
			cfg["obfs_mode"] = "none"
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func randomSS2022Key(method string) (string, error) {
	keyLen := 16
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		keyLen = 32
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func oboardMetadata(cfg map[string]any) map[string]any {
	if raw, ok := cfg["_oboard"].(map[string]any); ok {
		return raw
	}
	meta := map[string]any{}
	cfg["_oboard"] = meta
	return meta
}

func stringFromMap(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func randomNodeUsername() string {
	if token, err := security.RandomToken(6); err == nil {
		return "node-" + token
	}
	return "node-auto"
}

func randomUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (s *Server) outbounds(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/outbounds/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetOutbound(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"outbound": item})
			return
		}
		items, err := s.store.ListOutbounds(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"outbounds": items})
	case http.MethodPost:
		var v model.Outbound
		if !decode(w, r, &v) {
			return
		}
		if v.ConfigJSON == "" {
			v.ConfigJSON = "{}"
		}
		normalized, err := applyProtocolAuthDefaults(v.Protocol, v.ConfigJSON)
		if err != nil {
			fail(w, err, 400)
			return
		}
		v.ConfigJSON = normalized
		if err := normalizeMieruOutboundPorts(v.Protocol, &v.TargetPort, &v.ConfigJSON); err != nil {
			fail(w, err, 400)
			return
		}
		if err := validateOutbound(v); err != nil {
			fail(w, err, 400)
			return
		}
		ids := []int64{v.ServerID}
		if v.NextServerID != nil {
			ids = append(ids, *v.NextServerID)
		}
		if err := s.store.ValidateServerExists(r.Context(), ids...); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateOutboundAddress(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateOutbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 201, map[string]any{"outbound": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		var v model.Outbound
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if v.ConfigJSON == "" {
			v.ConfigJSON = "{}"
		}
		normalized, err := applyProtocolAuthDefaults(v.Protocol, v.ConfigJSON)
		if err != nil {
			fail(w, err, 400)
			return
		}
		v.ConfigJSON = normalized
		if err := normalizeMieruOutboundPorts(v.Protocol, &v.TargetPort, &v.ConfigJSON); err != nil {
			fail(w, err, 400)
			return
		}
		if err := validateOutbound(v); err != nil {
			fail(w, err, 400)
			return
		}
		ids := []int64{v.ServerID}
		if v.NextServerID != nil {
			ids = append(ids, *v.NextServerID)
		}
		if err := s.store.ValidateServerExists(r.Context(), ids...); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateOutboundAddress(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateOutbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"outbound": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		err := s.store.Delete(r.Context(), "outbounds", id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}
func validateOutbound(v model.Outbound) error {
	if v.ServerID == 0 {
		return errors.New("server_id required")
	}
	if v.Name == "" {
		return errors.New("name required")
	}
	if err := validJSONObject(v.ConfigJSON); err != nil {
		return err
	}
	a, err := core.AdapterFor(v.Protocol)
	if err != nil {
		return err
	}
	return a.ValidateOutbound(v)
}

func normalizeMieruOutboundPorts(protocol model.Protocol, port *int, configJSON *string) error {
	if protocol != model.ProtocolMieru || port == nil || configJSON == nil {
		return nil
	}
	primary, normalized, err := core.NormalizeMieruPortConfig(*port, *configJSON, "server_ports")
	if err != nil {
		return err
	}
	*port = primary
	*configJSON = normalized
	return nil
}

func (s *Server) validateOutboundAddress(ctx context.Context, v model.Outbound) error {
	server, err := s.store.GetServer(ctx, v.ServerID)
	if err != nil {
		return err
	}
	return core.ValidateAddressForIPStack(core.EffectiveIPStack(*server), v.TargetAddress)
}

func (s *Server) routingRules(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasSuffix(path, "/place") || strings.HasSuffix(path, "/reorder") {
		s.placeRoutingRules(w, r)
		return
	}
	if strings.HasSuffix(path, "/batch-delete") {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		var request struct {
			IDs []int64 `json:"ids"`
		}
		if !decode(w, r, &request) {
			return
		}
		if len(request.IDs) == 0 || len(request.IDs) > 256 {
			fail(w, errors.New("ids must contain 1 to 256 items"), http.StatusBadRequest)
			return
		}
		previous := make([]model.RoutingRule, 0, len(request.IDs))
		for _, id := range request.IDs {
			item, err := s.store.GetRoutingRule(r.Context(), id)
			if err == nil {
				previous = append(previous, *item)
			}
		}
		if err := s.store.DeleteRoutingRules(r.Context(), request.IDs); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusConflict
			}
			fail(w, err, status)
			return
		}
		s.syncFamilySplitTemplatesForRules(r.Context(), previous...)
		write(w, http.StatusOK, map[string]any{"deleted_ids": request.IDs})
		return
	}
	id := idFromPath(r.URL.Path, "/api/v1/routing-rules/")
	switch r.Method {
	case http.MethodGet:
		if id > 0 {
			item, err := s.store.GetRoutingRule(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"routing_rule": item})
			return
		}
		items, err := s.store.ListRoutingRules(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"routing_rules": items})
	case http.MethodPost:
		var v model.RoutingRule
		if !decode(w, r, &v) {
			return
		}
		syncSourceID, err := s.prepareRoutingRuleReuse(r.Context(), &v)
		if err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if syncSourceID != 0 {
			groupID, err := security.RandomToken(18)
			if err == nil {
				err = s.store.CreateSyncedRoutingRule(r.Context(), &v, syncSourceID, groupID)
			}
			if err != nil {
				fail(w, err, 500)
				return
			}
		} else if err := s.store.CreateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		s.syncFamilySplitTemplatesForRules(r.Context(), v)
		auditReq(s, r, "create", "routing_rule", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"routing_rule": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		var v model.RoutingRule
		if !decode(w, r, &v) {
			return
		}
		current, err := s.store.GetRoutingRule(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v.ID = id
		v.SyncGroupID = current.SyncGroupID
		v.SyncSourceRuleID = nil
		v.SyncEnabled = false
		if err := s.validateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		s.syncFamilySplitTemplatesForRules(r.Context(), *current, v)
		write(w, 200, map[string]any{"routing_rule": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetRoutingRule(r.Context(), id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fail(w, err, 500)
			return
		}
		if err := s.store.Delete(r.Context(), "routing_rules", id); err != nil {
			fail(w, err, 500)
			return
		}
		if current != nil {
			s.syncFamilySplitTemplatesForRules(r.Context(), *current)
		}
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) prepareRoutingRuleReuse(ctx context.Context, v *model.RoutingRule) (int64, error) {
	if v.SyncSourceRuleID == nil || *v.SyncSourceRuleID <= 0 {
		if v.SyncEnabled {
			return 0, errors.New("sync_source_rule_id required when sync_enabled is true")
		}
		return 0, nil
	}
	source, err := s.store.GetRoutingRule(ctx, *v.SyncSourceRuleID)
	if err != nil {
		return 0, fmt.Errorf("sync source routing rule: %w", err)
	}
	if source.Scope != model.RoutingRuleScopePathStage {
		return 0, errors.New("only path-stage routing rules can be reused")
	}
	v.Name = source.Name
	v.MatchSource = source.MatchSource
	v.RuleSetID = source.RuleSetID
	v.DNSResolver = source.DNSResolver
	v.MatchJSON = source.MatchJSON
	if !v.SyncEnabled {
		return 0, nil
	}
	return source.ID, nil
}

func (s *Server) validateRoutingRule(ctx context.Context, v *model.RoutingRule) error {
	return s.validateRoutingRuleWithCandidatePath(ctx, v, nil)
}

func (s *Server) validateRoutingRuleWithCandidatePath(ctx context.Context, v *model.RoutingRule, candidatePath *model.ProxyPath) error {
	if v.Scope == "" {
		v.Scope = model.RoutingRuleScopeServer
	}
	if v.MatchSource == "" {
		v.MatchSource = model.RoutingMatchSourceInline
	}
	if v.Scope == model.RoutingRuleScopePathStage {
		if v.ProxyPathID == nil || *v.ProxyPathID <= 0 {
			return errors.New("proxy_path_id required for path_stage rule")
		}
		var path *model.ProxyPath
		var err error
		if candidatePath != nil && candidatePath.ID == *v.ProxyPathID {
			copy := *candidatePath
			path = &copy
		} else {
			path, err = s.store.GetProxyPath(ctx, *v.ProxyPathID)
			if err != nil {
				return fmt.Errorf("proxy_path %d: %w", *v.ProxyPathID, err)
			}
		}
		if v.StageStepID == nil {
			inbound, err := s.store.GetInbound(ctx, path.InboundID)
			if err != nil {
				return fmt.Errorf("proxy path root inbound: %w", err)
			}
			v.ServerID = inbound.ServerID
		} else {
			step, err := s.store.GetProxyPathStep(ctx, *v.StageStepID)
			if err != nil {
				return fmt.Errorf("proxy_path_step %d: %w", *v.StageStepID, err)
			}
			if step.PathID != path.ID || step.NodeType != model.ProxyPathStepServerInbound || step.ServerID == nil {
				return errors.New("stage_step_id must identify a controlled server node in the selected proxy path")
			}
			v.ServerID = *step.ServerID
		}
		if v.SortPosition < 0 {
			return errors.New("sort_position cannot be negative")
		}
	} else if v.Scope == model.RoutingRuleScopeServer {
		v.ProxyPathID = nil
		v.StageStepID = nil
		v.TargetProxyPathID = nil
		v.FamilySplitTemplateID = nil
		v.FamilyDNSStrategy = ""
		v.RuleSetID = nil
		v.MatchSource = model.RoutingMatchSourceInline
		if v.ServerID == 0 {
			return errors.New("server_id required")
		}
	} else {
		return fmt.Errorf("unsupported routing rule scope %q", v.Scope)
	}
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("name required")
	}
	v.DNSResolver = strings.TrimSpace(v.DNSResolver)
	if err := core.ValidateRoutingRuleDNSResolver(v.DNSResolver); err != nil {
		return err
	}
	if v.Priority == 0 {
		v.Priority = 100
	}
	if v.MatchSource == model.RoutingMatchSourceRuleSet {
		if v.RuleSetID == nil {
			return errors.New("rule_set_id required for remote rule-set match")
		}
		set, err := s.store.GetRoutingRuleSet(ctx, *v.RuleSetID)
		if err != nil {
			return fmt.Errorf("routing_rule_set %d: %w", *v.RuleSetID, err)
		}
		if set.Revision == "" {
			return errors.New("routing rule set has no successful snapshot")
		}
		v.MatchJSON = "{}"
	} else if v.MatchSource != model.RoutingMatchSourceInline {
		return fmt.Errorf("unsupported match_source %q", v.MatchSource)
	}
	if strings.TrimSpace(v.MatchJSON) == "" {
		v.MatchJSON = "{}"
	}
	if len(v.MatchJSON) > 8192 {
		return errors.New("match_json is too large")
	}
	if err := validJSONObject(v.MatchJSON); err != nil {
		return fmt.Errorf("match_json: %w", err)
	}
	if err := core.ValidateRoutingMatchJSON(v.MatchJSON); err != nil {
		return fmt.Errorf("match_json: %w", err)
	}
	server, err := s.store.GetServer(ctx, v.ServerID)
	if err != nil {
		return fmt.Errorf("server %d: %w", v.ServerID, err)
	}
	if v.Action != model.RouteActionFamilySplit {
		v.FamilySplitTemplateID = nil
		v.FamilyDNSStrategy = ""
	}
	switch v.Action {
	case model.RouteActionDirect, model.RouteActionBlock:
		return nil
	case model.RouteActionOutbound:
		if v.OutboundID == nil {
			return errors.New("outbound_id required")
		}
		out, err := s.store.GetOutbound(ctx, *v.OutboundID)
		if err != nil {
			return fmt.Errorf("outbound %d: %w", *v.OutboundID, err)
		}
		if out.ServerID != v.ServerID {
			return errors.New("outbound must belong to the same server as the routing rule")
		}
		return core.ValidateAddressForIPStack(core.EffectiveIPStack(*server), out.TargetAddress)
	case model.RouteActionExternal:
		if v.ExternalOutboundID == nil {
			return errors.New("external_outbound_id required")
		}
		ext, err := s.store.GetExternalOutbound(ctx, *v.ExternalOutboundID)
		if err != nil {
			return fmt.Errorf("external_outbound %d: %w", *v.ExternalOutboundID, err)
		}
		if ext.Scope == model.ExternalOutboundScopeServer && (ext.ServerID == nil || *ext.ServerID != v.ServerID) {
			return errors.New("server-scoped external outbound must belong to the same server")
		}
		return core.ValidateAddressForIPStack(core.EffectiveIPStack(*server), ext.TargetAddress)
	case model.RouteActionProxyPath:
		if candidatePath != nil {
			return errors.New("atomic root routing rule cannot target another proxy path")
		}
		if v.Scope != model.RoutingRuleScopePathStage || v.ProxyPathID == nil {
			return errors.New("proxy_path action requires a path_stage routing rule")
		}
		if v.TargetProxyPathID == nil || *v.TargetProxyPathID <= 0 {
			return errors.New("target_proxy_path_id required")
		}
		_, err := normalizeRoutingRuleProxyPathBinding(v, *server)
		if err != nil {
			return err
		}
		return s.validateRoutingRuleTargetPath(ctx, *v.ProxyPathID, v.StageStepID, *v.TargetProxyPathID, v.ID)
	case model.RouteActionFamilySplit:
		if candidatePath != nil {
			return errors.New("atomic root routing rule cannot create a family split")
		}
		if v.Scope != model.RoutingRuleScopePathStage || v.ProxyPathID == nil {
			return errors.New("family_split action requires a path_stage routing rule")
		}
		if v.FamilySplitTemplateID == nil || *v.FamilySplitTemplateID <= 0 {
			return errors.New("family_split_template_id required")
		}
		if v.FamilyDNSStrategy == "" {
			v.FamilyDNSStrategy = model.FamilyDNSStrategyAuto
		}
		switch v.FamilyDNSStrategy {
		case model.FamilyDNSStrategyAuto, model.FamilyDNSStrategyPreferIPv4, model.FamilyDNSStrategyPreferIPv6:
		default:
			return fmt.Errorf("unsupported family_dns_strategy %q", v.FamilyDNSStrategy)
		}
		v.TargetProxyPathID = nil
		v.InterfaceName = ""
		v.SourcePrefix = ""
		v.OutboundTag = ""
		if !v.Enabled {
			return nil
		}
		return s.validateFamilyBranchGraft(ctx, *v.ProxyPathID, v.StageStepID, *v.FamilySplitTemplateID, v.ID)
	case model.RouteActionInterface:
		v.InterfaceName = strings.TrimSpace(v.InterfaceName)
		if v.InterfaceName == "" {
			return errors.New("interface_name required")
		}
		if err := core.ValidateNetworkInterfaceName(v.InterfaceName); err != nil {
			return fmt.Errorf("interface_name: %w", err)
		}
		v.OutboundTag = v.InterfaceName
		return nil
	case model.RouteActionSourcePrefix:
		prefix, err := netip.ParsePrefix(strings.TrimSpace(v.SourcePrefix))
		if err != nil {
			return fmt.Errorf("source_prefix must be a valid IPv4 or IPv6 CIDR: %w", err)
		}
		prefix = prefix.Masked()
		if server.IPStack == model.IPStackIPv4Only && prefix.Addr().Is6() {
			return errors.New("IPv6 source_prefix is incompatible with ipv4_only")
		}
		if server.IPStack == model.IPStackIPv6Only && prefix.Addr().Is4() {
			return errors.New("IPv4 source_prefix is incompatible with ipv6_only")
		}
		v.SourcePrefix = prefix.String()
		v.OutboundTag = v.SourcePrefix
		return nil
	default:
		return fmt.Errorf("unsupported action %q", v.Action)
	}
}

func normalizeRoutingRuleProxyPathBinding(v *model.RoutingRule, server model.Server) (bool, error) {
	v.InterfaceName = strings.TrimSpace(v.InterfaceName)
	v.SourcePrefix = strings.TrimSpace(v.SourcePrefix)
	if v.InterfaceName != "" && v.SourcePrefix != "" {
		return false, errors.New("proxy_path action cannot bind both interface_name and source_prefix")
	}
	if v.InterfaceName != "" {
		if err := core.ValidateNetworkInterfaceName(v.InterfaceName); err != nil {
			return false, fmt.Errorf("interface_name: %w", err)
		}
		v.OutboundTag = v.InterfaceName
		return true, nil
	}
	if v.SourcePrefix == "" {
		v.OutboundTag = ""
		return false, nil
	}
	prefix, err := netip.ParsePrefix(v.SourcePrefix)
	if err != nil {
		return false, fmt.Errorf("source_prefix must be a valid IPv4 or IPv6 CIDR: %w", err)
	}
	prefix = prefix.Masked()
	if server.IPStack == model.IPStackIPv4Only && prefix.Addr().Is6() {
		return false, errors.New("IPv6 source_prefix is incompatible with ipv4_only")
	}
	if server.IPStack == model.IPStackIPv6Only && prefix.Addr().Is4() {
		return false, errors.New("IPv4 source_prefix is incompatible with ipv6_only")
	}
	v.SourcePrefix = prefix.String()
	v.OutboundTag = v.SourcePrefix
	return true, nil
}

func (s *Server) validateRoutingRuleFamilySplit(ctx context.Context, sourcePathID int64, sourceStageStepID *int64, ipv4TargetPathID, ipv6TargetPathID, ruleID int64) error {
	sourceServer, err := s.routingRuleStageServer(ctx, sourcePathID, sourceStageStepID)
	if err != nil {
		return err
	}
	if sourceServer.Status != model.ServerOnline {
		return fmt.Errorf("family split decision server %s is offline", sourceServer.Name)
	}
	if strings.TrimSpace(sourceServer.AgentID) != "" && !serverHasKernelCapability(*sourceServer, "family_selector_v1") {
		return fmt.Errorf("family split decision server %s 的内核缺少 family_selector_v1 能力；请先更新 Agent/内核", sourceServer.Name)
	}
	for _, target := range []struct {
		family string
		pathID int64
	}{
		{family: "IPv4", pathID: ipv4TargetPathID},
		{family: "IPv6", pathID: ipv6TargetPathID},
	} {
		if target.pathID == sourcePathID {
			if err := s.validateRoutingRuleSourceContinuation(ctx, sourcePathID, sourceStageStepID); err != nil {
				return fmt.Errorf("%s family branch: %w", target.family, err)
			}
		} else if err := s.validateRoutingRuleTargetPath(ctx, sourcePathID, sourceStageStepID, target.pathID, ruleID); err != nil {
			return fmt.Errorf("%s family branch: %w", target.family, err)
		}
		if err := s.validateRoutingRuleFamilyBranchAvailability(ctx, sourcePathID, sourceStageStepID, target.pathID, strings.ToLower(target.family), *sourceServer); err != nil {
			return fmt.Errorf("%s family branch: %w", target.family, err)
		}
	}
	return nil
}

func serverHasKernelCapability(server model.Server, capability string) bool {
	for _, current := range server.KernelCapabilities {
		if current == capability {
			return true
		}
	}
	return false
}

func (s *Server) routingRuleStageServer(ctx context.Context, sourcePathID int64, sourceStageStepID *int64) (*model.Server, error) {
	path, err := s.store.GetProxyPath(ctx, sourcePathID)
	if err != nil {
		return nil, err
	}
	if sourceStageStepID == nil {
		inbound, err := s.store.GetInbound(ctx, path.InboundID)
		if err != nil {
			return nil, err
		}
		return s.store.GetServer(ctx, inbound.ServerID)
	}
	step, err := s.store.GetProxyPathStep(ctx, *sourceStageStepID)
	if err != nil {
		return nil, err
	}
	if step.PathID != sourcePathID || step.NodeType != model.ProxyPathStepServerInbound || step.ServerID == nil {
		return nil, errors.New("family split stage must identify a controlled server")
	}
	return s.store.GetServer(ctx, *step.ServerID)
}

func (s *Server) validateRoutingRuleFamilyBranchAvailability(ctx context.Context, sourcePathID int64, sourceStageStepID *int64, targetPathID int64, family string, sourceServer model.Server) error {
	sourceSteps, err := s.store.ListProxyPathStepsForPath(ctx, sourcePathID)
	if err != nil {
		return err
	}
	stagePosition := 0
	if sourceStageStepID != nil {
		for _, step := range sourceSteps {
			if step.ID == *sourceStageStepID {
				stagePosition = step.Position
				break
			}
		}
	}
	targetSteps, err := s.store.ListProxyPathStepsForPath(ctx, targetPathID)
	if err != nil {
		return err
	}
	sort.SliceStable(targetSteps, func(i, j int) bool { return targetSteps[i].Position < targetSteps[j].Position })
	if len(targetSteps) <= stagePosition {
		return errors.New("target proxy path does not continue after the family split stage")
	}
	next := targetSteps[stagePosition]
	mode := next.TransportMode
	if mode == "" {
		mode = model.ProxyPathTransportSingBox
	}
	if mode != model.ProxyPathTransportSingBox || next.NodeType != model.ProxyPathStepServerInbound {
		return errors.New("family branch must enter a controlled server through a sing-box hop immediately after the split stage")
	}
	var inbound model.Inbound
	var targetServerID int64
	if next.InboundID != nil && *next.InboundID > 0 {
		item, err := s.store.GetInbound(ctx, *next.InboundID)
		if err != nil {
			return fmt.Errorf("target inbound %d: %w", *next.InboundID, err)
		}
		if !item.Enabled {
			return errors.New("target inbound is disabled")
		}
		inbound = *item
		targetServerID = item.ServerID
	} else if next.ServerID != nil && *next.ServerID > 0 {
		targetServerID = *next.ServerID
	} else {
		return errors.New("target controlled server is missing")
	}
	targetServer, err := s.store.GetServer(ctx, targetServerID)
	if err != nil {
		return err
	}
	if targetServer.Status != model.ServerOnline {
		return fmt.Errorf("target server %s is offline", targetServer.Name)
	}
	if inbound.ID == 0 {
		inbound = model.Inbound{ServerID: targetServer.ID, ListenIP: targetServer.ListenIP, EntryIPMode: model.EntryIPModeAuto, Enabled: true}
	}
	_, err = core.ResolveReachableEntryAddressForFamily(sourceServer, inbound, *targetServer, family)
	return err
}

func (s *Server) validateRoutingRuleSourceContinuation(ctx context.Context, sourcePathID int64, sourceStageStepID *int64) error {
	sourcePath, err := s.store.GetProxyPath(ctx, sourcePathID)
	if err != nil {
		return err
	}
	if !sourcePath.Enabled || sourcePath.Kind != model.ProxyPathKindChain {
		return errors.New("source proxy path must be an enabled chain")
	}
	steps, err := s.store.ListProxyPathStepsForPath(ctx, sourcePathID)
	if err != nil {
		return err
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	stagePosition := 0
	if sourceStageStepID != nil {
		for _, step := range steps {
			if step.ID == *sourceStageStepID {
				stagePosition = step.Position
				break
			}
		}
		if stagePosition == 0 {
			return errors.New("routing rule stage no longer belongs to its source path")
		}
	}
	if len(steps) <= stagePosition {
		return errors.New("source proxy path does not continue after the routing stage")
	}
	mode := steps[stagePosition].TransportMode
	if mode == "" {
		mode = model.ProxyPathTransportSingBox
	}
	if mode == model.ProxyPathTransportPortForward {
		return errors.New("family branches cannot start with transparent port forwarding after the routing stage")
	}
	return nil
}

func (s *Server) validateRoutingRuleTargetPath(ctx context.Context, sourcePathID int64, sourceStageStepID *int64, targetPathID, ruleID int64) error {
	if sourcePathID == targetPathID {
		return errors.New("routing rule target path must differ from its fallback path")
	}
	sourcePath, err := s.store.GetProxyPath(ctx, sourcePathID)
	if err != nil {
		return err
	}
	targetPath, err := s.store.GetProxyPath(ctx, targetPathID)
	if err != nil {
		return fmt.Errorf("target proxy path %d: %w", targetPathID, err)
	}
	if !targetPath.Enabled || targetPath.Kind != model.ProxyPathKindChain || targetPath.InboundID != sourcePath.InboundID {
		return errors.New("target proxy path must be an enabled chain from the same root inbound")
	}
	sourceSteps, err := s.store.ListProxyPathStepsForPath(ctx, sourcePathID)
	if err != nil {
		return err
	}
	targetSteps, err := s.store.ListProxyPathStepsForPath(ctx, targetPathID)
	if err != nil {
		return err
	}
	sort.SliceStable(sourceSteps, func(i, j int) bool { return sourceSteps[i].Position < sourceSteps[j].Position })
	sort.SliceStable(targetSteps, func(i, j int) bool { return targetSteps[i].Position < targetSteps[j].Position })
	stagePosition := 0
	if sourceStageStepID != nil {
		for _, step := range sourceSteps {
			if step.ID == *sourceStageStepID {
				stagePosition = step.Position
				break
			}
		}
		if stagePosition == 0 {
			return errors.New("routing rule stage no longer belongs to its fallback path")
		}
	}
	if len(targetSteps) <= stagePosition {
		return errors.New("target proxy path must continue after the routing stage")
	}
	for position := 1; position <= stagePosition; position++ {
		if len(sourceSteps) < position || len(targetSteps) < position || !equivalentRoutingPrefixStep(sourceSteps[position-1], targetSteps[position-1]) {
			return fmt.Errorf("target proxy path must share the fallback prefix through step %d", stagePosition)
		}
	}
	next := targetSteps[stagePosition]
	mode := next.TransportMode
	if mode == "" {
		mode = model.ProxyPathTransportSingBox
	}
	if mode == model.ProxyPathTransportPortForward {
		return errors.New("rule-specific proxy paths cannot start with transparent port forwarding after the routing stage")
	}
	items, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return err
	}
	edges := map[int64][]int64{}
	for _, item := range items {
		if item.ID == ruleID {
			continue
		}
		s.appendRoutingRulePathEdges(ctx, edges, item)
	}
	edges[sourcePathID] = append(edges[sourcePathID], targetPathID)
	if routingPathReachable(edges, targetPathID, sourcePathID, map[int64]bool{}) {
		return errors.New("routing rule proxy paths must not form a cycle")
	}
	return nil
}

func (s *Server) validateRoutingRuleTargetCandidate(ctx context.Context, rule model.RoutingRule, targetPath model.ProxyPath, targetSteps []model.ProxyPathStep) error {
	if rule.ProxyPathID == nil || *rule.ProxyPathID == targetPath.ID {
		return errors.New("routing rule target path must differ from its fallback path")
	}
	sourcePath, err := s.store.GetProxyPath(ctx, *rule.ProxyPathID)
	if err != nil {
		return err
	}
	if !targetPath.Enabled || targetPath.Kind != model.ProxyPathKindChain || targetPath.InboundID != sourcePath.InboundID {
		return errors.New("target proxy path must be an enabled chain from the same root inbound")
	}
	sourceSteps, err := s.store.ListProxyPathStepsForPath(ctx, sourcePath.ID)
	if err != nil {
		return err
	}
	sort.SliceStable(sourceSteps, func(i, j int) bool { return sourceSteps[i].Position < sourceSteps[j].Position })
	sort.SliceStable(targetSteps, func(i, j int) bool { return targetSteps[i].Position < targetSteps[j].Position })
	stagePosition := 0
	if rule.StageStepID != nil {
		for _, step := range sourceSteps {
			if step.ID == *rule.StageStepID {
				stagePosition = step.Position
				break
			}
		}
		if stagePosition == 0 {
			return errors.New("routing rule stage no longer belongs to its fallback path")
		}
	}
	if len(targetSteps) <= stagePosition {
		return errors.New("target proxy path must continue after the routing stage")
	}
	for position := 1; position <= stagePosition; position++ {
		if len(sourceSteps) < position || !equivalentRoutingPrefixStep(sourceSteps[position-1], targetSteps[position-1]) {
			return fmt.Errorf("target proxy path must share the fallback prefix through step %d", stagePosition)
		}
	}
	mode := targetSteps[stagePosition].TransportMode
	if mode == "" {
		mode = model.ProxyPathTransportSingBox
	}
	if mode == model.ProxyPathTransportPortForward {
		return errors.New("rule-specific proxy paths cannot start with transparent port forwarding after the routing stage")
	}
	items, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return err
	}
	edges := map[int64][]int64{}
	for _, item := range items {
		if item.ID == rule.ID {
			continue
		}
		s.appendRoutingRulePathEdges(ctx, edges, item)
	}
	edges[sourcePath.ID] = append(edges[sourcePath.ID], targetPath.ID)
	if routingPathReachable(edges, targetPath.ID, sourcePath.ID, map[int64]bool{}) {
		return errors.New("routing rule proxy paths must not form a cycle")
	}
	return nil
}

func (s *Server) appendRoutingRulePathEdges(ctx context.Context, edges map[int64][]int64, rule model.RoutingRule) {
	if !rule.Enabled || rule.ProxyPathID == nil {
		return
	}
	sourcePathID := *rule.ProxyPathID
	appendTarget := func(target int64) {
		if target > 0 && target != sourcePathID {
			edges[sourcePathID] = append(edges[sourcePathID], target)
		}
	}
	switch rule.Action {
	case model.RouteActionProxyPath:
		if rule.TargetProxyPathID != nil {
			appendTarget(*rule.TargetProxyPathID)
		}
	case model.RouteActionFamilySplit:
		if rule.FamilySplitTemplateID == nil {
			return
		}
		paths, err := s.store.ListProxyPaths(ctx)
		if err != nil {
			return
		}
		ipv4Path, ipv6Path, err := core.FamilySplitTemplatePaths(paths, *rule.FamilySplitTemplateID)
		if err != nil {
			return
		}
		appendTarget(ipv4Path.ID)
		appendTarget(ipv6Path.ID)
	}
}

func equivalentRoutingPrefixStep(left, right model.ProxyPathStep) bool {
	leftMode, rightMode := left.TransportMode, right.TransportMode
	if leftMode == "" {
		leftMode = model.ProxyPathTransportSingBox
	}
	if rightMode == "" {
		rightMode = model.ProxyPathTransportSingBox
	}
	return left.NodeType == right.NodeType && leftMode == rightMode && left.ProcessingRole == right.ProcessingRole &&
		sameOptionalInt64(left.ServerID, right.ServerID) && sameOptionalInt64(left.InboundID, right.InboundID) &&
		sameOptionalInt64(left.ExternalOutboundID, right.ExternalOutboundID) && canonicalRoutingJSON(left.ConfigJSON) == canonicalRoutingJSON(right.ConfigJSON)
}

func canonicalRoutingJSON(raw string) string {
	var value any
	if json.Unmarshal([]byte(firstNonEmptyString(strings.TrimSpace(raw), "{}")), &value) != nil {
		return strings.TrimSpace(raw)
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func routingPathReachable(edges map[int64][]int64, current, target int64, seen map[int64]bool) bool {
	if current == target {
		return true
	}
	if seen[current] {
		return false
	}
	seen[current] = true
	for _, next := range edges[current] {
		if routingPathReachable(edges, next, target, seen) {
			return true
		}
	}
	return false
}

func (s *Server) externalOutbounds(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/import") {
		s.importExternalOutbounds(w, r)
		return
	}
	id := idFromPath(r.URL.Path, "/api/v1/external-outbounds/")
	switch r.Method {
	case http.MethodGet:
		items, err := s.resolvedExternalOutbounds(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		if id > 0 {
			for i := range items {
				if items[i].ID == id {
					write(w, 200, map[string]any{"external_outbound": items[i]})
					return
				}
			}
			fail(w, sql.ErrNoRows, 404)
			return
		}
		write(w, 200, map[string]any{"external_outbounds": items})
	case http.MethodPost:
		var v model.ExternalOutbound
		if !decode(w, r, &v) {
			return
		}
		if err := s.validateExternalOutbound(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateExternalOutbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 201, map[string]any{"external_outbound": s.resolvedExternalOutbound(r.Context(), v)})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetExternalOutbound(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if err := s.validateExternalOutbound(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateExternalOutbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"external_outbound": s.resolvedExternalOutbound(r.Context(), v)})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if _, err := s.guardAssignableNodeDelete(r.Context(), model.AssignableNodeExternalOutbound, id); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if _, err := s.store.RemoveAssignableNodeFromPlans(r.Context(), model.AssignableNodeExternalOutbound, id); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if err := s.store.DeleteProxyPathStepsForExternal(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.Delete(r.Context(), "external_outbounds", id); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) validateExternalOutbound(ctx context.Context, v *model.ExternalOutbound) error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("name required")
	}
	if err := normalizeRegionSelection(&v.RegionMode, &v.RegionCode, "region"); err != nil {
		return err
	}
	if v.Scope == "" {
		v.Scope = model.ExternalOutboundScopeGlobal
	}
	if v.Scope != model.ExternalOutboundScopeGlobal && v.Scope != model.ExternalOutboundScopeServer {
		return fmt.Errorf("unsupported scope %q", v.Scope)
	}
	if v.Scope == model.ExternalOutboundScopeServer && (v.ServerID == nil || *v.ServerID == 0) {
		return errors.New("server_id required for server-scoped external outbound")
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
	if err := validJSONObject(v.ConfigJSON); err != nil {
		return err
	}
	if v.Protocol == model.ProtocolSocks {
		if err := validateSocksExternalConfig(v.ConfigJSON); err != nil {
			return err
		}
	} else {
		if _, err := core.AdapterFor(v.Protocol); err != nil {
			return err
		}
		normalized, err := applyProtocolAuthDefaults(v.Protocol, v.ConfigJSON)
		if err != nil {
			return err
		}
		v.ConfigJSON = normalized
	}
	if err := normalizeMieruOutboundPorts(v.Protocol, &v.TargetPort, &v.ConfigJSON); err != nil {
		return err
	}
	if v.TargetAddress == "" {
		var raw map[string]any
		if json.Unmarshal([]byte(v.ConfigJSON), &raw) == nil {
			if server, _ := raw["server"].(string); server != "" {
				v.TargetAddress = server
			}
			if port, ok := raw["server_port"].(float64); ok && port > 0 {
				v.TargetPort = int(port)
			}
		}
	}
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	if err := core.ValidatePort(v.TargetPort); err != nil {
		return err
	}
	if v.Protocol == model.ProtocolMieru {
		adapter, err := core.AdapterFor(v.Protocol)
		if err != nil {
			return err
		}
		if err := adapter.ValidateOutbound(model.Outbound{
			Protocol:      v.Protocol,
			TargetAddress: v.TargetAddress,
			TargetPort:    v.TargetPort,
			ConfigJSON:    v.ConfigJSON,
		}); err != nil {
			return err
		}
	}
	if v.ServerID != nil && *v.ServerID != 0 {
		server, err := s.store.GetServer(ctx, *v.ServerID)
		if err != nil {
			return err
		}
		return core.ValidateAddressForIPStack(core.EffectiveIPStack(*server), v.TargetAddress)
	}
	return nil
}

func (s *Server) importExternalOutbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req struct {
		ServerID      *int64                      `json:"server_id"`
		Scope         model.ExternalOutboundScope `json:"scope"`
		Content       string                      `json:"content"`
		ExposeToUsers bool                        `json:"expose_to_users"`
	}
	if !decode(w, r, &req) {
		return
	}
	items, err := parseExternalOutboundImport(req.Content)
	if err != nil {
		fail(w, err, 400)
		return
	}
	created := []model.ExternalOutbound{}
	for _, item := range items {
		item.ServerID = req.ServerID
		item.Scope = req.Scope
		if item.Scope == "" {
			item.Scope = model.ExternalOutboundScopeGlobal
		}
		item.ExposeToUsers = req.ExposeToUsers
		item.Enabled = true
		if err := s.validateExternalOutbound(r.Context(), &item); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateExternalOutbound(r.Context(), &item); err != nil {
			fail(w, err, 500)
			return
		}
		created = append(created, item)
	}
	if resolved, resolveErr := s.resolvedExternalOutbounds(r.Context()); resolveErr == nil {
		resolvedByID := make(map[int64]model.ExternalOutbound, len(resolved))
		for _, item := range resolved {
			resolvedByID[item.ID] = item
		}
		for i := range created {
			if item, ok := resolvedByID[created[i].ID]; ok {
				created[i] = item
			}
		}
	}
	write(w, 201, map[string]any{"external_outbounds": created})
}

func parseExternalOutboundImport(content string) ([]model.ExternalOutbound, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("content required")
	}
	if len(content) > 256*1024 {
		return nil, errors.New("import content is too large")
	}
	if !strings.HasPrefix(content, "{") && !strings.HasPrefix(content, "[") {
		lines := strings.Split(content, "\n")
		if len(lines) > 200 {
			return nil, errors.New("too many import lines")
		}
		out := make([]model.ExternalOutbound, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
				continue
			}
			items, err := parseExternalOutboundLine(line)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		if len(out) == 0 {
			return nil, errors.New("no importable node found")
		}
		return out, nil
	}
	var rawList []map[string]any
	if err := json.Unmarshal([]byte(content), &rawList); err != nil {
		var one map[string]any
		if err2 := json.Unmarshal([]byte(content), &one); err2 != nil {
			return nil, err
		}
		rawList = []map[string]any{one}
	}
	if len(rawList) > 100 {
		return nil, errors.New("too many outbounds in one import")
	}
	out := make([]model.ExternalOutbound, 0, len(rawList))
	for i, raw := range rawList {
		item, err := externalOutboundFromRawMap(raw, fmt.Sprintf("imported-%d", i+1))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func parseExternalOutboundLine(line string) ([]model.ExternalOutbound, error) {
	if strings.HasPrefix(line, "{") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, err
		}
		item, err := externalOutboundFromRawMap(raw, "imported-1")
		return []model.ExternalOutbound{item}, err
	}
	u, err := url.Parse(line)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "ss":
		item, err := parseSSImportURI(line)
		return []model.ExternalOutbound{item}, err
	case "socks", "socks5":
		item, err := parseSocksImportURI(u)
		return []model.ExternalOutbound{item}, err
	case "vless":
		item, err := parseVLESSImportURI(u)
		return []model.ExternalOutbound{item}, err
	case "mierus":
		return parseMieruSimpleImportURI(u)
	case "mieru":
		return nil, errors.New("不支持导入二进制 mieru:// 配置，请使用官方 mierus:// 简单分享链接")
	case "trojan":
		return nil, errors.New("暂不支持导入 Trojan")
	default:
		return nil, fmt.Errorf("unsupported import scheme %q", u.Scheme)
	}
}

func externalOutboundFromRawMap(raw map[string]any, fallbackName string) (model.ExternalOutbound, error) {
	if raw == nil {
		return model.ExternalOutbound{}, errors.New("empty outbound")
	}
	proto, _ := raw["type"].(string)
	switch proto {
	case "hysteria2":
		proto = string(model.ProtocolHY2)
	case "shadow_socks":
		proto = string(model.ProtocolSS)
	}
	switch model.Protocol(proto) {
	case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolMieru, model.ProtocolSnell, model.ProtocolSocks:
	default:
		return model.ExternalOutbound{}, fmt.Errorf("unsupported outbound type %q", proto)
	}
	name, _ := raw["tag"].(string)
	if strings.TrimSpace(name) == "" {
		name = fallbackName
	}
	addr, _ := raw["server"].(string)
	port := intFromAnyController(raw["server_port"])
	b, _ := json.Marshal(raw)
	return model.ExternalOutbound{Name: name, Protocol: model.Protocol(proto), TargetAddress: addr, TargetPort: port, ConfigJSON: string(b), Enabled: true}, nil
}

func parseMieruSimpleImportURI(u *url.URL) ([]model.ExternalOutbound, error) {
	if u == nil || !strings.EqualFold(u.Scheme, "mierus") || u.Opaque != "" {
		return nil, errors.New("invalid mierus URI")
	}
	if u.User == nil || strings.TrimSpace(u.User.Username()) == "" {
		return nil, errors.New("invalid mierus URI: username required")
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		return nil, errors.New("invalid mierus URI: password required")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return nil, errors.New("invalid mierus URI: server required")
	}
	query := u.Query()
	profile := strings.TrimSpace(query.Get("profile"))
	if profile == "" {
		return nil, errors.New("invalid mierus URI: profile required")
	}
	if query.Get("mtu") != "" || query.Get("handshake-mode") != "" {
		return nil, errors.New("暂不支持导入带 mtu 或 handshake-mode 的 mierus 链接")
	}
	portValues := query["port"]
	protocolValues := query["protocol"]
	if len(portValues) == 0 || len(portValues) != len(protocolValues) {
		return nil, errors.New("invalid mierus URI: port and protocol counts must match")
	}
	rangesByTransport := map[string][]string{"TCP": {}, "UDP": {}}
	for index, value := range portValues {
		transport := strings.ToUpper(strings.TrimSpace(protocolValues[index]))
		if transport != "TCP" && transport != "UDP" {
			return nil, fmt.Errorf("invalid mierus URI transport %q", protocolValues[index])
		}
		value = strings.TrimSpace(value)
		if port, err := strconv.Atoi(value); err == nil {
			if err := core.ValidatePort(port); err != nil {
				return nil, fmt.Errorf("invalid mierus URI port %q", value)
			}
			value = fmt.Sprintf("%d-%d", port, port)
		}
		rangesByTransport[transport] = append(rangesByTransport[transport], value)
	}
	transports := make([]string, 0, 2)
	for _, transport := range []string{"TCP", "UDP"} {
		if len(rangesByTransport[transport]) > 0 {
			transports = append(transports, transport)
		}
	}
	out := make([]model.ExternalOutbound, 0, len(transports))
	for _, transport := range transports {
		config := map[string]any{
			"type":         "mieru",
			"server":       host,
			"username":     u.User.Username(),
			"password":     password,
			"transport":    transport,
			"server_ports": rangesByTransport[transport],
		}
		if multiplexing := strings.TrimSpace(query.Get("multiplexing")); multiplexing != "" {
			config["multiplexing"] = multiplexing
		}
		if pattern := strings.TrimSpace(query.Get("traffic-pattern")); pattern != "" {
			config["traffic_pattern"] = pattern
		}
		configJSON, err := json.Marshal(config)
		if err != nil {
			return nil, err
		}
		primary, normalized, err := core.NormalizeMieruPortConfig(0, string(configJSON), "server_ports")
		if err != nil {
			return nil, fmt.Errorf("invalid mierus URI ports: %w", err)
		}
		name := importNodeName(u, profile)
		if len(transports) > 1 {
			name += " / " + transport
		}
		var normalizedConfig map[string]any
		if err := json.Unmarshal([]byte(normalized), &normalizedConfig); err != nil {
			return nil, err
		}
		normalizedConfig["tag"] = name
		normalizedConfig["server_port"] = primary
		normalized, err = marshalControllerJSON(normalizedConfig)
		if err != nil {
			return nil, err
		}
		out = append(out, model.ExternalOutbound{
			Name:          name,
			Protocol:      model.ProtocolMieru,
			TargetAddress: host,
			TargetPort:    primary,
			ConfigJSON:    normalized,
			Enabled:       true,
		})
	}
	return out, nil
}

func marshalControllerJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func parseSSImportURI(rawURI string) (model.ExternalOutbound, error) {
	u, err := url.Parse(rawURI)
	if err != nil {
		return model.ExternalOutbound{}, err
	}
	if u.Host == "" {
		decoded, err := decodeShareBase64(strings.TrimPrefix(strings.Split(rawURI, "#")[0], "ss://"))
		if err != nil {
			return model.ExternalOutbound{}, fmt.Errorf("invalid fully encoded ss URI: %w", err)
		}
		fragment := ""
		if idx := strings.Index(rawURI, "#"); idx >= 0 {
			fragment = rawURI[idx+1:]
		}
		u, err = url.Parse("ss://" + decoded + "#" + fragment)
		if err != nil {
			return model.ExternalOutbound{}, err
		}
	}
	host, port, err := splitURIHostPort(u)
	if err != nil {
		return model.ExternalOutbound{}, err
	}
	method := u.User.Username()
	password, hasPassword := u.User.Password()
	if !hasPassword {
		decoded, err := decodeShareBase64(method)
		if err != nil {
			return model.ExternalOutbound{}, errors.New("invalid ss URI: method/password missing")
		}
		parts := strings.SplitN(decoded, ":", 2)
		if len(parts) != 2 {
			return model.ExternalOutbound{}, errors.New("invalid ss URI: method/password missing")
		}
		method, password = parts[0], parts[1]
	}
	if method == "" || password == "" {
		return model.ExternalOutbound{}, errors.New("invalid ss URI: method/password required")
	}
	cfg := map[string]any{"type": "shadowsocks", "tag": importNodeName(u, "SS "+host), "server": host, "server_port": port, "method": method, "password": password}
	if plugin := u.Query().Get("plugin"); plugin != "" {
		cfg["plugin"] = plugin
	}
	return externalOutboundFromRawMap(cfg, "ss-import")
}

func parseSocksImportURI(u *url.URL) (model.ExternalOutbound, error) {
	host, port, err := splitURIHostPort(u)
	if err != nil {
		return model.ExternalOutbound{}, err
	}
	cfg := map[string]any{"type": "socks", "tag": importNodeName(u, "SOCKS "+host), "server": host, "server_port": port, "version": "5"}
	if strings.ToLower(u.Scheme) == "socks4" {
		cfg["version"] = "4"
	}
	if username := u.User.Username(); username != "" {
		cfg["username"] = username
	}
	if password, ok := u.User.Password(); ok {
		cfg["password"] = password
	}
	return externalOutboundFromRawMap(cfg, "socks-import")
}

func parseVLESSImportURI(u *url.URL) (model.ExternalOutbound, error) {
	host, port, err := splitURIHostPort(u)
	if err != nil {
		return model.ExternalOutbound{}, err
	}
	uuid := strings.TrimSpace(u.User.Username())
	if uuid == "" {
		return model.ExternalOutbound{}, errors.New("invalid vless URI: uuid required")
	}
	q := u.Query()
	cfg := map[string]any{"type": "vless", "tag": importNodeName(u, "VLESS "+host), "server": host, "server_port": port, "uuid": uuid}
	if flow := q.Get("flow"); flow != "" {
		cfg["flow"] = flow
	}
	if packetEncoding := firstQuery(q, "packetEncoding", "packet_encoding"); packetEncoding != "" {
		cfg["packet_encoding"] = packetEncoding
	}
	transportType := q.Get("type")
	if transportType != "" && transportType != "tcp" {
		transport := map[string]any{"type": transportType}
		if transportType == "ws" {
			if path := q.Get("path"); path != "" {
				transport["path"] = path
			}
			if hostHeader := q.Get("host"); hostHeader != "" {
				transport["headers"] = map[string]any{"Host": hostHeader}
			}
		}
		cfg["transport"] = transport
	}
	security := strings.ToLower(q.Get("security"))
	if security == "tls" || security == "reality" {
		tls := map[string]any{"enabled": true}
		if sni := firstQuery(q, "sni", "serverName", "peer"); sni != "" {
			tls["server_name"] = sni
		}
		if fp := firstQuery(q, "fp", "fingerprint"); fp != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		if security == "reality" {
			reality := map[string]any{"enabled": true}
			if pbk := firstQuery(q, "pbk", "public_key"); pbk != "" {
				reality["public_key"] = pbk
			}
			if sid := firstQuery(q, "sid", "short_id"); sid != "" {
				reality["short_id"] = sid
			}
			tls["reality"] = reality
		}
		cfg["tls"] = tls
	}
	return externalOutboundFromRawMap(cfg, "vless-import")
}

func splitURIHostPort(u *url.URL) (string, int, error) {
	host := strings.Trim(u.Hostname(), "[]")
	portText := u.Port()
	if host == "" || portText == "" {
		return "", 0, errors.New("host and port required")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, err
	}
	if err := core.ValidatePort(port); err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func importNodeName(u *url.URL, fallback string) string {
	if name, err := url.QueryUnescape(u.Fragment); err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return fallback
}

func decodeShareBase64(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if decoded, err := enc.DecodeString(raw); err == nil {
			return string(decoded), nil
		}
	}
	if m := len(raw) % 4; m != 0 {
		return decodeShareBase64(raw + strings.Repeat("=", 4-m))
	}
	return "", errors.New("invalid base64")
}

func firstQuery(q url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(q.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func intFromAnyController(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func validateSocksExternalConfig(raw string) error {
	var cfg map[string]any
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return err
	}
	if version := strings.TrimSpace(stringFromMap(cfg, "version")); version != "" && version != "4" && version != "4a" && version != "5" {
		return errors.New("socks version must be 4, 4a or 5")
	}
	return nil
}

func (s *Server) proxyPaths(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/proxy-paths/")
	parts := pathParts(r.URL.Path, "/api/v1/proxy-paths/")
	if len(parts) > 0 && parts[0] == "reuse-preview" {
		s.proxyPathReusePreview(w, r)
		return
	}
	if len(parts) > 0 && parts[0] == "reuse" {
		s.proxyPathReuseApply(w, r)
		return
	}
	if id != 0 && len(parts) > 1 && parts[1] == "probe-egress" {
		s.probeProxyPathEgress(w, r, id)
		return
	}
	if len(parts) > 0 && parts[0] == "direct-branches" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		s.createDirectProxyPathBranch(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if id != 0 && len(parts) > 1 && parts[1] == "plan" {
			items, steps, servers, inbounds, externals, err := s.proxyPathNameData(r.Context())
			if err != nil {
				fail(w, err, 500)
				return
			}
			resolved := core.ResolveProxyPathNames(items, steps, servers, inbounds, externals)
			item, ok := proxyPathByID(resolved, id)
			if !ok {
				fail(w, sql.ErrNoRows, 404)
				return
			}
			pathSteps := proxyPathStepsForPath(steps, id)
			// Project the whole set against the persisted ports: shared listeners,
			// shared tunnels and the single-transparent-path rule all depend on the
			// other paths, so a single-path or ledger-less projection would report
			// ports and conflicts that differ from what a deployment produces.
			allocations, err := s.store.ListProxyPathPortAllocations(r.Context())
			if err != nil {
				fail(w, err, 500)
				return
			}
			plans, err := core.BuildProxyPathPlansWithLedger(resolved, steps, servers, inbounds, core.NewProxyPathPortLedger(allocations))
			if err != nil {
				fail(w, err, 400)
				return
			}
			plan, ok := proxyPathPlanByID(plans, id)
			if !ok {
				write(w, 200, map[string]any{"plan": proxyPathPlanSummary(item, pathSteps)})
				return
			}
			write(w, 200, map[string]any{"plan": publicProxyPathPlan(plan)})
			return
		}
		if id != 0 {
			items, steps, servers, inbounds, externals, err := s.proxyPathNameData(r.Context())
			if err != nil {
				fail(w, err, 500)
				return
			}
			item, ok := proxyPathByID(core.ResolveProxyPathNames(items, steps, servers, inbounds, externals), id)
			if !ok {
				fail(w, sql.ErrNoRows, 404)
				return
			}
			write(w, 200, map[string]any{"proxy_path": item})
			return
		}
		items, steps, servers, inbounds, externals, err := s.proxyPathNameData(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		items = core.ResolveProxyPathNames(items, steps, servers, inbounds, externals)
		write(w, 200, map[string]any{"proxy_paths": items})
	case http.MethodPost:
		var request struct {
			model.ProxyPath
			InitialSteps  []model.ProxyPathStep `json:"initial_steps,omitempty"`
			RoutingRuleID int64                 `json:"routing_rule_id,omitempty"`
		}
		if !decode(w, r, &request) {
			return
		}
		v := request.ProxyPath
		if v.Kind == model.ProxyPathKindFamilyBranch {
			fail(w, errors.New("family_branch 路径只能通过双栈模板接口创建"), 400)
			return
		}
		if len(request.InitialSteps) > 0 {
			s.createProxyPathComposition(w, r, &v, request.InitialSteps, request.RoutingRuleID)
			return
		}
		if v.BranchSourceStepID != nil {
			fail(w, errors.New("branch_source_step_id 只能由直接出口分支接口设置"), 400)
			return
		}
		if strings.TrimSpace(v.Secret) == "" {
			secret, err := security.RandomToken(24)
			if err != nil {
				fail(w, err, 500)
				return
			}
			v.Secret = secret
		}
		if err := s.validateProxyPath(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if v.Enabled {
			if err := s.validateProxyPathCandidateData(r.Context(), v); err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
		}
		if err := s.store.CreateProxyPath(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if v.Enabled && v.Kind == model.ProxyPathKindDirect {
			if err := s.validateEnabledProxyPathPlan(r.Context(), v.ID); err != nil {
				_ = s.store.DeleteProxyPath(r.Context(), v.ID)
				fail(w, err, 400)
				return
			}
		}
		v = s.resolvedProxyPath(r.Context(), v)
		auditReq(s, r, "create", "proxy-path", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"proxy_path": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetProxyPath(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if current.Kind == model.ProxyPathKindFamilyBranch {
			fail(w, errors.New("family_branch 路径只能通过双栈模板接口修改"), 400)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if v.Kind != current.Kind {
			fail(w, errors.New("代理路径类型不能修改，请删除后重新创建"), 400)
			return
		}
		if !sameOptionalInt64(v.BranchSourceStepID, current.BranchSourceStepID) {
			fail(w, errors.New("代理路径分支来源不能修改"), 400)
			return
		}
		if strings.TrimSpace(v.Secret) == "" {
			v.Secret = current.Secret
		}
		if v.InboundID != current.InboundID && v.NameMode == model.ProxyPathNameCustom {
			steps, _ := s.store.ListProxyPathStepsForPath(r.Context(), id)
			servers, _ := s.store.ListServers(r.Context())
			inbounds, _ := s.store.ListInbounds(r.Context())
			externals, _ := s.store.ListExternalOutbounds(r.Context())
			if !core.ProxyPathNameTemplateIsValid(v, steps, servers, inbounds, externals) {
				v.NameMode = model.ProxyPathNameAuto
				v.NameTemplate = []model.ProxyPathNamePart{}
			}
		}
		if err := s.validateProxyPath(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if v.Enabled {
			if err := s.validateProxyPathCandidateData(r.Context(), v); err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
		}
		if err := s.store.UpdateProxyPath(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if v.Enabled {
			if err := s.ensureWARPProfilesForProxyPaths(r.Context()); err != nil {
				restore := *current
				_ = s.store.UpdateProxyPath(r.Context(), &restore)
				fail(w, err, 500)
				return
			}
		}
		v = s.resolvedProxyPath(r.Context(), v)
		auditReq(s, r, "update", "proxy-path", fmt.Sprint(id))
		write(w, 200, map[string]any{"proxy_path": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		currentPath, err := s.store.GetProxyPath(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if currentPath.Kind == model.ProxyPathKindFamilyBranch {
			fail(w, errors.New("family_branch 路径只能通过双栈模板接口删除"), 400)
			return
		}
		if _, err := s.guardAssignableNodeDelete(r.Context(), model.AssignableNodeProxyPath, id); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if err := s.store.DeleteProxyPath(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "proxy-path", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) createProxyPathComposition(w http.ResponseWriter, r *http.Request, path *model.ProxyPath, steps []model.ProxyPathStep, routingRuleID int64) {
	if strings.TrimSpace(path.Secret) == "" {
		secret, err := security.RandomToken(24)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		path.Secret = secret
	}
	if path.BranchSourceStepID != nil {
		fail(w, errors.New("branch_source_step_id 只能由直接出口分支接口设置"), http.StatusBadRequest)
		return
	}
	if err := s.validateProxyPath(r.Context(), path); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	for _, item := range data.ProxyPaths {
		if item.ID >= path.ID {
			path.ID = item.ID + 1
		}
	}
	if path.ID <= 0 {
		path.ID = 1
	}
	maxStepID := int64(0)
	for _, item := range data.ProxyPathSteps {
		if item.ID > maxStepID {
			maxStepID = item.ID
		}
	}
	seenPositions := map[int]bool{}
	for index := range steps {
		step := &steps[index]
		if step.Position <= 0 {
			step.Position = index + 1
		}
		if seenPositions[step.Position] {
			fail(w, errors.New("same path step position already exists"), http.StatusBadRequest)
			return
		}
		seenPositions[step.Position] = true
		step.PathID = path.ID
		maxStepID++
		step.ID = maxStepID
		if err := s.normalizeProxyPathStepCandidate(r.Context(), path, step); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
	}
	if err := s.validateProxyPathServerLoop(r.Context(), path.InboundID, steps); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	data.ProxyPaths = append(data.ProxyPaths, *path)
	data.ProxyPathSteps = append(data.ProxyPathSteps, steps...)
	if err := normalizeProxyPathProcessingRolesInMemory(data.ProxyPathSteps, path.ID); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	copy(steps, data.ProxyPathSteps[len(data.ProxyPathSteps)-len(steps):])
	resolveRoutingProxyPathNames(&data)
	if _, err := core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	var routingRule *model.RoutingRule
	if routingRuleID > 0 {
		routingRule, err = s.store.GetRoutingRule(r.Context(), routingRuleID)
		if err != nil {
			fail(w, err, http.StatusNotFound)
			return
		}
		if err := s.validateRoutingRuleTargetCandidate(r.Context(), *routingRule, *path, steps); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
	}
	storedPath := *path
	if routingRuleID > 0 {
		storedPath.Enabled = false
	}
	if err := s.store.CreateProxyPathComposition(r.Context(), &storedPath, steps); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	path.ID = storedPath.ID
	if routingRuleID > 0 {
		if err := s.store.ActivateProxyPathComposition(r.Context(), path.ID, routingRuleID); err != nil {
			_ = s.store.DeleteProxyPath(r.Context(), path.ID)
			fail(w, err, http.StatusInternalServerError)
			return
		}
	}
	if err := s.ensureWARPProfilesForProxyPaths(r.Context()); err != nil {
		_ = s.store.DeleteProxyPath(r.Context(), path.ID)
		fail(w, err, http.StatusInternalServerError)
		return
	}
	resolved := s.resolvedProxyPath(r.Context(), *path)
	storedSteps, err := s.store.ListProxyPathStepsForPath(r.Context(), path.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	response := map[string]any{"proxy_path": resolved, "proxy_path_steps": publicProxyPathSteps(storedSteps)}
	if routingRule != nil {
		routingRule.Action = model.RouteActionProxyPath
		routingRule.TargetProxyPathID = &path.ID
		routingRule.OutboundID = nil
		routingRule.ExternalOutboundID = nil
		routingRule.TargetServerID = nil
		routingRule.OutboundTag = ""
		response["routing_rule"] = routingRule
	}
	auditReq(s, r, "create", "proxy-path", fmt.Sprint(path.ID))
	write(w, http.StatusCreated, response)
}

type directProxyPathBranchRequest struct {
	InboundID    int64              `json:"inbound_id"`
	SourceStepID int64              `json:"source_step_id"`
	RoutingRule  *model.RoutingRule `json:"routing_rule,omitempty"`
}

func (s *Server) createDirectProxyPathBranch(w http.ResponseWriter, r *http.Request) {
	var request directProxyPathBranchRequest
	if !decode(w, r, &request) {
		return
	}
	if (request.InboundID == 0) == (request.SourceStepID == 0) {
		fail(w, errors.New("inbound_id 和 source_step_id 必须且只能提供一个"), 400)
		return
	}

	inboundID := request.InboundID
	var branchSourceStepID *int64
	prefix := []model.ProxyPathStep{}
	if request.SourceStepID != 0 {
		sourceStep, err := s.store.GetProxyPathStep(r.Context(), request.SourceStepID)
		if err != nil {
			fail(w, fmt.Errorf("source_step_id: %w", err), 404)
			return
		}
		if sourceStep.NodeType != model.ProxyPathStepServerInbound || ((sourceStep.ServerID == nil || *sourceStep.ServerID == 0) && (sourceStep.InboundID == nil || *sourceStep.InboundID == 0)) {
			fail(w, errors.New("直接出口只能从可控服务器节点创建"), 400)
			return
		}
		sourcePath, err := s.store.GetProxyPath(r.Context(), sourceStep.PathID)
		if err != nil {
			fail(w, fmt.Errorf("source path: %w", err), 404)
			return
		}
		if !sourcePath.Enabled || sourcePath.Kind != model.ProxyPathKindChain {
			fail(w, errors.New("直接出口只能从已启用的普通代理路径创建"), 400)
			return
		}
		inboundID = sourcePath.InboundID
		steps, err := s.store.ListProxyPathStepsForPath(r.Context(), sourcePath.ID)
		if err != nil {
			fail(w, err, 500)
			return
		}
		found := false
		for _, step := range steps {
			if step.Position > sourceStep.Position {
				break
			}
			prefix = append(prefix, step)
			if step.ID == sourceStep.ID {
				found = true
				break
			}
		}
		if !found {
			fail(w, errors.New("source_step_id 不属于有效路径前缀"), 400)
			return
		}
		branchSourceStepID = &request.SourceStepID
	}

	secret, err := security.RandomToken(24)
	if err != nil {
		fail(w, err, 500)
		return
	}
	path := model.ProxyPath{
		Kind:               model.ProxyPathKindDirect,
		BranchSourceStepID: branchSourceStepID,
		NameMode:           model.ProxyPathNameAuto,
		NameTemplate:       []model.ProxyPathNamePart{},
		InboundID:          inboundID,
		Secret:             secret,
		Enabled:            false,
	}
	if err := s.validateProxyPath(r.Context(), &path); err != nil {
		fail(w, err, 400)
		return
	}
	if len(prefix) == 0 {
		if err := s.store.CreateProxyPath(r.Context(), &path); err != nil {
			fail(w, err, 500)
			return
		}
	} else {
		paths, err := s.store.ListProxyPaths(r.Context())
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		path.ID = 1
		for _, item := range paths {
			if item.ID >= path.ID {
				path.ID = item.ID + 1
			}
		}
		steps := make([]model.ProxyPathStep, len(prefix))
		for index, source := range prefix {
			step := source
			step.ID = 0
			step.PathID = path.ID
			step.Position = index + 1
			step.ProcessingRole = false
			step.CreatedAt = time.Time{}
			step.UpdatedAt = time.Time{}
			steps[index] = step
		}
		if err := normalizeProxyPathProcessingRolesInMemory(steps, path.ID); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		if err := s.store.CreateProxyPathComposition(r.Context(), &path, steps); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
	}
	cleanup := func() { _ = s.store.DeleteProxyPath(r.Context(), path.ID) }
	path.Enabled = true
	if err := s.validateProxyPath(r.Context(), &path); err != nil {
		cleanup()
		fail(w, err, 400)
		return
	}
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		cleanup()
		fail(w, err, http.StatusInternalServerError)
		return
	}
	for index := range data.ProxyPaths {
		if data.ProxyPaths[index].ID == path.ID {
			data.ProxyPaths[index] = path
		}
	}
	if err := normalizeProxyPathProcessingRolesInMemory(data.ProxyPathSteps, path.ID); err != nil {
		cleanup()
		fail(w, err, http.StatusBadRequest)
		return
	}
	resolveRoutingProxyPathNames(&data)
	if _, err := core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)); err != nil {
		cleanup()
		fail(w, err, http.StatusBadRequest)
		return
	}
	var sourceRuleID int64
	var groupID string
	if request.RoutingRule != nil {
		rule := request.RoutingRule
		rule.ProxyPathID = &path.ID
		if rule.Scope == "" {
			rule.Scope = model.RoutingRuleScopePathStage
		}
		if err := s.validateRoutingRule(r.Context(), rule); err != nil {
			cleanup()
			fail(w, err, http.StatusBadRequest)
			return
		}
		var err error
		sourceRuleID, err = s.prepareRoutingRuleReuse(r.Context(), rule)
		if err != nil {
			cleanup()
			fail(w, err, http.StatusBadRequest)
			return
		}
		if sourceRuleID > 0 {
			groupID, err = security.RandomToken(18)
			if err != nil {
				cleanup()
				fail(w, err, http.StatusInternalServerError)
				return
			}
		}
		if err := s.store.ActivateProxyPathWithRoutingRule(r.Context(), path.ID, rule, sourceRuleID, groupID); err != nil {
			cleanup()
			fail(w, err, http.StatusInternalServerError)
			return
		}
	} else if err := s.store.UpdateProxyPath(r.Context(), &path); err != nil {
		cleanup()
		fail(w, err, 500)
		return
	}
	if err := s.validateEnabledProxyPathPlan(r.Context(), path.ID); err != nil {
		cleanup()
		fail(w, err, 400)
		return
	}
	if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
		cleanup()
		fail(w, err, 500)
		return
	}
	path = s.resolvedProxyPath(r.Context(), path)
	steps, err := s.store.ListProxyPathStepsForPath(r.Context(), path.ID)
	if err != nil {
		cleanup()
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "create", "proxy-path", fmt.Sprint(path.ID))
	response := map[string]any{"proxy_path": path, "proxy_path_steps": publicProxyPathSteps(steps)}
	if request.RoutingRule != nil {
		response["routing_rule"] = request.RoutingRule
	}
	write(w, http.StatusCreated, response)
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Server) validateProxyPath(ctx context.Context, v *model.ProxyPath) error {
	if v.Kind == "" {
		v.Kind = model.ProxyPathKindChain
	}
	switch v.Kind {
	case model.ProxyPathKindChain, model.ProxyPathKindDirect, model.ProxyPathKindFamilyBranch:
	default:
		return errors.New("kind must be chain, direct or family_branch")
	}
	if v.Kind == model.ProxyPathKindChain && v.BranchSourceStepID != nil {
		return errors.New("普通代理路径不能设置 branch_source_step_id")
	}
	if v.Kind == model.ProxyPathKindFamilyBranch {
		if v.InboundID != 0 {
			return errors.New("family_branch 不能绑定入口")
		}
		if v.TemplateID == nil || *v.TemplateID <= 0 {
			return errors.New("template_id required")
		}
		if v.Family != model.FamilySplitFamilyIPv4 && v.Family != model.FamilySplitFamilyIPv6 {
			return errors.New("family must be ipv4 or ipv6")
		}
		if v.BranchSourceStepID != nil {
			return errors.New("family_branch 不能设置 branch_source_step_id")
		}
	} else if v.InboundID == 0 {
		return errors.New("inbound_id required")
	}
	if err := normalizeRegionSelection(&v.ExitRegionMode, &v.ExitRegionCode, "exit region"); err != nil {
		return err
	}
	if v.Kind != model.ProxyPathKindFamilyBranch {
		_, err := s.store.GetInbound(ctx, v.InboundID)
		if err != nil {
			return fmt.Errorf("inbound_id: %w", err)
		}
		if v.Enabled {
			if err := s.validateInboundPathReuse(ctx, v.InboundID, v.ID); err != nil {
				return err
			}
		}
	}
	steps, err := s.store.ListProxyPathStepsForPath(ctx, v.ID)
	if err != nil {
		return err
	}
	if v.BranchSourceStepID != nil {
		source, err := s.store.GetProxyPathStep(ctx, *v.BranchSourceStepID)
		if err != nil {
			return fmt.Errorf("branch_source_step_id: %w", err)
		}
		sourcePath, err := s.store.GetProxyPath(ctx, source.PathID)
		if err != nil {
			return fmt.Errorf("branch source path: %w", err)
		}
		if sourcePath.InboundID != v.InboundID || source.ID == 0 {
			return errors.New("branch_source_step_id 必须属于同一根入口")
		}
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return err
	}
	externals, err := s.store.ListExternalOutbounds(ctx)
	if err != nil {
		return err
	}
	return core.NormalizeProxyPathName(v, steps, servers, inbounds, externals)
}

// validateProxyPathTruncation projects the topology that would remain after
// cutting one path at the given position. It runs entirely in memory so a
// rejected delete leaves the stored chain untouched.
func (s *Server) validateProxyPathTruncation(ctx context.Context, pathID int64, position int) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	steps := make([]model.ProxyPathStep, 0, len(data.ProxyPathSteps))
	remainingPathSteps := 0
	for _, step := range data.ProxyPathSteps {
		if step.PathID == pathID && step.Position >= position {
			continue
		}
		steps = append(steps, step)
		if step.PathID == pathID {
			remainingPathSteps++
		}
	}
	if remainingPathSteps == 0 {
		keepAsDirect, err := s.proxyPathHasRootRoutingRules(ctx, pathID)
		if err != nil {
			return err
		}
		if keepAsDirect {
			for index := range data.ProxyPaths {
				if data.ProxyPaths[index].ID != pathID {
					continue
				}
				data.ProxyPaths[index].Kind = model.ProxyPathKindDirect
				data.ProxyPaths[index].BranchSourceStepID = nil
				break
			}
		}
	}
	// The remaining leading transparent segment may end on a different hop, so
	// mirror normalizeProxyPathProcessingRoles instead of reusing stale flags.
	if err := normalizeProxyPathProcessingRolesInMemory(steps, pathID); err != nil {
		return err
	}
	data.ProxyPathSteps = steps
	resolveRoutingProxyPathNames(&data)
	_, err = core.BuildProxyPathPlansWithLedger(data.ProxyPaths, steps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	return err
}

// normalizeProxyPathProcessingRolesInMemory applies the same derivation as
// normalizeProxyPathProcessingRoles to one path inside a candidate step slice.
func normalizeProxyPathProcessingRolesInMemory(steps []model.ProxyPathStep, pathID int64) error {
	indexes := make([]int, 0, len(steps))
	for i := range steps {
		if steps[i].PathID == pathID {
			indexes = append(indexes, i)
		}
	}
	sort.SliceStable(indexes, func(a, b int) bool {
		left, right := steps[indexes[a]], steps[indexes[b]]
		if left.Position == right.Position {
			return left.ID < right.ID
		}
		return left.Position < right.Position
	})
	processorIndex := -1
	transparentPrefix := true
	for _, index := range indexes {
		mode := steps[index].TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode == model.ProxyPathTransportPortForward {
			if !transparentPrefix {
				return errors.New("端口转发只能位于路径开头；链式代理或隧道之后不能再透明转发")
			}
			processorIndex = index
			continue
		}
		transparentPrefix = false
	}
	for _, index := range indexes {
		steps[index].ProcessingRole = processorIndex >= 0 && index == processorIndex
	}
	return nil
}

// validateEnabledProxyPathPlan rejects enabling a path whose stored steps cannot
// be projected. Step writes already run this check, but a path that was built
// while disabled would otherwise pass its own validation and then fail the
// deployment for every server.
func (s *Server) validateProxyPathCandidateData(ctx context.Context, candidate model.ProxyPath) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	found := false
	if candidate.ID <= 0 {
		candidate.ID = 1
		for _, path := range data.ProxyPaths {
			if path.ID >= candidate.ID {
				candidate.ID = path.ID + 1
			}
		}
	} else {
		for index := range data.ProxyPaths {
			if data.ProxyPaths[index].ID == candidate.ID {
				data.ProxyPaths[index] = candidate
				found = true
				break
			}
		}
	}
	if !found {
		data.ProxyPaths = append(data.ProxyPaths, candidate)
	}
	if err := normalizeProxyPathProcessingRolesInMemory(data.ProxyPathSteps, candidate.ID); err != nil {
		return err
	}
	resolveRoutingProxyPathNames(&data)
	_, err = core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	return err
}

func (s *Server) validateEnabledProxyPathPlan(ctx context.Context, pathID int64) error {
	if err := s.normalizeProxyPathProcessingRoles(ctx, pathID); err != nil {
		return err
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	resolveRoutingProxyPathNames(&data)
	_, err = core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	return err
}

func (s *Server) ensureWARPProfilesForProxyPaths(ctx context.Context) error {
	data, err := s.store.ProxyTopologyData(ctx)
	if err != nil {
		return err
	}
	return s.ensureWARPProfilesForProxyTopology(ctx, data)
}

func (s *Server) ensureWARPProfilesForProxyTopology(ctx context.Context, data store.ProxyTopologyData) error {
	serverIDs, err := core.ProxyPathWARPServerIDs(data.ProxyPaths, data.ProxyPathSteps, data.Inbounds)
	if err != nil {
		return err
	}
	for serverID := range serverIDs {
		if _, err := s.store.EnsureWARPProfileForServer(ctx, serverID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) createProxyPathStepBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		Steps []model.ProxyPathStep `json:"steps"`
	}
	if !decode(w, r, &request) {
		return
	}
	if len(request.Steps) == 0 || len(request.Steps) > 64 {
		fail(w, errors.New("steps must contain 1 to 64 items"), http.StatusBadRequest)
		return
	}
	pathID := request.Steps[0].PathID
	path, err := s.store.GetProxyPath(r.Context(), pathID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	existing, err := s.store.ListProxyPathStepsForPath(r.Context(), pathID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	candidate := append([]model.ProxyPathStep(nil), existing...)
	seenPositions := map[int]bool{}
	maxStepID := int64(0)
	for _, item := range existing {
		seenPositions[item.Position] = true
	}
	allSteps, err := s.store.ListProxyPathSteps(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	for _, item := range allSteps {
		if item.ID > maxStepID {
			maxStepID = item.ID
		}
	}
	for index := range request.Steps {
		step := &request.Steps[index]
		if step.PathID != pathID || step.Position <= 0 || seenPositions[step.Position] {
			fail(w, errors.New("all steps must target the same path with unique positive positions"), http.StatusBadRequest)
			return
		}
		seenPositions[step.Position] = true
		maxStepID++
		step.ID = maxStepID
		if err := s.normalizeProxyPathStepCandidate(r.Context(), path, step); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		candidate = append(candidate, *step)
	}
	if err := s.validateProxyPathServerLoop(r.Context(), path.InboundID, candidate); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if err := normalizeProxyPathProcessingRolesInMemory(candidate, pathID); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	copy(request.Steps, candidate[len(candidate)-len(request.Steps):])
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	filtered := data.ProxyPathSteps[:0]
	for _, item := range data.ProxyPathSteps {
		if item.PathID != pathID {
			filtered = append(filtered, item)
		}
	}
	data.ProxyPathSteps = append(filtered, candidate...)
	resolveRoutingProxyPathNames(&data)
	if _, err := core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if err := s.store.CreateProxyPathSteps(r.Context(), request.Steps); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	stored, err := s.store.ListProxyPathStepsForPath(r.Context(), pathID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusCreated, map[string]any{"proxy_path_steps": publicProxyPathSteps(stored[len(stored)-len(request.Steps):])})
}

func (s *Server) proxyPathSteps(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/batch") {
		s.createProxyPathStepBatch(w, r)
		return
	}
	id := idFromPath(r.URL.Path, "/api/v1/proxy-path-steps/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetProxyPathStep(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"proxy_path_step": publicProxyPathStep(*item)})
			return
		}
		items, err := s.store.ListProxyPathSteps(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"proxy_path_steps": publicProxyPathSteps(items)})
	case http.MethodPost:
		var v model.ProxyPathStep
		if !decode(w, r, &v) {
			return
		}
		if v.Position <= 0 {
			position, err := s.nextProxyPathStepPosition(r.Context(), v.PathID)
			if err != nil {
				fail(w, err, 500)
				return
			}
			v.Position = position
		}
		if err := s.validateProxyPathStep(r.Context(), &v, 0); err != nil {
			fail(w, err, 400)
			return
		}
		var restoredDirectPath *model.ProxyPath
		if v.NodeType != model.ProxyPathStepServerInbound {
			path, err := s.store.GetProxyPath(r.Context(), v.PathID)
			if err != nil {
				fail(w, err, 404)
				return
			}
			if path.Kind == model.ProxyPathKindDirect {
				converted := *path
				converted.Kind = model.ProxyPathKindChain
				converted.BranchSourceStepID = nil
				if err := s.validateProxyPath(r.Context(), &converted); err != nil {
					fail(w, err, 400)
					return
				}
				if err := s.store.UpdateProxyPath(r.Context(), &converted); err != nil {
					fail(w, err, 500)
					return
				}
				restoredDirectPath = path
			}
		}
		restoreDirectPath := func() {
			if restoredDirectPath != nil {
				_ = s.store.UpdateProxyPath(r.Context(), restoredDirectPath)
			}
		}
		if err := s.store.CreateProxyPathStep(r.Context(), &v); err != nil {
			restoreDirectPath()
			fail(w, err, 500)
			return
		}
		topology, err := s.normalizeAndValidateProxyPathData(r.Context(), v.PathID)
		if err != nil {
			_ = s.store.Delete(r.Context(), "proxy_path_steps", v.ID)
			restoreDirectPath()
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), v.PathID)
			fail(w, err, 400)
			return
		}
		if err := s.ensureWARPProfilesForProxyTopology(r.Context(), topology); err != nil {
			_ = s.store.Delete(r.Context(), "proxy_path_steps", v.ID)
			restoreDirectPath()
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), v.PathID)
			fail(w, err, 500)
			return
		}
		if err := s.store.ClearProxyPathBranchSource(r.Context(), v.PathID); err != nil {
			_ = s.store.Delete(r.Context(), "proxy_path_steps", v.ID)
			restoreDirectPath()
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), v.PathID)
			fail(w, err, 500)
			return
		}
		if err := s.reconcileProxyPathNameTemplatesWithData(r.Context(), topology); err != nil {
			_ = s.store.Delete(r.Context(), "proxy_path_steps", v.ID)
			restoreDirectPath()
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), v.PathID)
			fail(w, err, 500)
			return
		}
		if stored, err := s.store.GetProxyPathStep(r.Context(), v.ID); err == nil {
			v = *stored
		}
		auditReq(s, r, "create", "proxy-path-step", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"proxy_path_step": publicProxyPathStep(v)})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetProxyPathStep(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if v.Position <= 0 {
			v.Position = current.Position
		}
		if err := s.validateProxyPathStep(r.Context(), &v, id); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateProxyPathStep(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		topology, err := s.normalizeAndValidateProxyPathData(r.Context(), v.PathID)
		if err != nil {
			_ = s.store.UpdateProxyPathStep(r.Context(), current)
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), current.PathID)
			fail(w, err, 400)
			return
		}
		if err := s.ensureWARPProfilesForProxyTopology(r.Context(), topology); err != nil {
			_ = s.store.UpdateProxyPathStep(r.Context(), current)
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), current.PathID)
			fail(w, err, 500)
			return
		}
		if err := s.store.ClearProxyPathBranchSourcesFromPosition(r.Context(), current.PathID, current.Position); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.ClearProxyPathBranchSource(r.Context(), current.PathID); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.reconcileProxyPathNameTemplatesWithData(r.Context(), topology); err != nil {
			fail(w, err, 500)
			return
		}
		if stored, err := s.store.GetProxyPathStep(r.Context(), v.ID); err == nil {
			v = *stored
		}
		auditReq(s, r, "update", "proxy-path-step", fmt.Sprint(id))
		write(w, 200, map[string]any{"proxy_path_step": publicProxyPathStep(v)})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetProxyPathStep(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		steps, err := s.store.ListProxyPathStepsForPath(r.Context(), current.PathID)
		if err != nil {
			fail(w, err, 500)
			return
		}
		deletedSteps := 0
		for _, step := range steps {
			if step.Position >= current.Position {
				deletedSteps++
			}
		}
		// Verify the projection of the remaining chain before deleting anything.
		// Deleting first would leave the operator with a 400 response and a path
		// that has already lost its steps.
		keepAsDirect := false
		if deletedSteps == len(steps) {
			keepAsDirect, err = s.proxyPathHasRootRoutingRules(r.Context(), current.PathID)
			if err != nil {
				fail(w, err, 500)
				return
			}
		}
		if deletedSteps < len(steps) || keepAsDirect {
			if err := s.validateProxyPathTruncation(r.Context(), current.PathID, current.Position); err != nil {
				fail(w, err, 400)
				return
			}
		}
		if err := s.store.DeleteProxyPathStepsFromPosition(r.Context(), current.PathID, current.Position); err != nil {
			fail(w, err, 500)
			return
		}
		remaining, err := s.store.ListProxyPathStepsForPath(r.Context(), current.PathID)
		if err != nil {
			fail(w, err, 500)
			return
		}
		retainedPath, pathDeleted, err := s.finishProxyPathTruncation(r.Context(), current.PathID, len(remaining) == 0)
		if err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "proxy-path-step", fmt.Sprint(id))
		result := map[string]any{"deleted": true, "deleted_steps": deletedSteps, "path_deleted": pathDeleted}
		if retainedPath != nil {
			result["proxy_path"] = *retainedPath
		}
		write(w, 200, result)
	default:
		method(w)
	}
}

func (s *Server) finishProxyPathTruncation(ctx context.Context, pathID int64, empty bool) (*model.ProxyPath, bool, error) {
	if empty {
		keepAsDirect, err := s.proxyPathHasRootRoutingRules(ctx, pathID)
		if err != nil {
			return nil, false, err
		}
		if !keepAsDirect {
			if err := s.store.DeleteProxyPath(ctx, pathID); err != nil {
				return nil, false, err
			}
			if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
				return nil, false, err
			}
			return nil, true, nil
		}
		path, err := s.store.GetProxyPath(ctx, pathID)
		if err != nil {
			return nil, false, err
		}
		path.Kind = model.ProxyPathKindDirect
		path.BranchSourceStepID = nil
		if err := s.store.UpdateProxyPath(ctx, path); err != nil {
			return nil, false, err
		}
	} else {
		if err := s.normalizeAndValidateProxyPath(ctx, pathID); err != nil {
			return nil, false, err
		}
		if err := s.store.ClearProxyPathBranchSource(ctx, pathID); err != nil {
			return nil, false, err
		}
	}
	if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
		return nil, false, err
	}
	path, err := s.store.GetProxyPath(ctx, pathID)
	if err != nil {
		return nil, false, err
	}
	resolved := s.resolvedProxyPath(ctx, *path)
	return &resolved, false, nil
}

func (s *Server) proxyPathHasRootRoutingRules(ctx context.Context, pathID int64) (bool, error) {
	rules, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return false, err
	}
	for _, rule := range rules {
		if rule.Scope == model.RoutingRuleScopePathStage && rule.ProxyPathID != nil && *rule.ProxyPathID == pathID && rule.StageStepID == nil {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) nextProxyPathStepPosition(ctx context.Context, pathID int64) (int, error) {
	steps, err := s.store.ListProxyPathStepsForPath(ctx, pathID)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, step := range steps {
		if step.Position > max {
			max = step.Position
		}
	}
	return max + 1, nil
}

func (s *Server) validateProxyPathStep(ctx context.Context, v *model.ProxyPathStep, currentID int64) error {
	if v.PathID == 0 {
		return errors.New("path_id required")
	}
	path, err := s.store.GetProxyPath(ctx, v.PathID)
	if err != nil {
		return fmt.Errorf("path_id: %w", err)
	}
	if v.Position <= 0 {
		return errors.New("position must be >= 1")
	}
	if err := s.normalizeProxyPathStepCandidate(ctx, path, v); err != nil {
		return err
	}
	steps, err := s.store.ListProxyPathStepsForPath(ctx, v.PathID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.ID != currentID && step.Position == v.Position {
			return errors.New("same path step position already exists")
		}
	}
	merged := appendProxyPathStep(steps, *v, currentID)
	if path.Kind == model.ProxyPathKindFamilyBranch {
		if err := core.ValidateFamilyBranchTransport(merged); err != nil {
			return err
		}
	}
	if err := s.validateProxyPathServerLoop(ctx, path.InboundID, merged); err != nil {
		return err
	}
	// Branch reuse is a property of the path set, not of one step. It is enforced
	// when a path is created or enabled; repeating it here would reject adding a
	// hop with an error about branch reuse that the operator cannot act on.
	return nil
}

func (s *Server) normalizeProxyPathStepCandidate(ctx context.Context, path *model.ProxyPath, v *model.ProxyPathStep) error {
	if v.TransportMode == "" {
		v.TransportMode = model.ProxyPathTransportSingBox
	}
	switch v.TransportMode {
	case model.ProxyPathTransportSingBox, model.ProxyPathTransportPortForward, model.ProxyPathTransportTunnel:
	default:
		return errors.New("transport_mode must be singbox, port_forward or tunnel")
	}
	v.ConfigJSON = strings.TrimSpace(v.ConfigJSON)
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
	rawConfig := v.ConfigJSON
	var cfg map[string]any
	if err := json.Unmarshal([]byte(v.ConfigJSON), &cfg); err != nil {
		return fmt.Errorf("config_json: %w", err)
	}
	v.ProcessingRole = false // Derived from the leading transparent-forward segment.
	switch v.NodeType {
	case model.ProxyPathStepWARP:
		if v.TransportMode != model.ProxyPathTransportSingBox {
			return errors.New("WARP 只能作为 sing-box 链路出口")
		}
		v.ServerID = nil
		v.InboundID = nil
		v.ExternalOutboundID = nil
		v.ProcessingRole = false
		if path != nil && path.Kind == model.ProxyPathKindFamilyBranch {
			if err := applyFamilyBranchBindConfig(v, cfg); err != nil {
				return err
			}
		} else {
			v.ConfigJSON = "{}"
		}
	case model.ProxyPathStepImported:
		if v.TransportMode != model.ProxyPathTransportSingBox {
			return errors.New("导入节点只能使用 sing-box 出站链，端口转发和隧道需要连接到可控服务器")
		}
		v.InboundID = nil
		v.ServerID = nil
		v.ProcessingRole = false
		if v.ExternalOutboundID == nil || *v.ExternalOutboundID == 0 {
			return errors.New("external_outbound_id required")
		}
		if _, err := s.store.GetExternalOutbound(ctx, *v.ExternalOutboundID); err != nil {
			return fmt.Errorf("external_outbound_id: %w", err)
		}
		if path != nil && path.Kind == model.ProxyPathKindFamilyBranch {
			if err := applyFamilyBranchBindConfig(v, cfg); err != nil {
				return err
			}
		}
	case model.ProxyPathStepServerInbound:
		v.ExternalOutboundID = nil
		if v.InboundID != nil && *v.InboundID != 0 {
			inbound, err := s.store.GetInbound(ctx, *v.InboundID)
			if err != nil {
				return fmt.Errorf("inbound_id: %w", err)
			}
			if err := core.ValidateProxyPathStepInboundBinding(*v, *inbound, rawConfig); err != nil {
				return err
			}
			if v.ServerID != nil && *v.ServerID != 0 && *v.ServerID != inbound.ServerID {
				return errors.New("server_id and inbound_id refer to different servers")
			}
			if v.ServerID == nil || *v.ServerID == 0 {
				serverID := inbound.ServerID
				v.ServerID = &serverID
			}
		} else if v.ServerID == nil || *v.ServerID == 0 {
			return errors.New("server_id or inbound_id required")
		}
		if v.ServerID != nil && *v.ServerID != 0 {
			if _, err := s.store.GetServer(ctx, *v.ServerID); err != nil {
				return fmt.Errorf("server_id: %w", err)
			}
		}
		if (v.InboundID == nil || *v.InboundID == 0) && v.TransportMode != model.ProxyPathTransportPortForward {
			chainFields, err := normalizedProxyPathChainFields(cfg)
			if err != nil {
				return err
			}
			for key, value := range chainFields {
				cfg[key] = value
			}
		}
		switch v.TransportMode {
		case model.ProxyPathTransportTunnel:
			if err := normalizeProxyPathTunnelConfig(v, cfg); err != nil {
				return err
			}
		case model.ProxyPathTransportPortForward:
			if err := normalizeProxyPathForwardConfig(v, cfg); err != nil {
				return err
			}
		default:
			if v.InboundID != nil && *v.InboundID != 0 {
				managed := map[string]any{}
				if path != nil && path.Kind == model.ProxyPathKindFamilyBranch {
					if err := mergeFamilyBranchBindFields(managed, cfg); err != nil {
						return err
					}
				}
				encoded, err := json.Marshal(managed)
				if err != nil {
					return err
				}
				v.ConfigJSON = string(encoded)
			} else if err := normalizeProxyPathChainConfig(v, cfg); err != nil {
				return err
			}
			if path != nil && path.Kind == model.ProxyPathKindFamilyBranch && (v.InboundID == nil || *v.InboundID == 0) {
				var managed map[string]any
				if err := json.Unmarshal([]byte(v.ConfigJSON), &managed); err != nil {
					return err
				}
				if err := mergeFamilyBranchBindFields(managed, cfg); err != nil {
					return err
				}
				encoded, err := json.Marshal(managed)
				if err != nil {
					return err
				}
				v.ConfigJSON = string(encoded)
			}
		}
	default:
		return errors.New("node_type must be imported, server_inbound or warp")
	}
	return nil
}

// normalizeProxyPathChainConfig rebuilds a sing-box step's config from an
// allowlist. Unknown keys are dropped so a client cannot inject a value that a
// generator reads without validation, such as internal_port.
func normalizeProxyPathChainConfig(v *model.ProxyPathStep, cfg map[string]any) error {
	managed, err := normalizedProxyPathChainFields(cfg)
	if err != nil {
		return err
	}
	b, err := json.Marshal(managed)
	if err != nil {
		return err
	}
	v.ConfigJSON = string(b)
	return nil
}

func normalizedProxyPathChainFields(cfg map[string]any) (map[string]any, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	chain, err := core.ParseProxyPathChainConfig(string(b))
	if err != nil {
		return nil, err
	}
	managed := map[string]any{"chain_protocol": string(chain.Protocol)}
	switch chain.Protocol {
	case model.ProtocolSS:
		managed["chain_method"] = chain.Method
	case model.ProtocolVLESS:
		managed["reality_handshake_server"] = chain.RealityHandshakeServer
		managed["reality_handshake_port"] = chain.RealityHandshakePort
	case model.ProtocolMieru, model.ProtocolSocks:
	default:
		return nil, errors.New("链路协议必须是 shadowsocks、vless、mieru 或 socks")
	}
	return managed, nil
}

// normalizeProxyPathForwardConfig rebuilds a transparent forward step's config
// from an allowlist and validates the operator-selectable fields. internal_port
// is deliberately not accepted: the generated processing listener must stay
// under Controller port allocation so plan and core config agree.
func normalizeProxyPathForwardConfig(v *model.ProxyPathStep, cfg map[string]any) error {
	managed := map[string]any{}
	if raw, ok := cfg["listen_ip"]; ok {
		listenIP := strings.TrimSpace(fmt.Sprint(raw))
		if listenIP != "" && listenIP != "<nil>" {
			if _, err := netip.ParseAddr(listenIP); err != nil {
				return fmt.Errorf("listen_ip: %w", err)
			}
			managed["listen_ip"] = listenIP
		}
	}
	b, err := json.Marshal(managed)
	if err != nil {
		return err
	}
	v.ConfigJSON = string(b)
	return nil
}

func normalizeProxyPathTunnelConfig(v *model.ProxyPathStep, cfg map[string]any) error {
	for _, key := range []string{"client_private_key", "client_public_key", "source_private_key", "source_public_key", "target_private_key", "target_public_key"} {
		delete(cfg, key)
	}
	typeName := strings.ToLower(strings.TrimSpace(fmt.Sprint(cfg["type"])))
	if typeName == "" {
		typeName = string(model.TunnelTypeSSH)
		cfg["type"] = typeName
	}
	switch model.TunnelType(typeName) {
	case model.TunnelTypeSSH:
		port := intFromAnyController(cfg["ssh_port"])
		if port <= 0 || port > 65535 {
			return errors.New("目标端隧道服务端口必须是 1 到 65535 的整数")
		}
		managed := map[string]any{
			"type":         string(model.TunnelTypeSSH),
			"managed_pair": true,
			"ssh_port":     port,
		}
		if v.InboundID == nil || *v.InboundID == 0 {
			chainFields, err := normalizedProxyPathChainFields(cfg)
			if err != nil {
				return err
			}
			for key, value := range chainFields {
				managed[key] = value
			}
		}
		cfg = managed
	case model.TunnelTypeWireGuard:
		keepalive := intFromAnyController(cfg["persistent_keepalive"])
		if _, ok := cfg["persistent_keepalive"]; !ok {
			keepalive = 25
		}
		if keepalive < 0 || keepalive > 65535 {
			return errors.New("WireGuard persistent_keepalive 必须是 0 到 65535 的整数")
		}
		managed := map[string]any{
			"type":                 string(model.TunnelTypeWireGuard),
			"managed_pair":         true,
			"persistent_keepalive": keepalive,
		}
		if v.InboundID == nil || *v.InboundID == 0 {
			chainFields, err := normalizedProxyPathChainFields(cfg)
			if err != nil {
				return err
			}
			for key, value := range chainFields {
				managed[key] = value
			}
		}
		cfg = managed
	default:
		return errors.New("隧道类型必须是 ssh 或 wireguard")
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	v.ConfigJSON = string(b)
	return nil
}

// normalizeEnabledProxyPathProcessingRoles refreshes derived processing roles for
// every enabled path. Disabled paths are skipped on purpose: Core validation and
// plan building both exempt them, so a half-configured disabled branch must not
// be able to fail a page load or block a deployment for every server.
func (s *Server) normalizeEnabledProxyPathProcessingRoles(ctx context.Context) error {
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		if err := s.normalizeProxyPathProcessingRoles(ctx, path.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) normalizeProxyPathProcessingRoles(ctx context.Context, pathID int64) error {
	steps, err := s.store.ListProxyPathStepsForPath(ctx, pathID)
	if err != nil {
		return err
	}
	stored := make([]bool, len(steps))
	for i := range steps {
		stored[i] = steps[i].ProcessingRole
	}
	if err := normalizeProxyPathProcessingRolesInMemory(steps, pathID); err != nil {
		return err
	}
	for i := range steps {
		if steps[i].ProcessingRole == stored[i] {
			continue
		}
		if err := s.store.UpdateProxyPathStep(ctx, &steps[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) normalizeAndValidateProxyPath(ctx context.Context, pathID int64) error {
	_, err := s.normalizeAndValidateProxyPathData(ctx, pathID)
	return err
}

func (s *Server) normalizeAndValidateProxyPathData(ctx context.Context, pathID int64) (store.ProxyTopologyData, error) {
	if err := s.normalizeProxyPathProcessingRoles(ctx, pathID); err != nil {
		return store.ProxyTopologyData{}, err
	}
	data, err := s.store.ProxyTopologyData(ctx)
	if err != nil {
		return store.ProxyTopologyData{}, err
	}
	data.ProxyPaths = core.ResolveProxyPathNames(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds)
	_, err = core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	return data, err
}

func (s *Server) proxyPathNameData(ctx context.Context) ([]model.ProxyPath, []model.ProxyPathStep, []model.Server, []model.Inbound, []model.ExternalOutbound, error) {
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	steps, err := s.store.ListProxyPathSteps(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	externals, err := s.store.ListExternalOutbounds(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	results, err := s.store.ListProxyPathEgressResults(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	paths, externals = core.ResolveProxyPathExitRegions(paths, steps, servers, inbounds, externals, results)
	return paths, steps, servers, inbounds, externals, nil
}

func (s *Server) resolvedExternalOutbounds(ctx context.Context) ([]model.ExternalOutbound, error) {
	_, _, _, _, externals, err := s.proxyPathNameData(ctx)
	return externals, err
}

func (s *Server) resolvedExternalOutbound(ctx context.Context, fallback model.ExternalOutbound) model.ExternalOutbound {
	items, err := s.resolvedExternalOutbounds(ctx)
	if err != nil {
		return fallback
	}
	for _, item := range items {
		if item.ID == fallback.ID {
			return item
		}
	}
	return fallback
}

func (s *Server) resolvedProxyPath(ctx context.Context, fallback model.ProxyPath) model.ProxyPath {
	paths, steps, servers, inbounds, externals, err := s.proxyPathNameData(ctx)
	if err != nil {
		return fallback
	}
	resolved, ok := proxyPathByID(core.ResolveProxyPathNames(paths, steps, servers, inbounds, externals), fallback.ID)
	if !ok {
		return fallback
	}
	return resolved
}

func (s *Server) reconcileProxyPathNameTemplates(ctx context.Context) error {
	data, err := s.store.ProxyTopologyData(ctx)
	if err != nil {
		return err
	}
	return s.reconcileProxyPathNameTemplatesWithData(ctx, data)
}

func (s *Server) reconcileProxyPathNameTemplatesWithData(ctx context.Context, data store.ProxyTopologyData) error {
	paths, externals := core.ResolveProxyPathExitRegions(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds, data.ProxyPathEgressResults)
	for index := range paths {
		path := &paths[index]
		if path.NameMode != model.ProxyPathNameCustom || core.ProxyPathNameTemplateIsValid(*path, data.ProxyPathSteps, data.Servers, data.Inbounds, externals) {
			continue
		}
		if err := s.store.ResetProxyPathNameTemplate(ctx, path.ID); err != nil {
			return err
		}
	}
	return nil
}

func resolveRoutingProxyPathNames(data *store.FullRoutingConfig) {
	data.ProxyPaths = core.ResolveProxyPathNames(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds)
}

func proxyPathByID(paths []model.ProxyPath, id int64) (model.ProxyPath, bool) {
	for _, path := range paths {
		if path.ID == id {
			return path, true
		}
	}
	return model.ProxyPath{}, false
}

func proxyPathPlanByID(plans []model.ProxyPathPlan, id int64) (model.ProxyPathPlan, bool) {
	for _, plan := range plans {
		if plan.PathID == id {
			return plan, true
		}
	}
	return model.ProxyPathPlan{}, false
}

func proxyPathStepsForPath(steps []model.ProxyPathStep, pathID int64) []model.ProxyPathStep {
	out := make([]model.ProxyPathStep, 0)
	for _, step := range steps {
		if step.PathID == pathID {
			out = append(out, step)
		}
	}
	return out
}

func appendProxyPathStep(steps []model.ProxyPathStep, next model.ProxyPathStep, currentID int64) []model.ProxyPathStep {
	out := make([]model.ProxyPathStep, 0, len(steps)+1)
	for _, step := range steps {
		if step.ID == currentID {
			continue
		}
		out = append(out, step)
	}
	return append(out, next)
}

func (s *Server) validateProxyPathServerLoop(ctx context.Context, rootInboundID int64, steps []model.ProxyPathStep) error {
	seen := map[int64]bool{}
	if rootInboundID > 0 {
		root, err := s.store.GetInbound(ctx, rootInboundID)
		if err != nil {
			return err
		}
		seen[root.ServerID] = true
	}
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Position == steps[j].Position {
			return steps[i].ID < steps[j].ID
		}
		return steps[i].Position < steps[j].Position
	})
	for _, step := range steps {
		if step.NodeType != model.ProxyPathStepServerInbound {
			continue
		}
		serverID := int64(0)
		if step.ServerID != nil && *step.ServerID != 0 {
			serverID = *step.ServerID
		} else if step.InboundID != nil && *step.InboundID != 0 {
			inbound, err := s.store.GetInbound(ctx, *step.InboundID)
			if err != nil {
				return err
			}
			serverID = inbound.ServerID
		}
		if serverID == 0 {
			continue
		}
		if seen[serverID] {
			return errors.New("代理路径不能重复经过同一台服务器")
		}
		seen[serverID] = true
	}
	return nil
}

func (s *Server) validateInboundPathReuse(ctx context.Context, inboundID, currentPathID int64) error {
	inbound, err := s.store.GetInbound(ctx, inboundID)
	if err != nil {
		return err
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return err
	}
	count := 1
	for _, path := range paths {
		if path.ID != currentPathID && path.Enabled && path.InboundID == inboundID {
			count++
		}
	}
	if count > 1 && !core.InboundSupportsMultipleUsers(*inbound) {
		return errors.New("该入口协议不支持分支复用，请只保留一条代理路径")
	}
	return nil
}

func proxyPathPlanSummary(path model.ProxyPath, steps []model.ProxyPathStep) map[string]any {
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Position == steps[j].Position {
			return steps[i].ID < steps[j].ID
		}
		return steps[i].Position < steps[j].Position
	})
	items := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		items = append(items, map[string]any{
			"id":                   step.ID,
			"position":             step.Position,
			"node_type":            step.NodeType,
			"transport_mode":       firstNonEmptyString(string(step.TransportMode), string(model.ProxyPathTransportSingBox)),
			"processing_role":      step.ProcessingRole,
			"server_id":            step.ServerID,
			"inbound_id":           step.InboundID,
			"external_outbound_id": step.ExternalOutboundID,
		})
	}
	return map[string]any{"path_id": path.ID, "kind": path.Kind, "name": path.Name, "inbound_id": path.InboundID, "enabled": path.Enabled, "steps": items, "warnings": []string{}}
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s *Server) warpProfiles(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/warp-profiles/")
	switch r.Method {
	case http.MethodGet:
		if id > 0 {
			item, err := s.store.GetWARPProfile(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"warp_profile": publicWARPProfile(*item)})
			return
		}
		items, err := s.store.ListWARPProfiles(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"warp_profiles": publicWARPProfiles(items)})
	default:
		method(w)
	}
}

func publicWARPProfiles(items []model.WARPProfile) []model.WARPProfile {
	out := make([]model.WARPProfile, len(items))
	for i := range items {
		out[i] = publicWARPProfile(items[i])
	}
	return out
}

// publicWARPProfile redacts the WireGuard private key from API responses. A
// ready profile's config_json holds live key material, and the Web UI only
// needs to know whether a configuration exists.
func publicWARPProfile(item model.WARPProfile) model.WARPProfile {
	item.ConfigJSON = redactWARPConfigJSON(item.ConfigJSON)
	return item
}

// restoreWARPPrivateKey carries the stored WireGuard private key into an
// incoming configuration whose key is still the redaction placeholder.
func restoreWARPPrivateKey(incoming, stored string) string {
	var next map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(incoming)), &next); err != nil {
		return incoming
	}
	key, _ := next["private_key"].(string)
	if strings.TrimSpace(key) != "" && strings.Trim(key, "*") != "" {
		return incoming
	}
	var previous map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stored)), &previous); err != nil {
		return incoming
	}
	storedKey, _ := previous["private_key"].(string)
	if strings.TrimSpace(storedKey) == "" {
		return incoming
	}
	next["private_key"] = storedKey
	delete(next, "private_key_configured")
	out, err := json.Marshal(next)
	if err != nil {
		return incoming
	}
	return string(out)
}

func redactWARPConfigJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return `{"configured":true}`
	}
	if key, _ := cfg["private_key"].(string); strings.TrimSpace(key) != "" {
		cfg["private_key"] = "********"
		cfg["private_key_configured"] = true
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return `{"configured":true}`
	}
	return string(out)
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/users/")
	parts := pathParts(r.URL.Path, "/api/v1/users/")
	if r.Method != http.MethodGet && id > 0 {
		if err := s.requireUserMutationAccess(r.Context(), currentRole(r), id); err != nil {
			status := http.StatusForbidden
			if !errors.Is(err, errAdministratorAccountManagedByOperator) {
				status = http.StatusInternalServerError
			}
			fail(w, err, status)
			return
		}
	}
	if len(parts) >= 2 && parts[1] == "devices" {
		s.userDevices(w, r, id, parts[2:])
		return
	}
	if len(parts) == 3 && parts[1] == "sessions" && parts[2] == "revoke" {
		s.revokeUserSessions(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "subscription-token" {
		s.userSubscriptionToken(w, r, id, parts[2])
		return
	}
	if len(parts) == 2 && parts[1] == "traffic-ledger" {
		s.userTrafficLedger(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "subscription-age" {
		s.userSubscriptionAge(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "subscription-custom-path" {
		s.userSubscriptionCustomPath(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "subscription-custom-path-policy" {
		s.userSubscriptionCustomPathPolicy(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "nodes" {
		s.userEffectiveNodes(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "subscription-access" && parts[2] == "resume" {
		s.resumeUserSubscriptionAccess(w, r, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetUser(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"user": item})
			return
		}
		items, err := s.store.ListUsers(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		items = s.withTrafficStatus(r.Context(), items)
		write(w, 200, map[string]any{"users": items})
	case http.MethodPost:
		var req struct {
			model.User
			Password           string `json:"password"`
			LegacyProxyEnabled *bool  `json:"legacy_proxy_enabled"`
		}
		if !decode(w, r, &req) {
			return
		}
		u := req.User
		u.LegacyProxyEnabled = true
		if req.LegacyProxyEnabled != nil {
			u.LegacyProxyEnabled = *req.LegacyProxyEnabled
		}
		u.LegacyProxyEnabledSet = true
		if u.Username == "" {
			fail(w, errors.New("username required"), 400)
			return
		}
		if u.Role == "" {
			u.Role = model.RoleViewer
		}
		if err := requireAssignedRoleAccess(currentRole(r), u.Role); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if u.Status == "" {
			u.Status = "active"
		}
		generatedPassword := ""
		if req.Password == "" {
			password, err := security.RandomToken(12)
			if err != nil {
				fail(w, err, 500)
				return
			}
			req.Password = password
			generatedPassword = password
		} else if len(req.Password) < 8 {
			fail(w, errors.New("password must be at least 8 characters"), 400)
			return
		}
		pass, err := security.HashPassword(req.Password)
		if err != nil {
			fail(w, err, 500)
			return
		}
		u.PasswordHash = pass
		if u.ProxyUUID == "" {
			u.ProxyUUID, err = security.RandomUUID()
			if err != nil {
				fail(w, err, 500)
				return
			}
		}
		if u.ProxyPassword == "" {
			u.ProxyPassword, err = security.RandomToken(18)
			if err != nil {
				fail(w, err, 500)
				return
			}
		}
		if u.SubscriptionToken == "" {
			u.SubscriptionToken, err = security.RandomToken(24)
			if err != nil {
				fail(w, err, 500)
				return
			}
		}
		if err := validateUser(&u); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateUser(r.Context(), &u); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				fail(w, errors.New("用户名已被占用"), http.StatusConflict)
				return
			}
			fail(w, err, 500)
			return
		}
		if u.Role != model.RoleNone {
			groupKey := store.UserGroupSystemUsers
			if u.Role == model.RoleAdmin {
				groupKey = store.UserGroupSystemAdmins
			}
			if err := s.store.AssignUserToBuiltinGroup(r.Context(), u.ID, groupKey); err != nil {
				fail(w, err, 500)
				return
			}
		}
		if err := s.queueCoreConfigRefreshForUser(r.Context(), u.ID, "user_created"); err != nil {
			logConfigurationError("queue core config for user create", err)
		}
		resp := map[string]any{"user": u}
		if generatedPassword != "" {
			resp["generated_password"] = generatedPassword
		}
		write(w, 201, resp)
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetUser(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		protected, err := s.store.IsBootstrapAdmin(r.Context(), id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		var req struct {
			model.User
			Password string `json:"password"`
		}
		req.User = *current
		if !decode(w, r, &req) {
			return
		}
		u := req.User
		u.ID = id
		mergeUserPatch(&u, current)
		if err := requireAssignedRoleAccess(currentRole(r), u.Role); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
		if protected {
			u.Role = model.RoleAdmin
			u.Status = "active"
		}
		if req.Password != "" {
			if len(req.Password) < 8 {
				fail(w, errors.New("password must be at least 8 characters"), 400)
				return
			}
			h, err := security.HashPassword(req.Password)
			if err != nil {
				fail(w, err, 500)
				return
			}
			u.PasswordHash = h
		}
		if err := validateUser(&u); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateUser(r.Context(), &u); err != nil {
			fail(w, err, 500)
			return
		}
		s.syncUserChange(r.Context(), *current, u)
		revokeSessions := req.Password != "" ||
			(current.Status == "active" && u.Status != "active") ||
			(current.Role != u.Role && roleAllows(current.Role, u.Role))
		if revokeSessions {
			if _, err := s.store.BumpSessionVersion(r.Context(), u.ID); err != nil {
				fail(w, err, 500)
				return
			}
		}
		write(w, 200, map[string]any{"user": u})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if protected, err := s.store.IsBootstrapAdmin(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		} else if protected {
			fail(w, errors.New("初始管理员账号不允许删除"), 400)
			return
		}
		if _, err := s.store.GetUser(r.Context(), id); err != nil {
			fail(w, err, 404)
			return
		}
		if err := s.store.DeleteUserGroupMembersForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteSubscriptionTokenPolicyForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteOneTimeSubscriptionTokensForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteSubscriptionAgeForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteNotificationDataForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		serverIDs, acctErr := s.userAccountingServerIDs(r.Context(), id)
		if acctErr != nil {
			logConfigurationError("user accounting servers before delete", acctErr)
		}
		err := s.store.Delete(r.Context(), "users", id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.queueCoreConfigRefreshForServers(r.Context(), serverIDs, "user_deleted"); err != nil {
			logConfigurationError("queue core config for user delete", err)
		}
		auditReq(s, r, "delete", "user", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) revokeUserSessions(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if id == 0 {
		fail(w, errors.New("missing id"), http.StatusBadRequest)
		return
	}
	user, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	if _, err := s.store.BumpSessionVersion(r.Context(), user.ID); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "revoke", "user-sessions", fmt.Sprint(user.ID))
	write(w, http.StatusOK, map[string]any{"ok": true, "session_revoked": true})
}

func (s *Server) withTrafficStatus(ctx context.Context, users []model.User) []model.User {
	groups, _ := s.store.ListUserGroups(ctx)
	members, _ := s.store.ListUserGroupMembers(ctx)
	userPolicies, _ := s.userPlanPolicies(ctx, users)
	settings, _ := s.store.ListSettings(ctx)
	loc := trafficLocation(settings)
	for i := range users {
		users[i].Protected, _ = s.store.IsBootstrapAdmin(ctx, users[i].ID)
		limit, okLimit := userPolicies[users[i].ID]
		if !okLimit {
			limit = defaultUserLimitPolicy(users[i])
		}
		periodKey, start, end, err := s.resolvedTrafficWindow(ctx, users[i].ID, time.Now(), limit, loc)
		if err != nil {
			continue
		}
		period, err := s.store.EnsureTrafficPeriod(ctx, users[i].ID, periodKey, start, end, limit.TrafficLimitBytes)
		if err != nil {
			continue
		}
		users[i].TrafficUsedBytes = period.Upload + period.Download
		users[i].TrafficPeriodKey = period.PeriodKey
		users[i].TrafficPeriodEnd = period.EndsAt.Format(time.RFC3339Nano)
		users[i].TrafficQuotaState = period.State
	}
	_ = s.enrichSubscriptionCustomPaths(ctx, users, groups, members)
	return users
}

func (s *Server) userSubscriptionToken(w http.ResponseWriter, r *http.Request, id int64, action string) {
	user, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	switch action {
	case "one-time":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		if user.Status != "active" {
			fail(w, errors.New("active user required for a one-time subscription link"), 400)
			return
		}
		if user.SubscriptionSuspended {
			fail(w, errors.New("subscription access is suspended"), http.StatusConflict)
			return
		}
		token, err := security.RandomToken(24)
		if err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.CreateOneTimeSubscriptionToken(r.Context(), id, token); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "subscription-token", fmt.Sprint(id)+":one-time")
		write(w, 201, map[string]any{"subscription_token": token, "burn_after_read": true})
	case "rotate":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		token, err := security.RandomToken(24)
		if err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.UpdateUserSubscriptionToken(r.Context(), id, token); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "rotate", "subscription-token", fmt.Sprint(id))
		write(w, 200, map[string]any{"subscription_token": token})
	case "revoke":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		if err := s.store.RevokeSubscriptionCredentials(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "revoke", "subscription-token", fmt.Sprint(id))
		write(w, 200, map[string]any{"subscription_token": ""})
	case "policy":
		if r.Method != http.MethodPatch {
			method(w)
			return
		}
		var req struct {
			BurnAfterRead bool `json:"burn_after_read"`
		}
		if !decode(w, r, &req) {
			return
		}
		if err := s.store.SetUserSubscriptionBurnAfterRead(r.Context(), id, req.BurnAfterRead); err != nil {
			fail(w, err, 500)
			return
		}
		user, err := s.store.GetUser(r.Context(), id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "subscription-token", fmt.Sprint(id))
		write(w, 200, map[string]any{"user": user})
	default:
		fail(w, errors.New("unsupported subscription-token action"), 404)
	}
}

func (s *Server) userSubscriptionAge(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	if _, err := s.store.GetUser(r.Context(), id); err != nil {
		fail(w, err, 404)
		return
	}
	var req struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
	}
	if !decode(w, r, &req) {
		return
	}
	publicKey := strings.TrimSpace(req.PublicKey)
	if publicKey != "" {
		_, canonical, err := parseSubscriptionAgeRecipient(publicKey)
		if err != nil {
			fail(w, err, 400)
			return
		}
		publicKey = canonical
	}
	if req.Enabled && publicKey == "" {
		fail(w, errSubscriptionAgeKeyRequired, 400)
		return
	}
	if err := s.store.SetUserSubscriptionAge(r.Context(), id, req.Enabled, publicKey); err != nil {
		fail(w, err, 500)
		return
	}
	user, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "update", "subscription-age", fmt.Sprint(id))
	write(w, 200, map[string]any{"user": user})
}

func validateUser(u *model.User) error {
	if u == nil || strings.TrimSpace(u.Username) == "" {
		return errors.New("username required")
	}
	u.Nickname = strings.TrimSpace(u.Nickname)
	if len([]rune(u.Nickname)) > 40 {
		return errors.New("nickname must be at most 40 characters")
	}
	switch u.Role {
	case model.RoleAdmin, model.RoleOperator, model.RoleViewer, model.RoleNone:
	default:
		return fmt.Errorf("invalid role %q", u.Role)
	}
	switch u.Status {
	case "active", "disabled":
	default:
		return fmt.Errorf("invalid user status %q", u.Status)
	}
	if u.ProxyUUID != "" && !security.ValidUUID(u.ProxyUUID) {
		return errors.New("proxy_uuid must be a valid UUID")
	}
	if u.SpeedLimitMbps < -1 || u.TrafficLimitBytes < -1 || u.TrafficUsedBytes < 0 || u.DeviceLimit < 0 {
		return errors.New("personal limits must be -1, 0, or positive; device limit and traffic counters must be >= 0")
	}
	u.TrafficResetMode = normalizeControllerTrafficResetMode(u.TrafficResetMode)
	u.TrafficResetDay = normalizeControllerTrafficResetDay(u.TrafficResetDay)
	if strings.TrimSpace(u.SubscriptionAgePublicKey) != "" {
		_, canonical, err := parseSubscriptionAgeRecipient(u.SubscriptionAgePublicKey)
		if err != nil {
			return err
		}
		u.SubscriptionAgePublicKey = canonical
	}
	if u.SubscriptionAgeEnabled && u.SubscriptionAgePublicKey == "" {
		return errSubscriptionAgeKeyRequired
	}
	return nil
}

func mergeUserPatch(dst *model.User, current *model.User) {
	if dst.Username == "" {
		dst.Username = current.Username
	}
	if dst.Role == "" {
		dst.Role = current.Role
	}
	if dst.Status == "" {
		dst.Status = current.Status
	}
	if dst.ProxyUUID == "" {
		dst.ProxyUUID = current.ProxyUUID
	}
	if dst.ProxyPassword == "" {
		dst.ProxyPassword = current.ProxyPassword
	}
	if dst.SubscriptionToken == "" {
		dst.SubscriptionToken = current.SubscriptionToken
	}
	if dst.PasswordHash == "" {
		dst.PasswordHash = current.PasswordHash
	}
	if dst.TrafficResetMode == "" {
		dst.TrafficResetMode = current.TrafficResetMode
	}
	if dst.TrafficResetDay == 0 {
		dst.TrafficResetDay = current.TrafficResetDay
	}
}

func (s *Server) dnsLists(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/dns-lists/")
	if r.Method == http.MethodPost {
		parts := pathParts(r.URL.Path, "/api/v1/dns-lists/")
		if len(parts) == 2 && parts[1] == "set-default" {
			if id == 0 {
				fail(w, errors.New("missing id"), 400)
				return
			}
			item, err := s.store.SetDefaultDNSList(r.Context(), id)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					fail(w, err, 404)
					return
				}
				if strings.Contains(err.Error(), "cannot be set as default") {
					fail(w, err, 400)
					return
				}
				fail(w, err, 500)
				return
			}
			auditReq(s, r, "set_default", "dns_list", fmt.Sprint(id))
			write(w, 200, map[string]any{"dns_list": item})
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetDNSList(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"dns_list": item})
			return
		}
		items, err := s.store.ListDNSLists(r.Context(), false)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"dns_lists": items})
	case http.MethodPost:
		var v model.DNSList
		if !decode(w, r, &v) {
			return
		}
		v.ID = 0
		v.Revision = 1
		v.Protected = false
		v.Enabled = true
		if err := core.ValidateDNSList(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateDNSList(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "dns_list", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"dns_list": v})
	case http.MethodPut:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetDNSList(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		var v model.DNSList
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		v.Kind = current.Kind
		v.Protected = current.Protected
		if current.Protected {
			v.Enabled = true
		}
		if err := core.ValidateDNSList(v); err != nil {
			fail(w, err, 400)
			return
		}
		changed, err := s.store.UpdateDNSList(r.Context(), &v)
		if err != nil {
			fail(w, err, 409)
			return
		}
		if changed {
			s.queuePeriodicDNSBenchmarksForList(r.Context(), v)
		}
		auditReq(s, r, "update", "dns_list", fmt.Sprint(v.ID))
		write(w, 200, map[string]any{"dns_list": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		err := s.store.DeleteDNSList(r.Context(), id)
		if err != nil {
			fail(w, err, 409)
			return
		}
		auditReq(s, r, "delete", "dns_list", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) snellProfiles(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/snell-profiles/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetSnellProfile(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"snell_profile": item})
			return
		}
		items, err := s.store.ListSnellProfiles(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"snell_profiles": items})
	case http.MethodPost:
		var v model.SnellProfile
		if !decode(w, r, &v) {
			return
		}
		v.ID = 0
		v.Builtin = false
		if strings.TrimSpace(v.Name) == "" {
			fail(w, errors.New("name required"), 400)
			return
		}
		if err := validateSnellProfile(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateSnellProfile(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "snell_profile", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"snell_profile": v})
	case http.MethodPut:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetSnellProfile(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		var v model.SnellProfile
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		v.Builtin = current.Builtin
		v.CreatedAt = current.CreatedAt
		if strings.TrimSpace(v.Name) == "" {
			v.Name = current.Name
		}
		if v.Enabled == current.Enabled && v.Enabled == false && v.Builtin {
			v.Enabled = true
		}
		if err := validateSnellProfile(v); err != nil {
			fail(w, err, 400)
			return
		}
		changed, err := s.store.UpdateSnellProfile(r.Context(), &v)
		if err != nil {
			fail(w, err, 409)
			return
		}
		v.UsageCount = current.UsageCount
		v.UpdatedAt = current.UpdatedAt
		if changed && v.UsageCount > 0 {
			// 参数变更影响引用入站的生成配置；部署对账时 Controller 会携带
			// 最新参数，因此无需在此额外触发，仅记录供运营知晓。
			auditReq(s, r, "update_profile_affects_inbounds", "snell_profile", fmt.Sprintf("%d (%d inbounds)", v.ID, v.UsageCount))
		}
		auditReq(s, r, "update", "snell_profile", fmt.Sprint(v.ID))
		write(w, 200, map[string]any{"snell_profile": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteSnellProfile(r.Context(), id); err != nil {
			fail(w, err, 409)
			return
		}
		auditReq(s, r, "delete", "snell_profile", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) nodePresets(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/restore-system") {
		s.nodePresetsRestoreSystem(w, r)
		return
	}
	id := idFromPath(r.URL.Path, "/api/v1/node-presets/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetNodePreset(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"node_preset": item})
			return
		}
		items, err := s.store.ListNodePresets(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"node_presets": items})
	case http.MethodPost:
		var v model.NodePreset
		if !decode(w, r, &v) {
			return
		}
		v.ID = 0
		v.Builtin = false
		v.Enabled = true
		if err := store.NormalizeNodePreset(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateNodePreset(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "node_preset", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"node_preset": v})
	case http.MethodPut:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetNodePreset(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		var v model.NodePreset
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		v.Builtin = current.Builtin
		v.CreatedAt = current.CreatedAt
		if strings.TrimSpace(v.Name) == "" {
			v.Name = current.Name
		}
		if strings.TrimSpace(v.Kind) == "" {
			v.Kind = current.Kind
		}
		if strings.TrimSpace(v.Protocol) == "" {
			v.Protocol = current.Protocol
		}
		if v.ConfigJSON == "" {
			v.ConfigJSON = current.ConfigJSON
		}
		if v.DefaultPort == 0 {
			v.DefaultPort = current.DefaultPort
		}
		if err := s.store.UpdateNodePreset(r.Context(), &v); err != nil {
			fail(w, err, 409)
			return
		}
		v.UsageCount = current.UsageCount
		auditReq(s, r, "update", "node_preset", fmt.Sprint(v.ID))
		write(w, 200, map[string]any{"node_preset": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteNodePreset(r.Context(), id); err != nil {
			fail(w, err, 409)
			return
		}
		auditReq(s, r, "delete", "node_preset", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

// nodePresetsRestoreSystem resets every builtin node preset to the canonical
// system template. Row IDs are preserved so inbound references stay valid.
func (s *Server) nodePresetsRestoreSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	restored, err := s.store.RestoreBuiltinNodePresets(r.Context())
	if err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	items, err := s.store.ListNodePresets(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "restore", "node_preset", "system")
	write(w, 200, map[string]any{"restored": restored, "node_presets": items})
}

func validateSnellProfile(v model.SnellProfile) error {
	if v.Version != core.SnellVersionV4 && v.Version != core.SnellVersionV6 {
		return fmt.Errorf("unsupported snell version %d", v.Version)
	}
	if psk := strings.TrimSpace(v.PSK); psk != "" {
		switch v.Version {
		case core.SnellVersionV6:
			if len([]byte(psk)) < 12 || len([]byte(psk)) > 255 {
				return errors.New("snell v6 psk must be between 12 and 255 bytes")
			}
		default:
			if len([]byte(psk)) < 8 {
				return errors.New("snell psk must be at least 8 characters")
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(v.ObfsMode)) {
	case "", "none":
		v.ObfsMode = "none"
	case "http":
		v.ObfsMode = "http"
	default:
		return fmt.Errorf("unsupported snell obfs_mode %q (only none or http)", v.ObfsMode)
	}
	if v.Version == core.SnellVersionV6 && v.ObfsMode != "none" {
		return errors.New("snell v6 does not support obfs_mode")
	}
	// obfs_host is optional; sing-box defaults it to bing.com for http obfs.
	switch strings.ToLower(strings.TrimSpace(v.Mode)) {
	case "", "default":
		v.Mode = "default"
	case "unshaped", "unsafe-raw":
		v.Mode = strings.ToLower(strings.TrimSpace(v.Mode))
	default:
		return fmt.Errorf("unsupported snell v6 mode %q", v.Mode)
	}
	return nil
}

func (s *Server) queuePeriodicDNSBenchmarksForList(ctx context.Context, list model.DNSList) {
	policies, err := s.store.ListServerDNSPolicies(ctx)
	if err != nil {
		return
	}
	for _, policy := range policies {
		if policy.AutoTest != model.DNSAutoTestPeriodic || list.Kind == model.DNSListEncrypted && policy.EncryptedListID != list.ID || list.Kind == model.DNSListBootstrap && policy.BootstrapListID != list.ID {
			continue
		}
		server, err := s.store.GetServer(ctx, policy.ServerID)
		if err != nil {
			continue
		}
		encrypted, bootstrap, err := s.dnsPolicyLists(ctx, policy)
		if err != nil {
			continue
		}
		plan, err := core.DNSBenchmarkPlanForPolicy(time.Now().UnixNano(), policy, encrypted, *bootstrap, core.EffectiveIPStack(*server), model.DNSAutoTestPeriodic, "")
		if err != nil {
			continue
		}
		_, _ = s.queueAgentTask(ctx, policy.ServerID, model.AgentTaskTypeBenchmarkDNS, plan, plan.Version)
	}
}

func (s *Server) portForwards(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/v1/port-forwards/") {
		parts := pathParts(r.URL.Path, "/api/v1/port-forwards/")
		if len(parts) == 2 && parts[1] == "probe" {
			id, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || id == 0 {
				fail(w, errors.New("missing id"), 400)
				return
			}
			s.portForwardProbeNow(w, r, id)
			return
		}
	}
	id := idFromPath(r.URL.Path, "/api/v1/port-forwards/")
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListPortForwards(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		if id != 0 {
			for i := range items {
				if items[i].ID == id {
					write(w, 200, map[string]any{"port_forward": items[i]})
					return
				}
			}
			fail(w, sql.ErrNoRows, 404)
			return
		}
		write(w, 200, map[string]any{"port_forwards": items})
	case http.MethodPost:
		var v model.PortForward
		if !decode(w, r, &v) {
			return
		}
		v.Enabled = true
		normalizePortForward(&v)
		if err := validatePortForward(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.ValidateServerExists(r.Context(), portForwardServerIDs(v)...); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreatePortForward(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.validateAllForwards(r.Context()); err != nil {
			_ = s.store.Delete(r.Context(), "port_forwards", v.ID)
			fail(w, err, 400)
			return
		}
		auditReq(s, r, "create", "port_forward", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"port_forward": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		var v model.PortForward
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		normalizePortForward(&v)
		if err := validatePortForward(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.ValidateServerExists(r.Context(), portForwardServerIDs(v)...); err != nil {
			fail(w, err, 400)
			return
		}
		previous, err := s.store.GetPortForward(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if err := s.store.UpdatePortForward(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.validateAllForwards(r.Context()); err != nil {
			_ = s.store.UpdatePortForward(r.Context(), previous)
			fail(w, err, 400)
			return
		}
		auditReq(s, r, "update", "port_forward", fmt.Sprint(v.ID))
		write(w, 200, map[string]any{"port_forward": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeletePortForwardProbeResults(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.Delete(r.Context(), "port_forwards", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "port_forward", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) portForwardProbeNow(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	forward, err := s.store.GetPortForward(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if !forward.Enabled {
		fail(w, errors.New("disabled port forward cannot be probed"), 400)
		return
	}
	source, err := s.store.GetServer(r.Context(), forward.SourceServerID)
	if err != nil {
		fail(w, err, 400)
		return
	}
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	version := time.Now().Unix()
	plan, err := core.BuildPortForwardPlan(version, *source, servers, []model.PortForward{*forward})
	if err != nil {
		fail(w, err, 400)
		return
	}
	if len(plan.Rules) == 0 {
		fail(w, errors.New("port forward is not applicable to its source server"), 400)
		return
	}
	task, err := s.queueAgentTask(r.Context(), forward.SourceServerID, model.AgentTaskTypeProbePortForwards, plan, version)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "probe", "port_forward", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task, "port_forward": forward})
}

func normalizePortForward(v *model.PortForward) {
	v.Name = strings.TrimSpace(v.Name)
	v.ListenIP = strings.TrimSpace(v.ListenIP)
	v.TargetAddress = strings.TrimSpace(v.TargetAddress)
	if v.Protocol == "" {
		v.Protocol = model.ForwardProtocolTCP
	}
	v.Backend = model.ForwardBackendRealm
	if v.ProbeMode == "" {
		v.ProbeMode = "periodic"
	}
	if v.ProbeIntervalSeconds == 0 {
		v.ProbeIntervalSeconds = 300
	}
	if v.Priority == 0 {
		v.Priority = 100
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
}

func validatePortForward(v model.PortForward) error {
	if v.Name == "" {
		return errors.New("name required")
	}
	if v.SourceServerID == 0 {
		return errors.New("source_server_id required")
	}
	if v.TargetServerID == 0 && strings.TrimSpace(v.TargetAddress) == "" {
		return errors.New("target_address required when target_server_id is omitted")
	}
	if v.TargetServerID != 0 && v.SourceServerID == v.TargetServerID {
		return errors.New("port forward source and target must be different")
	}
	if err := core.ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if strings.TrimSpace(v.TargetAddress) != "" {
		if err := core.ValidateSafeHost(v.TargetAddress); err != nil {
			return fmt.Errorf("target_address: %w", err)
		}
	}
	if err := core.ValidatePort(v.ListenPort); err != nil {
		return fmt.Errorf("listen_port: %w", err)
	}
	if err := core.ValidatePort(v.TargetPort); err != nil {
		return fmt.Errorf("target_port: %w", err)
	}
	if err := core.ValidateForwardProtocol(v.Protocol); err != nil {
		return err
	}
	if err := core.ValidateForwardBackend(v.Backend); err != nil {
		return err
	}
	if err := core.ValidateForwardProbeMode(v.ProbeMode); err != nil {
		return err
	}
	if v.ProbeIntervalSeconds < 300 {
		return errors.New("probe_interval_seconds must be >= 300")
	}
	return validJSONObject(v.ConfigJSON)
}

func portForwardServerIDs(v model.PortForward) []int64 {
	ids := []int64{v.SourceServerID}
	if v.TargetServerID > 0 {
		ids = append(ids, v.TargetServerID)
	}
	return ids
}

func (s *Server) validateAllForwards(ctx context.Context) error {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return err
	}
	forwards, err := s.store.ListPortForwards(ctx)
	if err != nil {
		return err
	}
	tunnels, err := s.store.ListTunnels(ctx)
	if err != nil {
		return err
	}
	if err := core.ValidatePortForwards(servers, forwards); err != nil {
		return err
	}
	if err := core.ValidateTunnels(servers, tunnels); err != nil {
		return err
	}
	return core.ValidateTopologyDAG(servers, forwards, tunnels)
}

func (s *Server) tunnels(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/tunnels/")
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListTunnels(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		if id != 0 {
			for i := range items {
				if items[i].ID == id {
					write(w, 200, map[string]any{"tunnel": publicTunnels(items[i : i+1])[0]})
					return
				}
			}
			fail(w, sql.ErrNoRows, 404)
			return
		}
		write(w, 200, map[string]any{"tunnels": publicTunnels(items)})
	case http.MethodPost:
		var v model.Tunnel
		if !decode(w, r, &v) {
			return
		}
		v.Enabled = true
		normalizeTunnel(&v)
		if err := validateTunnel(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.ValidateServerExists(r.Context(), v.SourceServerID, v.TargetServerID); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateTunnel(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.validateAllForwards(r.Context()); err != nil {
			_ = s.store.Delete(r.Context(), "tunnels", v.ID)
			fail(w, err, 400)
			return
		}
		auditReq(s, r, "create", "tunnel", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"tunnel": publicTunnels([]model.Tunnel{v})[0]})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		var v model.Tunnel
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		normalizeTunnel(&v)
		if err := validateTunnel(v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.ValidateServerExists(r.Context(), v.SourceServerID, v.TargetServerID); err != nil {
			fail(w, err, 400)
			return
		}
		previous, err := s.store.GetTunnel(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if err := s.store.UpdateTunnel(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.validateAllForwards(r.Context()); err != nil {
			_ = s.store.UpdateTunnel(r.Context(), previous)
			fail(w, err, 400)
			return
		}
		auditReq(s, r, "update", "tunnel", fmt.Sprint(v.ID))
		write(w, 200, map[string]any{"tunnel": publicTunnels([]model.Tunnel{v})[0]})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.Delete(r.Context(), "tunnels", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "tunnel", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func normalizeTunnel(v *model.Tunnel) {
	v.Name = strings.TrimSpace(v.Name)
	v.LocalAddress = strings.TrimSpace(v.LocalAddress)
	v.PeerAddress = strings.TrimSpace(v.PeerAddress)
	v.TargetEndpoint = strings.TrimSpace(v.TargetEndpoint)
	if v.Type == "" {
		v.Type = model.TunnelTypeWireGuard
	}
	if v.Priority == 0 {
		v.Priority = 100
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
}

func validateTunnel(v model.Tunnel) error {
	if v.Name == "" {
		return errors.New("name required")
	}
	if v.SourceServerID == 0 || v.TargetServerID == 0 {
		return errors.New("source_server_id and target_server_id required")
	}
	if v.SourceServerID == v.TargetServerID {
		return errors.New("tunnel source and target must be different")
	}
	if err := core.ValidateTunnelType(v.Type); err != nil {
		return err
	}
	if err := core.ValidateTunnelConfig(v); err != nil {
		return err
	}
	if strings.TrimSpace(v.TargetEndpoint) != "" {
		if err := core.ValidateTunnelEndpoint(v.TargetEndpoint); err != nil {
			return fmt.Errorf("target_endpoint: %w", err)
		}
	}
	if v.ListenPort != 0 {
		if err := core.ValidatePort(v.ListenPort); err != nil {
			return fmt.Errorf("listen_port: %w", err)
		}
	}
	if v.TargetPort != 0 {
		if err := core.ValidatePort(v.TargetPort); err != nil {
			return fmt.Errorf("target_port: %w", err)
		}
	}
	return validJSONObject(v.ConfigJSON)
}

func (s *Server) portForwardProbes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := s.store.ListPortForwardProbeResults(r.Context(), int64Query(r, "server_id", 0), int64Query(r, "port_forward_id", 0), intQuery(r, "limit", 100))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"port_forward_probes": items})
}

func (s *Server) applyDeployment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		ServerID int64 `json:"server_id"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decode(w, r, &request) {
		return
	}
	if request.ServerID != 0 {
		if _, err := s.store.GetServer(r.Context(), request.ServerID); err != nil {
			fail(w, errors.New("server_id not found"), 400)
			return
		}
	}
	tasks, version, err := s.deployConfiguration(r.Context(), request.ServerID, false)
	if err != nil {
		var herr *deploymentHTTPError
		if errors.As(err, &herr) {
			fail(w, herr.err, herr.status)
			return
		}
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "apply", "deployment", fmt.Sprint(version))
	write(w, 202, map[string]any{"config_version": version, "tasks": sanitizeTasksForRole(tasks, currentRole(r)), "summary": taskSummary(tasks)})
}

// deploymentHTTPError carries the HTTP status a failed deployment preparation
// should report so the REST handler preserves operator-facing semantics while
// recovery and enrollment callers just log the failure.
type deploymentHTTPError struct {
	status int
	err    error
}

func (e *deploymentHTTPError) Error() string { return e.err.Error() }

func deploymentFail(status int, err error) error {
	return &deploymentHTTPError{status: status, err: err}
}

// deployConfiguration prepares and queues apply_deployment tasks under the
// deployment lock. selectedServerID==0 targets every server; otherwise only that
// server. expandTransparentScope lets automatic pushes fall back to a full
// deployment when the selected server belongs to a trusted transparent
// forwarding prefix, because those members must change together.
func (s *Server) deployConfiguration(ctx context.Context, selectedServerID int64, expandTransparentScope bool) ([]model.AgentTask, int64, error) {
	return s.deployConfigurationScoped(ctx, selectedServerID, expandTransparentScope, nil, nil)
}

func (s *Server) deployConfigurationScoped(ctx context.Context, selectedServerID int64, expandTransparentScope bool, allowedServerIDs, ignoredPathIDs map[int64]bool) ([]model.AgentTask, int64, error) {
	// Preparation repairs stored topology, refreshes derived roles and allocates
	// one monotonic config version. Serialize it so two concurrent applies cannot
	// interleave those writes or queue overlapping desired state.
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()
	if err := s.store.PruneOrphanedProxyPathSteps(ctx); err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	if err := s.normalizeEnabledProxyPathProcessingRoles(ctx); err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	if len(ignoredPathIDs) > 0 {
		data = routingConfigWithoutProxyPaths(data, ignoredPathIDs)
	}
	resolveRoutingProxyPathNames(&data)
	servers, in := data.Servers, data.Inbounds
	inboundIndex := map[int64]model.Inbound{}
	for _, inbound := range in {
		inboundIndex[inbound.ID] = inbound
	}
	for _, path := range data.ProxyPaths {
		root, ok := inboundIndex[path.InboundID]
		if !path.Enabled || !ok || root.Protocol != model.ProtocolSSH {
			continue
		}
		server, ok := serverByID(servers, root.ServerID)
		if !ok || !agentBuildSupportsTask(server.AgentBuild, agentBuildMinSSHPathRelay) {
			name := fmt.Sprintf("#%d", root.ServerID)
			if ok {
				name = server.Name
			}
			return nil, 0, deploymentFail(http.StatusConflict, fmt.Errorf("服务器 %s 的 Agent 不支持 SSH 链式代理；请先更新 Agent", name))
		}
	}
	groupedServers := core.TransparentForwardServerIDs(data.ProxyPaths, data.ProxyPathSteps, in)
	effectiveScope := selectedServerID
	if allowedServerIDs != nil {
		for serverID := range allowedServerIDs {
			if !expandTransparentScope || !groupedServers[serverID] {
				continue
			}
			for groupedServerID := range groupedServers {
				allowedServerIDs[groupedServerID] = true
			}
			break
		}
		effectiveScope = 0
	} else if expandTransparentScope && effectiveScope != 0 && groupedServers[effectiveScope] {
		effectiveScope = 0
	}
	externalEgressTargetsByServer := map[int64][]model.ExternalEgressProbeTarget{}
	if effectiveScope == 0 {
		targets := core.ExternalEgressProbeTargets(data.ProxyPaths, data.ProxyPathSteps, servers, in, data.ExternalOutbounds)
		if len(targets) > maxExternalEgressTargets {
			return nil, 0, deploymentFail(http.StatusBadRequest, fmt.Errorf("第三方出口探测分支过多，单次最多支持 %d 个", maxExternalEgressTargets))
		}
		for _, target := range targets {
			if allowedServerIDs != nil && !allowedServerIDs[target.OwnerServerID] {
				continue
			}
			externalEgressTargetsByServer[target.OwnerServerID] = append(externalEgressTargetsByServer[target.OwnerServerID], target)
		}
		for serverID, targets := range externalEgressTargetsByServer {
			if len(targets) == 0 {
				continue
			}
			server, ok := serverByID(servers, serverID)
			if !ok || !agentBuildSupportsTask(server.AgentBuild, agentBuildMinExternalEgress) {
				name := fmt.Sprintf("#%d", serverID)
				if ok {
					name = server.Name
				}
				return nil, 0, deploymentFail(http.StatusConflict, fmt.Errorf("服务器 %s 的 Agent 不支持第三方节点出口探测；请先更新 Agent", name))
			}
		}
	}
	warpServerIDs, err := core.ProxyPathWARPServerIDs(data.ProxyPaths, data.ProxyPathSteps, in)
	if err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	for serverID := range warpServerIDs {
		if _, err := s.store.EnsureWARPProfileForServer(ctx, serverID); err != nil {
			return nil, 0, deploymentFail(500, err)
		}
	}
	data.WARPProfiles, err = s.store.ListWARPProfiles(ctx)
	if err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	if err := validateTransparentForwardDeploymentSelection(effectiveScope, allowedServerIDs, groupedServers); err != nil {
		return nil, 0, deploymentFail(http.StatusConflict, err)
	}
	forwards, err := s.store.ListPortForwards(ctx)
	if err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	tunnels, err := s.store.ListTunnels(ctx)
	if err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	if allowedServerIDs != nil {
		forwards = filterPortForwardsForServers(forwards, allowedServerIDs)
		tunnels = filterTunnelsForServers(tunnels, allowedServerIDs)
	}
	// Reuse the ports already recorded for generated listeners and let the
	// projection allocate only what is genuinely new. One ledger is shared by the
	// derivation below and by every per-server config, so all of them agree.
	allocations, err := s.store.ListProxyPathPortAllocations(ctx)
	if err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	ledger := core.NewProxyPathPortLedger(allocations)
	derivedForwards, err := core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, servers, in, ledger)
	if err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	derivedTunnels, err := core.DerivedTunnelsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, servers, in, ledger)
	if err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	forwards = append(forwards, derivedForwards...)
	tunnels = append(tunnels, derivedTunnels...)
	if err := core.ValidatePortForwards(servers, forwards); err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	if err := core.ValidateTunnels(servers, tunnels); err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	if err := core.ValidateTopologyDAG(servers, forwards, tunnels); err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	dnsServers, dnsInbounds := servers, in
	if allowedServerIDs != nil {
		dnsServers = filterServersByID(servers, allowedServerIDs)
		dnsInbounds = filterInboundsByServerID(in, allowedServerIDs)
	}
	if _, err := s.syncDNSInbounds(ctx, dnsServers, dnsInbounds); err != nil {
		return nil, 0, deploymentFail(400, err)
	}
	inboundBindings, pathBindings, _, err := s.runtimeAccessBindings(ctx, data)
	if err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	version, err := s.store.NextConfigVersion(ctx)
	if err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	type preparedDeployment struct {
		serverID int64
		payload  model.DeploymentTaskPayload
	}
	prepared := make([]preparedDeployment, 0, len(servers))
	waitingForCertificate := map[int64]bool{}
	for _, server := range servers {
		if allowedServerIDs != nil && !allowedServerIDs[server.ID] || allowedServerIDs == nil && effectiveScope != 0 && server.ID != effectiveScope {
			continue
		}
		warpRequests := make([]model.WARPRequestPlan, 0)
		if warpServerIDs[server.ID] {
			profile, ok := findWARPProfileForServer(data.WARPProfiles, server.ID)
			if !ok || !profile.Enabled {
				return nil, 0, deploymentFail(400, fmt.Errorf("server %s requires an unavailable WARP profile", server.Name))
			}
			if profile.Status == model.WARPStatusReady && strings.TrimSpace(profile.ConfigJSON) != "" {
				// The complete endpoint is already generated into the Controller config.
			} else {
				now := time.Now().UTC()
				profile.Status = model.WARPStatusRequested
				profile.LastRequestedAt = &now
				profile.Error = ""
				if err := s.store.UpdateWARPProfile(ctx, &profile); err != nil {
					return nil, 0, deploymentFail(500, err)
				}
				data.WARPProfiles = replaceWARPProfile(data.WARPProfiles, profile)
				effectiveStack := core.EffectiveIPStack(server)
				plan := model.WARPRequestPlan{Version: version, ServerID: server.ID, ProfileID: profile.ID, OutboundTag: core.WARPOutboundTag(profile.ID), IPStack: effectiveStack, MTU: warpRequestMTU(profile), DNSStrategy: string(effectiveStack)}
				if plan.DNSStrategy == string(model.IPStackAuto) || plan.DNSStrategy == string(model.IPStackDualStack) {
					plan.DNSStrategy = "auto"
				}
				if underlay, err := core.ValidateDialConstraint(profile.UnderlayJSON); err == nil && underlay.Mode != core.DialConstraintModeAuto {
					plan.Underlay = &model.DialConstraint{Mode: underlay.Mode, InterfaceName: underlay.InterfaceName, SourceAddress: underlay.SourceAddress, Family: underlay.Family}
				}
				warpRequests = append(warpRequests, plan)
			}
		}

		generated, err := s.generateServerCoreConfigWithLedger(ctx, server, data, ledger)
		if err != nil {
			if automaticConfigurationSync(ctx) && errors.Is(err, errCertificateProvisioning) {
				waitingForCertificate[server.ID] = true
				continue
			}
			// Generation rejects operator-fixable desired state too — a listener
			// conflict, an unreachable address, an unsupported protocol field. Those
			// are 400s like the dedicated validators below, not server faults.
			return nil, 0, deploymentFail(deploymentConfigErrorStatus(err), err)
		}
		managedAssets, cfg := generated.Assets, generated.Config
		configChanged := true
		if cmp, err := s.compareServerConfigState(ctx, server.ID, cfg); err != nil {
			return nil, 0, deploymentFail(500, err)
		} else if cmp.DataPlaneEqual {
			configChanged = false
		}
		triggerReason := "manual_deploy"
		if automaticConfigurationSync(ctx) {
			triggerReason = "configuration_recovery"
		}

		forwardPlan, err := core.BuildPortForwardPlan(version, server, servers, forwards)
		if err != nil {
			return nil, 0, deploymentFail(400, err)
		}

		inboundProbePlan, externalInboundProbePlan := buildInboundProbePlans(version, server, in, ledger, true)
		var inboundProbe *model.InboundProbePlan
		var externalInboundProbe *model.InboundProbePlan
		if len(inboundProbePlan.EntryTargets) > 0 {
			inboundProbe = &inboundProbePlan
		}
		if len(externalInboundProbePlan.EntryTargets) > 0 {
			externalInboundProbe = &externalInboundProbePlan
		}

		var forwardProbe *model.PortForwardPlan
		if probePlan := immediateForwardProbePlan(forwardPlan); len(probePlan.Rules) > 0 {
			forwardProbe = &probePlan
		}

		tunnelPlan, err := core.BuildTunnelPlan(version, server, servers, tunnels)
		if err != nil {
			return nil, 0, deploymentFail(400, err)
		}
		sshInboundPlan, err := buildSSHInboundPlan(version, server, data, inboundBindings, pathBindings, generated.TrafficPolicies)
		if err != nil {
			return nil, 0, deploymentFail(400, err)
		}
		if err := core.ValidateDeploymentListenResources(server.ID, cfg, forwardPlan, tunnelPlan, sshInboundPlan); err != nil {
			return nil, 0, deploymentFail(400, err)
		}
		dnsState, err := core.DNSConfigStateForServer(server.ID, data.DNSLists, data.ServerDNSPolicies)
		if err != nil {
			return nil, 0, deploymentFail(400, err)
		}
		dnsPlan, err := core.DNSBenchmarkPlanForPolicy(version, *dnsState.Policy, dnsState.EncryptedList, *dnsState.BootstrapList, core.EffectiveIPStack(server), dnsState.Policy.AutoTest, "")
		if err != nil {
			return nil, 0, deploymentFail(400, err)
		}
		var mtuPlan *model.MTUDetectionPlan
		if server.MTUMode != "" && server.MTUMode != model.MTUModeDisabled {
			candidate := mtuPlanFromServer(version, server, server.MTUMode)
			run, err := s.shouldRunDeploymentMTU(ctx, candidate)
			if err != nil {
				return nil, 0, deploymentFail(500, err)
			}
			if run {
				mtuPlan = &candidate
			}
		}

		settings, err := s.store.ListSettings(ctx)
		if err != nil {
			return nil, 0, deploymentFail(500, err)
		}
		timePlan := model.TimeCheckPlan{Version: version, CorrectionMode: server.TimeCorrectionMode, ThresholdSeconds: timeCheckThresholdSeconds, NTPServers: timeCheckNTPServers(settings), Force: true}
		var externalEgressProbe *model.ExternalEgressProbePlan
		if targets := externalEgressTargetsByServer[server.ID]; len(targets) > 0 {
			externalEgressProbe = &model.ExternalEgressProbePlan{Version: version, ExpectedConfigVersion: version, TimeoutMS: externalEgressTimeoutMS, Targets: targets}
		}
		payload := model.DeploymentTaskPayload{
			Version:              version,
			Config:               model.ApplyCoreConfigTaskPayload{Config: cfg, Assets: managedAssets},
			ConfigChanged:        configChanged,
			TriggerReason:        triggerReason,
			WARPRequests:         warpRequests,
			TimeCheck:            &timePlan,
			PortForwards:         forwardPlan,
			InboundProbe:         inboundProbe,
			ExternalInboundProbe: externalInboundProbe,
			PortForwardProbe:     forwardProbe,
			ExternalEgressProbe:  externalEgressProbe,
			Tunnels:              tunnelPlan,
			SSHInbounds:          sshInboundPlan,
			DNSBenchmark:         dnsPlan,
			MTUDetection:         mtuPlan,
		}
		prepared = append(prepared, preparedDeployment{serverID: server.ID, payload: payload})
	}
	// Transparent-forward members must move together. If one member is waiting
	// for a certificate, hold the complete transparent scope while still allowing
	// unrelated servers in this reconciliation batch to proceed.
	groupedScopeWaiting := false
	for serverID := range waitingForCertificate {
		if groupedServers[serverID] {
			groupedScopeWaiting = true
			break
		}
	}
	if groupedScopeWaiting {
		filtered := prepared[:0]
		for _, item := range prepared {
			if groupedServers[item.serverID] {
				waitingForCertificate[item.serverID] = true
				continue
			}
			filtered = append(filtered, item)
		}
		prepared = filtered
	}
	// Every server validated, so the ports this projection chose are the ones the
	// Agents will receive. Persist them before queueing any task: from now on a
	// later topology change must reuse these values instead of re-deriving them.
	staleAllocationIDs := core.StaleProxyPathPortAllocationIDs(allocations, ledger)
	if len(ignoredPathIDs) > 0 {
		staleAllocationIDs = nil
	}
	if err := s.store.SaveProxyPathPortAllocations(ctx, ledger.Pending(), staleAllocationIDs); err != nil {
		return nil, 0, deploymentFail(500, err)
	}
	tasks := make([]model.AgentTask, 0, len(prepared))
	for _, item := range prepared {
		task, err := s.queueAgentTask(ctx, item.serverID, model.AgentTaskTypeApplyDeployment, item.payload, version)
		if err != nil {
			return nil, 0, deploymentFail(500, err)
		}
		s.applyDeploymentCreatedTotal.Add(1)
		tasks = append(tasks, task)
		if item.payload.ExternalEgressProbe != nil {
			for _, target := range item.payload.ExternalEgressProbe.Targets {
				if err := s.store.MarkProxyPathEgressPending(ctx, target, version, task.ID); err != nil {
					return nil, 0, deploymentFail(500, err)
				}
			}
		}
	}
	return tasks, version, nil
}

func validateTransparentForwardDeploymentScope(selectedServerID int64, required map[int64]bool) error {
	if selectedServerID != 0 && required[selectedServerID] {
		return errors.New("透明转发涉及多台服务器；请执行完整部署，不能仅部署其中一台服务器")
	}
	return nil
}

func validateTransparentForwardDeploymentSelection(selectedServerID int64, allowed, required map[int64]bool) error {
	if allowed == nil {
		return validateTransparentForwardDeploymentScope(selectedServerID, required)
	}
	selectedGrouped := false
	for serverID := range allowed {
		if required[serverID] {
			selectedGrouped = true
			break
		}
	}
	if !selectedGrouped {
		return nil
	}
	for serverID := range required {
		if !allowed[serverID] {
			return errors.New("透明转发涉及多台服务器；必须同步完整成员集合")
		}
	}
	return nil
}

func routingConfigWithoutProxyPaths(data store.FullRoutingConfig, ignored map[int64]bool) store.FullRoutingConfig {
	paths := data.ProxyPaths[:0]
	for _, path := range data.ProxyPaths {
		if !ignored[path.ID] {
			paths = append(paths, path)
		}
	}
	data.ProxyPaths = paths
	steps := data.ProxyPathSteps[:0]
	for _, step := range data.ProxyPathSteps {
		if !ignored[step.PathID] {
			steps = append(steps, step)
		}
	}
	data.ProxyPathSteps = steps
	rules := data.RoutingRules[:0]
	for _, rule := range data.RoutingRules {
		if routingRuleTouchesIgnoredPaths(rule, ignored, data.ProxyPaths) {
			continue
		}
		rules = append(rules, rule)
	}
	data.RoutingRules = rules
	results := data.ProxyPathEgressResults[:0]
	for _, result := range data.ProxyPathEgressResults {
		if !ignored[result.PathID] {
			results = append(results, result)
		}
	}
	data.ProxyPathEgressResults = results
	planNodes := data.ActivePlanNodes[:0]
	for _, node := range data.ActivePlanNodes {
		if node.NodeType != model.AssignableNodeProxyPath || !ignored[node.NodeID] {
			planNodes = append(planNodes, node)
		}
	}
	data.ActivePlanNodes = planNodes
	exceptions := data.UserNodeExceptions[:0]
	for _, exception := range data.UserNodeExceptions {
		if exception.NodeType != model.AssignableNodeProxyPath || !ignored[exception.NodeID] {
			exceptions = append(exceptions, exception)
		}
	}
	data.UserNodeExceptions = exceptions
	return data
}

func optionalIDInSet(id *int64, set map[int64]bool) bool {
	return id != nil && set[*id]
}

func routingRuleTouchesIgnoredPaths(rule model.RoutingRule, ignored map[int64]bool, paths []model.ProxyPath) bool {
	if optionalIDInSet(rule.ProxyPathID, ignored) || optionalIDInSet(rule.TargetProxyPathID, ignored) {
		return true
	}
	if rule.Action != model.RouteActionFamilySplit || rule.FamilySplitTemplateID == nil {
		return false
	}
	for _, path := range paths {
		if path.TemplateID != nil && *path.TemplateID == *rule.FamilySplitTemplateID && ignored[path.ID] {
			return true
		}
	}
	return false
}

func applyFamilyBranchBindConfig(v *model.ProxyPathStep, cfg map[string]any) error {
	managed := map[string]any{}
	if err := mergeFamilyBranchBindFields(managed, cfg); err != nil {
		return err
	}
	encoded, err := json.Marshal(managed)
	if err != nil {
		return err
	}
	v.ConfigJSON = string(encoded)
	return nil
}

func mergeFamilyBranchBindFields(dst, src map[string]any) error {
	if dst == nil {
		return errors.New("config is required")
	}
	encoded, err := json.Marshal(src)
	if err != nil {
		return err
	}
	binding, err := core.ParseFamilyBranchExitBinding(string(encoded))
	if err != nil {
		return err
	}
	delete(dst, "interface_name")
	delete(dst, "source_prefix")
	if binding.InterfaceName != "" {
		dst["interface_name"] = binding.InterfaceName
	}
	if binding.SourcePrefix != "" {
		dst["source_prefix"] = binding.SourcePrefix
	}
	return nil
}

func filterPortForwardsForServers(items []model.PortForward, allowed map[int64]bool) []model.PortForward {
	out := items[:0]
	for _, item := range items {
		if allowed[item.SourceServerID] {
			out = append(out, item)
		}
	}
	return out
}

func filterTunnelsForServers(items []model.Tunnel, allowed map[int64]bool) []model.Tunnel {
	out := items[:0]
	for _, item := range items {
		if allowed[item.SourceServerID] || allowed[item.TargetServerID] {
			out = append(out, item)
		}
	}
	return out
}

func filterServersByID(items []model.Server, allowed map[int64]bool) []model.Server {
	out := make([]model.Server, 0, len(allowed))
	for _, item := range items {
		if allowed[item.ID] {
			out = append(out, item)
		}
	}
	return out
}

func filterInboundsByServerID(items []model.Inbound, allowed map[int64]bool) []model.Inbound {
	out := make([]model.Inbound, 0)
	for _, item := range items {
		if allowed[item.ServerID] {
			out = append(out, item)
		}
	}
	return out
}

// buildSSHInboundPlan turns the regular inbound permissions into a dedicated
// user-facing SSH listener plan. It reuses the user's proxy password and never
// exposes the panel login password.
func buildSSHInboundPlan(version int64, server model.Server, data store.FullRoutingConfig, inboundUsers []model.InboundUser, pathUsers []model.ProxyPathUser, policies map[int64]model.TrafficRuntimePolicy) (model.SSHInboundPlan, error) {
	plan := model.SSHInboundPlan{Version: version, Inbounds: []model.SSHInbound{}}
	users := make(map[int64][]model.User, len(data.Users))
	for _, user := range core.ExpandDeviceUsers(data.Users, data.UserDevices) {
		users[user.ID] = append(users[user.ID], user)
	}
	pathBound := map[int64][]int64{}
	for _, binding := range pathUsers {
		if binding.Enabled {
			pathBound[binding.ProxyPathID] = append(pathBound[binding.ProxyPathID], binding.UserID)
		}
	}
	pathsByInbound := map[int64][]model.ProxyPath{}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, path := range data.ProxyPaths {
		if path.Enabled {
			pathsByInbound[path.InboundID] = append(pathsByInbound[path.InboundID], path)
		}
	}
	for _, step := range data.ProxyPathSteps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	for inboundID := range pathsByInbound {
		sort.SliceStable(pathsByInbound[inboundID], func(i, j int) bool { return pathsByInbound[inboundID][i].ID < pathsByInbound[inboundID][j].ID })
	}
	for _, inbound := range data.Inbounds {
		if !inbound.Enabled || inbound.ServerID != server.ID || inbound.Protocol != model.ProtocolSSH {
			continue
		}
		address := core.ResolveDNSPreferredEntryAddress(inbound, server)
		if strings.TrimSpace(address) == "" {
			return model.SSHInboundPlan{}, fmt.Errorf("SSH 入口 %s 缺少可用的连接地址", inbound.Name)
		}
		entry := model.SSHInbound{InboundID: inbound.ID, ServerID: server.ID, Name: inbound.Name, ListenIP: core.EffectiveListenIP(server, inbound.ListenIP), Address: address, Port: inbound.Port, Enabled: true, Users: []model.SSHInboundUser{}, Policies: map[string]model.TrafficRuntimePolicy{}}
		seenPolicy := map[int64]bool{}
		appendSSHUser := func(user model.User, pathID int64, routeInboundTag, routeAuthUser string) error {
			if user.Status != "active" || strings.HasPrefix(user.Username, "__oboard_") || strings.TrimSpace(user.ProxyPassword) == "" {
				return nil
			}
			if strings.TrimSpace(user.SSHRandomID) == "" {
				return fmt.Errorf("SSH 用户 %d 缺少随机登录标识", user.ID)
			}
			credential := core.UserCredentialForRoute(user, inbound.ID, pathID, model.ProtocolSSH)
			status := strings.TrimSpace(user.CredentialStatus)
			if status == "" {
				status = "active"
			}
			entry.Users = append(entry.Users, model.SSHInboundUser{UserID: user.ID, Username: sshLoginName(user, pathID), Password: credential.ProxyPassword, DeviceIDHash: user.DeviceIDHash, CredentialEpoch: user.CredentialEpoch, CredentialStatus: status, PathID: pathID, RouteKind: "kernel", RouteInboundTag: routeInboundTag, RouteAuthUser: routeAuthUser, Enabled: true})
			if !seenPolicy[user.ID] {
				seenPolicy[user.ID] = true
				if policy, ok := policies[user.ID]; ok {
					policy.InboundID = inbound.ID
					entry.Policies[fmt.Sprintf("user:%d", user.ID)] = policy
				}
			}
			return nil
		}
		for _, path := range pathsByInbound[inbound.ID] {
			_, _, err := core.ProxyPathEntryRoute(path, stepsByPath[path.ID], inbound, data.WARPProfiles)
			if err != nil {
				return model.SSHInboundPlan{}, fmt.Errorf("SSH 路径 %s: %w", path.Name, err)
			}
			for _, userID := range pathBound[path.ID] {
				for _, user := range users[userID] {
					routeInboundTag, routeAuthUser, err := core.ProxyPathEntryRoutingIdentity(path, inbound, user)
					if err != nil {
						return model.SSHInboundPlan{}, fmt.Errorf("SSH path %s: %w", path.Name, err)
					}
					if err := appendSSHUser(user, path.ID, routeInboundTag, routeAuthUser); err != nil {
						return model.SSHInboundPlan{}, err
					}
				}
			}
		}
		if len(pathsByInbound[inbound.ID]) == 0 {
			// A branchless SSH inbound serves its inbound-level grants over one
			// implicit direct-exit route with a virtual branch id.
			for _, binding := range inboundUsers {
				if !binding.Enabled || binding.InboundID != inbound.ID {
					continue
				}
				for _, user := range users[binding.UserID] {
					routeInboundTag, routeAuthUser, pathID := core.SSHDirectBranchIdentity(inbound.ID, user.Username)
					if err := appendSSHUser(user, pathID, routeInboundTag, routeAuthUser); err != nil {
						return model.SSHInboundPlan{}, err
					}
				}
			}
		}
		plan.Inbounds = append(plan.Inbounds, entry)
	}
	return plan, nil
}

func sshLoginName(user model.User, pathID int64) string {
	return fmt.Sprintf("u%s-p%d", user.SSHRandomID, pathID)
}

func sshInboundPlanDigest(plan model.SSHInboundPlan) string {
	type digestUser struct {
		UserID           int64  `json:"user_id"`
		Username         string `json:"username"`
		DeviceIDHash     string `json:"device_id_hash,omitempty"`
		CredentialEpoch  int64  `json:"credential_epoch,omitempty"`
		CredentialStatus string `json:"credential_status"`
		PathID           int64  `json:"path_id"`
		RouteKind        string `json:"route_kind"`
		OutboundTag      string `json:"outbound_tag,omitempty"`
		RouteInboundTag  string `json:"route_inbound_tag,omitempty"`
		RouteAuthUser    string `json:"route_auth_user,omitempty"`
	}
	type digestInbound struct {
		InboundID int64        `json:"inbound_id"`
		ServerID  int64        `json:"server_id"`
		ListenIP  string       `json:"listen_ip"`
		Address   string       `json:"address"`
		Port      int          `json:"port"`
		Users     []digestUser `json:"users"`
	}
	canonical := make([]digestInbound, 0, len(plan.Inbounds))
	for _, inbound := range plan.Inbounds {
		item := digestInbound{InboundID: inbound.InboundID, ServerID: inbound.ServerID, ListenIP: inbound.ListenIP, Address: inbound.Address, Port: inbound.Port, Users: []digestUser{}}
		for _, user := range inbound.Users {
			if user.Enabled {
				status := strings.TrimSpace(user.CredentialStatus)
				if status == "" {
					status = "active"
				}
				item.Users = append(item.Users, digestUser{UserID: user.UserID, Username: user.Username, DeviceIDHash: user.DeviceIDHash, CredentialEpoch: user.CredentialEpoch, CredentialStatus: status, PathID: user.PathID, RouteKind: user.RouteKind, OutboundTag: user.OutboundTag, RouteInboundTag: user.RouteInboundTag, RouteAuthUser: user.RouteAuthUser})
			}
		}
		canonical = append(canonical, item)
	}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

// sshInboundListenerPlanDigest identifies the deployed public SSH services,
// without coupling subscription availability to other users or runtime IP
// detection. Address is client-facing metadata, while ListenIP may be
// re-derived from health reports; neither changes the listener that is already
// serving the persisted port.
func sshInboundListenerPlanDigest(plan model.SSHInboundPlan) string {
	type digestInbound struct {
		InboundID int64 `json:"inbound_id"`
		ServerID  int64 `json:"server_id"`
		Port      int   `json:"port"`
	}
	canonical := make([]digestInbound, 0, len(plan.Inbounds))
	for _, inbound := range plan.Inbounds {
		if inbound.Enabled {
			canonical = append(canonical, digestInbound{InboundID: inbound.InboundID, ServerID: inbound.ServerID, Port: inbound.Port})
		}
	}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func sshInboundIdentityRouteDigest(plan model.SSHInboundPlan, identity sshPasswordDeploymentIdentity) (string, bool) {
	type digestRoute struct {
		InboundID       int64  `json:"inbound_id"`
		PathID          int64  `json:"path_id"`
		Username        string `json:"username"`
		CredentialState string `json:"credential_status"`
		RouteKind       string `json:"route_kind"`
		OutboundTag     string `json:"outbound_tag,omitempty"`
		RouteInboundTag string `json:"route_inbound_tag,omitempty"`
		RouteAuthUser   string `json:"route_auth_user,omitempty"`
	}
	routes := []digestRoute{}
	for _, inbound := range plan.Inbounds {
		if !inbound.Enabled {
			continue
		}
		for _, user := range inbound.Users {
			if !user.Enabled || sshPasswordDeploymentIdentityForPlanUser(user) != identity {
				continue
			}
			status := strings.TrimSpace(user.CredentialStatus)
			if status == "" {
				status = "active"
			}
			routes = append(routes, digestRoute{InboundID: inbound.InboundID, PathID: user.PathID, Username: user.Username, CredentialState: status, RouteKind: user.RouteKind, OutboundTag: user.OutboundTag, RouteInboundTag: user.RouteInboundTag, RouteAuthUser: user.RouteAuthUser})
		}
	}
	if len(routes) == 0 {
		return "", false
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].InboundID != routes[j].InboundID {
			return routes[i].InboundID < routes[j].InboundID
		}
		if routes[i].PathID != routes[j].PathID {
			return routes[i].PathID < routes[j].PathID
		}
		return routes[i].Username < routes[j].Username
	})
	encoded, _ := json.Marshal(routes)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:]), true
}

func matchingSSHIdentityRoutePlan(current, deployed model.SSHInboundPlan, identity sshPasswordDeploymentIdentity) bool {
	currentDigest, currentOK := sshInboundIdentityRouteDigest(current, identity)
	deployedDigest, deployedOK := sshInboundIdentityRouteDigest(deployed, identity)
	return currentOK && deployedOK && currentDigest == deployedDigest
}

func (s *Server) subscriptionSSHServerHostKeys(ctx context.Context, user model.User, data store.FullRoutingConfig, inboundUsers []model.InboundUser, pathUsers []model.ProxyPathUser) (map[int64]string, error) {
	deployments, err := s.store.ListSSHPasswordDeploymentsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	identity := sshPasswordDeploymentIdentityForUser(user)
	hostKeys := map[int64]string{}
	for _, server := range data.Servers {
		plan, err := buildSSHInboundPlan(0, server, data, inboundUsers, pathUsers, nil)
		if err != nil {
			return nil, err
		}
		expectedDeployments, err := s.sshPasswordDeploymentsFromPlan(server.ID, plan)
		if err != nil {
			return nil, err
		}
		expected, expectedOK := sshPasswordDeploymentForIdentity(expectedDeployments, server.ID, identity)
		persisted, persistedOK := sshPasswordDeploymentForIdentity(deployments, server.ID, identity)
		if !expectedOK || !persistedOK || !matchingSSHPasswordDeployment(persisted, expected) {
			continue
		}
		hostKey, deployedPlan, ready, err := s.matchingDeployedSSHPlan(ctx, server.ID, plan)
		if err != nil {
			return nil, err
		}
		if ready && matchingSSHIdentityRoutePlan(plan, deployedPlan, identity) {
			hostKeys[server.ID] = hostKey.PublicKey
		}
	}
	return hostKeys, nil
}

func (s *Server) matchingDeployedSSHPlan(ctx context.Context, serverID int64, current model.SSHInboundPlan) (*model.SSHServerHostKey, model.SSHInboundPlan, bool, error) {
	hostKey, err := s.store.GetSSHServerHostKey(ctx, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.SSHInboundPlan{}, false, nil
	}
	if err != nil {
		return nil, model.SSHInboundPlan{}, false, err
	}
	task, err := s.store.LastSuccessfulTaskByServerType(ctx, serverID, model.AgentTaskTypeApplyDeployment)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.SSHInboundPlan{}, false, nil
	}
	if err != nil {
		return nil, model.SSHInboundPlan{}, false, err
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
		return nil, model.SSHInboundPlan{}, false, nil
	}
	version := payload.Version
	if version == 0 {
		version = task.ConfigVersion
	}
	if version != hostKey.ConfigVersion || hostKey.PlanDigest != sshInboundPlanDigest(payload.SSHInbounds) {
		return nil, model.SSHInboundPlan{}, false, nil
	}
	if sshInboundListenerPlanDigest(current) != sshInboundListenerPlanDigest(payload.SSHInbounds) {
		return nil, model.SSHInboundPlan{}, false, nil
	}
	return hostKey, payload.SSHInbounds, true, nil
}

type sshPasswordDeploymentIdentity struct {
	UserID          int64
	DeviceIDHash    string
	CredentialEpoch int64
}

type sshPasswordDeploymentCredential struct {
	InboundID int64
	PathID    int64
	Username  string
	Password  string
}

func sshPasswordDeploymentIdentityForUser(user model.User) sshPasswordDeploymentIdentity {
	return sshPasswordDeploymentIdentity{UserID: user.ID, DeviceIDHash: strings.TrimSpace(user.DeviceIDHash), CredentialEpoch: user.CredentialEpoch}
}

func sshPasswordDeploymentIdentityForPlanUser(user model.SSHInboundUser) sshPasswordDeploymentIdentity {
	return sshPasswordDeploymentIdentity{UserID: user.UserID, DeviceIDHash: strings.TrimSpace(user.DeviceIDHash), CredentialEpoch: user.CredentialEpoch}
}

func (s *Server) sshPasswordDeploymentsFromPlan(serverID int64, plan model.SSHInboundPlan) ([]model.SSHPasswordDeployment, error) {
	type credentialGroup struct {
		status      string
		credentials []sshPasswordDeploymentCredential
	}
	groups := map[sshPasswordDeploymentIdentity]*credentialGroup{}
	for _, inbound := range plan.Inbounds {
		for _, user := range inbound.Users {
			if !user.Enabled || user.UserID <= 0 || strings.TrimSpace(user.Password) == "" {
				continue
			}
			identity := sshPasswordDeploymentIdentityForPlanUser(user)
			status := strings.TrimSpace(user.CredentialStatus)
			if status == "" {
				status = "active"
			}
			group := groups[identity]
			if group == nil {
				group = &credentialGroup{status: status}
				groups[identity] = group
			} else if group.status != status {
				return nil, fmt.Errorf("SSH deployment identity for user %d contains conflicting credential states", user.UserID)
			}
			group.credentials = append(group.credentials, sshPasswordDeploymentCredential{InboundID: inbound.InboundID, PathID: user.PathID, Username: user.Username, Password: user.Password})
		}
	}
	identities := make([]sshPasswordDeploymentIdentity, 0, len(groups))
	for identity := range groups {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].UserID != identities[j].UserID {
			return identities[i].UserID < identities[j].UserID
		}
		if identities[i].DeviceIDHash != identities[j].DeviceIDHash {
			return identities[i].DeviceIDHash < identities[j].DeviceIDHash
		}
		return identities[i].CredentialEpoch < identities[j].CredentialEpoch
	})
	deployments := make([]model.SSHPasswordDeployment, 0, len(identities))
	for _, identity := range identities {
		group := groups[identity]
		sort.Slice(group.credentials, func(i, j int) bool {
			left, right := group.credentials[i], group.credentials[j]
			if left.InboundID != right.InboundID {
				return left.InboundID < right.InboundID
			}
			if left.PathID != right.PathID {
				return left.PathID < right.PathID
			}
			return left.Username < right.Username
		})
		mac := hmac.New(sha256.New, []byte(s.sessionSecret))
		_, _ = fmt.Fprintf(mac, "ssh-password-deployment-v2\x00%d\x00%d\x00%s\x00%d\x00%s", serverID, identity.UserID, identity.DeviceIDHash, identity.CredentialEpoch, group.status)
		for _, credential := range group.credentials {
			_, _ = fmt.Fprintf(mac, "\x00%d\x00%d\x00%s\x00%s", credential.InboundID, credential.PathID, credential.Username, credential.Password)
		}
		deployments = append(deployments, model.SSHPasswordDeployment{ServerID: serverID, UserID: identity.UserID, DeviceIDHash: identity.DeviceIDHash, CredentialEpoch: identity.CredentialEpoch, CredentialStatus: group.status, PasswordDigest: fmt.Sprintf("%x", mac.Sum(nil))})
	}
	return deployments, nil
}

func sshPasswordDeploymentForIdentity(deployments []model.SSHPasswordDeployment, serverID int64, identity sshPasswordDeploymentIdentity) (model.SSHPasswordDeployment, bool) {
	for _, deployment := range deployments {
		if deployment.ServerID == serverID && deployment.UserID == identity.UserID && strings.TrimSpace(deployment.DeviceIDHash) == identity.DeviceIDHash && deployment.CredentialEpoch == identity.CredentialEpoch {
			return deployment, true
		}
	}
	return model.SSHPasswordDeployment{}, false
}

func matchingSSHPasswordDeployment(persisted, expected model.SSHPasswordDeployment) bool {
	return persisted.ServerID == expected.ServerID &&
		persisted.UserID == expected.UserID &&
		strings.TrimSpace(persisted.DeviceIDHash) == strings.TrimSpace(expected.DeviceIDHash) &&
		persisted.CredentialEpoch == expected.CredentialEpoch &&
		strings.TrimSpace(persisted.CredentialStatus) == strings.TrimSpace(expected.CredentialStatus) &&
		strings.TrimSpace(persisted.PasswordDigest) != "" &&
		persisted.PasswordDigest == expected.PasswordDigest
}

func effectiveConfigSHA256FromDeploymentResult(raw string) string {
	var result struct {
		Steps []struct {
			Key    string `json:"key"`
			Result struct {
				EffectiveConfigSHA256 string `json:"effective_config_sha256"`
			} `json:"result"`
		} `json:"steps"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil {
		return ""
	}
	for _, step := range result.Steps {
		if step.Key == "config" {
			return strings.TrimSpace(step.Result.EffectiveConfigSHA256)
		}
	}
	return ""
}

func canonicalConfigSHA256(config string) (string, error) {
	var value any
	if err := json.Unmarshal([]byte(config), &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(canonical)), nil
}

func (s *Server) shouldRunDeploymentMTU(ctx context.Context, plan model.MTUDetectionPlan) (bool, error) {
	items, err := s.store.ListMTUDetectionResults(ctx, plan.ServerID, 1)
	if err != nil {
		return false, err
	}
	if len(items) == 0 {
		return true, nil
	}
	last := items[0]
	if last.Mode != plan.Mode || last.TargetHost != plan.TargetHost || last.TargetPort != plan.TargetPort {
		return true, nil
	}
	var previous struct {
		OverheadBytes int `json:"overhead_bytes"`
		DesiredMTU    int `json:"desired_mtu"`
	}
	if json.Unmarshal([]byte(last.ResultJSON), &previous) != nil || previous.OverheadBytes != plan.OverheadBytes {
		return true, nil
	}
	effectiveMTU := last.RecommendedMTU
	if last.AppliedMTU > 0 {
		effectiveMTU = last.AppliedMTU
	}
	lastSucceeded := strings.TrimSpace(last.Error) == "" && last.RecommendedMTU > 0 && (last.Mode != model.MTUModeApply || last.AppliedMTU > 0)
	if lastSucceeded {
		return plan.DesiredMTU != effectiveMTU, nil
	}
	return plan.DesiredMTU != previous.DesiredMTU, nil
}

func (s *Server) deployment(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/deployments/")
	id := idFromPath(r.URL.Path, "/api/v1/deployments/")
	if id <= 0 {
		fail(w, errors.New("missing deployment version"), http.StatusBadRequest)
		return
	}
	if len(parts) == 2 && parts[1] == "dismiss-failure" {
		s.dismissDeploymentFailure(w, r)
		return
	}
	if len(parts) != 1 {
		fail(w, errors.New("unknown deployment action"), http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	tasks, err := s.store.ListTasksByConfigVersion(r.Context(), id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	status, err := s.deploymentStatus(r.Context(), tasks)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	status["tasks"] = sanitizeTasksForRole(tasks, currentRole(r))
	write(w, 200, status)
}

func (s *Server) deploymentStatus(ctx context.Context, tasks []model.AgentTask) (map[string]any, error) {
	version := int64(0)
	if len(tasks) > 0 {
		version = tasks[0].ConfigVersion
	}
	status := map[string]any{
		"config_version":    version,
		"summary":           taskSummary(tasks),
		"failure_dismissed": false,
	}
	if version <= 0 {
		return status, nil
	}
	dismissal, err := s.store.GetDeploymentFailureDismissal(ctx, version)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return nil, err
	}
	status["failure_dismissed"] = true
	status["failure_dismissed_by"] = dismissal.ActorID
	status["failure_dismissed_at"] = dismissal.DismissedAt
	return status, nil
}

func (s *Server) dismissDeploymentFailure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	s.expireTimedOutTasks(r.Context())
	latest, err := s.store.LatestDeploymentTasks(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if len(latest) == 0 {
		fail(w, errors.New("deployment has no failure"), http.StatusConflict)
		return
	}
	version := latest[0].ConfigVersion
	summary := taskSummary(latest)
	if summary["pending"] > 0 || summary["running"] > 0 || summary["failed"] == 0 {
		status, statusErr := s.deploymentStatus(r.Context(), latest)
		if statusErr != nil {
			fail(w, statusErr, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"deployment_status": status})
		return
	}
	actor := currentUser(r)
	if actor == nil {
		fail(w, errors.New("authentication required"), http.StatusUnauthorized)
		return
	}
	if err := s.store.DismissDeploymentFailure(r.Context(), version, actor.ID); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "dismiss", "deployment", fmt.Sprint(version))
	status, err := s.deploymentStatus(r.Context(), latest)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"deployment_status": status})
}

func (s *Server) agentTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := s.store.ListTasks(r.Context(), intQuery(r, "limit", 300))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"tasks": sanitizeTasksForRole(items, currentRole(r))})
}

func (s *Server) agentTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	id := idFromPath(r.URL.Path, "/api/v1/agent-tasks/")
	if id == 0 {
		fail(w, errors.New("missing id"), 400)
		return
	}
	task, err := s.store.GetTask(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	write(w, 200, map[string]any{"task": sanitizeTaskForRole(*task, currentRole(r))})
}

func taskSummary(tasks []model.AgentTask) map[string]int {
	out := map[string]int{"total": len(tasks), "pending": 0, "running": 0, "succeeded": 0, "failed": 0}
	for _, task := range tasks {
		switch task.Status {
		case "pending":
			out["pending"]++
		case "running":
			out["running"]++
		case "succeeded":
			out["succeeded"]++
		default:
			if strings.Contains(task.Status, "fail") {
				out["failed"]++
			}
		}
	}
	return out
}

type generatedServerCoreConfig struct {
	Config          string
	Assets          []model.ManagedAssetReference
	Inbounds        []model.Inbound
	TrafficPolicies map[int64]model.TrafficRuntimePolicy
}

// deploymentConfigErrorStatus separates desired state the operator can correct
// from genuine server faults, so a listener conflict does not surface as a 500.
func deploymentConfigErrorStatus(err error) int {
	if errors.Is(err, core.ErrInvalidDesiredState) {
		return 400
	}
	if errors.Is(err, errCertificateProvisioning) {
		return http.StatusConflict
	}
	return 500
}

func (s *Server) generateServerCoreConfigWithLedger(ctx context.Context, server model.Server, data store.FullRoutingConfig, ledger *core.ProxyPathPortLedger) (generatedServerCoreConfig, error) {
	if ledger == nil {
		ledger = core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	}
	return s.generateServerCoreConfigInner(ctx, server, data, ledger, true)
}

func routingRuleUsesHostInterface(rule model.RoutingRule, serverID int64) bool {
	return rule.Enabled && rule.ServerID == serverID && strings.TrimSpace(rule.InterfaceName) != ""
}

func (s *Server) routingRulesWithInterfaceIPStacks(ctx context.Context, serverID int64, rules []model.RoutingRule) ([]model.RoutingRule, error) {
	needsInventory := false
	for _, rule := range rules {
		if routingRuleUsesHostInterface(rule, serverID) {
			needsInventory = true
			break
		}
	}
	if !needsInventory {
		return rules, nil
	}
	task, err := s.store.LastSuccessfulTaskByServerType(ctx, serverID, model.AgentTaskTypeListNetworkInterfaces)
	if errors.Is(err, sql.ErrNoRows) {
		return rules, nil
	}
	if err != nil {
		return nil, err
	}
	var result struct {
		Interfaces []model.NetworkInterfaceInfo `json:"interfaces"`
	}
	if err := json.Unmarshal([]byte(task.ResultJSON), &result); err != nil {
		return nil, fmt.Errorf("decode server %d network interface inventory: %w", serverID, err)
	}
	return applyNetworkInterfaceBindInfo(rules, serverID, result.Interfaces), nil
}

func applyNetworkInterfaceBindInfo(rules []model.RoutingRule, serverID int64, interfaces []model.NetworkInterfaceInfo) []model.RoutingRule {
	type bindInfo struct {
		stack model.IPStack
		v4    bool
		v6    bool
	}
	inventory := make(map[string]bindInfo, len(interfaces))
	for _, networkInterface := range interfaces {
		name := strings.TrimSpace(networkInterface.Name)
		v4, v6 := networkInterfaceGlobalFamilies(networkInterface)
		inventory[name] = bindInfo{stack: networkInterfaceIPStack(networkInterface), v4: v4, v6: v6}
	}
	resolved := append([]model.RoutingRule(nil), rules...)
	for index := range resolved {
		rule := &resolved[index]
		if !routingRuleUsesHostInterface(*rule, serverID) {
			continue
		}
		info, ok := inventory[strings.TrimSpace(rule.InterfaceName)]
		if !ok {
			continue
		}
		rule.InterfaceIPStack = info.stack
		rule.InterfaceBindKnown = true
		rule.InterfaceHasGlobalIPv4 = info.v4
		rule.InterfaceHasGlobalIPv6 = info.v6
	}
	return resolved
}

func networkInterfaceGlobalFamilies(networkInterface model.NetworkInterfaceInfo) (ipv4, ipv6 bool) {
	for _, raw := range networkInterface.Addresses {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		address := prefix.Addr().Unmap()
		if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsPrivate() {
			continue
		}
		if address.Is4() {
			ipv4 = true
		} else if address.Is6() {
			ipv6 = true
		}
	}
	return ipv4, ipv6
}

func networkInterfaceIPStack(networkInterface model.NetworkInterfaceInfo) model.IPStack {
	var ipv4, ipv6 bool
	for _, raw := range networkInterface.Addresses {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		address := prefix.Addr().Unmap()
		if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() {
			continue
		}
		if address.Is4() {
			ipv4 = true
		} else if address.Is6() {
			ipv6 = true
		}
	}
	switch {
	case ipv4 && ipv6:
		return model.IPStackDualStack
	case ipv4:
		return model.IPStackIPv4Only
	case ipv6:
		return model.IPStackIPv6Only
	default:
		return model.IPStackAuto
	}
}

func (s *Server) generateServerCoreConfigInner(ctx context.Context, server model.Server, data store.FullRoutingConfig, ledger *core.ProxyPathPortLedger, includeTrafficRuntime bool) (generatedServerCoreConfig, error) {
	var err error
	data.RoutingRules, err = s.routingRulesWithInterfaceIPStacks(ctx, server.ID, data.RoutingRules)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	resolveRoutingProxyPathNames(&data)
	inbounds, assets, err := s.prepareCertificateInbounds(ctx, data.Inbounds, server.ID)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	routingAssets := routingRuleSetAssetReferences(server.ID, data.RoutingRules, data.RoutingRuleSets)
	assets = append(assets, routingAssets...)
	dnsState, err := core.DNSConfigStateForServer(server.ID, data.DNSLists, data.ServerDNSPolicies)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	bindings, pathBindings, userPolicies, err := s.runtimeAccessBindings(ctx, data)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	if includeTrafficRuntime {
		if users, listErr := s.store.ListUsers(ctx); listErr == nil && len(users) > 0 {
			data.Users = users
		}
	}
	accountingUsers := core.TrafficAccountingUsersForServer(server.ID, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, bindings, pathBindings)
	var trafficPolicies map[int64]model.TrafficRuntimePolicy
	if includeTrafficRuntime {
		trafficPolicies, err = s.trafficRuntimePolicies(ctx, server.ID, data.Users, accountingUsers, userPolicies)
		if err != nil {
			return generatedServerCoreConfig{}, err
		}
	}
	config, err := core.GenerateServerConfigWithOptions(server, inbounds, data.Outbounds, dnsState, data.Users, core.ConfigOptions{
		RoutingRules: data.RoutingRules, RoutingRuleSets: data.RoutingRuleSets, ExternalOutbounds: data.ExternalOutbounds, ProxyPaths: data.ProxyPaths, ProxyPathSteps: data.ProxyPathSteps,
		Servers: data.Servers, Inbounds: inbounds, WARPProfiles: data.WARPProfiles, InboundUsers: bindings, ProxyPathUsers: pathBindings,
		UserPolicies: userPolicies, TrafficPolicies: trafficPolicies, UserDevices: data.UserDevices,
		PortLedger: ledger,
	})
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	return generatedServerCoreConfig{Config: config, Assets: assets, Inbounds: inbounds, TrafficPolicies: trafficPolicies}, nil
}

func requireReadyWARPForFocusedApply(data store.FullRoutingConfig, serverID int64) error {
	serverIDs, err := core.ProxyPathWARPServerIDs(data.ProxyPaths, data.ProxyPathSteps, data.Inbounds)
	if err != nil {
		return err
	}
	if !serverIDs[serverID] {
		return nil
	}
	profile, ok := findWARPProfileForServer(data.WARPProfiles, serverID)
	if !ok || profile.Status != model.WARPStatusReady || strings.TrimSpace(profile.ConfigJSON) == "" {
		return errors.New("WARP 配置尚未就绪，请先执行完整下发")
	}
	return nil
}

func (s *Server) queueCoreConfigRefreshForUserRemoval(ctx context.Context, userID int64, reason string) error {
	return s.queueCoreConfigRefresh(ctx, userID, reason, nil)
}

func (s *Server) queueCoreConfigRefreshForUser(ctx context.Context, userID int64, reason string) error {
	serverIDs, err := s.userAccountingServerIDs(ctx, userID)
	if err != nil {
		return err
	}
	return s.queueCoreConfigRefreshForServers(ctx, serverIDs, reason)
}

func (s *Server) queueCoreConfigRefreshForServers(ctx context.Context, serverIDs []int64, reason string) error {
	allowed := make(map[int64]bool, len(serverIDs))
	for _, serverID := range serverIDs {
		if serverID > 0 {
			allowed[serverID] = true
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	return s.queueCoreConfigRefresh(ctx, 0, reason, allowed)
}

func (s *Server) queueCoreConfigRefresh(ctx context.Context, userID int64, reason string, allowed map[int64]bool) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	// Resolving derived forwards seeds the ledger with the ports the generated
	// listeners already own, so the config below reuses them instead of picking
	// new ones.
	_, err = core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger)
	if err != nil {
		return err
	}
	type preparedCoreRefresh struct {
		serverID int64
		payload  model.ApplyCoreConfigTaskPayload
	}
	prepared := make([]preparedCoreRefresh, 0, len(data.Servers))
	for _, server := range data.Servers {
		if allowed != nil && !allowed[server.ID] {
			continue
		}
		if strings.TrimSpace(server.AgentID) == "" {
			continue
		}
		if err := requireReadyWARPForFocusedApply(data, server.ID); err != nil {
			return err
		}
		generated, err := s.generateServerCoreConfigWithLedger(ctx, server, data, ledger)
		if err != nil {
			return err
		}
		unchanged, err := s.serverConfigUnchanged(ctx, server.ID, generated.Config)
		if err != nil {
			return err
		}
		if unchanged {
			continue
		}
		payload := model.ApplyCoreConfigTaskPayload{Config: generated.Config, Reason: reason, PrunedUserID: userID, Assets: generated.Assets}
		prepared = append(prepared, preparedCoreRefresh{serverID: server.ID, payload: payload})
	}
	if len(prepared) == 0 {
		return nil
	}
	preparedServerIDs := make(map[int64]bool, len(prepared))
	for _, item := range prepared {
		preparedServerIDs[item.serverID] = true
	}
	pendingAllocations := make([]model.ProxyPathPortAllocation, 0)
	for _, allocation := range ledger.Pending() {
		if preparedServerIDs[allocation.ServerID] {
			pendingAllocations = append(pendingAllocations, allocation)
		}
	}
	staleCandidates := core.StaleProxyPathPortAllocationIDs(data.ProxyPathPortAllocations, ledger)
	staleCandidateSet := make(map[int64]bool, len(staleCandidates))
	for _, id := range staleCandidates {
		staleCandidateSet[id] = true
	}
	staleAllocationIDs := make([]int64, 0, len(staleCandidates))
	for _, allocation := range data.ProxyPathPortAllocations {
		if preparedServerIDs[allocation.ServerID] && staleCandidateSet[allocation.ID] {
			staleAllocationIDs = append(staleAllocationIDs, allocation.ID)
		}
	}
	if err := s.store.SaveProxyPathPortAllocations(ctx, pendingAllocations, staleAllocationIDs); err != nil {
		return err
	}
	version, err := s.store.NextConfigVersion(ctx)
	if err != nil {
		return err
	}
	for _, item := range prepared {
		if _, err := s.queueAgentTask(ctx, item.serverID, model.AgentTaskTypeApplyCoreConfig, item.payload, version); err != nil {
			return err
		}
		s.applyCoreConfigCreatedTotal.Add(1)
	}
	return nil
}

func findWARPProfileForServer(items []model.WARPProfile, serverID int64) (model.WARPProfile, bool) {
	for _, item := range items {
		if item.ServerID == serverID {
			return item, true
		}
	}
	return model.WARPProfile{}, false
}

// warpRequestMTU is the WireGuard tunnel MTU sent to the Agent for a WARP
// profile request. WARP always uses the fixed 1280 value; it never inherits
// server.MTUValue, which is the main-network MTU and would push encrypted
// outer packets past the path MTU (fragmented datagrams are dropped on many
// paths, stalling page loads while small control packets still pass).
func warpRequestMTU(profile model.WARPProfile) int {
	return core.WarpTunnelMTU
}

func replaceWARPProfile(items []model.WARPProfile, next model.WARPProfile) []model.WARPProfile {
	out := append([]model.WARPProfile(nil), items...)
	for i := range out {
		if out[i].ID == next.ID {
			out[i] = next
			return out
		}
	}
	return append(out, next)
}

func immediateForwardProbePlan(plan model.PortForwardPlan) model.PortForwardPlan {
	out := model.PortForwardPlan{Version: plan.Version}
	for _, rule := range plan.Rules {
		if rule.ProbeMode != "never" {
			out.Rules = append(out.Rules, rule)
		}
	}
	return out
}

func (s *Server) subscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if !s.allowRate(w, r, "subscription-ip:"+clientIP(r), 120, time.Minute) {
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/api/v1/subscriptions/")
	if token == "" {
		fail(w, errors.New("missing token"), 400)
		return
	}
	customCredential, custom := isSubscriptionCustomCredential(r)
	rateKind := "subscription-token:"
	if custom {
		rateKind = "subscription-custom-path:"
	} else if strings.HasPrefix(token, "obd_") {
		rateKind = "subscription-device:"
	}
	if !s.allowRate(w, r, rateKind+token, 60, time.Minute) {
		return
	}
	var user *model.User
	var device *model.UserDevice
	deviceTokenHash := ""
	var err error
	if custom {
		user = &customCredential.User
	} else if strings.HasPrefix(token, "obd_") {
		deviceTokenHash = security.HashAPISecret(s.sessionSecret, token)
		device, err = s.store.GetUserDeviceByTokenHash(r.Context(), deviceTokenHash)
		if err == nil {
			user, err = s.store.GetUser(r.Context(), device.UserID)
		}
	} else {
		user, err = s.store.GetUserBySubscriptionToken(r.Context(), token)
	}
	if err != nil || user == nil || user.Status != "active" {
		fail(w, errors.New("invalid subscription link"), 404)
		return
	}
	if device == nil && !user.LegacyProxyEnabled {
		fail(w, errors.New("this account requires a device-specific subscription link"), http.StatusForbidden)
		return
	}
	subscriptionUser := *user
	if device != nil {
		subscriptionUser = core.UserForDevice(*user, *device)
	}
	resolution := core.ResolveSubscriptionFormat(model.SubscriptionFormat(r.URL.Query().Get("format")), r.UserAgent())
	if !core.IsSupportedSubscriptionFormat(resolution.Requested) {
		s.recordRejectedSubscriptionPull(r, user.ID, resolution, nil, false, "unsupported subscription format")
		fail(w, fmt.Errorf("unsupported subscription format %q", r.URL.Query().Get("format")), 400)
		return
	}
	format := resolution.Resolved
	settings, err := s.store.ListSettings(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	ageRecipient, ageEncrypted, err := resolveSubscriptionAgeRecipient(r, subscriptionUser, settings[settingSubscriptionAgePolicy], format)
	if err != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, resolution, nil, false, err.Error())
		status := http.StatusBadRequest
		if errors.Is(err, errSubscriptionAgeKeyRequired) {
			status = http.StatusPreconditionRequired
		} else if errors.Is(err, errSubscriptionAgeNotEnabled) {
			status = http.StatusForbidden
		}
		fail(w, err, status)
		return
	}
	var requestedProfileID *int64
	var subscriptionOutput *model.SubscriptionOutput
	if rawProfileID := strings.TrimSpace(r.URL.Query().Get("profile_id")); rawProfileID != "" {
		profileID, parseErr := strconv.ParseInt(rawProfileID, 10, 64)
		if parseErr != nil || profileID <= 0 {
			s.recordRejectedSubscriptionPull(r, user.ID, resolution, nil, ageEncrypted, "invalid profile_id")
			fail(w, errors.New("invalid profile_id"), http.StatusBadRequest)
			return
		}
		subscriptionOutput, err = s.store.GetSubscriptionOutput(r.Context(), user.ID, profileID)
		if err != nil || !subscriptionOutput.Enabled {
			s.recordRejectedSubscriptionPull(r, user.ID, resolution, &profileID, ageEncrypted, "subscription profile not found")
			fail(w, errors.New("subscription profile not found"), http.StatusNotFound)
			return
		}
	} else {
		subscriptionOutput, err = s.store.GetDefaultSubscriptionOutput(r.Context(), user.ID)
		if err != nil {
			fail(w, err, 500)
			return
		}
	}
	requestedProfileID = &subscriptionOutput.ID
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	servers, in := data.Servers, data.Inbounds
	snapshot, err := s.buildAccessSnapshot(r.Context(), data)
	if err != nil {
		fail(w, err, 500)
		return
	}
	effectiveNodes := snapshot.EffectiveNodeKeys(user.ID)
	effectiveGroups := snapshot.EffectiveNodeGroups(user.ID)
	hiddenInbounds, err := s.store.ListHiddenInboundIDs(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	for inboundID := range hiddenInbounds {
		delete(effectiveNodes, core.NodeKeyOf(model.AssignableNodeInbound, inboundID))
	}
	for _, path := range data.ProxyPaths {
		if hiddenInbounds[path.InboundID] {
			delete(effectiveNodes, core.NodeKeyOf(model.AssignableNodeProxyPath, path.ID))
		}
	}
	orderPolicy, orderPositions, planNodeNames, err := s.store.GetEffectiveSubscriptionNodePresentation(r.Context(), user.ID, time.Now())
	if err != nil {
		fail(w, err, 500)
		return
	}
	nodeMetadata, err := s.store.ListNodeMetadata(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	globalNodeNames := map[string]*string{}
	for key, metadata := range nodeMetadata {
		if metadata.DisplayNameOverride != nil {
			globalNodeNames[key] = metadata.DisplayNameOverride
		}
	}
	pullPathUsers := snapshot.ProxyPathUserBindings()
	sshServerHostKeys, err := s.subscriptionSSHServerHostKeys(r.Context(), subscriptionUser, data, snapshot.InboundUserBindings(), pullPathUsers)
	if err != nil {
		fail(w, err, 500)
		return
	}
	opts := core.SubscriptionOptions{
		Format:                 format,
		ProxyPaths:             data.ProxyPaths,
		ProxyPathSteps:         data.ProxyPathSteps,
		RoutingRules:           data.RoutingRules,
		ProxyPathEgressResults: data.ProxyPathEgressResults,
		ExternalOutbounds:      data.ExternalOutbounds,
		SSHServerHostKeys:      sshServerHostKeys,
		EffectiveNodes:         effectiveNodes,
		EffectiveNodeGroups:    effectiveGroups,
		NodeOrderPolicy:        model.SubscriptionNodeOrderPolicy{},
		NodeOrderPositions:     orderPositions,
		GlobalNodeNames:        globalNodeNames,
		PlanNodeNames:          planNodeNames,
		AlwaysUseDomainHost:    settingBool(settings, settingSubscriptionAlwaysUseDomainHost, false),
		PortLedger:             core.NewProxyPathPortLedger(data.ProxyPathPortAllocations),
	}
	if orderPolicy != nil {
		opts.NodeOrderPolicy = *orderPolicy
	}
	oboardNodes, err := core.BuildSubscriptionNodes(subscriptionUser, servers, in, opts)
	if err != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, resolution, requestedProfileID, ageEncrypted, "subscription generation failed")
		fail(w, err, 500)
		return
	}
	selectedNodes, err := s.mergeWorkspaceOutputNodes(r.Context(), subscriptionUser, subscriptionOutput, oboardNodes)
	if err != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, resolution, requestedProfileID, ageEncrypted, "subscription profile generation failed")
		fail(w, err, 500)
		return
	}
	renderOpts, renderOptErr := core.ParseSubscriptionRenderOptions(format, r.URL.Query(), fmt.Sprintf("%d:%d", user.ID, subscriptionOutput.ID))
	if renderOptErr != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, resolution, requestedProfileID, ageEncrypted, renderOptErr.Error())
		fail(w, renderOptErr, http.StatusBadRequest)
		return
	}
	templateContent, templateDigest, err := s.store.EffectiveSubscriptionTemplateContent(r.Context(), format)
	if err != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, resolution, requestedProfileID, ageEncrypted, "subscription template load failed")
		fail(w, err, 500)
		return
	}
	renderOpts.Template = templateContent
	renderOpts.TemplateDigest = templateDigest
	renderOpts.UserAgent = r.UserAgent()
	renderOpts.RequestedFormat = resolution.Requested
	sub, err := core.RenderSubscriptionNodesWithOptions(selectedNodes, format, renderOpts)
	if err != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, resolution, requestedProfileID, ageEncrypted, "subscription generation failed")
		fail(w, err, 500)
		return
	}
	revisionDigest := sha256.Sum256([]byte("oboard-subscription-v3\x00" + strconv.FormatInt(subscriptionOutput.ID, 10) + "\x00" + string(format) + "\x00" + templateDigest + "\x00" + sub + "\x00" + fmt.Sprint(ageRecipient)))
	subscriptionRevision := fmt.Sprintf("sub_%x", revisionDigest[:16])
	etag := fmt.Sprintf("W/\"%s\"", subscriptionRevision)
	event := s.newSubscriptionPullAudit(r, user.ID, resolution, requestedProfileID, ageEncrypted)
	event.SubscriptionRevision = subscriptionRevision
	event.RouteID = s.subscriptionAuditRouteID(event)
	credentialGeneration := "legacy:" + security.HashAPISecret(s.sessionSecret, token)
	if device != nil {
		event.DeviceIDHash = device.DeviceIDHash
		credentialGeneration = fmt.Sprintf("device:%s:%d", device.DeviceIDHash, device.CredentialEpoch)
	}
	profileValue := int64(0)
	if requestedProfileID != nil {
		profileValue = *requestedProfileID
	}
	representationSeed := fmt.Sprintf("oboard-subscription-representation-v1\x00%d\x00%s\x00%s\x00%d\x00%s\x00%t\x00%s", user.ID, event.DeviceIDHash, credentialGeneration, profileValue, format, ageEncrypted, subscriptionRevision)
	representationHash := security.HashAPISecret(s.sessionSecret, representationSeed)
	event.RepresentationID = "rep_" + representationHash[:32]
	event.ConditionalRequest = subscriptionETagMatches(r.Header.Get("If-None-Match"), etag)
	auditState := s.auditSettingsState(r.Context())
	auditOptions := store.SubscriptionAuditOptions{
		AuditEnabled: auditState.Enabled && auditState.Subscription,
		Action:       auditState.Action,
	}
	var decision store.SubscriptionPullDecision
	if device != nil {
		decision, err = s.store.AuthorizeDeviceSubscriptionPull(r.Context(), user.ID, device.ID, deviceTokenHash, event, s.auditPolicy(r.Context()), auditOptions)
	} else if custom {
		decision, err = s.store.AuthorizeCustomSubscriptionPull(r.Context(), user.ID, token, event, s.auditPolicy(r.Context()), auditOptions)
	} else {
		decision, err = s.store.AuthorizeSubscriptionPull(r.Context(), user.ID, token, event, s.auditPolicy(r.Context()), auditOptions)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, errors.New("invalid subscription link"), 404)
			return
		}
		fail(w, err, 500)
		return
	}
	if auditState.Enabled && auditState.Subscription {
		s.notifySubscriptionAuditRisk(r.Context(), *user, decision)
		s.publishRealtime("audit", "subscriptions", "users", "user_overview")
	}
	if decision.RateLimited {
		if auditState.Enabled && auditState.Subscription {
			s.maybeNotifySubscriptionAbnormal(r.Context(), user.ID)
		}
		retrySeconds := int(math.Ceil(decision.RetryAfter.Seconds()))
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
		fail(w, errors.New("subscription request rate limit exceeded"), http.StatusTooManyRequests)
		return
	}
	if !decision.Allowed {
		if auditState.Enabled && auditState.Subscription {
			s.maybeNotifySubscriptionAbnormal(r.Context(), user.ID)
		}
		fail(w, errors.New("subscription access is suspended for this credential"), http.StatusForbidden)
		return
	}
	cacheControl := "private, no-cache, max-age=0"
	if decision.Burned {
		cacheControl = "no-store, max-age=0"
	}
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("ETag", etag)
	if resolution.Auto {
		appendHTTPVary(w.Header(), "User-Agent")
	}
	if decision.Burned {
		w.Header().Set("X-OBoard-Subscription", "burned-after-read")
	}
	if event.ConditionalRequest {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := []byte(sub)
	if ageEncrypted {
		body, err = encryptSubscriptionAgeArmor(sub, ageRecipient)
		if err != nil {
			s.recordRejectedSubscriptionPull(r, user.ID, resolution, requestedProfileID, true, "subscription encryption failed")
			fail(w, fmt.Errorf("encrypt subscription with age: %w", err), 500)
			return
		}
	}
	if ageEncrypted {
		w.Header().Set("Content-Type", "application/age")
		w.Header().Set("Subscription-Encryption", "age")
		w.Header().Add("Vary", "X-Age-Public-Key")
	} else {
		w.Header().Set("Content-Type", core.SubscriptionContentType(format))
	}
	// #nosec G705 -- subscription formats are JSON, YAML, or plain text; Content-Type and nosniff are set above.
	_, _ = w.Write(body)
}

func (s *Server) auditLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := s.store.ListAuditPage(r.Context(), intQuery(r, "limit", 100), intQuery(r, "offset", 0), r.URL.Query().Get("action"))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"audit_logs": items})
}

func (s *Server) agentEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "agent-enroll:"+clientIP(r), 10, time.Minute) {
		return
	}
	var req model.AgentEnrollRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.EnrollmentToken) == "" {
		fail(w, errors.New("invalid enrollment token"), 401)
		return
	}
	agentID, err := security.RandomToken(16)
	if err != nil {
		fail(w, err, 500)
		return
	}
	agentToken, err := security.RandomToken(32)
	if err != nil {
		fail(w, err, 500)
		return
	}
	hash := security.HashSecret(req.EnrollmentToken)
	server, err := s.store.ClaimServerEnrollment(r.Context(), hash, agentID, security.HashSecret(agentToken))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, errors.New("invalid enrollment token"), 401)
			return
		}
		fail(w, err, 500)
		return
	}
	if req.Health.OS != "" {
		server.OS = req.Health.OS
		server.DistroID = req.Health.DistroID
		server.DistroVersion = req.Health.DistroVersion
		server.DistroName = req.Health.DistroName
		server.Libc = req.Health.Libc
		server.ServiceManager = req.Health.ServiceManager
		server.PackageManager = req.Health.PackageManager
		server.Arch = req.Health.Arch
		applyDetectedEntryIPs(server, req.Health, clientIP(r))
		if code := normalizeControllerRegionCode(req.Health.RegionCode); code != "" {
			server.DetectedRegionCode = code
		}
		server.SingBoxVersion = req.Health.SingBoxVersion
		server.KernelCapabilities = normalizeKernelCapabilities(req.Health.KernelCapabilities)
		server.CPU = req.Health.CPU
		if req.Health.CPUCores > 0 {
			server.CPUCores = req.Health.CPUCores
		}
		server.MemoryBytes = req.Health.MemoryBytes
		server.CPUUsagePercent = req.Health.CPUUsagePercent
		server.MemoryUsedBytes = req.Health.MemoryUsedBytes
		server.MemoryTotalBytes = req.Health.MemoryTotalBytes
		server.AgentMemoryBytes = req.Health.AgentMemoryBytes
		server.AgentVersion = req.Health.AgentVersion
		server.AgentBuild = req.Health.AgentBuild
		if err := s.store.UpdateServer(r.Context(), server); err != nil {
			fail(w, err, 500)
			return
		}
	}
	// A reinstalled server starts from empty local state. Push the current
	// desired state immediately so its first connection applies the topology
	// instead of waiting for a manual deployment. Brand-new servers without any
	// topology are skipped by the relevance gate inside the helper.
	s.queueDeploymentAfterReconnect(r.Context(), server.ID)
	_ = s.store.AddAudit(r.Context(), model.AuditLog{Action: "agent_enroll", Target: "server", Detail: server.Name, IP: clientIP(r)})
	log.Printf("agent enrolled server=%d(%s) agent_id=%s remote=%s", server.ID, safeLogField(server.Name), safeLogField(agentID), safeLogField(clientIP(r)))
	write(w, 200, model.AgentEnrollResponse{ServerID: server.ID, AgentID: agentID, AgentToken: agentToken, ConnectionAuditEnabled: s.effectiveConnectionAuditEnabled(r.Context(), server)})
}

func applyDetectedEntryIPs(server *model.Server, health model.HealthReport, remote string) {
	for _, value := range []string{health.PublicIPv4, health.PublicIPv6, remote} {
		ip, family := cleanPublicEntryIP(value)
		if ip == "" {
			continue
		}
		if family == "ipv4" && server.PublicIPv4 == "" {
			server.PublicIPv4 = ip
		}
		if family == "ipv6" && server.PublicIPv6 == "" {
			server.PublicIPv6 = ip
		}
	}
	if ip, family := cleanPublicEntryIP(health.InterfaceIPv6); family == "ipv6" {
		server.InterfaceIPv6 = ip
	} else {
		server.InterfaceIPv6 = ""
	}
}

func cleanPublicEntryIP(raw string) (string, string) {
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = strings.Trim(host, "[]")
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() {
		return "", ""
	}
	if addr.Is4() {
		return addr.String(), "ipv4"
	}
	return addr.String(), "ipv6"
}

func (s *Server) trackAgentConnection(ctx context.Context, serverID int64, connected bool, effectiveAt time.Time) {
	s.agentConnectionMu.Lock()
	previous := s.agentConnectionCount[serverID]
	next := previous
	if connected {
		next++
	} else if next > 0 {
		next--
	}
	if next == 0 {
		delete(s.agentConnectionCount, serverID)
	} else {
		s.agentConnectionCount[serverID] = next
	}
	s.agentConnectionMu.Unlock()
	if (connected && previous == 0) || (!connected && previous > 0 && next == 0) {
		source := model.ConnectivityEventSourceAgentSocket
		if !connected && s.controllerUpdateMaintenance.Load() {
			source = model.ConnectivityEventSourceControllerUpdate
		}
		_ = s.store.RecordControllerConnectionEventWithSource(ctx, serverID, connected, effectiveAt.UTC(), source)
	}
}

func (s *Server) agentConnect(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowAgentRate(w, "agent-connect:"+server.AgentID, 30, time.Minute) {
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20) // 1 MiB max agent websocket frame
	log.Printf("agent connected server=%d(%s) agent_id=%s remote=%s", server.ID, safeLogField(server.Name), safeLogField(server.AgentID), safeLogField(clientIP(r)))
	connectedAt := time.Now()
	s.trackAgentConnection(r.Context(), server.ID, true, connectedAt.UTC())
	controlCh := make(chan any, 16)
	s.registerAgentLive(server.ID, controlCh)
	defer func() {
		s.unregisterAgentLive(server.ID, controlCh)
		s.trackAgentConnection(context.Background(), server.ID, false, time.Now().UTC())
		log.Printf("agent disconnected server=%d(%s) connected_for=%s", server.ID, safeLogField(server.Name), time.Since(connectedAt).Round(time.Second))
	}()
	mode, _ := serverMonitoringPolicy(server)
	auditEnabled := s.effectiveConnectionAuditEnabled(r.Context(), server)
	if !auditEnabled {
		_ = s.store.ClearConnectionPresenceForServer(r.Context(), server.ID)
	}
	hello := map[string]any{"type": "hello", "server_id": server.ID, "monitoring_mode": mode, "connection_audit_enabled": auditEnabled}
	for key, value := range s.configurationHeartbeatFields(r.Context(), server.ID) {
		hello[key] = value
	}
	if plan, err := latencyProbePlanForServer(r.Context(), *server); err == nil {
		hello["latency_probe_plan"] = plan
	}
	writeAgentJSON := func(payload any) error {
		return conn.WriteJSON(s.withControllerTime(payload))
	}
	_ = writeAgentJSON(hello)
	type agentSocketRead struct {
		message map[string]json.RawMessage
		err     error
	}
	reads := make(chan agentSocketRead, 8)
	go func() {
		for {
			var message map[string]json.RawMessage
			err := conn.ReadJSON(&message)
			select {
			case reads <- agentSocketRead{message: message, err: err}:
			case <-r.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var inFlightTaskID int64
	var inFlightTaskType string
	var inFlightTimer *time.Timer
	var inFlightTimeout <-chan time.Time
	defer func() {
		if inFlightTimer != nil {
			inFlightTimer.Stop()
		}
		if inFlightTaskID == 0 {
			return
		}
		if inFlightTaskType == model.AgentTaskTypeRemoteExec || inFlightTaskType == model.AgentTaskTypeRemoteOperation {
			result, _ := json.Marshal(map[string]any{
				"error":    "agent connection closed before remote execution result was acknowledged",
				"code":     "remote_exec_result_lost",
				"agent_id": server.AgentID,
			})
			if err := s.store.CompleteTask(context.Background(), inFlightTaskID, "failed", string(result)); err != nil {
				log.Printf("complete remote exec task %d after agent disconnect: %v", inFlightTaskID, err)
			} else {
				s.publishRealtime(realtimeResourcesForTask(inFlightTaskType)...)
			}
			return
		}
		result, _ := json.Marshal(map[string]any{
			"message":  "agent connection closed before task result was acknowledged; task requeued",
			"agent_id": server.AgentID,
		})
		if err := s.store.RequeueTaskIfRunning(context.Background(), inFlightTaskID, string(result)); err != nil {
			log.Printf("requeue task %d after agent disconnect: %v", inFlightTaskID, err)
		} else {
			s.publishRealtime(realtimeResourcesForTask(inFlightTaskType)...)
		}
	}()
	// Task delivery is event-driven: the notifier wakes this loop when a task
	// becomes claimable, and the loop additionally claims on connect and after
	// every task_ack so the next queued task is dispatched immediately. The
	// jittered recovery scan re-wakes this channel after lost notifications;
	// NextTask remains the atomic, database-backed claim.
	notifyCh := s.tasks.channel(server.ID)
	claimTask := func() {
		if inFlightTaskID != 0 {
			return
		}
		task, err := s.store.NextTask(r.Context(), server.ID)
		if err != nil {
			return
		}
		inFlightTaskID = task.ID
		inFlightTaskType = task.Type
		log.Printf("task dispatched server=%d(%s) id=%d type=%s version=%d", server.ID, safeLogField(server.Name), task.ID, task.Type, task.ConfigVersion)
		s.publishRealtime(realtimeResourcesForTask(task.Type)...)
		taskTimeout := 10 * time.Minute
		if task.Type == model.AgentTaskTypeIssueCertificateHTTP {
			taskTimeout = 20 * time.Minute
		}
		inFlightTimer = time.NewTimer(taskTimeout)
		inFlightTimeout = inFlightTimer.C
		if err := writeAgentJSON(map[string]any{"type": "task_request", "task": task, "signature_version": 2, "signature": signAgentTaskEnvelope(server.AgentTokenHash, *task)}); err != nil {
			// The socket is broken; the read side observes the failure and
			// the deferred requeue returns the task to the queue.
			return
		}
	}
	claimTask()
	_, heartbeatInterval := serverMonitoringPolicy(server)
	heartbeatTimer := time.NewTimer(heartbeatInterval)
	defer heartbeatTimer.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case received := <-reads:
			if received.err != nil {
				if websocket.IsUnexpectedCloseError(received.err) {
					log.Printf("agent ws closed: %v", received.err)
				}
				return
			}
			var envelope struct {
				Type   string `json:"type"`
				TaskID int64  `json:"task_id"`
			}
			if raw, ok := received.message["type"]; ok {
				_ = json.Unmarshal(raw, &envelope.Type)
			}
			if raw, ok := received.message["task_id"]; ok {
				_ = json.Unmarshal(raw, &envelope.TaskID)
			}
			acknowledged := false
			if envelope.Type == "task_ack" && envelope.TaskID == inFlightTaskID {
				acknowledged = true
				inFlightTaskID = 0
				inFlightTaskType = ""
				if inFlightTimer != nil {
					inFlightTimer.Stop()
					inFlightTimer = nil
					inFlightTimeout = nil
				}
			}
			if envelope.Type == "interactive_ready" || envelope.Type == "interactive_failed" {
				s.handleInteractiveAgentStatus(server.ID, received.message)
			}
			acceptedLatencyReportID, acceptedMetricReportID := s.processAgentSocketMessage(r.Context(), server, received.message, clientIP(r))
			if acceptedLatencyReportID != "" {
				if err := writeAgentJSON(map[string]any{"type": "latency_probe_ack", "report_id": acceptedLatencyReportID}); err != nil {
					return
				}
			}
			if acceptedMetricReportID != "" {
				if err := writeAgentJSON(map[string]any{"type": "metric_report_ack", "report_id": acceptedMetricReportID}); err != nil {
					return
				}
			}
			if acknowledged {
				// Deliver the next queued task without waiting for a wake.
				claimTask()
			}
		case <-notifyCh:
			claimTask()
		case payload := <-controlCh:
			if err := writeAgentJSON(payload); err != nil {
				return
			}
		case <-heartbeatTimer.C:
			if latest, loadErr := s.store.GetServer(r.Context(), server.ID); loadErr == nil {
				server = latest
			}
			mode, heartbeatInterval = serverMonitoringPolicy(server)
			auditEnabled = s.effectiveConnectionAuditEnabled(r.Context(), server)
			if !auditEnabled {
				_ = s.store.ClearConnectionPresenceForServer(r.Context(), server.ID)
			}
			heartbeat := map[string]any{"type": "heartbeat", "monitoring_mode": mode, "connection_audit_enabled": auditEnabled}
			for key, value := range s.configurationHeartbeatFields(r.Context(), server.ID) {
				heartbeat[key] = value
			}
			if plan, planErr := latencyProbePlanForServer(r.Context(), *server); planErr == nil {
				heartbeat["latency_probe_plan"] = plan
			}
			if err := writeAgentJSON(heartbeat); err != nil {
				return
			}
			heartbeatTimer.Reset(heartbeatInterval)
		case <-inFlightTimeout:
			log.Printf("agent task timed out server=%d id=%d type=%s", server.ID, inFlightTaskID, safeLogField(inFlightTaskType))
			return
		}
	}
}

func serverMonitoringPolicy(server *model.Server) (string, time.Duration) {
	if server != nil && strings.EqualFold(strings.TrimSpace(server.MonitoringMode), "standard") {
		return "standard", 10 * time.Second
	}
	return "lightweight", 20 * time.Second
}

func signAgentTaskEnvelope(secret string, task model.AgentTask) string {
	return security.SignTaskEnvelope(secret, security.TaskEnvelope{ID: task.ID, ServerID: task.ServerID, Type: task.Type, ConfigVersion: task.ConfigVersion, Nonce: task.Nonce, PayloadJSON: task.PayloadJSON})
}

func (s *Server) processAgentSocketMessage(ctx context.Context, server *model.Server, msg map[string]json.RawMessage, remoteIP string) (string, string) {
	acceptedReportID := ""
	acceptedMetricReportID := ""
	if raw, ok := msg["latency_probe_report"]; ok {
		var report model.LatencyProbeResultReport
		if err := json.Unmarshal(raw, &report); err != nil {
			log.Printf("reject latency probe report server=%d: %v", server.ID, err)
		} else if err := validateAutonomousLatencyProbeReport(&report); err != nil {
			log.Printf("reject latency probe report server=%d: %v", server.ID, err)
		} else if err := s.store.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
			log.Printf("save latency probe report server=%d: %v", server.ID, err)
		} else {
			acceptedReportID = report.ReportID
			s.publishRealtime("server_metrics", "latency_probes")
		}
	}
	if raw, ok := msg["metric_report"]; ok {
		var report model.MetricReport
		if err := json.Unmarshal(raw, &report); err != nil {
			log.Printf("reject metric report server=%d: %v", server.ID, err)
		} else if err := validateMetricReport(&report, time.Now().UTC()); err != nil {
			log.Printf("reject metric report server=%d: %v", server.ID, err)
		} else if inserted, err := s.store.SaveMetricReport(ctx, server.ID, report); err != nil {
			log.Printf("save metric report server=%d: %v", server.ID, err)
		} else {
			acceptedMetricReportID = report.ReportID
			if inserted {
				s.publishRealtime("server_metrics")
			}
		}
	}
	if raw, ok := msg["presence_delta"]; ok {
		var delta connectionPresenceDelta
		if err := json.Unmarshal(raw, &delta); err == nil {
			if accepted, err := s.acceptConnectionPresenceDelta(ctx, server, delta); err != nil {
				log.Printf("reject connection presence server=%d: %v", server.ID, err)
			} else if len(accepted) > 0 {
				s.publishRealtime("audit", "connection_presence")
			}
		}
	}
	if raw, ok := msg["health_report"]; ok {
		var h model.HealthReport
		if json.Unmarshal(raw, &h) == nil {
			h.AgentID = server.AgentID
			sanitizeServerHealthReport(&h)
			if h.PublicIPv4 == "" || h.PublicIPv6 == "" {
				ip, family := cleanPublicEntryIP(remoteIP)
				if family == "ipv4" && h.PublicIPv4 == "" {
					h.PublicIPv4 = ip
				}
				if family == "ipv6" && h.PublicIPv6 == "" {
					h.PublicIPv6 = ip
				}
			}
			// The in-memory server was refreshed on the last heartbeat, so no
			// per-report GetServer query is needed; settings come from the
			// revision-keyed in-memory snapshot.
			settings := s.runtimeSettings(ctx)
			_, start, end := trafficWindow(time.Now(), server.TrafficResetMode, server.TrafficResetDay, time.Time{}, trafficLocation(settings))
			window := model.ServerTrafficWindow{Key: start.Format("2006-01-02"), Start: start, End: end}
			result, err := s.store.ApplyHealthReport(ctx, server.ID, h, window)
			if err == nil {
				// Refresh the connection's in-memory server copy so heartbeat
				// and plan generation observe the report without a reload.
				applyHealthReportToServer(server, result)
				_ = s.store.UpsertServerRemoteAccessStatus(ctx, server.ID, h.RemoteAccess)
				s.reconcileAgentAppliedState(ctx, server.ID, h)
				s.completeAgentUpdateAfterReconnect(ctx, server.ID, h.AgentBuild)
				s.publishServerPatch(result)
			}
			if err == nil && result.StatusChanged && result.OldStatus == model.ServerOffline && result.NewStatus == model.ServerOnline {
				log.Printf("server %d(%s) recovered and is online again", server.ID, safeLogField(server.Name))
				s.handleServerRecovered(ctx, server.ID)
			}
		}
	}
	return acceptedReportID, acceptedMetricReportID
}

func validateMetricReport(report *model.MetricReport, now time.Time) error {
	report.ReportID = strings.TrimSpace(report.ReportID)
	if report.ReportID == "" || len(report.ReportID) > 128 {
		return errors.New("metric report id is invalid")
	}
	if report.SampledAt.IsZero() || report.SampledAt.Before(now.Add(-35*24*time.Hour)) || report.SampledAt.After(now.Add(2*time.Minute)) {
		return errors.New("metric report timestamp is outside the accepted window")
	}
	report.SampledAt = report.SampledAt.UTC()
	if math.IsNaN(report.CPUUsagePercent) || math.IsInf(report.CPUUsagePercent, 0) || report.CPUUsagePercent < 0 || report.CPUUsagePercent > 100 {
		return errors.New("metric report cpu usage is invalid")
	}
	if report.MemoryTotalBytes > 0 && report.MemoryUsedBytes > report.MemoryTotalBytes {
		return errors.New("metric report memory usage is invalid")
	}
	if report.DiskTotalBytes > 0 && report.DiskUsedBytes > report.DiskTotalBytes {
		return errors.New("metric report disk usage is invalid")
	}
	const maxSystemCount = uint64(10_000_000)
	if report.TCPConnectionCount > maxSystemCount || report.UDPConnectionCount > maxSystemCount || report.ProcessCount > maxSystemCount {
		return errors.New("metric report system count is invalid")
	}
	const maxNetworkBPS = uint64(100 << 30)
	if report.NetworkUploadBPS > maxNetworkBPS || report.NetworkDownloadBPS > maxNetworkBPS {
		return errors.New("metric report network rate is invalid")
	}
	return nil
}

func sanitizeServerHealthReport(report *model.HealthReport) {
	now := time.Now().UTC()
	if report.Timestamp.IsZero() || report.Timestamp.Before(now.Add(-5*time.Minute)) || report.Timestamp.After(now.Add(2*time.Minute)) {
		report.Timestamp = now
	} else {
		report.Timestamp = report.Timestamp.UTC()
	}
	if math.IsNaN(report.CPUUsagePercent) || math.IsInf(report.CPUUsagePercent, 0) || report.CPUUsagePercent < 0 {
		report.CPUUsagePercent = 0
	}
	if report.CPUUsagePercent > 100 {
		report.CPUUsagePercent = 100
	}
	if report.MemoryTotalBytes > 0 && report.MemoryUsedBytes > report.MemoryTotalBytes {
		report.MemoryUsedBytes = report.MemoryTotalBytes
	}
	if report.DiskTotalBytes > 0 && report.DiskBytes > report.DiskTotalBytes {
		report.DiskBytes = report.DiskTotalBytes
	}
	const maxPlausibleSystemCount = uint64(10_000_000)
	if report.TCPConnectionCount > maxPlausibleSystemCount {
		report.TCPConnectionCount = 0
	}
	if report.UDPConnectionCount > maxPlausibleSystemCount {
		report.UDPConnectionCount = 0
	}
	if report.ProcessCount > maxPlausibleSystemCount {
		report.ProcessCount = 0
	}
	const maxPlausibleNetworkBPS = uint64(100 << 30)
	if report.NetworkUploadBPS > maxPlausibleNetworkBPS {
		report.NetworkUploadBPS = 0
	}
	if report.NetworkDownloadBPS > maxPlausibleNetworkBPS {
		report.NetworkDownloadBPS = 0
	}
	report.RegionCode = normalizeControllerRegionCode(report.RegionCode)
	report.KernelCapabilities = normalizeKernelCapabilities(report.KernelCapabilities)
	normalizeReportedTCPFastOpen(report)
	normalizeReportedCPUCores(report)
}

func normalizeReportedCPUCores(report *model.HealthReport) {
	if report.CPUCores < 0 {
		report.CPUCores = 0
	}
	const maxCPUCores = 4096
	if report.CPUCores > maxCPUCores {
		report.CPUCores = maxCPUCores
	}
}

// normalizeReportedTCPFastOpen keeps the raw net.ipv4.tcp_fastopen bitmask as
// the only authority: a reported state is recomputed from it so an Agent cannot
// claim server-side TFO that the kernel bitmask does not grant.
func normalizeReportedTCPFastOpen(report *model.HealthReport) {
	const maxTCPFastOpenMask = 0xFFFF
	switch model.NormalizeTCPFastOpenState(report.TCPFastOpenState) {
	case model.TCPFastOpenStateUnknown:
		report.TCPFastOpenState, report.TCPFastOpenValue = model.TCPFastOpenStateUnknown, 0
	case model.TCPFastOpenStateUnavailable:
		report.TCPFastOpenState, report.TCPFastOpenValue = model.TCPFastOpenStateUnavailable, 0
	default:
		if report.TCPFastOpenValue < 0 || report.TCPFastOpenValue > maxTCPFastOpenMask {
			report.TCPFastOpenValue = 0
		}
		report.TCPFastOpenState = model.TCPFastOpenStateFromMask(report.TCPFastOpenValue)
	}
}

func normalizeKernelCapabilities(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 || seen[value] || len(result) >= 64 {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// applyHealthReportToServer mirrors the persisted health report state onto the
// connection's in-memory server copy so heartbeat planning and future reports
// observe the applied report without a per-report GetServer reload. It never
// advances UpdatedAt (runtime state only).
func applyHealthReportToServer(server *model.Server, result store.HealthApplyResult) {
	if server == nil {
		return
	}
	current := result.Curr
	server.Status = current.Status
	server.PublicIPv4 = current.PublicIPv4
	server.PublicIPv6 = current.PublicIPv6
	server.InterfaceIPv6 = current.InterfaceIPv6
	server.DetectedRegionCode = current.DetectedRegionCode
	server.OS = current.OS
	server.DistroID = current.DistroID
	server.DistroVersion = current.DistroVersion
	server.DistroName = current.DistroName
	server.Libc = current.Libc
	server.ServiceManager = current.ServiceManager
	server.PackageManager = current.PackageManager
	server.Arch = current.Arch
	server.Kernel = current.Kernel
	server.CPU = current.CPU
	server.CPUCores = current.CPUCores
	server.MemoryBytes = current.MemoryBytes
	server.CPUUsagePercent = result.Curr.CPUUsagePercent
	server.MemoryUsedBytes = result.Curr.MemoryUsedBytes
	server.MemoryTotalBytes = result.Curr.MemoryTotalBytes
	server.AgentMemoryBytes = result.Curr.AgentMemoryBytes
	server.DiskBytes = result.Curr.DiskBytes
	server.DiskTotalBytes = result.Curr.DiskTotalBytes
	server.TCPConnectionCount = result.Curr.TCPConnectionCount
	server.UDPConnectionCount = result.Curr.UDPConnectionCount
	server.ProcessCount = result.Curr.ProcessCount
	server.NetworkUploadBPS = result.Curr.NetworkUploadBPS
	server.NetworkDownloadBPS = result.Curr.NetworkDownloadBPS
	server.TrafficUploadBytes = result.Curr.TrafficUploadBytes
	server.TrafficDownloadBytes = result.Curr.TrafficDownloadBytes
	server.AgentVersion = current.AgentVersion
	server.AgentBuild = current.AgentBuild
	server.SingBoxVersion = current.SingBoxVersion
	server.KernelCapabilities = append([]string(nil), current.KernelCapabilities...)
	server.TCPFastOpenState = current.TCPFastOpenState
	server.TCPFastOpenValue = current.TCPFastOpenValue
	server.ConnectivityStatus = current.ConnectivityStatus
	server.ConnectivityLatencyMS = current.ConnectivityLatencyMS
	server.ConnectivityCheckedAt = current.ConnectivityCheckedAt
	server.ConnectivityError = current.ConnectivityError
	server.TelemetryUpdatedAt = result.Curr.TelemetryUpdatedAt
	server.LastSeenAt = result.Curr.LastSeenAt
}

func (s *Server) completeAgentUpdateAfterReconnect(ctx context.Context, serverID int64, agentBuild string) {
	agentBuild = strings.TrimSpace(agentBuild)
	if agentBuild == "" {
		return
	}
	task, err := s.store.ActiveTaskByServerType(ctx, serverID, model.AgentTaskTypeUpdateAgent)
	if err != nil {
		return
	}
	var payload model.UpdateAgentTaskPayload
	if json.Unmarshal([]byte(task.PayloadJSON), &payload) != nil || strings.TrimSpace(payload.ExpectedBuild) == "" || agentBuild != strings.TrimSpace(payload.ExpectedBuild) {
		return
	}
	result, _ := json.Marshal(map[string]any{
		"message":         "Agent 已更新并重新连接",
		"expected_build":  payload.ExpectedBuild,
		"installed_build": agentBuild,
		"restart":         "completed",
	})
	if err := s.store.CompleteTask(ctx, task.ID, "succeeded", string(result)); err != nil {
		log.Printf("complete agent update task %d after reconnect: %v", task.ID, err)
		return
	}
	log.Printf("agent update confirmed server=%d build=%s", task.ServerID, agentBuild)
	s.publishRealtime(realtimeResourcesForTask(task.Type)...)
	s.noteAgentUpdateOutcome(ctx, serverID, "succeeded", "", payload.ExpectedBuild)
}

func (s *Server) agentTaskResults(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowAgentRate(w, "agent-task-result:"+server.AgentID, 120, time.Minute) {
		return
	}
	var req model.AgentTaskResultReport
	if !decode(w, r, &req) {
		return
	}
	if req.HealthReport != nil {
		raw, _ := json.Marshal(req.HealthReport)
		s.processAgentSocketMessage(r.Context(), server, map[string]json.RawMessage{"health_report": raw}, clientIP(r))
	}
	if req.ResultJSON == "" {
		req.ResultJSON = "{}"
	}
	if !allowedTaskStatus(req.Status) {
		fail(w, errors.New("invalid task status"), 400)
		return
	}
	if err := validJSONObject(req.ResultJSON); err != nil {
		fail(w, err, 400)
		return
	}
	task, err := s.store.GetTask(r.Context(), req.TaskID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if task.ServerID != server.ID {
		fail(w, errors.New("task does not belong to this agent"), 403)
		return
	}
	if task.Type == model.AgentTaskTypeRemoteExec || task.Type == model.AgentTaskTypeRemoteOperation {
		req.ResultJSON = s.captureRemoteExecResult(*task, req.Status, req.ResultJSON)
	}
	if task.Type == model.AgentTaskTypeListNetworkInterfaces && req.Status == "succeeded" {
		if err := validateNetworkInterfacesTaskResult(req.ResultJSON); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
	}
	if task.Type == model.AgentTaskTypeIssueCertificateHTTP {
		var payload model.IssueCertificateHTTPTaskPayload
		if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
			fail(w, err, 400)
			return
		}
		certificate, err := s.store.GetCertificate(r.Context(), payload.CertificateID)
		if err != nil {
			fail(w, err, 404)
			return
		}
		if req.Status == "succeeded" && certificate.Status != model.CertificateStatusReady {
			fail(w, errors.New("HTTP-01 task did not report certificate material"), 400)
			return
		}
		if req.Status != "succeeded" {
			certificate.Status = model.CertificateStatusFailed
			certificate.LastError = "Agent HTTP-01 issuance failed"
			var result map[string]any
			if json.Unmarshal([]byte(req.ResultJSON), &result) == nil {
				if message, ok := result["error"].(string); ok && strings.TrimSpace(message) != "" {
					certificate.LastError = message
				}
			}
			if err := s.store.UpdateCertificate(r.Context(), certificate); err != nil {
				fail(w, err, 500)
				return
			}
		}
	}
	if task.Type == model.AgentTaskTypeApplyDeployment {
		if err := s.applyDeploymentWARPReports(r.Context(), server.ID, req.ResultJSON); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		if req.Status == "succeeded" {
			if err := s.applyDeploymentSSHState(r.Context(), server.ID, *task, req.ResultJSON); err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
		}
	}
	if task.Type == model.AgentTaskTypeProbeExternalEgress || task.Type == model.AgentTaskTypeApplyDeployment {
		if err := s.applyExternalEgressTaskResults(r.Context(), server.ID, *task, req.Status, req.ResultJSON); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
	}
	if task.Type == model.AgentTaskTypeProbeLatencyTargets {
		if err := s.applyLatencyProbeTaskResult(r.Context(), server.ID, *task, req.Status, req.ResultJSON); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
	}
	if err := s.applyTimeCheckTaskResult(r.Context(), *task, req.Status, req.ResultJSON); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	// An Agent update is not finished when the binaries are on disk. The Agent
	// still has to restart onto its own new executable, and that restart is
	// armed after this report. Holding the task open until the Agent reconnects
	// on the expected build is what makes "更新成功" mean the new process is up.
	if awaiting, err := s.holdAgentUpdateForRestart(r.Context(), *task, req.Status, req.ResultJSON); err != nil {
		fail(w, err, 500)
		return
	} else if !awaiting {
		if err := s.completeTaskWithNotification(r.Context(), req.TaskID, req.Status, req.ResultJSON); err != nil {
			fail(w, err, 500)
			return
		}
		if task.Type == model.AgentTaskTypeUpdateAgent {
			s.noteAgentUpdateOutcome(r.Context(), task.ServerID, req.Status, taskResultMessage(model.AgentTask{ResultJSON: req.ResultJSON}), agentUpdatePayloadBuild(*task))
		}
	}
	s.recordConfigurationTaskResult(r.Context(), *task, req.Status, req.ResultJSON)
	// A completed task may advance an access-change phase or open an
	// authorization window; wake the access workers instead of polling.
	s.wakeAccessWorkers()
	log.Printf("task result server=%d(%s) id=%d type=%s status=%s duration_ms=%d", task.ServerID, safeLogField(server.Name), task.ID, task.Type, req.Status, time.Since(task.CreatedAt.UTC()).Milliseconds())
	defer s.publishRealtime(realtimeResourcesForTask(task.Type)...)
	if task.Type == model.AgentTaskTypeApplyCoreConfig {
		if err := s.store.CompleteDNSBenchmarkApplyTask(r.Context(), task.ID, req.Status == "succeeded", req.ResultJSON); err != nil {
			fail(w, err, 500)
			return
		}
	}
	if task.Type == model.AgentTaskTypeBenchmarkDNS && req.Status != "succeeded" {
		if err := s.store.FailDNSBenchmarkRunForTask(r.Context(), task.ID, req.ResultJSON); err != nil {
			fail(w, err, 500)
			return
		}
	}
	if task.Type == model.AgentTaskTypeUpdateAgentConfig && task.ConfigVersion == s.basePathState().MigrationVersion {
		s.maybeFinalizeBasePathMigration(r.Context())
	}
	if task.Type == model.AgentTaskTypeApplyDeployment && req.Status == "succeeded" {
		var payload model.DeploymentTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) == nil && payload.ExternalInboundProbe != nil {
			go s.runControllerInboundProbeTelemetry(context.WithoutCancel(r.Context()), *payload.ExternalInboundProbe)
		}
	}
	if task.Type == model.AgentTaskTypeUninstallAgent && req.Status == "succeeded" {
		var uninstallPayload model.UninstallAgentTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &uninstallPayload) != nil {
			uninstallPayload.ActorID = 0
		}
		var actorID *int64
		if uninstallPayload.ActorID > 0 {
			actorID = &uninstallPayload.ActorID
		}
		if deleteStatus, deleteErr := s.deleteServerRecord(context.WithoutCancel(r.Context()), task.ServerID, actorID, "controller"); deleteErr != nil {
			log.Printf("delete server %d after agent uninstall: %v", task.ServerID, deleteErr)
			fail(w, deleteErr, deleteStatus)
			return
		}
	}
	write(w, 200, map[string]any{"ok": true})
}

func (s *Server) applyDeploymentWARPReports(ctx context.Context, serverID int64, resultJSON string) error {
	var result struct {
		Steps []struct {
			Key    string          `json:"key"`
			Result json.RawMessage `json:"result"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return err
	}
	for _, step := range result.Steps {
		if !strings.HasPrefix(step.Key, "warp_") || len(step.Result) == 0 || string(step.Result) == "null" {
			continue
		}
		var report model.WARPConfigReport
		if err := json.Unmarshal(step.Result, &report); err != nil {
			return fmt.Errorf("decode WARP deployment result: %w", err)
		}
		if report.ProfileID == 0 {
			return errors.New("WARP deployment result is missing profile_id")
		}
		profile, err := s.store.GetWARPProfile(ctx, report.ProfileID)
		if err != nil || profile.ServerID != serverID {
			return errors.New("warp profile does not belong to this agent")
		}
		report.ServerID = serverID
		if report.Status == "" {
			report.Status = model.WARPStatusFailed
		}
		if err := s.store.ApplyWARPReport(ctx, report); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) applyDeploymentSSHState(ctx context.Context, serverID int64, task model.AgentTask, resultJSON string) error {
	var result struct {
		Steps []struct {
			Key    string          `json:"key"`
			Status string          `json:"status"`
			Result json.RawMessage `json:"result"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return err
	}
	var report struct {
		HostPublicKey      string `json:"host_public_key"`
		HostKeyFingerprint string `json:"host_key_fingerprint"`
	}
	found := false
	for _, step := range result.Steps {
		if step.Key != "ssh_inbounds" || (step.Status != "succeeded" && step.Status != "skipped") {
			continue
		}
		found = true
		if err := json.Unmarshal(step.Result, &report); err != nil {
			return fmt.Errorf("decode SSH deployment result: %w", err)
		}
		break
	}
	if !found || strings.TrimSpace(report.HostPublicKey) == "" {
		return s.store.ClearSSHDeploymentState(ctx, serverID)
	}
	hostPublicKey, rest, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(report.HostPublicKey)))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return errors.New("SSH deployment result contains an invalid host public key")
	}
	canonicalHostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostPublicKey)))
	hostFingerprint := ssh.FingerprintSHA256(hostPublicKey)
	if report.HostKeyFingerprint != hostFingerprint {
		return errors.New("SSH deployment host key fingerprint does not match")
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
		return fmt.Errorf("decode SSH deployment payload: %w", err)
	}
	version := payload.Version
	if version == 0 {
		version = task.ConfigVersion
	}
	passwordDeployments, err := s.sshPasswordDeploymentsFromPlan(serverID, payload.SSHInbounds)
	if err != nil {
		return err
	}
	return s.store.ApplySSHDeploymentState(ctx, model.SSHServerHostKey{ServerID: serverID, PublicKey: canonicalHostKey, Fingerprint: hostFingerprint, PlanDigest: sshInboundPlanDigest(payload.SSHInbounds), ConfigVersion: version}, passwordDeployments)
}

func allowedTaskStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "rollback_failed":
		return true
	default:
		return false
	}
}
func (s *Server) agentTrafficReports(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowAgentRate(w, "agent-traffic:"+server.AgentID, 120, time.Minute) {
		return
	}
	var req agentTrafficReportEnvelope
	if !decode(w, r, &req) {
		return
	}
	if len(req.Items) > 0 {
		fail(w, errors.New("traffic reports must use checkpoint ranges"), 400)
		return
	}
	s.handleAgentTrafficLedger(w, r, server, req)
}

func parseReportTime(raw string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err == nil {
		return t
	}
	return time.Now().UTC()
}

func (s *Server) dnsBenchmarks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := s.store.ListDNSBenchmarkResults(r.Context(), int64Query(r, "server_id", 0), intQuery(r, "limit", 100))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"dns_benchmarks": items})
}

func (s *Server) mtuDetections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := s.store.ListMTUDetectionResults(r.Context(), int64Query(r, "server_id", 0), intQuery(r, "limit", 100))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"mtu_detections": items})
}

func (s *Server) agentDNSBenchmarks(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowAgentRate(w, "agent-dns-benchmark:"+server.AgentID, 30, time.Minute) {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req model.DNSBenchmarkResult
	if !decode(w, r, &req) {
		return
	}
	req.ServerID = server.ID
	if req.ReportID == "" || req.PolicyRevision == 0 || req.BootstrapListID == 0 || req.BootstrapListRevision == 0 {
		fail(w, errors.New("dns benchmark report and revision snapshot are required"), 400)
		return
	}
	// A plain-DNS-only policy reports no encrypted list, so both encrypted
	// fields are zero together; one zero alone is a malformed snapshot.
	if (req.EncryptedListID == 0) != (req.EncryptedListRevision == 0) {
		fail(w, errors.New("dns benchmark encrypted list snapshot is inconsistent"), 400)
		return
	}
	if req.EncryptedListID == 0 && len(req.Encrypted.Items) > 0 {
		fail(w, errors.New("dns benchmark reported encrypted results without an encrypted list"), 400)
		return
	}
	if len(req.Encrypted.Items) > 32 || len(req.Bootstrap.Items) > 32 || len(req.Encrypted.BestTags) > 2 || len(req.Bootstrap.BestTags) > 2 {
		fail(w, errors.New("dns benchmark result exceeds group limits"), 400)
		return
	}
	outcome, err := s.store.RecordDNSBenchmarkResult(r.Context(), &req)
	if err != nil {
		fail(w, err, 500)
		return
	}
	if outcome.Duplicate {
		write(w, 200, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if outcome.ApplyRequested && (outcome.Success || outcome.PlainFallback) {
		if err := s.queueDNSBenchmarkCoreApply(r.Context(), *server, req.RequestID); err != nil {
			_ = s.store.UpdateDNSBenchmarkRunApply(r.Context(), req.RequestID, nil, "apply_failed", err.Error())
			fail(w, err, 500)
			return
		}
	}
	write(w, 200, map[string]any{"ok": true, "status": req.Status, "stale": outcome.Stale})
}

func (s *Server) queueDNSBenchmarkCoreApply(ctx context.Context, server model.Server, requestID string) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	// Resolving derived forwards seeds the ledger with the ports the generated
	// listeners already own, so the config below reuses them instead of picking
	// new ones.
	_, err = core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger)
	if err != nil {
		return err
	}
	if err := requireReadyWARPForFocusedApply(data, server.ID); err != nil {
		return err
	}
	generated, err := s.generateServerCoreConfigWithLedger(ctx, server, data, ledger)
	if err != nil {
		return err
	}
	unchanged, err := s.serverConfigUnchanged(ctx, server.ID, generated.Config)
	if err != nil {
		return err
	}
	if unchanged {
		return s.store.UpdateDNSBenchmarkRunApply(ctx, requestID, nil, "applied", "")
	}
	version, err := s.store.NextConfigVersion(ctx)
	if err != nil {
		return err
	}
	payload := model.ApplyCoreConfigTaskPayload{Config: generated.Config, Reason: "dns_benchmark", Assets: generated.Assets}
	task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeApplyCoreConfig, payload, version)
	if err != nil {
		return err
	}
	if task.Status == "failed" {
		return s.store.UpdateDNSBenchmarkRunApply(ctx, requestID, &task.ID, "apply_failed", task.ResultJSON)
	}
	return s.store.UpdateDNSBenchmarkRunApply(ctx, requestID, &task.ID, "apply_queued", "")
}

func (s *Server) agentMTUDetections(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowAgentRate(w, "agent-mtu:"+server.AgentID, 30, time.Minute) {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req model.MTUDetectionResult
	if !decode(w, r, &req) {
		return
	}
	req.ServerID = server.ID
	if req.Mode == "" {
		req.Mode = model.MTUModeDetect
	}
	switch req.Mode {
	case model.MTUModeDetect, model.MTUModeApply:
	default:
		fail(w, errors.New("invalid mtu mode"), 400)
		return
	}
	if req.CurrentMTU < 0 || req.PathMTU < 0 || req.RecommendedMTU < 0 || req.AppliedMTU < 0 {
		fail(w, errors.New("mtu values must be >= 0"), 400)
		return
	}
	if req.ResultJSON == "" {
		b, _ := json.Marshal(map[string]any{"methods": req.Methods})
		req.ResultJSON = string(b)
	}
	if err := validJSONObject(req.ResultJSON); err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.store.AddMTUDetectionResult(r.Context(), req); err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"ok": true})
}

func (s *Server) agentPortForwardProbes(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowAgentRate(w, "agent-pf-probe:"+server.AgentID, 60, time.Minute) {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req model.PortForwardProbeResult
	if !decode(w, r, &req) {
		return
	}
	req.ServerID = server.ID
	if req.PortForwardID == 0 {
		fail(w, errors.New("port_forward_id required"), 400)
		return
	}
	forward, err := s.portForwardForAgentReport(r.Context(), req.PortForwardID)
	if err != nil || forward.SourceServerID != server.ID {
		fail(w, errors.New("port forward does not belong to this agent"), 403)
		return
	}
	if req.Mode == "" {
		req.Mode = "task"
	}
	if req.LatencyMS < 0 || req.SampleCount < 0 {
		fail(w, errors.New("latency_ms and sample_count must be >= 0"), 400)
		return
	}
	if req.ResultJSON == "" {
		req.ResultJSON = "{}"
	}
	if err := validJSONObject(req.ResultJSON); err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.store.AddPortForwardProbeResult(r.Context(), req); err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"ok": true})
}

func (s *Server) portForwardForAgentReport(ctx context.Context, id int64) (*model.PortForward, error) {
	forward, err := s.store.GetPortForward(ctx, id)
	if err == nil {
		return forward, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return nil, err
	}
	steps, err := s.store.ListProxyPathSteps(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	derived, err := core.DerivedPortForwardsFromProxyPaths(paths, steps, servers, inbounds)
	if err != nil {
		return nil, err
	}
	for index := range derived {
		if derived[index].ID == id {
			return &derived[index], nil
		}
	}
	return nil, sql.ErrNoRows
}

// authAgent resolves the Agent identity for a callback.
//
// The failure budget is checked before the credential lookup. A node that was
// deleted or re-enrolled keeps its old token and its own report timers, so it
// retries on a fixed schedule forever; without this gate every one of those
// retries still cost a SQLite read, and no per-Agent budget applied because
// the budget is keyed by an identity the request never established.
func (s *Server) authAgent(w http.ResponseWriter, r *http.Request) (*model.Server, bool) {
	agentID := r.Header.Get("X-Agent-ID")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ip := clientIP(r)
	if s.agentAuthBlocked(ip) {
		fail(w, errors.New("too many failed agent authentications"), http.StatusTooManyRequests)
		return nil, false
	}
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(token) == "" {
		s.noteAgentAuthFailure(ip)
		fail(w, errors.New("invalid agent credentials"), 401)
		return nil, false
	}
	server, err := s.store.GetServerByAgent(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.noteAgentAuthFailure(ip)
			fail(w, errors.New("invalid agent credentials"), 401)
		} else {
			fail(w, err, http.StatusInternalServerError)
		}
		return nil, false
	}
	// Constant-time compare of hex-encoded SHA-256 hashes.
	if !hmac.Equal([]byte(server.AgentTokenHash), []byte(security.HashSecret(token))) {
		s.noteAgentAuthFailure(ip)
		fail(w, errors.New("invalid agent credentials"), 401)
		return nil, false
	}
	s.noteAgentAuthSuccess(ip)
	return server, true
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		method(w)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	staticDir := s.staticDir
	if staticDir == "" {
		staticDir = "web/dist"
	}
	root, err := filepath.Abs(staticDir)
	if err != nil {
		fail(w, err, 500)
		return
	}
	rel := strings.TrimPrefix(urlpath.Clean("/"+r.URL.Path), "/")
	candidate := filepath.Join(root, filepath.FromSlash(rel))
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil || !pathContained(root, candidateAbs) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(candidateAbs); err == nil && !info.IsDir() {
		if strings.HasPrefix(rel, "assets/") || strings.HasPrefix(rel, "region-flags/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFile(w, r, candidateAbs)
		return
	}
	if !isKnownUIPagePath(r.URL.Path) {
		writeNotFoundJSON(w)
		return
	}
	index := filepath.Join(root, "index.html")
	if !pathContained(root, index) {
		http.NotFound(w, r)
		return
	}
	s.serveIndex(w, r, index)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, index string) {
	// #nosec G304 -- the caller constructs index under staticDir and verifies path containment.
	content, err := os.ReadFile(index)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	baseHref := requestBasePath(r, s.currentBasePath()) + "/"
	content = []byte(strings.Replace(string(content), `<base href="/"`, `<base href="`+baseHref+`"`, 1))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(content)
	}
}

func (s *Server) agentInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		method(w)
		return
	}
	baseURL, err := s.publicBaseURL(r.Context())
	if err != nil {
		fail(w, err, http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	script := `#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null && set -o pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "请使用 root 执行，或通过 sudo 运行命令。" >&2
  exit 1
fi

ACTION=${OBOARD_ACTION:-${1:-install}}
INSTALL_DIR_INPUT=${INSTALL_DIR:-${OBOARD_INSTALL_DIR:-}}
INSTALL_ENV_PATH=${OBOARD_AGENT_INSTALL_ENV:-/etc/oboard-agent/install.env}
INSTALL_DIR=
CONFIG_PATH=${OBOARD_AGENT_CONFIG:-/etc/oboard-agent/config.json}
STATE_DIR=${OBOARD_AGENT_STATE:-/var/lib/oboard-agent}
INSTALL_LOG=
UPDATE_TMP=
AGENT_RESTART=${OBOARD_AGENT_RESTART:-delayed}
DEFAULT_BASE_URL=__BASE_URL__
BASE_URL=${OBOARD_CONTROLLER_URL:-$DEFAULT_BASE_URL}
BASE_URL=${BASE_URL%%/}
ALLOW_PANEL_UPDATE=${OBOARD_ALLOW_PANEL_UPDATE:-1}
UPDATE_SOURCE=${OBOARD_UPDATE_SOURCE:-panel}
UPDATE_REPO=${OBOARD_UPDATE_REPO:-OboardProject/oboard-agent}
OBOARD_PURGE=${OBOARD_PURGE:-1}
INSTALL_BBR=${OBOARD_INSTALL_BBR:-0}
BBR_AVAILABLE_PATH=${OBOARD_BBR_AVAILABLE_PATH:-/proc/sys/net/ipv4/tcp_available_congestion_control}
BBR_CONGESTION_PATH=${OBOARD_BBR_CONGESTION_PATH:-/proc/sys/net/ipv4/tcp_congestion_control}
BBR_QDISC_PATH=${OBOARD_BBR_QDISC_PATH:-/proc/sys/net/core/default_qdisc}
BBR_CONFIG_PATH=${OBOARD_BBR_CONFIG_PATH:-/etc/sysctl.d/99-oboard-bbr.conf}
RELEASE_PUBLIC_KEY=__RELEASE_PUBLIC_KEY__
ACME_SH_VERSION=3.1.4
ACME_SH_SHA256=fcabf274d4f96966ec933879ae0257266e8ef2f7d16161f14b84dd896c0cac32
ACME_SH_URL="https://raw.githubusercontent.com/acmesh-official/acme.sh/$ACME_SH_VERSION/acme.sh"
ACME_SH_INSTALL_PATH=/usr/local/bin/acme.sh

normalize_install_dir() {
  value=${1:-/opt/oboard}
  while [ "$value" != / ] && [ "${value%/}" != "$value" ]; do
    value=${value%/}
  done
  case "$value" in
    /*) ;;
    *) return 1 ;;
  esac
  case "$value" in
    /|*//*|*[!A-Za-z0-9_./-]*) return 1 ;;
  esac
  case "$value/" in
    */./*|*/../*) return 1 ;;
  esac
  printf '%s\n' "$value"
}

install_dir_from_input() {
  normalize_install_dir "${1:-/opt/oboard}"
}

configured_agent_install_dir() {
  [ -f "$INSTALL_ENV_PATH" ] || return 0
  sed -n 's/^OBOARD_INSTALL_DIR=//p' "$INSTALL_ENV_PATH" 2>/dev/null | tail -n1 | tr -d "'\""
}

choose_install_dir() {
  if [ ! -r /dev/tty ] || [ ! -w /dev/tty ]; then
    install_dir_from_input
    return
  fi
  while :; do
    printf '请输入安装目录（留空为/opt/oboard）：' > /dev/tty
    IFS= read -r choice < /dev/tty || choice=
    if selected=$(install_dir_from_input "$choice"); then
      printf '%s\n' "$selected"
      return 0
    fi
    printf '请输入规范的绝对路径，例如 /data/oboard。\n' > /dev/tty
  done
}

resolve_agent_install_dir() {
  persisted=$(configured_agent_install_dir)
  if [ -n "$persisted" ]; then
    raw_persisted=$persisted
    if ! normalized=$(normalize_install_dir "$persisted"); then
      echo "已保存的 Agent 安装目录无效：$raw_persisted" >&2
      exit 1
    fi
    persisted=$normalized
  fi
  existing_dir=$persisted
  if [ -z "$existing_dir" ]; then
    for candidate in /usr/local/bin /opt/oboard /usr/local/sbin; do
      if [ -x "$candidate/oboard-agent" ]; then
        existing_dir=$candidate
        break
      fi
    done
  fi
  if [ -n "$INSTALL_DIR_INPUT" ]; then
    if ! normalized=$(normalize_install_dir "$INSTALL_DIR_INPUT"); then
      echo "INSTALL_DIR/OBOARD_INSTALL_DIR 必须是规范的绝对路径。" >&2
      exit 1
    fi
    INSTALL_DIR_INPUT=$normalized
    if [ -n "$existing_dir" ] && [ "$INSTALL_DIR_INPUT" != "$existing_dir" ]; then
      echo "已安装 Agent 使用 $existing_dir；更新或卸载时不能改为 $INSTALL_DIR_INPUT。" >&2
      exit 1
    fi
    INSTALL_DIR=$INSTALL_DIR_INPUT
  elif [ -n "$existing_dir" ]; then
    INSTALL_DIR=$existing_dir
  elif [ "$ACTION" = install ]; then
    INSTALL_DIR=$(choose_install_dir) || {
      echo "安装目录无效。" >&2
      exit 1
    }
  else
    INSTALL_DIR=/usr/local/bin
  fi
  export INSTALL_DIR
}

persist_agent_install_dir() {
  install -d -m 0700 "$(dirname "$INSTALL_ENV_PATH")"
  printf 'OBOARD_INSTALL_DIR=%s\n' "$INSTALL_DIR" > "$INSTALL_ENV_PATH.new"
  chmod 0600 "$INSTALL_ENV_PATH.new"
  mv -f "$INSTALL_ENV_PATH.new" "$INSTALL_ENV_PATH"
}

finish_install() {
  status=$?
  [ -z "$UPDATE_TMP" ] || rm -rf "$UPDATE_TMP"
  if [ "$status" -ne 0 ] && [ -n "$INSTALL_LOG" ] && [ -f "$INSTALL_LOG" ]; then
    echo "" >&2
    echo "OBoard Agent 操作未完成。" >&2
    echo "请根据上方提示处理后重试；详细日志：$INSTALL_LOG" >&2
  fi
  trap - EXIT
  exit "$status"
}

prepare_install_log() {
  local log_dir log_tmp
  INSTALL_LOG=${OBOARD_AGENT_INSTALL_LOG:-$STATE_DIR/install.log}
  case "$INSTALL_LOG" in
    */*) log_dir=${INSTALL_LOG%/*}; [ -n "$log_dir" ] || log_dir=/ ;;
    *) log_dir=. ;;
  esac
  mkdir -p "$STATE_DIR" "$log_dir"
  chmod 0700 "$STATE_DIR"
  log_tmp=$(mktemp "$log_dir/.oboard-agent-install-log.XXXXXX")
  chmod 0600 "$log_tmp"
  mv -f "$log_tmp" "$INSTALL_LOG"
}

# The Agent serializes its own kernel work with an in-process mutex, which this
# script cannot see. Both sides take the same advisory flock so an operator
# update over SSH and a panel-driven deployment cannot replace the same binaries
# and restart the same service at once.
CORE_LOCK_HELD=0
acquire_core_lifecycle_lock() {
  core_lock_path="$STATE_DIR/core-lifecycle.lock"
  mkdir -p "$STATE_DIR" 2>/dev/null || true
  if ! command -v flock >/dev/null 2>&1; then
    echo "未找到 flock，无法与面板下发的内核操作互斥；请确认此时没有正在进行的配置下发。" >&2
    return 0
  fi
  # A redirection failure on the ":" special builtin would exit a non-interactive
  # shell outright, so the writability probe runs in a subshell.
  if ! ( : >> "$core_lock_path" ) 2>/dev/null; then
    echo "无法创建更新互斥锁 $core_lock_path，继续执行。" >&2
    return 0
  fi
  chmod 0600 "$core_lock_path" 2>/dev/null || true
  exec 9>> "$core_lock_path"
  # BusyBox flock accepts only [-sxun]: -w is a usage error there, and its
  # non-zero exit is indistinguishable from a busy lock, so an Alpine host would
  # report a phantom concurrent deployment. A bounded -n retry loop is the one
  # wait util-linux and BusyBox both implement.
  core_lock_waited=0
  until flock -n 9; do
    if [ "$core_lock_waited" -ge 120 ]; then
      echo "另一个 OBoard 更新或面板配置下发正在进行，请稍后重试。" >&2
      exit 1
    fi
    sleep 1
    core_lock_waited=$((core_lock_waited + 1))
  done
  CORE_LOCK_HELD=1
  printf 'installer %s\n' "$$" >&9 2>/dev/null || true
}

release_core_lifecycle_lock() {
  [ "$CORE_LOCK_HELD" = 1 ] || return 0
  CORE_LOCK_HELD=0
  exec 9>&-
}

service_active() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl is-active --quiet "$1"
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    rc-service "$1" status >/dev/null 2>&1
  else
    return 1
  fi
}

restart_managed_service() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl restart "$1" >> "$INSTALL_LOG" 2>&1
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    rc-service "$1" restart >> "$INSTALL_LOG" 2>&1
  else
    return 1
  fi
}

# A service that comes up and immediately exits is a failed update, not a
# successful one. Restart exit codes alone do not catch that.
wait_service_stable() {
  wait_service=$1
  wait_seconds=${2:-15}
  wait_stable=0
  wait_elapsed=0
  while [ "$wait_elapsed" -lt "$wait_seconds" ]; do
    if service_active "$wait_service"; then
      wait_stable=$((wait_stable + 1))
      if [ "$wait_stable" -ge 3 ]; then
        return 0
      fi
    else
      wait_stable=0
    fi
    sleep 1
    wait_elapsed=$((wait_elapsed + 1))
  done
  return 1
}

# The kernel that is actually serving traffic is the only thing that proves an
# update landed. Agent owns the operational-digest normalization, so the check
# is delegated to it rather than reimplemented here.
verify_core_runtime() {
  [ -s "$STATE_DIR/sing-box.json" ] || return 0
  [ -x "$INSTALL_DIR/oboard-agent" ] || return 0
  if ! "$INSTALL_DIR/oboard-agent" -h 2>&1 | grep -q -- '-verify-core-runtime'; then
    echo "当前 Agent 版本不支持内核运行态校验，已跳过该检查。" >&2
    return 0
  fi
  if "$INSTALL_DIR/oboard-agent" -verify-core-runtime \
    -config "$CONFIG_PATH" \
    -state-dir "$STATE_DIR" \
    -core-binary "$INSTALL_DIR/oboard-sb" \
    -core-service oboard-sb >> "$INSTALL_LOG" 2>&1; then
    return 0
  fi
  echo "内核已重启，但运行中的进程仍不是本次安装的版本或配置，更新未完成。" >&2
  echo "详细信息见 $INSTALL_LOG。" >&2
  return 1
}

resolve_agent_install_dir

if [ "$ACTION" = uninstall ]; then
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop oboard-agent oboard-sb 2>/dev/null || true
    systemctl disable oboard-agent oboard-sb 2>/dev/null || true
    rm -f /etc/systemd/system/oboard-agent.service /etc/systemd/system/oboard-sb.service
    systemctl daemon-reload || true
  elif command -v rc-service >/dev/null 2>&1; then
    rc-service oboard-agent stop 2>/dev/null || true
    rc-service oboard-sb stop 2>/dev/null || true
    rc-update del oboard-agent default 2>/dev/null || true
    rc-update del oboard-sb default 2>/dev/null || true
    rm -f /etc/init.d/oboard-agent /etc/init.d/oboard-sb
  fi
  rm -f "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/oboard-sb" "$INSTALL_DIR/oboard-realm" "$INSTALL_DIR/obag"
  obag_profile_path="${OBOARD_PROFILE_DIR:-/etc/profile.d}/oboard-agent.sh"
  if [ -f "$obag_profile_path" ] && grep -Fq "$INSTALL_DIR" "$obag_profile_path" 2>/dev/null; then
    rm -f "$obag_profile_path"
  fi
  case "$INSTALL_DIR" in
    /usr/local/bin|/usr/local/sbin) ;;
    *) rmdir "$INSTALL_DIR" 2>/dev/null || true ;;
  esac
  if [ "$OBOARD_PURGE" = 1 ]; then
    rm -rf "$(dirname "$CONFIG_PATH")" "$STATE_DIR"
  fi
  echo "OBoard Agent、oboard-sb、管理命令和本机配置已卸载。"
  exit 0
fi

need_base_url() {
  if [ -z "$BASE_URL" ]; then
    echo "缺少 OBOARD_CONTROLLER_URL" >&2
    exit 1
  fi
}

fix_hostname_resolution() {
  host_name=$(hostname 2>/dev/null || true)
  [ -n "$host_name" ] || return 0
  [ -w /etc/hosts ] || return 0
  resolved=0
  if command -v getent >/dev/null 2>&1; then
    getent hosts "$host_name" >/dev/null 2>&1 && resolved=1
  elif command -v python3 >/dev/null 2>&1; then
    python3 - "$host_name" <<'PY' >/dev/null 2>&1 && resolved=1
import socket, sys
socket.getaddrinfo(sys.argv[1], None)
PY
  else
    # Minimal systems without getent: accept if already listed in /etc/hosts.
    grep -E "(^|[[:space:]])${host_name}([[:space:]]|$)" /etc/hosts >/dev/null 2>&1 && resolved=1
  fi
  if [ "$resolved" = 1 ]; then
    return 0
  fi
  printf '\n127.0.1.1 %s\n' "$host_name" >> /etc/hosts
  echo "已修复本机 hostname 解析：$host_name" >> "${INSTALL_LOG:-/dev/null}"
}

bbr_requested() {
  case "$INSTALL_BBR" in
    1|true|TRUE|yes|YES|on|ON) return 0 ;;
    0|false|FALSE|no|NO|off|OFF|'') return 1 ;;
    *)
      echo "BBR 安装选项无效，请回到面板重新复制安装命令。" >&2
      exit 1
      ;;
  esac
}

bbr_available() {
  [ -r "$BBR_AVAILABLE_PATH" ] || return 1
  for value in $(cat "$BBR_AVAILABLE_PATH" 2>/dev/null); do
    [ "$value" = bbr ] && return 0
  done
  return 1
}

persist_bbr_fq() {
  if [ -L "$BBR_CONFIG_PATH" ] || { [ -e "$BBR_CONFIG_PATH" ] && [ ! -f "$BBR_CONFIG_PATH" ]; }; then
    echo "BBR 配置保存位置不是普通文件：$BBR_CONFIG_PATH" >&2
    return 1
  fi
  bbr_config_dir=${BBR_CONFIG_PATH%/*}
  [ "$bbr_config_dir" != "$BBR_CONFIG_PATH" ] || bbr_config_dir=.
  install -d -m 0755 "$bbr_config_dir" || return 1
  bbr_config_new="$BBR_CONFIG_PATH.new.$$"
  rm -f "$bbr_config_new"
  if ! (umask 077; printf '%s\n' \
    'net.core.default_qdisc = fq' \
    'net.ipv4.tcp_congestion_control = bbr' > "$bbr_config_new"); then
    rm -f "$bbr_config_new"
    return 1
  fi
  chmod 0600 "$bbr_config_new" || {
    rm -f "$bbr_config_new"
    return 1
  }
  if ! mv -f "$bbr_config_new" "$BBR_CONFIG_PATH"; then
    rm -f "$bbr_config_new"
    return 1
  fi
}

enable_bbr_fq() {
  bbr_requested || {
    return 0
  }
  echo "正在启用 BBR + FQ..."
  if ! bbr_available; then
    if ! command -v modprobe >/dev/null 2>&1; then
      echo "未能加载 BBR：当前系统没有可用的 BBR 模块，也缺少 modprobe。" >&2
      return 1
    fi
    if ! modprobe tcp_bbr 2>/dev/null || ! bbr_available; then
      echo "未能加载 BBR：当前内核不支持 BBR，安装程序不会自动更换内核。" >&2
      return 1
    fi
  fi
  if command -v modprobe >/dev/null 2>&1; then
    modprobe sch_fq 2>/dev/null || true
  fi
  if [ ! -r "$BBR_CONGESTION_PATH" ] || [ ! -w "$BBR_CONGESTION_PATH" ] || [ ! -r "$BBR_QDISC_PATH" ] || [ ! -w "$BBR_QDISC_PATH" ]; then
    echo "未能启用 BBR + FQ：当前环境不允许修改内核网络参数，常见于受限容器。" >&2
    return 1
  fi
  previous_bbr=$(cat "$BBR_CONGESTION_PATH" 2>/dev/null || true)
  previous_qdisc=$(cat "$BBR_QDISC_PATH" 2>/dev/null || true)
  if ! printf '%s\n' fq > "$BBR_QDISC_PATH" || ! printf '%s\n' bbr > "$BBR_CONGESTION_PATH"; then
    [ -n "$previous_qdisc" ] && printf '%s\n' "$previous_qdisc" > "$BBR_QDISC_PATH" 2>/dev/null || true
    [ -n "$previous_bbr" ] && printf '%s\n' "$previous_bbr" > "$BBR_CONGESTION_PATH" 2>/dev/null || true
    echo "未能启用 BBR + FQ：当前环境拒绝修改内核网络参数。" >&2
    return 1
  fi
  if [ "$(cat "$BBR_QDISC_PATH" 2>/dev/null)" != fq ] || [ "$(cat "$BBR_CONGESTION_PATH" 2>/dev/null)" != bbr ]; then
    [ -n "$previous_qdisc" ] && printf '%s\n' "$previous_qdisc" > "$BBR_QDISC_PATH" 2>/dev/null || true
    [ -n "$previous_bbr" ] && printf '%s\n' "$previous_bbr" > "$BBR_CONGESTION_PATH" 2>/dev/null || true
    echo "未能启用 BBR + FQ：内核没有接受新的网络参数。" >&2
    return 1
  fi
  if ! persist_bbr_fq; then
    [ -n "$previous_qdisc" ] && printf '%s\n' "$previous_qdisc" > "$BBR_QDISC_PATH" 2>/dev/null || true
    [ -n "$previous_bbr" ] && printf '%s\n' "$previous_bbr" > "$BBR_CONGESTION_PATH" 2>/dev/null || true
    echo "无法保存 BBR + FQ 设置，已恢复原来的网络设置。" >&2
    return 1
  fi
  echo "BBR + FQ 已启用，并会在重启后继续生效。"
}

try_enable_bbr_fq() {
  if enable_bbr_fq; then
    return 0
  fi
  echo "提示：BBR + FQ 未能启用，Agent 安装将继续，其他功能不受影响。你可以稍后在宿主机确认内核支持和参数权限。" >&2
  return 0
}

json_value() {
  key=$1
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1], ""))' "$key"
  else
    grep -o "\"$key\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -n1 | sed 's/.*:[[:space:]]*"//; s/"$//'
  fi
}

raw_b64_to_file() {
  value=$1
  out=$2
  python3 - "$value" "$out" <<'PY'
import base64, sys
v=sys.argv[1]
v += '=' * ((4 - len(v) % 4) % 4)
open(sys.argv[2], 'wb').write(base64.b64decode(v))
PY
}

pkg_install() {
  local log_file=${INSTALL_LOG:-/dev/null}
  # Install packages with the host package manager. Supports Debian/Ubuntu,
  # Alpine, RHEL/CentOS/Rocky/Alma (dnf/yum), openSUSE (zypper), Arch (pacman).
  if [ "$#" -eq 0 ]; then
    return 0
  fi
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache "$@" >> "$log_file" 2>&1
  elif command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y >> "$log_file" 2>&1
    apt-get install -y --no-install-recommends "$@" >> "$log_file" 2>&1
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "$@" >> "$log_file" 2>&1
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "$@" >> "$log_file" 2>&1
  elif command -v microdnf >/dev/null 2>&1; then
    microdnf install -y "$@" >> "$log_file" 2>&1
  elif command -v zypper >/dev/null 2>&1; then
    zypper --non-interactive install -y "$@" >> "$log_file" 2>&1
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm "$@" >> "$log_file" 2>&1
  else
    echo "未找到支持的包管理器（apk/apt/dnf/yum/zypper/pacman），无法自动安装：$*" >&2
    return 1
  fi
}

ensure_base_tools() {
  # curl + ca-certificates are required for HTTPS downloads on minimal
  # containers (Alpine, Debian slim, CentOS minimal, many LXC templates).
  need_curl=0
  need_ca=0
  need_install=0
  command -v curl >/dev/null 2>&1 || need_curl=1
  command -v install >/dev/null 2>&1 || need_install=1
  if [ ! -f /etc/ssl/certs/ca-certificates.crt ] && [ ! -f /etc/pki/tls/certs/ca-bundle.crt ] && [ ! -f /etc/ssl/cert.pem ]; then
    need_ca=1
  fi
  if [ "$need_curl$need_ca$need_install" = "000" ]; then
    return 0
  fi
  echo "  正在补齐系统所需组件..."
  packages=""
  if [ "$need_curl" = 1 ]; then
    packages="$packages curl"
  fi
  if [ "$need_ca" = 1 ]; then
    packages="$packages ca-certificates"
  fi
  if [ "$need_install" = 1 ]; then
    packages="$packages coreutils"
  fi
  # shellcheck disable=SC2086
  pkg_install $packages || {
    echo "基础依赖安装失败，请手动安装 curl、CA 证书和 install 后重试。" >&2
    exit 1
  }
  if command -v update-ca-certificates >/dev/null 2>&1; then
    update-ca-certificates >/dev/null 2>&1 || true
  elif command -v update-ca-trust >/dev/null 2>&1; then
    update-ca-trust extract >/dev/null 2>&1 || true
  fi
}

__AGENT_RELEASE_VERIFIER__

sha256_file() {
  local path=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$path" | awk '{print $NF}'
  else
    return 1
  fi
}

install_pinned_acme_sh() {
  local download staged actual target_dir
  download=$(mktemp "${OBOARD_TMPDIR:-/tmp}/oboard-acme.XXXXXX") || {
    echo "无法创建 acme.sh 下载临时文件。" >&2
    return 1
  }
  target_dir=${ACME_SH_INSTALL_PATH%/*}
  staged="$target_dir/.acme.sh.$$"
  if ! curl --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 60 -fsSL \
    "$ACME_SH_URL" -o "$download"; then
    rm -f "$download"
    echo "无法下载固定版本的 acme.sh。" >&2
    return 1
  fi
  actual=$(sha256_file "$download" || true)
  if [ "$actual" != "$ACME_SH_SHA256" ]; then
    rm -f "$download"
    echo "acme.sh 校验失败，已停止安装。" >&2
    return 1
  fi
  mkdir -p "$target_dir"
  rm -f "$staged"
  if ! cp "$download" "$staged" || ! chmod 0755 "$staged" || ! mv -f "$staged" "$ACME_SH_INSTALL_PATH"; then
    rm -f "$download" "$staged"
    echo "无法安装 acme.sh 到 $ACME_SH_INSTALL_PATH。" >&2
    return 1
  fi
  rm -f "$download"
}

ensure_acme_sh() {
  local packages=
  command -v openssl >/dev/null 2>&1 || packages="$packages openssl"
  command -v socat >/dev/null 2>&1 || packages="$packages socat"
  if [ -n "$packages" ]; then
    echo "  正在准备证书签发组件..."
    # shellcheck disable=SC2086
    if ! pkg_install $packages; then
      echo "HTTP-01 证书签发依赖安装失败，请手动安装 openssl 和 socat 后重试。" >&2
      exit 1
    fi
  fi
  if ! command -v openssl >/dev/null 2>&1 || ! command -v socat >/dev/null 2>&1; then
    echo "安装后仍缺少 openssl 或 socat。" >&2
    exit 1
  fi

  if command -v acme.sh >/dev/null 2>&1; then
    return 0
  fi
  echo "  正在准备证书签发工具..."
  if pkg_install acme.sh && command -v acme.sh >/dev/null 2>&1; then
    return 0
  fi

  echo "  正在安装经过校验的证书签发工具..."
  if ! install_pinned_acme_sh || [ ! -x "$ACME_SH_INSTALL_PATH" ]; then
    echo "acme.sh 安装失败。" >&2
    exit 1
  fi
}

detect_virt_hint() {
  # Best-effort virtualization/container hint for install logs and service tweaks.
  if [ -f /.dockerenv ] || [ -f /run/.containerenv ]; then
    echo container
    return
  fi
  if [ -r /run/systemd/container ]; then
    cat /run/systemd/container 2>/dev/null | tr -d '\n' || echo container
    return
  fi
  if [ -n "${container:-}" ]; then
    echo "$container"
    return
  fi
  if command -v systemd-detect-virt >/dev/null 2>&1; then
    v=$(systemd-detect-virt 2>/dev/null || true)
    if [ -n "$v" ] && [ "$v" != none ]; then
      echo "$v"
      return
    fi
  fi
  if [ -r /proc/1/environ ] && tr '\0' '\n' </proc/1/environ 2>/dev/null | grep -qiE '^(container=|lxc|libpod)'; then
    echo container
    return
  fi
  if [ -r /proc/1/cgroup ] && grep -qiE 'lxc|docker|kubepods|containerd|podman|libpod|incus' /proc/1/cgroup 2>/dev/null; then
    echo container
    return
  fi
  echo bare
}

verify_manifest_with_openssl() {
  manifest=$1
  sig=$2
  base_dir=$3
  os_value=$4
  arch_value=$5
  shift 5
  if [ -z "$RELEASE_PUBLIC_KEY" ]; then
    if [ "${TARGET_DEV:-false}" = true ] && [ "${OBOARD_ALLOW_UNSIGNED_DEV_UPDATE:-0}" = "1" ]; then
      echo "开发模式：跳过 unsigned manifest 校验。" >&2
      return 0
    fi
    echo "缺少 release 公钥，拒绝安装未验证的 Agent。" >&2
    exit 1
  fi
  ensure_release_verifier
  raw_b64_to_file "$RELEASE_PUBLIC_KEY" "$base_dir/release-public.raw"
  raw_b64_to_file "$(tr -d '[:space:]' < "$sig")" "$base_dir/release.sig"
  python3 - "$base_dir/release-public.raw" "$base_dir/release-public.der" <<'PY'
import sys
raw=open(sys.argv[1], 'rb').read()
if len(raw) != 32:
    raise SystemExit('invalid Ed25519 public key length')
open(sys.argv[2], 'wb').write(bytes.fromhex('302a300506032b6570032100') + raw)
PY
  verify_ed25519_signature "$base_dir/release-public.raw" "$base_dir/release-public.der" "$manifest" "$base_dir/release.sig"
  python3 - "$manifest" "$base_dir" "$os_value" "$arch_value" "$@" <<'PY'
import hashlib, json, pathlib, sys
manifest=json.load(open(sys.argv[1]))
base=pathlib.Path(sys.argv[2]); os_value=sys.argv[3]; arch_value=sys.argv[4]
want={item['name']: item for item in manifest.get('files', []) if item.get('os') == os_value and item.get('arch') == arch_value}
for name in sys.argv[5:]:
    item=want.get(name)
    if not item:
        raise SystemExit(f'{name} not found in release manifest')
    data=(base/name).read_bytes()
    if hashlib.sha256(data).hexdigest() != item.get('sha256') or len(data) != int(item.get('size', -1)):
        raise SystemExit(f'{name} checksum mismatch')
PY
}

verify_downloaded_release() {
  manifest=$1
  sig=$2
  base_dir=$3
  os_value=$4
  arch_value=$5
  shift 5
  if [ -x "$INSTALL_DIR/oboard-agent" ] && "$INSTALL_DIR/oboard-agent" -h 2>&1 | grep -q -- '-verify-release'; then
    "$INSTALL_DIR/oboard-agent" -verify-release -verify-manifest "$manifest" -verify-signature "$sig" -verify-base-dir "$base_dir" -verify-os "$os_value" -verify-arch "$arch_value" "$@"
  else
    verify_manifest_with_openssl "$manifest" "$sig" "$base_dir" "$os_value" "$arch_value" "$@"
  fi
}

load_target_version() {
  need_base_url
  version_json=$(curl -fsSL "$BASE_URL/api/v1/ui/version" 2>/dev/null || true)
  TARGET_VERSION=$(printf '%s' "$version_json" | json_value agent_expected_version 2>/dev/null || true)
  TARGET_BUILD=$(printf '%s' "$version_json" | json_value agent_expected_build 2>/dev/null || true)
  TARGET_KERNEL_BUILD=$(printf '%s' "$version_json" | json_value kernel_build 2>/dev/null || true)
  TARGET_DEV=$(printf '%s' "$version_json" | json_value dev 2>/dev/null || true)
  if [ -n "$TARGET_VERSION$TARGET_BUILD" ]; then
    echo "目标版本：Agent ${TARGET_VERSION:-unknown} build ${TARGET_BUILD:-unknown}，内核 build ${TARGET_KERNEL_BUILD:-$TARGET_BUILD}" >> "${INSTALL_LOG:-/dev/null}"
  fi
}

resolve_update_policy() {
  if [ -z "$ALLOW_PANEL_UPDATE" ]; then
    case "${TARGET_DEV:-false}" in
      true|True|1|yes|YES) ALLOW_PANEL_UPDATE=1 ;;
      *) ALLOW_PANEL_UPDATE=0 ;;
    esac
  fi
  case "$ALLOW_PANEL_UPDATE" in
    1|true|TRUE|yes|YES|on|ON) ALLOW_PANEL_UPDATE_BOOL=true ;;
    *) ALLOW_PANEL_UPDATE_BOOL=false ;;
  esac
  if [ -z "$UPDATE_SOURCE" ]; then
    if [ "$ALLOW_PANEL_UPDATE_BOOL" = true ]; then
      UPDATE_SOURCE=panel
    else
      UPDATE_SOURCE=github
    fi
  fi
}

print_installed_versions() {
  echo "当前二进制版本：" >> "${INSTALL_LOG:-/dev/null}"
  if [ -x "$INSTALL_DIR/oboard-agent" ]; then
    echo "- Agent: $($INSTALL_DIR/oboard-agent -version 2>/dev/null || true)" >> "${INSTALL_LOG:-/dev/null}"
  else
    echo "- Agent: 未安装" >> "${INSTALL_LOG:-/dev/null}"
  fi
  if [ -x "$INSTALL_DIR/oboard-sb" ]; then
    echo "- 内核: $($INSTALL_DIR/oboard-sb -version 2>/dev/null | head -n1 || true)" >> "${INSTALL_LOG:-/dev/null}"
  else
    echo "- 内核: 未安装" >> "${INSTALL_LOG:-/dev/null}"
  fi
  if [ -x "$INSTALL_DIR/oboard-realm" ]; then
    echo "- 端口转发: $($INSTALL_DIR/oboard-realm -v 2>/dev/null | head -n1 || true)" >> "${INSTALL_LOG:-/dev/null}"
  else
    echo "- 端口转发: 未安装" >> "${INSTALL_LOG:-/dev/null}"
  fi
}

verify_installed_versions() {
  print_installed_versions
  if [ -n "${TARGET_BUILD:-}" ] && [ -x "$INSTALL_DIR/oboard-agent" ]; then
    if ! "$INSTALL_DIR/oboard-agent" -version 2>/dev/null | grep -q "build $TARGET_BUILD"; then
      echo "安装的 Agent 二进制 build 与目标 build 不一致，操作未完成。请检查下载缓存或重新执行命令。" >&2
      return 1
    fi
  fi
  if [ -n "${TARGET_KERNEL_BUILD:-${TARGET_BUILD:-}}" ] && [ -x "$INSTALL_DIR/oboard-sb" ]; then
    expected_core_build=${TARGET_KERNEL_BUILD:-$TARGET_BUILD}
    if ! "$INSTALL_DIR/oboard-sb" -version 2>/dev/null | grep -q "\"build\": \"$expected_core_build\""; then
      echo "安装的优化内核 build 与目标 build 不一致，操作未完成。请检查下载缓存或重新执行命令。" >&2
      return 1
    fi
  fi
}

detect_os() {
  case "$(uname -s | tr '[:upper:]' '[:lower:]')" in
    linux) echo linux ;;
    *) echo unsupported ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) echo unsupported ;;
  esac
}

detect_service_manager() {
  # Prefer a live systemd manager. LXC/OpenVZ templates sometimes ship systemctl
  # without a real PID1 systemd; require /run/systemd/system.
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    echo systemd
    return
  fi
  if command -v rc-service >/dev/null 2>&1 || [ -x /sbin/openrc-run ] || [ -d /etc/init.d ]; then
    # Alpine / Gentoo / some LXC images
    if command -v rc-service >/dev/null 2>&1 || command -v openrc >/dev/null 2>&1 || [ -x /sbin/openrc-run ]; then
      echo openrc
      return
    fi
  fi
  echo unknown
}

OS_VALUE=$(detect_os)
ARCH_VALUE=$(detect_arch)
SERVICE_MANAGER=$(detect_service_manager)
VIRT_HINT=$(detect_virt_hint)
if [ "$OS_VALUE" != linux ] || [ "$ARCH_VALUE" = unsupported ]; then
  echo "当前系统暂不支持：$(uname -s)/$(uname -m)" >&2
  exit 1
fi
prepare_install_log
trap finish_install EXIT
echo "OBoard Agent"
echo "------------"
echo "主控地址：$BASE_URL"
echo "安装目录：$INSTALL_DIR"
echo ""
echo "[1/4] 检查运行环境"
printf '环境：linux/%s 服务管理器=%s 虚拟化=%s\n' "$ARCH_VALUE" "$SERVICE_MANAGER" "$VIRT_HINT" >> "$INSTALL_LOG"
ensure_base_tools
ensure_acme_sh
fix_hostname_resolution
load_target_version

__AGENT_DOWNLOAD_HELPERS__

make_update_tmp() {
  for cleanup_root in "${OBOARD_TMPDIR:-}" /var/tmp "$STATE_DIR" /tmp /run; do
    [ -n "$cleanup_root" ] || continue
    [ -d "$cleanup_root" ] || continue
    find "$cleanup_root" -maxdepth 1 -type d \( -name 'oboard-agent-update.*' -o -name 'oboard-self-update.*' \) -mtime +0 -exec rm -rf {} + 2>/dev/null || true
  done
  for base in "${OBOARD_TMPDIR:-}" /var/tmp "$STATE_DIR" /tmp /run; do
    [ -n "$base" ] || continue
    mkdir -p "$base" 2>/dev/null || continue
    candidate=$(mktemp -d "$base/oboard-agent-update.XXXXXX" 2>/dev/null || true)
    [ -n "$candidate" ] || continue
    available_kb=$(df -Pk "$candidate" 2>/dev/null | awk 'NR==2 {print $4}')
    if [ -n "$available_kb" ] && [ "$available_kb" -ge 65536 ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
    rm -rf "$candidate"
  done
  echo "没有可用的更新临时目录，需要至少 64 MB 可用空间。" >&2
  echo "请清理 /var/tmp、$STATE_DIR、/tmp 或 /run 后重试；也可通过 OBOARD_TMPDIR 指定其他目录。" >&2
  df -h / /var/tmp "$STATE_DIR" /tmp /run 2>/dev/null >&2 || true
  df -i / /var/tmp "$STATE_DIR" /tmp /run 2>/dev/null >&2 || true
  return 1
}

download_binaries() {
  need_base_url
  echo "[2/4] 下载 Agent 组件"
  tmp=$(make_update_tmp)
  UPDATE_TMP=$tmp
  agent_name="oboard-agent-${OS_VALUE}-${ARCH_VALUE}"
  core_name="oboard-sb-${OS_VALUE}-${ARCH_VALUE}"
  realm_name="oboard-realm-${OS_VALUE}-${ARCH_VALUE}"
  agent_url="${BASE_URL}/downloads/${agent_name}"
  core_url="${BASE_URL}/downloads/${core_name}"
  realm_url="${BASE_URL}/downloads/${realm_name}"
  download_component "Agent" "$agent_url" "$tmp/$agent_name"
  download_component "优化内核" "$core_url" "$tmp/$core_name"
  download_component "端口转发组件" "$realm_url" "$tmp/$realm_name"
  echo "[3/4] 校验并安装组件"
  download_quiet "${BASE_URL}/downloads/release-manifest.json" "$tmp/release-manifest.json"
  download_quiet "${BASE_URL}/downloads/release-manifest.json.sig" "$tmp/release-manifest.json.sig"
  verify_downloaded_release "$tmp/release-manifest.json" "$tmp/release-manifest.json.sig" "$tmp" "$OS_VALUE" "$ARCH_VALUE" "$agent_name" "$core_name" "$realm_name" >> "$INSTALL_LOG" 2>&1
  chmod 0755 "$tmp/$agent_name" "$tmp/$core_name" "$tmp/$realm_name"
  install -d -m 0755 -o root -g root "$INSTALL_DIR"
  # Do not truncate an executable that may currently be running. Write beside it
  # and atomically rename; Linux keeps the old inode for the running process and
  # new restarts pick up the new binary.
  install -m 0755 "$tmp/$agent_name" "$INSTALL_DIR/oboard-agent.new"
  install -m 0755 "$tmp/$core_name" "$INSTALL_DIR/oboard-sb.new"
  install -m 0755 "$tmp/$realm_name" "$INSTALL_DIR/oboard-realm.new"
  preflight_staged_core "$INSTALL_DIR/oboard-sb.new"
  mv -f "$INSTALL_DIR/oboard-agent.new" "$INSTALL_DIR/oboard-agent"
  mv -f "$INSTALL_DIR/oboard-sb.new" "$INSTALL_DIR/oboard-sb"
  mv -f "$INSTALL_DIR/oboard-realm.new" "$INSTALL_DIR/oboard-realm"
  rm -f "$INSTALL_DIR/obag"
  ln -s "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/obag"
  register_obag_path
}

# The downloaded kernel is checked against the configuration this node is
# serving right now, while every file on disk is still the working one.
# Installing first and finding out at restart time replaces a stale-but-serving
# node with an outage.
preflight_staged_core() {
  staged=$1
  staged_config="$STATE_DIR/sing-box.json"
  if [ ! -s "$staged_config" ]; then
    return 0
  fi
  echo "校验新版内核是否接受当前运行的配置"
  staged_status=0
  "$staged" -check -config "$staged_config" >> "$INSTALL_LOG" 2>&1 || staged_status=$?
  case "$staged_status" in
    0) return 0 ;;
    126|127)
      # The staged binary could not be executed at all, so there is no verdict.
      echo "无法执行新版内核进行预检（退出码 $staged_status），已跳过该检查。" >&2
      return 0
      ;;
  esac
  rm -f "$INSTALL_DIR/oboard-agent.new" "$INSTALL_DIR/oboard-sb.new" "$INSTALL_DIR/oboard-realm.new"
  echo "新版内核无法接受当前正在运行的配置，已中止更新，未替换任何文件。" >&2
  echo "请先在面板重新下发配置后重试；详细信息见 $INSTALL_LOG。" >&2
  return 1
}

register_obag_path() {
  case "$INSTALL_DIR" in
    /usr/local/bin|/usr/local/sbin|/usr/bin|/usr/sbin|/bin|/sbin) return 0 ;;
  esac
  profile_dir=${OBOARD_PROFILE_DIR:-/etc/profile.d}
  if ! mkdir -p "$profile_dir" 2>/dev/null; then
    echo "无法创建 $profile_dir，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  fi
  profile_path="$profile_dir/oboard-agent.sh"
  if [ -e "$profile_path" ] && [ ! -f "$profile_path" ]; then
    echo "无法注册 obag 快捷命令：$profile_path 不是普通文件。" >&2
    return 1
  fi
  tmp_path="$profile_path.new.$$"
  rm -f "$tmp_path"
  if ! (umask 022 && printf '%s\n' \
    '# OBoard Agent management command PATH; regenerated by the Controller installer' \
    'case ":$PATH:" in' \
    "  *\":$INSTALL_DIR:\"*) ;;" \
    '  *) PATH="$PATH:'"$INSTALL_DIR"'" ;;' \
    'esac' \
    'export PATH' > "$tmp_path"); then
    rm -f "$tmp_path"
    echo "无法写入 $profile_path，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  fi
  chmod 0644 "$tmp_path" || {
    rm -f "$tmp_path"
    echo "无法写入 $profile_path，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  }
  if ! mv -f "$tmp_path" "$profile_path"; then
    rm -f "$tmp_path"
    echo "无法写入 $profile_path，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  fi
}

print_management_help() {
  result_title=$1
  management_command=obag
  command -v obag >/dev/null 2>&1 || management_command="$INSTALL_DIR/obag"
  if [ "$management_command" != obag ]; then
    echo "当前会话尚未刷新 PATH；重新登录 SSH 后可直接输入 obag。"
  fi
  echo ""
  echo "OBoard Agent $result_title"
  echo "------------------------"
  echo "管理 Agent：$management_command"
  echo "查看状态：$management_command status"
  echo "检查连接：$management_command check"
  echo "Agent 日志：$management_command logs agent"
  echo "内核日志：$management_command logs core"
}

write_systemd_units() {
  cat > /etc/systemd/system/oboard-sb.service <<UNIT
[Unit]
Description=OBoard optimized sing-box kernel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/oboard-sb -config $STATE_DIR/sing-box.json -api unix:/run/oboard-sb.sock
StandardOutput=append:/var/log/oboard-sb.log
StandardError=append:/var/log/oboard-sb.log
# No ExecReload: oboard-sb installs SIGINT/SIGTERM handlers only, so a HUP is
# not a configuration reload. Agent applies every change with a controlled
# restart that is verified against the kernel's own runtime status.
Restart=always
RestartSec=3
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=$STATE_DIR /var/log /run
LockPersonality=true
# Allow low-memory socket governor to tune tcp_rmem/tcp_wmem when the host grants access
# (common on KVM; may still fail on unprivileged LXC and is reported in diagnostics).
ProtectKernelTunables=false
ProtectControlGroups=false
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT
  cat > /etc/systemd/system/oboard-agent.service <<UNIT
[Unit]
Description=OBoard Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# Intentionally privileged host-management service; see Agent privilege boundary.
User=root
ExecStart=$INSTALL_DIR/oboard-agent -config $CONFIG_PATH
StandardOutput=append:/var/log/oboard-agent.log
StandardError=append:/var/log/oboard-agent.log
Restart=always
RestartSec=5
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
# Agent reconciles tunnel prerequisites and its dedicated SSH account.
ProtectSystem=false
ProtectHome=true
ReadWritePaths=$INSTALL_DIR $(dirname "$CONFIG_PATH") $STATE_DIR /var/log /run
LockPersonality=true
MemoryDenyWriteExecute=true
ProtectClock=true
ProtectHostname=true
ProtectKernelLogs=true
RestrictRealtime=true
RestrictSUIDSGID=true
SystemCallArchitectures=native
# Allow low-memory socket governor to tune tcp_rmem/tcp_wmem when the host grants access
# (common on KVM; may still fail on unprivileged LXC and is reported in diagnostics).
ProtectKernelTunables=false
ProtectControlGroups=false
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload >> "$INSTALL_LOG" 2>&1
  systemctl enable oboard-sb >> "$INSTALL_LOG" 2>&1
  systemctl enable oboard-agent >> "$INSTALL_LOG" 2>&1
}

write_openrc_units() {
  cat > /etc/init.d/oboard-sb <<OPENRC
#!/sbin/openrc-run

description="OBoard optimized sing-box kernel"
command="$INSTALL_DIR/oboard-sb"
command_args="-config $STATE_DIR/sing-box.json -api unix:/run/oboard-sb.sock"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/var/log/\${RC_SVCNAME}.log"
error_log="/var/log/\${RC_SVCNAME}.log"
# No reload action: oboard-sb installs SIGINT/SIGTERM handlers only, so a HUP
# would kill the kernel here, and OpenRC has no automatic restart to recover it.
# Agent applies every change with a controlled restart instead.

depend() {
  need net
  after firewall
}

start_pre() {
  checkpath -d -m 0700 "$STATE_DIR"
}
OPENRC
  cat > /etc/init.d/oboard-agent <<OPENRC
#!/sbin/openrc-run

description="OBoard Agent"
command="$INSTALL_DIR/oboard-agent"
command_args="-config $CONFIG_PATH"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/var/log/\${RC_SVCNAME}.log"
error_log="/var/log/\${RC_SVCNAME}.log"

depend() {
  need net
  after firewall
}

start_pre() {
  checkpath -d -m 0700 "$(dirname "$CONFIG_PATH")" "$STATE_DIR"
}
OPENRC
  chmod 0755 /etc/init.d/oboard-sb /etc/init.d/oboard-agent
  rc-update add oboard-sb default >> "$INSTALL_LOG" 2>&1
  rc-update add oboard-agent default >> "$INSTALL_LOG" 2>&1
}

write_units() {
  install -d -m 0700 "$(dirname "$CONFIG_PATH")" "$STATE_DIR"
  if [ "$SERVICE_MANAGER" = systemd ]; then
    write_systemd_units
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    write_openrc_units
  else
    echo "未识别服务管理器，只安装二进制。" >&2
  fi
}

restart_after_install() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl restart oboard-agent >> "$INSTALL_LOG" 2>&1
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    rc-service oboard-agent restart >> "$INSTALL_LOG" 2>&1
  else
    echo "请手动运行：$INSTALL_DIR/oboard-agent -config $CONFIG_PATH" >&2
  fi
}

# Replacing binaries is not an update. Every restart below is fatal on failure,
# so a node that keeps running the old kernel or loses its Agent can never end
# with this script printing "更新完成".
restart_core_after_update() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl daemon-reload >> "$INSTALL_LOG" 2>&1 || true
  fi
  if [ ! -s "$STATE_DIR/sing-box.json" ]; then
    return 0
  fi
  if ! restart_managed_service oboard-sb; then
    echo "内核 oboard-sb 重启失败，更新未完成。详细信息见 $INSTALL_LOG。" >&2
    return 1
  fi
  if ! wait_service_stable oboard-sb 15; then
    echo "内核 oboard-sb 重启后未能保持运行，更新未完成。详细信息见 $INSTALL_LOG。" >&2
    return 1
  fi
  return 0
}

restart_agent_after_update() {
  if [ "$SERVICE_MANAGER" != systemd ] && [ "$SERVICE_MANAGER" != openrc ]; then
    echo "未识别服务管理器，二进制已更新；请手动重启 oboard-sb 与 oboard-agent。" >&2
    return 1
  fi
  if ! restart_managed_service oboard-agent; then
    echo "Agent 重启失败，更新未完成。详细信息见 $INSTALL_LOG。" >&2
    return 1
  fi
  if ! wait_service_stable oboard-agent 15; then
    echo "Agent 重启后未能保持运行，更新未完成。详细信息见 $INSTALL_LOG。" >&2
    return 1
  fi
  return 0
}

case "$ACTION" in
  install)
    need_base_url
    : "${OBOARD_ENROLL_TOKEN:?缺少 OBOARD_ENROLL_TOKEN}"
    acquire_core_lifecycle_lock
    download_binaries
    persist_agent_install_dir
    write_units
    echo "[4/4] 注册并启动 Agent 服务"
    resolve_update_policy
    try_enable_bbr_fq
    if ! OBOARD_ENROLL_TOKEN="$OBOARD_ENROLL_TOKEN" "$INSTALL_DIR/oboard-agent" \
      -config "$CONFIG_PATH" \
      -controller "$BASE_URL" \
      -state-dir "$STATE_DIR" \
      -core-binary "$INSTALL_DIR/oboard-sb" \
      -core-service oboard-sb \
      -update-source "$UPDATE_SOURCE" \
      -allow-panel-update="$ALLOW_PANEL_UPDATE_BOOL" \
      -update-repo "$UPDATE_REPO" \
      -enroll-only >> "$INSTALL_LOG" 2>&1; then
      echo "Agent 未能连接主控完成注册，请确认主控地址和安装令牌后重试。" >&2
      exit 1
    fi
    unset OBOARD_ENROLL_TOKEN
    release_core_lifecycle_lock
    restart_after_install
    verify_installed_versions
    print_management_help "安装完成"
    echo "提示：oboard-sb 会在面板首次下发配置后自动启动。"
    ;;
  update)
    need_base_url
    acquire_core_lifecycle_lock
    download_binaries
    persist_agent_install_dir
    write_units
    echo "[4/4] 刷新 Agent 服务"
    restart_core_after_update
    verify_core_runtime
    # The Agent is restarted after the lock is released so its first task on
    # reconnect does not collide with this installer still holding it.
    release_core_lifecycle_lock
    restart_agent_after_update
    verify_installed_versions
    print_management_help "更新完成"
    ;;
  uninstall)
    # Handled before dependency detection and downloads.
    exit 0
    ;;
  *)
    echo "未知操作：$ACTION" >&2
    exit 1
    ;;
esac
`
	script = strings.ReplaceAll(script, "__BASE_URL__", shellSingleQuote(baseURL))
	script = strings.ReplaceAll(script, "__RELEASE_PUBLIC_KEY__", shellSingleQuote(version.ReleasePublicKey))
	script = strings.ReplaceAll(script, "__AGENT_RELEASE_VERIFIER__", agentReleaseVerifierShell)
	script = strings.ReplaceAll(script, "__AGENT_DOWNLOAD_HELPERS__", agentDownloadHelpersShell)
	_, _ = w.Write([]byte(script))
}

const agentDownloadHelpersShell = `format_download_value() {
  awk -v bytes="${1:-0}" 'BEGIN {
    split("B KB MB GB TB", units, " ")
    value = bytes + 0
    unit = 1
    while (value >= 1024 && unit < 5) {
      value /= 1024
      unit++
    }
    if (unit == 1) printf "%.0f %s", value, units[unit]
    else printf "%.1f %s", value, units[unit]
  }'
}

download_component() {
  label=$1
  url=$2
  destination=$3
  echo "  $label"
  if [ -t 2 ]; then
    meter=--progress-bar
  else
    meter=--silent
  fi

  attempt=1
  while :; do
    if stats=$(curl --proto '=http,https' --proto-redir '=https' --tlsv1.2 \
      --fail --location --show-error --connect-timeout 15 --continue-at - "$meter" \
      --write-out '%{size_download} %{speed_download}' "$url" -o "$destination"); then
      break
    else
      curl_status=$?
    fi
    case "$curl_status" in
      5|6|7|16|18|28|35|52|55|56|92) ;;
      *) echo "下载失败：$label" >&2; return 1 ;;
    esac
    if [ "$attempt" -ge 3 ]; then
      echo "下载失败：$label（连续 3 次连接失败）" >&2
      return 1
    fi
    echo "  连接中断，保留已下载内容并重试（$attempt/3）..." >&2
    sleep "$attempt"
    attempt=$((attempt + 1))
  done
  if [ "$attempt" -gt 1 ]; then
    size=$(wc -c < "$destination" | tr -d '[:space:]')
  else
    size=${stats%% *}
  fi
  speed=${stats#* }
  printf '  完成：%s · %s/s\n' "$(format_download_value "$size")" "$(format_download_value "$speed")"
}

download_quiet() {
  quiet_url=$1
  quiet_destination=$2
  quiet_attempt=1
  while :; do
    if curl --proto '=http,https' --proto-redir '=https' --tlsv1.2 \
      --fail --silent --show-error --location --connect-timeout 15 --continue-at - \
      "$quiet_url" -o "$quiet_destination"; then
      return 0
    else
      quiet_status=$?
    fi
    case "$quiet_status" in
      5|6|7|16|18|28|35|52|55|56|92) ;;
      *) return 1 ;;
    esac
    [ "$quiet_attempt" -lt 3 ] || return 1
    sleep "$quiet_attempt"
    quiet_attempt=$((quiet_attempt + 1))
  done
}`

const agentReleaseVerifierShell = `ensure_release_verifier() {
  need_tools=0
  if ! command -v python3 >/dev/null 2>&1 || ! command -v openssl >/dev/null 2>&1; then
    need_tools=1
  fi
  if [ "$need_tools" = 1 ]; then
    echo "正在准备安装包校验组件..."
  fi
  packages=""
  command -v python3 >/dev/null 2>&1 || packages="$packages python3"
  command -v openssl >/dev/null 2>&1 || packages="$packages openssl"
  # shellcheck disable=SC2086
  if [ -n "$packages" ] && ! pkg_install $packages; then
    echo "缺少 python3 和 openssl，且自动安装失败。请手动安装后重试。" >&2
    exit 1
  fi
  if ! command -v python3 >/dev/null 2>&1 || ! command -v openssl >/dev/null 2>&1; then
    echo "安装后仍缺少 python3 或 openssl。" >&2
    exit 1
  fi
  if openssl_supports_ed25519 || python_supports_ed25519; then
    return 0
  fi
  echo "当前 OpenSSL 不支持 Ed25519，正在安装兼容验签组件..."
  if ! install_python_cryptography || ! python_supports_ed25519; then
    echo "无法准备 Ed25519 验签组件。请安装系统 Python cryptography 包后重试。" >&2
    exit 1
  fi
}

openssl_supports_ed25519() {
  openssl pkeyutl -help 2>&1 | grep -q -- '-rawin'
}

python_supports_ed25519() {
  python3 -c 'from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey' >/dev/null 2>&1
}

install_python_cryptography() {
  if command -v apk >/dev/null 2>&1; then
    pkg_install py3-cryptography
  elif command -v pacman >/dev/null 2>&1; then
    pkg_install python-cryptography
  else
    pkg_install python3-cryptography
  fi
}

verify_ed25519_signature() {
  public_raw=$1
  public_der=$2
  message=$3
  signature=$4
  if openssl_supports_ed25519; then
    openssl pkeyutl -verify -pubin -inkey "$public_der" -rawin -in "$message" -sigfile "$signature" >/dev/null
    return
  fi
  python3 - "$public_raw" "$message" "$signature" <<'PY'
import sys
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

public_key = Ed25519PublicKey.from_public_bytes(open(sys.argv[1], 'rb').read())
public_key.verify(open(sys.argv[3], 'rb').read(), open(sys.argv[2], 'rb').read())
PY
}`

func (s *Server) agentSelfUpdateScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		method(w)
		return
	}
	baseURL, err := s.publicBaseURL(r.Context())
	if err != nil {
		fail(w, err, http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	script := strings.ReplaceAll(`#!/bin/sh
set -eu

BASE_URL=__BASE_URL__
RELEASE_PUBLIC_KEY=__RELEASE_PUBLIC_KEY__
INSTALL_DIR_INPUT=${INSTALL_DIR:-${OBOARD_INSTALL_DIR:-}}
INSTALL_ENV_PATH=${OBOARD_AGENT_INSTALL_ENV:-/etc/oboard-agent/install.env}
INSTALL_DIR=
CONFIG_PATH=${OBOARD_AGENT_CONFIG:-/etc/oboard-agent/config.json}
STATE_DIR=${OBOARD_AGENT_STATE:-/var/lib/oboard-agent}
AGENT_RESTART=${OBOARD_AGENT_RESTART:-delayed}

if [ "$(id -u)" -ne 0 ]; then
  echo "请使用 root 执行，或通过 sudo 运行命令。" >&2
  exit 1
fi

configured_agent_install_dir() {
  [ -f "$INSTALL_ENV_PATH" ] || return 0
  sed -n 's/^OBOARD_INSTALL_DIR=//p' "$INSTALL_ENV_PATH" 2>/dev/null | tail -n1 | tr -d "'\""
}

normalize_install_dir() {
  value=${1:-/usr/local/bin}
  while [ "$value" != / ] && [ "${value%/}" != "$value" ]; do
    value=${value%/}
  done
  case "$value" in
    /*) ;;
    *) return 1 ;;
  esac
  case "$value" in
    /|*//*|*[!A-Za-z0-9_./-]*) return 1 ;;
  esac
  case "$value/" in
    */./*|*/../*) return 1 ;;
  esac
  printf '%s\n' "$value"
}

persisted_install_dir=$(configured_agent_install_dir)
if [ -n "$persisted_install_dir" ]; then
  if ! persisted_install_dir=$(normalize_install_dir "$persisted_install_dir"); then
    echo "已保存的 Agent 安装目录无效。" >&2
    exit 1
  fi
fi
if [ -n "$INSTALL_DIR_INPUT" ]; then
  if ! INSTALL_DIR_INPUT=$(normalize_install_dir "$INSTALL_DIR_INPUT"); then
    echo "INSTALL_DIR/OBOARD_INSTALL_DIR 必须是规范的绝对路径。" >&2
    exit 1
  fi
fi
if [ -n "$INSTALL_DIR_INPUT" ] && [ -n "$persisted_install_dir" ] && [ "$INSTALL_DIR_INPUT" != "$persisted_install_dir" ]; then
  echo "已安装 Agent 使用 $persisted_install_dir；自更新时不能改为 $INSTALL_DIR_INPUT。" >&2
  exit 1
fi
INSTALL_DIR=${INSTALL_DIR_INPUT:-${persisted_install_dir:-/usr/local/bin}}
export INSTALL_DIR

fix_hostname_resolution() {
  host_name=$(hostname 2>/dev/null || true)
  [ -n "$host_name" ] || return 0
  [ -w /etc/hosts ] || return 0
  resolved=0
  if command -v getent >/dev/null 2>&1; then
    getent hosts "$host_name" >/dev/null 2>&1 && resolved=1
  elif command -v python3 >/dev/null 2>&1; then
    python3 - "$host_name" <<'PY' >/dev/null 2>&1 && resolved=1
import socket, sys
socket.getaddrinfo(sys.argv[1], None)
PY
  else
    # Minimal systems without getent: accept if already listed in /etc/hosts.
    grep -E "(^|[[:space:]])${host_name}([[:space:]]|$)" /etc/hosts >/dev/null 2>&1 && resolved=1
  fi
  if [ "$resolved" = 1 ]; then
    return 0
  fi
  printf '\n127.0.1.1 %s\n' "$host_name" >> /etc/hosts
  echo "已修复本机 hostname 解析：$host_name"
}

json_value() {
  key=$1
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1], ""))' "$key"
  else
    grep -o "\"$key\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -n1 | sed 's/.*:[[:space:]]*"//; s/"$//'
  fi
}

raw_b64_to_file() {
  value=$1
  out=$2
  python3 - "$value" "$out" <<'PY'
import base64, sys
v=sys.argv[1]
v += '=' * ((4 - len(v) % 4) % 4)
open(sys.argv[2], 'wb').write(base64.b64decode(v))
PY
}

pkg_install() {
  # Install packages with the host package manager. Supports Debian/Ubuntu,
  # Alpine, RHEL/CentOS/Rocky/Alma (dnf/yum), openSUSE (zypper), Arch (pacman).
  if [ "$#" -eq 0 ]; then
    return 0
  fi
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache "$@"
  elif command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    apt-get install -y --no-install-recommends "$@"
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "$@"
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "$@"
  elif command -v microdnf >/dev/null 2>&1; then
    microdnf install -y "$@"
  elif command -v zypper >/dev/null 2>&1; then
    zypper --non-interactive install -y "$@"
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm "$@"
  else
    echo "未找到支持的包管理器（apk/apt/dnf/yum/zypper/pacman），无法自动安装：$*" >&2
    return 1
  fi
}

ensure_base_tools() {
  # curl + ca-certificates are required for HTTPS downloads on minimal
  # containers (Alpine, Debian slim, CentOS minimal, many LXC templates).
  need_curl=0
  need_ca=0
  need_install=0
  command -v curl >/dev/null 2>&1 || need_curl=1
  command -v install >/dev/null 2>&1 || need_install=1
  if [ ! -f /etc/ssl/certs/ca-certificates.crt ] && [ ! -f /etc/pki/tls/certs/ca-bundle.crt ] && [ ! -f /etc/ssl/cert.pem ]; then
    need_ca=1
  fi
  if [ "$need_curl$need_ca$need_install" = "000" ]; then
    return 0
  fi
  echo "正在安装基础依赖（curl / CA 证书 / install）..."
  packages=""
  if [ "$need_curl" = 1 ]; then
    packages="$packages curl"
  fi
  if [ "$need_ca" = 1 ]; then
    packages="$packages ca-certificates"
  fi
  if [ "$need_install" = 1 ]; then
    packages="$packages coreutils"
  fi
  # shellcheck disable=SC2086
  pkg_install $packages || {
    echo "基础依赖安装失败，请手动安装 curl、CA 证书和 install 后重试。" >&2
    exit 1
  }
  if command -v update-ca-certificates >/dev/null 2>&1; then
    update-ca-certificates >/dev/null 2>&1 || true
  elif command -v update-ca-trust >/dev/null 2>&1; then
    update-ca-trust extract >/dev/null 2>&1 || true
  fi
}

__AGENT_RELEASE_VERIFIER__

detect_virt_hint() {
  # Best-effort virtualization/container hint for install logs and service tweaks.
  if [ -f /.dockerenv ] || [ -f /run/.containerenv ]; then
    echo container
    return
  fi
  if [ -r /run/systemd/container ]; then
    cat /run/systemd/container 2>/dev/null | tr -d '\n' || echo container
    return
  fi
  if [ -n "${container:-}" ]; then
    echo "$container"
    return
  fi
  if command -v systemd-detect-virt >/dev/null 2>&1; then
    v=$(systemd-detect-virt 2>/dev/null || true)
    if [ -n "$v" ] && [ "$v" != none ]; then
      echo "$v"
      return
    fi
  fi
  if [ -r /proc/1/environ ] && tr '\0' '\n' </proc/1/environ 2>/dev/null | grep -qiE '^(container=|lxc|libpod)'; then
    echo container
    return
  fi
  if [ -r /proc/1/cgroup ] && grep -qiE 'lxc|docker|kubepods|containerd|podman|libpod|incus' /proc/1/cgroup 2>/dev/null; then
    echo container
    return
  fi
  echo bare
}

verify_manifest_with_openssl() {
  manifest=$1
  sig=$2
  base_dir=$3
  os_value=$4
  arch_value=$5
  shift 5
  if [ -z "$RELEASE_PUBLIC_KEY" ]; then
    if [ "${TARGET_DEV:-false}" = true ] && [ "${OBOARD_ALLOW_UNSIGNED_DEV_UPDATE:-0}" = "1" ]; then
      echo "开发模式：跳过 unsigned manifest 校验。" >&2
      return 0
    fi
    echo "缺少 release 公钥，拒绝安装未验证的 Agent。" >&2
    exit 1
  fi
  ensure_release_verifier
  raw_b64_to_file "$RELEASE_PUBLIC_KEY" "$base_dir/release-public.raw"
  raw_b64_to_file "$(tr -d '[:space:]' < "$sig")" "$base_dir/release.sig"
  python3 - "$base_dir/release-public.raw" "$base_dir/release-public.der" <<'PY'
import sys
raw=open(sys.argv[1], 'rb').read()
if len(raw) != 32:
    raise SystemExit('invalid Ed25519 public key length')
open(sys.argv[2], 'wb').write(bytes.fromhex('302a300506032b6570032100') + raw)
PY
  verify_ed25519_signature "$base_dir/release-public.raw" "$base_dir/release-public.der" "$manifest" "$base_dir/release.sig"
  python3 - "$manifest" "$base_dir" "$os_value" "$arch_value" "$@" <<'PY'
import hashlib, json, pathlib, sys
manifest=json.load(open(sys.argv[1]))
base=pathlib.Path(sys.argv[2]); os_value=sys.argv[3]; arch_value=sys.argv[4]
want={item['name']: item for item in manifest.get('files', []) if item.get('os') == os_value and item.get('arch') == arch_value}
for name in sys.argv[5:]:
    item=want.get(name)
    if not item:
        raise SystemExit(f'{name} not found in release manifest')
    data=(base/name).read_bytes()
    if hashlib.sha256(data).hexdigest() != item.get('sha256') or len(data) != int(item.get('size', -1)):
        raise SystemExit(f'{name} checksum mismatch')
PY
}

verify_downloaded_release() {
  manifest=$1
  sig=$2
  base_dir=$3
  os_value=$4
  arch_value=$5
  shift 5
  if [ -x "$INSTALL_DIR/oboard-agent" ] && "$INSTALL_DIR/oboard-agent" -h 2>&1 | grep -q -- '-verify-release'; then
    "$INSTALL_DIR/oboard-agent" -verify-release -verify-manifest "$manifest" -verify-signature "$sig" -verify-base-dir "$base_dir" -verify-os "$os_value" -verify-arch "$arch_value" "$@"
  else
    verify_manifest_with_openssl "$manifest" "$sig" "$base_dir" "$os_value" "$arch_value" "$@"
  fi
}

load_target_version() {
  version_json=$(curl -fsSL "$BASE_URL/api/v1/ui/version" 2>/dev/null || true)
  TARGET_VERSION=$(printf '%s' "$version_json" | json_value agent_expected_version 2>/dev/null || true)
  TARGET_BUILD=$(printf '%s' "$version_json" | json_value agent_expected_build 2>/dev/null || true)
  TARGET_KERNEL_BUILD=$(printf '%s' "$version_json" | json_value kernel_build 2>/dev/null || true)
  TARGET_DEV=$(printf '%s' "$version_json" | json_value dev 2>/dev/null || true)
  if [ -n "$TARGET_VERSION$TARGET_BUILD" ]; then
    echo "目标版本：Agent ${TARGET_VERSION:-unknown} build ${TARGET_BUILD:-unknown}，内核 build ${TARGET_KERNEL_BUILD:-$TARGET_BUILD}"
  fi
}

print_installed_versions() {
  echo "当前二进制版本："
  if [ -x "$INSTALL_DIR/oboard-agent" ]; then
    echo "- Agent: $($INSTALL_DIR/oboard-agent -version 2>/dev/null || true)"
  else
    echo "- Agent: 未安装"
  fi
  if [ -x "$INSTALL_DIR/oboard-sb" ]; then
    echo "- 内核: $($INSTALL_DIR/oboard-sb -version 2>/dev/null | head -n1 || true)"
  else
    echo "- 内核: 未安装"
  fi
  if [ -x "$INSTALL_DIR/oboard-realm" ]; then
    echo "- 端口转发: $($INSTALL_DIR/oboard-realm -v 2>/dev/null | head -n1 || true)"
  else
    echo "- 端口转发: 未安装"
  fi
}

verify_installed_versions() {
  print_installed_versions
  if [ -n "${TARGET_BUILD:-}" ] && [ -x "$INSTALL_DIR/oboard-agent" ]; then
    if ! "$INSTALL_DIR/oboard-agent" -version 2>/dev/null | grep -q "build $TARGET_BUILD"; then
      echo "安装的 Agent 二进制 build 与目标 build 不一致，更新未完成。" >&2
      return 1
    fi
  fi
  if [ -n "${TARGET_KERNEL_BUILD:-${TARGET_BUILD:-}}" ] && [ -x "$INSTALL_DIR/oboard-sb" ]; then
    expected_core_build=${TARGET_KERNEL_BUILD:-$TARGET_BUILD}
    if ! "$INSTALL_DIR/oboard-sb" -version 2>/dev/null | grep -q "\"build\": \"$expected_core_build\""; then
      echo "安装的优化内核 build 与目标 build 不一致，更新未完成。" >&2
      return 1
    fi
  fi
}

detect_os() {
  case "$(uname -s | tr '[:upper:]' '[:lower:]')" in
    linux) echo linux ;;
    *) echo unsupported ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) echo unsupported ;;
  esac
}

detect_service_manager() {
  # Prefer a live systemd manager. LXC/OpenVZ templates sometimes ship systemctl
  # without a real PID1 systemd; require /run/systemd/system.
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    echo systemd
    return
  fi
  if command -v rc-service >/dev/null 2>&1 || [ -x /sbin/openrc-run ] || [ -d /etc/init.d ]; then
    # Alpine / Gentoo / some LXC images
    if command -v rc-service >/dev/null 2>&1 || command -v openrc >/dev/null 2>&1 || [ -x /sbin/openrc-run ]; then
      echo openrc
      return
    fi
  fi
  echo unknown
}

OS_VALUE=$(detect_os)
ARCH_VALUE=$(detect_arch)
SERVICE_MANAGER=$(detect_service_manager)
VIRT_HINT=$(detect_virt_hint)
if [ "$OS_VALUE" != linux ] || [ "$ARCH_VALUE" = unsupported ]; then
  echo "当前系统暂不支持：$(uname -s)/$(uname -m)" >&2
  exit 1
fi
echo "环境：linux/$ARCH_VALUE 服务管理器=$SERVICE_MANAGER 虚拟化=$VIRT_HINT"
ensure_base_tools
install -d -m 0700 "$(dirname "$INSTALL_ENV_PATH")"
printf 'OBOARD_INSTALL_DIR=%s\n' "$INSTALL_DIR" > "$INSTALL_ENV_PATH.new"
chmod 0600 "$INSTALL_ENV_PATH.new"
mv -f "$INSTALL_ENV_PATH.new" "$INSTALL_ENV_PATH"
fix_hostname_resolution
load_target_version

make_update_tmp() {
  for cleanup_root in "${OBOARD_TMPDIR:-}" /var/tmp "$STATE_DIR" /tmp /run; do
    [ -n "$cleanup_root" ] || continue
    [ -d "$cleanup_root" ] || continue
    find "$cleanup_root" -maxdepth 1 -type d \( -name 'oboard-agent-update.*' -o -name 'oboard-self-update.*' \) -mtime +0 -exec rm -rf {} + 2>/dev/null || true
  done
  for base in "${OBOARD_TMPDIR:-}" /var/tmp "$STATE_DIR" /tmp /run; do
    [ -n "$base" ] || continue
    mkdir -p "$base" 2>/dev/null || continue
    candidate=$(mktemp -d "$base/oboard-self-update.XXXXXX" 2>/dev/null || true)
    [ -n "$candidate" ] || continue
    available_kb=$(df -Pk "$candidate" 2>/dev/null | awk 'NR==2 {print $4}')
    if [ -n "$available_kb" ] && [ "$available_kb" -ge 65536 ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
    rm -rf "$candidate"
  done
  echo "没有可用的更新临时目录，需要至少 64 MB 可用空间。" >&2
  echo "请清理 /var/tmp、$STATE_DIR、/tmp 或 /run 后重试；也可通过 OBOARD_TMPDIR 指定其他目录。" >&2
  df -h / /var/tmp "$STATE_DIR" /tmp /run 2>/dev/null >&2 || true
  df -i / /var/tmp "$STATE_DIR" /tmp /run 2>/dev/null >&2 || true
  return 1
}

__AGENT_DOWNLOAD_HELPERS__

tmp=$(make_update_tmp)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

agent_name="oboard-agent-${OS_VALUE}-${ARCH_VALUE}"
core_name="oboard-sb-${OS_VALUE}-${ARCH_VALUE}"
realm_name="oboard-realm-${OS_VALUE}-${ARCH_VALUE}"
echo "下载 Agent 组件"
download_component "Agent" "$BASE_URL/downloads/$agent_name" "$tmp/$agent_name"
download_component "优化内核" "$BASE_URL/downloads/$core_name" "$tmp/$core_name"
download_component "端口转发组件" "$BASE_URL/downloads/$realm_name" "$tmp/$realm_name"
download_quiet "$BASE_URL/downloads/release-manifest.json" "$tmp/release-manifest.json"
download_quiet "$BASE_URL/downloads/release-manifest.json.sig" "$tmp/release-manifest.json.sig"
verify_downloaded_release "$tmp/release-manifest.json" "$tmp/release-manifest.json.sig" "$tmp" "$OS_VALUE" "$ARCH_VALUE" "$agent_name" "$core_name" "$realm_name"
chmod 0755 "$tmp/$agent_name" "$tmp/$core_name" "$tmp/$realm_name"

# The Agent serializes its own kernel work with an in-process mutex this script
# cannot see. Both sides take the same advisory flock so a self-update and a
# panel-driven deployment cannot touch the same binaries and service at once.
CORE_LOCK_HELD=0
acquire_core_lifecycle_lock() {
  core_lock_path="$STATE_DIR/core-lifecycle.lock"
  mkdir -p "$STATE_DIR" 2>/dev/null || true
  if ! command -v flock >/dev/null 2>&1; then
    echo "未找到 flock，无法与面板下发的内核操作互斥；请确认此时没有正在进行的配置下发。" >&2
    return 0
  fi
  # A redirection failure on the ":" special builtin would exit a non-interactive
  # shell outright, so the writability probe runs in a subshell.
  if ! ( : >> "$core_lock_path" ) 2>/dev/null; then
    echo "无法创建更新互斥锁 $core_lock_path，继续执行。" >&2
    return 0
  fi
  chmod 0600 "$core_lock_path" 2>/dev/null || true
  exec 9>> "$core_lock_path"
  # BusyBox flock accepts only [-sxun]: -w is a usage error there, and its
  # non-zero exit is indistinguishable from a busy lock, so an Alpine host would
  # report a phantom concurrent deployment. A bounded -n retry loop is the one
  # wait util-linux and BusyBox both implement.
  core_lock_waited=0
  until flock -n 9; do
    if [ "$core_lock_waited" -ge 120 ]; then
      echo "另一个 OBoard 更新或面板配置下发正在进行，请稍后重试。" >&2
      exit 1
    fi
    sleep 1
    core_lock_waited=$((core_lock_waited + 1))
  done
  CORE_LOCK_HELD=1
  printf 'self-update %s\n' "$$" >&9 2>/dev/null || true
}

release_core_lifecycle_lock() {
  [ "$CORE_LOCK_HELD" = 1 ] || return 0
  CORE_LOCK_HELD=0
  exec 9>&-
}

# The downloaded kernel is checked against the configuration this node serves
# right now, before any file on disk is replaced. Finding out at restart time
# turns a stale-but-serving node into an outage.
preflight_staged_core() {
  staged=$1
  staged_config="$STATE_DIR/sing-box.json"
  if [ ! -s "$staged_config" ]; then
    return 0
  fi
  echo "校验新版内核是否接受当前运行的配置"
  staged_status=0
  "$staged" -check -config "$staged_config" >/dev/null 2>&1 || staged_status=$?
  case "$staged_status" in
    0) return 0 ;;
    126|127)
      echo "无法执行新版内核进行预检（退出码 $staged_status），已跳过该检查。" >&2
      return 0
      ;;
  esac
  echo "新版内核无法接受当前正在运行的配置，已中止更新，未替换任何文件。" >&2
  echo "请先在面板重新下发配置后重试。" >&2
  return 1
}

service_active() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl is-active --quiet "$1"
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    rc-service "$1" status >/dev/null 2>&1
  else
    return 1
  fi
}

# A service that comes up and immediately exits is a failed update. Restart exit
# codes alone do not catch that.
wait_service_stable() {
  wait_service=$1
  wait_seconds=${2:-15}
  wait_stable=0
  wait_elapsed=0
  while [ "$wait_elapsed" -lt "$wait_seconds" ]; do
    if service_active "$wait_service"; then
      wait_stable=$((wait_stable + 1))
      if [ "$wait_stable" -ge 3 ]; then
        return 0
      fi
    else
      wait_stable=0
    fi
    sleep 1
    wait_elapsed=$((wait_elapsed + 1))
  done
  return 1
}

# The kernel that is actually serving traffic is the only proof an update
# landed. Agent owns the operational-digest normalization, so the verdict is
# delegated to it rather than reimplemented here.
verify_core_runtime() {
  [ -s "$STATE_DIR/sing-box.json" ] || return 0
  [ -x "$INSTALL_DIR/oboard-agent" ] || return 0
  if ! "$INSTALL_DIR/oboard-agent" -h 2>&1 | grep -q -- '-verify-core-runtime'; then
    echo "当前 Agent 版本不支持内核运行态校验，已跳过该检查。" >&2
    return 0
  fi
  if "$INSTALL_DIR/oboard-agent" -verify-core-runtime \
    -config "$CONFIG_PATH" \
    -state-dir "$STATE_DIR" \
    -core-binary "$INSTALL_DIR/oboard-sb" \
    -core-service oboard-sb; then
    return 0
  fi
  echo "内核已重启，但运行中的进程仍不是本次安装的版本或配置，更新未完成。" >&2
  return 1
}

install_downloaded_binaries_direct() {
  install -d -m 0755 -o root -g root "$INSTALL_DIR"
  # Do not truncate an executable that may currently be running. Write beside it
  # and atomically rename; Linux keeps the old inode for the running process and
  # new restarts pick up the new binary.
  install -m 0755 "$tmp/$agent_name" "$INSTALL_DIR/oboard-agent.new"
  install -m 0755 "$tmp/$core_name" "$INSTALL_DIR/oboard-sb.new"
  install -m 0755 "$tmp/$realm_name" "$INSTALL_DIR/oboard-realm.new"
  mv -f "$INSTALL_DIR/oboard-agent.new" "$INSTALL_DIR/oboard-agent"
  mv -f "$INSTALL_DIR/oboard-sb.new" "$INSTALL_DIR/oboard-sb"
  mv -f "$INSTALL_DIR/oboard-realm.new" "$INSTALL_DIR/oboard-realm"
  rm -f "$INSTALL_DIR/obag"
  ln -s "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/obag"
}

register_obag_path() {
  case "$INSTALL_DIR" in
    /usr/local/bin|/usr/local/sbin|/usr/bin|/usr/sbin|/bin|/sbin) return 0 ;;
  esac
  profile_dir=${OBOARD_PROFILE_DIR:-/etc/profile.d}
  if ! mkdir -p "$profile_dir" 2>/dev/null; then
    echo "无法创建 $profile_dir，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  fi
  profile_path="$profile_dir/oboard-agent.sh"
  if [ -e "$profile_path" ] && [ ! -f "$profile_path" ]; then
    echo "无法注册 obag 快捷命令：$profile_path 不是普通文件。" >&2
    return 1
  fi
  tmp_path="$profile_path.new.$$"
  rm -f "$tmp_path"
  if ! (umask 022 && printf '%s\n' \
    '# OBoard Agent management command PATH; regenerated by the Controller installer' \
    'case ":$PATH:" in' \
    "  *\":$INSTALL_DIR:\"*) ;;" \
    '  *) PATH="$PATH:'"$INSTALL_DIR"'" ;;' \
    'esac' \
    'export PATH' > "$tmp_path"); then
    rm -f "$tmp_path"
    echo "无法写入 $profile_path，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  fi
  chmod 0644 "$tmp_path" || {
    rm -f "$tmp_path"
    echo "无法写入 $profile_path，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  }
  if ! mv -f "$tmp_path" "$profile_path"; then
    rm -f "$tmp_path"
    echo "无法写入 $profile_path，obag 快捷命令仅可通过 $INSTALL_DIR/obag 使用。" >&2
    return 1
  fi
}

install_downloaded_binaries_via_systemd() {
  helper="$tmp/install-helper.sh"
  cat > "$helper" <<'HELPER'
#!/bin/sh
set -eu
install -d -m 0755 -o root -g root "$INSTALL_DIR"
install -m 0755 "$TMP_DIR/$AGENT_NAME" "$INSTALL_DIR/oboard-agent.new"
install -m 0755 "$TMP_DIR/$CORE_NAME" "$INSTALL_DIR/oboard-sb.new"
install -m 0755 "$TMP_DIR/$REALM_NAME" "$INSTALL_DIR/oboard-realm.new"
mv -f "$INSTALL_DIR/oboard-agent.new" "$INSTALL_DIR/oboard-agent"
mv -f "$INSTALL_DIR/oboard-sb.new" "$INSTALL_DIR/oboard-sb"
mv -f "$INSTALL_DIR/oboard-realm.new" "$INSTALL_DIR/oboard-realm"
rm -f "$INSTALL_DIR/obag"
ln -s "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/obag"
HELPER
  chmod 0755 "$helper"
  systemd-run --wait --collect --pipe \
    --unit "oboard-agent-self-update-$$" \
    --setenv=INSTALL_DIR="$INSTALL_DIR" \
    --setenv=TMP_DIR="$tmp" \
    --setenv=AGENT_NAME="$agent_name" \
    --setenv=CORE_NAME="$core_name" \
    --setenv=REALM_NAME="$realm_name" \
    /bin/sh "$helper"
}

install_downloaded_binaries() {
  err_file="$tmp/install.err"
  if install_downloaded_binaries_direct 2>"$err_file"; then
    return 0
  fi
  cat "$err_file" >&2
  if [ "$SERVICE_MANAGER" = systemd ] && command -v systemd-run >/dev/null 2>&1 && grep -qi 'read-only file system' "$err_file"; then
    echo "当前 Agent 服务限制了系统目录写入，切换到 systemd 临时任务完成更新。"
    install_downloaded_binaries_via_systemd
    return 0
  fi
  return 1
}

acquire_core_lifecycle_lock
preflight_staged_core "$tmp/$core_name"
install_downloaded_binaries

register_obag_path || true

print_management_help() {
  management_command=obag
  command -v obag >/dev/null 2>&1 || management_command="$INSTALL_DIR/obag"
  if [ "$management_command" != obag ]; then
    echo "当前会话尚未刷新 PATH；重新登录 SSH 后可直接输入 obag。"
  fi
  echo ""
  echo "========================================"
  echo "OBoard Agent 自更新完成"
  echo "========================================"
  echo "通过 SSH 登录本机后输入：$management_command"
  echo "即可查看状态、控制服务、读取日志和检查主控连接。"
  echo ""
  echo "常用快捷命令："
  echo "  $management_command status       查看运行状态"
  echo "  $management_command check        检查与主控的连接"
  echo "  $management_command logs agent   查看 Agent 日志"
  echo "  $management_command logs core    查看 oboard-sb 日志"
  echo "========================================"
}

if [ -f "$CONFIG_PATH" ]; then
  if command -v python3 >/dev/null 2>&1; then
    TARGET_DEV="${TARGET_DEV:-}" python3 - "$CONFIG_PATH" "$INSTALL_DIR" <<'PY'
import json, sys
path = sys.argv[1]
install_dir = sys.argv[2]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)
data["reload_command"] = "auto"
data["restart_command"] = "auto"
data["time_sync_command"] = "auto"
data.setdefault("time_correction_mode", "auto")
if not data.get("core_binary"):
    data["core_binary"] = install_dir + "/oboard-sb"
if not data.get("core_service"):
    data["core_service"] = "oboard-sb"
data.setdefault("update_repo", "OboardProject/oboard-agent")
target_dev = str(__import__("os").environ.get("TARGET_DEV", "")).lower() in ("true", "1", "yes")
if "update_source" not in data:
    data["update_source"] = "panel" if target_dev else "github"
if "allow_panel_update" not in data:
    data["allow_panel_update"] = bool(target_dev)
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, indent=2, ensure_ascii=False)
    f.write("\n")
PY
  else
		sed -i 's#"reload_command"[[:space:]]*:[[:space:]]*"[^"]*"#"reload_command": "auto"#' "$CONFIG_PATH" || true
		sed -i 's#"restart_command"[[:space:]]*:[[:space:]]*"[^"]*"#"restart_command": "auto"#' "$CONFIG_PATH" || true
		sed -i 's#"time_sync_command"[[:space:]]*:[[:space:]]*"[^"]*"#"time_sync_command": "auto"#' "$CONFIG_PATH" || true
	fi
fi

restart_agent_delayed() {
	if [ "$SERVICE_MANAGER" = systemd ]; then
		nohup sh -c 'sleep 60; systemctl restart oboard-agent || true' >/dev/null 2>&1 &
	elif [ "$SERVICE_MANAGER" = openrc ]; then
		nohup sh -c 'sleep 60; rc-service oboard-agent restart || true' >/dev/null 2>&1 &
	fi
	echo "Agent 将在任务回传后自动重启。"
}

# Replacing binaries is not an update. A kernel restart that fails, or a kernel
# that comes back on the old build or the wrong configuration, must not end with
# this script reporting success.
restart_core_after_update() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl daemon-reload || true
  fi
  if [ ! -s "$STATE_DIR/sing-box.json" ]; then
    return 0
  fi
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl restart oboard-sb || { echo "内核 oboard-sb 重启失败，更新未完成。" >&2; return 1; }
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    rc-service oboard-sb restart || { echo "内核 oboard-sb 重启失败，更新未完成。" >&2; return 1; }
  else
    return 0
  fi
  if ! wait_service_stable oboard-sb 15; then
    echo "内核 oboard-sb 重启后未能保持运行，更新未完成。" >&2
    return 1
  fi
  return 0
}

restart_agent_after_update() {
	if [ "$AGENT_RESTART" = none ]; then
		echo "Agent 将在任务结果回传后由当前进程安排重启。"
		return 0
	fi
	if [ "$AGENT_RESTART" = delayed ]; then
		restart_agent_delayed
		return 0
	fi
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl restart oboard-agent || { echo "Agent 重启失败，更新未完成。" >&2; return 1; }
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    rc-service oboard-agent restart || { echo "Agent 重启失败，更新未完成。" >&2; return 1; }
  else
    return 0
  fi
  if ! wait_service_stable oboard-agent 15; then
    echo "Agent 重启后未能保持运行，更新未完成。" >&2
    return 1
  fi
  return 0
}

if [ "$SERVICE_MANAGER" != systemd ] && [ "$SERVICE_MANAGER" != openrc ]; then
  release_core_lifecycle_lock
  echo "未识别服务管理器，二进制已更新；请手动重启 oboard-sb 与 oboard-agent。" >&2
  exit 1
fi

restart_core_after_update
verify_core_runtime
# The Agent restarts after the lock is released so its first task on reconnect
# does not collide with this script still holding it.
release_core_lifecycle_lock
restart_agent_after_update

verify_installed_versions
print_management_help
`, "__BASE_URL__", shellSingleQuote(baseURL))
	script = strings.ReplaceAll(script, "__RELEASE_PUBLIC_KEY__", shellSingleQuote(version.ReleasePublicKey))
	script = strings.ReplaceAll(script, "__AGENT_RELEASE_VERIFIER__", agentReleaseVerifierShell)
	script = strings.ReplaceAll(script, "__AGENT_DOWNLOAD_HELPERS__", agentDownloadHelpersShell)
	_, _ = w.Write([]byte(script))
}

func (s *Server) publicBaseURL(ctx context.Context) (string, error) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(settings["controller_url"])
	if raw == "" {
		return "", errors.New("请先在系统设置中配置主控公开地址（controller_url）")
	}
	normalized, err := s.normalizeControllerURL(raw)
	if err != nil || normalized == "" {
		if err != nil {
			return "", err
		}
		return "", errors.New("controller_url 无效")
	}
	return normalized, nil
}

func (s *Server) subscriptionPublicBaseURL(ctx context.Context) (string, error) {
	configuredURL, err := s.store.GetSetting(ctx, settingSubscriptionRelayURL)
	if err != nil {
		return "", err
	}
	if normalized, normalizeErr := s.normalizeSubscriptionRelayURL(configuredURL); normalizeErr == nil && normalized != "" {
		return normalized, nil
	}
	relays, err := s.publicSubscriptionRelays(ctx)
	if err != nil {
		return "", err
	}
	for _, relay := range relays {
		if active, _ := relay["active"].(bool); active {
			if url, _ := relay["public_url"].(string); url != "" {
				return url, nil
			}
		}
	}
	return "", nil
}

func (s *Server) normalizeSubscriptionRelayURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return "", errors.New("subscription_relay_url must be a valid HTTPS URL")
	}
	path := strings.TrimSuffix(target.EscapedPath(), "/")
	want := s.currentBasePath()
	if path == "/" {
		path = ""
	}
	if path != want {
		return "", fmt.Errorf("subscription_relay_url path must be %s", fallbackBasePath(want))
	}
	target.Path = path
	target.RawPath = ""
	return strings.TrimSuffix(target.String(), "/"), nil
}

func fallbackBasePath(value string) string {
	if value == "" {
		return "/"
	}
	return value
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (s *Server) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		method(w)
		return
	}
	name := urlpath.Base(r.URL.Path)
	switch name {
	case "oboard-agent-linux-amd64", "oboard-agent-linux-arm64", "oboard-sb-linux-amd64", "oboard-sb-linux-arm64", "oboard-realm-linux-amd64", "oboard-realm-linux-arm64", "release-manifest.json", "release-manifest.json.sig", "oboard-subscription-relay-linux-amd64.tar.gz", "oboard-subscription-relay-linux-arm64.tar.gz", "subscription-relay-sha256s.txt":
	default:
		http.NotFound(w, r)
		return
	}
	root, err := filepath.Abs(downloadsDir(s.staticDir))
	if err != nil {
		fail(w, err, 500)
		return
	}
	candidate := filepath.Join(root, name)
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil || !pathContained(root, candidateAbs) {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(candidateAbs); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeFile(w, r, candidateAbs)
}

func downloadsDir(staticDir string) string {
	if dir := strings.TrimSpace(os.Getenv("OBOARD_DOWNLOADS")); dir != "" {
		return dir
	}
	if staticDir == "" {
		staticDir = "web/dist"
	}
	webDir := filepath.Dir(staticDir)
	rootDir := filepath.Dir(webDir)
	return filepath.Join(rootDir, "downloads")
}

func pathContained(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		fail(w, err, 400)
		return false
	}
	return true
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error, status int) {
	if status >= http.StatusInternalServerError {
		log.Printf("internal API error status=%d: %v", status, err)
		write(w, status, map[string]any{"error": "internal server error"})
		return
	}
	payload := map[string]any{"error": err.Error()}
	var located interface{ ValidationPath() string }
	if errors.As(err, &located) && strings.TrimSpace(located.ValidationPath()) != "" {
		payload["error_path"] = located.ValidationPath()
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) && strings.TrimSpace(coded.Code()) != "" {
		payload["code"] = coded.Code()
	}
	var detailed interface{ ErrorDetails() map[string]any }
	if errors.As(err, &detailed) {
		for key, value := range detailed.ErrorDetails() {
			if _, exists := payload[key]; !exists && strings.TrimSpace(key) != "" && key != "error" && key != "code" {
				payload[key] = value
			}
		}
	}
	write(w, status, payload)
}

func failCode(w http.ResponseWriter, code, message string, status int) {
	write(w, status, map[string]any{"error": message, "code": code})
}
func method(w http.ResponseWriter) { fail(w, errors.New("method not allowed"), 405) }
// writeOpaqueNotFound returns a bare 404 with no body. Used when a request
// misses the configured security base path so the response does not fingerprint
// the Controller through structured error JSON or request IDs.
func writeOpaqueNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
}

func writeNotFoundJSON(w http.ResponseWriter) {
	id, _ := security.RandomToken(18)
	writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": "404 page not found", "request_id": "req_" + id}})
}

func notFound(w http.ResponseWriter, r *http.Request) { writeNotFoundJSON(w) }
func pathParts(path, prefix string) []string {
	return strings.Split(strings.Trim(strings.TrimPrefix(path, prefix), "/"), "/")
}
func idFromPath(path, prefix string) int64 {
	if !strings.HasPrefix(path, prefix) {
		return 0
	}
	parts := pathParts(path, prefix)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	return id
}
func intQuery(r *http.Request, key string, fallback int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func int64Query(r *http.Request, key string, fallback int64) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
func validJSONObject(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("config_json must be a JSON object")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return fmt.Errorf("invalid config_json: %w", err)
	}
	return nil
}
func auditReq(s *Server, r *http.Request, action, target, detail string) {
	if claims, ok := r.Context().Value(claimsKey).(security.TokenClaims); ok {
		_ = s.store.AddAudit(r.Context(), model.AuditLog{ActorID: &claims.Subject, Action: action, Target: target, Detail: detail, IP: clientIP(r)})
	}
}

var _ = sql.ErrNoRows
