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
// handler for 100- and 500-item batches with a warm routing snapshot cache.
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
	post := func(count int) {
		items := make([]map[string]any, 0, count)
		for index := 0; index < count; index++ {
			items = append(items, item(fmt.Sprintf("bench-report-%d-%d", time.Now().UnixNano(), index), index))
		}
		body, err := json.Marshal(map[string]any{"items": items})
		if err != nil {
			b.Fatal(err)
		}
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
	// Warm the per-user overview cache path.
	post(10)
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprintf("restrict_%d", count), func(b *testing.B) {
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				post(count)
			}
		})
	}
	// With action=warn the device-action evaluation is skipped entirely; this
	// isolates the batch ingest cost.
	if err := db.SetSetting(ctx, settingAuditAction, string(model.AuditActionWarn)); err != nil {
		b.Fatal(err)
	}
	srv.invalidateSettingsSnapshot()
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprintf("warn_%d", count), func(b *testing.B) {
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
