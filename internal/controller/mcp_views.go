package controller

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

// mcpCapabilityView renders a capability descriptor for MCP. It never includes
// function fields or any internal state.
func mcpCapabilityView(descriptor capability.Descriptor) map[string]any {
	return map[string]any{
		"name": descriptor.Name, "version": descriptor.Version, "description": descriptor.Description,
		"input_schema": string(descriptor.InputSchema), "output_schema": string(descriptor.OutputSchema),
		"required_scopes": mcpStringSlice(descriptor.RequiredScopes), "resource_types": mcpStringSlice(descriptor.ResourceTypes), "resource_filter_evaluator": descriptor.ResourceEvaluator,
		"risk_class": descriptor.RiskClass, "approval_policy": descriptor.ApprovalPolicy, "idempotent": descriptor.Idempotent, "read_only": descriptor.ReadOnly,
		"data_classification": descriptor.DataClassification, "sensitive_fields": mcpStringSlice(descriptor.SensitiveFields), "sensitive_input_fields": mcpStringSlice(descriptor.SensitiveInput),
		"sensitive_output_fields": mcpStringSlice(descriptor.SensitiveOutput), "destructive": descriptor.Destructive, "open_world": descriptor.OpenWorld,
		"documentation": descriptor.Documentation, "deprecated_since": descriptor.DeprecatedSince, "replacement": descriptor.Replacement,
		"mcp_enabled": descriptor.MCPEnabled, "executable": descriptor.Executable,
		"minimum_access": descriptor.MinimumAccess, "rbac_permission": descriptor.RBACPermission,
	}
}

func mcpCapabilityViews(descriptors []capability.Descriptor) []map[string]any {
	items := make([]map[string]any, 0, len(descriptors))
	for _, descriptor := range descriptors {
		items = append(items, mcpCapabilityView(descriptor))
	}
	return items
}

func mcpStringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func mcpChangesetValidationSummary(raw json.RawMessage) map[string]any {
	var value struct {
		Valid               bool     `json:"valid"`
		Warnings            []string `json:"warnings"`
		ValidatedOperations int      `json:"validated_operations"`
	}
	_ = json.Unmarshal(raw, &value)
	if value.Warnings == nil {
		value.Warnings = []string{}
	}
	return map[string]any{"valid": value.Valid, "warnings": value.Warnings, "validated_operations": value.ValidatedOperations}
}

func workflowResultStatus(status model.WorkflowStatus) string {
	switch status {
	case model.WorkflowExternalActionRequired:
		return "external_action_required"
	case model.WorkflowApprovalRequired:
		return "approval_required"
	case model.WorkflowQueued:
		return "queued"
	case model.WorkflowRunning, model.WorkflowPlanning, model.WorkflowWaitingForAgent:
		return "running"
	case model.WorkflowSucceeded:
		return "succeeded"
	case model.WorkflowPartiallySucceeded:
		return "partially_succeeded"
	case model.WorkflowCancelled:
		return "cancelled"
	default:
		return "failed"
	}
}

func mcpChangesetView(item *model.AutomationChangeset) map[string]any {
	operations := make([]map[string]any, 0, len(item.Operations))
	for _, operation := range item.Operations {
		operations = append(operations, mcpOperationView(operation))
	}
	var blast struct {
		OperationCount int      `json:"operation_count"`
		Capabilities   []string `json:"capabilities"`
		ResourceKinds  []string `json:"resource_kinds"`
	}
	_ = json.Unmarshal(item.BlastRadius, &blast)
	return map[string]any{
		"id": item.ID, "principal_id": item.PrincipalID, "actor_user_id": item.ActorUserID, "status": item.Status,
		"reason": item.Reason, "idempotency_key": item.IdempotencyKey, "base_revisions": string(item.BaseRevisions), "plan_hash": item.PlanHash,
		"risk_class": item.RiskClass, "auto_apply": item.AutoApply, "validation": mcpChangesetValidationSummary(item.Validation),
		"blast_radius": map[string]any{"operation_count": blast.OperationCount, "capabilities": mcpStringSlice(blast.Capabilities), "resource_kinds": mcpStringSlice(blast.ResourceKinds)},
		"result":       string(item.Result), "expires_at": item.ExpiresAt, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
		"validated_at": item.ValidatedAt, "approved_at": item.ApprovedAt, "applied_at": item.AppliedAt, "completed_at": item.CompletedAt, "operations": operations,
	}
}

func mcpOperationView(item model.AutomationOperation) map[string]any {
	return map[string]any{
		"id": item.ID, "changeset_id": item.ChangesetID, "position": item.Position, "capability": item.Capability,
		"input": string(item.Input), "secret_refs": mcpStringSlice(item.SecretRefs), "resource_refs": string(item.ResourceRefs),
		"risk_class": item.RiskClass, "status": item.Status, "result": string(item.Result), "error_code": item.ErrorCode,
		"error_message": item.ErrorMessage, "created_at": item.CreatedAt, "completed_at": item.CompletedAt,
	}
}

func closedMCPSchema(properties map[string]any, required ...string) map[string]any {
	value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		value["required"] = required
	}
	return value
}

