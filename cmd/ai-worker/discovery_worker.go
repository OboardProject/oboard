package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/airpc"
)

func runModelDiscoveryLoop(ctx context.Context, runtime *workerRuntime, client *http.Client, workerID string, retryInterval time.Duration) {
	for {
		err := runModelDiscoveryOnce(ctx, runtime, client, workerID)
		logLoopError("AI model discovery", err)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !sleepContext(ctx, retryInterval) {
			return
		}
	}
}

func runModelDiscoveryOnce(ctx context.Context, runtime *workerRuntime, client *http.Client, workerID string) error {
	var lease airpc.ModelDiscoveryLeaseResponse
	if err := rpcJSON(ctx, client, http.MethodPost, "http://unix/v1/model-discovery/lease", airpc.ModelDiscoveryLeaseRequest{WorkerID: workerID}, &lease); err != nil {
		return err
	}
	if lease.Request == nil {
		return nil
	}
	models, err := discoverModels(ctx, runtime, lease.Request)
	callback, cancel := callbackContext()
	defer cancel()
	if err != nil {
		_ = rpcJSON(callback, client, http.MethodPost, "http://unix/v1/model-discovery/"+lease.Request.ID+"/fail", airpc.ModelDiscoveryFailRequest{WorkerID: workerID, Error: bounded(err.Error(), 1000)}, nil)
		return err
	}
	return rpcJSON(callback, client, http.MethodPost, "http://unix/v1/model-discovery/"+lease.Request.ID+"/complete", airpc.ModelDiscoveryCompleteRequest{WorkerID: workerID, Models: models}, nil)
}

func discoverModels(ctx context.Context, runtime *workerRuntime, discovery *airpc.ModelDiscoveryRequest) ([]string, error) {
	if discovery == nil {
		return nil, errors.New("model discovery request is required")
	}
	endpoint := runtimeEndpoint(discovery.ProviderID, discovery.Endpoint, discovery.BaseURL, discovery.APIFormat, discovery.APIKey)
	models, err := runtime.router.ListModels(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	unique := map[string]struct{}{}
	for _, item := range models {
		id := strings.TrimSpace(item.ID)
		if id == "" || len(id) > 512 {
			return nil, errors.New("invalid model ID")
		}
		unique[id] = struct{}{}
	}
	if len(unique) == 0 || len(unique) > 1000 {
		return nil, errors.New("model discovery returned an invalid number of models")
	}
	result := make([]string, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func runtimeEndpoint(providerID string, endpoint airpc.RuntimeEndpoint, legacyBaseURL, legacyFormat, legacyKey string) aiprovider.RuntimeEndpoint {
	if endpoint.ID == "" {
		endpoint = airpc.RuntimeEndpoint{ID: "draft", Name: "Draft", BaseURL: legacyBaseURL, APIStyle: legacyAPIStyle(legacyFormat), AuthMode: aiprovider.AuthModeBearer, Credential: legacyKey, Priority: 100, Enabled: true, TimeoutMS: 60000, MaxRetries: 2}
		if parsed, err := url.Parse(legacyBaseURL); err == nil && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
			endpoint.AllowPrivateNetwork = true
		}
	}
	return aiprovider.RuntimeEndpoint{ID: endpoint.ID, ProviderID: providerID, Name: endpoint.Name, BaseURL: endpoint.BaseURL, APIStyle: aiprovider.APIStyle(endpoint.APIStyle), AuthMode: endpoint.AuthMode, Credential: endpoint.Credential, AnthropicVersion: endpoint.AnthropicVersion, Headers: endpoint.Headers, ModelsPath: endpoint.ModelsPath, GeneratePath: endpoint.GeneratePath, ModelOverride: endpoint.ModelOverride, Priority: endpoint.Priority, Enabled: endpoint.Enabled, TimeoutMS: endpoint.TimeoutMS, MaxRetries: endpoint.MaxRetries, AllowPrivateNetwork: endpoint.AllowPrivateNetwork, Capability: endpoint.Capability}
}

func legacyAPIStyle(format string) string {
	if strings.TrimSpace(format) == "responses" || strings.TrimSpace(format) == "openai_responses" {
		return string(aiprovider.APIStyleOpenAIResponses)
	}
	return string(aiprovider.APIStyleOpenAIChatCompletions)
}
