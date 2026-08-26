package mcpauth

import (
	"github.com/OboardProject/oboard/internal/authorization"
)

// Decision codes are the structured error codes returned in MCP envelopes and
// authorization summaries. External responses for sensitive objects must not
// distinguish "does not exist" from "exists but denied"; internal audit can.
const (
	CodeInvalidToken           = "invalid_token"
	CodeInvalidAudience        = "invalid_audience"
	CodeGrantRevoked           = "grant_revoked"
	CodeClientDisabled         = "client_disabled"
	CodeUserDisabled           = "user_disabled"
	CodeInsufficientScope      = "insufficient_scope"
	CodeRoleDenied             = "role_denied"
	CodeResourceDenied         = "resource_denied"
	CodeApprovalRequired       = "approval_required"
	CodeRevisionConflict       = "revision_conflict"
	CodeInvalidInput           = "invalid_input"
	CodeNotFound               = "not_found"
	CodeExpired                = "expired"
	CodeAlreadyConsumed        = "already_consumed"
	CodeIdempotencyConflict    = "idempotency_conflict"
	CodeWorkflowNotRetryable   = "workflow_not_retryable"
	CodeExternalActionRequired     = "external_action_required"
	CodeClientMetadataInvalid      = "client_metadata_invalid"
	CodePrivilegedGrantRequired    = "privileged_grant_required"
	CodePrivilegedGrantExpired     = "privileged_grant_expired"
	CodePrivilegedGrantRevoked     = "privileged_grant_revoked"
	CodeRawShellNotGranted         = "raw_shell_not_granted"
	CodeRemoteAccessGlobalDisabled = "remote_access_global_disabled"
	CodeRemoteAccessServerDisabled = "remote_access_server_disabled"
	CodeAgentOffline               = "agent_offline"
	CodeAgentUpgradeRequired       = "agent_upgrade_required"
	CodeAgentLocalGateDenied       = "agent_local_gate_denied"
)

// Decision builders produce uniformly structured AuthorizationDecision values.

func allowDecision(approval authorization.ApprovalMode, riskClass int) AuthorizationDecision {
	return AuthorizationDecision{Allowed: true, Code: "allowed", ApprovalMode: string(approval), RiskClass: riskClass, Recoverable: true}
}

func denyDecision(code, reason string, recoverable bool) AuthorizationDecision {
	return AuthorizationDecision{Allowed: false, Code: code, Reason: reason, Recoverable: recoverable}
}

func AllowDecision(approval authorization.ApprovalMode, riskClass int) AuthorizationDecision {
	return allowDecision(approval, riskClass)
}

// DenyDecision produces a generic denial.
func DenyDecision(code, reason string, recoverable bool) AuthorizationDecision {
	return denyDecision(code, reason, recoverable)
}

// DenyScope reports that the grant's access level is below the capability
// minimum.
func DenyScope(minimum AccessLevel) AuthorizationDecision {
	decision := denyDecision(CodeInsufficientScope, "this operation requires "+minimum.RequiredScope(), true)
	decision.RequiredScope = minimum.RequiredScope()
	return decision
}

// DenyRole reports that the human role does not grant the RBAC permission.
func DenyRole(permission string) AuthorizationDecision {
	decision := denyDecision(CodeRoleDenied, "the current role does not permit this operation", true)
	decision.RequiredRole = permission
	return decision
}

// DenyResources reports resource refs rejected by the grant boundary.
func DenyResources(refs []ResourceRef) AuthorizationDecision {
	decision := denyDecision(CodeResourceDenied, "the current grant does not include one or more requested resources", true)
	decision.DeniedResources = refs
	return decision
}

// DenyInput reports that resource refs could not be resolved from the input.
func DenyInput(err error) AuthorizationDecision {
	decision := denyDecision(CodeInvalidInput, "the operation input could not be resolved to resource references", false)
	if err != nil {
		decision.Reason = err.Error()
	}
	return decision
}
