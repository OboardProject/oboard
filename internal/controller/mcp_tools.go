package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/authorization"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

type mcpOperationRef struct {
	Capability string         `json:"capability"`
	Input      map[string]any `json:"input"`
}

type mcpDesiredStatePlan struct {
	PlanID            string                `json:"plan_id"`
	PlanDigest        string                `json:"plan_digest"`
	CapabilityID      string                `json:"capability_id"`
	Goal              string                `json:"goal"`
	Operations        []mcpOperationRef     `json:"operations"`
	ResourceRefs      []mcpauth.ResourceRef `json:"resource_refs"`
	ExpectedRevisions map[string]string     `json:"expected_revisions"`
	BlastRadius       map[string]any        `json:"blast_radius"`
	RiskClass         int                   `json:"risk_class"`
	Approval          map[string]any        `json:"approval"`
	ExternalActions   []any                 `json:"external_actions"`
	Assumptions       []string              `json:"assumptions"`
	Warnings          []string              `json:"warnings"`
	ExpiresAt         string                `json:"expires_at"`
}

func (s *Server) registerMCPTools(server *mcp.Server, principal application.Principal) {
	if s.grantAllowsAccess(principal, mcpauth.AccessRead) {
		s.addMCPTaskTool(server, principal)
	}
	if s.grantAllowsOperate(principal) {
		s.addMCPCommitTaskTool(server, principal)
	}
	s.addMCPDiscoverTool(server, principal)
	s.addMCPGetCapabilitySchemaTool(server, principal)
	if s.grantAllowsAccess(principal, mcpauth.AccessRead) {
		s.addMCPPlanDesiredStateTool(server, principal)
		s.addMCPValidateDesiredStateTool(server, principal)
		s.addMCPGetChangesetTool(server, principal)
		s.addMCPGetWorkflowTool(server, principal)
	}
	if s.grantAllowsOperate(principal) {
		s.addMCPSubmitChangesetTool(server, principal)
		s.addMCPCancelWorkflowTool(server, principal)
		s.addMCPRetryWorkflowStepTool(server, principal)
		s.addMCPRedeemExternalActionTool(server, principal)
	}
	if s.grantAllowsAccess(principal, mcpauth.AccessRead) {
		s.addMCPSystemCapabilitiesTool(server)
		s.addMCPSystemBootstrapTool(server)
	}
	s.registerMCPCapabilityTools(server, principal)
}

type mcpDiscoverInput struct {
	IncludeDenied          *bool  `json:"include_denied,omitempty" jsonschema:"Include capabilities denied by this grant in full detail mode, defaults to false"`
	IncludeSchemaSummaries *bool  `json:"include_schema_summaries,omitempty" jsonschema:"Include strict capability schema summaries, defaults to false"`
	DetailLevel            string `json:"detail_level,omitempty"`
}

func (s *Server) addMCPDiscoverTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_discover", Title: "Discover OBoard", Description: "ADVANCED / FALLBACK TOOL. Routine OBoard operations should use oboard_task first. Return a compact recipe and capability-group index by default; request detail_level=full only when Fast Path returned fallback_required.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"include_denied": map[string]any{"type": "boolean", "default": false}, "include_schema_summaries": map[string]any{"type": "boolean", "default": false}, "detail_level": map[string]any{"type": "string", "enum": []string{"compact", "full"}, "default": "compact"}})),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpDiscoverInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		decision := s.evaluatorForRead(ctx)
		if !decision.Allowed {
			return mcpFailureResult(decision, ""), nil, nil
		}
		detail := strings.ToLower(strings.TrimSpace(input.DetailLevel))
		var data map[string]any
		if detail == "full" {
			data = s.mcpDiscoverData(ctx, boolPtrDefault(input.IncludeDenied, false), boolPtrDefault(input.IncludeSchemaSummaries, false))
		} else {
			data = s.mcpDiscoverCompactData(ctx)
		}
		s.recordToolCall(ctx, principal, "discover", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, newToolEnvelope("succeeded", "", data), nil
	})
}

func boolPtrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func (s *Server) evaluatorForRead(ctx context.Context) mcpauth.AuthorizationDecision {
	grant, err := mcpGrantPrincipal(ctx)
	if err != nil {
		return mcpauth.DenyDecision(mcpauth.CodeInvalidToken, "authenticated OAuth grant is required", false)
	}
	if grant.Grant.RevokedAt != nil || grant.Grant.AccessLevel == "" {
		return mcpauth.DenyDecision(mcpauth.CodeGrantRevoked, "the OAuth grant is not active", false)
	}
	return mcpauth.AuthorizationDecision{Allowed: true, Code: "allowed", Recoverable: true, ApprovalMode: "automatic"}
}

type mcpCapabilitySchemaInput struct {
	CapabilityID string `json:"capability_id"`
}

func (s *Server) addMCPGetCapabilitySchemaTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_get_capability_schema", Title: "Get Capability Schema", Description: "ADVANCED / FALLBACK TOOL. Routine OBoard operations should use oboard_task first. Return one authorized capability's strict schema after Fast Path reports fallback_required.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"capability_id": map[string]any{"type": "string", "minLength": 1}}, "capability_id")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpCapabilitySchemaInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		descriptor, known := s.capabilities.Get(strings.TrimSpace(input.CapabilityID))
		if !known || !descriptor.MCPEnabled {
			return mcpPlainFailureResult("", "capability is not available to this grant"), nil, nil
		}
		// Schema queries must not resolve resource references from input
		// (there is no input yet); only the grant access level and the shared
		// RBAC permission gate the schema itself.
		decision := s.authorizeCapabilitySchema(ctx, descriptor)
		if !decision.Allowed {
			return mcpFailureResult(decision, ""), nil, nil
		}
		s.recordToolCall(ctx, principal, "capabilities.schema", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, newToolEnvelope("succeeded", "", mcpCapabilityView(descriptor)), nil
	})
}

