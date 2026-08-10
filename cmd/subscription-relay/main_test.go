package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/controller"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/subrelay"
)

func TestRelaySignsAndRestrictsRequests(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	relayID := "7"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := subrelay.Verify(secret, relayID, r.Method, r.URL.RequestURI(), r.Header.Get(subrelay.HeaderTimestamp), r.Header.Get(subrelay.HeaderNonce), r.Header.Get(subrelay.HeaderClientIP), r.UserAgent(), r.Header.Get("If-None-Match"), r.Header.Get(subrelay.HeaderSignature), time.Now()); err != nil {
			t.Error(err)
		}
		if got := r.Header.Get(subrelay.HeaderRelayID); got != relayID {
			t.Errorf("relay ID = %q", got)
		}
		if got := r.Header.Get(subrelay.HeaderClientIP); got != "203.0.113.9" {
			t.Errorf("client IP = %q", got)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Subscription-Encryption", "age")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Set-Cookie", "secret=value")
		_, _ = w.Write([]byte("subscription"))
	}))
	defer upstream.Close()
	target, _ := validateUpstream(upstream.URL, true)
	handler := &relay{upstream: target, id: relayID, secret: secret, client: upstream.Client()}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/token?format=mihomo", nil)
	request.RemoteAddr = "203.0.113.9:1234"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != "subscription" {
		t.Fatalf("unexpected response: %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Set-Cookie") != "" {
		t.Fatal("sensitive upstream response header forwarded")
	}
	if recorder.Header().Get("Subscription-Encryption") != "age" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatal("subscription protocol response headers were dropped")
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v2/ui/settings", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("management path status = %d", recorder.Code)
	}
}

func TestRelayTrustsForwardedIPOnlyFromConfiguredProxy(t *testing.T) {
	prefixes, err := parseTrustedProxies("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	handler := &relay{trustedProxies: prefixes}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/token", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.7, 127.0.0.2")
	if got, err := handler.clientIP(request); err != nil || got != "203.0.113.7" {
		t.Fatalf("trusted proxy client = %q, %v", got, err)
	}
	request.RemoteAddr = "198.51.100.10:1234"
	if got, err := handler.clientIP(request); err != nil || got != "198.51.100.10" {
		t.Fatalf("untrusted peer client = %q, %v", got, err)
	}
}

func TestRelayBasePathAndHealth(t *testing.T) {
	if !allowedPath("/hidden/api/v1/subscriptions/token", "/hidden") {
		t.Fatal("subscription under the configured base path was rejected")
	}
	if allowedPath("/other/api/v1/subscriptions/token", "/hidden") {
		t.Fatal("subscription outside the configured base path was accepted")
	}
	target, _ := validateUpstream("https://controller.example/hidden", false)
	handler := &relay{upstream: target}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hidden/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("base-path health status = %d", recorder.Code)
	}
}

func TestRelayControllerEndToEnd(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	masterSecret := "controller-session-secret-at-least-32"
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := &model.User{Username: "relay-user", Nickname: "Relay User", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "proxy-password", SubscriptionToken: "relay-subscription-token", LegacyProxyEnabled: true}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	encrypted, err := security.EncryptSecret(masterSecret, "subscription-relay-signing-secret", secret)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	managed := &model.SubscriptionRelay{Name: "relay", PublicURL: "https://relay.example", Status: "pending", EnrollmentHash: security.HashSecret("enroll-token"), EnrollmentExpiresAt: &expiresAt}
	if err := db.CreateSubscriptionRelay(context.Background(), managed); err != nil {
		t.Fatal(err)
	}
	managed, err = db.ClaimSubscriptionRelayEnrollment(context.Background(), security.HashSecret("enroll-token"), security.HashSecret("relay-token"), encrypted)
	if err != nil {
		t.Fatal(err)
	}
	app := controller.New(db, masterSecret, "", "", nil)
	defer app.Close()
	upstream := httptest.NewServer(app.Handler())
	defer upstream.Close()
	target, err := validateUpstream(upstream.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &relay{upstream: target, id: strconv.FormatInt(managed.ID, 10), secret: secret, client: upstream.Client()}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/relay-subscription-token?format=sing-box", nil)
	request.RemoteAddr = "203.0.113.20:1234"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"outbounds"`) {
		t.Fatalf("end-to-end subscription response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
