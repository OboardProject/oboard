package controller

import (
	"encoding/json"
	"fmt"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
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

func TestProbeMonitorUsesOnlyNextTraceASN(t *testing.T) {
	trace := model.NetworkTraceResult{Provider: "NextTrace-API", Reached: true, Hops: []model.NetworkTraceHop{{Hop: 1, Addresses: []string{"1.1.1.1"}, Geography: []model.NetworkTraceGeography{{Address: "1.1.1.1", ASN: "AS13335"}}}}}
	path, err := monitorASNPath(trace)
	if err != nil || len(path) != 1 || path[0] != "AS13335" {
		t.Fatalf("path=%v err=%v", path, err)
	}
	trace.Provider = "ipinfo"
	if _, err := monitorASNPath(trace); err == nil {
		t.Fatal("wrong provider accepted")
	}
	trace.Provider = "NextTrace-API"
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
