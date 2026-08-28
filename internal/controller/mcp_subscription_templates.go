package controller

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) registerSubscriptionTemplateOperations() {
	for _, name := range []string{"subscription_templates.update", "subscription_templates.reset"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.subscriptionTemplateAutomationValidate(ctx, input, name)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return map[string]string{}, nil
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applySubscriptionTemplateOperation(ctx, principal, input, name)
		})
	}
}

type subscriptionTemplateOperationInput struct {
	Format           string `json:"format"`
	Content          string `json:"content"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *Server) subscriptionTemplateAutomationValidate(ctx context.Context, input json.RawMessage, name string) (any, error) {
	var request subscriptionTemplateOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	format := core.NormalizeSubscriptionFormatForAPI(model.SubscriptionFormat(request.Format))
	if !core.IsConcreteSubscriptionFormat(format) {
		return nil, errors.New("format is required")
	}
	if name == "subscription_templates.update" {
		if err := core.ValidateSubscriptionTemplateWithPreview(format, request.Content); err != nil {
			return nil, err
		}
	}
	item, err := s.store.GetSubscriptionClientTemplate(ctx, format)
	if err != nil {
		return nil, err
	}
	return map[string]any{"subscription_template": item}, nil
}

func (s *Server) applySubscriptionTemplateOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	var request subscriptionTemplateOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	format := core.NormalizeSubscriptionFormatForAPI(model.SubscriptionFormat(request.Format))
	switch name {
	case "subscription_templates.update":
		actorID := int64(0)
		if principal.UserID != nil {
			actorID = *principal.UserID
		}
		item, err := s.store.PutSubscriptionClientTemplate(ctx, format, request.Content, request.ExpectedRevision, actorID)
		if err != nil {
			if errors.Is(err, store.ErrSubscriptionTemplateConflict) {
				return nil, err
			}
			return nil, err
		}
		return map[string]any{"subscription_template": item}, nil
	case "subscription_templates.reset":
		item, err := s.store.ResetSubscriptionClientTemplate(ctx, format)
		if err != nil {
			return nil, err
		}
		return map[string]any{"subscription_template": item}, nil
	default:
		return nil, errors.New("unsupported subscription template operation")
	}
}
