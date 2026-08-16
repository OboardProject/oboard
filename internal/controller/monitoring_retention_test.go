package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestServerMonitoringRetentionSettingsAndAggregatedHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "monitoring-retention.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	settingsResponse := request(t, handler, http.MethodGet, "/api/v2/ui/settings", token, nil, http.StatusOK)
	settings := settingsResponse["settings"].(map[string]any)
	if settings[store.ServerMonitoringRetentionDaysSetting] != float64(store.DefaultServerMonitoringRetentionDays) {
		t.Fatalf("default monitoring retention = %#v", settings[store.ServerMonitoringRetentionDaysSetting])
	}
	for _, days := range []int{0, store.MaxServerMonitoringRetentionDays + 1} {
		request(t, handler, http.MethodPost, "/api/v2/ui/settings", token, map[string]any{store.ServerMonitoringRetentionDaysSetting: days}, http.StatusBadRequest)
	}
	request(t, handler, http.MethodPost, "/api/v2/ui/settings", token, map[string]any{store.ServerMonitoringRetentionDaysSetting: 7}, http.StatusOK)

	mcpInput := json.RawMessage(`{"changes":{"server_monitoring_retention_days":15}}`)
	changed, err := app.settingsUpdateCandidate(ctx, mcpInput, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != store.ServerMonitoringRetentionDaysSetting {
		t.Fatalf("MCP settings change = %#v", changed)
	}
	if _, err := app.settingsUpdateCandidate(ctx, json.RawMessage(`{"changes":{"server_monitoring_retention_days":31}}`), false); err == nil {
		t.Fatal("MCP settings validation accepted retention above maximum")
	}

	created := request(t, handler, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "retention-node"}, http.StatusCreated)
	serverID := int64(created["server"].(map[string]any)["id"].(float64))
	resourceResponse := request(t, handler, http.MethodGet, fmt.Sprintf("/api/v2/ui/servers/%d/resource-metrics?hours=24", serverID), token, nil, http.StatusOK)
	if resourceResponse["retention_days"] != float64(15) {
		t.Fatalf("resource retention days = %#v", resourceResponse["retention_days"])
	}

	now := time.Now().UTC().Truncate(time.Hour)
	for batch := 0; batch < 10; batch++ {
		items := make([]model.LatencyProbeResult, 0, 60)
		for index := 0; index < 60; index++ {
			items = append(items, model.LatencyProbeResult{
				ProbeID:      fmt.Sprintf("regional-%d", index),
				Kind:         "regional",
				Mode:         "tcp",
				Province:     "广东",
				Carrier:      "中国电信",
				Host:         "192.0.2.1",
				IP:           "192.0.2.1",
				Port:         443,
				Available:    true,
				LatencyMS:    int64(20 + batch),
				SampleCount:  3,
				SuccessCount: 3,
			})
		}
		if err := db.SaveLatencyProbeResults(ctx, serverID, model.LatencyProbeResultReport{
			ReportID:        fmt.Sprintf("regional-report-%d", batch),
			ResourceVersion: "retention-test-v1",
			CheckedAt:       now.Add(time.Duration(batch-10) * time.Hour),
			Items:           items,
		}); err != nil {
			t.Fatal(err)
		}
	}

	connectivityResponse := request(t, handler, http.MethodGet, fmt.Sprintf("/api/v2/ui/servers/%d/connectivity?window=24h", serverID), token, nil, http.StatusOK)
	if connectivityResponse["retention_days"] != float64(15) {
		t.Fatalf("connectivity retention days = %#v", connectivityResponse["retention_days"])
	}
	points, ok := connectivityResponse["regional_latency_points"].([]any)
	if !ok || len(points) != 10 {
		t.Fatalf("regional aggregate points = %#v, want 10", connectivityResponse["regional_latency_points"])
	}
	var aggregatedCount int
	for _, value := range points {
		point := value.(map[string]any)
		aggregatedCount += int(point["count"].(float64))
		if _, exposed := point["probe_id"]; exposed {
			t.Fatalf("aggregated history exposed a raw probe: %#v", point)
		}
	}
	if aggregatedCount != 600 {
		t.Fatalf("aggregated regional sample count = %d, want 600", aggregatedCount)
	}
	if connectivityResponse["regional_data_start_at"] == nil {
		t.Fatal("regional history did not return its actual data start")
	}
}
