package controller

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginsandbox"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// pluginWorkerState is the last heartbeat of the plugin worker.
type pluginWorkerState struct {
	mu        sync.Mutex
	lastSeen  time.Time
	isolation pluginsandbox.IsolationStatus
}

func (w *pluginWorkerState) record(isolation pluginsandbox.IsolationStatus) {
	w.mu.Lock()
	w.lastSeen, w.isolation = time.Now(), isolation
	w.mu.Unlock()
}

func (w *pluginWorkerState) snapshot() (bool, pluginsandbox.IsolationStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.lastSeen.IsZero() && time.Since(w.lastSeen) < 30*time.Second, w.isolation
}

// pluginHost exposes existing, validated Controller services to the plugin
// gateway. Nothing here accepts a raw Agent task, command or REST call.
type pluginHost struct {
	server *Server

	denyMu      sync.Mutex
	denyUntil   time.Time
	denyAddress map[netip.Addr]bool
}

func (h *pluginHost) Server(ctx context.Context, id int64) (plugin.ServerInfo, bool) {
	server, err := h.server.store.GetServer(ctx, id)
	if err != nil || server == nil {
		return plugin.ServerInfo{}, false
	}
	return h.serverInfo(ctx, server), true
}

func (h *pluginHost) serverInfo(ctx context.Context, server *model.Server) plugin.ServerInfo {
	info := plugin.ServerInfo{
		ID: server.ID, Name: server.Name, Status: string(server.Status), RegionCode: server.RegionCode,
		PublicIPv4: server.PublicIPv4, PublicIPv6: server.PublicIPv6, Enrolled: strings.TrimSpace(server.AgentID) != "",
		Online: h.server.agentControlOnline(server.ID), AgentVersion: server.AgentVersion, LastSeenAt: server.LastSeenAt,
		Capabilities: append([]string(nil), server.KernelCapabilities...),
	}
	if status, err := h.server.store.GetServerRemoteAccessStatus(ctx, server.ID); err == nil {
		info.PluginsGate = status.LocalAllow.PluginsEnabled
	}
	return info
}

func (h *pluginHost) ListServers(ctx context.Context) []plugin.ServerInfo {
	servers, err := h.server.store.ListServers(ctx)
	if err != nil {
		return nil
	}
	out := make([]plugin.ServerInfo, 0, len(servers))
	for i := range servers {
		out = append(out, h.serverInfo(ctx, &servers[i]))
	}
	return out
}

func (h *pluginHost) ServerHealth(ctx context.Context, id int64) (map[string]any, error) {
	server, err := h.server.store.GetServer(ctx, id)
	if err != nil || server == nil {
		return nil, plugin.Fail(plugin.CodeServerNotFound, "server no longer exists")
	}
	now := time.Now().UTC()
	stale := true
	lastSeen := ""
	if server.LastSeenAt != nil {
		lastSeen = server.LastSeenAt.UTC().Format(time.RFC3339)
		stale = now.Sub(server.LastSeenAt.UTC()) > 3*time.Minute
	}
	sync := "unknown"
	if state, err := h.server.store.ConfigurationSyncState(ctx, server.ID); err == nil && state.State != "" {
		sync = state.State
	}
	return map[string]any{
		"server_id":           strconv.FormatInt(server.ID, 10),
		"status":              string(server.Status),
		"agent_connected":     h.server.agentControlOnline(server.ID),
		"last_seen_at":        lastSeen,
		"stale":               stale,
		"config_sync":         sync,
		"connectivity_status": server.ConnectivityStatus,
		"agent_version":       server.AgentVersion,
		"core_version":        server.SingBoxVersion,
		"expired":             server.ExpiresAt != nil && !server.ExpiresAt.After(now),
		"observed_at":         now.Format(time.RFC3339),
	}, nil
}

