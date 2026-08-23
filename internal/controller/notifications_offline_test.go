package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func createServerWithLastSeen(t *testing.T, db *store.Store, name, agentID string, lastSeen time.Time, notifyEnabled bool, offlineAfterSeconds int) model.Server {
	t.Helper()
	server := model.Server{Name: name, AgentID: agentID, Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, OfflineNotifyEnabled: notifyEnabled, OfflineAfterSeconds: offlineAfterSeconds}
	if err := db.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	server.LastSeenAt = &lastSeen
	if err := db.UpdateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *stored
}

func TestOfflineNoticeMergedAndPerServerDisable(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 3, 7, 0, 0, time.UTC)
	stale := now.Add(-10 * time.Minute)
	serverA := createServerWithLastSeen(t, db, "香港-01", "agent-hk", stale, true, 0)
	serverB := createServerWithLastSeen(t, db, "日本-01", "agent-jp", stale, true, 0)
	createServerWithLastSeen(t, db, "禁用-01", "agent-off", stale, false, 0)
	createServerWithLastSeen(t, db, "在线-01", "agent-online", now.Add(-30*time.Second), true, 0)

	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title+"\n"+body)
		return nil
	}
	admin := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "admin-uuid", ProxyPassword: "admin-pass"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{Name: "ops", Type: "telegram", Enabled: true, Events: notificationServerOffline, ConfigJSON: `{}`, OwnerUserID: admin.ID}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	bindTestTelegramChannel(t, srv, db, channel.ID, 1)

	srv.checkOfflineAt(ctx, now)

	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statusByName := map[string]model.ServerStatus{}
	for _, server := range servers {
		statusByName[server.Name] = server.Status
	}
	if statusByName["香港-01"] != model.ServerOffline || statusByName["日本-01"] != model.ServerOffline || statusByName["禁用-01"] != model.ServerOffline {
		t.Fatalf("stale servers should be offline: %#v", statusByName)
	}
	if statusByName["在线-01"] != model.ServerOnline {
		t.Fatalf("fresh server should stay online: %#v", statusByName)
	}

	pending, err := db.ListDueOfflineNotices(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected two pending offline notices, got %#v", pending)
	}
	seenEnabled := map[int64]bool{}
	for _, item := range pending {
		seenEnabled[item.ServerID] = true
		if strings.Contains(item.ServerName, "禁用") {
			t.Fatalf("disabled server must not have a notice: %#v", pending)
		}
	}
	if !seenEnabled[serverA.ID] || !seenEnabled[serverB.ID] {
		t.Fatalf("both enabled servers should have notices: %#v", pending)
	}
	if pending[0].GroupKey == "" || pending[0].GroupKey != pending[1].GroupKey {
		t.Fatalf("simultaneous failures should share a merge group: %#v", pending)
	}

	// Force the group window to end and deliver the merged notification.
	past := now.Add(-time.Second)
	for _, item := range pending {
		if err := db.UpsertServerOfflineNotice(ctx, item.ServerID, store.ServerOfflineNoticeStatusOffline, item.SinceAt, past, item.GroupKey); err != nil {
			t.Fatal(err)
		}
	}
	srv.fireDueOfflineNotices(ctx, true, now)
	waitNotificationCount(t, srv, &sentMu, &sent, 1)
	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("expected one merged offline notification, got %d: %#v", len(sent), sent)
	}
	message := sent[0]
	if !strings.Contains(message, "香港-01") || !strings.Contains(message, "日本-01") {
		t.Fatalf("merged notification should include both servers: %q", message)
	}
	if strings.Contains(message, "禁用-01") {
		t.Fatalf("disabled server must not be notified: %q", message)
	}
}

func TestOfflineNoticeGroupExtendsAcrossWindows(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 3, 7, 0, 0, time.UTC)
	short := createServerWithLastSeen(t, db, "短窗-01", "agent-short", now.Add(-3*time.Minute), true, 0)
	long := createServerWithLastSeen(t, db, "长窗-01", "agent-long", now.Add(-time.Minute), true, 300)

	// First pass: only the default-window server is marked offline.
	srv.checkOfflineAt(ctx, now)
	pending, err := db.ListDueOfflineNotices(ctx, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ServerID != short.ID {
		t.Fatalf("expected only the short-window server pending, got %#v", pending)
	}

	// Long-window server goes stale later (same failure bucket); its insertion
	// must extend the group so both are delivered in one merged message.
	staleLater := now.Add(-5 * time.Minute)
	long.LastSeenAt = &staleLater
	if err := db.UpdateServer(ctx, &long); err != nil {
		t.Fatal(err)
	}
	srv.checkOfflineAt(ctx, now)
	due, err := db.ListDueOfflineNotices(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("expected both servers due in the merged group, got %#v", due)
	}
	if due[0].GroupKey == "" || due[0].GroupKey != due[1].GroupKey {
		t.Fatalf("both notices should share a merge group: %#v", due)
	}
	groupNotify := due[0].NotifyAt
	for _, item := range due {
		if !item.NotifyAt.Equal(groupNotify) {
			t.Fatalf("group members should share notify_at: %#v", due)
		}
	}
}

