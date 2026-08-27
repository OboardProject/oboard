package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestTopologyWriteRejectsInvalidStepBeforeMutation(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "entry", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	input, _ := json.Marshal(map[string]any{
		"path":  map[string]any{"kind": "chain", "name_mode": "auto", "name_template": []any{}, "inbound_id": inbound.ID, "exit_region_mode": "auto", "enabled": true},
		"steps": []map[string]any{{"node_type": "server_inbound", "transport_mode": "singbox", "server_id": node.ID}},
	})
	revisions, err := server.topologyWriteRevisions(ctx, principal, topologyWriteOperation{Path: model.ProxyPath{InboundID: inbound.ID}, Steps: []model.ProxyPathStep{{ServerID: &node.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(revisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{
		IdempotencyKey: "invalid-loop", BaseRevisions: base,
		Operations: []automation.OperationRequest{{Capability: "topology.write", Input: input}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err == nil {
		t.Fatal("looping topology operation passed dry-run validation")
	}
	paths, err := db.ListProxyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("rejected topology validation left persisted paths: %#v", paths)
	}
}

func TestTopologyWriteCommitsPathAndStepsAtomicallyThroughChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "topology-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "55555555-5555-4555-8555-555555555555", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	entry := &model.Server{Name: "entry", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	target := &model.Server{Name: "target", EntryAddress: "198.51.100.8", PublicIPv4: "198.51.100.8", ListenIP: "0.0.0.0", PortRangeStart: 11001, PortRangeEnd: 12000, Status: model.ServerOnline}
	for _, item := range []*model.Server{entry, target} {
		if err := db.CreateServer(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	root := &model.Inbound{ServerID: entry.ID, Name: "root", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	targetInbound := &model.Inbound{ServerID: target.ID, Name: "target", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true}
	for _, item := range []*model.Inbound{root, targetInbound} {
		if err := db.CreateInbound(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	input, _ := json.Marshal(map[string]any{
		"path":  map[string]any{"kind": "chain", "name_mode": "auto", "name_template": []any{}, "inbound_id": root.ID, "exit_region_mode": "auto", "enabled": true},
		"steps": []map[string]any{{"node_type": "server_inbound", "transport_mode": "singbox", "inbound_id": targetInbound.ID}},
	})
	draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "topology.write", Input: input}}})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "atomic-topology", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "topology.write", Input: input}}})
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
	paths, err := db.ListProxyPaths(ctx)
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%#v err=%v", paths, err)
	}
	steps, err := db.ListProxyPathStepsForPath(ctx, paths[0].ID)
	if err != nil || len(steps) != 1 || steps[0].InboundID == nil || *steps[0].InboundID != targetInbound.ID {
		t.Fatalf("steps=%#v err=%v", steps, err)
	}

	directInput, _ := json.Marshal(map[string]any{
		"path":         map[string]any{"kind": "direct", "name_mode": "auto", "name_template": []any{}, "inbound_id": root.ID, "exit_region_mode": "auto", "enabled": true},
		"steps":        []any{},
		"routing_rule": map[string]any{"name": "atomic-root-rule", "priority": 100, "match_json": `{"domain_suffix":["direct.example"]}`, "action": "direct", "enabled": true},
	})
	directDraft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "topology.write", Input: directInput}}})
	if err != nil {
		t.Fatal(err)
	}
	directBase, _ := json.Marshal(directDraft.ExpectedRevisions)
	directChangeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "atomic-direct-rule", BaseRevisions: directBase, Operations: []automation.OperationRequest{{Capability: "topology.write", Input: directInput}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, directChangeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, directChangeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	directApplied, err := server.automation.Apply(ctx, principal, directChangeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	var directResult struct {
		Operations []any `json:"operations"`
	}
	if err := json.Unmarshal(directApplied.Result, &directResult); err != nil || len(directResult.Operations) != 1 {
		t.Fatalf("topology result=%s err=%v", directApplied.Result, err)
	}
	assertCapabilityOutputSchema(t, server, "topology.write", directResult.Operations[0])
	rules, err := db.ListRoutingRules(ctx)
	if err != nil || len(rules) != 1 || rules[0].ProxyPathID == nil || rules[0].Name != "atomic-root-rule" {
		t.Fatalf("atomic topology routing rule=%#v err=%v", rules, err)
	}
	directPath, err := db.GetProxyPath(ctx, *rules[0].ProxyPathID)
	if err != nil || !directPath.Enabled || directPath.Kind != model.ProxyPathKindDirect {
		t.Fatalf("atomic direct path=%#v err=%v", directPath, err)
	}
}

func TestInboundCreateCapabilityAppliesThroughChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "entry", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	input := json.RawMessage(`{"inbound":{"server_id":1,"name":"MCP VLESS","kind":"vless-tcp","listen_ip":"0.0.0.0","port":443,"config_json":"{}","enabled":true}}`)
	draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "inbounds.create", Input: input}}})
	if err != nil {
		t.Fatalf("validate inbound draft: %v", err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "create-inbound", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "inbounds.create", Input: input}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved in test"); err != nil {
		t.Fatal(err)
	}
	applied, err := server.automation.Apply(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := db.ListInbounds(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbounds=%#v err=%v", items, err)
	}
	if items[0].Name != "MCP VLESS" || items[0].ServerID != node.ID || !items[0].Enabled {
		t.Fatalf("unexpected inbound: %#v", items[0])
	}
	if strings.Contains(string(applied.Result), "config_json") {
		t.Fatalf("changeset result exposed advanced inbound config: %s", applied.Result)
	}
}

