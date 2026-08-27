package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

type inboundCreateOperation struct {
	Inbound json.RawMessage `json:"inbound"`
}

type inboundUpdateOperation struct {
	InboundID int64           `json:"inbound_id"`
	Changes   json.RawMessage `json:"changes"`
}

type inboundPaddingUpdateOperation struct {
	InboundID     int64    `json:"inbound_id"`
	Operation     string   `json:"operation"`
	PresetID      string   `json:"preset_id,omitempty"`
	AutoTune      *bool    `json:"auto_tune,omitempty"`
	PaddingScheme []string `json:"padding_scheme,omitempty"`
}

var inboundAutomationFields = map[string]bool{
	"server_id": true, "name": true, "protocol": true, "listen_ip": true, "port": true, "advertise_port": true,
	"entry_ip_mode": true, "external_ip": true, "dns_sync_enabled": true, "dns_credential_id": true,
	"dns_domain": true, "dns_proxy_enabled": true, "dns_record_types": true, "ddns_enabled": true,
	"ddns_interval_seconds": true, "tls": true, "certificate_mode": true, "certificate_id": true,
	"certificate_domain": true, "config_json": true, "kind": true, "reality": true,
	"rotate_reality_key": true, "enabled": true,
	"anytls_padding": true,
}

func (s *Server) registerInboundAutomationOperations() {
	s.automation.RegisterValidator("inbounds.padding.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, inbound, err := s.decodeInboundPaddingUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		updated, err := core.ApplyAnyTLSPaddingOperation(inbound.ConfigJSON, request.coreOperation(), nil, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		metadata, _, _ := core.AnyTLSPaddingMetadataFromJSON(updated)
		return map[string]any{"inbound_id": inbound.ID, "anytls_padding": metadata, "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("inbounds.padding.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		_, inbound, err := s.decodeInboundPaddingUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return s.inboundAutomationRevisions(ctx, principal, inbound.ID, inbound.ServerID)
	})
	s.automation.Register("inbounds.padding.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, _, err := s.decodeInboundPaddingUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		inbound, err := s.application.UpdateAnyTLSPadding(ctx, principal, request.InboundID, request.coreOperation())
		if err != nil {
			return nil, err
		}
		return map[string]any{"inbound": automationInboundView(*inbound), "requires_deployment": true}, nil
	})

	s.automation.RegisterValidator("inbounds.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		inbound, pathCount, err := s.inboundDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"inbound_id": inbound.ID, "deleted_proxy_path_count": pathCount, "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("inbounds.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		if _, _, err := s.inboundDeleteAutomationCandidate(ctx, principal, input); err != nil {
			return nil, err
		}
		return s.routingTopologyAutomationRevision(ctx)
	})
	s.automation.Register("inbounds.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		inbound, pathCount, err := s.inboundDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if _, err := s.store.RemoveAssignableNodeFromPlans(ctx, model.AssignableNodeInbound, inbound.ID); err != nil {
			return nil, err
		}
		if err := s.deleteDNSInboundRecords(ctx, inbound); err != nil {
			return nil, err
		}
		if err := s.store.DeleteProxyPathsForInbound(ctx, inbound.ID); err != nil {
			return nil, err
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			return nil, err
		}
		if err := s.store.DeleteInboundProbeResults(ctx, inbound.ID); err != nil {
			return nil, err
		}
		if err := s.store.Delete(ctx, "inbounds", inbound.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "inbound_id": inbound.ID, "deleted_proxy_path_count": pathCount, "requires_deployment": true}, nil
	})

	s.automation.RegisterValidator("inbounds.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		inbound, err := s.decodeInboundCreateOperation(input)
		if err != nil {
			return nil, err
		}
		if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
			return nil, err
		}
		return map[string]any{"inbound": automationInboundView(inbound), "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("inbounds.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		inbound, err := s.decodeInboundCreateOperation(input)
		if err != nil {
			return nil, err
		}
		if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
			return nil, err
		}
		return s.inboundAutomationRevisions(ctx, principal, 0, inbound.ServerID)
	})
	s.automation.Register("inbounds.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		inbound, err := s.decodeInboundCreateOperation(input)
		if err != nil {
			return nil, err
		}
		if err := s.application.PrepareInboundCreate(ctx, &inbound); err != nil {
			return nil, err
		}
		if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
			return nil, err
		}
		if err := s.store.CreateInbound(ctx, &inbound); err != nil {
			return nil, err
		}
		if err := s.saveInboundCertificateBinding(ctx, inbound); err != nil {
			_ = s.store.Delete(ctx, "inbounds", inbound.ID)
			return nil, err
		}
		return map[string]any{"inbound": automationInboundView(inbound), "requires_deployment": true}, nil
	})

	s.automation.RegisterValidator("inbounds.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		_, inbound, err := s.decodeInboundUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
			return nil, err
		}
		return map[string]any{"inbound": automationInboundView(inbound), "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("inbounds.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		current, inbound, err := s.decodeInboundUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
			return nil, err
		}
		return s.inboundAutomationRevisions(ctx, principal, current.ID, inbound.ServerID)
	})
	s.automation.Register("inbounds.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		current, inbound, err := s.decodeInboundUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateInboundAutomationCandidate(ctx, principal, &inbound); err != nil {
			return nil, err
		}
		oldDomain := normalizeDomainName(current.DNSDomain)
		newDomain := normalizeDomainName(inbound.DNSDomain)
		if current.DNSSyncEnabled && (!inbound.DNSSyncEnabled || oldDomain != newDomain) {
			if err := s.deleteDNSInboundRecords(ctx, *current); err != nil {
				return nil, err
			}
		}
		if err := s.store.UpdateInbound(ctx, &inbound); err != nil {
			return nil, err
		}
		if err := s.saveInboundCertificateBinding(ctx, inbound); err != nil {
			return nil, err
		}
		if stored, getErr := s.store.GetInbound(ctx, inbound.ID); getErr == nil {
			inbound = *stored
		}
		return map[string]any{"inbound": automationInboundView(inbound), "requires_deployment": true}, nil
	})
}

