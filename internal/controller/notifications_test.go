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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	oboardlog "github.com/OboardProject/oboard/internal/logging"
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
	if len(templates) != 3 || templates[notificationAdminAnnouncement].Title == "" || templates[notificationUserRisk].Title == "" {
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
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	// Reject incomplete create.
	bad := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"name": "ops", "type": "telegram", "enabled": true, "events": "server_offline", "config_json": `{"bot_token":"","chat_id":""}`})
	req := httptest.NewRequest(http.MethodPost, "/api/v2/ui/notification-channels", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("incomplete create status = %d body=%s", bad.Code, bad.Body.String())
	}

	created := request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
		"name": "ops-tg", "type": "telegram", "enabled": true, "events": "server_offline,server_online",
		"config_json": `{"bot_token":"123:abc","chat_id":"-1001"}`,
	}, http.StatusCreated)
	id := int64(created["notification_channel"].(map[string]any)["id"].(float64))
	if id == 0 {
		t.Fatalf("missing id: %#v", created)
	}

	// Test endpoint should fail transport (invalid token) but route must exist and validate.
	testRec := httptest.NewRecorder()
	testReq := httptest.NewRequest(http.MethodPost, "/api/v2/ui/notification-channels/"+itoa(id)+"/test", bytes.NewReader([]byte("{}")))
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
	draftReq := httptest.NewRequest(http.MethodPost, "/api/v2/ui/notification-channels/test", bytes.NewReader(draftBody))
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
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
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

	listed := request(t, h, http.MethodGet, "/api/v2/ui/notification-channels", token, nil, http.StatusOK)
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
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	createdUser := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	adminID := int64(adminLogin["user"].(map[string]any)["id"].(float64))
	viewerLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)

	page := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=notifications", viewerToken, nil, http.StatusOK)
	config := page["notification_config"].(map[string]any)
	events := config["events"].([]any)
	if len(events) != 3 {
		t.Fatalf("viewer events = %#v", events)
	}
	seenEvents := map[string]bool{}
	for _, item := range events {
		seenEvents[item.(map[string]any)["value"].(string)] = true
	}
	if !seenEvents[notificationTrafficQuota] || !seenEvents[notificationUserRisk] || !seenEvents[notificationAdminAnnouncement] {
		t.Fatalf("viewer event scope = %#v", seenEvents)
	}

	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", viewerToken, map[string]any{
		"name": "forbidden", "type": "telegram", "enabled": true, "events": "server_offline",
		"config_json": `{"bot_token":"viewer-token","chat_id":"100"}`,
	}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", viewerToken, map[string]any{
		"name": "wrong-user", "type": "telegram", "enabled": true, "events": notificationTrafficQuota,
		"user_ids": []int64{adminID}, "config_json": `{"bot_token":"viewer-token","chat_id":"100"}`,
	}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", viewerToken, map[string]any{
		"name": "wrong-risk-user", "type": "telegram", "enabled": true, "events": notificationUserRisk,
		"user_ids": []int64{adminID}, "config_json": `{"bot_token":"viewer-token","chat_id":"100"}`,
	}, http.StatusBadRequest)

	customTemplates := map[string]model.NotificationTemplate{
		notificationTrafficQuota: {Title: "额度用完 · {{.UserName}}", Body: "{{.Used}} / {{.Limit}}，{{.ResetAt}} 重置"},
	}
	createdViewerChannel := request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", viewerToken, map[string]any{
		"name": "viewer-bark", "type": "bark", "enabled": true, "events": notificationTrafficQuota + "," + notificationUserRisk + "," + notificationAdminAnnouncement,
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

	request(t, h, http.MethodGet, "/api/v2/ui/notification-channels/"+itoa(viewerChannelID), adminToken, nil, http.StatusNotFound)
	adminList := request(t, h, http.MethodGet, "/api/v2/ui/notification-channels", adminToken, nil, http.StatusOK)
	if len(adminList["notification_channels"].([]any)) != 0 {
		t.Fatalf("admin could list another user's channels: %#v", adminList)
	}
	createdAdminChannel := request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", adminToken, map[string]any{
		"name": "admin-tg", "type": "telegram", "enabled": true,
		"events":   notificationServerOffline + "," + notificationServerOnline + "," + notificationTrafficQuota + "," + notificationUserRisk + "," + notificationTaskFailed + "," + notificationTaskTimeout,
		"user_ids": []int64{adminID, viewerID}, "config_json": `{"bot_token":"admin-token","chat_id":"200"}`,
	}, http.StatusCreated)
	adminChannel := createdAdminChannel["notification_channel"].(map[string]any)
	if len(adminChannel["user_ids"].([]any)) != 2 {
		t.Fatalf("admin monitored users = %#v", adminChannel["user_ids"])
	}
	request(t, h, http.MethodGet, "/api/v2/ui/notification-channels/"+itoa(int64(adminChannel["id"].(float64))), viewerToken, nil, http.StatusNotFound)
}

