package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

// TestSubscriptionConditionalRequestNeverBurnsWithoutBody covers a burn-after-read
// bypass. The subscription ETag is only the subscription revision and
// If-None-Match: * matches unconditionally, so anyone holding the link could
// spend a one-time credential and receive 304 with no body. The audit record
// then claims the subscription was served while the client got nothing.
func TestSubscriptionConditionalRequestNeverBurnsWithoutBody(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "conditional-burn", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	user := created["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	persistentToken := user["subscription_token"].(string)

	fetch := func(token, ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	oneTime := request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/one-time", adminToken, map[string]any{}, http.StatusCreated)
	oneTimeToken := oneTime["subscription_token"].(string)

	burned := fetch(oneTimeToken, "*")
	if burned.Code != http.StatusOK {
		t.Fatalf("burning fetch answered %d instead of delivering the body", burned.Code)
	}
	if burned.Header().Get("X-OBoard-Subscription") != "burned-after-read" || burned.Body.Len() == 0 {
		t.Fatalf("burned subscription delivered no content: headers=%#v len=%d", burned.Header(), burned.Body.Len())
	}
	if got := fetch(oneTimeToken, ""); got.Code != http.StatusNotFound {
		t.Fatalf("one-time token survived its single use: status=%d", got.Code)
	}

	// A credential that is not spent by the read keeps ordinary revalidation.
	first := fetch(persistentToken, "")
	if first.Code != http.StatusOK {
		t.Fatalf("persistent fetch status=%d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("persistent fetch returned no ETag")
	}
	if got := fetch(persistentToken, etag); got.Code != http.StatusNotModified {
		t.Fatalf("persistent revalidation status=%d, conditional requests must still work", got.Code)
	}
}
