package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestRemoteAccessEffectiveIntegration(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "eff.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	srvModel := &model.Server{Name: "Aether", AgentID: "a1", AgentTokenHash: security.HashSecret("tok"), ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, srvModel); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, srvModel.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityExec, model.RemoteAccessCapabilityInteractiveMCP}, LocalMode: model.RemoteAccessModeStandard}); err != nil {
		t.Fatal(err)
	}
	matrix := []struct {
		global bool
		server bool
		code   string
	}{
		{false, false, "remote_access_global_disabled"},
		{false, true, "remote_access_global_disabled"},
		{true, false, "remote_access_server_disabled"},
		{true, true, ""},
	}
	for _, c := range matrix {
		_ = db.SetSetting(ctx, settingMCPEnabled, boolStr(c.global))
		if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: srvModel.ID, RemoteTerminalEnabled: true, MCPEnabled: c.server}); err != nil {
			t.Fatal(err)
		}
		server, _ := db.GetServer(ctx, srvModel.ID)
		err := srv.assertRemotePrivilegeAllowed(ctx, server, model.PrivilegeRemoteShell)
		if c.code == "" && err != nil {
			t.Fatalf("global %v server %v expected allow got %v", c.global, c.server, err)
		}
		if c.code != "" {
			if err == nil {
				t.Fatalf("global %v server %v expected %s", c.global, c.server, c.code)
			}
			if coded, ok := err.(interface{ Code() string }); !ok || coded.Code() != c.code {
				t.Fatalf("global %v server %v expected %s got %v", c.global, c.server, c.code, err)
			}
		}
	}
}

func TestRemoteAccessManageIsolated(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "priv.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111112", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	srvModel := &model.Server{Name: "Aether", AgentID: "a1", AgentTokenHash: security.HashSecret("tok"), ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, srvModel); err != nil {
		t.Fatal(err)
	}
	_ = db.SetSetting(ctx, settingMCPEnabled, "true")
	if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: srvModel.ID, RemoteTerminalEnabled: true, MCPEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, srvModel.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityExec}, LocalMode: model.RemoteAccessModeStandard}); err != nil {
		t.Fatal(err)
	}
	client := testOAuthClient(t, db, "oc_priv", "Priv", []string{"http://127.0.0.1/cb"})
	grant, _ := createTestGrant(t, srv, *admin, client, []string{"oboard:read", "oboard:operate"})
	boundary, _ := json.Marshal(mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll}}})
	if _, err := db.UpsertMCPPrivilegedGrant(ctx, model.MCPPrivilegedGrant{OAuthGrantID: grant.ID, OAuthClientID: client.ID, AuthorizedUserID: admin.ID, Capabilities: []string{model.PrivilegeRemoteShell}, ResourceBoundaryJSON: boundary, CreatedByUserID: admin.ID}); err != nil {
		t.Fatal(err)
	}
	desc, ok := srv.capabilities.Get("server.remote_access.update")
	if !ok {
		t.Fatal("descriptor not found")
	}
	grantPrincipal := mcpauth.GrantPrincipal{
		Grant: mcpauth.GrantPolicy{GrantID: grant.ID, AccessLevel: mcpauth.AccessOperate, ResourceBoundary: mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll}}}},
		Role: model.RoleAdmin,
		PrivilegedGrant: &mcpauth.PrivilegedGrantPolicy{Capabilities: []string{model.PrivilegeRemoteShell}, ResourceBoundary: mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll}}}},
	}
	spec := srv.capabilitySpec(desc)
	decision := srv.mcpEvaluator().Authorize(ctx, grantPrincipal, spec, map[string]any{"server_id": srvModel.ID})
	if decision.Allowed {
		t.Fatal("remote_shell must not grant server_remote_access_manage")
	}
	grantPrincipal.PrivilegedGrant.Capabilities = []string{model.PrivilegeServerRemoteAccessManage}
	decision = srv.mcpEvaluator().Authorize(ctx, grantPrincipal, spec, map[string]any{"server_id": srvModel.ID})
	if !decision.Allowed {
		t.Fatalf("manage should allow, got %#v", decision)
	}
	grantPrincipal.PrivilegedGrant.ResourceBoundary = mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionSelected, IDs: []string{"999"}}}}
	decision = srv.mcpEvaluator().Authorize(ctx, grantPrincipal, spec, map[string]any{"server_id": srvModel.ID})
	if decision.Allowed {
		t.Fatal("resource denied should block")
	}
}

