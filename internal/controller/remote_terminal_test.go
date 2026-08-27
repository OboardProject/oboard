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
