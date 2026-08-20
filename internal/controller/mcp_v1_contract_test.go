package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestV1UnifiedRoutesRemoveLegacyPrefixes(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	h := newTestServer(db, "test-secret", "").Handler()

	for _, path := range []string{
		"/api/v2/version",
		"/api/v2/ui/version",
		"/api/v2/capabilities",
		"/mcp",
		"/mcp/initialize",
	} {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d; want 404", path, recorder.Code)
		}
	}

	version := httptest.NewRecorder()
	h.ServeHTTP(version, httptest.NewRequest(http.MethodGet, "/api/v1/ui/version", nil))
	if version.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/ui/version status = %d; want 200", version.Code)
	}
	if !strings.Contains(version.Body.String(), `"api_prefix":"/api/v1"`) {
		t.Fatalf("version response does not advertise /api/v1: %s", version.Body.String())
	}

	mcp := httptest.NewRecorder()
	h.ServeHTTP(mcp, httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)))
	if mcp.Code != http.StatusUnauthorized && mcp.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/v1/mcp status = %d; want 401 before authentication", mcp.Code)
	}
}

func TestMCPCapabilityToolAndResourceCoverage(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, capability := range []string{"servers.list", "servers.get", "users.create", "routing_rules.place", "dns_lists.list"} {
		name := mcpCapabilityToolName(capability)
		if !names[name] {
			t.Fatalf("MCP tools/list is missing generated tool %q for %s", name, capability)
		}
	}

	resources, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resourceURIs := map[string]bool{}
	for _, resource := range resources.Resources {
		resourceURIs[resource.URI] = true
	}
	templates, err := session.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, template := range templates.ResourceTemplates {
		resourceURIs[template.URITemplate] = true
	}
	for _, uri := range []string{
		"oboard://servers",
		"oboard://user-group-members",
		"oboard://capability/node_incidents.get/{id}",
	} {
		if !resourceURIs[uri] {
			t.Fatalf("MCP resources/list is missing generated resource %q; got %d resources", uri, len(resourceURIs))
		}
	}

	for _, capability := range []string{"settings.get", "agent_tasks.list", "audit.connection.overview", "access_changes.list"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: mcpCapabilityToolName(capability), Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("dynamic capability %s call: %v", capability, err)
		}
		if result.IsError {
			t.Fatalf("dynamic capability %s returned an error result", capability)
		}
		var text string
		for _, content := range result.Content {
			if item, ok := content.(*mcp.TextContent); ok {
				text += item.Text
			}
		}
		var envelope struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(text), &envelope); err != nil || envelope.Status != "succeeded" {
			t.Fatalf("dynamic capability %s did not return a succeeded envelope: %q err=%v", capability, text, err)
		}
	}
}
