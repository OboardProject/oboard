package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) apiPrincipalManage(w http.ResponseWriter, r *http.Request, id string) {
	item, err := s.store.GetAPIPrincipal(r.Context(), id)
	if err != nil || item.Type != model.APIPrincipalServiceAccount {
		v2HandleError(w, r, errors.New("service account not found"))
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.store.DeleteAPIPrincipal(r.Context(), id); err != nil {
			v2HandleError(w, r, err)
			return
		}
		auditReq(s, r, "delete", "api-principal", id)
		v2Write(w, r, http.StatusOK, map[string]bool{"deleted": true}, nil)
		return
	}
	var request struct {
		Name               *string          `json:"name"`
		Enabled            *bool            `json:"enabled"`
		Scopes             *[]string        `json:"scopes"`
		ResourceFilter     *json.RawMessage `json:"resource_filter"`
		AllowedCIDRs       *[]string        `json:"allowed_cidrs"`
		RateLimitPerMinute *int             `json:"rate_limit_per_minute"`
		MaxConcurrency     *int             `json:"max_concurrency"`
		ExpiresAt          **time.Time      `json:"expires_at"`
	}
	if !decodeV2(w, r, &request) {
		return
	}
	if request.Name != nil {
		item.Name = *request.Name
	}
	if request.Enabled != nil {
		item.Enabled = *request.Enabled
	}
	if request.Scopes != nil {
		item.Scopes = *request.Scopes
	}
	if request.ResourceFilter != nil {
		item.ResourceFilter = *request.ResourceFilter
	}
	if request.AllowedCIDRs != nil {
		item.AllowedCIDRs = *request.AllowedCIDRs
	}
	if request.RateLimitPerMinute != nil {
		item.RateLimitPerMinute = *request.RateLimitPerMinute
	}
	if request.MaxConcurrency != nil {
		item.MaxConcurrency = *request.MaxConcurrency
	}
	if request.ExpiresAt != nil {
		item.ExpiresAt = *request.ExpiresAt
	}
	owner := currentUser(r)
	if owner == nil {
		v2Error(w, r, http.StatusUnauthorized, "unauthorized", "需要管理员登录")
		return
	}
	validated, err := s.newServicePrincipal(*owner, item.Name, item.Scopes, item.ResourceFilter, item.AllowedCIDRs, item.RateLimitPerMinute, item.MaxConcurrency, item.ExpiresAt)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	item.Name, item.Scopes, item.ResourceFilter, item.AllowedCIDRs = validated.Name, validated.Scopes, validated.ResourceFilter, validated.AllowedCIDRs
	item.RateLimitPerMinute, item.MaxConcurrency = validated.RateLimitPerMinute, validated.MaxConcurrency
	if err := s.store.UpdateAPIPrincipal(r.Context(), item); err != nil {
		v2HandleError(w, r, err)
		return
	}
	auditReq(s, r, "update", "api-principal", id)
	v2Write(w, r, http.StatusOK, item, nil)
}

