package automation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type MutationHandler func(context.Context, application.Principal, json.RawMessage) (any, error)
type RevisionResolver func(context.Context, application.Principal, json.RawMessage) (map[string]string, error)

type MutationResult struct {
	Public  any
	OneTime any
}

type Service struct {
	store             *store.Store
	catalog           *capability.Catalog
	mu                sync.RWMutex
	handlers          map[string]MutationHandler
	validators        map[string]MutationHandler
	revisionResolvers map[string]RevisionResolver
	now               func() time.Time
}

type CreateRequest struct {
	Reason         string             `json:"reason"`
	IdempotencyKey string             `json:"idempotency_key"`
	BaseRevisions  json.RawMessage    `json:"base_revisions"`
	AutoApply      bool               `json:"auto_apply"`
	Operations     []OperationRequest `json:"operations"`
}

type DraftValidationRequest struct {
	BaseRevisions json.RawMessage
	Operations    []OperationRequest
}

type DraftValidationResult struct {
	Valid             bool              `json:"valid"`
	PlanHash          string            `json:"plan_hash"`
	RiskClass         int               `json:"risk_class"`
	ExpectedRevisions map[string]string `json:"expected_revisions"`
	Evidence          []any             `json:"evidence"`
	Warnings          []string          `json:"warnings"`
}

type OperationRequest struct {
	Capability   string          `json:"capability"`
	Input        json.RawMessage `json:"input"`
	SecretRefs   []string        `json:"secret_refs,omitempty"`
	ResourceRefs json.RawMessage `json:"resource_refs"`
}

func NewService(store *store.Store, catalog *capability.Catalog) *Service {
	return &Service{
		store: store, catalog: catalog,
		handlers: map[string]MutationHandler{}, validators: map[string]MutationHandler{}, revisionResolvers: map[string]RevisionResolver{},
		now: time.Now,
	}
}

func (s *Service) RegisterValidator(name string, handler MutationHandler) {
	if _, ok := s.catalog.Get(name); !ok {
		panic("register validator for unknown capability: " + name)
	}
	s.mu.Lock()
	s.validators[name] = handler
	s.mu.Unlock()
}

func (s *Service) Register(name string, handler MutationHandler) {
	if _, ok := s.catalog.Get(name); !ok {
		panic("register unknown capability: " + name)
	}
	s.mu.Lock()
	s.handlers[name] = handler
	s.mu.Unlock()
}

func (s *Service) RegisterRevisionResolver(name string, resolver RevisionResolver) {
	if _, ok := s.catalog.Get(name); !ok {
		panic("register revision resolver for unknown capability: " + name)
	}
	s.mu.Lock()
	s.revisionResolvers[name] = resolver
	s.mu.Unlock()
}

func (s *Service) Create(ctx context.Context, principal application.Principal, request CreateRequest) (*model.AutomationChangeset, error) {
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" || len(request.IdempotencyKey) > 128 {
		return nil, errors.New("idempotency_key is required and must not exceed 128 characters")
	}
	if existing, err := s.store.FindAutomationChangesetByIdempotency(ctx, principal.ID, request.IdempotencyKey); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if len(request.Operations) == 0 || len(request.Operations) > 64 {
		return nil, errors.New("changeset must contain between 1 and 64 operations")
	}
	id, err := prefixedID("chg")
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	baseRevisions, err := canonicalRevisions(request.BaseRevisions)
	if err != nil {
		return nil, err
	}
	item := &model.AutomationChangeset{
		ID: id, PrincipalID: principal.ID, ActorUserID: principal.UserID, Status: model.ChangesetDraft,
		Reason: strings.TrimSpace(request.Reason), IdempotencyKey: request.IdempotencyKey,
		BaseRevisions: baseRevisions, AutoApply: request.AutoApply,
		Validation: json.RawMessage(`{}`), BlastRadius: json.RawMessage(`{}`), Result: json.RawMessage(`{}`),
		ExpiresAt: now.Add(30 * time.Minute), Operations: make([]model.AutomationOperation, 0, len(request.Operations)),
	}
	for position, requested := range request.Operations {
		descriptor, authorized := s.catalog.Authorize(principal, requested.Capability)
		if !authorized || descriptor.ReadOnly || !descriptor.Executable {
			return nil, fmt.Errorf("capability %q is not authorized for changesets", requested.Capability)
		}
		if s.validator(requested.Capability) == nil || s.handler(requested.Capability) == nil {
			return nil, fmt.Errorf("capability %q is not executable in this Controller build", requested.Capability)
		}
		opID, err := prefixedID("op")
		if err != nil {
			return nil, err
		}
		input, err := canonicalObject(requested.Input)
		if err != nil {
			return nil, fmt.Errorf("operation %d input: %w", position, err)
		}
		item.Operations = append(item.Operations, model.AutomationOperation{ID: opID, ChangesetID: id, Position: position, Capability: descriptor.Name, Input: input, SecretRefs: requested.SecretRefs, ResourceRefs: normalizedObject(requested.ResourceRefs), RiskClass: descriptor.RiskClass, Status: "pending", Result: json.RawMessage(`{}`)})
		if descriptor.RiskClass > item.RiskClass {
			item.RiskClass = descriptor.RiskClass
		}
	}
	if err := s.store.CreateAutomationChangeset(ctx, item); err != nil {
		if existing, findErr := s.store.FindAutomationChangesetByIdempotency(ctx, principal.ID, request.IdempotencyKey); findErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return item, nil
}

