package controller

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestServerExpiryAutoRenewRunsOnceAfterThreeDayGrace(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")

	expiry := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	server := &model.Server{
		Name: "lease", Status: model.ServerOnline, ExpiresAt: &expiry,
		AutoRenewEnabled: true, RenewalCycle: model.ServerRenewalCycleMonthly, ExpiryNotifyEnabled: false,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	srv.checkServerExpiryAt(ctx, now)
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExpiresAt == nil || stored.ExpiresAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("auto renewed expiry = %v, want 2026-09-01", stored.ExpiresAt)
	}
	if stored.LastAutoRenewedAt == nil {
		t.Fatal("last_auto_renewed_at was not recorded")
	}

	firstRenewedAt := stored.LastAutoRenewedAt
	srv.checkServerExpiryAt(ctx, now)
	stored, err = db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastAutoRenewedAt == nil || !stored.LastAutoRenewedAt.Equal(*firstRenewedAt) {
		t.Fatalf("auto renewal repeated: before=%v after=%v", firstRenewedAt, stored.LastAutoRenewedAt)
	}
}

func TestServerExpiryNotificationsMilestoneAndDailyDedupe(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "expiry", Type: "bark", Enabled: true, Events: notificationServerExpiry, ConfigJSON: `{"device_key":"expiry"}`}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}

	threeDays := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	sevenDays := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	first := &model.Server{Name: "three", Status: model.ServerOnline, ExpiresAt: &threeDays, ExpiryNotifyEnabled: true, AutoRenewEnabled: false}
	second := &model.Server{Name: "seven", Status: model.ServerOnline, ExpiresAt: &sevenDays, ExpiryNotifyEnabled: true, AutoRenewEnabled: false}
	if err := db.CreateServer(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, second); err != nil {
		t.Fatal(err)
	}

	type sentMessage struct{ title, body string }
	var sentMu sync.Mutex
	sent := []sentMessage{}
	srv.notificationSender = func(_ context.Context, _ model.NotificationChannel, title, body string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, sentMessage{title: title, body: body})
		return nil
	}

	settings := map[string]string{settingServerExpiryNotifyLeadDays: `[7,3]`, settingServerExpiryNotifyTime: "00:00"}
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	srv.scheduleServerExpiryNotifications(ctx, settings, now)
	srv.deliverPendingNotifications(ctx)

	sentMu.Lock()
	if len(sent) != 2 {
		sentMu.Unlock()
		t.Fatalf("expiry notifications = %#v", sent)
	}
	sentMu.Unlock()

	srv.scheduleServerExpiryNotifications(ctx, settings, now)
	srv.deliverPendingNotifications(ctx)
	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("duplicate expiry notifications sent: %#v", sent)
	}
}

func TestServerExtendExpiryRESTEndpoint(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "server-expiry-controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{
		"name": "lease", "expires_at": "2026-09-01T00:00:00Z", "auto_renew_enabled": true,
		"renewal_cycle": "monthly", "expiry_notify_enabled": false,
	}, http.StatusCreated)["server"].(map[string]any)
	if created["expires_at"] == nil || created["auto_renew_enabled"] != true || created["expiry_notify_enabled"] != false {
		t.Fatalf("created server expiry fields = %#v", created)
	}

	extended := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+fmt.Sprint(int64(created["id"].(float64)))+"/extend-expiry", token, map[string]any{"days": 3}, http.StatusOK)["server"].(map[string]any)
	expiresAt, _ := time.Parse(time.RFC3339Nano, fmt.Sprintf("%v", extended["expires_at"]))
	if expiresAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02") != "2026-09-04" {
		t.Fatalf("extended expiry = %v, want 2026-09-04", expiresAt)
	}
}

func TestServerExpiryNotificationEventIsAdminScoped(t *testing.T) {
	allowed := allowedNotificationEventSet(model.RoleAdmin)
	if !allowed[notificationServerExpiry] {
		t.Fatal("admin cannot subscribe to server_expiring")
	}
	if allowedNotificationEventSet(model.RoleViewer)[notificationServerExpiry] {
		t.Fatal("viewer should not subscribe to server_expiring")
	}
	if notificationEventsTargetUsers(notificationServerExpiry) {
		t.Fatal("server expiry should not require user targets")
	}
}

func TestServerExpirySettingsRoundTrip(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	settings := srv.publicSettings(context.Background(), map[string]string{})
	if got := settings[settingServerExpiryNotifyTime]; got != "00:00" {
		t.Fatalf("default expiry notify time = %#v", got)
	}
	leadDays, ok := settings[settingServerExpiryNotifyLeadDays].([]int)
	if !ok || len(leadDays) != 2 || leadDays[0] != 7 || leadDays[1] != 3 {
		t.Fatalf("default expiry lead days = %#v", settings[settingServerExpiryNotifyLeadDays])
	}
}


