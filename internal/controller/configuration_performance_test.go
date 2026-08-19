package controller

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const configurationPerformanceSamples = 32

func durationP95(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := (len(ordered)*95 + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(ordered) {
		index = len(ordered)
	}
	return ordered[index-1]
}

// TestConfigurationPerformanceSLO is the reproducible local SLO check for the
// desired-state workflow. It uses a warmed SQLite database, fixed samples,
// no external network, and excludes Agent task execution time.
func TestConfigurationPerformanceSLO(t *testing.T) {
	t.Run("management_write_p95", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		srv := newTestServer(db, "test-secret", "")
		handler := srv.Handler()
		request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
		login := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
		token := login["token"].(string)
		created := request(t, handler, http.MethodPost, "/api/v2/ui/servers", token, map[string]any{"name": "perf-node", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 11000}, http.StatusCreated)
		serverID := int64(created["server"].(map[string]any)["id"].(float64))

		// Warm the same route and SQLite statement/cache path before sampling.
		request(t, handler, http.MethodPatch, fmt.Sprintf("/api/v2/ui/servers/%d", serverID), token, map[string]any{"name": "perf-warm"}, http.StatusOK)
		samples := make([]time.Duration, 0, configurationPerformanceSamples)
		for index := 0; index < configurationPerformanceSamples; index++ {
			started := time.Now()
			request(t, handler, http.MethodPatch, fmt.Sprintf("/api/v2/ui/servers/%d", serverID), token, map[string]any{"name": fmt.Sprintf("perf-%02d", index)}, http.StatusOK)
			samples = append(samples, time.Since(started))
		}
		p95 := durationP95(samples)
		t.Logf("configuration_write samples=%d p95=%s max=%s", len(samples), p95, maxDuration(samples))
		if p95 > 500*time.Millisecond {
			t.Fatalf("ordinary management write p95=%s, want <=500ms", p95)
		}
	})

	t.Run("online_agent_dispatch_p95", func(t *testing.T) {
		db, srv, server, httpServer := newTaskDispatchServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go srv.StartConfigurationReconciler(ctx)
		socket := connectTestAgent(t, srv, httpServer.URL, server)
		defer socket.close()
		samples := make([]time.Duration, 0, configurationPerformanceSamples)
		for index := 0; index < configurationPerformanceSamples; index++ {
			inbound := &model.Inbound{ServerID: server.ID, Name: fmt.Sprintf("perf-entry-%02d", index), Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443 + index, ConfigJSON: "{}", Enabled: true}
			if err := db.CreateInbound(ctx, inbound); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			srv.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
			task := socket.expectTaskRequest(2 * time.Second)
			samples = append(samples, time.Since(started))
			if task["type"] != model.AgentTaskTypeApplyDeployment {
				t.Fatalf("task type=%v, want apply_deployment", task["type"])
			}
			socket.sendTaskAck(float64(taskID(task)))
		}
		p95 := durationP95(samples)
		t.Logf("online_agent_dispatch samples=%d p95=%s max=%s", len(samples), p95, maxDuration(samples))
		if p95 > 2*time.Second {
			t.Fatalf("online Agent dispatch p95=%s, want <=2s", p95)
		}
	})
}

func maxDuration(values []time.Duration) time.Duration {
	var max time.Duration
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
