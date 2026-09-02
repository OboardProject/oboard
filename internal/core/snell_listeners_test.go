package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func snellTestServer() model.Server {
	return model.Server{ID: 1, Name: "edge", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 40000, PortRangeEnd: 40100}
}

func snellTestInbound() model.Inbound {
	return model.Inbound{ID: 2, ServerID: 1, Name: "snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, ConfigJSON: `{"version":4,"psk":"inbound-seed-psk-1234"}`, Enabled: true}
}

func snellTestUsers(n int) []model.User {
	out := make([]model.User, 0, n)
	for i := 1; i <= n; i++ {
		letter := string(rune('a' + i - 1))
		out = append(out, model.User{
			ID:            int64(i),
			Username:      letter + "-user",
			Status:        "active",
			ProxyUUID:     fmt.Sprintf("1111111%d-1111-4111-8111-111111111111", i),
			ProxyPassword: letter + "-proxy-password",
		})
	}
	return out
}

// snellListenersFromConfig collects the generated Snell listeners keyed by tag.
func snellListenersFromConfig(t *testing.T, config string) map[string]map[string]any {
	t.Helper()
	var parsed SingBoxConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, inbound := range parsed.Inbounds {
		if inbound["type"] == "snell" {
			out[stringFromAny(inbound["tag"])] = inbound
		}
	}
	return out
}

// A Snell inbound must never render one shared multi-user listener: sing-box
// would then authenticate by userkey, which no client other than sing-box can
// present, and every Surge/Mihomo/Egern user would be rejected.
func TestSnellGeneratesPerUserSingleUserListeners(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	users := snellTestUsers(3)
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), users, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound},
		PortLedger: NewProxyPathPortLedger(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	listeners := snellListenersFromConfig(t, config)
	if len(listeners) != len(users) {
		t.Fatalf("want one listener per user, got %d: %v", len(listeners), listeners)
	}
	ports := map[float64]bool{}
	psks := map[string]bool{}
	for _, user := range users {
		tag := snellUserInboundTag(inbound.ID, user.ID, 0)
		item, ok := listeners[tag]
		if !ok {
			t.Fatalf("listener %q missing: %v", tag, listeners)
		}
		if _, hasUsers := item["users"]; hasUsers {
			t.Fatalf("listener %q must not carry a users table: %#v", tag, item)
		}
		psk := stringFromAny(item["psk"])
		if psk == "" || psk == "inbound-seed-psk-1234" {
			t.Fatalf("listener %q must use a derived psk, got %q", tag, psk)
		}
		port := item["listen_port"].(float64)
		if port < 40000 || port > 40100 {
			t.Fatalf("listener %q port %v outside the server auto range", tag, port)
		}
		if ports[port] {
			t.Fatalf("listener %q reuses port %v", tag, port)
		}
		if psks[psk] {
			t.Fatalf("listener %q reuses psk of another user", tag)
		}
		ports[port] = true
		psks[psk] = true
	}
}

// Each user's listener carries that user's own runtime limit, keyed by its own
// inbound tag. This is what replaces the per-user accounting the multi-user
// users table used to provide.
func TestSnellPerUserListenersCarryPerUserRuntimeLimits(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	users := snellTestUsers(2)
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), users, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound},
		PortLedger: NewProxyPathPortLedger(nil),
		UserPolicies: map[int64]UserLimitPolicy{
			users[0].ID: {SpeedLimitMbps: 20},
			users[1].ID: {SpeedLimitMbps: 50},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseSingBoxConfig(t, config)
	if parsed.OBoard == nil {
		t.Fatalf("missing runtime metadata: %s", config)
	}
	for _, tc := range []struct {
		user  model.User
		speed int
	}{{users[0], 20}, {users[1], 50}} {
		tag := snellUserInboundTag(inbound.ID, tc.user.ID, 0)
		limit, ok := parsed.OBoard.RateLimits.Inbounds[tag]
		if !ok {
			t.Fatalf("no runtime limit for %q: %#v", tag, parsed.OBoard.RateLimits.Inbounds)
		}
		if limit.UserID != tc.user.ID || limit.SpeedLimitMbps != tc.speed {
			t.Fatalf("runtime limit for %q = %#v", tag, limit)
		}
	}
}

// Adding a user must not move the listeners of the users already deployed:
// their ports come from the ledger, and a moved port silently breaks a live
// client.
func TestSnellPerUserPortsSurviveUserChanges(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	users := snellTestUsers(2)
	ledger := NewProxyPathPortLedger(nil)
	first, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), users, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, PortLedger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := snellListenersFromConfig(t, first)

	// Replay the persisted allocations the way Controller does between
	// deployments, then add a third user.
	stored := ledger.Pending()
	grown := NewProxyPathPortLedger(stored)
	second, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), snellTestUsers(3), ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, PortLedger: grown,
	})
	if err != nil {
		t.Fatal(err)
	}
	after := snellListenersFromConfig(t, second)
	if len(after) != 3 {
		t.Fatalf("want 3 listeners after adding a user, got %d", len(after))
	}
	for tag, item := range before {
		grownItem, ok := after[tag]
		if !ok {
			t.Fatalf("listener %q disappeared after adding a user", tag)
		}
		if grownItem["listen_port"] != item["listen_port"] {
			t.Fatalf("listener %q moved from %v to %v", tag, item["listen_port"], grownItem["listen_port"])
		}
		if grownItem["psk"] != item["psk"] {
			t.Fatalf("listener %q psk rotated after an unrelated user was added", tag)
		}
	}
}

