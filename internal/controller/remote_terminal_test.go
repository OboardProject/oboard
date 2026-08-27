package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestBrowserTerminalWebsocketUsesTicketCookieAndRelaysOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "terminal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	token, sessionCookie, _ := realtimeLogin(t, httpServer.URL)
	ctx := context.Background()
	node := &model.Server{
		Name: "GL-U", AgentID: "agent-gl-u", AgentTokenHash: security.HashSecret("agent-token"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{
		Capabilities: []string{model.RemoteAccessCapabilityTerminal},
		LocalMode:    model.RemoteAccessModeStandard,
	}); err != nil {
		t.Fatal(err)
	}
	falseValue := false
	settingsBody, _ := json.Marshal(map[string]any{"remote_terminal_password_confirmation_enabled": falseValue})
	settingsReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ui/settings", bytes.NewReader(settingsBody))
	settingsReq.Header.Set("Content-Type", "application/json")
	settingsReq.Header.Set("Authorization", "Bearer "+token)
	settingsRes, err := httpServer.Client().Do(settingsReq)
	if err != nil {
		t.Fatal(err)
	}
	settingsRes.Body.Close()
	if settingsRes.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", settingsRes.StatusCode)
	}

	live := make(chan any, 4)
	app.registerAgentLive(node.ID, live)
	defer app.unregisterAgentLive(node.ID, live)

	createBody, _ := json.Marshal(map[string]any{"cols": 80, "rows": 24})
	createReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ui/servers/"+itoa(node.ID)+"/terminal/sessions", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRes, err := httpServer.Client().Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createRes.Body.Close()
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create session status = %d body=%s", createRes.StatusCode, readBody(t, createRes))
	}
	var created map[string]any
	if err := json.NewDecoder(createRes.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	sessionID, _ := created["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("create session = %#v", created)
	}
	ticket := cookieNamed(createRes.Cookies(), terminalCookieName)
	if ticket == nil || ticket.Value == "" || ticket.SameSite != http.SameSiteLaxMode {
		t.Fatalf("terminal ticket cookie = %#v", ticket)
	}
	if !strings.Contains(ticket.Path, "/api/v1/ui/servers/"+itoa(node.ID)+"/terminal/") {
		t.Fatalf("terminal ticket path = %q", ticket.Path)
	}

	prepare := <-live
	payload, _ := prepare.(map[string]any)
	nonce, _ := payload["nonce"].(string)
	expires, _ := payload["expires_at"].(string)
	if nonce == "" || expires == "" {
		t.Fatalf("interactive prepare = %#v", payload)
	}

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ui/servers/" + itoa(node.ID) + "/terminal/ws/" + sessionID
	if conn, response, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Cookie": []string{sessionCookie.Name + "=" + sessionCookie.Value},
		"Origin": []string{httpServer.URL},
	}); err == nil {
		conn.Close()
		t.Fatal("websocket without ticket unexpectedly connected")
	} else if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing ticket status = %#v err=%v", response, err)
	}

	browserHeader := http.Header{
		"Cookie": []string{sessionCookie.Name + "=" + sessionCookie.Value + "; " + ticket.Name + "=" + ticket.Value},
		"Origin": []string{httpServer.URL},
	}
	agentHeader := http.Header{
		"Authorization":              []string{"Bearer agent-token"},
		"X-Agent-ID":                 []string{node.AgentID},
		"X-OBoard-Interactive-Proof": []string{security.InteractiveProof(security.HashSecret("agent-token"), sessionID, node.ID, nonce, expires)},
		"Origin":                     []string{httpServer.URL},
	}
	agentURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/agent/interactive/" + sessionID

	var browserConn, agentConn *websocket.Conn
	var browserErr, agentErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		browserConn, _, browserErr = websocket.DefaultDialer.Dial(wsURL, browserHeader)
	}()
	go func() {
		defer wg.Done()
		agentConn, _, agentErr = websocket.DefaultDialer.Dial(agentURL, agentHeader)
	}()
	wg.Wait()
	if browserErr != nil {
		t.Fatalf("browser websocket: %v", browserErr)
	}
	defer browserConn.Close()
	if agentErr != nil {
		t.Fatalf("agent websocket: %v", agentErr)
	}
	defer agentConn.Close()

	if err := agentConn.WriteMessage(websocket.BinaryMessage, []byte("prompt>")); err != nil {
		t.Fatal(err)
	}
	_ = browserConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := browserConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "prompt>" {
		t.Fatalf("browser received %q", data)
	}
	if err := browserConn.WriteMessage(websocket.BinaryMessage, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	_ = agentConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err = agentConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ls\n" {
		t.Fatalf("agent received %q", data)
	}
	time.Sleep(50 * time.Millisecond)
	if err := browserConn.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("relay closed early: %v", err)
	}
}

