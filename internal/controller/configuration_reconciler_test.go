package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestConfigurationMutationClassification(t *testing.T) {
	paths := []struct {
		method string
		path   string
		want   bool
	}{
		{method: "POST", path: "/api/v2/ui/servers", want: true},
		{method: "PATCH", path: "/api/v2/ui/servers/4", want: true},
		{method: "POST", path: "/api/v2/ui/servers/4/dns-policy", want: true},
		{method: "POST", path: "/api/v2/ui/servers/4/dns-test", want: false},
		{method: "POST", path: "/api/v2/ui/servers/4/agent-update", want: false},
		{method: "POST", path: "/api/v2/ui/inbounds", want: true},
		{method: "POST", path: "/api/v2/ui/inbounds/8/probe", want: false},
		{method: "POST", path: "/api/v2/ui/proxy-paths/reuse", want: true},
		{method: "POST", path: "/api/v2/ui/proxy-paths/reuse-preview", want: false},
		{method: "POST", path: "/api/v2/ui/proxy-paths/8/probe-egress", want: false},
		{method: "POST", path: "/api/v2/ui/external-outbounds/import", want: true},
		{method: "POST", path: "/api/v2/ui/routing-rules/place", want: true},
		{method: "PUT", path: "/api/v2/ui/dns-lists/3", want: true},
		{method: "POST", path: "/api/v2/ui/dns-lists/3/set-default", want: true},
		{method: "POST", path: "/api/v2/ui/routing-rule-sets/3/refresh", want: false},
		{method: "POST", path: "/api/v2/ui/port-forwards/8/probe", want: false},
		{method: "PATCH", path: "/api/v2/ui/tunnels/8", want: true},
		{method: "POST", path: "/api/v2/ui/subscription-plans/8/changes/apply", want: false},
		{method: "POST", path: "/api/v2/ui/subscription-plans", want: true},
		{method: "POST", path: "/api/v2/ui/user-node-exceptions", want: true},
		{method: "POST", path: "/api/v2/changesets/cs_1/apply", want: false},
		{method: "GET", path: "/api/v2/ui/inbounds", want: false},
	}
	for _, item := range paths {
		if got := configurationMutationPath(item.path, item.method); got != item.want {
			t.Errorf("%s %s configuration mutation = %t, want %t", item.method, item.path, got, item.want)
		}
	}

	capabilities := []struct {
		name string
		want bool
	}{
		{name: "servers.update", want: true},
		{name: "servers.update_agent", want: false},
		{name: "servers.dns_policy.set", want: true},
		{name: "servers.dns_test", want: false},
		{name: "inbounds.create", want: true},
		{name: "inbounds.probe", want: false},
		{name: "proxy_paths.update", want: true},
		{name: "proxy_paths.probe_egress", want: false},
		{name: "port_forwards.create", want: true},
		{name: "deployments.apply", want: false},
		{name: "certificates.issue", want: false},
		{name: "subscription_plans.update", want: true},
		{name: "user_node_exceptions.update", want: true},
	}
	for _, item := range capabilities {
		if got := configurationCapability(item.name); got != item.want {
			t.Errorf("capability %s configuration mutation = %t, want %t", item.name, got, item.want)
		}
	}
}

func TestConfigurationMutationAffectedServerScope(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	first := &model.Server{Name: "scope-a", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	second := &model.Server{Name: "scope-b", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20001, PortRangeEnd: 30000}
	for _, server := range []*model.Server{first, second} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	if got := srv.configurationMutationServerIDs(ctx, "/api/v2/ui/servers/"+itoa(first.ID), "PATCH"); len(got) != 1 || got[0] != first.ID {
		t.Fatalf("server patch scope = %v", got)
	}
	if got := srv.configurationMutationServerIDs(ctx, "/api/v2/ui/servers/"+itoa(first.ID), "DELETE"); got != nil {
		t.Fatalf("server delete scope = %v, want all", got)
	}
	forward := &model.PortForward{Name: "scope-forward", SourceServerID: first.ID, TargetServerID: second.ID, ListenPort: 12000, TargetPort: 443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendAuto, ProbeMode: "never", ProbeIntervalSeconds: 300, ConfigJSON: "{}", Enabled: true}
	if err := db.CreatePortForward(ctx, forward); err != nil {
		t.Fatal(err)
	}
	got := srv.configurationMutationServerIDs(ctx, "/api/v2/ui/port-forwards/"+itoa(forward.ID), "DELETE")
	if len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("forward delete scope = %v", got)
	}
}

