package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type aiProviderRequest struct {
	Name            *string             `json:"name"`
	ProviderKind    *string             `json:"provider_kind"`
	DefaultModel    *string             `json:"default_model"`
	RoutingStrategy *string             `json:"routing_strategy"`
	Enabled         *bool               `json:"enabled"`
	AllowRawAudit   *bool               `json:"allow_raw_audit"`
	DailyTokenLimit *int64              `json:"daily_token_limit"`
	Endpoints       []aiEndpointRequest `json:"endpoints"`

	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIFormat string `json:"api_format"`
	APIKey    string `json:"api_key"`
}

type aiEndpointRequest struct {
	Name                *string           `json:"name"`
	BaseURL             *string           `json:"base_url"`
	APIStyle            *string           `json:"api_style"`
	AuthMode            *string           `json:"auth_mode"`
	APIKey              *string           `json:"api_key"`
	RemoveCredential    bool              `json:"remove_credential"`
	AnthropicVersion    *string           `json:"anthropic_version"`
	Headers             map[string]string `json:"headers"`
	ModelsPath          *string           `json:"models_path"`
	GeneratePath        *string           `json:"generate_path"`
	ModelOverride       *string           `json:"model_override"`
	Priority            *int              `json:"priority"`
	Enabled             *bool             `json:"enabled"`
	TimeoutMS           *int              `json:"timeout_ms"`
	MaxRetries          *int              `json:"max_retries"`
	AllowPrivateNetwork *bool             `json:"allow_private_network"`
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
		var request aiProviderRequest
		if !decodeV2(w, r, &request) {
			return
		}
		item, endpointRequests, err := normalizeAIProviderCreate(request)
		if err != nil {
			v2Error(w, r, http.StatusBadRequest, "invalid_provider", err.Error())
			return
		}
		random, err := security.RandomToken(18)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		item.ID = "aip_" + random
		if err := s.store.CreateAIProvider(r.Context(), item); err != nil {
			v2HandleError(w, r, err)
			return
		}
		for _, endpointRequest := range endpointRequests {
			if _, err := s.createAIEndpoint(r, item, endpointRequest); err != nil {
				_ = s.store.DeleteAIProvider(r.Context(), item.ID)
				v2Error(w, r, http.StatusBadRequest, "invalid_provider_endpoint", err.Error())
				return
			}
		}
		stored, err := s.store.GetAIProvider(r.Context(), item.ID)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		stored.CredentialEncrypted = ""
		v2Write(w, r, http.StatusCreated, stored, nil)
	default:
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func normalizeAIProviderCreate(request aiProviderRequest) (*model.AIProvider, []aiEndpointRequest, error) {
	name := ""
	if request.Name != nil {
		name = strings.TrimSpace(*request.Name)
	}
	kind := "openai"
	if request.ProviderKind != nil {
		kind = strings.ToLower(strings.TrimSpace(*request.ProviderKind))
	}
	modelID := ""
	if request.DefaultModel != nil {
		modelID = strings.TrimSpace(*request.DefaultModel)
	}
	if modelID == "" {
		modelID = strings.TrimSpace(request.Model)
	}
	routing := "ordered_failover"
	if request.RoutingStrategy != nil {
		routing = strings.TrimSpace(*request.RoutingStrategy)
	}
	if name == "" || modelID == "" || len(modelID) > aiModelIDLimit || !oneOf(kind, "openai", "anthropic", "custom") || routing != "ordered_failover" {
		return nil, nil, errors.New("Provider 名称、厂商、默认模型或路由策略无效")
	}
	daily := int64(0)
	if request.DailyTokenLimit != nil {
		daily = *request.DailyTokenLimit
	}
	if daily < 0 {
		return nil, nil, errors.New("每日 Token 限额不能为负数")
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	raw := false
	if request.AllowRawAudit != nil {
		raw = *request.AllowRawAudit
	}
	item := &model.AIProvider{Name: name, ProviderKind: kind, DefaultModel: modelID, RoutingStrategy: routing, Enabled: enabled, AllowRawAudit: raw, DailyTokenLimit: daily}
	endpoints := request.Endpoints
	if len(endpoints) == 0 && strings.TrimSpace(request.BaseURL) != "" {
		style := legacyAPIStyleController(request.APIFormat)
		auth := aiprovider.AuthModeBearer
		endpointName := "Primary"
		base := request.BaseURL
		key := request.APIKey
		priority := 100
		endpointEnabled := true
		allowPrivate := isLoopbackURL(base)
		endpoints = []aiEndpointRequest{{Name: &endpointName, BaseURL: &base, APIStyle: &style, AuthMode: &auth, APIKey: &key, Priority: &priority, Enabled: &endpointEnabled, AllowPrivateNetwork: &allowPrivate}}
		item.BaseURL, item.Model, item.APIFormat = request.BaseURL, modelID, normalizeAIProviderFormat(request.APIFormat)
	}
	return item, endpoints, nil
}

func (s *Server) apiV2AIProvider(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v2/ai/providers/")
	if len(parts) == 0 || parts[0] == "" {
		v2Error(w, r, http.StatusBadRequest, "invalid_id", "缺少 Provider ID")
		return
	}
	provider, err := s.store.GetAIProvider(r.Context(), parts[0])
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if len(parts) == 1 {
		s.manageAIProvider(w, r, provider)
		return
	}
	if len(parts) == 2 && parts[1] == "endpoints" && r.Method == http.MethodPost {
		var request aiEndpointRequest
		if !decodeV2(w, r, &request) {
			return
		}
		endpoint, err := s.createAIEndpoint(r, provider, request)
		if err != nil {
			v2Error(w, r, http.StatusBadRequest, "invalid_provider_endpoint", err.Error())
			return
		}
		v2Write(w, r, http.StatusCreated, endpoint, nil)
		return
	}
	if len(parts) == 3 && parts[1] == "endpoints" {
		s.manageAIEndpoint(w, r, provider, parts[2])
		return
	}
	v2Error(w, r, http.StatusNotFound, "not_found", "AI Provider 操作不存在")
}

func (s *Server) manageAIProvider(w http.ResponseWriter, r *http.Request, item *model.AIProvider) {
	switch r.Method {
	case http.MethodGet:
		item.CredentialEncrypted = ""
		v2Write(w, r, http.StatusOK, item, nil)
	case http.MethodDelete:
		if err := s.store.DeleteAIProvider(r.Context(), item.ID); err != nil {
			v2HandleError(w, r, errors.New("已产生分析记录的 Provider 只能禁用，不能删除"))
			return
		}
		v2Write(w, r, http.StatusOK, map[string]bool{"deleted": true}, nil)
	case http.MethodPatch:
		var request aiProviderRequest
		if !decodeV2(w, r, &request) {
			return
		}
		if request.Name != nil {
			item.Name = strings.TrimSpace(*request.Name)
		}
		if request.ProviderKind != nil {
			item.ProviderKind = strings.ToLower(strings.TrimSpace(*request.ProviderKind))
		}
		if request.DefaultModel != nil {
			item.DefaultModel = strings.TrimSpace(*request.DefaultModel)
		}
		if request.RoutingStrategy != nil {
			item.RoutingStrategy = strings.TrimSpace(*request.RoutingStrategy)
		}
		if request.Enabled != nil {
			item.Enabled = *request.Enabled
		}
		if request.AllowRawAudit != nil {
			item.AllowRawAudit = *request.AllowRawAudit
		}
		if request.DailyTokenLimit != nil {
			item.DailyTokenLimit = *request.DailyTokenLimit
		}
		if item.Name == "" || item.DefaultModel == "" || !oneOf(item.ProviderKind, "openai", "anthropic", "custom") || item.RoutingStrategy != "ordered_failover" || item.DailyTokenLimit < 0 {
			v2Error(w, r, http.StatusBadRequest, "invalid_provider", "AI Provider 配置无效")
			return
		}
		if err := s.store.UpdateAIProvider(r.Context(), item); err != nil {
			v2HandleError(w, r, err)
			return
		}
		stored, _ := s.store.GetAIProvider(r.Context(), item.ID)
		stored.CredentialEncrypted = ""
		v2Write(w, r, http.StatusOK, stored, nil)
	default:
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func (s *Server) createAIEndpoint(r *http.Request, provider *model.AIProvider, request aiEndpointRequest) (*model.AIProviderEndpoint, error) {
	if provider.ProviderKind == "custom" && (request.APIStyle == nil || strings.TrimSpace(*request.APIStyle) == "") {
		return nil, errors.New("Custom Provider 必须显式选择 API Style")
	}
	random, err := security.RandomToken(18)
	if err != nil {
		return nil, err
	}
	endpoint := &model.AIProviderEndpoint{ID: "aipe_" + random, ProviderID: provider.ID, Priority: 100, Enabled: true, TimeoutMS: 60000, MaxRetries: 2, HeadersJSON: "{}"}
	applyAIEndpointRequest(endpoint, request)
	applyEndpointTemplate(endpoint, provider.ProviderKind)
	if err := validateAIEndpoint(endpoint); err != nil {
		return nil, err
	}
	if request.APIKey != nil && strings.TrimSpace(*request.APIKey) != "" {
		endpoint.CredentialEncrypted, err = security.EncryptSecret(s.sessionSecret, "ai-provider-endpoint-credential:"+endpoint.ID, strings.TrimSpace(*request.APIKey))
		if err != nil {
			return nil, err
		}
	}
	if err := s.store.CreateAIProviderEndpoint(r.Context(), endpoint); err != nil {
		return nil, err
	}
	endpoint.CredentialEncrypted = ""
	return endpoint, nil
}

func (s *Server) manageAIEndpoint(w http.ResponseWriter, r *http.Request, provider *model.AIProvider, endpointID string) {
	endpoint, err := s.store.GetAIProviderEndpoint(r.Context(), provider.ID, endpointID)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := s.store.DeleteAIProviderEndpoint(r.Context(), provider.ID, endpointID); err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, http.StatusOK, map[string]bool{"deleted": true}, nil)
	case http.MethodPatch:
		var request aiEndpointRequest
		if !decodeV2(w, r, &request) {
			return
		}
		if request.RemoveCredential && request.APIKey != nil && strings.TrimSpace(*request.APIKey) != "" {
			v2Error(w, r, http.StatusBadRequest, "invalid_provider_endpoint", "不能同时移除和替换 Credential")
			return
		}
		applyAIEndpointRequest(endpoint, request)
		if request.RemoveCredential {
			endpoint.CredentialEncrypted = ""
		} else if request.APIKey != nil && strings.TrimSpace(*request.APIKey) != "" {
			endpoint.CredentialEncrypted, err = security.EncryptSecret(s.sessionSecret, "ai-provider-endpoint-credential:"+endpoint.ID, strings.TrimSpace(*request.APIKey))
			if err != nil {
				v2HandleError(w, r, err)
				return
			}
		}
		if err := validateAIEndpoint(endpoint); err != nil {
			v2Error(w, r, http.StatusBadRequest, "invalid_provider_endpoint", err.Error())
			return
		}
		if err := s.store.UpdateAIProviderEndpoint(r.Context(), endpoint); err != nil {
			v2HandleError(w, r, err)
			return
		}
		stored, _ := s.store.GetAIProviderEndpoint(r.Context(), provider.ID, endpoint.ID)
		stored.CredentialEncrypted = ""
		v2Write(w, r, http.StatusOK, stored, nil)
	default:
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func applyAIEndpointRequest(endpoint *model.AIProviderEndpoint, request aiEndpointRequest) {
	if request.Name != nil {
		endpoint.Name = strings.TrimSpace(*request.Name)
	}
	if request.BaseURL != nil {
		endpoint.BaseURL = strings.TrimSpace(*request.BaseURL)
	}
	if request.APIStyle != nil {
		endpoint.APIStyle = strings.TrimSpace(*request.APIStyle)
	}
	if request.AuthMode != nil {
		endpoint.AuthMode = strings.TrimSpace(*request.AuthMode)
	}
	if request.AnthropicVersion != nil {
		endpoint.AnthropicVersion = strings.TrimSpace(*request.AnthropicVersion)
	}
	if request.Headers != nil {
		encoded, _ := json.Marshal(request.Headers)
		endpoint.HeadersJSON = string(encoded)
	}
	if request.ModelsPath != nil {
		endpoint.ModelsPath = strings.TrimSpace(*request.ModelsPath)
	}
	if request.GeneratePath != nil {
		endpoint.GeneratePath = strings.TrimSpace(*request.GeneratePath)
	}
	if request.ModelOverride != nil {
		endpoint.ModelOverride = strings.TrimSpace(*request.ModelOverride)
	}
	if request.Priority != nil {
		endpoint.Priority = *request.Priority
	}
	if request.Enabled != nil {
		endpoint.Enabled = *request.Enabled
	}
	if request.TimeoutMS != nil {
		endpoint.TimeoutMS = *request.TimeoutMS
	}
	if request.MaxRetries != nil {
		endpoint.MaxRetries = *request.MaxRetries
	}
	if request.AllowPrivateNetwork != nil {
		endpoint.AllowPrivateNetwork = *request.AllowPrivateNetwork
	}
}

func applyEndpointTemplate(endpoint *model.AIProviderEndpoint, providerKind string) {
	if endpoint.Name == "" {
		endpoint.Name = "Primary"
	}
	if endpoint.APIStyle == "" {
		if providerKind == "anthropic" {
			endpoint.APIStyle = string(aiprovider.APIStyleAnthropicMessages)
		} else {
			endpoint.APIStyle = string(aiprovider.APIStyleOpenAIResponses)
		}
	}
	if endpoint.BaseURL == "" {
		if endpoint.APIStyle == string(aiprovider.APIStyleAnthropicMessages) {
			endpoint.BaseURL = "https://api.anthropic.com/v1"
		} else {
			endpoint.BaseURL = "https://api.openai.com/v1"
		}
	}
	if endpoint.AuthMode == "" {
		if endpoint.APIStyle == string(aiprovider.APIStyleAnthropicMessages) {
			endpoint.AuthMode = aiprovider.AuthModeXAPIKey
		} else {
			endpoint.AuthMode = aiprovider.AuthModeBearer
		}
	}
	if endpoint.APIStyle == string(aiprovider.APIStyleAnthropicMessages) && endpoint.AnthropicVersion == "" {
		endpoint.AnthropicVersion = "2023-06-01"
	}
}

func validateAIEndpoint(endpoint *model.AIProviderEndpoint) error {
	if endpoint.Name == "" || !oneOf(endpoint.APIStyle, string(aiprovider.APIStyleOpenAIResponses), string(aiprovider.APIStyleOpenAIChatCompletions), string(aiprovider.APIStyleAnthropicMessages)) || !oneOf(endpoint.AuthMode, aiprovider.AuthModeBearer, aiprovider.AuthModeXAPIKey, aiprovider.AuthModeNone) || endpoint.Priority < 1 || endpoint.Priority > 1_000_000 || endpoint.TimeoutMS < 1000 || endpoint.TimeoutMS > 600_000 || endpoint.MaxRetries < 0 || endpoint.MaxRetries > 10 {
		return errors.New("Endpoint 名称、协议、认证、优先级或超时重试配置无效")
	}
	normalized, err := normalizeAIEndpointURL(endpoint.BaseURL, endpoint.AllowPrivateNetwork)
	if err != nil {
		return err
	}
	endpoint.BaseURL = normalized
	headers := map[string]string{}
	if json.Unmarshal([]byte(endpoint.HeadersJSON), &headers) != nil {
		return errors.New("自定义 Header 必须是字符串对象")
	}
	dummy, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL, nil) // #nosec G704 -- this request is never sent; it only validates the configured headers.
	if err := aiprovider.ApplyHeaders(dummy, aiprovider.RuntimeEndpoint{AuthMode: aiprovider.AuthModeNone, Headers: headers}); err != nil {
		return err
	}
	modelsPath, generatePath := aiprovider.DefaultPaths(aiprovider.APIStyle(endpoint.APIStyle))
	if endpoint.ModelsPath != "" {
		modelsPath = endpoint.ModelsPath
	}
	if endpoint.GeneratePath != "" {
		generatePath = endpoint.GeneratePath
	}
	if _, err := aiprovider.ResolveEndpointURL(endpoint.BaseURL, modelsPath); err != nil {
		return err
	}
	if _, err := aiprovider.ResolveEndpointURL(endpoint.BaseURL, generatePath); err != nil {
		return err
	}
	return nil
}

func normalizeAIEndpointURL(raw string, allowPrivate bool) (string, error) {
	value, err := aiprovider.NormalizeEndpointBaseURL(raw, allowPrivate)
	if err != nil {
		return "", errors.New("Base URL 无效：" + err.Error())
	}
	return value, nil
}
func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
func legacyAPIStyleController(value string) string {
	if normalizeAIProviderFormat(value) == aiProviderFormatResponses {
		return string(aiprovider.APIStyleOpenAIResponses)
	}
	return string(aiprovider.APIStyleOpenAIChatCompletions)
}

func (s *Server) resolveAIRequestEndpoint(ctx context.Context, providerID, endpointID, providerKind string, draft *aiEndpointRequest, legacyBaseURL, legacyFormat, legacyKey string) (*model.AIProvider, airpc.RuntimeEndpoint, error) {
	if strings.TrimSpace(providerID) != "" {
		provider, err := s.store.GetAIProvider(ctx, strings.TrimSpace(providerID))
		if err != nil {
			return nil, airpc.RuntimeEndpoint{}, err
		}
		if len(provider.Endpoints) > 0 {
			selected := provider.Endpoints[0]
			if endpointID != "" {
				found := false
				for _, candidate := range provider.Endpoints {
					if candidate.ID == endpointID {
						selected = candidate
						found = true
						break
					}
				}
				if !found {
					return nil, airpc.RuntimeEndpoint{}, errors.New("Endpoint 不属于指定 Provider")
				}
			}
			runtimeEndpoints, err := s.runtimeAIEndpoints(provider)
			if err != nil {
				return nil, airpc.RuntimeEndpoint{}, err
			}
			for _, candidate := range runtimeEndpoints {
				if candidate.ID == selected.ID {
					if draft != nil {
						draftEndpoint := selected
						applyAIEndpointRequest(&draftEndpoint, *draft)
						if err := validateAIEndpoint(&draftEndpoint); err != nil {
							return nil, airpc.RuntimeEndpoint{}, err
						}
						headers := map[string]string{}
						if err := json.Unmarshal([]byte(draftEndpoint.HeadersJSON), &headers); err != nil {
							return nil, airpc.RuntimeEndpoint{}, err
						}
						candidate.Name = draftEndpoint.Name
						candidate.BaseURL = draftEndpoint.BaseURL
						candidate.APIStyle = draftEndpoint.APIStyle
						candidate.AuthMode = draftEndpoint.AuthMode
						candidate.AnthropicVersion = draftEndpoint.AnthropicVersion
						candidate.Headers = headers
						candidate.ModelsPath = draftEndpoint.ModelsPath
						candidate.GeneratePath = draftEndpoint.GeneratePath
						candidate.ModelOverride = draftEndpoint.ModelOverride
						candidate.Priority = draftEndpoint.Priority
						candidate.Enabled = draftEndpoint.Enabled
						candidate.TimeoutMS = draftEndpoint.TimeoutMS
						candidate.MaxRetries = draftEndpoint.MaxRetries
						candidate.AllowPrivateNetwork = draftEndpoint.AllowPrivateNetwork
						candidate.Capability = nil
						if draft.RemoveCredential {
							candidate.Credential = ""
						} else if draft.APIKey != nil && strings.TrimSpace(*draft.APIKey) != "" {
							candidate.Credential = strings.TrimSpace(*draft.APIKey)
						}
					}
					if endpointID == "" && draft == nil {
						if strings.TrimSpace(legacyBaseURL) != "" {
							candidate.BaseURL = strings.TrimRight(strings.TrimSpace(legacyBaseURL), "/")
						}
						if strings.TrimSpace(legacyKey) != "" {
							candidate.Credential = strings.TrimSpace(legacyKey)
						}
						if strings.TrimSpace(legacyFormat) != "" {
							candidate.APIStyle = legacyAPIStyleController(legacyFormat)
						}
					}
					return provider, candidate, nil
				}
			}
		}
		credential := ""
		if provider.CredentialEncrypted != "" {
			credential, err = security.DecryptSecret(s.sessionSecret, "ai-provider-credential:"+provider.ID, provider.CredentialEncrypted)
			if err != nil {
				return nil, airpc.RuntimeEndpoint{}, err
			}
		}
		baseURL := legacyBaseURL
		if baseURL == "" {
			baseURL = provider.BaseURL
		}
		format := legacyFormat
		if format == "" {
			format = provider.APIFormat
		}
		return provider, airpc.RuntimeEndpoint{ID: "legacy", Name: "Primary", BaseURL: baseURL, APIStyle: legacyAPIStyleController(format), AuthMode: aiprovider.AuthModeBearer, Credential: firstString(strings.TrimSpace(legacyKey), credential), Priority: 100, Enabled: true, TimeoutMS: 60000, MaxRetries: 2, AllowPrivateNetwork: isLoopbackURL(baseURL)}, nil
	}
	if draft != nil {
		if providerKind == "custom" && (draft.APIStyle == nil || strings.TrimSpace(*draft.APIStyle) == "") {
			return nil, airpc.RuntimeEndpoint{}, errors.New("Custom Provider 必须显式选择 API Style")
		}
		endpoint := &model.AIProviderEndpoint{ID: "draft", Name: "Draft", Priority: 100, Enabled: true, TimeoutMS: 60000, MaxRetries: 2, HeadersJSON: "{}"}
		applyAIEndpointRequest(endpoint, *draft)
		applyEndpointTemplate(endpoint, providerKind)
		if err := validateAIEndpoint(endpoint); err != nil {
			return nil, airpc.RuntimeEndpoint{}, err
		}
		headers := map[string]string{}
		_ = json.Unmarshal([]byte(endpoint.HeadersJSON), &headers)
		credential := ""
		if draft.APIKey != nil {
			credential = strings.TrimSpace(*draft.APIKey)
		}
		return nil, airpc.RuntimeEndpoint{ID: endpoint.ID, Name: endpoint.Name, BaseURL: endpoint.BaseURL, APIStyle: endpoint.APIStyle, AuthMode: endpoint.AuthMode, Credential: credential, AnthropicVersion: endpoint.AnthropicVersion, Headers: headers, ModelsPath: endpoint.ModelsPath, GeneratePath: endpoint.GeneratePath, ModelOverride: endpoint.ModelOverride, Priority: endpoint.Priority, Enabled: endpoint.Enabled, TimeoutMS: endpoint.TimeoutMS, MaxRetries: endpoint.MaxRetries, AllowPrivateNetwork: endpoint.AllowPrivateNetwork}, nil
	}
	baseURL, err := normalizeAIProviderBaseURL(legacyBaseURL)
	if err != nil {
		return nil, airpc.RuntimeEndpoint{}, err
	}
	return nil, airpc.RuntimeEndpoint{ID: "draft", Name: "Draft", BaseURL: baseURL, APIStyle: legacyAPIStyleController(legacyFormat), AuthMode: aiprovider.AuthModeBearer, Credential: strings.TrimSpace(legacyKey), Priority: 100, Enabled: true, TimeoutMS: 60000, MaxRetries: 2, AllowPrivateNetwork: isLoopbackURL(baseURL)}, nil
}
func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func isLoopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	return strings.EqualFold(parsed.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
}
