package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
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

func testOAuthClient(t *testing.T, db *store.Store, id, name string, redirects []string) *model.OAuthClient {
	t.Helper()
	client := &model.OAuthClient{ID: id, Name: name, RedirectURIs: redirects, IdentityType: "preregistered", ClientMetadata: json.RawMessage(`{}`), Enabled: true}
	if err := db.CreateOAuthClient(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	return client
}

func testGrantRequest(accessLevel string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", nil)
	req.Form = url.Values{"server_mode": {"all"}, "user_mode": {"all"}, "auto_approve_risk": {"0"}, "allow_create_servers": {"1"}}
	if accessLevel == "" {
		accessLevel = "read"
	}
	req.Form.Set("access_level", accessLevel)
	return req
}

func createTestGrant(t *testing.T, server *Server, user model.User, client *model.OAuthClient, scopes []string) (*model.OAuthGrant, *model.APIPrincipal) {
	t.Helper()
	grant, principal, err := server.createOAuthGrantV2(testGrantRequest(""), user, *client, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return grant, principal
}

func issueTestMCPToken(t *testing.T, db *store.Store, grant *model.OAuthGrant, principal *model.APIPrincipal, client *model.OAuthClient, userID int64, resource, plain string) {
	t.Helper()
	access := &model.OAuthToken{TokenHash: security.HashAPISecret("test-secret", plain), GrantID: grant.ID, PrincipalID: principal.ID, ClientID: client.ID, UserID: userID, Resource: resource, ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.CreateOAuthTokens(context.Background(), access, nil); err != nil {
		t.Fatal(err)
	}
}

func newMCPTestEnvironment(t *testing.T, accessLevel string, scopes []string) (*store.Store, *Server, *mcp.ClientSession, *model.APIPrincipal, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	httpServer := httptest.NewServer(server.Handler())
	if err := db.SetSetting(context.Background(), "controller_url", httpServer.URL); err != nil {
		httpServer.Close()
		db.Close()
		t.Fatal(err)
	}
	request(t, server.Handler(), http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		httpServer.Close()
		db.Close()
		t.Fatal(err)
	}
	if accessLevel == "read" || accessLevel == "operator" {
		role := model.RoleViewer
		if accessLevel == "operator" {
			role = model.RoleOperator
		}
		member := &model.User{
			Username: string(role) + "-" + randomTestID(), PasswordHash: "unused", Role: role, Status: "active",
			ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "member-password", SubscriptionToken: "member-subscription",
		}
		if err := db.CreateUser(context.Background(), member); err != nil {
			httpServer.Close()
			db.Close()
			t.Fatal(err)
		}
		user = member
	}
	client := testOAuthClient(t, db, "oc_"+randomTestID(), "MCP test client", []string{"http://127.0.0.1/callback"})
	grant, principal := createTestGrant(t, server, *user, client, scopes)
	if accessLevel == "legacy-read-admin" {
		rawDB, openErr := sql.Open("sqlite", dbPath)
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, updateErr := rawDB.ExecContext(context.Background(), `update oauth_grants set access_level='read',resource_boundary_v2_json='{"version":1,"resources":{"server":{"selection":"none"}}}' where id=?`, grant.ID)
		_ = rawDB.Close()
		if updateErr != nil {
			t.Fatal(updateErr)
		}
	}
	plain := "oba_test-token-" + randomTestID()
	issueTestMCPToken(t, db, grant, principal, client, user.ID, httpServer.URL+"/api/v1/mcp", plain)
	httpClient := &http.Client{Transport: bearerTransport{token: plain, base: http.DefaultTransport}}
	sessionClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := sessionClient.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/api/v1/mcp", HTTPClient: httpClient}, nil)
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

func randomTestID() string {
	token, _ := security.RandomToken(8)
	return token
}

var finalToolNames = []string{
	"oboard_task", "oboard_commit_task",
	"oboard_discover", "oboard_get_capability_schema",
	"oboard_plan_desired_state", "oboard_validate_desired_state", "oboard_submit_changeset",
	"oboard_get_changeset", "oboard_get_workflow", "oboard_cancel_workflow", "oboard_retry_workflow_step", "oboard_redeem_external_action",
}

var readToolNames = []string{
	"oboard_task",
	"oboard_discover", "oboard_get_capability_schema",
	"oboard_plan_desired_state", "oboard_validate_desired_state",
	"oboard_get_changeset", "oboard_get_workflow",
}

func TestMCPOperateGrantListsOnlyFinalTools(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	if len(byName) < len(finalToolNames) {
		t.Fatalf("tools/list returned %d tools, want at least %d: %#v", len(byName), len(finalToolNames), tools.Tools)
	}
	for _, name := range finalToolNames {
		if _, ok := byName[name]; !ok {
			t.Fatalf("tools/list is missing %q", name)
		}
	}
	for _, capability := range []string{"servers.list", "users.create", "routing_rules.place"} {
		name := mcpCapabilityToolName(capability)
		if _, ok := byName[name]; !ok {
			t.Fatalf("tools/list is missing dynamic capability tool %q", name)
		}
	}
	for _, name := range []string{"server_exec", "server_exec_shell", "server_get_system_info"} {
		if _, ok := byName[name]; ok {
			t.Fatalf("operate tools/list leaked privileged tool %q", name)
		}
	}
	if alwaysLoad, ok := byName["oboard_task"].Meta["anthropic/alwaysLoad"].(bool); !ok || !alwaysLoad {
		t.Fatalf("oboard_task must advertise anthropic/alwaysLoad: %#v", byName["oboard_task"].Meta)
	}
	for name, tool := range byName {
		if name != "oboard_task" && tool.Meta != nil {
			if _, ok := tool.Meta["anthropic/alwaysLoad"]; ok {
				t.Fatalf("tool %q must not advertise anthropic/alwaysLoad", name)
			}
		}
	}
	for name, tool := range byName {
		if tool.Title == "" || tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("tool %q needs title, description, and input schema", name)
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
}

func TestMCPReadGrantDoesNotListOperateTools(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "read", []string{"oboard:read"})
	defer closeServer()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range readToolNames {
		if _, ok := byName[name]; !ok {
			t.Fatalf("read grant tools/list is missing %q", name)
		}
	}
	for _, name := range []string{"oboard_commit_task", "oboard_submit_changeset", "oboard_cancel_workflow", "oboard_retry_workflow_step", "oboard_redeem_external_action"} {
		if _, ok := byName[name]; ok {
			t.Fatalf("read grant must not list %q", name)
		}
	}
}

func TestMCPExistingReadGrantImmediatelyInheritsAdminRole(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "legacy-read-admin", []string{"oboard:read"})
	defer closeServer()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "oboard_commit_task" {
			return
		}
	}
	t.Fatal("existing persisted read grant did not inherit the admin user's operate permission")
}

func TestMCPRoleDowngradeTakesEffectWithoutReauthorization(t *testing.T) {
	db, _, session, principal, closeServer := newMCPTestEnvironment(t, "operator", []string{"oboard:read"})
	defer closeServer()

	hasTool := func(name string) bool {
		tools, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range tools.Tools {
			if tool.Name == name {
				return true
			}
		}
		return false
	}
	if !hasTool("oboard_commit_task") {
		t.Fatal("operator did not inherit operate tools")
	}
	if principal.OwnerUserID == nil {
		t.Fatal("OAuth principal is missing its owner")
	}
	user, err := db.GetUser(context.Background(), *principal.OwnerUserID)
	if err != nil {
		t.Fatal(err)
	}
	user.Role = model.RoleViewer
	if err := db.UpdateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if hasTool("oboard_commit_task") {
		t.Fatal("write tools remained available after the user was downgraded to viewer")
	}
	if !hasTool("oboard_task") {
		t.Fatal("read tools disappeared after the user was downgraded to viewer")
	}
}

func TestMCPDiscoverReturnsGrantSummary(t *testing.T) {
	db, _, session, principal, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_discover", Arguments: map[string]any{"include_denied": true, "detail_level": "full"}})
	if err != nil || result.IsError {
		t.Fatalf("discover error=%v result=%#v", err, result)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	body := string(encoded)
	for _, fragment := range []string{`"schema_version":"1"`, `"access_level":"operate"`, `"grant_id":"` + principal.OAuthGrantID, `"authorized_capabilities"`, `"workflow_rules"`, `"limits"`, `"recommended_actions"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("discover output missing %q: %s", fragment, body)
		}
	}
	audits, err := db.ListToolCallAudits(context.Background(), principal.ID, 10)
	if err != nil || len(audits) != 1 || audits[0].Capability != "discover" {
		t.Fatalf("discover audits = %#v err=%v", audits, err)
	}
}

func TestMCPGrantResourceRead(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	got, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "oboard://auth/grant"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Contents) != 1 {
		t.Fatalf("grant resource contents = %#v", got.Contents)
	}
	text := got.Contents[0].Text
	for _, fragment := range []string{`"schema_version":"1"`, `"access_level":"operate"`, `"resource_uri":"oboard://auth/grant"`, `"authorization"`, `"revision":"rev_`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("grant resource missing %q: %s", fragment, text)
		}
	}
}

func TestMCPAuthRejectsApiKeyAndSessionAndWrongAudience(t *testing.T) {
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
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_auth", "Auth test", []string{"http://127.0.0.1/callback"})
	grant, principal := createTestGrant(t, server, *user, client, []string{"oboard:read"})
	issueTestMCPToken(t, db, grant, principal, client, user.ID, "https://panel.example.com/api/v1/mcp", "oba_correct-audience")
	issueTestMCPToken(t, db, grant, principal, client, user.ID, "https://other.example.com/api/v1/mcp", "oba_wrong-audience")

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`
	for _, test := range []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "no token", wantStatus: http.StatusUnauthorized},
		{name: "api key", authorization: "Bearer obk_static-token", wantStatus: http.StatusUnauthorized},
		{name: "refresh token", authorization: "Bearer obr_refresh-token", wantStatus: http.StatusUnauthorized},
		{name: "bad token", authorization: "Bearer oba_bogus", wantStatus: http.StatusUnauthorized},
		{name: "wrong audience", authorization: "Bearer oba_wrong-audience", wantStatus: http.StatusUnauthorized},
		{name: "correct audience", authorization: "Bearer oba_correct-audience", wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://panel.example.com/api/v1/mcp", strings.NewReader(initialize))
			req.Host = "panel.example.com"
			if test.authorization != "" {
				req.Header.Set("Authorization", test.authorization)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != test.wantStatus {
				t.Fatalf("%s status=%d body=%s", test.name, response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusUnauthorized {
				if challenge := response.Header().Get("WWW-Authenticate"); !strings.Contains(challenge, "resource_metadata=") {
					t.Fatalf("%s missing WWW-Authenticate challenge", test.name)
				}
			}
		})
	}
}

func TestMCPAdminInheritsOperateWhenClientRequestsReadScope(t *testing.T) {
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
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_operate", "Operate client", []string{"https://client.example/callback"})

	grant, principal := createTestGrant(t, server, *user, client, []string{"oboard:read", "offline_access"})
	if grant.AccessLevel != "operate" {
		t.Fatalf("grant access level = %q, want operate", grant.AccessLevel)
	}
	if !grant.OfflineAccess {
		t.Fatal("grant did not record offline_access")
	}
	if !slices.Contains(principal.Scopes, "oboard:operate") {
		t.Fatalf("principal scopes = %#v, want oboard:operate", principal.Scopes)
	}
	var boundary mcpauth.ResourceBoundary
	if err := json.Unmarshal(grant.ResourceBoundaryJSON, &boundary); err != nil {
		t.Fatal(err)
	}
	if !boundary.AllowsCreate("server") {
		t.Fatal("boundary did not persist allow_create for servers")
	}
	// The client record must not carry a runtime permission ceiling.
	persisted, err := db.GetOAuthClient(context.Background(), client.ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedClient, _ := json.Marshal(persisted)
	if strings.Contains(string(encodedClient), "allowed_scopes") {
		t.Fatalf("client record still exposes allowed_scopes: %s", encodedClient)
	}
	if persisted.IdentityType != "preregistered" {
		t.Fatalf("client identity type = %q", persisted.IdentityType)
	}
	adminToken := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	listed := request(t, handler, http.MethodGet, "/api/v1/oauth-grants", adminToken, nil, http.StatusOK)
	items, _ := listed["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("grant list data = %#v", listed["data"])
	}
	item, _ := items[0].(map[string]any)
	if item["effective_role"] != "admin" || item["access_level"] != "operate" {
		t.Fatalf("grant list did not project the current user role: %#v", item)
	}

	request(t, handler, http.MethodDelete, "/api/v1/oauth-grants/"+grant.ID, adminToken, nil, http.StatusOK)
	listed = request(t, handler, http.MethodGet, "/api/v1/oauth-grants", adminToken, nil, http.StatusOK)
	items, _ = listed["data"].([]any)
	if len(items) != 0 {
		t.Fatalf("revoked grant remained in management list: %#v", listed["data"])
	}
	revoked, err := db.GetOAuthGrant(context.Background(), grant.ID)
	if err != nil || revoked.Status != model.OAuthGrantRevoked || revoked.RevokedAt == nil {
		t.Fatalf("revoked grant audit state was not retained: grant=%#v err=%v", revoked, err)
	}
}

func TestOAuthViewerInheritsReadWhenClientRequestsOperateScope(t *testing.T) {
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
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	admin := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, handler, http.MethodPost, "/api/v1/ui/users", admin, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewer := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)["token"].(string)
	client := testOAuthClient(t, db, "oc_viewer_operate", "Viewer operate", []string{"https://client.example/callback"})
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	form := url.Values{"client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "response_type": {"code"}, "scope": {"oboard:read oboard:operate"}, "state": {"s"}, "resource": {"https://panel.example.com/api/v1/mcp"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "decision": {"approve"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+viewer)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "授权成功") {
		t.Fatalf("viewer authorization status=%d body=%s", response.Code, response.Body.String())
	}
	grants, err := db.ListOAuthGrants(context.Background())
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants=%#v err=%v", grants, err)
	}
	if grants[0].AccessLevel != "read" {
		t.Fatalf("viewer grant access level=%q, want read", grants[0].AccessLevel)
	}
}

func TestOAuthRejectsUnknownCoarseScope(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	admin := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	client := testOAuthClient(t, db, "oc_scope", "Scope client", []string{"https://client.example/callback"})
	form := oauthTestAuthorizationForm(client, "topology:write", base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 43))[:43]))
	form.Set("decision", "approve")
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+admin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown scope status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOAuthMetadataRemovesRegistrationEndpoint(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	handler := newTestServer(db, "test-secret", "").Handler()

	auth := httptest.NewRecorder()
	handler.ServeHTTP(auth, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	var authMetadata map[string]any
	if err := json.Unmarshal(auth.Body.Bytes(), &authMetadata); err != nil {
		t.Fatal(err)
	}
	if _, ok := authMetadata["registration_endpoint"]; ok {
		t.Fatalf("authorization metadata still advertises registration_endpoint: %#v", authMetadata)
	}
	if supported, _ := authMetadata["client_id_metadata_document_supported"].(bool); !supported {
		t.Fatalf("authorization metadata does not advertise CIMD support: %#v", authMetadata)
	}
	scopes, _ := authMetadata["scopes_supported"].([]any)
	scopeSet := map[string]bool{}
	for _, scope := range scopes {
		scopeSet[scope.(string)] = true
	}
	for _, want := range []string{"oboard:read", "oboard:operate", "offline_access"} {
		if !scopeSet[want] {
			t.Fatalf("authorization metadata scopes missing %q: %#v", want, authMetadata)
		}
	}

	resource := httptest.NewRecorder()
	handler.ServeHTTP(resource, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/api/v1/mcp", nil))
	var resourceMetadata map[string]any
	if err := json.Unmarshal(resource.Body.Bytes(), &resourceMetadata); err != nil {
		t.Fatal(err)
	}
	resourceScopes, _ := resourceMetadata["scopes_supported"].([]any)
	resourceScopeSet := map[string]bool{}
	for _, scope := range resourceScopes {
		resourceScopeSet[scope.(string)] = true
	}
	for _, want := range []string{"oboard:read", "oboard:operate", "offline_access"} {
		if !resourceScopeSet[want] {
			t.Fatalf("protected resource metadata scopes missing %q: %#v", want, resourceMetadata)
		}
	}
}

func TestOAuthDynamicRegistrationIsRetired(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-secret", "").Handler()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"client_name":"Codex","redirect_uris":["http://127.0.0.1:8765/callback"],"scope":"inventory:read"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusGone {
		t.Fatalf("dynamic registration status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOAuthProtectedResourceChallengeRequestsDurableAccess(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com/hidden"); err != nil {
		t.Fatal(err)
	}
	handler := New(db, "test-secret", "", "/hidden", nil).Handler()
	req := httptest.NewRequest(http.MethodPost, "/hidden/api/v1/mcp", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	challenge := response.Header().Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="https://panel.example.com/.well-known/oauth-protected-resource/hidden/api/v1/mcp"`) || !strings.Contains(challenge, `scope="oboard:read oboard:operate offline_access"`) {
		t.Fatalf("challenge=%q", challenge)
	}
}

func TestMCPPlanValidateSubmitWorkflow(t *testing.T) {
	db, _, session, principal, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	planResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_plan_desired_state", Arguments: map[string]any{
		"capability_id": "servers.onboard",
		"goal":          "onboard a PH node",
		"desired_state": map[string]any{
			"server":                 map[string]any{"name": "PH", "region_code": "PH", "ip_stack": "auto", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 60000},
			"issue_enrollment_token": false,
		},
	}})
	if err != nil || planResult.IsError {
		t.Fatalf("plan error=%v result=%#v", err, planResult)
	}
	planJSON, _ := json.Marshal(planResult.StructuredContent)
	var planEnvelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(planJSON, &planEnvelope); err != nil {
		t.Fatal(err)
	}
	plan, ok := planEnvelope.Data["plan_id"].(string)
	if !ok || plan == "" {
		t.Fatalf("plan output missing plan_id: %s", planJSON)
	}
	planDigest, _ := planEnvelope.Data["plan_digest"].(string)
	if planDigest == "" {
		t.Fatalf("plan output missing plan_digest: %s", planJSON)
	}

	validateResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_validate_desired_state", Arguments: map[string]any{
		"plan": planEnvelope.Data, "plan_digest": planDigest,
	}})
	if err != nil || validateResult.IsError {
		t.Fatalf("validate error=%v result=%#v", err, validateResult)
	}
	validateJSON, _ := json.Marshal(validateResult.StructuredContent)
	if !strings.Contains(string(validateJSON), `"valid":true`) {
		t.Fatalf("validate did not succeed: %s", validateJSON)
	}

	submitResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oboard_submit_changeset", Arguments: map[string]any{
		"validated_plan":      planEnvelope.Data,
		"validation_digest":   planDigest,
		"idempotency_key":     "submit-flow-test-001",
		"reason":              "onboard PH from plan",
		"approval_preference": "use_preapproval_if_available",
	}})
	if err != nil || submitResult.IsError {
		t.Fatalf("submit error=%v result=%#v", err, submitResult)
	}
	submitJSON, _ := json.Marshal(submitResult.StructuredContent)
	body := string(submitJSON)
	for _, fragment := range []string{`"schema_version":"1"`, `"changeset_id":"chg_`, `"workflow_id":"wf_`, `"changeset_status":"succeeded"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("submit output missing %q: %s", fragment, body)
		}
	}

	changesets, err := db.ListAutomationChangesets(context.Background(), principal.ID, 10)
	if err != nil || len(changesets) != 1 {
		t.Fatalf("changesets=%#v err=%v", changesets, err)
	}
	if changesets[0].Status != model.ChangesetSucceeded {
		t.Fatalf("changeset status=%s, want succeeded", changesets[0].Status)
	}
}

func TestMCPScopeForGrantIncludesOperateCapabilities(t *testing.T) {
	_, server, _, principal, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	appPrincipal := application.Principal{ID: principal.ID, AccessLevel: mcpauth.AccessOperate, Role: model.RoleAdmin, Scopes: principal.Scopes}
	scopes := server.capabilities.ScopesForGrant(appPrincipal)
	if !slices.Contains(scopes, "servers:onboard") || !slices.Contains(scopes, "deployments:apply") {
		t.Fatalf("derived scopes missing write capabilities: %#v", scopes)
	}
}

func TestOAuthRefreshRotationAndReuseRevokesFamily(t *testing.T) {
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
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_reuse_v2", "Reuse v2", []string{"https://client.example/callback"})
	grant, principal := createTestGrant(t, server, *user, client, []string{"oboard:read", "oboard:operate", "offline_access"})

	now := time.Now().UTC()
	accessRaw, refreshRaw := "oba_reuse-v2-access", "obr_reuse-v2-refresh"
	access := &model.OAuthToken{TokenHash: security.HashAPISecret("test-secret", accessRaw), GrantID: grant.ID, PrincipalID: principal.ID, ClientID: client.ID, UserID: user.ID, Resource: "https://panel.example.com/api/v1/mcp", ExpiresAt: now.Add(time.Hour)}
	refresh := &model.OAuthToken{TokenHash: security.HashAPISecret("test-secret", refreshRaw), FamilyID: "family-v2", GrantID: grant.ID, PrincipalID: principal.ID, ClientID: client.ID, UserID: user.ID, Resource: "https://panel.example.com/api/v1/mcp", ExpiresAt: now.Add(time.Hour)}
	if err := db.CreateOAuthTokens(context.Background(), access, refresh); err != nil {
		t.Fatal(err)
	}

	rotate := func(refreshToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {client.ID}, "resource": {"https://panel.example.com/api/v1/mcp"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	rotated := rotate(refreshRaw)
	if rotated.Code != http.StatusOK {
		t.Fatalf("refresh rotation status=%d body=%s", rotated.Code, rotated.Body.String())
	}
	var rotatedTokens struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotatedTokens); err != nil {
		t.Fatal(err)
	}
	reuse := rotate(refreshRaw)
	if reuse.Code != http.StatusBadRequest || !strings.Contains(reuse.Body.String(), "reuse") {
		t.Fatalf("refresh reuse status=%d body=%s", reuse.Code, reuse.Body.String())
	}
	afterFamily := rotate(rotatedTokens.RefreshToken)
	if afterFamily.Code != http.StatusBadRequest {
		t.Fatalf("rotated token after family revocation status=%d body=%s", afterFamily.Code, afterFamily.Body.String())
	}
	if _, _, _, _, err := server.store.AuthenticateMCPAccessToken(context.Background(), security.HashAPISecret("test-secret", accessRaw), "https://panel.example.com/api/v1/mcp", time.Now().UTC()); err == nil {
		t.Fatal("access token remained valid after refresh reuse")
	}
}

func TestOAuthGrantRevocationInvalidatesTokens(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	request(t, server.Handler(), http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_revoke_v2", "Revoke v2", []string{"https://client.example/callback"})
	grant, principal := createTestGrant(t, server, *user, client, []string{"oboard:read", "offline_access"})
	issueTestMCPToken(t, db, grant, principal, client, user.ID, "https://panel.example.com/api/v1/mcp", "oba_revoke-v2-token")
	if err := db.RevokeOAuthGrant(context.Background(), grant.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := server.store.AuthenticateMCPAccessToken(context.Background(), security.HashAPISecret("test-secret", "oba_revoke-v2-token"), "https://panel.example.com/api/v1/mcp", time.Now().UTC()); err == nil {
		t.Fatal("revoked grant access token remained valid")
	}
}

func TestOAuthWithoutOfflineAccessDoesNotIssueRefreshToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	request(t, server.Handler(), http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_online_v2", "Online v2", []string{"https://client.example/callback"})
	grant, principal := createTestGrant(t, server, *user, client, []string{"oboard:read"})
	verifier := strings.Repeat("d", 43)
	sum := sha256.Sum256([]byte(verifier))
	code := &model.OAuthAuthorizationCode{CodeHash: security.HashAPISecret("test-secret", "online-v2-code"), GrantID: grant.ID, ClientID: client.ID, UserID: user.ID, PrincipalID: principal.ID, RedirectURI: client.RedirectURIs[0], Resource: "https://panel.example.com/api/v1/mcp", CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]), ExpiresAt: time.Now().Add(time.Minute)}
	if err := db.CreateOAuthAuthorizationCode(context.Background(), code); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"grant_type": {"authorization_code"}, "code": {"online-v2-code"}, "client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "code_verifier": {verifier}, "resource": {"https://panel.example.com/api/v1/mcp"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("online-only exchange status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["refresh_token"]; ok {
		t.Fatalf("online-only grant received refresh token: %s", response.Body.String())
	}
	if value, _ := payload["access_token"].(string); !strings.HasPrefix(value, "oba_") {
		t.Fatalf("online-only grant did not receive an access token: %s", response.Body.String())
	}
}

func TestOAuthAuthorizationCodePKCESingleUseAndCoarseScopes(t *testing.T) {
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
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	sessionToken := login["token"].(string)
	client := testOAuthClient(t, db, "oc_pkce_v2", "PKCE v2", []string{"https://client.example/callback"})
	verifier := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	form := url.Values{"client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "response_type": {"code"}, "scope": {"oboard:read offline_access"}, "state": {"state-test"}, "resource": {"https://panel.example.com/api/v1/mcp"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "decision": {"approve"}}
	authorize := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	authorize.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	authorize.Header.Set("Authorization", "Bearer "+sessionToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorize)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "授权成功") {
		t.Fatalf("authorize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	location, err := oauthSuccessRedirectURL(recorder.Body.String())
	if err != nil || location.Query().Get("code") == "" {
		t.Fatal(err)
	}
	code := location.Query().Get("code")
	exchange := func() *httptest.ResponseRecorder {
		values := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "code_verifier": {verifier}, "resource": {"https://panel.example.com/api/v1/mcp"}}
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	first := exchange()
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"access_token":"oba_`) || !strings.Contains(first.Body.String(), `"refresh_token":"obr_`) || !strings.Contains(first.Body.String(), `"scope":"oboard:read oboard:operate offline_access"`) {
		t.Fatalf("first exchange status=%d body=%s", first.Code, first.Body.String())
	}
	second := exchange()
	if second.Code != http.StatusBadRequest {
		t.Fatalf("second exchange status=%d body=%s", second.Code, second.Body.String())
	}
	grants, err := db.ListOAuthGrants(context.Background())
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants=%#v err=%v", grants, err)
	}
	if grants[0].AccessLevel != "operate" || !grants[0].OfflineAccess || grants[0].ConsentVersion != 2 {
		t.Fatalf("grant v2 fields missing: %#v", grants[0])
	}
	audits, err := db.ListAuditPage(context.Background(), 100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	auditJSON, _ := json.Marshal(audits)
	if strings.Contains(string(auditJSON), code) {
		t.Fatalf("OAuth audit contains the authorization code: %s", auditJSON)
	}
}

