package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/subrelay"
	"github.com/OboardProject/oboard/internal/version"
)

func TestSubscriptionPublicBaseURLFallsBackToActiveRelay(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	ctx := t.Context()
	if err := db.SetSetting(ctx, "controller_url", "https://panel.example"); err != nil {
		t.Fatal(err)
	}
	relay := &model.SubscriptionRelay{Name: "relay", PublicURL: "https://relay.example/qzq", Status: "pending"}
	if err := db.CreateSubscriptionRelay(ctx, relay); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSettings(ctx, map[string]string{settingSubscriptionRelayURL: "https://relay.example/qzq", settingSubscriptionControllerDirectEnabled: "false"}); err != nil {
		t.Fatal(err)
	}
	value, err := server.subscriptionPublicBaseURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if value != "https://relay.example/qzq" {
		t.Fatalf("subscription public base URL=%q", value)
	}
	settings, err := db.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if public := server.publicSettings(ctx, settings); public[settingSubscriptionRelayURL] != "https://relay.example/qzq" {
		t.Fatalf("public settings relay URL=%q", public[settingSubscriptionRelayURL])
	}
}

func TestManagedSubscriptionRelayEnrollmentHeartbeatAndUpdate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-session-secret-at-least-32", "").Handler()
	if err := db.SetSetting(t.Context(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	created := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-relays", token, map[string]any{"name": "domestic", "public_url": "https://relay.example"}, http.StatusCreated)
	relay := created["subscription_relay"].(map[string]any)
	relayID := int64(relay["id"].(float64))
	enrollmentToken := created["enrollment_token"].(string)
	if enrollmentToken == "" || relay["active"] != false || relay["enrolled"] != false {
		t.Fatalf("unexpected created relay: %#v", created)
	}
	if preview, _ := relay["install_command_preview"].(string); !strings.Contains(preview, "<one-time-token>") || !strings.Contains(preview, "/install/subscription-relay.sh") || strings.Contains(preview, enrollmentToken) {
		t.Fatalf("relay install preview did not stay non-sensitive: %q", preview)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/subscription-relays/"+strconv.FormatInt(relayID, 10)+"/activate", token, map[string]any{}, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v1/ui/subscription-relays", token, map[string]any{"name": "duplicate", "public_url": "https://relay.example/"}, http.StatusConflict)

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
	settings, err := db.ListSettings(t.Context())
	if err != nil || settings[settingSubscriptionRelayURL] != "https://relay.example" || settings[settingSubscriptionControllerDirectEnabled] != "false" {
		t.Fatalf("initial relay access settings: %#v err=%v", settings, err)
	}
	nodes := request(t, handler, http.MethodGet, "/api/v1/ui/page-data?page=nodes", token, nil, http.StatusOK)
	if base, _ := nodes["subscription_public_base_url"].(string); base != "https://relay.example" {
		t.Fatalf("nodes page subscription base URL=%q", base)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{"subscription_controller_direct_enabled": true}, http.StatusOK)
	settings, err = db.ListSettings(t.Context())
	if err != nil || settings[settingSubscriptionControllerDirectEnabled] != "true" {
		t.Fatalf("direct access setting was not enabled: value=%q err=%v", settings[settingSubscriptionControllerDirectEnabled], err)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/subscription-relays/"+strconv.FormatInt(relayID, 10)+"/update", token, map[string]any{}, http.StatusAccepted)
	if action := checkedHeartbeat("old-build"); action["action"] != "update" || action["target_build"] != version.Build {
		t.Fatalf("unexpected update action %#v", action)
	}
	if action := checkedHeartbeat(version.Build)["action"]; action != "none" {
		t.Fatalf("completed update action %v", action)
	}

	listed := request(t, handler, http.MethodGet, "/api/v1/ui/subscription-relays", token, nil, http.StatusOK)["subscription_relays"].([]any)
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
	settings, err = db.ListSettings(t.Context())
	if err != nil || settings[settingSubscriptionRelayURL] != "" || settings[settingSubscriptionControllerDirectEnabled] != "true" {
		t.Fatalf("relay access settings were not restored: %#v err=%v", settings, err)
	}
}

func TestSubscriptionRelayHeartbeatClearsStaleUpdateError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-session-secret-at-least-32", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-relays", token, map[string]any{"name": "domestic", "public_url": "https://relay.example"}, http.StatusCreated)
	relayID := int64(created["subscription_relay"].(map[string]any)["id"].(float64))
	enrollmentToken := created["enrollment_token"].(string)
	enrolled := request(t, handler, http.MethodPost, "/api/v1/subscription-relay/enroll", "", map[string]any{"enrollment_token": enrollmentToken}, http.StatusOK)
	relayToken := enrolled["relay_token"].(string)
	signingSecret := enrolled["signing_secret"].(string)

	sendHeartbeat := func(updateError *string) map[string]any {
		payload := map[string]any{"version": "dev", "build": "old-build", "commit": "abc", "os": "linux", "arch": "amd64", "service_manager": "systemd"}
		if updateError != nil {
			payload["update_error"] = *updateError
		}
		body, _ := json.Marshal(payload)
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
		if recorder.Code != http.StatusOK {
			t.Fatalf("heartbeat status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		listed := request(t, handler, http.MethodGet, "/api/v1/ui/subscription-relays", token, nil, http.StatusOK)["subscription_relays"].([]any)
		return listed[0].(map[string]any)
	}

	failed := "relay update failed: exit status 2: Cannot mkdir: Function not implemented"
	afterFailure := sendHeartbeat(&failed)
	if afterFailure["status"] != "failed" || afterFailure["last_update_error"] != failed {
		t.Fatalf("failed heartbeat did not persist update error: %#v", afterFailure)
	}
	recovered := sendHeartbeat(nil)
	if recovered["status"] != "online" {
		t.Fatalf("healthy heartbeat did not recover online status: %#v", recovered)
	}
	if errText, _ := recovered["last_update_error"].(string); errText != "" {
		t.Fatalf("healthy heartbeat did not clear stale update error: %#v", recovered)
	}
}

func TestSubscriptionRelaySettingsURLMustMatchEnrolledRelay(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-session-secret-at-least-32", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	ctx := context.Background()
	encrypted, err := security.EncryptSecret("test-session-secret-at-least-32", subscriptionRelaySecretPurpose, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	relay := &model.SubscriptionRelay{Name: "relay", PublicURL: "https://relay.example", Status: "pending", EnrollmentHash: security.HashSecret("enroll-token"), EnrollmentExpiresAt: &expiresAt}
	if err := db.CreateSubscriptionRelay(ctx, relay); err != nil {
		t.Fatal(err)
	}
	// A pending (not yet enrolled) relay URL must not be accepted as the entry.
	request(t, handler, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{"subscription_relay_url": "https://relay.example"}, http.StatusBadRequest)
	if _, err := db.ClaimSubscriptionRelayEnrollment(ctx, security.HashSecret("enroll-token"), security.HashSecret("relay-token"), encrypted); err != nil {
		t.Fatal(err)
	}
	// After enrollment the same URL is a valid entry.
	request(t, handler, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{"subscription_relay_url": "https://relay.example"}, http.StatusOK)
	// A URL without any matching record is rejected.
	request(t, handler, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{"subscription_relay_url": "https://other.example"}, http.StatusBadRequest)
	// Empty clears the entry.
	request(t, handler, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{"subscription_relay_url": ""}, http.StatusOK)
	settings, err := db.ListSettings(ctx)
	if err != nil || settings[settingSubscriptionRelayURL] != "" {
		t.Fatalf("relay URL setting after clear: %#v err=%v", settings, err)
	}
}

func TestSubscriptionRelayActivateRequiresOnlineRelay(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-session-secret-at-least-32", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	ctx := context.Background()
	encrypted, err := security.EncryptSecret("test-session-secret-at-least-32", subscriptionRelaySecretPurpose, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	relay := &model.SubscriptionRelay{Name: "relay", PublicURL: "https://relay.example", Status: "pending", EnrollmentHash: security.HashSecret("enroll-token"), EnrollmentExpiresAt: &expiresAt}
	if err := db.CreateSubscriptionRelay(ctx, relay); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimSubscriptionRelayEnrollment(ctx, security.HashSecret("enroll-token"), security.HashSecret("relay-token"), encrypted); err != nil {
		t.Fatal(err)
	}
	// Enrolled but without a recent heartbeat: activation must be refused so
	// the Controller-direct subscription route is never cut onto a dead relay.
	stale := time.Now().UTC().Add(-3 * time.Minute)
	relay.Status = "online"
	relay.LastSeenAt = &stale
	if err := db.UpdateSubscriptionRelayHeartbeat(ctx, relay); err != nil {
		t.Fatal(err)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/subscription-relays/"+strconv.FormatInt(relay.ID, 10)+"/activate", token, map[string]any{}, http.StatusConflict)
	settings, err := db.ListSettings(ctx)
	if err != nil || settings[settingSubscriptionRelayURL] != "" {
		t.Fatalf("relay URL setting after refused activation: %#v err=%v", settings, err)
	}
	now := time.Now().UTC()
	relay.Status = "online"
	relay.LastSeenAt = &now
	if err := db.UpdateSubscriptionRelayHeartbeat(ctx, relay); err != nil {
		t.Fatal(err)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/subscription-relays/"+strconv.FormatInt(relay.ID, 10)+"/activate", token, map[string]any{}, http.StatusOK)
	settings, err = db.ListSettings(ctx)
	if err != nil || settings[settingSubscriptionRelayURL] != "https://relay.example" {
		t.Fatalf("relay URL setting after activation: %#v err=%v", settings, err)
	}
}
