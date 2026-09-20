package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestDNSBatchOperationAuthorizationAndPersistence(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "batch-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, user.ID)
	one := model.Server{Name: "batch-one", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "batch-agent"}
	if err := db.CreateServer(ctx, &one); err != nil {
		t.Fatal(err)
	}
	two := one
	two.ID = 0
	two.Name = "batch-two"
	two.AgentID = ""
	two.Status = model.ServerOffline
	if err := db.CreateServer(ctx, &two); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(dnsBatchInput{ServerIDs: []int64{one.ID, two.ID}})
	restricted := principal
	restricted.ResourceFilter = json.RawMessage(`{"servers":{"mode":"selected","ids":[` + jsonNumber(one.ID) + `]}}`)
	if _, err := s.applyDNSBatch(ctx, restricted, input); err == nil {
		t.Fatal("out-of-scope target accepted")
	}
	ids, err := db.ListTaskOperationIDs(ctx, []int64{one.ID, two.ID}, "", "", 100)
	if err != nil || len(ids) != 0 {
		t.Fatalf("authorization failure persisted intent: %v %v", ids, err)
	}
	applyAutomationChangeset(t, s, principal, "batch-dns-intent", automation.OperationRequest{Capability: "servers.dns_test_batch", Input: input})
	ids, err = db.ListTaskOperationIDs(ctx, []int64{one.ID, two.ID}, "", "", 100)
	if err != nil || len(ids) != 1 {
		t.Fatalf("missing operation: %v %v", ids, err)
	}
	op, err := db.GetTaskOperation(ctx, ids[0], []int64{one.ID, two.ID})
	if err != nil {
		t.Fatal(err)
	}
	if op.ActorPrincipal != principal.ID || op.ActorUserID == nil || *op.ActorUserID != user.ID || len(op.Targets) != 2 {
		t.Fatalf("untrusted actor or incomplete target set: %+v", op)
	}
	states := map[string]int{}
	for _, target := range op.Targets {
		states[target.State]++
	}
	if states["pending"] != 1 || states["failed"] != 1 {
		t.Fatalf("offline target lost: %+v", op.Targets)
	}
}

func jsonNumber(id int64) string { raw, _ := json.Marshal(id); return string(raw) }
