package core

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestListenAddressOverlapIsSymmetricForWildcards(t *testing.T) {
	cases := [][2]string{
		{"0.0.0.0", "192.0.2.10"},
		{"::", "2001:db8::10"},
		{"::", "192.0.2.10"},
		{"0.0.0.0", "::"},
	}
	for _, pair := range cases {
		if !listenAddressesOverlap(pair[0], pair[1]) || !listenAddressesOverlap(pair[1], pair[0]) {
			t.Fatalf("wildcard overlap is not symmetric for %q and %q", pair[0], pair[1])
		}
	}
	if listenAddressesOverlap("192.0.2.10", "192.0.2.11") {
		t.Fatal("distinct specific addresses should not overlap")
	}
}

func TestValidatePortForwardsHandlesTransportAndWildcardOverlap(t *testing.T) {
	servers := []model.Server{{ID: 1}, {ID: 2}}
	base := model.PortForward{ID: 1, Name: "tcp", SourceServerID: 1, TargetServerID: 2, ListenIP: "0.0.0.0", ListenPort: 443, TargetPort: 8443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendBuiltin, Enabled: true}
	udp := base
	udp.ID = 2
	udp.Name = "udp"
	udp.ListenIP = "192.0.2.10"
	udp.Protocol = model.ForwardProtocolUDP
	if err := ValidatePortForwards(servers, []model.PortForward{base, udp}); err != nil {
		t.Fatalf("TCP and UDP should be allowed on the same port: %v", err)
	}
	udp.Protocol = model.ForwardProtocolTCPUDP
	if err := ValidatePortForwards(servers, []model.PortForward{base, udp}); err == nil {
		t.Fatal("tcp_udp should conflict with an overlapping TCP wildcard listener")
	}
}

func TestValidateDeploymentListenResourcesChecksCrossComponentOwners(t *testing.T) {
	config := `{"inbounds":[{"type":"vless","tag":"public","listen":"0.0.0.0","listen_port":443}]}`
	udpPlan := model.PortForwardPlan{Rules: []model.PortForward{{ID: 1, Name: "udp-only", SourceServerID: 1, ListenIP: "192.0.2.10", ListenPort: 443, Protocol: model.ForwardProtocolUDP, Enabled: true}}}
	if err := ValidateDeploymentListenResources(1, config, udpPlan, model.TunnelPlan{}, model.SSHInboundPlan{}); err != nil {
		t.Fatalf("cross-component TCP and UDP listeners should coexist: %v", err)
	}
	tcpPlan := udpPlan
	tcpPlan.Rules[0].Protocol = model.ForwardProtocolTCP
	err := ValidateDeploymentListenResources(1, config, tcpPlan, model.TunnelPlan{}, model.SSHInboundPlan{})
	if err == nil || !strings.Contains(err.Error(), "core inbound public") || !strings.Contains(err.Error(), "port forward") {
		t.Fatalf("cross-component conflict did not identify both owners: %v", err)
	}

	wg := model.TunnelPlan{Tunnels: []model.Tunnel{{ID: 7, Name: "wg", Type: model.TunnelTypeWireGuard, ListenPort: 51820, Enabled: true}}}
	ssConfig := `{"inbounds":[{"type":"shadowsocks","tag":"ss","listen":"::","listen_port":51820}]}`
	if err := ValidateDeploymentListenResources(1, ssConfig, model.PortForwardPlan{}, wg, model.SSHInboundPlan{}); err == nil {
		t.Fatal("WireGuard UDP should conflict with a wildcard Shadowsocks UDP listener")
	}
}
