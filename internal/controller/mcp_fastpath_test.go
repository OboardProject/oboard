package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func TestMCPRecipeRouting(t *testing.T) {
	s := &Server{}
	tests := []struct {
		name, goal, intent, want string
		ambiguous, fallback      bool
	}{
		{name: "explicit", intent: "proxy_path.manage", want: "proxy_path.manage"},
		{name: "explicit inbound", intent: "inbound.create", want: "inbound.create"},
		{name: "chinese", goal: "新增东京服务器并开启 BBR", want: "server.onboard"},
		{name: "chinese inbound", goal: "在东京节点创建 VLESS 入站", want: "inbound.create"},
		{name: "english", goal: "deploy all configuration changes", want: "deployment.apply"},
		{name: "structured proxy ref", goal: "", want: "proxy_path.manage"},
		{name: "structured server settings", goal: "", want: "server.manage"},
		{name: "structured server expiry", goal: "", want: "server.manage"},
		{name: "ambiguous", goal: "apply server", ambiguous: true},
		{name: "ambiguous server ref", goal: "", ambiguous: true},
		{name: "no match", goal: "explain the weather", fallback: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := mcpTaskInput{Goal: test.goal, Intent: test.intent}
			switch test.name {
			case "structured proxy ref":
				input.TargetRefs = []string{"inbound:7"}
			case "structured server settings":
				input.TargetRefs, input.Params = []string{"server:7"}, map[string]any{"bbr_enabled": true}
			case "structured server expiry":
				input.TargetRefs, input.Params = []string{"server:7"}, map[string]any{"expires_at": "2027-01-02T03:04:05Z", "renewal_cycle": "quarterly"}
			case "explicit inbound":
				input.TargetRefs, input.Params = []string{"server:7"}, map[string]any{"protocol": "vless", "port": 443}
			case "ambiguous server ref":
				input.TargetRefs = []string{"server:7"}
			}
			recipe, candidates, ok := s.matchMCPRecipe(input)
			if test.fallback {
				if ok || len(candidates) != 0 {
					t.Fatalf("recipe=%#v candidates=%#v ok=%v", recipe, candidates, ok)
				}
				return
			}
			if test.ambiguous {
				if ok || len(candidates) < 2 {
					t.Fatalf("recipe=%#v candidates=%#v ok=%v", recipe, candidates, ok)
				}
				return
			}
			if !ok || recipe.ID != test.want {
				t.Fatalf("recipe=%#v candidates=%#v ok=%v", recipe, candidates, ok)
			}
		})
	}
}

func TestMCPPreparedPlanHashIsCanonicalAndIdentityBound(t *testing.T) {
	left := mcpPreparedPlanHash("p", "g", "server.manage", "1", []mcpOperationRef{{Capability: "servers.update", Input: map[string]any{"changes": map[string]any{"bbr_enabled": true, "ip_stack": "prefer_ipv6"}, "server_id": 1}}}, map[string]string{"server:1": "rev", "routing": "topology"})
	right := mcpPreparedPlanHash("p", "g", "server.manage", "1", []mcpOperationRef{{Capability: "servers.update", Input: map[string]any{"server_id": 1, "changes": map[string]any{"ip_stack": "prefer_ipv6", "bbr_enabled": true}}}}, map[string]string{"routing": "topology", "server:1": "rev"})
	if left != right {
		t.Fatalf("canonical hashes differ: %s != %s", left, right)
	}
	if left == mcpPreparedPlanHash("other", "g", "server.manage", "1", []mcpOperationRef{{Capability: "servers.update", Input: map[string]any{"server_id": 1, "changes": map[string]any{"ip_stack": "prefer_ipv6", "bbr_enabled": true}}}}, map[string]string{"routing": "topology", "server:1": "rev"}) {
		t.Fatal("plan hash is not bound to principal identity")
	}
}

