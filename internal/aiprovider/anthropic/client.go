package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
)

type Client struct{ httpClient *http.Client }

func NewMessagesClient(client *http.Client) *Client { return &Client{httpClient: client} }

func (c *Client) ListModels(ctx context.Context, endpoint aiprovider.RuntimeEndpoint) ([]aiprovider.ModelInfo, error) {
	modelsPath, _ := aiprovider.DefaultPaths(aiprovider.APIStyleAnthropicMessages)
	if endpoint.ModelsPath != "" {
		modelsPath = endpoint.ModelsPath
	}
	base, err := aiprovider.ResolveEndpointURL(endpoint.BaseURL, modelsPath)
	if err != nil {
		return nil, err
	}
	unique := map[string]struct{}{}
	afterID := ""
	for page := 0; page < 20; page++ {
		target := *base
		query := url.Values{}
		query.Set("limit", "100")
		if afterID != "" {
			query.Set("after_id", afterID)
		}
		target.RawQuery = query.Encode()
		requestCtx, cancel := aiprovider.EndpointContext(ctx, endpoint)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
		if err != nil {
			cancel()
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		if err := applyAnthropicHeaders(request, endpoint); err != nil {
			cancel()
			return nil, aiprovider.NewError(aiprovider.ErrorInvalidRequest, false, 0, err.Error(), err)
		}
		response, err := c.httpClient.Do(request)
		if err != nil {
			cancel()
			return nil, aiprovider.RequestError(err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			providerErr := aiprovider.DecodeProviderError(response, endpoint.Credential)
			response.Body.Close()
			cancel()
			return nil, providerErr
		}
		body, err := aiprovider.ReadBounded(response.Body, aiprovider.MaxModelsBytes)
		response.Body.Close()
		cancel()
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "malformed Anthropic model list", nil)
		}
		for _, item := range envelope.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" || len(id) > 512 {
				return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "invalid Anthropic model ID", nil)
			}
			unique[id] = struct{}{}
			if len(unique) > 1000 {
				return nil, aiprovider.NewError(aiprovider.ErrorResponseTooLarge, false, 0, "too many Anthropic models", nil)
			}
		}
		if !envelope.HasMore {
			ids := make([]string, 0, len(unique))
			for id := range unique {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			models := make([]aiprovider.ModelInfo, 0, len(ids))
			for _, id := range ids {
				models = append(models, aiprovider.ModelInfo{ID: id})
			}
			return models, nil
		}
		if envelope.LastID == "" || envelope.LastID == afterID {
			return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "Anthropic model pagination did not advance", nil)
		}
		afterID = envelope.LastID
	}
	return nil, aiprovider.NewError(aiprovider.ErrorResponseTooLarge, false, 0, "Anthropic model pagination exceeds 20 pages", nil)
}

func (c *Client) Complete(ctx context.Context, endpoint aiprovider.RuntimeEndpoint, input aiprovider.Request) (*aiprovider.Response, error) {
	payload := anthropicPayload(input, false)
	_, generatePath := aiprovider.DefaultPaths(aiprovider.APIStyleAnthropicMessages)
	if endpoint.GeneratePath != "" {
		generatePath = endpoint.GeneratePath
	}
	target, err := aiprovider.ResolveEndpointURL(endpoint.BaseURL, generatePath)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(payload)
	requestCtx, cancel := aiprovider.EndpointContext(ctx, endpoint)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if err := applyAnthropicHeaders(request, endpoint); err != nil {
		return nil, aiprovider.NewError(aiprovider.ErrorInvalidRequest, false, 0, err.Error(), err)
	}
	started := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, aiprovider.RequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, aiprovider.DecodeProviderError(response, endpoint.Credential)
	}
	responseBody, err := aiprovider.ReadBounded(response.Body, aiprovider.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	result, err := decode(responseBody)
	if err != nil {
		return nil, err
	}
	result.ProviderRequestID = response.Header.Get("request-id")
	if result.ProviderRequestID == "" {
		result.ProviderRequestID = response.Header.Get("x-request-id")
	}
	result.Latency = time.Since(started)
	return result, nil
}

