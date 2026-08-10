package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAuditReadSurfaces(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	connection, err := server.mcpAuditConnectionOverview(ctx, principal, 24)
	if err != nil {
		t.Fatalf("connection audit overview: %v", err)
	}
	if _, ok := connection.(model.ConnectionAuditOverview); !ok {
		t.Fatalf("unexpected connection audit payload: %#v", connection)
	}
	subscription, err := server.mcpAuditSubscriptionOverview(ctx, principal, 24)
	if err != nil {
		t.Fatalf("subscription audit overview: %v", err)
	}
	if _, ok := subscription.(model.SubscriptionAuditOverview); !ok {
		t.Fatalf("unexpected subscription audit payload: %#v", subscription)
	}
	risk, err := server.mcpAuditRiskOverview(ctx, principal, 24)
	if err != nil {
		t.Fatalf("risk overview: %v", err)
	}
	if _, ok := risk.(model.CombinedAuditOverview); !ok {
		t.Fatalf("unexpected risk payload: %#v", risk)
	}
	logs, err := server.mcpAuditLogs(ctx, principal, 100)
	if err != nil {
		t.Fatalf("audit logs: %v", err)
	}
	encoded, _ := json.Marshal(logs)
	if len(encoded) == 0 {
		t.Fatal("empty audit logs payload")
	}
	reviews, err := server.mcpAuditAIReviews(ctx, principal, 50)
	if err != nil {
		t.Fatalf("ai reviews: %v", err)
	}
	if _, ok := reviews.(map[string]any); !ok {
		t.Fatalf("unexpected ai reviews payload: %#v", reviews)
	}
}

func TestAuditReviewDeleteCapabilityAppliesThroughChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "55555555-5555-4555-8555-555555555555", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	review := &model.AuditReview{
		ID: "review-delete", RequestID: "request-delete", ProviderID: provider.ID, RequestedBy: admin.ID,
		Scope:         model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "all"}, Servers: model.AuditReviewSelector{Mode: "all"}},
		EvidenceTypes: []string{"connection"}, WindowStartedAt: nowTime.Add(-time.Hour), WindowEndedAt: nowTime, SnapshotAt: nowTime, PrivacyMode: "masked",
	}
	job := model.AuditReviewJob{ID: "job-delete", ReviewID: review.ID, ProviderID: provider.ID, Kind: "evidence", Input: json.RawMessage(`{"evidence":[]}`)}
	if err := db.CreateAuditReview(ctx, review, nil, []model.AuditReviewJob{job}); err != nil {
		t.Fatal(err)
	}
	descriptor, ok := server.capabilities.Get("audit.ai_reviews.delete")
	if !ok || !descriptor.MCPEnabled || !descriptor.Executable || !descriptor.Destructive || descriptor.RiskClass != 3 {
		t.Fatalf("unsafe or missing delete capability: %#v", descriptor)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	input := json.RawMessage(`{"review_id":"review-delete","confirm":true}`)
	operation := automation.OperationRequest{Capability: "audit.ai_reviews.delete", Input: input}
	if _, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{operation}}); !errors.Is(err, store.ErrAuditReviewActive) {
		t.Fatalf("active review delete validation error = %v", err)
	}
	if err := db.CancelAuditReview(ctx, review.ID); err != nil {
		t.Fatal(err)
	}
	applyAutomationChangeset(t, server, principal, "delete-ai-review", operation)
	if _, err := db.GetAuditReview(ctx, review.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted review lookup error = %v", err)
	}
}