// authorizeCapabilitySchema gates schema discovery by grant liveness, the
// grant access level, and the shared RBAC permission — without resolving
// resource references from operation input.
func (s *Server) authorizeCapabilitySchema(ctx context.Context, descriptor capability.Descriptor) mcpauth.AuthorizationDecision {
	grant, err := mcpGrantPrincipal(ctx)
	if err != nil {
		return mcpauth.DenyDecision(mcpauth.CodeInvalidToken, "authenticated OAuth grant is required", false)
	}
	if grant.Grant.RevokedAt != nil {
		return mcpauth.DenyDecision(mcpauth.CodeGrantRevoked, "the OAuth grant has been revoked", false)
	}
	if grant.Grant.ExpiresAt != nil && time.Now().After(*grant.Grant.ExpiresAt) {
		return mcpauth.DenyDecision(mcpauth.CodeExpired, "the OAuth grant has expired", false)
	}
	if !grant.Grant.AccessLevel.Allows(descriptor.MinimumAccess) {
		return mcpauth.DenyScope(descriptor.MinimumAccess)
	}
	if !s.capabilities.RBAC().Allows(grant.Role, descriptor.RBACPermission) {
		return mcpauth.DenyRole(descriptor.RBACPermission)
	}
	return mcpauth.AllowDecision(authorization.ApprovalRequired, descriptor.RiskClass)
}

func (s *Server) authorizeCapability(ctx context.Context, descriptor capability.Descriptor, input any) mcpauth.AuthorizationDecision {
	grant, err := mcpGrantPrincipal(ctx)
	if err != nil {
		return mcpauth.DenyDecision(mcpauth.CodeInvalidToken, "authenticated OAuth grant is required", false)
	}
	return s.mcpEvaluator().Authorize(ctx, grant, s.capabilitySpec(descriptor), input)
}

// authorizePlanOperation authorizes one operation inside a plan/validate step.
// Planning is non-persistent, so the capability's execution MinimumAccess is
// checked exactly like execution; the plan output carries the required access
// so a read grant can prepare only read-authorized operations.
func (s *Server) authorizePlanOperation(ctx context.Context, capabilityName string, input map[string]any) mcpauth.AuthorizationDecision {
	descriptor, known := s.capabilities.Get(capabilityName)
	if !known || !descriptor.MCPEnabled || !descriptor.Executable {
		return mcpauth.DenyDecision(mcpauth.CodeNotFound, "capability is not executable through MCP", false)
	}
	return s.authorizeCapability(ctx, descriptor, input)
}

type mcpPlanDesiredStateInput struct {
	CapabilityID      string            `json:"capability_id"`
	Goal              string            `json:"goal"`
	DesiredState      map[string]any    `json:"desired_state"`
	Targets           []mcpTargetRef    `json:"targets,omitempty"`
	ExpectedRevisions map[string]string `json:"expected_revisions,omitempty"`
	Assumptions       []string          `json:"assumptions,omitempty"`
}

type mcpTargetRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (s *Server) addMCPPlanDesiredStateTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_plan_desired_state", Title: "Plan Desired State", Description: "ADVANCED / FALLBACK TOOL. Routine OBoard operations should use oboard_task first. Build a non-persistent desired-state plan for one authorized capability when no Fast Path recipe applies.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"capability_id":      map[string]any{"type": "string"},
			"goal":               map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
			"desired_state":      map[string]any{"type": "object"},
			"targets":            map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"type": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}}, "required": []any{"type", "id"}}},
			"expected_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"assumptions":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "capability_id", "goal", "desired_state")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpPlanDesiredStateInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		decision := s.authorizePlanOperation(ctx, strings.TrimSpace(input.CapabilityID), input.DesiredState)
		if !decision.Allowed {
			return mcpFailureResult(decision, ""), nil, nil
		}
		plan, err := s.buildDesiredStatePlan(ctx, principal, input)
		if err != nil {
			s.recordToolCall(ctx, principal, "desired_state.plan", input, "failed", capability.DataInternal)
			return mcpFailureResult(mcpauth.DenyDecision(mcpauth.CodeInvalidInput, err.Error(), false), ""), nil, nil
		}
		s.recordToolCall(ctx, principal, "desired_state.plan", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, newToolEnvelope("planned", "", plan), nil
	})
}

