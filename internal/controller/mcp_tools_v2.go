package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

type mcpCapabilitySchemaInput struct {
	Capability string `json:"capability" jsonschema:"Authorized capability name"`
}

type mcpSubmitChangesetInput struct {
	Reason            string                `json:"reason" jsonschema:"Short description of the requested outcome"`
	IdempotencyKey    string                `json:"idempotency_key" jsonschema:"Stable client-generated UUID or equivalent key"`
	ExpectedRevisions map[string]string     `json:"expected_revisions,omitempty" jsonschema:"Expected resource revisions keyed by resource identity"`
	Operations        []mcpOperationRequest `json:"operations" jsonschema:"One to 64 catalog operations"`
	ApplyMode         string                `json:"apply_mode" jsonschema:"plan_only, request_approval, when_preapproved, or apply_after_approval"`
}

type mcpDesiredStateInput struct {
	ExpectedRevisions map[string]string     `json:"expected_revisions,omitempty" jsonschema:"Optional expected resource revisions keyed by resource identity"`
	Operations        []mcpOperationRequest `json:"operations" jsonschema:"One to 64 catalog operations"`
}

type mcpStartDeploymentInput struct {
	ServerIDs         []int64           `json:"server_ids" jsonschema:"One server or the complete server set"`
	Reason            string            `json:"reason" jsonschema:"Short description of the deployment outcome"`
	IdempotencyKey    string            `json:"idempotency_key" jsonschema:"Stable client-generated UUID or equivalent key"`
	ExpectedRevisions map[string]string `json:"expected_revisions,omitempty" jsonschema:"Expected server revisions"`
	ApplyMode         string            `json:"apply_mode" jsonschema:"request_approval, when_preapproved, or apply_after_approval"`
}

type mcpPrepareServerOnboardingInput struct {
	Name           string `json:"name" jsonschema:"Unique server display name"`
	RegionCode     string `json:"region_code,omitempty" jsonschema:"ISO 3166-1 alpha-2 region code"`
	IPStack        string `json:"ip_stack,omitempty" jsonschema:"auto, ipv4_only, ipv6_only, dual_stack, prefer_ipv4, or prefer_ipv6"`
	EntryAddress   string `json:"entry_address,omitempty" jsonschema:"Optional public entry address"`
	BBREnabled     bool   `json:"bbr_enabled,omitempty" jsonschema:"Request one-time BBR and fq setup during first installation"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Stable client-generated UUID or equivalent key"`
	Reason         string `json:"reason" jsonschema:"Short description of the requested outcome"`
	ApplyMode      string `json:"apply_mode" jsonschema:"request_approval or when_preapproved"`
}

type mcpStartWorkflowInput struct {
	ChangesetID    string `json:"changeset_id" jsonschema:"Existing Changeset ID"`
	Kind           string `json:"kind,omitempty" jsonschema:"Workflow kind"`
	Reason         string `json:"reason" jsonschema:"Short workflow reason"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Stable workflow idempotency key"`
}

type mcpWorkflowIDInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Workflow ID"`
}

type mcpRetryWorkflowInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Workflow ID"`
	StepID     string `json:"step_id" jsonschema:"Retryable failed step ID"`
}

type mcpResultEnvelope struct {
	Status            string                `json:"status"`
	WorkflowID        string                `json:"workflow_id"`
	ChangesetID       string                `json:"changeset_id"`
	OperationID       string                `json:"operation_id"`
	AffectedResources []mcpAffectedResource `json:"affected_resources"`
	Warnings          []string              `json:"warnings"`
	NextAction        any                   `json:"next_action"`
	CorrelationID     string                `json:"correlation_id"`
	Retryable         bool                  `json:"retryable"`
	Data              any                   `json:"data"`
}

type mcpAffectedResource struct {
	Type string `json:"type"`
	ID   any    `json:"id"`
}

