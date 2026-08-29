package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestConfigurationSyncViewsMarkUnreachableAgents(t *testing.T) {
	states := []store.ConfigurationSyncState{
		{ServerID: 1, WantedRevision: 9, State: "queued"},
		{ServerID: 2, WantedRevision: 9, State: "pending"},
		{ServerID: 3, WantedRevision: 9, State: "running"},
	}
	views := configurationSyncViews(states, []model.Server{
		{ID: 1, Name: "offline", AgentID: "agent-offline", Status: model.ServerOffline},
		{ID: 2, Name: "unenrolled", Status: model.ServerUnknown},
		{ID: 3, Name: "online", AgentID: "agent-online", Status: model.ServerOnline},
	})
	if len(views) != 3 {
		t.Fatalf("views = %#v", views)
	}
	if views[0]["agent_reachable"] != false || views[1]["agent_reachable"] != false || views[2]["agent_reachable"] != true {
		t.Fatalf("agent reachability = %#v", views)
	}
}

func TestConfigurationMutationClassification(t *testing.T) {
	paths := []struct {
		method string
		path   string
		want   bool
	}{
		{method: "POST", path: "/api/v1/ui/servers", want: true},
		{method: "PATCH", path: "/api/v1/ui/servers/4", want: true},
		{method: "POST", path: "/api/v1/ui/servers/4/dns-policy", want: true},
		{method: "POST", path: "/api/v1/ui/servers/4/dns-test", want: false},
		{method: "POST", path: "/api/v1/ui/servers/4/agent-update", want: false},
		{method: "POST", path: "/api/v1/ui/servers/4/agent-uninstall", want: false},
		{method: "POST", path: "/api/v1/ui/servers/4/extend-expiry", want: false},
		{method: "POST", path: "/api/v1/ui/servers/4/reset-traffic", want: false},
		{method: "POST", path: "/api/v1/ui/inbounds", want: true},
		{method: "POST", path: "/api/v1/ui/inbounds/8/probe", want: false},
		{method: "POST", path: "/api/v1/ui/proxy-paths/reuse", want: true},
		{method: "POST", path: "/api/v1/ui/proxy-paths/reuse-preview", want: false},
		{method: "POST", path: "/api/v1/ui/proxy-paths/8/probe-egress", want: false},
		{method: "POST", path: "/api/v1/ui/external-outbounds/import", want: true},
		{method: "POST", path: "/api/v1/ui/routing-rules/place", want: true},
		{method: "PUT", path: "/api/v1/ui/dns-lists/3", want: true},
		{method: "POST", path: "/api/v1/ui/dns-lists/3/set-default", want: true},
		{method: "POST", path: "/api/v1/ui/routing-rule-sets/3/refresh", want: false},
		{method: "POST", path: "/api/v1/ui/port-forwards/8/probe", want: false},
		{method: "PATCH", path: "/api/v1/ui/tunnels/8", want: true},
		{method: "POST", path: "/api/v1/ui/subscription-plans/8/changes/apply", want: false},
		{method: "POST", path: "/api/v1/ui/subscription-plans", want: true},
		{method: "POST", path: "/api/v1/ui/user-node-exceptions", want: true},
		{method: "POST", path: "/api/v1/changesets/cs_1/apply", want: false},
		{method: "GET", path: "/api/v1/ui/inbounds", want: false},
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
		{name: "servers.delete", want: true},
		{name: "servers.enrollment.issue", want: false},
		{name: "servers.extend_expiry", want: false},
		{name: "servers.reset_traffic", want: false},
		{name: "servers.update_agent", want: false},
		{name: "servers.uninstall_agent", want: false},
		{name: "servers.dns_policy.set", want: true},
		{name: "servers.dns_test", want: false},
		{name: "inbounds.create", want: true},
		{name: "inbounds.padding.update", want: true},
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
	third := &model.Server{Name: "scope-unrelated", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 30001, PortRangeEnd: 40000}
	for _, server := range []*model.Server{first, second, third} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	if got := srv.configurationMutationServerIDs(ctx, "/api/v1/ui/servers/"+itoa(first.ID), "PATCH"); len(got) != 1 || got[0] != first.ID {
		t.Fatalf("server patch scope = %v", got)
	}
	if got := srv.configurationMutationServerIDs(ctx, "/api/v1/ui/servers/"+itoa(first.ID), "DELETE"); got != nil {
		t.Fatalf("server delete scope = %v, want all", got)
	}
	forward := &model.PortForward{Name: "scope-forward", SourceServerID: first.ID, TargetServerID: second.ID, ListenPort: 12000, TargetPort: 443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendAuto, ProbeMode: "never", ProbeIntervalSeconds: 300, ConfigJSON: "{}", Enabled: true}
	if err := db.CreatePortForward(ctx, forward); err != nil {
		t.Fatal(err)
	}
	got := srv.configurationMutationServerIDs(ctx, "/api/v1/ui/port-forwards/"+itoa(forward.ID), "DELETE")
	if len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("forward delete scope = %v", got)
	}
	inbound := &model.Inbound{ServerID: first.ID, Name: "scoped-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, Name: "scope-a-b", Secret: "scope-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &second.ID, ConfigJSON: "{}"}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	for label, scoped := range map[string][]int64{
		"inbound": srv.configurationMutationServerIDs(ctx, "/api/v1/ui/inbounds/"+itoa(inbound.ID), http.MethodPatch),
		"path":    srv.configurationMutationServerIDs(ctx, "/api/v1/ui/proxy-paths/"+itoa(path.ID), http.MethodPatch),
		"step":    srv.configurationMutationServerIDs(ctx, "/api/v1/ui/proxy-path-steps/"+itoa(step.ID), http.MethodDelete),
	} {
		if len(scoped) != 2 || scoped[0] != first.ID || scoped[1] != second.ID {
			t.Fatalf("%s topology scope = %v, want [%d %d] without unrelated server %d", label, scoped, first.ID, second.ID, third.ID)
		}
	}
	response, _ := json.Marshal(map[string]any{"proxy_path_step": map[string]any{"id": step.ID, "path_id": path.ID}})
	responseScope, resolved := srv.configurationMutationResponseServerIDs(ctx, "/api/v1/ui/proxy-path-steps", response)
	if !resolved || len(responseScope) != 2 || responseScope[0] != first.ID || responseScope[1] != second.ID {
		t.Fatalf("step create response scope = %v resolved=%t", responseScope, resolved)
	}
}

