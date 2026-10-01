package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"strings"
	"testing"
)

func sharedSnellFixture() (model.Server, model.Inbound) {
	s := snellTestServer()
	s.AgentID = "enrolled"
	s.KernelCapabilities = []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, "authorization_lease_v1", "runtime_users_snell_psk_v1", "runtime_users_snell_psk_control_v1", "snell_multi_psk_v4_v1", "snell_multi_psk_v6_v1"}
	s.UsersConfirmed = true
	s.AuthorizationConfirmed = true
	in := snellTestInbound()
	in.ConfigJSON = `{"version":4,"psk":"unused-legacy-seed","listener_mode":"shared_port"}`
	return s, in
}
func TestSnellListenerModeCapabilities(t *testing.T) {
	_, in := sharedSnellFixture()
	old := snellTestInbound()
	if SnellListenerMode(old) != SnellListenerShared || !AuthCapabilities(old).MultiUser || !AuthCapabilities(old).RoutingAuthUser {
		t.Fatal("Snell must use the shared listener")
	}
	old.ConfigJSON = `{"version":4,"listener_mode":"per_identity_port"}`
	if ValidateSnellListenerMode(old) == nil {
		t.Fatal("retired independent port mode accepted")
	}
	in.ConfigJSON = `{"version":6,"listener_mode":"shared_port","mode":"unsafe-raw"}`
	if !errors.Is(ValidateSnellListenerMode(in), ErrSnellUnsafeMode) {
		t.Fatal("unsafe raw accepted")
	}
}
func TestSnellSharedConfigUsesPSKRuntimeLaneAndNoPerIdentityPorts(t *testing.T) {
	for _, n := range []int{0, 1, 3, 64, 65} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, in := sharedSnellFixture()
			users := snellTestUsers(n)
			for i := range users {
				users[i].Username = fmt.Sprintf("user-%03d", i)
			}
			ledger := NewProxyPathPortLedger(nil)
			var pkg *RuntimeUserPackage
			config, err := generateFixtureConfig(s, []model.Inbound{in}, nil, testDNSState(1), users, ConfigOptions{Servers: []model.Server{s}, Inbounds: []model.Inbound{in}, PortLedger: ledger, RuntimeUsersOut: &pkg})
			if n > 64 {
				if !errors.Is(err, ErrSnellCredentialLimit) {
					t.Fatalf("capacity: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			listeners := snellListenersFromConfig(t, config)
			item := listeners["in-2"]
			if len(listeners) != 1 || item["listen_port"] != float64(in.Port) || item["auth_mode"] != "multi_psk" {
				t.Fatalf("shared listener: %v", listeners)
			}
			if _, exists := item["psk"]; exists {
				t.Fatal("global fallback exposed")
			}
			if strings.Contains(config, "userkey") {
				t.Fatal("legacy credential leaked")
			}
			if len(ledger.owners) != 0 {
				t.Fatal("shared listener allocated identity ports")
			}
			if pkg == nil || len(pkg.Entries) != n {
				t.Fatalf("runtime entries: %+v", pkg)
			}
			for i, entry := range pkg.Entries {
				if entry.Credential.PSK == "" || entry.Credential.UserKey != "" || entry.AuthorizationKey == "" || entry.RouteOutbound == "" {
					t.Fatalf("entry %d missing PSK/route/grant", i)
				}
			}
			if list, ok := item["users"].([]any); !ok || len(list) != 0 {
				t.Fatal("runtime rewrite lost empty multi_psk table")
			}
		})
	}
}
func TestSnellSharedActivationRequiresEveryCapability(t *testing.T) {
	s, in := sharedSnellFixture()
	for i := range s.KernelCapabilities {
		copy := s
		copy.KernelCapabilities = append(append([]string(nil), s.KernelCapabilities[:i]...), s.KernelCapabilities[i+1:]...)
		if s.KernelCapabilities[i] == "snell_multi_psk_v6_v1" {
			continue
		}
		if ServerSupportsSnellShared(copy, in) {
			t.Fatalf("missing %s accepted", s.KernelCapabilities[i])
		}
	}
}
func TestSnellSharedSubscriptionsUseDesiredEndpointAndOwnPSK(t *testing.T) {
	s, in := sharedSnellFixture()
	users := fixtureCredentials(snellTestUsers(2), []model.Inbound{in}, nil)
	for _, u := range users {
		identity := UserCredentialForRoute(u, in.ID, 0, in.Protocol)
		if _, ok, err := SnellSubscriptionNode(nil, identity, in, s, 0); err != nil || !ok {
			t.Fatalf("desired endpoint missing before deployment: %v", err)
		}
	}
	in.SnellActiveMode = SnellListenerShared
	in.SnellActivePort = in.Port
	in.SnellActiveVersion = 4
	for _, u := range users {
		identity := UserCredentialForRoute(u, in.ID, 0, in.Protocol)
		node, ok, err := SnellSubscriptionNode(nil, identity, in, s, 0)
		if err != nil || !ok {
			t.Fatalf("confirmed node missing: %v", err)
		}
		if node["psk"] != identity.ProxyPassword || node["server_port"] != in.Port {
			t.Fatalf("wrong endpoint/credential: %v", node)
		}
		raw, _ := json.Marshal(node)
		for _, forbidden := range []string{"userkey", "auth_mode", "unused-legacy-seed", "users"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("server-only field %s leaked", forbidden)
			}
		}
	}
	in.Port++
	if ports := SnellRuntimeProbePorts(in, false); len(ports) != 1 || ports[0] != in.SnellActivePort {
		t.Fatalf("probe guessed desired port: %v", ports)
	}
}
func TestSnellSharedDuplicatePSKRejectedAtomically(t *testing.T) {
	_, in := sharedSnellFixture()
	users := []model.User{{ID: 1, AuthorizationKey: "a", ProxyPassword: "duplicate-independent-key"}, {ID: 2, AuthorizationKey: "b", ProxyPassword: "duplicate-independent-key"}}
	if _, err := snellSharedInbound(in, users); !errors.Is(err, ErrSnellDuplicatePSK) {
		t.Fatal(err)
	}
}

