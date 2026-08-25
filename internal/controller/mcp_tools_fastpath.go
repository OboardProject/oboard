package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const mcpPreparedPlanTTL = 30 * time.Minute

type mcpTaskInput struct {
	Goal           string         `json:"goal"`
	Intent         string         `json:"intent,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	TargetRefs     []string       `json:"target_refs,omitempty"`
	ContinuationID string         `json:"continuation_id,omitempty"`
}

type mcpCommitTaskInput struct {
	PreparedID     string `json:"prepared_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason,omitempty"`
}

func (s *Server) addMCPTaskTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Meta: mcp.Meta{"anthropic/alwaysLoad": true},
		Name: "oboard_task", Title: "Prepare OBoard Task",
		Description: "PRIMARY ENTRYPOINT for normal OBoard operations. Use this tool first for server onboarding, server settings, inbounds, DNS/certificates, proxy paths, deployments, users, subscriptions, forwarding, tunnels and diagnostics. It resolves resources, selects the correct OBoard workflow, fills defaults and validates the proposed change. It never mutates persistent business state.",
		InputSchema: mustRawSchema(closedMCPSchema(map[string]any{
			"goal": map[string]any{"type": "string", "maxLength": 4000}, "intent": map[string]any{"type": "string", "maxLength": 128},
			"params": map[string]any{"type": "object", "additionalProperties": true}, "target_refs": map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "maxLength": 256}},
			"continuation_id": map[string]any{"type": "string", "maxLength": 128},
		})),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotations(true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpTaskInput) (*mcp.CallToolResult, any, error) {
		principal, _ := mcpPrincipal(ctx)
		started := time.Now()
		result := s.prepareMCPTask(ctx, principal, input)
		s.recordFastPathMetric(ctx, principal, result, "prepare", time.Since(started))
		return &mcp.CallToolResult{}, result, nil
	})
}

func (s *Server) addMCPCommitTaskTool(server *mcp.Server, principal application.Principal) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "oboard_commit_task", Title: "Commit Prepared OBoard Task",
		Description:  "Commit a prepared_id returned by oboard_task. Reauthorizes the current grant, checks recipe version and resource revisions, revalidates the immutable prepared operations, creates the canonical Changeset, and starts the canonical Workflow. Idempotent retries must reuse the same key.",
		InputSchema:  mustRawSchema(closedMCPSchema(map[string]any{"prepared_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "idempotency_key": map[string]any{"type": "string", "minLength": 8, "maxLength": 128}, "reason": map[string]any{"type": "string", "maxLength": 4000}}, "prepared_id", "idempotency_key")),
		OutputSchema: mustRawSchema(map[string]any{"type": "object"}), Annotations: mcpAnnotationsWrite(true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input mcpCommitTaskInput) (*mcp.CallToolResult, any, error) {
		principal, _ := mcpPrincipal(ctx)
		started := time.Now()
		result := s.commitMCPTask(ctx, principal, input)
		s.recordFastPathMetric(ctx, principal, result, "commit", time.Since(started))
		return &mcp.CallToolResult{}, result, nil
	})
}

func (s *Server) prepareMCPTask(ctx context.Context, principal application.Principal, input mcpTaskInput) *ToolEnvelope {
	input.Goal = strings.TrimSpace(input.Goal)
	input.Intent = strings.TrimSpace(input.Intent)
	if input.Params == nil {
		input.Params = map[string]any{}
	}
	var recipe mcpRecipe
	if continuationID := strings.TrimSpace(input.ContinuationID); continuationID != "" {
		continuation, err := s.store.GetMCPTaskContinuation(ctx, continuationID, principal.ID, principal.GrantID, time.Now().UTC())
		if err != nil {
			return fastPathError("continuation_unavailable", "continuation is missing, expired, or belongs to another grant", true, "restart_task")
		}
		if continuation.RecipeID == "router" {
			if continuation.RecipeVersion != mcpRecipeVersion {
				return fastPathError("continuation_version_changed", "continuation routing version changed", true, "restart_task")
			}
		} else if current, ok := s.mcpRecipeByID(continuation.RecipeID); !ok || current.Version != continuation.RecipeVersion {
			return fastPathError("continuation_version_changed", "continuation recipe version changed", true, "restart_task")
		}
		storedParams := map[string]any{}
		_ = json.Unmarshal(continuation.Params, &storedParams)
		for key, value := range input.Params {
			storedParams[key] = value
		}
		input.Params = storedParams
		storedRefs := []string{}
		_ = json.Unmarshal(continuation.TargetRefs, &storedRefs)
		input.TargetRefs = append(storedRefs, input.TargetRefs...)
		if input.Goal == "" {
			input.Goal = continuation.Goal
		}
		if continuation.RecipeID == "router" {
			if input.Intent == "" {
				input.Intent = taskStringParam(input.Params, "intent")
			}
		} else {
			input.Intent = continuation.RecipeID
		}
		_ = s.store.DeleteMCPTaskContinuation(ctx, continuation.ID, principal.ID, principal.GrantID)
	}
	matched, candidates, ok := s.matchMCPRecipe(input)
	if !ok {
		if len(candidates) > 0 {
			return s.continuationResult(ctx, principal, input, "router", mcpRecipeVersion, &mcpPreparedRecipe{Status: "choose_candidate", Intent: "", Field: "intent", Candidates: candidates})
		}
		return newToolEnvelope("fallback_required", "", map[string]any{"reason": "no deterministic Fast Path recipe matched the request", "recommended_capabilities": []string{"oboard_discover", "oboard_get_capability_schema", "oboard_plan_desired_state", "oboard_validate_desired_state", "oboard_submit_changeset"}})
	}
	recipe = matched
	if recipe.Prepare == nil {
		return fastPathError("recipe_unavailable", "matched Fast Path recipe is not executable", true, "fallback")
	}
	prepared, err := recipe.Prepare(ctx, principal, input)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fastPathError("resource_not_found", "the requested resource was not found in the current grant", true, "change_parameters")
		}
		return fastPathError("recipe_validation_failed", err.Error(), true, "change_parameters")
	}
	if prepared.Status == "needs_input" || prepared.Status == "choose_candidate" {
		return s.continuationResult(ctx, principal, input, recipe.ID, recipe.Version, prepared)
	}
	if prepared.Status == "fallback_required" {
		return newToolEnvelope("fallback_required", "", map[string]any{"intent": recipe.ID, "recipe_version": recipe.Version, "reason": "the matched Recipe cannot safely express this operation with the current executable capability catalog", "recommended_capabilities": prepared.Fallback})
	}
	if prepared.Status == "query_ready" && prepared.DirectResult != nil {
		s.recordToolCall(ctx, principal, "fastpath.query:"+recipe.ID+"@"+recipe.Version, map[string]any{"intent": recipe.ID, "target_ref_count": len(input.TargetRefs)}, "succeeded", capability.DataInternal)
		return newToolEnvelope("succeeded", "", prepared.DirectResult)
	}
	if prepared.Status != "ready" {
		return fastPathError("recipe_failed", "recipe did not produce a prepared operation", true, "fallback")
	}
	operationRequests, err := mcpOperationRequests(prepared.Operations)
	if err != nil {
		return fastPathError("invalid_operations", err.Error(), false, "fallback")
	}
	approvalMode := "automatic"
	for _, operation := range prepared.Operations {
		decision := s.authorizePlanOperation(ctx, operation.Capability, operation.Input)
		if !decision.Allowed {
			return errorEnvelope("", decision, "", "")
		}
		if decision.ApprovalMode != "automatic" {
			approvalMode = decision.ApprovalMode
		}
	}
	validated, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: operationRequests})
	if err != nil {
		return fastPathError("validation_failed", err.Error(), true, "change_parameters")
	}
	operationsJSON, _ := json.Marshal(prepared.Operations)
	revisionsJSON, _ := json.Marshal(validated.ExpectedRevisions)
	summaryJSON, _ := json.Marshal(prepared.Summary)
	verificationJSON, _ := json.Marshal(prepared.Verification)
	planHash := mcpPreparedPlanHash(principal.ID, principal.GrantID, recipe.ID, recipe.Version, prepared.Operations, validated.ExpectedRevisions)
	idToken, err := security.RandomToken(18)
	if err != nil {
		return fastPathError("internal_error", "could not allocate prepared task", true, "retry")
	}
	now := time.Now().UTC()
	item := &model.MCPPreparedPlan{ID: "prep_" + idToken, PrincipalID: principal.ID, GrantID: principal.GrantID, RecipeID: recipe.ID, RecipeVersion: recipe.Version, Operations: operationsJSON, ExpectedRevisions: revisionsJSON, PlanHash: planHash, RiskClass: validated.RiskClass, ApprovalMode: approvalMode, Summary: summaryJSON, Verification: verificationJSON, ExpiresAt: now.Add(mcpPreparedPlanTTL)}
	if err := s.store.CreateMCPPreparedPlan(ctx, item); err != nil {
		return fastPathError("internal_error", "could not persist prepared task", true, "retry")
	}
	data := map[string]any{"intent": recipe.ID, "summary": prepared.Summary, "validation": map[string]any{"valid": validated.Valid, "warnings": validated.Warnings}, "prepared_id": item.ID, "expires_at": item.ExpiresAt, "risk": map[string]any{"class": validated.RiskClass}, "approval": map[string]any{"required": approvalMode != "automatic", "mode": approvalMode}, "verification": prepared.Verification, "next_action": map[string]any{"type": "commit"}}
	s.recordToolCall(ctx, principal, "fastpath.prepare:"+recipe.ID+"@"+recipe.Version, map[string]any{"intent": recipe.ID, "target_ref_count": len(input.TargetRefs)}, "ready", capability.DataInternal)
	return newToolEnvelope("ready", "", data)
}

func (s *Server) continuationResult(ctx context.Context, principal application.Principal, input mcpTaskInput, recipeID, version string, prepared *mcpPreparedRecipe) *ToolEnvelope {
	idToken, err := security.RandomToken(18)
	if err != nil {
		return fastPathError("internal_error", "could not allocate continuation", true, "retry")
	}
	now := time.Now().UTC()
	params, _ := json.Marshal(input.Params)
	refs, _ := json.Marshal(input.TargetRefs)
	item := &model.MCPTaskContinuation{ID: "cont_" + idToken, PrincipalID: principal.ID, GrantID: principal.GrantID, RecipeID: recipeID, RecipeVersion: version, Goal: input.Goal, Params: params, TargetRefs: refs, State: json.RawMessage(`{}`), ExpiresAt: now.Add(mcpPreparedPlanTTL)}
	if err := s.store.CreateMCPTaskContinuation(ctx, item); err != nil {
		return fastPathError("internal_error", "could not persist continuation", true, "retry")
	}
	data := map[string]any{"intent": prepared.Intent, "continuation_id": item.ID, "expires_at": item.ExpiresAt}
	if prepared.Status == "needs_input" {
		data["questions"] = prepared.Questions
	} else {
		data["field"] = prepared.Field
		data["candidates"] = prepared.Candidates
	}
	return newToolEnvelope(prepared.Status, "", data)
}

func (s *Server) commitMCPTask(ctx context.Context, principal application.Principal, input mcpCommitTaskInput) *ToolEnvelope {
	preparedID, key := strings.TrimSpace(input.PreparedID), strings.TrimSpace(input.IdempotencyKey)
	if preparedID == "" || len(key) < 8 || len(key) > 128 {
		return fastPathError("invalid_input", "prepared_id and an 8-128 character idempotency_key are required", true, "change_parameters")
	}
	item, err := s.store.ClaimMCPPreparedPlan(ctx, preparedID, principal.ID, principal.GrantID, key, time.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrMCPPreparedPlanExpired):
			return fastPathError("prepared_expired", "prepared task expired", true, "reprepare")
		case errors.Is(err, store.ErrMCPPreparedPlanCommitKey):
			return fastPathError("idempotency_conflict", "prepared task is already bound to a different idempotency key", false, "use_original_key")
		case errors.Is(err, store.ErrMCPPreparedPlanClaimed):
			return fastPathError("commit_in_progress", "another commit is in progress", true, "retry")
		default:
			return fastPathError("prepared_not_found", "prepared task is unavailable to this principal and grant", false, "reprepare")
		}
	}
	if item.ConsumedAt != nil {
		workflow, workflowErr := s.automation.GetWorkflow(ctx, principal, item.WorkflowID)
		if workflowErr != nil {
			return fastPathError("committed_result_unavailable", "prepared task was consumed but its workflow is unavailable", true, "get_workflow")
		}
		result := newToolEnvelope(workflowResultStatus(workflow.Status), "", map[string]any{"intent": item.RecipeID, "recipe_version": item.RecipeVersion, "workflow": workflowResourceSummary(workflow)})
		result.WorkflowID = item.WorkflowID
		result.ChangesetID = item.ChangesetID
		if actionID, actionErr := s.ensureWorkflowExternalAction(ctx, principal, workflow); actionErr != nil {
			return fastPathError("external_action_unavailable", "workflow external action is unavailable", true, "get_workflow")
		} else if actionID != "" {
			result.NextAction = map[string]any{"type": "redeem_external_action", "action_id": actionID, "workflow_id": workflow.ID, "sensitive": true, "must_not_log": true}
		}
		return result
	}
	release := true
	defer func() {
		if release {
			_ = s.store.ReleaseMCPPreparedPlanClaim(ctx, item.ID, key)
		}
	}()
	recipe, ok := s.mcpRecipeByID(item.RecipeID)
	if !ok || recipe.Version != item.RecipeVersion {
		return fastPathError("recipe_version_changed", "recipe version changed after preparation", true, "reprepare")
	}
	operations := []mcpOperationRef{}
	revisions := map[string]string{}
	if json.Unmarshal(item.Operations, &operations) != nil || json.Unmarshal(item.ExpectedRevisions, &revisions) != nil {
		return fastPathError("prepared_corrupt", "prepared task payload is invalid", false, "reprepare")
	}
	if mcpPreparedPlanHash(principal.ID, principal.GrantID, item.RecipeID, item.RecipeVersion, operations, revisions) != item.PlanHash {
		return fastPathError("plan_hash_mismatch", "prepared task integrity check failed", false, "reprepare")
	}
	for _, operation := range operations {
		decision := s.authorizePlanOperation(ctx, operation.Capability, operation.Input)
		if !decision.Allowed {
			return errorEnvelope("", decision, "", "")
		}
	}
	requests, err := mcpOperationRequests(operations)
	if err != nil {
		return fastPathError("prepared_corrupt", err.Error(), false, "reprepare")
	}
	base, _ := json.Marshal(revisions)
	validated, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{BaseRevisions: base, Operations: requests})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "revision") {
			return fastPathError("stale", err.Error(), true, "reprepare")
		}
		return fastPathError("validation_failed", err.Error(), true, "reprepare")
	}
	if validated.PlanHash == "" {
		return fastPathError("validation_failed", "revalidation did not produce a plan hash", true, "reprepare")
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "Commit prepared OBoard task " + item.RecipeID
	}
	idempotencySum := sha256.Sum256([]byte(principal.ID + "\x00" + item.ID + "\x00" + key))
	result, err := s.submitPreparedOperations(ctx, principal, operations, revisions, reason, "fastpath:"+hex.EncodeToString(idempotencySum[:16]), "use_preapproval_if_available")
	if err != nil {
		return fastPathError("changeset_submit_failed", err.Error(), true, "retry")
	}
	if err := s.store.ConsumeMCPPreparedPlan(ctx, item.ID, key, result.ChangesetID, result.WorkflowID, time.Now().UTC()); err != nil {
		failure := fastPathError("commit_finalize_failed", "workflow was created but prepared task finalization failed", true, "get_workflow")
		failure.WorkflowID = result.WorkflowID
		failure.ChangesetID = result.ChangesetID
		return failure
	}
	release = false
	if data, ok := result.Data.(map[string]any); ok {
		data["intent"] = item.RecipeID
		data["recipe_version"] = item.RecipeVersion
	}
	s.recordToolCall(ctx, principal, "fastpath.commit:"+item.RecipeID+"@"+item.RecipeVersion, map[string]any{"prepared_id": item.ID, "plan_hash": item.PlanHash}, result.Status, capability.DataInternal)
	return result
}

func mcpPreparedPlanHash(principalID, grantID, recipeID, recipeVersion string, operations []mcpOperationRef, revisions map[string]string) string {
	payload := struct {
		PrincipalID   string            `json:"principal_id"`
		GrantID       string            `json:"grant_id"`
		RecipeID      string            `json:"recipe_id"`
		RecipeVersion string            `json:"recipe_version"`
		Operations    []mcpOperationRef `json:"operations"`
		Revisions     map[string]string `json:"expected_revisions"`
	}{principalID, grantID, recipeID, recipeVersion, operations, revisions}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func fastPathError(code, message string, recoverable bool, nextAction string) *ToolEnvelope {
	result := newToolEnvelope("error", "", map[string]any{"code": code, "message": message, "recoverable": recoverable, "next_action": map[string]any{"type": nextAction}})
	result.Error = &mcpErrorBody{Code: code, Message: message, Recoverable: recoverable}
	if nextAction != "" {
		result.NextAction = map[string]any{"type": nextAction}
	}
	return result
}

func (s *Server) recordFastPathMetric(ctx context.Context, principal application.Principal, result *ToolEnvelope, phase string, duration time.Duration) {
	if result == nil {
		return
	}
	metric := &model.MCPFastPathMetric{
		PrincipalID: principal.ID, GrantID: principal.GrantID, FastPathUsed: true, Phase: phase, Status: result.Status,
		DurationMS: duration.Milliseconds(), ChangesetID: result.ChangesetID, WorkflowID: result.WorkflowID,
	}
	if phase == "workflow" {
		metric.FinalWorkflowStatus = result.Status
	}
	if data, ok := result.Data.(map[string]any); ok {
		metric.RecipeID, _ = data["intent"].(string)
		metric.RecipeVersion, _ = data["recipe_version"].(string)
		metric.FallbackReason, _ = data["reason"].(string)
		metric.NeedsInputCount = anySliceLength(data["questions"])
		metric.CandidateResolutionCount = anySliceLength(data["candidates"])
	}
	if metric.RecipeID != "" && metric.RecipeVersion == "" {
		metric.RecipeVersion = mcpRecipeVersion
	}
	if result.Error != nil {
		metric.ValidationFailure = result.Error.Code == "validation_failed" || result.Error.Code == "recipe_validation_failed"
		if metric.FallbackReason == "" && result.Error.Code == "fallback_required" {
			metric.FallbackReason = result.Error.Message
		}
	}
	id, err := security.RandomToken(18)
	if err != nil {
		return
	}
	metric.ID = "mfm_" + id
	_ = s.store.CreateMCPFastPathMetric(ctx, metric)
}

func anySliceLength(value any) int {
	switch items := value.(type) {
	case []map[string]any:
		return len(items)
	case []MCPResourceRef:
		return len(items)
	case []any:
		return len(items)
	default:
		return 0
	}
}
