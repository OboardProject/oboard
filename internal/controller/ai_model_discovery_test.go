package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAIProviderModelDiscoveryUsesDraftAndStoredCredentials(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "model-discovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	provider := request(t, handler, http.MethodPost, "/api/v2/ai/providers", token, map[string]any{
		"name": "local", "base_url": "http://127.0.0.1:11434/v1", "model": "manual", "api_key": "stored-key", "enabled": true,
	}, http.StatusCreated)["data"].(map[string]any)
	providerID := provider["id"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketFile, err := os.CreateTemp("/tmp", "oboard-ai-worker-*.sock")
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
		{name: "stored credential", requestBody: map[string]any{"provider_id": providerID, "base_url": "http://127.0.0.1:11434/v1"}, wantKey: "stored-key"},
		{name: "draft credential", requestBody: map[string]any{"provider_id": providerID, "base_url": "http://127.0.0.1:11434/v1", "api_key": "draft-key"}, wantKey: "draft-key"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workerErr := make(chan error, 1)
			go func() {
				var lease airpc.ModelDiscoveryLeaseResponse
				if err := testRPCJSON(workerClient, "/v1/model-discovery/lease", airpc.ModelDiscoveryLeaseRequest{WorkerID: "worker-1"}, &lease); err != nil {
					workerErr <- err
					return
				}
				if lease.Request == nil || lease.Request.APIKey != testCase.wantKey || lease.Request.BaseURL != "http://127.0.0.1:11434/v1" {
					workerErr <- fmt.Errorf("unexpected lease: %#v", lease.Request)
					return
				}
				workerErr <- testRPCJSON(workerClient, "/v1/model-discovery/"+lease.Request.ID+"/complete", airpc.ModelDiscoveryCompleteRequest{WorkerID: "worker-1", Models: []string{"z-model", "a-model", "z-model"}}, nil)
			}()
			response := request(t, handler, http.MethodPost, "/api/v2/ai/provider-models", token, testCase.requestBody, http.StatusOK)
			if err := <-workerErr; err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(response)
			if strings.Contains(string(encoded), testCase.wantKey) {
				t.Fatal("model discovery response leaked the provider credential")
			}
			models := response["data"].(map[string]any)["models"].([]any)
			if len(models) != 2 || models[0] != "a-model" || models[1] != "z-model" {
				t.Fatalf("models = %#v", models)
			}
		})
	}
}

func TestAIProviderModelDiscoveryRejectsMissingCredentialAndTimesOut(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "model-discovery-errors.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	server.aiModelDiscoveryTimeout = 20 * time.Millisecond
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, handler, http.MethodPost, "/api/v2/ai/provider-models", token, map[string]any{"base_url": "https://api.example.com/v1"}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v2/ai/provider-models", token, map[string]any{"base_url": "https://api.example.com/v1?key=value", "api_key": "secret"}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v2/ai/provider-models", token, map[string]any{"base_url": "https://api.example.com/v1", "api_key": "secret"}, http.StatusServiceUnavailable)
}

func TestAIModelDiscoveryFailureSurfacesWorkerDetail(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "model-discovery-detail.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newTestServer(db, "test-secret", "")
	server.aiModelDiscoveryTimeout = 5 * time.Second
	socketFile, err := os.CreateTemp("/tmp", "oboard-ai-worker-detail-*.sock")
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
		var lease airpc.ModelDiscoveryLeaseResponse
		if err := testRPCJSON(workerClient, "/v1/model-discovery/lease", airpc.ModelDiscoveryLeaseRequest{WorkerID: "worker-detail"}, &lease); err != nil {
			workerErr <- err
			return
		}
		if lease.Request == nil {
			workerErr <- fmt.Errorf("no discovery lease")
			return
		}
		workerErr <- testRPCJSON(workerClient, "/v1/model-discovery/"+lease.Request.ID+"/fail", airpc.ModelDiscoveryFailRequest{WorkerID: "worker-detail", Error: "model list endpoint returned HTTP 401: invalid api key"}, nil)
	}()
	response := request(t, handler, http.MethodPost, "/api/v2/ai/provider-models", token, map[string]any{"base_url": "https://api.example.com/v1", "api_key": "secret", "api_format": "responses"}, http.StatusBadGateway)
	if err := <-workerErr; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(response)
	if !strings.Contains(string(encoded), "HTTP 401") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("discovery failure response = %s", encoded)
	}
}

func TestAIModelDiscoveryQueueIsBounded(t *testing.T) {
	queue := newAIModelDiscoveryQueue()
	for index := 0; index < aiModelDiscoveryCapacity; index++ {
		id := fmt.Sprintf("request-%d", index)
		if _, err := queue.submit(airpc.ModelDiscoveryRequest{ID: id}, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.submit(airpc.ModelDiscoveryRequest{ID: "overflow"}, "overflow"); !errors.Is(err, errAIModelDiscoveryBusy) {
		t.Fatalf("overflow error = %v", err)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s was not created", path)
}

func testUnixClient(socketPath string) *http.Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath)
	}}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}
}

func testRPCJSON(client *http.Client, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	response, err := client.Post("http://unix"+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("RPC %s returned HTTP %d", path, response.StatusCode)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}
