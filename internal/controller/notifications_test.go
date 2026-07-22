package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type sequenceNotificationResolver struct {
	responses [][]net.IPAddr
	err       error
	calls     int
}

func (r *sequenceNotificationResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	if r.err != nil {
		return nil, r.err
	}
	index := r.calls
	r.calls++
	if index >= len(r.responses) {
		index = len(r.responses) - 1
	}
	if index < 0 {
		return nil, nil
	}
	return r.responses[index], nil
}

type recordingNotificationDialer struct {
	addresses []string
}

func (d *recordingNotificationDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.addresses = append(d.addresses, address)
	return nil, errors.New("dial stopped by test")
}

func TestValidateNotificationChannelRequiresTypedConfig(t *testing.T) {
	telegram := &model.NotificationChannel{Name: "ops", Type: "telegram", Events: "server_offline", ConfigJSON: `{"bot_token":"","chat_id":""}`}
	if err := validateNotificationChannel(telegram, model.RoleAdmin); err == nil || !strings.Contains(err.Error(), "Telegram") {
		t.Fatalf("expected telegram validation error, got %v", err)
	}
	telegram.ConfigJSON = `{"bot_token":"tok","chat_id":"-1001"}`
	if err := validateNotificationChannel(telegram, model.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if telegram.Events != "server_offline" {
		t.Fatalf("events = %q", telegram.Events)
	}

	bark := &model.NotificationChannel{Name: "phone", Type: "bark", Events: "server_online,server_offline,server_offline", ConfigJSON: `{"device_key":"abc"}`}
	if err := validateNotificationChannel(bark, model.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if bark.Events != "server_online,server_offline" {
		t.Fatalf("normalized events = %q", bark.Events)
	}
	if !strings.Contains(bark.ConfigJSON, "api.day.app") {
		t.Fatalf("bark default server_url missing: %s", bark.ConfigJSON)
	}
}

func TestNotificationTemplateValidationIsEventScoped(t *testing.T) {
	channel := &model.NotificationChannel{
		Name:       "self",
		Type:       "telegram",
		Events:     notificationTrafficQuota,
		ConfigJSON: `{"bot_token":"tok","chat_id":"1"}`,
		TemplatesJSON: mustJSON(t, map[string]model.NotificationTemplate{
			notificationTrafficQuota: {Title: "{{.ServerName}}", Body: "{{.Used}}"},
		}),
	}
	if err := validateNotificationChannel(channel, model.RoleViewer); err == nil || !strings.Contains(err.Error(), "ServerName") {
		t.Fatalf("wrong-event variable was accepted: %v", err)
	}
	channel.TemplatesJSON = mustJSON(t, map[string]model.NotificationTemplate{
		notificationTrafficQuota: {Title: "流量 · {{.UserName}}", Body: "{{.Used}} / {{.Limit}}"},
	})
	if err := validateNotificationChannel(channel, model.RoleViewer); err != nil {
		t.Fatal(err)
	}
	var templates map[string]model.NotificationTemplate
	if err := json.Unmarshal([]byte(channel.TemplatesJSON), &templates); err != nil {
		t.Fatal(err)
	}
	if len(templates) != 2 || templates[notificationAdminAnnouncement].Title == "" {
		t.Fatalf("viewer default template set = %#v", templates)
	}
}

func TestNotificationChannelCRUDAndTestValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	// Reject incomplete create.
	bad := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"name": "ops", "type": "telegram", "enabled": true, "events": "server_offline", "config_json": `{"bot_token":"","chat_id":""}`})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/notification-channels", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("incomplete create status = %d body=%s", bad.Code, bad.Body.String())
	}

	created := request(t, h, http.MethodPost, "/api/v1/notification-channels", token, map[string]any{
		"name": "ops-tg", "type": "telegram", "enabled": true, "events": "server_offline,server_online",
		"config_json": `{"bot_token":"123:abc","chat_id":"-1001"}`,
	}, http.StatusCreated)
	id := int64(created["notification_channel"].(map[string]any)["id"].(float64))
	if id == 0 {
		t.Fatalf("missing id: %#v", created)
	}

	// Test endpoint should fail transport (invalid token) but route must exist and validate.
	testRec := httptest.NewRecorder()
	testReq := httptest.NewRequest(http.MethodPost, "/api/v1/notification-channels/"+itoa(id)+"/test", bytes.NewReader([]byte("{}")))
	testReq.Header.Set("content-type", "application/json")
	testReq.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(testRec, testReq)
	if testRec.Code != http.StatusBadGateway && testRec.Code != http.StatusOK {
		// Upstream telegram will usually return non-2xx for fake token -> 502 from our handler.
		t.Fatalf("test channel status = %d body=%s", testRec.Code, testRec.Body.String())
	}

	draftRec := httptest.NewRecorder()
	draftBody, _ := json.Marshal(map[string]any{
		"name": "draft", "type": "bark", "enabled": true, "events": "server_offline",
		"config_json": `{"device_key":""}`,
	})
	draftReq := httptest.NewRequest(http.MethodPost, "/api/v1/notification-channels/test", bytes.NewReader(draftBody))
	draftReq.Header.Set("content-type", "application/json")
	draftReq.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(draftRec, draftReq)
	if draftRec.Code != http.StatusBadRequest {
		t.Fatalf("draft test without device_key status = %d body=%s", draftRec.Code, draftRec.Body.String())
	}

	admin, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := db.ListNotificationChannelsByOwner(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "ops-tg" {
		t.Fatalf("listed channels = %#v", listed)
	}
}

