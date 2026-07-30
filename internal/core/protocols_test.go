package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"golang.org/x/crypto/ssh"
)

func TestProtocolAdaptersGenerateSingBoxBlocks(t *testing.T) {
	users := []model.User{{Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}}
	protocols := []model.Protocol{model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS}
	for _, protocol := range protocols {
		adapter, err := AdapterFor(protocol)
		if err != nil {
			t.Fatal(err)
		}
		inbound := model.Inbound{ID: 1, ServerID: 1, Name: string(protocol), Protocol: protocol, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: testInboundConfig(protocol), Enabled: true}
		block, err := adapter.Inbound(inbound, users)
		if err != nil {
			t.Fatalf("%s inbound: %v", protocol, err)
		}
		if block["tag"] == "" || block["type"] == "" {
			t.Fatalf("%s inbound missing type/tag: %#v", protocol, block)
		}
		outbound := model.Outbound{ID: 2, ServerID: 1, Name: string(protocol), Protocol: protocol, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: `{}`, Enabled: true}
		out, err := adapter.Outbound(outbound, &users[0])
		if err != nil {
			t.Fatalf("%s outbound: %v", protocol, err)
		}
		if out["server"] != "example.com" {
			t.Fatalf("%s outbound target mismatch: %#v", protocol, out)
		}
	}
}

func TestConnectionAuditMetadataIsOnlyEmittedWhenEnabled(t *testing.T) {
	disabled, err := GenerateServerConfigWithOptions(model.Server{ID: 1, Name: "edge"}, nil, nil, nil, nil, ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(disabled, "connection_audit") {
		t.Fatalf("disabled server emitted connection audit metadata: %s", disabled)
	}
	enabled, err := GenerateServerConfigWithOptions(model.Server{ID: 1, Name: "edge", ConnectionAuditEnabled: true}, nil, nil, nil, nil, ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enabled, `"connection_audit"`) || !strings.Contains(enabled, `"enabled": true`) {
		t.Fatalf("enabled server omitted connection audit metadata: %s", enabled)
	}
}

func TestProtocolOutboundCustomCredentialsOverrideUserDefaults(t *testing.T) {
	user := &model.User{ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "user-password"}
	cases := []struct {
		name     string
		protocol model.Protocol
		config   string
		key      string
		want     any
	}{
		{name: "vless uuid", protocol: model.ProtocolVLESS, config: `{"uuid":"22222222-2222-4222-8222-222222222222"}`, key: "uuid", want: "22222222-2222-4222-8222-222222222222"},
		{name: "hy2 password", protocol: model.ProtocolHY2, config: `{"password":"hy2-custom"}`, key: "password", want: "hy2-custom"},
		{name: "anytls password", protocol: model.ProtocolAnyTLS, config: `{"password":"anytls-custom"}`, key: "password", want: "anytls-custom"},
		{name: "ss password", protocol: model.ProtocolSS, config: `{"method":"2022-blake3-aes-128-gcm","password":"ss-custom"}`, key: "password", want: normalizeSS2022Key("ss-custom", "2022-blake3-aes-128-gcm")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := AdapterFor(tt.protocol)
			if err != nil {
				t.Fatal(err)
			}
			out, err := adapter.Outbound(model.Outbound{ID: 1, Protocol: tt.protocol, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: tt.config, Enabled: true}, user)
			if err != nil {
				t.Fatal(err)
			}
			if out[tt.key] != tt.want {
				t.Fatalf("%s = %v, want %v in %#v", tt.key, out[tt.key], tt.want, out)
			}
		})
	}
}

