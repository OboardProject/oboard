package controller

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

// TestMCPAccessChangeRetry verifies the MCP retry path for a failed 套餐发布
// (access_change) workflow: the failed step is retryable, carries the failure
// text, the Controller resumes the access change from its durable failure
// point, and the workflow synchronizes back to waiting_for_agent and then
// succeeds when the release finishes.
func TestMCPAccessChangeRetry(t *testing.T) {
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
		t.Fatal(err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{
		IdempotencyKey: "retry-access-change-setup", BaseRevisions: base,
		Operations: []automation.OperationRequest{{Capability: "subscription_plans.nodes.update", Input: input}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	approved, err := server.automation.Approve(ctx, principal, changeset.ID, "approved in test")
	if err != nil {
		t.Fatal(err)
	}
	var applied *model.AutomationChangeset
	if approved.Status == model.ChangesetSucceeded {
		applied = approved
	} else {
		applied, err = server.automation.Apply(ctx, principal, changeset.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if applied.Status != model.ChangesetSucceeded {
		t.Fatalf("changeset status = %s", applied.Status)
	}
	workflow, err := server.automation.StartWorkflow(ctx, principal, automation.StartWorkflowRequest{
		Kind: "access_change", IdempotencyKey: "track-retry-access-change", ChangesetID: changeset.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.reconcilePlans(ctx)
	changes, err := db.ListAccessChanges(ctx, 10)
	if err != nil || len(changes) != 1 {
		t.Fatalf("access changes = %#v, err=%v", changes, err)
	}
	change := changes[0]

	// Simulate the durable failure the user reported: the release dies while
	// activating with a transient SQLite busy error.
	if err := db.UpdateAccessChangeStatus(ctx, change.ID, []model.AccessChangeStatus{model.AccessChangePreparing}, model.AccessChangeFailed, "activate: database is locked (5) (SQLITE_BUSY)"); err != nil {
		t.Fatal(err)
	}

	// The workflow must synchronize to failed with a retryable step and the
	// failure text, and the access change id must be resolvable for MCP.
	workflow, err = server.automation.GetWorkflow(ctx, principal, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Status != model.WorkflowFailed {
		t.Fatalf("workflow status = %s, want failed", workflow.Status)
	}
	if !workflow.Steps[0].Retryable {
		t.Fatalf("access change step is not retryable: %#v", workflow.Steps[0])
	}
	if workflow.Steps[0].ErrorCode != "access_change_failed" {
		t.Fatalf("step error code = %q", workflow.Steps[0].ErrorCode)
	}
	if !strings.Contains(workflow.ErrorMessage, "database is locked") {
		t.Fatalf("workflow error message does not carry the failure reason: %q", workflow.ErrorMessage)
	}
	if got := mcpWorkflowAccessChangeID(workflow); got != change.ID {
		t.Fatalf("mcpWorkflowAccessChangeID = %d, want %d", got, change.ID)
	}

	// MCP retry: resume the release from its durable failure point.
	phase, queued, err := server.retryAccessChange(ctx, change.ID)
	if err != nil {
		t.Fatalf("retryAccessChange: %v", err)
	}
	if phase != "prepare" {
		t.Fatalf("retry phase = %q, want prepare (activation had not completed)", phase)
	}
	_ = queued
	refreshed, err := server.automation.GetWorkflow(ctx, principal, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != model.WorkflowFailed {
		t.Fatalf("workflow before step reset = %s", refreshed.Status)
	}
	reset, err := server.automation.RetryWorkflowStep(ctx, principal, workflow.ID, workflow.Steps[0].ID)
	if err != nil {
		t.Fatalf("RetryWorkflowStep: %v", err)
	}
	if reset.Status != model.WorkflowQueued {
		t.Fatalf("workflow after retry = %s, want queued", reset.Status)
	}
	resumed, err := server.automation.GetWorkflow(ctx, principal, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != model.WorkflowWaitingForAgent {
		t.Fatalf("workflow after resume = %s, want waiting_for_agent", resumed.Status)
	}

	// Complete the release: activation happens through the worker path, then
	// the finalize phase finishes and the workflow succeeds.
	changeAfterRetry, err := db.GetAccessChange(ctx, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changeAfterRetry.Status != model.AccessChangePreparing {
		t.Fatalf("change status after retry = %s, want preparing", changeAfterRetry.Status)
	}
	changeID := changeAfterRetry.ID
	if err := db.UpdateAccessChangeStatus(ctx, changeID, []model.AccessChangeStatus{model.AccessChangePreparing, model.AccessChangeActivating}, model.AccessChangeFinalizing, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAccessChangeStatus(ctx, changeID, []model.AccessChangeStatus{model.AccessChangeFinalizing}, model.AccessChangeFinalized, ""); err != nil {
		t.Fatal(err)
	}
	done, err := server.automation.GetWorkflow(ctx, principal, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != model.WorkflowSucceeded {
		t.Fatalf("workflow after completed release = %s, want succeeded", done.Status)
	}
}
