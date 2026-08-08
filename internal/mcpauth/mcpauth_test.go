package mcpauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/authorization"
	"github.com/OboardProject/oboard/internal/model"
)

func TestAccessLevelAllows(t *testing.T) {
	if !AccessOperate.Allows(AccessRead) {
		t.Fatal("operate must include read")
	}
	if !AccessOperate.Allows(AccessOperate) {
		t.Fatal("operate must include operate")
	}
	if !AccessRead.Allows(AccessRead) {
		t.Fatal("read must include read")
	}
	if AccessRead.Allows(AccessOperate) {
		t.Fatal("read must not include operate")
	}
}

func TestNormalizedScopes(t *testing.T) {
	got := AccessRead.NormalizedScopes(false)
	if len(got) != 1 || got[0] != ScopeRead {
		t.Fatalf("read scopes = %#v", got)
	}
	got = AccessOperate.NormalizedScopes(false)
	if len(got) != 2 || got[0] != ScopeRead || got[1] != ScopeOperate {
		t.Fatalf("operate scopes = %#v", got)
	}
	got = AccessOperate.NormalizedScopes(true)
	if len(got) != 3 {
		t.Fatalf("operate offline scopes = %#v", got)
	}
}

func TestResourceBoundaryDenied(t *testing.T) {
	boundary := ResourceBoundary{
		Version: ResourceBoundaryVersion,
		Resources: map[string]ResourceSelection{
			"server": {Selection: SelectionSelected, IDs: []string{"1", "2"}},
			"user":   {Selection: SelectionNone},
		},
	}.Normalized()
	if len(boundary.Denied([]ResourceRef{{Type: "server", ID: "1"}})) != 0 {
		t.Fatal("selected server 1 should be allowed")
	}
	if len(boundary.Denied([]ResourceRef{{Type: "server", ID: "3"}})) != 1 {
		t.Fatal("server 3 should be denied")
	}
	if len(boundary.Denied([]ResourceRef{{Type: "user", ID: "5"}})) != 1 {
		t.Fatal("user should be denied when selection is none")
	}
	if len(boundary.Denied([]ResourceRef{{Type: "unknown", ID: "1"}})) != 1 {
		t.Fatal("unknown resource type must default to denied")
	}
	all := ResourceBoundary{Version: ResourceBoundaryVersion, Resources: map[string]ResourceSelection{"server": {Selection: SelectionAll}}}.Normalized()
	if len(all.Denied([]ResourceRef{{Type: "server", ID: "99"}})) != 0 {
		t.Fatal("all selection must allow any server")
	}
}

func TestResourceBoundaryAllowsCreateNotImplied(t *testing.T) {
	boundary := ResourceBoundary{Version: 1, Resources: map[string]ResourceSelection{"server": {Selection: SelectionAll}}}.Normalized()
	if boundary.AllowsCreate("server") {
		t.Fatal("all must not imply allow_create")
	}
	boundary.Resources["server"] = ResourceSelection{Selection: SelectionAll, AllowCreate: true}
	if !boundary.AllowsCreate("server") {
		t.Fatal("allow_create must be honored")
	}
}

type stubRBAC struct {
	allowed  map[string]bool
	readOnly map[string]bool
}

func (s stubRBAC) Allows(role model.Role, permission string) bool {
	if role == model.RoleViewer {
		return s.readOnly[permission]
	}
	return s.allowed[permission]
}

