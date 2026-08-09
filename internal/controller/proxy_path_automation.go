package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type proxyPathUpdateOperation struct {
	PathID  int64           `json:"path_id"`
	Changes json.RawMessage `json:"changes"`
}

type proxyPathStepCreateOperation struct {
	Step json.RawMessage `json:"step"`
}

type proxyPathStepUpdateOperation struct {
	StepID  int64           `json:"step_id"`
	Changes json.RawMessage `json:"changes"`
}

var proxyPathAutomationFields = map[string]bool{
	"name_mode": true, "name_template": true, "inbound_id": true,
	"exit_region_mode": true, "exit_region_code": true, "enabled": true,
}

var proxyPathStepAutomationFields = map[string]bool{
	"path_id": true, "position": true, "node_type": true, "transport_mode": true,
	"server_id": true, "inbound_id": true, "external_outbound_id": true,
	"chain_protocol": true, "chain_method": true, "reality_handshake_server": true,
	"reality_handshake_port": true, "tunnel_type": true, "ssh_port": true,
	"persistent_keepalive": true, "backend": true, "listen_ip": true,
}

func (s *Server) registerProxyPathAutomationOperations() {
	s.automation.RegisterValidator("proxy_paths.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		path, err := s.proxyPathDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path_id": path.ID, "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("proxy_paths.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		if _, err := s.proxyPathDeleteAutomationCandidate(ctx, principal, input); err != nil {
			return nil, err
		}
		return s.routingTopologyAutomationRevision(ctx)
	})
	s.automation.Register("proxy_paths.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		path, err := s.proxyPathDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteProxyPath(ctx, path.ID); err != nil {
			return nil, err
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "path_id": path.ID, "requires_deployment": true}, nil
	})

	s.automation.RegisterValidator("proxy_path_steps.truncate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		_, deleted, pathDeleted, err := s.proxyPathStepTruncateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"deleted_steps": deleted, "path_deleted": pathDeleted, "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("proxy_path_steps.truncate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		if _, _, _, err := s.proxyPathStepTruncateAutomationCandidate(ctx, principal, input); err != nil {
			return nil, err
		}
		return s.routingTopologyAutomationRevision(ctx)
	})
	s.automation.Register("proxy_path_steps.truncate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		step, deleted, pathDeleted, err := s.proxyPathStepTruncateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteProxyPathStepsFromPosition(ctx, step.PathID, step.Position); err != nil {
			return nil, err
		}
		if pathDeleted {
			if err := s.store.DeleteProxyPath(ctx, step.PathID); err != nil {
				return nil, err
			}
		} else {
			if err := s.normalizeAndValidateProxyPath(ctx, step.PathID); err != nil {
				return nil, err
			}
			if err := s.store.ClearProxyPathBranchSource(ctx, step.PathID); err != nil {
				return nil, err
			}
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "path_id": step.PathID, "deleted_steps": deleted, "path_deleted": pathDeleted, "requires_deployment": true}, nil
	})

	s.automation.RegisterValidator("proxy_paths.create_direct", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		path, prefix, err := s.directProxyPathAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateDirectProxyPathAutomationCandidate(ctx, path, prefix); err != nil {
			return nil, err
		}
		return map[string]any{"inbound_id": path.InboundID, "copied_step_count": len(prefix), "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("proxy_paths.create_direct", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		path, prefix, err := s.directProxyPathAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateDirectProxyPathAutomationCandidate(ctx, path, prefix); err != nil {
			return nil, err
		}
		return s.routingTopologyAutomationRevision(ctx)
	})
	s.automation.Register("proxy_paths.create_direct", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		path, prefix, err := s.directProxyPathAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateDirectProxyPathAutomationCandidate(ctx, path, prefix); err != nil {
			return nil, err
		}
		path.Secret, err = security.RandomToken(24)
		if err != nil {
			return nil, err
		}
		path.Enabled = false
		if err := s.store.CreateProxyPath(ctx, &path); err != nil {
			return nil, err
		}
		cleanup := func() { _ = s.store.DeleteProxyPath(ctx, path.ID) }
		for index, source := range prefix {
			step := source
			step.ID, step.PathID, step.Position = 0, path.ID, index+1
			step.ProcessingRole, step.CreatedAt, step.UpdatedAt = false, time.Time{}, time.Time{}
			if err := s.store.CreateProxyPathStep(ctx, &step); err != nil {
				cleanup()
				return nil, err
			}
		}
		path.Enabled = true
		if err := s.validateProxyPath(ctx, &path); err != nil {
			cleanup()
			return nil, err
		}
		if err := s.store.UpdateProxyPath(ctx, &path); err != nil {
			cleanup()
			return nil, err
		}
		if err := s.validateEnabledProxyPathPlan(ctx, path.ID); err != nil {
			cleanup()
			return nil, err
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			cleanup()
			return nil, err
		}
		if stored, getErr := s.store.GetProxyPath(ctx, path.ID); getErr == nil {
			path = *stored
		}
		path = s.resolvedProxyPath(ctx, path)
		steps, err := s.store.ListProxyPathStepsForPath(ctx, path.ID)
		if err != nil {
			cleanup()
			return nil, err
		}
		views := make([]map[string]any, 0, len(steps))
		for _, step := range steps {
			views = append(views, automationProxyPathStepView(step))
		}
		return map[string]any{"proxy_path": automationProxyPathView(path), "proxy_path_steps": views, "requires_deployment": true}, nil
	})

	s.automation.RegisterValidator("proxy_paths.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		current, path, err := s.decodeProxyPathUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateProxyPathAutomationCandidate(ctx, principal, current, &path); err != nil {
			return nil, err
		}
		return map[string]any{"proxy_path": automationProxyPathView(path), "requires_deployment": true}, nil
	})
	s.automation.RegisterRevisionResolver("proxy_paths.update", s.proxyPathUpdateRevision)
	s.automation.Register("proxy_paths.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		current, path, err := s.decodeProxyPathUpdateOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.validateProxyPathAutomationCandidate(ctx, principal, current, &path); err != nil {
			return nil, err
		}
		if err := s.store.UpdateProxyPath(ctx, &path); err != nil {
			return nil, err
		}
		if path.Enabled {
			if err := s.validateEnabledProxyPathPlan(ctx, path.ID); err != nil {
				_ = s.store.UpdateProxyPath(ctx, current)
				_ = s.normalizeProxyPathProcessingRoles(ctx, path.ID)
				return nil, err
			}
			if err := s.ensureWARPProfilesForProxyPaths(ctx); err != nil {
				_ = s.store.UpdateProxyPath(ctx, current)
				return nil, err
			}
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			return nil, err
		}
		if stored, getErr := s.store.GetProxyPath(ctx, path.ID); getErr == nil {
			path = *stored
		}
		path = s.resolvedProxyPath(ctx, path)
		return map[string]any{"proxy_path": automationProxyPathView(path), "requires_deployment": true}, nil
	})

	for _, capabilityName := range []string{"proxy_path_steps.create", "proxy_path_steps.update"} {
		name := capabilityName
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			_, step, err := s.decodeProxyPathStepAutomationOperation(ctx, principal, name, input)
			if err != nil {
				return nil, err
			}
			if err := s.validateProxyPathStepAutomationCandidate(ctx, principal, &step); err != nil {
				return nil, err
			}
			return map[string]any{"proxy_path_step": automationProxyPathStepView(step), "requires_deployment": true}, nil
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			_, step, err := s.decodeProxyPathStepAutomationOperation(ctx, principal, name, input)
			if err != nil {
				return nil, err
			}
			if err := s.authorizeProxyPathStepCandidate(ctx, principal, step); err != nil {
				return nil, err
			}
			return s.routingTopologyAutomationRevision(ctx)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			current, step, err := s.decodeProxyPathStepAutomationOperation(ctx, principal, name, input)
			if err != nil {
				return nil, err
			}
			if err := s.validateProxyPathStepAutomationCandidate(ctx, principal, &step); err != nil {
				return nil, err
			}
			if current == nil {
				if err := s.store.CreateProxyPathStep(ctx, &step); err != nil {
					return nil, err
				}
			} else if err := s.store.UpdateProxyPathStep(ctx, &step); err != nil {
				return nil, err
			}
			rollback := func() {
				if current == nil {
					_ = s.store.Delete(ctx, "proxy_path_steps", step.ID)
				} else {
					_ = s.store.UpdateProxyPathStep(ctx, current)
				}
				_ = s.normalizeProxyPathProcessingRoles(ctx, step.PathID)
			}
			if err := s.normalizeAndValidateProxyPath(ctx, step.PathID); err != nil {
				rollback()
				return nil, err
			}
			if err := s.ensureWARPProfilesForProxyPaths(ctx); err != nil {
				rollback()
				return nil, err
			}
			if current != nil {
				if err := s.store.ClearProxyPathBranchSourcesFromPosition(ctx, current.PathID, current.Position); err != nil {
					return nil, err
				}
			}
			if err := s.store.ClearProxyPathBranchSource(ctx, step.PathID); err != nil {
				return nil, err
			}
			if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
				return nil, err
			}
			if stored, getErr := s.store.GetProxyPathStep(ctx, step.ID); getErr == nil {
				step = *stored
			}
			return map[string]any{"proxy_path_step": automationProxyPathStepView(step), "requires_deployment": true}, nil
		})
	}
}

