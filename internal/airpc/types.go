package airpc

import (
	"encoding/json"

	"github.com/OboardProject/oboard/internal/model"
)

type LeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

type Provider struct {
	ID            string `json:"id"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	APIKey        string `json:"api_key"`
	AllowRawAudit bool   `json:"allow_raw_audit"`
}

type LeaseResponse struct {
	Job      *model.AIAnalysisJob `json:"job,omitempty"`
	Provider *Provider            `json:"provider,omitempty"`
}

type CompleteRequest struct {
	WorkerID     string          `json:"worker_id"`
	Finding      model.AIFinding `json:"finding"`
	RawOutput    json.RawMessage `json:"raw_output"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
}

type FailRequest struct {
	WorkerID string `json:"worker_id"`
	Error    string `json:"error"`
}
