package controller

import (
	"context"
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
)

func (s *Server) resolvePluginCaller(ctx context.Context, run model.PluginRun) (application.Principal, error) {
	var snapshot struct {
		GrantID  string                 `json:"caller_grant_id"`
		SourceIP string                 `json:"caller_source_ip"`
		Type     model.APIPrincipalType `json:"caller_type"`
	}
	if json.Unmarshal(run.SnapshotJSON, &snapshot) != nil {
		return application.Principal{}, plugin.ErrPermissionDenied
	}
	ip, _ := netip.ParseAddr(snapshot.SourceIP)
	if raw, human := strings.CutPrefix(run.CallerPrincipal, "user:"); human && snapshot.GrantID == "" && snapshot.Type == model.APIPrincipalOAuth {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return application.Principal{}, plugin.ErrPermissionDenied
		}
		user, err := s.store.GetUser(ctx, id)
		if err != nil || user == nil || user.Status != "active" {
			return application.Principal{}, plugin.ErrPermissionDenied
		}
		role, err := s.store.EffectiveUserRole(ctx, *user)
		if err != nil || role == model.RoleNone {
			return application.Principal{}, plugin.ErrPermissionDenied
		}
		return application.HumanPrincipal(*user, role, ip), nil
	}
	if snapshot.GrantID != "" && snapshot.Type == model.APIPrincipalOAuth {
		grant, role, active, err := s.store.ResolveActiveGrant(ctx, snapshot.GrantID, time.Now().UTC())
		if err != nil || !active || role == model.RoleNone || grant.PrincipalID != run.CallerPrincipal {
			return application.Principal{}, plugin.ErrPermissionDenied
		}
		client, err := s.store.GetOAuthClient(ctx, grant.ClientID)
		if err != nil || !client.Enabled {
			return application.Principal{}, plugin.ErrPermissionDenied
		}
		if client.IdentityType == "cimd" {
			if err := s.refreshClientMetadataIfStale(ctx, client); err != nil {
				return application.Principal{}, plugin.ErrPermissionDenied
			}
		}
		boundary := s.oauthRoleBoundary(role)
		policy := &mcpauth.GrantPolicy{GrantID: grant.ID, ClientID: grant.ClientID, UserID: strconv.FormatInt(grant.UserID, 10), PrincipalID: grant.PrincipalID, AccessLevel: mcpAccessLevelForRole(role), ResourceBoundary: boundary, ApprovalProfile: grant.ApprovalProfileID, ApprovalMaxRisk: grantApprovalMaxRisk(grant), OfflineAccess: grant.OfflineAccess, PolicyVersion: grant.PolicyVersion, RoleVersion: grant.RoleVersion, ConsentVersion: grant.ConsentVersion, IssuedAt: grant.CreatedAt, ExpiresAt: grant.ExpiresAt, RevokedAt: grant.RevokedAt}
		caller := application.Principal{ID: grant.PrincipalID, GrantID: grant.ID, UserID: &grant.UserID, Type: model.APIPrincipalOAuth, Role: role, AccessLevel: policy.AccessLevel, GrantPolicy: policy, ResourceFilter: application.ResourceFilterFromBoundary(boundary), SourceIP: ip, ClientName: client.Name}
		caller.Scopes = s.capabilities.ScopesForGrant(caller)
		return caller, nil
	}
	if snapshot.Type != model.APIPrincipalServiceAccount {
		return application.Principal{}, plugin.ErrPermissionDenied
	}
	stored, err := s.store.GetAPIPrincipal(ctx, run.CallerPrincipal)
	if err != nil || !stored.Enabled || stored.Type != model.APIPrincipalServiceAccount || stored.ExpiresAt != nil && !stored.ExpiresAt.After(time.Now()) || !principalIPAllowed(stored.AllowedCIDRs, ip) {
		return application.Principal{}, plugin.ErrPermissionDenied
	}
	return application.Principal{ID: stored.ID, UserID: stored.OwnerUserID, Name: stored.Name, Type: stored.Type, Scopes: stored.Scopes, ResourceFilter: stored.ResourceFilter, SourceIP: ip}, nil
}
