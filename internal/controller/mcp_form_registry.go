package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// FormMode distinguishes create (defaults) vs update (PATCH).
type FormMode string

const (
	FormModeCreate FormMode = "create"
	FormModeUpdate FormMode = "update"
)

// FormDefinition is the single registry entry for one management form.
type FormDefinition struct {
	ID         string
	Mode       FormMode
	Capability string
	Validate   func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]any, []map[string]any, []string, []formValidationError, map[string]string, error)
}

type formValidationError struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type formValidationResult struct {
	Valid             bool                   `json:"valid"`
	FormID            string                 `json:"form_id"`
	Mode              string                 `json:"mode"`
	NormalizedValues  map[string]any         `json:"normalized_values"`
	AppliedDefaults   []map[string]any       `json:"applied_defaults"`
	Warnings          []string               `json:"warnings"`
	Errors            []formValidationError  `json:"errors"`
	ValidationContext map[string]string      `json:"validation_context"`
	ValidationDigest  string                 `json:"validation_digest"`
}

// formRegistry is the single source for all node-mutation forms.
func (s *Server) formRegistry() map[string]FormDefinition {
	return map[string]FormDefinition{
		"server-create": {
			ID: "server-create", Mode: FormModeCreate, Capability: "servers.onboard",
			Validate: s.validateServerCreateForm,
		},
		"inbound-create": {
			ID: "inbound-create", Mode: FormModeCreate, Capability: "inbounds.create",
			Validate: s.validateInboundCreateForm,
		},
		"inbound-update": {
			ID: "inbound-update", Mode: FormModeUpdate, Capability: "inbounds.update",
			Validate: s.validateInboundUpdateForm,
		},
		"external-outbound-create": {
			ID: "external-outbound-create", Mode: FormModeCreate, Capability: "external_outbounds.create",
			Validate: s.validateExternalOutboundCreateForm,
		},
		"external-outbound-update": {
			ID: "external-outbound-update", Mode: FormModeUpdate, Capability: "external_outbounds.update",
			Validate: s.validateExternalOutboundUpdateForm,
		},
	}
}

func (s *Server) validateServerCreateForm(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]any, []map[string]any, []string, []formValidationError, map[string]string, error) {
	normalized, applied, warnings, err := s.materializeServerOnboardForm(ctx, input)
	if err != nil {
		return nil, nil, nil, []formValidationError{{Code: "validation_failed", Path: "", Message: err.Error()}}, nil, nil
	}
	return normalized, applied, warnings, nil, map[string]string{}, nil
}

func (s *Server) validateInboundCreateForm(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]any, []map[string]any, []string, []formValidationError, map[string]string, error) {
	var raw map[string]any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, nil, nil, []formValidationError{{Code: "invalid_json", Path: "", Message: err.Error()}}, nil, nil
	}
	// input is expected to be the inbound object directly or {"inbound": {...}}
	var inboundRaw json.RawMessage
	if v, ok := raw["inbound"]; ok {
		b, _ := json.Marshal(v)
		inboundRaw = b
	} else {
		b, _ := json.Marshal(raw)
		inboundRaw = b
	}
	inbound, err := s.decodeInboundCreateOperation(inboundRawWrapper(inboundRaw))
	if err != nil {
		return nil, nil, nil, mapInboundError(err), nil, nil
	}
	if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
		return nil, nil, nil, mapInboundError(err), nil, nil
	}
	normalized, _ := json.Marshal(map[string]any{"inbound": inbound})
	var out map[string]any
	_ = json.Unmarshal(normalized, &out)
	// Compute applied defaults by diffing input vs normalized
	applied := []map[string]any{}
	if _, ok := raw["inbound"]; ok {
		// Check for PSK generation
		if pskMissingInInput(raw, inbound.ConfigJSON) {
			applied = append(applied, map[string]any{"field": "inbound.config_json.psk", "value": "***", "reason": "panel_create_default"})
		}
	}
	// Determine server revision for context
	revisions := map[string]string{}
	if server, err := s.store.GetServer(ctx, inbound.ServerID); err == nil {
		revisions["server:"+fmt.Sprint(server.ID)] = server.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	if topo, err := s.store.RoutingTopologyRevision(ctx); err == nil {
		revisions["routing_topology"] = topo
	}
	// Snell compatibility warning
	warnings := []string{}
	if inbound.Protocol == model.ProtocolSnell {
		caps := core.ResolveTargetCapabilities(model.SubscriptionFormatMihomo, "")
		for _, feat := range core.RequiredFeaturesForInbound(inbound) {
			if !core.IsFeatureSupported(caps, feat) {
				warnings = append(warnings, fmt.Sprintf("subscription_client_incompatible: mihomo does not support %s", feat))
				break
			}
		}
	}
	return out, applied, warnings, nil, revisions, nil
}

