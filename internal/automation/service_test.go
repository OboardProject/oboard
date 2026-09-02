package automation

import (
	"context"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestInteractiveChangesetAlwaysAwaitsExplicitApproval(t *testing.T) {
	db := openAutomationTestStore(t)
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	item := createAutomationTestChangeset(t, service, principal, "interactive", json.RawMessage(`{}`))
	validated, err := service.Validate(context.Background(), principal, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Status != model.ChangesetAwaitingApproval || validated.ApprovedAt != nil {
		t.Fatalf("interactive Changeset bypassed approval: %#v", validated)
	}
}

func TestMachineAutomaticApprovalRequiresMatchingPolicyFilter(t *testing.T) {
	db := openAutomationTestStore(t)
	principalModel := &model.APIPrincipal{ID: "prn_test", Name: "automation", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principalModel); err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: principalModel.ID, Name: principalModel.Name, Type: principalModel.Type, Scopes: principalModel.Scopes, ResourceFilter: principalModel.ResourceFilter}
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	policy := &model.ApprovalPolicy{ID: "pol_test", PrincipalID: principal.ID, Capability: "servers.onboard", ResourceFilter: json.RawMessage(`{"server_ids":[7]}`), Mode: model.ApprovalAutomatic, ExpiresAt: timePtr(time.Now().Add(time.Hour))}
	if err := db.UpsertApprovalPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	matching := createAutomationTestChangeset(t, service, principal, "matching", json.RawMessage(`{"server_ids":[7]}`))
	validated, err := service.Validate(context.Background(), principal, matching.ID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Status != model.ChangesetApproved {
		t.Fatalf("matching policy status = %s", validated.Status)
	}
	nonmatching := createAutomationTestChangeset(t, service, principal, "nonmatching", json.RawMessage(`{"server_ids":[8]}`))
	validated, err = service.Validate(context.Background(), principal, nonmatching.ID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Status != model.ChangesetAwaitingApproval {
		t.Fatalf("nonmatching policy status = %s", validated.Status)
	}
}

func TestMachineDeniedApprovalPolicyIsEnforced(t *testing.T) {
	db := openAutomationTestStore(t)
	principalModel := &model.APIPrincipal{ID: "prn_denied", Name: "denied automation", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principalModel); err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: principalModel.ID, Name: principalModel.Name, Type: principalModel.Type, Scopes: principalModel.Scopes, ResourceFilter: principalModel.ResourceFilter}
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	if err := db.UpsertApprovalPolicy(context.Background(), &model.ApprovalPolicy{ID: "pol_denied", PrincipalID: principal.ID, Capability: "servers.onboard", ResourceFilter: json.RawMessage(`{}`), Mode: model.ApprovalDenied}); err != nil {
		t.Fatal(err)
	}
	item := createAutomationTestChangeset(t, service, principal, "denied", json.RawMessage(`{}`))
	if _, err := service.Validate(context.Background(), principal, item.ID); err == nil || !strings.Contains(err.Error(), "denies") {
		t.Fatalf("denied policy validation err=%v", err)
	}
	stored, err := service.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.ChangesetDraft {
		t.Fatalf("denied Changeset status=%s", stored.Status)
	}
}

func TestLegacyOAuthDeniedPolicyRequiresApprovalInsteadOfRemovingRoleAccess(t *testing.T) {
	db := openAutomationTestStore(t)
	principalModel := &model.APIPrincipal{ID: "oauth_legacy_denied", Name: "legacy OAuth", Type: model.APIPrincipalOAuth, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage([]byte("{}")), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principalModel); err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: principalModel.ID, Name: principalModel.Name, Type: principalModel.Type, Role: model.RoleAdmin, AccessLevel: mcpauth.AccessOperate, Scopes: principalModel.Scopes, ResourceFilter: principalModel.ResourceFilter}
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	if err := db.UpsertApprovalPolicy(context.Background(), &model.ApprovalPolicy{ID: "pol_oauth_legacy_denied", PrincipalID: principal.ID, Capability: "servers.onboard", ResourceFilter: json.RawMessage([]byte("{}")), Mode: model.ApprovalDenied}); err != nil {
		t.Fatal(err)
	}
	item := createAutomationTestChangeset(t, service, principal, "legacy-oauth-denied", json.RawMessage([]byte("{}")))
	validated, err := service.Validate(context.Background(), principal, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Status != model.ChangesetAwaitingApproval {
		t.Fatalf("legacy OAuth denied policy status=%s, want awaiting approval", validated.Status)
	}
}

func TestMachineDeniedPolicyIsRecheckedWhenAdminApplies(t *testing.T) {
	db := openAutomationTestStore(t)
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	principalModel := &model.APIPrincipal{ID: "prn_later_denied", Name: "later denied", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principalModel); err != nil {
		t.Fatal(err)
	}
	machine := application.Principal{ID: principalModel.ID, Name: principalModel.Name, Type: principalModel.Type, Scopes: principalModel.Scopes, ResourceFilter: principalModel.ResourceFilter}
	operator := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	policy := &model.ApprovalPolicy{ID: "pol_later_denied", PrincipalID: machine.ID, Capability: "servers.onboard", ResourceFilter: json.RawMessage(`{}`), Mode: model.ApprovalAutomatic}
	if err := db.UpsertApprovalPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	item := createAutomationTestChangeset(t, service, machine, "later-denied", json.RawMessage(`{}`))
	validated, err := service.Validate(context.Background(), machine, item.ID)
	if err != nil || validated.Status != model.ChangesetApproved {
		t.Fatalf("automatic validation status=%v err=%v", validated.Status, err)
	}
	policy.Mode = model.ApprovalDenied
	if err := db.UpsertApprovalPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), operator, item.ID); err == nil || !strings.Contains(err.Error(), "denies") {
		t.Fatalf("admin apply after deny err=%v", err)
	}
}

