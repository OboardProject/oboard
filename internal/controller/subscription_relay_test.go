package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/subrelay"
)

func TestSubscriptionRelayAuthenticatesClientIPAndRejectsReplay(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	previous, existed := os.LookupEnv("OBOARD_SUBSCRIPTION_RELAY_SECRET")
	if err := os.Setenv("OBOARD_SUBSCRIPTION_RELAY_SECRET", secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("OBOARD_SUBSCRIPTION_RELAY_SECRET", previous)
		} else {
			_ = os.Unsetenv("OBOARD_SUBSCRIPTION_RELAY_SECRET")
		}
	})
	s := &Server{subscriptionRelayNonces: map[string]time.Time{}}
	handler := s.withSubscriptionRelay(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clientIP(r)))
	}))
	request := httptest.NewRequest(http.MethodGet, "https://controller.example/api/v1/subscriptions/token?format=mihomo", nil)
	request.RemoteAddr = "10.0.0.4:1234"
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "0123456789abcdef01234567"
	client := "203.0.113.8"
	request.Header.Set(subrelay.HeaderTimestamp, timestamp)
	request.Header.Set(subrelay.HeaderNonce, nonce)
	request.Header.Set(subrelay.HeaderClientIP, client)
	request.Header.Set(subrelay.HeaderSignature, subrelay.Sign(secret, request.Method, request.URL.RequestURI(), timestamp, nonce, client, "", ""))

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