func (s *Server) validateInboundUpdateForm(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]any, []map[string]any, []string, []formValidationError, map[string]string, error) {
	var raw map[string]any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, nil, nil, []formValidationError{{Code: "invalid_json", Path: "", Message: err.Error()}}, nil, nil
	}
	// Expect {"inbound_id": ..., "changes": {...}} or direct
	var request inboundUpdateOperation
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, nil, nil, []formValidationError{{Code: "invalid_json", Path: "", Message: err.Error()}}, nil, nil
	}
	if request.InboundID == 0 {
		// Try alternative key "inbound_id" already handled, but if raw has "inbound_id" at top level
		if id, ok := raw["inbound_id"]; ok {
			if f, ok := id.(float64); ok {
				request.InboundID = int64(f)
			}
		}
	}
	// If input is just changes without wrapper, treat as error
	if request.InboundID == 0 || len(request.Changes) == 0 {
		// Try to handle input as {"inbound_id":1, "changes":{...}}
		if len(raw) > 0 {
			// Re-encode to use existing decode path
		}
		return nil, nil, nil, []formValidationError{{Code: "missing_field", Path: "inbound_id", Message: "inbound_id and changes are required"}}, nil, nil
	}
	current, inbound, err := s.decodeInboundUpdateOperation(ctx, principal, input)
	if err != nil {
		return nil, nil, nil, mapInboundError(err), nil, nil
	}
	if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
		return nil, nil, nil, mapInboundError(err), nil, nil
	}
	// PATCH semantics: ensure PSK not overwritten if not supplied
	// decodeInboundUpdateOperation already preserves current PSK via mergeInboundPatch
	normalized, _ := json.Marshal(map[string]any{"inbound_id": request.InboundID, "changes": inbound})
	var out map[string]any
	_ = json.Unmarshal(normalized, &out)
	revisions := map[string]string{}
	if current != nil {
		revisions["inbound:"+fmt.Sprint(current.ID)] = current.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	if server, err := s.store.GetServer(ctx, inbound.ServerID); err == nil {
		revisions["server:"+fmt.Sprint(server.ID)] = server.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	warnings := []string{}
	if inbound.Protocol == model.ProtocolSnell {
		caps := core.ResolveTargetCapabilities(model.SubscriptionFormatMihomo, "")
		for _, feat := range core.RequiredFeaturesForInbound(inbound) {
			if !core.IsFeatureSupported(caps, feat) {
				warnings = append(warnings, fmt.Sprintf("subscription_client_incompatible: mihomo does not support %s", feat))
				break
			}
		}
	}
	return out, nil, warnings, nil, revisions, nil
}

func (s *Server) validateExternalOutboundCreateForm(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]any, []map[string]any, []string, []formValidationError, map[string]string, error) {
	var raw map[string]any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, nil, nil, []formValidationError{{Code: "invalid_json", Path: "", Message: err.Error()}}, nil, nil
	}
	var outboundRaw json.RawMessage
	if v, ok := raw["external_outbound"]; ok {
		b, _ := json.Marshal(v)
		outboundRaw = b
	} else {
		b, _ := json.Marshal(raw)
		outboundRaw = b
	}
	var v model.ExternalOutbound
	if err := json.Unmarshal(outboundRaw, &v); err != nil {
		return nil, nil, nil, []formValidationError{{Code: "invalid_json", Path: "", Message: err.Error()}}, nil, nil
	}
	if err := s.validateExternalOutbound(ctx, &v); err != nil {
		return nil, nil, nil, mapExternalOutboundError(err), nil, nil
	}
	normalized, _ := json.Marshal(map[string]any{"external_outbound": v})
	var out map[string]any
	_ = json.Unmarshal(normalized, &out)
	return out, nil, nil, nil, map[string]string{}, nil
}