func (s *Server) addMCPV2Tools(server *mcp.Server) {
	closedWorld := false
	write, destructive := false, true
	mcp.AddTool(server, &mcp.Tool{Name: "oboard_discover", Title: "Discover OBoard", Description: "Return the current OAuth Grant, authorized capabilities, resource boundaries, version, and workflow rules.", OutputSchema: mcpEnvelopeSchema(mcpDiscoverDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpEmptyInput) (*mcp.CallToolResult, any, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		data := map[string]any{"controller_version": version.Version, "grant_id": principal.GrantID, "scopes": principal.Scopes, "resource_filter": principal.ResourceFilter, "capabilities": mcpCapabilityViews(s.capabilities.ListMCP(principal)), "limits": map[string]any{"max_changeset_operations": 64, "changeset_ttl_seconds": 1800}, "workflow_rules": map[string]any{"write_via_changeset": true, "ssh_supported": false, "shell_supported": false, "admin_deletion_supported": false, "risk4_auto_approval": false}}
		result := newMCPEnvelope("succeeded", "", "", data)
		s.recordToolCall(ctx, principal, "discover", nil, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_get_capability_schema", Title: "Get Capability Schema", Description: "Return one authorized catalog descriptor with strict input and output schemas.", OutputSchema: mcpEnvelopeSchema(mcpCapabilityDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpCapabilitySchemaInput) (*mcp.CallToolResult, any, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		descriptor, allowed := s.capabilities.Authorize(principal, strings.TrimSpace(input.Capability))
		if !allowed || !descriptor.MCPEnabled {
			return mcpFailure(errors.New("capability is not available to this grant")), nil, nil
		}
		result := newMCPEnvelope("succeeded", "", "", mcpCapabilityView(descriptor))
		s.recordToolCall(ctx, principal, "capabilities.schema", input, "succeeded", capability.DataInternal)
		return &mcp.CallToolResult{}, result, nil
	})

	addDesiredStateTool := func(name, title, description, status string) {
		mcp.AddTool(server, &mcp.Tool{Name: name, Title: title, Description: description, InputSchema: s.mcpDesiredStateInputSchema(), OutputSchema: mcpEnvelopeSchema(mcpDesiredStateDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpDesiredStateInput) (*mcp.CallToolResult, any, error) {
			principal, err := mcpPrincipal(ctx)
			if err != nil {
				return mcpFailure(err), nil, nil
			}
			operations, _, err := s.mcpAutomationOperations(principal, input.Operations)
			if err != nil {
				return mcpFailure(err), nil, nil
			}
			revisions, err := json.Marshal(input.ExpectedRevisions)
			if err != nil {
				return mcpFailure(err), nil, nil
			}
			validated, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{BaseRevisions: revisions, Operations: operations})
			if err != nil {
				s.recordToolCall(ctx, principal, "desired_state.validate", input, "failed", capability.DataInternal)
				return mcpFailure(err), nil, nil
			}
			data := map[string]any{"valid": validated.Valid, "plan_hash": validated.PlanHash, "risk_class": validated.RiskClass, "expected_revisions": validated.ExpectedRevisions, "validated_operations": len(validated.Evidence), "warnings": validated.Warnings}
			result := newMCPEnvelope(status, "", "", data)
			s.recordToolCall(ctx, principal, "desired_state.validate", input, "succeeded", capability.DataInternal)
			return &mcp.CallToolResult{}, result, nil
		})
	}
	addDesiredStateTool("oboard_plan_desired_state", "Plan Desired State", "Normalize and fully preflight authorized catalog operations in memory without creating a Changeset or writing Controller state.", "planned")
	addDesiredStateTool("oboard_validate_desired_state", "Validate Desired State", "Run the same domain validators and revision resolvers used by Changesets without persisting the draft.", "succeeded")

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_submit_changeset", Title: "Submit Changeset", Description: "Create and validate an idempotent Changeset. when_preapproved applies only when the OAuth Grant's server-side policy authorizes every operation; no input can bypass approval.", InputSchema: s.mcpSubmitInputSchema(), OutputSchema: mcpEnvelopeSchema(mcpChangesetDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: write, IdempotentHint: true, DestructiveHint: &destructive, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpSubmitChangesetInput) (*mcp.CallToolResult, any, error) {
		result, err := s.submitMCPChangeset(ctx, input)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		return &mcp.CallToolResult{}, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_prepare_server_onboarding", Title: "Prepare Server Onboarding", Description: "Create a server through the Changeset engine and, when preauthorized, return a one-time external installation action. OBoard never connects with SSH.", OutputSchema: mcpEnvelopeSchema(mcpChangesetDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: write, IdempotentHint: true, DestructiveHint: &destructive, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpPrepareServerOnboardingInput) (*mcp.CallToolResult, any, error) {
		mode := strings.TrimSpace(input.ApplyMode)
		if mode == "" {
			mode = "when_preapproved"
		}
		if mode != "request_approval" && mode != "when_preapproved" {
			return mcpFailure(errors.New("server onboarding apply_mode must be request_approval or when_preapproved")), nil, nil
		}
		serverInput := map[string]any{"name": strings.TrimSpace(input.Name), "region_code": strings.ToUpper(strings.TrimSpace(input.RegionCode)), "ip_stack": strings.TrimSpace(input.IPStack), "entry_address": strings.TrimSpace(input.EntryAddress), "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 60000, "bbr_enabled": input.BBREnabled}
		if serverInput["ip_stack"] == "" {
			serverInput["ip_stack"] = "auto"
		}
		result, err := s.submitMCPChangeset(ctx, mcpSubmitChangesetInput{Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, Operations: []mcpOperationRequest{{Capability: "servers.onboard", Input: map[string]any{"server": serverInput, "issue_enrollment_token": true}}}, ApplyMode: mode})
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		return &mcp.CallToolResult{}, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_start_deployment", Title: "Start Deployment", Description: "Submit deployment through the validated Changeset and existing Controller deployment pipeline; no raw Agent task can be supplied.", InputSchema: s.mcpStartDeploymentInputSchema(), OutputSchema: mcpEnvelopeSchema(mcpChangesetDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: write, IdempotentHint: true, DestructiveHint: &destructive, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpStartDeploymentInput) (*mcp.CallToolResult, any, error) {
		mode := strings.TrimSpace(input.ApplyMode)
		if mode == "" {
			mode = "when_preapproved"
		}
		if mode != "request_approval" && mode != "when_preapproved" && mode != "apply_after_approval" {
			return mcpFailure(errors.New("deployment apply_mode must be request_approval, when_preapproved, or apply_after_approval")), nil, nil
		}
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		operation := mcpOperationRequest{Capability: "deployments.apply", Input: map[string]any{"server_ids": input.ServerIDs, "reason": input.Reason}, ResourceRefs: map[string]any{"server_ids": input.ServerIDs}}
		if len(input.ExpectedRevisions) == 0 {
			operations, _, operationErr := s.mcpAutomationOperations(principal, []mcpOperationRequest{operation})
			if operationErr != nil {
				return mcpFailure(operationErr), nil, nil
			}
			validated, validateErr := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: operations})
			if validateErr != nil {
				return mcpFailure(validateErr), nil, nil
			}
			input.ExpectedRevisions = validated.ExpectedRevisions
		}
		result, err := s.submitMCPChangeset(ctx, mcpSubmitChangesetInput{Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, ExpectedRevisions: input.ExpectedRevisions, Operations: []mcpOperationRequest{operation}, ApplyMode: mode})
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		return &mcp.CallToolResult{}, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_start_workflow", Title: "Start Workflow", Description: "Create persistent tracking for an existing Changeset without keeping an HTTP request open.", OutputSchema: mcpEnvelopeSchema(mcpWorkflowDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: write, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpStartWorkflowInput) (*mcp.CallToolResult, any, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		item, err := s.automation.StartWorkflow(ctx, principal, automation.StartWorkflowRequest{Kind: input.Kind, Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, ChangesetID: input.ChangesetID})
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		return &mcp.CallToolResult{}, workflowEnvelope(item), nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_get_workflow", Title: "Get Workflow", Description: "Read one persistent Workflow and its digest-only step history.", OutputSchema: mcpEnvelopeSchema(mcpWorkflowDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpWorkflowIDInput) (*mcp.CallToolResult, any, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		item, err := s.automation.GetWorkflow(ctx, principal, input.WorkflowID)
		if err != nil {
			return mcpFailure(errors.New("workflow not found")), nil, nil
		}
		return &mcp.CallToolResult{}, workflowEnvelope(item), nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_cancel_workflow", Title: "Cancel Workflow", Description: "Cancel tracking while the Workflow is in a cancellable waiting state; this never rolls back an applied Changeset.", OutputSchema: mcpEnvelopeSchema(mcpWorkflowDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: write, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpWorkflowIDInput) (*mcp.CallToolResult, any, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		item, err := s.automation.CancelWorkflow(ctx, principal, input.WorkflowID)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		return &mcp.CallToolResult{}, workflowEnvelope(item), nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "oboard_retry_workflow_step", Title: "Retry Workflow Step", Description: "Retry only a step that OBoard marked retryable; clients cannot mark a step retryable.", OutputSchema: mcpEnvelopeSchema(mcpWorkflowDataSchema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: write, IdempotentHint: false, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpRetryWorkflowInput) (*mcp.CallToolResult, any, error) {
		principal, err := mcpPrincipal(ctx)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		item, err := s.automation.RetryWorkflowStep(ctx, principal, input.WorkflowID, input.StepID)
		if err != nil {
			return mcpFailure(err), nil, nil
		}
		return &mcp.CallToolResult{}, workflowEnvelope(item), nil
	})
}