func (s *Server) buildDesiredStatePlan(ctx context.Context, principal application.Principal, input mcpPlanDesiredStateInput) (*mcpDesiredStatePlan, error) {
	capabilityName := strings.TrimSpace(input.CapabilityID)
	operation := mcpOperationRef{Capability: capabilityName, Input: input.DesiredState}
	rawInput, err := json.Marshal(input.DesiredState)
	if err != nil {
		return nil, err
	}
	revisions, err := json.Marshal(input.ExpectedRevisions)
	if err != nil {
		return nil, err
	}
	validated, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{BaseRevisions: revisions, Operations: []automation.OperationRequest{{Capability: capabilityName, Input: rawInput}}})
	if err != nil {
		return nil, err
	}
	descriptor, _ := s.capabilities.Get(capabilityName)
	refs := []mcpauth.ResourceRef{}
	if descriptor.ResolveResourceRefs != nil {
		if resolved, resolveErr := descriptor.ResolveResourceRefs(ctx, input.DesiredState); resolveErr == nil {
			refs = resolved
		}
	}
	grantPrincipal, grantErr := mcpGrantPrincipal(ctx)
	approvalMode := "automatic"
	if grantErr == nil {
		decision := s.authorizePlanOperation(ctx, capabilityName, input.DesiredState)
		approvalMode = decision.ApprovalMode
		if approvalMode == "" {
			approvalMode = "required"
		}
		_ = grantPrincipal
	}
	id, _ := security.RandomToken(18)
	plan := &mcpDesiredStatePlan{
		PlanID: "plan_" + id, PlanDigest: mcpPlanDigest(capabilityName, []mcpOperationRef{operation}, validated.ExpectedRevisions), CapabilityID: capabilityName,
		Goal: input.Goal, Operations: []mcpOperationRef{operation}, ResourceRefs: refs,
		ExpectedRevisions: validated.ExpectedRevisions, RiskClass: validated.RiskClass,
		Approval:        map[string]any{"mode": approvalMode, "max_auto_approve_risk": s.grantApprovalMaxRisk(ctx), "plan_hash": validated.PlanHash},
		ExternalActions: []any{}, Assumptions: mcpStringSlice(input.Assumptions), Warnings: mcpStringSlice(validated.Warnings),
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339Nano),
	}
	return plan, nil
}

func (s *Server) grantApprovalMaxRisk(ctx context.Context) int {
	grant, err := mcpGrantPrincipal(ctx)
	if err != nil {
		return 0
	}
	return grant.Grant.ApprovalMaxRisk
}

type mcpValidateDesiredStateInput struct {
	Plan             map[string]any `json:"plan"`
	PlanDigest       string         `json:"plan_digest"`
	RefreshRevisions *bool          `json:"refresh_revisions,omitempty"`
}

func (s *Server) addMCPValidateDesiredStateTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_validate_desired_state", Title: "Validate Desired State", Description: "ADVANCED / FALLBACK TOOL. Routine OBoard operations should use oboard_task first. Validate a client-carried desired-state plan when Fast Path cannot represent the operation.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"plan":              map[string]any{"type": "object"},
			"plan_digest":       map[string]any{"type": "string", "minLength": 1},
			"refresh_revisions": map[string]any{"type": "boolean"},
		}, "plan", "plan_digest")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpValidateDesiredStateInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		plan, err := parseDesiredStatePlan(input.Plan)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil, nil
		}
		if mcpPlanDigest(plan.CapabilityID, plan.Operations, plan.ExpectedRevisions) != input.PlanDigest {
			return mcpFailureResult(mcpauth.DenyDecision(mcpauth.CodeInvalidInput, "plan_digest does not match the plan content", false), ""), nil, nil
		}
		for _, operation := range plan.Operations {
			decision := s.authorizePlanOperation(ctx, operation.Capability, operation.Input)
			if !decision.Allowed {
				return mcpFailureResult(decision, ""), nil, nil
			}
		}
		result, err := s.validatePlanRevisions(ctx, principal, plan)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil, nil
		}
		s.recordToolCall(ctx, principal, "desired_state.validate", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, newToolEnvelope("succeeded", "", result), nil
	})
}

func parseDesiredStatePlan(raw map[string]any) (*mcpDesiredStatePlan, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("plan must be a JSON object")
	}
	var plan mcpDesiredStatePlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return nil, errors.New("plan has an invalid shape")
	}
	if len(plan.Operations) == 0 || len(plan.Operations) > 64 {
		return nil, errors.New("plan must contain between 1 and 64 operations")
	}
	for _, operation := range plan.Operations {
		if strings.TrimSpace(operation.Capability) == "" || operation.Input == nil {
			return nil, errors.New("plan operations must include capability and input")
		}
	}
	return &plan, nil
}

func (s *Server) validatePlanRevisions(ctx context.Context, principal application.Principal, plan *mcpDesiredStatePlan) (map[string]any, error) {
	operations := make([]automation.OperationRequest, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		rawInput, err := json.Marshal(operation.Input)
		if err != nil {
			return nil, err
		}
		operations = append(operations, automation.OperationRequest{Capability: operation.Capability, Input: rawInput})
	}
	base, err := json.Marshal(plan.ExpectedRevisions)
	if err != nil {
		return nil, err
	}
	validated, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{BaseRevisions: base, Operations: operations})
	if err != nil {
		return nil, err
	}
	conflicts := []string{}
	for key, expected := range plan.ExpectedRevisions {
		if current, ok := validated.ExpectedRevisions[key]; ok && current != expected {
			conflicts = append(conflicts, key)
		}
	}
	requiredApprovals := []map[string]any{}
	for _, operation := range plan.Operations {
		descriptor, known := s.capabilities.Get(operation.Capability)
		if !known {
			continue
		}
		if descriptor.ApprovalPolicy == "required" && descriptor.RiskClass >= 4 {
			requiredApprovals = append(requiredApprovals, map[string]any{"capability": operation.Capability, "reason": "risk_4_requires_admin"})
		}
	}
	return map[string]any{
		"valid":                 validated.Valid,
		"validation_digest":     mcpPlanDigest(plan.CapabilityID, plan.Operations, plan.ExpectedRevisions),
		"plan_hash":             validated.PlanHash,
		"digest_hint":           "pass validation_digest to oboard_submit_changeset together with the exact validated_plan object returned here",
		"normalized_operations": len(validated.Evidence),
		"revision_conflicts":    conflicts,
		"policy_violations":     []string{},
		"warnings":              mcpStringSlice(validated.Warnings),
		"required_approvals":    requiredApprovals,
		"expires_at":            time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339Nano),
	}, nil
}

