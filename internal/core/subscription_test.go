package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestGenerateSubscriptionWithPlanNodesAndGroups(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}
	inboundID := int64(101)
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}, {ID: 2, Name: "sg", PublicIPv4: "203.0.113.2"}},
		[]model.Inbound{
			{ID: inboundID, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 102, ServerID: 2, Name: "sg-ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{}`, Enabled: true},
		},
		SubscriptionOptions{
			EffectiveNodes:      map[string]bool{NodeKeyOf(model.AssignableNodeInbound, inboundID): true},
			EffectiveNodeGroups: map[string]string{NodeKeyOf(model.AssignableNodeInbound, inboundID): "香港"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if nodes[0].Name != "🇦🇶 hk" || nodes[0].Group != "香港" {
		t.Fatalf("node = %#v", nodes[0])
	}
	if nodes[0].Raw["tag"] != "🇦🇶 hk" {
		t.Fatalf("node tag = %v", nodes[0].Raw["tag"])
	}

	sub, err := GenerateSubscriptionWithOptions(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: inboundID, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		SubscriptionOptions{Format: model.SubscriptionFormatSingBox, EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, inboundID): true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(sub), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Outbounds) != 2 {
		t.Fatalf("outbounds = %d, want direct + assigned node", len(parsed.Outbounds))
	}
}

func TestRenderedSubscriptionUsesEffectiveNodeNamePrecedence(t *testing.T) {
	user := model.User{ID: 9, Username: "name-user", Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "pass-b"}
	server := model.Server{ID: 1, Name: "Tokyo-01", PublicIPv4: "203.0.113.9", RegionMode: "manual", RegionCode: "JP"}
	inbound := model.Inbound{ID: 21, ServerID: server.ID, Name: "Tokyo-01", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	key := NodeKeyOf(model.AssignableNodeInbound, inbound.ID)
	globalName := "日本 01"
	planName := "VIP 日本"
	tests := []struct {
		name       string
		global     map[string]*string
		plan       map[string]*string
		want       string
		planScoped bool
	}{
		{name: "source", want: "Tokyo-01"},
		{name: "global", global: map[string]*string{key: &globalName}, want: globalName},
		{name: "plan", global: map[string]*string{key: &globalName}, plan: map[string]*string{key: &planName}, want: planName, planScoped: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := SubscriptionOptions{EffectiveNodes: map[string]bool{key: true}, GlobalNodeNames: test.global, PlanNodeNames: test.plan}
			nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != 1 || !strings.Contains(nodes[0].Name, test.want) || nodes[0].HasPlanNameOverride != test.planScoped {
				t.Fatalf("effective node = %#v", nodes)
			}
			body, err := GenerateSubscriptionWithOptions(user, []model.Server{server}, []model.Inbound{inbound}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, test.want) {
				t.Fatalf("subscription body does not contain %q: %s", test.want, body)
			}
		})
	}
}

func TestSubscriptionRespectsInboundUserBindings(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{
			{ID: 1, ServerID: 1, Name: "allowed", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 2, ServerID: 1, Name: "blocked", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true},
		},
		SubscriptionOptions{EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Inbound.ID != 1 {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}
}

func TestSSHSubscriptionRequiresDeployedHostAndAuthorization(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "ssh-pass", SSHRandomID: "123456789012"}
	server := model.Server{ID: 1, Name: "tokyo", PublicIPv4: "203.0.113.10"}
	inbound := model.Inbound{ID: 11, ServerID: server.ID, Name: "ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, EntryIPMode: model.EntryIPModeIPv4, Enabled: true}
	base := SubscriptionOptions{
		EffectiveNodes:    map[string]bool{NodeKeyOf(model.AssignableNodeProxyPath, 17): true},
		SSHServerHostKeys: map[int64]string{server.ID: sshSubscriptionHostKey},
		ProxyPaths:        []model.ProxyPath{{ID: 17, Kind: model.ProxyPathKindDirect, Name: "direct", InboundID: inbound.ID, Enabled: true}},
	}
	tests := []struct {
		name   string
		mutate func(*SubscriptionOptions)
		want   int
	}{
		{name: "all state matches", want: 1},
		{name: "missing agent host key", mutate: func(opts *SubscriptionOptions) { opts.SSHServerHostKeys = nil }},
		{name: "user not authorized", mutate: func(opts *SubscriptionOptions) {
			opts.EffectiveNodes = map[string]bool{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := base
			opts.SSHServerHostKeys = map[int64]string{server.ID: base.SSHServerHostKeys[server.ID]}
			if test.mutate != nil {
				test.mutate(&opts)
			}
			nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != test.want {
				t.Fatalf("SSH nodes = %#v, want %d", nodes, test.want)
			}
			if test.want == 1 {
				raw := nodes[0].Raw
				if raw["server"] != server.PublicIPv4 || raw["username"] != "u123456789012-p17" || raw["password"] != user.ProxyPassword || !stringSetContains(stringListFromAny(raw["host_key"]), sshSubscriptionHostKey) {
					t.Fatalf("SSH node = %#v", raw)
				}
			}
		})
	}
}

func TestSSHStandaloneInboundRendersImplicitDirectBranch(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "ssh-pass", SSHRandomID: "123456789012"}
	server := model.Server{ID: 1, Name: "ixp", PublicIPv4: "203.0.113.10"}
	inbound := model.Inbound{ID: 11, ServerID: server.ID, Name: "ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, EntryIPMode: model.EntryIPModeIPv4, Enabled: true}
	opts := SubscriptionOptions{
		EffectiveNodes:    map[string]bool{NodeKeyOf(model.AssignableNodeInbound, inbound.ID): true},
		SSHServerHostKeys: map[int64]string{server.ID: sshSubscriptionHostKey},
	}
	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Key != NodeKeyOf(model.AssignableNodeInbound, inbound.ID) {
		t.Fatalf("implicit SSH direct node = %#v", nodes)
	}
	raw := nodes[0].Raw
	if raw["username"] != "u123456789012-p11" || raw["password"] != user.ProxyPassword || raw["server"] != server.PublicIPv4 {
		t.Fatalf("implicit SSH direct raw = %#v", raw)
	}

	opts.ProxyPaths = []model.ProxyPath{{ID: 17, Kind: model.ProxyPathKindDirect, Name: "configured", InboundID: inbound.ID, Enabled: true}}
	nodes, err = BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("inbound grant bypassed configured SSH branch authorization: %#v", nodes)
	}
}

func TestSubscriptionWithoutEffectiveNodesReturnsNoNodes(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{
			{ID: 1, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
		},
		SubscriptionOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("nodes without authorization leaked: %#v", nodes)
	}
}

func TestShadowsocks2022SubscriptionUsesServerAndUserPassword(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "user-pass"}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "ss2022", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"method":"2022-blake3-aes-128-gcm","password":"server-pass"}`, Enabled: true}},
		SubscriptionOptions{EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantPassword := normalizeSS2022Key("server-pass", "2022-blake3-aes-128-gcm") + ":" + normalizeSS2022Key("user-pass", "2022-blake3-aes-128-gcm")
	if got := nodes[0].Raw["password"]; got != wantPassword {
		t.Fatalf("password = %v", got)
	}
}

func TestShadowsocksUoTSubscriptionConfiguresClientOutbound(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "user-pass"}
	server := model.Server{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1", UDPInboundMode: model.UDPInboundUoT}
	inbound := model.Inbound{ID: 1, ServerID: 1, Name: "ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"method":"chacha20-ietf-poly1305"}`, Enabled: true}
	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !udpOverTCPEnabled(nodes[0].Raw["udp_over_tcp"]) {
		t.Fatalf("UoT client option missing: %#v", nodes)
	}
	uot, ok := nodes[0].Raw["udp_over_tcp"].(map[string]any)
	if !ok || intFromAny(uot["version"]) != shadowsocksUoTVersion {
		t.Fatalf("UoT version = %#v, want %d", nodes[0].Raw["udp_over_tcp"], shadowsocksUoTVersion)
	}

	clash, err := renderSubscriptionTarget(nodes, model.SubscriptionFormatMihomo)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"udp: true", "udp-over-tcp: true", "udp-over-tcp-version: 2"} {
		if !strings.Contains(clash, want) {
			t.Fatalf("Clash UoT subscription missing %q:\n%s", want, clash)
		}
	}
}