func (s *Server) submitMCPChangeset(ctx context.Context, input mcpSubmitChangesetInput) (*mcpResultEnvelope, error) {
	principal, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	mode := strings.TrimSpace(input.ApplyMode)
	if mode == "" {
		mode = "request_approval"
	}
	if mode != "plan_only" && mode != "request_approval" && mode != "when_preapproved" && mode != "apply_after_approval" {
		return nil, errors.New("invalid apply_mode")
	}
	base, err := json.Marshal(input.ExpectedRevisions)
	if err != nil {
		return nil, err
	}
	operations, kind, err := s.mcpAutomationOperations(principal, input.Operations)
	if err != nil {
		return nil, err
	}
	item, err := s.automation.Create(ctx, principal, automation.CreateRequest{Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, BaseRevisions: base, AutoApply: mode == "when_preapproved", Operations: operations})
	if err != nil {
		s.recordToolCall(ctx, principal, "changesets.submit", input, "failed", capability.DataInternal)
		return nil, err
	}
	if mode != "plan_only" && (item.Status == model.ChangesetDraft || item.Status == model.ChangesetValidated || item.Status == model.ChangesetAwaitingApproval) {
		item, err = s.automation.Validate(ctx, principal, item.ID)
	}
	if err == nil && mode == "apply_after_approval" && item.Status == model.ChangesetApproved {
		item, err = s.automation.Apply(ctx, principal, item.ID)
	}
	if err != nil {
		s.recordToolCall(ctx, principal, "changesets.submit", input, "failed", capability.DataInternal)
		return nil, err
	}
	externalAction := s.mcpEnrollmentAction(ctx, item)
	workflowKeySum := sha256.Sum256([]byte(principal.ID + "\x00" + input.IdempotencyKey))
	workflow, err := s.automation.StartWorkflow(ctx, principal, automation.StartWorkflowRequest{Kind: kind, Reason: input.Reason, IdempotencyKey: "submit:" + hex.EncodeToString(workflowKeySum[:16]), ChangesetID: item.ID, ExternalAction: externalAction != nil})
	if err != nil {
		return nil, err
	}
	data := map[string]any{"changeset_status": item.Status, "risk_class": item.RiskClass, "plan_hash": item.PlanHash, "validation": mcpChangesetValidationSummary(item.Validation), "workflow": workflow}
	status := workflowResultStatus(workflow.Status)
	result := newMCPEnvelope(status, workflow.ID, item.ID, data)
	if externalAction != nil {
		result.NextAction = externalAction
	}
	s.recordToolCall(ctx, principal, "changesets.submit", input, "succeeded", capability.DataInternal)
	return result, nil
}