func TestMCPRejectsInvalidOriginHeader(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com/hidden"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBOARD_CORS_ORIGINS", "https://ui.example.com")
	server := New(db, "test-secret", "", "/hidden", nil)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request(t, server.Handler(), http.MethodPost, "/hidden/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_origin_v2", "Origin v2", []string{"http://127.0.0.1/callback"})
	grant, principal := createTestGrant(t, server, *user, client, []string{"oboard:read"})
	plain := "oba_origin-v2-token"
	issueTestMCPToken(t, db, grant, principal, client, user.ID, "https://panel.example.com/hidden/api/v1/mcp", plain)
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`
	for _, test := range []struct {
		name       string
		origin     string
		wantStatus int
	}{
		{name: "no origin", wantStatus: http.StatusOK},
		{name: "configured public origin", origin: "https://panel.example.com", wantStatus: http.StatusOK},
		{name: "configured CORS origin", origin: "https://ui.example.com", wantStatus: http.StatusOK},
		{name: "unknown host", origin: "https://evil.example.com", wantStatus: http.StatusForbidden},
		{name: "path in origin", origin: "https://panel.example.com/hidden", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/hidden/api/v1/mcp", strings.NewReader(initialize))
			if err != nil {
				t.Fatal(err)
			}
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			req.Header.Set("Authorization", "Bearer "+plain)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("Origin %q status=%d, want %d", test.origin, response.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestOAuthConsentPageRendersWithPreview(t *testing.T) {
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
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	sessionToken := login["token"].(string)
	client := testOAuthClient(t, db, "oc_consent", "Consent client", []string{"https://client.example/callback"})
	verifier := strings.Repeat("c", 43)
	sum := sha256.Sum256([]byte(verifier))
	form := oauthTestAuthorizationForm(client, "oboard:read oboard:operate", base64.RawURLEncoding.EncodeToString(sum[:]))
	get := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+form.Encode(), nil)
	get.Header.Set("Authorization", "Bearer "+sessionToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, get)
	if response.Code != http.StatusOK {
		t.Fatalf("consent status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, fragment := range []string{"继承当前账号权限", "继承管理员权限", "账号角色变更会立即生效", "Changeset", "Workflow", "实际能力预览", "servers.onboard"} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("consent page missing %q", fragment)
		}
	}
	for _, fragment := range []string{`name="access_level"`, `name="server_mode"`, `name="user_mode"`, `name="auto_approve_risk"`} {
		if strings.Contains(body, fragment) {
			t.Fatalf("consent page still contains secondary permission control %q", fragment)
		}
	}
}

func TestMCPServerInstructionsStatic(t *testing.T) {
	instructions := mcpServerInstructions
	for _, fragment := range []string{"oboard_task", "oboard_commit_task", "fallback_required", "Changeset", "Workflow", "one-time", "approval", "Never perform SSH", "Never request, reveal, persist, repeat, or log", "certificate_mode=auto", "Do not wait for a ready certificate"} {
		if !strings.Contains(instructions, fragment) {
			t.Fatalf("instructions missing %q", fragment)
		}
	}
	if strings.Index(instructions, "oboard_task") > strings.Index(instructions, "Treat every tool result") {
		t.Fatal("Fast Path guidance must precede general safety detail")
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
	return url.Values{"client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]}, "response_type": {"code"}, "scope": {scope}, "state": {"state-test"}, "resource": {"https://panel.example.com/api/v1/mcp"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
}
