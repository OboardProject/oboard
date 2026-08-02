package controller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type apiPrincipalContextKey struct{}

func (s *Server) registerAPIV2Routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/openapi.json", s.apiAuth(s.apiV2OpenAPI, model.RoleViewer))
	mux.HandleFunc("/api/v2/capabilities", s.apiAuth(s.apiV2Capabilities, model.RoleViewer))
	mux.HandleFunc("/api/v2/query", s.apiAuth(s.apiV2Query, model.RoleViewer))
	mux.HandleFunc("/api/v2/servers", s.apiAuth(s.apiV2Servers, model.RoleViewer))
	mux.HandleFunc("/api/v2/servers/", s.apiAuth(s.apiV2Server, model.RoleViewer))
	mux.HandleFunc("/api/v2/users", s.apiAuth(s.apiV2Users, model.RoleViewer))
	mux.HandleFunc("/api/v2/topology", s.apiAuth(s.apiV2Topology, model.RoleViewer))
	mux.HandleFunc("/api/v2/audit/incidents", s.apiAuth(s.apiV2AuditIncidents, model.RoleViewer))
	mux.HandleFunc("/api/v2/audit/incidents/", s.apiAuth(s.apiV2AuditIncident, model.RoleViewer))
	mux.HandleFunc("/api/v2/changesets", s.apiAuth(s.apiV2Changesets, model.RoleViewer))
	mux.HandleFunc("/api/v2/changesets/", s.apiAuth(s.apiV2Changeset, model.RoleViewer))
	mux.HandleFunc("/api/v2/api-principals", s.auth(s.apiPrincipals, model.RoleAdmin))
	mux.HandleFunc("/api/v2/api-principals/", s.auth(s.apiPrincipalSubroutes, model.RoleAdmin))
	mux.HandleFunc("/api/v2/ai/providers", s.auth(s.apiV2AIProviders, model.RoleAdmin))
	mux.HandleFunc("/api/v2/ai/providers/", s.auth(s.apiV2AIProvider, model.RoleAdmin))
	mux.HandleFunc("/api/v2/ai/provider-models", s.auth(s.apiV2AIProviderModels, model.RoleAdmin))
	mux.HandleFunc("/api/v2/approval-policies", s.auth(s.apiV2ApprovalPolicies, model.RoleAdmin))
	mux.HandleFunc("/api/v2/approval-policies/", s.auth(s.apiV2ApprovalPolicy, model.RoleAdmin))
	mux.HandleFunc("/api/v2/tool-audits", s.auth(s.apiV2ToolAudits, model.RoleAdmin))
}

func (s *Server) apiV2AIProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListAIProviders(r.Context())
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
	case http.MethodPost:
		var request struct {
			Name            string `json:"name"`
			BaseURL         string `json:"base_url"`
			Model           string `json:"model"`
			APIKey          string `json:"api_key"`
			Enabled         bool   `json:"enabled"`
			AllowRawAudit   bool   `json:"allow_raw_audit"`
			DailyTokenLimit int64  `json:"daily_token_limit"`
		}
		if !decodeV2(w, r, &request) {
			return
		}
		request.Name, request.Model, request.APIKey = strings.TrimSpace(request.Name), strings.TrimSpace(request.Model), strings.TrimSpace(request.APIKey)
		baseURL, err := normalizeAIProviderBaseURL(request.BaseURL)
		request.BaseURL = baseURL
		if request.Name == "" || request.Model == "" || request.APIKey == "" || err != nil || request.DailyTokenLimit < 0 {
			v2Error(w, r, http.StatusBadRequest, "invalid_provider", "AI Provider 配置无效")
			return
		}
		random, err := security.RandomToken(18)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		id := "aip_" + random
		encrypted, err := security.EncryptSecret(s.sessionSecret, "ai-provider-credential:"+id, request.APIKey)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		item := &model.AIProvider{ID: id, Name: request.Name, BaseURL: request.BaseURL, Model: request.Model, CredentialEncrypted: encrypted, Enabled: request.Enabled, AllowRawAudit: request.AllowRawAudit, DailyTokenLimit: request.DailyTokenLimit}
		if err := s.store.CreateAIProvider(r.Context(), item); err != nil {
			v2HandleError(w, r, err)
			return
		}
		item.CredentialEncrypted = ""
		v2Write(w, r, http.StatusCreated, item, nil)
	default:
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func (s *Server) apiV2AuditIncidents(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if !principal.HasScope("audit:read") {
		v2Error(w, r, http.StatusForbidden, "scope_denied", "缺少 audit:read 权限")
		return
	}
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	items, err := s.application.ListAuditIncidents(r.Context(), principal, queryLimit(r, 50))
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
}