func TestInboundCreateManagedCertificateAcceptsDNSDomainWithoutReadyCertificate(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "OC", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	input := json.RawMessage(`{"inbound":{"server_id":1,"name":"OC HY2","kind":"hy2-tls","listen_ip":"0.0.0.0","port":443,"dns_domain":"oc.example.com","certificate_mode":"auto","enabled":true}}`)
	if _, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "inbounds.create", Input: input}}}); err != nil {
		t.Fatalf("managed certificate create should accept dns_domain without a ready certificate: %v", err)
	}
}

func TestProxyPathEditCapabilitiesApplyThroughChangesets(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	entry := &model.Server{Name: "entry", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	exit := &model.Server{Name: "exit", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, exit); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Kind: model.ProxyPathKindChain, InboundID: inbound.ID, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "test-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	apply := func(key, capabilityName string, input json.RawMessage) {
		t.Helper()
		draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: capabilityName, Input: input}}})
		if err != nil {
			t.Fatalf("validate %s: %v", capabilityName, err)
		}
		base, _ := json.Marshal(draft.ExpectedRevisions)
		changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: key, BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: capabilityName, Input: input}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved in test"); err != nil {
			t.Fatal(err)
		}
		if _, err := server.automation.Apply(ctx, principal, changeset.ID); err != nil {
			t.Fatal(err)
		}
	}
	stepInput, _ := json.Marshal(map[string]any{"step": map[string]any{"path_id": path.ID, "position": 1, "node_type": "server_inbound", "transport_mode": "singbox", "server_id": exit.ID, "chain_protocol": "shadowsocks"}})
	apply("create-path-step", "proxy_path_steps.create", stepInput)
	steps, err := db.ListProxyPathStepsForPath(ctx, path.ID)
	if err != nil || len(steps) != 1 || steps[0].ServerID == nil || *steps[0].ServerID != exit.ID {
		t.Fatalf("steps=%#v err=%v", steps, err)
	}
	pathInput, _ := json.Marshal(map[string]any{"path_id": path.ID, "changes": map[string]any{"exit_region_mode": "manual", "exit_region_code": "US", "enabled": true}})
	apply("update-path", "proxy_paths.update", pathInput)
	stored, err := db.GetProxyPath(ctx, path.ID)
	if err != nil || stored.ExitRegionMode != "manual" || stored.ExitRegionCode != "US" {
		t.Fatalf("path=%#v err=%v", stored, err)
	}
	directInput, _ := json.Marshal(map[string]any{"inbound_id": inbound.ID})
	apply("create-direct-path", "proxy_paths.create_direct", directInput)
	paths, err := db.ListProxyPaths(ctx)
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths after direct branch=%#v err=%v", paths, err)
	}
	var directPathID int64
	for _, candidate := range paths {
		if candidate.Kind == model.ProxyPathKindDirect {
			directPathID = candidate.ID
			break
		}
	}
	deleteDirectInput, _ := json.Marshal(map[string]any{"path_id": directPathID, "confirm": true})
	apply("delete-direct-path", "proxy_paths.delete", deleteDirectInput)
	paths, err = db.ListProxyPaths(ctx)
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths after deleting direct branch=%#v err=%v", paths, err)
	}
	rule := &model.RoutingRule{ServerID: entry.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &path.ID, MatchSource: model.RoutingMatchSourceInline, Name: "keep-root-stage", MatchJSON: `{}`, Action: model.RouteActionDirect, Enabled: true}
	if err := db.CreateRoutingRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	truncateInput, _ := json.Marshal(map[string]any{"path_id": path.ID, "step_id": steps[0].ID, "confirm": true})
	apply("truncate-path", "proxy_path_steps.truncate", truncateInput)
	paths, err = db.ListProxyPaths(ctx)
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths after truncate=%#v err=%v", paths, err)
	}
	retained, err := db.GetProxyPath(ctx, path.ID)
	if err != nil || retained.Kind != model.ProxyPathKindDirect {
		t.Fatalf("routing path after truncate=%#v err=%v", retained, err)
	}
	if _, err := db.GetRoutingRule(ctx, rule.ID); err != nil {
		t.Fatalf("root routing rule disappeared after automation truncate: %v", err)
	}
	deleteInboundInput, _ := json.Marshal(map[string]any{"inbound_id": inbound.ID, "confirm": true})
	apply("delete-inbound", "inbounds.delete", deleteInboundInput)
	inbounds, err := db.ListInbounds(ctx)
	if err != nil || len(inbounds) != 0 {
		t.Fatalf("inbounds after delete=%#v err=%v", inbounds, err)
	}
	paths, err = db.ListProxyPaths(ctx)
	if err != nil || len(paths) != 0 {
		t.Fatalf("paths after inbound delete=%#v err=%v", paths, err)
	}
}

