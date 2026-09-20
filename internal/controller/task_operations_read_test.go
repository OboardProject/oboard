package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"testing"
)

func TestTaskOperationMalformedScopeDenies(t *testing.T) {
	p := application.Principal{ResourceFilter: json.RawMessage(`{"servers":{"mode":"all"},"users":{"ids":"invalid"}}`)}
	ids, all := taskOperationScope(p)
	if all || len(ids) != 0 {
		t.Fatal("malformed resource filter widened operation access")
	}
}

func TestTaskOperationReadsPreparationFailureAndBoundary(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	p := application.Principal{ID: "test-admin", Type: "human", Role: model.RoleAdmin, Scopes: []string{"*"}}
	item := store.OperationTask{Target: model.TaskOperationTarget{Type: "server", ID: "4321", State: "failed", CauseCode: "dns_plan_invalid"}, Task: model.AgentTask{ServerID: 4321}}
	op, _, err := db.CreateTaskOperation(ctx, model.TaskOperation{Kind: "servers.dns_test_batch", Source: "human", ActorPrincipal: p.ID}, []store.OperationTask{item})
	if err != nil {
		t.Fatal(err)
	}
	// A newly constructed controller sees a failure with no task and no live server.
	s = newTestServer(db, "test-secret", "")
	raw, _ := json.Marshal(map[string]string{"id": op.ID})
	got, err := s.queryMCPCapabilityFallback(ctx, p, "task_operations.get", raw)
	if err != nil {
		t.Fatal(err)
	}
	record := got.(model.TaskOperation)
	if len(record.Targets) != 1 || record.Targets[0].CauseCode != "dns_plan_invalid" {
		t.Fatalf("lost target: %+v", record)
	}
	got, err = s.queryMCPCapabilityFallback(ctx, p, "task_operations.list", json.RawMessage(`{"limit":1}`))
	if err != nil || len(got.(map[string]any)["operations"].([]model.TaskOperation)) != 1 {
		t.Fatalf("list: %v %v", got, err)
	}
	p.ResourceFilter = json.RawMessage(`{"servers":{"mode":"selected","ids":[1]}}`)
	if _, err = s.queryMCPCapabilityFallback(ctx, p, "task_operations.get", raw); err == nil {
		t.Fatal("cross-boundary get")
	}
	got, err = s.queryMCPCapabilityFallback(ctx, p, "task_operations.list", json.RawMessage(`{}`))
	if err != nil || len(got.(map[string]any)["operations"].([]model.TaskOperation)) != 0 {
		t.Fatalf("cross-boundary list: %v %v", got, err)
	}
	item.DNSRun = &model.DNSBenchmarkRun{}
	if _, err = db.RetryTaskOperationTarget(ctx, op.ID, item); err == nil {
		t.Fatal("run retry silently discarded metadata")
	}
}

func TestDNSBatch101AndCapacityValidation(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	p := application.Principal{ID: "admin", Role: model.RoleAdmin, Scopes: []string{"*"}}
	ids := []int64{}
	for i := 0; i < 101; i++ {
		server := model.Server{Name: fmt.Sprintf("batch-capacity-%d", i), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOffline}
		if err := db.CreateServer(ctx, &server); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, server.ID)
	}
	raw, _ := json.Marshal(dnsBatchInput{ServerIDs: ids})
	if _, err := s.validateDNSBatch(ctx, p, raw); err != nil {
		t.Fatalf("101 rejected: %v", err)
	}
	raw, _ = json.Marshal(dnsBatchInput{ServerIDs: make([]int64, 1001)})
	if _, err := s.validateDNSBatch(ctx, p, raw); err == nil {
		t.Fatal("1001 accepted")
	}
	descriptor, ok := s.capabilities.Get("servers.dns_test_batch")
	if !ok {
		t.Fatal("missing capability")
	}
	var schema struct {
		Properties map[string]struct {
			MaxItems int `json:"maxItems"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(descriptor.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["server_ids"].MaxItems != 1000 {
		t.Fatal("catalog bound mismatch")
	}
}