func (h *pluginHost) ServerMetrics(ctx context.Context, id int64) (map[string]any, error) {
	samples, err := h.server.store.ListServerMetricSamples(ctx, id, 1)
	if err != nil {
		return nil, plugin.Fail(plugin.CodeInternal, "metrics unavailable")
	}
	view := map[string]any{"server_id": strconv.FormatInt(id, 10), "exists": len(samples) > 0}
	if len(samples) == 0 {
		view["stale"] = true
		return view, nil
	}
	sample := samples[0]
	view["stale"] = time.Since(sample.SampledAt) > 3*time.Minute
	view["observed_at"] = sample.SampledAt.UTC().Format(time.RFC3339)
	view["cpu_usage_percent"] = sample.CPUUsagePercent
	view["memory_used_bytes"] = sample.MemoryUsedBytes
	view["memory_total_bytes"] = sample.MemoryTotalBytes
	view["disk_used_bytes"] = sample.DiskUsedBytes
	view["disk_total_bytes"] = sample.DiskTotalBytes
	view["tcp_connections"] = sample.TCPConnectionCount
	view["udp_connections"] = sample.UDPConnectionCount
	view["network_upload_bps"] = sample.NetworkUploadBPS
	view["network_download_bps"] = sample.NetworkDownloadBPS
	return view, nil
}

func (h *pluginHost) NotificationChannelExists(ctx context.Context, id int64) bool {
	channel, err := h.server.store.GetNotificationChannel(ctx, id)
	return err == nil && channel != nil
}

func (h *pluginHost) SendNotification(ctx context.Context, channelID int64, title, body string) error {
	channel, err := h.server.store.GetNotificationChannel(ctx, channelID)
	if err != nil || channel == nil {
		return plugin.Fail(plugin.CodeResourceDenied, "notification channel no longer exists")
	}
	sender := h.server.notificationSender
	if sender == nil {
		sender = sendNotification
	}
	return sender(ctx, *channel, title, body)
}

func pluginUserInfo(user *model.User) plugin.UserInfo {
	return plugin.UserInfo{ID: user.ID, Username: user.Username, Nickname: user.Nickname, Status: user.Status}
}

func (h *pluginHost) User(ctx context.Context, id int64) (plugin.UserInfo, bool) {
	user, err := h.server.store.GetUser(ctx, id)
	if err != nil || user == nil {
		return plugin.UserInfo{}, false
	}
	info := pluginUserInfo(user)
	return info, true
}

func (h *pluginHost) ListUsers(ctx context.Context) []plugin.UserInfo {
	users, err := h.server.store.ListUsers(ctx)
	if err != nil {
		return nil
	}
	out := make([]plugin.UserInfo, 0, len(users))
	for i := range users {
		out = append(out, pluginUserInfo(&users[i]))
	}
	return out
}

func (h *pluginHost) Plan(ctx context.Context, id int64) (plugin.PlanInfo, bool) {
	plan, err := h.server.store.GetSubscriptionPlan(ctx, id)
	if err != nil || plan == nil {
		return plugin.PlanInfo{}, false
	}
	return plugin.PlanInfo{ID: plan.ID, Name: plan.Name, Enabled: plan.Enabled}, true
}

func (h *pluginHost) ListPlans(ctx context.Context) []plugin.PlanInfo {
	plans, err := h.server.store.ListSubscriptionPlans(ctx)
	if err != nil {
		return nil
	}
	out := make([]plugin.PlanInfo, 0, len(plans))
	for _, plan := range plans {
		out = append(out, plugin.PlanInfo{ID: plan.ID, Name: plan.Name, Enabled: plan.Enabled})
	}
	return out
}

func (h *pluginHost) UsersOnPlan(ctx context.Context, planID int64) ([]plugin.UserInfo, error) {
	bindings, err := h.server.store.ListUserPlanBindingsForPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	users, err := h.server.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	member := map[int64]bool{}
	for _, binding := range bindings {
		if binding.StartsAt != nil && binding.StartsAt.After(now) || binding.ExpiresAt != nil && !binding.ExpiresAt.After(now) {
			continue
		}
		member[binding.UserID] = true
	}
	out := []plugin.UserInfo{}
	for i := range users {
		if member[users[i].ID] {
			out = append(out, pluginUserInfo(&users[i]))
		}
	}
	return out, nil
}

