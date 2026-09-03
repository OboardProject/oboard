package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRecoveryQueuesDeploymentAndSupersedesStaleTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")

	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("token-edge"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 60000, Status: model.ServerOffline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}

	// A stale pre-outage deployment must never be delivered after recovery.
	stale := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: `{"version":3}`, Status: "pending", ConfigVersion: 3, Nonce: "stale-deployment"}
	if err := db.CreateTask(ctx, stale); err != nil {
		t.Fatal(err)
	}

	// Recovery marks the server online first (UpsertHealthTransition), then
	// handleServerRecovered queues the fresh desired state.
	server.Status = model.ServerOnline
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	srv.handleServerRecovered(ctx, server.ID)

	storedStale, err := db.GetTask(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedStale.Status != "failed" || !strings.Contains(storedStale.ResultJSON, "恢复在线") {
		t.Fatalf("stale deployment was not superseded: %#v", storedStale)
	}

	fresh, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "pending" || fresh.ConfigVersion == 0 || fresh.ConfigVersion <= stale.ConfigVersion {
		t.Fatalf("recovery did not queue a fresh deployment: %#v", fresh)
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(fresh.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != fresh.ConfigVersion || strings.TrimSpace(payload.Config.Config) == "" {
		t.Fatalf("recovery deployment payload is incomplete: %#v", payload)
	}
	var cfg struct {
		Inbounds []struct {
			ListenPort int `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(payload.Config.Config), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 1 || cfg.Inbounds[0].ListenPort != 10443 {
		t.Fatalf("recovery deployment config = %#v, want inbound port 10443", cfg.Inbounds)
	}
}

func TestRecoverySkipsDeploymentWithoutRelevantState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")

	server := &model.Server{Name: "empty", AgentID: "agent-empty", AgentTokenHash: security.HashSecret("token-empty"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOffline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	server.Status = model.ServerOnline
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	srv.handleServerRecovered(ctx, server.ID)

	tasks, err := db.ListTasksByServer(ctx, server.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("brand-new server without topology must not receive a deployment: %#v", tasks)
	}
}

func TestEnrollmentQueuesDeploymentForExistingTopology(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()

	server := &model.Server{Name: "reinstall", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 60000, Status: model.ServerOffline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	enrollmentToken := "reinstall-one-time-token"
	if err := db.SetServerEnrollmentHash(ctx, server.ID, security.HashSecret(enrollmentToken), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/enroll", strings.NewReader(`{"enrollment_token":"`+enrollmentToken+`","health":{"status":"online","os":"linux","arch":"amd64","agent_version":"0.1.0","agent_build":"20260711050000"}}`))
	req.Header.Set("content-type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enroll status = %d body=%s", rr.Code, rr.Body.String())
	}

	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := db.ActiveTaskByServerType(ctx, stored.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatalf("re-enrollment did not queue a deployment: %v", err)
	}
	if fresh.Status != "pending" || fresh.ConfigVersion == 0 {
		t.Fatalf("re-enrollment deployment task is not pending: %#v", fresh)
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(fresh.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(payload.Config.Config) == "" || !strings.Contains(payload.Config.Config, "10443") {
		t.Fatalf("re-enrollment deployment payload missing config: %#v", payload)
	}
}

func TestRecoveryOfTransparentForwardMemberExpandsToFullDeployment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")

	root := &model.Server{Name: "root", AgentID: "agent-root", AgentTokenHash: security.HashSecret("token-root"), AgentBuild: agentBuildMinSSHPathRelay, EntryAddress: "203.0.113.1", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOffline}
	processing := &model.Server{Name: "processing", AgentID: "agent-processing", AgentTokenHash: security.HashSecret("token-processing"), AgentBuild: agentBuildMinSSHPathRelay, EntryAddress: "203.0.113.2", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010, Status: model.ServerOnline}
	for _, server := range []*model.Server{root, processing} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	inbound := &model.Inbound{ServerID: root.ID, Name: "root", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10001, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Name: "trusted-chain", InboundID: inbound.ID, Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ServerID: &processing.ID, ConfigJSON: "{}"}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}

	root.Status = model.ServerOnline
	if err := db.UpdateServer(ctx, root); err != nil {
		t.Fatal(err)
	}
	srv.handleServerRecovered(ctx, root.ID)

	for _, server := range []*model.Server{root, processing} {
		fresh, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeApplyDeployment)
		if err != nil {
			t.Fatalf("trusted recovery did not deploy server %s: %v", server.Name, err)
		}
		if fresh.Status != "pending" {
			t.Fatalf("trusted recovery task for %s is not pending: %#v", server.Name, fresh)
		}
	}
	// The single-server scope was expanded: both trusted members got the same
	// version instead of only the recovered root being deployed.
	rootTask, err := db.ActiveTaskByServerType(ctx, root.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	processingTask, err := db.ActiveTaskByServerType(ctx, processing.ID, model.AgentTaskTypeApplyDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if rootTask.ConfigVersion == 0 || rootTask.ConfigVersion != processingTask.ConfigVersion {
		t.Fatalf("trusted members deployed with different versions: root=%d processing=%d", rootTask.ConfigVersion, processingTask.ConfigVersion)
	}
}