func TestProxyPathPlannerProducesValidTopologyChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	entry := &model.Server{Name: "entry", RegionMode: "manual", RegionCode: "JP", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	exit := &model.Server{Name: "exit", RegionMode: "manual", RegionCode: "US", PublicIPv4: "1.1.1.1", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, exit); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	plan, err := server.application.PlanProxyPath(ctx, principal, json.RawMessage(`{"entry_server_id":1,"exit_region":"US","max_hops":3}`))
	if err != nil || !plan.Valid || len(plan.Candidates) == 0 {
		t.Fatalf("proxy path plan=%#v err=%v", plan, err)
	}
	rawSuggested, _ := json.Marshal(plan.SuggestedChangeset)
	var suggested struct {
		BaseRevisions json.RawMessage             `json:"base_revisions"`
		Operation     automation.OperationRequest `json:"operation"`
	}
	if err := json.Unmarshal(rawSuggested, &suggested); err != nil {
		t.Fatal(err)
	}
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{
		IdempotencyKey: "planned-path", BaseRevisions: suggested.BaseRevisions, Operations: []automation.OperationRequest{suggested.Operation},
	})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := server.automation.Validate(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatalf("planned topology validation failed: %v", err)
	}
	if validated.Status != model.ChangesetAwaitingApproval {
		t.Fatalf("planned topology validation status=%v err=%v evidence=%s", validated.Status, err, validated.Validation)
	}
}

