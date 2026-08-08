package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAIProviderTestUsesDraftAndStoredCredentials(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "ai-test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	server.aiTestTimeout = 10 * time.Second
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	provider := request(t, handler, http.MethodPost, "/api/v2/ai/providers", token, map[string]any{
		"name": "local", "base_url": "http://127.0.0.1:11434/v1", "model": "manual", "api_key": "stored-key", "enabled": true,
	}, http.StatusCreated)["data"].(map[string]any)
	providerID := provider["id"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketFile, err := os.CreateTemp("/tmp", "oboard-ai-test-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := socketFile.Name()
	_ = socketFile.Close()
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	if err := server.StartAIWorkerRPC(ctx, socketPath); err != nil {
		t.Fatal(err)
	}
	waitForSocket(t, socketPath)
	workerClient := testUnixClient(socketPath)

	for _, testCase := range []struct {
		name        string
		requestBody map[string]any
		wantKey     string
	}{
		{name: "stored credential", requestBody: map[string]any{"provider_id": providerID, "base_url": "http://127.0.0.1:11434/v1", "model": "manual"}, wantKey: "stored-key"},
		{name: "draft credential", requestBody: map[string]any{"provider_id": providerID, "base_url": "http://127.0.0.1:11434/v1", "model": "manual", "api_key": "draft-key"}, wantKey: "draft-key"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workerErr := make(chan error, 1)
			go func() {
				var lease airpc.AITestLeaseResponse
				if err := testRPCJSON(workerClient, "/v1/ai-test/lease", airpc.AITestLeaseRequest{WorkerID: "worker-test-1"}, &lease); err != nil {
					workerErr <- err
					return
				}
				if lease.Request == nil || lease.Request.Endpoint.Credential != testCase.wantKey || lease.Request.Endpoint.BaseURL != "http://127.0.0.1:11434/v1" || lease.Request.Model != "manual" {
					workerErr <- fmt.Errorf("unexpected lease: %#v", lease.Request)
					return
				}
				workerErr <- testRPCJSON(workerClient, "/v1/ai-test/"+lease.Request.ID+"/complete", airpc.AITestCompleteRequest{WorkerID: "worker-test-1", OK: true, RequestJSON: `{"model":"manual"}`, ResponseJSON: `{"choices":[{"message":{"content":"ok"}}]}`, StatusCode: 200, DurationMS: 123, Content: "ok"}, nil)
			}()
			response := request(t, handler, http.MethodPost, "/api/v2/ai/provider-test", token, testCase.requestBody, http.StatusOK)
			if err := <-workerErr; err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(response)
			if strings.Contains(string(encoded), testCase.wantKey) {
				t.Fatal("AI provider test response leaked the provider credential")
			}
			data := response["data"].(map[string]any)
			if data["ok"] != true || data["status_code"] != float64(200) || data["duration_ms"] != float64(123) || data["content"] != "ok" {
				t.Fatalf("test result = %#v", data)
			}
			if !strings.Contains(data["request_json"].(string), `"model"`) || !strings.Contains(data["response_json"].(string), `"choices"`) {
				t.Fatalf("raw JSON missing: %#v", data)
			}
		})
	}
}

func TestAIProviderTestFailureSurfacesDetailAndRedactsRawResponse(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "ai-test-fail.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newTestServer(db, "test-secret", "")
	server.aiTestTimeout = 10 * time.Second
	socketFile, err := os.CreateTemp("/tmp", "oboard-ai-test-fail-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := socketFile.Name()
	_ = socketFile.Close()
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	if err := server.StartAIWorkerRPC(ctx, socketPath); err != nil {
		t.Fatal(err)
	}
	waitForSocket(t, socketPath)
	workerClient := testUnixClient(socketPath)
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	workerErr := make(chan error, 1)
	go func() {
		var lease airpc.AITestLeaseResponse
		if err := testRPCJSON(workerClient, "/v1/ai-test/lease", airpc.AITestLeaseRequest{WorkerID: "worker-test-fail"}, &lease); err != nil {
			workerErr <- err
			return
		}
		if lease.Request == nil {
			workerErr <- fmt.Errorf("no AI test lease")
			return
		}
		workerErr <- testRPCJSON(workerClient, "/v1/ai-test/"+lease.Request.ID+"/fail", airpc.AITestFailRequest{WorkerID: "worker-test-fail", Error: "model endpoint returned HTTP 401: invalid api key", RequestJSON: `{"model":"manual"}`, ResponseJSON: `{"error":{"message":"bad key secret-key"}}`, StatusCode: 401, DurationMS: 55}, nil)
	}()
	response := request(t, handler, http.MethodPost, "/api/v2/ai/provider-test", token, map[string]any{"base_url": "http://127.0.0.1:11434/v1", "model": "manual", "api_key": "secret-key"}, http.StatusOK)
	if err := <-workerErr; err != nil {
		t.Fatal(err)
	}
	data := response["data"].(map[string]any)
	if data["ok"] != false || data["status_code"] != float64(401) || data["duration_ms"] != float64(55) {
		t.Fatalf("test failure result = %#v", data)
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "secret-key") {
		t.Fatal("AI provider test failure leaked the provider credential")
	}
	if !strings.Contains(data["message"].(string), "HTTP 401") || !strings.Contains(data["response_json"].(string), "[redacted]") {
		t.Fatalf("failure detail missing: %#v", data)
	}
}

func TestAIProviderTestRejectsInvalidInput(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "ai-test-invalid.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, handler, http.MethodPost, "/api/v2/ai/provider-test", token, map[string]any{"base_url": "https://api.example.com/v1", "api_key": "secret"}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v2/ai/provider-test", token, map[string]any{"base_url": "https://api.example.com/v1?key=value", "api_key": "secret", "model": "m"}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v2/ai/provider-test", token, map[string]any{"base_url": "https://api.example.com/v1", "model": "m"}, http.StatusBadRequest)
}
