package authorization

// ApprovalMode is the resolved approval outcome for one operation within a
// grant. It mirrors the persisted model.ApprovalMode values.
type ApprovalMode string

const (
	ApprovalDenied    ApprovalMode = "denied"
	ApprovalRequired  ApprovalMode = "required"
	ApprovalAutomatic ApprovalMode = "automatic"
)

// ApprovalResolver decides how a grant's approval profile applies to a
// capability. The server-side enforcement still happens in the Changeset and
// Workflow engine; this resolver is the single MCP-facing projection.
type ApprovalResolver interface {
	Resolve(maxAutoApproveRisk, riskClass int, approvalRequired bool) ApprovalMode
}

// GrantApprovalResolver implements the approval rules used by Consent and the
// unified evaluator:
//   - non-approval-required capabilities are automatic;
//   - risk class 4 is never auto-approved;
//   - otherwise a capability at or below the profile maximum is automatic.
type GrantApprovalResolver struct{}

func (GrantApprovalResolver) Resolve(maxAutoApproveRisk, riskClass int, approvalRequired bool) ApprovalMode {
	if !approvalRequired {
		return ApprovalAutomatic
	}
	if riskClass >= 4 {
		return ApprovalRequired
	}
	if maxAutoApproveRisk >= riskClass {
		return ApprovalAutomatic
	}
	return ApprovalRequired
}