func (s *Server) apiV2AuditIncident(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v2/audit/incidents/")
	if len(parts) == 0 || parts[0] == "" {
		v2Error(w, r, http.StatusBadRequest, "invalid_id", "缺少 Incident ID")
		return
	}
	principal, _ := apiPrincipal(r)
	if len(parts) == 1 && r.Method == http.MethodGet {
		if !principal.HasScope("audit:read") {
			v2Error(w, r, http.StatusForbidden, "scope_denied", "缺少 audit:read 权限")
			return
		}
		item, err := s.application.GetAuditIncident(r.Context(), principal, parts[0])
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, item, nil)
		return
	}
	if len(parts) == 2 && parts[1] == "feedback" && r.Method == http.MethodPost {
		if !principal.Interactive || principal.UserID == nil {
			v2Error(w, r, http.StatusForbidden, "interactive_required", "反馈需要人工登录")
			return
		}
		if _, err := s.application.GetAuditIncident(r.Context(), principal, parts[0]); err != nil {
			v2HandleError(w, r, err)
			return
		}
		var request struct {
			Label   string `json:"label"`
			Comment string `json:"comment"`
		}
		if !decodeV2(w, r, &request) {
			return
		}
		allowed := []string{"confirmed_abuse", "confirmed_sharing", "legitimate_multi_device", "family_use", "travel", "shared_nat", "vpn_exit", "false_positive", "unknown"}
		if !slices.Contains(allowed, request.Label) {
			v2Error(w, r, http.StatusBadRequest, "invalid_feedback", "反馈标签无效")
			return
		}
		random, _ := security.RandomToken(18)
		feedback := &model.OperatorFeedback{ID: "fb_" + random, IncidentID: parts[0], ActorUserID: *principal.UserID, Label: request.Label, Comment: strings.TrimSpace(request.Comment)}
		if err := s.store.CreateOperatorFeedback(r.Context(), feedback); err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusCreated, feedback, nil)
		return
	}
	v2Error(w, r, http.StatusNotFound, "not_found", "Incident 操作不存在")
}

func (s *Server) apiV2OpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	v2Write(w, r, http.StatusOK, s.capabilities.OpenAPI(requestBasePath(r, s.currentBasePath())), nil)
}

func (s *Server) apiAuth(next http.HandlerFunc, minimumRole model.Role) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		if strings.HasPrefix(token, "obk_") {
			s.machineAPIAuth(next, w, r, token)
			return
		}
		if strings.HasPrefix(token, "oba_") {
			stored, err := s.authenticateOAuthToken(r, token)
			if err != nil {
				if r.URL.Path == "/mcp" {
					s.writeMCPAuthenticationRequired(w, r, true)
					return
				}
				v2Error(w, r, http.StatusUnauthorized, "invalid_token", "OAuth access token 无效或已过期")
				return
			}
			source, _ := netip.ParseAddr(clientIP(r))
			principal := application.Principal{ID: stored.ID, UserID: stored.OwnerUserID, Name: stored.Name, Type: stored.Type, Scopes: stored.Scopes, ResourceFilter: stored.ResourceFilter, SourceIP: source, ClientName: r.Header.Get("User-Agent")}
			s.machinePrincipalAuth(next, w, r, principal)
			return
		}
		if r.URL.Path == "/mcp" {
			s.writeMCPAuthenticationRequired(w, r, authorization != "")
			return
		}
		s.auth(func(w http.ResponseWriter, r *http.Request) {
			user := currentUser(r)
			if user == nil {
				v2Error(w, r, http.StatusUnauthorized, "unauthorized", "需要登录")
				return
			}
			ip, _ := netip.ParseAddr(clientIP(r))
			principal := application.HumanPrincipal(*user, currentRole(r), ip)
			next(w, r.WithContext(context.WithValue(r.Context(), apiPrincipalContextKey{}, principal)))
		}, minimumRole)(w, r)
	}
}

func (s *Server) machineAPIAuth(next http.HandlerFunc, w http.ResponseWriter, r *http.Request, token string) {
	now := time.Now().UTC()
	stored, _, err := s.store.AuthenticateAPIToken(r.Context(), security.HashAPISecret(s.sessionSecret, token), now)
	if err != nil {
		if r.URL.Path == "/mcp" {
			s.writeMCPAuthenticationRequired(w, r, true)
			return
		}
		v2Error(w, r, http.StatusUnauthorized, "invalid_token", "API token 无效或已过期")
		return
	}
	source, err := netip.ParseAddr(clientIP(r))
	if err != nil || !principalIPAllowed(stored.AllowedCIDRs, source) {
		v2Error(w, r, http.StatusForbidden, "source_ip_denied", "请求来源 IP 不在允许范围内")
		return
	}
	principal := application.Principal{ID: stored.ID, UserID: stored.OwnerUserID, Name: stored.Name, Type: stored.Type, Scopes: stored.Scopes, ResourceFilter: stored.ResourceFilter, SourceIP: source, ClientName: r.Header.Get("User-Agent")}
	s.machinePrincipalAuth(next, w, r, principal)
}

