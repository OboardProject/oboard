package core

import (
	"bytes"
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
	protocols := []model.Protocol{model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolMieru, model.ProtocolSnell, model.ProtocolSocks}
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
		outbound := model.Outbound{ID: 2, ServerID: 1, Name: string(protocol), Protocol: protocol, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: testOutboundConfig(protocol), Enabled: true}
		out, err := adapter.Outbound(outbound, &users[0])
		if err != nil {
			t.Fatalf("%s outbound: %v", protocol, err)
		}
		if out["server"] != "example.com" {
			t.Fatalf("%s outbound target mismatch: %#v", protocol, out)
		}
	}
}

func TestEffectiveListenIP(t *testing.T) {
	cases := []struct {
		name   string
		server model.Server
		stored string
		want   string
	}{
		{name: "dual-stack auto", server: model.Server{PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::10"}, stored: "0.0.0.0", want: "::"},
		{name: "ipv6-only", server: model.Server{PublicIPv6: "2001:db8::10"}, stored: "0.0.0.0", want: "::"},
		{name: "interface ipv6 inbound-only", server: model.Server{PublicIPv4: "203.0.113.10", InterfaceIPv6: "2400:3200::10"}, stored: "0.0.0.0", want: "::"},
		{name: "ipv4-only", server: model.Server{PublicIPv4: "203.0.113.10"}, stored: "0.0.0.0", want: "0.0.0.0"},
		{name: "unknown addresses", server: model.Server{}, stored: "0.0.0.0", want: "0.0.0.0"},
		{name: "empty stored", server: model.Server{PublicIPv6: "2001:db8::10"}, stored: "", want: "::"},
		{name: "dual mode on ipv4-only host", server: model.Server{PublicIPv4: "203.0.113.10", ListenMode: model.ListenModeDual}, stored: "0.0.0.0", want: "::"},
		{name: "ipv4-only mode on dual host", server: model.Server{PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::10", ListenMode: model.ListenModeIPv4Only}, stored: "0.0.0.0", want: "0.0.0.0"},
		{name: "ipv4-only mode ignores interface ipv6", server: model.Server{InterfaceIPv6: "2400:3200::10", ListenMode: model.ListenModeIPv4Only}, stored: "0.0.0.0", want: "0.0.0.0"},
		{name: "explicit listen overrides dual mode", server: model.Server{PublicIPv6: "2001:db8::10", ListenMode: model.ListenModeDual}, stored: "127.0.0.1", want: "127.0.0.1"},
		{name: "explicit ipv6 wildcard", server: model.Server{PublicIPv4: "203.0.113.10"}, stored: "::", want: "::"},
		{name: "specific address preserved", server: model.Server{PublicIPv6: "2001:db8::10"}, stored: "127.0.0.1", want: "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveListenIP(tc.server, tc.stored); got != tc.want {
				t.Fatalf("EffectiveListenIP(%+v, %q) = %q, want %q", tc.server, tc.stored, got, tc.want)
			}
		})
	}
}

func TestServerEntryIPv6PrefersPublicOverInterface(t *testing.T) {
	cases := []struct {
		name   string
		server model.Server
		want   string
	}{
		{name: "public wins", server: model.Server{PublicIPv6: "2001:db8::1", InterfaceIPv6: "2400:3200::1"}, want: "2001:db8::1"},
		{name: "interface fallback", server: model.Server{InterfaceIPv6: "2400:3200::2"}, want: "2400:3200::2"},
		{name: "none", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServerEntryIPv6(tc.server); got != tc.want {
				t.Fatalf("ServerEntryIPv6(%+v) = %q, want %q", tc.server, got, tc.want)
			}
		})
	}
}

func TestResolveServerEntryAddressUsesInterfaceIPv6Fallback(t *testing.T) {
	server := model.Server{EntryIPMode: model.EntryIPModeIPv6, InterfaceIPv6: "2400:3200::3"}
	if got := ResolveServerEntryAddress(server); got != "2400:3200::3" {
		t.Fatalf("ResolveServerEntryAddress = %q, want interface IPv6", got)
	}
	server = model.Server{EntryIPMode: model.EntryIPModeAuto, InterfaceIPv6: "2400:3200::4"}
	if got := ResolveServerEntryAddress(server); got != "2400:3200::4" {
		t.Fatalf("ResolveServerEntryAddress auto = %q, want interface IPv6", got)
	}
}

func TestGeneratedInboundListenFollowsDetectedFamilies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server model.Server
		want   string
	}{
		{name: "dual-stack", server: model.Server{ID: 1, Name: "dual", PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::10", ListenIP: "0.0.0.0"}, want: "::"},
		{name: "ipv6-only", server: model.Server{ID: 2, Name: "v6", PublicIPv6: "2001:db8::11", ListenIP: "0.0.0.0"}, want: "::"},
		{name: "ipv4-only", server: model.Server{ID: 3, Name: "v4", PublicIPv4: "203.0.113.11", ListenIP: "0.0.0.0"}, want: "0.0.0.0"},
		{name: "explicit specific listen", server: model.Server{ID: 4, Name: "explicit", PublicIPv6: "2001:db8::12", ListenIP: "0.0.0.0"}, want: "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listenIP := tc.server.ListenIP
			if tc.want == "127.0.0.1" {
				listenIP = "127.0.0.1"
			}
			inbound := model.Inbound{ID: 10, ServerID: tc.server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: listenIP, Port: 443, ConfigJSON: `{}`, Enabled: true}
			config, err := GenerateServerConfigWithOptions(tc.server, []model.Inbound{inbound}, nil, testDNSState(tc.server.ID), []model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111"}}, ConfigOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Inbounds []map[string]any `json:"inbounds"`
			}
			if err := json.Unmarshal([]byte(config), &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Inbounds) != 1 || parsed.Inbounds[0]["listen"] != tc.want {
				t.Fatalf("generated inbounds = %#v, want listen %q", parsed.Inbounds, tc.want)
			}
		})
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

func TestSnellGeneratedConfigAcrossUDPModes(t *testing.T) {
	for _, mode := range []model.UDPInboundMode{model.UDPInboundAllow, model.UDPInboundBlock, model.UDPInboundUoT} {
		t.Run(string(mode), func(t *testing.T) {
			server := model.Server{ID: 1, Name: "edge", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", UDPInboundMode: model.UDPInboundMode(mode)}
			v4 := model.Inbound{ID: 2, ServerID: 1, Name: "snell-v4", Protocol: model.ProtocolSnell, ListenIP: "", Port: 6160, ConfigJSON: `{"version":4,"psk":"secret-psk-1234","obfs_mode":"http","obfs_host":"bing.com"}`, Enabled: true}
			v6 := model.Inbound{ID: 3, ServerID: 1, Name: "snell-v6", Protocol: model.ProtocolSnell, ListenIP: "", Port: 7177, ConfigJSON: `{"version":6,"psk":"secret-psk-1234","mode":"unshaped"}`, Enabled: true}
			config, err := GenerateServerConfigWithOptions(server, []model.Inbound{v4, v6}, nil, testDNSState(1), []model.User{{ID: 1, Username: "alice", Status: "active", ProxyPassword: "user-pass"}}, ConfigOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var parsed SingBoxConfig
			if err := json.Unmarshal([]byte(config), &parsed); err != nil {
				t.Fatal(err)
			}
			var foundV4, foundV6 bool
			for _, inbound := range parsed.Inbounds {
				if inbound["type"] != "snell" {
					continue
				}
				switch inbound["version"] {
				case float64(5):
					foundV4 = true
					if inbound["psk"] != "secret-psk-1234" || inbound["obfs_mode"] != "http" || inbound["listen_port"] != float64(6160) {
						t.Fatalf("snell v4 inbound block = %#v", inbound)
					}
				case float64(6):
					foundV6 = true
					if inbound["psk"] != "secret-psk-1234" || inbound["mode"] != "unshaped" || inbound["listen_port"] != float64(7177) {
						t.Fatalf("snell v6 inbound block = %#v", inbound)
					}
				}
			}
			if !foundV4 || !foundV6 {
				t.Fatalf("snell inbounds not both generated (%v/%v): %s", foundV4, foundV6, config)
			}
			if strings.Contains(config, `"network": "tcp"`) {
				t.Fatalf("snell inbound must stay TCP-only listener without network field: %s", config)
			}
		})
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

func TestShadowsocksOutboundForcesUoTVersion2(t *testing.T) {
	adapter, err := AdapterFor(model.ProtocolSS)
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.Outbound(model.Outbound{
		ID:            1,
		Protocol:      model.ProtocolSS,
		TargetAddress: "example.com",
		TargetPort:    8388,
		ConfigJSON:    `{"method":"aes-128-gcm","udp_over_tcp":{"enabled":true,"version":1}}`,
		Enabled:       true,
	}, &model.User{ProxyPassword: "password"})
	if err != nil {
		t.Fatal(err)
	}
	uot, ok := out["udp_over_tcp"].(map[string]any)
	if !ok || !boolValue(uot["enabled"]) || intFromAny(uot["version"]) != shadowsocksUoTVersion {
		t.Fatalf("udp_over_tcp = %#v, want enabled sp.v%d", out["udp_over_tcp"], shadowsocksUoTVersion)
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
	sharedTag := proxyPathChainServiceTag(proxyPathChainServiceKey{Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod})
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

func TestProxyPathStageRulesRunBeforeEachStageContinuation(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	serverC := model.Server{ID: 3, Name: "C", PublicIPv4: "203.0.113.3", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 32000, PortRangeEnd: 32100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Name: "A-B-C", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	serverBID, serverCID := serverB.ID, serverC.ID
	stepB := model.ProxyPathStep{ID: 101, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID}
	stepC := model.ProxyPathStep{ID: 102, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverCID}
	pathID, stepBID, stepCID, ruleSetID := path.ID, stepB.ID, stepC.ID, int64(9)
	opts := ConfigOptions{
		Servers:        []model.Server{serverA, serverB, serverC},
		Inbounds:       []model.Inbound{root},
		ProxyPaths:     []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{stepB, stepC},
		InboundUsers:   []model.InboundUser{{InboundID: root.ID, UserID: 1, Enabled: true}},
		RoutingRules: []model.RoutingRule{
			{ID: 1, ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID, SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "at-a", MatchJSON: `{"domain":["a.example"]}`, Action: model.RouteActionDirect, Enabled: true},
			{ID: 2, ServerID: serverB.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID, StageStepID: &stepBID, SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "at-b", MatchJSON: `{"domain":["b.example"]}`, Action: model.RouteActionBlock, Enabled: true},
			{ID: 3, ServerID: serverC.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID, StageStepID: &stepCID, SortPosition: 0, MatchSource: model.RoutingMatchSourceRuleSet, RuleSetID: &ruleSetID, Name: "at-c", Action: model.RouteActionDirect, Enabled: true},
		},
		RoutingRuleSets: []model.RoutingRuleSet{{ID: ruleSetID, Name: "remote", Format: model.RoutingRuleSetFormatSingBoxSource, Revision: "abc"}},
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	tests := []struct {
		server       model.Server
		matchKey     string
		matchValue   string
		continuation string
	}{
		{serverA, "domain", "a.example", "path-50-step-1"},
		{serverB, "domain", "b.example", "path-50-step-2"},
		{serverC, "rule_set", "routing-rule-set-9", ""},
	}
	for _, test := range tests {
		t.Run(test.server.Name, func(t *testing.T) {
			config := mustServerConfig(t, test.server, []model.Inbound{root}, []model.User{user}, opts)
			var parsed SingBoxConfig
			if err := json.Unmarshal([]byte(config), &parsed); err != nil {
				t.Fatal(err)
			}
			rules := mapList(parsed.Route["rules"])
			if len(rules) == 0 {
				t.Fatalf("stage rules missing: %s", config)
			}
			values, ok := rules[0][test.matchKey].([]any)
			if !ok || len(values) != 1 || values[0] != test.matchValue {
				t.Fatalf("first stage rule match = %#v, want %s=%s; config=%s", rules[0], test.matchKey, test.matchValue, config)
			}
			if test.continuation != "" && (len(rules) < 2 || rules[1]["outbound"] != test.continuation) {
				t.Fatalf("stage continuation did not follow custom rule: %#v", rules)
			}
			if rules[0]["inbound"] == nil || rules[0]["auth_user"] == nil {
				t.Fatalf("stage rule lacks branch identity: %#v", rules[0])
			}
		})
	}
}

func TestFamilySplitOutboundsForceTargetEntryFamilies(t *testing.T) {
	tests := []struct {
		name       string
		ipv4Server model.Server
		ipv6Server model.Server
	}{
		{
			name:       "single stack targets",
			ipv4Server: model.Server{ID: 2, Name: "v4", PublicIPv4: "203.0.113.4", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, Status: model.ServerOnline, PortRangeStart: 31000, PortRangeEnd: 31100},
			ipv6Server: model.Server{ID: 3, Name: "v6", PublicIPv6: "2001:db8::6", ListenIP: "::", IPStack: model.IPStackIPv6Only, Status: model.ServerOnline, PortRangeStart: 32000, PortRangeEnd: 32100},
		},
		{
			name:       "dual stack targets",
			ipv4Server: model.Server{ID: 2, Name: "dual-v4", PublicIPv4: "203.0.113.14", PublicIPv6: "2001:db8::14", ListenIP: "::", IPStack: model.IPStackDualStack, Status: model.ServerOnline, PortRangeStart: 31000, PortRangeEnd: 31100},
			ipv6Server: model.Server{ID: 3, Name: "dual-v6", PublicIPv4: "203.0.113.16", PublicIPv6: "2001:db8::16", ListenIP: "::", IPStack: model.IPStackDualStack, Status: model.ServerOnline, PortRangeStart: 32000, PortRangeEnd: 32100},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := model.Server{ID: 1, Name: "entry", PublicIPv4: "203.0.113.1", PublicIPv6: "2001:db8::1", ListenIP: "::", IPStack: model.IPStackDualStack, Status: model.ServerOnline, PortRangeStart: 30000, PortRangeEnd: 30100}
			root := model.Inbound{ID: 10, ServerID: entry.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "::", Port: 443, ConfigJSON: `{}`, Enabled: true}
			ipv4Path := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindChain, Name: "v4-path", InboundID: root.ID, Secret: "v4-path", Enabled: true}
			ipv6Path := model.ProxyPath{ID: 51, Kind: model.ProxyPathKindChain, Name: "v6-path", InboundID: root.ID, Secret: "v6-path", Enabled: true}
			ipv4ServerID, ipv6ServerID := test.ipv4Server.ID, test.ipv6Server.ID
			ipv4Step := model.ProxyPathStep{ID: 101, PathID: ipv4Path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &ipv4ServerID}
			ipv6Step := model.ProxyPathStep{ID: 201, PathID: ipv6Path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &ipv6ServerID}
			ipv4PathID, ipv6PathID := ipv4Path.ID, ipv6Path.ID
			rule := model.RoutingRule{
				ID: 7, ServerID: entry.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &ipv4PathID,
				SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "family", MatchJSON: `{}`,
				Action: model.RouteActionFamilySplit, IPv4TargetProxyPathID: &ipv4PathID, IPv6TargetProxyPathID: &ipv6PathID,
				FamilyDNSStrategy: model.FamilyDNSStrategyAuto, Enabled: true,
			}
			user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
			config := mustServerConfig(t, entry, []model.Inbound{root}, []model.User{user}, ConfigOptions{
				Servers: []model.Server{entry, test.ipv4Server, test.ipv6Server}, Inbounds: []model.Inbound{root},
				ProxyPaths: []model.ProxyPath{ipv4Path, ipv6Path}, ProxyPathSteps: []model.ProxyPathStep{ipv4Step, ipv6Step},
				InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}}, RoutingRules: []model.RoutingRule{rule},
			})
			selectorTag := routingRuleFamilySelectorTag(rule.ID)
			selector := findOutbound(config, selectorTag)
			if selector["type"] != "family-selector" || selector["strategy"] != "prefer_ipv4" || selector["fallback"] != true {
				t.Fatalf("family selector = %#v; config=%s", selector, config)
			}
			ipv4BranchTag := routingRuleFamilyBranchTag(rule.ID, "ipv4", proxyPathStepTag(ipv4Path.ID, ipv4Step.Position))
			ipv6BranchTag := routingRuleFamilyBranchTag(rule.ID, "ipv6", proxyPathStepTag(ipv6Path.ID, ipv6Step.Position))
			if selector["ipv4_outbound"] != ipv4BranchTag || selector["ipv6_outbound"] != ipv6BranchTag {
				t.Fatalf("family selector children = %#v", selector)
			}
			if branch := findOutbound(config, ipv4BranchTag); branch["server"] != test.ipv4Server.PublicIPv4 {
				t.Fatalf("IPv4 branch entry = %#v, want %s", branch, test.ipv4Server.PublicIPv4)
			}
			if branch := findOutbound(config, ipv6BranchTag); branch["server"] != test.ipv6Server.PublicIPv6 {
				t.Fatalf("IPv6 branch entry = %#v, want %s", branch, test.ipv6Server.PublicIPv6)
			}
			baseIPv4 := findOutbound(config, proxyPathStepTag(ipv4Path.ID, ipv4Step.Position))
			baseIPv6 := findOutbound(config, proxyPathStepTag(ipv6Path.ID, ipv6Step.Position))
			if baseIPv4["server"] == nil || baseIPv6["server"] == nil {
				t.Fatalf("shared path outbounds were lost: v4=%#v v6=%#v", baseIPv4, baseIPv6)
			}
			matched := false
			for _, route := range mapList(parseSingBoxConfig(t, config).Route["rules"]) {
				if route["outbound"] == selectorTag && len(stringList(route["auth_user"])) == 1 {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("family selector route missing: %s", config)
			}
		})
	}
}

func TestNormalizedFamilyDNSStrategy(t *testing.T) {
	ipv4Server := model.Server{PublicIPv4: "203.0.113.1", IPStack: model.IPStackAuto}
	ipv6Server := model.Server{PublicIPv6: "2001:db8::1", IPStack: model.IPStackAuto}
	dualServer := model.Server{PublicIPv4: "203.0.113.2", PublicIPv6: "2001:db8::2", IPStack: model.IPStackAuto}
	for _, test := range []struct {
		name      string
		strategy  model.FamilyDNSStrategy
		server    model.Server
		inherited string
		want      string
	}{
		{name: "auto IPv4", strategy: model.FamilyDNSStrategyAuto, server: ipv4Server, want: "prefer_ipv4"},
		{name: "auto IPv6", strategy: model.FamilyDNSStrategyAuto, server: ipv6Server, want: "prefer_ipv6"},
		{name: "auto dual default", strategy: model.FamilyDNSStrategyAuto, server: dualServer, want: "prefer_ipv4"},
		{name: "auto inherits server DNS policy", strategy: model.FamilyDNSStrategyAuto, server: dualServer, inherited: "prefer_ipv6", want: "prefer_ipv6"},
		{name: "explicit IPv4", strategy: model.FamilyDNSStrategyPreferIPv4, server: ipv6Server, want: "prefer_ipv4"},
		{name: "explicit IPv6", strategy: model.FamilyDNSStrategyPreferIPv6, server: ipv4Server, want: "prefer_ipv6"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizedFamilyDNSStrategy(test.strategy, test.server, test.inherited)
			if err != nil || got != test.want {
				t.Fatalf("strategy=%q err=%v, want %q", got, err, test.want)
			}
		})
	}
}

func TestResolveReachableEntryAddressForFamilyMatrix(t *testing.T) {
	sourceDual := model.Server{Name: "source-dual", PublicIPv4: "203.0.113.1", PublicIPv6: "2001:db8::1", IPStack: model.IPStackDualStack}
	targetDual := model.Server{Name: "target-dual", PublicIPv4: "203.0.113.2", PublicIPv6: "2001:db8::2", ListenIP: "::", IPStack: model.IPStackDualStack}
	inbound := model.Inbound{ListenIP: "::", EntryIPMode: model.EntryIPModeAuto, Enabled: true}
	if got, err := ResolveReachableEntryAddressForFamily(sourceDual, inbound, targetDual, "ipv4"); err != nil || got != targetDual.PublicIPv4 {
		t.Fatalf("IPv4 family address=%q err=%v", got, err)
	}
	if got, err := ResolveReachableEntryAddressForFamily(sourceDual, inbound, targetDual, "ipv6"); err != nil || got != targetDual.PublicIPv6 {
		t.Fatalf("IPv6 family address=%q err=%v", got, err)
	}
	managed := inbound
	managed.DNSSyncEnabled = true
	managed.DNSDomain = "edge.example.com"
	if got, err := ResolveReachableEntryAddressForFamily(sourceDual, managed, targetDual, "ipv6"); err != nil || got != managed.DNSDomain {
		t.Fatalf("managed IPv6 family address=%q err=%v", got, err)
	}
	sourceV4 := model.Server{Name: "source-v4", PublicIPv4: "203.0.113.3", IPStack: model.IPStackIPv4Only}
	if _, err := ResolveReachableEntryAddressForFamily(sourceV4, inbound, targetDual, "ipv6"); err == nil || !errors.Is(err, ErrInvalidDesiredState) {
		t.Fatalf("IPv4-only source reached IPv6 branch: %v", err)
	}
	missingV6 := targetDual
	missingV6.PublicIPv6 = ""
	if _, err := ResolveReachableEntryAddressForFamily(sourceDual, inbound, missingV6, "ipv6"); err == nil || !strings.Contains(err.Error(), "缺少 IPV6") {
		t.Fatalf("missing IPv6 entry was accepted: %v", err)
	}
	ipv4Listener := targetDual
	ipv4Listener.ListenMode = model.ListenModeIPv4Only
	inbound.ListenIP = "0.0.0.0"
	if _, err := ResolveReachableEntryAddressForFamily(sourceDual, inbound, ipv4Listener, "ipv6"); err == nil || !strings.Contains(err.Error(), "监听地址") {
		t.Fatalf("IPv4-only listener accepted IPv6 branch: %v", err)
	}
}

func TestPathStageInterfaceRuleFollowsContinuationThroughBoundNextHop(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindChain, Name: "A-B", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	serverBID, pathID := serverB.ID, path.ID
	stepB := model.ProxyPathStep{ID: 101, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID}
	rule := model.RoutingRule{
		ID: 7, ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID,
		SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "all-via-eth0", MatchJSON: `{}`,
		Action: model.RouteActionInterface, InterfaceName: "eth0", Enabled: true,
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	config := mustServerConfig(t, serverA, []model.Inbound{root}, []model.User{user}, ConfigOptions{
		Servers: []model.Server{serverA, serverB}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{stepB}, InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
		RoutingRules: []model.RoutingRule{rule},
	})

	baseTag := proxyPathStepTag(path.ID, stepB.Position)
	boundTag := routingRuleBoundOutboundTag(rule.ID, baseTag)
	bound := findOutbound(config, boundTag)
	if len(bound) == 0 || bound["bind_interface"] != "eth0" {
		t.Fatalf("bound continuation %q = %#v, want bind_interface=eth0; config=%s", boundTag, bound, config)
	}
	if terminal := findOutbound(config, routingRuleInterfaceOutboundTag(rule.ID)); len(terminal) != 0 {
		t.Fatalf("terminal interface outbound should be omitted when continuation exists: %#v", terminal)
	}
	base := findOutbound(config, baseTag)
	if base["bind_interface"] != nil {
		t.Fatalf("shared continuation was modified: %#v", base)
	}
	routes := mapList(parseSingBoxConfig(t, config).Route["rules"])
	if len(routes) < 2 || routes[0]["action"] != "route" || routes[0]["outbound"] != boundTag {
		t.Fatalf("first path-stage rule = %#v, want route through %q; config=%s", routes, boundTag, config)
	}
	if routes[1]["outbound"] != baseTag {
		t.Fatalf("unmatched fallback changed unexpectedly: %#v", routes)
	}
}

func TestPathStageInterfaceRuleOnSnellFollowsContinuationWithoutAuthUser(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "LQ", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "Cogent", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "LQ-snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 11787, ConfigJSON: `{"version":4,"psk":"secret-psk-1234"}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindDirect, Name: "LQ | Cogent", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	serverBID, pathID := serverB.ID, path.ID
	stepB := model.ProxyPathStep{ID: 101, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID}
	rule := model.RoutingRule{
		ID: 7, ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID,
		SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "LQ-route", MatchJSON: `{}`,
		Action: model.RouteActionInterface, InterfaceName: "eth0", Enabled: true,
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	config := mustServerConfig(t, serverA, []model.Inbound{root}, []model.User{user}, ConfigOptions{
		Servers: []model.Server{serverA, serverB}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{stepB}, InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
		RoutingRules: []model.RoutingRule{rule},
	})

	baseTag := proxyPathStepTag(path.ID, stepB.Position)
	boundTag := routingRuleBoundOutboundTag(rule.ID, baseTag)
	bound := findOutbound(config, boundTag)
	if len(bound) == 0 || bound["bind_interface"] != "eth0" {
		t.Fatalf("bound continuation %q = %#v, want bind_interface=eth0; config=%s", boundTag, bound, config)
	}
	routes := mapList(parseSingBoxConfig(t, config).Route["rules"])
	if len(routes) < 2 || routes[0]["outbound"] != boundTag {
		t.Fatalf("first path-stage rule = %#v, want route through %q; config=%s", routes, boundTag, config)
	}
	if _, hasAuth := routes[0]["auth_user"]; !hasAuth {
		t.Fatalf("Snell path-stage rule must constrain auth_user: %#v", routes[0])
	}
	if routes[1]["outbound"] != baseTag {
		t.Fatalf("unmatched fallback = %#v, want %q", routes[1], baseTag)
	}
	if _, hasAuth := routes[1]["auth_user"]; !hasAuth {
		t.Fatalf("Snell fallback must constrain auth_user: %#v", routes[1])
	}
}

func TestPathStageInterfaceRuleSkipsBindWhenInterfaceLacksGlobalIPv4(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "LQ", PublicIPv4: "116.192.3.132", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "Cogent", PublicIPv4: "82.29.38.156", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "LQ-snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 11787, ConfigJSON: `{"version":4,"psk":"secret-psk-1234"}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindDirect, Name: "LQ | Cogent", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	serverBID, pathID := serverB.ID, path.ID
	stepB := model.ProxyPathStep{ID: 101, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID}
	rule := model.RoutingRule{
		ID: 7, ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID,
		SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "LQ-route", MatchJSON: `{}`,
		Action: model.RouteActionInterface, InterfaceName: "eth0", Enabled: true,
		InterfaceBindKnown: true, InterfaceHasGlobalIPv4: false, InterfaceHasGlobalIPv6: true,
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	config := mustServerConfig(t, serverA, []model.Inbound{root}, []model.User{user}, ConfigOptions{
		Servers: []model.Server{serverA, serverB}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{stepB}, InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
		RoutingRules: []model.RoutingRule{rule},
	})
	baseTag := proxyPathStepTag(path.ID, stepB.Position)
	boundTag := routingRuleBoundOutboundTag(rule.ID, baseTag)
	bound := findOutbound(config, boundTag)
	if len(bound) == 0 {
		t.Fatalf("continuation clone %q missing; config=%s", boundTag, config)
	}
	if bound["bind_interface"] != nil {
		t.Fatalf("IPv4 hop must not bind an interface without global IPv4: %#v", bound)
	}
	if bound["server"] != "82.29.38.156" {
		t.Fatalf("continuation dest = %#v, want Cogent IPv4", bound["server"])
	}
	routes := mapList(parseSingBoxConfig(t, config).Route["rules"])
	if len(routes) < 1 || routes[0]["outbound"] != boundTag {
		t.Fatalf("path-stage rule = %#v, want continuation %q", routes, boundTag)
	}
}

func TestPathStageInterfaceRuleWithoutContinuationStaysTerminal(t *testing.T) {
	server := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	root := model.Inbound{ID: 10, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindDirect, Name: "direct", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	pathID := path.ID
	rule := model.RoutingRule{
		ID: 8, ServerID: server.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID,
		SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "ssh-via-eth0", MatchJSON: `{"port":[22]}`,
		Action: model.RouteActionInterface, InterfaceName: "eth0", Enabled: true,
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	config := mustServerConfig(t, server, []model.Inbound{root}, []model.User{user}, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path},
		InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
		RoutingRules: []model.RoutingRule{rule},
	})

	boundTag := routingRuleInterfaceOutboundTag(rule.ID)
	bound := findOutbound(config, boundTag)
	if bound["type"] != "direct" || bound["bind_interface"] != "eth0" {
		t.Fatalf("terminal interface outbound = %#v, want direct outbound on eth0; config=%s", bound, config)
	}
	routes := mapList(parseSingBoxConfig(t, config).Route["rules"])
	if len(routes) < 2 || routes[0]["outbound"] != boundTag || routes[1]["outbound"] != "direct" {
		t.Fatalf("path-stage rules = %#v, want terminal interface then unmatched direct; config=%s", routes, config)
	}
}

func TestPathStageInterfaceRuleFollowsWARPContinuationThroughBoundEndpoint(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	root := model.Inbound{ID: 10, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindChain, Name: "warp-fallback", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	warpStep := model.ProxyPathStep{ID: 201, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepWARP, TransportMode: model.ProxyPathTransportSingBox}
	pathID := path.ID
	rule := model.RoutingRule{
		ID: 9, ServerID: server.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID,
		SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "all-via-eth1", MatchJSON: `{}`,
		Action: model.RouteActionInterface, InterfaceName: "eth1", InterfaceIPStack: model.IPStackIPv4Only, Enabled: true,
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	profile := model.WARPProfile{ID: 30, ServerID: server.ID, Name: "warp", Status: model.WARPStatusReady, ConfigJSON: `{"type":"wireguard","address":["172.16.0.2/32"],"private_key":"private","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"public","allowed_ips":["0.0.0.0/0","::/0"]}]}`, Enabled: true}
	config := mustServerConfig(t, server, []model.Inbound{root}, []model.User{user}, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{warpStep}, WARPProfiles: []model.WARPProfile{profile},
		InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
		RoutingRules: []model.RoutingRule{rule},
	})

	baseTag := WARPOutboundTag(profile.ID)
	boundTag := routingRuleBoundOutboundTag(rule.ID, baseTag)
	base := findEndpoint(config, baseTag)
	bound := findEndpoint(config, boundTag)
	if len(base) == 0 || len(bound) == 0 {
		t.Fatalf("base or bound WARP endpoint missing: %s", config)
	}
	if base["bind_interface"] != nil {
		t.Fatalf("shared WARP endpoint was modified: %#v", base)
	}
	if bound["bind_interface"] != "eth1" {
		t.Fatalf("bound WARP endpoint = %#v, want bind_interface=eth1", bound)
	}
	if terminal := findOutbound(config, routingRuleInterfaceOutboundTag(rule.ID)); len(terminal) != 0 {
		t.Fatalf("terminal interface outbound should be omitted when WARP continuation exists: %#v", terminal)
	}
	routes := mapList(parseSingBoxConfig(t, config).Route["rules"])
	if len(routes) < 2 || routes[0]["outbound"] != boundTag {
		t.Fatalf("first path-stage rule = %#v, want route through %q; config=%s", routes, boundTag, config)
	}
	if routes[1]["outbound"] != baseTag {
		t.Fatalf("unmatched fallback changed unexpectedly: %#v", routes)
	}
}

func TestProxyPathStageRuleDoesNotAffectSiblingBranch(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	pathA := model.ProxyPath{ID: 50, Name: "branch-a", InboundID: root.ID, Secret: "path-a-secret", Enabled: true}
	pathB := model.ProxyPath{ID: 51, Name: "branch-b", InboundID: root.ID, Secret: "path-b-secret", Enabled: true}
	serverBID, pathAID := serverB.ID, pathA.ID
	opts := ConfigOptions{
		Servers:    []model.Server{serverA, serverB},
		Inbounds:   []model.Inbound{root},
		ProxyPaths: []model.ProxyPath{pathA, pathB},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 101, PathID: pathA.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
			{ID: 102, PathID: pathB.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
		},
		InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: 1, Enabled: true}},
		RoutingRules: []model.RoutingRule{{ID: 1, ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathAID, SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "only-a", MatchJSON: `{"domain":["a.example"]}`, Action: model.RouteActionDirect, Enabled: true}},
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	config := mustServerConfig(t, serverA, []model.Inbound{root}, []model.User{user}, opts)
	parsed := parseSingBoxConfig(t, config)
	var customUsers, pathAUsers, pathBUsers []string
	for _, rule := range mapList(parsed.Route["rules"]) {
		users := stringList(rule["auth_user"])
		switch rule["outbound"] {
		case "direct":
			if domains := stringList(rule["domain"]); len(domains) == 1 && domains[0] == "a.example" {
				customUsers = users
			}
		case "path-50-step-1":
			pathAUsers = users
		case "path-51-step-1":
			pathBUsers = users
		}
	}
	if len(customUsers) != 1 || len(pathAUsers) != 1 || len(pathBUsers) != 1 {
		t.Fatalf("branch routes are incomplete: custom=%v path-a=%v path-b=%v config=%s", customUsers, pathAUsers, pathBUsers, config)
	}
	if customUsers[0] != pathAUsers[0] || customUsers[0] == pathBUsers[0] {
		t.Fatalf("path-a rule leaked across branch identity: custom=%v path-a=%v path-b=%v", customUsers, pathAUsers, pathBUsers)
	}
}

func TestProxyPathStageRuleCanSwitchToIndependentTargetPathBeforeFallback(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	serverC := model.Server{ID: 3, Name: "C", PublicIPv4: "203.0.113.3", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 32000, PortRangeEnd: 32100}
	serverD := model.Server{ID: 4, Name: "D", PublicIPv4: "203.0.113.4", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 33000, PortRangeEnd: 33100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	fallback := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindChain, Name: "A-B-C", InboundID: root.ID, Secret: "fallback-secret", Enabled: true}
	target := model.ProxyPath{ID: 51, Kind: model.ProxyPathKindChain, Name: "A-B-D", InboundID: root.ID, Secret: "target-secret", Enabled: true}
	serverBID, serverCID, serverDID := serverB.ID, serverC.ID, serverD.ID
	fallbackB := model.ProxyPathStep{ID: 101, PathID: fallback.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID}
	fallbackC := model.ProxyPathStep{ID: 102, PathID: fallback.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverCID}
	targetB := model.ProxyPathStep{ID: 201, PathID: target.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID}
	targetD := model.ProxyPathStep{ID: 202, PathID: target.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverDID}
	fallbackID, fallbackBID, targetID := fallback.ID, fallbackB.ID, target.ID
	opts := ConfigOptions{
		Servers:        []model.Server{serverA, serverB, serverC, serverD},
		Inbounds:       []model.Inbound{root},
		ProxyPaths:     []model.ProxyPath{fallback, target},
		ProxyPathSteps: []model.ProxyPathStep{fallbackB, fallbackC, targetB, targetD},
		InboundUsers:   []model.InboundUser{{InboundID: root.ID, UserID: 1, Enabled: true}},
		RoutingRules: []model.RoutingRule{{
			ID: 1, ServerID: serverB.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &fallbackID, StageStepID: &fallbackBID,
			SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: "switch-at-b", MatchJSON: `{"domain":["special.example"]}`,
			Action: model.RouteActionProxyPath, TargetProxyPathID: &targetID, Enabled: true,
		}},
	}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	configB := mustServerConfig(t, serverB, []model.Inbound{root}, []model.User{user}, opts)
	parsed := parseSingBoxConfig(t, configB)
	rules := mapList(parsed.Route["rules"])
	matchIndex := -1
	var branchUsers []string
	for index, rule := range rules {
		if domains := stringList(rule["domain"]); len(domains) == 1 && domains[0] == "special.example" {
			matchIndex = index
			branchUsers = stringList(rule["auth_user"])
			if rule["outbound"] != "path-51-step-2" {
				t.Fatalf("matched rule outbound = %#v, want target path hop; config=%s", rule, configB)
			}
			break
		}
	}
	if matchIndex < 0 || len(branchUsers) != 1 {
		t.Fatalf("target-path rule or branch identity missing: %s", configB)
	}
	fallbackFound := false
	for _, rule := range rules[matchIndex+1:] {
		users := stringList(rule["auth_user"])
		if rule["outbound"] == "path-50-step-2" && len(users) == 1 && users[0] == branchUsers[0] {
			fallbackFound = true
			break
		}
	}
	if !fallbackFound {
		t.Fatalf("fallback continuation did not remain after target-path rule: %s", configB)
	}
	configC := mustServerConfig(t, serverC, []model.Inbound{root}, []model.User{user}, opts)
	if strings.Contains(configC, "special.example") {
		t.Fatalf("rule leaked into fallback destination server: %s", configC)
	}
}

func TestProxyPathStageRuleSpecificPathInheritsEgressBinding(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	fallback := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindDirect, Name: "fallback", InboundID: root.ID, Secret: "fallback-secret", Enabled: true}
	target := model.ProxyPath{ID: 51, Kind: model.ProxyPathKindChain, Name: "target", InboundID: root.ID, Secret: "target-secret", Enabled: true}
	serverBID := serverB.ID
	externalID := int64(30)
	external := model.ExternalOutbound{ID: externalID, Name: "socks", Protocol: model.ProtocolSocks, TargetAddress: "socks.example.com", TargetPort: 1080, ConfigJSON: `{"type":"socks","server":"socks.example.com","server_port":1080}`, Enabled: true}
	targetProxyStep := model.ProxyPathStep{ID: 201, PathID: target.ID, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID}
	targetServerStep := model.ProxyPathStep{ID: 202, PathID: target.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID}
	fallbackID, targetID := fallback.ID, target.ID
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}

	tests := []struct {
		name          string
		interfaceName string
		sourcePrefix  string
	}{
		{name: "interface", interfaceName: "eth1"},
		{name: "source prefix", sourcePrefix: "198.51.100.0/24"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := model.RoutingRule{
				ID: int64(70 + index), ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &fallbackID,
				SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: test.name, MatchJSON: `{"domain":["bound.example"]}`,
				Action: model.RouteActionProxyPath, TargetProxyPathID: &targetID, InterfaceName: test.interfaceName, SourcePrefix: test.sourcePrefix, Enabled: true,
			}
			opts := ConfigOptions{
				Servers:           []model.Server{serverA, serverB},
				Inbounds:          []model.Inbound{root},
				ProxyPaths:        []model.ProxyPath{fallback, target},
				ProxyPathSteps:    []model.ProxyPathStep{targetProxyStep, targetServerStep},
				ExternalOutbounds: []model.ExternalOutbound{external},
				InboundUsers:      []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
				RoutingRules:      []model.RoutingRule{rule},
			}
			config := mustServerConfig(t, serverA, []model.Inbound{root}, []model.User{user}, opts)
			parsed := parseSingBoxConfig(t, config)
			baseTag := proxyPathStepTag(target.ID, targetServerStep.Position)
			baseDialerTag := proxyPathStepTag(target.ID, targetProxyStep.Position)
			boundTag := routingRuleBoundOutboundTag(rule.ID, baseTag)
			bound := findOutbound(config, boundTag)
			if len(bound) == 0 {
				t.Fatalf("bound first hop %q missing: %s", boundTag, config)
			}
			boundDialerTag := routingRuleBoundOutboundTag(rule.ID, baseDialerTag)
			if bound["detour"] != boundDialerTag {
				t.Fatalf("bound route outbound = %#v, want detour=%q", bound, boundDialerTag)
			}
			boundDialer := findOutbound(config, boundDialerTag)
			if test.interfaceName != "" && boundDialer["bind_interface"] != test.interfaceName {
				t.Fatalf("bound first hop = %#v, want bind_interface=%q", boundDialer, test.interfaceName)
			}
			if test.sourcePrefix != "" {
				prefixTag := sourcePrefixOutboundTag(test.sourcePrefix)
				if boundDialer["detour"] != prefixTag {
					t.Fatalf("bound first hop = %#v, want detour=%q", boundDialer, prefixTag)
				}
				if prefixOutbound := findOutbound(config, prefixTag); prefixOutbound["type"] != "source-prefix" || prefixOutbound["prefix"] != test.sourcePrefix {
					t.Fatalf("source-prefix outbound = %#v", prefixOutbound)
				}
			}
			base := findOutbound(config, baseTag)
			baseDialer := findOutbound(config, baseDialerTag)
			if base["bind_interface"] != nil || base["detour"] != baseDialerTag || baseDialer["bind_interface"] != nil || baseDialer["detour"] != nil {
				t.Fatalf("shared target path was modified by rule binding: %#v", base)
			}

			routes := mapList(parsed.Route["rules"])
			matched := false
			fallbackDirect := false
			var matchedUsers []string
			for _, route := range routes {
				if domains := stringList(route["domain"]); len(domains) == 1 && domains[0] == "bound.example" {
					matched = route["outbound"] == boundTag
					matchedUsers = stringList(route["auth_user"])
					continue
				}
				users := stringList(route["auth_user"])
				if route["outbound"] == "direct" && len(users) == 1 && len(matchedUsers) == 1 && users[0] == matchedUsers[0] {
					fallbackDirect = true
				}
			}
			if !matched || !fallbackDirect {
				t.Fatalf("bound route or implicit direct fallback missing: matched=%v fallback=%v config=%s", matched, fallbackDirect, config)
			}
		})
	}
}

func TestProxyPathStageRuleSpecificWARPInheritsEgressBinding(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	root := model.Inbound{ID: 10, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	fallback := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindDirect, Name: "fallback", InboundID: root.ID, Secret: "fallback-secret", Enabled: true}
	target := model.ProxyPath{ID: 51, Kind: model.ProxyPathKindChain, Name: "warp", InboundID: root.ID, Secret: "target-secret", Enabled: true}
	warpStep := model.ProxyPathStep{ID: 201, PathID: target.ID, Position: 1, NodeType: model.ProxyPathStepWARP, TransportMode: model.ProxyPathTransportSingBox}
	fallbackID, targetID := fallback.ID, target.ID
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	readyConfig := `{"type":"wireguard","address":["172.16.0.2/32"],"private_key":"private","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"public","allowed_ips":["0.0.0.0/0","::/0"]}]}`

	tests := []struct {
		name             string
		status           model.WARPStatus
		configJSON       string
		interfaceName    string
		interfaceIPStack model.IPStack
		sourcePrefix     string
		wantDNSStrategy  string
	}{
		{name: "ready IPv6 interface", status: model.WARPStatusReady, configJSON: readyConfig, interfaceName: "eth1", interfaceIPStack: model.IPStackIPv6Only, wantDNSStrategy: "ipv6_only"},
		{name: "ready IPv6 source prefix", status: model.WARPStatusReady, configJSON: readyConfig, sourcePrefix: "2001:db8:100::/64", wantDNSStrategy: "ipv6_only"},
		{name: "pending IPv4 interface", status: model.WARPStatusRequested, configJSON: `{}`, interfaceName: "eth1", interfaceIPStack: model.IPStackIPv4Only, wantDNSStrategy: "ipv4_only"},
		{name: "pending IPv6 source prefix", status: model.WARPStatusRequested, configJSON: `{}`, sourcePrefix: "2001:db8:100::/64", wantDNSStrategy: "ipv6_only"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := model.RoutingRule{
				ID: int64(80 + index), ServerID: server.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &fallbackID,
				SortPosition: 0, MatchSource: model.RoutingMatchSourceInline, Name: test.name, MatchJSON: `{"domain":["warp.example"]}`,
				Action: model.RouteActionProxyPath, TargetProxyPathID: &targetID, InterfaceName: test.interfaceName, InterfaceIPStack: test.interfaceIPStack, SourcePrefix: test.sourcePrefix, Enabled: true,
			}
			profile := model.WARPProfile{ID: 30, ServerID: server.ID, Name: "warp", Status: test.status, ConfigJSON: test.configJSON, Enabled: true}
			config := mustServerConfig(t, server, []model.Inbound{root}, []model.User{user}, ConfigOptions{
				Servers: []model.Server{server}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{fallback, target},
				ProxyPathSteps: []model.ProxyPathStep{warpStep}, WARPProfiles: []model.WARPProfile{profile},
				InboundUsers: []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}}, RoutingRules: []model.RoutingRule{rule},
			})
			baseTag := WARPOutboundTag(profile.ID)
			boundTag := routingRuleBoundOutboundTag(rule.ID, baseTag)
			base := findEndpoint(config, baseTag)
			bound := findEndpoint(config, boundTag)
			if len(base) == 0 || len(bound) == 0 {
				t.Fatalf("base or bound WARP endpoint missing: %s", config)
			}
			if base["bind_interface"] != nil || base["detour"] != nil {
				t.Fatalf("shared WARP endpoint was modified: %#v", base)
			}
			if test.interfaceName != "" && bound["bind_interface"] != test.interfaceName {
				t.Fatalf("bound WARP endpoint = %#v, want bind_interface=%q", bound, test.interfaceName)
			}
			if test.sourcePrefix != "" {
				prefixTag := sourcePrefixOutboundTag(test.sourcePrefix)
				if bound["detour"] != prefixTag {
					t.Fatalf("bound WARP endpoint = %#v, want detour=%q", bound, prefixTag)
				}
				if prefixOutbound := findOutbound(config, prefixTag); prefixOutbound["type"] != "source-prefix" || prefixOutbound["prefix"] != test.sourcePrefix {
					t.Fatalf("source-prefix outbound = %#v", prefixOutbound)
				}
			}
			resolver, ok := bound["domain_resolver"].(map[string]any)
			if !ok || resolver["server"] != primaryBootstrapDNSTag || resolver["strategy"] != test.wantDNSStrategy {
				t.Fatalf("bound WARP domain_resolver = %#v, want strategy=%q", bound["domain_resolver"], test.wantDNSStrategy)
			}
			if test.status != model.WARPStatusReady && bound["_oboard_warp_pending"] != float64(profile.ID) {
				t.Fatalf("bound pending WARP endpoint lost profile marker: %#v", bound)
			}
			matched := false
			for _, route := range mapList(parseSingBoxConfig(t, config).Route["rules"]) {
				if domains := stringList(route["domain"]); len(domains) == 1 && domains[0] == "warp.example" {
					matched = route["outbound"] == boundTag
				}
			}
			if !matched {
				t.Fatalf("routing rule does not target bound WARP endpoint: %s", config)
			}
		})
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
	sharedTag := proxyPathChainServiceTag(proxyPathChainServiceKey{Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod})
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
	subscriptionNodes, err := BuildSubscriptionNodes(user, []model.Server{serverA, serverB, serverC}, []model.Inbound{rootInbound}, SubscriptionOptions{
		EffectiveNodes: map[string]bool{
			NodeKeyOf(model.AssignableNodeProxyPath, chain.ID):   true,
			NodeKeyOf(model.AssignableNodeProxyPath, direct.ID):  true,
			NodeKeyOf(model.AssignableNodeProxyPath, directC.ID): true,
		},
		ProxyPaths:     opts.ProxyPaths,
		ProxyPathSteps: opts.ProxyPathSteps,
	})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, node := range subscriptionNodes {
		names[node.SourceName] = true
	}
	for _, name := range []string{"A｜B｜C", "A｜B", "A｜B｜C｜直出"} {
		if !names[name] {
			t.Fatalf("subscription should preserve %q, got %#v", name, names)
		}
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
	if forward.ListenPort != 55778 || forward.TargetPort <= 0 || forward.TargetPort == rootInbound.Port || forward.Protocol != model.ForwardProtocolTCP {
		t.Fatalf("forward = %#v, want YT:55778 TCP -> one generated WAWO listener", forward)
	}
	if forward.TrustedForward == nil || forward.TrustedForward.Version != 1 || forward.TrustedForward.Key == "" {
		t.Fatalf("transparent entry forward omitted trusted sender: %#v", forward)
	}

	configB := mustServerConfig(t, serverB, []model.Inbound{rootInbound}, []model.User{user}, opts)
	var parsedB SingBoxConfig
	if err := json.Unmarshal([]byte(configB), &parsedB); err != nil {
		t.Fatal(err)
	}
	processingTag := proxyPathSharedTransparentInboundTag(rootInbound.ID, step.Position)
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
		{protocol: model.ProtocolMieru, config: `{"transport":"TCP"}`, want: model.ForwardProtocolTCP},
		{protocol: model.ProtocolMieru, config: `{"transport":"UDP"}`, want: model.ForwardProtocolUDP},
		{protocol: model.ProtocolSocks, config: `{}`, want: model.ForwardProtocolTCPUDP},
	}
	for _, tc := range cases {
		t.Run(string(tc.protocol)+tc.config, func(t *testing.T) {
			if got := transparentForwardProtocol(model.Inbound{Protocol: tc.protocol, ConfigJSON: tc.config}); got != tc.want {
				t.Fatalf("protocol = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSocks5InboundRequiresNativeUDPMode(t *testing.T) {
	inbound := model.Inbound{Name: "SOCKS5", Protocol: model.ProtocolSocks}
	for _, mode := range []model.UDPInboundMode{model.UDPInboundBlock, model.UDPInboundUoT} {
		err := validateServerUDPForInbound(model.Server{Name: "edge", UDPInboundMode: mode}, inbound)
		if err == nil || !strings.Contains(err.Error(), "SOCKS5") {
			t.Fatalf("udp_inbound_mode=%s error = %v", mode, err)
		}
	}
	if err := validateServerUDPForInbound(model.Server{Name: "edge", UDPInboundMode: model.UDPInboundAllow}, inbound); err != nil {
		t.Fatalf("allow mode rejected SOCKS5: %v", err)
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
		{name: "mieru", protocol: model.ProtocolMieru, config: `{"transport":"TCP"}`, wantType: "mieru", wantForward: model.ForwardProtocolTCP},
		{name: "socks5", protocol: model.ProtocolSocks, config: `{}`, wantType: "socks", wantForward: model.ForwardProtocolTCPUDP},
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
			processingTag := proxyPathSharedTransparentInboundTag(root.ID, step.Position)
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
	if !hasInbound(configB, proxyPathChainServiceTag(proxyPathChainServiceKey{Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod}), "__oboard_path_51_step_2") {
		t.Fatalf("B missing shared internal inbound for imported detour path: %s", configB)
	}
	if !hasRoute(configB, proxyPathChainServiceTag(proxyPathChainServiceKey{Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod}), "path-51-step-3") {
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

func findEndpoint(raw, tag string) map[string]any {
	var parsed SingBoxConfig
	_ = json.Unmarshal([]byte(raw), &parsed)
	for _, endpoint := range parsed.Endpoints {
		if endpoint["tag"] == tag {
			return endpoint
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
	sub, err := GenerateSubscriptionWithOptions(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{
		EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeProxyPath, pathA.ID): true, NodeKeyOf(model.AssignableNodeProxyPath, pathB.ID): true, NodeKeyOf(model.AssignableNodeProxyPath, pathDirect.ID): true},
		ProxyPaths:     opts.ProxyPaths,
		ProxyPathSteps: opts.ProxyPathSteps,
	})
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

func TestGenerateServerConfigUsesExactProxyPathUsers(t *testing.T) {
	server := model.Server{ID: 1, Name: "s1", PublicIPv4: "203.0.113.1"}
	inbound := model.Inbound{ID: 1, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	users := []model.User{
		{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111"},
		{ID: 8, Username: "bob", Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222"},
	}
	paths := []model.ProxyPath{{ID: 40, InboundID: inbound.ID, Kind: model.ProxyPathKindDirect, Enabled: true}}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, nil, users, ConfigOptions{
		Inbounds:       []model.Inbound{inbound},
		ProxyPaths:     paths,
		ProxyPathUsers: []model.ProxyPathUser{{ProxyPathID: 40, InboundID: inbound.ID, UserID: 7, Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"alice__oboard_path_40"} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config missing %s: %s", expected, config)
		}
	}
	for _, unexpected := range []string{"bob__oboard_path_40"} {
		if strings.Contains(config, unexpected) {
			t.Fatalf("config leaked %s: %s", unexpected, config)
		}
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
		{name: "mieru", protocol: model.ProtocolMieru, config: `{"transport":"TCP"}`, port: 8964},
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
				if tt.protocol == model.ProtocolMieru {
					if name := stringFromAny(user["name"]); !strings.HasPrefix(name, "oboard-s") || len(name) > 64 {
						t.Fatalf("Mieru placeholder name = %#v", user)
					}
				} else if user["name"] != placeholderName {
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

func TestGenerateServerConfigPlaceholderIsStableWithServerSecret(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", ChainSecret: "stable-server-secret"}
	inbounds := []model.Inbound{{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}}
	generate := func(current model.Server) string {
		t.Helper()
		config, err := GenerateServerConfigWithOptions(current, inbounds, nil, nil, nil, ConfigOptions{InboundUsers: []model.InboundUser{}})
		if err != nil {
			t.Fatal(err)
		}
		return config
	}
	first := generate(server)
	second := generate(server)
	if first != second {
		t.Fatalf("same server secret generated different placeholder configs:\n%s\n%s", first, second)
	}
	server.ChainSecret = "different-server-secret"
	if third := generate(server); third == first {
		t.Fatal("different server secrets generated the same placeholder config")
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
		SubscriptionOptions{Format: model.SubscriptionFormatSingBox, EffectiveNodes: map[string]bool{}},
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
	withoutGrant, err := GenerateSubscriptionWithOptions(user, nil, nil, SubscriptionOptions{Format: model.SubscriptionFormatSingBox, ExternalOutbounds: []model.ExternalOutbound{external}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutGrant, "socks-a") {
		t.Fatalf("imported node leaked without grant: %s", withoutGrant)
	}
	withGrant, err := GenerateSubscriptionWithOptions(user, nil, nil, SubscriptionOptions{Format: model.SubscriptionFormatSingBox, ExternalOutbounds: []model.ExternalOutbound{external}, EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeExternalOutbound, external.ID): true}})
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

func TestValidateInboundConfigJSONReportsRealityFieldPath(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "legacy dest",
			raw:  `{"tls":{"reality":{"enabled":true,"dest":"gateway.icloud.com:443"}}}`,
			want: "config_json.tls.reality.dest: unsupported field",
		},
		{
			name: "unknown handshake field",
			raw:  `{"tls":{"reality":{"enabled":true,"handshake":{"server":"gateway.icloud.com","server_port":443,"address":"legacy"}}}}`,
			want: "config_json.tls.reality.handshake.address: unsupported field",
		},
		{
			name: "invalid handshake port",
			raw:  `{"tls":{"reality":{"enabled":true,"handshake":{"server":"gateway.icloud.com","server_port":443.5}}}}`,
			want: "config_json.tls.reality.handshake.server_port: must be an integer between 1 and 65535",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInboundConfigJSON(model.ProtocolVLESS, tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			var fieldErr *ConfigFieldError
			if !errors.As(err, &fieldErr) || fieldErr.ValidationPath() == "" {
				t.Fatalf("error = %v, want located ConfigFieldError", err)
			}
		})
	}
}

func TestValidatePersistedInboundConfigJSONRequiresCompleteReality(t *testing.T) {
	privateKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	publicKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "tls disabled", raw: `{"tls":{"enabled":false,"server_name":"gateway.icloud.com","reality":{"enabled":true,"handshake":{"server":"gateway.icloud.com","server_port":443},"private_key":"` + privateKey + `","public_key":"` + publicKey + `","short_id":"abcd"}}}`, want: "config_json.tls.enabled"},
		{name: "missing handshake", raw: `{"tls":{"enabled":true,"server_name":"gateway.icloud.com","reality":{"enabled":true,"private_key":"` + privateKey + `","public_key":"` + publicKey + `","short_id":"abcd"}}}`, want: "config_json.tls.reality.handshake"},
		{name: "bad short id", raw: `{"tls":{"enabled":true,"server_name":"gateway.icloud.com","reality":{"enabled":true,"handshake":{"server":"gateway.icloud.com","server_port":443},"private_key":"` + privateKey + `","public_key":"` + publicKey + `","short_id":"xyz"}}}`, want: "config_json.tls.reality.short_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePersistedInboundConfigJSON(model.ProtocolVLESS, tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want path %s", err, tt.want)
			}
		})
	}
}

func TestGenerateServerConfigRejectsUnknownRealityField(t *testing.T) {
	privateKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	_, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "edge"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "reality", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{
  "flow": "xtls-rprx-vision",
  "tls": {
    "enabled": true,
    "server_name": "gateway.icloud.com",
    "reality": {
      "enabled": true,
      "dest": "gateway.icloud.com:443",
      "handshake": {"server": "gateway.icloud.com", "server_port": 443},
      "private_key": "` + privateKey + `",
      "short_id": "abcd"
    }
  }
}`, Enabled: true}},
		nil,
		nil,
		[]model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}},
	)
	if err == nil || !strings.Contains(err.Error(), "inbounds[0].tls.reality.dest unsupported field") {
		t.Fatalf("error = %v, want precise generated Reality field path", err)
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

func TestValidateGeneratedSingBoxConfigRejectsUnknownDialResolver(t *testing.T) {
	tests := []struct {
		name      string
		outbounds []map[string]any
		endpoints []map[string]any
		want      string
	}{
		{
			name:      "outbound",
			outbounds: []map[string]any{{"type": "direct", "tag": "direct", "domain_resolver": map[string]any{"server": "bootstrap"}}, {"type": "block", "tag": "block"}},
			want:      `outbounds[0].domain_resolver references unknown dns server "bootstrap"`,
		},
		{
			name:      "endpoint",
			outbounds: []map[string]any{{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}},
			endpoints: []map[string]any{{"type": "wireguard", "tag": "warp-1", "domain_resolver": "bootstrap"}},
			want:      `endpoints[0].domain_resolver references unknown dns server "bootstrap"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := SingBoxConfig{
				DNS: map[string]any{"servers": []map[string]any{
					{"type": "udp", "tag": primaryBootstrapDNSTag, "server": "1.1.1.1", "server_port": 53},
				}, "final": primaryBootstrapDNSTag},
				Outbounds: tt.outbounds,
				Endpoints: tt.endpoints,
				Route:     map[string]any{"final": "direct"},
			}
			err := ValidateGeneratedSingBoxConfig(config)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateGeneratedSingBoxConfigRejectsUnknownEndpointDetour(t *testing.T) {
	config := SingBoxConfig{
		DNS: map[string]any{"servers": []map[string]any{
			{"type": "udp", "tag": primaryBootstrapDNSTag, "server": "1.1.1.1", "server_port": 53},
		}, "final": primaryBootstrapDNSTag},
		Outbounds: []map[string]any{{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}},
		Endpoints: []map[string]any{{"type": "wireguard", "tag": "warp-1", "detour": "missing"}},
		Route:     map[string]any{"final": "direct"},
	}
	err := ValidateGeneratedSingBoxConfig(config)
	if err == nil || !strings.Contains(err.Error(), `endpoints[0].detour references unknown outbound "missing"`) {
		t.Fatalf("error = %v, want unknown endpoint detour rejection", err)
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

func TestValidateGeneratedSingBoxConfigRejectsAnyTLSOutboundTFO(t *testing.T) {
	config := SingBoxConfig{
		DNS: map[string]any{"servers": []map[string]any{
			{"type": "udp", "tag": "remote", "server": "1.1.1.1", "server_port": 53},
		}, "final": "remote"},
		Outbounds: []map[string]any{
			{"type": "direct", "tag": "direct"},
			{"type": "block", "tag": "block"},
			{"type": "anytls", "tag": "anytls-out", "server": "anytls.example.com", "server_port": 443, "password": "secret", "tcp_fast_open": true},
		},
		Route: map[string]any{"final": "direct"},
	}
	err := ValidateGeneratedSingBoxConfig(config)
	if err == nil || !strings.Contains(err.Error(), "tcp_fast_open is not supported with anytls outbound") {
		t.Fatalf("error = %v, want anytls outbound TFO rejection", err)
	}
}

func TestAnyTLSPathHopOmitsTCPFastOpen(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "edge-a", PublicIPv4: "203.0.113.1", IPStack: model.IPStackPreferIPv4}
	serverB := model.Server{ID: 2, Name: "edge-b", PublicIPv4: "203.0.113.2", IPStack: model.IPStackPreferIPv4}
	rootInbound := model.Inbound{ID: 10, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	targetInbound := model.Inbound{
		ID: 20, ServerID: 2, Name: "next-anytls", Protocol: model.ProtocolAnyTLS, ListenIP: "0.0.0.0", Port: 8443, Enabled: true,
		ConfigJSON: `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"},"tcp_fast_open":true}`,
	}
	path := model.ProxyPath{ID: 40, Name: "entry-via-anytls", InboundID: rootInbound.ID, Secret: "path-secret", Enabled: true}
	targetID := targetInbound.ID
	external := model.ExternalOutbound{
		ID: 30, Name: "imported-anytls", Protocol: model.ProtocolAnyTLS, TargetAddress: "imported.example.com", TargetPort: 443, Enabled: true,
		ConfigJSON: `{"type":"anytls","server":"imported.example.com","server_port":443,"password":"imported-pass","tcp_fast_open":true,"tls":{"enabled":true,"server_name":"imported.example.com"}}`,
	}
	externalID := external.ID
	opts := ConfigOptions{
		Servers:           []model.Server{serverA, serverB},
		Inbounds:          []model.Inbound{rootInbound, targetInbound},
		ExternalOutbounds: []model.ExternalOutbound{external},
		ProxyPaths:        []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
			{ID: 2, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, InboundID: &targetID},
		},
		InboundUsers: []model.InboundUser{{InboundID: rootInbound.ID, UserID: 1, Enabled: true}},
	}
	users := []model.User{{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}}
	config, err := GenerateServerConfigWithOptions(serverA, []model.Inbound{rootInbound, targetInbound}, nil, nil, users, opts)
	if err != nil {
		t.Fatal(err)
	}
	imported := findOutbound(config, "path-40-step-1")
	if imported["type"] != "anytls" {
		t.Fatalf("imported hop = %#v", imported)
	}
	if _, exists := imported["tcp_fast_open"]; exists {
		t.Fatalf("imported anytls hop still carries tcp_fast_open: %#v", imported)
	}
	hop := findOutbound(config, "path-40-step-2")
	if hop["type"] != "anytls" || hop["detour"] != "path-40-step-1" {
		t.Fatalf("controlled anytls hop = %#v", hop)
	}
	if _, exists := hop["tcp_fast_open"]; exists {
		t.Fatalf("path anytls hop still carries tcp_fast_open: %#v", hop)
	}
	targetConfig, err := GenerateServerConfigWithOptions(serverB, []model.Inbound{rootInbound, targetInbound}, nil, nil, users, opts)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(targetConfig), &parsed); err != nil {
		t.Fatal(err)
	}
	var foundListen bool
	for _, inbound := range parsed.Inbounds {
		if inbound["tag"] == "in-20" {
			foundListen = inbound["tcp_fast_open"] == true
			break
		}
	}
	if !foundListen {
		t.Fatalf("target anytls inbound lost listen-side tcp_fast_open: %s", targetConfig)
	}
}

func TestGeneratedConfigPassesOfficialSingBoxCheck(t *testing.T) {
	bin, oboardSB := configuredSingBoxCheckBinary()
	if bin == "" {
		t.Skip("set OBOARD_SB_BIN or SING_BOX_BIN to run a sing-box check")
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
	runSingBoxCheck(t, bin, oboardSB, config)
}

func TestGeneratedWARPAndRouteConfigPassesOfficialSingBoxCheck(t *testing.T) {
	bin, oboardSB := configuredSingBoxCheckBinary()
	if bin == "" {
		t.Skip("set OBOARD_SB_BIN or SING_BOX_BIN to run a sing-box check")
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
			WARPProfiles: []model.WARPProfile{{ID: warpID, ServerID: 1, Name: "warp", Status: model.WARPStatusReady, ConfigJSON: `{"type":"wireguard","address":["172.16.0.2/32","2606:4700:110:abcd::2/128"],"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"bmXOC+F1sQvdD4mp8yt3l7wY6/3mpYBvn04zP65yzM8=","reserved":[1,2,3],"allowed_ips":["0.0.0.0/0","::/0"]}],"domain_resolver":{"server":"bootstrap","strategy":"prefer_ipv6"}}`, Enabled: true}},
			RoutingRules: []model.RoutingRule{{ID: 2, ServerID: 1, Name: "ssh-via-eth1", Priority: 20, MatchJSON: `{"port":[22]}`, Action: model.RouteActionInterface, InterfaceName: "eth1", Enabled: true}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	runSingBoxCheck(t, bin, oboardSB, config)
}

func TestReadyWARPInfersIPv6ResolverAndMTUForAutoServer(t *testing.T) {
	endpoint, err := warpProfileToSingBox(model.WARPProfile{
		ID:          30,
		DNSStrategy: "auto",
		ConfigJSON:  `{"type":"wireguard","address":["172.16.0.2/32","2606:4700:110::2/128"],"private_key":"private","peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"public","allowed_ips":["0.0.0.0/0","::/0"]}],"domain_resolver":{"server":"bootstrap","strategy":"prefer_ipv4"}}`,
	}, model.Server{IPStack: model.IPStackAuto, PublicIPv6: "2001:db8::10"})
	if err != nil {
		t.Fatal(err)
	}
	resolver, ok := endpoint["domain_resolver"].(map[string]any)
	if !ok || resolver["server"] != primaryBootstrapDNSTag || resolver["strategy"] != "prefer_ipv6" {
		t.Fatalf("WARP domain_resolver = %#v", endpoint["domain_resolver"])
	}
	if endpoint["mtu"] != 1280 {
		t.Fatalf("WARP MTU = %#v, want 1280", endpoint["mtu"])
	}
}

func TestWARPEndpointMTUIsFixedAt1280(t *testing.T) {
	profile := model.WARPProfile{
		ID:          30,
		DNSStrategy: "auto",
		ConfigJSON:  `{"type":"wireguard","address":["172.16.0.2/32"],"private_key":"private","mtu":1500,"peers":[{"address":"engage.cloudflareclient.com","port":2408,"public_key":"public","allowed_ips":["0.0.0.0/0","::/0"]}],"domain_resolver":{"server":"bootstrap","strategy":"prefer_ipv4"}}`,
	}
	// The server's main-network MTU must never leak into the WARP tunnel; a
	// legacy ready profile may also carry a poisoned mtu in its stored config.
	for _, server := range []model.Server{
		{IPStack: model.IPStackPreferIPv4, MTUValue: 1500},
		{IPStack: model.IPStackPreferIPv4, MTUValue: 9000},
	} {
		endpoint, err := warpProfileToSingBox(profile, server)
		if err != nil {
			t.Fatal(err)
		}
		if endpoint["mtu"] != WarpTunnelMTU {
			t.Fatalf("WARP MTU = %#v, want %d", endpoint["mtu"], WarpTunnelMTU)
		}
	}
	// A profile whose MTU column was poisoned by an older auto flow is also
	// normalized back to the fixed tunnel MTU.
	poisoned := profile
	poisoned.MTU = 1500
	endpoint, err := warpProfileToSingBox(poisoned, model.Server{IPStack: model.IPStackPreferIPv4, MTUValue: 1500})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint["mtu"] != WarpTunnelMTU {
		t.Fatalf("poisoned profile WARP MTU = %#v, want %d", endpoint["mtu"], WarpTunnelMTU)
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
	for _, protocol := range []model.Protocol{model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolMieru, model.ProtocolSocks} {
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

func TestSocksAdapterUsesAuthenticatedSocks5Credentials(t *testing.T) {
	user := model.User{Username: "alice", Status: "active", ProxyPassword: "socks-password"}
	adapter, err := AdapterFor(model.ProtocolSocks)
	if err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ID: 7, Name: "SOCKS5", Protocol: model.ProtocolSocks, ListenIP: "0.0.0.0", Port: 1080, ConfigJSON: `{}`, Enabled: true}
	block, err := adapter.Inbound(inbound, []model.User{user})
	if err != nil {
		t.Fatal(err)
	}
	users, ok := block["users"].([]map[string]any)
	if !ok || len(users) != 1 || users[0]["username"] != "alice" || users[0]["password"] != "socks-password" {
		t.Fatalf("SOCKS5 inbound users = %#v", block["users"])
	}
	if _, exists := users[0]["name"]; exists {
		t.Fatalf("SOCKS5 inbound emitted unsupported name field: %#v", users[0])
	}
	node, err := adapter.SubscriptionNode(user, inbound, model.Server{EntryAddress: "proxy.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if node["type"] != "socks" || node["version"] != "5" || node["username"] != "alice" || node["password"] != "socks-password" {
		t.Fatalf("SOCKS5 subscription node = %#v", node)
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

func TestAnyTLSPaddingSchemesValidateAndRender(t *testing.T) {
	upstream := strings.Join(AnyTLSUpstreamDefaultPaddingScheme(), "\n")
	adapter, err := AdapterFor(model.ProtocolAnyTLS)
	if err != nil {
		t.Fatal(err)
	}
	for name, scheme := range map[string][]string{
		"balanced": AnyTLSBalancedPaddingScheme(),
		"large":    AnyTLSLargePaddingScheme(),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Join(scheme, "\n") == upstream {
				t.Fatal("OBoard preset must differ from the sing-anytls default")
			}
			if err := ValidateAnyTLSPaddingScheme(scheme); err != nil {
				t.Fatal(err)
			}
			config, err := json.Marshal(map[string]any{
				"tls":            map[string]any{"enabled": true, "certificate_path": "/tmp/cert.pem", "key_path": "/tmp/key.pem"},
				"padding_scheme": scheme,
			})
			if err != nil {
				t.Fatal(err)
			}
			inbound := model.Inbound{ID: 7, Name: "AnyTLS", Protocol: model.ProtocolAnyTLS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: string(config), Enabled: true}
			block, err := adapter.Inbound(inbound, []model.User{{Username: "alice", ProxyPassword: "secret"}})
			if err != nil {
				t.Fatal(err)
			}
			assertAnyTLSPaddingScheme(t, block["padding_scheme"], scheme)
			node, err := adapter.SubscriptionNode(model.User{Username: "alice", ProxyPassword: "secret"}, inbound, model.Server{EntryAddress: "edge.example.com"})
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := node["padding_scheme"]; exists {
				t.Fatalf("server-only padding_scheme leaked into subscription node: %#v", node)
			}
		})
	}
}

func TestAnyTLSPaddingSchemeRejectsInvalidRules(t *testing.T) {
	invalid := map[string]any{
		"wrong type":        map[string]any{"stop": 3},
		"missing stop":      []any{"0=64-128"},
		"duplicate stop":    []any{"stop=3", "stop=4", "0=64-128"},
		"index after stop":  []any{"stop=2", "2=64-128"},
		"packet zero split": []any{"stop=2", "0=64-128,128-256"},
		"bad range":         []any{"stop=2", "0=256-128"},
		"bad check marker":  []any{"stop=2", "1=c,64-128"},
		"non-string item":   []any{"stop=2", 42},
	}
	for name, scheme := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAnyTLSPaddingScheme(scheme); err == nil {
				t.Fatal("expected invalid padding scheme to fail")
			}
		})
	}
	if err := ValidateAnyTLSPaddingScheme([]any{}); err != nil {
		t.Fatalf("empty padding scheme should preserve the upstream default: %v", err)
	}
}

func assertAnyTLSPaddingScheme(t *testing.T, value any, want []string) {
	t.Helper()
	raw, ok := value.([]any)
	if !ok || len(raw) != len(want) {
		t.Fatalf("padding_scheme = %#v, want %#v", value, want)
	}
	for index, item := range raw {
		if item != want[index] {
			t.Fatalf("padding_scheme[%d] = %#v, want %q", index, item, want[index])
		}
	}
}

func testInboundConfig(protocol model.Protocol) string {
	switch protocol {
	case model.ProtocolHY2, model.ProtocolAnyTLS:
		return `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"}}`
	case model.ProtocolMieru:
		return `{"transport":"TCP"}`
	case model.ProtocolSnell:
		return `{"version":4,"psk":"secret-psk-1234"}`
	default:
		return `{}`
	}
}

func testOutboundConfig(protocol model.Protocol) string {
	if protocol == model.ProtocolMieru {
		return `{"transport":"TCP","username":"node","password":"pass"}`
	}
	if protocol == model.ProtocolSnell {
		return `{"version":4,"psk":"secret-psk-1234"}`
	}
	return `{}`
}

func TestSnellAdapterVersionMapping(t *testing.T) {
	adapter, err := AdapterFor(model.ProtocolSnell)
	if err != nil {
		t.Fatal(err)
	}
	// Panel v4 maps to the sing-box v5 inbound (compatible with v4 clients)
	// and advertises v4 to clients.
	inbound := model.Inbound{ID: 7, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, ConfigJSON: `{"version":4,"psk":"secret-psk-1234","obfs_mode":"http","obfs_host":"bing.com"}`, Enabled: true}
	block, err := adapter.Inbound(inbound, []model.User{{Username: "alice", ProxyPassword: "pass-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if block["version"] != 5 || block["psk"] != "secret-psk-1234" || block["obfs_mode"] != "http" {
		t.Fatalf("snell v4 inbound block = %#v", block)
	}
	node, err := adapter.SubscriptionNode(model.User{Username: "alice", ProxyPassword: "pass-a"}, inbound, model.Server{EntryAddress: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	if node["version"] != 4 || node["psk"] != "secret-psk-1234" || node["userkey"] != "pass-a" || node["obfs_mode"] != "http" || node["obfs_host"] != "bing.com" {
		t.Fatalf("snell v4 subscription node = %#v", node)
	}
	// Panel v6 maps to the sing-box v6 inbound and advertises v6; obfs is
	// rejected.
	v6 := model.Inbound{ID: 8, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 7177, ConfigJSON: `{"version":6,"psk":"secret-psk-1234","mode":"unshaped"}`, Enabled: true}
	block6, err := adapter.Inbound(v6, nil)
	if err != nil {
		t.Fatal(err)
	}
	if block6["version"] != 6 || block6["mode"] != "unshaped" {
		t.Fatalf("snell v6 inbound block = %#v", block6)
	}
	if _, err := adapter.Inbound(model.Inbound{ID: 9, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 7178, ConfigJSON: `{"version":6,"psk":"secret-psk-1234","obfs_mode":"http"}`, Enabled: true}, nil); err == nil {
		t.Fatal("snell v6 with obfs must be rejected")
	}
	// PSK is now a stable per-inbound server credential and must be present
	// in config_json; the old fallback to the first bound user's password is
	// removed. An inbound without a PSK fails at config generation so the
	// controller can persist a generated one.
	pskless := model.Inbound{ID: 10, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6161, ConfigJSON: `{"version":4}`, Enabled: true}
	if _, err := adapter.Inbound(pskless, []model.User{{Username: "alice", ProxyPassword: "12345678-abcdef"}}); err == nil {
		t.Fatal("snell inbound without psk must be rejected")
	}
	// Inbound must emit a multi-user users array derived from bound users.
	multi := model.Inbound{ID: 15, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6165, ConfigJSON: `{"version":4,"psk":"secret-psk-1234"}`, Enabled: true}
	multiBlock, err := adapter.Inbound(multi, []model.User{{Username: "alice", ProxyPassword: "alice-key"}, {Username: "bob", ProxyPassword: "bob-key"}})
	if err != nil {
		t.Fatal(err)
	}
	users, ok := multiBlock["users"].([]map[string]any)
	if !ok || len(users) != 2 {
		t.Fatalf("snell multi-user inbound users = %#v", multiBlock["users"])
	}
	if users[0]["name"] != "alice" || users[0]["userkey"] != "alice-key" || users[1]["name"] != "bob" || users[1]["userkey"] != "bob-key" {
		t.Fatalf("snell users = %#v", users)
	}
	// Unsupported panel versions are rejected.
	if _, err := adapter.Inbound(model.Inbound{ID: 11, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6162, ConfigJSON: `{"version":5,"psk":"secret-psk-1234"}`, Enabled: true}, nil); err == nil {
		t.Fatal("snell panel version 5 must be rejected")
	}
	// v6 psk length contract: 12-255 bytes per sing-box 1.14 docs and
	// sing-snell v6 server validation.
	shortV6 := model.Inbound{ID: 12, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 7160, ConfigJSON: `{"version":6,"psk":"short-psk"}`, Enabled: true}
	if _, err := adapter.Inbound(shortV6, nil); err == nil {
		t.Fatal("snell v6 psk shorter than 12 bytes must be rejected")
	}
	// tls obfs is not exposed for v4 either (sing-box 1.14 / Surge docs).
	tlsObfs := model.Inbound{ID: 13, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6163, ConfigJSON: `{"version":4,"psk":"secret-psk-1234","obfs_mode":"tls"}`, Enabled: true}
	if _, err := adapter.Inbound(tlsObfs, nil); err == nil {
		t.Fatal("snell v4 tls obfs must be rejected")
	}
	// http obfs without explicit host is valid (sing-box defaults bing.com).
	nohost := model.Inbound{ID: 14, Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6164, ConfigJSON: `{"version":4,"psk":"secret-psk-1234","obfs_mode":"http"}`, Enabled: true}
	if _, err := adapter.Inbound(nohost, nil); err != nil {
		t.Fatalf("snell v4 http obfs without host must be accepted: %v", err)
	}
}

func TestSnellAdapterOutboundVersionMapping(t *testing.T) {
	adapter, err := AdapterFor(model.ProtocolSnell)
	if err != nil {
		t.Fatal(err)
	}
	outbound := model.Outbound{ID: 5, Protocol: model.ProtocolSnell, TargetAddress: "example.com", TargetPort: 6160, ConfigJSON: `{"version":4,"psk":"secret-psk-1234","obfs_mode":"http","obfs_host":"cdn.example.com","reuse":true}`, Enabled: true}
	out, err := adapter.Outbound(outbound, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["version"] != 4 || out["obfs_mode"] != "http" || out["obfs_host"] != "cdn.example.com" || out["reuse"] != true {
		t.Fatalf("snell v4 outbound = %#v", out)
	}
	v6out := model.Outbound{ID: 6, Protocol: model.ProtocolSnell, TargetAddress: "example.com", TargetPort: 7177, ConfigJSON: `{"version":6,"psk":"secret-psk-1234","mode":"unsafe-raw"}`, Enabled: true}
	out6, err := adapter.Outbound(v6out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out6["version"] != 6 || out6["mode"] != "unsafe-raw" {
		t.Fatalf("snell v6 outbound = %#v", out6)
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

func TestHY2SalamanderObfsValidation(t *testing.T) {
	adapter, err := AdapterFor(model.ProtocolHY2)
	if err != nil {
		t.Fatal(err)
	}
	valid := model.Inbound{ID: 5, Protocol: model.ProtocolHY2, ListenIP: "127.0.0.1", Port: 443, ConfigJSON: `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"},"obfs":{"type":"salamander","password":"obfs-pass"}}`, Enabled: true}
	block, err := adapter.Inbound(valid, []model.User{{Username: "u", Status: "active", ProxyPassword: "pass"}})
	if err != nil {
		t.Fatalf("valid salamander inbound: %v", err)
	}
	obfs, _ := block["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "obfs-pass" {
		t.Fatalf("salamander obfs = %#v", block["obfs"])
	}
	if _, err := adapter.Inbound(model.Inbound{ID: 6, Protocol: model.ProtocolHY2, ListenIP: "127.0.0.1", Port: 443, ConfigJSON: `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"},"obfs":{"type":"salamander"}}`, Enabled: true}, nil); err == nil {
		t.Fatal("salamander without password must be rejected")
	}
	if _, err := adapter.Inbound(model.Inbound{ID: 7, Protocol: model.ProtocolHY2, ListenIP: "127.0.0.1", Port: 443, ConfigJSON: `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"},"obfs":{"type":"gecko","password":"x"}}`, Enabled: true}, nil); err == nil {
		t.Fatal("unsupported hy2 obfs type must be rejected")
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
				{ID: 7, ServerID: 1, Name: "dynamic-v6", Priority: 50, MatchJSON: `{"domain_suffix":["v6.example"]}`, Action: model.RouteActionSourcePrefix, SourcePrefix: "2001:db8:42::/64", Enabled: true},
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
	if len(parsed.Outbounds) != 6 {
		t.Fatalf("outbounds = %d, want direct/block + normal + external + source-prefix + bound interface", len(parsed.Outbounds))
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
	if len(rules) != 5 {
		t.Fatalf("route rules = %d, want direct/outbound/external/interface/source-prefix: %#v", len(rules), parsed.Route["rules"])
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
	if interfaceRule["action"] != "route" || interfaceRule["outbound"] != routingRuleInterfaceOutboundTag(6) {
		t.Fatalf("interface route = %#v, want bound direct outbound", interfaceRule)
	}
	interfaceOutbound := findOutbound(config, routingRuleInterfaceOutboundTag(6))
	if interfaceOutbound["type"] != "direct" || interfaceOutbound["bind_interface"] != "eth1" {
		t.Fatalf("interface outbound = %#v, want direct outbound bound to eth1", interfaceOutbound)
	}
	ports, ok := interfaceRule["port"].([]any)
	if !ok || len(ports) != 1 || ports[0].(float64) != 22 {
		t.Fatalf("interface route ports = %#v, want [22]", interfaceRule["port"])
	}
	prefixOutbound := parsed.Outbounds[4]
	if prefixOutbound["type"] != "source-prefix" || prefixOutbound["prefix"] != "2001:db8:42::/64" {
		t.Fatalf("source-prefix outbound = %#v", prefixOutbound)
	}
	resolver, ok := prefixOutbound["domain_resolver"].(map[string]any)
	if !ok || resolver["server"] != primaryBootstrapDNSTag || resolver["strategy"] != "ipv6_only" {
		t.Fatalf("source-prefix domain_resolver = %#v", prefixOutbound["domain_resolver"])
	}
	prefixRule := rules[4].(map[string]any)
	if prefixRule["action"] != "route" || prefixRule["outbound"] != prefixOutbound["tag"] {
		t.Fatalf("source-prefix route = %#v, outbound = %#v", prefixRule, prefixOutbound)
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

func TestRoutingRuleDNSResolverEmitsDNSRules(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge"}
	rulesetID := int64(9)
	rules := []model.RoutingRule{
		{ID: 1, ServerID: 1, Name: "inline-dns", Priority: 10, MatchJSON: `{"domain_suffix":["example.com"]}`, DNSResolver: "local", Action: model.RouteActionDirect, Enabled: true},
		{ID: 2, ServerID: 1, Name: "ruleset-dns", Priority: 20, MatchSource: model.RoutingMatchSourceRuleSet, RuleSetID: &rulesetID, DNSResolver: "remote-primary", Action: model.RouteActionDirect, Enabled: true},
	}
	sets := []model.RoutingRuleSet{{ID: rulesetID, Name: "remote", Revision: "rev-1", Status: model.RoutingRuleSetStatusReady}}
	config, err := GenerateServerConfigWithOptions(server, nil, nil, nil, nil, ConfigOptions{RoutingRules: rules, RoutingRuleSets: sets})
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	rulesValue, ok := parsed.DNS["rules"].([]any)
	if !ok || len(rulesValue) != 2 {
		t.Fatalf("dns rules = %#v", parsed.DNS["rules"])
	}
	first := rulesValue[0].(map[string]any)
	if first["server"] != "local" || first["domain_suffix"] == nil {
		t.Fatalf("inline dns rule = %#v", first)
	}
	second := rulesValue[1].(map[string]any)
	if second["server"] != "remote-primary" || second["rule_set"] == nil {
		t.Fatalf("ruleset dns rule = %#v", second)
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

func configuredSingBoxCheckBinary() (string, bool) {
	if bin := os.Getenv("OBOARD_SB_BIN"); bin != "" {
		return bin, true
	}
	return os.Getenv("SING_BOX_BIN"), false
}

func runSingBoxCheck(t *testing.T, bin string, oboardSB bool, config string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"check", "-c", path}
	if oboardSB {
		args = []string{"-check", "-config", path}
	}
	cmd := exec.Command(bin, args...)
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
		InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 1, Enabled: true}, {InboundID: 1, UserID: 2, Enabled: true}},
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
	if got := parsed.OBoard.RateLimits.Users["alice"].SpeedLimitMbps; got != 0 {
		t.Fatalf("alice speed limit = %d, want default 0", got)
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

	internalPort := proxyPathInternalPort(server, 4, 1, "0.0.0.0", inbounds)
	if internalPort != 0 {
		t.Fatalf("internal port = %d, want exhausted public range to fail without overflow", internalPort)
	}
	wgPort := proxyPathTunnelPort(server, 4, 1, 47, inbounds)
	if wgPort != existing.Port {
		t.Fatalf("WireGuard port = %d, want UDP to share TCP-only port %d", wgPort, existing.Port)
	}
	sshLoopbackPort := proxyPathTunnelPort(server, 4, 1, 31, inbounds)
	if sshLoopbackPort < 30000 || sshLoopbackPort > 59999 || sshLoopbackPort == existing.Port {
		t.Fatalf("SSH loopback port = %d, want a free port from the internal loopback pool", sshLoopbackPort)
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
	target := model.Server{ID: 5, Name: "target", PublicIPv4: "203.0.113.5", ListenIP: "0.0.0.0", PortRangeStart: 60100, PortRangeEnd: 60103}
	rootA := model.Inbound{ID: 1, ServerID: sourceA.ID, Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	rootB := model.Inbound{ID: 2, ServerID: sourceB.ID, Protocol: model.ProtocolVLESS, Port: 8443, Enabled: true}
	targetPublic := model.Inbound{ID: 6, ServerID: target.ID, Protocol: model.ProtocolVLESS, Port: 60100, Enabled: true}
	targetID := target.ID
	chainPath := model.ProxyPath{ID: 9, Name: "chain", InboundID: rootA.ID, Enabled: true}
	chainStep := model.ProxyPathStep{ID: 9, PathID: chainPath.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`}
	sshPath := model.ProxyPath{ID: 10, Name: "ssh", InboundID: rootB.ID, Enabled: true}
	privateKey, publicKey := testSSHKeyPair(t)
	sshConfig, _ := json.Marshal(map[string]any{"type": "ssh", "ssh_port": 60103, "client_private_key": privateKey, "client_public_key": publicKey})
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
	target := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 1, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 70, Name: "A-SSH-B", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	targetID := target.ID
	privateKey, publicKey := testSSHKeyPair(t)
	stepConfig, _ := json.Marshal(map[string]any{"type": "ssh", "ssh_port": 22, "managed_pair": true, "client_private_key": privateKey, "client_public_key": publicKey})
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
	service := services[proxyPathChainServiceKey{ServerID: target.ID, Protocol: model.ProtocolSS, Profile: DefaultProxyPathChainMethod}]
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
	if targetSSH["role"] != "server" || targetSSH["authorized_key"] != tunnelConfig["client_public_key"] || targetSSH["authorized_key"] == publicKey || targetSSH["client_private_key"] != nil || intFromAny(targetSSH["server_port"]) != 22 {
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

func TestSSHRootUsesKernelRouteIdentityAndManagedSSHTunnelContinues(t *testing.T) {
	source := model.Server{ID: 1, Name: "A", PublicIPv4: "198.51.100.10", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	middle := model.Server{ID: 2, Name: "B", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	exit := model.Server{ID: 3, Name: "C", PublicIPv4: "203.0.113.30", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 32000, PortRangeEnd: 32100}
	root := model.Inbound{ID: 1, ServerID: source.ID, Name: "ssh-entry", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 80, Name: "SSH-A-B-C", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	middleID, exitID := middle.ID, exit.ID
	privateKey, publicKey := testSSHKeyPair(t)
	sshConfig, _ := json.Marshal(map[string]any{"type": "ssh", "ssh_port": 31080, "managed_pair": true, "client_private_key": privateKey, "client_public_key": publicKey})
	steps := []model.ProxyPathStep{
		{ID: 81, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &middleID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: string(sshConfig)},
		{ID: 82, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: &exitID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`},
	}
	opts := ConfigOptions{Servers: []model.Server{source, middle, exit}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: steps}

	routeKind, outboundTag, err := ProxyPathEntryRoute(path, steps, root, nil)
	if err != nil || routeKind != "outbound" || outboundTag != proxyPathStepTag(path.ID, 1) {
		t.Fatalf("SSH entry route = %q %q, %v", routeKind, outboundTag, err)
	}
	sourceConfig := parseSingBoxConfig(t, mustServerConfig(t, source, []model.Inbound{root}, nil, opts))
	if hasInbound(string(mustJSON(t, sourceConfig)), tag("in", root.ID), "") {
		t.Fatalf("SSH root leaked into sing-box inbounds: %#v", sourceConfig.Inbounds)
	}
	var firstOutbound map[string]any
	for _, outbound := range sourceConfig.Outbounds {
		if outbound["tag"] == outboundTag {
			firstOutbound = outbound
		}
	}
	if firstOutbound == nil || firstOutbound["server"] != "127.0.0.1" {
		t.Fatalf("SSH tunnel first outbound = %#v", firstOutbound)
	}
	foundSyntheticContinuation := false
	for _, rule := range mapList(sourceConfig.Route["rules"]) {
		if stringSetContains(stringListFromAny(rule["inbound"]), tag("in", root.ID)) && rule["outbound"] == outboundTag {
			foundSyntheticContinuation = true
		}
	}
	if !foundSyntheticContinuation {
		t.Fatalf("source missing synthetic SSH root continuation: %#v", sourceConfig.Route["rules"])
	}

	middleConfig := parseSingBoxConfig(t, mustServerConfig(t, middle, []model.Inbound{root}, nil, opts))
	wantNext := proxyPathStepTag(path.ID, 2)
	foundContinuation := false
	for _, rule := range mapList(middleConfig.Route["rules"]) {
		if rule["outbound"] == wantNext {
			foundContinuation = true
		}
	}
	if !foundContinuation {
		t.Fatalf("managed SSH tunnel did not continue through the middle server: %#v", middleConfig.Route["rules"])
	}
}

func TestSSHRootDirectAndWARPEntryRoutes(t *testing.T) {
	root := model.Inbound{ID: 4, ServerID: 7, Protocol: model.ProtocolSSH, Enabled: true}
	direct := model.ProxyPath{ID: 40, Kind: model.ProxyPathKindDirect, InboundID: root.ID, Enabled: true}
	if kind, tag, err := ProxyPathEntryRoute(direct, nil, root, nil); err != nil || kind != "direct" || tag != "" {
		t.Fatalf("direct route = %q %q, %v", kind, tag, err)
	}
	warp := model.ProxyPath{ID: 41, InboundID: root.ID, Enabled: true}
	steps := []model.ProxyPathStep{{ID: 1, PathID: warp.ID, Position: 1, NodeType: model.ProxyPathStepWARP}}
	profiles := []model.WARPProfile{{ID: 9, ServerID: root.ServerID, Enabled: true}}
	if kind, tag, err := ProxyPathEntryRoute(warp, steps, root, profiles); err != nil || kind != "outbound" || tag != "warp-9" {
		t.Fatalf("WARP route = %q %q, %v", kind, tag, err)
	}
}

func TestProxyPathSSHTunnelRequiresExplicitPort(t *testing.T) {
	source := model.Server{ID: 1, Name: "source", PublicIPv4: "198.51.100.10", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30100}
	target := model.Server{ID: 2, Name: "target", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 1, ServerID: source.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	targetID := target.ID
	paths := []model.ProxyPath{
		{ID: 91, Name: "saved-port-a", InboundID: root.ID, Enabled: true},
		{ID: 92, Name: "saved-port-b", InboundID: root.ID, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 91, PathID: 91, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh","ssh_port":22}`},
		{ID: 92, PathID: 92, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &targetID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh","ssh_port":22}`},
	}
	plans, err := BuildProxyPathPlans(paths, steps, []model.Server{source, target}, []model.Inbound{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || plans[0].Tunnels[0].TargetPort != 22 || plans[1].Tunnels[0].TargetPort != 22 {
		t.Fatalf("SSH paths did not project the explicit shared port: %#v", plans)
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

	override[0].ConfigJSON = `{"type":"ssh"}`
	if _, err := BuildProxyPathPlans(paths[:1], override, []model.Server{source, target}, []model.Inbound{root}); err == nil || !strings.Contains(err.Error(), "未设置目标端隧道服务端口") {
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
