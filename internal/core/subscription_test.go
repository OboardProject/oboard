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
	if nodes[0].Name != "hk-vless" || nodes[0].Group != "香港" {
		t.Fatalf("node = %#v", nodes[0])
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
	uri, err := shareURIFromNode(nodes[0])
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
	got := map[string]string{}
	for _, node := range nodes {
		got[node.Name] = node.Raw["server"].(string)
	}
	if got["server-default"] != "2001:db8::1" {
		t.Fatalf("server-default address = %q", got["server-default"])
	}
	if got["force-v4"] != "203.0.113.10" {
		t.Fatalf("force-v4 address = %q", got["force-v4"])
	}
	if got["custom"] != "entry.example.com" {
		t.Fatalf("custom address = %q", got["custom"])
	}
	if got["managed"] != "edge.example.com" {
		t.Fatalf("managed address = %q", got["managed"])
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
	for _, want := range []string{"proxies:", "proxy-groups:", "type: vless", "name: \"自动选择\"", "MATCH,自动选择"} {
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
