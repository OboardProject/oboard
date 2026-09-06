package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestBuildDNSConfigUsesBootstrapForDomainDoH(t *testing.T) {
	server := model.Server{ID: 1, Name: "s1", IPStack: model.IPStackPreferIPv6}
	dns, err := BuildDNSConfig(server, testDNSState(server.ID))
	if err != nil {
		t.Fatal(err)
	}
	servers := dns["servers"].([]map[string]any)
	remote := servers[0]
	if remote["type"] != "https" || remote["domain_resolver"] != "bootstrap-primary" {
		t.Fatalf("remote dns = %#v, want https with bootstrap resolver", remote)
	}
	if _, ok := remote["domain_strategy"]; ok {
		t.Fatalf("remote dns should not emit deprecated domain_strategy: %#v", remote)
	}
	if dns["strategy"] != "prefer_ipv6" {
		t.Fatalf("strategy = %v, want prefer_ipv6", dns["strategy"])
	}
	if !json.Valid(mustJSON(t, dns)) {
		t.Fatal("dns config is not valid json")
	}
}

func TestEffectiveIPStack(t *testing.T) {
	tests := []struct {
		name   string
		server model.Server
		want   model.IPStack
	}{
		{name: "unknown", server: model.Server{IPStack: model.IPStackAuto}, want: model.IPStackAuto},
		{name: "ipv4", server: model.Server{IPStack: model.IPStackAuto, PublicIPv4: "198.51.100.10"}, want: model.IPStackIPv4Only},
		{name: "ipv6", server: model.Server{IPStack: model.IPStackAuto, PublicIPv6: "2001:db8::10"}, want: model.IPStackIPv6Only},
		{name: "dual", server: model.Server{IPStack: model.IPStackAuto, PublicIPv4: "198.51.100.10", PublicIPv6: "2001:db8::10"}, want: model.IPStackDualStack},
		{name: "explicit", server: model.Server{IPStack: model.IPStackPreferIPv6, PublicIPv4: "198.51.100.10"}, want: model.IPStackPreferIPv6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveIPStack(tt.server); got != tt.want {
				t.Fatalf("EffectiveIPStack() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildDNSConfigInfersIPv6OnlyFromDetectedAddress(t *testing.T) {
	server := model.Server{ID: 1, IPStack: model.IPStackAuto, PublicIPv6: "2001:db8::10"}
	dns, err := BuildDNSConfig(server, testDNSState(server.ID))
	if err != nil {
		t.Fatal(err)
	}
	if dns["strategy"] != "prefer_ipv6" {
		t.Fatalf("strategy = %v, want prefer_ipv6", dns["strategy"])
	}
}

func TestBuildDNSConfigIncludesSelectedPrimaryAndSecondary(t *testing.T) {
	serverID := int64(7)
	state := testDNSState(serverID)
	state.Policy.EncryptedSelected = []model.DNSCandidate{
		{Tag: "google", Transport: model.DNSTransportDoH, Server: "dns.google", Port: 443, Path: "/dns-query", TLSName: "dns.google"},
		{Tag: "quad9", Transport: model.DNSTransportDoH, Server: "dns.quad9.net", Port: 443, Path: "/dns-query", TLSName: "dns.quad9.net"},
	}
	state.Policy.EncryptedSelectionRevision = state.EncryptedList.Revision
	dns, err := BuildDNSConfig(model.Server{ID: serverID, IPStack: model.IPStackPreferIPv4}, state)
	if err != nil {
		t.Fatal(err)
	}
	servers := dns["servers"].([]map[string]any)
	byTag := map[string]map[string]any{}
	for _, item := range servers {
		byTag[item["tag"].(string)] = item
	}
	if dns["final"] != "remote" || byTag["remote-primary"]["server"] != "dns.google" || byTag["remote-secondary"]["server"] != "dns.quad9.net" || byTag["bootstrap-primary"]["server"] != "1.1.1.1" || byTag["bootstrap-secondary"]["server"] != "8.8.8.8" {
		t.Fatalf("dual dns config = %#v", dns)
	}
	group := byTag["remote"]
	if group["type"] != "oboard-dns-group" || strings.Join(group["members"].([]string), ",") != "remote-primary,remote-secondary" {
		t.Fatalf("remote failover group = %#v", group)
	}
	if _, ok := byTag["bootstrap"]; ok {
		t.Fatal("bootstrap group emitted while encrypted resolvers are final")
	}
}

// The failover group is a kernel feature. A server whose active kernel does
// not report dns_group_v1 keeps the primary alone as its final resolver; the
// kernel that does report it receives the sequential group, and a plain-DNS
// policy groups the bootstrap pair instead.
func TestBuildDNSConfigFailoverGroupFollowsKernelCapability(t *testing.T) {
	state := testDNSState(1)
	legacy := model.Server{ID: 1, AgentID: "enrolled", KernelCapabilities: []string{"dns_doq_v1"}}
	dns, err := BuildDNSConfig(legacy, state)
	if err != nil {
		t.Fatal(err)
	}
	if dns["final"] != "remote-primary" {
		t.Fatalf("legacy kernel final = %v", dns["final"])
	}
	for _, server := range dns["servers"].([]map[string]any) {
		if server["type"] == "oboard-dns-group" {
			t.Fatalf("legacy kernel received a DNS group: %#v", server)
		}
	}
	current := legacy
	current.KernelCapabilities = []string{"dns_doq_v1", "dns_group_v1"}
	dns, err = BuildDNSConfig(current, state)
	if err != nil {
		t.Fatal(err)
	}
	if dns["final"] != "remote" {
		t.Fatalf("group-capable kernel final = %v", dns["final"])
	}
	plain := testDNSState(1)
	plain.Policy.EncryptedListID, plain.EncryptedList = 0, nil
	dns, err = BuildDNSConfig(current, plain)
	if err != nil {
		t.Fatal(err)
	}
	if dns["final"] != "bootstrap" {
		t.Fatalf("plain-only group-capable final = %v", dns["final"])
	}
	var group map[string]any
	for _, server := range dns["servers"].([]map[string]any) {
		if server["tag"] == "bootstrap" {
			group = server
		}
	}
	if group == nil || strings.Join(group["members"].([]string), ",") != "bootstrap-primary,bootstrap-secondary" {
		t.Fatalf("bootstrap group = %#v", group)
	}
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	if err := ValidateGeneratedSingBoxConfig(SingBoxConfig{DNS: dns, Inbounds: []map[string]any{}, Outbounds: outbounds, Route: map[string]any{"final": "direct", "default_domain_resolver": map[string]any{"server": "bootstrap"}}}); err != nil {
		t.Fatalf("group configuration rejected by validator: %v", err)
	}
	group["members"] = []string{"bootstrap-primary", "bootstrap"}
	if err := ValidateGeneratedSingBoxConfig(SingBoxConfig{DNS: dns, Inbounds: []map[string]any{}, Outbounds: outbounds, Route: map[string]any{"final": "direct"}}); err == nil {
		t.Fatal("nested DNS group accepted by validator")
	}
}

func TestBuildDNSConfigUsesOnlyLocalDNSAfterNoUsableCandidates(t *testing.T) {
	state := testDNSState(1)
	state.Policy.LastError = model.DNSBenchmarkNoUsableCandidatesError
	dns, err := BuildDNSConfig(model.Server{ID: 1, IPStack: model.IPStackPreferIPv6}, state)
	if err != nil {
		t.Fatal(err)
	}
	servers := dns["servers"].([]map[string]any)
	if len(servers) != 1 || servers[0]["type"] != "local" || servers[0]["tag"] != "local" {
		t.Fatalf("fallback dns servers = %#v, want local only", servers)
	}
	if dns["final"] != "local" || dns["strategy"] != "prefer_ipv6" {
		t.Fatalf("fallback dns = %#v", dns)
	}
}

func TestBuildDNSConfigSupportsDoQ(t *testing.T) {
	state := testDNSState(1)
	state.EncryptedList.Candidates[0] = model.DNSCandidate{Tag: "adguard", Transport: model.DNSTransportDoQ, Server: "dns.adguard-dns.com", Port: 853, TLSName: "dns.adguard-dns.com"}
	dns, err := BuildDNSConfig(model.Server{ID: 1}, state)
	if err != nil {
		t.Fatal(err)
	}
	remote := dns["servers"].([]map[string]any)[0]
	if remote["type"] != "quic" || remote["server_port"] != 853 || remote["domain_resolver"] != "bootstrap-primary" {
		t.Fatalf("doq remote = %#v", remote)
	}
}

func TestBuildDNSConfigRequiresActiveDoQCapability(t *testing.T) {
	state := testDNSState(1)
	state.EncryptedList.Candidates[0] = model.DNSCandidate{Tag: "doq", Transport: model.DNSTransportDoQ, Server: "dns.example.com", Port: 853}
	server := model.Server{ID: 1, AgentID: "enrolled"}
	if _, err := BuildDNSConfig(server, state); err == nil {
		t.Fatal("DoQ accepted without reported kernel capability")
	}
	server.KernelCapabilities = []string{"dns_doq_v1"}
	if _, err := BuildDNSConfig(server, state); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDNSListRejectsWrongKindAndPrivateBootstrap(t *testing.T) {
	list := *testDNSState(1).BootstrapList
	list.Candidates[0].Transport = model.DNSTransportDoH
	if err := ValidateDNSList(list); err == nil {
		t.Fatal("expected bootstrap transport validation error")
	}
	list = *testDNSState(1).BootstrapList
	list.Candidates[0].Server = "192.168.1.1"
	if err := ValidateDNSList(list); err == nil {
		t.Fatal("expected private bootstrap address validation error")
	}
}

func TestValidateDNSCandidateAllowsAtSignInDoHPath(t *testing.T) {
	candidate := model.DNSCandidate{
		Tag:       "novaxns",
		Transport: model.DNSTransportDoH,
		Server:    "global.novaxns.one",
		Port:      443,
		Path:      "/@hockey2168/dns-query",
		TLSName:   "global.novaxns.one",
	}
	if err := ValidateDNSCandidate(candidate); err != nil {
		t.Fatalf("custom DoH path rejected: %v", err)
	}
}

func TestGeneratedConfigUsesCurrentDomainResolverShape(t *testing.T) {
	config, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "edge", IPStack: model.IPStackPreferIPv6},
		nil,
		[]model.Outbound{{ID: 2, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, TargetAddress: "example.com", TargetPort: 443, ConfigJSON: `{"domain_resolver":{"server":"bootstrap-primary","strategy":"prefer_ipv4"}}`, Enabled: true}},
		nil,
		[]model.User{{Username: "u", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	// Business targets follow the DNS policy's final resolver (the encrypted
	// failover group); only infrastructure lookups stay on bootstrap.
	resolver, ok := parsed.Route["default_domain_resolver"].(map[string]any)
	if !ok || resolver["server"] != parsed.DNS["final"] || resolver["server"] != "remote" || resolver["strategy"] != "prefer_ipv6" {
		t.Fatalf("default_domain_resolver = %#v, want final resolver object with prefer_ipv6", parsed.Route["default_domain_resolver"])
	}
	outbound := parsed.Outbounds[2]
	if _, ok := outbound["domain_strategy"]; ok {
		t.Fatalf("outbound should not emit deprecated domain_strategy: %#v", outbound)
	}
	dialResolver, ok := outbound["domain_resolver"].(map[string]any)
	if !ok || dialResolver["server"] != "bootstrap-primary" || dialResolver["strategy"] != "prefer_ipv4" {
		t.Fatalf("outbound domain_resolver = %#v", outbound["domain_resolver"])
	}
}

func TestBuildDNSConfigUsesListDraftWhenSelectionIsMissing(t *testing.T) {
	state := testDNSState(1)
	dns, err := BuildDNSConfig(model.Server{ID: 1, Name: "edge", IPStack: model.IPStackPreferIPv6}, state)
	if err != nil {
		t.Fatal(err)
	}
	servers := dns["servers"].([]map[string]any)
	if len(servers) != 6 || servers[0]["tag"] != "remote-primary" || servers[3]["tag"] != "bootstrap-secondary" || servers[5]["tag"] != "remote" {
		t.Fatalf("draft dns servers = %#v", servers)
	}
}

func TestGenerateServerConfigRejectsHY2WhenUDPInboundBlocked(t *testing.T) {
	_, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "s1", UDPInboundMode: model.UDPInboundBlock},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "hy2", Protocol: model.ProtocolHY2, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		nil,
		nil,
		[]model.User{{Username: "u", Status: "active", ProxyPassword: "pass"}},
	)
	if err == nil {
		t.Fatal("expected HY2 inbound to be rejected when UDP inbound is blocked")
	}
}

func TestUoTPolicyDefaultsVLESSAndSS(t *testing.T) {
	config, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "s1", UDPInboundMode: model.UDPInboundUoT},
		[]model.Inbound{
			{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 2, ServerID: 1, Name: "ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"udp_over_tcp":{"enabled":true}}`, Enabled: true},
		},
		[]model.Outbound{{ID: 2, ServerID: 1, Name: "ss", Protocol: model.ProtocolSS, TargetAddress: "example.com", TargetPort: 8388, ConfigJSON: `{}`, Enabled: true}},
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
	if _, ok := parsed.Inbounds[0]["packet_encoding"]; ok {
		t.Fatalf("vless inbound must not include packet_encoding: %#v", parsed.Inbounds[0])
	}
	if parsed.Inbounds[1]["network"] != "tcp" {
		t.Fatalf("UoT shadowsocks inbound must be TCP-only: %#v", parsed.Inbounds[1])
	}
	if _, ok := parsed.Inbounds[1]["udp_over_tcp"]; ok {
		t.Fatalf("shadowsocks inbound must not include outbound-only udp_over_tcp: %#v", parsed.Inbounds[1])
	}
	if _, ok := parsed.Outbounds[2]["udp_over_tcp"]; ok {
		t.Fatalf("server UDP inbound policy must not change unrelated outbounds: %#v", parsed.Outbounds[2])
	}
}

func TestBlockedUDPUsesTCPOnlyShadowsocksInbound(t *testing.T) {
	config, err := GenerateServerConfig(
		model.Server{ID: 1, Name: "s1", UDPInboundMode: model.UDPInboundBlock},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"network":"udp"}`, Enabled: true}},
		nil,
		nil,
		[]model.User{{Username: "u", Status: "active", ProxyPassword: "pass"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Inbounds[0]["network"] != "tcp" {
		t.Fatalf("blocked UDP policy did not override shadowsocks inbound: %#v", parsed.Inbounds[0])
	}
}

func TestBuildDNSConfigDefaultState(t *testing.T) {
	dns, err := BuildDNSConfig(model.Server{ID: 1, Name: "v6", IPStack: model.IPStackIPv6Only}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dns["strategy"] != "prefer_ipv6" {
		t.Fatalf("strategy = %v, want prefer_ipv6", dns["strategy"])
	}
	servers := dns["servers"].([]map[string]any)
	if len(servers) != 6 || servers[2]["tag"] != "bootstrap-primary" || servers[2]["server"] != "2606:4700:4700::1111" || servers[3]["server"] != "2001:4860:4860::8888" || servers[5]["tag"] != "remote" {
		t.Fatalf("default dns servers = %#v", servers)
	}
}

func testDNSState(serverID int64) *DNSConfigState {
	return &DNSConfigState{
		Policy: &model.ServerDNSPolicy{ServerID: serverID, EncryptedListID: 1, BootstrapListID: 2, Revision: 1, Strategy: "auto"},
		EncryptedList: &model.DNSList{ID: 1, Name: "encrypted", Kind: model.DNSListEncrypted, Revision: 1, Enabled: true, Candidates: []model.DNSCandidate{
			{Tag: "cloudflare", Transport: model.DNSTransportDoH, Server: "cloudflare-dns.com", Port: 443, Path: "/dns-query", TLSName: "cloudflare-dns.com"},
			{Tag: "google", Transport: model.DNSTransportDoT, Server: "dns.google", Port: 853, TLSName: "dns.google"},
		}},
		BootstrapList: &model.DNSList{ID: 2, Name: "bootstrap", Kind: model.DNSListBootstrap, Revision: 1, Enabled: true, Candidates: []model.DNSCandidate{
			{Tag: "cloudflare", Transport: model.DNSTransportUDP, Server: "1.1.1.1", Port: 53},
			{Tag: "google", Transport: model.DNSTransportTCP, Server: "8.8.8.8", Port: 53},
		}},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildDNSConfigFiltersBootstrapByEffectiveStack(t *testing.T) {
	state := testDNSState(1)
	state.BootstrapList.Candidates = []model.DNSCandidate{
		{Tag: "cloudflare-udp", Transport: model.DNSTransportUDP, Server: "1.1.1.1", Port: 53},
		{Tag: "google-tcp", Transport: model.DNSTransportTCP, Server: "8.8.8.8", Port: 53},
		{Tag: "cloudflare-udp-v6", Transport: model.DNSTransportUDP, Server: "2606:4700:4700::1111", Port: 53},
		{Tag: "google-tcp-v6", Transport: model.DNSTransportTCP, Server: "2001:4860:4860::8888", Port: 53},
	}
	tests := []struct {
		name          string
		stack         model.IPStack
		wantPrimary   string
		wantSecondary string
		wantStrategy  string
	}{
		{name: "ipv6 only", stack: model.IPStackIPv6Only, wantPrimary: "2606:4700:4700::1111", wantSecondary: "2001:4860:4860::8888", wantStrategy: "prefer_ipv6"},
		{name: "ipv4 only", stack: model.IPStackIPv4Only, wantPrimary: "1.1.1.1", wantSecondary: "8.8.8.8", wantStrategy: "prefer_ipv4"},
		{name: "dual stack", stack: model.IPStackDualStack, wantPrimary: "1.1.1.1", wantSecondary: "8.8.8.8", wantStrategy: "prefer_ipv4"},
		{name: "prefer ipv6", stack: model.IPStackPreferIPv6, wantPrimary: "2606:4700:4700::1111", wantSecondary: "2001:4860:4860::8888", wantStrategy: "prefer_ipv6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dns, err := BuildDNSConfig(model.Server{ID: 1, IPStack: tt.stack}, state)
			if err != nil {
				t.Fatal(err)
			}
			if dns["strategy"] != tt.wantStrategy {
				t.Fatalf("strategy = %v, want %v", dns["strategy"], tt.wantStrategy)
			}
			servers := dns["servers"].([]map[string]any)
			byTag := map[string]map[string]any{}
			for _, item := range servers {
				byTag[item["tag"].(string)] = item
			}
			if byTag["bootstrap-primary"]["server"] != tt.wantPrimary || byTag["bootstrap-secondary"]["server"] != tt.wantSecondary {
				t.Fatalf("bootstrap servers = primary %v secondary %v, want %v / %v", byTag["bootstrap-primary"]["server"], byTag["bootstrap-secondary"]["server"], tt.wantPrimary, tt.wantSecondary)
			}
			if _, ok := byTag["remote-primary"]; !ok {
				t.Fatalf("remote resolver missing: %#v", byTag)
			}
		})
	}
}

func TestDNSBenchmarkPlanForPolicyFiltersBootstrapByStack(t *testing.T) {
	state := testDNSState(1)
	state.BootstrapList.Candidates = []model.DNSCandidate{
		{Tag: "cloudflare-udp", Transport: model.DNSTransportUDP, Server: "1.1.1.1", Port: 53},
		{Tag: "google-tcp", Transport: model.DNSTransportTCP, Server: "8.8.8.8", Port: 53},
		{Tag: "cloudflare-udp-v6", Transport: model.DNSTransportUDP, Server: "2606:4700:4700::1111", Port: 53},
		{Tag: "google-tcp-v6", Transport: model.DNSTransportTCP, Server: "2001:4860:4860::8888", Port: 53},
	}
	plan, err := DNSBenchmarkPlanForPolicy(42, *state.Policy, state.EncryptedList, *state.BootstrapList, model.IPStackIPv6Only, model.DNSAutoTestFirstApply, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.BootstrapCandidates) != 2 {
		t.Fatalf("bootstrap candidates = %#v, want only the IPv6 entries", plan.BootstrapCandidates)
	}
	for _, candidate := range plan.BootstrapCandidates {
		if candidate.Server == "1.1.1.1" || candidate.Server == "8.8.8.8" {
			t.Fatalf("IPv6-only benchmark plan kept IPv4 bootstrap %#v", candidate)
		}
	}
	if len(plan.EncryptedCandidates) != 2 {
		t.Fatalf("encrypted candidates = %#v, want unfiltered", plan.EncryptedCandidates)
	}
}

func plainOnlyDNSState(serverID int64) *DNSConfigState {
	state := testDNSState(serverID)
	state.Policy.EncryptedListID = 0
	state.EncryptedList = nil
	return state
}

func TestBuildDNSConfigWithoutEncryptedListUsesBootstrapOnly(t *testing.T) {
	dns, err := BuildDNSConfig(model.Server{ID: 1, IPStack: model.IPStackPreferIPv4}, plainOnlyDNSState(1))
	if err != nil {
		t.Fatal(err)
	}
	servers := dns["servers"].([]map[string]any)
	if len(servers) != 4 {
		t.Fatalf("servers = %#v, want two bootstrap resolvers, local and the bootstrap failover group", servers)
	}
	if servers[0]["tag"] != "bootstrap-primary" || servers[1]["tag"] != "bootstrap-secondary" || servers[2]["tag"] != "local" || servers[3]["tag"] != "bootstrap" {
		t.Fatalf("servers = %#v, want no remote-* objects", servers)
	}
	if dns["final"] != "bootstrap" {
		t.Fatalf("final = %v, want bootstrap failover group", dns["final"])
	}
	for _, item := range servers {
		if item["type"] == "https" || item["type"] == "tls" || item["type"] == "quic" {
			t.Fatalf("plain-only dns emitted an encrypted resolver: %#v", item)
		}
	}
}

func TestDNSConfigStateForServerAllowsMissingEncryptedList(t *testing.T) {
	lists := []model.DNSList{{ID: 2, Name: "bootstrap", Kind: model.DNSListBootstrap, Revision: 1, Enabled: true}}
	policies := []model.ServerDNSPolicy{{ServerID: 1, EncryptedListID: 0, BootstrapListID: 2, Revision: 1}}
	state, err := DNSConfigStateForServer(1, lists, policies)
	if err != nil {
		t.Fatal(err)
	}
	if state.EncryptedList != nil {
		t.Fatalf("encrypted list = %#v, want nil for a plain-only policy", state.EncryptedList)
	}
	policies[0].EncryptedListID = 9
	if _, err := DNSConfigStateForServer(1, lists, policies); err == nil {
		t.Fatal("a policy that binds a missing encrypted list must still fail")
	}
}

func TestDNSBenchmarkPlanForPolicyWithoutEncryptedList(t *testing.T) {
	state := plainOnlyDNSState(1)
	plan, err := DNSBenchmarkPlanForPolicy(7, *state.Policy, nil, *state.BootstrapList, model.IPStackIPv4Only, model.DNSAutoTestFirstApply, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.EncryptedListID != 0 || plan.EncryptedListRevision != 0 || len(plan.EncryptedCandidates) != 0 {
		t.Fatalf("plan encrypted group = %d/%d/%#v, want empty", plan.EncryptedListID, plan.EncryptedListRevision, plan.EncryptedCandidates)
	}
	if len(plan.BootstrapCandidates) == 0 {
		t.Fatal("plan must still benchmark the bootstrap resolvers")
	}
	if _, err := DNSBenchmarkPlanForPolicy(7, *state.Policy, testDNSState(1).EncryptedList, *state.BootstrapList, model.IPStackIPv4Only, model.DNSAutoTestFirstApply, ""); err == nil {
		t.Fatal("an encrypted list that the policy does not bind must be rejected")
	}
}

// An explicit resolver reference never degrades to another resolver: a
// plain-DNS-only server has no remote-* transport, so a rule that still names
// one is rejected instead of silently answering from bootstrap.
func TestRequireDNSServerTagIsStrict(t *testing.T) {
	dns, err := BuildDNSConfig(model.Server{ID: 1}, plainOnlyDNSState(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequireDNSServerTag(dns, "remote-primary"); err == nil || !strings.Contains(err.Error(), "resolver_not_found") {
		t.Fatalf("missing resolver accepted: %v", err)
	}
	if got, err := RequireDNSServerTag(dns, "local"); err != nil || got != "local" {
		t.Fatalf("RequireDNSServerTag(local) = %q, %v", got, err)
	}
	full, err := BuildDNSConfig(model.Server{ID: 1}, testDNSState(1))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := RequireDNSServerTag(full, "remote-secondary"); err != nil || got != "remote-secondary" {
		t.Fatalf("RequireDNSServerTag(remote-secondary) = %q, %v", got, err)
	}
}
