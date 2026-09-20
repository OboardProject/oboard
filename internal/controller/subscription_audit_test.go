package controller

import (
	"context"
	"database/sql"
	"errors"
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

func TestSubscriptionPullAuditKeepsGeographyAdvisory(t *testing.T) {
	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "127.0.0.0/8")
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enableTestAudit(t, db)
	srv := newTestServer(db, "test-secret", "")
	srv.geoIP = subscriptionAuditGeoResolver{}
	srv.geoIPStatus = srv.geoIP.Status()
	h := srv.Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "subscription-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
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
	db.AllowSubscriptionIngress("1.1.1.1", time.Now().Add(-time.Minute))
	for index, ip := range []string{"1.1.1.1", "8.8.8.8"} {
		if got := fetch(subscriptionToken, ip, "Mihomo/1.19.0"); got.Code != http.StatusOK || got.Body.Len() == 0 {
			t.Fatalf("seed pull %d status=%d body=%s", index, got.Code, got.Body.String())
		}
	}
	third := fetch(subscriptionToken, "9.9.9.9", "Shadowrocket/2.2.0\nignored")
	if third.Code != http.StatusOK || third.Body.String() == "" {
		t.Fatalf("third region status=%d body=%s", third.Code, third.Body.String())
	}
	stored, err := db.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionSuspended || stored.Status != "active" {
		t.Fatalf("geography changed subscription state: %#v", stored)
	}

	request(t, h, http.MethodGet, "/api/v1/ui/audit/subscriptions/users/"+itoa(userID), adminToken, nil, http.StatusGone)
	request(t, h, http.MethodGet, "/api/v1/ui/audit/risk-overview?window_hours=24", adminToken, nil, http.StatusGone)

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
	request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-access/resume", operatorToken, map[string]any{}, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-access/resume", adminToken, map[string]any{}, http.StatusOK)
	if got := fetch(subscriptionToken, "208.67.222.222", "sing-box/1.12"); got.Code != http.StatusOK {
		t.Fatalf("resumed pull status=%d body=%s", got.Code, got.Body.String())
	}

	if got := fetch("invalid-token", "1.0.0.1", "attacker"); got.Code != http.StatusNotFound {
		t.Fatalf("invalid token status=%d", got.Code)
	}
	detail, err := db.SubscriptionAuditUserDetail(context.Background(), userID, 24, store.DefaultAuditPolicy())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if len(detail.Recent) != 0 {
		t.Fatal("subscription ingress wrote retired per-request history")
	}
}

func TestSubscriptionAuditPolicySettingsValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	policy := store.DefaultAuditPolicy()
	settings := request(t, h, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"audit_policy": policy}, http.StatusOK)["settings"].(map[string]any)
	if settings[settingAuditPolicy] == nil {
		t.Fatal("audit policy missing from public settings")
	}
	policy.Mode = "custom"
	policy.RoutesPer15Minutes.Hard = policy.RoutesPer15Minutes.Soft
	request(t, h, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"audit_policy": policy}, http.StatusBadRequest)
}

func TestAuditSettingsRoundTripAndValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	initial := request(t, h, http.MethodGet, "/api/v1/ui/settings", adminToken, nil, http.StatusOK)["settings"].(map[string]any)
	auditSettingTrue := func(value any) bool { return value == true || value == "true" }
	if auditSettingTrue(initial[settingAuditEnabled]) || !auditSettingTrue(initial[settingSubscriptionAuditEnabled]) || !auditSettingTrue(initial[settingConnectionAuditEnabled]) || initial[settingAuditAction] != "restrict" {
		t.Fatalf("unexpected audit defaults: %#v", initial)
	}
	if state := srv.auditSettingsState(context.Background()); state.Enabled {
		t.Fatalf("audit runtime must default to disabled: %#v", state)
	}

	updated := request(t, h, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{
		"audit_enabled": true, "subscription_audit_enabled": true, "connection_audit_enabled": true, "audit_action": "warn",
	}, http.StatusOK)["settings"].(map[string]any)
	for key, want := range map[string]string{settingAuditEnabled: "true", settingSubscriptionAuditEnabled: "true", settingConnectionAuditEnabled: "true", settingAuditAction: "warn"} {
		if got := updated[key]; got != want {
			t.Fatalf("setting %s = %#v, want %s", key, got, want)
		}
	}
	request(t, h, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"audit_action": "ban"}, http.StatusBadRequest)
}

func TestSubscriptionPullAuditDisabledServesWithoutRecordingOrSuspending(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	srv.geoIP = subscriptionAuditGeoResolver{}
	srv.geoIPStatus = srv.geoIP.Status()
	h := srv.Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "audit-off-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	subscriptionToken := created["user"].(map[string]any)["subscription_token"].(string)
	userID := int64(created["user"].(map[string]any)["id"].(float64))

	request(t, h, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"audit_enabled": true, "subscription_audit_enabled": false}, http.StatusOK)

	fetch := func(ip string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+subscriptionToken+"?format=mihomo", nil)
		req.RemoteAddr = "127.0.0.1:43210"
		req.Header.Set("X-Real-IP", ip)
		req.Header.Set("User-Agent", "Mihomo/1.19.0")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	db.AllowSubscriptionIngress("1.1.1.1", time.Now().Add(-time.Minute))
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"} {
		if got := fetch(ip); got.Code != http.StatusOK || got.Body.Len() == 0 {
			t.Fatalf("pull from %s status=%d body=%s", ip, got.Code, got.Body.String())
		}
	}
	stored, err := db.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionSuspended {
		t.Fatalf("disabled audit suspended the user: %#v", stored)
	}
	request(t, h, http.MethodGet, "/api/v1/ui/audit/subscriptions/overview?window_hours=24", adminToken, nil, http.StatusGone)
	detail, err := db.SubscriptionAuditUserDetail(context.Background(), userID, 24, store.DefaultAuditPolicy())
	if (err != nil && !errors.Is(err, sql.ErrNoRows)) || len(detail.Recent) != 0 {
		t.Fatalf("disabled audit recorded pulls: %#v, %v", detail, err)
	}
	limited := false
	for i := 0; i < 2*store.DefaultAuditPolicy().RawRequestsPer60Seconds.Hard; i++ {
		got := fetch("1.1.1.1")
		if got.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		if got.Code != http.StatusOK {
			t.Fatalf("unexpected subscription response: %d", got.Code)
		}
	}
	if !limited {
		t.Fatal("disabled behavior audit bypassed raw request rate limiting")
	}
}