func TestNotificationDispatchScopeTemplatesAndDedupe(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hServer := newTestServer(db, "test-secret", "")
	h := hServer.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	adminID := int64(adminLogin["user"].(map[string]any)["id"].(float64))
	createdUser := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "viewer", "nickname": "小王", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	viewerLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)

	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", viewerToken, map[string]any{
		"name": "viewer", "type": "telegram", "enabled": true, "events": notificationTrafficQuota + "," + notificationAdminAnnouncement,
		"config_json":    `{"bot_token":"viewer-token","chat_id":"100"}`,
		"templates_json": mustJSON(t, map[string]model.NotificationTemplate{notificationTrafficQuota: {Title: "自定义 · {{.UserName}}", Body: "剩余 0，已用 {{.Used}}"}}),
	}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", adminToken, map[string]any{
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

	waitNotificationCount(t, hServer, &sentMu, &sent, 5)
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

func TestCertificateFailureNotificationCoversAllIssuersAndRedactsEABSecret(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hServer := newTestServer(db, "test-secret", "")
	h := hServer.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
		"name": "google-eab", "type": "telegram", "enabled": true, "events": notificationCertificateFailed,
		"config_json": `{"bot_token":"admin-token","chat_id":"200"}`,
	}, http.StatusCreated)

	const hmacKey = "notification-secret-hmac"
	created := request(t, h, http.MethodPost, "/api/v2/ui/certificates", token, map[string]any{
		"name": "google-notify", "domains": []string{"notify.example.com"}, "challenge_type": model.CertificateChallengeDNSManual,
		"acme_ca": "google", "eab_key_id": "notify-key-id", "eab_hmac_key": hmacKey,
	}, http.StatusCreated)["certificate"].(map[string]any)
	certificate, err := db.GetCertificate(context.Background(), int64(created["id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}

	type sentMessage struct{ title, body string }
	var sentMu sync.Mutex
	sent := []sentMessage{}
	hServer.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, sentMessage{title: title, body: body})
		return nil
	}
	hServer.markCertificateIssueFailed(context.Background(), certificate, errors.New("externalAccountRequired: "+hmacKey))
	waitNotificationCount(t, hServer, &sentMu, &sent, 1)

	sentMu.Lock()
	message := sent[0]
	sentMu.Unlock()
	if !strings.Contains(message.title, "证书签发失败") || !strings.Contains(message.body, "Google Trust Services") || !strings.Contains(message.body, "notify-key-id") || !strings.Contains(message.body, "externalAccountRequired") {
		t.Fatalf("Google certificate failure message = %#v", message)
	}
	if strings.Contains(message.body, hmacKey) {
		t.Fatal("Google certificate failure notification exposed the HMAC key")
	}
	stored, err := db.GetCertificate(context.Background(), certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.LastError, hmacKey) || !strings.Contains(stored.LastError, "[已隐藏]") {
		t.Fatalf("stored certificate error was not redacted: %q", stored.LastError)
	}
	created = request(t, h, http.MethodPost, "/api/v2/ui/certificates", token, map[string]any{
		"name": "letsencrypt-notify", "domains": []string{"le.example.com"}, "challenge_type": model.CertificateChallengeDNSManual,
		"acme_ca": "letsencrypt",
	}, http.StatusCreated)["certificate"].(map[string]any)
	certificate, err = db.GetCertificate(context.Background(), int64(created["id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	hServer.markCertificateIssueFailed(context.Background(), certificate, errors.New("DNS validation failed"))
	waitNotificationCount(t, hServer, &sentMu, &sent, 2)
	sentMu.Lock()
	message = sent[1]
	sentMu.Unlock()
	if !strings.Contains(message.body, "Let's Encrypt") || !strings.Contains(message.body, "DNS validation failed") {
		t.Fatalf("Let's Encrypt failure message = %#v", message)
	}
}

func TestHTTPCertificateTaskFailureQueuesCertificateNotification(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
		"name": "certificate", "type": "telegram", "enabled": true, "events": notificationCertificateFailed,
		"config_json": `{"bot_token":"admin","chat_id":"1"}`,
	}, http.StatusCreated)
	server := model.Server{Name: "issue-node", AgentID: "issue-agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	certificate := model.Certificate{Name: "HTTP 证书", PrimaryDomain: "http.example.com", Domains: []string{"http.example.com"}, ChallengeType: model.CertificateChallengeHTTP, IssuanceServerID: &server.ID, ACMECA: "letsencrypt", Status: model.CertificateStatusIssuing, LastRenewalAttemptAt: &now}
	if err := db.CreateCertificate(context.Background(), &certificate); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(model.IssueCertificateHTTPTaskPayload{CertificateID: certificate.ID, Domains: certificate.Domains, ACMECA: certificate.ACMECA})
	if err != nil {
		t.Fatal(err)
	}
	task := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeIssueCertificateHTTP, PayloadJSON: string(payload), Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "certificate-task"}
	if err := db.CreateTask(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title+"\n"+body)
		return nil
	}
	if err := srv.completeTaskWithNotification(context.Background(), task.ID, "failed", `{"error":"HTTP-01 验证未通过"}`); err != nil {
		t.Fatal(err)
	}
	waitNotificationCount(t, srv, &sentMu, &sent, 1)
	stored, err := db.GetCertificate(context.Background(), certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.CertificateStatusFailed || !strings.Contains(stored.LastError, "HTTP-01 验证未通过") {
		t.Fatalf("certificate failure state = %#v", stored)
	}
	sentMu.Lock()
	defer sentMu.Unlock()
	if !strings.Contains(sent[0], "证书签发失败 · HTTP 证书") || !strings.Contains(sent[0], "HTTP-01 验证未通过") {
		t.Fatalf("certificate notification = %q", sent[0])
	}
}

func TestConnectionAuditRiskNotificationTargetsUserAndAdmin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	createdUser := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "viewer", "nickname": "小王", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	viewerToken := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", viewerToken, map[string]any{"name": "viewer-risk", "type": "telegram", "enabled": true, "events": notificationUserRisk, "config_json": `{"bot_token":"viewer","chat_id":"1"}`}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", adminToken, map[string]any{"name": "admin-risk", "type": "telegram", "enabled": true, "events": notificationUserRisk, "user_ids": []int64{viewerID}, "config_json": `{"bot_token":"admin","chat_id":"2"}`}, http.StatusCreated)

	server := model.Server{Name: "risk-node", AgentID: "risk-agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reports := make([]model.ConnectionAuditReport, 0, 4)
	for i, country := range []string{"CN", "US", "DE", "JP"} {
		startedAt := now.Add(-90 * time.Second)
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: fmt.Sprintf("risk-%d", i), ServerID: server.ID, UserID: viewerID, DeviceIDHash: "device-cloned", CredentialEpoch: 1,
			SourceIP: fmt.Sprintf("11.%d.0.1", i+1), RouteID: fmt.Sprintf("route-%d", i), SourceCountryCode: country, SourceCountry: country, SourceISP: fmt.Sprintf("ISP-%d", i), GeoDatabaseRevision: "test",
			Network: "tcp", ConnectionCount: 1, ClosedCount: 1, DurationTotalMS: 90000, DurationMaxMS: 90000, DurationGT20SCount: 1,
			UploadBytes: 1024, DownloadBytes: 1024, PayloadFirstAt: startedAt, PayloadLastAt: now, PresenceSequence: uint64(i + 1), ActivePeak: 1, BucketCapacity: 4096,
			CollectionStartedAt: startedAt, CollectionEndedAt: now, StartedAt: startedAt, EndedAt: now,
		})
	}
	if _, err := db.AddConnectionAuditReports(context.Background(), reports); err != nil {
		t.Fatal(err)
	}
	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title+"\n"+body)
		return nil
	}
	srv.notifyConnectionAuditRisks(context.Background(), []int64{viewerID})
	waitNotificationCount(t, srv, &sentMu, &sent, 2)
	sentMu.Lock()
	for _, message := range sent {
		if !strings.Contains(message, "异常使用提醒 · 小王") || !strings.Contains(message, "告警") || !strings.Contains(message, "设备凭证") {
			t.Fatalf("risk notification = %q", message)
		}
	}
	sentMu.Unlock()
	srv.notifyConnectionAuditRisks(context.Background(), []int64{viewerID})
	time.Sleep(50 * time.Millisecond)
	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("duplicate risk notifications = %#v", sent)
	}
}

