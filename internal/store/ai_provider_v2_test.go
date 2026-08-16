package store

import (
	"context"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func TestMigrateAIProvidersV2ReencryptsCredentialAndIsIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	const secret = "test-session-secret-with-at-least-32-bytes"
	legacyCredential, err := security.EncryptSecret(secret, "ai-provider-credential:legacy", "provider-key")
	if err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "legacy", Name: "Legacy", BaseURL: "https://api.example.com/v1", Model: "legacy-model", APIFormat: "responses", CredentialEncrypted: legacyCredential, Enabled: true, Capability: &model.AIProviderCapability{AuditReady: true, StructuredOutput: model.AuditProviderStructuredJSONSchema, OutputMode: model.AuditOutputModeStrictSchema}}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := db.MigrateAIProvidersV2(ctx, secret); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}
	stored, err := db.GetAIProvider(ctx, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderKind != "openai" || stored.DefaultModel != "legacy-model" || stored.RoutingStrategy != "ordered_failover" {
		t.Fatalf("migrated provider = %#v", stored)
	}
	if stored.Capability != nil || len(stored.Endpoints) != 1 {
		t.Fatalf("capability/endpoints = %#v / %#v", stored.Capability, stored.Endpoints)
	}
	endpoint := stored.Endpoints[0]
	if endpoint.APIStyle != "openai_responses" || endpoint.CredentialEncrypted == legacyCredential {
		t.Fatalf("migrated endpoint = %#v", endpoint)
	}
	plain, err := security.DecryptSecret(secret, "ai-provider-endpoint-credential:"+endpoint.ID, endpoint.CredentialEncrypted)
	if err != nil || plain != "provider-key" {
		t.Fatalf("decrypt migrated credential = %q, %v", plain, err)
	}
	if stored.BaseURL != "" || stored.CredentialEncrypted != "" {
		t.Fatal("legacy canonical endpoint fields were retained after migration")
	}
	if err := db.DeleteAIProviderEndpoint(ctx, stored.ID, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAIProvidersV2(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if endpoints, err := db.ListAIProviderEndpoints(ctx, stored.ID); err != nil || len(endpoints) != 0 {
		t.Fatalf("deleted migrated endpoint was recreated: endpoints=%#v err=%v", endpoints, err)
	}
}

func TestMigrateAIProvidersV2RollsBackOnCredentialFailure(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.CreateAIProvider(ctx, &model.AIProvider{ID: "broken", Name: "Broken", BaseURL: "https://api.example.com/v1", Model: "m", CredentialEncrypted: "not-ciphertext"}); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAIProvidersV2(ctx, "test-session-secret-with-at-least-32-bytes"); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	endpoints, err := db.ListAIProviderEndpoints(ctx, "broken")
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("partial migration created endpoints: %#v", endpoints)
	}
}

func TestAIProviderV2MigrationIgnoresEndpointlessV2Provider(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	provider := &model.AIProvider{
		ID:              "v2",
		Name:            "V2",
		ProviderKind:    "custom",
		DefaultModel:    "model",
		RoutingStrategy: "ordered_failover",
		Enabled:         true,
	}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAIProvidersV2(ctx, "test-session-secret-with-at-least-32-bytes"); err != nil {
		t.Fatal(err)
	}
	endpoints, err := db.ListAIProviderEndpoints(ctx, provider.ID)
	if err != nil || len(endpoints) != 0 {
		t.Fatalf("endpointless V2 provider was treated as legacy: endpoints=%#v err=%v", endpoints, err)
	}
}

func TestAIProviderEndpointCapabilityUsesDigest(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	provider := &model.AIProvider{ID: "provider", Name: "Provider", DefaultModel: "m"}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	endpoint := &model.AIProviderEndpoint{ID: "endpoint", ProviderID: provider.ID, Name: "Primary", BaseURL: "https://api.example.com/v1", APIStyle: "openai_responses", AuthMode: "bearer", Enabled: true}
	if err := db.CreateAIProviderEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	capability := &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, ProviderID: provider.ID, EndpointID: endpoint.ID, Model: "m", ConfigDigest: "digest-a", ConnectivityOK: true, AuthenticationOK: true, TextSupported: true, AuditReady: true, StructuredOutput: model.AuditProviderStructuredPromptedJSON, OutputMode: model.AuditOutputModeText}
	if err := db.UpsertAIProviderEndpointCapability(ctx, capability); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetAIProviderEndpointCapability(ctx, endpoint.ID, "m", "digest-a")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.AuditReady || stored.StructuredOutput != model.AuditProviderStructuredPromptedJSON || stored.OutputMode != model.AuditOutputModeText {
		t.Fatalf("stored capability = %#v", stored)
	}
	if _, err := db.GetAIProviderEndpointCapability(ctx, endpoint.ID, "m", "digest-b"); err == nil {
		t.Fatal("changed config digest reused stale capability")
	}
}
