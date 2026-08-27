package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestServerResetTrafficRESTEndpoint(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "server-traffic-reset.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	ctx := context.Background()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{
		"name": "traffic-reset", "traffic_used_bytes": 123456,
	}, http.StatusCreated)["server"].(map[string]any)
	id := int64(created["id"].(float64))
	if used := created["traffic_upload_bytes"].(float64) + created["traffic_download_bytes"].(float64); used != 123456 {
		t.Fatalf("created used traffic = %v, want 123456", used)
	}

	stored, err := db.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	beforeUpdatedAt := stored.UpdatedAt

	reset := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+fmt.Sprint(id)+"/reset-traffic", token, map[string]any{}, http.StatusOK)
	server := reset["server"].(map[string]any)
	if server["traffic_upload_bytes"] != float64(0) || server["traffic_download_bytes"] != float64(0) || reset["traffic_used_bytes"] != float64(0) {
		t.Fatalf("reset response = %#v", reset)
	}

	after, err := db.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.TrafficUploadBytes != 0 || after.TrafficDownloadBytes != 0 {
		t.Fatalf("stored used traffic = %d/%d", after.TrafficUploadBytes, after.TrafficDownloadBytes)
	}
	if !after.UpdatedAt.Equal(beforeUpdatedAt) {
		t.Fatalf("reset churned servers.updated_at: before=%v after=%v", beforeUpdatedAt, after.UpdatedAt)
	}

	again := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+fmt.Sprint(id)+"/reset-traffic", token, map[string]any{}, http.StatusOK)
	if again["traffic_used_bytes"] != float64(0) {
		t.Fatalf("idempotent reset used = %#v", again["traffic_used_bytes"])
	}
}

func TestServerResetTrafficMCPOperation(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111115", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBootstrapAdmin(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	server := &model.Server{Name: "mcp-traffic-reset", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	window := model.ServerTrafficWindow{Key: "2026-08", Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := db.SetServerTrafficUsed(ctx, server.ID, 888888, window); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(map[string]any{"server_id": server.ID})
	applyAutomationChangeset(t, srv, principal, "reset-traffic", automation.OperationRequest{Capability: "servers.reset_traffic", Input: input})
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUploadBytes != 0 || stored.TrafficDownloadBytes != 0 {
		t.Fatalf("MCP reset used = %d/%d", stored.TrafficUploadBytes, stored.TrafficDownloadBytes)
	}
}

func TestServerResetTrafficKeepsLaterHealthDelta(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "server-traffic-reset-delta.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "delta", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, loc)
	key, start, end := trafficWindow(now, server.TrafficResetMode, server.TrafficResetDay, time.Time{}, loc)
	window := model.ServerTrafficWindow{Key: key, Start: start, End: end}

	first := model.HealthReport{NetworkTotalUploadBytes: 1000, NetworkTotalDownloadBytes: 2000, Timestamp: now}
	if err := db.UpdateServerTelemetryReport(ctx, server.ID, first, window); err != nil {
		t.Fatal(err)
	}
	second := model.HealthReport{NetworkTotalUploadBytes: 1100, NetworkTotalDownloadBytes: 2100, Timestamp: now.Add(30 * time.Second)}
	if err := db.UpdateServerTelemetryReport(ctx, server.ID, second, window); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUploadBytes != 100 || stored.TrafficDownloadBytes != 100 {
		t.Fatalf("pre-reset period = %d/%d, want 100/100", stored.TrafficUploadBytes, stored.TrafficDownloadBytes)
	}

	if err := db.SetServerTrafficUsed(ctx, server.ID, 0, window); err != nil {
		t.Fatal(err)
	}
	third := model.HealthReport{NetworkTotalUploadBytes: 1150, NetworkTotalDownloadBytes: 2150, Timestamp: now.Add(60 * time.Second)}
	if err := db.UpdateServerTelemetryReport(ctx, server.ID, third, window); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TrafficUploadBytes != 50 || after.TrafficDownloadBytes != 50 {
		t.Fatalf("post-reset period = %d/%d, want 50/50", after.TrafficUploadBytes, after.TrafficDownloadBytes)
	}
}

func TestServerResetTrafficRecipe(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111116", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	server := &model.Server{Name: "tokyo", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	prepared, err := srv.prepareServerManageRecipe(ctx, principal, mcpTaskInput{
		Goal:       "清零已用流量",
		TargetRefs: []string{fmt.Sprintf("server:%d", server.ID)},
	})
	if err != nil || prepared.Status != "ready" || len(prepared.Operations) != 1 {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	if prepared.Operations[0].Capability != "servers.reset_traffic" {
		t.Fatalf("capability = %s", prepared.Operations[0].Capability)
	}
	if got := int64(taskIntParam(prepared.Operations[0].Input, "server_id")); got != server.ID {
		t.Fatalf("server_id = %d, want %d", got, server.ID)
	}

	blocked, err := srv.prepareServerManageRecipe(ctx, principal, mcpTaskInput{
		Goal:       "清零用户流量账本",
		TargetRefs: []string{fmt.Sprintf("server:%d", server.ID)},
		Params:     map[string]any{"reset_traffic": true},
	})
	if err != nil || blocked.Status != "needs_input" {
		t.Fatalf("user ledger rewrite=%#v err=%v", blocked, err)
	}
}
