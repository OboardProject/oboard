package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type externalEgressControllerFixture struct {
	db       *store.Store
	server   *model.Server
	inbound  *model.Inbound
	external *model.ExternalOutbound
	path     *model.ProxyPath
	step     *model.ProxyPathStep
	srv      *Server
}

func newExternalEgressControllerFixture(t *testing.T) externalEgressControllerFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	server := &model.Server{
		Name: "egress-owner", AgentID: "agent-egress", AgentBuild: agentBuildMinExternalEgress,
		EntryAddress: "8.8.4.4", PublicIPv4: "8.8.4.4", ListenIP: "0.0.0.0",
		PortRangeStart: 30000, PortRangeEnd: 30100, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	external := &model.ExternalOutbound{Name: "imported", Protocol: model.ProtocolSocks, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "1.1.1.1", TargetPort: 1080, ConfigJSON: "{}", RegionMode: "auto", Enabled: true}
	if err := db.CreateExternalOutbound(ctx, external); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, InboundID: inbound.ID, ExitRegionMode: "auto", Secret: "egress-test-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepImported, TransportMode: model.ProxyPathTransportSingBox, ExternalOutboundID: &external.ID, ConfigJSON: "{}"}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	return externalEgressControllerFixture{db: db, server: server, inbound: inbound, external: external, path: path, step: step, srv: newTestServer(db, "test-secret", "")}
}

