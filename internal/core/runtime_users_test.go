package core

import (
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func capableRuntimeUserServer(protocolCaps ...string) model.Server {
	caps := []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers}
	caps = append(caps, protocolCaps...)
	return model.Server{ID: 1, Name: "edge", AgentID: "agent-1", KernelCapabilities: caps}
}

func TestGenerateServerConfigRewritesCapableRuntimeUsers(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"},
	}
	var pkg *RuntimeUserPackage
	config, err := generateFixtureConfig(
		capableRuntimeUserServer(model.AgentCapabilityRuntimeUsersVLESS),
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		nil,
		nil,
		users,
		ConfigOptions{
			InboundUsers:    []model.InboundUser{{InboundID: 1, UserID: 1, Enabled: true}},
			RuntimeUsersOut: &pkg,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pkg == nil || len(pkg.Entries) != 1 || pkg.Entries[0].AuthUser != "alice" || pkg.Entries[0].InboundTag != "in-1" || pkg.Entries[0].RouteOutbound == "" {
		t.Fatalf("runtime user package = %+v", pkg)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OBoard == nil || parsed.OBoard.RuntimeUsers == nil || len(parsed.OBoard.RuntimeUsers.Inbounds) != 1 || parsed.OBoard.RuntimeUsers.Inbounds[0] != "in-1" {
		t.Fatalf("runtime_users declaration missing: %#v", parsed.OBoard)
	}
	if users, ok := parsed.Inbounds[0]["users"].([]any); !ok || len(users) != 0 {
		t.Fatalf("capable vless users not stripped: %#v", parsed.Inbounds[0])
	}
	foundSelector := false
	for _, outbound := range parsed.Outbounds {
		if outbound["type"] == "user-selector" && outbound["tag"] == "userselector-in-1" {
			foundSelector = true
		}
	}
	if !foundSelector {
		t.Fatalf("user-selector outbound missing: %#v", parsed.Outbounds)
	}
}

func TestGenerateServerConfigKeepsUsersWithoutRuntimeCaps(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"},
	}
	config, err := generateFixtureConfig(
		model.Server{ID: 1, Name: "legacy", AgentID: "agent-1"},
		[]model.Inbound{{ID: 1, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		nil,
		nil,
		users,
		ConfigOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 1, Enabled: true}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OBoard != nil && parsed.OBoard.RuntimeUsers != nil {
		t.Fatalf("legacy server declared runtime_users: %#v", parsed.OBoard.RuntimeUsers)
	}
	rawUsers, ok := parsed.Inbounds[0]["users"].([]any)
	if !ok || len(rawUsers) != 1 || rawUsers[0].(map[string]any)["name"] != "alice" {
		t.Fatalf("legacy users rewritten: %#v", parsed.Inbounds[0])
	}
}

func TestGenerateServerConfigRewritesCapableShadowsocksUsers(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a-ss2022-key!!"},
	}
	var pkg *RuntimeUserPackage
	config, err := generateFixtureConfig(
		capableRuntimeUserServer(model.AgentCapabilityRuntimeUsersShadowsocks),
		[]model.Inbound{{ID: 4, ServerID: 1, Name: "ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: `{}`, Enabled: true}},
		nil,
		nil,
		users,
		ConfigOptions{
			InboundUsers:    []model.InboundUser{{InboundID: 4, UserID: 1, Enabled: true}},
			RuntimeUsersOut: &pkg,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pkg == nil || len(pkg.Entries) != 1 || pkg.Entries[0].InboundTag != "in-4" {
		t.Fatalf("ss package = %+v", pkg)
	}
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Inbounds[0]["users"]; ok {
		t.Fatalf("capable ss users not stripped: %#v", parsed.Inbounds[0])
	}
	if managed, _ := parsed.Inbounds[0]["managed"].(bool); !managed {
		t.Fatalf("capable ss inbound not marked managed: %#v", parsed.Inbounds[0])
	}
}

func TestUsersDigestMatchesSortedCanonicalForm(t *testing.T) {
	entries := []model.UsersInstallEntry{
		{InboundTag: "in-b", AuthUser: "bob", AuthorizationKey: "k-b", Credential: model.UsersCredential{UUID: "22222222-2222-2222-2222-222222222222"}, RouteOutbound: "direct"},
		{InboundTag: "in-a", AuthUser: "alice", AuthorizationKey: "k-a", Credential: model.UsersCredential{UUID: "11111111-1111-1111-1111-111111111111"}, RouteOutbound: "direct"},
	}
	first, err := UsersDigest(3, []string{"in-b", "in-a"}, entries)
	if err != nil || first == "" {
		t.Fatalf("digest=%q err=%v", first, err)
	}
	second, err := UsersDigest(3, []string{"in-a", "in-b"}, []model.UsersInstallEntry{entries[1], entries[0]})
	if err != nil || second != first {
		t.Fatalf("order changed digest: %q vs %q", first, second)
	}
	other, err := UsersDigest(4, []string{"in-a", "in-b"}, []model.UsersInstallEntry{entries[1], entries[0]})
	if err != nil || other == first {
		t.Fatal("revision is not part of the digest")
	}
}

func BenchmarkUsersDigest(b *testing.B) {
	entries := make([]model.UsersInstallEntry, 128)
	scope := make([]string, 8)
	for i := range scope {
		scope[i] = "in-" + string(rune('a'+i))
	}
	for i := range entries {
		entries[i] = model.UsersInstallEntry{
			InboundTag: scope[i%len(scope)], AuthUser: "user-" + string(rune('a'+i%26)),
			AuthorizationKey: "key", Credential: model.UsersCredential{UUID: "11111111-1111-4111-8111-111111111111"},
			RouteOutbound: "direct",
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := UsersDigest(1, scope, entries); err != nil {
			b.Fatal(err)
		}
	}
}