func (s *Server) proxyPathDeleteAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.ProxyPath, error) {
	var request struct {
		PathID  int64 `json:"path_id"`
		Confirm bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.ProxyPath{}, err
	}
	if request.PathID <= 0 || !request.Confirm {
		return model.ProxyPath{}, errors.New("path_id and confirm=true are required")
	}
	path, err := s.store.GetProxyPath(ctx, request.PathID)
	if err != nil {
		return model.ProxyPath{}, err
	}
	root, err := s.store.GetInbound(ctx, path.InboundID)
	if err != nil {
		return model.ProxyPath{}, err
	}
	if !principal.AllowsInt64("proxy_path_ids", path.ID) || !principal.AllowsInt64("server_ids", root.ServerID) {
		return model.ProxyPath{}, errors.New("proxy path is outside the authorized resource boundary")
	}
	if _, err := s.guardAssignableNodeDelete(ctx, model.AssignableNodeProxyPath, path.ID); err != nil {
		return model.ProxyPath{}, err
	}
	return *path, nil
}

func (s *Server) proxyPathStepTruncateAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.ProxyPathStep, int, bool, error) {
	var request struct {
		PathID  int64 `json:"path_id"`
		StepID  int64 `json:"step_id"`
		Confirm bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.ProxyPathStep{}, 0, false, err
	}
	if request.PathID <= 0 || request.StepID <= 0 || !request.Confirm {
		return model.ProxyPathStep{}, 0, false, errors.New("path_id, step_id and confirm=true are required")
	}
	step, err := s.store.GetProxyPathStep(ctx, request.StepID)
	if err != nil {
		return model.ProxyPathStep{}, 0, false, err
	}
	if step.PathID != request.PathID {
		return model.ProxyPathStep{}, 0, false, errors.New("step_id does not belong to path_id")
	}
	if err := s.authorizeProxyPathStepCandidate(ctx, principal, *step); err != nil {
		return model.ProxyPathStep{}, 0, false, err
	}
	steps, err := s.store.ListProxyPathStepsForPath(ctx, step.PathID)
	if err != nil {
		return model.ProxyPathStep{}, 0, false, err
	}
	deleted := 0
	for _, item := range steps {
		if item.Position >= step.Position {
			deleted++
		}
	}
	pathDeleted := deleted == len(steps)
	if !pathDeleted {
		if err := s.validateProxyPathTruncation(ctx, step.PathID, step.Position); err != nil {
			return model.ProxyPathStep{}, 0, false, err
		}
	}
	if pathDeleted {
		if _, err := s.guardAssignableNodeDelete(ctx, model.AssignableNodeProxyPath, step.PathID); err != nil {
			return model.ProxyPathStep{}, 0, false, err
		}
	}
	return *step, deleted, pathDeleted, nil
}

