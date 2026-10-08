package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/store"
)

func TestPluginUserNotificationDeliversBarkAndTelegram(t *testing.T) {
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
	adminID := int64(adminLogin["user"].(map[string]any)["id"].(float64))
	createUser := func(name string) (int64, string) {
		t.Helper()
		created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": name, "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
		id := int64(created["user"].(map[string]any)["id"].(float64))
		login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": name, "password": "long-viewer-password"}, http.StatusOK)
		return id, login["token"].(string)
	}
	barkID, barkToken := createUser("bark-user")
	telegramID, telegramToken := createUser("telegram-user")
	silentID, silentToken := createUser("silent-user")
	request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", barkToken, map[string]any{
		"name": "phone", "type": "bark", "enabled": true, "events": notificationAdminAnnouncement,
		"config_json": `{"device_key":"bark-device"}`,
	}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", telegramToken, map[string]any{
		"name": "chat", "type": "telegram", "enabled": true, "events": notificationAdminAnnouncement,
		"config_json": `{"bot_token":"token","chat_id":"100"}`,
	}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", silentToken, map[string]any{
		"name": "quota-only", "type": "bark", "enabled": true, "events": notificationTrafficQuota,
		"config_json": `{"device_key":"other-device"}`,
	}, http.StatusCreated)

	host := pluginHost{server: srv}
	requestNotify := plugin.UserNotifyRequest{
		ActorUserID: adminID, Title: "维护", Body: "今晚升级", UserIDs: []int64{barkID, telegramID, silentID}, IdempotencyKey: "plugin:test:1",
	}
	result, err := host.NotifyUsers(context.Background(), requestNotify)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recipients != 3 || result.Queued != 2 || result.Unbound != 1 {
		t.Fatalf("delivery counts = %+v", result)
	}
	types, err := db.CountNotificationDeliveriesByChannelType(context.Background(), notificationAdminAnnouncement)
	if err != nil || types["bark"] != 1 || types["telegram"] != 1 {
		t.Fatalf("queued channel types = %+v %v", types, err)
	}
	again, err := host.NotifyUsers(context.Background(), requestNotify)
	if err != nil || again.Queued != 2 || again.Unbound != 1 || again.Recipients != 3 {
		t.Fatalf("replay = %+v %v", again, err)
	}
	requery, err := db.CountNotificationDeliveriesByChannelType(context.Background(), notificationAdminAnnouncement)
	if err != nil || requery["bark"] != 1 || requery["telegram"] != 1 {
		t.Fatalf("replay queued more deliveries: %+v %v", requery, err)
	}
}
