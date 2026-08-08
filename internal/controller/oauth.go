package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/authorization"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const (
	oauthAuthorizationCodeTTL = 5 * time.Minute
	oauthAccessTokenTTL       = 15 * time.Minute
	oauthRefreshTokenTTL      = 30 * 24 * time.Hour

	oauthAuthorizationMetadataPath = "/.well-known/oauth-authorization-server"
	oauthProtectedResourcePath     = "/.well-known/oauth-protected-resource"
)

func (s *Server) registerOAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc(oauthAuthorizationMetadataPath, s.oauthAuthorizationMetadata)
	mux.HandleFunc(oauthProtectedResourcePath, s.oauthProtectedResourceMetadata)
	mux.HandleFunc("/oauth/authorize", s.auth(s.oauthAuthorize, model.RoleViewer))
	mux.HandleFunc("/oauth/token", s.oauthToken)
	mux.HandleFunc("/oauth/revoke", s.oauthRevoke)
	// DCR is retired. The route remains during the migration window to return a
	// structured 410 so old clients fail loudly instead of silently dropping
	// their write scopes; the final version removes the route entirely.
	mux.HandleFunc("/oauth/register", s.oauthDynamicRegisterGone)
	mux.HandleFunc("/api/v2/oauth-clients", s.auth(s.oauthClients, model.RoleAdmin))
	mux.HandleFunc("/api/v2/oauth-clients/", s.auth(s.oauthClient, model.RoleAdmin))
	mux.HandleFunc("/api/v2/oauth-grants", s.auth(s.oauthGrants, model.RoleAdmin))
	mux.HandleFunc("/api/v2/oauth-grants/", s.auth(s.oauthGrant, model.RoleAdmin))
}

func (s *Server) oauthAuthorizationMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	base, err := s.publicBaseURL(r.Context())
	if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{mcpauth.ScopeRead, mcpauth.ScopeOperate, mcpauth.ScopeOffline},
		"resource":                              base + "/mcp",
	})
}

func oauthAuthorizationMetadataURL(base string) string {
	return oauthWellKnownURL(base, oauthAuthorizationMetadataPath, "")
}

func oauthProtectedResourceMetadataURL(base string) string {
	return oauthWellKnownURL(base, oauthProtectedResourcePath, "/mcp")
}

func oauthWellKnownURL(base, prefix, suffix string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return ""
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = prefix + basePath + suffix
	parsed.RawPath = ""
	return parsed.String()
}

func (s *Server) matchOAuthWellKnownPath(requestPath string) (string, string, bool) {
	state := s.basePathState()
	basePaths := []string{state.Current}
	if state.MigrationVersion > 0 && state.Previous != state.Current {
		basePaths = append(basePaths, state.Previous)
	}
	for _, basePath := range basePaths {
		if requestPath == oauthAuthorizationMetadataPath+basePath {
			return basePath, oauthAuthorizationMetadataPath, true
		}
		if requestPath == oauthProtectedResourcePath+basePath+"/mcp" {
			return basePath, oauthProtectedResourcePath, true
		}
	}
	return "", "", false
}

func (s *Server) oauthProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	base, err := s.publicBaseURL(r.Context())
	if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base + "/mcp",
		"authorization_servers":    []string{base},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{mcpauth.ScopeRead, mcpauth.ScopeOperate},
		"resource_documentation":   base + "/docs/mcp",
	})
}

// oauthDynamicRegisterGone reports that dynamic client registration is retired
// and instructs the caller to use CIMD (client_id metadata document) or a
// pre-registered client.
func (s *Server) oauthDynamicRegisterGone(w http.ResponseWriter, r *http.Request) {
	oauthError(w, http.StatusGone, "invalid_client_metadata", "dynamic client registration is retired; use a Client ID Metadata Document (CIMD) or a pre-registered client")
}