func (s *Server) directProxyPathAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.ProxyPath, []model.ProxyPathStep, error) {
	var request struct {
		InboundID    int64 `json:"inbound_id"`
		SourcePathID int64 `json:"source_path_id"`
		SourceStepID int64 `json:"source_step_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.ProxyPath{}, nil, err
	}
	if (request.InboundID > 0) == (request.SourceStepID > 0) {
		return model.ProxyPath{}, nil, errors.New("exactly one of inbound_id and source_step_id is required")
	}
	inboundID := request.InboundID
	var branchSourceStepID *int64
	prefix := []model.ProxyPathStep{}
	if request.SourceStepID > 0 {
		if request.SourcePathID <= 0 {
			return model.ProxyPath{}, nil, errors.New("source_path_id is required with source_step_id")
		}
		sourceStep, err := s.store.GetProxyPathStep(ctx, request.SourceStepID)
		if err != nil {
			return model.ProxyPath{}, nil, err
		}
		if sourceStep.PathID != request.SourcePathID {
			return model.ProxyPath{}, nil, errors.New("source_step_id does not belong to source_path_id")
		}
		if sourceStep.NodeType != model.ProxyPathStepServerInbound || sourceStep.ServerID == nil && sourceStep.InboundID == nil {
			return model.ProxyPath{}, nil, errors.New("direct branch source must be a controlled server step")
		}
		sourcePath, err := s.store.GetProxyPath(ctx, request.SourcePathID)
		if err != nil {
			return model.ProxyPath{}, nil, err
		}
		if !principal.AllowsInt64("proxy_path_ids", sourcePath.ID) || !sourcePath.Enabled || sourcePath.Kind != model.ProxyPathKindChain {
			return model.ProxyPath{}, nil, errors.New("direct branch source path is unauthorized or not an enabled chain")
		}
		inboundID = sourcePath.InboundID
		steps, err := s.store.ListProxyPathStepsForPath(ctx, sourcePath.ID)
		if err != nil {
			return model.ProxyPath{}, nil, err
		}
		for _, step := range steps {
			if step.Position > sourceStep.Position {
				break
			}
			prefix = append(prefix, step)
			if step.ID == sourceStep.ID {
				branchSourceStepID = &request.SourceStepID
				break
			}
		}
		if branchSourceStepID == nil {
			return model.ProxyPath{}, nil, errors.New("source_step_id is not part of a valid path prefix")
		}
	} else if request.SourcePathID != 0 {
		return model.ProxyPath{}, nil, errors.New("source_path_id is only valid with source_step_id")
	}
	root, err := s.store.GetInbound(ctx, inboundID)
	if err != nil {
		return model.ProxyPath{}, nil, err
	}
	if !principal.AllowsInt64("server_ids", root.ServerID) {
		return model.ProxyPath{}, nil, errors.New("direct branch inbound is outside the authorized resource boundary")
	}
	path := model.ProxyPath{Kind: model.ProxyPathKindDirect, BranchSourceStepID: branchSourceStepID, NameMode: model.ProxyPathNameAuto, NameTemplate: []model.ProxyPathNamePart{}, InboundID: inboundID, ExitRegionMode: "auto", Enabled: true}
	return path, prefix, nil
}

func (s *Server) validateDirectProxyPathAutomationCandidate(ctx context.Context, path model.ProxyPath, prefix []model.ProxyPathStep) error {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	path.ID = 1
	for _, current := range data.ProxyPaths {
		if current.ID >= path.ID {
			path.ID = current.ID + 1
		}
	}
	path.Enabled = false
	if err := s.validateProxyPath(ctx, &path); err != nil {
		return err
	}
	path.Enabled = true
	data.ProxyPaths = append(data.ProxyPaths, path)
	for index, source := range prefix {
		step := source
		step.ID, step.PathID, step.Position = -(int64(index) + 1), path.ID, index+1
		step.ProcessingRole = false
		data.ProxyPathSteps = append(data.ProxyPathSteps, step)
	}
	if err := normalizeProxyPathProcessingRolesInMemory(data.ProxyPathSteps, path.ID); err != nil {
		return err
	}
	resolveRoutingProxyPathNames(&data)
	_, err = core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	return err
}

func (s *Server) decodeProxyPathUpdateOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (*model.ProxyPath, model.ProxyPath, error) {
	var request proxyPathUpdateOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, model.ProxyPath{}, err
	}
	if request.PathID <= 0 {
		return nil, model.ProxyPath{}, errors.New("path_id must be a positive integer")
	}
	current, err := s.store.GetProxyPath(ctx, request.PathID)
	if err != nil {
		return nil, model.ProxyPath{}, err
	}
	if !principal.AllowsInt64("proxy_path_ids", current.ID) {
		return nil, model.ProxyPath{}, errors.New("proxy path is outside the authorized resource boundary")
	}
	fields, err := decodeClosedAutomationFields(request.Changes, proxyPathAutomationFields, "proxy path changes")
	if err != nil {
		return nil, model.ProxyPath{}, err
	}
	if len(fields) == 0 {
		return nil, model.ProxyPath{}, errors.New("changes must contain at least one proxy path field")
	}
	path := *current
	if err := json.Unmarshal(request.Changes, &path); err != nil {
		return nil, model.ProxyPath{}, err
	}
	path.ID, path.Kind, path.BranchSourceStepID, path.Secret = current.ID, current.Kind, current.BranchSourceStepID, current.Secret
	return current, path, nil
}

func (s *Server) validateProxyPathAutomationCandidate(ctx context.Context, principal application.Principal, current *model.ProxyPath, path *model.ProxyPath) error {
	root, err := s.store.GetInbound(ctx, path.InboundID)
	if err != nil {
		return err
	}
	if !principal.AllowsInt64("server_ids", root.ServerID) || !principal.AllowsInt64("proxy_path_ids", current.ID) {
		return errors.New("proxy path references an unauthorized inbound or path")
	}
	return s.validateProxyPath(ctx, path)
}

func (s *Server) proxyPathUpdateRevision(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
	current, path, err := s.decodeProxyPathUpdateOperation(ctx, principal, input)
	if err != nil {
		return nil, err
	}
	if err := s.validateProxyPathAutomationCandidate(ctx, principal, current, &path); err != nil {
		return nil, err
	}
	return s.routingTopologyAutomationRevision(ctx)
}

func (s *Server) decodeProxyPathStepAutomationOperation(ctx context.Context, principal application.Principal, capabilityName string, input json.RawMessage) (*model.ProxyPathStep, model.ProxyPathStep, error) {
	var raw json.RawMessage
	var current *model.ProxyPathStep
	if capabilityName == "proxy_path_steps.create" {
		var request proxyPathStepCreateOperation
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, model.ProxyPathStep{}, err
		}
		raw = request.Step
	} else {
		var request proxyPathStepUpdateOperation
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, model.ProxyPathStep{}, err
		}
		if request.StepID <= 0 {
			return nil, model.ProxyPathStep{}, errors.New("step_id must be a positive integer")
		}
		stored, err := s.store.GetProxyPathStep(ctx, request.StepID)
		if err != nil {
			return nil, model.ProxyPathStep{}, err
		}
		current = stored
		raw = request.Changes
	}
	fields, err := decodeClosedAutomationFields(raw, proxyPathStepAutomationFields, "proxy path step")
	if err != nil {
		return nil, model.ProxyPathStep{}, err
	}
	if len(fields) == 0 {
		return nil, model.ProxyPathStep{}, errors.New("proxy path step data must not be empty")
	}
	step := model.ProxyPathStep{}
	if current != nil {
		step = *current
	}
	var wire struct {
		PathID             int64                            `json:"path_id"`
		Position           int                              `json:"position"`
		NodeType           model.ProxyPathStepNodeType      `json:"node_type"`
		TransportMode      model.ProxyPathStepTransportMode `json:"transport_mode"`
		ServerID           *int64                           `json:"server_id"`
		InboundID          *int64                           `json:"inbound_id"`
		ExternalOutboundID *int64                           `json:"external_outbound_id"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, model.ProxyPathStep{}, err
	}
	if fields["path_id"] != nil {
		step.PathID = wire.PathID
	}
	if fields["position"] != nil {
		step.Position = wire.Position
	}
	if fields["node_type"] != nil {
		step.NodeType = wire.NodeType
	}
	if fields["transport_mode"] != nil {
		step.TransportMode = wire.TransportMode
	}
	if fields["server_id"] != nil {
		step.ServerID = wire.ServerID
	}
	if fields["inbound_id"] != nil {
		step.InboundID = wire.InboundID
	}
	if fields["external_outbound_id"] != nil {
		step.ExternalOutboundID = wire.ExternalOutboundID
	}
	if current != nil && step.PathID != current.PathID {
		return nil, model.ProxyPathStep{}, errors.New("proxy path step cannot move to another path")
	}
	if current == nil && step.Position <= 0 {
		position, err := s.nextProxyPathStepPosition(ctx, step.PathID)
		if err != nil {
			return nil, model.ProxyPathStep{}, err
		}
		step.Position = position
	}
	if hasAnyAutomationField(fields, "node_type", "transport_mode", "chain_protocol", "chain_method", "reality_handshake_server", "reality_handshake_port", "tunnel_type", "ssh_port", "persistent_keepalive", "backend", "listen_ip") || current == nil {
		config := map[string]any{}
		if current != nil && !hasAnyAutomationField(fields, "node_type", "transport_mode") {
			_ = json.Unmarshal([]byte(current.ConfigJSON), &config)
		}
		for _, field := range []string{"chain_protocol", "chain_method", "reality_handshake_server", "reality_handshake_port", "backend", "listen_ip"} {
			if value, ok := fields[field]; ok {
				var decoded any
				_ = json.Unmarshal(value, &decoded)
				config[field] = decoded
			}
		}
		if value, ok := fields["tunnel_type"]; ok {
			var decoded string
			_ = json.Unmarshal(value, &decoded)
			config["type"] = decoded
		}
		for _, field := range []string{"ssh_port", "persistent_keepalive"} {
			if value, ok := fields[field]; ok {
				var decoded any
				_ = json.Unmarshal(value, &decoded)
				config[field] = decoded
			}
		}
		encoded, _ := json.Marshal(config)
		step.ConfigJSON = string(encoded)
	}
	if err := s.authorizeProxyPathStepCandidate(ctx, principal, step); err != nil {
		return nil, model.ProxyPathStep{}, err
	}
	return current, step, nil
}

