package aiprovider

import (
	"context"
	"encoding/json"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type APIStyle string

const (
	APIStyleOpenAIResponses       APIStyle = "openai_responses"
	APIStyleOpenAIChatCompletions APIStyle = "openai_chat_completions"
	APIStyleAnthropicMessages     APIStyle = "anthropic_messages"

	AuthModeBearer  = "bearer"
	AuthModeXAPIKey = "x_api_key"
	AuthModeNone    = "none"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	RequestID       string
	Model           string
	System          string
	Messages        []Message
	MaxOutputTokens int
	Temperature     *float64
	Schema          map[string]any
	OutputMode      string
	Stream          bool
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type Response struct {
	Text              string          `json:"text"`
	Structured        json.RawMessage `json:"structured,omitempty"`
	Usage             Usage           `json:"usage"`
	FinishReason      string          `json:"finish_reason"`
	RawFinishReason   string          `json:"raw_finish_reason,omitempty"`
	ProviderRequestID string          `json:"provider_request_id,omitempty"`
	ProviderID        string          `json:"provider_id"`
	EndpointID        string          `json:"endpoint_id"`
	APIStyle          APIStyle        `json:"api_style"`
	Model             string          `json:"model"`
	Latency           time.Duration   `json:"-"`
	AttemptCount      int             `json:"attempt_count"`
}

type ModelInfo struct {
	ID string `json:"id"`
}

type StreamEvent struct {
	Delta             string
	Usage             *Usage
	FinishReason      string
	ProviderRequestID string
}

type RuntimeEndpoint struct {
	ID                  string
	ProviderID          string
	Name                string
	BaseURL             string
	APIStyle            APIStyle
	AuthMode            string
	Credential          string
	AnthropicVersion    string
	Headers             map[string]string
	ModelsPath          string
	GeneratePath        string
	ModelOverride       string
	Priority            int
	Enabled             bool
	TimeoutMS           int
	MaxRetries          int
	AllowPrivateNetwork bool
	Capability          *model.AIProviderCapability
}

type RuntimeProvider struct {
	ID              string
	Name            string
	ProviderKind    string
	Model           string
	RoutingStrategy string
	AllowRawAudit   bool
	Endpoints       []RuntimeEndpoint
}

type Client interface {
	ListModels(context.Context, RuntimeEndpoint) ([]ModelInfo, error)
	Complete(context.Context, RuntimeEndpoint, Request) (*Response, error)
	Stream(context.Context, RuntimeEndpoint, Request, func(StreamEvent) error) error
}

type Registry map[APIStyle]Client

func (r Registry) Client(style APIStyle) (Client, error) {
	client, ok := r[style]
	if !ok {
		return nil, NewError(ErrorUnsupportedFeature, false, 0, "unsupported AI API style", nil)
	}
	return client, nil
}