func (s *Server) machinePrincipalAuth(next http.HandlerFunc, w http.ResponseWriter, r *http.Request, principal application.Principal) {
	storedID := principal.ID
	limit := 60
	maximum := 4
	if stored, err := s.store.GetAPIPrincipal(r.Context(), storedID); err == nil {
		limit = stored.RateLimitPerMinute
		maximum = stored.MaxConcurrency
	}
	if limit <= 0 {
		limit = 60
	}
	allowed, err := s.store.AllowRate(r.Context(), security.HashSecret("api-principal:"+storedID), limit, time.Minute, 10_000)
	if err != nil {
		v2Error(w, r, http.StatusServiceUnavailable, "rate_limit_unavailable", "限速状态不可用")
		return
	}
	if !allowed {
		v2Error(w, r, http.StatusTooManyRequests, "rate_limit_exceeded", "API 调用频率超过限制")
		return
	}
	if !s.enterAPIGate(storedID, maximum) {
		v2Error(w, r, http.StatusTooManyRequests, "concurrency_limit_exceeded", "API 并发超过限制")
		return
	}
	defer s.leaveAPIGate(storedID)
	if !principal.SourceIP.IsValid() {
		principal.SourceIP, _ = netip.ParseAddr(clientIP(r))
	}
	if principal.ClientName == "" {
		principal.ClientName = r.Header.Get("User-Agent")
	}
	authenticated := r.WithContext(context.WithValue(r.Context(), apiPrincipalContextKey{}, principal))
	if r.URL.Path == "/mcp" {
		next(w, authenticated)
		return
	}
	recorder := &responseStatusWriter{ResponseWriter: w}
	next(recorder, authenticated)
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	result := "succeeded"
	if status >= http.StatusBadRequest {
		result = "failed"
	}
	classification := capability.DataInternal
	if strings.HasPrefix(r.URL.Path, "/api/v2/users") || strings.HasPrefix(r.URL.Path, "/api/v2/audit") {
		classification = capability.DataSensitive
	}
	s.recordToolCall(r.Context(), principal, "http."+strings.ToLower(r.Method)+":"+r.URL.Path, map[string]string{"query": r.URL.RawQuery}, result, classification)
}

func (s *Server) enterAPIGate(principalID string, maximum int) bool {
	if maximum <= 0 {
		maximum = 4
	}
	s.apiGateMu.Lock()
	defer s.apiGateMu.Unlock()
	if s.apiInFlight[principalID] >= maximum {
		return false
	}
	s.apiInFlight[principalID]++
	return true
}

func (s *Server) leaveAPIGate(principalID string) {
	s.apiGateMu.Lock()
	if s.apiInFlight[principalID] <= 1 {
		delete(s.apiInFlight, principalID)
	} else {
		s.apiInFlight[principalID]--
	}
	s.apiGateMu.Unlock()
}

func principalIPAllowed(cidrs []string, source netip.Addr) bool {
	if len(cidrs) == 0 {
		return true
	}
	for _, raw := range cidrs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err == nil && prefix.Contains(source.Unmap()) {
			return true
		}
	}
	return false
}

func apiPrincipal(r *http.Request) (application.Principal, bool) {
	principal, ok := r.Context().Value(apiPrincipalContextKey{}).(application.Principal)
	return principal, ok
}

func (s *Server) apiV2Capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	principal, _ := apiPrincipal(r)
	v2Write(w, r, http.StatusOK, s.capabilities.List(principal), nil)
}

func (s *Server) apiV2Query(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	var request struct {
		Capability string          `json:"capability"`
		Arguments  json.RawMessage `json:"arguments"`
	}
	if !decodeV2(w, r, &request) {
		return
	}
	principal, _ := apiPrincipal(r)
	descriptor, authorized := s.capabilities.Authorize(principal, request.Capability)
	if !authorized || !descriptor.ReadOnly {
		v2Error(w, r, http.StatusForbidden, "capability_denied", "当前身份无权调用该查询能力")
		return
	}
	result, err := s.application.Query(r.Context(), principal, request.Capability, request.Arguments)
	if err != nil {
		s.recordToolCall(r.Context(), principal, request.Capability, request.Arguments, "failed", descriptor.DataClassification)
		v2HandleError(w, r, err)
		return
	}
	s.recordToolCall(r.Context(), principal, request.Capability, request.Arguments, "succeeded", descriptor.DataClassification)
	v2Write(w, r, http.StatusOK, result, nil)
}

func (s *Server) apiV2Servers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "服务器变更必须通过 Changeset")
		return
	}
	principal, _ := apiPrincipal(r)
	if !principal.HasScope("servers:read") {
		v2Error(w, r, http.StatusForbidden, "scope_denied", "缺少 servers:read 权限")
		return
	}
	items, err := s.application.ListServers(r.Context(), principal)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
}

func (s *Server) apiV2Server(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "服务器变更必须通过 Changeset")
		return
	}
	id, err := application.ParseID(strings.TrimPrefix(r.URL.Path, "/api/v2/servers/"))
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	principal, _ := apiPrincipal(r)
	if !principal.HasScope("servers:read") {
		v2Error(w, r, http.StatusForbidden, "scope_denied", "缺少 servers:read 权限")
		return
	}
	item, err := s.application.GetServer(r.Context(), principal, id)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, item, nil)
}

func (s *Server) apiV2Users(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "用户变更必须通过 Changeset")
		return
	}
	principal, _ := apiPrincipal(r)
	if !principal.HasScope("users:read") {
		v2Error(w, r, http.StatusForbidden, "scope_denied", "缺少 users:read 权限")
		return
	}
	items, err := s.application.ListUsers(r.Context(), principal)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
}

func (s *Server) apiV2Topology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "拓扑变更必须通过 Changeset")
		return
	}
	principal, _ := apiPrincipal(r)
	if !principal.HasScope("topology:read") {
		v2Error(w, r, http.StatusForbidden, "scope_denied", "缺少 topology:read 权限")
		return
	}
	item, err := s.application.Topology(r.Context(), principal)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, item, nil)
}

