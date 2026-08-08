package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestTopologyWriteRejectsInvalidStepBeforeMutation(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "entry", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	input, _ := json.Marshal(map[string]any{
		"path":  map[string]any{"kind": "chain", "name_mode": "auto", "name_template": []any{}, "inbound_id": inbound.ID, "exit_region_mode": "auto", "enabled": true},
		"steps": []map[string]any{{"node_type": "server_inbound", "transport_mode": "singbox", "server_id": node.ID}},
	})
	revisions, err := server.topologyWriteRevisions(ctx, principal, topologyWriteOperation{Path: model.ProxyPath{InboundID: inbound.ID}, Steps: []model.ProxyPathStep{{ServerID: &node.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(revisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{
		IdempotencyKey: "invalid-loop", BaseRevisions: base,
		Operations: []automation.OperationRequest{{Capability: "topology.write", Input: input}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err == nil {
		t.Fatal("looping topology operation passed dry-run validation")
	}
	paths, err := db.ListProxyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("rejected topology validation left persisted paths: %#v", paths)
	}
}

func TestProxyPathPlannerProducesValidTopologyChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	entry := &model.Server{Name: "entry", RegionMode: "manual", RegionCode: "JP", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	exit := &model.Server{Name: "exit", RegionMode: "manual", RegionCode: "US", PublicIPv4: "1.1.1.1", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, exit); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	plan, err := server.application.PlanProxyPath(ctx, principal, json.RawMessage(`{"entry_server_id":1,"exit_region":"US","max_hops":3}`))
	if err != nil || !plan.Valid || len(plan.Candidates) == 0 {
		t.Fatalf("proxy path plan=%#v err=%v", plan, err)
	}
	rawSuggested, _ := json.Marshal(plan.SuggestedChangeset)
	var suggested struct {
		BaseRevisions json.RawMessage             `json:"base_revisions"`
		Operation     automation.OperationRequest `json:"operation"`
	}
	if err := json.Unmarshal(rawSuggested, &suggested); err != nil {
		t.Fatal(err)
	}
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{
		IdempotencyKey: "planned-path", BaseRevisions: suggested.BaseRevisions, Operations: []automation.OperationRequest{suggested.Operation},
	})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := server.automation.Validate(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatalf("planned topology validation failed: %v", err)
	}
	if validated.Status != model.ChangesetAwaitingApproval {
		t.Fatalf("planned topology validation status=%v err=%v evidence=%s", validated.Status, err, validated.Validation)
	}
}

func TestDeploymentOperationReturnsOnlyPublicSummary(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "deploy", AgentID: "agent-test", AgentTokenHash: security.HashSecret("agent-secret"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	result, err := server.runDeploymentOperation(ctx, principal, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	text := string(encoded)
	for _, forbidden := range []string{"payload_json", "nonce", "signature", "agent-secret", "chain_secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("deployment result exposed %q: %s", forbidden, text)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["config_version"] == nil || fields["task_ids"] == nil || fields["summary"] == nil {
		t.Fatalf("unexpected deployment result: %#v", fields)
	}
}

func TestServerOnboardingEnrollmentTokenIsReturnedOnlyOnce(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	plan, err := server.application.PlanServerOnboarding(ctx, principal, json.RawMessage(`{"name":"new-node","region_code":"JP","ip_stack":"auto"}`))
	if err != nil {
		t.Fatal(err)
	}
	rawSuggested, _ := json.Marshal(plan.SuggestedChangeset)
	var suggested struct {
		BaseRevisions json.RawMessage             `json:"base_revisions"`
		Operation     automation.OperationRequest `json:"operation"`
	}
	if err := json.Unmarshal(rawSuggested, &suggested); err != nil {
		t.Fatal(err)
	}
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "onboard-once", BaseRevisions: suggested.BaseRevisions, Operations: []automation.OperationRequest{suggested.Operation}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	applied, err := server.automation.Apply(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied.Result), `"enrollment_token"`) {
		t.Fatalf("initial apply response omitted enrollment token: %s", applied.Result)
	}
	persisted, err := server.automation.Get(ctx, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(persisted)
	if strings.Contains(string(encoded), `"enrollment_token":`) {
		t.Fatalf("persisted Changeset retained one-time token: %s", encoded)
	}
}

func TestAutomationAdminCanDisableAndRotateCredentials(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	request(t, handler, http.MethodPost, "/api/v2/api-principals", token, map[string]any{
		"name": "Future grant", "scopes": []string{"future:write"}, "resource_filter": map[string]any{},
	}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v2/api-principals", token, map[string]any{
		"name": "Typo filter", "scopes": []string{"inventory:read"}, "resource_filter": map[string]any{"servers_ids": []int64{1}},
	}, http.StatusBadRequest)

	created := request(t, handler, http.MethodPost, "/api/v2/api-principals", token, map[string]any{
		"name": "Codex", "scopes": []string{"inventory:read"}, "resource_filter": map[string]any{}, "allowed_cidrs": []string{"192.0.2.0/24"},
	}, http.StatusCreated)["data"].(map[string]any)
	principalID := created["id"].(string)
	first := request(t, handler, http.MethodPost, "/api/v2/api-principals/"+principalID+"/tokens", token, map[string]any{}, http.StatusCreated)["data"].(map[string]any)
	second := request(t, handler, http.MethodPost, "/api/v2/api-principals/"+principalID+"/tokens", token, map[string]any{}, http.StatusCreated)["data"].(map[string]any)
	if first["token"] == second["token"] {
		t.Fatal("token rotation returned the same plaintext token")
	}
	machineToken := second["token"].(string)
	request(t, handler, http.MethodGet, "/api/v2/capabilities", machineToken, nil, http.StatusOK)
	audits, err := db.ListToolCallAudits(context.Background(), principalID, 10)
	if err != nil || len(audits) == 0 || audits[0].Capability != "http.get:/api/v2/capabilities" {
		t.Fatalf("direct API access audit=%#v err=%v", audits, err)
	}
	firstInfo := first["token_info"].(map[string]any)
	request(t, handler, http.MethodDelete, "/api/v2/api-principals/"+principalID+"/tokens/"+firstInfo["id"].(string), token, nil, http.StatusOK)
	request(t, handler, http.MethodPatch, "/api/v2/api-principals/"+principalID, token, map[string]any{"enabled": false}, http.StatusOK)
	storedPrincipal, err := db.GetAPIPrincipal(context.Background(), principalID)
	if err != nil || storedPrincipal.Enabled {
		t.Fatalf("service account disable failed: %#v err=%v", storedPrincipal, err)
	}
	request(t, handler, http.MethodDelete, "/api/v2/api-principals/"+principalID, token, nil, http.StatusOK)
	if storedPrincipal, err := db.GetAPIPrincipal(context.Background(), principalID); err == nil {
		t.Fatalf("service account delete failed: %#v", storedPrincipal)
	}
	request(t, handler, http.MethodGet, "/api/v2/capabilities", machineToken, nil, http.StatusUnauthorized)

	provider := request(t, handler, http.MethodPost, "/api/v2/ai/providers", token, map[string]any{
		"name": "local", "base_url": "http://127.0.0.1:11434/v1", "model": "test", "api_key": "first-key", "enabled": true,
	}, http.StatusCreated)["data"].(map[string]any)
	providerID := provider["id"].(string)
	endpointID := provider["endpoints"].([]any)[0].(map[string]any)["id"].(string)
	request(t, handler, http.MethodPatch, "/api/v2/ai/providers/"+providerID+"/endpoints/"+endpointID, token, map[string]any{"api_key": "second-key"}, http.StatusOK)
	request(t, handler, http.MethodPatch, "/api/v2/ai/providers/"+providerID, token, map[string]any{"enabled": false}, http.StatusOK)
	storedProvider, err := db.GetAIProvider(context.Background(), providerID)
	if err != nil || storedProvider.Enabled {
		t.Fatalf("AI Provider disable failed: %#v err=%v", storedProvider, err)
	}
	storedEndpoint, err := db.GetAIProviderEndpoint(context.Background(), providerID, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := security.DecryptSecret("test-secret", "ai-provider-endpoint-credential:"+endpointID, storedEndpoint.CredentialEncrypted)
	if err != nil || plain != "second-key" {
		t.Fatalf("AI Provider credential rotation failed: value=%q err=%v", plain, err)
	}
}

func openControllerAutomationTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "automation-controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