func (s *Service) ValidateDraft(ctx context.Context, principal application.Principal, request DraftValidationRequest) (DraftValidationResult, error) {
	if len(request.Operations) == 0 || len(request.Operations) > 64 {
		return DraftValidationResult{}, errors.New("desired state must contain between 1 and 64 operations")
	}
	item := &model.AutomationChangeset{BaseRevisions: json.RawMessage(`{}`), Operations: make([]model.AutomationOperation, 0, len(request.Operations))}
	for position, requested := range request.Operations {
		descriptor, authorized := s.catalog.Authorize(principal, requested.Capability)
		if !authorized || descriptor.ReadOnly || !descriptor.Executable {
			return DraftValidationResult{}, fmt.Errorf("capability %q is not authorized for desired state validation", requested.Capability)
		}
		if s.validator(requested.Capability) == nil || s.handler(requested.Capability) == nil {
			return DraftValidationResult{}, fmt.Errorf("capability %q is not executable in this Controller build", requested.Capability)
		}
		input, err := canonicalObject(requested.Input)
		if err != nil {
			return DraftValidationResult{}, fmt.Errorf("operation %d input: %w", position, err)
		}
		item.Operations = append(item.Operations, model.AutomationOperation{ID: fmt.Sprintf("draft-%d", position+1), Position: position, Capability: descriptor.Name, Input: input, SecretRefs: requested.SecretRefs, ResourceRefs: normalizedObject(requested.ResourceRefs), RiskClass: descriptor.RiskClass, Status: "planned", Result: json.RawMessage(`{}`)})
		if descriptor.RiskClass > item.RiskClass {
			item.RiskClass = descriptor.RiskClass
		}
	}
	evidence, revisions, err := s.inspectOperations(ctx, principal, item)
	if err != nil {
		return DraftValidationResult{}, err
	}
	if len(request.BaseRevisions) > 0 && string(request.BaseRevisions) != "null" && string(request.BaseRevisions) != "{}" {
		expected, decodeErr := decodeRevisions(request.BaseRevisions)
		if decodeErr != nil {
			return DraftValidationResult{}, decodeErr
		}
		if err := compareRevisions(expected, revisions); err != nil {
			return DraftValidationResult{}, err
		}
	}
	item.BaseRevisions = mustJSON(revisions)
	planHash, err := changesetHash(item)
	if err != nil {
		return DraftValidationResult{}, err
	}
	return DraftValidationResult{Valid: true, PlanHash: planHash, RiskClass: item.RiskClass, ExpectedRevisions: revisions, Evidence: evidence, Warnings: []string{}}, nil
}