func (s *Server) apiV2Changesets(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	switch r.Method {
	case http.MethodGet:
		items, err := s.automation.List(r.Context(), principal, queryLimit(r, 50))
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
	case http.MethodPost:
		var request automation.CreateRequest
		if !decodeV2(w, r, &request) {
			return
		}
		item, err := s.automation.Create(r.Context(), principal, request)
		if err != nil {
			s.recordToolCall(r.Context(), principal, "changesets.create", request, "failed", capability.DataInternal)
			v2HandleError(w, r, err)
			return
		}
		s.recordToolCall(r.Context(), principal, "changesets.create", request, "succeeded", capability.DataInternal)
		v2Write(w, r, http.StatusCreated, item, nil)
	default:
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func (s *Server) apiV2Changeset(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v2/changesets/")
	if len(parts) == 0 || parts[0] == "" {
		v2Error(w, r, http.StatusBadRequest, "invalid_id", "缺少 Changeset ID")
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	principal, _ := apiPrincipal(r)
	if r.Method == http.MethodGet && action == "" {
		item, err := s.automation.Get(r.Context(), id)
		if err != nil || item.PrincipalID != principal.ID && !(principal.Interactive && principal.Role == model.RoleAdmin) {
			v2HandleError(w, r, sql.ErrNoRows)
			return
		}
		v2Write(w, r, http.StatusOK, item, nil)
		return
	}
	if r.Method != http.MethodPost {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	var item *model.AutomationChangeset
	var err error
	switch action {
	case "validate":
		item, err = s.automation.Validate(r.Context(), principal, id)
	case "approve":
		var request struct {
			Comment string `json:"comment"`
		}
		if !decodeV2(w, r, &request) {
			return
		}
		item, err = s.automation.Approve(r.Context(), principal, id, request.Comment)
	case "apply":
		item, err = s.automation.Apply(r.Context(), principal, id)
	default:
		v2Error(w, r, http.StatusNotFound, "not_found", "Changeset 操作不存在")
		return
	}
	if err != nil {
		s.recordToolCall(r.Context(), principal, "changesets."+action, map[string]string{"changeset_id": id}, "failed", capability.DataInternal)
		v2HandleError(w, r, err)
		return
	}
	s.recordToolCall(r.Context(), principal, "changesets."+action, map[string]string{"changeset_id": id}, "succeeded", capability.DataInternal)
	v2Write(w, r, http.StatusOK, item, nil)
}

func (s *Server) apiPrincipals(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListAPIPrincipals(r.Context())
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
	case http.MethodPost:
		var request struct {
			Name               string          `json:"name"`
			Scopes             []string        `json:"scopes"`
			ResourceFilter     json.RawMessage `json:"resource_filter"`
			AllowedCIDRs       []string        `json:"allowed_cidrs"`
			RateLimitPerMinute int             `json:"rate_limit_per_minute"`
			MaxConcurrency     int             `json:"max_concurrency"`
			ExpiresAt          *time.Time      `json:"expires_at"`
		}
		if !decodeV2(w, r, &request) {
			return
		}
		user := currentUser(r)
		if user == nil {
			v2Error(w, r, http.StatusUnauthorized, "unauthorized", "需要管理员登录")
			return
		}
		if request.ExpiresAt != nil && !request.ExpiresAt.After(time.Now().UTC()) {
			v2Error(w, r, http.StatusBadRequest, "invalid_expiry", "Principal 有效期必须晚于当前时间")
			return
		}
		principal, err := s.newServicePrincipal(*user, request.Name, request.Scopes, request.ResourceFilter, request.AllowedCIDRs, request.RateLimitPerMinute, request.MaxConcurrency, request.ExpiresAt)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		if err := s.store.CreateAPIPrincipal(r.Context(), principal); err != nil {
			v2HandleError(w, r, err)
			return
		}
		auditReq(s, r, "create", "api-principal", principal.ID)
		v2Write(w, r, http.StatusCreated, principal, nil)
	default:
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func (s *Server) apiPrincipalSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v2/api-principals/")
	if len(parts) < 1 || parts[0] == "" {
		v2Error(w, r, http.StatusBadRequest, "invalid_id", "缺少 API Principal ID")
		return
	}
	principalID := parts[0]
	if len(parts) == 1 && (r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
		s.apiPrincipalManage(w, r, principalID)
		return
	}
	if len(parts) == 2 && parts[1] == "tokens" {
		s.apiPrincipalTokens(w, r, principalID)
		return
	}
	if len(parts) == 3 && parts[1] == "tokens" && r.Method == http.MethodDelete {
		if err := s.store.RevokeAPIToken(r.Context(), principalID, parts[2]); err != nil {
			v2HandleError(w, r, err)
			return
		}
		auditReq(s, r, "revoke", "api-token", parts[2])
		v2Write(w, r, http.StatusOK, map[string]bool{"revoked": true}, nil)
		return
	}
	v2Error(w, r, http.StatusNotFound, "not_found", "API Principal 子资源不存在")
}

func (s *Server) apiPrincipalTokens(w http.ResponseWriter, r *http.Request, principalID string) {
	principal, err := s.store.GetAPIPrincipal(r.Context(), principalID)
	if err != nil || principal.Type != model.APIPrincipalServiceAccount {
		v2HandleError(w, r, sql.ErrNoRows)
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListAPITokens(r.Context(), principalID)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
	case http.MethodPost:
		var request struct {
			ExpiresAt *time.Time `json:"expires_at"`
		}
		if !decodeV2(w, r, &request) {
			return
		}
		raw, err := security.RandomToken(32)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		plain := "obk_" + raw
		id, _ := security.RandomToken(18)
		expires := time.Now().UTC().Add(90 * 24 * time.Hour)
		if request.ExpiresAt != nil {
			expires = request.ExpiresAt.UTC()
		}
		if !expires.After(time.Now().UTC()) || expires.After(time.Now().UTC().Add(366*24*time.Hour)) {
			v2Error(w, r, http.StatusBadRequest, "invalid_expiry", "token 有效期必须在未来一年内")
			return
		}
		token := &model.APIToken{ID: "tok_" + id, PrincipalID: principalID, TokenHash: security.HashAPISecret(s.sessionSecret, plain), Prefix: plain[:12], ExpiresAt: expires}
		if err := s.store.CreateAPIToken(r.Context(), token); err != nil {
			v2HandleError(w, r, err)
			return
		}
		auditReq(s, r, "create", "api-token", token.ID)
		v2Write(w, r, http.StatusCreated, map[string]any{"token": plain, "token_info": token}, nil)
	default:
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func (s *Server) newServicePrincipal(owner model.User, name string, scopes []string, filter json.RawMessage, cidrs []string, rate, concurrency int, expires *time.Time) (*model.APIPrincipal, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return nil, errors.New("name is required and must not exceed 80 characters")
	}
	if len(scopes) == 0 || len(scopes) > 128 {
		return nil, errors.New("at least one scope is required")
	}
	knownScopes := s.allCapabilityScopes()
	for _, scope := range scopes {
		known := scope == "*" || slices.Contains(knownScopes, scope)
		if strings.HasSuffix(scope, ":*") {
			domain := strings.TrimSuffix(scope, "*")
			known = slices.ContainsFunc(knownScopes, func(candidate string) bool { return strings.HasPrefix(candidate, domain) })
		}
		if !known || len(scope) > 80 {
			return nil, fmt.Errorf("invalid scope %q", scope)
		}
	}
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	normalizedCIDRs := make([]string, 0, len(cidrs))
	for _, raw := range cidrs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid allowed CIDR %q", raw)
		}
		normalizedCIDRs = append(normalizedCIDRs, prefix.Masked().String())
	}
	slices.Sort(normalizedCIDRs)
	normalizedCIDRs = slices.Compact(normalizedCIDRs)
	if rate == 0 {
		rate = 60
	}
	if concurrency == 0 {
		concurrency = 4
	}
	if rate < 1 || rate > 10_000 || concurrency < 1 || concurrency > 64 {
		return nil, errors.New("rate or concurrency limit is out of range")
	}
	if len(filter) == 0 {
		filter = json.RawMessage(`{}`)
	}
	var filterObject map[string]json.RawMessage
	if json.Unmarshal(filter, &filterObject) != nil {
		return nil, errors.New("resource_filter must be a JSON object")
	}
	canonicalFilter := map[string]any{}
	for key, raw := range filterObject {
		switch key {
		case "server_ids", "user_ids", "proxy_path_ids":
			var ids []int64
			if json.Unmarshal(raw, &ids) != nil {
				return nil, fmt.Errorf("resource_filter.%s must be an array of IDs", key)
			}
			for _, id := range ids {
				if id <= 0 {
					return nil, fmt.Errorf("resource_filter.%s contains an invalid ID", key)
				}
			}
			slices.Sort(ids)
			canonicalFilter[key] = slices.Compact(ids)
		case "global":
			var allowed bool
			if json.Unmarshal(raw, &allowed) != nil {
				return nil, errors.New("resource_filter.global must be a boolean")
			}
			canonicalFilter[key] = allowed
		default:
			return nil, fmt.Errorf("unsupported resource_filter field %q", key)
		}
	}
	filter, _ = json.Marshal(canonicalFilter)
	id, err := security.RandomToken(18)
	if err != nil {
		return nil, err
	}
	return &model.APIPrincipal{ID: "prn_" + id, OwnerUserID: &owner.ID, Name: name, Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: scopes, ResourceFilter: filter, AllowedCIDRs: normalizedCIDRs, RateLimitPerMinute: rate, MaxConcurrency: concurrency, ExpiresAt: expires}, nil
}

func (s *Server) registerAutomationHandlers() {
	s.automation.RegisterValidator("subscriptions.resume", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct {
			UserID int64 `json:"user_id"`
		}
		if err := strictAutomationInput(input, &request); err != nil || request.UserID <= 0 || !principal.AllowsInt64("user_ids", request.UserID) {
			return nil, errors.New("authorized user_id is required")
		}
		user, err := s.store.GetUser(ctx, request.UserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_id": user.ID, "currently_suspended": user.SubscriptionSuspended}, nil
	})
	s.automation.RegisterRevisionResolver("subscriptions.resume", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		var request struct {
			UserID int64 `json:"user_id"`
		}
		if err := strictAutomationInput(input, &request); err != nil || request.UserID <= 0 || !principal.AllowsInt64("user_ids", request.UserID) {
			return nil, errors.New("authorized user_id is required")
		}
		user, err := s.store.GetUser(ctx, request.UserID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"user:" + strconv.FormatInt(user.ID, 10): user.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("subscriptions.resume", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		if principal.UserID == nil {
			return nil, errors.New("subscription resume requires an owner user")
		}
		var request struct {
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(input, &request); err != nil || request.UserID <= 0 || !principal.AllowsInt64("user_ids", request.UserID) {
			return nil, errors.New("authorized user_id is required")
		}
		return s.store.ResumeSubscriptionAccess(ctx, request.UserID, *principal.UserID)
	})
	s.automation.RegisterValidator("servers.onboard", func(_ context.Context, _ application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerOnboardingOperation(input)
		if err != nil {
			return nil, err
		}
		if err := validateServer(&request.Server); err != nil {
			return nil, err
		}
		return map[string]any{"server_name": request.Server.Name, "issue_enrollment_token": request.IssueEnrollmentToken}, nil
	})
	s.automation.Register("servers.onboard", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerOnboardingOperation(input)
		if err != nil {
			return nil, err
		}
		server := request.Server
		server.ID, server.AgentID, server.AgentTokenHash, server.ChainSecret = 0, "", "", ""
		server.EnrollmentHash, server.EnrollmentExpiresAt = "", nil
		if err := validateServer(&server); err != nil {
			return nil, err
		}
		server.Status = model.ServerUnknown
		if err := s.store.CreateServer(ctx, &server); err != nil {
			return nil, err
		}
		result := map[string]any{"server": server}
		if request.IssueEnrollmentToken {
			token, err := security.RandomToken(32)
			if err != nil {
				return nil, err
			}
			expires := time.Now().UTC().Add(enrollmentTokenTTL)
			if err := s.store.SetServerEnrollmentHash(ctx, server.ID, security.HashSecret(token), expires); err != nil {
				return nil, err
			}
			result["enrollment_expires_at"] = expires
			oneTime := maps.Clone(result)
			oneTime["enrollment_token"] = token
			return automation.MutationResult{Public: result, OneTime: oneTime}, nil
		}
		return result, nil
	})
	s.automation.RegisterValidator("topology.write", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeTopologyWriteOperation(input)
		if err != nil {
			return nil, err
		}
		inbound, err := s.store.GetInbound(ctx, request.Path.InboundID)
		if err != nil || !principal.AllowsInt64("server_ids", inbound.ServerID) {
			return nil, errors.New("authorized path inbound is required")
		}
		for _, step := range request.Steps {
			if step.ServerID != nil && !principal.AllowsInt64("server_ids", *step.ServerID) {
				return nil, errors.New("proxy path step references an unauthorized server")
			}
		}
		return s.validateTopologyWriteCandidate(ctx, request)
	})
	s.automation.RegisterRevisionResolver("topology.write", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := decodeTopologyWriteOperation(input)
		if err != nil {
			return nil, err
		}
		return s.topologyWriteRevisions(ctx, principal, request)
	})
	s.automation.Register("topology.write", func(ctx context.Context, principal application.Principal, input json.RawMessage) (result any, resultErr error) {
		request, err := decodeTopologyWriteOperation(input)
		if err != nil {
			return nil, err
		}
		path := request.Path
		path.ID, path.Secret, path.BranchSourceStepID = 0, "", nil
		path.Secret, err = security.RandomToken(24)
		if err != nil {
			return nil, err
		}
		if err := s.validateProxyPath(ctx, &path); err != nil {
			return nil, err
		}
		if err := s.store.CreateProxyPath(ctx, &path); err != nil {
			return nil, err
		}
		defer func() {
			if resultErr != nil {
				_ = s.store.DeleteProxyPath(ctx, path.ID)
			}
		}()
		for index := range request.Steps {
			step := request.Steps[index]
			step.ID, step.PathID, step.Position = 0, path.ID, index+1
			if err := s.validateProxyPathStep(ctx, &step, 0); err != nil {
				return nil, err
			}
			if err := s.store.CreateProxyPathStep(ctx, &step); err != nil {
				return nil, err
			}
		}
		if err := s.normalizeAndValidateProxyPath(ctx, path.ID); err != nil {
			return nil, err
		}
		if err := s.ensureWARPProfilesForProxyPaths(ctx); err != nil {
			return nil, err
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			return nil, err
		}
		stored, _ := s.store.GetProxyPath(ctx, path.ID)
		steps, _ := s.store.ListProxyPathStepsForPath(ctx, path.ID)
		return map[string]any{"proxy_path": s.resolvedProxyPath(ctx, *stored), "proxy_path_steps": publicProxyPathSteps(steps), "requires_deployment": true}, nil
	})
	s.automation.RegisterValidator("topology.reuse_inbound", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request proxyPathReuseRequest
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		plan, err := s.planProxyPathReuse(ctx, request, false)
		if err != nil {
			return nil, err
		}
		for _, serverID := range plan.AffectedServerIDs {
			if !principal.AllowsInt64("server_ids", serverID) {
				return nil, errors.New("proxy path reuse references an unauthorized server")
			}
		}
		return map[string]any{
			"source_count": plan.SourceCount, "result_path_count": plan.ResultPathCount,
			"affected_servers": plan.AffectedServerIDs, "full_deployment_required": true,
		}, nil
	})
	s.automation.RegisterRevisionResolver("topology.reuse_inbound", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		var request proxyPathReuseRequest
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		plan, err := s.planProxyPathReuse(ctx, request, false)
		if err != nil {
			return nil, err
		}
		for _, serverID := range plan.AffectedServerIDs {
			if !principal.AllowsInt64("server_ids", serverID) {
				return nil, errors.New("proxy path reuse references an unauthorized server")
			}
		}
		return map[string]string{"routing_topology": plan.Revision}, nil
	})
	s.automation.Register("topology.reuse_inbound", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request proxyPathReuseRequest
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		plan, err := s.applyProxyPathReuseOperation(ctx, request, func(plan *proxyPathReusePlan) error {
			for _, serverID := range plan.AffectedServerIDs {
				if !principal.AllowsInt64("server_ids", serverID) {
					return errors.New("proxy path reuse references an unauthorized server")
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		paths := make([]model.ProxyPath, 0, len(plan.Writes))
		for _, write := range plan.Writes {
			paths = append(paths, write.Path)
		}
		return map[string]any{
			"proxy_paths": paths, "result_path_count": plan.ResultPathCount,
			"affected_server_ids": plan.AffectedServerIDs, "requires_deployment": true,
		}, nil
	})
	s.automation.RegisterValidator("deployments.apply", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		return s.application.PlanDeployment(ctx, principal, input)
	})
	s.automation.RegisterRevisionResolver("deployments.apply", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		var request struct {
			ServerIDs []int64 `json:"server_ids"`
			Reason    string  `json:"reason"`
		}
		if err := strictAutomationInput(input, &request); err != nil || len(request.ServerIDs) == 0 {
			return nil, errors.New("server_ids are required")
		}
		revisions := make(map[string]string, len(request.ServerIDs))
		for _, id := range slices.Compact(request.ServerIDs) {
			server, err := s.application.GetServer(ctx, principal, id)
			if err != nil {
				return nil, err
			}
			revisions["server:"+strconv.FormatInt(id, 10)] = server.Revision
		}
		return revisions, nil
	})
	s.automation.Register("deployments.apply", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct {
			ServerIDs []int64 `json:"server_ids"`
			Reason    string  `json:"reason"`
		}
		if err := strictAutomationInput(input, &request); err != nil || len(request.ServerIDs) == 0 {
			return nil, errors.New("server_ids are required")
		}
		allServers, err := s.store.ListServers(ctx)
		if err != nil {
			return nil, err
		}
		serverID := int64(0)
		if len(request.ServerIDs) == 1 {
			serverID = request.ServerIDs[0]
		} else if len(slices.Compact(request.ServerIDs)) != len(allServers) {
			return nil, errors.New("deployment Changeset must target one server or the complete server set")
		}
		return s.runDeploymentOperation(ctx, principal, serverID)
	})
}

