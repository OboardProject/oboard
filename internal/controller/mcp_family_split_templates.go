package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

var familySplitTemplateAutomationFields = map[string]bool{
	"name": true,
}

type familySplitTemplateOperationInput struct {
	FamilySplitTemplate   json.RawMessage `json:"family_split_template,omitempty"`
	FamilySplitTemplateID int64           `json:"family_split_template_id,omitempty"`
	Changes               json.RawMessage `json:"changes,omitempty"`
	Confirm               bool            `json:"confirm,omitempty"`
}

func (s *Server) registerFamilySplitTemplateOperations() {
	for _, name := range []string{"family_split_templates.create", "family_split_templates.update", "family_split_templates.delete"} {
		operation := name
		s.automation.RegisterValidator(operation, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			item, changed, err := s.familySplitTemplateAutomationCandidate(ctx, input, operation)
			if err != nil {
				return nil, err
			}
			return automationFamilySplitTemplateResult(item, changed), nil
		})
		s.automation.RegisterRevisionResolver(operation, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			item, _, err := s.familySplitTemplateAutomationCandidate(ctx, input, operation)
			if err != nil {
				return nil, err
			}
			if operation == "family_split_templates.create" {
				return map[string]string{}, nil
			}
			return map[string]string{"family_split_template:" + strconv.FormatInt(item.ID, 10): item.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		})
		s.automation.Register(operation, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			item, changed, err := s.familySplitTemplateAutomationCandidate(ctx, input, operation)
			if err != nil {
				return nil, err
			}
			switch operation {
			case "family_split_templates.create":
				ipv4Secret, err := security.RandomToken(24)
				if err != nil {
					return nil, err
				}
				ipv6Secret, err := security.RandomToken(24)
				if err != nil {
					return nil, err
				}
				if err := s.store.CreateFamilySplitTemplate(ctx, &item, ipv4Secret, ipv6Secret); err != nil {
					return nil, err
				}
			case "family_split_templates.update":
				if err := s.store.UpdateFamilySplitTemplate(ctx, &item); err != nil {
					return nil, err
				}
			case "family_split_templates.delete":
				if err := s.store.DeleteFamilySplitTemplate(ctx, item.ID); err != nil {
					return nil, err
				}
				return map[string]any{"deleted": true, "family_split_template_id": item.ID}, nil
			}
			return automationFamilySplitTemplateResult(item, changed), nil
		})
	}
}

func (s *Server) familySplitTemplateAutomationCandidate(ctx context.Context, input json.RawMessage, operation string) (model.FamilySplitTemplate, []string, error) {
	var request familySplitTemplateOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.FamilySplitTemplate{}, nil, err
	}
	switch operation {
	case "family_split_templates.create":
		if len(request.FamilySplitTemplate) == 0 {
			return model.FamilySplitTemplate{}, nil, errors.New("family_split_template object is required")
		}
		if _, err := decodeClosedAutomationFields(request.FamilySplitTemplate, familySplitTemplateAutomationFields, "family_split_template"); err != nil {
			return model.FamilySplitTemplate{}, nil, err
		}
		var item model.FamilySplitTemplate
		if err := json.Unmarshal(request.FamilySplitTemplate, &item); err != nil {
			return model.FamilySplitTemplate{}, nil, err
		}
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			return model.FamilySplitTemplate{}, nil, errors.New("name required")
		}
		return item, nil, nil
	case "family_split_templates.update":
		if request.FamilySplitTemplateID <= 0 || len(request.Changes) == 0 {
			return model.FamilySplitTemplate{}, nil, errors.New("family_split_template_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, familySplitTemplateAutomationFields, "changes")
		if err != nil {
			return model.FamilySplitTemplate{}, nil, err
		}
		current, err := s.store.GetFamilySplitTemplate(ctx, request.FamilySplitTemplateID)
		if err != nil {
			return model.FamilySplitTemplate{}, nil, err
		}
		var patch model.FamilySplitTemplate
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.FamilySplitTemplate{}, nil, err
		}
		if _, ok := fields["name"]; ok {
			current.Name = patch.Name
		}
		changed := make([]string, 0, len(fields))
		for field := range fields {
			changed = append(changed, field)
		}
		return *current, changed, nil
	case "family_split_templates.delete":
		if request.FamilySplitTemplateID <= 0 || !request.Confirm {
			return model.FamilySplitTemplate{}, nil, errors.New("family_split_template_id and confirm=true are required")
		}
		current, err := s.store.GetFamilySplitTemplate(ctx, request.FamilySplitTemplateID)
		if err != nil {
			return model.FamilySplitTemplate{}, nil, err
		}
		return *current, nil, nil
	default:
		return model.FamilySplitTemplate{}, nil, errors.New("unsupported family split template operation")
	}
}

func automationFamilySplitTemplateResult(item model.FamilySplitTemplate, changed []string) any {
	view := map[string]any{
		"id": item.ID, "revision": item.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"name": item.Name, "ipv4_path_id": item.IPv4PathID, "ipv6_path_id": item.IPv6PathID,
		"created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
	}
	if len(changed) == 0 {
		return map[string]any{"family_split_template": view}
	}
	return map[string]any{"family_split_template": view, "changed_fields": changed}
}
