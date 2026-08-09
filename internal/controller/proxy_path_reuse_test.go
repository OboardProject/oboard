package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type proxyPathReuseFixture struct {
	db           *store.Store
	server       *Server
	servers      map[string]model.Server
	root         model.Inbound
	secondRoot   model.Inbound
	target       model.Inbound
	sourcePath   model.ProxyPath
	sourceStep   model.ProxyPathStep
	chainBranch  model.ProxyPath
	chainStep    model.ProxyPathStep
	directBranch model.ProxyPath
}

func newProxyPathReuseFixture(t *testing.T) proxyPathReuseFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "proxy-path-reuse.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	servers := map[string]model.Server{}
	for index, name := range []string{"A", "B", "C", "D"} {
		item := model.Server{
			Name: name, EntryAddress: "203.0.113." + string(rune('1'+index)), PublicIPv4: "203.0.113." + string(rune('1'+index)),
			ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000 + index*1000, PortRangeEnd: 30999 + index*1000, Status: model.ServerOnline,
		}
		if err := db.CreateServer(ctx, &item); err != nil {
			t.Fatal(err)
		}
		servers[name] = item
	}
	createInbound := func(server model.Server, name string, protocol model.Protocol, port int, config string, enabled bool) model.Inbound {
		inbound := model.Inbound{ServerID: server.ID, Name: name, Protocol: protocol, ListenIP: "0.0.0.0", Port: port, ConfigJSON: config, Enabled: enabled}
		if err := db.CreateInbound(ctx, &inbound); err != nil {
			t.Fatal(err)
		}
		return inbound
	}
	root := createInbound(servers["A"], "root", model.ProtocolVLESS, 443, `{}`, true)
	secondRoot := createInbound(servers["A"], "root-two", model.ProtocolAnyTLS, 8443, `{"tls":{"enabled":true}}`, true)
	target := createInbound(servers["B"], "reusable", model.ProtocolVLESS, 9443, `{}`, true)
	createInbound(servers["B"], "single-ss", model.ProtocolSS, 9444, `{"method":"aes-128-gcm"}`, true)
	createInbound(servers["B"], "disabled-hy2", model.ProtocolHY2, 9445, `{"tls":{"enabled":true}}`, false)
	createInbound(servers["B"], "ssh", model.ProtocolSSH, 9446, `{}`, true)

	createPath := func(path model.ProxyPath) model.ProxyPath {
		if err := db.CreateProxyPath(ctx, &path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	createStep := func(step model.ProxyPathStep) model.ProxyPathStep {
		if err := db.CreateProxyPathStep(ctx, &step); err != nil {
			t.Fatal(err)
		}
		return step
	}
	sourcePath := createPath(model.ProxyPath{Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, InboundID: root.ID, ExitRegionMode: "auto", Secret: "source-secret", Enabled: true})
	dID := servers["D"].ID
	sourceStep := createStep(model.ProxyPathStep{PathID: sourcePath.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &dID, ConfigJSON: `{"chain_protocol":"shadowsocks","chain_method":"2022-blake3-aes-128-gcm"}`})
	chainBranch := createPath(model.ProxyPath{Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameCustom, NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameLiteral, Value: "old target name"}}, InboundID: target.ID, ExitRegionMode: "manual", ExitRegionCode: "US", Secret: "target-chain", Enabled: true})
	cID := servers["C"].ID
	chainStep := createStep(model.ProxyPathStep{PathID: chainBranch.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &cID, ConfigJSON: `{"chain_protocol":"mieru"}`})
	directBranch := createPath(model.ProxyPath{Kind: model.ProxyPathKindDirect, NameMode: model.ProxyPathNameCustom, NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameLiteral, Value: "old direct name"}}, InboundID: target.ID, ExitRegionMode: "manual", ExitRegionCode: "JP", Secret: "target-direct", Enabled: true})
	return proxyPathReuseFixture{db: db, server: newTestServer(db, "test-secret", ""), servers: servers, root: root, secondRoot: secondRoot, target: target, sourcePath: sourcePath, sourceStep: sourceStep, chainBranch: chainBranch, chainStep: chainStep, directBranch: directBranch}
}