func TestNotificationChannelSecretsAreRedactedOnRead(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/notification-channels", token, map[string]any{
		"name": "ops-tg", "type": "telegram", "enabled": true, "events": "server_offline",
		"config_json": `{"bot_token":"123:abc","chat_id":"-1001"}`,
	}, http.StatusCreated)
	channel := created["notification_channel"].(map[string]any)
	if !strings.Contains(fmt.Sprint(channel["config_json"]), "********") {
		t.Fatalf("create response should redact bot_token: %#v", channel)
	}
	if strings.Contains(fmt.Sprint(channel["config_json"]), "123:abc") {
		t.Fatalf("create response leaked bot_token: %#v", channel)
	}

	listed := request(t, h, http.MethodGet, "/api/v1/notification-channels", token, nil, http.StatusOK)
	items := listed["notification_channels"].([]any)
	if len(items) != 1 {
		t.Fatalf("listed = %#v", listed)
	}
	cfg := fmt.Sprint(items[0].(map[string]any)["config_json"])
	if strings.Contains(cfg, "123:abc") || !strings.Contains(cfg, "********") {
		t.Fatalf("list response should redact secrets: %s", cfg)
	}

	// DB still keeps the real secret.
	admin, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.ListNotificationChannelsByOwner(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || !strings.Contains(stored[0].ConfigJSON, "123:abc") {
		t.Fatalf("stored channel should keep secret: %#v", stored)
	}
}

func TestBarkServerURLRejectsPrivateHosts(t *testing.T) {
	for _, raw := range []string{
		"http://example.com",
		"https://127.0.0.1",
		"https://localhost",
		"https://10.0.0.5",
		"https://192.168.1.1",
		"https://169.254.169.254",
	} {
		if err := validateBarkServerURL(raw); err == nil {
			t.Fatalf("expected reject for %s", raw)
		}
	}
	ch := &model.NotificationChannel{Name: "phone", Type: "bark", Events: "server_offline", ConfigJSON: `{"device_key":"abc","server_url":"http://127.0.0.1"}`}
	if err := validateNotificationChannel(ch, model.RoleAdmin); err == nil {
		t.Fatal("expected private bark server_url to be rejected")
	}
}

