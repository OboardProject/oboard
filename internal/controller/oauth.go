package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
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
		scopes = s.allCapabilityScopes()
	}
	if strings.TrimSpace(input.ClientName) == "" {
		input.ClientName = "Remote MCP Client"
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
	challenge := "Bearer resource_metadata=" + strconv.Quote(oauthProtectedResourceMetadataURL(base))
	code, message := "unauthorized", "需要 OAuth 登录或 Service Account Token"
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
		oauthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if r.Method == http.MethodGet {
		if err := s.validateOAuthUserGrant(r.Context(), currentUser(r), request.Scope); err != nil {
			oauthError(w, http.StatusForbidden, "access_denied", err.Error())
			return
		}
		s.renderOAuthConsent(w, r, request, client.Name)
		return
	}
	if r.Form.Get("decision") != "approve" {
		oauthRedirect(w, r, request.RedirectURI, request.State, "", "access_denied")
		return
	}
	user := currentUser(r)
	if user == nil {
		oauthError(w, http.StatusUnauthorized, "access_denied", "login required")
		return
	}
	if err := s.validateOAuthUserGrant(r.Context(), user, request.Scope); err != nil {
		oauthError(w, http.StatusForbidden, "access_denied", err.Error())
		return
	}
	principal, err := s.oauthPrincipal(r, *user, *client, request.Scope)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	rawCode, err := security.RandomToken(32)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	code := &model.OAuthAuthorizationCode{CodeHash: security.HashAPISecret(s.sessionSecret, rawCode), ClientID: client.ID, UserID: user.ID, PrincipalID: principal.ID, RedirectURI: request.RedirectURI, Scopes: request.Scope, Resource: request.Resource, CodeChallenge: request.CodeChallenge, ExpiresAt: time.Now().UTC().Add(oauthAuthorizationCodeTTL)}
	if err := s.store.CreateOAuthAuthorizationCode(r.Context(), code); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	oauthRedirect(w, r, request.RedirectURI, request.State, rawCode, "")
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

func (s *Server) oauthPrincipal(r *http.Request, user model.User, client model.OAuthClient, scopes []string) (*model.APIPrincipal, error) {
	digest := sha256.Sum256([]byte(client.ID + "\x00" + strconv.FormatInt(user.ID, 10)))
	id := "oauth_" + hex.EncodeToString(digest[:12])
	if existing, err := s.store.GetAPIPrincipal(r.Context(), id); err == nil {
		existing.Scopes = slices.Clone(scopes)
		if err := s.store.UpdateAPIPrincipal(r.Context(), existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	principal := &model.APIPrincipal{ID: id, OwnerUserID: &user.ID, Name: client.Name + " / " + user.Username, Type: model.APIPrincipalOAuth, Enabled: true, Scopes: slices.Clone(scopes), ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 120, MaxConcurrency: 4}
	if err := s.store.CreateAPIPrincipal(r.Context(), principal); err != nil {
		return nil, err
	}
	return principal, nil
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
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid")
		return
	}
	s.issueOAuthTokens(w, r, code.PrincipalID, code.ClientID, code.UserID, code.Scopes, code.Resource, "")
}

func (s *Server) oauthExchangeRefresh(w http.ResponseWriter, r *http.Request) {
	refresh, err := s.store.ConsumeOAuthRefreshToken(r.Context(), security.HashAPISecret(s.sessionSecret, r.Form.Get("refresh_token")), time.Now().UTC())
	if errors.Is(err, store.ErrOAuthRefreshReuse) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected; token family revoked")
		return
	}
	if err != nil || refresh.ClientID != r.Form.Get("client_id") || refresh.Resource != r.Form.Get("resource") {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid")
		return
	}
	s.issueOAuthTokens(w, r, refresh.PrincipalID, refresh.ClientID, refresh.UserID, refresh.Scopes, refresh.Resource, refresh.FamilyID)
}

func (s *Server) issueOAuthTokens(w http.ResponseWriter, r *http.Request, principalID, clientID string, userID int64, scopes []string, resource, familyID string) {
	client, err := s.store.GetOAuthClient(r.Context(), clientID)
	if err != nil || !client.Enabled || !scopesAllowedByOAuthClient(scopes, client.AllowedScopes) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "OAuth client is disabled")
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil || s.validateOAuthUserGrant(r.Context(), user, scopes) != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "user is no longer authorized for the requested scopes")
		return
	}
	accessRaw, err := security.RandomToken(32)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	refreshRaw, err := security.RandomToken(32)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	if familyID == "" {
		familyID, _ = security.RandomToken(18)
	}
	now := time.Now().UTC()
	accessPlain, refreshPlain := "oba_"+accessRaw, "obr_"+refreshRaw
	access := &model.OAuthToken{TokenHash: security.HashAPISecret(s.sessionSecret, accessPlain), PrincipalID: principalID, ClientID: clientID, UserID: userID, Scopes: scopes, Resource: resource, ExpiresAt: now.Add(oauthAccessTokenTTL)}
	refresh := &model.OAuthToken{TokenHash: security.HashAPISecret(s.sessionSecret, refreshPlain), FamilyID: familyID, PrincipalID: principalID, ClientID: clientID, UserID: userID, Scopes: scopes, Resource: resource, ExpiresAt: now.Add(oauthRefreshTokenTTL)}
	if err := s.store.CreateOAuthTokens(r.Context(), access, refresh); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{"access_token": accessPlain, "token_type": "Bearer", "expires_in": int(oauthAccessTokenTTL.Seconds()), "refresh_token": refreshPlain, "scope": strings.Join(scopes, " "), "resource": resource})
}