func TestVLESSRealitySubscriptionUsesTCPRealityVision(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "user-pass"}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "reality", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{
  "flow": "xtls-rprx-vision",
  "packet_encoding": "xudp",
  "tls": {
    "enabled": true,
    "server_name": "www.cloudflare.com",
    "reality": {
      "enabled": true,
      "handshake": {"server": "www.cloudflare.com", "server_port": 443},
      "private_key": "server-private",
      "public_key": "client-public",
      "short_id": "abcd"
    }
  }
}`, Enabled: true}},
		SubscriptionOptions{EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	raw := nodes[0].Raw
	if raw["flow"] != "xtls-rprx-vision" {
		t.Fatalf("flow = %v", raw["flow"])
	}
	tls, ok := raw["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls missing: %#v", raw)
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok {
		t.Fatalf("reality missing: %#v", tls)
	}
	if reality["public_key"] != "client-public" || reality["short_id"] != "abcd" {
		t.Fatalf("reality client fields = %#v", reality)
	}
	if _, ok := reality["private_key"]; ok {
		t.Fatalf("private key leaked into subscription: %#v", reality)
	}
	if _, ok := reality["handshake"]; ok {
		t.Fatalf("handshake leaked into subscription: %#v", reality)
	}
	utls, ok := tls["utls"].(map[string]any)
	if !ok || utls["fingerprint"] != "chrome" {
		t.Fatalf("reality subscription should default uTLS chrome fingerprint: %#v", tls)
	}
	uri, err := renderSubscriptionTarget(nodes, model.SubscriptionFormatV2RayURI)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type=tcp", "security=reality", "flow=xtls-rprx-vision", "pbk=client-public", "sid=abcd", "fp=chrome"} {
		if !strings.Contains(uri, want) {
			t.Fatalf("uri missing %q: %s", want, uri)
		}
	}
}

func TestSubscriptionFiltersUnauthorizedProxyPaths(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass"}
	inbound := model.Inbound{ID: 1, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	paths := []model.ProxyPath{{ID: 10, InboundID: inbound.ID, Kind: model.ProxyPathKindDirect, Name: "allowed", Enabled: true}, {ID: 11, InboundID: inbound.ID, Kind: model.ProxyPathKindDirect, Name: "blocked", Enabled: true}}
	nodes, err := BuildSubscriptionNodes(user, []model.Server{{ID: 1, Name: "edge", PublicIPv4: "203.0.113.1"}}, []model.Inbound{inbound}, SubscriptionOptions{
		EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeProxyPath, 10): true},
		ProxyPaths:     paths,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantUUID := proxyPathBranchUser(paths[0], inbound, user).ProxyUUID
	if len(nodes) != 1 || nodes[0].Raw["uuid"] != wantUUID {
		t.Fatalf("nodes = %#v, want only allowed path", nodes)
	}
}

func TestSubscriptionFamilySplitEmitsSingleLogicalNode(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass"}
	server := model.Server{ID: 1, Name: "edge", PublicIPv4: "203.0.113.1", PublicIPv6: "2001:db8::1"}
	inbound := model.Inbound{ID: 1, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true}
	paths := []model.ProxyPath{
		{ID: 10, InboundID: inbound.ID, Kind: model.ProxyPathKindDirect, Name: "dual logical node", Enabled: true},
		{ID: 11, InboundID: inbound.ID, Kind: model.ProxyPathKindDirect, Name: "IPv6 implementation branch", Enabled: true},
	}
	sourceID, targetID := paths[0].ID, paths[1].ID
	rule := model.RoutingRule{ID: 5, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &sourceID, Action: model.RouteActionFamilySplit, IPv4TargetProxyPathID: &sourceID, IPv6TargetProxyPathID: &targetID, Enabled: true}
	effective := map[string]bool{
		NodeKeyOf(model.AssignableNodeProxyPath, sourceID): true,
		NodeKeyOf(model.AssignableNodeProxyPath, targetID): true,
	}
	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{ProxyPaths: paths, RoutingRules: []model.RoutingRule{rule}, EffectiveNodes: effective})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != sourceID {
		t.Fatalf("family split nodes = %#v, want only source path %d", nodes, sourceID)
	}
	delete(effective, NodeKeyOf(model.AssignableNodeProxyPath, sourceID))
	nodes, err = BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{ProxyPaths: paths, RoutingRules: []model.RoutingRule{rule}, EffectiveNodes: effective})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != targetID {
		t.Fatalf("independently granted target branch was hidden without its logical source: %#v", nodes)
	}
}

func TestSubscriptionEntryAddressOverride(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::1", EntryIPMode: model.EntryIPModeIPv6}},
		[]model.Inbound{
			{ID: 1, ServerID: 1, Name: "server-default", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, EntryIPMode: model.EntryIPModeAuto, ConfigJSON: `{}`, Enabled: true},
			{ID: 2, ServerID: 1, Name: "force-v4", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, EntryIPMode: model.EntryIPModeIPv4, ConfigJSON: `{}`, Enabled: true},
			{ID: 3, ServerID: 1, Name: "custom", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 9443, EntryIPMode: model.EntryIPModeCustom, ExternalIP: "entry.example.com", ConfigJSON: `{}`, Enabled: true},
			{ID: 4, ServerID: 1, Name: "managed", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, EntryIPMode: model.EntryIPModeCustom, ExternalIP: "origin.example.net", DNSSyncEnabled: true, DNSDomain: "edge.example.com", ConfigJSON: `{}`, Enabled: true},
		},
		SubscriptionOptions{EffectiveNodes: map[string]bool{
			NodeKeyOf(model.AssignableNodeInbound, 1): true,
			NodeKeyOf(model.AssignableNodeInbound, 2): true,
			NodeKeyOf(model.AssignableNodeInbound, 3): true,
			NodeKeyOf(model.AssignableNodeInbound, 4): true,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, node := range nodes {
		got[node.Inbound.ID] = node.Raw["server"].(string)
	}
	if got[1] != "2001:db8::1" {
		t.Fatalf("server-default address = %q", got[1])
	}
	if got[2] != "203.0.113.10" {
		t.Fatalf("force-v4 address = %q", got[2])
	}
	if got[3] != "entry.example.com" {
		t.Fatalf("custom address = %q", got[3])
	}
	if got[4] != "edge.example.com" {
		t.Fatalf("managed address = %q", got[4])
	}
}

func TestResolveEntryAddressHostUsesIPForStaticSingleStack(t *testing.T) {
	ipv4 := model.Server{ID: 1, Name: "v4", PublicIPv4: "203.0.113.10"}
	dual := model.Server{ID: 2, Name: "dual", PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::10"}
	domain := "entry.example.com"
	for _, tc := range []struct {
		name    string
		inbound model.Inbound
		server  model.Server
		always  bool
		want    string
	}{
		{
			name:    "static ipv4 a records",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "a", EntryIPMode: model.EntryIPModeAuto},
			server:  ipv4,
			want:    "203.0.113.10",
		},
		{
			name:    "static ipv6 aaaa records",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "aaaa", EntryIPMode: model.EntryIPModeIPv6},
			server:  dual,
			want:    "2001:db8::10",
		},
		{
			name:    "dual stack both records",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "both", EntryIPMode: model.EntryIPModeAuto},
			server:  dual,
			want:    domain,
		},
		{
			name:    "dual stack a records stay ipv4",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "a", EntryIPMode: model.EntryIPModeAuto},
			server:  dual,
			want:    "203.0.113.10",
		},
		{
			name:    "ddns uses domain",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "a", DDNSEnabled: true, EntryIPMode: model.EntryIPModeAuto},
			server:  ipv4,
			want:    domain,
		},
		{
			name:    "always use domain",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "a", EntryIPMode: model.EntryIPModeAuto},
			server:  ipv4,
			always:  true,
			want:    domain,
		},
		{
			name:    "custom domain target keeps managed name",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, EntryIPMode: model.EntryIPModeCustom, ExternalIP: "origin.example.net"},
			server:  dual,
			want:    domain,
		},
		{
			name:    "custom ipv4 stays ip",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "a", EntryIPMode: model.EntryIPModeCustom, ExternalIP: "198.51.100.8"},
			server:  dual,
			want:    "198.51.100.8",
		},
		{
			name:    "both records on ipv4-only server still use domain",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "both", EntryIPMode: model.EntryIPModeAuto},
			server:  ipv4,
			want:    domain,
		},
		{
			name:    "ipv4 mode on dual stack",
			inbound: model.Inbound{DNSSyncEnabled: true, DNSDomain: domain, DNSRecordTypes: "both", EntryIPMode: model.EntryIPModeIPv4},
			server:  dual,
			want:    "203.0.113.10",
		},
		{
			name:    "dns disabled uses ip",
			inbound: model.Inbound{DNSDomain: domain, EntryIPMode: model.EntryIPModeAuto},
			server:  ipv4,
			want:    "203.0.113.10",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveEntryAddressHost(tc.inbound, tc.server, tc.always); got != tc.want {
				t.Fatalf("ResolveEntryAddressHost = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSubscriptionStandaloneNamesUseVisibleServersAndProtocols(t *testing.T) {
	user := model.User{ID: 7, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	servers := []model.Server{{ID: 20, Name: "香港", PublicIPv4: "203.0.113.20"}, {ID: 10, Name: "香港", PublicIPv4: "203.0.113.10"}, {ID: 30, Name: "东京", PublicIPv4: "203.0.113.30"}}
	inbounds := []model.Inbound{
		{ID: 202, ServerID: 20, Name: "ignored-hy2", Protocol: model.ProtocolHY2, Port: 8443, ConfigJSON: `{}`, Enabled: true},
		{ID: 201, ServerID: 20, Name: "ignored-vless", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true},
		{ID: 101, ServerID: 10, Name: "ignored-ss", Protocol: model.ProtocolSS, Port: 8388, ConfigJSON: `{}`, Enabled: true},
		{ID: 302, ServerID: 30, Name: "ignored-second-vless", Protocol: model.ProtocolVLESS, Port: 9443, ConfigJSON: `{}`, Enabled: true},
		{ID: 301, ServerID: 30, Name: "ignored-first-vless", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true},
	}
	effective := make(map[string]bool, len(inbounds))
	for _, inbound := range inbounds {
		effective[NodeKeyOf(model.AssignableNodeInbound, inbound.ID)] = true
	}
	nodes, err := BuildSubscriptionNodes(user, servers, inbounds, SubscriptionOptions{EffectiveNodes: effective})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{
		101: "🇦🇶 香港｜01",
		201: "🇦🇶 香港｜02｜VLESS",
		202: "🇦🇶 香港｜02｜HY2",
		301: "🇦🇶 东京｜01",
		302: "🇦🇶 东京｜02",
	}
	for _, node := range nodes {
		if got := node.Name; got != want[node.Inbound.ID] {
			t.Fatalf("inbound %d name = %q, want %q", node.Inbound.ID, got, want[node.Inbound.ID])
		}
		if node.Raw["tag"] != node.Name {
			t.Fatalf("inbound %d tag = %v, name = %q", node.Inbound.ID, node.Raw["tag"], node.Name)
		}
	}
}

func TestSubscriptionStandaloneNamesUseOnlyVisibleProtocols(t *testing.T) {
	user := model.User{ID: 7, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	server := model.Server{ID: 1, Name: "香港", PublicIPv4: "203.0.113.1"}
	inbounds := []model.Inbound{
		{ID: 1, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true},
		{ID: 2, ServerID: 1, Name: "hy2", Protocol: model.ProtocolHY2, Port: 8443, ConfigJSON: `{}`, Enabled: true},
	}
	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, inbounds, SubscriptionOptions{EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "🇦🇶 香港" {
		t.Fatalf("nodes = %#v", nodes)
	}
}

func TestSubscriptionNamesAvoidPathsAndDisambiguateImportedNodes(t *testing.T) {
	user := model.User{ID: 7, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	servers := []model.Server{
		{ID: 1, Name: "入口", PublicIPv4: "203.0.113.1"},
		{ID: 2, Name: "链路名", PublicIPv4: "203.0.113.2"},
		{ID: 3, Name: "导入名", PublicIPv4: "203.0.113.3"},
	}
	inbounds := []model.Inbound{
		{ID: 1, ServerID: 1, Name: "path-root", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true},
		{ID: 2, ServerID: 2, Name: "path-collision", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true},
		{ID: 3, ServerID: 3, Name: "external-collision", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true},
	}
	paths := []model.ProxyPath{{ID: 10, Kind: model.ProxyPathKindDirect, NameMode: model.ProxyPathNameCustom, NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameLiteral, Value: "链路名"}}, InboundID: 1, Enabled: true}}
	externals := []model.ExternalOutbound{
		{ID: 30, Name: "重复导入", Protocol: model.ProtocolSocks, TargetAddress: "203.0.113.30", TargetPort: 1080, ConfigJSON: `{}`, ExposeToUsers: true, Enabled: true},
		{ID: 20, Name: "重复导入", Protocol: model.ProtocolSocks, TargetAddress: "203.0.113.20", TargetPort: 1080, ConfigJSON: `{}`, ExposeToUsers: true, Enabled: true},
		{ID: 40, Name: "导入名", Protocol: model.ProtocolSocks, TargetAddress: "203.0.113.40", TargetPort: 1080, ConfigJSON: `{}`, ExposeToUsers: true, Enabled: true},
	}
	nodes, err := BuildSubscriptionNodes(user, servers, inbounds, SubscriptionOptions{
		EffectiveNodes: map[string]bool{
			NodeKeyOf(model.AssignableNodeInbound, 1):           true,
			NodeKeyOf(model.AssignableNodeInbound, 2):           true,
			NodeKeyOf(model.AssignableNodeInbound, 3):           true,
			NodeKeyOf(model.AssignableNodeProxyPath, 10):        true,
			NodeKeyOf(model.AssignableNodeExternalOutbound, 20): true,
			NodeKeyOf(model.AssignableNodeExternalOutbound, 30): true,
			NodeKeyOf(model.AssignableNodeExternalOutbound, 40): true,
		},
		ProxyPaths:        paths,
		ExternalOutbounds: externals,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"🇦🇶 链路名":     true,
		"🇦🇶 链路名｜01":  true,
		"🇦🇶 导入名":     true,
		"🇦🇶 导入名｜01":  true,
		"🇦🇶 重复导入｜01": true,
		"🇦🇶 重复导入｜02": true,
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		if seen[node.Name] {
			t.Fatalf("duplicate node name %q: %#v", node.Name, nodes)
		}
		seen[node.Name] = true
		if node.Raw["tag"] != node.Name {
			t.Fatalf("node %q tag = %v", node.Name, node.Raw["tag"])
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("missing name %q: %#v", name, nodes)
		}
	}
}

func TestManagedCertificateDomainOverridesSubscriptionSNI(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	server := model.Server{ID: 1, Name: "hk", PublicIPv4: "203.0.113.10"}
	for _, test := range []struct {
		name     string
		protocol model.Protocol
		config   string
	}{
		{name: "vless", protocol: model.ProtocolVLESS, config: `{"tls":{"enabled":true,"server_name":"example.com"}}`},
		{name: "hy2", protocol: model.ProtocolHY2, config: `{"tls":{"enabled":true,"server_name":"example.com"}}`},
		{name: "anytls", protocol: model.ProtocolAnyTLS, config: `{"tls":{"enabled":true,"server_name":"example.com"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			inbound := model.Inbound{ID: 1, ServerID: 1, Name: test.name, Protocol: test.protocol, ListenIP: "0.0.0.0", Port: 443, CertificateMode: model.CertificateModeExplicit, CertificateDomain: "entry.example.net", ConfigJSON: test.config, Enabled: true}
			nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}})
			if err != nil {
				t.Fatal(err)
			}
			tls, ok := nodes[0].Raw["tls"].(map[string]any)
			if !ok || tls["server_name"] != "entry.example.net" {
				t.Fatalf("subscription TLS = %#v", nodes[0].Raw["tls"])
			}
			uri, err := renderSubscriptionTarget(nodes, model.SubscriptionFormatV2RayURI)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(uri, "sni=entry.example.net") || strings.Contains(uri, "sni=example.com") {
				t.Fatalf("subscription URI has wrong SNI: %s", uri)
			}
		})
	}
}

