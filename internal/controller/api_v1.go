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

func (s *Server) registerAPIV1Routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/openapi.json", s.apiAuth(s.apiV1OpenAPI, model.RoleViewer))
	mux.HandleFunc("/api/v1/capabilities", s.apiAuth(s.apiV1Capabilities, model.RoleViewer))
	mux.HandleFunc("/api/v1/query", s.apiAuth(s.apiV1Query, model.RoleViewer))
	mux.HandleFunc("/api/v1/servers", s.apiAuth(s.apiV1Servers, model.RoleViewer))
	mux.HandleFunc("/api/v1/servers/", s.apiAuth(s.apiV1Server, model.RoleViewer))
	mux.HandleFunc("/api/v1/latency-probes", s.apiAuth(s.apiV1LatencyProbes, model.RoleViewer))
	mux.HandleFunc("/api/v1/users", s.apiAuth(s.apiV1Users, model.RoleViewer))
	mux.HandleFunc("/api/v1/topology", s.apiAuth(s.apiV1Topology, model.RoleViewer))
	mux.HandleFunc("/api/v1/audit/incidents", s.apiAuth(s.apiV1AuditIncidents, model.RoleViewer))
	mux.HandleFunc("/api/v1/audit/incidents/", s.apiAuth(s.apiV1AuditIncident, model.RoleViewer))
	mux.HandleFunc("/api/v1/node-incidents", s.apiAuth(s.apiV1NodeIncidents, model.RoleViewer))
	mux.HandleFunc("/api/v1/node-incidents/", s.apiAuth(s.apiV1NodeIncidents, model.RoleViewer))
	mux.HandleFunc("/api/v1/telegram/binding-code", s.apiAuth(s.apiV1TelegramBindingCode, model.RoleNone))
	mux.HandleFunc("/api/v1/telegram/bindings", s.apiAuth(s.apiV1TelegramBindings, model.RoleNone))
	mux.HandleFunc("/api/v1/telegram/bindings/", s.apiAuth(s.apiV1TelegramBindings, model.RoleNone))
	mux.HandleFunc("/api/v1/notification-broadcasts", s.apiAuth(s.apiV1NotificationBroadcasts, model.RoleAdmin))
	mux.HandleFunc("/api/v1/notification-broadcasts/", s.apiAuth(s.apiV1NotificationBroadcasts, model.RoleAdmin))
	mux.HandleFunc("/api/v1/changesets", s.apiAuth(s.apiV1Changesets, model.RoleViewer))
	mux.HandleFunc("/api/v1/changesets/", s.apiAuth(s.apiV1Changeset, model.RoleViewer))
	mux.HandleFunc("/api/v1/api-principals", s.auth(s.apiPrincipals, model.RoleAdmin))
	mux.HandleFunc("/api/v1/api-principals/", s.auth(s.apiPrincipalSubroutes, model.RoleAdmin))
	mux.HandleFunc("/api/v1/ai/providers", s.auth(s.apiV1AIProviders, model.RoleAdmin))
	mux.HandleFunc("/api/v1/ai/providers/", s.auth(s.apiV1AIProvider, model.RoleAdmin))
	mux.HandleFunc("/api/v1/ai/provider-models", s.auth(s.apiV1AIProviderModels, model.RoleAdmin))
	mux.HandleFunc("/api/v1/ai/provider-test", s.auth(s.apiV1AIProviderTest, model.RoleAdmin))
	mux.HandleFunc("/api/v1/ai/provider-test-logs", s.auth(s.apiV1AIProviderTestLogs, model.RoleAdmin))
	mux.HandleFunc("/api/v1/approval-policies", s.auth(s.apiV1ApprovalPolicies, model.RoleAdmin))
	mux.HandleFunc("/api/v1/approval-policies/", s.auth(s.apiV1ApprovalPolicy, model.RoleAdmin))
	mux.HandleFunc("/api/v1/tool-audits", s.auth(s.apiV1ToolAudits, model.RoleAdmin))
}

