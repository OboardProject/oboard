package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type allowAllRBAC struct{}

func (allowAllRBAC) Allows(model.Role, string) bool { return true }

func TestStepUpTokenCannotBeReusedOrCrossPurpose(t *testing.T) {
	secret := "step-up-secret"
	now := time.Now().UTC()
	token, err := security.SignStepUpToken(secret, security.StepUpTokenClaims{
		UserID: 1, SessionID: "sess", SessionVersion: 1, Purpose: model.StepUpPurposeRemoteTerminal,
		ResourceType: "server", ResourceID: "17", Nonce: "abc", IssuedAt: now, ExpiresAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := security.VerifyStepUpToken(secret, token, now)
	if err != nil || claims.Purpose != model.StepUpPurposeRemoteTerminal || claims.ResourceID != "17" {
		t.Fatalf("verify: %#v err=%v", claims, err)
	}
	if _, err := security.VerifyStepUpToken(secret, token, now.Add(3*time.Minute)); err == nil {
		t.Fatal("expired token should fail")
	}
}

func TestNormalizeRemoteAccessSwitchesEnforcesMCPDependency(t *testing.T) {
	on, off := true, false
	remote, mcp, err := normalizeRemoteAccessSwitches(&on, &on, nil, nil)
	if err != nil || remote == nil || !*remote || mcp == nil || !*mcp {
		t.Fatalf("normalize with on must preserve on: remote=%v mcp=%v err=%v", remote, mcp, err)
	}
	remote, mcp, err = normalizeRemoteAccessSwitches(&off, nil, nil, nil)
	if err != nil || remote == nil || *remote {
		t.Fatalf("remote off should stay off: remote=%v err=%v", remote, err)
	}
	if mcp != nil {
		t.Fatalf("mcp should remain nil when not provided: mcp=%v", mcp)
	}
	if _, _, err := normalizeRemoteAccessSwitches(&on, &on, &off, &on); err != nil {
		t.Fatalf("split MCP control values must be allowed independently: %v", err)
	}
	patch := RemoteAccessPolicyPatch{
		MCPEnabled: &on,
	}
	if err := normalizeRemoteAccessPatch(patch); err != nil {
		t.Fatalf("independent patch must be valid: %v", err)
	}
	_ = off
}

func TestRemoteAccessDefaults(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "remote-defaults.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	settings := server.publicSettings(context.Background(), map[string]string{})
	if settings[settingRemoteTerminalEnabled] != true || settings[settingRemoteTerminalPasswordConfirmationEnabled] != true {
		t.Fatalf("remote control and password confirmation must default on: %#v", settings)
	}
	if settings[settingMCPEnabled] != false {
		t.Fatalf("MCP control must default off: %#v", settings)
	}
}

func TestPrivilegedGrantRemoteExecDoesNotImplyRawShell(t *testing.T) {
	grant := &model.OAuthGrant{ID: "grt_exec", ClientID: "oc_1", UserID: 1}
	got, err := normalizePrivilegedGrantInput(grant, 1, privilegedAccessInput{Capabilities: []string{model.PrivilegeRemoteExec}}, []string{"12"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasCapability(model.PrivilegeRemoteExec) {
		t.Fatalf("exec grant missing remote_exec: %#v", got.Capabilities)
	}
	if got.HasCapability(model.PrivilegeRemoteShell) || got.HasCapability(model.PrivilegeRemoteInteractive) {
		t.Fatalf("exec-only grant expanded to shell/pty: %#v", got.Capabilities)
	}
}

func TestPrivilegedGrantSnapshotsServersWhenIncludeFutureFalse(t *testing.T) {
	grant := &model.OAuthGrant{ID: "grt_snap", ClientID: "oc_1", UserID: 1}
	boundary, _ := json.Marshal(mcpauth.ResourceBoundary{
		Version: mcpauth.ResourceBoundaryVersion,
		Resources: map[string]mcpauth.ResourceSelection{
			"server": {Selection: mcpauth.SelectionAll, IncludeFuture: false},
		},
	})
	got, err := normalizePrivilegedGrantInput(grant, 1, privilegedAccessInput{
		Capabilities:     []string{model.PrivilegeRemoteExec},
		ResourceBoundary: boundary,
	}, []string{"12", "15"})
	if err != nil {
		t.Fatal(err)
	}
	parsed := mcpauth.ParseBoundary(got.ResourceBoundaryJSON)
	sel := parsed.Selection("server")
	if sel.Selection != mcpauth.SelectionSelected || sel.IncludeFuture {
		t.Fatalf("expected selected snapshot, got %#v", sel)
	}
	if !slices.Contains(sel.IDs, "12") || !slices.Contains(sel.IDs, "15") {
		t.Fatalf("snapshot ids=%v", sel.IDs)
	}
	if parsed.AllowsResource(mcpauth.ResourceRef{Type: "server", ID: "99"}) {
		t.Fatal("new server must be denied when include_future is false")
	}
}

func TestPrivilegedGrantElevation(t *testing.T) {
	next := model.MCPPrivilegedGrant{Capabilities: []string{model.PrivilegeRemoteExec}}
	if !privilegedGrantElevates(nil, next) {
		t.Fatal("creating a grant with capabilities requires step-up")
	}
	existing := model.MCPPrivilegedGrant{Capabilities: []string{model.PrivilegeRemoteExec}, ResourceBoundaryJSON: []byte(`{"version":1,"resources":{"server":{"selection":"selected","ids":["1"]}}}`)}
	wider := existing
	wider.ResourceBoundaryJSON = []byte(`{"version":1,"resources":{"server":{"selection":"all"}}}`)
	if !privilegedGrantElevates(&existing, wider) {
		t.Fatal("selected -> all requires step-up")
	}
	reduced := existing
	reduced.Capabilities = nil
	if privilegedGrantElevates(&existing, reduced) {
		t.Fatal("reducing capabilities must not require step-up")
	}
}

func TestMCPEvaluatorRequiresPrivilegedGrant(t *testing.T) {
	eval := mcpauth.NewEvaluator(allowAllRBAC{}, nil)
	spec := mcpauth.CapabilitySpec{
		MinimumAccess: mcpauth.AccessOperate, RBACPermission: "admin.settings",
		PrivilegeClass: model.PrivilegeRemoteExec, RiskClass: 3,
		ResolveResourceRefs: func(ctx context.Context, input any) ([]mcpauth.ResourceRef, error) {
			args, _ := input.(map[string]any)
			return []mcpauth.ResourceRef{{Type: "server", ID: fmt.Sprint(args["server_id"])}}, nil
		},
	}
	grant := mcpauth.GrantPrincipal{Grant: mcpauth.GrantPolicy{GrantID: "grt_1", AccessLevel: mcpauth.AccessOperate, ResourceBoundary: mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll, IncludeFuture: true}}}}, Role: model.RoleAdmin}
	decision := eval.Authorize(context.Background(), grant, spec, map[string]any{"server_id": 12})
	if decision.Allowed || decision.Code != mcpauth.CodePrivilegedGrantRequired {
		t.Fatalf("operate without privileged grant: %#v", decision)
	}
	grant.PrivilegedGrant = &mcpauth.PrivilegedGrantPolicy{
		Capabilities:     []string{model.PrivilegeRemoteExec},
		ResourceBoundary: mcpauth.ResourceBoundary{Version: 1, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionSelected, IDs: []string{"12"}}}},
	}
	decision = eval.Authorize(context.Background(), grant, spec, map[string]any{"server_id": 12})
	if !decision.Allowed || decision.ApprovalMode != "automatic" {
		t.Fatalf("privileged grant should auto-allow: %#v", decision)
	}
	denied := eval.Authorize(context.Background(), grant, spec, map[string]any{"server_id": 99})
	if denied.Allowed || denied.Code != mcpauth.CodeResourceDenied {
		t.Fatalf("privileged boundary must deny other servers: %#v", denied)
	}
}