func (h *pluginHost) NotifyUsers(ctx context.Context, request plugin.UserNotifyRequest) (plugin.UserNotifyResult, error) {
	if _, err := h.server.globalTelegramBot(ctx); err != nil {
		return plugin.UserNotifyResult{}, plugin.Fail(plugin.CodeOperationFailed, "还没有可用的 Telegram 机器人，无法向用户推送")
	}
	if request.ActorUserID <= 0 {
		return plugin.UserNotifyResult{}, plugin.Fail(plugin.CodeOperationFailed, "缺少授权管理员，无法向用户推送")
	}
	actor, err := h.server.store.GetUser(ctx, request.ActorUserID)
	if err != nil || actor == nil {
		return plugin.UserNotifyResult{}, plugin.Fail(plugin.CodeOperationFailed, "授权管理员已不存在，无法向用户推送")
	}
	actorName := strings.TrimSpace(actor.Nickname)
	if actorName == "" {
		actorName = actor.Username
	}
	recipients := make([]store.BroadcastRecipient, 0, len(request.UserIDs))
	result := plugin.UserNotifyResult{}
	for _, userID := range request.UserIDs {
		bindings, err := h.server.store.ListTelegramBindingsForUser(ctx, userID)
		if err != nil {
			return plugin.UserNotifyResult{}, plugin.Fail(plugin.CodeOperationFailed, "读取用户通知绑定失败")
		}
		recipients = append(recipients, store.BroadcastRecipient{UserID: userID, Bindings: bindings})
		result.Recipients++
		result.Queued += len(bindings)
		if len(bindings) == 0 {
			result.Unbound++
		}
	}
	filter, _ := json.Marshal(map[string]any{"source": "plugin", "user_ids": request.UserIDs})
	broadcast := model.NotificationBroadcast{ActorUserID: actor.ID, ActorName: actorName, Title: request.Title, Body: request.Body, FilterJSON: string(filter), IdempotencyKey: request.IdempotencyKey}
	created, err := h.server.store.CreateNotificationBroadcast(ctx, &broadcast, recipients)
	if err != nil {
		return plugin.UserNotifyResult{}, plugin.Fail(plugin.CodeOperationFailed, "用户通知没有进入发送队列")
	}
	if created {
		announcement := model.NotificationAnnouncement{ActorUserID: actor.ID, ActorName: actorName, Title: request.Title, Body: request.Body, UserIDs: append([]int64(nil), request.UserIDs...), QueuedCount: result.Queued}
		if err := h.server.store.CreateNotificationAnnouncement(ctx, &announcement); err != nil {
			return plugin.UserNotifyResult{}, plugin.Fail(plugin.CodeOperationFailed, "用户通知没有进入发送队列")
		}
		h.server.wakeNotificationDelivery(ctx)
	}
	return result, nil
}

// HTTPDenied refuses the Controller's own addresses and every enrolled
// node's addresses in addition to the public-address rule, so http.request
// can never be turned against OBoard itself.
func (h *pluginHost) HTTPDenied(ctx context.Context, ip netip.Addr) bool {
	h.denyMu.Lock()
	defer h.denyMu.Unlock()
	if time.Now().After(h.denyUntil) {
		denied := map[netip.Addr]bool{}
		if addresses, err := net.InterfaceAddrs(); err == nil {
			for _, address := range addresses {
				if prefix, err := netip.ParsePrefix(address.String()); err == nil {
					denied[prefix.Addr().Unmap()] = true
				}
			}
		}
		if servers, err := h.server.store.ListServers(ctx); err == nil {
			for _, server := range servers {
				for _, raw := range []string{server.PublicIPv4, server.PublicIPv6, server.InterfaceIPv6} {
					if addr, err := netip.ParseAddr(strings.TrimSpace(raw)); err == nil {
						denied[addr.Unmap()] = true
					}
				}
			}
		}
		h.denyAddress, h.denyUntil = denied, time.Now().Add(time.Minute)
	}
	return h.denyAddress[ip.Unmap()]
}

func (h *pluginHost) EncryptSecret(plain string) (string, error) {
	return security.EncryptSecret(h.server.sessionSecret, "plugin-secret", plain)
}

func (h *pluginHost) DecryptSecret(encrypted string) (string, error) {
	return security.DecryptSecret(h.server.sessionSecret, "plugin-secret", encrypted)
}

