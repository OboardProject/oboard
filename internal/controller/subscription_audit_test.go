package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type subscriptionAuditGeoResolver struct{}

func (subscriptionAuditGeoResolver) Lookup(ip string) (model.IPGeography, error) {
	province := map[string]string{"1.1.1.1": "广东", "8.8.8.8": "北京", "9.9.9.9": "上海", "208.67.222.222": "江苏"}[ip]
	return model.IPGeography{CountryCode: "CN", Country: "中国", Province: province, City: province, ISP: "测试网络", Revision: "subscription-test"}, nil
}

func (subscriptionAuditGeoResolver) Status() model.GeoDatabaseStatus {
	return model.GeoDatabaseStatus{Available: true, Provider: "test", Revision: "subscription-test"}
}

func (subscriptionAuditGeoResolver) Close() {}

func TestSubscriptionPullAuditSuspendsBeforeResponseAndAdminResumes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	srv.geoIP = subscriptionAuditGeoResolver{}
	srv.geoIPStatus = srv.geoIP.Status()
	h := srv.Handler()

	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/users", adminToken, map[string]any{"username": "subscription-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	user := created["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	subscriptionToken := user["subscription_token"].(string)

	fetch := func(token, ip, userAgent string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token+"?format=mihomo", nil)
		req.RemoteAddr = "127.0.0.1:43210"
		req.Header.Set("X-Real-IP", ip)
		req.Header.Set("User-Agent", userAgent)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	for index, ip := range []string{"1.1.1.1", "8.8.8.8"} {
		if got := fetch(subscriptionToken, ip, "Mihomo/1.19.0"); got.Code != http.StatusOK || got.Body.Len() == 0 {
			t.Fatalf("seed pull %d status=%d body=%s", index, got.Code, got.Body.String())
		}
	}
	blocked := fetch(subscriptionToken, "9.9.9.9", "Shadowrocket/2.2.0\nignored")
	if blocked.Code != http.StatusForbidden || blocked.Body.String() == "" {
		t.Fatalf("third region status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	stored, err := db.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SubscriptionSuspended || stored.Status != "active" {
		t.Fatalf("subscription suspension changed the wrong state: %#v", stored)
	}

	detail := request(t, h, http.MethodGet, "/api/v1/audit/subscriptions/users/"+itoa(userID), adminToken, nil, http.StatusOK)["subscription_audit_user"].(map[string]any)
	recent := detail["recent"].([]any)
	if len(recent) != 3 {
		t.Fatalf("recent pulls=%d, want 3", len(recent))
	}
	latest := recent[0].(map[string]any)
	if latest["outcome"] != "denied_risk" || latest["client_name"] != "Shadowrocket" || latest["user_agent"] != "Shadowrocket/2.2.0ignored" {
		t.Fatalf("unexpected latest audit: %#v", latest)
	}
	overviews := request(t, h, http.MethodGet, "/api/v1/audit/risk-overview?window_hours=24", adminToken, nil, http.StatusOK)
	for _, key := range []string{"connection_audit", "subscription_audit", "audit_risk"} {
		if overviews[key] == nil {
			t.Fatalf("combined audit response missing %q: %#v", key, overviews)
		}
	}

	operatorHash, err := security.HashPassword("long-operator-password")
	if err != nil {
		t.Fatal(err)
	}
	operator := &model.User{Username: "operator", PasswordHash: operatorHash, Role: model.RoleOperator, Status: "active", ProxyUUID: "operator-uuid", ProxyPassword: "operator-password"}
	if err := db.CreateUser(context.Background(), operator); err != nil {
		t.Fatal(err)
	}
	operatorToken, err := security.SignSession("test-secret", security.TokenClaims{Subject: operator.ID, Role: string(model.RoleOperator), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodPost, "/api/v1/users/"+itoa(userID)+"/subscription-access/resume", operatorToken, map[string]any{}, http.StatusForbidden)
	request(t, h, http.MethodPost, "/api/v1/users/"+itoa(userID)+"/subscription-access/resume", adminToken, map[string]any{}, http.StatusOK)
	if got := fetch(subscriptionToken, "208.67.222.222", "sing-box/1.12"); got.Code != http.StatusOK {
		t.Fatalf("resumed pull status=%d body=%s", got.Code, got.Body.String())
	}

	if got := fetch("invalid-token", "1.0.0.1", "attacker"); got.Code != http.StatusNotFound {
		t.Fatalf("invalid token status=%d", got.Code)
	}
	detail = request(t, h, http.MethodGet, "/api/v1/audit/subscriptions/users/"+itoa(userID), adminToken, nil, http.StatusOK)["subscription_audit_user"].(map[string]any)
	if len(detail["recent"].([]any)) != 4 {
		t.Fatal("invalid token request entered a user audit")
	}
}

func TestSubscriptionAuditPolicySettingsValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	policy := store.DefaultSubscriptionAuditPolicy()
	settings := request(t, h, http.MethodPost, "/api/v1/settings", adminToken, map[string]any{"subscription_audit_policy": policy}, http.StatusOK)["settings"].(map[string]any)
	if settings[settingSubscriptionAuditPolicy] == nil {
		t.Fatal("subscription audit policy missing from public settings")
	}
	policy.Long.RegionLimit = 2
	request(t, h, http.MethodPost, "/api/v1/settings", adminToken, map[string]any{"subscription_audit_policy": policy}, http.StatusBadRequest)
}
