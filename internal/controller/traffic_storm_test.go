package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestTrafficReportsDoNotTriggerConfigurationDeployment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	servers := make([]*model.Server, 0, 20)
	for i := 0; i < 20; i++ {
		server := &model.Server{
			Name: fmt.Sprintf("storm-%02d", i), AgentID: fmt.Sprintf("agent-%02d", i),
			AgentTokenHash: security.HashSecret("token-storm"), Status: model.ServerOnline,
			ListenIP: "0.0.0.0", PortRangeStart: 10000 + i*10, PortRangeEnd: 10009 + i*10,
		}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server)
	}
	inbound := &model.Inbound{ServerID: servers[0].ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "storm-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111199", ProxyPassword: "password", TrafficLimitBytes: 1 << 30}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)

	beforeConfig, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeRouting, err := db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeDeploy := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	beforeCore := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig)
	beforeVersion := maxTaskConfigVersion(t, db)

	h := newTestServer(db, "test-secret", "").Handler()
	var used int64
	for i := 0; i < 100; i++ {
		from := used
		used += 1024
		body := ledgerTrafficBody(user.ID, inbound.ID, fmt.Sprintf("tr-storm-%d", i), from, used, from, used)
		resp := postAgentTraffic(t, h, servers[0].AgentID, "token-storm", body, http.StatusOK)
		if resp["ok"] != true {
			t.Fatalf("traffic report %d not ok: %#v", i, resp)
		}
		if resp["policy_revision"] == nil {
			t.Fatalf("traffic report %d missing policy_revision: %#v", i, resp)
		}
	}
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes < used*2 {
		t.Fatalf("traffic_used_bytes = %d, want at least %d", stored.TrafficUsedBytes, used*2)
	}
	afterConfig, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterRouting, err := db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterConfig != beforeConfig {
		t.Fatalf("configuration_revision %d -> %d", beforeConfig, afterConfig)
	}
	if afterRouting != beforeRouting {
		t.Fatalf("routing_cache_revision %d -> %d", beforeRouting, afterRouting)
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != beforeDeploy {
		t.Fatalf("apply_deployment count %d -> %d", beforeDeploy, got)
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig); got != beforeCore {
		t.Fatalf("apply_core_config count %d -> %d", beforeCore, got)
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyTrafficPolicy); got != 0 {
		t.Fatalf("traffic reports queued apply_traffic_policy: %d", got)
	}
	if got := maxTaskConfigVersion(t, db); got != beforeVersion {
		t.Fatalf("config_version %d -> %d", beforeVersion, got)
	}
}

func TestRoutingSnapshotCacheStableOnTrafficUsage(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	before, err := db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-cache-1", 0, 50, 0, 50), http.StatusOK)
	postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-cache-2", 50, 150, 50, 150), http.StatusOK)
	after, err := db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("routing cache revision moved on traffic usage %d -> %d", before, after)
	}
}

func TestAdminTrafficLimitQueuesTrafficPolicyNotDeployment(t *testing.T) {
	db, server, inbound, user, _ := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	ctl := newTestServer(db, "test-secret", "")
	server.KernelCapabilities = []string{model.AgentCapabilityTrafficPolicy}
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeDeploy := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	beforeCore := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig)
	updated := *user
	updated.TrafficLimitBytes = 2 << 30
	if err := db.UpdateUser(ctx, &updated); err != nil {
		t.Fatal(err)
	}
	ctl.syncUserChange(ctx, *user, updated)
	_ = inbound
	if after, err := db.ConfigurationRevision(ctx); err != nil || after != beforeConfig {
		t.Fatalf("quota change bumped configuration_revision")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != beforeDeploy {
		t.Fatalf("quota change created apply_deployment")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig); got != beforeCore {
		t.Fatalf("quota change created apply_core_config")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyTrafficPolicy); got == 0 {
		t.Fatal("quota change did not create apply_traffic_policy")
	}
}

func TestAdminSpeedLimitQueuesTrafficPolicyNotDeployment(t *testing.T) {
	db, server, _, user, _ := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	ctl := newTestServer(db, "test-secret", "")
	server.KernelCapabilities = []string{model.AgentCapabilityTrafficPolicy}
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	beforeDeploy := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	beforeCore := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig)
	updated := *user
	updated.SpeedLimitMbps = 80
	if err := db.UpdateUser(ctx, &updated); err != nil {
		t.Fatal(err)
	}
	ctl.syncUserChange(ctx, *user, updated)
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != beforeDeploy {
		t.Fatal("speed limit change created apply_deployment")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig); got != beforeCore {
		t.Fatal("speed limit change created apply_core_config")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyTrafficPolicy); got == 0 {
		t.Fatal("speed limit change did not create apply_traffic_policy")
	}
}

