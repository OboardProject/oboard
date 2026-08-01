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
		content, _ := json.Marshal(map[string]any{"classification": "possible_account_sharing", "confidence": 0.82, "evidence_refs": []string{"feature:regions"}, "counter_evidence": []string{"mobile_network"}, "recommended_actions": []string{"request_manual_review"}, "summary": "multiple concurrent regions"})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 120, "completion_tokens": 40}})
	}))
	defer modelServer.Close()

	var mu sync.Mutex
	var completed airpc.CompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/lease":
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AIAnalysisJob{ID: "job-1", IncidentID: "incident-1", Input: json.RawMessage(`{"privacy_mode":"masked"}`)}, Provider: &airpc.Provider{ID: "provider-1", BaseURL: modelServer.URL, Model: "test-model", APIKey: apiKey}})
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
	if completed.WorkerID != "worker-1" || completed.Finding.IncidentID != "incident-1" || completed.Finding.Classification != "possible_account_sharing" || completed.InputTokens != 120 || completed.OutputTokens != 40 {
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
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AIAnalysisJob{ID: "job-2", IncidentID: "incident-2", Input: json.RawMessage(`{}`)}, Provider: &airpc.Provider{ID: "provider-1", BaseURL: modelServer.URL, Model: "test-model", APIKey: "secret"}})
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