func TestConfigurationReconcilerRepairsMissingStateFromDurableRevision(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "recovery-watermark", AgentID: "recovery-agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "recovery-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil || revision == 0 {
		t.Fatalf("configuration watermark = %d, err=%v", revision, err)
	}
	// The desired tables committed, but no asynchronous sync-state row exists,
	// which is the state left by a crash in the post-commit handoff window.
	go srv.StartConfigurationReconciler(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, stateErr := db.ConfigurationSyncState(ctx, server.ID)
		if stateErr == nil && state.WantedRevision == revision {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, stateErr := db.ConfigurationSyncState(ctx, server.ID)
	t.Fatalf("startup repair did not restore missing desired state: %#v err=%v", state, stateErr)
}

func TestConfigurationWriteRespondsBeforeAsyncDeployment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := newTestServer(db, "test-secret", "")
	srv.configurationDelay = 20 * time.Millisecond
	handler := srv.Handler()
	request(t, handler, "POST", "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	login := request(t, handler, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
	token := login["token"].(string)
	created := request(t, handler, "POST", "/api/v2/ui/servers", token, map[string]any{"name": "async-save", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 11000}, 201)
	server := created["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	if created["desired_revision"] == nil {
		t.Fatalf("save response missing desired_revision: %#v", created)
	}
	syncRows, ok := created["configuration_sync"].([]any)
	if !ok || len(syncRows) != 1 || syncRows[0].(map[string]any)["state"] != "pending" {
		t.Fatalf("save response sync metadata = %#v", created["configuration_sync"])
	}
	state, err := db.ConfigurationSyncState(ctx, serverID)
	if err != nil || state.State != "pending" || state.LastTaskID != 0 {
		t.Fatalf("save response did not leave pending desired state = %#v err=%v", state, err)
	}
	if tasks, err := db.ListTasksByServer(ctx, serverID, 10); err != nil || len(tasks) != 0 {
		t.Fatalf("save response waited for or synchronously queued tasks = %#v err=%v", tasks, err)
	}
	go srv.StartConfigurationReconciler(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err = db.ConfigurationSyncState(ctx, serverID)
		if err == nil && state.State == "queued" && state.LastTaskID > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background reconciler did not queue saved state within 2s: %#v err=%v", state, err)
}

func TestInvalidConfigurationWriteDoesNotCreateDesiredState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	handler := srv.Handler()
	request(t, handler, "POST", "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	login := request(t, handler, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
	token := login["token"].(string)
	request(t, handler, "POST", "/api/v2/ui/servers", token, map[string]any{"name": "invalid", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 10000}, 400)
	states, err := db.ListAllConfigurationSyncStates(context.Background())
	if err != nil || len(states) != 0 {
		t.Fatalf("invalid save created desired state = %#v err=%v", states, err)
	}
}

func TestConfigurationSyncRetryOnlyReopensFailures(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	handler := srv.Handler()
	request(t, handler, "POST", "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	login := request(t, handler, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
	token := login["token"].(string)
	server := &model.Server{Name: "retry-node", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, 90, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 90); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncPreparationFailure(ctx, server.ID, 90, "invalid desired state"); err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, "POST", "/api/v2/ui/configuration-sync/retry", token, map[string]any{"server_ids": []int64{server.ID}}, 202)
	if response["retried"] != float64(1) {
		t.Fatalf("retry response = %#v", response)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" || state.RetryCount != 0 {
		t.Fatalf("retry state = %#v err=%v", state, err)
	}
	request(t, handler, "POST", "/api/v2/ui/configuration-sync/retry", token, map[string]any{"server_ids": []int64{server.ID}}, 409)
}

func TestChangesetConfigurationObserverExcludesCommandOperations(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "observer-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, user.ID)
	server := &model.Server{Name: "observer-node", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	update, _ := json.Marshal(map[string]any{"server_id": server.ID, "changes": map[string]any{"ip_stack": "ipv4_only"}})
	applyAutomationChangeset(t, srv, principal, "observer-config", automation.OperationRequest{Capability: "servers.update", Input: update})
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" || state.WantedRevision == 0 {
		t.Fatalf("configuration Changeset did not mark pending = %#v err=%v", state, err)
	}
	if _, err := db.RetryFailedConfigurationSync(ctx, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	// A command operation may write tasks/audit state, but it must not advance
	// the desired configuration revision or create a second sync revision.
	before := state.WantedRevision
	diagnose, _ := json.Marshal(map[string]any{"server_id": server.ID})
	applyAutomationChangeset(t, srv, principal, "observer-command", automation.OperationRequest{Capability: "servers.diagnose", Input: diagnose})
	after, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || after.WantedRevision != before {
		t.Fatalf("command Changeset changed desired revision: before=%d after=%#v err=%v", before, after, err)
	}
}

func TestConfigurationSyncRetryCapabilityUsesChangesetBoundary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	user := &model.User{Username: "sync-operator", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, user.ID)
	server := &model.Server{Name: "retry-capability", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, 91, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 91); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncPreparationFailure(ctx, server.ID, 91, "failed preparation"); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"server_ids": []int64{server.ID}})
	applyAutomationChangeset(t, srv, principal, "configuration-sync-retry", automation.OperationRequest{Capability: "configuration_sync.retry", Input: input})
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" {
		t.Fatalf("retry capability state = %#v err=%v", state, err)
	}
}