func TestOperationalNotificationEventsUseAdminScope(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
		"name": "operations", "type": "telegram", "enabled": true,
		"events":      notificationCertificateExpiry + "," + notificationBackupFailed + "," + notificationUpdateFailed + "," + notificationDNSSyncFailed,
		"config_json": `{"bot_token":"admin","chat_id":"1"}`,
	}, http.StatusCreated)
	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title+"\n"+body)
		return nil
	}
	notAfter := time.Now().UTC().Add(10 * 24 * time.Hour)
	srv.notifyCertificateExpiring(context.Background(), &model.Certificate{ID: 7, Name: "入口证书", Domains: []string{"edge.example.com"}, ACMECA: "letsencrypt", NotAfter: &notAfter})
	srv.notifyBackupFailure(context.Background(), "2026-07-28", "第三方上传", "WebDAV 无法连接")
	srv.notifyControllerUpdateFailure(context.Background(), "安装更新", "dev-next", "更新器返回失败")
	srv.notifyDNSSyncFailure(context.Background(), model.Inbound{ID: 9, ServerID: 2, Name: "主入口", DNSDomain: "edge.example.com"}, "香港节点", errors.New("DNS 服务暂时不可用"))
	waitNotificationCount(t, srv, &sentMu, &sent, 4)
	sentMu.Lock()
	defer sentMu.Unlock()
	joined := strings.Join(sent, "\n")
	for _, expected := range []string{"证书到期提醒", "自动备份失败", "主控自动更新失败", "域名自动更新失败"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %s in notifications: %s", expected, joined)
		}
	}
}