func TestSubscriptionHostUsesIPWhileTLSUsesCertificateDomain(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	server := model.Server{ID: 1, Name: "hk", PublicIPv4: "203.0.113.10"}
	for _, test := range []struct {
		name     string
		protocol model.Protocol
		config   string
	}{
		{name: "anytls", protocol: model.ProtocolAnyTLS, config: `{"tls":{"enabled":true,"server_name":"example.com"}}`},
		{name: "hy2", protocol: model.ProtocolHY2, config: `{"tls":{"enabled":true,"server_name":"example.com"}}`},
		{name: "vless", protocol: model.ProtocolVLESS, config: `{"tls":{"enabled":true,"server_name":"example.com"}}`},
		{name: "shadowsocks", protocol: model.ProtocolSS, config: `{"method":"aes-128-gcm"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			inbound := model.Inbound{
				ID: 1, ServerID: 1, Name: test.name, Protocol: test.protocol, ListenIP: "0.0.0.0", Port: 443,
				DNSSyncEnabled: true, DNSDomain: "entry.example.net", DNSRecordTypes: "a",
				CertificateMode: model.CertificateModeAuto, CertificateDomain: "entry.example.net",
				ConfigJSON: test.config, Enabled: true,
			}
			nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}})
			if err != nil {
				t.Fatal(err)
			}
			if nodes[0].Raw["server"] != "203.0.113.10" {
				t.Fatalf("subscription host = %#v, want public IPv4", nodes[0].Raw["server"])
			}
			if test.protocol == model.ProtocolSS {
				return
			}
			tls, ok := nodes[0].Raw["tls"].(map[string]any)
			if !ok || tls["server_name"] != "entry.example.net" {
				t.Fatalf("subscription TLS SNI = %#v", nodes[0].Raw["tls"])
			}
		})
	}
}

func TestGenerateClashMetaSubscription(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}
	sub, err := GenerateSubscriptionWithOptions(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{"tls":{"enabled":true,"server_name":"example.com"}}`, Enabled: true}},
		SubscriptionOptions{
			Format:              model.SubscriptionFormatMihomo,
			EffectiveNodes:      map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true},
			EffectiveNodeGroups: map[string]string{NodeKeyOf(model.AssignableNodeInbound, 1): "自动选择"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proxies:", "proxy-groups:", "type: vless", "name: 自动选择", "MATCH,自动选择"} {
		if !strings.Contains(sub, want) {
			t.Fatalf("clash subscription missing %q:\n%s", want, sub)
		}
	}
}

