package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryTimeoutIsNotReportedAsUserCancellation(t *testing.T) {
	for _, machine := range []bool{false, true} {
		for _, item := range []struct {
			err  error
			code string
		}{{context.DeadlineExceeded, "history_timeout"}, {context.Canceled, "history_canceled"}, {errHistoryBusy, "history_busy"}} {
			recorder := httptest.NewRecorder()
			writeHistoryReadError(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ui/servers/1/connectivity?view=chart", nil), item.err, machine)
			if recorder.Code != 503 || recorder.Header().Get("Retry-After") != "5" || strings.Contains(recorder.Body.String(), "request canceled") || !strings.Contains(recorder.Body.String(), item.code) {
				t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestHistoryBudgetFailureCanRecoverWithoutChangingLimits(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	node := &model.Server{Name: "retry"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(30 * time.Second)
	report := model.LatencyProbeResultReport{ReportID: "sample", ResourceVersion: "test", CheckedAt: at.Add(-time.Minute), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Available: true, LatencyMS: 23, SampleCount: 3, SuccessCount: 3}}}
	if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	app.latencyHistoryNow = func() time.Time { return at }
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)["token"].(string)
	cache := app.historyReads()
	cache.budget.timeout = 20 * time.Millisecond
	cache.budget.retryDelay = time.Millisecond
	for _, view := range []string{"", "&view=chart"} {
		path := fmt.Sprintf("/api/v1/ui/servers/%d/connectivity?window=24h%s", node.ID, view)
		cache.buildSem <- struct{}{}
		failed := request(t, handler, http.MethodGet, path, token, nil, 503)
		<-cache.buildSem
		if failed["code"] != "history_timeout" {
			t.Fatalf("not a service deadline: %v", failed)
		}
		time.Sleep(3 * time.Millisecond)
		// Restore the production per-query budget, not a larger allowance.
		cache.budget.timeout = 5 * time.Second
		recovered := request(t, handler, http.MethodGet, path, token, nil, 200)
		points, ok := recovered["latency_points"].([]any)
		if !ok || len(points) == 0 {
			t.Fatalf("data lost after transient failure: %v", recovered)
		}
		cache.budget.timeout = 20 * time.Millisecond
	}
	if cap(cache.buildSem) != 1 || cache.budget.maxInflight != 9 {
		t.Fatal("recovery relaxed query concurrency")
	}
}
