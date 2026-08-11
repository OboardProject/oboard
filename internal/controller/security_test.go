package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
)

func TestAuthRechecksUserStatusAndRole(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	opLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	opToken := opLogin["token"].(string)

	u, err := db.GetUserByUsername(context.Background(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	u.Role = model.RoleViewer
	if err := db.UpdateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodGet, "/api/v2/ui/servers", opToken, nil, http.StatusForbidden)

	u.Status = "disabled"
	if err := db.UpdateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodGet, "/api/v2/ui/dashboard/summary", opToken, nil, http.StatusUnauthorized)
}

func TestLogoutAndAdminSessionRevocationInvalidateTokens(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "member", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	memberID := int64(created["user"].(map[string]any)["id"].(float64))

	firstLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "member", "password": "long-user-password"}, http.StatusOK)
	firstToken := firstLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/auth/logout", firstToken, map[string]any{}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/me", firstToken, nil, http.StatusUnauthorized)

	secondLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "member", "password": "long-user-password"}, http.StatusOK)
	secondToken := secondLogin["token"].(string)
	thirdLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "member", "password": "long-user-password"}, http.StatusOK)
	thirdToken := thirdLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/users/"+itoa(memberID)+"/sessions/revoke", adminToken, map[string]any{}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/me", secondToken, nil, http.StatusUnauthorized)
	request(t, h, http.MethodGet, "/api/v2/ui/me", thirdToken, nil, http.StatusUnauthorized)
}

func TestConcurrentLoginsRemainIndependent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	first := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	second := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	if first == second {
		t.Fatal("separate logins returned the same token")
	}

	request(t, h, http.MethodGet, "/api/v2/ui/me", first, nil, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/me", second, nil, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v2/ui/auth/logout", first, map[string]any{}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/me", first, nil, http.StatusUnauthorized)
	request(t, h, http.MethodGet, "/api/v2/ui/me", second, nil, http.StatusOK)
}