func (s *Server) mcpAutomationOperations(principal application.Principal, requested []mcpOperationRequest) ([]automation.OperationRequest, string, error) {
	if len(requested) == 0 || len(requested) > 64 {
		return nil, "", errors.New("operations must contain between 1 and 64 items")
	}
	operations := make([]automation.OperationRequest, 0, len(requested))
	kind := "changeset"
	for _, operation := range requested {
		descriptor, authorized := s.capabilities.Authorize(principal, operation.Capability)
		if !authorized || !descriptor.MCPEnabled || !descriptor.Executable {
			return nil, "", fmt.Errorf("capability %q is not executable through this OAuth Grant", operation.Capability)
		}
		rawInput, err := json.Marshal(operation.Input)
		if err != nil {
			return nil, "", err
		}
		resourceRefs, err := json.Marshal(operation.ResourceRefs)
		if err != nil {
			return nil, "", err
		}
		operations = append(operations, automation.OperationRequest{Capability: descriptor.Name, Input: rawInput, SecretRefs: operation.SecretRefs, ResourceRefs: resourceRefs})
		if descriptor.Name == "servers.onboard" {
			kind = "server_onboarding"
		} else if descriptor.Name == "deployments.apply" {
			kind = "deployment"
		}
	}
	return operations, kind, nil
}