func TestNotificationDNSFailureAndRebindingAreRejected(t *testing.T) {
	u, err := url.Parse("https://notify.example/message")
	if err != nil {
		t.Fatal(err)
	}
	resolverFailure := &sequenceNotificationResolver{err: errors.New("dns unavailable")}
	if err := validatePublicHTTPSURLWithResolver(context.Background(), u, resolverFailure); err == nil {
		t.Fatal("DNS failure was accepted")
	}

	resolver := &sequenceNotificationResolver{responses: [][]net.IPAddr{
		{{IP: net.ParseIP("93.184.216.34")}},
		{{IP: net.ParseIP("169.254.169.254")}},
	}}
	if err := validatePublicHTTPSURLWithResolver(context.Background(), u, resolver); err != nil {
		t.Fatalf("initial public resolution rejected: %v", err)
	}
	dialer := &recordingNotificationDialer{}
	transport := newNotificationTransport(resolver, dialer)
	if _, err := transport.DialContext(context.Background(), "tcp", "notify.example:443"); err == nil {
		t.Fatal("dial-time private rebind was accepted")
	}
	if len(dialer.addresses) != 0 {
		t.Fatalf("private rebound address reached dialer: %#v", dialer.addresses)
	}

	publicResolver := &sequenceNotificationResolver{responses: [][]net.IPAddr{{{IP: net.ParseIP("93.184.216.34")}}}}
	publicDialer := &recordingNotificationDialer{}
	transport = newNotificationTransport(publicResolver, publicDialer)
	_, _ = transport.DialContext(context.Background(), "tcp", "notify.example:443")
	if len(publicDialer.addresses) != 1 || publicDialer.addresses[0] != "93.184.216.34:443" {
		t.Fatalf("dial was not pinned to validated IP: %#v", publicDialer.addresses)
	}
}

func TestNotificationRedirectRejectsMetadataAddress(t *testing.T) {
	redirectURL, err := url.Parse("https://169.254.169.254/latest/meta-data/")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, redirectURL.String(), nil)
	policy := notificationRedirectPolicy(&sequenceNotificationResolver{})
	if err := policy(req, []*http.Request{{}}); err == nil {
		t.Fatal("redirect to metadata address was accepted")
	}
}

func TestNotificationChannelRoleScopeOwnershipAndTemplates(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	createdUser := request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	adminID := int64(adminLogin["user"].(map[string]any)["id"].(float64))
	viewerLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)

	page := request(t, h, http.MethodGet, "/api/v1/page-data?page=notifications", viewerToken, nil, http.StatusOK)
	config := page["notification_config"].(map[string]any)
	events := config["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("viewer events = %#v", events)
	}
	seenEvents := map[string]bool{}
	for _, item := range events {
		seenEvents[item.(map[string]any)["value"].(string)] = true
	}
	if !seenEvents[notificationTrafficQuota] || !seenEvents[notificationAdminAnnouncement] {
		t.Fatalf("viewer event scope = %#v", seenEvents)
	}

	request(t, h, http.MethodPost, "/api/v1/notification-channels", viewerToken, map[string]any{
		"name": "forbidden", "type": "telegram", "enabled": true, "events": "server_offline",
		"config_json": `{"bot_token":"viewer-token","chat_id":"100"}`,
	}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/notification-channels", viewerToken, map[string]any{
		"name": "wrong-user", "type": "telegram", "enabled": true, "events": notificationTrafficQuota,
		"user_ids": []int64{adminID}, "config_json": `{"bot_token":"viewer-token","chat_id":"100"}`,
	}, http.StatusBadRequest)

	customTemplates := map[string]model.NotificationTemplate{
		notificationTrafficQuota: {Title: "额度用完 · {{.UserName}}", Body: "{{.Used}} / {{.Limit}}，{{.ResetAt}} 重置"},
	}
	createdViewerChannel := request(t, h, http.MethodPost, "/api/v1/notification-channels", viewerToken, map[string]any{
		"name": "viewer-bark", "type": "bark", "enabled": true, "events": notificationTrafficQuota + "," + notificationAdminAnnouncement,
		"config_json": `{"device_key":"viewer-key"}`, "templates_json": mustJSON(t, customTemplates),
	}, http.StatusCreated)
	viewerChannel := createdViewerChannel["notification_channel"].(map[string]any)
	viewerChannelID := int64(viewerChannel["id"].(float64))
	if int64(viewerChannel["owner_user_id"].(float64)) != viewerID {
		t.Fatalf("viewer channel owner = %#v", viewerChannel)
	}
	targets := viewerChannel["user_ids"].([]any)
	if len(targets) != 1 || int64(targets[0].(float64)) != viewerID {
		t.Fatalf("viewer channel targets = %#v", targets)
	}
	var mergedTemplates map[string]model.NotificationTemplate
	if err := json.Unmarshal([]byte(viewerChannel["templates_json"].(string)), &mergedTemplates); err != nil || mergedTemplates[notificationTrafficQuota].Title != "额度用完 · {{.UserName}}" || mergedTemplates[notificationAdminAnnouncement].Body == "" {
		t.Fatalf("default templates were not merged: %s (%v)", viewerChannel["templates_json"], err)
	}

	request(t, h, http.MethodGet, "/api/v1/notification-channels/"+itoa(viewerChannelID), adminToken, nil, http.StatusNotFound)
	adminList := request(t, h, http.MethodGet, "/api/v1/notification-channels", adminToken, nil, http.StatusOK)
	if len(adminList["notification_channels"].([]any)) != 0 {
		t.Fatalf("admin could list another user's channels: %#v", adminList)
	}
	createdAdminChannel := request(t, h, http.MethodPost, "/api/v1/notification-channels", adminToken, map[string]any{
		"name": "admin-tg", "type": "telegram", "enabled": true,
		"events":   notificationServerOffline + "," + notificationServerOnline + "," + notificationTrafficQuota + "," + notificationTaskFailed + "," + notificationTaskTimeout,
		"user_ids": []int64{adminID, viewerID}, "config_json": `{"bot_token":"admin-token","chat_id":"200"}`,
	}, http.StatusCreated)
	adminChannel := createdAdminChannel["notification_channel"].(map[string]any)
	if len(adminChannel["user_ids"].([]any)) != 2 {
		t.Fatalf("admin monitored users = %#v", adminChannel["user_ids"])
	}
	request(t, h, http.MethodGet, "/api/v1/notification-channels/"+itoa(int64(adminChannel["id"].(float64))), viewerToken, nil, http.StatusNotFound)
}