func TestCookieSessionsRequireCSRFForWrites(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v2/ui/auth/login", bytes.NewBufferString(`{"username":"admin","password":"very-secure-password"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("cookie login status = %d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginPayload struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginPayload); err != nil || loginPayload.CSRFToken == "" {
		t.Fatalf("cookie login CSRF token missing: payload=%#v err=%v", loginPayload, err)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookies: %#v", loginResponse.Result().Cookies())
	}
	if sessionCookie.MaxAge != int(sessionLifetime/time.Second) || time.Until(sessionCookie.Expires) < sessionLifetime-time.Minute || time.Until(sessionCookie.Expires) > sessionLifetime+time.Minute {
		t.Fatalf("session cookie is not persistent for 24 hours: %#v", sessionCookie)
	}

	readRequest := httptest.NewRequest(http.MethodGet, "/api/v2/ui/auth/session", nil)
	readRequest.AddCookie(sessionCookie)
	readResponse := httptest.NewRecorder()
	h.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated read status = %d body=%s", readResponse.Code, readResponse.Body.String())
	}
	var restored struct {
		CSRFToken string         `json:"csrf_token"`
		User      map[string]any `json:"user"`
	}
	if err := json.Unmarshal(readResponse.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CSRFToken != loginPayload.CSRFToken || restored.User["username"] != "admin" || restored.User["role"] != "admin" {
		t.Fatalf("unexpected restored session: %#v", restored)
	}
	for _, sensitive := range []string{"proxy_uuid", "proxy_password", "subscription_token"} {
		if _, ok := restored.User[sensitive]; ok {
			t.Fatalf("restored session leaked %s: %#v", sensitive, restored.User)
		}
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v2/ui/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	h.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusForbidden {
		t.Fatalf("cookie-authenticated write without CSRF status = %d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}

	logoutRequest = httptest.NewRequest(http.MethodPost, "/api/v2/ui/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutRequest.Header.Set("X-OBoard-CSRF", loginPayload.CSRFToken)
	logoutResponse = httptest.NewRecorder()
	h.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated write with CSRF status = %d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	cleared := logoutResponse.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != sessionCookieName || cleared[0].MaxAge >= 0 || !cleared[0].Expires.Before(time.Now()) {
		t.Fatalf("logout did not clear persistent session cookie: %#v", cleared)
	}
}

func TestCookieSessionRejectsChangedUserAgent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v2/ui/auth/login", bytes.NewBufferString(`{"username":"admin","password":"very-secure-password"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("User-Agent", "OBoard-Browser/1")
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("cookie login status = %d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	sessionCookie := loginResponse.Result().Cookies()[0]

	restoreRequest := httptest.NewRequest(http.MethodGet, "/api/v2/ui/auth/session", nil)
	restoreRequest.Header.Set("User-Agent", "OBoard-Browser/2")
	restoreRequest.AddCookie(sessionCookie)
	restoreResponse := httptest.NewRecorder()
	h.ServeHTTP(restoreResponse, restoreRequest)
	if restoreResponse.Code != http.StatusUnauthorized {
		t.Fatalf("changed user agent status = %d body=%s", restoreResponse.Code, restoreResponse.Body.String())
	}
	cleared := restoreResponse.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != sessionCookieName || cleared[0].MaxAge >= 0 {
		t.Fatalf("changed user agent did not clear session cookie: %#v", cleared)
	}
}

func TestSessionCookieSecureAttributeFollowsTrustedTransport(t *testing.T) {
	srv := &Server{}
	assertSecure := func(name string, request *http.Request, want bool) {
		t.Helper()
		response := httptest.NewRecorder()
		srv.setSessionCookie(response, request, "session-token", time.Now().Add(sessionLifetime))
		cookies := response.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Secure != want || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge != int(sessionLifetime/time.Second) {
			t.Fatalf("%s cookie = %#v, want secure=%v with persistent HttpOnly and SameSite=Lax", name, cookies, want)
		}
	}

	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "")
	assertSecure("direct HTTPS", httptest.NewRequest(http.MethodGet, "https://panel.example/", nil), true)
	assertSecure("local HTTP", httptest.NewRequest(http.MethodGet, "http://localhost/", nil), false)

	localProxy := httptest.NewRequest(http.MethodGet, "http://controller/", nil)
	localProxy.RemoteAddr = "127.0.0.1:53000"
	localProxy.Header.Set("X-Forwarded-Proto", "https")
	assertSecure("local HTTPS proxy", localProxy, true)
	if !webAuthnSupportedForRequest(localProxy) {
		t.Fatal("passkeys should be available through a local HTTPS proxy")
	}

	untrustedProxy := httptest.NewRequest(http.MethodGet, "http://controller/", nil)
	untrustedProxy.RemoteAddr = "203.0.113.10:53000"
	untrustedProxy.Header.Set("X-Forwarded-Proto", "https")
	assertSecure("untrusted proxy header", untrustedProxy, false)
	if webAuthnSupportedForRequest(untrustedProxy) {
		t.Fatal("passkeys should not trust HTTPS headers from a direct client")
	}

	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "203.0.113.0/24")
	assertSecure("configured HTTPS proxy", untrustedProxy, true)
}

func TestClientIPUsesHeadersOnlyFromTrustedProxy(t *testing.T) {
	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "")

	localProxy := httptest.NewRequest(http.MethodGet, "http://controller/", nil)
	localProxy.RemoteAddr = "127.0.0.1:53000"
	localProxy.Header.Set("X-Real-IP", "198.51.100.7")
	localProxy.Header.Set("X-Forwarded-For", "192.0.2.9, 198.51.100.8")
	if got := clientIP(localProxy); got != "198.51.100.8" {
		t.Fatalf("local proxy client IP = %q, want 198.51.100.8", got)
	}

	localProxy.Header.Del("X-Real-IP")
	if got := clientIP(localProxy); got != "198.51.100.8" {
		t.Fatalf("local proxy forwarded client IP = %q, want 198.51.100.8", got)
	}
	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "198.51.100.0/24")
	if got := clientIP(localProxy); got != "192.0.2.9" {
		t.Fatalf("trusted proxy chain client IP = %q, want 192.0.2.9", got)
	}

	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "")
	direct := httptest.NewRequest(http.MethodGet, "http://controller/", nil)
	direct.RemoteAddr = "203.0.113.10:53000"
	direct.Header.Set("X-Real-IP", "198.51.100.7")
	direct.Header.Set("X-Forwarded-For", "198.51.100.8")
	if got := clientIP(direct); got != "203.0.113.10" {
		t.Fatalf("direct client IP = %q, want 203.0.113.10", got)
	}
}