func TestServerRecoveryCancelsOfflineAndDelaysOnline(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	server := createServerWithLastSeen(t, db, "恢复-01", "agent-recover", time.Now().UTC().Add(-10*time.Minute), true, 0)
	now := time.Now().UTC()
	if err := db.UpsertServerOfflineNotice(ctx, server.ID, store.ServerOfflineNoticeStatusOffline, now.Add(-2*time.Minute), now.Add(-time.Minute), "group"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingNotificationServerOnlineAfter, "60"); err != nil {
		t.Fatal(err)
	}
	srv.handleServerRecovered(ctx, server.ID)

	offline, err := db.ListDueOfflineNotices(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(offline) != 0 {
		t.Fatalf("pending offline notice should be cancelled after recovery, got %#v", offline)
	}
	onlineNow, err := db.ListDueOnlineNotices(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlineNow) != 0 {
		t.Fatalf("online notice should not fire before its window, got %#v", onlineNow)
	}
	onlineLater, err := db.ListDueOnlineNotices(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(onlineLater) != 1 || onlineLater[0].ServerID != server.ID {
		t.Fatalf("expected one due online notice after the window, got %#v", onlineLater)
	}
}

func TestSubscriptionAbnormalNotificationQueuesOncePerHour(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	createdChannel := request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", adminToken, map[string]any{"name": "sub", "type": "telegram", "enabled": true, "events": notificationSubscriptionAbnormal, "config_json": `{}`}, http.StatusCreated)["notification_channel"].(map[string]any)
	bindTestTelegramChannel(t, srv, db, int64(createdChannel["id"].(float64)), 1)
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-uuid", ProxyPassword: "alice-pass", SubscriptionToken: "sub-token"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}

	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, _ string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title)
		return nil
	}
	for i := 0; i < 3; i++ {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/sub-token?format=bogus", nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("pull %d status = %d", i, recorder.Code)
		}
	}
	waitNotificationCount(t, srv, &sentMu, &sent, 1)
	sentMu.Lock()
	if !strings.Contains(sent[0], "alice") {
		t.Fatalf("subscription abnormal title missing user: %q", sent[0])
	}
	sentMu.Unlock()

	// A fourth abnormal pull within the same hour must not queue again.
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/sub-token?format=bogus", nil))
	time.Sleep(200 * time.Millisecond)
	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("dedup failed, got %d notifications", len(sent))
	}
}

func TestBarkNotificationTargetIncludesGroup(t *testing.T) {
	target := barkNotificationTarget("https://api.day.app", "device-1", "OBoard 提醒", "标题", "正文")
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("group") != "OBoard 提醒" {
		t.Fatalf("group query missing: %s", target)
	}
	if !strings.HasSuffix(parsed.Path, "/device-1/标题/正文") {
		t.Fatalf("unexpected bark path: %s", parsed.Path)
	}
	plain := barkNotificationTarget("https://api.day.app/", "device-1", "  ", "标题", "正文")
	if strings.Contains(plain, "?") {
		t.Fatalf("blank group must not add query: %s", plain)
	}
}

