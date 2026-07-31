package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestGenerateSubscriptionWithAssignmentsAndGroups(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}
	profile := &model.SubscriptionProfile{ID: 3, Name: "premium", GroupName: "默认组", Enabled: true}
	inboundID := int64(101)
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}, {ID: 2, Name: "sg", PublicIPv4: "203.0.113.2"}},
		[]model.Inbound{
			{ID: inboundID, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 102, ServerID: 2, Name: "sg-ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{}`, Enabled: true},
		},
		SubscriptionOptions{Profile: profile, Assignments: []model.SubscriptionAssignment{{ProfileID: 3, UserID: 7, InboundID: &inboundID, GroupName: "香港", Enabled: true}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if nodes[0].Name != "hk" || nodes[0].Group != "香港" {
		t.Fatalf("node = %#v", nodes[0])
	}
	if nodes[0].Raw["tag"] != "hk" {
		t.Fatalf("node tag = %v", nodes[0].Raw["tag"])
	}

	sub, err := GenerateSubscriptionWithOptions(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: inboundID, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		SubscriptionOptions{Profile: profile, Assignments: []model.SubscriptionAssignment{{ProfileID: 3, UserID: 7, InboundID: &inboundID, GroupName: "香港", Enabled: true}}},
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

func TestSubscriptionRespectsInboundUserBindings(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{
			{ID: 1, ServerID: 1, Name: "allowed", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
			{ID: 2, ServerID: 1, Name: "blocked", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: `{}`, Enabled: true},
		},
		SubscriptionOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 7, Enabled: true}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Inbound.ID != 1 {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}
}

func TestSSHSubscriptionRequiresCredentialDeploymentAndAuthorization(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active"}
	server := model.Server{ID: 1, Name: "tokyo", PublicIPv4: "203.0.113.10"}
	inbound := model.Inbound{ID: 11, ServerID: server.ID, Name: "ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, EntryIPMode: model.EntryIPModeIPv4, Enabled: true}
	base := SubscriptionOptions{
		InboundUsers:             []model.InboundUser{{InboundID: inbound.ID, UserID: user.ID, Enabled: true}},
		SSHPrivateKey:            sshSubscriptionPrivateKey,
		SSHCredentialFingerprint: "SHA256:user-key",
		SSHServerHostKeys:        map[int64]string{server.ID: sshSubscriptionHostKey},
		SSHDeployedFingerprints:  map[int64]string{server.ID: "SHA256:user-key"},
	}
	tests := []struct {
		name   string
		mutate func(*SubscriptionOptions)
		want   int
	}{
		{name: "all state matches", want: 1},
		{name: "missing private key", mutate: func(opts *SubscriptionOptions) { opts.SSHPrivateKey = "" }},
		{name: "missing credential fingerprint", mutate: func(opts *SubscriptionOptions) { opts.SSHCredentialFingerprint = "" }},
		{name: "credential not deployed", mutate: func(opts *SubscriptionOptions) { opts.SSHDeployedFingerprints = nil }},
		{name: "rotated credential pending deployment", mutate: func(opts *SubscriptionOptions) { opts.SSHDeployedFingerprints[server.ID] = "SHA256:old-key" }},
		{name: "missing agent host key", mutate: func(opts *SubscriptionOptions) { opts.SSHServerHostKeys = nil }},
		{name: "user not authorized", mutate: func(opts *SubscriptionOptions) {
			opts.InboundUsers = []model.InboundUser{{InboundID: inbound.ID, UserID: 8, Enabled: true}}
		}},
		{name: "profile assignment missing", mutate: func(opts *SubscriptionOptions) {
			opts.Profile = &model.SubscriptionProfile{ID: 3, Enabled: true}
			opts.RequireAssignments = true
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := base
			opts.SSHServerHostKeys = map[int64]string{server.ID: base.SSHServerHostKeys[server.ID]}
			opts.SSHDeployedFingerprints = map[int64]string{server.ID: base.SSHDeployedFingerprints[server.ID]}
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
				if raw["server"] != server.PublicIPv4 || raw["username"] != "oboard-7" || raw["private_key"] != sshSubscriptionPrivateKey || !stringSetContains(stringListFromAny(raw["host_key"]), sshSubscriptionHostKey) {
					t.Fatalf("SSH node = %#v", raw)
				}
			}
		})
	}
}

func TestSubscriptionProfileWithoutAssignmentsReturnsNoNodes(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	profile := &model.SubscriptionProfile{ID: 9, Name: "empty-profile", GroupName: "default", Enabled: true}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{
			{ID: 1, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true},
		},
		SubscriptionOptions{
			Profile:            profile,
			RequireAssignments: true,
			Assignments:        nil,
			InboundUsers:       []model.InboundUser{{InboundID: 1, UserID: 7, Enabled: true}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("profile without assignments leaked nodes: %#v", nodes)
	}
}

func TestShadowsocks2022SubscriptionUsesServerAndUserPassword(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "user-pass"}
	nodes, err := BuildSubscriptionNodes(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "ss2022", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{"method":"2022-blake3-aes-128-gcm","password":"server-pass"}`, Enabled: true}},
		SubscriptionOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 7, Enabled: true}}},
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
	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !udpOverTCPEnabled(nodes[0].Raw["udp_over_tcp"]) {
		t.Fatalf("UoT client option missing: %#v", nodes)
	}

	clash, err := renderClashMetaSubscription(nodes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"udp: true", "udp-over-tcp: true"} {
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
		SubscriptionOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 7, Enabled: true}}},
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
		SubscriptionOptions{InboundUsers: []model.InboundUser{
			{InboundID: 1, UserID: 7, Enabled: true},
			{InboundID: 2, UserID: 7, Enabled: true},
			{InboundID: 3, UserID: 7, Enabled: true},
			{InboundID: 4, UserID: 7, Enabled: true},
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
	bindings := make([]model.InboundUser, 0, len(inbounds))
	for _, inbound := range inbounds {
		bindings = append(bindings, model.InboundUser{InboundID: inbound.ID, UserID: user.ID, Enabled: true})
	}
	nodes, err := BuildSubscriptionNodes(user, servers, inbounds, SubscriptionOptions{InboundUsers: bindings})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{
		101: "香港｜01",
		201: "香港｜02｜VLESS",
		202: "香港｜02｜HY2",
		301: "东京｜01",
		302: "东京｜02",
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
	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, inbounds, SubscriptionOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: user.ID, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "香港" {
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
		InboundUsers: []model.InboundUser{
			{InboundID: 1, UserID: user.ID, Enabled: true},
			{InboundID: 2, UserID: user.ID, Enabled: true},
			{InboundID: 3, UserID: user.ID, Enabled: true},
		},
		ProxyPaths:        paths,
		ExternalOutbounds: externals,
		ExternalOutboundAccessGrants: []model.ExternalOutboundAccessGrant{
			{ExternalOutboundID: 20, SubjectType: model.AccessSubjectUser, SubjectID: user.ID, Enabled: true},
			{ExternalOutboundID: 30, SubjectType: model.AccessSubjectUser, SubjectID: user.ID, Enabled: true},
			{ExternalOutboundID: 40, SubjectType: model.AccessSubjectUser, SubjectID: user.ID, Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"链路名":     true,
		"链路名｜01":  true,
		"导入名":     true,
		"导入名｜01":  true,
		"重复导入｜01": true,
		"重复导入｜02": true,
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
			nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{})
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

func TestGenerateClashMetaSubscription(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-1111-1111-111111111111", ProxyPassword: "pass-a"}
	sub, err := GenerateSubscriptionWithOptions(user,
		[]model.Server{{ID: 1, Name: "hk", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{"tls":{"enabled":true,"server_name":"example.com"}}`, Enabled: true}},
		SubscriptionOptions{Format: model.SubscriptionFormatClashMeta, Profile: &model.SubscriptionProfile{GroupName: "自动选择", Enabled: true}},
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
		SubscriptionOptions{Format: model.SubscriptionFormatClashMeta, Profile: &model.SubscriptionProfile{GroupName: "自动选择", Enabled: true}},
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
		"plain-json",
		"stash",
		"clash-meta",
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
		"clash",
	} {
		if !IsSupportedSubscriptionFormat(format) {
			t.Fatalf("format %q is not accepted", format)
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
