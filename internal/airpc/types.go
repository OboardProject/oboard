package airpc

import (
	"encoding/json"

	"github.com/OboardProject/oboard/internal/model"
)

type LeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type Provider struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	ProviderKind    string            `json:"provider_kind"`
	Model           string            `json:"model"`
	RoutingStrategy string            `json:"routing_strategy"`
	AllowRawAudit   bool              `json:"allow_raw_audit"`
	Endpoints       []RuntimeEndpoint `json:"endpoints"`

	BaseURL    string                      `json:"base_url,omitempty"`
	APIFormat  string                      `json:"api_format,omitempty"`
	APIKey     string                      `json:"api_key,omitempty"`
	Capability *model.AIProviderCapability `json:"capability,omitempty"`
}

type RuntimeEndpoint struct {
	ID                  string                      `json:"id"`
	Name                string                      `json:"name"`
	BaseURL             string                      `json:"base_url"`
	APIStyle            string                      `json:"api_style"`
	AuthMode            string                      `json:"auth_mode"`
	Credential          string                      `json:"credential"`
	AnthropicVersion    string                      `json:"anthropic_version,omitempty"`
	Headers             map[string]string           `json:"headers,omitempty"`
	ModelsPath          string                      `json:"models_path,omitempty"`
	GeneratePath        string                      `json:"generate_path,omitempty"`
	ModelOverride       string                      `json:"model_override,omitempty"`
	Priority            int                         `json:"priority"`
	Enabled             bool                        `json:"enabled"`
	TimeoutMS           int                         `json:"timeout_ms"`
	MaxRetries          int                         `json:"max_retries"`
	AllowPrivateNetwork bool                        `json:"allow_private_network"`
	Capability          *model.AIProviderCapability `json:"capability,omitempty"`
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
	Route        *RouteEvidence  `json:"route,omitempty"`
}

type RouteEvidence struct {
	ProviderID               string `json:"provider_id"`
	EndpointID               string `json:"endpoint_id"`
	APIStyle                 string `json:"api_style"`
	Model                    string `json:"model"`
	CapabilityProfileVersion string `json:"capability_profile_version"`
	CapabilityConfigDigest   string `json:"capability_config_digest"`
	AttemptCount             int    `json:"attempt_count"`
	ProviderRequestID        string `json:"provider_request_id,omitempty"`
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	FinishReason             string `json:"finish_reason"`
	LatencyMS                int64  `json:"latency_ms"`
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
	ID         string          `json:"id"`
	ProviderID string          `json:"provider_id,omitempty"`
	Endpoint   RuntimeEndpoint `json:"endpoint"`
	BaseURL    string          `json:"base_url"`
	APIFormat  string          `json:"api_format"`
	APIKey     string          `json:"api_key"`
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
	ID           string          `json:"id"`
	ProviderID   string          `json:"provider_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	BaseURL      string          `json:"base_url"`
	APIFormat    string          `json:"api_format"`
	APIKey       string          `json:"api_key"`
	Model        string          `json:"model"`
	ProviderKind string          `json:"provider_kind,omitempty"`
	Endpoint     RuntimeEndpoint `json:"endpoint"`
}

type AITestLeaseResponse struct {
	Request *AITestRequest `json:"request,omitempty"`
}

type AITestCompleteRequest struct {
	WorkerID     string                      `json:"worker_id"`
	OK           bool                        `json:"ok"`
	Error        string                      `json:"error,omitempty"`
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
