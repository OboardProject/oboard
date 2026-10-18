package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestPluginNotificationChannelOptions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	ownerID := int64(login["user"].(map[string]any)["id"].(float64))
	other := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "other", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	otherID := int64(other["user"].(map[string]any)["id"].(float64))
	path := "/api/v1/plugins/notification-channels"
	empty := request(t, h, http.MethodGet, path, token, nil, http.StatusOK)["data"].(map[string]any)["notification_channels"]
	if items, ok := empty.([]any); !ok || len(items) != 0 {
		t.Fatalf("empty list = %#v", empty)
	}
	for _, channel := range []model.NotificationChannel{
		{OwnerUserID: ownerID, Name: "Zulu", Type: "bark", ConfigJSON: "{}"},
		{OwnerUserID: ownerID, Name: "alpha", Type: "telegram", ConfigJSON: "{}"},
		{OwnerUserID: otherID, Name: "hidden", Type: "bark", ConfigJSON: "{}"},
	} {
		if err := db.CreateNotificationChannel(context.Background(), &channel); err != nil {
			t.Fatal(err)
		}
	}
	data := request(t, h, http.MethodGet, path, token, nil, http.StatusOK)["data"].(map[string]any)
	items := data["notification_channels"].([]any)
	if len(items) != 2 {
		t.Fatalf("channels = %#v", items)
	}
	for i, name := range []string{"alpha", "Zulu"} {
		item := items[i].(map[string]any)
		if len(item) != 3 || item["name"] != name || item["id"] == nil || item["type"] == nil {
			t.Fatalf("option = %#v", item)
		}
	}
	request(t, h, http.MethodGet, path, "", nil, http.StatusUnauthorized)
	request(t, h, http.MethodPost, path, token, map[string]any{}, http.StatusMethodNotAllowed)
	req := httptest.NewRequest(http.MethodGet, path+"/extra", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("extra path status = %d", recorder.Code)
	}
	if _, err := srv.plugins.NotificationChannelOptions(context.Background(), application.Principal{}); err == nil {
		t.Fatal("missing plugin permission accepted")
	}
	itemsWithoutOwner, err := srv.plugins.NotificationChannelOptions(context.Background(), application.Principal{Scopes: []string{"plugins:read"}})
	if err != nil || len(itemsWithoutOwner) != 0 {
		t.Fatalf("ownerless principal = %#v, %v", itemsWithoutOwner, err)
	}
}