func mcpCapabilityViews(descriptors []capability.Descriptor) []map[string]any {
	items := make([]map[string]any, 0, len(descriptors))
	for _, descriptor := range descriptors {
		items = append(items, mcpCapabilityView(descriptor))
	}
	return items
}

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
	}
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

func (s *Server) mcpEnrollmentAction(ctx context.Context, item *model.AutomationChangeset) map[string]any {
	var result struct {
		Operations []map[string]any `json:"operations"`
	}
	if json.Unmarshal(item.Result, &result) != nil {
		return nil
	}
	for _, operation := range result.Operations {
		token, _ := operation["enrollment_token"].(string)
		server, _ := operation["server"].(map[string]any)
		if token == "" || server == nil {
			continue
		}
		base, err := s.publicBaseURL(ctx)
		if err != nil {
			return nil
		}
		return map[string]any{"type": "execute_on_target", "title": "安装 OBoard Agent", "command": "curl -fsSL " + shellSingleQuote(strings.TrimRight(base, "/")+"/install/agent.sh") + ` | env OBOARD_ENROLL_TOKEN="$OBOARD_ENROLL_TOKEN" OBOARD_INSTALL_BBR="${OBOARD_INSTALL_BBR:-0}" sh`, "environment": map[string]any{"OBOARD_ENROLL_TOKEN": token, "OBOARD_INSTALL_BBR": fmt.Sprint(server["bbr_enabled"])}, "expires_at": operation["enrollment_expires_at"], "sensitive": true, "must_not_log": true, "completion_condition": map[string]any{"resource_uri": fmt.Sprintf("oboard://servers/%v/health", server["id"]), "field": "agent_connected", "equals": true}}
	}
	return nil
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

func workflowEnvelope(item *model.AutomationWorkflow) *mcpResultEnvelope {
	result := newMCPEnvelope(workflowResultStatus(item.Status), item.ID, item.ChangesetID, item)
	if len(item.NextAction) > 0 && string(item.NextAction) != "{}" {
		var action any
		if json.Unmarshal(item.NextAction, &action) == nil {
			result.NextAction = action
		}
	}
	return result
}

func newMCPEnvelope(status, workflowID, changesetID string, data any) *mcpResultEnvelope {
	correlation, _ := security.RandomToken(18)
	return &mcpResultEnvelope{Status: status, WorkflowID: workflowID, ChangesetID: changesetID, OperationID: "", AffectedResources: []mcpAffectedResource{}, Warnings: []string{}, NextAction: nil, CorrelationID: "corr_" + correlation, Retryable: false, Data: data}
}

func (s *Server) mcpSubmitInputSchema() json.RawMessage {
	return mustRawSchema(closedMCPSchema(map[string]any{
		"reason":             map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
		"idempotency_key":    map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"expected_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"operations":         s.mcpOperationListSchema(),
		"apply_mode":         map[string]any{"type": "string", "enum": []string{"plan_only", "request_approval", "when_preapproved", "apply_after_approval"}},
	}, "reason", "idempotency_key", "operations", "apply_mode"))
}

func (s *Server) mcpDesiredStateInputSchema() json.RawMessage {
	return mustRawSchema(closedMCPSchema(map[string]any{
		"expected_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"operations":         s.mcpOperationListSchema(),
	}, "operations"))
}

func (s *Server) mcpStartDeploymentInputSchema() json.RawMessage {
	return mustRawSchema(closedMCPSchema(map[string]any{
		"server_ids":         map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": map[string]any{"type": "integer", "minimum": 1}},
		"reason":             map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
		"idempotency_key":    map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"expected_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"apply_mode":         map[string]any{"type": "string", "enum": []string{"request_approval", "when_preapproved", "apply_after_approval"}},
	}, "server_ids", "reason", "idempotency_key", "apply_mode"))
}

func (s *Server) mcpOperationListSchema() map[string]any {
	inputs := []any{}
	capabilities := []string{}
	for _, descriptor := range s.capabilities.List(application.Principal{Scopes: []string{"*"}}) {
		if !descriptor.MCPEnabled || !descriptor.Executable {
			continue
		}
		var schema any
		if json.Unmarshal(descriptor.InputSchema, &schema) == nil {
			inputs = append(inputs, schema)
			capabilities = append(capabilities, descriptor.Name)
		}
	}
	return map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": closedMCPSchema(map[string]any{"capability": map[string]any{"type": "string", "enum": capabilities}, "input": map[string]any{"type": "object", "anyOf": inputs}, "secret_refs": map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string"}}, "resource_refs": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}}}}, "capability", "input")}
}