func (s *Server) oauthClients(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.oauthRegister(w, r)
		return
	}
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	items, err := s.store.ListOAuthClients(r.Context())
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
}

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		MetadataURI  string   `json:"metadata_uri"`
	}
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.newOAuthClient(input.ClientName, input.RedirectURIs)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if metadataURI := strings.TrimSpace(input.MetadataURI); metadataURI != "" {
		metadata, fetchErr := s.fetchClientMetadata(r.Context(), metadataURI)
		if fetchErr != nil {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", fetchErr.Error())
			return
		}
		item.MetadataURI = metadataURI
		item.MetadataHash = metadata.hash
		item.MetadataETag = metadata.etag
		item.MetadataFetchedAt = metadata.fetchedAt
		item.IdentityType = "cimd"
		item.RedirectURIs = metadata.redirectURIs
	}
	if err := s.store.CreateOAuthClient(r.Context(), item); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s.auditOAuthEvent(r, oauthAuditActor(currentUser(r)), "oauth_client_created", "oauth_client", item.ID, map[string]any{"client_name": boundedOAuthAuditValue(item.Name), "redirect_uris": item.RedirectURIs, "identity_type": item.IdentityType})
	writeJSON(w, http.StatusCreated, map[string]any{"client_id": item.ID, "client_name": item.Name, "redirect_uris": item.RedirectURIs, "identity_type": item.IdentityType, "token_endpoint_auth_method": "none"})
}

func (s *Server) oauthGrants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	items, err := s.store.ListOAuthGrants(r.Context())
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
}

func (s *Server) oauthGrant(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v2/oauth-grants/"), "/")
	if id == "" || strings.Contains(id, "/") {
		v2Error(w, r, http.StatusNotFound, "not_found", "OAuth Grant 不存在")
		return
	}
	if r.Method != http.MethodDelete {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	if err := s.store.RevokeOAuthGrant(r.Context(), id, time.Now().UTC()); err != nil {
		v2HandleError(w, r, err)
		return
	}
	auditReq(s, r, "revoke", "oauth_grant", id)
	v2Write(w, r, http.StatusOK, map[string]any{"id": id, "revoked": true}, nil)
}

// newOAuthClient creates a client identity record. It never stores a
// permission ceiling; a client may request whatever the consenting user's role
// allows at grant time.
func (s *Server) newOAuthClient(name string, redirects []string) (*model.OAuthClient, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 || len(redirects) == 0 || len(redirects) > 16 {
		return nil, errors.New("client_name and redirect_uris are required")
	}
	seen := map[string]bool{}
	validated := make([]string, 0, len(redirects))
	for _, raw := range redirects {
		raw = strings.TrimSpace(raw)
		u, err := url.Parse(raw)
		if err != nil || u.Fragment != "" || u.User != nil || u.Host == "" {
			return nil, errors.New("redirect URIs must use HTTPS or an HTTP loopback address")
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackRedirectHost(u.Hostname())) {
			return nil, errors.New("redirect URIs must use HTTPS or an HTTP loopback address")
		}
		if u.Scheme == "https" && isLoopbackRedirectHost(u.Hostname()) && u.Port() == "" {
			return nil, errors.New("loopback HTTPS redirect URIs require an explicit port")
		}
		if !seen[raw] {
			seen[raw] = true
			validated = append(validated, raw)
		}
	}
	if len(validated) == 0 {
		return nil, errors.New("at least one redirect URI is required")
	}
	random, err := security.RandomToken(18)
	if err != nil {
		return nil, err
	}
	return &model.OAuthClient{ID: "oc_" + random, Name: name, RedirectURIs: validated, IdentityType: "preregistered", ClientMetadata: json.RawMessage(`{}`), Enabled: true}, nil
}

func isLoopbackRedirectHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) writeMCPAuthenticationRequired(w http.ResponseWriter, r *http.Request, invalidToken bool) {
	base, err := s.publicBaseURL(r.Context())
	if err != nil {
		v2Error(w, r, http.StatusServiceUnavailable, "oauth_metadata_unavailable", err.Error())
		return
	}
	challenge := "Bearer resource_metadata=" + strconv.Quote(oauthProtectedResourceMetadataURL(base)) + `, scope="oboard:read"`
	code, message := "unauthorized", "需要 OAuth 登录"
	if invalidToken {
		challenge += `, error="invalid_token", error_description="The access token is invalid or expired"`
		code, message = "invalid_token", "访问 Token 无效或已过期"
	}
	w.Header().Set("WWW-Authenticate", challenge)
	v2Error(w, r, http.StatusUnauthorized, code, message)
}

