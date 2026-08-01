package controller

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	age "github.com/metacubex/age"
	"github.com/metacubex/age/armor"

	"github.com/OboardProject/oboard/internal/store"
)

func decryptAgeTestPayload(t *testing.T, encrypted []byte, identity age.Identity) string {
	t.Helper()
	reader, err := age.Decrypt(armor.NewReader(bytes.NewReader(encrypted)), identity)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

func TestSubscriptionAgeArmorSupportsMihomoRecipients(t *testing.T) {
	x25519, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	hybrid, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for name, identity := range map[string]age.Identity{
		"x25519": identityWithRecipient{x25519, x25519.Recipient()},
		"hybrid": identityWithRecipient{hybrid, hybrid.Recipient()},
	} {
		t.Run(name, func(t *testing.T) {
			withRecipient := identity.(identityWithRecipient)
			recipient, canonical, err := parseSubscriptionAgeRecipient(withRecipient.recipient.String())
			if err != nil {
				t.Fatal(err)
			}
			if canonical != withRecipient.recipient.String() {
				t.Fatalf("canonical recipient = %q", canonical)
			}
			encrypted, err := encryptSubscriptionAgeArmor("proxies:\n  - name: encrypted\n", recipient)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(encrypted, []byte(armor.Header)) {
				t.Fatalf("missing age armor header: %q", encrypted)
			}
			if got := decryptAgeTestPayload(t, encrypted, withRecipient.Identity); got != "proxies:\n  - name: encrypted\n" {
				t.Fatalf("decrypted payload = %q", got)
			}
		})
	}
	if _, _, err := parseSubscriptionAgeRecipient(x25519.String()); err == nil || !strings.Contains(err.Error(), "do not upload") {
		t.Fatalf("secret key was accepted: %v", err)
	}
}

type identityWithRecipient struct {
	age.Identity
	recipient interface {
		age.Recipient
		String() string
	}
}

func TestSubscriptionAgeAPIOptionalRequiredAndHeaderModes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "age-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	user := created["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	subscriptionToken := user["subscription_token"].(string)
	userLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "age-user", "password": "long-user-password"}, http.StatusOK)
	userToken := userLogin["token"].(string)

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	publicKey := identity.Recipient().String()
	configured := request(t, h, http.MethodPatch, "/api/v2/ui/users/"+itoa(userID)+"/subscription-age", adminToken, map[string]any{"enabled": true, "public_key": publicKey}, http.StatusOK)
	configuredUser := configured["user"].(map[string]any)
	if configuredUser["subscription_age_enabled"] != true || configuredUser["subscription_age_public_key"] != publicKey {
		t.Fatalf("age key was not stored: %#v", configuredUser)
	}
	request(t, h, http.MethodPatch, "/api/v2/ui/users/"+itoa(userID)+"/subscription-age", adminToken, map[string]any{"enabled": true, "public_key": identity.String()}, http.StatusBadRequest)
	request(t, h, http.MethodPatch, "/api/v2/ui/users/"+itoa(userID)+"/subscription-age", userToken, map[string]any{"enabled": true, "public_key": publicKey}, http.StatusForbidden)

	fetch := func(token, query, headerKey string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token+query, nil)
		if headerKey != "" {
			req.Header.Set("X-Age-Public-Key", headerKey)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	plain := fetch(subscriptionToken, "?format=mihomo", "")
	if plain.Code != http.StatusOK || plain.Header().Get("Subscription-Encryption") != "" || bytes.HasPrefix(plain.Body.Bytes(), []byte(armor.Header)) {
		t.Fatalf("optional plain subscription status=%d headers=%#v body=%s", plain.Code, plain.Header(), plain.Body.String())
	}
	encrypted := fetch(subscriptionToken, "?format=mihomo&age=1", "")
	if encrypted.Code != http.StatusOK || encrypted.Header().Get("Subscription-Encryption") != "age" || encrypted.Header().Get("Content-Type") != "application/age" {
		t.Fatalf("optional age subscription status=%d headers=%#v body=%s", encrypted.Code, encrypted.Header(), encrypted.Body.String())
	}
	if decrypted := decryptAgeTestPayload(t, encrypted.Body.Bytes(), identity); decrypted != plain.Body.String() {
		t.Fatalf("decrypted subscription differs from plain output\nplain=%s\ndecrypted=%s", plain.Body.String(), decrypted)
	}

	headerIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	headerEncrypted := fetch(subscriptionToken, "?format=mihomo", headerIdentity.Recipient().String())
	if headerEncrypted.Code != http.StatusOK || headerEncrypted.Header().Get("Subscription-Encryption") != "age" {
		t.Fatalf("header age subscription status=%d headers=%#v", headerEncrypted.Code, headerEncrypted.Header())
	}
	if decrypted := decryptAgeTestPayload(t, headerEncrypted.Body.Bytes(), headerIdentity); decrypted != plain.Body.String() {
		t.Fatalf("header-key decrypted subscription differs from plain output")
	}
	request(t, h, http.MethodPatch, "/api/v2/ui/users/"+itoa(userID)+"/subscription-age", adminToken, map[string]any{"enabled": false, "public_key": publicKey}, http.StatusOK)
	if disabled := fetch(subscriptionToken, "?format=mihomo&age=1", ""); disabled.Code != http.StatusForbidden {
		t.Fatalf("disabled optional age subscription status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	if explicit := fetch(subscriptionToken, "?format=mihomo", headerIdentity.Recipient().String()); explicit.Code != http.StatusOK || explicit.Header().Get("Subscription-Encryption") != "age" {
		t.Fatalf("explicit header age request should work independently of stored opt-in: status=%d", explicit.Code)
	}
	optionalSelf := request(t, h, http.MethodPatch, "/api/v2/ui/me/subscription-age", userToken, map[string]any{"enabled": true, "public_key": publicKey}, http.StatusOK)
	if optionalSelf["user"].(map[string]any)["subscription_age_enabled"] != true {
		t.Fatalf("user could not opt in to optional age encryption: %#v", optionalSelf)
	}
	if unsupported := fetch(subscriptionToken, "?format=sing-box&age=1", ""); unsupported.Code != http.StatusBadRequest {
		t.Fatalf("unsupported age format status=%d body=%s", unsupported.Code, unsupported.Body.String())
	}

	settings := request(t, h, http.MethodPost, "/api/v2/ui/settings", adminToken, map[string]any{"subscription_age_policy": "required"}, http.StatusOK)
	if settings["settings"].(map[string]any)["subscription_age_policy"] != "required" {
		t.Fatalf("required age policy not saved: %#v", settings)
	}
	required := fetch(subscriptionToken, "?format=mihomo", "")
	if required.Code != http.StatusOK || required.Header().Get("Subscription-Encryption") != "age" {
		t.Fatalf("required age subscription status=%d headers=%#v body=%s", required.Code, required.Header(), required.Body.String())
	}
	if decrypted := decryptAgeTestPayload(t, required.Body.Bytes(), identity); decrypted != plain.Body.String() {
		t.Fatalf("required decrypted subscription differs from plain output")
	}
	ordinarySingBox := fetch(subscriptionToken, "", "")
	if ordinarySingBox.Code != http.StatusOK || ordinarySingBox.Header().Get("Subscription-Encryption") != "" {
		t.Fatalf("required policy affected sing-box output: status=%d headers=%#v", ordinarySingBox.Code, ordinarySingBox.Header())
	}

	missing := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "missing-key", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	missingUser := missing["user"].(map[string]any)
	missingID := int64(missingUser["id"].(float64))
	missingToken := missingUser["subscription_token"].(string)
	if got := fetch(missingToken, "?format=mihomo", ""); got.Code != http.StatusPreconditionRequired {
		t.Fatalf("required subscription without key status=%d body=%s", got.Code, got.Body.String())
	}
	missingLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "missing-key", "password": "long-user-password"}, http.StatusOK)
	missingSession := missingLogin["token"].(string)
	selfIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	selfUpdate := request(t, h, http.MethodPatch, "/api/v2/ui/me/subscription-age", missingSession, map[string]any{"enabled": false, "public_key": selfIdentity.Recipient().String()}, http.StatusOK)
	selfUser := selfUpdate["user"].(map[string]any)
	if selfUser["subscription_age_enabled"] != true || selfUser["subscription_age_policy"] != "required" || int64(selfUser["id"].(float64)) != missingID {
		t.Fatalf("required self age configuration was not enforced: %#v", selfUser)
	}
	if got := fetch(missingToken, "?format=mihomo", ""); got.Code != http.StatusOK || got.Header().Get("Subscription-Encryption") != "age" {
		t.Fatalf("self-configured required subscription status=%d headers=%#v", got.Code, got.Header())
	}

	request(t, h, http.MethodPatch, "/api/v2/ui/users/"+itoa(userID)+"/subscription-token/policy", adminToken, map[string]any{"burn_after_read": true}, http.StatusOK)
	rotated := request(t, h, http.MethodPost, "/api/v2/ui/users/"+itoa(userID)+"/subscription-token/rotate", adminToken, map[string]any{}, http.StatusOK)
	oneTimeToken := rotated["subscription_token"].(string)
	if got := fetch(oneTimeToken, "?format=mihomo", ""); got.Code != http.StatusOK || got.Header().Get("Subscription-Encryption") != "age" || got.Header().Get("X-OBoard-Subscription") != "burned-after-read" {
		t.Fatalf("one-time age subscription status=%d headers=%#v", got.Code, got.Header())
	}
	if got := fetch(oneTimeToken, "?format=mihomo", ""); got.Code != http.StatusNotFound {
		t.Fatalf("burned age subscription remained valid: status=%d body=%s", got.Code, got.Body.String())
	}
}
