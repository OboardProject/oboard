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
	"github.com/OboardProject/oboard/internal/security"
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
		if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `"not_found"`) || !strings.Contains(recorder.Body.String(), `"request_id"`) {
			t.Errorf("GET %s status=%d body=%s; want structured 404", path, recorder.Code, recorder.Body.String())
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

func TestMCPChangesPublishRealtimeInvalidation(t *testing.T) {
	db, server, session, _, closeServer := newMCPTestEnvironment(t, "", []string{"oboard:read", "oboard:operate", "offline_access"})
	defer closeServer()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: mcpCapabilityToolName("settings.update"),
		Arguments: map[string]any{
			"capability_input":    map[string]any{"changes": map[string]any{"subscription_controller_direct_enabled": true}},
			"reason":              "realtime regression test",
			"idempotency_key":     "realtime-regression-123",
			"approval_preference": "use_preapproval_if_available",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		var text string
		for _, content := range result.Content {
			if item, ok := content.(*mcp.TextContent); ok {
				text += item.Text
			}
		}
		t.Fatalf("settings.update returned an error result: %s", text)
	}

	server.realtime.mu.Lock()
	sequence := server.realtime.sequence.Load()
	allSequence := server.realtime.resourceSequences["all"]
	server.realtime.mu.Unlock()
	if sequence == 0 || allSequence == 0 {
		t.Fatalf("MCP mutation did not publish realtime invalidation: sequence=%d all=%d", sequence, allSequence)
	}
	_ = db
}

func TestMCPRelayActivationPublishesRealtimeAndKeepsPublicURL(t *testing.T) {
	db, server, session, _, closeServer := newMCPTestEnvironment(t, "", []string{"oboard:read", "oboard:operate", "offline_access"})
	defer closeServer()

	expiresAt := time.Now().Add(time.Hour)
	relay := &model.SubscriptionRelay{Name: "relay", PublicURL: "https://relay.example", Status: "pending", EnrollmentHash: security.HashSecret("enroll-token"), EnrollmentExpiresAt: &expiresAt}
	if err := db.CreateSubscriptionRelay(context.Background(), relay); err != nil {
		t.Fatal(err)
	}
	encrypted, err := security.EncryptSecret(server.sessionSecret, subscriptionRelaySecretPurpose, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimSubscriptionRelayEnrollment(context.Background(), security.HashSecret("enroll-token"), security.HashSecret("relay-token"), encrypted); err != nil {
		t.Fatal(err)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: mcpCapabilityToolName("subscription_relays.activate"),
		Arguments: map[string]any{
			"capability_input":    map[string]any{"relay_id": relay.ID},
			"reason":              "relay realtime regression test",
			"idempotency_key":     "relay-realtime-regression-123",
			"approval_preference": "use_preapproval_if_available",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		var text string
		for _, content := range result.Content {
			if item, ok := content.(*mcp.TextContent); ok {
				text += item.Text
			}
		}
		t.Fatalf("subscription_relays.activate returned an error result: %s", text)
	}

	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings[settingSubscriptionRelayURL] != "https://relay.example" || settings[settingSubscriptionControllerDirectEnabled] != "false" {
		t.Fatalf("relay settings after MCP activation=%#v", settings)
	}
	public := server.publicSettings(context.Background(), settings)
	if public[settingSubscriptionRelayURL] != "https://relay.example" {
		t.Fatalf("public settings relay URL=%q", public[settingSubscriptionRelayURL])
	}

	server.realtime.mu.Lock()
	allSequence := server.realtime.resourceSequences["all"]
	server.realtime.mu.Unlock()
	if allSequence == 0 {
		t.Fatal("MCP relay activation did not publish realtime invalidation")
	}
}

func TestMCPMetricsCapabilitiesReturnExistingData(t *testing.T) {
	db, _, session, _, closeServer := newMCPTestEnvironment(t, "", []string{"oboard:read", "oboard:operate", "offline_access"})
	defer closeServer()

	server := &model.Server{Name: "metrics-edge", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline, ResourceHistoryEnabled: true, LatencyProbeEnabled: true}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		capability string
		arguments  map[string]any
		check      string
	}{
		{"servers.metrics.read", map[string]any{"server_id": server.ID}, "server_id"},
		{"servers.latency_probes.read", map[string]any{"server_id": server.ID, "limit": 10}, "server_id"},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: mcpCapabilityToolName(test.capability), Arguments: test.arguments})
		if err != nil {
			t.Fatalf("%s call: %v", test.capability, err)
		}
		if result.IsError {
			var text string
			for _, content := range result.Content {
				if item, ok := content.(*mcp.TextContent); ok {
					text += item.Text
				}
			}
			t.Fatalf("%s returned an error result: %s", test.capability, text)
		}
		var text string
		for _, content := range result.Content {
			if item, ok := content.(*mcp.TextContent); ok {
				text += item.Text
			}
		}
		if !strings.Contains(text, test.check) {
			t.Fatalf("%s output missing %q: %s", test.capability, test.check, text)
		}
	}

	query, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_task", Arguments: map[string]any{"goal": "查看服务器流量和连接数", "params": map[string]any{"server_id": server.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if query.IsError {
		var text string
		for _, content := range query.Content {
			if item, ok := content.(*mcp.TextContent); ok {
				text += item.Text
			}
		}
		t.Fatalf("oboard_task metrics query returned an error result: %s", text)
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
