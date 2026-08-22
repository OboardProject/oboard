package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// ---- Snell profiles (shared parameter sets) ----

var snellProfileAutomationFields = map[string]bool{
	"name": true, "version": true, "psk": true, "obfs_mode": true,
	"obfs_host": true, "mode": true, "reuse": true, "remark": true, "enabled": true,
}

func (s *Server) registerSnellProfileOperations() {
	for _, name := range []string{"snell_profiles.create", "snell_profiles.update", "snell_profiles.delete"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.snellProfileAutomationValidate(ctx, principal, input, name)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.snellProfileAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applySnellProfileOperation(ctx, principal, input, name)
		})
	}
}

type snellProfileOperationInput struct {
	SnellProfile   json.RawMessage `json:"snell_profile,omitempty"`
	SnellProfileID int64           `json:"snell_profile_id,omitempty"`
	Changes        json.RawMessage `json:"changes,omitempty"`
	Confirm        bool            `json:"confirm,omitempty"`
}

func (s *Server) snellProfileAutomationValidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	var request snellProfileOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	switch name {
	case "snell_profiles.create":
		if len(request.SnellProfile) == 0 {
			return nil, errors.New("snell_profile object is required")
		}
		fields, err := decodeClosedAutomationFields(request.SnellProfile, snellProfileAutomationFields, "snell_profile")
		if err != nil {
			return nil, err
		}
		if _, ok := fields["name"]; !ok {
			return nil, errors.New("snell_profile.name is required")
		}
		var profile model.SnellProfile
		if err := json.Unmarshal(request.SnellProfile, &profile); err != nil {
			return nil, err
		}
		profile.ID, profile.Builtin, profile.UsageCount, profile.CreatedAt, profile.UpdatedAt = 0, false, 0, time.Time{}, time.Time{}
		if err := validateSnellProfile(profile); err != nil {
			return nil, err
		}
		return map[string]any{"snell_profile": automationSnellProfileView(profile)}, nil
	case "snell_profiles.update":
		if request.SnellProfileID <= 0 || len(request.Changes) == 0 {
			return nil, errors.New("snell_profile_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, snellProfileAutomationFields, "changes")
		if err != nil {
			return nil, err
		}
		if len(fields) == 0 {
			return nil, errors.New("changes must contain at least one Snell profile field")
		}
		current, err := s.store.GetSnellProfile(ctx, request.SnellProfileID)
		if err != nil {
			return nil, err
		}
		var patch model.SnellProfile
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return nil, err
		}
		merged := mergeSnellProfilePatch(*current, patch, fields)
		if err := validateSnellProfile(merged); err != nil {
			return nil, err
		}
		return map[string]any{"snell_profile": automationSnellProfileView(merged), "changed_fields": automationChangedFields(fields)}, nil
	case "snell_profiles.delete":
		if request.SnellProfileID <= 0 || !request.Confirm {
			return nil, errors.New("snell_profile_id and confirm=true are required")
		}
		profile, err := s.store.GetSnellProfile(ctx, request.SnellProfileID)
		if err != nil {
			return nil, err
		}
		if profile.Builtin {
			return nil, errors.New("内置 Snell 预设不可删除")
		}
		return map[string]any{"snell_profile_id": request.SnellProfileID}, nil
	default:
		return nil, errors.New("unsupported Snell profile operation")
	}
}

func mergeSnellProfilePatch(current model.SnellProfile, patch model.SnellProfile, fields map[string]json.RawMessage) model.SnellProfile {
	merged := current
	merged.UsageCount = 0
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["version"]; ok {
		merged.Version = patch.Version
	}
	if _, ok := fields["psk"]; ok {
		merged.PSK = patch.PSK
	}
	if _, ok := fields["obfs_mode"]; ok {
		merged.ObfsMode = patch.ObfsMode
	}
	if _, ok := fields["obfs_host"]; ok {
		merged.ObfsHost = patch.ObfsHost
	}
	if _, ok := fields["mode"]; ok {
		merged.Mode = patch.Mode
	}
	if _, ok := fields["reuse"]; ok {
		merged.Reuse = patch.Reuse
	}
	if _, ok := fields["remark"]; ok {
		merged.Remark = patch.Remark
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	if merged.Builtin {
		merged.Enabled = true
	}
	return merged
}

func (s *Server) snellProfileAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	var request snellProfileOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if name == "snell_profiles.create" {
		return map[string]string{}, nil
	}
	if request.SnellProfileID <= 0 {
		return nil, errors.New("snell_profile_id is required")
	}
	current, err := s.store.GetSnellProfile(ctx, request.SnellProfileID)
	if err != nil {
		return nil, err
	}
	return map[string]string{"snell_profile:" + strconv.FormatInt(current.ID, 10): current.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) applySnellProfileOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	if _, err := s.snellProfileAutomationValidate(ctx, principal, input, name); err != nil {
		return nil, err
	}
	var request snellProfileOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	switch name {
	case "snell_profiles.create":
		var profile model.SnellProfile
		if err := json.Unmarshal(request.SnellProfile, &profile); err != nil {
			return nil, err
		}
		profile.ID, profile.Builtin, profile.UsageCount, profile.CreatedAt, profile.UpdatedAt = 0, false, 0, time.Time{}, time.Time{}
		if err := validateSnellProfile(profile); err != nil {
			return nil, err
		}
		if err := s.store.CreateSnellProfile(ctx, &profile); err != nil {
			return nil, err
		}
		return map[string]any{"snell_profile": automationSnellProfileView(profile)}, nil
	case "snell_profiles.update":
		current, err := s.store.GetSnellProfile(ctx, request.SnellProfileID)
		if err != nil {
			return nil, err
		}
		fields, err := decodeClosedAutomationFields(request.Changes, snellProfileAutomationFields, "changes")
		if err != nil {
			return nil, err
		}
		var patch model.SnellProfile
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return nil, err
		}
		merged := mergeSnellProfilePatch(*current, patch, fields)
		merged.ID = current.ID
		merged.CreatedAt = current.CreatedAt
		if err := validateSnellProfile(merged); err != nil {
			return nil, err
		}
		if _, err := s.store.UpdateSnellProfile(ctx, &merged); err != nil {
			return nil, err
		}
		return map[string]any{"snell_profile": automationSnellProfileView(merged), "changed_fields": automationChangedFields(fields)}, nil
	case "snell_profiles.delete":
		if err := s.store.DeleteSnellProfile(ctx, request.SnellProfileID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "snell_profile_id": request.SnellProfileID}, nil
	default:
		return nil, fmt.Errorf("unsupported Snell profile operation %q", name)
	}
}

func automationSnellProfileView(profile model.SnellProfile) map[string]any {
	return map[string]any{
		"id": profile.ID, "name": profile.Name, "version": profile.Version,
		"psk": profile.PSK, "obfs_mode": profile.ObfsMode, "obfs_host": profile.ObfsHost,
		"mode": profile.Mode, "reuse": profile.Reuse, "remark": profile.Remark,
		"builtin": profile.Builtin, "enabled": profile.Enabled, "usage_count": profile.UsageCount,
		"created_at": profile.CreatedAt, "updated_at": profile.UpdatedAt,
	}
}

var _ = core.SnellVersionV4