func TestMachineReadAndChangesetWithManage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "machine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111113", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	srvModel := &model.Server{Name: "Aether", AgentID: "a1", AgentTokenHash: security.HashSecret("tok"), ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, srvModel); err != nil {
		t.Fatal(err)
	}
	_ = db.SetSetting(ctx, settingMCPEnabled, "true")
	if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: srvModel.ID, RemoteTerminalEnabled: true, MCPEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, srvModel.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityExec}, LocalMode: model.RemoteAccessModeStandard}); err != nil {
		t.Fatal(err)
	}
	view, err := srv.remoteAccessMachineView(ctx, srvModel)
	if err != nil {
		t.Fatal(err)
	}
	if view.Configured.MCPEnabled != false || view.Global.MCPEnabled != true || view.Effective.MCPEnabled != false {
		t.Fatalf("machine view incorrect %#v", view)
	}
	if len(view.MCPExecution.Blockers) == 0 || view.MCPExecution.Blockers[0].Code != "remote_access_server_disabled" {
		t.Fatalf("expected server_disabled blocker %#v", view.MCPExecution)
	}
	client := testOAuthClient(t, db, "oc_machine", "Machine", []string{"http://127.0.0.1/cb"})
	grant, _ := createTestGrant(t, srv, *admin, client, []string{"oboard:read", "oboard:operate"})
	principal := userAutomationPrincipal(t, db, admin.ID)
	principal.AccessLevel = mcpauth.AccessOperate
	principal.PrivilegedClasses = []string{model.PrivilegeServerRemoteAccessManage}
	boundary, _ := json.Marshal(mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll}}})
	principal.GrantID = grant.ID
	if _, err := db.UpsertMCPPrivilegedGrant(ctx, model.MCPPrivilegedGrant{OAuthGrantID: grant.ID, OAuthClientID: client.ID, AuthorizedUserID: admin.ID, Capabilities: []string{model.PrivilegeServerRemoteAccessManage}, ResourceBoundaryJSON: boundary, CreatedByUserID: admin.ID}); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"server_id": srvModel.ID, "mcp_enabled": true})
	validated, err := srv.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "server.remote_access.update", Input: input}}})
	if err != nil {
		t.Fatalf("validate draft failed: %v", err)
	}
	if !validated.Valid {
		t.Fatal("should be valid")
	}
	enc, _ := json.Marshal(validated.ExpectedRevisions)
	chs, err := srv.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "test-manage-1", BaseRevisions: enc, Operations: []automation.OperationRequest{{Capability: "server.remote_access.update", Input: input}}})
	if err != nil {
		t.Fatalf("create changeset: %v", err)
	}
	if _, err := srv.automation.Validate(ctx, principal, chs.ID); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Approve if needed (human interactive required)
	if chs.Status == "awaiting_approval" || validated.RiskClass >= 2 {
		if _, err := srv.automation.Approve(ctx, principal, chs.ID, "ok"); err != nil {
			// If already approved automatically, ignore
			_ = err
		}
		chs, _ = srv.automation.Get(ctx, chs.ID)
	}
	applied, err := srv.automation.Apply(ctx, principal, chs.ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status != model.ChangesetSucceeded {
		t.Fatalf("apply status %s", applied.Status)
	}
	policy, _ := db.GetServerRemoteAccessPolicy(ctx, srvModel.ID)
	if !policy.MCPEnabled {
		t.Fatal("policy should be true after apply")
	}
	tasks, _ := db.ListTasksByServer(ctx, srvModel.ID, 10)
	for _, task := range tasks {
		if task.Type == "apply_deployment" || task.Type == "apply_core_config" {
			t.Fatalf("unexpected deployment task %s", task.Type)
		}
	}
}
func TestGlobalDoesNotAutoEnableServer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "global.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	srvModel := &model.Server{Name: "Aether", AgentID: "a1", AgentTokenHash: security.HashSecret("tok"), ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	db.CreateServer(ctx, srvModel)
	db.SetSetting(ctx, settingMCPEnabled, "false")
	db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: srvModel.ID, RemoteTerminalEnabled: true, MCPEnabled: false})
	// Global false, server false
	server, _ := db.GetServer(ctx, srvModel.ID)
	if err := srv.assertRemotePrivilegeAllowed(ctx, server, model.PrivilegeRemoteShell); err == nil || err.(interface{ Code() string }).Code() != "remote_access_global_disabled" {
		t.Fatalf("expected global disabled")
	}
	// Now flip global true, server should still be false (not auto enabled)
	db.SetSetting(ctx, settingMCPEnabled, "true")
	policy, _ := db.GetServerRemoteAccessPolicy(ctx, srvModel.ID)
	if policy.MCPEnabled {
		t.Fatal("global true must not auto enable server policy")
	}
	if err := srv.assertRemotePrivilegeAllowed(ctx, server, model.PrivilegeRemoteShell); err == nil || err.(interface{ Code() string }).Code() != "remote_access_server_disabled" {
		t.Fatalf("expected server disabled after global true, got %v", err)
	}
	// Now update server to true via direct domain service (simulating web PATCH while global is true)
	patch := RemoteAccessPolicyPatch{MCPEnabled: boolPtr(true)}
	view, err := srv.updateServerRemoteAccessPolicy(ctx, srvModel, patch, "user", "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Server.MCPEnabled || !view.Effective.MCPEnabled {
		t.Fatalf("after server enable, effective should be true: %#v", view)
	}
	// Global false -> server true should retain configured true but effective false
	db.SetSetting(ctx, settingMCPEnabled, "false")
	policy, _ = db.GetServerRemoteAccessPolicy(ctx, srvModel.ID)
	if !policy.MCPEnabled {
		t.Fatal("server policy must stay true after global false")
	}
	view, _ = srv.remoteAccessViewFromContext(ctx, srvModel)
	if view.Server.MCPEnabled != true || view.Effective.MCPEnabled != false {
		t.Fatalf("effective should be false while configured true: %#v", view)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestDiagnosticToolRemediation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "diag.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111114", ProxyPassword: "unused"}
	db.CreateUser(ctx, admin)
	srvModel := &model.Server{Name: "Aether", AgentID: "a1", AgentTokenHash: security.HashSecret("tok"), ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	db.CreateServer(ctx, srvModel)
	db.SetSetting(ctx, settingMCPEnabled, "true")
	db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: srvModel.ID, RemoteTerminalEnabled: true, MCPEnabled: false})
	db.UpsertServerRemoteAccessStatus(ctx, srvModel.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityExec}, LocalMode: model.RemoteAccessModeStandard})
	// Without manage, diagnostic should show remediation requiring manage
	client := testOAuthClient(t, db, "oc_diag", "Diag", []string{"http://127.0.0.1/cb"})
	grant, _ := createTestGrant(t, srv, *admin, client, []string{"oboard:read", "oboard:operate"})
	boundary, _ := json.Marshal(mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll}}})
	db.UpsertMCPPrivilegedGrant(ctx, model.MCPPrivilegedGrant{OAuthGrantID: grant.ID, OAuthClientID: client.ID, AuthorizedUserID: admin.ID, Capabilities: []string{model.PrivilegeRemoteShell}, ResourceBoundaryJSON: boundary, CreatedByUserID: admin.ID})
	// Build grant principal for diagnostic
	gp := &mcpauth.GrantPrincipal{
		Grant: mcpauth.GrantPolicy{GrantID: grant.ID, AccessLevel: mcpauth.AccessOperate, ResourceBoundary: mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll}}}},
		Role: model.RoleAdmin,
		PrivilegedGrant: &mcpauth.PrivilegedGrantPolicy{Capabilities: []string{model.PrivilegeRemoteShell}, ResourceBoundary: mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll}}}},
	}
	diag, err := srv.remoteAccessDiagnosticView(ctx, srvModel, gp)
	if err != nil {
		t.Fatal(err)
	}
	if len(diag.Blockers) == 0 || diag.Blockers[0] != "remote_access_server_disabled" {
		t.Fatalf("blockers %#v", diag.Blockers)
	}
	if diag.Remediation == nil || diag.Remediation["requires_capability"] != model.PrivilegeServerRemoteAccessManage {
		t.Fatalf("remediation %#v", diag.Remediation)
	}
	if diag.EffectiveMCPEnabled != false || diag.ServerMCPEnabled != false || diag.GlobalMCPEnabled != true {
		t.Fatalf("effective %#v", diag)
	}
}


