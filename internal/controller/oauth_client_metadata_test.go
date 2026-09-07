package controller

import (
	"context"
	"net/netip"
	"testing"
)

func TestResolvePublicMetadataHostRejectsForbiddenAddresses(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "100.64.0.1", "192.0.2.1"} {
		t.Run(host, func(t *testing.T) {
			if _, err := resolvePublicMetadataHost(context.Background(), host); err == nil {
				t.Fatalf("resolvePublicMetadataHost(%q) accepted a forbidden address", host)
			}
		})
	}
}

func TestDialPublicMetadataHostRejectsPrivateDestination(t *testing.T) {
	if _, err := dialPublicMetadataHost(context.Background(), "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("dialPublicMetadataHost accepted a private destination")
	}
}

func TestForbiddenClientMetadataIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "169.254.169.254", "2001:db8::1", "240.0.0.1"} {
		if !forbiddenClientMetadataIP(netip.MustParseAddr(raw)) {
			t.Fatalf("%s allowed", raw)
		}
	}
	if forbiddenClientMetadataIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("8.8.8.8 rejected")
	}
}

func TestParseClientMetadataURLRejectsSpecialPurposeLiteral(t *testing.T) {
	if _, err := parseClientMetadataURL("https://100.64.0.1/client.json"); err == nil {
		t.Fatal("CGNAT client_id accepted")
	}
	if _, err := parseClientMetadataURL("https://127.0.0.1/client.json"); err == nil {
		t.Fatal("loopback client_id accepted")
	}
}