func TestConfigurationReconcilerIsolatesDuplicateDirectBranchFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	servers := []*model.Server{
		{Name: "broken-entry", AgentID: "broken-agent", AgentTokenHash: security.HashSecret("broken-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 19999},
		{Name: "healthy-a", AgentID: "healthy-a-agent", AgentTokenHash: security.HashSecret("healthy-a-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 29999},
		{Name: "healthy-b", AgentID: "healthy-b-agent", AgentTokenHash: security.HashSecret("healthy-b-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 39999},
	}
	for _, server := range servers {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	entries := make([]*model.Inbound, 0, len(servers))
	for index, server := range servers {
		inbound := &model.Inbound{ServerID: server.ID, Name: server.Name + "-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443 + index, ConfigJSON: "{}", Enabled: true}
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, inbound)
	}
	duplicatePathIDs := make([]int64, 0, 2)
	for _, name := range []string{"duplicate-a", "duplicate-b"} {
		path := &model.ProxyPath{InboundID: entries[0].ID, Kind: model.ProxyPathKindDirect, Name: name, Secret: name + "-secret", Enabled: true}
		if err := db.CreateProxyPath(ctx, path); err != nil {
			t.Fatal(err)
		}
		duplicatePathIDs = append(duplicatePathIDs, path.ID)
	}
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil || revision == 0 {
		t.Fatalf("configuration revision=%d err=%v", revision, err)
	}
	serverIDs := []int64{servers[0].ID, servers[1].ID, servers[2].ID}
	if _, err := db.MarkConfigurationSyncPending(ctx, revision, serverIDs); err != nil {
		t.Fatal(err)
	}
	srv.reconcileConfiguration(ctx)
	broken, err := db.ConfigurationSyncState(ctx, servers[0].ID)
	if err != nil || broken.State != "failed" || !strings.Contains(broken.LastError, fmt.Sprintf("#%d", duplicatePathIDs[0])) || !strings.Contains(broken.LastError, fmt.Sprintf("#%d", duplicatePathIDs[1])) {
		t.Fatalf("broken server state=%#v err=%v", broken, err)
	}
	for _, server := range servers[1:] {
		state, stateErr := db.ConfigurationSyncState(ctx, server.ID)
		if stateErr != nil || state.State != "queued" || state.LastTaskID == 0 {
			t.Fatalf("healthy server %s state=%#v err=%v", server.Name, state, stateErr)
		}
		task, taskErr := db.GetTask(ctx, state.LastTaskID)
		if taskErr != nil || task.ServerID != server.ID || task.Type != model.AgentTaskTypeApplyDeployment {
			t.Fatalf("healthy server %s task=%#v err=%v", server.Name, task, taskErr)
		}
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
	request(t, handler, "POST", "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	login := request(t, handler, "POST", "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
	token := login["token"].(string)
	created := request(t, handler, "POST", "/api/v1/ui/servers", token, map[string]any{"name": "async-save", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 11000}, 201)
	server := created["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	if created["desired_revision"] == nil {
		t.Fatalf("save response missing desired_revision: %#v", created)
	}
	syncRows, ok := created["configuration_sync"].([]any)
	if !ok || len(syncRows) != 1 || syncRows[0].(map[string]any)["state"] != "pending" || syncRows[0].(map[string]any)["agent_reachable"] != false {
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

func TestConfigurationReconcilerWaitsForCertificateWithoutBlockingOtherServers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	waitingServer := &model.Server{Name: "certificate-waiting", AgentID: "certificate-waiting-agent", AgentTokenHash: security.HashSecret("certificate-waiting-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	readyServer := &model.Server{Name: "certificate-independent", AgentID: "certificate-independent-agent", AgentTokenHash: security.HashSecret("certificate-independent-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20001, PortRangeEnd: 30000}
	for _, server := range []*model.Server{waitingServer, readyServer} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	credential := &model.DNSCredential{Name: "certificate-dns", Provider: model.DNSProviderCloudflare, ZoneName: "example.com", Enabled: true}
	if err := db.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	credentialID := credential.ID
	certificate := &model.Certificate{Name: "issuing certificate", PrimaryDomain: "waiting.example.com", Domains: []string{"waiting.example.com"}, ChallengeType: model.CertificateChallengeDNS, DNSCredentialID: &credentialID, ACMECA: "letsencrypt", Status: model.CertificateStatusIssuing, AutoRenew: true}
	if err := db.CreateCertificate(ctx, certificate); err != nil {
		t.Fatal(err)
	}
	waitingInbound := &model.Inbound{ServerID: waitingServer.ID, Name: "waiting entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, DNSCredentialID: &credentialID, DNSDomain: "waiting.example.com", TLS: true, ConfigJSON: `{}`, Enabled: true}
	readyInbound := &model.Inbound{ServerID: readyServer.ID, Name: "ready entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 20443, ConfigJSON: `{}`, Enabled: true}
	for _, inbound := range []*model.Inbound{waitingInbound, readyInbound} {
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertInboundCertificateBinding(ctx, &model.InboundCertificateBinding{InboundID: waitingInbound.ID, CertificateID: &certificate.ID, Mode: model.CertificateModeAuto, ServerName: "waiting.example.com"}); err != nil {
		t.Fatal(err)
	}
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil || revision == 0 {
		t.Fatalf("configuration revision=%d err=%v", revision, err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, revision, []int64{waitingServer.ID, readyServer.ID}); err != nil {
		t.Fatal(err)
	}

	srv.reconcileConfiguration(ctx)
	waitingState, err := db.ConfigurationSyncState(ctx, waitingServer.ID)
	if err != nil || waitingState.State != "pending" || waitingState.RetryCount != 0 || waitingState.NextRetryAt == nil || waitingState.LastError != "等待证书签发完成" {
		t.Fatalf("certificate server state = %#v err=%v", waitingState, err)
	}
	readyState, err := db.ConfigurationSyncState(ctx, readyServer.ID)
	if err != nil || readyState.State != "queued" || readyState.LastTaskID == 0 {
		t.Fatalf("independent server state = %#v err=%v", readyState, err)
	}
	if tasks, err := db.ListTasksByServer(ctx, waitingServer.ID, 10); err != nil || len(tasks) != 0 {
		t.Fatalf("certificate server queued before certificate was ready: tasks=%#v err=%v", tasks, err)
	}

	expiresAt := time.Now().UTC().Add(60 * 24 * time.Hour)
	certificate.Status = model.CertificateStatusReady
	certificate.Revision = "issued-revision"
	certificate.NotAfter = &expiresAt
	if err := db.UpdateCertificate(ctx, certificate); err != nil {
		t.Fatal(err)
	}
	srv.markCertificateServersForSync(ctx, certificate.ID)
	srv.reconcileConfiguration(ctx)
	waitingState, err = db.ConfigurationSyncState(ctx, waitingServer.ID)
	if err != nil || waitingState.State != "queued" || waitingState.LastTaskID == 0 || waitingState.RetryCount != 0 || waitingState.LastError != "" {
		t.Fatalf("certificate server did not resume after issuance = %#v err=%v", waitingState, err)
	}
	task, err := db.GetTask(ctx, waitingState.LastTaskID)
	if err != nil || task.ServerID != waitingServer.ID || task.Type != model.AgentTaskTypeApplyDeployment || !strings.Contains(task.PayloadJSON, "issued-revision") {
		t.Fatalf("resumed deployment task = %#v err=%v", task, err)
	}
}

func TestInvalidConfigurationWriteDoesNotCreateDesiredState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	handler := srv.Handler()
	request(t, handler, "POST", "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	login := request(t, handler, "POST", "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
	token := login["token"].(string)
	request(t, handler, "POST", "/api/v1/ui/servers", token, map[string]any{"name": "invalid", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 10000}, 400)
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
	request(t, handler, "POST", "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	login := request(t, handler, "POST", "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
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
	response := request(t, handler, "POST", "/api/v1/ui/configuration-sync/retry", token, map[string]any{"server_ids": []int64{server.ID}}, 202)
	if response["retried"] != float64(1) {
		t.Fatalf("retry response = %#v", response)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" || state.RetryCount != 0 {
		t.Fatalf("retry state = %#v err=%v", state, err)
	}
	request(t, handler, "POST", "/api/v1/ui/configuration-sync/retry", token, map[string]any{"server_ids": []int64{server.ID}}, 409)
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

func TestFailedSyncHealthReportDoesNotReopenReconciliation(t *testing.T) {
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
	if err != nil || state.State != "failed" {
		t.Fatalf("failed health report reopened sync = %#v err=%v", state, err)
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

func TestSemanticNoopPreservesHealthReportConvergence(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "noop-node", AgentID: "noop-agent", AgentTokenHash: security.HashSecret("noop-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "in1", Protocol: "vless", Port: 8443, ConfigJSON: `{}`}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv.reconcileConfiguration(ctx)
	task, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil || task == nil {
		t.Fatalf("expected apply_deployment task, got err=%v", err)
	}
	if err := db.SetTaskStateForTest(ctx, task.ID, "succeeded", task.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, task.ConfigVersion, true, ""); err != nil {
		t.Fatal(err)
	}
	expectedDigest := configurationTaskPayloadDigest(*task)
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "synced" {
		t.Fatalf("state after task success = %#v, err=%v", state, err)
	}

	// Trigger a new configuration revision (e.g. an unrelated server is added or modified)
	if _, err := db.MarkConfigurationSyncPending(ctx, revision+1, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	srv.reconcileConfiguration(ctx)
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "synced" || state.SyncStrategy != "semantic_noop" {
		t.Fatalf("state after semantic noop = %#v, err=%v", state, err)
	}
	if state.WantedDigest != expectedDigest {
		t.Fatalf("wanted_digest after semantic noop = %q, want %q", state.WantedDigest, expectedDigest)
	}

	// Agent reports regular health with its active version and digest
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(model.HealthReport{
		Status:               model.ServerOnline,
		AppliedConfigVersion: task.ConfigVersion,
		AppliedConfigDigest:  expectedDigest,
		Timestamp:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.processAgentSocketMessage(ctx, stored, map[string]json.RawMessage{"health_report": raw}, "192.0.2.10")

	// State must stay synced and not drift back to pending
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "synced" {
		t.Fatalf("health report reopened sync loop = %#v, err=%v", state, err)
	}
}

func TestLegacySemanticNoopDigestConvergesAndRepairs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "legacy-noop-node", AgentID: "legacy-noop-agent", AgentTokenHash: security.HashSecret("legacy-noop-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "in1", Protocol: "vless", Port: 8443, ConfigJSON: `{}`}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
	srv.reconcileConfiguration(ctx)
	task, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil || task == nil {
		t.Fatalf("expected task, got err=%v", err)
	}
	if err := db.SetTaskStateForTest(ctx, task.ID, "succeeded", task.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, task.ConfigVersion, true, ""); err != nil {
		t.Fatal(err)
	}
	expectedDigest := configurationTaskPayloadDigest(*task)

	// Simulate legacy state where wanted_digest was written as semantic_noop:10
	if err := db.MarkConfigurationSyncNoop(ctx, server.ID, 10, "semantic_noop:10"); err != nil {
		t.Fatal(err)
	}

	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(model.HealthReport{
		Status:               model.ServerOnline,
		AppliedConfigVersion: task.ConfigVersion,
		AppliedConfigDigest:  expectedDigest,
		Timestamp:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.processAgentSocketMessage(ctx, stored, map[string]json.RawMessage{"health_report": raw}, "192.0.2.10")

	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "synced" {
		t.Fatalf("legacy semantic noop caused drift = %#v, err=%v", state, err)
	}
	if state.WantedDigest != expectedDigest {
		t.Fatalf("legacy semantic noop was not repaired = %q, want %q", state.WantedDigest, expectedDigest)
	}
}

func failLatestApplyDeployment(t *testing.T, db *store.Store, serverID int64) *model.AgentTask {
	t.Helper()
	ctx := context.Background()
	task, err := db.ActiveTaskByServerType(ctx, serverID, model.AgentTaskTypeApplyDeployment)
	if err != nil || task == nil {
		t.Fatalf("expected apply_deployment task, got err=%v", err)
	}
	if err := db.SetTaskStateForTest(ctx, task.ID, "failed", task.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, serverID, task.ConfigVersion, false, "部署失败：1个关键步骤未完成"); err != nil {
		t.Fatal(err)
	}
	failed, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return failed
}

func TestFailedDeploymentDoesNotLoopOrCreateNewConfig(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "loop-node", AgentID: "loop-agent", AgentTokenHash: security.HashSecret("loop-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
	srv.reconcileConfiguration(ctx)
	first := failLatestApplyDeployment(t, db, server.ID)
	before := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(model.HealthReport{Status: model.ServerOnline, AppliedConfigVersion: 0, Timestamp: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		srv.processAgentSocketMessage(ctx, stored, map[string]json.RawMessage{"health_report": raw}, "192.0.2.10")
		srv.reconcileConfiguration(ctx)
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != before {
		t.Fatalf("failed deployment created extra tasks %d -> %d", before, got)
	}
	if got := maxTaskConfigVersion(t, db); got != first.ConfigVersion {
		t.Fatalf("failed deployment allocated new config_version %d, want %d", got, first.ConfigVersion)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "failed" {
		t.Fatalf("failed deployment state = %#v err=%v", state, err)
	}
}

func TestUnchangedRevisionAfterFailedDeployDoesNotCreateConfig(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "stale-rev-node", AgentID: "stale-rev-agent", AgentTokenHash: security.HashSecret("stale-rev-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
	srv.reconcileConfiguration(ctx)
	first := failLatestApplyDeployment(t, db, server.ID)
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, revision+1, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	before := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	srv.reconcileConfiguration(ctx)
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != before {
		t.Fatalf("unchanged revision created apply_deployment %d -> %d", before, got)
	}
	if got := maxTaskConfigVersion(t, db); got != first.ConfigVersion {
		t.Fatalf("unchanged revision allocated config_version %d, want %d", got, first.ConfigVersion)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "failed" {
		t.Fatalf("unchanged revision state = %#v err=%v", state, err)
	}
}

func TestOperatorRetryRequeuesSameConfigVersion(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "retry-node", AgentID: "retry-agent", AgentTokenHash: security.HashSecret("retry-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
	srv.reconcileConfiguration(ctx)
	first := failLatestApplyDeployment(t, db, server.ID)
	if _, err := db.RetryFailedConfigurationSync(ctx, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	srv.reconcileConfiguration(ctx)
	latest, err := db.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil || latest == nil {
		t.Fatalf("operator retry missing task err=%v", err)
	}
	if latest.ID == first.ID || latest.ConfigVersion != first.ConfigVersion || latest.Status != "pending" {
		t.Fatalf("operator retry task = %#v, first = %#v", latest, first)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "queued" || state.LastTaskID != latest.ID || state.LastConfigVersion != first.ConfigVersion {
		t.Fatalf("operator retry sync state = %#v err=%v", state, err)
	}
}
