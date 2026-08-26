package controller

import (
	"context"
	"fmt"
	"path/filepath"
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