type mcpSubmitChangesetInput struct {
	ValidatedPlan      map[string]any `json:"validated_plan"`
	ValidationDigest   string         `json:"validation_digest"`
	IdempotencyKey     string         `json:"idempotency_key"`
	Reason             string         `json:"reason"`
	ApprovalPreference string         `json:"approval_preference,omitempty"`
}

func (s *Server) addMCPSubmitChangesetTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_submit_changeset", Title: "Submit Changeset", Description: "ADVANCED / FALLBACK TOOL. Routine OBoard operations should use oboard_task then oboard_commit_task. Submit a client-carried validated plan through the same Changeset and Workflow service used by Fast Path.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"validated_plan":      map[string]any{"type": "object"},
			"validation_digest":   map[string]any{"type": "string", "minLength": 1},
			"idempotency_key":     map[string]any{"type": "string", "minLength": 8, "maxLength": 200},
			"reason":              map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
			"approval_preference": map[string]any{"type": "string", "enum": []string{"request_approval", "use_preapproval_if_available"}},
		}, "validated_plan", "validation_digest", "idempotency_key", "reason")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotationsWrite(true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpSubmitChangesetInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		result, err := s.submitMCPChangeset(ctx, principal, input)
		if err != nil {
			return mcpPlainFailureResult("", err.Error()), nil, nil
		}
		return &mcp.CallToolResult{}, result, nil
	})
}

func (s *Server) submitMCPChangeset(ctx context.Context, principal application.Principal, input mcpSubmitChangesetInput) (*ToolEnvelope, error) {
	plan, err := parseDesiredStatePlan(input.ValidatedPlan)
	if err != nil {
		return nil, err
	}
	if mcpPlanDigest(plan.CapabilityID, plan.Operations, plan.ExpectedRevisions) != input.ValidationDigest {
		return nil, errors.New("validation_digest does not match the validated plan")
	}
	for _, operation := range plan.Operations {
		decision := s.authorizePlanOperation(ctx, operation.Capability, operation.Input)
		if !decision.Allowed {
			envelope := errorEnvelope("", decision, "", "")
			return envelope, errors.New(decision.Reason)
		}
	}
	result, err := s.submitPreparedOperations(ctx, principal, plan.Operations, plan.ExpectedRevisions, input.Reason, input.IdempotencyKey, input.ApprovalPreference)
if err == nil {
		s.recordToolCall(ctx, principal, "changesets.submit", input, result.Status, capability.DataInternal)
	}
	return result, err
}

func (s *Server) submitPreparedOperations(ctx context.Context, principal application.Principal, planOperations []mcpOperationRef, expectedRevisions map[string]string, reason, idempotencyKey, approvalPreference string) (*ToolEnvelope, error) {
	mode := strings.TrimSpace(approvalPreference)
	if mode == "" {
		mode = "request_approval"
	}
	if mode != "request_approval" && mode != "use_preapproval_if_available" {
		return nil, errors.New("approval_preference must be request_approval or use_preapproval_if_available")
	}
	base, err := json.Marshal(expectedRevisions)
	if err != nil {
		return nil, err
	}
	operations := make([]automation.OperationRequest, 0, len(planOperations))
	kind := "changeset"
	for _, operation := range planOperations {
		decision := s.authorizePlanOperation(ctx, operation.Capability, operation.Input)
		if !decision.Allowed {
			return errorEnvelope("", decision, "", ""), errors.New(decision.Reason)
		}
		rawInput, marshalErr := json.Marshal(operation.Input)
		if marshalErr != nil {
			return nil, marshalErr
		}
		operations = append(operations, automation.OperationRequest{Capability: operation.Capability, Input: rawInput})
		if operation.Capability == "servers.onboard" || operation.Capability == "servers.enrollment.issue" {
			kind = "server_onboarding"
		} else if operation.Capability == "deployments.apply" {
			kind = "deployment"
		} else if operation.Capability == "subscription_plans.nodes.update" {
			kind = "access_change"
		}
	}
	item, err := s.automation.Create(ctx, principal, automation.CreateRequest{Reason: reason, IdempotencyKey: idempotencyKey, BaseRevisions: base, AutoApply: mode == "use_preapproval_if_available", Operations: operations})
	if err != nil {
		return nil, err
	}
	if item.Status == model.ChangesetDraft || item.Status == model.ChangesetValidated || item.Status == model.ChangesetAwaitingApproval {
		item, err = s.automation.Validate(ctx, principal, item.ID)
	}
	if err != nil {
		return nil, err
	}
	workflowKeySum := sha256.Sum256([]byte(principal.ID + "\x00" + idempotencyKey))
	externalActionPending := s.planHasExternalAction(item)
	workflow, err := s.automation.StartWorkflow(ctx, principal, automation.StartWorkflowRequest{Kind: kind, Reason: reason, IdempotencyKey: "submit:" + hex.EncodeToString(workflowKeySum[:16]), ChangesetID: item.ID, ExternalAction: externalActionPending})
	if err != nil {
		return nil, err
	}
	workflow, err = s.automation.GetWorkflow(ctx, principal, workflow.ID)
	if err != nil {
		return nil, err
	}
	actionID := ""
	if s.planHasExternalAction(item) {
		actionID, err = s.storeOneTimeExternalAction(ctx, principal, workflow, item)
		if err != nil {
			return nil, err
		}
	}
	data := map[string]any{
		"changeset_status": item.Status, "risk_class": item.RiskClass, "plan_hash": item.PlanHash,
		"validation": mcpChangesetValidationSummary(item.Validation), "workflow": workflow,
	}
	status := workflowResultStatus(workflow.Status)
	result := newToolEnvelope(status, "", data)
	result.WorkflowID = workflow.ID
	result.ChangesetID = item.ID
	if actionID != "" {
		result.NextAction = map[string]any{"type": "redeem_external_action", "action_id": actionID, "workflow_id": workflow.ID, "sensitive": true, "must_not_log": true}
	} else if workflow.Status == model.WorkflowApprovalRequired {
		result.NextAction = map[string]any{"type": "open_approval", "changeset_id": item.ID}
	}
	return result, nil
}