func mustRawSchema(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// mcpDiscoverData builds the oboard_discover payload from the live grant.
func (s *Server) mcpDiscoverData(ctx context.Context, includeDenied, includeSchemaSummaries bool) map[string]any {
	principal, _ := mcpPrincipal(ctx)
	grant, _ := mcpGrantPrincipal(ctx)
	authorized := s.capabilities.ListMCP(principal)
	denied := []map[string]any{}
	if includeDenied {
		for _, descriptor := range s.capabilities.List(application.Principal{Scopes: []string{"*"}}) {
			if !descriptor.MCPEnabled {
				continue
			}
			if _, allowed := s.capabilities.Authorize(principal, descriptor.Name); allowed {
				continue
			}
			denied = append(denied, map[string]any{"capability": descriptor.Name, "reason": s.mcpDenialReason(ctx, principal, descriptor)})
		}
		sort.Slice(denied, func(i, j int) bool { return denied[i]["capability"].(string) < denied[j]["capability"].(string) })
	}
	capabilityViews := mcpCapabilityViews(authorized)
	if !includeSchemaSummaries {
		for _, view := range capabilityViews {
			delete(view, "input_schema")
			delete(view, "output_schema")
		}
	}
	return map[string]any{
		"grant": map[string]any{
			"grant_id": grant.Grant.GrantID, "client_id": grant.Grant.ClientID, "user_id": grant.Grant.UserID,
			"access_level": grant.Grant.AccessLevel, "offline_access": grant.Grant.OfflineAccess,
			"resource_boundary": grant.Grant.ResourceBoundary, "approval_profile": grant.Grant.ApprovalProfile,
			"approval_max_risk": grant.Grant.ApprovalMaxRisk, "policy_version": grant.Grant.PolicyVersion,
			"role_version": grant.Grant.RoleVersion, "consent_version": grant.Grant.ConsentVersion,
			"expires_at": grant.Grant.ExpiresAt, "revoked_at": grant.Grant.RevokedAt,
		},
		"authorized_capabilities": capabilityViews,
		"denied_capabilities":     denied,
		"resource_groups":         s.mcpResourceGroups(principal),
		"prompt_groups":           s.mcpPromptGroups(principal),
		"workflow_rules":          map[string]any{"write_via_changeset": true, "ssh_supported": false, "shell_supported": false, "admin_deletion_supported": false, "risk4_auto_approval": false},
		"limits":                  map[string]any{"max_changeset_operations": 64, "changeset_ttl_seconds": 1800, "plan_ttl_seconds": 1800},
		"recommended_actions":     []string{"Read oboard://auth/grant before planning a change"},
	}
}

func (s *Server) mcpDiscoverCompactData(ctx context.Context) map[string]any {
	principal, _ := mcpPrincipal(ctx)
	groups := map[string]bool{}
	for _, descriptor := range s.capabilities.ListMCP(principal) {
		group, _, _ := strings.Cut(descriptor.Name, ".")
		groups[group] = true
	}
	capabilityGroups := make([]string, 0, len(groups))
	for group := range groups {
		capabilityGroups = append(capabilityGroups, group)
	}
	sort.Strings(capabilityGroups)
	recipes := []string{}
	for _, recipe := range s.mcpRecipes() {
		recipes = append(recipes, recipe.ID)
	}
	return map[string]any{
		"primary_tool": "oboard_task", "recipes": recipes, "capability_groups": capabilityGroups,
		"fallback_tools": []string{"oboard_get_capability_schema", "oboard_plan_desired_state", "oboard_validate_desired_state", "oboard_submit_changeset"},
		"workflow_rules": map[string]any{"write_via_changeset": true, "execution_via_workflow": true, "ssh_supported": false},
	}
}

func (s *Server) mcpDenialReason(ctx context.Context, principal application.Principal, descriptor capability.Descriptor) string {
	decision := s.authorizeCapability(ctx, descriptor, nil)
	switch decision.Code {
	case mcpauth.CodeInsufficientScope:
		return "insufficient_scope: requires " + descriptor.MinimumAccess.RequiredScope()
	case mcpauth.CodeRoleDenied:
		return "role_denied: current role does not permit " + descriptor.RBACPermission
	case mcpauth.CodeResourceDenied:
		return "resource_denied: outside the grant resource boundary"
	default:
		return "not_available"
	}
}

func (s *Server) mcpResourceGroups(principal application.Principal) []map[string]any {
	groups := []map[string]any{}
	if s.grantAllowsAccess(principal, mcpauth.AccessRead) {
		groups = append(groups,
			map[string]any{"group": "identity", "uris": []string{"oboard://context/bootstrap", "oboard://auth/grant", "oboard://system/version", "oboard://system/capabilities"}},
			map[string]any{"group": "docs", "uris": []string{"oboard://docs/guide", "oboard://docs/security", "oboard://docs/workflows", "oboard://docs/capabilities"}},
			map[string]any{"group": "inventory", "uris": []string{"oboard://inventory/summary", "oboard://servers", "oboard://users", "oboard://topology/current", "oboard://subscriptions", "oboard://subscription-plans", "oboard://proxy-paths", "oboard://deployments"}},
			map[string]any{"group": "audit", "uris": []string{"oboard://audit/incidents"}},
			map[string]any{"group": "automation", "uris": []string{"oboard://changesets", "oboard://workflows"}},
			map[string]any{"group": "templates", "uris": []string{"oboard://servers/{id}", "oboard://servers/{id}/health", "oboard://subscriptions/{id}", "oboard://subscription-plans/{id}", "oboard://proxy-paths/{id}", "oboard://deployments/{id}", "oboard://audit/incidents/{id}", "oboard://changesets/{id}", "oboard://workflows/{id}", "oboard://operations/{id}", "oboard://schemas/{id}"}},
		)
	}
	return groups
}

func (s *Server) mcpPromptGroups(principal application.Principal) []map[string]any {
	groups := []map[string]any{
		map[string]any{"group": "diagnosis", "prompts": []string{"oboard_permission_diagnosis"}},
		map[string]any{"group": "planning", "prompts": []string{"oboard_safe_change", "oboard_server_onboarding", "oboard_deployment"}},
		map[string]any{"group": "operations", "prompts": []string{"oboard_incident_review", "oboard_workflow_recovery"}},
	}
	_ = principal
	return groups
}