func TestRemoteAccessRevokesMCPSessionOnDisable(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "revoke.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	srvModel := &model.Server{Name: "Aether", AgentID: "a1", AgentTokenHash: security.HashSecret("tok"), ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	db.CreateServer(ctx, srvModel)
	db.SetSetting(ctx, settingMCPEnabled, "true")
	db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: srvModel.ID, RemoteTerminalEnabled: true, MCPEnabled: true})
	db.UpsertServerRemoteAccessStatus(ctx, srvModel.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityInteractiveMCP}, LocalMode: model.RemoteAccessModeStandard})
	// Manually insert a terminal session
	sessID := "test-sess-1"
	srv.terminalHub.mu.Lock()
	srv.terminalHub.sessions[sessID] = &terminalSession{ID: sessID, ServerID: srvModel.ID, OwnerType: InteractiveOwnerMCP, OAuthGrantID: "grant1", OAuthClientID: "client1", PrivilegedGrantID: 1, CreatedAt: srvModel.CreatedAt}
	srv.terminalHub.mu.Unlock()
	if srv.terminalHub.countForServer(srvModel.ID) != 1 {
		t.Fatal("session not inserted")
	}
	patch := RemoteAccessPolicyPatch{MCPEnabled: boolPtr(false)}
	if _, err := srv.updateServerRemoteAccessPolicy(ctx, srvModel, patch, "user", "127.0.0.1", ""); err != nil {
		t.Fatal(err)
	}
	if srv.terminalHub.countForServer(srvModel.ID) != 0 {
		t.Fatal("session should be revoked after MCP disable")
	}
	// Web disable should not affect MCP session (already revoked, but test isolation)
	db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: srvModel.ID, RemoteTerminalEnabled: true, MCPEnabled: true})
	srv.terminalHub.mu.Lock()
	srv.terminalHub.sessions["sess2"] = &terminalSession{ID: "sess2", ServerID: srvModel.ID, OwnerType: InteractiveOwnerMCP, OAuthGrantID: "grant1"}
	srv.terminalHub.mu.Unlock()
	patch2 := RemoteAccessPolicyPatch{RemoteTerminalEnabled: boolPtr(false)}
	if _, err := srv.updateServerRemoteAccessPolicy(ctx, srvModel, patch2, "user", "127.0.0.1", ""); err != nil {
		t.Fatal(err)
	}
	if srv.terminalHub.countForServer(srvModel.ID) != 1 {
		t.Fatalf("web disable should not close MCP session, got %d", srv.terminalHub.countForServer(srvModel.ID))
	}
	// Global disable should close all
	srv.closeAllMCPTerminals("remote_access_global_disabled")
	if srv.terminalHub.countForServer(srvModel.ID) != 0 {
		t.Fatal("global disable should close all MCP")
	}
}

func TestMCPDiagnosticTool(t *testing.T) {
db, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	ctx := context.Background()
	node := &model.Server{Name: "Aether", AgentID: "agent-diag", AgentTokenHash: security.HashSecret("tok"), ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingMCPEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertServerRemoteAccessPolicy(ctx, model.ServerRemoteAccessPolicy{ServerID: node.ID, RemoteTerminalEnabled: true, MCPEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityExec}, LocalMode: model.RemoteAccessModeStandard}); err != nil {
		t.Fatal(err)
	}
	// Need privileged grant for remote_shell to be able to call diagnostic? Diagnostic requires servers:read only, not privileged, so should work without grant.
	// Call diagnostic tool without privileged grant
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "server_remote_access_get", Arguments: map[string]any{"server_id": node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("diagnostic isError: %v", mcpResultText(res))
	}
	text := mcpResultText(res)
	if !containsStr(text, "remote_access_server_disabled") {
		t.Fatalf("expected server_disabled blocker, got %s", text)
	}
	if !containsStr(text, "server_remote_access_manage") {
		t.Fatalf("expected remediation with manage, got %s", text)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
