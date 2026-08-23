package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

var nodePresetAutomationFields = map[string]bool{
	"name": true, "protocol": true, "kind": true, "config_json": true,
	"default_port": true, "remark": true, "enabled": true,
}

func (s *Server) registerNodePresetOperations() {
	for _, name := range []string{"node_presets.create", "node_presets.update", "node_presets.delete"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.nodePresetAutomationValidate(ctx, principal, input, name)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.nodePresetAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyNodePresetOperation(ctx, principal, input, name)
		})
	}
}

type nodePresetOperationInput struct {
	NodePreset   json.RawMessage `json:"node_preset,omitempty"`
	NodePresetID int64           `json:"node_preset_id,omitempty"`
	Changes      json.RawMessage `json:"changes,omitempty"`
	Confirm      bool            `json:"confirm,omitempty"`
}

func (s *Server) nodePresetAutomationValidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	var request nodePresetOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	switch name {
	case "node_presets.create":
		if len(request.NodePreset) == 0 {
			return nil, errors.New("node_preset object is required")
		}
		fields, err := decodeClosedAutomationFields(request.NodePreset, nodePresetAutomationFields, "node_preset")
		if err != nil {
			return nil, err
		}
		if _, ok := fields["name"]; !ok {
			return nil, errors.New("node_preset.name is required")
		}
		if _, ok := fields["kind"]; !ok {
			return nil, errors.New("node_preset.kind is required")
		}
		preset, err := decodeNodePresetPayload(request.NodePreset)
		if err != nil {
			return nil, err
		}
		preset.ID, preset.Builtin, preset.UsageCount, preset.CreatedAt, preset.UpdatedAt = 0, false, 0, time.Time{}, time.Time{}
		if _, ok := fields["enabled"]; !ok {
			preset.Enabled = true
		}
		if err := store.NormalizeNodePreset(&preset); err != nil {
			return nil, err
		}
		return map[string]any{"node_preset": automationNodePresetView(preset)}, nil
	case "node_presets.update":
		if request.NodePresetID <= 0 || len(request.Changes) == 0 {
			return nil, errors.New("node_preset_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, nodePresetAutomationFields, "changes")
		if err != nil {
			return nil, err
		}
		if len(fields) == 0 {
			return nil, errors.New("changes must contain at least one node preset field")
		}
		current, err := s.store.GetNodePreset(ctx, request.NodePresetID)
		if err != nil {
			return nil, err
		}
		patch, err := decodeNodePresetPayload(request.Changes)
		if err != nil {
			return nil, err
		}
		merged := mergeNodePresetPatch(*current, patch, fields)
		if err := store.NormalizeNodePreset(&merged); err != nil {
			return nil, err
		}
		return map[string]any{"node_preset": automationNodePresetView(merged), "changed_fields": automationChangedFields(fields)}, nil
	case "node_presets.delete":
		if request.NodePresetID <= 0 || !request.Confirm {
			return nil, errors.New("node_preset_id and confirm=true are required")
		}
		preset, err := s.store.GetNodePreset(ctx, request.NodePresetID)
		if err != nil {
			return nil, err
		}
		if preset.Builtin {
			return nil, errors.New("内置节点预设不可删除")
		}
		return map[string]any{"node_preset_id": request.NodePresetID}, nil
	default:
		return nil, errors.New("unsupported node preset operation")
	}
}

func mergeNodePresetPatch(current model.NodePreset, patch model.NodePreset, fields map[string]json.RawMessage) model.NodePreset {
	merged := current
	merged.UsageCount = 0
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["protocol"]; ok {
		merged.Protocol = patch.Protocol
	}
	if _, ok := fields["kind"]; ok {
		merged.Kind = patch.Kind
	}
	if _, ok := fields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := fields["default_port"]; ok {
		merged.DefaultPort = patch.DefaultPort
	}
	if _, ok := fields["remark"]; ok {
		merged.Remark = patch.Remark
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	if merged.Builtin {
		merged.Enabled = true
		merged.Kind = current.Kind
		merged.Protocol = current.Protocol
	}
	return merged
}

func (s *Server) nodePresetAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	var request nodePresetOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if name == "node_presets.create" {
		return map[string]string{}, nil
	}
	if request.NodePresetID <= 0 {
		return nil, errors.New("node_preset_id is required")
	}
	current, err := s.store.GetNodePreset(ctx, request.NodePresetID)
	if err != nil {
		return nil, err
	}
	return map[string]string{"node_preset:" + strconv.FormatInt(current.ID, 10): current.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) applyNodePresetOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	if _, err := s.nodePresetAutomationValidate(ctx, principal, input, name); err != nil {
		return nil, err
	}
	var request nodePresetOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	switch name {
	case "node_presets.create":
		preset, err := decodeNodePresetPayload(request.NodePreset)
		if err != nil {
			return nil, err
		}
		preset.ID, preset.Builtin, preset.UsageCount, preset.CreatedAt, preset.UpdatedAt = 0, false, 0, time.Time{}, time.Time{}
		fields, err := decodeClosedAutomationFields(request.NodePreset, nodePresetAutomationFields, "node_preset")
		if err != nil {
			return nil, err
		}
		if _, ok := fields["enabled"]; !ok {
			preset.Enabled = true
		}
		if err := store.NormalizeNodePreset(&preset); err != nil {
			return nil, err
		}
		if err := s.store.CreateNodePreset(ctx, &preset); err != nil {
			return nil, err
		}
		return map[string]any{"node_preset": automationNodePresetView(preset)}, nil
	case "node_presets.update":
		current, err := s.store.GetNodePreset(ctx, request.NodePresetID)
		if err != nil {
			return nil, err
		}
		fields, err := decodeClosedAutomationFields(request.Changes, nodePresetAutomationFields, "changes")
		if err != nil {
			return nil, err
		}
		patch, err := decodeNodePresetPayload(request.Changes)
		if err != nil {
			return nil, err
		}
		merged := mergeNodePresetPatch(*current, patch, fields)
		merged.ID = current.ID
		merged.CreatedAt = current.CreatedAt
		if err := store.NormalizeNodePreset(&merged); err != nil {
			return nil, err
		}
		if err := s.store.UpdateNodePreset(ctx, &merged); err != nil {
			return nil, err
		}
		return map[string]any{"node_preset": automationNodePresetView(merged), "changed_fields": automationChangedFields(fields)}, nil
	case "node_presets.delete":
		if err := s.store.DeleteNodePreset(ctx, request.NodePresetID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "node_preset_id": request.NodePresetID}, nil
	default:
		return nil, fmt.Errorf("unsupported node preset operation %q", name)
	}
}

func decodeNodePresetPayload(raw json.RawMessage) (model.NodePreset, error) {
	var payload struct {
		Name        string          `json:"name"`
		Protocol    string          `json:"protocol"`
		Kind        string          `json:"kind"`
		ConfigJSON  json.RawMessage `json:"config_json"`
		DefaultPort int             `json:"default_port"`
		Remark      string          `json:"remark"`
		Enabled     bool            `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return model.NodePreset{}, err
	}
	config := "{}"
	if len(payload.ConfigJSON) > 0 && string(payload.ConfigJSON) != "null" {
		if payload.ConfigJSON[0] == '"' {
			if err := json.Unmarshal(payload.ConfigJSON, &config); err != nil {
				return model.NodePreset{}, errors.New("config_json must be a JSON object")
			}
		} else {
			config = string(payload.ConfigJSON)
		}
	}
	return model.NodePreset{
		Name: payload.Name, Protocol: payload.Protocol, Kind: payload.Kind,
		ConfigJSON: config, DefaultPort: payload.DefaultPort, Remark: payload.Remark, Enabled: payload.Enabled,
	}, nil
}

func automationNodePresetView(preset model.NodePreset) map[string]any {
	var config any = map[string]any{}
	if err := json.Unmarshal([]byte(preset.ConfigJSON), &config); err != nil {
		config = map[string]any{}
	}
	return map[string]any{
		"id": preset.ID, "name": preset.Name, "protocol": preset.Protocol, "kind": preset.Kind,
		"config_json": config, "default_port": preset.DefaultPort, "remark": preset.Remark,
		"builtin": preset.Builtin, "enabled": preset.Enabled, "usage_count": preset.UsageCount,
		"created_at": preset.CreatedAt, "updated_at": preset.UpdatedAt,
	}
}
