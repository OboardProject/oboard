package controller

import (
	"context"
	"net/http"
	"net/netip"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestServerResourceMetricsReturnsLiveStateWhenHistoryDisabled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "resource-node", "resource_history_enabled": false}, http.StatusCreated)
	serverID := int64(created["server"].(map[string]any)["id"].(float64))
	server, err := db.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	reportAt := time.Now().UTC().Add(-time.Minute)
	report := model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, CPUUsagePercent: 64, MemoryUsedBytes: 640, MemoryTotalBytes: 1000, DiskBytes: 800, DiskTotalBytes: 2000, TCPConnectionCount: 20, UDPConnectionCount: 4, ProcessCount: 70, NetworkUploadBPS: 50, NetworkDownloadBPS: 75, Timestamp: reportAt}
	window := model.ServerTrafficWindow{Key: reportAt.Format("2006-01-02"), Start: reportAt.Truncate(24 * time.Hour), End: reportAt.Truncate(24 * time.Hour).Add(24 * time.Hour)}
	server.Status = report.Status
	server.CPUUsagePercent = report.CPUUsagePercent
	server.MemoryUsedBytes = report.MemoryUsedBytes
	server.MemoryTotalBytes = report.MemoryTotalBytes
	server.DiskBytes = report.DiskBytes
	server.DiskTotalBytes = report.DiskTotalBytes
	server.TCPConnectionCount = report.TCPConnectionCount
	server.UDPConnectionCount = report.UDPConnectionCount
	server.ProcessCount = report.ProcessCount
	server.LastSeenAt = &reportAt
	if err := db.UpdateServerRuntimeState(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateServerTelemetryReport(context.Background(), server.ID, report, window); err != nil {
		t.Fatal(err)
	}

	response := request(t, h, http.MethodGet, "/api/v2/ui/servers/"+itoa(serverID)+"/resource-metrics?hours=24", token, nil, http.StatusOK)
	if response["history_enabled"] != false {
		t.Fatalf("history_enabled = %#v", response["history_enabled"])
	}
	if points, ok := response["points"].([]any); !ok || len(points) != 0 {
		t.Fatalf("disabled history points = %#v", response["points"])
	}
	current := response["current"].(map[string]any)
	if current["cpu_usage_percent"] != float64(64) || current["memory_used_bytes"] != float64(640) || current["memory_total_bytes"] != float64(1000) {
		t.Fatalf("current resource snapshot = %#v", current)
	}
	if current["disk_used_bytes"] != float64(800) || current["disk_total_bytes"] != float64(2000) || current["tcp_connection_count"] != float64(20) || current["udp_connection_count"] != float64(4) || current["process_count"] != float64(70) {
		t.Fatalf("current extended resource snapshot = %#v", current)
	}
}

func TestMCPServerResourceMetricsHonorsAuthorizationAndDisabledHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	allowed := &model.Server{Name: "allowed-resource", Status: model.ServerOnline, ResourceHistoryEnabled: false, ResourceHistoryConfigured: true, CPUUsagePercent: 25, MemoryUsedBytes: 250, MemoryTotalBytes: 1000}
	denied := &model.Server{Name: "denied-resource", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, allowed); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, denied); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")
	boundary := mcpauth.ResourceBoundary{Version: mcpauth.ResourceBoundaryVersion, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionSelected, IDs: []string{strconv.FormatInt(allowed.ID, 10)}}}}
	grant := mcpauth.GrantPolicy{GrantID: "grant-resource", AccessLevel: mcpauth.AccessRead, ResourceBoundary: boundary, IssuedAt: time.Now().UTC()}
	principal := application.Principal{ID: "grant-resource", Role: model.RoleViewer, AccessLevel: mcpauth.AccessRead, GrantPolicy: &grant, ResourceFilter: application.ResourceFilterFromBoundary(boundary), SourceIP: netip.MustParseAddr("127.0.0.1")}
	ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grant, Role: model.RoleViewer})
	def := mcpResourceDef{uri: "oboard://servers/{id}/resource-metrics", capability: "servers.get", template: true, kind: "query_server_resource_metrics"}
	payload, err := app.readMCPResource(ctx, principal, def, "oboard://servers/"+strconv.FormatInt(allowed.ID, 10)+"/resource-metrics")
	if err != nil {
		t.Fatal(err)
	}
	resource := payload.(map[string]any)
	if resource["history_enabled"] != false || len(resource["points"].([]model.ServerResourceMetricPoint)) != 0 {
		t.Fatalf("disabled MCP resource payload = %#v", resource)
	}
	if _, err := app.readMCPResource(ctx, principal, def, "oboard://servers/"+strconv.FormatInt(denied.ID, 10)+"/resource-metrics"); err == nil {
		t.Fatal("MCP resource returned an unauthorized server")
	}
}