func TestSnellSharedPSKRotationIsScopedToOneIdentity(t *testing.T) {
	server, inbound := sharedSnellFixture()
	users := fixtureCredentials(snellTestUsers(2), []model.Inbound{inbound}, nil)
	build := func() *RuntimeUserPackage {
		t.Helper()
		var pkg *RuntimeUserPackage
		ledger := NewProxyPathPortLedger(nil)
		_, err := generateFixtureConfig(server, []model.Inbound{inbound}, nil, testDNSState(1), users, ConfigOptions{Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound}, PortLedger: ledger, RuntimeUsersOut: &pkg})
		if err != nil {
			t.Fatal(err)
		}
		if len(ledger.Pending()) != 0 {
			t.Fatal("shared PSK changes allocated identity ports")
		}
		return pkg
	}
	before := build()
	users[0].ProxyCredentials[0].Password = "rotated-independent-psk"
	users[0].ProxyCredentials[0].ID = "rotated-credential-key"
	after := build()
	if len(before.Entries) != 2 || len(after.Entries) != 2 {
		t.Fatal("rotation lost an identity")
	}
	if before.Entries[0].Credential.PSK == after.Entries[0].Credential.PSK || after.Entries[0].AuthorizationKey != "rotated-credential-key" || before.Entries[1] != after.Entries[1] {
		t.Fatal("rotation changed the wrong identity")
	}
}

