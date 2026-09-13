package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) authorizeAutomationReplay(ctx context.Context, principal application.Principal, op model.AutomationOperation) error {
	return s.authorizeStoredOperation(ctx, principal, op, false)
}

func (s *Server) authorizeAutomationResult(ctx context.Context, principal application.Principal, op model.AutomationOperation) error {
	return s.authorizeStoredOperation(ctx, principal, op, true)
}

func (s *Server) authorizeStoredOperation(ctx context.Context, principal application.Principal, op model.AutomationOperation, readOnly bool) error {
	if principal.AccessLevel == "" {
		return nil
	}
	descriptor, ok := s.capabilities.Get(op.Capability)
	if !ok || !descriptor.MCPEnabled || descriptor.AdminOnly && principal.Role != model.RoleAdmin {
		return fmt.Errorf("capability %q is not authorized", op.Capability)
	}
	var input map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(op.Input)))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	grant, err := mcpGrantPrincipal(ctx)
	if err != nil {
		return err
	}
	spec := s.capabilitySpec(descriptor)
	if readOnly {
		// Reading a result retains the operation's resource/RBAC/privilege
		// boundary without requiring permission to execute it again.
		spec.MinimumAccess = mcpauth.AccessRead
		spec.ReadOnly = true
	}
	decision := s.mcpEvaluator().Authorize(ctx, grant, spec, input)
	if !decision.Allowed {
		return fmt.Errorf("replay is not authorized: %s", decision.Reason)
	}
	return nil
}
