package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestRunOnceCompletesFindingWithoutReturningProviderCredential(t *testing.T) {
	const apiKey = "provider-secret-that-must-not-return"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("model authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), apiKey) {
			t.Fatal("provider credential leaked into model request body")
		}
		content, _ := json.Marshal(map[string]any{
			"verdict": "attention", "risk_level": "medium", "confidence": 0.82, "summary": "multiple concurrent regions",
			"dimensions":          []any{map[string]any{"kind": "connection", "risk_level": "medium", "summary": "multiple regions", "evidence_refs": []string{"user:1"}, "counter_evidence": []string{"mobile_network"}}},
			"notable_subjects":    []any{map[string]any{"subject_ref": "user:1", "risk_level": "medium", "summary": "multiple regions", "evidence_refs": []string{"user:1"}}},
			"recommended_actions": []string{"request_manual_review"}, "data_gaps": []string{}, "coverage_summary": "reviewed one user",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 120, "completion_tokens": 40}})
	}))
	defer modelServer.Close()

	var mu sync.Mutex
	var completed airpc.CompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/lease":
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AuditReviewJob{ID: "job-1", ReviewID: "review-1", Input: json.RawMessage(`{"privacy_mode":"masked"}`)}, Provider: &airpc.Provider{ID: "provider-1", BaseURL: modelServer.URL, Model: "test-model", APIKey: apiKey}})
		case "/v1/jobs/job-1/complete":
			mu.Lock()
			defer mu.Unlock()
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()

	client := controllerClient(t, controller.URL)
	if err := runOnce(context.Background(), client, "worker-1"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	encoded, _ := json.Marshal(completed)
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("provider credential leaked into completion RPC")
	}
	if completed.WorkerID != "worker-1" || completed.Report.Verdict != "attention" || completed.Report.RiskLevel != "medium" || completed.InputTokens != 120 || completed.OutputTokens != 40 {
		t.Fatalf("unexpected completion: %#v", completed)
	}
}

func TestRunOnceSupportsResponsesAPIFormat(t *testing.T) {
	const apiKey = "responses-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != http.MethodPost {
			t.Fatalf("responses request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("responses authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			Model       string                 `json:"model"`
			Temperature int                    `json:"temperature"`
			Input       []map[string]string    `json:"input"`
			Extra       map[string]interface{} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "responses-model" || request.Temperature != 0 || len(request.Input) != 2 || request.Input[1]["role"] != "user" || request.Extra != nil {
			t.Fatalf("responses payload = %#v", request)
		}
		content, _ := json.Marshal(map[string]any{
			"verdict": "normal", "risk_level": "low", "confidence": 0.9, "summary": "responses ok",
			"dimensions": []any{}, "notable_subjects": []any{}, "recommended_actions": []string{}, "data_gaps": []string{}, "coverage_summary": "ok",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{"output_text": string(content), "usage": map[string]int{"input_tokens": 200, "output_tokens": 60}})
	}))
	defer modelServer.Close()

	var completed airpc.CompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/lease":
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AuditReviewJob{ID: "job-responses", ReviewID: "review-responses", Input: json.RawMessage(`{"privacy_mode":"masked"}`)}, Provider: &airpc.Provider{ID: "provider-1", BaseURL: modelServer.URL, Model: "responses-model", APIFormat: "responses", APIKey: apiKey}})
		case "/v1/jobs/job-responses/complete":
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()

	if err := runOnce(context.Background(), controllerClient(t, controller.URL), "worker-responses"); err != nil {
		t.Fatal(err)
	}
	if completed.WorkerID != "worker-responses" || completed.Report.Verdict != "normal" || completed.InputTokens != 200 || completed.OutputTokens != 60 {
		t.Fatalf("unexpected completion: %#v", completed)
	}
}

