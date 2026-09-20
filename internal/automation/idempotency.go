package automation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

var ErrIdempotencyConflict = errors.New("idempotency key is already bound to a different request")

// Request identity uses immutable request fields, not the approval plan hash or
// execution results. Both normal replay and a racing unique-key insert use it.
func changesetRequestDigest(item *model.AutomationChangeset) ([32]byte, error) {
	request := CreateRequest{Reason: item.Reason, BaseRevisions: item.BaseRevisions, AutoApply: item.AutoApply, Operations: make([]OperationRequest, 0, len(item.Operations))}
	var err error
	request.BaseRevisions, err = canonicalRevisions(request.BaseRevisions)
	if err != nil {
		return [32]byte{}, err
	}
	for _, op := range item.Operations {
		input, err := canonicalObject(op.Input)
		if err != nil {
			return [32]byte{}, err
		}
		refs, err := canonicalObject(op.ResourceRefs)
		if err != nil {
			return [32]byte{}, err
		}
		request.Operations = append(request.Operations, OperationRequest{Capability: op.Capability, Input: input, ResourceRefs: refs, SecretRefs: op.SecretRefs})
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func (s *Service) replayChangeset(ctx context.Context, principal application.Principal, existing, requested *model.AutomationChangeset) (*model.AutomationChangeset, error) {
	want, err := changesetRequestDigest(requested)
	if err != nil {
		return nil, err
	}
	got, err := changesetRequestDigest(existing)
	if err != nil {
		return nil, err
	}
	if want != got {
		return nil, ErrIdempotencyConflict
	}
	if err := s.authorizeChangesetResult(ctx, principal, existing, false); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *Service) authorizeChangesetResult(ctx context.Context, principal application.Principal, item *model.AutomationChangeset, readOnly bool) error {
	s.mu.RLock()
	authorizer := s.replayAuthorizer
	if readOnly && principal.AccessLevel != "" {
		authorizer = s.resultAuthorizer
	}
	s.mu.RUnlock()
	for _, op := range item.Operations {
		if !readOnly || principal.AccessLevel == "" {
			if _, authorized := s.catalog.Authorize(principal, op.Capability); !authorized {
				return errors.New("operation result is not authorized")
			}
		}
		if authorizer != nil {
			if err := authorizer(ctx, principal, op); err != nil {
				return err
			}
		} else if principal.AccessLevel != "" {
			return errors.New("replay is not authorized without a current grant check")
		}
		// The OAuth authorizer resolves current resource permissions without
		// repeating mutation validation (a saved create now has an existing name).
		if principal.AccessLevel != "" {
			continue
		}
		filter := strings.TrimSpace(string(principal.ResourceFilter))
		if principal.Type == model.APIPrincipalPlugin || filter != "" && filter != "{}" && filter != "null" {
			// Client-supplied refs cannot prove permission to read a previous
			// result. Restricted callers must pass the current domain check.
			validator := s.validator(op.Capability)
			if validator == nil {
				return errors.New("replay resource is not authorized")
			}
			if _, err := validator(ctx, principal, op.Input); err != nil {
				return errors.New("replay resource is not authorized")
			}
		}
	}
	return nil
}

func replayWorkflow(existing *model.AutomationWorkflow, request StartWorkflowRequest) (*model.AutomationWorkflow, error) {
	if existing.Kind != request.Kind || existing.Reason != request.Reason || existing.ChangesetID != request.ChangesetID {
		return nil, ErrIdempotencyConflict
	}
	return existing, nil
}