func TestDeploymentOperationReturnsOnlyPublicSummary(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "deploy", AgentID: "agent-test", AgentTokenHash: security.HashSecret("agent-secret"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	result, err := server.runDeploymentOperation(ctx, principal, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	text := string(encoded)
	for _, forbidden := range []string{"payload_json", "nonce", "signature", "agent-secret", "chain_secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("deployment result exposed %q: %s", forbidden, text)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["config_version"] == nil || fields["task_ids"] == nil || fields["summary"] == nil {
		t.Fatalf("unexpected deployment result: %#v", fields)
	}
}

func TestDeploymentOperationChecksCompleteScopeBeforeMutation(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	allowed := &model.Server{Name: "allowed", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	denied := &model.Server{Name: "denied", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, allowed); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, denied); err != nil {
		t.Fatal(err)
	}
	filter, _ := json.Marshal(application.ResourceFilter{Servers: &application.ResourceSelection{Mode: "selected", IDs: []int64{allowed.ID}}})
	principal := application.Principal{ID: "limited", ResourceFilter: filter}
	if _, err := server.runDeploymentOperation(ctx, principal, 0); err == nil || !strings.Contains(err.Error(), "authorized resource boundary") {
		t.Fatalf("full deployment boundary error=%v", err)
	}
	tasks, err := db.ListTasks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("rejected deployment created tasks: %#v", tasks)
	}
}

func TestServerOnboardingEnrollmentTokenIsReturnedOnlyOnce(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	plan, err := server.application.PlanServerOnboarding(ctx, principal, json.RawMessage(`{"name":"new-node","region_code":"JP","ip_stack":"auto"}`))
	if err != nil {
		t.Fatal(err)
	}
	rawSuggested, _ := json.Marshal(plan.SuggestedChangeset)
	var suggested struct {
		BaseRevisions json.RawMessage             `json:"base_revisions"`
		Operation     automation.OperationRequest `json:"operation"`
	}
	if err := json.Unmarshal(rawSuggested, &suggested); err != nil {
		t.Fatal(err)
	}
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "onboard-once", BaseRevisions: suggested.BaseRevisions, Operations: []automation.OperationRequest{suggested.Operation}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	applied, err := server.automation.Apply(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied.Result), `"enrollment_token"`) {
		t.Fatalf("initial apply response omitted enrollment token: %s", applied.Result)
	}
	persisted, err := server.automation.Get(ctx, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(persisted)
	if strings.Contains(string(encoded), `"enrollment_token":`) {
		t.Fatalf("persisted Changeset retained one-time token: %s", encoded)
	}
}

func TestAutomationAdminCanDisableAndRotateCredentials(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	request(t, handler, http.MethodPost, "/api/v1/api-principals", token, map[string]any{
		"name": "Future grant", "scopes": []string{"future:write"}, "resource_filter": map[string]any{},
	}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v1/api-principals", token, map[string]any{
		"name": "Typo filter", "scopes": []string{"inventory:read"}, "resource_filter": map[string]any{"servers_ids": []int64{1}},
	}, http.StatusBadRequest)

	created := request(t, handler, http.MethodPost, "/api/v1/api-principals", token, map[string]any{
		"name": "Codex", "scopes": []string{"inventory:read"}, "resource_filter": map[string]any{}, "allowed_cidrs": []string{"192.0.2.0/24"},
	}, http.StatusCreated)["data"].(map[string]any)
	principalID := created["id"].(string)
	first := request(t, handler, http.MethodPost, "/api/v1/api-principals/"+principalID+"/tokens", token, map[string]any{}, http.StatusCreated)["data"].(map[string]any)
	second := request(t, handler, http.MethodPost, "/api/v1/api-principals/"+principalID+"/tokens", token, map[string]any{}, http.StatusCreated)["data"].(map[string]any)
	if first["token"] == second["token"] {
		t.Fatal("token rotation returned the same plaintext token")
	}
	machineToken := second["token"].(string)
	request(t, handler, http.MethodGet, "/api/v1/capabilities", machineToken, nil, http.StatusOK)
	audits, err := db.ListToolCallAudits(context.Background(), principalID, 10)
	if err != nil || len(audits) == 0 || audits[0].Capability != "http.get:/api/v1/capabilities" {
		t.Fatalf("direct API access audit=%#v err=%v", audits, err)
	}
	firstInfo := first["token_info"].(map[string]any)
	request(t, handler, http.MethodDelete, "/api/v1/api-principals/"+principalID+"/tokens/"+firstInfo["id"].(string), token, nil, http.StatusOK)
	request(t, handler, http.MethodPatch, "/api/v1/api-principals/"+principalID, token, map[string]any{"enabled": false}, http.StatusOK)
	storedPrincipal, err := db.GetAPIPrincipal(context.Background(), principalID)
	if err != nil || storedPrincipal.Enabled {
		t.Fatalf("service account disable failed: %#v err=%v", storedPrincipal, err)
	}
	request(t, handler, http.MethodDelete, "/api/v1/api-principals/"+principalID, token, nil, http.StatusOK)
	if storedPrincipal, err := db.GetAPIPrincipal(context.Background(), principalID); err == nil {
		t.Fatalf("service account delete failed: %#v", storedPrincipal)
	}
	request(t, handler, http.MethodGet, "/api/v1/capabilities", machineToken, nil, http.StatusUnauthorized)

	provider := request(t, handler, http.MethodPost, "/api/v1/ai/providers", token, map[string]any{
		"name": "local", "base_url": "http://127.0.0.1:11434/v1", "model": "test", "api_key": "first-key", "enabled": true,
	}, http.StatusCreated)["data"].(map[string]any)
	providerID := provider["id"].(string)
	endpointID := provider["endpoints"].([]any)[0].(map[string]any)["id"].(string)
	request(t, handler, http.MethodPatch, "/api/v1/ai/providers/"+providerID+"/endpoints/"+endpointID, token, map[string]any{"api_key": "second-key"}, http.StatusOK)
	request(t, handler, http.MethodPatch, "/api/v1/ai/providers/"+providerID, token, map[string]any{"enabled": false}, http.StatusOK)
	storedProvider, err := db.GetAIProvider(context.Background(), providerID)
	if err != nil || storedProvider.Enabled {
		t.Fatalf("AI Provider disable failed: %#v err=%v", storedProvider, err)
	}
	storedEndpoint, err := db.GetAIProviderEndpoint(context.Background(), providerID, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := security.DecryptSecret("test-secret", "ai-provider-endpoint-credential:"+endpointID, storedEndpoint.CredentialEncrypted)
	if err != nil || plain != "second-key" {
		t.Fatalf("AI Provider credential rotation failed: value=%q err=%v", plain, err)
	}
}

func openControllerAutomationTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "automation-controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
