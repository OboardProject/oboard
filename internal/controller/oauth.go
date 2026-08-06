package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
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
	mux.HandleFunc("/oauth/register", s.oauthDynamicRegister)
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
	writeJSON(w, http.StatusOK, map[string]any{"issuer": base, "authorization_endpoint": base + "/oauth/authorize", "token_endpoint": base + "/oauth/token", "revocation_endpoint": base + "/oauth/revoke", "registration_endpoint": base + "/oauth/register", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": s.allCapabilityScopes(), "resource": base + "/mcp"})
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
	writeJSON(w, http.StatusOK, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "bearer_methods_supported": []string{"header"}, "scopes_supported": s.allCapabilityScopes()})
}

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	var input struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		Scope        string   `json:"scope"`
	}
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.newOAuthClient(input.ClientName, input.RedirectURIs, strings.Fields(input.Scope))
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	if err := s.store.CreateOAuthClient(r.Context(), item); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s.auditOAuthEvent(r, oauthAuditActor(currentUser(r)), "oauth_client_created", "oauth_client", item.ID, map[string]any{"client_name": boundedOAuthAuditValue(item.Name), "redirect_uris": item.RedirectURIs, "scopes": item.AllowedScopes})
	writeJSON(w, http.StatusCreated, map[string]any{"client_id": item.ID, "client_name": item.Name, "redirect_uris": item.RedirectURIs, "scope": strings.Join(item.AllowedScopes, " "), "token_endpoint_auth_method": "none"})
}

type oauthDynamicRegistrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	ClientName              string   `json:"client_name"`
	Scope                   string   `json:"scope"`
}

func (s *Server) oauthDynamicRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	allowed, err := s.store.AllowRate(r.Context(), security.HashSecret("oauth-register:"+clientIP(r)), 10, time.Hour, 10_000)
	if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "registration rate limit is unavailable")
		return
	}
	if !allowed {
		oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "registration rate limit exceeded")
		return
	}
	var input oauthDynamicRegistrationRequest
	if !decodeOAuthRegistration(w, r, &input) {
		return
	}
	if input.TokenEndpointAuthMethod != "" && input.TokenEndpointAuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "token_endpoint_auth_method must be none")
		return
	}
	if !oauthRegistrationValuesAllowed(input.GrantTypes, "authorization_code", "refresh_token") || !oauthRegistrationValuesAllowed(input.ResponseTypes, "code") {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported OAuth grant or response type")
		return
	}
	scopes := strings.Fields(input.Scope)
	if len(scopes) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "scope is required for dynamic registration")
		return
	}
	if strings.TrimSpace(input.ClientName) == "" {
		input.ClientName = "Remote MCP Client"
	}
	for _, scope := range scopes {
		if !oauthDynamicRegistrationScopeAllowed(scope) {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "dynamic registration is limited to read and planning scopes")
			return
		}
	}
	item, err := s.newOAuthClient(input.ClientName, input.RedirectURIs, scopes)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	item.ClientMetadata = json.RawMessage(`{"registration":"dynamic"}`)
	if err := s.store.CreateOAuthClient(r.Context(), item); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s.auditOAuthEvent(r, nil, "oauth_client_registered", "oauth_client", item.ID, map[string]any{"client_name": boundedOAuthAuditValue(item.Name), "redirect_uris": item.RedirectURIs, "scopes": item.AllowedScopes})
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  item.ID,
		"client_id_issued_at":        item.CreatedAt.Unix(),
		"client_name":                item.Name,
		"redirect_uris":              item.RedirectURIs,
		"scope":                      strings.Join(item.AllowedScopes, " "),
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

func oauthDynamicRegistrationScopeAllowed(scope string) bool {
	if scope == "offline_access" {
		return true
	}
	return strings.HasSuffix(scope, ":read") || strings.HasSuffix(scope, ":plan") || scope == "deployments:validate"
}

func decodeOAuthRegistration(w http.ResponseWriter, r *http.Request, output any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(output); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid client metadata")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid client metadata")
		return false
	}
	return true
}

func oauthRegistrationValuesAllowed(values []string, allowed ...string) bool {
	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return false
		}
	}
	return true
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