// With proxy path branches each (user, branch) pair is a distinct identity and
// therefore needs its own listener: the branch a connection belongs to is
// decided by which port it arrived on.
func TestSnellBranchesAllocateOnePortPerUserAndBranch(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	users := snellTestUsers(2)
	// Two real branches: each exits through a different downstream server, so
	// they occupy distinct positions and both stay enabled.
	exitB := model.Server{ID: 2, Name: "exit-b", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 41000, PortRangeEnd: 41100}
	exitC := model.Server{ID: 3, Name: "exit-c", PublicIPv4: "203.0.113.30", ListenIP: "0.0.0.0", PortRangeStart: 42000, PortRangeEnd: 42100}
	exitBID, exitCID := exitB.ID, exitC.ID
	pathA := model.ProxyPath{ID: 50, Name: "branch-a", InboundID: inbound.ID, Secret: "secret-a", Enabled: true}
	pathB := model.ProxyPath{ID: 51, Name: "branch-b", InboundID: inbound.ID, Secret: "secret-b", Enabled: true}
	stepA := model.ProxyPathStep{ID: 101, PathID: pathA.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &exitBID}
	stepB := model.ProxyPathStep{ID: 102, PathID: pathB.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &exitCID}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), users, ConfigOptions{
		Servers: []model.Server{server, exitB, exitC}, Inbounds: []model.Inbound{inbound},
		ProxyPaths: []model.ProxyPath{pathA, pathB}, ProxyPathSteps: []model.ProxyPathStep{stepA, stepB},
		PortLedger: NewProxyPathPortLedger(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	listeners := snellListenersFromConfig(t, config)
	if len(listeners) != len(users)*2 {
		t.Fatalf("want one listener per (user, branch), got %d: %v", len(listeners), listeners)
	}
	ports := map[float64]bool{}
	for _, user := range users {
		for _, path := range []model.ProxyPath{pathA, pathB} {
			tag := snellUserInboundTag(inbound.ID, user.ID, path.ID)
			item, ok := listeners[tag]
			if !ok {
				t.Fatalf("listener %q missing: %v", tag, listeners)
			}
			port := item["listen_port"].(float64)
			if ports[port] {
				t.Fatalf("listener %q reuses port %v", tag, port)
			}
			ports[port] = true
		}
	}
}

// Rotating one user's proxy password rotates only that user's Snell PSK.
func TestSnellPSKRotationIsScopedToOneUser(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	users := snellTestUsers(2)
	ledger := NewProxyPathPortLedger(nil)
	before := snellListenersFromConfig(t, mustSnellConfig(t, server, inbound, users, ledger))

	rotated := snellTestUsers(2)
	rotated[0].ProxyPassword = "rotated-proxy-password"
	after := snellListenersFromConfig(t, mustSnellConfig(t, server, inbound, rotated, NewProxyPathPortLedger(ledger.Pending())))

	rotatedTag := snellUserInboundTag(inbound.ID, users[0].ID, 0)
	untouchedTag := snellUserInboundTag(inbound.ID, users[1].ID, 0)
	if after[rotatedTag]["psk"] == before[rotatedTag]["psk"] {
		t.Fatal("rotating a proxy password must rotate that user's snell psk")
	}
	if after[untouchedTag]["psk"] != before[untouchedTag]["psk"] {
		t.Fatal("rotating one user's password must not touch another user's psk")
	}
	if after[rotatedTag]["listen_port"] != before[rotatedTag]["listen_port"] {
		t.Fatal("a password rotation must not move the listener port")
	}
}

func mustSnellConfig(t *testing.T, server model.Server, inbound model.Inbound, users []model.User, ledger *ProxyPathPortLedger) string {
	t.Helper()
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), users, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, PortLedger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

// Running out of ports must fail the projection loudly. Silently dropping the
// users that did not fit would leave them without a node and no explanation.
func TestSnellPortRangeExhaustionFails(t *testing.T) {
	server := snellTestServer()
	server.PortRangeStart, server.PortRangeEnd = 40000, 40001
	inbound := snellTestInbound()
	_, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), snellTestUsers(3), ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, PortLedger: NewProxyPathPortLedger(nil),
	})
	if err == nil {
		t.Fatal("exhausting the auto port range must fail config generation")
	}
	if !errors.Is(err, ErrInvalidDesiredState) {
		t.Fatalf("port exhaustion must be reported as an invalid desired state, got %v", err)
	}
}