func TestAnalyzeSurfacesProviderErrorMessageWithoutCredential(t *testing.T) {
	const apiKey = "error-body-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key error-body-secret","type":"authentication_error"}}`))
	}))
	defer modelServer.Close()

	_, _, _, err := analyze(context.Background(), &model.AuditReviewJob{Input: json.RawMessage(`{}`)}, &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIKey: apiKey})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "invalid api key") || strings.Contains(err.Error(), apiKey) {
		t.Fatalf("provider error = %v", err)
	}
}

func TestRunOnceReportsBoundedModelFailure(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer modelServer.Close()
	failed := make(chan airpc.FailRequest, 1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/lease":
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AuditReviewJob{ID: "job-2", ReviewID: "review-2", Input: json.RawMessage(`{}`)}, Provider: &airpc.Provider{ID: "provider-1", BaseURL: modelServer.URL, Model: "test-model", APIKey: "secret"}})
		case "/v1/jobs/job-2/fail":
			var request airpc.FailRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			failed <- request
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runOnce(context.Background(), controllerClient(t, controller.URL), "worker-2"); err == nil {
		t.Fatal("model failure was not returned")
	}
	request := <-failed
	if request.WorkerID != "worker-2" || len(request.Error) > 1000 || strings.Contains(request.Error, "secret") {
		t.Fatalf("unexpected failure RPC: %#v", request)
	}
}

func TestRunModelDiscoveryOnceReturnsSortedModelsWithoutCredential(t *testing.T) {
	const apiKey = "model-list-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Fatalf("model request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("model authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "z-model"}, map[string]string{"id": "a-model"}, map[string]string{"id": "z-model"}}})
	}))
	defer modelServer.Close()

	completed := make(chan airpc.ModelDiscoveryCompleteRequest, 1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/model-discovery/lease":
			_ = json.NewEncoder(w).Encode(airpc.ModelDiscoveryLeaseResponse{Request: &airpc.ModelDiscoveryRequest{ID: "discovery-1", BaseURL: modelServer.URL + "/v1", APIKey: apiKey}})
		case "/v1/model-discovery/discovery-1/complete":
			var request airpc.ModelDiscoveryCompleteRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			completed <- request
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runModelDiscoveryOnce(context.Background(), controllerClient(t, controller.URL), "worker-models"); err != nil {
		t.Fatal(err)
	}
	request := <-completed
	encoded, _ := json.Marshal(request)
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("provider credential leaked into model discovery callback")
	}
	if request.WorkerID != "worker-models" || len(request.Models) != 2 || request.Models[0] != "a-model" || request.Models[1] != "z-model" {
		t.Fatalf("unexpected completion: %#v", request)
	}
}

func TestDiscoverModelsRejectsRedirectAndMalformedResponse(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "redirected"}}})
	}))
	defer redirectTarget.Close()
	redirectServer := httptest.NewServer(http.RedirectHandler(redirectTarget.URL, http.StatusFound))
	defer redirectServer.Close()
	if _, err := discoverModels(context.Background(), &airpc.ModelDiscoveryRequest{BaseURL: redirectServer.URL, APIKey: "secret"}); err == nil {
		t.Fatal("redirected model endpoint was accepted")
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":""}]}`))
	}))
	defer malformed.Close()
	if _, err := discoverModels(context.Background(), &airpc.ModelDiscoveryRequest{BaseURL: malformed.URL, APIKey: "secret"}); err == nil {
		t.Fatal("malformed model list was accepted")
	}
}

func controllerClient(t *testing.T, baseURL string) *http.Client {
	t.Helper()
	target, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		clone.Host = target.Host
		return transport.RoundTrip(clone)
	})}
}