func (s *Server) mcpLegacyQueryInputSchema() json.RawMessage {
	names := []string{}
	inputs := []any{}
	for _, descriptor := range s.capabilities.List(application.Principal{Scopes: []string{"*"}}) {
		if !descriptor.MCPEnabled || !descriptor.ReadOnly {
			continue
		}
		var schema any
		if json.Unmarshal(descriptor.InputSchema, &schema) == nil {
			names = append(names, descriptor.Name)
			inputs = append(inputs, schema)
		}
	}
	return mustRawSchema(closedMCPSchema(map[string]any{"capability": map[string]any{"type": "string", "enum": names}, "arguments": map[string]any{"type": "object", "anyOf": inputs}}, "capability"))
}

func (s *Server) mcpLegacyQueryOutputSchema() json.RawMessage {
	outputs := []any{}
	for _, descriptor := range s.capabilities.List(application.Principal{Scopes: []string{"*"}}) {
		if !descriptor.MCPEnabled || !descriptor.ReadOnly {
			continue
		}
		var schema any
		if json.Unmarshal(descriptor.OutputSchema, &schema) == nil {
			outputs = append(outputs, schema)
		}
	}
	return mustRawSchema(map[string]any{"anyOf": outputs})
}

func (s *Server) mcpLegacyChangesetInputSchema() json.RawMessage {
	submit := s.mcpSubmitInputSchema()
	var schema map[string]any
	_ = json.Unmarshal(submit, &schema)
	properties, _ := schema["properties"].(map[string]any)
	properties["base_revisions"] = properties["expected_revisions"]
	delete(properties, "expected_revisions")
	delete(properties, "apply_mode")
	required, _ := schema["required"].([]any)
	filtered := []any{}
	for _, value := range required {
		if value != "apply_mode" {
			filtered = append(filtered, value)
		}
	}
	schema["required"] = filtered
	return mustRawSchema(schema)
}

func (s *Server) mcpLegacyChangesetSchema() json.RawMessage {
	return mustRawSchema(s.mcpLegacyChangesetSchemaMap())
}

func (s *Server) mcpLegacyChangesetSchemaMap() map[string]any {
	stringValue := map[string]any{"type": "string"}
	nullableTime := map[string]any{"type": []string{"string", "null"}}
	operation := s.mcpLegacyOperationSchema()
	validation := closedMCPSchema(map[string]any{"valid": map[string]any{"type": "boolean"}, "warnings": map[string]any{"type": "array", "items": stringValue}, "validated_operations": map[string]any{"type": "integer"}})
	blast := closedMCPSchema(map[string]any{"operation_count": map[string]any{"type": "integer"}, "capabilities": map[string]any{"type": "array", "items": stringValue}, "resource_kinds": map[string]any{"type": "array", "items": stringValue}})
	return closedMCPSchema(map[string]any{"id": stringValue, "principal_id": stringValue, "actor_user_id": map[string]any{"type": []string{"integer", "null"}}, "status": stringValue, "reason": stringValue, "idempotency_key": stringValue, "base_revisions": map[string]any{"type": "string", "contentMediaType": "application/json"}, "plan_hash": stringValue, "risk_class": map[string]any{"type": "integer"}, "auto_apply": map[string]any{"type": "boolean"}, "validation": validation, "blast_radius": blast, "result": map[string]any{"type": "string", "contentMediaType": "application/json"}, "expires_at": stringValue, "created_at": stringValue, "updated_at": stringValue, "validated_at": nullableTime, "approved_at": nullableTime, "applied_at": nullableTime, "completed_at": nullableTime, "operations": map[string]any{"type": "array", "items": operation}})
}