func TestProxyPathReusePreviewFiltersTargetsAndDefaultsToNoBranchCopy(t *testing.T) {
	fixture := newProxyPathReuseFixture(t)
	ctx := context.Background()
	request := proxyPathReuseRequest{Sources: []proxyPathReuseSource{{InboundID: fixture.secondRoot.ID}}, TargetServerID: fixture.servers["B"].ID, TargetKind: "existing", TargetInboundID: fixture.target.ID}
	preview, err := fixture.server.buildProxyPathReusePreview(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if preview["valid"] != true || int(preview["result_path_count"].(int)) != 1 {
		t.Fatalf("default preview = %#v", preview)
	}
	targets := preview["target_options"].([]proxyPathReuseTargetOption)
	generated, existing := 0, 0
	for _, option := range targets {
		if option.Kind == "generated" {
			generated++
		}
		if option.Kind == "existing" {
			existing++
			if option.InboundID != fixture.target.ID || !option.Eligible {
				t.Fatalf("unexpected existing target: %#v", option)
			}
		}
	}
	if generated != 6 || existing != 1 {
		t.Fatalf("target groups generated=%d existing=%d targets=%#v", generated, existing, targets)
	}
	branches := preview["branch_options"].([]proxyPathReuseBranchOption)
	if len(branches) != 2 {
		t.Fatalf("branch options = %#v", branches)
	}
	plan, err := fixture.server.planProxyPathReuse(ctx, request, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResultPathCount != 1 || len(plan.Writes[0].Steps) != 1 || plan.Writes[0].Path.ExitRegionMode != "auto" {
		t.Fatalf("default no-copy plan = %#v", plan)
	}
}

func TestSocks5InboundAndGeneratedChainAreAccepted(t *testing.T) {
	inbound := model.Inbound{ServerID: 1, Name: "SOCKS5", Protocol: model.ProtocolSocks, ListenIP: "0.0.0.0", Port: 1080, ConfigJSON: `{}`, Enabled: true}
	inbound = normalizeInbound(inbound)
	if err := validateInbound(inbound); err != nil {
		t.Fatalf("SOCKS5 inbound rejected: %v", err)
	}
	step := model.ProxyPathStep{ConfigJSON: `{"chain_protocol":"socks"}`}
	cfg, err := core.ParseProxyPathChainConfig(step.ConfigJSON)
	if err != nil || cfg.Protocol != model.ProtocolSocks {
		t.Fatalf("SOCKS5 generated chain rejected: config=%#v err=%v", cfg, err)
	}
}

func TestProxyPathReuseCopiesSingleBranchAsIndependentSnapshot(t *testing.T) {
	fixture := newProxyPathReuseFixture(t)
	request := proxyPathReuseRequest{
		Sources: []proxyPathReuseSource{{InboundID: fixture.secondRoot.ID}}, TargetServerID: fixture.servers["B"].ID,
		TargetKind: "existing", TargetInboundID: fixture.target.ID, CopyMode: "single", BranchPathID: fixture.chainBranch.ID,
	}
	plan, err := fixture.server.planProxyPathReuse(context.Background(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Writes) != 1 {
		t.Fatalf("writes = %#v", plan.Writes)
	}
	write := plan.Writes[0]
	if write.Path.Kind != model.ProxyPathKindChain || write.Path.ExitRegionMode != "manual" || write.Path.ExitRegionCode != "US" || write.Path.NameMode != model.ProxyPathNameAuto {
		t.Fatalf("copied path metadata = %#v", write.Path)
	}
	if len(write.Steps) != 2 || write.Steps[0].InboundID == nil || *write.Steps[0].InboundID != fixture.target.ID || write.Steps[1].ServerID == nil || *write.Steps[1].ServerID != fixture.servers["C"].ID {
		t.Fatalf("copied steps = %#v", write.Steps)
	}
	if write.Steps[1].ID == fixture.chainStep.ID || write.Steps[1].PathID == fixture.chainStep.PathID {
		t.Fatalf("branch was not projected as an independent snapshot: %#v", write.Steps[1])
	}
}

func TestProxyPathReuseAllExpandsSharedSourcesAndMapsDirectBranch(t *testing.T) {
	fixture := newProxyPathReuseFixture(t)
	request := proxyPathReuseRequest{
		Sources: []proxyPathReuseSource{{StepID: fixture.sourceStep.ID}, {InboundID: fixture.secondRoot.ID}}, TargetServerID: fixture.servers["B"].ID,
		TargetKind: "existing", TargetInboundID: fixture.target.ID, CopyMode: "all",
	}
	plan, err := fixture.server.applyProxyPathReuseOperation(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceCount != 2 || plan.ResultPathCount != 4 || len(plan.Writes) != 4 {
		t.Fatalf("shared expansion = sources=%d results=%d writes=%#v", plan.SourceCount, plan.ResultPathCount, plan.Writes)
	}
	if plan.Writes[0].ExistingPathID != fixture.sourcePath.ID {
		t.Fatalf("first source path was not extended in place: %#v", plan.Writes[0])
	}
	for _, write := range plan.Writes {
		if write.Path.ID <= 0 {
			t.Fatalf("store did not return a real path id: %#v", write.Path)
		}
		for _, step := range write.Steps {
			if step.ID <= 0 || step.PathID != write.Path.ID {
				t.Fatalf("store did not remap cloned step: path=%#v step=%#v", write.Path, step)
			}
		}
		if write.Path.Kind == model.ProxyPathKindDirect {
			if write.Path.BranchSourceStepID == nil || write.BranchSourcePosition == 0 {
				t.Fatalf("direct branch source was not mapped: %#v", write)
			}
			matched := false
			for _, step := range write.Steps {
				if step.ID == *write.Path.BranchSourceStepID && step.Position == write.BranchSourcePosition {
					matched = step.InboundID != nil && *step.InboundID == fixture.target.ID
				}
			}
			if !matched {
				t.Fatalf("direct branch source does not reference the inserted target step: %#v", write)
			}
		}
	}
	storedSourceSteps, err := fixture.db.ListProxyPathStepsForPath(context.Background(), fixture.sourcePath.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedSourceSteps) != 3 {
		t.Fatalf("extended source step count = %d, want 3", len(storedSourceSteps))
	}
}

func TestProxyPathReuseBranchesFromMiddleStepWithoutChangingSourcePath(t *testing.T) {
	fixture := newProxyPathReuseFixture(t)
	cID := fixture.servers["C"].ID
	tail := model.ProxyPathStep{
		PathID: fixture.sourcePath.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound,
		TransportMode: model.ProxyPathTransportSingBox, ServerID: &cID,
		ConfigJSON: `{"chain_protocol":"shadowsocks","chain_method":"2022-blake3-aes-128-gcm"}`,
	}
	if err := fixture.db.CreateProxyPathStep(context.Background(), &tail); err != nil {
		t.Fatal(err)
	}

	request := proxyPathReuseRequest{
		Sources: []proxyPathReuseSource{{StepID: fixture.sourceStep.ID}}, TargetServerID: fixture.servers["B"].ID,
		TargetKind: "generated", ChainProtocol: model.ProtocolSS,
	}
	plan, err := fixture.server.applyProxyPathReuseOperation(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Writes) != 1 || plan.Writes[0].ExistingPathID != 0 || plan.Writes[0].Path.ID == fixture.sourcePath.ID {
		t.Fatalf("middle-step branch must create an independent path: %#v", plan.Writes)
	}

	sourceSteps, err := fixture.db.ListProxyPathStepsForPath(context.Background(), fixture.sourcePath.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceSteps) != 2 || sourceSteps[0].ID != fixture.sourceStep.ID || sourceSteps[1].ID != tail.ID {
		t.Fatalf("source path changed while branching: %#v", sourceSteps)
	}
	branchSteps, err := fixture.db.ListProxyPathStepsForPath(context.Background(), plan.Writes[0].Path.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(branchSteps) != 2 || branchSteps[0].ServerID == nil || *branchSteps[0].ServerID != fixture.servers["D"].ID || branchSteps[1].ServerID == nil || *branchSteps[1].ServerID != fixture.servers["B"].ID {
		t.Fatalf("middle-step branch = %#v, want D -> B", branchSteps)
	}
	if branchSteps[0].ID == fixture.sourceStep.ID || branchSteps[0].PathID == fixture.sourcePath.ID {
		t.Fatalf("source prefix was not cloned for the new branch: %#v", branchSteps[0])
	}
}

func TestProxyPathReuseRejectsInvalidSuffixAndServerLoop(t *testing.T) {
	t.Run("transparent suffix", func(t *testing.T) {
		fixture := newProxyPathReuseFixture(t)
		step := fixture.chainStep
		step.TransportMode = model.ProxyPathTransportPortForward
		step.ConfigJSON = `{}`
		if err := fixture.db.UpdateProxyPathStep(context.Background(), &step); err != nil {
			t.Fatal(err)
		}
		request := proxyPathReuseRequest{Sources: []proxyPathReuseSource{{InboundID: fixture.secondRoot.ID}}, TargetServerID: fixture.servers["B"].ID, TargetKind: "existing", TargetInboundID: fixture.target.ID, CopyMode: "single", BranchPathID: fixture.chainBranch.ID}
		if _, err := fixture.server.planProxyPathReuse(context.Background(), request, false); err == nil || !strings.Contains(err.Error(), "端口转发只能位于路径开头") {
			t.Fatalf("transparent suffix error = %v", err)
		}
		request.CopyMode, request.BranchPathID = "all", 0
		preview, err := fixture.server.buildProxyPathReusePreview(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		branches := preview["branch_options"].([]proxyPathReuseBranchOption)
		if preview["valid"] != false || len(branches) != 2 || branches[0].Eligible || !strings.Contains(branches[0].Reason, "端口转发只能位于路径开头") {
			t.Fatalf("invalid all-branch preview = %#v", preview)
		}
	})
	t.Run("server loop", func(t *testing.T) {
		fixture := newProxyPathReuseFixture(t)
		request := proxyPathReuseRequest{Sources: []proxyPathReuseSource{{StepID: fixture.sourceStep.ID}}, TargetServerID: fixture.servers["A"].ID, TargetKind: "generated", ChainProtocol: model.ProtocolVLESS}
		if _, err := fixture.server.planProxyPathReuse(context.Background(), request, false); err == nil || !strings.Contains(err.Error(), "不能重复经过") {
			t.Fatalf("server loop error = %v", err)
		}
	})
}

func TestGeneratedVLESSPreviewCountsSelectedHandshakeProfile(t *testing.T) {
	fixture := newProxyPathReuseFixture(t)
	ctx := context.Background()
	request := proxyPathReuseRequest{Sources: []proxyPathReuseSource{{InboundID: fixture.secondRoot.ID}}, TargetServerID: fixture.servers["B"].ID, TargetKind: "generated", ChainProtocol: model.ProtocolVLESS, RealityHandshakeServer: "example.com", RealityHandshakePort: 8443}
	if _, err := fixture.server.applyProxyPathReuseOperation(ctx, request, nil); err != nil {
		t.Fatal(err)
	}
	preview, err := fixture.server.buildProxyPathReusePreview(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	targets := preview["target_options"].([]proxyPathReuseTargetOption)
	for _, option := range targets {
		if option.Kind == "generated" && option.Protocol == model.ProtocolVLESS {
			if option.ActiveReuseCount != 1 {
				t.Fatalf("custom Reality reuse count = %d, want 1", option.ActiveReuseCount)
			}
			return
		}
	}
	t.Fatal("generated VLESS target missing")
}

func TestProxyPathReuseRequestJSONDefaults(t *testing.T) {
	var request proxyPathReuseRequest
	if err := json.Unmarshal([]byte(`{"sources":[{"inbound_id":1}],"target_server_id":2}`), &request); err != nil {
		t.Fatal(err)
	}
	if err := normalizeProxyPathReuseRequest(&request); err != nil {
		t.Fatal(err)
	}
	if request.TargetKind != "generated" || request.ChainProtocol != model.ProtocolSS || request.ChainMethod != core.DefaultProxyPathChainMethod || request.CopyMode != "none" {
		t.Fatalf("defaults = %#v", request)
	}
}

func TestProxyPathReuseUIRouteRespectsBasePath(t *testing.T) {
	fixture := newProxyPathReuseFixture(t)
	if err := fixture.db.SetSetting(context.Background(), settingControllerBasePath, "/panel"); err != nil {
		t.Fatal(err)
	}
	handler := New(fixture.db, "test-secret", "", "/panel", nil).Handler()
	request(t, handler, http.MethodPost, "/panel/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/panel/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	body := map[string]any{"sources": []map[string]any{{"inbound_id": fixture.secondRoot.ID}}, "target_server_id": fixture.servers["B"].ID, "target_kind": "generated"}
	preview := request(t, handler, http.MethodPost, "/panel/api/v2/ui/proxy-paths/reuse-preview", token, body, http.StatusOK)
	if preview["valid"] != true {
		t.Fatalf("base-path preview = %#v", preview)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v2/ui/proxy-paths/reuse-preview", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("route outside base path = %d, want 404", recorder.Code)
	}
}

func TestProxyPathReuseAutomationChangesetAndResourceAuthorization(t *testing.T) {
	t.Run("approved apply", func(t *testing.T) {
		fixture := newProxyPathReuseFixture(t)
		ctx := context.Background()
		user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
		if err := fixture.db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
		reuse := proxyPathReuseRequest{Sources: []proxyPathReuseSource{{InboundID: fixture.secondRoot.ID}}, TargetServerID: fixture.servers["B"].ID, TargetKind: "generated", ChainProtocol: model.ProtocolMieru}
		plan, err := fixture.server.planProxyPathReuse(ctx, reuse, false)
		if err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(reuse)
		base, _ := json.Marshal(map[string]string{"routing_topology": plan.Revision})
		changeset, err := fixture.server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "reuse-inbound", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "topology.reuse_inbound", Input: input}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.server.automation.Validate(ctx, principal, changeset.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
			t.Fatal(err)
		}
		applied, err := fixture.server.automation.Apply(ctx, principal, changeset.ID)
		if err != nil || applied.Status != model.ChangesetSucceeded {
			t.Fatalf("applied changeset=%#v err=%v", applied, err)
		}
	})

	t.Run("resource filter", func(t *testing.T) {
		fixture := newProxyPathReuseFixture(t)
		ctx := context.Background()
		user := &model.User{Username: "restricted", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
		if err := fixture.db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
		principal.ResourceFilter = json.RawMessage(`{"server_ids":[` + itoa(fixture.servers["A"].ID) + `]}`)
		reuse := proxyPathReuseRequest{Sources: []proxyPathReuseSource{{InboundID: fixture.secondRoot.ID}}, TargetServerID: fixture.servers["B"].ID, TargetKind: "generated"}
		plan, err := fixture.server.planProxyPathReuse(ctx, reuse, false)
		if err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(reuse)
		base, _ := json.Marshal(map[string]string{"routing_topology": plan.Revision})
		changeset, err := fixture.server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "restricted-reuse", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "topology.reuse_inbound", Input: input}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.server.automation.Validate(ctx, principal, changeset.ID); err == nil || !strings.Contains(err.Error(), "unauthorized server") {
			t.Fatalf("resource-filter validation error = %v", err)
		}
	})
}