func (s *Server) planHasExternalAction(item *model.AutomationChangeset) bool {
	var result struct {
		Operations []map[string]any `json:"operations"`
	}
	if json.Unmarshal(item.Result, &result) != nil {
		return false
	}
	for _, operation := range result.Operations {
		if token, _ := operation["enrollment_token"].(string); token != "" {
			return true
		}
		if token, _ := operation["api_token"].(string); token != "" {
			return true
		}
	}
	return false
}

// storeOneTimeExternalAction extracts one-time material from the applied
// changeset result, stores it encrypted, and returns the action ID. The secret
// never enters the workflow, resources, or logs.
func (s *Server) storeOneTimeExternalAction(ctx context.Context, principal application.Principal, workflow *model.AutomationWorkflow, item *model.AutomationChangeset) (string, error) {
	if existing, err := s.store.FindExternalActionByWorkflow(ctx, workflow.ID); err == nil && existing.GrantID == principal.GrantID && existing.ConsumedAt == nil && existing.ExpiresAt.After(time.Now().UTC()) {
		return existing.ID, nil
	}
	var result struct {
		Operations []map[string]any `json:"operations"`
	}
	if json.Unmarshal(item.Result, &result) != nil {
		return "", nil
	}
	for _, operation := range result.Operations {
		token, _ := operation["api_token"].(string)
		tokenInfo, _ := operation["token_info"].(map[string]any)
		if token == "" || tokenInfo == nil {
			continue
		}
		action := map[string]any{
			"type": "present_secret", "title": "OBoard 服务账号访问令牌",
			"token": token, "token_info": tokenInfo,
			"sensitive": true, "must_not_log": true,
		}
		encoded, err := json.Marshal(action)
		if err != nil {
			return "", err
		}
		encrypted, err := security.EncryptSecret(s.sessionSecret, "external-action", string(encoded))
		if err != nil {
			return "", err
		}
		id, err := security.RandomToken(18)
		if err != nil {
			return "", err
		}
		expiresAt := time.Now().UTC().Add(10 * time.Minute)
		if expires, _ := tokenInfo["expires_at"].(string); expires != "" {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, expires); parseErr == nil && parsed.Before(expiresAt) {
				expiresAt = parsed
			}
		}
		actionID := "ext_" + id
		if err := s.store.CreateExternalAction(ctx, &store.ExternalAction{ID: actionID, GrantID: principal.GrantID, WorkflowID: workflow.ID, Kind: "present_secret", Payload: encrypted, ExpiresAt: expiresAt}); err != nil {
			if existing, findErr := s.store.FindExternalActionByWorkflow(ctx, workflow.ID); findErr == nil && existing.GrantID == principal.GrantID && existing.ConsumedAt == nil && existing.ExpiresAt.After(time.Now().UTC()) {
				return existing.ID, nil
			}
			return "", err
		}
		return actionID, nil
	}
	for _, operation := range result.Operations {
		token, _ := operation["enrollment_token"].(string)
		server, _ := operation["server"].(map[string]any)
		if token == "" || server == nil {
			continue
		}
		installBBR := agentInstallBBRValue(false)
		if enabled, _ := server["bbr_enabled"].(bool); enabled {
			installBBR = agentInstallBBRValue(true)
		}
		base, err := s.publicBaseURL(ctx)
		if err != nil {
			return "", err
		}
		action := map[string]any{
			"type": "execute_on_target", "title": "安装 OBoard Agent",
			"command":     agentInstallCommand(base, installBBR),
			"environment": map[string]any{"OBOARD_ENROLL_TOKEN": token, "OBOARD_INSTALL_BBR": installBBR},
			"expires_at":  operation["enrollment_expires_at"],
			"sensitive":   true, "must_not_log": true,
			"completion_condition": map[string]any{"resource_uri": fmt.Sprintf("oboard://servers/%v/health", server["id"]), "field": "agent_connected", "equals": true},
		}
		encoded, err := json.Marshal(action)
		if err != nil {
			return "", err
		}
		encrypted, err := security.EncryptSecret(s.sessionSecret, "external-action", string(encoded))
		if err != nil {
			return "", err
		}
		id, err := security.RandomToken(18)
		if err != nil {
			return "", err
		}
		expires, _ := operation["enrollment_expires_at"].(string)
		expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
		if parsed, parseErr := time.Parse(time.RFC3339Nano, expires); parseErr == nil {
			expiresAt = parsed
		}
		actionID := "ext_" + id
		if err := s.store.CreateExternalAction(ctx, &store.ExternalAction{ID: actionID, GrantID: principal.GrantID, WorkflowID: workflow.ID, Kind: "execute_on_target", Payload: encrypted, ExpiresAt: expiresAt}); err != nil {
			if existing, findErr := s.store.FindExternalActionByWorkflow(ctx, workflow.ID); findErr == nil && existing.GrantID == principal.GrantID && existing.ConsumedAt == nil && existing.ExpiresAt.After(time.Now().UTC()) {
				return existing.ID, nil
			}
			return "", err
		}
		return actionID, nil
	}
	for _, operation := range result.Operations {
		token, _ := operation["enrollment_token"].(string)
		if token == "" {
			continue
		}
		relayID, relayName := int64(0), ""
		if relay, ok := operation["subscription_relay"].(map[string]any); ok {
			if id, ok := relay["id"].(float64); ok {
				relayID = int64(id)
			}
			relayName, _ = relay["name"].(string)
		} else if id, ok := operation["relay_id"].(float64); ok {
			relayID = int64(id)
		}
		if relayID == 0 {
			continue
		}
		base, err := s.publicBaseURL(ctx)
		if err != nil {
			return "", err
		}
		releaseVersion := version.Version
		if strings.Contains(releaseVersion, "dev") {
			releaseVersion = "dev"
		}
		action := map[string]any{
			"type": "execute_on_target", "title": "安装 OBoard 订阅中继",
			"command":     "curl -fsSL " + shellSingleQuote(strings.TrimRight(base, "/")+"/install/subscription-relay.sh") + ` | env VERSION=` + shellSingleQuote(releaseVersion) + ` OBOARD_CONTROLLER_URL=` + shellSingleQuote(strings.TrimRight(base, "/")) + ` OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN="$OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN" /bin/sh`,
			"environment": map[string]any{"VERSION": releaseVersion, "OBOARD_CONTROLLER_URL": strings.TrimRight(base, "/"), "OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN": token},
			"relay_id":    relayID, "relay_name": relayName, "expires_at": operation["enrollment_expires_at"],
			"sensitive": true, "must_not_log": true,
		}
		encoded, err := json.Marshal(action)
		if err != nil {
			return "", err
		}
		encrypted, err := security.EncryptSecret(s.sessionSecret, "external-action", string(encoded))
		if err != nil {
			return "", err
		}
		id, err := security.RandomToken(18)
		if err != nil {
			return "", err
		}
		expires, _ := operation["enrollment_expires_at"].(string)
		expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
		if parsed, parseErr := time.Parse(time.RFC3339Nano, expires); parseErr == nil {
			expiresAt = parsed
		}
		actionID := "ext_" + id
		if err := s.store.CreateExternalAction(ctx, &store.ExternalAction{ID: actionID, GrantID: principal.GrantID, WorkflowID: workflow.ID, Kind: "execute_on_target", Payload: encrypted, ExpiresAt: expiresAt}); err != nil {
			if existing, findErr := s.store.FindExternalActionByWorkflow(ctx, workflow.ID); findErr == nil && existing.GrantID == principal.GrantID && existing.ConsumedAt == nil && existing.ExpiresAt.After(time.Now().UTC()) {
				return existing.ID, nil
			}
			return "", err
		}
		return actionID, nil
	}
	return "", nil
}