func TestMCPServerOnboardingUsesControllerDefaults(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()

	request, err := decodeServerOnboardingOperation(json.RawMessage(`{"server":{"name":"Tokyo-01"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.applyServerOnboardingDefaults(ctx, json.RawMessage(`{"server":{"name":"Tokyo-01"}}`), &request); err != nil {
		t.Fatal(err)
	}
	if !request.Server.LatencyProbeEnabled || !request.Server.ConnectionAuditEnabled {
		t.Fatalf("missing controller defaults: latency_probe_enabled=%v connection_audit_enabled=%v", request.Server.LatencyProbeEnabled, request.Server.ConnectionAuditEnabled)
	}

	request, err = decodeServerOnboardingOperation(json.RawMessage(`{"server":{"name":"Tokyo-02","latency_probe_enabled":false,"connection_audit_enabled":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.applyServerOnboardingDefaults(ctx, json.RawMessage(`{"server":{"name":"Tokyo-02","latency_probe_enabled":false,"connection_audit_enabled":false}}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.Server.LatencyProbeEnabled || request.Server.ConnectionAuditEnabled {
		t.Fatalf("explicit false values were replaced: latency_probe_enabled=%v connection_audit_enabled=%v", request.Server.LatencyProbeEnabled, request.Server.ConnectionAuditEnabled)
	}

	prepared, err := s.prepareServerOnboardRecipe(ctx, application.Principal{}, mcpTaskInput{Params: map[string]any{
		"name":                           "Tokyo-03",
		"listen_ip":                      "0.0.0.0",
		"entry_ip_mode":                  "custom",
		"entry_address":                  "203.0.113.1",
		"region_mode":                    "manual",
		"region_code":                    "JP",
		"port_range_start":               12000,
		"port_range_end":                 13000,
		"internal_port_range_start":      40000,
		"internal_port_range_end":        45000,
		"connection_audit_enabled":       false,
		"time_correction_mode":           "auto",
		"offline_notify_enabled":         false,
		"offline_after_seconds":          120,
		"expires_at":                     "2027-01-02T03:04:05Z",
		"auto_renew_enabled":             true,
		"renewal_cycle":                  "quarterly",
		"expiry_notify_enabled":          false,
		"latency_probe_enabled":          true,
		"latency_probe_mode":             "icmp",
		"latency_probe_public_target":    "google",
		"latency_probe_interval_seconds": 90,
		"latency_probe_sample_count":     5,
		"latency_probe_regions":          []any{map[string]any{"province": "广东", "carrier": "中国电信"}, map[string]any{"province": "浙江", "carrier": "中国联通"}},
		"latency_probe_max_targets":      16,
	}})
	if err != nil || len(prepared.Operations) != 1 {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	serverInput := prepared.Operations[0].Input["server"].(map[string]any)
	if serverInput["listen_ip"] != "0.0.0.0" || serverInput["entry_ip_mode"] != "custom" || serverInput["entry_address"] != "203.0.113.1" || serverInput["region_mode"] != "manual" || serverInput["region_code"] != "JP" {
		t.Fatalf("server addressing settings were not forwarded: %#v", serverInput)
	}
	if serverInput["port_range_start"] != 12000 || serverInput["port_range_end"] != 13000 || serverInput["internal_port_range_start"] != 40000 || serverInput["internal_port_range_end"] != 45000 {
		t.Fatalf("port ranges were not forwarded: %#v", serverInput)
	}
	if serverInput["expires_at"] != "2027-01-02T03:04:05Z" || serverInput["auto_renew_enabled"] != true || serverInput["renewal_cycle"] != "quarterly" || serverInput["expiry_notify_enabled"] != false {
		t.Fatalf("expiry settings were not forwarded: %#v", serverInput)
	}
	if serverInput["connection_audit_enabled"] != false || serverInput["time_correction_mode"] != "auto" || serverInput["offline_notify_enabled"] != false || serverInput["offline_after_seconds"] != 120 {
		t.Fatalf("operational settings were not forwarded: %#v", serverInput)
	}
	if serverInput["latency_probe_mode"] != "icmp" || serverInput["latency_probe_public_target"] != "google" || serverInput["latency_probe_interval_seconds"] != 90 || serverInput["latency_probe_sample_count"] != 5 || serverInput["latency_probe_max_targets"] != 16 {
		t.Fatalf("latency settings were not forwarded: %#v", serverInput)
	}
	if regions, ok := serverInput["latency_probe_regions"].([]any); !ok || len(regions) != 2 {
		t.Fatalf("latency regions were not forwarded: %#v", serverInput["latency_probe_regions"])
	}
}

func TestMCPResourceResolver(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	for _, item := range []model.Server{
		{Name: "Tokyo-01", RegionMode: "manual", RegionCode: "JP", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline},
		{Name: "tokyo_01", RegionMode: "manual", RegionCode: "JP", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000, Status: model.ServerOnline},
	} {
		item := item
		if err := db.CreateServer(ctx, &item); err != nil {
			t.Fatal(err)
		}
	}
	principal := application.Principal{}
	byID, err := s.resolveServerRef(ctx, principal, "server:1")
	if err != nil || byID.Value == nil || byID.Value.ID != 1 {
		t.Fatalf("byID=%#v err=%v", byID, err)
	}
	ambiguous, err := s.resolveServerRef(ctx, principal, "server:Tokyo 01")
	if err != nil || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous=%#v err=%v", ambiguous, err)
	}
	if _, err := s.resolveServerRef(ctx, principal, "server:missing"); !errorsIs(err, sql.ErrNoRows) {
		t.Fatalf("missing error=%v", err)
	}
}

func errorsIs(err, target error) bool {
	return err != nil && (err == target || strings.Contains(err.Error(), target.Error()))
}

func fastPathCall(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func fastPathPreparedID(t *testing.T, envelope map[string]any) string {
	t.Helper()
	if envelope["status"] != "ready" {
		t.Fatalf("task status=%v envelope=%#v", envelope["status"], envelope)
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("task data=%#v", envelope["data"])
	}
	id, _ := data["prepared_id"].(string)
	if id == "" {
		t.Fatalf("prepared_id missing: %#v", envelope)
	}
	encoded, _ := json.Marshal(envelope)
	if strings.Contains(string(encoded), "operations") {
		t.Fatalf("prepared task leaked operations: %s", encoded)
	}
	return id
}

func TestMCPFastPathRecipesCommitThroughWorkflow(t *testing.T) {
	db, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	ctx := context.Background()
	entry := model.Server{Name: "Hong Kong", RegionMode: "manual", RegionCode: "HK", ListenIP: "0.0.0.0", PublicIPv4: "203.0.113.10", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	exit := model.Server{Name: "Singapore", RegionMode: "manual", RegionCode: "SG", ListenIP: "0.0.0.0", PublicIPv4: "203.0.113.20", PortRangeStart: 12000, PortRangeEnd: 13000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, &entry); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, &exit); err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: entry.ID, Name: "VLESS", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, &inbound); err != nil {
		t.Fatal(err)
	}
	external := model.ExternalOutbound{Name: "Los Angeles Imported", Protocol: model.ProtocolSocks, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "198.51.100.30", TargetPort: 1080, ConfigJSON: `{}`, RegionMode: "manual", RegionCode: "US", Enabled: true}
	if err := db.CreateExternalOutbound(ctx, &external); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args map[string]any
	}{
		{name: "server onboard", args: map[string]any{"intent": "server.onboard", "goal": "新增一台 Tokyo 服务器，IPv6 优先，打开 BBR"}},
		{name: "server manage", args: map[string]any{"intent": "server.manage", "goal": "把香港服务器调成 IPv6 优先", "target_refs": []any{fmt.Sprintf("server:%d", entry.ID)}, "params": map[string]any{"ip_stack": "prefer_ipv6"}}},
		{name: "inbound create", args: map[string]any{"intent": "inbound.create", "goal": "在新加坡创建 VLESS 入口", "target_refs": []any{fmt.Sprintf("server:%d", exit.ID)}, "params": map[string]any{"name": "Singapore VLESS", "protocol": "vless", "port": 8443}}},
		{name: "proxy path", args: map[string]any{"intent": "proxy_path.manage", "goal": "把香港 VLESS 入口通过新加坡", "target_refs": []any{fmt.Sprintf("inbound:%d", inbound.ID), fmt.Sprintf("server:%d", exit.ID)}}},
		{name: "proxy path wireguard", args: map[string]any{"intent": "proxy_path.manage", "goal": "把香港 VLESS 入口通过新加坡并使用 WireGuard", "target_refs": []any{fmt.Sprintf("inbound:%d", inbound.ID), fmt.Sprintf("server:%d", exit.ID)}, "params": map[string]any{"transport": "wireguard"}}},
		{name: "proxy path direct", args: map[string]any{"intent": "proxy_path.manage", "goal": "给香港 VLESS 增加 Direct Branch", "target_refs": []any{fmt.Sprintf("inbound:%d", inbound.ID)}}},
		{name: "proxy path imported", args: map[string]any{"intent": "proxy_path.manage", "goal": "把香港 VLESS 连接 Los Angeles Imported", "target_refs": []any{fmt.Sprintf("inbound:%d", inbound.ID), fmt.Sprintf("external_outbound:%d", external.ID)}}},
		{name: "deployment", args: map[string]any{"intent": "deployment.apply", "goal": "部署刚才的所有修改", "target_refs": []any{fmt.Sprintf("server:%d", entry.ID)}}},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			prepared := fastPathPreparedID(t, fastPathCall(t, session, "oboard_task", test.args))
			key := fmt.Sprintf("fastpath-integration-%02d", index)
			committed := fastPathCall(t, session, "oboard_commit_task", map[string]any{"prepared_id": prepared, "idempotency_key": key, "reason": test.name})
			workflowID, _ := committed["workflow_id"].(string)
			changesetID, _ := committed["changeset_id"].(string)
			if workflowID == "" || changesetID == "" {
				t.Fatalf("commit did not create Changeset/Workflow: %#v", committed)
			}
			if _, err := db.GetAutomationChangeset(ctx, changesetID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.GetAutomationWorkflow(ctx, workflowID); err != nil {
				t.Fatal(err)
			}
			workflow := fastPathCall(t, session, "oboard_get_workflow", map[string]any{"workflow_id": workflowID})
			workflowJSON, _ := json.Marshal(workflow)
			if strings.Contains(string(workflowJSON), `"steps"`) {
				t.Fatalf("compact workflow unexpectedly included steps: %s", workflowJSON)
			}
			retry := fastPathCall(t, session, "oboard_commit_task", map[string]any{"prepared_id": prepared, "idempotency_key": key, "reason": test.name})
			if retry["workflow_id"] != workflowID || retry["changeset_id"] != changesetID {
				t.Fatalf("idempotent retry diverged first=%#v retry=%#v", committed, retry)
			}
		})
	}
	metrics, err := db.ListMCPFastPathMetrics(ctx, 100)
	if err != nil || len(metrics) < len(cases)*4 {
		t.Fatalf("metrics=%d err=%v", len(metrics), err)
	}
}

func TestMCPServerOnboardRedeemedActionEnablesBBR(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()

	prepared := fastPathCall(t, session, "oboard_task", map[string]any{
		"intent": "server.onboard",
		"goal":   "新增一台 Tokyo 服务器，IPv6 优先，打开 BBR",
	})
	preparedJSON, _ := json.Marshal(prepared)
	if strings.Contains(string(preparedJSON), "OBOARD_ENROLL_TOKEN") {
		t.Fatalf("prepared summary leaked enrollment material: %s", preparedJSON)
	}

	committed := fastPathCall(t, session, "oboard_commit_task", map[string]any{
		"prepared_id":     fastPathPreparedID(t, prepared),
		"idempotency_key": "onboard-bbr-external-action",
	})
	if committed["status"] != "external_action_required" {
		t.Fatalf("onboarding commit did not reach its external action: %#v", committed)
	}
	workflowID, _ := committed["workflow_id"].(string)

	workflow := fastPathCall(t, session, "oboard_get_workflow", map[string]any{"workflow_id": workflowID})
	if workflow["status"] != "external_action_required" {
		t.Fatalf("workflow did not wait for external action: %#v", workflow)
	}
	nextAction, _ := workflow["next_action"].(map[string]any)
	actionID, _ := nextAction["action_id"].(string)
	if actionID == "" {
		t.Fatalf("workflow did not return an external action: %#v", workflow)
	}

	redeemed := fastPathCall(t, session, "oboard_redeem_external_action", map[string]any{"action_id": actionID})
	data, _ := redeemed["data"].(map[string]any)
	action, _ := data["action"].(map[string]any)
	environment, _ := action["environment"].(map[string]any)
	if got := environment["OBOARD_INSTALL_BBR"]; got != "1" {
		t.Fatalf("OBOARD_INSTALL_BBR=%#v action=%#v", got, action)
	}
}

func TestMCPFastPathContinuationAndStaleRevision(t *testing.T) {
	db, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	ctx := context.Background()
	needs := fastPathCall(t, session, "oboard_task", map[string]any{"intent": "server.onboard", "goal": "新增服务器"})
	if needs["status"] != "needs_input" {
		t.Fatalf("needs_input=%#v", needs)
	}
	continuationID := needs["data"].(map[string]any)["continuation_id"].(string)
	ready := fastPathCall(t, session, "oboard_task", map[string]any{"continuation_id": continuationID, "params": map[string]any{"server.name": "Tokyo-Continuation"}})
	fastPathPreparedID(t, ready)
	for _, name := range []string{"Tokyo-01", "Tokyo-02"} {
		candidate := model.Server{Name: name, RegionMode: "manual", RegionCode: "JP", ListenIP: "0.0.0.0", PortRangeStart: 16000, PortRangeEnd: 17000, Status: model.ServerOnline}
		if err := db.CreateServer(ctx, &candidate); err != nil {
			t.Fatal(err)
		}
	}
	choose := fastPathCall(t, session, "oboard_task", map[string]any{"intent": "server.manage", "goal": "修改东京服务器", "params": map[string]any{"bbr_enabled": true}})
	if choose["status"] != "choose_candidate" {
		t.Fatalf("choose_candidate=%#v", choose)
	}
	chooseData := choose["data"].(map[string]any)
	candidates := chooseData["candidates"].([]any)
	selected := candidates[0].(map[string]any)["ref"].(string)
	resumed := fastPathCall(t, session, "oboard_task", map[string]any{"continuation_id": chooseData["continuation_id"], "params": map[string]any{"target_server": selected}})
	fastPathPreparedID(t, resumed)

	server := model.Server{Name: "stale", ListenIP: "0.0.0.0", PortRangeStart: 14000, PortRangeEnd: 15000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	prepared := fastPathPreparedID(t, fastPathCall(t, session, "oboard_task", map[string]any{"intent": "server.manage", "target_refs": []any{fmt.Sprintf("server:%d", server.ID)}, "params": map[string]any{"bbr_enabled": true}}))
	server.Name = "stale-updated"
	if err := db.UpdateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	stale := fastPathCall(t, session, "oboard_commit_task", map[string]any{"prepared_id": prepared, "idempotency_key": "stale-revision-key"})
	if stale["status"] != "error" || stale["error"].(map[string]any)["code"] != "stale" {
		t.Fatalf("stale commit=%#v", stale)
	}
}

func TestMCPCompactDiscoverAndWorkflow(t *testing.T) {
	_, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	compact := fastPathCall(t, session, "oboard_discover", map[string]any{})
	compactJSON, _ := json.Marshal(compact)
	if !strings.Contains(string(compactJSON), `"primary_tool":"oboard_task"`) || strings.Contains(string(compactJSON), "authorized_capabilities") {
		t.Fatalf("compact discover=%s", compactJSON)
	}
	full := fastPathCall(t, session, "oboard_discover", map[string]any{"detail_level": "full"})
	fullJSON, _ := json.Marshal(full)
	if !strings.Contains(string(fullJSON), "authorized_capabilities") {
		t.Fatalf("full discover=%s", fullJSON)
	}
}

func TestMCPUnifiedSkillClientPacks(t *testing.T) {
	root := filepath.Join("..", "..")
	canonical, err := os.ReadFile(filepath.Join(root, "skills", "oboard", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		filepath.Join(".agents", "skills", "oboard", "SKILL.md"),
		filepath.Join(".claude", "skills", "oboard", "SKILL.md"),
		filepath.Join(".opencode", "skills", "oboard", "SKILL.md"),
		filepath.Join(".pi", "skills", "oboard", "SKILL.md"),
	} {
		generated, err := os.ReadFile(filepath.Join(root, target))
		if err != nil {
			t.Fatal(err)
		}
		if string(generated) != string(canonical) {
			t.Fatalf("generated skill is stale: %s", target)
		}
	}
}