func TestNormalizeTrustedProxyCIDRs(t *testing.T) {
	values, err := normalizeTrustedProxyCIDRs([]string{" 2001:db8::1 ", "192.0.2.9", "::ffff:192.0.2.9", "192.0.2.9/32", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.9/32", "2001:db8::1/128"}
	if fmt.Sprint(values) != fmt.Sprint(want) {
		t.Fatalf("normalized trusted proxies = %v, want %v", values, want)
	}
	for _, invalid := range [][]string{{"not-an-ip"}, {"0.0.0.0/0"}, {"::/0"}, {"0.0.0.0"}} {
		if _, err := normalizeTrustedProxyCIDRs(invalid); err == nil {
			t.Fatalf("invalid trusted proxy list accepted: %v", invalid)
		}
	}
	tooMany := make([]string, 65)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("192.0.2.%d", index)
	}
	if _, err := normalizeTrustedProxyCIDRs(tooMany); err == nil {
		t.Fatal("trusted proxy list larger than 64 entries was accepted")
	}
}

func TestTrustedProxySettingsApplyImmediatelyAndPersist(t *testing.T) {
	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	before, _ := trustedProxyTestRequest(t, handler, http.MethodGet, "/api/v2/ui/me/authentication", token, nil, "172.18.0.2:43000", "198.51.100.40", "https", http.StatusOK)
	if before["passkey_supported"] != false {
		t.Fatalf("untrusted proxy enabled passkeys: %#v", before)
	}

	saved, response := trustedProxyTestRequest(t, handler, http.MethodPost, "/api/v2/ui/settings", token, map[string]any{
		"trusted_proxy_cidrs": []string{"2001:db8::1", "172.18.0.2", "172.18.0.2/32"},
	}, "172.18.0.2:43000", "198.51.100.40", "https", http.StatusOK)
	settings := saved["settings"].(map[string]any)
	if got := fmt.Sprint(settings["trusted_proxy_cidrs"]); got != "[172.18.0.2/32 2001:db8::1/128]" {
		t.Fatalf("saved trusted proxies = %s", got)
	}
	if got := fmt.Sprint(settings["trusted_proxy_environment_cidrs"]); got != "[10.0.0.0/8]" {
		t.Fatalf("environment trusted proxies = %s", got)
	}
	status := saved["reverse_proxy_status"].(map[string]any)
	if status["peer_trusted"] != true || status["https"] != true || status["client_ip"] != "198.51.100.40" {
		t.Fatalf("trusted proxy status = %#v", status)
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("bearer-authenticated settings request unexpectedly set a session cookie")
	}

	after, _ := trustedProxyTestRequest(t, handler, http.MethodGet, "/api/v2/ui/me/authentication", token, nil, "172.18.0.2:43000", "198.51.100.40", "https", http.StatusOK)
	if after["passkey_supported"] != true {
		t.Fatalf("trusted HTTPS proxy did not enable passkeys: %#v", after)
	}
	environmentStatus, _ := trustedProxyTestRequest(t, handler, http.MethodGet, "/api/v2/ui/settings", token, nil, "10.9.8.7:43000", "198.51.100.41", "https", http.StatusOK)
	environmentProxy := environmentStatus["reverse_proxy_status"].(map[string]any)
	if environmentProxy["peer_trusted"] != true || environmentProxy["client_ip"] != "198.51.100.41" {
		t.Fatalf("environment proxy was not additive: %#v", environmentProxy)
	}

	audits, err := db.ListAuditPage(context.Background(), 10, 0, "update")
	if err != nil {
		t.Fatal(err)
	}
	foundSettingsAudit := false
	for _, audit := range audits {
		if audit.Target == "settings" && strings.Contains(audit.Detail, settingTrustedProxyCIDRs) {
			foundSettingsAudit = true
			if audit.IP != "198.51.100.40" {
				t.Fatalf("settings audit IP = %q, want real client IP", audit.IP)
			}
		}
	}
	if !foundSettingsAudit {
		t.Fatal("trusted proxy settings audit was not recorded")
	}

	restarted := newTestServer(db, "test-secret", "").Handler()
	restartedStatus, _ := trustedProxyTestRequest(t, restarted, http.MethodGet, "/api/v2/ui/settings", token, nil, "172.18.0.2:43000", "198.51.100.42", "https", http.StatusOK)
	restartedProxy := restartedStatus["reverse_proxy_status"].(map[string]any)
	if restartedProxy["peer_trusted"] != true || restartedProxy["https"] != true || restartedProxy["client_ip"] != "198.51.100.42" {
		t.Fatalf("persisted trusted proxy status = %#v", restartedProxy)
	}
}

func TestTrustedProxySettingsRefreshSecureSessionCookie(t *testing.T) {
	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "")
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)

	loginBody := bytes.NewBufferString(`{"username":"admin","password":"very-secure-password"}`)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v2/ui/auth/login", loginBody)
	loginRequest.RemoteAddr = "172.19.0.2:43000"
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("X-Forwarded-Proto", "https")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var login map[string]any
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Secure {
		t.Fatalf("untrusted proxy login cookie = %#v, want non-Secure before configuration", cookies)
	}

	settingsBody := bytes.NewBufferString(`{"trusted_proxy_cidrs":["172.19.0.2"]}`)
	settingsRequest := httptest.NewRequest(http.MethodPost, "/api/v2/ui/settings", settingsBody)
	settingsRequest.RemoteAddr = "172.19.0.2:43000"
	settingsRequest.Header.Set("Content-Type", "application/json")
	settingsRequest.Header.Set("X-OBoard-CSRF", login["csrf_token"].(string))
	settingsRequest.Header.Set("X-Forwarded-Proto", "https")
	settingsRequest.AddCookie(cookies[0])
	settingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(settingsResponse, settingsRequest)
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("settings status = %d body=%s", settingsResponse.Code, settingsResponse.Body.String())
	}
	refreshed := settingsResponse.Result().Cookies()
	if len(refreshed) != 1 || !refreshed[0].Secure || refreshed[0].Value != cookies[0].Value {
		t.Fatalf("refreshed trusted proxy cookie = %#v, want same session with Secure", refreshed)
	}
}