type mcpChangesetIDInput struct {
	ChangesetID string `json:"changeset_id"`
}

func (s *Server) addMCPGetChangesetTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_get_changeset", Title: "Get Changeset", Description: "Return one authorized Changeset with validation, operations, expected revisions, approvals, workflow reference, and redacted results.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"changeset_id": map[string]any{"type": "string", "minLength": 1}}, "changeset_id")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpChangesetIDInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		item, err := s.automation.Get(ctx, strings.TrimSpace(input.ChangesetID))
		if err != nil || item.PrincipalID != principal.ID {
			return mcpPlainFailureResult("", "changeset not found"), nil, nil
		}
		s.recordToolCall(ctx, principal, "changesets.get", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, newToolEnvelope("succeeded", "", mcpChangesetView(item)), nil
	})
}

type mcpWorkflowIDInput struct {
	WorkflowID string `json:"workflow_id"`
	Detail     string `json:"detail,omitempty"`
}

func (s *Server) addMCPGetWorkflowTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_get_workflow", Title: "Get Workflow", Description: "Return a compact authorized Workflow status by default. Use detail=full only for recovery or debugging. Workflow is the canonical execution state; do not infer completion from Changeset creation.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"workflow_id": map[string]any{"type": "string", "minLength": 1}, "detail": map[string]any{"type": "string", "enum": []string{"compact", "full"}, "default": "compact"}}, "workflow_id")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpWorkflowIDInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		item, err := s.automation.GetWorkflow(ctx, principal, strings.TrimSpace(input.WorkflowID))
		if err != nil {
			return mcpPlainFailureResult("", "workflow not found"), nil, nil
		}
		s.recordToolCall(ctx, principal, "workflows.get", input, "succeeded", capability.DataInternal)
		data := any(workflowResourceSummary(item))
		if strings.EqualFold(strings.TrimSpace(input.Detail), "full") {
			data = workflowResourceView(item)
		}
		if prepared, findErr := s.store.FindMCPPreparedPlanByWorkflow(ctx, item.ID, principal.ID, principal.GrantID); findErr == nil {
			metricResult := newToolEnvelope(workflowResultStatus(item.Status), "", map[string]any{"intent": prepared.RecipeID, "recipe_version": prepared.RecipeVersion})
			metricResult.WorkflowID = item.ID
			metricResult.ChangesetID = item.ChangesetID
			s.recordFastPathMetric(ctx, principal, metricResult, "workflow", 0)
		}
		envelope := newToolEnvelope(workflowResultStatus(item.Status), "", data)
		if actionID, actionErr := s.ensureWorkflowExternalAction(ctx, principal, item); actionErr != nil {
			return mcpPlainFailureResult("", "workflow external action is unavailable"), nil, nil
		} else if actionID != "" {
			envelope.NextAction = map[string]any{"type": "redeem_external_action", "action_id": actionID, "workflow_id": item.ID, "sensitive": true, "must_not_log": true}
		}
		return &mcp.CallToolResult{}, envelope, nil
	})
}

