package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestTOTPEnrollmentLoginReplayProtectionAndRecoveryCodes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-session-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	oldToken := login["token"].(string)

	request(t, h, http.MethodPost, "/api/v1/me/totp/setup/begin", oldToken, map[string]any{"current_password": "wrong-password"}, http.StatusForbidden)
	setup := request(t, h, http.MethodPost, "/api/v1/me/totp/setup/begin", oldToken, map[string]any{"current_password": "very-secure-password"}, http.StatusOK)
	secret := setup["secret"].(string)
	if secret == "" || setup["qr_data_url"] == "" {
		t.Fatalf("incomplete setup response: %#v", setup)
	}
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetUserAuthentication(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TOTPSecretEncrypted == "" || stored.TOTPSecretEncrypted == secret {
		t.Fatalf("TOTP secret was not encrypted at rest: %#v", stored)
	}

	enrollmentCode, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	confirmed := request(t, h, http.MethodPost, "/api/v1/me/totp/setup/confirm", oldToken, map[string]any{"code": enrollmentCode}, http.StatusOK)
	newToken := confirmed["token"].(string)
	recoveryCodes := confirmed["recovery_codes"].([]any)
	if len(recoveryCodes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(recoveryCodes))
	}
	request(t, h, http.MethodGet, "/api/v1/me", oldToken, nil, http.StatusUnauthorized)
	request(t, h, http.MethodGet, "/api/v1/me", newToken, nil, http.StatusOK)

	passwordLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	if required, _ := passwordLogin["two_factor_required"].(bool); !required || passwordLogin["token"] != nil {
		t.Fatalf("password login did not require TOTP: %#v", passwordLogin)
	}
	challenge := passwordLogin["challenge_token"].(string)
	request(t, h, http.MethodPost, "/api/v1/auth/totp/verify", "", map[string]any{"challenge_token": challenge, "code": enrollmentCode}, http.StatusUnauthorized)

	nextCode, err := totp.GenerateCode(secret, time.Now().UTC().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	verified := request(t, h, http.MethodPost, "/api/v1/auth/totp/verify", "", map[string]any{"challenge_token": challenge, "code": nextCode}, http.StatusOK)
	if verified["token"] == "" {
		t.Fatalf("TOTP login token missing: %#v", verified)
	}

	recoveryLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	recoveryCode := recoveryCodes[0].(string)
	recovered := request(t, h, http.MethodPost, "/api/v1/auth/totp/verify", "", map[string]any{"challenge_token": recoveryLogin["challenge_token"], "code": recoveryCode}, http.StatusOK)
	recoveredToken := recovered["token"].(string)

	reuseLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v1/auth/totp/verify", "", map[string]any{"challenge_token": reuseLogin["challenge_token"], "code": recoveryCode}, http.StatusUnauthorized)
	status := request(t, h, http.MethodGet, "/api/v1/me/authentication", recoveredToken, nil, http.StatusOK)
	if status["totp_enabled"] != true || int(status["recovery_codes_remaining"].(float64)) != 9 {
		t.Fatalf("authentication status after recovery login: %#v", status)
	}
}

func TestMatchingTOTPStepRejectsMalformedCodes(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	now := time.Unix(1_800_000_000, 0).UTC()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	step, ok := matchingTOTPStep(secret, code, now)
	if !ok || step != now.Unix()/30 {
		t.Fatalf("matching step = %d, %v", step, ok)
	}
	for _, invalid := range []string{"", "12345", "1234567", "12a456"} {
		if _, ok := matchingTOTPStep(secret, invalid, now); ok {
			t.Fatalf("malformed code %q accepted", invalid)
		}
	}
}

func TestPasskeyRegistrationBeginRequiresPasswordAndSealsChallenge(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-session-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	perform := func(password string, want int) map[string]any {
		t.Helper()
		body, err := json.Marshal(map[string]any{"current_password": password, "name": "MacBook"})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "https://panel.example/api/v1/me/passkeys/register/begin", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("passkey begin: want %d got %d body=%s", want, rr.Code, rr.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	perform("wrong-password", http.StatusForbidden)
	result := perform("very-secure-password", http.StatusOK)
	challengeToken := result["challenge_token"].(string)
	options := result["options"].(map[string]any)["publicKey"].(map[string]any)
	rp := options["rp"].(map[string]any)
	if rp["id"] != "panel.example" || options["challenge"] == "" {
		t.Fatalf("unexpected passkey options: %#v", options)
	}
	challenge, err := db.GetAuthChallenge(context.Background(), security.HashSecret(challengeToken), authChallengePasskeyRegister)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(challenge.DataEncrypted, "v1.") || strings.Contains(challenge.DataEncrypted, challengeToken) {
		t.Fatalf("passkey challenge was not sealed: %#v", challenge)
	}
}

func TestPasskeyLoginBeginAllowsDiscoverableCredentialWithoutUsername(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-session-secret", "")
	body, err := json.Marshal(map[string]any{"username": ""})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://panel.example/api/v1/auth/passkey/login/begin", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("discoverable passkey begin: want %d got %d body=%s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var result struct {
		Options        map[string]any `json:"options"`
		ChallengeToken string         `json:"challenge_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	publicKey, ok := result.Options["publicKey"].(map[string]any)
	if !ok || publicKey["challenge"] == "" {
		t.Fatalf("discoverable options are incomplete: %#v", result.Options)
	}
	if allowed, exists := publicKey["allowCredentials"]; exists && allowed != nil {
		if items, ok := allowed.([]any); !ok || len(items) != 0 {
			t.Fatalf("discoverable login unexpectedly restricted credentials: %#v", allowed)
		}
	}
	challenge, payload, err := srv.loadAuthChallenge(httptest.NewRequest(http.MethodGet, "https://panel.example/", nil), result.ChallengeToken, authChallengePasskeyLogin)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.UserID != 0 || !payload.Discoverable || payload.WebAuthnSession == nil || len(payload.WebAuthnSession.UserID) != 0 {
		t.Fatalf("discoverable challenge was bound to a username: challenge=%#v payload=%#v", challenge, payload)
	}
}
