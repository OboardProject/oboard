package core

import (
	"encoding/json"
	"strings"
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

// A proxy-path chain service authenticates the previous hop with an internal
// identity that never gets an authorization key, so the runtime lane cannot
// install it. The inbound must therefore keep its static users: stripping it
// into an empty user-selector made the kernel reject the whole snapshot and
// refuse every chained connection.
func TestGenerateServerConfigKeepsChainServiceInboundStatic(t *testing.T) {
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := capableRuntimeUserServer(model.AgentCapabilityRuntimeUsersShadowsocks, model.AgentCapabilityRuntimeUsersVLESS)
	serverB.ID, serverB.Name, serverB.ChainSecret = 2, "B", "chain-b"
	serverB.PublicIPv4, serverB.ListenIP, serverB.IPStack = "203.0.113.2", "0.0.0.0", model.IPStackIPv4Only
	serverB.PortRangeStart, serverB.PortRangeEnd = 31000, 31100
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 40, Name: "chain", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	bID := serverB.ID
	step := model.ProxyPathStep{ID: 41, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{}`}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "alice-password"}
	var pkg *RuntimeUserPackage
	config, err := generateFixtureConfig(serverB, nil, nil, nil, []model.User{user}, ConfigOptions{
		Servers:         []model.Server{serverA, serverB},
		Inbounds:        []model.Inbound{root},
		ProxyPaths:      []model.ProxyPath{path},
		ProxyPathSteps:  []model.ProxyPathStep{step},
		InboundUsers:    []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
		ProxyPathUsers:  []model.ProxyPathUser{{ProxyPathID: path.ID, InboundID: root.ID, UserID: user.ID, Enabled: true}},
		RuntimeUsersOut: &pkg,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseSingBoxConfig(t, config)
	chain := map[string]any{}
	for _, inbound := range parsed.Inbounds {
		if strings.HasPrefix(stringValue(inbound, "tag", ""), "oboard-chain-") {
			chain = inbound
		}
	}
	if len(chain) == 0 {
		t.Fatalf("no chain service inbound generated: %s", config)
	}
	users, ok := chain["users"].([]any)
	if !ok || len(users) == 0 {
		t.Fatalf("chain service inbound lost its static users: %#v", chain)
	}
	if managed, _ := chain["managed"].(bool); managed {
		t.Fatalf("chain service inbound was marked runtime managed: %#v", chain)
	}
	if pkg != nil {
		t.Fatalf("chain service inbound entered the runtime users lane: %+v", pkg)
	}
	if parsed.OBoard != nil && parsed.OBoard.RuntimeUsers != nil {
		t.Fatalf("chain service inbound was declared in runtime_users: %#v", parsed.OBoard.RuntimeUsers)
	}
	for _, outbound := range parsed.Outbounds {
		if outbound["type"] == "user-selector" {
			t.Fatalf("chain traffic was routed to a user-selector: %#v", outbound)
		}
	}
	rules, err := json.Marshal(parsed.Route["rules"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rules), "userselector-") {
		t.Fatalf("chain route was rewritten to a user-selector: %s", rules)
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

func TestProjectServerRuntimeUsersMatchesFullGeneration(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a", AuthorizationKey: "auth-alice"},
	}
	server := capableRuntimeUserServer(model.AgentCapabilityRuntimeUsersVLESS)
	inbounds := []model.Inbound{{ID: 1, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}}
	opts := ConfigOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 1, Enabled: true}}}

	var fromFull *RuntimeUserPackage
	fullOpts := opts
	fullOpts.RuntimeUsersOut = &fromFull
	if _, err := GenerateServerConfigWithOptions(server, inbounds, nil, testDNSState(1), users, fullOpts); err != nil {
		t.Fatal(err)
	}
	projected, err := ProjectServerRuntimeUsers(server, inbounds, nil, testDNSState(1), users, opts)
	if err != nil {
		t.Fatal(err)
	}
	if fromFull == nil {
		t.Fatal("full generation produced no runtime package")
	}
	if projected.Mode != fromFull.Mode || len(projected.Entries) != len(fromFull.Entries) || len(projected.Scope) != len(fromFull.Scope) {
		t.Fatalf("projection mismatch: full=%+v projected=%+v", fromFull, projected)
	}
	for i := range projected.Entries {
		if projected.Entries[i].AuthUser != fromFull.Entries[i].AuthUser ||
			projected.Entries[i].InboundTag != fromFull.Entries[i].InboundTag ||
			projected.Entries[i].RouteOutbound != fromFull.Entries[i].RouteOutbound ||
			projected.Entries[i].AuthorizationKey != fromFull.Entries[i].AuthorizationKey {
			t.Fatalf("entry[%d] mismatch: full=%+v projected=%+v", i, fromFull.Entries[i], projected.Entries[i])
		}
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