func TestUserDisableQueuesCoreConfigNotDeployment(t *testing.T) {
	db, _, _, user, _ := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	ctl := newTestServer(db, "test-secret", "")
	beforeConfig, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeDeploy := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	updated := *user
	updated.Status = "disabled"
	if err := db.UpdateUser(ctx, &updated); err != nil {
		t.Fatal(err)
	}
	ctl.syncUserChange(ctx, *user, updated)
	afterConfig, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterConfig != beforeConfig {
		t.Fatalf("disabling a user bumped configuration_revision %d -> %d", beforeConfig, afterConfig)
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != beforeDeploy {
		t.Fatal("disabling a user created apply_deployment")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig); got == 0 {
		t.Fatal("disabling a user did not create apply_core_config")
	}
}

func TestLegacyAgentTrafficPolicyFallsBackToCoreConfig(t *testing.T) {
	db, _, _, user, _ := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	ctl := newTestServer(db, "test-secret", "")
	beforeDeploy := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	updated := *user
	updated.TrafficLimitBytes = 5 << 30
	if err := db.UpdateUser(ctx, &updated); err != nil {
		t.Fatal(err)
	}
	ctl.syncUserChange(ctx, *user, updated)
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyTrafficPolicy); got != 0 {
		t.Fatal("legacy agent received apply_traffic_policy")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != beforeDeploy {
		t.Fatal("legacy fallback created apply_deployment")
	}
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig); got == 0 {
		t.Fatal("legacy agent did not fall back to apply_core_config")
	}
}

func TestPendingTrafficStormDeploymentsAreSuperseded(t *testing.T) {
	db, server, _, _, _ := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	ctl := newTestServer(db, "test-secret", "")
	data, err := db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := ctl.generateServerCoreConfigWithLedger(ctx, *server, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(model.DeploymentTaskPayload{Config: model.ApplyCoreConfigTaskPayload{Config: generated.Config}})
	if err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: string(payload), Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "storm-pending"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	ctl.cleanupTrafficStormPendingDeployments(ctx)
	stored, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" {
		t.Fatalf("storm pending task status=%s, want superseded/failed", stored.Status)
	}
}

func TestAutomaticReconcilerSemanticNoopSkipsUnchangedServers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ctl := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "noop-node", AgentID: "noop-agent", AgentTokenHash: security.HashSecret("noop-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	ctl.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
	ctl.reconcileConfiguration(ctx)
	first, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskStateForTest(ctx, first.ID, "succeeded", first.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, first.ConfigVersion, true, ""); err != nil {
		t.Fatal(err)
	}
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, revision+1, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	before := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment)
	ctl.reconcileConfiguration(ctx)
	if got := countTasksByType(t, db, model.AgentTaskTypeApplyDeployment); got != before {
		t.Fatalf("semantic noop created apply_deployment %d -> %d", before, got)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "synced" || state.SyncStrategy != "semantic_noop" {
		t.Fatalf("sync state = %#v, want synced semantic_noop", state)
	}
}

func countTasksByType(t *testing.T, db *store.Store, taskType string) int {
	t.Helper()
	ctx := context.Background()
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, server := range servers {
		tasks, err := db.ListTasksByServer(ctx, server.ID, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.Type == taskType {
				count++
			}
		}
	}
	return count
}

func maxTaskConfigVersion(t *testing.T, db *store.Store) int64 {
	t.Helper()
	ctx := context.Background()
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var max int64
	for _, server := range servers {
		tasks, err := db.ListTasksByServer(ctx, server.ID, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.ConfigVersion > max {
				max = task.ConfigVersion
			}
		}
	}
	return max
}

func TestTrafficReportPerformanceDoesNotDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("performance")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	servers := make([]*model.Server, 0, 100)
	for i := 0; i < 100; i++ {
		server := &model.Server{
			Name: fmt.Sprintf("perf-%03d", i), AgentID: fmt.Sprintf("agent-perf-%03d", i),
			AgentTokenHash: security.HashSecret("token-perf"), Status: model.ServerOnline,
			ListenIP: "0.0.0.0", PortRangeStart: 10000 + i, PortRangeEnd: 10000 + i + 9,
		}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server)
	}
	inbound := &model.Inbound{ServerID: servers[0].ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "perf-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111177", ProxyPassword: "password", TrafficLimitBytes: 10 << 30}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	beforeConfig, _ := db.ConfigurationRevision(ctx)
	beforeRouting, _ := db.RoutingCacheRevision(ctx)
	h := newTestServer(db, "test-secret", "").Handler()
	var cursor int64
	for cycle := 0; cycle < 20; cycle++ {
		from := cursor
		cursor += 4096
		postAgentTraffic(t, h, servers[0].AgentID, "token-perf", ledgerTrafficBody(user.ID, inbound.ID, fmt.Sprintf("tr-perf-%d", cycle), from, cursor, from, cursor), http.StatusOK)
	}
	afterConfig, _ := db.ConfigurationRevision(ctx)
	afterRouting, _ := db.RoutingCacheRevision(ctx)
	if afterConfig != beforeConfig || afterRouting != beforeRouting {
		t.Fatalf("performance traffic moved revisions config %d->%d routing %d->%d", beforeConfig, afterConfig, beforeRouting, afterRouting)
	}
	if countTasksByType(t, db, model.AgentTaskTypeApplyDeployment) != 0 || countTasksByType(t, db, model.AgentTaskTypeApplyCoreConfig) != 0 {
		t.Fatal("performance traffic queued configuration tasks")
	}
}
