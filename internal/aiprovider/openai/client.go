package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
)

type Client struct {
	httpClient *http.Client
	style      aiprovider.APIStyle
}

func NewResponsesClient(client *http.Client) *Client {
	return &Client{httpClient: client, style: aiprovider.APIStyleOpenAIResponses}
}
func NewChatClient(client *http.Client) *Client {
	return &Client{httpClient: client, style: aiprovider.APIStyleOpenAIChatCompletions}
}

func (c *Client) ListModels(ctx context.Context, endpoint aiprovider.RuntimeEndpoint) ([]aiprovider.ModelInfo, error) {
	modelsPath, _ := aiprovider.DefaultPaths(c.style)
	if endpoint.ModelsPath != "" {
		modelsPath = endpoint.ModelsPath
	}
	target, err := aiprovider.ResolveEndpointURL(endpoint.BaseURL, modelsPath)
	if err != nil {
		return nil, aiprovider.NewError(aiprovider.ErrorInvalidRequest, false, 0, err.Error(), err)
	}
	requestCtx, cancel := aiprovider.EndpointContext(ctx, endpoint)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if err := aiprovider.ApplyHeaders(request, endpoint); err != nil {
		return nil, aiprovider.NewError(aiprovider.ErrorInvalidRequest, false, 0, err.Error(), err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, aiprovider.RequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, aiprovider.DecodeProviderError(response, endpoint.Credential)
	}
	body, err := aiprovider.ReadBounded(response.Body, aiprovider.MaxModelsBytes)
	if err != nil {
		return nil, err
	}
	ids, ok := decodeModelIDs(body)
	if !ok || len(ids) == 0 || len(ids) > 1000 {
		return nil, aiprovider.NewError(aiprovider.ErrorParse, false, response.StatusCode, "unsupported OpenAI model list", nil)
	}
	unique := map[string]struct{}{}
	for _, value := range ids {
		id := strings.TrimSpace(value)
		if id == "" || len(id) > 512 {
			return nil, aiprovider.NewError(aiprovider.ErrorParse, false, response.StatusCode, "invalid model ID", nil)
		}
		unique[id] = struct{}{}
	}
	sortedIDs := make([]string, 0, len(unique))
	for id := range unique {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Strings(sortedIDs)
	models := make([]aiprovider.ModelInfo, 0, len(sortedIDs))
	for _, id := range sortedIDs {
		models = append(models, aiprovider.ModelInfo{ID: id})
	}
	return models, nil
}

func decodeModelIDs(body []byte) ([]string, bool) {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return nil, false
	}
	items := root
	if object, ok := root.(map[string]any); ok {
		items = object["data"]
		if items == nil {
			items = object["models"]
		}
	}
	list, ok := items.([]any)
	if !ok {
		return nil, false
	}
	ids := make([]string, 0, len(list))
	for _, item := range list {
		switch value := item.(type) {
		case string:
			ids = append(ids, value)
		case map[string]any:
			for _, key := range []string{"id", "name", "model"} {
				if id, ok := value[key].(string); ok && strings.TrimSpace(id) != "" {
					ids = append(ids, id)
					break
				}
			}
		}
	}
	return ids, true
}

func (c *Client) Complete(ctx context.Context, endpoint aiprovider.RuntimeEndpoint, input aiprovider.Request) (*aiprovider.Response, error) {
	payload := c.payload(input, false)
	_, generatePath := aiprovider.DefaultPaths(c.style)
	if endpoint.GeneratePath != "" {
		generatePath = endpoint.GeneratePath
	}
	target, err := aiprovider.ResolveEndpointURL(endpoint.BaseURL, generatePath)
	if err != nil {
		return nil, aiprovider.NewError(aiprovider.ErrorInvalidRequest, false, 0, err.Error(), err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := aiprovider.EndpointContext(ctx, endpoint)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if input.RequestID != "" {
		request.Header.Set("X-Client-Request-Id", input.RequestID)
	}
	if err := aiprovider.ApplyHeaders(request, endpoint); err != nil {
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
	result, err := c.decode(responseBody)
	if err != nil {
		return nil, err
	}
	result.ProviderRequestID = firstNonEmpty(response.Header.Get("x-request-id"), response.Header.Get("request-id"))
	result.Latency = time.Since(started)
	result.Model = input.Model
	if raw := aiprovider.ExtractJSONObject(result.Text); raw != nil {
		result.Structured = raw
	}
	return result, nil
}

func (c *Client) Stream(ctx context.Context, endpoint aiprovider.RuntimeEndpoint, input aiprovider.Request, callback func(aiprovider.StreamEvent) error) error {
	payload := c.payload(input, true)
	_, generatePath := aiprovider.DefaultPaths(c.style)
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
	if err := aiprovider.ApplyHeaders(request, endpoint); err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return aiprovider.RequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return aiprovider.DecodeProviderError(response, endpoint.Credential)
	}
	return readSSE(response.Body, func(event string, data []byte) error {
		if string(data) == "[DONE]" {
			return nil
		}
		var value map[string]any
		if json.Unmarshal(data, &value) != nil {
			return aiprovider.NewError(aiprovider.ErrorParse, false, 0, "malformed stream event", nil)
		}
		delta := ""
		finish := ""
		var streamUsage *aiprovider.Usage
		if c.style == aiprovider.APIStyleOpenAIResponses {
			if event == "response.output_text.delta" {
				delta, _ = value["delta"].(string)
			}
			if event == "response.completed" {
				finish = "stop"
				if responseValue, ok := value["response"].(map[string]any); ok {
					streamUsage = decodeStreamUsage(responseValue["usage"])
				}
			}
		} else if choices, ok := value["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if d, ok := choice["delta"].(map[string]any); ok {
					delta, _ = d["content"].(string)
				}
				finish, _ = choice["finish_reason"].(string)
			}
		}
		if c.style == aiprovider.APIStyleOpenAIChatCompletions {
			streamUsage = decodeStreamUsage(value["usage"])
		}
		if delta == "" && finish == "" && streamUsage == nil {
			return nil
		}
		normalizedFinish := ""
		if finish != "" {
			normalizedFinish = normalizeFinish(finish)
		}
		return callback(aiprovider.StreamEvent{Delta: delta, Usage: streamUsage, FinishReason: normalizedFinish, ProviderRequestID: response.Header.Get("x-request-id")})
	})
}

func readSSE(reader io.Reader, callback func(string, []byte) error) error {
	limited := &io.LimitedReader{R: reader, N: aiprovider.MaxResponseBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	event := ""
	data := []string{}
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		joined := []byte(strings.Join(data, "\n"))
		data = nil
		current := event
		event = ""
		return callback(current, joined)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if limited.N == 0 {
		return aiprovider.NewError(aiprovider.ErrorResponseTooLarge, false, 0, "upstream stream exceeds the allowed size", nil)
	}
	return flush()
}

func decodeStreamUsage(raw any) *aiprovider.Usage {
	usageMap, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	input := streamInt(usageMap, "input_tokens", "prompt_tokens")
	output := streamInt(usageMap, "output_tokens", "completion_tokens")
	total := streamInt(usageMap, "total_tokens")
	if total == 0 {
		total = input + output
	}
	if input == 0 && output == 0 && total == 0 {
		return nil
	}
	return &aiprovider.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total}
}

func streamInt(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := values[key].(float64); ok && value >= 0 {
			return int64(value)
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
