package controller

import (
	"context"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"testing"
)

func TestProxyDSCPGlobalOverrideAndOfflineChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin-dscp", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111112", ProxyPassword: "unused"}
	if e := db.CreateUser(ctx, admin); e != nil {
		t.Fatal(e)
	}
	p := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "dscp", AgentID: "dscp-agent", Status: model.ServerOffline, KernelCapabilities: []string{model.ProxyDSCPCapability, model.ProxyDSCPKernelCapability}}
	if e := db.CreateServer(ctx, node); e != nil {
		t.Fatal(e)
	}
	enabled := true
	raw, _ := json.Marshal(proxyDSCPOperation{ServerID: node.ID, Enabled: &enabled})
	applyAutomationChangeset(t, s, p, "dscp-single", automation.OperationRequest{Capability: "servers.proxy_dscp.update", Input: raw})
	desired, e := db.ProxyDSCPDesired(ctx, node.ID)
	if e != nil || !desired.Enabled || desired.Revision == 0 {
		t.Fatal(desired, e)
	}
	tasks, e := db.ListTasksByServer(ctx, node.ID, 20)
	if e != nil || len(tasks) != 0 {
		t.Fatal("offline task created", e)
	}
	globalRaw := json.RawMessage(`{"changes":{"proxy_dscp_enabled":true}}`)
	applyAutomationChangeset(t, s, p, "dscp-global", automation.OperationRequest{Capability: "settings.update", Input: globalRaw})
	enabled = false
	raw, _ = json.Marshal(proxyDSCPOperation{ServerID: node.ID, Enabled: &enabled})
	applyAutomationChangeset(t, s, p, "dscp-retain", automation.OperationRequest{Capability: "servers.proxy_dscp.update", Input: raw})
	view, e := s.readProxyDSCP(ctx, p, node.ID)
	if e != nil || view["enabled"] != false || view["effective_enabled"] != true {
		t.Fatal(view, e)
	}
	if e = db.SetSetting(ctx, store.ProxyDSCPGlobalSetting, "false"); e != nil {
		t.Fatal(e)
	}
	next, e := db.ProxyDSCPDesired(ctx, node.ID)
	if e != nil || next.Enabled || next.Revision <= desired.Revision {
		t.Fatal(next, e)
	}
	restricted := application.Principal{ResourceFilter: json.RawMessage(`{"servers":{"mode":"selected","ids":[]}}`)}
	if _, e = s.readProxyDSCP(ctx, restricted, node.ID); e == nil {
		t.Fatal("scope bypass")
	}
}
func TestProxyDSCPConvergenceAcknowledgementAndRetry(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	node := &model.Server{Name: "dscp-converge", AgentID: "dscp-agent", Status: model.ServerOnline, KernelCapabilities: []string{model.ProxyDSCPCapability, model.ProxyDSCPKernelCapability}}
	if e := db.CreateServer(ctx, node); e != nil {
		t.Fatal(e)
	}
	if e := db.SetSetting(ctx, store.ProxyDSCPGlobalSetting, "true"); e != nil {
		t.Fatal(e)
	}
	desired, _ := db.ProxyDSCPDesired(ctx, node.ID)
	s.reconcileProxyDSCP(ctx, node, nil)
	s.reconcileProxyDSCP(ctx, node, nil)
	tasks, e := db.ListTasksByServer(ctx, node.ID, 20)
	if e != nil || len(tasks) != 1 || tasks[0].Type != model.AgentTaskTypeProxyDSCP {
		t.Fatal(tasks, e)
	}
	if tasks[0].ConfigVersion != 0 {
		t.Fatal("DSCP changed deployment version")
	}
	report := &model.ProxyDSCPReport{ProxyDSCPPolicy: desired, State: "partial", Value: 46, Excluded: []string{"hysteria2"}}
	s.reconcileProxyDSCP(ctx, node, report)
	saved, e := db.ProxyDSCPReport(ctx, node.ID)
	if e != nil || saved == nil || saved.Revision != desired.Revision {
		t.Fatal(saved, e)
	}
	report.Revision--
	if e := db.SaveProxyDSCPReport(ctx, *node, *report); e != nil {
		t.Fatal(e)
	}
	saved, _ = db.ProxyDSCPReport(ctx, node.ID)
	if saved.Revision != desired.Revision {
		t.Fatal("stale report replaced current")
	}
	old := *node
	old.AgentID = "retired-agent"
	enabled := false
	if _, e = db.SetServerProxyDSCP(ctx, old, &enabled, false); e == nil {
		t.Fatal("stale server mutation accepted")
	}
}
func TestProxyDSCPNewServerInheritsGlobalAndOldKernelIsNotClaimed(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	if e := db.SetSetting(ctx, store.ProxyDSCPGlobalSetting, "true"); e != nil {
		t.Fatal(e)
	}
	node := &model.Server{Name: "dscp-new", AgentID: "agent-old", Status: model.ServerOnline}
	if e := db.CreateServer(ctx, node); e != nil {
		t.Fatal(e)
	}
	s.reconcileProxyDSCP(ctx, node, nil)
	desired, _ := db.ProxyDSCPDesired(ctx, node.ID)
	if !desired.Enabled || desired.Revision == 0 {
		t.Fatal(desired)
	}
	tasks, _ := db.ListTasksByServer(ctx, node.ID, 10)
	if len(tasks) != 0 {
		t.Fatal("unsupported agent received task")
	}
	view, e := s.readProxyDSCP(ctx, application.Principal{}, node.ID)
	if e != nil || view["state"] != "unsupported" {
		t.Fatal(view, e)
	}
}