func (s *Server) mcpLegacyOperationSchema() map[string]any {
	stringValue := map[string]any{"type": "string"}
	jsonValue := map[string]any{"type": "string", "contentMediaType": "application/json"}
	return closedMCPSchema(map[string]any{"id": stringValue, "changeset_id": stringValue, "position": map[string]any{"type": "integer"}, "capability": stringValue, "input": jsonValue, "secret_refs": map[string]any{"type": "array", "items": stringValue}, "resource_refs": jsonValue, "risk_class": map[string]any{"type": "integer"}, "status": stringValue, "result": jsonValue, "error_code": stringValue, "error_message": stringValue, "created_at": stringValue, "completed_at": map[string]any{"type": []string{"string", "null"}}})
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

func mcpEnvelopeSchema(data any) json.RawMessage {
	return mustRawSchema(closedMCPSchema(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"planned", "external_action_required", "waiting_for_agent", "approval_required", "queued", "running", "succeeded", "partially_succeeded", "failed", "cancelled", "expired"}}, "workflow_id": map[string]any{"type": "string"}, "changeset_id": map[string]any{"type": "string"}, "operation_id": map[string]any{"type": "string"}, "affected_resources": map[string]any{"type": "array", "items": closedMCPSchema(map[string]any{"type": map[string]any{"type": "string"}, "id": map[string]any{"type": []string{"string", "integer"}}}, "type", "id")}, "warnings": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "next_action": map[string]any{"oneOf": []any{map[string]any{"type": "null"}, mcpNextActionSchema()}}, "correlation_id": map[string]any{"type": "string"}, "retryable": map[string]any{"type": "boolean"}, "data": data}, "status", "workflow_id", "changeset_id", "operation_id", "affected_resources", "warnings", "next_action", "correlation_id", "retryable", "data"))
}

func mcpNextActionSchema() map[string]any {
	return closedMCPSchema(map[string]any{"type": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "command": map[string]any{"type": "string"}, "environment": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "server_id": map[string]any{"type": "integer", "minimum": 1}, "expires_at": map[string]any{"type": []string{"string", "null"}}, "sensitive": map[string]any{"type": "boolean"}, "must_not_log": map[string]any{"type": "boolean"}, "changeset_id": map[string]any{"type": "string"}, "completion_condition": closedMCPSchema(map[string]any{"resource_uri": map[string]any{"type": "string"}, "field": map[string]any{"type": "string"}, "equals": map[string]any{"type": "boolean"}})})
}

func mcpDiscoverDataSchema() map[string]any {
	return closedMCPSchema(map[string]any{"controller_version": map[string]any{"type": "string"}, "grant_id": map[string]any{"type": "string"}, "scopes": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "resource_filter": mcpResourceFilterSchema(), "capabilities": map[string]any{"type": "array", "items": mcpDescriptorSchema()}, "limits": closedMCPSchema(map[string]any{"max_changeset_operations": map[string]any{"type": "integer"}, "changeset_ttl_seconds": map[string]any{"type": "integer"}}), "workflow_rules": closedMCPSchema(map[string]any{"write_via_changeset": map[string]any{"type": "boolean"}, "ssh_supported": map[string]any{"type": "boolean"}, "shell_supported": map[string]any{"type": "boolean"}, "admin_deletion_supported": map[string]any{"type": "boolean"}, "risk4_auto_approval": map[string]any{"type": "boolean"}})})
}

func mcpResourceFilterSchema() map[string]any {
	mode := map[string]any{"type": "string", "enum": []string{"all", "selected", "none"}}
	ids := map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}}
	return closedMCPSchema(map[string]any{
		"servers":                closedMCPSchema(map[string]any{"mode": mode, "ids": ids, "allow_create": map[string]any{"type": "boolean"}}, "mode", "ids", "allow_create"),
		"users":                  closedMCPSchema(map[string]any{"mode": mode, "ids": ids}, "mode"),
		"proxy_paths":            closedMCPSchema(map[string]any{"mode": mode, "ids": ids}, "mode"),
		"settings":               closedMCPSchema(map[string]any{"allowed_sections": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "allowed_sections"),
		"destructive_operations": map[string]any{"type": "boolean"},
	}, "servers", "users", "proxy_paths", "settings", "destructive_operations")
}