func TestRunAITestOnceCompletesWithoutCredentialOrResponseFormat(t *testing.T) {
	const apiKey = "ai-test-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Fatalf("test request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("test authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), apiKey) {
			t.Fatal("provider credential leaked into test request body")
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if _, exists := payload["response_format"]; exists {
			t.Fatal("config test must not require response_format")
		}
		if _, exists := payload["temperature"]; exists {
			t.Fatal("config test payload must stay minimal")
		}
		if payload["max_tokens"] != float64(16) {
			t.Fatalf("max_tokens = %#v", payload["max_tokens"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 1}})
	}))
	defer modelServer.Close()

	var completed airpc.AITestCompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ai-test/lease":
			_ = json.NewEncoder(w).Encode(airpc.AITestLeaseResponse{Request: &airpc.AITestRequest{ID: "test-1", BaseURL: modelServer.URL, APIKey: apiKey, Model: "test-model", APIFormat: "chat_completions"}})
		case "/v1/ai-test/test-1/complete":
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()

	if err := runAITestOnce(context.Background(), controllerClient(t, controller.URL), "worker-ai-test"); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(completed)
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("provider credential leaked into AI test completion RPC")
	}
	if completed.WorkerID != "worker-ai-test" || completed.StatusCode != 200 || completed.DurationMS < 0 || completed.Content != "ok" {
		t.Fatalf("unexpected AI test completion: %#v", completed)
	}
	if !strings.Contains(completed.RequestJSON, `"messages"`) || !strings.Contains(completed.ResponseJSON, `"choices"`) {
		t.Fatalf("raw JSON missing: %#v", completed)
	}
}

func TestRunAITestOnceSupportsResponsesFormat(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != http.MethodPost {
			t.Fatalf("responses test request = %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if _, exists := payload["response_format"]; exists {
			t.Fatal("responses config test must not require response_format")
		}
		if payload["max_output_tokens"] != float64(16) || payload["input"] != "Reply with exactly one word: ok" {
			t.Fatalf("responses test payload = %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output_text": "ok", "usage": map[string]int{"input_tokens": 8, "output_tokens": 1}})
	}))
	defer modelServer.Close()

	var completed airpc.AITestCompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ai-test/lease":
			_ = json.NewEncoder(w).Encode(airpc.AITestLeaseResponse{Request: &airpc.AITestRequest{ID: "test-responses", BaseURL: modelServer.URL, APIKey: "responses-key", Model: "responses-model", APIFormat: "responses"}})
		case "/v1/ai-test/test-responses/complete":
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runAITestOnce(context.Background(), controllerClient(t, controller.URL), "worker-ai-responses"); err != nil {
		t.Fatal(err)
	}
	if completed.WorkerID != "worker-ai-responses" || completed.StatusCode != 200 || completed.Content != "ok" {
		t.Fatalf("unexpected AI test completion: %#v", completed)
	}
}

func TestRunAITestOnceReportsHTTPErrorWithRedactedRawResponse(t *testing.T) {
	const apiKey = "error-body-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key error-body-secret","type":"authentication_error"}}`))
	}))
	defer modelServer.Close()

	failed := make(chan airpc.AITestFailRequest, 1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ai-test/lease":
			_ = json.NewEncoder(w).Encode(airpc.AITestLeaseResponse{Request: &airpc.AITestRequest{ID: "test-error", BaseURL: modelServer.URL, APIKey: apiKey, Model: "m"}})
		case "/v1/ai-test/test-error/fail":
			var request airpc.AITestFailRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			failed <- request
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runAITestOnce(context.Background(), controllerClient(t, controller.URL), "worker-ai-error"); err == nil {
		t.Fatal("model failure was not returned")
	}
	request := <-failed
	if request.WorkerID != "worker-ai-error" || request.StatusCode != 401 || request.DurationMS < 0 {
		t.Fatalf("unexpected AI test failure RPC: %#v", request)
	}
	if !strings.Contains(request.Error, "HTTP 401") || !strings.Contains(request.Error, "invalid api key") || strings.Contains(request.Error, apiKey) || len(request.Error) > 1000 {
		t.Fatalf("AI test failure error = %q", request.Error)
	}
	if strings.Contains(request.ResponseJSON, apiKey) || !strings.Contains(request.ResponseJSON, "[redacted]") || request.RequestJSON == "" {
		t.Fatalf("AI test failure raw JSON = %#v", request)
	}
}