// ResolveCaller re-resolves whoever started a manual run with their current
// status, role and scope; a removed, disabled or narrowed caller stops the
// run's authority immediately.
func (h *pluginHost) ResolveCaller(ctx context.Context, ref plugin.CallerRef) (application.Principal, error) {
	s := h.server
	ip, _ := netip.ParseAddr(ref.SourceIP)
	denied := plugin.Fail(plugin.CodeCapabilityDenied, "caller no longer authorized")
	if raw, human := strings.CutPrefix(ref.PrincipalID, "user:"); human && ref.GrantID == "" && ref.Type == string(model.APIPrincipalOAuth) {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return application.Principal{}, denied
		}
		user, err := s.store.GetUser(ctx, id)
		if err != nil || user == nil || user.Status != "active" {
			return application.Principal{}, denied
		}
		role, err := s.store.EffectiveUserRole(ctx, *user)
		if err != nil || role == model.RoleNone {
			return application.Principal{}, denied
		}
		return application.HumanPrincipal(*user, role, ip), nil
	}
	if ref.GrantID != "" && ref.Type == string(model.APIPrincipalOAuth) {
		grant, role, active, err := s.store.ResolveActiveGrant(ctx, ref.GrantID, time.Now().UTC())
		if err != nil || !active || role == model.RoleNone || grant.PrincipalID != ref.PrincipalID {
			return application.Principal{}, denied
		}
		client, err := s.store.GetOAuthClient(ctx, grant.ClientID)
		if err != nil || !client.Enabled {
			return application.Principal{}, denied
		}
		if client.IdentityType == "cimd" {
			if err := s.refreshClientMetadataIfStale(ctx, client); err != nil {
				return application.Principal{}, denied
			}
		}
		boundary := s.oauthRoleBoundary(role)
		policy := &mcpauth.GrantPolicy{GrantID: grant.ID, ClientID: grant.ClientID, UserID: strconv.FormatInt(grant.UserID, 10), PrincipalID: grant.PrincipalID, AccessLevel: mcpAccessLevelForRole(role), ResourceBoundary: boundary, ApprovalProfile: grant.ApprovalProfileID, ApprovalMaxRisk: grantApprovalMaxRisk(grant), OfflineAccess: grant.OfflineAccess, PolicyVersion: grant.PolicyVersion, RoleVersion: grant.RoleVersion, ConsentVersion: grant.ConsentVersion, IssuedAt: grant.CreatedAt, ExpiresAt: grant.ExpiresAt, RevokedAt: grant.RevokedAt}
		caller := application.Principal{ID: grant.PrincipalID, GrantID: grant.ID, UserID: &grant.UserID, Type: model.APIPrincipalOAuth, Role: role, AccessLevel: policy.AccessLevel, GrantPolicy: policy, ResourceFilter: application.ResourceFilterFromBoundary(boundary), SourceIP: ip, ClientName: client.Name}
		caller.Scopes = s.capabilities.ScopesForGrant(caller)
		return caller, nil
	}
	if ref.Type != string(model.APIPrincipalServiceAccount) {
		return application.Principal{}, denied
	}
	stored, err := s.store.GetAPIPrincipal(ctx, ref.PrincipalID)
	if err != nil || !stored.Enabled || stored.Type != model.APIPrincipalServiceAccount || stored.ExpiresAt != nil && !stored.ExpiresAt.After(time.Now()) || !principalIPAllowed(stored.AllowedCIDRs, ip) {
		return application.Principal{}, denied
	}
	return application.Principal{ID: stored.ID, UserID: stored.OwnerUserID, Name: stored.Name, Type: stored.Type, Scopes: stored.Scopes, ResourceFilter: stored.ResourceFilter, SourceIP: ip}, nil
}

func (h *pluginHost) RuntimeStatus() plugin.RuntimeHostStatus {
	connected, isolation := h.server.pluginWorker.snapshot()
	installed := connected
	if !installed {
		for _, path := range []string{"/etc/systemd/system/oboard-plugin-worker.service", "/etc/init.d/oboard-plugin-worker"} {
			if _, err := os.Stat(path); err == nil {
				installed = true
			}
		}
	}
	return plugin.RuntimeHostStatus{Installed: installed, InstallCommand: pluginRuntimeInstallCommand(), WorkerConnected: connected, IsolationAvailable: isolation.Available, IsolationMode: isolation.Mode, IsolationReason: isolation.Reason}
}

func pluginRuntimeInstallCommand() string {
	channel := strings.ToLower(strings.TrimSpace(os.Getenv("OBOARD_UPDATE_CHANNEL")))
	version := "latest"
	switch channel {
	case "dev", "development", "nightly":
		version = "dev"
	case "pinned":
		if pinned := strings.TrimSpace(os.Getenv("OBOARD_VERSION")); pinned != "" {
			version = pinned
		}
	}
	return "curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/plugins/install.sh | sudo env OBOARD_ACTION=enable-plugins VERSION=" + version + " sh"
}
