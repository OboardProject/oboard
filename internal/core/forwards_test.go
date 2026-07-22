package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestBuildPortForwardPlanResolvesAndSortsRules(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "a", PublicIPv4: "198.51.100.1"}, {ID: 2, Name: "b", PublicIPv4: "203.0.113.2"}}
	forwards := []model.PortForward{
		{ID: 2, Name: "late", SourceServerID: 1, TargetServerID: 2, ListenPort: 2000, TargetPort: 2000, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendRealm, Priority: 200, Enabled: true},
		{ID: 1, Name: "early", SourceServerID: 1, TargetServerID: 2, ListenPort: 1000, TargetPort: 1000, Protocol: model.ForwardProtocolUDP, Backend: model.ForwardBackendNFT, Priority: 10, Enabled: true},
	}
	plan, err := BuildPortForwardPlan(7, servers[0], servers, forwards)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != 7 || len(plan.Rules) != 2 {
		t.Fatalf("bad plan: %#v", plan)
	}
	if plan.Rules[0].Name != "early" || plan.Rules[1].Name != "late" {
		t.Fatalf("rules not sorted by priority: %#v", plan.Rules)
	}
	if plan.Rules[0].TargetAddress != "203.0.113.2" {
		t.Fatalf("target address not resolved: %#v", plan.Rules[0])
	}
}

func TestBuildPortForwardPlanUsesDetectedAddressForSourceIPStack(t *testing.T) {
	servers := []model.Server{
		{ID: 1, Name: "source", PublicIPv4: "198.51.100.1", IPStack: model.IPStackIPv4Only},
		{ID: 2, Name: "target", PublicIPv4: "203.0.113.2", PublicIPv6: "2001:db8::2", ListenIP: "0.0.0.0"},
	}
	plan, err := BuildPortForwardPlan(8, servers[0], servers, []model.PortForward{{ID: 1, Name: "auto-address", SourceServerID: 1, TargetServerID: 2, ListenPort: 443, TargetPort: 8443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendBuiltin, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rules) != 1 || plan.Rules[0].TargetAddress != servers[1].PublicIPv4 {
		t.Fatalf("plan = %#v, want detected IPv4 target", plan)
	}
}

func TestValidatePortForwardsDetectsDuplicateAndMixedCycle(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	forwards := []model.PortForward{
		{ID: 1, Name: "one", SourceServerID: 1, TargetServerID: 2, ListenPort: 443, TargetPort: 8443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendAuto, Enabled: true},
		{ID: 2, Name: "dup", SourceServerID: 1, TargetServerID: 2, ListenPort: 443, TargetPort: 9443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendAuto, Enabled: true},
	}
	if err := ValidatePortForwards(servers, forwards); err == nil {
		t.Fatal("expected duplicate listen tuple to fail")
	}

	forwards = []model.PortForward{{ID: 3, Name: "back", SourceServerID: 2, TargetServerID: 1, ListenPort: 443, TargetPort: 8443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendAuto, Enabled: true}}
	tunnels := []model.Tunnel{{ID: 4, Name: "out", SourceServerID: 1, TargetServerID: 2, Type: model.TunnelTypeWireGuard, Enabled: true}}
	if err := ValidateTopologyDAG(servers, forwards, tunnels); err == nil {
		t.Fatal("expected mixed tunnel/forward cycle to fail")
	}
}

func TestValidateForwardBackendAndProbeModes(t *testing.T) {
	if err := ValidateForwardBackend(model.ForwardBackendBuiltin); err != nil {
		t.Fatalf("builtin backend should be accepted: %v", err)
	}
	if err := ValidateForwardProbeMode("periodic_sampled"); err != nil {
		t.Fatalf("periodic_sampled should be accepted: %v", err)
	}
	if err := ValidateForwardProbeMode("aggressive"); err == nil {
		t.Fatal("invalid probe mode should fail")
	}
}
