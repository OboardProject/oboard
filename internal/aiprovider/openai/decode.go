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
		if input.Schema != nil && input.OutputMode != "json_object" {
			payload["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "oboard_result", "strict": true, "schema": input.Schema}}
		}
		return payload
	}
	messages := make([]map[string]any, 0, len(input.Messages)+1)
	if input.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": input.System})
	}
	for _, message := range input.Messages {
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	payload := map[string]any{"model": input.Model, "messages": messages, "max_tokens": input.MaxOutputTokens, "stream": stream}
	if input.Temperature != nil {
		payload["temperature"] = *input.Temperature
	}
	if input.Schema != nil {
		if input.OutputMode == "json_object" {
			payload["response_format"] = map[string]any{"type": "json_object"}
		} else {
			payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "oboard_result", "strict": true, "schema": input.Schema}}
		}
	}
	return payload
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
				Content string `json:"content"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Input  int64 `json:"prompt_tokens"`
			Output int64 `json:"completion_tokens"`
			Total  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Choices) == 0 || envelope.Choices[0].Message.Content == "" {
		return nil, aiprovider.NewError(aiprovider.ErrorParse, false, 0, "malformed OpenAI Chat payload", nil)
	}
	raw := envelope.Choices[0].Finish
	return &aiprovider.Response{Text: envelope.Choices[0].Message.Content, Usage: aiprovider.Usage{InputTokens: envelope.Usage.Input, OutputTokens: envelope.Usage.Output, TotalTokens: envelope.Usage.Total}, FinishReason: normalizeFinish(raw), RawFinishReason: raw, Model: envelope.Model}, nil
}

func normalizeFinish(raw string) string {
	switch raw {
	case "completed", "stop", "stop_sequence":
		return "stop"
	case "incomplete", "length", "max_tokens", "max_output_tokens":
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