func (s *Server) newOAuthClient(name string, redirects, scopes []string) (*model.OAuthClient, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 || len(redirects) == 0 || len(redirects) > 16 {
		return nil, errors.New("client_name and redirect_uris are required")
	}
	for _, raw := range redirects {
		u, err := url.Parse(raw)
		if err != nil || u.Fragment != "" || u.User != nil || u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) || u.Host == "" {
			return nil, errors.New("redirect URIs must use HTTPS or an HTTP loopback address")
		}
	}
	known := s.allCapabilityScopes()
	if len(scopes) == 0 {
		return nil, errors.New("at least one scope is required")
	}
	for _, scope := range scopes {
		if !slices.Contains(known, scope) {
			return nil, errors.New("unknown scope: " + scope)
		}
	}
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	random, err := security.RandomToken(18)
	if err != nil {
		return nil, err
	}
	return &model.OAuthClient{ID: "oc_" + random, Name: name, RedirectURIs: slices.Compact(redirects), AllowedScopes: slices.Compact(scopes), ClientMetadata: json.RawMessage(`{}`), Enabled: true}, nil
}

func (s *Server) writeMCPAuthenticationRequired(w http.ResponseWriter, r *http.Request, invalidToken bool) {
	base, err := s.publicBaseURL(r.Context())
	if err != nil {
		v2Error(w, r, http.StatusServiceUnavailable, "oauth_metadata_unavailable", err.Error())
		return
	}
	challenge := "Bearer resource_metadata=" + strconv.Quote(oauthProtectedResourceMetadataURL(base)) + `, scope="inventory:read"`
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
	if r.Method == http.MethodGet {
		if err := s.validateOAuthUserGrant(r.Context(), currentUser(r), request.Scope); err != nil {
			s.auditOAuthEvent(r, oauthAuditActor(currentUser(r)), "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "scope_denied", "scopes": request.Scope})
			oauthError(w, http.StatusForbidden, "access_denied", err.Error())
			return
		}
		s.renderOAuthConsent(w, r, request, client.Name)
		return
	}
	if r.Form.Get("decision") != "approve" {
		s.auditOAuthEvent(r, oauthAuditActor(currentUser(r)), "oauth_authorization_denied", "oauth_client", client.ID, map[string]any{"reason": "user_denied", "scopes": request.Scope})
		oauthRedirect(w, r, request.RedirectURI, request.State, "", "access_denied")
		return
	}
	user := currentUser(r)
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
	grant, principal, err := s.createOAuthGrant(r, *user, *client, request.Scope)
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
	code := &model.OAuthAuthorizationCode{CodeHash: security.HashAPISecret(s.sessionSecret, rawCode), GrantID: grant.ID, ClientID: client.ID, UserID: user.ID, PrincipalID: principal.ID, RedirectURI: request.RedirectURI, Scopes: request.Scope, Resource: request.Resource, CodeChallenge: request.CodeChallenge, ExpiresAt: time.Now().UTC().Add(oauthAuthorizationCodeTTL)}
	if err := s.store.CreateOAuthAuthorizationCode(r.Context(), code); err != nil {
		s.auditOAuthEvent(r, &user.ID, "oauth_authorization_denied", "oauth_grant", grant.ID, map[string]any{"client_id": client.ID, "reason": "code_issue_failed"})
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s.auditOAuthEvent(r, &user.ID, "oauth_authorization_granted", "oauth_grant", grant.ID, map[string]any{"client_id": client.ID, "scopes": request.Scope, "resource": request.Resource})
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
		if !slices.Contains(client.AllowedScopes, scope) {
			return request, nil, errors.New("scope is not registered for this client")
		}
	}
	return request, client, nil
}

func (s *Server) createOAuthGrant(r *http.Request, user model.User, client model.OAuthClient, scopes []string) (*model.OAuthGrant, *model.APIPrincipal, error) {
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
	filter, policyFilter, allowCreate, err := s.oauthGrantResourceFilter(r, scopes)
	if err != nil {
		return nil, nil, err
	}
	autoRisk, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("auto_approve_risk")))
	if strings.TrimSpace(r.Form.Get("auto_approve_risk")) == "" {
		autoRisk, err = 0, nil
	}
	if err != nil || autoRisk < 0 || autoRisk > 3 {
		return nil, nil, errors.New("auto_approve_risk must be between 0 and 3")
	}
	principal := &model.APIPrincipal{ID: "oauth_" + principalToken, OwnerUserID: &user.ID, Name: client.Name + " / " + user.Username, Type: model.APIPrincipalOAuth, Enabled: true, Scopes: slices.Clone(scopes), ResourceFilter: filter, RateLimitPerMinute: 120, MaxConcurrency: 4}
	profile := &model.OAuthApprovalProfile{ID: "oap_" + profileToken, Name: "OAuth grant approval", AutoApproveRisk: autoRisk}
	grant := &model.OAuthGrant{ID: "grt_" + grantToken, ClientID: client.ID, ClientName: client.Name, UserID: user.ID, Username: user.Username, PrincipalID: principal.ID, Scopes: slices.Clone(scopes), ResourceFilter: filter, ApprovalProfileID: profile.ID, OfflineAccess: slices.Contains(scopes, "offline_access"), ConsentVersion: 1}
	policies := []model.ApprovalPolicy{}
	for _, descriptor := range s.capabilities.List(application.Principal{Scopes: scopes}) {
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
		policies = append(policies, model.ApprovalPolicy{ID: "pol_" + policyToken, PrincipalID: principal.ID, Capability: descriptor.Name, ResourceFilter: policyFilter, Mode: mode})
	}
	if err := s.store.CreateOAuthGrant(r.Context(), grant, principal, profile, policies); err != nil {
		return nil, nil, err
	}
	return grant, principal, nil
}

