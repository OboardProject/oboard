package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestSubscriptionPlanNodesCapabilityAppliesThroughChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "9929", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "9929 Reality", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 15787, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "管理员", Enabled: true, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	if err := db.CreateAPIPrincipal(ctx, &model.APIPrincipal{
		ID: principal.ID, OwnerUserID: &admin.ID, Name: principal.Name, Type: principal.Type,
		Enabled: true, Scopes: principal.Scopes, ResourceFilter: json.RawMessage(`{}`),
		RateLimitPerMinute: 60, MaxConcurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"plan_id": plan.ID, "op": "add", "nodes": []map[string]any{{"node_type": "inbound", "node_id": inbound.ID}},
		"change_summary": "加入 9929 Reality",
	})
	draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "subscription_plans.nodes.update", Input: input}}})
	if err != nil {
		t.Fatalf("validate subscription plan node draft: %v", err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{
		IdempotencyKey: "add-9929-to-admin-plan", BaseRevisions: base,
		Operations: []automation.OperationRequest{{Capability: "subscription_plans.nodes.update", Input: input}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved in test"); err != nil {
		t.Fatal(err)
	}
	applied, err := server.automation.Apply(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != model.ChangesetSucceeded {
		t.Fatalf("changeset status = %s", applied.Status)
	}
	updated, err := db.GetSubscriptionPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PendingRevisionID == 0 || updated.LatestRevisionID != updated.PendingRevisionID || updated.CurrentRevisionID == updated.LatestRevisionID {
		t.Fatalf("plan version pointers were not staged through access change: %#v", updated)
	}
	nodes, err := db.ListPlanRevisionNodes(ctx, updated.LatestRevisionID)
	if err != nil || len(nodes) != 1 || nodes[0].NodeType != model.AssignableNodeInbound || nodes[0].NodeID != inbound.ID {
		t.Fatalf("latest plan nodes = %#v, err=%v", nodes, err)
	}
	queued, err := server.automation.StartWorkflow(ctx, principal, automation.StartWorkflowRequest{
		Kind: "access_change", IdempotencyKey: "queued-plan-version", ChangesetID: changeset.ID,
	})
	if err != nil || queued.Status != model.WorkflowWaitingForAgent {
		t.Fatalf("queued revision completed prematurely: %#v, %v", queued, err)
	}
	if changes, err := db.ListAccessChanges(ctx, 10); err != nil || len(changes) != 0 {
		t.Fatalf("save synchronously materialized access changes: %#v, %v", changes, err)
	}
	server.reconcilePlans(ctx)
	changes, err := db.ListAccessChanges(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].SourcePlanID != plan.ID || changes[0].CandidateRevisionID != updated.LatestRevisionID {
		t.Fatalf("access changes = %#v, err=%v", changes, err)
	}
	if changes[0].CreatedBy == nil || *changes[0].CreatedBy != admin.ID {
		t.Fatalf("access change actor was not preserved: %#v", changes[0].CreatedBy)
	}
	workflow, err := server.automation.GetWorkflow(ctx, principal, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Status != model.WorkflowWaitingForAgent || workflow.CurrentStep != "access_change" {
		t.Fatalf("workflow completed before access change: %#v", workflow)
	}
	if err := db.UpdateAccessChangeStatus(ctx, changes[0].ID, []model.AccessChangeStatus{model.AccessChangePreparing}, model.AccessChangeFinalized, ""); err != nil {
		t.Fatal(err)
	}
	workflow, err = server.automation.GetWorkflow(ctx, principal, workflow.ID)
	if err != nil || workflow.Status != model.WorkflowSucceeded {
		t.Fatalf("workflow did not follow finalized access change: %#v, err=%v", workflow, err)
	}
	detail, err := server.application.GetSubscriptionPlan(ctx, principal, plan.ID)
	if err != nil || len(detail.Nodes) != 1 || len(detail.CurrentNodes) != 0 {
		t.Fatalf("MCP plan detail = %#v, err=%v", detail, err)
	}
}

func TestSubscriptionPlanNodesFastPathResolvesPlanAndInboundNames(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "9929", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "9929 Reality", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 15787, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "管理员", Enabled: true, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	prepared, err := server.prepareSubscriptionPlanNodesRecipe(ctx, principal, mcpTaskInput{Goal: "把 9929 Reality 入口加入管理员套餐", Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "ready" || len(prepared.Operations) != 1 {
		t.Fatalf("unexpected prepared recipe: %#v", prepared)
	}
	operation := prepared.Operations[0]
	if operation.Capability != "subscription_plans.nodes.update" || operation.Input["plan_id"] != plan.ID || operation.Input["op"] != "add" {
		t.Fatalf("unexpected operation: %#v", operation)
	}
	encoded, _ := json.Marshal(operation.Input["nodes"])
	var nodes []planNodeRequest
	if err := json.Unmarshal(encoded, &nodes); err != nil || len(nodes) != 1 || nodes[0].NodeID != inbound.ID || nodes[0].NodeType != model.AssignableNodeInbound {
		t.Fatalf("resolved nodes = %#v, err=%v", nodes, err)
	}
	prepared, err = server.prepareSubscriptionPlanNodesRecipe(ctx, principal, mcpTaskInput{
		Goal: "添加入口", Params: map[string]any{"plan_id": plan.ID}, TargetRefs: []string{"inbound:" + itoa(inbound.ID)},
	})
	if err != nil || prepared.Status != "ready" || prepared.Operations[0].Input["plan_id"] != plan.ID {
		t.Fatalf("explicit plan_id was not resolved: %#v, err=%v", prepared, err)
	}
	matched, _, ok := server.matchMCPRecipe(mcpTaskInput{
		Goal: "继续分配套餐节点", Params: map[string]any{"plan": "管理员"},
		TargetRefs: []string{"inbound:" + itoa(inbound.ID)},
	})
	if !ok || matched.ID != "subscription_plan.nodes.manage" {
		t.Fatalf("continuation routed to %#v, ok=%v", matched, ok)
	}
}

func TestSubscriptionPlanDeleteRecipeAndCapability(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	viewer := &model.User{Username: "alice", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "标准套餐", Enabled: true, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: viewer.ID, PlanID: plan.ID}}); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	if err := db.CreateAPIPrincipal(ctx, &model.APIPrincipal{
		ID: principal.ID, OwnerUserID: &admin.ID, Name: principal.Name, Type: principal.Type,
		Enabled: true, Scopes: principal.Scopes, ResourceFilter: json.RawMessage(`{}`),
		RateLimitPerMinute: 60, MaxConcurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	matched, _, ok := server.matchMCPRecipe(mcpTaskInput{Goal: "删除套餐 标准套餐"})
	if !ok || matched.ID != "subscription_plan.delete" {
		t.Fatalf("delete goal routed to %#v, ok=%v", matched, ok)
	}
	prepared, err := server.prepareSubscriptionPlanDeleteRecipe(ctx, principal, mcpTaskInput{Goal: "删除套餐 标准套餐"})
	if err != nil || prepared.Status != "ready" || len(prepared.Operations) != 1 {
		t.Fatalf("unexpected prepared recipe: %#v, err=%v", prepared, err)
	}
	if prepared.Operations[0].Capability != "subscription_plans.delete" || prepared.Operations[0].Input["confirm"] != true {
		t.Fatalf("unexpected operation: %#v", prepared.Operations[0])
	}
	nodesGoal, _, nodesOK := server.matchMCPRecipe(mcpTaskInput{Goal: "删除套餐节点"})
	if !nodesOK || nodesGoal.ID != "subscription_plan.nodes.manage" {
		t.Fatalf("deleting plan nodes routed to %#v, ok=%v", nodesGoal, nodesOK)
	}
	input, _ := json.Marshal(prepared.Operations[0].Input)
	applyAutomationChangeset(t, server, principal, "delete-plan", automation.OperationRequest{Capability: "subscription_plans.delete", Input: input})
	changes, err := db.ListAccessChanges(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].ChangeType != model.AccessChangePlanDelete || changes[0].SourcePlanID != plan.ID {
		t.Fatalf("expected plan_delete access change, got %#v err=%v", changes, err)
	}
	var finalized *model.AccessChange
	for i := 0; i < 10; i++ {
		server.reconcileAccessChanges(ctx)
		item, getErr := db.GetAccessChange(ctx, changes[0].ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if item.Status == model.AccessChangeFinalized {
			finalized = item
			break
		}
	}
	if finalized == nil {
		t.Fatal("plan delete access change did not finalize")
	}
	if _, err := db.GetSubscriptionPlan(ctx, plan.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("plan should be deleted after finalize, err=%v", err)
	}
	if _, err := db.GetUser(ctx, viewer.ID); err != nil {
		t.Fatalf("bound user must remain: %v", err)
	}
	if _, err := db.GetActiveUserPlanBinding(ctx, viewer.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("user plan binding should have been removed: %v", err)
	}
}