func trustedProxyTestRequest(t *testing.T, handler http.Handler, method, path, token string, body any, remoteAddr, forwardedFor, forwardedProto string, wantStatus int) (map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-For", forwardedFor)
	req.Header.Set("X-Real-IP", forwardedFor)
	req.Header.Set("X-Forwarded-Proto", forwardedProto)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != wantStatus {
		t.Fatalf("%s %s: want %d got %d body=%s", method, path, wantStatus, response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result, response
}

func TestUserDisableAndDirectRoleDemotionRevokeSessions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorID := int64(created["user"].(map[string]any)["id"].(float64))

	operatorLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)
	request(t, h, http.MethodPatch, "/api/v2/ui/users/"+itoa(operatorID), adminToken, map[string]any{"role": "viewer"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/servers", operatorToken, nil, http.StatusUnauthorized)

	viewerLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)
	request(t, h, http.MethodPatch, "/api/v2/ui/users/"+itoa(operatorID), adminToken, map[string]any{"status": "disabled"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/me", viewerToken, nil, http.StatusUnauthorized)
}

func TestSafeLogFieldRemovesControlCharacters(t *testing.T) {
	got := safeLogField("GET\r\nforged\x00entry\x7f")
	if got != "GET__forged_entry_" {
		t.Fatalf("safe log field = %q", got)
	}
}

func TestViewerCannotReadTasks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	viewerPass, _ := security.HashPassword("long-user-password")
	viewer := &model.User{Username: "viewer", PasswordHash: viewerPass, Role: model.RoleViewer, Status: "active", ProxyUUID: "u", ProxyPassword: "p", SubscriptionToken: "sub"}
	if err := db.CreateUser(context.Background(), viewer); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "s1", AgentID: "agent-a", AgentTokenHash: security.HashSecret("token-a"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: `{"secret":"payload"}`, ResultJSON: `{"secret":"result"}`, Status: "pending", Nonce: "nonce"}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: viewer.ID, Role: string(model.RoleViewer), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodGet, "/api/v2/ui/agent-tasks/"+itoa(task.ID), token, nil, http.StatusForbidden)
}

func TestPageDataIncludesMinimalCurrentUser(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	password, _ := security.HashPassword("long-user-password")
	viewer := &model.User{Username: "viewer", PasswordHash: password, Role: model.RoleViewer, Status: "active", ProxyUUID: "private-uuid", ProxyPassword: "private-password", SubscriptionToken: "private-subscription"}
	if err := db.CreateUser(context.Background(), viewer); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), settingSubscriptionRelayURL, "https://relay.example"); err != nil {
		t.Fatal(err)
	}
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: viewer.ID, Role: string(model.RoleViewer), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	dashboard := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=dashboard", token, nil, http.StatusOK)
	if _, ok := dashboard["user_overview"].(map[string]any); !ok {
		t.Fatalf("viewer dashboard overview missing: %#v", dashboard)
	}
	request(t, h, http.MethodGet, "/api/v2/ui/dashboard/summary", token, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=plans", token, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v2/ui/subscription-plans", token, nil, http.StatusForbidden)
	page := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=account", token, nil, http.StatusOK)
	current := page["current_user"].(map[string]any)
	if current["username"] != "viewer" || current["role"] != "viewer" {
		t.Fatalf("unexpected current user: %#v", current)
	}
	for _, sensitive := range []string{"proxy_uuid", "proxy_password", "subscription_token"} {
		if _, ok := current[sensitive]; ok {
			t.Fatalf("current user leaked %s: %#v", sensitive, current)
		}
	}

	subscriptions := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=subscriptions", token, nil, http.StatusOK)
	if subscriptions["subscription_public_base_url"] != "https://relay.example" {
		t.Fatalf("viewer subscription base URL = %#v", subscriptions["subscription_public_base_url"])
	}
	self := subscriptions["account_user"].(map[string]any)
	if self["subscription_token"] != "private-subscription" {
		t.Fatalf("self subscription token missing: %#v", self)
	}
	for _, privateAdminField := range []string{"users", "user_groups", "subscription_plans", "subscription_plan_nodes", "user_plan_bindings", "user_node_exceptions", "servers", "settings"} {
		if _, ok := subscriptions[privateAdminField]; ok {
			t.Fatalf("viewer subscription page leaked admin field %s: %#v", privateAdminField, subscriptions)
		}
	}
}

func TestDNSPagesKeepResolverAndDomainRecordsSeparate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)

	request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=dns", operatorToken, nil, http.StatusForbidden)
	dnsPage := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=dns", adminToken, nil, http.StatusOK)
	for _, key := range []string{"dns_lists", "server_dns_policies", "dns_benchmarks"} {
		if _, ok := dnsPage[key]; !ok {
			t.Fatalf("DNS settings page missing %s: %#v", key, dnsPage)
		}
	}
	if _, ok := dnsPage["dns_credentials"]; ok {
		t.Fatalf("DNS settings page unexpectedly included domain account data: %#v", dnsPage)
	}

	request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=dns-records", operatorToken, nil, http.StatusForbidden)
	recordsPage := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=dns-records", adminToken, nil, http.StatusOK)
	for _, key := range []string{"dns_credentials", "inbounds", "servers"} {
		if _, ok := recordsPage[key]; !ok {
			t.Fatalf("domain records page missing %s: %#v", key, recordsPage)
		}
	}
	if _, ok := recordsPage["dns_lists"]; ok {
		t.Fatalf("domain records page unexpectedly included resolver settings: %#v", recordsPage)
	}

	settingsPage := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=settings", adminToken, nil, http.StatusOK)
	if _, ok := settingsPage["dns_lists"]; ok {
		t.Fatalf("system settings page unexpectedly included resolver settings: %#v", settingsPage)
	}
}

