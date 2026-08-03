package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	aiModelDiscoveryCapacity  = 8
	aiModelDiscoveryTimeout   = 25 * time.Second
	aiModelLeaseWait          = 10 * time.Second
	aiModelLimit              = 1000
	aiModelIDLimit            = 512
	aiProviderBaseURLLimit    = 2048
	aiProviderCredentialLimit = 8192

	aiProviderFormatChatCompletions = "chat_completions"
	aiProviderFormatResponses       = "responses"
)

var (
	errAIModelDiscoveryBusy    = errors.New("AI model discovery queue is full")
	errAIModelDiscoveryMissing = errors.New("AI model discovery request not found")
)

type aiModelDiscoveryResult struct {
	models []string
	err    error
	detail string
}

type aiTaskEntry[Request any, Result any] struct {
	request  Request
	id       string
	workerID string
	result   chan Result
}

type aiTaskQueue[Request any, Result any] struct {
	mu      sync.Mutex
	pending []*aiTaskEntry[Request, Result]
	active  map[string]*aiTaskEntry[Request, Result]
	wake    chan struct{}
}

func newAITaskQueue[Request any, Result any]() *aiTaskQueue[Request, Result] {
	return &aiTaskQueue[Request, Result]{active: map[string]*aiTaskEntry[Request, Result]{}, wake: make(chan struct{}, 1)}
}

type aiModelDiscoveryQueue = aiTaskQueue[airpc.ModelDiscoveryRequest, aiModelDiscoveryResult]

func newAIModelDiscoveryQueue() *aiModelDiscoveryQueue {
	return newAITaskQueue[airpc.ModelDiscoveryRequest, aiModelDiscoveryResult]()
}

func (q *aiTaskQueue[Request, Result]) submit(request Request, id string) (*aiTaskEntry[Request, Result], error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending)+len(q.active) >= aiModelDiscoveryCapacity {
		return nil, errAIModelDiscoveryBusy
	}
	entry := &aiTaskEntry[Request, Result]{request: request, id: id, result: make(chan Result, 1)}
	q.pending = append(q.pending, entry)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return entry, nil
}

func (q *aiTaskQueue[Request, Result]) lease(ctx context.Context, workerID string) (*Request, error) {
	timer := time.NewTimer(aiModelLeaseWait)
	defer timer.Stop()
	for {
		q.mu.Lock()
		if len(q.pending) > 0 {
			entry := q.pending[0]
			q.pending = q.pending[1:]
			entry.workerID = workerID
			q.active[entry.id] = entry
			request := entry.request
			q.mu.Unlock()
			return &request, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		case <-q.wake:
		}
	}
}

func (q *aiTaskQueue[Request, Result]) finish(id, workerID string, result Result) (Request, error) {
	q.mu.Lock()
	entry, ok := q.active[id]
	if !ok || entry.workerID != workerID {
		q.mu.Unlock()
		var zero Request
		return zero, errAIModelDiscoveryMissing
	}
	delete(q.active, id)
	q.mu.Unlock()
	entry.result <- result
	return entry.request, nil
}

func (q *aiTaskQueue[Request, Result]) cancel(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index, entry := range q.pending {
		if entry.id == id {
			q.pending = append(q.pending[:index], q.pending[index+1:]...)
			return
		}
	}
	delete(q.active, id)
}

func normalizeAIProviderBaseURL(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if len(value) == 0 || len(value) > aiProviderBaseURLLimit {
		return "", errors.New("invalid AI Provider base URL")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost")) {
		return "", errors.New("invalid AI Provider base URL")
	}
	return value, nil
}

func normalizeAIModelIDs(models []string) ([]string, error) {
	if len(models) > aiModelLimit {
		return nil, errors.New("too many AI models")
	}
	unique := make(map[string]struct{}, len(models))
	for _, raw := range models {
		modelID := strings.TrimSpace(raw)
		if modelID == "" || len(modelID) > aiModelIDLimit {
			return nil, errors.New("invalid AI model ID")
		}
		unique[modelID] = struct{}{}
	}
	if len(unique) == 0 {
		return nil, errors.New("AI Provider returned no models")
	}
	result := make([]string, 0, len(unique))
	for modelID := range unique {
		result = append(result, modelID)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeAIProviderFormat(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case aiProviderFormatResponses:
		return aiProviderFormatResponses
	default:
		return aiProviderFormatChatCompletions
	}
}

func normalizeErrorDetail(detail json.RawMessage) json.RawMessage {
	var object map[string]any
	if len(detail) == 0 || json.Unmarshal(detail, &object) != nil {
		return nil
	}
	if len(detail) > 96<<10 {
		return nil
	}
	return detail
}

func (s *Server) apiV2AIProviderModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	var request struct {
		ProviderID string `json:"provider_id"`
		BaseURL    string `json:"base_url"`
		APIKey     string `json:"api_key"`
		APIFormat  string `json:"api_format"`
	}
	if !decodeV2(w, r, &request) {
		return
	}
	baseURL, err := normalizeAIProviderBaseURL(request.BaseURL)
	if err != nil {
		v2Error(w, r, http.StatusBadRequest, "invalid_provider_url", "Base URL 必须是有效的 HTTPS 版本根端点；本机服务可使用 HTTP loopback 地址")
		return
	}
	credential := strings.TrimSpace(request.APIKey)
	if credential == "" && strings.TrimSpace(request.ProviderID) != "" {
		provider, loadErr := s.store.GetAIProvider(r.Context(), strings.TrimSpace(request.ProviderID))
		if loadErr != nil || provider.CredentialEncrypted == "" {
			v2Error(w, r, http.StatusBadRequest, "provider_credential_missing", "该 Provider 没有可用的 API Key")
			return
		}
		credential, err = security.DecryptSecret(s.sessionSecret, "ai-provider-credential:"+provider.ID, provider.CredentialEncrypted)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
	}
	if credential == "" {
		v2Error(w, r, http.StatusBadRequest, "provider_credential_missing", "请先填写 API Key")
		return
	}
	if len(credential) > aiProviderCredentialLimit {
		v2Error(w, r, http.StatusBadRequest, "provider_credential_invalid", "API Key 长度无效")
		return
	}
	random, err := security.RandomToken(18)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	requestID := "aim_" + random
	entry, err := s.aiModelDiscoveries.submit(airpc.ModelDiscoveryRequest{ID: requestID, BaseURL: baseURL, APIFormat: normalizeAIProviderFormat(request.APIFormat), APIKey: credential}, requestID)
	if errors.Is(err, errAIModelDiscoveryBusy) {
		v2Error(w, r, http.StatusTooManyRequests, "provider_models_busy", "模型拉取请求较多，请稍后重试")
		return
	}
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	defer s.aiModelDiscoveries.cancel(entry.request.ID)
	timer := time.NewTimer(s.aiModelDiscoveryTimeout)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
		v2Error(w, r, http.StatusServiceUnavailable, "provider_models_unavailable", "AI Worker 未及时返回模型列表，请确认服务正在运行")
	case result := <-entry.result:
		if result.err != nil {
			message := "无法从 Provider 拉取模型，请检查 Base URL、API Key 和服务兼容性"
			if result.detail != "" {
				message += "；" + result.detail
			}
			v2Error(w, r, http.StatusBadGateway, "provider_models_failed", message)
			return
		}
		v2Write(w, r, http.StatusOK, map[string]any{"models": result.models}, map[string]any{"count": len(result.models)})
	}
}
