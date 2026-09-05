package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// BenchmarkTaskDispatchThroughput measures one agent's end-to-end dispatch
// latency: task creation (with wake), claim, signature, write, ack. The
// previous design could add up to a full second of polling latency per task.
func BenchmarkTaskDispatchThroughput(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "bench-task", AgentID: "bench-task-agent", AgentTokenHash: security.HashSecret(agentTestToken), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		b.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	header := http.Header{}
	header.Set("X-Agent-ID", server.AgentID)
	header.Set("Authorization", "Bearer "+agentTestToken)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/v1/agent/connect", header)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hello map[string]any
	if err := conn.ReadJSON(&hello); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeCollectLogs, PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: fmt.Sprintf("bench-dispatch-%d", index)}
		if err := srv.createTaskAndWake(ctx, task); err != nil {
			b.Fatal(err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			b.Fatal(err)
		}
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			b.Fatal(err)
		}
		for message["type"] != "task_request" {
			if err := conn.ReadJSON(&message); err != nil {
				b.Fatal(err)
			}
		}
		taskJSON, _ := json.Marshal(message["task"])
		var dispatched struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(taskJSON, &dispatched)
		if err := conn.WriteJSON(map[string]any{"type": "task_ack", "task_id": dispatched.ID}); err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark100AgentIdleDispatch measures wake-to-dispatch latency with 100
// connected agents while one task is delivered to a rotating server.
func Benchmark100AgentIdleDispatch(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	const agents = 100
	servers := make([]*model.Server, agents)
	conns := make([]*websocket.Conn, agents)
	for index := 0; index < agents; index++ {
		servers[index] = &model.Server{Name: fmt.Sprintf("bench-node-%d", index), AgentID: fmt.Sprintf("bench-node-agent-%d", index), AgentTokenHash: security.HashSecret(agentTestToken), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
		if err := db.CreateServer(ctx, servers[index]); err != nil {
			b.Fatal(err)
		}
	}
	srv := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	for index := 0; index < agents; index++ {
		header := http.Header{}
		header.Set("X-Agent-ID", servers[index].AgentID)
		header.Set("Authorization", "Bearer "+agentTestToken)
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/v1/agent/connect", header)
		if err != nil {
			b.Fatal(err)
		}
		conns[index] = conn
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		var hello map[string]any
		if err := conn.ReadJSON(&hello); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		server := servers[index%agents]
		conn := conns[index%agents]
		task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeCollectLogs, PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: fmt.Sprintf("bench-100-%d", index)}
		if err := srv.createTaskAndWake(ctx, task); err != nil {
			b.Fatal(err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			b.Fatal(err)
		}
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			b.Fatal(err)
		}
		for message["type"] != "task_request" {
			if err := conn.ReadJSON(&message); err != nil {
				b.Fatal(err)
			}
		}
		taskJSON, _ := json.Marshal(message["task"])
		var dispatched struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(taskJSON, &dispatched)
		if err := conn.WriteJSON(map[string]any{"type": "task_ack", "task_id": dispatched.ID}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAgentConnectionReports measures the full /agent/connection-reports
// handler for new and retried 100- and 500-item batches with a warm routing
// snapshot cache. Derived risk and probe scans run on the coalescing worker.
func BenchmarkAgentConnectionReports(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "bench-audit", AgentID: "bench-audit-agent", AgentTokenHash: security.HashSecret(agentTestToken), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		b.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		b.Fatal(err)
	}
	user := &model.User{Username: "bench-audit-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		b.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "bench-audit-plan", Enabled: true}
	if err := db.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: inbound.ID}}); err != nil {
		b.Fatal(err)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: plan.ID}}); err != nil {
		b.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	// Warm the routing snapshot cache.
	if _, err := srv.routingSnapshot(ctx); err != nil {
		b.Fatal(err)
	}
	nowTime := time.Now().UTC()
	item := func(reportID string, index int) map[string]any {
		return map[string]any{
			"report_id": reportID, "user_id": user.ID, "inbound_id": inbound.ID,
			"device_id_hash": fmt.Sprintf("bench-device-hash-%d", index%64), "credential_epoch": 1,
			"source_ip": fmt.Sprintf("198.51.%d.%d", 100+index%50, 1+index%200), "network": "tcp", "destination": "example.com", "destination_port": 443, "outbound_tag": "direct", "outbound_type": "direct",
			"connection_count": 2, "closed_count": 2, "duration_total_ms": 1000, "duration_max_ms": 500, "duration_le_1s_count": 2, "active_peak": 2, "presence_sequence": 1,
			"upload_bytes": 4096, "download_bytes": 8192,
			"payload_first_at": nowTime.Add(-time.Second).Format(time.RFC3339Nano), "payload_last_at": nowTime.Format(time.RFC3339Nano),
			"bucket_capacity": 4096, "collection_started_at": nowTime.Add(-time.Minute).Format(time.RFC3339Nano), "collection_ended_at": nowTime.Format(time.RFC3339Nano),
			"started_at": nowTime.Add(-time.Second).Format(time.RFC3339Nano), "ended_at": nowTime.Format(time.RFC3339Nano),
		}
	}
	postPayload := func(body []byte) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/connection-reports", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agent-ID", server.AgentID)
		req.Header.Set("Authorization", "Bearer "+agentTestToken)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("connection report status = %d: %s", rr.Code, rr.Body.String())
		}
	}
	post := func(count int) {
		items := make([]map[string]any, 0, count)
		for index := 0; index < count; index++ {
			items = append(items, item(fmt.Sprintf("bench-report-%d-%d", time.Now().UnixNano(), index), index))
		}
		body, err := json.Marshal(map[string]any{"items": items})
		if err != nil {
			b.Fatal(err)
		}
		postPayload(body)
	}
	// Warm the per-user overview cache path.
	post(10)
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprintf("restrict_%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				post(count)
			}
		})
	}
	retryItems := make([]map[string]any, 0, 500)
	for index := 0; index < 500; index++ {
		retryItems = append(retryItems, item(fmt.Sprintf("bench-retry-%d", index), index))
	}
	retryBody, err := json.Marshal(map[string]any{"items": retryItems})
	if err != nil {
		b.Fatal(err)
	}
	postPayload(retryBody)
	b.Run("retry_500", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			postPayload(retryBody)
		}
	})
	// With action=warn the device-action evaluation is skipped entirely; this
	// isolates the batch ingest cost.
	if err := db.SetSetting(ctx, settingAuditAction, string(model.AuditActionWarn)); err != nil {
		b.Fatal(err)
	}
	srv.invalidateSettingsSnapshot()
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprintf("warn_%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				post(count)
			}
		})
	}
}

