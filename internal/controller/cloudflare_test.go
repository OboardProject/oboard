package controller

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestDNSInboundTargets(t *testing.T) {
	server := model.Server{PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::10"}
	tests := []struct {
		name    string
		inbound model.Inbound
		want    []dnsRecordTarget
	}{
		{"server dual stack", model.Inbound{DNSDomain: "entry.example.com", DNSRecordTypes: "both"}, []dnsRecordTarget{{Type: "A", Content: "203.0.113.10"}, {Type: "AAAA", Content: "2001:db8::10"}}},
		{"custom ipv4", model.Inbound{DNSDomain: "entry.example.com", ExternalIP: "198.51.100.8", DNSRecordTypes: "a"}, []dnsRecordTarget{{Type: "A", Content: "198.51.100.8"}}},
		{"custom domain becomes cname", model.Inbound{DNSDomain: "entry.example.com", ExternalIP: "origin.example.net", DNSRecordTypes: "both"}, []dnsRecordTarget{{Type: "CNAME", Content: "origin.example.net"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dnsInboundTargets(server, tt.inbound)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("targets = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("target %d = %#v, want %#v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestNormalizeDNSZones(t *testing.T) {
	got, err := normalizeDNSZones("Example.COM, example.net\nexample.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "example.com" || got[1] != "example.net" {
		t.Fatalf("zones = %#v", got)
	}
	if _, err := normalizeDNSZones("not a domain"); err == nil {
		t.Fatal("invalid zone should fail")
	}
}

func TestSelectDNSCredentialZonePrefersBoundServer(t *testing.T) {
	serverID := int64(7)
	credential := model.DNSCredential{Zones: []model.DNSCredentialZone{
		{ID: 1, ZoneName: "example.com"},
		{ID: 2, ZoneName: "oboard.proxy"},
		{ID: 3, ZoneName: "oboard.proxy", ServerID: &serverID},
	}}
	zone, err := selectDNSCredentialZone(credential, "entry.oboard.proxy", serverID)
	if err != nil || zone.ID != 3 {
		t.Fatalf("bound zone=%#v err=%v", zone, err)
	}
	zone, err = selectDNSCredentialZone(credential, "www.example.com", 99)
	if err != nil || zone.ID != 1 {
		t.Fatalf("fallback zone=%#v err=%v", zone, err)
	}
}