func (request inboundPaddingUpdateOperation) coreOperation() core.AnyTLSPaddingOperation {
	return core.AnyTLSPaddingOperation{Operation: request.Operation, PresetID: request.PresetID, AutoTune: request.AutoTune, Scheme: request.PaddingScheme}
}

func (s *Server) decodeInboundPaddingUpdateOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (inboundPaddingUpdateOperation, *model.Inbound, error) {
	var request inboundPaddingUpdateOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, nil, err
	}
	if !model.HasManagementAccess(principal.Role) {
		return request, nil, errors.New("management role required")
	}
	if request.InboundID <= 0 {
		return request, nil, errors.New("inbound_id must be a positive integer")
	}
	inbound, err := s.store.GetInbound(ctx, request.InboundID)
	if err != nil {
		return request, nil, err
	}
	if !principal.AllowsInt64("server_ids", inbound.ServerID) {
		return request, nil, errors.New("inbound is outside the authorized server boundary")
	}
	if inbound.Protocol != model.ProtocolAnyTLS {
		return request, nil, errors.New("AnyTLS padding operations require an AnyTLS inbound")
	}
	return request, inbound, nil
}

func (s *Server) inboundDeleteAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.Inbound, int, error) {
	var request struct {
		InboundID int64 `json:"inbound_id"`
		Confirm   bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Inbound{}, 0, err
	}
	if request.InboundID <= 0 || !request.Confirm {
		return model.Inbound{}, 0, errors.New("inbound_id and confirm=true are required")
	}
	inbound, err := s.store.GetInbound(ctx, request.InboundID)
	if err != nil {
		return model.Inbound{}, 0, err
	}
	if !principal.AllowsInt64("server_ids", inbound.ServerID) {
		return model.Inbound{}, 0, errors.New("inbound is outside the authorized server boundary")
	}
	if _, err := s.guardAssignableNodeDelete(ctx, model.AssignableNodeInbound, inbound.ID); err != nil {
		return model.Inbound{}, 0, err
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return model.Inbound{}, 0, err
	}
	pathCount := 0
	for _, path := range paths {
		if path.InboundID != inbound.ID {
			continue
		}
		if !principal.AllowsInt64("proxy_path_ids", path.ID) {
			return model.Inbound{}, 0, errors.New("an attached proxy path is outside the authorized resource boundary")
		}
		if _, err := s.guardAssignableNodeDelete(ctx, model.AssignableNodeProxyPath, path.ID); err != nil {
			return model.Inbound{}, 0, err
		}
		pathCount++
	}
	return *inbound, pathCount, nil
}

func (s *Server) decodeInboundCreateOperation(input json.RawMessage) (model.Inbound, error) {
	var request inboundCreateOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Inbound{}, err
	}
	fields, err := decodeInboundAutomationFields(request.Inbound)
	if err != nil {
		return model.Inbound{}, err
	}
	var inbound model.Inbound
	if err := json.Unmarshal(request.Inbound, &inbound); err != nil {
		return model.Inbound{}, err
	}
	if _, exists := fields["enabled"]; !exists {
		inbound.Enabled = true
	}
	return normalizeInboundAutomationCandidate(inbound, nil)
}

func (s *Server) decodeInboundUpdateOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (*model.Inbound, model.Inbound, error) {
	var request inboundUpdateOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, model.Inbound{}, err
	}
	if request.InboundID <= 0 {
		return nil, model.Inbound{}, errors.New("inbound_id must be a positive integer")
	}
	current, err := s.store.GetInbound(ctx, request.InboundID)
	if err != nil {
		return nil, model.Inbound{}, err
	}
	if !principal.AllowsInt64("server_ids", current.ServerID) {
		return nil, model.Inbound{}, errors.New("inbound is outside the authorized server boundary")
	}
	fields, err := decodeInboundAutomationFields(request.Changes)
	if err != nil {
		return nil, model.Inbound{}, err
	}
	if len(fields) == 0 {
		return nil, model.Inbound{}, errors.New("changes must contain at least one inbound field")
	}
	if _, supplied := fields["anytls_padding"]; supplied {
		return nil, model.Inbound{}, errors.New("anytls_padding can only be changed through inbounds.padding.update")
	}
	var patch model.Inbound
	if err := json.Unmarshal(request.Changes, &patch); err != nil {
		return nil, model.Inbound{}, err
	}
	inbound := mergeInboundPatch(*current, patch, fields)
	inbound.ID = current.ID
	inbound, err = normalizeInboundAutomationCandidate(inbound, current)
	if err == nil && current.Protocol == model.ProtocolAnyTLS && inbound.Protocol == model.ProtocolAnyTLS {
		inbound.ConfigJSON, err = core.PreserveAnyTLSPaddingSnapshot(current.ConfigJSON, inbound.ConfigJSON)
	} else if err == nil && current.Protocol != model.ProtocolAnyTLS && inbound.Protocol == model.ProtocolAnyTLS {
		err = s.application.PrepareInboundCreate(ctx, &inbound)
	}
	return current, inbound, err
}

func decodeInboundAutomationFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("inbound data must be an object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errors.New("inbound data must be an object")
	}
	for field := range fields {
		if !inboundAutomationFields[field] {
			return nil, fmt.Errorf("unsupported inbound field %q", field)
		}
	}
	return fields, nil
}

func normalizeInboundAutomationCandidate(inbound model.Inbound, current *model.Inbound) (model.Inbound, error) {
	inbound.ID = max(inbound.ID, 0)
	inbound.ConfigJSON = strings.TrimSpace(inbound.ConfigJSON)
	if inbound.ConfigJSON == "" {
		inbound.ConfigJSON = "{}"
	}
	if err := applyInboundKindDefaults(&inbound, current); err != nil {
		return model.Inbound{}, err
	}
	config, err := applyInboundConfigDefaults(inbound.Protocol, inbound.ConfigJSON)
	if err != nil {
		return model.Inbound{}, err
	}
	inbound.ConfigJSON = config
	inbound = normalizeInbound(inbound)
	if err := normalizeMieruInboundPorts(&inbound); err != nil {
		return model.Inbound{}, err
	}
	return inbound, nil
}

func (s *Server) validateInboundAutomationCandidate(ctx context.Context, principal application.Principal, inbound *model.Inbound) error {
	if inbound == nil {
		return errors.New("inbound required")
	}
	if !principal.AllowsInt64("server_ids", inbound.ServerID) {
		return errors.New("inbound server is outside the authorized resource boundary")
	}
	if err := s.resolveInboundTemplates(ctx, inbound); err != nil {
		return err
	}
	config, err := applyInboundConfigDefaults(inbound.Protocol, inbound.ConfigJSON)
	if err != nil {
		return err
	}
	inbound.ConfigJSON = config
	if err := normalizeMieruInboundPorts(inbound); err != nil {
		return err
	}
	if err := validateInbound(*inbound); err != nil {
		return err
	}
	if err := s.store.ValidateServerExists(ctx, inbound.ServerID); err != nil {
		return err
	}
	if err := s.validateInboundManagedReferences(ctx, *inbound); err != nil {
		return err
	}
	return s.ensureInboundListenAvailable(ctx, *inbound)
}

func (s *Server) inboundAutomationRevisions(ctx context.Context, principal application.Principal, inboundID, serverID int64) (map[string]string, error) {
	server, err := s.application.GetServer(ctx, principal, serverID)
	if err != nil {
		return nil, err
	}
	topologyRevision, err := s.store.RoutingTopologyRevision(ctx)
	if err != nil {
		return nil, err
	}
	revisions := map[string]string{
		"routing_topology":                          topologyRevision,
		"server:" + strconv.FormatInt(serverID, 10): server.Revision,
	}
	if inboundID > 0 {
		inbound, err := s.store.GetInbound(ctx, inboundID)
		if err != nil {
			return nil, err
		}
		revisions["inbound:"+strconv.FormatInt(inboundID, 10)] = inbound.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return revisions, nil
}

func automationInboundView(inbound model.Inbound) map[string]any {
	view := map[string]any{
		"id": inbound.ID, "revision": inbound.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"server_id": inbound.ServerID, "name": inbound.Name, "protocol": inbound.Protocol,
		"listen_ip": inbound.ListenIP, "port": inbound.Port, "advertise_port": inbound.AdvertisePort, "entry_ip_mode": inbound.EntryIPMode,
		"external_ip": inbound.ExternalIP, "dns_sync_enabled": inbound.DNSSyncEnabled,
		"dns_domain": inbound.DNSDomain, "tls": inbound.TLS, "certificate_mode": inbound.CertificateMode,
		"certificate_domain": inbound.CertificateDomain, "kind": inferredInboundKind(inbound), "enabled": inbound.Enabled,
		"advanced_configured": strings.TrimSpace(inbound.ConfigJSON) != "" && strings.TrimSpace(inbound.ConfigJSON) != "{}",
	}
	if inbound.Protocol == model.ProtocolAnyTLS {
		metadata, scheme, _ := core.AnyTLSPaddingMetadataFromJSON(inbound.ConfigJSON)
		view["anytls_padding"] = map[string]any{"metadata": metadata, "padding_scheme": scheme}
	}
	return view
}
