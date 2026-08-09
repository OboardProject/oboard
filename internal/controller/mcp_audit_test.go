package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
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
