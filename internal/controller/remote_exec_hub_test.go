package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// A reconnecting Agent regularly holds two sockets at once. When the newer one
// exits first, the older connection is still reading and still reporting health,
// so the server stays online and every remote request must keep working.
func TestAgentControlSurvivesDuplicateConnectionUnregister(t *testing.T) {
	app := &Server{}
	first := make(chan any, 4)
	second := make(chan any, 4)

	app.registerAgentLive(7, first)
	app.registerAgentLive(7, second)
	app.unregisterAgentLive(7, second)

	if !app.agentControlOnline(7) {
		t.Fatal("surviving connection must keep the server reachable")
	}
	if !app.sendAgentControl(7, map[string]any{"type": "interactive_prepare"}) {
		t.Fatal("control payload must reach the surviving connection")
	}
	select {
	case payload := <-first:
		message, _ := payload.(map[string]any)
		if message["type"] != "interactive_prepare" {
			t.Fatalf("payload = %#v", payload)
		}
	default:
		t.Fatal("surviving connection received nothing")
	}

	app.unregisterAgentLive(7, first)
	if app.agentControlOnline(7) {
		t.Fatal("no connection left must report unreachable")
	}
	if app.sendAgentControl(7, map[string]any{"type": "interactive_prepare"}) {
		t.Fatal("send must fail once every connection is gone")
	}
}

// Exactly one connection receives a payload, and it is the newest one: a
// duplicated Agent identity must not start two PTYs for a single request.
func TestSendAgentControlPrefersNewestConnectionOnce(t *testing.T) {
	app := &Server{}
	older := make(chan any, 4)
	newer := make(chan any, 4)
	app.registerAgentLive(9, older)
	app.registerAgentLive(9, newer)

	if !app.sendAgentControl(9, "payload") {
		t.Fatal("send must succeed")
	}
	if len(newer) != 1 {
		t.Fatalf("newest connection received %d payloads", len(newer))
	}
	if len(older) != 0 {
		t.Fatalf("older connection received %d payloads", len(older))
	}
}

// A full newest buffer falls back to an older connection instead of reporting
// the server unreachable.
func TestSendAgentControlFallsBackWhenNewestBufferIsFull(t *testing.T) {
	app := &Server{}
	older := make(chan any, 1)
	newer := make(chan any, 1)
	newer <- "queued"
	app.registerAgentLive(11, older)
	app.registerAgentLive(11, newer)

	if !app.sendAgentControl(11, "payload") {
		t.Fatal("send must fall back to the older connection")
	}
	if len(older) != 1 {
		t.Fatalf("older connection received %d payloads", len(older))
	}
}

// End to end version of the reported failure: the panel shows the node online,
// a duplicate connection has come and gone, and opening a terminal must not
// answer agent_offline.
func TestCreateTerminalSessionAfterDuplicateAgentConnection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "terminal-duplicate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	token, _, _ := realtimeLogin(t, httpServer.URL)
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
	settingsBody, _ := json.Marshal(map[string]any{"remote_terminal_password_confirmation_enabled": false})
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

	// The connection that keeps serving this server.
	surviving := make(chan any, 4)
	app.registerAgentLive(node.ID, surviving)
	defer app.unregisterAgentLive(node.ID, surviving)
	// A reconnect attempt that registered and then died.
	duplicate := make(chan any, 4)
	app.registerAgentLive(node.ID, duplicate)
	app.unregisterAgentLive(node.ID, duplicate)

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
		t.Fatalf("create status = %d body=%s", createRes.StatusCode, readBody(t, createRes))
	}
	select {
	case prepare := <-surviving:
		payload, _ := prepare.(map[string]any)
		if payload["type"] != "interactive_prepare" {
			t.Fatalf("prepare = %#v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("surviving connection never received interactive_prepare")
	}
}

// When the persisted row is still online but no socket is left, the failure must
// name the control channel instead of claiming the host is offline.
func TestCreateTerminalSessionReportsControlChannelUnavailable(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "terminal-no-channel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	token, _, _ := realtimeLogin(t, httpServer.URL)
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
	settingsBody, _ := json.Marshal(map[string]any{"remote_terminal_password_confirmation_enabled": false})
	settingsReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ui/settings", bytes.NewReader(settingsBody))
	settingsReq.Header.Set("Content-Type", "application/json")
	settingsReq.Header.Set("Authorization", "Bearer "+token)
	settingsRes, err := httpServer.Client().Do(settingsReq)
	if err != nil {
		t.Fatal(err)
	}
	settingsRes.Body.Close()

	createBody, _ := json.Marshal(map[string]any{"cols": 80, "rows": 24})
	createReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ui/servers/"+itoa(node.ID)+"/terminal/sessions", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRes, err := httpServer.Client().Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createRes.Body.Close()
	if createRes.StatusCode != http.StatusConflict {
		t.Fatalf("create status = %d", createRes.StatusCode)
	}
	var failure map[string]any
	if err := json.NewDecoder(createRes.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure["code"] != "agent_control_unavailable" {
		t.Fatalf("failure = %#v", failure)
	}
}

// An Agent that reconnected but has not reported health yet is reachable: the
// gate must not refuse it just because the persisted row still says offline.
func TestRemoteTerminalGateAcceptsLiveChannelWhileStatusLags(t *testing.T) {
	app := &Server{}
	node := &model.Server{ID: 21, Status: model.ServerOffline}
	view := remoteAccessView{
		Agent: model.ServerRemoteAccessStatus{
			ServerID:     node.ID,
			Capabilities: []string{model.RemoteAccessCapabilityTerminal},
			LocalMode:    model.RemoteAccessModeStandard,
		},
	}
	view.Global.RemoteTerminalEnabled = true
	view.Server.RemoteTerminalEnabled = true

	if reasons := app.remoteAccessUnavailableReasons(node, view, "remote_terminal"); len(reasons) == 0 {
		t.Fatal("an offline row with no channel must still be refused")
	}

	live := make(chan any, 1)
	app.registerAgentLive(node.ID, live)
	defer app.unregisterAgentLive(node.ID, live)
	if reasons := app.remoteAccessUnavailableReasons(node, view, "remote_terminal"); len(reasons) != 0 {
		t.Fatalf("live channel must satisfy the gate, got %v", reasons)
	}
}