func TestFullDeploymentAddsExternalEgressPlanAndManualProbeReusesActiveTask(t *testing.T) {
	fixture := newExternalEgressControllerFixture(t)
	handler := fixture.srv.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	deployment := request(t, handler, http.MethodPost, "/api/v2/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	tasks := deployment["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("deployment tasks = %#v", tasks)
	}
	taskMap := tasks[0].(map[string]any)
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(taskMap["payload_json"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExternalEgressProbe == nil || len(payload.ExternalEgressProbe.Targets) != 1 {
		t.Fatalf("deployment egress probe = %#v", payload.ExternalEgressProbe)
	}
	target := payload.ExternalEgressProbe.Targets[0]
	if target.PathID != fixture.path.ID || target.ExternalOutboundID != fixture.external.ID || target.OwnerServerID != fixture.server.ID || target.OutboundTag != "path-"+strconv.FormatInt(fixture.path.ID, 10)+"-step-1" {
		t.Fatalf("deployment egress target = %#v", target)
	}
	if payload.ExternalEgressProbe.ExpectedConfigVersion != int64(deployment["config_version"].(float64)) {
		t.Fatalf("egress config version = %d, deployment = %v", payload.ExternalEgressProbe.ExpectedConfigVersion, deployment["config_version"])
	}

	deployedDigest, err := canonicalConfigSHA256(payload.Config.Config)
	if err != nil {
		t.Fatal(err)
	}
	deploymentResult, err := json.Marshal(map[string]any{"steps": []map[string]any{{"key": "config", "result": map[string]any{"effective_config_sha256": deployedDigest}}}})
	if err != nil {
		t.Fatal(err)
	}
	deploymentTaskID := int64(taskMap["id"].(float64))
	if err := fixture.db.CompleteTask(context.Background(), deploymentTaskID, "succeeded", string(deploymentResult)); err != nil {
		t.Fatal(err)
	}
	currentData, err := fixture.db.FullRoutingConfigData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolveRoutingProxyPathNames(&currentData)
	regenerated, err := fixture.srv.generateServerCoreConfigWithLedger(context.Background(), *fixture.server, currentData, core.NewProxyPathPortLedger(currentData.ProxyPathPortAllocations))
	if err != nil {
		t.Fatal(err)
	}
	regeneratedDigest, err := canonicalConfigSHA256(regenerated.Config)
	if err != nil {
		t.Fatal(err)
	}
	if deployedDigest != regeneratedDigest {
		t.Fatalf("deployed and regenerated config digests differ: %s != %s", deployedDigest, regeneratedDigest)
	}
	pathURL := "/api/v2/ui/proxy-paths/" + strconv.FormatInt(fixture.path.ID, 10) + "/probe-egress"
	first := request(t, handler, http.MethodPost, pathURL, token, map[string]any{}, http.StatusAccepted)
	if first["reused"] != false {
		t.Fatalf("first manual probe unexpectedly reused a task: %#v", first)
	}
	second := request(t, handler, http.MethodPost, pathURL, token, map[string]any{}, http.StatusAccepted)
	if second["reused"] != true {
		t.Fatalf("second manual probe did not reuse active task: %#v", second)
	}
	queued, err := fixture.db.ListTasksByServer(context.Background(), fixture.server.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	probeTasks := 0
	for _, task := range queued {
		if task.Type == model.AgentTaskTypeProbeExternalEgress {
			probeTasks++
		}
	}
	if probeTasks != 1 {
		t.Fatalf("manual probe task count = %d, want 1", probeTasks)
	}
}

func TestProbeProxyPathEgressRejectsUnsupportedAgentBuild(t *testing.T) {
	fixture := newExternalEgressControllerFixture(t)
	fixture.server.AgentBuild = "20260731235959"
	if err := fixture.db.UpdateServer(context.Background(), fixture.server); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/ui/proxy-paths/1/probe-egress", nil)
	fixture.srv.probeProxyPathEgress(recorder, request, fixture.path.ID)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "Agent") {
		t.Fatalf("unsupported Agent response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestApplyExternalEgressTaskResultsValidatesAndClassifiesControllerSide(t *testing.T) {
	fixture := newExternalEgressControllerFixture(t)
	ctx := context.Background()
	data, err := fixture.db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	targets := core.ExternalEgressProbeTargets(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, data.ExternalOutbounds)
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	plan := model.ExternalEgressProbePlan{Version: 42, ExpectedConfigVersion: 42, TimeoutMS: externalEgressTimeoutMS, Targets: targets}
	payloadJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	newTask := func(nonce string) model.AgentTask {
		t.Helper()
		task := model.AgentTask{ServerID: fixture.server.ID, Type: model.AgentTaskTypeProbeExternalEgress, PayloadJSON: string(payloadJSON), Status: "pending", ResultJSON: `{}`, ConfigVersion: 42, Nonce: nonce}
		if err := fixture.db.CreateTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	resultJSON := func(probeID, exitIP string) string {
		t.Helper()
		raw, err := json.Marshal(model.ExternalEgressProbeResult{Items: []model.ExternalEgressProbeItem{{ProbeID: probeID, Status: "succeeded", ExitIP: exitIP}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	fixture.srv.geoIP = &fakeConnectionAuditGeoResolver{geo: model.IPGeography{CountryCode: "US", Revision: "geo-v1"}}
	fixture.srv.geoIPStatus = model.GeoDatabaseStatus{Available: true, Revision: "geo-v1"}
	task := newTask("egress-valid")
	if err := fixture.srv.applyExternalEgressTaskResults(ctx, fixture.server.ID, task, "succeeded", resultJSON(targets[0].ProbeID, "8.8.8.8")); err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.db.GetProxyPathEgressResult(ctx, fixture.path.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "succeeded" || stored.LastExitIP != "8.8.8.8" || stored.LastRegionCode != "US" || stored.GeoDatabaseRevision != "geo-v1" {
		t.Fatalf("classified egress result = %#v", stored)
	}

	invalidTask := newTask("egress-unknown-probe")
	if err := fixture.srv.applyExternalEgressTaskResults(ctx, fixture.server.ID, invalidTask, "succeeded", resultJSON("unknown", "8.8.8.8")); err == nil {
		t.Fatal("unknown probe_id was accepted")
	}

	fixture.srv.geoIP = &fakeConnectionAuditGeoResolver{geo: model.IPGeography{CountryCode: "AQ", Revision: "geo-v2"}}
	aqTask := newTask("egress-aq")
	if err := fixture.srv.applyExternalEgressTaskResults(ctx, fixture.server.ID, aqTask, "succeeded", resultJSON(targets[0].ProbeID, "8.8.4.4")); err != nil {
		t.Fatal(err)
	}
	stored, err = fixture.db.GetProxyPathEgressResult(ctx, fixture.path.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != core.RegionStatusFailed || stored.LastRegionCode != "US" || stored.LastExitIP != "8.8.8.8" || stored.LastError == "" {
		t.Fatalf("AQ classification should fail while retaining the last success: %#v", stored)
	}

	previousTaskID := *stored.TaskID
	fixture.external.TargetPort++
	if err := fixture.db.UpdateExternalOutbound(ctx, fixture.external); err != nil {
		t.Fatal(err)
	}
	staleTask := newTask("egress-stale")
	if err := fixture.srv.applyExternalEgressTaskResults(ctx, fixture.server.ID, staleTask, "succeeded", resultJSON(targets[0].ProbeID, "1.1.1.1")); err != nil {
		t.Fatal(err)
	}
	stored, err = fixture.db.GetProxyPathEgressResult(ctx, fixture.path.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TaskID == nil || *stored.TaskID != previousTaskID {
		t.Fatalf("stale topology result replaced current state: %#v", stored)
	}
}

func TestParsePublicEgressIPRejectsNonPublicAddresses(t *testing.T) {
	for _, raw := range []string{"", "not-an-ip", "127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "198.18.0.1", "2001:db8::1"} {
		if got, err := parsePublicEgressIP(raw); err == nil {
			t.Fatalf("parsePublicEgressIP(%q) = %q, want error", raw, got)
		}
	}
	if got, err := parsePublicEgressIP("::ffff:8.8.8.8"); err != nil || got != "8.8.8.8" {
		t.Fatalf("public mapped IPv4 = %q, %v", got, err)
	}
}
