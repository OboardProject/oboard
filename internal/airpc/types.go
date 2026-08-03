package airpc

import "github.com/OboardProject/oboard/internal/model"

type LeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type Provider struct {
	ID            string `json:"id"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	APIFormat     string `json:"api_format"`
	APIKey        string `json:"api_key"`
	AllowRawAudit bool   `json:"allow_raw_audit"`
}

type LeaseResponse struct {
	Job      *model.AuditReviewJob `json:"job,omitempty"`
	Provider *Provider             `json:"provider,omitempty"`
}

type CompleteRequest struct {
	WorkerID     string                  `json:"worker_id"`
	Report       model.AuditReviewReport `json:"report"`
	InputTokens  int64                   `json:"input_tokens"`
	OutputTokens int64                   `json:"output_tokens"`
}

type FailRequest struct {
	WorkerID string `json:"worker_id"`
	Error    string `json:"error"`
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
