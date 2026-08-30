package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) mcpPrivilegedAccess(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/mcp/grants/")
	if len(parts) < 2 || parts[1] != "privileged-access" {
		fail(w, errNotFound(), http.StatusNotFound)
		return
	}
	grantID := parts[0]
	switch r.Method {
	case http.MethodGet:
		s.getPrivilegedAccess(w, r, grantID)
	case http.MethodPut, http.MethodPatch:
		s.putPrivilegedAccess(w, r, grantID)
	case http.MethodDelete:
		s.deletePrivilegedAccess(w, r, grantID)
	default:
		method(w)
	}
}

func (s *Server) getPrivilegedAccess(w http.ResponseWriter, r *http.Request, grantID string) {
	grant, err := s.store.GetOAuthGrant(r.Context(), grantID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	item, err := s.store.GetMCPPrivilegedGrantByOAuthGrant(r.Context(), grant.ID)
	if err != nil {
		write(w, http.StatusOK, map[string]any{"grant_id": grant.ID, "privileged_access": nil})
		return
	}
	write(w, http.StatusOK, map[string]any{"grant_id": grant.ID, "privileged_access": privilegedAccessView(*item)})
}

type privilegedAccessInput struct {
	StepUpToken      string          `json:"step_up_token"`
	Capabilities     []string        `json:"capabilities"`
	ResourceBoundary json.RawMessage `json:"resource_boundary"`
	ExpiresAt        *time.Time      `json:"expires_at"`
	UntilRevoked     bool            `json:"until_revoked"`
}

func (s *Server) putPrivilegedAccess(w http.ResponseWriter, r *http.Request, grantID string) {
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	grant, err := s.store.GetOAuthGrant(r.Context(), grantID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	var req privilegedAccessInput
	if !decode(w, r, &req) {
		return
	}
	next, err := normalizePrivilegedGrantInput(grant, user.ID, req)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	existing, _ := s.store.GetMCPPrivilegedGrantByOAuthGrant(r.Context(), grant.ID)
	if privilegedGrantElevates(existing, next) {
		if err := s.consumeStepUp(r, req.StepUpToken, model.StepUpPurposePrivilegedGrant, "oauth_grant", grant.ID); err != nil {
			fail(w, err, http.StatusForbidden)
			return
		}
	}
	saved, err := s.store.UpsertMCPPrivilegedGrant(r.Context(), next)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	event := model.RemoteAccessAuditPrivilegedGrantUpdated
	if existing == nil {
		event = model.RemoteAccessAuditPrivilegedGrantCreated
	}
	s.recordRemoteAccessAudit(r, model.RemoteAccessAuditEvent{
		EventType: event, ActorType: "user", ActorUserID: &user.ID, OAuthGrantID: grant.ID, OAuthClientID: grant.ClientID,
		Result: "updated", Capability: strings.Join(saved.Capabilities, ","),
	})
	s.mcpInvalidateRegistry()
	// Immediately close MCP interactive sessions that are no longer authorized by the new grant.
	s.enforceMCPTerminalsForGrant(grant.ID, saved)
	write(w, http.StatusOK, map[string]any{"privileged_access": privilegedAccessView(*saved)})
}

func (s *Server) deletePrivilegedAccess(w http.ResponseWriter, r *http.Request, grantID string) {
	user := currentUser(r)
	grant, err := s.store.GetOAuthGrant(r.Context(), grantID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	item, err := s.store.GetMCPPrivilegedGrantByOAuthGrant(r.Context(), grant.ID)
	if err != nil {
		write(w, http.StatusOK, map[string]any{"revoked": true})
		return
	}
	if err := s.store.RevokeMCPPrivilegedGrant(r.Context(), item.ID); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if user != nil {
		s.recordRemoteAccessAudit(r, model.RemoteAccessAuditEvent{
			EventType: model.RemoteAccessAuditPrivilegedGrantRevoked, ActorType: "user", ActorUserID: &user.ID,
			OAuthGrantID: grant.ID, OAuthClientID: grant.ClientID, Result: "revoked",
		})
	}
	s.mcpInvalidateRegistry()
	s.closeMCPTerminalsForGrant(grant.ID)
	write(w, http.StatusOK, map[string]any{"revoked": true})
}

func normalizePrivilegedGrantInput(grant *model.OAuthGrant, actorID int64, req privilegedAccessInput) (model.MCPPrivilegedGrant, error) {
	caps := []string{}
	seen := map[string]bool{}
	for _, item := range req.Capabilities {
		switch strings.TrimSpace(item) {
		case model.PrivilegeRemoteOperations, model.PrivilegeRemoteExec, model.PrivilegeRemoteShell, model.PrivilegeRemoteInteractive:
			if !seen[item] {
				caps = append(caps, item)
				seen[item] = true
			}
		case "":
		default:
			return model.MCPPrivilegedGrant{}, errors.New("unsupported privileged capability")
		}
	}
	boundary := mcpauth.ResourceBoundary{Version: mcpauth.ResourceBoundaryVersion, Resources: map[string]mcpauth.ResourceSelection{
		"server": {Selection: mcpauth.SelectionNone, IncludeFuture: false, AllowCreate: false},
	}}
	if len(req.ResourceBoundary) > 0 {
		if err := json.Unmarshal(req.ResourceBoundary, &boundary); err != nil {
			return model.MCPPrivilegedGrant{}, errors.New("invalid resource_boundary")
		}
	}
	boundary = boundary.Normalized()
	if sel, ok := boundary.Resources["server"]; ok {
		sel.AllowCreate = false
		if sel.Selection == mcpauth.SelectionAll && !sel.IncludeFuture {
			// keep include_future as provided; default false
		}
		if sel.Selection == "" {
			sel.Selection = mcpauth.SelectionSelected
		}
		boundary.Resources["server"] = sel
	}
	raw, _ := json.Marshal(boundary)
	item := model.MCPPrivilegedGrant{
		OAuthGrantID: grant.ID, OAuthClientID: grant.ClientID, AuthorizedUserID: grant.UserID,
		Capabilities: caps, ResourceBoundaryJSON: raw, CreatedByUserID: actorID,
	}
	if req.ExpiresAt != nil && !req.UntilRevoked {
		exp := req.ExpiresAt.UTC()
		item.ExpiresAt = &exp
	}
	return item, nil
}

func privilegedGrantElevates(existing *model.MCPPrivilegedGrant, next model.MCPPrivilegedGrant) bool {
	if existing == nil || !existing.Active(time.Now().UTC()) {
		return len(next.Capabilities) > 0
	}
	for _, cap := range next.Capabilities {
		if !existing.HasCapability(cap) {
			return true
		}
	}
	var prevBound, nextBound mcpauth.ResourceBoundary
	_ = json.Unmarshal(existing.ResourceBoundaryJSON, &prevBound)
	_ = json.Unmarshal(next.ResourceBoundaryJSON, &nextBound)
	prevSel := prevBound.Selection("server")
	nextSel := nextBound.Selection("server")
	if prevSel.Selection != mcpauth.SelectionAll && nextSel.Selection == mcpauth.SelectionAll {
		return true
	}
	if !prevSel.IncludeFuture && nextSel.IncludeFuture {
		return true
	}
	if nextSel.Selection == mcpauth.SelectionSelected {
		for _, id := range nextSel.IDs {
			found := false
			for _, prev := range prevSel.IDs {
				if prev == id {
					found = true
					break
				}
			}
			if !found && prevSel.Selection != mcpauth.SelectionAll {
				return true
			}
		}
	}
	if existing.ExpiresAt != nil && next.ExpiresAt == nil {
		return true
	}
	if existing.ExpiresAt != nil && next.ExpiresAt != nil && next.ExpiresAt.After(*existing.ExpiresAt) {
		return true
	}
	return false
}

func privilegedAccessView(item model.MCPPrivilegedGrant) map[string]any {
	var boundary any
	_ = json.Unmarshal(item.ResourceBoundaryJSON, &boundary)
	return map[string]any{
		"id": item.ID, "oauth_grant_id": item.OAuthGrantID, "oauth_client_id": item.OAuthClientID,
		"capabilities": item.Capabilities, "resource_boundary": boundary,
		"expires_at": item.ExpiresAt, "revoked_at": item.RevokedAt, "revision": item.Revision,
		"last_step_up_at": item.LastStepUpAt, "updated_at": item.UpdatedAt,
	}
}

func loadPrivilegedGrantPolicy(item *model.MCPPrivilegedGrant) *mcpauth.PrivilegedGrantPolicy {
	if item == nil || !item.Active(time.Now().UTC()) {
		return nil
	}
	var boundary mcpauth.ResourceBoundary
	_ = json.Unmarshal(item.ResourceBoundaryJSON, &boundary)
	return &mcpauth.PrivilegedGrantPolicy{
		ID: item.ID, OAuthGrantID: item.OAuthGrantID, Capabilities: item.Capabilities,
		ResourceBoundary: boundary.Normalized(), ExpiresAt: item.ExpiresAt, RevokedAt: item.RevokedAt, Revision: item.Revision,
	}
}
