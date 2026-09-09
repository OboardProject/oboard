package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func remoteSettingsPost(t *testing.T, baseURL, token, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/ui"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, result
}

func remoteTerminalSettingsStepUp(t *testing.T, baseURL, token string, enabled bool) string {
	t.Helper()
	status, begin := remoteSettingsPost(t, baseURL, token, "/auth/step-up/begin", map[string]any{
		"purpose": "remote_terminal_settings", "resource": map[string]any{"type": "setting", "id": settingRemoteTerminalPasswordConfirmationEnabled + ":" + strconv.FormatBool(enabled)},
	})
	if status != http.StatusOK {
		t.Fatalf("begin: %d %#v", status, begin)
	}
	status, finish := remoteSettingsPost(t, baseURL, token, "/auth/step-up/password", map[string]any{"challenge_id": begin["challenge_id"], "password": "very-secure-password"})
	if status != http.StatusOK {
		t.Fatalf("password: %d %#v", status, finish)
	}
	result, _ := finish["step_up_token"].(string)
	if result == "" {
		t.Fatal("missing step-up token")
	}
	return result
}

func TestRemoteTerminalSettingsRequireFreshScopedStepUp(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	token, _, _ := realtimeLogin(t, server.URL)
	for _, enabled := range []bool{false, true} {
		for _, proof := range []string{"", "invalid"} {
			status, body := remoteSettingsPost(t, server.URL, token, "/settings", map[string]any{settingRemoteTerminalPasswordConfirmationEnabled: enabled, "step_up_token": proof, "mcp_enabled": true})
			if status != http.StatusForbidden {
				t.Fatalf("unverified update: %d %#v", status, body)
			}
		}
		settings := app.runtimeSettings(context.Background())
		if settingBool(settings, settingRemoteTerminalPasswordConfirmationEnabled, true) == enabled || settingBool(settings, settingMCPEnabled, false) {
			t.Fatal("rejected request changed settings")
		}
		proof := remoteTerminalSettingsStepUp(t, server.URL, token, enabled)
		status, body := remoteSettingsPost(t, server.URL, token, "/settings", map[string]any{settingRemoteTerminalPasswordConfirmationEnabled: !enabled, "step_up_token": proof})
		if status != http.StatusForbidden {
			t.Fatalf("wrong direction: %d %#v", status, body)
		}
		update := map[string]any{settingRemoteTerminalPasswordConfirmationEnabled: enabled, "step_up_token": proof}
		status, body = remoteSettingsPost(t, server.URL, token, "/settings", update)
		if status != http.StatusOK {
			t.Fatalf("verified update: %d %#v", status, body)
		}
		if settingBool(app.runtimeSettings(context.Background()), settingRemoteTerminalPasswordConfirmationEnabled, true) != enabled {
			t.Fatal("verified setting not saved")
		}
		status, body = remoteSettingsPost(t, server.URL, token, "/settings", update)
		if status != http.StatusForbidden {
			t.Fatalf("replayed token: %d %#v", status, body)
		}
	}
	for _, apply := range []bool{false, true} {
		input := json.RawMessage(`{"changes":{"remote_terminal_password_confirmation_enabled":false}}`)
		if _, err := app.settingsUpdateCandidate(context.Background(), input, apply); err == nil {
			t.Fatal("automation bypassed step-up")
		}
	}
}