// BenchmarkHealthReportSocket measures the WebSocket health report handling
// path (sanitize, settings snapshot, telemetry, rate-limited metric sample).
func BenchmarkHealthReportSocket(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "bench-health", AgentID: "bench-health-agent", AgentTokenHash: security.HashSecret(agentTestToken), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		b.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	raw, _ := json.Marshal(model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, Timestamp: time.Now().UTC(), CPUUsagePercent: 11, MemoryUsedBytes: 1 << 30, MemoryTotalBytes: 4 << 30, TCPConnectionCount: 50, NetworkUploadBPS: 500, NetworkDownloadBPS: 900})
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		srv.processAgentSocketMessage(ctx, server, map[string]json.RawMessage{"health_report": raw}, "198.51.100.9")
	}
}

// BenchmarkHeartbeatScaling measures the per-heartbeat work an agent
// connection performs (in-memory server refresh, monitoring policy, audit
// gate, latency probe plan, heartbeat JSON) as the managed fleet grows.
// Heartbeat cost must not depend on total server count.
func BenchmarkHeartbeatScaling(b *testing.B) {
	ctx := context.Background()
	for _, count := range []int{1, 10, 100, 500} {
		b.Run(fmt.Sprintf("servers_%d", count), func(b *testing.B) {
			b.ReportAllocs()
			db, err := store.Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			servers := make([]*model.Server, count)
			for index := range servers {
				server := &model.Server{Name: fmt.Sprintf("bench-hb-%d", index), AgentID: fmt.Sprintf("bench-hb-agent-%d", index), AgentTokenHash: security.HashSecret(agentTestToken), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
				if err := db.CreateServer(ctx, server); err != nil {
					b.Fatal(err)
				}
				servers[index] = server
			}
			srv := newTestServer(db, "test-secret", "")
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				server := servers[index%count]
				latest, err := srv.store.GetServer(ctx, server.ID)
				if err != nil {
					b.Fatal(err)
				}
				_, _ = serverMonitoringPolicy(latest)
				_ = srv.effectiveConnectionAuditEnabled(ctx, latest)
				if _, err := srv.latencyProbePlanForServer(ctx, *latest); err != nil {
					b.Fatal(err)
				}
				heartbeat := map[string]any{"type": "heartbeat", "ts": time.Now().UTC()}
				if _, err := json.Marshal(heartbeat); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRealtimeSnapshotScaling measures full realtime snapshot broadcast
// for server counts of 10/100/500 and client counts of 1/3/10. The snapshot
// query and its serialization must be shared across clients: per-iteration
// cost must grow with server count but not with client count.
func BenchmarkRealtimeSnapshotScaling(b *testing.B) {
	ctx := context.Background()
	for _, serverCount := range []int{10, 100, 500} {
		for _, clientCount := range []int{1, 3, 10} {
			b.Run(fmt.Sprintf("servers_%d_clients_%d", serverCount, clientCount), func(b *testing.B) {
				b.ReportAllocs()
				db, err := store.Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close()
				for index := 0; index < serverCount; index++ {
					server := &model.Server{Name: fmt.Sprintf("bench-rt-%d", index), AgentID: fmt.Sprintf("bench-rt-agent-%d", index), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
					if err := db.CreateServer(ctx, server); err != nil {
						b.Fatal(err)
					}
				}
				srv := newTestServer(db, "test-secret", "")
				defer srv.Close()
				var deliveredBytes uint64
				b.ResetTimer()
				for index := 0; index < b.N; index++ {
					snapshots, err := srv.realtimeServerSnapshots(ctx)
					if err != nil {
						b.Fatal(err)
					}
					payload, err := json.Marshal(realtimeMessage{Type: "server_snapshot", Sequence: uint64(index), ServerSnapshots: snapshots})
					if err != nil {
						b.Fatal(err)
					}
					srv.realtime.counters.snapshotBytes.Add(uint64(len(payload)))
					for client := 0; client < clientCount; client++ {
						deliveredBytes += uint64(len(payload))
					}
				}
				builds, rows, encodedBytes, _ := srv.realtime.counters.snapshot()
				b.ReportMetric(float64(builds)/float64(b.N), "builds/op")
				b.ReportMetric(float64(rows)/float64(b.N), "rows/op")
				b.ReportMetric(float64(encodedBytes)/float64(b.N), "encoded-bytes/op")
				b.ReportMetric(float64(deliveredBytes)/float64(b.N), "wire-bytes/op")
				b.ReportMetric(float64(clientCount), "clients")
			})
		}
	}
}