func mcpCapabilityDataSchema() map[string]any { return mcpDescriptorSchema() }

func mcpDescriptorSchema() map[string]any {
	return closedMCPSchema(map[string]any{"name": map[string]any{"type": "string"}, "version": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "input_schema": map[string]any{"type": "string", "contentMediaType": "application/schema+json"}, "output_schema": map[string]any{"type": "string", "contentMediaType": "application/schema+json"}, "required_scopes": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "resource_types": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "resource_filter_evaluator": map[string]any{"type": "string"}, "risk_class": map[string]any{"type": "integer"}, "approval_policy": map[string]any{"type": "string"}, "idempotent": map[string]any{"type": "boolean"}, "read_only": map[string]any{"type": "boolean"}, "data_classification": map[string]any{"type": "string"}, "sensitive_fields": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "sensitive_input_fields": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "sensitive_output_fields": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "destructive": map[string]any{"type": "boolean"}, "open_world": map[string]any{"type": "boolean"}, "documentation": map[string]any{"type": "string"}, "deprecated_since": map[string]any{"type": "string"}, "replacement": map[string]any{"type": "string"}, "mcp_enabled": map[string]any{"type": "boolean"}, "executable": map[string]any{"type": "boolean"}})
}

func mcpChangesetDataSchema() map[string]any {
	validation := closedMCPSchema(map[string]any{"valid": map[string]any{"type": "boolean"}, "warnings": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "validated_operations": map[string]any{"type": "integer"}}, "valid", "warnings", "validated_operations")
	return closedMCPSchema(map[string]any{"changeset_status": map[string]any{"type": "string"}, "risk_class": map[string]any{"type": "integer"}, "plan_hash": map[string]any{"type": "string"}, "validation": validation, "workflow": mcpWorkflowDataSchema()})
}

func mcpDesiredStateDataSchema() map[string]any {
	return closedMCPSchema(map[string]any{
		"valid":                map[string]any{"type": "boolean"},
		"plan_hash":            map[string]any{"type": "string"},
		"risk_class":           map[string]any{"type": "integer", "minimum": 0, "maximum": 4},
		"expected_revisions":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"validated_operations": map[string]any{"type": "integer", "minimum": 1, "maximum": 64},
		"warnings":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, "valid", "plan_hash", "risk_class", "expected_revisions", "validated_operations", "warnings")
}

func mcpWorkflowDataSchema() map[string]any {
	step := closedMCPSchema(map[string]any{"id": map[string]any{"type": "string"}, "workflow_id": map[string]any{"type": "string"}, "position": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "attempt": map[string]any{"type": "integer"}, "idempotency_key": map[string]any{"type": "string"}, "input_digest": map[string]any{"type": "string"}, "output_digest": map[string]any{"type": "string"}, "retryable": map[string]any{"type": "boolean"}, "next_action": mcpNextActionSchema(), "error_code": map[string]any{"type": "string"}, "correlation_id": map[string]any{"type": "string"}, "started_at": map[string]any{"type": []string{"string", "null"}}, "finished_at": map[string]any{"type": []string{"string", "null"}}, "created_at": map[string]any{"type": "string"}, "updated_at": map[string]any{"type": "string"}})
	return closedMCPSchema(map[string]any{"id": map[string]any{"type": "string"}, "principal_id": map[string]any{"type": "string"}, "grant_id": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}, "changeset_id": map[string]any{"type": "string"}, "current_step": map[string]any{"type": "string"}, "correlation_id": map[string]any{"type": "string"}, "affected_resources": map[string]any{"type": "array", "items": closedMCPSchema(map[string]any{"type": map[string]any{"type": "string"}, "id": map[string]any{"type": []string{"string", "integer"}}})}, "next_action": mcpNextActionSchema(), "error_code": map[string]any{"type": "string"}, "error_message": map[string]any{"type": "string"}, "created_at": map[string]any{"type": "string"}, "updated_at": map[string]any{"type": "string"}, "completed_at": map[string]any{"type": []string{"string", "null"}}, "steps": map[string]any{"type": "array", "items": step}})
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
