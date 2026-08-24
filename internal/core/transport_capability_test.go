package core

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestValidateTransportOptionsMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		protocol   model.Protocol
		configJSON string
		side       transportSide
		wantErr    string
	}{
		{name: "vless mux", protocol: model.ProtocolVLESS, configJSON: `{"multiplex":{"enabled":true,"padding":true}}`, side: transportSideInbound},
		{name: "vless tfo", protocol: model.ProtocolVLESS, configJSON: `{"tcp_fast_open":true}`, side: transportSideInbound},
		{name: "vless quic transport rejects tfo", protocol: model.ProtocolVLESS, configJSON: `{"transport":{"type":"quic"},"tcp_fast_open":true}`, side: transportSideInbound, wantErr: "tcp_fast_open is not applicable"},
		{name: "ss mux", protocol: model.ProtocolSS, configJSON: `{"multiplex":{"enabled":true}}`, side: transportSideInbound},
		{name: "ss tfo", protocol: model.ProtocolSS, configJSON: `{"tcp_fast_open":true}`, side: transportSideInbound},
		{name: "inbound rejects client mux fields", protocol: model.ProtocolSS, configJSON: `{"multiplex":{"enabled":true,"protocol":"smux"}}`, side: transportSideInbound, wantErr: "client-side option"},
		{name: "outbound accepts mux protocol", protocol: model.ProtocolSS, configJSON: `{"multiplex":{"enabled":true,"protocol":"yamux","max_streams":8}}`, side: transportSideOutbound},
		{name: "unknown mux protocol", protocol: model.ProtocolVLESS, configJSON: `{"multiplex":{"enabled":true,"protocol":"mux.cool"}}`, side: transportSideOutbound, wantErr: "h2mux, smux or yamux"},
		{name: "brutal stays unexposed", protocol: model.ProtocolVLESS, configJSON: `{"multiplex":{"enabled":true,"brutal":{"enabled":true}}}`, side: transportSideOutbound, wantErr: "brutal"},
		{name: "fractional stream limit", protocol: model.ProtocolVLESS, configJSON: `{"multiplex":{"enabled":true,"max_streams":1.5}}`, side: transportSideOutbound, wantErr: "non-negative integer"},
		{name: "anytls rejects generic mux", protocol: model.ProtocolAnyTLS, configJSON: `{"multiplex":{"enabled":true}}`, side: transportSideInbound, wantErr: "does not support the sing-box multiplex option"},
		{name: "anytls tfo", protocol: model.ProtocolAnyTLS, configJSON: `{"tcp_fast_open":true}`, side: transportSideInbound},
		{name: "hy2 rejects generic mux", protocol: model.ProtocolHY2, configJSON: `{"multiplex":{"enabled":true}}`, side: transportSideInbound, wantErr: "QUIC"},
		{name: "hy2 rejects tfo", protocol: model.ProtocolHY2, configJSON: `{"tcp_fast_open":true}`, side: transportSideInbound, wantErr: "QUIC over UDP"},
		{name: "hy2 accepts disabled tfo", protocol: model.ProtocolHY2, configJSON: `{"tcp_fast_open":false}`, side: transportSideInbound},
		{name: "mieru tcp tfo", protocol: model.ProtocolMieru, configJSON: `{"transport":"TCP","tcp_fast_open":true}`, side: transportSideInbound},
		{name: "mieru udp rejects tfo", protocol: model.ProtocolMieru, configJSON: `{"transport":"UDP","tcp_fast_open":true}`, side: transportSideInbound, wantErr: "UDP transport"},
		{name: "mieru rejects generic mux", protocol: model.ProtocolMieru, configJSON: `{"transport":"TCP","multiplex":{"enabled":true}}`, side: transportSideInbound, wantErr: "own multiplexing level"},
		{name: "snell tfo", protocol: model.ProtocolSnell, configJSON: `{"tcp_fast_open":true}`, side: transportSideInbound},
		{name: "snell rejects generic mux", protocol: model.ProtocolSnell, configJSON: `{"multiplex":{"enabled":true}}`, side: transportSideOutbound, wantErr: "own connection reuse"},
		{name: "socks tfo", protocol: model.ProtocolSocks, configJSON: `{"tcp_fast_open":true}`, side: transportSideInbound},
		{name: "socks rejects generic mux", protocol: model.ProtocolSocks, configJSON: `{"multiplex":{"enabled":true}}`, side: transportSideOutbound, wantErr: "no connection reuse layer"},
		{name: "tfo must be boolean", protocol: model.ProtocolSS, configJSON: `{"tcp_fast_open":"true"}`, side: transportSideInbound, wantErr: "must be boolean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTransportOptions(tc.protocol, parseExtra(tc.configJSON), tc.side)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestSocksOutboundDropsMultiplexAndKeepsTCPFastOpen(t *testing.T) {
	outbound := model.Outbound{ID: 3, Protocol: model.ProtocolSocks, TargetAddress: "203.0.113.9", TargetPort: 1080, ConfigJSON: `{"tcp_fast_open":true}`}
	item, err := (socksAdapter{}).Outbound(outbound, nil)
	if err != nil {
		t.Fatal(err)
	}
	if item["tcp_fast_open"] != true {
		t.Fatalf("socks outbound tcp_fast_open = %#v", item["tcp_fast_open"])
	}
	if _, exists := item["multiplex"]; exists {
		t.Fatal("socks outbound must never carry a multiplex object")
	}
}

func TestShadowsocksRejectsUoTWithMultiplex(t *testing.T) {
	outbound := model.Outbound{
		ID: 4, Protocol: model.ProtocolSS, TargetAddress: "203.0.113.10", TargetPort: 8388,
		ConfigJSON: `{"method":"aes-128-gcm","password":"secret","udp_over_tcp":{"enabled":true},"multiplex":{"enabled":true}}`,
	}
	if err := (ssAdapter{}).ValidateOutbound(outbound); err == nil || !strings.Contains(err.Error(), "udp_over_tcp conflicts with multiplex") {
		t.Fatalf("error = %v, want the documented UoT/multiplex conflict", err)
	}
	server := model.Server{Name: "hk-1", UDPInboundMode: model.UDPInboundUoT}
	inbound := model.Inbound{ID: 5, Name: "hk-1-ss", Protocol: model.ProtocolSS, ConfigJSON: `{"multiplex":{"enabled":true}}`}
	if err := validateServerUDPForInbound(server, inbound); err == nil || !strings.Contains(err.Error(), "udp_inbound_mode=uot conflicts with multiplex") {
		t.Fatalf("error = %v, want the server-level UoT/multiplex conflict", err)
	}
}

func TestShadowsocksSubscriptionNodeCarriesMultiplexOrUoT(t *testing.T) {
	inbound := model.Inbound{ID: 6, Name: "hk-1-ss", Protocol: model.ProtocolSS, Port: 8388, ConfigJSON: `{"method":"aes-128-gcm","multiplex":{"enabled":true},"tcp_fast_open":true}`}
	user := model.User{ID: 2, Username: "u2", ProxyPassword: "secret"}
	node, err := (ssAdapter{}).SubscriptionNode(user, inbound, model.Server{EntryAddress: "203.0.113.11"})
	if err != nil {
		t.Fatal(err)
	}
	if !genericMuxEnabled(node["multiplex"]) {
		t.Fatalf("subscription node lost multiplex: %#v", node)
	}
	if node["tcp_fast_open"] != true {
		t.Fatalf("subscription node tcp_fast_open = %#v", node["tcp_fast_open"])
	}
	uotNode, err := (ssAdapter{}).SubscriptionNode(user, inbound, model.Server{EntryAddress: "203.0.113.11", UDPInboundMode: model.UDPInboundUoT})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := uotNode["multiplex"]; exists {
		t.Fatal("a udp_over_tcp node must not also advertise multiplex")
	}
	if !udpOverTCPEnabled(uotNode["udp_over_tcp"]) {
		t.Fatalf("uot node = %#v", uotNode)
	}
}