func (s *Server) ensureWorkflowExternalAction(ctx context.Context, principal application.Principal, workflow *model.AutomationWorkflow) (string, error) {
	if workflow == nil || workflow.Status != model.WorkflowExternalActionRequired || workflow.ChangesetID == "" {
		return "", nil
	}
	if existing, err := s.store.FindExternalActionByWorkflow(ctx, workflow.ID); err == nil && existing.GrantID == principal.GrantID && existing.ConsumedAt == nil && existing.ExpiresAt.After(time.Now().UTC()) {
		return existing.ID, nil
	}
	changeset, err := s.automation.Get(ctx, workflow.ChangesetID)
	if err != nil || changeset.PrincipalID != principal.ID {
		return "", err
	}
	if !s.planHasExternalAction(changeset) {
		return "", nil
	}
	return s.storeOneTimeExternalAction(ctx, principal, workflow, changeset)
}

type mcpCancelWorkflowInput struct {
	WorkflowID     string `json:"workflow_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason"`
}

func (s *Server) addMCPCancelWorkflowTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_cancel_workflow", Title: "Cancel Workflow", Description: "Request cancellation of an authorized cancellable Workflow. Cancellation is idempotent and never claims rollback of operations that have already completed.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"workflow_id": map[string]any{"type": "string", "minLength": 1}, "idempotency_key": map[string]any{"type": "string", "minLength": 8, "maxLength": 200}, "reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}}, "workflow_id", "idempotency_key", "reason")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotationsWrite(true),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpCancelWorkflowInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		item, err := s.cancelWorkflow(ctx, principal, strings.TrimSpace(input.WorkflowID))
		if err != nil {
			return mcpPlainFailureResult("", "workflow cannot be cancelled in its current state"), nil, nil
		}
		s.recordToolCall(ctx, principal, "workflows.cancel", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, newToolEnvelope(workflowResultStatus(item.Status), "", item), nil
	})
}

func (s *Server) cancelWorkflow(ctx context.Context, principal application.Principal, workflowID string) (*model.AutomationWorkflow, error) {
	workflow, err := s.automation.GetWorkflow(ctx, principal, workflowID)
	if err != nil {
		return nil, err
	}
	if workflow.Kind != "access_change" || workflow.Status != model.WorkflowFailed {
		return s.automation.CancelWorkflow(ctx, principal, workflowID)
	}
	changeID := mcpWorkflowAccessChangeID(workflow)
	if changeID == 0 {
		return nil, errors.New("access change workflow has no associated access change id")
	}
	change, err := s.store.GetAccessChange(ctx, changeID)
	if err != nil {
		return nil, err
	}
	if !s.accessChangeAbandonable(ctx, change) {
		return nil, errors.New("failed access change cannot be abandoned after activation")
	}
	if err := s.store.MarkAccessChangeCancelled(ctx, changeID); err != nil {
		return nil, err
	}
	return s.automation.AbandonFailedWorkflow(ctx, principal, workflowID)
}

type mcpRetryWorkflowInput struct {
	WorkflowID string `json:"workflow_id"`
	StepID     string `json:"step_id"`
}

