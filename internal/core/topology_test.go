package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestValidateTopologyDAGRejectsTunnelCycle(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}, {ID: 3, Name: "c"}}
	forwards := []model.PortForward{{ID: 1, Name: "a-b", SourceServerID: 1, TargetServerID: 2, ListenPort: 443, TargetPort: 443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendRealm, Enabled: true}, {ID: 2, Name: "b-c", SourceServerID: 2, TargetServerID: 3, ListenPort: 8443, TargetPort: 443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendRealm, Enabled: true}}
	tunnels := []model.Tunnel{{ID: 3, Name: "c-a", SourceServerID: 3, TargetServerID: 1, Type: model.TunnelTypeWireGuard, Enabled: true}}
	if err := ValidateTopologyDAG(servers, forwards, tunnels); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected tunnel to participate in cycle detection, got %v", err)
	}
}

func TestValidateTopologyDAGIgnoresExternalPortForwardTargets(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "source"}}
	forwards := []model.PortForward{{ID: 1, Name: "external", SourceServerID: 1, TargetAddress: "203.0.113.80", ListenPort: 443, TargetPort: 8443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendRealm, Enabled: true}}
	if err := ValidateTopologyDAG(servers, forwards, nil); err != nil {
		t.Fatalf("external target must not become a managed topology edge: %v", err)
	}
}

func TestValidateTunnelConfigRejectsUnsafeSSHExtraArgs(t *testing.T) {
	err := ValidateTunnelConfig(model.Tunnel{Type: model.TunnelTypeSSH, ConfigJSON: `{"user":"root","extra_args":["-o","ProxyCommand=sh -c id"]}`})
	if err == nil || !strings.Contains(err.Error(), "extra_args") {
		t.Fatalf("expected extra_args validation error, got %v", err)
	}
}

func TestValidateTunnelConfigRejectsBadWireGuardCIDR(t *testing.T) {
	err := ValidateTunnelConfig(model.Tunnel{Type: model.TunnelTypeWireGuard, ConfigJSON: `{"private_key":"k","peer_public_key":"p","allowed_ips":["not-cidr"]}`})
	if err == nil || !strings.Contains(err.Error(), "not an IP/CIDR") {
		t.Fatalf("expected bad CIDR validation error, got %v", err)
	}
}

func FuzzValidateTunnelConfig(f *testing.F) {
	f.Add("ssh", `{"user":"oboard","port":22}`)
	f.Add("wireguard", `{"private_key":"k","peer_public_key":"p","allowed_ips":["10.0.0.0/24"]}`)
	f.Fuzz(func(t *testing.T, tunnelType, configJSON string) {
		if len(configJSON) > 1<<16 {
			t.Skip()
		}
		_ = ValidateTunnelConfig(model.Tunnel{Type: model.TunnelType(tunnelType), ConfigJSON: configJSON})
	})
}

func TestValidateTunnelEndpoint(t *testing.T) {
	for _, good := range []string{"203.0.113.10", "example.com", "node-1.example", "2001:db8::1", "[2001:db8::1]"} {
		if err := ValidateTunnelEndpoint(good); err != nil {
			t.Fatalf("good endpoint %q rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"-oProxyCommand=evil", "user@host", "https://evil", "host/path", "host port", "../x"} {
		if err := ValidateTunnelEndpoint(bad); err == nil {
			t.Fatalf("bad endpoint %q accepted", bad)
		}
	}
}

func TestBuildTunnelPlanSelectsCompatibleAddressAndRejectsExplicitMismatch(t *testing.T) {
	servers := []model.Server{
		{ID: 1, Name: "source", PublicIPv6: "2001:db8::1", IPStack: model.IPStackIPv6Only},
		{ID: 2, Name: "target", PublicIPv4: "203.0.113.2", PublicIPv6: "2001:db8::2", EntryIPMode: model.EntryIPModeAuto},
	}
	tunnel := model.Tunnel{ID: 1, Name: "auto-address", SourceServerID: 1, TargetServerID: 2, Type: model.TunnelTypeSSH, TargetPort: 22, Enabled: true}
	plan, err := BuildTunnelPlan(10, servers[0], servers, []model.Tunnel{tunnel})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tunnels) != 1 || plan.Tunnels[0].TargetEndpoint != servers[1].PublicIPv6 {
		t.Fatalf("tunnel plan = %#v", plan)
	}
	tunnel.TargetEndpoint = servers[1].PublicIPv4
	if _, err := BuildTunnelPlan(11, servers[0], servers, []model.Tunnel{tunnel}); err == nil || !errors.Is(err, ErrInvalidDesiredState) || !strings.Contains(err.Error(), "IPv4") {
		t.Fatalf("incompatible tunnel error = %v", err)
	}
}