func (s *Server) authorizeProxyPathStepCandidate(ctx context.Context, principal application.Principal, step model.ProxyPathStep) error {
	path, err := s.store.GetProxyPath(ctx, step.PathID)
	if err != nil {
		return err
	}
	root, err := s.store.GetInbound(ctx, path.InboundID)
	if err != nil {
		return err
	}
	if !principal.AllowsInt64("proxy_path_ids", path.ID) || !principal.AllowsInt64("server_ids", root.ServerID) {
		return errors.New("proxy path step is outside the authorized resource boundary")
	}
	if step.ServerID != nil && !principal.AllowsInt64("server_ids", *step.ServerID) {
		return errors.New("proxy path step references an unauthorized server")
	}
	if step.InboundID != nil {
		inbound, err := s.store.GetInbound(ctx, *step.InboundID)
		if err != nil || !principal.AllowsInt64("server_ids", inbound.ServerID) {
			return errors.New("proxy path step references an unauthorized inbound")
		}
	}
	if step.ExternalOutboundID != nil {
		external, err := s.store.GetExternalOutbound(ctx, *step.ExternalOutboundID)
		if err != nil {
			return err
		}
		if external.ServerID != nil && !principal.AllowsInt64("server_ids", *external.ServerID) {
			return errors.New("proxy path step references an unauthorized external outbound")
		}
	}
	return nil
}