func TestInteractiveFailedClosesBrowserAndFreesSlot(t *testing.T) {
	app, httpServer, token, sessionCookie, node, _, sessionID, ticket, cleanup := startTerminalSession(t)
	defer cleanup()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ui/servers/" + itoa(node.ID) + "/terminal/ws/" + sessionID
	browserConn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Cookie": []string{sessionCookie.Name + "=" + sessionCookie.Value + "; " + ticket.Name + "=" + ticket.Value},
		"Origin": []string{httpServer.URL},
	})
	if err != nil {
		t.Fatalf("browser websocket: %v", err)
	}
	defer browserConn.Close()

	app.handleInteractiveAgentStatus(node.ID, map[string]json.RawMessage{
		"type":       json.RawMessage(`"interactive_failed"`),
		"session_id": json.RawMessage(`"` + sessionID + `"`),
		"reason":     json.RawMessage(`"pty_start_failed"`),
		"detail":     json.RawMessage(`"fork/exec /bin/bash: operation not permitted"`),
	})

	_ = browserConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := browserConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	if message["type"] != "error" || message["reason"] != "pty_start_failed" {
		t.Fatalf("browser error = %#v", message)
	}
	if app.terminalHub.countForServer(node.ID) != 0 {
		t.Fatal("failed session still occupies a terminal slot")
	}

	createBody, _ := json.Marshal(map[string]any{"cols": 80, "rows": 24})
	createReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ui/servers/"+itoa(node.ID)+"/terminal/sessions", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRes, err := httpServer.Client().Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createRes.Body.Close()
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create after fail status = %d body=%s", createRes.StatusCode, readBody(t, createRes))
	}
}

func TestInteractivePrepareTimeoutClosesUnusedSession(t *testing.T) {
	previous := terminalPrepareTimeout
	terminalPrepareTimeout = 400 * time.Millisecond
	defer func() { terminalPrepareTimeout = previous }()

	app, httpServer, _, sessionCookie, node, _, sessionID, ticket, cleanup := startTerminalSession(t)
	defer cleanup()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ui/servers/" + itoa(node.ID) + "/terminal/ws/" + sessionID
	browserConn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Cookie": []string{sessionCookie.Name + "=" + sessionCookie.Value + "; " + ticket.Name + "=" + ticket.Value},
		"Origin": []string{httpServer.URL},
	})
	if err != nil {
		t.Fatalf("browser websocket: %v", err)
	}
	defer browserConn.Close()

	_ = browserConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := browserConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	if message["type"] != "error" || message["reason"] != "prepare_timeout" {
		t.Fatalf("timeout message = %#v", message)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if app.terminalHub.countForServer(node.ID) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed-out session still occupies a terminal slot")
}

func TestInteractiveReadyCancelsPrepareTimeout(t *testing.T) {
	previous := terminalPrepareTimeout
	terminalPrepareTimeout = 250 * time.Millisecond
	defer func() { terminalPrepareTimeout = previous }()

	app, _, _, _, node, _, sessionID, _, cleanup := startTerminalSession(t)
	defer cleanup()

	app.handleInteractiveAgentStatus(node.ID, map[string]json.RawMessage{
		"type":       json.RawMessage(`"interactive_ready"`),
		"session_id": json.RawMessage(`"` + sessionID + `"`),
	})
	time.Sleep(400 * time.Millisecond)
	if app.terminalHub.countForServer(node.ID) != 1 {
		t.Fatal("ready session was closed by prepare timeout")
	}
	app.terminalHub.mu.Lock()
	session := app.terminalHub.sessions[sessionID]
	app.terminalHub.mu.Unlock()
	if session == nil || !session.agentReady {
		t.Fatal("session was not marked ready")
	}
}

func startTerminalSession(t *testing.T) (*Server, *httptest.Server, string, *http.Cookie, *model.Server, chan any, string, *http.Cookie, func()) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "terminal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(app.Handler())
	token, sessionCookie, _ := realtimeLogin(t, httpServer.URL)
	ctx := context.Background()
	node := &model.Server{
		Name: "GL-U", AgentID: "agent-gl-u", AgentTokenHash: security.HashSecret("agent-token"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{
		Capabilities: []string{model.RemoteAccessCapabilityTerminal},
		LocalMode:    model.RemoteAccessModeStandard,
	}); err != nil {
		t.Fatal(err)
	}
	falseValue := false
	settingsBody, _ := json.Marshal(map[string]any{"remote_terminal_password_confirmation_enabled": falseValue})
	settingsReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ui/settings", bytes.NewReader(settingsBody))
	settingsReq.Header.Set("Content-Type", "application/json")
	settingsReq.Header.Set("Authorization", "Bearer "+token)
	settingsRes, err := httpServer.Client().Do(settingsReq)
	if err != nil {
		t.Fatal(err)
	}
	settingsRes.Body.Close()
	if settingsRes.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", settingsRes.StatusCode)
	}

	live := make(chan any, 4)
	app.registerAgentLive(node.ID, live)

	createBody, _ := json.Marshal(map[string]any{"cols": 80, "rows": 24})
	createReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ui/servers/"+itoa(node.ID)+"/terminal/sessions", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRes, err := httpServer.Client().Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createRes.Body.Close()
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create session status = %d body=%s", createRes.StatusCode, readBody(t, createRes))
	}
	var created map[string]any
	if err := json.NewDecoder(createRes.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	sessionID, _ := created["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("create session = %#v", created)
	}
	ticket := cookieNamed(createRes.Cookies(), terminalCookieName)
	if ticket == nil {
		t.Fatal("missing terminal ticket")
	}
	select {
	case <-live:
	case <-time.After(time.Second):
		t.Fatal("missing interactive_prepare")
	}
	cleanup := func() {
		app.unregisterAgentLive(node.ID, live)
		httpServer.Close()
		app.Close()
		db.Close()
	}
	return app, httpServer, token, sessionCookie, node, live, sessionID, ticket, cleanup
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(res.Body)
	return buf.String()
}
