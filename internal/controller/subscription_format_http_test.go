package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestRemovedSubscriptionFormatsReturn400(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sub-format.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "sub-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	token := created["user"].(map[string]any)["subscription_token"].(string)
	for _, format := range []string{"clash", "clash-meta", "mieru"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token+"?format="+format, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("format=%s status=%d body=%s", format, rr.Code, rr.Body.String())
		}
	}
}

func TestBareSubscriptionURLIgnoresUserAgent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sub-bare.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "bare-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	token := created["user"].(map[string]any)["subscription_token"].(string)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil)
	req.Header.Set("User-Agent", "Surge iOS/5.8.0")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "proxies:") || strings.Contains(rr.Header().Get("Vary"), "User-Agent") {
		t.Fatalf("bare URL status=%d vary=%q body=%s", rr.Code, rr.Header().Get("Vary"), rr.Body.String())
	}
}

func TestAutoSubscriptionFormatVariesByUserAgentAndTemplate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sub-auto.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "auto-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	token := created["user"].(map[string]any)["subscription_token"].(string)

	fetch := func(ua string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token+"?format=auto", nil)
		req.Header.Set("User-Agent", ua)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	mihomo := fetch("mihomo/1.19.0")
	surge := fetch("Surge iOS/5.8.0")
	if mihomo.Code != http.StatusOK || surge.Code != http.StatusOK {
		t.Fatalf("auto status mihomo=%d surge=%d", mihomo.Code, surge.Code)
	}
	if !strings.Contains(mihomo.Header().Get("Vary"), "User-Agent") {
		t.Fatalf("auto missing Vary: %q", mihomo.Header().Get("Vary"))
	}
	if mihomo.Header().Get("ETag") == "" || mihomo.Header().Get("ETag") == surge.Header().Get("ETag") {
		t.Fatalf("auto etags equal: %q %q", mihomo.Header().Get("ETag"), surge.Header().Get("ETag"))
	}
	if !strings.Contains(mihomo.Body.String(), "proxies:") || !strings.Contains(surge.Body.String(), "[Proxy]") {
		t.Fatalf("auto bodies not format-specific:\n%s\n---\n%s", mihomo.Body.String(), surge.Body.String())
	}

	listed := request(t, h, http.MethodGet, "/api/v1/ui/subscription-templates/mihomo", adminToken, nil, http.StatusOK)
	item := listed["subscription_template"].(map[string]any)
	content := strings.Replace(item["content"].(string), "mixed-port: 7890", "mixed-port: 17890", 1)
	request(t, h, http.MethodPut, "/api/v1/ui/subscription-templates/mihomo", adminToken, map[string]any{
		"content": content, "expected_revision": item["revision"],
	}, http.StatusOK)
	tasks := request(t, h, http.MethodGet, "/api/v1/ui/agent-tasks", adminToken, nil, http.StatusOK)
	if raw, _ := tasks["tasks"].([]any); len(raw) != 0 {
		t.Fatalf("template save queued tasks: %#v", tasks["tasks"])
	}
	updated := fetch("mihomo/1.19.0")
	if updated.Header().Get("ETag") == mihomo.Header().Get("ETag") {
		t.Fatalf("template change did not rotate ETag: %q", updated.Header().Get("ETag"))
	}
	if !strings.Contains(updated.Body.String(), "mixed-port: 17890") {
		t.Fatalf("custom template not applied:\n%s", updated.Body.String())
	}
	request(t, h, http.MethodPost, "/api/v1/ui/subscription-templates/mihomo/reset", adminToken, map[string]any{}, http.StatusOK)
	restored := fetch("mihomo/1.19.0")
	if restored.Header().Get("ETag") != mihomo.Header().Get("ETag") {
		t.Fatalf("reset etag=%q want %q", restored.Header().Get("ETag"), mihomo.Header().Get("ETag"))
	}
}