type serverOnboardingOperation struct {
	Server               model.Server `json:"server"`
	IssueEnrollmentToken bool         `json:"issue_enrollment_token"`
}

func decodeServerOnboardingOperation(input json.RawMessage) (serverOnboardingOperation, error) {
	var request serverOnboardingOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	request.Server.ID, request.Server.AgentID, request.Server.AgentTokenHash, request.Server.ChainSecret = 0, "", "", ""
	request.Server.EnrollmentHash, request.Server.EnrollmentExpiresAt = "", nil
	return request, nil
}

type topologyWriteOperation struct {
	Path  model.ProxyPath       `json:"path"`
	Steps []model.ProxyPathStep `json:"steps"`
}

func decodeTopologyWriteOperation(input json.RawMessage) (topologyWriteOperation, error) {
	var request topologyWriteOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	if request.Path.ID != 0 || request.Path.InboundID <= 0 || len(request.Steps) == 0 || len(request.Steps) > 5 {
		return request, errors.New("topology.write requires a new path and between 1 and 5 ordered steps")
	}
	for _, step := range request.Steps {
		if step.ID != 0 || step.PathID != 0 || strings.TrimSpace(step.ConfigJSON) != "" && strings.TrimSpace(step.ConfigJSON) != "{}" {
			return request, errors.New("topology.write accepts only high-level steps without IDs or raw config_json")
		}
	}
	return request, nil
}

