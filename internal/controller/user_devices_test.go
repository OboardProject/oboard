package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestUserDeviceLifecycleAndLimit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	createdUser := request(t, handler, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{
		"username":     "device-user",
		"password":     "long-user-password",
		"role":         "viewer",
		"status":       "active",
		"device_limit": 1,
	}, http.StatusCreated)
	userID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	devicesPath := "/api/v1/ui/users/" + itoa(userID) + "/devices"

	request(t, handler, http.MethodPost, devicesPath, adminToken, map[string]any{"name": "Phone"}, http.StatusGone)

	device := &model.UserDevice{ID: "dev_leftover", DeviceIDHash: "hash", UserID: userID, Name: "Phone", TokenHash: "tok", TokenPrefix: "obd_left", CredentialEpoch: 1}
	if err := db.CreateUserDevice(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	listed := request(t, handler, http.MethodGet, devicesPath, adminToken, nil, http.StatusOK)
	items := listed["devices"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["token_prefix"] == "" {
		t.Fatalf("device inventory missing leftover row: %#v", listed)
	}
	if _, exposed := items[0].(map[string]any)["token_hash"]; exposed {
		t.Fatalf("device inventory exposed token hash: %#v", listed)
	}
	devicePath := devicesPath + "/" + device.ID
	request(t, handler, http.MethodPost, devicePath+"/suspend-subscription", adminToken, map[string]any{}, http.StatusGone)
	request(t, handler, http.MethodPost, devicePath+"/resume-subscription", adminToken, map[string]any{}, http.StatusGone)
	request(t, handler, http.MethodPost, devicePath+"/rotate", adminToken, map[string]any{}, http.StatusGone)
	request(t, handler, http.MethodDelete, devicePath, adminToken, nil, http.StatusOK)
	listed = request(t, handler, http.MethodGet, devicesPath, adminToken, nil, http.StatusOK)
	if status, _ := listed["devices"].([]any)[0].(map[string]any)["status"].(string); status != "revoked" {
		t.Fatalf("device was not revoked: %#v", listed)
	}
}

func TestDeviceSubscriptionTokenIsRejected(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodGet, "/api/v1/subscriptions/obd_removed-device-token?format=mihomo", "", nil, http.StatusNotFound)
}

func TestRotateUserProxyCredentialsLeavesSubscriptionToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	createdUser := request(t, handler, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{
		"username": "cred-user",
		"password": "long-user-password",
		"role":     "viewer",
		"status":   "active",
	}, http.StatusCreated)
	userID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	before, err := db.GetUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	rotated := request(t, handler, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/proxy-credentials/rotate", adminToken, map[string]any{}, http.StatusOK)
	if rotated["rotated"] != true {
		t.Fatalf("rotate response = %#v", rotated)
	}
	if _, leaked := rotated["proxy_password"]; leaked {
		t.Fatalf("rotate leaked proxy password: %#v", rotated)
	}
	after, err := db.GetUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ProxyUUID == before.ProxyUUID || after.ProxyPassword == before.ProxyPassword {
		t.Fatalf("proxy identity was not rotated: before=%#v after=%#v", before, after)
	}
	if after.SubscriptionToken != before.SubscriptionToken {
		t.Fatalf("subscription token changed during node-password rotate: %q -> %q", before.SubscriptionToken, after.SubscriptionToken)
	}
}
