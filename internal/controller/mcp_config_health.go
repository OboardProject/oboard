package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core/confighealth"
)

// mcpConfigHealthReportInput is the read-side argument object. Its only field
// narrows the report to one resource family.
type mcpConfigHealthReportInput struct {
	Scope string `json:"scope,omitempty"`
}

// readConfigHealthCapability answers config_health.report. It shares the cached
// report with the console, so an automation poll costs the same as a panel poll:
// one revision lookup on a hit.
func (s *Server) readConfigHealthCapability(ctx context.Context, input json.RawMessage) (any, error) {
	var request mcpConfigHealthReportInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if request.Scope != "" && !validConfigHealthScope(request.Scope) {
		return nil, fmt.Errorf("unsupported scope %q", request.Scope)
	}
	entry, err := s.configHealthReport(ctx)
	if err != nil {
		return nil, err
	}
	findings := entry.report.Findings
	if request.Scope != "" {
		filtered := make([]confighealth.Finding, 0, len(findings))
		for _, finding := range findings {
			if finding.Scope == request.Scope {
				filtered = append(filtered, finding)
			}
		}
		findings = filtered
	}
	return map[string]any{
		"fingerprint": entry.fingerprint,
		"summary":     entry.report.Summary,
		"findings":    findings,
	}, nil
}

func validConfigHealthScope(scope string) bool {
	switch scope {
	case confighealth.ScopeInbound, confighealth.ScopeProxyPath, confighealth.ScopeRoutingRule, confighealth.ScopeDNSPolicy, confighealth.ScopeSyncLane:
		return true
	default:
		return false
	}
}

// registerConfigHealthOperations wires the cleanup capability into the
// automation layer. Validation runs the cleanup in preview mode, so a Changeset
// shows the operator exactly which findings would be acted on and what each one
// removes before anything is committed.
func (s *Server) registerConfigHealthOperations() {
	const name = "config_health.cleanup"
	s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeConfigHealthCleanupOperation(input)
		if err != nil {
			return nil, err
		}
		request.Confirm = false
		return s.runConfigHealthCleanup(ctx, nil, request)
	})
	s.automation.RegisterRevisionResolver(name, func(ctx context.Context, _ application.Principal, _ json.RawMessage) (map[string]string, error) {
		entry, err := s.configHealthReport(ctx)
		if err != nil {
			return nil, err
		}
		// Binding the Changeset to the report fingerprint means a commit whose
		// finding set moved after validation is rejected by the same mechanism
		// the REST endpoint uses. The routing revision cannot serve here: it is
		// bumped by ordinary runtime writes that change no finding, so a
		// Changeset bound to it could never be committed on a live fleet.
		return map[string]string{"config_health_report": entry.fingerprint}, nil
	})
	s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeConfigHealthCleanupOperation(input)
		if err != nil {
			return nil, err
		}
		return s.runConfigHealthCleanup(ctx, nil, request)
	})
}

func decodeConfigHealthCleanupOperation(input json.RawMessage) (configHealthCleanupRequest, error) {
	var request configHealthCleanupRequest
	if err := strictAutomationInput(input, &request); err != nil {
		return configHealthCleanupRequest{}, err
	}
	if len(request.Actions) == 0 {
		return configHealthCleanupRequest{}, errors.New("actions is required")
	}
	if len(request.Actions) > configHealthCleanupLimit {
		return configHealthCleanupRequest{}, fmt.Errorf("actions must not exceed %d entries", configHealthCleanupLimit)
	}
	for _, action := range request.Actions {
		if action.Code == "" || action.Scope == "" || action.ResourceID <= 0 {
			return configHealthCleanupRequest{}, errors.New("each action requires code, scope and resource_id")
		}
		if !validConfigHealthScope(action.Scope) {
			return configHealthCleanupRequest{}, fmt.Errorf("unsupported scope %q", action.Scope)
		}
	}
	return request, nil
}

// errConfigHealthRevisionConflict is the automation-side form of the REST 409.
var errConfigHealthRevisionConflict = errors.New("配置在你查看之后发生了变化，请重新体检后再清理")
