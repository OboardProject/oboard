package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
)

func TestResponsesAndChatContracts(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		style aiprovider.APIStyle
		path  string
	}{
		{"responses", aiprovider.APIStyleOpenAIResponses, "/v1/responses"},
		{"chat", aiprovider.APIStyleOpenAIChatCompletions, "/v1/chat/completions"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != testCase.path || r.Header.Get("Authorization") != "Bearer super-secret-provider-key-123" {
					t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
				}
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if testCase.style == aiprovider.APIStyleOpenAIResponses {
					text := payload["text"].(map[string]any)
					if _, ok := text["format"]; !ok {
						t.Error("responses schema missing")
					}
					w.Header().Set("x-request-id", "req-responses")
					_, _ = w.Write([]byte(`{"model":"gpt-test","status":"completed","output_text":"{\"ok\":true}","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`))
				} else {
					format := payload["response_format"].(map[string]any)
					if format["type"] != "json_schema" {
						t.Errorf("chat response_format=%#v", format)
					}
					_, _ = w.Write([]byte(`{"model":"gpt-test","choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`))
				}
			}))
			defer server.Close()
			client := NewResponsesClient(aiprovider.NewHTTPClient())
			if testCase.style == aiprovider.APIStyleOpenAIChatCompletions {
				client = NewChatClient(aiprovider.NewHTTPClient())
			}
			response, err := client.Complete(context.Background(), aiprovider.RuntimeEndpoint{BaseURL: server.URL + "/v1", AuthMode: aiprovider.AuthModeBearer, Credential: "super-secret-provider-key-123", AllowPrivateNetwork: true, TimeoutMS: 2000}, aiprovider.Request{RequestID: "internal", Model: "gpt-test", Messages: []aiprovider.Message{{Role: "user", Content: "test"}}, MaxOutputTokens: 100, Schema: map[string]any{"type": "object"}})
			if err != nil || string(response.Structured) != `{"ok":true}` || response.Usage.TotalTokens != 6 || response.FinishReason != "stop" {
				t.Fatalf("response=%#v err=%v", response, err)
			}
		})
	}
}

func TestOpenAIModelsErrorsAndStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"z"},{"id":"a"},{"id":"a"}]}`))
		case "/v1/responses":
			if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"hi\"}\n\nevent: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"))
				return
			}
			w.Header().Set("Retry-After", "2")
			http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
		}
	}))
	defer server.Close()
	client := NewResponsesClient(aiprovider.NewHTTPClient())
	endpoint := aiprovider.RuntimeEndpoint{BaseURL: server.URL + "/v1", AuthMode: aiprovider.AuthModeBearer, Credential: "key", AllowPrivateNetwork: true, TimeoutMS: 2000}
	models, err := client.ListModels(context.Background(), endpoint)
	if err != nil || len(models) != 2 || models[0].ID != "a" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	_, err = client.Complete(context.Background(), endpoint, aiprovider.Request{Model: "m", MaxOutputTokens: 1})
	providerErr := aiprovider.AsProviderError(err)
	if providerErr.Kind != aiprovider.ErrorRateLimited || providerErr.RetryAfter <= 0 {
		t.Fatalf("429=%#v", providerErr)
	}
	deltas := ""
	var usage *aiprovider.Usage
	err = client.Stream(context.Background(), endpoint, aiprovider.Request{Model: "m", MaxOutputTokens: 1}, func(event aiprovider.StreamEvent) error {
		deltas += event.Delta
		if event.Usage != nil {
			usage = event.Usage
		}
		return nil
	})
	if err != nil || deltas != "hi" || usage == nil || usage.TotalTokens != 4 {
		t.Fatalf("stream=%q usage=%#v err=%v", deltas, usage, err)
	}
}

func TestOpenAIChatJSONObjectFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		format, _ := payload["response_format"].(map[string]any)
		if format["type"] != "json_object" {
			t.Fatalf("response_format=%#v", format)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := NewChatClient(aiprovider.NewHTTPClient())
	_, err := client.Complete(context.Background(), aiprovider.RuntimeEndpoint{BaseURL: server.URL, AuthMode: aiprovider.AuthModeBearer, Credential: "key", AllowPrivateNetwork: true, TimeoutMS: 2000}, aiprovider.Request{Model: "m", MaxOutputTokens: 10, Schema: map[string]any{"type": "object"}, OutputMode: "json_object"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenAIChatPromptedJSONFallbackExtractsFirstObject(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"ok"}, "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if _, exists := payload["response_format"]; exists {
			t.Fatalf("prompted JSON sent response_format: %#v", payload["response_format"])
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) == 0 {
			t.Fatalf("messages=%#v", messages)
		}
		system, _ := messages[0].(map[string]any)
		if system["role"] != "system" || !strings.Contains(system["content"].(string), `"type":"object"`) {
			t.Fatalf("system message=%#v", system)
		}
		response, _ := json.Marshal(map[string]any{
			"model": "deepseek-v4-flash",
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": "Here is the result: {\"ok\":true} and an ignored example {\"ok\":false}"},
				"finish_reason": "stop",
			}},
		})
		_, _ = w.Write(response)
	}))
	defer server.Close()

	request := aiprovider.PrepareStructuredRequest(aiprovider.Request{Model: "deepseek-v4-flash", MaxOutputTokens: 10, Schema: schema, OutputMode: "text"})
	response, err := NewChatClient(aiprovider.NewHTTPClient()).Complete(context.Background(), aiprovider.RuntimeEndpoint{BaseURL: server.URL, AuthMode: aiprovider.AuthModeBearer, Credential: "key", AllowPrivateNetwork: true, TimeoutMS: 2000}, request)
	if err != nil || string(response.Structured) != `{"ok":true}` {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestOpenAIAdaptivePayloadTokensAndFormats(t *testing.T) {
	client := NewChatClient(aiprovider.NewHTTPClient())
	for _, testCase := range []struct {
		model              string
		completionTokenKey bool
	}{
		{model: "gpt-5", completionTokenKey: true},
		{model: "vendor/gpt-5-mini", completionTokenKey: true},
		{model: "o1-preview", completionTokenKey: true},
		{model: "o3-mini", completionTokenKey: true},
		{model: "o4-mini", completionTokenKey: true},
		{model: "gpt-4o"},
		{model: "deepseek-v4-flash"},
	} {
		t.Run(testCase.model, func(t *testing.T) {
			payload := client.payload(aiprovider.Request{Model: testCase.model, MaxOutputTokens: 17}, false)
			_, hasCompletion := payload["max_completion_tokens"]
			_, hasLegacy := payload["max_tokens"]
			if hasCompletion != testCase.completionTokenKey || hasLegacy == testCase.completionTokenKey {
				t.Fatalf("token fields=%#v", payload)
			}
		})
	}

	schema := map[string]any{"type": "object"}
	textPayload := client.payload(aiprovider.Request{Model: "m", Schema: schema, OutputMode: "text"}, false)
	if _, exists := textPayload["response_format"]; exists {
		t.Fatalf("text-mode response_format=%#v", textPayload["response_format"])
	}
	responses := NewResponsesClient(aiprovider.NewHTTPClient())
	responseObjectPayload := responses.payload(aiprovider.Request{Model: "m", Schema: schema, OutputMode: "json_object"}, false)
	responseText, _ := responseObjectPayload["text"].(map[string]any)
	responseFormat, _ := responseText["format"].(map[string]any)
	if responseFormat["type"] != "json_object" {
		t.Fatalf("responses JSON object format=%#v", responseObjectPayload)
	}
	if payload := responses.payload(aiprovider.Request{Model: "m", Schema: schema, OutputMode: "text"}, false); payload["text"] != nil {
		t.Fatalf("responses text format=%#v", payload["text"])
	}
	if payload := responses.payload(aiprovider.Request{Model: "m", Schema: schema, OutputMode: "strict_schema"}, false); payload["text"] == nil {
		t.Fatalf("responses strict format missing: %#v", payload)
	}
}

func TestDecodeOpenAIModelListVariants(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		want string
	}{
		{name: "OpenAI data objects", body: `{"data":[{"id":"a"},{"name":"b"},{"model":"c"}]}`, want: "a,b,c"},
		{name: "models strings and objects", body: `{"models":["a",{"name":"b"},{"id":"c"}]}`, want: "a,b,c"},
		{name: "top-level array", body: `["a",{"model":"b"}]`, want: "a,b"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ids, ok := decodeModelIDs([]byte(testCase.body))
			if !ok || strings.Join(ids, ",") != testCase.want {
				t.Fatalf("ids=%#v ok=%v", ids, ok)
			}
		})
	}
}

func TestOpenAIErrorClassificationAndBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/malformed":
			_, _ = w.Write([]byte(`{"not":"a response"}`))
		case "/oversized":
			_, _ = w.Write([]byte(strings.Repeat("x", aiprovider.MaxResponseBytes+1)))
		case "/error500":
			http.Error(w, `{"error":{"message":"temporary"}}`, http.StatusInternalServerError)
		case "/slow":
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(`{"status":"completed","output_text":"ok"}`))
		case "/unsupported":
			http.Error(w, `{"error":{"message":"response_format unsupported"}}`, http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := NewResponsesClient(aiprovider.NewHTTPClient())
	base := aiprovider.RuntimeEndpoint{BaseURL: server.URL, AuthMode: aiprovider.AuthModeBearer, Credential: "key", AllowPrivateNetwork: true, TimeoutMS: 2000}
	request := aiprovider.Request{Model: "m", MaxOutputTokens: 10}
	for _, testCase := range []struct {
		path string
		kind string
	}{
		{"/malformed", aiprovider.ErrorParse},
		{"/oversized", aiprovider.ErrorResponseTooLarge},
		{"/error500", aiprovider.ErrorUpstream5xx},
		{"/unsupported", aiprovider.ErrorInvalidRequest},
	} {
		endpoint := base
		endpoint.GeneratePath = testCase.path
		_, err := client.Complete(context.Background(), endpoint, request)
		if aiprovider.AsProviderError(err).Kind != testCase.kind {
			t.Fatalf("path=%s err=%v", testCase.path, err)
		}
	}
	endpoint := base
	endpoint.GeneratePath, endpoint.TimeoutMS = "/slow", 10
	_, err := client.Complete(context.Background(), endpoint, request)
	if aiprovider.AsProviderError(err).Kind != aiprovider.ErrorTimeout {
		t.Fatalf("timeout err=%v", err)
	}
}
