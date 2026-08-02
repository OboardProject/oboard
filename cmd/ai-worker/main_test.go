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
