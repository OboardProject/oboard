package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) authorizeAutomationReplay(ctx context.Context, principal application.Principal, op model.AutomationOperation) error {
	if principal.AccessLevel == "" {
		return nil
	}
	descriptor, ok := s.capabilities.Authorize(principal, op.Capability)
	if !ok {
		return fmt.Errorf("capability %q is not authorized", op.Capability)
	}
	var input map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(op.Input)))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	decision := s.authorizeCapability(ctx, descriptor, input)
	if !decision.Allowed {
		return fmt.Errorf("replay is not authorized: %s", decision.Reason)
	}
	return nil
}