func TestBuiltinGroupsAndGroupAdminRole(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "owner", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "owner", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	page := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=users", adminToken, nil, http.StatusOK)
	groups := page["user_groups"].([]any)
	var adminGroupID, usersGroupID int64
	for _, raw := range groups {
		group := raw.(map[string]any)
		switch group["system_key"] {
		case store.UserGroupSystemAdmins:
			adminGroupID = int64(group["id"].(float64))
		case store.UserGroupSystemUsers:
			usersGroupID = int64(group["id"].(float64))
		}
	}
	if adminGroupID == 0 || usersGroupID == 0 {
		t.Fatalf("builtin groups missing: %#v", groups)
	}
	owner := login["user"].(map[string]any)
	ownerID := int64(owner["id"].(float64))
	var ownerMembershipID int64
	for _, raw := range page["user_group_members"].([]any) {
		member := raw.(map[string]any)
		if int64(member["group_id"].(float64)) == adminGroupID && int64(member["user_id"].(float64)) == ownerID {
			ownerMembershipID = int64(member["id"].(float64))
		}
	}
	if ownerMembershipID == 0 {
		t.Fatal("bootstrap admin was not assigned to the administrators group")
	}
	request(t, h, http.MethodDelete, "/api/v2/ui/user-groups/"+itoa(adminGroupID), adminToken, nil, http.StatusBadRequest)
	request(t, h, http.MethodDelete, "/api/v2/ui/user-group-members/"+itoa(ownerMembershipID), adminToken, nil, http.StatusBadRequest)
	request(t, h, http.MethodDelete, "/api/v2/ui/users/"+itoa(ownerID), adminToken, nil, http.StatusBadRequest)

	created := request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "member", "nickname": "普通成员", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	memberID := int64(created["user"].(map[string]any)["id"].(float64))
	page = request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=users", adminToken, nil, http.StatusOK)
	foundDefault := false
	for _, raw := range page["user_group_members"].([]any) {
		member := raw.(map[string]any)
		if int64(member["group_id"].(float64)) == usersGroupID && int64(member["user_id"].(float64)) == memberID {
			foundDefault = true
		}
	}
	if !foundDefault {
		t.Fatal("new viewer was not assigned to the default users group")
	}

	adminGroup := request(t, h, http.MethodPost, "/api/v2/ui/user-groups", adminToken, map[string]any{"name": "运维管理员", "role": "admin", "enabled": true}, http.StatusCreated)
	groupID := int64(adminGroup["user_group"].(map[string]any)["id"].(float64))
	membership := request(t, h, http.MethodPost, "/api/v2/ui/user-group-members", adminToken, map[string]any{"group_id": groupID, "user_id": memberID, "enabled": true}, http.StatusCreated)
	membershipID := int64(membership["user_group_member"].(map[string]any)["id"].(float64))
	memberLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "member", "password": "long-user-password"}, http.StatusOK)
	memberToken := memberLogin["token"].(string)
	if memberLogin["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("group role did not promote member: %#v", memberLogin)
	}
	request(t, h, http.MethodGet, "/api/v2/ui/users", memberToken, nil, http.StatusOK)
	request(t, h, http.MethodDelete, "/api/v2/ui/user-group-members/"+itoa(membershipID), adminToken, nil, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/users", memberToken, nil, http.StatusForbidden)

	request(t, h, http.MethodPatch, "/api/v2/ui/me", memberToken, map[string]any{"nickname": "新昵称"}, http.StatusOK)
	account := request(t, h, http.MethodGet, "/api/v2/ui/page-data?page=account", memberToken, nil, http.StatusOK)
	if account["account_user"].(map[string]any)["nickname"] != "新昵称" {
		t.Fatalf("nickname was not updated: %#v", account)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/auth/password", memberToken, map[string]any{"current_password": "long-user-password", "new_password": "new-long-password"}, http.StatusOK)
	newLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "member", "password": "new-long-password"}, http.StatusOK)
	if newLogin["user"].(map[string]any)["role"] != "viewer" {
		t.Fatalf("group promotion leaked into the user's direct role: %#v", newLogin)
	}
}

