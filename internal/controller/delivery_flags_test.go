package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRuntimeUsersLaneHonorsGraySwitch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "users-flag-secret", "")
	server := &model.Server{
		Name: "flag-users", PublicIPv4: "203.0.113.21", AgentID: "flag-users-agent",
		AgentTokenHash: security.HashSecret("flag-users-token"), Status: model.ServerOnline,
		KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, model.AgentCapabilityRuntimeUsersVLESS},
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "vless", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := srv.saveServerUpdate(ctx, server, nil, nil, &off); err != nil {
		t.Fatal(err)
	}
	controlCh := make(chan any, 4)
	srv.registerAgentLive(server.ID, controlCh)
	defer srv.unregisterAgentLive(server.ID, controlCh)
	srv.reconcileRuntimeUsersSync(ctx, true)
	select {
	case payload := <-controlCh:
		t.Fatalf("disabled users lane still pushed users_update: %+v", payload)
	default:
	}
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingReason != store.RuntimeUsersPendingCoreConfigFallback {
		t.Fatalf("pending reason = %q", state.PendingReason)
	}
}

func TestAuthorizationFastLaneHonorsGraySwitch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "auth-flag-secret", "")
	server := &model.Server{
		Name: "flag-auth", PublicIPv4: "203.0.113.22", AgentID: "flag-auth-agent",
		AgentTokenHash: security.HashSecret("flag-auth-token"), Status: model.ServerOnline,
		KernelCapabilities: []string{model.AgentCapabilityAuthorizationLease, model.AgentCapabilityAuthorizationControl},
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "socks", Protocol: model.ProtocolSocks, Port: 10443, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := srv.saveServerUpdate(ctx, server, nil, &off, nil); err != nil {
		t.Fatal(err)
	}
	controlCh := make(chan any, 4)
	srv.registerAgentLive(server.ID, controlCh)
	defer srv.unregisterAgentLive(server.ID, controlCh)
	srv.reconcileAuthorizationSync(ctx, true)
	select {
	case payload := <-controlCh:
		t.Fatalf("disabled auth lane still pushed authorization_update: %+v", payload)
	default:
	}
	state, err := db.AuthorizationState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingReason != store.AuthorizationPendingCompatibilityTask {
		t.Fatalf("pending reason = %q", state.PendingReason)
	}
}

func TestLegalTrafficTailClassifiesDisableAndDelete(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "tail-secret", "")
	server := &model.Server{Name: "tail-ctrl"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "tail-ctrl-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if kind, ok := srv.legalTrafficTail(ctx, server.ID, user.ID, "missing", "user_inactive"); ok || kind != "" {
		t.Fatalf("new stream after disable must stay unattributable: kind=%q ok=%v", kind, ok)
	}
	if _, err := db.CommitTrafficLedger(ctx, store.TrafficLedgerCommit{
		ServerID:        server.ID,
		AgentInstanceID: "tail-agent",
		Streams: []model.TrafficStreamObservation{{
			UserID: user.ID, Source: "core", StreamID: "stream-1",
			CounterEpoch: "epoch-1", PeriodKey: "2026-09", InboundID: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	kind, ok := srv.legalTrafficTail(ctx, server.ID, user.ID, "stream-1", "user_inactive")
	if !ok || kind != trafficTailActiveCheckpoint {
		t.Fatalf("disable tail: kind=%q ok=%v", kind, ok)
	}
	if err := db.Delete(ctx, "users", user.ID); err != nil {
		t.Fatal(err)
	}
	kind, ok = srv.legalTrafficTail(ctx, server.ID, user.ID, "stream-1", "user_deleted")
	if !ok || kind != trafficTailDeletedSnapshot {
		t.Fatalf("delete tail: kind=%q ok=%v", kind, ok)
	}
	kind, ok = srv.legalTrafficTail(ctx, server.ID, user.ID, "other", "user_deleted")
	if ok || kind != "" {
		t.Fatalf("unmatched delete must be unattributable: kind=%q ok=%v", kind, ok)
	}
}

func TestDeliveryPolicyRefreshSurvivesSemanticNoop(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "policy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "policy-secret", "")
	server := &model.Server{Name: "policy", AgentID: "policy-agent", AgentTokenHash: security.HashSecret("token"), PublicIPv4: "203.0.113.21", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	srv.reconcileConfiguration(ctx)
	first, err := db.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskStateForTest(ctx, first.ID, "succeeded", first.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, first.ConfigVersion, true, ""); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := srv.saveServerUpdate(ctx, server, nil, nil, &off); err != nil {
		t.Fatal(err)
	}
	saved, err := db.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != first.ID {
		t.Fatal("saving constructed a deployment synchronously")
	}
	srv.reconcileConfiguration(ctx)
	next, err := db.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == first.ID {
		t.Fatal("identical topology swallowed policy change")
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(next.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.ForceRefresh || !payload.ConfigChanged {
		t.Fatalf("policy task did not force runtime refresh: %+v", payload)
	}
	flags, err := db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if flags.ProcessingRevision != flags.Revision || flags.ProcessingConfigVersion != next.ConfigVersion || flags.AppliedRevision != 0 {
		t.Fatalf("unbound policy task: %+v", flags)
	}
	// Ordinary unrelated changes while the policy is in flight may reuse its task.
	other := &model.Server{Name: "unrelated"}
	if err := db.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, revision, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	srv.reconcileConfiguration(ctx)
	current, err := db.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != next.ID {
		t.Fatal("in-flight policy created duplicate deployment")
	}
	if err := db.SetTaskStateForTest(ctx, next.ID, "failed", next.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, next.ConfigVersion, false, "application rejected"); err != nil {
		t.Fatal(err)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan := srv.automaticProjectionChanges(ctx, []store.ConfigurationSyncState{state})
	if plan.changed[server.ID] || len(plan.keepFailed) != 1 {
		t.Fatalf("failed policy should remain blocked, got %+v", plan)
	}
	superseded := state
	superseded.TriggerReason = store.ConfigurationSyncTriggerSuperseded
	if recovery := srv.automaticProjectionChanges(ctx, []store.ConfigurationSyncState{superseded}); !recovery.changed[server.ID] {
		t.Fatal("superseded policy refresh stayed blocked")
	}
	if _, err := db.RetryFailedConfigurationSync(ctx, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	srv.reconcileConfiguration(ctx)
	retry, err := db.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID == next.ID {
		t.Fatal("operator retry did not rebuild policy refresh")
	}
	if err := db.SetTaskStateForTest(ctx, retry.ID, "succeeded", retry.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, retry.ConfigVersion, true, ""); err != nil {
		t.Fatal(err)
	}
	flags, err = db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if flags.AppliedRevision != flags.Revision {
		t.Fatalf("successful retry did not confirm policy: %+v", flags)
	}
}
