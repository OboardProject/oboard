package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type publishedPull struct {
	status int
	etag   string
	body   string
}

func TestSubscriptionPublicationIsReusedAndInvalidatedByItsInputs(t *testing.T) {
	ctx := context.Background()
	h, srv, adminToken := setupPlansAPITestServer(t)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", adminToken, map[string]any{"name": "pub-node", "entry_ip_mode": "custom", "entry_address": "203.0.113.5", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", adminToken, map[string]any{"server_id": serverID, "name": "pub-in", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", adminToken, map[string]any{"inbound_id": int64(inbound["id"].(float64)), "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	plan := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", adminToken, map[string]any{
		"name": "pub-plan", "enabled": true, "speed_limit_mbps": 100,
		"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": int64(path["id"].(float64))}},
	}, http.StatusCreated)["subscription_plan"].(map[string]any)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "pub-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(created["id"].(float64))
	token := created["subscription_token"].(string)
	// Bind directly: the two-phase assignment would leave the node waiting for
	// an Agent to confirm delivery, and this test is about what a pull
	// publishes, not about delivery gating.
	if err := srv.store.SetUserPlanBindings(t.Context(), []model.UserPlanBinding{{UserID: userID, PlanID: int64(plan["id"].(float64)), Enabled: true}}); err != nil {
		t.Fatal(err)
	}

	pull := func(ifNoneMatch string) publishedPull {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return publishedPull{status: response.Code, etag: response.Header().Get("ETag"), body: response.Body.String()}
	}

	first := pull("")
	if first.status != http.StatusOK || first.etag == "" || first.body == "" {
		t.Fatalf("first pull = %+v", first)
	}
	builds := srv.subscriptionPublicationBuilds.Load()
	second := pull("")
	if second.status != http.StatusOK || second.body != first.body || second.etag != first.etag {
		t.Fatalf("unchanged pull produced a different body: %+v vs %+v", second, first)
	}
	if srv.subscriptionPublicationBuilds.Load() != builds {
		t.Fatal("an unchanged pull rebuilt the subscription instead of reusing what was published")
	}
	if hits := srv.subscriptionPublicationHits.Load(); hits == 0 {
		t.Fatal("the published body was not reused")
	}

	// A conditional request is answered without rebuilding.
	conditional := pull(first.etag)
	if conditional.status != http.StatusNotModified {
		t.Fatalf("conditional pull = %+v", conditional)
	}
	if srv.subscriptionPublicationBuilds.Load() != builds {
		t.Fatal("a conditional request rebuilt the subscription")
	}

	// Any input change has to produce a different key, so the body is rebuilt
	// rather than reused. Rotating the credentials the body embeds is the case
	// that matters most: reusing there would hand a client a credential the
	// node no longer accepts. (That the rebuilt body then rotates the ETag when
	// its content really differs is covered by the template test in
	// subscription_format_http_test.go.)
	user, err := srv.store.GetUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	user.ProxyUUID = "22222222-2222-2222-2222-222222222222"
	user.ProxyPassword = "rotated-proxy-password"
	if err := srv.store.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if rotated := pull(""); rotated.status != http.StatusOK {
		t.Fatalf("pull after rotation = %+v", rotated)
	}
	if srv.subscriptionPublicationBuilds.Load() == builds {
		t.Fatal("a credential rotation served the previously published body")
	}
}

// TestBurnAfterReadSubscriptionIsNeverPublished keeps the one-time semantics
// intact: the payload is spent by the request that receives it, so it must not
// be retained or reused, and the second pull must not be answered from a cache.
func TestBurnAfterReadSubscriptionIsNeverPublished(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "burn-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(created["id"].(float64))
	token := created["subscription_token"].(string)
	request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/users/%d/subscription-token/policy", userID), adminToken, map[string]any{"burn_after_read": true}, http.StatusOK)

	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil))
	if response.Code != http.StatusOK || response.Header().Get("X-OBoard-Subscription") != "burned-after-read" {
		t.Fatalf("burn pull status=%d header=%q", response.Code, response.Header().Get("X-OBoard-Subscription"))
	}
	if hits := srv.subscriptionPublicationHits.Load(); hits != 0 {
		t.Fatalf("a burn-after-read pull was served from a published body: hits=%d", hits)
	}
	// Even a conditional request must receive the body it just spent.
	etag := response.Header().Get("ETag")
	repeat := httptest.NewRecorder()
	conditionalRequest := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil)
	conditionalRequest.Header.Set("If-None-Match", etag)
	h.ServeHTTP(repeat, conditionalRequest)
	if repeat.Code == http.StatusNotModified {
		t.Fatal("a spent one-time credential was answered with 304 and no body")
	}
}