func TestExternalRawOutboundStripsPrivateMetadata(t *testing.T) {
	serverID := int64(1)
	config, err := GenerateServerConfigWithOptions(
		model.Server{ID: serverID, Name: "edge"},
		nil,
		nil,
		nil,
		nil,
		ConfigOptions{ExternalOutbounds: []model.ExternalOutbound{{ID: 9, Name: "raw", Protocol: model.ProtocolVLESS, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: `{"type":"vless","server":"example.com","server_port":443,"uuid":"22222222-2222-4222-8222-222222222222","username":"legacy","_oboard":{"username":"node-a"}}`, Enabled: true, ServerID: &serverID}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, out := range parsed.Outbounds {
		if out["tag"] == "ext-9" {
			if _, ok := out["_oboard"]; ok {
				t.Fatalf("private _oboard leaked into sing-box outbound: %#v", out)
			}
			if _, ok := out["username"]; ok {
				t.Fatalf("legacy username leaked into sing-box outbound: %#v", out)
			}
			return
		}
	}
	t.Fatalf("external outbound ext-9 not found: %#v", parsed.Outbounds)
}

func TestExternalSocksOutboundAndProxyPathDetour(t *testing.T) {
	server1 := model.Server{ID: 1, Name: "edge-a", PublicIPv4: "203.0.113.1", IPStack: model.IPStackPreferIPv4}
	server2 := model.Server{ID: 2, Name: "edge-b", PublicIPv4: "203.0.113.2", IPStack: model.IPStackPreferIPv4}
	rootInbound := model.Inbound{ID: 10, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	targetInbound := model.Inbound{ID: 20, ServerID: 2, Name: "next", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true}
	external := model.ExternalOutbound{ID: 30, Name: "socks-a", Protocol: model.ProtocolSocks, TargetAddress: "socks.example.com", TargetPort: 1080, ConfigJSON: `{"type":"socks","server":"socks.example.com","server_port":1080,"username":"u","password":"p"}`, Enabled: true}
	path := model.ProxyPath{ID: 40, Name: "entry-via-socks", InboundID: rootInbound.ID, Secret: "path-secret", Enabled: true}
	targetID := targetInbound.ID
	externalID := external.ID
	opts := ConfigOptions{
		Servers:           []model.Server{server1, server2},
		Inbounds:          []model.Inbound{rootInbound, targetInbound},
		ExternalOutbounds: []model.ExternalOutbound{external},
		ProxyPaths:        []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
			{ID: 2, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, InboundID: &targetID},
		},
		InboundUsers: []model.InboundUser{{InboundID: rootInbound.ID, UserID: 1, Enabled: true}},
	}
	config, err := GenerateServerConfigWithOptions(server1, []model.Inbound{rootInbound, targetInbound}, nil, nil, []model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	var foundSocks, foundTarget, foundRoute bool
	for _, outbound := range parsed.Outbounds {
		if outbound["tag"] == "path-40-step-1" {
			foundSocks = outbound["type"] == "socks" && outbound["username"] == "u"
		}
		if outbound["tag"] == "path-40-step-2" {
			foundTarget = outbound["type"] == "vless" && outbound["detour"] == "path-40-step-1" && outbound["server"] == "203.0.113.2"
		}
	}
	for _, rule := range mapList(parsed.Route["rules"]) {
		if rule["outbound"] == "path-40-step-2" {
			foundRoute = true
		}
	}
	if !foundSocks || !foundTarget || !foundRoute {
		t.Fatalf("proxy path config missing expected chain: socks=%v target=%v route=%v config=%s", foundSocks, foundTarget, foundRoute, config)
	}

	targetConfig, err := GenerateServerConfigWithOptions(server2, []model.Inbound{rootInbound, targetInbound}, nil, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(targetConfig, "__oboard_path_40_inbound_20") {
		t.Fatalf("target inbound missing hidden path user: %s", targetConfig)
	}
}

func TestProxyPathServerOnlyMultiHopUsesSharedShadowsocksInbounds(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	serverC := model.Server{ID: 3, Name: "C", PublicIPv4: "203.0.113.3", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 32000, PortRangeEnd: 32100}
	rootInbound := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	external := model.ExternalOutbound{ID: 30, Name: "socks-p", Protocol: model.ProtocolSocks, TargetAddress: "socks.example.com", TargetPort: 1080, ConfigJSON: `{"type":"socks","server":"socks.example.com","server_port":1080}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Name: "A-B-C-P", InboundID: rootInbound.ID, Secret: "path-secret", Enabled: true}
	serverBID, serverCID, externalID := serverB.ID, serverC.ID, external.ID
	opts := ConfigOptions{
		Servers:           []model.Server{serverA, serverB, serverC},
		Inbounds:          []model.Inbound{rootInbound},
		ExternalOutbounds: []model.ExternalOutbound{external},
		ProxyPaths:        []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
			{ID: 2, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverCID},
			{ID: 3, PathID: path.ID, Position: 3, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
		},
		InboundUsers: []model.InboundUser{{InboundID: rootInbound.ID, UserID: 1, Enabled: true}},
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}

	configA := mustServerConfig(t, serverA, []model.Inbound{rootInbound}, []model.User{user}, opts)
	if out := findOutbound(configA, "path-50-step-1"); out["type"] != "shadowsocks" || out["method"] != DefaultProxyPathChainMethod || out["server"] != serverB.PublicIPv4 || intFromAny(out["server_port"]) == 0 {
		t.Fatalf("A should route root inbound to B shared Shadowsocks inbound, got %#v in %s", out, configA)
	}
	if !hasRoute(configA, "in-10", "path-50-step-1") {
		t.Fatalf("A missing route from root inbound to first hop: %s", configA)
	}

	configB := mustServerConfig(t, serverB, []model.Inbound{rootInbound}, nil, opts)
	sharedTag := proxyPathChainServiceTag(DefaultProxyPathChainMethod)
	if !hasInbound(configB, sharedTag, "__oboard_path_50_step_1") {
		t.Fatalf("B missing shared internal inbound for step 1: %s", configB)
	}
	if out := findOutbound(configB, "path-50-step-2"); out["type"] != "shadowsocks" || out["server"] != serverC.PublicIPv4 || intFromAny(out["server_port"]) == 0 {
		t.Fatalf("B should route to C shared internal inbound, got %#v in %s", out, configB)
	}
	if !hasRoute(configB, sharedTag, "path-50-step-2") {
		t.Fatalf("B missing route from shared inbound to step 2: %s", configB)
	}

	configC := mustServerConfig(t, serverC, []model.Inbound{rootInbound}, nil, opts)
	if !hasInbound(configC, sharedTag, "__oboard_path_50_step_2") {
		t.Fatalf("C missing shared internal inbound for step 2: %s", configC)
	}
	if out := findOutbound(configC, "path-50-step-3"); out["type"] != "socks" || out["server"] != external.TargetAddress {
		t.Fatalf("C should route to imported SOCKS exit, got %#v in %s", out, configC)
	}
	if !hasRoute(configC, sharedTag, "path-50-step-3") {
		t.Fatalf("C missing route from shared inbound to SOCKS exit: %s", configC)
	}
}

func TestIntermediateDirectBranchRoutesAtItsSourceServer(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	serverC := model.Server{ID: 3, Name: "C", PublicIPv4: "203.0.113.3", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 32000, PortRangeEnd: 32100}
	rootInbound := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	chain := model.ProxyPath{ID: 50, Name: "A-B-C", InboundID: rootInbound.ID, Secret: "chain-secret", Enabled: true}
	direct := model.ProxyPath{ID: 51, Kind: model.ProxyPathKindDirect, Name: "A-B-Direct", InboundID: rootInbound.ID, Secret: "direct-secret", Enabled: true}
	directC := model.ProxyPath{ID: 52, Kind: model.ProxyPathKindDirect, Name: "A-B-C-Direct", InboundID: rootInbound.ID, Secret: "direct-c-secret", Enabled: true}
	serverBID, serverCID := serverB.ID, serverC.ID
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	opts := ConfigOptions{
		Servers:    []model.Server{serverA, serverB, serverC},
		Inbounds:   []model.Inbound{rootInbound},
		ProxyPaths: []model.ProxyPath{chain, direct, directC},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 1, PathID: chain.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
			{ID: 2, PathID: chain.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverCID},
			{ID: 3, PathID: direct.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
			{ID: 4, PathID: directC.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
			{ID: 5, PathID: directC.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverCID},
		},
		InboundUsers: []model.InboundUser{{InboundID: rootInbound.ID, UserID: user.ID, Enabled: true}},
	}

	configA := mustServerConfig(t, serverA, []model.Inbound{rootInbound}, []model.User{user}, opts)
	if !hasRoute(configA, "in-10", "path-50-step-1") || !hasRoute(configA, "in-10", "path-51-step-1") || !hasRoute(configA, "in-10", "path-52-step-1") {
		t.Fatalf("A should preserve both downstream branches: %s", configA)
	}

	configB := mustServerConfig(t, serverB, []model.Inbound{rootInbound}, nil, opts)
	sharedTag := proxyPathChainServiceTag(DefaultProxyPathChainMethod)
	if !hasInbound(configB, sharedTag, "__oboard_path_50_step_1") || !hasInbound(configB, sharedTag, "__oboard_path_51_step_1") || !hasInbound(configB, sharedTag, "__oboard_path_52_step_1") {
		t.Fatalf("B should accept both branch identities: %s", configB)
	}
	parsed := parseSingBoxConfig(t, configB)
	var chainRoute, directRoute, secondHopRoute bool
	for _, rule := range mapList(parsed.Route["rules"]) {
		users := stringList(rule["auth_user"])
		if rule["outbound"] == "path-50-step-2" && len(users) == 1 && users[0] == "__oboard_path_50_step_1" {
			chainRoute = true
		}
		if rule["outbound"] == "direct" && len(users) == 1 && users[0] == "__oboard_path_51_step_1" {
			directRoute = true
		}
		if rule["outbound"] == "path-52-step-2" && len(users) == 1 && users[0] == "__oboard_path_52_step_1" {
			secondHopRoute = true
		}
	}
	if !chainRoute || !directRoute || !secondHopRoute {
		t.Fatalf("B should route the chain and C branch onward while keeping its own direct branch: chain=%v direct=%v second_hop=%v config=%s", chainRoute, directRoute, secondHopRoute, configB)
	}

	configC := mustServerConfig(t, serverC, []model.Inbound{rootInbound}, nil, opts)
	if !hasInbound(configC, sharedTag, "__oboard_path_52_step_2") {
		t.Fatalf("C should accept the two-hop direct branch identity: %s", configC)
	}
	parsedC := parseSingBoxConfig(t, configC)
	var cDirectRoute bool
	for _, rule := range mapList(parsedC.Route["rules"]) {
		users := stringList(rule["auth_user"])
		if rule["outbound"] == "direct" && len(users) == 1 && users[0] == "__oboard_path_52_step_2" {
			cDirectRoute = true
		}
	}
	if !cDirectRoute {
		t.Fatalf("C should terminate the two-hop branch with direct: %s", configC)
	}
	subscription, err := GenerateSubscriptionWithOptions(user, []model.Server{serverA, serverB, serverC}, []model.Inbound{rootInbound}, SubscriptionOptions{
		InboundUsers:   opts.InboundUsers,
		ProxyPaths:     opts.ProxyPaths,
		ProxyPathSteps: opts.ProxyPathSteps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subscription, "A｜C") || !strings.Contains(subscription, "A｜B｜直出") || !strings.Contains(subscription, "A｜B｜C｜直出") {
		t.Fatalf("subscription should preserve both chain and direct branches: %s", subscription)
	}
}

func TestResolveReachableEntryAddressUsesSourceStackAndHonorsExplicitMode(t *testing.T) {
	target := model.Server{ID: 2, Name: "target", PublicIPv4: "203.0.113.20", PublicIPv6: "2001:db8::20", EntryIPMode: model.EntryIPModeAuto}
	ipv4Source := model.Server{ID: 1, Name: "v4-source", PublicIPv4: "198.51.100.10", IPStack: model.IPStackIPv4Only}
	address, err := ResolveReachableEntryAddress(ipv4Source, model.Inbound{}, target)
	if err != nil || address != target.PublicIPv4 {
		t.Fatalf("IPv4 source address = %q, err=%v", address, err)
	}
	ipv6Source := model.Server{ID: 3, Name: "v6-source", PublicIPv6: "2001:db8::10", IPStack: model.IPStackIPv6Only}
	address, err = ResolveReachableEntryAddress(ipv6Source, model.Inbound{}, target)
	if err != nil || address != target.PublicIPv6 {
		t.Fatalf("IPv6 source address = %q, err=%v", address, err)
	}
	target.EntryIPMode = model.EntryIPModeIPv6
	if _, err := ResolveReachableEntryAddress(ipv4Source, model.Inbound{}, target); err == nil || !errors.Is(err, ErrInvalidDesiredState) || !strings.Contains(err.Error(), "IPv6") {
		t.Fatalf("explicit IPv6 mismatch error = %v", err)
	}
	_, err = GenerateServerConfigWithOptions(ipv4Source, nil, []model.Outbound{{ID: 1, ServerID: ipv4Source.ID, Name: "bad-v6", Protocol: model.ProtocolVLESS, TargetAddress: target.PublicIPv6, TargetPort: 443, Enabled: true}}, testDNSState(ipv4Source.ID), nil, ConfigOptions{})
	if err == nil || !errors.Is(err, ErrInvalidDesiredState) {
		t.Fatalf("generated config mismatch error = %v", err)
	}
}

func TestTransparentPortForwardMovesUserProtocolToProcessingServer(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "YT", PublicIPv4: "199.30.91.70", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 55777, PortRangeEnd: 55780}
	serverB := model.Server{ID: 2, Name: "WAWO", PublicIPv4: "2.27.109.100", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 443, PortRangeEnd: 20000}
	privateKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	rootInbound := model.Inbound{ID: 3, ServerID: serverA.ID, Name: "YT-vless-55778", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 55778, ConfigJSON: `{
  "flow":"xtls-rprx-vision",
  "tls":{
    "enabled":true,
    "server_name":"cdn.icloud-content.com",
    "reality":{
      "enabled":true,
      "handshake":{"server":"cdn.icloud-content.com","server_port":443},
      "private_key":"` + privateKey + `",
      "short_id":"abcd"
    }
  }
}`, Enabled: true}
	path := model.ProxyPath{ID: 1, Name: "YT to WAWO processing", InboundID: rootInbound.ID, Secret: "path-secret", Enabled: true}
	serverBID := serverB.ID
	step := model.ProxyPathStep{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ConfigJSON: `{}`}
	user := model.User{ID: 1, Username: "admin", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	opts := ConfigOptions{
		Servers:        []model.Server{serverA, serverB},
		Inbounds:       []model.Inbound{rootInbound},
		ProxyPaths:     []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{step},
		InboundUsers:   []model.InboundUser{{InboundID: rootInbound.ID, UserID: user.ID, Enabled: true}},
	}

	configA := mustServerConfig(t, serverA, []model.Inbound{rootInbound}, []model.User{user}, opts)
	var parsedA SingBoxConfig
	if err := json.Unmarshal([]byte(configA), &parsedA); err != nil {
		t.Fatal(err)
	}
	for _, inbound := range parsedA.Inbounds {
		if intFromAny(inbound["listen_port"]) == rootInbound.Port || inbound["tag"] == "in-3" {
			t.Fatalf("source server must not start the user VLESS inbound when processing is remote: %s", configA)
		}
	}

	plans, err := BuildProxyPathPlans(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].PortForwards) != 1 {
		t.Fatalf("unexpected port-forward plan: %#v", plans)
	}
	forward := plans[0].PortForwards[0]
	if forward.ListenPort != 55778 || forward.TargetPort != 557 || forward.Protocol != model.ForwardProtocolTCP {
		t.Fatalf("forward = %#v, want YT:55778 TCP -> WAWO:557", forward)
	}
	if forward.TrustedForward == nil || forward.TrustedForward.Version != 1 || forward.TrustedForward.Key == "" {
		t.Fatalf("transparent entry forward omitted trusted sender: %#v", forward)
	}

	configB := mustServerConfig(t, serverB, []model.Inbound{rootInbound}, []model.User{user}, opts)
	var parsedB SingBoxConfig
	if err := json.Unmarshal([]byte(configB), &parsedB); err != nil {
		t.Fatal(err)
	}
	processingTag := "oboard-path-1-step-1-in"
	var processing map[string]any
	for _, inbound := range parsedB.Inbounds {
		if inbound["tag"] == processingTag {
			processing = inbound
			break
		}
	}
	if processing == nil || processing["type"] != "vless" || processing["listen"] != "127.0.0.1" || intFromAny(processing["listen_port"]) == 557 {
		t.Fatalf("processing server missing loopback-only cloned VLESS inbound: %s", configB)
	}
	if parsedB.OBoard == nil || parsedB.OBoard.TrustedForward == nil || len(parsedB.OBoard.TrustedForward.Receivers) != 1 {
		t.Fatalf("processing server missing trusted receiver: %s", configB)
	}
	receiver := parsedB.OBoard.TrustedForward.Receivers[0]
	if receiver.ListenPort != forward.TargetPort || receiver.TargetPort != intFromAny(processing["listen_port"]) || receiver.Key != forward.TrustedForward.Key {
		t.Fatalf("trusted receiver does not bridge forward to processing inbound: receiver=%#v forward=%#v inbound=%#v", receiver, forward, processing)
	}
	tls, _ := processing["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	if tls["enabled"] != true || reality["enabled"] != true {
		t.Fatalf("processing inbound did not preserve Reality settings: %#v", processing)
	}
	if !hasInbound(configB, processingTag, "admin__oboard_path_1") {
		t.Fatalf("processing inbound missing branch user credentials: %s", configB)
	}
}

func TestTransparentPortForwardProtocolsFollowUserInbound(t *testing.T) {
	cases := []struct {
		protocol model.Protocol
		config   string
		want     model.ForwardProtocol
	}{
		{protocol: model.ProtocolVLESS, config: `{}`, want: model.ForwardProtocolTCP},
		{protocol: model.ProtocolAnyTLS, config: `{}`, want: model.ForwardProtocolTCP},
		{protocol: model.ProtocolHY2, config: `{}`, want: model.ForwardProtocolUDP},
		{protocol: model.ProtocolSS, config: `{}`, want: model.ForwardProtocolTCPUDP},
		{protocol: model.ProtocolSS, config: `{"network":"tcp"}`, want: model.ForwardProtocolTCP},
	}
	for _, tc := range cases {
		t.Run(string(tc.protocol)+tc.config, func(t *testing.T) {
			if got := transparentForwardProtocol(model.Inbound{Protocol: tc.protocol, ConfigJSON: tc.config}); got != tc.want {
				t.Fatalf("protocol = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTransparentPortForwardUsesReachableIPAndAvoidsExistingInboundPort(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "IPv4 source", PublicIPv4: "198.51.100.10", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 40000, PortRangeEnd: 40100}
	serverB := model.Server{ID: 2, Name: "IPv4 target", PublicIPv4: "203.0.113.20", PublicIPv6: "2001:db8::20", ListenIP: "0.0.0.0", PortRangeStart: 443, PortRangeEnd: 600}
	root := model.Inbound{ID: 1, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 40001, ConfigJSON: `{}`, Enabled: true}
	existing := model.Inbound{ID: 2, ServerID: serverB.ID, Name: "occupied", Protocol: model.ProtocolVLESS, Port: 557, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 1, Name: "transparent", InboundID: root.ID, Enabled: true}
	targetID := serverB.ID
	step := model.ProxyPathStep{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true}
	plans, err := BuildProxyPathPlans([]model.ProxyPath{path}, []model.ProxyPathStep{step}, []model.Server{serverA, serverB}, []model.Inbound{root, existing})
	if err != nil {
		t.Fatal(err)
	}
	forward := plans[0].PortForwards[0]
	if forward.TargetAddress != serverB.PublicIPv4 {
		t.Fatalf("target address = %q, want %q", forward.TargetAddress, serverB.PublicIPv4)
	}
	if forward.TargetPort == existing.Port {
		t.Fatalf("hidden target port reused existing inbound port %d", existing.Port)
	}
}

func TestTransparentProcessingRejectsExistingInboundAsProcessor(t *testing.T) {
	root := model.Inbound{ID: 1, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	targetInboundID := int64(2)
	path := model.ProxyPath{ID: 1, Name: "bad processor", InboundID: root.ID, Enabled: true}
	step := model.ProxyPathStep{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, InboundID: &targetInboundID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true}
	err := validateProxyPathTransportSet([]model.ProxyPath{path}, map[int64][]model.ProxyPathStep{path.ID: {step}}, map[int64]model.Inbound{root.ID: root, targetInboundID: {ID: targetInboundID, ServerID: 2, Protocol: model.ProtocolVLESS, Port: 8443, Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "不能复用已有入口") {
		t.Fatalf("error = %v, want existing inbound processor rejection", err)
	}
}

func TestTransparentProcessingClonesEverySupportedInboundProtocol(t *testing.T) {
	cases := []struct {
		name        string
		protocol    model.Protocol
		config      string
		wantType    string
		wantForward model.ForwardProtocol
	}{
		{name: "vless", protocol: model.ProtocolVLESS, config: `{}`, wantType: "vless", wantForward: model.ForwardProtocolTCP},
		{name: "hysteria2", protocol: model.ProtocolHY2, config: testInboundConfig(model.ProtocolHY2), wantType: "hysteria2", wantForward: model.ForwardProtocolUDP},
		{name: "anytls", protocol: model.ProtocolAnyTLS, config: testInboundConfig(model.ProtocolAnyTLS), wantType: "anytls", wantForward: model.ForwardProtocolTCP},
		{name: "shadowsocks", protocol: model.ProtocolSS, config: `{"method":"aes-128-gcm"}`, wantType: "shadowsocks", wantForward: model.ForwardProtocolTCPUDP},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := model.Server{ID: 1, Name: "source", PublicIPv4: "198.51.100.10", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 40000, PortRangeEnd: 40100}
			target := model.Server{ID: 2, Name: "target", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 10000, PortRangeEnd: 20000}
			root := model.Inbound{ID: int64(100 + index), ServerID: source.ID, Name: tc.name, Protocol: tc.protocol, ListenIP: "0.0.0.0", Port: 40001 + index, ConfigJSON: tc.config, Enabled: true}
			path := model.ProxyPath{ID: int64(200 + index), Name: tc.name + " transparent", InboundID: root.ID, Secret: "path-secret", Enabled: true}
			targetID := target.ID
			step := model.ProxyPathStep{ID: int64(300 + index), PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true}
			user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password-a"}
			opts := ConfigOptions{Servers: []model.Server{source, target}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: []model.ProxyPathStep{step}, InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}}}

			sourceConfig := parseSingBoxConfig(t, mustServerConfig(t, source, []model.Inbound{root}, []model.User{user}, opts))
			for _, inbound := range sourceConfig.Inbounds {
				if intFromAny(inbound["listen_port"]) == root.Port {
					t.Fatalf("source retained user protocol inbound: %#v", inbound)
				}
			}
			plans, err := BuildProxyPathPlans(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
			if err != nil {
				t.Fatal(err)
			}
			if len(plans[0].PortForwards) != 1 || plans[0].PortForwards[0].Protocol != tc.wantForward {
				t.Fatalf("forward plan = %#v, want protocol %s", plans[0].PortForwards, tc.wantForward)
			}
			if plans[0].PortForwards[0].TrustedForward == nil {
				t.Fatalf("forward plan omitted trusted sender: %#v", plans[0].PortForwards)
			}
			targetConfig := parseSingBoxConfig(t, mustServerConfig(t, target, []model.Inbound{root}, []model.User{user}, opts))
			processingTag := proxyPathInternalInboundTag(path.ID, step.Position)
			found := false
			for _, inbound := range targetConfig.Inbounds {
				if inbound["tag"] == processingTag {
					found = true
					if inbound["type"] != tc.wantType || inbound["listen"] != "127.0.0.1" || intFromAny(inbound["listen_port"]) == plans[0].PortForwards[0].TargetPort {
						t.Fatalf("processing inbound = %#v", inbound)
					}
				}
			}
			if !found {
				t.Fatalf("target missing processing inbound %s: %#v", processingTag, targetConfig.Inbounds)
			}
			if targetConfig.OBoard == nil || targetConfig.OBoard.TrustedForward == nil || len(targetConfig.OBoard.TrustedForward.Receivers) != 1 {
				t.Fatalf("target missing trusted receiver: %#v", targetConfig.OBoard)
			}
			receiver := targetConfig.OBoard.TrustedForward.Receivers[0]
			if receiver.Network != string(tc.wantForward) || receiver.ListenPort != plans[0].PortForwards[0].TargetPort || receiver.Key != plans[0].PortForwards[0].TrustedForward.Key {
				t.Fatalf("trusted receiver = %#v, forward = %#v", receiver, plans[0].PortForwards[0])
			}
		})
	}
}

func TestProxyPathImportedNodeCanBeMiddleDetourBeforeServerHop(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	serverC := model.Server{ID: 3, Name: "C", PublicIPv4: "203.0.113.3", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 32000, PortRangeEnd: 32100}
	rootInbound := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	external := model.ExternalOutbound{ID: 30, Name: "socks-p", Protocol: model.ProtocolSocks, TargetAddress: "socks.example.com", TargetPort: 1080, ConfigJSON: `{"type":"socks","server":"socks.example.com","server_port":1080}`, Enabled: true}
	path := model.ProxyPath{ID: 51, Name: "A-P-B-C", InboundID: rootInbound.ID, Secret: "path-secret", Enabled: true}
	serverBID, serverCID, externalID := serverB.ID, serverC.ID, external.ID
	opts := ConfigOptions{
		Servers:           []model.Server{serverA, serverB, serverC},
		Inbounds:          []model.Inbound{rootInbound},
		ExternalOutbounds: []model.ExternalOutbound{external},
		ProxyPaths:        []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
			{ID: 2, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
			{ID: 3, PathID: path.ID, Position: 3, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverCID},
		},
		InboundUsers: []model.InboundUser{{InboundID: rootInbound.ID, UserID: 1, Enabled: true}},
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}

	configA := mustServerConfig(t, serverA, []model.Inbound{rootInbound}, []model.User{user}, opts)
	if out := findOutbound(configA, "path-51-step-1"); out["type"] != "socks" {
		t.Fatalf("A should have imported SOCKS detour as first outbound, got %#v in %s", out, configA)
	}
	if out := findOutbound(configA, "path-51-step-2"); out["type"] != "shadowsocks" || out["detour"] != "path-51-step-1" || out["server"] != serverB.PublicIPv4 {
		t.Fatalf("A should connect to B through imported SOCKS detour, got %#v in %s", out, configA)
	}
	if !hasRoute(configA, "in-10", "path-51-step-2") {
		t.Fatalf("A missing route to detoured B hop: %s", configA)
	}

	configB := mustServerConfig(t, serverB, []model.Inbound{rootInbound}, nil, opts)
	if !hasInbound(configB, proxyPathChainServiceTag(DefaultProxyPathChainMethod), "__oboard_path_51_step_2") {
		t.Fatalf("B missing shared internal inbound for imported detour path: %s", configB)
	}
	if !hasRoute(configB, proxyPathChainServiceTag(DefaultProxyPathChainMethod), "path-51-step-3") {
		t.Fatalf("B missing route from shared inbound to C: %s", configB)
	}
}

func mustServerConfig(t *testing.T, server model.Server, inbounds []model.Inbound, users []model.User, opts ConfigOptions) string {
	t.Helper()
	config, err := GenerateServerConfigWithOptions(server, inbounds, nil, nil, users, opts)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func parseSingBoxConfig(t *testing.T, raw string) SingBoxConfig {
	t.Helper()
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
}

func findOutbound(raw, tag string) map[string]any {
	var parsed SingBoxConfig
	_ = json.Unmarshal([]byte(raw), &parsed)
	for _, outbound := range parsed.Outbounds {
		if outbound["tag"] == tag {
			return outbound
		}
	}
	return nil
}

func hasRoute(raw, inboundTag, outboundTag string) bool {
	var parsed SingBoxConfig
	_ = json.Unmarshal([]byte(raw), &parsed)
	for _, rule := range mapList(parsed.Route["rules"]) {
		if rule["outbound"] != outboundTag {
			continue
		}
		for _, inbound := range stringList(rule["inbound"]) {
			if inbound == inboundTag {
				return true
			}
		}
	}
	return false
}

func hasInbound(raw, tag, username string) bool {
	var parsed SingBoxConfig
	_ = json.Unmarshal([]byte(raw), &parsed)
	for _, inbound := range parsed.Inbounds {
		if inbound["tag"] != tag {
			continue
		}
		for _, user := range mapList(inbound["users"]) {
			if user["name"] == username {
				return true
			}
		}
	}
	return false
}

func TestProxyPathBranchesUseAuthUserRoutesAndSubscriptionNodes(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", PublicIPv4: "203.0.113.1", IPStack: model.IPStackPreferIPv4}
	inbound := model.Inbound{ID: 10, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	externalA := model.ExternalOutbound{ID: 30, Name: "socks-a", Protocol: model.ProtocolSocks, TargetAddress: "socks-a.example.com", TargetPort: 1080, ConfigJSON: `{"type":"socks","server":"socks-a.example.com","server_port":1080}`, Enabled: true}
	externalB := model.ExternalOutbound{ID: 31, Name: "socks-b", Protocol: model.ProtocolSocks, TargetAddress: "socks-b.example.com", TargetPort: 1080, ConfigJSON: `{"type":"socks","server":"socks-b.example.com","server_port":1080}`, Enabled: true}
	pathA := model.ProxyPath{ID: 40, NameMode: model.ProxyPathNameCustom, NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameLiteral, Value: "branch-a"}}, InboundID: inbound.ID, Secret: "path-a", Enabled: true}
	pathB := model.ProxyPath{ID: 41, NameMode: model.ProxyPathNameCustom, NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameLiteral, Value: "branch-b"}}, InboundID: inbound.ID, Secret: "path-b", Enabled: true}
	pathDirect := model.ProxyPath{ID: 42, Kind: model.ProxyPathKindDirect, NameMode: model.ProxyPathNameCustom, NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameLiteral, Value: "branch-direct"}}, InboundID: inbound.ID, Secret: "path-direct", Enabled: true}
	extAID, extBID := externalA.ID, externalB.ID
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	opts := ConfigOptions{
		Servers:           []model.Server{server},
		Inbounds:          []model.Inbound{inbound},
		ExternalOutbounds: []model.ExternalOutbound{externalA, externalB},
		ProxyPaths:        []model.ProxyPath{pathA, pathB, pathDirect},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 1, PathID: pathA.ID, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &extAID},
			{ID: 2, PathID: pathB.ID, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &extBID},
		},
		InboundUsers: []model.InboundUser{{InboundID: inbound.ID, UserID: user.ID, Enabled: true}},
	}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, nil, []model.User{user}, opts)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, `"name": "alice"`) {
		t.Fatalf("base user should be replaced by branch users when paths exist: %s", config)
	}
	if !strings.Contains(config, "alice__oboard_path_40") || !strings.Contains(config, "alice__oboard_path_41") || !strings.Contains(config, "alice__oboard_path_42") {
		t.Fatalf("branch users missing: %s", config)
	}
	var routeA, routeB, routeDirect bool
	for _, rule := range mapList(parsed.Route["rules"]) {
		users := stringList(rule["auth_user"])
		if rule["outbound"] == "path-40-step-1" && len(users) == 1 && users[0] == "alice__oboard_path_40" {
			routeA = true
		}
		if rule["outbound"] == "path-41-step-1" && len(users) == 1 && users[0] == "alice__oboard_path_41" {
			routeB = true
		}
		if rule["outbound"] == "direct" && len(users) == 1 && users[0] == "alice__oboard_path_42" {
			routeDirect = true
		}
	}
	if !routeA || !routeB || !routeDirect {
		t.Fatalf("branch auth_user routes missing: routeA=%v routeB=%v routeDirect=%v config=%s", routeA, routeB, routeDirect, config)
	}
	sub, err := GenerateSubscriptionWithOptions(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{InboundUsers: opts.InboundUsers, ProxyPaths: opts.ProxyPaths, ProxyPathSteps: opts.ProxyPathSteps})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sub, "branch-a") || !strings.Contains(sub, "branch-b") || !strings.Contains(sub, "branch-direct") {
		t.Fatalf("subscription should expose chain and direct branches: %s", sub)
	}
}

func TestGenerateServerConfig(t *testing.T) {
	config, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "s1"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "vless-in", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		[]model.Outbound{{ID: 2, ServerID: 1, Name: "ss-out", Protocol: model.ProtocolSS, TargetAddress: "1.2.3.4", TargetPort: 8388, ConfigJSON: `{}`, Enabled: true}},
		nil,
		[]model.User{{Username: "u", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 1 {
		t.Fatalf("expected 1 inbound, got %d", len(parsed.Inbounds))
	}
	if len(parsed.Outbounds) < 3 {
		t.Fatalf("expected default outbounds plus configured outbound, got %d", len(parsed.Outbounds))
	}
}

func TestVLESSInboundDoesNotEmitPacketEncoding(t *testing.T) {
	config, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "edge"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{"packet_encoding":"xudp"}`, Enabled: true}},
		nil,
		nil,
		[]model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Inbounds[0]["packet_encoding"]; ok {
		t.Fatalf("vless inbound must not include packet_encoding: %#v", parsed.Inbounds[0])
	}
}

func TestGenerateServerConfigUsesInboundUserBindings(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"},
		{ID: 2, Username: "bob", Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "pass-b"},
	}
	config, err := GenerateServerConfigWithOptions(
		model.Server{ID: 1, Name: "s1"},
		[]model.Inbound{
			{ID: 1, ServerID: 1, Name: "alice-only", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 2, ServerID: 1, Name: "bob-only", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true},
		},
		nil,
		nil,
		users,
		ConfigOptions{InboundUsers: []model.InboundUser{
			{InboundID: 1, UserID: 1, Enabled: true},
			{InboundID: 2, UserID: 2, Enabled: true},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, inbound := range parsed.Inbounds {
		rawUsers := inbound["users"].([]any)
		if inbound["tag"] == "in-1" && rawUsers[0].(map[string]any)["name"] != "alice" {
			t.Fatalf("in-1 users = %#v", rawUsers)
		}
		if inbound["tag"] == "in-2" && rawUsers[0].(map[string]any)["name"] != "bob" {
			t.Fatalf("in-2 users = %#v", rawUsers)
		}
	}
}

func TestGenerateServerConfigUsesPlaceholderWhenInboundHasNoUsers(t *testing.T) {
	cases := []struct {
		name     string
		protocol model.Protocol
		config   string
		port     int
	}{
		{name: "vless", protocol: model.ProtocolVLESS, config: `{}`, port: 443},
		{name: "hy2", protocol: model.ProtocolHY2, config: testInboundConfig(model.ProtocolHY2), port: 8443},
		{name: "anytls", protocol: model.ProtocolAnyTLS, config: testInboundConfig(model.ProtocolAnyTLS), port: 9443},
		{name: "ss-single-password", protocol: model.ProtocolSS, config: `{"method":"aes-128-gcm"}`, port: 8388},
		{name: "ss-2022", protocol: model.ProtocolSS, config: `{"method":"2022-blake3-aes-128-gcm","password":"` + base64.StdEncoding.EncodeToString(make([]byte, 16)) + `"}`, port: 8389},
		{name: "ss-2022-default", protocol: model.ProtocolSS, config: `{}`, port: 8390},
	}
	for i, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			inboundID := int64(i + 1)
			config, err := GenerateServerConfigWithOptions(
				model.Server{ID: 1, Name: "edge"},
				[]model.Inbound{{ID: inboundID, ServerID: 1, Name: tt.name, Protocol: tt.protocol, ListenIP: "0.0.0.0", Port: tt.port, ConfigJSON: tt.config, Enabled: true}},
				nil,
				nil,
				nil,
				ConfigOptions{InboundUsers: []model.InboundUser{}},
			)
			if err != nil {
				t.Fatal(err)
			}
			var parsed SingBoxConfig
			if err := json.Unmarshal([]byte(config), &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Inbounds) != 1 {
				t.Fatalf("expected one inbound: %#v", parsed.Inbounds)
			}
			inbound := parsed.Inbounds[0]
			placeholderName := "__oboard_placeholder_inbound_" + strconv.FormatInt(inboundID, 10)
			switch tt.name {
			case "ss-single-password":
				if inbound["password"] == "" {
					t.Fatalf("single-password ss placeholder password missing: %#v", inbound)
				}
				if _, ok := inbound["users"]; ok {
					t.Fatalf("single-password ss should not emit users: %#v", inbound)
				}
			default:
				rawUsers, ok := inbound["users"].([]any)
				if !ok || len(rawUsers) != 1 {
					t.Fatalf("placeholder users missing: %#v", inbound)
				}
				user := rawUsers[0].(map[string]any)
				if user["name"] != placeholderName {
					t.Fatalf("placeholder name = %#v, want %s", user, placeholderName)
				}
				if tt.protocol == model.ProtocolVLESS && user["uuid"] == "" {
					t.Fatalf("vless placeholder uuid missing: %#v", user)
				}
				if tt.protocol != model.ProtocolVLESS && user["password"] == "" {
					t.Fatalf("password placeholder missing: %#v", user)
				}
				if tt.protocol == model.ProtocolSS {
					method := stringFromAny(inbound["method"])
					if !validSS2022Key(stringFromAny(inbound["password"]), method) {
						t.Fatalf("ss2022 placeholder server password invalid: %#v", inbound)
					}
					if !validSS2022Key(stringFromAny(user["password"]), method) {
						t.Fatalf("ss2022 placeholder user password invalid: %#v", user)
					}
				}
			}
			if strings.Contains(config, "alice") || strings.Contains(config, "pass-a") {
				t.Fatalf("unexpected real user in placeholder config: %s", config)
			}
		})
	}
}

func TestGenerateServerConfigPlaceholderDisappearsWhenUserBound(t *testing.T) {
	config, err := GenerateServerConfigWithOptions(
		model.Server{ID: 1, Name: "edge"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		nil,
		nil,
		[]model.User{{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}},
		ConfigOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 7, Enabled: true}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "__oboard_placeholder_inbound_") {
		t.Fatalf("placeholder should not be present when real user is bound: %s", config)
	}
	if !strings.Contains(config, "alice") {
		t.Fatalf("real user missing: %s", config)
	}
}

func TestPlaceholderDoesNotEnterSubscription(t *testing.T) {
	content, err := GenerateSubscriptionWithOptions(
		model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"},
		[]model.Server{{ID: 1, Name: "edge", PublicIPv4: "203.0.113.10"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		SubscriptionOptions{InboundUsers: []model.InboundUser{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "__oboard_placeholder_inbound_") {
		t.Fatalf("placeholder leaked into subscription: %s", content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatal(err)
	}
	outbounds, _ := out["outbounds"].([]any)
	if len(outbounds) != 1 {
		t.Fatalf("subscription should only contain default direct outbound when user is not bound: %s", content)
	}
	if outbound, _ := outbounds[0].(map[string]any); outbound["tag"] != "direct" {
		t.Fatalf("subscription should not contain inbound node when user is not bound: %s", content)
	}
}

func TestImportedSocksSubscriptionRequiresGrant(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	external := model.ExternalOutbound{ID: 9, Name: "socks-a", Protocol: model.ProtocolSocks, TargetAddress: "socks.example.com", TargetPort: 1080, ConfigJSON: `{"type":"socks","server":"socks.example.com","server_port":1080,"username":"u","password":"p"}`, ExposeToUsers: true, Enabled: true}
	withoutGrant, err := GenerateSubscriptionWithOptions(user, nil, nil, SubscriptionOptions{ExternalOutbounds: []model.ExternalOutbound{external}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutGrant, "socks-a") {
		t.Fatalf("imported node leaked without grant: %s", withoutGrant)
	}
	withGrant, err := GenerateSubscriptionWithOptions(user, nil, nil, SubscriptionOptions{ExternalOutbounds: []model.ExternalOutbound{external}, ExternalOutboundAccessGrants: []model.ExternalOutboundAccessGrant{{ExternalOutboundID: external.ID, SubjectType: model.AccessSubjectUser, SubjectID: user.ID, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withGrant, `"type": "socks"`) || !strings.Contains(withGrant, "socks-a") {
		t.Fatalf("imported socks subscription missing: %s", withGrant)
	}
}

func TestGenerateServerConfigRejectsMultipleUsersOnSingleUserInbound(t *testing.T) {
	_, err := GenerateServerConfigWithOptions(
		model.Server{ID: 1, Name: "s1"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "single-password-ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"method":"chacha20-ietf-poly1305"}`, Enabled: true}},
		nil,
		nil,
		[]model.User{
			{ID: 1, Username: "alice", Status: "active", ProxyPassword: "pass-a"},
			{ID: 2, Username: "bob", Status: "active", ProxyPassword: "pass-b"},
		},
		ConfigOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 1, Enabled: true}, {InboundID: 1, UserID: 2, Enabled: true}}},
	)
	if err == nil {
		t.Fatal("expected single-user inbound to reject multiple bound users")
	}
}

func TestGenerateServerConfigRejectsTLSServerWithoutCertificate(t *testing.T) {
	users := []model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}}
	cases := []model.Inbound{
		{ID: 1, ServerID: 1, Name: "vless-tls", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{"flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"example.com"}}`, Enabled: true},
		{ID: 2, ServerID: 1, Name: "hy2", Protocol: model.ProtocolHY2, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{"tls":{"enabled":true,"server_name":"example.com"}}`, Enabled: true},
		{ID: 3, ServerID: 1, Name: "anytls", Protocol: model.ProtocolAnyTLS, ListenIP: "0.0.0.0", Port: 9443, ConfigJSON: `{"tls":{"enabled":true,"server_name":"example.com"}}`, Enabled: true},
	}
	for _, inbound := range cases {
		t.Run(inbound.Name, func(t *testing.T) {
			_, err := GenerateServerConfig(model.Server{ID: 1, Name: "edge"}, []model.Inbound{inbound}, nil, nil, users)
			if err == nil || !strings.Contains(err.Error(), "certificate/key or ACME is missing") {
				t.Fatalf("error = %v, want missing TLS key material", err)
			}
		})
	}
}

func TestGenerateServerConfigAcceptsVLESSRealityVisionAndStripsPublicKey(t *testing.T) {
	privateKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	config, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "edge"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "reality", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{
  "flow": "xtls-rprx-vision",
  "tls": {
    "enabled": true,
    "server_name": "cdn.icloud-content.com",
    "reality": {
      "enabled": true,
      "handshake": {"server": "cdn.icloud-content.com", "server_port": 443},
      "private_key": "` + privateKey + `",
      "public_key": "client-public",
      "short_id": "abcd"
    }
  }
}`, Enabled: true}},
		nil,
		nil,
		[]model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	tls := parsed.Inbounds[0]["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	if _, ok := reality["public_key"]; ok {
		t.Fatalf("public_key leaked into server config: %#v", reality)
	}
}

func TestGenerateServerConfigRejectsVLESSRealityWebSocketMix(t *testing.T) {
	privateKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	_, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "edge"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "bad", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{
  "flow": "xtls-rprx-vision",
  "transport": {"type": "ws", "path": "/vless"},
  "tls": {
    "enabled": true,
    "server_name": "cdn.icloud-content.com",
    "reality": {
      "enabled": true,
      "handshake": {"server": "cdn.icloud-content.com", "server_port": 443},
      "private_key": "` + privateKey + `"
    }
  }
}`, Enabled: true}},
		nil,
		nil,
		[]model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}},
	)
	if err == nil || !strings.Contains(err.Error(), "Reality requires TCP transport") {
		t.Fatalf("error = %v, want reality/tcp mismatch", err)
	}
}

func TestValidateGeneratedSingBoxConfigRejectsBadDNSDetour(t *testing.T) {
	config := SingBoxConfig{
		DNS: map[string]any{"servers": []map[string]any{
			{"type": "udp", "tag": "remote", "server": "1.1.1.1", "server_port": 53, "detour": "direct"},
		}, "final": "remote"},
		Outbounds: []map[string]any{{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}},
		Route:     map[string]any{"final": "direct"},
	}
	err := ValidateGeneratedSingBoxConfig(config)
	if err == nil || !strings.Contains(err.Error(), "detour=direct") {
		t.Fatalf("error = %v, want detour=direct rejection", err)
	}
}

func TestValidateGeneratedSingBoxConfigRejectsUoTOnInbound(t *testing.T) {
	config := SingBoxConfig{
		DNS: map[string]any{"servers": []map[string]any{
			{"type": "udp", "tag": "remote", "server": "1.1.1.1", "server_port": 53},
		}, "final": "remote"},
		Inbounds:  []map[string]any{{"type": "shadowsocks", "tag": "ss-in", "listen": "0.0.0.0", "listen_port": 8388, "method": "chacha20-ietf-poly1305", "password": "pass", "udp_over_tcp": map[string]any{"enabled": true}}},
		Outbounds: []map[string]any{{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}},
		Route:     map[string]any{"final": "direct"},
	}
	err := ValidateGeneratedSingBoxConfig(config)
	if err == nil || !strings.Contains(err.Error(), "udp_over_tcp is outbound-only") {
		t.Fatalf("error = %v, want inbound UoT rejection", err)
	}
}

func TestGeneratedConfigPassesOfficialSingBoxCheck(t *testing.T) {
	bin := os.Getenv("SING_BOX_BIN")
	if bin == "" {
		t.Skip("set SING_BOX_BIN to run official sing-box check")
	}
	config, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "edge", IPStack: model.IPStackPreferIPv4},
		nil,
		[]model.Outbound{
			{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 2, ServerID: 1, Name: "hy2", Protocol: model.ProtocolHY2, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 3, ServerID: 1, Name: "anytls", Protocol: model.ProtocolAnyTLS, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 4, ServerID: 1, Name: "ss", Protocol: model.ProtocolSS, TargetAddress: "example.com", TargetPort: 8388, ConfigJSON: `{"method":"aes-128-gcm"}`, Enabled: true},
		},
		nil,
		[]model.User{{Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "secret-password"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	runSingBoxCheck(t, bin, config)
}

func TestGeneratedWARPAndRouteConfigPassesOfficialSingBoxCheck(t *testing.T) {
	bin := os.Getenv("SING_BOX_BIN")
	if bin == "" {
		t.Skip("set SING_BOX_BIN to run official sing-box check")
	}
	warpID := int64(30)
	server := model.Server{ID: 1, Name: "edge", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, MTUValue: 1280}
	inbound := model.Inbound{ID: 10, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 20, Name: "edge｜WARP", InboundID: inbound.ID, Enabled: true}
	step := model.ProxyPathStep{ID: 21, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepWARP, TransportMode: model.ProxyPathTransportSingBox}
	config, err := GenerateServerConfigWithOptions(
		server,
		[]model.Inbound{inbound},
		nil,
		nil,
		[]model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111"}},
		ConfigOptions{
			Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: []model.ProxyPathStep{step},
			WARPProfiles: []model.WARPProfile{{ID: warpID, ServerID: 1, Name: "warp", Status: model.WARPStatusReady, ConfigJSON: `{"type":"wireguard","address":["172.16.0.2/32","2606:4700:110:abcd::2/128"],"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"bmXOC+F1sQvdD4mp8yt3l7wY6/3mpYBvn04zP65yzM8=","reserved":[1,2,3],"allowed_ips":["0.0.0.0/0","::/0"]}]}`, Enabled: true}},
			RoutingRules: []model.RoutingRule{{ID: 2, ServerID: 1, Name: "ssh-via-eth1", Priority: 20, MatchJSON: `{"port":[22]}`, Action: model.RouteActionInterface, InterfaceName: "eth1", Enabled: true}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	runSingBoxCheck(t, bin, config)
}

func TestReadyWARPInfersIPv6ResolverAndMTUForAutoServer(t *testing.T) {
	endpoint, err := warpProfileToSingBox(model.WARPProfile{
		ID:          30,
		DNSStrategy: "auto",
		ConfigJSON:  `{"type":"wireguard","address":["172.16.0.2/32","2606:4700:110::2/128"],"private_key":"private","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"public","allowed_ips":["0.0.0.0/0","::/0"]}]}`,
	}, model.Server{IPStack: model.IPStackAuto, PublicIPv6: "2001:db8::10"})
	if err != nil {
		t.Fatal(err)
	}
	resolver, ok := endpoint["domain_resolver"].(map[string]any)
	if !ok || resolver["strategy"] != "ipv6_only" {
		t.Fatalf("WARP domain_resolver = %#v", endpoint["domain_resolver"])
	}
	if endpoint["mtu"] != 1280 {
		t.Fatalf("WARP MTU = %#v, want 1280", endpoint["mtu"])
	}
}

func TestProxyPathWARPUsesLastControlledServer(t *testing.T) {
	warpID := int64(30)
	server := model.Server{ID: 1, Name: "edge", ListenIP: "0.0.0.0", IPStack: model.IPStackDualStack}
	inbound := model.Inbound{ID: 10, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 20, Name: "edge｜WARP", InboundID: inbound.ID, Enabled: true}
	step := model.ProxyPathStep{ID: 21, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepWARP, TransportMode: model.ProxyPathTransportSingBox}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, nil, []model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111"}}, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: []model.ProxyPathStep{step},
		WARPProfiles: []model.WARPProfile{{ID: warpID, ServerID: server.ID, Status: model.WARPStatusRequested, ConfigJSON: `{}`, Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Endpoints) != 1 || parsed.Endpoints[0]["_oboard_warp_pending"] == nil {
		t.Fatalf("pending WARP template endpoint = %#v", parsed.Endpoints)
	}
	rules := parsed.Route["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["outbound"] != WARPOutboundTag(warpID) {
		t.Fatalf("WARP path rule = %#v", rules)
	}
}

func TestProxyPathWARPRejectsFollowingNode(t *testing.T) {
	serverID := int64(1)
	path := model.ProxyPath{ID: 20, Name: "invalid", InboundID: 10, Enabled: true}
	steps := []model.ProxyPathStep{
		{ID: 21, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepWARP, TransportMode: model.ProxyPathTransportSingBox},
		{ID: 22, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverID, TransportMode: model.ProxyPathTransportSingBox},
	}
	_, err := BuildProxyPathPlans([]model.ProxyPath{path}, steps, []model.Server{{ID: serverID, Name: "edge"}}, []model.Inbound{{ID: 10, ServerID: serverID, Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "WARP 必须是最后一个节点") {
		t.Fatalf("expected terminal WARP validation, got %v", err)
	}
}

func TestRuntimeLimitsCarryOfflineLeaseEnforcement(t *testing.T) {
	limits := runtimeLimitsForUsers([]model.User{{ID: 7, Username: "alice", Status: "active"}}, ConfigOptions{TrafficPolicies: map[int64]model.TrafficRuntimePolicy{
		7: {UserID: 7, Billable: true, TrafficLimitBytes: 1000, UsedBaselineBytes: 100, LeaseBytes: 200, ResetLeaseBytes: 300, LeaseEnforced: true},
	}})
	limit := limits["alice"]
	if !limit.LeaseEnforced || limit.LeaseBytes != 200 || limit.ResetLeaseBytes != 300 {
		t.Fatalf("runtime lease enforcement was not preserved: %#v", limit)
	}
}

func TestGenerateSubscription(t *testing.T) {
	sub, err := GenerateSubscription(
		model.User{Username: "u", Status: "active", ProxyUUID: "uuid", ProxyPassword: "pass"},
		[]model.Server{{ID: 1, Name: "s1", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, Enabled: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(sub)) {
		t.Fatal("subscription is not valid json")
	}
}

func TestVLESSRealityAndPQTLSPassthrough(t *testing.T) {
	adapter, err := AdapterFor(model.ProtocolVLESS)
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.Outbound(model.Outbound{
		ID:            9,
		Protocol:      model.ProtocolVLESS,
		TargetAddress: "example.com",
		TargetPort:    443,
		ConfigJSON: `{
  "flow": "xtls-rprx-vision",
  "packet_encoding": "xudp",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "reality": {"enabled": true, "public_key": "pub", "short_id": "abcd"},
    "ech": {"enabled": true, "pq_signature_schemes_enabled": true}
  }
}`,
	}, &model.User{ProxyUUID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	if out["flow"] != "xtls-rprx-vision" || out["packet_encoding"] != "xudp" {
		t.Fatalf("vless extension fields missing: %#v", out)
	}
	tls, ok := out["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls passthrough missing: %#v", out)
	}
	if _, ok := tls["reality"].(map[string]any); !ok {
		t.Fatalf("reality passthrough missing: %#v", tls)
	}
	ech, ok := tls["ech"].(map[string]any)
	if !ok || ech["pq_signature_schemes_enabled"] != true {
		t.Fatalf("pq ech passthrough missing: %#v", tls)
	}
}

func TestVLESSRealityInboundStripsClientPublicKey(t *testing.T) {
	adapter, err := AdapterFor(model.ProtocolVLESS)
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.Inbound(model.Inbound{
		ID:       9,
		Protocol: model.ProtocolVLESS,
		ListenIP: "0.0.0.0",
		Port:     443,
		ConfigJSON: `{
  "flow": "xtls-rprx-vision",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "reality": {
      "enabled": true,
      "handshake": {"server": "example.com", "server_port": 443},
      "private_key": "server-private",
      "public_key": "client-public",
      "short_id": "abcd"
    }
  }
}`,
	}, []model.User{{Username: "alice", ProxyUUID: "11111111-1111-4111-8111-111111111111"}})
	if err != nil {
		t.Fatal(err)
	}
	tls, ok := out["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls missing: %#v", out)
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok {
		t.Fatalf("reality missing: %#v", tls)
	}
	if _, ok := reality["public_key"]; ok {
		t.Fatalf("public_key leaked into inbound server config: %#v", reality)
	}
	if reality["private_key"] != "server-private" {
		t.Fatalf("private_key missing from server config: %#v", reality)
	}
}

func TestProtocolUsersUseSingBoxObjectShape(t *testing.T) {
	users := []model.User{{Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}}
	for _, protocol := range []model.Protocol{model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS} {
		adapter, err := AdapterFor(protocol)
		if err != nil {
			t.Fatal(err)
		}
		block, err := adapter.Inbound(model.Inbound{ID: 1, Protocol: protocol, ListenIP: "127.0.0.1", Port: 443, ConfigJSON: testInboundConfig(protocol), Enabled: true}, users)
		if err != nil {
			t.Fatalf("%s inbound: %v", protocol, err)
		}
		rawUsers, ok := block["users"].([]map[string]any)
		if !ok || len(rawUsers) != 1 {
			t.Fatalf("%s users shape = %#v, want []map[string]any", protocol, block["users"])
		}
	}
}

func TestHY2AndAnyTLSInboundRequireTLS(t *testing.T) {
	for _, protocol := range []model.Protocol{model.ProtocolHY2, model.ProtocolAnyTLS} {
		adapter, err := AdapterFor(protocol)
		if err != nil {
			t.Fatal(err)
		}
		_, err = adapter.Inbound(model.Inbound{ID: 1, Protocol: protocol, ListenIP: "127.0.0.1", Port: 443, ConfigJSON: `{}`, Enabled: true}, []model.User{{Username: "u", Status: "active", ProxyPassword: "pass"}})
		if err == nil {
			t.Fatalf("expected %s inbound without tls to fail", protocol)
		}
	}
}

func testInboundConfig(protocol model.Protocol) string {
	switch protocol {
	case model.ProtocolHY2, model.ProtocolAnyTLS:
		return `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"}}`
	default:
		return `{}`
	}
}

func TestHY2LatestFieldsPassThrough(t *testing.T) {
	adapter, err := AdapterFor(model.ProtocolHY2)
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.Outbound(model.Outbound{
		ID:            3,
		Protocol:      model.ProtocolHY2,
		TargetAddress: "example.com",
		TargetPort:    443,
		ConfigJSON: `{
    "server_ports": ["2080:3000"],
    "hop_interval_max": "30s",
    "bbr_profile": "mobile",
    "realm": {"server_url":"https://realm.example.com","token":"t"},
    "max_idle_timeout": "10s",
    "max_incoming_streams": 128
  }`,
	}, &model.User{ProxyPassword: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"server_ports", "hop_interval_max", "bbr_profile", "realm", "max_idle_timeout", "max_incoming_streams"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("hy2 outbound missing %s: %#v", key, out)
		}
	}
	inbound, err := adapter.Inbound(model.Inbound{ID: 4, Protocol: model.ProtocolHY2, ListenIP: "127.0.0.1", Port: 443, ConfigJSON: `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"},"realm":{"server_url":"https://realm.example.com"},"bbr_profile":"desktop","disable_path_mtu_discovery":true}`, Enabled: true}, []model.User{{Username: "u", Status: "active", ProxyPassword: "pass"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"realm", "bbr_profile", "disable_path_mtu_discovery"} {
		if _, ok := inbound[key]; !ok {
			t.Fatalf("hy2 inbound missing %s: %#v", key, inbound)
		}
	}
}

func TestGenerateServerConfigWithRoutingRulesIgnoresUnreferencedWARP(t *testing.T) {
	outboundID := int64(10)
	externalID := int64(20)
	warpID := int64(30)
	config, err := GenerateServerConfigWithOptions(
		model.Server{ID: 1, Name: "edge", IPStack: model.IPStackDualStack, MTUValue: 1360},
		nil,
		[]model.Outbound{{ID: outboundID, ServerID: 1, Name: "paid-ss", Protocol: model.ProtocolSS, TargetAddress: "example.com", TargetPort: 8388, ConfigJSON: `{}`, Enabled: true}},
		nil,
		[]model.User{{Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}},
		ConfigOptions{
			ExternalOutbounds: []model.ExternalOutbound{{ID: externalID, Name: "imported-vless", Protocol: model.ProtocolVLESS, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "ext.example.com", TargetPort: 443, ConfigJSON: `{}`, Enabled: true}},
			WARPProfiles:      []model.WARPProfile{{ID: warpID, ServerID: 1, Name: "warp-ready", Status: model.WARPStatusReady, ConfigJSON: `{"type":"wireguard","address":["172.16.0.2/32","2606:4700:110:abcd::2/128"],"private_key":"priv","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"pub","reserved":[1,2,3],"allowed_ips":["0.0.0.0/0","::/0"]}]}`, Enabled: true}},
			RoutingRules: []model.RoutingRule{
				{ID: 1, ServerID: 1, Name: "direct-local", Priority: 10, MatchJSON: `{"domain_suffix":["lan"]}`, Action: model.RouteActionDirect, Enabled: true},
				{ID: 2, ServerID: 1, Name: "paid", Priority: 20, MatchJSON: `{"domain_suffix":["example.org"]}`, Action: model.RouteActionOutbound, OutboundID: &outboundID, Enabled: true},
				{ID: 3, ServerID: 1, Name: "external", Priority: 30, MatchJSON: `{"domain_suffix":["example.net"]}`, Action: model.RouteActionExternal, ExternalOutboundID: &externalID, Enabled: true},
				{ID: 6, ServerID: 1, Name: "ssh-via-wan6", Priority: 45, MatchJSON: `{"port":[22]}`, Action: model.RouteActionInterface, InterfaceName: "eth1", Enabled: true},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Outbounds) != 4 {
		t.Fatalf("outbounds = %d, want direct/block + normal + external", len(parsed.Outbounds))
	}
	if len(parsed.Endpoints) != 0 {
		t.Fatalf("unreferenced WARP profile emitted endpoints: %#v", parsed.Endpoints)
	}
	rules, ok := parsed.Route["rules"].([]any)
	if !ok {
		if raw, ok2 := parsed.Route["rules"].([]map[string]any); ok2 {
			for _, rule := range raw {
				rules = append(rules, rule)
			}
		}
	}
	if len(rules) != 4 {
		t.Fatalf("route rules = %d, want direct/outbound/external/interface: %#v", len(rules), parsed.Route["rules"])
	}
	want := []string{"direct", tag("out", outboundID), tag("ext", externalID)}
	for i, outbound := range want {
		rule := rules[i].(map[string]any)
		if rule["action"] != "route" {
			t.Fatalf("rule %d action = %v, want route; rules=%#v", i, rule["action"], rules)
		}
		if rule["outbound"] != outbound {
			t.Fatalf("rule %d outbound = %v, want %s; rules=%#v", i, rule["outbound"], outbound, rules)
		}
	}
	interfaceRule := rules[3].(map[string]any)
	if interfaceRule["action"] != "direct" || interfaceRule["bind_interface"] != "eth1" {
		t.Fatalf("interface route = %#v, want direct action bound to eth1", interfaceRule)
	}
	ports, ok := interfaceRule["port"].([]any)
	if !ok || len(ports) != 1 || ports[0].(float64) != 22 {
		t.Fatalf("interface route ports = %#v, want [22]", interfaceRule["port"])
	}
}

func TestGenerateServerConfigRejectsLegacyWARPRoutingAction(t *testing.T) {
	_, err := GenerateServerConfigWithOptions(
		model.Server{ID: 1, Name: "edge", IPStack: model.IPStackDualStack},
		nil,
		nil,
		nil,
		nil,
		ConfigOptions{
			WARPProfiles: []model.WARPProfile{{ID: 7, ServerID: 1, Status: model.WARPStatusReady, ConfigJSON: `{}`, Enabled: true}},
			RoutingRules: []model.RoutingRule{{ID: 1, ServerID: 1, Name: "legacy-warp", Action: model.RouteAction("warp"), OutboundTag: "warp-7", Enabled: true}},
		},
	)
	if err == nil || !strings.Contains(err.Error(), `unsupported route action "warp"`) {
		t.Fatalf("expected legacy WARP routing action rejection, got %v", err)
	}
}

func TestValidateRoutingMatchJSONPorts(t *testing.T) {
	for _, valid := range []string{`{"port":22}`, `{"port":[22,443]}`, `{"port_range":"1000:2000"}`, `{"port_range":["1000:2000","8443:8443"]}`} {
		if err := ValidateRoutingMatchJSON(valid); err != nil {
			t.Fatalf("valid match %s rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{`{"port":0}`, `{"port":[22,"443"]}`, `{"port_range":"2000:1000"}`, `{"port_range":"22"}`} {
		if err := ValidateRoutingMatchJSON(invalid); err == nil {
			t.Fatalf("invalid match %s accepted", invalid)
		}
	}
}

func runSingBoxCheck(t *testing.T, bin string, config string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "check", "-c", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sing-box check failed: %v\n%s\nconfig:\n%s", err, out, config)
	}
}

func TestGenerateServerConfigRejectsIPv6OnlyLiteralIPv4Outbounds(t *testing.T) {
	_, err := GenerateServerConfigWithOptions(
		model.Server{ID: 1, Name: "v6", IPStack: model.IPStackIPv6Only},
		nil,
		[]model.Outbound{{ID: 1, ServerID: 1, Name: "bad-v4", Protocol: model.ProtocolVLESS, TargetAddress: "1.1.1.1", TargetPort: 443, ConfigJSON: `{}`, Enabled: true}},
		nil,
		[]model.User{{Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111"}},
		ConfigOptions{},
	)
	if err == nil {
		t.Fatal("expected IPv6-only server to reject IPv4 literal outbound")
	}
}

func TestGenerateServerConfigRejectsIPv6OnlyLiteralIPv4ExternalOutbound(t *testing.T) {
	_, err := GenerateServerConfigWithOptions(
		model.Server{ID: 1, Name: "v6", IPStack: model.IPStackIPv6Only},
		nil,
		nil,
		nil,
		[]model.User{{Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111"}},
		ConfigOptions{ExternalOutbounds: []model.ExternalOutbound{{ID: 1, Name: "bad-v4", Protocol: model.ProtocolVLESS, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "1.1.1.1", TargetPort: 443, ConfigJSON: `{}`, Enabled: true}}},
	)
	if err == nil {
		t.Fatal("expected IPv6-only server to reject IPv4 literal external outbound")
	}
}

func TestGeneratedConfigIncludesRuntimeRateLimits(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", IPStack: model.IPStackPreferIPv4}
	inbound := model.Inbound{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"},
		{ID: 2, Username: "bob", Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "pass-b", SpeedLimitMbps: 10},
	}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, nil, users, ConfigOptions{
		InboundUsers:     []model.InboundUser{{InboundID: 1, UserID: 1, Enabled: true}, {InboundID: 1, UserID: 2, Enabled: true}},
		UserGroups:       []model.UserGroup{{ID: 1, Name: "limited", Enabled: true, SpeedLimitMbps: 20}},
		UserGroupMembers: []model.UserGroupMember{{GroupID: 1, UserID: 1, Enabled: true}, {GroupID: 1, UserID: 2, Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OBoard == nil || parsed.OBoard.RateLimits.Users == nil {
		t.Fatalf("missing runtime limits: %s", config)
	}
	if got := parsed.OBoard.RateLimits.Users["alice"].SpeedLimitMbps; got != 20 {
		t.Fatalf("alice speed limit = %d, want group limit 20", got)
	}
	if got := parsed.OBoard.RateLimits.Users["bob"].SpeedLimitMbps; got != 10 {
		t.Fatalf("bob speed limit = %d, want user override 10", got)
	}
}

func TestGeneratedConfigTracksUnlimitedRealUser(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", IPStack: model.IPStackPreferIPv4}
	inbound := model.Inbound{ID: 7, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	user := model.User{ID: 9, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, nil, []model.User{user}, ConfigOptions{
		InboundUsers: []model.InboundUser{{InboundID: inbound.ID, UserID: user.ID, Enabled: true}},
		TrafficPolicies: map[int64]model.TrafficRuntimePolicy{
			user.ID: {UserID: user.ID, Billable: true, PeriodKey: "2026-07-01", PeriodStart: "2026-06-30T16:00:00Z", PeriodEnd: "2026-07-31T16:00:00Z", ResetMode: "monthly", Timezone: "Asia/Shanghai", QuotaState: "active"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	limit, ok := parsed.OBoard.RateLimits.Users[user.Username]
	if !ok || !limit.Billable || limit.UserID != user.ID {
		t.Fatalf("unlimited real user is not billable: %#v", parsed.OBoard)
	}
	if limit.SpeedLimitMbps != 0 || limit.TrafficLimitBytes != 0 {
		t.Fatalf("unlimited user unexpectedly received limits: %#v", limit)
	}
	if limit.InboundID != inbound.ID || limit.PeriodKey != "2026-07-01" {
		t.Fatalf("unlimited runtime identity missing: %#v", limit)
	}
}

func TestRuntimePathIDFromUsername(t *testing.T) {
	if got := runtimePathIDFromUsername("alice__oboard_path_42"); got != 42 {
		t.Fatalf("path id = %d, want 42", got)
	}
	for _, username := range []string{"alice", "alice__oboard_path_bad", "__oboard_path_0"} {
		if got := runtimePathIDFromUsername(username); got != 0 {
			t.Fatalf("path id for %q = %d, want 0", username, got)
		}
	}
}

func TestGeneratedConfigOmitsPlaceholderRuntimeRateLimits(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", IPStack: model.IPStackPreferIPv4}
	inbound := model.Inbound{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, nil, nil, ConfigOptions{InboundUsers: []model.InboundUser{}})
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OBoard != nil && len(parsed.OBoard.RateLimits.Users) > 0 {
		t.Fatalf("placeholder users must not create runtime limits: %#v", parsed.OBoard.RateLimits.Users)
	}
}

func TestSingleUserInboundIncludesInboundRuntimeRateLimit(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", IPStack: model.IPStackPreferIPv4}
	inbound := model.Inbound{ID: 7, ServerID: 1, Name: "ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"method":"aes-128-gcm"}`, Enabled: true}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a", SpeedLimitMbps: 20}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, nil, []model.User{user}, ConfigOptions{InboundUsers: []model.InboundUser{{InboundID: 7, UserID: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OBoard == nil || parsed.OBoard.RateLimits.Inbounds["in-7"].SpeedLimitMbps != 20 {
		t.Fatalf("missing inbound fallback limit: %#v", parsed.OBoard)
	}
}

func TestProxyPathInternalPortsAvoidOccupiedSinglePortRanges(t *testing.T) {
	server := model.Server{ID: 2, Name: "single-port", PortRangeStart: 11122, PortRangeEnd: 11122}
	existing := model.Inbound{ID: 9, ServerID: server.ID, Protocol: model.ProtocolVLESS, Port: 11122, Enabled: true}
	inbounds := map[int64]model.Inbound{existing.ID: existing}

	internalPort := proxyPathInternalPort(server, 4, 1, inbounds)
	if internalPort == existing.Port || internalPort < 30000 || internalPort > 60000 {
		t.Fatalf("internal port = %d, want a free port from the internal pool", internalPort)
	}
	wgPort := proxyPathTunnelPort(server, 4, 1, 47, inbounds)
	if wgPort != existing.Port {
		t.Fatalf("WireGuard port = %d, want UDP to share TCP-only port %d", wgPort, existing.Port)
	}
	sshLoopbackPort := proxyPathTunnelPort(server, 4, 1, 31, inbounds)
	if sshLoopbackPort < 20000 || sshLoopbackPort > 29999 || sshLoopbackPort == existing.Port {
		t.Fatalf("SSH loopback port = %d, want a free port from the loopback pool", sshLoopbackPort)
	}
	if sshServerPort := proxyPathTunnelPort(server, 4, 1, 61, inbounds); sshServerPort != 0 {
		t.Fatalf("SSH server port = %d, want exhausted TCP range to fail", sshServerPort)
	}
	udpInbound := model.Inbound{ID: 10, ServerID: server.ID, Protocol: model.ProtocolHY2, Port: 11122, Enabled: true}
	if port := proxyPathTunnelPort(server, 4, 1, 47, map[int64]model.Inbound{udpInbound.ID: udpInbound}); port != 0 {
		t.Fatalf("WireGuard port = %d, want exhausted UDP range to fail", port)
	}
}

func TestProxyPathTunnelPortsAvoidGeneratedInboundCollisions(t *testing.T) {
	sourceA := model.Server{ID: 1, Name: "A", PublicIPv4: "198.51.100.1", ListenIP: "0.0.0.0", PortRangeStart: 40000, PortRangeEnd: 40100}
	sourceB := model.Server{ID: 2, Name: "B", PublicIPv4: "198.51.100.2", ListenIP: "0.0.0.0", PortRangeStart: 41000, PortRangeEnd: 41100}
	target := model.Server{ID: 5, Name: "target", PublicIPv4: "203.0.113.5", ListenIP: "0.0.0.0", PortRangeStart: 60100, PortRangeEnd: 60103, SSHPort: 60103}
	rootA := model.Inbound{ID: 1, ServerID: sourceA.ID, Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	rootB := model.Inbound{ID: 2, ServerID: sourceB.ID, Protocol: model.ProtocolVLESS, Port: 8443, Enabled: true}
	targetPublic := model.Inbound{ID: 6, ServerID: target.ID, Protocol: model.ProtocolVLESS, Port: 60100, Enabled: true}
	targetID := target.ID
	chainPath := model.ProxyPath{ID: 9, Name: "chain", InboundID: rootA.ID, Enabled: true}
	chainStep := model.ProxyPathStep{ID: 9, PathID: chainPath.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`}
	sshPath := model.ProxyPath{ID: 10, Name: "ssh", InboundID: rootB.ID, Enabled: true}
	privateKey, publicKey := testSSHKeyPair(t)
	sshConfig, _ := json.Marshal(map[string]any{"type": "ssh", "client_private_key": privateKey, "client_public_key": publicKey})
	sshStep := model.ProxyPathStep{ID: 10, PathID: sshPath.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: string(sshConfig)}

	plans, err := BuildProxyPathPlans(
		[]model.ProxyPath{sshPath, chainPath},
		[]model.ProxyPathStep{sshStep, chainStep},
		[]model.Server{sourceA, sourceB, target},
		[]model.Inbound{rootA, rootB, targetPublic},
	)
	if err != nil {
		t.Fatal(err)
	}
	chainInbound := proxyPathInternalInbound(chainPath, chainStep, target, map[int64]model.Inbound{targetPublic.ID: targetPublic}, nil)
	var sshTunnel model.Tunnel
	for _, plan := range plans {
		if plan.PathID == sshPath.ID && len(plan.Tunnels) == 1 {
			sshTunnel = plan.Tunnels[0]
		}
	}
	if sshTunnel.TargetPort == 0 || sshTunnel.TargetPort == targetPublic.Port || sshTunnel.TargetPort == chainInbound.Port {
		t.Fatalf("SSH server port %d collided with generated/public inbound ports %d/%d", sshTunnel.TargetPort, targetPublic.Port, chainInbound.Port)
	}
}

func TestProxyPathSSHTunnelConnectsSingBoxToManagedLocalForward(t *testing.T) {
	source := model.Server{ID: 1, Name: "A", PublicIPv4: "198.51.100.10", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100, SSHPort: 22}
	root := model.Inbound{ID: 1, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 70, Name: "A-SSH-B", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	targetID := target.ID
	privateKey, publicKey := testSSHKeyPair(t)
	stepConfig, _ := json.Marshal(map[string]any{"type": "ssh", "managed_pair": true, "client_private_key": privateKey, "client_public_key": publicKey})
	step := model.ProxyPathStep{ID: 71, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: string(stepConfig)}
	opts := ConfigOptions{Servers: []model.Server{source, target}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: []model.ProxyPathStep{step}}

	plans, err := BuildProxyPathPlans(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Tunnels) != 1 {
		t.Fatalf("SSH path tunnel plan = %#v", plans)
	}
	tunnel := plans[0].Tunnels[0]
	if tunnel.Type != model.TunnelTypeSSH || tunnel.ListenPort == 0 || tunnel.TargetPort != 22 {
		t.Fatalf("SSH tunnel = %#v", tunnel)
	}
	var tunnelConfig map[string]any
	if err := json.Unmarshal([]byte(tunnel.ConfigJSON), &tunnelConfig); err != nil {
		t.Fatal(err)
	}
	services, err := buildProxyPathChainServices(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := services[proxyPathChainServiceKey{ServerID: target.ID, Method: DefaultProxyPathChainMethod}]
	if service == nil || service.Inbound.Port == 0 {
		t.Fatalf("SSH target shared chain service = %#v", services)
	}
	wantForward := "127.0.0.1:" + strconv.Itoa(tunnel.ListenPort) + ":127.0.0.1:" + strconv.Itoa(service.Inbound.Port)
	if tunnelConfig["local_forward"] != wantForward {
		t.Fatalf("SSH local_forward = %v, want %s", tunnelConfig["local_forward"], wantForward)
	}
	sourcePlan, err := BuildTunnelPlan(1, source, opts.Servers, []model.Tunnel{tunnel})
	if err != nil {
		t.Fatal(err)
	}
	targetPlan, err := BuildTunnelPlan(1, target, opts.Servers, []model.Tunnel{tunnel})
	if err != nil {
		t.Fatal(err)
	}
	if len(sourcePlan.Tunnels) != 1 || len(targetPlan.Tunnels) != 1 {
		t.Fatalf("SSH pair projections source=%#v target=%#v", sourcePlan, targetPlan)
	}
	var sourceSSH, targetSSH map[string]any
	_ = json.Unmarshal([]byte(sourcePlan.Tunnels[0].ConfigJSON), &sourceSSH)
	_ = json.Unmarshal([]byte(targetPlan.Tunnels[0].ConfigJSON), &targetSSH)
	if sourceSSH["role"] != "client" || sourceSSH["client_private_key"] == "" || sourceSSH["authorized_key"] != nil {
		t.Fatalf("SSH source projection = %#v", sourceSSH)
	}
	if targetSSH["role"] != "server" || targetSSH["authorized_key"] != tunnelConfig["client_public_key"] || targetSSH["authorized_key"] == publicKey || targetSSH["client_private_key"] != nil || intFromAny(targetSSH["server_port"]) != target.SSHPort {
		t.Fatalf("SSH target projection = %#v", targetSSH)
	}

	sourceConfig := parseSingBoxConfig(t, mustServerConfig(t, source, []model.Inbound{root}, nil, opts))
	var pathOutbound map[string]any
	for _, outbound := range sourceConfig.Outbounds {
		if outbound["tag"] == proxyPathStepTag(path.ID, step.Position) {
			pathOutbound = outbound
		}
	}
	if pathOutbound == nil || pathOutbound["server"] != "127.0.0.1" || intFromAny(pathOutbound["server_port"]) != tunnel.ListenPort {
		t.Fatalf("sing-box did not dial SSH local forward: %#v", pathOutbound)
	}
	targetConfig := parseSingBoxConfig(t, mustServerConfig(t, target, []model.Inbound{root}, nil, opts))
	if !hasInbound(string(mustJSON(t, targetConfig)), service.Tag, proxyPathInternalUser(path, step).Username) {
		t.Fatalf("target missing shared Shadowsocks chain inbound: %#v", targetConfig.Inbounds)
	}
}

func TestProxyPathSSHTunnelPortSelection(t *testing.T) {
	source := model.Server{ID: 1, Name: "source", PublicIPv4: "198.51.100.10", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "target", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 31000, PortRangeEnd: 31100, SSHPort: 22}
	root := model.Inbound{ID: 1, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	targetID := target.ID
	paths := []model.ProxyPath{
		{ID: 91, Name: "saved-port-a", InboundID: root.ID, Enabled: true},
		{ID: 92, Name: "saved-port-b", InboundID: root.ID, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 91, PathID: 91, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh"}`},
		{ID: 92, PathID: 92, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh"}`},
	}
	plans, err := BuildProxyPathPlans(paths, steps, []model.Server{source, target}, []model.Inbound{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || plans[0].Tunnels[0].TargetPort != 22 || plans[1].Tunnels[0].TargetPort != 22 {
		t.Fatalf("SSH paths did not share saved port: %#v", plans)
	}

	override := steps[:1]
	override[0].ConfigJSON = `{"type":"ssh","ssh_port":2222}`
	plans, err = BuildProxyPathPlans(paths[:1], override, []model.Server{source, target}, []model.Inbound{root})
	if err != nil {
		t.Fatal(err)
	}
	if got := plans[0].Tunnels[0].TargetPort; got != 2222 {
		t.Fatalf("step SSH port = %d, want override 2222", got)
	}

	target.SSHPort = 0
	override[0].ConfigJSON = `{"type":"ssh"}`
	if _, err := BuildProxyPathPlans(paths[:1], override, []model.Server{source, target}, []model.Inbound{root}); err == nil || !strings.Contains(err.Error(), "未设置 SSH 端口") {
		t.Fatalf("missing SSH port error = %v", err)
	}
}

func TestProxyPathWireGuardTunnelBuildsBothEndpointsAndUsesPeerAddress(t *testing.T) {
	source := model.Server{ID: 1, Name: "A", PublicIPv4: "198.51.100.10", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 1, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 80, Name: "A-WG-B", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	targetID := target.ID
	step := model.ProxyPathStep{ID: 81, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"wireguard","source_private_key":"src-private","source_public_key":"src-public","target_private_key":"dst-private","target_public_key":"dst-public","persistent_keepalive":25}`}
	opts := ConfigOptions{Servers: []model.Server{source, target}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: []model.ProxyPathStep{step}}
	plans, err := BuildProxyPathPlans(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Tunnels) != 1 {
		t.Fatalf("WireGuard path tunnel plan = %#v", plans)
	}
	logical := plans[0].Tunnels[0]
	if err := ValidateTunnels(opts.Servers, []model.Tunnel{logical}); err != nil {
		t.Fatal(err)
	}
	sourcePlan, err := BuildTunnelPlan(1, source, opts.Servers, []model.Tunnel{logical})
	if err != nil {
		t.Fatal(err)
	}
	targetPlan, err := BuildTunnelPlan(1, target, opts.Servers, []model.Tunnel{logical})
	if err != nil {
		t.Fatal(err)
	}
	if len(sourcePlan.Tunnels) != 1 || len(targetPlan.Tunnels) != 1 {
		t.Fatalf("WireGuard pair projections source=%#v target=%#v", sourcePlan, targetPlan)
	}
	if sourcePlan.Tunnels[0].TargetEndpoint != target.PublicIPv4 || targetPlan.Tunnels[0].ListenPort != logical.TargetPort || targetPlan.Tunnels[0].TargetEndpoint != "" {
		t.Fatalf("WireGuard endpoint projection source=%#v target=%#v", sourcePlan.Tunnels[0], targetPlan.Tunnels[0])
	}

	sourceConfig := parseSingBoxConfig(t, mustServerConfig(t, source, []model.Inbound{root}, nil, opts))
	var pathOutbound map[string]any
	for _, outbound := range sourceConfig.Outbounds {
		if outbound["tag"] == proxyPathStepTag(path.ID, step.Position) {
			pathOutbound = outbound
		}
	}
	if pathOutbound == nil || pathOutbound["server"] != prefixHost(logical.PeerAddress) {
		t.Fatalf("sing-box did not dial WireGuard peer: %#v", pathOutbound)
	}
}

func testSSHKeyPair(t *testing.T) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal(err)
	}
	parsedPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsedPublicKey)))
}