// The generated listeners must not collide with each other, with the declared
// inbound port, or with any other listener on the server.
func TestSnellPerUserListenersHaveNoListenConflicts(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	other := model.Inbound{ID: 3, ServerID: 1, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 40005, ConfigJSON: `{}`, Enabled: true}
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound, other}, nil, testDNSState(1), snellTestUsers(5), ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound, other}, PortLedger: NewProxyPathPortLedger(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseSingBoxConfig(t, config)
	if err := validateListenResources(singBoxListenResources(server.ID, parsed.Inbounds)); err != nil {
		t.Fatalf("generated snell listeners conflict: %v\n%s", err, config)
	}
	for _, item := range parsed.Inbounds {
		if item["type"] == "snell" && item["listen_port"] == float64(other.Port) {
			t.Fatalf("snell listener took another inbound's port: %#v", item)
		}
	}
}

// The subscription must advertise exactly the port and PSK the kernel listens
// on. This is the invariant whose absence made Snell unusable.
func TestSnellSubscriptionMatchesKernelListener(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	users := snellTestUsers(2)
	ledger := NewProxyPathPortLedger(nil)
	listeners := snellListenersFromConfig(t, mustSnellConfig(t, server, inbound, users, ledger))

	// Controller replays the persisted rows for read-only rendering.
	renderLedger := NewProxyPathPortLedger(ledger.Pending())
	for _, user := range users {
		nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{
			Format:         model.SubscriptionFormatSingBox,
			EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, inbound.ID): true},
			PortLedger:     renderLedger,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 {
			t.Fatalf("user %s got %d nodes, want 1", user.Username, len(nodes))
		}
		raw := nodes[0].Raw
		if _, hasUserKey := raw["userkey"]; hasUserKey {
			t.Fatalf("snell node must not carry a userkey: %#v", raw)
		}
		listener := listeners[snellUserInboundTag(inbound.ID, user.ID, 0)]
		if float64(raw["server_port"].(int)) != listener["listen_port"].(float64) {
			t.Fatalf("user %s advertised port %v, kernel listens on %v", user.Username, raw["server_port"], listener["listen_port"])
		}
		if raw["psk"] != listener["psk"] {
			t.Fatalf("user %s advertised psk %v, kernel expects %v", user.Username, raw["psk"], listener["psk"])
		}
	}
}

// A user whose listener was never deployed has no port to advertise. Guessing
// one would hand the client a dead endpoint, so the node is omitted instead.
func TestSnellSubscriptionSkipsUndeployedListener(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	user := snellTestUsers(1)[0]
	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{
		Format:         model.SubscriptionFormatSingBox,
		EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, inbound.ID): true},
		PortLedger:     NewProxyPathPortLedger(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("a user without a deployed listener must get no snell node, got %#v", nodes)
	}
}

// Rendering must never allocate: a subscription pull is read-only, and a port
// invented there would never be persisted or listened on.
func TestSnellSubscriptionRenderingNeverAllocates(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	ledger := NewProxyPathPortLedger(nil)
	if _, err := BuildSubscriptionNodes(snellTestUsers(1)[0], []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{
		Format:         model.SubscriptionFormatSingBox,
		EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, inbound.ID): true},
		PortLedger:     ledger,
	}); err != nil {
		t.Fatal(err)
	}
	if pending := ledger.Pending(); len(pending) != 0 {
		t.Fatalf("subscription rendering allocated %d ports: %#v", len(pending), pending)
	}
}

// Snell nodes must reach every client that supports the protocol version.
// Mihomo in particular used to lose all of them to the multi-user userkey gate.
func TestSnellNodesRenderWithoutUserKeyAcrossClients(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	user := snellTestUsers(1)[0]
	ledger := NewProxyPathPortLedger(nil)
	mustSnellConfig(t, server, inbound, []model.User{user}, ledger)

	nodes, err := BuildSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{
		Format:         model.SubscriptionFormatSingBox,
		EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, inbound.ID): true},
		PortLedger:     NewProxyPathPortLedger(ledger.Pending()),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []model.SubscriptionFormat{
		model.SubscriptionFormatSingBox, model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac,
		model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket, model.SubscriptionFormatEgern,
		model.SubscriptionFormatSurfboard,
	} {
		preview, err := PreviewSubscriptionNodes(nodes, format)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if len(preview.Nodes) != 1 {
			t.Fatalf("%s dropped the snell v4 node: filtered=%#v", format, preview.FilteredNodes)
		}
		rendered, err := RenderSubscriptionNodes(preview.Nodes, format)
		if err != nil {
			t.Fatalf("%s render: %v", format, err)
		}
		if containsSubstring(rendered, "userkey") {
			t.Fatalf("%s output still carries a userkey:\n%s", format, rendered)
		}
	}
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
