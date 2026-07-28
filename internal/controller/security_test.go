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

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	opLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	opToken := opLogin["token"].(string)

	u, err := db.GetUserByUsername(context.Background(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	u.Role = model.RoleViewer
	if err := db.UpdateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodGet, "/api/v1/servers", opToken, nil, http.StatusForbidden)

	u.Status = "disabled"
	if err := db.UpdateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodGet, "/api/v1/dashboard/summary", opToken, nil, http.StatusUnauthorized)
}

func TestLogoutAndAdminSessionRevocationInvalidateTokens(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "member", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	memberID := int64(created["user"].(map[string]any)["id"].(float64))

	firstLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "member", "password": "long-user-password"}, http.StatusOK)
	firstToken := firstLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/auth/logout", firstToken, map[string]any{}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/me", firstToken, nil, http.StatusUnauthorized)

	secondLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "member", "password": "long-user-password"}, http.StatusOK)
	secondToken := secondLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/users/"+itoa(memberID)+"/sessions/revoke", adminToken, map[string]any{}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/me", secondToken, nil, http.StatusUnauthorized)
}

func TestCookieSessionsRequireCSRFForWrites(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"very-secure-password"}`))
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
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookies: %#v", loginResponse.Result().Cookies())
	}

	readRequest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	readRequest.AddCookie(sessionCookie)
	readResponse := httptest.NewRecorder()
	h.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated read status = %d body=%s", readResponse.Code, readResponse.Body.String())
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	h.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusForbidden {
		t.Fatalf("cookie-authenticated write without CSRF status = %d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}

	logoutRequest = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutRequest.Header.Set("X-OBoard-CSRF", loginPayload.CSRFToken)
	logoutResponse = httptest.NewRecorder()
	h.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated write with CSRF status = %d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
}

func TestSessionCookieSecureAttributeFollowsTrustedTransport(t *testing.T) {
	srv := &Server{}
	assertSecure := func(name string, request *http.Request, want bool) {
		t.Helper()
		response := httptest.NewRecorder()
		srv.setSessionCookie(response, request, "session-token")
		cookies := response.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Secure != want || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Fatalf("%s cookie = %#v, want secure=%v with HttpOnly and SameSite=Strict", name, cookies, want)
		}
	}

	t.Setenv("OBOARD_TRUST_PROXY", "false")
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

	t.Setenv("OBOARD_TRUST_PROXY", "true")
	assertSecure("configured HTTPS proxy", untrustedProxy, true)
}

func TestClientIPUsesHeadersOnlyFromTrustedProxy(t *testing.T) {
	t.Setenv("OBOARD_TRUST_PROXY", "false")

	localProxy := httptest.NewRequest(http.MethodGet, "http://controller/", nil)
	localProxy.RemoteAddr = "127.0.0.1:53000"
	localProxy.Header.Set("X-Real-IP", "198.51.100.7")
	localProxy.Header.Set("X-Forwarded-For", "192.0.2.9, 198.51.100.8")
	if got := clientIP(localProxy); got != "198.51.100.7" {
		t.Fatalf("local proxy client IP = %q, want 198.51.100.7", got)
	}

	localProxy.Header.Del("X-Real-IP")
	if got := clientIP(localProxy); got != "198.51.100.8" {
		t.Fatalf("local proxy forwarded client IP = %q, want 198.51.100.8", got)
	}

	direct := httptest.NewRequest(http.MethodGet, "http://controller/", nil)
	direct.RemoteAddr = "203.0.113.10:53000"
	direct.Header.Set("X-Real-IP", "198.51.100.7")
	direct.Header.Set("X-Forwarded-For", "198.51.100.8")
	if got := clientIP(direct); got != "203.0.113.10" {
		t.Fatalf("direct client IP = %q, want 203.0.113.10", got)
	}
}

func TestUserDisableAndDirectRoleDemotionRevokeSessions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorID := int64(created["user"].(map[string]any)["id"].(float64))

	operatorLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)
	request(t, h, http.MethodPatch, "/api/v1/users/"+itoa(operatorID), adminToken, map[string]any{"role": "viewer"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/servers", operatorToken, nil, http.StatusUnauthorized)

	viewerLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)
	request(t, h, http.MethodPatch, "/api/v1/users/"+itoa(operatorID), adminToken, map[string]any{"status": "disabled"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/me", viewerToken, nil, http.StatusUnauthorized)
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
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: viewer.ID, Role: string(model.RoleViewer), Expiry: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodGet, "/api/v1/agent-tasks/"+itoa(task.ID), token, nil, http.StatusForbidden)
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
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: viewer.ID, Role: string(model.RoleViewer), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodGet, "/api/v1/page-data?page=dashboard", token, nil, http.StatusForbidden)
	page := request(t, h, http.MethodGet, "/api/v1/page-data?page=account", token, nil, http.StatusOK)
	current := page["current_user"].(map[string]any)
	if current["username"] != "viewer" || current["role"] != "viewer" {
		t.Fatalf("unexpected current user: %#v", current)
	}
	for _, sensitive := range []string{"proxy_uuid", "proxy_password", "subscription_token"} {
		if _, ok := current[sensitive]; ok {
			t.Fatalf("current user leaked %s: %#v", sensitive, current)
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

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)

	request(t, h, http.MethodGet, "/api/v1/page-data?page=dns", operatorToken, nil, http.StatusForbidden)
	dnsPage := request(t, h, http.MethodGet, "/api/v1/page-data?page=dns", adminToken, nil, http.StatusOK)
	for _, key := range []string{"dns_lists", "server_dns_policies", "dns_benchmarks"} {
		if _, ok := dnsPage[key]; !ok {
			t.Fatalf("DNS settings page missing %s: %#v", key, dnsPage)
		}
	}
	if _, ok := dnsPage["dns_credentials"]; ok {
		t.Fatalf("DNS settings page unexpectedly included domain account data: %#v", dnsPage)
	}

	request(t, h, http.MethodGet, "/api/v1/page-data?page=dns-records", operatorToken, nil, http.StatusForbidden)
	recordsPage := request(t, h, http.MethodGet, "/api/v1/page-data?page=dns-records", adminToken, nil, http.StatusOK)
	for _, key := range []string{"dns_credentials", "inbounds", "servers"} {
		if _, ok := recordsPage[key]; !ok {
			t.Fatalf("domain records page missing %s: %#v", key, recordsPage)
		}
	}
	if _, ok := recordsPage["dns_lists"]; ok {
		t.Fatalf("domain records page unexpectedly included resolver settings: %#v", recordsPage)
	}

	settingsPage := request(t, h, http.MethodGet, "/api/v1/page-data?page=settings", adminToken, nil, http.StatusOK)
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

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "owner", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "owner", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	page := request(t, h, http.MethodGet, "/api/v1/page-data?page=users", adminToken, nil, http.StatusOK)
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
	request(t, h, http.MethodDelete, "/api/v1/user-groups/"+itoa(adminGroupID), adminToken, nil, http.StatusBadRequest)
	request(t, h, http.MethodDelete, "/api/v1/user-group-members/"+itoa(ownerMembershipID), adminToken, nil, http.StatusBadRequest)
	request(t, h, http.MethodDelete, "/api/v1/users/"+itoa(ownerID), adminToken, nil, http.StatusBadRequest)

	created := request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "member", "nickname": "普通成员", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	memberID := int64(created["user"].(map[string]any)["id"].(float64))
	page = request(t, h, http.MethodGet, "/api/v1/page-data?page=users", adminToken, nil, http.StatusOK)
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

	adminGroup := request(t, h, http.MethodPost, "/api/v1/user-groups", adminToken, map[string]any{"name": "运维管理员", "role": "admin", "enabled": true}, http.StatusCreated)
	groupID := int64(adminGroup["user_group"].(map[string]any)["id"].(float64))
	membership := request(t, h, http.MethodPost, "/api/v1/user-group-members", adminToken, map[string]any{"group_id": groupID, "user_id": memberID, "enabled": true}, http.StatusCreated)
	membershipID := int64(membership["user_group_member"].(map[string]any)["id"].(float64))
	memberLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "member", "password": "long-user-password"}, http.StatusOK)
	memberToken := memberLogin["token"].(string)
	if memberLogin["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("group role did not promote member: %#v", memberLogin)
	}
	request(t, h, http.MethodGet, "/api/v1/users", memberToken, nil, http.StatusOK)
	request(t, h, http.MethodDelete, "/api/v1/user-group-members/"+itoa(membershipID), adminToken, nil, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/users", memberToken, nil, http.StatusForbidden)

	request(t, h, http.MethodPatch, "/api/v1/me", memberToken, map[string]any{"nickname": "新昵称"}, http.StatusOK)
	account := request(t, h, http.MethodGet, "/api/v1/page-data?page=account", memberToken, nil, http.StatusOK)
	if account["account_user"].(map[string]any)["nickname"] != "新昵称" {
		t.Fatalf("nickname was not updated: %#v", account)
	}
	request(t, h, http.MethodPost, "/api/v1/auth/password", memberToken, map[string]any{"current_password": "long-user-password", "new_password": "new-long-password"}, http.StatusOK)
	newLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "member", "password": "new-long-password"}, http.StatusOK)
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

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)

	sourceResponse := request(t, h, http.MethodPost, "/api/v1/servers", adminToken, map[string]any{"name": "source", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	sourceID := int64(sourceResponse["server"].(map[string]any)["id"].(float64))
	targetResponse := request(t, h, http.MethodPost, "/api/v1/servers", adminToken, map[string]any{"name": "target", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20010}, http.StatusCreated)
	targetID := int64(targetResponse["server"].(map[string]any)["id"].(float64))
	forwardResponse := request(t, h, http.MethodPost, "/api/v1/port-forwards", operatorToken, map[string]any{"name": "probe", "source_server_id": sourceID, "target_server_id": targetID, "listen_ip": "0.0.0.0", "listen_port": 10000, "target_port": 20000, "protocol": "tcp", "backend": "auto", "probe_mode": "apply", "priority": 100, "config_json": "{}"}, http.StatusCreated)
	forwardID := int64(forwardResponse["port_forward"].(map[string]any)["id"].(float64))

	for _, endpoint := range []string{
		"/api/v1/servers/" + itoa(sourceID) + "/agent-config",
		"/api/v1/servers/" + itoa(sourceID) + "/agent-update",
		"/api/v1/servers/" + itoa(sourceID) + "/diagnose",
		"/api/v1/servers/" + itoa(sourceID) + "/logs",
		"/api/v1/servers/" + itoa(sourceID) + "/enroll-token",
	} {
		request(t, h, http.MethodPost, endpoint, operatorToken, map[string]any{}, http.StatusForbidden)
	}
	probe := request(t, h, http.MethodPost, "/api/v1/port-forwards/"+itoa(forwardID)+"/probe", operatorToken, map[string]any{}, http.StatusAccepted)
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
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(sourceID)+"/agent-config", adminToken, map[string]any{"restart_command": "sh -c reboot"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(sourceID)+"/agent-config", adminToken, map[string]any{"time_sync_interval_seconds": 1}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(sourceID)+"/agent-config", adminToken, map[string]any{"heartbeat_interval_seconds": 30}, http.StatusBadRequest)
}

func TestEnrollmentTokenIsOneTimeAndAgentAuthUsesConstantTimePath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/servers", adminToken, map[string]any{"name": "node-1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(created["server"].(map[string]any)["id"].(float64))
	enroll := request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/enroll-token", adminToken, map[string]any{}, http.StatusOK)
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
	if err := db.CreateInboundUser(ctx, &model.InboundUser{InboundID: inboundB.ID, UserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
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
	if err := db.CreateInboundUser(ctx, &model.InboundUser{InboundID: inboundA.ID, UserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
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
	if err := db.CreateInboundUser(ctx, &model.InboundUser{InboundID: root.ID, UserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Name: "source-forward-processing", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
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
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	// short password rejected on create
	request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "shorty", "password": "short", "role": "viewer", "status": "active"}, http.StatusBadRequest)

	// change password requires >= 10
	request(t, h, http.MethodPost, "/api/v1/auth/password", adminToken, map[string]any{"current_password": "very-secure-password", "new_password": "shortpwd"}, http.StatusBadRequest)

	server := request(t, h, http.MethodPost, "/api/v1/servers", adminToken, map[string]any{"name": "node", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
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
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"core_binary": "/tmp/evil"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"core_binary": "/usr/local/bin/evil"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"state_dir": "/etc/oboard-agent"}, http.StatusBadRequest)
	// accept managed paths
	request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/agent-config", adminToken, map[string]any{"core_binary": "/usr/local/bin/oboard-sb", "state_dir": "/var/lib/oboard-agent"}, http.StatusAccepted)
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
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	oldToken := login["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/auth/password", oldToken, map[string]any{"current_password": "very-secure-password", "new_password": "brand-new-password"}, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/me", oldToken, nil, http.StatusUnauthorized)
	request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "brand-new-password"}, http.StatusOK)
}

func TestOperatorTaskPayloadScrubsSecrets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "operator", "password": "long-user-password", "role": "operator", "status": "active"}, http.StatusCreated)
	opLogin := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "operator", "password": "long-user-password"}, http.StatusOK)
	opToken := opLogin["token"].(string)

	server := &model.Server{Name: "s1", AgentID: "a1", AgentTokenHash: security.HashSecret("t"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: `{"config":"{\"private_key\":\"abc\"}","reason":"deploy"}`, ResultJSON: `{"message":"ok","token":"secret-token"}`, Status: "succeeded", Nonce: "nonce-secret"}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	out := request(t, h, http.MethodGet, "/api/v1/agent-tasks/"+itoa(task.ID), opToken, nil, http.StatusOK)
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
	adminOut := request(t, h, http.MethodGet, "/api/v1/agent-tasks/"+itoa(task.ID), adminToken, nil, http.StatusOK)
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
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/servers", adminToken, map[string]any{"name": "node", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	id := int64(created["server"].(map[string]any)["id"].(float64))
	enroll := request(t, h, http.MethodPost, "/api/v1/servers/"+itoa(id)+"/enroll-token", adminToken, map[string]any{}, http.StatusOK)
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
		token, err := security.SignSession("test-secret", security.TokenClaims{Subject: u.ID, Role: string(u.Role), Expiry: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	viewerToken := tokenFor(viewer)
	operatorToken := tokenFor(operator)
	h := newTestServer(db, "test-secret", "").Handler()

	for _, path := range []string{"/api/v1/dns-benchmarks", "/api/v1/mtu-detections", "/api/v1/port-forward-probes", "/api/v1/inbound-probes"} {
		request(t, h, http.MethodGet, path, viewerToken, nil, http.StatusForbidden)
		request(t, h, http.MethodGet, path, operatorToken, nil, http.StatusOK)
	}

	// The audit trail records admin activity and must stay admin-only.
	request(t, h, http.MethodGet, "/api/v1/audit-logs", viewerToken, nil, http.StatusForbidden)
	request(t, h, http.MethodGet, "/api/v1/audit-logs", operatorToken, nil, http.StatusForbidden)
}