type oauthAuthorizationRequest struct {
	ClientID      string
	RedirectURI   string
	Scope         []string
	State         string
	Resource      string
	CodeChallenge string
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid authorization request")
		return
	}
	request, client, err := s.validateOAuthAuthorizationRequest(r)
	if err != nil {
		s.auditOAuthEvent(r, nil, "oauth_authorization_denied", "oauth_client", boundedOAuthAuditValue(r.Form.Get("client_id")), map[string]any{"reason": "invalid_request"})
		oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user := currentUser(r)
	if r.Method == http.MethodGet {
		if user == nil {
			s.auditOAuthEvent(r, nil, "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "login_required"})
			oauthError(w, http.StatusUnauthorized, "access_denied", "login required")
			return
		}
		if err := s.validateOAuthUserGrant(r.Context(), user, request.Scope); err != nil {
			s.auditOAuthEvent(r, oauthAuditActor(user), "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "scope_denied", "scopes": request.Scope})
			oauthError(w, http.StatusForbidden, "access_denied", err.Error())
			return
		}
		s.renderOAuthConsent(w, r, request, client)
		return
	}
	if r.Form.Get("decision") != "approve" {
		s.auditOAuthEvent(r, oauthAuditActor(user), "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "user_denied", "scopes": request.Scope})
		oauthRedirect(w, r, request.RedirectURI, request.State, "", "access_denied")
		return
	}
	if user == nil {
		s.auditOAuthEvent(r, nil, "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "login_required"})
		oauthError(w, http.StatusUnauthorized, "access_denied", "login required")
		return
	}
	if err := s.validateOAuthUserGrant(r.Context(), user, request.Scope); err != nil {
		s.auditOAuthEvent(r, &user.ID, "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "scope_denied", "scopes": request.Scope})
		oauthError(w, http.StatusForbidden, "access_denied", err.Error())
		return
	}
	grant, principal, err := s.createOAuthGrantV2(r, *user, *client, request.Scope)
	if err != nil {
		s.auditOAuthEvent(r, &user.ID, "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "invalid_consent", "scopes": request.Scope})
		oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	rawCode, err := security.RandomToken(32)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	code := &model.OAuthAuthorizationCode{CodeHash: security.HashAPISecret(s.sessionSecret, rawCode), GrantID: grant.ID, ClientID: client.ID, UserID: user.ID, PrincipalID: principal.ID, RedirectURI: request.RedirectURI, Resource: request.Resource, CodeChallenge: request.CodeChallenge, ExpiresAt: time.Now().UTC().Add(oauthAuthorizationCodeTTL)}
	if err := s.store.CreateOAuthAuthorizationCode(r.Context(), code); err != nil {
		s.auditOAuthEvent(r, &user.ID, "oauth_authorization_denied", "oauth_grant", grant.ID, map[string]any{"client_id": client.ID, "reason": "code_issue_failed"})
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s.auditOAuthEvent(r, &user.ID, "oauth_authorization_granted", "oauth_grant", grant.ID, map[string]any{"client_id": client.ID, "scopes": mcpauth.ParseAccessLevel(grant.AccessLevel).NormalizedScopes(grant.OfflineAccess), "resource": request.Resource, "access_level": grant.AccessLevel})
	s.renderOAuthSuccess(w, r, request.RedirectURI, request.State, rawCode, client.Name)
}

func (s *Server) validateOAuthAuthorizationRequest(r *http.Request) (oauthAuthorizationRequest, *model.OAuthClient, error) {
	request := oauthAuthorizationRequest{ClientID: strings.TrimSpace(r.Form.Get("client_id")), RedirectURI: r.Form.Get("redirect_uri"), Scope: strings.Fields(r.Form.Get("scope")), State: r.Form.Get("state"), Resource: r.Form.Get("resource"), CodeChallenge: r.Form.Get("code_challenge")}
	client, err := s.store.GetOAuthClient(r.Context(), request.ClientID)
	if err != nil || !client.Enabled {
		return request, nil, errors.New("unknown or disabled client")
	}
	if r.Form.Get("response_type") != "code" || r.Form.Get("code_challenge_method") != "S256" || len(request.CodeChallenge) < 43 || len(request.CodeChallenge) > 128 {
		return request, nil, errors.New("authorization code with PKCE S256 is required")
	}
	if !slices.Contains(client.RedirectURIs, request.RedirectURI) {
		return request, nil, errors.New("redirect_uri does not exactly match the registered client")
	}
	base, err := s.publicBaseURL(r.Context())
	if err != nil || request.Resource != base+"/mcp" {
		return request, nil, errors.New("invalid resource audience")
	}
	if len(request.Scope) == 0 {
		return request, nil, errors.New("scope is required")
	}
	for _, scope := range request.Scope {
		if !mcpauth.KnownScope(scope) {
			return request, nil, errors.New("unsupported OAuth scope: " + scope)
		}
	}
	return request, client, nil
}

// normalizeRequestedScopes canonicalizes the client's coarse scope request.
// operate implies read; the server never silently drops operate to read — it
// fails the authorization instead.
func normalizeRequestedScopes(requested []string) (mcpauth.AccessLevel, bool, error) {
	operate := mcpauth.RequestsOperate(requested)
	offline := mcpauth.RequestsOffline(requested)
	for _, scope := range requested {
		if !mcpauth.KnownScope(scope) {
			return "", false, errors.New("unsupported OAuth scope: " + scope)
		}
	}
	level := mcpauth.AccessRead
	if operate {
		level = mcpauth.AccessOperate
	}
	return level, offline, nil
}

// createOAuthGrantV2 creates the grant, its audit principal, approval profile,
// and approval policies from the structured consent. The grant stores the
// coarse access level and the versioned resource boundary; no fine-grained
// scopes or resource filters are used for MCP authorization afterwards.
func (s *Server) createOAuthGrantV2(r *http.Request, user model.User, client model.OAuthClient, scopes []string) (*model.OAuthGrant, *model.APIPrincipal, error) {
	accessLevel, offline, err := normalizeRequestedScopes(scopes)
	if err != nil {
		return nil, nil, err
	}
	boundary, allowCreate, err := s.oauthConsentBoundary(r, accessLevel)
	if err != nil {
		return nil, nil, err
	}
	autoRisk, err := oauthAutoApproveRisk(r.Form.Get("auto_approve_risk"))
	if err != nil {
		return nil, nil, err
	}
	grantToken, err := security.RandomToken(18)
	if err != nil {
		return nil, nil, err
	}
	principalToken, err := security.RandomToken(18)
	if err != nil {
		return nil, nil, err
	}
	profileToken, err := security.RandomToken(18)
	if err != nil {
		return nil, nil, err
	}
	role, err := s.store.EffectiveUserRole(r.Context(), user)
	if err != nil {
		return nil, nil, err
	}
	boundaryJSON, err := json.Marshal(boundary.Normalized())
	if err != nil {
		return nil, nil, err
	}
	appPrincipal := &model.APIPrincipal{ID: "oauth_" + principalToken, OwnerUserID: &user.ID, Name: client.Name + " / " + user.Username, Type: model.APIPrincipalOAuth, Enabled: true, Scopes: accessLevel.NormalizedScopes(offline), ResourceFilter: application.ResourceFilterFromBoundary(boundary), RateLimitPerMinute: 120, MaxConcurrency: 4}
	profile := &model.OAuthApprovalProfile{ID: "oap_" + profileToken, Name: "OAuth grant approval", AutoApproveRisk: autoRisk}
	grant := &model.OAuthGrant{
		ID: "grt_" + grantToken, ClientID: client.ID, ClientName: client.Name, UserID: user.ID, Username: user.Username,
		PrincipalID: appPrincipal.ID, AccessLevel: string(accessLevel), ResourceBoundaryJSON: boundaryJSON,
		Scopes: accessLevel.NormalizedScopes(offline), ResourceFilter: appPrincipal.ResourceFilter,
		ApprovalProfileID: profile.ID, OfflineAccess: offline, PolicyVersion: 1, RoleVersion: 1,
		ConsentVersion: 2, Status: model.OAuthGrantActive,
	}
	principalForEval := application.Principal{ID: appPrincipal.ID, UserID: &user.ID, Name: user.Username, Type: model.APIPrincipalOAuth, Role: role, Scopes: accessLevel.NormalizedScopes(offline), ResourceFilter: appPrincipal.ResourceFilter, AccessLevel: accessLevel}
	policies := []model.ApprovalPolicy{}
	for _, descriptor := range s.capabilities.ListMCP(principalForEval) {
		if !descriptor.Executable {
			continue
		}
		policyToken, randomErr := security.RandomToken(18)
		if randomErr != nil {
			return nil, nil, randomErr
		}
		mode := model.ApprovalRequired
		if descriptor.RiskClass <= autoRisk && descriptor.RiskClass < 4 {
			mode = model.ApprovalAutomatic
		}
		if descriptor.Name == "servers.onboard" && !allowCreate {
			mode = model.ApprovalDenied
		}
		policyFilter := json.RawMessage(`{}`)
		if boundary.Selection("server").Selection == mcpauth.SelectionSelected {
			if encoded, marshalErr := json.Marshal(map[string]any{"server_ids": int64IDs(boundary.Selection("server").IDs)}); marshalErr == nil {
				policyFilter = encoded
			}
		} else if boundary.Selection("server").Selection == mcpauth.SelectionNone {
			policyFilter = json.RawMessage(`{"server_ids":[]}`)
		}
		policies = append(policies, model.ApprovalPolicy{ID: "pol_" + policyToken, PrincipalID: appPrincipal.ID, Capability: descriptor.Name, ResourceFilter: policyFilter, Mode: mode})
	}
	if err := s.store.CreateOAuthGrantV2(r.Context(), grant, appPrincipal, profile, policies); err != nil {
		return nil, nil, err
	}
	return grant, appPrincipal, nil
}

func int64IDs(raw []string) []int64 {
	out := []int64{}
	for _, value := range raw {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			out = append(out, parsed)
		}
	}
	return out
}

// oauthConsentBoundary builds the versioned resource boundary from the consent
// form. Server mode current (all existing, no future) is persisted as a
// selected snapshot so a later-created server is not implicitly included.
func (s *Server) oauthConsentBoundary(r *http.Request, accessLevel mcpauth.AccessLevel) (mcpauth.ResourceBoundary, bool, error) {
	serverMode := strings.ToLower(strings.TrimSpace(r.Form.Get("server_mode")))
	if serverMode == "" {
		serverMode = "current"
	}
	if !slices.Contains([]string{"all", "current", "selected", "none"}, serverMode) {
		return mcpauth.ResourceBoundary{}, false, errors.New("invalid server resource mode")
	}
	selected := []int64{}
	seen := map[int64]bool{}
	for _, raw := range r.Form["server_id"] {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			return mcpauth.ResourceBoundary{}, false, errors.New("invalid server resource id")
		}
		if !seen[id] {
			seen[id] = true
			selected = append(selected, id)
		}
	}
	if serverMode == "selected" && len(selected) == 0 {
		return mcpauth.ResourceBoundary{}, false, errors.New("selected server mode requires at least one server")
	}
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		return mcpauth.ResourceBoundary{}, false, err
	}
	existing := map[int64]bool{}
	for _, server := range servers {
		existing[server.ID] = true
	}
	for _, id := range selected {
		if !existing[id] {
			return mcpauth.ResourceBoundary{}, false, errors.New("selected server is not available")
		}
	}
	slices.Sort(selected)
	serverSelection := mcpauth.ResourceSelection{Selection: mcpauth.SelectionNone}
	switch serverMode {
	case "all":
		serverSelection = mcpauth.ResourceSelection{Selection: mcpauth.SelectionAll, IncludeFuture: true}
	case "current":
		snapshot := make([]string, 0, len(servers))
		for _, server := range servers {
			snapshot = append(snapshot, strconv.FormatInt(server.ID, 10))
		}
		serverSelection = mcpauth.ResourceSelection{Selection: mcpauth.SelectionSelected, IDs: snapshot}
	case "selected":
		ids := make([]string, 0, len(selected))
		for _, id := range selected {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		serverSelection = mcpauth.ResourceSelection{Selection: mcpauth.SelectionSelected, IDs: ids}
	}
	allowCreate := r.Form.Get("allow_create_servers") == "1" && accessLevel == mcpauth.AccessOperate
	serverSelection.AllowCreate = allowCreate
	userMode := strings.ToLower(strings.TrimSpace(r.Form.Get("user_mode")))
	if userMode == "" {
		userMode = "none"
	}
	userSelection := mcpauth.ResourceSelection{Selection: mcpauth.SelectionNone}
	if userMode == "all" {
		userSelection = mcpauth.ResourceSelection{Selection: mcpauth.SelectionAll, IncludeFuture: true}
	} else if userMode != "none" {
		return mcpauth.ResourceBoundary{}, false, errors.New("invalid user resource mode")
	}
	globalCapabilities := []string{}
	for _, descriptor := range s.capabilities.ListMCP(application.Principal{AccessLevel: accessLevel, Role: model.RoleAdmin, Scopes: []string{"*"}}) {
		if len(descriptor.ResourceTypes) == 0 {
			globalCapabilities = append(globalCapabilities, descriptor.Name)
		}
	}
	return mcpauth.ResourceBoundary{
		Version: mcpauth.ResourceBoundaryVersion,
		Resources: map[string]mcpauth.ResourceSelection{
			"server":     serverSelection,
			"user":       userSelection,
			"proxy_path": {Selection: mcpauth.SelectionAll, IncludeFuture: true},
			"inbound":    {Selection: mcpauth.SelectionAll, IncludeFuture: true},
			"deployment": {Selection: mcpauth.SelectionAll, IncludeFuture: true},
		},
		GlobalCapabilities:    globalCapabilities,
		DestructiveOperations: false,
	}, allowCreate, nil
}

func oauthAutoApproveRisk(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 || value > 3 {
		return 0, errors.New("auto_approve_risk must be between 0 and 3")
	}
	return value, nil
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if !s.allowRate(w, r, "oauth-token:"+clientIP(r), 30, time.Minute) {
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid token request")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		s.oauthExchangeCode(w, r)
	case "refresh_token":
		s.oauthExchangeRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

func (s *Server) oauthExchangeCode(w http.ResponseWriter, r *http.Request) {
	code, err := s.store.ConsumeOAuthAuthorizationCode(r.Context(), security.HashAPISecret(s.sessionSecret, r.Form.Get("code")))
	if err != nil || !code.ExpiresAt.After(time.Now().UTC()) || code.ClientID != r.Form.Get("client_id") || code.RedirectURI != r.Form.Get("redirect_uri") || !pkceMatches(code.CodeChallenge, r.Form.Get("code_verifier")) || code.Resource != r.Form.Get("resource") {
		s.auditOAuthEvent(r, nil, "oauth_token_denied", "oauth_client", boundedOAuthAuditValue(r.Form.Get("client_id")), map[string]any{"flow": "authorization_code", "reason": "invalid_grant"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid")
		return
	}
	s.issueOAuthTokens(w, r, "authorization_code", code.GrantID, code.PrincipalID, code.ClientID, code.UserID, code.Resource, "", "")
}

func (s *Server) oauthExchangeRefresh(w http.ResponseWriter, r *http.Request) {
	refresh, err := s.store.ConsumeOAuthRefreshToken(r.Context(), security.HashAPISecret(s.sessionSecret, r.Form.Get("refresh_token")), time.Now().UTC())
	if errors.Is(err, store.ErrOAuthRefreshReuse) {
		var actor *int64
		target := ""
		if refresh != nil {
			actor, target = &refresh.UserID, refresh.GrantID
		}
		s.auditOAuthEvent(r, actor, "oauth_refresh_reuse", "oauth_grant", target, map[string]any{"client_id": boundedOAuthAuditValue(r.Form.Get("client_id")), "reason": "token_family_revoked"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected; token family revoked")
		return
	}
	if err != nil || refresh.ClientID != r.Form.Get("client_id") || refresh.Resource != r.Form.Get("resource") {
		var actor *int64
		target := boundedOAuthAuditValue(r.Form.Get("client_id"))
		if refresh != nil {
			actor, target = &refresh.UserID, refresh.GrantID
		}
		s.auditOAuthEvent(r, actor, "oauth_token_denied", "oauth_grant", target, map[string]any{"flow": "refresh_token", "reason": "invalid_grant"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid")
		return
	}
	s.issueOAuthTokens(w, r, "refresh_token", refresh.GrantID, refresh.PrincipalID, refresh.ClientID, refresh.UserID, refresh.Resource, refresh.FamilyID, refresh.TokenHash)
}

func (s *Server) issueOAuthTokens(w http.ResponseWriter, r *http.Request, flow, grantID, principalID, clientID string, userID int64, resource, familyID, parentTokenHash string) {
	grant, err := s.store.GetOAuthGrant(r.Context(), grantID)
	if err != nil || grant.RevokedAt != nil || grant.Status != model.OAuthGrantActive || grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now().UTC()) || grant.PrincipalID != principalID || grant.ClientID != clientID || grant.UserID != userID {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "inactive_grant"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "OAuth grant is no longer active")
		return
	}
	client, err := s.store.GetOAuthClient(r.Context(), clientID)
	if err != nil || !client.Enabled {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "disabled_client"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "OAuth client is disabled")
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "user_not_authorized"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "user is no longer authorized")
		return
	}
	if err := s.validateOAuthUserGrant(r.Context(), user, mcpauth.ParseAccessLevel(grant.AccessLevel).NormalizedScopes(grant.OfflineAccess)); err != nil {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "user_not_authorized"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "user is no longer authorized for the requested scopes")
		return
	}
	accessRaw, err := security.RandomToken(32)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	now := time.Now().UTC()
	accessPlain := "oba_" + accessRaw
	access := &model.OAuthToken{TokenHash: security.HashAPISecret(s.sessionSecret, accessPlain), GrantID: grantID, PrincipalID: principalID, ClientID: clientID, UserID: userID, Resource: resource, ExpiresAt: now.Add(oauthAccessTokenTTL)}
	var refresh *model.OAuthToken
	refreshPlain := ""
	if grant.OfflineAccess {
		refreshRaw, randomErr := security.RandomToken(32)
		if randomErr != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", randomErr.Error())
			return
		}
		if familyID == "" {
			familyID, _ = security.RandomToken(18)
		}
		refreshPlain = "obr_" + refreshRaw
		refresh = &model.OAuthToken{TokenHash: security.HashAPISecret(s.sessionSecret, refreshPlain), FamilyID: familyID, GrantID: grantID, ParentTokenHash: parentTokenHash, PrincipalID: principalID, ClientID: clientID, UserID: userID, Resource: resource, ExpiresAt: now.Add(oauthRefreshTokenTTL)}
	}
	if err := s.store.CreateOAuthTokens(r.Context(), access, refresh); err != nil {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "token_issue_failed"})
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	scopes := mcpauth.ParseAccessLevel(grant.AccessLevel).NormalizedScopes(grant.OfflineAccess)
	response := map[string]any{"access_token": accessPlain, "token_type": "Bearer", "expires_in": int(oauthAccessTokenTTL.Seconds()), "scope": strings.Join(scopes, " "), "resource": resource}
	if refreshPlain != "" {
		response["refresh_token"] = refreshPlain
	}
	action := "oauth_token_issued"
	if flow == "refresh_token" {
		action = "oauth_token_refreshed"
	}
	s.auditOAuthEvent(r, &userID, action, "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "offline_access": refreshPlain != "", "resource": resource, "scope": strings.Join(scopes, " ")})
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	outcome := "ignored"
	if err := r.ParseForm(); err == nil {
		if err := s.store.RevokeOAuthToken(r.Context(), security.HashAPISecret(s.sessionSecret, r.Form.Get("token"))); err == nil {
			outcome = "revoked"
		}
	}
	s.auditOAuthEvent(r, nil, "oauth_token_revoked", "oauth_token", "", map[string]any{"outcome": outcome})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) auditOAuthEvent(r *http.Request, actorID *int64, action, target, targetID string, detail map[string]any) {
	detail["target_id"] = boundedOAuthAuditValue(targetID)
	encoded, err := json.Marshal(detail)
	if err != nil {
		return
	}
	_ = s.store.AddAudit(r.Context(), model.AuditLog{ActorID: actorID, Action: action, Target: target, Detail: string(encoded), IP: clientIP(r)})
}

func oauthAuditActor(user *model.User) *int64 {
	if user == nil {
		return nil
	}
	return &user.ID
}

func boundedOAuthAuditValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

// authenticateOAuthToken is the legacy REST path resolver for oba_ tokens. MCP
// uses the dedicated mcpAuth middleware instead; this remains only for the
// generic apiAuth compatibility branch and older tests.
func (s *Server) authenticateOAuthToken(r *http.Request, raw string) (*model.APIPrincipal, error) {
	principal, token, grant, err := s.store.AuthenticateOAuthAccessToken(r.Context(), security.HashAPISecret(s.sessionSecret, raw), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	base, err := s.publicBaseURL(r.Context())
	if err != nil || token.Resource != base+"/mcp" {
		return nil, sql.ErrNoRows
	}
	client, err := s.store.GetOAuthClient(r.Context(), token.ClientID)
	if err != nil || !client.Enabled {
		return nil, sql.ErrNoRows
	}
	user, err := s.store.GetUser(r.Context(), token.UserID)
	if err != nil || s.validateOAuthUserGrant(r.Context(), user, mcpauth.ParseAccessLevel(grant.AccessLevel).NormalizedScopes(grant.OfflineAccess)) != nil {
		return nil, sql.ErrNoRows
	}
	principal.Scopes = slices.Clone(grant.Scopes)
	principal.ResourceFilter = append(json.RawMessage(nil), grant.ResourceFilter...)
	principal.OAuthGrantID = grant.ID
	return principal, nil
}

func (s *Server) validateOAuthUserGrant(ctx context.Context, user *model.User, scopes []string) error {
	if user == nil || user.Status != "active" {
		return errors.New("active user is required")
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		return err
	}
	requested, offline, err := normalizeRequestedScopes(scopes)
	if err != nil {
		return err
	}
	_ = offline
	if requested == mcpauth.AccessOperate && authorization.RoleRank(role) < 2 {
		return errors.New("the current role does not permit operate access")
	}
	if authorization.RoleRank(role) < 1 {
		return errors.New("the current role does not permit MCP access")
	}
	return nil
}

// allCapabilityScopes lists the legacy fine-grained scopes for Service Account
// scope validation. MCP never uses these scopes.
func (s *Server) allCapabilityScopes() []string {
	seen := map[string]bool{"offline_access": true}
	for _, descriptor := range s.capabilities.List(application.Principal{Scopes: []string{"*"}}) {
		for _, scope := range descriptor.RequiredScopes {
			seen[scope] = true
		}
	}
	out := make([]string, 0, len(seen))
	for scope := range seen {
		out = append(out, scope)
	}
	slices.Sort(out)
	return out
}

func pkceMatches(challenge, verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return challenge == base64.RawURLEncoding.EncodeToString(sum[:])
}

func oauthRedirect(w http.ResponseWriter, r *http.Request, redirectURI, state, code, oauthErr string) {
	http.Redirect(w, r, oauthRedirectLocation(redirectURI, state, code, oauthErr), http.StatusFound)
}

func oauthRedirectLocation(redirectURI, state, code, oauthErr string) string {
	u, _ := url.Parse(redirectURI)
	query := u.Query()
	if state != "" {
		query.Set("state", state)
	}
	if oauthErr != "" {
		query.Set("error", oauthErr)
	} else {
		query.Set("code", code)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
