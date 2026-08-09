package controller

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type mcpGrantPrincipalContextKey struct{}

// mcpAuth is the ONLY authentication path for /mcp. It accepts OAuth 2.1
// Authorization Code + PKCE S256 Bearer access tokens bound to the exact
// canonical MCP resource. Browser session cookies, API keys (obk_),
// Basic/Client Secret auth, URL query tokens, and custom signed tokens are
// permanently rejected.
func (s *Server) mcpAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			s.writeMCPAuthenticationRequired(w, r, false)
			return
		}
		if strings.HasPrefix(token, "obk_") || strings.HasPrefix(token, "obr_") || strings.Contains(token, " ") {
			s.writeMCPInvalidBearer(w, r)
			return
		}
		canonical, err := s.publicBaseURL(r.Context())
		if err != nil {
			s.writeMCPInvalidBearer(w, r)
			return
		}
		resource := strings.TrimRight(canonical, "/") + "/mcp"
		storedToken, _, userStatus, _, err := s.store.AuthenticateMCPAccessToken(r.Context(), security.HashAPISecret(s.sessionSecret, token), resource, time.Now().UTC())
		if err != nil || errors.Is(err, sql.ErrNoRows) {
			s.writeMCPInvalidBearer(w, r)
			return
		}
		if userStatus != "active" {
			s.writeMCPInvalidBearer(w, r)
			return
		}
		grant, effectiveRole, active, err := s.store.ResolveActiveGrant(r.Context(), storedToken.GrantID, time.Now().UTC())
		if err != nil || !active || effectiveRole == model.RoleNone {
			s.writeMCPInvalidBearer(w, r)
			return
		}
		client, err := s.store.GetOAuthClient(r.Context(), storedToken.ClientID)
		if err != nil || !client.Enabled {
			s.writeMCPInvalidBearer(w, r)
			return
		}
		if client.IdentityType == "cimd" {
			if err := s.refreshClientMetadataIfStale(r.Context(), client); err != nil {
				s.writeMCPInvalidBearer(w, r)
				return
			}
		}
		accessLevel := mcpAccessLevelForRole(effectiveRole)
		boundary := s.oauthRoleBoundary(effectiveRole)
		grantPolicy := mcpauth.GrantPolicy{
			GrantID: grant.ID, ClientID: grant.ClientID, UserID: strconv.FormatInt(grant.UserID, 10),
			PrincipalID: grant.PrincipalID, AccessLevel: accessLevel,
			ResourceBoundary: boundary, ApprovalProfile: grant.ApprovalProfileID,
			ApprovalMaxRisk: grantApprovalMaxRisk(grant), OfflineAccess: grant.OfflineAccess,
			PolicyVersion: grant.PolicyVersion, RoleVersion: grant.RoleVersion,
			ConsentVersion: grant.ConsentVersion, IssuedAt: grant.CreatedAt,
			ExpiresAt: grant.ExpiresAt, RevokedAt: grant.RevokedAt,
		}
		principal := application.Principal{
			ID: grant.PrincipalID, GrantID: grant.ID, UserID: &grant.UserID,
			Name: client.Name + " / " + string(effectiveRole), Type: model.APIPrincipalOAuth,
			Role: effectiveRole, AccessLevel: grantPolicy.AccessLevel, GrantPolicy: &grantPolicy,
			ClientName: client.Name, Interactive: false,
		}
		principal.Scopes = s.capabilities.ScopesForGrant(principal)
		principal.ResourceFilter = application.ResourceFilterFromBoundary(boundary)
		if source, parseErr := netip.ParseAddr(clientIP(r)); parseErr == nil {
			principal.SourceIP = source
		}
		if principal.SourceIP == (netip.Addr{}) {
			principal.SourceIP = netip.MustParseAddr("0.0.0.0")
		}
		ctx := context.WithValue(r.Context(), apiPrincipalContextKey{}, principal)
		ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grantPolicy, Role: effectiveRole, UserID: grant.UserID, ClientID: grant.ClientID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func grantApprovalMaxRisk(grant *model.OAuthGrant) int {
	if grant.ApprovalProfile != nil {
		return grant.ApprovalProfile.AutoApproveRisk
	}
	return 0
}

func bearerToken(authorization string) (string, bool) {
	authorization = strings.TrimSpace(authorization)
	if !strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(authorization[len("bearer "):])
	return token, token != ""
}

func (s *Server) writeMCPInvalidBearer(w http.ResponseWriter, r *http.Request) {
	base, err := s.publicBaseURL(r.Context())
	if err != nil {
		v2Error(w, r, http.StatusServiceUnavailable, "oauth_metadata_unavailable", err.Error())
		return
	}
	challenge := `Bearer resource_metadata="` + oauthProtectedResourceMetadataURL(base) + `", error="invalid_token", error_description="The token is not valid for this MCP resource."`
	w.Header().Set("WWW-Authenticate", challenge)
	v2Error(w, r, http.StatusUnauthorized, "invalid_token", "访问 Token 无效或已过期")
}

// mcpGrantPrincipal returns the grant principal for the unified evaluator.
func mcpGrantPrincipal(ctx context.Context) (mcpauth.GrantPrincipal, error) {
	principal, ok := ctx.Value(mcpGrantPrincipalContextKey{}).(mcpauth.GrantPrincipal)
	if !ok || principal.Grant.GrantID == "" {
		return mcpauth.GrantPrincipal{}, errors.New("authenticated OAuth grant is required")
	}
	return principal, nil
}
