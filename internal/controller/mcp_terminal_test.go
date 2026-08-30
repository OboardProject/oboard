package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func newTerminalWebSocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			accepted <- conn
		}
	}))
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	agent, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		httpServer.Close()
		t.Fatal(err)
	}
	controller := <-accepted
	return controller, agent, func() {
		_ = controller.Close()
		_ = agent.Close()
		httpServer.CloseClientConnections()
		httpServer.Close()
	}
}

func mcpResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	raw, _ := json.Marshal(result.StructuredContent)
	return string(raw)
}

func upsertInteractiveTestGrant(t *testing.T, db interface {
	ListOAuthGrants(context.Context) ([]model.OAuthGrant, error)
	UpsertMCPPrivilegedGrant(context.Context, model.MCPPrivilegedGrant) (*model.MCPPrivilegedGrant, error)
}, principalID string) *model.MCPPrivilegedGrant {
	t.Helper()
	grants, err := db.ListOAuthGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var grant *model.OAuthGrant
	for i := range grants {
		if grants[i].PrincipalID == principalID {
			grant = &grants[i]
			break
		}
	}
	if grant == nil {
		t.Fatal("OAuth grant for test principal was not found")
	}
	boundary, _ := json.Marshal(mcpauth.ResourceBoundary{
		Version: mcpauth.ResourceBoundaryVersion,
		Resources: map[string]mcpauth.ResourceSelection{
			"server": {Selection: mcpauth.SelectionAll},
		},
	})
	saved, err := db.UpsertMCPPrivilegedGrant(context.Background(), model.MCPPrivilegedGrant{
		OAuthGrantID: grant.ID, OAuthClientID: grant.ClientID, AuthorizedUserID: grant.UserID,
		Capabilities: []string{model.PrivilegeRemoteInteractive}, ResourceBoundaryJSON: boundary,
		CreatedByUserID: grant.UserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestHermesStableTerminalToolsAuthorizeWithoutRelist(t *testing.T) {
	db, app, session, principal, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	ctx := context.Background()

	node := &model.Server{
		Name: "Hermes terminal", AgentID: "agent-hermes", AgentTokenHash: security.HashSecret("agent-token"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingMCPInteractiveTerminalEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: node.ID, RemoteTerminalEnabled: true, MCPInteractiveEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{
		Capabilities: []string{model.RemoteAccessCapabilityInteractiveMCP, model.RemoteAccessCapabilityTerminalLoginEnv},
		LocalMode:    model.RemoteAccessModeStandard,
	}); err != nil {
		t.Fatal(err)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	visible := map[string]bool{}
	for _, tool := range tools.Tools {
		visible[tool.Name] = true
	}
	for _, name := range []string{"server_terminal_command", "server_terminal_open", "server_terminal_io", "server_terminal_resize", "server_terminal_close"} {
		if !visible[name] {
			t.Fatalf("initial tools/list missing %q", name)
		}
	}

	execDenied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "server_exec", Arguments: map[string]any{"server_id": node.ID, "argv": []string{"true"}}})
	if err != nil {
		t.Fatal(err)
	}
	execDeniedText := mcpResultText(execDenied)
	if !execDenied.IsError || !strings.Contains(execDeniedText, `"status":"denied"`) || !strings.Contains(execDeniedText, `"code":"privileged_grant_required"`) || !strings.Contains(execDeniedText, `"required_privilege":"remote_exec"`) {
		t.Fatalf("server_exec without privileged grant = %s", execDeniedText)
	}

	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "server_terminal_open", Arguments: map[string]any{"server_id": node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	deniedText := mcpResultText(denied)
	if !denied.IsError || !strings.Contains(deniedText, `"status":"denied"`) || !strings.Contains(deniedText, `"code":"privileged_grant_required"`) || !strings.Contains(deniedText, `"required_privilege":"remote_interactive"`) {
		t.Fatalf("open without privileged grant = %s", deniedText)
	}

	privileged := upsertInteractiveTestGrant(t, db, principal.ID)
	capabilities, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "system_get_capabilities", Arguments: map[string]any{}})
	if err != nil || capabilities.IsError {
		t.Fatalf("system_get_capabilities after grant: err=%v result=%s", err, mcpResultText(capabilities))
	}
	capabilityText := mcpResultText(capabilities)
	if !strings.Contains(capabilityText, `"interactive_terminal":{"authorized":true`) || !strings.Contains(capabilityText, `"servers":[1]`) || !strings.Contains(capabilityText, `"tool_visible":true`) {
		t.Fatalf("interactive host_access state = %s", capabilityText)
	}
	assertOpenDenied := func(code string) {
		t.Helper()
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "server_terminal_open", Arguments: map[string]any{"server_id": node.ID}})
		if callErr != nil {
			t.Fatal(callErr)
		}
		body := mcpResultText(result)
		if !result.IsError || !strings.Contains(body, `"status":"denied"`) || !strings.Contains(body, `"code":"`+code+`"`) {
			t.Fatalf("terminal denial %s = %s", code, body)
		}
	}
	if err := db.SetSetting(ctx, settingMCPInteractiveTerminalEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	assertOpenDenied("remote_access_global_disabled")
	if err := db.SetSetting(ctx, settingMCPInteractiveTerminalEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: node.ID, RemoteTerminalEnabled: true}); err != nil {
		t.Fatal(err)
	}
	assertOpenDenied("remote_access_server_disabled")
	if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: node.ID, RemoteTerminalEnabled: true, MCPInteractiveEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityTerminalLoginEnv}, LocalMode: model.RemoteAccessModeStandard}); err != nil {
		t.Fatal(err)
	}
	assertOpenDenied("agent_upgrade_required")
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityInteractiveMCP, model.RemoteAccessCapabilityTerminalLoginEnv}, LocalMode: model.RemoteAccessModeHardened}); err != nil {
		t.Fatal(err)
	}
	assertOpenDenied("agent_local_gate_denied")
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityInteractiveMCP, model.RemoteAccessCapabilityTerminalLoginEnv}, LocalMode: model.RemoteAccessModeStandard}); err != nil {
		t.Fatal(err)
	}
	deniedBoundary, _ := json.Marshal(mcpauth.ResourceBoundary{Version: mcpauth.ResourceBoundaryVersion, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionSelected, IDs: []string{"999"}}}})
	privileged.ResourceBoundaryJSON = deniedBoundary
	if _, err := db.UpsertMCPPrivilegedGrant(ctx, *privileged); err != nil {
		t.Fatal(err)
	}
	assertOpenDenied("privileged_resource_denied")
	privileged = upsertInteractiveTestGrant(t, db, principal.ID)

	live := make(chan any, 4)
	app.registerAgentLive(node.ID, live)
	defer app.unregisterAgentLive(node.ID, live)
	controllerConn, agentConn, closePair := newTerminalWebSocketPair(t)
	defer closePair()
	prepareSeen := make(chan map[string]any, 1)
	go func() {
		payload, _ := (<-live).(map[string]any)
		prepareSeen <- payload
		sessionID, _ := payload["session_id"].(string)
		app.terminalHub.mu.Lock()
		terminal := app.terminalHub.sessions[sessionID]
		app.terminalHub.mu.Unlock()
		if terminal == nil {
			return
		}
		terminal.mu.Lock()
		terminal.agent = controllerConn
		terminal.mu.Unlock()
		terminal.markAgentConnected()
		app.relayTerminal(terminal)
		_ = agentConn.WriteMessage(websocket.BinaryMessage, []byte("login$ "))
	}()

	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "server_terminal_open", Arguments: map[string]any{"server_id": node.ID, "mode": "login"}})
	if err != nil {
		t.Fatal(err)
	}
	if opened.IsError {
		t.Fatalf("open after grant without tools/list = %s", mcpResultText(opened))
	}
	prepare := <-prepareSeen
	if prepare["origin"] != model.InteractiveOriginMCP || prepare["mode"] != "login" {
		t.Fatalf("interactive_prepare = %#v", prepare)
	}
	app.terminalHub.mu.Lock()
	var active *terminalSession
	for _, item := range app.terminalHub.sessions {
		active = item
		break
	}
	app.terminalHub.mu.Unlock()
	if active == nil || active.PrivilegedGrantID != privileged.ID || active.PrivilegedGrantID == 0 {
		t.Fatalf("terminal privileged grant binding = %#v, want %d", active, privileged.ID)
	}

	if err := db.RevokeMCPPrivilegedGrant(ctx, privileged.ID); err != nil {
		t.Fatal(err)
	}
	app.closeMCPTerminalsForGrant(privileged.OAuthGrantID)
	if app.terminalHub.countForServer(node.ID) != 0 {
		t.Fatal("revoked privileged grant left an active PTY")
	}
	revoked, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "server_terminal_open", Arguments: map[string]any{"server_id": node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.IsError || (!strings.Contains(mcpResultText(revoked), `"code":"privileged_grant_revoked"`) && !strings.Contains(mcpResultText(revoked), `"code":"privileged_grant_required"`)) {
		t.Fatalf("open after revoke = %s", mcpResultText(revoked))
	}
}

