package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAIProviderV2EndpointCRUDPreservesAndRemovesCredential(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "provider-v2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	provider := request(t, handler, http.MethodPost, "/api/v1/ai/providers", token, map[string]any{"name": "Production", "provider_kind": "anthropic", "default_model": "claude-test", "routing_strategy": "ordered_failover", "enabled": true}, http.StatusCreated)["data"].(map[string]any)
	providerID := provider["id"].(string)
	first := request(t, handler, http.MethodPost, "/api/v1/ai/providers/"+providerID+"/endpoints", token, map[string]any{"name": "Native", "api_key": "first-key", "priority": 10}, http.StatusCreated)["data"].(map[string]any)
	second := request(t, handler, http.MethodPost, "/api/v1/ai/providers/"+providerID+"/endpoints", token, map[string]any{"name": "Compatibility", "base_url": "https://api.example.com/v1", "api_style": "openai_chat_completions", "auth_mode": "bearer", "api_key": "second-key", "priority": 20}, http.StatusCreated)["data"].(map[string]any)
	if first["api_style"] != "anthropic_messages" || first["auth_mode"] != "x_api_key" || first["anthropic_version"] != "2023-06-01" {
		t.Fatalf("Anthropic template=%#v", first)
	}
	firstID := first["id"].(string)
	draftBaseURL := "https://draft.example.com/v1"
	_, runtimeDraft, err := server.resolveAIRequestEndpoint(context.Background(), providerID, firstID, "", &aiEndpointRequest{BaseURL: &draftBaseURL}, "", "", "")
	if err != nil || runtimeDraft.BaseURL != draftBaseURL || runtimeDraft.Credential != "first-key" {
		t.Fatalf("draft endpoint runtime=%#v err=%v", runtimeDraft, err)
	}
	stored, err := db.GetAIProviderEndpoint(context.Background(), providerID, firstID)
	if err != nil {
		t.Fatal(err)
	}
	original := stored.CredentialEncrypted
	request(t, handler, http.MethodPatch, "/api/v1/ai/providers/"+providerID+"/endpoints/"+firstID, token, map[string]any{"name": "Native primary", "api_key": ""}, http.StatusOK)
	stored, _ = db.GetAIProviderEndpoint(context.Background(), providerID, firstID)
	if stored.CredentialEncrypted != original {
		t.Fatal("empty API key replaced the stored credential")
	}
	request(t, handler, http.MethodPatch, "/api/v1/ai/providers/"+providerID+"/endpoints/"+firstID, token, map[string]any{"api_key": "replacement-key"}, http.StatusOK)
	stored, _ = db.GetAIProviderEndpoint(context.Background(), providerID, firstID)
	plain, err := security.DecryptSecret("test-secret", "ai-provider-endpoint-credential:"+firstID, stored.CredentialEncrypted)
	if err != nil || plain != "replacement-key" {
		t.Fatalf("replacement credential=%q err=%v", plain, err)
	}
	response := request(t, handler, http.MethodGet, "/api/v1/ai/providers/"+providerID, token, nil, http.StatusOK)
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "replacement-key") || strings.Contains(string(encoded), stored.CredentialEncrypted) {
		t.Fatal("provider API leaked credential material")
	}
	request(t, handler, http.MethodPatch, "/api/v1/ai/providers/"+providerID+"/endpoints/"+firstID, token, map[string]any{"remove_credential": true}, http.StatusOK)
	stored, _ = db.GetAIProviderEndpoint(context.Background(), providerID, firstID)
	if stored.CredentialEncrypted != "" {
		t.Fatal("explicit credential removal failed")
	}
	secondID := second["id"].(string)
	request(t, handler, http.MethodDelete, "/api/v1/ai/providers/"+providerID+"/endpoints/"+secondID, token, nil, http.StatusOK)
	remaining, _ := db.GetAIProvider(context.Background(), providerID)
	if len(remaining.Endpoints) != 1 {
		t.Fatalf("provider endpoints=%#v", remaining.Endpoints)
	}
	request(t, handler, http.MethodDelete, "/api/v1/ai/providers/"+providerID, token, nil, http.StatusOK)
	if _, err := db.GetAIProviderEndpoint(context.Background(), providerID, firstID); err == nil {
		t.Fatal("provider deletion did not cascade to endpoints")
	}
}

