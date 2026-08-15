package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type testAgentSocket struct {
	t    *testing.T
	conn *websocket.Conn
}

func (a *testAgentSocket) close() {
	_ = a.conn.Close()
}

// readMessage waits up to timeout for the next WebSocket message and returns
// its decoded object.
func (a *testAgentSocket) readMessage(timeout time.Duration) map[string]any {
	a.t.Helper()
	if err := a.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		a.t.Fatalf("set read deadline: %v", err)
	}
	var raw map[string]any
	if err := a.conn.ReadJSON(&raw); err != nil {
		a.t.Fatalf("read agent message: %v", err)
	}
	return raw
}

// readMessageMaybe returns the next message or nil after timeout, for
// assertions that no task_request arrives.
func (a *testAgentSocket) readMessageMaybe(timeout time.Duration) map[string]any {
	a.t.Helper()
	if err := a.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		a.t.Fatalf("set read deadline: %v", err)
	}
	var raw map[string]any
	if err := a.conn.ReadJSON(&raw); err != nil {
		return nil
	}
	return raw
}

func (a *testAgentSocket) sendTaskAck(taskID float64) {
	a.t.Helper()
	if err := a.conn.WriteJSON(map[string]any{"type": "task_ack", "task_id": int64(taskID)}); err != nil {
		a.t.Fatalf("send task_ack: %v", err)
	}
}

// expectTaskRequest reads until a task_request arrives (skipping hello and
// heartbeat) and returns the task object.
func (a *testAgentSocket) expectTaskRequest(timeout time.Duration) map[string]any {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			a.t.Fatal("timed out waiting for task_request")
		}
		msg := a.readMessage(remaining)
		if msg["type"] == "task_request" {
			return msg["task"].(map[string]any)
		}
		if msg["type"] == "task_ack" {
			a.t.Fatalf("unexpected task_ack: %#v", msg)
		}
	}
}

func connectTestAgent(t *testing.T, srv *Server, baseURL string, server *model.Server) *testAgentSocket {
	t.Helper()
	header := http.Header{}
	header.Set("X-Agent-ID", server.AgentID)
	header.Set("Authorization", "Bearer "+agentTestToken)
	wsURL := "ws" + baseURL[4:] + "/api/v1/agent/connect"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial agent websocket: %v", err)
	}
	return &testAgentSocket{t: t, conn: conn}
}

// newTaskDispatchServer opens a fresh store and controller with a registered
// agent server and shortened recovery scan intervals.
func newTaskDispatchServer(t *testing.T) (*store.Store, *Server, *model.Server, *httptest.Server) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{
		Name: "task-node", AgentID: "task-agent", AgentTokenHash: security.HashSecret(agentTestToken),
		ListenIP: "0.0.0.0", Status: model.ServerOnline, MonitoringMode: "lightweight",
	}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	srv.taskRecoveryScanMin = 50 * time.Millisecond
	srv.taskRecoveryScanMax = 150 * time.Millisecond
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		db.Close()
	})
	return db, srv, server, httpServer
}

const agentTestToken = "test-agent-token"

func pendingTestTask(serverID int64, index int) *model.AgentTask {
	return &model.AgentTask{
		ServerID: serverID, Type: model.AgentTaskTypeCollectLogs, PayloadJSON: `{"reason":"test"}`,
		Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "test-nonce-" + string(rune('a'+index)),
	}
}

func taskID(task map[string]any) int64 {
	raw, err := json.Marshal(task)
	if err != nil {
		panic(err)
	}
	var parsed struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		panic(err)
	}
	return parsed.ID
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestTaskDispatchTaskCreatedBeforeConnect verifies a pending task created
// before the Agent connects is dispatched immediately on connect without
// waiting for a polling tick.
func TestTaskDispatchTaskCreatedBeforeConnect(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx := context.Background()
	if err := db.CreateTask(ctx, pendingTestTask(server.ID, 0)); err != nil {
		t.Fatal(err)
	}
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	task := socket.expectTaskRequest(2 * time.Second)
	if taskID(task) <= 0 {
		t.Fatalf("invalid dispatched task: %#v", task)
	}
}

// TestTaskDispatchTaskCreatedAfterConnect verifies a task created while the
// Agent is connected is delivered promptly via the wake channel.
func TestTaskDispatchTaskCreatedAfterConnect(t *testing.T) {
	_, srv, server, httpServer := newTaskDispatchServer(t)
	ctx := context.Background()
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	task := pendingTestTask(server.ID, 1)
	if err := srv.createTaskAndWake(ctx, task); err != nil {
		t.Fatal(err)
	}
	dispatched := socket.expectTaskRequest(2 * time.Second)
	if taskID(dispatched) != task.ID {
		t.Fatalf("dispatched task %d, want %d", taskID(dispatched), task.ID)
	}
}

// TestTaskDispatchWakeLossRecovery verifies the jittered recovery scan
// delivers a task whose wake notification was lost (created directly through
// the store, bypassing the notifier).
func TestTaskDispatchWakeLossRecovery(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.StartTaskRecoveryScan(ctx)
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	if err := db.CreateTask(ctx, pendingTestTask(server.ID, 2)); err != nil {
		t.Fatal(err)
	}
	task := socket.expectTaskRequest(5 * time.Second)
	if taskID(task) <= 0 {
		t.Fatalf("invalid dispatched task: %#v", task)
	}
}

