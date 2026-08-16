package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/model"
)

func TestBindReportRouteUsesSelectedEndpointCapability(t *testing.T) {
	capability := &model.AIProviderCapability{
		ProviderProfileVersion: model.AuditProviderProfileVersion,
		ConnectivityOK:         true,
		AuthenticationOK:       true,
		TextSupported:          true,
		AuditReady:             true,
		StructuredOutput:       model.AuditProviderStructuredPromptedJSON,
		OutputMode:             model.AuditOutputModeText,
	}
	boundInput, boundOutput, err := bindReportRoute(
		json.RawMessage(`{"engine":{"structured_output":"json_schema","output_mode":"strict_schema","model":"primary"}}`),
		json.RawMessage(`{"methodology":{"structured_output":"json_schema","output_mode":"strict_schema","model":"primary"}}`),
		capability,
		"fallback-model",
	)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Engine map[string]any `json:"engine"`
	}
	if err := json.Unmarshal(boundInput, &input); err != nil {
		t.Fatal(err)
	}
	if input.Engine["structured_output"] != model.AuditProviderStructuredPromptedJSON || input.Engine["output_mode"] != model.AuditOutputModeText || input.Engine["model"] != "fallback-model" {
		t.Fatalf("bound engine = %#v", input.Engine)
	}
	if _, exists := input.Engine["provider_grade"]; exists {
		t.Fatalf("bound engine retained provider grade: %#v", input.Engine)
	}
	var report struct {
		Methodology map[string]any `json:"methodology"`
	}
	if err := json.Unmarshal(boundOutput, &report); err != nil {
		t.Fatal(err)
	}
	if report.Methodology["structured_output"] != model.AuditProviderStructuredPromptedJSON || report.Methodology["output_mode"] != model.AuditOutputModeText || report.Methodology["model"] != "fallback-model" {
		t.Fatalf("bound methodology = %#v", report.Methodology)
	}
	if _, exists := report.Methodology["provider_grade"]; exists {
		t.Fatalf("bound methodology retained provider grade: %#v", report.Methodology)
	}
}

func TestDiscoverModelsUsesAnthropicAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("request path=%s headers=%v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-b"},{"id":"claude-a"}],"has_more":false}`))
	}))
	defer server.Close()
	models, err := discoverModels(context.Background(), newWorkerRuntime(), &airpc.ModelDiscoveryRequest{ProviderID: "p", Endpoint: airpc.RuntimeEndpoint{ID: "e", BaseURL: server.URL + "/v1", APIStyle: string(aiprovider.APIStyleAnthropicMessages), AuthMode: aiprovider.AuthModeXAPIKey, Credential: "key", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 2000}})
	if err != nil || len(models) != 2 || models[0] != "claude-a" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
}

func TestProviderTestProducesEndpointCapabilityWithoutSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		case "/v1/responses":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["stream"] == true {
				_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"ok\"}\n\n"))
				return
			}
			_, _ = w.Write([]byte(`{"model":"m","status":"completed","output_text":"{\"schema_version\":\"audit-user-finding-v1\",\"subject_ref\":\"user:sample\",\"behavior_profile\":{\"usual_pattern\":[],\"current_pattern\":[],\"key_changes\":[]},\"findings\":[],\"counter_evidence\":[],\"data_gaps\":[]}","usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4}}`))
		}
	}))
	defer server.Close()
	test := &airpc.AITestRequest{ID: "test", ProviderID: "p", Model: "m", Endpoint: airpc.RuntimeEndpoint{ID: "e", BaseURL: server.URL + "/v1", APIStyle: string(aiprovider.APIStyleOpenAIResponses), AuthMode: aiprovider.AuthModeBearer, Credential: "super-secret-provider-key-123", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 2000}}
	outcome := testProvider(context.Background(), newWorkerRuntime(), test)
	if outcome.err != nil || outcome.capability.EndpointID != "e" || outcome.capability.ConfigDigest == "" {
		t.Fatalf("outcome=%#v", outcome)
	}
	if !outcome.capability.AuditReady || outcome.capability.StructuredOutput != model.AuditProviderStructuredJSONSchema || outcome.capability.OutputMode != model.AuditOutputModeStrictSchema {
		t.Fatalf("capability=%#v", outcome.capability)
	}
	if contains := json.Valid([]byte(outcome.responseJSON)) && string(outcome.responseJSON) != ""; !contains {
		t.Fatal("missing safe summary")
	}
	if outcome.requestJSON == "" {
		t.Fatal("missing request summary")
	}
	serialized := outcome.requestJSON + outcome.responseJSON + outcome.content
	if outcome.err != nil {
		serialized += outcome.err.Error()
	}
	if strings.Contains(serialized, test.Endpoint.Credential) {
		t.Fatal("provider test artifacts leaked the credential")
	}
}