func TestAIProviderV2RejectsReservedHeaders(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "provider-headers.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	provider := request(t, handler, http.MethodPost, "/api/v1/ai/providers", token, map[string]any{"name": "Custom", "provider_kind": "custom", "default_model": "m"}, http.StatusCreated)["data"].(map[string]any)
	request(t, handler, http.MethodPost, "/api/v1/ai/providers/"+provider["id"].(string)+"/endpoints", token, map[string]any{"name": "Implicit", "base_url": "https://api.example.com/v1", "auth_mode": "bearer", "api_key": "key"}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v1/ai/providers/"+provider["id"].(string)+"/endpoints", token, map[string]any{"name": "Unsafe", "base_url": "https://api.example.com/v1", "api_style": "openai_responses", "auth_mode": "bearer", "api_key": "key", "headers": map[string]string{"Authorization": "override"}}, http.StatusBadRequest)
}

func TestValidateAuditRouteEvidenceRejectsStaleOrNonReadyCapability(t *testing.T) {
	capability := &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, ProviderID: "provider", EndpointID: "endpoint", APIStyle: string(aiprovider.APIStyleOpenAIResponses), Model: "model", ConfigDigest: "current", ConnectivityOK: true, AuthenticationOK: true, TextSupported: true, AuditReady: true, StructuredOutput: model.AuditProviderStructuredPromptedJSON, OutputMode: model.AuditOutputModeText}
	provider := &model.AIProvider{ID: "provider", DefaultModel: "model", Endpoints: []model.AIProviderEndpoint{{ID: "endpoint", APIStyle: string(aiprovider.APIStyleOpenAIResponses), Enabled: true, Capability: capability}}}
	route := &airpc.RouteEvidence{ProviderID: provider.ID, EndpointID: "endpoint", APIStyle: string(aiprovider.APIStyleOpenAIResponses), Model: "model", CapabilityProfileVersion: model.AuditProviderProfileVersion, CapabilityConfigDigest: "current"}
	if got, err := validateAuditRouteEvidence(provider, route); err != nil || got != capability {
		t.Fatalf("valid route rejected: capability=%#v err=%v", got, err)
	}
	route.CapabilityConfigDigest = "stale"
	if _, err := validateAuditRouteEvidence(provider, route); err == nil {
		t.Fatal("stale route capability was accepted")
	}
	route.CapabilityConfigDigest = "current"
	provider.Endpoints[0].Capability.AuditReady = false
	provider.Endpoints[0].Capability.StructuredOutput = model.AuditProviderStructuredNone
	if _, err := validateAuditRouteEvidence(provider, route); err == nil {
		t.Fatal("non-audit-ready route capability was accepted")
	}
}

func TestValidateAITestCapabilityAcceptsPromptedJSONAndRejectsContradictions(t *testing.T) {
	capability := &model.AIProviderCapability{
		ProviderProfileVersion:  model.AuditProviderProfileVersion,
		ProviderID:              "provider",
		EndpointID:              "endpoint",
		Model:                   "model",
		ConfigDigest:            "digest",
		TestedAt:                time.Now().UTC(),
		ConnectivityOK:          true,
		AuthenticationOK:        true,
		TextSupported:           true,
		AuditReady:              true,
		StructuredOutput:        model.AuditProviderStructuredPromptedJSON,
		OutputMode:              model.AuditOutputModeText,
		MaxVerifiedOutputTokens: 4096,
	}
	if err := validateAITestCapability(capability); err != nil {
		t.Fatalf("prompted JSON capability rejected: %v", err)
	}
	capability.StructuredOutput = model.AuditProviderStructuredNone
	if err := validateAITestCapability(capability); err == nil {
		t.Fatal("contradictory audit-ready capability was accepted")
	}
	capability.AuditReady = false
	if err := validateAITestCapability(capability); err != nil {
		t.Fatalf("consistent non-ready capability rejected: %v", err)
	}
}

func TestValidateAITestTargetBindsEndpointModelAndDigest(t *testing.T) {
	request := airpc.AITestRequest{ID: "test", ProviderID: "provider", Model: "model", Endpoint: airpc.RuntimeEndpoint{ID: "endpoint", BaseURL: "https://api.example.com/v1", APIStyle: string(aiprovider.APIStyleOpenAIResponses), AuthMode: aiprovider.AuthModeBearer, Headers: map[string]string{"X-Tenant": "one"}}}
	runtimeEndpoint := aiprovider.RuntimeEndpoint{ID: "endpoint", ProviderID: "provider", BaseURL: request.Endpoint.BaseURL, APIStyle: aiprovider.APIStyleOpenAIResponses, AuthMode: aiprovider.AuthModeBearer, Headers: request.Endpoint.Headers}
	capability := &model.AIProviderCapability{ProviderID: "provider", EndpointID: "endpoint", APIStyle: string(aiprovider.APIStyleOpenAIResponses), Model: "model", ConfigDigest: aiprovider.ConfigDigest(runtimeEndpoint, "model")}
	if err := validateAITestTarget(request, capability); err != nil {
		t.Fatal(err)
	}
	capability.ConfigDigest = "stale"
	if err := validateAITestTarget(request, capability); err == nil {
		t.Fatal("stale capability digest was accepted")
	}
	request.ProviderID = ""
	capability.ProviderID = "draft:test"
	capability.ConfigDigest = aiprovider.ConfigDigest(runtimeEndpoint, "model")
	if err := validateAITestTarget(request, capability); err != nil {
		t.Fatalf("draft target rejected: %v", err)
	}
}