func (s *Server) validateProxyPathStepAutomationCandidate(ctx context.Context, principal application.Principal, step *model.ProxyPathStep) error {
	if err := s.authorizeProxyPathStepCandidate(ctx, principal, *step); err != nil {
		return err
	}
	currentID := step.ID
	if err := s.validateProxyPathStep(ctx, step, currentID); err != nil {
		return err
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	replaced := false
	for index := range data.ProxyPathSteps {
		if currentID > 0 && data.ProxyPathSteps[index].ID == currentID {
			data.ProxyPathSteps[index] = *step
			replaced = true
			break
		}
	}
	if !replaced {
		candidate := *step
		candidate.ID = -1
		data.ProxyPathSteps = append(data.ProxyPathSteps, candidate)
	}
	if err := normalizeProxyPathProcessingRolesInMemory(data.ProxyPathSteps, step.PathID); err != nil {
		return err
	}
	resolveRoutingProxyPathNames(&data)
	_, err = core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	return err
}

func (s *Server) routingTopologyAutomationRevision(ctx context.Context) (map[string]string, error) {
	revision, err := s.store.RoutingTopologyRevision(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"routing_topology": revision}, nil
}

func decodeClosedAutomationFields(raw json.RawMessage, allowed map[string]bool, label string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	for field := range fields {
		if !allowed[field] {
			return nil, fmt.Errorf("unsupported %s field %q", label, field)
		}
	}
	return fields, nil
}

func hasAnyAutomationField(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if _, ok := fields[name]; ok {
			return true
		}
	}
	return false
}

func automationProxyPathView(path model.ProxyPath) map[string]any {
	return map[string]any{
		"id": path.ID, "revision": path.UpdatedAt.UTC().Format(time.RFC3339Nano), "kind": path.Kind,
		"name": path.Name, "name_mode": path.NameMode, "inbound_id": path.InboundID,
		"exit_region_mode": path.ExitRegionMode, "exit_region_code": path.ExitRegionCode, "enabled": path.Enabled,
	}
}

func automationProxyPathStepView(step model.ProxyPathStep) map[string]any {
	return map[string]any{
		"id": step.ID, "revision": step.UpdatedAt.UTC().Format(time.RFC3339Nano), "path_id": step.PathID,
		"position": step.Position, "node_type": step.NodeType, "transport_mode": step.TransportMode,
		"server_id": step.ServerID, "inbound_id": step.InboundID, "external_outbound_id": step.ExternalOutboundID,
	}
}
