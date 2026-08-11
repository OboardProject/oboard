package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/subrelay"
	"github.com/OboardProject/oboard/internal/version"
)

func TestManagedSubscriptionRelayEnrollmentHeartbeatAndUpdate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-session-secret-at-least-32", "").Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	created := request(t, handler, http.MethodPost, "/api/v2/ui/subscription-relays", token, map[string]any{"name": "domestic", "public_url": "https://relay.example"}, http.StatusCreated)
	relay := created["subscription_relay"].(map[string]any)
	relayID := int64(relay["id"].(float64))
	enrollmentToken := created["enrollment_token"].(string)
	if enrollmentToken == "" || relay["active"] != false || relay["enrolled"] != false {
		t.Fatalf("unexpected created relay: %#v", created)
	}
	request(t, handler, http.MethodPost, "/api/v2/ui/subscription-relays/"+strconv.FormatInt(relayID, 10)+"/activate", token, map[string]any{}, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v2/ui/subscription-relays", token, map[string]any{"name": "duplicate", "public_url": "https://relay.example/"}, http.StatusConflict)

	enrolled := request(t, handler, http.MethodPost, "/api/v1/subscription-relay/enroll", "", map[string]any{"enrollment_token": enrollmentToken}, http.StatusOK)
	relayToken := enrolled["relay_token"].(string)
	signingSecret := enrolled["signing_secret"].(string)
	if signingSecret == "" || relayToken == "" {
		t.Fatalf("incomplete enrollment response: %#v", enrolled)
	}
	request(t, handler, http.MethodPost, "/api/v1/subscription-relay/enroll", "", map[string]any{"enrollment_token": enrollmentToken}, http.StatusUnauthorized)

	heartbeat := func(build string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"version": "old", "build": build, "commit": "abc", "os": "linux", "arch": "amd64", "service_manager": "systemd"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription-relay/heartbeat", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+relayToken)
		relayIDValue := strconv.FormatInt(relayID, 10)
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := fmt.Sprintf("%032d", time.Now().UnixNano())
		req.Header.Set(subrelay.HeaderRelayID, relayIDValue)
		req.Header.Set(subrelay.HeaderTimestamp, timestamp)
		req.Header.Set(subrelay.HeaderNonce, nonce)
		req.Header.Set(subrelay.HeaderSignature, subrelay.SignControl(signingSecret, relayIDValue, req.Method, req.URL.RequestURI(), timestamp, nonce, body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	checkedHeartbeat := func(build string) map[string]any {
		recorder := heartbeat(build)
		if recorder.Code != http.StatusOK {
			t.Fatalf("heartbeat status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	if action := checkedHeartbeat("old-build")["action"]; action != "none" {
		t.Fatalf("unexpected initial action %v", action)
	}
	request(t, handler, http.MethodPost, "/api/v2/ui/subscription-relays/"+strconv.FormatInt(relayID, 10)+"/update", token, map[string]any{}, http.StatusAccepted)
	if action := checkedHeartbeat("old-build"); action["action"] != "update" || action["target_build"] != version.Build {
		t.Fatalf("unexpected update action %#v", action)
	}
	if action := checkedHeartbeat(version.Build)["action"]; action != "none" {
		t.Fatalf("completed update action %v", action)
	}

	listed := request(t, handler, http.MethodGet, "/api/v2/ui/subscription-relays", token, nil, http.StatusOK)["subscription_relays"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["status"] != "online" || listed[0].(map[string]any)["active"] != true || listed[0].(map[string]any)["token_hash"] != nil {
		t.Fatalf("unexpected public relay list: %#v", listed)
	}

	installer := httptest.NewRecorder()
	handler.ServeHTTP(installer, httptest.NewRequest(http.MethodGet, "/install/subscription-relay.sh", nil))
	if installer.Code != http.StatusOK || !strings.Contains(installer.Body.String(), "OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN") {
		t.Fatalf("installer response status=%d", installer.Code)
	}

	uninstallBody := []byte(`{}`)
	uninstall := httptest.NewRequest(http.MethodPost, "/api/v1/subscription-relay/uninstall", bytes.NewReader(uninstallBody))
	uninstall.Header.Set("Content-Type", "application/json")
	uninstall.Header.Set("Authorization", "Bearer "+relayToken)
	relayIDValue := strconv.FormatInt(relayID, 10)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := fmt.Sprintf("%032d", time.Now().UnixNano())
	uninstall.Header.Set(subrelay.HeaderRelayID, relayIDValue)
	uninstall.Header.Set(subrelay.HeaderTimestamp, timestamp)
	uninstall.Header.Set(subrelay.HeaderNonce, nonce)
	uninstall.Header.Set(subrelay.HeaderSignature, subrelay.SignControl(signingSecret, relayIDValue, uninstall.Method, uninstall.URL.RequestURI(), timestamp, nonce, uninstallBody))
	uninstallResult := httptest.NewRecorder()
	handler.ServeHTTP(uninstallResult, uninstall)
	if uninstallResult.Code != http.StatusOK {
		t.Fatalf("uninstall status=%d body=%s", uninstallResult.Code, uninstallResult.Body.String())
	}
	if response := heartbeat(version.Build); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked relay credentials status=%d body=%s", response.Code, response.Body.String())
	}
	settings, err := db.ListSettings(t.Context())
	if err != nil || settings[settingSubscriptionRelayURL] != "" {
		t.Fatalf("active relay setting was not cleared: value=%q err=%v", settings[settingSubscriptionRelayURL], err)
	}
}
