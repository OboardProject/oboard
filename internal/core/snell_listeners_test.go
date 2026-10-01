package core

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func snellTestServer() model.Server {
	return model.Server{ID: 1, AgentID: "snell-agent", Name: "edge", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 40000, PortRangeEnd: 40100, KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, "authorization_lease_v1", "runtime_users_snell_psk_v1", "runtime_users_snell_psk_control_v1", "snell_multi_psk_v4_v1", "snell_multi_psk_v6_v1"}}
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

// Rendering must never allocate: a subscription pull is read-only, and a port
// invented there would never be persisted or listened on.
func TestSnellSubscriptionRenderingNeverAllocates(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	ledger := NewProxyPathPortLedger(nil)
	if _, err := buildFixtureSubscriptionNodes(snellTestUsers(1)[0], []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{
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
	if _, err := generateFixtureConfig(server, []model.Inbound{inbound}, nil, testDNSState(server.ID), []model.User{user}, ConfigOptions{Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, PortLedger: ledger}); err != nil {
		t.Fatal(err)
	}

	nodes, err := buildFixtureSubscriptionNodes(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{
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
