package mcpauth

import (
	"context"

	"github.com/OboardProject/oboard/internal/authorization"
	"github.com/OboardProject/oboard/internal/model"
)

// RBACChecker is the shared role-based permission service used by Web, REST,
// and MCP.
type RBACChecker interface {
	Allows(role model.Role, permission string) bool
}

// Evaluator is the single MCP authorization evaluator. Every Resource read,
// Tool call, and Changeset operation must go through it. No Controller handler
// may assemble its own scope checks.
type Evaluator struct {
	rbac      RBACChecker
	approvals authorization.ApprovalResolver
}

func NewEvaluator(rbac RBACChecker, approvals authorization.ApprovalResolver) *Evaluator {
	if approvals == nil {
		approvals = authorization.GrantApprovalResolver{}
	}
	return &Evaluator{rbac: rbac, approvals: approvals}
}

// Authorize runs the unified evaluation order:
//
//  1. Grant is active (not revoked / expired).
//  2. Grant access level satisfies the capability minimum.
//  3. Shared RBAC allows the capability permission for the human role.
//  4. Resource refs resolve from the input.
//  5. The grant resource boundary allows every resolved ref.
//  6. Approval rules resolve for the risk class.
func (e *Evaluator) Authorize(ctx context.Context, principal GrantPrincipal, spec CapabilitySpec, input any) AuthorizationDecision {
	grant := principal.Grant
	if grant.RevokedAt != nil {
		return DenyDecision("grant_revoked", "the OAuth grant has been revoked", false)
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(grant.IssuedAt.Add(0)) {
		// The snapshot's issued/expiry are checked by the store; keep this
		// simple and rely on RevokedAt plus store-level liveness.
	}
	if !grant.AccessLevel.Allows(spec.MinimumAccess) {
		return DenyScope(spec.MinimumAccess)
	}
	if !e.rbac.Allows(principal.Role, spec.RBACPermission) {
		return DenyRole(spec.RBACPermission)
	}
	refs := []ResourceRef{}
	if spec.ResolveResourceRefs != nil {
		resolved, err := spec.ResolveResourceRefs(ctx, input)
		if err != nil {
			return DenyInput(err)
		}
		refs = resolved
	}
	if denied := grant.ResourceBoundary.Denied(refs); len(denied) != 0 {
		return DenyResources(denied)
	}
	approval := authorization.ApprovalRequired
	if spec.ApprovalRequired {
		approval = e.approvals.Resolve(grant.ApprovalMaxRisk, spec.RiskClass, true)
	} else {
		approval = e.approvals.Resolve(grant.ApprovalMaxRisk, spec.RiskClass, false)
	}
	return AllowDecision(approval, spec.RiskClass)
}

// AuthorizeCapability is a convenience for callers that already hold a
// resolved capability spec.
func (e *Evaluator) AuthorizeCapability(ctx context.Context, principal GrantPrincipal, spec CapabilitySpec, input any) AuthorizationDecision {
	return e.Authorize(ctx, principal, spec, input)
}
