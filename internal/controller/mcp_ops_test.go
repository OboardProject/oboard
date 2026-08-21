package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestOpsTaskTriggerCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "entry", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "agent_1", AgentBuild: agentBuildMinNetworkInterfaces}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	diagnoseInput, _ := json.Marshal(map[string]any{"server_id": node.ID})
	applyAutomationChangeset(t, server, principal, "ops-diagnose", automation.OperationRequest{Capability: "servers.diagnose", Input: diagnoseInput})
	tasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	if tasks[0].Type != model.AgentTaskTypeDiagnoseNetwork {
		t.Fatalf("unexpected task type: %s", tasks[0].Type)
	}
	logsInput, _ := json.Marshal(map[string]any{"server_id": node.ID, "services": "agent", "lines": 200})
	applyAutomationChangeset(t, server, principal, "ops-logs", automation.OperationRequest{Capability: "servers.collect_logs", Input: logsInput})
	logTasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil || len(logTasks) < 2 {
		t.Fatalf("log tasks=%#v err=%v", logTasks, err)
	}
	found := false
	for _, task := range logTasks {
		if task.Type == model.AgentTaskTypeCollectLogs {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("collect_logs task was not queued")
	}
	interfacesInput, _ := json.Marshal(map[string]any{"server_id": node.ID})
	applyAutomationChangeset(t, server, principal, "ops-network-interfaces", automation.OperationRequest{Capability: "servers.list_network_interfaces", Input: interfacesInput})
	interfaceTasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundInterfaces := false
	for _, task := range interfaceTasks {
		if task.Type == model.AgentTaskTypeListNetworkInterfaces {
			foundInterfaces = true
			break
		}
	}
	if !foundInterfaces {
		t.Fatal("list_network_interfaces task was not queued")
	}
	probeInput, _ := json.Marshal(map[string]any{"inbound_id": inbound.ID})
	applyAutomationChangeset(t, server, principal, "ops-probe", automation.OperationRequest{Capability: "inbounds.probe", Input: probeInput})
	probeTasks, err := db.ListTasksByServer(ctx, node.ID, 20)
	if err != nil || len(probeTasks) < 3 {
		t.Fatalf("probe tasks=%#v err=%v", probeTasks, err)
	}
	foundProbe := false
	for _, task := range probeTasks {
		if task.Type == model.AgentTaskTypeProbeInbounds {
			foundProbe = true
			break
		}
	}
	if !foundProbe {
		t.Fatal("probe_inbounds task was not queued")
	}
	payload, err := server.listAgentTasksMCP(ctx, principal, 0, 0)
	if err != nil {
		t.Fatalf("agent_tasks.list resource: %v", err)
	}
	encoded, _ := json.Marshal(payload)
	if contains(encoded, "nonce") && contains(encoded, "token") {
		t.Fatalf("agent task output may leak material: %s", encoded)
	}
}

func TestNetworkInterfacesCapabilityRejectsUnavailableAgents(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111119", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	tests := []model.Server{
		{Name: "old-agent", Status: model.ServerOnline, AgentID: "old", AgentBuild: "20260801000000"},
		{Name: "offline-agent", Status: model.ServerOffline, AgentID: "offline", AgentBuild: agentBuildMinNetworkInterfaces},
	}
	for index := range tests {
		node := &tests[index]
		if err := db.CreateServer(ctx, node); err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(map[string]any{"server_id": node.ID})
		_, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "servers.list_network_interfaces", Input: input}}})
		if err == nil {
			t.Fatalf("server %q unexpectedly accepted list_network_interfaces", node.Name)
		}
	}
}

func TestLatencyProbeCapabilityDeduplicatesActiveTask(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "latency-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "latency-entry", Status: model.ServerOnline, AgentID: "latency-agent", AgentBuild: agentBuildMinLatencyProbe, LatencyProbeEnabled: true, LatencyProbeSampleCount: 3, LatencyProbeMaxTargets: 8}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	resource, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["192.0.2.1"]}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	latencyProbeCache.Lock()
	latencyProbeCache.resource = resource
	latencyProbeCache.fetched = time.Now()
	latencyProbeCache.Unlock()
	t.Cleanup(resetLatencyProbeCacheForTest)
	input, _ := json.Marshal(map[string]any{"server_id": node.ID})
	operation := automation.OperationRequest{Capability: "servers.probe_latency", Input: input}
	applyAutomationChangeset(t, server, principal, "latency-probe-1", operation)
	applyAutomationChangeset(t, server, principal, "latency-probe-2", operation)
	tasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, task := range tasks {
		if task.Type == model.AgentTaskTypeProbeLatencyTargets {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("latency probe tasks = %d, want 1", count)
	}
}

func TestUninstallAgentCapabilityQueuesTask(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111118", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "entry", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "agent_1", AgentBuild: "dev"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"server_id": node.ID})
	applyAutomationChangeset(t, server, principal, "ops-uninstall", automation.OperationRequest{Capability: "servers.uninstall_agent", Input: input})
	tasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range tasks {
		if task.Type == model.AgentTaskTypeUninstallAgent {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("uninstall_agent task was not queued: %#v", tasks)
	}
}

func TestAgentsUpdateAllCapability(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "entry", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "agent_1", AgentBuild: "20260901000000"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{}`)
	draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "agents.update_all", Input: input}}})
	if err != nil {
		t.Fatalf("validate agents.update_all: %v", err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "ops-update-all", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "agents.update_all", Input: input}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Apply(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range tasks {
		if task.Type == model.AgentTaskTypeUpdateAgent {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("update_agent task was not queued for update-all: %#v", tasks)
	}
}