// TestTaskDispatchMultiAckImmediateNext verifies the next queued task is
// dispatched immediately after each task_ack, without waiting for a wake.
func TestTaskDispatchMultiAckImmediateNext(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := db.CreateTask(ctx, pendingTestTask(server.ID, i)); err != nil {
			t.Fatal(err)
		}
	}
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	first := socket.expectTaskRequest(2 * time.Second)
	socket.sendTaskAck(float64(taskID(first)))
	second := socket.expectTaskRequest(2 * time.Second)
	if second["id"].(float64) == first["id"].(float64) {
		t.Fatal("same task dispatched twice")
	}
	socket.sendTaskAck(float64(taskID(second)))
	third := socket.expectTaskRequest(2 * time.Second)
	if third["id"].(float64) == second["id"].(float64) {
		t.Fatal("same task dispatched twice")
	}
}

// TestTaskWakeMergeDoesNotLoseTasks verifies a burst of merged wake signals
// still delivers every queued task: after each ack the loop re-claims.
func TestTaskWakeMergeDoesNotLoseTasks(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := db.CreateTask(ctx, pendingTestTask(server.ID, i)); err != nil {
			t.Fatal(err)
		}
	}
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	for i := 0; i < 50; i++ {
		srv.tasks.wake(server.ID)
	}
	seen := map[int64]bool{}
	for i := 0; i < 5; i++ {
		task := socket.expectTaskRequest(3 * time.Second)
		id := taskID(task)
		if seen[id] {
			t.Fatalf("task %d dispatched twice", id)
		}
		seen[id] = true
		socket.sendTaskAck(float64(id))
	}
	if len(seen) != 5 {
		t.Fatalf("dispatched %d distinct tasks, want 5", len(seen))
	}
}

// TestTaskDispatchRecoveryAfterRestart verifies pending tasks survive a
// Controller restart and are delivered to the reconnected Agent.
func TestTaskDispatchRecoveryAfterRestart(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	server := &model.Server{
		Name: "task-node", AgentID: "task-agent", AgentTokenHash: security.HashSecret(agentTestToken),
		ListenIP: "0.0.0.0", Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.CreateTask(ctx, pendingTestTask(server.ID, i)); err != nil {
			t.Fatal(err)
		}
	}
	// First Controller instance processes nothing (simulating the tasks being
	// queued before shutdown).
	first := newTestServer(db, "test-secret", "")
	first.taskRecoveryScanMin = 50 * time.Millisecond
	first.taskRecoveryScanMax = 150 * time.Millisecond
	firstHTTP := httptest.NewServer(first.Handler())
	// Restart: a fresh Controller instance over the same database.
	second := newTestServer(db, "test-secret", "")
	second.taskRecoveryScanMin = 50 * time.Millisecond
	second.taskRecoveryScanMax = 150 * time.Millisecond
	secondHTTP := httptest.NewServer(second.Handler())
	defer func() {
		secondHTTP.Close()
		firstHTTP.Close()
		db.Close()
	}()
	socket := connectTestAgent(t, second, secondHTTP.URL, server)
	defer socket.close()
	firstTask := socket.expectTaskRequest(2 * time.Second)
	socket.sendTaskAck(float64(taskID(firstTask)))
	secondTask := socket.expectTaskRequest(2 * time.Second)
	if taskID(secondTask) == taskID(firstTask) {
		t.Fatal("same task dispatched twice after restart")
	}
}

// TestDuplicateAgentConnectionClaimsTaskOnce verifies two concurrent Agent
// connections for the same server can never execute the same task twice; the
// atomic NextTask claim gives it to exactly one connection.
func TestDuplicateAgentConnectionClaimsTaskOnce(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx := context.Background()
	if err := db.CreateTask(ctx, pendingTestTask(server.ID, 0)); err != nil {
		t.Fatal(err)
	}
	first := connectTestAgent(t, srv, httpServer.URL, server)
	defer first.close()
	second := connectTestAgent(t, srv, httpServer.URL, server)
	defer second.close()
	// Both loops wake and race the claim; exactly one wins.
	var received *testAgentSocket
	var receivedTask map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && received == nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		for _, socket := range []*testAgentSocket{first, second} {
			if socket == received {
				continue
			}
			_ = socket.conn.SetReadDeadline(time.Now().Add(remaining / 2))
			var raw map[string]any
			if err := socket.conn.ReadJSON(&raw); err == nil && raw["type"] == "task_request" {
				received = socket
				receivedTask = raw["task"].(map[string]any)
				break
			}
		}
	}
	if received == nil {
		t.Fatal("no connection received the task_request")
	}
	// Complete the task through the store; the second connection must not see
	// a duplicate dispatch.
	if err := srv.store.CompleteTask(ctx, taskID(receivedTask), "succeeded", `{}`); err != nil {
		t.Fatal(err)
	}
	other := first
	if received == first {
		other = second
	}
	if msg := other.readMessageMaybe(800 * time.Millisecond); msg != nil && msg["type"] == "task_request" {
		t.Fatalf("duplicate task dispatched on second connection: %#v", msg)
	}
}
