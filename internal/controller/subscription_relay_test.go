package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/subrelay"
)

func TestSubscriptionRelayAuthenticatesClientIPAndRejectsReplay(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	masterSecret := "controller-session-secret-at-least-32"
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	encrypted, err := security.EncryptSecret(masterSecret, subscriptionRelaySecretPurpose, secret)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	relay := &model.SubscriptionRelay{Name: "relay", PublicURL: "https://relay.example", Status: "pending", EnrollmentHash: security.HashSecret("enroll-token"), EnrollmentExpiresAt: &expiresAt}
	if err := db.CreateSubscriptionRelay(context.Background(), relay); err != nil {
		t.Fatal(err)
	}
	relay, err = db.ClaimSubscriptionRelayEnrollment(context.Background(), security.HashSecret("enroll-token"), security.HashSecret("relay-token"), encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), settingSubscriptionRelayURL, relay.PublicURL); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: db, sessionSecret: masterSecret, subscriptionRelayNonces: map[string]time.Time{}}
	handler := s.withSubscriptionRelay(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clientIP(r)))
	}))
	request := httptest.NewRequest(http.MethodGet, "https://controller.example/api/v1/subscriptions/token?format=mihomo", nil)
	request.RemoteAddr = "10.0.0.4:1234"
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "0123456789abcdef01234567"
	client := "203.0.113.8"
	relayID := strconv.FormatInt(relay.ID, 10)
	request.Header.Set(subrelay.HeaderTimestamp, timestamp)
	request.Header.Set(subrelay.HeaderNonce, nonce)
	request.Header.Set(subrelay.HeaderClientIP, client)
	request.Header.Set(subrelay.HeaderRelayID, relayID)
	request.Header.Set(subrelay.HeaderSignature, subrelay.Sign(secret, relayID, request.Method, request.URL.RequestURI(), timestamp, nonce, client, "", ""))

	direct := httptest.NewRecorder()
	handler.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "https://controller.example/api/v1/subscriptions/token?format=mihomo", nil))
	if direct.Code != http.StatusNotFound {
		t.Fatalf("direct subscription status = %d", direct.Code)
	}
	if err := db.SetSetting(context.Background(), settingSubscriptionControllerDirectEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	direct = httptest.NewRecorder()
	handler.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "https://controller.example/api/v1/subscriptions/token?format=mihomo", nil))
	if direct.Code != http.StatusOK {
		t.Fatalf("enabled direct subscription status = %d", direct.Code)
	}
	if err := db.SetSetting(context.Background(), settingSubscriptionControllerDirectEnabled, "false"); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != client {
		t.Fatalf("unexpected relay response: %d %q", recorder.Code, recorder.Body.String())
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, request.Clone(request.Context()))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", replay.Code)
	}
}

func TestSubscriptionRelayLeavesControllerRouteAvailableWithoutActiveRelay(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{store: db, subscriptionRelayNonces: map[string]time.Time{}}
	handler := s.withSubscriptionRelay(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://controller.example/api/v1/subscriptions/token", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("direct subscription status = %d", recorder.Code)
	}
}

func TestSubscriptionRelayPathFollowsControllerBasePath(t *testing.T) {
	s := &Server{basePath: "/hidden"}
	s.basePaths.Store(&basePathState{Current: "/hidden"})
	if !s.isSubscriptionRelayPath("/hidden/api/v1/subscriptions/token") || !s.isSubscriptionRelayPath("/hidden/s/alias") {
		t.Fatal("relay paths under Controller base path were rejected")
	}
	if s.isSubscriptionRelayPath("/api/v1/subscriptions/token") || s.isSubscriptionRelayPath("/other/s/alias") {
		t.Fatal("relay path outside Controller base path was accepted")
	}
}
