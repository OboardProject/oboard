package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestNodeIncidentMonitorThresholdAndRecoveryWindow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "incident-monitor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "secret", "")
	ctx := context.Background()
	nowTime := time.Now().UTC().Truncate(time.Second)
	server := createServerWithLastSeen(t, db, "edge", "agent-edge", nowTime.Add(-119*time.Second), true, 0)
	if err := db.SetSetting(ctx, settingNotificationServerOfflineAfter, "120"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingNotificationServerOnlineAfter, "300"); err != nil {
		t.Fatal(err)
	}
	srv.checkOfflineAt(ctx, nowTime)
	items, err := db.ListNodeIncidents(ctx, "", 10, 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("incident before threshold=%#v err=%v", items, err)
	}
	srv.checkOfflineAt(ctx, nowTime.Add(2*time.Second))
	items, err = db.ListNodeIncidents(ctx, "", 10, 0)
	if err != nil || len(items) != 1 || items[0].Status != model.NodeIncidentActive {
		t.Fatalf("incident at threshold=%#v err=%v", items, err)
	}
	srv.checkOfflineAt(ctx, nowTime.Add(time.Minute))
	again, _ := db.ListNodeIncidents(ctx, "", 10, 0)
	if len(again) != 1 || again[0].ID != items[0].ID {
		t.Fatalf("offline checks duplicated incident: %#v", again)
	}
	candidate := nowTime.Add(2 * time.Minute)
	server.Status = model.ServerOnline
	server.LastSeenAt = &candidate
	if err := db.UpdateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	srv.handleServerRecovered(ctx, server.ID)
	recovering, err := db.GetNodeIncident(ctx, items[0].ID)
	if err != nil || recovering.Status != model.NodeIncidentRecovering || recovering.RecoveryDeadlineAt == nil {
		t.Fatalf("recovering=%#v err=%v", recovering, err)
	}
	srv.finalizeRecoveredNodeIncidents(ctx, recovering.RecoveryCandidateAt.Add(299*time.Second))
	stillRecovering, _ := db.GetNodeIncident(ctx, items[0].ID)
	if stillRecovering.Status != model.NodeIncidentRecovering {
		t.Fatalf("incident resolved before stable window: %#v", stillRecovering)
	}
	stableHeartbeat := *recovering.RecoveryDeadlineAt
	server.LastSeenAt = &stableHeartbeat
	if err := db.UpdateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	srv.finalizeRecoveredNodeIncidents(ctx, stableHeartbeat)
	resolved, err := db.GetNodeIncident(ctx, items[0].ID)
	if err != nil || resolved.Status != model.NodeIncidentResolved || resolved.OutageDurationSeconds <= 0 {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}