func TestScheduledUpdateFailureNotifiesOnlyWhenAutomaticUpdatesEnabled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	srv.controllerUpdater = controllerupdate.NewClient(filepath.Join(t.TempDir(), "missing-updater.sock"))
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
		"name": "updates", "type": "telegram", "enabled": true, "events": notificationUpdateFailed,
		"config_json": `{"bot_token":"admin","chat_id":"1"}`,
	}, http.StatusCreated)
	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title+"\n"+body)
		return nil
	}

	srv.runScheduledControllerUpdate(context.Background())
	sentMu.Lock()
	if len(sent) != 0 {
		t.Fatalf("automatic updates are disabled, notifications = %#v", sent)
	}
	sentMu.Unlock()

	if err := db.SetSetting(context.Background(), controllerAutoUpdateSetting, "true"); err != nil {
		t.Fatal(err)
	}
	srv.runScheduledControllerUpdate(context.Background())
	waitNotificationCount(t, srv, &sentMu, &sent, 1)
	sentMu.Lock()
	defer sentMu.Unlock()
	if !strings.Contains(sent[0], "主控自动更新失败") || !strings.Contains(sent[0], "主控更新器不可用") {
		t.Fatalf("automatic update failure notification = %q", sent[0])
	}
}

func TestCertificateRenewalExpiryNotificationRequiresUserAction(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
		"name": "certificates", "type": "telegram", "enabled": true, "events": notificationCertificateExpiry,
		"config_json": `{"bot_token":"admin","chat_id":"1"}`,
	}, http.StatusCreated)
	server := model.Server{Name: "renewal-node", AgentID: "renewal-agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(10 * 24 * time.Hour)
	manual := model.Certificate{Name: "手动证书", PrimaryDomain: "manual.example.com", Domains: []string{"manual.example.com"}, ChallengeType: "imported", ACMECA: "letsencrypt", Status: model.CertificateStatusReady, NotAfter: &expiresAt}
	automatic := model.Certificate{Name: "自动证书", PrimaryDomain: "automatic.example.com", Domains: []string{"automatic.example.com"}, ChallengeType: model.CertificateChallengeHTTP, IssuanceServerID: &server.ID, ACMECA: "letsencrypt", Status: model.CertificateStatusReady, NotAfter: &expiresAt, AutoRenew: true}
	if err := db.CreateCertificate(context.Background(), &manual); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCertificate(context.Background(), &automatic); err != nil {
		t.Fatal(err)
	}
	var sentMu sync.Mutex
	sent := []string{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, title+"\n"+body)
		return nil
	}

	srv.renewCertificates(context.Background())
	waitNotificationCount(t, srv, &sentMu, &sent, 1)
	time.Sleep(50 * time.Millisecond)
	sentMu.Lock()
	if len(sent) != 1 || !strings.Contains(sent[0], "手动证书") || strings.Contains(sent[0], "自动证书") {
		t.Fatalf("certificate expiry notifications = %#v", sent)
	}
	sentMu.Unlock()
	storedAutomatic, err := db.GetCertificate(context.Background(), automatic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedAutomatic.Status != model.CertificateStatusIssuing {
		t.Fatalf("automatic certificate status = %q", storedAutomatic.Status)
	}
}