func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if err := r.ParseForm(); err == nil {
		_ = s.store.RevokeOAuthToken(r.Context(), security.HashAPISecret(s.sessionSecret, r.Form.Get("token")))
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) authenticateOAuthToken(r *http.Request, raw string) (*model.APIPrincipal, error) {
	principal, token, err := s.store.AuthenticateOAuthAccessToken(r.Context(), security.HashAPISecret(s.sessionSecret, raw), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	base, err := s.publicBaseURL(r.Context())
	if err != nil || token.Resource != base+"/mcp" || r.URL.Path != "/mcp" {
		return nil, sql.ErrNoRows
	}
	client, err := s.store.GetOAuthClient(r.Context(), token.ClientID)
	if err != nil || !client.Enabled || !scopesAllowedByOAuthClient(token.Scopes, client.AllowedScopes) {
		return nil, sql.ErrNoRows
	}
	user, err := s.store.GetUser(r.Context(), token.UserID)
	if err != nil || s.validateOAuthUserGrant(r.Context(), user, token.Scopes) != nil {
		return nil, sql.ErrNoRows
	}
	principal.Scopes = slices.Clone(token.Scopes)
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
		if !allowed.HasScope(scope) {
			return errors.New("requested scope exceeds the current user's role")
		}
	}
	return nil
}

func (s *Server) allCapabilityScopes() []string {
	seen := map[string]bool{}
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
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) renderOAuthConsent(w http.ResponseWriter, r *http.Request, request oauthAuthorizationRequest, clientName string) {
	const page = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>OBoard 授权</title><style>body{font-family:system-ui,sans-serif;max-width:640px;margin:10vh auto;padding:24px;color:#17202a}fieldset{border:1px solid #ccd1d1;border-radius:6px;padding:20px}button{padding:9px 14px;margin-right:8px}code{overflow-wrap:anywhere}</style></head><body><form method="post"><fieldset><legend>授权 {{.ClientName}}</legend><p>请求访问 OBoard MCP 资源：</p><code>{{.Resource}}</code><p>权限：{{.Scopes}}</p>{{range .Hidden}}<input type="hidden" name="{{.Key}}" value="{{.Value}}">{{end}}<button name="decision" value="approve" type="submit">允许</button><button name="decision" value="deny" type="submit">拒绝</button></fieldset></form></body></html>`
	tmpl := template.Must(template.New("consent").Parse(page))
	hidden := []map[string]string{{"Key": "client_id", "Value": request.ClientID}, {"Key": "redirect_uri", "Value": request.RedirectURI}, {"Key": "response_type", "Value": "code"}, {"Key": "scope", "Value": strings.Join(request.Scope, " ")}, {"Key": "state", "Value": request.State}, {"Key": "resource", "Value": request.Resource}, {"Key": "code_challenge", "Value": request.CodeChallenge}, {"Key": "code_challenge_method", "Value": "S256"}}
	if sessionToken := currentSessionToken(r); sessionToken != "" {
		hidden = append(hidden, map[string]string{"Key": "_oboard_csrf", "Value": s.csrfTokenForSession(sessionToken)})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = tmpl.Execute(w, map[string]any{"ClientName": clientName, "Resource": request.Resource, "Scopes": strings.Join(request.Scope, " "), "Hidden": hidden})
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