func strictAutomationInput(input json.RawMessage, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

func (s *Server) topologyWriteRevisions(ctx context.Context, principal application.Principal, request topologyWriteOperation) (map[string]string, error) {
	revisions := map[string]string{}
	addInbound := func(id int64) error {
		inbound, err := s.store.GetInbound(ctx, id)
		if err != nil || !principal.AllowsInt64("server_ids", inbound.ServerID) {
			return errors.New("proxy path references an unauthorized inbound")
		}
		revisions["inbound:"+strconv.FormatInt(id, 10)] = inbound.UpdatedAt.UTC().Format(time.RFC3339Nano)
		return nil
	}
	addServer := func(id int64) error {
		server, err := s.application.GetServer(ctx, principal, id)
		if err != nil {
			return err
		}
		revisions["server:"+strconv.FormatInt(id, 10)] = server.Revision
		return nil
	}
	if err := addInbound(request.Path.InboundID); err != nil {
		return nil, err
	}
	for _, step := range request.Steps {
		if step.ServerID != nil {
			if err := addServer(*step.ServerID); err != nil {
				return nil, err
			}
		}
		if step.InboundID != nil {
			if err := addInbound(*step.InboundID); err != nil {
				return nil, err
			}
		}
		if step.ExternalOutboundID != nil {
			external, err := s.store.GetExternalOutbound(ctx, *step.ExternalOutboundID)
			if err != nil {
				return nil, err
			}
			if external.ServerID != nil && !principal.AllowsInt64("server_ids", *external.ServerID) {
				return nil, errors.New("proxy path references an unauthorized external outbound")
			}
			revisions["external_outbound:"+strconv.FormatInt(external.ID, 10)] = external.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	return revisions, nil
}

func (s *Server) validateTopologyWriteCandidate(ctx context.Context, request topologyWriteOperation) (map[string]any, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	path := request.Path
	path.ID, path.Secret, path.BranchSourceStepID = 1, "", nil
	for _, current := range data.ProxyPaths {
		if current.ID >= path.ID {
			path.ID = current.ID + 1
		}
	}
	if err := s.validateProxyPath(ctx, &path); err != nil {
		return nil, err
	}
	steps := make([]model.ProxyPathStep, 0, len(request.Steps))
	nextStepID := int64(1)
	for _, current := range data.ProxyPathSteps {
		if current.ID >= nextStepID {
			nextStepID = current.ID + 1
		}
	}
	for index := range request.Steps {
		step := request.Steps[index]
		step.ID, step.PathID, step.Position = nextStepID+int64(index), path.ID, index+1
		if err := s.normalizeProxyPathStepCandidate(ctx, &step); err != nil {
			return nil, err
		}
		steps = append(steps, step)
		if err := s.validateProxyPathServerLoop(ctx, path.InboundID, steps); err != nil {
			return nil, err
		}
	}
	data.ProxyPaths = append(data.ProxyPaths, path)
	data.ProxyPathSteps = append(data.ProxyPathSteps, steps...)
	if err := normalizeProxyPathProcessingRolesInMemory(data.ProxyPathSteps, path.ID); err != nil {
		return nil, err
	}
	resolveRoutingProxyPathNames(&data)
	plans, err := core.BuildProxyPathPlansWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, core.NewProxyPathPortLedger(data.ProxyPathPortAllocations))
	if err != nil {
		return nil, err
	}
	plan, ok := proxyPathPlanByID(plans, path.ID)
	if !ok {
		return nil, errors.New("candidate proxy path did not produce a deployment plan")
	}
	return map[string]any{
		"inbound_id": path.InboundID, "step_count": len(steps), "full_deployment_required": true,
		"affected_servers": proxyPathPlanServerIDs(plan), "warnings": plan.Warnings,
	}, nil
}

func proxyPathPlanServerIDs(plan model.ProxyPathPlan) []int64 {
	seen := map[int64]bool{}
	ids := make([]int64, 0)
	for _, step := range plan.Steps {
		if step.ServerID == nil || seen[*step.ServerID] {
			continue
		}
		seen[*step.ServerID] = true
		ids = append(ids, *step.ServerID)
	}
	slices.Sort(ids)
	return ids
}

func (s *Server) runDeploymentOperation(ctx context.Context, principal application.Principal, serverID int64) (any, error) {
	body, _ := json.Marshal(map[string]int64{"server_id": serverID})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/apply", bytes.NewReader(body)).WithContext(ctx)
	if principal.UserID != nil {
		if user, err := s.store.GetUser(ctx, *principal.UserID); err == nil {
			request = request.WithContext(context.WithValue(request.Context(), userKey, user))
		}
	}
	request = request.WithContext(context.WithValue(request.Context(), claimsKey, security.TokenClaims{Role: string(model.RoleAdmin)}))
	recorder := httptest.NewRecorder()
	s.applyDeployment(recorder, request)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return nil, errors.New("deployment pipeline returned an invalid response")
	}
	if recorder.Code < 200 || recorder.Code >= 300 {
		return nil, fmt.Errorf("deployment validation failed: %v", response["error"])
	}
	taskIDs := []any{}
	if tasks, ok := response["tasks"].([]any); ok {
		for _, raw := range tasks {
			if task, ok := raw.(map[string]any); ok {
				taskIDs = append(taskIDs, task["id"])
			}
		}
	}
	return map[string]any{"config_version": response["config_version"], "task_ids": taskIDs, "summary": response["summary"]}, nil
}

func decodeV2(w http.ResponseWriter, r *http.Request, output any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		v2Error(w, r, http.StatusBadRequest, "invalid_request", "请求 JSON 无效")
		return false
	}
	return true
}

func v2Write(w http.ResponseWriter, r *http.Request, status int, data any, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["request_id"] = requestID(r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", meta["request_id"].(string))
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": meta})
}

func v2Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	requestID := requestID(r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "request_id": requestID}})
}

func v2HandleError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		v2Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case strings.Contains(strings.ToLower(err.Error()), "forbidden"), strings.Contains(strings.ToLower(err.Error()), "not authorized"):
		v2Error(w, r, http.StatusForbidden, "forbidden", "当前身份无权执行该操作")
	default:
		v2Error(w, r, http.StatusBadRequest, "invalid_operation", err.Error())
	}
}

func requestID(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if value != "" && len(value) <= 80 {
		return value
	}
	random, err := security.RandomToken(12)
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return "req_" + random
}

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 200 {
		return 200
	}
	return value
}
