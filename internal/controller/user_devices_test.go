package controller

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestUserDeviceLifecycleAndLimit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	createdUser := request(t, handler, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{
		"username":     "device-user",
		"password":     "long-user-password",
		"role":         "viewer",
		"status":       "active",
		"device_limit": 1,
	}, http.StatusCreated)
	userID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	devicesPath := "/api/v2/ui/users/" + itoa(userID) + "/devices"

	created := request(t, handler, http.MethodPost, devicesPath, adminToken, map[string]any{"name": "Phone"}, http.StatusCreated)
	firstToken, _ := created["device_token"].(string)
	device := created["device"].(map[string]any)
	deviceID, _ := device["id"].(string)
	if firstToken == "" || deviceID == "" {
		t.Fatalf("created device missing one-time credentials: %#v", created)
	}

	listed := request(t, handler, http.MethodGet, devicesPath, adminToken, nil, http.StatusOK)
	items := listed["devices"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["token_prefix"] == "" {
		t.Fatalf("device inventory missing token prefix: %#v", listed)
	}
	if _, exposed := items[0].(map[string]any)["token_hash"]; exposed {
		t.Fatalf("device inventory exposed token hash: %#v", listed)
	}
	if _, exposed := items[0].(map[string]any)["device_token"]; exposed {
		t.Fatalf("device inventory exposed one-time token: %#v", listed)
	}
	request(t, handler, http.MethodPost, devicesPath, adminToken, map[string]any{"name": "Laptop"}, http.StatusConflict)

	devicePath := devicesPath + "/" + deviceID
	request(t, handler, http.MethodPost, devicePath+"/suspend-subscription", adminToken, map[string]any{}, http.StatusOK)
	listed = request(t, handler, http.MethodGet, devicesPath, adminToken, nil, http.StatusOK)
	if suspended, _ := listed["devices"].([]any)[0].(map[string]any)["subscription_suspended"].(bool); !suspended {
		t.Fatalf("device subscription was not suspended: %#v", listed)
	}
	request(t, handler, http.MethodPost, devicePath+"/resume-subscription", adminToken, map[string]any{}, http.StatusOK)

	rotated := request(t, handler, http.MethodPost, devicePath+"/rotate", adminToken, map[string]any{}, http.StatusOK)
	rotatedToken, _ := rotated["device_token"].(string)
	if rotatedToken == "" || rotatedToken == firstToken {
		t.Fatalf("device token was not rotated: %#v", rotated)
	}

	request(t, handler, http.MethodDelete, devicePath, adminToken, nil, http.StatusOK)
	listed = request(t, handler, http.MethodGet, devicesPath, adminToken, nil, http.StatusOK)
	if status, _ := listed["devices"].([]any)[0].(map[string]any)["status"].(string); status != "revoked" {
		t.Fatalf("device was not revoked: %#v", listed)
	}
}
