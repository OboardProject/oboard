package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/model"
)

func TestMCPSystemGetCapabilitiesIsStable(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	for _, name := range []string{"system_get_capabilities", "system_bootstrap"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("%s call: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s returned error", name)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		body := string(raw)
		for _, fragment := range []string{`"capability_revision"`, `"toolset_hash":"sha256:`, `"server_version"`, `"api_version"`, `"min_mcp_protocol"`} {
			if !strings.Contains(body, fragment) {
				t.Fatalf("%s output missing %q: %s", name, fragment, body)
			}
		}
		if name == "system_get_capabilities" {
			for _, fragment := range []string{`"host_access"`, `"remote_operations"`, `"structured_exec"`, `"raw_shell"`, `"interactive_terminal"`, `"tool_visible":true`, `"authorized":false`, `"reason":"privileged_grant_required"`} {
				if !strings.Contains(body, fragment) {
					t.Fatalf("%s host access output missing %q: %s", name, fragment, body)
				}
			}
		}
	}
	// Tools list must contain the stable tools.
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"system_get_capabilities", "system_bootstrap", "oboard_task", "oboard_commit_task"} {
		if !names[want] {
			t.Fatalf("tools/list missing %q", want)
		}
	}
}

func TestMCPInitializeAdvertisesListChanged(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	clientCaps := session.InitializeResult().Capabilities
	if clientCaps == nil || clientCaps.Tools == nil || !clientCaps.Tools.ListChanged {
		t.Fatalf("initialize response missing tools.listChanged: %#v", clientCaps)
	}
	if clientCaps.Resources == nil || !clientCaps.Resources.ListChanged {
		t.Fatalf("initialize response missing resources.listChanged: %#v", clientCaps)
	}
	if clientCaps.Prompts == nil || !clientCaps.Prompts.ListChanged {
		t.Fatalf("initialize response missing prompts.listChanged: %#v", clientCaps)
	}
}

func TestMCPToolListChangedNotification(t *testing.T) {
	db, server, _, principal, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	if err := db.SetSetting(context.Background(), "controller_url", httpServer.URL); err != nil {
		t.Fatal(err)
	}
	grants, err := db.ListOAuthGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var grant *model.OAuthGrant
	for i := range grants {
		if grants[i].PrincipalID == principal.ID {
			grant = &grants[i]
			break
		}
	}
	if grant == nil {
		t.Fatal("OAuth grant not found")
	}
	clientRecord, err := db.GetOAuthClient(context.Background(), grant.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	plain := "oba_notify-" + randomTestID()
	issueTestMCPToken(t, db, grant, principal, clientRecord, grant.UserID, httpServer.URL+"/api/v1/mcp", plain)

	notified := make(chan struct{}, 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-style-notify", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case notified <- struct{}{}:
			default:
			}
		},
	})
	httpClient := &http.Client{Transport: bearerTransport{token: plain, base: http.DefaultTransport}}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelConnect()
	session, err := client.Connect(connectCtx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/api/v1/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	server.notifyMCPToolsChanged()
	select {
	case <-notified:
	case <-time.After(3 * time.Second):
		t.Fatal("connected Streamable HTTP client did not receive notifications/tools/list_changed")
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "server_terminal_open" {
			return
		}
	}
	t.Fatal("stable terminal tools disappeared after list_changed")
}

func TestMCPReadGrantSeesStableManifestButNotOperateTools(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "read", []string{"oboard:read"})
	defer closeServer()

	// Read grant should see system_get_capabilities (read) but not operate tools
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["system_get_capabilities"] {
		t.Fatal("read grant missing system_get_capabilities")
	}
	if names["oboard_commit_task"] {
		t.Fatal("read grant should not see oboard_commit_task")
	}
	if names["oboard_submit_changeset"] {
		t.Fatal("read grant should not see oboard_submit_changeset")
	}
	// But it should see read capability tools
	if !names["oboard_task"] {
		t.Fatal("read grant missing oboard_task")
	}

	// system_get_capabilities should still succeed
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "system_get_capabilities", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("system_get_capabilities for read grant: err=%v result=%#v", err, result)
	}
	// system_bootstrap should also succeed
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "system_bootstrap", Arguments: map[string]any{"client_name": "test", "protocol": "2025-11-25"}})
	if err != nil || result.IsError {
		t.Fatalf("system_bootstrap for read grant: err=%v result=%#v", err, result)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), `"dynamic_tool_discovery":true`) {
		t.Fatalf("system_bootstrap missing dynamic_tool_discovery: %s", raw)
	}
	_ = time.Now
}
