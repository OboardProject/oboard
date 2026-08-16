package openai

import (
	"encoding/json"
	"strings"

	"github.com/OboardProject/oboard/internal/aiprovider"
)

func (c *Client) payload(input aiprovider.Request, stream bool) map[string]any {
	if c.style == aiprovider.APIStyleOpenAIResponses {
		messages := make([]map[string]any, 0, len(input.Messages))
		for _, message := range input.Messages {
			messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
		}
		payload := map[string]any{"model": input.Model, "input": messages, "max_output_tokens": input.MaxOutputTokens, "stream": stream}
		if input.System != "" {
			payload["instructions"] = input.System
		}
		if input.Temperature != nil {
			payload["temperature"] = *input.Temperature
		}
		if input.Schema != nil && input.OutputMode != "text" {
			format := map[string]any{"type": "json_object"}
			if input.OutputMode != "json_object" {
				format = map[string]any{"type": "json_schema", "name": "oboard_result", "strict": true, "schema": input.Schema}
			}
			payload["text"] = map[string]any{"format": format}
		}
		return payload
	}
	messages := make([]map[string]any, 0, len(input.Messages)+1)
	if input.System != "" {
		role := "system"
		if usesMaxCompletionTokens(input.Model) {
			role = "developer"
		}
		messages = append(messages, map[string]any{"role": role, "content": input.System})
	}
	for _, message := range input.Messages {
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	payload := map[string]any{"model": input.Model, "messages": messages, "stream": stream}
	if usesMaxCompletionTokens(input.Model) {
		payload["max_completion_tokens"] = input.MaxOutputTokens
	} else {
		payload["max_tokens"] = input.MaxOutputTokens
	}
	if input.Temperature != nil {
		payload["temperature"] = *input.Temperature
	}
	if input.Schema != nil && input.OutputMode != "text" {
		if input.OutputMode == "json_object" {
			payload["response_format"] = map[string]any{"type": "json_object"}
		} else {
			payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "oboard_result", "strict": true, "schema": input.Schema}}
		}
	}
	return payload
}

func usesMaxCompletionTokens(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if slash := strings.LastIndex(modelID, "/"); slash >= 0 {
		modelID = modelID[slash+1:]
	}
	return strings.HasPrefix(modelID, "gpt-5") || strings.HasPrefix(modelID, "o1") || strings.HasPrefix(modelID, "o3") || strings.HasPrefix(modelID, "o4")
}

func (c *Client) decode(body []byte) (*aiprovider.Response, error) {
	if c.style == aiprovider.APIStyleOpenAIResponses {
		var envelope struct {
			Model             string `json:"model"`
			Status            string `json:"status"`
			OutputText        string `json:"output_text"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Output []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
				Total  int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "malformed OpenAI Responses payload", nil)
		}
		text := envelope.OutputText
		if text == "" {
			parts := []string{}
			for _, output := range envelope.Output {
				for _, content := range output.Content {
					if content.Text != "" {
						parts = append(parts, content.Text)
					}
				}
			}
			text = strings.Join(parts, "")
		}
		if text == "" {
			return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "OpenAI Responses returned no text", nil)
		}
		raw := envelope.Status
		if envelope.IncompleteDetails.Reason != "" {
			raw = envelope.IncompleteDetails.Reason
		}
		return &aiprovider.Response{Text: text, Usage: aiprovider.Usage{InputTokens: envelope.Usage.Input, OutputTokens: envelope.Usage.Output, TotalTokens: envelope.Usage.Total}, FinishReason: normalizeFinish(raw), RawFinishReason: raw, Model: envelope.Model}, nil
	}
	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
			} `json:"message"`
			Finish     string `json:"finish_reason"`
			StopReason string `json:"stop_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			Total      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Choices) == 0 {
		return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "malformed OpenAI Chat payload", nil)
	}
	choice := envelope.Choices[0]
	text := decodeChatContent(choice.Message.Content)
	if text == "" {
		text = choice.Message.ReasoningContent
	}
	if text == "" {
		return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "OpenAI Chat returned no text", nil)
	}
	inputTokens, outputTokens := envelope.Usage.Prompt, envelope.Usage.Completion
	if inputTokens == 0 {
		inputTokens = envelope.Usage.Input
	}
	if outputTokens == 0 {
		outputTokens = envelope.Usage.Output
	}
	totalTokens := envelope.Usage.Total
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}
	raw := choice.Finish
	if raw == "" {
		raw = choice.StopReason
	}
	return &aiprovider.Response{Text: text, Usage: aiprovider.Usage{InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens}, FinishReason: normalizeFinish(raw), RawFinishReason: raw, Model: envelope.Model}, nil
}

func decodeChatContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			values = append(values, part.Text)
		}
	}
	return strings.Join(values, "")
}

func normalizeFinish(raw string) string {
	switch raw {
	case "completed", "stop", "stop_sequence", "end_turn", "eos", "finished", "normal", "success":
		return "stop"
	case "incomplete", "length", "max_tokens", "max_output_tokens", "max_completion_tokens":
		return "length"
	case "tool_calls", "function_call", "tool_use":
		return "tool"
	case "content_filter":
		return "content_filter"
	case "error", "failed", "cancelled":
		return "error"
	default:
		return "unknown"
	}
}
