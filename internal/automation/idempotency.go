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
	s.mu.RLock()
	authorizer := s.replayAuthorizer
	s.mu.RUnlock()
	for _, op := range existing.Operations {
		if authorizer != nil {
			if err := authorizer(ctx, principal, op); err != nil {
				return nil, err
			}
		} else if principal.AccessLevel != "" {
			return nil, errors.New("replay is not authorized without a current grant check")
		}
		filter := strings.TrimSpace(string(principal.ResourceFilter))
		if principal.Type == model.APIPrincipalScript || filter != "" && filter != "{}" && filter != "null" {
			// Client-supplied refs cannot prove permission to read a previous
			// result. Restricted callers must pass the current domain check.
			validator := s.validator(op.Capability)
			if validator == nil {
				return nil, errors.New("replay resource is not authorized")
			}
			if _, err := validator(ctx, principal, op.Input); err != nil {
				return nil, errors.New("replay resource is not authorized")
			}
		}
	}
	return existing, nil
}
