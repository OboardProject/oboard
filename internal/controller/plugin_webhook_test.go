package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/store"
)

func TestWebhookHTTPBasePathAndManagement(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/webhook.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const base = "/private-panel"
	if err = db.SetSettings(ctx, map[string]string{"controller_url": "http://example.com" + base, model.PluginSettingEnabled: "true", model.PluginSettingSchedulerPaused: "false"}); err != nil {
		t.Fatal(err)
	}
	s := New(db, "test-master", "", base, nil)
	h := s.Handler()
	request(t, h, http.MethodPost, base+"/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, base+"/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	user, err := db.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	actor := application.HumanPrincipal(*user, model.RoleAdmin, application.Principal{}.SourceIP)
	p, err := s.plugins.CreatePlugin(ctx, actor, "HTTP webhook", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.plugins.UpdatePlugin(ctx, actor, p.ID, p.Name, p.Description, model.PluginStatusEnabled, "")
	if err != nil {
		t.Fatal(err)
	}
	manifest := model.PluginManifest{SchemaVersion: 1, Runtime: model.PluginRuntimeOBoardJSv1, SDKVersion: model.PluginSDKVersionV1, Entry: "main", Capabilities: []string{model.PluginSDKServersGet}, Params: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"message":{"type":"string"}},"required":["message"]}`)}
	rev, err := s.plugins.SaveDraft(ctx, actor, p.ID, "function main(ctx) { return ctx.params; }", plugin.MustJSON(manifest))
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.plugins.Publish(ctx, actor, p.ID, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := model.PluginTriggerBinding{PluginID: p.ID, RevisionID: rev.ID, Name: "hook", Kind: model.PluginTriggerEvent, SpecJSON: json.RawMessage(`{"timezone":"UTC","event":"plugin.webhook"}`), ParamsJSON: json.RawMessage(`{"message":"fixed"}`), EnvJSON: json.RawMessage(`{}`), Enabled: true, CreatedByUserID: user.ID}
	if err = db.CreatePluginTrigger(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	grant, err := s.plugins.CreateGrant(ctx, actor, model.PluginGrant{PluginID: p.ID, RevisionID: rev.ID, BindingID: &binding.ID, CapabilitiesJSON: plugin.MustJSON(manifest.Capabilities), ResourceScopeJSON: json.RawMessage(`{"servers":{"mode":"none"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	created := request(t, h, http.MethodPost, base+"/api/v1/plugin-webhooks", token, map[string]any{"binding_id": binding.ID, "grant_id": grant.ID}, http.StatusCreated)["data"].(map[string]any)
	key := created["secret"].(string)
	endpoint := created["webhook"].(map[string]any)
	id := endpoint["id"].(string)
	if endpoint["enabled"] != false {
		t.Fatal("endpoint created enabled")
	}
	path := "/api/v1/plugin-webhooks/receive/" + id
	body := `{"message":"external"}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := strings.Repeat("a", 32)
	deliver := func(method, target, payload string, headers func(http.Header), want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(payload))
		req.Header.Set("X-Oboard-Timestamp", ts)
		req.Header.Set("X-Oboard-Nonce", nonce)
		req.Header.Set("X-Oboard-Signature", plugin.WebhookSignature(key, id, ts, nonce, []byte(payload)))
		if headers != nil {
			headers(req.Header)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, target, rr.Code, want, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), key) || strings.Contains(rr.Body.String(), "secret_encrypted") {
			t.Fatal("response leaked secret")
		}
		return rr
	}
	deliver(http.MethodPost, path, body, nil, http.StatusNotFound)
	deliver(http.MethodPost, base+"-other"+path, body, nil, http.StatusNotFound)
	deliver(http.MethodPost, base+path, body, nil, http.StatusUnauthorized)
	request(t, h, http.MethodPatch, base+"/api/v1/plugin-webhooks/"+id, token, map[string]any{"expected_generation": endpoint["generation"], "enabled": true}, http.StatusOK)
	listed := request(t, h, http.MethodGet, base+"/api/v1/plugin-webhooks?plugin_id="+strconv.FormatInt(p.ID, 10), token, nil, http.StatusOK)
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), "secret") {
		t.Fatal("list leaked secret")
	}
	deliver(http.MethodGet, base+path, body, nil, http.StatusMethodNotAllowed)
	deliver(http.MethodPost, base+path+"/extra", body, nil, http.StatusNotFound)
	deliver(http.MethodPost, base+path+"?token=x", body, nil, http.StatusBadRequest)
	deliver(http.MethodPost, base+path, strings.Repeat("x", plugin.MaxWebhookBodyBytes+1), nil, http.StatusRequestEntityTooLarge)
	deliver(http.MethodPost, base+path, body, func(h http.Header) { h.Del("X-Oboard-Signature") }, http.StatusUnauthorized)
	deliver(http.MethodPost, base+path, body, func(h http.Header) { h.Add("X-Oboard-Nonce", nonce) }, http.StatusUnauthorized)
	deliver(http.MethodPost, base+path, body, func(h http.Header) { h.Set("X-Oboard-Signature", strings.Repeat("0", 64)) }, http.StatusUnauthorized)
	deliver(http.MethodPost, base+path, `{"message":"ok","grant_id":999}`, nil, http.StatusBadRequest)
	rr := deliver(http.MethodPost, base+path, body, nil, http.StatusAccepted)
	if rr.Header().Get("Cache-Control") != "no-store" || strings.Contains(rr.Body.String(), "snapshot") || strings.Contains(rr.Body.String(), "grant_id") {
		t.Fatal("public receipt exposed internal run state")
	}
	deliver(http.MethodPost, base+path, body, nil, http.StatusConflict)
	if count, err := db.CountActivePluginRuns(ctx, p.ID); err != nil || count != 1 {
		t.Fatalf("delivery queued %d runs: %v", count, err)
	}
	for i := 0; i < 60; i++ {
		if err := db.AllowPluginWebhookAttempt(ctx, id, time.Now()); err != nil {
			break
		}
	}
	rr = deliver(http.MethodPost, base+path, body, nil, http.StatusTooManyRequests)
	if rr.Header().Get("Retry-After") != "60" {
		t.Fatal("missing bounded retry hint")
	}
}