func TestAgentHostOperationsRequireAdminButOperatorCanProbeForward(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)

	sourceResponse := request(t, h, http.MethodPost, "/api/v2/ui/servers", adminToken, map[string]any{"name": "source", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	sourceID := int64(sourceResponse["server"].(map[string]any)["id"].(float64))
	targetResponse := request(t, h, http.MethodPost, "/api/v2/ui/servers", adminToken, map[string]any{"name": "target", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20010}, http.StatusCreated)
	targetID := int64(targetResponse["server"].(map[string]any)["id"].(float64))
	forwardResponse := request(t, h, http.MethodPost, "/api/v2/ui/port-forwards", operatorToken, map[string]any{"name": "probe", "source_server_id": sourceID, "target_server_id": targetID, "listen_ip": "0.0.0.0", "listen_port": 10000, "target_port": 20000, "protocol": "tcp", "backend": "auto", "probe_mode": "apply", "priority": 100, "config_json": "{}"}, http.StatusCreated)
	forwardID := int64(forwardResponse["port_forward"].(map[string]any)["id"].(float64))

	for _, endpoint := range []string{
		"/api/v2/ui/servers/" + itoa(sourceID) + "/agent-config",
		"/api/v2/ui/servers/" + itoa(sourceID) + "/agent-update",
		"/api/v2/ui/servers/" + itoa(sourceID) + "/diagnose",
		"/api/v2/ui/servers/" + itoa(sourceID) + "/logs",
		"/api/v2/ui/servers/" + itoa(sourceID) + "/enroll-token",
	} {
		request(t, h, http.MethodPost, endpoint, operatorToken, map[string]any{}, http.StatusForbidden)
	}
	probe := request(t, h, http.MethodPost, "/api/v2/ui/port-forwards/"+itoa(forwardID)+"/probe", operatorToken, map[string]any{}, http.StatusAccepted)
	if probe["task"].(map[string]any)["type"] != "probe_port_forwards" {
		t.Fatalf("unexpected operator probe response: %#v", probe)
	}

	source, err := db.GetServer(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	source.AgentID = "agent-source"
	if err := db.UpdateServer(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(sourceID)+"/agent-config", adminToken, map[string]any{"restart_command": "sh -c reboot"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(sourceID)+"/agent-config", adminToken, map[string]any{"time_sync_interval_seconds": 1}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(sourceID)+"/agent-config", adminToken, map[string]any{"heartbeat_interval_seconds": 30}, http.StatusBadRequest)
}

func TestEnrollmentTokenIsOneTimeAndAgentAuthUsesConstantTimePath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v2/ui/servers", adminToken, map[string]any{"name": "node-1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(created["server"].(map[string]any)["id"].(float64))
	enroll := request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(serverID)+"/enroll-token", adminToken, map[string]any{}, http.StatusOK)
	enrollmentToken := enroll["enrollment_token"].(string)
	if enrollmentToken == "" {
		t.Fatal("missing enrollment token")
	}

	first := request(t, h, http.MethodPost, "/api/v1/agent/enroll", "", map[string]any{
		"enrollment_token": enrollmentToken,
		"health":           map[string]any{"status": "online", "os": "linux", "arch": "amd64", "agent_version": "0.1.0", "agent_build": "1"},
	}, http.StatusOK)
	agentID := first["agent_id"].(string)
	agentToken := first["agent_token"].(string)
	if agentID == "" || agentToken == "" || int64(first["server_id"].(float64)) != serverID {
		t.Fatalf("missing agent credentials: %#v", first)
	}

	// Token must not be reusable after a successful claim.
	request(t, h, http.MethodPost, "/api/v1/agent/enroll", "", map[string]any{
		"enrollment_token": enrollmentToken,
		"health":           map[string]any{"status": "online", "os": "linux"},
	}, http.StatusUnauthorized)

	// Wrong token is rejected.
	bad := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic-reports", bytes.NewReader([]byte(`{"user_id":1,"inbound_id":1,"upload_bytes":1,"download_bytes":1}`)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("Authorization", "Bearer wrong-token")
	h.ServeHTTP(bad, req)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong agent token status = %d body=%s", bad.Code, bad.Body.String())
	}

	stored, err := db.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EnrollmentHash != "" {
		t.Fatalf("enrollment hash should be cleared, got %q", stored.EnrollmentHash)
	}
	if stored.AgentID != agentID {
		t.Fatalf("agent id = %q want %q", stored.AgentID, agentID)
	}
}

