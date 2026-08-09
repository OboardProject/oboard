package core

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestProxyPathPlanRuntimeNodesAreSharedAndCredentialFree(t *testing.T) {
	source := model.Server{ID: 1, Name: "source", ChainSecret: "source-secret", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "target", ChainSecret: "target-secret", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	paths := []model.ProxyPath{
		{ID: 1, Name: "one", InboundID: root.ID, Secret: "one-secret", Enabled: true},
		{ID: 2, Name: "two", InboundID: root.ID, Secret: "two-secret", Enabled: true},
		{ID: 3, Name: "disabled", InboundID: root.ID, Secret: "disabled-secret", Enabled: false},
	}
	targetID := target.ID
	steps := []model.ProxyPathStep{
		{ID: 101, PathID: 1, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
		{ID: 201, PathID: 2, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
		{ID: 301, PathID: 3, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
	}

	plans, err := BuildProxyPathPlansWithLedger(paths, steps, []model.Server{source, target}, []model.Inbound{root}, NewProxyPathPortLedger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 || len(plans[0].RuntimeNodes) != 1 || len(plans[1].RuntimeNodes) != 1 || len(plans[2].RuntimeNodes) != 0 {
		t.Fatalf("runtime nodes = %#v", plans)
	}
	first, second := plans[0].RuntimeNodes[0], plans[1].RuntimeNodes[0]
	if first.Kind != "shared_chain_inbound" || !first.Shared || first.ReferenceCount != 2 || first.ResourceKey != second.ResourceKey || first.Port != second.Port {
		t.Fatalf("shared runtime nodes = %#v / %#v", first, second)
	}
	if first.Profile != DefaultProxyPathChainMethod || first.Network != model.ForwardProtocolTCPUDP || first.ListenScope != "public" {
		t.Fatalf("runtime summary = %#v", first)
	}
	encoded, err := json.Marshal(plans)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"source-secret", "target-secret", "one-secret", "two-secret", "password", "private_key"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("runtime plan leaked %q: %s", secret, encoded)
		}
	}
}

func TestProxyPathPlanRuntimeNodesIncludeTrustedProcessingInbound(t *testing.T) {
	source := model.Server{ID: 1, Name: "source", ChainSecret: "source-secret", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100, InternalPortRangeStart: 50000, InternalPortRangeEnd: 50100}
	target := model.Server{ID: 2, Name: "target", ChainSecret: "target-secret", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100, InternalPortRangeStart: 51000, InternalPortRangeEnd: 51100}
	root := model.Inbound{ID: 10, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 1, Name: "transparent", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	targetID := target.ID
	step := model.ProxyPathStep{ID: 101, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`}

	plans, err := BuildProxyPathPlansWithLedger([]model.ProxyPath{path}, []model.ProxyPathStep{step}, []model.Server{source, target}, []model.Inbound{root}, NewProxyPathPortLedger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].RuntimeNodes) != 2 {
		t.Fatalf("runtime nodes = %#v", plans)
	}
	kinds := map[string]model.ProxyPathRuntimeNode{}
	for _, node := range plans[0].RuntimeNodes {
		kinds[node.Kind] = node
	}
	if kinds["shared_transparent_inbound"].ListenScope != "public" {
		t.Fatalf("outer runtime node = %#v", kinds["shared_transparent_inbound"])
	}
	inner := kinds["trusted_processing_inbound"]
	if inner.ListenScope != "loopback" || inner.ListenIP != "127.0.0.1" || inner.Protocol != model.ProtocolVLESS {
		t.Fatalf("trusted processing node = %#v", inner)
	}
}

func TestProxyPathPlanRuntimeNodesReserveDistinctTrustedInnerPorts(t *testing.T) {
	source := model.Server{ID: 1, Name: "source", ChainSecret: "source-secret", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "target", ChainSecret: "target-secret", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100, InternalPortRangeStart: 51000, InternalPortRangeEnd: 51001}
	roots := []model.Inbound{
		{ID: 10, ServerID: source.ID, Name: "entry-one", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
		{ID: 11, ServerID: source.ID, Name: "entry-two", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true},
	}
	paths := []model.ProxyPath{
		{ID: 1, Name: "one", InboundID: roots[0].ID, Secret: "one-secret", Enabled: true},
		{ID: 2, Name: "two", InboundID: roots[1].ID, Secret: "two-secret", Enabled: true},
	}
	targetID := target.ID
	steps := []model.ProxyPathStep{
		{ID: 101, PathID: 1, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`},
		{ID: 201, PathID: 2, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`},
	}

	plans, err := BuildProxyPathPlansWithLedger(paths, steps, []model.Server{source, target}, roots, NewProxyPathPortLedger(nil))
	if err != nil {
		t.Fatal(err)
	}
	ports := map[int]bool{}
	for _, plan := range plans {
		for _, node := range plan.RuntimeNodes {
			if node.Kind == "trusted_processing_inbound" {
				ports[node.Port] = true
			}
		}
	}
	if len(ports) != 2 || ports[0] {
		t.Fatalf("trusted processing ports = %#v, plans = %#v", ports, plans)
	}
}

func TestSharedProxyPathShadowsocksServiceReusesPortAndRoutesByPath(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	serverC := model.Server{ID: 3, Name: "C", ChainSecret: "chain-c", PublicIPv4: "203.0.113.3", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 32000, PortRangeEnd: 32100}
	serverD := model.Server{ID: 4, Name: "D", ChainSecret: "chain-d", PublicIPv4: "203.0.113.4", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 33000, PortRangeEnd: 33100}
	serverE := model.Server{ID: 5, Name: "E", ChainSecret: "chain-e", PublicIPv4: "203.0.113.5", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 34000, PortRangeEnd: 34100}
	rootA := model.Inbound{ID: 11, ServerID: serverA.ID, Name: "entry-a", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	rootC := model.Inbound{ID: 12, ServerID: serverC.ID, Name: "entry-c", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true}
	pathA := model.ProxyPath{ID: 101, Name: "A-B-D", InboundID: rootA.ID, Secret: "path-a", Enabled: true}
	pathC := model.ProxyPath{ID: 202, Name: "C-B-E", InboundID: rootC.ID, Secret: "path-c", Enabled: true}
	bID, dID, eID := serverB.ID, serverD.ID, serverE.ID
	steps := []model.ProxyPathStep{
		{ID: 1001, PathID: pathA.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
		{ID: 1002, PathID: pathA.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &dID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
		{ID: 2001, PathID: pathC.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
		{ID: 2002, PathID: pathC.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &eID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
	}
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "alice-password"},
		{ID: 2, Username: "bob", Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "bob-password"},
	}
	opts := ConfigOptions{
		Servers:        []model.Server{serverA, serverB, serverC, serverD, serverE},
		Inbounds:       []model.Inbound{rootA, rootC},
		ProxyPaths:     []model.ProxyPath{pathA, pathC},
		ProxyPathSteps: steps,
		InboundUsers: []model.InboundUser{
			{InboundID: rootA.ID, UserID: users[0].ID, Enabled: true},
			{InboundID: rootC.ID, UserID: users[1].ID, Enabled: true},
		},
	}

	configA := mustServerConfig(t, serverA, opts.Inbounds, users, opts)
	configC := mustServerConfig(t, serverC, opts.Inbounds, users, opts)
	outA := findOutbound(configA, proxyPathStepTag(pathA.ID, 1))
	outC := findOutbound(configC, proxyPathStepTag(pathC.ID, 1))
	if outA["type"] != "shadowsocks" || outA["method"] != DefaultProxyPathChainMethod {
		t.Fatalf("A first hop = %#v", outA)
	}
	if outC["type"] != "shadowsocks" || outC["method"] != DefaultProxyPathChainMethod {
		t.Fatalf("C first hop = %#v", outC)
	}
	if outA["server"] != serverB.PublicIPv4 || outC["server"] != serverB.PublicIPv4 || intFromAny(outA["server_port"]) != intFromAny(outC["server_port"]) {
		t.Fatalf("A/C did not reuse B service: A=%#v C=%#v", outA, outC)
	}

	configB := mustServerConfig(t, serverB, opts.Inbounds, users, opts)
	parsedB := parseSingBoxConfig(t, configB)
	sharedTag := proxyPathChainServiceTag(proxyPathChainServiceKey{Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod})
	sharedCount := 0
	var sharedInbound map[string]any
	for _, inbound := range parsedB.Inbounds {
		if inbound["tag"] == sharedTag {
			sharedCount++
			sharedInbound = inbound
		}
	}
	if sharedCount != 1 {
		t.Fatalf("B shared SS inbound count = %d, config=%s", sharedCount, configB)
	}
	chainUsers := mapList(sharedInbound["users"])
	if len(chainUsers) != 2 {
		t.Fatalf("B shared SS users = %#v, want one internal identity per path", chainUsers)
	}
	for _, user := range chainUsers {
		name := strings.TrimSpace(stringValue(user, "name", ""))
		if !strings.HasPrefix(name, "__oboard_path_") || name == "alice" || name == "bob" {
			t.Fatalf("downstream service retained an end-user identity: %#v", chainUsers)
		}
	}
	if !hasAuthUserRoute(configB, sharedTag, proxyPathStepTag(pathA.ID, 2), proxyPathInternalUser(pathA, steps[0]).Username) {
		t.Fatalf("B missing path A auth_user route: %s", configB)
	}
	if !hasAuthUserRoute(configB, sharedTag, proxyPathStepTag(pathC.ID, 2), proxyPathInternalUser(pathC, steps[2]).Username) {
		t.Fatalf("B missing path C auth_user route: %s", configB)
	}
}

func TestSharedProxyPathShadowsocksMethodsUseSeparatePorts(t *testing.T) {
	for _, method := range []string{"", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305"} {
		if err := ValidateProxyPathChainMethod(method); err != nil {
			t.Fatalf("valid method %q rejected: %v", method, err)
		}
	}
	for _, method := range []string{"aes-128-gcm", "chacha20-ietf-poly1305", "2022-blake3-aes-192-gcm"} {
		if err := ValidateProxyPathChainMethod(method); err == nil {
			t.Fatalf("invalid chain method %q accepted", method)
		}
	}
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path128 := model.ProxyPath{ID: 10, Name: "default-128", InboundID: root.ID, Secret: "path-128", Enabled: true}
	path256 := model.ProxyPath{ID: 20, Name: "explicit-256", InboundID: root.ID, Secret: "path-256", Enabled: true}
	path256Again := model.ProxyPath{ID: 30, Name: "reused-256", InboundID: root.ID, Secret: "path-256-again", Enabled: true}
	bID := serverB.ID
	steps := []model.ProxyPathStep{
		{ID: 11, PathID: path128.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
		{ID: 21, PathID: path256.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{"chain_method":"2022-blake3-aes-256-gcm"}`},
		{ID: 31, PathID: path256Again.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{"chain_method":"2022-blake3-aes-256-gcm"}`},
	}
	opts := ConfigOptions{Servers: []model.Server{serverA, serverB}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path128, path256, path256Again}, ProxyPathSteps: steps}
	services, err := buildProxyPathChainServices(opts.ProxyPaths, steps, opts.Servers, opts.Inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	service128 := services[proxyPathChainServiceKey{ServerID: serverB.ID, Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod}]
	service256 := services[proxyPathChainServiceKey{ServerID: serverB.ID, Protocol: model.ProtocolSS, Profile: "2022-blake3-aes-256-gcm"}]
	if len(services) != 2 || service128 == nil || service256 == nil {
		t.Fatalf("shared services = %#v", services)
	}
	if service128.Inbound.Port == service256.Inbound.Port {
		t.Fatalf("different methods reused port %d", service128.Inbound.Port)
	}
	if len(service256.Users) != 2 || service256.Users[0].Username == service256.Users[1].Username {
		t.Fatalf("later AES-256 path did not reuse the method service: %#v", service256.Users)
	}
	if got := proxyPathStepChainMethod(steps[0]); got != DefaultProxyPathChainMethod {
		t.Fatalf("empty chain_method default = %q", got)
	}
	configB := mustServerConfig(t, serverB, opts.Inbounds, nil, opts)
	if !hasInbound(configB, service128.Tag, proxyPathInternalUser(path128, steps[0]).Username) || !hasInbound(configB, service256.Tag, proxyPathInternalUser(path256, steps[1]).Username) {
		t.Fatalf("B did not emit both method services: %s", configB)
	}
}

func TestSharedProxyPathVLESSRealityServiceReusesProfile(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	paths := []model.ProxyPath{
		{ID: 10, Name: "reality-one", InboundID: root.ID, Secret: "path-one", Enabled: true},
		{ID: 20, Name: "reality-two", InboundID: root.ID, Secret: "path-two", Enabled: true},
	}
	bID := serverB.ID
	steps := []model.ProxyPathStep{
		{ID: 11, PathID: paths[0].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{"chain_protocol":"vless","reality_handshake_server":"cdn.icloud-content.com","reality_handshake_port":443}`},
		{ID: 21, PathID: paths[1].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{"chain_protocol":"vless"}`},
	}
	opts := ConfigOptions{Servers: []model.Server{serverA, serverB}, Inbounds: []model.Inbound{root}, ProxyPaths: paths, ProxyPathSteps: steps}
	services, err := buildProxyPathChainServices(paths, steps, opts.Servers, opts.Inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := proxyPathChainServiceKey{ServerID: serverB.ID, Protocol: model.ProtocolVLESS, Profile: "reality:cdn.icloud-content.com:443"}
	service := services[key]
	if len(services) != 1 || service == nil || len(service.Users) != 2 {
		t.Fatalf("VLESS services = %#v", services)
	}
	var serviceConfig map[string]any
	if err := json.Unmarshal([]byte(service.Inbound.ConfigJSON), &serviceConfig); err != nil {
		t.Fatal(err)
	}
	tls := serviceConfig["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	handshake := reality["handshake"].(map[string]any)
	if service.Inbound.Protocol != model.ProtocolVLESS || serviceConfig["flow"] != "xtls-rprx-vision" || tls["server_name"] != DefaultProxyPathRealityHandshakeServer || handshake["server"] != DefaultProxyPathRealityHandshakeServer || intFromAny(handshake["server_port"]) != 443 {
		t.Fatalf("VLESS service config = %#v", serviceConfig)
	}
	if reality["private_key"] == "" || reality["public_key"] == "" || reality["short_id"] == "" {
		t.Fatalf("VLESS Reality credentials missing: %#v", reality)
	}
	configA := mustServerConfig(t, serverA, opts.Inbounds, nil, opts)
	outbound := findOutbound(configA, proxyPathStepTag(paths[0].ID, 1))
	if outbound["type"] != "vless" || outbound["flow"] != "xtls-rprx-vision" || intFromAny(outbound["server_port"]) != service.Inbound.Port {
		t.Fatalf("VLESS outbound = %#v", outbound)
	}
	configB := mustServerConfig(t, serverB, opts.Inbounds, nil, opts)
	if !hasInbound(configB, service.Tag, proxyPathInternalUser(paths[0], steps[0]).Username) || !hasInbound(configB, service.Tag, proxyPathInternalUser(paths[1], steps[1]).Username) {
		t.Fatalf("VLESS service did not contain both path users: %s", configB)
	}
}

func TestGeneratedProxyPathMieruServiceUsesTCPAndMandatoryUserHint(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 10, Name: "mieru", InboundID: root.ID, Secret: "path-mieru", Enabled: true}
	bID := serverB.ID
	step := model.ProxyPathStep{ID: 11, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{"chain_protocol":"mieru"}`}
	opts := ConfigOptions{Servers: []model.Server{serverA, serverB}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: []model.ProxyPathStep{step}}
	services, err := buildProxyPathChainServices(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := services[proxyPathChainServiceKey{ServerID: serverB.ID, Protocol: model.ProtocolMieru, Profile: "tcp"}]
	if len(services) != 1 || service == nil || service.Inbound.Protocol != model.ProtocolMieru {
		t.Fatalf("Mieru services = %#v", services)
	}
	var serviceConfig map[string]any
	if err := json.Unmarshal([]byte(service.Inbound.ConfigJSON), &serviceConfig); err != nil {
		t.Fatal(err)
	}
	if serviceConfig["transport"] != "TCP" || serviceConfig["multiplexing"] != "MULTIPLEXING_DEFAULT" || serviceConfig["user_hint_is_mandatory"] != true {
		t.Fatalf("Mieru service config = %#v", serviceConfig)
	}
	configA := mustServerConfig(t, serverA, opts.Inbounds, nil, opts)
	outbound := findOutbound(configA, proxyPathStepTag(path.ID, 1))
	if outbound["type"] != "mieru" || outbound["transport"] != "TCP" || outbound["multiplexing"] != "MULTIPLEXING_DEFAULT" || outbound["username"] == "" || outbound["password"] == "" {
		t.Fatalf("Mieru outbound = %#v", outbound)
	}
	configB := mustServerConfig(t, serverB, opts.Inbounds, nil, opts)
	parsed := parseSingBoxConfig(t, configB)
	found := false
	for _, inbound := range parsed.Inbounds {
		if inbound["tag"] == service.Tag {
			found = inbound["type"] == "mieru" && inbound["transport"] == "TCP" && inbound["user_hint_is_mandatory"] == true
		}
	}
	if !found {
		t.Fatalf("Mieru listener missing or invalid: %s", configB)
	}
}

func TestExplicitProxyPathInboundOverridesSharedShadowsocksDefault(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	target := model.Inbound{ID: 20, ServerID: serverB.ID, Name: "existing-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 40, Name: "explicit-inbound", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	bID, targetID := serverB.ID, target.ID
	step := model.ProxyPathStep{ID: 41, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, InboundID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "alice-password"}
	opts := ConfigOptions{
		Servers:        []model.Server{serverA, serverB},
		Inbounds:       []model.Inbound{root, target},
		ProxyPaths:     []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{step},
		InboundUsers:   []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
	}

	services, err := buildProxyPathChainServices(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("explicit inbound allocated shared Shadowsocks services: %#v", services)
	}

	configA := mustServerConfig(t, serverA, opts.Inbounds, []model.User{user}, opts)
	outbound := findOutbound(configA, proxyPathStepTag(path.ID, step.Position))
	if outbound["type"] != "vless" || intFromAny(outbound["server_port"]) != target.Port {
		t.Fatalf("explicit inbound protocol/port was not preserved: %#v", outbound)
	}
	configB := mustServerConfig(t, serverB, opts.Inbounds, []model.User{user}, opts)
	for _, inbound := range parseSingBoxConfig(t, configB).Inbounds {
		if strings.HasPrefix(stringValue(inbound, "tag", ""), "oboard-chain-ss-") {
			t.Fatalf("explicit inbound emitted an unused shared Shadowsocks listener: %#v", inbound)
		}
	}
}

func TestProxyPathDerivedTunnelsReuseStableResources(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	bID := serverB.ID

	t.Run("ssh", func(t *testing.T) {
		paths := []model.ProxyPath{{ID: 10, Name: "ssh-one", InboundID: root.ID, Enabled: true}, {ID: 20, Name: "ssh-two", InboundID: root.ID, Enabled: true}}
		steps := []model.ProxyPathStep{
			{ID: 11, PathID: paths[0].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh","ssh_port":22}`},
			{ID: 21, PathID: paths[1].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh","ssh_port":22}`},
		}
		plans, err := BuildProxyPathPlans(paths, steps, []model.Server{serverA, serverB}, []model.Inbound{root})
		if err != nil {
			t.Fatal(err)
		}
		if len(plans) != 2 || len(plans[0].Tunnels) != 1 || len(plans[1].Tunnels) != 1 {
			t.Fatalf("SSH plans = %#v", plans)
		}
		first, second := plans[0].Tunnels[0], plans[1].Tunnels[0]
		if first.ID != second.ID || first.ListenPort != second.ListenPort || first.ConfigJSON != second.ConfigJSON {
			t.Fatalf("SSH tunnel not reused: first=%#v second=%#v", first, second)
		}
		if _, err := exec.LookPath("ssh-keygen"); err == nil {
			var config map[string]any
			if err := json.Unmarshal([]byte(first.ConfigJSON), &config); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(t.TempDir(), "id_ed25519")
			if err := os.WriteFile(keyPath, []byte(stringValue(config, "client_private_key", "")), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("ssh-keygen", "-y", "-f", keyPath).CombinedOutput()
			if err != nil {
				t.Fatalf("native OpenSSH rejected derived key: %v: %s", err, output)
			}
			if strings.TrimSpace(string(output)) != stringValue(config, "client_public_key", "") {
				t.Fatalf("native OpenSSH public key mismatch: got=%q config=%q", strings.TrimSpace(string(output)), config["client_public_key"])
			}
		}
		derived, err := DerivedTunnelsFromProxyPaths(paths, steps, []model.Server{serverA, serverB}, []model.Inbound{root})
		if err != nil || len(derived) != 1 {
			t.Fatalf("deduplicated SSH tunnels = %#v err=%v", derived, err)
		}
		remaining, err := BuildProxyPathPlans(paths[1:], steps[1:], []model.Server{serverA, serverB}, []model.Inbound{root})
		if err != nil {
			t.Fatal(err)
		}
		if got := remaining[0].Tunnels[0]; got.ID != first.ID || got.ConfigJSON != first.ConfigJSON {
			t.Fatalf("deleting the first path rotated shared SSH state: before=%#v after=%#v", first, got)
		}
	})

	t.Run("wireguard", func(t *testing.T) {
		paths := []model.ProxyPath{{ID: 30, Name: "wg-128", InboundID: root.ID, Enabled: true}, {ID: 40, Name: "wg-256", InboundID: root.ID, Enabled: true}}
		steps := []model.ProxyPathStep{
			{ID: 31, PathID: paths[0].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"wireguard","chain_method":"2022-blake3-aes-128-gcm","persistent_keepalive":25}`},
			{ID: 41, PathID: paths[1].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"wireguard","chain_method":"2022-blake3-aes-256-gcm","persistent_keepalive":25}`},
		}
		plans, err := BuildProxyPathPlans(paths, steps, []model.Server{serverA, serverB}, []model.Inbound{root})
		if err != nil {
			t.Fatal(err)
		}
		first, second := plans[0].Tunnels[0], plans[1].Tunnels[0]
		if first.ID != second.ID || first.ConfigJSON != second.ConfigJSON {
			t.Fatalf("WireGuard tunnel not reused across SS methods: first=%#v second=%#v", first, second)
		}
		derived, err := DerivedTunnelsFromProxyPaths(paths, steps, []model.Server{serverA, serverB}, []model.Inbound{root})
		if err != nil || len(derived) != 1 {
			t.Fatalf("deduplicated WireGuard tunnels = %#v err=%v", derived, err)
		}
		remaining, err := BuildProxyPathPlans(paths[1:], steps[1:], []model.Server{serverA, serverB}, []model.Inbound{root})
		if err != nil {
			t.Fatal(err)
		}
		if got := remaining[0].Tunnels[0]; got.ID != first.ID || got.ConfigJSON != first.ConfigJSON {
			t.Fatalf("deleting the first path rotated shared WireGuard state: before=%#v after=%#v", first, got)
		}
	})
}

func TestProxyPathAccountingUsesFirstDecryptingServer(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A"}
	serverB := model.Server{ID: 2, Name: "B"}
	serverC := model.Server{ID: 3, Name: "C"}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, Enabled: true}
	userBinding := []model.InboundUser{{InboundID: root.ID, UserID: 7, Enabled: true}}
	bID, cID := serverB.ID, serverC.ID

	normalPath := model.ProxyPath{ID: 10, Name: "A-B-C", InboundID: root.ID, Enabled: true}
	normalSteps := []model.ProxyPathStep{
		{ID: 11, PathID: normalPath.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox},
		{ID: 12, PathID: normalPath.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &cID, TransportMode: model.ProxyPathTransportTunnel},
	}
	if got, ok := ProxyPathAccountingServerID(normalPath, normalSteps, []model.Inbound{root}); !ok || got != serverA.ID {
		t.Fatalf("normal path accounting server = %d, %v", got, ok)
	}
	if ProxyPathRequiresAccountingPathID(root.ID, []model.ProxyPath{normalPath}, normalSteps, []model.Inbound{root}) {
		t.Fatal("normal chain unexpectedly requires downstream accounting")
	}
	if users := TrafficAccountingUsersForServer(serverB.ID, []model.ProxyPath{normalPath}, normalSteps, []model.Inbound{root}, userBinding); users[7] {
		t.Fatalf("downstream shared server received billable user: %#v", users)
	}

	transparentPath := model.ProxyPath{ID: 20, Name: "A-forward-B-chain-C", InboundID: root.ID, Enabled: true}
	transparentSteps := []model.ProxyPathStep{
		{ID: 21, PathID: transparentPath.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true},
		{ID: 22, PathID: transparentPath.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &cID, TransportMode: model.ProxyPathTransportSingBox},
	}
	if got, ok := ProxyPathAccountingServerID(transparentPath, transparentSteps, []model.Inbound{root}); !ok || got != serverB.ID {
		t.Fatalf("transparent path accounting server = %d, %v", got, ok)
	}
	if !ProxyPathRequiresAccountingPathID(root.ID, []model.ProxyPath{transparentPath}, transparentSteps, []model.Inbound{root}) {
		t.Fatal("transparent path did not require path-scoped accounting")
	}
	if !IsProxyPathAccountingLocation(serverB.ID, root.ID, transparentPath.ID, []model.ProxyPath{transparentPath}, transparentSteps, []model.Inbound{root}) {
		t.Fatal("processing server was not accepted as accounting location")
	}
	if IsProxyPathAccountingLocation(serverA.ID, root.ID, transparentPath.ID, []model.ProxyPath{transparentPath}, transparentSteps, []model.Inbound{root}) {
		t.Fatal("transparent forwarding source was accepted as accounting location")
	}
	if users := TrafficAccountingUsersForServer(serverB.ID, []model.ProxyPath{transparentPath}, transparentSteps, []model.Inbound{root}, userBinding); !users[7] {
		t.Fatalf("processing server did not receive billable user: %#v", users)
	}
	if users := TrafficAccountingUsersForServer(serverC.ID, []model.ProxyPath{transparentPath}, transparentSteps, []model.Inbound{root}, userBinding); users[7] {
		t.Fatalf("downstream chain server received billable user: %#v", users)
	}
}

func TestTransparentPrefixBranchesShareForwardAndProcessingInbound(t *testing.T) {
	entry := model.Server{ID: 1, Name: "entry", ChainSecret: "entry-secret", PublicIPv4: "198.51.100.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	processor := model.Server{ID: 2, Name: "processor", ChainSecret: "processor-secret", PublicIPv4: "198.51.100.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	exit := model.Server{ID: 3, Name: "exit", ChainSecret: "exit-secret", PublicIPv4: "198.51.100.3", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 32000, PortRangeEnd: 32100}
	root := model.Inbound{ID: 17, ServerID: entry.ID, Name: "SS:8388", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"method":"2022-blake3-aes-128-gcm","password":"entry-password"}`, Enabled: true}
	chain := model.ProxyPath{ID: 29, Name: "entry-processor-exit", InboundID: root.ID, Secret: "chain-secret", Enabled: true}
	direct := model.ProxyPath{ID: 30, Kind: model.ProxyPathKindDirect, Name: "entry-processor-direct", InboundID: root.ID, Secret: "direct-secret", Enabled: true}
	processorID, exitID := processor.ID, exit.ID
	steps := []model.ProxyPathStep{
		{ID: 291, PathID: chain.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &processorID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`},
		{ID: 292, PathID: chain.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &exitID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
		{ID: 301, PathID: direct.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &processorID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`},
	}
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "alice-password"}
	opts := ConfigOptions{
		Servers:        []model.Server{entry, processor, exit},
		Inbounds:       []model.Inbound{root},
		ProxyPaths:     []model.ProxyPath{chain, direct},
		ProxyPathSteps: steps,
		InboundUsers:   []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
	}

	plans, err := BuildProxyPathPlans(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || len(plans[0].PortForwards) != 1 || len(plans[1].PortForwards) != 1 {
		t.Fatalf("shared transparent plans = %#v", plans)
	}
	firstForward, secondForward := plans[0].PortForwards[0], plans[1].PortForwards[0]
	if firstForward.ID != secondForward.ID || firstForward.ListenPort != root.Port || firstForward.TargetPort != secondForward.TargetPort || !sameTrustedForwardSender(firstForward.TrustedForward, secondForward.TrustedForward) {
		t.Fatalf("transparent prefix was not shared: first=%#v second=%#v", firstForward, secondForward)
	}
	forwards, err := DerivedPortForwardsFromProxyPaths(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
	if err != nil || len(forwards) != 1 {
		t.Fatalf("derived shared forwards = %#v, err=%v", forwards, err)
	}

	entryConfig := mustServerConfig(t, entry, opts.Inbounds, []model.User{user}, opts)
	if findInbound(entryConfig, tag("in", root.ID)) != nil {
		t.Fatalf("entry server retained the decrypted protocol listener: %s", entryConfig)
	}
	processorConfig := mustServerConfig(t, processor, opts.Inbounds, []model.User{user}, opts)
	processingTag := proxyPathSharedTransparentInboundTag(root.ID, 1)
	parsed := parseSingBoxConfig(t, processorConfig)
	processingCount := 0
	for _, inbound := range parsed.Inbounds {
		if inbound["tag"] == processingTag {
			processingCount++
		}
	}
	if processingCount != 1 || parsed.OBoard == nil || parsed.OBoard.TrustedForward == nil || len(parsed.OBoard.TrustedForward.Receivers) != 1 {
		t.Fatalf("processor did not emit one shared processing surface: %s", processorConfig)
	}
	chainUser := proxyPathBranchUser(chain, root, user).Username
	directUser := proxyPathBranchUser(direct, root, user).Username
	if !hasInbound(processorConfig, processingTag, chainUser) || !hasInbound(processorConfig, processingTag, directUser) {
		t.Fatalf("shared processing inbound is missing branch users: %s", processorConfig)
	}
	if !hasAuthUserRoute(processorConfig, processingTag, proxyPathStepTag(chain.ID, 2), chainUser) {
		t.Fatalf("processor is missing the HKT branch route: %s", processorConfig)
	}
	if !hasAuthUserRoute(processorConfig, processingTag, "direct", directUser) {
		t.Fatalf("processor is missing the direct branch route: %s", processorConfig)
	}

	singleForwards, err := DerivedPortForwardsFromProxyPaths([]model.ProxyPath{chain}, steps[:2], opts.Servers, opts.Inbounds)
	if err != nil || len(singleForwards) != 1 || singleForwards[0].ID != forwards[0].ID || singleForwards[0].TargetPort != forwards[0].TargetPort || !sameTrustedForwardSender(singleForwards[0].TrustedForward, forwards[0].TrustedForward) {
		t.Fatalf("shared resource changed after removing one branch: before=%#v after=%#v err=%v", forwards, singleForwards, err)
	}
}

func TestTransparentPrefixBranchesRejectIncompatibleForks(t *testing.T) {
	entry := model.Server{ID: 1, Name: "entry"}
	processorA := model.Server{ID: 2, Name: "processor-a"}
	processorB := model.Server{ID: 3, Name: "processor-b"}
	root := model.Inbound{ID: 17, ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	first := model.ProxyPath{ID: 1, Name: "first", InboundID: root.ID, Enabled: true}
	second := model.ProxyPath{ID: 2, Kind: model.ProxyPathKindDirect, Name: "second", InboundID: root.ID, Enabled: true}
	aID, bID := processorA.ID, processorB.ID
	firstStep := model.ProxyPathStep{ID: 11, PathID: first.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &aID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`}

	t.Run("different processor", func(t *testing.T) {
		secondStep := model.ProxyPathStep{ID: 21, PathID: second.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`}
		if _, err := BuildProxyPathPlans([]model.ProxyPath{first, second}, []model.ProxyPathStep{firstStep, secondStep}, []model.Server{entry, processorA, processorB}, []model.Inbound{root}); err == nil || !strings.Contains(err.Error(), "完全相同的透明转发前缀") {
			t.Fatalf("different transparent processors error = %v", err)
		}
	})

	t.Run("root direct", func(t *testing.T) {
		if _, err := BuildProxyPathPlans([]model.ProxyPath{first, second}, []model.ProxyPathStep{firstStep}, []model.Server{entry, processorA}, []model.Inbound{root}); err == nil || !strings.Contains(err.Error(), "所有启用分支") {
			t.Fatalf("pre-processing fork error = %v", err)
		}
	})

	t.Run("single-user protocol", func(t *testing.T) {
		singleUserRoot := root
		singleUserRoot.Protocol = model.ProtocolSS
		singleUserRoot.ConfigJSON = `{"method":"aes-128-gcm","password":"password"}`
		secondStep := firstStep
		secondStep.ID, secondStep.PathID = 21, second.ID
		if _, err := BuildProxyPathPlans([]model.ProxyPath{first, second}, []model.ProxyPathStep{firstStep, secondStep}, []model.Server{entry, processorA}, []model.Inbound{singleUserRoot}); err == nil || !strings.Contains(err.Error(), "多个用户名") {
			t.Fatalf("single-user transparent branches error = %v", err)
		}
	})
}

func hasAuthUserRoute(raw, inboundTag, outboundTag, username string) bool {
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return false
	}
	for _, rule := range mapList(parsed.Route["rules"]) {
		if rule["outbound"] != outboundTag {
			continue
		}
		if !containsString(stringList(rule["inbound"]), inboundTag) {
			continue
		}
		if containsString(stringList(rule["auth_user"]), username) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSyntheticProxyPathIDsUseDisjointFields(t *testing.T) {
	// Step IDs come from a global autoincrement. A decimal layout carried a large
	// step ID into the neighbouring path's range, so two paths derived one shared
	// forward. Disjoint bit fields must keep these distinct.
	// Under the previous decimal layout (base + kind*1e9 + pathID*1e4 + stepID),
	// step ID 10000 on path 1 landed exactly on path 2 step 0.
	if a, b := syntheticProxyPathID(1, 10000, 10), syntheticProxyPathID(2, 0, 10); a == b {
		t.Fatalf("path/step fields collide: %d", a)
	}
	seen := map[int64]string{}
	for _, pathID := range []int64{1, 2, 3, 999, 100000} {
		for _, stepID := range []int64{0, 1, 9999, 10000, 20000, 123456} {
			id := syntheticProxyPathID(pathID, stepID, 10)
			key := fmt.Sprintf("path=%d step=%d", pathID, stepID)
			if previous, ok := seen[id]; ok {
				t.Fatalf("id %d shared by %s and %s", id, previous, key)
			}
			seen[id] = key
			if id >= 1<<53 {
				t.Fatalf("%s produced %d, which loses precision as JSON", key, id)
			}
		}
	}
	// Generated inbound IDs are negative and must not overlap between the shared
	// service range and the per-hop range.
	if internal, chain := proxyPathInternalOutboundID(100000, 1), proxyPathChainServiceID(proxyPathChainServiceKey{ServerID: 100000, Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod}); internal == chain {
		t.Fatalf("internal and chain ranges overlap at %d", internal)
	}
	if a, b := proxyPathInternalOutboundID(1, 5000), proxyPathInternalOutboundID(2, 1); a == b {
		t.Fatalf("internal inbound path/position fields collide: %d", a)
	}
}

func TestTransparentPathForwardTargetsGeneratedProcessingPort(t *testing.T) {
	// The derived forward and the processing server's generated listener are
	// produced by two different builders. They must agree on the port, otherwise
	// the forward relays into a port nobody listens on.
	front := model.Server{ID: 1, Name: "front", ChainSecret: "chain-1", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	back := model.Server{ID: 2, Name: "back", ChainSecret: "chain-2", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 11, ServerID: front.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 101, Name: "front-back", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	backID := back.ID
	steps := []model.ProxyPathStep{
		{ID: 1001, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &backID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`},
	}
	users := []model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "alice-password"}}
	opts := ConfigOptions{
		Servers:        []model.Server{front, back},
		Inbounds:       []model.Inbound{root},
		ProxyPaths:     []model.ProxyPath{path},
		ProxyPathSteps: steps,
		InboundUsers:   []model.InboundUser{{InboundID: root.ID, UserID: users[0].ID, Enabled: true}},
	}

	forwards, err := DerivedPortForwardsFromProxyPaths(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(forwards) != 1 {
		t.Fatalf("derived forwards = %d, want 1", len(forwards))
	}
	if forwards[0].ListenPort != root.Port {
		t.Fatalf("forward should listen on the public entry port: %#v", forwards[0])
	}

	configBack := mustServerConfig(t, back, opts.Inbounds, users, opts)
	processing := findInbound(configBack, proxyPathSharedTransparentInboundTag(root.ID, 1))
	if processing == nil {
		t.Fatalf("processing inbound missing on back server: %s", configBack)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(configBack), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OBoard == nil || parsed.OBoard.TrustedForward == nil || len(parsed.OBoard.TrustedForward.Receivers) != 1 {
		t.Fatalf("trusted receiver missing from processing server: %s", configBack)
	}
	receiver := parsed.OBoard.TrustedForward.Receivers[0]
	if receiver.ListenPort != forwards[0].TargetPort || receiver.TargetPort != intFromAny(processing["listen_port"]) {
		t.Fatalf("forward target %d does not match trusted receiver %#v and inner listener %#v", forwards[0].TargetPort, receiver, processing)
	}
	// The front server must not keep a user-protocol listener on that port.
	if front := mustServerConfig(t, front, opts.Inbounds, users, opts); findInbound(front, tag("in", root.ID)) != nil {
		t.Fatalf("front server must hand its entry port to the managed forward: %s", front)
	}
}

func TestDisabledProxyPathDoesNotReserveGeneratedPorts(t *testing.T) {
	// Enabling or disabling one branch must not shift the ports another branch
	// receives, otherwise a routine toggle silently rewrites unrelated config.
	source := model.Server{ID: 1, Name: "src", ChainSecret: "chain-1", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "dst", ChainSecret: "chain-2", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 11, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	other := model.Inbound{ID: 12, ServerID: source.ID, Name: "entry-2", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true}
	live := model.ProxyPath{ID: 101, Name: "live", InboundID: root.ID, Secret: "live", Enabled: true}
	targetID := target.ID
	liveSteps := []model.ProxyPathStep{
		{ID: 1001, PathID: live.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
	}
	servers := []model.Server{source, target}
	inbounds := []model.Inbound{root, other}

	baseline, err := DerivedTunnelsFromProxyPaths([]model.ProxyPath{live}, liveSteps, servers, inbounds)
	if err != nil {
		t.Fatal(err)
	}
	baselineServices, err := buildProxyPathChainServices([]model.ProxyPath{live}, liveSteps, servers, inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}

	disabled := model.ProxyPath{ID: 102, Name: "disabled", InboundID: other.ID, Secret: "disabled", Enabled: false}
	withDisabled := append(append([]model.ProxyPathStep(nil), liveSteps...), model.ProxyPathStep{ID: 2001, PathID: disabled.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`})
	afterServices, err := buildProxyPathChainServices([]model.ProxyPath{live, disabled}, withDisabled, servers, inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, service := range baselineServices {
		after, ok := afterServices[key]
		if !ok {
			t.Fatalf("shared service %#v disappeared once a disabled path existed", key)
		}
		if after.Inbound.Port != service.Inbound.Port {
			t.Fatalf("disabled path moved shared port from %d to %d", service.Inbound.Port, after.Inbound.Port)
		}
	}
	afterTunnels, err := DerivedTunnelsFromProxyPaths([]model.ProxyPath{live, disabled}, withDisabled, servers, inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterTunnels) != len(baseline) {
		t.Fatalf("disabled path contributed %d tunnels", len(afterTunnels)-len(baseline))
	}
}

func TestGeneratedPortSkipsDisabledInboundPort(t *testing.T) {
	// Allocation is deterministic, so handing a disabled inbound's port to a
	// generated listener would create a conflict the operator cannot fix by
	// re-enabling that inbound.
	server := model.Server{ID: 1, Name: "s", ChainSecret: "chain-1", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30001}
	disabled := model.Inbound{ID: 21, ServerID: server.ID, Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 30000, ConfigJSON: `{}`, Enabled: false}
	inbounds := map[int64]model.Inbound{disabled.ID: disabled}
	for attempt := 0; attempt < 4; attempt++ {
		port := proxyPathAvailablePortForProtocol(server, int64(attempt), 0, 30000, 30001, model.ForwardProtocolTCP, "0.0.0.0", inbounds)
		if port == disabled.Port {
			t.Fatalf("generated listener took the disabled inbound's port %d", port)
		}
	}
}

func findInbound(raw, tag string) map[string]any {
	var parsed SingBoxConfig
	_ = json.Unmarshal([]byte(raw), &parsed)
	for _, inbound := range parsed.Inbounds {
		if inbound["tag"] == tag {
			return inbound
		}
	}
	return nil
}

func TestPortLedgerKeepsAllocatedPortsStableAcrossTopologyChanges(t *testing.T) {
	// The whole point of persisting ports: adding an unrelated inbound on the
	// target must not move a listener that is already deployed.
	source := model.Server{ID: 1, Name: "src", ChainSecret: "chain-1", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "dst", ChainSecret: "chain-2", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 11, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 101, Name: "src-dst", InboundID: root.ID, Secret: "seed", Enabled: true}
	targetID := target.ID
	steps := []model.ProxyPathStep{
		{ID: 1001, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
	}
	servers := []model.Server{source, target}

	first := NewProxyPathPortLedger(nil)
	services, err := buildProxyPathChainServices([]model.ProxyPath{path}, steps, servers, []model.Inbound{root}, first)
	if err != nil {
		t.Fatal(err)
	}
	key := proxyPathChainServiceKey{ServerID: target.ID, Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod}
	originalPort := services[key].Inbound.Port
	if originalPort == 0 {
		t.Fatal("no port was allocated")
	}
	pending := first.Pending()
	if len(pending) != 1 || pending[0].Kind != model.ProxyPathPortKindChainService || pending[0].Port != originalPort || pending[0].ServerID != target.ID {
		t.Fatalf("pending allocation = %#v", pending)
	}

	// Occupy the allocated port with a new inbound and re-project from the stored
	// allocation. Without persistence the allocator would skip to another port.
	squatter := model.Inbound{ID: 21, ServerID: target.ID, Name: "new", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: originalPort, ConfigJSON: `{}`, Enabled: true}
	stored := []model.ProxyPathPortAllocation{{ID: 7, Kind: pending[0].Kind, ScopeKey: pending[0].ScopeKey, ServerID: pending[0].ServerID, Port: originalPort}}
	second := NewProxyPathPortLedger(stored)
	services, err = buildProxyPathChainServices([]model.ProxyPath{path}, steps, servers, []model.Inbound{root, squatter}, second)
	if err != nil {
		t.Fatal(err)
	}
	if got := services[key].Inbound.Port; got != originalPort {
		t.Fatalf("stored port moved from %d to %d", originalPort, got)
	}
	// The re-projection must not pick another port. It may emit one metadata-only
	// correction: the fixture row predates the pool column, so the ledger fills
	// in pool/network/listen metadata while keeping the port untouched.
	extra := second.Pending()
	if len(extra) > 1 {
		t.Fatalf("re-projection allocated again: %#v", extra)
	}
	if len(extra) == 1 {
		item := extra[0]
		if item.Port != originalPort || item.Pool != model.PortPoolPublic || item.Network != "tcp_udp" || item.ListenIP != "0.0.0.0" {
			t.Fatalf("metadata correction = %#v, want same port with public pool metadata", item)
		}
	}
	// The record is still claimed, so it must not be released.
	if stale := StaleProxyPathPortAllocationIDs(stored, second); len(stale) != 0 {
		t.Fatalf("claimed allocation reported stale: %#v", stale)
	}
}

func TestStaleProxyPathPortAllocationsNeedACompleteProjection(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{
		{ID: 1, Kind: model.ProxyPathPortKindChainService, ScopeKey: DefaultProxyPathChainMethod, ServerID: 9, Port: 31000},
		{ID: 2, Kind: model.ProxyPathPortKindTunnelWG, ScopeKey: "555", ServerID: 9, Port: 31500},
	}

	// A projection that aborted resolved only part of the topology. Treating that
	// partial view as authoritative would release ports that are still deployed.
	partial := NewProxyPathPortLedger(stored)
	partial.resolve(PortRequirement{Kind: model.ProxyPathPortKindChainService, ScopeKey: DefaultProxyPathChainMethod, ServerID: 9, Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCPUDP, Allocate: func() int { return 31000 }})
	if stale := StaleProxyPathPortAllocationIDs(stored, partial); len(stale) != 0 {
		t.Fatalf("incomplete projection released %#v", stale)
	}

	// Once the projection completed, whatever it did not claim is genuinely free.
	complete := NewProxyPathPortLedger(stored)
	complete.resolve(PortRequirement{Kind: model.ProxyPathPortKindChainService, ScopeKey: DefaultProxyPathChainMethod, ServerID: 9, Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCPUDP, Allocate: func() int { return 31000 }})
	complete.markProjectionComplete()
	stale := StaleProxyPathPortAllocationIDs(stored, complete)
	if len(stale) != 1 || stale[0] != 2 {
		t.Fatalf("stale ids = %#v, want only the unclaimed WireGuard record", stale)
	}
}

func TestSSHTunnelIdentityStaysStableWhenTargetGainsInbound(t *testing.T) {
	// The SSH reuse key embeds the target service port, so before ports were
	// persisted an unrelated inbound on the target could move that port, change
	// the reuse key, and silently rotate the tunnel ID and its key pair.
	source := model.Server{ID: 1, Name: "src", ChainSecret: "chain-1", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "dst", ChainSecret: "chain-2", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 11, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 101, Name: "src-dst", InboundID: root.ID, Secret: "seed", Enabled: true}
	targetID := target.ID
	steps := []model.ProxyPathStep{
		{ID: 1001, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh","ssh_port":22}`},
	}
	servers := []model.Server{source, target}

	ledger := NewProxyPathPortLedger(nil)
	before, err := DerivedTunnelsFromProxyPathsWithLedger([]model.ProxyPath{path}, steps, servers, []model.Inbound{root}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("tunnel count = %d", len(before))
	}
	stored := make([]model.ProxyPathPortAllocation, 0, len(ledger.Pending()))
	for index, item := range ledger.Pending() {
		item.ID = int64(index + 1)
		stored = append(stored, item)
	}

	// A new inbound occupies the shared service port that the first projection
	// picked. With the allocation persisted the tunnel identity must not move.
	chainPort := 0
	services, err := buildProxyPathChainServices([]model.ProxyPath{path}, steps, servers, []model.Inbound{root}, NewProxyPathPortLedger(stored))
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range services {
		chainPort = service.Inbound.Port
	}
	if chainPort == 0 {
		t.Fatal("shared service port not allocated")
	}
	squatter := model.Inbound{ID: 21, ServerID: target.ID, Name: "new", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: chainPort, ConfigJSON: `{}`, Enabled: true}
	after, err := DerivedTunnelsFromProxyPathsWithLedger([]model.ProxyPath{path}, steps, servers, []model.Inbound{root, squatter}, NewProxyPathPortLedger(stored))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("tunnel count after change = %d", len(after))
	}
	if before[0].ID != after[0].ID {
		t.Fatalf("tunnel ID rotated: %d -> %d", before[0].ID, after[0].ID)
	}
	if before[0].ConfigJSON != after[0].ConfigJSON {
		t.Fatalf("tunnel credentials rotated:\nbefore=%s\nafter=%s", before[0].ConfigJSON, after[0].ConfigJSON)
	}
	if before[0].ListenPort != after[0].ListenPort {
		t.Fatalf("loopback listener moved: %d -> %d", before[0].ListenPort, after[0].ListenPort)
	}
}

func TestTrustedForwardIsEmittedOnlyByPublicEntryHop(t *testing.T) {
	entry := model.Server{ID: 1, Name: "entry", ChainSecret: "entry-secret", PublicIPv4: "198.51.100.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	relay := model.Server{ID: 2, Name: "relay", ChainSecret: "relay-secret", PublicIPv4: "198.51.100.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	processor := model.Server{ID: 3, Name: "processor", ChainSecret: "processor-secret", PublicIPv4: "198.51.100.3", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 32000, PortRangeEnd: 32100}
	root := model.Inbound{ID: 10, ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 20, Name: "entry-relay-processor", InboundID: root.ID, Enabled: true}
	relayID, processorID := relay.ID, processor.ID
	steps := []model.ProxyPathStep{
		{ID: 21, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &relayID, TransportMode: model.ProxyPathTransportPortForward},
		{ID: 22, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &processorID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true},
	}
	plans, err := BuildProxyPathPlans([]model.ProxyPath{path}, steps, []model.Server{entry, relay, processor}, []model.Inbound{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].PortForwards) != 2 {
		t.Fatalf("plans = %#v", plans)
	}
	if plans[0].PortForwards[0].TrustedForward == nil || plans[0].PortForwards[1].TrustedForward != nil {
		t.Fatalf("trusted sender must exist only on public entry hop: %#v", plans[0].PortForwards)
	}
	required := TrustedForwardServerIDs([]model.ProxyPath{path}, steps, []model.Inbound{root})
	if !required[entry.ID] || !required[relay.ID] || !required[processor.ID] || len(required) != 3 {
		t.Fatalf("trusted forward build gate servers = %#v", required)
	}
}

func TestLedgerRecordsPoolAndNetworkMetadata(t *testing.T) {
	ledger := NewProxyPathPortLedger(nil)
	port := ledger.resolve(PortRequirement{
		Kind: model.ProxyPathPortKindTunnelSSH, ScopeKey: "42", ServerID: 3,
		Pool: model.PortPoolInternal, ListenIP: "127.0.0.1", Network: model.ForwardProtocolTCP,
		Allocate: func() int { return 41000 },
	})
	if port != 41000 {
		t.Fatalf("port = %d", port)
	}
	pending := ledger.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %#v", pending)
	}
	item := pending[0]
	if item.Pool != model.PortPoolInternal || item.ListenIP != "127.0.0.1" || item.Network != "tcp" {
		t.Fatalf("allocation metadata = %#v", item)
	}
	if item.Generation != 1 || item.Ordinal != 0 || item.State != model.PortAllocationStateActive {
		t.Fatalf("allocation lifecycle = %#v", item)
	}
}

func TestLedgerConvergesLegacyRowMetadataWithoutMovingPort(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{{ID: 9, Kind: model.ProxyPathPortKindTrustedInner, ScopeKey: "7:2", ServerID: 1, Port: 40010}}
	ledger := NewProxyPathPortLedger(stored)
	port := ledger.resolve(PortRequirement{
		Kind: model.ProxyPathPortKindTrustedInner, ScopeKey: "7:2", ServerID: 1,
		Pool: model.PortPoolInternal, ListenIP: "127.0.0.1", Network: model.ForwardProtocolTCP,
		Allocate: func() int { t.Fatal("stored port must not reallocate"); return 0 },
	})
	if port != 40010 {
		t.Fatalf("stored port moved to %d", port)
	}
	pending := ledger.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %#v, want one metadata correction", pending)
	}
	item := pending[0]
	if item.Port != 40010 || item.Pool != model.PortPoolInternal || item.ListenIP != "127.0.0.1" || item.Network != "tcp" {
		t.Fatalf("metadata correction = %#v", item)
	}
	if stale := StaleProxyPathPortAllocationIDs(stored, ledger); len(stale) != 0 {
		t.Fatalf("claimed allocation reported stale: %#v", stale)
	}
	ledger.markProjectionComplete()
	if stale := StaleProxyPathPortAllocationIDs(stored, ledger); len(stale) != 0 {
		t.Fatalf("metadata-corrected allocation reported stale: %#v", stale)
	}
}

func TestLedgerKeepsStoredPortWhenMetadataMatches(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{{ID: 9, Kind: model.ProxyPathPortKindTunnelWG, ScopeKey: "42", ServerID: 2, Pool: model.PortPoolPublic, ListenIP: "*", Network: "udp", Port: 33000}}
	ledger := NewProxyPathPortLedger(stored)
	port := ledger.resolve(PortRequirement{
		Kind: model.ProxyPathPortKindTunnelWG, ScopeKey: "42", ServerID: 2,
		Pool: model.PortPoolPublic, ListenIP: "*", Network: model.ForwardProtocolUDP,
		Allocate: func() int { t.Fatal("stored port must not reallocate"); return 0 },
	})
	if port != 33000 {
		t.Fatalf("port = %d", port)
	}
	if pending := ledger.Pending(); len(pending) != 0 {
		t.Fatalf("matching metadata produced an upsert: %#v", pending)
	}
}

func TestChainServicePublicRangeExhaustionFailsWithoutOverflow(t *testing.T) {
	// A one-port server whose only public port is taken by a user inbound must
	// fail the chain-service projection instead of silently binding outside the
	// configured auto range.
	source := model.Server{ID: 1, Name: "src", ChainSecret: "chain-1", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30000}
	target := model.Server{ID: 2, Name: "dst", ChainSecret: "chain-2", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31000}
	root := model.Inbound{ID: 11, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	occupied := model.Inbound{ID: 22, ServerID: target.ID, Name: "squatter", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 31000, ConfigJSON: `{"method":"2022-blake3-aes-128-gcm"}`, Enabled: true}
	path := model.ProxyPath{ID: 101, Name: "src-dst", InboundID: root.ID, Secret: "seed", Enabled: true}
	targetID := target.ID
	steps := []model.ProxyPathStep{
		{ID: 1001, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
	}
	ledger := NewProxyPathPortLedger(nil)
	_, err := buildProxyPathChainServices([]model.ProxyPath{path}, steps, []model.Server{source, target}, []model.Inbound{root, occupied}, ledger)
	if err == nil {
		t.Fatal("chain service projection must fail when the managed public range is exhausted")
	}
	if pending := ledger.Pending(); len(pending) != 0 {
		t.Fatalf("failed projection must not leave pending allocations: %#v", pending)
	}
}
