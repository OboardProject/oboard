package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

var routingRuleSetAutomationFields = map[string]bool{
	"name": true, "url": true, "format": true, "mihomo_behavior": true,
}

type routingRuleSetOperationInput struct {
	RoutingRuleSet   json.RawMessage `json:"routing_rule_set,omitempty"`
	RoutingRuleSetID int64           `json:"routing_rule_set_id,omitempty"`
	Changes          json.RawMessage `json:"changes,omitempty"`
	Confirm          bool            `json:"confirm,omitempty"`
}

func (s *Server) registerRoutingRuleSetOperations() {
	for _, name := range []string{"routing_rule_sets.create", "routing_rule_sets.update", "routing_rule_sets.delete", "routing_rule_sets.refresh"} {
		operation := name
		s.automation.RegisterValidator(operation, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			item, changed, err := s.routingRuleSetAutomationCandidate(ctx, input, operation)
			if err != nil {
				return nil, err
			}
			return automationRoutingRuleSetResult(item, changed), nil
		})
		s.automation.RegisterRevisionResolver(operation, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			item, _, err := s.routingRuleSetAutomationCandidate(ctx, input, operation)
			if err != nil {
				return nil, err
			}
			if operation == "routing_rule_sets.create" {
				return map[string]string{}, nil
			}
			return map[string]string{"routing_rule_set:" + strconv.FormatInt(item.ID, 10): item.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		})
		s.automation.Register(operation, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			item, changed, err := s.routingRuleSetAutomationCandidate(ctx, input, operation)
			if err != nil {
				return nil, err
			}
			switch operation {
			case "routing_rule_sets.create":
				fetched, err := s.fetchRoutingRuleSetSnapshot(ctx, item, false)
				if err != nil {
					return nil, err
				}
				now := time.Now().UTC()
				item.Content, item.Revision, item.ETag, item.LastModified = fetched.content, fetched.revision, fetched.etag, fetched.lastModified
				item.Status, item.LastAttemptAt, item.LastSuccessAt = model.RoutingRuleSetStatusReady, &now, &now
				if err := s.store.CreateRoutingRuleSet(ctx, &item); err != nil {
					return nil, err
				}
			case "routing_rule_sets.update":
				current, err := s.store.GetRoutingRuleSet(ctx, item.ID)
				if err != nil {
					return nil, err
				}
				contentChanged := false
				if item.URL != current.URL || item.Format != current.Format || item.MihomoBehavior != current.MihomoBehavior {
					fetched, err := s.fetchRoutingRuleSetSnapshot(ctx, item, false)
					if err != nil {
						return nil, err
					}
					now := time.Now().UTC()
					item.Content, item.Revision, item.ETag, item.LastModified = fetched.content, fetched.revision, fetched.etag, fetched.lastModified
					item.Status, item.LastError, item.LastAttemptAt, item.LastSuccessAt = model.RoutingRuleSetStatusReady, "", &now, &now
					contentChanged = item.Revision != current.Revision
				}
				if err := s.store.UpdateRoutingRuleSet(ctx, &item); err != nil {
					return nil, err
				}
				if contentChanged {
					serverIDs, err := s.store.ListServerIDsReferencingRoutingRuleSet(ctx, item.ID)
					if err != nil {
						return nil, err
					}
					if err := s.queueCoreConfigRefreshForServers(ctx, serverIDs, "routing_rule_set_updated"); err != nil {
						return nil, err
					}
				}
			case "routing_rule_sets.delete":
				if err := s.store.DeleteRoutingRuleSet(ctx, item.ID); err != nil {
					return nil, err
				}
				return map[string]any{"deleted": true, "routing_rule_set_id": item.ID}, nil
			case "routing_rule_sets.refresh":
				refreshed, didChange, err := s.refreshRoutingRuleSet(ctx, item.ID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"routing_rule_set": routingRuleSetView(*refreshed), "changed": didChange}, nil
			}
			return automationRoutingRuleSetResult(item, changed), nil
		})
	}
}

func (s *Server) routingRuleSetAutomationCandidate(ctx context.Context, input json.RawMessage, operation string) (model.RoutingRuleSet, []string, error) {
	var request routingRuleSetOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.RoutingRuleSet{}, nil, err
	}
	switch operation {
	case "routing_rule_sets.create":
		if len(request.RoutingRuleSet) == 0 {
			return model.RoutingRuleSet{}, nil, errors.New("routing_rule_set object is required")
		}
		if _, err := decodeClosedAutomationFields(request.RoutingRuleSet, routingRuleSetAutomationFields, "routing_rule_set"); err != nil {
			return model.RoutingRuleSet{}, nil, err
		}
		var item model.RoutingRuleSet
		if err := json.Unmarshal(request.RoutingRuleSet, &item); err != nil {
			return item, nil, err
		}
		if err := validateRoutingRuleSetInput(&item); err != nil {
			return item, nil, err
		}
		return item, nil, nil
	case "routing_rule_sets.update":
		if request.RoutingRuleSetID <= 0 || len(request.Changes) == 0 {
			return model.RoutingRuleSet{}, nil, errors.New("routing_rule_set_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, routingRuleSetAutomationFields, "changes")
		if err != nil {
			return model.RoutingRuleSet{}, nil, err
		}
		current, err := s.store.GetRoutingRuleSet(ctx, request.RoutingRuleSetID)
		if err != nil {
			return model.RoutingRuleSet{}, nil, err
		}
		var patch model.RoutingRuleSet
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.RoutingRuleSet{}, nil, err
		}
		merged := *current
		if fields["name"] != nil {
			merged.Name = patch.Name
		}
		if fields["url"] != nil {
			merged.URL = patch.URL
		}
		if fields["format"] != nil {
			merged.Format = patch.Format
		}
		if fields["mihomo_behavior"] != nil {
			merged.MihomoBehavior = patch.MihomoBehavior
		}
		if err := validateRoutingRuleSetInput(&merged); err != nil {
			return merged, nil, err
		}
		changed := make([]string, 0, len(fields))
		for field := range fields {
			changed = append(changed, field)
		}
		return merged, changed, nil
	case "routing_rule_sets.delete", "routing_rule_sets.refresh":
		if request.RoutingRuleSetID <= 0 || (operation == "routing_rule_sets.delete" && !request.Confirm) {
			return model.RoutingRuleSet{}, nil, errors.New("routing_rule_set_id is required and delete requires confirm=true")
		}
		item, err := s.store.GetRoutingRuleSet(ctx, request.RoutingRuleSetID)
		if err != nil {
			return model.RoutingRuleSet{}, nil, err
		}
		return *item, nil, nil
	default:
		return model.RoutingRuleSet{}, nil, errors.New("unsupported routing rule set operation")
	}
}

func automationRoutingRuleSetResult(item model.RoutingRuleSet, changed []string) map[string]any {
	result := map[string]any{"routing_rule_set": routingRuleSetView(item)}
	if len(changed) > 0 {
		result["changed_fields"] = changed
	}
	return result
}

func routingRuleSetView(item model.RoutingRuleSet) map[string]any {
	return map[string]any{"id": item.ID, "revision": item.Revision, "name": item.Name, "url": item.URL, "format": item.Format, "mihomo_behavior": item.MihomoBehavior, "status": item.Status, "last_error": item.LastError, "last_attempt_at": item.LastAttemptAt, "last_success_at": item.LastSuccessAt, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt}
}
