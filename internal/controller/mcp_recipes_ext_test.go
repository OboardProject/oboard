package controller

import (
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func TestNewRecipeRouting(t *testing.T) {
	s := &Server{}
	tests := []struct {
		name, goal string
		want       string
		refs       []string
		params     map[string]any
	}{
		{name: "outbound zh", goal: "给东京服务器创建出口", want: "outbound.manage", refs: []string{"server:1"}, params: map[string]any{"name": "出口A", "protocol": "shadowsocks", "target_address": "203.0.113.5", "target_port": 8388}},
		{name: "outbound en", goal: "create outbound on server 1", want: "outbound.manage", params: map[string]any{"server_id": 1}},
		{name: "routing rule set zh", goal: "创建分流规则集", want: "routing_rule_set.manage", params: map[string]any{"name": "广告域名", "url": "https://rules.example/ads.json", "format": "singbox_source"}},
		{name: "routing rule set en", goal: "refresh routing rule set", want: "routing_rule_set.manage", params: map[string]any{"routing_rule_set_id": 1}},
		{name: "routing zh", goal: "新建一条分流规则", want: "routing.manage", refs: []string{"server:1"}, params: map[string]any{"name": "直连", "action": "direct"}},
		{name: "import zh", goal: "导入节点", want: "external_outbound.import", params: map[string]any{"content": "ss://base64@host:8388"}},
		{name: "dns policy", goal: "设置东京服务器的 DNS 策略", want: "dns_policy.manage", refs: []string{"server:1"}, params: map[string]any{"encrypted_list_id": 1, "bootstrap_list_id": 2}},
		{name: "dns record", goal: "创建一条解析记录", want: "dns_record.manage", params: map[string]any{"record": map[string]any{"dns_zone_id": 1, "name": "www", "type": "A", "content": "1.2.3.4"}}},
		{name: "port forward", goal: "添加端口转发", want: "port_forward.manage", params: map[string]any{"port_forward": map[string]any{"name": "pf", "listen_port": 1000, "target_port": 2000}}},
		{name: "tunnel", goal: "创建一条 wireguard 隧道", want: "tunnel.manage", params: map[string]any{"tunnel": map[string]any{"name": "t1"}}},
		{name: "diagnose", goal: "诊断服务器", want: "host_ops.manage", refs: []string{"server:1"}},
		{name: "logs", goal: "拉取服务器日志", want: "host_ops.manage", refs: []string{"server:1"}},
		{name: "network interfaces", goal: "读取服务器网卡", want: "host_ops.manage", refs: []string{"server:1"}},
		{name: "controller update", goal: "检查主控更新", want: "controller_update.manage"},
		{name: "notification", goal: "创建一个通知频道", want: "notification.manage", params: map[string]any{"notification_channel": map[string]any{"name": "tg", "type": "telegram"}}},
		{name: "certificate", goal: "签发证书", want: "certificate.manage", params: map[string]any{"certificate_id": 3}},
		{name: "settings", goal: "修改全局设置", want: "settings.manage", params: map[string]any{"audit_enabled": false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := mcpTaskInput{Goal: test.goal, TargetRefs: test.refs, Params: test.params}
			recipe, candidates, ok := s.matchMCPRecipe(input)
			if !ok || recipe.ID != test.want {
				t.Fatalf("recipe=%#v candidates=%#v ok=%v", recipe, candidates, ok)
			}
			if recipe.Prepare == nil {
				t.Fatalf("matched recipe %q is not executable", recipe.ID)
			}
		})
	}
}

func TestHostOpsAndControllerUpdateRecipes(t *testing.T) {
	server := &Server{}
	interfaces, err := server.prepareHostOpsRecipe(t.Context(), application.Principal{}, mcpTaskInput{Goal: "读取网卡", Params: map[string]any{"server_id": 41}})
	if err != nil || interfaces.Status != "ready" || interfaces.Operations[0].Capability != "servers.list_network_interfaces" || taskIntParam(interfaces.Operations[0].Input, "server_id") != 41 {
		t.Fatalf("interfaces=%#v err=%v", interfaces, err)
	}
	tests := []struct {
		name, goal, capability string
		params                 map[string]any
		confirm                bool
	}{
		{name: "check", goal: "检查主控更新", capability: "controller_update.check"},
		{name: "channel", goal: "切换主控更新通道", capability: "controller_update.set_channel", params: map[string]any{"channel": "stable"}},
		{name: "install", goal: "安装主控更新", capability: "controller_update.install", confirm: true},
		{name: "cancel", goal: "取消主控更新", capability: "controller_update.cancel", confirm: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := server.prepareControllerUpdateRecipe(t.Context(), application.Principal{}, mcpTaskInput{Goal: test.goal, Params: test.params})
			if err != nil || prepared.Status != "ready" || len(prepared.Operations) != 1 {
				t.Fatalf("prepared=%#v err=%v", prepared, err)
			}
			operation := prepared.Operations[0]
			if operation.Capability != test.capability {
				t.Fatalf("capability=%q want %q", operation.Capability, test.capability)
			}
			if test.confirm && operation.Input["confirm"] != true {
				t.Fatalf("confirmation missing: %#v", operation.Input)
			}
		})
	}
}