func TestEvaluatorAuthorizationOrder(t *testing.T) {
	now := time.Now().UTC()
	grant := GrantPolicy{
		GrantID: "grt_1", ClientID: "oc_1", UserID: "1", PrincipalID: "prn_1",
		AccessLevel: AccessRead, ResourceBoundary: ResourceBoundary{Version: 1, Resources: map[string]ResourceSelection{"server": {Selection: SelectionAll}}},
		ApprovalMaxRisk: 2, PolicyVersion: 1, RoleVersion: 1, ConsentVersion: 2, IssuedAt: now,
	}
	spec := CapabilitySpec{ID: "servers.get", MinimumAccess: AccessRead, RBACPermission: "servers.get", RiskClass: 0}
	evaluator := NewEvaluator(stubRBAC{allowed: map[string]bool{"servers.get": true, "deployments.apply": true, "critical": true, "x": true}, readOnly: map[string]bool{"servers.get": true}}, authorization.GrantApprovalResolver{})

	decision := evaluator.Authorize(context.Background(), GrantPrincipal{Grant: grant, Role: model.RoleOperator}, spec, nil)
	if !decision.Allowed {
		t.Fatalf("allowed read expected: %#v", decision)
	}

	writeSpec := CapabilitySpec{ID: "deployments.apply", MinimumAccess: AccessOperate, RBACPermission: "deployments.apply", RiskClass: 3, ApprovalRequired: true}
	decision = evaluator.Authorize(context.Background(), GrantPrincipal{Grant: grant, Role: model.RoleOperator}, writeSpec, nil)
	if decision.Allowed || decision.Code != CodeInsufficientScope {
		t.Fatalf("read grant must deny operate capability: %#v", decision)
	}

	operateGrant := grant
	operateGrant.AccessLevel = AccessOperate
	operateGrant.ApprovalMaxRisk = 0
	decision = evaluator.Authorize(context.Background(), GrantPrincipal{Grant: operateGrant, Role: model.RoleOperator}, writeSpec, nil)
	if !decision.Allowed {
		t.Fatalf("operate grant must allow operate capability: %#v", decision)
	}
	if decision.ApprovalMode != string(authorization.ApprovalRequired) {
		t.Fatalf("risk 3 with max risk 0 must require approval: %#v", decision)
	}

	deniedRBAC := evaluator.Authorize(context.Background(), GrantPrincipal{Grant: operateGrant, Role: model.RoleViewer}, writeSpec, nil)
	if deniedRBAC.Allowed || deniedRBAC.Code != CodeRoleDenied {
		t.Fatalf("viewer role must be denied: %#v", deniedRBAC)
	}

	boundaryDeniedGrant := operateGrant
	boundaryDeniedGrant.ResourceBoundary = ResourceBoundary{Version: 1, Resources: map[string]ResourceSelection{"server": {Selection: SelectionNone}}}
	boundarySpec := CapabilitySpec{ID: "deployments.apply", MinimumAccess: AccessOperate, RBACPermission: "deployments.apply", RiskClass: 2, ResolveResourceRefs: func(_ context.Context, _ any) ([]ResourceRef, error) {
		return []ResourceRef{{Type: "server", ID: "1"}}, nil
	}}
	decision = evaluator.Authorize(context.Background(), GrantPrincipal{Grant: boundaryDeniedGrant, Role: model.RoleOperator}, boundarySpec, nil)
	if decision.Allowed || decision.Code != CodeResourceDenied || len(decision.DeniedResources) != 1 {
		t.Fatalf("boundary must deny: %#v", decision)
	}

	revokedGrant := operateGrant
	revokedAt := now.Add(-time.Hour)
	revokedGrant.RevokedAt = &revokedAt
	decision = evaluator.Authorize(context.Background(), GrantPrincipal{Grant: revokedGrant, Role: model.RoleOperator}, writeSpec, nil)
	if decision.Allowed || decision.Code != CodeGrantRevoked {
		t.Fatalf("revoked grant must deny: %#v", decision)
	}

	resolverErr := CapabilitySpec{ID: "x", MinimumAccess: AccessOperate, RBACPermission: "x", ResolveResourceRefs: func(_ context.Context, _ any) ([]ResourceRef, error) {
		return nil, errors.New("bad input")
	}}
	decision = evaluator.Authorize(context.Background(), GrantPrincipal{Grant: operateGrant, Role: model.RoleOperator}, resolverErr, nil)
	if decision.Allowed || decision.Code != CodeInvalidInput {
		t.Fatalf("resolver error must deny input: %#v", decision)
	}
}

func TestEvaluatorRisk4NeverAutoApproved(t *testing.T) {
	now := time.Now().UTC()
	grant := GrantPolicy{GrantID: "g", AccessLevel: AccessOperate, ApprovalMaxRisk: 4, ResourceBoundary: ResourceBoundary{Version: 1}, IssuedAt: now}
	spec := CapabilitySpec{ID: "critical", MinimumAccess: AccessOperate, RBACPermission: "critical", RiskClass: 4, ApprovalRequired: true}
	evaluator := NewEvaluator(stubRBAC{allowed: map[string]bool{"critical": true}}, authorization.GrantApprovalResolver{})
	decision := evaluator.Authorize(context.Background(), GrantPrincipal{Grant: grant, Role: model.RoleAdmin}, spec, nil)
	if !decision.Allowed {
		t.Fatalf("admin must be allowed to run risk 4 with approval: %#v", decision)
	}
	if decision.ApprovalMode != string(authorization.ApprovalRequired) {
		t.Fatalf("risk 4 must never be automatic: %#v", decision)
	}
}

func TestMCPErrorEnvelope(t *testing.T) {
	decision := DenyResources([]ResourceRef{{Type: "server", ID: "srv_123"}})
	envelope := NotAllowedError(decision, "corr_1")
	if envelope.SchemaVersion != "2" || envelope.Status != "failed" || envelope.Error.Code != CodeResourceDenied {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if envelope.Error.NextAction == nil || envelope.Error.NextAction.Type != "request_new_consent" {
		t.Fatalf("missing next_action: %#v", envelope.Error)
	}
}
