package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
)

// resolvePluginChangesetPrincipal prevents an approver's identity from replacing
// the plugin's resource boundary, including when the runner has already exited.
func (s *Server) resolvePluginChangesetPrincipal(ctx context.Context, item *model.AutomationChangeset) (application.Principal, error) {
	denied := application.Principal{}
	runID, ok := strings.CutPrefix(item.PrincipalID, "plugin:")
	if !ok || s.plugins == nil || len(item.Operations) != 1 {
		return denied, plugin.ErrPermissionDenied
	}
	run, err := s.store.GetPluginRunByUUID(ctx, runID)
	if err != nil {
		return denied, plugin.ErrPermissionDenied
	}
	actions, err := s.store.ListPluginRunActions(ctx, run.ID)
	if err != nil {
		return denied, plugin.ErrPermissionDenied
	}
	bound := false
	for _, action := range actions {
		if action.ActionKey == item.IdempotencyKey && action.Capability == plugin.SDKManagementApply &&
			(action.ChangesetID == item.ID || action.ChangesetID == "" && action.Status == model.PluginActionPending && run.Status == model.PluginRunRunning) {
			bound = true
			break
		}
	}
	if !bound {
		return denied, plugin.ErrPermissionDenied
	}
	principal, err := s.plugins.EffectiveChangesetPrincipal(ctx, run)
	name := item.Operations[0].Capability
	if err != nil || !principal.HasScope(plugin.SDKManagementApply) || !principal.HasScope("management:"+name) || !plugin.ManagementCapabilityAllowed(name) {
		return denied, plugin.ErrPermissionDenied
	}
	descriptor, exists := s.capabilities.Get(name)
	if !exists || descriptor.ReadOnly || !descriptor.Executable || descriptor.AdminOnly || descriptor.PrivilegeClass != "" {
		return denied, plugin.ErrPermissionDenied
	}
	if run.CallerPrincipal != "" {
		policy, err := s.store.GetApprovalPolicy(ctx, run.CallerPrincipal, name, time.Now().UTC())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return denied, err
		}
		if err == nil && policy.Mode == model.ApprovalDenied {
			return denied, plugin.ErrPermissionDenied
		}
	}
	principal.Scopes = append([]string(nil), descriptor.RequiredScopes...)
	return principal, nil
}

func (s *Server) PluginManagement(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, sdk string, request plugin.ManagementRequest) (map[string]any, error) {
	if principal.ID != "plugin:"+run.UUID || !plugin.ManagementCapabilityAllowed(request.Capability) {
		return nil, plugin.ErrPermissionDenied
	}
	effective, err := s.plugins.EffectivePrincipal(ctx, run)
	if err != nil || !effective.HasScope(sdk) || !effective.HasScope("management:"+request.Capability) {
		return nil, plugin.ErrPermissionDenied
	}
	descriptor, exists := s.capabilities.Get(request.Capability)
	if !exists || descriptor.AdminOnly || descriptor.PrivilegeClass != "" {
		return nil, plugin.ErrPermissionDenied
	}
	principal = effective
	principal.Scopes = append([]string(nil), descriptor.RequiredScopes...)
	if _, allowed := s.capabilities.Authorize(principal, request.Capability); !allowed {
		return nil, plugin.ErrPermissionDenied
	}
	raw, err := json.Marshal(request.Input)
	if err != nil {
		return nil, plugin.ErrPermissionDenied
	}
	if sdk == plugin.SDKManagementQuery {
		if !descriptor.ReadOnly {
			return nil, plugin.ErrPermissionDenied
		}
		result, err := s.queryManagementCapability(ctx, principal, request.Capability, raw)
		if err != nil {
			return nil, err
		}
		return map[string]any{"data": result}, nil
	}
	if sdk != plugin.SDKManagementPreview && sdk != plugin.SDKManagementApply || descriptor.ReadOnly || !descriptor.Executable {
		return nil, plugin.ErrPermissionDenied
	}
	if sdk == plugin.SDKManagementApply && run.Mode != model.PluginRunModeLive {
		return nil, plugin.ErrPermissionDenied
	}
	operations := []automation.OperationRequest{{Capability: request.Capability, Input: raw}}
	revisions := plugin.MustJSON(request.ExpectedRevisions)
	if sdk == plugin.SDKManagementPreview {
		preview, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{BaseRevisions: revisions, Operations: operations})
		if err != nil {
			return nil, err
		}
		return map[string]any{"preview": preview}, nil
	}
	// A caller's explicit denial remains authoritative even with a plugin grant.
	if run.CallerPrincipal != "" {
		policy, err := s.store.GetApprovalPolicy(ctx, run.CallerPrincipal, request.Capability, time.Now().UTC())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && policy.Mode == model.ApprovalDenied {
			return nil, plugin.ErrPermissionDenied
		}
	}
	draft, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{BaseRevisions: revisions, Operations: operations})
	if err != nil {
		return nil, err
	}
	item, err := s.automation.Create(ctx, principal, automation.CreateRequest{Reason: request.Reason, IdempotencyKey: action.ActionKey, BaseRevisions: plugin.MustJSON(draft.ExpectedRevisions), Operations: operations})
	if err != nil {
		return nil, err
	}
	if item.Status == model.ChangesetDraft {
		item, err = s.automation.Validate(ctx, principal, item.ID)
	}
	if err != nil {
		return nil, err
	}
	if item.Status == model.ChangesetApproved {
		item, err = s.applyAutomationChangeset(ctx, principal, item.ID)
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{"changeset_id": item.ID, "status": item.Status, "approval_required": item.Status == model.ChangesetAwaitingApproval}
	if len(item.Operations) == 1 {
		result["operation_id"] = item.Operations[0].ID
	}
	return result, nil
}