func TestNotificationDispatchScopeTemplatesAndDedupe(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hServer := newTestServer(db, "test-secret", "")
	h := hServer.Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	adminID := int64(adminLogin["user"].(map[string]any)["id"].(float64))
	createdUser := request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "viewer", "nickname": "小王", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	viewerLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)

	request(t, h, http.MethodPost, "/api/v1/notification-channels", viewerToken, map[string]any{
		"name": "viewer", "type": "telegram", "enabled": true, "events": notificationTrafficQuota + "," + notificationAdminAnnouncement,
		"config_json":    `{"bot_token":"viewer-token","chat_id":"100"}`,
		"templates_json": mustJSON(t, map[string]model.NotificationTemplate{notificationTrafficQuota: {Title: "自定义 · {{.UserName}}", Body: "剩余 0，已用 {{.Used}}"}}),
	}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/notification-channels", adminToken, map[string]any{
		"name": "admin", "type": "telegram", "enabled": true,
		"events":   notificationServerOffline + "," + notificationTrafficQuota + "," + notificationTaskFailed,
		"user_ids": []int64{adminID, viewerID}, "config_json": `{"bot_token":"admin-token","chat_id":"200"}`,
	}, http.StatusCreated)

	type sentMessage struct {
		channelID   int64
		title, body string
	}
	var sentMu sync.Mutex
	sent := []sentMessage{}
	hServer.notificationSender = func(_ context.Context, channel model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, sentMessage{channel.ID, title, body})
		return nil
	}
	server := model.Server{Name: "edge-a", AgentID: "agent-a", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}

	if got := hServer.enqueueNotificationEvent(context.Background(), notificationEvent{Name: notificationServerOffline, Key: "server:offline:1", Data: map[string]string{"ServerName": server.Name, "ServerID": fmt.Sprint(server.ID), "LastSeen": "刚刚", "Time": "现在"}}); got != 1 {
		t.Fatalf("server event queued = %d, want admin only", got)
	}
	period := model.TrafficPeriod{UserID: viewerID, PeriodKey: "2026-07", Upload: 1024, Download: 1024, Limit: 2048, State: "quota_exceeded", EndsAt: time.Now().AddDate(0, 1, 0)}
	viewer, err := db.GetUser(context.Background(), viewerID)
	if err != nil {
		t.Fatal(err)
	}
	hServer.notifyTrafficQuotaExceeded(context.Background(), *viewer, period)
	if got := hServer.enqueueNotificationEvent(context.Background(), notificationEvent{Name: notificationAdminAnnouncement, Key: "announcement:1:user:2", TargetUserID: viewerID, Data: map[string]string{"Title": "维护通知", "Message": "今晚维护", "Sender": "admin", "Time": "现在"}}); got != 1 {
		t.Fatalf("announcement queued = %d, want viewer only", got)
	}
	task := model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "notify-task"}
	if err := db.CreateTask(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	if err := hServer.completeTaskWithNotification(context.Background(), task.ID, "failed", `{"error":"validation failed"}`); err != nil {
		t.Fatal(err)
	}

	waitNotificationCount(t, &sentMu, &sent, 5)
	if duplicate := hServer.enqueueNotificationEvent(context.Background(), notificationEvent{Name: notificationServerOffline, Key: "server:offline:1", Data: map[string]string{"ServerName": server.Name, "ServerID": fmt.Sprint(server.ID), "LastSeen": "刚刚", "Time": "现在"}}); duplicate != 0 {
		t.Fatalf("duplicate event queued = %d", duplicate)
	}
	hServer.deliverPendingNotifications(context.Background())
	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 5 {
		t.Fatalf("sent notifications = %#v", sent)
	}
	foundCustom := false
	for _, item := range sent {
		if strings.Contains(item.title, "自定义 · 小王") && strings.Contains(item.body, "2.00 KB") {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatalf("custom rendered template not sent: %#v", sent)
	}
}

func TestTaskTimeoutAndAdminAnnouncementQueue(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	createdUser := request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	viewerLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/notification-channels", adminToken, map[string]any{"name": "admin", "type": "telegram", "enabled": true, "events": notificationTaskTimeout, "config_json": `{"bot_token":"admin","chat_id":"1"}`}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/notification-channels", viewerToken, map[string]any{"name": "viewer", "type": "telegram", "enabled": true, "events": notificationAdminAnnouncement, "config_json": `{"bot_token":"viewer","chat_id":"2"}`}, http.StatusCreated)

	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, _ string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title)
		return nil
	}
	server := model.Server{Name: "offline", AgentID: "agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	task := model.AgentTask{ServerID: server.ID, Type: "update_agent", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "timeout-task"}
	if err := db.CreateTask(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskStateForTest(context.Background(), task.ID, "pending", time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	srv.expireTimedOutTasks(context.Background())
	waitNotificationCount(t, &sentMu, &sent, 1)

	request(t, h, http.MethodPost, "/api/v1/notification-announcements", viewerToken, map[string]any{"title": "no", "body": "no", "user_ids": []int64{viewerID}}, http.StatusForbidden)
	announcement := request(t, h, http.MethodPost, "/api/v1/notification-announcements", adminToken, map[string]any{"title": "维护", "body": "今晚维护", "user_ids": []int64{viewerID}}, http.StatusAccepted)
	if announcement["queued_count"].(float64) != 1 {
		t.Fatalf("announcement queued = %#v", announcement)
	}
	waitNotificationCount(t, &sentMu, &sent, 2)
	history := request(t, h, http.MethodGet, "/api/v1/notification-announcements", adminToken, nil, http.StatusOK)
	if len(history["notification_announcements"].([]any)) != 1 {
		t.Fatalf("announcement history = %#v", history)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func waitNotificationCount[T any](t *testing.T, mu *sync.Mutex, items *[]T, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		current := len(*items)
		mu.Unlock()
		if current >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("notification count = %d, want at least %d", len(*items), count)
}
