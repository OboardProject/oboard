package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OboardProject/oboard/internal/aiprovider"
)

func TestAnthropicMessagesAndPaginatedModels(t *testing.T) {
	modelPages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "claude-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("headers key=%q version=%q", r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"))
		}
		switch r.URL.Path {
		case "/v1/models":
			modelPages++
			if r.URL.Query().Get("after_id") == "" {
				_, _ = w.Write([]byte(`{"data":[{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`))
			} else {
				_, _ = w.Write([]byte(`{"data":[{"id":"claude-a"},{"id":"claude-b"}],"has_more":false,"last_id":"claude-b"}`))
			}
		case "/v1/messages":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["stream"] == true {
				_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":5}}}\n\nevent: content_block_delta\ndata: {\"delta\":{\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"))
				return
			}
			if payload["system"] != "system prompt" {
				t.Errorf("system=%#v", payload["system"])
			}
			if _, ok := payload["output_config"]; !ok {
				t.Error("output_config missing")
			}
			w.Header().Set("request-id", "req-claude")
			_, _ = w.Write([]byte(`{"model":"claude-test","content":[{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`))
		}
	}))
	defer server.Close()
	client := NewMessagesClient(aiprovider.NewHTTPClient())
	endpoint := aiprovider.RuntimeEndpoint{BaseURL: server.URL + "/v1", AuthMode: aiprovider.AuthModeXAPIKey, Credential: "claude-key", AllowPrivateNetwork: true, TimeoutMS: 2000}
	models, err := client.ListModels(context.Background(), endpoint)
	if err != nil || modelPages != 2 || len(models) != 2 || models[0].ID != "claude-a" {
		t.Fatalf("models=%#v pages=%d err=%v", models, modelPages, err)
	}
	response, err := client.Complete(context.Background(), endpoint, aiprovider.Request{Model: "claude-test", System: "system prompt", Messages: []aiprovider.Message{{Role: "user", Content: "test"}}, MaxOutputTokens: 100, Schema: map[string]any{"type": "object"}})
	if err != nil || response.Usage.TotalTokens != 8 || response.FinishReason != "stop" || response.ProviderRequestID != "req-claude" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	delta := ""
	usageTotal := int64(0)
	err = client.Stream(context.Background(), endpoint, aiprovider.Request{Model: "claude-test", Messages: []aiprovider.Message{{Role: "user", Content: "test"}}, MaxOutputTokens: 10}, func(event aiprovider.StreamEvent) error {
		delta += event.Delta
		if event.Usage != nil {
			usageTotal += event.Usage.TotalTokens
		}
		return nil
	})
	if err != nil || delta != "ok" || usageTotal != 7 {
		t.Fatalf("stream delta=%q usage=%d err=%v", delta, usageTotal, err)
	}
}

func TestAnthropicModelPageLimitAndMalformedContent(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			pages++
			_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"id":"m-%d"}],"has_more":true,"last_id":"m-%d"}`, pages, pages)))
		case "/messages":
			_, _ = w.Write([]byte(`{"model":"m","content":[{"type":"tool_use"}],"stop_reason":"tool_use","usage":{}}`))
		}
	}))
	defer server.Close()
	client := NewMessagesClient(aiprovider.NewHTTPClient())
	endpoint := aiprovider.RuntimeEndpoint{BaseURL: server.URL, AuthMode: aiprovider.AuthModeXAPIKey, Credential: "key", AllowPrivateNetwork: true, TimeoutMS: 2000}
	if _, err := client.ListModels(context.Background(), endpoint); aiprovider.AsProviderError(err).Kind != aiprovider.ErrorResponseTooLarge || pages != 20 {
		t.Fatalf("pages=%d err=%v", pages, err)
	}
	if _, err := client.Complete(context.Background(), endpoint, aiprovider.Request{Model: "m", MaxOutputTokens: 10}); aiprovider.AsProviderError(err).Kind != aiprovider.ErrorParse {
		t.Fatalf("malformed content err=%v", err)
	}
	endpoint.Credential = ""
	if _, err := client.Complete(context.Background(), endpoint, aiprovider.Request{Model: "m", MaxOutputTokens: 10}); aiprovider.AsProviderError(err).Kind != aiprovider.ErrorInvalidRequest {
		t.Fatalf("missing credential err=%v", err)
	}
}