func (c *Client) Stream(ctx context.Context, endpoint aiprovider.RuntimeEndpoint, input aiprovider.Request, callback func(aiprovider.StreamEvent) error) error {
	payload := anthropicPayload(input, true)
	_, generatePath := aiprovider.DefaultPaths(aiprovider.APIStyleAnthropicMessages)
	if endpoint.GeneratePath != "" {
		generatePath = endpoint.GeneratePath
	}
	target, err := aiprovider.ResolveEndpointURL(endpoint.BaseURL, generatePath)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(payload)
	requestCtx, cancel := aiprovider.EndpointContext(ctx, endpoint)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if err := applyAnthropicHeaders(request, endpoint); err != nil {
		return aiprovider.NewError(aiprovider.ErrorInvalidRequest, false, 0, err.Error(), err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return aiprovider.RequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return aiprovider.DecodeProviderError(response, endpoint.Credential)
	}
	limited := &io.LimitedReader{R: response.Body, N: aiprovider.MaxResponseBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	event := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var value map[string]any
		if json.Unmarshal([]byte(data), &value) != nil {
			return aiprovider.NewError(aiprovider.ErrorParse, false, 0, "malformed Anthropic stream event", nil)
		}
		streamEvent := aiprovider.StreamEvent{ProviderRequestID: response.Header.Get("request-id")}
		if event == "message_start" {
			if message, ok := value["message"].(map[string]any); ok {
				streamEvent.Usage = decodeStreamUsage(message["usage"])
			}
		}
		if event == "content_block_delta" {
			if delta, ok := value["delta"].(map[string]any); ok {
				streamEvent.Delta, _ = delta["text"].(string)
			}
		}
		if event == "message_delta" {
			if delta, ok := value["delta"].(map[string]any); ok {
				raw, _ := delta["stop_reason"].(string)
				streamEvent.FinishReason = normalizeFinish(raw)
			}
			streamEvent.Usage = decodeStreamUsage(value["usage"])
		}
		if streamEvent.Delta != "" || streamEvent.FinishReason != "" || streamEvent.Usage != nil {
			if err := callback(streamEvent); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if limited.N == 0 {
		return aiprovider.NewError(aiprovider.ErrorResponseTooLarge, false, 0, "upstream stream exceeds the allowed size", nil)
	}
	return nil
}

func decodeStreamUsage(raw any) *aiprovider.Usage {
	usageMap, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	input, _ := usageMap["input_tokens"].(float64)
	output, _ := usageMap["output_tokens"].(float64)
	if input == 0 && output == 0 {
		return nil
	}
	return &aiprovider.Usage{InputTokens: int64(input), OutputTokens: int64(output), TotalTokens: int64(input + output)}
}

func applyAnthropicHeaders(request *http.Request, endpoint aiprovider.RuntimeEndpoint) error {
	if err := aiprovider.ApplyHeaders(request, endpoint); err != nil {
		return err
	}
	version := strings.TrimSpace(endpoint.AnthropicVersion)
	if version == "" {
		version = "2023-06-01"
	}
	request.Header.Set("anthropic-version", version)
	return nil
}

func anthropicPayload(input aiprovider.Request, stream bool) map[string]any {
	messages := make([]map[string]any, 0, len(input.Messages))
	for _, message := range input.Messages {
		if message.Role == "system" {
			continue
		}
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	payload := map[string]any{"model": input.Model, "messages": messages, "max_tokens": input.MaxOutputTokens, "stream": stream}
	if input.System != "" {
		payload["system"] = input.System
	}
	if input.Temperature != nil {
		payload["temperature"] = *input.Temperature
	}
	if input.Schema != nil && input.OutputMode != "json_object" {
		payload["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": input.Schema}}
	}
	return payload
}

func decode(body []byte) (*aiprovider.Response, error) {
	var envelope struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "malformed Anthropic Messages payload", nil)
	}
	parts := []string{}
	for _, content := range envelope.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	text := strings.Join(parts, "")
	if text == "" {
		return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "Anthropic Messages returned no text content", nil)
	}
	raw := json.RawMessage(nil)
	trimmed := strings.TrimSpace(text)
	if json.Valid([]byte(trimmed)) {
		raw = json.RawMessage(trimmed)
	}
	usage := aiprovider.Usage{InputTokens: envelope.Usage.Input, OutputTokens: envelope.Usage.Output, TotalTokens: envelope.Usage.Input + envelope.Usage.Output}
	return &aiprovider.Response{Text: text, Structured: raw, Usage: usage, FinishReason: normalizeFinish(envelope.StopReason), RawFinishReason: envelope.StopReason, Model: envelope.Model}, nil
}
func normalizeFinish(raw string) string {
	switch raw {
	case "end_turn", "stop_sequence", "stop":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool"
	case "refusal":
		return "content_filter"
	case "error":
		return "error"
	default:
		return "unknown"
	}
}