func (s *Server) validateExternalOutboundUpdateForm(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]any, []map[string]any, []string, []formValidationError, map[string]string, error) {
	var request struct {
		ExternalOutboundID int64           `json:"external_outbound_id"`
		Changes            json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, nil, nil, []formValidationError{{Code: "invalid_json", Path: "", Message: err.Error()}}, nil, nil
	}
	if request.ExternalOutboundID <= 0 || len(request.Changes) == 0 {
		return nil, nil, nil, []formValidationError{{Code: "missing_field", Path: "external_outbound_id", Message: "external_outbound_id and changes are required"}}, nil, nil
	}
	current, err := s.store.GetExternalOutbound(ctx, request.ExternalOutboundID)
	if err != nil {
		return nil, nil, nil, []formValidationError{{Code: "not_found", Path: "external_outbound_id", Message: err.Error()}}, nil, nil
	}
	var patch model.ExternalOutbound
	if err := json.Unmarshal(request.Changes, &patch); err != nil {
		return nil, nil, nil, []formValidationError{{Code: "invalid_json", Path: "changes", Message: err.Error()}}, nil, nil
	}
	merged := *current
	// Simple merge: only allow fields in external outbound
	patchFields := jsonObjectKeys(request.Changes)
	if _, ok := patchFields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := patchFields["target_address"]; ok {
		merged.TargetAddress = patch.TargetAddress
	}
	if _, ok := patchFields["target_port"]; ok {
		merged.TargetPort = patch.TargetPort
	}
	if _, ok := patchFields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := patchFields["protocol"]; ok {
		merged.Protocol = patch.Protocol
	}
	// Validate merged
	if err := s.validateExternalOutbound(ctx, &merged); err != nil {
		return nil, nil, nil, mapExternalOutboundError(err), nil, nil
	}
	normalized, _ := json.Marshal(map[string]any{"external_outbound_id": request.ExternalOutboundID, "changes": patch})
	var out map[string]any
	_ = json.Unmarshal(normalized, &out)
	revisions := map[string]string{"external_outbound:" + fmt.Sprint(current.ID): current.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
	return out, nil, nil, nil, revisions, nil
}

func inboundRawWrapper(raw json.RawMessage) json.RawMessage {
	// Wrap raw inbound for decodeInboundCreateOperation which expects {"inbound": ...}
	wrapped, _ := json.Marshal(map[string]json.RawMessage{"inbound": raw})
	return wrapped
}

func pskMissingInInput(input map[string]any, normalizedConfig string) bool {
	// Check if input had psk
	var inbound map[string]any
	if v, ok := input["inbound"]; ok {
		if m, ok := v.(map[string]any); ok {
			inbound = m
		}
	} else {
		inbound = input
	}
	if configRaw, ok := inbound["config_json"]; ok {
		if s, ok := configRaw.(string); ok {
			var cfg map[string]any
			if json.Unmarshal([]byte(s), &cfg) == nil {
				if psk, ok := cfg["psk"]; ok && strings.TrimSpace(fmt.Sprint(psk)) != "" {
					return false
				}
				return true
			}
		}
		if m, ok := configRaw.(map[string]any); ok {
			if psk, ok := m["psk"]; ok && strings.TrimSpace(fmt.Sprint(psk)) != "" {
				return false
			}
			return true
		}
	}
	// If no config_json at all, then psk was missing
	return true
}

func mapInboundError(err error) []formValidationError {
	if err == nil {
		return nil
	}
	msg := err.Error()
	code := "validation_failed"
	path := ""
	// Map known Snell errors to codes
	if strings.Contains(msg, "unsupported snell version") {
		code = "invalid_snell_version"
		path = "config_json.version"
	} else if strings.Contains(msg, "managed_field") {
		code = "managed_field"
		path = "config_json.userkey"
	} else if strings.Contains(msg, "snell v6 does not support") {
		code = "invalid_snell_v6_obfs"
		path = "config_json.obfs_mode"
	} else if strings.Contains(msg, "snell v6 psk") {
		code = "invalid_snell_psk"
		path = "config_json.psk"
	} else if strings.Contains(msg, "snell psk") {
		code = "invalid_snell_psk"
		path = "config_json.psk"
	} else if strings.Contains(msg, "psk required") {
		code = "missing_psk"
		path = "config_json.psk"
	} else if strings.Contains(msg, "obfs_mode") {
		code = "invalid_obfs"
		path = "config_json.obfs_mode"
	}
	return []formValidationError{{Code: code, Path: path, Message: msg}}
}

func mapExternalOutboundError(err error) []formValidationError {
	if err == nil {
		return nil
	}
	return []formValidationError{{Code: "validation_failed", Path: "", Message: err.Error()}}
}

func formValidationDigest(formID string, normalized map[string]any, revisions map[string]string) string {
	payload := struct {
		FormID     string            `json:"form_id"`
		Normalized map[string]any    `json:"normalized"`
		Revisions  map[string]string `json:"revisions"`
	}{formID, normalized, revisions}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// EnforceFormValidationForFastPath ensures every node-mutation Fast Path
// operation is validated via the shared domain validators, even if the LLM
// never called oboard_validate_form. It is called inside prepareMCPTask
// before creating the prepared plan.
func (s *Server) enforceFormValidationForFastPath(ctx context.Context, principal application.Principal, operations []mcpOperationRef) error {
	for _, op := range operations {
		var formID string
		switch op.Capability {
		case "inbounds.create":
			formID = "inbound-create"
		case "inbounds.update":
			formID = "inbound-update"
		case "external_outbounds.create":
			formID = "external-outbound-create"
		case "external_outbounds.update":
			formID = "external-outbound-update"
		case "servers.onboard":
			formID = "server-create"
		default:
			continue
		}
		def, ok := s.formRegistry()[formID]
		if !ok {
			continue
		}
		input, _ := json.Marshal(op.Input)
		_, _, _, errs, _, err := def.Validate(ctx, principal, input)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errors.New(errs[0].Message)
		}
	}
	return nil
}
