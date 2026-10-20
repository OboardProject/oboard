package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeMonitorHTTPBasePathAndAuthentication(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	app := New(db, "test-secret", "", "/panel", nil)
	admin := &model.User{Username: "http-probe-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "33333333-3333-4333-8333-333333333333", ProxyPassword: "unused"}
	if err := db.CreateUser(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "http-probe-node"}
	if err := db.CreateServer(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: admin.ID, Role: string(admin.Role), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	call := func(method, path, body, auth string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	call(http.MethodGet, "/api/v1/probe-monitors", "", token, http.StatusNotFound)
	call(http.MethodGet, "/panel/api/v1/probe-monitors", "", "", http.StatusUnauthorized)
	body := fmt.Sprintf(`{"action":"save","request_id":"http-probe-create","input":{"server_id":%d,"name":"HTTP route","kind":"route","target":"1.1.1.1","mode":"icmp","port":0,"interval_seconds":900,"enabled":true}}`, server.ID)
	call(http.MethodPost, "/panel/api/v1/probe-monitors", body, token, http.StatusOK)
	if result := call(http.MethodGet, "/panel/api/v1/probe-monitors", "", token, http.StatusOK); !strings.Contains(result, "HTTP route") {
		t.Fatalf("saved monitor missing: %s", result)
	}
}

func TestProbeMonitorAutomationAndBoundary(t *testing.T) {
	ctx := t.Context()
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "test-secret", "")
	admin := &model.User{Username: "probe-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	p := userAutomationPrincipal(t, db, admin.ID)
	server := &model.Server{Name: "probe-node"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"server_id":%d,"name":"route","kind":"route","target":"1.1.1.1","mode":"icmp","port":0,"interval_seconds":900,"enabled":true}`, server.ID))
	applyAutomationChangeset(t, app, p, "probe-create", automation.OperationRequest{Capability: "probe_monitors.save", Input: raw})
	tasks, err := db.ListProbeMonitors(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	task := tasks[0]
	restricted := p
	restricted.ResourceFilter, _ = json.Marshal(application.ResourceFilter{Servers: &application.ResourceSelection{Mode: "selected", IDs: []int64{server.ID + 1}}})
	if _, _, err := app.probeMonitorBoundary(ctx, restricted, probeMonitorInput{ID: task.ID, ServerID: server.ID}); err == nil {
		t.Fatal("boundary bypass")
	}
	value, err := app.queryProbeMonitors(ctx, restricted, "probe_monitors.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(value.(map[string]any)["monitors"].([]model.ProbeMonitor)) != 0 {
		t.Fatal("out of scope list")
	}
	raw = json.RawMessage(fmt.Sprintf(`{"server_id":%d,"id":%d,"revision":%d}`, server.ID, task.ID, task.Revision))
	applyAutomationChangeset(t, app, p, "probe-run", automation.OperationRequest{Capability: "probe_monitors.run", Input: raw})
	applyAutomationChangeset(t, app, p, "probe-delete", automation.OperationRequest{Capability: "probe_monitors.delete", Input: raw})
	tasks, err = db.ListProbeMonitors(ctx)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("deleted tasks=%v err=%v", tasks, err)
	}
}

func TestProbeMonitorASNPathRequiresCompleteBoundGeography(t *testing.T) {
	trace := model.NetworkTraceResult{Provider: "NextTrace-API", Reached: true, Hops: []model.NetworkTraceHop{{Hop: 1, Addresses: []string{"1.1.1.1"}, Geography: []model.NetworkTraceGeography{{Address: "1.1.1.1", ASN: "AS13335"}}}}}
	path, err := monitorASNPath(trace)
	if err != nil || len(path) != 1 || path[0] != "AS13335" {
		t.Fatalf("path=%v err=%v", path, err)
	}
	trace.GeographyError = "rate limited"
	if _, err := monitorASNPath(trace); err == nil {
		t.Fatal("partial lookup accepted")
	}
	trace.GeographyError = ""
	trace.Hops[0].Geography[0].Address = "8.8.8.8"
	if _, err := monitorASNPath(trace); err == nil {
		t.Fatal("unbound geography accepted")
	}
}

type failingRouteFiller struct{ calls int }

func (f *failingRouteFiller) Fill(context.Context, *model.NetworkTraceResult) error {
	f.calls++
	return errors.New("NextTrace API 已限流")
}
func (f *failingRouteFiller) SetToken(string) {}

func TestRouteGeographyKeepsAgentNextTraceAndFallsBackToAttribution(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	app := New(db, "test-secret", "", "", nil)
	state := app.ipDataState()
	filler := &failingRouteFiller{}
	state.nextTrace = filler
	queried := map[string]int{}
	state.service.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		queried[r.URL.Host]++
		body := `{}`
		if r.URL.Host == "ipinfo.io" {
			body = `{"ip":"8.8.8.8","org":"AS15169 Google LLC","country":"US"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	trace := model.NetworkTraceResult{Provider: "NextTrace-API", GeographyError: "NextTrace API 冷却中", Reached: true, Hops: []model.NetworkTraceHop{
		{Hop: 1, Addresses: []string{"1.1.1.1"}, Geography: []model.NetworkTraceGeography{{Address: "1.1.1.1", ASN: "AS13335"}}},
		{Hop: 2, Addresses: []string{"8.8.8.8"}},
	}}
	app.completeTraceGeography(t.Context(), &trace)
	if filler.calls != 1 || trace.GeographyError != "" || len(trace.Hops[1].Geography) != 1 || trace.Hops[1].Geography[0].ASN != "AS15169" || trace.Hops[1].Geography[0].Source != "ipinfo" {
		t.Fatalf("fallback not applied: calls=%d trace=%+v", filler.calls, trace)
	}
	if queried["ipinfo.io"] != 1 || len(trace.Hops[0].Geography) != 1 {
		t.Fatalf("agent NextTrace hop re-queried: %+v", queried)
	}
	path, err := monitorASNPath(trace)
	if err != nil || len(path) != 2 {
		t.Fatalf("path=%v err=%v", path, err)
	}
	unresolved := model.NetworkTraceResult{Reached: true, Hops: []model.NetworkTraceHop{{Hop: 1, Addresses: []string{"9.9.9.9"}}}}
	state.service.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})
	app.completeTraceGeography(t.Context(), &unresolved)
	if unresolved.GeographyError == "" {
		t.Fatal("unresolved hop not reported as incomplete")
	}
}

func TestIPDataKeysAreEncryptedAndNeverReturned(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	app := New(db, "test-secret", "", "", nil)
	admin := &model.User{Username: "ip-data-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "44444444-4444-4444-8444-444444444444", ProxyPassword: "unused"}
	if err := db.CreateUser(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: admin.ID, Role: string(admin.Role), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	call := func(method, path, body string) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	saved := call(http.MethodPost, "/api/v1/ui/ip-data/keys", `{"keys":{"ipinfo":"ipinfo-secret-token","abuseipdb":"abuse-secret"}}`)
	if strings.Contains(saved, "secret") || !strings.Contains(saved, `"configured":true`) {
		t.Fatalf("keys leaked or not configured: %s", saved)
	}
	raw, _ := db.GetSetting(t.Context(), ipDataKeysSetting)
	if raw == "" || strings.Contains(raw, "ipinfo-secret-token") {
		t.Fatal("keys stored in plaintext")
	}
	if settings := call(http.MethodGet, "/api/v1/ui/settings", ""); strings.Contains(settings, ipDataKeysSetting) {
		t.Fatal("encrypted keys exposed through settings")
	}
	if app.ipDataService(t.Context()).Key("ipinfo") != "ipinfo-secret-token" {
		t.Fatal("service did not receive saved key")
	}
	call(http.MethodPost, "/api/v1/ui/ip-data/keys", `{"keys":{"ipinfo":""},"clear":["abuseipdb"]}`)
	keys, err := app.loadIPDataKeys(t.Context())
	if err != nil || keys["ipinfo"] != "ipinfo-secret-token" || keys["abuseipdb"] != "" {
		t.Fatalf("blank must keep, clear must remove: %v %v", keys, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGoogleRegionReportOnlyProbesTheServersOwnOnlineAddress(t *testing.T) {
	s := &Server{}
	server := model.Server{ID: 1, PublicIPv4: "203.0.113.9", Status: model.ServerOnline, AgentID: "agent-1"}
	if got := s.monitorGoogleRegion(context.Background(), server, model.ProbeMonitor{Kind: "ip_quality", Target: "198.51.100.7"}); got.Status != "not_applicable" {
		t.Fatalf("a foreign target must not be probed from the server: %+v", got)
	}
	offline := server
	offline.Status = model.ServerOffline
	if got := s.monitorGoogleRegion(context.Background(), offline, model.ProbeMonitor{Kind: "ip_quality"}); got.Status != "offline" {
		t.Fatalf("offline server: %+v", got)
	}
	if got := s.monitorGoogleRegion(context.Background(), server, model.ProbeMonitor{Kind: "ip_quality", Target: "203.0.113.9"}); got.Status != "unsupported" {
		t.Fatalf("an Agent without the capability must not receive the task: %+v", got)
	}
	for _, tc := range []struct {
		result model.GoogleRegionResult
		want   googleRegionReport
	}{
		{model.GoogleRegionResult{CountryCode: "CN", SentToChina: true}, googleRegionReport{Status: "ok", CountryCode: "CN", SentToChina: true}},
		{model.GoogleRegionResult{CountryCode: "sg"}, googleRegionReport{Status: "ok", CountryCode: "SG"}},
		{model.GoogleRegionResult{ErrorClass: "region_unknown"}, googleRegionReport{Status: "unknown", Error: "Google 未返回地区"}},
		{model.GoogleRegionResult{ErrorClass: "timeout"}, googleRegionReport{Status: "unknown", Error: "无法连接 Google（timeout）"}},
		{model.GoogleRegionResult{CountryCode: "C1"}, googleRegionReport{Status: "unknown", Error: "Google 未返回地区"}},
	} {
		if got := googleRegionFromResult(tc.result); got != tc.want {
			t.Fatalf("%+v: got %+v, want %+v", tc.result, got, tc.want)
		}
	}
}
