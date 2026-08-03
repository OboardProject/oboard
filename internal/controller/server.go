package controller

import (
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
	settingTimeCheckNTPServers            = "time_check_ntp_servers"
	settingSubscriptionAuditPolicy        = "subscription_audit_policy"
	settingTrustedProxyCIDRs              = "trusted_proxy_cidrs"
	settingNotificationServerOfflineAfter = "notification_server_offline_after_seconds"
	settingNotificationServerOnlineAfter  = "notification_server_online_after_seconds"
	settingNotificationServerMergeOffline = "notification_server_merge_offline"
	timeCheckThresholdSeconds             = 30
)

var defaultTimeCheckNTPServers = []string{"time.cloudflare.com", "time.google.com", "pool.ntp.org"}

var automaticTrustedProxyCIDRs = []string{"127.0.0.0/8", "::1/128"}

type trustedProxyState struct {
	prefixes []netip.Prefix
}

type trustedProxyStateContextKey struct{}

type Server struct {
	store                   *store.Store
	sessionSecret           string
	staticDir               string
	application             *application.Service
	capabilities            *capability.Catalog
	automation              *automation.Service
	auditIntel              *auditintel.Service
	auditReviews            *auditreview.Service
	aiModelDiscoveries      *aiModelDiscoveryQueue
	aiModelDiscoveryTimeout time.Duration
	apiGateMu               sync.Mutex
	apiInFlight             map[string]int
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
	notificationMu                sync.Mutex
	connectionAuditNotificationMu sync.Mutex
	notificationWG                sync.WaitGroup
	notificationSender            func(context.Context, model.NotificationChannel, string, string) error
	certificateIssueMu            sync.Mutex
	certificateIssues             map[int64]bool
	controllerUpdater             *controllerupdate.Client
	controllerBackupDir           string
	controllerRuntimeStatePath    string
	controllerListenAddress       string
	controllerUpdatesConfigured   bool
	controllerUpdateMu            sync.Mutex
	controllerUpdateRunMu         sync.Mutex
	controllerUpdateWatchMu       sync.Mutex
	controllerUpdateWatching      bool
	controllerLastLoginCheck      time.Time
	backupManager                 *backup.Manager
	backupConfigured              bool
	backupMu                      sync.Mutex
	backupRestart                 func()
	// deploymentMu serializes deployment preparation. Preparing a deployment
	// repairs stored topology, refreshes derived roles and allocates one
	// monotonic config version, so two concurrent applies would interleave those
	// writes and hand overlapping desired state to the same Agents.
	deploymentMu sync.Mutex
	geoIP        connectionAuditGeoResolver
	geoIPStatus  model.GeoDatabaseStatus
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
	s := &Server{store: store, sessionSecret: sessionSecret, staticDir: staticDir, basePath: basePath, application: application.NewService(store), capabilities: catalog, automation: automation.NewService(store, catalog), auditIntel: auditintel.New(store, sessionSecret), auditReviews: auditreview.New(store, sessionSecret), aiModelDiscoveries: newAIModelDiscoveryQueue(), aiModelDiscoveryTimeout: aiModelDiscoveryTimeout, apiInFlight: map[string]int{}, allowedOrigins: parseAllowedOrigins(os.Getenv("OBOARD_CORS_ORIGINS")), dnsEndpoints: defaultDNSProviderEndpoints(), acmeCommand: acmeCommand, acmeHome: acmeHome, logs: logs, realtime: newRealtimeBroker(), activeProbes: map[int64]bool{}, notificationSender: sendNotification, certificateIssues: map[int64]bool{}, controllerUpdater: controllerupdate.NewClient(socketPath), geoIPStatus: model.GeoDatabaseStatus{Provider: "ip2region", Error: "IP 归属库不可用"}}
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
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/api/v1/version", s.version)
	mux.HandleFunc("/api/v1/auth/bootstrap", s.bootstrap)
	mux.HandleFunc("/api/v1/auth/login", s.login)
	mux.HandleFunc("/api/v1/auth/totp/verify", s.verifyTOTPLogin)
	mux.HandleFunc("/api/v1/auth/passkey/login/begin", s.passkeyLoginBegin)
	mux.HandleFunc("/api/v1/auth/passkey/login/finish", s.passkeyLoginFinish)
	mux.HandleFunc("/api/v1/auth/session", s.auth(s.restoreSession, model.RoleViewer))
	mux.HandleFunc("/api/v1/auth/logout", s.auth(s.logout, model.RoleViewer))
	mux.HandleFunc("/api/v1/auth/password", s.auth(s.changePassword, model.RoleViewer))
	mux.HandleFunc("/api/v1/me", s.auth(s.me, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/authentication", s.auth(s.authenticationStatus, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/totp/setup/begin", s.auth(s.totpSetupBegin, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/totp/setup/confirm", s.auth(s.totpSetupConfirm, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/totp/disable", s.auth(s.totpDisable, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/totp/recovery-codes", s.auth(s.totpRecoveryCodes, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/passkeys/register/begin", s.auth(s.passkeyRegisterBegin, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/passkeys/register/finish", s.auth(s.passkeyRegisterFinish, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/passkeys/", s.auth(s.passkeys, model.RoleViewer))
	mux.HandleFunc("/api/v1/me/subscription-age", s.auth(s.selfSubscriptionAge, model.RoleViewer))
	mux.HandleFunc("/api/v1/page-data", s.auth(s.pageData, model.RoleViewer))
	mux.HandleFunc("/api/v1/events", s.auth(s.uiEvents, model.RoleViewer))
	mux.HandleFunc("/api/v1/dashboard/summary", s.auth(s.dashboard, model.RoleViewer))
	mux.HandleFunc("/api/v1/settings/base-path/retry", s.auth(s.settingsBasePathRetry, model.RoleAdmin))
	mux.HandleFunc("/api/v1/settings", s.auth(s.settings, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update", s.auth(s.controllerUpdate, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/check", s.auth(s.controllerUpdateCheck, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/install", s.auth(s.controllerUpdateInstall, model.RoleAdmin))
	mux.HandleFunc("/api/v1/controller-update/cancel", s.auth(s.controllerUpdateCancel, model.RoleAdmin))
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
	mux.HandleFunc("/api/v1/reality/keypair", s.auth(s.realityKeypair, model.RoleOperator))
	mux.HandleFunc("/api/v1/servers", s.auth(s.servers, model.RoleOperator))
	mux.HandleFunc("/api/v1/servers/", s.auth(s.serverSubroutes, model.RoleOperator))
	mux.HandleFunc("/api/v1/agents/update-all", s.auth(s.agentsUpdateAll, model.RoleAdmin))
	mux.HandleFunc("/api/v1/inbounds", s.auth(s.inbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/inbounds/", s.auth(s.inbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/inbound-users", s.auth(s.inboundUsers, model.RoleAdmin))
	mux.HandleFunc("/api/v1/inbound-users/", s.auth(s.inboundUsers, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-groups", s.auth(s.userGroups, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-groups/", s.auth(s.userGroups, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-group-members", s.auth(s.userGroupMembers, model.RoleAdmin))
	mux.HandleFunc("/api/v1/user-group-members/", s.auth(s.userGroupMembers, model.RoleAdmin))
	mux.HandleFunc("/api/v1/inbound-access-grants", s.auth(s.inboundAccessGrants, model.RoleAdmin))
	mux.HandleFunc("/api/v1/inbound-access-grants/", s.auth(s.inboundAccessGrants, model.RoleAdmin))
	mux.HandleFunc("/api/v1/outbounds", s.auth(s.outbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/outbounds/", s.auth(s.outbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/routing-rules", s.auth(s.routingRules, model.RoleOperator))
	mux.HandleFunc("/api/v1/routing-rules/", s.auth(s.routingRules, model.RoleOperator))
	mux.HandleFunc("/api/v1/external-outbounds", s.auth(s.externalOutbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/external-outbounds/", s.auth(s.externalOutbounds, model.RoleOperator))
	mux.HandleFunc("/api/v1/external-outbound-access-grants", s.auth(s.externalOutboundAccessGrants, model.RoleAdmin))
	mux.HandleFunc("/api/v1/external-outbound-access-grants/", s.auth(s.externalOutboundAccessGrants, model.RoleAdmin))
	mux.HandleFunc("/api/v1/proxy-paths", s.auth(s.proxyPaths, model.RoleOperator))
	mux.HandleFunc("/api/v1/proxy-paths/", s.auth(s.proxyPaths, model.RoleOperator))
	mux.HandleFunc("/api/v1/proxy-path-steps", s.auth(s.proxyPathSteps, model.RoleOperator))
	mux.HandleFunc("/api/v1/proxy-path-steps/", s.auth(s.proxyPathSteps, model.RoleOperator))
	mux.HandleFunc("/api/v1/warp-profiles", s.auth(s.warpProfiles, model.RoleOperator))
	mux.HandleFunc("/api/v1/warp-profiles/", s.auth(s.warpProfiles, model.RoleOperator))
	mux.HandleFunc("/api/v1/users", s.auth(s.users, model.RoleAdmin))
	mux.HandleFunc("/api/v1/users/", s.auth(s.users, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-profiles", s.auth(s.subscriptionProfiles, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-profiles/", s.auth(s.subscriptionProfiles, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-assignments", s.auth(s.subscriptionAssignments, model.RoleAdmin))
	mux.HandleFunc("/api/v1/subscription-assignments/", s.auth(s.subscriptionAssignments, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-lists", s.auth(s.dnsLists, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-lists/", s.auth(s.dnsLists, model.RoleAdmin))
	mux.HandleFunc("/api/v1/dns-benchmarks", s.auth(s.dnsBenchmarks, model.RoleOperator))
	mux.HandleFunc("/api/v1/mtu-detections", s.auth(s.mtuDetections, model.RoleOperator))
	mux.HandleFunc("/api/v1/port-forwards", s.auth(s.portForwards, model.RoleOperator))
	mux.HandleFunc("/api/v1/port-forwards/", s.auth(s.portForwards, model.RoleOperator))
	mux.HandleFunc("/api/v1/tunnels", s.auth(s.tunnels, model.RoleOperator))
	mux.HandleFunc("/api/v1/tunnels/", s.auth(s.tunnels, model.RoleOperator))
	mux.HandleFunc("/api/v1/notification-channels", s.auth(s.notificationChannels, model.RoleViewer))
	mux.HandleFunc("/api/v1/notification-channels/", s.auth(s.notificationChannels, model.RoleViewer))
	mux.HandleFunc("/api/v1/notification-announcements", s.auth(s.notificationAnnouncements, model.RoleAdmin))
	mux.HandleFunc("/api/v1/port-forward-probes", s.auth(s.portForwardProbes, model.RoleOperator))
	mux.HandleFunc("/api/v1/inbound-probes", s.auth(s.inboundProbes, model.RoleOperator))
	mux.HandleFunc("/api/v1/deployments/apply", s.auth(s.applyDeployment, model.RoleOperator))
	mux.HandleFunc("/api/v1/deployments/", s.auth(s.deployment, model.RoleOperator))
	mux.HandleFunc("/api/v1/agent-tasks", s.auth(s.agentTasks, model.RoleOperator))
	mux.HandleFunc("/api/v1/agent-tasks/", s.auth(s.agentTask, model.RoleOperator))
	mux.HandleFunc("/api/v1/subscriptions", notFound)
	mux.HandleFunc("/api/v1/subscriptions/", s.subscription)
	mux.HandleFunc("/api/v1/audit-logs", s.auth(s.auditLogs, model.RoleAdmin))
	mux.HandleFunc("/api/v1/audit/overview", s.auth(s.connectionAuditOverview, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/users/", s.auth(s.connectionAuditUser, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/risk-overview", s.auth(s.combinedAuditOverview, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/subscriptions/overview", s.auth(s.subscriptionAuditOverview, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/subscriptions/users/", s.auth(s.subscriptionAuditUser, model.RoleOperator))
	mux.HandleFunc("/api/v1/audit/ai-reviews", s.auth(s.auditAIReviews, model.RoleAdmin))
	mux.HandleFunc("/api/v1/audit/ai-reviews/", s.auth(s.auditAIReview, model.RoleAdmin))
	s.registerAPIV2Routes(mux)
	s.registerOAuthRoutes(mux)
	mcpHandler := s.newMCPHandler()
	mux.HandleFunc("/mcp", s.apiAuth(mcpHandler.ServeHTTP, model.RoleViewer))
	mux.HandleFunc("/api/v1/agent/enroll", s.agentEnroll)
	mux.HandleFunc("/api/v1/agent/connect", s.agentConnect)
	mux.HandleFunc("/api/v1/agent/task-results", s.agentTaskResults)
	mux.HandleFunc("/api/v1/agent/assets", s.agentManagedAssets)
	mux.HandleFunc("/api/v1/agent/certificate-issues", s.agentCertificateIssues)
	mux.HandleFunc("/api/v1/agent/traffic-reports", s.agentTrafficReports)
	mux.HandleFunc("/api/v1/agent/connection-reports", s.agentConnectionReports)
	mux.HandleFunc("/api/v1/agent/dns-benchmarks", s.agentDNSBenchmarks)
	mux.HandleFunc("/api/v1/agent/mtu-detections", s.agentMTUDetections)
	mux.HandleFunc("/api/v1/agent/port-forward-probes", s.agentPortForwardProbes)
	mux.HandleFunc("/api/v1/agent/inbound-probes", s.agentInboundProbes)
	mux.HandleFunc("/install/agent.sh", s.agentInstallScript)
	mux.HandleFunc("/install/agent-self-update.sh", s.agentSelfUpdateScript)
	mux.HandleFunc("/downloads", notFound)
	mux.HandleFunc("/downloads/", s.downloadArtifact)
	mux.HandleFunc("/", s.static)
	return s.withTrustedProxyState(s.withBasePath(s.requestLogger(s.withSecurityHeaders(s.realtimeInvalidation(s.managementAPIVersionGate(mux))))))
}

func (s *Server) managementAPIVersionGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/ui/") {
			relative := strings.TrimPrefix(r.URL.Path, "/api/v2/ui/")
			if relative == "agent" || strings.HasPrefix(relative, "agent/") || relative == "subscriptions" || strings.HasPrefix(relative, "subscriptions/") {
				http.NotFound(w, r)
				return
			}
			request := r.Clone(r.Context())
			request.URL = new(url.URL)
			*request.URL = *r.URL
			request.URL.Path = "/api/v1/" + strings.TrimPrefix(r.URL.Path, "/api/v2/ui/")
			request.URL.RawPath = ""
			next.ServeHTTP(w, request)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && !strings.HasPrefix(r.URL.Path, "/api/v1/agent/") && r.URL.Path != "/api/v1/subscriptions" && !strings.HasPrefix(r.URL.Path, "/api/v1/subscriptions/") {
			http.NotFound(w, r)
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
			http.NotFound(w, r)
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
			http.NotFound(w, r)
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

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/v1/agent/connect" || r.URL.Path == "/api/v2/ui/events" {
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
			ControllerURL               *string                        `json:"controller_url"`
			BasePath                    *string                        `json:"base_path"`
			CertificateAutoMatch        *bool                          `json:"certificate_auto_match_enabled"`
			CertificatePreference       *string                        `json:"certificate_default_preference"`
			CertificateAutoIssueCA      *string                        `json:"certificate_auto_issue_acme_ca"`
			CertificateAutoIssueEAB     *int64                         `json:"certificate_auto_issue_google_eab_credential_id"`
			SubscriptionAgePolicy       *string                        `json:"subscription_age_policy"`
			SubscriptionAuditPolicy     *model.SubscriptionAuditPolicy `json:"subscription_audit_policy"`
			TrafficTimezone             *string                        `json:"traffic_timezone"`
			TrafficEnforcementMode      *string                        `json:"traffic_enforcement_mode"`
			ControllerLogMaxMB          *int                           `json:"controller_log_max_mb"`
			ControllerLogBackups        *int                           `json:"controller_log_backups"`
			ControllerAutoUpdate        *bool                          `json:"controller_auto_update_enabled"`
			ServerDefaultMTUMode        *string                        `json:"server_default_mtu_mode"`
			ServerDefaultBBREnabled     *bool                          `json:"server_default_bbr_enabled"`
			ServerDefaultTimeCorrection *string                        `json:"server_default_time_correction_mode"`
			TimeCheckNTPServers         []string                       `json:"time_check_ntp_servers"`
			TrustedProxyCIDRs           *[]string                      `json:"trusted_proxy_cidrs"`
			NotificationOfflineAfter    *int                           `json:"notification_server_offline_after_seconds"`
			NotificationOnlineAfter     *int                           `json:"notification_server_online_after_seconds"`
			NotificationMergeOffline    *bool                          `json:"notification_server_merge_offline"`
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
		if req.SubscriptionAuditPolicy != nil {
			if err := store.ValidateSubscriptionAuditPolicy(*req.SubscriptionAuditPolicy); err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
			raw, err := json.Marshal(req.SubscriptionAuditPolicy)
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			if err := s.store.SetSetting(r.Context(), settingSubscriptionAuditPolicy, string(raw)); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			changed = append(changed, settingSubscriptionAuditPolicy)
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
		if len(changed) > 0 {
			auditReq(s, r, "update", "settings", strings.Join(changed, ","))
		}
		items, err := s.store.ListSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
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
	out := map[string]any{"certificate_auto_match_enabled": true, "certificate_default_preference": "subdomain", settingCertificateAutoIssueACMECA: "letsencrypt", settingCertificateAutoIssueGoogleEABCredential: 0, "subscription_age_policy": "optional", settingSubscriptionAuditPolicy: store.DefaultSubscriptionAuditPolicy(), "traffic_timezone": "Asia/Shanghai", "traffic_enforcement_mode": "disconnect_and_reject", "controller_log_max_mb": "32", "controller_log_backups": "5", controllerAutoUpdateSetting: false, settingServerDefaultMTUMode: string(model.MTUModeDetect), settingServerDefaultBBREnabled: false, settingServerDefaultTimeCorrection: string(model.TimeCorrectionOff), settingTimeCheckNTPServers: append([]string(nil), defaultTimeCheckNTPServers...), settingTrustedProxyCIDRs: []string{}, settingNotificationServerOfflineAfter: defaultNotificationOfflineAfterSeconds, settingNotificationServerOnlineAfter: defaultNotificationOnlineAfterSeconds, settingNotificationServerMergeOffline: true, "trusted_proxy_environment_cidrs": append([]string(nil), s.trustedProxyEnvironmentCIDRs...)}
	for key, value := range items {
		if strings.HasPrefix(key, "controller_base_path") || key == controllerBackupSetting || key == controllerUpdateErrorSetting || key == settingSubscriptionAuditPolicy || key == settingTrustedProxyCIDRs {
			continue
		}
		out[key] = value
	}
	if values, err := trustedProxyCIDRsFromSettings(items); err == nil {
		out[settingTrustedProxyCIDRs] = values
	}
	if raw := strings.TrimSpace(items[settingSubscriptionAuditPolicy]); raw != "" {
		var policy model.SubscriptionAuditPolicy
		if json.Unmarshal([]byte(raw), &policy) == nil && store.ValidateSubscriptionAuditPolicy(policy) == nil {
			out[settingSubscriptionAuditPolicy] = policy
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
	out["base_path"] = s.currentBasePath()
	if migration, err := s.basePathMigrationProgress(ctx); err == nil {
		out["base_path_migration"] = migration
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
	return mode, settingBool(settings, settingServerDefaultBBREnabled, false), normalizeControllerTimeCorrectionMode(model.TimeCorrectionMode(settings[settingServerDefaultTimeCorrection]))
}

func normalizeControllerTimeCorrectionMode(mode model.TimeCorrectionMode) model.TimeCorrectionMode {
	switch model.TimeCorrectionMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case model.TimeCorrectionAuto:
		return model.TimeCorrectionAuto
	case model.TimeCorrectionNTP:
		return model.TimeCorrectionNTP
	default:
		return model.TimeCorrectionOff
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
		Protocols:            []string{"vless", "hy2", "anytls", "shadowsocks", "mieru"},
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
	ctx := r.Context()
	addServerSnapshot := func() error {
		items, err := s.store.ListServers(ctx)
		if err != nil {
			return err
		}
		out["servers"] = items
		return nil
	}
	addServers := func() error {
		s.checkOffline(ctx)
		return addServerSnapshot()
	}
	addSettings := func() error {
		items, err := s.store.ListSettings(ctx)
		if err != nil {
			return err
		}
		out["settings"] = s.publicSettings(ctx, items)
		out["reverse_proxy_status"] = s.reverseProxyStatus(r)
		return nil
	}
	addServerCreationDefaults := func() error {
		settings, err := s.store.ListSettings(ctx)
		if err != nil {
			return err
		}
		mtuMode, bbrEnabled, timeMode := serverCreationDefaults(settings)
		out["server_creation_defaults"] = map[string]any{"mtu_mode": mtuMode, "bbr_enabled": bbrEnabled, "time_correction_mode": timeMode}
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
		groups, err := s.store.ListUserGroups(ctx)
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
	addProxy := func() error {
		if err := addServers(); err != nil {
			return err
		}
		if err := s.store.PruneOrphanedProxyPathSteps(ctx); err != nil {
			return err
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			return err
		}
		inbounds, err := s.store.ListInbounds(ctx)
		if err != nil {
			return err
		}
		outbounds, err := s.store.ListOutbounds(ctx)
		if err != nil {
			return err
		}
		rules, err := s.store.ListRoutingRules(ctx)
		if err != nil {
			return err
		}
		externals, err := s.store.ListExternalOutbounds(ctx)
		if err != nil {
			return err
		}
		if err := s.normalizeEnabledProxyPathProcessingRoles(ctx); err != nil {
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
		paths = core.ResolveProxyPathNames(paths, steps, out["servers"].([]model.Server), inbounds, externals)
		egressResults, err := s.store.ListProxyPathEgressResults(ctx)
		if err != nil {
			return err
		}
		paths, externals = core.ResolveProxyPathExitRegions(paths, steps, out["servers"].([]model.Server), inbounds, externals, egressResults)
		forwards, err := s.store.ListPortForwards(ctx)
		if err != nil {
			return err
		}
		tunnels, err := s.store.ListTunnels(ctx)
		if err != nil {
			return err
		}
		warps, err := s.store.ListWARPProfiles(ctx)
		if err != nil {
			return err
		}
		dnsLists, err := s.store.ListDNSLists(ctx, false)
		if err != nil {
			return err
		}
		dnsPolicies, err := s.store.ListServerDNSPolicies(ctx)
		if err != nil {
			return err
		}
		inboundProbes, err := s.store.ListInboundProbeResults(ctx, 0, 0, 200)
		if err != nil {
			return err
		}
		forwardProbes, err := s.store.ListPortForwardProbeResults(ctx, 0, 0, 200)
		if err != nil {
			return err
		}
		out["inbounds"] = inbounds
		out["outbounds"] = outbounds
		out["routing_rules"] = rules
		out["external_outbounds"] = externals
		out["proxy_paths"] = paths
		out["proxy_path_steps"] = publicProxyPathSteps(steps)
		out["port_forwards"] = forwards
		out["tunnels"] = publicTunnels(tunnels)
		out["warp_profiles"] = publicWARPProfiles(warps)
		out["dns_lists"] = dnsLists
		out["server_dns_policies"] = dnsPolicies
		out["inbound_probes"] = inboundProbes
		out["port_forward_probes"] = forwardProbes
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
			inboundUsers, err := s.store.ListInboundUsers(ctx)
			if err != nil {
				return err
			}
			grants, err := s.store.ListInboundAccessGrants(ctx)
			if err != nil {
				return err
			}
			groups, err := s.store.ListUserGroups(ctx)
			if err != nil {
				return err
			}
			members, err := s.store.ListUserGroupMembers(ctx)
			if err != nil {
				return err
			}
			externalGrants, err := s.store.ListExternalOutboundAccessGrants(ctx)
			if err != nil {
				return err
			}
			settings, err := s.store.ListSettings(ctx)
			if err != nil {
				return err
			}
			out["users"] = users
			out["inbound_users"] = inboundUsers
			out["inbound_access_grants"] = grants
			out["user_groups"] = groups
			out["user_group_members"] = members
			out["external_outbound_access_grants"] = externalGrants
			out["settings"] = s.publicSettings(ctx, settings)
			out["reverse_proxy_status"] = s.reverseProxyStatus(r)
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
			inboundUsers, err := s.store.ListInboundUsers(ctx)
			if err != nil {
				return err
			}
			grants, err := s.store.ListInboundAccessGrants(ctx)
			if err != nil {
				return err
			}
			out["users"] = users
			out["inbound_users"] = inboundUsers
			out["inbound_access_grants"] = grants
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
			groups, err := s.store.ListUserGroups(ctx)
			if err != nil {
				return err
			}
			grants, err := s.store.ListExternalOutboundAccessGrants(ctx)
			if err != nil {
				return err
			}
			out["users"] = users
			out["user_groups"] = groups
			out["external_outbound_access_grants"] = grants
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
		if err = require(model.RoleOperator); err == nil {
			s.checkOffline(ctx)
			s.expireTimedOutTasks(ctx)
			var summary any
			summary, err = s.store.Dashboard(ctx)
			out["summary"] = summary
		}
		if err == nil {
			err = addServerSnapshot()
		}
		if err == nil {
			var inbounds []model.Inbound
			inbounds, err = s.store.ListInbounds(ctx)
			out["inbounds"] = inbounds
		}
		if err == nil {
			var tasks []model.AgentTask
			tasks, err = s.store.ListTasks(ctx, 20)
			out["agent_tasks"] = sanitizeTasksForRole(tasks, role)
		}
		if err == nil {
			var overview model.ConnectionAuditOverview
			overview, err = s.store.ConnectionAuditOverview(ctx, 24)
			if err == nil {
				out["connection_audit"] = map[string]any{
					"window_hours":        overview.WindowHours,
					"elevated_risk_count": overview.ElevatedRiskCount,
				}
			}
		}
		if err == nil {
			err = addSettings()
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
	case "subscriptions":
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
			// Entry servers + proxy paths power the subscription assignment UI.
			err = addProxy()
		}
		if err == nil {
			var profiles []model.SubscriptionProfile
			profiles, err = s.store.ListSubscriptionProfiles(ctx)
			if err == nil {
				out["subscription_profiles"] = profiles
			}
		}
		if err == nil {
			var assignments []model.SubscriptionAssignment
			assignments, err = s.store.ListSubscriptionAssignments(ctx)
			if err == nil {
				out["subscription_assignments"] = assignments
			}
		}
	case "notifications":
		if user := currentUser(r); user != nil {
			var channels []model.NotificationChannel
			channels, err = s.store.ListNotificationChannelsByOwner(ctx, user.ID)
			if err == nil {
				out["notification_channels"] = publicNotificationChannels(channels)
				out["notification_config"] = notificationPageConfig(role)
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
			s.expireTimedOutTasks(ctx)
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
		if err == nil {
			var connectionOverview model.ConnectionAuditOverview
			var subscriptionOverview model.SubscriptionAuditOverview
			var combinedOverview model.CombinedAuditOverview
			connectionOverview, subscriptionOverview, combinedOverview, err = s.auditOverviewData(ctx, intQuery(r, "window_hours", 24))
			if err == nil {
				out["connection_audit"] = connectionOverview
				out["subscription_audit"] = subscriptionOverview
				out["audit_risk"] = combinedOverview
			}
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
					servers := make(map[int64]model.Server, len(config.Servers))
					for _, server := range config.Servers {
						servers[server.ID] = server
					}
					allowed := make(map[int64]bool)
					for _, binding := range effectiveInboundUsersForRouting(config) {
						if binding.Enabled && binding.UserID == user.ID {
							allowed[binding.InboundID] = true
						}
					}
					accesses := make([]map[string]any, 0)
					for _, inbound := range config.Inbounds {
						server, ok := servers[inbound.ServerID]
						if !ok || !allowed[inbound.ID] || !inbound.Enabled || inbound.Protocol != model.ProtocolSSH {
							continue
						}
						address := strings.TrimSpace(core.ResolveEntryAddress(inbound, server))
						if address == "" {
							continue
						}
						accesses = append(accesses, map[string]any{"inbound_id": inbound.ID, "name": inbound.Name, "address": address, "port": inbound.Port, "username": sshLoginName(user.ID)})
					}
					out["ssh_accesses"] = accesses
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
			out["dns_credentials"], err = s.store.ListDNSCredentials(ctx)
		}
		if err == nil {
			out["certificates"], err = s.store.ListCertificates(ctx)
		}
		if err == nil {
			out["google_eab_credentials"], err = s.store.ListGoogleEABCredentials(ctx)
		}
		if err == nil {
			err = addServers()
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
	}
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
	if req.Username == "" || len(req.Password) < 10 {
		fail(w, errors.New("username and password >= 10 chars required"), 400)
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
	if len(req.NewPassword) < 10 {
		fail(w, errors.New("new password must be at least 10 characters"), 400)
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
	return map[string]any{
		"id":                          user.ID,
		"username":                    user.Username,
		"nickname":                    user.Nickname,
		"role":                        role,
		"status":                      user.Status,
		"protected":                   protected,
		"subscription_token":          user.SubscriptionToken,
		"subscription_age_enabled":    user.SubscriptionAgeEnabled,
		"subscription_age_public_key": user.SubscriptionAgePublicKey,
		"subscription_age_policy":     normalizeSubscriptionAgePolicy(settings[settingSubscriptionAgePolicy]),
		"subscription_suspended":      user.SubscriptionSuspended,
		"subscription_suspended_at":   user.SubscriptionSuspendedAt,
		"subscription_suspend_reason": user.SubscriptionSuspendReason,
		"totp_enabled":                authentication.TOTPEnabled,
		"passkey_count":               len(passkeys),
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

func roleAllows(got, min model.Role) bool {
	rank := map[model.Role]int{model.RoleViewer: 1, model.RoleOperator: 2, model.RoleAdmin: 3}
	return rank[got] >= rank[min]
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	s.checkOffline(r.Context())
	s.expireTimedOutTasks(r.Context())
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
		s.checkOffline(r.Context())
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
			MTUMode              *model.MTUMode            `json:"mtu_mode"`
			BBREnabled           *bool                     `json:"bbr_enabled"`
			TimeCorrectionMode   *model.TimeCorrectionMode `json:"time_correction_mode"`
			OfflineNotifyEnabled *bool                     `json:"offline_notify_enabled"`
			OfflineAfterSeconds  *int                      `json:"offline_after_seconds"`
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
		if input.OfflineNotifyEnabled == nil {
			v.OfflineNotifyEnabled = true
		} else {
			v.OfflineNotifyEnabled = *input.OfflineNotifyEnabled
		}
		if input.OfflineAfterSeconds != nil {
			v.OfflineAfterSeconds = *input.OfflineAfterSeconds
		}
		if err := validateServer(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if v.Status == "" {
			v.Status = model.ServerUnknown
		}
		if err := s.store.CreateServer(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
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
	if len(parts) == 2 && parts[1] == "diagnose" {
		s.serverDiagnose(w, r, id)
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
			MTUMode              *model.MTUMode            `json:"mtu_mode"`
			BBREnabled           *bool                     `json:"bbr_enabled"`
			TimeCorrectionMode   *model.TimeCorrectionMode `json:"time_correction_mode"`
			OfflineNotifyEnabled *bool                     `json:"offline_notify_enabled"`
			OfflineAfterSeconds  *int                      `json:"offline_after_seconds"`
		}
		if !decode(w, r, &input) {
			return
		}
		current, err := s.store.GetServer(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v := input.Server
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
		// Automatic region is Agent telemetry. Panel edits may select auto or a
		// manual region, but cannot replace the last detected value.
		v.DetectedRegionCode = current.DetectedRegionCode
		if err := validateServer(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateServer(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
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
		if err := s.store.CleanupRoutingForServer(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteServerTelemetry(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteInboundAccessGrantsForServer(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if inbounds, err := s.store.ListInbounds(r.Context()); err != nil {
			fail(w, err, 500)
			return
		} else {
			for _, inbound := range inbounds {
				if inbound.ServerID == id {
					if err := s.deleteDNSInboundRecords(r.Context(), inbound); err != nil {
						fail(w, err, 502)
						return
					}
					if err := s.store.DeleteInboundUsersForInbound(r.Context(), inbound.ID); err != nil {
						fail(w, err, 500)
						return
					}
					if err := s.store.DeleteInboundAccessGrantsForInbound(r.Context(), inbound.ID); err != nil {
						fail(w, err, 500)
						return
					}
					if err := s.store.DeleteProxyPathsForInbound(r.Context(), inbound.ID); err != nil {
						fail(w, err, 500)
						return
					}
					if err := s.store.DeleteInboundProbeResults(r.Context(), inbound.ID); err != nil {
						fail(w, err, 500)
						return
					}
					if err := s.store.Delete(r.Context(), "inbounds", inbound.ID); err != nil {
						fail(w, err, 500)
						return
					}
				}
			}
		}
		if err := s.store.Delete(r.Context(), "servers", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "server", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
		return
	}
	method(w)
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
	s.expireTimedOutTasks(r.Context())
	items, err := s.store.ListTasksByServer(r.Context(), id, intQuery(r, "limit", 100))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"tasks": sanitizeTasksForRole(items, currentRole(r))})
}

const (
	agentBuildMinDiagnosticsTask = "20260706000116"
	agentBuildMinTrustedForward  = "20260729000000"
	// Pending and running tasks both expire after 5 minutes so the panel does
	// not keep "waiting" forever for dead Agents or stuck executions.
	agentTaskPendingTimeout = 5 * time.Minute
	agentTaskRunningTimeout = 5 * time.Minute
)

func (s *Server) expireTimedOutTasks(ctx context.Context) {
	now := time.Now()
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

func (s *Server) createAgentTask(ctx context.Context, serverID int64, taskType, payloadJSON string, configVersion int64) (model.AgentTask, error) {
	if configVersion == 0 {
		configVersion = time.Now().Unix()
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
	if reason := agentTaskImmediateFailure(server); reason != "" {
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
	if err := s.store.CreateTask(ctx, &task); err != nil {
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
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "update", "agent", fmt.Sprint(id))
	write(w, 202, map[string]any{"task": task, "existing": existing})
}

// agentsUpdateAll queues update_agent tasks for every enrolled server.
// Offline/unenrolled servers are still recorded (createAgentTask fails them immediately)
// so operators can see results in the task center.
func (s *Server) agentsUpdateAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
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
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	versionStamp := time.Now().Unix()
	type itemResult struct {
		ServerID   int64  `json:"server_id"`
		ServerName string `json:"server_name"`
		Status     string `json:"status"` // created | existing | skipped | failed
		TaskID     int64  `json:"task_id,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	results := make([]itemResult, 0, len(servers))
	summary := map[string]int{"total": 0, "created": 0, "existing": 0, "skipped": 0, "failed": 0}
	tasks := make([]model.AgentTask, 0)

	for _, server := range servers {
		if strings.TrimSpace(server.AgentID) == "" {
			summary["skipped"]++
			summary["total"]++
			results = append(results, itemResult{ServerID: server.ID, ServerName: server.Name, Status: "skipped", Error: "Agent 未接入"})
			continue
		}
		summary["total"]++
		// Use shared version stamp so task center batches bulk updates together.
		task, existing, err := s.enqueueAgentUpdateWithVersion(r.Context(), &server, req, versionStamp)
		if err != nil {
			summary["failed"]++
			results = append(results, itemResult{ServerID: server.ID, ServerName: server.Name, Status: "failed", Error: err.Error()})
			continue
		}
		if existing {
			summary["existing"]++
			results = append(results, itemResult{ServerID: server.ID, ServerName: server.Name, Status: "existing", TaskID: task.ID})
		} else if task.Status == "failed" {
			summary["failed"]++
			results = append(results, itemResult{ServerID: server.ID, ServerName: server.Name, Status: "failed", TaskID: task.ID, Error: taskResultMessage(task)})
		} else {
			summary["created"]++
			results = append(results, itemResult{ServerID: server.ID, ServerName: server.Name, Status: "created", TaskID: task.ID})
		}
		tasks = append(tasks, task)
	}
	auditReq(s, r, "update", "agent", fmt.Sprintf("all:%d", summary["created"]+summary["existing"]))
	write(w, 202, map[string]any{
		"summary":        summary,
		"results":        results,
		"tasks":          tasks,
		"config_version": versionStamp,
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
	_ = s.store.FailStaleActiveTasksByServerType(ctx, server.ID, model.AgentTaskTypeUpdateAgent, time.Now().Add(-10*time.Minute), `{"message":"更新任务超时，已允许重新创建"}`)
	if active, err := s.store.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeUpdateAgent); err == nil {
		return *active, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.AgentTask{}, false, err
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
	task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeUpdateAgent, model.UpdateAgentTaskPayload{
		ControllerURL: controllerURL,
		ExpectedBuild: version.AgentBuild,
		Source:        source,
		GitHubRepo:    repo,
	}, configVersion)
	if err != nil {
		return model.AgentTask{}, false, err
	}
	return task, false, nil
}

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
	targets := []model.DiagnosticTarget{}
	for _, inbound := range inbounds {
		if inbound.ServerID != server.ID || !inbound.Enabled {
			continue
		}
		host := core.ResolveEntryAddress(inbound, *server)
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
	for i := range plan.PortForwards {
		plan.PortForwards[i].TrustedForward = nil
	}
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
	if v.OfflineAfterSeconds < 0 || v.OfflineAfterSeconds > 86400 {
		return errors.New("offline_after_seconds must be between 0 and 86400")
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
		v.PortRangeStart = 10000
	}
	if v.PortRangeEnd == 0 {
		v.PortRangeEnd = 20000
	}
	switch strings.ToLower(strings.TrimSpace(v.MonitoringMode)) {
	case "", "lightweight":
		v.MonitoringMode = "lightweight"
	case "standard":
		v.MonitoringMode = "standard"
	default:
		return errors.New("monitoring_mode must be lightweight or standard")
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
	return core.ValidatePortRange(v.PortRangeStart, v.PortRangeEnd)
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
	if v.EntryIPMode == model.EntryIPModeCustom && strings.TrimSpace(v.EntryAddress) == "" {
		return errors.New("custom entry address required")
	}
	v.EntryAddress = strings.TrimSpace(v.EntryAddress)
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
	encrypted, err := s.store.GetDNSList(r.Context(), policy.EncryptedListID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	bootstrap, err := s.store.GetDNSList(r.Context(), policy.BootstrapListID)
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
	plan, err := core.DNSBenchmarkPlanForPolicy(version, *policy, *encrypted, *bootstrap, model.DNSAutoTestAlways, requestID)
	if err != nil {
		fail(w, err, 400)
		return
	}
	run := model.DNSBenchmarkRun{
		RequestID: requestID, ServerID: server.ID, PolicyRevision: policy.Revision,
		EncryptedListID: encrypted.ID, EncryptedListRevision: encrypted.Revision,
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
		if policy.EncryptedListID == 0 || policy.BootstrapListID == 0 {
			fail(w, errors.New("encrypted_list_id and bootstrap_list_id are required"), 400)
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

const enrollmentTokenTTL = 30 * time.Minute

func (s *Server) enrollToken(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !roleAllows(currentRole(r), model.RoleAdmin) {
		fail(w, errors.New("admin role required for Agent enrollment"), 403)
		return
	}
	srv, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	token, err := security.RandomToken(32)
	if err != nil {
		fail(w, err, 500)
		return
	}
	expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
	if err := s.store.SetServerEnrollmentHash(r.Context(), srv.ID, security.HashSecret(token), expiresAt); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "create", "enroll-token", fmt.Sprint(id))
	write(w, 200, map[string]any{"enrollment_token": token, "expires_at": expiresAt, "expires_in_seconds": int(enrollmentTokenTTL.Seconds())})
}

func (s *Server) inbounds(w http.ResponseWriter, r *http.Request) {
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
			write(w, 200, map[string]any{"inbound": item})
			return
		}
		items, err := s.store.ListInbounds(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
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
		if err := s.store.CreateInbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.saveInboundCertificateBinding(r.Context(), v); err != nil {
			_ = s.store.Delete(r.Context(), "inbounds", v.ID)
			fail(w, err, 500)
			return
		}
		if actor := currentUser(r); actor != nil {
			binding := model.InboundUser{InboundID: v.ID, UserID: actor.ID, Enabled: true}
			if err := s.store.CreateInboundUser(r.Context(), &binding); err != nil {
				_ = s.store.Delete(r.Context(), "inbounds", v.ID)
				fail(w, err, 500)
				return
			}
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
		if v.ConfigJSON == "" {
			v.ConfigJSON = "{}"
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
		if err := s.ensureInboundUserCapacity(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			replaceInbound(data, v)
		}); err != nil {
			fail(w, err, 400)
			return
		}
		oldDomain := normalizeDomainName(current.DNSDomain)
		newDomain := normalizeDomainName(v.DNSDomain)
		if current.DNSSyncEnabled && (!v.DNSSyncEnabled || oldDomain != newDomain) {
			if err := s.deleteDNSInboundRecords(r.Context(), *current); err != nil {
				fail(w, err, 502)
				return
			}
		}
		if err := s.store.UpdateInbound(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.saveInboundCertificateBinding(r.Context(), v); err != nil {
			fail(w, err, 500)
			return
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
		if err := s.deleteDNSInboundRecords(r.Context(), *inbound); err != nil {
			fail(w, err, 502)
			return
		}
		if err := s.store.DeleteInboundUsersForInbound(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteInboundAccessGrantsForInbound(r.Context(), id); err != nil {
			fail(w, err, 500)
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
	return nil
}

func (s *Server) inboundUsers(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/inbound-users/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetInboundUser(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"inbound_user": item})
			return
		}
		items, err := s.store.ListInboundUsers(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"inbound_users": items})
	case http.MethodPost:
		var v model.InboundUser
		if !decode(w, r, &v) {
			return
		}
		if err := s.validateInboundUserBinding(r.Context(), v, 0); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			upsertInboundUser(data, v)
		}); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateInboundUser(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "grant", "inbound-user", fmt.Sprintf("%d:%d", v.InboundID, v.UserID))
		write(w, 201, map[string]any{"inbound_user": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetInboundUser(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if err := s.validateInboundUserBinding(r.Context(), v, id); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			replaceInboundUser(data, v)
		}); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateInboundUser(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"inbound_user": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteInboundUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "revoke", "inbound-user", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) userGroups(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/user-groups/")
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
		if err := validateUserGroup(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			data.Groups = append(data.Groups, v)
		}); err != nil {
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
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		mergeUserGroupPatch(&v, current)
		v.SystemKey = current.SystemKey
		if current.SystemKey == store.UserGroupSystemAdmins {
			v.Role = model.RoleAdmin
			v.Enabled = true
		}
		if err := validateUserGroup(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			replaceUserGroup(data, v)
		}); err != nil {
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
		if current.SystemKey != "" {
			fail(w, errors.New("系统用户组不允许删除"), 400)
			return
		}
		if err := s.store.DeleteUserGroupMembersForGroup(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteInboundAccessGrantsForGroup(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteExternalOutboundAccessGrantsForGroup(r.Context(), id); err != nil {
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
		if err := s.validateUserGroupMember(r.Context(), v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			upsertUserGroupMember(data, v)
		}); err != nil {
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
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
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
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			replaceUserGroupMember(data, v)
		}); err != nil {
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

func (s *Server) inboundAccessGrants(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/inbound-access-grants/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetInboundAccessGrant(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"inbound_access_grant": item})
			return
		}
		items, err := s.store.ListInboundAccessGrants(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"inbound_access_grants": items})
	case http.MethodPost:
		var v model.InboundAccessGrant
		if !decode(w, r, &v) {
			return
		}
		if err := s.validateInboundAccessGrant(r.Context(), &v, 0); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			data.Grants = append(data.Grants, v)
		}); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateInboundAccessGrant(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "grant", "inbound-access", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"inbound_access_grant": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetInboundAccessGrant(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if err := s.validateInboundAccessGrant(r.Context(), &v, id); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			replaceInboundAccessGrant(data, v)
		}); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateInboundAccessGrant(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "inbound-access", fmt.Sprint(id))
		write(w, 200, map[string]any{"inbound_access_grant": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.Delete(r.Context(), "inbound_access_grants", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "revoke", "inbound-access", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) ensureInboundUserCapacity(ctx context.Context, inbound model.Inbound) error {
	if core.InboundSupportsMultipleUsers(inbound) {
		return nil
	}
	bindings, err := s.store.ListInboundUsersForInbound(ctx, inbound.ID)
	if err != nil {
		return err
	}
	active := 0
	for _, binding := range bindings {
		if binding.Enabled {
			active++
		}
	}
	if active > 1 {
		return errors.New("this inbound type supports only one user")
	}
	return nil
}

type accessData struct {
	Inbounds     []model.Inbound
	Users        []model.User
	InboundUsers []model.InboundUser
	Groups       []model.UserGroup
	Members      []model.UserGroupMember
	Grants       []model.InboundAccessGrant
}

func (s *Server) loadAccessData(ctx context.Context) (*accessData, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	inboundUsers, err := s.store.ListInboundUsers(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.store.ListUserGroups(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.store.ListUserGroupMembers(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := s.store.ListInboundAccessGrants(ctx)
	if err != nil {
		return nil, err
	}
	return &accessData{Inbounds: inbounds, Users: users, InboundUsers: inboundUsers, Groups: groups, Members: members, Grants: grants}, nil
}

func (s *Server) validateAccessCapacityWith(ctx context.Context, mutate func(*accessData)) error {
	data, err := s.loadAccessData(ctx)
	if err != nil {
		return err
	}
	if mutate != nil {
		mutate(data)
	}
	return core.ValidateInboundAccessCapacity(data.Inbounds, data.Users, data.InboundUsers, data.Groups, data.Members, data.Grants)
}

func effectiveInboundUsersForRouting(data store.FullRoutingConfig) []model.InboundUser {
	return core.EffectiveInboundUsers(data.Inbounds, data.Users, data.InboundUsers, data.UserGroups, data.UserGroupMembers, data.InboundAccessGrants)
}

func (s *Server) trafficRuntimePolicies(ctx context.Context, serverID int64, users []model.User, groups []model.UserGroup, members []model.UserGroupMember, accountingUsers map[int64]bool) (map[int64]model.TrafficRuntimePolicy, error) {
	settings, _ := s.store.ListSettings(ctx)
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
		limit := core.EffectiveUserLimitPolicy(user, groups, members)
		periodKey, start, end := trafficWindow(time.Now(), limit.TrafficResetMode, limit.TrafficResetDay, loc)
		period, err := s.store.EnsureTrafficPeriod(ctx, user.ID, periodKey, start, end, limit.TrafficLimitBytes)
		if err != nil {
			return nil, err
		}
		used := period.Upload + period.Download
		lease, err := s.store.EnsureTrafficLeaseAllocation(ctx, serverID, user.ID, periodKey, limit.TrafficLimitBytes, used)
		if err != nil {
			return nil, err
		}
		policies[user.ID] = model.TrafficRuntimePolicy{UserID: user.ID, Billable: true, SpeedLimitMbps: limit.SpeedLimitMbps, TrafficLimitBytes: limit.TrafficLimitBytes, UsedBaselineBytes: used, LeaseBytes: lease.RemainingBytes, ResetLeaseBytes: lease.ResetBytes, LeaseEnforced: limit.TrafficLimitBytes > 0, PeriodKey: periodKey, PeriodStart: start.UTC().Format(time.RFC3339Nano), PeriodEnd: end.UTC().Format(time.RFC3339Nano), ResetMode: limit.TrafficResetMode, ResetDay: limit.TrafficResetDay, Timezone: tz, QuotaState: period.State, EnforcementMode: enforcement}
	}
	_ = serverID
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
	if v.SpeedLimitMbps < 0 || v.TrafficLimitBytes < 0 {
		return errors.New("user group limits must be >= 0")
	}
	v.TrafficResetMode = normalizeControllerTrafficResetMode(v.TrafficResetMode)
	v.TrafficResetDay = normalizeControllerTrafficResetDay(v.TrafficResetDay)
	return nil
}

func mergeUserGroupPatch(v *model.UserGroup, current *model.UserGroup) {
	if v.Name == "" {
		v.Name = current.Name
	}
	if v.Role == "" {
		v.Role = current.Role
	}
	if v.TrafficResetMode == "" {
		v.TrafficResetMode = current.TrafficResetMode
	}
	if v.TrafficResetDay == 0 {
		v.TrafficResetDay = current.TrafficResetDay
	}
}

func normalizeControllerTrafficResetMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "month_day", "day", "custom_day":
		return "month_day"
	default:
		return "monthly"
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

func trafficWindow(now time.Time, mode string, day int, loc *time.Location) (string, time.Time, time.Time) {
	if loc == nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	n := now.In(loc)
	mode = normalizeControllerTrafficResetMode(mode)
	day = normalizeControllerTrafficResetDay(day)
	if mode == "monthly" {
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

func trafficWindowForPeriodKey(now time.Time, periodKey, mode string, day int, loc *time.Location) (string, time.Time, time.Time, error) {
	periodKey = strings.TrimSpace(periodKey)
	if periodKey == "" {
		key, start, end := trafficWindow(now, mode, day, loc)
		return key, start, end, nil
	}
	if loc == nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	parsed, err := time.ParseInLocation("2006-01-02", periodKey, loc)
	if err != nil {
		return "", time.Time{}, time.Time{}, errors.New("traffic period_key must use YYYY-MM-DD")
	}
	key, start, end := trafficWindow(parsed.Add(12*time.Hour), mode, day, loc)
	if key != periodKey {
		return "", time.Time{}, time.Time{}, errors.New("traffic period_key does not match the user reset cycle")
	}
	return key, start, end, nil
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

func (s *Server) validateInboundAccessGrant(ctx context.Context, v *model.InboundAccessGrant, currentID int64) error {
	switch v.SubjectType {
	case model.AccessSubjectUser:
		if _, err := s.store.GetUser(ctx, v.SubjectID); err != nil {
			return fmt.Errorf("subject_id: %w", err)
		}
	case model.AccessSubjectGroup:
		if _, err := s.store.GetUserGroup(ctx, v.SubjectID); err != nil {
			return fmt.Errorf("subject_id: %w", err)
		}
	default:
		return errors.New("subject_type must be user or group")
	}
	switch v.ScopeType {
	case model.AccessScopeGlobal:
		v.ServerID = nil
		v.InboundID = nil
	case model.AccessScopeServer:
		if v.ServerID == nil || *v.ServerID == 0 {
			return errors.New("server_id required for server scope")
		}
		if _, err := s.store.GetServer(ctx, *v.ServerID); err != nil {
			return fmt.Errorf("server_id: %w", err)
		}
		v.InboundID = nil
	case model.AccessScopeInbound:
		if v.InboundID == nil || *v.InboundID == 0 {
			return errors.New("inbound_id required for inbound scope")
		}
		if _, err := s.store.GetInbound(ctx, *v.InboundID); err != nil {
			return fmt.Errorf("inbound_id: %w", err)
		}
		v.ServerID = nil
	default:
		return errors.New("scope_type must be global, server or inbound")
	}
	items, err := s.store.ListInboundAccessGrants(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == currentID {
			continue
		}
		if sameInboundAccessGrant(item, *v) {
			return errors.New("same access grant already exists")
		}
	}
	return nil
}

func sameInboundAccessGrant(a, b model.InboundAccessGrant) bool {
	return a.SubjectType == b.SubjectType && a.SubjectID == b.SubjectID && a.ScopeType == b.ScopeType && ptrInt64Equal(a.ServerID, b.ServerID) && ptrInt64Equal(a.InboundID, b.InboundID)
}

func ptrInt64Equal(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func upsertInboundUser(data *accessData, v model.InboundUser) {
	for i := range data.InboundUsers {
		if data.InboundUsers[i].InboundID == v.InboundID && data.InboundUsers[i].UserID == v.UserID {
			v.ID = data.InboundUsers[i].ID
			data.InboundUsers[i] = v
			return
		}
	}
	data.InboundUsers = append(data.InboundUsers, v)
}

func replaceInboundUser(data *accessData, v model.InboundUser) {
	for i := range data.InboundUsers {
		if data.InboundUsers[i].ID == v.ID {
			data.InboundUsers[i] = v
			return
		}
	}
	data.InboundUsers = append(data.InboundUsers, v)
}

func replaceUser(data *accessData, v model.User) {
	for i := range data.Users {
		if data.Users[i].ID == v.ID {
			data.Users[i] = v
			return
		}
	}
	data.Users = append(data.Users, v)
}

func replaceInbound(data *accessData, v model.Inbound) {
	for i := range data.Inbounds {
		if data.Inbounds[i].ID == v.ID {
			data.Inbounds[i] = v
			return
		}
	}
	data.Inbounds = append(data.Inbounds, v)
}

func replaceUserGroup(data *accessData, v model.UserGroup) {
	for i := range data.Groups {
		if data.Groups[i].ID == v.ID {
			data.Groups[i] = v
			return
		}
	}
	data.Groups = append(data.Groups, v)
}

func upsertUserGroupMember(data *accessData, v model.UserGroupMember) {
	for i := range data.Members {
		if data.Members[i].GroupID == v.GroupID && data.Members[i].UserID == v.UserID {
			v.ID = data.Members[i].ID
			data.Members[i] = v
			return
		}
	}
	data.Members = append(data.Members, v)
}

func replaceUserGroupMember(data *accessData, v model.UserGroupMember) {
	for i := range data.Members {
		if data.Members[i].ID == v.ID {
			data.Members[i] = v
			return
		}
	}
	data.Members = append(data.Members, v)
}

func replaceInboundAccessGrant(data *accessData, v model.InboundAccessGrant) {
	for i := range data.Grants {
		if data.Grants[i].ID == v.ID {
			data.Grants[i] = v
			return
		}
	}
	data.Grants = append(data.Grants, v)
}

func (s *Server) validateInboundUserBinding(ctx context.Context, v model.InboundUser, currentID int64) error {
	if v.InboundID == 0 || v.UserID == 0 {
		return errors.New("inbound_id and user_id required")
	}
	inbound, err := s.store.GetInbound(ctx, v.InboundID)
	if err != nil {
		return err
	}
	if _, err := s.store.GetUser(ctx, v.UserID); err != nil {
		return err
	}
	if !v.Enabled {
		return nil
	}
	if core.InboundSupportsMultipleUsers(*inbound) {
		return nil
	}
	existing, err := s.store.ListInboundUsersForInbound(ctx, v.InboundID)
	if err != nil {
		return err
	}
	for _, item := range existing {
		if item.ID != currentID && item.Enabled && item.UserID != v.UserID {
			return errors.New("this inbound type supports only one user")
		}
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

func validateInbound(v model.Inbound) error {
	if v.ServerID == 0 {
		return errors.New("server_id required")
	}
	if v.Name == "" {
		return errors.New("name required")
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
	switch v.CertificateMode {
	case model.CertificateModeExternal:
	case model.CertificateModeAuto, model.CertificateModeExact, model.CertificateModeWildcard:
		if !isDNSDomainName(v.CertificateDomain) {
			return errors.New("托管证书需要有效的 SNI 域名")
		}
	case model.CertificateModeExplicit:
		if !isDNSDomainName(v.CertificateDomain) {
			return errors.New("指定证书需要有效的 SNI 域名")
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
		if err := s.validateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
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
		v.ID = id
		if err := s.validateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateRoutingRule(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"routing_rule": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.Delete(r.Context(), "routing_rules", id); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) validateRoutingRule(ctx context.Context, v *model.RoutingRule) error {
	if v.ServerID == 0 {
		return errors.New("server_id required")
	}
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("name required")
	}
	if v.Priority == 0 {
		v.Priority = 100
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
	default:
		return fmt.Errorf("unsupported action %q", v.Action)
	}
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
		if err := s.store.DeleteExternalOutboundAccessGrantsForExternal(r.Context(), id); err != nil {
			fail(w, err, 500)
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
	case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolMieru, model.ProtocolSocks:
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

func (s *Server) externalOutboundAccessGrants(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/external-outbound-access-grants/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetExternalOutboundAccessGrant(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"external_outbound_access_grant": item})
			return
		}
		items, err := s.store.ListExternalOutboundAccessGrants(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"external_outbound_access_grants": items})
	case http.MethodPost:
		var v model.ExternalOutboundAccessGrant
		if !decode(w, r, &v) {
			return
		}
		if err := s.validateExternalOutboundAccessGrant(r.Context(), &v, 0); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateExternalOutboundAccessGrant(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "grant", "external-outbound-access", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"external_outbound_access_grant": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetExternalOutboundAccessGrant(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		v := *current
		if !decode(w, r, &v) {
			return
		}
		v.ID = id
		if err := s.validateExternalOutboundAccessGrant(r.Context(), &v, id); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateExternalOutboundAccessGrant(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "external-outbound-access", fmt.Sprint(id))
		write(w, 200, map[string]any{"external_outbound_access_grant": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.Delete(r.Context(), "external_outbound_access_grants", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "revoke", "external-outbound-access", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) validateExternalOutboundAccessGrant(ctx context.Context, v *model.ExternalOutboundAccessGrant, currentID int64) error {
	if v.ExternalOutboundID == 0 {
		return errors.New("external_outbound_id required")
	}
	if _, err := s.store.GetExternalOutbound(ctx, v.ExternalOutboundID); err != nil {
		return fmt.Errorf("external_outbound_id: %w", err)
	}
	switch v.SubjectType {
	case model.AccessSubjectUser:
		if _, err := s.store.GetUser(ctx, v.SubjectID); err != nil {
			return fmt.Errorf("subject_id: %w", err)
		}
	case model.AccessSubjectGroup:
		if _, err := s.store.GetUserGroup(ctx, v.SubjectID); err != nil {
			return fmt.Errorf("subject_id: %w", err)
		}
	default:
		return errors.New("subject_type must be user or group")
	}
	items, err := s.store.ListExternalOutboundAccessGrants(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == currentID {
			continue
		}
		if item.ExternalOutboundID == v.ExternalOutboundID && item.SubjectType == v.SubjectType && item.SubjectID == v.SubjectID {
			return errors.New("same access grant already exists")
		}
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
		var v model.ProxyPath
		if !decode(w, r, &v) {
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
		if err := s.store.UpdateProxyPath(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		// A path built while disabled can hold steps that no longer project. Verify
		// the stored result and restore the previous row instead of letting the next
		// deployment fail for every server.
		if v.Enabled {
			if err := s.validateEnabledProxyPathPlan(r.Context(), id); err != nil {
				restore := *current
				_ = s.store.UpdateProxyPath(r.Context(), &restore)
				_ = s.normalizeProxyPathProcessingRoles(r.Context(), id)
				fail(w, err, 400)
				return
			}
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

type directProxyPathBranchRequest struct {
	InboundID    int64 `json:"inbound_id"`
	SourceStepID int64 `json:"source_step_id"`
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
	if err := s.store.CreateProxyPath(r.Context(), &path); err != nil {
		fail(w, err, 500)
		return
	}
	cleanup := func() { _ = s.store.DeleteProxyPath(r.Context(), path.ID) }
	for index, source := range prefix {
		step := source
		step.ID = 0
		step.PathID = path.ID
		step.Position = index + 1
		step.ProcessingRole = false
		step.CreatedAt = time.Time{}
		step.UpdatedAt = time.Time{}
		if err := s.store.CreateProxyPathStep(r.Context(), &step); err != nil {
			cleanup()
			fail(w, err, 500)
			return
		}
	}
	path.Enabled = true
	if err := s.validateProxyPath(r.Context(), &path); err != nil {
		cleanup()
		fail(w, err, 400)
		return
	}
	if err := s.store.UpdateProxyPath(r.Context(), &path); err != nil {
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
	write(w, http.StatusCreated, map[string]any{"proxy_path": path, "proxy_path_steps": publicProxyPathSteps(steps)})
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
	case model.ProxyPathKindChain, model.ProxyPathKindDirect:
	default:
		return errors.New("kind must be chain or direct")
	}
	if v.Kind == model.ProxyPathKindChain && v.BranchSourceStepID != nil {
		return errors.New("普通代理路径不能设置 branch_source_step_id")
	}
	if v.InboundID == 0 {
		return errors.New("inbound_id required")
	}
	if err := normalizeRegionSelection(&v.ExitRegionMode, &v.ExitRegionCode, "exit region"); err != nil {
		return err
	}
	inbound, err := s.store.GetInbound(ctx, v.InboundID)
	if err != nil {
		return fmt.Errorf("inbound_id: %w", err)
	}
	if inbound.Protocol == model.ProtocolSSH {
		return errors.New("SSH 入口是独立受限代理，不能加入代理链路")
	}
	if v.Enabled {
		if err := s.validateInboundPathReuse(ctx, v.InboundID, v.ID); err != nil {
			return err
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
	resolveRoutingProxyPathNames(&data)
	steps := make([]model.ProxyPathStep, 0, len(data.ProxyPathSteps))
	for _, step := range data.ProxyPathSteps {
		if step.PathID == pathID && step.Position >= position {
			continue
		}
		steps = append(steps, step)
	}
	// The remaining leading transparent segment may end on a different hop, so
	// mirror normalizeProxyPathProcessingRoles instead of reusing stale flags.
	if err := normalizeProxyPathProcessingRolesInMemory(steps, pathID); err != nil {
		return err
	}
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
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
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

func (s *Server) proxyPathSteps(w http.ResponseWriter, r *http.Request) {
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
		if err := s.store.CreateProxyPathStep(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.normalizeAndValidateProxyPath(r.Context(), v.PathID); err != nil {
			_ = s.store.Delete(r.Context(), "proxy_path_steps", v.ID)
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), v.PathID)
			fail(w, err, 400)
			return
		}
		if err := s.ensureWARPProfilesForProxyPaths(r.Context()); err != nil {
			_ = s.store.Delete(r.Context(), "proxy_path_steps", v.ID)
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), v.PathID)
			fail(w, err, 500)
			return
		}
		if err := s.store.ClearProxyPathBranchSource(r.Context(), v.PathID); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
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
		if err := s.normalizeAndValidateProxyPath(r.Context(), v.PathID); err != nil {
			_ = s.store.UpdateProxyPathStep(r.Context(), current)
			_ = s.normalizeProxyPathProcessingRoles(r.Context(), current.PathID)
			fail(w, err, 400)
			return
		}
		if err := s.ensureWARPProfilesForProxyPaths(r.Context()); err != nil {
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
		if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
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
		if deletedSteps < len(steps) {
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
		pathDeleted := len(remaining) == 0
		if pathDeleted {
			if err := s.store.DeleteProxyPath(r.Context(), current.PathID); err != nil {
				fail(w, err, 500)
				return
			}
		} else {
			if err := s.normalizeAndValidateProxyPath(r.Context(), current.PathID); err != nil {
				fail(w, err, 500)
				return
			}
			if err := s.store.ClearProxyPathBranchSource(r.Context(), current.PathID); err != nil {
				fail(w, err, 500)
				return
			}
		}
		if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "proxy-path-step", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true, "deleted_steps": deletedSteps, "path_deleted": pathDeleted})
	default:
		method(w)
	}
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
	if err := s.normalizeProxyPathStepCandidate(ctx, v); err != nil {
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
	if err := s.validateProxyPathServerLoop(ctx, path.InboundID, appendProxyPathStep(steps, *v, currentID)); err != nil {
		return err
	}
	// Branch reuse is a property of the path set, not of one step. It is enforced
	// when a path is created or enabled; repeating it here would reject adding a
	// hop with an error about branch reuse that the operator cannot act on.
	return nil
}

func (s *Server) normalizeProxyPathStepCandidate(ctx context.Context, v *model.ProxyPathStep) error {
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
		v.ConfigJSON = "{}"
		v.ProcessingRole = false
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
	case model.ProxyPathStepServerInbound:
		v.ExternalOutboundID = nil
		if v.InboundID != nil && *v.InboundID != 0 {
			inbound, err := s.store.GetInbound(ctx, *v.InboundID)
			if err != nil {
				return fmt.Errorf("inbound_id: %w", err)
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
			if err := normalizeProxyPathChainConfig(v, cfg); err != nil {
				return err
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
	case model.ProtocolMieru:
	default:
		return nil, errors.New("链路协议必须是 shadowsocks、vless 或 mieru")
	}
	return managed, nil
}

// normalizeProxyPathForwardConfig rebuilds a transparent forward step's config
// from an allowlist and validates the operator-selectable fields. internal_port
// is deliberately not accepted: the generated processing listener must stay
// under Controller port allocation so plan and core config agree.
func normalizeProxyPathForwardConfig(v *model.ProxyPathStep, cfg map[string]any) error {
	managed := map[string]any{}
	if raw, ok := cfg["backend"]; ok {
		backend := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
		if backend != "" && backend != "<nil>" {
			switch model.ForwardBackend(backend) {
			case model.ForwardBackendAuto, model.ForwardBackendRealm, model.ForwardBackendNFT, model.ForwardBackendBuiltin:
				managed["backend"] = backend
			default:
				return errors.New("端口转发后端必须是 auto、realm、nft 或 builtin")
			}
		}
	}
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
	paths, steps, servers, inbounds, externals, err := s.proxyPathNameData(ctx)
	if err != nil {
		return err
	}
	for index := range paths {
		path := &paths[index]
		if path.NameMode != model.ProxyPathNameCustom || core.ProxyPathNameTemplateIsValid(*path, steps, servers, inbounds, externals) {
			continue
		}
		path.NameMode = model.ProxyPathNameAuto
		path.NameTemplate = []model.ProxyPathNamePart{}
		if err := s.store.UpdateProxyPath(ctx, path); err != nil {
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
	root, err := s.store.GetInbound(ctx, rootInboundID)
	if err != nil {
		return err
	}
	seen := map[int64]bool{root.ServerID: true}
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

func (s *Server) validateWARPProfile(ctx context.Context, v *model.WARPProfile) error {
	if v.ServerID == 0 {
		return errors.New("server_id required")
	}
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("name required")
	}
	if _, err := s.store.GetServer(ctx, v.ServerID); err != nil {
		return err
	}
	if v.Status == "" {
		if strings.TrimSpace(v.ConfigJSON) != "" && strings.TrimSpace(v.ConfigJSON) != "{}" {
			v.Status = model.WARPStatusReady
		} else {
			v.Status = model.WARPStatusNeeded
		}
	}
	switch v.Status {
	case model.WARPStatusNeeded, model.WARPStatusRequested, model.WARPStatusReady, model.WARPStatusFailed:
	default:
		return fmt.Errorf("unsupported warp status %q", v.Status)
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
	if err := validJSONObject(v.ConfigJSON); err != nil {
		return err
	}
	if v.MTU < 0 || v.MTU > 9000 {
		return errors.New("mtu must be 0..9000")
	}
	return nil
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/users/")
	parts := pathParts(r.URL.Path, "/api/v1/users/")
	if len(parts) == 3 && parts[1] == "sessions" && parts[2] == "revoke" {
		s.revokeUserSessions(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "subscription-token" {
		s.userSubscriptionToken(w, r, id, parts[2])
		return
	}
	if len(parts) == 2 && parts[1] == "subscription-age" {
		s.userSubscriptionAge(w, r, id)
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
			Password string `json:"password"`
		}
		if !decode(w, r, &req) {
			return
		}
		u := req.User
		if u.Username == "" {
			fail(w, errors.New("username required"), 400)
			return
		}
		if u.Role == "" {
			u.Role = model.RoleViewer
		}
		if u.Status == "" {
			u.Status = "active"
		}
		if req.Password == "" {
			password, err := security.RandomToken(12)
			if err != nil {
				fail(w, err, 500)
				return
			}
			req.Password = password
		} else if len(req.Password) < 10 {
			fail(w, errors.New("password must be at least 10 characters"), 400)
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
			fail(w, err, 500)
			return
		}
		groupKey := store.UserGroupSystemUsers
		if u.Role == model.RoleAdmin {
			groupKey = store.UserGroupSystemAdmins
		}
		if err := s.store.AssignUserToBuiltinGroup(r.Context(), u.ID, groupKey); err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 201, map[string]any{"user": u})
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
		if protected {
			u.Role = model.RoleAdmin
			u.Status = "active"
		}
		if req.Password != "" {
			if len(req.Password) < 10 {
				fail(w, errors.New("password must be at least 10 characters"), 400)
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
		if err := s.validateAccessCapacityWith(r.Context(), func(data *accessData) {
			replaceUser(data, u)
		}); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateUser(r.Context(), &u); err != nil {
			fail(w, err, 500)
			return
		}
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
		if err := s.store.DeleteSubscriptionAssignmentsForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteInboundUsersForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteUserGroupMembersForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteInboundAccessGrantsForUser(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteExternalOutboundAccessGrantsForUser(r.Context(), id); err != nil {
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
		err := s.store.Delete(r.Context(), "users", id)
		if err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.queueCoreConfigRefreshForUserRemoval(r.Context(), id, "user_deleted"); err != nil {
			fail(w, err, 500)
			return
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
	settings, _ := s.store.ListSettings(ctx)
	loc := trafficLocation(settings)
	for i := range users {
		users[i].Protected, _ = s.store.IsBootstrapAdmin(ctx, users[i].ID)
		limit := core.EffectiveUserLimitPolicy(users[i], groups, members)
		periodKey, start, end := trafficWindow(time.Now(), limit.TrafficResetMode, limit.TrafficResetDay, loc)
		period, err := s.store.EnsureTrafficPeriod(ctx, users[i].ID, periodKey, start, end, limit.TrafficLimitBytes)
		if err != nil {
			continue
		}
		users[i].TrafficUsedBytes = period.Upload + period.Download
		users[i].TrafficPeriodKey = period.PeriodKey
		users[i].TrafficPeriodEnd = period.EndsAt.Format(time.RFC3339Nano)
		users[i].TrafficQuotaState = period.State
	}
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
		if err := s.store.UpdateUserSubscriptionToken(r.Context(), id, ""); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.DeleteOneTimeSubscriptionTokensForUser(r.Context(), id); err != nil {
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

func (s *Server) subscriptionProfiles(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/subscription-profiles/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetSubscriptionProfile(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"subscription_profile": item})
			return
		}
		items, err := s.store.ListSubscriptionProfiles(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"subscription_profiles": items})
	case http.MethodPost:
		var v model.SubscriptionProfile
		if !decode(w, r, &v) {
			return
		}
		if err := validateSubscriptionProfile(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateSubscriptionProfile(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "subscription-profile", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"subscription_profile": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetSubscriptionProfile(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		var v model.SubscriptionProfile
		if !decode(w, r, &v) {
			return
		}
		mergeSubscriptionProfilePatch(&v, current)
		v.ID = id
		if err := validateSubscriptionProfile(&v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateSubscriptionProfile(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "subscription-profile", fmt.Sprint(id))
		write(w, 200, map[string]any{"subscription_profile": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteSubscriptionAssignmentsForProfile(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		if err := s.store.Delete(r.Context(), "subscription_profiles", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "subscription-profile", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) subscriptionAssignments(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/subscription-assignments/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			item, err := s.store.GetSubscriptionAssignment(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"subscription_assignment": item})
			return
		}
		items, err := s.store.ListSubscriptionAssignments(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"subscription_assignments": items})
	case http.MethodPost:
		var v model.SubscriptionAssignment
		if !decode(w, r, &v) {
			return
		}
		if err := s.validateSubscriptionAssignment(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateSubscriptionAssignment(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "subscription-assignment", fmt.Sprint(v.ID))
		write(w, 201, map[string]any{"subscription_assignment": v})
	case http.MethodPatch:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetSubscriptionAssignment(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		var v model.SubscriptionAssignment
		if !decode(w, r, &v) {
			return
		}
		mergeSubscriptionAssignmentPatch(&v, current)
		v.ID = id
		if err := s.validateSubscriptionAssignment(r.Context(), &v); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateSubscriptionAssignment(r.Context(), &v); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "subscription-assignment", fmt.Sprint(id))
		write(w, 200, map[string]any{"subscription_assignment": v})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.Delete(r.Context(), "subscription_assignments", id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "subscription-assignment", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func validateSubscriptionProfile(v *model.SubscriptionProfile) error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("name required")
	}
	if strings.TrimSpace(v.GroupName) == "" {
		v.GroupName = "default"
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
	return validJSONObject(v.ConfigJSON)
}

func (s *Server) validateSubscriptionAssignment(ctx context.Context, v *model.SubscriptionAssignment) error {
	if v.ProfileID == 0 || v.UserID == 0 {
		return errors.New("profile_id and user_id are required")
	}
	if _, err := s.store.GetSubscriptionProfile(ctx, v.ProfileID); err != nil {
		return fmt.Errorf("profile_id: %w", err)
	}
	if _, err := s.store.GetUser(ctx, v.UserID); err != nil {
		return fmt.Errorf("user_id: %w", err)
	}
	if v.ServerID != nil {
		if _, err := s.store.GetServer(ctx, *v.ServerID); err != nil {
			return fmt.Errorf("server_id: %w", err)
		}
	}
	if v.InboundID != nil {
		if _, err := s.store.GetInbound(ctx, *v.InboundID); err != nil {
			return fmt.Errorf("inbound_id: %w", err)
		}
	}
	return nil
}

func mergeSubscriptionProfilePatch(v *model.SubscriptionProfile, current *model.SubscriptionProfile) {
	if v.Name == "" {
		v.Name = current.Name
	}
	if v.GroupName == "" {
		v.GroupName = current.GroupName
	}
	if v.Description == "" {
		v.Description = current.Description
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = current.ConfigJSON
	}
	v.Enabled = v.Enabled || current.Enabled
}

func mergeSubscriptionAssignmentPatch(v *model.SubscriptionAssignment, current *model.SubscriptionAssignment) {
	if v.ProfileID == 0 {
		v.ProfileID = current.ProfileID
	}
	if v.UserID == 0 {
		v.UserID = current.UserID
	}
	if v.ServerID == nil {
		v.ServerID = current.ServerID
	}
	if v.InboundID == nil {
		v.InboundID = current.InboundID
	}
	if v.GroupName == "" {
		v.GroupName = current.GroupName
	}
	v.Enabled = v.Enabled || current.Enabled
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
	case model.RoleAdmin, model.RoleOperator, model.RoleViewer:
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
	if u.SpeedLimitMbps < 0 || u.TrafficLimitBytes < 0 || u.TrafficUsedBytes < 0 {
		return errors.New("limits and traffic counters must be >= 0")
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

func (s *Server) queuePeriodicDNSBenchmarksForList(ctx context.Context, list model.DNSList) {
	policies, err := s.store.ListServerDNSPolicies(ctx)
	if err != nil {
		return
	}
	for _, policy := range policies {
		if policy.AutoTest != model.DNSAutoTestPeriodic || list.Kind == model.DNSListEncrypted && policy.EncryptedListID != list.ID || list.Kind == model.DNSListBootstrap && policy.BootstrapListID != list.ID {
			continue
		}
		encrypted, err := s.store.GetDNSList(ctx, policy.EncryptedListID)
		if err != nil {
			continue
		}
		bootstrap, err := s.store.GetDNSList(ctx, policy.BootstrapListID)
		if err != nil {
			continue
		}
		plan, err := core.DNSBenchmarkPlanForPolicy(time.Now().UnixNano(), policy, *encrypted, *bootstrap, model.DNSAutoTestPeriodic, "")
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
		if err := s.store.ValidateServerExists(r.Context(), v.SourceServerID, v.TargetServerID); err != nil {
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
		if err := s.store.ValidateServerExists(r.Context(), v.SourceServerID, v.TargetServerID); err != nil {
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
	if v.Backend == "" {
		v.Backend = model.ForwardBackendAuto
	}
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
	v.TrustedForward = nil
}

func validatePortForward(v model.PortForward) error {
	if v.Name == "" {
		return errors.New("name required")
	}
	if v.SourceServerID == 0 || v.TargetServerID == 0 {
		return errors.New("source_server_id and target_server_id required")
	}
	if v.SourceServerID == v.TargetServerID {
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
	if v.Backend == model.ForwardBackendBuiltin && v.Protocol != model.ForwardProtocolTCP {
		return errors.New("builtin forward backend currently supports tcp only")
	}
	if err := core.ValidateForwardProbeMode(v.ProbeMode); err != nil {
		return err
	}
	if v.ProbeIntervalSeconds < 300 {
		return errors.New("probe_interval_seconds must be >= 300")
	}
	if v.SampleRate < 0 || v.SampleRate > 1 {
		return errors.New("sample_rate must be between 0 and 1")
	}
	if (v.ProbeMode == "sampled" || v.ProbeMode == "periodic_sampled") && v.SampleRate <= 0 {
		return errors.New("sampled probe modes require sample_rate > 0")
	}
	return validJSONObject(v.ConfigJSON)
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
	// Preparation repairs stored topology, refreshes derived roles and allocates
	// one monotonic config version. Serialize it so two concurrent applies cannot
	// interleave those writes or queue overlapping desired state.
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()
	if err := s.store.PruneOrphanedProxyPathSteps(r.Context()); err != nil {
		fail(w, err, 500)
		return
	}
	if err := s.reconcileProxyPathNameTemplates(r.Context()); err != nil {
		fail(w, err, 500)
		return
	}
	if err := s.normalizeEnabledProxyPathProcessingRoles(r.Context()); err != nil {
		fail(w, err, 400)
		return
	}
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	resolveRoutingProxyPathNames(&data)
	servers, in := data.Servers, data.Inbounds
	externalEgressTargetsByServer := map[int64][]model.ExternalEgressProbeTarget{}
	if request.ServerID == 0 {
		targets := core.ExternalEgressProbeTargets(data.ProxyPaths, data.ProxyPathSteps, servers, in, data.ExternalOutbounds)
		if len(targets) > maxExternalEgressTargets {
			fail(w, fmt.Errorf("第三方出口探测分支过多，单次最多支持 %d 个", maxExternalEgressTargets), http.StatusBadRequest)
			return
		}
		for _, target := range targets {
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
				fail(w, fmt.Errorf("服务器 %s 的 Agent 不支持第三方节点出口探测；请先更新 Agent", name), http.StatusConflict)
				return
			}
		}
	}
	warpServerIDs, err := core.ProxyPathWARPServerIDs(data.ProxyPaths, data.ProxyPathSteps, in)
	if err != nil {
		fail(w, err, 400)
		return
	}
	for serverID := range warpServerIDs {
		if _, err := s.store.EnsureWARPProfileForServer(r.Context(), serverID); err != nil {
			fail(w, err, 500)
			return
		}
	}
	data.WARPProfiles, err = s.store.ListWARPProfiles(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	trustedServers := core.TrustedForwardServerIDs(data.ProxyPaths, data.ProxyPathSteps, in)
	if err := validateTrustedForwardAgentBuilds(servers, trustedServers); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if err := validateTrustedForwardDeploymentScope(request.ServerID, trustedServers); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	forwards, err := s.store.ListPortForwards(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	tunnels, err := s.store.ListTunnels(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	// Reuse the ports already recorded for generated listeners and let the
	// projection allocate only what is genuinely new. One ledger is shared by the
	// derivation below and by every per-server config, so all of them agree.
	allocations, err := s.store.ListProxyPathPortAllocations(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	ledger := core.NewProxyPathPortLedger(allocations)
	derivedForwards, err := core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, servers, in, ledger)
	if err != nil {
		fail(w, err, 400)
		return
	}
	derivedTunnels, err := core.DerivedTunnelsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, servers, in, ledger)
	if err != nil {
		fail(w, err, 400)
		return
	}
	forwards = append(forwards, derivedForwards...)
	tunnels = append(tunnels, derivedTunnels...)
	if err := core.ValidatePortForwards(servers, forwards); err != nil {
		fail(w, err, 400)
		return
	}
	if err := core.ValidateTunnels(servers, tunnels); err != nil {
		fail(w, err, 400)
		return
	}
	if err := core.ValidateTopologyDAG(servers, forwards, tunnels); err != nil {
		fail(w, err, 400)
		return
	}
	if _, err := s.syncDNSInbounds(r.Context(), servers, in); err != nil {
		fail(w, err, 400)
		return
	}
	version, err := s.store.NextConfigVersion(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	type preparedDeployment struct {
		serverID int64
		payload  model.DeploymentTaskPayload
	}
	prepared := make([]preparedDeployment, 0, len(servers))
	for _, server := range servers {
		if request.ServerID != 0 && server.ID != request.ServerID {
			continue
		}
		warpRequests := make([]model.WARPRequestPlan, 0)
		if warpServerIDs[server.ID] {
			profile, ok := findWARPProfileForServer(data.WARPProfiles, server.ID)
			if !ok || !profile.Enabled {
				fail(w, fmt.Errorf("server %s requires an unavailable WARP profile", server.Name), 400)
				return
			}
			if profile.Status == model.WARPStatusReady && strings.TrimSpace(profile.ConfigJSON) != "" {
				// The complete endpoint is already generated into the Controller config.
			} else {
				now := time.Now().UTC()
				profile.Status = model.WARPStatusRequested
				profile.LastRequestedAt = &now
				profile.Error = ""
				if err := s.store.UpdateWARPProfile(r.Context(), &profile); err != nil {
					fail(w, err, 500)
					return
				}
				data.WARPProfiles = replaceWARPProfile(data.WARPProfiles, profile)
				effectiveStack := core.EffectiveIPStack(server)
				plan := model.WARPRequestPlan{Version: version, ServerID: server.ID, ProfileID: profile.ID, OutboundTag: core.WARPOutboundTag(profile.ID), IPStack: effectiveStack, MTU: server.MTUValue, DNSStrategy: string(effectiveStack)}
				if plan.DNSStrategy == string(model.IPStackAuto) || plan.DNSStrategy == string(model.IPStackDualStack) {
					plan.DNSStrategy = "auto"
				}
				if plan.MTU == 0 && effectiveStack == model.IPStackIPv6Only {
					plan.MTU = 1280
				}
				warpRequests = append(warpRequests, plan)
			}
		}

		generated, err := s.generateServerCoreConfigWithLedger(r.Context(), server, data, ledger)
		if err != nil {
			// Generation rejects operator-fixable desired state too — a listener
			// conflict, an unreachable address, an unsupported protocol field. Those
			// are 400s like the dedicated validators below, not server faults.
			fail(w, err, deploymentConfigErrorStatus(err))
			return
		}
		managedAssets, cfg := generated.Assets, generated.Config
		configChanged := true
		if same, err := s.serverConfigUnchanged(r.Context(), server.ID, cfg); err != nil {
			fail(w, err, 500)
			return
		} else if same {
			configChanged = false
		}

		forwardPlan, err := core.BuildPortForwardPlan(version, server, servers, forwards)
		if err != nil {
			fail(w, err, 400)
			return
		}

		// Transparent processing paths remove the user protocol from the
		// source sing-box and bind the public entry port through the managed forwarder.
		inboundProbePlan := buildInboundProbePlan(version, server, in)
		var inboundProbe *model.InboundProbePlan
		var externalInboundProbe *model.InboundProbePlan
		if len(inboundProbePlan.EntryTargets) > 0 {
			inboundProbe = &inboundProbePlan
			externalInboundProbe = &inboundProbePlan
		}

		var forwardProbe *model.PortForwardPlan
		if probePlan := immediateForwardProbePlan(forwardPlan); len(probePlan.Rules) > 0 {
			forwardProbe = &probePlan
		}

		tunnelPlan, err := core.BuildTunnelPlan(version, server, servers, tunnels)
		if err != nil {
			fail(w, err, 400)
			return
		}
		sshInboundPlan, err := buildSSHInboundPlan(version, server, data, effectiveInboundUsersForRouting(data), generated.TrafficPolicies)
		if err != nil {
			fail(w, err, 400)
			return
		}
		if err := core.ValidateDeploymentListenResources(server.ID, cfg, forwardPlan, tunnelPlan, sshInboundPlan); err != nil {
			fail(w, err, 400)
			return
		}
		dnsState, err := core.DNSConfigStateForServer(server.ID, data.DNSLists, data.ServerDNSPolicies)
		if err != nil {
			fail(w, err, 400)
			return
		}
		dnsPlan, err := core.DNSBenchmarkPlanForPolicy(version, *dnsState.Policy, *dnsState.EncryptedList, *dnsState.BootstrapList, dnsState.Policy.AutoTest, "")
		if err != nil {
			fail(w, err, 400)
			return
		}
		var mtuPlan *model.MTUDetectionPlan
		if server.MTUMode != "" && server.MTUMode != model.MTUModeDisabled {
			candidate := mtuPlanFromServer(version, server, server.MTUMode)
			run, err := s.shouldRunDeploymentMTU(r.Context(), candidate)
			if err != nil {
				fail(w, err, 500)
				return
			}
			if run {
				mtuPlan = &candidate
			}
		}

		settings, err := s.store.ListSettings(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
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
	// Every server validated, so the ports this projection chose are the ones the
	// Agents will receive. Persist them before queueing any task: from now on a
	// later topology change must reuse these values instead of re-deriving them.
	if err := s.store.SaveProxyPathPortAllocations(r.Context(), ledger.Pending(), core.StaleProxyPathPortAllocationIDs(allocations, ledger)); err != nil {
		fail(w, err, 500)
		return
	}
	tasks := make([]model.AgentTask, 0, len(prepared))
	for _, item := range prepared {
		task, err := s.queueAgentTask(r.Context(), item.serverID, model.AgentTaskTypeApplyDeployment, item.payload, version)
		if err != nil {
			fail(w, err, 500)
			return
		}
		tasks = append(tasks, task)
		if item.payload.ExternalEgressProbe != nil {
			for _, target := range item.payload.ExternalEgressProbe.Targets {
				if err := s.store.MarkProxyPathEgressPending(r.Context(), target, version, task.ID); err != nil {
					fail(w, err, 500)
					return
				}
			}
		}
	}
	auditReq(s, r, "apply", "deployment", fmt.Sprint(version))
	write(w, 202, map[string]any{"config_version": version, "tasks": sanitizeTasksForRole(tasks, currentRole(r)), "summary": taskSummary(tasks)})
}

func validateTrustedForwardAgentBuilds(servers []model.Server, required map[int64]bool) error {
	for _, server := range servers {
		if required[server.ID] && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinTrustedForward) {
			return fmt.Errorf("服务器 %s 的 Agent 不支持可信透明转发；请先更新所有相关 Agent 后重试", server.Name)
		}
	}
	return nil
}

func validateTrustedForwardDeploymentScope(selectedServerID int64, required map[int64]bool) error {
	if selectedServerID != 0 && required[selectedServerID] {
		return errors.New("可信透明转发涉及多台服务器；请执行完整部署，不能仅部署其中一台服务器")
	}
	return nil
}

// buildSSHInboundPlan turns the regular inbound permissions into a dedicated
// user-facing SSH listener plan. It reuses the user's proxy password and never
// exposes the panel login password.
func buildSSHInboundPlan(version int64, server model.Server, data store.FullRoutingConfig, bindings []model.InboundUser, policies map[int64]model.TrafficRuntimePolicy) (model.SSHInboundPlan, error) {
	plan := model.SSHInboundPlan{Version: version, Inbounds: []model.SSHInbound{}}
	users := make(map[int64]model.User, len(data.Users))
	for _, user := range data.Users {
		users[user.ID] = user
	}
	bound := map[int64][]int64{}
	for _, binding := range bindings {
		if binding.Enabled {
			bound[binding.InboundID] = append(bound[binding.InboundID], binding.UserID)
		}
	}
	for _, inbound := range data.Inbounds {
		if !inbound.Enabled || inbound.ServerID != server.ID || inbound.Protocol != model.ProtocolSSH {
			continue
		}
		address := core.ResolveEntryAddress(inbound, server)
		if strings.TrimSpace(address) == "" {
			return model.SSHInboundPlan{}, fmt.Errorf("SSH 入口 %s 缺少可用的连接地址", inbound.Name)
		}
		entry := model.SSHInbound{InboundID: inbound.ID, ServerID: server.ID, Name: inbound.Name, ListenIP: core.EffectiveListenIP(server, inbound.ListenIP), Address: address, Username: "oboard", Port: inbound.Port, Enabled: true, Users: []model.SSHInboundUser{}, Policies: map[string]model.TrafficRuntimePolicy{}}
		seen := map[int64]bool{}
		for _, userID := range bound[inbound.ID] {
			if seen[userID] {
				continue
			}
			seen[userID] = true
			user, ok := users[userID]
			if !ok || user.Status != "active" || strings.HasPrefix(user.Username, "__oboard_") {
				continue
			}
			if strings.TrimSpace(user.ProxyPassword) == "" {
				continue
			}
			entry.Users = append(entry.Users, model.SSHInboundUser{UserID: user.ID, Username: sshLoginName(user.ID), Password: user.ProxyPassword, Enabled: true})
			if policy, ok := policies[user.ID]; ok {
				policy.InboundID = inbound.ID
				entry.Policies[fmt.Sprintf("user:%d", user.ID)] = policy
			}
		}
		plan.Inbounds = append(plan.Inbounds, entry)
	}
	return plan, nil
}

func sshLoginName(userID int64) string { return fmt.Sprintf("oboard-%d", userID) }

func (s *Server) sshPasswordDeploymentDigest(serverID, userID int64, password string) string {
	mac := hmac.New(sha256.New, []byte(s.sessionSecret))
	_, _ = fmt.Fprintf(mac, "ssh-password-deployment-v1\x00%d\x00%d\x00%s", serverID, userID, password)
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func (s *Server) serverConfigUnchanged(ctx context.Context, serverID int64, cfg string) (bool, error) {
	last, err := s.store.LastSuccessfulConfigTaskByServer(ctx, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch last.Type {
	case model.AgentTaskTypeApplyDeployment:
		if effective := effectiveConfigSHA256FromDeploymentResult(last.ResultJSON); effective != "" {
			digest, err := canonicalConfigSHA256(cfg)
			if err != nil {
				return false, err
			}
			return digest == effective, nil
		}
		var payload model.DeploymentTaskPayload
		if json.Unmarshal([]byte(last.PayloadJSON), &payload) == nil && strings.TrimSpace(payload.Config.Config) != "" {
			return payload.Config.Config == cfg, nil
		}
	case model.AgentTaskTypeApplyCoreConfig:
		var payload model.ApplyCoreConfigTaskPayload
		if json.Unmarshal([]byte(last.PayloadJSON), &payload) == nil && strings.TrimSpace(payload.Config) != "" {
			return payload.Config == cfg, nil
		}
	}
	return false, nil
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
		s.dismissDeploymentFailure(w, r, id)
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
	s.expireTimedOutTasks(r.Context())
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

func (s *Server) dismissDeploymentFailure(w http.ResponseWriter, r *http.Request, version int64) {
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
	version = latest[0].ConfigVersion
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
	s.expireTimedOutTasks(r.Context())
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
	s.expireTimedOutTasks(r.Context())
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

type trustedForwardDeploymentFootprint struct {
	Senders   []string `json:"senders,omitempty"`
	Receivers []string `json:"receivers,omitempty"`
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

func (s *Server) generateServerCoreConfig(ctx context.Context, server model.Server, data store.FullRoutingConfig) (generatedServerCoreConfig, error) {
	return s.generateServerCoreConfigWithLedger(ctx, server, data, nil)
}

// generateServerCoreConfigWithLedger lets the deployment pipeline share one
// generated-port allocation across every server it prepares. Passing nil derives
// a ledger from the allocations already loaded with the routing snapshot.
func (s *Server) generateServerCoreConfigWithLedger(ctx context.Context, server model.Server, data store.FullRoutingConfig, ledger *core.ProxyPathPortLedger) (generatedServerCoreConfig, error) {
	if ledger == nil {
		ledger = core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	}
	return s.generateServerCoreConfigInner(ctx, server, data, ledger)
}

func (s *Server) generateServerCoreConfigInner(ctx context.Context, server model.Server, data store.FullRoutingConfig, ledger *core.ProxyPathPortLedger) (generatedServerCoreConfig, error) {
	resolveRoutingProxyPathNames(&data)
	inbounds, assets, err := s.prepareCertificateInbounds(ctx, data.Inbounds, server.ID)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	dnsState, err := core.DNSConfigStateForServer(server.ID, data.DNSLists, data.ServerDNSPolicies)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	bindings := effectiveInboundUsersForRouting(data)
	accountingUsers := core.TrafficAccountingUsersForServer(server.ID, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, bindings)
	trafficPolicies, err := s.trafficRuntimePolicies(ctx, server.ID, data.Users, data.UserGroups, data.UserGroupMembers, accountingUsers)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	config, err := core.GenerateServerConfigWithOptions(server, inbounds, data.Outbounds, dnsState, data.Users, core.ConfigOptions{
		RoutingRules: data.RoutingRules, ExternalOutbounds: data.ExternalOutbounds, ProxyPaths: data.ProxyPaths, ProxyPathSteps: data.ProxyPathSteps,
		Servers: data.Servers, Inbounds: inbounds, WARPProfiles: data.WARPProfiles, InboundUsers: bindings,
		UserGroups: data.UserGroups, UserGroupMembers: data.UserGroupMembers, TrafficPolicies: trafficPolicies,
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
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	derivedForwards, err := core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger)
	if err != nil {
		return err
	}
	type preparedCoreRefresh struct {
		serverID int64
		payload  model.ApplyCoreConfigTaskPayload
	}
	prepared := make([]preparedCoreRefresh, 0, len(data.Servers))
	for _, server := range data.Servers {
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
		forwardPlan, err := core.BuildPortForwardPlan(0, server, data.Servers, derivedForwards)
		if err != nil {
			return err
		}
		if err := s.requireTrustedForwardDeploymentBaseline(ctx, server, generated.Config, forwardPlan); err != nil {
			return err
		}
		payload := model.ApplyCoreConfigTaskPayload{Config: generated.Config, Reason: reason, PrunedUserID: userID, Assets: generated.Assets}
		prepared = append(prepared, preparedCoreRefresh{serverID: server.ID, payload: payload})
	}
	if len(prepared) == 0 {
		return nil
	}
	version, err := s.store.NextConfigVersion(ctx)
	if err != nil {
		return err
	}
	for _, item := range prepared {
		if _, err := s.queueAgentTask(ctx, item.serverID, model.AgentTaskTypeApplyCoreConfig, item.payload, version); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) requireTrustedForwardDeploymentBaseline(ctx context.Context, server model.Server, config string, forwardPlan model.PortForwardPlan) error {
	expected, required, err := trustedForwardFootprint(config, forwardPlan)
	if err != nil || !required {
		return err
	}
	last, err := s.store.LastSuccessfulTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("服务器 %s 的可信透明转发尚未完成首次完整部署；请先执行完整部署", server.Name)
		}
		return err
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(last.PayloadJSON), &payload); err != nil {
		return fmt.Errorf("服务器 %s 的可信透明转发缺少有效的完整部署基线；请重新执行完整部署", server.Name)
	}
	actual, _, err := trustedForwardFootprint(payload.Config.Config, payload.PortForwards)
	if err != nil || actual != expected {
		return fmt.Errorf("服务器 %s 的可信透明转发已变更；请先执行完整部署再刷新核心配置", server.Name)
	}
	return nil
}

func trustedForwardFootprint(config string, forwardPlan model.PortForwardPlan) (string, bool, error) {
	footprint := trustedForwardDeploymentFootprint{}
	for _, rule := range forwardPlan.Rules {
		if rule.TrustedForward == nil {
			continue
		}
		signature, err := json.Marshal(struct {
			RuleID        int64                       `json:"rule_id"`
			ListenIP      string                      `json:"listen_ip"`
			ListenPort    int                         `json:"listen_port"`
			TargetAddress string                      `json:"target_address"`
			TargetPort    int                         `json:"target_port"`
			Protocol      model.ForwardProtocol       `json:"protocol"`
			Sender        *model.TrustedForwardSender `json:"sender"`
		}{rule.ID, rule.ListenIP, rule.ListenPort, rule.TargetAddress, rule.TargetPort, rule.Protocol, rule.TrustedForward})
		if err != nil {
			return "", false, err
		}
		footprint.Senders = append(footprint.Senders, string(signature))
	}
	if strings.TrimSpace(config) != "" {
		var runtime struct {
			OBoard struct {
				TrustedForward struct {
					Receivers []struct {
						Version             int    `json:"version"`
						ID                  string `json:"id"`
						PathID              int64  `json:"path_id"`
						InboundTag          string `json:"inbound_tag"`
						Network             string `json:"network"`
						Listen              string `json:"listen"`
						ListenPort          int    `json:"listen_port"`
						Target              string `json:"target"`
						TargetPort          int    `json:"target_port"`
						Key                 string `json:"key"`
						MaxClockSkewSeconds int    `json:"max_clock_skew_seconds"`
					} `json:"receivers"`
				} `json:"trusted_forward"`
			} `json:"_oboard"`
		}
		if err := json.Unmarshal([]byte(config), &runtime); err != nil {
			return "", false, err
		}
		for _, receiver := range runtime.OBoard.TrustedForward.Receivers {
			// path_id identifies one current branch for diagnostics, but a shared
			// transparent receiver can outlive that branch. It does not change the
			// listener, framing, key, or target and therefore is not topology.
			signature, err := json.Marshal(struct {
				Version             int    `json:"version"`
				ID                  string `json:"id"`
				InboundTag          string `json:"inbound_tag"`
				Network             string `json:"network"`
				Listen              string `json:"listen"`
				ListenPort          int    `json:"listen_port"`
				Target              string `json:"target"`
				TargetPort          int    `json:"target_port"`
				Key                 string `json:"key"`
				MaxClockSkewSeconds int    `json:"max_clock_skew_seconds"`
			}{receiver.Version, receiver.ID, receiver.InboundTag, receiver.Network, receiver.Listen, receiver.ListenPort, receiver.Target, receiver.TargetPort, receiver.Key, receiver.MaxClockSkewSeconds})
			if err != nil {
				return "", false, err
			}
			footprint.Receivers = append(footprint.Receivers, string(signature))
		}
	}
	sort.Strings(footprint.Senders)
	sort.Strings(footprint.Receivers)
	required := len(footprint.Senders) > 0 || len(footprint.Receivers) > 0
	encoded, err := json.Marshal(footprint)
	return string(encoded), required, err
}

func findWARPProfile(items []model.WARPProfile, id int64) (model.WARPProfile, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return model.WARPProfile{}, false
}

func findWARPProfileForServer(items []model.WARPProfile, serverID int64) (model.WARPProfile, bool) {
	for _, item := range items {
		if item.ServerID == serverID {
			return item, true
		}
	}
	return model.WARPProfile{}, false
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
	if !s.allowRate(w, r, "subscription-token:"+token, 60, time.Minute) {
		return
	}
	user, err := s.store.GetUserBySubscriptionToken(r.Context(), token)
	if err != nil {
		fail(w, errors.New("invalid subscription link"), 404)
		return
	}
	format := core.NormalizeSubscriptionFormatForAPI(model.SubscriptionFormat(r.URL.Query().Get("format")))
	if !core.IsSupportedSubscriptionFormat(format) {
		s.recordRejectedSubscriptionPull(r, user.ID, string(format), nil, false, "unsupported subscription format")
		fail(w, fmt.Errorf("unsupported subscription format %q", r.URL.Query().Get("format")), 400)
		return
	}
	settings, err := s.store.ListSettings(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	ageRecipient, ageEncrypted, err := resolveSubscriptionAgeRecipient(r, *user, settings[settingSubscriptionAgePolicy], format)
	if err != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, string(format), nil, false, err.Error())
		status := http.StatusBadRequest
		if errors.Is(err, errSubscriptionAgeKeyRequired) {
			status = http.StatusPreconditionRequired
		} else if errors.Is(err, errSubscriptionAgeNotEnabled) {
			status = http.StatusForbidden
		}
		fail(w, err, status)
		return
	}
	var profile *model.SubscriptionProfile
	profileID := int64Query(r, "profile_id", 0)
	var requestedProfileID *int64
	if profileID != 0 {
		requestedProfileID = &profileID
	}
	if profileID != 0 {
		profile, err = s.store.GetSubscriptionProfile(r.Context(), profileID)
		if err != nil {
			s.recordRejectedSubscriptionPull(r, user.ID, string(format), requestedProfileID, ageEncrypted, "subscription profile not found")
			fail(w, err, 404)
			return
		}
		if !profile.Enabled {
			s.recordRejectedSubscriptionPull(r, user.ID, string(format), requestedProfileID, ageEncrypted, "subscription profile is disabled")
			fail(w, errors.New("subscription profile is disabled"), 403)
			return
		}
	}
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	servers, in := data.Servers, data.Inbounds
	sshServerHostKeys := map[int64]string{}
	deployedPasswordDigests := map[int64]string{}
	deployments, deploymentErr := s.store.ListSSHPasswordDeploymentsForUser(r.Context(), user.ID)
	if deploymentErr != nil {
		fail(w, deploymentErr, 500)
		return
	}
	for _, deployment := range deployments {
		deployedPasswordDigests[deployment.ServerID] = deployment.PasswordDigest
	}
	for _, server := range servers {
		if deployedPasswordDigests[server.ID] != s.sshPasswordDeploymentDigest(server.ID, user.ID, user.ProxyPassword) {
			continue
		}
		hostKey, hostErr := s.store.GetSSHServerHostKey(r.Context(), server.ID)
		if hostErr == nil {
			sshServerHostKeys[server.ID] = hostKey.PublicKey
		} else if !errors.Is(hostErr, sql.ErrNoRows) {
			fail(w, hostErr, 500)
			return
		}
	}
	assignments, err := s.store.ListSubscriptionAssignmentsForUser(r.Context(), user.ID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	inboundUsers := core.EffectiveInboundUsers(in, []model.User{*user}, data.InboundUsers, data.UserGroups, data.UserGroupMembers, data.InboundAccessGrants)
	if profileID != 0 {
		filtered := assignments[:0]
		for _, assignment := range assignments {
			if assignment.ProfileID == profileID {
				filtered = append(filtered, assignment)
			}
		}
		assignments = filtered
	}
	sub, err := core.GenerateSubscriptionWithOptions(*user, servers, in, core.SubscriptionOptions{
		Format:                       format,
		Profile:                      profile,
		RequireAssignments:           profileID != 0,
		Assignments:                  assignments,
		InboundUsers:                 inboundUsers,
		ProxyPaths:                   data.ProxyPaths,
		ProxyPathSteps:               data.ProxyPathSteps,
		ProxyPathEgressResults:       data.ProxyPathEgressResults,
		ExternalOutbounds:            data.ExternalOutbounds,
		ExternalOutboundAccessGrants: data.ExternalOutboundAccessGrants,
		UserGroups:                   data.UserGroups,
		UserGroupMembers:             data.UserGroupMembers,
		SSHServerHostKeys:            sshServerHostKeys,
	})
	if err != nil {
		s.recordRejectedSubscriptionPull(r, user.ID, string(format), requestedProfileID, ageEncrypted, "subscription generation failed")
		fail(w, err, 500)
		return
	}
	body := []byte(sub)
	if ageEncrypted {
		body, err = encryptSubscriptionAgeArmor(sub, ageRecipient)
		if err != nil {
			s.recordRejectedSubscriptionPull(r, user.ID, string(format), requestedProfileID, true, "subscription encryption failed")
			fail(w, fmt.Errorf("encrypt subscription with age: %w", err), 500)
			return
		}
	}
	event := s.newSubscriptionPullAudit(r, user.ID, string(format), requestedProfileID, ageEncrypted)
	decision, err := s.store.AuthorizeSubscriptionPull(r.Context(), user.ID, token, event, s.subscriptionAuditPolicy(r.Context()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, errors.New("invalid subscription link"), 404)
			return
		}
		fail(w, err, 500)
		return
	}
	s.notifySubscriptionAuditRisk(r.Context(), *user, decision)
	s.publishRealtime("audit", "subscriptions", "users")
	if !decision.Allowed {
		s.maybeNotifySubscriptionAbnormal(r.Context(), user.ID)
		fail(w, errors.New("subscription access suspended; contact an administrator"), http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	if decision.Burned {
		w.Header().Set("X-OBoard-Subscription", "burned-after-read")
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
		server.CPU = req.Health.CPU
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
	_ = s.store.AddAudit(r.Context(), model.AuditLog{Action: "agent_enroll", Target: "server", Detail: server.Name, IP: clientIP(r)})
	log.Printf("agent enrolled server=%d(%s) agent_id=%s remote=%s", server.ID, safeLogField(server.Name), safeLogField(agentID), safeLogField(clientIP(r)))
	write(w, 200, model.AgentEnrollResponse{ServerID: server.ID, AgentID: agentID, AgentToken: agentToken, ConnectionAuditEnabled: server.ConnectionAuditEnabled})
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

func (s *Server) agentConnect(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowRate(w, r, "agent-connect:"+server.AgentID, 30, time.Minute) {
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
	defer func() {
		log.Printf("agent disconnected server=%d(%s) connected_for=%s", server.ID, safeLogField(server.Name), time.Since(connectedAt).Round(time.Second))
	}()
	mode, _ := serverMonitoringPolicy(server)
	var heartbeatInterval time.Duration
	_ = conn.WriteJSON(map[string]any{"type": "hello", "ts": time.Now().UTC(), "server_id": server.ID, "monitoring_mode": mode, "connectivity_probe_enabled": server.ConnectivityProbeEnabled, "connection_audit_enabled": server.ConnectionAuditEnabled})
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	var initial map[string]json.RawMessage
	if err := conn.ReadJSON(&initial); err == nil {
		s.processAgentSocketMessage(r.Context(), server, initial, clientIP(r))
	}
	var inFlightTaskID int64
	var inFlightTaskType string
	defer func() {
		if inFlightTaskID == 0 {
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
	for {
		readDeadline := 35 * time.Second
		task, err := s.store.NextTask(r.Context(), server.ID)
		if err == nil {
			inFlightTaskID = task.ID
			inFlightTaskType = task.Type
			log.Printf("task dispatched server=%d(%s) id=%d type=%s version=%d", server.ID, safeLogField(server.Name), task.ID, task.Type, task.ConfigVersion)
			s.publishRealtime(realtimeResourcesForTask(task.Type)...)
			readDeadline = 10 * time.Minute
			if task.Type == model.AgentTaskTypeIssueCertificateHTTP {
				readDeadline = 20 * time.Minute
			}
			_ = conn.WriteJSON(map[string]any{"type": "task_request", "ts": time.Now().UTC(), "task": task, "signature_version": 2, "signature": signAgentTaskEnvelope(server.AgentTokenHash, *task)})
		} else {
			inFlightTaskID = 0
			inFlightTaskType = ""
			if latest, loadErr := s.store.GetServer(r.Context(), server.ID); loadErr == nil {
				server = latest
			}
			mode, heartbeatInterval = serverMonitoringPolicy(server)
			select {
			case <-r.Context().Done():
				return
			case <-time.After(heartbeatInterval):
			}
			_ = conn.WriteJSON(map[string]any{"type": "heartbeat", "ts": time.Now().UTC(), "monitoring_mode": mode, "connectivity_probe_enabled": server.ConnectivityProbeEnabled, "connection_audit_enabled": server.ConnectionAuditEnabled})
		}
		var msg map[string]json.RawMessage
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err) {
				log.Printf("agent ws closed: %v", err)
			}
			return
		}
		inFlightTaskID = 0
		inFlightTaskType = ""
		s.processAgentSocketMessage(r.Context(), server, msg, clientIP(r))
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

func (s *Server) processAgentSocketMessage(ctx context.Context, server *model.Server, msg map[string]json.RawMessage, remoteIP string) {
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
			current, currentErr := s.store.GetServer(ctx, server.ID)
			if currentErr != nil {
				return
			}
			h.ConnectivityProbeEnabled = current.ConnectivityProbeEnabled
			if !current.ConnectivityProbeEnabled {
				h.ConnectivityAvailable = false
				h.ConnectivityLatencyMS = 0
				h.ConnectivityCheckedAt = time.Time{}
				h.ConnectivityError = ""
			}
			settings, _ := s.store.ListSettings(ctx)
			_, start, end := trafficWindow(time.Now(), current.TrafficResetMode, current.TrafficResetDay, trafficLocation(settings))
			window := model.ServerTrafficWindow{Key: start.Format("2006-01-02"), Start: start, End: end}
			old, next, err := s.store.UpsertHealthTransition(ctx, h, window)
			if err == nil {
				s.completeAgentUpdateAfterReconnect(ctx, server.ID, h.AgentBuild)
				s.publishRealtime("server_runtime", "server_metrics")
			}
			if err == nil && old == model.ServerOffline && next == model.ServerOnline {
				log.Printf("server %d(%s) recovered and is online again", server.ID, safeLogField(current.Name))
				s.handleServerRecovered(ctx, server.ID)
			}
		}
	}
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
	const maxPlausibleNetworkBPS = uint64(100 << 30)
	if report.NetworkUploadBPS > maxPlausibleNetworkBPS {
		report.NetworkUploadBPS = 0
	}
	if report.NetworkDownloadBPS > maxPlausibleNetworkBPS {
		report.NetworkDownloadBPS = 0
	}
	if report.ConnectivityLatencyMS < 0 || report.ConnectivityLatencyMS > 60_000 {
		report.ConnectivityLatencyMS = 0
		report.ConnectivityAvailable = false
		report.ConnectivityError = "invalid connectivity probe latency"
	}
	if !report.ConnectivityCheckedAt.IsZero() {
		checked := report.ConnectivityCheckedAt.UTC()
		if checked.Before(now.Add(-10*time.Minute)) || checked.After(now.Add(2*time.Minute)) {
			report.ConnectivityCheckedAt = time.Time{}
		} else {
			report.ConnectivityCheckedAt = checked
		}
	}
	if len(report.ConnectivityError) > 240 {
		report.ConnectivityError = report.ConnectivityError[:240]
	}
	report.RegionCode = normalizeControllerRegionCode(report.RegionCode)
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
}

func (s *Server) agentTaskResults(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowRate(w, r, "agent-task-result:"+server.AgentID, 120, time.Minute) {
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
	if err := s.applyTimeCheckTaskResult(r.Context(), *task, req.Status, req.ResultJSON); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if err := s.completeTaskWithNotification(r.Context(), req.TaskID, req.Status, req.ResultJSON); err != nil {
		fail(w, err, 500)
		return
	}
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
	passwordDigests := map[int64]string{}
	for _, inbound := range payload.SSHInbounds.Inbounds {
		for _, user := range inbound.Users {
			if !user.Enabled || user.UserID <= 0 || strings.TrimSpace(user.Password) == "" {
				continue
			}
			digest := s.sshPasswordDeploymentDigest(serverID, user.UserID, user.Password)
			if previous := passwordDigests[user.UserID]; previous != "" && previous != digest {
				return fmt.Errorf("SSH deployment user %d contains conflicting passwords", user.UserID)
			}
			passwordDigests[user.UserID] = digest
		}
	}
	return s.store.ApplySSHDeploymentState(ctx, model.SSHServerHostKey{ServerID: serverID, PublicKey: canonicalHostKey, Fingerprint: hostFingerprint, ConfigVersion: version}, passwordDigests)
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
	if !s.allowRate(w, r, "agent-traffic:"+server.AgentID, 120, time.Minute) {
		return
	}
	settings, _ := s.store.ListSettings(r.Context())
	loc := trafficLocation(settings)
	type trafficItem struct {
		ReportID  string `json:"report_id"`
		UserID    int64  `json:"user_id"`
		InboundID *int64 `json:"inbound_id"`
		PathID    *int64 `json:"path_id"`
		PeriodKey string `json:"period_key"`
		Upload    int64  `json:"upload_bytes"`
		Download  int64  `json:"download_bytes"`
		StartedAt string `json:"started_at"`
		EndedAt   string `json:"ended_at"`
	}
	var req struct {
		PeriodKey string        `json:"period_key"`
		Items     []trafficItem `json:"items"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Items) > 500 {
		fail(w, errors.New("too many traffic items in one report"), 400)
		return
	}
	accepted := []string{}
	access, err := s.loadAccessData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	users := access.Users
	groups := access.Groups
	members := access.Members
	userByID := map[int64]model.User{}
	for _, u := range users {
		userByID[u.ID] = u
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range access.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	type accessPair struct{ inboundID, userID int64 }
	allowed := map[accessPair]struct{}{}
	for _, binding := range core.EffectiveInboundUsers(access.Inbounds, access.Users, access.InboundUsers, access.Groups, access.Members, access.Grants) {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID}] = struct{}{}
		}
	}
	paths, err := s.store.ListProxyPaths(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	steps, err := s.store.ListProxyPathSteps(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	for _, item := range req.Items {
		if item.Upload < 0 || item.Download < 0 || item.UserID <= 0 {
			fail(w, errors.New("traffic report is invalid"), 400)
			return
		}
		if item.InboundID == nil {
			fail(w, errors.New("traffic report must identify an inbound"), 400)
			return
		}
		inbound, ok := inboundByID[*item.InboundID]
		if !ok || !inbound.Enabled {
			fail(w, errors.New("inbound does not belong to this agent"), 403)
			return
		}
		accountingLocation := inbound.ServerID == server.ID
		if item.PathID != nil {
			if *item.PathID <= 0 {
				fail(w, errors.New("traffic report path_id must be positive"), 400)
				return
			}
			accountingLocation = core.IsProxyPathAccountingLocation(server.ID, inbound.ID, *item.PathID, paths, steps, access.Inbounds)
		} else if core.ProxyPathRequiresAccountingPathID(inbound.ID, paths, steps, access.Inbounds) {
			fail(w, errors.New("traffic report must identify the transparent proxy path"), 400)
			return
		}
		if !accountingLocation {
			fail(w, errors.New("inbound does not belong to this agent"), 403)
			return
		}
		u, ok := userByID[item.UserID]
		if !ok || u.Status != "active" {
			fail(w, errors.New("user is invalid or inactive"), 400)
			return
		}
		if _, ok := allowed[accessPair{inboundID: *item.InboundID, userID: item.UserID}]; !ok {
			fail(w, errors.New("user is not authorized for this inbound"), 403)
			return
		}
		limit := core.EffectiveUserLimitPolicy(u, groups, members)
		reportedPeriodKey := strings.TrimSpace(item.PeriodKey)
		if reportedPeriodKey == "" {
			reportedPeriodKey = strings.TrimSpace(req.PeriodKey)
		}
		periodKey, start, end, err := trafficWindowForPeriodKey(time.Now(), reportedPeriodKey, limit.TrafficResetMode, limit.TrafficResetDay, loc)
		if err != nil {
			fail(w, err, 400)
			return
		}
		started := parseReportTime(item.StartedAt)
		ended := parseReportTime(item.EndedAt)
		report := model.TrafficReport{ReportID: item.ReportID, ServerID: server.ID, UserID: item.UserID, InboundID: item.InboundID, PathID: item.PathID, PeriodKey: periodKey, Upload: item.Upload, Download: item.Download, StartedAt: started, EndedAt: ended}
		ids, err := s.store.AddTrafficReports(r.Context(), []model.TrafficReport{report}, model.TrafficPeriod{UserID: item.UserID, PeriodKey: periodKey, StartedAt: start, EndsAt: end, Limit: limit.TrafficLimitBytes})
		if err != nil {
			fail(w, err, 500)
			return
		}
		accepted = append(accepted, ids...)
		if period, err := s.store.GetTrafficPeriod(r.Context(), item.UserID, periodKey); err == nil {
			s.notifyTrafficQuotaExceeded(r.Context(), u, period)
		}
	}
	effectiveBindings := core.EffectiveInboundUsers(access.Inbounds, access.Users, access.InboundUsers, access.Groups, access.Members, access.Grants)
	accountingUsers := core.TrafficAccountingUsersForServer(server.ID, paths, steps, access.Inbounds, effectiveBindings)
	policies, err := s.trafficRuntimePolicies(r.Context(), server.ID, users, groups, members, accountingUsers)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"ok": true, "accepted_report_ids": accepted, "policies": policies})
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
	if !s.allowRate(w, r, "agent-dns-benchmark:"+server.AgentID, 30, time.Minute) {
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
	if req.ReportID == "" || req.PolicyRevision == 0 || req.EncryptedListID == 0 || req.EncryptedListRevision == 0 || req.BootstrapListID == 0 || req.BootstrapListRevision == 0 {
		fail(w, errors.New("dns benchmark report and revision snapshot are required"), 400)
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
	if outcome.Success && outcome.ApplyOnSuccess {
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
	derivedForwards, err := core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger)
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
	forwardPlan, err := core.BuildPortForwardPlan(0, server, data.Servers, derivedForwards)
	if err != nil {
		return err
	}
	if err := s.requireTrustedForwardDeploymentBaseline(ctx, server, generated.Config, forwardPlan); err != nil {
		return err
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
	if !s.allowRate(w, r, "agent-mtu:"+server.AgentID, 30, time.Minute) {
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
	if !s.allowRate(w, r, "agent-pf-probe:"+server.AgentID, 60, time.Minute) {
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

func (s *Server) authAgent(w http.ResponseWriter, r *http.Request) (*model.Server, bool) {
	agentID := r.Header.Get("X-Agent-ID")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(token) == "" {
		fail(w, errors.New("invalid agent credentials"), 401)
		return nil, false
	}
	server, err := s.store.GetServerByAgent(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, errors.New("invalid agent credentials"), 401)
		} else {
			fail(w, err, http.StatusInternalServerError)
		}
		return nil, false
	}
	// Constant-time compare of hex-encoded SHA-256 hashes.
	if !hmac.Equal([]byte(server.AgentTokenHash), []byte(security.HashSecret(token))) {
		fail(w, errors.New("invalid agent credentials"), 401)
		return nil, false
	}
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
  rm -f "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/oboard-sb" "$INSTALL_DIR/obag"
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
  version_json=$(curl -fsSL "$BASE_URL/api/v2/ui/version" 2>/dev/null || true)
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
}

verify_installed_versions() {
  print_installed_versions
  if [ -n "${TARGET_BUILD:-}" ] && [ -x "$INSTALL_DIR/oboard-agent" ]; then
    if ! "$INSTALL_DIR/oboard-agent" -version 2>/dev/null | grep -q "build $TARGET_BUILD"; then
      echo "警告：Agent 二进制 build 与目标 build 不一致，请检查下载缓存或重新执行更新命令。" >&2
      return 1
    fi
  fi
  if [ -n "${TARGET_KERNEL_BUILD:-${TARGET_BUILD:-}}" ] && [ -x "$INSTALL_DIR/oboard-sb" ]; then
    expected_core_build=${TARGET_KERNEL_BUILD:-$TARGET_BUILD}
    if ! "$INSTALL_DIR/oboard-sb" -version 2>/dev/null | grep -q "\"build\": \"$expected_core_build\""; then
      echo "警告：优化内核 build 与目标 build 不一致，请检查下载缓存或重新执行更新命令。" >&2
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
  agent_url="${BASE_URL}/downloads/${agent_name}"
  core_url="${BASE_URL}/downloads/${core_name}"
  curl -fsSL "$agent_url" -o "$tmp/$agent_name"
  curl -fsSL "$core_url" -o "$tmp/$core_name"
  echo "[3/4] 校验并安装组件"
  curl -fsSL "${BASE_URL}/downloads/release-manifest.json" -o "$tmp/release-manifest.json"
  curl -fsSL "${BASE_URL}/downloads/release-manifest.json.sig" -o "$tmp/release-manifest.json.sig"
  verify_downloaded_release "$tmp/release-manifest.json" "$tmp/release-manifest.json.sig" "$tmp" "$OS_VALUE" "$ARCH_VALUE" "$agent_name" "$core_name" >> "$INSTALL_LOG" 2>&1
  chmod 0755 "$tmp/$agent_name" "$tmp/$core_name"
  install -d -m 0755 -o root -g root "$INSTALL_DIR"
  # Do not truncate an executable that may currently be running. Write beside it
  # and atomically rename; Linux keeps the old inode for the running process and
  # new restarts pick up the new binary.
  install -m 0755 "$tmp/$agent_name" "$INSTALL_DIR/oboard-agent.new"
  install -m 0755 "$tmp/$core_name" "$INSTALL_DIR/oboard-sb.new"
  mv -f "$INSTALL_DIR/oboard-agent.new" "$INSTALL_DIR/oboard-agent"
  mv -f "$INSTALL_DIR/oboard-sb.new" "$INSTALL_DIR/oboard-sb"
  rm -f "$INSTALL_DIR/obag"
  ln -s "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/obag"
  register_obag_path
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
ExecReload=/bin/kill -HUP \$MAINPID
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
extra_commands="reload"

depend() {
  need net
  after firewall
}

start_pre() {
  checkpath -d -m 0700 "$STATE_DIR"
}

reload() {
  ebegin "Reloading \${RC_SVCNAME}"
  start-stop-daemon --signal HUP --pidfile "\${pidfile}"
  eend $?
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

restart_after_update() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl daemon-reload >> "$INSTALL_LOG" 2>&1
    if [ -s "$STATE_DIR/sing-box.json" ]; then systemctl restart oboard-sb >> "$INSTALL_LOG" 2>&1 || true; fi
    systemctl restart oboard-agent >> "$INSTALL_LOG" 2>&1 || true
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    if [ -s "$STATE_DIR/sing-box.json" ]; then rc-service oboard-sb restart >> "$INSTALL_LOG" 2>&1 || true; fi
    rc-service oboard-agent restart >> "$INSTALL_LOG" 2>&1 || true
  fi
}

case "$ACTION" in
  install)
    need_base_url
    : "${OBOARD_ENROLL_TOKEN:?缺少 OBOARD_ENROLL_TOKEN}"
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
    restart_after_install
    verify_installed_versions || true
    print_management_help "安装完成"
    echo "提示：oboard-sb 会在面板首次下发配置后自动启动。"
    ;;
  update)
    need_base_url
    download_binaries
    persist_agent_install_dir
    write_units
    echo "[4/4] 刷新 Agent 服务"
    restart_after_update
    verify_installed_versions || true
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
	_, _ = w.Write([]byte(script))
}

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
  version_json=$(curl -fsSL "$BASE_URL/api/v2/ui/version" 2>/dev/null || true)
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
}

verify_installed_versions() {
  print_installed_versions
  if [ -n "${TARGET_BUILD:-}" ] && [ -x "$INSTALL_DIR/oboard-agent" ]; then
    "$INSTALL_DIR/oboard-agent" -version 2>/dev/null | grep -q "build $TARGET_BUILD" || echo "警告：Agent 二进制 build 与目标 build 不一致。" >&2
  fi
  if [ -n "${TARGET_KERNEL_BUILD:-${TARGET_BUILD:-}}" ] && [ -x "$INSTALL_DIR/oboard-sb" ]; then
    expected_core_build=${TARGET_KERNEL_BUILD:-$TARGET_BUILD}
    "$INSTALL_DIR/oboard-sb" -version 2>/dev/null | grep -q "\"build\": \"$expected_core_build\"" || echo "警告：优化内核 build 与目标 build 不一致。" >&2
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

tmp=$(make_update_tmp)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

agent_name="oboard-agent-${OS_VALUE}-${ARCH_VALUE}"
core_name="oboard-sb-${OS_VALUE}-${ARCH_VALUE}"
echo "下载 Agent：$BASE_URL/downloads/$agent_name"
curl -fsSL "$BASE_URL/downloads/$agent_name" -o "$tmp/$agent_name"
echo "下载优化内核：$BASE_URL/downloads/$core_name"
curl -fsSL "$BASE_URL/downloads/$core_name" -o "$tmp/$core_name"
curl -fsSL "$BASE_URL/downloads/release-manifest.json" -o "$tmp/release-manifest.json"
curl -fsSL "$BASE_URL/downloads/release-manifest.json.sig" -o "$tmp/release-manifest.json.sig"
verify_downloaded_release "$tmp/release-manifest.json" "$tmp/release-manifest.json.sig" "$tmp" "$OS_VALUE" "$ARCH_VALUE" "$agent_name" "$core_name"
chmod 0755 "$tmp/$agent_name" "$tmp/$core_name"

install_downloaded_binaries_direct() {
  install -d -m 0755 -o root -g root "$INSTALL_DIR"
  # Do not truncate an executable that may currently be running. Write beside it
  # and atomically rename; Linux keeps the old inode for the running process and
  # new restarts pick up the new binary.
  install -m 0755 "$tmp/$agent_name" "$INSTALL_DIR/oboard-agent.new"
  install -m 0755 "$tmp/$core_name" "$INSTALL_DIR/oboard-sb.new"
  mv -f "$INSTALL_DIR/oboard-agent.new" "$INSTALL_DIR/oboard-agent"
  mv -f "$INSTALL_DIR/oboard-sb.new" "$INSTALL_DIR/oboard-sb"
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
mv -f "$INSTALL_DIR/oboard-agent.new" "$INSTALL_DIR/oboard-agent"
mv -f "$INSTALL_DIR/oboard-sb.new" "$INSTALL_DIR/oboard-sb"
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
data.setdefault("time_correction_mode", "off")
data.setdefault("core_binary", install_dir + "/oboard-sb")
data.setdefault("core_service", "oboard-sb")
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

if [ "$SERVICE_MANAGER" = systemd ]; then
  systemctl daemon-reload || true
  if [ -s "$STATE_DIR/sing-box.json" ]; then systemctl restart oboard-sb || true; fi
	if [ "$AGENT_RESTART" = none ]; then
		echo "Agent 将在任务结果回传后由当前进程安排重启。"
	elif [ "$AGENT_RESTART" = delayed ]; then
		restart_agent_delayed
  else
    systemctl restart oboard-agent || true
  fi
elif [ "$SERVICE_MANAGER" = openrc ]; then
  if [ -s "$STATE_DIR/sing-box.json" ]; then rc-service oboard-sb restart || true; fi
	if [ "$AGENT_RESTART" = none ]; then
		echo "Agent 将在任务结果回传后由当前进程安排重启。"
	elif [ "$AGENT_RESTART" = delayed ]; then
    restart_agent_delayed
  else
    rc-service oboard-agent restart || true
  fi
else
  echo "未识别服务管理器，只更新二进制。"
fi

verify_installed_versions || true
print_management_help
`, "__BASE_URL__", shellSingleQuote(baseURL))
	script = strings.ReplaceAll(script, "__RELEASE_PUBLIC_KEY__", shellSingleQuote(version.ReleasePublicKey))
	script = strings.ReplaceAll(script, "__AGENT_RELEASE_VERIFIER__", agentReleaseVerifierShell)
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
	case "oboard-agent-linux-amd64", "oboard-agent-linux-arm64", "oboard-sb-linux-amd64", "oboard-sb-linux-arm64", "release-manifest.json", "release-manifest.json.sig":
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
	write(w, status, map[string]any{"error": err.Error()})
}
func method(w http.ResponseWriter)                    { fail(w, errors.New("method not allowed"), 405) }
func notFound(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
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