func TestHermesCLIInitialDiscoveryIncludesStableTerminalTools(t *testing.T) {
	hermesBin := strings.TrimSpace(os.Getenv("HERMES_BIN"))
	if hermesBin == "" {
		t.Skip("set HERMES_BIN to run the real Hermes CLI integration")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "hermes-cli.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	if err := db.SetSetting(context.Background(), "controller_url", httpServer.URL); err != nil {
		t.Fatal(err)
	}
	request(t, app.Handler(), http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_hermes_cli", "Hermes CLI", []string{"http://127.0.0.1/callback"})
	grant, principal := createTestGrant(t, app, *user, client, []string{"oboard:read", "oboard:operate"})
	plain := "oba_hermes-" + randomTestID()
	issueTestMCPToken(t, db, grant, principal, client, user.ID, httpServer.URL+"/api/v1/mcp", plain)

	hermesHome := filepath.Join(t.TempDir(), "hermes-home")
	if err := os.MkdirAll(hermesHome, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "mcp_servers:\n  oboard:\n    url: " + httpServer.URL + "/api/v1/mcp\n    headers:\n      Authorization: 'Bearer ${MCP_OBOARD_API_KEY}'\n"
	if err := os.WriteFile(filepath.Join(hermesHome, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, hermesBin, "mcp", "test", "oboard")
	command.Env = append(os.Environ(), "HERMES_HOME="+hermesHome, "MCP_OBOARD_API_KEY="+plain, "NO_COLOR=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("hermes mcp test oboard: %v\n%s", err, output)
	}
	body := string(output)
	if !strings.Contains(body, "Connected") {
		t.Fatalf("Hermes did not connect:\n%s", body)
	}
	for _, name := range []string{"server_terminal_command", "server_terminal_open", "server_terminal_io", "server_terminal_resize", "server_terminal_close"} {
		if !strings.Contains(body, name) {
			t.Fatalf("Hermes initial registry missing %q:\n%s", name, body)
		}
	}
}

func TestServerTerminalCommandUsesMCPLoginPTY(t *testing.T) {
	db, app, session, principal, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	ctx := context.Background()
	node := &model.Server{Name: "PTY command", AgentID: "agent-command", AgentTokenHash: security.HashSecret("agent-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingMCPInteractiveTerminalEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: node.ID, RemoteTerminalEnabled: true, MCPInteractiveEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityInteractiveMCP, model.RemoteAccessCapabilityTerminalLoginEnv}, LocalMode: model.RemoteAccessModeStandard}); err != nil {
		t.Fatal(err)
	}
	privileged := upsertInteractiveTestGrant(t, db, principal.ID)
	live := make(chan any, 4)
	app.registerAgentLive(node.ID, live)
	defer app.unregisterAgentLive(node.ID, live)
	controllerConn, agentConn, closePair := newTerminalWebSocketPair(t)
	defer closePair()
	markerPattern := regexp.MustCompile(`__OBOARD_MCP_DONE_[A-Za-z0-9_-]+__`)
	fakeAgentResult := make(chan string, 1)
	go func() {
		payload, _ := (<-live).(map[string]any)
		if payload["origin"] != model.InteractiveOriginMCP || payload["mode"] != "login" {
			fakeAgentResult <- "unexpected prepare payload"
			return
		}
		sessionID, _ := payload["session_id"].(string)
		app.terminalHub.mu.Lock()
		terminal := app.terminalHub.sessions[sessionID]
		app.terminalHub.mu.Unlock()
		if terminal == nil || terminal.PrivilegedGrantID != privileged.ID {
			fakeAgentResult <- "missing terminal or privileged grant binding"
			return
		}
		terminal.mu.Lock()
		terminal.agent = controllerConn
		terminal.mu.Unlock()
		terminal.markAgentConnected()
		app.relayTerminal(terminal)
		_, firstPayload, firstErr := agentConn.ReadMessage()
		if firstErr != nil {
			fakeAgentResult <- "first read: " + firstErr.Error()
			return
		}
		_, commandPayload, err := agentConn.ReadMessage()
		if err != nil {
			fakeAgentResult <- "command read: " + err.Error() + " first=" + string(firstPayload)
			return
		}
		marker := markerPattern.FindString(string(commandPayload))
		if marker == "" {
			fakeAgentResult <- "marker missing in " + string(commandPayload)
			return
		}
		if err := agentConn.WriteMessage(websocket.BinaryMessage, []byte("/vendor/login/bin\n"+marker+":7\n")); err != nil {
			fakeAgentResult <- "write: " + err.Error()
			return
		}
		fakeAgentResult <- "ok"
	}()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "server_terminal_command", Arguments: map[string]any{
		"server_id": node.ID, "command": "vendor-cli --path", "timeout_ms": 2000, "mode": "login",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		select {
		case fake := <-fakeAgentResult:
			t.Fatalf("terminal command error = %s; fake agent = %s", mcpResultText(result), fake)
		default:
			t.Fatalf("terminal command error = %s; fake agent did not report", mcpResultText(result))
		}
	}
	body := mcpResultText(result)
	if !strings.Contains(body, `"exit_code":7`) || !strings.Contains(body, `/vendor/login/bin`) {
		t.Fatalf("terminal command result = %s", body)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && app.terminalHub.countForServer(node.ID) != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if app.terminalHub.countForServer(node.ID) != 0 {
		t.Fatal("terminal command did not close its PTY")
	}
}