func TestAgentTrafficAllowsEmptyPolicySync(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "node-1", AgentID: "agent-1", AgentTokenHash: security.HashSecret("agent-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password-a", SubscriptionToken: "subscription-token"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic-reports", bytes.NewReader([]byte(`{"items":[]}`)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer agent-token")
	rr := httptest.NewRecorder()
	newTestServer(db, "test-secret", "").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty traffic policy sync status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"policies"`) || !strings.Contains(rr.Body.String(), `"accepted_report_ids":[]`) {
		t.Fatalf("unexpected policy sync response: %s", rr.Body.String())
	}
}

func TestAgentTrafficRequiresLocalInboundAuthorizationAndIsIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	serverA := &model.Server{Name: "node-a", AgentID: "agent-a", AgentTokenHash: security.HashSecret("token-a"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	serverB := &model.Server{Name: "node-b", AgentID: "agent-b", AgentTokenHash: security.HashSecret("token-b"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010}
	for _, server := range []*model.Server{serverA, serverB} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	inboundA := &model.Inbound{ServerID: serverA.ID, Name: "a", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10001, Enabled: true, ConfigJSON: "{}"}
	inboundB := &model.Inbound{ServerID: serverB.ID, Name: "b", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 20001, Enabled: true, ConfigJSON: "{}"}
	for _, inbound := range []*model.Inbound{inboundA, inboundB} {
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	user := &model.User{Username: "alice", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password-a", SubscriptionToken: "subscription-token-a"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inboundB.ID)
	h := newTestServer(db, "test-secret", "").Handler()
	report := func(inboundID *int64, reportID string, want int) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"items": []map[string]any{{"report_id": reportID, "user_id": user.ID, "inbound_id": inboundID, "upload_bytes": 10, "download_bytes": 20}}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic-reports", bytes.NewReader(body))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("X-Agent-ID", serverA.AgentID)
		req.Header.Set("Authorization", "Bearer token-a")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("traffic inbound=%v status=%d want=%d body=%s", inboundID, rr.Code, want, rr.Body.String())
		}
	}

	report(nil, "missing-inbound", http.StatusBadRequest)
	report(&inboundB.ID, "cross-server", http.StatusForbidden)
	report(&inboundA.ID, "unauthorized-user", http.StatusForbidden)
	grantTestPlanInboundNode(t, db, user.ID, inboundA.ID)
	report(&inboundA.ID, "authorized-report", http.StatusOK)
	report(&inboundA.ID, "authorized-report", http.StatusOK)
	storedUser, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedUser.TrafficUsedBytes != 30 {
		t.Fatalf("replayed traffic was counted more than once: used=%d", storedUser.TrafficUsedBytes)
	}
}

func TestAgentTrafficAcceptsOnlyTransparentPathProcessingServer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	servers := []*model.Server{
		{Name: "forward-source", AgentID: "agent-source", AgentTokenHash: security.HashSecret("token-source"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010},
		{Name: "processing", AgentID: "agent-processing", AgentTokenHash: security.HashSecret("token-processing"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010},
		{Name: "downstream", AgentID: "agent-downstream", AgentTokenHash: security.HashSecret("token-downstream"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30010},
	}
	for _, server := range servers {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	root := &model.Inbound{ServerID: servers[0].ID, Name: "root", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10001, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, root); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password-a", SubscriptionToken: "subscription-token-a"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Name: "source-forward-processing", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	grantTestPlanNode(t, db, user.ID, model.AssignableNodeProxyPath, path.ID)
	processingID := servers[1].ID
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ServerID: &processingID, ConfigJSON: "{}"}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}

	h := newTestServer(db, "test-secret", "").Handler()
	report := func(agentID, token, reportID string, pathID *int64, want int) map[string]any {
		t.Helper()
		item := map[string]any{"report_id": reportID, "user_id": user.ID, "inbound_id": root.ID, "upload_bytes": 10, "download_bytes": 20}
		if pathID != nil {
			item["path_id"] = *pathID
		}
		body, err := json.Marshal(map[string]any{"items": []map[string]any{item}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic-reports", bytes.NewReader(body))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("agent=%s path=%v status=%d want=%d body=%s", agentID, pathID, rr.Code, want, rr.Body.String())
		}
		var response map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &response)
		return response
	}

	report(servers[0].AgentID, "token-source", "source-without-path", nil, http.StatusBadRequest)
	report(servers[0].AgentID, "token-source", "source-with-path", &path.ID, http.StatusForbidden)
	report(servers[2].AgentID, "token-downstream", "downstream-with-path", &path.ID, http.StatusForbidden)
	response := report(servers[1].AgentID, "token-processing", "processing-with-path", &path.ID, http.StatusOK)
	policies, ok := response["policies"].(map[string]any)
	if !ok || policies[strconv.FormatInt(user.ID, 10)] == nil {
		t.Fatalf("processing server did not receive the user runtime policy: %#v", response)
	}
	storedUser, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedUser.TrafficUsedBytes != 30 {
		t.Fatalf("processing report traffic = %d, want 30", storedUser.TrafficUsedBytes)
	}
}

func TestAgentConfigPathAllowlistAndPasswordPolicy(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	// short password rejected on create
	request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "shorty", "password": "short", "role": "viewer", "status": "active"}, http.StatusBadRequest)

	// change password requires >= 8
	request(t, h, http.MethodPost, "/api/v2/ui/auth/password", adminToken, map[string]any{"current_password": "very-secure-password", "new_password": "shortpw"}, http.StatusBadRequest)

	server := request(t, h, http.MethodPost, "/api/v2/ui/servers", adminToken, map[string]any{"name": "node", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(server["server"].(map[string]any)["id"].(float64))
	src, err := db.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	src.AgentID = "agent-node"
	if err := db.UpdateServer(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	// reject dangerous core_binary / state_dir
	request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"core_binary": "/tmp/evil"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"core_binary": "/usr/local/bin/evil"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"state_dir": "/etc/oboard-agent"}, http.StatusBadRequest)
	// accept managed paths
	request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"core_binary": "/usr/local/bin/oboard-sb", "state_dir": "/var/lib/oboard-agent"}, http.StatusAccepted)
}

func TestValidateAgentManagedPathHelpers(t *testing.T) {
	if err := validateAgentManagedPath("core_binary", "/usr/local/bin/oboard-sb"); err != nil {
		t.Fatal(err)
	}
	if err := validateAgentManagedPath("core_binary", "/tmp/oboard-sb"); err == nil {
		t.Fatal("expected /tmp core_binary rejected")
	}
	if err := validateAgentManagedPath("state_dir", "/var/lib/oboard-agent/data"); err != nil {
		t.Fatal(err)
	}
	if err := validateAgentManagedPath("state_dir", "/etc/passwd"); err == nil {
		t.Fatal("expected /etc state_dir rejected")
	}
	if err := validateAgentServiceName("oboard-sb"); err != nil {
		t.Fatal(err)
	}
	if err := validateAgentServiceName("oboard-sb;reboot"); err == nil {
		t.Fatal("expected bad service name rejected")
	}
}

func TestFailRedactsInternalErrors(t *testing.T) {
	recorder := httptest.NewRecorder()
	fail(recorder, errors.New("sqlite: secret database path"), http.StatusInternalServerError)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "sqlite") || !strings.Contains(recorder.Body.String(), "internal server error") {
		t.Fatalf("internal error was not redacted: %s", recorder.Body.String())
	}
}

func TestPasswordChangeRevokesSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	oldToken := login["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/auth/password", oldToken, map[string]any{"current_password": "very-secure-password", "new_password": "brand-new-password"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v2/ui/me", oldToken, nil, http.StatusUnauthorized)
	request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "brand-new-password"}, http.StatusOK)
}

func TestOperatorTaskPayloadScrubsSecrets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	opLogin := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	opToken := opLogin["token"].(string)

	server := &model.Server{Name: "s1", AgentID: "a1", AgentTokenHash: security.HashSecret("t"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: `{"config":"{\"private_key\":\"abc\"}","reason":"deploy"}`, ResultJSON: `{"message":"ok","token":"secret-token"}`, Status: "succeeded", Nonce: "nonce-secret"}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	out := request(t, h, http.MethodGet, "/api/v2/ui/agent-tasks/"+itoa(task.ID), opToken, nil, http.StatusOK)
	got := out["task"].(map[string]any)
	if got["nonce"] != "<redacted>" {
		t.Fatalf("operator should not see nonce: %#v", got)
	}
	payload := fmt.Sprint(got["payload_json"])
	result := fmt.Sprint(got["result_json"])
	payloadRedacted := strings.Contains(payload, "<redacted>") || strings.Contains(payload, "\\u003credacted\\u003e")
	resultRedacted := strings.Contains(result, "<redacted>") || strings.Contains(result, "\\u003credacted\\u003e")
	if strings.Contains(payload, "abc") || !payloadRedacted {
		t.Fatalf("operator payload not scrubbed: %s", payload)
	}
	if strings.Contains(result, "secret-token") || !resultRedacted {
		t.Fatalf("operator result not scrubbed: %s", result)
	}
	adminOut := request(t, h, http.MethodGet, "/api/v2/ui/agent-tasks/"+itoa(task.ID), adminToken, nil, http.StatusOK)
	adminTask := adminOut["task"].(map[string]any)
	if !strings.Contains(fmt.Sprint(adminTask["payload_json"]), "abc") {
		t.Fatalf("admin should see payload secrets: %#v", adminTask)
	}
}

func TestEnrollmentTokenIncludesExpiry(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v2/ui/servers", adminToken, map[string]any{"name": "node", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	id := int64(created["server"].(map[string]any)["id"].(float64))
	enroll := request(t, h, http.MethodPost, "/api/v2/ui/servers/"+itoa(id)+"/enroll-token", adminToken, map[string]any{}, http.StatusOK)
	if enroll["enrollment_token"] == nil || enroll["expires_at"] == nil {
		t.Fatalf("missing expiry fields: %#v", enroll)
	}
	if int(enroll["expires_in_seconds"].(float64)) != 1800 {
		t.Fatalf("expires_in_seconds=%v", enroll["expires_in_seconds"])
	}
}

// TestDiagnosticRoutesRequireOperator pins the role floor for infrastructure
// diagnostics and the audit trail. Viewers are ordinary proxy users, so they
// must not read resolver benchmarks, MTU/probe results, or the audit log.
func TestDiagnosticRoutesRequireOperator(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	password, _ := security.HashPassword("long-user-password")
	viewer := &model.User{Username: "viewer", PasswordHash: password, Role: model.RoleViewer, Status: "active", ProxyUUID: "u", ProxyPassword: "p", SubscriptionToken: "s"}
	if err := db.CreateUser(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	operator := &model.User{Username: "operator", PasswordHash: password, Role: model.RoleOperator, Status: "active", ProxyUUID: "u2", ProxyPassword: "p2", SubscriptionToken: "s2"}
	if err := db.CreateUser(ctx, operator); err != nil {
		t.Fatal(err)
	}
	tokenFor := func(u *model.User) string {
		token, err := security.SignSession("test-secret", security.TokenClaims{Subject: u.ID, Role: string(u.Role), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	viewerToken := tokenFor(viewer)
	operatorToken := tokenFor(operator)
	h := newTestServer(db, "test-secret", "").Handler()

	for _, path := range []string{"/api/v2/ui/dns-benchmarks", "/api/v2/ui/mtu-detections", "/api/v2/ui/port-forward-probes", "/api/v2/ui/inbound-probes"} {
		request(t, h, http.MethodGet, path, viewerToken, nil, http.StatusForbidden)
		request(t, h, http.MethodGet, path, operatorToken, nil, http.StatusOK)
	}

	// The audit trail records admin activity and must stay admin-only.
	request(t, h, http.MethodGet, "/api/v2/ui/audit-logs", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v2/ui/audit-logs", operatorToken, nil, http.StatusForbidden)
}