func TestNotificationDNSErrorTextUsesUserFacingReasons(t *testing.T) {
	tests := map[string]string{
		"DNS credential is not selected":                   "未选择域名服务凭据",
		"DNS credential is unavailable":                    "域名服务凭据不可用",
		"DNS credential is not verified":                   "域名服务凭据尚未验证",
		"DNS proxy is only supported by Cloudflare":        "当前域名服务不支持代理加速",
		"inbound server not found":                         "入口绑定的服务器不存在",
		"server 3 has no address for DNS record mode both": "服务器没有可用于更新域名记录的公网地址",
	}
	for input, expected := range tests {
		if actual := notificationDNSErrorText(input); actual != expected {
			t.Errorf("notificationDNSErrorText(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestTaskTypeNotificationLabelIncludesExternalEgress(t *testing.T) {
	if got := taskTypeNotificationLabel(model.AgentTaskTypeProbeExternalEgress); got != "第三方出口探测" {
		t.Fatalf("task label = %q", got)
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
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	createdUser := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	viewerLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", adminToken, map[string]any{"name": "admin", "type": "telegram", "enabled": true, "events": notificationTaskTimeout, "config_json": `{"bot_token":"admin","chat_id":"1"}`}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", viewerToken, map[string]any{"name": "viewer", "type": "telegram", "enabled": true, "events": notificationAdminAnnouncement, "config_json": `{"bot_token":"viewer","chat_id":"2"}`}, http.StatusCreated)

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
	waitNotificationCount(t, srv, &sentMu, &sent, 1)

	request(t, h, http.MethodPost, "/api/v2/ui/notification-announcements", viewerToken, map[string]any{"title": "no", "body": "no", "user_ids": []int64{viewerID}}, http.StatusForbidden)
	realtimeClient, _, ok := srv.realtime.subscribe(model.RoleViewer)
	if !ok {
		t.Fatal("subscribe viewer realtime client")
	}
	defer srv.realtime.unsubscribe(realtimeClient)
	announcement := request(t, h, http.MethodPost, "/api/v2/ui/notification-announcements", adminToken, map[string]any{"title": "维护", "body": "今晚维护", "user_ids": []int64{viewerID}}, http.StatusAccepted)
	if announcement["queued_count"].(float64) != 1 {
		t.Fatalf("announcement queued = %#v", announcement)
	}
	realtimeEvent, ok := realtimeClient.drain()
	if !ok || !slices.Contains(realtimeEvent.Resources, "user_overview") {
		t.Fatalf("announcement realtime event = %#v", realtimeEvent)
	}
	waitNotificationCount(t, srv, &sentMu, &sent, 2)
	history := request(t, h, http.MethodGet, "/api/v2/ui/notification-announcements", adminToken, nil, http.StatusOK)
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

func waitNotificationCount[T any](t *testing.T, server *Server, mu *sync.Mutex, items *[]T, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		current := len(*items)
		mu.Unlock()
		if current >= count {
			server.notificationWG.Wait()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("notification count = %d, want at least %d", len(*items), count)
}

func TestNotificationTestChannelAndRawLog(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logs, err := oboardlog.New(filepath.Join(t.TempDir(), "controller.log"), oboardlog.Config{MaxBytes: 1 << 20, Backups: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	h := New(db, "test-secret", "", "", logs).Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/users", token, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)
	request(t, h, http.MethodGet, "/api/v2/ui/notification-channels/raw-log?lines=100", viewerToken, nil, http.StatusForbidden)

	created := request(t, h, http.MethodPost, "/api/v2/ui/notification-channels", token, map[string]any{
		"name": "调试", "type": "test", "enabled": true, "events": notificationTrafficQuota,
		"config_json": "{}", "templates_json": "{}",
	}, http.StatusCreated)
	id := int64(created["notification_channel"].(map[string]any)["id"].(float64))
	if id <= 0 {
		t.Fatalf("test channel id = %d", id)
	}

	if err := sendNotification(context.Background(), model.NotificationChannel{Name: "调试", Type: "test"}, "OBoard 测试通知", "原始消息正文"); err != nil {
		t.Fatalf("test channel delivery failed: %v", err)
	}
	request(t, h, http.MethodPost, fmt.Sprintf("/api/v2/ui/notification-channels/%d/test", id), token, map[string]any{}, http.StatusOK)

	if _, err := logs.Write([]byte("unrelated log line\nnotification[test] channel=调试 title=\"OBoard 测试通知\" body=\"原始消息正文\"\n")); err != nil {
		t.Fatal(err)
	}
	response := request(t, h, http.MethodGet, "/api/v2/ui/notification-channels/raw-log?lines=100", token, nil, http.StatusOK)
	content := response["logs"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "notification[test]") || strings.Contains(content, "unrelated log line") {
		t.Fatalf("raw log does not filter test channel records: %q", content)
	}

	invalid := request(t, h, http.MethodGet, "/api/v2/ui/notification-channels/raw-log?lines=9999", token, nil, http.StatusBadRequest)
	if invalid == nil {
		t.Fatal("raw log accepted out-of-range lines")
	}
}

func TestNotificationChannelValidationRejectsUnknownType(t *testing.T) {
	channel := model.NotificationChannel{Name: "未知", Type: "slack", Enabled: true, Events: notificationTrafficQuota, ConfigJSON: "{}"}
	if err := validateNotificationChannel(&channel, model.RoleAdmin); err == nil {
		t.Fatal("unknown channel type was accepted")
	}
	testChannel := model.NotificationChannel{Name: "调试", Type: "test", Enabled: true, Events: notificationTrafficQuota, ConfigJSON: "{}"}
	if err := validateNotificationChannel(&testChannel, model.RoleAdmin); err != nil {
		t.Fatalf("test channel type rejected: %v", err)
	}
	if testChannel.ConfigJSON != "{}" {
		t.Fatalf("test channel config = %q", testChannel.ConfigJSON)
	}
}
