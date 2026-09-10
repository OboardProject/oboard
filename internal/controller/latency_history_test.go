package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestLatencyChartCacheAuthorizationAndInvalidation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "chart.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "chart", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	task := &model.LatencyProbeTask{Name: "original", Method: model.LatencyProbeModeTCP, Address: "example.com", Port: 443, Enabled: true, IntervalSeconds: 120, ServerIDs: []int64{node.ID}}
	if err := db.SaveLatencyProbeTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(30 * time.Second)
	report := model.LatencyProbeResultReport{ReportID: "one", ResourceVersion: "test", CheckedAt: at.Add(-time.Minute), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Available: true, LatencyMS: 20, SampleCount: 3, SuccessCount: 3}, {ProbeID: "task", Kind: "custom", TaskID: task.ID, TaskName: task.Name, Available: true, LatencyMS: 40, SampleCount: 3, SuccessCount: 3}}}
	if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "secret", "")
	server.latencyHistoryNow = func() time.Time { return at }
	principal := application.HumanPrincipal(model.User{ID: 1}, model.RoleAdmin, netip.Addr{})
	input := latencyChartInput{ServerID: node.ID, Window: "24h", MaxPoints: 120}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := server.readLatencyChart(ctx, principal, input)
			if err != nil || len(result.LatencyPoints) != 1 || result.LatencyPoints[0].Count != 1 {
				t.Errorf("result=%+v err=%v", result, err)
			}
		}()
	}
	wg.Wait()
	if count := server.historyReads().builds.Load(); count != 1 {
		t.Fatalf("history builds=%d", count)
	}
	result, err := server.readLatencyChart(ctx, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.ObservedThrough == nil || !result.Metadata.ObservedThrough.Equal(report.CheckedAt) || result.Metadata.Stale {
		t.Fatalf("metadata=%+v", result.Metadata)
	}
	result.RegionalLatencyPoints[0].TaskName = "mutated"
	denied := principal
	denied.ResourceFilter = json.RawMessage(`{"servers":{"mode":"selected","ids":[]}}`)
	if _, err := server.readLatencyChart(ctx, denied, input); err == nil {
		t.Fatal("cached data leaked after resource revocation")
	}
	task.Name = "renamed"
	if err := db.SaveLatencyProbeTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	result, err = server.readLatencyChart(ctx, principal, input)
	if err != nil || result.RegionalLatencyPoints[0].TaskName != "renamed" {
		t.Fatalf("renamed=%+v %v", result, err)
	}
	if count := server.historyReads().builds.Load(); count != 1 {
		t.Fatalf("rename rebuilt history: %d", count)
	}
	task.Address = "other.example"
	if err := db.SaveLatencyProbeTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := server.readLatencyChart(ctx, principal, input); err != nil {
		t.Fatal(err)
	}
	if count := server.historyReads().builds.Load(); count != 2 {
		t.Fatalf("semantics not invalidated: %d", count)
	}
	if err := db.SetSetting(ctx, "unrelated-setting", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.readLatencyChart(ctx, principal, input); err != nil {
		t.Fatal(err)
	}
	if server.historyReads().builds.Load() != 2 {
		t.Fatal("unrelated setting invalidated charts")
	}
	if err := db.SetSetting(ctx, "server_monitoring_retention_days", "30"); err != nil {
		t.Fatal(err)
	}
	if result, err := server.readLatencyChart(ctx, principal, input); err != nil || result.RetentionDays != 30 {
		t.Fatalf("retention=%d err=%v", result.RetentionDays, err)
	}
	if err := db.DeleteLatencyProbeTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	result, err = server.readLatencyChart(ctx, principal, input)
	if err != nil || len(result.RegionalLatencyPoints) != 0 {
		t.Fatalf("deleted target=%+v %v", result, err)
	}
	if err := db.DeleteServer(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.readLatencyChart(ctx, principal, input); err == nil {
		t.Fatal("deleted server served from cache")
	}
}

func TestLatencyChartHTTPContract(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "chart.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "chart"}
	if err := db.CreateServer(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)["token"].(string)
	path := fmt.Sprintf("/api/v1/ui/servers/%d/connectivity?view=chart&window=24h&max_points=60", node.ID)
	request(t, handler, http.MethodGet, path, "", nil, 401)
	result := request(t, handler, http.MethodGet, path, token, nil, 200)
	if _, ok := result["summary"]; ok {
		t.Fatal("chart built full SLA")
	}
	if _, ok := result["current"]; ok {
		t.Fatal("chart cached live state")
	}
	metadata := result["metadata"].(map[string]any)
	if metadata["aggregation_state"] != "ready" || metadata["observed_through"] != nil {
		t.Fatalf("metadata=%v", metadata)
	}
	request(t, handler, http.MethodGet, path+"0", token, nil, 400)
	request(t, handler, http.MethodGet, fmt.Sprintf("/api/v1/ui/servers/%d/connectivity?view=events", node.ID), token, nil, 400)
	request(t, handler, http.MethodGet, "/api/v1/ui/servers/999999/connectivity?view=chart", token, nil, 404)
	machine := fmt.Sprintf("/api/v1/servers/%d/connectivity?view=chart&window=1h", node.ID)
	result = request(t, handler, http.MethodGet, machine, token, nil, 200)
	if _, ok := result["data"]; !ok {
		t.Fatalf("machine contract=%v", result)
	}

	oldPath := fmt.Sprintf("/api/v1/ui/servers/%d/connectivity?window=1h", node.ID)
	old := request(t, handler, http.MethodGet, oldPath, token, nil, 200)
	if _, ok := old["summary"]; !ok {
		t.Fatal("legacy full response changed")
	}
	server.historyReads().mu.Lock()
	for key := range server.historyReads().entries {
		if strings.HasPrefix(key, "full:") {
			t.Error("full response was cached")
		}
	}
	server.historyReads().mu.Unlock()
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BumpSessionVersion(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	builds := server.historyReads().builds.Load()
	request(t, handler, http.MethodGet, path, token, nil, 401)
	if server.historyReads().builds.Load() != builds {
		t.Fatal("revoked session reached history computation")
	}
}
func TestLatencyHistoryCacheRollbackSwitch(t *testing.T) {
	t.Setenv("OBOARD_LATENCY_HISTORY_CACHE", "0")
	s := &Server{}
	c := s.historyReads()
	if c.ttl != 0 || c.maxStale != 0 || cap(c.buildSem) != 1 || c.budget.maxInflight != 9 {
		t.Fatal("rollback disabled budgets or retained TTL")
	}
}
