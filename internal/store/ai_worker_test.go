package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAIProviderPersistsAPIFormat(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "ai-worker.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	legacy := &model.AIProvider{ID: "legacy", Name: "legacy", BaseURL: "https://api.example.com/v1", Model: "m", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetAIProvider(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIFormat != "chat_completions" {
		t.Fatalf("legacy provider format = %q", stored.APIFormat)
	}

	responses := &model.AIProvider{ID: "responses", Name: "responses", BaseURL: "https://api.openai.com/v1", Model: "m", APIFormat: "responses", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, responses); err != nil {
		t.Fatal(err)
	}
	stored, err = db.GetAIProvider(ctx, "responses")
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIFormat != "responses" {
		t.Fatalf("responses provider format = %q", stored.APIFormat)
	}
}
