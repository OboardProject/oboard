package airpc

import (
	"encoding/json"

	"github.com/OboardProject/oboard/internal/model"
)

type LeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type Provider struct {
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	BaseURL       string                      `json:"base_url"`
	Model         string                      `json:"model"`
	APIFormat     string                      `json:"api_format"`
	APIKey        string                      `json:"api_key"`
	AllowRawAudit bool                        `json:"allow_raw_audit"`
	Capability    *model.AIProviderCapability `json:"capability,omitempty"`
}

type LeaseResponse struct {
	Job      *model.AuditReviewJob `json:"job,omitempty"`
	Provider *Provider             `json:"provider,omitempty"`
}

type CompleteRequest struct {
	WorkerID     string          `json:"worker_id"`
	Output       json.RawMessage `json:"output"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
}

type FailRequest struct {
	WorkerID    string          `json:"worker_id"`
	Error       string          `json:"error"`
	ErrorDetail json.RawMessage `json:"error_detail,omitempty"`
}

type ProviderLog struct {
	Provider        string            `json:"provider,omitempty"`
	Model           string            `json:"model,omitempty"`
	APIFormat       string            `json:"api_format,omitempty"`
	RequestMethod   string            `json:"request_method,omitempty"`
	RequestURL      string            `json:"request_url,omitempty"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	RequestBody     string            `json:"request_body,omitempty"`
	Status          int               `json:"status,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body,omitempty"`
}

type ModelDiscoveryLeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type ModelDiscoveryRequest struct {
	ID        string `json:"id"`
	BaseURL   string `json:"base_url"`
	APIFormat string `json:"api_format"`
	APIKey    string `json:"api_key"`
}

type ModelDiscoveryLeaseResponse struct {
	Request *ModelDiscoveryRequest `json:"request,omitempty"`
}

type ModelDiscoveryCompleteRequest struct {
	WorkerID string   `json:"worker_id"`
	Models   []string `json:"models"`
}

type ModelDiscoveryFailRequest struct {
	WorkerID string `json:"worker_id"`
	Error    string `json:"error"`
}

type AITestLeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type AITestRequest struct {
	ID         string `json:"id"`
	ProviderID string `json:"provider_id,omitempty"`
	Name       string `json:"name,omitempty"`
	BaseURL    string `json:"base_url"`
	APIFormat  string `json:"api_format"`
	APIKey     string `json:"api_key"`
	Model      string `json:"model"`
}

type AITestLeaseResponse struct {
	Request *AITestRequest `json:"request,omitempty"`
}

type AITestCompleteRequest struct {
	WorkerID     string                      `json:"worker_id"`
	RequestJSON  string                      `json:"request_json"`
	ResponseJSON string                      `json:"response_json"`
	StatusCode   int                         `json:"status_code"`
	DurationMS   int64                       `json:"duration_ms"`
	Content      string                      `json:"content,omitempty"`
	Capability   *model.AIProviderCapability `json:"capability,omitempty"`
}

type AITestFailRequest struct {
	WorkerID     string `json:"worker_id"`
	Error        string `json:"error"`
	RequestJSON  string `json:"request_json,omitempty"`
	ResponseJSON string `json:"response_json,omitempty"`
	StatusCode   int    `json:"status_code,omitempty"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
}