func (s *Server) oauthGrantResourceFilter(r *http.Request, scopes []string) (json.RawMessage, json.RawMessage, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(r.Form.Get("server_mode")))
	if mode == "" {
		mode = "all"
	}
	if !slices.Contains([]string{"all", "selected", "none"}, mode) {
		return nil, nil, false, errors.New("invalid server resource mode")
	}
	selected := []int64{}
	seen := map[int64]bool{}
	for _, raw := range r.Form["server_id"] {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			return nil, nil, false, errors.New("invalid server resource id")
		}
		if !seen[id] {
			seen[id] = true
			selected = append(selected, id)
		}
	}
	if mode == "selected" && len(selected) == 0 {
		return nil, nil, false, errors.New("selected server mode requires at least one server")
	}
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		return nil, nil, false, err
	}
	existing := map[int64]bool{}
	for _, server := range servers {
		existing[server.ID] = true
	}
	for _, id := range selected {
		if !existing[id] {
			return nil, nil, false, errors.New("selected server is not available")
		}
	}
	slices.Sort(selected)
	allowCreate := r.Form.Get("allow_create_servers") == "1" && slices.Contains(scopes, "servers:onboard")
	userMode := "none"
	for _, scope := range scopes {
		if strings.HasPrefix(scope, "users:") || strings.HasPrefix(scope, "subscriptions:") || strings.HasPrefix(scope, "audit:") {
			userMode = "all"
			break
		}
	}
	filter := map[string]any{
		"servers":                map[string]any{"mode": mode, "ids": selected, "allow_create": allowCreate},
		"users":                  map[string]any{"mode": userMode},
		"proxy_paths":            map[string]any{"mode": "all"},
		"settings":               map[string]any{"allowed_sections": []string{}},
		"destructive_operations": false,
	}
	encoded, err := json.Marshal(filter)
	if err != nil {
		return nil, nil, false, err
	}
	policyFilter := json.RawMessage(`{}`)
	if mode == "selected" {
		policyFilter, err = json.Marshal(map[string]any{"server_ids": selected})
		if err != nil {
			return nil, nil, false, err
		}
	} else if mode == "none" {
		policyFilter = json.RawMessage(`{"server_ids":[]}`)
	}
	return json.RawMessage(encoded), policyFilter, allowCreate, nil
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
	s.issueOAuthTokens(w, r, "authorization_code", code.GrantID, code.PrincipalID, code.ClientID, code.UserID, code.Scopes, code.Resource, "", "")
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
	s.issueOAuthTokens(w, r, "refresh_token", refresh.GrantID, refresh.PrincipalID, refresh.ClientID, refresh.UserID, refresh.Scopes, refresh.Resource, refresh.FamilyID, refresh.TokenHash)
}