func (s *Server) apiV1LatencyProbes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	principal, _ := apiPrincipal(r)
	if !principal.HasScope("servers:read") {
		v2Error(w, r, http.StatusForbidden, "scope_denied", "缺少 servers:read 权限")
		return
	}
	serverID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("server_id")), 10, 64)
	if err != nil || serverID <= 0 || !principal.AllowsInt64("server_ids", serverID) {
		v2Error(w, r, http.StatusBadRequest, "invalid_server", "server_id 无效")
		return
	}
	items, err := s.store.ListLatencyProbeResults(r.Context(), serverID, queryLimit(r, 512))
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, map[string]any{"server_id": serverID, "results": items}, nil)
}

func (s *Server) apiV1AuditIncidents(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) apiV1AuditIncident(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/audit/incidents/")
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

func (s *Server) apiV1OpenAPI(w http.ResponseWriter, r *http.Request) {
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
			if r.URL.Path == "/api/v1/mcp" {
				s.writeMCPAuthenticationRequired(w, r, true)
				return
			}
			s.machineAPIAuth(next, w, r, token)
			return
		}
		if strings.HasPrefix(token, "oba_") {
			stored, err := s.authenticateOAuthToken(r, token)
			if err != nil {
				if r.URL.Path == "/api/v1/mcp" {
					s.writeMCPAuthenticationRequired(w, r, true)
					return
				}
				v2Error(w, r, http.StatusUnauthorized, "invalid_token", "OAuth access token 无效或已过期")
				return
			}
			source, _ := netip.ParseAddr(clientIP(r))
			principal := application.Principal{ID: stored.ID, GrantID: stored.OAuthGrantID, UserID: stored.OwnerUserID, Name: stored.Name, Type: stored.Type, Scopes: stored.Scopes, ResourceFilter: stored.ResourceFilter, SourceIP: source, ClientName: r.Header.Get("User-Agent")}
			s.machinePrincipalAuth(next, w, r, principal)
			return
		}
		if r.URL.Path == "/api/v1/mcp" {
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
		if r.URL.Path == "/api/v1/mcp" {
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
	if r.URL.Path == "/api/v1/mcp" {
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
	if strings.HasPrefix(r.URL.Path, "/api/v1/users") || strings.HasPrefix(r.URL.Path, "/api/v1/audit") {
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

func (s *Server) apiV1Capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	principal, _ := apiPrincipal(r)
	v2Write(w, r, http.StatusOK, s.capabilities.List(principal), nil)
}

func (s *Server) apiV1Query(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.queryManagementCapability(r.Context(), principal, request.Capability, request.Arguments)
	if err != nil {
		s.recordToolCall(r.Context(), principal, request.Capability, request.Arguments, "failed", descriptor.DataClassification)
		v2HandleError(w, r, err)
		return
	}
	s.recordToolCall(r.Context(), principal, request.Capability, request.Arguments, "succeeded", descriptor.DataClassification)
	v2Write(w, r, http.StatusOK, result, nil)
}

func (s *Server) apiV1Servers(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) apiV1Server(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "服务器变更必须通过 Changeset")
		return
	}
	id, err := application.ParseID(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"))
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

func (s *Server) apiV1Users(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) apiV1Topology(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) apiV1Changesets(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) apiV1Changeset(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/changesets/")
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
		if err != nil || item.PrincipalID != principal.ID && !(principal.Interactive && model.HasManagementAccess(principal.Role)) {
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
		item, err = s.applyAutomationChangeset(r.Context(), principal, id)
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
	parts := pathParts(r.URL.Path, "/api/v1/api-principals/")
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
		case "server_ids", "user_ids", "group_ids", "proxy_path_ids":
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
	s.registerServerLifecycleOperations()
	s.registerServerUpdateOperation()
	s.registerServerExpiryOperation()
	s.registerInboundAutomationOperations()
	s.registerProxyPathAutomationOperations()
	s.registerSubscriptionPlanAutomationOperations()
	s.registerUserAutomationOperations()
	s.registerTrafficAutomationOperations()
	s.registerNetworkAutomationOperations()
	s.registerOpsAutomationOperations()
	s.registerAuditAutomationOperations()
	s.registerSystemAutomationOperations()
	s.registerNodeIncidentAutomationOperations()
	s.registerNodeWorkspaceAutomationOperations()
	s.automation.RegisterValidator("subscriptions.custom_paths.set_alias", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct {
			UserID int64  `json:"user_id"`
			Alias  string `json:"alias"`
			Delete bool   `json:"delete"`
		}
		if err := strictAutomationInput(input, &request); err != nil || request.UserID <= 0 || !principal.AllowsInt64("user_ids", request.UserID) {
			return nil, errors.New("authorized user_id is required")
		}
		state, err := s.application.SubscriptionCustomPathUser(ctx, principal, request.UserID)
		if err != nil {
			return nil, err
		}
		if request.Delete {
			if strings.TrimSpace(request.Alias) != "" {
				return nil, errors.New("alias must be empty when delete is true")
			}
			return map[string]any{"user_id": request.UserID, "delete": true, "currently_configured": state.SubscriptionCustomPath != ""}, nil
		}
		alias, err := core.NormalizeSubscriptionCustomPathAlias(request.Alias)
		if err != nil {
			return nil, err
		}
		if !state.SubscriptionCustomPathEnabled {
			return nil, errors.New("custom subscription path is not enabled for this user")
		}
		return map[string]any{"user_id": request.UserID, "alias": alias, "replaces_existing": state.SubscriptionCustomPath != ""}, nil
	})
	s.automation.RegisterRevisionResolver("subscriptions.custom_paths.set_alias", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		var request struct {
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(input, &request); err != nil || request.UserID <= 0 || !principal.AllowsInt64("user_ids", request.UserID) {
			return nil, errors.New("authorized user_id is required")
		}
		user, err := s.store.GetUser(ctx, request.UserID)
		if err != nil {
			return nil, err
		}
		revision := user.UpdatedAt.UTC().Format(time.RFC3339Nano)
		if path, err := s.store.GetSubscriptionCustomPathForUser(ctx, request.UserID); err == nil {
			revision = path.UpdatedAt.UTC().Format(time.RFC3339Nano)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return map[string]string{"subscription_custom_path:user:" + strconv.FormatInt(request.UserID, 10): revision}, nil
	})
	s.automation.Register("subscriptions.custom_paths.set_alias", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct {
			UserID int64  `json:"user_id"`
			Alias  string `json:"alias"`
			Delete bool   `json:"delete"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		if request.Delete {
			if err := s.application.DeleteSubscriptionCustomPath(ctx, principal, request.UserID); err != nil {
				return nil, err
			}
			return map[string]any{"user_id": request.UserID, "deleted": true}, nil
		}
		item, err := s.application.SetSubscriptionCustomPath(ctx, principal, request.UserID, request.Alias)
		if err != nil {
			return nil, err
		}
		return map[string]any{"subscription_custom_path": item}, nil
	})
	s.automation.RegisterValidator("subscriptions.custom_paths.set_policy", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionCustomPathPolicyOperation(input)
		if err != nil {
			return nil, err
		}
		switch request.TargetType {
		case "global":
			if request.TargetID != 0 || !principal.AllowsGlobal() {
				return nil, errors.New("global resource access is required")
			}
			mode := model.SubscriptionCustomPathMode(request.Mode)
			switch mode {
			case model.SubscriptionCustomPathDisabled, model.SubscriptionCustomPathSelective, model.SubscriptionCustomPathEnabled:
				return map[string]any{"target_type": "global", "mode": mode}, nil
			default:
				return nil, errors.New("global mode must be disabled, selective or enabled")
			}
		case "user":
			if !principal.AllowsInt64("user_ids", request.TargetID) {
				return nil, errors.New("authorized user target is required")
			}
			if _, err := s.store.GetUser(ctx, request.TargetID); err != nil {
				return nil, err
			}
		case "group":
			if !principal.AllowsInt64("group_ids", request.TargetID) {
				return nil, errors.New("authorized group target is required")
			}
			if _, err := s.store.GetUserGroup(ctx, request.TargetID); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("target_type must be global, user or group")
		}
		policy := model.SubscriptionCustomPathPolicy(request.Mode)
		if err := core.ValidateSubscriptionCustomPathPolicy(policy); err != nil {
			return nil, err
		}
		return map[string]any{"target_type": request.TargetType, "target_id": request.TargetID, "mode": policy}, nil
	})
	s.automation.RegisterRevisionResolver("subscriptions.custom_paths.set_policy", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := decodeSubscriptionCustomPathPolicyOperation(input)
		if err != nil {
			return nil, err
		}
		switch request.TargetType {
		case "global":
			if !principal.AllowsGlobal() {
				return nil, errors.New("global resource access is required")
			}
			settings, err := s.store.ListSettings(ctx)
			return map[string]string{"setting:" + application.SubscriptionCustomPathModeSetting: settings[application.SubscriptionCustomPathModeSetting]}, err
		case "user":
			user, err := s.store.GetUser(ctx, request.TargetID)
			if err != nil || !principal.AllowsInt64("user_ids", request.TargetID) {
				return nil, errors.New("authorized user target is required")
			}
			return map[string]string{"user:" + strconv.FormatInt(user.ID, 10): user.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		case "group":
			group, err := s.store.GetUserGroup(ctx, request.TargetID)
			if err != nil || !principal.AllowsInt64("group_ids", request.TargetID) {
				return nil, errors.New("authorized group target is required")
			}
			return map[string]string{"group:" + strconv.FormatInt(group.ID, 10): group.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		default:
			return nil, errors.New("target_type must be global, user or group")
		}
	})
	s.automation.Register("subscriptions.custom_paths.set_policy", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionCustomPathPolicyOperation(input)
		if err != nil {
			return nil, err
		}
		switch request.TargetType {
		case "global":
			err = s.application.SetSubscriptionCustomPathMode(ctx, principal, model.SubscriptionCustomPathMode(request.Mode))
		case "user":
			err = s.application.SetSubscriptionCustomPathUserPolicy(ctx, principal, request.TargetID, model.SubscriptionCustomPathPolicy(request.Mode))
		case "group":
			err = s.application.SetSubscriptionCustomPathGroupPolicy(ctx, principal, request.TargetID, model.SubscriptionCustomPathPolicy(request.Mode))
		default:
			err = errors.New("target_type must be global, user or group")
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"target_type": request.TargetType, "target_id": request.TargetID, "mode": request.Mode}, nil
	})
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
	s.automation.RegisterValidator("servers.onboard", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		if !principal.AllowsCreate("server") {
			return nil, errors.New("resource filter does not allow creating servers")
		}
		request, err := decodeServerOnboardingOperation(input)
		if err != nil {
			return nil, err
		}
		if err := s.applyServerOnboardingDefaults(ctx, input, &request); err != nil {
			return nil, err
		}
		if err := validateServer(&request.Server); err != nil {
			return nil, err
		}
		if err := s.rejectDuplicateServerName(ctx, request.Server.Name, 0); err != nil {
			return nil, err
		}
		return map[string]any{"server_name": request.Server.Name, "issue_enrollment_token": request.IssueEnrollmentToken}, nil
	})
	s.automation.Register("servers.onboard", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		if !principal.AllowsCreate("server") {
			return nil, errors.New("resource filter does not allow creating servers")
		}
		request, err := decodeServerOnboardingOperation(input)
		if err != nil {
			return nil, err
		}
		if err := s.applyServerOnboardingDefaults(ctx, input, &request); err != nil {
			return nil, err
		}
		server := request.Server
		server.ID, server.AgentID, server.AgentTokenHash, server.ChainSecret = 0, "", "", ""
		server.EnrollmentHash, server.EnrollmentExpiresAt = "", nil
		if err := validateServer(&server); err != nil {
			return nil, err
		}
		if err := s.rejectDuplicateServerName(ctx, server.Name, 0); err != nil {
			return nil, err
		}
		server.Status = model.ServerUnknown
		if err := s.store.CreateServer(ctx, &server); err != nil {
			return nil, err
		}
		result := map[string]any{"server": server}
		if request.IssueEnrollmentToken {
			token, expires, updated, err := s.issueServerEnrollmentToken(ctx, server.ID)
			if err != nil {
				return nil, err
			}
			result["server"] = *updated
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
		if request.RoutingRule != nil {
			for _, serverID := range []int64{request.RoutingRule.ServerID} {
				if serverID > 0 && !principal.AllowsInt64("server_ids", serverID) {
					return nil, errors.New("routing rule references an unauthorized server")
				}
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
		pathCreated := false
		defer func() {
			if resultErr != nil && pathCreated {
				_ = s.store.DeleteProxyPath(ctx, path.ID)
			}
		}()
		if len(request.Steps) == 0 {
			if request.RoutingRule == nil {
				if err := s.store.CreateProxyPath(ctx, &path); err != nil {
					return nil, err
				}
				pathCreated = true
			} else {
				requestedEnabled := path.Enabled
				path.Enabled = false
				if err := s.store.CreateProxyPath(ctx, &path); err != nil {
					return nil, err
				}
				pathCreated = true
				path.Enabled = requestedEnabled
				rule := *request.RoutingRule
				rule.ID, rule.ProxyPathID, rule.StageStepID = 0, &path.ID, nil
				root, err := s.store.GetInbound(ctx, path.InboundID)
				if err != nil {
					return nil, err
				}
				rule.ServerID, rule.Scope = root.ServerID, model.RoutingRuleScopePathStage
				if err := s.validateRoutingRuleWithCandidatePath(ctx, &rule, &path); err != nil {
					return nil, err
				}
				sourceRuleID, err := s.prepareRoutingRuleReuse(ctx, &rule)
				if err != nil {
					return nil, err
				}
				groupID := ""
				if sourceRuleID > 0 {
					groupID, err = security.RandomToken(18)
					if err != nil {
						return nil, err
					}
				}
				if err := s.store.ActivateProxyPathWithRoutingRule(ctx, path.ID, &rule, sourceRuleID, groupID); err != nil {
					return nil, err
				}
				request.RoutingRule = &rule
			}
		} else {
			data, err := s.store.FullRoutingConfigData(ctx)
			if err != nil {
				return nil, err
			}
			path.ID = 1
			for _, current := range data.ProxyPaths {
				if current.ID >= path.ID {
					path.ID = current.ID + 1
				}
			}
			maxStepID := int64(0)
			for _, current := range data.ProxyPathSteps {
				if current.ID > maxStepID {
					maxStepID = current.ID
				}
			}
			steps := make([]model.ProxyPathStep, len(request.Steps))
			for index := range request.Steps {
				step := request.Steps[index]
				step.ID, step.PathID, step.Position = maxStepID+int64(index)+1, path.ID, index+1
				if err := s.normalizeProxyPathStepCandidate(ctx, &step); err != nil {
					return nil, err
				}
				steps[index] = step
			}
			if err := s.validateProxyPathServerLoop(ctx, path.InboundID, steps); err != nil {
				return nil, err
			}
			if err := normalizeProxyPathProcessingRolesInMemory(steps, path.ID); err != nil {
				return nil, err
			}
			if err := s.store.CreateProxyPathComposition(ctx, &path, steps); err != nil {
				return nil, err
			}
			pathCreated = true
		}
		if err := s.ensureWARPProfilesForProxyPaths(ctx); err != nil {
			return nil, err
		}
		if err := s.reconcileProxyPathNameTemplates(ctx); err != nil {
			return nil, err
		}
		stored, _ := s.store.GetProxyPath(ctx, path.ID)
		steps, _ := s.store.ListProxyPathStepsForPath(ctx, path.ID)
		response := map[string]any{"proxy_path": s.resolvedProxyPath(ctx, *stored), "proxy_path_steps": publicProxyPathSteps(steps), "requires_deployment": true}
		if request.RoutingRule != nil {
			response["routing_rule"] = request.RoutingRule
		}
		return response, nil
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

type subscriptionCustomPathPolicyOperation struct {
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
	Mode       string `json:"mode"`
}

func decodeSubscriptionCustomPathPolicyOperation(input json.RawMessage) (subscriptionCustomPathPolicyOperation, error) {
	var request subscriptionCustomPathPolicyOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	request.TargetType = strings.ToLower(strings.TrimSpace(request.TargetType))
	request.Mode = strings.ToLower(strings.TrimSpace(request.Mode))
	if request.TargetType != "global" && request.TargetID <= 0 {
		return request, errors.New("positive target_id is required")
	}
	return request, nil
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

// applyServerOnboardingDefaults keeps automation/MCP onboarding aligned with
// the panel create form. A missing field uses the current Controller default;
// an explicitly supplied false/zero value remains authoritative.
func (s *Server) applyServerOnboardingDefaults(ctx context.Context, input json.RawMessage, request *serverOnboardingOperation) error {
	var envelope struct {
		Server map[string]json.RawMessage `json:"server"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return err
	}
	mtuMode, bbrEnabled, timeMode := serverCreationDefaults(settings)
	if _, ok := envelope.Server["mtu_mode"]; !ok {
		request.Server.MTUMode = mtuMode
	}
	if _, ok := envelope.Server["bbr_enabled"]; !ok {
		request.Server.BBREnabled = bbrEnabled
	}
	if _, ok := envelope.Server["time_correction_mode"]; !ok {
		request.Server.TimeCorrectionMode = timeMode
	}
	if _, ok := envelope.Server["offline_notify_enabled"]; !ok {
		request.Server.OfflineNotifyEnabled = true
	}
	if _, ok := envelope.Server["expiry_notify_enabled"]; !ok {
		request.Server.ExpiryNotifyEnabled = true
	}
	if _, ok := envelope.Server["renewal_cycle"]; !ok {
		request.Server.RenewalCycle = model.ServerRenewalCycleMonthly
	}
	if _, ok := envelope.Server["resource_history_enabled"]; !ok {
		request.Server.ResourceHistoryEnabled = true
	}
	request.Server.ResourceHistoryConfigured = true
	if _, ok := envelope.Server["latency_probe_enabled"]; !ok {
		request.Server.LatencyProbeEnabled = true
	}
	if _, ok := envelope.Server["connection_audit_enabled"]; !ok {
		request.Server.ConnectionAuditEnabled = settingBool(settings, settingConnectionAuditEnabled, true)
	}
	_, hasMode := envelope.Server["traffic_reset_mode"]
	_, hasDay := envelope.Server["traffic_reset_day"]
	if !hasMode && !hasDay {
		if derivedMode, derivedDay, ok := deriveServerTrafficReset(nil, nil, request.Server.ServiceStartAt, request.Server.ExpiresAt, trafficLocation(settings)); ok {
			request.Server.TrafficResetMode = derivedMode
			request.Server.TrafficResetDay = derivedDay
		} else {
			request.Server.TrafficResetMode = "monthly"
			request.Server.TrafficResetDay = 1
		}
	} else {
		if !hasMode {
			request.Server.TrafficResetMode = "monthly"
		} else {
			request.Server.TrafficResetMode = normalizeControllerTrafficResetMode(request.Server.TrafficResetMode)
		}
		if !hasDay {
			request.Server.TrafficResetDay = 1
		} else {
			request.Server.TrafficResetDay = normalizeControllerTrafficResetDay(request.Server.TrafficResetDay)
		}
	}
	return nil
}

type topologyWriteOperation struct {
	Path        model.ProxyPath       `json:"path"`
	Steps       []model.ProxyPathStep `json:"steps"`
	RoutingRule *model.RoutingRule    `json:"routing_rule,omitempty"`
}

func decodeTopologyWriteOperation(input json.RawMessage) (topologyWriteOperation, error) {
	type highLevelStep struct {
		NodeType            model.ProxyPathStepNodeType      `json:"node_type"`
		TransportMode       model.ProxyPathStepTransportMode `json:"transport_mode"`
		ProcessingRole      bool                             `json:"processing_role"`
		ServerID            *int64                           `json:"server_id,omitempty"`
		InboundID           *int64                           `json:"inbound_id,omitempty"`
		ExternalOutboundID  *int64                           `json:"external_outbound_id,omitempty"`
		TunnelType          string                           `json:"tunnel_type,omitempty"`
		SSHPort             int                              `json:"ssh_port,omitempty"`
		PersistentKeepalive *int                             `json:"persistent_keepalive,omitempty"`
	}
	var wire struct {
		Path        model.ProxyPath    `json:"path"`
		Steps       []highLevelStep    `json:"steps"`
		RoutingRule *model.RoutingRule `json:"routing_rule,omitempty"`
	}
	if err := strictAutomationInput(input, &wire); err != nil {
		return topologyWriteOperation{}, err
	}
	request := topologyWriteOperation{Path: wire.Path, Steps: make([]model.ProxyPathStep, 0, len(wire.Steps)), RoutingRule: wire.RoutingRule}
	for _, value := range wire.Steps {
		step := model.ProxyPathStep{NodeType: value.NodeType, TransportMode: value.TransportMode, ProcessingRole: value.ProcessingRole, ServerID: value.ServerID, InboundID: value.InboundID, ExternalOutboundID: value.ExternalOutboundID}
		if step.TransportMode == model.ProxyPathTransportTunnel {
			config := map[string]any{"type": strings.ToLower(strings.TrimSpace(value.TunnelType))}
			if config["type"] == "" {
				config["type"] = string(model.TunnelTypeSSH)
			}
			if value.SSHPort > 0 {
				config["ssh_port"] = value.SSHPort
			}
			if value.PersistentKeepalive != nil {
				config["persistent_keepalive"] = *value.PersistentKeepalive
			}
			encoded, _ := json.Marshal(config)
			step.ConfigJSON = string(encoded)
		}
		request.Steps = append(request.Steps, step)
	}
	if request.Path.ID != 0 || request.Path.InboundID <= 0 || len(request.Steps) > 5 || request.Path.Kind != model.ProxyPathKindDirect && len(request.Steps) == 0 {
		return request, errors.New("topology.write requires a new direct path or a new chain with between 1 and 5 ordered steps")
	}
	for _, step := range request.Steps {
		if step.ID != 0 || step.PathID != 0 {
			return request, errors.New("topology.write accepts only high-level steps without IDs or raw config_json")
		}
	}
	if request.RoutingRule != nil {
		if request.Path.Kind != model.ProxyPathKindDirect || len(request.Steps) != 0 || request.RoutingRule.ID != 0 || request.RoutingRule.Action == model.RouteActionProxyPath {
			return request, errors.New("topology.write routing_rule requires a new direct path without steps and cannot use proxy_path action")
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
	result := map[string]any{
		"inbound_id": path.InboundID, "step_count": len(steps), "full_deployment_required": true,
		"affected_servers": proxyPathPlanServerIDs(plan), "warnings": plan.Warnings,
	}
	if request.RoutingRule != nil {
		rule := *request.RoutingRule
		rule.ID, rule.ProxyPathID, rule.StageStepID = 0, &path.ID, nil
		root, err := s.store.GetInbound(ctx, path.InboundID)
		if err != nil {
			return nil, err
		}
		rule.ServerID, rule.Scope = root.ServerID, model.RoutingRuleScopePathStage
		if err := s.validateRoutingRuleWithCandidatePath(ctx, &rule, &path); err != nil {
			return nil, err
		}
		result["routing_rule"] = rule
	}
	return result, nil
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
	if serverID > 0 && !principal.AllowsInt64("server_ids", serverID) {
		return nil, errors.New("deployment target is outside the authorized resource boundary")
	}
	if serverID == 0 {
		servers, err := s.store.ListServers(ctx)
		if err != nil {
			return nil, err
		}
		for _, server := range servers {
			if !principal.AllowsInt64("server_ids", server.ID) {
				return nil, errors.New("deployment target set is outside the authorized resource boundary")
			}
		}
	}
	tasks, version, err := s.deployConfiguration(ctx, serverID, false)
	if err != nil {
		return nil, err
	}
	taskIDs := make([]int64, 0, len(tasks))
	for _, task := range tasks {
		if !principal.AllowsInt64("server_ids", task.ServerID) {
			return nil, errors.New("deployment expanded outside the authorized resource boundary")
		}
		taskIDs = append(taskIDs, task.ID)
	}
	return map[string]any{"config_version": version, "task_ids": taskIDs, "summary": taskSummary(tasks)}, nil
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
