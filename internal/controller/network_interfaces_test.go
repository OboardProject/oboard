package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestNetworkInterfacesEndpointQueuesSupportedAgentTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	server := &model.Server{
		Name:           "edge",
		AgentID:        "agent-edge",
		AgentTokenHash: security.HashSecret("agent-token"),
		AgentBuild:     "20260804000000",
		ListenIP:       "0.0.0.0",
		PortRangeStart: 10000,
		PortRangeEnd:   10100,
		Status:         model.ServerOnline,
	}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/ui/servers/%d/network-interfaces", server.ID)
	request(t, h, http.MethodPost, path, token, map[string]any{}, http.StatusConflict)

	server.AgentBuild = agentBuildMinNetworkInterfaces
	if err := db.UpdateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	response := request(t, h, http.MethodPost, path, token, map[string]any{}, http.StatusAccepted)
	task := response["task"].(map[string]any)
	if task["type"] != model.AgentTaskTypeListNetworkInterfaces || task["status"] != "pending" {
		t.Fatalf("unexpected network interface task: %#v", task)
	}
}

func TestValidateNetworkInterfacesTaskResult(t *testing.T) {
	valid := `{"message":"network interfaces listed","interfaces":[{"name":"eth0","up":true,"running":true,"loopback":false,"addresses":["192.0.2.10/24","2001:db8::10/64"]}]}`
	if err := validateNetworkInterfacesTaskResult(valid); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"empty name":        `{"interfaces":[{"name":"","addresses":[]}]}`,
		"invalid name":      `{"interfaces":[{"name":"eth0;id","addresses":[]}]}`,
		"duplicate name":    `{"interfaces":[{"name":"eth0","addresses":[]},{"name":"eth0","addresses":[]}]}`,
		"invalid address":   `{"interfaces":[{"name":"eth0","addresses":["not-an-ip"]}]}`,
		"duplicate address": `{"interfaces":[{"name":"eth0","addresses":["192.0.2.1/24","192.0.2.1/24"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateNetworkInterfacesTaskResult(raw); err == nil {
				t.Fatal("invalid result was accepted")
			}
		})
	}
}

func TestNetworkInterfaceIPStackIgnoresLocalOnlyAddresses(t *testing.T) {
	for _, test := range []struct {
		name      string
		addresses []string
		want      model.IPStack
	}{
		{name: "IPv4", addresses: []string{"192.0.2.10/24", "fe80::1/64"}, want: model.IPStackIPv4Only},
		{name: "HE IPv6", addresses: []string{"2001:470:1f00::2/64", "fe80::1/64"}, want: model.IPStackIPv6Only},
		{name: "dual stack", addresses: []string{"10.0.0.2/24", "2001:db8::2/64"}, want: model.IPStackDualStack},
		{name: "local only", addresses: []string{"127.0.0.1/8", "::1/128", "fe80::1/64"}, want: model.IPStackAuto},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := networkInterfaceIPStack(model.NetworkInterfaceInfo{Addresses: test.addresses}); got != test.want {
				t.Fatalf("networkInterfaceIPStack(%v) = %q, want %q", test.addresses, got, test.want)
			}
		})
	}
}

func TestNetworkInterfaceGlobalFamiliesIgnoresPrivateIPv4(t *testing.T) {
	gotIPv4, gotIPv6 := networkInterfaceGlobalFamilies(model.NetworkInterfaceInfo{Addresses: []string{"10.7.0.68/23", "2408:820c:7509:b244::1/64"}})
	if gotIPv4 || !gotIPv6 {
		t.Fatalf("eth0-style addresses v4=%v v6=%v, want v4=false v6=true", gotIPv4, gotIPv6)
	}
}

func TestRoutingRulesUseLatestAgentInterfaceIPStack(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "edge", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeListNetworkInterfaces, PayloadJSON: `{}`, Status: "pending", ResultJSON: `{}`, Nonce: "interfaces"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(ctx, task.ID, "succeeded", `{"interfaces":[{"name":"he-ipv6","up":true,"running":true,"loopback":false,"addresses":["2001:470:23:701::2/64","fe80::5216:1a9f/64"]}]}`); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	for _, action := range []model.RouteAction{model.RouteActionProxyPath, model.RouteActionInterface} {
		rules, err := srv.routingRulesWithInterfaceIPStacks(ctx, server.ID, []model.RoutingRule{{ServerID: server.ID, Action: action, InterfaceName: "he-ipv6", Enabled: true}})
		if err != nil {
			t.Fatal(err)
		}
		if len(rules) != 1 || rules[0].InterfaceIPStack != model.IPStackIPv6Only {
			t.Fatalf("action %s resolved rules = %#v", action, rules)
		}
		if !rules[0].InterfaceBindKnown || rules[0].InterfaceHasGlobalIPv4 || !rules[0].InterfaceHasGlobalIPv6 {
			t.Fatalf("action %s global families = known=%v v4=%v v6=%v", action, rules[0].InterfaceBindKnown, rules[0].InterfaceHasGlobalIPv4, rules[0].InterfaceHasGlobalIPv6)
		}
	}
}

func TestGenerateConfigInterfaceActionWARPUsesAgentIPv6Stack(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	node := &model.Server{Name: "NB TYO", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Kind: model.ProxyPathKindChain, InboundID: inbound.ID, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProxyPathStep(ctx, &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepWARP, TransportMode: model.ProxyPathTransportSingBox}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateWARPProfile(ctx, &model.WARPProfile{
		ServerID: node.ID, Name: "warp", Status: model.WARPStatusReady, Enabled: true,
		ConfigJSON: `{"type":"wireguard","address":["172.16.0.2/32"],"private_key":"private","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"public","allowed_ips":["0.0.0.0/0","::/0"]}]}`,
	}); err != nil {
		t.Fatal(err)
	}
	rule := &model.RoutingRule{
		ServerID: node.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &path.ID,
		SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "NB TYO-route",
		MatchJSON: `{}`, Action: model.RouteActionInterface, InterfaceName: "he-ipv6", Enabled: true,
	}
	if err := db.CreateRoutingRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: node.ID, Type: model.AgentTaskTypeListNetworkInterfaces, PayloadJSON: `{}`, Status: "pending", ResultJSON: `{}`, Nonce: "interfaces"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(ctx, task.ID, "succeeded", `{"interfaces":[{"name":"he-ipv6","up":true,"running":true,"loopback":false,"addresses":["2001:470:23:701::2/64","fe80::5216:1a9f/64"]}]}`); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	data, err := db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := srv.generateServerCoreConfigWithLedger(ctx, *node, data, nil)
	if err != nil {
		t.Fatalf("generate with interface action: %v", err)
	}
	if _, err := srv.currentDeploymentProjection(ctx, *node, data, nil, nil, nil); err != nil {
		t.Fatalf("projection with interface action: %v", err)
	}
	if !strings.Contains(generated.Config, `"bind_interface": "he-ipv6"`) {
		t.Fatalf("generated config missing he-ipv6 bind: %s", generated.Config)
	}
	if !strings.Contains(generated.Config, `"strategy": "ipv6_only"`) {
		t.Fatalf("generated config missing ipv6_only WARP resolver: %s", generated.Config)
	}
}

func TestNetworkInterfacesTaskCallbackRejectsInvalidResult(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{
		Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline,
	}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{
		ServerID: server.ID, Type: model.AgentTaskTypeListNetworkInterfaces, PayloadJSON: "{}",
		Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "network-interfaces",
	}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	report, _ := json.Marshal(map[string]any{
		"task_id": task.ID, "status": "succeeded",
		"result_json": `{"interfaces":[{"name":"eth0;id","addresses":[]}]}`,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/task-results", bytes.NewReader(report))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer agent-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid callback status=%d body=%s", rr.Code, rr.Body.String())
	}
	stored, err := db.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" {
		t.Fatalf("invalid callback completed task: %#v", stored)
	}
}