func TestProviderTestReturnsUnusableCapabilityForAuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()
	outcome := testProvider(context.Background(), newWorkerRuntime(), &airpc.AITestRequest{ID: "draft-test", Model: "m", Endpoint: airpc.RuntimeEndpoint{ID: "draft", BaseURL: server.URL + "/v1", APIStyle: string(aiprovider.APIStyleOpenAIResponses), AuthMode: aiprovider.AuthModeBearer, Credential: "bad-key", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 2000}})
	if outcome.err == nil || outcome.statusCode != http.StatusUnauthorized || outcome.capability == nil {
		t.Fatalf("outcome=%#v", outcome)
	}
	if outcome.capability.ProviderID != "draft:draft-test" || !outcome.capability.ConnectivityOK || outcome.capability.AuthenticationOK || outcome.capability.AuditReady || outcome.capability.StructuredOutput != model.AuditProviderStructuredNone || outcome.capability.OutputMode != model.AuditOutputModeText {
		t.Fatalf("capability=%#v", outcome.capability)
	}
}

func TestProviderTestFallsBackToPromptedJSON(t *testing.T) {
	const finding = `{"schema_version":"audit-user-finding-v1","subject_ref":"user:sample","behavior_profile":{"usual_pattern":[],"current_pattern":[],"key_changes":[]},"findings":[],"counter_evidence":[],"data_gaps":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"models":["deepseek-v4-flash"]}`))
		case "/v1/chat/completions":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["stream"] == true {
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
				return
			}
			finish, content := "stop", "not-json"
			if payload["max_tokens"] == float64(16) && payload["response_format"] == nil {
				finish, content = "length", "partial"
			} else if payload["response_format"] == nil {
				messages, _ := payload["messages"].([]any)
				if len(messages) == 0 || !strings.Contains(messages[0].(map[string]any)["content"].(string), "JSON Schema") {
					t.Fatalf("prompted JSON instructions missing: %#v", payload)
				}
				content = "Result: " + finding + ` ignored: {"example":true}`
			}
			response, _ := json.Marshal(map[string]any{"model": "deepseek-v4-flash", "choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 2, "total_tokens": 4}})
			_, _ = w.Write(response)
		}
	}))
	defer server.Close()
	outcome := testProvider(context.Background(), newWorkerRuntime(), &airpc.AITestRequest{ID: "compat", ProviderID: "p", ProviderKind: "custom", Model: "deepseek-v4-flash", Endpoint: airpc.RuntimeEndpoint{ID: "e", BaseURL: server.URL + "/v1", APIStyle: string(aiprovider.APIStyleOpenAIChatCompletions), AuthMode: aiprovider.AuthModeBearer, Credential: "key", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 2000}})
	if outcome.err != nil || !outcome.capability.AuditReady || outcome.capability.StructuredOutput != model.AuditProviderStructuredPromptedJSON || outcome.capability.OutputMode != model.AuditOutputModeText || !outcome.capability.FinishReasonSupported || !outcome.capability.ModelsSupported {
		t.Fatalf("outcome=%#v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.capability.Notes, " "), "提示词 JSON") || strings.Contains(outcome.responseJSON, "grade") || !strings.Contains(outcome.responseJSON, `"prompted_json_success":2`) {
		t.Fatalf("notes=%#v", outcome.capability.Notes)
	}
}

func TestJSONObjectProbeSupportMatchesAPIAdapters(t *testing.T) {
	if !supportsJSONObjectProbe(aiprovider.APIStyleOpenAIResponses) || !supportsJSONObjectProbe(aiprovider.APIStyleOpenAIChatCompletions) {
		t.Fatal("OpenAI adapters must probe JSON Object mode")
	}
	if supportsJSONObjectProbe(aiprovider.APIStyleAnthropicMessages) {
		t.Fatal("Anthropic adapter must report prompt JSON instead of JSON Object fallback")
	}
}
