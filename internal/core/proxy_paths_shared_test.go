package core

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

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
	sharedTag := proxyPathChainServiceTag(DefaultProxyPathChainMethod)
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
	services, err := buildProxyPathChainServices(opts.ProxyPaths, steps, opts.Servers, opts.Inbounds)
	if err != nil {
		t.Fatal(err)
	}
	service128 := services[proxyPathChainServiceKey{ServerID: serverB.ID, Method: DefaultProxyPathChainMethod}]
	service256 := services[proxyPathChainServiceKey{ServerID: serverB.ID, Method: "2022-blake3-aes-256-gcm"}]
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

	services, err := buildProxyPathChainServices(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
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
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100, SSHPort: 22}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	bID := serverB.ID

	t.Run("ssh", func(t *testing.T) {
		paths := []model.ProxyPath{{ID: 10, Name: "ssh-one", InboundID: root.ID, Enabled: true}, {ID: 20, Name: "ssh-two", InboundID: root.ID, Enabled: true}}
		steps := []model.ProxyPathStep{
			{ID: 11, PathID: paths[0].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh"}`},
			{ID: 21, PathID: paths[1].ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh"}`},
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