func TestBarkGroupValidationInChannelConfig(t *testing.T) {
	channel := &model.NotificationChannel{Name: "phone", Type: "bark", Events: notificationServerOffline, ConfigJSON: `{"device_key":"abc","group":"提醒分类"}`}
	if err := validateNotificationChannel(channel, model.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(channel.ConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["group"] != "提醒分类" {
		t.Fatalf("group not persisted: %s", channel.ConfigJSON)
	}
	if cfg["server_url"] != "https://api.day.app" {
		t.Fatalf("default server url missing: %s", channel.ConfigJSON)
	}
}

func TestTelegramNotificationChannelUsesGlobalBotConfiguration(t *testing.T) {
	channel := &model.NotificationChannel{Name: "personal-bot", Type: "telegram", Events: notificationTrafficQuota, ConfigJSON: `{"bot_token":"must-not-remain","chat_id":"2"}`}
	if err := validateNotificationChannel(channel, model.RoleViewer); err != nil {
		t.Fatalf("ordinary user Telegram channel rejected: %v", err)
	}
	if channel.ConfigJSON != "{}" {
		t.Fatalf("Telegram channel retained Bot credentials: %s", channel.ConfigJSON)
	}
}

func TestTelegramBotReplyCommands(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	now := time.Now().UTC()
	createServerWithLastSeen(t, db, "香港-01", "agent-hk", now.Add(-time.Minute), true, 0)
	createServerWithLastSeen(t, db, "日本-01", "agent-jp", now.Add(-30*time.Minute), true, 0)
	user := &model.User{Username: "alice", Nickname: "爱丽丝", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-uuid", ProxyPassword: "alice-pass", TrafficLimitBytes: 10 << 30, TrafficUsedBytes: 2 << 30}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	status := srv.telegramBotReply(ctx, "/status")
	if !strings.Contains(status, "香港-01") || !strings.Contains(status, "在线") {
		t.Fatalf("status reply missing server: %q", status)
	}
	detail := srv.telegramBotReply(ctx, "/server 香港-01")
	if !strings.Contains(detail, "香港-01") || !strings.Contains(detail, "ID") {
		t.Fatalf("server detail reply missing info: %q", detail)
	}
	detailByID := srv.telegramBotReply(ctx, "/server 1")
	if !strings.Contains(detailByID, "香港-01") {
		t.Fatalf("server detail by id failed: %q", detailByID)
	}
	traffic := srv.telegramBotReply(ctx, "/traffic")
	if !strings.Contains(traffic, "周期流量") || !strings.Contains(traffic, "香港-01") {
		t.Fatalf("traffic reply missing info: %q", traffic)
	}
	users := srv.telegramBotReply(ctx, "/users")
	if !strings.Contains(users, "用户流量") || !strings.Contains(users, "爱丽丝") {
		t.Fatalf("users reply missing info: %q", users)
	}
	audit := srv.telegramBotReply(ctx, "/audit")
	if !strings.Contains(audit, "审计概览") {
		t.Fatalf("audit reply missing info: %q", audit)
	}
	help := srv.telegramBotReply(ctx, "/help")
	if !strings.HasPrefix(help, "OBoard 运维指令\n") || !strings.Contains(help, "/status") {
		t.Fatalf("help reply missing commands: %q", help)
	}
	for _, symbol := range []string{"🤖", "📡", "🖥", "📊", "👥", "🛡", "🟢", "🔴", "🟡", "⚪"} {
		if strings.Contains(status+detail+traffic+users+audit+help, symbol) {
			t.Fatalf("Telegram Bot copy contains decorative symbol %q", symbol)
		}
	}
	unknown := srv.telegramBotReply(ctx, "/whatever")
	if !strings.Contains(unknown, "/help") {
		t.Fatalf("unknown command should point to help: %q", unknown)
	}
}

func TestOfflineNoticeSettingsClamp(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSetting(ctx, settingNotificationServerOfflineAfter, "100000"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingNotificationServerOnlineAfter, "999999"); err != nil {
		t.Fatal(err)
	}
	settings, err := db.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	offline := settingInt(settings, settingNotificationServerOfflineAfter, defaultNotificationOfflineAfterSeconds, 30, 86400)
	if offline != defaultNotificationOfflineAfterSeconds {
		t.Fatalf("out-of-range offline setting should fall back, got %d", offline)
	}
	online := settingInt(settings, settingNotificationServerOnlineAfter, defaultNotificationOnlineAfterSeconds, 0, 86400)
	if online != defaultNotificationOnlineAfterSeconds {
		t.Fatalf("out-of-range online setting should fall back, got %d", online)
	}
}

func TestServerPatchKeepsOfflineNotifyDisabled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "quiet", "offline_notify_enabled": false}, http.StatusCreated)
	id := int64(created["server"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPatch, "/api/v1/ui/servers/"+strconv.FormatInt(id, 10), token, map[string]any{"name": "quiet", "offline_notify_enabled": false, "offline_after_seconds": 0}, http.StatusOK)
	request(t, h, http.MethodPatch, "/api/v1/ui/servers/"+strconv.FormatInt(id, 10), token, map[string]any{"name": "quiet-renamed"}, http.StatusOK)
	server, err := db.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if server.OfflineNotifyEnabled {
		t.Fatalf("unrelated patch must not re-enable offline notifications: %#v", server)
	}
}
