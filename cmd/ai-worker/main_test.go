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
		AuditGrade:             model.AuditProviderGradeB,
		StructuredOutput:       model.AuditProviderStructuredJSONObject,
		OutputMode:             model.AuditOutputModeJSONObject,
	}
	boundInput, boundOutput, err := bindReportRoute(
		json.RawMessage(`{"engine":{"provider_grade":"A","model":"primary"}}`),
		json.RawMessage(`{"methodology":{"provider_grade":"A","model":"primary"}}`),
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
	if input.Engine["provider_grade"] != model.AuditProviderGradeB || input.Engine["model"] != "fallback-model" {
		t.Fatalf("bound engine = %#v", input.Engine)
	}
	var report model.AuditReviewReport
	if err := json.Unmarshal(boundOutput, &report); err != nil {
		t.Fatal(err)
	}
	if report.Methodology.ProviderGrade != model.AuditProviderGradeB || report.Methodology.OutputMode != model.AuditOutputModeJSONObject || report.Methodology.Model != "fallback-model" {
		t.Fatalf("bound methodology = %#v", report.Methodology)
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
	if outcome.capability.ProviderID != "draft:draft-test" || !outcome.capability.ConnectivityOK || outcome.capability.AuthenticationOK || outcome.capability.AuditGrade != model.AuditProviderGradeUnusable {
		t.Fatalf("capability=%#v", outcome.capability)
	}
}

func TestProviderTestCapsAnthropicOpenAICompatibilityAtGradeB(t *testing.T) {
	const finding = `{"schema_version":"audit-user-finding-v1","subject_ref":"user:sample","behavior_profile":{"usual_pattern":[],"current_pattern":[],"key_changes":[]},"findings":[],"counter_evidence":[],"data_gaps":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		case "/v1/chat/completions":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["stream"] == true {
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
				return
			}
			finish, content := "stop", finding
			if payload["max_tokens"] == float64(16) && payload["response_format"] == nil {
				finish, content = "length", "partial"
			}
			response, _ := json.Marshal(map[string]any{"model": "m", "choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 2, "total_tokens": 4}})
			_, _ = w.Write(response)
		}
	}))
	defer server.Close()
	outcome := testProvider(context.Background(), newWorkerRuntime(), &airpc.AITestRequest{ID: "compat", ProviderID: "p", ProviderKind: "anthropic", Model: "m", Endpoint: airpc.RuntimeEndpoint{ID: "e", BaseURL: server.URL + "/v1", APIStyle: string(aiprovider.APIStyleOpenAIChatCompletions), AuthMode: aiprovider.AuthModeBearer, Credential: "key", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 2000}})
	if outcome.err != nil || outcome.capability.AuditGrade != model.AuditProviderGradeB || outcome.capability.OutputMode != model.AuditOutputModeJSONObject || !outcome.capability.FinishReasonSupported {
		t.Fatalf("outcome=%#v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.capability.Notes, " "), "compatibility") {
		t.Fatalf("notes=%#v", outcome.capability.Notes)
	}
}