func TestSubscriptionFormatUsesRequestOption(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}
	sub, err := GenerateSubscriptionWithOptions(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		SubscriptionOptions{
			Format:              model.SubscriptionFormatMihomo,
			EffectiveNodes:      map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true},
			EffectiveNodeGroups: map[string]string{NodeKeyOf(model.AssignableNodeInbound, 1): "自动选择"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sub, "proxies:") {
		t.Fatalf("requested format was not used:\n%s", sub)
	}
}

func TestSubStoreTargetFormatsAreAccepted(t *testing.T) {
	for _, format := range []model.SubscriptionFormat{
		"stash",
		"mihomo",
		"surfboard",
		"surge",
		"surge-mac",
		"loon",
		"egern",
		"shadowrocket",
		"qx",
		"sing-box",
		"v2ray",
		"v2ray-uri",
		"auto",
	} {
		if !IsSupportedSubscriptionFormat(format) {
			t.Fatalf("format %q is not accepted", format)
		}
	}
}

func TestPlainJSONSubscriptionFormatIsRejected(t *testing.T) {
	for _, format := range []model.SubscriptionFormat{"plain-json", "plainjson", "plain json", "json", "clash", "clash-meta", "mieru", "sing-box-mieru"} {
		if IsSupportedSubscriptionFormat(format) {
			t.Fatalf("removed format %q is still accepted", format)
		}
	}
}

func FuzzNormalizeSubscriptionFormat(f *testing.F) {
	for _, format := range SupportedSubscriptionFormats() {
		f.Add(string(format))
	}
	f.Add("unknown-format")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1024 {
			t.Skip()
		}
		normalized := NormalizeSubscriptionFormatForAPI(model.SubscriptionFormat(raw))
		if IsSupportedSubscriptionFormat(model.SubscriptionFormat(raw)) && !IsSupportedSubscriptionFormat(normalized) {
			t.Fatalf("normalizer returned unsupported format %q for %q", normalized, raw)
		}
	})
}
