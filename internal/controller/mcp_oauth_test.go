package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func newMCPTestEnvironment(t *testing.T, id string, scopes []string) (*store.Store, *Server, *mcp.ClientSession, *model.APIPrincipal, func()) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	principal := &model.APIPrincipal{ID: id, Name: "MCP test principal", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: scopes, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principal); err != nil {
		db.Close()
		t.Fatal(err)
	}
	plain := "obk_mcp-test-token-value"
	if err := db.CreateAPIToken(context.Background(), &model.APIToken{ID: "tok_" + id, PrincipalID: principal.ID, TokenHash: security.HashAPISecret("test-secret", plain), Prefix: "obk_mcptest", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(server.Handler())
	httpClient := &http.Client{Transport: bearerTransport{token: plain, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		httpServer.Close()
		db.Close()
		t.Fatal(err)
	}
	closeServer := func() {
		session.Close()
		httpServer.Close()
		db.Close()
	}
	return db, server, session, principal, closeServer
}

func TestMCPExposesCompleteToolAndResourceSurface(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "prn_mcp_surface", []string{
		"inventory:read", "topology:read", "servers:read", "servers:plan", "proxy_paths:plan",
		"deployments:validate", "audit:read", "audit:analyze", "servers:onboard",
	})
	defer closeServer()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantTools := []string{
		"oboard_list_capabilities", "oboard_query",
		"oboard_plan_server_onboarding", "oboard_plan_proxy_path", "oboard_plan_deployment", "oboard_plan_incident_response",
		"oboard_create_changeset", "oboard_validate_changeset", "oboard_apply_changeset", "oboard_get_operation", "oboard_list_changesets",
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	if len(byName) != len(wantTools) {
		t.Fatalf("tools/list returned %d tools, want %d: %#v", len(byName), len(wantTools), tools.Tools)
	}
	for _, name := range wantTools {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("tools/list is missing %q", name)
		}
		if tool.Title == "" || tool.Description == "" {
			t.Errorf("tool %q needs a title and description", name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %q needs an output schema", name)
		}
		schemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(schemaJSON, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" {
			t.Errorf("tool %q input schema is not an object: %s", name, schemaJSON)
		}
	}
	wantPlanProperties := map[string][]string{
		"oboard_plan_server_onboarding": {"name", "region_code", "ip_stack"},
		"oboard_plan_proxy_path":        {"entry_server_id", "exit_region", "preferred_relay_regions", "max_hops"},
		"oboard_plan_deployment":        {"server_ids", "reason"},
		"oboard_plan_incident_response": {"incident_id", "user_id", "rule_score", "anomaly_score"},
	}
	for name, want := range wantPlanProperties {
		schemaJSON, err := json.Marshal(byName[name].InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(schemaJSON, &schema); err != nil {
			t.Fatal(err)
		}
		for _, property := range want {
			if _, ok := schema.Properties[property]; !ok {
				t.Errorf("tool %q schema is missing property %q: %s", name, property, schemaJSON)
			}
		}
	}

	resources, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantResources := map[string]string{
		"oboard://inventory/summary": "Inventory summary",
		"oboard://topology/current":  "Current topology",
		"oboard://docs/capabilities": "Capability catalog",
		"oboard://docs/guide":        "MCP guide",
	}
	byURI := map[string]*mcp.Resource{}
	for _, resource := range resources.Resources {
		byURI[resource.URI] = resource
	}
	if len(byURI) != len(wantResources) {
		t.Fatalf("resources/list returned %d resources, want %d", len(byURI), len(wantResources))
	}
	for uri, name := range wantResources {
		resource, ok := byURI[uri]
		if !ok {
			t.Fatalf("resources/list is missing %q", uri)
		}
		if resource.Name != name || resource.Description == "" {
			t.Errorf("resource %q needs name and description: %#v", uri, resource)
		}
	}

	templates, err := session.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantTemplates := map[string]string{
		"oboard://servers/{id}":         "Server by ID",
		"oboard://changesets/{id}":      "Changeset by ID",
		"oboard://audit/incidents/{id}": "Audit incident by ID",
	}
	if len(templates.ResourceTemplates) != len(wantTemplates) {
		t.Fatalf("resources/templates/list returned %d templates, want %d", len(templates.ResourceTemplates), len(wantTemplates))
	}
	for _, template := range templates.ResourceTemplates {
		if want, ok := wantTemplates[template.URITemplate]; !ok || template.Name != want {
			t.Errorf("unexpected resource template: %#v", template)
		}
	}

	guide, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "oboard://docs/guide"})
	if err != nil {
		t.Fatal(err)
	}
	if len(guide.Contents) != 1 || !strings.Contains(guide.Contents[0].Text, `"oboard_create_changeset"`) {
		t.Fatalf("docs/guide resource is unusable: %#v", guide)
	}

	query, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_query", Arguments: map[string]any{"capability": "servers.list", "arguments": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if query.IsError || query.StructuredContent == nil {
		t.Fatalf("oboard_query failed: %#v", query.Content)
	}
	queryJSON, err := json.Marshal(query.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var queryList []any
	if err := json.Unmarshal(queryJSON, &queryList); err != nil {
		t.Fatalf("oboard_query servers.list should return an array, got: %s", queryJSON)
	}
}

func TestMCPPlanToChangesetWorkflow(t *testing.T) {
	db, _, session, principal, closeServer := newMCPTestEnvironment(t, "prn_mcp_workflow", []string{"servers:plan", "servers:onboard"})
	defer closeServer()

	plan, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_plan_server_onboarding", Arguments: map[string]any{"name": "PH", "region_code": "PH", "ip_stack": "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsError || plan.StructuredContent == nil {
		t.Fatalf("plan tool failed: %#v", plan.Content)
	}
	var planOut struct {
		Valid              bool `json:"valid"`
		SuggestedChangeset struct {
			BaseRevisions map[string]any `json:"base_revisions"`
			Operation     struct {
				Capability string         `json:"capability"`
				Input      map[string]any `json:"input"`
			} `json:"operation"`
		} `json:"suggested_changeset"`
	}
	planJSON, err := json.Marshal(plan.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(planJSON, &planOut); err != nil {
		t.Fatal(err)
	}
	if !planOut.Valid || planOut.SuggestedChangeset.Operation.Capability != "servers.onboard" || planOut.SuggestedChangeset.Operation.Input == nil {
		t.Fatalf("unexpected plan output: %s", plan.StructuredContent)
	}

	createArguments := map[string]any{
		"reason":          "onboard PH from plan",
		"idempotency_key": "plan-workflow-ph",
		"base_revisions":  planOut.SuggestedChangeset.BaseRevisions,
		"operations": []any{map[string]any{
			"capability":    planOut.SuggestedChangeset.Operation.Capability,
			"input":         planOut.SuggestedChangeset.Operation.Input,
			"resource_refs": map[string]any{},
		}},
	}
	created, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_create_changeset", Arguments: createArguments})
	if err != nil {
		t.Fatal(err)
	}
	if created.IsError || created.StructuredContent == nil {
		t.Fatalf("create changeset failed: %#v", created.Content)
	}
	var changesetOut struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		Operations []struct {
			ID string `json:"id"`
		} `json:"operations"`
	}
	createdJSON, err := json.Marshal(created.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(createdJSON, &changesetOut); err != nil {
		t.Fatal(err)
	}
	if changesetOut.ID == "" || changesetOut.Status != "draft" || len(changesetOut.Operations) != 1 {
		t.Fatalf("unexpected changeset output: %s", created.StructuredContent)
	}

	retry, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_create_changeset", Arguments: createArguments})
	if err != nil {
		t.Fatal(err)
	}
	if retry.IsError {
		t.Fatalf("idempotent retry failed: %#v", retry.Content)
	}
	var retryOut struct {
		ID string `json:"id"`
	}
	retryJSON, err := json.Marshal(retry.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(retryJSON, &retryOut); err != nil {
		t.Fatal(err)
	}
	if retryOut.ID != changesetOut.ID {
		t.Fatalf("idempotent retry returned a different Changeset: %s vs %s", retryOut.ID, changesetOut.ID)
	}

	validated, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_validate_changeset", Arguments: map[string]any{"changeset_id": changesetOut.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if validated.IsError {
		t.Fatalf("validate changeset failed: %#v", validated.Content)
	}
	var validatedOut struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		PlanHash string `json:"plan_hash"`
	}
	validatedJSON, err := json.Marshal(validated.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(validatedJSON, &validatedOut); err != nil {
		t.Fatal(err)
	}
	if validatedOut.ID != changesetOut.ID || validatedOut.Status != "awaiting_approval" || validatedOut.PlanHash == "" {
		t.Fatalf("unexpected validation output: %s", validated.StructuredContent)
	}

	operation, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_get_operation", Arguments: map[string]any{"changeset_id": changesetOut.ID, "operation_id": changesetOut.Operations[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if operation.IsError {
		t.Fatalf("get operation failed: %#v", operation.Content)
	}
	var operationOut struct {
		Capability string `json:"capability"`
	}
	operationJSON, err := json.Marshal(operation.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(operationJSON, &operationOut); err != nil {
		t.Fatal(err)
	}
	if operationOut.Capability != "servers.onboard" {
		t.Fatalf("unexpected operation output: %s", operation.StructuredContent)
	}

	listed, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_list_changesets", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if listed.IsError {
		t.Fatalf("list changesets failed: %#v", listed.Content)
	}
	var listedOut []struct {
		ID string `json:"id"`
	}
	listedJSON, err := json.Marshal(listed.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(listedJSON, &listedOut); err != nil {
		t.Fatal(err)
	}
	if len(listedOut) != 1 || listedOut[0].ID != changesetOut.ID {
		t.Fatalf("unexpected changeset list: %s", listed.StructuredContent)
	}
	audits, err := db.ListToolCallAudits(context.Background(), principal.ID, 20)
	if err != nil || len(audits) < 4 {
		t.Fatalf("expected tool audits, got %#v err=%v", audits, err)
	}
}

func TestMCPResourceTemplates(t *testing.T) {
	db, server, session, principal, closeServer := newMCPTestEnvironment(t, "prn_mcp_resources", []string{"inventory:read", "servers:read", "servers:onboard"})
	defer closeServer()

	node := &model.Server{Name: "PH", Status: model.ServerOnline}
	if err := db.CreateServer(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	serverURI := "oboard://servers/" + strconv.FormatInt(node.ID, 10)
	got, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: serverURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Contents) != 1 || !strings.Contains(got.Contents[0].Text, `"name":"PH"`) {
		t.Fatalf("server resource read failed: %#v", got)
	}

	summary, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "oboard://inventory/summary"})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Contents) != 1 || !strings.Contains(summary.Contents[0].Text, `"PH"`) {
		t.Fatalf("inventory resource read failed: %#v", summary)
	}

	appPrincipal := application.Principal{ID: principal.ID, Type: principal.Type, Scopes: principal.Scopes, ResourceFilter: principal.ResourceFilter, SourceIP: netip.MustParseAddr("127.0.0.1"), ClientName: "test"}
	changeset, err := server.automation.Create(context.Background(), appPrincipal, automation.CreateRequest{
		IdempotencyKey: "resource-changeset",
		Operations:     []automation.OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(`{"server":{"name":"PH"},"issue_enrollment_token":false}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	changesetResource, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "oboard://changesets/" + changeset.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(changesetResource.Contents) != 1 || !strings.Contains(changesetResource.Contents[0].Text, `"id":"`+changeset.ID+`"`) {
		t.Fatalf("changeset resource read failed: %#v", changesetResource)
	}

	_, _, deniedSession, _, closeDenied := newMCPTestEnvironment(t, "prn_mcp_denied", []string{"inventory:read"})
	defer closeDenied()
	if _, err := deniedSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: serverURI}); err == nil {
		t.Fatal("expected server resource read to be denied without servers:read")
	}
}

func TestMCPUsesServicePrincipalAndRecordsAudit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := &model.APIPrincipal{ID: "prn_test", Name: "test MCP", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"inventory:read"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	plain := "obk_test-token-value"
	if err := db.CreateAPIToken(context.Background(), &model.APIToken{ID: "tok_test", PrincipalID: principal.ID, TokenHash: security.HashAPISecret("test-secret", plain), Prefix: "obk_test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(newTestServer(db, "test-secret", "").Handler())
	defer httpServer.Close()
	httpClient := &http.Client{Transport: bearerTransport{token: plain, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_query", Arguments: map[string]any{"capability": "inventory.read", "arguments": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent == nil {
		t.Fatalf("unexpected MCP result: %#v", result)
	}
	audits, err := db.ListToolCallAudits(context.Background(), principal.ID, 10)
	if err != nil || len(audits) != 1 || audits[0].Capability != "inventory.read" {
		t.Fatalf("tool audits = %#v, err=%v", audits, err)
	}
}

func TestMCPCreateChangesetAcceptsObjectInput(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := &model.APIPrincipal{ID: "prn_changeset", Name: "changeset MCP", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	plain := "obk_changeset-token-value"
	if err := db.CreateAPIToken(context.Background(), &model.APIToken{ID: "tok_changeset", PrincipalID: principal.ID, TokenHash: security.HashAPISecret("test-secret", plain), Prefix: "obk_changeset", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(newTestServer(db, "test-secret", "").Handler())
	defer httpServer.Close()
	httpClient := &http.Client{Transport: bearerTransport{token: plain, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var createTool *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "oboard_create_changeset" {
			createTool = tool
			break
		}
	}
	if createTool == nil {
		t.Fatal("oboard_create_changeset is missing from tools/list")
	}
	schemaJSON, err := json.Marshal(createTool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Operations struct {
				Items struct {
					Properties struct {
						Input struct {
							Type string `json:"type"`
						} `json:"input"`
						ResourceRefs struct {
							Type string `json:"type"`
						} `json:"resource_refs"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"operations"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties.Operations.Items.Properties.Input.Type != "object" || schema.Properties.Operations.Items.Properties.ResourceRefs.Type != "object" {
		t.Fatalf("changeset operation schema does not require objects: %s", schemaJSON)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_create_changeset", Arguments: map[string]any{
		"reason":          "onboard PH node",
		"idempotency_key": "mcp-onboard-ph",
		"operations": []any{map[string]any{
			"capability": "servers.onboard",
			"input": map[string]any{
				"server":                 map[string]any{"name": "PH", "region_code": "PH", "ip_stack": "auto", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 60000},
				"issue_enrollment_token": true,
			},
			"resource_refs": map[string]any{},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("object changeset input was rejected: %#v", result.Content)
	}
	changeset, err := db.FindAutomationChangesetByIdempotency(context.Background(), principal.ID, "mcp-onboard-ph")
	if err != nil {
		t.Fatal(err)
	}
	if len(changeset.Operations) != 1 {
		t.Fatalf("operations = %#v", changeset.Operations)
	}
	var operationInput map[string]any
	if err := json.Unmarshal(changeset.Operations[0].Input, &operationInput); err != nil {
		t.Fatal(err)
	}
	if _, ok := operationInput["server"].(map[string]any); !ok {
		t.Fatalf("persisted input is not an object: %s", changeset.Operations[0].Input)
	}
}

func TestMCPAcceptsConfiguredPublicHostBehindLoopbackProxy(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://ob.example.com/qzq"); err != nil {
		t.Fatal(err)
	}
	principal := &model.APIPrincipal{ID: "prn_proxy_mcp", Name: "proxy MCP", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"inventory:read"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	plain := "obk_proxy-mcp-token"
	if err := db.CreateAPIToken(context.Background(), &model.APIToken{ID: "tok_proxy_mcp", PrincipalID: principal.ID, TokenHash: security.HashAPISecret("test-secret", plain), Prefix: "obk_proxy", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer(New(db, "test-secret", "", "/qzq", nil).Handler())
	defer httpServer.Close()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`
	for _, test := range []struct {
		host       string
		wantStatus int
	}{
		{host: "ob.example.com", wantStatus: http.StatusOK},
		{host: "OB.EXAMPLE.COM:443", wantStatus: http.StatusOK},
		{host: "ob.example.com.", wantStatus: http.StatusOK},
		{host: "other.example.com", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.host, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/qzq/mcp", strings.NewReader(initialize))
			if err != nil {
				t.Fatal(err)
			}
			req.Host = test.host
			req.Header.Set("Authorization", "Bearer "+plain)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("Host %q status=%d, want %d", test.host, response.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestMCPAuthenticationChallengeUsesConfiguredBasePath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com/hidden"); err != nil {
		t.Fatal(err)
	}
	handler := New(db, "test-secret", "", "/hidden", nil).Handler()
	for _, authorization := range []string{"", "Bearer oba_invalid"} {
		req := httptest.NewRequest(http.MethodPost, "/hidden/mcp", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization=%q status=%d body=%s", authorization, response.Code, response.Body.String())
		}
		want := `resource_metadata="https://panel.example.com/.well-known/oauth-protected-resource/hidden/mcp"`
		if challenge := response.Header().Get("WWW-Authenticate"); !strings.Contains(challenge, want) {
			t.Fatalf("authorization=%q challenge=%q, want %q", authorization, challenge, want)
		}
	}
}

func TestOAuthWellKnownMetadataUsesRFCPathsWithBasePath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com/hidden"); err != nil {
		t.Fatal(err)
	}
	handler := New(db, "test-secret", "", "/hidden", nil).Handler()

	authorization := httptest.NewRecorder()
	handler.ServeHTTP(authorization, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server/hidden", nil))
	if authorization.Code != http.StatusOK {
		t.Fatalf("authorization metadata status=%d body=%s", authorization.Code, authorization.Body.String())
	}
	var authorizationMetadata map[string]any
	if err := json.Unmarshal(authorization.Body.Bytes(), &authorizationMetadata); err != nil {
		t.Fatal(err)
	}
	if authorizationMetadata["issuer"] != "https://panel.example.com/hidden" {
		t.Fatalf("authorization metadata=%#v", authorizationMetadata)
	}

	resource := httptest.NewRecorder()
	handler.ServeHTTP(resource, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/hidden/mcp", nil))
	if resource.Code != http.StatusOK {
		t.Fatalf("protected resource metadata status=%d body=%s", resource.Code, resource.Body.String())
	}
	var resourceMetadata map[string]any
	if err := json.Unmarshal(resource.Body.Bytes(), &resourceMetadata); err != nil {
		t.Fatal(err)
	}
	if resourceMetadata["resource"] != "https://panel.example.com/hidden/mcp" {
		t.Fatalf("protected resource metadata=%#v", resourceMetadata)
	}

	for _, path := range []string{
		"/.well-known/oauth-authorization-server/hidden/extra",
		"/.well-known/oauth-protected-resource/hidden",
		"/healthz",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("outside base path %s status=%d, want 404", path, response.Code)
		}
	}
}

func TestOAuthDynamicRegistrationIsPublicAndBounded(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-secret", "").Handler()
	register := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	first := register(`{"client_name":"Codex","redirect_uris":["http://127.0.0.1:8765/callback"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"software_id":"codex"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("registration status=%d body=%s", first.Code, first.Body.String())
	}
	var registration map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	clientID, _ := registration["client_id"].(string)
	client, err := db.GetOAuthClient(context.Background(), clientID)
	if err != nil || len(client.AllowedScopes) == 0 || !strings.Contains(string(client.ClientMetadata), `"registration":"dynamic"`) {
		t.Fatalf("dynamic client=%#v err=%v", client, err)
	}
	invalid := register(`{"client_name":"Secret client","redirect_uris":["http://localhost:8765/callback"],"token_endpoint_auth_method":"client_secret_post"}`)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "invalid_client_metadata") {
		t.Fatalf("invalid registration status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	for index := 0; index < 8; index++ {
		response := register(`{"client_name":"Claude Code","redirect_uris":["http://localhost:8765/callback"]}`)
		if response.Code != http.StatusCreated {
			t.Fatalf("registration %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	limited := register(`{"client_name":"Limited","redirect_uris":["http://localhost:8765/callback"]}`)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited registration status=%d body=%s", limited.Code, limited.Body.String())
	}
}

func TestOAuthAuthorizationCodePKCEAndSingleUse(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	sessionToken := login["token"].(string)
	client := &model.OAuthClient{ID: "oc_test", Name: "Codex", RedirectURIs: []string{"https://client.example/callback"}, AllowedScopes: []string{"inventory:read"}, ClientMetadata: json.RawMessage(`{}`), Enabled: true}
	if err := db.CreateOAuthClient(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	form := url.Values{"client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "response_type": {"code"}, "scope": {"inventory:read"}, "state": {"state-test"}, "resource": {"https://panel.example.com/mcp"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "decision": {"approve"}}
	authorize := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	authorize.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	authorize.Header.Set("Authorization", "Bearer "+sessionToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorize)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "授权成功") {
		t.Fatalf("authorize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	location, err := oauthSuccessRedirectURL(recorder.Body.String())
	if err != nil || location.Query().Get("state") != "state-test" || location.Query().Get("code") == "" {
		t.Fatalf("authorization redirect in success page=%q err=%v", recorder.Body.String(), err)
	}
	code := location.Query().Get("code")
	exchange := func() *httptest.ResponseRecorder {
		values := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "code_verifier": {verifier}, "resource": {"https://panel.example.com/mcp"}}
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	first := exchange()
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"access_token":"oba_`) || !strings.Contains(first.Body.String(), `"refresh_token":"obr_`) {
		t.Fatalf("first exchange status=%d body=%s", first.Code, first.Body.String())
	}
	second := exchange()
	if second.Code != http.StatusBadRequest || !strings.Contains(second.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("second exchange status=%d body=%s", second.Code, second.Body.String())
	}
	var firstTokens struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstTokens); err != nil {
		t.Fatal(err)
	}
	rotate := httptest.NewRecorder()
	rotateReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {firstTokens.RefreshToken}, "client_id": {client.ID}, "resource": {"https://panel.example.com/mcp"}}.Encode()))
	rotateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(rotate, rotateReq)
	if rotate.Code != http.StatusOK || !strings.Contains(rotate.Body.String(), `"access_token":"oba_`) {
		t.Fatalf("refresh rotation status=%d body=%s", rotate.Code, rotate.Body.String())
	}
	var rotated struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rotate.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	request(t, handler, http.MethodDelete, "/api/v2/oauth-clients/"+client.ID, sessionToken, nil, http.StatusOK)
	if _, err := db.GetOAuthClient(context.Background(), client.ID); err == nil {
		t.Fatal("oauth client delete did not remove the client")
	}
	revoked := httptest.NewRecorder()
	revokedReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rotated.RefreshToken}, "client_id": {client.ID}, "resource": {"https://panel.example.com/mcp"}}.Encode()))
	revokedReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(revoked, revokedReq)
	if revoked.Code != http.StatusBadRequest || !strings.Contains(revoked.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("refresh after client delete status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestOAuthScopesCannotExceedCurrentUserRole(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	admin := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, handler, http.MethodPost, "/api/v2/ui/users", admin, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewer := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)["token"].(string)
	client := &model.OAuthClient{ID: "oc_privileged", Name: "Privileged Client", RedirectURIs: []string{"https://client.example/callback"}, AllowedScopes: []string{"deployments:apply"}, ClientMetadata: json.RawMessage(`{}`), Enabled: true}
	if err := db.CreateOAuthClient(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("b", 43)
	sum := sha256.Sum256([]byte(verifier))
	form := oauthTestAuthorizationForm(client, "deployments:apply", base64.RawURLEncoding.EncodeToString(sum[:]))
	form.Set("decision", "approve")
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+viewer)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"error":"access_denied"`) {
		t.Fatalf("viewer OAuth grant status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOAuthClientScopeReductionInvalidatesAccessToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	request(t, server.Handler(), http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := &model.OAuthClient{ID: "oc_scope_reduction", Name: "Scope Reduction", RedirectURIs: []string{"https://client.example/callback"}, AllowedScopes: []string{"inventory:read"}, ClientMetadata: json.RawMessage(`{}`), Enabled: true}
	if err := db.CreateOAuthClient(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	principal, err := server.oauthPrincipal(httptest.NewRequest(http.MethodPost, "/oauth/authorize", nil), *user, *client, client.AllowedScopes)
	if err != nil {
		t.Fatal(err)
	}
	raw := "oba_scope-reduction-token"
	now := time.Now().UTC()
	access := &model.OAuthToken{TokenHash: security.HashAPISecret("test-secret", raw), PrincipalID: principal.ID, ClientID: client.ID, UserID: user.ID, Scopes: []string{"inventory:read"}, Resource: "https://panel.example.com/mcp", ExpiresAt: now.Add(time.Hour)}
	refresh := &model.OAuthToken{TokenHash: security.HashAPISecret("test-secret", "obr_scope-reduction-token"), FamilyID: "family", PrincipalID: principal.ID, ClientID: client.ID, UserID: user.ID, Scopes: access.Scopes, Resource: access.Resource, ExpiresAt: now.Add(time.Hour)}
	if err := db.CreateOAuthTokens(context.Background(), access, refresh); err != nil {
		t.Fatal(err)
	}
	if _, err := server.authenticateOAuthToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), raw); err != nil {
		t.Fatalf("valid access token rejected: %v", err)
	}
	client.AllowedScopes = []string{"audit:read"}
	if err := db.UpdateOAuthClient(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if _, err := server.authenticateOAuthToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), raw); err == nil {
		t.Fatal("access token survived removal of its scope from the OAuth client")
	}
}

func TestOAuthAuthorizationDoesNotReenableDisabledPrincipal(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	request(t, server.Handler(), http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := model.OAuthClient{ID: "oc_disabled_principal", Name: "Disabled Principal"}
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", nil)
	principal, err := server.oauthPrincipal(req, *user, client, []string{"inventory:read"})
	if err != nil {
		t.Fatal(err)
	}
	principal.Enabled = false
	if err := db.UpdateAPIPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	principal, err = server.oauthPrincipal(req, *user, client, []string{"inventory:read"})
	if err != nil {
		t.Fatal(err)
	}
	if principal.Enabled {
		t.Fatal("new authorization re-enabled a disabled OAuth principal")
	}
}

func TestOAuthConsentSupportsCookieSessionFormCSRF(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v2/ui/auth/login", strings.NewReader(`{"username":"admin","password":"very-secure-password"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d cookies=%#v", loginResponse.Code, loginResponse.Result().Cookies())
	}
	cookie := loginResponse.Result().Cookies()[0]
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("OAuth browser session cookie SameSite=%v", cookie.SameSite)
	}
	client := &model.OAuthClient{ID: "oc_browser", Name: "Browser Client", RedirectURIs: []string{"https://client.example/callback"}, AllowedScopes: []string{"inventory:read"}, ClientMetadata: json.RawMessage(`{}`), Enabled: true}
	if err := db.CreateOAuthClient(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("c", 43)
	sum := sha256.Sum256([]byte(verifier))
	form := oauthTestAuthorizationForm(client, "inventory:read", base64.RawURLEncoding.EncodeToString(sum[:]))
	get := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+form.Encode(), nil)
	get.AddCookie(cookie)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `_oboard_csrf`) {
		t.Fatalf("consent status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	form.Set("decision", "approve")
	form.Set("_oboard_csrf", server.csrfTokenForSession(cookie.Value))
	post := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusOK || !strings.Contains(postResponse.Body.String(), "授权成功") {
		t.Fatalf("consent submit status=%d body=%s", postResponse.Code, postResponse.Body.String())
	}
}

var oauthSuccessRedirectPattern = regexp.MustCompile(`content="1\.2; url=([^"]+)"`)

func oauthSuccessRedirectURL(body string) (*url.URL, error) {
	match := oauthSuccessRedirectPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		return nil, errors.New("success page does not contain the auto-redirect URL")
	}
	raw := strings.ReplaceAll(match[1], "&amp;", "&")
	return url.Parse(raw)
}

func oauthTestAuthorizationForm(client *model.OAuthClient, scope, challenge string) url.Values {
	return url.Values{"client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "response_type": {"code"}, "scope": {scope}, "state": {"state-test"}, "resource": {"https://panel.example.com/mcp"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
}
