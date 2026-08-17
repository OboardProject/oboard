package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) queryNodeWorkspaceCapability(ctx context.Context, principal application.Principal, name string, input json.RawMessage) (any, error) {
	var request struct {
		UserID   int64                    `json:"user_id"`
		OutputID int64                    `json:"output_id"`
		Format   model.SubscriptionFormat `json:"format"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	user, err := s.nodeWorkspacePrincipalUser(ctx, principal, request.UserID)
	if err != nil {
		return nil, err
	}
	switch name {
	case "node_groups.list":
		_, groups, err := s.workspaceSubscriptionNodes(ctx, *user, nil)
		return groups, err
	case "node_sources.list":
		items, err := s.store.ListNodeSources(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i] = sanitizeNodeSource(items[i], s.nodeSourceDisplay(items[i]))
		}
		return items, nil
	case "subscription_outputs.list":
		return s.store.ListSubscriptionOutputs(ctx, user.ID)
	case "subscription_outputs.preview":
		output, err := s.store.GetSubscriptionOutput(ctx, user.ID, request.OutputID)
		if err != nil || !output.Enabled {
			return nil, errors.New("subscription output not found")
		}
		nodes, _, err := s.workspaceSubscriptionNodes(ctx, *user, output)
		if err != nil {
			return nil, err
		}
		preview, err := core.PreviewSubscriptionNodes(nodes, request.Format)
		if err != nil {
			return nil, err
		}
		return subscriptionPreviewView(preview, false), nil
	case "node_library.list":
		nodes, groups, err := s.workspaceAllNodes(ctx, *user)
		if err != nil {
			return nil, err
		}
		views := make([]nodeLibraryView, 0, len(nodes))
		for _, node := range nodes {
			_, shareErr := core.CanonicalShareURIForNode(node)
			views = append(views, nodeLibraryView{ID: node.Key, GroupID: groupIDForNode(node, groups), Name: node.Name, Protocol: model.PrivateSubscriptionProtocol(strings.ToLower(stringFromNodeType(node.Raw["type"]))), Source: nodeSourceKind(node), Copyable: shareErr == nil})
		}
		return views, nil
	default:
		return nil, errors.New("unsupported node workspace capability")
	}
}

func stringFromNodeType(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func (s *Server) nodeWorkspacePrincipalUser(ctx context.Context, principal application.Principal, userID int64) (*model.User, error) {
	if userID <= 0 || !principal.AllowsInt64("user_ids", userID) {
		return nil, errors.New("authorized user_id is required")
	}
	if principal.Role != model.RoleAdmin && (principal.UserID == nil || *principal.UserID != userID) {
		return nil, errors.New("only administrators may manage another user's nodes")
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil || user.Status != "active" {
		return nil, errors.New("active user not found")
	}
	return user, nil
}

func (s *Server) registerNodeWorkspaceAutomationOperations() {
	names := []string{"node_groups.create", "node_groups.update", "node_groups.delete", "node_sources.refresh", "subscription_outputs.save", "subscription_outputs.delete"}
	for _, capabilityName := range names {
		name := capabilityName
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.validateNodeWorkspaceOperation(ctx, principal, name, input)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyNodeWorkspaceOperation(ctx, principal, name, input)
		})
	}
}

type nodeWorkspaceOperation struct {
	UserID   int64               `json:"user_id"`
	GroupID  int64               `json:"group_id"`
	SourceID int64               `json:"source_id"`
	OutputID int64               `json:"output_id"`
	Name     string              `json:"name"`
	Kind     model.NodeGroupKind `json:"kind"`
	URL      string              `json:"url"`
	Content  string              `json:"content"`
	GroupIDs []int64             `json:"group_ids"`
	Enabled  *bool               `json:"enabled"`
}

func (s *Server) validateNodeWorkspaceOperation(ctx context.Context, principal application.Principal, name string, input json.RawMessage) (any, error) {
	var request nodeWorkspaceOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if _, err := s.nodeWorkspacePrincipalUser(ctx, principal, request.UserID); err != nil {
		return nil, err
	}
	switch name {
	case "node_groups.create":
		if strings.TrimSpace(request.Name) == "" || (request.Kind != model.NodeGroupManual && request.Kind != model.NodeGroupRemote) {
			return nil, errors.New("valid name and kind are required")
		}
		if request.Kind == model.NodeGroupRemote {
			if _, err := validateNodeSourceURL(request.URL); err != nil {
				return nil, err
			}
		} else if _, err := core.ParsePrivateSubscription(request.Content); err != nil {
			return nil, err
		}
	case "node_groups.update", "node_groups.delete":
		if _, err := s.store.GetNodeGroup(ctx, request.UserID, request.GroupID); err != nil {
			return nil, err
		}
	case "node_sources.refresh":
		if _, err := s.store.GetNodeSource(ctx, request.UserID, request.SourceID); err != nil {
			return nil, err
		}
	case "subscription_outputs.save":
		if strings.TrimSpace(request.Name) == "" || len(request.GroupIDs) == 0 {
			return nil, errors.New("name and group_ids are required")
		}
		if request.OutputID > 0 {
			if _, err := s.store.GetSubscriptionOutput(ctx, request.UserID, request.OutputID); err != nil {
				return nil, err
			}
		}
	case "subscription_outputs.delete":
		output, err := s.store.GetSubscriptionOutput(ctx, request.UserID, request.OutputID)
		if err != nil {
			return nil, err
		}
		if output.IsDefault {
			return nil, errors.New("default subscription output cannot be deleted")
		}
	}
	return map[string]any{"user_id": request.UserID, "operation": name, "validated": true}, nil
}

func (s *Server) applyNodeWorkspaceOperation(ctx context.Context, principal application.Principal, name string, input json.RawMessage) (any, error) {
	var request nodeWorkspaceOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if _, err := s.nodeWorkspacePrincipalUser(ctx, principal, request.UserID); err != nil {
		return nil, err
	}
	switch name {
	case "node_groups.create":
		group := &model.NodeGroup{UserID: request.UserID, Name: request.Name, Kind: request.Kind}
		if err := s.store.CreateNodeGroup(ctx, group); err != nil {
			return nil, err
		}
		if request.Kind == model.NodeGroupRemote {
			source, err := s.createNodeSource(ctx, request.UserID, group.ID, request.URL)
			if err != nil {
				_ = s.store.DeleteNodeGroup(ctx, request.UserID, group.ID)
				return nil, err
			}
			result, err := s.refreshNodeSource(ctx, *source)
			if err != nil {
				return map[string]any{"node_group": group, "node_source": sanitizeNodeSource(*source, s.nodeSourceDisplay(*source)), "refresh_error": err.Error()}, nil
			}
			return map[string]any{"node_group": group, "node_source": sanitizeNodeSource(*source, s.nodeSourceDisplay(*source)), "node_count": len(result.Nodes), "issues": result.Issues}, nil
		}
		result, err := s.importManualNodes(ctx, request.UserID, group.ID, request.Content)
		if err != nil {
			_ = s.store.DeleteNodeGroup(ctx, request.UserID, group.ID)
			return nil, err
		}
		return map[string]any{"node_group": group, "node_count": len(result.Nodes), "issues": result.Issues}, nil
	case "node_groups.update":
		return s.store.RenameNodeGroup(ctx, request.UserID, request.GroupID, request.Name)
	case "node_groups.delete":
		return map[string]any{"deleted": true}, s.store.DeleteNodeGroup(ctx, request.UserID, request.GroupID)
	case "node_sources.refresh":
		source, err := s.store.GetNodeSource(ctx, request.UserID, request.SourceID)
		if err != nil {
			return nil, err
		}
		result, err := s.refreshNodeSource(ctx, *source)
		if err != nil {
			return nil, err
		}
		return map[string]any{"source_id": source.ID, "node_count": len(result.Nodes), "issues": result.Issues}, nil
	case "subscription_outputs.save":
		output := &model.SubscriptionOutput{ID: request.OutputID, UserID: request.UserID, Name: request.Name, GroupIDs: request.GroupIDs, Enabled: true}
		if request.OutputID > 0 {
			current, err := s.store.GetSubscriptionOutput(ctx, request.UserID, request.OutputID)
			if err != nil {
				return nil, err
			}
			output.IsDefault = current.IsDefault
			output.Enabled = current.Enabled
		}
		if request.Enabled != nil {
			output.Enabled = *request.Enabled
		}
		if err := s.store.SaveSubscriptionOutput(ctx, output); err != nil {
			return nil, err
		}
		return output, nil
	case "subscription_outputs.delete":
		return map[string]any{"deleted": true}, s.store.DeleteSubscriptionOutput(ctx, request.UserID, request.OutputID)
	default:
		return nil, errors.New("unsupported node workspace operation")
	}
}