func TestConfigurationReconcilerQueuesLatestRevisionOnly(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "desired-node", AgentID: "desired-agent", AgentTokenHash: security.HashSecret("desired-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}

	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", "POST")
	srv.reconcileConfiguration(ctx)
	first, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	firstState, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || firstState.State != "queued" || firstState.LastTaskID != first.ID {
		t.Fatalf("first sync state = %#v, err=%v", firstState, err)
	}

	inbound.Port = 10444
	if err := db.UpdateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", "PATCH")
	srv.reconcileConfiguration(ctx)
	stale, err := db.GetTask(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != "failed" || stale.ResultJSON == "{}" {
		t.Fatalf("stale task was not suppressed: %#v", stale)
	}
	latest, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID == first.ID || latest.Status != "pending" || latest.ConfigVersion <= first.ConfigVersion {
		t.Fatalf("latest task = %#v, first = %#v", latest, first)
	}
	latestState, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || latestState.State != "queued" || latestState.LastTaskID != latest.ID || latestState.WantedRevision <= firstState.WantedRevision {
		t.Fatalf("latest sync state = %#v, first = %#v, err=%v", latestState, firstState, err)
	}
}

func TestAgentHelloCarriesConfigurationSyncHint(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx := context.Background()
	if _, err := db.MarkConfigurationSyncPending(ctx, 77, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	hello := socket.readMessage(2 * time.Second)
	if hello["type"] != "hello" || hello["desired_config_revision"] != float64(77) || hello["configuration_sync_state"] != "pending" {
		t.Fatalf("hello sync hint = %#v", hello)
	}
}

func TestConfigurationSaveDispatchesOnlineAgentWithinSLO(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.configurationDelay = 25 * time.Millisecond
	go srv.StartConfigurationReconciler(ctx)
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	inbound := &model.Inbound{ServerID: server.ID, Name: "instant-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	started := time.Now()
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", "POST")
	task := socket.expectTaskRequest(2 * time.Second)
	if task["type"] != model.AgentTaskTypeApplyDeployment {
		t.Fatalf("task type = %#v", task["type"])
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("configuration dispatch took %s, want <2s", elapsed)
	}
}

func TestFailedSyncHealthReportReopensReconciliation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "failed-reconnect", AgentID: "failed-agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, 101, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 101); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncPreparationFailure(ctx, server.ID, 101, "offline timeout"); err != nil {
		t.Fatal(err)
	}
	srv.reconcileAgentAppliedState(ctx, server.ID, model.HealthReport{})
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" {
		t.Fatalf("failed reconnect state = %#v err=%v", state, err)
	}
}

func TestAgentHealthReportDetectsAppliedVersionDrift(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "drift-node", AgentID: "drift-agent", AgentTokenHash: security.HashSecret("drift-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, 31, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 31); err != nil || !ok {
		t.Fatalf("claim = %v, err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 31, 301, 11, "expected-digest"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 301, true, ""); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(model.HealthReport{Status: model.ServerOnline, AppliedConfigVersion: 301, AppliedConfigDigest: "wrong-digest", Timestamp: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	srv.processAgentSocketMessage(ctx, stored, map[string]json.RawMessage{"health_report": raw}, "192.0.2.10")
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" || state.WantedRevision == 0 {
		t.Fatalf("drift did not create pending reconciliation = %#v, err=%v", state, err)
	}
}