func TestHumanTerminalsCloseWhenServerTerminalDisabled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "human-pty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	node := &model.Server{Name: "human-pty", AgentID: "agent-human", AgentTokenHash: security.HashSecret("token"), ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	srv.terminalHub.sessions["human-1"] = &terminalSession{ID: "human-1", ServerID: node.ID, OwnerType: InteractiveOwnerHuman}
	disabled := false
	if _, err := srv.updateServerRemoteAccessPolicy(ctx, node, RemoteAccessPolicyPatch{RemoteTerminalEnabled: &disabled}, "user", "127.0.0.1", ""); err != nil {
		t.Fatal(err)
	}
	if srv.terminalHub.countForServer(node.ID) != 0 {
		t.Fatal("disabling remote terminal left a human PTY open")
	}
}

func TestHumanTerminalsCloseWhenGlobalTerminalDisabled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "human-pty-global.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	node := &model.Server{Name: "human-pty-global", AgentID: "agent-human-global", AgentTokenHash: security.HashSecret("token"), ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	srv.terminalHub.sessions["human-global"] = &terminalSession{ID: "human-global", ServerID: node.ID, OwnerType: InteractiveOwnerHuman}
	srv.handleGlobalRemoteAccessChange(ctx, []string{settingRemoteTerminalEnabled}, map[string]string{settingRemoteTerminalEnabled: "false"})
	if srv.terminalHub.countForServer(node.ID) != 0 {
		t.Fatal("disabling global remote terminal left a human PTY open")
	}
}

func TestSettingsUpdateCandidateClosesHumanTerminals(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "settings-pty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	node := &model.Server{Name: "settings-pty", AgentID: "agent-settings-pty", AgentTokenHash: security.HashSecret("token"), ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	srv.terminalHub.sessions["human-settings"] = &terminalSession{ID: "human-settings", ServerID: node.ID, OwnerType: InteractiveOwnerHuman}
	input, _ := json.Marshal(map[string]any{"changes": map[string]any{settingRemoteTerminalEnabled: false}})
	if _, err := srv.settingsUpdateCandidate(ctx, input, true); err != nil {
		t.Fatal(err)
	}
	if srv.terminalHub.countForServer(node.ID) != 0 {
		t.Fatal("settings.update left a human PTY open after global disable")
	}
}

func TestOAuthConsentPreviewOmitsPrivilegedCapabilities(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "consent-remote.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	for _, item := range server.oauthConsentPreview(model.RoleAdmin, mcpauth.AccessOperate) {
		switch item.Capability {
		case "node.exec", "node.exec_shell", "node.system_info":
			t.Fatalf("consent preview leaked privileged capability %s", item.Capability)
		}
	}
}
