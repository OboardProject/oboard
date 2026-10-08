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
	if err := db.SaveRuntimeSecurityDesired(ctx, server.ID, model.RuntimeSecurityRequest{Mode: "enhanced", Revision: 42}); err != nil {
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
	if err := db.SaveRuntimeSecurityDesired(ctx, server.ID, desired); err != nil {
		t.Fatal(err)
	}
	task, err := srv.queueRuntimeSecurity(ctx, *server, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(ctx, task.ID, "failed", "{}"); err != nil {
		t.Fatal(err)
	}
	report := model.RuntimeSecurityReport{DesiredMode: "standard", ActualMode: "standard", State: "standard", CheckedAt: time.Now().UTC()}
	for range 3 {
		srv.recordRuntimeSecurity(ctx, server, &report)
		srv.reconcileRuntimeSecurityDesired(ctx, server)
	}
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("retry storm: %d %v", len(tasks), err)
	}
	desired.Revision++
	if err := db.SaveRuntimeSecurityDesired(ctx, server.ID, desired); err != nil {
		t.Fatal(err)
	}
	srv.recordRuntimeSecurity(ctx, server, &report)
	tasks, err = db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("explicit new revision not retried: %d %v", len(tasks), err)
	}
}