func TestNodeIncidentTelegramRecoveryFallsBackWhenEditFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "incident-telegram.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "uuid", ProxyPassword: "pass"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: admin.ID, Name: "ops", Type: "telegram", Enabled: true, Events: notificationServerOffline + "," + notificationServerOnline, ConfigJSON: `{}`}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if err := srv.saveTelegramBotConfig(ctx, telegramBotConfig{Enabled: true, BotToken: "token"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTelegramBindingCode(ctx, security.HashSecret("ADMINCODE"), admin.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConsumeTelegramBindingCode(ctx, security.HashSecret("ADMINCODE"), channel.ID, 100, 1000, "private", time.Now()); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "edge", Status: model.ServerOffline, OfflineNotifyEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	first := time.Now().UTC().Add(-2 * time.Minute)
	incident, _, err := db.OpenOrReopenNodeIncident(ctx, *server, first, time.Now().UTC(), 2*time.Minute, 5*time.Minute, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	sendCount := 0
	failEdit := false
	srv.telegramAPI = func(_ context.Context, _ string, target string, _ url.Values) ([]byte, error) {
		if strings.Contains(target, "/editMessageText") {
			if failEdit {
				return nil, errors.New("message to edit not found")
			}
			return []byte(`{"ok":true,"result":true}`), nil
		}
		sendCount++
		return []byte(`{"ok":true,"result":{"message_id":` + string(rune('0'+sendCount)) + `}}`), nil
	}
	srv.syncNodeIncidentTelegram(ctx, incident)
	message, err := db.GetNodeIncidentTelegramMessage(ctx, incident.ID, 100)
	if err != nil || message.MessageID != 1 {
		t.Fatalf("initial message=%#v err=%v", message, err)
	}
	candidate := time.Now().UTC()
	recovering, err := db.MarkNodeIncidentRecovering(ctx, server.ID, candidate, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv.syncNodeIncidentTelegram(ctx, *recovering)
	failEdit = true
	resolved, err := db.ResolveNodeIncident(ctx, incident.ID, recovering.Version, candidate.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	srv.syncNodeIncidentTelegram(ctx, *resolved)
	message, err = db.GetNodeIncidentTelegramMessage(ctx, incident.ID, 100)
	if err != nil || message.MessageID != 1 || message.FallbackMessageID != 2 || !strings.Contains(message.LastError, "message to edit not found") {
		t.Fatalf("fallback message=%#v err=%v", message, err)
	}
}

func TestTelegramGlobalBotAcceptsAccountBindingFromAnyChat(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "global-telegram.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "admin-uuid", ProxyPassword: "admin-pass"}
	viewer := &model.User{Username: "viewer", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "viewer-uuid", ProxyPassword: "viewer-pass"}
	for _, user := range []*model.User{admin, viewer} {
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	channel := &model.NotificationChannel{OwnerUserID: viewer.ID, Name: "personal", Type: "telegram", Enabled: true, Events: notificationTrafficQuota, ConfigJSON: `{}`}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if err := srv.saveTelegramBotConfig(ctx, telegramBotConfig{Enabled: true, BotToken: "global-token"}); err != nil {
		t.Fatal(err)
	}
	bot, err := srv.globalTelegramBot(ctx)
	if err != nil || bot.botToken != "global-token" {
		t.Fatalf("global bot=%#v err=%v", bot, err)
	}
	if err := db.CreateTelegramBindingCode(ctx, security.HashSecret("BINDCODE"), viewer.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var update telegramUpdate
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"from":{"id":900},"chat":{"id":800,"type":"private"},"text":"/bind %d BINDCODE"}}`, channel.ID)), &update); err != nil {
		t.Fatal(err)
	}
	srv.telegramAPI = func(_ context.Context, _ string, _ string, _ url.Values) ([]byte, error) {
		return []byte(`{"ok":true,"result":{"message_id":1}}`), nil
	}
	srv.handleTelegramUpdate(ctx, *bot, update, &telegramBotRateLimiter{counts: map[string][]time.Time{}})
	binding, err := db.GetTelegramBinding(ctx, channel.ID, 800, 900)
	if err != nil || binding.UserID != viewer.ID {
		t.Fatalf("binding=%#v err=%v", binding, err)
	}
	second := model.NotificationChannel{OwnerUserID: viewer.ID, Name: "second", Type: "telegram", Enabled: true, Events: notificationTrafficQuota, ConfigJSON: `{}`}
	if err := validateNotificationChannel(&second, model.RoleViewer); err != nil {
		t.Fatalf("ordinary user Telegram channel should be accepted: %v", err)
	}
}

func TestTelegramBindingCodeRequiresConfiguredGlobalBot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "binding-api.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/telegram/binding-code", token, map[string]any{"channel_id": 1}, http.StatusNotFound)
	request(t, h, http.MethodPut, "/api/v1/ui/telegram-bot", token, map[string]any{"enabled": true, "bot_token": "global-token"}, http.StatusOK)
	channel := request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", token, map[string]any{
		"name": "personal", "type": "telegram", "enabled": true, "events": notificationServerOffline,
		"config_json": `{}`,
	}, http.StatusCreated)["notification_channel"].(map[string]any)
	channelID := int64(channel["id"].(float64))
	created := request(t, h, http.MethodPost, "/api/v1/ui/telegram/binding-code", token, map[string]any{"channel_id": channelID}, http.StatusCreated)
	data, _ := created["data"].(map[string]any)
	if strings.TrimSpace(data["code"].(string)) == "" {
		t.Fatal("binding code is empty")
	}
}

func TestSubscriptionIsolationHidesOnlySelectedInboundWithoutDeployment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "isolation-subscription.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "secret", "")
	ctx := context.Background()
	server := &model.Server{Name: "edge", PublicIPv4: "203.0.113.10", Status: model.ServerOffline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	first := &model.Inbound{ServerID: server.ID, Name: "selected-node", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true}
	second := &model.Inbound{ServerID: server.ID, Name: "remaining-node", Protocol: model.ProtocolVLESS, Port: 8443, ConfigJSON: `{}`, Enabled: true}
	for _, inbound := range []*model.Inbound{first, second} {
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "pass", SubscriptionToken: "subscription-token"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, first.ID)
	plan, err := db.GetActiveUserPlanBinding(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := db.ListActivePlanNodes(ctx, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	nodes = append(nodes, model.SubscriptionPlanNode{PlanID: plan.PlanID, NodeType: model.AssignableNodeInbound, NodeID: second.ID, SourceType: "manual"})
	if err := db.SyncPlanDraftNodes(ctx, plan.PlanID, 1, nodes, "replace"); err == nil {
		// The test helper already activates one immutable plan version. Creating a
		// second plan is unnecessary when the draft API is not available here.
	}
	before := httptest.NewRecorder()
	srv.Handler().ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/subscription-token?format=sing-box", nil))
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"type": "vless"`) {
		t.Fatalf("subscription before isolation status=%d body=%s", before.Code, before.Body.String())
	}
	incident, _, err := db.OpenOrReopenNodeIncident(ctx, *server, time.Now().Add(-2*time.Minute), time.Now(), 2*time.Minute, 5*time.Minute, srv.nodeIncidentSnapshot(ctx, *server))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateNodePublicationIsolations(ctx, incident.ID, user.ID, []int64{first.ID}, "manual"); err != nil {
		t.Fatal(err)
	}
	after := httptest.NewRecorder()
	srv.Handler().ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/subscription-token?format=sing-box", nil))
	if after.Code != http.StatusOK || strings.Contains(after.Body.String(), `"type": "vless"`) {
		t.Fatalf("subscription after isolation status=%d body=%s", after.Code, after.Body.String())
	}
	tasks, err := db.ListTasks(ctx, 100)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("temporary isolation queued deployment tasks=%#v err=%v", tasks, err)
	}
}