func (s *Service) Validate(ctx context.Context, principal application.Principal, id string) (*model.AutomationChangeset, error) {
	item, err := s.authorizedChangeset(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if !item.ExpiresAt.After(now) {
		item.Status = model.ChangesetExpired
		_ = s.store.UpdateAutomationChangeset(ctx, item)
		return item, errors.New("changeset expired")
	}
	if item.Status != model.ChangesetDraft && item.Status != model.ChangesetValidated && item.Status != model.ChangesetAwaitingApproval {
		return nil, errors.New("changeset cannot be validated in its current state")
	}
	validationEvidence, err := s.validateOperations(ctx, principal, item)
	if err != nil {
		return nil, err
	}
	item.PlanHash, err = changesetHash(item)
	if err != nil {
		return nil, err
	}
	item.Validation = mustJSON(map[string]any{"valid": true, "warnings": []string{}, "validated_operations": len(item.Operations), "evidence": validationEvidence})
	item.BlastRadius = mustJSON(blastRadius(item.Operations))
	item.ValidatedAt = &now
	item.Status = model.ChangesetAwaitingApproval
	if automatic, policyErr := s.automaticAllowed(ctx, principal, item); policyErr != nil {
		return nil, policyErr
	} else if automatic {
		item.Status = model.ChangesetApproved
		item.ApprovedAt = &now
	}
	if err := s.store.UpdateAutomationChangeset(ctx, item); err != nil {
		return nil, err
	}
	if item.AutoApply && item.Status == model.ChangesetApproved {
		return s.Apply(ctx, principal, item.ID)
	}
	return item, nil
}

func (s *Service) Approve(ctx context.Context, principal application.Principal, id, comment string) (*model.AutomationChangeset, error) {
	if !principal.Interactive || principal.UserID == nil || principal.Role != model.RoleAdmin && principal.Role != model.RoleOperator {
		return nil, errors.New("approval requires an interactive operator session")
	}
	item, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != model.ChangesetAwaitingApproval || item.PlanHash == "" {
		return nil, errors.New("changeset is not awaiting approval")
	}
	if item.RiskClass >= 4 && principal.Role != model.RoleAdmin {
		return nil, errors.New("risk class 4 changes require an administrator")
	}
	approvalID, err := prefixedID("apr")
	if err != nil {
		return nil, err
	}
	approval := &model.AutomationApproval{ID: approvalID, ChangesetID: item.ID, ApproverID: *principal.UserID, Decision: "approved", PlanHash: item.PlanHash, Comment: strings.TrimSpace(comment), ApprovedRisk: item.RiskClass}
	if err := s.store.CreateAutomationApproval(ctx, approval); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	item.Status, item.ApprovedAt = model.ChangesetApproved, &now
	if err := s.store.UpdateAutomationChangeset(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) Apply(ctx context.Context, principal application.Principal, id string) (*model.AutomationChangeset, error) {
	item, err := s.authorizedChangeset(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if item.Status != model.ChangesetApproved {
		return nil, errors.New("changeset is not approved")
	}
	if !item.ExpiresAt.After(s.now().UTC()) {
		item.Status = model.ChangesetExpired
		_ = s.store.UpdateAutomationChangeset(ctx, item)
		return item, errors.New("changeset expired")
	}
	hash, err := changesetHash(item)
	if err != nil || hash != item.PlanHash {
		item.Status = model.ChangesetSuperseded
		_ = s.store.UpdateAutomationChangeset(ctx, item)
		return item, errors.New("changeset plan hash no longer matches")
	}
	if _, err := s.validateOperations(ctx, principal, item); err != nil {
		item.Status = model.ChangesetSuperseded
		_ = s.store.UpdateAutomationChangeset(ctx, item)
		return item, fmt.Errorf("changeset state no longer matches the approved plan: %w", err)
	}
	if _, err := s.automaticAllowed(ctx, principal, item); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	claimed, err := s.store.ClaimAutomationChangesetApply(ctx, item.ID, now)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errors.New("changeset is already being applied")
	}
	item.Status, item.AppliedAt = model.ChangesetApplying, &now
	persistedResults := make([]any, 0, len(item.Operations))
	responseResults := make([]any, 0, len(item.Operations))
	hasOneTimeResults := false
	for index := range item.Operations {
		op := &item.Operations[index]
		handler := s.handler(op.Capability)
		if handler == nil {
			return s.failOperation(ctx, item, op, "capability_unavailable", "capability is not executable in this Controller build")
		}
		result, applyErr := handler(ctx, principal, op.Input)
		completed := s.now().UTC()
		op.CompletedAt = &completed
		if applyErr != nil {
			op.Status, op.ErrorCode, op.ErrorMessage = "failed", "apply_failed", applyErr.Error()
			_ = s.store.UpdateAutomationOperation(ctx, op)
			return s.failChangeset(ctx, item, applyErr)
		}
		publicResult, responseResult := result, result
		if protected, ok := result.(MutationResult); ok {
			publicResult = protected.Public
			if protected.OneTime != nil {
				responseResult = protected.OneTime
				hasOneTimeResults = true
			}
		}
		op.Status, op.Result = "succeeded", mustJSON(publicResult)
		if err := s.store.UpdateAutomationOperation(ctx, op); err != nil {
			return s.failChangeset(ctx, item, err)
		}
		persistedResults = append(persistedResults, publicResult)
		responseResults = append(responseResults, responseResult)
	}
	completed := s.now().UTC()
	item.Status, item.CompletedAt = model.ChangesetSucceeded, &completed
	item.Result = mustJSON(map[string]any{"operations": persistedResults})
	if err := s.store.UpdateAutomationChangeset(ctx, item); err != nil {
		return nil, err
	}
	if hasOneTimeResults {
		item.Result = mustJSON(map[string]any{"operations": responseResults})
	}
	return item, nil
}

func (s *Service) Get(ctx context.Context, id string) (*model.AutomationChangeset, error) {
	return s.store.GetAutomationChangeset(ctx, id)
}

func (s *Service) GetOperation(ctx context.Context, principal application.Principal, id string) (*model.AutomationOperation, error) {
	operation, err := s.store.GetAutomationOperation(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizedChangeset(ctx, principal, operation.ChangesetID); err != nil {
		return nil, sql.ErrNoRows
	}
	return operation, nil
}

func (s *Service) List(ctx context.Context, principal application.Principal, limit int) ([]model.AutomationChangeset, error) {
	if principal.Interactive && principal.Role == model.RoleAdmin {
		return s.store.ListAutomationChangesets(ctx, "", limit)
	}
	return s.store.ListAutomationChangesets(ctx, principal.ID, limit)
}

type StartWorkflowRequest struct {
	Kind           string
	Reason         string
	IdempotencyKey string
	ChangesetID    string
	ExternalAction bool
}

func (s *Service) StartWorkflow(ctx context.Context, principal application.Principal, request StartWorkflowRequest) (*model.AutomationWorkflow, error) {
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" || len(request.IdempotencyKey) > 128 {
		return nil, errors.New("workflow idempotency_key is required and must not exceed 128 characters")
	}
	if existing, err := s.store.FindAutomationWorkflowByIdempotency(ctx, principal.ID, request.IdempotencyKey); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	changeset, err := s.authorizedChangeset(ctx, principal, strings.TrimSpace(request.ChangesetID))
	if err != nil {
		return nil, err
	}
	id, err := prefixedID("wf")
	if err != nil {
		return nil, err
	}
	stepID, err := prefixedID("wfs")
	if err != nil {
		return nil, err
	}
	correlationID, err := prefixedID("corr")
	if err != nil {
		return nil, err
	}
	status, stepStatus, currentStep, completedAt := workflowState(changeset, request.ExternalAction, s.now().UTC())
	nextAction := workflowNextAction(changeset, request.ExternalAction)
	accessChangeID := int64(0)
	if strings.TrimSpace(request.Kind) == "access_change" && changeset.Status == model.ChangesetSucceeded {
		status, stepStatus, currentStep, completedAt, nextAction, accessChangeID, err = s.accessChangeWorkflowState(ctx, changeset, s.now().UTC())
		if err != nil {
			return nil, err
		}
	}
	inputDigest := sha256.Sum256([]byte(changeset.ID + "\x00" + changeset.PlanHash))
	outputDigest := sha256.Sum256(changeset.Result)
	affectedResources := []map[string]any{{"type": "changeset", "id": changeset.ID}}
	if accessChangeID > 0 {
		affectedResources = append(affectedResources, map[string]any{"type": "access_change", "id": accessChangeID})
	}
	if serverID := workflowExternalServerID(nextAction); serverID > 0 {
		affectedResources = append(affectedResources, map[string]any{"type": "server", "id": serverID})
	}
	item := &model.AutomationWorkflow{
		ID: id, PrincipalID: principal.ID, GrantID: principal.GrantID, Kind: strings.TrimSpace(request.Kind), Status: status,
		Reason: strings.TrimSpace(request.Reason), IdempotencyKey: request.IdempotencyKey, ChangesetID: changeset.ID,
		CurrentStep: currentStep, CorrelationID: correlationID, AffectedResources: mustJSON(affectedResources),
		NextAction: nextAction, CompletedAt: completedAt,
		Steps: []model.AutomationWorkflowStep{{ID: stepID, Position: 1, Name: "changeset", Status: stepStatus, Attempt: 1, IdempotencyKey: request.IdempotencyKey + ":changeset", InputDigest: hex.EncodeToString(inputDigest[:]), OutputDigest: hex.EncodeToString(outputDigest[:]), Retryable: false, NextAction: nextAction, CorrelationID: correlationID}},
	}
	if item.Kind == "" {
		item.Kind = "changeset"
	}
	now := s.now().UTC()
	item.Steps[0].StartedAt = &now
	if stepStatus == "succeeded" || stepStatus == "failed" {
		item.Steps[0].FinishedAt = &now
	}
	if err := s.store.CreateAutomationWorkflow(ctx, item); err != nil {
		if existing, findErr := s.store.FindAutomationWorkflowByIdempotency(ctx, principal.ID, request.IdempotencyKey); findErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return item, nil
}

func (s *Service) GetWorkflow(ctx context.Context, principal application.Principal, id string) (*model.AutomationWorkflow, error) {
	item, err := s.store.GetAutomationWorkflow(ctx, strings.TrimSpace(id))
	if err != nil || item.PrincipalID != principal.ID && !(principal.Interactive && principal.Role == model.RoleAdmin) {
		return nil, sql.ErrNoRows
	}
	return s.synchronizeWorkflow(ctx, item)
}

func (s *Service) synchronizeWorkflow(ctx context.Context, item *model.AutomationWorkflow) (*model.AutomationWorkflow, error) {
	if item.Status == model.WorkflowCancelled || item.Status == model.WorkflowSucceeded || item.Status == model.WorkflowFailed || item.Status == model.WorkflowPartiallySucceeded || len(item.Steps) == 0 {
		return item, nil
	}
	now := s.now().UTC()
	if item.Kind != "access_change" && (item.Status == model.WorkflowExternalActionRequired || item.Status == model.WorkflowWaitingForAgent) {
		serverID := workflowExternalServerID(item.NextAction)
		server, err := s.store.GetServer(ctx, serverID)
		if err != nil || server.Status != model.ServerOnline {
			return item, nil
		}
		step := &item.Steps[0]
		step.Status, step.Retryable, step.NextAction, step.FinishedAt = "succeeded", false, json.RawMessage(`{}`), &now
		item.Status, item.CurrentStep, item.NextAction, item.CompletedAt = model.WorkflowSucceeded, "", json.RawMessage(`{}`), &now
		if err := s.store.UpdateAutomationWorkflowAndStep(ctx, item, step); err != nil {
			return nil, err
		}
		return item, nil
	}
	changeset, err := s.store.GetAutomationChangeset(ctx, item.ChangesetID)
	if err != nil {
		return nil, err
	}
	status, stepStatus, currentStep, completedAt := workflowState(changeset, false, now)
	nextAction := workflowNextAction(changeset, false)
	if item.Kind == "access_change" && changeset.Status == model.ChangesetSucceeded {
		var accessChangeID int64
		status, stepStatus, currentStep, completedAt, nextAction, accessChangeID, err = s.accessChangeWorkflowState(ctx, changeset, now)
		if err != nil {
			return nil, err
		}
		if accessChangeID > 0 && !workflowHasAffectedResource(item.AffectedResources, "access_change", accessChangeID) {
			var affected []map[string]any
			_ = json.Unmarshal(item.AffectedResources, &affected)
			affected = append(affected, map[string]any{"type": "access_change", "id": accessChangeID})
			item.AffectedResources = mustJSON(affected)
		}
	}
	step := &item.Steps[0]
	if item.Status == status && step.Status == stepStatus && item.CurrentStep == currentStep && string(item.NextAction) == string(nextAction) {
		return item, nil
	}
	outputDigest := sha256.Sum256(changeset.Result)
	step.Status, step.OutputDigest, step.NextAction = stepStatus, hex.EncodeToString(outputDigest[:]), nextAction
	step.Retryable, step.ErrorCode = false, ""
	if stepStatus == "succeeded" || stepStatus == "failed" {
		step.FinishedAt = &now
	} else {
		step.FinishedAt = nil
	}
	item.Status, item.CurrentStep, item.NextAction, item.CompletedAt = status, currentStep, nextAction, completedAt
	if status == model.WorkflowFailed {
		item.ErrorCode = "changeset_" + string(changeset.Status)
	} else {
		item.ErrorCode, item.ErrorMessage = "", ""
	}
	if err := s.store.UpdateAutomationWorkflowAndStep(ctx, item, step); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) accessChangeWorkflowState(ctx context.Context, changeset *model.AutomationChangeset, now time.Time) (model.WorkflowStatus, string, string, *time.Time, json.RawMessage, int64, error) {
	var result struct {
		Operations []struct {
			AccessChangeID int64 `json:"access_change_id"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(changeset.Result, &result); err != nil {
		return "", "", "", nil, nil, 0, err
	}
	accessChangeID := int64(0)
	for _, operation := range result.Operations {
		if operation.AccessChangeID > 0 {
			accessChangeID = operation.AccessChangeID
			break
		}
	}
	if accessChangeID == 0 {
		return model.WorkflowSucceeded, "succeeded", "", &now, json.RawMessage(`{}`), 0, nil
	}
	change, err := s.store.GetAccessChange(ctx, accessChangeID)
	if err != nil {
		return "", "", "", nil, nil, 0, err
	}
	nextAction := mustJSON(map[string]any{"type": "wait", "resource_type": "access_change", "resource_id": accessChangeID, "status": change.Status})
	switch change.Status {
	case model.AccessChangeFinalized:
		return model.WorkflowSucceeded, "succeeded", "", &now, json.RawMessage(`{}`), accessChangeID, nil
	case model.AccessChangeFailed, model.AccessChangeCancelled:
		return model.WorkflowFailed, "failed", "", &now, json.RawMessage(`{}`), accessChangeID, nil
	default:
		return model.WorkflowWaitingForAgent, "running", "access_change", nil, nextAction, accessChangeID, nil
	}
}

func workflowHasAffectedResource(raw json.RawMessage, resourceType string, id int64) bool {
	var affected []map[string]any
	if json.Unmarshal(raw, &affected) != nil {
		return false
	}
	for _, resource := range affected {
		if fmt.Sprint(resource["type"]) == resourceType && fmt.Sprint(resource["id"]) == strconv.FormatInt(id, 10) {
			return true
		}
	}
	return false
}

func (s *Service) RequireWorkflowExternalAction(ctx context.Context, principal application.Principal, id string, changeset *model.AutomationChangeset) (*model.AutomationWorkflow, error) {
	item, err := s.store.GetAutomationWorkflow(ctx, strings.TrimSpace(id))
	if err != nil || item.PrincipalID != principal.ID && !(principal.Interactive && principal.Role == model.RoleAdmin) {
		return nil, sql.ErrNoRows
	}
	if changeset == nil || changeset.ID != item.ChangesetID || changeset.Status != model.ChangesetSucceeded || len(item.Steps) == 0 {
		return nil, errors.New("workflow cannot require an external action for this changeset")
	}
	nextAction := workflowNextAction(changeset, true)
	step := &item.Steps[0]
	step.Status, step.NextAction, step.Retryable, step.ErrorCode, step.FinishedAt = "external_action_required", nextAction, false, "", nil
	item.Status, item.CurrentStep, item.NextAction, item.CompletedAt = model.WorkflowExternalActionRequired, "external_install", nextAction, nil
	item.ErrorCode, item.ErrorMessage = "", ""
	if serverID := workflowExternalServerID(nextAction); serverID > 0 {
		var affected []map[string]any
		_ = json.Unmarshal(item.AffectedResources, &affected)
		found := false
		for _, resource := range affected {
			if resource["type"] == "server" && fmt.Sprint(resource["id"]) == fmt.Sprint(serverID) {
				found = true
				break
			}
		}
		if !found {
			affected = append(affected, map[string]any{"type": "server", "id": serverID})
			item.AffectedResources = mustJSON(affected)
		}
	}
	if err := s.store.UpdateAutomationWorkflowAndStep(ctx, item, step); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) CancelWorkflow(ctx context.Context, principal application.Principal, id string) (*model.AutomationWorkflow, error) {
	item, err := s.GetWorkflow(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	switch item.Status {
	case model.WorkflowSucceeded, model.WorkflowPartiallySucceeded, model.WorkflowFailed, model.WorkflowCancelled:
		return nil, errors.New("workflow cannot be cancelled in its current state")
	}
	now := s.now().UTC()
	item.Status, item.CurrentStep, item.CompletedAt = model.WorkflowCancelled, "", &now
	item.NextAction = json.RawMessage(`{}`)
	step := &item.Steps[0]
	step.Status, step.Retryable, step.NextAction, step.FinishedAt = "cancelled", false, json.RawMessage(`{}`), &now
	if err := s.store.UpdateAutomationWorkflowAndStep(ctx, item, step); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) RetryWorkflowStep(ctx context.Context, principal application.Principal, workflowID, stepID string) (*model.AutomationWorkflow, error) {
	item, err := s.GetWorkflow(ctx, principal, workflowID)
	if err != nil {
		return nil, err
	}
	for index := range item.Steps {
		step := &item.Steps[index]
		if step.ID != strings.TrimSpace(stepID) {
			continue
		}
		if !step.Retryable || step.Status != "failed" {
			return nil, errors.New("workflow step is not retryable")
		}
		step.Attempt++
		step.Status, step.ErrorCode, step.Retryable = "queued", "", false
		now := s.now().UTC()
		step.StartedAt, step.FinishedAt = &now, nil
		if err := s.store.UpdateAutomationWorkflowStep(ctx, step); err != nil {
			return nil, err
		}
		item.Status, item.CurrentStep, item.CompletedAt = model.WorkflowQueued, step.Name, nil
		if err := s.store.UpdateAutomationWorkflow(ctx, item); err != nil {
			return nil, err
		}
		return item, nil
	}
	return nil, sql.ErrNoRows
}

func workflowState(changeset *model.AutomationChangeset, externalAction bool, now time.Time) (model.WorkflowStatus, string, string, *time.Time) {
	if externalAction {
		return model.WorkflowExternalActionRequired, "external_action_required", "external_install", nil
	}
	switch changeset.Status {
	case model.ChangesetDraft, model.ChangesetValidated:
		return model.WorkflowPlanning, "running", "changeset", nil
	case model.ChangesetAwaitingApproval:
		return model.WorkflowApprovalRequired, "approval_required", "approval", nil
	case model.ChangesetApproved:
		return model.WorkflowQueued, "queued", "changeset", nil
	case model.ChangesetApplying:
		return model.WorkflowRunning, "running", "changeset", nil
	case model.ChangesetSucceeded:
		return model.WorkflowSucceeded, "succeeded", "", &now
	case model.ChangesetFailed, model.ChangesetExpired, model.ChangesetSuperseded, model.ChangesetRollbackFailed:
		return model.WorkflowFailed, "failed", "", &now
	default:
		return model.WorkflowPlanning, "running", "changeset", nil
	}
}

func workflowNextAction(changeset *model.AutomationChangeset, externalAction bool) json.RawMessage {
	if externalAction {
		var result struct {
			Operations []struct {
				Server struct {
					ID int64 `json:"id"`
				} `json:"server"`
				EnrollmentExpiresAt string `json:"enrollment_expires_at"`
			} `json:"operations"`
		}
		if json.Unmarshal(changeset.Result, &result) == nil {
			for _, operation := range result.Operations {
				if operation.Server.ID > 0 {
					return mustJSON(map[string]any{"type": "execute_on_target", "server_id": operation.Server.ID, "expires_at": operation.EnrollmentExpiresAt, "sensitive": true, "completion_condition": map[string]any{"resource_uri": fmt.Sprintf("oboard://servers/%d/health", operation.Server.ID), "field": "agent_connected", "equals": true}})
				}
			}
		}
		return mustJSON(map[string]any{"type": "execute_on_target", "sensitive": true})
	}
	if changeset.Status == model.ChangesetAwaitingApproval {
		return mustJSON(map[string]any{"type": "open_approval", "changeset_id": changeset.ID})
	}
	return json.RawMessage(`{}`)
}

func workflowExternalServerID(nextAction json.RawMessage) int64 {
	var action struct {
		ServerID int64 `json:"server_id"`
	}
	if json.Unmarshal(nextAction, &action) != nil {
		return 0
	}
	return action.ServerID
}

func (s *Service) automaticAllowed(ctx context.Context, principal application.Principal, item *model.AutomationChangeset) (bool, error) {
	if principal.Interactive {
		return s.policiesAllowAutomatic(ctx, item, false)
	}
	storedPrincipal, err := s.store.GetAPIPrincipal(ctx, item.PrincipalID)
	if err != nil {
		return false, err
	}
	return s.policiesAllowAutomatic(ctx, item, storedPrincipal.Type == model.APIPrincipalOAuth)
}

func (s *Service) policiesAllowAutomatic(ctx context.Context, item *model.AutomationChangeset, oauthPrincipal bool) (bool, error) {
	automatic := true
	for _, operation := range item.Operations {
		policy, err := s.store.GetApprovalPolicy(ctx, item.PrincipalID, operation.Capability, s.now().UTC())
		if errors.Is(err, sql.ErrNoRows) {
			automatic = false
			continue
		}
		if err != nil {
			return false, err
		}
		if policy.Mode == model.ApprovalDenied {
			if oauthPrincipal {
				// Legacy OAuth consent could persist a per-capability denial.
				// OAuth MCP now inherits the user's live RBAC role, so the old
				// denial can require human approval but cannot remove access.
				automatic = false
				continue
			}
			return false, fmt.Errorf("approval policy denies capability %q", operation.Capability)
		}
		if policy.Mode != model.ApprovalAutomatic || operation.RiskClass >= 4 && (oauthPrincipal || !policy.AllowRisk4) {
			automatic = false
			continue
		}
		if !approvalFilterAllows(policy.ResourceFilter, operation.ResourceRefs) {
			automatic = false
		}
	}
	return automatic, nil
}

func approvalFilterAllows(policyFilter, operationRefs json.RawMessage) bool {
	if len(policyFilter) == 0 || string(policyFilter) == "{}" || string(policyFilter) == "null" {
		return true
	}
	var policy, refs map[string]json.RawMessage
	if json.Unmarshal(policyFilter, &policy) != nil || json.Unmarshal(operationRefs, &refs) != nil {
		return false
	}
	for key, allowedRaw := range policy {
		actualRaw, ok := refs[key]
		if !ok {
			return false
		}
		var allowedValues, actualValues []int64
		if json.Unmarshal(allowedRaw, &allowedValues) == nil && json.Unmarshal(actualRaw, &actualValues) == nil {
			allowed := map[int64]bool{}
			for _, value := range allowedValues {
				allowed[value] = true
			}
			for _, value := range actualValues {
				if !allowed[value] {
					return false
				}
			}
			continue
		}
		if string(allowedRaw) != string(actualRaw) {
			return false
		}
	}
	return true
}

func (s *Service) authorizedChangeset(ctx context.Context, principal application.Principal, id string) (*model.AutomationChangeset, error) {
	item, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.PrincipalID != principal.ID && !(principal.Interactive && principal.Role == model.RoleAdmin) {
		return nil, sql.ErrNoRows
	}
	return item, nil
}

func (s *Service) handler(name string) MutationHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handlers[name]
}

func (s *Service) validator(name string) MutationHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.validators[name]
}

func (s *Service) revisionResolver(name string) RevisionResolver {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revisionResolvers[name]
}

func (s *Service) validateOperations(ctx context.Context, principal application.Principal, item *model.AutomationChangeset) ([]any, error) {
	expected, err := decodeRevisions(item.BaseRevisions)
	if err != nil {
		return nil, err
	}
	evidence, current, err := s.inspectOperations(ctx, principal, item)
	if err != nil {
		return nil, err
	}
	if err := compareRevisions(expected, current); err != nil {
		return nil, err
	}
	return evidence, nil
}

func (s *Service) inspectOperations(ctx context.Context, principal application.Principal, item *model.AutomationChangeset) ([]any, map[string]string, error) {
	current := map[string]string{}
	evidence := make([]any, 0, len(item.Operations))
	for _, operation := range item.Operations {
		descriptor, ok := s.catalog.Authorize(principal, operation.Capability)
		if !ok || descriptor.ReadOnly || !descriptor.Executable || descriptor.RiskClass != operation.RiskClass {
			return nil, nil, fmt.Errorf("operation capability %q is no longer authorized", operation.Capability)
		}
		validator := s.validator(operation.Capability)
		if validator == nil || s.handler(operation.Capability) == nil {
			return nil, nil, fmt.Errorf("operation capability %q is not executable in this Controller build", operation.Capability)
		}
		validated, validateErr := validator(ctx, principal, operation.Input)
		if validateErr != nil {
			return nil, nil, fmt.Errorf("validate %s: %w", operation.Capability, validateErr)
		}
		if resolver := s.revisionResolver(operation.Capability); resolver != nil {
			resolved, resolveErr := resolver(ctx, principal, operation.Input)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("resolve %s base revisions: %w", operation.Capability, resolveErr)
			}
			for key, revision := range resolved {
				if existing, duplicate := current[key]; duplicate && existing != revision {
					return nil, nil, fmt.Errorf("resource %q resolved to conflicting revisions", key)
				}
				current[key] = revision
			}
		}
		evidence = append(evidence, map[string]any{"operation_id": operation.ID, "capability": operation.Capability, "evidence": validated})
	}
	return evidence, current, nil
}

func (s *Service) failOperation(ctx context.Context, item *model.AutomationChangeset, operation *model.AutomationOperation, code, message string) (*model.AutomationChangeset, error) {
	now := s.now().UTC()
	operation.Status, operation.ErrorCode, operation.ErrorMessage, operation.CompletedAt = "failed", code, message, &now
	_ = s.store.UpdateAutomationOperation(ctx, operation)
	return s.failChangeset(ctx, item, errors.New(message))
}

func (s *Service) failChangeset(ctx context.Context, item *model.AutomationChangeset, applyErr error) (*model.AutomationChangeset, error) {
	now := s.now().UTC()
	item.Status, item.CompletedAt = model.ChangesetFailed, &now
	item.Result = mustJSON(map[string]any{"error": applyErr.Error()})
	_ = s.store.UpdateAutomationChangeset(ctx, item)
	return item, applyErr
}

func changesetHash(item *model.AutomationChangeset) (string, error) {
	type hashOperation struct {
		Capability   string          `json:"capability"`
		Input        json.RawMessage `json:"input"`
		ResourceRefs json.RawMessage `json:"resource_refs"`
		RiskClass    int             `json:"risk_class"`
	}
	operations := make([]hashOperation, 0, len(item.Operations))
	for _, operation := range item.Operations {
		operations = append(operations, hashOperation{Capability: operation.Capability, Input: operation.Input, ResourceRefs: operation.ResourceRefs, RiskClass: operation.RiskClass})
	}
	payload, err := json.Marshal(struct {
		BaseRevisions json.RawMessage `json:"base_revisions"`
		Operations    []hashOperation `json:"operations"`
	}{item.BaseRevisions, operations})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("input must be a JSON object")
	}
	encoded, err := json.Marshal(value)
	return json.RawMessage(encoded), err
}

func normalizedObject(raw json.RawMessage) json.RawMessage {
	value, err := canonicalObject(raw)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return value
}

func canonicalRevisions(raw json.RawMessage) (json.RawMessage, error) {
	revisions, err := decodeRevisions(raw)
	if err != nil {
		return nil, fmt.Errorf("base_revisions: %w", err)
	}
	encoded, err := json.Marshal(revisions)
	return json.RawMessage(encoded), err
}

func decodeRevisions(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]string{}, nil
	}
	var revisions map[string]string
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&revisions); err != nil || revisions == nil {
		return nil, errors.New("must be an object whose values are revision strings")
	}
	for key, revision := range revisions {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(revision) == "" {
			return nil, errors.New("resource keys and revision values must not be empty")
		}
	}
	return revisions, nil
}

func compareRevisions(expected, current map[string]string) error {
	if len(expected) != len(current) {
		return fmt.Errorf("base revisions do not cover the current resource set")
	}
	for key, revision := range current {
		if expected[key] != revision {
			return fmt.Errorf("resource %q revision changed", key)
		}
	}
	return nil
}

func prefixedID(prefix string) (string, error) {
	random, err := security.RandomToken(18)
	if err != nil {
		return "", err
	}
	return prefix + "_" + random, nil
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func blastRadius(operations []model.AutomationOperation) map[string]any {
	capabilities := make([]string, 0, len(operations))
	resources := map[string]struct{}{}
	for _, operation := range operations {
		capabilities = append(capabilities, operation.Capability)
		var refs map[string]any
		if json.Unmarshal(operation.ResourceRefs, &refs) == nil {
			for key := range refs {
				resources[key] = struct{}{}
			}
		}
	}
	sort.Strings(capabilities)
	resourceKinds := make([]string, 0, len(resources))
	for key := range resources {
		resourceKinds = append(resourceKinds, key)
	}
	sort.Strings(resourceKinds)
	return map[string]any{"operation_count": len(operations), "capabilities": capabilities, "resource_kinds": resourceKinds}
}
