package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestServerUpdateIntentUsesAuthenticatedActor(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "intent-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111113", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBootstrapAdmin(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	v := model.Server{Name: "intent-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOffline}
	if err := db.CreateServer(ctx, &v); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"server_id": v.ID, "changes": map[string]any{"listen_mode": "ipv4_only"}})
	applyAutomationChangeset(t, srv, principal, "intent-actor", automation.OperationRequest{Capability: "servers.update", Input: raw})
	ids, err := db.ListTaskOperationIDs(ctx, []int64{v.ID}, "", "", 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("operation: %v %v", ids, err)
	}
	op, err := db.GetTaskOperation(ctx, ids[0], []int64{v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if op.ActorPrincipal != principal.ID || op.ActorUserID == nil || *op.ActorUserID != admin.ID || op.Kind != "servers.update" {
		t.Fatalf("wrong actor: %+v", op)
	}
	if len(op.Targets) != 1 || op.Targets[0].DesiredRevision == nil {
		t.Fatal("missing saved revision")
	}
}
