package core

import (
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestResolveProbeDDNSTargets(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	server := model.Server{PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::10"}
	iface := []model.NetworkInterfaceInfo{{
		Name: "eth0", Up: true, Running: true,
		Addresses: []string{"203.0.113.8/24", "2001:db8::8/64", "192.168.1.8/24", "100.64.0.8/32"},
	}}
	cases := []struct {
		name    string
		req     ProbeDDNSAddressRequest
		want    string
		wantErr string
	}{
		{
			name: "public both",
			req:  ProbeDDNSAddressRequest{Record: model.ProbeDDNSRecord{RecordTypes: "both"}, Server: server, Now: now},
			want: "A 203.0.113.10,AAAA 2001:db8::10",
		},
		{
			name: "public family filter",
			req:  ProbeDDNSAddressRequest{Record: model.ProbeDDNSRecord{RecordTypes: "both", AddressFamily: "ipv6"}, Server: server, Now: now},
			want: "AAAA 2001:db8::10",
		},
		{
			name:    "public private filtered",
			req:     ProbeDDNSAddressRequest{Record: model.ProbeDDNSRecord{RecordTypes: "a", ExcludePrivate: true}, Server: model.Server{PublicIPv4: "10.1.1.1"}, Now: now},
			wantErr: "排除内网地址",
		},
		{
			name: "bound public address",
			req: ProbeDDNSAddressRequest{
				Record:      model.ProbeDDNSRecord{RecordTypes: "a", AddressFamily: "ipv4", InterfaceName: "eth0", InterfaceAddress: "203.0.113.8/24", ExcludePrivate: true},
				Interfaces:  iface,
				InventoryAt: fresh,
				Now:         now,
			},
			want: "A 203.0.113.8",
		},
		{
			name: "bound private allowed when not filtered",
			req: ProbeDDNSAddressRequest{
				Record:      model.ProbeDDNSRecord{RecordTypes: "a", InterfaceName: "eth0", InterfaceAddress: "192.168.1.8/24"},
				Interfaces:  iface,
				InventoryAt: fresh,
				Now:         now,
			},
			want: "A 192.168.1.8",
		},
		{
			name: "cgnat is internal",
			req: ProbeDDNSAddressRequest{
				Record:      model.ProbeDDNSRecord{RecordTypes: "a", InterfaceName: "eth0", InterfaceAddress: "100.64.0.8/32", ExcludePrivate: true},
				Interfaces:  iface,
				InventoryAt: fresh,
				Now:         now,
			},
			wantErr: "内网地址",
		},
		{
			name: "missing address",
			req: ProbeDDNSAddressRequest{
				Record:      model.ProbeDDNSRecord{RecordTypes: "a", InterfaceName: "eth0", InterfaceAddress: "198.51.100.9/32"},
				Interfaces:  iface,
				InventoryAt: fresh,
				Now:         now,
			},
			wantErr: "已不在网卡",
		},
		{
			name: "stale inventory",
			req: ProbeDDNSAddressRequest{
				Record:      model.ProbeDDNSRecord{RecordTypes: "a", InterfaceName: "eth0", InterfaceAddress: "203.0.113.8/24"},
				Interfaces:  iface,
				InventoryAt: now.Add(-ProbeDDNSInventoryMaxAge - time.Second),
				Now:         now,
			},
			wantErr: "已过期",
		},
		{
			name: "no inventory",
			req: ProbeDDNSAddressRequest{
				Record: model.ProbeDDNSRecord{RecordTypes: "a", InterfaceName: "eth0", InterfaceAddress: "203.0.113.8/24"},
				Now:    now,
			},
			wantErr: "尚未读取",
		},
		{
			name: "down interface",
			req: ProbeDDNSAddressRequest{
				Record:      model.ProbeDDNSRecord{RecordTypes: "aaaa", InterfaceName: "eth1", InterfaceAddress: "2001:db8::9/64"},
				Interfaces:  []model.NetworkInterfaceInfo{{Name: "eth1", Up: false, Running: false, Addresses: []string{"2001:db8::9/64"}}},
				InventoryAt: fresh,
				Now:         now,
			},
			wantErr: "未启用",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveProbeDDNSTargets(tc.req)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(got))
			for i, target := range got {
				parts[i] = target.Type + " " + target.Content
			}
			if strings.Join(parts, ",") != tc.want {
				t.Fatalf("got %s", parts)
			}
		})
	}
}

func TestProbeDDNSPrefixSelection(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, types string
		addresses           []string
		want                string
		fail                bool
	}{
		{"carrier ipv4", "203.0.113.0/24", "a", []string{"198.51.100.8/24", "203.0.113.9/24"}, "203.0.113.9", false},
		{"changed ipv4", "203.0.113.0/24", "a", []string{"203.0.113.17/24"}, "203.0.113.17", false},
		{"ipv6", "2001:db8:1::/48", "aaaa", []string{"2001:db8:2::1/64", "2001:db8:1::4/64"}, "2001:db8:1::4", false},
		{"ambiguous", "203.0.113.0/24", "a", []string{"203.0.113.1/24", "203.0.113.2/24"}, "", true},
		{"missing", "203.0.113.0/24", "a", []string{"198.51.100.8/24"}, "", true},
		{"private", "10.0.0.0/8", "a", []string{"10.1.1.1/24"}, "", true},
		{"invalid", "carrier", "a", []string{"203.0.113.1/24"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveProbeDDNSTargets(ProbeDDNSAddressRequest{Record: model.ProbeDDNSRecord{InterfaceName: "eth0", InterfacePrefix: tc.prefix, RecordTypes: tc.types, ExcludePrivate: true}, Interfaces: []model.NetworkInterfaceInfo{{Name: "eth0", Up: true, Running: true, Addresses: tc.addresses}}, InventoryAt: time.Now()})
			if tc.fail {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil || len(got) != 1 || got[0].Content != tc.want {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}