func (s *Server) addMCPRetryWorkflowStepTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_retry_workflow_step", Title: "Retry Workflow Step", Description: "Retry one authorized retryable Workflow step using the original validated operation and resource boundary. The retry cannot change targets, broaden permissions, or bypass revision and approval checks.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"workflow_id": map[string]any{"type": "string", "minLength": 1}, "step_id": map[string]any{"type": "string", "minLength": 1}}, "workflow_id", "step_id")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotationsWrite(false),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpRetryWorkflowInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		workflowID, stepID := strings.TrimSpace(input.WorkflowID), strings.TrimSpace(input.StepID)
		workflow, err := s.automation.GetWorkflow(ctx, principal, workflowID)
		if err != nil {
			return mcpPlainFailureResult("", "workflow not found"), nil, nil
		}
		// Access-change releases (套餐发布) fail durably (for example with a
		// transient SQLite busy error) and are always retryable: the Controller
		// resumes them from the durable failure point and the worker continues
		// the prepare -> activate -> finalize state machine.
		if workflow.Kind == "access_change" && workflow.Status == model.WorkflowFailed {
			changeID := mcpWorkflowAccessChangeID(workflow)
			if changeID == 0 {
				return mcpPlainFailureResult("", "access change workflow has no associated access change id"), nil, nil
			}
			phase, queued, retryErr := s.retryAccessChange(ctx, changeID)
			if retryErr != nil {
				if strings.Contains(strings.ToLower(retryErr.Error()), "only failed access changes") {
					return mcpPlainFailureResult("", "套餐发布不在可重试状态：仅失败状态的套餐发布可以重试（可能已被取消或已完成）。可读取 oboard://access-changes/"+strconv.FormatInt(changeID, 10)+" 查看当前状态"), nil, nil
				}
				return mcpPlainFailureResult("", "套餐发布重试失败："+retryErr.Error()), nil, nil
			}
			// Re-queue the workflow step without relying on the persisted
			// retryable flag (releases are always durable-retryable), so the
			// next status read synchronizes it to waiting_for_agent while the
			// worker resumes.
			if _, retryErr := s.automation.ResetWorkflowForRetry(ctx, principal, workflowID, stepID); retryErr != nil {
				if _, retryErr = s.automation.RetryWorkflowStep(ctx, principal, workflowID, stepID); retryErr != nil {
					return mcpPlainFailureResult("", "套餐发布已恢复，但工作流步骤状态重置失败，请重新读取工作流"), nil, nil
				}
			}
			refreshed, _ := s.automation.GetWorkflow(ctx, principal, workflowID)
			if refreshed == nil {
				refreshed = workflow
			}
			view := workflowResourceView(refreshed)
			view["retry_summary"] = map[string]any{
				"message":       "套餐发布已重新排队，正在继续执行",
				"phase":         phase,
				"queued_tasks":  queued,
				"access_change": changeID,
			}
			envelope := newToolEnvelope(workflowResultStatus(refreshed.Status), "", view)
			envelope.WorkflowID = refreshed.ID
			envelope.ChangesetID = refreshed.ChangesetID
			envelope.Retryable = true
			envelope.NextAction = map[string]any{"type": "wait", "resource_type": "access_change", "resource_id": changeID, "phase": phase, "queued_tasks": queued}
			s.recordToolCall(ctx, principal, "workflows.retry:access_change", map[string]any{"workflow_id": workflowID, "access_change_id": changeID, "phase": phase}, "succeeded", capability.DataInternal)
			return &mcp.CallToolResult{}, envelope, nil
		}
		item, err := s.automation.RetryWorkflowStep(ctx, principal, workflowID, stepID)
		if err != nil {
			return mcpPlainFailureResult("", "workflow step is not retryable"), nil, nil
		}
		s.recordToolCall(ctx, principal, "workflows.retry", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, newToolEnvelope(workflowResultStatus(item.Status), "", item), nil
	})
}

// mcpWorkflowAccessChangeID extracts the access_change id from a workflow's
// affected resources.
func mcpWorkflowAccessChangeID(workflow *model.AutomationWorkflow) int64 {
	if workflow == nil {
		return 0
	}
	var affected []map[string]any
	if json.Unmarshal(workflow.AffectedResources, &affected) != nil {
		return 0
	}
	for _, resource := range affected {
		if fmt.Sprint(resource["type"]) == "access_change" {
			if id, ok := resource["id"].(float64); ok && id > 0 {
				return int64(id)
			}
		}
	}
	return 0
}

type mcpRedeemExternalActionInput struct {
	ActionID string `json:"action_id"`
}

func (s *Server) addMCPRedeemExternalActionTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_redeem_external_action", Title: "Redeem External Action", Description: "Redeem one authorized, pending, one-time external action produced by an OBoard Workflow. Sensitive material is returned at most once, is never exposed through resources, and must not be logged or persisted by the client.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"action_id": map[string]any{"type": "string", "minLength": 1}}, "action_id")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotationsWrite(false),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input mcpRedeemExternalActionInput) (*mcp.CallToolResult, any, error) {
		principal, _ := s.mcpPrincipalFromRequest(ctx, request)
		actionID := strings.TrimSpace(input.ActionID)
		action, err := s.store.GetExternalAction(ctx, actionID)
		if err != nil || action.GrantID != principal.GrantID || action.ConsumedAt != nil {
			return mcpFailureResult(mcpauth.DenyDecision(mcpauth.CodeNotFound, "external action not found or already redeemed", false), ""), nil, nil
		}
		redeemed, err := s.store.ConsumeExternalAction(ctx, actionID, time.Now().UTC())
		if err != nil {
			if errors.Is(err, store.ErrExternalActionConsumed) {
				return mcpFailureResult(mcpauth.DenyDecision(mcpauth.CodeAlreadyConsumed, "external action already consumed", false), ""), nil, nil
			}
			if errors.Is(err, store.ErrExternalActionExpired) {
				return mcpFailureResult(mcpauth.DenyDecision(mcpauth.CodeExpired, "external action expired", false), ""), nil, nil
			}
			return mcpPlainFailureResult("", "external action is unavailable"), nil, nil
		}
		payload := map[string]any{}
		if plaintext, decryptErr := security.DecryptSecret(s.sessionSecret, "external-action", redeemed.Payload); decryptErr == nil {
			_ = json.Unmarshal([]byte(plaintext), &payload)
		}
		if len(payload) == 0 {
			return mcpPlainFailureResult("", "external action payload is unavailable"), nil, nil
		}
		envelope := newToolEnvelope("succeeded", "", map[string]any{"action": payload, "consumed": true})
		envelope.OperationID = actionID
		s.recordToolCall(ctx, principal, "external_actions.redeem", map[string]string{"action_id": actionID}, "succeeded", capability.DataSensitive)
		return &mcp.CallToolResult{}, envelope, nil
	})
}

func mcpPlanDigest(capabilityID string, operations []mcpOperationRef, expectedRevisions map[string]string) string {
	payload := struct {
		CapabilityID      string            `json:"capability_id"`
		Operations        []mcpOperationRef `json:"operations"`
		ExpectedRevisions map[string]string `json:"expected_revisions"`
	}{capabilityID, operations, expectedRevisions}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