func (s *Server) issueOAuthTokens(w http.ResponseWriter, r *http.Request, flow, grantID, principalID, clientID string, userID int64, scopes []string, resource, familyID, parentTokenHash string) {
	grant, err := s.store.GetOAuthGrant(r.Context(), grantID)
	if err != nil || grant.RevokedAt != nil || grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now().UTC()) || grant.PrincipalID != principalID || grant.ClientID != clientID || grant.UserID != userID || !slices.Equal(grant.Scopes, scopes) {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "inactive_grant"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "OAuth grant is no longer active")
		return
	}
	client, err := s.store.GetOAuthClient(r.Context(), clientID)
	if err != nil || !client.Enabled || !scopesAllowedByOAuthClient(scopes, client.AllowedScopes) {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "disabled_client"})
		oauthError(w, http.StatusBadRequest, "invalid_grant", "OAuth client is disabled")
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil || s.validateOAuthUserGrant(r.Context(), user, scopes) != nil {
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
	access := &model.OAuthToken{TokenHash: security.HashAPISecret(s.sessionSecret, accessPlain), GrantID: grantID, PrincipalID: principalID, ClientID: clientID, UserID: userID, Scopes: scopes, Resource: resource, ExpiresAt: now.Add(oauthAccessTokenTTL)}
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
		refresh = &model.OAuthToken{TokenHash: security.HashAPISecret(s.sessionSecret, refreshPlain), FamilyID: familyID, GrantID: grantID, ParentTokenHash: parentTokenHash, PrincipalID: principalID, ClientID: clientID, UserID: userID, Scopes: scopes, Resource: resource, ExpiresAt: now.Add(oauthRefreshTokenTTL)}
	}
	if err := s.store.CreateOAuthTokens(r.Context(), access, refresh); err != nil {
		s.auditOAuthEvent(r, &userID, "oauth_token_denied", "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "reason": "token_issue_failed"})
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	response := map[string]any{"access_token": accessPlain, "token_type": "Bearer", "expires_in": int(oauthAccessTokenTTL.Seconds()), "scope": strings.Join(scopes, " "), "resource": resource}
	if refreshPlain != "" {
		response["refresh_token"] = refreshPlain
	}
	action := "oauth_token_issued"
	if flow == "refresh_token" {
		action = "oauth_token_refreshed"
	}
	s.auditOAuthEvent(r, &userID, action, "oauth_grant", grantID, map[string]any{"client_id": clientID, "flow": flow, "offline_access": refreshPlain != "", "resource": resource, "scopes": scopes})
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

func (s *Server) authenticateOAuthToken(r *http.Request, raw string) (*model.APIPrincipal, error) {
	principal, token, grant, err := s.store.AuthenticateOAuthAccessToken(r.Context(), security.HashAPISecret(s.sessionSecret, raw), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	base, err := s.publicBaseURL(r.Context())
	if err != nil || token.Resource != base+"/mcp" || r.URL.Path != "/mcp" {
		return nil, sql.ErrNoRows
	}
	client, err := s.store.GetOAuthClient(r.Context(), token.ClientID)
	if err != nil || !client.Enabled || !scopesAllowedByOAuthClient(grant.Scopes, client.AllowedScopes) {
		return nil, sql.ErrNoRows
	}
	user, err := s.store.GetUser(r.Context(), token.UserID)
	if err != nil || s.validateOAuthUserGrant(r.Context(), user, grant.Scopes) != nil {
		return nil, sql.ErrNoRows
	}
	principal.Scopes = slices.Clone(grant.Scopes)
	principal.ResourceFilter = append(json.RawMessage(nil), grant.ResourceFilter...)
	principal.OAuthGrantID = grant.ID
	return principal, nil
}

func scopesAllowedByOAuthClient(scopes, allowed []string) bool {
	for _, scope := range scopes {
		if !slices.Contains(allowed, scope) {
			return false
		}
	}
	return true
}

func (s *Server) validateOAuthUserGrant(ctx context.Context, user *model.User, scopes []string) error {
	if user == nil || user.Status != "active" {
		return errors.New("active user is required")
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	if err != nil {
		return err
	}
	allowed := application.HumanPrincipal(*user, role, netip.Addr{})
	for _, scope := range scopes {
		if scope == "offline_access" {
			continue
		}
		if !allowed.HasScope(scope) {
			return errors.New("requested scope exceeds the current user's role")
		}
	}
	return nil
}

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

type oauthConsentScope struct {
	Label string
	Code  string
}

type oauthConsentServer struct {
	ID   int64
	Name string
}

func (s *Server) oauthScopeViews(scopes []string) []oauthConsentScope {
	byScope := make(map[string][]string, len(scopes))
	for _, descriptor := range s.capabilities.List(application.Principal{Scopes: []string{"*"}}) {
		for _, scope := range descriptor.RequiredScopes {
			if !slices.Contains(scopes, scope) || descriptor.Description == "" || slices.Contains(byScope[scope], descriptor.Description) {
				continue
			}
			byScope[scope] = append(byScope[scope], descriptor.Description)
		}
	}
	views := make([]oauthConsentScope, 0, len(scopes))
	for _, scope := range scopes {
		label := strings.Join(byScope[scope], "；")
		code := ""
		if scope == "offline_access" {
			label, code = "允许客户端在你离线时轮换短期访问令牌", scope
		}
		if label == "" {
			label = scope
		} else {
			code = scope
		}
		views = append(views, oauthConsentScope{Label: label, Code: code})
	}
	return views
}

func (s *Server) renderOAuthConsent(w http.ResponseWriter, r *http.Request, request oauthAuthorizationRequest, clientName string) {
	const page = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>OAuth 授权 · OBoard</title>
<style>
:root{color-scheme:light;--bg-page:#f7f8fa;--bg-card:#ffffff;--bg-inset:#f3f4f6;--text-primary:#111827;--text-secondary:#4b5563;--text-muted:#6b7280;--border:#eaecef;--border-strong:#e2e5ea;--primary:#111827;--primary-hover:#0b1220;--primary-contrast:#ffffff;--primary-soft:rgba(17,24,39,.08);--success:#10b981;--success-bg:rgba(16,185,129,.1);--danger:#ef4444;--danger-bg:rgba(239,68,68,.1);--shadow:0 1px 3px rgba(0,0,0,.05),0 12px 32px rgba(15,23,42,.03)}
@media (prefers-color-scheme:dark){:root{color-scheme:dark;--bg-page:#0b0d12;--bg-card:#12151c;--bg-inset:#1a1f2b;--text-primary:#f3f4f6;--text-secondary:#c4cad4;--text-muted:#9aa3b2;--border:#2a3140;--border-strong:#3a4254;--primary:#f3f4f6;--primary-hover:#ffffff;--primary-contrast:#0b0d12;--primary-soft:rgba(243,244,246,.12);--success:#34d399;--success-bg:rgba(52,211,153,.14);--danger:#f87171;--danger-bg:rgba(248,113,113,.14);--shadow:0 0 0 1px rgba(255,255,255,.04) inset,0 20px 48px rgba(0,0,0,.3)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;background:var(--bg-page);color:var(--text-primary);font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;font-size:14px;-webkit-font-smoothing:antialiased;-moz-osx-font-smoothing:grayscale}
.card{width:min(560px,100%);background:var(--bg-card);border:1px solid var(--border-strong);border-radius:8px;box-shadow:var(--shadow);padding:32px}
.brand{display:flex;align-items:center;gap:10px;padding-bottom:18px;margin-bottom:20px;border-bottom:1px dashed var(--border)}
.brand-mark{flex-shrink:0;width:34px;height:34px;border-radius:9px}
.brand-name{font-weight:800;letter-spacing:0;font-size:15px}
.kicker{margin:0 0 8px;font-size:11.5px;font-weight:700;letter-spacing:0;text-transform:uppercase;color:var(--text-muted)}
h1{margin:0 0 10px;font-size:21px;font-weight:700;letter-spacing:0;line-height:1.35}
.sub{margin:0 0 22px;color:var(--text-secondary);font-size:13px;line-height:1.7}
.panel{border:1px solid var(--border);border-radius:8px;padding:2px 18px}
.row{padding:13px 0}
.row+.row{border-top:1px solid var(--border)}
.row-label{margin-bottom:8px;font-size:11.5px;font-weight:700;letter-spacing:0;color:var(--text-muted)}
.resource{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;font-size:12.5px;line-height:1.6;word-break:break-all;color:var(--text-secondary);background:var(--bg-inset);border:1px solid var(--border);border-radius:8px;padding:8px 10px}
.scope{display:flex;flex-direction:column;gap:4px;padding:5px 0}
.scope strong{font-size:13px;font-weight:600;line-height:1.5}
.scope code{width:fit-content;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;font-size:11px;color:var(--text-muted);background:var(--bg-inset);border:1px solid var(--border);border-radius:6px;padding:1px 7px}
.field{display:grid;gap:7px;margin-top:10px}.field>span{font-size:12px;color:var(--text-secondary)}
select{width:100%;min-height:40px;padding:8px 10px;border:1px solid var(--border-strong);border-radius:6px;background:var(--bg-inset);color:var(--text-primary);font:inherit}
select:focus-visible,input:focus-visible{outline:2px solid var(--primary);outline-offset:2px}
.check{display:flex;align-items:flex-start;gap:9px;padding:7px 0;color:var(--text-secondary);line-height:1.5}.check input{width:17px;height:17px;margin:2px 0 0;flex:0 0 auto}
.server-list{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:2px 14px;max-height:132px;overflow:auto;padding:6px 0}
.security-note{margin:9px 0 0;color:var(--text-muted);font-size:11.5px;line-height:1.55}
.actions{display:flex;gap:10px;justify-content:flex-end;margin-top:26px}
button{display:inline-flex;align-items:center;justify-content:center;gap:8px;min-height:40px;padding:8px 22px;border-radius:6px;border:1px solid transparent;font:inherit;font-size:13.5px;font-weight:600;letter-spacing:0;cursor:pointer;transition:background .18s ease,border-color .18s ease,transform .14s cubic-bezier(.22,1,.36,1)}
button:active{transform:scale(.98)}
button:focus-visible{outline:2px solid var(--primary);outline-offset:2px}
button.primary{background:var(--primary);color:var(--primary-contrast)}
button.primary:hover{background:var(--primary-hover)}
button.ghost{background:transparent;color:var(--text-primary);border-color:var(--border-strong)}
button.ghost:hover{background:var(--primary-soft)}
.foot{margin-top:20px;text-align:center;color:var(--text-muted);font-size:11.5px;line-height:1.7}
@media(max-width:520px){body{padding:12px}.card{padding:22px 18px}.server-list{grid-template-columns:1fr}}
</style>
</head>
<body>
<form class="card" method="post">
  <div class="brand">
    <svg class="brand-mark" viewBox="0 0 512 512" aria-hidden="true"><rect width="512" height="512" rx="116" fill="var(--primary)"/><circle cx="256" cy="256" r="128" fill="none" stroke="var(--primary-contrast)" stroke-width="30"/><circle cx="256" cy="256" r="38" fill="var(--primary-contrast)"/></svg>
    <span class="brand-name">OBOARD</span>
  </div>
  <p class="kicker">OAuth 授权</p>
  <h1>「{{.ClientName}}」请求访问</h1>
  <p class="sub">该客户端申请以你的账号访问 OBoard MCP 资源，后续操作仍受你的角色与审批策略约束。</p>
  <div class="panel">
    <div class="row">
      <div class="row-label">目标资源</div>
      <div class="resource">{{.Resource}}</div>
    </div>
    <div class="row">
      <div class="row-label">申请的权限</div>
      {{range .Scopes}}<div class="scope"><strong>{{.Label}}</strong>{{if .Code}}<code>{{.Code}}</code>{{end}}</div>{{end}}
    </div>
    {{if .HasServerScope}}<div class="row">
      <div class="row-label">服务器范围</div>
      <label class="field"><span>允许访问的服务器</span><select name="server_mode"><option value="all">所有现有服务器</option><option value="selected">仅所选服务器</option><option value="none">不允许访问服务器</option></select></label>
      {{if .Servers}}<div class="server-list">{{range .Servers}}<label class="check"><input type="checkbox" name="server_id" value="{{.ID}}"><span>{{.Name}} · #{{.ID}}</span></label>{{end}}</div>{{end}}
      {{if .CanCreateServer}}<label class="check"><input type="checkbox" name="allow_create_servers" value="1"><span>允许创建新服务器并签发一次性接入动作</span></label>{{end}}
    </div>{{end}}
    <div class="row">
      <div class="row-label">自动审批</div>
      <label class="field"><span>本 Grant 可自动批准的最高风险</span><select name="auto_approve_risk"><option value="0">不自动批准写操作</option><option value="2">Risk 2 · 草稿和普通变更</option><option value="3">Risk 3 · 包含受限范围内的部署</option></select></label>
      <p class="security-note">Risk 4、删除和关键设置始终需要管理员交互式批准，客户端参数不能绕过。</p>
    </div>
  </div>
  {{range .Hidden}}<input type="hidden" name="{{.Key}}" value="{{.Value}}">{{end}}
  <div class="actions">
    <button class="ghost" type="submit" name="decision" value="deny">拒绝</button>
    <button class="primary" type="submit" name="decision" value="approve">允许授权</button>
  </div>
  <div class="foot">当前账号：{{.Username}} · 授权后可随时在 OBoard 面板吊销</div>
</form>
</body>
</html>`
	tmpl := template.Must(template.New("consent").Parse(page))
	hidden := []map[string]string{{"Key": "client_id", "Value": request.ClientID}, {"Key": "redirect_uri", "Value": request.RedirectURI}, {"Key": "response_type", "Value": "code"}, {"Key": "scope", "Value": strings.Join(request.Scope, " ")}, {"Key": "state", "Value": request.State}, {"Key": "resource", "Value": request.Resource}, {"Key": "code_challenge", "Value": request.CodeChallenge}, {"Key": "code_challenge_method", "Value": "S256"}}
	if sessionToken := currentSessionToken(r); sessionToken != "" {
		hidden = append(hidden, map[string]string{"Key": "_oboard_csrf", "Value": s.csrfTokenForSession(sessionToken)})
	}
	username := ""
	if user := currentUser(r); user != nil {
		username = user.Username
	}
	servers := []oauthConsentServer{}
	if items, err := s.store.ListServers(r.Context()); err == nil {
		for _, item := range items {
			servers = append(servers, oauthConsentServer{ID: item.ID, Name: item.Name})
		}
	}
	hasServerScope := false
	for _, scope := range request.Scope {
		if strings.HasPrefix(scope, "servers:") || strings.HasPrefix(scope, "topology:") || strings.HasPrefix(scope, "deployments:") {
			hasServerScope = true
			break
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = tmpl.Execute(w, map[string]any{"ClientName": clientName, "Resource": request.Resource, "Scopes": s.oauthScopeViews(request.Scope), "Hidden": hidden, "Username": username, "Servers": servers, "HasServerScope": hasServerScope, "CanCreateServer": slices.Contains(request.Scope, "servers:onboard")})
}

func (s *Server) renderOAuthSuccess(w http.ResponseWriter, r *http.Request, redirectURI, state, code, clientName string) {
	location := oauthRedirectLocation(redirectURI, state, code, "")
	const page = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>授权成功 · OBoard</title>
{{.RefreshMeta}}
<style>
:root{color-scheme:light;--bg-page:#f7f8fa;--bg-card:#ffffff;--text-primary:#111827;--text-secondary:#4b5563;--text-muted:#6b7280;--border:#eaecef;--border-strong:#e2e5ea;--primary:#111827;--primary-hover:#0b1220;--primary-contrast:#ffffff;--primary-soft:rgba(17,24,39,.08);--success:#10b981;--success-bg:rgba(16,185,129,.1);--shadow:0 1px 3px rgba(0,0,0,.05),0 12px 32px rgba(15,23,42,.03)}
@media (prefers-color-scheme:dark){:root{color-scheme:dark;--bg-page:#0b0d12;--bg-card:#12151c;--text-primary:#f3f4f6;--text-secondary:#c4cad4;--text-muted:#9aa3b2;--border:#2a3140;--border-strong:#3a4254;--primary:#f3f4f6;--primary-hover:#ffffff;--primary-contrast:#0b0d12;--primary-soft:rgba(243,244,246,.12);--success:#34d399;--success-bg:rgba(52,211,153,.14);--shadow:0 0 0 1px rgba(255,255,255,.04) inset,0 20px 48px rgba(0,0,0,.3)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;background:var(--bg-page);color:var(--text-primary);font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;font-size:14px;-webkit-font-smoothing:antialiased;-moz-osx-font-smoothing:grayscale}
.card{width:min(472px,100%);background:var(--bg-card);border:1px solid var(--border-strong);border-radius:18px;box-shadow:var(--shadow);padding:32px;text-align:center}
.brand{display:flex;align-items:center;justify-content:center;gap:10px;padding-bottom:18px;margin-bottom:22px;border-bottom:1px dashed var(--border)}
.brand-mark{flex-shrink:0;width:34px;height:34px;border-radius:9px}
.brand-name{font-weight:800;letter-spacing:.16em;font-size:15px}
.check{width:64px;height:64px;margin:0 auto 20px;display:flex;align-items:center;justify-content:center;border-radius:50%;background:var(--success-bg);color:var(--success)}
.kicker{margin:0 0 8px;font-size:11.5px;font-weight:700;letter-spacing:.1em;text-transform:uppercase;color:var(--text-muted)}
h1{margin:0 0 10px;font-size:21px;font-weight:700;letter-spacing:-.01em;line-height:1.35}
.sub{margin:0 0 24px;color:var(--text-secondary);font-size:13px;line-height:1.7}
.fallback{color:var(--text-secondary);font-size:12.5px;text-decoration:none;border-bottom:1px dashed var(--border-strong);padding-bottom:2px}
.fallback:hover{color:var(--text-primary)}
</style>
</head>
<body>
<div class="card">
  <div class="brand">
    <svg class="brand-mark" viewBox="0 0 512 512" aria-hidden="true"><rect width="512" height="512" rx="116" fill="var(--primary)"/><circle cx="256" cy="256" r="128" fill="none" stroke="var(--primary-contrast)" stroke-width="30"/><circle cx="256" cy="256" r="38" fill="var(--primary-contrast)"/></svg>
    <span class="brand-name">OBOARD</span>
  </div>
  <div class="check"><svg viewBox="0 0 24 24" width="30" height="30" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></div>
  <p class="kicker">OAuth 授权</p>
  <h1>授权成功</h1>
  <p class="sub">已授权「{{.ClientName}}」访问 OBoard MCP 资源，正在返回客户端……</p>
  <a class="fallback" href="{{.RedirectURL}}">未自动跳转？点击这里返回客户端</a>
</div>
<script>window.setTimeout(function(){window.location.replace({{.RedirectJS}})},1200)</script>
</body>
</html>`
	tmpl := template.Must(template.New("success").Parse(page))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = tmpl.Execute(w, map[string]any{
		"ClientName":  clientName,
		"RedirectURL": template.URL(location),
		"RedirectJS":  oauthJSString(location),
		"RefreshMeta": template.HTML(`<meta http-equiv="refresh" content="1.2; url=` + template.HTMLEscapeString(location) + `">`),
	})
}

func oauthJSString(value string) template.JS {
	quoted := strconv.Quote(value)
	quoted = strings.ReplaceAll(quoted, "<", `\u003c`)
	quoted = strings.ReplaceAll(quoted, ">", `\u003e`)
	quoted = strings.ReplaceAll(quoted, "&", `\u0026`)
	return template.JS(quoted)
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