func TestRoutingRuleRecipeSupportsPathStageFieldsAndPlacement(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := t.Context()
	root := &model.Server{Name: "entry", Status: model.ServerOnline}
	stage := &model.Server{Name: "stage", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, stage); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: root.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &stage.ID, ConfigJSON: `{}`}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}

	prepared, err := server.prepareRoutingRuleRecipe(ctx, application.Principal{}, mcpTaskInput{Params: map[string]any{
		"scope": model.RoutingRuleScopePathStage, "proxy_path_id": path.ID, "stage_step_id": step.ID,
		"sort_position": 2, "match_source": model.RoutingMatchSourceRuleSet, "rule_set_id": 17,
		"name": "stage ads", "action": model.RouteActionProxyPath, "target_proxy_path_id": 19,
		"source_prefix": "2001:db8:10::/64", "enabled": true,
	}})
	if err != nil || prepared.Status != "ready" || len(prepared.Operations) != 1 {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	operation := prepared.Operations[0]
	if operation.Capability != "routing_rules.create" {
		t.Fatalf("capability=%q", operation.Capability)
	}
	rule, ok := operation.Input["routing_rule"].(map[string]any)
	if !ok {
		t.Fatalf("routing_rule=%#v", operation.Input["routing_rule"])
	}
	for key, want := range map[string]int64{
		"server_id": stage.ID, "proxy_path_id": path.ID, "stage_step_id": step.ID,
		"rule_set_id": 17, "target_proxy_path_id": 19,
	} {
		if got := int64(taskIntParam(rule, key)); got != want {
			t.Fatalf("%s=%d want %d in %#v", key, got, want, rule)
		}
	}
	if rule["scope"] != model.RoutingRuleScopePathStage || rule["match_source"] != model.RoutingMatchSourceRuleSet || rule["source_prefix"] != "2001:db8:10::/64" {
		t.Fatalf("recent routing fields were not forwarded: %#v", rule)
	}

	synchronized, err := server.prepareRoutingRuleRecipe(ctx, application.Principal{}, mcpTaskInput{Params: map[string]any{
		"scope": model.RoutingRuleScopePathStage, "proxy_path_id": path.ID,
		"sync_source_rule_id": 23, "sync_enabled": true, "action": model.RouteActionBlock, "enabled": true,
	}})
	if err != nil || synchronized.Status != "ready" {
		t.Fatalf("synchronized=%#v err=%v", synchronized, err)
	}
	syncRule := synchronized.Operations[0].Input["routing_rule"].(map[string]any)
	if taskIntParam(syncRule, "sync_source_rule_id") != 23 || syncRule["sync_enabled"] != true {
		t.Fatalf("synchronized fields were not forwarded: %#v", syncRule)
	}

	placements := []any{
		map[string]any{"rule_id": 31, "stage_step_id": step.ID, "sort_position": 0},
		map[string]any{"rule_id": 32, "sort_position": 0},
	}
	placed, err := server.prepareRoutingRuleRecipe(ctx, application.Principal{}, mcpTaskInput{
		Goal: "重排代理路径中的分流规则", TargetRefs: []string{"proxy_path:" + int64String(path.ID)}, Params: map[string]any{"placements": placements},
	})
	if err != nil || placed.Status != "ready" || len(placed.Operations) != 1 {
		t.Fatalf("placed=%#v err=%v", placed, err)
	}
	if placed.Operations[0].Capability != "routing_rules.place" || int64(taskIntParam(placed.Operations[0].Input, "proxy_path_id")) != path.ID || placed.Operations[0].Input["placements"] == nil {
		t.Fatalf("unexpected placement operation: %#v", placed.Operations[0])
	}
}

func TestRoutingRuleSetRecipeBuildsCRUDAndRefreshOperations(t *testing.T) {
	server := &Server{}
	tests := []struct {
		name       string
		input      mcpTaskInput
		capability string
		setID      int
	}{
		{name: "create", input: mcpTaskInput{Goal: "创建分流规则集", Params: map[string]any{"routing_rule_set": map[string]any{"name": "shared", "url": "https://rules.example/shared.json", "format": "singbox_source"}}}, capability: "routing_rule_sets.create"},
		{name: "update", input: mcpTaskInput{Goal: "修改分流规则集", Params: map[string]any{"routing_rule_set_id": 7, "changes": map[string]any{"name": "shared-v2"}}}, capability: "routing_rule_sets.update", setID: 7},
		{name: "delete", input: mcpTaskInput{Goal: "删除分流规则集", TargetRefs: []string{"routing_rule_set:8"}}, capability: "routing_rule_sets.delete", setID: 8},
		{name: "refresh", input: mcpTaskInput{Goal: "refresh routing rule set", Params: map[string]any{"rule_set_id": 9}}, capability: "routing_rule_sets.refresh", setID: 9},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := server.prepareRoutingRuleSetRecipe(t.Context(), application.Principal{}, test.input)
			if err != nil || prepared.Status != "ready" || len(prepared.Operations) != 1 {
				t.Fatalf("prepared=%#v err=%v", prepared, err)
			}
			operation := prepared.Operations[0]
			if operation.Capability != test.capability {
				t.Fatalf("capability=%q want %q", operation.Capability, test.capability)
			}
			if test.setID > 0 && taskIntParam(operation.Input, "routing_rule_set_id") != test.setID {
				t.Fatalf("routing_rule_set_id=%v want %d", operation.Input["routing_rule_set_id"], test.setID)
			}
			if test.capability == "routing_rule_sets.delete" && operation.Input["confirm"] != true {
				t.Fatalf("delete did not require confirmation: %#v", operation.Input)
			}
		})
	}
}