func (s *Server) apiV2ApprovalPolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items, err := s.store.ListApprovalPolicies(r.Context(), strings.TrimSpace(r.URL.Query().Get("principal_id")))
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
		return
	}
	if r.Method != http.MethodPost {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	var request struct {
		PrincipalID    string             `json:"principal_id"`
		Capability     string             `json:"capability"`
		ResourceFilter json.RawMessage    `json:"resource_filter"`
		Mode           model.ApprovalMode `json:"mode"`
		AllowRisk4     bool               `json:"allow_risk4"`
		ExpiresAt      *time.Time         `json:"expires_at"`
	}
	if !decodeV2(w, r, &request) {
		return
	}
	principal, principalErr := s.store.GetAPIPrincipal(r.Context(), strings.TrimSpace(request.PrincipalID))
	descriptor, capabilityOK := s.capabilities.Get(strings.TrimSpace(request.Capability))
	principalScopes := application.Principal{}
	if principal != nil {
		principalScopes.Scopes = principal.Scopes
	}
	if principalErr != nil || principal.Type == model.APIPrincipalOAuth || !descriptor.Executable || !capabilityOK || !slices.Contains([]model.ApprovalMode{model.ApprovalDenied, model.ApprovalRequired, model.ApprovalAutomatic}, request.Mode) || request.AllowRisk4 && descriptor.RiskClass < 4 || !principalScopes.HasScope(descriptor.RequiredScopes[0]) {
		v2Error(w, r, http.StatusBadRequest, "invalid_approval_policy", "审批策略与 Principal 或能力不匹配")
		return
	}
	if len(request.ResourceFilter) == 0 {
		request.ResourceFilter = json.RawMessage(`{}`)
	}
	var filter map[string]any
	if json.Unmarshal(request.ResourceFilter, &filter) != nil {
		v2Error(w, r, http.StatusBadRequest, "invalid_resource_filter", "资源过滤必须是 JSON 对象")
		return
	}
	id, _ := security.RandomToken(18)
	if existing, err := s.store.GetApprovalPolicy(r.Context(), principal.ID, descriptor.Name, time.Time{}); err == nil {
		id = strings.TrimPrefix(existing.ID, "pol_")
	}
	item := &model.ApprovalPolicy{ID: "pol_" + id, PrincipalID: principal.ID, Capability: descriptor.Name, ResourceFilter: request.ResourceFilter, Mode: request.Mode, AllowRisk4: request.AllowRisk4, ExpiresAt: request.ExpiresAt}
	if err := s.store.UpsertApprovalPolicy(r.Context(), item); err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, item, nil)
}

func (s *Server) apiV2ApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v2/approval-policies/"), "/")
	if err := s.store.DeleteApprovalPolicy(r.Context(), id); err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, map[string]bool{"deleted": true}, nil)
}

func (s *Server) apiV2ToolAudits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	items, err := s.store.ListToolCallAudits(r.Context(), strings.TrimSpace(r.URL.Query().Get("principal_id")), queryLimit(r, 50))
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, http.StatusOK, items, map[string]any{"count": len(items)})
}

func (s *Server) oauthClient(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v2/oauth-clients/"), "/")
	item, err := s.store.GetOAuthClient(r.Context(), id)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.store.DeleteOAuthClient(r.Context(), id); err != nil {
			v2HandleError(w, r, err)
			return
		}
		s.auditOAuthEvent(r, oauthAuditActor(currentUser(r)), "oauth_client_deleted", "oauth_client", id, map[string]any{"client_name": boundedOAuthAuditValue(item.Name)})
		v2Write(w, r, http.StatusOK, map[string]bool{"deleted": true}, nil)
		return
	}
	if r.Method != http.MethodPatch {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	var request struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		MetadataURI  string   `json:"metadata_uri"`
		Enabled      *bool    `json:"enabled"`
	}
	if !decodeV2(w, r, &request) {
		return
	}
	validated, err := s.newOAuthClient(request.ClientName, request.RedirectURIs)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	item.Name, item.RedirectURIs = validated.Name, validated.RedirectURIs
	if request.Enabled != nil {
		item.Enabled = *request.Enabled
	}
	if metadataURI := strings.TrimSpace(request.MetadataURI); metadataURI != "" && metadataURI != item.MetadataURI {
		metadata, fetchErr := s.fetchClientMetadata(r.Context(), metadataURI)
		if fetchErr != nil {
			v2HandleError(w, r, fetchErr)
			return
		}
		item.MetadataURI = metadataURI
		item.MetadataHash = metadata.hash
		item.MetadataETag = metadata.etag
		item.MetadataFetchedAt = metadata.fetchedAt
		item.IdentityType = "cimd"
		item.RedirectURIs = metadata.redirectURIs
	}
	if err := s.store.UpdateOAuthClient(r.Context(), item); err != nil {
		v2HandleError(w, r, err)
		return
	}
	s.auditOAuthEvent(r, oauthAuditActor(currentUser(r)), "oauth_client_updated", "oauth_client", id, map[string]any{"client_name": boundedOAuthAuditValue(item.Name), "redirect_uris": item.RedirectURIs, "enabled": item.Enabled})
	v2Write(w, r, http.StatusOK, item, nil)
}
