package controller

import (
	"context"
	"database/sql"
	"errors"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) applyAutomationChangeset(ctx context.Context, principal application.Principal, id string) (*model.AutomationChangeset, error) {
	item, err := s.automation.Apply(ctx, principal, id)
	if err != nil || !s.planHasExternalAction(item) {
		return item, err
	}
	workflow, err := s.store.FindAutomationWorkflowByChangeset(ctx, item.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return item, nil
	}
	if err != nil {
		return item, err
	}
	owner := principal
	owner.ID, owner.GrantID = workflow.PrincipalID, workflow.GrantID
	actionID, err := s.storeOneTimeExternalAction(ctx, owner, workflow, item)
	if err != nil {
		return item, err
	}
	if actionID == "" {
		return item, nil
	}
	if _, err := s.automation.RequireWorkflowExternalAction(ctx, principal, workflow.ID, item); err != nil {
		return item, err
	}
	persisted, err := s.automation.Get(ctx, item.ID)
	if err != nil {
		return item, err
	}
	return persisted, nil
}
