package controller

import (
	"context"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"testing"
	"time"
)

func TestRuntimeSecurityOfflineChangesetAndScope(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111112", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	server := &model.Server{Name: "security-offline", Status: model.ServerOffline, AgentID: "security-agent", KernelCapabilities: []string{model.RuntimeSecurityCapability}}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(runtimeSecurityOperation{ServerID: server.ID, Mode: "enhanced"})
	applyAutomationChangeset(t, srv, principal, "runtime-security-offline", automation.OperationRequest{Capability: "servers.runtime_security.update", Input: raw})
	desired, err := db.RuntimeSecurityDesired(ctx, server.ID)
	if err != nil || desired.Mode != "enhanced" || desired.Revision == 0 {
		t.Fatal(desired, err)
	}
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 0 {
		t.Fatal("offline change queued a task", err)
	}
	restricted := application.Principal{ResourceFilter: json.RawMessage("{\"servers\":{\"mode\":\"selected\",\"ids\":[]}}")}
	if _, err := srv.readRuntimeSecurity(ctx, restricted, server.ID); err == nil {
		t.Fatal("server scope bypass")
	}
}
func TestRuntimeSecurityReportFreshnessAndReconnect(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	server := &model.Server{Name: "security-reconnect", AgentID: "security-agent", Status: model.ServerOffline, KernelCapabilities: []string{model.RuntimeSecurityCapability}}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	report := model.RuntimeSecurityReport{DesiredMode: "standard", ActualMode: "standard", State: "standard", CheckedAt: time.Now().UTC()}
	srv.recordRuntimeSecurity(ctx, server, &report)
	if err := db.SaveRuntimeSecurityDesired(ctx, *server, model.RuntimeSecurityRequest{Mode: "enhanced", Revision: 42}); err != nil {
		t.Fatal(err)
	}
	server.Status = model.ServerOnline
	srv.recordRuntimeSecurity(ctx, server, &report)
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("reconnect tasks=%d error=%v", len(tasks), err)
	}
	srv.recordRuntimeSecurity(ctx, server, &report)
	tasks, err = db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatal("duplicate task", err)
	}
	fresh := report
	fresh.Revision = 42
	fresh.DesiredMode = "enhanced"
	fresh.CheckedAt = fresh.CheckedAt.Add(time.Second)
	fresh.State = "failed"
	srv.recordRuntimeSecurity(ctx, server, &fresh)
	srv.recordRuntimeSecurity(ctx, server, &report)
	saved, err := db.RuntimeSecurityReport(ctx, server.ID)
	if err != nil || saved.Revision != 42 {
		t.Fatal("old report overwrote current result", err)
	}
}
func TestRuntimeSecurityRejectInvalidReport(t *testing.T) {
	report := model.RuntimeSecurityReport{DesiredMode: "standard", ActualMode: "standard", State: "standard", CheckedAt: time.Now().UTC()}
	if !validRuntimeSecurityReport(report) {
		t.Fatal("valid report rejected")
	}
	report.Checks = make([]model.RuntimeSecurityCheck, 65)
	if validRuntimeSecurityReport(report) {
		t.Fatal("unbounded report accepted")
	}
}

func TestRuntimeSecurityFailureDoesNotCreateRetryStorm(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	server := &model.Server{Name: "security-failed", AgentID: "security-failed-agent", Status: model.ServerOnline, KernelCapabilities: []string{model.RuntimeSecurityCapability}}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	desired := model.RuntimeSecurityRequest{Mode: "enhanced", Revision: 42}
	if err := db.SaveRuntimeSecurityDesired(ctx, *server, desired); err != nil {
		t.Fatal(err)
	}
	task, err := srv.queueRuntimeSecurity(ctx, *server, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(ctx, task.ID, "failed", "{}"); err != nil {
		t.Fatal(err)
	}
	check, err := srv.queueRuntimeSecurity(ctx, *server, model.RuntimeSecurityRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(ctx, check.ID, "succeeded", "{}"); err != nil {
		t.Fatal(err)
	}
	report := model.RuntimeSecurityReport{DesiredMode: "standard", ActualMode: "standard", State: "standard", CheckedAt: time.Now().UTC()}
	for range 3 {
		srv.recordRuntimeSecurity(ctx, server, &report)
		srv.reconcileRuntimeSecurityDesired(ctx, server)
	}
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("retry storm: %d %v", len(tasks), err)
	}
	desired.Revision++
	if err := db.SaveRuntimeSecurityDesired(ctx, *server, desired); err != nil {
		t.Fatal(err)
	}
	srv.recordRuntimeSecurity(ctx, server, &report)
	tasks, err = db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 3 {
		t.Fatalf("explicit new revision not retried: %d %v", len(tasks), err)
	}
}

func TestRuntimeSecurityManagementInputIsClosed(t *testing.T) {
	for _, raw := range []string{`{"server_id":1,"path":"/etc/shadow"}`, `{"server_id":1,"command":"id"}`, `{"server_id":1,"mode":"standard"}`} {
		if _, err := decodeRuntimeSecurityOperation(json.RawMessage(raw), false); err == nil {
			t.Fatal("unsafe input accepted")
		}
	}
	if _, err := decodeRuntimeSecurityOperation(json.RawMessage(`{"server_id":1}`), false); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSecurityApprovalRejectsReplacedServer(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	admin := &model.User{Username: "security-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111113", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	old := &model.Server{Name: "security-old", AgentID: "old-agent", Status: model.ServerOffline}
	if err := db.CreateServer(ctx, old); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(runtimeSecurityOperation{ServerID: old.ID, Mode: "enhanced"})
	ops := []automation.OperationRequest{{Capability: "servers.runtime_security.update", Input: raw}}
	draft, err := srv.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	change, err := srv.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "security-replaced", BaseRevisions: base, Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.automation.Validate(ctx, principal, change.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh := &model.Server{Name: "security-new", AgentID: "new-agent", Status: model.ServerOffline}
	if err := db.CreateServer(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.ID != old.ID {
		t.Fatal("server ID was not reused")
	}
	if _, err := srv.automation.Approve(ctx, principal, change.ID, "approved"); err == nil {
		t.Fatal("stale approval changed new server")
	}
	desired, err := db.RuntimeSecurityDesired(ctx, fresh.ID)
	if err != nil || desired.Mode != "standard" || desired.Revision != 0 {
		t.Fatal(desired, err)
	}
	report := model.RuntimeSecurityReport{DesiredMode: "enhanced", ActualMode: "enhanced", State: "enhanced", CheckedAt: time.Now().UTC()}
	srv.recordRuntimeSecurity(ctx, old, &report)
	if value, err := db.RuntimeSecurityReport(ctx, fresh.ID); err != nil || value != nil {
		t.Fatal("old Agent report reached replacement", value, err)
	}
}