func TestSnellSharedAdvertisePortRoutesMultipleUsersAndBranches(t *testing.T) {
	for _, version := range []int{4, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			server, inbound := sharedSnellFixture()
			inbound.AdvertisePort = 20803
			inbound.ConfigJSON = fmt.Sprintf(`{"version":%d,"psk":"unused-legacy-seed","listener_mode":"shared_port"}`, version)
			exitB := model.Server{ID: 2, Name: "exit-b", PublicIPv4: "203.0.113.20", PortRangeStart: 41000, PortRangeEnd: 41100}
			exitC := model.Server{ID: 3, Name: "exit-c", PublicIPv4: "203.0.113.30", PortRangeStart: 42000, PortRangeEnd: 42100}
			paths := []model.ProxyPath{
				{ID: 50, Name: "branch-a", InboundID: inbound.ID, Secret: "secret-a", Enabled: true},
				{ID: 51, Name: "branch-b", InboundID: inbound.ID, Secret: "secret-b", Enabled: true},
			}
			steps := []model.ProxyPathStep{
				{ID: 101, PathID: 50, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &exitB.ID},
				{ID: 102, PathID: 51, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &exitC.ID},
			}
			users := fixtureCredentials(snellTestUsers(2), []model.Inbound{inbound}, paths)
			var pkg *RuntimeUserPackage
			ledger := NewProxyPathPortLedger(nil)
			opts := ConfigOptions{Servers: []model.Server{server, exitB, exitC}, Inbounds: []model.Inbound{inbound}, ProxyPaths: paths, ProxyPathSteps: steps, PortLedger: ledger, RuntimeUsersOut: &pkg}
			config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), users, opts)
			if err != nil {
				t.Fatal(err)
			}
			listeners := snellListenersFromConfig(t, config)
			if len(listeners) != 1 || listeners["in-2"]["listen_port"] != float64(inbound.Port) {
				t.Fatalf("want one shared listener: %v", listeners)
			}
			if pkg == nil || len(pkg.Entries) != 4 {
				t.Fatalf("want four user/branch identities: %+v", pkg)
			}
			entries := map[string]model.UsersInstallEntry{}
			for _, entry := range pkg.Entries {
				wantRoute := fmt.Sprintf("path-%d-step-1", entry.Identity.PathID)
				if entry.InboundTag != "in-2" || entry.RouteOutbound != wantRoute || len(findOutbound(config, wantRoute)) == 0 {
					t.Fatalf("wrong branch route: %+v", entry)
				}
				if _, exists := entries[entry.Credential.PSK]; exists {
					t.Fatal("users or branches share a PSK")
				}
				entries[entry.Credential.PSK] = entry
			}
			inbound.SnellActiveMode, inbound.SnellActivePort, inbound.SnellActiveVersion = SnellListenerShared, inbound.Port, version
			for _, user := range users {
				for _, path := range paths {
					identity := UserCredentialForRoute(user, inbound.ID, path.ID, inbound.Protocol)
					node, ok, err := SnellSubscriptionNode(ledger, identity, inbound, server, path.ID)
					if err != nil || !ok || node["server_port"] != 20803 {
						t.Fatalf("shared public endpoint = %v, ok = %v, err = %v", node, ok, err)
					}
					entry := entries[node["psk"].(string)]
					if entry.Identity.UserID != user.ID || entry.Identity.PathID != path.ID {
						t.Fatalf("subscription authenticates wrong user/branch: %+v", entry.Identity)
					}
				}
			}
		})
	}
}

func TestSnellSharedUsesGeneratedHopInsteadOfTargetPublicInbound(t *testing.T) {
	source, root := sharedSnellFixture()
	target := snellTestServer()
	target.ID, target.Name, target.PublicIPv4 = 2, "target", "203.0.113.20"
	public := snellTestInbound()
	public.ID, public.ServerID, public.Port = 20, target.ID, 8443
	path := model.ProxyPath{ID: 50, Name: "snell-chain", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	step := model.ProxyPathStep{ID: 101, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &target.ID}
	users := fixtureCredentials(snellTestUsers(1), []model.Inbound{root}, []model.ProxyPath{path})
	opts := ConfigOptions{Servers: []model.Server{source, target}, Inbounds: []model.Inbound{root, public}, ProxyPaths: []model.ProxyPath{path}, ProxyPathSteps: []model.ProxyPathStep{step}}
	if _, err := BuildProxyPathPlans(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds); err != nil {
		t.Fatal(err)
	}
	config, err := GenerateServerConfigWithOptions(source, opts.Inbounds, nil, testDNSState(1), users, opts)
	if err != nil {
		t.Fatal(err)
	}
	if outbound := findOutbound(config, "path-50-step-1"); outbound["type"] != "shadowsocks" || intFromAny(outbound["server_port"]) == public.Port {
		t.Fatalf("public shared Snell was used as an internal hop: %v", outbound)
	}
	step.InboundID = &public.ID
	if _, err = BuildProxyPathPlans(opts.ProxyPaths, []model.ProxyPathStep{step}, opts.Servers, opts.Inbounds); err == nil {
		t.Fatal("public shared Snell was accepted as an explicit internal hop")
	}
}