func TestChangesetRejectsUnavailableCapabilityBeforePersistence(t *testing.T) {
	db := openAutomationTestStore(t)
	service := NewService(db, capability.NewCatalog())
	principal := application.Principal{ID: "test", Scopes: []string{"*"}}
	_, err := service.Create(context.Background(), principal, CreateRequest{IdempotencyKey: "unavailable", Operations: []OperationRequest{{Capability: "topology.write", Input: json.RawMessage(`{}`)}}})
	if err == nil {
		t.Fatal("unavailable capability was accepted")
	}
}

func TestApprovedChangesetIsSupersededWhenBaseRevisionChanges(t *testing.T) {
	db := openAutomationTestStore(t)
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	currentRevision := "revision-1"
	applied := false
	service.RegisterRevisionResolver("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{"server:7": currentRevision}, nil
	})
	service.Register("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		applied = true
		return map[string]bool{"applied": true}, nil
	})
	item, err := service.Create(context.Background(), principal, CreateRequest{
		IdempotencyKey: "revision-change", BaseRevisions: json.RawMessage(`{"server:7":"revision-1"}`),
		Operations: []OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := service.Validate(context.Background(), principal, item.ID)
	if err != nil || validated.Status != model.ChangesetAwaitingApproval {
		t.Fatalf("validate status=%v err=%v", validated.Status, err)
	}
	// Approve executes the changeset, so the window in which state can drift is
	// between validation and approval. Apply still re-resolves the base
	// revisions before running anything, and a drifted revision must supersede
	// the plan rather than apply it.
	currentRevision = "revision-2"
	superseded, err := service.Approve(context.Background(), principal, item.ID, "approved test plan")
	if err == nil || superseded == nil || superseded.Status != model.ChangesetSuperseded {
		t.Fatalf("approve status=%v err=%v", superseded, err)
	}
	if applied {
		t.Fatal("mutation handler ran after the validated base revision changed")
	}
	// A superseded changeset is terminal: it must not be resurrected by a
	// later apply, and the handler must still never run.
	if _, err := service.Apply(context.Background(), principal, item.ID); err == nil {
		t.Fatal("apply must refuse a superseded changeset")
	}
	stored, err := service.Get(context.Background(), item.ID)
	if err != nil || stored.Status != model.ChangesetSuperseded {
		t.Fatalf("stored status=%v err=%v", stored, err)
	}
	if applied {
		t.Fatal("mutation handler ran for a superseded changeset")
	}
}

func TestChangesetValidationRequiresCompleteBaseRevisions(t *testing.T) {
	db := openAutomationTestStore(t)
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	service.RegisterRevisionResolver("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{"server:7": "revision-1"}, nil
	})
	principal := application.Principal{ID: "test", Scopes: []string{"*"}}
	item, err := service.Create(context.Background(), principal, CreateRequest{IdempotencyKey: "missing-revision", Operations: []OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(`{}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(context.Background(), principal, item.ID); err == nil {
		t.Fatal("validation accepted a Changeset without required base revisions")
	}
}

func TestChangesetApplyCannotExecuteTwiceConcurrently(t *testing.T) {
	db := openAutomationTestStore(t)
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	service := NewService(db, capability.NewCatalog())
	service.RegisterValidator("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		return map[string]bool{"valid": true}, nil
	})
	started, release := make(chan struct{}), make(chan struct{})
	var executions atomic.Int32
	service.Register("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		executions.Add(1)
		close(started)
		<-release
		return map[string]bool{"applied": true}, nil
	})
	item := createAutomationTestChangeset(t, service, principal, "single-apply", json.RawMessage(`{}`))
	if _, err := service.Validate(context.Background(), principal, item.ID); err != nil {
		t.Fatal(err)
	}
	// Approve is what executes the changeset, so the first execution runs
	// inside it. Hold the handler open there and race a second apply against
	// it: the changeset is already `applying`, so the second caller must be
	// turned away instead of running the mutation again.
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.Approve(context.Background(), principal, item.ID, "approved")
		firstResult <- err
	}()
	<-started
	if _, err := service.Apply(context.Background(), principal, item.ID); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("concurrent apply err=%v", err)
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 1 {
		t.Fatalf("mutation handler executions=%d", executions.Load())
	}
}

func TestWorkflowSynchronizesChangesetAndCancellationState(t *testing.T) {
	db := openAutomationTestStore(t)
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	principalModel := &model.APIPrincipal{ID: "prn_workflow", Name: "workflow automation", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principalModel); err != nil {
		t.Fatal(err)
	}
	machine := application.Principal{ID: principalModel.ID, Name: principalModel.Name, Type: principalModel.Type, Scopes: principalModel.Scopes, ResourceFilter: principalModel.ResourceFilter}
	operator := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)

	changeset := createAutomationTestChangeset(t, service, machine, "workflow-sync", json.RawMessage(`{}`))
	workflow, err := service.StartWorkflow(context.Background(), machine, StartWorkflowRequest{IdempotencyKey: "workflow-sync", ChangesetID: changeset.ID})
	if err != nil || workflow.Status != model.WorkflowPlanning {
		t.Fatalf("initial workflow status=%v err=%v", workflow.Status, err)
	}
	idempotent, err := service.StartWorkflow(context.Background(), machine, StartWorkflowRequest{IdempotencyKey: "workflow-sync", ChangesetID: changeset.ID})
	if err != nil || idempotent.ID != workflow.ID {
		t.Fatalf("workflow idempotency returned %#v err=%v", idempotent, err)
	}
	if _, err := service.Validate(context.Background(), machine, changeset.ID); err != nil {
		t.Fatal(err)
	}
	workflow, err = service.GetWorkflow(context.Background(), machine, workflow.ID)
	if err != nil || workflow.Status != model.WorkflowApprovalRequired || workflow.Steps[0].Status != "approval_required" {
		t.Fatalf("approval workflow status=%v step=%v err=%v", workflow.Status, workflow.Steps[0].Status, err)
	}
	if _, err := service.Approve(context.Background(), operator, changeset.ID, "approved for workflow test"); err != nil {
		t.Fatal(err)
	}
	workflow, err = service.GetWorkflow(context.Background(), machine, workflow.ID)
	if err != nil || workflow.Status != model.WorkflowSucceeded || workflow.Steps[0].Status != "succeeded" || workflow.CompletedAt == nil {
		t.Fatalf("completed workflow=%#v err=%v", workflow, err)
	}

	cancelChangeset := createAutomationTestChangeset(t, service, machine, "workflow-cancel", json.RawMessage(`{}`))
	cancelled, err := service.StartWorkflow(context.Background(), machine, StartWorkflowRequest{IdempotencyKey: "workflow-cancel", ChangesetID: cancelChangeset.ID})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err = service.CancelWorkflow(context.Background(), machine, cancelled.ID)
	if err != nil || cancelled.Status != model.WorkflowCancelled || cancelled.Steps[0].Status != "cancelled" || cancelled.Steps[0].FinishedAt == nil {
		t.Fatalf("cancelled workflow=%#v err=%v", cancelled, err)
	}
	persisted, err := db.GetAutomationWorkflow(context.Background(), cancelled.ID)
	if err != nil || persisted.Steps[0].Status != "cancelled" {
		t.Fatalf("persisted cancelled workflow=%#v err=%v", persisted, err)
	}

	failedChangeset := createAutomationTestChangeset(t, service, machine, "workflow-failed-abandon", json.RawMessage(`{}`))
	abandoned, err := service.StartWorkflow(context.Background(), machine, StartWorkflowRequest{IdempotencyKey: "workflow-failed-abandon", ChangesetID: failedChangeset.ID})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	abandoned.Status, abandoned.CompletedAt = model.WorkflowFailed, &now
	abandoned.Steps[0].Status, abandoned.Steps[0].Retryable, abandoned.Steps[0].FinishedAt = "failed", true, &now
	if err := db.UpdateAutomationWorkflowAndStep(context.Background(), abandoned, &abandoned.Steps[0]); err != nil {
		t.Fatal(err)
	}
	abandoned, err = service.AbandonFailedWorkflow(context.Background(), machine, abandoned.ID)
	if err != nil || abandoned.Status != model.WorkflowCancelled || abandoned.Steps[0].Status != "cancelled" || abandoned.Steps[0].Retryable {
		t.Fatalf("abandoned workflow=%#v err=%v", abandoned, err)
	}
}

func createAutomationTestChangeset(t *testing.T, service *Service, principal application.Principal, key string, refs json.RawMessage) *model.AutomationChangeset {
	t.Helper()
	item, err := service.Create(context.Background(), principal, CreateRequest{IdempotencyKey: key, Operations: []OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(`{}`), ResourceRefs: refs}}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func openAutomationTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "automation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestApproveAppliesChangeset(t *testing.T) {
	db := openAutomationTestStore(t)
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	principalModel := &model.APIPrincipal{ID: "prn_approve_apply", Name: "approve apply", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(context.Background(), principalModel); err != nil {
		t.Fatal(err)
	}
	machine := application.Principal{ID: principalModel.ID, Name: principalModel.Name, Type: principalModel.Type, Scopes: principalModel.Scopes, ResourceFilter: principalModel.ResourceFilter}
	operator := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	service := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(service)
	item := createAutomationTestChangeset(t, service, machine, "approve-apply", json.RawMessage(`{}`))
	if _, err := service.Validate(context.Background(), machine, item.ID); err != nil {
		t.Fatal(err)
	}
	applied, err := service.Approve(context.Background(), operator, item.ID, "approved in test")
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != model.ChangesetSucceeded {
		t.Fatalf("approve status=%s, want succeeded", applied.Status)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func registerAutomationTestCapability(service *Service) {
	service.RegisterValidator("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		return map[string]bool{"valid": true}, nil
	})
	service.Register("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		return map[string]bool{"applied": true}, nil
	})
}
